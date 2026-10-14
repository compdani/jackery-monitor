"""Authenticated BLE capture API; global /api middleware supplies authentication."""

from __future__ import annotations

import asyncio
import json
import os
import uuid
from pathlib import Path
from typing import Literal

from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import FileResponse, StreamingResponse
from pydantic import BaseModel, Field, model_validator

import ble_inspector as ble
import bms_ble

router = APIRouter(prefix="/api/ble")


class StartRequest(BaseModel):
    operation: Literal["scan", "inspect"]
    seconds: float = Field(default=10, ge=1, le=120, allow_inf_nan=False)
    name: str | None = Field(default=None, min_length=1, max_length=248)
    address: str | None = Field(default=None, min_length=1, max_length=100)
    read_all: bool = False
    notify: list[str] = Field(default_factory=list, max_length=100)
    notify_expected: dict[str, str] = Field(default_factory=dict)
    duration: float = Field(default=30, ge=1, le=3600, allow_inf_nan=False)
    timeout: float = Field(default=15, ge=1, le=120, allow_inf_nan=False)

    @model_validator(mode="after")
    def target(self):
        if self.operation == "inspect" and bool(self.name) == bool(self.address):
            raise ValueError("Inspect requires exactly one name or address")
        if self.operation == "scan" and (self.read_all or self.notify):
            raise ValueError("Scan cannot read or subscribe")
        return self


class Sessions:
    def __init__(self):
        self.task = None
        self.active = None
        self.capture = None

    @property
    def root(self):
        return Path(os.environ.get("JACKERY_DATA_DIR", "/data")) / "ble-captures"

    def path(self, capture_id):
        try:
            if str(uuid.UUID(capture_id)) != capture_id:
                raise ValueError()
        except ValueError:
            raise HTTPException(404, "Unknown capture") from None
        path = self.root / (capture_id + ".jsonl")
        if not path.is_file():
            raise HTTPException(404, "Unknown capture")
        return path

    async def start(self, request):
        lock = bms_ble._adapter_lock()
        if (self.task and not self.task.done()) or lock.locked():
            raise HTTPException(409, "BLE adapter busy; stop capture or wait for BMS operation")
        await lock.acquire()
        try:
            self.root.mkdir(parents=True, exist_ok=True)
            capture_id = str(uuid.uuid4())
            capture = ble.Capture(self.root / (capture_id + ".jsonl"), request.model_dump())
            self.capture = capture
            self.active = capture_id
            self.task = asyncio.create_task(self.execute(capture, request.model_dump(), lock))
        except OSError as exc:
            lock.release()
            raise HTTPException(503, f"Cannot create BLE capture: {exc}") from exc
        except Exception:
            lock.release()
            raise
        return {"id": capture_id}

    async def execute(self, capture, options, lock):
        try:
            await ble.run(capture, options)
        finally:
            # Also covers cancellation before run() begins.
            if not capture.file.closed:
                capture.finish("cancelled", "Stopped")
            self.active = None
            lock.release()

    async def stop(self):
        if self.task and not self.task.done():
            # Let the task enter its cleanup scope before cancellation.
            await asyncio.sleep(0)
            if not self.task.cancelling():
                self.task.cancel()
            await asyncio.gather(self.task, return_exceptions=True)


sessions = Sessions()


@router.get("/status")
async def status():
    captures = []
    if sessions.root.exists():
        for path in sorted(
            sessions.root.glob("*.jsonl"), key=lambda p: p.stat().st_mtime, reverse=True
        ):
            with path.open("rb") as stream:
                stream.seek(max(0, path.stat().st_size - 16384))
                tail = stream.read().splitlines()
            try:
                last = json.loads(tail[-1]) if tail else {}
            except ValueError:
                last = {}
            active = path.stem == sessions.active
            captures.append(
                {
                    "id": path.stem,
                    "bytes": path.stat().st_size,
                    "active": active,
                    "status": "running" if active else last.get("status", "incomplete"),
                    "error": None if active else last.get("error"),
                }
            )
    return {"active": sessions.active, "captures": captures, **bms_ble.bleak_status()}


@router.post("/sessions")
async def start(request: StartRequest):
    return await sessions.start(request)


@router.post("/sessions/{capture_id}/stop")
async def stop(capture_id: str):
    sessions.path(capture_id)
    if sessions.active == capture_id:
        await sessions.stop()
    return {"stopped": True}


@router.get("/captures/{capture_id}/events")
async def poll(capture_id: str, after: int = Query(0, ge=0), limit: int = Query(200, ge=1, le=500)):
    path = sessions.path(capture_id)
    rows = await asyncio.to_thread(ble.events, path, after, limit)
    return {
        "events": rows,
        "active": sessions.active == capture_id,
        "next": rows[-1]["seq"] if rows else after,
        "bytes": path.stat().st_size,
    }


@router.get("/captures/{capture_id}/download")
async def download(capture_id: str, format: Literal["json", "jsonl"] = "jsonl"):
    path = sessions.path(capture_id)
    if sessions.active == capture_id:
        raise HTTPException(409, "Stop capture before downloading")
    if format == "jsonl":
        return FileResponse(path, media_type="application/x-ndjson", filename=path.name)

    return StreamingResponse(
        ble.json_chunks(path),
        media_type="application/json",
        headers={"Content-Disposition": f'attachment; filename="{capture_id}.json"'},
    )


@router.delete("/captures/{capture_id}")
async def delete(capture_id: str):
    path = sessions.path(capture_id)
    if sessions.active == capture_id:
        raise HTTPException(409, "Stop capture before deleting")
    path.unlink()
    return {"deleted": True}
