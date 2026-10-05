"""EcoFlow private cloud client: app login + MQTT telemetry.

Auth and MQTT certification follow tolwi/hassio-ecoflow-cloud private_api
(Apache-2.0). Delta 3 family property pushes are protobuf; JSON payloads
are also accepted so sample/test data and newer envelopes work.

Telemetry is mapped onto the same live dict shape Jackery/Siseli use.
"""
from __future__ import annotations

import base64
import json
import logging
import ssl
import threading
import time
import uuid
from typing import Any, Callable

import httpx
import paho.mqtt.client as mqtt

import ecoflow_delta3
import ecoflow_devices

log = logging.getLogger("ecoflow_client")

DEFAULT_API_HOST = "api.ecoflow.com"
HTTP_TIMEOUT = 30.0
MQTT_CONNECT_WAIT_S = 10.0
# MQTT CONNACK 4 = bad user/pass, 5 = not authorized
MQTT_AUTH_FAILURE_RCS = frozenset({4, 5})


def resolve_client_id(user_id: str, preferred: str | None = None) -> str:
    """Stable private-API MQTT client id: ANDROID_<32hex>_<userId>.

    The long MD5/verify_info form used by some early reverse-engineered
    clients is rejected by the broker (CONNACK rc=5). Match
    hassio-ecoflow-cloud private_api's working format.
    """
    uid = str(user_id or "").strip()
    if not uid:
        raise ValueError("user_id is required")
    pref = (preferred or "").strip()
    if pref:
        parts = pref.split("_", 2)
        if (
            len(parts) == 3
            and parts[0] == "ANDROID"
            and len(parts[1]) == 32
            and all(c in "0123456789ABCDEF" for c in parts[1])
            and parts[2] == uid
        ):
            return pref
    return f"ANDROID_{uuid.uuid4().hex.upper()}_{uid}"

# Flat params → (telemetry key or special handler, scale)
# Accept snake_case (private protobuf) and camelCase (public / JSON).


def _num(value: Any) -> float | None:
    if isinstance(value, bool) or value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _first(params: dict[str, Any], *keys: str) -> Any:
    for k in keys:
        if k in params and params[k] is not None:
            return params[k]
    return None


def _flow_on(value: Any) -> bool | None:
    """Delta 3 flow_info_*: 14 = on, 4 = off."""
    try:
        v = int(value)
    except (TypeError, ValueError):
        return None
    if v == 14:
        return True
    if v == 4:
        return False
    return None


def solar_inputs_from_params(params: dict[str, Any], *, label_prefix: str = "EcoFlow") -> list[dict]:
    """Build per-string solar list from dual-PV EcoFlow fields."""
    out: list[dict] = []
    pairs = (
        (("pow_get_pv", "powGetPv"), f"{label_prefix} PV1"),
        (("pow_get_pv2", "powGetPv2"), f"{label_prefix} PV2"),
    )
    for keys, label in pairs:
        raw = _first(params, *keys)
        w = _num(raw)
        if w is None:
            continue
        # Keep zero readings so the UI can still show both inputs.
        out.append({
            "id": keys[0],
            "label": label,
            "source": "ecoflow",
            "watts": round(w),
        })
    return out


def params_to_telemetry(
    params: dict[str, Any],
    *,
    device_type: str = "DELTA_3_MAX_PLUS",
    alias: str = "",
    capacity_wh: int | None = None,
) -> dict[str, Any]:
    """Map a flat EcoFlow params dict onto dashboard telemetry."""
    p = params or {}
    prefix = (alias or device_type.replace("_", " ").title()).strip()
    solar_inputs = solar_inputs_from_params(p, label_prefix=prefix)
    solar = sum(float(i["watts"]) for i in solar_inputs)
    if not solar_inputs:
        solo = _num(_first(p, "pow_get_pv", "powGetPv", "solar_input_w"))
        if solo is not None:
            solar = solo
            solar_inputs = [{
                "id": "pow_get_pv",
                "label": f"{prefix} PV1",
                "source": "ecoflow",
                "watts": round(solo),
            }]

    soc = _num(_first(
        p, "cms_batt_soc", "cmsBattSoc", "bms_batt_soc", "bmsBattSoc", "soc",
    ))
    status_raw = _first(
        p, "cms_chg_dsg_state", "cmsChgDsgState",
        "bms_chg_dsg_state", "bmsChgDsgState", "chg_dsg_state",
    )
    try:
        battery_status = int(status_raw) if status_raw is not None else 0
    except (TypeError, ValueError):
        battery_status = 0
    # Public Max Plus docs: 0 idle, 1 discharging, 2 charging — same as Siseli.
    # Private Delta3 BMS uses the same mapping in HA's Delta3ChargingStateSensor.

    ac_in = _num(_first(p, "pow_get_ac_in", "powGetAcIn")) or 0.0
    ac_out = _num(_first(p, "pow_get_ac_out", "powGetAcOut"))
    if ac_out is not None:
        ac_out = abs(ac_out)
    total_out = _num(_first(p, "pow_out_sum_w", "powOutSumW"))
    total_in = _num(_first(p, "pow_in_sum_w", "powInSumW"))
    output = total_out if total_out is not None else (ac_out or 0.0)
    input_w = total_in if total_in is not None else (solar + ac_in)

    ac_on = _flow_on(_first(p, "flow_info_ac_out", "flowInfoAcOut"))
    if ac_on is None:
        cfg = _first(p, "cfg_ac_out_open", "cfgAcOutOpen")
        if cfg is not None:
            ac_on = bool(int(cfg))
    dc_on = _flow_on(_first(p, "flow_info_12v", "flowInfo12v"))
    if dc_on is None:
        cfg = _first(p, "cfg_dc12v_out_open", "cfgDc12vOutOpen", "dc_out_open")
        if cfg is not None:
            dc_on = bool(int(cfg))
    usb_on = None
    cfg_usb = _first(p, "cfg_usb_open", "cfgUsbOpen")
    if cfg_usb is not None:
        usb_on = bool(int(cfg_usb))

    temp = _num(_first(
        p, "bms_max_cell_temp", "bmsMaxCellTemp",
        "bms_min_cell_temp", "temp", "battery_temp_c",
    ))
    chg_min = _num(_first(p, "cms_chg_rem_time", "cmsChgRemTime", "bms_chg_rem_time"))
    dsg_min = _num(_first(p, "cms_dsg_rem_time", "cmsDsgRemTime", "bms_dsg_rem_time"))

    detail = {
        "pow_in_sum_w": _num(_first(p, "pow_in_sum_w", "powInSumW")),
        "pow_out_sum_w": _num(_first(p, "pow_out_sum_w", "powOutSumW")),
        "pow_get_pv": _num(_first(p, "pow_get_pv", "powGetPv")),
        "pow_get_pv2": _num(_first(p, "pow_get_pv2", "powGetPv2")),
        "pow_get_ac_in": ac_in,
        "pow_get_ac_out": ac_out,
        "pow_get_12v": _num(_first(p, "pow_get_12v", "powGet12v")),
        "cms_batt_soc": soc,
        "cms_chg_dsg_state": battery_status,
        "bms_min_cell_temp": _num(_first(p, "bms_min_cell_temp")),
        "bms_max_cell_temp": _num(_first(p, "bms_max_cell_temp")),
        "temp_pcs_dc": _num(_first(p, "temp_pcs_dc")),
        "temp_pcs_ac": _num(_first(p, "temp_pcs_ac")),
        "cycles": _num(_first(p, "cycles")),
        "cms_chg_rem_time": chg_min,
        "cms_dsg_rem_time": dsg_min,
        "ac_out_vol": _num(_first(p, "plug_in_info_ac_out_vol", "plugInInfoAcOutVol")),
        "ac_in_vol": _num(_first(p, "plug_in_info_ac_in_vol", "plugInInfoAcInVol")),
        "device_type": device_type,
    }

    tele: dict[str, Any] = {
        "battery_percent": round(soc) if soc is not None else None,
        "battery_status": battery_status,
        "battery_temp_c": temp,
        "solar_input_w": round(solar),
        "solar_inputs": solar_inputs,
        "ac_input_w": round(ac_in),
        "output_power_w": round(output),
        "input_power_w": round(input_w),
        "car_input_w": 0,
        "feed_in_w": 0,
        "ac_on": bool(ac_on) if ac_on is not None else False,
        "dc_on": bool(dc_on) if dc_on is not None else False,
        "usb_on": bool(usb_on) if usb_on is not None else False,
        "car_on": False,
        "time_to_full_h": (chg_min / 60.0) if chg_min and chg_min > 0 else None,
        "time_remaining_h": (dsg_min / 60.0) if dsg_min and dsg_min > 0 else None,
        "source": "ecoflow",
        "model_name": device_type.replace("_", " ").title(),
        "ecoflow_detail": detail,
    }
    if capacity_wh:
        tele["capacity_wh"] = int(capacity_wh)
    return tele


def merge_params(dst: dict[str, Any], src: dict[str, Any]) -> dict[str, Any]:
    """Merge sparse MQTT deltas into the accumulated params dict."""
    out = dict(dst)
    for k, v in (src or {}).items():
        if v is not None:
            out[k] = v
    return out


class EcoflowAuthError(Exception):
    pass


class EcoflowApiError(Exception):
    def __init__(self, message: str, code: str | None = None):
        super().__init__(message)
        self.code = code


class EcoflowPrivateClient:
    """Login + MQTT session for one EcoFlow app account."""

    def __init__(
        self,
        email: str,
        password: str,
        api_host: str = DEFAULT_API_HOST,
        *,
        mqtt_client_id: str | None = None,
        on_update: Callable[[str, dict[str, Any], dict[str, Any]], None] | None = None,
        http: httpx.Client | None = None,
    ):
        self.email = email.strip()
        self.password = password
        self.api_host = (api_host or DEFAULT_API_HOST).strip().lower()
        self.on_update = on_update
        self._http = http or httpx.Client(timeout=HTTP_TIMEOUT)
        self._owns_http = http is None
        self.token: str | None = None
        self.user_id: str | None = None
        self.mqtt_url: str | None = None
        self.mqtt_port: int = 8883
        self.mqtt_user: str | None = None
        self.mqtt_password: str | None = None
        self._preferred_client_id = (mqtt_client_id or "").strip() or None
        self.mqtt_client_id: str | None = None
        self._mqtt: mqtt.Client | None = None
        self._lock = threading.Lock()
        self._connect_event = threading.Event()
        # raw_sn → accumulated flat params
        self._params: dict[str, dict[str, Any]] = {}
        # raw_sn → device meta from registry
        self._devices: dict[str, dict[str, Any]] = {}
        self.connected = False
        self.auth_failed = False
        self.mqtt_error: str | None = None

    def close(self) -> None:
        self.stop_mqtt()
        if self._owns_http:
            try:
                self._http.close()
            except Exception:
                pass

    def login(self) -> None:
        url = f"https://{self.api_host}/auth/login"
        data = {
            "email": self.email,
            "password": base64.b64encode(self.password.encode()).decode(),
            "scene": "IOT_APP",
            "userType": "ECOFLOW",
        }
        headers = {"lang": "en_US", "content-type": "application/json"}
        resp = self._http.post(url, headers=headers, json=data)
        body = self._json(resp, auth_endpoint=True)
        try:
            self.token = body["data"]["token"]
            self.user_id = str(body["data"]["user"]["userId"])
        except (KeyError, TypeError) as e:
            raise EcoflowApiError(f"login response missing fields: {e}") from e
        cert = self._call_api("/iot-auth/app/certification")
        try:
            self.mqtt_url = cert["data"]["url"]
            self.mqtt_port = int(cert["data"]["port"])
            self.mqtt_user = cert["data"]["certificateAccount"]
            self.mqtt_password = cert["data"]["certificatePassword"]
        except (KeyError, TypeError, ValueError) as e:
            raise EcoflowApiError(f"MQTT certification missing fields: {e}") from e
        self.mqtt_client_id = resolve_client_id(
            self.user_id, self._preferred_client_id,
        )
        self._preferred_client_id = self.mqtt_client_id
        log.info(
            "EcoFlow login OK user_id=%s mqtt=%s:%s client_id=%s user=%s",
            self.user_id, self.mqtt_url, self.mqtt_port,
            self.mqtt_client_id, self.mqtt_user,
        )

    def _call_api(self, endpoint: str, params: dict | None = None) -> dict:
        headers = {
            "lang": "en_US",
            "authorization": f"Bearer {self.token}",
            "content-type": "application/json",
        }
        q = dict(params or {})
        if self.user_id and "userId" not in q:
            q["userId"] = self.user_id
        resp = self._http.get(
            f"https://{self.api_host}{endpoint}",
            headers=headers,
            params=q,
        )
        return self._json(resp, auth_endpoint=False)

    def _json(self, resp: httpx.Response, *, auth_endpoint: bool = True) -> dict:
        if resp.status_code != 200:
            raise EcoflowApiError(f"HTTP {resp.status_code}: {resp.reason_phrase}")
        try:
            body = resp.json()
        except Exception as e:
            raise EcoflowApiError(f"non-JSON response: {e}") from e
        msg = str(body.get("message") or "").lower()
        code = body.get("code")
        if msg != "success":
            detail = str(body.get("message") or "request failed")
            if auth_endpoint or (code is not None and str(code) not in ("0", "None")):
                raise EcoflowAuthError(detail)
            raise EcoflowApiError(detail, code=None if code is None else str(code))
        return body

    def configure_devices(self, devices: list[dict[str, Any]]) -> None:
        """Register devices to subscribe (registry rows with raw_sn)."""
        with self._lock:
            self._devices = {}
            for d in devices:
                raw = str(d.get("raw_sn") or ecoflow_devices.raw_sn_from_sn(d.get("sn") or ""))
                if not raw:
                    continue
                self._devices[raw] = dict(d)
                self._params.setdefault(raw, {})

    def start_mqtt(self, wait_s: float = MQTT_CONNECT_WAIT_S) -> bool:
        """Connect and wait for the first CONNACK. Returns True on success."""
        if not (self.mqtt_url and self.mqtt_user and self.mqtt_password and self.mqtt_client_id):
            raise EcoflowApiError("login first")
        self.stop_mqtt()
        self.connected = False
        self.auth_failed = False
        self.mqtt_error = None
        self._connect_event.clear()
        client = mqtt.Client(
            client_id=self.mqtt_client_id,
            clean_session=True,
            protocol=mqtt.MQTTv311,
        )
        client.username_pw_set(self.mqtt_user, self.mqtt_password)
        client.tls_set(cert_reqs=ssl.CERT_REQUIRED)
        client.tls_insecure_set(False)
        client.on_connect = self._on_connect
        client.on_disconnect = self._on_disconnect
        client.on_message = self._on_message
        log.info(
            "EcoFlow MQTT connecting %s:%s as client_id=%s user=%s",
            self.mqtt_url, self.mqtt_port, self.mqtt_client_id, self.mqtt_user,
        )
        client.connect(self.mqtt_url, self.mqtt_port, keepalive=30)
        client.loop_start()
        self._mqtt = client
        if not self._connect_event.wait(timeout=max(1.0, float(wait_s))):
            self.mqtt_error = "MQTT connect timed out"
            self.connected = False
            log.error("EcoFlow MQTT connect timed out after %.0fs", wait_s)
            return False
        return bool(self.connected)

    def stop_mqtt(self) -> None:
        client = self._mqtt
        self._mqtt = None
        self.connected = False
        if not client:
            return
        try:
            client.loop_stop()
        except Exception:
            pass
        try:
            client.disconnect()
        except Exception:
            pass

    def request_quota(self, raw_sn: str) -> None:
        """Ask the device for a full snapshot (Delta 3 protobuf get)."""
        if not self._mqtt or not self.user_id:
            return
        topic = f"/app/{self.user_id}/{raw_sn}/thing/property/get"
        try:
            payload = ecoflow_delta3.build_quota_request(raw_sn)
            self._mqtt.publish(topic, payload, qos=1)
        except Exception as e:
            log.debug("quota request failed for %s: %s", raw_sn, e)

    def _topics_for(self, raw_sn: str) -> list[str]:
        uid = self.user_id or ""
        return [
            f"/app/device/property/{raw_sn}",
            f"/app/{uid}/{raw_sn}/thing/property/set_reply",
            f"/app/{uid}/{raw_sn}/thing/property/get_reply",
        ]

    def _on_connect(self, client, userdata, flags, rc, properties=None):
        try:
            code = int(rc)
        except (TypeError, ValueError):
            code = -1
        if code != 0:
            self.connected = False
            self.auth_failed = code in MQTT_AUTH_FAILURE_RCS
            if self.auth_failed:
                self.mqtt_error = f"MQTT not authorized (rc={code})"
            else:
                self.mqtt_error = f"MQTT connect failed (rc={code})"
            log.error(
                "EcoFlow MQTT connect failed rc=%s client_id=%s",
                code, self.mqtt_client_id,
            )
            self._connect_event.set()
            return
        self.connected = True
        self.auth_failed = False
        self.mqtt_error = None
        topics = []
        with self._lock:
            sns = list(self._devices.keys())
        for sn in sns:
            for t in self._topics_for(sn):
                topics.append((t, 1))
        if topics:
            client.subscribe(topics)
            log.info("EcoFlow MQTT subscribed to %d topic(s)", len(topics))
            for sn in sns:
                self.request_quota(sn)
        self._connect_event.set()

    def _on_disconnect(self, client, userdata, rc, properties=None):
        self.connected = False
        try:
            code = int(rc)
        except (TypeError, ValueError):
            code = -1
        if code != 0:
            log.warning("EcoFlow MQTT disconnected rc=%s", code)
            if code in MQTT_AUTH_FAILURE_RCS:
                self.auth_failed = True
                self.mqtt_error = f"MQTT not authorized (rc={code})"
        # If connect never completed, unblock waiters.
        self._connect_event.set()

    def _on_message(self, client, userdata, message: mqtt.MQTTMessage):
        topic = message.topic or ""
        raw_sn = self._sn_from_topic(topic)
        if not raw_sn:
            return
        params = self._parse_payload(message.payload or b"", topic)
        if not params:
            return
        with self._lock:
            merged = merge_params(self._params.get(raw_sn) or {}, params)
            self._params[raw_sn] = merged
            meta = dict(self._devices.get(raw_sn) or {})
        sn = ecoflow_devices.make_sn(raw_sn)
        tele = params_to_telemetry(
            merged,
            device_type=str(meta.get("device_type") or "DELTA_3_MAX_PLUS"),
            alias=str(meta.get("alias") or ""),
            capacity_wh=meta.get("capacity_wh"),
        )
        detail = dict(tele.get("ecoflow_detail") or {})
        if self.on_update:
            try:
                self.on_update(sn, tele, detail)
            except Exception:
                log.exception("on_update failed for %s", sn)

    def _sn_from_topic(self, topic: str) -> str | None:
        # /app/device/property/{sn}
        # /app/{uid}/{sn}/thing/property/...
        parts = topic.strip("/").split("/")
        with self._lock:
            known = set(self._devices.keys())
        for p in parts:
            if p in known:
                return p
        if len(parts) >= 4 and parts[0] == "app" and parts[1] == "device":
            return parts[3] if parts[2] == "property" else None
        if len(parts) >= 3 and parts[0] == "app":
            cand = parts[2]
            if cand in known or (known and any(cand == k for k in known)):
                return cand
        return None

    def _parse_payload(self, payload: bytes, topic: str) -> dict[str, Any]:
        # JSON envelope first (legacy / some replies)
        try:
            text = payload.decode("utf-8")
            data = json.loads(text)
            if isinstance(data, dict):
                if isinstance(data.get("params"), dict):
                    return dict(data["params"])
                if isinstance(data.get("data"), dict) and isinstance(
                    data["data"].get("quotaMap"), dict
                ):
                    return dict(data["data"]["quotaMap"])
                # Flat JSON heartbeat (public-style keys)
                if any(k in data for k in ("powGetPv", "cmsBattSoc", "pow_get_pv", "cms_batt_soc")):
                    return dict(data)
        except Exception:
            pass
        # Protobuf Delta 3 family
        try:
            return ecoflow_delta3.decode_property_payload(payload)
        except Exception as e:
            log.debug("protobuf decode failed on %s: %s", topic, e)
            return {}

    def snapshot(self) -> dict[str, dict[str, Any]]:
        """raw_sn → {telemetry, detail, params} for all known devices."""
        with self._lock:
            out = {}
            for raw, meta in self._devices.items():
                params = dict(self._params.get(raw) or {})
                tele = params_to_telemetry(
                    params,
                    device_type=str(meta.get("device_type") or "DELTA_3_MAX_PLUS"),
                    alias=str(meta.get("alias") or ""),
                    capacity_wh=meta.get("capacity_wh"),
                )
                out[raw] = {
                    "telemetry": tele,
                    "detail": dict(tele.get("ecoflow_detail") or {}),
                    "params": params,
                }
            return out
