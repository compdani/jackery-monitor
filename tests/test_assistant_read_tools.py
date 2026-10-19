"""Smoke tests for assistant broad READ tools + secret redaction."""
from __future__ import annotations

import os

import pytest

os.environ.setdefault("BACKEND", "mock")

_SECRETISH = (
    "password", "passwd", "secret", "token", "api_key", "apikey",
    "credential", "authorization", "cookie",
)


def _assert_no_secret_fields(obj, path=""):
    if isinstance(obj, dict):
        for k, v in obj.items():
            ks = str(k).lower()
            if any(s in ks for s in _SECRETISH) and not ks.startswith("has_"):
                # Redaction may leave the key with "[redacted]" — that's ok.
                assert v == "[redacted]" or isinstance(v, bool), (
                    f"leaked secret field at {path}.{k}: {v!r}")
            _assert_no_secret_fields(v, f"{path}.{k}")
    elif isinstance(obj, list):
        for i, x in enumerate(obj[:50]):
            _assert_no_secret_fields(x, f"{path}[{i}]")


@pytest.fixture()
def helpers(monkeypatch):
    import assistant_routes as ar

    async def fake_fc(_sn):
        return {"ready": False, "forecast": []}

    async def fake_params(_sn):
        return {
            "device_sn": "SN-X",
            "params": [{"key": "solar_coefficient", "value": 1.2,
                        "source": "fit"}],
            "tz_offset_seconds": -25200,
        }

    async def fake_fit(sn, *, key, lookback_hours=None, apply=False):
        return {
            "device_sn": sn or "SN-X",
            "key": key,
            "lookback_hours": lookback_hours or 336,
            "applied": apply,
            "value": 1.5,
            "n_samples": 10,
            "source": "user" if apply else None,
        }

    async def fake_events(limit, since):
        return {
            "events": [
                {"ts": 1, "kind": "poll", "password": "should-redact",
                 "msg": "ok"},
            ],
        }

    h = ar.AssistantHelpers(
        get_device_sn=lambda: "SN-X",
        get_status=lambda: {
            "telemetry": {"soc": 50},
            "connection_status": "ok",
            "battery_percent": 50,
            "has_credentials": True,
            "api_token": "LEAK",
        },
        get_forecast=fake_fc,
        get_energy_totals=lambda _sn: {"today_wh": 1},
        list_suggestions=lambda _sn: [],
        apply_suggestion=lambda _id: None,
        set_output=lambda *_a, **_k: None,
        run_query=lambda sn, name, args: {
            "rows": [{"ts": "2026-01-01T00:00:00", "soc": 50}],
            "row_count": 1,
            "password": "nope",
        },
        get_device_params=fake_params,
        fit_device_param=fake_fit,
        get_energy_daily=lambda sn, days: {
            "device_sn": sn, "days": days, "daily": []},
        get_daily_summary=lambda sn, days: {
            "device_sn": sn, "days": days, "rows": []},
        get_charge_status_full=lambda sn: {
            "device_sn": sn,
            "smart_charge": {"config": {"enabled": True}, "history": []},
            "solar_charge": {"config": {}, "recent_decisions": []},
        },
        get_load_schedule_full=lambda sn: {
            "device_sn": sn, "mode": "historical",
            "windows": [], "learned_profile": [],
        },
        get_cost_plan=lambda: {"type": "flat", "rate": 0.15},
        get_location=lambda: {
            "latitude": 37.0, "longitude": -122.0,
            "label": "Test", "tz_offset_seconds": -28800,
        },
        get_solar_array=lambda sn: {"device_sn": sn, "array": None},
        list_automation_rules=lambda: [{"id": "r1", "enabled": True}],
        get_events=fake_events,
    )
    monkeypatch.setattr(ar, "_helpers", h)
    return ar


@pytest.mark.asyncio
@pytest.mark.parametrize("tool,args", [
    ("query_samples", {
        "start_iso": "2026-01-01T00:00:00",
        "end_iso": "2026-01-02T00:00:00",
        "bucket_s": 3600,
    }),
    ("query_weather", {
        "start_iso": "2026-01-01T00:00:00",
        "end_iso": "2026-01-02T00:00:00",
    }),
    ("query_predictions", {
        "start_iso": "2026-01-01T00:00:00",
        "end_iso": "2026-01-02T00:00:00",
    }),
    ("query_decisions", {
        "start_iso": "2026-01-01T00:00:00",
        "end_iso": "2026-01-02T00:00:00",
    }),
    ("query_battery_packs", {}),
    ("get_device_params", {}),
    ("get_energy_daily", {"days": 7}),
    ("get_daily_summary", {"days": 7}),
    ("get_charge_status", {}),
    ("get_load_schedule", {}),
    ("get_cost_plan", {}),
    ("get_location", {}),
    ("get_solar_array", {}),
    ("list_automation_rules", {}),
    ("get_events", {"limit": 10}),
    ("get_telemetry", {}),
])
async def test_read_tools_return_dict_without_secrets(helpers, tool, args):
    out = await helpers._handle_tool(tool, args)
    assert isinstance(out, dict), f"{tool} returned {type(out)}"
    assert out.get("error") in (None, False) or "error" not in out, out
    _assert_no_secret_fields(out)


@pytest.mark.asyncio
async def test_redact_secrets_on_events(helpers):
    out = await helpers._handle_tool("get_events", {"limit": 5})
    assert out["events"][0]["password"] == "[redacted]"
    assert out["events"][0]["msg"] == "ok"


@pytest.mark.asyncio
async def test_telemetry_redacts_api_token(helpers):
    out = await helpers._handle_tool("get_telemetry", {})
    # Status blob may include api_token — redaction strips values.
    # Our handler only returns a subset; ensure no raw token leaked.
    blob = str(out)
    assert "LEAK" not in blob


@pytest.mark.asyncio
async def test_fit_device_param_write_tool(helpers):
    import assistant_agent as agent
    assert "fit_device_param" in agent.WRITE_TOOLS
    out = await helpers._handle_tool("fit_device_param", {
        "key": "solar_coefficient",
        "lookback_hours": 24,
        "apply": True,
    })
    assert out["applied"] is True
    assert out["key"] == "solar_coefficient"
    assert out["lookback_hours"] == 24


@pytest.mark.asyncio
async def test_tool_definitions_include_new_reads():
    import assistant_agent as agent
    names = {t["name"] for t in agent.tool_definitions()}
    for expected in (
        "query_samples", "query_weather", "query_predictions",
        "query_decisions", "query_battery_packs", "get_device_params",
        "get_energy_daily", "get_daily_summary", "get_cost_plan",
        "get_location", "get_solar_array", "list_automation_rules",
        "get_events", "fit_device_param",
    ):
        assert expected in names
        assert expected in agent.READ_TOOLS or expected in agent.WRITE_TOOLS
