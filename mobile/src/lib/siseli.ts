import type { DeviceInfo, DeviceOverview, SiseliControl, SiseliReadings, StatusPayload } from "../api/types";
import { useLive } from "../store/live";

export function isSiseliDevice(d: { source?: string | null; device_id?: string | null; device_sn?: string | null } | null | undefined): boolean {
  if (!d) return false;
  return d.source === "siseli" || String(d.device_id || d.device_sn || "").startsWith("siseli:");
}

export function isSiseliView(status: StatusPayload | null | undefined): boolean {
  if (!status) return false;
  const selected = status.cloud?.selected_device_id;
  const active = (status.cloud?.devices || []).find((d) => String(d.device_id) === String(selected));
  if (isSiseliDevice(active)) return true;
  if (isSiseliDevice(status.device)) return true;
  return status.source === "siseli" || String(selected || "").startsWith("siseli:");
}

export function siseliDeviceId(status: StatusPayload | null | undefined): string {
  return String(
    status?.cloud?.selected_device_id || status?.device?.device_id || status?.device?.device_sn || "",
  );
}

export function pinnedControls(
  controls: SiseliControl[] | null | undefined,
  pins: string[] | null | undefined,
): SiseliControl[] {
  const set = new Set(pins || []);
  return (controls || []).filter((c) => set.has(c.canonical));
}

export function siseliFleet(status: StatusPayload | null | undefined): { sn: string; name: string }[] {
  const devices = (status?.cloud?.devices || []) as DeviceInfo[];
  const overview = (status?.cloud?.devices_overview || []) as DeviceOverview[];
  const rows = devices.length ? devices : overview;
  const out: { sn: string; name: string }[] = [];
  for (const d of rows) {
    if (!isSiseliDevice(d)) continue;
    const sn = String(d.device_sn || d.device_id || "");
    if (!sn) continue;
    out.push({ sn, name: String(d.name || sn) });
  }
  return out;
}

export function readingsLine(readings: SiseliReadings | null | undefined): string {
  if (!readings) return "";
  const parts: string[] = [];
  const add = (label: string, value: number | null | undefined, unit: string) => {
    if (value == null || Number.isNaN(Number(value))) return;
    parts.push(`${label} ${value}${unit}`);
  };
  add("PV", readings.solar_w, " W");
  add("load", readings.load_w, " W");
  add("grid", readings.grid_w, " W");
  add("feed-in", readings.feed_in_w, " W");
  add("battery", readings.battery_v, " V");
  add("charge", readings.charge_a, " A");
  add("discharge", readings.discharge_a, " A");
  add("SOC", readings.soc, "%");
  return parts.join(" · ");
}

export function applySiseliControls(controls: SiseliControl[]) {
  useLive.setState((s) => ({
    status: s.status ? { ...s.status, siseli_controls: controls } : s.status,
  }));
}

export function patchDevicePrefs(patch: {
  live_controls?: string[];
  ignore_inverter_soc?: boolean;
  alias?: string;
}) {
  useLive.setState((s) => {
    if (!s.status) return s;
    return {
      status: {
        ...s.status,
        device_prefs: { ...(s.status.device_prefs || {}), ...patch },
      },
    };
  });
}
