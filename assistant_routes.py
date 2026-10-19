"""FastAPI routes for the AI Assistant panel."""
from __future__ import annotations

import logging
from collections.abc import Awaitable, Callable
from typing import Any

from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import FileResponse

import assistant_agent as agent
import assistant_store as store
import load_schedule
import smart_charge
import solar_charge

log = logging.getLogger("assistant_routes")


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
    ):
        self.get_device_sn = get_device_sn
        self.get_status = get_status
        self.get_forecast = get_forecast
        self.get_energy_totals = get_energy_totals
        self.list_suggestions = list_suggestions
        self.apply_suggestion = apply_suggestion
        self.set_output = set_output


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
            # Trim points for context size
            pts = (fc.get("forecast") or [])[:48]
            return {
                "ready": fc.get("ready"),
                "solar_coefficient": fc.get("solar_coefficient"),
                "capacity_wh": fc.get("capacity_wh"),
                "load_mode": fc.get("load_mode"),
                "overall_load_w": fc.get("overall_load_w"),
                "forecast_sample": pts,
                "load_schedule": fc.get("load_schedule"),
            }
        return {"error": "no forecast"}

    if name == "get_load_schedule":
        if not sn:
            return {"error": "device_sn required"}
        return load_schedule.get(sn)

    if name == "get_energy_totals":
        return h.get_energy_totals(sn)

    if name == "list_suggestions":
        return {"suggestions": h.list_suggestions(sn)}

    if name == "get_charge_status":
        if not sn:
            return {"error": "device_sn required"}
        return {
            "smart_charge": smart_charge.get_config(sn),
            "solar_charge": solar_charge.get_config(sn),
        }

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
