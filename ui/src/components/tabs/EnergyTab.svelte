<script lang="ts">
  import { onMount } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { api } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { fmtKwh } from '$lib/format';
  import { savingsLines, type Savings } from '$lib/formatMoney';
  import {
    readEnergyBucketS,
    writeEnergyBucketS,
    readEnergyChartStyle,
    writeEnergyChartStyle,
    type EnergyChartStyle,
  } from '$lib/prefs';
  import { drawEnergyChart, energyCompareColor, setEnergyChartStyle } from '$lib/charts/canvas';

  type DailyRow = {
    date?: string;
    solar_kwh?: number;
    consumed_kwh?: number;
    charged_kwh?: number;
    grid_kwh?: number;
    diverted_kwh?: number;
    solar_charge_diverted_wh?: number;
    peak_solar_w?: number;
    peak_output_w?: number;
    min_soc?: number;
    max_soc?: number;
  };

  type HistorySeries = {
    device_sn: string;
    name: string;
    history: unknown[];
    bucket_s?: number;
  };

  const RANGES = [
    { hours: 6, label: '6h' },
    { hours: 24, label: '24h' },
    { hours: 168, label: '7d' },
    { hours: 720, label: '30d' },
  ] as const;

  const BUCKETS = [
    { s: 900, label: '15m' },
    { s: 1800, label: '30m' },
    { s: 3600, label: '1h' },
  ] as const;

  const COMPARE_COLORS = ['#4ade80', '#38bdf8', '#fbbf24', '#c084fc', '#fb7185'];

  let hours = $state(24);
  let bucketS = $state(readEnergyBucketS());
  let chartStyle = $state<EnergyChartStyle>(readEnergyChartStyle());
  let historyPayload = $state.raw<{ hours: number; series: HistorySeries[] } | null>(null);
  let daily = $state.raw<DailyRow[]>([]);
  let dailyDeviceSn = $state('');
  let compareSns = new SvelteSet<string>();
  let busy = $state(false);
  let error = $state('');

  const energy = $derived(live.status?.energy as Record<string, unknown> | undefined);
  const today = $derived((energy?.today ?? {}) as {
    output_wh?: number;
    input_wh?: number;
    solar_charge_diverted_wh?: number;
  });
  const last7 = $derived((energy?.last_7d ?? {}) as { output_wh?: number; input_wh?: number });
  const last30 = $derived((energy?.last_30d ?? {}) as { output_wh?: number; input_wh?: number });
  const life = $derived((energy?.lifetime ?? {}) as { output_wh?: number; input_wh?: number });
  const currency = $derived(
    ((energy?.cost_plan as { currency?: string } | undefined)?.currency) || 'USD',
  );
  const todaySavings = $derived(energy?.today_savings as Savings | undefined);
  const last7Savings = $derived(energy?.last_7d_savings as Savings | undefined);
  const last30Savings = $derived(energy?.last_30d_savings as Savings | undefined);
  const lifetimeSavings = $derived(energy?.lifetime_savings as Savings | undefined);
  const todaySavLines = $derived(savingsLines(todaySavings, currency));
  const last7SavLines = $derived(savingsLines(last7Savings, currency));
  const last30SavLines = $derived(savingsLines(last30Savings, currency));
  const lifeSavLines = $derived(savingsLines(lifetimeSavings, currency));

  const devices = $derived(
    (live.devices || []).filter((d) => d.device_sn) as {
      device_sn: string;
      name?: string;
      model_name?: string;
    }[],
  );
  const showCompare = $derived(devices.length >= 2);
  const hasDivertedCol = $derived(
    daily.some(
      (r) =>
        (r.diverted_kwh != null && r.diverted_kwh !== 0) ||
        (r.solar_charge_diverted_wh != null && r.solar_charge_diverted_wh !== 0),
    ),
  );

  type RecordTile = { label: string; value: string; sub: string };
  const records = $derived.by((): RecordTile[] => {
    if (!daily.length) return [];
    const tiles: RecordTile[] = [];
    const add = (label: string, key: keyof DailyRow) => {
      let best: DailyRow | null = null;
      let bestV = -Infinity;
      for (const d of daily) {
        const n = Number(d[key]);
        if (Number.isFinite(n) && n > bestV) {
          bestV = n;
          best = d;
        }
      }
      if (!best || bestV <= 0) return;
      tiles.push({
        label,
        value: bestV.toFixed(2),
        sub: fmtDayShort(best.date),
      });
    };
    add('Peak solar day', 'solar_kwh');
    add('Peak consumption day', 'consumed_kwh');
    add('Peak grid day', 'grid_kwh');
    add('Best diversion day', 'diverted_kwh');
    // Peak output watts (if present)
    {
      let best: DailyRow | null = null;
      let bestV = -Infinity;
      for (const d of daily) {
        const n = Number(d.peak_output_w);
        if (Number.isFinite(n) && n > bestV) {
          bestV = n;
          best = d;
        }
      }
      if (best && bestV > 0) {
        tiles.push({
          label: 'Peak output',
          value: String(Math.round(bestV)),
          sub: fmtDayShort(best.date),
        });
      }
    }
    const last30d = daily.slice(-30);
    const avg = (key: keyof DailyRow) =>
      last30d.reduce((s, d) => s + (Number(d[key]) || 0), 0) / (last30d.length || 1);
    tiles.push({
      label: 'Avg solar/day',
      value: avg('solar_kwh').toFixed(2),
      sub: 'last 30 days',
    });
    tiles.push({
      label: 'Avg consumed/day',
      value: avg('consumed_kwh').toFixed(2),
      sub: 'last 30 days',
    });
    return tiles;
  });

  function fmtDayShort(date?: string) {
    if (!date) return '—';
    try {
      const d = new Date(date + 'T12:00:00');
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    } catch {
      return date;
    }
  }

  function deviceName(sn: string) {
    const d = devices.find((x) => x.device_sn === sn);
    return d?.name || d?.model_name || sn;
  }

  function chipColor(sn: string) {
    const idx = devices.findIndex((d) => d.device_sn === sn);
    if (idx >= 0) return COMPARE_COLORS[idx % COMPARE_COLORS.length];
    return energyCompareColor(sn);
  }

  function seedCompare() {
    const sn = live.activeDeviceSn() || live.status?.device?.device_sn;
    if (!sn) return;
    if (!compareSns.size) compareSns.add(sn);
    for (const x of [...compareSns]) {
      if (!devices.some((d) => d.device_sn === x)) compareSns.delete(x);
    }
    if (!compareSns.size && sn) compareSns.add(sn);
  }

  function toggleCompare(sn: string) {
    if (compareSns.has(sn)) {
      if (compareSns.size <= 1) return;
      compareSns.delete(sn);
    } else {
      compareSns.add(sn);
    }
    loadHistory();
  }

  function orderedCompareSns(): string[] {
    seedCompare();
    const wanted = compareSns;
    const ordered = devices.map((d) => d.device_sn).filter((sn) => sn && wanted.has(sn));
    for (const sn of wanted) {
      if (sn && !ordered.includes(sn)) ordered.push(sn);
    }
    return ordered;
  }

  async function loadHistory() {
    busy = true;
    error = '';
    try {
      const sns = orderedCompareSns();
      const viewed = live.activeDeviceSn() || live.status?.device?.device_sn;
      const fallback = sns.length ? sns : viewed ? [viewed] : [];
      if (!fallback.length) {
        historyPayload = { hours, series: [] };
        return;
      }
      // Vanilla fetches one /energy/history?device_sn= per selected device
      // (not a comma-joined device_sns param) and merges into series[].
      const results = await Promise.all(
        fallback.map(async (sn) => {
          const q = new URLSearchParams({
            hours: String(hours),
            bucket_s: String(bucketS),
            device_sn: sn,
          });
          try {
            const j = await api<{ history?: unknown[]; bucket_s?: number }>(
              `/energy/history?${q}`,
            );
            return {
              device_sn: sn,
              name: deviceName(sn),
              history: j.history || [],
              bucket_s: j.bucket_s,
            } satisfies HistorySeries;
          } catch {
            return { device_sn: sn, name: deviceName(sn), history: [] } satisfies HistorySeries;
          }
        }),
      );
      historyPayload = { hours, series: results };
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load history';
      historyPayload = null;
    } finally {
      busy = false;
    }
  }

  async function loadDaily() {
    try {
      const sn = live.activeDeviceSn() || live.status?.device?.device_sn;
      const q = new URLSearchParams({ days: '365' });
      if (sn) q.set('device_sn', sn);
      const j = await api<{ daily?: DailyRow[]; device_sn?: string }>(`/energy/daily?${q}`);
      daily = j.daily || [];
      dailyDeviceSn = j.device_sn || sn || '';
    } catch {
      daily = [];
    }
  }

  onMount(() => {
    setEnergyChartStyle(chartStyle);
    seedCompare();
    loadHistory();
    loadDaily();
    return live.onDeviceChange(() => {
      seedCompare();
      loadHistory();
      loadDaily();
    });
  });

  function attachEnergyChart(payload: { hours: number; series: HistorySeries[] } | null) {
    return (_canvas: HTMLCanvasElement) => {
      setEnergyChartStyle(chartStyle);
      drawEnergyChart(
        payload ? { ...payload, chart_style: chartStyle } : { history: [], chart_style: chartStyle },
      );
    };
  }

  function setHours(h: number) {
    hours = h;
    loadHistory();
  }

  function setBucket(s: number) {
    bucketS = s;
    writeEnergyBucketS(s);
    loadHistory();
  }

  function setChartStyle(style: EnergyChartStyle) {
    chartStyle = style;
    writeEnergyChartStyle(style);
    setEnergyChartStyle(style);
    drawEnergyChart(
      historyPayload
        ? { ...historyPayload, chart_style: chartStyle }
        : { history: [], chart_style: chartStyle },
    );
  }

  function divertedKwh(row: DailyRow): number | null {
    if (row.diverted_kwh != null) return row.diverted_kwh;
    if (row.solar_charge_diverted_wh != null) return row.solar_charge_diverted_wh / 1000;
    return null;
  }

  function kwhCell(v: number | null | undefined) {
    const n = Number(v);
    return !Number.isFinite(n) || n === 0 ? '—' : n.toFixed(2);
  }

  function exportCsv() {
    const cols = [
      'date',
      'solar_kwh',
      'consumed_kwh',
      'charged_kwh',
      'grid_kwh',
      'diverted_kwh',
      'peak_solar_w',
      'peak_output_w',
      'min_soc',
      'max_soc',
    ];
    const lines = [cols.join(',')];
    for (const d of daily) {
      const row = {
        ...d,
        diverted_kwh: divertedKwh(d) ?? '',
      } as Record<string, unknown>;
      lines.push(cols.map((c) => (row[c] == null ? '' : String(row[c]))).join(','));
    }
    const sn = String(dailyDeviceSn || 'device').replace(/[^A-Za-z0-9._-]/g, '_');
    const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `energy-daily-${sn}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 5000);
  }
</script>

<section class="tab-panel">
  <div class="grid energy-grid">
    <section class="card">
      <div class="card-eyebrow"><span>Today</span></div>
      <div class="kpi-row">
        <div>
          <div class="kpi-value"><span>{fmtKwh(today.output_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">consumed</div>
        </div>
        <div>
          <div class="kpi-value alt"><span>{fmtKwh(today.input_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">charged</div>
        </div>
      </div>
      {#if todaySavLines}
        <div class="savings-row">
          <span class="sav-pos">{todaySavLines.solar}</span>
          <span class="sav-sep">·</span>
          <span class="sav-neg">{todaySavLines.grid}</span>
          <span class="sav-net">{todaySavLines.net}</span>
        </div>
      {/if}
    </section>
    <section class="card">
      <div class="card-eyebrow"><span>Last 7 days</span></div>
      <div class="kpi-row">
        <div>
          <div class="kpi-value"><span>{fmtKwh(last7.output_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">consumed</div>
        </div>
        <div>
          <div class="kpi-value alt"><span>{fmtKwh(last7.input_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">charged</div>
        </div>
      </div>
      {#if last7SavLines}
        <div class="savings-row">
          <span class="sav-pos">{last7SavLines.solar}</span>
          <span class="sav-sep">·</span>
          <span class="sav-neg">{last7SavLines.grid}</span>
          <span class="sav-net">{last7SavLines.net}</span>
        </div>
      {/if}
    </section>
    <section class="card">
      <div class="card-eyebrow"><span>Last 30 days</span></div>
      <div class="kpi-row">
        <div>
          <div class="kpi-value"><span>{fmtKwh(last30.output_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">consumed</div>
        </div>
        <div>
          <div class="kpi-value alt"><span>{fmtKwh(last30.input_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">charged</div>
        </div>
      </div>
      {#if last30SavLines}
        <div class="savings-row">
          <span class="sav-pos">{last30SavLines.solar}</span>
          <span class="sav-sep">·</span>
          <span class="sav-neg">{last30SavLines.grid}</span>
          <span class="sav-net">{last30SavLines.net}</span>
        </div>
      {/if}
    </section>
    <section class="card">
      <div class="card-eyebrow"><span>Lifetime</span></div>
      <div class="kpi-row">
        <div>
          <div class="kpi-value"><span>{fmtKwh(life.output_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">consumed</div>
        </div>
        <div>
          <div class="kpi-value alt"><span>{fmtKwh(life.input_wh)}</span><small>kWh</small></div>
          <div class="kpi-sub">charged</div>
        </div>
      </div>
      {#if lifeSavLines}
        <div class="savings-row">
          <span class="sav-pos">{lifeSavLines.solar}</span>
          <span class="sav-sep">·</span>
          <span class="sav-neg">{lifeSavLines.grid}</span>
          <span class="sav-net">{lifeSavLines.net}</span>
        </div>
      {/if}
    </section>
  </div>

  {#if records.length}
    <section class="card" id="energy-records">
      <div class="card-header">
        <h2>Records</h2>
      </div>
      <div class="energy-records-grid">
        {#each records as t (t.label)}
          <div class="energy-record">
            <div class="kpi-sub rec-label">{t.label}</div>
            <div class="kpi-value">
              {t.value}<small>{t.label === 'Peak output' ? 'W' : 'kWh'}</small>
            </div>
            <div class="kpi-sub">{t.sub}</div>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  <section class="card">
    <div class="card-header">
      <h2>Energy history</h2>
      <div class="energy-history-controls">
        <div class="range-picker" role="tablist" aria-label="Time range">
          {#each RANGES as r (r.hours)}
            <button
              type="button"
              class="range-btn"
              class:on={hours === r.hours}
              onclick={() => setHours(r.hours)}
            >
              {r.label}
            </button>
          {/each}
        </div>
        <div class="range-picker" role="tablist" aria-label="Interval">
          {#each BUCKETS as b (b.s)}
            <button
              type="button"
              class="ebucket-btn"
              class:on={bucketS === b.s}
              onclick={() => setBucket(b.s)}
            >
              {b.label}
            </button>
          {/each}
        </div>
        <div class="range-picker" role="tablist" aria-label="Chart style">
          <button
            type="button"
            class="range-btn"
            class:on={chartStyle === 'bar'}
            onclick={() => setChartStyle('bar')}
          >
            Bars
          </button>
          <button
            type="button"
            class="range-btn"
            class:on={chartStyle === 'line'}
            onclick={() => setChartStyle('line')}
          >
            Lines
          </button>
        </div>
      </div>
    </div>
    {#if showCompare}
      <div class="energy-compare" style="margin-bottom:10px">
        {#each devices as d (d.device_sn)}
          <button
            type="button"
            class="energy-compare-chip"
            class:on={compareSns.has(d.device_sn)}
            aria-pressed={compareSns.has(d.device_sn)}
            style={`--energy-chip-color:${chipColor(d.device_sn)}`}
            onclick={() => toggleCompare(d.device_sn)}
          >
            <i class="energy-compare-swatch"></i>
            {d.name || d.model_name || d.device_sn}
          </button>
        {/each}
        {#if compareSns.size > 1}
          <span class="hint energy-compare-hint"
            >Solid = consumed · dashed = charged · thin = battery %</span
          >
        {/if}
      </div>
    {/if}
    {#if error}
      <p class="hint" role="alert">{error}</p>
    {:else if busy}
      <p class="hint">Loading…</p>
    {/if}
    <canvas
      id="chart-energy"
      class="chart chart-tall"
      {@attach attachEnergyChart(historyPayload)}
    ></canvas>
  </section>

  <section class="card" hidden={!daily.length}>
    <div class="card-header">
      <h2>Daily breakdown</h2>
      <button class="btn btn-ghost" type="button" onclick={exportCsv} disabled={!daily.length}>
        Export CSV
      </button>
    </div>
    <div class="table-scroll">
      <table class="data-table daily-table">
        <thead>
          <tr>
            <th>Date</th>
            <th class="num">Solar kWh</th>
            <th class="num">Consumed kWh</th>
            <th class="num">Charged kWh</th>
            <th class="num">Grid kWh</th>
            {#if hasDivertedCol}
              <th class="num">Diverted kWh</th>
            {/if}
            <th class="num">Peak solar W</th>
            <th class="num">Peak output W</th>
            <th class="num">SOC min–max</th>
          </tr>
        </thead>
        <tbody>
          {#each daily as row (row.date)}
            <tr>
              <td title={row.date}>{fmtDayShort(row.date)}</td>
              <td class="num">{kwhCell(row.solar_kwh)}</td>
              <td class="num">{kwhCell(row.consumed_kwh)}</td>
              <td class="num">{kwhCell(row.charged_kwh)}</td>
              <td class="num">{kwhCell(row.grid_kwh)}</td>
              {#if hasDivertedCol}
                <td class="num">{kwhCell(divertedKwh(row))}</td>
              {/if}
              <td class="num">{row.peak_solar_w != null ? Math.round(row.peak_solar_w) : '—'}</td>
              <td class="num">{row.peak_output_w != null ? Math.round(row.peak_output_w) : '—'}</td>
              <td class="num">
                {row.min_soc != null || row.max_soc != null
                  ? `${row.min_soc ?? '—'}–${row.max_soc ?? '—'}`
                  : '—'}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
</section>
