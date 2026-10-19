/**
 * Pure BLE inspector helpers — shared by BleTab.svelte and tests.
 * Device-supplied strings must only ever be rendered as text, never HTML.
 */

export type BleChannel = {
  uuid?: string;
  handle: string | number;
  status?: string;
  subscription_properties?: string[];
  event_count?: number;
  error?: string;
  unsubscribe_status?: string;
  unsubscribe_error?: string;
};

export function selectionSummary(
  selected: { value: string; handle: string }[],
  durationSec: number,
): string {
  if (!selected.length) {
    return 'No subscriptions selected. Start will only discover structure and optionally read.';
  }
  const labels = selected.map((c) => `${c.value} (handle ${c.handle})`);
  return `Selected: ${labels.join(', ')} · listen for up to ${durationSec} seconds.`;
}

export function captureSummaryText(
  address: string | undefined,
  name: string | undefined,
  notify: string[],
  notifyExpected: Record<string, string> | undefined,
  durationSec: number,
): string {
  const selected = notify.map((key) =>
    notifyExpected?.[key] ? `${notifyExpected[key]} (handle ${key})` : key,
  );
  const target = address || name || 'scan filters';
  return (
    `Capture target: ${target} · ` +
    (selected.length
      ? `Requested: ${selected.join(', ')} · listening duration: ${durationSec} seconds.`
      : 'No subscriptions requested.')
  );
}

export function channelStatusLine(
  channel: BleChannel,
  listeningEnded: boolean,
): string {
  const status =
    channel.status === 'active'
      ? listeningEnded
        ? 'Subscribed (listening ended)'
        : 'Subscribed'
      : channel.status || 'unknown';
  const props = (channel.subscription_properties || []).join(' / ');
  let line =
    `${channel.uuid || '?'} · handle ${channel.handle} · supports ${props} · ${status} · ` +
    `${channel.event_count || 0} events` +
    (channel.error ? ': ' + channel.error : '');
  if (channel.unsubscribe_status) {
    line +=
      ` · unsubscribe: ${channel.unsubscribe_status}` +
      (channel.unsubscribe_error ? ': ' + channel.unsubscribe_error : '');
  }
  return line;
}

export function listeningEndedSummary(ev: {
  subscribed_count?: number;
  failed_count?: number;
  event_count?: number;
  elapsed_s?: number;
  reason?: string;
}): string {
  const parts = [
    `${ev.subscribed_count ?? 0} subscribed`,
    `${ev.failed_count ?? 0} failed`,
    `${ev.event_count ?? 0} events`,
  ];
  let s = parts.join(' · ');
  if (ev.elapsed_s != null) s += ` · ${ev.elapsed_s} seconds`;
  if (ev.reason === 'cancelled') s += ' · stopped by user';
  else if (ev.reason === 'duration_elapsed') s += ' · duration elapsed';
  else if (ev.reason === 'no_subscriptions_selected') s += ' · no subscriptions selected';
  if ((ev.event_count ?? 0) === 0 && (ev.subscribed_count ?? 0) > 0) {
    s += ' · received zero events';
  }
  return s;
}

/** Device name for list buttons — always plain text, never HTML. */
export function deviceButtonLabel(
  name: string | undefined,
  address: string,
  rssi: number | undefined,
): string {
  return `${name || '(unnamed)'} · ${address} · ${rssi ?? '?'} dBm`;
}
