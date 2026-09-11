"""F7 AC-reset gates: disabled, AC on, night, cooldown, pulse."""
from __future__ import annotations

import importlib

import f7_ac_reset as f7


def _cfg(**kw):
    base = {
        "enabled": True,
        "error_code": 8,
        "cooldown_min": 30,
        "pulse_s": 5,
        "last_cycle_ts": 0,
    }
    base.update(kw)
    return base


def _tele(**kw):
    base = {"ac_on": False, "error_code": 8}
    base.update(kw)
    return base


def test_evaluate_disabled():
    assert f7.evaluate(_cfg(enabled=False), _tele(), in_daylight=True) == "skip_disabled"


def test_evaluate_skips_when_ac_already_on():
    assert f7.evaluate(
        _cfg(), _tele(ac_on=True), in_daylight=True) == "skip_ac_on"


def test_evaluate_idle_wrong_error_code():
    assert f7.evaluate(
        _cfg(), _tele(error_code=0), in_daylight=True) == "idle"


def test_evaluate_skips_night():
    assert f7.evaluate(_cfg(), _tele(), in_daylight=False) == "skip_night"


def test_evaluate_skips_cooldown():
    now = 1_700_000_000
    action = f7.evaluate(
        _cfg(last_cycle_ts=now - 60), _tele(), now=now, in_daylight=True)
    assert action == "skip_cooldown"


def test_evaluate_cycle_when_f7_daylight_ac_off():
    now = 1_700_000_000
    action = f7.evaluate(
        _cfg(last_cycle_ts=now - 31 * 60), _tele(), now=now, in_daylight=True)
    assert action == "cycle"


def test_in_weather_daylight_ghi():
    now = 1_700_000_000
    hour = now - (now % 3600)
    assert f7.in_weather_daylight(now, [{"ts": hour, "ghi_w_m2": 12}]) is True
    assert f7.in_weather_daylight(now, [{"ts": hour, "ghi_w_m2": 0}]) is False
    assert f7.in_weather_daylight(now, []) is False


def test_manual_daylight_window():
    from datetime import datetime, timezone
    ts = datetime(2024, 7, 1, 12, 0, tzinfo=timezone.utc).timestamp()
    assert f7.in_manual_daylight(ts, "06:00", "20:00", 0) is True
    night = datetime(2024, 7, 1, 2, 0, tzinfo=timezone.utc).timestamp()
    assert f7.in_manual_daylight(night, "06:00", "20:00", 0) is False
    assert f7.in_manual_daylight(ts, None, None, 0) is None


def test_is_daylight_manual_overrides_weather():
    from datetime import datetime, timezone
    ts = datetime(2024, 7, 1, 2, 0, tzinfo=timezone.utc).timestamp()
    cfg = _cfg(day_start="06:00", day_end="20:00")
    hour = int(ts) - (int(ts) % 3600)
    # Weather claims sun; manual night window wins.
    assert f7.is_daylight(
        ts, cfg, tz_offset_s=0,
        weather_hourly=[{"ts": hour, "ghi_w_m2": 800}]) is False


def test_config_roundtrip_and_events(isolated_data):
    importlib.reload(f7)
    saved = f7.set_config({"enabled": True, "cooldown_min": 15}, device_sn="SN1")
    assert saved["enabled"] is True
    assert saved["cooldown_min"] == 15
    got = f7.get_config("SN1")
    assert got["enabled"] is True
    f7.stamp_cycle("SN1", 123.0)
    f7.record_event("SN1", "warn", "F7 AC reset pulse started", error_code=8)
    got = f7.get_config("SN1")
    assert got["last_cycle_ts"] == 123.0
    assert got["events"]
    assert got["events"][-1]["category"] == "f7"
    # UI save must not wipe history
    f7.set_config({"enabled": True, "cooldown_min": 20}, device_sn="SN1")
    got = f7.get_config("SN1")
    assert got["last_cycle_ts"] == 123.0
    assert got["events"]
    listed = f7.list_events()
    assert any(e["message"] == "F7 AC reset pulse started" for e in listed)
