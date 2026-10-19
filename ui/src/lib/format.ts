export function fmt(n: number | null | undefined, digits = 0): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return Number(n).toFixed(digits);
}

export function fmtKwh(wh: number | null | undefined): string {
  if (wh === null || wh === undefined || Number.isNaN(wh)) return '—';
  const k = wh / 1000;
  if (k >= 100) return k.toFixed(0);
  if (k >= 10) return k.toFixed(1);
  return k.toFixed(2);
}

export function fmtSecsMMSS(secs: number): string {
  if (!isFinite(secs) || secs <= 0) return '0:00';
  const m = Math.floor(secs / 60);
  const s = Math.floor(secs % 60);
  return m + ':' + (s < 10 ? '0' + s : String(s));
}

export function fmtBytes(n: number): string {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + ' MB';
  return (n / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

export function formatDeviceErrorCode(code: unknown): string {
  if (code == null || code === 0 || code === '0') return '—';
  return String(code);
}

export function formatTemp(celsius: number, unit: 'C' | 'F'): string {
  if (unit === 'F') {
    return `${Math.round((celsius * 9) / 5 + 32)} °F`;
  }
  return `${Math.round(celsius)} °C`;
}

export function wattsLabel(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—';
  return `${Math.round(value).toLocaleString()} W`;
}
