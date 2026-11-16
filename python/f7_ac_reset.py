"""Per-device F7 (ec=8) AC-pulse workaround for Explorer 2000 Plus.

Morning low-PV can latch the solar inverter. A short AC pulse (ON → 5s
→ OFF) clears it. Opt-in per serial number; never interrupts an inverter
that is already on. Daylight is clock-based (manual window or weather
GHI), not live solar watts — the fault happens at low/zero PV.

Config lives at /data/f7_ac_reset.json:

{
  "by_device": {
    "SN": {
      "enabled": false,
      "error_code": 8,
      "cooldown_min": 30,
      "pulse_s": 5,
      "day_start": null,
      "day_end": null,
      "last_cycle_ts": 0,
      "events": []
    }
  }
}
"""
from __future__ import annotations

import json
import logging
import os
import threading
import time
from typing import Any, Literal

import load_schedule as _load_sched

log = logging.getLogger("f7_ac_reset")

PATH = os.environ.get("JACKERY_F7_AC_RESET_FILE", "/data/f7_ac_reset.json")
EVENTS_MAX = 50
F7_ERROR_CODE = 8

Action = Literal[
    "idle",
    "cycle",
    "skip_disabled",
    "skip_ac_on",
    "skip_night",
    "skip_cooldown",
]

DEFAULT_CONFIG: dict[str, Any] = {
    "enabled": False,
    "error_code": F7_ERROR_CODE,
    "cooldown_min": 30,
    "pulse_s": 5,
    "day_start": None,
    "day_end": None,
}

_lock = threading.Lock()


def _load_raw() -> dict[str, Any]:
    try:
        with open(PATH) as f:
            data = json.load(f)
        return data if isinstance(data, dict) else {}
    except FileNotFoundError:
        return {}
    except Exception as e:
        log.warning("f7_ac_reset config unreadable (%s); using defaults", e)
        return {}


def _save_raw(data: dict[str, Any]) -> None:
    os.makedirs(os.path.dirname(PATH) or ".", exist_ok=True)
    tmp = PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, PATH)


def _hhmm_or_none(raw: Any) -> str | None:
    if raw is None or raw == "":
        return None
    minutes = _load_sched.parse_hhmm(raw)
    if minutes is None:
        return None
    return _load_sched.format_hhmm(minutes)


def _validate_config(cfg: dict[str, Any]) -> dict[str, Any]:
    out = dict(DEFAULT_CONFIG)
    if not isinstance(cfg, dict):
        return out
    out["enabled"] = bool(cfg.get("enabled"))
    try:
        ec = int(cfg.get("error_code") if cfg.get("error_code") is not None
                 else F7_ERROR_CODE)
        if 1 <= ec <= 999:
            out["error_code"] = ec
    except (TypeError, ValueError):
        pass
    try:
        cd = int(cfg.get("cooldown_min") if cfg.get("cooldown_min") is not None
                 else 30)
        if 1 <= cd <= 24 * 60:
            out["cooldown_min"] = cd
    except (TypeError, ValueError):
        pass
    try:
        pulse = int(cfg.get("pulse_s") if cfg.get("pulse_s") is not None else 5)
        if 1 <= pulse <= 60:
            out["pulse_s"] = pulse
    except (TypeError, ValueError):
        pass
    out["day_start"] = _hhmm_or_none(cfg.get("day_start"))
    out["day_end"] = _hhmm_or_none(cfg.get("day_end"))
    if out["day_start"] is None or out["day_end"] is None:
        out["day_start"] = None
        out["day_end"] = None
    return out


def _runtime_fields(raw: dict[str, Any] | None) -> dict[str, Any]:
    raw = raw if isinstance(raw, dict) else {}
    try:
        last = float(raw.get("last_cycle_ts") or 0)
    except (TypeError, ValueError):
        last = 0.0
    events = raw.get("events") if isinstance(raw.get("events"), list) else []
    clean_events = []
    for e in events[-EVENTS_MAX:]:
        if isinstance(e, dict) and e.get("message"):
            clean_events.append(e)
    return {"last_cycle_ts": last, "events": clean_events}


def _device_blob(raw_device: dict[str, Any] | None) -> dict[str, Any]:
    cfg = _validate_config(raw_device or {})
    cfg.update(_runtime_fields(raw_device))
    return cfg


def get_config(device_sn: str | None = None) -> dict[str, Any]:
    with _lock:
        data = _load_raw()
    by_dev = data.get("by_device") if isinstance(data.get("by_device"), dict) else {}
    if not device_sn:
        return _device_blob({})
    return _device_blob(by_dev.get(device_sn))


def set_config(cfg: dict[str, Any], device_sn: str | None = None) -> dict[str, Any]:
    """Merge UI fields onto the stored record; keep last_cycle_ts + events."""
    if not device_sn:
        raise ValueError("device_sn required")
    with _lock:
        data = _load_raw()
        if not isinstance(data.get("by_device"), dict):
            data = {"by_device": {}}
        prior = data["by_device"].get(device_sn) or {}
        merged = _validate_config(prior)
        merged.update(cfg or {})
        validated = _validate_config(merged)
        runtime = _runtime_fields(prior)
        blob = {**validated, **runtime}
        data["by_device"][device_sn] = blob
        _save_raw(data)
    log.info("f7_ac_reset config saved: device=%s enabled=%s cooldown=%dmin",
             device_sn, validated["enabled"], validated["cooldown_min"])
    return blob


def stamp_cycle(device_sn: str, ts: float | None = None) -> None:
    now = float(ts if ts is not None else time.time())
    with _lock:
        data = _load_raw()
        if not isinstance(data.get("by_device"), dict):
            data = {"by_device": {}}
        rec = _device_blob(data["by_device"].get(device_sn))
        rec["last_cycle_ts"] = now
        data["by_device"][device_sn] = rec
        _save_raw(data)


def record_event(device_sn: str, level: str, message: str, **extra) -> dict[str, Any]:
    """Append one dashboard-shaped event and mirror to the process logger."""
    e = {
        "ts": time.time(),
        "level": level if level in ("info", "warn", "error") else "info",
        "category": "f7",
        "message": message,
    }
    payload = {"device_sn": device_sn, **extra}
    e["extra"] = payload
    with _lock:
        data = _load_raw()
        if not isinstance(data.get("by_device"), dict):
            data = {"by_device": {}}
        rec = _device_blob(data["by_device"].get(device_sn))
        events = list(rec.get("events") or [])
        events.append(e)
        rec["events"] = events[-EVENTS_MAX:]
        data["by_device"][device_sn] = rec
        _save_raw(data)
    suffix = f" {payload}" if payload else ""
    if e["level"] == "error":
        log.error("%s: %s%s", e["category"], message, suffix)
    elif e["level"] == "warn":
        log.warning("%s: %s%s", e["category"], message, suffix)
    else:
        log.info("%s: %s%s", e["category"], message, suffix)
    return e


def list_events(since: float = 0.0, limit: int = 200) -> list[dict[str, Any]]:
    """All persisted F7 events across devices, oldest first then trimmed."""
    with _lock:
        data = _load_raw()
    by_dev = data.get("by_device") if isinstance(data.get("by_device"), dict) else {}
    out: list[dict[str, Any]] = []
    for rec in by_dev.values():
        if not isinstance(rec, dict):
            continue
        for e in rec.get("events") or []:
            if not isinstance(e, dict):
                continue
            try:
                ts = float(e.get("ts") or 0)
            except (TypeError, ValueError):
                continue
            if since and ts <= since:
                continue
            out.append(e)
    out.sort(key=lambda row: float(row.get("ts") or 0))
    if limit and len(out) > limit:
        out = out[-limit:]
    return out


def in_manual_daylight(now_ts: float, day_start: str | None, day_end: str | None,
                       tz_offset_s: int = 0) -> bool | None:
    """True/False if both HH:MM set; None means fall through to weather."""
    if not day_start or not day_end:
        return None
    local = int(now_ts) + int(tz_offset_s)
    minute = ((local % 86400) // 60)
    return _load_sched.in_sleep_window(minute, day_start, day_end)


def in_weather_daylight(now_ts: float, weather_hourly: list[dict] | None) -> bool:
    """True if the current UTC hour has GHI > 0. Fail closed if unknown."""
    if not weather_hourly:
        return False
    hour_ts = int(now_ts) - (int(now_ts) % 3600)
    best = None
    best_dt = None
    for w in weather_hourly:
        try:
            ts = int(w.get("ts") or 0)
        except (TypeError, ValueError):
            continue
        dt = abs(ts - hour_ts)
        if best_dt is None or dt < best_dt:
            best_dt = dt
            best = w
    if best is None or best_dt is None or best_dt > 3600:
        return False
    try:
        return float(best.get("ghi_w_m2") or 0) > 0
    except (TypeError, ValueError):
        return False


def is_daylight(now_ts: float, cfg: dict[str, Any], *,
                tz_offset_s: int = 0,
                weather_hourly: list[dict] | None = None) -> bool:
    manual = in_manual_daylight(
        now_ts, cfg.get("day_start"), cfg.get("day_end"), tz_offset_s)
    if manual is not None:
        return manual
    return in_weather_daylight(now_ts, weather_hourly)


def evaluate(
    cfg: dict[str, Any],
    telemetry: dict[str, Any] | None,
    *,
    now: float | None = None,
    in_daylight: bool = False,
) -> Action:
    """Pure gate. Does not mutate cfg or issue commands."""
    now_ts = float(now if now is not None else time.time())
    if not cfg or not cfg.get("enabled"):
        return "skip_disabled"
    tele = telemetry or {}
    if bool(tele.get("ac_on")):
        return "skip_ac_on"
    try:
        ec = int(tele.get("error_code") or 0)
    except (TypeError, ValueError):
        ec = 0
    try:
        want = int(cfg.get("error_code") or F7_ERROR_CODE)
    except (TypeError, ValueError):
        want = F7_ERROR_CODE
    if ec != want:
        return "idle"
    if not in_daylight:
        return "skip_night"
    try:
        last = float(cfg.get("last_cycle_ts") or 0)
    except (TypeError, ValueError):
        last = 0.0
    try:
        cooldown_s = int(cfg.get("cooldown_min") or 30) * 60
    except (TypeError, ValueError):
        cooldown_s = 30 * 60
    if last and (now_ts - last) < cooldown_s:
        return "skip_cooldown"
    return "cycle"
