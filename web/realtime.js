// Active query subscriptions and pushed results are shared with API adapters.
const queries = new Map();
let queryChanged;
let connected = false;
let lastData = 0;
export function resetRealtimeQueries() { queries.clear(); queryChanged?.(); }
export function realtimeConnected() { return connected && Date.now() - lastData < 15_000; }
function queryKey(path) {
  const [base, search = ''] = path.split('?');
  const q = new URLSearchParams(search); q.sort();
  return base + (q.size ? `?${q}` : '');
}
export function readRealtimeQuery(path) {
  const key = queryKey(path);
  if (!/^\/api\/energy\/(totals|daily|history|devices)$/.test(key.split('?')[0]) || key.length > 600) return undefined;
  const params = new URLSearchParams(key.split('?')[1] || '');
  for (const name of params.keys()) if (!['device_sn', 'hours', 'bucket_s', 'days'].includes(name)) return undefined;
  for (const [name, limit] of [['hours', 8760], ['days', 365]]) {
    const raw = params.get(name);
    if (raw !== null && (!/^\d+$/.test(raw) || Number(raw) < 1 || Number(raw) > limit)) return undefined;
  }
  if (!queries.has(key)) {
    const [base, search = ''] = key.split('?');
    const sn = new URLSearchParams(search).get('device_sn');
    // Changing range/bucket replaces the old query for that device.
    for (const previous of queries.keys()) {
      const [oldBase, oldSearch = ''] = previous.split('?');
      if (base === oldBase && sn === new URLSearchParams(oldSearch).get('device_sn')) queries.delete(previous);
    }
    if (queries.size >= 16) return undefined; // Overflow uses HTTP; never churn active subscriptions.
    queries.set(key, null); queryChanged?.();
  }
  const entry = queries.get(key);
  return realtimeConnected() && entry ? entry.data : undefined;
}
export async function fetchRealtimeQuery(path) {
  const data = readRealtimeQuery(path);
  return data === undefined ? fetch(path) : new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } });
}

/** PocketBase custom-topic SSE protocol, shared by web and React Native.
 * @param {import('./realtime').RealtimeOptions} options
 */
export function connectMonitorRealtime(options) {
  let stopped = false;
  let retryDelay = 2000;
  let updateSubscriptions;
  let queryTimer;
  for (const key of queries.keys()) queries.set(key, null);
  connected = false;
  queryChanged = () => { if (!queryTimer) queryTimer = setTimeout(() => { queryTimer = undefined; updateSubscriptions?.(); }, 100); };
  /** @type {any} */
  let source;
  /** @type {ReturnType<typeof setTimeout> | undefined} */
  let retry;
  const base = (options.baseUrl || '').replace(/\/$/, '');
  function connect() {
    if (stopped) return;
    if (retry) clearTimeout(retry);
    source?.close(); connected = false; lastData = 0;
    for (const key of queries.keys()) queries.set(key, null);
    const current = options.sourceFactory
      ? options.sourceFactory(`${base}/api/realtime`)
      : new EventSource(`${base}/api/realtime`, { withCredentials: true });
    source = current;
    let subscriptionGeneration = 0;
    const active = () => !stopped && source === current;
    const sn = options.getDeviceSn();
    const topics = options.onSnapshot ? ['monitor.events'] : ['monitor.fleet', 'monitor.events', ...(sn ? [`monitor.device.${sn}`] : [])];
    const dataTopic = 'monitor.data?options=' + encodeURIComponent(JSON.stringify({ query: { view: options.getViewId?.() || '' } }));
    current.addEventListener('monitor.data', event => {
      if (!active()) return;
      try {
        const data = JSON.parse(event.data || '{}');
        for (const [path, value] of Object.entries(data.queries || {})) {
          const key = queryKey(path);
          if (queries.has(key)) queries.set(key, { data: value });
        }
        if (data.snapshot === true || Object.keys(data.status || {}).length) options.onSnapshot?.(data.status || {}, data.snapshot === true);
        if (Object.keys(data.queries || {}).length) options.onQueries?.();
        lastData = Date.now();
      } catch {
        // A queued delta is not acknowledged by the browser. Reconnect for a
        // full baseline if parsing or applying it fails; never mask failure
        // with a healthy heartbeat and permanently suppress recovery reads.
        connected = false; lastData = 0;
        current.close(); options.onConnected?.(false);
        options.onError?.('Could not apply realtime payload');
        options.onRefresh('reconnect');
        retry = setTimeout(connect, retryDelay);
      }
    });
    for (const topic of topics) {
      current.addEventListener(topic, (/** @type {{data?: string}} */ event) => {
        if (!active()) return;
        try {
          const data = JSON.parse(event.data || '{}');
          if (topic === 'monitor.events') options.onEvent?.(data);
          if (!options.onSnapshot) options.onRefresh(typeof data.kind === 'string' ? data.kind : 'status');
        } catch { /* Ignore malformed notifications; fallback polling remains. */ }
      });
    }
    let clientId;
    async function subscribe() {
      if (!active()) return;
      const generation = ++subscriptionGeneration;
      const currentSubscription = () => active() && generation === subscriptionGeneration;
      try {
        if (typeof clientId !== 'string') return;
        const token = options.getToken?.();
        const headers = new Headers({ 'Content-Type': 'application/json' });
        if (token) headers.set('Authorization', `Bearer ${token}`);
        const response = await fetch(`${base}/api/realtime`, {
          method: 'POST', credentials: 'include', headers,
          body: JSON.stringify({ clientId, subscriptions: [...topics, ...(options.onSnapshot ? [dataTopic, ...[...queries.keys()].map(path => 'monitor.query.' + encodeURIComponent(path))] : [])] }),
        });
        if (!currentSubscription()) return;
        if (response.status === 401 || response.status === 403) {
          connected = false; options.onConnected?.(false);
          options.onError?.('Realtime session expired');
          options.onRefresh('reconnect');
          current.close();
          return; // Permanent auth failures require login or explicit reconnect.
        }
        if (!response.ok) throw new Error(`Realtime subscription failed (${response.status})`);
        retryDelay = 2000;
        connected = true;
        options.onConnected?.(true);
        // Initial connection and every reconnect reconcile anything missed offline.
        if (!options.onSnapshot) options.onRefresh('reconnect');
      } catch (error) {
        if (!currentSubscription()) return;
        connected = false; options.onConnected?.(false);
        options.onError?.(error instanceof Error ? error.message : 'Realtime connection failed');
        options.onRefresh('reconnect');
        current.close();
        retry = setTimeout(connect, retryDelay);
        retryDelay = Math.min(30_000, retryDelay * 2);
      }
    }
    let inFlightClientId;
    let queued = false;
    async function refreshSubscriptions() {
      if (typeof clientId !== 'string') return;
      if (inFlightClientId === clientId) { queued = true; return; }
      const id = clientId;
      inFlightClientId = id;
      await subscribe();
      if (inFlightClientId === id) {
        inFlightClientId = undefined;
        if (queued) { queued = false; void refreshSubscriptions(); }
      }
    }
    updateSubscriptions = refreshSubscriptions;
    current.addEventListener('PB_CONNECT', async event => {
      if (!active()) return;
      try { clientId = JSON.parse(event.data || '{}').clientId; } catch { return; }
      connected = false;
      await refreshSubscriptions();
    });
    current.addEventListener('error', () => {
      if (active()) { connected = false; options.onConnected?.(false); }
      // EventSource (native or RN polyfill) owns automatic reconnect.
    });
  }
  connect();
  return {
    reconnect() { if (!stopped) connect(); },
    stop() { stopped = true; connected = false; queryChanged = undefined; if (queryTimer) clearTimeout(queryTimer); if (retry) clearTimeout(retry); source?.close(); },
  };
}
