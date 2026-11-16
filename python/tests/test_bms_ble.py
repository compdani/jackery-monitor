"""BLE helper tests — no adapter required."""
from __future__ import annotations

import bms_ble


def test_looks_like_jbd_name_and_uuid():
    assert bms_ble.looks_like_jbd("JBD-SP04S020")
    assert bms_ble.looks_like_jbd("Overkill Solar")
    assert bms_ble.looks_like_jbd("Xiaoxiang BMS")
    assert bms_ble.looks_like_jbd(None, ["0000ff00-0000-1000-8000-00805f9b34fb"])
    assert bms_ble.looks_like_jbd(None, [], {"0000ff00-0000-1000-8000-00805f9b34fb": b""})
    assert not bms_ble.looks_like_jbd("iPhone")
    assert not bms_ble.looks_like_jbd(None, ["0000180a-0000-1000-8000-00805f9b34fb"])


def test_iter_scan_results_accepts_dict_or_list():
    class Dev:
        def __init__(self, address):
            self.address = address
    a, b = Dev("AA"), Dev("BB")
    assert bms_ble.iter_scan_results({a: "adv", b: "adv2"}) == [(a, "adv"), (b, "adv2")]
    assert bms_ble.iter_scan_results([a, b]) == [(a, None), (b, None)]
    assert bms_ble.iter_scan_results(None) == []


def test_bleak_status_is_structured():
    st = bms_ble.bleak_status()
    assert "available" in st
    assert "error" in st
