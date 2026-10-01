"""JBD / Overkill / XiaoXiang frame codec — no BLE required."""
from __future__ import annotations

import pytest

import jbd_bms as jbd


def _basic_payload(
    *,
    voltage_v: float = 53.21,
    current_a: float = -12.5,
    remain_ah: float = 80.0,
    full_ah: float = 100.0,
    cycles: int = 42,
    soc: int = 80,
    ntc_c: tuple[float, ...] = (25.0, 27.5),
    cells: int = 16,
    fet: int = 0x03,
    prot: int = 0,
) -> bytes:
    voltage = int(round(voltage_v * 100))
    current = int(round(current_a * 100))
    if current < 0:
        current = (current + 0x10000) & 0xFFFF
    remain = int(round(remain_ah * 100))
    full = int(round(full_ah * 100))
    buf = bytearray(23 + len(ntc_c) * 2)
    buf[0:2] = voltage.to_bytes(2, "big")
    buf[2:4] = current.to_bytes(2, "big")
    buf[4:6] = remain.to_bytes(2, "big")
    buf[6:8] = full.to_bytes(2, "big")
    buf[8:10] = cycles.to_bytes(2, "big")
    buf[16:18] = prot.to_bytes(2, "big")
    buf[19] = soc
    buf[20] = fet
    buf[21] = cells
    buf[22] = len(ntc_c)
    for i, t in enumerate(ntc_c):
        kelvin_x10 = int(round((t + 273.1) * 10))
        buf[23 + i * 2:25 + i * 2] = kelvin_x10.to_bytes(2, "big")
    return bytes(buf)


def test_read_request_checksum_matches_known_frames():
    assert jbd.build_read(0x03) == bytes.fromhex("dd a5 03 00 ff fd 77")
    assert jbd.build_read(0x04) == bytes.fromhex("dd a5 04 00 ff fc 77")


def test_round_trip_basic_info():
    payload = _basic_payload()
    frame = jbd.build_response(jbd.CMD_BASIC, payload)
    parsed = jbd.parse_frame(frame)
    assert parsed["cmd"] == jbd.CMD_BASIC
    info = jbd.parse_basic_info(parsed["payload"])
    assert info["voltage_v"] == pytest.approx(53.21)
    assert info["current_a"] == pytest.approx(-12.5)
    assert info["remain_ah"] == pytest.approx(80.0)
    assert info["full_ah"] == pytest.approx(100.0)
    assert info["cycles"] == 42
    assert info["soc_pct"] == 80
    assert info["charge_fet"] is True
    assert info["discharge_fet"] is True
    assert info["cell_count"] == 16
    assert info["temps_c"] == [25.0, 27.5]
    assert info["temp_c"] == 27.5
    assert info["power_w"] == pytest.approx(53.21 * -12.5, abs=0.2)
    assert info["protection"]["active"] == []


def test_protection_bits():
    payload = _basic_payload(prot=0b0000_0001_0000_0001)  # cell OV + charge OC
    info = jbd.parse_basic_info(payload)
    assert info["protection"]["cell_overvoltage"] is True
    assert info["protection"]["charge_overcurrent"] is True
    assert "cell_overvoltage" in info["protection"]["active"]


def test_cell_voltages():
    payload = b"".join((3300 + i).to_bytes(2, "big") for i in range(4))
    frame = jbd.build_response(jbd.CMD_CELLS, payload)
    cells = jbd.parse_cell_voltages(jbd.parse_frame(frame)["payload"])
    assert cells == [3300, 3301, 3302, 3303]
    merged = jbd.merge_reading({"soc_pct": 50}, cells)
    assert merged["min_cell_mv"] == 3300
    assert merged["max_cell_mv"] == 3303


def test_extract_frames_handles_fragments_and_junk():
    payload = _basic_payload()
    frame = jbd.build_response(jbd.CMD_BASIC, payload)
    # junk prefix + split across two notifies + leftover start of next
    buf = b"\x00\xff" + frame[:10]
    frames, rest = jbd.extract_frames(buf)
    assert frames == []
    buf = rest + frame[10:] + frame[:4]
    frames, rest = jbd.extract_frames(buf)
    assert len(frames) == 1
    assert frames[0] == frame
    assert rest == frame[:4]


def test_bad_checksum_rejected():
    payload = _basic_payload()
    frame = bytearray(jbd.build_response(jbd.CMD_BASIC, payload))
    frame[-3] ^= 0xFF
    with pytest.raises(jbd.JbdError, match="checksum"):
        jbd.parse_frame(bytes(frame))


def test_status_error_rejected():
    payload = _basic_payload()
    frame = jbd.build_response(jbd.CMD_BASIC, payload, status=0x80)
    with pytest.raises(jbd.JbdError, match="status"):
        jbd.parse_frame(frame)


def test_short_basic_info_rejected():
    with pytest.raises(jbd.JbdError, match="too short"):
        jbd.parse_basic_info(b"\x00" * 10)
