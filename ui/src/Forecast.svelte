<script lang="ts">
  import { onMount } from 'svelte';
  import { feature } from './api';
  type Location = { latitude: number | null; longitude: number | null; label?: string; timezone?: string };
  type Place = { name: string; admin1?: string; country?: string; latitude: number; longitude: number; timezone?: string };
  type Hour = { ts: number; predicted_soc: number; solar_w: number; load_w: number; cloud_cover_pct: number };
  type Forecast = { configured: boolean; ready: boolean; error?: string; forecast: Hour[]; starting_soc_pct?: number; capacity_wh?: number; timezone?: string; utc_offset_seconds?: number; today_actual_solar_wh?: number; readiness?: { have_hours: number; needed_hours: number; low_confidence_overhead_fit: boolean }; weather_stale?: boolean; weather_synthetic?: boolean; weather_stale_age_s?: number; solar_coefficient?: number; fit_samples?: number; effective_parasitic_w?: number; charge_efficiency?: number; inverter_overhead_source?: string };
  type Summary = { n: number; mae: number; bias_pp: number };
  type Daily = { date: string; predicted_sunset_soc_pct: number | null; actual_sunset_soc_pct: number | null; predicted_sunrise_soc_pct: number | null; actual_sunrise_soc_pct: number | null };
  let devices = $state<{ device_sn: string; name: string }[]>([]);
  let selected = $state('');
  let location = $state<Location>({ latitude: null, longitude: null });
  let latitude = $state<number | undefined>();
  let longitude = $state<number | undefined>();
  let label = $state('');
  let query = $state('');
  let places = $state<Place[]>([]);
  let data = $state<Forecast | null>(null);
  let accuracy = $state<Record<string, Summary>>({});
  let daily = $state<Daily[]>([]);
  let busy = $state(false);
  let searching = $state(false);
  let saving = $state(false);
  let error = $state('');
  let notice = $state('');
  let editLocation = $state(false);
  let generation = 0;
  let searchGeneration = 0;
  let disposed = false;
  const hours = $derived(data?.forecast ?? []);
  const start = $derived(hours[0]?.ts ?? 0);
  const end = $derived(hours.at(-1)?.ts ?? start + 1);
  const maxPower = $derived(Math.max(1, ...hours.flatMap(h => [h.solar_w, h.load_w])));
  const days = $derived.by(() => {
    const result = new Map<string, { solar: number; minSOC: number; maxSOC: number }>();
    const date = (ts: number) => new Intl.DateTimeFormat('en-CA', { timeZone: data?.timezone || 'UTC' }).format(displayDate(ts));
    for (const h of hours) { const key = date(h.ts); const d = result.get(key) ?? { solar: 0, minSOC: 100, maxSOC: 0 }; d.solar += h.solar_w / 1000; d.minSOC = Math.min(d.minSOC, h.predicted_soc); d.maxSOC = Math.max(d.maxSOC, h.predicted_soc); result.set(key, d); }
    const today = result.get(date(Date.now() / 1000)); if (today) today.solar += (data?.today_actual_solar_wh ?? 0) / 1000;
    return [...result.entries()];
  });
  function line(key: 'predicted_soc' | 'solar_w' | 'load_w') { const peak = key === 'predicted_soc' ? 100 : maxPower; return hours.map(h => `${40 + (h.ts - start) / Math.max(1, end - start) * 940},${210 - h[key] / peak * 180}`).join(' '); }
  function displayDate(ts: number) { return new Date((ts + (data?.timezone ? 0 : data?.utc_offset_seconds ?? 0)) * 1000); }
  function stamp(ts: number) { return displayDate(ts).toLocaleString(undefined, { timeZone: data?.timezone || 'UTC', month: 'short', day: 'numeric', hour: 'numeric' }); }
  function pct(value: number | null) { return value == null ? '—' : `${value.toFixed(1)}%`; }
  async function load() {
    const id = ++generation; busy = true; error = ''; data = null; accuracy = {}; daily = [];
    try {
      const q = `device_sn=${encodeURIComponent(selected)}`;
      const [forecast, stats, summary] = await Promise.all([feature<Forecast>(`/forecast?${q}`), feature<{ summary_post_fix: Record<string, Summary> }>(`/forecast/accuracy?${q}`), feature<{ rows: Daily[] }>(`/daily_summary?${q}`)]);
      if (disposed || id !== generation) return;
      data = forecast; accuracy = stats.summary_post_fix; daily = summary.rows;
    } catch (err) { if (!disposed && id === generation) error = err instanceof Error ? err.message : 'Unable to load forecast'; }
    finally { if (!disposed && id === generation) busy = false; }
  }
  async function init() {
    try {
      const [loc, fleet] = await Promise.all([feature<Location>('/location'), feature<{ devices: { device_sn: string; name: string }[] }>('/energy/devices')]);
      if (disposed) return;
      location = loc; latitude = loc.latitude ?? undefined; longitude = loc.longitude ?? undefined; label = loc.label ?? ''; editLocation = loc.latitude == null;
      devices = fleet.devices; selected = devices[0]?.device_sn ?? ''; await load();
    } catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to load forecast'; }
  }
  async function search(event: SubmitEvent) {
    event.preventDefault(); const id = ++searchGeneration; searching = true; error = ''; places = [];
    try { const result = await feature<{ results: Place[]; error?: string }>(`/location/geocode?q=${encodeURIComponent(query)}`); if (disposed || id !== searchGeneration) return; places = result.results; notice = result.error || (places.length ? '' : 'No matching places found.'); }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Search failed'; }
    finally { if (!disposed && id === searchGeneration) searching = false; }
  }
  async function save(lat: number | undefined, lon: number | undefined, name: string, timezone?: string) {
    saving = true; error = ''; notice = '';
    try { const saved = await feature<Location>('/location', { latitude: lat, longitude: lon, label: name, timezone }); if (disposed) return; location = saved; latitude = saved.latitude ?? undefined; longitude = saved.longitude ?? undefined; label = saved.label ?? ''; places = []; editLocation = false; notice = 'Location saved.'; await load(); }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to save location'; }
    finally { if (!disposed) saving = false; }
  }
  function locate() {
    if (!navigator.geolocation) { error = 'Geolocation is unavailable. Search for a city or enter coordinates.'; return; }
    saving = true;
    navigator.geolocation.getCurrentPosition(p => { if (!disposed) void save(p.coords.latitude, p.coords.longitude, ''); }, e => { if (!disposed) { saving = false; error = `${e.message}. Search for a city or enter coordinates instead.`; } }, { timeout: 15000 });
  }
  onMount(() => { void init(); const timer = setInterval(() => { if (!busy && !saving) void load(); }, 300000); return () => { disposed = true; generation++; searchGeneration++; clearInterval(timer); }; });
</script>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if notice}<p role="status">{notice}</p>{/if}
<section class="controls"><label>Device<select bind:value={selected} onchange={load}>{#each devices as d}<option value={d.device_sn}>{d.name || d.device_sn}</option>{/each}</select></label><button disabled={busy || saving} onclick={load}>{busy ? 'Loading…' : 'Refresh forecast'}</button></section>
<section class="card">
  <div class="controls"><div><h3>Forecast location</h3><p class="muted">{location.label || (location.latitude == null ? 'Choose where your power station is installed.' : `${location.latitude.toFixed(3)}, ${location.longitude?.toFixed(3)}`)}</p></div><button onclick={() => editLocation = !editLocation}>{editLocation ? 'Close' : 'Change location'}</button></div>
  {#if editLocation}
    <p class="muted">Coordinates are encrypted at rest and sent to Open-Meteo for weather forecasts.</p>
    <form onsubmit={search} class="controls"><label>City or place<input bind:value={query} minlength="2" maxlength="200" placeholder="Search for a city" required /></label><button disabled={searching || saving}>{searching ? 'Searching…' : 'Search'}</button><button type="button" disabled={saving} onclick={locate}>Use my location</button></form>
    {#each places as p}<button class="place" disabled={saving} onclick={() => save(p.latitude, p.longitude, [p.name, p.admin1, p.country].filter(Boolean).join(', '), p.timezone)}>{[p.name, p.admin1, p.country].filter(Boolean).join(', ')}</button>{/each}
    <details><summary>Enter coordinates manually</summary><form onsubmit={e => { e.preventDefault(); void save(latitude, longitude, label); }}><div class="controls"><label>Latitude<input type="number" min="-90" max="90" step="any" bind:value={latitude} required /></label><label>Longitude<input type="number" min="-180" max="180" step="any" bind:value={longitude} required /></label><label>Label<input bind:value={label} maxlength="200" /></label></div><button disabled={saving}>Save location</button></form></details>
  {/if}
</section>
{#if data?.error}<p class="error" role="alert">{data.error}</p>{/if}
{#if data?.weather_stale}<p class="warning">{data.weather_synthetic ? 'Weather service unavailable. This estimate uses recent observed weather patterns.' : 'Weather service unavailable. Using the last saved forecast.'} Data age: {((data.weather_stale_age_s ?? 0) / 3600).toFixed(1)} hours.</p>{/if}
{#if data?.ready && hours.length}
  <section class="card"><h3>Battery outlook</h3><p class="muted">Starting system SOC {data.starting_soc_pct?.toFixed(1)}% · {((data.capacity_wh ?? 0) / 1000).toFixed(2)} kWh capacity · {data.timezone || `UTC offset ${(data.utc_offset_seconds ?? 0) / 3600}h`}</p>
    <svg viewBox="0 0 1000 250" role="img" aria-label="Predicted battery state of charge over five days">{#each [0, 25, 50, 75, 100] as value}<line x1="40" x2="980" y1={210 - value * 1.8} y2={210 - value * 1.8} stroke="#344050"/><text x="0" y={214 - value * 1.8} fill="#9aa7b8" font-size="12">{value}%</text>{/each}<polyline points={line('predicted_soc')} fill="none" stroke="#4ade80" stroke-width="3"/></svg>
    <div class="axis"><span>{stamp(start)}</span><span>{stamp(end)}</span></div>
    {#if data.readiness?.low_confidence_overhead_fit}<p class="muted">Battery drain is still calibrating; this forecast uses default overhead where history is insufficient.</p>{/if}
    <p class="muted">Solar and household demand projection. Automatic grid charging and diversion controls are still being migrated.</p>
  </section>
  <section class="day-grid" aria-label="Daily solar forecast">{#each days as [date, day]}<div class="card"><span class="eyebrow">{date}</span><strong>{day.solar.toFixed(1)} kWh</strong><span class="muted">SOC {day.minSOC.toFixed(0)}–{day.maxSOC.toFixed(0)}%</span></div>{/each}</section>
  <section class="card"><h3>Solar and demand</h3><svg viewBox="0 0 1000 230" role="img" aria-label="Expected solar and load in watts"><polyline points={line('solar_w')} fill="none" stroke="#38bdf8" stroke-width="3"/><polyline points={line('load_w')} fill="none" stroke="#fbbf24" stroke-width="3"/></svg><p class="muted"><span class="solar">● Solar</span> · <span class="load">● Load including battery losses</span> · scale {maxPower.toFixed(0)} W</p></section>
  <details class="card"><summary>Model details</summary><p>Solar coefficient: {data.solar_coefficient} · {data.fit_samples} paired samples</p><p>Battery baseline: {data.effective_parasitic_w} W · efficiency: {((data.charge_efficiency ?? 0) * 100).toFixed(0)}% · drain source: {data.inverter_overhead_source}</p></details>
{:else if data?.configured && !data.error}
  <section class="card"><h3>Learning your energy patterns</h3><p class="muted">{data.readiness?.have_hours ?? 0} of {data.readiness?.needed_hours ?? 24} hours captured. Forecasts appear after a full day of device history.</p></section>
{/if}
{#if Object.keys(accuracy).length}<section class="card"><h3>Forecast accuracy</h3><p class="muted">Current Go model · mean absolute error in SOC percentage points</p><div class="table-scroll"><table><thead><tr><th>Lead time</th><th>Samples</th><th>MAE</th><th>Bias</th></tr></thead><tbody>{#each Object.entries(accuracy) as [key, b]}<tr><th>{key}</th><td>{b.n}</td><td>{b.mae.toFixed(2)}</td><td>{b.bias_pp.toFixed(2)}</td></tr>{/each}</tbody></table></div></section>{/if}
{#if daily.length}<section class="card"><h3>Sunset and sunrise</h3><p class="muted">First saved daily prediction versus observed system SOC. Includes imported history.</p><div class="table-scroll"><table><thead><tr><th>Date</th><th>Sunset predicted</th><th>Sunset actual</th><th>Next sunrise predicted</th><th>Next sunrise actual</th></tr></thead><tbody>{#each daily as d}<tr><th>{d.date}</th><td>{pct(d.predicted_sunset_soc_pct)}</td><td>{pct(d.actual_sunset_soc_pct)}</td><td>{pct(d.predicted_sunrise_soc_pct)}</td><td>{pct(d.actual_sunrise_soc_pct)}</td></tr>{/each}</tbody></table></div></section>{/if}
<p class="muted">Weather data by <a href="https://open-meteo.com/" target="_blank" rel="noreferrer">Open-Meteo</a>.</p>
<style>
  .controls{display:flex;align-items:end;justify-content:space-between;gap:16px;flex-wrap:wrap}.controls label{flex:1;min-width:140px}.place{display:block;width:100%;text-align:left;margin:8px 0}.axis{display:flex;justify-content:space-between;color:#9aa7b8;font-size:12px}.day-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:12px}.day-grid strong{display:block;font-size:24px;margin:14px 0}.day-grid .card{padding:18px}svg{width:100%}.solar{color:#38bdf8}.load{color:#fbbf24}details{margin-top:16px}summary{cursor:pointer}
</style>
