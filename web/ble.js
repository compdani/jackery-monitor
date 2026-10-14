/* BLE inspector: all device-supplied content is rendered as text. */
(() => {
  const el = id => document.getElementById('ble-' + id);
  let active = null, viewing = null, after = 0, busy = false;
  let recent = [];
  const deviceRows = new Map();
  const channels = new Map();
  let listeningResult = null;
  function subscriptionInputs() { return [...el('gatt').querySelectorAll('input[data-ble-notify]')]; }
  function updateSelection() {
    const checked = subscriptionInputs().filter(c => c.checked);
    const selected = checked.map(c => `${c.value} (handle ${c.dataset.handle})`);
    el('selection-summary').textContent = selected.length
      ? `Selected: ${selected.join(', ')} · listen for up to ${el('duration').value} seconds.`
      : 'No subscriptions selected. Start will only discover structure and optionally read.';
  }
  function resetCapture() {
    channels.clear(); listeningResult = null;
    el('capture-summary').textContent = '';
    el('subscription-status').replaceChildren();
    el('listening-result').textContent = '';
  }
  function captureSummary(options) {
    const selected = (options.notify || []).map(key => options.notify_expected?.[key]
      ? `${options.notify_expected[key]} (handle ${key})` : key);
    el('capture-summary').textContent = `Capture target: ${options.address || options.name || 'scan filters'} · ` +
      (selected.length ? `Requested: ${selected.join(', ')} · listening duration: ${options.duration ?? 30} seconds.`
        : 'No subscriptions requested.');
  }
  function renderSubscriptions() {
    el('subscription-status').replaceChildren();
    for (const channel of channels.values()) {
      const row = document.createElement('p');
      const status = channel.status === 'active'
        ? (listeningResult ? 'Subscribed (listening ended)' : 'Subscribed') : channel.status;
      row.textContent = `${channel.uuid} · handle ${channel.handle} · supports ${(channel.subscription_properties || []).join(" / ")} · ${status} · ` +
        `${channel.event_count || 0} events${channel.error ? ': ' + channel.error : ''}` +
        (channel.unsubscribe_status ? ` · unsubscribe: ${channel.unsubscribe_status}${channel.unsubscribe_error ? ': ' + channel.unsubscribe_error : ''}` : '');
      el('subscription-status').append(row);
    }
  }
  async function api(path, options = {}) {
    const response = await fetch('/api/ble' + path, options);
    if (!response.ok) throw new Error(await response.text());
    return response.json();
  }
  function button(text, callback) {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'btn btn-ghost'; b.textContent = text;
    b.onclick = () => Promise.resolve(callback()).catch(showError);
    return b;
  }
  function showError(error) { el('status').textContent = error.message; }
  function renderDevices() {
    el('devices').replaceChildren();
    for (const row of deviceRows.values()) {
      const line = document.createElement('div');
      line.append(button(`${row.name || '(unnamed)'} · ${row.address} · ${row.rssi ?? '?'} dBm`, () => {
        el('address').value = row.address; el('name').value = '';
        el('gatt').replaceChildren(); updateSelection();
      }));
      const details = document.createElement('pre');
      details.style.whiteSpace = 'pre-wrap';
      details.textContent = JSON.stringify(row, null, 2);
      line.append(details); el('devices').append(line);
    }
  }
  function process(event) {
    recent.push(event);
    if (recent.length > 200) recent.shift();
    if (event.type === 'advertisement') {
      deviceRows.set(event.address, event);
      // Keep the live device list bounded too; the journal holds all events.
      if (deviceRows.size > 200) deviceRows.delete(deviceRows.keys().next().value);
    }
    if (event.type === 'metadata') captureSummary(event.options || {});
    if (event.type === 'service') {
      const heading = document.createElement('h4');
      heading.textContent = `Service ${event.uuid} (handle ${event.handle})`;
      el('gatt').append(heading);
      for (const char of event.characteristics) {
        const label = document.createElement('label');
        label.style.display = 'block';
        if (char.properties.some(p => p === 'notify' || p === 'indicate')) {
          const checkbox = document.createElement('input');
          checkbox.type = 'checkbox'; checkbox.value = char.uuid;
          checkbox.dataset.handle = String(char.handle);
          checkbox.dataset.bleNotify = 'true'; checkbox.disabled = !!active;
          checkbox.addEventListener('change', updateSelection);
          label.append(checkbox, document.createTextNode(' Subscribe to events: '));
        }
        label.append(document.createTextNode(` ${char.uuid} · handle ${char.handle} · ${char.properties.join(', ')}`));
        el('gatt').append(label);
      }
      updateSelection();
    }
    if (['subscription_requested', 'subscription_active', 'subscription_error', 'unsubscribe_result'].includes(event.type)) {
      channels.set(String(event.handle), {...event});
      renderSubscriptions();
    }
    if (event.type === 'notification') {
      const channel = channels.get(String(event.handle));
      if (channel) channel.event_count = (channel.event_count || 0) + 1;
      renderSubscriptions();
    }
    if (event.type === 'listening_started') {
      el('listening-result').textContent = `Listening on ${event.channels.length} subscribed channel(s) for up to ${event.duration_s} seconds…`;
    }
    if (event.type === 'listening_ended') {
      listeningResult = event;
      for (const channel of event.channels) channels.set(String(channel.handle), channel);
      renderSubscriptions();
      const reasons = {duration_elapsed: 'duration elapsed', cancelled: 'stopped by user or server shutdown',
        disconnected: 'device disconnected', storage_failure: 'capture storage failure',
        all_subscriptions_failed: 'all subscriptions failed', no_subscriptions_selected: 'no subscriptions selected', error: 'operation failed'};
      el('listening-result').textContent = `${event.subscribed_count} subscribed · ${event.failed_count} failed · ` +
        `${event.event_count} events · listened for ${Number(event.elapsed_s).toFixed(2)} seconds · ${reasons[event.reason] || event.reason}.` +
        (event.subscribed_count > 0 && event.event_count === 0
          ? ' Confirmed subscriptions received zero events; no controller data was received.' : '');
    }
    if (event.type === 'completion') el('status').textContent = `${event.status}${event.error ? ': ' + event.error : ''}`;
  }
  async function refresh() {
    const state = await api('/status');
    active = state.active;
    el('stop').disabled = !active;
    for (const id of ['scan', 'inspect', 'start', 'select-all', 'clear', 'duration', 'read', 'name', 'address']) el(id).disabled = !!active;
    subscriptionInputs().forEach(c => { c.disabled = !!active; });
    el('selection-summary').hidden = !!active;
    el('captures').replaceChildren();
    for (const capture of state.captures) {
      const row = document.createElement('p');
      row.append(document.createTextNode(`${capture.id} · ${capture.bytes.toLocaleString()} bytes · ${capture.status}${capture.error ? ': ' + capture.error : ''} `));
      row.append(button('View', async () => {
        viewing = capture.id; after = 0; recent = []; deviceRows.clear(); el('gatt').replaceChildren();
        resetCapture(); updateSelection();
        await poll();
      }));
      if (!capture.active) {
        for (const format of ['json', 'jsonl']) {
          const link = document.createElement('a');
          link.href = `/api/ble/captures/${capture.id}/download?format=${format}`;
          link.textContent = ` ${format.toUpperCase()} `; row.append(link);
        }
        row.append(button('Delete', async () => {
          await api('/captures/' + capture.id, {method: 'DELETE'});
          if (viewing === capture.id) viewing = null;
          await refresh();
        }));
      }
      el('captures').append(row);
    }
    if (active && !viewing) { viewing = active; after = 0; }
    if (!state.available && !active) el('status').textContent = state.error;
  }
  async function poll() {
    if (!viewing || busy) return;
    busy = true;
    try {
      const result = await api(`/captures/${viewing}/events?after=${after}&limit=200`);
      result.events.forEach(process); after = result.next;
      el('events').textContent = recent.map(e => JSON.stringify(e)).join('\n');
      renderDevices();
      if (result.active) el('status').textContent = `Capturing · ${result.bytes.toLocaleString()} bytes`;
      if (!result.active) await refresh();
    } finally { busy = false; }
  }
  async function start(operation, discoverOnly = false) {
    const checked = discoverOnly || operation === 'scan' ? [] : subscriptionInputs().filter(c => c.checked);
    const selected = checked.map(c => c.dataset.handle);
    const expected = Object.fromEntries(checked.map(c => [c.dataset.handle, c.value]));
    const address = el('address').value.trim() || null;
    const name = el('name').value.trim() || null;
    if (operation === 'inspect' && !address && !name) throw new Error('Select a device or enter a name/address.');
    const options = {
      operation, address, name: operation === 'inspect' && address ? null : name,
      seconds: Number(el('seconds').value), duration: Number(el('duration').value),
      read_all: operation === 'inspect' && !discoverOnly && el('read').checked,
      notify: operation === 'inspect' ? selected : [], notify_expected: expected,
    };
    const result = await api('/sessions', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(options)});
    viewing = result.id; after = 0; recent = []; deviceRows.clear(); el('gatt').replaceChildren();
    resetCapture(); captureSummary(options); updateSelection();
    el('status').textContent = 'Starting…'; await refresh();
  }
  for (const [id, callback] of Object.entries({
    scan: () => start('scan'), inspect: () => start('inspect', true), start: () => start('inspect'),
    stop: async () => { if (active) await api(`/sessions/${active}/stop`, {method: 'POST'}); await poll(); await refresh(); },
    refresh,
    'select-all': () => { subscriptionInputs().forEach(c => { c.checked = true; }); updateSelection(); },
    clear: () => { subscriptionInputs().forEach(c => { c.checked = false; }); updateSelection(); },
  })) el(id).onclick = () => Promise.resolve(callback()).catch(showError);
  for (const id of ['address', 'name']) el(id).addEventListener('input', () => { el('gatt').replaceChildren(); updateSelection(); });
  el('duration').addEventListener('input', updateSelection);
  updateSelection();
  let visible = false;
  setInterval(() => {
    if (document.getElementById('tab-ble').hidden) { visible = false; return; }
    const work = visible ? poll() : refresh().then(poll);
    visible = true; work.catch(showError);
  }, 1000);
})();
