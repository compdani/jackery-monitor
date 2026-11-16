"""SQLite store for AI Assistant conversations, messages, attachments, tool runs."""
from __future__ import annotations

import json
import logging
import os
import sqlite3
import threading
import time
import uuid
from contextlib import contextmanager
from pathlib import Path
from typing import Any

log = logging.getLogger("assistant_store")

PATH = os.environ.get("JACKERY_ASSISTANT_DB", "/data/assistant_chat.db")
UPLOAD_ROOT = os.environ.get("JACKERY_ASSISTANT_UPLOADS", "/data/assistant_uploads")

SCHEMA = """
CREATE TABLE IF NOT EXISTS assistant_conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT 'New chat',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    model TEXT,
    provider TEXT
);
CREATE TABLE IF NOT EXISTS assistant_messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    parts_json TEXT,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES assistant_conversations(id)
);
CREATE INDEX IF NOT EXISTS idx_asst_msg_conv ON assistant_messages(conversation_id, created_at);
CREATE TABLE IF NOT EXISTS assistant_attachments (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    message_id TEXT,
    filename TEXT NOT NULL,
    mime TEXT NOT NULL,
    size INTEGER NOT NULL,
    path TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_asst_att_conv ON assistant_attachments(conversation_id);
CREATE TABLE IF NOT EXISTS assistant_tool_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    message_id TEXT,
    tool_name TEXT NOT NULL,
    args_json TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT,
    created_at INTEGER NOT NULL,
    decided_at INTEGER
);
"""

_lock = threading.Lock()
_initialized = False


def _ensure() -> None:
    global _initialized
    if _initialized:
        return
    Path(PATH).parent.mkdir(parents=True, exist_ok=True)
    Path(UPLOAD_ROOT).mkdir(parents=True, exist_ok=True)
    with _conn() as c:
        c.executescript(SCHEMA)
    _initialized = True


@contextmanager
def _conn():
    with _lock:
        con = sqlite3.connect(PATH, timeout=5.0)
        con.row_factory = sqlite3.Row
        try:
            con.execute("PRAGMA journal_mode=WAL")
            yield con
            con.commit()
        finally:
            con.close()


def _id() -> str:
    return uuid.uuid4().hex[:16]


def list_conversations(limit: int = 50) -> list[dict[str, Any]]:
    _ensure()
    with _conn() as c:
        rows = c.execute(
            """SELECT id, title, created_at, updated_at, model, provider
               FROM assistant_conversations
               ORDER BY updated_at DESC LIMIT ?""",
            (limit,),
        ).fetchall()
    out = []
    for r in rows:
        d = dict(r)
        snip = _snippet(d["id"])
        d["snippet"] = snip
        out.append(d)
    return out


def _snippet(conv_id: str) -> str:
    with _conn() as c:
        row = c.execute(
            """SELECT content, role FROM assistant_messages
               WHERE conversation_id = ?
               ORDER BY created_at DESC LIMIT 8""",
            (conv_id,),
        ).fetchall()
    for r in row:
        if r["role"] == "assistant" and (r["content"] or "").strip():
            t = r["content"].strip().replace("\n", " ")
            return t[:80] + ("…" if len(t) > 80 else "")
    for r in row:
        if (r["content"] or "").strip():
            t = r["content"].strip().replace("\n", " ")
            return t[:80] + ("…" if len(t) > 80 else "")
    return ""


def create_conversation(*, title: str = "New chat",
                        model: str | None = None,
                        provider: str | None = None) -> dict[str, Any]:
    _ensure()
    now = int(time.time())
    cid = _id()
    with _conn() as c:
        c.execute(
            """INSERT INTO assistant_conversations
               (id, title, created_at, updated_at, model, provider)
               VALUES (?, ?, ?, ?, ?, ?)""",
            (cid, title[:120], now, now, model, provider),
        )
    return get_conversation(cid)


def get_conversation(conv_id: str) -> dict[str, Any] | None:
    _ensure()
    with _conn() as c:
        row = c.execute(
            "SELECT * FROM assistant_conversations WHERE id = ?",
            (conv_id,),
        ).fetchone()
        if not row:
            return None
        msgs = c.execute(
            """SELECT id, role, content, parts_json, created_at
               FROM assistant_messages WHERE conversation_id = ?
               ORDER BY created_at ASC""",
            (conv_id,),
        ).fetchall()
        atts = c.execute(
            """SELECT id, message_id, filename, mime, size, created_at
               FROM assistant_attachments WHERE conversation_id = ?""",
            (conv_id,),
        ).fetchall()
        tools = c.execute(
            """SELECT id, message_id, tool_name, args_json, status, result_json, created_at
               FROM assistant_tool_runs WHERE conversation_id = ?
               ORDER BY created_at ASC""",
            (conv_id,),
        ).fetchall()
    messages = []
    for m in msgs:
        d = dict(m)
        if d.get("parts_json"):
            try:
                d["parts"] = json.loads(d["parts_json"])
            except Exception:
                d["parts"] = None
        else:
            d["parts"] = None
        del d["parts_json"]
        messages.append(d)
    return {
        **dict(row),
        "messages": messages,
        "attachments": [dict(a) for a in atts],
        "tool_runs": [
            {
                **dict(t),
                "args": json.loads(t["args_json"] or "{}"),
                "result": json.loads(t["result_json"]) if t["result_json"] else None,
            }
            for t in tools
        ],
    }


def delete_conversation(conv_id: str) -> bool:
    _ensure()
    with _conn() as c:
        paths = [
            r["path"]
            for r in c.execute(
                "SELECT path FROM assistant_attachments WHERE conversation_id = ?",
                (conv_id,),
            ).fetchall()
        ]
        c.execute("DELETE FROM assistant_tool_runs WHERE conversation_id = ?", (conv_id,))
        c.execute("DELETE FROM assistant_attachments WHERE conversation_id = ?", (conv_id,))
        c.execute("DELETE FROM assistant_messages WHERE conversation_id = ?", (conv_id,))
        cur = c.execute("DELETE FROM assistant_conversations WHERE id = ?", (conv_id,))
        deleted = cur.rowcount > 0
    for p in paths:
        try:
            os.remove(p)
        except OSError:
            pass
    return deleted


def touch_conversation(conv_id: str, *, title: str | None = None,
                       model: str | None = None) -> None:
    _ensure()
    now = int(time.time())
    with _conn() as c:
        if title:
            c.execute(
                "UPDATE assistant_conversations SET updated_at = ?, title = ? WHERE id = ?",
                (now, title[:120], conv_id),
            )
        elif model:
            c.execute(
                "UPDATE assistant_conversations SET updated_at = ?, model = ? WHERE id = ?",
                (now, model, conv_id),
            )
        else:
            c.execute(
                "UPDATE assistant_conversations SET updated_at = ? WHERE id = ?",
                (now, conv_id),
            )


def add_message(conv_id: str, role: str, content: str,
                parts: list | dict | None = None) -> dict[str, Any]:
    _ensure()
    mid = _id()
    now = int(time.time())
    parts_json = json.dumps(parts) if parts is not None else None
    with _conn() as c:
        c.execute(
            """INSERT INTO assistant_messages
               (id, conversation_id, role, content, parts_json, created_at)
               VALUES (?, ?, ?, ?, ?, ?)""",
            (mid, conv_id, role, content or "", parts_json, now),
        )
        c.execute(
            "UPDATE assistant_conversations SET updated_at = ? WHERE id = ?",
            (now, conv_id),
        )
    return {"id": mid, "conversation_id": conv_id, "role": role,
            "content": content or "", "parts": parts, "created_at": now}


def list_messages(conv_id: str) -> list[dict[str, Any]]:
    conv = get_conversation(conv_id)
    return (conv or {}).get("messages") or []


def save_attachment(conv_id: str, *, filename: str, mime: str,
                    data: bytes) -> dict[str, Any]:
    _ensure()
    aid = _id()
    now = int(time.time())
    safe_name = "".join(c if c.isalnum() or c in "._-" else "_" for c in filename)[:80]
    folder = Path(UPLOAD_ROOT) / conv_id
    folder.mkdir(parents=True, exist_ok=True)
    rel = folder / f"{aid}_{safe_name}"
    rel.write_bytes(data)
    with _conn() as c:
        c.execute(
            """INSERT INTO assistant_attachments
               (id, conversation_id, message_id, filename, mime, size, path, created_at)
               VALUES (?, ?, NULL, ?, ?, ?, ?, ?)""",
            (aid, conv_id, filename[:200], mime[:120], len(data), str(rel), now),
        )
    return {
        "id": aid, "conversation_id": conv_id, "filename": filename,
        "mime": mime, "size": len(data), "created_at": now,
    }


def get_attachment(att_id: str) -> dict[str, Any] | None:
    _ensure()
    with _conn() as c:
        row = c.execute(
            "SELECT * FROM assistant_attachments WHERE id = ?", (att_id,),
        ).fetchone()
    return dict(row) if row else None


def bind_attachments(att_ids: list[str], message_id: str) -> None:
    if not att_ids:
        return
    _ensure()
    with _conn() as c:
        for aid in att_ids:
            c.execute(
                "UPDATE assistant_attachments SET message_id = ? WHERE id = ?",
                (message_id, aid),
            )


def delete_attachment(att_id: str) -> bool:
    _ensure()
    with _conn() as c:
        row = c.execute(
            "SELECT path FROM assistant_attachments WHERE id = ?", (att_id,),
        ).fetchone()
        if not row:
            return False
        path = row["path"]
        c.execute("DELETE FROM assistant_attachments WHERE id = ?", (att_id,))
    try:
        os.remove(path)
    except OSError:
        pass
    return True


def create_tool_run(conv_id: str, tool_name: str, args: dict,
                    *, message_id: str | None = None,
                    status: str = "pending") -> dict[str, Any]:
    _ensure()
    rid = _id()
    now = int(time.time())
    with _conn() as c:
        c.execute(
            """INSERT INTO assistant_tool_runs
               (id, conversation_id, message_id, tool_name, args_json, status, result_json, created_at, decided_at)
               VALUES (?, ?, ?, ?, ?, ?, NULL, ?, NULL)""",
            (rid, conv_id, message_id, tool_name, json.dumps(args), status, now),
        )
    return {"id": rid, "tool_name": tool_name, "args": args, "status": status}


def get_tool_run(run_id: str) -> dict[str, Any] | None:
    _ensure()
    with _conn() as c:
        row = c.execute(
            "SELECT * FROM assistant_tool_runs WHERE id = ?", (run_id,),
        ).fetchone()
    if not row:
        return None
    d = dict(row)
    d["args"] = json.loads(d.pop("args_json") or "{}")
    d["result"] = json.loads(d["result_json"]) if d.get("result_json") else None
    return d


def finish_tool_run(run_id: str, status: str, result: Any = None) -> None:
    _ensure()
    now = int(time.time())
    with _conn() as c:
        c.execute(
            """UPDATE assistant_tool_runs
               SET status = ?, result_json = ?, decided_at = ?
               WHERE id = ?""",
            (status, json.dumps(result) if result is not None else None, now, run_id),
        )
