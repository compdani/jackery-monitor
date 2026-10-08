import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, Animated, Pressable, Text, View } from "react-native";
import { endpoints } from "../../../src/api/client";
import { reconnectLive } from "../../../src/api/ws";
import type { LinkedEcoflow } from "../../../src/api/types";
import { LineChart } from "../../../src/components/LineChart";
import { PowerFlow } from "../../../src/components/PowerFlow";
import { SiseliLiveControls } from "../../../src/components/SiseliControls";
import {
  Btn,
  Card,
  Collapsible,
  EnergyKpi,
  EodForecastPill,
  Eyebrow,
  Field,
  Hint,
  Pill,
  SavingsRow,
  Screen,
} from "../../../src/components/ui";
import { buildEodForecast, type ForecastPayload } from "../../../src/lib/forecast";
import { etaLabel, fmt, fmtKwh, fmtTemp, headlineSoc } from "../../../src/lib/format";
import { isSiseliView, pinnedControls, siseliDeviceId } from "../../../src/lib/siseli";
import { useLive } from "../../../src/store/live";
import { usePrefs } from "../../../src/store/prefs";
import { colors } from "../../../src/theme";

const EOD_DRIFT_THRESHOLD_PCT = 1;
const EOD_MIN_REFRESH_MS = 5 * 60_000;
const EOD_INTERVAL_MS = 30 * 60_000;

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
  disabled = false,
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
  disabled?: boolean;
  accent?: string;
  onBg?: string;
  minWidth?: number;
  inFlightShowsValue?: boolean;
}) {
  const waiting = !!(inFlight || pending);
  return (
    <Pressable
      onPress={onPress}
      disabled={!!inFlight || disabled}
      style={({ pressed }) => ({
        paddingVertical: 12,
        paddingHorizontal: 16,
        borderRadius: 12,
        backgroundColor: on ? onBg : colors.bgElev2,
        borderWidth: 1,
        borderColor: waiting ? accent : on ? accent : colors.border,
        minWidth,
        alignItems: "center",
        opacity: disabled ? 0.45 : pressed ? 0.65 : 1,
        transform: [{ scale: pressed && !disabled ? 0.96 : 1 }],
      })}
    >
      <PendingPulse active={waiting && !disabled} color={accent} />
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
  source?: string;
  alias?: string;
  error?: string;
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
  const [efPending, setEfPending] = useState<Record<string, PendingToggle>>({});
  const [toggleErr, setToggleErr] = useState<string | null>(null);
  const [efReconnectBusy, setEfReconnectBusy] = useState(false);
  const [correctStart, setCorrectStart] = useState("");
  const [correctEnd, setCorrectEnd] = useState("");
  const [correctW, setCorrectW] = useState("");
  const [correctBusy, setCorrectBusy] = useState(false);
  const [correctMsg, setCorrectMsg] = useState<string | null>(null);
  const [kasaBusy, setKasaBusy] = useState<"charge" | "divert" | null>(null);
  const [chargeHost, setChargeHost] = useState<string | null>(null);
  const [divertHost, setDivertHost] = useState<string | null>(null);
  const [chargeOn, setChargeOn] = useState<boolean | null>(null);
  const [divertOn, setDivertOn] = useState<boolean | null>(null);
  const [eodFc, setEodFc] = useState<ForecastPayload | null>(null);
  const eodAnchorRef = useRef<number | null>(null);
  const eodLastFetchRef = useRef(0);

  const t = status?.telemetry;
  const soc = headlineSoc(t);
  const devices = status?.cloud?.devices_overview || [];
  const selected = status?.cloud?.selected_device_id;
  const energy = status?.energy;
  const packs = (status?.battery_packs || []) as Pack[];
  const linkedEcoflow = (Array.isArray(t?.linked_ecoflow) ? t!.linked_ecoflow : []) as LinkedEcoflow[];
  const efBattUnits = linkedEcoflow.filter(
    (u) => (u.roles || []).includes("battery") && (u.fresh || u.telemetry?.battery_percent != null),
  );
  const showPacks = packs.length > 0 || efBattUnits.length > 0;
  const hist = status?.history || [];
  const conn = status?.connection_status || (connected ? "connected" : "disconnected");
  const source = status?.source ? String(status.source).toUpperCase() : null;
  const sn = status?.device?.device_sn as string | undefined;
  const isSiseli = isSiseliView(status);
  const siseliId = siseliDeviceId(status);
  const watchdog = status?.inverter_watchdog as { active?: boolean; message?: string } | null;
  const eod = useMemo(() => buildEodForecast(eodFc), [eodFc]);

  const loadEod = useCallback(async () => {
    eodLastFetchRef.current = Date.now();
    try {
      const j = (await endpoints.forecast(sn)) as ForecastPayload;
      setEodFc(j);
      if (j.starting_soc_pct != null) eodAnchorRef.current = j.starting_soc_pct;
    } catch {
      setEodFc(null);
    }
  }, [sn]);

  useEffect(() => {
    if (isSiseli) {
      setEodFc(null);
      return;
    }
    void loadEod();
    const id = setInterval(() => void loadEod(), EOD_INTERVAL_MS);
    return () => clearInterval(id);
  }, [loadEod, isSiseli]);

  useEffect(() => {
    if (isSiseli) return;
    const anchor = eodAnchorRef.current;
    if (soc == null || anchor == null) return;
    if (Date.now() - eodLastFetchRef.current < EOD_MIN_REFRESH_MS) return;
    if (Math.abs(soc - anchor) < EOD_DRIFT_THRESHOLD_PCT) return;
    void loadEod();
  }, [soc, loadEod, isSiseli]);

  useEffect(() => {
    if (isSiseli) {
      setChargeHost(null);
      setDivertHost(null);
      return;
    }
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
  }, [sn, isSiseli]);

  useEffect(() => {
    setPending({});
    setEfPending({});
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

  // Clear EcoFlow pending toggles when linked telemetry matches (or times out).
  useEffect(() => {
    const now = Date.now();
    setEfPending((prev) => {
      let changed = false;
      const next = { ...prev };
      for (const key of Object.keys(next)) {
        const p = next[key];
        if (p.inFlight) continue;
        const [efSn, port] = key.split(":");
        const unit = linkedEcoflow.find((u) => String(u.sn) === efSn);
        const live = portFlag(unit?.telemetry?.[`${port}_on` as keyof typeof unit.telemetry]);
        if (now > p.until || live === p.expected) {
          delete next[key];
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [linkedEcoflow]);

  useEffect(() => {
    const waits = [
      ...Object.entries(pending).filter(([, p]) => !p.inFlight && p.until > Date.now()),
      ...Object.entries(efPending).filter(([, p]) => !p.inFlight && p.until > Date.now()),
    ];
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
      setEfPending((prev) => {
        let changed = false;
        const next = { ...prev };
        for (const key of Object.keys(next)) {
          if (!next[key].inFlight && next[key].until <= now) {
            delete next[key];
            changed = true;
          }
        }
        return changed ? next : prev;
      });
    }, Math.max(ms, 0));
    return () => clearTimeout(id);
  }, [pending, efPending]);

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

  async function toggleEcoflow(sn: string, port: "ac" | "dc", on: boolean) {
    const key = `${sn}:${port}`;
    const run = async () => {
      setToggleErr(null);
      setEfPending((prev) => ({
        ...prev,
        [key]: { expected: on, until: Date.now() + PENDING_TOGGLE_MS, inFlight: true },
      }));
      try {
        await endpoints.setOutput(port, on, sn);
        setEfPending((prev) => ({
          ...prev,
          [key]: { expected: on, until: Date.now() + PENDING_TOGGLE_MS, inFlight: false },
        }));
      } catch (e: unknown) {
        setEfPending((prev) => {
          const next = { ...prev };
          delete next[key];
          return next;
        });
        setToggleErr(
          e instanceof Error ? e.message : `Failed to toggle EcoFlow ${port.toUpperCase()}`,
        );
      }
    };
    if (port === "ac" && !on) {
      Alert.alert(
        "Turn EcoFlow AC OFF?",
        "Anything plugged in will lose power.",
        [
          { text: "Cancel", style: "cancel" },
          { text: "Turn OFF", style: "destructive", onPress: () => void run() },
        ],
      );
      return;
    }
    await run();
  }

  async function reconnectEcoflow() {
    if (efReconnectBusy) return;
    setEfReconnectBusy(true);
    setToggleErr(null);
    try {
      await endpoints.ecoflowReconnect();
    } catch (e: unknown) {
      setToggleErr(e instanceof Error ? e.message : "EcoFlow reconnect failed");
    } finally {
      setTimeout(() => setEfReconnectBusy(false), 800);
    }
  }

  async function applyCorrectOutput() {
    if (correctBusy) return;
    const startMs = Date.parse(correctStart.replace(" ", "T"));
    const endMs = Date.parse(correctEnd.replace(" ", "T"));
    const watts = Number(correctW);
    if (!Number.isFinite(startMs) || !Number.isFinite(endMs)) {
      setCorrectMsg("Use times like 2026-10-08 13:00");
      return;
    }
    if (!Number.isFinite(watts) || watts < 0) {
      setCorrectMsg("Output watts must be >= 0");
      return;
    }
    setCorrectBusy(true);
    setCorrectMsg(null);
    try {
      const j = await endpoints.correctOutput({
        device_sn: sn || null,
        start_ts: Math.floor(startMs / 1000),
        end_ts: Math.floor(endMs / 1000),
        output_w: watts,
      });
      setCorrectMsg(
        `Updated ${j.buckets ?? 0} min · ${j.live_points_patched ?? 0} live points`,
      );
    } catch (e: unknown) {
      setCorrectMsg(e instanceof Error ? e.message : "Correction failed");
    } finally {
      setCorrectBusy(false);
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
            label={conn === "connected" && source ? source : conn}
            tone={
              conn === "connected"
                ? source
                  ? "mute"
                  : "ok"
                : conn === "error"
                  ? "err"
                  : "warn"
            }
          />
        </View>
        <View style={{ flexDirection: "row", alignItems: "center", gap: 12 }}>
          <View style={{ flex: 1, gap: 8 }}>
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
          </View>
          {!isSiseli ? <EodForecastPill view={eod} /> : null}
        </View>
      </Card>

      <Card>
        <Eyebrow>Power flow</Eyebrow>
        <PowerFlow
          t={t}
          deviceId={siseliId || selected || undefined}
          unified={!!status?.device_prefs?.solar_flow_unified}
          loadSources={status?.device_prefs?.load_sources}
        />
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
        <SavingsRow
          savings={energy?.today_savings}
          currency={energy?.cost_plan?.currency}
        />
      </EnergyKpi>

      {isSiseli ? (
        <Card>
          <Eyebrow>Solar panels</Eyebrow>
          <View style={{ flexDirection: "row", gap: 12 }}>
            {(
              [
                // Siseli-only — never the EcoFlow-combined solar_input_w total.
                [
                  "Power",
                  fmt(
                    t?.siseli_solar_w != null ? t.siseli_solar_w : t?.solar_input_w,
                    0,
                  ),
                  "W",
                ],
                ["Voltage", fmt(t?.pv_voltage_v, 1), "V"],
                ["Current", fmt(t?.pv_current_a, 2), "A"],
              ] as const
            ).map(([label, value, unit]) => (
              <View key={label} style={{ flex: 1 }}>
                <Text style={{ color: colors.text, fontSize: 22, fontWeight: "700" }}>
                  {value}
                  <Text style={{ fontSize: 13, color: colors.textDim }}> {unit}</Text>
                </Text>
                <Hint>{label}</Hint>
              </View>
            ))}
          </View>
        </Card>
      ) : null}

      {!isSiseli ? (
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
      ) : pinnedControls(status?.siseli_controls, status?.device_prefs?.live_controls).length ? (
        <Card>
          <Eyebrow>Inverter controls</Eyebrow>
          <SiseliLiveControls
            controls={status?.siseli_controls}
            pins={status?.device_prefs?.live_controls}
            deviceId={siseliId}
          />
        </Card>
      ) : null}

      {watchdog?.active && !isSiseli ? (
        <Card>
          <Eyebrow>Inverter watchdog</Eyebrow>
          <Hint>{watchdog.message || "AC trip recovery is active."}</Hint>
          <Btn title="Dismiss" kind="ghost" onPress={() => void endpoints.dismissWatchdog()} />
        </Card>
      ) : null}

      {showPacks ? (
        <Card>
          <Eyebrow>Battery packs</Eyebrow>
          <Hint>
            {packs.length > 0
              ? `${packs.length} pack${packs.length === 1 ? "" : "s"}`
              : `${efBattUnits.length} EcoFlow`}
            {efBattUnits.length && packs.length
              ? ` · ${efBattUnits.length} EcoFlow`
              : ""}
            {soc != null ? ` · system ${Math.round(soc)}%` : ""}
          </Hint>
          {packs.length > 0 ? (
            isSiseli ? (
              t?.inverter_soc_pct != null || t?.main_soc_pct != null ? (
                <PackRow
                  idx="★"
                  isMain
                  soc={t?.inverter_soc_pct ?? t?.main_soc_pct ?? null}
                  meta="Inverter (portal)"
                />
              ) : null
            ) : (
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
            )
          ) : null}
          {packs.map((p, i) => {
            const order = typeof p.deviceOrder === "number" ? p.deviceOrder : i;
            const sn = String(p.deviceSn || "");
            const label = p.alias || (sn ? `…${sn.slice(-6)}` : null);
            return (
              <PackRow
                key={sn || i}
                idx={String(order + 1)}
                soc={p.rb ?? null}
                meta={[
                  p.error ? String(p.error) : packFlow(p),
                  p.it != null ? fmtTemp(p.it, tempUnit) : null,
                  label,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              />
            );
          })}
          {efBattUnits.map((u, i) => {
            const et = u.telemetry || {};
            const solarW = Math.round(Number(et.solar_input_w) || 0);
            const outW = Math.round(Number(et.output_power_w) || 0);
            const flow = !u.fresh
              ? "stale"
              : solarW > 0
                ? `+${solarW}W`
                : outW > 0
                  ? `−${outW}W`
                  : "idle";
            return (
              <PackRow
                key={String(u.sn || `ef-${i}`)}
                idx={String(packs.length + i + 1)}
                soc={et.battery_percent != null ? Math.round(Number(et.battery_percent)) : null}
                meta={[
                  flow,
                  et.battery_temp_c != null ? fmtTemp(et.battery_temp_c, tempUnit) : null,
                  u.alias || "EcoFlow",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              />
            );
          })}
        </Card>
      ) : null}

      {isSiseli && linkedEcoflow.length > 0 ? (
        <Card>
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 8,
            }}
          >
            <View style={{ flex: 1, minWidth: 0 }}>
              <Eyebrow>EcoFlow</Eyebrow>
              <Hint>
                {linkedEcoflow.length} linked unit
                {linkedEcoflow.length === 1 ? "" : "s"}
              </Hint>
            </View>
            <Btn
              title="Reconnect"
              kind="ghost"
              loading={efReconnectBusy}
              disabled={efReconnectBusy}
              onPress={() => void reconnectEcoflow()}
            />
          </View>
          {linkedEcoflow.map((u) => {
            const et = u.telemetry || {};
            const detail = (u.detail || {}) as Record<string, unknown>;
            const efSn = String(u.sn || "");
            const socNum =
              et.battery_percent != null ? Math.round(Number(et.battery_percent)) : null;
            return (
              <View key={efSn || u.alias || "ecoflow"} style={{ marginTop: 12, gap: 8 }}>
                <Text style={{ color: colors.text, fontWeight: "600" }}>
                  {u.alias || u.sn || "EcoFlow"}
                  {!u.fresh ? " · stale" : ""}
                </Text>
                <PackRow
                  idx="·"
                  soc={socNum}
                  meta={[
                    et.battery_temp_c != null ? fmtTemp(et.battery_temp_c, tempUnit) : null,
                    `solar ${Math.round(Number(et.solar_input_w) || 0)} W`,
                    `out ${Math.round(Number(et.output_power_w) || 0)} W`,
                    `in ${Math.round(Number(et.input_power_w) || 0)} W`,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                />
                {detail.pow_get_pv != null || detail.pow_get_pv2 != null ? (
                  <Hint>
                    PV1 {detail.pow_get_pv != null ? `${Math.round(Number(detail.pow_get_pv))} W` : "—"}
                    {" · "}
                    PV2 {detail.pow_get_pv2 != null ? `${Math.round(Number(detail.pow_get_pv2))} W` : "—"}
                  </Hint>
                ) : null}
                <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8 }}>
                  {(["ac", "dc"] as const).map((port) => {
                    const key = `${efSn}:${port}`;
                    const liveOn = portOn(port === "ac" ? et.ac_on : et.dc_on);
                    const pend = efPending[key];
                    const inFlight = !!pend?.inFlight;
                    const waiting = !!pend && !pend.inFlight && Date.now() <= pend.until;
                    const on = pend && Date.now() <= pend.until ? pend.expected : liveOn;
                    const disabled = !u.fresh || !efSn;
                    return (
                      <PowerChip
                        key={port}
                        label={port.toUpperCase()}
                        on={on}
                        inFlight={inFlight}
                        pending={waiting}
                        disabled={disabled}
                        onPress={() => {
                          if (disabled) return;
                          void toggleEcoflow(efSn, port, !on);
                        }}
                      />
                    );
                  })}
                </View>
              </View>
            );
          })}
          {toggleErr ? (
            <Text style={{ color: colors.danger, fontSize: 12 }}>{toggleErr}</Text>
          ) : null}
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

      <Collapsible title="Correct output" summary="Fix EcoFlow dropout gaps">
        <Hint>
          Overwrites stored load Wh and the Live chart for the range (max 24h). Times
          are local, e.g. 2026-10-08 13:00.
        </Hint>
        <Field
          label="From"
          value={correctStart}
          onChangeText={setCorrectStart}
          placeholder="2026-10-08 13:00"
          autoCapitalize="none"
        />
        <Field
          label="To"
          value={correctEnd}
          onChangeText={setCorrectEnd}
          placeholder="2026-10-08 13:40"
          autoCapitalize="none"
        />
        <Field
          label="Output (W)"
          value={correctW}
          onChangeText={setCorrectW}
          placeholder="1200"
          keyboardType="numeric"
        />
        <Btn
          title="Apply correction"
          kind="primary"
          loading={correctBusy}
          disabled={correctBusy}
          onPress={() => void applyCorrectOutput()}
        />
        {correctMsg ? (
          <Text style={{ color: colors.textDim, fontSize: 12 }}>{correctMsg}</Text>
        ) : null}
      </Collapsible>
    </Screen>
  );
}
