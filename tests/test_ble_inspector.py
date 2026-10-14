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
    while ("notify", 2) not in radio.calls:
        await asyncio.sleep(0.001)
    task.cancel()
    assert await task == "cancelled"
    assert ble.events(path)[-1]["status"] == "cancelled"
    assert radio.calls[-1] == "disconnect" and capture.file.closed


@pytest.mark.asyncio
async def test_disconnect(tmp_path, radio, monkeypatch):
    async def subscribe(self, char, callback):
        self.disconnected_callback(self)

    monkeypatch.setattr(radio.Client, "start_notify", subscribe)
    status, rows = await execute(tmp_path, notify=["notify"])
    assert status == "incomplete" and "disconnected" in rows[-1]["error"]


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
