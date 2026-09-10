import { Platform } from "react-native";
import type { StatusPayload } from "../api/types";
import { etaLabel, headlineSoc } from "./format";
import StatusWidget, { type StatusWidgetProps } from "../../widgets/StatusWidget";

const MIN_INTERVAL_MS = 15_000;
const MEANINGFUL_THROTTLE_MS = 2_000;

const PLACEHOLDER: StatusWidgetProps = {
  name: "",
  soc: -1,
  solarW: 0,
  loadW: 0,
  eta: "",
  connected: false,
  updatedAt: 0,
};

let lastPushed: StatusWidgetProps | null = null;
let lastPushedAt = 0;

function watts(n: unknown): number {
  const v = Number(n ?? 0);
  return Number.isFinite(v) ? Math.round(v) : 0;
}

function socValue(status: StatusPayload): number {
  const raw = headlineSoc(status.telemetry);
  if (raw == null || !Number.isFinite(raw)) return -1;
  return Math.round(raw);
}

export function snapshotFromStatus(
  status: StatusPayload | null,
  wsConnected: boolean,
): StatusWidgetProps {
  if (!status) return PLACEHOLDER;
  const t = status.telemetry;
  const conn = status.connection_status || (wsConnected ? "connected" : "disconnected");
  return {
    name: (status.device?.name || status.device?.device_sn || "Jackery").trim() || "Jackery",
    soc: socValue(status),
    solarW: watts(t?.solar_input_w),
    loadW: watts(t?.output_power_w),
    eta: etaLabel(t),
    connected: conn === "connected",
    updatedAt: Date.now(),
  };
}

function sameGlance(a: StatusWidgetProps, b: StatusWidgetProps): boolean {
  return (
    a.name === b.name &&
    a.soc === b.soc &&
    a.solarW === b.solarW &&
    a.loadW === b.loadW &&
    a.eta === b.eta &&
    a.connected === b.connected &&
    (a.updatedAt === 0) === (b.updatedAt === 0)
  );
}

function meaningfulChange(a: StatusWidgetProps, b: StatusWidgetProps): boolean {
  return (
    a.name !== b.name ||
    a.soc !== b.soc ||
    Math.abs(a.solarW - b.solarW) >= 10 ||
    Math.abs(a.loadW - b.loadW) >= 10 ||
    a.connected !== b.connected ||
    (a.updatedAt === 0) !== (b.updatedAt === 0)
  );
}

function push(props: StatusWidgetProps, force: boolean) {
  if (Platform.OS !== "ios") return;
  const now = Date.now();
  if (!force && lastPushed) {
    if (sameGlance(props, lastPushed)) return;
    const elapsed = now - lastPushedAt;
    if (meaningfulChange(props, lastPushed)) {
      if (elapsed < MEANINGFUL_THROTTLE_MS) return;
    } else if (elapsed < MIN_INTERVAL_MS) {
      return;
    }
  }
  try {
    StatusWidget.updateSnapshot(props);
    lastPushed = props;
    lastPushedAt = now;
  } catch {
    /* widget target missing until a native rebuild */
  }
}

export function pushWidgetSnapshot(status: StatusPayload | null, wsConnected: boolean) {
  push(snapshotFromStatus(status, wsConnected), false);
}

export function clearWidgetSnapshot() {
  lastPushed = null;
  lastPushedAt = 0;
  push(PLACEHOLDER, true);
}
