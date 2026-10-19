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
    # Legacy weekend flag still works (weekday→Mon, weekend→Sat)
    assert ls.scheduled_load_w(windows, 18, weekend=False) == 1000
    assert ls.scheduled_load_w(windows, 18, weekend=True) == 800
    assert ls.scheduled_load_w(windows, 22, weekend=False) == 0  # end exclusive
    assert ls.scheduled_load_w(windows, 12, weekend=False) == 0


def test_weekly_weekdays_and_once_date():
    windows = [
        {
            "kind": "weekly", "weekdays": [0, 2, 4],  # Mon Wed Fri
            "start": "09:00", "end": "11:00", "watts": 500,
        },
        {
            "kind": "once", "date": "2024-07-03",  # Wednesday
            "start": "09:00", "end": "11:00", "watts": 300,
        },
    ]
    # Wednesday 2024-07-03 → weekday 2: both weekly + once
    assert ls.scheduled_load_w(
        windows, 9, weekday=2, date="2024-07-03") == 800
    # Monday (no once): weekly only
    assert ls.scheduled_load_w(
        windows, 9, weekday=0, date="2024-07-01") == 500
    # Tuesday: neither
    assert ls.scheduled_load_w(
        windows, 9, weekday=1, date="2024-07-02") == 0
    # Wrong hour
    assert ls.scheduled_load_w(
        windows, 12, weekday=2, date="2024-07-03") == 0


def test_legacy_days_migrate_on_normalize(isolated_data, monkeypatch):
    import importlib
    monkeypatch.setenv("JACKERY_LOAD_SCHEDULE_FILE",
                       str(isolated_data / "load_schedule.json"))
    importlib.reload(ls)
    rec = ls.set("SN1", {
        "mode": "scheduled",
        "windows": [{"start": "18:00", "end": "22:00", "watts": 800, "days": "weekend"}],
    })
    w = rec["windows"][0]
    assert w["kind"] == "weekly"
    assert w["weekdays"] == [5, 6]
    assert w.get("days") is None or "days" not in w


def test_set_get_roundtrip(isolated_data, monkeypatch):
    import importlib
    monkeypatch.setenv("JACKERY_LOAD_SCHEDULE_FILE",
                       str(isolated_data / "load_schedule.json"))
    importlib.reload(ls)
    rec = ls.set("SN1", {
        "mode": "scheduled",
        "sleep_start": "23:00",
        "sleep_end": "07:00",
        "windows": [{
            "start": "18:00", "end": "22:00", "watts": 800,
            "kind": "weekly", "weekdays": [0, 1, 2, 3, 4, 5, 6],
        }],
    })
    assert rec["mode"] == "scheduled"
    got = ls.get("SN1")
    assert got["sleep_start"] == "23:00"
    assert got["windows"][0]["watts"] == 800
    assert got["windows"][0]["kind"] == "weekly"
    # Unknown device defaults to calendar (scheduled)
    assert ls.get("OTHER")["mode"] == "scheduled"


def test_hour_parts_with_offset():
    ts = int(datetime(2024, 7, 1, 18, 0, tzinfo=timezone.utc).timestamp())
    h, weekend, minute = ls.hour_parts(ts, 0)
    assert h == 18
    assert weekend is False
    assert minute == 18 * 60


def test_clock_parts_date_and_weekday():
    # 2024-07-01 is a Monday
    ts = int(datetime(2024, 7, 1, 18, 0, tzinfo=timezone.utc).timestamp())
    h, weekday, weekend, minute, date = ls.clock_parts(ts, 0)
    assert h == 18
    assert weekday == 0
    assert weekend is False
    assert date == "2024-07-01"
