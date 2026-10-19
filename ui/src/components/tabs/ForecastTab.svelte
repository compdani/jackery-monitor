<script lang="ts">
  import { onMount } from 'svelte';
  import { api, apiPost } from '$lib/api';
  import { live } from '$lib/stores/live.svelte';
  import { fmt } from '$lib/format';
  import { drawForecastChart } from '$lib/charts/canvas';
  import { KEYS, getString, setString } from '$lib/prefs';

  type GeoHit = {
    name?: string;
    admin1?: string;
    country?: string;
    latitude: number;
    longitude: number;
    label?: string;
  };

  type ForecastPoint = {
    ts?: number;
    soc_pct?: number;
    predicted_soc?: number;
    load_w?: number;
    solar_w?: number;
  };

  type ForecastPayload = {
    ready?: boolean;
    reason?: string;
    configured?: boolean;
    forecast?: ForecastPoint[];
    fit?: { r2?: number; samples?: number };
    summary?: Record<string, unknown>;
    starting_soc_pct?: number;
    charge_efficiency?: number;
    capacity_wh?: number;
    low_battery_threshold?: number;
    today_actual_solar_wh?: number;
    solar_wh?: number;
    load_wh?: number;
    start_soc?: number;
    end_soc?: number;
    solar_coefficient?: number;
    fit_samples?: number;
    overall_load_w?: number;
    parasitic_w?: number;
    pack_baseline_w?: number;
    charge_ceiling_pct?: number | null;
    load_mode?: 'historical' | 'scheduled' | string;
  };

  type DayTile = {
    key: string;
    label: string;
    startSoc: number;
    endSoc: number;
    minSoc: number;
    maxSoc: number;
    solarKwh: number;
    depleted: boolean;
    showLow: boolean;
    cls: string;
  };

  type LoadWindow = {
    id: string;
    label: string;
    start: string;
    end: string;
    watts: number;
    kind: 'once' | 'weekly';
    date: string | null;
    weekdays: number[] | null;
  };

  const WEEKDAY_LABELS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const;

  type LearnedSample = {
    hour?: number;
    weekend?: boolean;
    watts?: number;
  };

  type LearnedHourRow = {
    hour: number;
    weekday: string | number;
    weekend: string | number;
  };

  type AccuracySample = {
    target?: number;
    lead_time_h?: number;
    predicted_soc?: number;
    actual_soc?: number | null;
    made_at?: number;
  };

  let forecast = $state.raw<ForecastPayload | null>(null);
  let loc = $state.raw<{
    latitude?: number;
    longitude?: number;
    label?: string;
  } | null>(null);
  let arrayCfg = $state({ declination: 30, azimuth: 0, kwp: 1 });
  let hasSolarKey = $state(false);
  let solarKey = $state('');
  let arrayHint = $state('');
  // Default collapsed unless the user explicitly expanded (same as Live packs).
  let arrayCollapsed = $state(getString(KEYS.forecastArrayCollapse) !== '0');
  let loadCollapsed = $state(getString(KEYS.forecastLoadCollapse) !== '0');
  let geoQ = $state('');
  let geoHits = $state.raw<GeoHit[]>([]);
  let lat = $state('');
  let lon = $state('');
  let msg = $state('');
  let busy = $state(false);
  let showManual = $state(false);
  let coeffMsg = $state('');

  let loadMode = $state<'historical' | 'scheduled'>('scheduled');
  let loadWindows = $state<LoadWindow[]>([]);
  let learnedRows = $state.raw<LearnedHourRow[]>([]);
  let sleepStart = $state('00:00');
  let sleepEnd = $state('06:00');
  let scheduleMsg = $state('');
  let calFocusDate = $state(localDateStr(new Date()));
  let draftKind = $state<'once' | 'weekly'>('once');
  let draftLabel = $state('');
  let draftStart = $state('18:00');
  let draftEnd = $state('22:00');
  let draftWatts = $state(800);
  let draftWeekdays = $state<number[]>([0, 1, 2, 3, 4]);

  let accuracySamples = $state.raw<AccuracySample[]>([]);
  let accuracyMsg = $state('');

  const needsConfig = $derived(!loc?.latitude || !loc?.longitude);

  const coeffDisplay = $derived(
    forecast?.solar_coefficient != null && Number.isFinite(forecast.solar_coefficient)
      ? fmt(forecast.solar_coefficient, 2)
      : '—',
  );
  const coeffSub = $derived.by(() => {
    const j = forecast;
    if (!j) return '—';
    const minFit = 2;
    const samples = j.fit_samples ?? 0;
    if (j.solar_coefficient === 0) return 'no solar production detected on this device';
    if (samples >= minFit) return `learned from ${samples} hourly samples`;
    const need = Math.max(1, minFit - samples);
    return `default — need ${need} more daylight hours of data to fit`;
  });
  const loadSub = $derived.by(() => {
    const j = forecast;
    if (!j) return '—';
    if (j.parasitic_w != null) {
      const idle = Math.round(Number(j.parasitic_w));
      const packs = Number(j.pack_baseline_w || 0);
      return packs
        ? `idle ${idle} W when inverter on · +${Math.round(packs)} W packs`
        : `idle ${idle} W when inverter on`;
    }
    return 'idle 0 W when inverter off';
  });

  const arraySummary = $derived.by(() => {
    const dec = arrayCfg.declination;
    const az = arrayCfg.azimuth;
    const kwp = arrayCfg.kwp;
    if (![dec, az, kwp].every((n) => n != null && Number.isFinite(Number(n)))) {
      return 'tilt / azimuth / kWp — click to edit';
    }
    const kwpStr = String(Number(Number(kwp).toFixed(2)));
    return `tilt ${dec}° · az ${az}° · ${kwpStr} kWp`;
  });

  const loadSummary = $derived.by(() => {
    const mode = loadMode === 'scheduled' ? 'Calendar' : 'Historical';
    const sleep = sleepStart && sleepEnd ? ` · sleep ${sleepStart}–${sleepEnd}` : '';
    const n = loadWindows.length;
    const win = n ? ` · ${n} event${n === 1 ? '' : 's'}` : '';
    return mode + win + sleep;
  });

  const calWeekDays = $derived.by(() => weekStrip(calFocusDate));
  const dayEvents = $derived.by(() => eventsForDate(loadWindows, calFocusDate));

  const dayTiles = $derived.by((): DayTile[] => {
    const fc = forecast?.forecast || [];
    if (!fc.length || forecast?.ready === false) return [];
    const days: {
      key: string;
      date: Date;
      solarWh: number;
      minSoc: number;
      maxSoc: number;
      startSoc: number;
      endSoc: number;
      depleted: boolean;
    }[] = [];
    let currentKey: string | null = null;
    let current: (typeof days)[0] | null = null;
    // Day 0 starts at current SOC; later days inherit the prior day's end.
    let prevEnd = Number(forecast?.starting_soc_pct ?? forecast?.start_soc ?? 0);
    for (const h of fc) {
      if (h.ts == null) continue;
      const d = new Date(h.ts * 1000);
      const key = d.toDateString();
      if (key !== currentKey) {
        if (current) {
          days.push(current);
          prevEnd = current.endSoc;
        }
        currentKey = key;
        current = {
          key,
          date: d,
          solarWh: 0,
          minSoc: Infinity,
          maxSoc: -Infinity,
          startSoc: prevEnd,
          endSoc: prevEnd,
          depleted: false,
        };
      }
      if (!current) continue;
      current.solarWh += Number(h.solar_w ?? 0);
      const s = Number(h.predicted_soc ?? h.soc_pct);
      if (Number.isFinite(s)) {
        if (s < current.minSoc) current.minSoc = s;
        if (s > current.maxSoc) current.maxSoc = s;
        current.endSoc = s;
        if (s <= 0.5) current.depleted = true;
      }
    }
    if (current) days.push(current);
    if (days.length > 0) {
      const todayActualWh = Number(forecast?.today_actual_solar_wh ?? 0);
      if (todayActualWh > 0) days[0].solarWh += todayActualWh;
    }
    const now = new Date();
    const todayMs = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
    return days.slice(0, 7).map((d) => {
      const dayMs = Date.UTC(d.date.getFullYear(), d.date.getMonth(), d.date.getDate());
      const diffDays = Math.round((dayMs - todayMs) / 86400000);
      let label = d.date.toLocaleDateString([], {
        weekday: 'short',
        month: 'numeric',
        day: 'numeric',
      });
      if (diffDays === 0) label = 'Today';
      else if (diffDays === 1) label = 'Tomorrow';
      const minSoc = d.minSoc === Infinity ? NaN : d.minSoc;
      const maxSoc = d.maxSoc === -Infinity ? NaN : d.maxSoc;
      let cls = '';
      if (d.depleted) cls = 'tile-bad';
      else if (Number.isFinite(minSoc) && minSoc < 20) cls = 'tile-warn';
      else if (Number.isFinite(minSoc)) cls = 'tile-good';
      // Surface intraday trough when it dips well below the day-end SOC.
      const showLow =
        Number.isFinite(minSoc) && Number.isFinite(d.endSoc) && minSoc < d.endSoc - 5;
      return {
        key: d.key,
        label,
        startSoc: d.startSoc,
        endSoc: d.endSoc,
        minSoc,
        maxSoc,
        solarKwh: d.solarWh / 1000,
        depleted: d.depleted,
        showLow,
        cls,
      };
    });
  });

  const budget = $derived.by(() => {
    const j = forecast;
    const fc = j?.forecast || [];
    if (!fc.length || !j?.ready) return null;

    // Prefer explicit payload narrative fields when present.
    if (
      j.solar_wh != null ||
      j.load_wh != null ||
      j.start_soc != null ||
      j.end_soc != null
    ) {
      return {
        solarKwh: Number(j.solar_wh ?? 0) / 1000,
        loadKwh: Number(j.load_wh ?? 0) / 1000,
        socStart: Number(j.start_soc ?? j.starting_soc_pct ?? NaN),
        socEnd: Number(j.end_soc ?? NaN),
        hours: fc.length,
        peakSolar: Math.max(...fc.map((h) => Number(h.solar_w ?? 0)), 0),
        netKwh: null as number | null,
        lossKwh: null as number | null,
        fromPayload: true,
      };
    }

    let i = 0;
    while (i < fc.length && (fc[i].solar_w || 0) <= 0) i++;
    const dawnIdx = i;
    while (i < fc.length && (fc[i].solar_w || 0) > 0) i++;
    if (i <= dawnIdx) return { afterSunset: true as const };

    const socStart =
      dawnIdx > 0
        ? Number(fc[dawnIdx - 1].predicted_soc ?? fc[dawnIdx - 1].soc_pct)
        : Number(j.starting_soc_pct ?? NaN);
    const window = fc.slice(dawnIdx, i);
    const eff = Number(j.charge_efficiency ?? 0.85);
    let solarWh = 0;
    let loadWh = 0;
    let storedWh = 0;
    let peakSolar = 0;
    for (const h of window) {
      const solar = Number(h.solar_w ?? 0);
      const load = Number(h.load_w ?? 0);
      const net = solar - load;
      solarWh += solar;
      loadWh += load;
      storedWh += net > 0 ? net * eff : net;
      if (solar > peakSolar) peakSolar = solar;
    }
    const socEnd = Number(
      window[window.length - 1]?.predicted_soc ?? window[window.length - 1]?.soc_pct,
    );
    return {
      solarKwh: solarWh / 1000,
      loadKwh: loadWh / 1000,
      socStart,
      socEnd,
      hours: window.length,
      peakSolar,
      netKwh: storedWh / 1000,
      lossKwh: (solarWh - loadWh - storedWh) / 1000,
      fromPayload: false,
    };
  });

  function attachForecastChart(data: ForecastPayload | null) {
    return (_canvas: HTMLCanvasElement) => {
      drawForecastChart(data || { forecast: [] });
    };
  }

  function deviceQ() {
    const sn = live.activeDeviceSn();
    return sn ? `?device_sn=${encodeURIComponent(sn)}` : '';
  }

  function newWindowId() {
    return Math.random().toString(16).slice(2, 10);
  }

  function localDateStr(d: Date): string {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
  }

  function parseLocalDate(s: string): Date {
    const [y, m, d] = s.split('-').map(Number);
    return new Date(y, (m || 1) - 1, d || 1);
  }

  /** JS getDay(): Sun=0 … Sat=6 → Mon=0 … Sun=6 */
  function mondayIndex(d: Date): number {
    return (d.getDay() + 6) % 7;
  }

  function weekStrip(focus: string): { date: string; label: string; dow: string }[] {
    const base = parseLocalDate(focus);
    const monOff = mondayIndex(base);
    // Construct dates without mutating (avoids SvelteDate autofixer nudge).
    const monday = new Date(base.getFullYear(), base.getMonth(), base.getDate() - monOff);
    const out: { date: string; label: string; dow: string }[] = [];
    for (let i = 0; i < 7; i++) {
      const d = new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + i);
      out.push({
        date: localDateStr(d),
        label: String(d.getDate()),
        dow: WEEKDAY_LABELS[i],
      });
    }
    return out;
  }

  function eventsForDate(windows: LoadWindow[], date: string): { w: LoadWindow; i: number }[] {
    const wd = mondayIndex(parseLocalDate(date));
    const rows: { w: LoadWindow; i: number }[] = [];
    windows.forEach((w, i) => {
      if (w.kind === 'once' && w.date === date) rows.push({ w, i });
      else if (w.kind === 'weekly' && (w.weekdays || []).includes(wd)) rows.push({ w, i });
    });
    return rows.sort((a, b) => (a.w.start || '').localeCompare(b.w.start || ''));
  }

  function legacyDaysToWeekdays(days: string | undefined): number[] {
    if (days === 'weekend') return [5, 6];
    if (days === 'weekday') return [0, 1, 2, 3, 4];
    return [0, 1, 2, 3, 4, 5, 6];
  }

  function normalizeWindows(
    windows: (Partial<LoadWindow> & { days?: string })[] | undefined,
  ): LoadWindow[] {
    if (!Array.isArray(windows)) return [];
    return windows.map((w) => {
      let kind: 'once' | 'weekly' = w.kind === 'once' ? 'once' : 'weekly';
      let date = w.date ?? null;
      let weekdays = w.weekdays ?? null;
      if (!w.kind && w.days) {
        kind = 'weekly';
        weekdays = legacyDaysToWeekdays(w.days);
        date = null;
      }
      if (kind === 'weekly' && (!weekdays || !weekdays.length)) {
        weekdays = [0, 1, 2, 3, 4, 5, 6];
      }
      if (kind === 'once' && !date) {
        date = localDateStr(new Date());
      }
      return {
        id: w.id || newWindowId(),
        label: w.label ?? '',
        start: w.start || '18:00',
        end: w.end || '22:00',
        watts: w.watts ?? 0,
        kind,
        date,
        weekdays,
      };
    });
  }

  function toggleDraftWeekday(d: number) {
    if (draftWeekdays.includes(d)) {
      if (draftWeekdays.length <= 1) return;
      draftWeekdays = draftWeekdays.filter((x) => x !== d);
    } else {
      draftWeekdays = [...draftWeekdays, d].sort((a, b) => a - b);
    }
  }

  function shiftCalWeek(delta: number) {
    const d = parseLocalDate(calFocusDate);
    d.setDate(d.getDate() + delta * 7);
    calFocusDate = localDateStr(d);
  }

  function eventBadge(w: LoadWindow): string {
    if (w.kind === 'once') return w.date || 'once';
    const days = (w.weekdays || []).map((i) => WEEKDAY_LABELS[i] || '?').join(' ');
    return days || 'weekly';
  }

  function buildLearnedRows(samples: LearnedSample[] | undefined): LearnedHourRow[] {
    const byHour: Record<number, LearnedHourRow> = {};
    for (const r of samples || []) {
      const h = Number(r.hour);
      if (!Number.isFinite(h)) continue;
      if (!byHour[h]) byHour[h] = { hour: h, weekday: '—', weekend: '—' };
      const watts = Math.round(Number(r.watts));
      if (r.weekend) byHour[h].weekend = watts;
      else byHour[h].weekday = watts;
    }
    return Object.keys(byHour)
      .map(Number)
      .sort((a, b) => a - b)
      .map((h) => byHour[h]);
  }

  async function loadForecast() {
    try {
      forecast = await api<ForecastPayload>(`/forecast${deviceQ()}`);
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Forecast failed';
    }
  }

  async function resetCoefficient() {
    const sn = live.activeDeviceSn();
    if (!sn) {
      coeffMsg = 'No device selected';
      return;
    }
    busy = true;
    coeffMsg = 'Resetting coefficient…';
    try {
      // Clear stored param and re-resolve from history (same as Device tab Refit).
      const j = await apiPost<{ value?: number; source?: string; n_samples?: number }>(
        '/devices/params/refit',
        { device_sn: sn, key: 'solar_coefficient' },
      );
      coeffMsg =
        j.value != null
          ? `Reset · k=${fmt(j.value, 2)} (${j.source || 'fit'}, ${j.n_samples ?? 0} samples)`
          : 'Coefficient cleared — reloading forecast…';
      await loadForecast();
    } catch (err) {
      coeffMsg = err instanceof Error ? err.message : 'Reset failed';
    } finally {
      busy = false;
    }
  }

  async function loadLocation() {
    try {
      loc = await api('/location');
      if (loc?.latitude != null) lat = String(loc.latitude);
      if (loc?.longitude != null) lon = String(loc.longitude);
    } catch {
      loc = null;
    }
  }

  async function loadArray() {
    try {
      const j = await api<{
        array?: { declination?: number; azimuth?: number; kwp?: number };
        has_key?: boolean;
      }>(`/forecast/solar_array${deviceQ()}`);
      const a = j.array || {};
      arrayCfg = {
        declination: a.declination ?? 30,
        azimuth: a.azimuth ?? 0,
        kwp: a.kwp ?? 1,
      };
      hasSolarKey = !!j.has_key;
    } catch {
      /* keep defaults */
    }
  }

  async function loadSchedule() {
    try {
      const j = await api<{
        mode?: string;
        sleep_start?: string | null;
        sleep_end?: string | null;
        windows?: LoadWindow[];
        learned_profile?: LearnedSample[];
      }>(`/forecast/load_schedule${deviceQ()}`);
      loadMode = j.mode === 'historical' ? 'historical' : 'scheduled';
      sleepStart = j.sleep_start || '00:00';
      sleepEnd = j.sleep_end || '06:00';
      loadWindows = normalizeWindows(j.windows);
      learnedRows = buildLearnedRows(j.learned_profile);
    } catch (err) {
      scheduleMsg = err instanceof Error ? err.message : 'Schedule load failed';
    }
  }

  async function loadAccuracy() {
    try {
      const j = await api<{ samples?: AccuracySample[] }>(
        `/forecast/accuracy${deviceQ()}`,
      );
      accuracySamples = (j.samples || [])
        .filter((s) => s.actual_soc != null)
        .slice(0, 40);
      accuracyMsg = '';
    } catch (err) {
      accuracySamples = [];
      accuracyMsg = err instanceof Error ? err.message : 'Accuracy load failed';
    }
  }

  onMount(() => {
    void (async () => {
      await Promise.all([loadLocation(), loadArray(), loadSchedule(), loadAccuracy()]);
      await loadForecast();
    })();
    return live.onDeviceChange(() => {
      void loadForecast();
      void loadArray();
      void loadSchedule();
      void loadAccuracy();
    });
  });

  async function geocode() {
    const q = geoQ.trim();
    if (!q) return;
    busy = true;
    msg = '';
    try {
      const j = await api<{ results?: GeoHit[] }>(
        `/location/geocode?q=${encodeURIComponent(q)}&count=6`,
      );
      geoHits = j.results || [];
      if (!geoHits.length) msg = 'No matches.';
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Geocode failed';
    } finally {
      busy = false;
    }
  }

  async function saveLocation(latitude: number, longitude: number, label?: string) {
    busy = true;
    msg = '';
    try {
      loc = await apiPost('/location', { latitude, longitude, label });
      showManual = false;
      msg = 'Location saved.';
      await loadForecast();
    } catch (err) {
      msg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function saveCoords() {
    const latitude = parseFloat(lat);
    const longitude = parseFloat(lon);
    if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) {
      msg = 'Enter valid lat/lon.';
      return;
    }
    await saveLocation(latitude, longitude);
  }

  async function useBrowserGeo() {
    if (!navigator.geolocation) {
      msg = 'Geolocation not available.';
      showManual = true;
      return;
    }
    busy = true;
    navigator.geolocation.getCurrentPosition(
      async (pos) => {
        await saveLocation(pos.coords.latitude, pos.coords.longitude, 'Browser location');
        busy = false;
      },
      () => {
        msg = 'Location permission denied — set manually.';
        showManual = true;
        busy = false;
      },
      { enableHighAccuracy: false, timeout: 10000 },
    );
  }

  function toggleArrayCollapsed() {
    arrayCollapsed = !arrayCollapsed;
    setString(KEYS.forecastArrayCollapse, arrayCollapsed ? '1' : '0');
  }

  function toggleLoadCollapsed() {
    loadCollapsed = !loadCollapsed;
    setString(KEYS.forecastLoadCollapse, loadCollapsed ? '1' : '0');
  }

  async function inferArray() {
    busy = true;
    arrayHint = 'Inferring from last 14 days of solar vs GHI…';
    try {
      const j = await apiPost<{
        declination?: number;
        azimuth?: number;
        kwp?: number;
        confidence?: string;
        correlation?: number;
        n_samples?: number;
        notes?: string;
      }>(`/forecast/solar_array/infer${deviceQ()}`);
      arrayCfg = {
        declination: j.declination ?? arrayCfg.declination,
        azimuth: j.azimuth ?? arrayCfg.azimuth,
        kwp: j.kwp ?? arrayCfg.kwp,
      };
      const bits: string[] = [];
      if (j.confidence) bits.push(`${j.confidence} confidence`);
      if (j.correlation != null) bits.push(`r=${j.correlation}`);
      if (j.n_samples != null) bits.push(`${j.n_samples} hours`);
      arrayHint =
        (j.notes || 'Filled from history') +
        (bits.length ? ` (${bits.join(' · ')})` : '') +
        ' — Save to apply.';
    } catch (err) {
      arrayHint = err instanceof Error ? err.message : 'Infer failed';
    } finally {
      busy = false;
    }
  }

  async function saveArray() {
    busy = true;
    arrayHint = 'Saving…';
    try {
      const sn = live.activeDeviceSn();
      const j = await apiPost<{
        declination?: number;
        azimuth?: number;
        kwp?: number;
      }>('/forecast/solar_array', {
        declination: arrayCfg.declination,
        azimuth: arrayCfg.azimuth,
        kwp: arrayCfg.kwp,
        device_sn: sn,
      });
      arrayHint = `Saved · tilt ${j.declination ?? arrayCfg.declination}° · az ${j.azimuth ?? arrayCfg.azimuth}° · ${j.kwp ?? arrayCfg.kwp} kWp`;
      await loadForecast();
    } catch (err) {
      arrayHint = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function saveSolarKey() {
    const key = solarKey.trim();
    if (!key) {
      arrayHint = 'Paste an API key first.';
      return;
    }
    busy = true;
    try {
      await apiPost('/forecast/solar_key', { api_key: key });
      solarKey = '';
      hasSolarKey = true;
      arrayHint = 'API key saved.';
      await loadForecast();
    } catch {
      arrayHint = 'Could not save key.';
    } finally {
      busy = false;
    }
  }

  async function clearSolarKey() {
    busy = true;
    try {
      await api('/forecast/solar_key', { method: 'DELETE' });
      hasSolarKey = false;
      arrayHint = 'API key cleared.';
      await loadForecast();
    } catch (err) {
      arrayHint = err instanceof Error ? err.message : 'Clear failed';
    } finally {
      busy = false;
    }
  }

  function addWindow() {
    const kind = draftKind;
    loadWindows.push({
      id: newWindowId(),
      label: draftLabel.trim(),
      start: draftStart || '18:00',
      end: draftEnd || '22:00',
      watts: Number(draftWatts) || 0,
      kind,
      date: kind === 'once' ? calFocusDate : null,
      weekdays: kind === 'weekly' ? [...draftWeekdays] : null,
    });
    draftLabel = '';
    scheduleMsg = 'Event added — Save load settings to persist.';
  }

  function removeWindow(i: number) {
    loadWindows.splice(i, 1);
  }

  async function copyLearned() {
    const sn = live.activeDeviceSn();
    if (!sn) {
      scheduleMsg = 'No device selected';
      return;
    }
    busy = true;
    scheduleMsg = 'Copying…';
    try {
      await apiPost('/forecast/load_schedule?copy_learned=1', {
        device_sn: sn,
        mode: 'scheduled',
        sleep_start: sleepStart || null,
        sleep_end: sleepEnd || null,
      });
      await loadSchedule();
      loadMode = 'scheduled';
      scheduleMsg = 'Copied learned hours into the calendar.';
      await loadForecast();
    } catch (err) {
      scheduleMsg = err instanceof Error ? err.message : 'Copy failed';
    } finally {
      busy = false;
    }
  }

  async function saveSchedule() {
    const sn = live.activeDeviceSn();
    if (!sn) {
      scheduleMsg = 'No device selected';
      return;
    }
    busy = true;
    scheduleMsg = 'Saving…';
    try {
      await apiPost('/forecast/load_schedule', {
        device_sn: sn,
        mode: loadMode,
        sleep_start: sleepStart || null,
        sleep_end: sleepEnd || null,
        windows: loadWindows,
      });
      scheduleMsg = 'Saved';
      await loadForecast();
    } catch (err) {
      scheduleMsg = err instanceof Error ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  function errCls(err: number) {
    const abs = Math.abs(err);
    if (abs <= 3) return 'acc-err-good';
    if (abs <= 8) return 'acc-err-warn';
    return 'acc-err-bad';
  }

  function padHour(h: number) {
    return String(h).padStart(2, '0') + ':00';
  }
</script>

<section class="tab-panel">
  {#if needsConfig}
    <section class="card">
      <h2>Allow location to enable forecasts</h2>
      <p class="hint">
        The forecaster needs your approximate location, then pulls solar irradiance and
        projects SOC from your array + load history.
      </p>
      <div class="forecast-geo-actions">
        <button class="btn btn-primary" type="button" disabled={busy} onclick={useBrowserGeo}>
          Use my location
        </button>
        <button class="btn btn-ghost" type="button" onclick={() => (showManual = true)}>
          Set manually
        </button>
      </div>
    </section>
  {/if}

  {#if showManual || !needsConfig}
    <section class="card">
      <div class="card-header">
        <h2>Location</h2>
        {#if !needsConfig}
          <button class="btn btn-ghost btn-tiny" type="button" onclick={() => (showManual = !showManual)}>
            {showManual ? 'Hide' : 'Change location'}
          </button>
        {/if}
      </div>
      {#if loc?.latitude != null}
        <p class="hint" style="margin:6px 0 12px">
          Current: {loc.label || `${loc.latitude}, ${loc.longitude}`}
        </p>
      {/if}
      {#if showManual || needsConfig}
        <div class="manual-loc-pane">
          <input
            class="device-select"
            type="text"
            bind:value={geoQ}
            placeholder="e.g. San Jose, CA"
            style="width:100%; max-width:360px"
          />
          <button class="btn btn-ghost" type="button" disabled={busy} onclick={geocode}>Search</button>
          <ul class="manual-loc-results">
            {#each geoHits as hit, i (`${hit.latitude}-${hit.longitude}-${i}`)}
              <li>
                <button
                  type="button"
                  class="btn btn-ghost"
                  style="text-align:left"
                  onclick={() =>
                    saveLocation(
                      hit.latitude,
                      hit.longitude,
                      hit.label || [hit.name, hit.admin1, hit.country].filter(Boolean).join(', '),
                    )}
                >
                  {hit.label || [hit.name, hit.admin1, hit.country].filter(Boolean).join(', ')}
                  <span class="hint"> ({hit.latitude}, {hit.longitude})</span>
                </button>
              </li>
            {/each}
          </ul>
          <div class="manual-loc-coord-row" style="margin-top:12px">
            <label>
              <span class="settings-label-text">Latitude</span>
              <input class="device-select" type="number" step="0.0001" bind:value={lat} style="width:140px" />
            </label>
            <label>
              <span class="settings-label-text">Longitude</span>
              <input class="device-select" type="number" step="0.0001" bind:value={lon} style="width:140px" />
            </label>
            <button class="btn btn-primary" type="button" disabled={busy} onclick={saveCoords}>Save</button>
          </div>
        </div>
      {/if}
    </section>
  {/if}

  <section class="card collapsible solar-array-card" class:collapsed={arrayCollapsed} id="solar-array-card">
    <div
      class="card-header collapsible-header"
      id="solar-array-toggle"
      role="button"
      tabindex="0"
      aria-expanded={!arrayCollapsed}
      aria-controls="solar-array-body"
      onclick={toggleArrayCollapsed}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          toggleArrayCollapsed();
        }
      }}
    >
      <h2>Solar array</h2>
      <span class="hint">{arraySummary}</span>
      <span class="collapsible-chevron" aria-hidden="true">▾</span>
    </div>
    <div id="solar-array-body" class="solar-array-body">
      <p class="hint" style="margin:0 0 12px">
        Public estimate covers today + 1 day (12 calls/hour). A personal API key extends the horizon.
        Remaining hours of the 7-day chart still use Open-Meteo × learned k.
        Infer from history guesses tilt, azimuth, and kWp from your last 14 days of solar vs GHI — check the numbers before saving.
      </p>
      <div class="settings-row">
        <label class="settings-label" for="fs-dec">
          <span class="settings-label-text">Tilt (declination)</span>
          <span class="settings-hint">0 = flat, 90 = vertical, degrees from the ground.</span>
        </label>
        <div class="settings-control">
          <input
            id="fs-dec"
            class="forecast-input"
            type="number"
            min="0"
            max="90"
            step="1"
            bind:value={arrayCfg.declination}
            placeholder="0–90"
          />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="fs-az">
          <span class="settings-label-text">Azimuth</span>
          <span class="settings-hint">Forecast.Solar: −180…180. 0 = south, −90 = east, 90 = west.</span>
        </label>
        <div class="settings-control">
          <input
            id="fs-az"
            class="forecast-input"
            type="number"
            min="-180"
            max="180"
            step="1"
            bind:value={arrayCfg.azimuth}
            placeholder="0 = S"
          />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="fs-kwp">
          <span class="settings-label-text">Array size (kWp)</span>
          <span class="settings-hint">Installed DC capacity feeding this Jackery.</span>
        </label>
        <div class="settings-control">
          <input
            id="fs-kwp"
            class="forecast-input"
            type="number"
            min="0.01"
            max="100"
            step="0.01"
            bind:value={arrayCfg.kwp}
            placeholder="kWp"
          />
        </div>
      </div>
      <div class="settings-row">
        <label class="settings-label" for="fs-key">
          <span class="settings-label-text">Forecast.Solar API key</span>
          <span class="settings-hint">Optional. Leave blank to use the public estimate URL.</span>
        </label>
        <div class="settings-control forecast-key-row">
          <input
            id="fs-key"
            class="forecast-input"
            type="password"
            autocomplete="off"
            bind:value={solarKey}
            placeholder="optional"
          />
          <span class="hint">{hasSolarKey ? 'key saved' : 'no key'}</span>
          <button class="btn btn-ghost btn-small" type="button" disabled={busy} onclick={saveSolarKey}>
            Save key
          </button>
          <button class="btn btn-ghost btn-small" type="button" disabled={busy} onclick={clearSolarKey}>
            Clear
          </button>
        </div>
      </div>
      <div class="forecast-config-actions">
        <button class="btn btn-ghost" type="button" disabled={busy} onclick={inferArray}>
          Infer from history
        </button>
        <button class="btn btn-primary" type="button" disabled={busy} onclick={saveArray}>
          Save array
        </button>
      </div>
      {#if arrayHint}
        <p class="hint" id="fs-array-hint" role="status">{arrayHint}</p>
      {/if}
    </div>
  </section>

  <section class="card collapsible" class:collapsed={loadCollapsed} id="forecast-load">
    <div
      class="card-header collapsible-header"
      role="button"
      tabindex="0"
      aria-expanded={!loadCollapsed}
      aria-controls="forecast-load-body"
      onclick={toggleLoadCollapsed}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          toggleLoadCollapsed();
        }
      }}
    >
      <h2>Expected load</h2>
      <span class="hint">{loadSummary}</span>
      <span class="collapsible-chevron" aria-hidden="true">▾</span>
    </div>
    <div id="forecast-load-body">
      <div class="forecast-config-actions" style="margin-top:0">
        <div class="range-picker" role="tablist" aria-label="Load source">
          <button
            type="button"
            class="lmode-btn"
            class:on={loadMode === 'scheduled'}
            onclick={() => (loadMode = 'scheduled')}
          >
            Calendar
          </button>
          <button
            type="button"
            class="lmode-btn"
            class:on={loadMode === 'historical'}
            onclick={() => (loadMode = 'historical')}
          >
            Historical
          </button>
        </div>
      </div>
      <p class="hint" style="margin:8px 0 12px">
        {#if loadMode === 'scheduled'}
          Calendar mode: only scheduled events draw power — hours with no event are
          <strong>0 W</strong> (inverter off). Add one-off dates or weekly repeats below.
          Sleep zeros inverter idle even when a calendar event has watts.
        {:else}
          Historical uses the learned 14-day hour × weekday/weekend table.
          Sleep zeros inverter idle even when a calendar event has watts.
        {/if}
      </p>
      <div class="settings-row">
        <label class="settings-label" for="load-sleep-start">
          <span class="settings-label-text">Sleep window</span>
          <span class="settings-hint">
            Local time. Sleep zeros idle even when a calendar event has watts.
          </span>
        </label>
        <div class="settings-control" style="display:flex; gap:8px; align-items:center; flex-wrap:wrap">
          <input id="load-sleep-start" class="forecast-input" type="time" bind:value={sleepStart} />
          <span class="hint">to</span>
          <input
            id="load-sleep-end"
            class="forecast-input"
            type="time"
            bind:value={sleepEnd}
            aria-label="Sleep end"
          />
        </div>
      </div>

      {#if loadMode === 'historical'}
        <div id="load-learned-wrap">
          <h3 class="settings-label-text" style="margin:12px 0 6px">Learned from history</h3>
          <div class="table-scroll" style="max-height:280px">
            <table class="data-table">
              <thead>
                <tr>
                  <th>Hour</th>
                  <th>Weekday W</th>
                  <th>Weekend W</th>
                </tr>
              </thead>
              <tbody>
                {#each learnedRows as r (r.hour)}
                  <tr>
                    <td>{padHour(r.hour)}</td>
                    <td>{r.weekday}</td>
                    <td>{r.weekend}</td>
                  </tr>
                {:else}
                  <tr>
                    <td colspan="3">No learned profile yet — keep the monitor running.</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          <button
            class="btn btn-ghost btn-small"
            type="button"
            disabled={busy}
            onclick={copyLearned}
            style="margin-top:8px"
          >
            Copy learned profile into calendar
          </button>
        </div>
      {:else}
        <div id="load-calendar-wrap" class="load-calendar">
          <div class="load-cal-nav">
            <button class="btn btn-ghost btn-tiny" type="button" onclick={() => shiftCalWeek(-1)}>‹</button>
            <span class="settings-label-text">Week of {calWeekDays[0]?.date}</span>
            <button class="btn btn-ghost btn-tiny" type="button" onclick={() => shiftCalWeek(1)}>›</button>
            <button
              class="btn btn-ghost btn-tiny"
              type="button"
              onclick={() => (calFocusDate = localDateStr(new Date()))}
            >
              Today
            </button>
          </div>
          <div class="load-cal-strip" role="tablist" aria-label="Week days">
            {#each calWeekDays as d (d.date)}
              <button
                type="button"
                class="load-cal-day"
                class:on={calFocusDate === d.date}
                onclick={() => (calFocusDate = d.date)}
              >
                <span class="lcd-dow">{d.dow}</span>
                <span class="lcd-num">{d.label}</span>
              </button>
            {/each}
          </div>

          <h3 class="settings-label-text" style="margin:14px 0 6px">
            Events on {calFocusDate}
          </h3>
          <div class="table-scroll" style="max-height:240px; margin-bottom:10px">
            <table class="data-table">
              <thead>
                <tr>
                  <th>Label</th>
                  <th>Start</th>
                  <th>End</th>
                  <th>W</th>
                  <th>When</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {#each dayEvents as { w, i } (w.id)}
                  <tr>
                    <td>
                      <input class="forecast-input lw-field" type="text" bind:value={w.label} style="width:100px" />
                    </td>
                    <td><input class="forecast-input lw-field" type="time" bind:value={w.start} /></td>
                    <td><input class="forecast-input lw-field" type="time" bind:value={w.end} /></td>
                    <td>
                      <input
                        class="forecast-input lw-field"
                        type="number"
                        min="0"
                        bind:value={w.watts}
                        style="width:72px"
                      />
                    </td>
                    <td><span class="hint">{eventBadge(w)}</span></td>
                    <td>
                      <button class="btn btn-ghost btn-tiny" type="button" onclick={() => removeWindow(i)}>
                        ×
                      </button>
                    </td>
                  </tr>
                {:else}
                  <tr>
                    <td colspan="6">No events this day — add one below.</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>

          <h3 class="settings-label-text" style="margin:8px 0 6px">Add event</h3>
          <div class="load-cal-draft">
            <div class="range-picker" role="tablist" aria-label="Event kind">
              <button
                type="button"
                class="lmode-btn"
                class:on={draftKind === 'once'}
                onclick={() => (draftKind = 'once')}
              >
                This date
              </button>
              <button
                type="button"
                class="lmode-btn"
                class:on={draftKind === 'weekly'}
                onclick={() => (draftKind = 'weekly')}
              >
                Repeats
              </button>
            </div>
            {#if draftKind === 'weekly'}
              <div class="load-weekday-chips" role="group" aria-label="Weekdays">
                {#each WEEKDAY_LABELS as lab, i (lab)}
                  <button
                    type="button"
                    class="load-wd-chip"
                    class:on={draftWeekdays.includes(i)}
                    onclick={() => toggleDraftWeekday(i)}
                  >
                    {lab}
                  </button>
                {/each}
              </div>
            {:else}
              <p class="hint" style="margin:0">One-off on <strong>{calFocusDate}</strong></p>
            {/if}
            <div class="load-cal-draft-fields">
              <input
                class="forecast-input"
                type="text"
                placeholder="Label"
                bind:value={draftLabel}
                style="min-width:8em"
              />
              <input class="forecast-input" type="time" bind:value={draftStart} aria-label="Start" />
              <input class="forecast-input" type="time" bind:value={draftEnd} aria-label="End" />
              <input
                class="forecast-input"
                type="number"
                min="0"
                bind:value={draftWatts}
                aria-label="Watts"
                style="width:5.5em"
              />
              <button class="btn btn-ghost" type="button" onclick={addWindow}>Add</button>
            </div>
          </div>

          {#if loadWindows.length}
            <p class="hint" style="margin-top:10px">
              {loadWindows.length} event{loadWindows.length === 1 ? '' : 's'} total in calendar
              ({loadWindows.filter((w) => w.kind === 'once').length} one-off ·
              {loadWindows.filter((w) => w.kind === 'weekly').length} weekly)
            </p>
          {/if}
        </div>
      {/if}

      <div class="forecast-config-actions">
        <button class="btn btn-primary" type="button" disabled={busy} onclick={saveSchedule}>
          Save load settings
        </button>
      </div>
      {#if scheduleMsg}
        <p class="hint" id="load-save-hint" role="status">{scheduleMsg}</p>
      {/if}
    </div>
  </section>

  <section class="card">
    <div class="card-header">
      <h2>Forecast</h2>
      <span class="hint">
        {#if forecast?.ready === false}
          {forecast.reason || 'calibrating'}
        {:else if forecast?.fit?.r2 != null}
          R² {fmt(forecast.fit.r2, 2)} · {forecast.fit.samples ?? '—'} samples
        {:else}
          —
        {/if}
      </span>
    </div>
    {#if msg}
      <p class="hint" role="status">{msg}</p>
    {/if}

    <canvas
      id="chart-forecast"
      class="chart chart-tall"
      aria-label="Forecast chart"
      {@attach attachForecastChart(forecast)}
    ></canvas>

    {#if dayTiles.length}
      <div class="forecast-day-strip" style="margin-top:14px">
        {#each dayTiles as d (d.key)}
          <div class="forecast-day-tile {d.cls}">
            <span class="fdt-label">{d.label}</span>
            <div class="fdt-soc">
              {#if Number.isFinite(d.startSoc) && Number.isFinite(d.endSoc)}
                {Math.round(d.startSoc)}<small>%</small>
                <span class="fdt-arrow">→</span>
                {Math.round(d.endSoc)}<small>%</small>
              {:else}
                —
              {/if}
            </div>
            <span class="fdt-solar">{d.solarKwh.toFixed(1)} kWh ↑</span>
            {#if d.depleted}
              <span class="fdt-warn">depleted</span>
            {:else if d.showLow}
              <span class="fdt-warn">low {Math.round(d.minSoc)}%</span>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </section>

  {#if forecast}
    <div class="grid forecast-stats-grid" id="forecast-stats">
      <section class="card">
        <div class="card-eyebrow"><span>Battery capacity</span></div>
        <div class="kpi-value"><span>{fmt(forecast.capacity_wh)}</span><small>Wh</small></div>
      </section>
      <section class="card">
        <div class="card-header" style="margin-bottom:8px">
          <div class="card-eyebrow" style="margin:0"><span>Solar coefficient (W per W/m²)</span></div>
          <button
            class="btn btn-ghost btn-tiny"
            type="button"
            disabled={busy || !live.activeDeviceSn()}
            onclick={resetCoefficient}
          >
            Reset
          </button>
        </div>
        <div class="kpi-value"><span>{coeffDisplay}</span></div>
        <div class="kpi-sub">{coeffSub}</div>
        {#if forecast.charge_ceiling_pct != null && Number.isFinite(forecast.charge_ceiling_pct)}
          <div class="kpi-sub">Charge ceiling {Math.round(forecast.charge_ceiling_pct)}%</div>
        {/if}
        {#if coeffMsg}<p class="hint" role="status">{coeffMsg}</p>{/if}
      </section>
      <section class="card">
        <div class="card-eyebrow"><span>Avg load</span></div>
        <div class="kpi-value"><span>{fmt(forecast.overall_load_w)}</span><small>W</small></div>
        <div class="kpi-sub">{loadSub}</div>
      </section>
    </div>
  {/if}

  <section class="card" id="today-budget-card">
    <div class="card-header">
      <h2>Today's energy budget</h2>
      {#if budget && !('afterSunset' in budget) && Number.isFinite(budget.socStart) && Number.isFinite(budget.socEnd)}
        <span class="hint">
          {Math.round(budget.socStart)} → {Math.round(budget.socEnd)}% · {budget.solarKwh.toFixed(1)} kWh solar
        </span>
      {/if}
    </div>
    {#if !budget}
      <p class="hint">No daylight window in the forecast yet.</p>
    {:else if 'afterSunset' in budget}
      <p class="hint">After sunset — open tomorrow.</p>
    {:else}
      <div class="kpi-row" style="margin-bottom:8px">
        <div>
          <div class="kpi-value"><span>{budget.solarKwh.toFixed(1)}</span><small>kWh</small></div>
          <div class="kpi-sub">solar · {budget.hours} h, peak {Math.round(budget.peakSolar)} W</div>
        </div>
        <div>
          <div class="kpi-value"><span>{budget.loadKwh.toFixed(1)}</span><small>kWh</small></div>
          <div class="kpi-sub">
            load{#if budget.hours}
              · avg {Math.round((budget.loadKwh * 1000) / budget.hours)} W
            {/if}
          </div>
        </div>
        <div>
          <div class="kpi-value">
            <span>
              {#if Number.isFinite(budget.socStart) && Number.isFinite(budget.socEnd)}
                {Math.round(budget.socStart)}→{Math.round(budget.socEnd)}
              {:else}
                —
              {/if}
            </span><small>%</small>
          </div>
          <div class="kpi-sub">
            {#if budget.netKwh != null}
              net {budget.netKwh.toFixed(1)} kWh
              {#if budget.lossKwh != null}
                · loss {budget.lossKwh.toFixed(1)}
              {/if}
            {:else}
              start → end SOC
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </section>

  <section class="card">
    <div class="card-header">
      <h2>Forecast accuracy</h2>
      <button class="btn btn-ghost btn-tiny" type="button" disabled={busy} onclick={loadAccuracy}>
        Refresh
      </button>
    </div>
    {#if accuracyMsg}
      <p class="hint">{accuracyMsg}</p>
    {/if}
    {#if !accuracySamples.length}
      <p class="hint">No predicted-vs-actual rows yet.</p>
    {:else}
      <div class="table-scroll" style="max-height:320px">
        <table class="acc-table">
          <thead>
            <tr>
              <th>Target</th>
              <th class="num">Lead h</th>
              <th class="num">Predicted</th>
              <th class="num">Actual</th>
              <th class="num">Error</th>
            </tr>
          </thead>
          <tbody>
            {#each accuracySamples as s, i (`${s.target ?? i}-${s.made_at ?? 0}`)}
              {@const err =
                s.predicted_soc != null && s.actual_soc != null
                  ? s.predicted_soc - s.actual_soc
                  : null}
              <tr>
                <td>
                  {s.target != null
                    ? new Date(s.target * 1000).toLocaleString([], {
                        month: 'short',
                        day: 'numeric',
                        hour: '2-digit',
                        minute: '2-digit',
                      })
                    : '—'}
                </td>
                <td class="num">{s.lead_time_h != null ? fmt(s.lead_time_h, 1) : '—'}</td>
                <td class="num">{s.predicted_soc != null ? `${Math.round(s.predicted_soc)}%` : '—'}</td>
                <td class="num">{s.actual_soc != null ? `${Math.round(s.actual_soc)}%` : '—'}</td>
                <td class="num {err != null ? errCls(err) : 'acc-missing'}">
                  {#if err != null}
                    {(err > 0 ? '+' : '') + err.toFixed(1)}
                  {:else}
                    —
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
</section>
