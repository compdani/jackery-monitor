"""Local Siseli decode → dashboard telemetry, without starting a sniffer."""
from __future__ import annotations

import base64
import json

from siseli_local.telemetry import decoded_to_canonical, should_skip_portal_latest


def test_decoded_snapshot_maps_watts_and_soc():
    canonical = decoded_to_canonical({
        "generation_power_w": 800,
        "pv_w": 500,
        "load_w": 267,
        "mains_power_w": 40,
        "mains_current_flow_direction": "Inverter To Mains",
        "bat_v": 53.3,
        "bat_cap": 88,
        "bat_charge_current": 5.0,
        "dischg_current": 0.0,
    })
    assert canonical["pvInputPower"] == 800
    assert canonical["loadPower"] == 267
    assert canonical["feedInPower"] == 40
    assert canonical["gridPower"] == 0
    assert canonical["batterySOC"] == 88
    assert canonical["batteryVoltage"] == 53.3
    assert canonical["batteryChargingCurrent"] == 5.0

    import siseli_client
    tele = siseli_client.to_telemetry(canonical)
    assert tele["solar_input_w"] == 800
    assert tele["output_power_w"] == 267
    assert tele["feed_in_w"] == 40
    assert tele["ac_input_w"] == 0
    assert tele["battery_percent"] == 88
    assert tele["source"] == "siseli"


def test_probe_outcome_names_the_three_results():
    from siseli_local.telemetry import probe_outcome
    failed = probe_outcome(
        error="permission denied", running=False, packets=False,
        decoded=False, readings=None,
    )
    assert failed["outcome"] == "capture-failed"
    assert failed["ok"] is False
    waiting = probe_outcome(
        error=None, running=True, packets=True, decoded=False, readings=None,
    )
    assert waiting["outcome"] == "packets"
    decoded = probe_outcome(
        error=None, running=True, packets=True, decoded=True,
        readings={"solar_w": 10},
    )
    assert decoded["outcome"] == "decoded"
    assert decoded["readings"]["solar_w"] == 10


def test_describe_mqtt_streams_names_plain_and_encrypted():
    from siseli_local.telemetry import describe_mqtt_streams
    text = describe_mqtt_streams([
        {"ip": "203.0.113.10", "port": 1883, "encrypted": False,
         "readings": {"solar_w": 12}, "readings_ts": 20.0},
        {"ip": "203.0.113.11", "port": 1883, "encrypted": False},
        {"ip": "203.0.113.12", "port": 8883, "encrypted": True},
    ], since=10.0)
    assert "Decoded an MQTT publish from 203.0.113.10:1883." in text
    assert "Saw MQTT to 203.0.113.11:1883" in text
    assert "Saw encrypted MQTT to 203.0.113.12:8883." in text
    stale = describe_mqtt_streams([
        {"ip": "203.0.113.10", "port": 1883, "encrypted": False,
         "readings": {"solar_w": 12}, "readings_ts": 1.0},
    ], since=10.0)
    assert stale.startswith("Saw MQTT to 203.0.113.10:1883")


def test_stream_counts_tcp_bytes_separately_from_a_decode():
    import siseli_local.runner as runner
    runner.note_stream("198.51.100.8", 1883, encrypted=False, payload_bytes=120)
    row = next(item for item in runner.mqtt_streams() if item["ip"] == "198.51.100.8")
    assert row["payload_bytes"] == 120
    assert not row.get("readings")
    assert not row.get("saw_mqtt")
    runner.note_stream("198.51.100.8", 1883, payload_bytes=10, saw_mqtt=True, mqtt_packets=2)
    row = next(item for item in runner.mqtt_streams() if item["ip"] == "198.51.100.8")
    assert row["payload_bytes"] == 130
    assert row["mqtt_packets"] == 2
    assert row["saw_mqtt"] is True


def test_publish_labels_the_bound_broker():
    import siseli_local.mqtt as mqtt
    got = {}
    mqtt.set_sink(lambda snap: got.update(snap))
    token = mqtt.bind_broker("203.0.113.10")
    try:
        assert mqtt.publish_grouped_state({"pv_w": 1}) is True
    finally:
        mqtt.unbind_broker(token)
        mqtt.set_sink(None)
    assert got["broker_ip"] == "203.0.113.10"
    assert got["pv_w"] == 1


def test_portal_latest_skipped_only_while_local_is_fresh():
    fresh = {"origin": "local", "ts": 1_000.0}
    assert should_skip_portal_latest(running=True, entry=fresh, now=1_100.0) is True
    assert should_skip_portal_latest(running=False, entry=fresh, now=1_100.0) is False
    assert should_skip_portal_latest(
        running=True, entry={"origin": "http", "ts": 1_000.0}, now=1_100.0,
    ) is False
    assert should_skip_portal_latest(
        running=True, entry=fresh, now=1_000.0 + 1800, 
    ) is False


def test_parser_fixture_reaches_the_sink(tmp_path, monkeypatch):
    cache = tmp_path / "state.json"
    monkeypatch.setenv("SISELI_LOCAL_STATE_FILE", str(cache))

    import siseli_local.config as cfg
    import siseli_local.mqtt as mqtt
    import siseli_local.parsers as parsers
    import siseli_local.state as st

    cfg.STATE_CACHE_FILE = str(cache)
    parsers.STATE_CACHE_FILE = str(cache)
    st.LAST_STATE.clear()
    st.DISCOVERY_PUBLISHED = True
    st.PUBLISHED_SENSOR_KEYS.clear()
    parsers.LAST_PUBLISH_TS = 0.0
    parsers.PENDING_PUBLISH = False

    seen: list[dict] = []
    mqtt.set_sink(seen.append)
    try:
        payload = json.dumps({"b": [
            {"cn": "2ONL", "co": base64.b64encode(b"4 53.3 88 5.0 0.0").decode()},
            {"cn": "2l0E", "co": base64.b64encode(b"229.9 49.9 390 267").decode()},
            {"cn": "Mpod", "co": base64.b64encode(b"100.0 2.5 200").decode()},
        ]}).encode()
        assert parsers.SolarParser.parse_payload(payload, source_topic="inv/test") is True
    finally:
        mqtt.set_sink(None)

    assert seen, "decoded snapshot was not published to the sink"
    snap = seen[-1]
    assert snap.get("bat_cap") == 88
    assert snap.get("load_w") == 267
    assert snap.get("pv_w") == 200
    tele = __import__("siseli_client").to_telemetry(decoded_to_canonical(snap))
    assert tele["battery_percent"] == 88
    assert tele["output_power_w"] == 267
    assert tele["solar_input_w"] == 200


_CAPTURED_MODBUS = {
    "s2te": "010322483c1e0020202020424d3732363020202020202000000306000000020000000100019115",
    "WfP8": "01030e0038020c00aa2626000000000000346d",
    "8eyo": "010306042c005a005d50dd",
    "bcCu": "010316000001ac02c60000000000000000002a00250866079698fb",
    "CSu4": "01030200023985",
    "XCQF": "0103060000000000002175",
}


def _modbus_payload() -> bytes:
    blocks = [
        {"cn": name, "co": base64.b64encode(bytes.fromhex(hexbody)).decode()}
        for name, hexbody in _CAPTURED_MODBUS.items()
    ]
    return json.dumps({"b": {"ct": blocks}}).encode()


def test_modbus_blocks_follow_the_portal_sample(tmp_path, monkeypatch):
    """The captured frames decode only by lining registers up with portal watts."""
    cache = tmp_path / "state.json"
    monkeypatch.setenv("SISELI_LOCAL_STATE_FILE", str(cache))

    import siseli_local.config as cfg
    import siseli_local.mqtt as mqtt
    import siseli_local.parsers as parsers
    import siseli_local.runner as runner
    import siseli_local.state as st

    cfg.STATE_CACHE_FILE = str(cache)
    parsers.STATE_CACHE_FILE = str(cache)
    st.LAST_STATE.clear()
    st.DISCOVERY_PUBLISHED = True
    st.PUBLISHED_SENSOR_KEYS.clear()
    parsers.LAST_PUBLISH_TS = 0.0
    parsers.PENDING_PUBLISH = False
    parsers.SolarParser._MODBUS_MAP.clear()
    parsers.SolarParser._MODBUS_READY = False

    seen: list[dict] = []
    mqtt.set_sink(seen.append)
    try:
        runner.note_http_telemetry({
            "solar_input_w": 710,
            "output_power_w": 1068,
            "ac_input_w": 0,
            "feed_in_w": 0,
            "battery_voltage_v": 52.4,
            "battery_percent": 56,
        })
        assert parsers.SolarParser.parse_payload(_modbus_payload(), source_topic="dtu/x/pub/event/dev_prop_post") is True
    finally:
        mqtt.set_sink(None)
        parsers.SolarParser._MODBUS_MAP.clear()
        parsers.SolarParser._MODBUS_READY = False

    assert seen, "modbus snapshot was not published"
    snap = seen[-1]
    assert snap.get("pv_w") == 710
    assert snap.get("load_w") == 1068
    assert snap.get("bat_v") == 52.4
    assert snap.get("bat_cap") == 56


def test_modbus_blocks_stay_undecoded_without_a_portal_match(tmp_path, monkeypatch):
    cache = tmp_path / "state.json"
    monkeypatch.setenv("SISELI_LOCAL_STATE_FILE", str(cache))

    import siseli_local.config as cfg
    import siseli_local.parsers as parsers
    import siseli_local.runner as runner
    import siseli_local.state as st

    cfg.STATE_CACHE_FILE = str(cache)
    parsers.STATE_CACHE_FILE = str(cache)
    st.LAST_STATE.clear()
    st.DISCOVERY_PUBLISHED = True
    parsers.SolarParser._MODBUS_MAP.clear()
    parsers.SolarParser._MODBUS_READY = False
    runner.note_http_telemetry({
        "solar_input_w": 9000,
        "output_power_w": 8000,
        "ac_input_w": 0,
        "feed_in_w": 0,
    })
    try:
        assert parsers.SolarParser.parse_payload(_modbus_payload(), source_topic="dtu/x/pub") is False
        assert parsers.SolarParser._MODBUS_READY is False
    finally:
        parsers.SolarParser._MODBUS_MAP.clear()
        parsers.SolarParser._MODBUS_READY = False
