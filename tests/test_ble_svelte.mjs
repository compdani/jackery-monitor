/**
 * Port of the BLE UI selection / text-safety checks against the Svelte-era
 * pure helpers in ui/src/lib/ble.ts. Legacy web/ble.js is still covered by
 * test_ble_ui.cjs for the /legacy rollback shell.
 */
import assert from 'node:assert/strict';
import {
  selectionSummary,
  captureSummaryText,
  channelStatusLine,
  listeningEndedSummary,
  deviceButtonLabel,
} from '../ui/src/lib/ble.ts';

// XSS: device names are returned as plain strings for text rendering.
const label = deviceButtonLabel('<img src=x>', 'AA:BB:CC:DD:EE:FF', -35);
assert.ok(label.includes('<img src=x>'));
assert.equal(typeof label, 'string');

assert.equal(
  selectionSummary([], 30),
  'No subscriptions selected. Start will only discover structure and optionally read.',
);
assert.ok(
  selectionSummary(
    [
      { value: 'notify', handle: '2' },
      { value: 'indicate', handle: '5' },
    ],
    30,
  ).includes('indicate (handle 5)'),
);

const cap = captureSummaryText(
  'AA:BB',
  undefined,
  ['2', '5'],
  { 2: 'notify', 5: 'indicate' },
  30,
);
assert.ok(cap.includes('Requested: notify (handle 2)'));
assert.ok(cap.includes('indicate (handle 5)'));

const failed = channelStatusLine(
  {
    uuid: 'indicate',
    handle: 5,
    status: 'failed',
    subscription_properties: ['indicate'],
    event_count: 0,
    error: '<denied>',
  },
  false,
);
assert.ok(failed.includes('failed') && failed.includes('<denied>'));
// Helper returns text only — callers must never HTML-escape into innerHTML.
assert.equal(failed.includes('<span'), false);

const ended = listeningEndedSummary({
  subscribed_count: 1,
  failed_count: 1,
  event_count: 0,
  elapsed_s: 0.25,
  reason: 'cancelled',
});
assert.ok(ended.includes('1 subscribed · 1 failed · 0 events'));
assert.ok(ended.includes('0.25 seconds'));
assert.ok(ended.includes('stopped by user'));
assert.ok(ended.includes('received zero events'));

const complete = listeningEndedSummary({
  subscribed_count: 1,
  failed_count: 0,
  event_count: 0,
  elapsed_s: 30,
  reason: 'duration_elapsed',
});
assert.ok(complete.includes('1 subscribed · 0 failed · 0 events'));
assert.ok(complete.includes('duration elapsed'));

console.log(
  'BLE Svelte helpers: selection, text-only labels, counts, zero-event summaries passed',
);
