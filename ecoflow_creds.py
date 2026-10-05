"""EcoFlow app credentials (email + password + API host).

Encrypted at /data/ecoflow-creds.json (AES-256-GCM via crypto_util),
same pattern as kasa_creds.py / siseli_creds.py.
"""
from __future__ import annotations

import json
import logging
import os

import crypto_util

log = logging.getLogger("ecoflow_creds")

PATH = os.environ.get("JACKERY_ECOFLOW_CREDS_FILE", "/data/ecoflow-creds.json")

DEFAULT_API_HOST = "api.ecoflow.com"
ALLOWED_API_HOSTS = frozenset({
    "api.ecoflow.com",
    "api-e.ecoflow.com",
    "api-a.ecoflow.com",
})

_SECRET_FIELDS = ("password", "token")


def has_credentials() -> bool:
    try:
        return os.path.getsize(PATH) > 0
    except OSError:
        return False


def _normalize(d: dict) -> dict:
    host = str(d.get("api_host") or DEFAULT_API_HOST).strip().lower()
    if host not in ALLOWED_API_HOSTS:
        host = DEFAULT_API_HOST
    return {
        "email": str(d.get("email") or "").strip(),
        "password": str(d.get("password") or ""),
        "api_host": host,
        "token": str(d.get("token") or ""),
        "user_id": str(d.get("user_id") or "").strip(),
        "mqtt_client_id": str(d.get("mqtt_client_id") or "").strip(),
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
        log.error("failed to save ecoflow creds: %s", e)
        return False


def load() -> dict | None:
    try:
        with open(PATH) as f:
            blob = json.load(f)
    except FileNotFoundError:
        return None
    except Exception as e:
        log.warning("ecoflow creds file %s unreadable: %s", PATH, e)
        return None
    if not isinstance(blob, dict) or "ct" not in blob:
        return None
    pt = crypto_util.decrypt(blob)
    if pt is None:
        return None
    try:
        d = json.loads(pt.decode())
    except Exception as e:
        log.error("ecoflow creds invalid JSON after decrypt: %s", e)
        return None
    if not isinstance(d, dict):
        return None
    norm = _normalize(d)
    if not (norm["email"] and norm["password"]):
        return None
    return norm


def save(email: str, password: str, *, api_host: str | None = None,
         token: str = "", user_id: str = "",
         mqtt_client_id: str | None = None) -> bool:
    existing = None
    try:
        existing = load()
    except Exception:
        existing = None
    if not password and existing:
        password = existing.get("password") or ""
    if mqtt_client_id is None and existing:
        mqtt_client_id = existing.get("mqtt_client_id") or ""
    payload = _normalize({
        "email": email,
        "password": password,
        "api_host": api_host or (existing or {}).get("api_host") or DEFAULT_API_HOST,
        "token": token or (existing or {}).get("token") or "",
        "user_id": user_id or (existing or {}).get("user_id") or "",
        "mqtt_client_id": mqtt_client_id or "",
    })
    if not (payload["email"] and payload["password"]):
        return False
    return _write(payload)


def update_session(
    token: str,
    user_id: str,
    *,
    mqtt_client_id: str | None = None,
) -> bool:
    d = load()
    if not d:
        return False
    d["token"] = token
    d["user_id"] = user_id
    if mqtt_client_id is not None:
        d["mqtt_client_id"] = mqtt_client_id
    return _write(_normalize(d))


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
        log.error("failed to delete ecoflow creds: %s", e)
        return False
