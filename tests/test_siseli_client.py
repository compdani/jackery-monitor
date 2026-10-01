"""Pure-function + mocked HTTP tests for the Siseli client. No live portal."""
from __future__ import annotations

import json

import httpx
import pytest

import siseli_client as sc


def test_sn_helpers():
    assert sc.is_siseli_sn("siseli:abc")
    assert not sc.is_siseli_sn("JK-123")
    assert sc.make_sn("abc") == "siseli:abc"
    assert sc.make_sn("siseli:abc") == "siseli:abc"
    assert sc.portal_id_from_sn("siseli:abc") == "abc"
    with pytest.raises(ValueError):
        sc.make_sn("")


def test_map_energy_flow_fields_pv_sum_and_signed_mains():
    fields = {
        "pv1Power": 100,
        "pv2Power": 50,
        "load_power": 0.6,
        "aPhaseMainsPower": -200,
        "bPhaseMainsPower": 0,
        "cPhaseMainsPower": 0,
        "batteryPercentage": 77,
        "positiveTerminalBatteryCurrent": -12.5,
        "bmsBatteryVoltage": 51.2,
    }
    mapped = sc.map_energy_flow_fields(fields)
    assert mapped["pvInputPower"] == 150
    assert mapped["acOutputActivePower"] == 600.0  # kW → W
    assert mapped["gridPower"] == 200.0            # import = clamp_neg
    assert mapped["feedInPower"] == 0.0
    assert mapped["batterySOC"] == 77
    assert mapped["batteryChargingCurrent"] == 12.5
    assert mapped["batteryDischargeCurrent"] == 0.0
    assert mapped["batteryVoltage"] == 51.2


def test_map_energy_flow_feed_in():
    mapped = sc.map_energy_flow_fields({"aPhaseMainsPower": 495})
    assert mapped["feedInPower"] == 495
    assert mapped["gridPower"] == 0


def test_resolve_setting_key_alias():
    settings = {"setOutputSourcePriority": {"value": 2}}
    assert sc.resolve_setting_key(settings, "outputSourcePrioritySetting") == (
        "setOutputSourcePriority"
    )
    assert sc.resolve_setting_key(settings, "batteryChargeLimit") is None


def test_to_telemetry_mapping():
    tele = sc.to_telemetry({
        "pvInputPower": 1200,
        "loadPower": 400,
        "gridPower": 100,
        "feedInPower": 0,
        "batterySOC": 88.4,
        "batteryChargingCurrent": 3.2,
        "batteryDischargeCurrent": 0,
        "batteryVoltage": 52.1,
    })
    assert tele["solar_input_w"] == 1200
    assert tele["output_power_w"] == 400
    assert tele["ac_input_w"] == 100
    assert tele["input_power_w"] == 1300
    assert tele["battery_percent"] == 88
    assert tele["battery_status"] == 1
    assert tele["source"] == "siseli"
    assert tele["ac_on"] is False


def test_apply_derived_values_gap_fill():
    values = {
        "batteryVoltage": 50,
        "batteryDischargeCurrent": 2,
        "batteryChargingCurrent": 0,
        "acOutputActivePower": 800,
        "pvInputPower": 200,
        "feedInPower": 0,
    }
    sc.apply_derived_values(values)
    assert values["batteryPower"] == 100.0
    assert values["loadPower"] == 800
    assert "gridPower" not in values


def test_normalize_device():
    d = sc.normalize_device({"id": "42", "name": "House inverter", "model": "Sumry"})
    assert d["device_id"] == "siseli:42"
    assert d["device_sn"] == "siseli:42"
    assert d["source"] == "siseli"
    assert d["name"] == "House inverter"
    assert sc.normalize_device({}) is None


def test_normalize_controls_hides_missing():
    raw = {
        "outputSourcePrioritySetting": {"value": 1},
        "acInputRangeSetting": {"value": 0},
    }
    controls = sc.normalize_controls(raw)
    keys = {c["canonical"] for c in controls}
    assert "outputSourcePrioritySetting" in keys
    assert "backupMode" in keys  # derived from output source
    assert "batteryChargeLimit" not in keys
    backup = next(c for c in controls if c["canonical"] == "backupMode")
    assert backup["value"] is False  # SUB=1, SBU=2
    grid = next(c for c in controls if c["canonical"] == "acInputRangeSetting")
    assert grid["value"] is True  # on_value 0


def test_signed_headers_shape():
    body = b'{"account":"x","password":"y"}'
    headers = sc.make_signed_headers(body)
    for k in ("IOT-Open-AppID", "IOT-Open-Nonce", "IOT-Open-Body-Hash",
              "IOT-Open-Sign"):
        assert headers[k]
    assert headers["IOT-Open-AppID"] == sc.IOT_APP_ID
    assert len(headers["IOT-Open-Nonce"]) == 32
    assert len(headers["IOT-Open-Sign"]) == 32


def _transport(handler):
    return httpx.MockTransport(handler)


def test_login_and_list_devices_mocked():
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/apis/login/account"):
            payload = json.loads(request.content)
            assert "account" in payload
            assert payload["password"] != "secret"  # MD5, not plaintext
            return httpx.Response(200, json={
                "code": 0,
                "data": {
                    "accessToken": "tok-1",
                    "refreshToken": "ref-1",
                    "accessTokenWillExpiredAt": "2099-01-01T00:00:00+00:00",
                },
            })
        if path.endswith("/apis/device/list"):
            assert request.headers.get("IOT-Token") == "tok-1"
            return httpx.Response(200, json={
                "code": 0,
                "data": {"total": 1, "list": [{"id": "99", "name": "Inv"}]},
            })
        return httpx.Response(404, json={"code": 404})

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="secret", http=http)
    api.login()
    assert api.access_token == "tok-1"
    devices = api.list_devices("station-1")
    assert devices[0]["id"] == "99"
    api.close()


def test_login_rejects_bad_credentials():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={"code": 7, "message": "bad password"})

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="nope", http=http)
    with pytest.raises(sc.AuthenticationError):
        api.login()
    api.close()


def test_controller_latest_state_maps_live_readings():
    """Charge-controller devices publish SOC and PV on the device latest
    endpoint the portal uses, not on station history."""
    calls = {"history": 0, "flow": 0, "latest": 0}

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/state/latest/v1"):
            calls["latest"] += 1
            assert request.url.params.get("deviceId") == "525039200608948225"
            assert request.url.params.get("dataSource") == "1"
            return httpx.Response(200, json={
                "code": 0,
                "data": {"fields": {
                    "batteryRemainingCapacitySOC": {"value": 42, "unit": "%"},
                    "BatteryVoltage": {"value": 51.6, "unit": "V"},
                    "generationPower": {"value": 0.152, "unit": "kW"},
                    "load_power": {"value": 0, "unit": "W"},
                    "ChargingCurrent": {"value": 2.9, "unit": "A"},
                    "batteryTemperature1": {"value": 39, "unit": "°C"},
                }},
            })
        if path.endswith("/history/v1"):
            calls["history"] += 1
            return httpx.Response(200, json={"code": 0, "data": {"payload": {"fields": {}}}})
        if path.endswith("/energy/flow/v1"):
            calls["flow"] += 1
            return httpx.Response(200, json={"code": 0, "data": {}})
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    values = api.fetch_latest_data("525039200608948225")
    tele = sc.to_telemetry(values)
    assert calls == {"latest": 1, "history": 0, "flow": 0}
    assert values["batterySOC"] == 42
    assert values["pvInputPower"] == 152
    assert values["loadPower"] == 0
    assert values["batteryVoltage"] == 51.6
    assert tele["battery_percent"] == 42
    assert tele["solar_input_w"] == 152
    assert tele["output_power_w"] == 0
    assert tele["battery_status"] == 1
    assert tele["battery_temp_c"] == 39
    api.close()


def test_fetch_latest_data_uses_energy_flow_fallback():
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/history/v1"):
            return httpx.Response(200, json={
                "code": 0,
                "data": {"payload": {"fields": {}}},
            })
        if path.endswith("/energy/flow/v1"):
            return httpx.Response(200, json={
                "code": 0,
                "data": {"deviceAttributeState": {"fields": {
                    "pv1Power": 300,
                    "batteryPercentage": 55,
                    "load_power": 0.2,
                }}},
            })
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(
        user_id="alice", password="p", iot_token="tok", http=http,
    )
    values = api.fetch_latest_data("99")
    assert values["pvInputPower"] == 300
    assert values["batterySOC"] == 55
    assert values["acOutputActivePower"] == 200.0
    api.close()


def _flow_fields():
    return {
        "pv1Power": 300,
        "bmsSOC": 55,
        "load_power": 0.2,
    }


def test_history_error_still_uses_energy_flow():
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/history/v1"):
            return httpx.Response(200, json={"code": 20101, "message": "Illegal argument"})
        if path.endswith("/energy/flow/v1"):
            return httpx.Response(200, json={
                "code": 0,
                "data": {"deviceAttributeState": {"fields": _flow_fields()}},
            })
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    values = api.fetch_latest_data("99")
    assert values["batterySOC"] == 55
    assert values["pvInputPower"] == 300
    api.close()


def test_partial_history_still_fills_missing_soc():
    calls = {"flow": 0}

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/history/v1"):
            return httpx.Response(200, json={
                "code": "0",
                "data": {"payload": {"fields": {"feedInPower": [0]}}},
            })
        if path.endswith("/energy/flow/v1"):
            calls["flow"] += 1
            return httpx.Response(200, json={
                "code": 0,
                "data": {"deviceAttributeState": {"fields": _flow_fields()}},
            })
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    values = api.fetch_latest_data("99")
    assert calls["flow"] == 1
    assert values["batterySOC"] == 55
    assert values["feedInPower"] == 0
    assert values["pvInputPower"] == 300
    api.close()


def test_history_with_soc_skips_energy_flow():
    calls = {"flow": 0}

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/history/v1"):
            return httpx.Response(200, json={
                "code": 0,
                "data": {"payload": {"fields": {"batterySOC": [42]}}},
            })
        if path.endswith("/energy/flow/v1"):
            calls["flow"] += 1
            return httpx.Response(200, json={
                "code": 0,
                "data": {"deviceAttributeState": {"fields": {"bmsSOC": 99}}},
            })
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    values = api.fetch_latest_data("99")
    assert calls["flow"] == 0
    assert values["batterySOC"] == 42
    api.close()


def test_set_device_setting_uses_alias():
    writes = []

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/config/write"):
            writes.append(json.loads(request.content))
            return httpx.Response(200, json={"code": 0, "data": {}})
        return httpx.Response(404)

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(
        user_id="alice", password="p", iot_token="tok", http=http,
    )
    api.set_device_setting(
        "99", "outputSourcePrioritySetting", 2,
        settings={"setOutputSourcePriority": {"value": 1}},
    )
    assert writes[0]["key"] == "setOutputSourcePriority"
    assert writes[0]["value"] == 2
    assert writes[0]["id"] == "99"
    api.close()


def test_load_switch_setting_writes_portal_body():
    raw = {
        "LoadSwitchSetting": {
            "value": "0",
            "valueDisplay": "Off",
            "name": "Load Switch Setting",
        },
    }
    controls = sc.normalize_controls(raw)
    assert len(controls) == 1
    load = controls[0]
    assert load["canonical"] == "LoadSwitchSetting"
    assert load["name"] == "Load"
    assert load["kind"] == "switch"
    assert load["value"] is False
    assert load["dynamic"] is False

    writes = []

    def handler(request: httpx.Request) -> httpx.Response:
        writes.append(json.loads(request.content))
        return httpx.Response(200, json={"code": 0, "data": {}})

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    api.set_device_setting("99", "LoadSwitchSetting", 1, settings=raw)
    assert writes[0]["key"] == "LoadSwitchSetting"
    assert writes[0]["id"] == "99"
    assert writes[0]["deviceId"] == "99"
    assert writes[0]["value"] == "1"
    api.close()


def test_dynamic_settings_are_separate_controls():
    raw = {
        "ChargeLimitVoltage": {
            "value": 15,
            "unit": "V",
            "name": "Charge Limit Voltage",
            "valueTypeDict": "Numeric",
        },
        "BatteryType": {
            "value": "4",
            "name": "Battery Type",
            "valueDisplay": "Lithium Battery",
            "valueTypeDict": "Enumeration",
        },
        "outputSourcePrioritySetting": {"value": 1},
    }
    controls = sc.normalize_controls(raw)
    by = {c["canonical"]: c for c in controls}
    voltage = by["ChargeLimitVoltage"]
    assert voltage["kind"] == "number"
    assert voltage["value"] == 15
    assert voltage["unit"] == "V"
    assert voltage["dynamic"] is True
    battery = by["BatteryType"]
    assert battery["dynamic"] is True
    assert battery["hint"] == "Lithium Battery"
    assert battery["value"] == 4
    assert "outputSourcePrioritySetting" in by
    assert by["outputSourcePrioritySetting"]["dynamic"] is False
    assert sc.default_live_control_keys(controls) == [
        "outputSourcePrioritySetting", "backupMode",
    ]

    writes = []

    def handler(request: httpx.Request) -> httpx.Response:
        writes.append(json.loads(request.content))
        return httpx.Response(200, json={"code": 0, "data": {}})

    http = httpx.Client(transport=_transport(handler))
    api = sc.SiseliAPI(user_id="alice", password="p", iot_token="tok", http=http)
    api.set_device_setting("99", "ChargeLimitVoltage", 16, settings=raw)
    api.set_device_setting("99", "BatteryType", 5, settings=raw)
    assert writes[0]["key"] == "ChargeLimitVoltage"
    assert writes[0]["value"] == 16
    assert writes[1]["key"] == "BatteryType"
    assert writes[1]["value"] == "5"
    api.close()
