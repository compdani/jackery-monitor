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
    assert values["gridPower"] == max(0.0, 800 - 200 + 100 + 0)


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
    api.close()
