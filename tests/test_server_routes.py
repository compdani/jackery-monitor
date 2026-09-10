"""End-to-end smoke tests for FastAPI routes registered in server.py.

Uses TestClient with a mock backend + isolated /data dir. Each test
reloads `server` so its module-level `state` picks up the patched env
vars (same pattern as test_server_view.py).

Coverage focus: high-traffic happy paths + the auth gate. Detailed
behavioral tests for individual subsystems live in their own files
(test_automation, test_smart_charge, test_energy_db, etc.); these are
the route-layer tests the audit flagged as missing.
"""

from __future__ import annotations

import importlib
import os

import pytest
from fastapi.testclient import TestClient

# Force mock backend at module-import time so server's `state.client`
# lands on the synthetic generator instead of trying to connect to the
# bridge daemon during the lifespan startup.
os.environ["BACKEND"] = "mock"


@pytest.fixture()
def app(isolated_data, monkeypatch, tmp_path):
    """Reload server with isolated data and a mock backend; return the
    module so tests can also reach `state` directly when needed.

    Every module that reads a /data path at import time has its
    module-level constants frozen on first import. We force-reload the
    relevant modules in dependency order so they pick up the patched
    env vars from the isolated_data fixture (which runs first)."""
    monkeypatch.setenv("JACKERY_MOCK", "1")
    monkeypatch.setenv("BACKEND", "mock")
    monkeypatch.setenv("JACKERY_DB", str(tmp_path / "energy.db"))
    monkeypatch.setenv("JACKERY_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("JACKERY_BACKUP_CREDS_FILE",
                       str(tmp_path / "backup-creds.json"))

    # Reload in dependency order so each module sees the patched env
    # AND its dependencies' updated constants. crypto_util has to be
    # first since auth, *_creds modules all depend on it.
    import crypto_util
    importlib.reload(crypto_util)
    for name in (
        "auth", "settings", "automation", "location", "smart_charge",
        "cost", "anthropic_creds", "anthropic_prefs",
        "kasa_creds", "kasa_devices", "backup_creds", "energy_db",
        "solar_array", "forecast_solar", "load_schedule",
    ):
        mod = importlib.import_module(name)
        importlib.reload(mod)

    import server as server_mod
    importlib.reload(server_mod)
    return server_mod


@pytest.fixture()
def unauth_client(app):
    """TestClient with NO auth user set up — used to exercise the
    /setup redirect path."""
    with TestClient(app.app) as c:
        yield c


@pytest.fixture()
def client(app):
    """TestClient with an admin user pre-created. Cookies are set on
    the client jar so subsequent requests are authenticated."""
    with TestClient(app.app) as c:
        r = c.post(
            "/api/auth/setup",
            json={"username": "smoke", "password": "smokesmokesmoke"},
        )
        assert r.status_code == 200, r.text
        yield c


# ---------- auth flow + middleware ----------

def test_unauth_root_redirects_to_setup_when_no_user(unauth_client):
    """Fresh install (no user yet) → / redirects to /setup."""
    r = unauth_client.get("/", follow_redirects=False)
    assert r.status_code == 303
    assert r.headers["location"] == "/setup"


def test_unauth_api_returns_401_when_no_user(unauth_client):
    """Same gate, but for an /api/* path → 401 setup_required."""
    r = unauth_client.get("/api/status")
    assert r.status_code == 401
    assert r.json() == {"detail": "setup_required"}


def test_auth_setup_creates_user_and_sets_cookie(unauth_client):
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    assert r.status_code == 200
    body = r.json()
    assert body["ok"] is True
    assert body["username"] == "alice"
    assert body["token"] and "." in body["token"]
    # Subsequent setup attempts are 403.
    r2 = unauth_client.post(
        "/api/auth/setup",
        json={"username": "bob", "password": "anothersecret"},
    )
    assert r2.status_code == 403


def test_auth_setup_rejects_short_password(unauth_client):
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "short"},
    )
    assert r.status_code == 400


def test_session_cookie_secure_flag_follows_env(unauth_client, monkeypatch):
    """JACKERY_COOKIE_SECURE=1 must promote the Set-Cookie to Secure.
    Default (unset/0) leaves it off so Cloudflare Tunnel HTTP-to-origin
    deployments still work."""
    monkeypatch.setenv("JACKERY_COOKIE_SECURE", "1")
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    assert r.status_code == 200
    set_cookie = r.headers.get("set-cookie", "")
    assert "jackery_session=" in set_cookie
    assert "Secure" in set_cookie
    assert "HttpOnly" in set_cookie
    assert "SameSite=lax" in set_cookie.lower() or "samesite=lax" in set_cookie.lower()


def test_session_cookie_default_is_not_secure(unauth_client, monkeypatch):
    """Default: no Secure flag — matches the canonical Cloudflare Tunnel
    deployment where the origin sees plain HTTP."""
    monkeypatch.delenv("JACKERY_COOKIE_SECURE", raising=False)
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    assert r.status_code == 200
    set_cookie = r.headers.get("set-cookie", "")
    assert "jackery_session=" in set_cookie
    # No "Secure" attribute on the cookie.
    assert "secure" not in set_cookie.lower()


def test_auth_login_logout_round_trip(client):
    # client fixture already created user "smoke"; logout, then back in.
    r = client.post("/api/auth/logout")
    assert r.status_code == 200
    # /api/auth/me should now 401.
    r = client.get("/api/auth/me")
    assert r.status_code == 401
    r = client.post(
        "/api/auth/login",
        json={"username": "smoke", "password": "smokesmokesmoke"},
    )
    assert r.status_code == 200
    r = client.get("/api/auth/me")
    assert r.status_code == 200
    assert r.json()["username"] == "smoke"


def test_bearer_token_authenticates_without_cookie(app, unauth_client):
    """Native clients store the JSON token and send Authorization: Bearer.
    A fresh client with no cookie jar must still reach /api/status."""
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    token = r.json()["token"]
    with TestClient(app.app) as c:
        denied = c.get("/api/status")
        assert denied.status_code == 401
        ok = c.get("/api/status", headers={"Authorization": f"Bearer {token}"})
        assert ok.status_code == 200
        me = c.get("/api/auth/me", headers={"Authorization": f"Bearer {token}"})
        assert me.status_code == 200
        assert me.json()["username"] == "alice"
        bad = c.get("/api/status", headers={"Authorization": "Bearer not.a.token"})
        assert bad.status_code == 401


def test_status_accepts_view_device_id_query(client):
    """Unknown view id falls back to the bridge-active device (same as
    a missing cookie). The query param is what native clients use."""
    r = client.get("/api/status", params={"view_device_id": "id-DOES-NOT-EXIST"})
    assert r.status_code == 200
    assert "device" in r.json()


def test_ws_accepts_query_token(app, unauth_client):
    r = unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    token = r.json()["token"]
    with TestClient(app.app) as c:
        with c.websocket_connect(f"/ws?token={token}") as ws:
            msg = ws.receive_json()
            assert msg["type"] == "snapshot"
            assert "data" in msg


def test_ws_rejects_invalid_query_token(app, unauth_client):
    unauth_client.post(
        "/api/auth/setup",
        json={"username": "alice", "password": "verysecret"},
    )
    with TestClient(app.app) as c:
        with pytest.raises(Exception):
            with c.websocket_connect("/ws?token=not.a.token") as ws:
                ws.receive_json()


def test_ws_cookie_still_works(client):
    with client.websocket_connect("/ws") as ws:
        msg = ws.receive_json()
        assert msg["type"] == "snapshot"


def test_auth_login_rejects_wrong_password(client):
    r = client.post(
        "/api/auth/login",
        json={"username": "smoke", "password": "WRONG"},
    )
    assert r.status_code == 401


def test_change_password_round_trip(client):
    r = client.post(
        "/api/auth/change_password",
        json={"current": "smokesmokesmoke", "new": "newsecretpw"},
    )
    assert r.status_code == 200
    # Old password should now fail.
    client.post("/api/auth/logout")
    r = client.post(
        "/api/auth/login",
        json={"username": "smoke", "password": "smokesmokesmoke"},
    )
    assert r.status_code == 401
    # New password works.
    r = client.post(
        "/api/auth/login",
        json={"username": "smoke", "password": "newsecretpw"},
    )
    assert r.status_code == 200


# ---------- core read endpoints ----------

def test_status_returns_telemetry_after_auth(client):
    r = client.get("/api/status")
    assert r.status_code == 200
    body = r.json()
    # connect_device runs async in the lifespan, so a fast test can
    # land before it completes — accept any non-error state.
    assert body["connection_status"] in (
        "scanning", "connecting", "connected", "disconnected",
    )
    # Top-level keys the UI relies on.
    assert "device" in body


def test_devices_endpoint_returns_list(client):
    r = client.get("/api/devices")
    assert r.status_code == 200
    body = r.json()
    assert "devices" in body
    assert isinstance(body["devices"], list)


def test_devices_capacity_endpoint(client):
    r = client.get("/api/devices/capacity")
    assert r.status_code == 200
    assert "devices" in r.json()


def test_settings_get_and_save_round_trip(client):
    r = client.get("/api/settings")
    assert r.status_code == 200
    body = r.json()
    # /api/settings returns metadata (label/hint/default + current value)
    # under "settings", keyed by name with the schema as values.
    assert "settings" in body
    keys = {s["key"] for s in body["settings"]}
    assert "low_battery_threshold" in keys
    # Round-trip a benign setting.
    r = client.post(
        "/api/settings",
        json={"low_battery_threshold": 12},
    )
    assert r.status_code == 200
    r = client.get("/api/settings")
    by_key = {s["key"]: s for s in r.json()["settings"]}
    assert by_key["low_battery_threshold"].get("value") == 12


def test_anthropic_models_returns_fallback_without_key(client):
    """No key configured → endpoint must still return a populated list
    (the static fallback) so the UI dropdown isn't empty."""
    r = client.get("/api/anthropic/models")
    assert r.status_code == 200
    body = r.json()
    assert body["source"].startswith("fallback")
    assert len(body["models"]) > 0


def test_automation_rules_initially_empty(client):
    r = client.get("/api/automation/rules")
    assert r.status_code == 200
    body = r.json()
    assert body["rules"] == []


def test_algorithm_suggestions_initially_empty(client):
    r = client.get("/api/algorithm/suggestions")
    assert r.status_code == 200
    body = r.json()
    assert body["suggestions"] == []


def test_algorithm_changes_initially_empty(client):
    r = client.get("/api/algorithm/changes")
    assert r.status_code == 200
    body = r.json()
    assert body["changes"] == []


def test_kasa_saved_initially_empty(client):
    """The /api/kasa/devices route runs a live LAN discovery (which 500s
    when python-kasa isn't installed in CI). /api/kasa/saved reads the
    persisted registry — that's the route the UI hits on tab open."""
    r = client.get("/api/kasa/saved", params={"refresh": "false"})
    assert r.status_code == 200
    body = r.json()
    assert body["devices"] == []


def test_backup_status_endpoint(client):
    r = client.get("/api/backup/status")
    assert r.status_code == 200
    # Should have at least the configured/last_run scaffolding even when
    # nothing's been backed up yet.
    body = r.json()
    assert isinstance(body, dict)


def test_smart_charge_config_returns_default(client):
    """No active device picked + no configs saved → endpoint should
    return a usable default config rather than 500."""
    r = client.get("/api/smart_charge/config")
    assert r.status_code == 200
    body = r.json()
    assert "config" in body
    assert "mode" in body["config"]


def test_cost_plan_get_endpoint(client):
    r = client.get("/api/cost/plan")
    assert r.status_code == 200
    assert isinstance(r.json(), dict)


def test_location_get_endpoint(client):
    r = client.get("/api/location")
    assert r.status_code == 200


# ---------- error paths ----------

def test_review_now_requires_device(client):
    """No active device + no device_sn arg → 400, not 500."""
    r = client.post("/api/algorithm/review_now", params={})
    # Mock backend DOES advertise a device, so this might actually 202.
    # Either is acceptable; we just want to know we don't 500.
    assert r.status_code in (202, 400)


def test_unknown_route_returns_404(client):
    r = client.get("/api/totally/made/up")
    assert r.status_code == 404


# ---------- energy daily rollup ----------

def test_energy_daily_route_shape_and_clamp(app, client):
    """/api/energy/daily: contract shape, days clamp, and per-device
    resolution. DB-level rollup math is covered in test_energy_db; this
    is the route-layer regression the integration review flagged."""
    sn = "TEST-DAILY-RT"
    app.state.energy.upsert_device(sn, "Rig", 13, "Explorer 5000 Plus")
    bucket = (int(__import__("time").time()) - 3600) // 60 * 60
    with app.state.energy._conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO samples
                   (device_sn, bucket, solar_wh, output_wh, input_wh,
                    ac_input_wh, solar_charge_diverted_wh,
                    last_solar_w, last_output_w, last_battery_pct,
                    sample_count)
               VALUES (?, ?, 1500, 800, 900, 200, 100, 3200, 1900, 42, 1)""",
            (sn, bucket))

    r = client.get(f"/api/energy/daily?device_sn={sn}&days=9999")
    assert r.status_code == 200
    j = r.json()
    assert j["device_sn"] == sn
    assert j["days"] == 365                      # clamped
    assert isinstance(j["tz_offset_s"], int)
    assert len(j["daily"]) == 1
    row = j["daily"][0]
    assert set(row) == {"date", "solar_kwh", "consumed_kwh", "charged_kwh",
                        "grid_kwh", "diverted_kwh", "peak_solar_w",
                        "peak_output_w", "min_soc", "max_soc"}
    assert row["solar_kwh"] == 1.5
    assert row["peak_output_w"] == 1900
    assert row["min_soc"] == 42 and row["max_soc"] == 42

    # Unknown device -> empty, not an error.
    r2 = client.get("/api/energy/daily?device_sn=NOPE")
    assert r2.status_code == 200 and r2.json()["daily"] == []


def test_energy_daily_requires_auth(unauth_client):
    unauth_client.post("/api/auth/setup",
                       json={"username": "u", "password": "p" * 12})
    unauth_client.post("/api/auth/logout")
    r = unauth_client.get("/api/energy/daily")
    assert r.status_code == 401


# ---------- energy history bucket size ----------

def test_energy_history_bucket_s_allow_list_and_coarsen(app, client):
    """/api/energy/history honors 15m/30m/1h, falls back on junk, and
    coarsens a 1y+15m request so the series stays near the point cap."""
    sn = "TEST-HIST-BUCKET"
    app.state.energy.upsert_device(sn, "Rig", 13, "Explorer 5000 Plus")

    r = client.get(f"/api/energy/history?hours=24&bucket_s=900&device_sn={sn}")
    assert r.status_code == 200
    j = r.json()
    assert j["hours"] == 24
    assert j["bucket_s"] == 900

    r30 = client.get(f"/api/energy/history?hours=24&bucket_s=1800&device_sn={sn}")
    assert r30.json()["bucket_s"] == 1800

    r1h = client.get(f"/api/energy/history?hours=24&bucket_s=3600&device_sn={sn}")
    assert r1h.json()["bucket_s"] == 3600

    r_bad = client.get(f"/api/energy/history?hours=24&bucket_s=123&device_sn={sn}")
    assert r_bad.json()["bucket_s"] == 900

    r_omit = client.get(f"/api/energy/history?hours=24&device_sn={sn}")
    assert r_omit.json()["bucket_s"] == 900

    r_year = client.get(
        f"/api/energy/history?hours=8760&bucket_s=900&device_sn={sn}")
    jy = r_year.json()
    expected = app._energy_history_bucket_s(8760, 900)
    assert expected > 900
    assert jy["bucket_s"] == expected
    assert jy["hours"] == 8760
    # Empty DB still returns the coarsened bucket; with data the series
    # would stay at/under the cap.
    assert len(jy["history"]) <= app._ENERGY_HISTORY_MAX_POINTS


def test_shell_sends_no_cache_and_forecast_load_markup(client):
    """Forecast array/load cards must ship in the HTML shell, and / + /sw.js
    revalidate so a PWA/CDN cannot keep an old Forecast tab after deploy."""
    r = client.get("/")
    assert r.status_code == 200
    assert "no-cache" in (r.headers.get("cache-control") or "").lower()
    html = r.text
    assert 'id="forecast-array"' in html
    assert 'id="forecast-load"' in html
    assert 'id="load-add-window"' in html
    assert 'id="load-windows-wrap"' in html
    assert 'id="load-windows-wrap" hidden' not in html
    sw = client.get("/sw.js")
    assert sw.status_code == 200
    assert "no-cache" in (sw.headers.get("cache-control") or "").lower()
    assert "jackery-shell-v5" in sw.text


def test_solar_array_validation_and_roundtrip(app, client):
    r = client.post("/api/forecast/solar_array",
                    json={"declination": 20, "azimuth": 0, "kwp": 2.4})
    assert r.status_code == 200
    assert r.json()["declination"] == 20
    g = client.get("/api/forecast/solar_array")
    assert g.json()["array"]["kwp"] == 2.4
    bad = client.post("/api/forecast/solar_array",
                      json={"declination": 99, "azimuth": 0, "kwp": 2.4})
    assert bad.status_code == 400


def test_solar_array_infer_requires_location(client):
    r = client.post("/api/forecast/solar_array/infer",
                    params={"device_sn": "TEST-INFER"})
    assert r.status_code == 400
    assert "location" in r.json()["detail"]


def test_solar_array_infer_empty_history_400(app, client, monkeypatch):
    loc = client.post("/api/location",
                      json={"latitude": 37.3, "longitude": -121.9})
    assert loc.status_code == 200

    async def no_wx(*_a, **_k):
        return {"hourly": []}

    monkeypatch.setattr(app.weather_client, "fetch_irradiance", no_wx)
    r = client.post("/api/forecast/solar_array/infer",
                    params={"device_sn": "TEST-INFER-EMPTY"})
    assert r.status_code == 400
    assert "not enough" in r.json()["detail"].lower()
    assert client.get("/api/forecast/solar_array").json()["array"] is None


def test_solar_array_infer_fills_without_saving(app, client):
    import math
    import time as _time

    lat, lon = 37.3, -121.9
    true_tilt, true_az = 20, -90
    loc = client.post("/api/location",
                      json={"latitude": lat, "longitude": lon})
    assert loc.status_code == 200

    sn = "TEST-INFER-EAST"
    app.state.energy.upsert_device(sn, "Rig", 13, "Explorer 5000 Plus")
    now_h = (int(_time.time()) // 3600) * 3600
    weather = []
    for day in range(1, 4):
        for hour in range(13, 24):
            ts = now_h - day * 86400 + hour * 3600
            zen, _az = app.solar_array.solar_zenith_azimuth(lat, lon, ts)
            if zen > 80:
                continue
            ghi = max(0.0, 900.0 * math.cos(math.radians(zen)))
            if ghi < 80:
                continue
            poa = app.solar_array.poa_from_ghi(
                ghi, lat, lon, ts, true_tilt, true_az)
            solar_w = 2.0 * poa
            if solar_w < 50:
                continue
            weather.append({"ts": ts, "ghi_w_m2": ghi, "cloud_cover_pct": 10})
            with app.state.energy._conn() as c:
                c.execute(
                    """INSERT OR REPLACE INTO samples
                           (device_sn, bucket, solar_wh, output_wh, input_wh,
                            ac_input_wh, solar_charge_diverted_wh,
                            last_solar_w, last_output_w, last_battery_pct,
                            sample_count)
                       VALUES (?, ?, ?, 0, 0, 0, 0, ?, 0, 50, 1)""",
                    (sn, ts, solar_w, solar_w))
    app.state.energy.upsert_weather_observations(weather)
    assert len(weather) >= 12

    r = client.post("/api/forecast/solar_array/infer",
                    params={"device_sn": sn})
    assert r.status_code == 200, r.text
    j = r.json()
    assert j["azimuth"] == true_az
    assert abs(j["declination"] - true_tilt) <= 10
    assert j["kwp"] >= 0.05
    assert j["n_samples"] >= 12
    # Preview only — Save is a separate POST.
    assert client.get("/api/forecast/solar_array").json()["array"] is None


def test_load_schedule_get_post(app, client):
    sn = "TEST-LOAD-SCHED"
    app.state.energy.upsert_device(sn, "Rig", 13, "Explorer 5000 Plus")
    r = client.post("/api/forecast/load_schedule", json={
        "device_sn": sn,
        "mode": "scheduled",
        "sleep_start": "23:00",
        "sleep_end": "07:00",
        "windows": [{"start": "18:00", "end": "22:00", "watts": 800, "days": "all"}],
    })
    assert r.status_code == 200, r.text
    assert r.json()["mode"] == "scheduled"
    g = client.get(f"/api/forecast/load_schedule?device_sn={sn}")
    assert g.status_code == 200
    j = g.json()
    assert j["sleep_start"] == "23:00"
    assert j["windows"][0]["watts"] == 800
    assert "learned_profile" in j
