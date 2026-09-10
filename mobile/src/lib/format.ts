export function fmt(n: number | null | undefined, digits = 1): string {
  if (n == null || !Number.isFinite(Number(n))) return "—";
  return Number(n).toFixed(digits);
}

export function fmtKwh(wh: number | null | undefined, digits = 2): string {
  if (wh == null || !Number.isFinite(Number(wh))) return "—";
  return (Number(wh) / 1000).toFixed(digits);
}

export function fmtW(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(Number(n))) return "—";
  return `${Math.round(Number(n))}`;
}

export function fmtTemp(
  c: number | null | undefined,
  unit: "C" | "F",
): string {
  if (c == null || !Number.isFinite(Number(c))) return "—";
  if (unit === "F") return `${Math.round((Number(c) * 9) / 5 + 32)}°F`;
  return `${Math.round(Number(c))}°C`;
}

export function headlineSoc(t: {
  system_soc_pct?: number | null;
  battery_percent?: number | null;
} | null | undefined): number | null {
  if (!t) return null;
  const v = t.system_soc_pct ?? t.battery_percent;
  return v == null ? null : Number(v);
}

export function etaLabel(t: {
  input_power_w?: number | null;
  output_power_w?: number | null;
  time_to_full_h?: number | null;
  time_remaining_h?: number | null;
  system_soc_pct?: number | null;
  battery_percent?: number | null;
  capacity_wh?: number | null;
} | null | undefined): string {
  if (!t) return "—";
  const inW = Number(t.input_power_w ?? 0);
  const outW = Number(t.output_power_w ?? 0);
  const netW = inW - outW;
  const soc = Number(t.system_soc_pct ?? t.battery_percent ?? 0);
  const capacityWh = Number(t.capacity_wh ?? 5040);
  const idleW = 25;
  const ttFull = Number(t.time_to_full_h ?? 0);
  const ttEmpty = Number(t.time_remaining_h ?? 0);
  if (Math.abs(netW) < idleW) return "Idle";
  if (netW > 0) {
    if (ttFull > 0) return `${fmt(ttFull, 1)} h until full`;
    const wh = ((100 - soc) / 100) * capacityWh;
    const eta = wh / netW;
    return eta > 0 && Number.isFinite(eta) ? `${fmt(eta, 1)} h until full` : "Charging…";
  }
  if (ttEmpty > 0) return `${fmt(ttEmpty, 1)} h remaining`;
  const wh = (soc / 100) * capacityWh;
  const eta = wh / Math.abs(netW);
  return eta > 0 && Number.isFinite(eta) ? `${fmt(eta, 1)} h remaining` : "Discharging…";
}

export function timeAgo(ts: number | null | undefined): string {
  if (!ts) return "—";
  const s = Date.now() / 1000 - ts;
  if (s < 5) return "just now";
  if (s < 60) return `${Math.round(s)}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return new Date(ts * 1000).toLocaleString();
}

export function money(
  n: number | null | undefined,
  currency = "USD",
): string {
  if (n == null || !Number.isFinite(Number(n))) return "—";
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency,
      maximumFractionDigits: 2,
    }).format(Number(n));
  } catch {
    return `${Number(n).toFixed(2)} ${currency}`;
  }
}
