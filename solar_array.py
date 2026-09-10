"""Site-level PV plane for Forecast.Solar (tilt / azimuth / kWp).

Lives at /data/solar_array.json. Lat/lon stay in location.json; this file
is only the plane geometry Forecast.Solar needs on the estimate URL.
Not encrypted — tilt and kWp are not PII.

`infer_plane` estimates the three fields from paired Jackery solar_w and
Open-Meteo GHI: kWp from peak / POA regression, tilt+azimuth from a
coarse grid search of which plane best matches the daily production curve.
"""
from __future__ import annotations

import json
import logging
import math
import os
import threading
from datetime import datetime, timezone
from typing import Any

log = logging.getLogger("solar_array")

PATH = os.environ.get("JACKERY_SOLAR_ARRAY_FILE", "/data/solar_array.json")

# Performance ratio: nameplate kWp * POA never shows up as AC-side solar_w
# 1:1 (inverter, soiling, temp, MPPT). 0.80 is a typical residential guess.
_INFER_PR = 0.80
_MIN_PAIRS = 12
_MIN_GHI = 80.0
_MIN_SOLAR_W = 50.0

_lock = threading.Lock()


def _validate(dec: Any, az: Any, kwp: Any) -> dict | None:
    try:
        declination = round(float(dec))
        azimuth = round(float(az))
        kwp_f = float(kwp)
    except (TypeError, ValueError):
        return None
    if not (0 <= declination <= 90):
        return None
    if not (-180 <= azimuth <= 180):
        return None
    if not (0.01 <= kwp_f <= 100.0):
        return None
    return {"declination": declination, "azimuth": azimuth, "kwp": round(kwp_f, 4)}


def get() -> dict | None:
    """Return {declination, azimuth, kwp} or None if unset / invalid."""
    with _lock:
        try:
            with open(PATH) as f:
                data = json.load(f)
        except FileNotFoundError:
            return None
        except Exception as e:
            log.warning("solar_array unreadable: %s", e)
            return None
    if not isinstance(data, dict):
        return None
    return _validate(data.get("declination"), data.get("azimuth"), data.get("kwp"))


def set(declination: Any, azimuth: Any, kwp: Any) -> dict | None:
    rec = _validate(declination, azimuth, kwp)
    if rec is None:
        return None
    os.makedirs(os.path.dirname(PATH) or ".", exist_ok=True)
    tmp = PATH + ".tmp"
    with _lock:
        with open(tmp, "w") as f:
            json.dump(rec, f)
        os.replace(tmp, PATH)
    log.info("solar array saved: dec=%s az=%s kwp=%s",
             rec["declination"], rec["azimuth"], rec["kwp"])
    return rec


def clear() -> bool:
    with _lock:
        try:
            os.remove(PATH)
            return True
        except FileNotFoundError:
            return True
        except Exception as e:
            log.warning("solar_array clear failed: %s", e)
            return False


# ---------- geometry inference ----------

def _wrap_deg(deg: float, lo: float = -180.0, hi: float = 180.0) -> float:
    span = hi - lo
    x = (deg - lo) % span + lo
    if x >= hi:
        x -= span
    return x


def solar_zenith_azimuth(lat_deg: float, lon_deg: float, ts: int
                         ) -> tuple[float, float]:
    """NOAA-style solar position.

    Returns (zenith_deg, azimuth_deg_from_north_clockwise).
    Azimuth 0 = north, 90 = east, 180 = south.
    """
    dt = datetime.fromtimestamp(int(ts), tz=timezone.utc)
    n = dt.timetuple().tm_yday
    hour = dt.hour + dt.minute / 60.0 + dt.second / 3600.0
    fy = 2.0 * math.pi / 365.0 * (n - 1 + (hour - 12.0) / 24.0)
    decl = (0.006918
            - 0.399912 * math.cos(fy) + 0.070257 * math.sin(fy)
            - 0.006758 * math.cos(2 * fy) + 0.000907 * math.sin(2 * fy)
            - 0.002697 * math.cos(3 * fy) + 0.00148 * math.sin(3 * fy))
    eqtime = 229.18 * (0.000075 + 0.001868 * math.cos(fy)
                       - 0.032077 * math.sin(fy)
                       - 0.014615 * math.cos(2 * fy)
                       - 0.040849 * math.sin(2 * fy))
    tst = hour * 60.0 + eqtime + 4.0 * lon_deg
    ha = math.radians((tst / 4.0) - 180.0)
    lat = math.radians(lat_deg)
    cos_zen = (math.sin(lat) * math.sin(decl)
               + math.cos(lat) * math.cos(decl) * math.cos(ha))
    cos_zen = max(-1.0, min(1.0, cos_zen))
    zen = math.acos(cos_zen)
    az = math.atan2(
        -math.sin(ha),
        math.cos(ha) * math.sin(lat) - math.tan(decl) * math.cos(lat),
    )
    az_deg = (math.degrees(az) + 180.0) % 360.0
    return math.degrees(zen), az_deg


def _fs_sun_azimuth(az_from_north: float) -> float:
    """Convert sun azimuth (0=north) to Forecast.Solar (0=south, -90=east)."""
    return _wrap_deg(az_from_north - 180.0)


def poa_from_ghi(ghi: float, lat: float, lon: float, ts: int,
                 tilt_deg: float, az_fs: float) -> float:
    """Transpose GHI onto a tilted plane (all-GHI-as-beam approximation).

    Shape vs hour is what the grid search needs; absolute scale is
    absorbed into the kWp least-squares fit.
    """
    if ghi <= 0:
        return 0.0
    zen_deg, az_n = solar_zenith_azimuth(lat, lon, ts)
    if zen_deg >= 87.0:
        return 0.0
    zen = math.radians(zen_deg)
    tilt = math.radians(tilt_deg)
    daz = math.radians(_wrap_deg(az_fs - _fs_sun_azimuth(az_n)))
    cos_aoi = (math.cos(zen) * math.cos(tilt)
               + math.sin(zen) * math.sin(tilt) * math.cos(daz))
    if cos_aoi <= 0:
        return 0.0
    return float(ghi) * cos_aoi / max(math.cos(zen), 0.12)


def _pearson(xs: list[float], ys: list[float]) -> float:
    n = len(xs)
    if n < 3:
        return 0.0
    mx = sum(xs) / n
    my = sum(ys) / n
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys, strict=False))
    dx = sum((x - mx) ** 2 for x in xs)
    dy = sum((y - my) ** 2 for y in ys)
    if dx <= 0 or dy <= 0:
        return 0.0
    return num / math.sqrt(dx * dy)


def _pair_hours(
    energy_history: list[dict[str, Any]],
    weather_hourly: list[dict[str, Any]],
) -> list[tuple[int, float, float]]:
    """[(hour_ts, ghi, solar_w), ...] daylight pairs."""
    by_hour_solar: dict[int, float] = {}
    for row in energy_history:
        ts = int(row.get("ts") or 0)
        if ts <= 0:
            continue
        h = (ts // 3600) * 3600
        sol = float(row.get("solar_w") or 0)
        if sol > by_hour_solar.get(h, 0.0):
            by_hour_solar[h] = sol
    pairs = []
    for w in weather_hourly:
        ts = int(w.get("ts") or 0)
        if ts <= 0:
            continue
        h = (ts // 3600) * 3600
        ghi = float(w.get("ghi_w_m2") or 0)
        if ghi < _MIN_GHI:
            continue
        sol = by_hour_solar.get(h)
        if sol is None or sol < _MIN_SOLAR_W:
            continue
        pairs.append((h, ghi, sol))
    return pairs


def infer_plane(
    energy_history: list[dict[str, Any]],
    weather_hourly: list[dict[str, Any]],
    lat: float,
    lon: float,
) -> dict[str, Any]:
    """Estimate tilt / azimuth / kWp from observed solar vs GHI.

    Does not write disk. Returns a dict ready to pass to `set()`, plus
    `n_samples`, `correlation`, `confidence`, and `notes`.
    """
    pairs = _pair_hours(energy_history, weather_hourly)
    peak = max((p[2] for p in pairs), default=0.0)
    fallback_tilt = int(round(min(60.0, max(0.0, abs(float(lat))))))
    fallback_az = 0 if lat >= 0 else 180
    fallback_kwp = round(max(0.05, peak / 850.0), 2) if peak > 0 else None

    if len(pairs) < _MIN_PAIRS or peak < _MIN_SOLAR_W:
        if fallback_kwp is None:
            return {
                "error": "not enough solar history to infer the array "
                         "(need ~12 daylight hours of production + GHI)",
                "n_samples": len(pairs),
            }
        rec = _validate(fallback_tilt, fallback_az, fallback_kwp)
        return {
            **(rec or {}),
            "n_samples": len(pairs),
            "correlation": 0.0,
            "confidence": "low",
            "notes": "Too few paired hours for a geometry search; "
                     "tilt≈latitude and kWp from peak watts / 850.",
        }

    best: tuple[float, int, int] | None = None  # (corr, tilt, az)
    tilts = list(range(0, 65, 5))
    azimuths = list(range(-90, 91, 15))
    if lat < 0:
        azimuths = list(range(90, 181, 15)) + list(range(-180, -89, 15))
    for tilt in tilts:
        for az in azimuths:
            poas = [poa_from_ghi(ghi, lat, lon, ts, tilt, az)
                    for ts, ghi, _sol in pairs]
            if max(poas) <= 1:
                continue
            c = _pearson(poas, [sol for _ts, _ghi, sol in pairs])
            if best is None or c > best[0]:
                best = (c, tilt, az)

    if best is None:
        rec = _validate(fallback_tilt, fallback_az, fallback_kwp or 0.05)
        return {
            **(rec or {}),
            "n_samples": len(pairs),
            "correlation": 0.0,
            "confidence": "low",
            "notes": "Geometry search found no sun-up hours; used latitude fallback.",
        }

    corr, tilt, az = best
    poas = [poa_from_ghi(ghi, lat, lon, ts, tilt, az)
            for ts, ghi, _sol in pairs]
    sols = [sol for _ts, _ghi, sol in pairs]
    sxx = sum(p * p for p in poas)
    sxy = sum(p * s for p, s in zip(poas, sols, strict=False))
    kwp_fit = (sxy / sxx / _INFER_PR) if sxx > 0 else (fallback_kwp or 0.05)
    # Nameplate is usually a bit above observed peak / 1000.
    kwp_peak = peak / 850.0 if peak else kwp_fit
    kwp = max(0.05, min(50.0, 0.5 * kwp_fit + 0.5 * kwp_peak))
    rec = _validate(tilt, az, round(kwp, 2))
    if corr >= 0.85:
        conf = "high"
    elif corr >= 0.6:
        conf = "medium"
    else:
        conf = "low"
    notes = (
        f"Best match among tilt 0–60° and azimuth −90…90 "
        f"(0=south): r={corr:.2f} on {len(pairs)} daylight hours. "
        "Review before saving — shade and MPPT clipping can skew the fit."
    )
    if lat < 0:
        notes = notes.replace("−90…90", "north-facing 90…180")
    return {
        **(rec or {}),
        "n_samples": len(pairs),
        "correlation": round(corr, 3),
        "confidence": conf,
        "peak_solar_w": round(peak, 1),
        "notes": notes,
    }
