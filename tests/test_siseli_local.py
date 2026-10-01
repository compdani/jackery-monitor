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


def test_modbus_panel_registers_decode_without_the_portal(tmp_path, monkeypatch):
    """8eyo is volts/10, amps/100, and watts. WfP8 is SOC, volts/10, and charge amps/100."""
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

    def fc03(regs: list[int]) -> str:
        data = b"".join(int(reg).to_bytes(2, "big") for reg in regs)
        body = bytes([0x01, 0x03, len(data)]) + data
        crc = parsers.SolarParser._crc16_modbus(body)
        return (body + crc.to_bytes(2, "little")).hex()

    frames = {
        "WfP8": fc03([55, 523, 90, 0, 0, 0, 0]),
        "8eyo": fc03([645, 80, 50]),
        "CSu4": "01030200023985",
    }
    payload = json.dumps({"b": {"ct": [
        {"cn": name, "co": base64.b64encode(bytes.fromhex(body)).decode()}
        for name, body in frames.items()
    ]}}).encode()

    seen: list[dict] = []
    mqtt.set_sink(seen.append)
    try:
        assert parsers.SolarParser.parse_payload(payload, source_topic="dtu/x/pub/event/dev_prop_post") is True
    finally:
        mqtt.set_sink(None)

    assert seen
    snap = seen[-1]
    assert snap.get("pv_v") == 64.5
    assert snap.get("pv_a") == 0.8
    assert snap.get("pv_w") == 50
    assert snap.get("generation_power_w") == 50
    assert snap.get("bat_v") == 52.3
    assert snap.get("bat_cap") == 55
    assert snap.get("bat_charge_current") == 0.9
    tele = __import__("siseli_client").to_telemetry(decoded_to_canonical(snap))
    assert tele["solar_input_w"] == 50
    assert tele["pv_voltage_v"] == 64.5
    assert tele["pv_current_a"] == 0.8


def test_mqtt_capture_ring_keeps_unmapped_load(tmp_path, monkeypatch):
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
    parsers.LAST_PUBLISH_TS = 0.0
    parsers.PENDING_PUBLISH = False
    runner.set_mqtt_capture(False)

    def fc03(regs: list[int]) -> str:
        data = b"".join(int(reg).to_bytes(2, "big") for reg in regs)
        body = bytes([0x01, 0x03, len(data)]) + data
        crc = parsers.SolarParser._crc16_modbus(body)
        return (body + crc.to_bytes(2, "little")).hex()

    frames = {
        "WfP8": fc03([55, 523, 90]),
        "8eyo": fc03([645, 80, 50]),
        "bcCu": fc03([0, 428, 710]),
    }
    payload = json.dumps({"b": {"ct": [
        {"cn": name, "co": base64.b64encode(bytes.fromhex(body)).decode()}
        for name, body in frames.items()
    ]}}).encode()

    mqtt.set_sink(lambda _snap: None)
    token = mqtt.bind_broker("203.0.113.10")
    try:
        assert parsers.SolarParser.parse_payload(payload, source_topic="dtu/x/pub") is True
        assert runner.mqtt_captures() == []
        runner.set_mqtt_capture(True)
        assert parsers.SolarParser.parse_payload(payload, source_topic="dtu/x/pub") is True
        rows = runner.mqtt_captures()
    finally:
        mqtt.unbind_broker(token)
        mqtt.set_sink(None)
        runner.set_mqtt_capture(False)

    assert len(rows) == 1
    rec = rows[0]
    assert rec["broker"] == "203.0.113.10"
    assert rec["topic"] == "dtu/x/pub"
    assert rec["load"] == "not mapped"
    assert rec["mapped"]["pv_w"] == 50
    by_name = {block["name"]: block for block in rec["blocks"]}
    assert by_name["8eyo"]["registers"] == [645, 80, 50]
    assert by_name["8eyo"]["used"] == [0, 1, 2]
    assert by_name["bcCu"]["registers"] == [0, 428, 710]
    assert by_name["bcCu"]["used"] == []
    assert runner.mqtt_captures() == []


def test_wfp8_register_6_is_load_watts(tmp_path, monkeypatch):
    """The capture: register 6 was 1218 W while the house load was about 1213 W."""
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
    parsers.LAST_PUBLISH_TS = 0.0
    parsers.PENDING_PUBLISH = False

    def fc03(regs: list[int]) -> str:
        data = b"".join(int(reg).to_bytes(2, "big") for reg in regs)
        body = bytes([0x01, 0x03, len(data)]) + data
        crc = parsers.SolarParser._crc16_modbus(body)
        return (body + crc.to_bytes(2, "little")).hex()

    frames = {
        "8eyo": fc03([679, 30, 22]),
        "WfP8": fc03([40, 509, 0, 10537, 517, 2350, 1218]),
    }
    payload = json.dumps({"b": {"ct": [
        {"cn": name, "co": base64.b64encode(bytes.fromhex(body)).decode()}
        for name, body in frames.items()
    ]}}).encode()

    seen: list[dict] = []
    mqtt.set_sink(seen.append)
    try:
        assert parsers.SolarParser.parse_payload(payload, source_topic="dtu/x/pub") is True
    finally:
        mqtt.set_sink(None)

    snap = seen[-1]
    assert snap.get("pv_w") == 22
    assert snap.get("pv_v") == 67.9
    assert snap.get("pv_a") == 0.3
    assert snap.get("bat_cap") == 40
    assert snap.get("bat_v") == 50.9
    assert snap.get("bat_charge_current") == 0.0
    assert snap.get("dischg_current") == 23.5
    assert snap.get("load_w") == 1218
