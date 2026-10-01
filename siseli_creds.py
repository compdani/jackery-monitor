"""Siseli (Solar of Things) portal credentials.

Persisted encrypted at /data/siseli-creds.json (AES-256-GCM via crypto_util),
same pattern as kasa_creds.py. Holds User ID + password + Station ID plus
cached access/refresh tokens so a restart does not have to log in again.
"""
from __future__ import annotations

import json
import logging
import os

import crypto_util

log = logging.getLogger("siseli_creds")

PATH = os.environ.get("JACKERY_SISELI_CREDS_FILE", "/data/siseli-creds.json")

_SECRET_FIELDS = ("password", "access_token", "refresh_token")


def has_credentials() -> bool:
    try:
        return os.path.getsize(PATH) > 0
    except OSError:
        return False


def _as_bool(value) -> bool:
    if isinstance(value, bool):
        return value
    return str(value or "").strip().lower() in {"1", "true", "yes", "on"}


def _normalize(d: dict) -> dict:
    return {
        "user_id": str(d.get("user_id") or "").strip(),
        "password": str(d.get("password") or ""),
        "station_id": str(d.get("station_id") or "").strip(),
        "device_id": str(d.get("device_id") or "").strip(),
        "time_zone": str(d.get("time_zone") or "").strip(),
        "access_token": str(d.get("access_token") or ""),
        "refresh_token": str(d.get("refresh_token") or ""),
        "access_token_expires": str(d.get("access_token_expires") or ""),
        "refresh_token_expires": str(d.get("refresh_token_expires") or ""),
        # LAN read of the dongle's existing cloud MQTT. Optional; portal
        # login still works without it.
        "local_read": _as_bool(d.get("local_read")),
        "inverter_ip": str(d.get("inverter_ip") or "").strip(),
        "router_ip": str(d.get("router_ip") or "").strip(),
        "sniff_iface": str(d.get("sniff_iface") or "").strip(),
        "inverter_mac": str(d.get("inverter_mac") or "").strip().lower(),
        "router_mac": str(d.get("router_mac") or "").strip().lower(),
        # Which plain-MQTT broker supplies Live watts. Empty keeps the portal.
        "mqtt_broker_ip": str(d.get("mqtt_broker_ip") or "").strip(),
    }


def _write(payload: dict) -> bool:
    blob = crypto_util.encrypt(json.dumps(payload).encode())
    try:
        os.makedirs(os.path.dirname(PATH) or ".", exist_ok=True)
        tmp = PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump(blob, f)
        try:
            os.chmod(tmp, 0o600)
        except Exception:
            pass
        os.replace(tmp, PATH)
        return True
    except Exception as e:
        log.error("failed to save siseli creds: %s", e)
        return False


def load() -> dict | None:
    try:
        with open(PATH) as f:
            blob = json.load(f)
    except FileNotFoundError:
        return None
    except Exception as e:
        log.warning("siseli creds file %s unreadable: %s", PATH, e)
        return None
    if not isinstance(blob, dict) or "ct" not in blob:
        return None
    pt = crypto_util.decrypt(blob)
    if pt is None:
        return None
    try:
        d = json.loads(pt.decode())
    except Exception as e:
        log.error("siseli creds invalid JSON after decrypt: %s", e)
        return None
    if not isinstance(d, dict):
        return None
    norm = _normalize(d)
    if not (norm["user_id"] and norm["password"] and norm["station_id"]):
        return None
    return norm


def save(**fields) -> bool:
    """Persist credentials. Required: user_id, password, station_id.

    Extra token fields are kept when present so a later save() from the
    UI does not wipe a still-valid access token unless the password
    changed. Pass password="" to keep the previously stored password.
    """
    existing = None
    try:
        existing = load()
    except Exception:
        existing = None
    merged = dict(existing or {})
    merged.update({k: v for k, v in fields.items() if v is not None})
    if not str(fields.get("password") or "") and existing:
        merged["password"] = existing["password"]
    payload = _normalize(merged)
    if not (payload["user_id"] and payload["password"] and payload["station_id"]):
        return False
    return _write(payload)


def update_tokens(
    access_token: str,
    refresh_token: str,
    access_token_expires: str,
    refresh_token_expires: str,
) -> bool:
    """Write refreshed tokens without requiring the caller to re-supply
    user_id / password / station_id."""
    d = load()
    if not d:
        return False
    d["access_token"] = access_token
    d["refresh_token"] = refresh_token
    d["access_token_expires"] = access_token_expires
    d["refresh_token_expires"] = refresh_token_expires
    return _write(d)


def public_view() -> dict | None:
    d = load()
    if not d:
        return None
    out = dict(d)
    out["has_password"] = bool(d.get("password"))
    for k in _SECRET_FIELDS:
        out[k] = ""
    return out


def clear() -> bool:
    try:
        os.remove(PATH)
        return True
    except FileNotFoundError:
        return True
    except Exception as e:
        log.error("failed to delete siseli creds: %s", e)
        return False
