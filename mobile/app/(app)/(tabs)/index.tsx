import { useEffect, useState } from "react";
import { Pressable, Text, View } from "react-native";
import { endpoints } from "../../../src/api/client";
import { reconnectLive } from "../../../src/api/ws";
import { LineChart } from "../../../src/components/LineChart";
import { PowerFlow } from "../../../src/components/PowerFlow";
import { Btn, Card, Eyebrow, Hint, Pill, Screen } from "../../../src/components/ui";
import { etaLabel, fmt, fmtKwh, fmtTemp, headlineSoc } from "../../../src/lib/format";
import { useLive } from "../../../src/store/live";
import { usePrefs } from "../../../src/store/prefs";
import { colors } from "../../../src/theme";

function portOn(v: unknown): boolean {
  return v === true || v === 1 || v === "1";
}

export default function LiveScreen() {
  const status = useLive((s) => s.status);
  const connected = useLive((s) => s.connected);
  const alerts = useLive((s) => s.alerts);
  const tempUnit = usePrefs((s) => s.tempUnit);
  const [busyPort, setBusyPort] = useState<string | null>(null);
  const [packsOpen, setPacksOpen] = useState(false);
  const [chargeHost, setChargeHost] = useState<string | null>(null);
  const [divertHost, setDivertHost] = useState<string | null>(null);
  const [chargeOn, setChargeOn] = useState<boolean | null>(null);
  const [divertOn, setDivertOn] = useState<boolean | null>(null);

  const t = status?.telemetry;
  const soc = headlineSoc(t);
  const devices = status?.cloud?.devices_overview || [];
  const selected = status?.cloud?.selected_device_id;
  const energy = status?.energy;
  const packs = status?.battery_packs || [];
  const hist = status?.history || [];
  const conn = status?.connection_status || (connected ? "connected" : "disconnected");
  const sn = status?.device?.device_sn as string | undefined;
  const watchdog = status?.inverter_watchdog as { active?: boolean; message?: string } | null;

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const sc = (await endpoints.smartConfig(sn)) as { config?: { kasa_device_host?: string } };
        const host = sc.config?.kasa_device_host || null;
        if (!cancelled) setChargeHost(host);
        if (host) {
          const st = (await endpoints.kasaStatus(host)) as { is_on?: boolean };
          if (!cancelled) setChargeOn(!!st.is_on);
        }
      } catch {
        if (!cancelled) setChargeHost(null);
      }
      try {
        const sol = (await endpoints.solarConfig(sn)) as { config?: { kasa_device_host?: string } };
        const host = sol.config?.kasa_device_host || null;
        if (!cancelled) setDivertHost(host);
        if (host) {
          const st = (await endpoints.kasaStatus(host)) as { is_on?: boolean };
          if (!cancelled) setDivertOn(!!st.is_on);
        }
      } catch {
        if (!cancelled) setDivertHost(null);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [sn]);

  async function pickDevice(id: string) {
    useLive.getState().setViewDeviceId(id);
    try {
      await endpoints.selectView(id);
    } catch {
      /* still reconnect so WS picks up the query param */
    }
    reconnectLive();
  }

  async function toggle(port: string, on: boolean) {
    setBusyPort(port);
    try {
      await endpoints.setOutput(port, on, status?.device?.device_sn as string | undefined);
    } finally {
      setBusyPort(null);
    }
  }

  return (
    <Screen>
      {alerts[0] ? (
        <Card>
          <Text style={{ color: colors.danger }}>{alerts[0].message}</Text>
          <Btn title="Dismiss" kind="ghost" onPress={() => useLive.getState().dismissAlert()} />
        </Card>
      ) : null}

      {devices.length > 1 ? (
        <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
          {devices.map((d) => {
            const on = String(d.device_id) === String(selected);
            return (
              <Pressable
                key={d.device_id}
                onPress={() => pickDevice(d.device_id)}
                style={{
                  padding: 10,
                  borderRadius: 12,
                  borderWidth: 1,
                  borderColor: on ? colors.accent : colors.border,
                  backgroundColor: colors.bgElev,
                  minWidth: 120,
                }}
              >
                <Text style={{ color: colors.text, fontWeight: "600" }}>{d.name || d.device_sn}</Text>
                <Text style={{ color: colors.textDim, fontSize: 12 }}>
                  {d.soc_pct != null ? `${Math.round(d.soc_pct)}%` : "—"} · {Math.round(d.solar_w || 0)}W in
                </Text>
              </Pressable>
            );
          })}
        </View>
      ) : null}

      <Card>
        <View style={{ flexDirection: "row", justifyContent: "space-between", alignItems: "center" }}>
          <Eyebrow>State of charge</Eyebrow>
          <Pill
            label={conn}
            tone={conn === "connected" ? "ok" : conn === "error" ? "err" : "warn"}
          />
        </View>
        <Text style={{ color: colors.text, fontSize: 48, fontWeight: "700" }}>
          {soc == null ? "—" : Math.round(soc)}
          <Text style={{ fontSize: 20, color: colors.textDim }}>%</Text>
        </Text>
        <View style={{ height: 8, backgroundColor: colors.bgElev2, borderRadius: 99, overflow: "hidden" }}>
          <View
            style={{
              width: `${Math.max(0, Math.min(100, soc ?? 0))}%`,
              height: "100%",
              backgroundColor: (soc ?? 0) < 20 ? colors.danger : colors.accent,
            }}
          />
        </View>
        <Hint>
          {etaLabel(t)}
          {t?.system_soc_pct == null && t?.battery_temp_c != null
            ? ` · ${fmtTemp(t.battery_temp_c, tempUnit)}`
            : ""}
        </Hint>
      </Card>

      <Card>
        <Eyebrow>Power flow</Eyebrow>
        <PowerFlow t={t} />
      </Card>

      <Card>
        <Eyebrow>Today</Eyebrow>
        <Text style={{ color: colors.text, fontSize: 22, fontWeight: "700" }}>
          {fmtKwh(energy?.today_consumed_wh)} kWh consumed
        </Text>
        <Hint>{fmtKwh(energy?.today_charged_wh)} kWh charged</Hint>
        {(energy?.today_solar_wh || 0) > 0 && (energy?.today_grid_wh || 0) > 0 ? (
          <Hint>
            ☀ {fmtKwh(energy?.today_solar_wh)} solar · ⚡ {fmtKwh(energy?.today_grid_wh)} AC
          </Hint>
        ) : null}
        {(energy?.today_diverted_wh || 0) > 0 ? (
          <Hint>{fmtKwh(energy?.today_diverted_wh)} kWh diverted</Hint>
        ) : null}
      </Card>

      <Card>
        <View style={{ flexDirection: "row", justifyContent: "space-between" }}>
          <Eyebrow>Power</Eyebrow>
          <Hint>Tap to toggle</Hint>
        </View>
        <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
          {(["ac", "dc", "usb", "car"] as const).map((port) => {
            const on = portOn(t?.[`${port}_on`]);
            return (
              <Pressable
                key={port}
                onPress={() => toggle(port, !on)}
                disabled={busyPort === port}
                style={{
                  paddingVertical: 12,
                  paddingHorizontal: 16,
                  borderRadius: 12,
                  backgroundColor: on ? "rgba(74,222,128,0.18)" : colors.bgElev2,
                  borderWidth: 1,
                  borderColor: on ? colors.accent : colors.border,
                  minWidth: 72,
                  alignItems: "center",
                }}
              >
                <Text style={{ color: colors.textDim, fontSize: 11 }}>{port.toUpperCase()}</Text>
                <Text style={{ color: on ? colors.accent : colors.text, fontWeight: "700" }}>
                  {on ? "ON" : "OFF"}
                </Text>
              </Pressable>
            );
          })}
          {chargeHost ? (
            <Pressable
              onPress={() =>
                void endpoints.kasaTest(chargeHost, !chargeOn).then(() => setChargeOn(!chargeOn))
              }
              style={{
                paddingVertical: 12,
                paddingHorizontal: 16,
                borderRadius: 12,
                backgroundColor: chargeOn ? "rgba(251,191,36,0.18)" : colors.bgElev2,
                borderWidth: 1,
                borderColor: chargeOn ? colors.accent3 : colors.border,
                minWidth: 88,
                alignItems: "center",
              }}
            >
              <Text style={{ color: colors.textDim, fontSize: 11 }}>AC CHARGE</Text>
              <Text style={{ color: chargeOn ? colors.accent3 : colors.text, fontWeight: "700" }}>
                {chargeOn ? "ON" : "OFF"}
              </Text>
            </Pressable>
          ) : null}
          {divertHost ? (
            <Pressable
              onPress={() =>
                void endpoints.kasaTest(divertHost, !divertOn).then(() => setDivertOn(!divertOn))
              }
              style={{
                paddingVertical: 12,
                paddingHorizontal: 16,
                borderRadius: 12,
                backgroundColor: divertOn ? "rgba(56,189,248,0.18)" : colors.bgElev2,
                borderWidth: 1,
                borderColor: divertOn ? colors.accent2 : colors.border,
                minWidth: 88,
                alignItems: "center",
              }}
            >
              <Text style={{ color: colors.textDim, fontSize: 11 }}>EXCESS</Text>
              <Text style={{ color: divertOn ? colors.accent2 : colors.text, fontWeight: "700" }}>
                {divertOn ? "ON" : "OFF"}
              </Text>
            </Pressable>
          ) : null}
        </View>
      </Card>

      {watchdog?.active ? (
        <Card>
          <Eyebrow>Inverter watchdog</Eyebrow>
          <Hint>{watchdog.message || "AC trip recovery is active."}</Hint>
          <Btn title="Dismiss" kind="ghost" onPress={() => void endpoints.dismissWatchdog()} />
        </Card>
      ) : null}

      {packs.length > 0 ? (
        <Card>
          <Pressable onPress={() => setPacksOpen((v) => !v)}>
            <Eyebrow>Battery packs · {packs.length}</Eyebrow>
          </Pressable>
          {packsOpen
            ? packs.map((p, i) => (
                <Hint key={i}>
                  {String(p.alias || p.name || `Pack ${i + 1}`)} · SOC {fmt(p.rb as number, 0)}%
                  {p.input_w != null ? ` · ${fmt(p.input_w as number, 0)} W` : ""}
                  {p.temp != null ? ` · ${fmtTemp(p.temp as number, tempUnit)}` : ""}
                </Hint>
              ))
            : null}
        </Card>
      ) : null}

      <Card>
        <Eyebrow>Power last 6 hours</Eyebrow>
        <LineChart
          series={[
            {
              id: "soc",
              color: colors.accent3,
              min: 0,
              max: 100,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.battery_percent ?? 0) })),
            },
            {
              id: "out",
              color: colors.accent,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.output_power_w ?? 0) })),
            },
            {
              id: "in",
              color: colors.accent2,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.input_power_w ?? 0) })),
            },
          ]}
        />
      </Card>
    </Screen>
  );
}
