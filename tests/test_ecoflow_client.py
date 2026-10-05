"""EcoFlow Delta 3 Max Plus param → telemetry mapping (no live MQTT)."""
from __future__ import annotations

import json
import re
from unittest.mock import MagicMock

import httpx
import paho.mqtt.client as mqtt

from ecoflow_client import (
    EcoflowPrivateClient,
    MQTT_KEEPALIVE_S,
    params_to_telemetry,
    resolve_client_id,
    solar_inputs_from_params,
)
from ecoflow_delta3 import decode_property_payload


_CLIENT_ID_RE = re.compile(r"^ANDROID_[0-9A-F]{32}_\d+$")


def test_resolve_client_id_simple_format():
    cid = resolve_client_id("2104939217409142785")
    assert _CLIENT_ID_RE.match(cid)
    # No legacy MD5/millis suffix (would be 4+ underscore-separated segments).
    assert cid.count("_") == 2


def test_resolve_client_id_reuses_stable():
    uid = "2104939217409142785"
    first = resolve_client_id(uid)
    again = resolve_client_id(uid, preferred=first)
    assert again == first


def test_resolve_client_id_rejects_legacy_md5_form():
    uid = "2104939217409142785"
    legacy = (
        f"ANDROID_ABCDEF0123456789ABCDEF0123456789_{uid}_"
        f"{'0' * 32}_1700000000000_deadbeefcafebabe0123456789abcdef"
    )
    fresh = resolve_client_id(uid, preferred=legacy)
    assert fresh != legacy
    assert _CLIENT_ID_RE.match(fresh)
    assert fresh.count("_") == 2


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


def test_on_connect_auth_failure_sets_flags():
    client = EcoflowPrivateClient(email="a@b.c", password="x")
    try:
        client.mqtt_client_id = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
        # VERSION2 signature: (client, userdata, flags, reason_code, properties=None)
        client._on_connect(None, None, None, 5, None)
        assert client.connected is False
        assert client.auth_failed is True
        assert "not authorized" in (client.mqtt_error or "").lower()
        assert "rc=5" in (client.mqtt_error or "")
    finally:
        client.close()


def test_on_connect_reasoncode_success():
    """VERSION2 passes ReasonCode; int(ReasonCode) TypeErrors — must use == 0."""
    from paho.mqtt.enums import ConnackCode

    client = EcoflowPrivateClient(email="a@b.c", password="x")
    try:
        client.mqtt_client_id = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
        client.user_id = "1"
        client._devices = {}
        reason = mqtt.convert_connack_rc_to_reason_code(ConnackCode.CONNACK_ACCEPTED)
        mock_mqtt = MagicMock()
        client._on_connect(mock_mqtt, None, None, reason, None)
        assert client.connected is True
        assert client.auth_failed is False
        assert client.mqtt_error is None
    finally:
        client.close()


def test_on_connect_reasoncode_not_authorized():
    from paho.mqtt.enums import ConnackCode

    client = EcoflowPrivateClient(email="a@b.c", password="x")
    try:
        client.mqtt_client_id = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
        reason = mqtt.convert_connack_rc_to_reason_code(
            ConnackCode.CONNACK_REFUSED_NOT_AUTHORIZED,
        )
        client._on_connect(None, None, None, reason, None)
        assert client.connected is False
        assert client.auth_failed is True
        err = client.mqtt_error or ""
        assert "not authorized" in err.lower()
        assert "rc=-1" not in err
        assert "Not authorized" in err
    finally:
        client.close()


def test_call_api_sends_userid_as_form_body(monkeypatch):
    """hassio-ecoflow-cloud sends userId as form body on certification GET."""
    client = EcoflowPrivateClient(email="a@b.c", password="x")
    client.token = "tok"
    client.user_id = "2104939217409142785"
    captured: dict = {}

    def fake_request(method, url, headers=None, params=None, content=None, **kwargs):
        captured.update({
            "method": method,
            "url": url,
            "headers": dict(headers or {}),
            "params": params,
            "content": content,
        })
        return httpx.Response(
            200,
            json={"message": "Success", "code": "0", "data": {"ok": True}},
            request=httpx.Request(method, url),
        )

    monkeypatch.setattr(client._http, "request", fake_request)
    try:
        body = client._call_api("/iot-auth/app/certification")
        assert body["data"]["ok"] is True
        assert captured["method"] == "GET"
        assert captured["url"].endswith("/iot-auth/app/certification")
        assert "userId" not in (captured["params"] or {})
        assert captured["headers"].get("content-type") == (
            "application/x-www-form-urlencoded"
        )
        assert captured["content"] == b"userId=2104939217409142785"
    finally:
        client.close()


def test_start_mqtt_uses_callback_api_v2_and_keepalive_15(monkeypatch):
    import ecoflow_client as ec

    client = EcoflowPrivateClient(email="a@b.c", password="x")
    client.mqtt_url = "mqtt-e.ecoflow.com"
    client.mqtt_port = 8883
    client.mqtt_user = "cert-user"
    client.mqtt_password = "cert-pass"
    client.mqtt_client_id = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
    client.user_id = "1"

    constructed: dict = {}
    mock_mqtt = MagicMock()

    def fake_client(**kwargs):
        constructed.update(kwargs)
        return mock_mqtt

    monkeypatch.setattr(ec.mqtt, "Client", fake_client)

    def fake_wait(timeout=None):
        client.connected = True
        return True

    monkeypatch.setattr(client._connect_event, "wait", fake_wait)
    try:
        assert MQTT_KEEPALIVE_S == 15
        ok = client.start_mqtt(wait_s=1.0)
        assert ok is True
        assert constructed.get("callback_api_version") == (
            mqtt.CallbackAPIVersion.VERSION2
        )
        assert constructed.get("client_id") == client.mqtt_client_id
        assert constructed.get("protocol") == mqtt.MQTTv311
        mock_mqtt.connect.assert_called_once_with(
            "mqtt-e.ecoflow.com", 8883, keepalive=15,
        )
        mock_mqtt.loop_start.assert_called_once()
    finally:
        client.close()


def test_decode_property_payload_rejects_garbage():
    assert decode_property_payload(b"not-a-protobuf") == {}


def test_on_message_empty_protobuf_increments_decode_empty():
    updates: list = []
    client = EcoflowPrivateClient(
        email="a@b.c", password="x",
        on_update=lambda sn, tele, detail: updates.append(sn),
    )
    try:
        client._devices = {"R351TEST": {"raw_sn": "R351TEST", "alias": "G"}}
        msg = MagicMock()
        msg.topic = "/app/device/property/R351TEST"
        msg.payload = b"not-a-protobuf"
        client._on_message(None, None, msg)
        assert client.mqtt_msg_count == 1
        assert client.mqtt_decode_empty == 1
        assert client.mqtt_update_count == 0
        assert updates == []
    finally:
        client.close()


def test_on_message_json_params_increments_update_count():
    updates: list = []
    client = EcoflowPrivateClient(
        email="a@b.c", password="x",
        on_update=lambda sn, tele, detail: updates.append((sn, tele)),
    )
    try:
        client._devices = {
            "R351TEST": {
                "raw_sn": "R351TEST",
                "alias": "Garage",
                "device_type": "DELTA_3_MAX_PLUS",
                "capacity_wh": 2048,
            },
        }
        msg = MagicMock()
        msg.topic = "/app/device/property/R351TEST"
        msg.payload = json.dumps({
            "params": {
                "cms_batt_soc": 55,
                "pow_get_pv": 120,
                "pow_out_sum_w": 40,
            },
        }).encode()
        client._on_message(None, None, msg)
        assert client.mqtt_msg_count == 1
        assert client.mqtt_decode_empty == 0
        assert client.mqtt_update_count == 1
        assert len(updates) == 1
        assert updates[0][0] == "ecoflow:R351TEST"
        assert updates[0][1]["battery_percent"] == 55
        assert updates[0][1]["solar_input_w"] == 120
        stats = client.mqtt_stats()
        assert stats["mqtt_update_count"] == 1
        assert stats["last_mqtt_topic"] == msg.topic
    finally:
        client.close()
