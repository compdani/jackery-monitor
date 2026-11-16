"""Forecast.Solar estimate client.

Docs: https://doc.forecast.solar/doku.php?id=api:estimate

Public:
  https://api.forecast.solar/estimate/:lat/:lon/:dec/:az/:kwp
With API key:
  https://api.forecast.solar/:apikey/estimate/:lat/:lon/:dec/:az/:kwp

Uses result.watts (period-average W), resampled to hourly means. Cache is
site-level with a 1h TTL so the per-device forecast recorder cannot 429
the public 12-calls/IP limit. On 429 we honor retry-at and do not hit
the API until then.

Optional API key is encrypted at rest like anthropic_creds.
"""
from __future__ import annotations

import json
import logging
import os
import threading
import time
from datetime import datetime, timezone
from typing import Any

import httpx

import crypto_util

log = logging.getLogger("forecast_solar")

API_HOST = "https://api.forecast.solar"
CACHE_TTL_S = 3600
CREDS_PATH = os.environ.get(
    "JACKERY_FORECAST_SOLAR_CREDS_FILE", "/data/forecast-solar-creds.json")

_cache: dict[str, tuple[float, dict]] = {}
_cache_lock = threading.Lock()
_retry_until = 0.0


def has_key() -> bool:
    try:
        return os.path.getsize(CREDS_PATH) > 0
    except OSError:
        return False


def load_key() -> str | None:
    try:
        with open(CREDS_PATH) as f:
            blob = json.load(f)
    except FileNotFoundError:
        return None
    except Exception as e:
        log.warning("forecast.solar creds unreadable: %s", e)
        return None
    if not isinstance(blob, dict) or "ct" not in blob:
        return None
    pt = crypto_util.decrypt(blob)
    if pt is None:
        return None
    try:
        d = json.loads(pt.decode())
    except Exception as e:
        log.error("forecast.solar creds invalid JSON after decrypt: %s", e)
        return None
    key = d.get("api_key")
    return str(key).strip() if key else None


def save_key(api_key: str) -> bool:
    payload = json.dumps({"api_key": api_key.strip()}).encode()
    blob = crypto_util.encrypt(payload)
    try:
        os.makedirs(os.path.dirname(CREDS_PATH) or ".", exist_ok=True)
        tmp = CREDS_PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump(blob, f)
        try:
            os.chmod(tmp, 0o600)
        except Exception:
            pass
        os.replace(tmp, CREDS_PATH)
        return True
    except Exception as e:
        log.error("failed to save forecast.solar key: %s", e)
        return False


def clear_key() -> bool:
    try:
        os.remove(CREDS_PATH)
        return True
    except FileNotFoundError:
        return True
    except Exception as e:
        log.error("failed to delete forecast.solar key: %s", e)
        return False


def estimate_url(lat: float, lon: float, declination: int, azimuth: int,
                 kwp: float, api_key: str | None = None) -> str:
    """Build the estimate URL. Pure — used by tests."""
    path = (f"estimate/{lat:.4f}/{lon:.4f}/"
            f"{int(declination)}/{int(azimuth)}/{kwp:g}")
    key = (api_key or "").strip()
    if key:
        return f"{API_HOST}/{key}/{path}"
    return f"{API_HOST}/{path}"


def parse_fs_timestamp(s: str, utc_offset_seconds: int = 0) -> int | None:
    """Parse Forecast.Solar local wall-clock key → unix seconds."""
    try:
        dt = datetime.strptime(str(s).strip(), "%Y-%m-%d %H:%M:%S")
    except (TypeError, ValueError):
        return None
    # Treat the naive stamp as local wall time at utc_offset_seconds.
    as_utc = dt.replace(tzinfo=timezone.utc).timestamp()
    return int(as_utc) - int(utc_offset_seconds)


def resample_watts_hourly(
    watts: dict[str, Any],
    utc_offset_seconds: int = 0,
) -> dict[int, float]:
    """Mean W per UTC hour from result.watts {local-datetime: watts}."""
    buckets: dict[int, list[float]] = {}
    for key, val in (watts or {}).items():
        try:
            w = float(val)
        except (TypeError, ValueError):
            continue
        ts = parse_fs_timestamp(key, utc_offset_seconds)
        if ts is None:
            continue
        hour_ts = ts - (ts % 3600)
        buckets.setdefault(hour_ts, []).append(w)
    return {h: sum(vs) / len(vs) for h, vs in buckets.items()}


def _cache_key(lat: float, lon: float, dec: int, az: int, kwp: float) -> str:
    return f"{round(lat, 4)}:{round(lon, 4)}:{dec}:{az}:{kwp:g}"


def clear_cache() -> None:
    global _retry_until
    with _cache_lock:
        _cache.clear()
        _retry_until = 0.0


def _parse_retry_at(resp: httpx.Response, body: dict | None) -> float:
    """Unix time until which we must not call again. Default +1h."""
    now = time.time()
    header = resp.headers.get("X-Ratelimit-Retry-At") or ""
    msg = (body or {}).get("message") or {}
    rl = msg.get("ratelimit") if isinstance(msg, dict) else {}
    raw = ""
    if isinstance(rl, dict):
        raw = str(rl.get("retry-at") or "")
    raw = raw or header
    if raw:
        try:
            # 2026-02-17T22:19:59+01:00
            dt = datetime.fromisoformat(raw.replace("Z", "+00:00"))
            return dt.timestamp()
        except ValueError:
            pass
    return now + CACHE_TTL_S


async def fetch_estimate(
    lat: float,
    lon: float,
    declination: int,
    azimuth: int,
    kwp: float,
    *,
    utc_offset_seconds: int = 0,
    api_key: str | None = None,
) -> dict[str, Any]:
    """Return {hourly: [{ts, watts}], fetched_at, source} or {error, ...}.

    Missing plane / 429 / network → error (caller falls back to Open-Meteo).
    Stale cache is returned on failure when we have one.
    """
    global _retry_until
    now = time.time()
    key = _cache_key(lat, lon, declination, azimuth, kwp)

    with _cache_lock:
        cached = _cache.get(key)
        if cached and cached[0] > now:
            return cached[1]
        if now < _retry_until:
            if cached and cached[1].get("hourly"):
                data = dict(cached[1])
                data["stale"] = True
                data["error"] = "rate limited (retry-at honored)"
                return data
            return {"hourly": [], "fetched_at": now,
                    "error": "rate limited (retry-at honored)"}

    url = estimate_url(lat, lon, declination, azimuth, kwp, api_key)
    body: dict | None = None
    async with httpx.AsyncClient(timeout=15.0) as client:
        try:
            r = await client.get(url, headers={"Accept": "application/json"})
            try:
                body = r.json()
            except Exception:
                body = None
            if r.status_code == 429:
                until = _parse_retry_at(r, body)
                with _cache_lock:
                    _retry_until = until
                    stale = _cache.get(key)
                log.warning("Forecast.Solar 429; retry after %s", until)
                if stale and stale[1].get("hourly"):
                    data = dict(stale[1])
                    data["stale"] = True
                    data["error"] = "rate limited"
                    return data
                return {"hourly": [], "fetched_at": now, "error": "rate limited"}
            r.raise_for_status()
            if not isinstance(body, dict):
                raise ValueError("non-json Forecast.Solar body")
        except Exception as e:
            msg = str(e) or f"{type(e).__module__}.{type(e).__name__}"
            log.warning("Forecast.Solar fetch failed: %s", msg)
            with _cache_lock:
                stale = _cache.get(key)
            if stale and stale[1].get("hourly"):
                data = dict(stale[1])
                data["stale"] = True
                data["stale_error"] = msg
                return data
            return {"hourly": [], "fetched_at": now, "error": msg}

    watts = ((body or {}).get("result") or {}).get("watts") or {}
    hourly_map = resample_watts_hourly(watts, utc_offset_seconds)
    rows = [{"ts": ts, "watts": w} for ts, w in sorted(hourly_map.items())]
    out = {
        "hourly": rows,
        "fetched_at": now,
        "lat": lat, "lon": lon,
        "declination": declination, "azimuth": azimuth, "kwp": kwp,
        "source": "forecast_solar",
    }
    with _cache_lock:
        _cache[key] = (now + CACHE_TTL_S, out)
    log.info("Forecast.Solar: %d hourly watts for (%.4f,%.4f) dec=%s az=%s kwp=%s",
             len(rows), lat, lon, declination, azimuth, kwp)
    return out
