<script lang="ts">
  import { onMount } from 'svelte';
  import { feature } from './api';
  type Device = { device_sn: string; name: string; model_code: number; model_recognized: boolean; default_capacity_wh: number; capacity_wh_override: number | null; auto_capacity_wh: number | null; pack_count: number; effective_capacity_wh: number };
  type Pack = { deviceSn: string; deviceOrder: number; rb: number | null; ip: number | null; op: number | null; it: number | null; ec: number | null };
  let devices = $state<Device[]>([]);
  let selected = $state('');
  let capacity = $state<number | undefined>();
  let packs = $state<Pack[]>([]);
  let packTime = $state(0);
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);
  let disposed = false;
  let requestID = 0;
  const device = $derived(devices.find(d => d.device_sn === selected));
  async function select() {
    capacity = device?.capacity_wh_override ?? undefined; const id = ++requestID;
    try { const response = await feature<{ packs: Pack[]; fetched_at: number }>(`/devices/battery_packs?device_sn=${encodeURIComponent(selected)}`); if (disposed || id !== requestID) return; packs = response.packs; packTime = response.fetched_at; }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to read battery packs'; }
  }
  async function load() {
    busy = true; error = '';
    try { const response = await feature<{ devices: Device[] }>('/devices/capacity'); if (disposed) return; devices = response.devices; if (!devices.some(d => d.device_sn === selected)) selected = devices[0]?.device_sn ?? ''; if (selected) await select(); }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to load devices'; }
    finally { if (!disposed) busy = false; }
  }
  onMount(() => { void load(); return () => { disposed = true; requestID++; }; });
  async function save(value: number | null) {
    busy = true; error = ''; notice = '';
    try { await feature('/devices/capacity', { device_sn: selected, capacity_wh: value }); await load(); notice = value == null ? 'Using the model and pack capacity.' : 'Capacity override saved.'; }
    catch (err) { error = err instanceof Error ? err.message : 'Unable to save capacity'; }
    finally { busy = false; }
  }
</script>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if notice}<p class="success" role="status">{notice}</p>{/if}
<div class="device-controls"><label>Device<select bind:value={selected} onchange={select}>{#each devices as d}<option value={d.device_sn}>{d.name || d.device_sn}</option>{/each}</select></label><button disabled={busy} onclick={load}>{busy ? 'Loading…' : 'Refresh'}</button></div>
{#if device}
  <section class="card"><h3>{device.name || device.device_sn}</h3><dl><dt>Serial number</dt><dd>{device.device_sn}</dd><dt>Model code</dt><dd>{device.model_code}</dd><dt>Main capacity</dt><dd>{device.default_capacity_wh.toLocaleString()} Wh</dd><dt>Expansion packs</dt><dd>{device.pack_count}</dd><dt>Effective system capacity</dt><dd>{device.effective_capacity_wh.toLocaleString()} Wh</dd></dl>
    {#if !device.model_recognized}<p class="error">This model is not in the capacity catalog. A fallback is in use; enter its total capacity below if known.</p>{/if}
    <form onsubmit={(event) => { event.preventDefault(); void save(capacity ?? null); }}><label>Total capacity override (Wh)<input type="number" min="500" max="200000" step="1" bind:value={capacity} placeholder="Use model + expansion packs" /></label><div class="actions"><button type="submit" class="primary" disabled={busy}>Save capacity</button><button type="button" disabled={busy} onclick={() => save(null)}>Use automatic capacity</button></div></form>
  </section>
  <section class="card"><h3>Expansion batteries</h3>
    {#if packTime}<p class="muted">Last reported: {new Date(packTime * 1000).toLocaleString()}</p>{/if}
    {#if packTime && Date.now() / 1000 - packTime > 1800}<p class="error">This is a cached pack snapshot older than 30 minutes.</p>{/if}
    {#if packs.length}<div class="pack-grid">{#each packs as pack (pack.deviceSn)}<div class="pack"><h4>Pack {pack.deviceOrder}</h4><strong>{pack.rb == null ? '—' : `${pack.rb}%`}</strong><small>{pack.deviceSn}</small><p class="muted">Input {pack.ip ?? '—'} W · Output {pack.op ?? '—'} W</p><small>Reported temperature {pack.it ?? '—'} °C · Error {pack.ec ?? '—'}</small></div>{/each}</div>
    {:else}<p class="muted">{packTime ? 'No expansion packs in the latest report.' : 'No battery-pack report has been received yet.'}</p>{/if}
  </section>
{:else if !busy}<section class="card empty"><h3>No recorded devices yet</h3><p class="muted">Connected and imported devices appear here after their first reading.</p></section>{/if}
<style>
  .device-controls{display:flex;align-items:end;gap:16px}.device-controls label{flex:1}dl{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin:24px 0}dt{color:#98a2b3}dd{margin:0;text-align:right}form{margin-top:28px}.actions{display:flex;gap:12px;flex-wrap:wrap}.pack-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:16px}.pack{border:1px solid #232a33;border-radius:10px;padding:18px}.pack h4{margin:0 0 12px}.pack strong{display:block;font-size:32px;color:#fbbf24}
</style>
