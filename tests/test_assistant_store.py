"""Unit tests for AI Assistant SQLite store + attachment helpers."""
from __future__ import annotations

import os
from pathlib import Path

import pytest

import assistant_agent as agent
import assistant_store as store


@pytest.fixture()
def asst_tmp(tmp_path, monkeypatch):
    db = tmp_path / "assistant.db"
    uploads = tmp_path / "uploads"
    monkeypatch.setenv("JACKERY_ASSISTANT_DB", str(db))
    monkeypatch.setenv("JACKERY_ASSISTANT_UPLOADS", str(uploads))
    monkeypatch.setattr(store, "PATH", str(db))
    monkeypatch.setattr(store, "UPLOAD_ROOT", str(uploads))
    monkeypatch.setattr(store, "_initialized", False)
    return tmp_path


def test_conversation_roundtrip(asst_tmp):
    conv = store.create_conversation(title="Hello", model="claude-test")
    assert conv["id"]
    assert conv["title"] == "Hello"
    store.add_message(conv["id"], "user", "hi there")
    store.add_message(conv["id"], "assistant", "hello back")
    loaded = store.get_conversation(conv["id"])
    assert loaded is not None
    assert len(loaded["messages"]) == 2
    listed = store.list_conversations()
    assert listed[0]["id"] == conv["id"]
    assert "hello" in (listed[0].get("snippet") or "").lower()
    assert store.delete_conversation(conv["id"]) is True
    assert store.get_conversation(conv["id"]) is None


def test_attachment_and_tool_run(asst_tmp):
    conv = store.create_conversation()
    att = store.save_attachment(
        conv["id"],
        filename="shot.png",
        mime="image/png",
        data=b"\x89PNG\r\n\x1a\nfake",
    )
    assert att["id"]
    assert Path(store.get_attachment(att["id"])["path"]).is_file()
    msg = store.add_message(
        conv["id"], "user", "see this",
        parts={"attachment_ids": [att["id"]]},
    )
    store.bind_attachments([att["id"]], msg["id"])
    meta = store.get_attachment(att["id"])
    assert meta["message_id"] == msg["id"]

    run = store.create_tool_run(conv["id"], "set_output", {"port": "ac", "on": True})
    assert run["status"] == "pending"
    store.finish_tool_run(run["id"], "applied", {"ok": True})
    got = store.get_tool_run(run["id"])
    assert got["status"] == "applied"
    assert got["result"]["ok"] is True


def test_allowed_mime_and_limits():
    assert "image/png" in agent.ALLOWED_MIME
    assert "application/pdf" in agent.ALLOWED_MIME
    assert agent.MAX_FILE_BYTES == 10 * 1024 * 1024
    assert agent.MAX_ATTACHMENTS == 5
    assert "schedule_upsert" in agent.WRITE_TOOLS
