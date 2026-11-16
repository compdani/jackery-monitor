<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost } from '$lib/api';

  type Config = {
    configured?: boolean;
    client_id?: string;
    client_secret?: string;
    has_client_secret?: boolean;
    vin?: string;
    region?: string;
    domain?: string;
    redirect_uri?: string;
    nav_transport?: string;
    nav_order_mode?: string;
    nav_stop_delay_seconds?: number;
    google_api_key?: string;
    has_google_api_key?: boolean;
    charge_efficiency?: number;
    home_radius_m?: number;
    poll_idle_minutes?: number;
    poll_charging_minutes?: number;
    polling_disabled?: boolean;
  };
  type Sample = {
    ts: number;
    charging_state?: string;
    battery_level?: number;
    charge_limit_soc?: number;
    charger_power?: number;
    charge_energy_added?: number;
  };
  type Session = {
    key: string;
    start_ts: number;
    end_ts: number;
    active: boolean;
    start_level: number;
    end_level: number;
    at_home: boolean;
    fast_charger: boolean;
    energy_added_kwh: number;
    energy_wall_kwh: number;
    cost: number;
    currency?: string;
    unpriced_kwh?: number;
  };
  type Status = {
    configured: boolean;
    connected?: boolean;
    has_key?: boolean;
    login_required?: boolean;
    vin?: string;
    domain?: string;
    public_key_url?: string;
    pairing_url?: string;
    redirect_uri?: string;
    vehicle_state?: string;
    last_poll?: number;
    next_poll?: number;
    last_error?: string;
    refresh_token_expires_at?: number;
    scope?: string;
    charge?: Sample;
    active_session?: Session;
    last_navigation?: NavJob;
  };
  type NavJob = {
    status: string;
    started: number;
    error?: string;
    options?: Record<string, string>;
    results?: { index: number; order: number; ok: boolean; error?: string; place_id?: string; address?: string }[];
  };

  let status = $state<Status | null>(null);
  let cfg = $state<Config>({ region: 'na', nav_transport: 'signed', nav_order_mode: 'enum' });
  let secret = $state('');
  let googleKey = $state('');
  let msg = $state('');
  let busy = $state(false);
  let appToken = $state('');
  let tokenDays = $state(365);
  let sessions = $state.raw<Session[]>([]);
  let totals = $state<{ cost: number; kwh: number; currency: string }>({ cost: 0, kwh: 0, currency: 'USD' });

  const fmtTime = (ts?: number) =>
    ts ? new Date(ts * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : '—';
  const money = (v: number, cur = totals.currency) =>
    new Intl.NumberFormat(undefined, { style: 'currency', currency: cur || 'USD' }).format(v || 0);

  const chip = $derived(
    !status?.configured
      ? 'not set up'
      : status.login_required
        ? 'sign in again'
        : status.connected
          ? `connected${status.vehicle_state ? ` · ${status.vehicle_state}` : ''}`
          : 'not signed in',
  );

  async function load() {
    try {
      const [st, c, ss] = await Promise.all([
        api<Status>('/tesla/status'),
        api<Config>('/tesla/config'),
        api<{ sessions: Session[]; total_cost: number; total_wall_kwh: number; currency: string }>(
          '/tesla/charging/sessions?limit=20',
        ),
      ]);
      status = st;
      if (c.configured) cfg = c;
      sessions = ss.sessions || [];
      totals = { cost: ss.total_cost, kwh: ss.total_wall_kwh, currency: ss.currency || 'USD' };
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Failed to load Tesla status';
    }
  }

  async function run(label: string, fn: () => Promise<string | void>) {
    busy = true;
    msg = `${label}…`;
    try {
      msg = (await fn()) || `${label}: done.`;
      await load();
    } catch (err) {
      msg = `${label} failed: ${err instanceof Error ? err.message : String(err)}`;
    } finally {
      busy = false;
    }
  }

  function saveConfig(e: SubmitEvent) {
    e.preventDefault();
    return run('Save', async () => {
      const body: Config = {
        ...cfg,
        client_secret: secret.trim() || undefined,
        google_api_key: googleKey.trim() || undefined,
      };
      delete body.configured;
      delete body.has_client_secret;
      delete body.has_google_api_key;
      await apiPost('/tesla/config', body);
      secret = '';
      googleKey = '';
      return 'Saved.';
    });
  }

  const genKey = (rotate = false) => {
    if (rotate && !confirm('Make a new key? The car must be paired again and the domain registered again.')) return;
    return run(rotate ? 'New key' : 'Generate key', async () => {
      const j = await apiPost<{ created: boolean }>('/tesla/key', rotate ? { rotate: true } : {});
      return j.created ? 'Key created. Register the domain next.' : 'A key already exists.';
    });
  };

  const register = () =>
    run('Register domain', async () => {
      const j = await apiPost<{ ok: boolean; status: number; tesla: unknown }>('/tesla/partner/register', {});
      return j.ok ? 'Domain registered with Tesla.' : `Tesla answered ${j.status}: ${JSON.stringify(j.tesla)}`;
    });

  const signIn = () =>
    run('Sign in', async () => {
      const j = await api<{ url: string }>('/tesla/oauth/start');
      window.location.href = j.url;
      return 'Opening Tesla sign-in…';
    });

  const forget = () => {
    if (!confirm('Disconnect the Tesla account? Charging tracking and sending stops will stop.')) return;
    return run('Disconnect', async () => {
      await apiPost('/tesla/oauth/forget', {});
    });
  };

  const wake = () =>
    run('Wake car', async () => {
      const j = await apiPost<{ state: string }>('/tesla/wake', {});
      return `Car is ${j.state}.`;
    });

  const poll = () =>
    run('Check charging', async () => {
      await apiPost('/tesla/charging/poll', {});
      await new Promise((r) => setTimeout(r, 4000));
      return 'Checked.';
    });

  const makeToken = () =>
    run('Create app token', async () => {
      const j = await apiPost<{ token: string; expires_in_days: number }>('/tesla/app_token', { days: tokenDays });
      appToken = j.token;
      return `Token created, valid ${j.expires_in_days} days. Copy it now: it is not shown again.`;
    });

  async function copyToken() {
    try {
      await navigator.clipboard.writeText(appToken);
      msg = 'Token copied.';
    } catch {
      msg = 'Copy failed: select the token and copy it by hand.';
    }
  }

  onMount(load);
</script>

<section class="card device-card">
  <div class="card-header">
    <h2>Tesla</h2>
    <span class="hint">{chip}</span>
  </div>
  <p class="hint" style="margin:6px 0 12px">
    Charging cost tracking and the TeslaHelper app (sends Amazon Flex stops to the car). Set up in
    order: save, key, register, sign in, pair.
  </p>
  {#if msg}<p class="hint" role="status">{msg}</p>{/if}

  {#if status}
    <ol class="tesla-steps">
      <li class:done={status.configured}>Config saved</li>
      <li class:done={status.has_key}>
        Key ready
        {#if status.public_key_url}
          · <a href={status.public_key_url} target="_blank" rel="noreferrer">public key</a>
        {/if}
      </li>
      <li>Domain registered (Tesla only says so when you register)</li>
      <li class:done={status.connected && !status.login_required}>
        Tesla account {status.login_required ? 'needs sign-in again' : status.connected ? 'connected' : 'not connected'}
        {#if status.refresh_token_expires_at}
          · sign-in good until {fmtTime(status.refresh_token_expires_at)}
        {/if}
      </li>
      <li>
        Key paired with the car
        {#if status.pairing_url}
          · open <a href={status.pairing_url} target="_blank" rel="noreferrer">{status.pairing_url}</a> on the phone with the
          Tesla app
        {/if}
      </li>
    </ol>
    {#if status.last_error}<p class="hint" style="color:var(--danger, #c33)">Last error: {status.last_error}</p>{/if}
  {/if}
</section>

<section class="card device-card">
  <div class="card-header"><h2>Tesla setup</h2></div>
  <p class="hint" style="margin:6px 0 12px">
    From your app at developer.tesla.com. Its redirect URI must be
    <code>{cfg.redirect_uri || `https://${cfg.domain || 'your-domain'}/api/tesla/oauth/callback`}</code>.
  </p>
  <form onsubmit={saveConfig}>
    <div class="settings-fields">
      <div class="settings-row">
        <label class="settings-label" for="t-client"><span class="settings-label-text">Client ID</span></label>
        <div class="settings-control">
          <input id="t-client" class="device-select" autocomplete="off" bind:value={cfg.client_id} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-secret">
          <span class="settings-label-text">Client secret</span>
          <span class="settings-hint">{cfg.has_client_secret ? 'Saved. Leave blank to keep it.' : 'Stored encrypted.'}</span>
        </label>
        <div class="settings-control">
          <input id="t-secret" class="device-select" type="password" autocomplete="off" bind:value={secret} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-vin"><span class="settings-label-text">VIN</span></label>
        <div class="settings-control">
          <input id="t-vin" class="device-select" autocomplete="off" bind:value={cfg.vin} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-domain">
          <span class="settings-label-text">Domain</span>
          <span class="settings-hint">Where this dashboard is reachable, e.g. j.cdani.net</span>
        </label>
        <div class="settings-control">
          <input id="t-domain" class="device-select" autocomplete="off" bind:value={cfg.domain} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-region"><span class="settings-label-text">Region</span></label>
        <div class="settings-control">
          <select id="t-region" class="device-select" bind:value={cfg.region}>
            <option value="na">North America / Asia-Pacific</option>
            <option value="eu">Europe / Middle East / Africa</option>
          </select>
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-transport">
          <span class="settings-label-text">Send stops via</span>
          <span class="settings-hint">Signed commands use the paired key. REST is the fallback to test.</span>
        </label>
        <div class="settings-control">
          <select id="t-transport" class="device-select" bind:value={cfg.nav_transport}>
            <option value="signed">Signed command</option>
            <option value="rest">REST</option>
          </select>
        </div>
      </div>
      <h3 class="tesla-sub">Places sends (one route)</h3>
      <div class="settings-row">
        <label class="settings-label" for="t-gkey">
          <span class="settings-label-text">Google Maps API key</span>
          <span class="settings-hint">{cfg.has_google_api_key
              ? 'Saved. Leave blank to keep it.'
              : 'Geocoding API. Turns each stop into a Google place for the route.'}</span>
        </label>
        <div class="settings-control">
          <input id="t-gkey" class="device-select" type="password" autocomplete="off" bind:value={googleKey} />
        </div>
      </div>
      <h3 class="tesla-sub">GPS sends (one command per stop)</h3>
      <p class="hint" style="margin:0 0 8px">Defaults for GPS batches; the app can override both. Single-stop sends always use GPS.</p>
      <div class="settings-row">
        <label class="settings-label" for="t-order">
          <span class="settings-label-text">Stop order field</span>
          <span class="settings-hint">How the car reads "order": replace/append, or stop position.</span>
        </label>
        <div class="settings-control">
          <select id="t-order" class="device-select" bind:value={cfg.nav_order_mode}>
            <option value="enum">Replace, then append (0, 2, 2…)</option>
            <option value="position">Position number (1, 2, 3…)</option>
            <option value="one_three">1 to replace, then 3 (1, 3, 3…)</option>
          </select>
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-delay">
          <span class="settings-label-text">Seconds between stops</span>
          <span class="settings-hint">Pause between GPS commands; the car drops stops sent too fast. The app can override it.</span>
        </label>
        <div class="settings-control">
          <input id="t-delay" type="number" min="1" max="60" step="1" bind:value={cfg.nav_stop_delay_seconds} />
        </div>
      </div>
      <h3 class="tesla-sub">Charging</h3>
      <div class="settings-row">
        <label class="settings-label" for="t-eff">
          <span class="settings-label-text">Charging efficiency</span>
          <span class="settings-hint">Battery kWh ÷ wall kWh, used to price home charging.</span>
        </label>
        <div class="settings-control">
          <input id="t-eff" type="number" min="0.5" max="1" step="0.01" bind:value={cfg.charge_efficiency} />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="t-poll">
          <span class="settings-label-text">Track charging</span>
          <span class="settings-hint">Checks the car without waking it.</span>
        </label>
        <div class="settings-control">
          <label style="display:flex; gap:6px; align-items:center">
            <input
              id="t-poll"
              type="checkbox"
              checked={!cfg.polling_disabled}
              onchange={(e) => (cfg.polling_disabled = !e.currentTarget.checked)}
            />
            On
          </label>
        </div>
      </div>
    </div>
    <div style="margin-top:16px; display:flex; gap:8px; flex-wrap:wrap">
      <button class="btn btn-primary" type="submit" disabled={busy}>Save</button>
    </div>
  </form>

  <div style="margin-top:16px; display:flex; gap:8px; flex-wrap:wrap">
    <button class="btn btn-ghost" type="button" disabled={busy || !status?.configured} onclick={() => genKey()}>
      {status?.has_key ? 'Show key' : 'Generate key'}
    </button>
    <button class="btn btn-ghost" type="button" disabled={busy || !status?.has_key} onclick={register}>
      Register domain
    </button>
    <button class="btn btn-ghost" type="button" disabled={busy || !status?.configured} onclick={signIn}>
      {status?.connected ? 'Sign in again' : 'Sign in to Tesla'}
    </button>
    <button class="btn btn-ghost" type="button" disabled={busy || !status?.connected} onclick={wake}>Wake car</button>
    {#if status?.connected}
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={forget}>Disconnect</button>
    {/if}
    {#if status?.has_key}
      <button class="btn btn-ghost" type="button" disabled={busy} onclick={() => genKey(true)}>New key</button>
    {/if}
  </div>
</section>

{#if status?.last_navigation}
  {@const job = status.last_navigation}
  <section class="card device-card">
    <div class="card-header">
      <h2>Last send</h2>
      <span class="hint">{job.status} · {fmtTime(job.started)}</span>
    </div>
    <p class="hint" style="margin:6px 0 8px">
      {job.options?.method === 'places' ? 'Places' : job.options?.method === 'single' ? 'Single stop' : 'GPS'} ·
      {job.options?.transport}
      {#if job.options?.position}· {job.options.position}{:else if job.options?.order_mode}· order {job.options.order_mode}{/if}
      {#if job.options?.stop_delay_seconds}· {job.options.stop_delay_seconds}s apart{/if}
      {#if job.error}<br />{job.error}{/if}
    </p>
    {#each job.results ?? [] as r (r.index)}
      <p class="hint" style="margin:2px 0">
        Stop {r.index + 1}: {job.options?.method === 'places' ? (r.address ?? r.place_id) : `order ${r.order}`} ·
        {r.ok ? 'accepted' : (r.error ?? 'failed')}
      </p>
    {/each}
  </section>
{/if}

<section class="card device-card">
  <div class="card-header"><h2>TeslaHelper app token</h2></div>
  <p class="hint" style="margin:6px 0 12px">
    Paste into TeslaHelper → Settings. It acts as your dashboard login; changing your password revokes it.
  </p>
  <div style="display:flex; gap:8px; align-items:center; flex-wrap:wrap">
    <label class="hint" for="t-days">Valid for</label>
    <input id="t-days" type="number" min="1" max="730" style="width:80px" bind:value={tokenDays} />
    <span class="hint">days</span>
    <button class="btn btn-primary" type="button" disabled={busy} onclick={makeToken}>Create token</button>
  </div>
  {#if appToken}
    <div style="margin-top:12px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <input class="device-select" readonly value={appToken} style="flex:1; min-width:240px; font-family:monospace" onfocus={(e) => e.currentTarget.select()} />
      <button class="btn btn-ghost" type="button" onclick={copyToken}>Copy</button>
      <button class="btn btn-ghost" type="button" onclick={() => (appToken = '')}>Hide</button>
    </div>
  {/if}
</section>

<section class="card device-card">
  <div class="card-header">
    <h2>Charging</h2>
    <span class="hint">{money(totals.cost)} · {totals.kwh} kWh from the wall</span>
  </div>
  {#if status?.charge}
    <p class="hint" style="margin:6px 0 12px">
      {status.charge.battery_level ?? '—'}% (limit {status.charge.charge_limit_soc ?? '—'}%) ·
      {status.charge.charging_state ?? 'unknown'}
      {#if status.charge.charger_power}· {status.charge.charger_power} kW{/if}
      · checked {fmtTime(status.charge.ts)}
      {#if status.next_poll}· next {fmtTime(status.next_poll)}{/if}
    </p>
  {/if}
  <button class="btn btn-ghost" type="button" disabled={busy || !status?.connected} onclick={poll}>Check now</button>
  {#if sessions.length === 0}
    <p class="hint" style="margin-top:12px">No charging sessions yet.</p>
  {:else}
    <div style="overflow-x:auto; margin-top:12px">
      <table class="tesla-table">
        <thead>
          <tr><th>Started</th><th>Battery</th><th>Where</th><th>Added</th><th>Wall</th><th>Cost</th></tr>
        </thead>
        <tbody>
          {#each sessions as s (s.key)}
            <tr>
              <td>{fmtTime(s.start_ts)}{s.active ? ' · charging' : ''}</td>
              <td>{s.start_level}% → {s.end_level}%</td>
              <td>{s.fast_charger ? 'Supercharger' : s.at_home ? 'Home' : 'Away'}</td>
              <td>{s.energy_added_kwh.toFixed(1)} kWh</td>
              <td>{s.energy_wall_kwh.toFixed(1)} kWh</td>
              <td>
                {s.at_home ? money(s.cost, s.currency) : '—'}
                {#if s.unpriced_kwh}<span class="hint"> ({s.unpriced_kwh.toFixed(1)} kWh unpriced)</span>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>

<style>
  .tesla-steps {
    margin: 0 0 8px;
    padding-left: 20px;
    display: grid;
    gap: 4px;
    font-size: 14px;
  }
  .tesla-steps li.done::marker {
    content: '✓  ';
    color: var(--ok, #2a7);
  }
  .tesla-steps a {
    word-break: break-all;
  }
  .tesla-sub {
    margin: 18px 0 6px;
    font-size: 13px;
    color: var(--text-dim);
    font-weight: 500;
  }
  .tesla-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  .tesla-table th,
  .tesla-table td {
    text-align: left;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border, rgba(127, 127, 127, 0.25));
    white-space: nowrap;
  }
  .tesla-table th {
    font-weight: 500;
    color: var(--text-dim);
  }
</style>
