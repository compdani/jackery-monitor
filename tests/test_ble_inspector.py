"""Hardware-free BLE contracts, including forbidden controller writes."""

import asyncio
import json
import sys
from types import SimpleNamespace as NS

import pytest

import ble_inspector as ble


@pytest.fixture
def radio(monkeypatch):
    def char(uuid, handle, props):
        return NS(uuid=uuid, handle=handle, properties=props)

    chars = [
        char("read", 1, ["read"]),
        char("notify", 2, ["notify"]),
        char("write", 3, ["write"]),
        char("bad-read", 4, ["read"]),
    ]
    devices = [
        (
            NS(address="AA", name=None),
            NS(
                local_name=None,
                rssi=0,
                service_uuids=[],
                manufacturer_data={1: b"\x00\xff"},
                service_data={"unknown": b""},
            ),
        )
    ]
    calls = []

    class Scanner:
        def __init__(self, detection_callback):
            self.callback = detection_callback

        async def start(self):
            for device, adv in devices:
                self.callback(device, adv)
                self.callback(device, adv)
            return self

        async def stop(self):
            calls.append("scanner_stop")

    class Client:
        def __init__(self, device, timeout, disconnected_callback):
            calls.append(("device", device))
            self.services = [NS(uuid="service", handle=0, characteristics=chars)]
            self.disconnected_callback = disconnected_callback

        async def connect(self):
            calls.append("connect")

        async def disconnect(self):
            calls.append("disconnect")

        async def read_gatt_char(self, char):
            calls.append(("read", char.handle))
            if char.handle == 4:
                raise RuntimeError("unreadable now")
            return b"\x00\xffhello"

        async def start_notify(self, char, callback):
            calls.append(("notify", char.handle))
            callback(char, bytearray(b"\xff\x00"))
            callback(char, bytearray())

        async def stop_notify(self, char):
            calls.append(("stop_notify", char.handle))

        async def write_gatt_char(self, *args, **kwargs):
            pytest.fail("forbidden write")

        async def write_gatt_descriptor(self, *args, **kwargs):
            pytest.fail("forbidden descriptor write")

    monkeypatch.setitem(sys.modules, "bleak", NS(BleakScanner=Scanner, BleakClient=Client))
    return NS(calls=calls, chars=chars, devices=devices, Client=Client)


async def execute(tmp_path, **options):
    options = {
        "operation": "inspect",
        "address": "AA",
        "seconds": 0.001,
        "duration": 0.001,
        **options,
    }
    path = tmp_path / "capture.jsonl"
    capture = ble.Capture(path, options)
    status = await ble.run(capture, options)
    return status, ble.events(path)


@pytest.mark.asyncio
async def test_structure_only_preserves_advertisements(tmp_path, radio):
    status, rows = await execute(tmp_path)
    assert status == "complete"
    adv = [r for r in rows if r["type"] == "advertisement"]
    assert len(adv) == 2 and adv[0]["name"] is None and adv[0]["rssi"] == 0
    assert adv[0]["manufacturer_data"]["1"]["hex"] == "00ff"
    assert adv[0]["service_data"]["unknown"]["length"] == 0
    assert not any(isinstance(c, tuple) and c[0] in ("read", "notify") for c in radio.calls)
    assert radio.calls[-1] == "disconnect"
    assert [r["seq"] for r in rows] == list(range(1, len(rows) + 1))


@pytest.mark.asyncio
async def test_read_and_explicit_notifications(tmp_path, radio):
    status, rows = await execute(tmp_path, read_all=True, notify=["notify"])
    assert status == "complete"
    assert [("read", 1), ("read", 4)] == [
        c for c in radio.calls if isinstance(c, tuple) and c[0] == "read"
    ]
    assert radio.calls.index(("read", 4)) < radio.calls.index(("notify", 2))
    assert any(r["type"] == "read_error" for r in rows)
    notifications = [r for r in rows if r["type"] == "notification"]
    assert [r["hex"] for r in notifications] == ["ff00", ""]
    assert ("stop_notify", 2) in radio.calls


def test_duplicate_and_unsupported_selectors(radio):
    with pytest.raises(ValueError):
        ble.select_notifications(radio.chars, ["write"])
    radio.chars.append(NS(uuid="notify", handle=5, properties=["indicate"]))
    with pytest.raises(ValueError):
        ble.select_notifications(radio.chars, ["notify"])
    assert ble.select_notifications(radio.chars, ["0x5"])[0].handle == 5


@pytest.mark.asyncio
async def test_ambiguous_name(tmp_path, radio):
    dev, adv = radio.devices[0]
    adv.local_name = "SILELI"
    radio.devices.append((NS(address="BB", name="sileli"), adv))
    status, rows = await execute(tmp_path, address=None, name="sile")
    assert status == "incomplete"
    assert "Candidates" in rows[-1]["error"] and "BB" in rows[-1]["error"]
    assert "connect" not in radio.calls


@pytest.mark.asyncio
async def test_no_match(tmp_path, radio):
    status, rows = await execute(tmp_path, address="missing")
    assert status == "incomplete" and "found 0" in rows[-1]["error"]


@pytest.mark.asyncio
async def test_cancellation_flushes_and_disconnects(tmp_path, radio):
    path = tmp_path / "partial.jsonl"
    capture = ble.Capture(path, {})
    task = asyncio.create_task(
        ble.run(
            capture,
            {
                "operation": "inspect",
                "address": "AA",
                "seconds": 0.001,
                "notify": ["notify"],
                "duration": 100,
            },
        )
    )

    async def wait_until_listening():
        while not any(r["type"] == "listening_started" for r in ble.events(path)):
            await asyncio.sleep(0.001)

    await asyncio.wait_for(wait_until_listening(), 1)
    task.cancel()
    assert await task == "cancelled"
    rows = ble.events(path)
    assert rows[-1]["status"] == "cancelled"
    assert listening_summary(rows)["reason"] == "cancelled"
    assert listening_summary(rows)["subscribed_count"] == 1
    assert radio.calls[-1] == "disconnect" and capture.file.closed


@pytest.mark.asyncio
async def test_disconnect(tmp_path, radio, monkeypatch):
    async def subscribe(self, char, callback):
        self.disconnected_callback(self)

    monkeypatch.setattr(radio.Client, "start_notify", subscribe)
    status, rows = await execute(tmp_path, notify=["notify"])
    assert status == "incomplete" and "disconnected" in rows[-1]["error"]
    assert listening_summary(rows)["reason"] == "disconnected"


@pytest.mark.asyncio
async def test_read_timeout(tmp_path, radio, monkeypatch):
    async def slow(self, char):
        await asyncio.sleep(10)

    monkeypatch.setattr(radio.Client, "read_gatt_char", slow)
    status, rows = await execute(tmp_path, read_all=True, timeout=0.001)
    assert status == "complete"
    assert len([r for r in rows if r["type"] == "read_error"]) == 2


def test_limit_and_pagination(tmp_path, monkeypatch):
    path = tmp_path / "limited.jsonl"
    cap = ble.Capture(path, {})
    cap.emit("read", **ble.payload(b"\x00\xff"))
    assert ble.events(path, after=1, limit=1)[0]["hex"] == "00ff"
    monkeypatch.setattr(ble, "MAX_BYTES", cap.size + 4100)
    with pytest.raises(OSError):
        cap.emit("advertisement", extra="x" * 100)
    cap.finish("incomplete", cap.failure)
    assert ble.events(path)[-1]["status"] == "incomplete"
    assert cap.failed.is_set()


@pytest.mark.asyncio
async def test_changing_advertisements(tmp_path, radio, monkeypatch):
    scanner = sys.modules["bleak"].BleakScanner

    async def changed(self):
        device, adv = radio.devices[0]
        self.callback(device, adv)
        adv.manufacturer_data = {1: b"\x02"}
        adv.local_name = "new name"
        self.callback(device, adv)

    monkeypatch.setattr(scanner, "start", changed)
    status, rows = await execute(tmp_path, operation="scan")
    adv = [r for r in rows if r["type"] == "advertisement"]
    assert [r["manufacturer_data"]["1"]["hex"] for r in adv] == ["00ff", "02"]
    assert rows[-2]["devices"][0]["name"] == "new name"


@pytest.mark.asyncio
async def test_connect_timeout_disconnects(tmp_path, radio, monkeypatch):
    async def slow(self):
        await asyncio.sleep(100)

    monkeypatch.setattr(radio.Client, "connect", slow)
    status, rows = await execute(tmp_path, timeout=0.001)
    assert status == "incomplete" and radio.calls[-1] == "disconnect"


@pytest.mark.asyncio
async def test_changed_handle_rejected(tmp_path, radio):
    status, rows = await execute(tmp_path, notify=["2"], notify_expected={"2": "old-uuid"})
    assert status == "incomplete" and "structure changed" in rows[-1]["error"]
    assert ("notify", 2) not in radio.calls


@pytest.mark.asyncio
async def test_storage_failure_retains_journal(tmp_path, radio):
    path = tmp_path / "disk.jsonl"
    capture = ble.Capture(path, {})
    original = capture.file

    class FullDisk:
        def write(self, line):
            raise OSError("disk full")

        def close(self):
            original.close()

    capture.file = FullDisk()
    status = await ble.run(capture, {"operation": "scan", "seconds": 0.001})
    assert status == "incomplete" and capture.failure == "disk full"
    assert ble.events(path)[0]["type"] == "metadata"
    assert original.closed


def test_cli_json_export(tmp_path, radio, monkeypatch):
    output = tmp_path / "cli.json"
    monkeypatch.setattr(
        sys, "argv", ["ble_inspector.py", "scan", "--seconds", "1", "--output", str(output)]
    )
    assert ble.main() == 0
    rows = json.loads(output.read_text())["events"]
    assert rows[-1]["status"] == "complete"
    assert output.with_suffix(".json.jsonl").exists()


def listening_summary(rows):
    return next(r for r in rows if r["type"] == "listening_ended")


@pytest.mark.asyncio
async def test_successful_notify_and_indicate_summary(tmp_path, radio):
    radio.chars.append(NS(uuid="indicate", handle=5, properties=["indicate"]))
    status, rows = await execute(tmp_path, notify=["notify", "5"])
    assert status == "complete"
    summary = listening_summary(rows)
    assert summary["reason"] == "duration_elapsed"
    assert summary["subscribed_count"] == 2 and summary["failed_count"] == 0
    assert summary["event_count"] == 4
    assert 0 < summary["elapsed_s"] < 1
    assert [c["event_count"] for c in summary["channels"]] == [2, 2]
    assert summary["channels"][1]["subscription_properties"] == ["indicate"]
    assert all(c["unsubscribe_status"] == "stopped" for c in summary["channels"])
    assert len([r for r in rows if r["type"] == "subscription_requested"]) == 2
    assert len([r for r in rows if r["type"] == "subscription_active"]) == 2
    assert len([r for r in rows if r["type"] == "unsubscribe_result"]) == 2
    assert not any("mode" in r for r in rows if r["type"] == "notification")


@pytest.mark.asyncio
async def test_partial_subscription_failure_keeps_other_channels(tmp_path, radio, monkeypatch):
    radio.chars.append(NS(uuid="indicate", handle=5, properties=["indicate"]))

    async def subscribe(self, char, callback):
        radio.calls.append(("notify", char.handle))
        if char.handle == 2:
            raise RuntimeError("subscription denied")
        callback(char, bytearray(b"\x00\xfe"))

    monkeypatch.setattr(radio.Client, "start_notify", subscribe)
    status, rows = await execute(tmp_path, notify=["notify", "5"])
    assert status == "incomplete"
    summary = listening_summary(rows)
    assert summary["reason"] == "duration_elapsed"
    assert summary["subscribed_count"] == summary["failed_count"] == summary["event_count"] == 1
    assert summary["channels"][0]["status"] == "failed"
    assert summary["channels"][0]["error"] == "subscription denied"
    assert ("notify", 5) in radio.calls and ("stop_notify", 5) in radio.calls
    assert ("stop_notify", 2) not in radio.calls
    assert any(r["type"] == "listening_started" for r in rows)


@pytest.mark.asyncio
async def test_all_subscriptions_fail_without_listening(tmp_path, radio, monkeypatch):
    async def denied(self, char, callback):
        raise RuntimeError("denied")

    monkeypatch.setattr(radio.Client, "start_notify", denied)
    status, rows = await execute(tmp_path, notify=["notify"])
    assert status == "incomplete"
    summary = listening_summary(rows)
    assert summary["reason"] == "all_subscriptions_failed"
    assert summary["elapsed_s"] == 0 and summary["subscribed_count"] == 0
    assert summary["failed_count"] == 1 and summary["event_count"] == 0
    assert not any(r["type"] == "listening_started" for r in rows)


@pytest.mark.asyncio
async def test_zero_events_after_success_is_complete(tmp_path, radio, monkeypatch):
    async def silent(self, char, callback):
        pass

    monkeypatch.setattr(radio.Client, "start_notify", silent)
    status, rows = await execute(tmp_path, notify=["notify"])
    summary = listening_summary(rows)
    assert status == "complete" and summary["subscribed_count"] == 1
    assert summary["event_count"] == 0 and summary["reason"] == "duration_elapsed"
    assert summary["channels"][0]["status"] == "active"


@pytest.mark.asyncio
async def test_no_selection_is_distinct_from_silent_subscription(tmp_path, radio):
    status, rows = await execute(tmp_path)
    summary = listening_summary(rows)
    assert status == "complete"
    assert summary["reason"] == "no_subscriptions_selected"
    assert summary["channels"] == [] and summary["elapsed_s"] == 0
    assert not any(r["type"] == "listening_started" for r in rows)


@pytest.mark.asyncio
async def test_read_rejections_do_not_block_subscriptions(tmp_path, radio, monkeypatch):
    async def denied(self, char):
        raise RuntimeError("READ_NOT_PERMITTED")

    monkeypatch.setattr(radio.Client, "read_gatt_char", denied)
    status, rows = await execute(tmp_path, read_all=True, notify=["notify"])
    assert status == "complete"
    assert len([r for r in rows if r["type"] == "read_error"]) == 2
    assert listening_summary(rows)["event_count"] == 2
    assert any(r["type"] == "subscription_active" for r in rows)


@pytest.mark.asyncio
async def test_duplicate_uuid_subscriptions_use_handles(tmp_path, radio):
    radio.chars.append(NS(uuid="notify", handle=5, properties=["indicate"]))
    status, rows = await execute(tmp_path, notify=["2", "5"])
    assert status == "complete"
    assert [c["handle"] for c in listening_summary(rows)["channels"]] == [2, 5]


@pytest.mark.asyncio
async def test_subscription_timeout_is_recorded(tmp_path, radio, monkeypatch):
    async def slow(self, char, callback):
        await asyncio.sleep(10)

    monkeypatch.setattr(radio.Client, "start_notify", slow)
    status, rows = await execute(tmp_path, notify=["notify"], timeout=0.001)
    assert status == "incomplete"
    error = next(r for r in rows if r["type"] == "subscription_error")
    assert error["error"] == "TimeoutError"
    assert listening_summary(rows)["reason"] == "all_subscriptions_failed"


@pytest.mark.asyncio
async def test_unsubscribe_error_recorded_and_other_channels_cleaned(tmp_path, radio, monkeypatch):
    radio.chars.append(NS(uuid="indicate", handle=5, properties=["indicate"]))

    async def stop(self, char):
        radio.calls.append(("stop_notify", char.handle))
        if char.handle == 2:
            raise RuntimeError("stop failed")

    monkeypatch.setattr(radio.Client, "stop_notify", stop)
    status, rows = await execute(tmp_path, notify=["2", "5"])
    assert status == "incomplete" and ("stop_notify", 5) in radio.calls
    summary = listening_summary(rows)
    assert summary["channels"][0]["unsubscribe_error"] == "stop failed"
    assert summary["channels"][1]["unsubscribe_status"] == "stopped"


@pytest.mark.asyncio
async def test_storage_failure_while_listening_still_cleans_up(tmp_path, radio, monkeypatch):
    path = tmp_path / "failed.jsonl"
    cap = ble.Capture(path, {})
    emit = cap.emit

    def fail_payload(kind, **data):
        if kind == "notification":
            cap.failure = "disk full"
            cap.failed.set()
            raise OSError("disk full")
        return emit(kind, **data)

    monkeypatch.setattr(cap, "emit", fail_payload)
    status = await ble.run(
        cap,
        {
            "operation": "inspect",
            "address": "AA",
            "seconds": 0.001,
            "notify": ["notify"],
            "duration": 100,
        },
    )
    rows = ble.events(path)
    assert status == "incomplete" and cap.file.closed
    assert listening_summary(rows)["reason"] == "storage_failure"
    assert ("stop_notify", 2) in radio.calls and radio.calls[-1] == "disconnect"


@pytest.mark.asyncio
async def test_limit_reserves_space_for_summary(tmp_path, radio, monkeypatch):
    path = tmp_path / "limit-listen.jsonl"
    cap = ble.Capture(path, {})
    emit = cap.emit

    def limit(kind, **data):
        if kind == "notification":
            monkeypatch.setattr(ble, "MAX_BYTES", cap.size + 4096)
        return emit(kind, **data)

    monkeypatch.setattr(cap, "emit", limit)
    status = await ble.run(
        cap,
        {
            "operation": "inspect",
            "address": "AA",
            "seconds": 0.001,
            "notify": ["notify"],
            "duration": 100,
        },
    )
    rows = ble.events(path)
    assert status == "incomplete"
    assert listening_summary(rows)["reason"] == "storage_failure"
    assert rows[-1]["status"] == "incomplete"
    assert path.stat().st_size <= ble.MAX_BYTES


@pytest.mark.asyncio
async def test_cancel_during_cleanup_retains_final_summary(tmp_path, radio, monkeypatch):
    entered = asyncio.Event()
    release = asyncio.Event()

    async def stop(self, char):
        entered.set()
        await release.wait()
        radio.calls.append(("stop_notify", char.handle))

    monkeypatch.setattr(radio.Client, "stop_notify", stop)
    path = tmp_path / "cleanup.jsonl"
    cap = ble.Capture(path, {})
    task = asyncio.create_task(
        ble.run(
            cap,
            {
                "operation": "inspect",
                "address": "AA",
                "seconds": 0.001,
                "duration": 0.001,
                "notify": ["notify"],
            },
        )
    )
    await asyncio.wait_for(entered.wait(), 1)
    task.cancel()
    release.set()
    assert await task == "cancelled"
    rows = ble.events(path)
    assert listening_summary(rows)["reason"] == "cancelled"
    assert listening_summary(rows)["channels"][0]["unsubscribe_status"] == "stopped"
    assert radio.calls[-1] == "disconnect"


@pytest.mark.asyncio
async def test_cancel_during_subscription_distinguishes_unconfirmed(tmp_path, radio, monkeypatch):
    entered = asyncio.Event()

    async def subscribe(self, char, callback):
        entered.set()
        await asyncio.sleep(100)

    monkeypatch.setattr(radio.Client, "start_notify", subscribe)
    path = tmp_path / "unconfirmed.jsonl"
    cap = ble.Capture(path, {})
    task = asyncio.create_task(
        ble.run(
            cap, {"operation": "inspect", "address": "AA", "seconds": 0.001, "notify": ["notify"]}
        )
    )
    await asyncio.wait_for(entered.wait(), 1)
    task.cancel()
    assert await task == "cancelled"
    summary = listening_summary(ble.events(path))
    assert summary["reason"] == "cancelled"
    assert summary["channels"][0]["status"] == "interrupted"
    assert summary["subscribed_count"] == 0 and summary["elapsed_s"] == 0
