<script lang="ts">
  import { onMount } from 'svelte';
  import { feature, pb } from './api';
  import type { Snapshot } from './live';

  let snapshot = $state<Snapshot | null>(null);
  let view = $state('');
  let connected = $state(false);
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);
  let socket: WebSocket | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let retryDelay = 1000;
  let requestID = 0;
  let polling = false;
  const ports = ['ac', 'dc', 'usb', 'car'] as const;
  const telemetry = $derived(snapshot?.telemetry);
  const paused = $derived((snapshot?.cloud.pause_remaining_s ?? 0) > 0);
  const stale = $derived((snapshot?.cloud.http_age_s ?? Infinity) > 180);
  const history = $derived(snapshot?.history ?? []);
  const peak = $derived(Math.max(100, ...history.flatMap(p => [p.solar_input_w, p.output_power_w])));
  function points(key: 'solar_input_w' | 'output_power_w') {
    return history.map((p, i) => `${(i / Math.max(1, history.length - 1)) * 1000},${160 - (p[key] / peak) * 145}`).join(' ');
  }
  function watts(value: number | null | undefined) { return value == null ? '—' : `${Math.round(value).toLocaleString()} W`; }
  function receive(data: Snapshot) {
    if (view && data.cloud.selected_device_id !== view) {
      // A removed device falls back to the runtime's first current device.
      if (data.cloud.devices_overview.some(d => d.device_id === view)) return;
      view = data.cloud.selected_device_id;
    }
    snapshot = data;
  }
  async function refresh() {
    const id = ++requestID;
    try {
      const data = await feature<Snapshot>(`/status?view_device_id=${encodeURIComponent(view)}`);
      if (!stopped && id === requestID) { receive(data); error = ''; }
    } catch (err) { if (!stopped && id === requestID) error = err instanceof Error ? err.message : 'Unable to load live status'; }
  }
  function connect() {
    if (stopped || !pb.authStore.isValid) return;
    const url = new URL('/ws', window.location.origin);
    url.protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    url.searchParams.set('token', pb.authStore.token);
    if (view) url.searchParams.set('view_device_id', view);
    const current = new WebSocket(url);
    socket = current;
    current.onopen = () => { if (socket !== current) return; connected = true; retryDelay = 1000; };
    current.onmessage = event => {
      if (socket !== current || stopped) return;
      try {
        const message = JSON.parse(event.data);
        if (message.type === 'snapshot' || message.type === 'status') { receive(message.data); error = ''; }
      } catch { error = 'Received an invalid live update'; }
    };
    current.onclose = () => {
      if (socket !== current || stopped) return;
      connected = false;
      void refresh(); // Also detects expired/revoked tokens and signs out.
      retry = setTimeout(connect, retryDelay);
      retryDelay = Math.min(30000, retryDelay * 2);
    };
  }
  function selectDevice(id: string) {
    view = id; notice = ''; error = ''; snapshot = null; requestID++;
    if (retry) clearTimeout(retry);
    const old = socket; socket = undefined; old?.close(); connected = false;
    void refresh(); connect();
  }
  onMount(() => {
    stopped = false;
    void refresh(); connect();
    const fallback = setInterval(async () => {
      if (!connected && !polling) { polling = true; try { await refresh(); } finally { polling = false; } }
    }, 5000);
    return () => { stopped = true; requestID++; clearInterval(fallback); if (retry) clearTimeout(retry); socket?.close(); };
  });
  async function toggle(port: typeof ports[number]) {
    if (!snapshot?.device || !telemetry) return;
    busy = true; error = ''; notice = '';
    try {
      await feature('/set_output', { device_sn: snapshot.device.device_sn, port, on: !telemetry[`${port}_on`] });
      notice = snapshot.mock_mode ? 'Mock output updated.' : 'Command accepted by the broker. Waiting for device confirmation.';
      await refresh();
    } catch (err) { error = err instanceof Error ? err.message : 'Command failed'; }
    finally { busy = false; }
  }
  async function control(action: 'pause_polling' | 'resume_polling' | 'reconnect') {
    busy = true; error = ''; notice = '';
    try { await feature(`/${action}`, action === 'pause_polling' ? { seconds: 600 } : {}); await refresh(); }
    catch (err) { error = err instanceof Error ? err.message : 'Request failed'; }
    finally { busy = false; }
  }
</script>

<section class="live-top" aria-label="Connection status">
  <div><span class:online={snapshot?.connection_status === 'connected'} class="status-dot"></span>{snapshot?.connection_status ?? 'Connecting'}
    <span class="muted"> · {snapshot?.mock_mode ? 'Simulated devices' : connected ? 'Live stream connected' : 'Reconnecting live stream'}</span>
  </div>
  <div class="controls"><button disabled={busy} onclick={() => control(paused ? 'resume_polling' : 'pause_polling')}>{paused ? 'Resume polling' : 'Pause for 10 min'}</button><button disabled={busy} onclick={() => control('reconnect')}>Refresh now</button></div>
</section>
{#if snapshot?.energy_error}<p class="error" role="alert">Energy recording: {snapshot.energy_error}</p>{/if}
{#if snapshot?.battery_packs_error}<p class="error" role="status">Battery packs: {snapshot.battery_packs_error}</p>{/if}
{#if snapshot?.battery_packs_ts && Date.now() / 1000 - snapshot.battery_packs_ts > 1800}<p class="error">System SOC includes a cached pack report older than 30 minutes.</p>{/if}
{#if error}<p role="alert" class="error">{error}</p>{/if}
{#if notice}<p role="status" class="success">{notice}</p>{/if}
{#if snapshot?.connection_error}<p class="error" role="status">{snapshot.connection_error}</p>{/if}
{#if paused}<p class="muted">HTTP polling paused for {Math.ceil(snapshot?.cloud.pause_remaining_s ?? 0)} seconds. MQTT updates may continue.</p>{/if}
{#if snapshot?.cloud.contested_remaining_s}<p class="muted">Another client claimed the cloud session. Retrying in {Math.ceil(snapshot.cloud.contested_remaining_s)} seconds.</p>{/if}
{#if snapshot?.cloud.mqtt_error}<p class="error" role="status">{snapshot.cloud.mqtt_error}. HTTP monitoring continues.</p>{/if}
{#if snapshot?.cloud.devices_overview.length}
  <div class="fleet" aria-label="Select a Jackery device">
    {#each snapshot.cloud.devices_overview as device (device.device_id)}
      <button class:selected={snapshot.cloud.selected_device_id === device.device_id} disabled={busy} onclick={() => selectDevice(device.device_id)} aria-pressed={snapshot.cloud.selected_device_id === device.device_id}>
        <strong>{device.name}</strong><span class="soc">{device.soc_pct == null ? '—' : `${Math.round(device.soc_pct * 10) / 10}%`}</span><small>Solar {watts(device.solar_w)} · Load {watts(device.output_w)}</small>
      </button>
    {/each}
  </div>
{/if}
{#if telemetry && snapshot?.device}
  {#if stale}<p class="error" role="status">Battery percentage and port states are stale. Last HTTP update: {snapshot.cloud.http_age_s == null ? 'not yet received' : `${Math.round(snapshot.cloud.http_age_s)} seconds ago`}.</p>{/if}
  <section class="card"><div class="section-heading"><h3>{snapshot.device.name}</h3><small>{snapshot.device.device_sn}</small></div>
    <div class="flow">
      <div class="flow-node input"><span class="eyebrow">SOLAR</span><strong>{watts(telemetry.solar_input_w)}</strong><small>Grid {watts(telemetry.ac_input_w)} · Car {watts(telemetry.car_input_w)}</small></div>
      <span class="flow-arrow" aria-hidden="true">→</span>
      <div class="flow-node battery"><span class="eyebrow">SYSTEM BATTERY</span><strong>{Math.round((telemetry.system_soc_pct ?? telemetry.battery_percent) * 10) / 10}%</strong><meter min="0" max="100" value={telemetry.system_soc_pct ?? telemetry.battery_percent} aria-label="Battery percentage"></meter><small>{telemetry.battery_temp_c} °C · {watts(telemetry.input_power_w - telemetry.output_power_w)} net</small></div>
      <span class="flow-arrow" aria-hidden="true">→</span>
      <div class="flow-node output"><span class="eyebrow">LOAD</span><strong>{watts(telemetry.output_power_w)}</strong><small>{telemetry.time_remaining_h ? `${telemetry.time_remaining_h} h remaining` : telemetry.time_to_full_h ? `${telemetry.time_to_full_h} h to full` : 'No time estimate'}</small></div>
    </div>
  </section>
  <section class="card"><h3>Outputs</h3><div class="outputs">{#each ports as port}<button class:enabled={telemetry[`${port}_on`]} disabled={busy || paused || (!snapshot.mock_mode && !snapshot.cloud.mqtt_connected)} aria-pressed={telemetry[`${port}_on`]} onclick={() => toggle(port)}><span>{port.toUpperCase()}</span><strong>{telemetry[`${port}_on`] ? 'ON' : 'OFF'}</strong></button>{/each}</div></section>
  <section class="card"><div class="section-heading"><h3>Recent power</h3><small>Last 6 hours · persisted history</small></div>
    {#if history.length > 1}<svg viewBox="0 0 1000 180" role="img" aria-label="Recent solar input and output power"><line x1="0" y1="160" x2="1000" y2="160" stroke="#344050"/><polyline points={points('solar_input_w')} fill="none" stroke="#38bdf8" stroke-width="3"/><polyline points={points('output_power_w')} fill="none" stroke="#4ade80" stroke-width="3"/></svg><div class="chart-legend"><span>● Solar</span><span>● Load</span><small>Scale: {watts(peak)}</small></div>
    {:else}<p class="muted">Collecting power history. The chart updates every 30 seconds.</p>{/if}
  </section>
{:else if snapshot?.connection_status === 'needs-credentials'}
  <section class="card empty"><h3>Connect your Jackery account</h3><p class="muted">Open Settings to securely save your Jackery cloud email and password.</p></section>
{:else}<section class="card empty"><h3>{snapshot?.connection_status === 'connected' ? 'No devices found' : 'Waiting for device telemetry'}</h3><p class="muted">{snapshot?.connection_status === 'connected' ? 'No supported bound devices were returned by this account.' : 'Live readings will appear when the cloud connection is ready.'}</p></section>{/if}

<style>
  .live-top,.controls,.section-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.status-dot{display:inline-block;width:8px;height:8px;background:#fbbf24;border-radius:50%;margin-right:8px}.status-dot.online{background:#4ade80}.fleet{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:14px}.fleet button{text-align:left;padding:20px;display:grid;gap:8px;background:#14181d}.fleet button.selected{border-color:#4ade80;background:#4ade800a}.soc{font-size:28px;color:#fbbf24}.flow{display:grid;grid-template-columns:1fr 24px 1fr 24px 1fr;align-items:center;gap:12px;padding:24px 0}.flow-node{text-align:center;display:grid;gap:12px}.flow-node strong{font-size:clamp(24px,4vw,40px)}.input strong{color:#38bdf8}.output strong{color:#4ade80}.battery strong{color:#fbbf24}meter{width:90%;margin:auto;height:14px;accent-color:#fbbf24}.flow-arrow{color:#6b7280;font-size:24px}.outputs{display:grid;grid-template-columns:repeat(4,1fr);gap:12px}.outputs button{display:flex;justify-content:space-between;gap:12px;padding:18px}.outputs button.enabled{color:#4ade80;border-color:#4ade8066}.section-heading h3{margin:0}svg{width:100%;margin-top:24px}.chart-legend{display:flex;gap:20px;font-size:12px}.chart-legend span:first-child{color:#38bdf8}.chart-legend span:nth-child(2){color:#4ade80}.chart-legend small{margin-left:auto}@media(max-width:600px){.flow{grid-template-columns:1fr}.flow-arrow{text-align:center;transform:rotate(90deg)}.outputs{grid-template-columns:repeat(2,1fr)}.controls{width:100%}.controls button{flex:1}}
</style>
