/** Client-side Forecast tab transforms, ported from web/app.js. */

export type ForecastHour = {
  ts?: number;
  predicted_soc?: number;
  load_w?: number;
  solar_w?: number;
};

export type ForecastPayload = {
  ready?: boolean;
  configured?: boolean;
  error?: string;
  error_detail?: string;
  weather_synthetic?: boolean;
  weather_stale?: boolean;
  forecast?: ForecastHour[];
  capacity_wh?: number;
  solar_coefficient?: number;
  fit_samples?: number;
  overall_load_w?: number;
  parasitic_w?: number;
  pack_baseline_w?: number;
  solar_source?: string;
  starting_soc_pct?: number;
  today_actual_solar_wh?: number;
  charge_efficiency?: number;
  readiness?: {
    have_hours?: number;
    needed_hours?: number;
    have_idle_windows?: number;
    needed_idle_windows?: number;
  };
};

export type LearnedBucket = {
  hour?: number;
  weekend?: boolean;
  watts?: number;
};

export type LearnedHourRow = {
  hour: number;
  label: string;
  weekday: string;
  weekend: string;
};

export type DayStripPoint = { label: string; value: number };
export type DayStripTone = "good" | "warn" | "bad" | "mute";
export type DayStripTile = {
  key: string;
  label: string;
  tone: DayStripTone;
  solarKwh: number;
  depleted: boolean;
  points: DayStripPoint[];
};

export type BudgetHour = {
  hour: string;
  solar: number;
  load: number;
  net: number;
  dpp: number;
  soc: number;
};

export type TodayBudget =
  | { empty: true; summary: string }
  | {
      empty: false;
      summary: string;
      chargeEfficiency: number;
      solarKwh: number;
      solarSub: string;
      loadKwh: number;
      loadSub: string;
      netKwh: number;
      netSub: string;
      rows: BudgetHour[];
    };

export type DailySummaryRow = {
  date?: string;
  predicted_sunset_soc_pct?: number | null;
  actual_sunset_soc_pct?: number | null;
  predicted_sunrise_soc_pct?: number | null;
  actual_sunrise_soc_pct?: number | null;
  predictions_made_at?: number | null;
};

export type AccTone = "ok" | "warn" | "err" | "mute";

export type AccuracyRow = {
  date: string;
  stale: boolean;
  sunsetPred: number | null;
  sunsetActual: number | null;
  sunsetErr: number | null;
  sunrisePred: number | null;
  sunriseActual: number | null;
  sunriseErr: number | null;
};

export type AccuracySummary = {
  rows: AccuracyRow[];
  sunsetMae: string | null;
  sunsetN: string;
  sunriseMae: string | null;
  sunriseN: string;
  hitRate: string | null;
  hitSub: string;
};

function num(v: unknown, fallback = 0): number {
  const n = Number(v);
  return Number.isFinite(n) ? n : fallback;
}

function fmt1(n: number): string {
  return (Math.round(n * 10) / 10).toFixed(1);
}

export function solarSourceSuffix(source?: string): string {
  if (source === "forecast_solar") return " · Forecast.Solar";
  if (source === "mixed") return " · Forecast.Solar + Open-Meteo";
  return "";
}

export function weatherNote(j: ForecastPayload): string {
  if (j.weather_synthetic) return " · recent-average solar (weather service offline)";
  if (j.weather_stale) return " · last-good forecast (weather service offline)";
  return "";
}

export function solarCoeffSub(j: ForecastPayload): string {
  const minFit = 2;
  if (j.solar_coefficient === 0) return "no solar production detected on this device";
  const n = num(j.fit_samples, 0);
  if (n >= minFit) return `learned from ${n} hourly samples`;
  const need = Math.max(1, minFit - n);
  return `default — need ${need} more daylight hours of data to fit`;
}

export function avgLoadSub(j: ForecastPayload): string | undefined {
  if (j.parasitic_w == null) return "idle 0 W when inverter off";
  const idle = Math.round(num(j.parasitic_w));
  const packs = num(j.pack_baseline_w);
  return packs
    ? `idle ${idle} W when inverter on · +${Math.round(packs)} W packs`
    : `idle ${idle} W when inverter on`;
}

export function formatLearnedProfile(rows: LearnedBucket[] | undefined): LearnedHourRow[] {
  const byHour: Record<number, LearnedHourRow> = {};
  for (const r of rows || []) {
    const h = Number(r.hour);
    if (!Number.isFinite(h)) continue;
    if (!byHour[h]) {
      byHour[h] = {
        hour: h,
        label: `${String(h).padStart(2, "0")}:00`,
        weekday: "—",
        weekend: "—",
      };
    }
    const watts = r.watts == null ? "—" : String(Math.round(Number(r.watts)));
    if (r.weekend) byHour[h].weekend = watts;
    else byHour[h].weekday = watts;
  }
  return Object.keys(byHour)
    .map(Number)
    .sort((a, b) => a - b)
    .map((h) => byHour[h]);
}

export function buildDayStrip(j: ForecastPayload): DayStripTile[] {
  const fc = j.forecast || [];
  if (!fc.length || !j.ready) return [];

  type Day = {
    key: string;
    date: Date;
    hours: ForecastHour[];
    solarWh: number;
    minSoc: number;
    depleted: boolean;
    startSoc: number;
    endSoc: number;
    sunsetSoc: number | null;
    sunriseSoc: number | null;
  };

  const days: Day[] = [];
  let currentKey: string | null = null;
  let current: Day | null = null;
  for (const h of fc) {
    if (h.ts == null) continue;
    const d = new Date(h.ts * 1000);
    const key = d.toDateString();
    if (key !== currentKey) {
      if (current) days.push(current);
      currentKey = key;
      current = {
        key,
        date: d,
        hours: [],
        solarWh: 0,
        minSoc: Infinity,
        depleted: false,
        startSoc: 0,
        endSoc: 0,
        sunsetSoc: null,
        sunriseSoc: null,
      };
    }
    if (!current) continue;
    current.hours.push(h);
    current.solarWh += num(h.solar_w);
    if (h.predicted_soc != null) {
      const s = Number(h.predicted_soc);
      if (s < current.minSoc) current.minSoc = s;
      if (s <= 0.5) current.depleted = true;
    }
  }
  if (current) days.push(current);

  let prevEnd = num(j.starting_soc_pct);
  for (const d of days) {
    d.startSoc = prevEnd;
    const lastSoc = d.hours[d.hours.length - 1]?.predicted_soc;
    d.endSoc = lastSoc != null ? Number(lastSoc) : prevEnd;
    prevEnd = d.endSoc;
    let sunsetSoc: number | null = null;
    let firstDaylightIdx = -1;
    for (let k = 0; k < d.hours.length; k++) {
      const h = d.hours[k];
      const isDay = num(h.solar_w) > 0;
      if (isDay && firstDaylightIdx === -1) firstDaylightIdx = k;
      if (isDay && h.predicted_soc != null) sunsetSoc = Number(h.predicted_soc);
    }
    let sunriseSoc: number | null = null;
    if (firstDaylightIdx > 0) {
      const prev = d.hours[firstDaylightIdx - 1];
      if (prev?.predicted_soc != null) sunriseSoc = Number(prev.predicted_soc);
    }
    d.sunsetSoc = sunsetSoc;
    d.sunriseSoc = sunriseSoc;
  }

  if (days.length > 0) {
    const todayActualWh = num(j.today_actual_solar_wh);
    if (todayActualWh > 0) days[0].solarWh += todayActualWh;
  }

  const visible = days.slice(0, 5);
  const today = new Date();
  today.setHours(0, 0, 0, 0);

  const labelFor = (d: Day) => {
    const dayStart = new Date(d.date);
    dayStart.setHours(0, 0, 0, 0);
    const diffDays = Math.round((dayStart.getTime() - today.getTime()) / 86400000);
    if (diffDays === 0) return "Today";
    if (diffDays === 1) return "Tomorrow";
    return d.date.toLocaleDateString(undefined, {
      weekday: "short",
      month: "numeric",
      day: "numeric",
    });
  };

  const toneFor = (d: Day): DayStripTone => {
    if (d.depleted) return "bad";
    if (d.minSoc === Infinity) return "mute";
    if (d.minSoc < 20) return "warn";
    return "good";
  };

  return visible.map((d, tileIdx) => {
    const isFirst = tileIdx === 0;
    const nextSunriseSoc =
      isFirst && visible[1]?.sunriseSoc != null ? Number(visible[1].sunriseSoc) : null;
    const points: DayStripPoint[] = [];
    points.push({ label: isFirst ? "now" : "midnight", value: d.startSoc });
    if (d.sunriseSoc != null && Math.abs(d.startSoc - d.sunriseSoc) > 2) {
      points.push({ label: "sunrise", value: d.sunriseSoc });
    }
    const lastShown = points[points.length - 1].value;
    if (
      d.sunsetSoc != null &&
      Math.abs(d.sunsetSoc - lastShown) > 2 &&
      Math.abs(d.sunsetSoc - d.endSoc) > 2
    ) {
      points.push({ label: "sunset", value: d.sunsetSoc });
    }
    points.push({ label: "midnight", value: d.endSoc });
    if (nextSunriseSoc != null) {
      points.push({ label: "sunrise", value: nextSunriseSoc });
    }
    return {
      key: d.key,
      label: labelFor(d),
      tone: toneFor(d),
      solarKwh: d.solarWh / 1000,
      depleted: d.depleted,
      points,
    };
  });
}

export function buildTodayBudget(j: ForecastPayload): TodayBudget {
  const fc = j.forecast || [];
  if (!fc.length || !j.ready) return { empty: true, summary: "—" };

  let i = 0;
  while (i < fc.length && num(fc[i].solar_w) <= 0) i++;
  const dawnIdx = i;
  while (i < fc.length && num(fc[i].solar_w) > 0) i++;
  if (i <= dawnIdx) return { empty: true, summary: "After sunset — open tomorrow" };

  const socStartRaw = dawnIdx > 0 ? fc[dawnIdx - 1].predicted_soc : j.starting_soc_pct;
  const socStart = num(socStartRaw);
  const window = fc.slice(dawnIdx, i);
  const eff = num(j.charge_efficiency, 0.85);
  const cap = num(j.capacity_wh, 30240);
  let solarWh = 0;
  let loadWh = 0;
  let storedWh = 0;
  const rows: BudgetHour[] = window.map((h) => {
    const solar = num(h.solar_w);
    const load = num(h.load_w);
    const net = solar - load;
    const effNet = net > 0 ? net * eff : net;
    solarWh += solar;
    loadWh += load;
    storedWh += effNet;
    const dpp = (effNet / cap) * 100;
    return {
      hour: h.ts
        ? new Date(h.ts * 1000).toLocaleTimeString(undefined, { hour: "numeric" })
        : "—",
      solar: Math.round(solar),
      load: Math.round(load),
      net: Math.round(net),
      dpp,
      soc: num(h.predicted_soc),
    };
  });
  const socEnd = num(window[window.length - 1]?.predicted_soc, socStart);
  const peak = rows.length ? Math.max(...rows.map((r) => r.solar)) : 0;
  const dSoc = socEnd - socStart;
  return {
    empty: false,
    summary: `${Math.round(socStart)} → ${Math.round(socEnd)}% · ${fmt1(solarWh / 1000)} kWh solar`,
    chargeEfficiency: eff,
    solarKwh: solarWh / 1000,
    solarSub: `${rows.length} h, peak ${Math.round(peak)} W`,
    loadKwh: loadWh / 1000,
    loadSub: `avg ${Math.round(loadWh / Math.max(rows.length, 1))} W`,
    netKwh: storedWh / 1000,
    netSub: `${dSoc >= 0 ? "+" : ""}${dSoc.toFixed(1)} pp (loss: ${fmt1((solarWh - loadWh - storedWh) / 1000)} kWh)`,
    rows,
  };
}

function errOf(pred: unknown, actual: unknown): number | null {
  if (pred == null || actual == null) return null;
  const p = Number(pred);
  const a = Number(actual);
  if (!Number.isFinite(p) || !Number.isFinite(a)) return null;
  return a - p;
}

export function accTone(err: number | null): AccTone {
  if (err == null) return "mute";
  const abs = Math.abs(err);
  if (abs <= 3) return "ok";
  if (abs <= 8) return "warn";
  return "err";
}

export function fmtPct(v: number | null): string {
  return v == null || !Number.isFinite(v) ? "—" : `${Math.round(v)}%`;
}

export function fmtErr(err: number | null): string {
  if (err == null || !Number.isFinite(err)) return "—";
  return `${err > 0 ? "+" : ""}${err.toFixed(1)}`;
}

export function summarizeDailyAccuracy(
  rows: DailySummaryRow[] | undefined,
  cutoffTs = 0,
): AccuracySummary {
  const sorted = [...(rows || [])].sort((a, b) => (b.date || "").localeCompare(a.date || ""));
  const sunsetAll: number[] = [];
  const sunriseAll: number[] = [];
  const sunsetPost: number[] = [];
  const sunrisePost: number[] = [];
  const out: AccuracyRow[] = sorted.map((row) => {
    const sunsetErr = errOf(row.predicted_sunset_soc_pct, row.actual_sunset_soc_pct);
    const sunriseErr = errOf(row.predicted_sunrise_soc_pct, row.actual_sunrise_soc_pct);
    const madeAt = num(row.predictions_made_at);
    const isPostFix = cutoffTs > 0 && madeAt > 0 && madeAt >= cutoffTs;
    if (sunsetErr != null) {
      sunsetAll.push(sunsetErr);
      if (isPostFix) sunsetPost.push(sunsetErr);
    }
    if (sunriseErr != null) {
      sunriseAll.push(sunriseErr);
      if (isPostFix) sunrisePost.push(sunriseErr);
    }
    return {
      date: row.date || "—",
      stale: !isPostFix,
      sunsetPred: row.predicted_sunset_soc_pct == null ? null : Number(row.predicted_sunset_soc_pct),
      sunsetActual: row.actual_sunset_soc_pct == null ? null : Number(row.actual_sunset_soc_pct),
      sunsetErr,
      sunrisePred: row.predicted_sunrise_soc_pct == null ? null : Number(row.predicted_sunrise_soc_pct),
      sunriseActual: row.actual_sunrise_soc_pct == null ? null : Number(row.actual_sunrise_soc_pct),
      sunriseErr,
    };
  });

  const mae = (xs: number[]) =>
    xs.length ? (xs.reduce((s, x) => s + Math.abs(x), 0) / xs.length).toFixed(1) : null;
  const hasPostFix = sunsetPost.length + sunrisePost.length > 0;
  const sunsetErrs = hasPostFix ? sunsetPost : sunsetAll;
  const sunriseErrs = hasPostFix ? sunrisePost : sunriseAll;
  const scopeNote = hasPostFix ? " · post-fix" : " · all (no post-fix yet)";
  const all = sunsetErrs.concat(sunriseErrs);
  const hits = all.filter((e) => Math.abs(e) <= 5).length;
  const nLabel = (n: number) =>
    n ? `${n} day${n === 1 ? "" : "s"} measured${scopeNote}` : "no data";

  return {
    rows: out,
    sunsetMae: mae(sunsetErrs),
    sunsetN: nLabel(sunsetErrs.length),
    sunriseMae: mae(sunriseErrs),
    sunriseN: nLabel(sunriseErrs.length),
    hitRate: all.length ? String(Math.round((hits / all.length) * 100)) : null,
    hitSub: all.length
      ? `${hits} of ${all.length} predictions within 5pp${scopeNote}`
      : "no data",
  };
}

export function calibratingCopy(j: ForecastPayload): { title: string; body: string } {
  const r = j.readiness || {};
  const haveH = r.have_hours ?? 0;
  const needH = r.needed_hours ?? 24;
  const haveW = r.have_idle_windows ?? 0;
  const needW = r.needed_idle_windows ?? 5;
  return {
    title: "Forecaster is calibrating",
    body:
      `Need ${needH}h of history (${haveH}h captured) and ` +
      `${needW} clean discharge windows (${haveW} so far). ` +
      `Once enough data accumulates, the forecast will appear here automatically.`,
  };
}
