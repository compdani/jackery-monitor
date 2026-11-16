"""Infer tilt / azimuth / kWp from paired solar_w and GHI."""
from __future__ import annotations

import math
from datetime import datetime, timezone

import solar_array as sa


def test_equator_equinox_noon_near_overhead():
    ts = int(datetime(2024, 3, 20, 12, 0, tzinfo=timezone.utc).timestamp())
    zen, az = sa.solar_zenith_azimuth(0.0, 0.0, ts)
    assert zen < 8.0, f"zenith={zen}"
    assert 0 <= az < 360


def test_infer_empty_history_errors():
    out = sa.infer_plane([], [], lat=37.3, lon=-121.9)
    assert "error" in out
    assert out["n_samples"] == 0


def test_infer_east_array_finds_negative_azimuth():
    lat, lon = 37.3, -121.9
    true_tilt, true_az = 20, -90
    energy = []
    weather = []
    # Three clear March days, UTC hours that are daylight in California.
    for day in range(3, 6):
        for hour in range(13, 24):
            ts = int(datetime(2024, 3, day, hour, 0, tzinfo=timezone.utc).timestamp())
            zen, _az = sa.solar_zenith_azimuth(lat, lon, ts)
            if zen > 80:
                continue
            ghi = max(0.0, 900.0 * math.cos(math.radians(zen)))
            if ghi < 80:
                continue
            poa = sa.poa_from_ghi(ghi, lat, lon, ts, true_tilt, true_az)
            solar_w = 2.0 * poa
            if solar_w < 50:
                continue
            energy.append({"ts": ts, "solar_w": solar_w})
            weather.append({"ts": ts, "ghi_w_m2": ghi, "cloud_cover_pct": 10})
    assert len(energy) >= 12, f"only {len(energy)} pairs"
    out = sa.infer_plane(energy, weather, lat, lon)
    assert "error" not in out, out
    assert out["azimuth"] == true_az, out
    assert abs(out["declination"] - true_tilt) <= 10, out
    assert 1.0 <= out["kwp"] <= 8.0, out
    assert out["n_samples"] >= 12


def test_get_set_isolated_per_device(tmp_path, monkeypatch):
    monkeypatch.setattr(sa, "PATH", str(tmp_path / "solar_array.json"))
    assert sa.get("A") is None
    sa.set("A", 20, 0, 2.4)
    sa.set("B", 30, -90, 1.2)
    assert sa.get("A")["kwp"] == 2.4
    assert sa.get("A")["declination"] == 20
    assert sa.get("B")["azimuth"] == -90
    assert sa.get("C") is None


def test_legacy_flat_file_is_default_until_device_saved(tmp_path, monkeypatch):
    path = tmp_path / "solar_array.json"
    path.write_text('{"declination": 20, "azimuth": 0, "kwp": 2.4}')
    monkeypatch.setattr(sa, "PATH", str(path))
    assert sa.get("ANY")["kwp"] == 2.4
    sa.set("SN1", 10, 90, 5.0)
    assert sa.get("SN1")["kwp"] == 5.0
    assert sa.get("SN2")["kwp"] == 2.4
    assert sa.get("SN2")["declination"] == 20
