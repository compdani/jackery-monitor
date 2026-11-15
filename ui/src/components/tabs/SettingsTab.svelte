<script lang="ts">
  import LocalBackups from "./LocalBackups.svelte";
  import { onMount } from 'svelte';
  import { api, apiPost } from '$lib/api';
  import {
    KEYS,
    getString,
    setString,
    settingsCollapseKey,
    readTempUnit,
    writeTempUnit,
    isKeepAwakeOn,
    setKeepAwake,
    type TempUnit,
  } from '$lib/prefs';
  import { setMode, userPrefersMode } from 'mode-watcher';
  import * as ToggleGroup from '$lib/components/ui/toggle-group/index.js';
  import { fmtBytes } from '$lib/format';

  type SettingSpec = {
    key: string;
    label?: string;
    hint?: string;
    value?: number | string | boolean;
    default?: number | string | boolean;
    min?: number;
    max?: number;
    step?: number;
  };

  type Subtab = 'general' | 'intelligence' | 'rates' | 'backup';
  type AiModel = { id: string; display_name?: string; supports_1m?: boolean };
  type TouSlot = {
    start_hour: number;
    end_hour: number;
    rate: number;
    label?: string;
    months?: number[];
  };
  type CostPlan = {
    type?: 'flat' | 'tou' | string;
    name?: string;
    currency?: string;
    rate_per_kwh?: number;
    tou_rates?: TouSlot[];
    slots?: TouSlot[];
  };
  type CostPreset = { id: string; label?: string; name?: string; plan?: CostPlan };
  const ANTHROPIC_1M_SUFFIX = '@1m';
  let subtab = $state<Subtab>(
    (getString(KEYS.settingsSubtab, 'general') as Subtab) || 'general',
  );
  let settings = $state.raw<SettingSpec[]>([]);
  let values = $state<Record<string, string | number>>({});
  let settingsMsg = $state('');
  let tempUnit = $state<TempUnit>(readTempUnit());
  let keepAwake = $state(isKeepAwakeOn());
  let keepAwakeStatus = $state('');
  let wakeLock: WakeLockSentinel | null = null;

  let aiProvider = $state('anthropic');
  let aiMsg = $state('');

  let anthropicLine = $state('—');
  let anthropicChip = $state('');
  let anthropicKey = $state('');
  let anthropicMsg = $state('');
  let anthropicModels = $state.raw<AiModel[]>([]);
  let anthropicModelsMsg = $state('');
  let akAdvisorModel = $state('');
  let akNarratorModel = $state('');
  let akAdvisorEffort = $state('high');

  let openaiLine = $state('—');
  let openaiChip = $state('');
  let openaiKey = $state('');
  let openaiMsg = $state('');
  let openaiModels = $state.raw<AiModel[]>([]);
  let openaiModelsMsg = $state('');
  let oaAdvisorModel = $state('');
  let oaNarratorModel = $state('');
  let oaAdvisorEffort = $state('high');
  type CompatPreset = { id: string; label: string; base_url: string; hint: string };
  let oaCompatPresets = $state.raw<CompatPreset[]>([]);
  let oaCompatPreset = $state('openai');
  let oaBaseUrl = $state('');
  let oaResolvedBaseUrl = $state('');

  let costPlan = $state<CostPlan | null>(null);
  let costPresets = $state.raw<CostPreset[]>([]);
  let costPresetId = $state('__custom_flat__');
  let costFlatRate = $state(0.3);
  let costTouSlots = $state<TouSlot[]>([]);
  let costMsg = $state('');
  let costChip = $state('—');

  let collapsed = $state<Record<string, boolean>>({});
  let busy = $state(false);

  function setSubtab(name: Subtab) {
    subtab = name;
    setString(KEYS.settingsSubtab, name);
  }

  function isCollapsed(key: string, defaultCollapsed = false) {
    if (key in collapsed) return collapsed[key];
    return getString(settingsCollapseKey(key), defaultCollapsed ? '1' : '0') === '1';
  }

  function toggleCollapse(key: string) {
    const next = !isCollapsed(key);
    collapsed = { ...collapsed, [key]: next };
    setString(settingsCollapseKey(key), next ? '1' : '0');
  }

  async function loadSettings() {
    try {
      const j = await api<{ settings?: SettingSpec[] }>('/settings');
      settings = j.settings || [];
      const next: Record<string, string | number> = {};
      for (const s of settings) {
        next[s.key] = (s.value ?? s.default ?? '') as string | number;
      }
      values = next;
    } catch (err) {
      settingsMsg = err instanceof Error ? err.message : 'Failed to load settings';
    }
  }

  async function loadAiProvider() {
    try {
      const j = await api<{ provider?: string }>('/ai/provider');
      aiProvider = j.provider || 'anthropic';
    } catch {
      /* ignore */
    }
  }

  async function loadAnthropic() {
    try {
      const ak = await api<{ has_key?: boolean; source?: string; masked?: string }>('/anthropic/key');
      anthropicChip = ak.has_key ? 'connected' : 'not configured';
      anthropicLine = ak.has_key
        ? ak.source === 'env'
          ? 'Configured via env var on the bridge — UI changes here only affect the on-disk key.'
          : 'API key saved. Advisor + AI narration unlock when Anthropic is active.'
        : 'No key saved. Enter one below to use Anthropic as the active provider.';
    } catch {
      anthropicChip = 'unavailable';
      anthropicLine = 'Status unavailable.';
    }
    try {
      const [modelsJ, prefsJ] = await Promise.all([
        api<{ models?: AiModel[]; source?: string; error?: string }>('/anthropic/models'),
        api<{
          advisor_model?: string;
          advisor_1m_context?: boolean;
          advisor_thinking_effort?: string;
          narrator_model?: string;
        }>('/anthropic/prefs'),
      ]);
      anthropicModels = modelsJ.models || [];
      const adv = prefsJ.advisor_model || '';
      akAdvisorModel = adv
        ? prefsJ.advisor_1m_context
          ? `${adv}${ANTHROPIC_1M_SUFFIX}`
          : adv
        : '';
      akNarratorModel = prefsJ.narrator_model || '';
      akAdvisorEffort = prefsJ.advisor_thinking_effort || 'high';
      anthropicModelsMsg = sourceLabel(modelsJ.source, 'Anthropic', modelsJ.error);
    } catch (err) {
      anthropicModelsMsg = err instanceof Error ? err.message : 'Failed to load models';
    }
  }

  async function loadOpenAI() {
    try {
      const oa = await api<{ has_key?: boolean; source?: string }>('/openai/key');
      openaiChip = oa.has_key ? 'connected' : 'not configured';
      openaiLine = oa.has_key
        ? oa.source === 'env'
          ? 'Configured via env var — UI changes here only affect the on-disk key.'
          : 'API key saved.'
        : 'No key saved. Enter one below to use the OpenAI-compatible provider.';
    } catch {
      openaiChip = 'unavailable';
      openaiLine = 'Status unavailable.';
    }
    try {
      const [modelsJ, prefsJ, compatJ] = await Promise.all([
        api<{ models?: AiModel[]; source?: string; error?: string; base_url?: string }>(
          '/openai/models',
        ),
        api<{
          openai_advisor_model?: string;
          openai_narrator_model?: string;
          openai_advisor_effort?: string;
          openai_compat_preset?: string;
          openai_base_url?: string;
        }>('/anthropic/prefs'),
        api<{
          presets?: CompatPreset[];
          preset?: string;
          base_url?: string;
          resolved_base_url?: string;
        }>('/openai/compat'),
      ]);
      openaiModels = modelsJ.models || [];
      oaAdvisorModel = prefsJ.openai_advisor_model || '';
      oaNarratorModel = prefsJ.openai_narrator_model || '';
      oaAdvisorEffort = prefsJ.openai_advisor_effort || 'high';
      oaCompatPresets = compatJ.presets || [];
      oaCompatPreset = prefsJ.openai_compat_preset || compatJ.preset || 'openai';
      oaResolvedBaseUrl = compatJ.resolved_base_url || modelsJ.base_url || '';
      const savedUrl = prefsJ.openai_base_url || '';
      const presetMeta = oaCompatPresets.find((p) => p.id === oaCompatPreset);
      oaBaseUrl =
        savedUrl ||
        oaResolvedBaseUrl ||
        presetMeta?.base_url ||
        '';
      const vendor =
        oaCompatPresets.find((p) => p.id === oaCompatPreset)?.label || 'OpenAI-compatible';
      openaiModelsMsg = sourceLabel(modelsJ.source, vendor, modelsJ.error);
    } catch (err) {
      openaiModelsMsg = err instanceof Error ? err.message : 'Failed to load models';
    }
  }

  /** Presets where the Base URL field is editable (local / custom). */
  function compatUrlEditable(preset: string) {
    return preset === 'custom' || preset === 'ollama';
  }

  async function saveOpenaiCompat() {
    const preset = oaCompatPresets.find((p) => p.id === oaCompatPreset);
    const body: Record<string, unknown> = { openai_compat_preset: oaCompatPreset };
    if (compatUrlEditable(oaCompatPreset)) {
      // Persist typed URL; empty ollama → fall back to preset default at resolve time.
      body.openai_base_url = oaBaseUrl.trim();
    } else {
      // Cloud presets: always use catalog default (clear override).
      body.openai_base_url = '';
      oaBaseUrl = preset?.base_url || '';
    }
    openaiModelsMsg = 'Saving endpoint…';
    try {
      await apiPost('/anthropic/prefs', body);
      await loadOpenAI();
      openaiModelsMsg = 'Saved. Endpoint applies on the next AI call.';
    } catch (err) {
      openaiModelsMsg = err instanceof Error ? err.message : 'Save failed';
    }
  }

  function onCompatPresetChange() {
    const preset = oaCompatPresets.find((p) => p.id === oaCompatPreset);
    if (!compatUrlEditable(oaCompatPreset)) {
      oaBaseUrl = preset?.base_url || '';
    } else if (!oaBaseUrl.trim() && preset?.base_url) {
      oaBaseUrl = preset.base_url;
    }
    void saveOpenaiCompat();
  }

  function sourceLabel(source: string | undefined, vendor: string, err?: string) {
    const map: Record<string, string> = {
      live: `Live list from ${vendor}.`,
      cache: 'Live list (cached).',
      fallback_no_key: 'No API key saved — showing fallback model list.',
      fallback_no_sdk: `${vendor} SDK not in this image — showing fallback list.`,
      fallback_fetch_failed: `Couldn't reach ${vendor} — showing fallback. ${err || ''}`,
      fallback_empty: `${vendor} returned an empty list — showing fallback.`,
    };
    return map[source || ''] || `Source: ${source || 'unknown'}.`;
  }

  function anthropicAdvisorOptions(): { id: string; label: string }[] {
    const opts: { id: string; label: string }[] = [];
    const seen: Record<string, true> = {};
    for (const m of anthropicModels) {
      if (!seen[m.id]) {
        seen[m.id] = true;
        opts.push({ id: m.id, label: m.display_name || m.id });
      }
      if (m.supports_1m) {
        const id1m = `${m.id}${ANTHROPIC_1M_SUFFIX}`;
        if (!seen[id1m]) {
          seen[id1m] = true;
          opts.push({ id: id1m, label: `${m.display_name || m.id} (1M context)` });
        }
      }
    }
    if (akAdvisorModel && !seen[akAdvisorModel]) {
      opts.push({ id: akAdvisorModel, label: `${akAdvisorModel} (saved)` });
    }
    return opts;
  }

  function modelOptions(models: AiModel[], current: string) {
    const opts = models.map((m) => ({ id: m.id, label: m.display_name || m.id }));
    if (current && !opts.some((o) => o.id === current)) {
      opts.push({ id: current, label: `${current} (saved)` });
    }
    return opts;
  }

  function applyCostPlanToForm(plan: CostPlan | null | undefined) {
    if (!plan) {
      costPlan = null;
      costPresetId = '__custom_flat__';
      costFlatRate = 0.3;
      costTouSlots = [];
      costChip = '—';
      return;
    }
    costPlan = { ...plan };
    const cur = plan.currency || 'USD';
    if (plan.type === 'flat') {
      costFlatRate = Number(plan.rate_per_kwh ?? 0.3);
      costTouSlots = [];
      const rate =
        plan.rate_per_kwh != null
          ? `${cur === 'USD' ? '$' : ''}${Number(plan.rate_per_kwh).toFixed(2)}/kWh`
          : '';
      costChip = ['Flat', rate, cur === 'USD' ? null : cur].filter(Boolean).join(' · ');
    } else {
      const slots = plan.tou_rates || plan.slots || [];
      costTouSlots = slots.map((s) => ({ ...s }));
      costChip = `TOU · ${slots.length} slot${slots.length === 1 ? '' : 's'} · ${cur}`;
    }
    const match = costPresets.find((p) => deepEqualPlan(p.plan, plan));
    if (match) costPresetId = match.id;
    else if (plan.type === 'tou') costPresetId = '__custom_tou__';
    else costPresetId = '__custom_flat__';
  }

  function deepEqualPlan(a?: CostPlan | null, b?: CostPlan | null) {
    if (!a || !b || a.type !== b.type) return false;
    if (a.type === 'flat') return a.rate_per_kwh === b.rate_per_kwh;
    if (a.type === 'tou') {
      const sa = a.tou_rates || a.slots || [];
      const sb = b.tou_rates || b.slots || [];
      if (sa.length !== sb.length) return false;
      for (let i = 0; i < sa.length; i++) {
        if (sa[i].start_hour !== sb[i].start_hour) return false;
        if (sa[i].end_hour !== sb[i].end_hour) return false;
        if (Math.abs(sa[i].rate - sb[i].rate) > 1e-6) return false;
      }
      return true;
    }
    return false;
  }

  async function loadCost() {
    try {
      const j = await api<{ plan?: CostPlan; presets?: CostPreset[] }>('/cost/plan');
      costPresets = j.presets || [];
      applyCostPlanToForm(j.plan || null);
    } catch {
      costPlan = null;
      costChip = '—';
    }
  }

  function onCostPresetChange() {
    if (costPresetId === '__custom_flat__') {
      costPlan = { type: 'flat', currency: costPlan?.currency || 'USD', rate_per_kwh: costFlatRate };
      costTouSlots = [];
      return;
    }
    if (costPresetId === '__custom_tou__') {
      if (!costTouSlots.length) {
        costTouSlots = [
          { start_hour: 0, end_hour: 7, rate: 0.15, label: 'Off-peak' },
          { start_hour: 7, end_hour: 24, rate: 0.3, label: 'Peak' },
        ];
      }
      costPlan = {
        type: 'tou',
        currency: costPlan?.currency || 'USD',
        tou_rates: costTouSlots,
      };
      return;
    }
    const preset = costPresets.find((p) => p.id === costPresetId);
    if (!preset?.plan) return;
    applyCostPlanToForm(JSON.parse(JSON.stringify(preset.plan)) as CostPlan);
    costPresetId = preset.id;
  }

  function addTouSlot() {
    costTouSlots = [
      ...costTouSlots,
      { start_hour: 0, end_hour: 24, rate: 0.2, label: '' },
    ];
    costPresetId = '__custom_tou__';
  }

  function removeTouSlot(i: number) {
    costTouSlots = costTouSlots.filter((_, idx) => idx !== i);
    costPresetId = '__custom_tou__';
  }

  function updateTouSlot(i: number, patch: Partial<TouSlot>) {
    costTouSlots = costTouSlots.map((s, idx) => (idx === i ? { ...s, ...patch } : s));
    costPresetId = '__custom_tou__';
  }

  onMount(() => {
    loadSettings();
    loadAiProvider();
    loadAnthropic();
    loadOpenAI();
    loadCost();
    if (keepAwake) requestWake();
  });

  async function saveSettings(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    settingsMsg = '';
    try {
      const body: Record<string, number> = {};
      for (const [k, v] of Object.entries(values)) {
        const n = typeof v === 'number' ? v : parseFloat(String(v));
        if (Number.isFinite(n)) body[k] = n;
      }
      await apiPost('/settings', body);
      settingsMsg = 'Saved.';
      await loadSettings();
    } catch (err) {
      settingsMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  function setTemp(unit: TempUnit) {
    tempUnit = unit;
    writeTempUnit(unit);
  }

  async function requestWake() {
    keepAwakeStatus = '';
    try {
      if (!('wakeLock' in navigator)) {
        keepAwakeStatus = 'Wake Lock not supported in this browser.';
        return;
      }
      wakeLock = await navigator.wakeLock.request('screen');
      wakeLock.addEventListener('release', () => {
        keepAwakeStatus = 'Wake lock released.';
      });
      keepAwakeStatus = 'Screen stay-awake active.';
    } catch (err) {
      keepAwakeStatus = err instanceof Error ? err.message : 'Wake lock failed';
    }
  }

  async function onKeepAwakeChange() {
    setKeepAwake(keepAwake);
    if (keepAwake) await requestWake();
    else {
      try {
        await wakeLock?.release();
      } catch {
        /* ignore */
      }
      wakeLock = null;
      keepAwakeStatus = '';
    }
  }

  async function saveAiProvider() {
    busy = true;
    aiMsg = '';
    try {
      await apiPost('/ai/provider', { provider: aiProvider });
      aiMsg = 'Provider updated.';
    } catch (err) {
      aiMsg = err instanceof Error ? err.message : 'Failed';
    } finally {
      busy = false;
    }
  }

  async function saveAnthropicKey(e: SubmitEvent) {
    e.preventDefault();
    const key = anthropicKey.trim();
    if (!key) return;
    busy = true;
    anthropicMsg = 'Validating with Anthropic…';
    try {
      await apiPost('/anthropic/key', { api_key: key });
      anthropicKey = '';
      anthropicMsg = 'Saved. Test call succeeded.';
      await loadAnthropic();
    } catch (err) {
      anthropicMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function clearAnthropicKey() {
    if (!confirm('Forget the saved Anthropic API key?')) return;
    busy = true;
    try {
      await api('/anthropic/key', { method: 'DELETE' });
      anthropicMsg = 'Forgotten.';
      await loadAnthropic();
    } catch (err) {
      anthropicMsg = err instanceof Error ? err.message : 'Clear failed';
    } finally {
      busy = false;
    }
  }

  async function saveAiPrefs(body: Record<string, unknown>, label: string, target: 'ak' | 'oa') {
    const setMsg = (m: string) => {
      if (target === 'ak') anthropicModelsMsg = m;
      else openaiModelsMsg = m;
    };
    setMsg(`Saving ${label}…`);
    try {
      await apiPost('/anthropic/prefs', body);
      setMsg(`Saved. ${label} applies on the next advisor/narrator call.`);
    } catch (err) {
      setMsg(err instanceof Error ? err.message : 'Save failed');
    }
  }

  function onAkAdvisorChange() {
    const raw = akAdvisorModel;
    const has1m = raw.endsWith(ANTHROPIC_1M_SUFFIX);
    const model = has1m ? raw.slice(0, -ANTHROPIC_1M_SUFFIX.length) : raw;
    saveAiPrefs(
      { advisor_model: model, advisor_1m_context: has1m },
      `advisor model${has1m ? ' (1M)' : ''}`,
      'ak',
    );
  }

  async function saveOpenAIKey(e: SubmitEvent) {
    e.preventDefault();
    const key = openaiKey.trim();
    if (!key) return;
    busy = true;
    openaiMsg = 'Validating with endpoint…';
    try {
      await apiPost('/openai/key', { api_key: key });
      openaiKey = '';
      openaiMsg = 'Saved. Test call succeeded.';
      await loadOpenAI();
    } catch (err) {
      openaiMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function clearOpenAIKey() {
    if (!confirm('Forget the saved OpenAI API key?')) return;
    busy = true;
    try {
      await api('/openai/key', { method: 'DELETE' });
      openaiMsg = 'Forgotten.';
      await loadOpenAI();
    } catch (err) {
      openaiMsg = err instanceof Error ? err.message : 'Clear failed';
    } finally {
      busy = false;
    }
  }

  function mapTouSlots(): TouSlot[] {
    return costTouSlots.map((s) => {
      const out: TouSlot = {
        start_hour: Number(s.start_hour),
        end_hour: Number(s.end_hour),
        rate: Number(s.rate),
        label: s.label || '',
      };
      if (s.months?.length) out.months = [...s.months];
      return out;
    });
  }

  async function saveCost(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    costMsg = '';
    try {
      const currency = costPlan?.currency || 'USD';
      const name = costPlan?.name;
      let plan: CostPlan;
      const preset = costPresets.find((p) => p.id === costPresetId);
      const wantTou =
        costPresetId === '__custom_tou__' ||
        preset?.plan?.type === 'tou' ||
        (costPlan?.type === 'tou' && costPresetId !== '__custom_flat__');
      if (wantTou) {
        plan = { type: 'tou', currency, name, tou_rates: mapTouSlots() };
      } else {
        plan = { type: 'flat', rate_per_kwh: costFlatRate, currency, name };
      }
      const j = await apiPost<{ plan?: CostPlan }>('/cost/plan', plan);
      applyCostPlanToForm(j.plan || plan);
      costMsg = 'Plan saved.';
    } catch (err) {
      costMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  function monthsLabel(months?: number[]) {
    if (!months?.length) return '';
    const NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
    return months.map((m) => NAMES[m - 1] || String(m)).join(',');
  }

  const showFlat = $derived(
    costPresetId === '__custom_flat__' ||
      costPresets.find((p) => p.id === costPresetId)?.plan?.type === 'flat' ||
      (costPlan?.type === 'flat' && costPresetId !== '__custom_tou__'),
  );
  const showTou = $derived(
    costPresetId === '__custom_tou__' ||
      costPresets.find((p) => p.id === costPresetId)?.plan?.type === 'tou' ||
      costPlan?.type === 'tou',
  );
</script>

<section class="tab-panel">
  <div class="settings-subtabs" role="tablist" aria-label="Settings sections">
    {#each ['general', 'intelligence', 'rates', 'backup'] as name (name)}
      <button
        class="settings-subtab"
        class:on={subtab === name}
        type="button"
        role="tab"
        onclick={() => setSubtab(name as Subtab)}
      >
        {name[0].toUpperCase() + name.slice(1)}
      </button>
    {/each}
  </div>

  {#if subtab === 'general'}
    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('runtime')}
      data-settings-collapse-key="runtime"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('runtime')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('runtime')}
      >
        <h2>Runtime settings</h2>
        {#if settingsMsg}<span class="hint">{settingsMsg}</span>{/if}
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('runtime')}
        <p class="hint" style="margin:6px 0 16px">
          Changes persist to <code>/data/settings.json</code> and apply on the next poll cycle.
        </p>
        <form onsubmit={saveSettings}>
          <div class="settings-fields">
            {#each settings as s (s.key)}
              <div class="settings-row">
                <label class="settings-label" for={`set-${s.key}`}>
                  <span class="settings-label-text">{s.label || s.key}</span>
                  {#if s.hint}<span class="settings-hint">{s.hint}</span>{/if}
                </label>
                <div class="settings-control">
                  <input
                    id={`set-${s.key}`}
                    type="number"
                    min={s.min}
                    max={s.max}
                    step={s.step ?? 'any'}
                    bind:value={values[s.key]}
                  />
                </div>
              </div>
            {/each}
          </div>
          <div style="margin-top:16px; display:flex; gap:8px; align-items:center">
            <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
            <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadSettings}>
              Reload
            </button>
          </div>
        </form>
      {/if}
    </section>

    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('display', true)}
      data-settings-collapse-key="display"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('display')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('display')}
      >
        <h2>Display</h2>
        <span class="hint">
          {tempUnit === 'F' ? '°F' : '°C'} · {userPrefersMode.current ?? 'system'}
        </span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('display', true)}
        <label class="keep-awake-row">
          <input type="checkbox" bind:checked={keepAwake} onchange={onKeepAwakeChange} />
          <span>Keep screen on</span>
          <span class="hint">{keepAwakeStatus}</span>
        </label>
        <div class="temp-unit-row" style="margin-top:14px">
          <span>Temperature</span>
          <div class="temp-unit-pills" role="radiogroup" aria-label="Temperature unit">
            <button
              class="temp-pill"
              class:on={tempUnit === 'C'}
              type="button"
              role="radio"
              aria-checked={tempUnit === 'C'}
              onclick={() => setTemp('C')}
            >
              °C
            </button>
            <button
              class="temp-pill"
              class:on={tempUnit === 'F'}
              type="button"
              role="radio"
              aria-checked={tempUnit === 'F'}
              onclick={() => setTemp('F')}
            >
              °F
            </button>
          </div>
        </div>
        <div class="temp-unit-row" style="margin-top:14px">
          <span>Theme</span>
          <ToggleGroup.Root
            type="single"
            value={userPrefersMode.current ?? 'system'}
            onValueChange={(v) => {
              if (v === 'light' || v === 'dark' || v === 'system') setMode(v);
            }}
            variant="outline"
            size="sm"
            aria-label="Color theme"
          >
            <ToggleGroup.Item value="light" aria-label="Light">Light</ToggleGroup.Item>
            <ToggleGroup.Item value="dark" aria-label="Dark">Dark</ToggleGroup.Item>
            <ToggleGroup.Item value="system" aria-label="System">System</ToggleGroup.Item>
          </ToggleGroup.Root>
        </div>
      {/if}
    </section>
  {/if}

  {#if subtab === 'intelligence'}
    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('ai-provider')}
      data-settings-collapse-key="ai-provider"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('ai-provider')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('ai-provider')}
      >
        <h2>AI provider</h2>
        {#if aiMsg}<span class="hint">{aiMsg}</span>{/if}
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('ai-provider')}
        <div class="settings-row">
          <label class="settings-label" for="ai-provider-select">
            <span class="settings-label-text">Active provider</span>
            <span class="settings-hint"
              >Anthropic (Claude) or OpenAI-compatible (OpenAI, OpenRouter, Groq, Ollama, custom). Uses
              whichever is selected and has a saved key.</span
            >
          </label>
          <div class="settings-control">
            <select id="ai-provider-select" class="device-select" bind:value={aiProvider}>
              <option value="anthropic">Anthropic (Claude)</option>
              <option value="openai">OpenAI-compatible</option>
            </select>
            <button class="btn btn-primary" type="button" disabled={busy} onclick={saveAiProvider}>
              Save
            </button>
          </div>
        </div>
      {/if}
    </section>

    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('anthropic')}
      data-settings-collapse-key="anthropic"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('anthropic')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('anthropic')}
      >
        <h2>Anthropic API key</h2>
        <span class="hint">{anthropicChip}</span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('anthropic')}
        <p class="hint" style="margin:6px 0 12px">
          Optional. Powers the advisor + narration when Anthropic is the active provider. Stored
          encrypted; never sent except to api.anthropic.com.
        </p>
        <p class="hint" style="margin:6px 0 12px">{anthropicLine}</p>
        <form
          style="display:flex; gap:8px; align-items:center; flex-wrap:wrap"
          onsubmit={saveAnthropicKey}
        >
          <input
            type="password"
            autocomplete="off"
            placeholder="sk-ant-…"
            class="device-select"
            style="min-width:280px; flex:1"
            bind:value={anthropicKey}
          />
          <button class="btn btn-primary" type="submit" disabled={busy}>Save & test</button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={clearAnthropicKey}>
            Forget
          </button>
        </form>
        {#if anthropicMsg}<p class="hint" role="status">{anthropicMsg}</p>{/if}

        <div style="margin-top:18px">
          <h3 style="margin:0 0 6px; font-size:13px; color:var(--text-dim); font-weight:500">
            Model selection
          </h3>
          <div class="settings-row">
            <label class="settings-label" for="ak-advisor-model">
              <span class="settings-label-text">Advisor model</span>
            </label>
            <div class="settings-control">
              <select
                id="ak-advisor-model"
                class="device-select"
                bind:value={akAdvisorModel}
                onchange={onAkAdvisorChange}
                disabled={!anthropicModels.length && !akAdvisorModel}
              >
                {#each anthropicAdvisorOptions() as o (o.id)}
                  <option value={o.id}>{o.label}</option>
                {/each}
              </select>
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="ak-advisor-effort">
              <span class="settings-label-text">Thinking effort</span>
            </label>
            <div class="settings-control">
              <select
                id="ak-advisor-effort"
                class="device-select"
                bind:value={akAdvisorEffort}
                onchange={() =>
                  saveAiPrefs({ advisor_thinking_effort: akAdvisorEffort }, 'thinking effort', 'ak')}
              >
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
              </select>
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="ak-narrator-model">
              <span class="settings-label-text">Narrator model</span>
            </label>
            <div class="settings-control">
              <select
                id="ak-narrator-model"
                class="device-select"
                bind:value={akNarratorModel}
                onchange={() =>
                  saveAiPrefs({ narrator_model: akNarratorModel }, 'narrator model', 'ak')}
                disabled={!anthropicModels.length && !akNarratorModel}
              >
                {#each modelOptions(anthropicModels, akNarratorModel) as o (o.id)}
                  <option value={o.id}>{o.label}</option>
                {/each}
              </select>
            </div>
          </div>
          {#if anthropicModelsMsg}
            <div class="hint" style="margin-top:6px">{anthropicModelsMsg}</div>
          {/if}
        </div>
      {/if}
    </section>

    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('openai')}
      data-settings-collapse-key="openai"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('openai')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('openai')}
      >
        <h2>OpenAI-compatible</h2>
        <span class="hint">{openaiChip}</span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('openai')}
        <p class="hint" style="margin:6px 0 12px">
          Optional. Powers the advisor, narration, and AI Assistant when OpenAI-compatible is the
          active provider. Stored encrypted; sent only to the Base URL you configure.
        </p>
        <p class="hint" style="margin:6px 0 12px">{openaiLine}</p>

        <div class="settings-row">
          <label class="settings-label" for="oa-compat-preset">
            <span class="settings-label-text">Endpoint</span>
            <span class="settings-hint"
              >{oaCompatPresets.find((p) => p.id === oaCompatPreset)?.hint ||
                'OpenAI Chat Completions–compatible API'}</span
            >
          </label>
          <div class="settings-control">
            <select
              id="oa-compat-preset"
              class="device-select"
              bind:value={oaCompatPreset}
              onchange={onCompatPresetChange}
            >
              {#each oaCompatPresets as p (p.id)}
                <option value={p.id}>{p.label}</option>
              {:else}
                <option value="openai">OpenAI</option>
                <option value="openrouter">OpenRouter</option>
                <option value="groq">Groq</option>
                <option value="ollama">Ollama</option>
                <option value="custom">Custom</option>
              {/each}
            </select>
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="oa-base-url">
            <span class="settings-label-text">Base URL</span>
            <span class="settings-hint"
              >Must end with <code>/v1</code> for most gateways. Leave blank for official OpenAI.
              {#if oaResolvedBaseUrl && oaCompatPreset !== 'custom'}
                Resolved: {oaResolvedBaseUrl}
              {/if}
            </span>
          </label>
          <div class="settings-control" style="display:flex; gap:8px; flex-wrap:wrap; align-items:center">
            <input
              id="oa-base-url"
              type="url"
              class="device-select"
              style="min-width:280px; flex:1"
              placeholder={oaCompatPreset === 'openai'
                ? '(default api.openai.com/v1)'
                : 'https://…/v1'}
              bind:value={oaBaseUrl}
              disabled={!compatUrlEditable(oaCompatPreset)}
              onchange={() => {
                if (compatUrlEditable(oaCompatPreset)) void saveOpenaiCompat();
              }}
            />
            {#if compatUrlEditable(oaCompatPreset)}
              <button class="btn btn-ghost" type="button" disabled={busy} onclick={saveOpenaiCompat}>
                Save URL
              </button>
            {/if}
          </div>
        </div>

        <form
          style="display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:8px"
          onsubmit={saveOpenAIKey}
        >
          <input
            type="password"
            autocomplete="off"
            placeholder={oaCompatPreset === 'openrouter'
              ? 'sk-or-…'
              : oaCompatPreset === 'groq'
                ? 'gsk_…'
                : 'sk-…'}
            class="device-select"
            style="min-width:280px; flex:1"
            bind:value={openaiKey}
          />
          <button class="btn btn-primary" type="submit" disabled={busy}>Save & test</button>
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={clearOpenAIKey}>
            Forget
          </button>
        </form>
        {#if openaiMsg}<p class="hint" role="status">{openaiMsg}</p>{/if}

        <div style="margin-top:18px">
          <h3 style="margin:0 0 6px; font-size:13px; color:var(--text-dim); font-weight:500">
            Model selection
          </h3>
          <div class="settings-row">
            <label class="settings-label" for="oa-advisor-model">
              <span class="settings-label-text">Advisor model</span>
            </label>
            <div class="settings-control">
              <select
                id="oa-advisor-model"
                class="device-select"
                bind:value={oaAdvisorModel}
                onchange={() =>
                  saveAiPrefs({ openai_advisor_model: oaAdvisorModel }, 'OpenAI advisor model', 'oa')}
                disabled={!openaiModels.length && !oaAdvisorModel}
              >
                {#each modelOptions(openaiModels, oaAdvisorModel) as o (o.id)}
                  <option value={o.id}>{o.label}</option>
                {/each}
              </select>
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="oa-advisor-effort">
              <span class="settings-label-text">Reasoning effort</span>
            </label>
            <div class="settings-control">
              <select
                id="oa-advisor-effort"
                class="device-select"
                bind:value={oaAdvisorEffort}
                onchange={() =>
                  saveAiPrefs(
                    { openai_advisor_effort: oaAdvisorEffort },
                    'OpenAI reasoning effort',
                    'oa',
                  )}
              >
                <option value="low">Low</option>
                <option value="medium">Medium</option>
                <option value="high">High</option>
              </select>
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="oa-narrator-model">
              <span class="settings-label-text">Narrator model</span>
            </label>
            <div class="settings-control">
              <select
                id="oa-narrator-model"
                class="device-select"
                bind:value={oaNarratorModel}
                onchange={() =>
                  saveAiPrefs(
                    { openai_narrator_model: oaNarratorModel },
                    'OpenAI narrator model',
                    'oa',
                  )}
                disabled={!openaiModels.length && !oaNarratorModel}
              >
                {#each modelOptions(openaiModels, oaNarratorModel) as o (o.id)}
                  <option value={o.id}>{o.label}</option>
                {/each}
              </select>
            </div>
          </div>
          {#if openaiModelsMsg}
            <div class="hint" style="margin-top:6px">{openaiModelsMsg}</div>
          {/if}
        </div>
      {/if}
    </section>
  {/if}

  {#if subtab === 'rates'}
    <section
      class="card device-card collapsible"
      class:collapsed={isCollapsed('cost')}
      data-settings-collapse-key="cost"
    >
      <div
        class="card-header collapsible-header"
        role="button"
        tabindex="0"
        onclick={() => toggleCollapse('cost')}
        onkeydown={(e) => e.key === 'Enter' && toggleCollapse('cost')}
      >
        <h2>Electricity rates</h2>
        <span class="hint">{costChip}</span>
        <span class="collapsible-chevron" aria-hidden="true">▾</span>
      </div>
      {#if !isCollapsed('cost')}
        <p class="hint" style="margin:6px 0 16px">
          Used to convert kWh into dollar savings on the dashboard. Pick a preset or enter a flat /
          TOU plan.
        </p>
        {#if costMsg}<p class="hint" role="status">{costMsg}</p>{/if}
        <form onsubmit={saveCost}>
          <div class="settings-row">
            <label class="settings-label" for="cost-name">
              <span class="settings-label-text">Name</span>
            </label>
            <div class="settings-control">
              <input
                id="cost-name"
                class="device-select"
                value={costPlan?.name ?? ''}
                oninput={(e) => {
                  costPlan = { ...(costPlan || {}), name: e.currentTarget.value };
                }}
              />
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="cost-currency">
              <span class="settings-label-text">Currency</span>
            </label>
            <div class="settings-control">
              <input
                id="cost-currency"
                class="device-select"
                value={costPlan?.currency ?? 'USD'}
                oninput={(e) => {
                  costPlan = { ...(costPlan || {}), currency: e.currentTarget.value };
                }}
              />
            </div>
          </div>
          <div class="settings-row">
            <label class="settings-label" for="cost-preset">
              <span class="settings-label-text">Plan</span>
              <span class="settings-hint">Pick a built-in preset, or Custom flat / Custom TOU.</span>
            </label>
            <div class="settings-control">
              <select
                id="cost-preset"
                class="device-select"
                bind:value={costPresetId}
                onchange={onCostPresetChange}
              >
                {#each costPresets as p (p.id)}
                  <option value={p.id}>{p.label || p.name || p.id}</option>
                {/each}
                <option value="__custom_flat__">Custom flat rate</option>
                <option value="__custom_tou__">Custom TOU</option>
              </select>
            </div>
          </div>

          {#if showFlat && !showTou}
            <div class="settings-row">
              <label class="settings-label" for="cost-flat-rate">
                <span class="settings-label-text">$/kWh (flat)</span>
              </label>
              <div class="settings-control">
                <input
                  id="cost-flat-rate"
                  type="number"
                  step="0.01"
                  min="0"
                  max="5"
                  bind:value={costFlatRate}
                />
              </div>
            </div>
          {/if}

          {#if showTou}
            <div class="settings-row">
              <div class="settings-label">
                <span class="settings-label-text">TOU slots</span>
                <span class="settings-hint">Hours are 0–24. Rates in currency/kWh.</span>
              </div>
              <div class="settings-control" style="flex-direction:column; align-items:stretch; gap:8px">
                {#each costTouSlots as slot, i (i)}
                  <div class="cost-tou-slot" style="display:flex; gap:8px; flex-wrap:wrap; align-items:center">
                    <input
                      id={i === 0 ? 'cost-tou-start-0' : undefined}
                      type="number"
                      min="0"
                      max="24"
                      step="1"
                      title="Start hour"
                      aria-label={`Slot ${i + 1} start hour`}
                      style="width:64px"
                      value={slot.start_hour}
                      oninput={(e) =>
                        updateTouSlot(i, { start_hour: Number(e.currentTarget.value) })}
                    />
                    <span class="hint">–</span>
                    <input
                      type="number"
                      min="0"
                      max="24"
                      step="1"
                      title="End hour"
                      style="width:64px"
                      value={slot.end_hour}
                      oninput={(e) => updateTouSlot(i, { end_hour: Number(e.currentTarget.value) })}
                    />
                    <input
                      type="number"
                      step="0.001"
                      min="0"
                      max="5"
                      title="Rate"
                      style="width:90px"
                      value={slot.rate}
                      oninput={(e) => updateTouSlot(i, { rate: Number(e.currentTarget.value) })}
                    />
                    <span class="hint">/kWh</span>
                    <input
                      type="text"
                      placeholder="label"
                      style="min-width:100px"
                      value={slot.label || ''}
                      oninput={(e) => updateTouSlot(i, { label: e.currentTarget.value })}
                    />
                    {#if slot.months?.length}
                      <span class="hint">{monthsLabel(slot.months)}</span>
                    {/if}
                    <button class="btn btn-ghost" type="button" onclick={() => removeTouSlot(i)}>
                      Remove
                    </button>
                  </div>
                {/each}
                <button class="btn btn-ghost" type="button" onclick={addTouSlot}>Add slot</button>
              </div>
            </div>
          {/if}

          <div style="margin-top:16px; display:flex; gap:8px; align-items:center">
            <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
            <button class="btn btn-ghost" type="button" disabled={busy} onclick={loadCost}>
              Reload
            </button>
          </div>
        </form>
      {/if}
    </section>
  {/if}

  {#if subtab === 'backup'}
    <LocalBackups />
  {/if}
</section>
