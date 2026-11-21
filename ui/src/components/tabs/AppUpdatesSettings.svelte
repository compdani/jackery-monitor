<script lang="ts">
  // TeslaHelper over-the-air updates: upload the zip from `npm run ota`,
  // publish it, roll back, delete. The app only checks; the driver taps to
  // download and restart.
  import { onMount } from 'svelte';
  import { api, apiPost, ApiError } from '$lib/api';

  type Update = {
    id: string;
    runtime_version: string;
    status: 'draft' | 'published' | 'rollback';
    notes: string;
    update_id: string;
    created: string;
    published_at?: string;
    platforms: string[];
    bytes: number;
  };

  let updates = $state<Update[]>([]);
  let loaded = $state(false);
  let busy = $state(false);
  let busyId = $state('');
  let busyAction = $state('');
  let message = $state('');
  let error = $state('');
  let file = $state<File | null>(null);
  let version = $state('');
  let versionHint = $state('');
  let notes = $state('');
  let publishNow = $state(true);
  let inputKey = $state(0);

  const label: Record<Update['status'], string> = {
    draft: 'Draft',
    published: 'Live',
    rollback: 'Rolled back to installed app',
  };

  const describe = (e: unknown) =>
    e instanceof ApiError && e.status === 404
      ? 'App updates are unavailable on this server version. Update the server.'
      : e instanceof Error
        ? e.message
        : 'Request failed.';

  const when = (iso?: string) => (iso ? new Date(iso).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '');
  const size = (n: number) => (n > 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`);

  async function load() {
    error = '';
    try {
      updates = await api<Update[]>('/ota/updates');
      loaded = true;
    } catch (e) {
      error = describe(e);
    }
  }

  function choose(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    file = input.files?.[0] ?? null;
    versionHint = '';
    if (!file) return;
    // TeslaHelper-0.3.0-2026-….zip → 0.3.0
    const m = file.name.match(/(\d+\.\d+\.\d+)/);
    if (m) {
      version = m[1];
      versionHint = 'Read from the file name. It must match the app version installed on the phone.';
    } else {
      versionHint = 'Enter the app version this update is for (Settings → App update on the phone).';
    }
  }

  async function upload(event: SubmitEvent) {
    event.preventDefault();
    error = '';
    message = '';
    if (!file) {
      error = 'Choose the zip built by npm run ota first.';
      return;
    }
    if (!/^[0-9A-Za-z._-]+$/.test(version.trim())) {
      error = 'Enter the app version, e.g. 0.3.0.';
      return;
    }
    busy = true;
    try {
      const form = new FormData();
      form.append('bundle_zip', file);
      form.append('runtime_version', version.trim());
      form.append('notes', notes.trim());
      form.append('publish', publishNow ? 'true' : 'false');
      await api('/ota/updates', { method: 'POST', body: form });
      file = null;
      notes = '';
      versionHint = '';
      inputKey += 1;
      message = publishNow
        ? `Published for version ${version.trim()}. On the phone: Settings → App update → Check for updates, then Download and restart.`
        : 'Uploaded as a draft. Publish it when ready.';
      await load();
    } catch (e) {
      error = describe(e);
    } finally {
      busy = false;
    }
  }

  async function act(u: Update, action: 'publish' | 'rollback' | 'delete') {
    const prompt =
      action === 'publish'
        ? `Publish this update for version ${u.runtime_version}? Phones on that version will offer it.`
        : action === 'rollback'
          ? `Send phones on version ${u.runtime_version} back to the version built into the app?`
          : u.status === 'rollback'
            ? 'Delete this rollback? Phones that have not applied it yet will no longer be offered it.'
            : 'Delete this update? Phones already running it keep it until you publish another or roll back.';
    if (!confirm(prompt)) return;
    busyId = u.id;
    busyAction = action;
    error = '';
    message = '';
    try {
      if (action === 'delete') await api(`/ota/updates/${u.id}`, { method: 'DELETE' });
      else await apiPost(`/ota/updates/${u.id}/${action}`);
      message = action === 'publish' ? 'Published.' : action === 'rollback' ? 'Rollback published.' : 'Deleted.';
      await load();
    } catch (e) {
      error = describe(e);
    } finally {
      busyId = '';
      busyAction = '';
    }
  }

  onMount(load);
</script>

<section class="card device-card">
  <div class="card-header">
    <h2>TeslaHelper app updates</h2>
    <span class="hint">{loaded ? `${updates.length} uploaded` : 'status unavailable'}</span>
  </div>
  <p class="hint">JavaScript-only changes, without rebuilding the app. The phone only checks; you tap Download and restart on it.</p>
  <ol class="hint">
    <li>In the app folder, run <code>npm run ota</code>. It builds <code>ota/TeslaHelper-&lt;version&gt;-….zip</code>.</li>
    <li>Choose that zip here and upload it (published right away unless you untick it).</li>
    <li>On the phone: Settings → App update → Check for updates, then Download and restart.</li>
  </ol>
  <p class="hint">Native changes (new libraries, permissions, app.json native settings) still need a new build with a new version.</p>

  <form onsubmit={upload}>
    <fieldset disabled={busy || !loaded} style="border:0; padding:0; margin:0; min-width:0">
      <div class="settings-fields">
        <div class="settings-row">
          <label class="settings-label" for="ota-zip">
            <span class="settings-label-text">Update zip</span>
            <span class="settings-hint">From npm run ota. Up to 30 MB.</span>
          </label>
          <div class="settings-control">
            {#key inputKey}<input id="ota-zip" type="file" accept=".zip,application/zip" onchange={choose} />{/key}
          </div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="ota-version">
            <span class="settings-label-text">App version</span>
            <span class="settings-hint">{versionHint || 'Must match the version installed on the phone.'}</span>
          </label>
          <div class="settings-control"><input id="ota-version" class="device-select" bind:value={version} placeholder="0.3.0" autocomplete="off" spellcheck="false" /></div>
        </div>
        <div class="settings-row">
          <label class="settings-label" for="ota-notes"><span class="settings-label-text">Notes</span><span class="settings-hint">For you: what changed.</span></label>
          <div class="settings-control"><input id="ota-notes" class="device-select" bind:value={notes} maxlength="2000" autocomplete="off" /></div>
        </div>
        <label class="hint" style="display:flex; gap:8px; align-items:center">
          <input type="checkbox" bind:checked={publishNow} /> Publish right away
        </label>
      </div>
      <button class="btn btn-primary" type="submit" disabled={!file}>{busy ? 'Uploading…' : publishNow ? 'Upload and publish' : 'Upload as draft'}</button>
    </fieldset>
  </form>

  {#if !loaded}<button class="btn btn-ghost" type="button" onclick={load} style="margin-top:8px">Retry</button>{/if}
  {#if message}<p class="hint" role="status">{message}</p>{/if}
  {#if error}<p class="hint" role="alert" style="color:var(--danger, #c33)">{error}</p>{/if}

  {#if updates.length}
    <div style="margin-top:16px; display:grid; gap:10px">
      {#each updates as u (u.id)}
        <div style="border:1px solid var(--border, #ccc); border-radius:10px; padding:10px 12px">
          <div style="display:flex; flex-wrap:wrap; gap:8px; align-items:baseline; justify-content:space-between">
            <strong>Version {u.runtime_version}</strong>
            <span class="hint" style="font-weight:600; color:{u.status === 'published' ? 'var(--pow-green, #1b7f3b)' : u.status === 'rollback' ? 'var(--pow-amber, #a35700)' : 'inherit'}">{label[u.status]}</span>
          </div>
          {#if u.notes}<p style="margin:4px 0">{u.notes}</p>{/if}
          <p class="hint" style="margin:2px 0">
            Uploaded {when(u.created)}{u.published_at && u.status !== 'draft' ? ` · ${u.status === 'rollback' ? 'rollback since' : 'published'} ${when(u.published_at)}` : ''} · {size(u.bytes)} · {u.platforms.join(', ')}
          </p>
          <div style="display:flex; flex-wrap:wrap; gap:8px; margin-top:6px">
            {#if u.status !== 'published'}
              <button class="btn btn-ghost" type="button" disabled={!!busyId} onclick={() => act(u, 'publish')}>{busyId === u.id && busyAction === 'publish' ? 'Working…' : 'Publish'}</button>
            {/if}
            {#if u.status === 'published'}
              <button class="btn btn-ghost" type="button" disabled={!!busyId} onclick={() => act(u, 'rollback')}>{busyId === u.id && busyAction === 'rollback' ? 'Working…' : 'Roll back to installed app'}</button>
            {/if}
            <button class="btn btn-ghost" type="button" disabled={!!busyId} onclick={() => act(u, 'delete')}>{busyId === u.id && busyAction === 'delete' ? 'Working…' : 'Delete'}</button>
          </div>
        </div>
      {/each}
    </div>
  {:else if loaded}
    <p class="hint">No updates uploaded yet.</p>
  {/if}
</section>
