<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api';

  type LogEvent = {
    ts: number;
    level?: string;
    category?: string;
    message?: string;
    extra?: Record<string, unknown>;
  };

  type DebugBlock = {
    title: string;
    ts: string;
    kind: 'json' | 'text';
    body: string;
  };

  let events = $state.raw<LogEvent[]>([]);
  let filter = $state('all');
  let autoRefresh = $state(true);
  let error = $state('');
  let busy = $state(false);
  let debugStatus = $state('');
  let debugBlocks = $state.raw<DebugBlock[]>([]);

  const levelOrder: Record<string, number> = { error: 0, warn: 1, info: 2 };

  const filtered = $derived.by(() => {
    const list = events.filter((e) => {
      if (filter === 'all') return true;
      if (filter in levelOrder) {
        return (levelOrder[e.level || 'info'] ?? 2) <= levelOrder[filter];
      }
      return e.category === filter;
    });
    return [...list].sort((a, b) => (b.ts || 0) - (a.ts || 0));
  });

  async function loadLogs() {
    busy = true;
    error = '';
    try {
      const j = await api<{ events?: LogEvent[] }>('/events?limit=200');
      events = j.events || [];
    } catch (err) {
      error = err instanceof Error ? err.message : 'Failed to load events';
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    loadLogs();
    const timer = setInterval(() => {
      if (document.hidden || !autoRefresh) return;
      loadLogs();
    }, 5000);
    return () => clearInterval(timer);
  });

  function extras(e: LogEvent): string {
    if (!e.extra) return '';
    return (
      ' · ' +
      Object.entries(e.extra)
        .map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`)
        .join(' ')
    );
  }

  function pushDebug(title: string, data: unknown) {
    const block: DebugBlock = {
      title,
      ts: new Date().toLocaleTimeString(),
      kind: 'json',
      body: typeof data === 'string' ? data : JSON.stringify(data, null, 2),
    };
    debugBlocks = [block, ...debugBlocks];
  }

  async function dbgFetch(path: string): Promise<unknown | null> {
    debugStatus = `Fetching ${path}…`;
    try {
      const j = await api(path);
      debugStatus = '';
      return j;
    } catch (err) {
      debugStatus = `Failed: ${err instanceof Error ? err.message : String(err)}`;
      return null;
    }
  }

  async function dbgForecast24h() {
    const j = (await dbgFetch('/forecast/accuracy')) as {
      samples?: {
        target: number;
        actual_soc?: number | null;
        predicted_soc: number;
        lead_time_h?: number;
      }[];
    } | null;
    if (!j) return;
    const cutoff = Date.now() / 1000 - 24 * 3600;
    const recent = (j.samples || []).filter(
      (s) => s.target >= cutoff && s.actual_soc != null,
    );
    if (!recent.length) {
      pushDebug('Forecast vs actual (last 24h)', { note: 'No predictions with actuals.' });
      return;
    }
    const errs = recent.map((s) => Math.abs(s.predicted_soc - (s.actual_soc as number)));
    const mae = errs.reduce((a, b) => a + b, 0) / errs.length;
    const max = Math.max(...errs);
    const bias =
      recent.reduce((a, s) => a + (s.predicted_soc - (s.actual_soc as number)), 0) /
      recent.length;
    pushDebug(`Forecast vs actual (last 24h, ${recent.length} samples)`, {
      mae_pp: Number(mae.toFixed(1)),
      max_error_pp: Number(max.toFixed(1)),
      bias_pp: Number(bias.toFixed(1)),
      samples: recent.map((s) => ({
        target: new Date(s.target * 1000).toLocaleString(),
        lead_h: s.lead_time_h,
        pred: Math.round(s.predicted_soc),
        actual: Math.round(s.actual_soc as number),
        err_pp: (s.predicted_soc - (s.actual_soc as number)).toFixed(1),
      })),
    });
  }

  async function dbgForecastBuckets() {
    const j = await dbgFetch('/forecast/accuracy');
    if (!j) return;
    pushDebug('Forecast accuracy by lead time', j);
  }

  async function dbgSmartCharge() {
    const j = await dbgFetch('/smart_charge/analytics?days=14');
    if (!j) return;
    pushDebug('Smart-charge analytics', j);
  }

  async function dbgDailySummary() {
    const j = await dbgFetch('/daily_summary?days=7');
    if (!j) return;
    pushDebug('Daily summary (last 7d)', j);
  }

  async function dbgBatteryPacks() {
    const j = await dbgFetch('/devices/battery_packs');
    if (!j) return;
    pushDebug('Battery packs (live)', j);
  }

  async function dbgRawProps() {
    const j = await dbgFetch('/debug/raw_props');
    if (!j) return;
    pushDebug('Raw cloud properties', j);
  }

  async function dbgCloudProbe() {
    const j = (await dbgFetch('/debug/cloud_probe')) as { results?: unknown } | null;
    if (!j) return;
    pushDebug('Cloud probe', j.results ?? j);
  }

  async function dbgCopyAll() {
    debugStatus = 'Gathering all debug data…';
    const safe = async (path: string) => {
      try {
        return await api(path);
      } catch (err) {
        return { error: err instanceof Error ? err.message : String(err), url: path };
      }
    };
    const [
      forecastAccuracy,
      smartChargeAnalytics,
      dailySummary,
      batteryPacks,
      rawProps,
      cloudProbe,
      forecast,
      smartChargeStatus,
      smartChargeConfig,
      status_,
    ] = await Promise.all([
      safe('/forecast/accuracy'),
      safe('/smart_charge/analytics?days=14'),
      safe('/daily_summary?days=14'),
      safe('/devices/battery_packs'),
      safe('/debug/raw_props'),
      safe('/debug/cloud_probe'),
      safe('/forecast'),
      safe('/smart_charge/status'),
      safe('/smart_charge/config'),
      safe('/status'),
    ]);
    const statusObj = status_ as {
      device?: { device_sn?: string; model_code?: string; name?: string };
      telemetry?: unknown;
      error?: string;
    };
    const bundle = {
      captured_at: new Date().toISOString(),
      active_device_sn: statusObj?.device?.device_sn || null,
      active_device_model: statusObj?.device?.model_code || null,
      active_device_name: statusObj?.device?.name || null,
      current_telemetry: statusObj?.telemetry || null,
      forecast_accuracy: forecastAccuracy,
      smart_charge_analytics: smartChargeAnalytics,
      smart_charge_status: smartChargeStatus,
      smart_charge_config: smartChargeConfig,
      daily_summary: dailySummary,
      battery_packs: batteryPacks,
      raw_cloud_props: rawProps,
      cloud_probe: cloudProbe,
      forecast,
    };
    const text = JSON.stringify(bundle, null, 2);
    try {
      await navigator.clipboard.writeText(text);
      debugStatus = `Copied ${(text.length / 1024).toFixed(1)} KB to clipboard.`;
      setTimeout(() => {
        debugStatus = '';
      }, 4000);
    } catch {
      debugStatus = 'Clipboard blocked — bundle rendered below for manual copy.';
      pushDebug('All debug data', bundle);
    }
  }

  function dbgClear() {
    debugBlocks = [];
    debugStatus = '';
  }
</script>

<section class="tab-panel">
  <section class="card">
    <div class="card-header">
      <h2>Bridge events</h2>
      <div class="logs-controls">
        <select class="device-select" bind:value={filter} onchange={() => loadLogs()}>
          <option value="all">All</option>
          <option value="error">Errors</option>
          <option value="warn">Warnings+</option>
          <option value="info">Info+</option>
          <option value="auth">Auth</option>
          <option value="poll">Poll</option>
          <option value="mqtt">MQTT</option>
          <option value="session">Session</option>
          <option value="f7">F7</option>
        </select>
        <label class="logs-auto">
          <input type="checkbox" bind:checked={autoRefresh} /> auto-refresh
        </label>
        <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadLogs}>
          Refresh
        </button>
      </div>
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Recent activity from the bridge — login, polling, MQTT, session contests. Most recent at the
      top.
    </p>
    {#if error}
      <div class="logs-empty">Failed to load: {error}</div>
    {:else if !filtered.length}
      <div class="logs-empty">{busy ? 'Loading…' : 'No events yet.'}</div>
    {:else}
      <div class="logs-list">
        {#each filtered as e, i (`${e.ts}-${e.category}-${i}`)}
          <div class="logs-row lvl-{e.level || 'info'}">
            <span class="lr-ts">
              {new Date(e.ts * 1000).toLocaleTimeString('en-GB', { hour12: false })}
            </span>
            <span class="lr-level">{e.level || '—'}</span>
            <span class="lr-cat">{e.category || '—'}</span>
            <span class="lr-msg">{e.message || ''}{extras(e)}</span>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <section class="card">
    <div class="card-header">
      <h2>Debug panel</h2>
      {#if debugStatus}<span class="hint">{debugStatus}</span>{/if}
    </div>
    <p class="hint" style="margin:6px 0 12px">
      Ad-hoc queries against the local data store. Results render as JSON below (newest first).
    </p>
    <div style="display:flex; gap:8px; flex-wrap:wrap; margin-bottom:12px">
      <button class="btn btn-ghost" type="button" onclick={dbgForecast24h}>
        Forecast vs actual (24h)
      </button>
      <button class="btn btn-ghost" type="button" onclick={dbgForecastBuckets}>
        Forecast accuracy (by lead time)
      </button>
      <button class="btn btn-ghost" type="button" onclick={dbgSmartCharge}>
        Smart-charge analytics
      </button>
      <button class="btn btn-ghost" type="button" onclick={dbgDailySummary}>
        Daily summary (last 7d)
      </button>
      <button class="btn btn-ghost" type="button" onclick={dbgBatteryPacks}>
        Battery packs (live)
      </button>
      <button class="btn btn-ghost" type="button" onclick={dbgRawProps}>Raw cloud props</button>
      <button class="btn btn-ghost" type="button" onclick={dbgCloudProbe}>
        Cloud endpoint probe
      </button>
      <button class="btn btn-primary" type="button" onclick={dbgCopyAll}>Copy all data</button>
      <button class="btn btn-ghost" type="button" onclick={dbgClear}>Clear</button>
    </div>
    {#if debugBlocks.length}
      <div id="debug-output">
        {#each debugBlocks as b, i (`${b.title}-${b.ts}-${i}`)}
          <div class="dbg-block">
            <h3>
              {b.title} <small class="hint">({b.ts})</small>
            </h3>
            <pre class="dbg-pre raw-props" style="max-height:480px; overflow:auto">{b.body}</pre>
          </div>
        {/each}
      </div>
    {/if}
  </section>
</section>
