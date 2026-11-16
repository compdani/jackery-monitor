import { test } from 'node:test';
import assert from 'node:assert/strict';
import { connectMonitorRealtime } from '../web/realtime.js';

class FakeSource {
  static instances = [];
  listeners = new Map();
  closed = false;
  constructor(url) { this.url = url; FakeSource.instances.push(this); }
  addEventListener(name, fn) { this.listeners.set(name, fn); }
  close() { this.closed = true; }
  async emit(name, data) { await this.listeners.get(name)?.({ data: JSON.stringify(data) }); }
}

test('custom SSE subscriptions authenticate, switch devices, and ignore old streams', async () => {
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => { calls.push({ url, init, body: JSON.parse(init.body) }); return { ok: true }; };
  let sn = 'siseli:A';
  const kinds = [];
  const connected = [];
  const connection = connectMonitorRealtime({
    baseUrl: 'https://monitor.example', getDeviceSn: () => sn, getToken: () => 'session-token',
    sourceFactory: url => new FakeSource(url), onRefresh: kind => kinds.push(kind), onConnected: value => connected.push(value),
  });
  try {
    const first = FakeSource.instances.at(-1);
    await first.emit('PB_CONNECT', { clientId: 'first' });
    assert.deepEqual(calls[0].body.subscriptions, ['monitor.fleet', 'monitor.events', 'monitor.device.siseli:A']);
    assert.equal(calls[0].init.headers.get('Authorization'), 'Bearer session-token');
    assert.equal(new URL(first.url).search, ''); // tokens never enter URLs
    await first.emit('monitor.device.siseli:A', { kind: 'energy' });
    assert.deepEqual(kinds, ['reconnect', 'energy']);
    sn = 'ecoflow:B'; connection.reconnect();
    const second = FakeSource.instances.at(-1);
    assert.equal(first.closed, true);
    await first.emit('monitor.device.siseli:A', { kind: 'stale' });
    await second.emit('PB_CONNECT', { clientId: 'second' });
    assert.equal(kinds.includes('stale'), false);
    assert.deepEqual(calls[1].body.subscriptions, ['monitor.fleet', 'monitor.events', 'monitor.device.ecoflow:B']);
    assert.deepEqual(connected, [true, true]);
    connection.stop();
    await second.emit('monitor.fleet', { kind: 'late' });
    assert.equal(kinds.includes('late'), false);
  } finally { connection.stop(); globalThis.fetch = originalFetch; }
});

test('late subscription response cannot revive a stopped client', async () => {
  const originalFetch = globalThis.fetch;
  let resolve;
  globalThis.fetch = () => new Promise(r => { resolve = r; });
  const states = [];
  const connection = connectMonitorRealtime({ getDeviceSn: () => null, sourceFactory: url => new FakeSource(url), onRefresh: () => states.push('refresh'), onConnected: () => states.push('connected') });
  try {
    const pending = FakeSource.instances.at(-1).emit('PB_CONNECT', { clientId: 'late' });
    connection.stop(); resolve({ ok: true }); await pending;
    assert.deepEqual(states, []);
  } finally { connection.stop(); globalThis.fetch = originalFetch; }
});

test('an older subscription failure cannot close a successfully reconnected stream', async () => {
  const originalFetch = globalThis.fetch;
  let resolveFirst;
  let calls = 0;
  globalThis.fetch = async () => {
    if (++calls === 1) return new Promise(resolve => { resolveFirst = resolve; });
    return { ok: true };
  };
  const states = [];
  const connection = connectMonitorRealtime({ getDeviceSn: () => 'A', sourceFactory: url => new FakeSource(url), onRefresh: () => {}, onConnected: value => states.push(value) });
  try {
    const source = FakeSource.instances.at(-1);
    const old = source.emit('PB_CONNECT', { clientId: 'old' });
    await source.emit('PB_CONNECT', { clientId: 'new' });
    resolveFirst({ ok: false, status: 404 }); await old;
    assert.equal(source.closed, false);
    assert.deepEqual(states, [true]);
  } finally { connection.stop(); globalThis.fetch = originalFetch; }
});

test('authentication failure closes the stream without scheduling retries', async () => {
  const originalFetch = globalThis.fetch;
  const originalTimer = globalThis.setTimeout;
  let retries = 0;
  globalThis.fetch = async () => ({ ok: false, status: 401 });
  globalThis.setTimeout = () => { retries++; return 0; };
  const connection = connectMonitorRealtime({ getDeviceSn: () => 'A', sourceFactory: url => new FakeSource(url), onRefresh: () => {} });
  try {
    const source = FakeSource.instances.at(-1);
    await source.emit('PB_CONNECT', { clientId: 'expired' });
    assert.equal(source.closed, true);
    assert.equal(retries, 0);
  } finally { connection.stop(); globalThis.fetch = originalFetch; globalThis.setTimeout = originalTimer; }
});

test('custom alert and automation events reach frontend handlers', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: true });
  const events = [];
  const connection = connectMonitorRealtime({ getDeviceSn: () => 'A', sourceFactory: url => new FakeSource(url), onRefresh: () => {}, onEvent: event => events.push(event) });
  try {
    const source = FakeSource.instances.at(-1);
    await source.emit('PB_CONNECT', { clientId: 'events' });
    await source.emit('monitor.events', { kind: 'automation_fired', id: 'rule-1', name: 'Charge' });
    await source.emit('monitor.events', { kind: 'alert', message: 'Battery low' });
    assert.equal(events[0].kind, 'automation_fired');
    assert.equal(events[1].message, 'Battery low');
  } finally { connection.stop(); globalThis.fetch = originalFetch; }
});

test('payload SSE applies status and serves subscribed energy without follow-up GETs', async () => {
  const { readRealtimeQuery, fetchRealtimeQuery, realtimeConnected } = await import('../web/realtime.js');
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => { calls.push({ url, init, body: JSON.parse(init.body) }); return { ok: true }; };
  const patches = [];
  let energyChanges = 0;
  const refreshes = [];
  const path = '/api/energy/history?hours=24&device_sn=siseli%3AA';
  const canonical = '/api/energy/history?device_sn=siseli%3AA&hours=24';
  const connection = connectMonitorRealtime({
    getDeviceSn: () => 'siseli:A', getViewId: () => 'device-A', sourceFactory: url => new FakeSource(url),
    onSnapshot: patch => patches.push(patch), onQueries: () => energyChanges++, onRefresh: kind => refreshes.push(kind),
  });
  try {
    assert.equal(readRealtimeQuery(path), undefined); // first read registers the query
    const source = FakeSource.instances.at(-1);
    await source.emit('PB_CONNECT', { clientId: 'payload' });
    assert.ok(calls[0].body.subscriptions.some(topic => topic.startsWith('monitor.data?options=')));
    assert.ok(calls[0].body.subscriptions.includes('monitor.query.' + encodeURIComponent(canonical)));
    await source.emit('monitor.data', { kind: 'data', status: { telemetry: { battery_percent: 49 } }, queries: { [canonical]: { history: [{ ts: 1, solar_wh: 10 }] } } });
    assert.equal(realtimeConnected(), true);
    assert.equal(patches[0].telemetry.battery_percent, 49);
    assert.equal(energyChanges, 1);
    const result = await fetchRealtimeQuery(path);
    assert.equal((await result.json()).history[0].solar_wh, 10);
    await source.emit('monitor.device.siseli:A', { kind: 'telemetry' });
    assert.deepEqual(refreshes, []); // no invalidation-triggered HTTP reads
    assert.equal(calls.length, 1); // subscription POST is the only request
    connection.reconnect();
    assert.equal(readRealtimeQuery(path), undefined); // old-view data never crosses streams
    await source.emit('monitor.data', { status: { stale: true } });
    assert.equal(patches.length, 1);
  } finally { connection.stop(); globalThis.fetch = originalFetch; }
});
