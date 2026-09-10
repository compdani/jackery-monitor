"""Per-device expected-load schedule + sleep window for the SOC forecast.

Persisted at /data/load_schedule.json:

{
  "SN": {
    "mode": "historical" | "scheduled",
    "sleep_start": "23:00" | null,
    "sleep_end": "07:00" | null,
    "windows": [
      {"id": "...", "label": "Evening", "start": "18:00", "end": "22:00",
       "watts": 800, "days": "all"|"weekday"|"weekend"}
    ]
  }
}
"""
from __future__ import annotations

import json
import logging
import os
import threading
import uuid
from datetime import datetime, timezone
from typing import Any

log = logging.getLogger("load_schedule")

PATH = os.environ.get("JACKERY_LOAD_SCHEDULE_FILE", "/data/load_schedule.json")
VALID_MODES = ("historical", "scheduled")
VALID_DAYS = ("all", "weekday", "weekend")

_lock = threading.Lock()


def parse_hhmm(s: Any) -> int | None:
    """Return minutes from midnight, or None."""
    if s is None or s == "":
        return None
    text = str(s).strip()
    parts = text.split(":")
    if len(parts) != 2:
        return None
    try:
        hh, mm = int(parts[0]), int(parts[1])
    except ValueError:
        return None
    if not (0 <= hh <= 23 and 0 <= mm <= 59):
        return None
    return hh * 60 + mm


def format_hhmm(minutes: int) -> str:
    minutes = int(minutes) % (24 * 60)
    return f"{minutes // 60:02d}:{minutes % 60:02d}"


def in_sleep_window(minute_of_day: int, sleep_start: str | None,
                    sleep_end: str | None) -> bool:
    """True if minute_of_day is inside [start, end), wrapping midnight."""
    a = parse_hhmm(sleep_start)
    b = parse_hhmm(sleep_end)
    if a is None or b is None:
        return False
    m = int(minute_of_day) % (24 * 60)
    if a == b:
        return False
    if a < b:
        return a <= m < b
    return m >= a or m < b


def window_covers(window: dict, hour: int, weekend: bool) -> bool:
    """Does this window apply to local hour `hour` (0-23)? End is exclusive."""
    days = window.get("days") or "all"
    if days == "weekday" and weekend:
        return False
    if days == "weekend" and not weekend:
        return False
    start = parse_hhmm(window.get("start"))
    end = parse_hhmm(window.get("end"))
    if start is None or end is None:
        return False
    # Hour bucket [H:00, H+1:00) is covered if H:00 is in [start, end).
    m = hour * 60
    if start == end:
        return False
    if start < end:
        return start <= m < end
    return m >= start or m < end


def scheduled_load_w(windows: list[dict], hour: int, weekend: bool) -> float:
    total = 0.0
    for w in windows or []:
        if window_covers(w, hour, weekend):
            try:
                total += max(0.0, float(w.get("watts") or 0))
            except (TypeError, ValueError):
                continue
    return total


def wall_clock(ts: int, utc_offset_seconds: int | None = None) -> datetime:
    if utc_offset_seconds is None:
        return datetime.fromtimestamp(int(ts))
    return datetime.fromtimestamp(
        int(ts) + int(utc_offset_seconds), tz=timezone.utc)


def hour_parts(ts: int, utc_offset_seconds: int | None = None
               ) -> tuple[int, bool, int]:
    """(hour, is_weekend, minute_of_day)."""
    d = wall_clock(ts, utc_offset_seconds)
    return d.hour, d.weekday() >= 5, d.hour * 60 + d.minute


def serialize_learned_profile(
    profile: dict[tuple[int, int], float],
) -> list[dict[str, Any]]:
    rows = []
    for (hour, weekend), watts in sorted(profile.items()):
        rows.append({
            "hour": int(hour),
            "weekend": bool(weekend),
            "watts": round(float(watts), 1),
        })
    return rows


def windows_from_learned_profile(
    profile: dict[tuple[int, int], float],
) -> list[dict[str, Any]]:
    """One 1-hour window per learned bucket (for 'copy into schedule')."""
    out = []
    for (hour, weekend), watts in sorted(profile.items()):
        h = int(hour) % 24
        out.append({
            "id": uuid.uuid4().hex[:8],
            "label": f"{h:02d}:00 {'weekend' if weekend else 'weekday'}",
            "start": f"{h:02d}:00",
            "end": f"{(h + 1) % 24:02d}:00",
            "watts": round(max(0.0, float(watts)), 1),
            "days": "weekend" if weekend else "weekday",
        })
    return out


def _default_device() -> dict[str, Any]:
    return {
        "mode": "historical",
        "sleep_start": None,
        "sleep_end": None,
        "windows": [],
    }


def _normalize_window(raw: Any) -> dict | None:
    if not isinstance(raw, dict):
        return None
    start = parse_hhmm(raw.get("start"))
    end = parse_hhmm(raw.get("end"))
    if start is None or end is None:
        return None
    try:
        watts = float(raw.get("watts") or 0)
    except (TypeError, ValueError):
        return None
    if watts < 0 or watts > 20000:
        return None
    days = raw.get("days") or "all"
    if days not in VALID_DAYS:
        days = "all"
    wid = str(raw.get("id") or uuid.uuid4().hex[:8])[:16]
    label = str(raw.get("label") or "").strip()[:80]
    return {
        "id": wid,
        "label": label,
        "start": format_hhmm(start),
        "end": format_hhmm(end),
        "watts": round(watts, 1),
        "days": days,
    }


def _normalize_device(raw: Any) -> dict[str, Any]:
    d = _default_device()
    if not isinstance(raw, dict):
        return d
    mode = raw.get("mode") or "historical"
    d["mode"] = mode if mode in VALID_MODES else "historical"
    ss = parse_hhmm(raw.get("sleep_start"))
    se = parse_hhmm(raw.get("sleep_end"))
    d["sleep_start"] = format_hhmm(ss) if ss is not None else None
    d["sleep_end"] = format_hhmm(se) if se is not None else None
    windows = []
    for w in raw.get("windows") or []:
        nw = _normalize_window(w)
        if nw:
            windows.append(nw)
    d["windows"] = windows
    return d


def _read_all() -> dict[str, Any]:
    try:
        with open(PATH) as f:
            data = json.load(f)
    except FileNotFoundError:
        return {}
    except Exception as e:
        log.warning("load_schedule unreadable: %s", e)
        return {}
    return data if isinstance(data, dict) else {}


def _write_all(data: dict) -> None:
    os.makedirs(os.path.dirname(PATH) or ".", exist_ok=True)
    tmp = PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, PATH)


def get(device_sn: str) -> dict[str, Any]:
    if not device_sn:
        return _default_device()
    with _lock:
        all_d = _read_all()
    return _normalize_device(all_d.get(device_sn))


def set(device_sn: str, payload: dict) -> dict[str, Any]:
    if not device_sn:
        raise ValueError("device_sn required")
    rec = _normalize_device(payload)
    with _lock:
        all_d = _read_all()
        all_d[device_sn] = rec
        _write_all(all_d)
    return rec
