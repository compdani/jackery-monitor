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
