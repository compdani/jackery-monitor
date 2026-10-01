"""Publish hook the vendored parser already calls.

fadmaz/siseli-ha sends this to a Home Assistant broker. Here it forwards
the decoded snapshot to whoever registered a sink (the dashboard). No
broker and no discovery.
"""
from __future__ import annotations

import contextvars
from typing import Callable

from . import state as _state

# Set on the capture thread for the duration of one inverter TCP segment so
# the decoded snapshot can be labeled with the broker it came from. The
# health thread republishes without this set, and must not inherit it.
_broker_ip: contextvars.ContextVar[str | None] = contextvars.ContextVar(
    "siseli_broker_ip", default=None,
)

SnapshotSink = Callable[[dict], None]
_sink: SnapshotSink | None = None


def set_sink(fn: SnapshotSink | None) -> None:
    global _sink
    _sink = fn


def publish_sensor_discovery(key: str) -> None:
    _state.PUBLISHED_SENSOR_KEYS.add(key)


def current_broker() -> str | None:
    return _broker_ip.get()


def bind_broker(ip: str | None):
    """Label publishes on this thread with the broker IP. Returns a reset token."""
    return _broker_ip.set(ip or None)


def unbind_broker(token) -> None:
    _broker_ip.reset(token)


def publish_grouped_state(snapshot: dict) -> bool:
    """Hand the decoded snapshot to the app. True when the sink accepts it."""
    fn = _sink
    if fn is None:
        return False
    snap = dict(snapshot)
    broker = _broker_ip.get()
    if broker:
        snap["broker_ip"] = broker
    try:
        fn(snap)
    except Exception:
        return False
    return True


def publish_availability(_online: bool) -> None:
    return None


def broker_is_connected() -> bool:
    # There is no broker. The health line treats this as "the sink is up".
    return _sink is not None


def start_mqtt() -> None:
    # parse_payload drops the publish when this is false ("no broker yet"),
    # which would also skip the sink. The snapshot still lands in LAST_STATE
    # either way; this flag is what lets the sink run.
    _state.DISCOVERY_PUBLISHED = True


class _Client:
    def disconnect(self) -> None:
        return None

    def loop_stop(self) -> None:
        return None


client = _Client()
