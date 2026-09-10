import { File, Paths } from "expo-file-system";
import * as Sharing from "expo-sharing";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import type { DailyRow, EnergyTotals, EnergyWindow } from "../../../src/api/types";
import { LineChart } from "../../../src/components/LineChart";
import { Btn, Card, EnergyKpi, Eyebrow, Hint, Screen, Segmented } from "../../../src/components/ui";
import { fmtKwh, money } from "../../../src/lib/format";
import { useLive } from "../../../src/store/live";
import { type EnergyBucketS, usePrefs } from "../../../src/store/prefs";
import { colors } from "../../../src/theme";

const RANGES = [
  { id: "6", label: "6h" },
  { id: "24", label: "24h" },
  { id: "168", label: "7d" },
  { id: "720", label: "30d" },
  { id: "2160", label: "90d" },
  { id: "8760", label: "1y" },
];

const INTERVALS = [
  { id: "900", label: "15m" },
  { id: "1800", label: "30m" },
  { id: "3600", label: "1h" },
];

const ALL = "all";

type WindowKey = "today" | "last_7d" | "last_30d" | "lifetime";
type HistPoint = {
  ts?: number;
  output_wh?: number;
  input_wh?: number;
  battery_pct?: number;
  system_soc?: number;
};

function sumWindow(src: EnergyTotals[], key: WindowKey): EnergyWindow {
  return {
    output_wh: src.reduce((n, d) => n + Number(d[key]?.output_wh ?? 0), 0),
    input_wh: src.reduce((n, d) => n + Number(d[key]?.input_wh ?? 0), 0),
  };
}

function deviceLabel(d: EnergyTotals): string {
  return String(d.name || d.device_sn || "Device").replace(/^Explorer\s+/i, "");
}

function mergeHistories(lists: HistPoint[][]): HistPoint[] {
  const byTs = new Map<number, { output_wh: number; input_wh: number }>();
  for (const list of lists) {
    for (const p of list) {
      const ts = Number(p.ts);
      if (!Number.isFinite(ts)) continue;
      const cur = byTs.get(ts) || { output_wh: 0, input_wh: 0 };
      cur.output_wh += Number(p.output_wh ?? 0);
      cur.input_wh += Number(p.input_wh ?? 0);
      byTs.set(ts, cur);
    }
  }
  return [...byTs.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([ts, v]) => ({ ts, ...v }));
}

export default function EnergyScreen() {
  const energy = useLive((s) => s.status?.energy);
  const energyBucketS = usePrefs((s) => s.energyBucketS);
  const [hours, setHours] = useState("24");
  const [chartSn, setChartSn] = useState(ALL);
  const [hist, setHist] = useState<HistPoint[]>([]);
  const [devices, setDevices] = useState<EnergyTotals[]>([]);
  const [daily, setDaily] = useState<DailyRow[]>([]);
  const [dRange, setDRange] = useState("90");

  const load = useCallback(async () => {
    let list: EnergyTotals[] = [];
    try {
      const d = (await endpoints.energyDevices()) as { devices?: EnergyTotals[] };
      list = d.devices || [];
      setDevices(list);
    } catch {
      /* ignore */
    }

    const viewed = activeSn();
    const sns =
      chartSn === ALL
        ? list.map((d) => d.device_sn).filter((sn): sn is string => !!sn)
        : [chartSn];
    const fetchSns = sns.length ? sns : viewed ? [viewed] : [];
    try {
      const results = await Promise.all(
        fetchSns.map(async (sn) => {
          const h = (await endpoints.energyHistory(Number(hours), sn, energyBucketS)) as { history?: HistPoint[] };
          return h.history || [];
        }),
      );
      setHist(results.length > 1 ? mergeHistories(results) : results[0] || []);
    } catch {
      setHist([]);
    }

    const dailySn = chartSn === ALL ? viewed : chartSn;
    if (dailySn) {
      try {
        const r = await endpoints.energyDaily(dailySn, 365);
        setDaily(r.daily || []);
      } catch {
        setDaily([]);
      }
    }
  }, [hours, chartSn, energyBucketS]);

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

  const totals = useMemo(() => {
    const src = devices.length ? devices : energy ? [energy] : [];
    return {
      today: sumWindow(src, "today"),
      last_7d: sumWindow(src, "last_7d"),
      last_30d: sumWindow(src, "last_30d"),
      lifetime: sumWindow(src, "lifetime"),
    };
  }, [devices, energy]);

  const lifeNet =
    devices.length <= 1 && energy?.lifetime_savings?.net_savings != null
      ? `net ${money(energy.lifetime_savings.net_savings, energy.cost_plan?.currency)}`
      : undefined;

  const showDevicePicker = devices.length > 1;
  const combined = chartSn === ALL;
  const selectedName =
    !combined ? devices.find((d) => d.device_sn === chartSn)?.name || chartSn : null;
  const showBattery =
    !combined && hist.some((h) => (h.system_soc ?? h.battery_pct) != null);

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
          <EnergyKpi label="Today" consumed={fmtKwh(totals.today.output_wh)} charged={fmtKwh(totals.today.input_wh)} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <EnergyKpi label="7 days" consumed={fmtKwh(totals.last_7d.output_wh)} charged={fmtKwh(totals.last_7d.input_wh)} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <EnergyKpi label="30 days" consumed={fmtKwh(totals.last_30d.output_wh)} charged={fmtKwh(totals.last_30d.input_wh)} />
        </View>
        <View style={{ flex: 1, minWidth: 150 }}>
          <EnergyKpi
            label="Lifetime"
            consumed={fmtKwh(totals.lifetime.output_wh)}
            charged={fmtKwh(totals.lifetime.input_wh)}
            sub={lifeNet}
          />
        </View>
      </View>

      <Card>
        <Eyebrow>Energy history</Eyebrow>
        {showDevicePicker ? (
          <Segmented
            options={[
              { id: ALL, label: "All" },
              ...devices.map((d) => ({ id: d.device_sn || deviceLabel(d), label: deviceLabel(d) })),
            ]}
            value={chartSn}
            onChange={setChartSn}
          />
        ) : null}
        <Segmented options={RANGES} value={hours} onChange={setHours} />
        <Segmented
          options={INTERVALS}
          value={String(energyBucketS)}
          onChange={(id) => void usePrefs.getState().setEnergyBucketS(Number(id) as EnergyBucketS)}
        />
        <Hint>{combined ? "Combined across all devices" : selectedName}</Hint>
        <LineChart
          series={[
            {
              id: "out",
              label: "Consumed",
              color: colors.grid,
              unit: "Wh",
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.output_wh ?? 0) })),
            },
            {
              id: "in",
              label: "Charged",
              color: colors.accent,
              unit: "Wh",
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.input_wh ?? 0) })),
            },
            ...(showBattery
              ? [
                  {
                    id: "soc",
                    label: "Battery",
                    color: colors.accent3,
                    axis: "right" as const,
                    dashed: true,
                    unit: "%",
                    min: 0,
                    max: 100,
                    values: hist.map((h, i) => ({
                      x: h.ts ?? i,
                      y: Number(h.system_soc ?? h.battery_pct ?? 0),
                    })),
                  },
                ]
              : []),
          ]}
        />
      </Card>

      {showDevicePicker ? (
        <Card>
          <Eyebrow>All devices</Eyebrow>
          <Hint>Tap a device to filter the chart.</Hint>
          {devices.map((d) => {
            const sn = d.device_sn;
            const on = Boolean(sn && chartSn === sn);
            return (
              <Pressable
                key={sn}
                onPress={() => setChartSn(on ? ALL : sn || ALL)}
                style={{
                  flexDirection: "row",
                  justifyContent: "space-between",
                  gap: 8,
                  paddingVertical: 6,
                  paddingHorizontal: 8,
                  borderRadius: 10,
                  borderWidth: 1,
                  borderColor: on ? colors.accent : colors.border,
                  backgroundColor: on ? "rgba(74,222,128,0.12)" : "transparent",
                }}
              >
                <Hint>{d.name || sn}</Hint>
                <Hint>
                  <Text style={{ color: colors.grid }}>{fmtKwh(d.today?.output_wh)}</Text>
                  {" / "}
                  <Text style={{ color: colors.accent }}>{fmtKwh(d.today?.input_wh)}</Text>
                  {" kWh"}
                </Hint>
              </Pressable>
            );
          })}
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
