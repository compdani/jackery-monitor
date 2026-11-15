<script lang="ts">
  import { login, setup } from '$lib/auth';
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

</script>

<main class="auth-page">
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
          Stored locally on this Raspberry Pi. Sessions last 30 days.
        </p>

        {#if mode === 'setup'}
          <p class="hint">Local backup restore during setup is pending migration.</p>
        {/if}
      </form>
    </Card.Root>
</main>
