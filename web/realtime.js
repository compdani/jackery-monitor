/** PocketBase custom-topic SSE protocol, shared by web and React Native.
 * @param {import('./realtime').RealtimeOptions} options
 */
export function connectMonitorRealtime(options) {
  let stopped = false;
  let retryDelay = 2000;
  /** @type {any} */
  let source;
  /** @type {ReturnType<typeof setTimeout> | undefined} */
  let retry;
  const base = (options.baseUrl || '').replace(/\/$/, '');
  function connect() {
    if (stopped) return;
    if (retry) clearTimeout(retry);
    source?.close();
    const current = options.sourceFactory
      ? options.sourceFactory(`${base}/api/realtime`)
      : new EventSource(`${base}/api/realtime`, { withCredentials: true });
    source = current;
    let subscriptionGeneration = 0;
    const active = () => !stopped && source === current;
    const sn = options.getDeviceSn();
    const topics = ['monitor.fleet', 'monitor.events', ...(sn ? [`monitor.device.${sn}`] : [])];
    for (const topic of topics) {
      current.addEventListener(topic, (/** @type {{data?: string}} */ event) => {
        if (!active()) return;
        try {
          const data = JSON.parse(event.data || '{}');
          if (topic === 'monitor.events') options.onEvent?.(data);
          options.onRefresh(typeof data.kind === 'string' ? data.kind : 'status');
        } catch { /* Ignore malformed notifications; fallback polling remains. */ }
      });
    }
    current.addEventListener('PB_CONNECT', async (/** @type {{data?: string}} */ event) => {
      if (!active()) return;
      const generation = ++subscriptionGeneration;
      const currentSubscription = () => active() && generation === subscriptionGeneration;
      try {
        const { clientId } = JSON.parse(event.data || '{}');
        if (typeof clientId !== 'string') throw new Error('Missing realtime client ID');
        const token = options.getToken?.();
        const headers = new Headers({ 'Content-Type': 'application/json' });
        if (token) headers.set('Authorization', `Bearer ${token}`);
        const response = await fetch(`${base}/api/realtime`, {
          method: 'POST', credentials: 'include', headers,
          body: JSON.stringify({ clientId, subscriptions: topics }),
        });
        if (!currentSubscription()) return;
        if (response.status === 401 || response.status === 403) {
          options.onConnected?.(false);
          options.onError?.('Realtime session expired');
          options.onRefresh('reconnect');
          current.close();
          return; // Permanent auth failures require login or explicit reconnect.
        }
        if (!response.ok) throw new Error(`Realtime subscription failed (${response.status})`);
        retryDelay = 2000;
        options.onConnected?.(true);
        // Initial connection and every reconnect reconcile anything missed offline.
        options.onRefresh('reconnect');
      } catch (error) {
        if (!currentSubscription()) return;
        options.onConnected?.(false);
        options.onError?.(error instanceof Error ? error.message : 'Realtime connection failed');
        options.onRefresh('reconnect');
        current.close();
        retry = setTimeout(connect, retryDelay);
        retryDelay = Math.min(30_000, retryDelay * 2);
      }
    });
    current.addEventListener('error', () => {
      if (active()) options.onConnected?.(false);
      // EventSource (native or RN polyfill) owns automatic reconnect.
    });
  }
  connect();
  return {
    reconnect() { if (!stopped) connect(); },
    stop() { stopped = true; if (retry) clearTimeout(retry); source?.close(); },
  };
}
