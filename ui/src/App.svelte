<script lang="ts">
  import { onMount } from 'svelte';
  import Live from './Live.svelte';
  import Forecast from './Forecast.svelte';
  import CostPlan from './CostPlan.svelte';
 import Energy from './Energy.svelte';
 import Device from './Device.svelte';
  import CloudCredentials from './CloudCredentials.svelte';
  import { pb, feature, FeatureError, type Setting } from './api';

  const tabs = ['Live', 'Energy', 'Forecast', 'Device', 'Automation', 'Logs', 'Settings'];
  let tab = $state('Live');
  let ready = $state(false);
  let signedIn = $state(false);
  let setup = $state(false);
  let username = $state('');
  let password = $state('');
  let confirmation = $state('');
  let oldPassword = $state('');
  let newPassword = $state('');
  let newConfirmation = $state('');
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);
  let settings = $state<Setting[]>([]);

  function navigate(path: string) { window.history.replaceState({}, '', path); }
  function selectTab(next: string) { tab = next; error = ''; notice = ''; navigate(`/${next.toLowerCase()}`); }
  async function loadSettings() {
    settings = (await feature<{ settings: Setting[] }>('/settings')).settings;
  }
  async function boot() {
    error = '';
    try {
      if (pb.authStore.isValid) {
        try { await pb.collection('users').authRefresh(); }
        catch (err) {
          // Preserve the token on network failure, but reject expired/revoked tokens.
          if (typeof err === 'object' && err && 'status' in err && [401, 403].includes(Number(err.status))) pb.authStore.clear();
          else throw err;
        }
      }
      await loadSettings();
      signedIn = pb.authStore.isValid;
      const selected = window.location.pathname.slice(1);
      tab = tabs.find(value => value.toLowerCase() === selected) || 'Live';
      navigate(`/${tab.toLowerCase()}`);
    } catch (err) {
      if (err instanceof FeatureError && err.status === 401) {
        setup = err.detail === 'setup_required';
        username = err.username || username;
        navigate(setup ? '/setup' : '/login');
      } else { error = err instanceof Error ? err.message : 'Unable to reach the server'; }
    } finally { ready = true; }
  }
  onMount(() => {
    const unsubscribe = pb.authStore.onChange(() => {
      signedIn = pb.authStore.isValid && pb.authStore.record?.collectionName === 'users';
      if (!signedIn && ready) { password = ''; oldPassword = ''; newPassword = ''; newConfirmation = ''; settings = []; navigate('/login'); }
    });
    void boot();
    return unsubscribe;
  });
  async function authenticate(event: SubmitEvent) {
    event.preventDefault(); busy = true; error = '';
    try {
      if (setup) {
        await pb.collection('users').create({ username, password, passwordConfirm: confirmation });
        setup = false; // A failed login after successful setup must not repeat account creation.
      }
      await pb.collection('users').authWithPassword(username, password);
      password = ''; confirmation = '';
      await loadSettings(); selectTab('Live');
    } catch (err) { error = err instanceof Error ? err.message : 'Sign in failed'; }
    finally { busy = false; }
  }
  async function saveSettings(event: SubmitEvent) {
    event.preventDefault(); busy = true; error = ''; notice = '';
    try {
      await feature('/settings', Object.fromEntries(settings.map(s => [s.key, s.value])));
      await loadSettings(); notice = 'Settings saved.';
    } catch (err) { error = err instanceof Error ? err.message : 'Save failed'; }
    finally { busy = false; }
  }
  async function changePassword(event: SubmitEvent) {
    event.preventDefault(); busy = true; error = ''; notice = '';
    try {
      await pb.collection('users').update(pb.authStore.record!.id, { oldPassword, password: newPassword, passwordConfirm: newConfirmation });
      oldPassword = ''; newPassword = ''; newConfirmation = '';
      pb.authStore.clear(); notice = 'Password changed. Sign in with your new password.';
    } catch (err) { error = err instanceof Error ? err.message : 'Password change failed'; }
    finally { busy = false; }
  }
</script>

<svelte:head><title>{signedIn ? tab : setup ? 'Setup' : 'Sign in'} · Jackery Monitor</title></svelte:head>

<header class="topbar">
  <div class="brand"><span class="brand-icon" aria-hidden="true">ϟ</span><div><h1>Jackery Monitor</h1><span class="muted">Energy, at a glance</span></div></div>
  {#if signedIn}<div class="account"><span class="muted">{pb.authStore.record?.username}</span><button onclick={() => pb.authStore.clear()}>Sign out</button></div>{/if}
</header>
{#if !ready}
  <main class="auth card" aria-busy="true">Connecting to your monitor…</main>
{:else if !signedIn}
  <main class="auth card">
    <span class="eyebrow">YOUR ENERGY DASHBOARD</span>
    <h2>{setup ? 'Set up your monitor' : 'Welcome back'}</h2>
    <p class="muted">{setup ? 'Create your owner account to get started.' : 'Sign in to your Jackery Monitor.'}</p>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if notice}<p role="status">{notice}</p>{/if}
    <form onsubmit={authenticate}>
      <label>Username<input bind:value={username} autocomplete="username" pattern="[a-zA-Z0-9_.\-]+" maxlength="64" required /></label>
      <label>Password<input type="password" bind:value={password} autocomplete={setup ? 'new-password' : 'current-password'} minlength={setup ? 12 : undefined} required /></label>
      {#if setup}<label>Confirm password<input type="password" bind:value={confirmation} autocomplete="new-password" minlength="12" required /></label><p class="muted">Use at least 12 characters. This also creates your PocketBase administrator account.</p>{/if}
      <button class="primary" type="submit" disabled={busy}>{busy ? 'Please wait…' : setup ? 'Create account' : 'Sign in'}</button>
    </form>
    <button class="text-button" onclick={boot} disabled={busy}>Retry connection</button>
  </main>
{:else}
  <nav aria-label="Dashboard tabs">{#each tabs as item}<button class:active={tab === item} aria-current={tab === item ? 'page' : undefined} onclick={() => selectTab(item)}>{item}</button>{/each}</nav>
  <main class="dashboard">
    <div class="page-heading"><div><span class="eyebrow">JACKERY MONITOR</span><h2>{tab}</h2></div><span class="badge">PocketBase preview</span></div>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if notice}<p class="success" role="status">{notice}</p>{/if}
    {#if tab === 'Live'}
      <Live />
    {:else if tab === 'Energy'}
      <Energy />
    {:else if tab === 'Forecast'}
      <Forecast />
    {:else if tab === 'Device'}
      <Device />
    {:else if tab === 'Settings'}
      <CloudCredentials />
      <CostPlan />
      <section class="card"><h3>Runtime settings</h3><p class="muted">Saved in PocketBase. Runtime jobs will use these settings as they are ported.</p>
        <form onsubmit={saveSettings}>
          {#each settings as setting}<label class="setting"><span>{setting.label}<small>{setting.hint}</small></span><input type="number" min={setting.min} max={setting.max} step="1" bind:value={setting.value} required /></label>{/each}
          <button type="submit" class="primary" disabled={busy}>Save settings</button>
        </form>
      </section>
      <section class="card"><h3>Account</h3><form onsubmit={changePassword}>
        <label>Current password<input type="password" bind:value={oldPassword} autocomplete="current-password" required /></label>
        <label>New password<input type="password" bind:value={newPassword} autocomplete="new-password" minlength="12" required /></label>
        <label>Confirm new password<input type="password" bind:value={newConfirmation} autocomplete="new-password" minlength="12" required /></label>
        <button type="submit" disabled={busy}>Change password</button>
      </form><p class="muted">The administrator password is managed separately after setup.</p><a href="/_/" target="_blank" rel="noreferrer">Open PocketBase administration ↗</a></section>
    {:else}
      <section class="card empty"><span class="empty-icon" aria-hidden="true">ϟ</span><h3>{tab} is being migrated</h3><p class="muted">This preview has Live, Energy, Forecast, Device, and Settings screens. The {tab.toLowerCase()} features will appear as their Go APIs are ported.</p><p class="muted">Use the existing dashboard for features still being migrated.</p></section>
    {/if}
  </main>
{/if}
