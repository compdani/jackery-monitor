"""Delta 3 / Delta 3 Max Plus private-API protobuf decode.

Vendored message classes live in ecoflow_proto/ef_delta3_pb2.py
(Apache-2.0, from tolwi/hassio-ecoflow-cloud). Decode logic mirrors that
integration's Delta3._prepare_data path without Home Assistant deps.

Delta 3 Max Plus dual-PV field ``pow_get_pv2`` (wire field 70) is not in
the vendored DisplayPropertyUpload schema, so it is recovered from the
raw pdata via a small protobuf wire scan after ParseFromString.
"""
from __future__ import annotations

import base64
import logging
import struct
from typing import Any

log = logging.getLogger("ecoflow_delta3")

BMS_HEARTBEAT_COMMANDS: set[tuple[int, int]] = {
    (3, 1), (3, 2), (3, 30), (3, 50),
    (32, 1), (32, 3), (32, 50), (32, 51), (32, 52),
    (254, 24), (254, 25), (254, 26), (254, 27), (254, 28), (254, 29), (254, 30),
}

# Max Plus DisplayPropertyUpload floats absent from vendored stubs.
# Field numbers from ecoflow-energy-ha ecocharge.proto / ioBroker Max Plus.
_DISPLAY_EXTRA_FLOATS: dict[int, str] = {
    70: "pow_get_pv2",
}


def _pb2():
    """Lazy-load generated protobuf module.

    Raises ImportError if ecoflow_proto is missing from the image/install —
    callers must catch so MQTT threads are not killed.
    """
    try:
        from ecoflow_proto import ef_delta3_pb2
    except ImportError:
        log.error(
            "ecoflow_proto package missing; rebuild image with "
            "`COPY ecoflow_proto ./ecoflow_proto` (Dockerfile)"
        )
        raise
    return ef_delta3_pb2


def _protobuf_to_dict(obj: Any) -> dict[str, Any]:
    try:
        from google.protobuf.json_format import MessageToDict
        return MessageToDict(obj, preserving_proto_field_name=True)
    except Exception:
        result: dict[str, Any] = {}
        for field, value in obj.ListFields():
            if field.label == field.LABEL_REPEATED:
                result[field.name] = list(value)
            elif hasattr(value, "ListFields"):
                result[field.name] = _protobuf_to_dict(value)
            else:
                result[field.name] = value
        return result


def _flatten_dict(d: dict, parent_key: str = "", sep: str = "_") -> dict:
    items: list[tuple[str, Any]] = []
    for k, v in d.items():
        new_key = f"{parent_key}{sep}{k}" if parent_key else k
        if isinstance(v, dict):
            items.extend(_flatten_dict(v, new_key, sep=sep).items())
        else:
            items.append((new_key, v))
    return dict(items)


def _xor_decode(pdata: bytes, seq: int) -> bytes:
    if not pdata:
        return b""
    return bytes((b ^ seq) & 0xFF for b in pdata)


def _read_varint(buf: bytes, i: int) -> tuple[int | None, int]:
    result = 0
    shift = 0
    n = len(buf)
    while i < n:
        b = buf[i]
        i += 1
        result |= (b & 0x7F) << shift
        if not (b & 0x80):
            return result, i
        shift += 7
        if shift > 63:
            break
    return None, i


def extract_undeclared_floats(
    pdata: bytes,
    field_map: dict[int, str] | None = None,
) -> dict[str, float]:
    """Scan protobuf wire bytes for fixed32 (wire type 5) fields by number.

    Used for Max Plus keys (e.g. pow_get_pv2 = 70) missing from the
    vendored DisplayPropertyUpload descriptor — upb drops UnknownFields.
    """
    mapping = field_map if field_map is not None else _DISPLAY_EXTRA_FLOATS
    out: dict[str, float] = {}
    if not pdata or not mapping:
        return out
    i = 0
    n = len(pdata)
    while i < n:
        tag, i = _read_varint(pdata, i)
        if tag is None:
            break
        field_num = tag >> 3
        wire = tag & 7
        if wire == 0:  # varint
            _, i = _read_varint(pdata, i)
        elif wire == 1:  # 64-bit
            i += 8
        elif wire == 2:  # length-delimited
            length, i = _read_varint(pdata, i)
            if length is None or i + length > n:
                break
            i += length
        elif wire == 5:  # 32-bit (float / fixed32)
            if i + 4 > n:
                break
            name = mapping.get(field_num)
            if name is not None:
                out[name] = struct.unpack_from("<f", pdata, i)[0]
            i += 4
        else:
            break
    return out


def _derive_outputs(result: dict[str, Any]) -> None:
    dc_flow = result.get("flow_info_12v")
    if dc_flow == 14:
        result["cfg_dc12v_out_open"] = 1
    elif dc_flow == 4:
        result["cfg_dc12v_out_open"] = 0
    ac_flow = result.get("flow_info_ac_out")
    if ac_flow == 14:
        result["cfg_ac_out_open"] = 1
    elif ac_flow == 4:
        result["cfg_ac_out_open"] = 0
    usb_keys = (
        "flow_info_qcusb1", "flow_info_qcusb2",
        "flow_info_typec1", "flow_info_typec2",
    )
    usb_values = [result.get(k) for k in usb_keys if k in result]
    if usb_values:
        if any(v == 14 for v in usb_values):
            result["cfg_usb_open"] = 1
        elif all(v == 4 for v in usb_values):
            result["cfg_usb_open"] = 0


def _decode_message_by_type(pdata: bytes, header_info: dict[str, Any]) -> dict[str, Any]:
    pb2 = _pb2()
    cmd_func = header_info.get("cmdFunc", 0)
    cmd_id = header_info.get("cmdId", 0)
    try:
        if cmd_func == 254 and cmd_id == 21:
            msg = pb2.Delta3DisplayPropertyUpload()
            msg.ParseFromString(pdata)
            result = _protobuf_to_dict(msg)
            # Recover Max Plus PV2 (and any other undeclared floats).
            for key, val in extract_undeclared_floats(pdata).items():
                if key not in result or result.get(key) is None:
                    result[key] = val
            _derive_outputs(result)
            return result
        if cmd_func == 254 and cmd_id == 22:
            msg = pb2.Delta3RuntimePropertyUpload()
            msg.ParseFromString(pdata)
            return _protobuf_to_dict(msg)
        if cmd_func == 32 and cmd_id == 2:
            msg = pb2.Delta3CMSHeartBeatReport()
            msg.ParseFromString(pdata)
            return _protobuf_to_dict(msg)
        if (cmd_func, cmd_id) in BMS_HEARTBEAT_COMMANDS:
            msg = pb2.Delta3BMSHeartBeatReport()
            msg.ParseFromString(pdata)
            return _protobuf_to_dict(msg)
    except Exception as e:
        log.debug("decode cmdFunc=%s cmdId=%s failed: %s", cmd_func, cmd_id, e)
    return {}


def _maybe_b64(raw: bytes) -> bytes:
    try:
        return base64.b64decode(raw, validate=True)
    except Exception:
        return raw


def peek_header_cmds(raw_data: bytes) -> list[str]:
    """Return cmdFunc/cmdId labels from a HeaderMessage without full decode."""
    try:
        pb2 = _pb2()
    except ImportError:
        return []
    raw_data = _maybe_b64(raw_data)
    try:
        header_msg = pb2.Delta3HeaderMessage()
        header_msg.ParseFromString(raw_data)
    except Exception:
        return []
    out: list[str] = []
    for header in header_msg.header or []:
        cmd_func = getattr(header, "cmd_func", 0)
        cmd_id = getattr(header, "cmd_id", 0)
        out.append(f"{cmd_func}/{cmd_id}")
    return out


def decode_property_payload(raw_data: bytes) -> dict[str, Any]:
    """Decode one private-API MQTT property push into a flat params dict.

    Returns {} when the payload is not a recognisable Delta 3 protobuf,
    or when the generated protobuf stubs are missing from the image.
    """
    try:
        pb2 = _pb2()
    except ImportError:
        return {}
    raw_data = _maybe_b64(raw_data)
    try:
        header_msg = pb2.Delta3HeaderMessage()
        header_msg.ParseFromString(raw_data)
    except Exception as e:
        log.debug("header parse failed: %s", e)
        return {}
    if not header_msg.header:
        return {}

    merged: dict[str, Any] = {}
    for header in header_msg.header:
        try:
            header_info = {
                "src": getattr(header, "src", 0),
                "encType": getattr(header, "enc_type", 0),
                "seq": getattr(header, "seq", 0),
                "cmdFunc": getattr(header, "cmd_func", 0),
                "cmdId": getattr(header, "cmd_id", 0),
            }
            pdata = getattr(header, "pdata", b"") or b""
            if not pdata:
                continue
            if header_info["encType"] == 1 and header_info["src"] != 32:
                pdata = _xor_decode(pdata, header_info["seq"])
            decoded = _decode_message_by_type(pdata, header_info)
            if decoded:
                merged.update(decoded)
        except Exception as e:
            log.debug("header decode failed: %s", e)
            continue
    if not merged:
        return {}
    return _flatten_dict(merged)


def build_quota_request(device_sn: str) -> bytes:
    """Protobuf 'get all' request published on the get topic.

    Returns b'' when protobuf stubs are unavailable so callers can skip publish.
    """
    try:
        pb2 = _pb2()
    except ImportError:
        return b""
    packet = pb2.Delta3SendHeaderMsg()
    header = packet.msg.add()
    header.src = 32
    header.dest = 32
    header.seq = int(time_seq())
    setattr(header, "from", "SolarPowMonitor")
    return packet.SerializeToString()


# Output port → Delta3SetCommand field (HA / ioBroker parity).
OUTPUT_SET_FIELDS = {
    "ac": "cfg_ac_out_open",
    "dc": "cfg_dc12v_out_open",
    "usb": "cfg_usb_open",
}


def build_set_command(field_name: str, value: int, device_sn: str) -> bytes:
    """Protobuf ConfigWrite (cmdFunc=254, cmdId=17) for one SetCommand field.

    Returns b'' when stubs are missing or the field is unknown.
    """
    try:
        pb2 = _pb2()
    except ImportError:
        return b""
    payload = pb2.Delta3SetCommand()
    if not hasattr(payload, field_name):
        log.error("unknown Delta3 set field: %s", field_name)
        return b""
    setattr(payload, field_name, int(value))
    pdata = payload.SerializeToString()

    packet = pb2.Delta3SendHeaderMsg()
    message = packet.msg.add()
    message.src = 32
    message.dest = 2
    message.d_src = 1
    message.d_dest = 1
    message.cmd_func = 254
    message.cmd_id = 17
    message.need_ack = 1
    message.seq = int(time_seq())
    message.product_id = 1
    message.version = 19
    message.payload_ver = 1
    message.device_sn = device_sn
    message.data_len = len(pdata)
    message.pdata = pdata
    return packet.SerializeToString()


def time_seq() -> int:
    import time
    return int(time.time() * 1000) % 2147483647
