import { useConnection } from "../store/connection";
import { useLive } from "../store/live";
import { useSession } from "../store/session";
import type { ProbeResult, StatusPayload } from "./types";

export class ApiError extends Error {
  status: number;
  detail: unknown;
  constructor(status: number, detail: unknown, message?: string) {
    super(message || (typeof detail === "string" ? detail : `HTTP ${status}`));
    this.status = status;
    this.detail = detail;
  }
}

function detailMessage(detail: unknown): string {
  if (detail == null) return "Request failed";
  if (typeof detail === "string") return detail;
  if (typeof detail === "object" && detail && "detail" in detail) {
    const d = (detail as { detail: unknown }).detail;
    if (typeof d === "string") return d;
    return JSON.stringify(d);
  }
  try {
    return JSON.stringify(detail);
  } catch {
    return "Request failed";
  }
}

type Opts = Omit<RequestInit, "body"> & {
  auth?: boolean;
  query?: Record<string, string | number | boolean | null | undefined>;
  body?: unknown;
};

function qs(query?: Opts["query"]): string {
  if (!query) return "";
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v == null || v === "") continue;
    p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

const SESSION_COOKIE = "jackery_session";

function sessionTokenFromHeaders(headers: Headers): string | null {
  const raw = headers.get("set-cookie") || headers.get("Set-Cookie") || "";
  if (!raw) return null;
  const m = raw.match(new RegExp(`(?:^|,)\\s*${SESSION_COOKIE}=([^;]+)`));
  if (!m) return null;
  try {
    return decodeURIComponent(m[1].trim());
  } catch {
    return m[1].trim();
  }
}

function authSession(
  data: unknown,
  headers: Headers,
  fallbackUsername: string,
): { ok: boolean; username: string; token: string } {
  const o = data && typeof data === "object" ? (data as Record<string, unknown>) : {};
  const token =
    (typeof o.token === "string" && o.token) || sessionTokenFromHeaders(headers) || "";
  const username =
    (typeof o.username === "string" && o.username) || fallbackUsername;
  if (!token) {
    throw new ApiError(
      0,
      data,
      "Server did not return a session token. Update Jackery Monitor and try again.",
    );
  }
  return { ok: true, username, token };
}

async function requestJson<T = unknown>(
  path: string,
  opts: Opts = {},
): Promise<{ data: T; res: Response }> {
  const base = useConnection.getState().baseUrl;
  if (!base) throw new ApiError(0, "no_server", "No server URL configured");
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(opts.headers as Record<string, string> | undefined),
  };
  let body = opts.body;
  if (body && typeof body === "object" && !(body instanceof FormData) && typeof body !== "string") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(body);
  }
  const auth = opts.auth !== false;
  const token = useSession.getState().token;
  if (auth && token) headers.Authorization = `Bearer ${token}`;

  const url = `${base}${path}${qs(opts.query)}`;
  const { auth: _auth, query: _query, body: _body, headers: _headers, ...init } = opts;
  let res: Response;
  try {
    res = await fetch(url, { ...init, headers, body: body as BodyInit | undefined });
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : "Network error");
  }

  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }

  if (typeof data === "string" && data.trimStart().startsWith("<")) {
    throw new ApiError(
      res.status,
      "html",
      "Server returned a web page instead of JSON. Check the server URL.",
    );
  }

  if (res.status === 401 && auth) {
    const detail = (data as { detail?: string } | null)?.detail;
    if (detail === "auth_required") {
      await useSession.getState().clear();
    }
  }

  if (!res.ok) {
    const d = (data as { detail?: unknown } | null)?.detail ?? data;
    throw new ApiError(res.status, d, detailMessage(d));
  }
  return { data: data as T, res };
}

export async function api<T = unknown>(path: string, opts: Opts = {}): Promise<T> {
  const { data } = await requestJson<T>(path, opts);
  return data;
}

async function authForm(path: string, username: string, password: string) {
  const { data, res } = await requestJson(path, {
    method: "POST",
    auth: false,
    body: { username, password },
  });
  return authSession(data, res.headers, username);
}

export async function probeServer(baseUrl: string): Promise<ProbeResult> {
  try {
    const res = await fetch(`${baseUrl}/api/status`, {
      headers: { Accept: "application/json" },
    });
    const text = await res.text();
    let data: unknown = null;
    try {
      data = text ? JSON.parse(text) : null;
    } catch {
      data = text;
    }
    if (res.status === 401) {
      const detail = (data as { detail?: string } | null)?.detail;
      if (detail === "setup_required") return { kind: "setup" };
      return { kind: "login" };
    }
    if (!res.ok) {
      return { kind: "unreachable", error: `HTTP ${res.status}` };
    }
    return { kind: "ok", status: data as StatusPayload };
  } catch (e) {
    return {
      kind: "unreachable",
      error: e instanceof Error ? e.message : "Could not reach server",
    };
  }
}

export const endpoints = {
  me: () => api<{ username: string }>("/api/auth/me"),
  login: (username: string, password: string) => authForm("/api/auth/login", username, password),
  setup: (username: string, password: string) => authForm("/api/auth/setup", username, password),
  logout: () => api("/api/auth/logout", { method: "POST" }),
  changePassword: (current: string, next: string) =>
    api("/api/auth/change_password", { method: "POST", body: { current, new: next } }),
  cloudStatus: () => api<{ has_credentials?: boolean; cloud_state?: string; backend?: string }>("/api/auth/status", { auth: false }),
  setCloudCreds: (email: string, password: string, region: string) =>
    api("/api/auth/credentials", { method: "POST", body: { email, password, region } }),
  forgetCloud: () => api("/api/auth/forget", { method: "POST" }),

  status: (viewDeviceId?: string | null) =>
    api<StatusPayload>("/api/status", { query: { view_device_id: viewDeviceId || undefined } }),
  selectView: (device_id: string) =>
    api("/api/view/select_device", { method: "POST", body: { device_id } }),
  setOutput: (port: string, on: boolean, device_sn?: string | null) =>
    api("/api/set_output", { method: "POST", body: { port, on, device_sn } }),
  reconnect: () => api("/api/reconnect", { method: "POST" }),
  pausePolling: (seconds: number) =>
    api("/api/pause_polling", { method: "POST", body: { seconds } }),
  resumePolling: () => api("/api/resume_polling", { method: "POST" }),

  energyHistory: (hours: number, device_sn?: string | null, bucket_s?: number) =>
    api("/api/energy/history", { query: { hours, device_sn, bucket_s } }),
  energyDevices: () => api("/api/energy/devices"),
  energyDaily: (device_sn: string | undefined, days = 365) =>
    api<{ device_sn: string; days: number; daily: import("./types").DailyRow[] }>("/api/energy/daily", {
      query: { device_sn, days },
    }),

  forecast: (device_sn?: string | null) =>
    api("/api/forecast", { query: { device_sn } }),
  forecastAccuracy: () => api("/api/forecast/accuracy"),
  solarArray: () => api("/api/forecast/solar_array"),
  setSolarArray: (declination: number, azimuth: number, kwp: number) =>
    api("/api/forecast/solar_array", { method: "POST", body: { declination, azimuth, kwp } }),
  inferSolarArray: (device_sn?: string | null) =>
    api("/api/forecast/solar_array/infer", { method: "POST", query: { device_sn } }),
  setSolarKey: (api_key: string) =>
    api("/api/forecast/solar_key", { method: "POST", body: { api_key } }),
  clearSolarKey: () => api("/api/forecast/solar_key", { method: "DELETE" }),
  loadSchedule: (device_sn?: string | null) =>
    api("/api/forecast/load_schedule", { query: { device_sn } }),
  setLoadSchedule: (body: Record<string, unknown>, copyLearned = false) =>
    api("/api/forecast/load_schedule", {
      method: "POST",
      query: copyLearned ? { copy_learned: 1 } : undefined,
      body,
    }),
  dailySummary: (days?: number) => api("/api/daily_summary", { query: { days } }),
  location: () => api("/api/location"),
  setLocation: (latitude: number, longitude: number, label?: string) =>
    api("/api/location", { method: "POST", body: { latitude, longitude, label } }),
  geocode: (q: string) => api("/api/location/geocode", { query: { q, count: 6 } }),

  devices: () => api("/api/devices"),
  capacity: () => api("/api/devices/capacity"),
  setCapacity: (device_sn: string, capacity_wh: number | null) =>
    api("/api/devices/capacity", { method: "POST", body: { device_sn, capacity_wh } }),
  params: (device_sn?: string | null) =>
    api("/api/devices/params", { query: { device_sn } }),
  setParam: (device_sn: string, key: string, value: number | null) =>
    api("/api/devices/params", { method: "POST", body: { device_sn, key, value } }),
  refitParam: (device_sn: string, key: string) =>
    api("/api/devices/params/refit", { method: "POST", body: { device_sn, key } }),
  rawProps: (device_sn?: string | null) =>
    api("/api/debug/raw_props", { query: { device_sn } }),
  cloudProbe: () => api("/api/debug/cloud_probe"),
  probeNow: (device_sn?: string | null) =>
    api("/api/devices/probe_now", { method: "POST", query: { device_sn } }),
  probeResults: () => api("/api/devices/probe_results"),
  dismissWatchdog: () => api("/api/inverter_watchdog/dismiss", { method: "POST" }),

  events: (limit = 200, since?: number) =>
    api<{ events: unknown[] }>("/api/events", { query: { limit, since } }),

  settings: () => api<{ settings: import("./types").SettingSpec[] }>("/api/settings"),
  saveSettings: (body: Record<string, number>) =>
    api("/api/settings", { method: "POST", body }),

  costPlan: () => api("/api/cost/plan"),
  saveCostPlan: (body: unknown) => api("/api/cost/plan", { method: "POST", body }),

  smartConfig: (device_sn?: string | null) =>
    api("/api/smart_charge/config", { query: { device_sn } }),
  saveSmartConfig: (body: unknown, device_sn?: string | null) =>
    api("/api/smart_charge/config", { method: "POST", body, query: { device_sn } }),
  smartStatus: (device_sn?: string | null) =>
    api("/api/smart_charge/status", { query: { device_sn } }),
  smartAnalytics: (device_sn?: string | null, days = 14) =>
    api("/api/smart_charge/analytics", { query: { device_sn, days } }),
  smartEvaluate: (device_sn?: string | null) =>
    api("/api/smart_charge/evaluate_now", { method: "POST", query: { device_sn } }),
  smartBacktest: (params: Record<string, string | number | undefined>) =>
    api("/api/smart_charge/backtest", { query: params }),
  smartDecisionDetails: (decided_at: string, device_sn?: string | null) =>
    api("/api/smart_charge/decision_details", { query: { decided_at, device_sn } }),

  solarConfig: (device_sn?: string | null) =>
    api("/api/solar_charge/config", { query: { device_sn } }),
  saveSolarConfig: (body: unknown, device_sn?: string | null) =>
    api("/api/solar_charge/config", { method: "POST", body, query: { device_sn } }),
  solarStatus: (device_sn?: string | null) =>
    api("/api/solar_charge/status", { query: { device_sn } }),
  solarHistory: (device_sn?: string | null) =>
    api("/api/solar_charge/history", { query: { device_sn, hours: 24, limit: 50 } }),
  solarEvaluate: (device_sn?: string | null) =>
    api("/api/solar_charge/evaluate_now", { method: "POST", query: { device_sn } }),

  rules: () => api<{ rules: unknown[] }>("/api/automation/rules"),
  saveRule: (body: unknown) => api("/api/automation/rules", { method: "POST", body }),
  deleteRule: (id: string) => api(`/api/automation/rules/${id}`, { method: "DELETE" }),
  ruleHistory: (id: string, days = 14) =>
    api(`/api/automation/rules/${id}/history`, { query: { days } }),
  disableRules: (rule_ids: string[]) =>
    api("/api/automation/rules/disable", { method: "POST", body: { rule_ids } }),

  kasaCreds: () => api("/api/kasa/credentials"),
  saveKasaCreds: (email: string, password: string) =>
    api("/api/kasa/credentials", { method: "POST", body: { email, password } }),
  forgetKasaCreds: () => api("/api/kasa/credentials", { method: "DELETE" }),
  kasaSaved: (refresh = false, jackery_sn?: string | null) =>
    api("/api/kasa/saved", { query: { refresh: refresh ? "true" : "false", jackery_sn } }),
  saveKasaDevice: (body: unknown) => api("/api/kasa/saved", { method: "POST", body }),
  deleteKasaDevice: (host: string) =>
    api(`/api/kasa/saved/${encodeURIComponent(host)}`, { method: "DELETE" }),
  kasaTest: (host: string, on: boolean) =>
    api("/api/kasa/test", { method: "POST", body: { host, on } }),
  kasaStatus: (host: string) => api("/api/kasa/status", { query: { host } }),
  kasaDiscover: () => api("/api/kasa/devices"),
  kasaHealth: () => api("/api/kasa/health"),

  suggestions: (device_sn?: string | null, status = "pending") =>
    api("/api/algorithm/suggestions", { query: { device_sn, status } }),
  applySuggestion: (id: string) =>
    api(`/api/algorithm/suggestions/${id}/apply`, { method: "POST" }),
  dismissSuggestion: (id: string) =>
    api(`/api/algorithm/suggestions/${id}/dismiss`, { method: "POST" }),
  reviewNow: (device_sn?: string | null) =>
    api("/api/algorithm/review_now", { method: "POST", query: { device_sn } }),
  reviewStatus: (device_sn?: string | null) =>
    api("/api/algorithm/review_status", { query: { device_sn } }),
  advisorPreview: (device_sn?: string | null) =>
    api("/api/algorithm/preview", { query: { device_sn } }),
  advisorChanges: (device_sn?: string | null) =>
    api("/api/algorithm/changes", { query: { device_sn } }),

  aiProvider: () => api("/api/ai/provider"),
  setAiProvider: (provider: string) =>
    api("/api/ai/provider", { method: "POST", body: { provider } }),
  anthropicKey: () => api("/api/anthropic/key"),
  saveAnthropicKey: (key: string) =>
    api("/api/anthropic/key", { method: "POST", body: { key } }),
  deleteAnthropicKey: () => api("/api/anthropic/key", { method: "DELETE" }),
  anthropicModels: () => api("/api/anthropic/models"),
  anthropicPrefs: () => api("/api/anthropic/prefs"),
  saveAnthropicPrefs: (body: unknown) =>
    api("/api/anthropic/prefs", { method: "POST", body }),
  openaiKey: () => api("/api/openai/key"),
  saveOpenaiKey: (key: string) =>
    api("/api/openai/key", { method: "POST", body: { key } }),
  deleteOpenaiKey: () => api("/api/openai/key", { method: "DELETE" }),
  openaiModels: () => api("/api/openai/models"),

  backupCreds: () => api("/api/backup/credentials"),
  saveBackupCreds: (body: unknown) =>
    api("/api/backup/credentials", { method: "POST", body }),
  deleteBackupCreds: () => api("/api/backup/credentials", { method: "DELETE" }),
  backupTest: (body?: unknown) =>
    api("/api/backup/test", { method: "POST", body: body ?? {} }),
  backupDiscover: () => api("/api/backup/discover"),
  backupListShares: (body: unknown) =>
    api("/api/backup/list-shares", { method: "POST", body }),
  backupListRsync: (body: unknown) =>
    api("/api/backup/list-rsync-modules", { method: "POST", body }),
  backupStatus: () => api("/api/backup/status"),
  backupRun: () => api("/api/backup/run", { method: "POST" }),
  backupSnapshots: () => api("/api/backup/snapshots"),
  backupRestore: (body: unknown) =>
    api("/api/backup/restore", { method: "POST", body }),
  setupRestoreTest: (body: unknown) =>
    api("/api/backup/setup_restore/test", { method: "POST", auth: false, body }),
  setupRestoreSnapshots: (body: unknown) =>
    api("/api/backup/setup_restore/snapshots", { method: "POST", auth: false, body }),
  setupRestore: (body: unknown) =>
    api("/api/backup/setup_restore/restore", { method: "POST", auth: false, body }),
};

export function activeSn(): string | undefined {
  const s = useLive.getState().status;
  return (s?.device?.device_sn as string | undefined) || undefined;
}
