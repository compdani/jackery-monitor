"""Local device aliases and Live-control pins."""
from __future__ import annotations

import importlib
import json

import pytest


@pytest.fixture()
def prefs_mod(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_DEVICE_PREFS_FILE", str(tmp_path / "device_prefs.json"))
    import device_prefs
    importlib.reload(device_prefs)
    return device_prefs


def test_alias_round_trip(prefs_mod, tmp_path):
    r = prefs_mod.DevicePrefs()
    row = r.update("siseli:42", alias="House")
    assert row["alias"] == "House"
    assert r.get("siseli:42")["alias"] == "House"
    r.update("siseli:42", alias="  ")
    assert "alias" not in r.get("siseli:42")
    on_disk = json.loads((tmp_path / "device_prefs.json").read_text())
    assert "siseli:42" not in on_disk or "alias" not in on_disk.get("siseli:42", {})


def test_live_controls_explicit_empty_vs_default(prefs_mod):
    r = prefs_mod.DevicePrefs()
    controls = [
        {"canonical": "LoadSwitchSetting", "dynamic": False},
        {"canonical": "BatteryType", "dynamic": True},
    ]
    assert prefs_mod.resolved_live_controls({}, controls) == ["LoadSwitchSetting"]
    r.update("siseli:42", live_controls=[])
    assert prefs_mod.resolved_live_controls(r.get("siseli:42"), controls) == []
    r.update("siseli:42", live_controls=["BatteryType", "LoadSwitchSetting"])
    assert prefs_mod.resolved_live_controls(r.get("siseli:42"), controls) == [
        "BatteryType", "LoadSwitchSetting",
    ]


def test_overlay_device_keeps_portal_name(prefs_mod):
    r = prefs_mod.DevicePrefs()
    r.update("id-A", alias="Garage")
    out = prefs_mod.overlay_device(
        {"device_id": "id-A", "name": "Jackery A", "model_name": "5000 Plus"},
        r.get("id-A"),
    )
    assert out["name"] == "Garage"
    assert out["portal_name"] == "Jackery A"
    untouched = prefs_mod.overlay_device(
        {"device_id": "id-B", "name": "Jackery B"},
        r.get("id-B"),
    )
    assert untouched["name"] == "Jackery B"
    assert untouched["portal_name"] == "Jackery B"


def test_update_requires_device_id(prefs_mod):
    r = prefs_mod.DevicePrefs()
    with pytest.raises(ValueError):
        r.update("  ", alias="x")
