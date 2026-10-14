// Exercise the actual page controller with a small DOM and a mocked BLE server.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
class Element {
  constructor(tag = 'div') { this.tag = tag; this.children = []; this.dataset = {}; this.value = ''; this.checked = false; this.hidden = false; this.style = {}; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  addEventListener(name, callback) { this[name] = callback; }
  querySelectorAll(selector) {
    const all = [];
    for (const c of this.children) if (c instanceof Element) {
      if (c.tag === 'input' && (selector === 'input[data-ble-notify]' ? c.dataset.bleNotify : c.checked)) all.push(c);
      all.push(...c.querySelectorAll(selector));
    }
    return all;
  }
}
const elements = new Map();
const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
const requests = [];
let timer, running = false, id = 0, captureEvents = [], captureStatus = 'complete', mixed = true;
const supported = [
  {uuid: 'notify', handle: 2, properties: ['notify']},
  {uuid: 'write', handle: 3, properties: ['write']},
  {uuid: 'indicate', handle: 5, properties: ['indicate']},
];
const channel = (c, status, extra = {}) => ({...c, subscription_properties: c.properties,
  status, event_count: 0, ...extra});
function record(type, fields = {}) { captureEvents.push({seq: captureEvents.length + 1, type, ...fields}); }
function finish(reason) {
  if (captureEvents.some(e => e.type === 'listening_started')) {
    record('unsubscribe_result', channel(supported[0], 'active', {unsubscribe_status: 'stopped'}));
    record('listening_ended', {reason, duration_s: 30, elapsed_s: .25,
      subscribed_count: 1, failed_count: mixed ? 1 : 0, event_count: 0,
      channels: [channel(supported[0], 'active', {unsubscribe_status: 'stopped'}),
        ...(mixed ? [channel(supported[2], 'failed', {error: '<denied>'})] : [])]});
  }
  record('completion', {status: captureStatus}); running = false;
}
vm.runInNewContext(fs.readFileSync('web/ble.js', 'utf8'), {
  document: {getElementById: get, createElement: tag => new Element(tag), createTextNode: text => ({textContent: text})},
  setInterval: callback => { timer = callback; },
  fetch: async (url, options) => {
    requests.push({url, options});
    let data;
    if (url.endsWith('/status')) data = {available: true, active: running ? String(id) : null,
      captures: id ? [{id: String(id), bytes: 123, active: running, status: running ? 'running' : captureStatus}] : []};
    else if (url.endsWith('/sessions')) {
      running = true; id++; captureEvents = []; const opts = JSON.parse(options.body);
      record('metadata', {options: opts});
      if (opts.operation === 'scan') {
        record('advertisement', {address: 'AA', name: '<img src=x>', rssi: -35, manufacturer_data: {'1': {hex: '00ff'}}});
        finish('no_subscriptions_selected');
      } else {
        record('service', {uuid: 'service', handle: 1, characteristics: supported});
        if (!opts.notify.length) {
          record('listening_ended', {reason: 'no_subscriptions_selected', duration_s: 30, elapsed_s: 0,
            subscribed_count: 0, failed_count: 0, event_count: 0, channels: []});
          finish('no_subscriptions_selected');
        } else {
          record('subscription_requested', channel(supported[0], 'requested'));
          record('subscription_active', channel(supported[0], 'active'));
          if (mixed) {
            record('subscription_requested', channel(supported[2], 'requested'));
            record('subscription_error', channel(supported[2], 'failed', {error: '<denied>'}));
          }
          record('listening_started', {duration_s: 30, channels: [channel(supported[0], 'active')]});
          captureStatus = mixed ? 'incomplete' : 'complete';
        }
      }
      data = {id: String(id)};
    }
    else if (url.includes('/events?')) {
      const after = Number(new URL('http://test' + url).searchParams.get('after'));
      data = {events: captureEvents.filter(e => e.seq > after), next: captureEvents.length, active: running, bytes: 123};
    }
    else if (url.endsWith('/stop')) { finish('cancelled'); data = {stopped: true}; }
    else throw new Error('Unexpected URL ' + url);
    return {ok: true, json: async () => data};
  },
});
const tick = () => new Promise(resolve => setImmediate(resolve));
async function poll() { timer(); await tick(); await tick(); }
(async () => {
  get('ble-seconds').value = '10'; get('ble-duration').value = '30';
  await get('ble-scan').onclick(); await poll();
  let request = requests.find(r => r.url.endsWith('/sessions'));
  assert.equal(JSON.parse(request.options.body).read_all, false);
  assert.deepEqual(JSON.parse(request.options.body).notify, []);
  const deviceButton = get('ble-devices').children[0].children[0];
  assert.ok(deviceButton.textContent.includes('<img src=x>'));
  assert.equal(deviceButton.children.length, 0); // Literal text, never HTML.
  await deviceButton.onclick();
  await get('ble-inspect').onclick(); await poll();
  const inputs = get('ble-gatt').querySelectorAll('input[data-ble-notify]');
  assert.equal(inputs.length, 2); // Write-only characteristic has no subscription control.
  assert.ok(inputs.every(c => !c.checked));
  assert.ok(get('ble-listening-result').textContent.includes('no subscriptions selected'));
  await get('ble-select-all').onclick();
  assert.ok(inputs.every(c => c.checked));
  assert.ok(get('ble-selection-summary').textContent.includes('indicate (handle 5)'));
  assert.ok(get('ble-selection-summary').textContent.includes('30 seconds'));
  await get('ble-clear').onclick(); assert.ok(inputs.every(c => !c.checked));
  await get('ble-select-all').onclick(); get('ble-read').checked = true;
  await get('ble-start').onclick();
  request = requests.filter(r => r.url.endsWith('/sessions')).at(-1);
  const options = JSON.parse(request.options.body);
  assert.deepEqual(options.notify, ['2', '5']);
  assert.deepEqual(options.notify_expected, {'2': 'notify', '5': 'indicate'});
  assert.equal(options.read_all, true);
  assert.ok(get('ble-capture-summary').textContent.includes('indicate (handle 5)'));
  assert.equal(get('ble-selection-summary').hidden, true);
  await poll();
  const rows = get('ble-subscription-status').children;
  assert.ok(rows[0].textContent.includes('Subscribed'));
  assert.ok(rows[1].textContent.includes('failed') && rows[1].textContent.includes('<denied>'));
  assert.equal(rows[1].children.length, 0);
  assert.ok(get('ble-listening-result').textContent.includes('Listening on 1'));
  await get('ble-stop').onclick();
  assert.ok(requests.some(r => r.url.endsWith('/stop')));
  assert.ok(get('ble-listening-result').textContent.includes('1 subscribed · 1 failed · 0 events'));
  assert.ok(get('ble-listening-result').textContent.includes('0.25 seconds'));
  assert.ok(get('ble-listening-result').textContent.includes('stopped by user'));
  assert.ok(get('ble-listening-result').textContent.includes('received zero events'));
  assert.ok(get('ble-capture-summary').textContent.includes('Requested: notify (handle 2)'));
  assert.ok(get('ble-captures').children[0].children.some(c => c.href?.endsWith('format=jsonl')));
  // A wholly successful silent capture must read as complete, not a subscription failure.
  mixed = false;
  const next = get('ble-gatt').querySelectorAll('input[data-ble-notify]');
  next[0].checked = true; next[0].change();
  assert.ok(get('ble-selection-summary').textContent.includes('notify (handle 2)'));
  await get('ble-start').onclick(); finish('duration_elapsed'); await poll();
  assert.equal(get('ble-status').textContent, 'complete');
  assert.ok(get('ble-listening-result').textContent.includes('1 subscribed · 0 failed · 0 events'));
  assert.ok(get('ble-listening-result').textContent.includes('duration elapsed'));
  console.log('BLE UI selection, opt-in, active/failed states, counts, zero-event summaries and stop checks passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
