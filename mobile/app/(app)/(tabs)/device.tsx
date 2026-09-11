import * as Clipboard from "expo-clipboard";
import { useCallback, useEffect, useState } from "react";
import { Text } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import { Btn, Card, Eyebrow, Field, Hint, Screen, Segmented, Title } from "../../../src/components/ui";
import { timeAgo } from "../../../src/lib/format";
import { useLive } from "../../../src/store/live";
import { colors } from "../../../src/theme";

export default function DeviceScreen() {
  const status = useLive((s) => s.status);
  const d = status?.device;
  const t = status?.telemetry;
  const [cap, setCap] = useState("");
  const [params, setParams] = useState<{ key: string; value?: number; source?: string; hint?: string }[]>([]);
  const [raw, setRaw] = useState<string | null>(null);
  const [probe, setProbe] = useState<string | null>(null);
  const [pause, setPause] = useState("600");
  const [msg, setMsg] = useState<string | null>(null);
  const [f7Enabled, setF7Enabled] = useState("0");
  const [f7Cooldown, setF7Cooldown] = useState("30");
  const [f7DayStart, setF7DayStart] = useState("");
  const [f7DayEnd, setF7DayEnd] = useState("");
  const [f7Status, setF7Status] = useState<string | null>(null);
  const [f7Events, setF7Events] = useState<{ ts?: number; level?: string; message?: string }[]>([]);

  const sn = (d?.device_sn as string) || activeSn();

  const load = useCallback(async () => {
    try {
      const c = (await endpoints.capacity()) as { devices?: { device_sn: string; capacity_wh?: number; default_wh?: number }[] };
      const row = c.devices?.find((x) => x.device_sn === sn);
      setCap(row?.capacity_wh != null ? String(row.capacity_wh) : "");
    } catch {
      /* ignore */
    }
    try {
      const p = (await endpoints.params(sn)) as { params?: typeof params } | typeof params;
      setParams(Array.isArray(p) ? p : p.params || []);
    } catch {
      setParams([]);
    }
    try {
      const f = (await endpoints.f7AcResetConfig(sn)) as {
        in_daylight?: boolean;
        config?: {
          enabled?: boolean;
          cooldown_min?: number;
          day_start?: string | null;
          day_end?: string | null;
          last_cycle_ts?: number;
          events?: { ts?: number; level?: string; message?: string }[];
        };
        events?: { ts?: number; level?: string; message?: string }[];
      };
      const c = f.config || {};
      setF7Enabled(c.enabled ? "1" : "0");
      setF7Cooldown(String(c.cooldown_min ?? 30));
      setF7DayStart(c.day_start || "");
      setF7DayEnd(c.day_end || "");
      const last = c.last_cycle_ts
        ? `last pulse ${new Date(c.last_cycle_ts * 1000).toLocaleString()}`
        : "no pulse yet";
      setF7Status(`${c.enabled ? "on" : "off"} · ${f.in_daylight ? "daylight" : "night"} · ${last}`);
      setF7Events(f.events || c.events || []);
    } catch {
      setF7Events([]);
    }
  }, [sn]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <Screen>
      <Card>
        <Eyebrow>Device info</Eyebrow>
        <Title>{d?.name || "—"}</Title>
        <Hint>Model {String(d?.model_name || d?.model_code || "—")}</Hint>
        <Hint>Serial {String(d?.device_sn || "—")}</Hint>
        <Hint>Source {String(status?.source || "—")} · {timeAgo(status?.last_update_ts)}</Hint>
        <Hint>
          UPS {t?.ups_on ? "on" : "off"} · Super charge {t?.super_charge_on ? "on" : "off"}
        </Hint>
        <Hint>
          Error {t?.error_code === 8 ? "F7 (8)" : (t?.error_code ?? "none")}
        </Hint>
      </Card>

      <Card>
        <Eyebrow>F7 AC reset</Eyebrow>
        <Hint>
          Explorer 2000 Plus morning solar-inverter latch. Pulses AC on 5s then off.
          Skipped if AC is already on, at night, or in cooldown.
        </Hint>
        {f7Status ? <Hint>{f7Status}</Hint> : null}
        <Segmented
          value={f7Enabled}
          onChange={setF7Enabled}
          options={[
            { id: "0", label: "Off" },
            { id: "1", label: "On" },
          ]}
        />
        <Field
          label="Cooldown (min)"
          value={f7Cooldown}
          onChangeText={setF7Cooldown}
          keyboardType="number-pad"
        />
        <Hint>Optional daylight override (leave blank to use forecast sunrise–sunset).</Hint>
        <Field label="Day start" value={f7DayStart} onChangeText={setF7DayStart} placeholder="06:00" />
        <Field label="Day end" value={f7DayEnd} onChangeText={setF7DayEnd} placeholder="20:00" />
        <Btn
          title="Save F7 settings"
          onPress={() =>
            void endpoints
              .saveF7AcResetConfig(
                {
                  device_sn: sn,
                  enabled: f7Enabled === "1",
                  cooldown_min: Number(f7Cooldown) || 30,
                  day_start: f7DayStart || null,
                  day_end: f7DayEnd || null,
                },
                sn,
              )
              .then(load)
              .then(() => setMsg("F7 settings saved"))
          }
        />
        {f7Events.slice(-8).reverse().map((e, i) => (
          <Hint key={i}>
            {e.ts ? new Date(e.ts * 1000).toLocaleString() : ""} · {e.level} · {e.message}
          </Hint>
        ))}
      </Card>

      <Card>
        <Eyebrow>Battery capacity</Eyebrow>
        <Hint>Override total Wh if you have expansion batteries stacked on this unit.</Hint>
        <Field label="Total Wh" value={cap} onChangeText={setCap} keyboardType="number-pad" />
        <Btn
          title="Save"
          onPress={() =>
            void endpoints.setCapacity(sn!, Number(cap)).then(load).then(() => setMsg("Capacity saved"))
          }
        />
        <Btn
          title="Clear override"
          kind="ghost"
          onPress={() => void endpoints.setCapacity(sn!, null).then(load)}
        />
      </Card>

      <Card>
        <Eyebrow>Learned parameters</Eyebrow>
        {params.map((p) => (
          <Hint key={p.key}>
            {p.key}: {p.value ?? "—"} ({p.source || "auto"})
          </Hint>
        ))}
        {!params.length ? <Hint>No parameters yet.</Hint> : null}
      </Card>

      <Card>
        <Eyebrow>Cloud properties</Eyebrow>
        <Btn
          title={raw ? "Hide" : "Show raw props"}
          kind="ghost"
          onPress={() =>
            void (raw
              ? setRaw(null)
              : endpoints.rawProps(sn).then((j) => setRaw(JSON.stringify(j, null, 2))))
          }
        />
        {raw ? <Text style={{ color: colors.textMute, fontSize: 11, fontFamily: "Menlo" }}>{raw}</Text> : null}
        <Btn
          title="Copy"
          kind="ghost"
          onPress={() => raw && void Clipboard.setStringAsync(raw)}
        />
      </Card>

      <Card>
        <Eyebrow>Cloud endpoint probe</Eyebrow>
        <Btn
          title="Probe now"
          kind="ghost"
          onPress={() =>
            void endpoints.cloudProbe().then((j) => setProbe(JSON.stringify(j, null, 2)))
          }
        />
        {probe ? <Text style={{ color: colors.textMute, fontSize: 11 }}>{probe.slice(0, 4000)}</Text> : null}
      </Card>

      <Card>
        <Eyebrow>Polling</Eyebrow>
        <Hint>Jackery cloud allows one session. Pause so the official phone app can sign in.</Hint>
        <Segmented
          value={pause}
          onChange={setPause}
          options={[
            { id: "60", label: "1m" },
            { id: "300", label: "5m" },
            { id: "600", label: "10m" },
            { id: "1800", label: "30m" },
            { id: "3600", label: "1h" },
          ]}
        />
        <Btn title="Pause polling" onPress={() => void endpoints.pausePolling(Number(pause)).then(() => setMsg("Paused"))} />
        <Btn title="Resume" kind="ghost" onPress={() => void endpoints.resumePolling().then(() => setMsg("Resumed"))} />
      </Card>

      <Card>
        <Eyebrow>Jackery account</Eyebrow>
        <Btn
          title="Forget cloud credentials"
          kind="danger"
          onPress={() => void endpoints.forgetCloud().then(() => setMsg("Credentials cleared"))}
        />
        {msg ? <Hint>{msg}</Hint> : null}
      </Card>
    </Screen>
  );
}
