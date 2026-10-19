<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost, ApiError } from '$lib/api';
  import {
    selectionSummary as formatSelectionSummary,
    captureSummaryText,
    channelStatusLine,
    deviceButtonLabel,
  } from '$lib/ble';

  type BleCapture = {
    id: string;
    bytes?: number;
    active?: boolean;
    status?: string;
    error?: string | null;
  };

  type BleEvent = {
    type?: string;
    seq?: number;
    address?: string;
    name?: string;
    rssi?: number;
    uuid?: string;
    handle?: number | string;
    characteristics?: {
      uuid: string;
      handle: number | string;
      properties: string[];
    }[];
    options?: Record<string, unknown>;
    status?: string;
    error?: string;
    channels?: unknown[];
    duration_s?: number;
    subscribed_count?: number;
    failed_count?: number;
    event_count?: number;
    elapsed_s?: number;
    reason?: string;
    subscription_properties?: string[];
    unsubscribe_status?: string;
    unsubscribe_error?: string;
    [key: string]: unknown;
  };

  type GattChar = {
    uuid: string;
    handle: string;
    properties: string[];
    subscribable: boolean;
    selected: boolean;
  };

  type GattService = {
    uuid: string;
    handle: string;
    chars: GattChar[];
  };

  type DeviceRow = {
    address: string;
    name?: string;
    rssi?: number;
    raw: BleEvent;
  };

  type ChannelStatus = {
    uuid?: string;
    handle: string;
    status?: string;
    subscription_properties?: string[];
    event_count?: number;
    error?: string;
    unsubscribe_status?: string;
    unsubscribe_error?: string;
  };

  let nameFilter = $state('');
  let addressFilter = $state('');
  let seconds = $state(10);
  let duration = $state(30);
  let readAll = $state(false);
  let statusText = $state('');
  let captureSummary = $state('');
  let listeningResult = $state('');
  let activeId = $state<string | null>(null);
  let viewingId = $state<string | null>(null);
  let after = $state(0);
  let captures = $state.raw<BleCapture[]>([]);
  let available = $state(true);
  let adapterError = $state('');
  let devices = $state.raw<DeviceRow[]>([]);
  let gatt = $state.raw<GattService[]>([]);
  let recent = $state.raw<BleEvent[]>([]);
  let channels = $state.raw<Record<string, ChannelStatus>>({});
  let pollBusy = $state(false);
  let actionBusy = $state(false);

  const isActive = $derived(!!activeId);

  const eventsText = $derived(recent.map((e) => JSON.stringify(e)).join('\n'));

  const selectionSummary = $derived.by(() => {
    const checked = gatt.flatMap((s) => s.chars.filter((c) => c.subscribable && c.selected));
    return formatSelectionSummary(
      checked.map((c) => ({ value: c.uuid, handle: c.handle })),
      duration,
    );
  });

  function resetCaptureUi() {
    channels = {};
    listeningResult = '';
    captureSummary = '';
  }

  function captureSummaryFrom(options: Record<string, unknown>) {
    captureSummary = captureSummaryText(
      options.address as string | undefined,
      options.name as string | undefined,
      (options.notify as string[]) || [],
      (options.notify_expected as Record<string, string>) || undefined,
      Number(options.duration ?? 30),
    );
  }

  function processEvent(event: BleEvent) {
    recent = [...recent, event].slice(-200);

    if (event.type === 'advertisement' && event.address) {
      const byAddr: Record<string, DeviceRow> = Object.fromEntries(
        devices.map((d) => [d.address, d]),
      );
      byAddr[event.address] = {
        address: event.address,
        name: event.name,
        rssi: event.rssi,
        raw: event,
      };
      let list = Object.values(byAddr);
      if (list.length > 200) list = list.slice(-200);
      devices = list;
    }

    if (event.type === 'metadata') {
      captureSummaryFrom((event.options as Record<string, unknown>) || {});
    }

    if (event.type === 'service' && event.uuid != null) {
      const chars: GattChar[] = (event.characteristics || []).map((c) => ({
        uuid: c.uuid,
        handle: String(c.handle),
        properties: c.properties || [],
        subscribable: (c.properties || []).some((p) => p === 'notify' || p === 'indicate'),
        selected: false,
      }));
      gatt = [
        ...gatt,
        { uuid: String(event.uuid), handle: String(event.handle ?? ''), chars },
      ];
    }

    if (
      event.type &&
      ['subscription_requested', 'subscription_active', 'subscription_error', 'unsubscribe_result'].includes(
        event.type,
      )
    ) {
      const handle = String(event.handle ?? '');
      const status =
        event.type === 'subscription_active'
          ? 'active'
          : event.type === 'subscription_error'
            ? 'error'
            : event.type === 'unsubscribe_result'
              ? 'unsubscribed'
              : 'requested';
      channels = {
        ...channels,
        [handle]: {
          uuid: event.uuid != null ? String(event.uuid) : undefined,
          handle,
          status,
          subscription_properties: event.subscription_properties,
          event_count: Number(event.event_count || 0),
          error: event.error,
          unsubscribe_status: event.unsubscribe_status,
          unsubscribe_error: event.unsubscribe_error,
        },
      };
    }

    if (event.type === 'notification') {
      const handle = String(event.handle ?? '');
      const ch = channels[handle];
      if (ch) {
        channels = {
          ...channels,
          [handle]: { ...ch, event_count: (ch.event_count || 0) + 1 },
        };
      }
    }

    if (event.type === 'listening_started') {
      const n = Array.isArray(event.channels) ? event.channels.length : 0;
      listeningResult = `Listening on ${n} subscribed channel(s) for up to ${event.duration_s} seconds…`;
    }

    if (event.type === 'listening_ended') {
      const reasons: Record<string, string> = {
        duration_elapsed: 'duration elapsed',
        cancelled: 'stopped by user or server shutdown',
        disconnected: 'device disconnected',
        storage_failure: 'capture storage failure',
        all_subscriptions_failed: 'all subscriptions failed',
        no_subscriptions_selected: 'no subscriptions selected',
        error: 'operation failed',
      };
      listeningResult =
        `${event.subscribed_count ?? 0} subscribed · ${event.failed_count ?? 0} failed · ` +
        `${event.event_count ?? 0} events · listened for ${Number(event.elapsed_s || 0).toFixed(2)} seconds · ` +
        `${reasons[String(event.reason)] || event.reason}.` +
        ((event.subscribed_count ?? 0) > 0 && (event.event_count ?? 0) === 0
          ? ' Confirmed subscriptions received zero events; no controller data was received.'
          : '');
      if (Array.isArray(event.channels)) {
        const next = { ...channels };
        for (const ch of event.channels as ChannelStatus[]) {
          next[String(ch.handle)] = { ...ch, handle: String(ch.handle) };
        }
        channels = next;
      }
    }

    if (event.type === 'completion') {
      statusText = `${event.status || 'done'}${event.error ? ': ' + event.error : ''}`;
    }
  }

  async function refresh() {
    try {
      const state = await api<{
        active?: string | null;
        captures?: BleCapture[];
        available?: boolean;
        error?: string;
      }>('/ble/status');
      activeId = state.active ?? null;
      captures = state.captures || [];
      available = state.available !== false;
      adapterError = state.error || '';
      if (!available && !activeId && adapterError) {
        statusText = adapterError;
      }
      if (activeId && !viewingId) {
        viewingId = activeId;
        after = 0;
      }
    } catch (err) {
      statusText = err instanceof Error ? err.message : 'Status failed';
    }
  }

  async function poll() {
    if (!viewingId || pollBusy) return;
    pollBusy = true;
    try {
      const result = await api<{
        events?: BleEvent[];
        active?: boolean;
        next?: number;
        bytes?: number;
      }>(`/ble/captures/${viewingId}/events?after=${after}&limit=200`);
      for (const ev of result.events || []) processEvent(ev);
      after = result.next ?? after;
      if (result.active) {
        statusText = `Capturing · ${(result.bytes || 0).toLocaleString()} bytes`;
      } else {
        await refresh();
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        viewingId = null;
        await refresh();
      } else {
        statusText = err instanceof Error ? err.message : 'Poll failed';
      }
    } finally {
      pollBusy = false;
    }
  }

  async function start(operation: 'scan' | 'inspect', discoverOnly = false) {
    actionBusy = true;
    try {
      const checked = discoverOnly || operation === 'scan'
        ? []
        : gatt.flatMap((s) => s.chars.filter((c) => c.subscribable && c.selected));
      const selected = checked.map((c) => c.handle);
      const expected = Object.fromEntries(checked.map((c) => [c.handle, c.uuid]));
      const address = addressFilter.trim() || null;
      const name = nameFilter.trim() || null;
      if (operation === 'inspect' && !address && !name) {
        throw new Error('Select a device or enter a name/address.');
      }
      const options = {
        operation,
        address,
        name: operation === 'inspect' && address ? null : name,
        seconds: Number(seconds),
        duration: Number(duration),
        read_all: operation === 'inspect' && !discoverOnly && readAll,
        notify: operation === 'inspect' ? selected : [],
        notify_expected: expected,
      };
      const result = await apiPost<{ id: string }>('/ble/sessions', options);
      viewingId = result.id;
      after = 0;
      recent = [];
      devices = [];
      gatt = [];
      resetCaptureUi();
      captureSummaryFrom(options);
      statusText = 'Starting…';
      await refresh();
    } catch (err) {
      statusText = err instanceof Error ? err.message : 'Start failed';
    } finally {
      actionBusy = false;
    }
  }

  async function stopCapture() {
    if (!activeId) return;
    actionBusy = true;
    try {
      await apiPost(`/ble/sessions/${activeId}/stop`);
      await poll();
      await refresh();
    } catch (err) {
      statusText = err instanceof Error ? err.message : 'Stop failed';
    } finally {
      actionBusy = false;
    }
  }

  async function viewCapture(id: string) {
    viewingId = id;
    after = 0;
    recent = [];
    devices = [];
    gatt = [];
    resetCaptureUi();
    await poll();
  }

  async function deleteCapture(id: string) {
    actionBusy = true;
    try {
      await api(`/ble/captures/${id}`, { method: 'DELETE' });
      if (viewingId === id) viewingId = null;
      await refresh();
    } catch (err) {
      statusText = err instanceof Error ? err.message : 'Delete failed';
    } finally {
      actionBusy = false;
    }
  }

  function selectDevice(d: DeviceRow) {
    addressFilter = d.address;
    nameFilter = '';
    gatt = [];
  }

  function selectAllSubs() {
    gatt = gatt.map((s) => ({
      ...s,
      chars: s.chars.map((c) => (c.subscribable ? { ...c, selected: true } : c)),
    }));
  }

  function clearSubs() {
    gatt = gatt.map((s) => ({
      ...s,
      chars: s.chars.map((c) => (c.subscribable ? { ...c, selected: false } : c)),
    }));
  }

  function toggleChar(serviceIdx: number, charIdx: number, checked: boolean) {
    gatt = gatt.map((s, si) => {
      if (si !== serviceIdx) return s;
      return {
        ...s,
        chars: s.chars.map((c, ci) => (ci === charIdx ? { ...c, selected: checked } : c)),
      };
    });
  }

  function channelLine(ch: ChannelStatus): string {
    return channelStatusLine(ch, !!listeningResult);
  }

  onMount(() => {
    let visible = false;
    const tick = setInterval(() => {
      // Only poll while this tab's panel is in the document (parent may hide it).
      const work = visible ? poll() : refresh().then(poll);
      visible = true;
      work.catch((err) => {
        statusText = err instanceof Error ? err.message : String(err);
      });
    }, 1000);
    refresh().then(poll).catch(() => {});
    return () => clearInterval(tick);
  });
</script>

<section class="tab-panel">
  <section class="card">
    <h2>BLE Inspector</h2>
    <p>
      Inspect advertisements and GATT using the server’s Bluetooth adapter. No controller commands
      or characteristic value writes. Connecting may prevent the vendor app from connecting.
    </p>
    <p class="hint">
      This is not a passive radio packet sniffer. Text interpretations are guesses; hex preserves
      received bytes. Notification subscriptions explicitly let BlueZ configure the subscription
      descriptor.
    </p>

    <div class="form-row">
      <label>Name filter <input bind:value={nameFilter} placeholder="Sileli (optional)" disabled={isActive} /></label>
      <label>
        Address filter / target
        <input bind:value={addressFilter} placeholder="AA:BB:CC:DD:EE:FF" disabled={isActive} />
      </label>
      <label>
        Scan seconds
        <input type="number" min="1" max="120" bind:value={seconds} disabled={isActive} />
      </label>
      <button class="btn" type="button" disabled={isActive || actionBusy} onclick={() => start('scan')}>
        Scan devices
      </button>
      <button
        class="btn"
        type="button"
        disabled={isActive || actionBusy}
        onclick={() => start('inspect', true)}
      >
        Inspect GATT
      </button>
    </div>

    <div>
      {#each devices as d (d.address)}
        <div>
          <button
            type="button"
            class="btn btn-ghost"
            onclick={() => selectDevice(d)}
          >
            {deviceButtonLabel(d.name, d.address, d.rssi)}
          </button>
          <pre style="white-space:pre-wrap">{JSON.stringify(d.raw, null, 2)}</pre>
        </div>
      {/each}
    </div>

    <div class="form-row">
      <label>
        <input type="checkbox" bind:checked={readAll} disabled={isActive} />
        Read readable characteristics (independent of subscriptions)
      </label>
    </div>

    <h3>Event subscriptions</h3>
    <p class="hint">
      Inspect GATT first, then explicitly select notify/indicate channels. Subscribing configures
      the BLE subscription descriptor and receives events only on the Pi’s connection. No controller
      commands are sent.
    </p>
    <div class="form-row">
      <button class="btn" type="button" disabled={isActive} onclick={selectAllSubs}>
        Select all subscribable
      </button>
      <button class="btn" type="button" disabled={isActive} onclick={clearSubs}>Clear selections</button>
      <label>
        Listening seconds
        <input type="number" min="1" max="3600" bind:value={duration} disabled={isActive} />
      </label>
    </div>
    <p class="hint" aria-live="polite" hidden={isActive}>{selectionSummary}</p>

    <div>
      {#each gatt as svc, si (svc.uuid + '-' + svc.handle)}
        <h4>Service {svc.uuid} (handle {svc.handle})</h4>
        {#each svc.chars as ch, ci (ch.handle)}
          <label style="display:block">
            {#if ch.subscribable}
              <input
                type="checkbox"
                checked={ch.selected}
                disabled={isActive}
                onchange={(e) => toggleChar(si, ci, (e.currentTarget as HTMLInputElement).checked)}
              />
              Subscribe to events:
            {/if}
            {ch.uuid} · handle {ch.handle} · {ch.properties.join(', ')}
          </label>
        {/each}
      {/each}
    </div>

    <div class="form-row">
      <button
        class="btn btn-primary"
        type="button"
        disabled={isActive || actionBusy}
        onclick={() => start('inspect')}
      >
        Start selected capture
      </button>
      <button class="btn" type="button" disabled={!isActive || actionBusy} onclick={stopCapture}>
        Stop
      </button>
    </div>

    <p role="status">{statusText}</p>
    <p class="hint">{captureSummary}</p>
    <div aria-live="polite">
      {#each Object.values(channels) as ch (ch.handle)}
        <p>{channelLine(ch)}</p>
      {/each}
    </div>
    <p role="status">{listeningResult}</p>

    <h3>Recent events (latest 200)</h3>
    <pre style="max-height:400px;overflow:auto;white-space:pre-wrap">{eventsText}</pre>

    <h3>Saved captures</h3>
    <button class="btn" type="button" onclick={() => refresh()}>Refresh captures</button>
    <div>
      {#each captures as c (c.id)}
        <p>
          {c.id} · {(c.bytes || 0).toLocaleString()} bytes · {c.status}{c.error ? ': ' + c.error : ''}
          <button class="btn btn-ghost" type="button" onclick={() => viewCapture(c.id)}>View</button>
          {#if !c.active}
            <a href="/api/ble/captures/{c.id}/download?format=json"> JSON </a>
            <a href="/api/ble/captures/{c.id}/download?format=jsonl"> JSONL </a>
            <button class="btn btn-ghost" type="button" onclick={() => deleteCapture(c.id)}>
              Delete
            </button>
          {/if}
        </p>
      {/each}
    </div>
  </section>
</section>
