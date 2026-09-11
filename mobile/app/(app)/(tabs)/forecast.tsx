import * as Location from "expo-location";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import { LineChart } from "../../../src/components/LineChart";
import {
  Btn,
  Card,
  Collapsible,
  Eyebrow,
  Field,
  Hint,
  Kpi,
  Pill,
  Screen,
  Segmented,
} from "../../../src/components/ui";
import {
  accTone,
  avgLoadSub,
  buildDayStrip,
  buildTodayBudget,
  calibratingCopy,
  fmtErr,
  fmtPct,
  formatLearnedProfile,
  solarCoeffSub,
  solarSourceSuffix,
  summarizeDailyAccuracy,
  weatherNote,
  type DailySummaryRow,
  type ForecastPayload,
  type LearnedBucket,
} from "../../../src/lib/forecast";
import { useLive } from "../../../src/store/live";
import { colors, radius } from "../../../src/theme";

type LoadWindow = {
  id?: string;
  label?: string;
  start?: string;
  end?: string;
  watts?: number;
  days?: string;
};

type Loc = {
  latitude?: number | null;
  longitude?: number | null;
  label?: string;
  timezone?: string;
};

type GeoHit = {
  name: string;
  admin1?: string;
  country?: string;
  latitude: number;
  longitude: number;
  timezone?: string;
};

const ACC_DAYS = [
  { id: "3", label: "3d" },
  { id: "7", label: "7d" },
  { id: "14", label: "14d" },
  { id: "30", label: "30d" },
  { id: "60", label: "60d" },
  { id: "90", label: "90d" },
];

const DAY_OPTS = [
  { id: "all", label: "all" },
  { id: "weekday", label: "weekday" },
  { id: "weekend", label: "weekend" },
];

function locLine(loc: Loc | null): string {
  if (loc?.latitude == null || loc?.longitude == null) {
    return "No location set — forecasts unavailable.";
  }
  const coords = `${Number(loc.latitude).toFixed(4)}, ${Number(loc.longitude).toFixed(4)}`;
  const name = loc.label ? `${loc.label} — ` : "";
  const tz = loc.timezone ? ` (${loc.timezone})` : "";
  return `Forecasting for: ${name}${coords}${tz}`;
}

function geoLabel(r: GeoHit): string {
  return [r.name, r.admin1, r.country].filter(Boolean).join(", ");
}

function signed(n: number): string {
  return `${n > 0 ? "+" : ""}${Math.round(n)}`;
}

export default function ForecastScreen() {
  const deviceSn = useLive((s) => s.status?.device?.device_sn || undefined);
  const deviceName = useLive((s) => s.status?.device?.name as string | undefined);

  const [loc, setLoc] = useState<Loc | null>(null);
  const [fc, setFc] = useState<ForecastPayload | null>(null);
  const [acc, setAcc] = useState<{ rows?: DailySummaryRow[]; cutoff_ts?: number } | null>(null);
  const [accDays, setAccDays] = useState("14");
  const [showManualLoc, setShowManualLoc] = useState(false);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<GeoHit[]>([]);
  const [searchHint, setSearchHint] = useState<string | null>(null);
  const [lat, setLat] = useState("");
  const [lon, setLon] = useState("");
  const [coordsHint, setCoordsHint] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [dec, setDec] = useState("20");
  const [az, setAz] = useState("0");
  const [kwp, setKwp] = useState("");
  const [fsKey, setFsKey] = useState("");
  const [hasFsKey, setHasFsKey] = useState(false);
  const [arrayHint, setArrayHint] = useState<string | null>(null);
  const [loadMode, setLoadMode] = useState("historical");
  const [sleepStart, setSleepStart] = useState("");
  const [sleepEnd, setSleepEnd] = useState("");
  const [windows, setWindows] = useState<LoadWindow[]>([]);
  const [learned, setLearned] = useState<LearnedBucket[]>([]);
  const [loadHint, setLoadHint] = useState<string | null>(null);
  const [hydrated, setHydrated] = useState(false);

  const configured = !!(loc?.latitude != null && loc?.longitude != null);

  const load = useCallback(async () => {
    try {
      const l = (await endpoints.location()) as Loc;
      setLoc(l);
    } catch {
      setLoc(null);
    }
    try {
      const f = (await endpoints.forecast(activeSn())) as ForecastPayload;
      setFc(f);
    } catch {
      setFc(null);
    }
    try {
      const s = (await endpoints.solarArray()) as {
        array?: { declination?: number; azimuth?: number; kwp?: number };
        has_key?: boolean;
      };
      if (s.array?.declination != null) setDec(String(s.array.declination));
      if (s.array?.azimuth != null) setAz(String(s.array.azimuth));
      if (s.array?.kwp != null) setKwp(String(s.array.kwp));
      setHasFsKey(!!s.has_key);
    } catch {
      /* ignore */
    }
    try {
      const ls = (await endpoints.loadSchedule(activeSn())) as {
        mode?: string;
        sleep_start?: string | null;
        sleep_end?: string | null;
        windows?: LoadWindow[];
        learned_profile?: LearnedBucket[];
      };
      setLoadMode(ls.mode === "scheduled" ? "scheduled" : "historical");
      setSleepStart(ls.sleep_start || "");
      setSleepEnd(ls.sleep_end || "");
      setWindows(ls.windows || []);
      setLearned(ls.learned_profile || []);
    } catch {
      /* ignore */
    } finally {
      setHydrated(true);
    }
  }, []);

  const loadAccuracy = useCallback(async (days: number) => {
    try {
      const a = (await endpoints.dailySummary(days, activeSn())) as {
        rows?: DailySummaryRow[];
        cutoff_ts?: number;
      };
      setAcc(a);
    } catch {
      setAcc(null);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, deviceSn]);

  useEffect(() => {
    void loadAccuracy(Number(accDays) || 14);
  }, [loadAccuracy, accDays, deviceSn]);

  async function useGps() {
    setMsg(null);
    const perm = await Location.requestForegroundPermissionsAsync();
    if (!perm.granted) {
      setMsg("Location permission denied");
      return;
    }
    const pos = await Location.getCurrentPositionAsync({});
    await endpoints.setLocation(pos.coords.latitude, pos.coords.longitude, "Current location");
    setShowManualLoc(false);
    await load();
  }

  async function search() {
    if (q.trim().length < 2) return;
    setSearchHint("Searching…");
    try {
      const r = (await endpoints.geocode(q.trim())) as { results?: GeoHit[] } | GeoHit[];
      const list = Array.isArray(r) ? r : r.results || [];
      setResults(list);
      setSearchHint(list.length ? `${list.length} match${list.length === 1 ? "" : "es"}` : "No matches");
    } catch (e: unknown) {
      setSearchHint(e instanceof Error ? e.message : "Search failed");
    }
  }

  async function pickPlace(r: GeoHit) {
    await endpoints.setLocation(r.latitude, r.longitude, geoLabel(r));
    setResults([]);
    setQ("");
    setSearchHint("Saved");
    setShowManualLoc(false);
    await load();
  }

  async function saveCoords() {
    const la = Number(lat);
    const lo = Number(lon);
    if (!Number.isFinite(la) || la < -90 || la > 90) {
      setCoordsHint("Latitude must be between −90 and 90");
      return;
    }
    if (!Number.isFinite(lo) || lo < -180 || lo > 180) {
      setCoordsHint("Longitude must be between −180 and 180");
      return;
    }
    await endpoints.setLocation(la, lo);
    setCoordsHint("Saved");
    setShowManualLoc(false);
    await load();
  }

  function patchWindow(i: number, patch: Partial<LoadWindow>) {
    setWindows((prev) => prev.map((w, idx) => (idx === i ? { ...w, ...patch } : w)));
  }

  async function saveLoad() {
    setLoadHint("Saving…");
    try {
      await endpoints.setLoadSchedule({
        device_sn: activeSn(),
        mode: loadMode,
        sleep_start: sleepStart || null,
        sleep_end: sleepEnd || null,
        windows,
      });
      setLoadHint("Saved");
      await load();
    } catch (e: unknown) {
      setLoadHint(e instanceof Error ? e.message : "Save failed");
    }
  }

  const hours = fc?.forecast || [];
  const ready = !!(configured && fc?.ready && !fc.error);
  const dayStrip = useMemo(() => (fc ? buildDayStrip(fc) : []), [fc]);
  const budget = useMemo(() => (fc ? buildTodayBudget(fc) : { empty: true as const, summary: "—" }), [fc]);
  const learnedRows = useMemo(() => formatLearnedProfile(learned), [learned]);
  const accuracy = useMemo(
    () => summarizeDailyAccuracy(acc?.rows, acc?.cutoff_ts),
    [acc],
  );

  const chartTitle = (() => {
    const base = deviceName ? `${deviceName} — next 5 days` : "State of charge — next 5 days";
    if (!fc) return base;
    return base + weatherNote(fc) + solarSourceSuffix(fc.solar_source);
  })();

  const arraySummary = `tilt ${dec}° · az ${az}° · ${kwp || "—"} kWp`;
  const sleepBit = sleepStart && sleepEnd ? ` · sleep ${sleepStart}–${sleepEnd}` : "";
  const winBit = windows.length
    ? ` · ${windows.length} window${windows.length === 1 ? "" : "s"}`
    : "";
  const loadSummary = `${loadMode === "scheduled" ? "Scheduled" : "Historical"}${winBit}${sleepBit}`;

  const fetchFailed = hydrated && configured && fc == null;
  const needsConfig = !configured || !!fc?.error || fc?.ready === false || fetchFailed;
  const cal = fc ? calibratingCopy(fc) : null;

  return (
    <Screen>
      {needsConfig ? (
        <Card>
          <Eyebrow>
            {!configured
              ? "Allow location to enable forecasts"
              : fc?.error
                ? fc.error
                : fetchFailed
                  ? "Couldn't load forecast"
                  : cal?.title || "Forecaster is calibrating"}
          </Eyebrow>
          <Hint>
            {!configured
              ? "Open-Meteo + Forecast.Solar need an approximate location. Use GPS or search by city."
              : fc?.error
                ? fc.error_detail
                  ? `The forecast needs weather data and can't reach the weather service right now (${fc.error_detail}). It refreshes automatically once the connection is restored.`
                  : "Weather data is unavailable."
                : fetchFailed
                  ? "The monitor didn't return a forecast. Check the connection and reopen this tab."
                  : cal?.body}
          </Hint>
          <Btn title="Use my location" onPress={() => void useGps()} />
          <Btn title="Set manually" kind="ghost" onPress={() => setShowManualLoc(true)} />
        </Card>
      ) : null}
      {msg ? <Hint>{msg}</Hint> : null}

      {showManualLoc || !configured ? (
        <Card>
          <View style={styles.rowBetween}>
            <Eyebrow>Set location</Eyebrow>
            {configured ? (
              <Pressable onPress={() => setShowManualLoc(false)}>
                <Text style={styles.link}>Close</Text>
              </Pressable>
            ) : null}
          </View>
          <Hint>
            {loc?.latitude != null
              ? `Currently using ${Number(loc.latitude).toFixed(4)}, ${Number(loc.longitude).toFixed(4)}${loc.timezone ? ` (${loc.timezone})` : ""}`
              : "No location saved yet."}
          </Hint>
          <Field
            label="City search"
            value={q}
            onChangeText={setQ}
            placeholder="San Jose, CA"
            onSubmitEditing={() => void search()}
          />
          <Btn title="Search" kind="ghost" onPress={() => void search()} />
          {searchHint ? <Hint>{searchHint}</Hint> : null}
          {results.map((r, i) => (
            <Btn
              key={`${r.latitude},${r.longitude},${i}`}
              title={geoLabel(r)}
              kind="ghost"
              onPress={() => void pickPlace(r)}
            />
          ))}
          <View style={styles.row}>
            <View style={{ flex: 1 }}>
              <Field label="Latitude" value={lat} onChangeText={setLat} keyboardType="decimal-pad" />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="Longitude" value={lon} onChangeText={setLon} keyboardType="decimal-pad" />
            </View>
          </View>
          <Btn title="Save coordinates" kind="ghost" onPress={() => void saveCoords()} />
          {coordsHint ? <Hint>{coordsHint}</Hint> : null}
        </Card>
      ) : null}

      <Collapsible title="Solar array" summary={arraySummary} defaultOpen>
        <Hint>
          Tilt 0–90°, azimuth −180…180 (0 = south). Public plan covers today + 1 day; a key extends
          the horizon. Open-Meteo fills remaining days. Infer from 14 days of history.
        </Hint>
        <View style={styles.row}>
          <View style={{ flex: 1 }}>
            <Field label="Tilt" value={dec} onChangeText={setDec} keyboardType="number-pad" />
          </View>
          <View style={{ flex: 1 }}>
            <Field
              label="Azimuth"
              value={az}
              onChangeText={setAz}
              keyboardType="numbers-and-punctuation"
            />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="kWp" value={kwp} onChangeText={setKwp} keyboardType="decimal-pad" />
          </View>
        </View>
        <Btn
          title="Infer from history"
          kind="ghost"
          onPress={() =>
            void endpoints
              .inferSolarArray(activeSn())
              .then((raw) => {
                const j = raw as {
                  declination?: number;
                  azimuth?: number;
                  kwp?: number;
                  notes?: string;
                };
                if (j.declination != null) setDec(String(j.declination));
                if (j.azimuth != null) setAz(String(j.azimuth));
                if (j.kwp != null) setKwp(String(j.kwp));
                setArrayHint(j.notes || "Filled from history — save to apply.");
              })
              .catch((e: unknown) => {
                setArrayHint(e instanceof Error ? e.message : "Infer failed");
              })
          }
        />
        <Btn
          title="Save array"
          onPress={() =>
            void endpoints
              .setSolarArray(Number(dec), Number(az), Number(kwp))
              .then(() => {
                setArrayHint("Saved");
                return load();
              })
              .catch((e: unknown) => {
                setArrayHint(e instanceof Error ? e.message : "Save failed");
              })
          }
        />
        <Field
          label="Forecast.Solar API key (optional)"
          value={fsKey}
          onChangeText={setFsKey}
          secureTextEntry
          placeholder="optional"
        />
        <Hint>{hasFsKey ? "Key saved" : "no key — public estimate URL"}</Hint>
        <View style={styles.row}>
          <View style={{ flex: 1 }}>
            <Btn
              title="Save key"
              kind="ghost"
              onPress={() =>
                void endpoints.setSolarKey(fsKey).then(() => {
                  setFsKey("");
                  setArrayHint("Key saved");
                  void load();
                })
              }
            />
          </View>
          <View style={{ flex: 1 }}>
            <Btn
              title="Clear key"
              kind="ghost"
              onPress={() =>
                void endpoints.clearSolarKey().then(() => {
                  setFsKey("");
                  setArrayHint("Key cleared");
                  void load();
                })
              }
            />
          </View>
        </View>
        {arrayHint ? <Hint>{arrayHint}</Hint> : null}
      </Collapsible>

      <Collapsible title="Expected load" summary={loadSummary} defaultOpen>
        <Segmented
          options={[
            { id: "historical", label: "Historical" },
            { id: "scheduled", label: "Scheduled" },
          ]}
          value={loadMode}
          onChange={setLoadMode}
        />
        <Hint>
          Historical = 14d learned by hour/weekday. Scheduled = windows only (uncovered hours = 0 W).
          0 W means the inverter is off — no parasitic. Sleep zeros idle even when a window has watts.
        </Hint>
        <View style={styles.row}>
          <View style={{ flex: 1 }}>
            <Field
              label="Sleep start"
              value={sleepStart}
              onChangeText={setSleepStart}
              placeholder="23:00"
            />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="Sleep end" value={sleepEnd} onChangeText={setSleepEnd} placeholder="07:00" />
          </View>
        </View>

        <Eyebrow>Learned profile (14d)</Eyebrow>
        {learnedRows.length ? (
          <View>
            <View style={styles.tableHead}>
              <Text style={[styles.th, { flex: 1.1 }]}>Hour</Text>
              <Text style={[styles.th, { flex: 1 }]}>Weekday</Text>
              <Text style={[styles.th, { flex: 1 }]}>Weekend</Text>
            </View>
            {learnedRows.map((r) => (
              <View key={r.hour} style={styles.tableRow}>
                <Text style={[styles.td, { flex: 1.1 }]}>{r.label}</Text>
                <Text style={[styles.td, { flex: 1 }]}>{r.weekday}</Text>
                <Text style={[styles.td, { flex: 1 }]}>{r.weekend}</Text>
              </View>
            ))}
          </View>
        ) : (
          <Hint>No learned profile yet — keep the monitor running.</Hint>
        )}
        <Btn
          title="Copy learned profile into schedule"
          kind="ghost"
          onPress={() =>
            void endpoints
              .setLoadSchedule(
                {
                  device_sn: activeSn(),
                  sleep_start: sleepStart || null,
                  sleep_end: sleepEnd || null,
                },
                true,
              )
              .then(() => {
                setLoadHint("Copied learned profile");
                return load();
              })
              .catch((e: unknown) => {
                setLoadHint(e instanceof Error ? e.message : "Copy failed");
              })
          }
        />

        <Eyebrow>Schedule windows</Eyebrow>
        {windows.length ? (
          windows.map((w, i) => (
            <View key={w.id || i} style={styles.window}>
              <Field
                label="Label"
                value={w.label || ""}
                onChangeText={(v) => patchWindow(i, { label: v })}
              />
              <View style={styles.row}>
                <View style={{ flex: 1 }}>
                  <Field
                    label="Start"
                    value={w.start || ""}
                    onChangeText={(v) => patchWindow(i, { start: v })}
                    placeholder="18:00"
                  />
                </View>
                <View style={{ flex: 1 }}>
                  <Field
                    label="End"
                    value={w.end || ""}
                    onChangeText={(v) => patchWindow(i, { end: v })}
                    placeholder="22:00"
                  />
                </View>
                <View style={{ flex: 1 }}>
                  <Field
                    label="Watts"
                    value={w.watts == null ? "" : String(w.watts)}
                    onChangeText={(v) => patchWindow(i, { watts: Number(v) || 0 })}
                    keyboardType="number-pad"
                  />
                </View>
              </View>
              <Segmented
                options={DAY_OPTS}
                value={w.days === "weekday" || w.days === "weekend" ? w.days : "all"}
                onChange={(id) => patchWindow(i, { days: id })}
              />
              <Btn
                title="Delete window"
                kind="ghost"
                onPress={() => setWindows((prev) => prev.filter((_, idx) => idx !== i))}
              />
            </View>
          ))
        ) : (
          <Hint>No windows — add one, or copy the learned profile.</Hint>
        )}
        <Btn
          title="Add window"
          kind="ghost"
          onPress={() =>
            setWindows((prev) => [
              ...prev,
              { label: "", start: "18:00", end: "22:00", watts: 800, days: "all" },
            ])
          }
        />
        <Btn title="Save load settings" onPress={() => void saveLoad()} />
        {loadHint ? <Hint>{loadHint}</Hint> : null}
      </Collapsible>

      {ready ? (
        <>
          <Card>
            <View style={styles.rowBetween}>
              <Text style={styles.chartTitle}>{chartTitle}</Text>
              <Pressable onPress={() => setShowManualLoc(true)}>
                <Text style={styles.link}>Change</Text>
              </Pressable>
            </View>
            <Hint>{locLine(loc)}</Hint>
            {dayStrip.length ? (
              <ScrollView
                horizontal
                nestedScrollEnabled
                showsHorizontalScrollIndicator={false}
                contentContainerStyle={styles.strip}
              >
                {dayStrip.map((d) => (
                  <View
                    key={d.key}
                    style={[
                      styles.tile,
                      d.tone === "good" && styles.tileGood,
                      d.tone === "warn" && styles.tileWarn,
                      d.tone === "bad" && styles.tileBad,
                    ]}
                  >
                    <Text style={styles.tileLabel}>{d.label}</Text>
                    <View style={styles.arc}>
                      {d.points.map((p, i) => (
                        <View key={`${p.label}-${i}`} style={styles.pt}>
                          <Text style={styles.ptLabel}>{p.label}</Text>
                          <Text style={styles.ptValue}>{Math.round(p.value)}%</Text>
                        </View>
                      ))}
                    </View>
                    <Text style={styles.tileSolar}>{d.solarKwh.toFixed(1)} kWh ↑</Text>
                    {d.depleted ? <Text style={styles.tileWarnText}>depleted</Text> : null}
                  </View>
                ))}
              </ScrollView>
            ) : null}
            <LineChart
              height={220}
              toggleable
              series={[
                {
                  id: "soc",
                  label: "Predicted SOC",
                  color: colors.accent3,
                  unit: "%",
                  min: 0,
                  max: 100,
                  values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.predicted_soc ?? 0) })),
                },
                {
                  id: "load",
                  label: "Expected load",
                  color: colors.accent,
                  unit: "W",
                  axis: "right",
                  dashed: true,
                  values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.load_w ?? 0) })),
                },
                {
                  id: "solar",
                  label: "Expected solar",
                  color: colors.accent2,
                  unit: "W",
                  axis: "right",
                  values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.solar_w ?? 0) })),
                },
              ]}
            />
          </Card>

          <Kpi
            label="Battery capacity"
            value={fc?.capacity_wh != null ? String(Math.round(fc.capacity_wh)) : "—"}
            unit="Wh"
          />
          <Kpi
            label="Solar coefficient (W per W/m²)"
            value={fc?.solar_coefficient != null ? fc.solar_coefficient.toFixed(2) : "—"}
            sub={solarCoeffSub(fc || {})}
          />
          <Kpi
            label="Avg load"
            value={fc?.overall_load_w != null ? String(Math.round(fc.overall_load_w)) : "—"}
            unit="W"
            sub={avgLoadSub(fc || {})}
          />

          <Collapsible title="Today's energy budget" summary={budget.summary}>
            {budget.empty ? (
              <Hint>
                {budget.summary === "After sunset — open tomorrow"
                  ? budget.summary
                  : "Forecast hasn't fitted yet — open after the calibrating badge clears."}
              </Hint>
            ) : (
              <>
                <Hint>
                  Hour-by-hour walk from now to sunset. Solar minus load gives net W; positive hours
                  are multiplied by {budget.chargeEfficiency.toFixed(2)} charge efficiency before
                  updating SOC. Load already includes parasitic + inverter overhead.
                </Hint>
                <View style={styles.tiles3}>
                  <View style={styles.mini}>
                    <Text style={styles.miniLabel}>Solar in</Text>
                    <Text style={styles.miniValue}>{budget.solarKwh.toFixed(1)} kWh</Text>
                    <Text style={styles.miniSub}>{budget.solarSub}</Text>
                  </View>
                  <View style={styles.mini}>
                    <Text style={styles.miniLabel}>Drain</Text>
                    <Text style={styles.miniValue}>{budget.loadKwh.toFixed(1)} kWh</Text>
                    <Text style={styles.miniSub}>{budget.loadSub}</Text>
                  </View>
                  <View style={styles.mini}>
                    <Text style={styles.miniLabel}>Net to battery</Text>
                    <Text style={styles.miniValue}>{budget.netKwh.toFixed(1)} kWh</Text>
                    <Text style={styles.miniSub}>{budget.netSub}</Text>
                  </View>
                </View>
                <View style={styles.tableHead}>
                  <Text style={[styles.th, { flex: 1.1 }]}>Hour</Text>
                  <Text style={[styles.th, { flex: 1 }]}>Solar</Text>
                  <Text style={[styles.th, { flex: 1 }]}>Load</Text>
                  <Text style={[styles.th, { flex: 1 }]}>Net</Text>
                  <Text style={[styles.th, { flex: 1 }]}>ΔSOC</Text>
                  <Text style={[styles.th, { flex: 0.9 }]}>SOC</Text>
                </View>
                {budget.rows.map((r, i) => (
                  <View key={`${r.hour}-${i}`} style={styles.tableRow}>
                    <Text style={[styles.td, { flex: 1.1 }]}>{r.hour}</Text>
                    <Text style={[styles.td, { flex: 1 }]}>{r.solar}</Text>
                    <Text style={[styles.td, { flex: 1 }]}>{r.load}</Text>
                    <Text
                      style={[
                        styles.td,
                        { flex: 1, color: r.net > 0 ? colors.accent : r.net < 0 ? colors.danger : colors.text },
                      ]}
                    >
                      {signed(r.net)}
                    </Text>
                    <Text
                      style={[
                        styles.td,
                        { flex: 1, color: r.dpp >= 0 ? colors.accent : colors.danger },
                      ]}
                    >
                      {`${r.dpp >= 0 ? "+" : ""}${r.dpp.toFixed(2)}`}
                    </Text>
                    <Text style={[styles.td, { flex: 0.9 }]}>{r.soc.toFixed(1)}</Text>
                  </View>
                ))}
              </>
            )}
          </Collapsible>

          <Collapsible
            title="Sunset / sunrise accuracy"
            summary={
              accuracy.sunsetMae
                ? `sunset MAE ${accuracy.sunsetMae} pp`
                : "No daily summary rows yet"
            }
          >
            <Segmented options={ACC_DAYS} value={accDays} onChange={setAccDays} />
            <Hint>
              Predicted SOC at sunset / sunrise vs actual. Error in pp. Green ≤3, yellow 4–8, red {">"}8.
            </Hint>
            <View style={styles.tiles3}>
              <View style={styles.mini}>
                <Text style={styles.miniLabel}>Sunset MAE</Text>
                <Text style={styles.miniValue}>{accuracy.sunsetMae ?? "—"} pp</Text>
                <Text style={styles.miniSub}>{accuracy.sunsetN}</Text>
              </View>
              <View style={styles.mini}>
                <Text style={styles.miniLabel}>Sunrise MAE</Text>
                <Text style={styles.miniValue}>{accuracy.sunriseMae ?? "—"} pp</Text>
                <Text style={styles.miniSub}>{accuracy.sunriseN}</Text>
              </View>
              <View style={styles.mini}>
                <Text style={styles.miniLabel}>Hit rate</Text>
                <Text style={styles.miniValue}>{accuracy.hitRate != null ? `${accuracy.hitRate}%` : "—"}</Text>
                <Text style={styles.miniSub}>{accuracy.hitSub}</Text>
              </View>
            </View>
            {accuracy.rows.length ? (
              accuracy.rows.map((row) => (
                <View key={row.date} style={[styles.accRow, row.stale && styles.accStale]}>
                  <View style={styles.rowBetween}>
                    <Text style={styles.accDate}>{row.date}</Text>
                    {row.stale ? <Pill label="stale" tone="mute" /> : null}
                  </View>
                  <View style={styles.rowBetween}>
                    <Text style={styles.miniLabel}>Sunset</Text>
                    <Text style={styles.td}>
                      {fmtPct(row.sunsetPred)} → {fmtPct(row.sunsetActual)}
                    </Text>
                    <Pill label={fmtErr(row.sunsetErr)} tone={accTone(row.sunsetErr)} />
                  </View>
                  <View style={styles.rowBetween}>
                    <Text style={styles.miniLabel}>Sunrise</Text>
                    <Text style={styles.td}>
                      {fmtPct(row.sunrisePred)} → {fmtPct(row.sunriseActual)}
                    </Text>
                    <Pill label={fmtErr(row.sunriseErr)} tone={accTone(row.sunriseErr)} />
                  </View>
                </View>
              ))
            ) : (
              <Hint>
                No daily summary rows yet — fills in over the next few days as the smart-charge tick
                logs each sunset/sunrise.
              </Hint>
            )}
          </Collapsible>
        </>
      ) : null}
    </Screen>
  );
}

const styles = StyleSheet.create({
  row: { flexDirection: "row", gap: 8 },
  rowBetween: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 8 },
  link: { color: colors.accent2, fontSize: 13, fontWeight: "600" },
  chartTitle: { color: colors.text, fontSize: 16, fontWeight: "600", flex: 1 },
  strip: { gap: 8, paddingVertical: 4 },
  tile: {
    width: 168,
    backgroundColor: colors.bgElev2,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.sm,
    padding: 10,
    gap: 6,
  },
  tileGood: { borderColor: colors.accent, backgroundColor: "rgba(74,222,128,0.08)" },
  tileWarn: { borderColor: colors.accent3, backgroundColor: "rgba(251,191,36,0.08)" },
  tileBad: { borderColor: colors.danger, backgroundColor: "rgba(239,68,68,0.08)" },
  tileLabel: { color: colors.textDim, fontSize: 11, fontWeight: "700", textTransform: "uppercase" },
  arc: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
  pt: { minWidth: 44 },
  ptLabel: { color: colors.textMute, fontSize: 9, textTransform: "uppercase" },
  ptValue: { color: colors.text, fontSize: 13, fontWeight: "700" },
  tileSolar: { color: colors.accent2, fontSize: 12, fontWeight: "600" },
  tileWarnText: { color: colors.danger, fontSize: 11, fontWeight: "700" },
  window: {
    backgroundColor: colors.bgElev2,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.sm,
    padding: 10,
    gap: 8,
  },
  tableHead: { flexDirection: "row", gap: 4, paddingBottom: 4 },
  tableRow: { flexDirection: "row", gap: 4, paddingVertical: 3 },
  th: { color: colors.textMute, fontSize: 10, fontWeight: "700", textTransform: "uppercase" },
  td: { color: colors.text, fontSize: 12 },
  tiles3: { flexDirection: "row", gap: 8 },
  mini: { flex: 1, gap: 2 },
  miniLabel: { color: colors.textMute, fontSize: 10, fontWeight: "600", textTransform: "uppercase" },
  miniValue: { color: colors.text, fontSize: 15, fontWeight: "700" },
  miniSub: { color: colors.textMute, fontSize: 10, lineHeight: 13 },
  accRow: {
    backgroundColor: colors.bgElev2,
    borderRadius: radius.sm,
    padding: 10,
    gap: 6,
  },
  accStale: { opacity: 0.55 },
  accDate: { color: colors.text, fontSize: 13, fontWeight: "600" },
});
