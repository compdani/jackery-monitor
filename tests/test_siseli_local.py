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
