"""Xiaoxiang / JBD / Overkill Solar BMS frame codec.

BLE service 0xFF00, notify 0xFF01, write 0xFF02. Request/response
frames are the same ones used by the Overkill Android app and the
community BLE readers (tgalarneau/bms, esphome-jbd-bms). No I/O here
— `bms_ble.py` talks to BlueZ.
"""

from __future__ import annotations

from typing import Any

START = 0xDD
END = 0x77
READ = 0xA5
WRITE = 0x5A

CMD_BASIC = 0x03
CMD_CELLS = 0x04

# 16-bit BLE UUIDs advertised by the JBD UART module.
SERVICE_UUID = "0000ff00-0000-1000-8000-00805f9b34fb"
NOTIFY_UUID = "0000ff01-0000-1000-8000-00805f9b34fb"
WRITE_UUID = "0000ff02-0000-1000-8000-00805f9b34fb"

PROTECTION_BITS = (
    "cell_overvoltage",
    "cell_undervoltage",
    "pack_overvoltage",
    "pack_undervoltage",
    "charge_overtemp",
    "charge_undertemp",
    "discharge_overtemp",
    "discharge_undertemp",
    "charge_overcurrent",
    "discharge_overcurrent",
    "short_circuit",
    "front_ic_error",
    "software_lock",
)


class JbdError(ValueError):
    """Malformed or checksum-failed JBD frame."""


def checksum(status: int, length: int, payload: bytes) -> int:
    """Two-byte checksum: 0x10000 minus (status + len + payload bytes)."""
    total = status + length + sum(payload)
    return (0x10000 - total) & 0xFFFF


def build_read(cmd: int) -> bytes:
    """Read request: DD A5 CMD 00 CKH CKL 77."""
    length = 0
    ck = checksum(cmd, length, b"")
    return bytes([START, READ, cmd & 0xFF, length, (ck >> 8) & 0xFF, ck & 0xFF, END])


def build_response(cmd: int, payload: bytes, status: int = 0) -> bytes:
    """Build a valid response frame (used by tests and the mock poller)."""
    length = len(payload)
    ck = checksum(status, length, payload)
    return bytes([START, cmd & 0xFF, status & 0xFF, length]) + payload + bytes([
        (ck >> 8) & 0xFF, ck & 0xFF, END,
    ])


def extract_frames(buf: bytes) -> tuple[list[bytes], bytes]:
    """Pull complete DD…77 frames out of a notify buffer.

    BLE notifications often fragment a single response. Returns
    (complete_frames, leftover_bytes).
    """
    frames: list[bytes] = []
    i = 0
    n = len(buf)
    while i < n:
        start = buf.find(bytes([START]), i)
        if start < 0:
            return frames, b""
        if start + 4 > n:
            return frames, buf[start:]
        length = buf[start + 3]
        total = 4 + length + 3  # header + payload + ckh + ckl + end
        if start + total > n:
            return frames, buf[start:]
        if buf[start + total - 1] != END:
            i = start + 1
            continue
        frames.append(buf[start:start + total])
        i = start + total
    return frames, buf[i:] if i < n else b""


def parse_frame(raw: bytes) -> dict[str, Any]:
    """Decode one complete response. Raises JbdError on garbage."""
    if len(raw) < 7 or raw[0] != START or raw[-1] != END:
        raise JbdError("not a JBD frame")
    cmd = raw[1]
    status = raw[2]
    length = raw[3]
    if len(raw) != 4 + length + 3:
        raise JbdError(f"length mismatch: header {length} frame {len(raw)}")
    payload = raw[4:4 + length]
    ck = (raw[-3] << 8) | raw[-2]
    expected = checksum(status, length, payload)
    if ck != expected:
        raise JbdError(f"checksum 0x{ck:04x} != 0x{expected:04x}")
    if status != 0:
        raise JbdError(f"BMS status 0x{status:02x}")
    return {"cmd": cmd, "status": status, "payload": payload}


def _u16(payload: bytes, offset: int) -> int:
    return (payload[offset] << 8) | payload[offset + 1]


def _s16(payload: bytes, offset: int) -> int:
    v = _u16(payload, offset)
    return v - 0x10000 if v >= 0x8000 else v


def parse_protection(bits: int) -> dict[str, bool]:
    out = {}
    for i, name in enumerate(PROTECTION_BITS):
        out[name] = bool(bits & (1 << i))
    out["raw"] = bits
    out["active"] = [n for n in PROTECTION_BITS if out[n]]
    return out


def parse_basic_info(payload: bytes) -> dict[str, Any]:
    """0x03 payload. Needs at least 23 bytes before the NTC array."""
    if len(payload) < 23:
        raise JbdError(f"basic-info payload too short ({len(payload)})")
    ntc_count = payload[22]
    ntc_end = 23 + ntc_count * 2
    temps_c: list[float] = []
    if len(payload) >= ntc_end:
        for i in range(ntc_count):
            kelvin_x10 = _u16(payload, 23 + i * 2)
            temps_c.append(round(kelvin_x10 / 10.0 - 273.1, 1))
    prot = parse_protection(_u16(payload, 16))
    fet = payload[20]
    voltage_v = _u16(payload, 0) / 100.0
    current_a = _s16(payload, 2) / 100.0
    remain_ah = _u16(payload, 4) / 100.0
    full_ah = _u16(payload, 6) / 100.0
    temp_c = max(temps_c) if temps_c else None
    return {
        "voltage_v": round(voltage_v, 3),
        "current_a": round(current_a, 3),
        "power_w": round(voltage_v * current_a, 1),
        "remain_ah": round(remain_ah, 3),
        "full_ah": round(full_ah, 3),
        "cycles": _u16(payload, 8),
        "balance_low": _u16(payload, 12),
        "balance_high": _u16(payload, 14),
        "protection": prot,
        "version": payload[18],
        "soc_pct": float(payload[19]),
        "charge_fet": bool(fet & 0x01),
        "discharge_fet": bool(fet & 0x02),
        "cell_count": payload[21],
        "ntc_count": ntc_count,
        "temps_c": temps_c,
        "temp_c": temp_c,
    }


def parse_cell_voltages(payload: bytes) -> list[int]:
    """0x04 payload: uint16 mV per cell, big-endian."""
    if len(payload) % 2:
        raise JbdError("cell-voltage payload odd length")
    return [_u16(payload, i) for i in range(0, len(payload), 2)]


def merge_reading(basic: dict[str, Any], cells: list[int] | None = None) -> dict[str, Any]:
    """Combine 0x03 + 0x04 into the shape stored in `state.bms_live`."""
    out = dict(basic)
    if cells:
        out["cell_mv"] = list(cells)
        out["min_cell_mv"] = min(cells)
        out["max_cell_mv"] = max(cells)
    else:
        out.setdefault("cell_mv", [])
        out.setdefault("min_cell_mv", None)
        out.setdefault("max_cell_mv", None)
    return out
