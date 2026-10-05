"""EcoFlow Delta 3 Max Plus param → telemetry mapping (no live MQTT)."""
from __future__ import annotations

import json

from ecoflow_client import EcoflowPrivateClient, params_to_telemetry, solar_inputs_from_params
from ecoflow_delta3 import decode_property_payload


def test_solar_inputs_dual_pv():
    rows = solar_inputs_from_params({
        "pow_get_pv": 420,
        "pow_get_pv2": 310,
    }, label_prefix="Garage")
    assert len(rows) == 2
    assert rows[0] == {
        "id": "pow_get_pv",
        "label": "Garage PV1",
        "source": "ecoflow",
        "watts": 420,
    }
    assert rows[1]["watts"] == 310
    assert rows[1]["id"] == "pow_get_pv2"


def test_params_to_telemetry_delta3_max_plus():
    tele = params_to_telemetry({
        "cms_batt_soc": 77.5,
        "cms_chg_dsg_state": 2,
        "pow_get_pv": 420,
        "pow_get_pv2": 310,
        "pow_get_ac_in": 50,
        "pow_out_sum_w": 180,
        "pow_in_sum_w": 780,
        "flow_info_ac_out": 14,
        "flow_info_12v": 4,
        "bms_max_cell_temp": 28.0,
        "cms_dsg_rem_time": 120,
    }, device_type="DELTA_3_MAX_PLUS", alias="Garage", capacity_wh=2048)
    assert tele["source"] == "ecoflow"
    assert tele["battery_percent"] == 78
    assert tele["battery_status"] == 2
    assert tele["solar_input_w"] == 730
    assert len(tele["solar_inputs"]) == 2
    assert tele["ac_input_w"] == 50
    assert tele["output_power_w"] == 180
    assert tele["input_power_w"] == 780
    assert tele["ac_on"] is True
    assert tele["dc_on"] is False
    assert tele["battery_temp_c"] == 28.0
    assert tele["time_remaining_h"] == 2.0
    assert tele["capacity_wh"] == 2048
    assert tele["ecoflow_detail"]["pow_get_pv"] == 420
    assert tele["ecoflow_detail"]["pow_get_pv2"] == 310
    assert tele["model_name"] == "Delta 3 Max Plus"


def test_params_camel_case_aliases():
    tele = params_to_telemetry({
        "cmsBattSoc": 50,
        "powGetPv": 100,
        "powOutSumW": 40,
        "flowInfoAcOut": 4,
    })
    assert tele["battery_percent"] == 50
    assert tele["solar_input_w"] == 100
    assert tele["output_power_w"] == 40
    assert tele["ac_on"] is False


def test_parse_json_params_envelope():
    client = EcoflowPrivateClient(email="a@b.c", password="x")
    try:
        params = client._parse_payload(json.dumps({
            "params": {
                "cms_batt_soc": 61,
                "pow_get_pv": 200,
                "pow_get_pv2": 150,
                "pow_out_sum_w": 90,
            },
        }).encode(), "/app/device/property/R351TEST")
        assert params["cms_batt_soc"] == 61
        assert params["pow_get_pv"] == 200
        tele = params_to_telemetry(params, alias="Delta")
        assert tele["solar_input_w"] == 350
        assert len(tele["solar_inputs"]) == 2
    finally:
        client.close()


def test_decode_property_payload_rejects_garbage():
    assert decode_property_payload(b"not-a-protobuf") == {}
