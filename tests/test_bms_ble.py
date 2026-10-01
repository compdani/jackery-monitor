"""BLE helper tests — no adapter required."""
from __future__ import annotations

import bms_ble


def test_looks_like_jbd_name_and_uuid():
    assert bms_ble.looks_like_jbd("JBD-SP04S020")
    assert bms_ble.looks_like_jbd("Overkill Solar")
    assert bms_ble.looks_like_jbd("Xiaoxiang BMS")
    assert bms_ble.looks_like_jbd(None, ["0000ff00-0000-1000-8000-00805f9b34fb"])
    assert not bms_ble.looks_like_jbd("iPhone")
    assert not bms_ble.looks_like_jbd(None, ["0000180a-0000-1000-8000-00805f9b34fb"])


def test_bleak_status_is_structured():
    st = bms_ble.bleak_status()
    assert "available" in st
    assert "error" in st
