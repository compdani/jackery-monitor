<script lang="ts">
  import { onMount, setContext } from 'svelte';
  import { afterNavigate, goto } from '$app/navigation';
  import { page } from '$app/state';
  import Topbar from '../components/shared/Topbar.svelte';
  import AssistantPanel from '../components/assistant/AssistantPanel.svelte';
  import { probeAuthGate, me } from '$lib/auth';
  import { setUnauthorizedHandler } from '$lib/api';
  import { connectWs } from '$lib/ws';
  import { live } from '$lib/stores/live.svelte';
  import { assistant } from '$lib/stores/assistant.svelte';
  import { VALID_TABS, type TabName } from '$lib/types';
  import { ModeWatcher } from 'mode-watcher';
  import { Button } from '$lib/components/ui/button/index.js';
  import '../app.css';
  import '$lib/styles/global.css';

  let { children } = $props();

  type Gate = 'loading' | 'setup' | 'login' | 'signedIn';
  let gate = $state<Gate>('loading');
  let username = $state('');

  const tabs: { id: TabName; label: string; href: string }[] = [
    { id: 'live', label: 'Live', href: '/' },
    { id: 'energy', label: 'Energy', href: '/energy' },
    { id: 'forecast', label: 'Forecast', href: '/forecast' },
    { id: 'device', label: 'Device', href: '/device' },
    { id: 'automation', label: 'Automation', href: '/automation' },
    { id: 'ble', label: 'BLE Inspector', href: '/ble' },
    { id: 'logs', label: 'Logs', href: '/logs' },
    { id: 'settings', label: 'Settings', href: '/settings' },
  ];

  const AUTH_PATHS = new Set(['/login', '/setup']);

  function tabFromPath(pathname: string): TabName {
    if (pathname === '/' || pathname === '') return 'live';
    const seg = pathname.replace(/^\//, '').split('/')[0] as TabName;
    return VALID_TABS.has(seg) ? seg : 'live';
  }

  const isAuthRoute = $derived(AUTH_PATHS.has(page.url.pathname));
  const currentTab = $derived(tabFromPath(page.url.pathname));

  function syncTabFromPath() {
    live.activeTab = tabFromPath(page.url.pathname);
  }

  function enforceAuthRoute() {
    if (gate === 'loading') return;
    const path = page.url.pathname;
    const onAuth = AUTH_PATHS.has(path);
    if (gate === 'signedIn' && onAuth) {
      void goto('/');
      return;
    }
    if (gate !== 'signedIn' && !onAuth) {
      void goto(gate === 'setup' ? '/setup' : '/login');
    }
  }

  function markSignedIn(name: string) {
    username = name;
    gate = 'signedIn';
    bootLive();
  }

  setContext('markSignedIn', markSignedIn);

  function onSignOut() {
    gate = 'login';
    username = '';
    stopLive();
    void goto('/login');
  }

  let stopWs: (() => void) | undefined;
  let pollTimer: ReturnType<typeof setInterval> | undefined;

  function bootLive() {
    stopWs?.();
    if (pollTimer) clearInterval(pollTimer);
    void live.refreshStatus();
    stopWs = connectWs({
      onStatus: (data) => live.applyStatus(data),
      onAlert: (msg) => live.showAlert(msg),
      onKasaUpdated: () => {
        /* AutomationTab polls; WS nudge is enough for now */
      },
      onAutomationFired: (data) => {
        live.automationDot = true;
        if (data?.name) {
          live.showAlert(
            `Rule fired: ${data.name} → ${(data.action || '').toUpperCase()} ${data.kasa_alias || ''}`,
            5000,
          );
        }
      },
    });
    pollTimer = setInterval(() => void live.refreshStatus(), 30_000);
  }

  function stopLive() {
    stopWs?.();
    stopWs = undefined;
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = undefined;
    }
  }

  afterNavigate(() => {
    syncTabFromPath();
    enforceAuthRoute();
  });

  onMount(() => {
    setUnauthorizedHandler(() => {
      gate = 'login';
      username = '';
      stopLive();
      void goto('/login');
    });

    // One-shot hash → path redirect for old bookmarks (#energy → /energy).
    const hash = location.hash.slice(1) as TabName;
    if (hash && VALID_TABS.has(hash)) {
      const target = hash === 'live' ? '/' : `/${hash}`;
      history.replaceState(null, '', location.pathname + location.search);
      void goto(target, { replaceState: true });
    }

    if (import.meta.env.PROD && 'serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch((e) => {
        console.warn('SW register failed:', e);
      });
    }

    void (async () => {
      const g = await probeAuthGate();
      if (g === 'signedIn') {
        const user = await me();
        username = user?.username || '';
        gate = 'signedIn';
        bootLive();
        if (AUTH_PATHS.has(page.url.pathname)) void goto('/');
      } else {
        gate = g;
        if (!AUTH_PATHS.has(page.url.pathname)) {
          void goto(g === 'setup' ? '/setup' : '/login');
        }
      }
      syncTabFromPath();
    })();

    return () => {
      stopLive();
      setUnauthorizedHandler(null);
    };
  });
</script>

<ModeWatcher
  defaultMode="system"
  themeColors={{ light: '#f8fafc', dark: '#0b0d10' }}
/>

{#if gate === 'loading'}
  <main class="auth-page">
    <div class="login-card" aria-busy="true">
      <div class="login-brand">
        <span class="logo-dot"></span>
        <h1>Solar Pow Monitor</h1>
      </div>
      <p class="login-sub">Connecting…</p>
    </div>
  </main>
{:else if isAuthRoute}
  {@render children()}
{:else if gate === 'signedIn'}
  <div class="app-shell" class:assistant-panel-open={assistant.open}>
    <Topbar onSignOut={onSignOut} />

    <main>
      <nav class="tabs flex flex-wrap gap-1 border-b border-border px-2" aria-label="Dashboard tabs">
        {#each tabs as t (t.id)}
          <Button
            variant={currentTab === t.id ? 'secondary' : 'ghost'}
            size="sm"
            href={t.href}
            class="rounded-b-none border-b-2 border-transparent {currentTab === t.id
              ? 'border-primary'
              : ''}"
            aria-current={currentTab === t.id ? 'page' : undefined}
            data-tab={t.id}
          >
            {t.label}
            {#if t.id === 'automation' && live.automationDot}
              <span class="tab-dot" title="New AI insights pending"></span>
            {/if}
          </Button>
        {/each}
      </nav>

      {@render children()}

      {#if live.alertVisible}
        <div id="alert-banner" class="alert">{live.alertMessage}</div>
      {/if}
    </main>

    <footer class="text-muted-foreground">
      Energy persisted in SQLite{#if username} · {username}{/if}
    </footer>
  </div>

  <AssistantPanel />
{/if}
