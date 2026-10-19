"""Per-device expected-load schedule + sleep window for the SOC forecast.

Persisted at /data/load_schedule.json:

{
  "SN": {
    "mode": "historical" | "scheduled",
    "sleep_start": "23:00" | null,
    "sleep_end": "07:00" | null,
    "windows": [
      {
        "id": "...", "label": "Evening",
        "start": "18:00", "end": "22:00", "watts": 800,
        "kind": "once" | "weekly",
        "date": "YYYY-MM-DD" | null,   # once
        "weekdays": [0-6] | null       # weekly, Mon=0 … Sun=6
      }
    ]
  }
}

Legacy windows with days: all|weekday|weekend are migrated to kind=weekly
on read/normalize.
"""
from __future__ import annotations

import json
import logging
import os
import re
import threading
import uuid
from datetime import datetime, timezone
from typing import Any

log = logging.getLogger("load_schedule")

PATH = os.environ.get("JACKERY_LOAD_SCHEDULE_FILE", "/data/load_schedule.json")
VALID_MODES = ("historical", "scheduled")
VALID_KINDS = ("once", "weekly")
# Legacy days filter → weekdays (Mon=0 … Sun=6), matching datetime.weekday().
_LEGACY_DAYS_MAP = {
    "all": [0, 1, 2, 3, 4, 5, 6],
    "weekday": [0, 1, 2, 3, 4],
    "weekend": [5, 6],
}
_DATE_RE = re.compile(r"^(\d{4})-(\d{2})-(\d{2})$")

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


def parse_date(s: Any) -> str | None:
    if s is None or s == "":
        return None
    text = str(s).strip()[:10]
    m = _DATE_RE.match(text)
    if not m:
        return None
    y, mo, d = int(m.group(1)), int(m.group(2)), int(m.group(3))
    try:
        datetime(y, mo, d)
    except ValueError:
        return None
    return f"{y:04d}-{mo:02d}-{d:02d}"


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


def _hour_in_window(hour: int, start_min: int, end_min: int) -> bool:
    """Hour bucket [H:00, H+1:00) covered if H:00 is in [start, end)."""
    m = hour * 60
    if start_min == end_min:
        return False
    if start_min < end_min:
        return start_min <= m < end_min
    return m >= start_min or m < end_min


def _weekdays_list(raw: Any) -> list[int] | None:
    if not isinstance(raw, (list, tuple)):
        return None
    out: list[int] = []
    for x in raw:
        try:
            d = int(x)
        except (TypeError, ValueError):
            continue
        if 0 <= d <= 6 and d not in out:
            out.append(d)
    return sorted(out) if out else None


def window_covers(window: dict, hour: int, weekday: int,
                  date: str | None = None) -> bool:
    """Does this window apply at local (hour, weekday, date)?

    `weekday` is Mon=0 … Sun=6 (datetime.weekday()).
    """
    start = parse_hhmm(window.get("start"))
    end = parse_hhmm(window.get("end"))
    if start is None or end is None:
        return False
    if not _hour_in_window(int(hour), start, end):
        return False

    kind = window.get("kind")
    if kind not in VALID_KINDS:
        # Legacy days: all|weekday|weekend
        days = window.get("days") or "all"
        weekdays = _LEGACY_DAYS_MAP.get(days, _LEGACY_DAYS_MAP["all"])
        return int(weekday) in weekdays

    if kind == "once":
        wdate = parse_date(window.get("date"))
        if not wdate or not date:
            return False
        return wdate == date

    # weekly
    weekdays = _weekdays_list(window.get("weekdays"))
    if not weekdays:
        return False
    return int(weekday) in weekdays


def scheduled_load_w(
    windows: list[dict],
    hour: int,
    weekend: bool | None = None,
    *,
    weekday: int | None = None,
    date: str | None = None,
) -> float:
    """Sum watts for windows covering this local hour.

    Prefer `weekday` + optional `date`. If only `weekend` is passed
    (legacy callers/tests), map False→Mon, True→Sat.
    """
    if weekday is None:
        if weekend is None:
            weekday = 0
        else:
            weekday = 5 if weekend else 0
    total = 0.0
    for w in windows or []:
        if window_covers(w, hour, weekday, date):
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


def clock_parts(ts: int, utc_offset_seconds: int | None = None
                ) -> tuple[int, int, bool, int, str]:
    """(hour, weekday Mon=0, is_weekend, minute_of_day, YYYY-MM-DD)."""
    d = wall_clock(ts, utc_offset_seconds)
    # Naive local wall from offset uses tzinfo=utc but shifted clock —
    # date/hour/weekday are the local calendar parts we want.
    return (
        d.hour,
        d.weekday(),
        d.weekday() >= 5,
        d.hour * 60 + d.minute,
        f"{d.year:04d}-{d.month:02d}-{d.day:02d}",
    )


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
    """One 1-hour weekly window per learned bucket (copy into calendar)."""
    out = []
    for (hour, weekend), watts in sorted(profile.items()):
        h = int(hour) % 24
        weekdays = [5, 6] if weekend else [0, 1, 2, 3, 4]
        out.append({
            "id": uuid.uuid4().hex[:8],
            "label": f"{h:02d}:00 {'weekend' if weekend else 'weekday'}",
            "start": f"{h:02d}:00",
            "end": f"{(h + 1) % 24:02d}:00",
            "watts": round(max(0.0, float(watts)), 1),
            "kind": "weekly",
            "date": None,
            "weekdays": weekdays,
        })
    return out


def _default_device() -> dict[str, Any]:
    return {
        "mode": "scheduled",
        "sleep_start": None,
        "sleep_end": None,
        "windows": [],
    }


def _migrate_legacy_window(raw: dict) -> dict:
    """Convert days: all|weekday|weekend into kind/weekdays if needed."""
    if raw.get("kind") in VALID_KINDS:
        return raw
    days = raw.get("days") or "all"
    weekdays = list(_LEGACY_DAYS_MAP.get(days, _LEGACY_DAYS_MAP["all"]))
    migrated = dict(raw)
    migrated["kind"] = "weekly"
    migrated["weekdays"] = weekdays
    migrated["date"] = None
    return migrated


def _normalize_window(raw: Any) -> dict | None:
    if not isinstance(raw, dict):
        return None
    raw = _migrate_legacy_window(raw)
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

    kind = raw.get("kind") if raw.get("kind") in VALID_KINDS else "weekly"
    date = parse_date(raw.get("date")) if kind == "once" else None
    weekdays = _weekdays_list(raw.get("weekdays")) if kind == "weekly" else None

    if kind == "once" and not date:
        return None
    if kind == "weekly" and not weekdays:
        # Fallback: treat as all days if somehow empty after migrate
        weekdays = list(_LEGACY_DAYS_MAP["all"])

    wid = str(raw.get("id") or uuid.uuid4().hex[:8])[:16]
    label = str(raw.get("label") or "").strip()[:80]
    return {
        "id": wid,
        "label": label,
        "start": format_hhmm(start),
        "end": format_hhmm(end),
        "watts": round(watts, 1),
        "kind": kind,
        "date": date,
        "weekdays": weekdays,
    }


def _normalize_device(raw: Any) -> dict[str, Any]:
    d = _default_device()
    if not isinstance(raw, dict):
        return d
    mode = raw.get("mode") or "scheduled"
    d["mode"] = mode if mode in VALID_MODES else "scheduled"
    ss = parse_hhmm(raw.get("sleep_start"))
    se = parse_hhmm(raw.get("sleep_end"))
    d["sleep_start"] = format_hhmm(ss) if ss is not None else None
    d["sleep_end"] = format_hhmm(se) if se is not None else None
    windows = []
    # Use a dict (not set()) — this module exports set() which shadows builtins.
    seen_ids: dict[str, None] = {}
    for w in raw.get("windows") or []:
        nw = _normalize_window(w)
        if not nw:
            continue
        wid = nw["id"]
        if wid in seen_ids:
            wid = uuid.uuid4().hex[:8]
            nw["id"] = wid
        seen_ids[wid] = None
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
