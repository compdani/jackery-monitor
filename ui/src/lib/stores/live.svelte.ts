import { api, apiPost } from '../api';
import type { CloudDevice, FleetDevice, StatusSnapshot, TabName } from '../types';

export type PendingToggle = { expected: boolean; until: number };

/** Shared live dashboard state — mirrors the legacy applyStatus globals. */
class LiveStore {
  status = $state<StatusSnapshot | null>(null);
  devices = $state<CloudDevice[]>([]);
  fleet = $state<FleetDevice[]>([]);
  selectedDeviceId = $state<string | null>(null);
  connectionLabel = $state('connecting…');
  connectionClass = $state('');
  deviceSwitching = $state(false);
  alertMessage = $state('');
  alertVisible = $state(false);
  activeTab = $state<TabName>('live');
  pendingToggles = $state<Record<string, PendingToggle>>({});
  automationDot = $state(false);

  #pendingViewSwitch: { id: string; until: number } | null = null;
  #alertTimer: ReturnType<typeof setTimeout> | undefined;
  #deviceChangeListeners = new Set<(prevSn: string | undefined, nextSn: string | undefined) => void>();

  onDeviceChange(fn: (prevSn: string | undefined, nextSn: string | undefined) => void) {
    this.#deviceChangeListeners.add(fn);
    return () => this.#deviceChangeListeners.delete(fn);
  }

  setDeviceSwitching(on: boolean) {
    this.deviceSwitching = on;
    if (typeof document !== 'undefined') {
      document.body.classList.toggle('device-switching', !!on);
    }
  }

  showAlert(message: string, ms = 8000) {
    this.alertMessage = message;
    this.alertVisible = true;
    if (this.#alertTimer) clearTimeout(this.#alertTimer);
    this.#alertTimer = setTimeout(() => {
      this.alertVisible = false;
    }, ms);
  }

  activeDeviceSn(): string | undefined {
    const id = this.status?.cloud?.selected_device_id;
    const devices = this.status?.cloud?.devices || [];
    const d = devices.find((x) => String(x.device_id) === String(id));
    return d?.device_sn || this.status?.device?.device_sn;
  }

  applyStatus(s: StatusSnapshot | null | undefined) {
    if (!s) return;

    if (this.#pendingViewSwitch) {
      if (Date.now() > this.#pendingViewSwitch.until) {
        this.#pendingViewSwitch = null;
        this.setDeviceSwitching(false);
      } else {
        const frameId =
          s.cloud?.selected_device_id != null
            ? String(s.cloud.selected_device_id)
            : null;
        if (frameId && frameId !== String(this.#pendingViewSwitch.id)) {
          return;
        }
        if (frameId === String(this.#pendingViewSwitch.id)) {
          this.setDeviceSwitching(false);
        }
      }
    }

    const prevSn = this.activeDeviceSn();
    this.status = s;
    this.devices = s.cloud?.devices || [];
    this.fleet = s.cloud?.devices_overview || [];
    this.selectedDeviceId = s.cloud?.selected_device_id ?? null;

    // Clear optimistic output toggles once telemetry catches up (or expires).
    const t = s.telemetry || {};
    for (const port of ['ac', 'dc', 'usb', 'car']) {
      const v = t[`${port}_on`];
      const actual = v === true ? true : v === false ? false : null;
      this.clearPendingIfCaughtUp(port, actual);
    }

    // Connection pill
    let label = s.connection_status || 'unknown';
    let cls = '';
    if (s.connection_status === 'connected') {
      cls = 'is-connected';
      label = 'connected';
    } else if (['scanning', 'connecting'].includes(s.connection_status || '')) {
      cls = 'is-connecting';
    } else if (s.connection_status === 'error') {
      cls = 'is-error';
      label = s.connection_error || 'error';
    }
    this.connectionLabel = label;
    this.connectionClass = cls;

    const nextSn = this.activeDeviceSn();
    if (prevSn !== nextSn) {
      for (const fn of this.#deviceChangeListeners) fn(prevSn, nextSn);
    }
  }

  async selectViewDevice(deviceId: string) {
    if (!deviceId) return;
    this.#pendingViewSwitch = { id: String(deviceId), until: Date.now() + 8000 };
    this.setDeviceSwitching(true);
    try {
      await apiPost('/view/select_device', { device_id: deviceId });
    } catch (err) {
      this.#pendingViewSwitch = null;
      this.setDeviceSwitching(false);
      console.warn('view/select_device failed', err);
    }
  }

  async refreshStatus() {
    try {
      const s = await api<StatusSnapshot>('/status');
      this.applyStatus(s);
    } catch (err) {
      console.warn('status poll failed', err);
    }
  }

  async reconnect() {
    try {
      await apiPost('/reconnect');
      await this.refreshStatus();
    } catch (err) {
      this.showAlert(err instanceof Error ? err.message : 'Reconnect failed');
    }
  }

  /**
   * Toggle an output port. With no `deviceSn`, targets the viewed device and tracks an
   * optimistic pending state. With `deviceSn` (e.g. a linked EcoFlow unit) the command
   * goes straight to that unit and no pending state is kept in the shared store.
   * Resolves true when the command was accepted.
   */
  async setOutput(port: string, on: boolean, deviceSn?: string): Promise<boolean> {
    if (deviceSn) {
      try {
        await apiPost('/set_output', { device_sn: deviceSn, port, on });
        return true;
      } catch (err) {
        this.showAlert(err instanceof Error ? err.message : 'Output command failed');
        return false;
      }
    }
    const sn = this.status?.device?.device_sn;
    if (!sn) return false;
    this.pendingToggles = {
      ...this.pendingToggles,
      [port]: { expected: on, until: Date.now() + 30_000 },
    };
    try {
      await apiPost('/set_output', { device_sn: sn, port, on });
      return true;
    } catch (err) {
      const next = { ...this.pendingToggles };
      delete next[port];
      this.pendingToggles = next;
      this.showAlert(err instanceof Error ? err.message : 'Output command failed');
      return false;
    }
  }

  portState(port: string): boolean | null {
    const t = this.status?.telemetry;
    if (!t) return null;
    const pending = this.pendingToggles[port];
    if (pending && pending.until > Date.now()) return pending.expected;
    const v = t[`${port}_on`];
    if (v === true) return true;
    if (v === false) return false;
    return null;
  }

  clearPendingIfCaughtUp(port: string, actual: boolean | null | undefined) {
    const pending = this.pendingToggles[port];
    if (!pending) return;
    if (actual === pending.expected || pending.until <= Date.now()) {
      const next = { ...this.pendingToggles };
      delete next[port];
      this.pendingToggles = next;
    }
  }
}

export const live = new LiveStore();
