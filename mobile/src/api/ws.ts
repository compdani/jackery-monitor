import { endpoints } from "./client";
import type { StatusPayload } from "./types";
import { wsUrl } from "../lib/url";
import { useConnection } from "../store/connection";
import { useLive } from "../store/live";
import { useSession } from "../store/session";

let socket: WebSocket | null = null;
let pollTimer: ReturnType<typeof setInterval> | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let stopped = true;

function handleMessage(raw: string) {
  let msg: { type?: string; data?: StatusPayload; level?: string; message?: string };
  try {
    msg = JSON.parse(raw);
  } catch {
    return;
  }
  const live = useLive.getState();
  if (msg.type === "snapshot" || msg.type === "telemetry" || msg.type === "status") {
    if (msg.data) live.applyStatus(msg.data);
  } else if (msg.type === "alert" && msg.message) {
    live.pushAlert({ level: msg.level, message: msg.message });
  } else if (msg.type === "automation_fired") {
    live.pushAlert({ message: "Automation rule fired" });
  }
}

async function pollStatus() {
  const view = useLive.getState().viewDeviceId;
  try {
    const s = await endpoints.status(view);
    useLive.getState().applyStatus(s);
  } catch {
    // WS is primary; poll is a safety net.
  }
}

function connect() {
  if (stopped) return;
  const base = useConnection.getState().baseUrl;
  const token = useSession.getState().token;
  if (!base || !token) return;
  const view = useLive.getState().viewDeviceId;
  try {
    socket?.close();
  } catch {
    /* ignore */
  }
  const url = wsUrl(base, token, view);
  const ws = new WebSocket(url);
  socket = ws;
  ws.onopen = () => {
    useLive.getState().setConnected(true);
    useLive.getState().setError(null);
  };
  ws.onmessage = (ev) => handleMessage(String(ev.data));
  ws.onerror = () => {
    useLive.getState().setError("WebSocket error");
  };
  ws.onclose = () => {
    useLive.getState().setConnected(false);
    if (stopped) return;
    reconnectTimer = setTimeout(connect, 2000);
  };
}

export function startLive() {
  stopped = false;
  connect();
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(pollStatus, 2000);
  void pollStatus();
}

export function stopLive() {
  stopped = true;
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = null;
  if (reconnectTimer) clearTimeout(reconnectTimer);
  reconnectTimer = null;
  try {
    socket?.close();
  } catch {
    /* ignore */
  }
  socket = null;
}

export function reconnectLive() {
  if (stopped) return;
  try {
    socket?.close();
  } catch {
    /* ignore */
  }
}
