"""BMS pack registry + Siseli SOC overlay (no BLE)."""
from __future__ import annotations

import importlib

import pytest


@pytest.fixture()
def reg(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_BMS_DEVICES_FILE", str(tmp_path / "bms.json"))
    import bms_devices
    importlib.reload(bms_devices)
    return bms_devices


def test_normalize_mac(reg):
    assert reg.normalize_mac("aa-bb-cc-dd-ee-ff") == "AA:BB:CC:DD:EE:FF"
    assert reg.normalize_mac("AABBCCDDEEFF") == "AA:BB:CC:DD:EE:FF"
    with pytest.raises(ValueError):
        reg.normalize_mac("aa:bb")


def test_upsert_and_assign(reg):
    r = reg.BmsRegistry()
    p = r.upsert("aa:bb:cc:dd:ee:ff", alias="Pack 1", capacity_wh=5120,
                 siseli_device_sn="siseli:42")
    assert p["mac"] == "AA:BB:CC:DD:EE:FF"
    assert r.use_as_main("siseli:42") is True
    assert len(r.list_packs("siseli:42")) == 1
    assert r.list_packs("siseli:99") == []
    r.set_use_as_main("siseli:42", False)
    assert r.use_as_main("siseli:42") is False
    assert r.delete("AA:BB:CC:DD:EE:FF") is True
    assert r.get("AA:BB:CC:DD:EE:FF") is None


def test_weighted_soc_pack_only(reg):
    # Two equal packs at 80% and 20% → 50%. Inverter is not in the mix.
    assert reg.weighted_soc([(80, 5000), (20, 5000)]) == pytest.approx(50.0)
    assert reg.weighted_soc([(100, 1000), (50, 3000)]) == pytest.approx(62.5)
    assert reg.weighted_soc([]) is None
    assert reg.weighted_soc([(80, 0)]) is None


def test_overlay_replaces_headline_keeps_portal(reg):
    tele = {"battery_percent": 64, "solar_input_w": 100, "source": "siseli"}
    packs = [
        {"mac": "AA:BB:CC:DD:EE:01", "capacity_wh": 5000, "alias": "A"},
        {"mac": "AA:BB:CC:DD:EE:02", "capacity_wh": 5000, "alias": "B"},
    ]
    now = 1_700_000_000.0
    live = {
        "AA:BB:CC:DD:EE:01": {"soc_pct": 80, "ts": now, "current_a": 10,
                              "power_w": 500, "voltage_v": 53.0, "temp_c": 22},
        "AA:BB:CC:DD:EE:02": {"soc_pct": 20, "ts": now, "current_a": 5,
                              "power_w": 250, "voltage_v": 53.1, "temp_c": 28},
    }
    out = reg.overlay_telemetry(
        tele, packs=packs, live=live, use_as_main=True, now=now)
    assert tele["battery_percent"] == 64  # original not mutated
    assert out["battery_percent"] == 50.0
    assert out["system_soc_pct"] == 50.0
    assert out["inverter_soc_pct"] == 64
    assert out["main_soc_pct"] == 64
    assert out["bms_source"] is True
    assert out["capacity_wh"] == 10000
    assert out["battery_temp_c"] == 28
    assert out["battery_status"] == 1  # charging


def test_overlay_falls_back_when_stale_or_disabled(reg):
    tele = {"battery_percent": 64}
    packs = [{"mac": "AA:BB:CC:DD:EE:01", "capacity_wh": 5000}]
    now = 1_700_000_000.0
    live = {"AA:BB:CC:DD:EE:01": {"soc_pct": 10, "ts": now - 500}}
    out = reg.overlay_telemetry(
        tele, packs=packs, live=live, use_as_main=True, now=now)
    assert out["battery_percent"] == 64
    assert "bms_source" not in out
    out2 = reg.overlay_telemetry(
        tele, packs=packs, live={
            "AA:BB:CC:DD:EE:01": {"soc_pct": 10, "ts": now},
        }, use_as_main=False, now=now)
    assert out2["battery_percent"] == 64


def test_ui_pack_rows_keep_stale_with_error(reg):
    packs = [{"mac": "AA:BB:CC:DD:EE:01", "capacity_wh": 1000, "alias": "P1"}]
    now = 1_700_000_000.0
    rows = reg.ui_pack_rows(packs, {}, now=now)
    assert rows[0]["source"] == "bms"
    assert rows[0]["error"] == "no reading"
    live = {"AA:BB:CC:DD:EE:01": {
        "soc_pct": 40, "ts": now - 400, "power_w": -100, "temp_c": 19,
    }}
    rows = reg.ui_pack_rows(packs, live, now=now)
    assert rows[0]["rb"] == 40
    assert rows[0]["op"] == 100
    assert rows[0]["error"] == "stale"


def test_infer_capacity_wh(reg):
    assert reg.infer_capacity_wh({"full_ah": 100, "voltage_v": 51.2}) == 5120
    assert reg.infer_capacity_wh({}) is None
