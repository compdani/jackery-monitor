import { connectMonitorRealtime } from '../../../web/realtime.js';
import { api } from './api';
import { live } from './stores/live.svelte';
import type { StatusSnapshot } from './types';

export function connectRealtime(): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let running = false;
  let pending = false;
  let energyChanged = false;
  async function refresh() {
    if (stopped) return;
    if (running) { pending = true; return; }
    running = true;
    const view = live.selectedDeviceId;
    const previous = live.status;
    try {
      const status = await api<StatusSnapshot>(`/status${view ? `?view_device_id=${encodeURIComponent(view)}` : ''}`);
      if (!stopped && view === live.selectedDeviceId && live.status === previous) live.applyStatus(status);
    } catch { /* HTTP auth handling and the fallback poll own recovery. */ }
    finally {
      running = false;
      if (!stopped && energyChanged) { energyChanged = false; live.notifyEnergyChange(); }
      if (!stopped && pending) { pending = false; schedule(); }
    }
  }
  function schedule(kind?: string) {
    if (kind === 'energy' || kind === 'reconnect') energyChanged = true;
    if (timer) return;
    timer = setTimeout(() => { timer = undefined; void refresh(); }, 75);
  }
  const connection = connectMonitorRealtime({ getDeviceSn: () => live.activeDeviceSn(), getViewId: () => live.selectedDeviceId,
    onSnapshot: patch => { live.applyStatus({ ...live.status, ...patch } as StatusSnapshot); },
    onQueries: () => live.notifyEnergyChange(), onRefresh: schedule,
    onEvent: event => {
      if (event.kind === 'automation_fired') {
        live.automationDot = true;
        live.showAlert(`Rule fired: ${String(event.name || 'Automation')}`, 5000);
      } else if (event.kind === 'alert' && typeof event.message === 'string') live.showAlert(event.message);
      if (event.kind === 'kasa_updated') window.dispatchEvent(new Event('monitor-kasa-updated'));
    },
  });
  const unsubscribe = live.onDeviceChange(() => { connection.reconnect(); schedule('reconnect'); });
  return () => { stopped = true; unsubscribe(); if (timer) clearTimeout(timer); connection.stop(); };
}
