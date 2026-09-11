import * as Location from "expo-location";
import { useCallback, useEffect, useState } from "react";
import { Text, View } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import { LineChart } from "../../../src/components/LineChart";
import { Btn, Card, Eyebrow, Field, Hint, Kpi, Screen, Segmented } from "../../../src/components/ui";
import { useLive } from "../../../src/store/live";
import { colors } from "../../../src/theme";

type ForecastHour = {
  ts?: number;
  predicted_soc?: number;
  load_w?: number;
  solar_w?: number;
};

type LoadWindow = {
  id?: string;
  label?: string;
  start?: string;
  end?: string;
  watts?: number;
  days?: string;
};

export default function ForecastScreen() {
  const [loc, setLoc] = useState<{ latitude?: number; longitude?: number; label?: string } | null>(null);
  const [fc, setFc] = useState<{
    ready?: boolean;
    forecast?: ForecastHour[];
    capacity_wh?: number;
    solar_coefficient?: number;
    overall_load_w?: number;
    parasitic_w?: number;
    pack_baseline_w?: number;
    solar_source?: string;
    configured?: boolean;
    today_budget?: {
      solar_kwh?: number;
      load_kwh?: number;
      net_kwh?: number;
      hours?: { hour: string; solar_w: number; load_w: number; net_w: number; dsoc: number; soc: number }[];
    };
  } | null>(null);
  const [acc, setAcc] = useState<{ rows?: { date: string; sunset_pred?: number; sunset_actual?: number; sunrise_pred?: number; sunrise_actual?: number }[] } | null>(null);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<{ name: string; latitude: number; longitude: number }[]>([]);
  const [lat, setLat] = useState("");
  const [lon, setLon] = useState("");
  const [msg, setMsg] = useState<string | null>(null);
  const [dec, setDec] = useState("20");
  const [az, setAz] = useState("0");
  const [kwp, setKwp] = useState("");
  const [fsKey, setFsKey] = useState("");
  const [hasFsKey, setHasFsKey] = useState(false);
  const [loadMode, setLoadMode] = useState("historical");
  const [sleepStart, setSleepStart] = useState("");
  const [sleepEnd, setSleepEnd] = useState("");
  const [windows, setWindows] = useState<LoadWindow[]>([]);
  const [wStart, setWStart] = useState("18:00");
  const [wEnd, setWEnd] = useState("22:00");
  const [wWatts, setWWatts] = useState("800");
  // Direct open of this tab races the live socket: activeSn() is null
  // until status arrives. Re-run load() when the SN appears or changes.
  const deviceSn = useLive((s) => s.status?.device?.device_sn || undefined);

  const load = useCallback(async () => {
    try {
      const l = (await endpoints.location()) as typeof loc;
      setLoc(l);
    } catch {
      setLoc(null);
    }
    try {
      const f = (await endpoints.forecast(activeSn())) as typeof fc;
      setFc(f);
    } catch {
      setFc(null);
    }
    try {
      const a = (await endpoints.dailySummary(14)) as typeof acc;
      setAcc(a);
    } catch {
      setAcc(null);
    }
    try {
      const s = (await endpoints.solarArray()) as { array?: { declination?: number; azimuth?: number; kwp?: number }; has_key?: boolean };
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
      };
      setLoadMode(ls.mode === "scheduled" ? "scheduled" : "historical");
      setSleepStart(ls.sleep_start || "");
      setSleepEnd(ls.sleep_end || "");
      setWindows(ls.windows || []);
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, deviceSn]);

  async function useGps() {
    setMsg(null);
    const perm = await Location.requestForegroundPermissionsAsync();
    if (!perm.granted) {
      setMsg("Location permission denied");
      return;
    }
    const pos = await Location.getCurrentPositionAsync({});
    await endpoints.setLocation(pos.coords.latitude, pos.coords.longitude, "Current location");
    await load();
  }

  async function search() {
    if (q.trim().length < 2) return;
    const r = (await endpoints.geocode(q.trim())) as { results?: typeof results };
    setResults(r.results || (Array.isArray(r) ? (r as typeof results) : []));
  }

  async function saveCoords() {
    await endpoints.setLocation(Number(lat), Number(lon));
    await load();
  }

  const hours = fc?.forecast || [];
  const configured = !!(loc?.latitude && loc?.longitude);

  return (
    <Screen>
      {!configured ? (
        <Card>
          <Eyebrow>Allow location to enable forecasts</Eyebrow>
          <Hint>
            Open-Meteo + Forecast.Solar need an approximate location. Use GPS or search by city.
          </Hint>
          <Btn title="Use my location" onPress={() => void useGps()} />
        </Card>
      ) : (
        <Hint>
          {loc?.label || `${loc?.latitude?.toFixed(3)}, ${loc?.longitude?.toFixed(3)}`}
        </Hint>
      )}
      {msg ? <Hint>{msg}</Hint> : null}

      <Card>
        <Eyebrow>Set location</Eyebrow>
        <Field label="City search" value={q} onChangeText={setQ} onSubmitEditing={() => void search()} />
        <Btn title="Search" kind="ghost" onPress={() => void search()} />
        {results.map((r, i) => (
          <Btn
            key={i}
            title={r.name}
            kind="ghost"
            onPress={() => void endpoints.setLocation(r.latitude, r.longitude, r.name).then(load)}
          />
        ))}
        <View style={{ flexDirection: "row", gap: 8 }}>
          <View style={{ flex: 1 }}>
            <Field label="Lat" value={lat} onChangeText={setLat} keyboardType="decimal-pad" />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="Lon" value={lon} onChangeText={setLon} keyboardType="decimal-pad" />
          </View>
        </View>
        <Btn title="Save coordinates" kind="ghost" onPress={() => void saveCoords()} />
      </Card>

      {configured ? (
        <Card>
          <Eyebrow>Solar array (Forecast.Solar)</Eyebrow>
          <Hint>Tilt 0–90°, azimuth −180…180 (0 = south). Open-Meteo fills days the estimate plan does not cover.</Hint>
          <View style={{ flexDirection: "row", gap: 8 }}>
            <View style={{ flex: 1 }}>
              <Field label="Tilt" value={dec} onChangeText={setDec} keyboardType="number-pad" />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="Azimuth" value={az} onChangeText={setAz} keyboardType="numbers-and-punctuation" />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="kWp" value={kwp} onChangeText={setKwp} keyboardType="decimal-pad" />
            </View>
          </View>
          <Btn
            title="Infer from history"
            kind="ghost"
            onPress={() =>
              void endpoints.inferSolarArray(activeSn()).then((j: {
                declination?: number;
                azimuth?: number;
                kwp?: number;
                notes?: string;
              }) => {
                if (j.declination != null) setDec(String(j.declination));
                if (j.azimuth != null) setAz(String(j.azimuth));
                if (j.kwp != null) setKwp(String(j.kwp));
                setMsg(j.notes || "Filled from history — save to apply.");
              }).catch((e: unknown) => {
                setMsg(e instanceof Error ? e.message : "Infer failed");
              })
            }
          />
          <Btn
            title="Save array"
            onPress={() =>
              void endpoints.setSolarArray(Number(dec), Number(az), Number(kwp)).then(load)
            }
          />
          <Field label="API key (optional)" value={fsKey} onChangeText={setFsKey} />
          <Hint>{hasFsKey ? "Key saved" : "Public estimate URL (no key)"}</Hint>
          <Btn
            title="Save key"
            kind="ghost"
            onPress={() =>
              void endpoints.setSolarKey(fsKey).then(() => {
                setFsKey("");
                void load();
              })
            }
          />
        </Card>
      ) : null}

      {configured ? (
        <Card>
          <Eyebrow>Expected load</Eyebrow>
          <Segmented
            options={[
              { id: "historical", label: "Historical" },
              { id: "scheduled", label: "Scheduled" },
            ]}
            value={loadMode}
            onChange={setLoadMode}
          />
          <Hint>0 W (or uncovered hours) means the inverter is off — no parasitic. Sleep zeros idle even when a window has watts.</Hint>
          <View style={{ flexDirection: "row", gap: 8 }}>
            <View style={{ flex: 1 }}>
              <Field label="Sleep start" value={sleepStart} onChangeText={setSleepStart} placeholder="23:00" />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="Sleep end" value={sleepEnd} onChangeText={setSleepEnd} placeholder="07:00" />
            </View>
          </View>
          {windows.map((w, i) => (
            <Hint key={w.id || i}>
              {w.start}–{w.end} · {w.watts} W · {w.days || "all"}
            </Hint>
          ))}
          <View style={{ flexDirection: "row", gap: 8 }}>
            <View style={{ flex: 1 }}>
              <Field label="Start" value={wStart} onChangeText={setWStart} />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="End" value={wEnd} onChangeText={setWEnd} />
            </View>
            <View style={{ flex: 1 }}>
              <Field label="W" value={wWatts} onChangeText={setWWatts} keyboardType="number-pad" />
            </View>
          </View>
          <Btn
            title="Add window"
            kind="ghost"
            onPress={() =>
              setWindows((prev) => [
                ...prev,
                { start: wStart, end: wEnd, watts: Number(wWatts) || 0, days: "all" },
              ])
            }
          />
          <Btn
            title="Save load settings"
            onPress={() =>
              void endpoints
                .setLoadSchedule({
                  device_sn: activeSn(),
                  mode: loadMode,
                  sleep_start: sleepStart || null,
                  sleep_end: sleepEnd || null,
                  windows,
                })
                .then(load)
            }
          />
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
                .then(load)
            }
          />
        </Card>
      ) : null}

      <Card>
        <Eyebrow>State of charge — next 5 days</Eyebrow>
        {fc && fc.ready === false ? <Hint>Calibrating — not enough history yet.</Hint> : null}
        <LineChart
          height={200}
          series={[
            {
              id: "solar",
              label: "Solar",
              color: colors.solar,
              unit: "W",
              values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.solar_w ?? 0) })),
            },
            {
              id: "load",
              label: "Load",
              color: colors.accent,
              unit: "W",
              values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.load_w ?? 0) })),
            },
            {
              id: "soc",
              label: "SOC",
              color: colors.accent3,
              axis: "right",
              dashed: true,
              unit: "%",
              min: 0,
              max: 100,
              values: hours.map((h, i) => ({ x: h.ts ?? i, y: Number(h.predicted_soc ?? 0) })),
            },
          ]}
        />
      </Card>

      <Kpi label="Battery capacity" value={fc?.capacity_wh != null ? String(Math.round(fc.capacity_wh)) : "—"} unit="Wh" />
      <Kpi
        label="Solar coefficient"
        value={fc?.solar_coefficient != null ? fc.solar_coefficient.toFixed(3) : "—"}
        sub={fc?.solar_source === "forecast_solar" ? "Forecast.Solar" : fc?.solar_source === "mixed" ? "Forecast.Solar + Open-Meteo" : "W per W/m²"}
      />
      <Kpi
        label="Avg load"
        value={fc?.overall_load_w != null ? String(Math.round(fc.overall_load_w)) : "—"}
        unit="W"
        sub={
          fc?.parasitic_w != null
            ? (fc.pack_baseline_w
              ? `idle ${Math.round(fc.parasitic_w)} W when inverter on · +${Math.round(fc.pack_baseline_w)} W packs`
              : `idle ${Math.round(fc.parasitic_w)} W when inverter on`)
            : undefined
        }
      />

      {fc?.today_budget ? (
        <Card>
          <Eyebrow>Today's energy budget</Eyebrow>
          <Hint>
            Solar {fc.today_budget.solar_kwh?.toFixed(2)} kWh · drain {fc.today_budget.load_kwh?.toFixed(2)} · net{" "}
            {fc.today_budget.net_kwh?.toFixed(2)}
          </Hint>
          {(fc.today_budget.hours || []).slice(0, 16).map((h) => (
            <Text key={h.hour} style={{ color: colors.textDim, fontSize: 12 }}>
              {h.hour}  ☀{Math.round(h.solar_w)}  load {Math.round(h.load_w)}  SOC {h.soc?.toFixed?.(0) ?? h.soc}
            </Text>
          ))}
        </Card>
      ) : null}

      <Card>
        <Eyebrow>Sunset / sunrise accuracy</Eyebrow>
        {(acc?.rows || []).slice(0, 14).map((d) => (
          <Hint key={d.date}>
            {d.date} · sunset {d.sunset_pred ?? "—"}→{d.sunset_actual ?? "—"} · sunrise {d.sunrise_pred ?? "—"}→
            {d.sunrise_actual ?? "—"}
          </Hint>
        ))}
        {!acc?.rows?.length ? <Hint>No daily summary rows yet.</Hint> : null}
      </Card>
    </Screen>
  );
}
