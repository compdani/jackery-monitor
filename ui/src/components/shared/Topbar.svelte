<script lang="ts">
  import { goto } from '$app/navigation';
  import { live } from '$lib/stores/live.svelte';
  import { assistant } from '$lib/stores/assistant.svelte';
  import { logout } from '$lib/auth';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import SparklesIcon from '@lucide/svelte/icons/sparkles';
  import ThemeToggle from './ThemeToggle.svelte';

  interface Props {
    onSignOut?: () => void;
  }
  let { onSignOut }: Props = $props();

  let reconnectBusy = $state(false);

  const showPicker = $derived((live.devices?.length ?? 0) >= 2);
  const mockMode = $derived(!!live.status?.mock_mode);

  async function onDeviceChange(e: Event) {
    const id = (e.currentTarget as HTMLSelectElement).value;
    if (id) await live.selectViewDevice(id);
  }

  async function onReconnect() {
    reconnectBusy = true;
    try {
      await live.reconnect();
    } finally {
      setTimeout(() => (reconnectBusy = false), 800);
    }
  }

  async function onLogout() {
    if (!confirm('Sign out of Solar Pow Monitor?')) return;
    try {
      await logout();
    } catch {
      /* still clear local session */
    }
    onSignOut?.();
  }

  function onBrandClick(e: MouseEvent) {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    e.preventDefault();
    void goto('/');
  }
</script>

<header class="topbar">
  <a href="/" class="brand" title="Back to Live" onclick={onBrandClick}>
    <span class="logo-dot"></span>
    <h1>Solar Pow Monitor</h1>
  </a>

  <div class="topbar-right">
    {#if showPicker}
      <label class="device-picker">
        <span class="dp-label">Device</span>
        <select
          class="device-select"
          value={live.selectedDeviceId ?? ''}
          onchange={onDeviceChange}
        >
          {#each live.devices as d (d.device_id)}
            <option value={d.device_id}>
              {d.name || d.model_name || d.device_sn || d.device_id}
            </option>
          {/each}
        </select>
      </label>
    {/if}

    {#if live.deviceSwitching}
      <Badge variant="secondary" class="gap-1.5 font-normal">
        <span class="spinner" aria-hidden="true"></span>
        loading device…
      </Badge>
    {/if}

    {#if mockMode}
      <Badge variant="outline">mock</Badge>
    {/if}

    <span class="pill pill-conn {live.connectionClass}">
      <span class="conn-dot"></span>
      <span>{live.connectionLabel}</span>
    </span>

    <ThemeToggle />

    <Button
      variant={assistant.open ? 'secondary' : 'ghost'}
      size="sm"
      class="gap-1.5"
      title="AI Assistant"
      aria-label="Open AI Assistant"
      aria-pressed={assistant.open}
      onclick={() => assistant.toggle()}
    >
      <SparklesIcon class="size-4" />
      <span class="asst-topbar-label">AI</span>
    </Button>

    <Button variant="ghost" size="sm" disabled={reconnectBusy} onclick={onReconnect}>
      Reconnect
    </Button>
    <Button variant="ghost" size="sm" onclick={onLogout}>Sign out</Button>
  </div>
</header>
