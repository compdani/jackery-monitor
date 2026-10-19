<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { fmt, fmtKwh, formatTemp } from '$lib/format';
  import { savingsLines } from '$lib/formatMoney';
  import {
    applyHeroOrder,
    writeHeroOrder,
    readTempUnit,
    KEYS,
    getString,
    setString,
  } from '$lib/prefs';
  import { siseliPinnedControls, viewingEcoflow, viewingSiseli } from '$lib/siseli';
  import { sortable } from '$lib/actions/sortable';
  import { drawLiveChart } from '$lib/charts/canvas';
  import { deriveEod, flowDur, flowEtaLabel, type EodState, type ForecastPayload } from '$lib/eod';
  import { createKasaPlugToggle } from '$lib/kasaToggle.svelte';
  import type { FleetDevice, LinkedEcoflow, SiseliControl } from '$lib/types';

  const FLOW_IDLE_W = 5;
  const IDLE_W = 25;

  let pauseSeconds = $state('600');
  let pauseBusy = $state(false);
  let tempUnit = $state(readTempUnit());
  let eod = $state<EodState>({ kind: 'hidden' });
  // Default collapsed unless the user explicitly expanded (vanilla stores '0' = expanded).
  let packsCollapsed = $state(getString(KEYS.packsCollapse) !== '0');
  let ecoflowCollapsed = $state(getString(KEYS.ecoflowCollapse) !== '0');
  let siseliStatusText = $state('');
  let ecoflowReconnectBusy = $state(false);
  /** Short-lived optimistic EcoFlow output states, keyed `${sn}:${port}`. */
  let efOverride = $state<Record<string, { on: boolean; until: number }>>({});
  let efBusy = $state<Record<string, boolean>>({});
  /** Local override while a save is in flight; null means follow server prefs. */
  let unifiedSolarOverride = $state<boolean | null>(null);

  const acCharge = createKasaPlugToggle({
    controllerName: 'smart-charge',
    configPath: '/smart_charge/config',
    revertWindow: '5 min',
  });
  const solarCharge = createKasaPlugToggle({
    controllerName: 'solar-charge',
    configPath: '/solar_charge/config',
    revertWindow: '30s',
  });

  const t = $derived(live.status?.telemetry ?? null);
  const today = $derived(live.status?.energy?.today);
  const savings = $derived(
    savingsLines(
      live.status?.energy?.today_savings as Parameters<typeof savingsLines>[0],
      live.status?.energy?.cost_plan?.currency || 'USD',
    ),
  );
  const history = $derived(
    (live.status as { history?: unknown[] } | null)?.history ?? t?.history ?? [],
  );

  const soc = $derived(
    t?.system_soc_pct != null ? Number(t.system_soc_pct) : Number(t?.battery_percent ?? NaN),
  );
  const socDisplay = $derived(Number.isFinite(soc) ? Math.round(soc) : null);
  const barPct = $derived(socDisplay != null ? Math.max(0, Math.min(100, socDisplay)) : 0);

  const inW = $derived(Number(t?.input_power_w ?? 0));
  const outW = $derived(Number(t?.output_power_w ?? 0));
  const netW = $derived(inW - outW);

  const batteryMood = $derived({
    charging: netW > IDLE_W,
    discharging: netW < -IDLE_W,
    low: Number(t?.battery_percent ?? 100) <= 20,
  });

  const timeLabel = $derived.by(() => {
    if (!t) return 'Loading…';
    const capacityWh = Number(t.capacity_wh ?? 5040);
    const ttFull = Number(t.time_to_full_h ?? 0);
    const ttEmpty = Number(t.time_remaining_h ?? 0);
    if (Math.abs(netW) < IDLE_W) return 'Idle';
    if (netW > 0) {
      if (ttFull > 0) return `${fmt(ttFull, 1)} h until full`;
      const wh = ((100 - (socDisplay ?? 0)) / 100) * capacityWh;
      const eta = wh / netW;
      return eta > 0 && isFinite(eta) ? `${fmt(eta, 1)} h until full` : 'Charging…';
    }
    if (ttEmpty > 0) return `${fmt(ttEmpty, 1)} h until empty`;
    const wh = ((socDisplay ?? 0) / 100) * capacityWh;
    const eta = wh / Math.abs(netW);
    return eta > 0 && isFinite(eta) ? `${fmt(eta, 1)} h until empty` : 'Discharging…';
  });

  const tempLabel = $derived(
    t?.battery_temp_c != null ? formatTemp(Number(t.battery_temp_c), tempUnit) : '—',
  );
  const hideTemp = $derived(t?.system_soc_pct != null);

  const solarInputs = $derived(Array.isArray(t?.solar_inputs) ? t!.solar_inputs! : []);
  const unifiedSolar = $derived(
    unifiedSolarOverride ?? !!live.status?.device_prefs?.solar_flow_unified,
  );
  const solarW = $derived.by(() => {
    if (!unifiedSolar && solarInputs.length) {
      return Math.max(
        0,
        Math.round(solarInputs.reduce((sum, row) => sum + Math.max(0, Number(row.watts) || 0), 0)),
      );
    }
    return Math.max(0, Math.round(Number(t?.solar_input_w ?? 0)));
  });
  const gridW = $derived(Math.max(0, Math.round(Number(t?.ac_input_w ?? 0))));
  const loadW = $derived(Math.max(0, Math.round(Number(t?.output_power_w ?? 0))));
  const solarLabel = $derived(
    !unifiedSolar && solarInputs.length > 1 ? `SOLAR (${solarInputs.length})` : 'SOLAR',
  );
  const flowEta = $derived(flowEtaLabel(t));

  // ---- Siseli / JJN chrome (mirrors vanilla applySiseliChrome) ----
  const isSiseli = $derived(viewingSiseli(live.status));
  const siseliPanelW = $derived(
    t?.siseli_solar_w != null ? t.siseli_solar_w : (t?.solar_input_w ?? null),
  );
  const siseliPinned = $derived(
    siseliPinnedControls(live.status?.siseli_controls, live.status?.device_prefs?.live_controls),
  );
  const linkedEf = $derived<LinkedEcoflow[]>(
    isSiseli && Array.isArray(t?.linked_ecoflow) ? t!.linked_ecoflow! : [],
  );
  const linkedEfFresh = $derived(linkedEf.filter((u) => u.fresh).length);
  const linkedEfSummary = $derived(
    linkedEf.length === 1
      ? `${linkedEf[0].alias || 'EcoFlow'}${linkedEfFresh ? '' : ' · stale'}`
      : `${linkedEf.length} units · ${linkedEfFresh} live`,
  );

  // ---- Battery packs (mirrors vanilla renderBatteryPacks) ----
  type PackRowView = {
    key: string;
    idx: string | number;
    soc: number | null | undefined;
    flow: string;
    flowClass: 'flow-in' | 'flow-out' | 'flow-idle';
    temp: { text: string; fault: boolean; title: string } | null;
    label: string;
    snTitle: string;
    isMain: boolean;
    needUpgrade: boolean;
  };

  const PACK_TEMP_PLAUSIBLE_C = { min: -20, max: 80 };
  const MAIN_DEFAULT_WH = 5040;

  function packTemp(celsius: number | null | undefined) {
    if (celsius == null || celsius === 999 || Number.isNaN(Number(celsius))) return null;
    const c = Number(celsius);
    const text = formatTemp(c, tempUnit);
    if (c < PACK_TEMP_PLAUSIBLE_C.min || c > PACK_TEMP_PLAUSIBLE_C.max) {
      return {
        text,
        fault: true,
        title:
          `Reported value ${c}°C is outside the plausible Li-ion operating range ` +
          `(${PACK_TEMP_PLAUSIBLE_C.min} to ${PACK_TEMP_PLAUSIBLE_C.max}°C). ` +
          `Probably a BMS fault code or sensor glitch on this pack — not a real temperature ` +
          `reading (display-only; not used).`,
      };
    }
    return { text, fault: false, title: '' };
  }

  const packs = $derived(live.status?.battery_packs ?? []);
  const efBattery = $derived(
    (Array.isArray(t?.linked_ecoflow) ? t!.linked_ecoflow! : []).filter(
      (u) => (u.roles || []).includes('battery') && (u.fresh || u.telemetry?.battery_percent != null),
    ),
  );

  const systemSoc = $derived.by(() => {
    if (t?.system_soc_pct != null) return Number(t.system_soc_pct);
    const isBms = !!(t?.bms_source || packs.some((p) => p.source === 'bms'));
    if (isBms) return t?.battery_percent != null ? Number(t.battery_percent) : null;
    const mainPct = t?.battery_percent;
    const mainWh = Number(t?.main_capacity_wh) || MAIN_DEFAULT_WH;
    if (mainPct == null || !packs.length || !mainWh) return null;
    const packWh = Number(t?.pack_capacity_wh) || mainWh;
    let stored = (Number(mainPct) * mainWh) / 100;
    for (const p of packs) if (p.rb != null) stored += (p.rb * packWh) / 100;
    const pct = (stored / (mainWh + packs.length * packWh)) * 100;
    return Number.isFinite(pct) ? Math.max(0, Math.min(100, pct)) : null;
  });

  const packsView = $derived.by(() => {
    const empty = { show: false, summary: '—', rows: [] as PackRowView[] };
    if (!packs.length && !efBattery.length) return empty;

    const isBms = !!(t?.bms_source || packs.some((p) => p.source === 'bms'));
    const mainPct = t?.battery_percent ?? null;
    const sysTxt = systemSoc != null ? ` · system ${Math.round(systemSoc)}%` : '';
    const withSoc = packs.filter((p) => p.rb != null);
    const avgPack = withSoc.length
      ? withSoc.reduce((s, p) => s + (p.rb || 0), 0) / withSoc.length
      : 0;
    const totalIn = packs.reduce((s, p) => s + (p.ip || 0), 0);
    const upgrades = packs.filter((p) => p.needUpgrade).length;
    const upgTxt = upgrades ? ` · ⬆ ${upgrades} update${upgrades > 1 ? 's' : ''}` : '';
    const offline = packs.filter((p) => p.error).length;
    const errTxt = offline ? ` · ${offline} offline` : '';

    let summary = isBms
      ? `${packs.length} BMS pack${packs.length === 1 ? '' : 's'} · avg ${Math.round(avgPack)}%${sysTxt}${errTxt}`
      : `${packs.length} packs · avg ${Math.round(avgPack)}%${sysTxt} · ${totalIn}W in${upgTxt}`;

    const rows: PackRowView[] = [];
    if (isBms) {
      const inv = t?.inverter_soc_pct ?? t?.main_soc_pct ?? null;
      const ignoreInv = !!(t?.ignore_inverter_soc || live.status?.device_prefs?.ignore_inverter_soc);
      if (inv != null && !ignoreInv) {
        rows.push({
          key: 'main',
          idx: '★',
          soc: inv,
          flow: 'portal',
          flowClass: 'flow-idle',
          temp: null,
          label: 'Inverter (portal)',
          snTitle: 'Siseli cloud-reported SOC',
          isMain: true,
          needUpgrade: false,
        });
      }
    } else if (mainPct != null) {
      rows.push({
        key: 'main',
        idx: '★',
        soc: mainPct,
        flow: '',
        flowClass: 'flow-idle',
        temp: packTemp(t?.battery_temp_c),
        label: 'Main',
        snTitle: 'Host unit (cloud-reported SOC)',
        isMain: true,
        needUpgrade: false,
      });
    }

    packs.forEach((p, i) => {
      const sn = String(p.deviceSn || p.sn || '');
      const ip = p.ip != null ? Math.round(p.ip) : 0;
      const op = p.op != null ? Math.round(p.op) : 0;
      const order = typeof p.deviceOrder === 'number' ? p.deviceOrder : i;
      const label = p.alias || (sn ? `…${sn.slice(-6)}` : isBms ? 'BMS' : 'pack');
      const volts = p.voltage_v != null ? `${Number(p.voltage_v).toFixed(1)}V` : '';
      const flow = p.error
        ? String(p.error).slice(0, 18)
        : ip > 0
          ? `+${ip}W`
          : op > 0
            ? `−${op}W`
            : volts || 'idle';
      rows.push({
        key: `pack:${sn || i}`,
        idx: order + 1,
        soc: p.rb,
        flow,
        flowClass: p.error ? 'flow-idle' : ip > 0 ? 'flow-in' : op > 0 ? 'flow-out' : 'flow-idle',
        temp: packTemp(p.it),
        label,
        snTitle: [sn, volts, p.error].filter(Boolean).join(' · '),
        isMain: false,
        needUpgrade: !!p.needUpgrade,
      });
    });

    // Linked EcoFlow battery-role units appear as pack rows too.
    let efIdx = packs.length;
    const allLinked = Array.isArray(t?.linked_ecoflow) ? t!.linked_ecoflow! : [];
    allLinked.forEach((u, ui) => {
      if (!(u.roles || []).includes('battery')) return;
      const et = u.telemetry || {};
      const soc = et.battery_percent;
      if (soc == null && !u.fresh) return;
      efIdx += 1;
      const solarW = Math.round(Number(et.solar_input_w) || 0);
      const outW = Math.round(Number(et.output_power_w) || 0);
      const flow = !u.fresh ? 'stale' : solarW > 0 ? `+${solarW}W` : outW > 0 ? `−${outW}W` : 'idle';
      rows.push({
        key: `ef:${u.sn || ui}`,
        idx: efIdx,
        soc: soc != null ? Math.round(soc) : null,
        flow,
        flowClass: !u.fresh ? 'flow-idle' : solarW > 0 ? 'flow-in' : outW > 0 ? 'flow-out' : 'flow-idle',
        temp: packTemp(et.battery_temp_c),
        label: u.alias || 'EcoFlow',
        snTitle: [u.sn, u.fresh ? '' : 'stale'].filter(Boolean).join(' · '),
        isMain: false,
        needUpgrade: false,
      });
    });

    if (!rows.length) return empty;

    if (efBattery.length && !packs.length) {
      const efWithSoc = efBattery.filter((u) => u.telemetry?.battery_percent != null);
      const avg = efWithSoc.length
        ? efWithSoc.reduce((s, u) => s + Number(u.telemetry!.battery_percent), 0) / efWithSoc.length
        : 0;
      summary = `${efBattery.length} EcoFlow · avg ${Math.round(avg)}%${sysTxt}`;
    } else if (efBattery.length && packs.length) {
      summary = `${summary} · ${efBattery.length} EcoFlow`;
    }
    return { show: true, summary, rows };
  });

  const ports = ['ac', 'dc', 'usb', 'car'] as const;
  const efPorts = ['ac', 'dc'] as const;
  const showFleet = $derived(live.fleet.length >= 2);
  const selectedId = $derived(live.selectedDeviceId);
  const pauseUntil = $derived(live.status?.cloud?.pause_until ?? null);
  const isPaused = $derived(pauseUntil != null && Number(pauseUntil) > Date.now() / 1000);
  const pauseRemaining = $derived(live.status?.cloud?.pause_remaining_s ?? null);
  const wd = $derived(live.status?.inverter_watchdog);

  const chartInput = $derived(
    live.status
      ? {
          ...live.status,
          history,
          deviceKey: live.selectedDeviceId ?? live.activeDeviceSn() ?? '',
        }
      : null,
  );

  function attachLiveChart(data: typeof chartInput) {
    return (_canvas: HTMLCanvasElement) => {
      if (data) drawLiveChart(data);
    };
  }

  async function fetchEod() {
    const sn = live.activeDeviceSn();
    if (
      live.status?.source === 'siseli' ||
      viewingSiseli(live.status) ||
      viewingEcoflow(live.status)
    ) {
      eod = { kind: 'hidden' };
      return;
    }
    try {
      const url = sn ? `/forecast?device_sn=${encodeURIComponent(sn)}` : '/forecast';
      const j = await api<ForecastPayload>(url);
      eod = deriveEod(j);
    } catch {
      eod = { kind: 'hidden' };
    }
  }

  async function toggleUnified(on: boolean) {
    unifiedSolarOverride = on;
    const deviceId = live.selectedDeviceId;
    if (!deviceId) return;
    try {
      await apiPost('/device_prefs', { device_id: deviceId, solar_flow_unified: on });
      unifiedSolarOverride = null; // follow server again
    } catch (err) {
      unifiedSolarOverride = !on;
      live.showAlert(err instanceof Error ? err.message : 'Failed to save preference');
    }
  }

  onMount(() => {
    applyHeroOrder(document.querySelector('.hero-grid') as HTMLElement | null);
    tempUnit = readTempUnit();
    void fetchEod();
    void acCharge.refresh();
    void solarCharge.refresh();
    const eodTimer = setInterval(() => void fetchEod(), 30 * 60_000);
    const kasaTimer = setInterval(() => {
      void acCharge.refresh();
      void solarCharge.refresh();
    }, 30_000);
    const unsub = live.onDeviceChange(() => {
      unifiedSolarOverride = null;
      siseliStatusText = '';
      eod = { kind: 'hidden' };
      efOverride = {};
      void fetchEod();
      void acCharge.refresh();
      void solarCharge.refresh();
    });
    return () => {
      clearInterval(eodTimer);
      clearInterval(kasaTimer);
      unsub();
    };
  });

  function fleetStatus(d: FleetDevice) {
    const bs = d.battery_status;
    if (bs === 1) return { cls: 'charging', text: 'charging' };
    if (bs === 2) return { cls: 'discharging', text: 'discharging' };
    if (bs === 0) return { cls: 'idle', text: 'idle' };
    const net = (d.solar_w || 0) + (d.ac_input_w || 0) - (d.output_w || 0);
    if (net > 25) return { cls: 'charging', text: 'charging' };
    if (net < -25) return { cls: 'discharging', text: 'discharging' };
    return { cls: 'idle', text: 'idle' };
  }

  function portLabel(port: string): string {
    const on = live.portState(port);
    if (on === true) return 'ON';
    if (on === false) return 'OFF';
    return '—';
  }

  async function togglePort(port: string) {
    const cur = live.portState(port);
    if (cur == null) return;
    // Watchdog error dismiss on AC click
    if (port === 'ac' && wd?.error_message) {
      try {
        await apiPost('/inverter_watchdog/dismiss');
      } catch {
        /* ignore */
      }
    }
    await live.setOutput(port, !cur);
  }

  function togglePacksCollapsed() {
    packsCollapsed = !packsCollapsed;
    setString(KEYS.packsCollapse, packsCollapsed ? '1' : '0');
  }

  function toggleEcoflowCollapsed() {
    ecoflowCollapsed = !ecoflowCollapsed;
    setString(KEYS.ecoflowCollapse, ecoflowCollapsed ? '1' : '0');
  }

  async function writeSiseliSetting(key: string, value: number) {
    const deviceId = live.selectedDeviceId;
    if (!deviceId) return;
    siseliStatusText = 'saving…';
    try {
      const j = await apiPost<{ controls?: SiseliControl[] }>('/siseli/settings', {
        device_id: deviceId,
        key,
        value,
      });
      if (Array.isArray(j?.controls) && live.status) live.status.siseli_controls = j.controls;
      siseliStatusText = 'saved';
    } catch (err) {
      siseliStatusText = err instanceof Error ? err.message : 'save failed';
    }
  }

  function siseliSwitchOn(c: SiseliControl): boolean {
    return !!c.value;
  }

  function onSiseliSwitch(c: SiseliControl) {
    const next = !siseliSwitchOn(c);
    void writeSiseliSetting(c.canonical, Number(next ? c.on_value : c.off_value));
  }

  function efOn(u: LinkedEcoflow, port: 'ac' | 'dc'): boolean {
    const o = efOverride[`${u.sn}:${port}`];
    if (o && o.until > Date.now()) return o.on;
    return !!(port === 'ac' ? u.telemetry?.ac_on : u.telemetry?.dc_on);
  }

  async function toggleLinkedEcoflow(u: LinkedEcoflow, port: 'ac' | 'dc') {
    const sn = String(u.sn || '');
    if (!sn) return;
    const k = `${sn}:${port}`;
    if (efBusy[k]) return;
    const turnOn = !efOn(u, port);
    if (port === 'ac' && !turnOn) {
      if (!confirm('Turn EcoFlow AC output OFF? Anything plugged in will lose power.')) return;
    }
    efBusy[k] = true;
    try {
      const ok = await live.setOutput(port, turnOn, sn);
      if (ok) efOverride[k] = { on: turnOn, until: Date.now() + 10_000 };
    } finally {
      efBusy[k] = false;
    }
  }

  async function reconnectEcoflowMqtt() {
    if (ecoflowReconnectBusy) return;
    ecoflowReconnectBusy = true;
    try {
      await apiPost('/ecoflow/reconnect');
    } catch (err) {
      live.showAlert(`EcoFlow reconnect failed: ${err instanceof Error ? err.message : err}`);
    } finally {
      setTimeout(() => (ecoflowReconnectBusy = false), 800);
    }
  }

  async function togglePause() {
    pauseBusy = true;
    try {
      if (isPaused) await apiPost('/resume_polling');
      else await apiPost('/pause_polling', { seconds: parseInt(pauseSeconds, 10) || 600 });
      await live.refreshStatus();
    } catch (err) {
      live.showAlert(err instanceof Error ? err.message : 'Pause toggle failed');
    } finally {
      pauseBusy = false;
    }
  }
</script>

<section class="tab-panel">
  {#if showFleet}
    <div class="fleet-strip">
      {#each live.fleet as d (d.device_id)}
        {@const st = fleetStatus(d)}
        {@const selected = String(d.device_id) === String(selectedId)}
        {@const socN = d.soc_pct != null && Number.isFinite(d.soc_pct) ? Math.round(d.soc_pct) : null}
        <button
          type="button"
          class="fleet-card {st.cls}"
          class:is-selected={selected}
          aria-pressed={selected}
          onclick={() => live.selectViewDevice(d.device_id)}
        >
          <div class="fleet-card-top">
            <span class="fleet-name">
              {d.name || d.model_name || d.device_sn || d.device_id}
              {#if d.source === 'siseli'}<span class="fleet-source">inverter</span>
              {:else if d.source === 'ecoflow'}<span class="fleet-source">ecoflow</span>{/if}
            </span>
            <span class="fleet-badge {st.cls}">{st.text}</span>
          </div>
          <div class="fleet-soc">
            <span class="fleet-soc-n">{socN ?? '—'}</span><small>%</small>
          </div>
          <div class="fleet-bar">
            <span class="fleet-bar-fill" style="width:{socN != null ? socN : 0}%"></span>
          </div>
          <div class="fleet-meta">
            <span class="fleet-watts">☀ {Math.round(d.solar_w || 0)}W · ⌂ {Math.round(d.output_w || 0)}W</span>
            {#if (d.pack_count ?? 0) > 0}
              <span class="fleet-packs">+{d.pack_count} pack{d.pack_count === 1 ? '' : 's'}</span>
            {/if}
          </div>
        </button>
      {/each}
    </div>
  {/if}

  <div
    class="grid hero-grid"
    use:sortable={{ handle: '.grab-handle', onEnd: (ids) => writeHeroOrder(ids) }}
  >
    <section
      class="card battery-card has-accent"
      data-card="battery"
      class:charging={batteryMood.charging}
      class:discharging={batteryMood.discharging}
      class:low={batteryMood.low}
    >
      <span class="grab-handle" aria-label="Drag to reorder" title="Drag to reorder"></span>
      <div class="card-eyebrow">
        <span>State of charge</span>
        <span class="pill pill-source">{live.status?.source || '—'}</span>
      </div>

      {#if eod.kind === 'calibrating'}
        <div class="eod-forecast calibrating" title={eod.title}>
          <span class="eod-label">Calibrating</span>
          <span class="eod-row">
            <span class="eod-trend"></span>
            <span>{eod.haveH}/{eod.needH} h · {eod.haveW}/{eod.needW} cycles</span>
          </span>
        </div>
      {:else if eod.kind === 'ready'}
        <div
          class="eod-forecast"
          class:eod-pair={eod.showBoth}
          class:low={eod.low}
          title={eod.title}
        >
          <span class="eod-label">{eod.label}</span>
          <span class="eod-row">
            {#if eod.showBoth}
              <span>{eod.peakVal}</span><small>%</small>
              <span class="eod-sep">·</span>
              <span>{eod.sunsetVal}</span><small>%</small>
            {:else}
              <span class="eod-trend {eod.trend}"></span>
              <span>{eod.sunsetVal}</span><small>%</small>
            {/if}
          </span>
        </div>
      {/if}

      <div class="battery-pct">
        <span>{socDisplay ?? '—'}</span><small>%</small>
      </div>
      <div class="battery-bar">
        <div class="battery-bar-fill" style="width:{barPct}%"></div>
      </div>
      <div class="battery-meta">
        <span>{timeLabel}</span>
        {#if !hideTemp}
          <span class="meta-segment">
            <span class="dot-sep">·</span>
            <span>{tempLabel}</span>
          </span>
        {/if}
      </div>
    </section>

    <section class="card kpi-card power-flow has-accent" data-card="power-flow">
      <span class="grab-handle" aria-label="Drag to reorder" title="Drag to reorder"></span>
      <div class="card-eyebrow">
        <span>Power flow</span>
        <span class="hint">
          {#if t?.ac_output_v != null}
            {Math.round(Number(t.ac_output_v))} V
            {#if t.ac_output_hz != null} · {Math.round(Number(t.ac_output_hz))} Hz{/if}
          {:else}
            —
          {/if}
        </span>
        {#if solarInputs.length >= 2}
          <label class="flow-unified-toggle" title="Collapse all solar inputs into one node">
            <input
              type="checkbox"
              checked={unifiedSolar}
              onchange={(e) => toggleUnified((e.currentTarget as HTMLInputElement).checked)}
            />
            <span>Unified solar</span>
          </label>
        {/if}
      </div>

      {#if !unifiedSolar && solarInputs.length >= 2}
        <div class="flow-solar-inputs">
          {#each solarInputs as row, i (row.source || row.label || i)}
            <div class="flow-solar-chip source-{row.source || 'siseli'}">
              <span class="chip-label">{row.label || 'Solar'}</span>
              <span class="chip-w">{Math.round(Number(row.watts) || 0)} W</span>
            </div>
          {/each}
        </div>
      {/if}

      <div class="power-flow-diagram">
        <svg
          class="power-flow-svg"
          viewBox="0 0 1000 300"
          preserveAspectRatio="xMidYMid meet"
          role="img"
          aria-label="Power flow diagram"
        >
          <defs>
            <path id="path-flow-solar" d="M 200 80 L 500 152" />
            <path id="path-flow-grid" d="M 200 224 L 500 152" />
            <path id="path-flow-load" d="M 500 152 L 820 152" />
          </defs>

          <line class="flow-channel solar" class:active={solarW >= FLOW_IDLE_W} x1="200" y1="80" x2="500" y2="152" />
          <line class="flow-channel grid" class:active={gridW >= FLOW_IDLE_W} x1="200" y1="224" x2="500" y2="152" />
          <line class="flow-channel load" class:active={loadW >= FLOW_IDLE_W} x1="500" y1="152" x2="820" y2="152" />

          <g class="flow-dot solar" class:active={solarW >= FLOW_IDLE_W}>
            <circle r="6" />
            <animateMotion dur={flowDur(solarW)} repeatCount="indefinite">
              <mpath href="#path-flow-solar" />
            </animateMotion>
          </g>
          <g class="flow-dot grid" class:active={gridW >= FLOW_IDLE_W}>
            <circle r="6" />
            <animateMotion dur={flowDur(gridW)} repeatCount="indefinite">
              <mpath href="#path-flow-grid" />
            </animateMotion>
          </g>
          <g class="flow-dot load" class:active={loadW >= FLOW_IDLE_W}>
            <circle r="6" />
            <animateMotion dur={flowDur(loadW)} repeatCount="indefinite">
              <mpath href="#path-flow-load" />
            </animateMotion>
          </g>

          <text class="flow-label" x="200" y="32">{solarLabel}</text>
          <g class="flow-node-group">
            <circle class="flow-node solar" class:active={solarW >= FLOW_IDLE_W} cx="200" cy="80" r="40" />
            <text class="flow-icon" x="200" y="80">☀</text>
          </g>
          <text class="flow-w" x="200" y="148"><tspan>{solarW}</tspan> W</text>

          <text class="flow-label" x="200" y="176">GRID</text>
          <g class="flow-node-group">
            <circle class="flow-node grid" class:active={gridW >= FLOW_IDLE_W} cx="200" cy="224" r="40" />
            <text class="flow-icon" x="200" y="224">⚡</text>
          </g>
          <text class="flow-w" x="200" y="290"><tspan>{gridW}</tspan> W</text>

          <text class="flow-label" x="500" y="80">BATTERY</text>
          <g class="flow-node-group">
            <circle
              class="flow-node battery"
              class:charging={solarW + gridW - loadW > IDLE_W}
              class:discharging={solarW + gridW - loadW < -IDLE_W}
              cx="500" cy="152" r="60"
            />
            <text class="flow-soc" x="500" y="144">{socDisplay != null ? `${socDisplay}%` : '—'}</text>
            <text class="flow-eta {flowEta.cls}" x="500" y="178">
              <title>{flowEta.title}</title>
              {flowEta.text}
            </text>
          </g>

          <text class="flow-label" x="820" y="100">LOADS</text>
          <g class="flow-node-group">
            <circle class="flow-node load" class:active={loadW >= FLOW_IDLE_W} cx="820" cy="152" r="40" />
            <text class="flow-icon house" x="820" y="152">⌂</text>
          </g>
          <text class="flow-w" x="820" y="220"><tspan>{loadW}</tspan> W</text>
        </svg>
      </div>
    </section>

    <section class="card kpi-card today has-accent" data-card="today">
      <span class="grab-handle" aria-label="Drag to reorder" title="Drag to reorder"></span>
      <div class="card-eyebrow"><span>Today</span></div>
      <div class="kpi-value">
        <span>{fmtKwh(today?.output_wh)}</span><small>kWh consumed</small>
      </div>
      <div class="kpi-sub">
        <span>{fmtKwh(today?.input_wh)}</span><small>kWh charged</small>
      </div>
      {#if (today?.solar_wh ?? 0) >= 50 && (today?.ac_input_wh ?? 0) >= 50}
        <div class="kpi-sub charged-split">
          <small>☀ {fmtKwh(today?.solar_wh)} solar · ⚡ {fmtKwh(today?.ac_input_wh)} AC kWh</small>
        </div>
      {/if}
      {#if (today?.solar_charge_diverted_wh ?? 0) >= 50}
        <div class="kpi-sub">
          <span>{fmtKwh(today?.solar_charge_diverted_wh)}</span>
          <small>kWh diverted to excess load</small>
        </div>
      {/if}
      {#if savings}
        <div class="savings-row">
          <span class="sav-solar">{savings.solar}</span>
          <span class="sav-grid">{savings.grid}</span>
          <span class="sav-net">{savings.net}</span>
        </div>
      {/if}
    </section>
  </div>

  {#if isSiseli}
    <section class="card" id="siseli-panels-card">
      <div class="card-header"><h2>Solar panels</h2></div>
      <div class="kpi-row panels">
        <div>
          <div class="kpi-value"><span>{fmt(siseliPanelW, 0)}</span><small>W</small></div>
          <div class="kpi-sub">power</div>
        </div>
        <div>
          <div class="kpi-value"><span>{fmt(t?.pv_voltage_v, 1)}</span><small>V</small></div>
          <div class="kpi-sub">voltage</div>
        </div>
        <div>
          <div class="kpi-value"><span>{fmt(t?.pv_current_a, 2)}</span><small>A</small></div>
          <div class="kpi-sub">current</div>
        </div>
      </div>
    </section>
  {/if}

  {#if !isSiseli}
  <section class="card" id="power-card">
    <div class="card-header">
      <h2>Power</h2>
      <span class="hint">Click to toggle</span>
    </div>
    <div class="switches">
      {#each ports as port (port)}
        {@const on = live.portState(port)}
        <button
          class="switch"
          class:on={on === true}
          class:pending={!!live.pendingToggles[port]}
          class:wd-warn={port === 'ac' && !!wd && (wd.consecutive_attempts ?? 0) > 0 && !wd.error_message}
          class:wd-err={port === 'ac' && !!wd?.error_message}
          data-port={port}
          type="button"
          title={port === 'ac' && wd?.error_message
            ? `${wd.error_message}\n\nClick AC to dismiss this error.`
            : port === 'ac' && wd && (wd.consecutive_attempts ?? 0) > 0
              ? `Inverter recovery in progress: cycle ${wd.consecutive_attempts}/2.`
              : ''}
          onclick={() => togglePort(port)}
        >
          <span class="sw-label">{port.toUpperCase()}</span>
          <span class="sw-state">{portLabel(port)}</span>
        </button>
      {/each}
      {#if !acCharge.hidden}
        <button
          class="switch"
          class:on={acCharge.isOn}
          class:pending={acCharge.pending}
          class:warn={acCharge.warn}
          type="button"
          title={acCharge.title}
          onclick={() => acCharge.toggle()}
        >
          <span class="sw-label">AC CHARGE</span>
          <span class="sw-state">{acCharge.stateLabel}</span>
        </button>
      {/if}
      {#if !solarCharge.hidden}
        <button
          class="switch"
          class:on={solarCharge.isOn}
          class:pending={solarCharge.pending}
          class:warn={solarCharge.warn}
          type="button"
          title={solarCharge.title}
          onclick={() => solarCharge.toggle()}
        >
          <span class="sw-label">DIVERT</span>
          <span class="sw-state">{solarCharge.stateLabel}</span>
        </button>
      {/if}
    </div>
  </section>
  {/if}

  {#if isSiseli && siseliPinned.length}
    <section id="siseli-controls-card" class="card">
      <div class="card-header">
        <h2>Inverter controls</h2>
        <span id="siseli-controls-status" class="hint">
          {siseliStatusText || `${siseliPinned.length} on Live`}
        </span>
      </div>
      <p class="hint" style="margin:6px 0 12px">
        Writes go to the Siseli portal (solar.siseli.com). Pin which controls appear here from the Device tab.
      </p>
      <div id="siseli-controls-body" class="siseli-controls">
        {#each siseliPinned as c (c.canonical)}
          {#if c.kind === 'switch'}
            {@const on = siseliSwitchOn(c)}
            <label class="auto-field siseli-control siseli-switch">
              <span class="auto-label">{c.name || c.canonical}</span>
              <button
                type="button"
                class="switch"
                class:on
                aria-pressed={on}
                onclick={() => onSiseliSwitch(c)}
              >
                <span class="sw-state">{on ? 'on' : 'off'}</span>
              </button>
            </label>
          {:else if c.kind === 'select'}
            <label class="auto-field siseli-control">
              <span class="auto-label">{c.name || c.canonical}{c.unit ? ` (${c.unit})` : ''}</span>
              <select
                onchange={(e) =>
                  void writeSiseliSetting(c.canonical, Number(e.currentTarget.value))}
              >
                {#each c.options || [] as o (o.value)}
                  <option value={o.value} selected={Number(o.value) === Number(c.value)}>
                    {o.label}
                  </option>
                {/each}
              </select>
            </label>
          {:else}
            <label class="auto-field siseli-control">
              <span class="auto-label">{c.name || c.canonical}{c.unit ? ` (${c.unit})` : ''}</span>
              <input
                type="number"
                min={c.min}
                max={c.max}
                step={c.step ?? 1}
                value={c.value ?? ''}
                onchange={(e) =>
                  void writeSiseliSetting(c.canonical, Number(e.currentTarget.value))}
              />
            </label>
          {/if}
        {/each}
      </div>
    </section>
  {/if}

  {#if packsView.show}
    <section class="card collapsible" class:collapsed={packsCollapsed} id="battery-packs-card">
      <div
        class="card-header collapsible-header"
        id="battery-packs-toggle"
        role="button"
        tabindex="0"
        aria-expanded={!packsCollapsed}
        aria-controls="battery-packs-list"
        onclick={togglePacksCollapsed}
        onkeydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            togglePacksCollapsed();
          }
        }}
      >
        <h2>Battery packs</h2>
        <span class="hint">{packsView.summary}</span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      <div id="battery-packs-list" class="battery-packs-list">
        {#each packsView.rows as r (r.key)}
          <div class="pack-row" class:pack-row-main={r.isMain}>
            <span class="pack-idx">{r.idx}</span>
            <span class="pack-bar">
              <span
                class="pack-bar-fill"
                style="width:{Math.max(0, Math.min(100, r.soc || 0))}%"
              ></span>
            </span>
            <span class="pack-soc">{r.soc != null ? Math.round(r.soc) : '—'}<small>%</small></span>
            <span class="pack-flow {r.flowClass}">{r.flow}</span>
            <span class="pack-temp">
              {#if r.temp?.fault}
                <span class="pack-temp-fault" title={r.temp.title}>{r.temp.text}</span>
              {:else if r.temp}
                {r.temp.text}
              {/if}
            </span>
            <span class="pack-sn" title={r.snTitle}>
              {r.label}
              {#if r.needUpgrade}
                <span
                  class="pack-fw"
                  title="Firmware update available in the Jackery app"
                  style="color:#EAF044;font:600 9px/1 'Space Mono',monospace;letter-spacing:.5px;border:1px solid #EAF044;border-radius:3px;padding:1px 3px;vertical-align:middle"
                  >UPD</span
                >
              {/if}
            </span>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  {#if isSiseli && linkedEf.length}
    <section
      id="linked-ecoflow-card"
      class="card collapsible"
      class:collapsed={ecoflowCollapsed}
    >
      <div
        class="card-header collapsible-header"
        id="linked-ecoflow-toggle"
        role="button"
        tabindex="0"
        aria-expanded={!ecoflowCollapsed}
        aria-controls="linked-ecoflow-body"
        onclick={toggleEcoflowCollapsed}
        onkeydown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            toggleEcoflowCollapsed();
          }
        }}
      >
        <h2>EcoFlow</h2>
        <span class="hint">{linkedEfSummary}</span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      <div id="linked-ecoflow-body" class="linked-ecoflow-body">
        {#each linkedEf as u, i (u.sn || i)}
          {@const et = u.telemetry || {}}
          {@const d = u.detail || {}}
          {@const socNum = et.battery_percent != null ? Math.round(et.battery_percent) : null}
          {@const status =
            ({ 0: 'idle', 1: 'discharging', 2: 'charging' } as Record<number, string>)[
              Number(et.battery_status)
            ] || '—'}
          {@const temp = packTemp(et.battery_temp_c)}
          <div class="linked-ecoflow-unit">
            <div class="card-header" style="margin-bottom:8px">
              <strong>{u.alias || u.sn || 'EcoFlow'}</strong>
              <span class="hint">{(u.roles || []).join(', ') || 'linked'}{u.fresh ? '' : ' · stale'}</span>
            </div>
            <div class="linked-ecoflow-batt">
              <span class="pack-bar">
                <span
                  class="pack-bar-fill"
                  style="width:{socNum != null ? Math.max(0, Math.min(100, socNum)) : 0}%"
                ></span>
              </span>
              <span class="pack-soc">{socNum ?? '—'}<small>%</small></span>
              <span class="pack-temp">{temp?.text ?? '—'}</span>
            </div>
            <div class="linked-ecoflow-grid">
              <div class="linked-ecoflow-stat">
                <span class="stat-label">Status</span><span class="stat-value">{status}</span>
              </div>
              <div class="linked-ecoflow-stat">
                <span class="stat-label">Solar</span>
                <span class="stat-value">{Math.round(Number(et.solar_input_w) || 0)} W</span>
              </div>
              <div class="linked-ecoflow-stat">
                <span class="stat-label">Output</span>
                <span class="stat-value">{Math.round(Number(et.output_power_w) || 0)} W</span>
              </div>
              <div class="linked-ecoflow-stat">
                <span class="stat-label">Input</span>
                <span class="stat-value">{Math.round(Number(et.input_power_w) || 0)} W</span>
              </div>
              <div class="linked-ecoflow-stat">
                <span class="stat-label">AC in</span>
                <span class="stat-value">{Math.round(Number(et.ac_input_w) || 0)} W</span>
              </div>
              {#if d.pow_get_pv != null}
                <div class="linked-ecoflow-stat">
                  <span class="stat-label">PV1</span>
                  <span class="stat-value">{Math.round(d.pow_get_pv)} W</span>
                </div>
              {/if}
              {#if d.pow_get_pv2 != null}
                <div class="linked-ecoflow-stat">
                  <span class="stat-label">PV2</span>
                  <span class="stat-value">{Math.round(d.pow_get_pv2)} W</span>
                </div>
              {/if}
            </div>
            <div class="switches linked-ecoflow-switches">
              {#each efPorts as port (port)}
                {@const on = efOn(u, port)}
                {@const busy = !!efBusy[`${u.sn}:${port}`]}
                <button
                  class="switch"
                  class:on
                  class:pending={busy}
                  type="button"
                  disabled={!u.fresh || busy}
                  onclick={() => toggleLinkedEcoflow(u, port)}
                >
                  <span class="sw-label">{port.toUpperCase()}</span>
                  <span class="sw-state">{busy ? '…' : on ? 'ON' : 'OFF'}</span>
                </button>
              {/each}
              {#if i === 0}
                <button
                  id="linked-ecoflow-reconnect"
                  type="button"
                  class="btn btn-ghost btn-small linked-ecoflow-reconnect"
                  title="Restart EcoFlow MQTT session"
                  disabled={ecoflowReconnectBusy}
                  onclick={reconnectEcoflowMqtt}
                >
                  Reconnect
                </button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  <section class="card">
    <div class="card-header"><h2>Power last 6 hours</h2></div>
    <canvas id="chart-live" class="chart" {@attach attachLiveChart(chartInput)}></canvas>
  </section>

  <section class="card">
    <div class="card-header">
      <h2>Polling</h2>
      <span class="hint">
        {#if isPaused}
          paused{#if pauseRemaining != null} — {Math.max(0, Math.round(pauseRemaining))}s left{/if}
        {:else}
          live
        {/if}
      </span>
    </div>
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
    </div>
  </section>
</section>
