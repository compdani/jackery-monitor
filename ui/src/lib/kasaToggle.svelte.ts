import { api, apiPost } from './api';
import { live } from './stores/live.svelte';

/** Reactive Kasa plug toggle bound to a controller config endpoint. */
export function createKasaPlugToggle(opts: {
  controllerName: string;
  configPath: string;
  revertWindow: string;
}) {
  let hidden = $state(true);
  let isOn = $state(false);
  let stateLabel = $state('—');
  let warn = $state(false);
  let pending = $state(false);
  let title = $state('');
  let host = $state<string | null>(null);
  let busy = false;

  async function readHost() {
    const deviceSn = live.activeDeviceSn();
    if (!deviceSn) return { deviceSn: null as string | null, cfg: null as Record<string, unknown> | null };
    try {
      const j = await api<{ config?: Record<string, unknown> }>(
        `${opts.configPath}?device_sn=${encodeURIComponent(deviceSn)}`,
      );
      return { deviceSn, cfg: j.config || null };
    } catch {
      return { deviceSn, cfg: null };
    }
  }

  async function refresh() {
    const { deviceSn, cfg } = await readHost();
    if (!deviceSn) {
      hidden = true;
      return;
    }
    const h = (cfg?.kasa_device_host as string) || '';
    if (!h) {
      hidden = true;
      host = null;
      return;
    }
    host = h;
    hidden = false;
    const mode = (cfg?.mode as string) || 'off';
    if (mode === 'active') {
      warn = true;
      title = `Plug ${h} · ${opts.controllerName} ${mode}\nWarning: your manual toggle may be reverted within ${opts.revertWindow}.`;
    } else {
      warn = false;
      title = `Plug ${h} · ${opts.controllerName} ${mode}`;
    }
    if (!busy) {
      try {
        const st = await api<{ is_on?: boolean }>(
          `/kasa/status?host=${encodeURIComponent(h)}`,
        );
        isOn = !!st.is_on;
        stateLabel = isOn ? 'ON' : 'OFF';
      } catch {
        isOn = false;
        stateLabel = '—';
      }
    }
  }

  async function toggle() {
    if (busy || !host) return;
    const wantOn = !isOn;
    busy = true;
    pending = true;
    stateLabel = wantOn ? 'ON…' : 'OFF…';
    try {
      await apiPost('/kasa/test', { host, on: wantOn });
      isOn = wantOn;
      stateLabel = wantOn ? 'ON' : 'OFF';
      pending = false;
      warn = false;
      busy = false;
      void refresh();
    } catch (e) {
      busy = false;
      pending = false;
      await refresh().catch(() => {});
      warn = true;
      title = `Last toggle failed: ${e instanceof Error ? e.message : e}\nClick to retry.`;
    }
  }

  return {
    get hidden() {
      return hidden;
    },
    get isOn() {
      return isOn;
    },
    get stateLabel() {
      return stateLabel;
    },
    get warn() {
      return warn;
    },
    get pending() {
      return pending;
    },
    get title() {
      return title;
    },
    refresh,
    toggle,
  };
}
