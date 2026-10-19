"""Conversational AI Assistant — tools, multimodal messages, provider calls."""
from __future__ import annotations

import base64
import json
import logging
import os
from collections.abc import Awaitable, Callable
from typing import Any

import anthropic_creds
import anthropic_prefs
import assistant_store as store
import openai_creds

log = logging.getLogger("assistant_agent")

MAX_FILE_BYTES = 10 * 1024 * 1024
MAX_ATTACHMENTS = 5
ALLOWED_MIME = {
    "image/jpeg", "image/png", "image/gif", "image/webp",
    "application/pdf", "text/plain", "text/markdown", "text/csv",
}

WRITE_TOOLS = {
    "schedule_upsert", "schedule_delete",
    "smart_charge_set", "solar_charge_set", "set_output",
    "apply_suggestion",
}
READ_TOOLS = {
    "get_tab_snapshot", "get_forecast", "get_load_schedule",
    "get_energy_totals", "list_suggestions", "get_telemetry",
    "get_charge_status",
}

ToolHandler = Callable[[str, dict[str, Any]], Awaitable[dict[str, Any]]]


def _resolve_key(provider: str | None = None) -> str | None:
    provider = provider or anthropic_prefs.get_provider()
    if provider == "openai":
        return openai_creds.load() or os.environ.get("OPENAI_API_KEY") or None
    return anthropic_creds.load() or os.environ.get("ANTHROPIC_API_KEY") or None


def system_prompt(tab: str | None, context: dict | None) -> str:
    tab = tab or "live"
    base = (
        "You are the Solar Pow Monitor AI Assistant. Help the user understand "
        "energy usage, forecast SOC, and plan loads to maximize solar.\n"
        "Be concise and concrete. When proposing writes, explain why.\n"
        f"The user is currently on the '{tab}' tab.\n"
    )
    if tab == "forecast" or tab == "/forecast":
        base += (
            "Focus on the calendar load schedule vs solar forecast: suggest "
            "shifting heavy one-off or weekly events into high-GHI hours, and "
            "flag depletion risk. Use schedule_upsert/schedule_delete tools.\n"
        )
    if context:
        base += "\nTab context JSON:\n" + json.dumps(context, default=str)[:12000]
    return base


def tool_definitions() -> list[dict[str, Any]]:
    return [
        {
            "name": "get_tab_snapshot",
            "description": "Read-only snapshot of current device telemetry / KPIs.",
            "input_schema": {"type": "object", "properties": {}, "additionalProperties": False},
        },
        {
            "name": "get_forecast",
            "description": "Read-only SOC/solar/load forecast summary for the active device.",
            "input_schema": {"type": "object", "properties": {
                "device_sn": {"type": "string"},
            }},
        },
        {
            "name": "get_load_schedule",
            "description": "Read the calendar/historical load schedule for a device.",
            "input_schema": {"type": "object", "properties": {
                "device_sn": {"type": "string"},
            }},
        },
        {
            "name": "get_energy_totals",
            "description": "Today / 7d / 30d / lifetime energy totals.",
            "input_schema": {"type": "object", "properties": {
                "device_sn": {"type": "string"},
            }},
        },
        {
            "name": "list_suggestions",
            "description": "List pending algorithm advisor suggestions (actionable items).",
            "input_schema": {"type": "object", "properties": {
                "device_sn": {"type": "string"},
            }},
        },
        {
            "name": "get_telemetry",
            "description": "Latest telemetry for the viewed device.",
            "input_schema": {"type": "object", "properties": {}},
        },
        {
            "name": "get_charge_status",
            "description": "Smart-charge and solar-charge config/status.",
            "input_schema": {"type": "object", "properties": {
                "device_sn": {"type": "string"},
            }},
        },
        {
            "name": "schedule_upsert",
            "description": "Create or update a calendar load event (once or weekly).",
            "input_schema": {
                "type": "object",
                "properties": {
                    "device_sn": {"type": "string"},
                    "id": {"type": "string"},
                    "label": {"type": "string"},
                    "start": {"type": "string"},
                    "end": {"type": "string"},
                    "watts": {"type": "number"},
                    "kind": {"type": "string", "enum": ["once", "weekly"]},
                    "date": {"type": "string"},
                    "weekdays": {"type": "array", "items": {"type": "integer"}},
                },
                "required": ["start", "end", "watts", "kind"],
            },
        },
        {
            "name": "schedule_delete",
            "description": "Delete a calendar load event by id.",
            "input_schema": {
                "type": "object",
                "properties": {
                    "device_sn": {"type": "string"},
                    "id": {"type": "string"},
                },
                "required": ["id"],
            },
        },
        {
            "name": "smart_charge_set",
            "description": "Update smart-charge config (enabled, max_charge_w, target_sunrise_soc_pct, max_on_duration_minutes).",
            "input_schema": {
                "type": "object",
                "properties": {
                    "device_sn": {"type": "string"},
                    "enabled": {"type": "boolean"},
                    "max_charge_w": {"type": "number"},
                    "target_sunrise_soc_pct": {"type": "number"},
                    "max_on_duration_minutes": {"type": "number"},
                },
            },
        },
        {
            "name": "solar_charge_set",
            "description": "Update solar-charge config (enabled and related fields).",
            "input_schema": {
                "type": "object",
                "properties": {
                    "device_sn": {"type": "string"},
                    "enabled": {"type": "boolean"},
                },
            },
        },
        {
            "name": "set_output",
            "description": "Toggle an output port (ac, dc, usb, car).",
            "input_schema": {
                "type": "object",
                "properties": {
                    "device_sn": {"type": "string"},
                    "port": {"type": "string", "enum": ["ac", "dc", "usb", "car"]},
                    "on": {"type": "boolean"},
                },
                "required": ["port", "on"],
            },
        },
        {
            "name": "apply_suggestion",
            "description": "Apply a pending algorithm suggestion by id.",
            "input_schema": {
                "type": "object",
                "properties": {"id": {"type": "integer"}},
                "required": ["id"],
            },
        },
    ]


def _attachment_blocks(att_ids: list[str]) -> tuple[list[dict], list[str]]:
    """Build multimodal content blocks + plain text excerpts."""
    blocks: list[dict] = []
    notes: list[str] = []
    for aid in (att_ids or [])[:MAX_ATTACHMENTS]:
        meta = store.get_attachment(aid)
        if not meta:
            continue
        path = meta["path"]
        mime = meta["mime"]
        try:
            data = open(path, "rb").read()
        except OSError:
            continue
        b64 = base64.standard_b64encode(data).decode("ascii")
        if mime.startswith("image/"):
            blocks.append({
                "type": "image",
                "mime": mime,
                "data": b64,
                "filename": meta["filename"],
            })
        elif mime.startswith("text/") or mime == "text/csv":
            try:
                text = data.decode("utf-8", errors="replace")[:20000]
            except Exception:
                text = ""
            notes.append(f"[file {meta['filename']}]\n{text}")
        elif mime == "application/pdf":
            # Best-effort: pass as document/base64 for Anthropic; text note otherwise
            blocks.append({
                "type": "document",
                "mime": mime,
                "data": b64,
                "filename": meta["filename"],
            })
            notes.append(f"[PDF attached: {meta['filename']}]")
    return blocks, notes


async def run_chat_turn(
    *,
    conversation_id: str,
    user_text: str,
    attachment_ids: list[str] | None,
    tab: str | None,
    context: dict | None,
    autopilot: bool,
    tool_handler: ToolHandler,
    model: str | None = None,
) -> dict[str, Any]:
    """Run one assistant turn; write tools become pending unless autopilot."""
    provider = anthropic_prefs.get_provider()
    model = model or anthropic_prefs.get_model(
        "advisor", provider=provider if provider == "openai" else "anthropic",
    )
    att_ids = list(attachment_ids or [])
    blocks, notes = _attachment_blocks(att_ids)
    content = (user_text or "").strip()
    if notes:
        content = (content + "\n\n" + "\n\n".join(notes)).strip()

    user_msg = store.add_message(
        conversation_id, "user", content,
        parts={"attachment_ids": att_ids} if att_ids else None,
    )
    if att_ids:
        store.bind_attachments(att_ids, user_msg["id"])

    # Auto-title from first user message
    conv = store.get_conversation(conversation_id)
    if conv and conv.get("title") in (None, "", "New chat") and content:
        store.touch_conversation(conversation_id, title=content[:60])

    history = store.list_messages(conversation_id)
    pending_writes: list[dict] = []
    assistant_text = ""

    try:
        if provider == "openai":
            assistant_text, pending_writes = await _turn_openai(
                history=history, blocks=blocks, system=system_prompt(tab, context),
                model=model, autopilot=autopilot, conversation_id=conversation_id,
                tool_handler=tool_handler, user_message_id=user_msg["id"],
            )
        else:
            assistant_text, pending_writes = await _turn_anthropic(
                history=history, blocks=blocks, system=system_prompt(tab, context),
                model=model, autopilot=autopilot, conversation_id=conversation_id,
                tool_handler=tool_handler, user_message_id=user_msg["id"],
            )
    except Exception as e:
        log.exception("assistant chat failed")
        assistant_text = f"Sorry — the assistant hit an error: {e}"

    asst = store.add_message(conversation_id, "assistant", assistant_text)
    store.touch_conversation(conversation_id, model=model)
    return {
        "conversation_id": conversation_id,
        "message": asst,
        "pending_tools": pending_writes,
    }


async def _turn_anthropic(
    *, history, blocks, system, model, autopilot, conversation_id,
    tool_handler, user_message_id,
) -> tuple[str, list[dict]]:
    api_key = _resolve_key("anthropic")
    if not api_key:
        return "No Anthropic API key configured. Add one in Settings → AI.", []
    from anthropic import AsyncAnthropic
    client = AsyncAnthropic(api_key=api_key)
    tools = [
        {"name": t["name"], "description": t["description"], "input_schema": t["input_schema"]}
        for t in tool_definitions()
    ]
    messages = _history_to_anthropic(history, blocks)
    pending: list[dict] = []
    final_text = ""
    for _ in range(6):
        resp = await client.messages.create(
            model=model,
            max_tokens=4096,
            system=system,
            tools=tools,
            messages=messages,
        )
        tool_uses = [b for b in resp.content if getattr(b, "type", None) == "tool_use"]
        texts = [b.text for b in resp.content if getattr(b, "type", None) == "text"]
        if texts:
            final_text = "\n".join(texts)
        if not tool_uses:
            break
        messages.append({"role": "assistant", "content": resp.content})
        results = []
        for tu in tool_uses:
            name = tu.name
            args = tu.input if isinstance(tu.input, dict) else {}
            if name in WRITE_TOOLS and not autopilot:
                run = store.create_tool_run(
                    conversation_id, name, args, message_id=user_message_id,
                )
                pending.append(run)
                results.append({
                    "type": "tool_result",
                    "tool_use_id": tu.id,
                    "content": json.dumps({
                        "status": "pending_approval",
                        "run_id": run["id"],
                        "message": "Waiting for user to Approve/Deny in the UI.",
                    }),
                })
            else:
                try:
                    out = await tool_handler(name, args)
                except Exception as e:
                    out = {"error": str(e)}
                if name in WRITE_TOOLS:
                    run = store.create_tool_run(
                        conversation_id, name, args, message_id=user_message_id,
                        status="applied",
                    )
                    store.finish_tool_run(run["id"], "applied", out)
                results.append({
                    "type": "tool_result",
                    "tool_use_id": tu.id,
                    "content": json.dumps(out, default=str)[:20000],
                })
        messages.append({"role": "user", "content": results})
        if pending and not autopilot:
            # Stop after proposing writes so the user can approve
            if not final_text:
                final_text = "I have actions ready — review and Approve or Deny below."
            break
    return final_text or "Done.", pending


def _history_to_anthropic(history: list[dict], blocks: list[dict]) -> list[dict]:
    msgs: list[dict] = []
    for i, m in enumerate(history):
        role = m.get("role")
        if role not in ("user", "assistant"):
            continue
        text = m.get("content") or ""
        is_last_user = role == "user" and i == len(history) - 1
        if is_last_user and blocks:
            content: list[dict] = []
            for b in blocks:
                if b["type"] == "image":
                    content.append({
                        "type": "image",
                        "source": {
                            "type": "base64",
                            "media_type": b["mime"],
                            "data": b["data"],
                        },
                    })
                elif b["type"] == "document":
                    content.append({
                        "type": "document",
                        "source": {
                            "type": "base64",
                            "media_type": b["mime"],
                            "data": b["data"],
                        },
                    })
            if text:
                content.append({"type": "text", "text": text})
            msgs.append({"role": "user", "content": content or text})
        else:
            msgs.append({"role": role, "content": text})
    return msgs


async def _turn_openai(
    *, history, blocks, system, model, autopilot, conversation_id,
    tool_handler, user_message_id,
) -> tuple[str, list[dict]]:
    api_key = _resolve_key("openai")
    if not api_key:
        return "No OpenAI API key configured. Add one in Settings → AI.", []
    import openai_compat
    client = openai_compat.make_async_client(api_key)
    tools = []
    for t in tool_definitions():
        tools.append({
            "type": "function",
            "function": {
                "name": t["name"],
                "description": t["description"],
                "parameters": t["input_schema"],
            },
        })
    messages = [{"role": "system", "content": system}]
    messages.extend(_history_to_openai(history, blocks))
    pending: list[dict] = []
    final_text = ""
    for _ in range(6):
        resp = await client.chat.completions.create(
            model=model,
            messages=messages,
            tools=tools,
        )
        choice = resp.choices[0]
        msg = choice.message
        if msg.content:
            final_text = msg.content
        tool_calls = msg.tool_calls or []
        if not tool_calls:
            break
        messages.append({
            "role": "assistant",
            "content": msg.content,
            "tool_calls": [
                {
                    "id": tc.id,
                    "type": "function",
                    "function": {
                        "name": tc.function.name,
                        "arguments": tc.function.arguments,
                    },
                }
                for tc in tool_calls
            ],
        })
        for tc in tool_calls:
            name = tc.function.name
            try:
                args = json.loads(tc.function.arguments or "{}")
            except Exception:
                args = {}
            if name in WRITE_TOOLS and not autopilot:
                run = store.create_tool_run(
                    conversation_id, name, args, message_id=user_message_id,
                )
                pending.append(run)
                out = {
                    "status": "pending_approval",
                    "run_id": run["id"],
                    "message": "Waiting for user to Approve/Deny in the UI.",
                }
            else:
                try:
                    out = await tool_handler(name, args)
                except Exception as e:
                    out = {"error": str(e)}
                if name in WRITE_TOOLS:
                    run = store.create_tool_run(
                        conversation_id, name, args, message_id=user_message_id,
                        status="applied",
                    )
                    store.finish_tool_run(run["id"], "applied", out)
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": json.dumps(out, default=str)[:20000],
            })
        if pending and not autopilot:
            if not final_text:
                final_text = "I have actions ready — review and Approve or Deny below."
            break
    return final_text or "Done.", pending


def _history_to_openai(history: list[dict], blocks: list[dict]) -> list[dict]:
    msgs: list[dict] = []
    for i, m in enumerate(history):
        role = m.get("role")
        if role not in ("user", "assistant"):
            continue
        text = m.get("content") or ""
        is_last_user = role == "user" and i == len(history) - 1
        if is_last_user and blocks:
            content: list[dict] = []
            if text:
                content.append({"type": "text", "text": text})
            for b in blocks:
                if b["type"] == "image":
                    content.append({
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:{b['mime']};base64,{b['data']}",
                        },
                    })
            msgs.append({"role": "user", "content": content or text})
        else:
            msgs.append({"role": role, "content": text})
    return msgs
