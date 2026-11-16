<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost, ApiError } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { formatDeviceErrorCode, fmtSecsMMSS } from '$lib/format';
  import { saveCloudCredentials, forgetCloudCredentials, cloudAuthStatus } from '$lib/auth';
  import { isSiseliDevice, viewingSiseli, viewingEcoflow } from '$lib/siseli';
  import type { SiseliControl } from '$lib/types';
  import SiseliControlField from '../shared/SiseliControlField.svelte';
  import { collapsible } from '$lib/actions/collapsible';

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
    capacity_wh?: number | null;
    siseli_device_sn?: string | null;
    roles?: string[] | null;
  };

  type EcoflowRole = 'solar' | 'battery' | 'output';
  const EF_ROLES: EcoflowRole[] = ['solar', 'battery', 'output'];
  const EF_TYPES = [
    { value: 'DELTA_3_MAX_PLUS', label: 'Delta 3 Max Plus' },
    { value: 'DELTA_3', label: 'Delta 3' },
    { value: 'DELTA_3_1500', label: 'Delta 3 1500' },
  ];

  /** `/api/bms/saved` row: live values live under `reading` (no top-level `online`). */
  type BmsPack = {
    mac?: string;
    alias?: string;
    capacity_wh?: number | null;
    siseli_device_sn?: string | null;
    error?: string | null;
    last_seen_ts?: number | null;
    reading?: { soc_pct?: number | null; error?: string | null; ts?: number | null };
  };

  /** A pack is online when its last good reading is recent (poller runs every ~15 s). */
  const BMS_STALE_S = 120;

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
  let ecoflowHost = $state('api.ecoflow.com');
  let ecoflowHasCreds = $state(false);
  let efSn = $state('');
  let efType = $state('DELTA_3_MAX_PLUS');
  let efAlias = $state('');
  let efSiseli = $state('');
  let efRoles = $state<Record<EcoflowRole, boolean>>({ solar: true, battery: true, output: true });
  let efEditing = $state(false);
  let efDeviceMsg = $state('');
  let efAuto = $state(true);
  let efStart = $state('');
  let efEnd = $state('');
  let efReconnectMsg = $state('');

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
  let paramDrafts = $state<Record<string, string>>({});
  let paramsMsg = $state('');

  let bmsPacks = $state.raw<BmsPack[]>([]);
  let bmsScan = $state.raw<{ mac?: string; name?: string; likely_jbd?: boolean }[]>([]);
  let bmsMsg = $state('');
  let bmsBleOk = $state(true);
  let bmsInverters = $state.raw<Record<string, { use_as_main?: boolean }>>({});
  let bmsMac = $state('');
  let bmsAlias = $state('');
  let bmsCapacity = $state('');
  let bmsSiseli = $state('');
  let bmsEditing = $state(false);
  let bmsFormMsg = $state('');
  let prefsMsg = $state('');

  let controlsMsg = $state('');
  let controlsBusy = $state(false);
  /** Optimistic pin set while a save is in flight; null = use device_prefs. */
  let pinDraft = $state<string[] | null>(null);

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

  // Jackery-only cards (F7, learned params, polling) hide for Siseli/EcoFlow,
  // like the legacy `data-jackery-only` gating.
  const isSiseli = $derived(viewingSiseli(live.status));
  const isEcoflow = $derived(viewingEcoflow(live.status));
  const isJackery = $derived(!isSiseli && !isEcoflow);
  const siseliInverters = $derived(live.devices.filter((d) => isSiseliDevice(d)));
  const activeSn = $derived(live.activeDeviceSn() || '');
  const prefs = $derived(live.status?.device_prefs);
  const useAsMain = $derived(bmsInverters[activeSn]?.use_as_main !== false);
  const bmsPacksHere = $derived(
    isSiseli ? bmsPacks.filter((p) => p.siseli_device_sn === activeSn) : bmsPacks,
  );

  const bmsSummary = $derived.by(() => {
    if (!bmsPacks.length) return 'none saved';
    const online = bmsPacks.filter((p) => packState(p) === 'online').length;
    return `${bmsPacks.length} saved · ${online} online`;
  });
  const ecoflowLinkedCount = $derived(ecoflowDevices.filter((d) => d.siseli_device_sn).length);

  function inverterName(sn?: string | null) {
    if (!sn) return 'unassigned';
    const d = live.devices.find((x) => x.device_sn === sn || x.device_id === sn);
    return d?.name || `Siseli inverter (${sn.replace(/^siseli:/, '')})`;
  }

  function packSoc(p: BmsPack): number | null {
    const v = p.reading?.soc_pct;
    return typeof v === 'number' && Number.isFinite(v) ? v : null;
  }

  function packError(p: BmsPack): string {
    return String(p.error || p.reading?.error || '');
  }

  /** online = fresh SOC; offline = had a reading but it went stale; none = never read. */
  function packState(p: BmsPack): 'online' | 'offline' | 'none' {
    const ts = Number(p.reading?.ts ?? p.last_seen_ts ?? 0);
    if (packSoc(p) == null || !ts) return 'none';
    return Date.now() / 1000 - ts < BMS_STALE_S ? 'online' : 'offline';
  }

  function packAge(p: BmsPack): string {
    const ts = Number(p.reading?.ts ?? p.last_seen_ts ?? 0);
    return ts ? ago(ts) : '';
  }

  function ago(ts: number): string {
    const s = Math.max(0, Math.round(Date.now() / 1000 - ts));
    if (s < 90) return `${s}s ago`;
    if (s < 5400) return `${Math.round(s / 60)}m ago`;
    return `${Math.round(s / 3600)}h ago`;
  }

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
      }>('/siseli/credentials');
      siseliStatus = j.has_credentials
        ? `saved${j.user_id ? ` · ${j.user_id}` : ''}${j.station_id ? ` · station ${j.station_id}` : ''}`
        : 'no credentials';
      if (j.user_id) siseliUser = j.user_id;
      if (j.station_id) siseliStation = j.station_id;
      if (j.time_zone) siseliTz = j.time_zone;
    } catch (err) {
      siseliStatus = err instanceof Error ? err.message : 'unavailable';
    }
  }

  type EcoflowStatus = {
    has_credentials?: boolean;
    email?: string | null;
    api_host?: string | null;
    state?: string;
    error?: string | null;
    last_poll_ts?: number | null;
    mqtt_msg_count?: number;
    mqtt_update_count?: number;
    devices?: EcoflowDevice[];
    reconnect?: { auto_reconnect?: boolean; reconnect_start?: string | null; reconnect_end?: string | null };
  };

  function ecoflowStatusText(j: EcoflowStatus) {
    if (!j.has_credentials) return 'no credentials';
    const email = j.email ? ` · ${j.email}` : '';
    if (j.error) return `error: ${j.error}`;
    if (j.state === 'connected') {
      if (j.last_poll_ts) {
        const age = Math.max(0, Math.round(Date.now() / 1000 - Number(j.last_poll_ts)));
        return `connected · live ${age}s ago${email}`;
      }
      if ((j.mqtt_msg_count || 0) > 0 && !j.mqtt_update_count) {
        return `connected · MQTT msgs but decode empty${email}`;
      }
      return `connected · waiting for telemetry${email}`;
    }
    if (j.state && j.state !== 'idle') return `${j.state}${email}`;
    return `saved${email}`;
  }

  async function loadEcoflow() {
    try {
      const j = await api<EcoflowStatus>('/ecoflow/credentials');
      ecoflowHasCreds = !!j.has_credentials;
      ecoflowStatus = ecoflowStatusText(j);
      if (j.email) ecoflowEmail = j.email;
      ecoflowHost = j.api_host || 'api.ecoflow.com';
      ecoflowDevices = j.devices || [];
      efAuto = j.reconnect?.auto_reconnect !== false;
      efStart = j.reconnect?.reconnect_start || '';
      efEnd = j.reconnect?.reconnect_end || '';
    } catch (err) {
      ecoflowStatus = err instanceof Error ? err.message : 'unavailable';
    }
  }

  function scrollToForm(id: string) {
    requestAnimationFrame(() =>
      document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'center' }),
    );
  }

  function resetEfForm() {
    efSn = '';
    efType = 'DELTA_3_MAX_PLUS';
    efAlias = '';
    efSiseli = isSiseli ? activeSn : '';
    efRoles = { solar: true, battery: true, output: true };
    efEditing = false;
  }

  function editEcoflowDevice(d: EcoflowDevice) {
    efSn = d.raw_sn || String(d.sn || '').replace(/^ecoflow:/, '');
    efType = d.device_type || 'DELTA_3_MAX_PLUS';
    efAlias = d.alias || '';
    efSiseli = d.siseli_device_sn || '';
    const set = new Set((d.roles || []).map((r) => String(r).toLowerCase()));
    const all = set.size === 0;
    efRoles = {
      solar: all || set.has('solar'),
      battery: all || set.has('battery'),
      output: all || set.has('output'),
    };
    efEditing = true;
    scrollToForm('ef-device-form');
    efDeviceMsg = 'Editing — save to update.';
  }

  async function saveEcoflowDevice(e: SubmitEvent) {
    e.preventDefault();
    const raw = efSn.trim();
    if (!raw) {
      efDeviceMsg = 'Serial number is required.';
      return;
    }
    const roles = EF_ROLES.filter((r) => efRoles[r]);
    busy = true;
    efDeviceMsg = 'Saving…';
    try {
      await apiPost('/ecoflow/devices', {
        raw_sn: raw,
        device_type: efType,
        alias: efAlias.trim(),
        siseli_device_sn: efSiseli,
        roles: roles.length ? roles : EF_ROLES,
      });
      efDeviceMsg = efSiseli
        ? `Saved · linked to ${inverterName(efSiseli)}.`
        : 'Saved · not linked (shows as its own device).';
      resetEfForm();
      await loadEcoflow();
      await live.refreshStatus();
    } catch (err) {
      efDeviceMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function linkEcoflowHere(d: EcoflowDevice) {
    if (!d.sn || !activeSn) return;
    busy = true;
    efDeviceMsg = 'Linking…';
    try {
      await apiPost(`/ecoflow/devices/${encodeURIComponent(d.sn)}/link`, {
        siseli_device_sn: activeSn,
        roles: d.roles?.length ? d.roles : EF_ROLES,
      });
      efDeviceMsg = `Linked to ${inverterName(activeSn)}.`;
      await loadEcoflow();
      await live.refreshStatus();
    } catch (err) {
      efDeviceMsg = err instanceof Error ? err.message : 'Link failed';
    } finally {
      busy = false;
    }
  }

  async function removeEcoflowDevice(d: EcoflowDevice) {
    if (!d.sn || !confirm(`Remove ${d.alias || d.sn}?`)) return;
    busy = true;
    try {
      await api(`/ecoflow/devices/${encodeURIComponent(d.sn)}`, { method: 'DELETE' });
      efDeviceMsg = 'Removed.';
      await loadEcoflow();
      await live.refreshStatus();
    } catch (err) {
      efDeviceMsg = err instanceof Error ? err.message : 'Remove failed';
    } finally {
      busy = false;
    }
  }

  async function forgetEcoflow() {
    if (!confirm('Forget EcoFlow credentials? Linked EcoFlow data stops updating.')) return;
    busy = true;
    try {
      await api('/ecoflow/credentials', { method: 'DELETE' });
      ecoflowPass = '';
      ecoflowMsg = 'Credentials forgotten.';
      await loadEcoflow();
    } catch (err) {
      ecoflowMsg = err instanceof Error ? err.message : 'Forget failed';
    } finally {
      busy = false;
    }
  }

  async function saveEcoflowReconnect(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    efReconnectMsg = 'Saving…';
    try {
      const j = await apiPost<{
        auto_reconnect?: boolean;
        reconnect_start?: string | null;
        reconnect_end?: string | null;
      }>('/ecoflow/reconnect_settings', {
        auto_reconnect: efAuto,
        reconnect_start: efStart.trim() || null,
        reconnect_end: efEnd.trim() || null,
      });
      efAuto = j.auto_reconnect !== false;
      efStart = j.reconnect_start || '';
      efEnd = j.reconnect_end || '';
      efReconnectMsg = 'Saved.';
    } catch (err) {
      efReconnectMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
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
      paramDrafts = {};
      return;
    }
    try {
      const j = await api<{ params?: DeviceParam[] }>(
        `/devices/params?device_sn=${encodeURIComponent(sn)}`,
      );
      params = j.params || [];
      paramDrafts = Object.fromEntries(
        params.map((p) => [p.key, p.value == null ? '' : String(p.value)]),
      );
      paramsMsg = '';
    } catch (err) {
      params = [];
      paramDrafts = {};
      paramsMsg = err instanceof Error ? err.message : 'Params load failed';
    }
  }

  async function loadBms() {
    try {
      const j = await api<{
        packs?: BmsPack[];
        inverters?: Record<string, { use_as_main?: boolean }>;
        ble?: { available?: boolean; error?: string };
      }>('/bms/saved');
      bmsPacks = j.packs || [];
      bmsInverters = j.inverters || {};
      bmsBleOk = j.ble?.available !== false;
      if (j.ble?.error) bmsMsg = j.ble.error;
    } catch (err) {
      bmsPacks = [];
      bmsMsg = err instanceof Error ? err.message : 'BMS load failed';
    }
  }

  function resetBmsForm() {
    bmsMac = '';
    bmsAlias = '';
    bmsCapacity = '';
    bmsSiseli = isSiseli ? activeSn : '';
    bmsEditing = false;
  }

  function editBms(p: BmsPack) {
    bmsMac = p.mac || '';
    bmsAlias = p.alias || '';
    bmsCapacity = p.capacity_wh ? String(p.capacity_wh) : '';
    bmsSiseli = p.siseli_device_sn || '';
    bmsEditing = true;
    scrollToForm('bms-pack-form');
    bmsFormMsg = 'Editing — save to update.';
  }

  function pickScanned(d: { mac?: string; name?: string }) {
    const saved = bmsPacks.find((p) => p.mac === d.mac);
    if (saved) {
      editBms(saved);
      return;
    }
    resetBmsForm();
    bmsMac = d.mac || '';
    if (d.name && d.name !== d.mac) bmsAlias = d.name;
    bmsFormMsg = 'Fill in and save the pack.';
    scrollToForm('bms-pack-form');
  }

  async function saveBms(e: SubmitEvent) {
    e.preventDefault();
    const mac = bmsMac.trim();
    if (!mac) {
      bmsFormMsg = 'MAC is required.';
      return;
    }
    const cap = bmsCapacity.trim() === '' ? null : Number(bmsCapacity);
    if (cap != null && (!Number.isInteger(cap) || cap <= 0)) {
      bmsFormMsg = 'Capacity must be a whole number of Wh.';
      return;
    }
    busy = true;
    bmsFormMsg = 'Saving…';
    try {
      await apiPost('/bms/saved', {
        mac,
        alias: bmsAlias.trim(),
        capacity_wh: cap,
        siseli_device_sn: bmsSiseli,
      });
      bmsFormMsg = bmsSiseli
        ? `Saved · assigned to ${inverterName(bmsSiseli)}.`
        : 'Saved · unassigned (not used for any inverter SOC).';
      resetBmsForm();
      await loadBms();
      await live.refreshStatus();
    } catch (err) {
      bmsFormMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function deleteBms(p: BmsPack) {
    if (!p.mac || !confirm(`Remove BMS ${p.alias || p.mac}?`)) return;
    busy = true;
    try {
      await api(`/bms/saved/${encodeURIComponent(p.mac)}`, { method: 'DELETE' });
      bmsFormMsg = 'Removed.';
      if (bmsMac === p.mac) resetBmsForm();
      await loadBms();
      await live.refreshStatus();
    } catch (err) {
      bmsFormMsg = err instanceof Error ? err.message : 'Remove failed';
    } finally {
      busy = false;
    }
  }

  async function setUseAsMain(on: boolean) {
    if (!activeSn) return;
    busy = true;
    try {
      await apiPost(`/bms/inverter/${encodeURIComponent(activeSn)}`, { use_as_main: on });
      bmsInverters = { ...bmsInverters, [activeSn]: { ...bmsInverters[activeSn], use_as_main: on } };
      await live.refreshStatus();
    } catch (err) {
      prefsMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function savePref(key: 'ignore_inverter_soc' | 'calc_grid', on: boolean) {
    const did = String(deviceId || '');
    if (!did) return;
    busy = true;
    prefsMsg = '';
    try {
      await apiPost('/device_prefs', { device_id: did, [key]: on });
      await live.refreshStatus();
    } catch (err) {
      prefsMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
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

  /** New-entry forms default their inverter picker to the JJN being viewed. */
  function defaultLinkTargets() {
    if (!efEditing) efSiseli = isSiseli ? activeSn : '';
    if (!bmsEditing) bmsSiseli = isSiseli ? activeSn : '';
  }

  onMount(() => {
    void refreshDevicePanels().then(defaultLinkTargets);
    return live.onDeviceChange(() => {
      void refreshDevicePanels().then(defaultLinkTargets);
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
        api_host: ecoflowHost,
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

  async function saveParam(key: string) {
    const sn = live.activeDeviceSn();
    if (!sn) return;
    const value = parseFloat(String(paramDrafts[key] ?? ''));
    if (!Number.isFinite(value)) {
      paramsMsg = 'Enter a valid number.';
      return;
    }
    busy = true;
    paramsMsg = `Saving ${key}…`;
    try {
      await apiPost('/devices/params', { device_sn: sn, key, value });
      if (key === 'battery_capacity_wh') {
        await apiPost('/devices/capacity', { device_sn: sn, capacity_wh: value });
      }
      paramsMsg = `Saved ${key}.`;
      await loadParams();
      if (key === 'battery_capacity_wh') await loadCapacity();
    } catch (err) {
      paramsMsg = err instanceof Error ? err.message : 'Save failed';
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
      if (key === 'battery_capacity_wh') {
        await apiPost('/devices/capacity', { device_sn: sn, capacity_wh: null });
      }
      paramsMsg = `Cleared ${key}.`;
      await loadParams();
      if (key === 'battery_capacity_wh') await loadCapacity();
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

  const controls = $derived<SiseliControl[]>(
    Array.isArray(live.status?.siseli_controls) ? live.status!.siseli_controls! : [],
  );
  const pins = $derived(new Set(pinDraft ?? prefs?.live_controls ?? controls.map((c) => c.canonical)));

  let controlsFilter = $state('');
  let collapsedGroups = $state<Record<string, boolean>>({});

  const CONTROL_GROUPS: [string, RegExp][] = [
    ['Temperature', /temp/i],
    ['Load & light control', /load|light/i],
    ['Discharge & protection', /discharg|over.?volt|low voltage|alarm/i],
    ['Charging', /charg|equaliz|float|boost|increase/i],
  ];

  /** Controls grouped by what they affect, so 30+ settings stay scannable. */
  const controlGroups = $derived.by(() => {
    const q = controlsFilter.trim().toLowerCase();
    const groups = new Map<string, SiseliControl[]>();
    for (const c of controls) {
      const name = c.name || c.canonical;
      if (q && !name.toLowerCase().includes(q)) continue;
      const group = CONTROL_GROUPS.find(([, re]) => re.test(name))?.[0] ?? 'General';
      groups.set(group, [...(groups.get(group) || []), c]);
    }
    const order = ['General', ...CONTROL_GROUPS.map(([n]) => n)];
    return order
      .filter((n) => groups.has(n))
      .map((name) => {
        const items = groups.get(name)!;
        return { name, items, pinned: items.filter((c) => pins.has(c.canonical)).length };
      });
  });

  async function savePins(next: string[]) {
    const did = String(deviceId || '');
    if (!did) return;
    pinDraft = next;
    controlsMsg = 'Saving…';
    try {
      await apiPost('/device_prefs', { device_id: did, live_controls: next });
      await live.refreshStatus();
      controlsMsg = `${next.length} on Live`;
    } catch (err) {
      controlsMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      pinDraft = null;
    }
  }

  function togglePin(key: string, on: boolean) {
    const next = controls.map((c) => c.canonical).filter((k) => (k === key ? on : pins.has(k)));
    void savePins(next);
  }

  async function writeControl(key: string, value: number) {
    const did = String(deviceId || '');
    if (!did || !Number.isFinite(value)) return;
    controlsBusy = true;
    controlsMsg = 'Writing…';
    try {
      const j = await apiPost<{ controls?: SiseliControl[] }>('/siseli/settings', {
        device_id: did,
        key,
        value,
      });
      if (Array.isArray(j?.controls) && live.status) live.status.siseli_controls = j.controls;
      controlsMsg = 'Saved to inverter.';
    } catch (err) {
      controlsMsg = err instanceof Error ? err.message : 'Write failed';
    } finally {
      controlsBusy = false;
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

</script>

<section class="tab-panel">
  <section class="card device-card" use:collapsible={{ key: 'device-info' }}>
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

  <section class="card device-card" use:collapsible={{ key: 'device-alias' }}>
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

  {#if isSiseli && controls.length}
    <section class="card device-card" use:collapsible={{ key: 'controller-settings', collapsed: true }}>
      <div class="card-header">
        <h2>Inverter controls</h2>
        <span class="hint">{controlsMsg || `${pins.size} of ${controls.length} on Live`}</span>
      </div>
      <p class="hint" style="margin:6px 0 12px">
        Edit any setting here; writes go to the Siseli portal. “Show on Live” also puts a control on
        the Live tab.
      </p>
      <div class="controls-toolbar">
        <input
          class="controls-filter"
          type="search"
          placeholder="Filter controls…"
          aria-label="Filter inverter controls"
          bind:value={controlsFilter}
        />
        <button class="btn btn-ghost btn-tiny" type="button" onclick={() => savePins(controls.map((c) => c.canonical))}>
          Show all on Live
        </button>
        <button class="btn btn-ghost btn-tiny" type="button" onclick={() => savePins([])}>
          Hide all from Live
        </button>
      </div>
      {#each controlGroups as g (g.name)}
        <details
          class="controls-group"
          open={!!controlsFilter || !collapsedGroups[g.name]}
          ontoggle={(e) => {
            if (!controlsFilter) collapsedGroups[g.name] = !e.currentTarget.open;
          }}
        >
          <summary>
            <span>{g.name}</span>
            <span class="hint">{g.items.length} · {g.pinned} on Live</span>
          </summary>
          <div class="inverter-controls-grid">
            {#each g.items as c (c.canonical)}
              {@const pinned = pins.has(c.canonical)}
              <div class="inverter-control-row" class:pinned>
                <SiseliControlField control={c} disabled={controlsBusy} onwrite={writeControl} />
                <label class="pin-toggle" class:on={pinned} title="Show this control on the Live tab">
                  <input
                    type="checkbox"
                    checked={pinned}
                    onchange={(e) => togglePin(c.canonical, e.currentTarget.checked)}
                  />
                  {pinned ? 'On Live' : 'Show on Live'}
                </label>
              </div>
            {/each}
          </div>
        </details>
      {:else}
        <p class="hint">No controls match “{controlsFilter}”.</p>
      {/each}
    </section>
  {/if}

  <section class="card device-card" use:collapsible={{ key: 'battery-capacity' }}>
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

  {#if isJackery}
  <section class="card device-card" use:collapsible={{ key: 'polling' }}>
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
  {/if}

  {#if isJackery}
  <section class="card device-card" use:collapsible={{ key: 'account', collapsed: true }}>
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
  {/if}

  <section class="card device-card" use:collapsible={{ key: 'siseli-account', collapsed: true }}>
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

  <section class="card device-card" use:collapsible={{ key: 'ecoflow-account', collapsed: true }}>
    <div class="card-header">
      <h2>EcoFlow</h2>
      <span class="hint">{ecoflowStatus}{ecoflowLinkedCount ? ` · ${ecoflowLinkedCount} linked` : ''}</span>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      EcoFlow power stations linked to a Siseli inverter (JJN) become part of it: their solar,
      battery and output fold into the inverter's Live view and they leave the fleet strip.
      Unlinked devices show as their own card.
    </p>
    <form onsubmit={saveEcoflow}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Email</span>
          <input type="email" bind:value={ecoflowEmail} autocomplete="username" required />
        </label>
        <label class="auto-field">
          <span class="auto-label">Password</span>
          <input
            type="password"
            bind:value={ecoflowPass}
            autocomplete="current-password"
            placeholder={ecoflowHasCreds ? 'blank keeps saved' : 'required first time'}
          />
        </label>
        <label class="auto-field">
          <span class="auto-label">API host</span>
          <select bind:value={ecoflowHost}>
            <option value="api.ecoflow.com">Global / US (api.ecoflow.com)</option>
            <option value="api-e.ecoflow.com">Europe (api-e.ecoflow.com)</option>
          </select>
        </label>
      </div>
      <div style="margin-top:12px; display:flex; gap:8px; flex-wrap:wrap">
        <button class="btn btn-primary" type="submit" disabled={busy}>Save credentials</button>
        {#if ecoflowHasCreds}
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={forgetEcoflow}>
            Forget
          </button>
        {/if}
      </div>
    </form>
    {#if ecoflowMsg}
      <p class="hint" style="margin-top:10px" role="status">{ecoflowMsg}</p>
    {/if}

    <h3 class="sub-head">Devices</h3>
    {#if ecoflowDevices.length}
      <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
        {#each ecoflowDevices as d, i (d.sn || d.raw_sn || i)}
          <li
            style="display:flex; gap:8px; align-items:center; justify-content:space-between; flex-wrap:wrap; padding:8px 0; border-bottom:1px solid var(--border-soft)"
          >
            <div style="flex:1; min-width:200px">
              <strong>{d.alias || d.raw_sn || d.sn || '—'}</strong>
              <span class="hint"> · {d.device_type || 'device'} · {d.raw_sn || d.sn}</span>
              <div class="hint">
                {#if d.siseli_device_sn}
                  part of {inverterName(d.siseli_device_sn)} ·
                  {(d.roles?.length ? d.roles : EF_ROLES).join(', ')}
                {:else}
                  not linked · shows as its own device
                {/if}
              </div>
            </div>
            <div style="display:flex; gap:6px; flex-wrap:wrap">
              {#if isSiseli && d.siseli_device_sn !== activeSn}
                <button
                  class="btn btn-ghost btn-tiny"
                  type="button"
                  disabled={busy}
                  title={`Make this part of ${inverterName(activeSn)}`}
                  onclick={() => linkEcoflowHere(d)}
                >
                  Link here
                </button>
              {/if}
              <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={() => editEcoflowDevice(d)}>
                Edit
              </button>
              <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={() => removeEcoflowDevice(d)}>
                Remove
              </button>
            </div>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="hint">No EcoFlow devices yet.</p>
    {/if}

    <h3 class="sub-head" id="ef-device-form">{efEditing ? `Edit ${efAlias || efSn}` : 'Add device'}</h3>
    <form onsubmit={saveEcoflowDevice}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Serial number</span>
          <input type="text" bind:value={efSn} required placeholder="from EcoFlow app" autocomplete="off" readonly={efEditing} />
        </label>
        <label class="auto-field">
          <span class="auto-label">Model</span>
          <select bind:value={efType}>
            {#each EF_TYPES as o (o.value)}
              <option value={o.value}>{o.label}</option>
            {/each}
          </select>
        </label>
        <label class="auto-field">
          <span class="auto-label">Alias</span>
          <input type="text" bind:value={efAlias} placeholder="optional" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Part of inverter</span>
          <select bind:value={efSiseli}>
            <option value="">— none (own device) —</option>
            {#each siseliInverters as inv (inv.device_id)}
              <option value={inv.device_sn || inv.device_id}>{inv.name || inv.device_sn || inv.device_id}</option>
            {/each}
          </select>
        </label>
        <fieldset class="auto-field roles-field" style="grid-column:1/-1" disabled={!efSiseli}>
          <legend class="auto-label">Roles when linked</legend>
          <span style="display:flex; gap:14px; flex-wrap:wrap">
            {#each EF_ROLES as r (r)}
              <label><input type="checkbox" bind:checked={efRoles[r]} /> {r}</label>
            {/each}
          </span>
          {#if !efSiseli}
            <span class="hint">Pick “Part of inverter” first — roles only apply to a linked device.</span>
          {/if}
        </fieldset>
      </div>
      <div style="margin-top:12px; display:flex; gap:8px; flex-wrap:wrap">
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {efEditing ? 'Update device' : 'Add device'}
        </button>
        {#if efEditing}
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={() => { resetEfForm(); efDeviceMsg = ''; }}>
            Cancel
          </button>
        {/if}
      </div>
    </form>
    {#if efDeviceMsg}
      <p class="hint" style="margin-top:10px" role="status">{efDeviceMsg}</p>
    {/if}

    <h3 class="sub-head">Auto-reconnect</h3>
    <p class="hint" style="margin:0 0 10px">
      Restart the MQTT session when it stays connected but silent. Leave both times blank to allow
      any hour.
    </p>
    <form onsubmit={saveEcoflowReconnect}>
      <div class="settings-row">
        <label class="settings-label" for="ef-auto">
          <span class="settings-label-text">Enable auto-reconnect</span>
        </label>
        <div class="settings-control">
          <input id="ef-auto" type="checkbox" bind:checked={efAuto} />
        </div>
      </div>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">Window start</span>
          <input type="time" bind:value={efStart} />
        </label>
        <label class="auto-field">
          <span class="auto-label">Window end</span>
          <input type="time" bind:value={efEnd} />
        </label>
      </div>
      <button class="btn btn-primary" type="submit" style="margin-top:12px" disabled={busy}>
        Save reconnect settings
      </button>
    </form>
    {#if efReconnectMsg}
      <p class="hint" style="margin-top:10px" role="status">{efReconnectMsg}</p>
    {/if}
  </section>

  {#if isJackery}
  <section class="card device-card" use:collapsible={{ key: 'f7-ac-reset' }}>
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
  {/if}

  {#if watchdogApplicable}
    <section class="card device-card" use:collapsible={{ key: 'ac-auto-recovery' }}>
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

  {#if isJackery}
  <section class="card device-card" use:collapsible={{ key: 'learned-parameters', collapsed: true }}>
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
            style="display:flex; gap:10px; align-items:flex-start; justify-content:space-between; flex-wrap:wrap; padding:8px 0; border-bottom:1px solid var(--border-soft)"
          >
            <div style="flex:1; min-width:180px">
              <strong>{p.label || p.key}</strong>
              <div
                style="display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:4px"
              >
                <input
                  class="device-select"
                  type="number"
                  step="any"
                  style="width:120px"
                  disabled={busy}
                  aria-label={`Value for ${p.label || p.key}`}
                  bind:value={paramDrafts[p.key]}
                />
                {#if p.unit}<small class="hint">{p.unit}</small>{/if}
                <span class="hint">
                  {p.source || '—'}
                  {#if p.n_samples} · n={p.n_samples}{/if}
                </span>
              </div>
            </div>
            <div style="display:flex; gap:6px; flex-wrap:wrap">
              <button
                class="btn btn-ghost btn-tiny"
                type="button"
                disabled={busy}
                onclick={() => saveParam(p.key)}
              >
                Save
              </button>
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

  {/if}

  <section class="card device-card" use:collapsible={{ key: 'bluetooth-bms', collapsed: true }}>
    <div class="card-header">
      <h2>Bluetooth BMS</h2>
      <span class="hint">
        {bmsBleOk ? bmsSummary : 'BLE unavailable'}
      </span>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      JBD / Overkill packs assigned to a Siseli inverter replace its voltage-based SOC on Live,
      capacity-weighted across packs. Unassigned packs are polled but not used.
    </p>

    {#if isSiseli}
      <div class="settings-row">
        <label class="settings-label" for="bms-main">
          <span class="settings-label-text">Use packs as main battery display</span>
        </label>
        <div class="settings-control">
          <input id="bms-main" type="checkbox" checked={useAsMain} disabled={busy}
            onchange={(e) => setUseAsMain(e.currentTarget.checked)} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="bms-ignore-soc">
          <span class="settings-label-text">Ignore inverter SOC</span>
          <span class="hint">Use pack SOC only; the inverter's reading is hidden and not stored.</span>
        </label>
        <div class="settings-control">
          <input id="bms-ignore-soc" type="checkbox" checked={!!prefs?.ignore_inverter_soc} disabled={busy}
            onchange={(e) => savePref('ignore_inverter_soc', e.currentTarget.checked)} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="bms-calc-grid">
          <span class="settings-label-text">Calculate grid</span>
          <span class="hint">Estimate grid watts when solar + battery fall short of load (display only).</span>
        </label>
        <div class="settings-control">
          <input id="bms-calc-grid" type="checkbox" checked={!!prefs?.calc_grid} disabled={busy}
            onchange={(e) => savePref('calc_grid', e.currentTarget.checked)} />
        </div>
      </div>
      {#if prefsMsg}<p class="hint" role="status">{prefsMsg}</p>{/if}
    {/if}

    {#if bmsMsg}
      <p class="hint" role="status">{bmsMsg}</p>
    {/if}
    {#if bmsPacks.length}
      <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
        {#each bmsPacks as p (p.mac)}
          {@const soc = packSoc(p)}
          {@const err = packError(p)}
          {@const state = packState(p)}
          {@const age = packAge(p)}
          <li
            style="display:flex; gap:8px; align-items:center; justify-content:space-between; flex-wrap:wrap; padding:8px 0; border-bottom:1px solid var(--border-soft)"
          >
            <div style="flex:1; min-width:200px">
              <strong>{p.alias || p.mac || '—'}</strong>
              <span class="hint">
                · {p.mac}
                {#if soc != null} · {Math.round(soc)}%{/if}
                · {p.capacity_wh ? `${p.capacity_wh} Wh` : 'capacity unset'}
                ·
                {#if state === 'none'}
                  no reading yet
                {:else}
                  <span class="pack-state {state}">{state}</span>
                  · last reading {age}
                {/if}
              </span>
              <div class="hint">→ {inverterName(p.siseli_device_sn)}</div>
              {#if err}
                <div class="hint pack-error" title={err}>⚠ Last poll failed: {err.slice(0, 140)}</div>
              {/if}
            </div>
            <div style="display:flex; gap:6px; flex-wrap:wrap">
              <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={() => editBms(p)}>
                Edit
              </button>
              <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={() => deleteBms(p)}>
                Remove
              </button>
            </div>
          </li>
        {/each}
      </ul>
      {#if isSiseli && !bmsPacksHere.length}
        <p class="hint" style="margin-top:8px">
          No pack is assigned to {inverterName(activeSn)} yet — Edit a pack and pick it under
          “Inverter”.
        </p>
      {/if}
    {:else}
      <p class="hint">No saved BMS packs. Scan or enter a MAC.</p>
    {/if}

    <h3 class="sub-head" id="bms-pack-form">{bmsEditing ? `Edit ${bmsAlias || bmsMac}` : 'Add pack'}</h3>
    <form onsubmit={saveBms}>
      <div class="auto-form-grid">
        <label class="auto-field">
          <span class="auto-label">MAC</span>
          <input type="text" bind:value={bmsMac} placeholder="AA:BB:CC:DD:EE:FF" autocomplete="off" required readonly={bmsEditing} />
        </label>
        <label class="auto-field">
          <span class="auto-label">Alias</span>
          <input type="text" bind:value={bmsAlias} placeholder="Pack 1" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Capacity (Wh)</span>
          <input type="number" min="1" step="1" bind:value={bmsCapacity} placeholder="blank = infer" />
        </label>
        <label class="auto-field">
          <span class="auto-label">Inverter</span>
          <select bind:value={bmsSiseli}>
            <option value="">— unassigned —</option>
            {#each siseliInverters as inv (inv.device_id)}
              <option value={inv.device_sn || inv.device_id}>{inv.name || inv.device_sn || inv.device_id}</option>
            {/each}
          </select>
        </label>
      </div>
      <div style="margin-top:12px; display:flex; gap:8px; flex-wrap:wrap">
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {bmsEditing ? 'Update pack' : 'Save pack'}
        </button>
        {#if bmsEditing}
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={() => { resetBmsForm(); bmsFormMsg = ''; }}>
            Cancel
          </button>
        {/if}
      </div>
    </form>
    {#if bmsFormMsg}
      <p class="hint" style="margin-top:10px" role="status">{bmsFormMsg}</p>
    {/if}

    <div style="margin-top:12px; display:flex; gap:8px; flex-wrap:wrap; align-items:center">
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={scanBms}>
        {busy && bmsMsg === 'Scanning…' ? 'Scanning…' : 'Scan BLE'}
      </button>
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadBms}>
        Refresh
      </button>
      <span class="hint">Scan takes ~8 s.</span>
    </div>
    {#if bmsScan.length}
      <h3 class="sub-head">Scan results</h3>
      <ul style="list-style:none; padding:0; margin:0">
        {#each bmsScan as d, i (`${d.mac ?? i}`)}
          {@const saved = bmsPacks.some((p) => p.mac === d.mac)}
          <li style="display:flex; gap:8px; align-items:center; justify-content:space-between; padding:4px 0">
            <span class="hint">
              {d.name || 'unknown'} · {d.mac || '—'}
              {#if d.likely_jbd} · likely JBD{/if}
              {#if saved} · saved{/if}
            </span>
            <button class="btn btn-ghost btn-tiny" type="button" disabled={busy || !d.mac} onclick={() => pickScanned(d)}>
              {saved ? 'Edit' : 'Use'}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</section>

<style>
  .sub-head {
    margin: 20px 0 10px;
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 0.02em;
    color: var(--text);
  }
  .controls-toolbar {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    align-items: center;
    margin-bottom: 12px;
  }
  .controls-filter {
    flex: 1 1 200px;
    min-width: 0;
    padding: 6px 10px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    color: var(--text);
    font: inherit;
    font-size: 14px;
  }
  .controls-filter:focus {
    outline: none;
    border-color: var(--pow-green);
  }
  .controls-group {
    border-top: 1px solid var(--border-soft);
    padding: 10px 0;
  }
  .controls-group summary {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 8px;
    cursor: pointer;
    font-weight: 600;
    font-size: 14px;
    padding: 4px 0;
  }
  .controls-group summary .hint {
    font-weight: 400;
  }
  .controls-group[open] summary {
    margin-bottom: 10px;
  }
  .inverter-controls-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    align-items: stretch;
    gap: 10px;
  }
  .inverter-control-row {
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    gap: 8px;
    padding: 10px;
    border: 1px solid var(--border-soft);
    border-radius: 10px;
  }
  .inverter-control-row.pinned {
    border-color: var(--pow-green);
    box-shadow: inset 3px 0 0 var(--pow-green);
    background: color-mix(in srgb, var(--pow-green) 6%, transparent);
  }
  .pin-toggle {
    align-self: flex-start;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 3px 10px 3px 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 12px;
    color: var(--text-dim, inherit);
    cursor: pointer;
    user-select: none;
  }
  .pin-toggle input {
    accent-color: var(--pow-green);
  }
  .pin-toggle.on {
    border-color: var(--pow-green);
    color: var(--pow-green);
    font-weight: 600;
  }
  .pack-state.online {
    color: var(--pow-green);
  }
  .pack-state.offline,
  .pack-error {
    color: var(--destructive, #c92a2a);
  }
  .roles-field:disabled label {
    opacity: 0.55;
  }
</style>
