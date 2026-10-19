import type { SiseliControl, StatusSnapshot } from './types';

type DeviceLike = { source?: string; device_id?: string; device_sn?: string } | null | undefined;

export function isSiseliDevice(d: DeviceLike): boolean {
  if (!d) return false;
  return d.source === 'siseli' || String(d.device_id || d.device_sn || '').startsWith('siseli:');
}

export function isEcoflowDevice(d: DeviceLike): boolean {
  if (!d) return false;
  return d.source === 'ecoflow' || String(d.device_id || d.device_sn || '').startsWith('ecoflow:');
}

function activeCloudDevice(status: StatusSnapshot | null | undefined) {
  const cloud = status?.cloud;
  if (!cloud) return null;
  return (
    (cloud.devices || []).find((d) => String(d.device_id) === String(cloud.selected_device_id)) ||
    null
  );
}

/** Mirrors vanilla `viewingSiseli`: selected cloud device, else `status.device`. */
export function viewingSiseli(status: StatusSnapshot | null | undefined): boolean {
  if (isSiseliDevice(activeCloudDevice(status))) return true;
  return isSiseliDevice(status?.device);
}

export function viewingEcoflow(status: StatusSnapshot | null | undefined): boolean {
  if (isEcoflowDevice(activeCloudDevice(status))) return true;
  return isEcoflowDevice(status?.device);
}

/** Controls pinned to Live via `device_prefs.live_controls` (canonical keys). */
export function siseliPinnedControls(
  controls: SiseliControl[] | null | undefined,
  pins: string[] | null | undefined,
): SiseliControl[] {
  const set = new Set(pins || []);
  return (Array.isArray(controls) ? controls : []).filter((c) => set.has(c.canonical));
}
