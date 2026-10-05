"""Siseli (Solar of Things) cloud client.

Protocol reverse-engineered by Conexo Casa and published under MIT in
https://github.com/Conexo-Casa/solar-of-things-ha — we reimplement the
HTTP/auth layer here without Home Assistant.

Auth: User ID + password against https://solar.siseli.com with IOT-Open
signed headers (AES-decrypt embedded app secret → HMAC-SHA256 → MD5).
Access tokens refresh ~5 min before expiry; login is retried if refresh
fails. Device discovery needs a numeric Station ID from the portal.
"""
from __future__ import annotations

import base64
import hashlib
import hmac as _hmac
import json
import logging
import os
import threading
from datetime import datetime, timedelta, timezone
from typing import Any, Callable
from zoneinfo import ZoneInfo

import httpx
from Cryptodome.Cipher import AES

log = logging.getLogger("siseli")

SN_PREFIX = "siseli:"
API_BASE_URL = "https://solar.siseli.com"
API_LOGIN = "/apis/login/account"
API_REFRESH_TOKEN = "/apis/login/refresh/access/token"
API_TIME_SERIES = "/apis/deviceState/simple/attribute/keys/history/v1"
API_MONTHLY_SUMMARY = "/apis/stationOverView/stateAttributeSummary/category/yearly"
API_SETTINGS_GET = "/apis/remote/device/configs/cache/get"
API_SETTINGS_SET = "/apis/remote/device/config/write"
API_DEVICE_LIST = "/apis/device/list"
API_ENERGY_FLOW = "/apis/deviceState/simple/energy/flow/v1"
API_STATE_LATEST = "/apis/deviceState/simple/state/latest/v1"

IOT_APP_ID = "rBrTRfAPXz"
IOT_APP_SECRET_ENC = "I4D0KRr2339z3pQ/at91V9BpFAOe54DaTafwSm6suIQ="
TOKEN_REFRESH_LEAD_SECONDS = 300
USER_AGENT = (
    "SolarPowMonitor-Siseli/1.0 "
    "(+https://github.com/YanivErel-code/jackery-monitor)"
)
DEFAULT_TZ = "UTC"
HTTP_TIMEOUT = 30.0

ENERGY_FLOW_RULES: dict[str, list[tuple[str, tuple[str, ...], float]]] = {
    "pvInputPower": [
        ("sum", ("pv1Power", "pv2Power", "pv3Power", "pv4Power"), 1.0),
    ],
    "batteryVoltage": [
        ("first", ("bmsBatteryVoltage", "positiveTerminalBatteryVoltage"), 1.0),
    ],
    "batterySOC": [
        ("first", ("batteryPercentage", "bmsSOC"), 1.0),
    ],
    "batteryPower": [
        ("first", ("batteryPower",), 1.0),
    ],
    "acOutputActivePower": [
        ("sum", ("load_power",), 1000.0),
    ],
    "gridPower": [
        ("clamp_neg", ("aPhaseMainsPower", "bPhaseMainsPower", "cPhaseMainsPower"), 1.0),
    ],
    "feedInPower": [
        ("clamp_pos", ("aPhaseMainsPower", "bPhaseMainsPower", "cPhaseMainsPower"), 1.0),
    ],
    "batteryChargingCurrent": [
        ("clamp_neg", ("positiveTerminalBatteryCurrent",), 1.0),
    ],
    "batteryDischargeCurrent": [
        ("clamp_pos", ("positiveTerminalBatteryCurrent",), 1.0),
    ],
}

REALTIME_PROBE_KEYS: tuple[str, ...] = (
    "pvInputPower",
    "acOutputActivePower",
    "batteryVoltage",
    "batterySOC",
    "batteryChargingCurrent",
    "batteryDischargeCurrent",
    "feedInPower",
)

SETTING_KEY_ALIASES: dict[str, tuple[str, ...]] = {
    "outputSourcePrioritySetting": (
        "outputSourcePrioritySetting", "setOutputSourcePriority",
    ),
    "chargerSourcePrioritySetting": ("chargerSourcePrioritySetting",),
    "acInputRangeSetting": ("acInputRangeSetting",),
    "batteryPowerLimitingSetting": ("batteryPowerLimitingSetting",),
    "batteryChargeLimit": ("batteryChargeLimit",),
    "batteryDischargeLimit": ("batteryDischargeLimit",),
    "gridChargeLimit": ("gridChargeLimit",),
}

CONTROL_DEFINITIONS: list[dict[str, Any]] = [
    {
        "canonical": "batteryChargeLimit",
        "kind": "number",
        "name": "Battery charge limit",
        "min": 0, "max": 100, "step": 1, "unit": "%",
    },
    {
        "canonical": "batteryDischargeLimit",
        "kind": "number",
        "name": "Battery discharge limit",
        "min": 0, "max": 100, "step": 1, "unit": "%",
    },
    {
        "canonical": "gridChargeLimit",
        "kind": "number",
        "name": "Grid charge limit",
        "min": 0, "max": 5000, "step": 100, "unit": "W",
    },
    {
        "canonical": "outputSourcePrioritySetting",
        "kind": "select",
        "name": "Output source priority",
        "options": [
            {"value": 0, "label": "Utility First (USO)"},
            {"value": 1, "label": "Solar First (SUB)"},
            {"value": 2, "label": "Solar+Battery First (SBU)"},
        ],
    },
    {
        "canonical": "chargerSourcePrioritySetting",
        "kind": "select",
        "name": "Charger source priority",
        "options": [
            {"value": 0, "label": "Solar + Utility (CSO)"},
            {"value": 1, "label": "Solar First (SNU)"},
            {"value": 2, "label": "Solar Only (OSO)"},
        ],
    },
    {
        "canonical": "acInputRangeSetting",
        "kind": "switch",
        "name": "Grid charging (AC input range)",
        "on_value": 0, "off_value": 1,
    },
    {
        "canonical": "batteryPowerLimitingSetting",
        "kind": "switch",
        "name": "Grid feed-in",
        "on_value": 1, "off_value": 0,
    },
    {
        "canonical": "backupMode",
        "kind": "switch",
        "name": "Backup mode (SBU priority)",
        "write_canonical": "outputSourcePrioritySetting",
        "on_value": 2, "off_value": 1,
    },
    {
        "canonical": "LoadSwitchSetting",
        "kind": "switch",
        "name": "Load",
        "on_value": "1", "off_value": "0",
    },
]

KNOWN_CONTROL_CANONICALS = tuple(c["canonical"] for c in CONTROL_DEFINITIONS)


def default_live_control_keys(controls: list[dict[str, Any]] | None) -> list[str]:
    """Known inverter controls this firmware actually exposed — default Live pins."""
    known = set(KNOWN_CONTROL_CANONICALS)
    out: list[str] = []
    for item in controls or []:
        key = item.get("canonical")
        if key in known and not item.get("dynamic") and key not in out:
            out.append(str(key))
    return out


class TokenExpiredError(Exception):
    """Access token expired and could not be refreshed."""


class AuthenticationError(Exception):
    """Login credentials rejected by the server."""


class PortalError(Exception):
    """Portal answered, but the JSON code was not success.

    Used for device-list failures such as an unknown Station ID. Network
    failures stay ordinary exceptions so callers can tell them apart.
    """

    def __init__(self, message: str = "", *, code: Any = None) -> None:
        self.code = code
        self.portal_message = (message or "").strip()
        super().__init__(self.portal_message or f"code={code}")


class EnergyFlowRuleNotConfiguredError(RuntimeError):
    """Portal returned code 70132 — no energy-flow rule for this device."""


def is_siseli_sn(value: Any) -> bool:
    return str(value or "").startswith(SN_PREFIX)


def make_sn(portal_id: str) -> str:
    raw = str(portal_id or "").strip()
    if not raw:
        raise ValueError("empty Siseli device id")
    if raw.startswith(SN_PREFIX):
        return raw
    return f"{SN_PREFIX}{raw}"


def portal_id_from_sn(sn: str) -> str:
    s = str(sn or "")
    if s.startswith(SN_PREFIX):
        return s[len(SN_PREFIX):]
    return s


def _decrypt_app_secret(app_id: str, encrypted_b64: str) -> str:
    md5_hex = hashlib.md5(app_id.encode("utf-8")).hexdigest()
    key = md5_hex[:16].encode("ascii")
    iv = md5_hex[16:].encode("ascii")
    ciphertext = base64.b64decode(encrypted_b64)
    cipher = AES.new(key, AES.MODE_CBC, iv)
    plaintext = cipher.decrypt(ciphertext).rstrip(b"\x00")
    return plaintext.decode("utf-8")


def _compute_iot_sign(app_id: str, nonce: str, body_hash: str, secret: str) -> str:
    sign_headers = {
        "IOT-Open-AppID": app_id,
        "IOT-Open-Body-Hash": body_hash,
        "IOT-Open-Nonce": nonce,
    }
    qs_str = "&".join(f"{k}={sign_headers[k]}" for k in sorted(sign_headers.keys()))
    b64_qs = base64.b64encode(qs_str.encode("utf-8")).decode("ascii")
    hmac_bytes = _hmac.new(
        secret.encode("utf-8"), b64_qs.encode("utf-8"), hashlib.sha256,
    ).digest()
    return hashlib.md5(hmac_bytes).hexdigest()


def make_signed_headers(body_bytes: bytes, extra: dict[str, str] | None = None) -> dict[str, str]:
    """Build IOT Open Platform signed headers for an auth POST."""
    secret = _decrypt_app_secret(IOT_APP_ID, IOT_APP_SECRET_ENC)
    nonce = os.urandom(16).hex()
    body_hash = hashlib.sha256(body_bytes).hexdigest()
    sign = _compute_iot_sign(IOT_APP_ID, nonce, body_hash, secret)
    headers: dict[str, str] = {
        "Accept": "application/json",
        "Content-Type": "application/json; charset=utf-8",
        "Origin": "https://solar.siseli.com",
        "Referer": "https://solar.siseli.com/",
        "IOT-Open-AppID": IOT_APP_ID,
        "IOT-Open-Nonce": nonce,
        "IOT-Open-Body-Hash": body_hash,
        "IOT-Open-Sign": sign,
        "User-Agent": USER_AGENT,
    }
    if extra:
        headers.update(extra)
    return headers


def _parse_expiry(value: str | None) -> datetime | None:
    if not value:
        return None
    try:
        cleaned = value.replace("Z", "+00:00")
        dt = datetime.fromisoformat(cleaned)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return dt.astimezone(timezone.utc)
    except Exception:
        return None


def coerce_number(value: Any) -> float | None:
    if value is None or isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        text = value.strip()
        if not text:
            return None
        try:
            return float(text)
        except ValueError:
            return None
    if isinstance(value, list):
        for item in reversed(value):
            number = coerce_number(item)
            if number is not None:
                return number
        return None
    if isinstance(value, dict):
        for key in ("value", "val", "latest"):
            if key in value:
                return coerce_number(value[key])
    return None


def _field_number(raw: Any) -> tuple[float | None, str]:
    """Return (number, unit) from a bare value or a portal ``{value, unit}`` object."""
    if isinstance(raw, dict):
        return coerce_number(raw.get("value")), str(raw.get("unit") or "")
    return coerce_number(raw), ""


def _first_field(fields: dict[str, Any], keys: tuple[str, ...]) -> tuple[float | None, str]:
    for key in keys:
        if key not in fields:
            continue
        number, unit = _field_number(fields.get(key))
        if number is not None:
            return number, unit
    return None, ""


def _as_watts(number: float | None, unit: str) -> float | None:
    if number is None:
        return None
    if unit.strip().lower() == "kw":
        return number * 1000.0
    return number


def map_latest_state_fields(fields: Any) -> dict[str, float]:
    """Map the device latest-state payload onto canonical sensor keys.

    Charge controllers (the portal's Devices details page) report live
    values here under names the station history endpoint never uses:
    ``batteryRemainingCapacitySOC``, ``BatteryVoltage``, ``generationPower``
    (kW), and ``load_power``. Watts follow the payload's own unit tag so a
    kW field is not left as a fraction of a watt and a W field is not
    multiplied by 1000.
    """
    if not isinstance(fields, dict) or not fields:
        return {}
    mapped: dict[str, float] = {}
    soc, _unit = _first_field(fields, (
        "batteryRemainingCapacitySOC", "batteryPercentage", "bmsSOC", "batterySOC",
    ))
    if soc is not None:
        mapped["batterySOC"] = soc
    volts, _unit = _first_field(fields, (
        "BatteryVoltage", "bmsBatteryVoltage", "positiveTerminalBatteryVoltage",
        "batteryVoltage",
    ))
    if volts is not None:
        mapped["batteryVoltage"] = volts
    gen, gen_unit = _first_field(fields, ("generationPower",))
    pv = _as_watts(gen, gen_unit) if gen_unit.strip() else None
    if pv is None:
        named, named_unit = _first_field(fields, ("pvInputPower", "pvPower"))
        pv = _as_watts(named, named_unit)
    if pv is None:
        strings = [
            number for key in ("pv1Power", "pv2Power", "pv3Power", "pv4Power")
            if (number := _field_number(fields.get(key))[0]) is not None
        ]
        if strings:
            pv = sum(strings)
    if pv is not None:
        mapped["pvInputPower"] = pv
    pv_v, _unit = _first_field(fields, ("PVVoltage", "pvVoltage"))
    if pv_v is not None:
        mapped["pvVoltage"] = pv_v
    pv_a, _unit = _first_field(fields, ("PVCurrent", "pvCurrent"))
    if pv_a is not None:
        mapped["pvCurrent"] = pv_a
    load, load_unit = _first_field(fields, ("load_power", "loadPower", "acOutputActivePower"))
    # A bare load_power with no unit is the older energy-flow convention (kW).
    # Leave it for that mapper. A tagged unit is authoritative.
    load_w = _as_watts(load, load_unit) if load_unit.strip() else None
    if load_w is not None:
        mapped["acOutputActivePower"] = load_w
        mapped["loadPower"] = load_w
    charge_a, _unit = _first_field(fields, ("ChargingCurrent", "batteryChargingCurrent"))
    if charge_a is not None:
        mapped["batteryChargingCurrent"] = charge_a
    discharge_a, _unit = _first_field(fields, ("batteryDischargeCurrent",))
    if discharge_a is not None:
        mapped["batteryDischargeCurrent"] = discharge_a
    temp, _unit = _first_field(fields, ("batteryTemperature1", "batteryTemperature2"))
    if temp is not None:
        mapped["batteryTempC"] = temp
    return mapped


def map_energy_flow_fields(fields: Any) -> dict[str, float]:
    """Translate energy-flow ``fields`` into canonical sensor keys."""
    if not isinstance(fields, dict) or not fields:
        return {}
    mapped: dict[str, float] = {}
    for canonical, rules in ENERGY_FLOW_RULES.items():
        for mode, sources, scale in rules:
            if mode in ("sum", "clamp_pos", "clamp_neg"):
                values = [
                    number for source in sources
                    if (number := coerce_number(fields.get(source))) is not None
                ]
                if values:
                    total = sum(values) * scale
                    if mode == "clamp_pos":
                        total = max(0.0, total)
                    elif mode == "clamp_neg":
                        total = max(0.0, -total)
                    mapped[canonical] = total
            else:
                for source in sources:
                    number = coerce_number(fields.get(source))
                    if number is not None:
                        mapped[canonical] = number * scale
                        break
            if canonical in mapped:
                break
    return mapped


def resolve_setting_key(settings: Any, canonical: str) -> str | None:
    if not isinstance(settings, dict):
        return None
    for candidate in SETTING_KEY_ALIASES.get(canonical, (canonical,)):
        if candidate in settings:
            return candidate
    return None


def has_realtime_values(values: Any) -> bool:
    if not isinstance(values, dict):
        return False
    return any(coerce_number(values.get(key)) is not None for key in REALTIME_PROBE_KEYS)


def apply_derived_values(latest_values: dict[str, Any]) -> None:
    if latest_values.get("batteryPower") is None:
        voltage = coerce_number(latest_values.get("batteryVoltage")) or 0.0
        discharge = coerce_number(latest_values.get("batteryDischargeCurrent")) or 0.0
        charge = coerce_number(latest_values.get("batteryChargingCurrent")) or 0.0
        latest_values["batteryPower"] = (discharge - charge) * voltage
    ac_output = coerce_number(latest_values.get("acOutputActivePower")) or 0.0
    if latest_values.get("loadPower") is None:
        latest_values["loadPower"] = ac_output


def to_telemetry(values: dict[str, Any]) -> dict[str, Any]:
    """Map canonical Siseli keys onto the dashboard's live telemetry dict."""
    solar = coerce_number(values.get("pvInputPower")) or 0.0
    load = coerce_number(values.get("loadPower"))
    if load is None:
        load = coerce_number(values.get("acOutputActivePower")) or 0.0
    grid = coerce_number(values.get("gridPower")) or 0.0
    feed_in = coerce_number(values.get("feedInPower")) or 0.0
    soc = coerce_number(values.get("batterySOC"))
    charge_a = coerce_number(values.get("batteryChargingCurrent")) or 0.0
    discharge_a = coerce_number(values.get("batteryDischargeCurrent")) or 0.0
    if charge_a > 0.05:
        battery_status = 1
    elif discharge_a > 0.05:
        battery_status = 2
    else:
        battery_status = 0
    solar_inputs: list[dict[str, Any]] = []
    for i, key in enumerate(("pv1Power", "pv2Power", "pv3Power", "pv4Power"), start=1):
        w = coerce_number(values.get(key))
        if w is None or w == 0:
            continue
        solar_inputs.append({
            "id": f"siseli:pv{i}",
            "label": f"Siseli PV{i}",
            "source": "siseli",
            "watts": round(w),
        })
    # Local MQTT path may only expose pv_w / pv2_power_w via canonical aliases.
    if not solar_inputs:
        for i, key in enumerate(("pvPower", "pv2Power"), start=1):
            w = coerce_number(values.get(key))
            if w is None or w == 0:
                continue
            solar_inputs.append({
                "id": f"siseli:pv{i}",
                "label": f"Siseli PV{i}",
                "source": "siseli",
                "watts": round(w),
            })
    if not solar_inputs and solar:
        solar_inputs.append({
            "id": "siseli:pv",
            "label": "Siseli solar",
            "source": "siseli",
            "watts": round(solar),
        })
    out: dict[str, Any] = {
        "battery_percent": round(soc) if soc is not None else None,
        "solar_input_w": round(solar),
        "solar_inputs": solar_inputs,
        "ac_input_w": round(grid),
        "feed_in_w": round(feed_in),
        "output_power_w": round(load),
        "input_power_w": round(solar + grid),
        "car_input_w": 0,
        "battery_status": battery_status,
        "battery_voltage_v": coerce_number(values.get("batteryVoltage")),
        "battery_charge_a": charge_a,
        "battery_discharge_a": discharge_a,
        "pv_voltage_v": coerce_number(values.get("pvVoltage")),
        "pv_current_a": coerce_number(values.get("pvCurrent")),
        "battery_power_w": coerce_number(values.get("batteryPower")),
        "battery_temp_c": coerce_number(values.get("batteryTempC")),
        "ac_on": False,
        "dc_on": False,
        "usb_on": False,
        "car_on": False,
        "source": "siseli",
    }
    return out


def portal_device_id(dev: dict[str, Any]) -> str:
    return str(dev.get("id") or dev.get("deviceId") or dev.get("device_id") or "").strip()


def portal_device_name(dev: dict[str, Any], fallback: str) -> str:
    return str(
        dev.get("name") or dev.get("deviceName") or dev.get("device_name") or fallback
    )


def normalize_device(dev: dict[str, Any]) -> dict[str, Any] | None:
    pid = portal_device_id(dev)
    if not pid:
        return None
    sn = make_sn(pid)
    model = str(dev.get("model") or dev.get("modelName") or dev.get("productName") or "")
    name = portal_device_name(dev, model or pid)
    return {
        "device_id": sn,
        "device_sn": sn,
        "portal_device_id": pid,
        "name": name,
        "model_name": model or "Siseli inverter",
        "model_code": None,
        "source": "siseli",
    }


def _setting_raw_value(obj: Any) -> Any:
    if isinstance(obj, dict):
        if "value" in obj:
            return obj.get("value")
        return obj.get("valueDisplay")
    return obj


def _control_number(num: float | None) -> int | float | None:
    if num is None:
        return None
    if num == int(num):
        return int(num)
    return num


def _dynamic_control(key: str, entry: Any) -> dict[str, Any]:
    """One editable field for a cache key that is not a known inverter control."""
    raw = _setting_raw_value(entry)
    num = coerce_number(raw)
    name = key
    unit = ""
    hint = ""
    if isinstance(entry, dict):
        name = str(entry.get("nameDisplay") or entry.get("name") or key)
        unit = str(entry.get("unit") or "")
        if str(entry.get("valueTypeDict") or "") == "Enumeration":
            display = entry.get("valueDisplay")
            if display not in (None, ""):
                hint = str(display)
    return {
        "canonical": key,
        "kind": "number",
        "name": name,
        "unit": unit,
        "hint": hint,
        "value": _control_number(num),
        "resolved_key": key,
        "write_canonical": key,
        "dynamic": True,
    }


def normalize_controls(raw_settings: Any) -> list[dict[str, Any]]:
    """Return controls the firmware actually exposes.

    Known inverter keys stay on the live card. Every other cache key is a
    separate device-tab field (``dynamic``) so one write does not touch the rest.
    """
    if not isinstance(raw_settings, dict):
        return []
    out: list[dict[str, Any]] = []
    seen: set[str] = set()
    for spec in CONTROL_DEFINITIONS:
        write_canonical = spec.get("write_canonical") or spec["canonical"]
        resolved = resolve_setting_key(raw_settings, write_canonical)
        if resolved is None:
            continue
        raw_val = _setting_raw_value(raw_settings.get(resolved))
        num = coerce_number(raw_val)
        item = {k: v for k, v in spec.items() if k != "write_canonical"}
        item["resolved_key"] = resolved
        item["write_canonical"] = write_canonical
        item["dynamic"] = False
        if spec["kind"] == "switch":
            on_value = spec["on_value"]
            item["value"] = (int(num) == int(on_value)) if num is not None else None
        elif spec["kind"] == "select":
            item["value"] = int(num) if num is not None else None
        else:
            item["value"] = num
        out.append(item)
        seen.add(resolved)
        seen.add(spec["canonical"])
    dynamic: list[dict[str, Any]] = []
    for key, entry in raw_settings.items():
        if key in seen or not isinstance(key, str):
            continue
        if isinstance(entry, dict) and entry.get("isHidden") in (True, 1, "1"):
            continue
        dynamic.append(_dynamic_control(key, entry))
    dynamic.sort(key=lambda c: (str(c["name"]).lower(), c["canonical"]))
    out.extend(dynamic)
    return out


def coerce_setting_write_value(value: Any, spec: dict[str, Any] | None, cached: Any) -> Any:
    """Match the portal's stored type: string settings stay strings."""
    if spec and spec.get("kind") == "switch" and isinstance(spec.get("on_value"), str):
        num = coerce_number(value)
        on_num = coerce_number(spec.get("on_value"))
        if num is not None and on_num is not None and int(num) == int(on_num):
            return str(spec["on_value"])
        return str(spec.get("off_value"))
    raw = _setting_raw_value(cached) if cached is not None else None
    if isinstance(raw, str):
        if isinstance(value, bool):
            return "1" if value else "0"
        return str(value)
    if isinstance(raw, bool):
        return value
    if isinstance(raw, int):
        num = coerce_number(value)
        if num is None:
            return value
        if num == int(num):
            return int(num)
        return num
    if isinstance(raw, float):
        num = coerce_number(value)
        return num if num is not None else value
    return value


class SiseliAPI:
    """Siseli portal wrapper with automatic token refresh."""

    def __init__(
        self,
        *,
        user_id: str,
        password: str,
        iot_token: str | None = None,
        refresh_token: str | None = None,
        access_token_expires: str | None = None,
        refresh_token_expires: str | None = None,
        time_zone: str | None = None,
        on_token_refreshed: Callable[[str, str, str, str], None] | None = None,
        http: httpx.Client | None = None,
    ) -> None:
        if not user_id or not password:
            raise ValueError("user_id and password are required")
        self._user_id = user_id
        self._password = password
        self._time_zone = time_zone or DEFAULT_TZ
        self._on_token_refreshed = on_token_refreshed
        self._access_token = iot_token or ""
        self._refresh_token = refresh_token or ""
        self._access_expires = _parse_expiry(access_token_expires)
        self._refresh_expires = _parse_expiry(refresh_token_expires)
        self._refresh_lock = threading.Lock()
        self._owns_http = http is None
        self.http = http or httpx.Client(timeout=HTTP_TIMEOUT)
        self._apply_token_headers()

    def close(self) -> None:
        if self._owns_http:
            self.http.close()

    def _apply_token_headers(self) -> None:
        self.http.headers.update({
            "Accept": "application/json",
            "Content-Type": "application/json; charset=utf-8",
            "IOT-Token": self._access_token,
            "IOT-Time-Zone": self._time_zone,
            "Origin": "https://solar.siseli.com",
            "Referer": "https://solar.siseli.com/",
            "User-Agent": USER_AGENT,
        })

    def login(self) -> None:
        password_md5 = hashlib.md5(self._password.encode("utf-8")).hexdigest()
        payload = {"account": self._user_id, "password": password_md5}
        body_bytes = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        headers = make_signed_headers(body_bytes)
        resp = self.http.post(
            f"{API_BASE_URL}{API_LOGIN}",
            content=body_bytes,
            headers=headers,
        )
        resp.raise_for_status()
        data = resp.json()
        if data.get("code") not in (0, None, "0"):
            msg = data.get("message") or data.get("msg") or str(data)
            raise AuthenticationError(f"Login failed: {msg}")
        self._store_tokens(data.get("data") or data)

    def refresh_access_token(self) -> None:
        if not self._refresh_token:
            raise TokenExpiredError("No refresh token available.")
        resp = self.http.post(
            f"{API_BASE_URL}{API_REFRESH_TOKEN}",
            json={"refreshToken": self._refresh_token},
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json; charset=utf-8",
                "Origin": "https://solar.siseli.com",
                "Referer": "https://solar.siseli.com/",
                "User-Agent": USER_AGENT,
            },
        )
        if resp.status_code in (401, 403):
            raise TokenExpiredError(
                "Refresh token rejected by server (expired or invalid)."
            )
        resp.raise_for_status()
        data = resp.json()
        if data.get("code") not in (0, None, "0"):
            raise TokenExpiredError(
                f"Refresh failed: code={data.get('code')} "
                f"message={data.get('message')}"
            )
        self._store_tokens(data.get("data") or data)

    def _store_tokens(self, payload: dict[str, Any]) -> None:
        access = (
            payload.get("accessToken")
            or payload.get("iotToken")
            or payload.get("token")
            or ""
        )
        refresh = payload.get("refreshToken") or ""
        access_exp = (
            payload.get("accessTokenWillExpiredAt")
            or payload.get("accessTokenExpiredAt")
            or ""
        )
        refresh_exp = (
            payload.get("refreshTokenWillExpiredAt")
            or payload.get("refreshTokenExpiredAt")
            or ""
        )
        if not access:
            raise AuthenticationError(
                "Login/refresh response did not contain an access token. "
                f"Keys received: {list(payload.keys())}"
            )
        self._access_token = access
        self._refresh_token = refresh
        self._access_expires = _parse_expiry(access_exp)
        self._refresh_expires = _parse_expiry(refresh_exp)
        self._apply_token_headers()
        if self._on_token_refreshed:
            try:
                self._on_token_refreshed(
                    self._access_token,
                    self._refresh_token,
                    self.access_token_expires_iso,
                    self.refresh_token_expires_iso,
                )
            except Exception as cb_err:
                log.warning("token-refresh callback raised: %s", cb_err)

    @property
    def access_token(self) -> str:
        return self._access_token

    @property
    def refresh_token(self) -> str:
        return self._refresh_token

    @property
    def access_token_expires_iso(self) -> str:
        return self._access_expires.isoformat() if self._access_expires else ""

    @property
    def refresh_token_expires_iso(self) -> str:
        return self._refresh_expires.isoformat() if self._refresh_expires else ""

    def _token_needs_refresh(self) -> bool:
        if not self._access_token:
            return True
        if self._access_expires is None:
            return bool(self._refresh_token)
        lead = timedelta(seconds=TOKEN_REFRESH_LEAD_SECONDS)
        return datetime.now(timezone.utc) >= (self._access_expires - lead)

    def _ensure_token_valid(self) -> None:
        if not self._token_needs_refresh():
            return
        with self._refresh_lock:
            if not self._token_needs_refresh():
                return
            if self._refresh_token:
                try:
                    self.refresh_access_token()
                    return
                except TokenExpiredError:
                    log.warning("refresh token expired; attempting re-login")
                except Exception as err:
                    log.error("token refresh request failed: %s", err)
            try:
                self.login()
                return
            except AuthenticationError as err:
                raise TokenExpiredError(
                    f"Re-login failed (credentials rejected): {err}"
                ) from err
            except Exception as err:
                raise TokenExpiredError(
                    f"Re-login failed (network error): {err}"
                ) from err

    def _post(self, path: str, payload: dict[str, Any], *, timeout: float = HTTP_TIMEOUT) -> dict[str, Any]:
        self._ensure_token_valid()
        resp = self.http.post(f"{API_BASE_URL}{path}", json=payload, timeout=timeout)
        if resp.status_code == 401:
            self._access_expires = None
            self._ensure_token_valid()
            resp = self.http.post(f"{API_BASE_URL}{path}", json=payload, timeout=timeout)
        resp.raise_for_status()
        return resp.json()

    def _get(self, path: str, params: dict[str, Any], *, timeout: float = HTTP_TIMEOUT) -> dict[str, Any]:
        self._ensure_token_valid()
        resp = self.http.get(f"{API_BASE_URL}{path}", params=params, timeout=timeout)
        if resp.status_code == 401:
            self._access_expires = None
            self._ensure_token_valid()
            resp = self.http.get(f"{API_BASE_URL}{path}", params=params, timeout=timeout)
        resp.raise_for_status()
        return resp.json()

    def _now(self) -> datetime:
        try:
            return datetime.now(tz=ZoneInfo(self._time_zone))
        except Exception:
            return datetime.now(timezone.utc)

    def _format_time(self, dt: datetime) -> str:
        try:
            dt = dt.astimezone(ZoneInfo(self._time_zone))
        except Exception:
            pass
        return dt.replace(microsecond=0).isoformat()

    def list_devices(self, station_id: str, page_size: int = 50) -> list[dict[str, Any]]:
        devices: list[dict[str, Any]] = []
        page = 1
        total: int | None = None
        while True:
            data = self._post(API_DEVICE_LIST, {
                "page": page, "count": page_size, "stationId": station_id,
            })
            if data.get("code") not in (0, None):
                raise PortalError(
                    data.get("message") or data.get("msg") or "",
                    code=data.get("code"),
                )
            d = data.get("data") or {}
            total = d.get("total", total)
            batch = d.get("list") or []
            if not isinstance(batch, list):
                batch = []
            devices.extend(batch)
            if total is None:
                if len(batch) < page_size:
                    break
            else:
                if len(devices) >= int(total):
                    break
            if not batch:
                break
            page += 1
        return devices

    def fetch_latest_data(self, device_id: str) -> dict[str, Any]:
        # The portal's device page reads live values from the per-device
        # latest-state endpoint. Station history is only a gap fill for
        # inverter firmware that still publishes the older key names.
        try:
            latest_values = self._fetch_latest_state(device_id)
        except TokenExpiredError:
            raise
        except Exception as err:
            log.warning("device %s: latest state unavailable: %s", device_id, err)
            latest_values = {}
        if latest_values.get("batterySOC") is None:
            try:
                history = self._fetch_time_series_values(device_id)
            except TokenExpiredError:
                raise
            except Exception as err:
                log.warning("device %s: time-series unavailable: %s", device_id, err)
                history = {}
            for key, value in history.items():
                latest_values.setdefault(key, value)
        # One history key, even a zero, used to skip this fallback entirely
        # and leave battery SOC empty. Fill gaps only — setdefault below
        # keeps a real reading.
        if (
            not has_realtime_values(latest_values)
            or latest_values.get("batterySOC") is None
        ):
            try:
                fields = self.fetch_energy_flow(device_id)
            except TokenExpiredError:
                raise
            except EnergyFlowRuleNotConfiguredError as err:
                log.warning(
                    "device %s: no energy-flow rule in the Siseli portal (%s)",
                    device_id, err,
                )
            except Exception as err:
                log.debug("device %s: energy-flow fallback unavailable: %s",
                          device_id, err)
            else:
                mapped = map_latest_state_fields(fields)
                mapped.update({
                    k: v for k, v in map_energy_flow_fields(fields).items()
                    if k not in mapped
                })
                if mapped:
                    for key, value in mapped.items():
                        latest_values.setdefault(key, value)
                elif fields:
                    log.warning(
                        "device %s: energy-flow returned %d field(s) "
                        "but none matched a known mapping: %s",
                        device_id, len(fields), sorted(fields),
                    )
        apply_derived_values(latest_values)
        return latest_values

    def _fetch_latest_state(self, device_id: str) -> dict[str, Any]:
        data = self._get(API_STATE_LATEST, {"deviceId": device_id, "dataSource": 1})
        if data.get("code") not in (0, None, "0"):
            raise RuntimeError(
                f"Latest state error code={data.get('code')} "
                f"message={data.get('message')}"
            )
        payload = data.get("data") or {}
        fields = payload.get("fields") if isinstance(payload, dict) else None
        return map_latest_state_fields(fields)

    def _fetch_time_series_values(self, device_id: str) -> dict[str, Any]:
        end_time = self._now()
        start_time = end_time - timedelta(hours=1)
        alias_groups = [
            ("pvInputPower", "pvPower"),
            ("acOutputActivePower", "outputActivePower"),
            ("batterySOC", "batteryCapacity"),
        ]
        keys = [
            "pvInputPower", "pvPower",
            "acOutputActivePower", "outputActivePower",
            "batteryDischargeCurrent", "batteryChargingCurrent",
            "batteryVoltage", "feedInPower",
            "batterySOC", "batteryCapacity",
        ]
        data = self._post(API_TIME_SERIES, {
            "deviceId": device_id,
            "count": 2000,
            "page": 1,
            "fromTime": self._format_time(start_time),
            "toTime": self._format_time(end_time),
            "orderByTimeAsc": True,
            "keys": keys,
        })
        if data.get("code") not in (0, None, "0"):
            raise RuntimeError(
                f"Timeseries error code={data.get('code')} "
                f"message={data.get('message')}"
            )
        payload_data = (data.get("data") or {}).get("payload") or {}
        fields = payload_data.get("fields") or {}
        latest_values: dict[str, Any] = {}
        for key, arr in fields.items():
            if isinstance(arr, list) and arr:
                latest_values[key] = arr[-1]
        aliased_keys: set[str] = set()
        for canonical, alternate in alias_groups:
            if latest_values.get(canonical) is None and latest_values.get(alternate) is not None:
                latest_values[canonical] = latest_values[alternate]
                aliased_keys.add(canonical)
                latest_values.pop(alternate, None)
        if "acOutputActivePower" in latest_values:
            converted = coerce_number(latest_values["acOutputActivePower"])
            if converted is not None:
                latest_values["acOutputActivePower"] = converted * 1000.0
        if "pvInputPower" in aliased_keys:
            converted = coerce_number(latest_values["pvInputPower"])
            if converted is not None:
                latest_values["pvInputPower"] = converted * 1000.0
        return latest_values

    def fetch_energy_flow(self, device_id: str) -> dict[str, Any]:
        data = self._get(API_ENERGY_FLOW, {"deviceId": device_id, "dataSource": 1})
        code = data.get("code")
        if code not in (0, None, "0"):
            message = data.get("message") or data.get("msg")
            if code in (70132, "70132"):
                raise EnergyFlowRuleNotConfiguredError(
                    f"Energy-flow error code={code} message={message}"
                )
            raise RuntimeError(f"Energy-flow error code={code} message={message}")
        payload = data.get("data") or {}
        state = payload.get("deviceAttributeState") or {}
        fields = state.get("fields")
        if not isinstance(fields, dict):
            fields = payload.get("fields")
        return fields if isinstance(fields, dict) else {}

    def fetch_monthly_summary(self, station_id: str) -> dict[str, Any]:
        now = self._now()
        year = now.year
        month_key = f"{year}-{str(now.month).zfill(2)}"
        self._ensure_token_valid()
        resp = self.http.post(
            f"{API_BASE_URL}{API_MONTHLY_SUMMARY}"
            f"?stationId={station_id}&summaryCategoryKey=pvInverterElectricityQuantityClass",
            json={"time": str(year)},
            timeout=HTTP_TIMEOUT,
        )
        resp.raise_for_status()
        data = resp.json()
        if data.get("code") not in (0, None):
            raise RuntimeError(
                f"Monthly summary error code={data.get('code')} "
                f"message={data.get('message')}"
            )
        props = (((data.get("data") or {}).get("properties"))
                 or (data.get("data") or {}).get("list") or [])
        result: dict[str, Any] = {}
        for item in props if isinstance(props, list) else []:
            k = item.get("key") or item.get("name")
            v = item.get("value")
            if k and v is not None:
                result[k] = v
        monthly: dict[str, Any] = {}
        pv_total = result.get(month_key) or result.get("pvTotal") or result.get("pv") or 0
        monthly["monthly_pv_generated"] = float(pv_total or 0)
        grid_import = result.get("gridImport") or result.get("buy") or 0
        monthly["monthly_grid_import"] = float(grid_import or 0)
        total_consumption = result.get("totalConsumption") or result.get("load") or 0
        monthly["monthly_total_consumption"] = float(total_consumption or 0)
        if monthly["monthly_total_consumption"] > 0:
            monthly["monthly_solar_percentage"] = round(
                100.0 * monthly["monthly_pv_generated"]
                / monthly["monthly_total_consumption"], 1,
            )
        else:
            monthly["monthly_solar_percentage"] = 0.0
        return monthly

    def get_device_settings(self, device_id: str) -> dict[str, Any]:
        self._ensure_token_valid()
        url = f"{API_BASE_URL}{API_SETTINGS_GET}?deviceId={device_id}"
        resp = self.http.post(url, json={}, timeout=HTTP_TIMEOUT)
        if resp.status_code == 401:
            self._access_expires = None
            self._ensure_token_valid()
            resp = self.http.post(url, json={}, timeout=HTTP_TIMEOUT)
        resp.raise_for_status()
        data = resp.json()
        if data.get("code") not in (0, None):
            raise RuntimeError(
                f"Settings fetch error code={data.get('code')} "
                f"message={data.get('message')}"
            )
        return data.get("data") or {}

    def _write_setting(self, device_id: str, key: str, value: Any) -> None:
        self._ensure_token_valid()
        url = f"{API_BASE_URL}{API_SETTINGS_SET}?deviceId={device_id}"
        payload = {"id": device_id, "deviceId": device_id, "key": key, "value": value}
        resp = self.http.post(url, json=payload, timeout=HTTP_TIMEOUT)
        if resp.status_code == 401:
            self._access_expires = None
            self._ensure_token_valid()
            resp = self.http.post(url, json=payload, timeout=HTTP_TIMEOUT)
        resp.raise_for_status()
        data = resp.json()
        if data.get("code") not in (0, None):
            raise RuntimeError(
                f"Settings write error code={data.get('code')} "
                f"message={data.get('message')} (key={key})"
            )

    def set_device_setting(
        self, device_id: str, canonical: str, value: Any,
        settings: dict[str, Any] | None = None,
    ) -> None:
        spec = next((c for c in CONTROL_DEFINITIONS if c["canonical"] == canonical), None)
        write_canonical = (spec or {}).get("write_canonical") or canonical
        if settings is None:
            key = write_canonical
            cached = None
        else:
            key = resolve_setting_key(settings, write_canonical) or write_canonical
            cached = settings.get(key)
        value = coerce_setting_write_value(value, spec, cached)
        self._write_setting(device_id, key, value)
