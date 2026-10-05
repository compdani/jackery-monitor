"""EcoFlow encrypted credentials at rest."""
from __future__ import annotations

import importlib
import json

import pytest


@pytest.fixture()
def fresh_creds(tmp_path, monkeypatch):
    creds_file = tmp_path / "ecoflow-creds.json"
    monkeypatch.setenv("JACKERY_ECOFLOW_CREDS_FILE", str(creds_file))
    monkeypatch.setenv("JACKERY_AT_REST_KEY_FILE", str(tmp_path / ".key"))
    import crypto_util
    importlib.reload(crypto_util)
    import ecoflow_creds as ec
    importlib.reload(ec)
    return ec, creds_file


def test_round_trip_save_and_load(fresh_creds):
    ec, _ = fresh_creds
    assert not ec.has_credentials()
    assert ec.load() is None
    assert ec.save("user@example.com", "hunter2", api_host="api-e.ecoflow.com")
    d = ec.load()
    assert d is not None
    assert d["email"] == "user@example.com"
    assert d["password"] == "hunter2"
    assert d["api_host"] == "api-e.ecoflow.com"
    view = ec.public_view()
    assert view["email"] == "user@example.com"
    assert view["password"] == ""
    assert view["has_password"] is True


def test_empty_password_keeps_previous(fresh_creds):
    ec, _ = fresh_creds
    ec.save("user@example.com", "hunter2")
    ec.save("user@example.com", "", api_host="api.ecoflow.com")
    d = ec.load()
    assert d["password"] == "hunter2"


def test_password_encrypted_at_rest(fresh_creds):
    ec, path = fresh_creds
    ec.save("user@example.com", "super-secret-password")
    raw = path.read_text()
    assert "super-secret-password" not in raw
    assert "ct" in json.loads(raw)


def test_clear(fresh_creds):
    ec, _ = fresh_creds
    ec.save("user@example.com", "hunter2")
    assert ec.clear() is True
    assert ec.load() is None


def test_mqtt_client_id_round_trip(fresh_creds):
    ec, _ = fresh_creds
    assert ec.save(
        "user@example.com", "hunter2",
        mqtt_client_id="ANDROID_ABCDEF0123456789ABCDEF0123456789_99",
    )
    d = ec.load()
    assert d["mqtt_client_id"] == "ANDROID_ABCDEF0123456789ABCDEF0123456789_99"
    assert ec.update_session(
        "tok", "uid-1",
        mqtt_client_id="ANDROID_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_uid-1",
    )
    again = ec.load()
    assert again["token"] == "tok"
    assert again["user_id"] == "uid-1"
    assert again["mqtt_client_id"] == (
        "ANDROID_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_uid-1"
    )
    # save without mqtt_client_id keeps the previous value
    assert ec.save("user@example.com", "hunter2")
    assert ec.load()["mqtt_client_id"] == (
        "ANDROID_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_uid-1"
    )
