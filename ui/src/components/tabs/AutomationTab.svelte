<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { getString, setString, KEYS } from '$lib/prefs';

  type Subtab = 'controllers' | 'devices' | 'rules' | 'insights';

  type Rule = {
    id: string;
    name?: string;
    enabled?: boolean;
    action?: string;
    operator?: string;
    value?: number;
    trigger?: string;
    kasa_alias?: string;
    kasa_host?: string;
    device_sn?: string;
    jackery_device_sn?: string;
    last_fired?: number | null;
    last_error?: string | null;
  };

  type KasaDevice = {
    host: string;
    alias?: string;
    online?: boolean;
    is_on?: boolean;
    error?: string | null;
  };

  type Suggestion = {
    id?: string;
    title?: string;
    summary?: string;
    reasoning?: string;
    confidence?: string;
    severity?: string;
    kind?: string;
  };

  type DecisionRow = {
    decided_at?: number;
    action?: string;
    reason?: string;
    mode?: string;
    executed?: boolean;
  };

  let subtab = $state<Subtab>(
    (getString(KEYS.automationSubtab, 'controllers') as Subtab) || 'controllers',
  );
  let rules = $state.raw<Rule[]>([]);
  let kasa = $state.raw<KasaDevice[]>([]);
  let msg = $state('');
  let busy = $state(false);

  // Smart charge
  let scMode = $state('off');
  let scKasaHost = $state('');
  let scTargetSoc = $state(25);
  let scMaxChargeW = $state(800);
  let scStatus = $state.raw<Record<string, unknown> | null>(null);
  let scHistory = $state.raw<DecisionRow[]>([]);
  let scMsg = $state('');

  // Solar charge
  let solMode = $state('off');
  let solKasaHost = $state('');
  let solStatus = $state.raw<Record<string, unknown> | null>(null);
  let solHistory = $state.raw<DecisionRow[]>([]);
  let solMsg = $state('');

  // Kasa credentials
  let kasaEmail = $state('');
  let kasaPassword = $state('');
  let kasaCredMsg = $state('');
  let kasaHasCreds = $state(false);

  // Rules form
  let ruleName = $state('');
  let ruleHost = $state('');
  let ruleSn = $state('');
  let ruleThreshold = $state(20);
  let ruleDirection = $state<'below' | 'above'>('below');
  let ruleAction = $state<'on' | 'off'>('on');
  let ruleMsg = $state('');
  let historyRuleId = $state<string | null>(null);
  let ruleFirings = $state.raw<
    { fired_at?: number; action?: string; soc_at_fire?: number; reason?: string }[]
  >([]);

  // Insights
  let insights = $state.raw<{
    pending?: number;
    suggestions?: Suggestion[];
    status?: string;
  } | null>(null);

  const deviceSn = $derived(live.activeDeviceSn());
  const jackeryDevices = $derived(live.devices || []);

  function setSubtab(name: Subtab) {
    subtab = name;
    setString(KEYS.automationSubtab, name);
  }

  function snQ(sn?: string) {
    const s = sn || deviceSn;
    return s ? `?device_sn=${encodeURIComponent(s)}` : '';
  }

  async function loadRules() {
    try {
      const j = await api<{ rules?: Rule[] }>('/automation/rules');
      rules = j.rules || [];
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Failed to load rules';
    }
  }

  async function loadKasa() {
    try {
      const sn = deviceSn;
      const q = sn ? `?jackery_sn=${encodeURIComponent(sn)}` : '';
      const j = await api<{ devices?: KasaDevice[] }>(`/kasa/saved${q}`);
      kasa = j.devices || [];
    } catch {
      try {
        const j = await api<{ devices?: KasaDevice[] }>('/kasa/saved');
        kasa = j.devices || [];
      } catch {
        kasa = [];
      }
    }
  }

  async function loadKasaCreds() {
    try {
      const j = await api<{ has_credentials?: boolean; email?: string }>(
        '/kasa/credentials',
      );
      kasaHasCreds = !!j.has_credentials;
      if (j.email) kasaEmail = j.email;
    } catch {
      kasaHasCreds = false;
    }
  }

  async function loadSmart() {
    const sn = deviceSn;
    if (!sn) {
      scStatus = null;
      scHistory = [];
      return;
    }
    try {
      const q = snQ(sn);
      const [cfgPayload, st] = await Promise.all([
        api<{ config?: Record<string, unknown> }>(`/smart_charge/config${q}`),
        api<Record<string, unknown>>(`/smart_charge/status${q}`).catch(() => null),
      ]);
      const cfg = cfgPayload.config || {};
      scMode = String(cfg.mode ?? 'off');
      scKasaHost = String(cfg.kasa_device_host ?? '');
      scTargetSoc = Number(cfg.target_sunrise_soc_pct ?? cfg.target_soc_pct ?? 25);
      scMaxChargeW = Number(
        cfg.max_charge_w ?? st?.observed_max_charge_w ?? 800,
      );
      scStatus = st;
      scHistory = Array.isArray(st?.history)
        ? (st.history as DecisionRow[]).slice(0, 12)
        : Array.isArray(st?.recent_decisions)
          ? (st.recent_decisions as DecisionRow[]).slice(0, 12)
          : [];
    } catch (err) {
      scMsg = err instanceof Error ? err.message : 'Smart charge load failed';
    }
  }

  async function loadSolar() {
    const sn = deviceSn;
    if (!sn) {
      solStatus = null;
      solHistory = [];
      return;
    }
    try {
      const q = snQ(sn);
      const [cfgPayload, st] = await Promise.all([
        api<{ config?: Record<string, unknown> }>(`/solar_charge/config${q}`),
        api<Record<string, unknown>>(`/solar_charge/status${q}`).catch(() => null),
      ]);
      const cfg = (cfgPayload.config || st?.config || {}) as Record<string, unknown>;
      solMode = String(cfg.mode ?? 'off');
      solKasaHost = String(cfg.kasa_device_host ?? '');
      solStatus = st;
      solHistory = Array.isArray(st?.recent_decisions)
        ? (st.recent_decisions as DecisionRow[]).slice(0, 12)
        : [];
    } catch (err) {
      solMsg = err instanceof Error ? err.message : 'Solar charge load failed';
    }
  }

  async function loadInsights() {
    try {
      const sn = deviceSn;
      const qs = new URLSearchParams({ status: 'pending' });
      if (sn) qs.set('device_sn', sn);
      const j = await api<{
        pending?: number;
        suggestions?: Suggestion[];
        status?: string;
      }>(`/algorithm/suggestions?${qs}`);
      insights = j;
    } catch {
      insights = { status: 'Advisor unavailable', suggestions: [] };
    }
  }

  async function refreshAll() {
    busy = true;
    msg = '';
    try {
      await Promise.all([
        loadRules(),
        loadKasa(),
        loadKasaCreds(),
        loadSmart(),
        loadSolar(),
        loadInsights(),
      ]);
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    void refreshAll();
    return live.onDeviceChange(() => {
      void loadSmart();
      void loadSolar();
      void loadKasa();
      void loadInsights();
      if (!ruleSn) ruleSn = live.activeDeviceSn() || '';
    });
  });

  async function saveSmart(e: SubmitEvent) {
    e.preventDefault();
    const sn = deviceSn;
    if (!sn) {
      scMsg = 'No active device.';
      return;
    }
    busy = true;
    scMsg = 'Saving…';
    try {
      await apiPost(`/smart_charge/config${snQ(sn)}`, {
        mode: scMode,
        kasa_device_host: scKasaHost || null,
        target_sunrise_soc_pct: scTargetSoc,
        max_charge_w: scMaxChargeW,
      });
      scMsg = 'Saved.';
      await loadSmart();
    } catch (err) {
      scMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function evaluateSmart() {
    busy = true;
    scMsg = 'Evaluating…';
    try {
      const j = await apiPost<{ plan?: Record<string, unknown>; narration?: string }>(
        `/smart_charge/evaluate_now${snQ()}`,
      );
      scMsg = j.narration
        ? String(j.narration)
        : j.plan
          ? `Plan: ${String(j.plan.action ?? '—')} — ${String(j.plan.reason ?? '')}`
          : 'No plan (controller off or no device).';
      await loadSmart();
    } catch (err) {
      scMsg = err instanceof Error ? err.message : 'Evaluate failed';
    } finally {
      busy = false;
    }
  }

  async function saveSolar(e: SubmitEvent) {
    e.preventDefault();
    const sn = deviceSn;
    if (!sn) {
      solMsg = 'No active device.';
      return;
    }
    busy = true;
    solMsg = 'Saving…';
    try {
      await apiPost(`/solar_charge/config${snQ(sn)}`, {
        mode: solMode,
        kasa_device_host: solKasaHost || null,
      });
      solMsg = 'Saved.';
      await loadSolar();
    } catch (err) {
      solMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function evaluateSolar() {
    busy = true;
    solMsg = 'Evaluating…';
    try {
      const j = await apiPost<{ plan?: Record<string, unknown>; reason?: string }>(
        `/solar_charge/evaluate_now${snQ()}`,
      );
      solMsg = j.plan
        ? `Plan: ${String(j.plan.action ?? '—')} — ${String(j.plan.reason ?? '')}`
        : j.reason || 'No plan.';
      await loadSolar();
    } catch (err) {
      solMsg = err instanceof Error ? err.message : 'Evaluate failed';
    } finally {
      busy = false;
    }
  }

  async function toggleKasa(d: KasaDevice) {
    busy = true;
    try {
      await apiPost('/kasa/test', { host: d.host, on: !d.is_on });
      await loadKasa();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Toggle failed';
    } finally {
      busy = false;
    }
  }

  async function saveKasaCreds(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    kasaCredMsg = 'Saving…';
    try {
      await apiPost('/kasa/credentials', {
        email: kasaEmail.trim(),
        password: kasaPassword,
      });
      kasaPassword = '';
      kasaCredMsg = 'Credentials saved.';
      await loadKasaCreds();
    } catch (err) {
      kasaCredMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function toggleRule(rule: Rule) {
    busy = true;
    try {
      await apiPost('/automation/rules', { ...rule, enabled: !rule.enabled });
      await loadRules();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Update failed';
    } finally {
      busy = false;
    }
  }

  async function createRule(e: SubmitEvent) {
    e.preventDefault();
    if (!ruleHost) {
      ruleMsg = 'Pick a Kasa host.';
      return;
    }
    if (!ruleSn) {
      ruleMsg = 'Pick a device SN.';
      return;
    }
    busy = true;
    ruleMsg = 'Saving…';
    try {
      const kasaDev = kasa.find((d) => d.host === ruleHost);
      const jdev = jackeryDevices.find((d) => d.device_sn === ruleSn);
      await apiPost('/automation/rules', {
        name: ruleName.trim() || `SOC ${ruleDirection} ${ruleThreshold}%`,
        operator: ruleDirection === 'below' ? '<' : '>',
        value: ruleThreshold,
        action: ruleAction,
        kasa_host: ruleHost,
        kasa_alias: kasaDev?.alias || ruleHost,
        jackery_device_sn: ruleSn,
        jackery_device_name: jdev?.name || jdev?.model_name || ruleSn,
        enabled: true,
        trigger: 'battery_percent',
      });
      ruleMsg = 'Rule created.';
      ruleName = '';
      await loadRules();
    } catch (err) {
      ruleMsg = err instanceof Error ? err.message : 'Create failed';
    } finally {
      busy = false;
    }
  }

  async function deleteRule(rule: Rule) {
    if (!confirm(`Delete rule “${rule.name || rule.id}”?`)) return;
    busy = true;
    try {
      await api(`/automation/rules/${encodeURIComponent(rule.id)}`, {
        method: 'DELETE',
      });
      if (historyRuleId === rule.id) {
        historyRuleId = null;
        ruleFirings = [];
      }
      await loadRules();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Delete failed';
    } finally {
      busy = false;
    }
  }

  async function showRuleHistory(rule: Rule) {
    busy = true;
    historyRuleId = rule.id;
    try {
      const j = await api<{
        firings?: { fired_at?: number; action?: string; soc_at_fire?: number; reason?: string }[];
      }>(`/automation/rules/${encodeURIComponent(rule.id)}/history?days=30`);
      ruleFirings = j.firings || [];
    } catch (err) {
      ruleFirings = [];
      msg = err instanceof Error ? err.message : 'History failed';
    } finally {
      busy = false;
    }
  }

  async function applySuggestion(s: Suggestion) {
    if (!s.id) return;
    busy = true;
    try {
      await apiPost(`/algorithm/suggestions/${encodeURIComponent(s.id)}/apply`);
      await loadInsights();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Apply failed';
    } finally {
      busy = false;
    }
  }

  async function dismissSuggestion(s: Suggestion) {
    if (!s.id) return;
    busy = true;
    try {
      await apiPost(`/algorithm/suggestions/${encodeURIComponent(s.id)}/dismiss`);
      await loadInsights();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Dismiss failed';
    } finally {
      busy = false;
    }
  }

  function fmtFired(ts?: number | null) {
    if (ts == null) return 'never';
    return new Date(ts * 1000).toLocaleString();
  }

  function statusLine(st: Record<string, unknown> | null): string {
    if (!st) return '—';
    const hist = st.history as DecisionRow[] | undefined;
    const recent = st.recent_decisions as DecisionRow[] | undefined;
    const latest = hist?.[0] || recent?.[0];
    if (latest) {
      return `${latest.action || '—'} · ${latest.reason || ''}`.trim();
    }
    return String(st.state ?? st.reason ?? '—');
  }
</script>

<section class="tab-panel" data-active-group={subtab}>
  <div class="automation-subtabs" role="tablist" aria-label="Automation sections">
    {#each ['controllers', 'devices', 'rules', 'insights'] as name (name)}
      <button
        class="automation-subtab"
        class:on={subtab === name}
        type="button"
        role="tab"
        onclick={() => setSubtab(name as Subtab)}
      >
        {name[0].toUpperCase() + name.slice(1)}
      </button>
    {/each}
  </div>

  {#if msg}
    <p class="hint" role="status">{msg}</p>
  {/if}

  {#if subtab === 'controllers'}
    <section class="card device-card">
      <div class="card-header">
        <h2>Smart charge</h2>
        <span class="hint">{scMode}</span>
      </div>
      <p class="hint" style="margin:6px 0 12px">
        Grid plug assist when forecast SOC won't last to sunrise.
      </p>
      <form onsubmit={saveSmart}>
        <div class="settings-row">
          <label class="settings-label" for="sc-mode">
            <span class="settings-label-text">Mode</span>
          </label>
          <div class="settings-control">
            <select id="sc-mode" class="device-select" bind:value={scMode}>
              <option value="off">off</option>
              <option value="test">test (no Kasa toggle)</option>
              <option value="active">active</option>
            </select>
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="sc-kasa">
            <span class="settings-label-text">Kasa plug</span>
          </label>
          <div class="settings-control">
            <select id="sc-kasa" class="device-select" bind:value={scKasaHost}>
              <option value="">— none —</option>
              {#each kasa as d (d.host)}
                <option value={d.host}>{d.alias || d.host} ({d.host.slice(-7)})</option>
              {/each}
            </select>
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="sc-target">
            <span class="settings-label-text">Target sunrise SOC %</span>
          </label>
          <div class="settings-control">
            <input
              id="sc-target"
              class="forecast-input"
              type="number"
              min="5"
              max="95"
              bind:value={scTargetSoc}
            />
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="sc-maxw">
            <span class="settings-label-text">Max charge W</span>
          </label>
          <div class="settings-control">
            <input
              id="sc-maxw"
              class="forecast-input"
              type="number"
              min="100"
              max="10000"
              bind:value={scMaxChargeW}
            />
          </div>
        </div>
        <div style="display:flex; gap:8px; flex-wrap:wrap; margin-top:12px">
          <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={evaluateSmart}>
            Evaluate now
          </button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadSmart}>
            Refresh status
          </button>
        </div>
      </form>
      <p class="hint" style="margin-top:10px" role="status">
        Status: {statusLine(scStatus)}
        {#if scMsg} · {scMsg}{/if}
      </p>
      {#if scHistory.length}
        <h3 style="margin:14px 0 8px; font-size:14px">Recent decisions</h3>
        <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
          {#each scHistory as row, i (`sc-${row.decided_at ?? i}`)}
            <li style="padding:6px 0; border-bottom:1px solid var(--border-soft)">
              <strong>{row.action || '—'}</strong>
              <span class="hint">
                · {fmtFired(row.decided_at)} · {row.reason || ''}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <section class="card device-card">
      <div class="card-header">
        <h2>Excess diversion (solar charge)</h2>
        <span class="hint">{solMode}</span>
      </div>
      <form onsubmit={saveSolar}>
        <div class="settings-row">
          <label class="settings-label" for="sol-mode">
            <span class="settings-label-text">Mode</span>
          </label>
          <div class="settings-control">
            <select id="sol-mode" class="device-select" bind:value={solMode}>
              <option value="off">off</option>
              <option value="test">test (no Kasa toggle)</option>
              <option value="active">active</option>
            </select>
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="sol-kasa">
            <span class="settings-label-text">Kasa plug</span>
          </label>
          <div class="settings-control">
            <select id="sol-kasa" class="device-select" bind:value={solKasaHost}>
              <option value="">— none —</option>
              {#each kasa as d (d.host)}
                <option value={d.host}>{d.alias || d.host} ({d.host.slice(-7)})</option>
              {/each}
            </select>
          </div>
        </div>
        <div style="display:flex; gap:8px; flex-wrap:wrap; margin-top:12px">
          <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={evaluateSolar}>
            Evaluate now
          </button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadSolar}>
            Refresh status
          </button>
        </div>
      </form>
      <p class="hint" style="margin-top:10px" role="status">
        Status: {statusLine(solStatus)}
        {#if solMsg} · {solMsg}{/if}
      </p>
      {#if solHistory.length}
        <h3 style="margin:14px 0 8px; font-size:14px">Recent decisions</h3>
        <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
          {#each solHistory as row, i (`sol-${row.decided_at ?? i}`)}
            <li style="padding:6px 0; border-bottom:1px solid var(--border-soft)">
              <strong>{row.action || '—'}</strong>
              <span class="hint">
                · {fmtFired(row.decided_at)} · {row.reason || ''}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}

  {#if subtab === 'devices'}
    <section class="card">
      <div class="card-header">
        <h2>Kasa devices</h2>
        <span class="hint">{kasa.length} saved</span>
      </div>
      {#if !kasa.length}
        <p class="hint">No saved Kasa plugs yet.</p>
      {:else}
        <ul class="device-params-list" style="list-style:none; padding:0; margin:0">
          {#each kasa as d (d.host)}
            <li
              style="display:flex; gap:12px; align-items:center; justify-content:space-between; padding:8px 0; border-bottom:1px solid var(--border-soft)"
            >
              <div>
                <strong>{d.alias || d.host}</strong>
                <div class="hint">
                  {d.host} · {d.online ? 'online' : 'offline'} · {d.is_on ? 'ON' : 'OFF'}
                  {#if d.error} · {d.error}{/if}
                </div>
              </div>
              <button
                class="btn btn-ghost"
                type="button"
                disabled={busy}
                onclick={() => toggleKasa(d)}
              >
                Turn {d.is_on ? 'off' : 'on'}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
      <button
        class="btn btn-ghost"
        type="button"
        style="margin-top:10px"
        disabled={busy}
        onclick={loadKasa}
      >
        Refresh
      </button>
    </section>

    <section class="card device-card">
      <div class="card-header">
        <h2>Kasa cloud credentials</h2>
        <span class="hint">{kasaHasCreds ? 'credentials saved' : 'no credentials'}</span>
      </div>
      <form onsubmit={saveKasaCreds}>
        <div class="auto-form-grid">
          <label class="auto-field">
            <span class="auto-label">Email</span>
            <input type="email" bind:value={kasaEmail} autocomplete="username" required />
          </label>
          <label class="auto-field">
            <span class="auto-label">Password</span>
            <input
              type="password"
              bind:value={kasaPassword}
              autocomplete="current-password"
              required
            />
          </label>
        </div>
        <button class="btn btn-primary" type="submit" style="margin-top:12px" disabled={busy}>
          Save credentials
        </button>
      </form>
      {#if kasaCredMsg}
        <p class="hint" style="margin-top:10px" role="status">{kasaCredMsg}</p>
      {/if}
    </section>
  {/if}

  {#if subtab === 'rules'}
    <section class="card">
      <div class="card-header">
        <h2>Automation rules</h2>
        <span class="hint">{rules.length}</span>
      </div>
      {#if !rules.length}
        <p class="hint">No rules yet.</p>
      {:else}
        <ul style="list-style:none; padding:0; margin:0">
          {#each rules as rule (rule.id)}
            <li
              style="display:flex; gap:12px; align-items:flex-start; justify-content:space-between; padding:10px 0; border-bottom:1px solid var(--border-soft)"
            >
              <div>
                <strong>{rule.name || rule.id}</strong>
                <div class="hint">
                  SOC {rule.operator || '?'} {rule.value ?? '?'}% →
                  {(rule.action || '').toUpperCase()} · {rule.kasa_alias || rule.kasa_host || '—'}
                  · last {fmtFired(rule.last_fired)}
                  {#if rule.last_error} · err: {rule.last_error}{/if}
                </div>
              </div>
              <div style="display:flex; gap:6px; flex-wrap:wrap">
                <button
                  class="btn btn-ghost btn-tiny"
                  type="button"
                  disabled={busy}
                  onclick={() => toggleRule(rule)}
                >
                  {rule.enabled === false ? 'Enable' : 'Disable'}
                </button>
                <button
                  class="btn btn-ghost btn-tiny"
                  type="button"
                  disabled={busy}
                  onclick={() => showRuleHistory(rule)}
                >
                  History
                </button>
                <button
                  class="btn btn-ghost btn-tiny"
                  type="button"
                  disabled={busy}
                  onclick={() => deleteRule(rule)}
                >
                  Delete
                </button>
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <section class="card device-card">
      <div class="card-header">
        <h2>Create rule</h2>
      </div>
      <form onsubmit={createRule}>
        <div class="auto-form-grid">
          <label class="auto-field">
            <span class="auto-label">Name</span>
            <input type="text" bind:value={ruleName} placeholder="Optional" />
          </label>
          <label class="auto-field">
            <span class="auto-label">Device SN</span>
            <select bind:value={ruleSn} required>
              <option value="">— pick —</option>
              {#each jackeryDevices as d (d.device_id || d.device_sn)}
                <option value={d.device_sn || ''}>
                  {d.name || d.model_name || d.device_sn}
                </option>
              {/each}
            </select>
          </label>
          <label class="auto-field">
            <span class="auto-label">Kasa host</span>
            <select bind:value={ruleHost} required>
              <option value="">— pick —</option>
              {#each kasa as d (d.host)}
                <option value={d.host}>{d.alias || d.host}</option>
              {/each}
            </select>
          </label>
          <label class="auto-field">
            <span class="auto-label">Threshold %</span>
            <input type="number" min="0" max="100" bind:value={ruleThreshold} required />
          </label>
          <label class="auto-field">
            <span class="auto-label">Direction</span>
            <select bind:value={ruleDirection}>
              <option value="below">below (SOC &lt;)</option>
              <option value="above">above (SOC &gt;)</option>
            </select>
          </label>
          <label class="auto-field">
            <span class="auto-label">Action</span>
            <select bind:value={ruleAction}>
              <option value="on">on</option>
              <option value="off">off</option>
            </select>
          </label>
        </div>
        <button class="btn btn-primary" type="submit" style="margin-top:12px" disabled={busy}>
          Create rule
        </button>
      </form>
      {#if ruleMsg}
        <p class="hint" style="margin-top:10px" role="status">{ruleMsg}</p>
      {/if}
    </section>

    {#if historyRuleId}
      <section class="card">
        <div class="card-header">
          <h2>Rule history</h2>
          <span class="hint">{historyRuleId}</span>
        </div>
        {#if !ruleFirings.length}
          <p class="hint">No firings in the last 30 days.</p>
        {:else}
          <ul style="list-style:none; padding:0; margin:0">
            {#each ruleFirings.slice(0, 30) as f, i (`f-${f.fired_at ?? i}`)}
              <li style="padding:6px 0; border-bottom:1px solid var(--border-soft)">
                <strong>{(f.action || '').toUpperCase()}</strong>
                <span class="hint">
                  · {fmtFired(f.fired_at)}
                  {#if f.soc_at_fire != null} · SOC {Math.round(f.soc_at_fire)}%{/if}
                  {#if f.reason} · {f.reason}{/if}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  {/if}

  {#if subtab === 'insights'}
    <section class="card device-card">
      <div class="card-header">
        <h2>AI insights</h2>
        {#if insights?.pending}
          <span class="alg-badge">{insights.pending}</span>
        {/if}
      </div>
      {#if insights?.status}
        <p class="hint" style="margin:6px 0 12px">{insights.status}</p>
      {/if}
      {#if insights?.suggestions?.length}
        <div class="alg-suggestions">
          {#each insights.suggestions as s, i (s.id ?? i)}
            <div class="alg-card" class:alg-anomaly={s.kind === 'anomaly'}>
              <div class="alg-card-head">
                <strong>{s.title || 'Suggestion'}</strong>
                {#if s.confidence}
                  <span class="alg-conf alg-conf-{s.confidence}">{s.confidence}</span>
                {/if}
              </div>
              <p class="alg-reasoning">{s.summary || s.reasoning || ''}</p>
              <div class="alg-actions">
                <button
                  class="btn btn-primary btn-tiny"
                  type="button"
                  disabled={busy || !s.id}
                  onclick={() => applySuggestion(s)}
                >
                  Apply
                </button>
                <button
                  class="btn btn-ghost btn-tiny"
                  type="button"
                  disabled={busy || !s.id}
                  onclick={() => dismissSuggestion(s)}
                >
                  Dismiss
                </button>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <p class="hint">No pending suggestions.</p>
      {/if}
      <button
        class="btn btn-ghost"
        type="button"
        style="margin-top:10px"
        disabled={busy}
        onclick={loadInsights}
      >
        Refresh
      </button>
    </section>
  {/if}
</section>
