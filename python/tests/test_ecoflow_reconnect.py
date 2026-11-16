"""EcoFlow auto-reconnect daily window settings."""
from __future__ import annotations

import ecoflow_reconnect as mod


def test_defaults_allow_any_time(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_ECOFLOW_RECONNECT_FILE", str(tmp_path / "ecoflow_reconnect.json"))
    cfg = mod.get()
    assert cfg["auto_reconnect"] is True
    assert cfg["reconnect_start"] is None
    assert cfg["reconnect_end"] is None
    assert mod.auto_reconnect_allowed(12 * 3600) is True


def test_window_gates_auto_reconnect(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_ECOFLOW_RECONNECT_FILE", str(tmp_path / "ecoflow_reconnect.json"))
    saved = mod.set_config({
        "auto_reconnect": True,
        "reconnect_start": "06:00",
        "reconnect_end": "22:00",
    })
    assert saved["reconnect_start"] == "06:00"
    noon = 12 * 3600
    assert mod.auto_reconnect_allowed(noon, tz_offset_s=0) is True
    night = 3 * 3600
    assert mod.auto_reconnect_allowed(night, tz_offset_s=0) is False
    mod.set_config({"auto_reconnect": False})
    assert mod.auto_reconnect_allowed(noon, tz_offset_s=0) is False


def test_partial_window_rejected(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_ECOFLOW_RECONNECT_FILE", str(tmp_path / "ecoflow_reconnect.json"))
    try:
        mod.set_config({"reconnect_start": "06:00", "reconnect_end": None})
        assert False, "expected ValueError"
    except ValueError:
        pass
