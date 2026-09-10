import { File, Paths } from "expo-file-system";
import * as Sharing from "expo-sharing";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Text, View } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import type { DailyRow } from "../../../src/api/types";
import { LineChart } from "../../../src/components/LineChart";
import { Btn, Card, Eyebrow, Hint, Kpi, Screen, Segmented } from "../../../src/components/ui";
import { fmtKwh, money } from "../../../src/lib/format";
import { useLive } from "../../../src/store/live";
import { colors } from "../../../src/theme";

const RANGES = [
  { id: "6", label: "6h" },
  { id: "24", label: "24h" },
  { id: "168", label: "7d" },
  { id: "720", label: "30d" },
  { id: "2160", label: "90d" },
  { id: "8760", label: "1y" },
];

export default function EnergyScreen() {
  const energy = useLive((s) => s.status?.energy);
  const [hours, setHours] = useState("24");
  const [hist, setHist] = useState<{ ts?: number; output_wh?: number; input_wh?: number; battery_pct?: number }[]>([]);
  const [devices, setDevices] = useState<{ device_sn: string; name?: string; totals?: Record<string, number> }[]>([]);
  const [daily, setDaily] = useState<DailyRow[]>([]);
  const [dRange, setDRange] = useState("90");

  const load = useCallback(async () => {
    const sn = activeSn();
    try {
      const h = (await endpoints.energyHistory(Number(hours), sn)) as { history?: typeof hist };
      setHist(h.history || (Array.isArray(h) ? (h as typeof hist) : []));
    } catch {
      setHist([]);
    }
    try {
      const d = (await endpoints.energyDevices()) as { devices?: typeof devices };
      setDevices(d.devices || []);
    } catch {
      /* ignore */
    }
    if (sn) {
      try {
        const r = await endpoints.energyDaily(sn, 365);
        setDaily(r.daily || []);
      } catch {
        setDaily([]);
      }
    }
  }, [hours]);

  useEffect(() => {
    void load();
  }, [load]);

  const filteredDaily = useMemo(() => {
    const n = Number(dRange);
    return daily.slice(-n);
  }, [daily, dRange]);

  const records = useMemo(() => {
    if (!daily.length) return null;
    const maxSolar = daily.reduce((a, b) => (b.solar_kwh > a.solar_kwh ? b : a));
    const maxLoad = daily.reduce((a, b) => (b.consumed_kwh > a.consumed_kwh ? b : a));
    return { maxSolar, maxLoad };
  }, [daily]);

  async function exportCsv() {
    const header = "date,solar_kwh,consumed_kwh,charged_kwh,grid_kwh,diverted_kwh,peak_solar_w,peak_output_w,min_soc,max_soc";
    const rows = filteredDaily.map((r) =>
      [r.date, r.solar_kwh, r.consumed_kwh, r.charged_kwh, r.grid_kwh, r.diverted_kwh, r.peak_solar_w, r.peak_output_w, r.min_soc, r.max_soc].join(","),
    );
    const file = new File(Paths.cache, "energy-daily.csv");
    file.create({ overwrite: true });
    file.write([header, ...rows].join("\n"));
    if (await Sharing.isAvailableAsync()) await Sharing.shareAsync(file.uri);
  }

  return (
    <Screen>
      <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
        <View style={{ flex: 1, minWidth: 150 }}>
          <Kpi label="Today" value={fmtKwh(energy?.today_consumed_wh)} unit="kWh" sub={`charged ${fmtKwh(energy?.today_charged_wh)}`} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <Kpi label="7 days" value={fmtKwh(energy?.d7_consumed_wh)} unit="kWh" sub={`charged ${fmtKwh(energy?.d7_charged_wh)}`} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <Kpi label="30 days" value={fmtKwh(energy?.d30_consumed_wh)} unit="kWh" sub={`charged ${fmtKwh(energy?.d30_charged_wh)}`} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <Kpi
            label="Lifetime"
            value={fmtKwh(energy?.life_consumed_wh)}
            unit="kWh"
            sub={energy?.life_net_savings != null ? `net ${money(energy.life_net_savings, energy.currency)}` : undefined}
          />
        </View>
      </View>

      <Card>
        <Eyebrow>Energy history</Eyebrow>
        <Segmented options={RANGES} value={hours} onChange={setHours} />
        <LineChart
          series={[
            {
              id: "out",
              color: colors.accent,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.output_wh ?? 0) })),
            },
            {
              id: "in",
              color: colors.accent2,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.input_wh ?? 0) })),
            },
          ]}
        />
      </Card>

      {devices.length > 1 ? (
        <Card>
          <Eyebrow>All devices</Eyebrow>
          {devices.map((d) => (
            <Hint key={d.device_sn}>
              {d.name || d.device_sn} · today {fmtKwh(d.totals?.today_consumed_wh)} kWh
            </Hint>
          ))}
        </Card>
      ) : null}

      {records ? (
        <Card>
          <Eyebrow>Records</Eyebrow>
          <Hint>Best solar day: {records.maxSolar.date} · {records.maxSolar.solar_kwh.toFixed(2)} kWh</Hint>
          <Hint>Highest consumption: {records.maxLoad.date} · {records.maxLoad.consumed_kwh.toFixed(2)} kWh</Hint>
        </Card>
      ) : null}

      <Card>
        <Eyebrow>Daily breakdown</Eyebrow>
        <Segmented
          options={[
            { id: "30", label: "30d" },
            { id: "90", label: "90d" },
            { id: "180", label: "180d" },
            { id: "365", label: "365d" },
          ]}
          value={dRange}
          onChange={setDRange}
        />
        {filteredDaily.slice().reverse().slice(0, 31).map((r) => (
          <View key={r.date} style={{ flexDirection: "row", justifyContent: "space-between" }}>
            <Text style={{ color: colors.text, fontSize: 12 }}>{r.date}</Text>
            <Text style={{ color: colors.textDim, fontSize: 12 }}>
              ☀{r.solar_kwh.toFixed(1)} · {r.consumed_kwh.toFixed(1)} out · SOC {r.min_soc}–{r.max_soc}
            </Text>
          </View>
        ))}
        <Btn title="Export CSV" kind="ghost" onPress={() => void exportCsv()} />
      </Card>
    </Screen>
  );
}
