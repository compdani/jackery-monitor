"""Bleak-based scanner/poller for JBD / Overkill / XiaoXiang BMS packs.

Sequential connect-query-disconnect: these modules drop notifications
if two centrals stay connected, and Synology's USB dongle is a single
adapter. Missing bleak or a missing BlueZ socket becomes a structured
error rather than an import-time crash (CI has no Bluetooth).
"""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Any

import jbd_bms

log = logging.getLogger("bms_ble")

SCAN_DEFAULT_S = 8.0
POLL_TIMEOUT_S = 8.0
NOTIFY_WAIT_S = 5.0

_NAME_HINTS = (
    "jbd", "overkill", "xiaoxiang", "xiao xiang", "smartbms", "smart bms",
    "temgot", "sp04s", "sp10s", "sp16s", "sp20s",
)


class BleUnavailable(RuntimeError):
    """bleak/BlueZ not usable in this process."""


def _adapter_lock() -> asyncio.Lock:
    lock = getattr(_adapter_lock, "_lock", None)
    if lock is None:
        lock = asyncio.Lock()
        _adapter_lock._lock = lock  # type: ignore[attr-defined]
    return lock


def bleak_status() -> dict[str, Any]:
    try:
        import bleak  # noqa: F401
    except ImportError as e:
        return {"available": False, "error": f"bleak not installed: {e}"}
    return {"available": True, "error": None}


def _uuid_blob(value: Any) -> str:
    return str(value or "").lower().replace("-", "")


def looks_like_jbd(name: str | None, service_uuids: list[str] | None = None,
                   service_data: Any = None) -> bool:
    n = (name or "").strip().lower()
    if n and any(h in n for h in _NAME_HINTS):
        return True
    for u in service_uuids or []:
        blob = _uuid_blob(u)
        if "ff00" in blob:
            return True
    if isinstance(service_data, dict):
        for key in service_data:
            if "ff00" in _uuid_blob(key):
                return True
    return False


def _adv_uuids(adv: Any) -> list[str]:
    uuids = getattr(adv, "service_uuids", None) or []
    return [str(u) for u in uuids]


def iter_scan_results(found: Any) -> list[tuple[Any, Any]]:
    """Normalize BleakScanner.discover() across bleak versions (dict vs list)."""
    if not found:
        return []
    if isinstance(found, dict):
        return list(found.items())
    return [(dev, None) for dev in found]


async def _discover(timeout: float) -> Any:
    from bleak import BleakScanner
    try:
        return await BleakScanner.discover(
            timeout=timeout, return_adv=True, scanning_mode="active",
        )
    except TypeError:
        # Older bleak: no scanning_mode / return_adv kwargs.
        try:
            return await BleakScanner.discover(timeout=timeout, return_adv=True)
        except TypeError:
            return await BleakScanner.discover(timeout=timeout)


async def scan(timeout: float = SCAN_DEFAULT_S) -> list[dict]:
    """Return nearby BLE advertisements. Likely JBD modules are flagged.

    JBD UART modules often omit a name and 0xFF00 from the advertisement,
    so the UI lists every nearby device and highlights likely matches.
    """
    st = bleak_status()
    if not st["available"]:
        raise BleUnavailable(st["error"])
    try:
        from bleak import BleakScanner  # noqa: F401
    except ImportError as e:
        raise BleUnavailable(str(e)) from e
    lock = _adapter_lock()
    try:
        await asyncio.wait_for(lock.acquire(), timeout=2.0)
    except asyncio.TimeoutError as e:
        raise BleUnavailable("BLE adapter busy (another BMS operation is running)") from e
    try:
        try:
            found = await asyncio.wait_for(
                _discover(timeout), timeout=timeout + 3.0,
            )
        except asyncio.TimeoutError as e:
            raise BleUnavailable(f"BLE scan timed out after {timeout:.0f}s") from e
        except Exception as e:
            raise BleUnavailable(f"BLE scan failed: {e}") from e
    finally:
        lock.release()
    out: list[dict] = []
    seen: set[str] = set()
    for dev, adv in iter_scan_results(found):
        addr = (getattr(dev, "address", None) or "").upper()
        if not addr or addr in seen:
            continue
        name = (
            (getattr(adv, "local_name", None) if adv is not None else None)
            or getattr(dev, "name", None)
        )
        uuids = _adv_uuids(adv) if adv is not None else []
        service_data = getattr(adv, "service_data", None) if adv is not None else None
        rssi = getattr(adv, "rssi", None) if adv is not None else getattr(dev, "rssi", None)
        seen.add(addr)
        out.append({
            "mac": addr,
            "name": name or addr,
            "rssi": rssi,
            "service_uuids": uuids,
            "likely_jbd": looks_like_jbd(name, uuids, service_data),
        })
    out.sort(key=lambda d: (
        0 if d.get("likely_jbd") else 1,
        -(d.get("rssi") or -999),
    ))
    return out


async def poll_one(address: str, timeout: float = POLL_TIMEOUT_S) -> dict[str, Any]:
    """Connect, read 0x03 then 0x04, disconnect. Raises on timeout/parse."""
    st = bleak_status()
    if not st["available"]:
        raise BleUnavailable(st["error"])
    from bleak import BleakClient

    buf = bytearray()
    frames: list[bytes] = []
    got = asyncio.Event()

    def on_notify(_handle, data: bytearray) -> None:
        buf.extend(data)
        extracted, rest = jbd_bms.extract_frames(bytes(buf))
        buf[:] = rest
        if extracted:
            frames.extend(extracted)
            got.set()

    def take_cmd(cmd: int) -> dict[str, Any] | None:
        for i, raw in enumerate(frames):
            try:
                parsed = jbd_bms.parse_frame(raw)
            except jbd_bms.JbdError:
                continue
            if parsed["cmd"] == cmd:
                del frames[i]
                return parsed
        return None

    basic: dict[str, Any] | None = None
    cells: list[int] | None = None
    async with BleakClient(address, timeout=timeout) as client:
        await client.start_notify(jbd_bms.NOTIFY_UUID, on_notify)
        try:
            for cmd in (jbd_bms.CMD_BASIC, jbd_bms.CMD_CELLS):
                parsed = take_cmd(cmd)
                if parsed is None:
                    got.clear()
                    await client.write_gatt_char(
                        jbd_bms.WRITE_UUID,
                        jbd_bms.build_read(cmd),
                        response=False,
                    )
                    deadline = time.monotonic() + min(NOTIFY_WAIT_S, timeout)
                    while parsed is None:
                        remaining = deadline - time.monotonic()
                        if remaining <= 0:
                            raise TimeoutError(f"timeout waiting for 0x{cmd:02x}")
                        try:
                            await asyncio.wait_for(got.wait(), timeout=remaining)
                        except asyncio.TimeoutError as e:
                            raise TimeoutError(f"timeout waiting for 0x{cmd:02x}") from e
                        got.clear()
                        parsed = take_cmd(cmd)
                if cmd == jbd_bms.CMD_BASIC:
                    basic = jbd_bms.parse_basic_info(parsed["payload"])
                else:
                    cells = jbd_bms.parse_cell_voltages(parsed["payload"])
        finally:
            try:
                await client.stop_notify(jbd_bms.NOTIFY_UUID)
            except Exception:
                pass
    if not basic:
        raise jbd_bms.JbdError("no basic-info response")
    reading = jbd_bms.merge_reading(basic, cells)
    reading["ts"] = time.time()
    reading["error"] = None
    return reading


async def poll_all(addresses: list[str], timeout: float = POLL_TIMEOUT_S) -> dict[str, dict]:
    """Sequential poll. Failed MACs land as {error, ts} so the UI can show them."""
    lock = _adapter_lock()
    async with lock:
        out: dict[str, dict] = {}
        for addr in addresses:
            try:
                out[addr] = await poll_one(addr, timeout=timeout)
            except Exception as e:
                log.warning("BMS poll %s failed: %s", addr, e)
                out[addr] = {
                    "ts": time.time(),
                    "error": str(e)[:240],
                    "soc_pct": None,
                }
        return out
