<script lang="ts">
  import { onMount } from 'svelte';
  import { feature } from './api';
  type Window = { input_wh: number; output_wh: number; solar_wh: number; ac_input_wh: number };
  type Totals = { device_sn: string; name?: string; lifetime: Window; today: Window; last_7d: Window; last_30d: Window };
  type Point = { ts: number; input_wh: number; output_wh: number; solar_wh: number; battery_pct: number | null; system_soc?: number | null };
  type Day = { date: string; solar_kwh: number; consumed_kwh: number; charged_kwh: number; grid_kwh: number; min_soc: number | null; max_soc: number | null };
  type Savings = { net_savings: number; solar_savings: number; grid_cost: number; currency: string };
  let savings = $state<{ today_savings: Savings; lifetime_savings: Savings } | null>(null);
  let devices = $state<Totals[]>([]);
  let selected = $state('');
  let compare = $state('');
  let hours = $state(24);
  let totals = $state<Totals | null>(null);
  let points = $state<Point[]>([]);
  let comparison = $state<Point[]>([]);
  let days = $state<Day[]>([]);
  let error = $state('');
  let busy = $state(false);
  let requestID = 0;
  let disposed = false;
  const peak = $derived(Math.max(1, ...points.flatMap(p => [p.input_wh, p.output_wh, p.solar_wh]), ...comparison.map(p => p.output_wh)));
  const start = $derived(Math.min(...points.map(p => p.ts), ...comparison.map(p => p.ts)));
  const end = $derived(Math.max(...points.map(p => p.ts), ...comparison.map(p => p.ts)));
  const bestSolar = $derived(days.length ? Math.max(...days.map(d => d.solar_kwh)) : 0);
  function kwh(wh: number | undefined) { return ((wh ?? 0) / 1000).toFixed(2); }
  function line(data: Point[], key: 'solar_wh' | 'output_wh' | 'input_wh') {
    return data.map(p => `${((p.ts - start) / Math.max(1, end - start)) * 1000},${180 - (p[key] / peak) * 160}`).join(' ');
  }
  async function load() {
    if (!selected) return;
    const id = ++requestID; busy = true; error = '';
    const query = `device_sn=${encodeURIComponent(selected)}`;
    try {
      const [nextTotals, history, daily, other, costs] = await Promise.all([
        feature<Totals>(`/energy/totals?${query}`),
        feature<{ history: Point[] }>(`/energy/history?${query}&hours=${hours}`),
        feature<{ daily: Day[] }>(`/energy/daily?${query}&days=30`),
        compare ? feature<{ history: Point[] }>(`/energy/history?device_sn=${encodeURIComponent(compare)}&hours=${hours}`) : Promise.resolve({ history: [] }),
        feature<{ today_savings: Savings; lifetime_savings: Savings }>(`/cost/savings?${query}`),
      ]);
      if (disposed || id !== requestID) return;
      savings = costs; totals = nextTotals; points = history.history; days = daily.daily; comparison = other.history;
    } catch (err) { if (!disposed && id === requestID) error = err instanceof Error ? err.message : 'Unable to load energy'; }
    finally { if (!disposed && id === requestID) busy = false; }
  }
  async function init() {
    try { const result = await feature<{ devices: Totals[] }>('/energy/devices'); if (disposed) return; devices = result.devices; selected = devices[0]?.device_sn ?? ''; await load(); }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to load devices'; }
  }
  onMount(() => { void init(); const timer = setInterval(() => { if (!busy) void (selected ? load() : init()); }, 30000); return () => { disposed = true; requestID++; clearInterval(timer); }; });
</script>

{#if error}<p class="error" role="alert">{error}</p>{/if}
<section class="energy-controls">
  <label>Device<select bind:value={selected} onchange={() => { compare = ''; void load(); }}>{#each devices as d}<option value={d.device_sn}>{d.name || d.device_sn}</option>{/each}</select></label>
  <label>History<select bind:value={hours} onchange={load}><option value={24}>24 hours</option><option value={168}>7 days</option><option value={720}>30 days</option></select></label>
  {#if devices.length > 1}<label>Compare consumption<select bind:value={compare} onchange={load}><option value="">None</option>{#each devices.filter(d => d.device_sn !== selected) as d}<option value={d.device_sn}>{d.name || d.device_sn}</option>{/each}</select></label>{/if}
  <button disabled={busy} onclick={selected ? load : init}>{busy ? 'Loading…' : 'Refresh'}</button>
</section>
{#if totals}
  <section class="energy-kpis" aria-label="Today’s energy">
    <div class="card"><span class="eyebrow">SOLAR TODAY</span><strong class="solar">{kwh(totals.today.solar_wh)} <small>kWh</small></strong></div>
    <div class="card"><span class="eyebrow">CONSUMED TODAY</span><strong class="load">{kwh(totals.today.output_wh)} <small>kWh</small></strong></div>
    <div class="card"><span class="eyebrow">CHARGED TODAY</span><strong>{kwh(totals.today.input_wh)} <small>kWh</small></strong></div>
    <div class="card"><span class="eyebrow">GRID TODAY</span><strong>{kwh(totals.today.ac_input_wh)} <small>kWh</small></strong></div>
  </section>
  <section class="card"><h3>Energy history</h3><p class="muted">Energy per time bucket · durable history across restarts</p>
    {#if points.length > 1}<svg viewBox="0 0 1000 200" role="img" aria-label="Solar, charged, and consumed energy over time"><line x1="0" y1="180" x2="1000" y2="180" stroke="#344050"/><polyline points={line(points, 'solar_wh')} fill="none" stroke="#38bdf8" stroke-width="3"/><polyline points={line(points, 'output_wh')} fill="none" stroke="#4ade80" stroke-width="3"/><polyline points={line(points, 'input_wh')} fill="none" stroke="#fbbf24" stroke-width="2"/>{#if comparison.length}<polyline points={line(comparison, 'output_wh')} fill="none" stroke="#c084fc" stroke-width="3" stroke-dasharray="8 5"/>{/if}</svg><div class="legend"><span class="solar">● Solar</span><span class="load">● Consumed</span><span class="charge">● Charged</span>{#if comparison.length}<span class="compare">● Comparison consumed</span>{/if}</div><p class="muted">{new Date(start * 1000).toLocaleString()} — {new Date(end * 1000).toLocaleString()} · scale {kwh(peak)} kWh</p>
    {:else}<p class="muted">Collecting energy samples. The chart needs two time buckets.</p>{/if}
  </section>
  {#if savings}<section class="card"><h3>Electricity savings</h3><p>Today: <strong>{savings.today_savings.net_savings.toFixed(2)} {savings.today_savings.currency}</strong> net · grid charging cost {savings.today_savings.grid_cost.toFixed(2)}</p><p>Lifetime: <strong>{savings.lifetime_savings.net_savings.toFixed(2)} {savings.lifetime_savings.currency}</strong> net</p><p class="muted">Based on your current electricity plan in Settings and installation timezone in Forecast.</p></section>{/if}
  <section class="card"><h3>Totals</h3><div class="table-scroll"><table><thead><tr><th>Period</th><th>Solar kWh</th><th>Consumed kWh</th><th>Charged kWh</th><th>Grid kWh</th></tr></thead><tbody>{#each [['Last 7 days', totals.last_7d], ['Last 30 days', totals.last_30d], ['Lifetime', totals.lifetime]] as [label, window]}{@const w = window as Window}<tr><th>{label}</th><td>{kwh(w.solar_wh)}</td><td>{kwh(w.output_wh)}</td><td>{kwh(w.input_wh)}</td><td>{kwh(w.ac_input_wh)}</td></tr>{/each}</tbody></table></div></section>
  <section class="card"><h3>Daily energy</h3><p class="muted">Last 30 days · best solar day: {bestSolar.toFixed(2)} kWh</p><div class="table-scroll"><table><thead><tr><th>Date</th><th>Solar kWh</th><th>Consumed kWh</th><th>Charged kWh</th><th>Grid kWh</th><th>Main SOC range</th></tr></thead><tbody>{#each [...days].reverse() as day}<tr><th>{day.date}</th><td>{day.solar_kwh.toFixed(2)}</td><td>{day.consumed_kwh.toFixed(2)}</td><td>{day.charged_kwh.toFixed(2)}</td><td>{day.grid_kwh.toFixed(2)}</td><td>{day.min_soc ?? '—'}–{day.max_soc ?? '—'}%</td></tr>{/each}</tbody></table></div></section>
{:else if !busy}<section class="card empty"><h3>No energy history yet</h3><p class="muted">Connect a device or import an existing energy database. Recorded devices will appear here.</p></section>{/if}
<style>
  .energy-controls{display:flex;align-items:end;gap:16px;flex-wrap:wrap}.energy-controls label{flex:1;min-width:140px}.energy-kpis{display:grid;grid-template-columns:repeat(4,1fr);gap:14px}.energy-kpis .card{padding:20px}.energy-kpis strong{display:block;font-size:30px;margin-top:12px}.energy-kpis small{display:inline;font-size:13px}.solar{color:#38bdf8}.load{color:#4ade80}.charge{color:#fbbf24}.compare{color:#c084fc}svg{width:100%}.legend{display:flex;gap:16px;flex-wrap:wrap;font-size:12px}@media(max-width:750px){.energy-kpis{grid-template-columns:repeat(2,1fr)}}
</style>
