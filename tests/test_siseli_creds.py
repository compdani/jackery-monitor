"""Tests for siseli_creds — encrypted Solar of Things portal credentials."""
from __future__ import annotations

import importlib
import json
import os

import pytest


@pytest.fixture()
def fresh_creds(tmp_path, monkeypatch):
    creds_file = tmp_path / "siseli-creds.json"
    monkeypatch.setenv("JACKERY_SISELI_CREDS_FILE", str(creds_file))
    monkeypatch.setenv("JACKERY_AT_REST_KEY_FILE", str(tmp_path / ".key"))
    import crypto_util
    importlib.reload(crypto_util)
    import siseli_creds as sc
    importlib.reload(sc)
    return sc, creds_file


def test_round_trip_save_and_load(fresh_creds):
    sc, _ = fresh_creds
    assert not sc.has_credentials()
    assert sc.load() is None
    ok = sc.save(user_id="alice", password="hunter2", station_id="12345")
    assert ok
    d = sc.load()
    assert d is not None
    assert d["user_id"] == "alice"
    assert d["password"] == "hunter2"
    assert d["station_id"] == "12345"


def test_save_rejects_missing_required_fields(fresh_creds):
    sc, _ = fresh_creds
    assert sc.save(user_id="", password="p", station_id="1") is False
    assert sc.save(user_id="u", password="", station_id="1") is False
    assert sc.save(user_id="u", password="p", station_id="") is False


def test_empty_password_keeps_previous(fresh_creds):
    sc, _ = fresh_creds
    sc.save(user_id="alice", password="hunter2", station_id="123")
    sc.save(user_id="alice", password="", station_id="123", time_zone="America/Chicago")
    d = sc.load()
    assert d["password"] == "hunter2"
    assert d["time_zone"] == "America/Chicago"


def test_password_is_encrypted_at_rest(fresh_creds):
    sc, path = fresh_creds
    sc.save(user_id="alice", password="super-secret-password", station_id="99")
    raw = path.read_text()
    assert "super-secret-password" not in raw
    blob = json.loads(raw)
    assert "ct" in blob


def test_public_view_redacts_secrets(fresh_creds):
    sc, _ = fresh_creds
    sc.save(user_id="alice", password="hunter2", station_id="99",
            access_token="tok")
    pv = sc.public_view()
    assert pv is not None
    assert pv["user_id"] == "alice"
    assert pv["password"] == ""
    assert pv["access_token"] == ""
    assert pv["has_password"] is True


def test_update_tokens(fresh_creds):
    sc, _ = fresh_creds
    sc.save(user_id="alice", password="p", station_id="99")
    assert sc.update_tokens("a", "r", "exp-a", "exp-r")
    d = sc.load()
    assert d["access_token"] == "a"
    assert d["refresh_token"] == "r"
    assert d["access_token_expires"] == "exp-a"


def test_clear_removes_file(fresh_creds):
    sc, path = fresh_creds
    sc.save(user_id="u", password="p", station_id="1")
    assert path.exists()
    sc.clear()
    assert not path.exists()
    sc.clear()


def test_file_permissions_are_tight(fresh_creds):
    sc, path = fresh_creds
    sc.save(user_id="u", password="p", station_id="1")
    mode = os.stat(path).st_mode & 0o777
    assert mode == 0o600


def test_backup_includes_siseli_creds():
    import backup
    assert "siseli-creds.json" in backup.SMALL_FILES
    assert "bms_devices.json" in backup.SMALL_FILES
