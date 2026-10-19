<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost, ApiError } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { formatDeviceErrorCode, fmtSecsMMSS } from '$lib/format';
  import { saveCloudCredentials, forgetCloudCredentials, cloudAuthStatus } from '$lib/auth';

  type DeviceParam = {
    key: string;
    value?: number | null;
    source?: string;
    unit?: string;
    label?: string;
    n_samples?: number;
    note?: string;
  };

  type EcoflowDevice = {
    sn?: string;
    raw_sn?: string;
    alias?: string;
    device_type?: string;
  };

  type BmsPack = {
    mac?: string;
    alias?: string;
    capacity_wh?: number | null;
    online?: boolean;
    soc_pct?: number | null;
  };

  let capacityWh = $state('');
  let capacityDefault = $state<number | null>(null);
  let capacityMsg = $state('');
  let email = $state('');
  let password = $state('');
  let region = $state('US');
  let cloudHasCreds = $state(false);
  let accountMsg = $state('');
  let pauseSeconds = $state('600');
  let pauseBusy = $state(false);
  let busy = $state(false);

  let alias = $state('');
  let aliasMsg = $state('');

  let siseliUser = $state('');
  let siseliPass = $state('');
  let siseliStation = $state('');
  let siseliTz = $state('');
  let siseliStatus = $state('—');
  let siseliMsg = $state('');

  let ecoflowEmail = $state('');
  let ecoflowPass = $state('');
  let ecoflowStatus = $state('—');
  let ecoflowMsg = $state('');
  let ecoflowDevices = $state.raw<EcoflowDevice[]>([]);

  let f7Enabled = $state(false);
  let f7Cooldown = $state(30);
  let f7Pulse = $state(5);
  let f7Msg = $state('');
  let f7Daylight = $state(false);

  let watchdogEnabled = $state(false);
  let watchdogStatus = $state('');
  let watchdogBusy = $state(false);
  let watchdogApplicable = $state(false);

  let params = $state.raw<DeviceParam[]>([]);
  let paramsMsg = $state('');

  let bmsPacks = $state.raw<BmsPack[]>([]);
  let bmsScan = $state.raw<{ mac?: string; name?: string; likely_jbd?: boolean }[]>([]);
  let bmsMsg = $state('');
  let bmsBleOk = $state(true);

  let lanRead = $state(false);
  let lanHybrid = $state(false);
  let lanInverterIp = $state('');
  let lanRouterIp = $state('');
  let lanIface = $state('');
  let lanMqttBroker = $state('');
  let lanMsg = $state('');

  const device = $derived(live.status?.device);
  const t = $derived(live.status?.telemetry);
  const cloud = $derived(live.status?.cloud);
  const pauseUntil = $derived(cloud?.pause_until ?? null);
  const isPaused = $derived(pauseUntil != null && Number(pauseUntil) > Date.now() / 1000);
  const pauseLeft = $derived(cloud?.pause_remaining_s ?? null);
  const deviceId = $derived(
    live.selectedDeviceId ||
      live.status?.cloud?.selected_device_id ||
      device?.device_sn ||
      '',
  );

  const upsLabel = $derived.by(() => {
    const parts = [t?.ups_on && 'UPS', t?.super_charge_on && 'Super charge'].filter(Boolean);
    if (parts.length) return parts.join(' + ');
    if (t?.ups_on === false || t?.super_charge_on === false) return 'Off';
    return '—';
  });

  function snQ() {
    const sn = live.activeDeviceSn();
    return sn ? `?device_sn=${encodeURIComponent(sn)}` : '';
  }

  async function loadCapacity() {
    try {
      const j = await api<{
        capacity_wh?: number | null;
        default_wh?: number | null;
        override_wh?: number | null;
      }>('/devices/capacity');
      capacityDefault = j.default_wh ?? null;
      const ov = j.override_wh ?? j.capacity_wh;
      capacityWh = ov != null ? String(ov) : '';
    } catch {
      /* ignore */
    }
  }

  async function loadCloud() {
    try {
      const j = await cloudAuthStatus();
      cloudHasCreds = !!j.has_credentials;
    } catch {
      cloudHasCreds = false;
    }
  }

  async function loadAlias() {
    alias = live.status?.device_prefs?.alias || '';
  }

  async function loadSiseli() {
    try {
      const j = await api<{
        has_credentials?: boolean;
        user_id?: string;
        station_id?: string;
        time_zone?: string;
        local_read?: boolean;
        hybrid_pull?: boolean;
        inverter_ip?: string;
        router_ip?: string;
        sniff_iface?: string;
        mqtt_broker_ip?: string;
        local_running?: boolean;
        local_error?: string | null;
      }>('/siseli/credentials');
      siseliStatus = j.has_credentials
        ? `saved${j.user_id ? ` · ${j.user_id}` : ''}${j.station_id ? ` · station ${j.station_id}` : ''}`
        : 'no credentials';
      if (j.user_id) siseliUser = j.user_id;
      if (j.station_id) siseliStation = j.station_id;
      if (j.time_zone) siseliTz = j.time_zone;
      lanRead = !!j.local_read;
      lanHybrid = !!j.hybrid_pull;
      lanInverterIp = j.inverter_ip || '';
      lanRouterIp = j.router_ip || '';
      lanIface = j.sniff_iface || '';
      lanMqttBroker = j.mqtt_broker_ip || '';
      if (j.local_error) lanMsg = j.local_error;
      else if (j.local_running) lanMsg = 'LAN read running';
      else if (j.local_read) lanMsg = 'LAN read enabled';
    } catch (err) {
      siseliStatus = err instanceof Error ? err.message : 'unavailable';
    }
  }

  async function loadEcoflow() {
    try {
      const [creds, devices] = await Promise.all([
        api<{ has_credentials?: boolean; email?: string }>('/ecoflow/credentials'),
        api<{ devices?: EcoflowDevice[] }>('/ecoflow/devices').catch(() => ({ devices: [] })),
      ]);
      ecoflowStatus = creds.has_credentials
        ? `saved${creds.email ? ` · ${creds.email}` : ''}`
        : 'no credentials';
      if (creds.email) ecoflowEmail = creds.email;
      ecoflowDevices = devices.devices || [];
    } catch (err) {
      ecoflowStatus = err instanceof Error ? err.message : 'unavailable';
    }
  }

  async function loadF7() {
    try {
      const j = await api<{
        config?: { enabled?: boolean; cooldown_min?: number; pulse_s?: number };
        in_daylight?: boolean;
      }>(`/f7_ac_reset/config${snQ()}`);
      const cfg = j.config || {};
      f7Enabled = !!cfg.enabled;
      f7Cooldown = Number(cfg.cooldown_min ?? 30);
      f7Pulse = Number(cfg.pulse_s ?? 5);
      f7Daylight = !!j.in_daylight;
    } catch (err) {
      f7Msg = err instanceof Error ? err.message : 'F7 load failed';
    }
  }

  function watchdogStatusText(source?: string) {
    return source === 'user' ? 'Saved for this device' : 'Default for this model';
  }

  async function loadWatchdogConfig() {
    try {
      const j = await api<{
        enabled?: boolean;
        source?: 'user' | 'default';
      }>(`/inverter_watchdog/config${snQ()}`);
      watchdogApplicable = true;
      watchdogEnabled = !!j.enabled;
      watchdogStatus = watchdogStatusText(j.source);
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      const msg = err instanceof Error ? err.message : '';
      if (
        status === 400 ||
        status === 404 ||
        /Jackery devices only|AC auto recovery|no active device/i.test(msg)
      ) {
        watchdogApplicable = false;
        watchdogEnabled = false;
        watchdogStatus = '';
        return;
      }
      watchdogApplicable = false;
      watchdogEnabled = false;
      watchdogStatus = msg || 'Watchdog load failed';
    }
  }

  async function saveWatchdogConfig() {
    const sn = live.activeDeviceSn();
    if (!sn) {
      watchdogStatus = 'No active device.';
      return;
    }
    watchdogBusy = true;
    try {
      const j = await api<{
        enabled?: boolean;
        source?: 'user' | 'default';
      }>('/inverter_watchdog/config', {
        method: 'PUT',
        body: JSON.stringify({ device_sn: sn, enabled: watchdogEnabled }),
      });
      watchdogEnabled = !!j.enabled;
      watchdogStatus = watchdogStatusText(j.source);
    } catch (err) {
      watchdogStatus = err instanceof Error ? err.message : 'Save failed';
      await loadWatchdogConfig();
    } finally {
      watchdogBusy = false;
    }
  }

  async function loadParams() {
    const sn = live.activeDeviceSn();
    if (!sn) {
      params = [];
      return;
    }
    try {
      const j = await api<{ params?: DeviceParam[] }>(
        `/devices/params?device_sn=${encodeURIComponent(sn)}`,
      );
      params = j.params || [];
      paramsMsg = '';
    } catch (err) {
      params = [];
      paramsMsg = err instanceof Error ? err.message : 'Params load failed';
    }
  }

  async function loadBms() {
    try {
      const j = await api<{
        packs?: BmsPack[];
        ble?: { available?: boolean; error?: string };
      }>('/bms/saved');
      bmsPacks = j.packs || [];
      bmsBleOk = j.ble?.available !== false;
      if (j.ble?.error) bmsMsg = j.ble.error;
    } catch (err) {
      bmsPacks = [];
      bmsMsg = err instanceof Error ? err.message : 'BMS load failed';
    }
  }

  async function refreshDevicePanels() {
    await Promise.all([
      loadCapacity(),
      loadCloud(),
      loadAlias(),
      loadSiseli(),
      loadEcoflow(),
      loadF7(),
      loadWatchdogConfig(),
      loadParams(),
      loadBms(),
    ]);
  }

  onMount(() => {
    void refreshDevicePanels();
    return live.onDeviceChange(() => {
      void refreshDevicePanels();
    });
  });

  async function saveCapacity(e: SubmitEvent) {
    e.preventDefault();
    const sn = device?.device_sn;
    if (!sn) {
      capacityMsg = 'No active device.';
      return;
    }
    busy = true;
    capacityMsg = '';
    try {
      const wh = capacityWh.trim() === '' ? null : parseInt(capacityWh, 10);
      await apiPost('/devices/capacity', { device_sn: sn, capacity_wh: wh });
      capacityMsg = 'Saved.';
      await loadCapacity();
    } catch (err) {
      capacityMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function clearCapacity() {
    capacityWh = '';
    const sn = device?.device_sn;
    if (!sn) return;
    busy = true;
    try {
      await apiPost('/devices/capacity', { device_sn: sn, capacity_wh: null });
      capacityMsg = 'Override cleared.';
      await loadCapacity();
    } catch (err) {
      capacityMsg = err instanceof Error ? err.message : 'Clear failed';
    } finally {
      busy = false;
    }
  }

  async function saveAlias(e: SubmitEvent) {
    e.preventDefault();
    const did = String(deviceId || '');
    if (!did) {
      aliasMsg = 'No device id.';
      return;
    }
    busy = true;
    aliasMsg = 'Saving…';
    try {
      await apiPost('/device_prefs', { device_id: did, alias: alias.trim() });
      aliasMsg = 'Alias saved.';
      await live.refreshStatus();
    } catch (err) {
      aliasMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function saveCloud(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    accountMsg = '';
    try {
      await saveCloudCredentials({ email: email.trim(), password, region });
      accountMsg = 'Jackery credentials saved.';
      password = '';
      await loadCloud();
      await live.reconnect();
    } catch (err) {
      accountMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function forgetCloud() {
    if (!confirm('Forget Jackery cloud credentials?')) return;
    busy = true;
    accountMsg = '';
    try {
      await forgetCloudCredentials();
      cloudHasCreds = false;
      accountMsg = 'Credentials forgotten.';
    } catch (err) {
      accountMsg = err instanceof Error ? err.message : 'Forget failed';
    } finally {
      busy = false;
    }
  }

  async function togglePause() {
    pauseBusy = true;
    try {
      if (isPaused) {
        await apiPost('/resume_polling');
      } else {
        await apiPost('/pause_polling', { seconds: parseInt(pauseSeconds, 10) || 600 });
      }
      await live.refreshStatus();
    } catch (err) {
      live.showAlert(err instanceof Error ? err.message : 'Pause toggle failed');
    } finally {
      pauseBusy = false;
    }
  }

  async function saveSiseli(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    siseliMsg = 'Saving…';
    try {
      await apiPost('/siseli/credentials', {
        user_id: siseliUser.trim(),
        password: siseliPass,
        station_id: siseliStation.trim(),
        time_zone: siseliTz.trim() || undefined,
      });
      siseliPass = '';
      siseliMsg = 'Siseli credentials saved.';
      await loadSiseli();
    } catch (err) {
      siseliMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function saveEcoflow(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    ecoflowMsg = 'Saving…';
    try {
      await apiPost('/ecoflow/credentials', {
        email: ecoflowEmail.trim(),
        password: ecoflowPass,
      });
      ecoflowPass = '';
      ecoflowMsg = 'EcoFlow credentials saved.';
      await loadEcoflow();
    } catch (err) {
      ecoflowMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function saveF7(e: SubmitEvent) {
    e.preventDefault();
    const sn = live.activeDeviceSn();
    if (!sn) {
      f7Msg = 'No active device.';
      return;
    }
    busy = true;
    f7Msg = 'Saving…';
    try {
      await apiPost(`/f7_ac_reset/config?device_sn=${encodeURIComponent(sn)}`, {
        device_sn: sn,
        enabled: f7Enabled,
        cooldown_min: f7Cooldown,
        pulse_s: f7Pulse,
      });
      f7Msg = 'F7 AC reset saved.';
      await loadF7();
    } catch (err) {
      f7Msg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function refitParam(key: string) {
    const sn = live.activeDeviceSn();
    if (!sn) return;
    busy = true;
    paramsMsg = `Refitting ${key}…`;
    try {
      await apiPost('/devices/params/refit', { device_sn: sn, key });
      paramsMsg = `Refit ${key} done.`;
      await loadParams();
    } catch (err) {
      paramsMsg = err instanceof Error ? err.message : 'Refit failed';
    } finally {
      busy = false;
    }
  }

  async function resetParam(key: string) {
    const sn = live.activeDeviceSn();
    if (!sn) return;
    busy = true;
    paramsMsg = `Clearing ${key}…`;
    try {
      await apiPost('/devices/params', { device_sn: sn, key, value: null });
      paramsMsg = `Cleared ${key}.`;
      await loadParams();
    } catch (err) {
      paramsMsg = err instanceof Error ? err.message : 'Reset failed';
    } finally {
      busy = false;
    }
  }

  async function scanBms() {
    busy = true;
    bmsMsg = 'Scanning…';
    try {
      const j = await apiPost<{
        devices?: { mac?: string; name?: string; likely_jbd?: boolean }[];
        error?: string | null;
        ble_available?: boolean;
      }>('/bms/scan', { seconds: 8 });
      bmsScan = j.devices || [];
      bmsBleOk = j.ble_available !== false;
      bmsMsg = j.error || `Found ${bmsScan.length} device(s).`;
    } catch (err) {
      bmsMsg = err instanceof Error ? err.message : 'Scan failed';
    } finally {
      busy = false;
    }
  }

  async function saveLan(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    lanMsg = 'Saving…';
    try {
      const j = await apiPost<{
        local_read?: boolean;
        local_running?: boolean;
        local_error?: string | null;
      }>('/siseli/local', {
        local_read: lanRead,
        hybrid_pull: lanHybrid,
        inverter_ip: lanInverterIp.trim(),
        router_ip: lanRouterIp.trim(),
        sniff_iface: lanIface.trim(),
        mqtt_broker_ip: lanMqttBroker.trim(),
      });
      lanMsg =
        j.local_error && !j.local_running
          ? j.local_error
          : j.local_read
            ? j.local_running
              ? 'LAN read saved · running'
              : 'LAN read saved.'
            : 'LAN read turned off.';
      await loadSiseli();
    } catch (err) {
      lanMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function testLan() {
    busy = true;
    lanMsg = 'Testing LAN (up to ~25s)…';
    try {
      const j = await apiPost<{
        ok?: boolean;
        decoded?: boolean;
        error?: string;
        detail?: string;
      }>('/siseli/local/test', {
        local_read: true,
        hybrid_pull: lanHybrid,
        inverter_ip: lanInverterIp.trim(),
        router_ip: lanRouterIp.trim(),
        sniff_iface: lanIface.trim(),
        mqtt_broker_ip: lanMqttBroker.trim(),
      });
      lanMsg = j.decoded
        ? 'Test OK — decoded a reading.'
        : j.error || j.detail || 'Test finished — no decode yet.';
    } catch (err) {
      lanMsg = err instanceof Error ? err.message : 'Test failed';
    } finally {
      busy = false;
    }
  }

  function describeCloud(meta: typeof cloud) {
    if (!meta) return '—';
    const parts: string[] = [];
    if (meta.mqtt_connected) parts.push('mqtt');
    if (meta.age_s != null) parts.push(`${meta.age_s}s ago`);
    if (meta.mqtt_error) parts.push(`(${meta.mqtt_error})`);
    return parts.join(' · ') || '—';
  }

  function fmtParam(p: DeviceParam) {
    if (p.value == null) return '—';
    return `${p.value}${p.unit ? ` ${p.unit}` : ''}`;
  }
</script>

<section class="tab-panel">
  <section class="card device-card">
    <div class="card-header">
      <h2>Device info</h2>
      <span class="pill pill-source">{live.status?.source || '—'}</span>
    </div>
    <dl>
      <dt>Portal / cloud name</dt>
      <dd>{device?.portal_name || device?.name || '—'}</dd>
      <dt>Model</dt>
      <dd>{device?.model_code != null ? `model ${device.model_code}` : '—'}</dd>
      <dt>Serial</dt>
      <dd>{device?.device_sn || '—'}</dd>
      <dt>Cloud</dt>
      <dd>{describeCloud(cloud)}</dd>
      <dt>Last update</dt>
      <dd>
        {live.status?.last_update_ts
          ? new Date(live.status.last_update_ts * 1000).toLocaleString()
          : '—'}
      </dd>
      <dt>UPS / Super charge</dt>
      <dd>{upsLabel}</dd>
      <dt>Error code</dt>
      <dd>{formatDeviceErrorCode(t?.error_code)}</dd>
    </dl>
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Device alias</h2>
      {#if aliasMsg}<span class="hint">{aliasMsg}</span>{/if}
    </div>
    <form style="display:flex; gap:8px; align-items:center; flex-wrap:wrap" onsubmit={saveAlias}>
      <input
        class="device-select"
        type="text"
        style="width:220px"
        bind:value={alias}
        placeholder="Friendly name"
      />
      <button class="btn btn-primary" type="submit" disabled={busy}>Save alias</button>
    </form>
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Battery capacity</h2>
      {#if capacityMsg}<span class="hint">{capacityMsg}</span>{/if}
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Override total Wh for extension batteries. Used by forecast SOC math.
    </p>
    <form style="display:flex; gap:8px; align-items:center; flex-wrap:wrap" onsubmit={saveCapacity}>
      <label class="hint" for="capacity-input">Total capacity</label>
      <input
        id="capacity-input"
        class="device-select"
        type="number"
        min="500"
        max="200000"
        step="1"
        placeholder="e.g. 10080"
        style="width:140px"
        bind:value={capacityWh}
      />
      <small class="hint">Wh</small>
      <span class="hint">(device default: {capacityDefault ?? '—'})</span>
      <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={clearCapacity}>
        Clear override
      </button>
    </form>
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Polling</h2>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Pause cloud polling to free the Jackery session for the phone app.
    </p>
    <div style="display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <select class="device-select" bind:value={pauseSeconds} disabled={isPaused || pauseBusy}>
        <option value="60">1 min</option>
        <option value="300">5 min</option>
        <option value="600">10 min</option>
        <option value="1800">30 min</option>
        <option value="3600">1 hour</option>
      </select>
      <button class="btn btn-ghost" type="button" disabled={pauseBusy} onclick={togglePause}>
        {isPaused ? 'Resume polling' : 'Pause polling'}
      </button>
      {#if isPaused && pauseLeft != null}
        <span class="hint">paused — {fmtSecsMMSS(pauseLeft)} left</span>
      {/if}
    </div>
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Jackery account</h2>
      <span class="hint">{cloudHasCreds ? 'credentials saved' : 'no credentials'}</span>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Cloud credentials are encrypted on disk. Forget them to force a fresh sign-in.
    </p>
    <form onsubmit={saveCloud}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Email</span>
          <input type="email" bind:value={email} autocomplete="username" required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Password</span>
          <input type="password" bind:value={password} autocomplete="current-password" required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Region</span>
          <select bind:value={region}>
            <option value="US">US</option>
            <option value="EU">EU</option>
          </select>
        </label>
      </div>
      <div style="margin-top:14px; display:flex; gap:8px; flex-wrap:wrap">
        <button class="btn btn-primary" type="submit" disabled={busy}>Save credentials</button>
        <button class="btn btn-ghost" type="button" disabled={busy} onclick={forgetCloud}>
          Sign out / Forget credentials
        </button>
      </div>
    </form>
    {#if accountMsg}
      <p class="hint" style="margin-top:10px" role="status">{accountMsg}</p>
    {/if}
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Siseli account</h2>
      <span class="hint">{siseliStatus}</span>
    </div>
    <form onsubmit={saveSiseli}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">User ID</span>
          <input type="text" bind:value={siseliUser} required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Password</span>
          <input type="password" bind:value={siseliPass} autocomplete="current-password" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Station ID</span>
          <input type="text" bind:value={siseliStation} required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Timezone</span>
          <input type="text" bind:value={siseliTz} placeholder="e.g. America/Los_Angeles" />
        </label>
      </div>
      <button class="btn btn-primary" type="submit" style="margin-top:12px" disabled={busy}>
        Save Siseli credentials
      </button>
    </form>
    {#if siseliMsg}
      <p class="hint" style="margin-top:10px" role="status">{siseliMsg}</p>
    {/if}
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>Siseli LAN</h2>
      <span class="hint">{lanRead ? 'enabled' : 'off'}</span>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Local MQTT sniffer settings. Portal account must be saved first. Loaded from
      <code>/api/siseli/credentials</code>; saved via <code>/api/siseli/local</code>.
    </p>
    <form onsubmit={saveLan}>
      <div class="settings-row">
        <label class="settings-label" for="lan-read">
          <span class="settings-label-text">LAN read</span>
        </label>
        <div class="settings-control">
          <input id="lan-read" type="checkbox" bind:checked={lanRead} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="lan-hybrid">
          <span class="settings-label-text">Hybrid pull</span>
        </label>
        <div class="settings-control">
          <input id="lan-hybrid" type="checkbox" bind:checked={lanHybrid} />
        </div>
      </div>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Inverter IP</span>
          <input type="text" bind:value={lanInverterIp} placeholder="192.168.x.x" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Router IP</span>
          <input type="text" bind:value={lanRouterIp} placeholder="192.168.x.1" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Sniff iface</span>
          <input type="text" bind:value={lanIface} placeholder="eth0" />
        </label>
        <label class="auto-field">
          <span class="auto-label">MQTT broker IP</span>
          <input type="text" bind:value={lanMqttBroker} placeholder="optional" />
        </label>
      </div>
      <div style="margin-top:12px; display:flex; gap:8px; flex-wrap:wrap">
        <button class="btn btn-primary" type="submit" disabled={busy}>Save LAN</button>
        <button class="btn btn-ghost" type="button" disabled={busy} onclick={testLan}>
          Test LAN
        </button>
      </div>
    </form>
    {#if lanMsg}
      <p class="hint" style="margin-top:10px" role="status">{lanMsg}</p>
    {/if}
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>EcoFlow account</h2>
      <span class="hint">{ecoflowStatus}</span>
    </div>
    <form onsubmit={saveEcoflow}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Email</span>
          <input type="email" bind:value={ecoflowEmail} autocomplete="username" required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Password</span>
          <input type="password" bind:value={ecoflowPass} autocomplete="current-password" />
        </label>
      </div>
      <button class="btn btn-primary" type="submit" style="margin-top:12px" disabled={busy}>
        Save EcoFlow credentials
      </button>
    </form>
    {#if ecoflowMsg}
      <p class="hint" style="margin-top:10px" role="status">{ecoflowMsg}</p>
    {/if}
    {#if ecoflowDevices.length}
      <h3 style="margin:14px 0 8px; font-size:14px">Devices</h3>
      <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
        {#each ecoflowDevices as d, i (d.sn || d.raw_sn || i)}
          <li style="padding:6px 0; border-bottom:1px solid var(--border-soft)">
            <strong>{d.alias || d.sn || d.raw_sn || '—'}</strong>
            <span class="hint"> · {d.device_type || 'device'} · {d.sn || d.raw_sn || ''}</span>
          </li>
        {/each}
      </ul>
    {/if}
    <button
      class="btn btn-ghost"
      type="button"
      style="margin-top:10px"
      disabled={busy}
      onclick={loadEcoflow}
    >
      Refresh devices
    </button>
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>F7 AC reset</h2>
      <span class="hint">{f7Daylight ? 'daylight' : 'night'} · {f7Enabled ? 'on' : 'off'}</span>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Pulse AC off briefly when F7 error is stuck during daylight.
    </p>
    <form onsubmit={saveF7}>
      <div class="settings-row">
        <label class="settings-label" for="f7-en">
          <span class="settings-label-text">Enabled</span>
        </label>
        <div class="settings-control">
          <input id="f7-en" type="checkbox" bind:checked={f7Enabled} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="f7-cd">
          <span class="settings-label-text">Cooldown (min)</span>
        </label>
        <div class="settings-control">
          <input id="f7-cd" class="forecast-input" type="number" min="1" max="1440" bind:value={f7Cooldown} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="f7-pulse">
          <span class="settings-label-text">Pulse (s)</span>
        </label>
        <div class="settings-control">
          <input id="f7-pulse" class="forecast-input" type="number" min="1" max="60" bind:value={f7Pulse} />
        </div>
      </div>
      <button class="btn btn-primary" type="submit" style="margin-top:8px" disabled={busy}>
        Save F7 config
      </button>
    </form>
    {#if f7Msg}
      <p class="hint" style="margin-top:10px" role="status">{f7Msg}</p>
    {/if}
  </section>

  {#if watchdogApplicable}
    <section class="card device-card">
      <div class="card-header">
        <h2>AC auto recovery</h2>
        <span class="hint">{watchdogEnabled ? 'on' : 'off'}</span>
      </div>
      <p class="hint" style="margin:6px 0 12px">
        When AC output collapses while the port still claims ON (hardware trip), briefly cycle AC
        off/on to recover. Defaults on for Explorer 5000 Plus; off for other Jackery models.
      </p>
      <div class="settings-row">
        <label class="settings-label" for="wd-keep-ac">
          <span class="settings-label-text">Keep AC output on</span>
        </label>
        <div class="settings-control">
          <input
            id="wd-keep-ac"
            type="checkbox"
            bind:checked={watchdogEnabled}
            disabled={watchdogBusy}
            onchange={saveWatchdogConfig}
          />
        </div>
      </div>
      {#if watchdogStatus}
        <p class="hint" style="margin-top:10px" role="status">{watchdogStatus}</p>
      {/if}
    </section>
  {/if}

  <section class="card device-card">
    <div class="card-header">
      <h2>Learned parameters</h2>
      <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={loadParams}>
        Refresh
      </button>
    </div>
    {#if paramsMsg}
      <p class="hint" role="status">{paramsMsg}</p>
    {/if}
    {#if !params.length}
      <p class="hint">No resolved parameters for this device.</p>
    {:else}
      <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
        {#each params as p (p.key)}
          <li
            style="display:flex; gap:10px; align-items:center; justify-content:space-between; padding:8px 0; border-bottom:1px solid var(--border-soft)"
          >
            <div>
              <strong>{p.label || p.key}</strong>
              <div class="hint">
                {fmtParam(p)} · {p.source || '—'}
                {#if p.n_samples} · n={p.n_samples}{/if}
              </div>
            </div>
            <div style="display:flex; gap:6px">
              <button
                class="btn btn-ghost btn-tiny"
                type="button"
                disabled={busy}
                onclick={() => refitParam(p.key)}
              >
                Refit
              </button>
              <button
                class="btn btn-ghost btn-tiny"
                type="button"
                disabled={busy}
                onclick={() => resetParam(p.key)}
              >
                Reset
              </button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="card device-card">
    <div class="card-header">
      <h2>BMS packs</h2>
      <span class="hint">{bmsBleOk ? `${bmsPacks.length} saved` : 'BLE unavailable'}</span>
    </div>
    {#if bmsMsg}
      <p class="hint" role="status">{bmsMsg}</p>
    {/if}
    {#if bmsPacks.length}
      <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
        {#each bmsPacks as p (p.mac)}
          <li style="padding:6px 0; border-bottom:1px solid var(--border-soft)">
            <strong>{p.alias || p.mac || '—'}</strong>
            <span class="hint">
              · {p.mac}
              {#if p.soc_pct != null} · {Math.round(p.soc_pct)}%{/if}
              {#if p.capacity_wh != null} · {p.capacity_wh} Wh{/if}
              · {p.online ? 'online' : 'offline'}
            </span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="hint">No saved BMS packs.</p>
    {/if}
    <div style="margin-top:10px; display:flex; gap:8px; flex-wrap:wrap">
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={scanBms}>
        Scan BLE
      </button>
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadBms}>
        Refresh saved
      </button>
    </div>
    {#if bmsScan.length}
      <h3 style="margin:14px 0 8px; font-size:14px">Scan results</h3>
      <ul style="list-style:none; padding:0; margin:0">
        {#each bmsScan as d, i (`${d.mac ?? i}`)}
          <li style="padding:4px 0" class="hint">
            {d.name || 'unknown'} · {d.mac || '—'}
            {#if d.likely_jbd} · likely JBD{/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</section>
