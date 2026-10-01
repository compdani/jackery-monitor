"""Map a decoded siseli-ha snapshot onto the portal canonical keys.

to_telemetry() in siseli_client.py already turns those keys into the
dashboard's live dict. This module does not import the sniffer.
"""
from __future__ import annotations

from typing import Any

# Matches the add-on's TELEMETRY_TIMEOUT_SEC default. Past this with no
# decoded payload, the HTTP portal latest-state is the fallback.
STALE_S = 1800


def _num(value: Any) -> float | None:
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def decoded_to_canonical(snapshot: dict[str, Any] | None) -> dict[str, Any]:
    """Wire keys (pv_w, load_w, bat_cap, …) → portal canonical keys."""
    snap = snapshot or {}
    solar = _num(snap.get("generation_power_w"))
    if solar is None:
        solar = _num(snap.get("pv_w")) or 0.0
    load = _num(snap.get("load_w"))
    mains = _num(snap.get("mains_power_w")) or 0.0
    direction = str(snap.get("mains_current_flow_direction") or "")
    grid = 0.0
    feed = 0.0
    if direction == "Inverter To Mains":
        feed = mains
    elif direction == "Idle":
        pass
    else:
        # "Mains To Inverter", or a power reading with no direction yet.
        grid = mains
    out: dict[str, Any] = {
        "pvInputPower": solar,
        "loadPower": load,
        "gridPower": grid,
        "feedInPower": feed,
        "batteryVoltage": _num(snap.get("bat_v")),
        "batteryChargingCurrent": _num(snap.get("bat_charge_current")) or 0.0,
        "batteryDischargeCurrent": _num(snap.get("dischg_current")) or 0.0,
    }
    soc = _num(snap.get("bat_cap"))
    if soc is not None:
        out["batterySOC"] = soc
    return out


def readings_from_telemetry(tele: dict[str, Any] | None) -> dict[str, Any]:
    """The handful of Live figures worth showing on the LAN card."""
    src = tele or {}
    return {
        "solar_w": src.get("solar_input_w"),
        "load_w": src.get("output_power_w"),
        "grid_w": src.get("ac_input_w"),
        "feed_in_w": src.get("feed_in_w"),
        "battery_v": src.get("battery_voltage_v"),
        "charge_a": src.get("battery_charge_a"),
        "discharge_a": src.get("battery_discharge_a"),
        "soc": src.get("battery_percent"),
    }


def probe_outcome(
    *,
    error: str | None,
    running: bool,
    packets: bool,
    decoded: bool,
    readings: dict[str, Any] | None,
) -> dict[str, Any]:
    """Three results a 25s listen can honestly report."""
    if decoded:
        return {
            "ok": True,
            "outcome": "decoded",
            "detail": "Decoded an MQTT publish from the inverter.",
            "readings": readings,
        }
    if error and not running:
        return {
            "ok": False,
            "outcome": "capture-failed",
            "detail": error,
            "readings": None,
        }
    if packets:
        return {
            "ok": True,
            "outcome": "packets",
            "detail": (
                "Packets from the inverter are on the path, but no MQTT "
                "publish arrived yet. The dongle often waits a few minutes."
            ),
            "readings": None,
        }
    if error:
        return {
            "ok": False,
            "outcome": "capture-failed",
            "detail": error,
            "readings": None,
        }
    return {
        "ok": False,
        "outcome": "no-packets",
        "detail": (
            "Listened, but no packets arrived from that inverter IP. "
            "Check the IP, that this host is on the same network, and "
            "that ARP inspection is not blocking the path."
        ),
        "readings": None,
    }


def should_skip_portal_latest(
    *,
    running: bool,
    entry: dict | None,
    now: float,
    stale_s: float = STALE_S,
) -> bool:
    """True when a live local decode should replace fetch_latest_data.

    Capture down, an HTTP-origin sample, or a decode older than stale_s
    all fall through to the portal.
    """
    if not running or not isinstance(entry, dict) or entry.get("origin") != "local":
        return False
    ts = entry.get("ts")
    if isinstance(ts, bool) or not isinstance(ts, (int, float)):
        return False
    return (now - float(ts)) < stale_s


def describe_mqtt_streams(streams: list[dict] | None, *, since: float | None = None) -> str | None:
    """Name the brokers a listen actually saw, instead of a generic miss."""
    rows = list(streams or [])

    def _fresh_decode(row: dict) -> bool:
        if row.get("encrypted") or not row.get("readings"):
            return False
        if since is None:
            return True
        return float(row.get("readings_ts") or 0) >= since

    def _label(row: dict) -> str:
        return f"{row.get('ip')}:{row.get('port')}"

    decoded = [row for row in rows if _fresh_decode(row)]
    plain = [
        row for row in rows
        if not row.get("encrypted") and row not in decoded
    ]
    encrypted = [row for row in rows if row.get("encrypted")]
    parts: list[str] = []
    if decoded:
        names = ", ".join(_label(row) for row in decoded)
        parts.append(f"Decoded an MQTT publish from {names}.")
    if plain:
        names = ", ".join(_label(row) for row in plain)
        parts.append(
            f"Saw MQTT to {names}, but no publish decoded yet. "
            "The dongle often waits a few minutes."
        )
    if encrypted:
        names = ", ".join(_label(row) for row in encrypted)
        parts.append(f"Saw encrypted MQTT to {names}. That stream cannot be decoded.")
    return " ".join(parts) or None
