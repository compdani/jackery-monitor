/** End-of-day / next sun-phase SOC prediction from /api/forecast. */

export type EodState =
  | { kind: 'hidden' }
  | {
      kind: 'calibrating';
      haveH: number;
      needH: number;
      haveW: number;
      needW: number;
      title: string;
    }
  | {
      kind: 'ready';
      label: string;
      sunsetVal: number;
      peakVal: number;
      showBoth: boolean;
      trend: 'up' | 'down' | '';
      low: boolean;
      title: string;
    };

export type ForecastPayload = {
  configured?: boolean;
  error?: string;
  ready?: boolean;
  readiness?: {
    have_hours?: number;
    needed_hours?: number;
    have_idle_windows?: number;
    needed_idle_windows?: number;
  };
  forecast?: { solar_w?: number; predicted_soc?: number | null }[];
  starting_soc_pct?: number;
};

export function deriveEod(j: ForecastPayload | null | undefined): EodState {
  if (!j || !j.configured || j.error) return { kind: 'hidden' };
  if (j.ready === false) {
    const r = j.readiness || {};
    const haveH = Math.round(r.have_hours ?? 0);
    const needH = r.needed_hours ?? 24;
    const haveW = r.have_idle_windows ?? 0;
    const needW = r.needed_idle_windows ?? 5;
    return {
      kind: 'calibrating',
      haveH,
      needH,
      haveW,
      needW,
      title:
        'Forecaster needs more history before it can predict this device. ' +
        `Need ${needH}h of samples (${haveH}h captured) and ${needW} clean discharge windows (${haveW} so far).`,
    };
  }
  const fc = j.forecast;
  if (!Array.isArray(fc) || !fc.length) return { kind: 'hidden' };

  const isDayNow = (fc[0]?.solar_w || 0) > 0;
  let i = 0;
  let label: string;
  if (isDayNow) {
    while (i < fc.length && (fc[i].solar_w || 0) > 0) i++;
    label = 'At sunset';
  } else {
    while (i < fc.length && (fc[i].solar_w || 0) <= 0) i++;
    label = 'At sunrise';
  }
  if (i === 0 || i >= fc.length) return { kind: 'hidden' };
  const best = fc[i - 1];
  if (best.predicted_soc == null) return { kind: 'hidden' };

  const start = j.starting_soc_pct ?? best.predicted_soc;
  const sunsetVal = Math.round(best.predicted_soc);
  let peakVal = sunsetVal;
  if (isDayNow) {
    let pk = -Infinity;
    for (let d = 0; d < i; d++) {
      if (fc[d].predicted_soc != null) pk = Math.max(pk, fc[d].predicted_soc!);
    }
    if (isFinite(pk)) peakVal = Math.round(pk);
  }
  const showBoth = isDayNow && peakVal > Math.round(start) && peakVal > sunsetVal + 1;
  const delta = best.predicted_soc - start;
  const trend: 'up' | 'down' | '' =
    delta > 1 ? 'up' : delta < -1 ? 'down' : '';

  return {
    kind: 'ready',
    label: showBoth ? 'Peak · sunset' : label,
    sunsetVal,
    peakVal,
    showBoth,
    trend,
    low: sunsetVal <= 20,
    title: showBoth
      ? `Peak ${peakVal}% then ${sunsetVal}% at sunset`
      : `${label} ${sunsetVal}% (from ${Math.round(start)}% now)`,
  };
}

/** Watt-scaled animateMotion duration — matches vanilla setFlow. */
export function flowDur(watts: number, idle = 5): string {
  if (watts < idle) return '999s';
  const dur = Math.max(1.2, Math.min(4.5, 4500 / Math.max(50, watts)));
  return `${dur.toFixed(2)}s`;
}

export function flowEtaLabel(t: {
  time_to_full_h?: number | null;
  time_remaining_h?: number | null;
  input_power_w?: number | null;
  output_power_w?: number | null;
  system_soc_pct?: number | null;
  battery_percent?: number | null;
  capacity_wh?: number | null;
} | null): { text: string; cls: string; title: string } {
  if (!t) return { text: '—', cls: '', title: '' };
  const ttFull = Number(t.time_to_full_h ?? 0);
  const ttEmpty = Number(t.time_remaining_h ?? 0);
  const inW = Number(t.input_power_w ?? 0);
  const outW = Number(t.output_power_w ?? 0);
  const netW = inW - outW;
  const soc = Number(t.system_soc_pct ?? t.battery_percent ?? 0);
  const capacityWh = Number(t.capacity_wh ?? 5040);
  const synthFull =
    netW > 25 ? (((100 - soc) / 100) * capacityWh) / netW : 0;
  const synthEmpty =
    netW < -25 ? (soc / 100 * capacityWh) / Math.abs(netW) : 0;

  if (ttFull > 0) {
    return {
      text: `↑ ${ttFull.toFixed(1)}h`,
      cls: 'charging',
      title: `${ttFull.toFixed(1)} h until full`,
    };
  }
  if (ttEmpty > 0) {
    return {
      text: `↓ ${ttEmpty.toFixed(1)}h`,
      cls: 'discharging',
      title: `${ttEmpty.toFixed(1)} h remaining`,
    };
  }
  if (synthFull > 0 && isFinite(synthFull)) {
    return {
      text: `↑ ${synthFull.toFixed(1)}h`,
      cls: 'charging',
      title: `~${synthFull.toFixed(1)} h until full (synthesized)`,
    };
  }
  if (synthEmpty > 0 && isFinite(synthEmpty)) {
    return {
      text: `↓ ${synthEmpty.toFixed(1)}h`,
      cls: 'discharging',
      title: `~${synthEmpty.toFixed(1)} h until empty (synthesized)`,
    };
  }
  return { text: '—', cls: '', title: '' };
}
