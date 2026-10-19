"""FastAPI routes for the AI Assistant panel."""
from __future__ import annotations

import logging
import re
from collections.abc import Awaitable, Callable
from typing import Any

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import FileResponse

import assistant_agent as agent
import assistant_store as store
import forecaster
import load_schedule
import smart_charge
import solar_charge

log = logging.getLogger("assistant_routes")

# Keys / substrings that must never leave assistant tool results.
_SECRET_KEY_RE = re.compile(
    r"(password|passwd|secret|token|api[_-]?key|credential|authorization|"
    r"cookie|session|smb|ssh)",
    re.IGNORECASE,
)


def _redact_secrets(obj: Any, *, depth: int = 0) -> Any:
    """Drop or mask credential-like fields recursively."""
    if depth > 8:
        return None
    if isinstance(obj, dict):
        out: dict[str, Any] = {}
        for k, v in obj.items():
            ks = str(k)
            if _SECRET_KEY_RE.search(ks):
                # Booleans like has_credentials / has_key are safe.
                if isinstance(v, bool) or ks.lower().startswith("has_"):
                    out[ks] = v
                else:
                    out[ks] = "[redacted]"
                continue
            out[ks] = _redact_secrets(v, depth=depth + 1)
        return out
    if isinstance(obj, list):
        return [_redact_secrets(x, depth=depth + 1) for x in obj[:500]]
    return obj


class AssistantHelpers:
    def __init__(
        self,
        *,
        get_device_sn: Callable[[], str | None],
        get_status: Callable[[], dict | None],
        get_forecast: Callable[[str | None], Awaitable[Any]],
        get_energy_totals: Callable[[str | None], dict],
        list_suggestions: Callable[[str | None], list],
        apply_suggestion: Callable[[int], Awaitable[dict]],
        set_output: Callable[[str | None, str, bool], Awaitable[Any]],
        run_query: Callable[[str | None, str, dict], Any] | None = None,
        get_device_params: Callable[[str | None], Awaitable[dict]] | None = None,
        fit_device_param: Callable[..., Awaitable[dict]] | None = None,
        get_energy_daily: Callable[[str | None, int], dict] | None = None,
        get_daily_summary: Callable[[str | None, int], dict] | None = None,
        get_charge_status_full: Callable[[str | None], dict] | None = None,
        get_load_schedule_full: Callable[[str | None], dict] | None = None,
        get_cost_plan: Callable[[], dict] | None = None,
        get_location: Callable[[], dict] | None = None,
        get_solar_array: Callable[[str | None], dict] | None = None,
        list_automation_rules: Callable[[], list] | None = None,
        get_events: Callable[[int, float], Awaitable[dict]] | None = None,
    ):
        self.get_device_sn = get_device_sn
        self.get_status = get_status
        self.get_forecast = get_forecast
        self.get_energy_totals = get_energy_totals
        self.list_suggestions = list_suggestions
        self.apply_suggestion = apply_suggestion
        self.set_output = set_output
        self.run_query = run_query
        self.get_device_params = get_device_params
        self.fit_device_param = fit_device_param
        self.get_energy_daily = get_energy_daily
        self.get_daily_summary = get_daily_summary
        self.get_charge_status_full = get_charge_status_full
        self.get_load_schedule_full = get_load_schedule_full
        self.get_cost_plan = get_cost_plan
        self.get_location = get_location
        self.get_solar_array = get_solar_array
        self.list_automation_rules = list_automation_rules
        self.get_events = get_events


_helpers: AssistantHelpers | None = None


def install(app: FastAPI, helpers: AssistantHelpers) -> None:
    global _helpers
    _helpers = helpers

    @app.get("/api/assistant/conversations")
    def api_list_conversations():
        return {"conversations": store.list_conversations()}

    @app.post("/api/assistant/conversations")
    async def api_create_conversation(body: dict | None = None):
        body = body or {}
        return store.create_conversation(
            title=str(body.get("title") or "New chat"),
            model=body.get("model"),
            provider=body.get("provider"),
        )

    @app.get("/api/assistant/conversations/{conv_id}")
    def api_get_conversation(conv_id: str):
        conv = store.get_conversation(conv_id)
        if not conv:
            raise HTTPException(404, "conversation not found")
        return conv

    @app.delete("/api/assistant/conversations/{conv_id}")
    def api_delete_conversation(conv_id: str):
        if not store.delete_conversation(conv_id):
            raise HTTPException(404, "conversation not found")
        return {"ok": True}

    @app.post("/api/assistant/attachments")
    async def api_upload_attachment(
        conversation_id: str = Form(...),
        file: UploadFile = File(...),
    ):
        if not store.get_conversation(conversation_id):
            raise HTTPException(404, "conversation not found")
        data = await file.read()
        if len(data) > agent.MAX_FILE_BYTES:
            raise HTTPException(400, f"file too large (max {agent.MAX_FILE_BYTES} bytes)")
        mime = (file.content_type or "application/octet-stream").split(";")[0].strip()
        if mime not in agent.ALLOWED_MIME:
            raise HTTPException(400, f"unsupported type: {mime}")
        return store.save_attachment(
            conversation_id,
            filename=file.filename or "upload",
            mime=mime,
            data=data,
        )

    @app.get("/api/assistant/attachments/{att_id}")
    def api_get_attachment(att_id: str):
        meta = store.get_attachment(att_id)
        if not meta:
            raise HTTPException(404, "not found")
        return FileResponse(
            meta["path"],
            media_type=meta["mime"],
            filename=meta["filename"],
        )

    @app.delete("/api/assistant/attachments/{att_id}")
    def api_delete_attachment(att_id: str):
        if not store.delete_attachment(att_id):
            raise HTTPException(404, "not found")
        return {"ok": True}

    @app.post("/api/assistant/chat")
    async def api_chat(body: dict):
        conv_id = body.get("conversation_id")
        if not conv_id or not store.get_conversation(conv_id):
            raise HTTPException(400, "conversation_id required")
        text = body.get("message") or body.get("text") or ""
        att_ids = body.get("attachment_ids") or []
        if not str(text).strip() and not att_ids:
            raise HTTPException(400, "message or attachments required")
        result = await agent.run_chat_turn(
            conversation_id=conv_id,
            user_text=str(text),
            attachment_ids=list(att_ids),
            tab=body.get("tab"),
            context=body.get("context"),
            autopilot=bool(body.get("autopilot")),
            tool_handler=_handle_tool,
            model=body.get("model"),
        )
        return result

    @app.post("/api/assistant/tools/{run_id}/approve")
    async def api_approve_tool(run_id: str):
        run = store.get_tool_run(run_id)
        if not run:
            raise HTTPException(404, "tool run not found")
        if run["status"] != "pending":
            raise HTTPException(400, f"run is {run['status']}")
        try:
            out = await _handle_tool(run["tool_name"], run["args"])
        except Exception as e:
            store.finish_tool_run(run_id, "error", {"error": str(e)})
            raise HTTPException(400, str(e)) from e
        store.finish_tool_run(run_id, "applied", out)
        return {"ok": True, "run_id": run_id, "result": out}

    @app.post("/api/assistant/tools/{run_id}/deny")
    def api_deny_tool(run_id: str):
        run = store.get_tool_run(run_id)
        if not run:
            raise HTTPException(404, "tool run not found")
        if run["status"] != "pending":
            raise HTTPException(400, f"run is {run['status']}")
        store.finish_tool_run(run_id, "denied", {"denied": True})
        return {"ok": True}


async def _handle_tool(name: str, args: dict[str, Any]) -> dict[str, Any]:
    if not _helpers:
        raise RuntimeError("assistant not installed")
    h = _helpers
    sn = args.get("device_sn") or h.get_device_sn()
    result = await _dispatch_tool(name, args, h, sn)
    return _redact_secrets(result) if isinstance(result, (dict, list)) else result


async def _dispatch_tool(
        name: str, args: dict[str, Any], h: AssistantHelpers,
        sn: str | None) -> dict[str, Any]:
    if name == "get_tab_snapshot" or name == "get_telemetry":
        st = h.get_status() or {}
        return {
            "device_sn": sn,
            "telemetry": st.get("telemetry") or {},
            "connection_status": st.get("connection_status"),
            "battery_percent": st.get("battery_percent"),
        }

    if name == "get_forecast":
        fc = await h.get_forecast(sn)
        if isinstance(fc, dict):
            hours = list(fc.get("forecast") or [])
            # Prefer backend daily rollups; recompute if an older payload
            # lacks them so week-ahead EV advice still works.
            daily = fc.get("daily")
            if not daily and hours:
                daily = forecaster.summarize_forecast_days(
                    hours,
                    starting_soc_pct=fc.get("starting_soc_pct"),
                )
            car_w = None
            try:
                car_w = float(
                    (solar_charge.get_config(sn) or {}).get("car_load_w") or 0)
            except (TypeError, ValueError):
                car_w = 0.0
            floor = 20.0
            try:
                thr = fc.get("low_battery_threshold")
                if thr is not None:
                    floor = float(thr)
            except (TypeError, ValueError):
                pass
            ev = forecaster.summarize_ev_divert(
                hours, car_w or 0.0, comfort_floor_pct=floor)
            # Thin hourly sample (~every 3h) for nuance without blowing
            # the context window; prefer daily + ev_divert for planning.
            sample = hours[::3] if hours else []
            if hours and hours[-1] not in sample:
                sample = list(sample) + [hours[-1]]
            return {
                "ready": fc.get("ready"),
                "solar_coefficient": fc.get("solar_coefficient"),
                "solar_calibration": fc.get("solar_calibration"),
                "charge_ceiling_pct": fc.get("charge_ceiling_pct"),
                "capacity_wh": fc.get("capacity_wh"),
                "load_mode": fc.get("load_mode"),
                "overall_load_w": fc.get("overall_load_w"),
                "starting_soc_pct": fc.get("starting_soc_pct"),
                "daily": daily,
                "ev_divert": ev,
                "forecast_sample": sample,
                "load_schedule": fc.get("load_schedule"),
            }
        return {"error": "no forecast"}

    if name == "get_load_schedule":
        if not sn:
            return {"error": "device_sn required"}
        if h.get_load_schedule_full:
            return h.get_load_schedule_full(sn)
        return load_schedule.get(sn)

    if name == "get_energy_totals":
        return h.get_energy_totals(sn)

    if name == "list_suggestions":
        return {"suggestions": h.list_suggestions(sn)}

    if name == "get_charge_status":
        if not sn:
            return {"error": "device_sn required"}
        if h.get_charge_status_full:
            return h.get_charge_status_full(sn)
        return {
            "smart_charge": smart_charge.get_config(sn),
            "solar_charge": solar_charge.get_config(sn),
        }

    if name in (
        "query_samples", "query_weather", "query_predictions",
        "query_decisions", "query_battery_packs",
    ):
        if not h.run_query:
            return {"error": "query tools not available"}
        return h.run_query(sn, name, args) or {"error": "query failed"}

    if name == "get_device_params":
        if not h.get_device_params:
            return {"error": "get_device_params not available"}
        return await h.get_device_params(sn)

    if name == "get_energy_daily":
        if not h.get_energy_daily:
            return {"error": "get_energy_daily not available"}
        days = int(args.get("days") or 90)
        return h.get_energy_daily(sn, days)

    if name == "get_daily_summary":
        if not h.get_daily_summary:
            return {"error": "get_daily_summary not available"}
        days = int(args.get("days") or 7)
        return h.get_daily_summary(sn, days)

    if name == "get_cost_plan":
        if not h.get_cost_plan:
            return {"error": "get_cost_plan not available"}
        return h.get_cost_plan()

    if name == "get_location":
        if not h.get_location:
            return {"error": "get_location not available"}
        return h.get_location()

    if name == "get_solar_array":
        if not h.get_solar_array:
            return {"error": "get_solar_array not available"}
        return h.get_solar_array(sn)

    if name == "list_automation_rules":
        if not h.list_automation_rules:
            return {"error": "list_automation_rules not available"}
        return {"rules": h.list_automation_rules()}

    if name == "get_events":
        if not h.get_events:
            return {"error": "get_events not available"}
        limit = int(args.get("limit") or 100)
        since = float(args.get("since") or 0.0)
        return await h.get_events(limit, since)

    if name == "fit_device_param":
        if not h.fit_device_param:
            return {"error": "fit_device_param not available"}
        key = args.get("key")
        if not key:
            return {"error": "key required"}
        return await h.fit_device_param(
            sn,
            key=str(key),
            lookback_hours=args.get("lookback_hours"),
            apply=bool(args.get("apply")),
        )

    if name == "schedule_upsert":
        if not sn:
            raise ValueError("device_sn required")
        rec = load_schedule.get(sn)
        windows = list(rec.get("windows") or [])
        wid = args.get("id")
        event = {
            "id": wid,
            "label": args.get("label") or "",
            "start": args.get("start"),
            "end": args.get("end"),
            "watts": args.get("watts"),
            "kind": args.get("kind") or "once",
            "date": args.get("date"),
            "weekdays": args.get("weekdays"),
        }
        replaced = False
        if wid:
            for i, w in enumerate(windows):
                if w.get("id") == wid:
                    windows[i] = {**w, **{k: v for k, v in event.items() if v is not None}}
                    replaced = True
                    break
        if not replaced:
            windows.append(event)
        saved = load_schedule.set(sn, {
            **rec,
            "mode": "scheduled",
            "windows": windows,
        })
        return {"ok": True, "schedule": saved}

    if name == "schedule_delete":
        if not sn:
            raise ValueError("device_sn required")
        wid = args.get("id")
        if not wid:
            raise ValueError("id required")
        rec = load_schedule.get(sn)
        windows = [w for w in (rec.get("windows") or []) if w.get("id") != wid]
        saved = load_schedule.set(sn, {**rec, "windows": windows})
        return {"ok": True, "schedule": saved}

    if name == "smart_charge_set":
        if not sn:
            raise ValueError("device_sn required")
        cfg = smart_charge.get_config(sn)
        for key in ("enabled", "max_charge_w", "target_sunrise_soc_pct",
                    "max_on_duration_minutes"):
            if key in args and args[key] is not None:
                cfg[key] = args[key]
        smart_charge.set_config(cfg, device_sn=sn)
        return {"ok": True, "config": smart_charge.get_config(sn)}

    if name == "solar_charge_set":
        if not sn:
            raise ValueError("device_sn required")
        patch: dict[str, Any] = {}
        if "enabled" in args and args["enabled"] is not None:
            patch["enabled"] = bool(args["enabled"])
        for k, v in args.items():
            if k in ("device_sn", "enabled"):
                continue
            if v is not None:
                patch[k] = v
        solar_charge.set_config(patch, device_sn=sn)
        return {"ok": True, "config": solar_charge.get_config(sn)}

    if name == "set_output":
        port = args.get("port")
        on = bool(args.get("on"))
        if port not in ("ac", "dc", "usb", "car"):
            raise ValueError("invalid port")
        await h.set_output(sn, port, on)
        return {"ok": True, "port": port, "on": on}

    if name == "apply_suggestion":
        sid = int(args.get("id"))
        return await h.apply_suggestion(sid)

    return {"error": f"unknown tool {name}"}
