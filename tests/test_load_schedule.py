"""Load schedule helpers: sleep window, scheduled watts, persistence."""
from __future__ import annotations

from datetime import datetime, timezone

import load_schedule as ls


def test_in_sleep_window_wraps_midnight():
    assert ls.in_sleep_window(23 * 60, "23:00", "07:00") is True
    assert ls.in_sleep_window(2 * 60, "23:00", "07:00") is True
    assert ls.in_sleep_window(7 * 60, "23:00", "07:00") is False
    assert ls.in_sleep_window(12 * 60, "23:00", "07:00") is False
    assert ls.in_sleep_window(18 * 60, None, "07:00") is False


def test_scheduled_load_sums_matching_windows():
    windows = [
        {"start": "18:00", "end": "22:00", "watts": 800, "days": "all"},
        {"start": "18:00", "end": "22:00", "watts": 200, "days": "weekday"},
    ]
    # Monday 18:00 UTC
    assert ls.scheduled_load_w(windows, 18, weekend=False) == 1000
    assert ls.scheduled_load_w(windows, 18, weekend=True) == 800
    assert ls.scheduled_load_w(windows, 22, weekend=False) == 0  # end exclusive
    assert ls.scheduled_load_w(windows, 12, weekend=False) == 0


def test_set_get_roundtrip(isolated_data, monkeypatch):
    import importlib
    monkeypatch.setenv("JACKERY_LOAD_SCHEDULE_FILE",
                       str(isolated_data / "load_schedule.json"))
    importlib.reload(ls)
    rec = ls.set("SN1", {
        "mode": "scheduled",
        "sleep_start": "23:00",
        "sleep_end": "07:00",
        "windows": [{"start": "18:00", "end": "22:00", "watts": 800, "days": "all"}],
    })
    assert rec["mode"] == "scheduled"
    got = ls.get("SN1")
    assert got["sleep_start"] == "23:00"
    assert got["windows"][0]["watts"] == 800
    assert ls.get("OTHER")["mode"] == "historical"


def test_hour_parts_with_offset():
    ts = int(datetime(2024, 7, 1, 18, 0, tzinfo=timezone.utc).timestamp())
    h, weekend, minute = ls.hour_parts(ts, 0)
    assert h == 18
    assert weekend is False
    assert minute == 18 * 60
