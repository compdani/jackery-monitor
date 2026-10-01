"""Run the vendored sniffer in a background thread.

Config is environment-driven inside the vendored modules, and core.py
copies those names at import (`from .config import *`). Apply them on
the core module before the thread starts, and again on every restart.
"""
from __future__ import annotations

import atexit
import logging
import os
import threading
import time

log = logging.getLogger("siseli_local")

_thread: threading.Thread | None = None
_lock = threading.Lock()
_last_error: str | None = None
_pending: dict | None = None
_last_decode_ts: float | None = None
_atexit_registered = False
_streams: dict[tuple[str, int], dict] = {}
_streams_lock = threading.Lock()
_debug_events: list[dict] = []
_debug_lock = threading.Lock()


def last_error() -> str | None:
    return _last_error


def pending_snapshot() -> dict | None:
    return _pending


def last_decode_ts() -> float | None:
    return _last_decode_ts


def capture_mark() -> float:
    """Monotonic stamp of the last captured packet, or 0 if capture never started."""
    try:
        import siseli_local.core as core
        return float(core.LAST_PACKET_TS or 0.0)
    except Exception:
        return 0.0


def saw_inverter_packets(mark: float) -> bool:
    """True when a packet arrived after `mark`."""
    try:
        import siseli_local.core as core
        return float(core.LAST_PACKET_TS or 0.0) > float(mark)
    except Exception:
        return False


def note_snapshot(snapshot: dict) -> None:
    global _pending, _last_decode_ts
    _pending = dict(snapshot)
    _last_decode_ts = time.time()


def note_stream(
    ip: str,
    port: int,
    *,
    encrypted: bool = False,
    readings: dict | None = None,
    payload_bytes: int = 0,
    mqtt_packets: int = 0,
    saw_mqtt: bool = False,
) -> None:
    """Remember a broker the inverter contacted. Readings attach when a publish decodes."""
    host = str(ip or "").strip()
    try:
        port_n = int(port)
    except (TypeError, ValueError):
        return
    if not host or port_n <= 0:
        return
    key = (host, port_n)
    with _streams_lock:
        row = dict(_streams.get(key) or {})
        row["ip"] = host
        row["port"] = port_n
        row["encrypted"] = bool(encrypted) or bool(row.get("encrypted"))
        row["last_seen"] = time.time()
        if payload_bytes:
            row["payload_bytes"] = int(row.get("payload_bytes") or 0) + int(payload_bytes)
        if mqtt_packets:
            row["mqtt_packets"] = int(row.get("mqtt_packets") or 0) + int(mqtt_packets)
        if saw_mqtt or mqtt_packets:
            row["saw_mqtt"] = True
        if readings:
            row["readings"] = dict(readings)
            row["readings_ts"] = time.time()
            row["encrypted"] = False
            row["saw_mqtt"] = True
        _streams[key] = row


def note_debug(event: dict) -> None:
    """Keep a short ring of decode diagnostics for the LAN card to forward."""
    with _debug_lock:
        _debug_events.append(dict(event))
        del _debug_events[:-40]


def debug_events() -> list[dict]:
    with _debug_lock:
        return [dict(event) for event in _debug_events]


def mqtt_streams(*, since: float | None = None) -> list[dict]:
    """Brokers seen so far, newest first. `since` is a time.time() cutoff."""
    with _streams_lock:
        rows = [dict(row) for row in _streams.values()]
    if since is not None:
        rows = [row for row in rows if float(row.get("last_seen") or 0) >= since]
    rows.sort(key=lambda row: float(row.get("last_seen") or 0), reverse=True)
    return rows


def is_running() -> bool:
    from . import state as st

    thread = _thread
    return bool(
        thread is not None
        and thread.is_alive()
        and st.RUNNING
        and not st.STOP_REQUESTED
    )


def _blank(value: object) -> str | None:
    text = str(value or "").strip()
    return text or None


def _apply_runtime(cfg: dict) -> None:
    """Push inverter/router settings into config and the star-imported core."""
    import siseli_local.config as cfgmod
    import siseli_local.core as core

    pairs = {
        "INVERTER_IP": str(cfg.get("inverter_ip") or "").strip(),
        "ROUTER_IP": str(cfg.get("router_ip") or "").strip(),
        "INVERTER_MAC_CFG": _blank(cfg.get("inverter_mac")),
        "ROUTER_MAC_CFG": _blank(cfg.get("router_mac")),
        "SNIFF_IFACE": _blank(cfg.get("sniff_iface")),
        "AUTO_INTERCEPT": True,
        "FORWARD_ALL_INVERTER_TRAFFIC": True,
    }
    if pairs["INVERTER_MAC_CFG"]:
        pairs["INVERTER_MAC_CFG"] = str(pairs["INVERTER_MAC_CFG"]).lower()
    if pairs["ROUTER_MAC_CFG"]:
        pairs["ROUTER_MAC_CFG"] = str(pairs["ROUTER_MAC_CFG"]).lower()
    for name, value in pairs.items():
        setattr(cfgmod, name, value)
        setattr(core, name, value)
    core.INV_MAC = None
    core.RTR_MAC = None
    core.CAPTURE_FAILURES = 0


def _export_env(cfg: dict) -> None:
    os.environ["INVERTER_IP"] = str(cfg.get("inverter_ip") or "").strip()
    os.environ["ROUTER_IP"] = str(cfg.get("router_ip") or "").strip()
    os.environ["INVERTER_MAC"] = str(cfg.get("inverter_mac") or "").strip()
    os.environ["ROUTER_MAC"] = str(cfg.get("router_mac") or "").strip()
    os.environ["SNIFF_IFACE"] = str(cfg.get("sniff_iface") or "").strip()
    os.environ["AUTO_INTERCEPT"] = "true"
    os.environ["FORWARD_ALL_INVERTER_TRAFFIC"] = "true"
    # validate_config refuses an empty MQTT_HOST. The stub never connects.
    os.environ.setdefault("MQTT_HOST", "127.0.0.1")


def _run(cfg: dict) -> None:
    global _last_error
    from . import state as st

    st.RUNNING = True
    st.STOP_REQUESTED = False
    try:
        _export_env(cfg)
        import siseli_local.core as core
        from siseli_local.config import validate_config

        _apply_runtime(cfg)
        try:
            validate_config()
        except SystemExit as exc:
            _last_error = str(exc) or "local read config rejected"
            log.error("siseli local config rejected: %s", _last_error)
            return

        from siseli_local.mqtt import start_mqtt

        core.prepare_startup_state()
        start_mqtt()
        if core.AUTO_INTERCEPT:
            threading.Thread(target=core.arp_spoofer.run, daemon=True).start()
            wait_start = time.monotonic()
            while st.RUNNING and time.monotonic() - wait_start < 15 and (
                not core.INV_MAC or not core.RTR_MAC
            ):
                time.sleep(1)
        else:
            core.INV_MAC = core.norm_mac(core.INVERTER_MAC_CFG)
            core.RTR_MAC = core.norm_mac(core.ROUTER_MAC_CFG)
            core.log("[ARP] AUTO_INTERCEPT disabled; relying on existing network redirection")

        threading.Thread(target=core.health_logger, daemon=True).start()
        core.sniffer = core.build_sniffer()
        core.sniffer.start()
        core.log("[Bridge] Sniffer started", level="info")
        _last_error = None
        while st.RUNNING and not st.STOP_REQUESTED:
            time.sleep(1)
    except Exception as exc:
        _last_error = str(exc) or exc.__class__.__name__
        log.exception("siseli local sniffer failed")
    finally:
        try:
            from . import core as coremod

            # On this thread, with RUNNING still set, so restore_arp finishes
            # instead of dying with the process.
            if coremod is not None:
                coremod.shutdown()
        except Exception:
            log.exception("siseli local shutdown failed")


def start(cfg: dict) -> None:
    """Start or restart the sniffer. Safe to call if it is already running."""
    global _thread, _last_error, _atexit_registered
    stop()
    _last_error = None
    with _lock:
        thread = threading.Thread(
            target=_run, args=(dict(cfg),), name="siseli-local", daemon=False,
        )
        _thread = thread
        thread.start()
        if not _atexit_registered:
            atexit.register(stop)
            _atexit_registered = True


def stop(timeout: float = 8.0) -> None:
    """Ask the sniffer to restore ARP and exit. Idempotent."""
    global _thread
    from . import state as st

    thread = _thread
    if thread is None or not thread.is_alive():
        _thread = None
        return
    st.STOP_REQUESTED = True
    thread.join(timeout)
    if thread.is_alive():
        try:
            import siseli_local.core as core

            core.shutdown()
        except Exception:
            log.exception("siseli local forced shutdown failed")
        thread.join(2.0)
    if thread is _thread and not thread.is_alive():
        _thread = None
