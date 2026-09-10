import * as Location from "expo-location";
import { useCallback, useEffect, useState } from "react";
import { Text, View } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import { LineChart } from "../../../src/components/LineChart";
import { Btn, Card, Eyebrow, Field, Hint, Kpi, Screen } from "../../../src/components/ui";
import { colors } from "../../../src/theme";

type ForecastHour = {
  ts?: number;
  predicted_soc?: number;
  load_w?: number;
  solar_w?: number;
};

export default function ForecastScreen() {
  const [loc, setLoc] = useState<{ latitude?: number; longitude?: number; label?: string } | null>(null);
  const [fc, setFc] = useState<{
    ready?: boolean;
    forecast?: ForecastHour[];
    capacity_wh?: number;
    solar_coeff?: number;
    avg_load_w?: number;
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
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

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
            Open-Meteo solar irradiance needs an approximate location. Use GPS or search by city.
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
      <Kpi label="Solar coefficient" value={fc?.solar_coeff != null ? fc.solar_coeff.toFixed(3) : "—"} sub="W per W/m²" />
      <Kpi label="Avg load" value={fc?.avg_load_w != null ? String(Math.round(fc.avg_load_w)) : "—"} unit="W" />

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
