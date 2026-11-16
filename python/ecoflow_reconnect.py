"""EcoFlow silent-session auto-reconnect settings.

Manual POST /api/ecoflow/reconnect always works. Auto force-restart of
a connected-but-silent MQTT session only runs when auto_reconnect is on
and the current local time falls inside [reconnect_start, reconnect_end)
(or any time if both ends are unset).

Config at /data/ecoflow_reconnect.json:

    {
      "auto_reconnect": true,
      "reconnect_start": "06:00",
      "reconnect_end": "22:00"
    }
"""
from __future__ import annotations

import json
import logging
import os
import threading
import time
from typing import Any

import load_schedule as _load_sched

log = logging.getLogger("ecoflow_reconnect")

DEFAULTS: dict[str, Any] = {
    "auto_reconnect": True,
    "reconnect_start": None,
    "reconnect_end": None,
}

_lock = threading.Lock()


def path() -> str:
    return os.environ.get(
        "JACKERY_ECOFLOW_RECONNECT_FILE", "/data/ecoflow_reconnect.json",
    )


# Alias so older tests can monkeypatch ``PATH``; prefer ``path()`` / env.
PATH = "/data/ecoflow_reconnect.json"


def _active_path() -> str:
    # Prefer env (set by test fixtures). Fall back to module PATH if a
    # test monkeypatched it away from the default.
    env = os.environ.get("JACKERY_ECOFLOW_RECONNECT_FILE")
    if env:
        return env
    return PATH


def _load_raw() -> dict[str, Any]:
    try:
        with open(_active_path()) as f:
            data = json.load(f)
        return data if isinstance(data, dict) else {}
    except FileNotFoundError:
        return {}
    except Exception as e:
        log.warning("ecoflow_reconnect unreadable (%s); using defaults", e)
        return {}


def _save_raw(data: dict[str, Any]) -> None:
    p = _active_path()
    os.makedirs(os.path.dirname(p) or ".", exist_ok=True)
    tmp = p + ".tmp"
    with open(tmp, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, p)


def _clean(raw: dict[str, Any] | None) -> dict[str, Any]:
    src = raw if isinstance(raw, dict) else {}
    start = src.get("reconnect_start")
    end = src.get("reconnect_end")
    start_s = None
    end_s = None
    if start not in (None, ""):
        mins = _load_sched.parse_hhmm(start)
        if mins is None:
            raise ValueError("reconnect_start must be HH:MM")
        start_s = _load_sched.format_hhmm(mins)
    if end not in (None, ""):
        mins = _load_sched.parse_hhmm(end)
        if mins is None:
            raise ValueError("reconnect_end must be HH:MM")
        end_s = _load_sched.format_hhmm(mins)
    if (start_s is None) ^ (end_s is None):
        raise ValueError(
            "reconnect_start and reconnect_end must both be set or both cleared",
        )
    auto = src.get("auto_reconnect")
    if auto is None:
        auto = DEFAULTS["auto_reconnect"]
    return {
        "auto_reconnect": bool(auto),
        "reconnect_start": start_s,
        "reconnect_end": end_s,
    }


def get() -> dict[str, Any]:
    with _lock:
        try:
            return _clean({**DEFAULTS, **_load_raw()})
        except ValueError:
            return dict(DEFAULTS)


def set_config(payload: dict[str, Any] | None) -> dict[str, Any]:
    with _lock:
        cur = {**DEFAULTS, **_load_raw()}
        patch = payload if isinstance(payload, dict) else {}
        merged = {
            **cur,
            **{
                k: patch[k]
                for k in ("auto_reconnect", "reconnect_start", "reconnect_end")
                if k in patch
            },
        }
        cleaned = _clean(merged)
        _save_raw(cleaned)
        return cleaned


def in_allowed_window(now_ts: float | None = None, *, tz_offset_s: int = 0) -> bool:
    """True when auto force-reconnect may run (ignores auto_reconnect flag)."""
    cfg = get()
    start, end = cfg.get("reconnect_start"), cfg.get("reconnect_end")
    if not start or not end:
        return True
    ts = time.time() if now_ts is None else float(now_ts)
    local = int(ts) + int(tz_offset_s)
    minute = (local % 86400) // 60
    return _load_sched.in_sleep_window(minute, start, end)


def auto_reconnect_allowed(now_ts: float | None = None, *, tz_offset_s: int = 0) -> bool:
    cfg = get()
    if not cfg.get("auto_reconnect"):
        return False
    return in_allowed_window(now_ts, tz_offset_s=tz_offset_s)
