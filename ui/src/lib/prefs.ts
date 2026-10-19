/** Browser prefs — key names must match the legacy app for cutover continuity. */

export const KEYS = {
  energyBucketS: 'energyBucketS',
  energyChartStyle: 'energyChartStyle',
  settingsSubtab: 'jackery-settings-subtab',
  automationSubtab: 'jackery-automation-subtab',
  keepAwake: 'jackery-keep-awake',
  tempUnit: 'jackery-temp-unit',
  heroOrder: 'jackery-hero-order',
  locationDenied: 'jackery-location-denied',
  ecoflowCollapse: 'jackery-linked-ecoflow-collapsed',
  packsCollapse: 'jackery-battery-packs-collapsed',
  forecastArrayCollapse: 'jackery-forecast-array-collapsed',
  forecastLoadCollapse: 'jackery-forecast-load-collapsed',
  assistantOpen: 'jackery-assistant-open',
  assistantAutopilot: 'jackery-assistant-autopilot',
  assistantToolPrefs: 'jackery-assistant-tool-prefs',
} as const;

export function settingsCollapseKey(cardKey: string) {
  return `jackery-settings-collapse:${cardKey}`;
}

export function collapseKey(key: string) {
  return `jackery-collapse-${key}`;
}

export function getString(key: string, fallback = ''): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

export function setString(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* quota / private mode */
  }
}

export function removeKey(key: string) {
  try {
    localStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

export function getJson<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    if (raw == null) return fallback;
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

export function setJson(key: string, value: unknown) {
  setString(key, JSON.stringify(value));
}

const ENERGY_BUCKETS = new Set([900, 1800, 3600]);

export function readEnergyBucketS(): number {
  try {
    const v = parseInt(localStorage.getItem(KEYS.energyBucketS) || '', 10);
    if (ENERGY_BUCKETS.has(v)) return v;
  } catch {
    /* ignore */
  }
  return 900;
}

export function writeEnergyBucketS(v: number) {
  if (ENERGY_BUCKETS.has(v)) setString(KEYS.energyBucketS, String(v));
}

export type EnergyChartStyle = 'bar' | 'line';

export function readEnergyChartStyle(): EnergyChartStyle {
  return getString(KEYS.energyChartStyle, 'bar') === 'line' ? 'line' : 'bar';
}

export function writeEnergyChartStyle(style: EnergyChartStyle) {
  setString(KEYS.energyChartStyle, style === 'line' ? 'line' : 'bar');
}

export type TempUnit = 'C' | 'F';

export function readTempUnit(): TempUnit {
  return getString(KEYS.tempUnit, 'C') === 'F' ? 'F' : 'C';
}

export function writeTempUnit(unit: TempUnit) {
  setString(KEYS.tempUnit, unit);
}

export function isKeepAwakeOn(): boolean {
  return getString(KEYS.keepAwake) === '1';
}

export function setKeepAwake(on: boolean) {
  setString(KEYS.keepAwake, on ? '1' : '0');
}

/** Reorder hero-grid children to match KEYS.heroOrder (legacy jackery-hero-order). */
export function applyHeroOrder(grid: HTMLElement | null | undefined) {
  if (!grid) return;
  const saved = getJson<string[]>(KEYS.heroOrder, []);
  if (!Array.isArray(saved) || !saved.length) return;
  for (const id of saved) {
    const el = grid.querySelector(`[data-card="${id.replace(/"/g, '')}"]`);
    if (el) grid.appendChild(el);
  }
}

export function writeHeroOrder(ids: string[]) {
  setJson(KEYS.heroOrder, ids);
}
