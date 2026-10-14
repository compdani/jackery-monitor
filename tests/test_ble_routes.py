import asyncio
import json

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

import api_auth
import auth
import ble_inspector as ble
import ble_routes as routes
import bms_ble


@pytest.fixture
def client(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_DATA_DIR", str(tmp_path))
    monkeypatch.setattr(routes, "sessions", routes.Sessions())
    monkeypatch.setattr(bms_ble._adapter_lock, "_lock", asyncio.Lock(), raising=False)
    app = FastAPI()
    app.include_router(routes.router)
    with TestClient(app) as client:
        yield client


def test_validation(client):
    for body in (
        {"operation": "inspect"},
        {"operation": "scan", "seconds": 0},
        {"operation": "scan", "notify": ["uuid"]},
        {"operation": "inspect", "name": "a", "address": "b"},
    ):
        assert client.post("/api/ble/sessions", json=body).status_code == 422


def test_exports_pagination_and_delete(client):
    session = routes.sessions
    session.root.mkdir()
    import uuid

    capture_id = str(uuid.uuid4())
    capture = ble.Capture(session.root / (capture_id + ".jsonl"), {})
    capture.emit("read", **ble.payload(b"\x00\xff"))
    capture.finish("complete")
    rows = client.get(f"/api/ble/captures/{capture_id}/events?after=1&limit=1").json()
    assert rows["next"] == 2 and rows["events"][0]["hex"] == "00ff"
    for fmt in ("json", "jsonl"):
        response = client.get(f"/api/ble/captures/{capture_id}/download?format={fmt}")
        assert response.status_code == 200
        data = (
            response.json()["events"]
            if fmt == "json"
            else [json.loads(s) for s in response.text.splitlines()]
        )
        assert data[-1]["status"] == "complete"
    assert client.get("/api/ble/status").json()["captures"][0]["bytes"] > 0
    assert client.delete(f"/api/ble/captures/{capture_id}").status_code == 200
    assert client.get(f"/api/ble/captures/{capture_id}/events").status_code == 404
    assert client.get("/api/ble/captures/not-a-uuid/events").status_code == 404


def test_session_cleanup_and_busy(client, monkeypatch):
    async def wait(capture, options):
        try:
            await asyncio.sleep(100)
        finally:
            capture.finish("cancelled")

    monkeypatch.setattr(ble, "run", wait)
    response = client.post("/api/ble/sessions", json={"operation": "scan"})
    assert response.status_code == 200
    capture_id = response.json()["id"]
    assert client.post("/api/ble/sessions", json={"operation": "scan"}).status_code == 409
    assert client.delete(f"/api/ble/captures/{capture_id}").status_code == 409
    assert client.get(f"/api/ble/captures/{capture_id}/download").status_code == 409
    assert client.post(f"/api/ble/sessions/{capture_id}/stop").status_code == 200
    assert not bms_ble._adapter_lock().locked()
    assert routes.sessions.capture.file.closed
    assert client.get("/api/ble/status").json()["active"] is None


@pytest.mark.asyncio
async def test_bms_contention(tmp_path, monkeypatch):
    monkeypatch.setenv("JACKERY_DATA_DIR", str(tmp_path))
    lock = asyncio.Lock()
    monkeypatch.setattr(bms_ble._adapter_lock, "_lock", lock, raising=False)
    async with lock:
        from fastapi import HTTPException

        with pytest.raises(HTTPException) as error:
            await routes.Sessions().start(routes.StartRequest(operation="scan"))
        assert error.value.status_code == 409


def test_authentication(isolated_data):
    import importlib

    import crypto_util

    importlib.reload(crypto_util)
    importlib.reload(auth)
    app = FastAPI()
    app.include_router(routes.router)
    # Same production middleware, without starting unrelated pollers.
    api_auth.install(app, web_dir=__import__("pathlib").Path("web"))
    with TestClient(app) as client:
        assert client.get("/api/ble/status").status_code in (401, 403)
        assert client.post("/api/ble/sessions", json={"operation": "scan"}).status_code in (
            401,
            403,
        )
        assert (
            client.post(
                "/api/auth/setup", json={"username": "test", "password": "testing123"}
            ).status_code
            == 200
        )
        assert client.get("/api/ble/status").status_code == 200


def test_storage_failure_releases_lock(client, monkeypatch):
    def fail(*args, **kwargs):
        raise OSError("disk full")

    monkeypatch.setattr(ble, "Capture", fail)
    response = client.post("/api/ble/sessions", json={"operation": "scan"})
    assert response.status_code == 503
    assert not bms_ble._adapter_lock().locked()


def test_interrupted_capture_status(client):
    import uuid

    session = routes.sessions
    session.root.mkdir()
    path = session.root / (str(uuid.uuid4()) + ".jsonl")
    cap = ble.Capture(path, {})
    cap.file.close()  # Simulate process loss before completion.
    assert client.get("/api/ble/status").json()["captures"][0]["status"] == "incomplete"
