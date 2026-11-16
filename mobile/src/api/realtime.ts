import { AppState } from 'react-native';
import EventSource from 'react-native-sse';
import { connectMonitorRealtime, realtimeConnected } from '../../../web/realtime.js';
import { endpoints } from './client';
import { useConnection } from '../store/connection';
import { useLive } from '../store/live';
import { useSession } from '../store/session';

let stop: (() => void) | undefined;
export function startLive() {
  stopLive();
  if (!useConnection.getState().baseUrl || !useSession.getState().token) { useLive.getState().setConnected(false); return; }
  let stopped = false;
  let running = false;
  let pending = false;
  let energy = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  async function refresh() {
    if (stopped) return;
    if (running) { pending = true; return; }
    running = true;
    const view = useLive.getState().viewDeviceId;
    const previous = useLive.getState().status;
    try {
      const status = await endpoints.status(view);
      if (!stopped && view === useLive.getState().viewDeviceId && previous === useLive.getState().status) useLive.getState().applyStatus(status);
    } catch { /* Retain last good snapshot; HTTP handles expired sessions. */ }
    finally {
      running = false;
      if (!stopped && energy) { energy = false; useLive.getState().energyChanged(); }
      if (!stopped && pending) { pending = false; schedule(); }
    }
  }
  function schedule(kind?: string) {
    if (kind === 'energy' || kind === 'reconnect') energy = true;
    if (timer) return;
    timer = setTimeout(() => { timer = undefined; void refresh(); }, 75);
  }
  const connection = connectMonitorRealtime({
    baseUrl: useConnection.getState().baseUrl || undefined,
    getToken: () => useSession.getState().token,
    getDeviceSn: () => useLive.getState().status?.device?.device_sn,
    sourceFactory: url => new EventSource(url),
    getViewId: () => useLive.getState().viewDeviceId,
    onSnapshot: patch => useLive.getState().applyStatus({ ...useLive.getState().status, ...patch } as import('./types').StatusPayload),
    onQueries: () => useLive.getState().energyChanged(),
    onRefresh: schedule,
    onEvent: event => {
      if (event.kind === 'alert' && typeof event.message === 'string') useLive.getState().pushAlert({ message: event.message });
      if (event.kind === 'automation_fired') useLive.getState().pushAlert({ message: `Rule fired: ${String(event.name || 'Automation')}` });
    },
    onConnected: connected => { useLive.getState().setConnected(connected); if (connected) useLive.getState().setError(null); },
    onError: message => useLive.getState().setError(message),
  });
  const unsubscribe = useLive.subscribe((state, prev) => {
    if (state.viewDeviceId !== prev.viewDeviceId || state.status?.device?.device_sn !== prev.status?.device?.device_sn) { connection.reconnect(); schedule('reconnect'); }
  });
  const appState = AppState.addEventListener('change', state => {
    if (state === 'active') { connection.reconnect(); schedule('reconnect'); }
  });
  const offSession = useSession.subscribe((state, prev) => {
    if (state.token !== prev.token) { if (!state.token) stopLive(); else connection.reconnect(); }
  });
  const poll = setInterval(() => { if (!realtimeConnected()) schedule('energy'); }, 30_000);
  reconnect = () => connection.reconnect();
  schedule('reconnect');
  stop = () => { stopped = true; clearInterval(poll); if (timer) clearTimeout(timer); unsubscribe(); offSession(); appState.remove(); connection.stop(); useLive.getState().setConnected(false); };
}
let reconnect = () => {};
export function reconnectLive() { reconnect(); }
export function stopLive() { stop?.(); stop = undefined; reconnect = () => {}; }
