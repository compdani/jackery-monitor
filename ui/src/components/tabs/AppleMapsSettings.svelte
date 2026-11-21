<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost, ApiError } from '$lib/api';

  type Config = {
    configured: boolean;
    team_id?: string;
    key_id?: string;
    has_private_key?: boolean;
    daily_call_limit?: number;
    used_today?: number;
  };
  let saved = $state<Config | null>(null);
  let team = $state('');
  let keyId = $state('');
  let privateKey = $state('');
  let filename = $state('');
  let limit = $state<number | undefined>(5000);
  let busy = $state(false);
  let loaded = $state(false);
  let message = $state('');
  let error = $state('');
  const dirty = $derived(!!privateKey || team !== (saved?.team_id ?? '') || keyId !== (saved?.key_id ?? '') || limit !== (saved?.daily_call_limit ?? 5000));

  const describe = (e: unknown) => e instanceof ApiError && e.status === 404
    ? 'Apple Maps setup is unavailable on this server version. Update the server to enable route planning setup.'
    : e instanceof Error ? e.message : 'Request failed.';

  async function load() {
    busy = true;
    error = '';
    try {
      saved = await api<Config>('/route/config');
      team = saved.team_id ?? '';
      keyId = saved.key_id ?? '';
      limit = saved.daily_call_limit ?? 5000;
      loaded = true;
    } catch (e) {
      error = describe(e);
    } finally {
      busy = false;
    }
  }

  async function chooseFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    busy = true;
    error = '';
    try {
      if (file.size > 16384) throw new Error('Choose the small .p8 private-key file downloaded from Apple.');
      const contents = await file.text();
      if (!contents.includes('-----BEGIN PRIVATE KEY-----')) throw new Error('This file does not contain an Apple .p8 private key.');
      privateKey = contents;
      filename = file.name;
      message = 'Key selected. Save to store it on the server.';
    } catch (e) {
      error = describe(e);
    } finally {
      input.value = '';
      busy = false;
    }
  }

  async function save(event: SubmitEvent) {
    event.preventDefault();
    error = '';
    message = '';
    if (!/^[A-Z0-9]{10}$/.test(team.trim()) || !/^[A-Z0-9]{10}$/.test(keyId.trim())) {
      error = 'Team ID and Key ID must each contain 10 uppercase letters or digits.';
      return;
    }
    if (!privateKey.trim() && !saved?.has_private_key) {
      error = 'Choose or paste the .p8 private key first.';
      return;
    }
    if (!Number.isInteger(limit) || limit! < 1 || limit! > 25000) {
      error = 'Daily call limit must be a whole number from 1 to 25,000.';
      return;
    }
    busy = true;
    try {
      await apiPost('/route/config', {
        team_id: team.trim(), key_id: keyId.trim(), daily_call_limit: limit,
        ...(privateKey.trim() ? { private_key_pem: privateKey.trim() } : {}),
      });
      privateKey = '';
      filename = '';
      await load();
      message = 'Saved. Test the connection to verify Apple accepts the key.';
    } catch (e) {
      error = describe(e);
    } finally {
      busy = false;
    }
  }

  async function test() {
    busy = true;
    error = '';
    message = '';
    try {
      await apiPost('/route/test', {});
      await load();
      message = 'Apple Maps connection verified. Address search is working; no car command was sent.';
    } catch (e) {
      error = describe(e);
    } finally {
      busy = false;
    }
  }

  onMount(load);
</script>

<section class="card device-card">
  <div class="card-header">
    <h2>Apple Maps route planning</h2>
    <span class="hint">{saved?.configured ? 'key saved' : loaded ? 'not set up' : 'status unavailable'}</span>
  </div>
  <p class="hint">Used by TeslaHelper for address search, road routes, and optimization.</p>
  <ol class="hint">
    <li>Open <a href="https://developer.apple.com/account/resources/authkeys/list" target="_blank" rel="noreferrer">Apple Developer keys</a>, create a key with Maps capability, and download its .p8 file.</li>
    <li>Enter your Team ID from your developer membership and the Key ID shown for that key.</li>
    <li>Choose the .p8 file, save, then test the connection.</li>
  </ol>
  <form onsubmit={save}>
    <fieldset disabled={busy || !loaded} style="border:0; padding:0; margin:0; min-width:0">
      <div class="settings-fields">
        <div class="settings-row">
          <label class="settings-label" for="apple-team"><span class="settings-label-text">Team ID</span></label>
          <div class="settings-control"><input id="apple-team" class="device-select" bind:value={team} maxlength="10" autocomplete="off" spellcheck="false" required /></div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="apple-key-id"><span class="settings-label-text">Key ID</span></label>
          <div class="settings-control"><input id="apple-key-id" class="device-select" bind:value={keyId} maxlength="10" autocomplete="off" spellcheck="false" required /></div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="apple-key-file">
            <span class="settings-label-text">Private key (.p8)</span>
            <span class="settings-hint">{saved?.has_private_key ? 'Key saved. Leave blank to keep it, or select a replacement.' : 'Stored encrypted on the server. Never returned to the dashboard.'}</span>
          </label>
          <div class="settings-control"><input id="apple-key-file" type="file" accept=".p8,.pem" onchange={chooseFile} />{#if filename}<p class="hint">{filename} selected</p>{/if}</div>
        </div>
        <details>
          <summary class="hint">Paste private key instead</summary>
          <label class="hint" for="apple-key-paste">Full .p8 contents, including BEGIN/END PRIVATE KEY</label>
          <textarea id="apple-key-paste" rows="5" bind:value={privateKey} autocomplete="off" spellcheck="false" style="width:100%; box-sizing:border-box; font-family:monospace"></textarea>
        </details>
        <div class="settings-row">
          <label class="settings-label" for="apple-limit"><span class="settings-label-text">Daily call limit</span><span class="settings-hint">Shared by route planning and address search. Default 5,000.</span></label>
          <div class="settings-control"><input id="apple-limit" type="number" min="1" max="25000" step="1" bind:value={limit} required /></div>
        </div>
      </div>
      <button class="btn btn-primary" type="submit" disabled={!dirty}>Save Apple Maps setup</button>
    </fieldset>
  </form>
  <div style="display:flex; flex-wrap:wrap; gap:8px; margin-top:12px">
    <button class="btn btn-ghost" type="button" disabled={busy || !saved?.configured || dirty} onclick={test}>{busy ? 'Working…' : 'Test saved connection'}</button>
    {#if !loaded}<button class="btn btn-ghost" type="button" disabled={busy} onclick={load}>Retry setup status</button>{/if}
  </div>
  {#if dirty && saved?.configured}<p class="hint">Save your changes before testing.</p>{/if}
  {#if saved?.configured}<p class="hint">{saved.used_today ?? 0} of {saved.daily_call_limit ?? 5000} calls used today. The connection test makes an Apple address-search request using a sample location.</p>{/if}
  {#if message}<p class="hint" role="status">{message}</p>{/if}
  {#if error}<p class="hint" role="alert" style="color:var(--danger, #c33)">{error}</p>{/if}
</section>
