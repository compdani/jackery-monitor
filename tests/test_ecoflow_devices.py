"""EcoFlow device registry + Siseli overlay (no MQTT)."""
from __future__ import annotations

import importlib
import time

import pytest

from ecoflow_client import params_to_telemetry


@pytest.fixture()
def reg(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_ECOFLOW_DEVICES_FILE", str(tmp_path / "ecoflow.json"))
    import ecoflow_devices
    importlib.reload(ecoflow_devices)
    return ecoflow_devices


def test_sn_helpers(reg):
    assert reg.is_ecoflow_sn("ecoflow:R351")
    assert not reg.is_ecoflow_sn("siseli:42")
    assert reg.make_sn("R351ABC") == "ecoflow:R351ABC"
    assert reg.make_sn("ecoflow:R351ABC") == "ecoflow:R351ABC"
    assert reg.raw_sn_from_sn("ecoflow:R351ABC") == "R351ABC"
    with pytest.raises(ValueError):
        reg.make_sn("")


def test_upsert_link_delete(reg):
    r = reg.EcoflowRegistry()
    row = r.upsert(
        "R351TEST",
        device_type="DELTA_3_MAX_PLUS",
        alias="Garage",
        capacity_wh=2048,
    )
    assert row["sn"] == "ecoflow:R351TEST"
    assert row["roles"] == ["solar", "battery", "output"]
    linked = r.link(
        "ecoflow:R351TEST",
        siseli_device_sn="siseli:42",
        roles=["solar", "output"],
    )
    assert linked["siseli_device_sn"] == "siseli:42"
    assert linked["roles"] == ["solar", "output"]
    assert len(r.list_devices("siseli:42")) == 1
    assert r.list_devices("siseli:99") == []
    assert r.delete("ecoflow:R351TEST") is True
    assert r.get("ecoflow:R351TEST") is None


def test_overlay_sums_solar_and_output(reg):
    now = time.time()
    ef = params_to_telemetry({
        "cms_batt_soc": 80,
        "pow_get_pv": 400,
        "pow_get_pv2": 300,
        "pow_out_sum_w": 150,
    }, alias="Garage", capacity_wh=2048)
    live = {
        "ecoflow:R351": {
            "telemetry": ef,
            "detail": ef["ecoflow_detail"],
            "ts": now,
        },
    }
    linked = [{
        "sn": "ecoflow:R351",
        "alias": "Garage",
        "roles": ["solar", "battery", "output"],
        "capacity_wh": 2048,
    }]
    base = {
        "battery_percent": 55,
        "solar_input_w": 1000,
        "solar_inputs": [
            {"id": "siseli:pv1", "label": "Siseli PV1", "source": "siseli", "watts": 400},
            {"id": "siseli:pv2", "label": "Siseli PV2", "source": "siseli", "watts": 600},
        ],
        "output_power_w": 200,
        "ac_input_w": 0,
        "source": "siseli",
    }
    out = reg.overlay_telemetry(base, linked=linked, live_by_sn=live, now=now)
    assert base["solar_input_w"] == 1000  # original not mutated
    assert out["solar_input_w"] == 1700
    assert out["additional_solar_w"] == 700
    assert out["output_power_w"] == 350
    assert out["additional_output_w"] == 150
    assert out["ecoflow_linked"] is True
    assert len(out["linked_ecoflow"]) == 1
    assert out["linked_ecoflow"][0]["fresh"] is True
    assert out["load_inputs"] == [
        {"id": "siseli", "label": "Siseli load", "source": "siseli", "watts": 200},
        {
            "id": "ecoflow:R351",
            "label": "Garage load",
            "source": "ecoflow",
            "device_sn": "ecoflow:R351",
            "watts": 150,
        },
    ]
    # Battery role: EcoFlow only (no BMS yet) → EcoFlow SOC
    assert out["battery_percent"] == 80.0
    assert out["ecoflow_source"] is True
    # Siseli + EcoFlow PV strings
    sources = {i["source"] for i in out["solar_inputs"]}
    assert sources == {"siseli", "ecoflow"}


def test_overlay_load_sources_filter(reg):
    now = time.time()
    ef = params_to_telemetry({
        "cms_batt_soc": 80,
        "pow_get_pv": 0,
        "pow_out_sum_w": 150,
    }, alias="Garage", capacity_wh=2048)
    live = {
        "ecoflow:R351": {"telemetry": ef, "detail": {}, "ts": now},
    }
    linked = [{
        "sn": "ecoflow:R351",
        "alias": "Garage",
        "roles": ["output"],
        "capacity_wh": 2048,
    }]
    base = {
        "battery_percent": 55,
        "solar_input_w": 0,
        "output_power_w": 200,
        "source": "siseli",
    }
    only_eco = reg.overlay_telemetry(
        base, linked=linked, live_by_sn=live, now=now,
        load_sources=["ecoflow:R351"],
    )
    assert only_eco["output_power_w"] == 150
    assert only_eco["siseli_output_w"] == 200
    only_siseli = reg.overlay_telemetry(
        base, linked=linked, live_by_sn=live, now=now,
        load_sources=["siseli"],
    )
    assert only_siseli["output_power_w"] == 200
    none = reg.overlay_telemetry(
        base, linked=linked, live_by_sn=live, now=now,
        load_sources=[],
    )
    assert none["output_power_w"] == 0
    # load_inputs always lists every contributor for the gear UI
    assert len(none["load_inputs"]) == 2


def test_overlay_weighted_soc_with_bms(reg):
    now = time.time()
    ef = params_to_telemetry({
        "cms_batt_soc": 100,
        "pow_get_pv": 0,
        "pow_out_sum_w": 0,
    }, capacity_wh=2048)
    live = {
        "ecoflow:R351": {"telemetry": ef, "detail": {}, "ts": now},
    }
    linked = [{
        "sn": "ecoflow:R351",
        "alias": "Garage",
        "roles": ["battery"],
        "capacity_wh": 2048,
    }]
    # Pretend BMS already set a 5000 Wh pack at 50%.
    base = {
        "battery_percent": 50.0,
        "capacity_wh": 5000,
        "bms_source": True,
        "solar_input_w": 0,
        "output_power_w": 0,
        "source": "siseli",
    }
    out = reg.overlay_telemetry(
        base, linked=linked, live_by_sn=live, now=now, bms_already=True,
    )
    # (50*5000 + 100*2048) / (5000+2048) ≈ 64.5
    assert out["battery_percent"] == pytest.approx(64.5, abs=0.2)
    assert out["ecoflow_source"] is True
    assert out["capacity_wh"] == 5000 + 2048


def test_overlay_skips_stale(reg):
    now = time.time()
    ef = params_to_telemetry({"cms_batt_soc": 80, "pow_get_pv": 500, "pow_out_sum_w": 100})
    live = {
        "ecoflow:R351": {
            "telemetry": ef,
            "detail": {},
            "ts": now - 500,
        },
    }
    linked = [{
        "sn": "ecoflow:R351",
        "alias": "Garage",
        "roles": ["solar", "output", "battery"],
        "capacity_wh": 2048,
    }]
    out = reg.overlay_telemetry(
        {"battery_percent": 40, "solar_input_w": 10, "output_power_w": 5, "source": "siseli"},
        linked=linked,
        live_by_sn=live,
        now=now,
    )
    assert out["solar_input_w"] == 10
    assert out["output_power_w"] == 5
    assert out["battery_percent"] == 40
    assert out["linked_ecoflow"][0]["fresh"] is False
