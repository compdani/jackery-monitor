import { useEffect, useRef, useState } from "react";
import { ActivityIndicator, Animated, Pressable, Text, View } from "react-native";
import { endpoints } from "../../../src/api/client";
import { reconnectLive } from "../../../src/api/ws";
import { LineChart } from "../../../src/components/LineChart";
import { PowerFlow } from "../../../src/components/PowerFlow";
import { Btn, Card, EnergyKpi, Eyebrow, Hint, Pill, Screen } from "../../../src/components/ui";
import { etaLabel, fmtKwh, fmtTemp, headlineSoc } from "../../../src/lib/format";
import { useLive } from "../../../src/store/live";
import { usePrefs } from "../../../src/store/prefs";
import { colors } from "../../../src/theme";

const PENDING_TOGGLE_MS = 30000;

function portOn(v: unknown): boolean {
  return v === true || v === 1 || v === "1";
}

function portFlag(v: unknown): boolean | null {
  if (v === true || v === 1 || v === "1") return true;
  if (v === false || v === 0 || v === "0") return false;
  return null;
}

type PendingToggle = { expected: boolean; until: number; inFlight: boolean };

function PendingPulse({ active, color }: { active: boolean; color: string }) {
  const opacity = useRef(new Animated.Value(0)).current;
  useEffect(() => {
    if (!active) {
      opacity.setValue(0);
      return;
    }
    opacity.setValue(0.95);
    const anim = Animated.loop(
      Animated.sequence([
        Animated.timing(opacity, { toValue: 0.25, duration: 650, useNativeDriver: true }),
        Animated.timing(opacity, { toValue: 0.95, duration: 650, useNativeDriver: true }),
      ]),
    );
    anim.start();
    return () => anim.stop();
  }, [active, opacity]);
  if (!active) return null;
  return (
    <Animated.View
      pointerEvents="none"
      style={{
        position: "absolute",
        top: -2,
        left: -2,
        right: -2,
        bottom: -2,
        borderRadius: 14,
        borderWidth: 2,
        borderColor: color,
        opacity,
      }}
    />
  );
}

function PowerChip({
  label,
  on,
  inFlight,
  pending,
  onPress,
  accent = colors.accent,
  onBg = "rgba(74,222,128,0.18)",
  minWidth = 72,
  inFlightShowsValue = false,
}: {
  label: string;
  on: boolean;
  inFlight?: boolean;
  pending?: boolean;
  onPress: () => void;
  accent?: string;
  onBg?: string;
  minWidth?: number;
  inFlightShowsValue?: boolean;
}) {
  const waiting = !!(inFlight || pending);
  return (
    <Pressable
      onPress={onPress}
      disabled={!!inFlight}
      style={({ pressed }) => ({
        paddingVertical: 12,
        paddingHorizontal: 16,
        borderRadius: 12,
        backgroundColor: on ? onBg : colors.bgElev2,
        borderWidth: 1,
        borderColor: waiting ? accent : on ? accent : colors.border,
        minWidth,
        alignItems: "center",
        opacity: pressed ? 0.65 : 1,
        transform: [{ scale: pressed ? 0.96 : 1 }],
      })}
    >
      <PendingPulse active={waiting} color={accent} />
      <Text style={{ color: colors.textDim, fontSize: 11 }}>{label}</Text>
      {inFlight && !inFlightShowsValue ? (
        <View style={{ flexDirection: "row", alignItems: "center", gap: 6, minHeight: 20 }}>
          <ActivityIndicator size="small" color={accent} />
          <Text style={{ color: accent, fontWeight: "700" }}>…</Text>
        </View>
      ) : (
        <View style={{ flexDirection: "row", alignItems: "center", gap: 6, minHeight: 20 }}>
          {inFlight ? <ActivityIndicator size="small" color={accent} /> : null}
          <Text style={{ color: on ? accent : colors.text, fontWeight: "700" }}>{on ? "ON" : "OFF"}</Text>
        </View>
      )}
      {pending && !inFlight ? (
        <Text style={{ color: accent, fontSize: 10, fontWeight: "600" }}>pending</Text>
      ) : null}
    </Pressable>
  );
}

type Pack = {
  rb?: number | null;
  ip?: number | null;
  op?: number | null;
  it?: number | null;
  deviceSn?: string;
  deviceOrder?: number;
  needUpgrade?: boolean;
};

function packFlow(p: Pack): string {
  const ip = Math.round(Number(p.ip ?? 0));
  const op = Math.round(Number(p.op ?? 0));
  if (ip > 0) return `+${ip}W`;
  if (op > 0) return `−${op}W`;
  return "idle";
}

function PackRow({
  idx,
  soc,
  meta,
  isMain,
}: {
  idx: string;
  soc: number | null;
  meta?: string;
  isMain?: boolean;
}) {
  const pct = soc == null || !Number.isFinite(soc) ? 0 : Math.max(0, Math.min(100, soc));
  return (
    <View style={{ gap: 4 }}>
      <View style={{ flexDirection: "row", alignItems: "center", gap: 8 }}>
        <Text
          style={{
            color: isMain ? colors.accent2 : colors.textMute,
            width: 28,
            fontSize: 12,
            fontWeight: "700",
            textAlign: "center",
          }}
        >
          {idx}
        </Text>
        <View
          style={{
            flex: 1,
            height: 8,
            borderRadius: 4,
            backgroundColor: colors.border,
            overflow: "hidden",
          }}
        >
          <View
            style={{
              width: `${pct}%`,
              height: "100%",
              backgroundColor: colors.accent,
              borderRadius: 4,
            }}
          />
        </View>
        <Text style={{ color: colors.text, fontWeight: "700", width: 44, textAlign: "right", fontSize: 14 }}>
          {soc == null ? "—" : `${Math.round(soc)}%`}
        </Text>
      </View>
      {meta ? (
        <Text style={{ color: colors.textMute, fontSize: 11, paddingLeft: 36 }}>{meta}</Text>
      ) : null}
    </View>
  );
}

export default function LiveScreen() {
  const status = useLive((s) => s.status);
  const connected = useLive((s) => s.connected);
  const alerts = useLive((s) => s.alerts);
  const tempUnit = usePrefs((s) => s.tempUnit);
  const [pending, setPending] = useState<Record<string, PendingToggle>>({});
  const [toggleErr, setToggleErr] = useState<string | null>(null);
  const [kasaBusy, setKasaBusy] = useState<"charge" | "divert" | null>(null);
  const [chargeHost, setChargeHost] = useState<string | null>(null);
  const [divertHost, setDivertHost] = useState<string | null>(null);
  const [chargeOn, setChargeOn] = useState<boolean | null>(null);
  const [divertOn, setDivertOn] = useState<boolean | null>(null);

  const t = status?.telemetry;
  const soc = headlineSoc(t);
  const devices = status?.cloud?.devices_overview || [];
  const selected = status?.cloud?.selected_device_id;
  const energy = status?.energy;
  const packs = (status?.battery_packs || []) as Pack[];
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

  useEffect(() => {
    setPending({});
    setToggleErr(null);
  }, [sn]);

  useEffect(() => {
    const now = Date.now();
    setPending((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const port of Object.keys(next)) {
        const p = next[port];
        if (p.inFlight) continue;
        const live = portFlag(t?.[`${port}_on`]);
        if (now > p.until || live === p.expected) {
          delete next[port];
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [t?.ac_on, t?.dc_on, t?.usb_on, t?.car_on]);

  useEffect(() => {
    const waits = Object.entries(pending).filter(([, p]) => !p.inFlight && p.until > Date.now());
    if (!waits.length) return;
    const ms = Math.min(...waits.map(([, p]) => p.until - Date.now())) + 25;
    const id = setTimeout(() => {
      const now = Date.now();
      setPending((prev) => {
        let changed = false;
        const next = { ...prev };
        for (const port of Object.keys(next)) {
          if (!next[port].inFlight && next[port].until <= now) {
            delete next[port];
            changed = true;
          }
        }
        return changed ? next : prev;
      });
    }, Math.max(ms, 0));
    return () => clearTimeout(id);
  }, [pending]);

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
    setToggleErr(null);
    setPending((prev) => ({
      ...prev,
      [port]: { expected: on, until: Date.now() + PENDING_TOGGLE_MS, inFlight: true },
    }));
    try {
      await endpoints.setOutput(port, on, status?.device?.device_sn as string | undefined);
      setPending((prev) => ({
        ...prev,
        [port]: { expected: on, until: Date.now() + PENDING_TOGGLE_MS, inFlight: false },
      }));
    } catch (e: unknown) {
      setPending((prev) => {
        const next = { ...prev };
        delete next[port];
        return next;
      });
      setToggleErr(e instanceof Error ? e.message : `Failed to toggle ${port.toUpperCase()}`);
    }
  }

  async function toggleKasa(kind: "charge" | "divert", host: string, current: boolean | null) {
    const next = !current;
    setToggleErr(null);
    setKasaBusy(kind);
    if (kind === "charge") setChargeOn(next);
    else setDivertOn(next);
    try {
      await endpoints.kasaTest(host, next);
    } catch (e: unknown) {
      if (kind === "charge") setChargeOn(current);
      else setDivertOn(current);
      setToggleErr(e instanceof Error ? e.message : `Failed to toggle ${kind}`);
    } finally {
      setKasaBusy(null);
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

      <EnergyKpi
        label="Today"
        consumed={fmtKwh(energy?.today?.output_wh)}
        charged={fmtKwh(energy?.today?.input_wh)}
      >
        {(energy?.today?.solar_wh || 0) > 0 && (energy?.today?.ac_input_wh || 0) > 0 ? (
          <Hint>
            ☀ {fmtKwh(energy?.today?.solar_wh)} solar · ⚡ {fmtKwh(energy?.today?.ac_input_wh)} AC
          </Hint>
        ) : null}
        {(energy?.today?.solar_charge_diverted_wh || 0) > 0 ? (
          <Hint>{fmtKwh(energy?.today?.solar_charge_diverted_wh)} kWh diverted</Hint>
        ) : null}
      </EnergyKpi>

      <Card>
        <View style={{ flexDirection: "row", justifyContent: "space-between" }}>
          <Eyebrow>Power</Eyebrow>
          <Hint>Tap to toggle</Hint>
        </View>
        <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
          {(["ac", "dc", "usb", "car"] as const).map((port) => {
            const liveOn = portOn(t?.[`${port}_on`]);
            const pend = pending[port];
            const inFlight = !!pend?.inFlight;
            const waiting = !!pend && !pend.inFlight && Date.now() <= pend.until;
            const on = pend && Date.now() <= pend.until ? pend.expected : liveOn;
            return (
              <PowerChip
                key={port}
                label={port.toUpperCase()}
                on={on}
                inFlight={inFlight}
                pending={waiting}
                onPress={() => void toggle(port, !on)}
              />
            );
          })}
          {chargeHost ? (
            <PowerChip
              label="AC CHARGE"
              on={!!chargeOn}
              inFlight={kasaBusy === "charge"}
              inFlightShowsValue
              onPress={() => void toggleKasa("charge", chargeHost, chargeOn)}
              accent={colors.accent3}
              onBg="rgba(251,191,36,0.18)"
              minWidth={88}
            />
          ) : null}
          {divertHost ? (
            <PowerChip
              label="EXCESS"
              on={!!divertOn}
              inFlight={kasaBusy === "divert"}
              inFlightShowsValue
              onPress={() => void toggleKasa("divert", divertHost, divertOn)}
              accent={colors.accent2}
              onBg="rgba(56,189,248,0.18)"
              minWidth={88}
            />
          ) : null}
        </View>
        {toggleErr ? (
          <Text style={{ color: colors.danger, fontSize: 12 }}>{toggleErr}</Text>
        ) : null}
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
          <Eyebrow>Battery packs</Eyebrow>
          <Hint>
            {packs.length} pack{packs.length === 1 ? "" : "s"}
            {soc != null ? ` · system ${Math.round(soc)}%` : ""}
          </Hint>
          <PackRow
            idx="★"
            isMain
            soc={t?.main_soc_pct ?? t?.battery_percent ?? null}
            meta={[
              t?.battery_temp_c != null ? fmtTemp(t.battery_temp_c, tempUnit) : null,
              "Main",
            ]
              .filter(Boolean)
              .join(" · ")}
          />
          {packs.map((p, i) => {
            const order = typeof p.deviceOrder === "number" ? p.deviceOrder : i;
            const sn = String(p.deviceSn || "");
            return (
              <PackRow
                key={sn || i}
                idx={String(order + 1)}
                soc={p.rb ?? null}
                meta={[
                  packFlow(p),
                  p.it != null ? fmtTemp(p.it, tempUnit) : null,
                  sn ? `…${sn.slice(-6)}` : null,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              />
            );
          })}
        </Card>
      ) : null}

      <Card>
        <Eyebrow>Power last 6 hours</Eyebrow>
        <LineChart
          series={[
            {
              id: "out",
              label: "Output",
              color: colors.grid,
              axis: "left",
              unit: "W",
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.output_power_w ?? 0) })),
            },
            {
              id: "in",
              label: "Input",
              color: colors.accent,
              axis: "left",
              unit: "W",
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.input_power_w ?? 0) })),
            },
            {
              id: "soc",
              label: "Battery",
              color: colors.accent3,
              axis: "right",
              dashed: true,
              unit: "%",
              min: 0,
              max: 100,
              values: hist.map((h, i) => ({ x: h.ts ?? i, y: Number(h.battery_percent ?? 0) })),
            },
          ]}
        />
      </Card>
    </Screen>
  );
}
