"""Read-only BLE advertisement/GATT inspector. No controller command writes."""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from pathlib import Path

MAX_BYTES = 100_000_000


def payload(data: bytes) -> dict:
    raw = bytes(data)
    return {
        "hex": raw.hex(),
        "length": len(raw),
        "text_guess": raw.decode("utf-8", errors="backslashreplace"),
        "interpretation": "UTF-8 interpretation guess",
    }


class Capture:
    """Append and flush each event; JSON outputs also have a recovery JSONL sidecar."""

    def __init__(self, path: Path, options: dict, echo=False):
        self.path = path
        self.seq = 0
        self.size = 0
        self.echo = echo
        self.failure = None
        self.failed = asyncio.Event()
        self.file = path.open("x", encoding="utf-8")
        try:
            self.emit("metadata", schema_version=1, options=options)
        except OSError:
            self.file.close()
            raise

    def emit(self, kind: str, **data):
        event = {
            "seq": self.seq + 1,
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "type": kind,
            **data,
        }
        line = json.dumps(event, ensure_ascii=True) + "\n"
        try:
            # Reserve room for bounded cleanup records and a final summary at the size limit.
            terminal = kind in {"completion", "listening_ended", "unsubscribe_result"}
            if self.size + len(line.encode()) > MAX_BYTES - (0 if terminal else 131072):
                raise OSError("100 MB capture limit reached")
            self.file.write(line)
            self.file.flush()
        except OSError as exc:
            self.failure = str(exc)
            self.failed.set()
            raise
        self.seq += 1
        self.size += len(line.encode())
        if self.echo:
            print(line, end="", flush=True)

    def finish(self, status, error=None):
        try:
            self.emit("completion", status=status, error=error)
        except OSError:
            pass  # Existing flushed events remain recoverable on storage failure.
        finally:
            self.file.close()


def events(path: Path, after=0, limit=None):
    result = []
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                break  # A reader may race the final append.
            if event["seq"] > after:
                result.append(event)
                if limit and len(result) >= limit:
                    break
    return result


def json_chunks(path):
    """Export journals without loading a large capture into Pi RAM."""
    yield '{"schema_version":1,"events":['
    first = True
    with path.open(encoding="utf-8") as source:
        for line in source:
            try:
                json.loads(line)
            except ValueError:
                break
            yield ("" if first else ",") + line.strip()
            first = False
    yield "]}"


@asynccontextmanager
async def connected(client, timeout):
    try:
        await asyncio.wait_for(client.connect(), timeout)
        yield client
    finally:
        await asyncio.wait_for(client.disconnect(), timeout)


async def scan(capture, seconds=10, name=None, address=None):
    from bleak import BleakScanner

    found = {}

    def detected(device, adv):
        device_name = adv.local_name or device.name
        if name and name.casefold() not in (device_name or "").casefold():
            return
        if address and address.casefold() != device.address.casefold():
            return
        row = {
            "address": device.address,
            "name": device_name,
            "rssi": adv.rssi,
            "service_uuids": list(adv.service_uuids),
            "manufacturer_data": {str(k): payload(v) for k, v in adv.manufacturer_data.items()},
            "service_data": {k: payload(v) for k, v in adv.service_data.items()},
        }
        try:
            capture.emit("advertisement", **row)
        except OSError:
            return
        found[device.address] = (device, row)

    scanner = BleakScanner(detection_callback=detected)
    try:
        await asyncio.wait_for(scanner.start(), 15)
        try:
            await asyncio.wait_for(capture.failed.wait(), seconds)
        except TimeoutError:
            pass
        if capture.failure:
            raise OSError(capture.failure)
    finally:
        await asyncio.wait_for(scanner.stop(), 15)
    capture.emit("scan_summary", devices=[row for _, row in found.values()])
    return found


def characteristic_info(char):
    return {"uuid": char.uuid, "handle": char.handle, "properties": list(char.properties)}


def select_notifications(chars, selections):
    selected = []
    for key in selections:
        try:
            handle = int(str(key), 0)
        except ValueError:
            handle = None
        matches = [
            c
            for c in chars
            if (
                c.handle == handle
                if handle is not None
                else c.uuid.casefold() == str(key).casefold()
            )
        ]
        if len(matches) != 1:
            raise ValueError(
                f"Notification selector {key}: expected one match, found {len(matches)}; use handle"
            )
        char = matches[0]
        if not {"notify", "indicate"}.intersection(char.properties):
            raise ValueError(f"Characteristic {key} does not support notify/indicate")
        if char not in selected:
            selected.append(char)
    return selected


async def inspect(capture, options):
    from bleak import BleakClient

    found = await scan(
        capture, options.get("seconds", 10), options.get("name"), options.get("address")
    )
    if len(found) != 1:
        candidates = [row for _, row in found.values()]
        raise ValueError(
            f"Expected one device, found {len(found)}. Candidates: {json.dumps(candidates)}"
        )
    device, row = next(iter(found.values()))
    capture.emit("target", **row)
    disconnected = asyncio.Event()
    timeout = options.get("timeout", 15)
    async with connected(
        BleakClient(device, timeout=timeout, disconnected_callback=lambda _: disconnected.set()),
        timeout,
    ) as client:
        chars = []
        for service in client.services:
            chars.extend(service.characteristics)
            capture.emit(
                "service",
                uuid=service.uuid,
                handle=service.handle,
                characteristics=[characteristic_info(c) for c in service.characteristics],
            )
        selected = select_notifications(chars, options.get("notify", []))
        expected = options.get("notify_expected", {})
        for char in selected:
            if (
                str(char.handle) in expected
                and expected[str(char.handle)].casefold() != char.uuid.casefold()
            ):
                raise ValueError("GATT structure changed; inspect again before subscribing")
        await inspect_values(capture, client, chars, selected, disconnected, options)


async def inspect_values(capture, client, chars, selected, disconnected, options):
    """Track requested subscriptions independently, including silent successful streams."""
    timeout = options.get("timeout", 15)
    duration = options.get("duration", 30)
    loop = asyncio.get_running_loop()
    channels = {
        char.handle: {
            **characteristic_info(char),
            "subscription_properties": [p for p in char.properties if p in ("notify", "indicate")],
            "status": "not_attempted",
            "event_count": 0,
        }
        for char in selected
    }
    subscribed = []
    listening_at = None
    reason = "no_subscriptions_selected" if not selected else "error"
    failures = 0
    cleanup_failures = 0

    def received(sender, data):
        channel = channels[sender.handle]
        channel["event_count"] += 1
        try:
            capture.emit("notification", **characteristic_info(sender), **payload(data))
        except OSError:
            pass  # capture.failed wakes the listener; preserve already flushed records.

    try:
        for char in selected:
            channels[char.handle]["status"] = "requested"
            capture.emit("subscription_requested", **channels[char.handle])
        if options.get("read_all"):
            for char in chars:
                if disconnected.is_set():
                    raise ConnectionError("Device disconnected")
                if "read" not in char.properties:
                    continue
                info = characteristic_info(char)
                try:
                    data = await asyncio.wait_for(client.read_gatt_char(char), timeout)
                except Exception as exc:
                    capture.emit("read_error", **info, error=str(exc) or type(exc).__name__)
                else:
                    capture.emit("read", **info, **payload(data))
        for char in selected:
            if capture.failure:
                raise OSError(capture.failure)
            if disconnected.is_set():
                raise ConnectionError("Device disconnected")
            channel = channels[char.handle]
            channel["status"] = "starting"
            try:
                await asyncio.wait_for(client.start_notify(char, received), timeout)
            except Exception as exc:
                failures += 1
                channel.update(status="failed", error=str(exc) or type(exc).__name__)
                capture.emit("subscription_error", **channel)
                continue
            subscribed.append(char)
            channel["status"] = "active"
            capture.emit("subscription_active", **channel)
        if subscribed:
            listening_at = loop.time()
            capture.emit(
                "listening_started",
                duration_s=duration,
                channels=[dict(channels[c.handle]) for c in subscribed],
            )
            waits = [asyncio.create_task(e.wait()) for e in (disconnected, capture.failed)]
            try:
                await asyncio.wait(waits, timeout=duration, return_when=asyncio.FIRST_COMPLETED)
            finally:
                for task in waits:
                    task.cancel()
                await asyncio.gather(*waits, return_exceptions=True)
        if capture.failure:
            raise OSError(capture.failure)
        if disconnected.is_set():
            raise ConnectionError("Device disconnected")
        reason = (
            "duration_elapsed"
            if subscribed
            else ("all_subscriptions_failed" if selected else "no_subscriptions_selected")
        )
    except asyncio.CancelledError:
        reason = "cancelled"
        raise
    except Exception:
        reason = (
            "storage_failure"
            if capture.failure
            else ("disconnected" if disconnected.is_set() else "error")
        )
        raise
    finally:
        # Measure the listening window separately from scanning, reads and cleanup.
        elapsed = loop.time() - listening_at if listening_at is not None else 0.0

        async def finalize():
            nonlocal cleanup_failures, reason
            for char in subscribed:
                channel = channels[char.handle]
                try:
                    await asyncio.wait_for(client.stop_notify(char), timeout)
                except Exception as exc:
                    cleanup_failures += 1
                    channel.update(
                        unsubscribe_status="failed",
                        unsubscribe_error=str(exc) or type(exc).__name__,
                    )
                else:
                    channel["unsubscribe_status"] = "stopped"
                try:
                    capture.emit("unsubscribe_result", **channel)
                except OSError:
                    reason = "storage_failure"
            for channel in channels.values():
                if channel["status"] == "requested":
                    channel["status"] = "not_attempted"
                elif channel["status"] == "starting":
                    channel.update(
                        status="interrupted", error="Subscription activation was not confirmed"
                    )
            try:
                capture.emit(
                    "listening_ended",
                    reason=reason,
                    duration_s=duration,
                    elapsed_s=elapsed,
                    subscribed_count=len(subscribed),
                    failed_count=failures,
                    event_count=sum(c["event_count"] for c in channels.values()),
                    channels=list(channels.values()),
                )
            except OSError:
                pass

        # Stop can arrive during unsubscribe as well as during the listening window.
        cleanup = asyncio.create_task(finalize())
        try:
            await asyncio.shield(cleanup)
        except asyncio.CancelledError:
            reason = "cancelled"
            await asyncio.shield(cleanup)
            raise
    if capture.failure:
        raise OSError(capture.failure)
    if failures or cleanup_failures:
        raise RuntimeError(
            f"{failures} subscription(s) failed; {cleanup_failures} unsubscribe(s) failed"
        )


async def run(capture, options):
    status, error = "complete", None
    try:
        if options["operation"] == "scan":
            await scan(
                capture, options.get("seconds", 10), options.get("name"), options.get("address")
            )
        else:
            await inspect(capture, options)
    except asyncio.CancelledError:
        status, error = "cancelled", "Stopped by user or server shutdown"
    except Exception as exc:
        status, error = "incomplete", str(exc)
        try:
            capture.emit("error", error=error)
        except OSError:
            pass
    finally:
        capture.finish(status, error)
    return status


def bounded(low, high):
    def parse(value):
        number = float(value)
        if not low <= number <= high:
            raise argparse.ArgumentTypeError(f"must be between {low} and {high}")
        return number

    return parse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="operation", required=True)
    for operation in ("scan", "inspect"):
        command = sub.add_parser(operation)
        command.add_argument("--seconds", type=bounded(1, 120), default=10)
        target = command.add_mutually_exclusive_group(required=operation == "inspect")
        target.add_argument("--name")
        target.add_argument("--address")
        command.add_argument("--output", type=Path)
        if operation == "inspect":
            command.add_argument("--read-all", action="store_true")
            command.add_argument("--notify", action="append", default=[])
            command.add_argument("--duration", type=bounded(1, 3600), default=30)
            command.add_argument("--timeout", type=bounded(1, 120), default=15)
    args = vars(parser.parse_args())
    output = args.pop("output")
    path = output or Path("ble-" + datetime.now().strftime("%Y%m%d-%H%M%S") + ".jsonl")
    journal = path.with_suffix(path.suffix + ".jsonl") if path.suffix == ".json" else path
    try:
        capture = Capture(journal, args, echo=True)
        status = asyncio.run(run(capture, args))
        if path.suffix == ".json":
            with path.open("x", encoding="utf-8") as stream:
                stream.writelines(json_chunks(journal))
        return 0 if status == "complete" else 1
    except (OSError, KeyboardInterrupt) as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
