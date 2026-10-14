// Hardware-free interaction check of the actual page controller with a tiny DOM.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
class Element {
  constructor(tag = 'div') { this.tag = tag; this.children = []; this.dataset = {}; this.value = ''; this.checked = false; this.hidden = false; this.style = {}; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  addEventListener(name, callback) { this[name] = callback; }
  querySelectorAll() {
    const all = [];
    for (const c of this.children) if (c instanceof Element) {
      if (c.tag === 'input' && c.checked) all.push(c);
      all.push(...c.querySelectorAll());
    }
    return all;
  }
}
const elements = new Map();
const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
const requests = [];
let timer, running = false;
const id = 'example';
const events = [
  {seq: 1, type: 'advertisement', address: 'AA', name: '<img src=x>', rssi: -35, manufacturer_data: {'1': {hex: '00ff'}}},
  {seq: 2, type: 'service', uuid: 'service', handle: 1, characteristics: [{uuid: 'notify', handle: 2, properties: ['notify']}, {uuid: 'write', handle: 3, properties: ['write']}]},
  {seq: 3, type: 'completion', status: 'complete'},
];
vm.runInNewContext(fs.readFileSync('web/ble.js', 'utf8'), {
  document: {getElementById: get, createElement: tag => new Element(tag), createTextNode: text => ({textContent: text})},
  setInterval: callback => { timer = callback; },
  fetch: async (url, options) => {
    requests.push({url, options});
    let data;
    if (url.endsWith('/status')) data = {available: true, active: running ? id : null, captures: [{id, bytes: 123, active: running, status: running ? 'running' : 'complete'}]};
    else if (url.endsWith('/sessions')) { running = true; data = {id}; }
    else if (url.includes('/events?')) { running = false; data = {events: url.includes('after=0&') ? events : [], next: 3, active: false, bytes: 123}; }
    else if (url.endsWith('/stop')) { running = false; data = {stopped: true}; }
    else throw new Error('Unexpected URL ' + url);
    return {ok: true, json: async () => data};
  },
});
const tick = () => new Promise(resolve => setImmediate(resolve));
(async () => {
  get('ble-seconds').value = '10'; get('ble-duration').value = '30';
  await get('ble-scan').onclick();
  let request = requests.find(r => r.url.endsWith('/sessions'));
  assert.equal(JSON.parse(request.options.body).read_all, false);
  assert.deepEqual(JSON.parse(request.options.body).notify, []);
  timer(); await tick(); await tick();
  const deviceButton = get('ble-devices').children[0].children[0];
  assert.ok(deviceButton.textContent.includes('<img src=x>')); // Literal text, never HTML.
  assert.equal(deviceButton.children.length, 0);
  const notify = get('ble-gatt').children[1].children[0];
  assert.equal(notify.checked, false);
  notify.checked = true; get('ble-address').value = 'AA'; get('ble-read').checked = true;
  await get('ble-start').onclick();
  request = requests.filter(r => r.url.endsWith('/sessions')).at(-1);
  const options = JSON.parse(request.options.body);
  assert.deepEqual(options.notify, ['2']);
  assert.deepEqual(options.notify_expected, {'2': 'notify'});
  assert.equal(options.read_all, true);
  await get('ble-stop').onclick();
  assert.ok(requests.some(r => r.url.endsWith('/stop')));
  assert.ok(get('ble-captures').children[0].children.some(c => c.href?.endsWith('format=jsonl')));
  console.log('BLE UI mocked scan, safe rendering, opt-in subscription, stop and download checks passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
