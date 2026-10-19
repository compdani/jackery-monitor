"""Assistant get_forecast tool returns week rollups, not a 48h trim."""
from __future__ import annotations

import os

import pytest

os.environ.setdefault("BACKEND", "mock")


@pytest.mark.asyncio
async def test_get_forecast_tool_returns_daily_and_ev_divert(monkeypatch):
    import assistant_routes as ar
    import solar_charge

    hours = []
    base = 1_700_000_000
    for i in range(72):
        hours.append({
            "ts": base + (i + 1) * 3600,
            "duration_h": 1.0,
            "solar_w": 2000 if 8 <= (i % 24) <= 16 else 0,
            "load_w": 100,
            "predicted_soc": 70,
        })

    async def fake_fc(_sn):
        return {
            "ready": True,
            "solar_coefficient": 1.5,
            "solar_calibration": {
                "actual_over_predicted": 0.95,
                "n": 12,
                "pool": "headroom",
                "suggested_k": 1.425,
            },
            "charge_ceiling_pct": 81.0,
            "capacity_wh": 30000,
            "load_mode": "scheduled",
            "overall_load_w": 100,
            "starting_soc_pct": 70,
            "low_battery_threshold": 20,
            "forecast": hours,
            "daily": None,  # force recompute path
            "load_schedule": {"mode": "scheduled", "windows": []},
        }

    helpers = ar.AssistantHelpers(
        get_device_sn=lambda: "SN-X",
        get_status=lambda: {},
        get_forecast=fake_fc,
        get_energy_totals=lambda _sn: {},
        list_suggestions=lambda _sn: [],
        apply_suggestion=lambda _id: None,
        set_output=lambda *_a, **_k: None,
    )
    monkeypatch.setattr(ar, "_helpers", helpers)
    monkeypatch.setattr(
        solar_charge, "get_config",
        lambda _sn: {"car_load_w": 1751},
    )

    out = await ar._handle_tool("get_forecast", {})
    assert out["ready"] is True
    assert out["charge_ceiling_pct"] == 81.0
    assert out["solar_calibration"]["suggested_k"] == 1.425
    assert out["daily"]
    assert len(out["daily"]) >= 2
    assert "ev_divert" in out
    assert out["ev_divert"]["car_load_w"] == 1751
    # Thinned sample, not a hard 48-hour cut of dense hours only.
    assert len(out["forecast_sample"]) < len(hours)
    assert len(out["forecast_sample"]) >= 24
