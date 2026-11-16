"""cloud_props_to_telemetry adapter."""
from __future__ import annotations

from cloud_client import cloud_props_to_telemetry


def test_maps_core_keys():
    tele = cloud_props_to_telemetry({
        "rb": 26, "bt": 313, "ip": 47, "op": 0,
        "acip": 0, "cip": 0, "oac": 1, "acov": 0, "acohz": 0,
        "it": 999, "ot": 999, "ec": 0,
    })
    assert tele["battery_percent"] == 26
    assert tele["battery_temp_c"] == 31.3
    assert tele["input_power_w"] == 47
    assert tele["solar_input_w"] == 47
    assert tele["output_power_w"] == 0
    assert tele["ac_on"] is True
    assert tele["time_to_full_h"] == 0.0
    assert "battery_status" not in tele


def test_bs_idle_charging_discharging():
    assert cloud_props_to_telemetry({"bs": 0})["battery_status"] == 0
    assert cloud_props_to_telemetry({"bs": 1})["battery_status"] == 1
    assert cloud_props_to_telemetry({"bs": 2})["battery_status"] == 2


def test_solar_subtracts_grid_and_car():
    tele = cloud_props_to_telemetry({"ip": 500, "acip": 200, "cip": 50})
    assert tele["solar_input_w"] == 250
    assert tele["ac_input_w"] == 200
    assert tele["car_input_w"] == 50
