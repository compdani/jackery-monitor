"""Custom-window device-param fit: clamps, preview vs apply-as-user."""
from __future__ import annotations

import importlib
import os

import pytest
from fastapi.testclient import TestClient

os.environ["BACKEND"] = "mock"

SN = "FIT-TEST-SN"
FIXED_NOW = 1_800_000_000


@pytest.fixture()
def app(isolated_data, monkeypatch, tmp_path):
    monkeypatch.setenv("JACKERY_MOCK", "1")
    monkeypatch.setenv("BACKEND", "mock")
    monkeypatch.setenv("JACKERY_DB", str(tmp_path / "energy.db"))
    monkeypatch.setenv("JACKERY_DATA_DIR", str(tmp_path))
    monkeypatch.setenv("JACKERY_BACKUP_CREDS_FILE",
                       str(tmp_path / "backup-creds.json"))

    import crypto_util
    importlib.reload(crypto_util)
    for name in (
        "auth", "settings", "automation", "location", "smart_charge",
        "cost", "anthropic_creds", "anthropic_prefs",
        "kasa_creds", "kasa_devices", "backup_creds", "energy_db",
        "solar_array", "forecast_solar", "load_schedule",
        "siseli_creds", "bms_devices", "device_prefs",
        "ecoflow_creds", "ecoflow_devices",
    ):
        mod = importlib.import_module(name)
        importlib.reload(mod)

    import server as server_mod
    importlib.reload(server_mod)
    return server_mod


@pytest.fixture()
def client(app):
    with TestClient(app.app) as c:
        r = c.post(
            "/api/auth/setup",
            json={"username": "smoke", "password": "smokesmokesmoke"},
        )
        assert r.status_code == 200, r.text
        yield c


def _freeze_now(app, monkeypatch):
    monkeypatch.setattr(app.time, "time", lambda: float(FIXED_NOW))
    import energy_db as edb
    monkeypatch.setattr(edb.time, "time", lambda: float(FIXED_NOW))


def test_clamp_fit_lookback_hours(app):
    assert app._clamp_fit_lookback_hours(None) == 14 * 24
    assert app._clamp_fit_lookback_hours(24) == 24
    assert app._clamp_fit_lookback_hours(1) == 6
    assert app._clamp_fit_lookback_hours(99999) == 45 * 24
    assert app._clamp_fit_lookback_hours("nope") == 14 * 24


def _seed_solar_history(energy, *, k_recent: float, k_old: float):
    """Hourly samples: recent 24h use k_recent, older hours use k_old.

    Weather is interval-end stamped (Open-Meteo convention) so it pairs
    with start-stamped telemetry at ts.
    """
    weather = []
    now = FIXED_NOW
    for i in range(14 * 24):
        ts = now - (i + 1) * 3600  # interval start
        age_h = i
        k = k_recent if age_h < 24 else k_old
        ghi = 800.0  # clear-sky pool (>= 700)
        solar_w = k * ghi
        with energy._conn() as c:
            c.execute(
                """INSERT OR REPLACE INTO samples
                       (device_sn, bucket, solar_wh, output_wh, input_wh,
                        ac_input_wh, solar_charge_diverted_wh,
                        last_solar_w, last_output_w, last_input_w,
                        last_ac_input_w, last_battery_pct, sample_count)
                   VALUES (?, ?, ?, 100, 0, 0, 0, ?, 100, 0, 0, 50, 1)""",
                (SN, ts, solar_w, solar_w),
            )
        weather.append({
            "ts": ts + 3600,  # interval end
            "ghi_w_m2": ghi,
            "cloud_cover_pct": 10,
        })
    energy.upsert_weather_observations(weather)


@pytest.mark.asyncio
async def test_fit_24h_vs_14d_differs_and_apply_as_user(app, monkeypatch):
    _freeze_now(app, monkeypatch)
    energy = app.state.energy
    energy.upsert_device(SN, "fit-test", 13, "Explorer 5000 Plus")
    _seed_solar_history(energy, k_recent=2.0, k_old=0.5)

    async def fake_wx(lookback_hours=None):
        hours = app._clamp_fit_lookback_hours(lookback_hours)
        return energy.list_weather_observations(
            since_ts=FIXED_NOW - hours * 3600, limit=hours + 48,
        )

    monkeypatch.setattr(app, "_ensure_weather_hourly", fake_wx)

    # Preview via the same helper the assistant / API use.
    j24 = await app._assistant_fit_device_param(
        SN, key="solar_coefficient", lookback_hours=24, apply=False)
    assert j24["applied"] is False
    assert j24["lookback_hours"] == 24
    assert j24["value"] is not None

    j14 = await app._assistant_fit_device_param(
        SN, key="solar_coefficient", lookback_hours=None, apply=False)
    assert j14["lookback_hours"] == 14 * 24
    assert abs(float(j24["value"]) - float(j14["value"])) > 0.2

    ja = await app._assistant_fit_device_param(
        SN, key="solar_coefficient", lookback_hours=24, apply=True)
    assert ja["applied"] is True
    assert ja["source"] == "user"
    assert "fit over last 24h" in (ja.get("note") or "")

    stored = energy.get_device_param(SN, "solar_coefficient")
    assert stored is not None
    assert stored["source"] == "user"
    assert stored["note"] == "fit over last 24h"
    assert abs(float(stored["value"]) - float(ja["value"])) < 1e-6

    resolved = app.resolve_device_param(SN, "solar_coefficient")
    assert resolved["source"] == "user"
    assert abs(float(resolved["value"]) - float(stored["value"])) < 1e-6


def test_fit_unknown_key_and_capacity_rejected(app, client):
    r = client.post("/api/devices/params/fit", json={
        "device_sn": SN, "key": "not_a_real_param", "apply": False,
    })
    assert r.status_code == 400

    r2 = client.post("/api/devices/params/fit", json={
        "device_sn": SN, "key": "battery_capacity_wh",
        "lookback_hours": 24, "apply": True,
    })
    assert r2.status_code == 400


@pytest.mark.asyncio
async def test_fit_lookback_clamped_in_response(app, monkeypatch):
    _freeze_now(app, monkeypatch)
    monkeypatch.setattr(
        app, "_history_for_fit",
        lambda sn, lookback_hours=None: [],
    )
    energy = app.state.energy
    energy.upsert_device(SN, "fit-test", 13, "Explorer 5000 Plus")

    j = await app._assistant_fit_device_param(
        SN, key="solar_coefficient", lookback_hours=2, apply=False)
    assert j["lookback_hours"] == 6

    j2 = await app._assistant_fit_device_param(
        SN, key="parasitic_w", lookback_hours=99999, apply=False)
    assert j2["lookback_hours"] == 45 * 24


@pytest.mark.asyncio
async def test_refit_lookback_via_resolve(app, monkeypatch):
    _freeze_now(app, monkeypatch)
    energy = app.state.energy
    energy.upsert_device(SN, "fit-test", 13, "Explorer 5000 Plus")
    _seed_solar_history(energy, k_recent=1.5, k_old=0.4)

    wx = energy.list_weather_observations(
        since_ts=FIXED_NOW - 24 * 3600, limit=24 + 48,
    )
    energy.clear_device_param(SN, "solar_coefficient")
    app._clear_history_fit_cache(SN, 24)
    resolved = app.resolve_device_param(
        SN, "solar_coefficient", weather_hourly=wx, lookback_hours=24)
    assert resolved.get("value") is not None
    assert resolved["source"] in ("fit", "default")


def test_fit_route_smoke(app, client):
    """HTTP surface exists and rejects bad keys (auth cookie intact)."""
    r = client.post("/api/devices/params/fit", json={
        "device_sn": SN,
        "key": "solar_coefficient",
        "lookback_hours": 24,
        "apply": False,
    })
    # No history → still 200 with a default/fit value or low samples.
    assert r.status_code == 200, r.text
    j = r.json()
    assert j["lookback_hours"] == 24
    assert j["applied"] is False
    assert "value" in j
