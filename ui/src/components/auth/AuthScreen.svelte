<script lang="ts">
  import { login, setup } from '$lib/auth';
  import { apiPost } from '$lib/api';
  import { fmtBytes } from '$lib/format';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Card from '$lib/components/ui/card/index.js';

  interface Props {
    mode: 'login' | 'setup';
    onAuthenticated: (username: string) => void;
  }
  let { mode, onAuthenticated }: Props = $props();

  let username = $state('');
  let password = $state('');
  let confirmation = $state('');
  let error = $state('');
  let busy = $state(false);

  // Restore wizard (setup only)
  let showRestore = $state(false);
  let restoreStep = $state<'creds' | 'pick' | 'confirm' | 'done'>('creds');
  let rsHost = $state('');
  let rsShare = $state('');
  let rsSubdir = $state('jackery-monitor');
  let rsUsername = $state('');
  let rsPassword = $state('');
  let rsDomain = $state('WORKGROUP');
  let rsError = $state('');
  let rsInfo = $state('');
  let snapshots = $state<
    { dir: string; iso?: string; valid: boolean; error?: string; total_bytes?: number; files?: string[] }[]
  >([]);
  let picked: (typeof snapshots)[0] | null = $state(null);

  function creds() {
    return {
      host: rsHost.trim(),
      share: rsShare.trim(),
      subdir: rsSubdir.trim() || 'jackery-monitor',
      username: rsUsername.trim(),
      password: rsPassword,
      domain: rsDomain.trim() || 'WORKGROUP',
    };
  }

  async function authenticate(event: SubmitEvent) {
    event.preventDefault();
    error = '';
    if (mode === 'setup' && password !== confirmation) {
      error = "Passwords don't match.";
      return;
    }
    busy = true;
    try {
      const result =
        mode === 'setup'
          ? await setup(username, password)
          : await login(username, password);
      onAuthenticated(result.username);
    } catch (err) {
      error = err instanceof Error ? err.message : 'Sign in failed';
    } finally {
      busy = false;
    }
  }

  async function testRestore() {
    rsError = '';
    rsInfo = '';
    const c = creds();
    if (!c.host || !c.share || !c.username || !c.password) {
      rsError = 'Fill in host, share, username, and password.';
      return;
    }
    busy = true;
    try {
      const j = await apiPost<{ ok: boolean; latency_ms?: number; error?: string }>(
        '/backup/setup_restore/test',
        c,
      );
      if (j.ok) {
        const ms = j.latency_ms != null ? ` (${j.latency_ms} ms)` : '';
        rsInfo = `Mount + write probe OK${ms}.`;
      } else {
        rsError = 'Failed: ' + (j.error || 'unknown');
      }
    } catch (err) {
      rsError = 'Failed: ' + (err instanceof Error ? err.message : String(err));
    } finally {
      busy = false;
    }
  }

  async function listSnapshots() {
    rsError = '';
    rsInfo = '';
    const c = creds();
    if (!c.host || !c.share || !c.username || !c.password) {
      rsError = 'Fill in host, share, username, and password.';
      return;
    }
    busy = true;
    try {
      const j = await apiPost<{ snapshots?: typeof snapshots }>('/backup/setup_restore/snapshots', c);
      const snaps = j.snapshots || [];
      if (!snaps.length) {
        rsError = 'No snapshots found at that destination.';
        return;
      }
      snapshots = snaps;
      restoreStep = 'pick';
    } catch (err) {
      rsError = 'Failed: ' + (err instanceof Error ? err.message : String(err));
    } finally {
      busy = false;
    }
  }

  async function runRestore() {
    if (!picked) return;
    rsError = '';
    busy = true;
    try {
      const body = { ...creds(), snapshot: picked.dir, scope: { full: true } };
      const j = await apiPost<{ ok: boolean; error?: string }>('/backup/setup_restore/restore', body);
      if (!j.ok) throw new Error(j.error || 'Restore failed');
      restoreStep = 'done';
    } catch (err) {
      rsError = 'Failed: ' + (err instanceof Error ? err.message : String(err));
    } finally {
      busy = false;
    }
  }
</script>

<main class="auth-page">
  {#if !showRestore}
    <Card.Root class="login-card w-full max-w-md border-border bg-card">
      <form class="flex flex-col gap-3 p-1" autocomplete="on" onsubmit={authenticate}>
        <Card.Header class="px-0 pt-0">
          <div class="login-brand">
            <span class="logo-dot"></span>
            <Card.Title class="text-xl">Solar Pow Monitor</Card.Title>
          </div>
          <Card.Description>
            {mode === 'setup'
              ? 'First-time setup. Pick a username and password.'
              : 'Sign in to continue.'}
          </Card.Description>
        </Card.Header>

        <div class="grid gap-2">
          <Label for="auth-user">Username</Label>
          <Input
            id="auth-user"
            bind:value={username}
            type="text"
            name="username"
            autocomplete="username"
            required
          />
        </div>
        <div class="grid gap-2">
          <Label for="auth-pass">Password</Label>
          <Input
            id="auth-pass"
            bind:value={password}
            type="password"
            name="password"
            autocomplete={mode === 'setup' ? 'new-password' : 'current-password'}
            required
            minlength={6}
          />
        </div>
        {#if mode === 'setup'}
          <div class="grid gap-2">
            <Label for="auth-confirm">Confirm password</Label>
            <Input
              id="auth-confirm"
              bind:value={confirmation}
              type="password"
              name="confirm"
              autocomplete="new-password"
              required
              minlength={6}
            />
          </div>
        {/if}

        {#if error}
          <div class="login-error" role="alert">{error}</div>
        {/if}

        <Button type="submit" disabled={busy} class="w-full">
          {busy ? 'Please wait…' : mode === 'setup' ? 'Create account' : 'Sign in'}
        </Button>
        <p class="login-foot text-muted-foreground">
          Stored locally on this NAS, never sent off-device. The session cookie is HMAC-signed and lasts
          30 days.
        </p>

        {#if mode === 'setup'}
          <div class="mt-2 border-t border-border pt-3 text-center">
            <p class="hint mb-2">Already have a backup on a NAS?</p>
            <Button variant="ghost" type="button" onclick={() => (showRestore = true)}>
              Restore from backup instead
            </Button>
          </div>
        {/if}
      </form>
    </Card.Root>
  {:else}
    <section class="login-card">
      <div class="login-brand">
        <span class="logo-dot"></span>
        <h1>Restore from backup</h1>
      </div>
      <p class="login-sub">
        Pull settings + telemetry from a remote NAS. After restoring you'll sign in with the
        credentials from the original install.
      </p>

      {#if restoreStep === 'creds'}
        <label class="field"><span class="field-label">NAS host / IP</span><input bind:value={rsHost} type="text" placeholder="nas.local" autocomplete="off" required /></label>
        <label class="field"><span class="field-label">Share name</span><input bind:value={rsShare} type="text" placeholder="backups" autocomplete="off" required /></label>
        <label class="field"><span class="field-label">Subdirectory</span><input bind:value={rsSubdir} type="text" autocomplete="off" /></label>
        <label class="field"><span class="field-label">Username</span><input bind:value={rsUsername} type="text" autocomplete="username" required /></label>
        <label class="field"><span class="field-label">Password</span><input bind:value={rsPassword} type="password" autocomplete="current-password" required /></label>
        <label class="field"><span class="field-label">Domain</span><input bind:value={rsDomain} type="text" autocomplete="off" /></label>
        {#if rsError}<div class="login-error" role="alert">{rsError}</div>{/if}
        {#if rsInfo}<div class="login-error" role="status" style="background:rgba(74,222,128,.12); color:var(--pow-green); border-color:rgba(74,222,128,.2)">{rsInfo}</div>{/if}
        <div class="mt-1.5 flex gap-2">
          <Button variant="ghost" type="button" onclick={testRestore} disabled={busy}>Test</Button>
          <Button type="button" class="flex-1" onclick={listSnapshots} disabled={busy}>List snapshots →</Button>
        </div>
      {:else if restoreStep === 'pick'}
        <p class="login-sub">Pick a snapshot to restore.</p>
        <div style="display:flex; flex-direction:column; gap:8px; margin:6px 0 12px">
          {#each snapshots as s (s.dir)}
            <button
              type="button"
              class="btn btn-ghost"
              style="text-align:left; justify-content:flex-start; padding:10px 12px; line-height:1.4"
              disabled={!s.valid}
              onclick={() => { picked = s; restoreStep = 'confirm'; }}
            >
              <div><strong>{s.valid ? (s.iso || s.dir) : s.dir}</strong></div>
              <div class="hint" style="font-size:11px">
                {#if s.valid}
                  {(s.files || []).length} files · {s.total_bytes ? fmtBytes(s.total_bytes) : '—'}
                {:else}
                  Invalid: {s.error || '?'}
                {/if}
              </div>
            </button>
          {/each}
        </div>
        <button class="btn btn-ghost" type="button" onclick={() => (restoreStep = 'creds')}>← Back</button>
      {:else if restoreStep === 'confirm' && picked}
        <p class="login-sub">
          Restore <strong>{picked.iso || picked.dir}</strong>? This writes the snapshot's
          {(picked.files || []).length} files into <code>/data</code>.
        </p>
        {#if rsError}<div class="login-error" role="alert">{rsError}</div>{/if}
        <div class="mt-2 flex gap-2">
          <Button variant="ghost" type="button" onclick={() => (restoreStep = 'pick')}>← Back</Button>
          <Button type="button" class="flex-1" onclick={runRestore} disabled={busy}>
            {busy ? 'Restoring…' : 'Restore now'}
          </Button>
        </div>
      {:else if restoreStep === 'done'}
        <p class="login-sub" style="color:var(--pow-green)">Restore complete.</p>
        <p class="hint">Your old settings, telemetry, and user account are now on this device.</p>
        <Button type="button" onclick={() => { showRestore = false; restoreStep = 'creds'; }}>
          Continue to sign in
        </Button>
      {/if}

      <p class="login-foot" style="margin-top:14px">
        <button type="button" class="btn btn-ghost" onclick={() => { showRestore = false; restoreStep = 'creds'; }}>
          Cancel and create a new account instead
        </button>
      </p>
    </section>
  {/if}
</main>
