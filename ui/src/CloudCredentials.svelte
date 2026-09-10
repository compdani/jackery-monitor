<script lang="ts">
  import { onMount } from 'svelte';
  import { feature } from './api';
  type CloudAuth = { has_credentials: boolean; email?: string; cloud_state: string; error?: string; backend: string; editable: boolean; source: string };
  let status = $state<CloudAuth | null>(null);
  let email = $state('');
  let password = $state('');
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);
  let disposed = false;
  async function refresh() {
    try { const result = await feature<CloudAuth>('/auth/status'); if (!disposed) status = result; }
    catch (err) { if (!disposed) error = err instanceof Error ? err.message : 'Unable to read cloud status'; }
  }
  onMount(() => { void refresh(); return () => { disposed = true; password = ''; }; });
  async function save(event: SubmitEvent) {
    event.preventDefault(); busy = true; error = ''; notice = '';
    try { await feature('/auth/credentials', { email, password, region: 'US' }); password = ''; notice = 'Account verified and saved. Open Live to see your devices.'; await refresh(); }
    catch (err) { error = err instanceof Error ? err.message : 'Unable to save credentials'; }
    finally { busy = false; }
  }
  async function forget() {
    busy = true; error = ''; notice = '';
    try { await feature('/auth/forget', {}); email = ''; password = ''; notice = 'Cloud credentials removed.'; await refresh(); }
    catch (err) { error = err instanceof Error ? err.message : 'Unable to remove credentials'; }
    finally { busy = false; }
  }
</script>
<section class="card"><h3>Jackery cloud account</h3>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if notice}<p class="success" role="status">{notice}</p>{/if}
  {#if status?.backend === 'mock'}<p class="muted">Mock mode uses simulated devices. No cloud credentials are needed.</p>
  {:else if status}
    <p class="muted">{status.has_credentials ? `Saved account: ${status.email ?? ''}` : 'Connect your US-region Jackery account to start monitoring.'}</p>
    {#if status.error}<p class="error">{status.error}</p>{/if}
    {#if status.editable}<form onsubmit={save}>
      <label>Jackery email<input type="email" bind:value={email} autocomplete="username" required /></label>
      <label>Jackery password<input type="password" bind:value={password} autocomplete="current-password" required /></label>
      <p class="muted">Credentials are encrypted on disk. Signing in may end your session in the Jackery phone app. Use “Pause for 10 min” on Live when switching to the phone app.</p>
      <button type="submit" class="primary" disabled={busy}>{busy ? 'Please wait…' : 'Verify and save account'}</button>
    </form>{#if status.has_credentials}<button class="text-button" disabled={busy} onclick={forget}>Forget saved account</button>{/if}
    {:else}<p class="muted">This account is configured through environment variables.</p>{/if}
  {:else}<p class="muted">Loading account status…</p>{/if}
</section>
