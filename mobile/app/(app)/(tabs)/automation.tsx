import { useCallback, useEffect, useState } from "react";
import { Text } from "react-native";
import { activeSn, endpoints } from "../../../src/api/client";
import { Btn, Card, Eyebrow, Field, Hint, Screen, Segmented, Title } from "../../../src/components/ui";
import { colors } from "../../../src/theme";

type Rule = {
  id: string;
  name?: string;
  operator?: string;
  value?: number;
  action?: string;
  kasa_host?: string;
  jackery_device_sn?: string;
  enabled?: boolean;
};

export default function AutomationScreen() {
  const [tab, setTab] = useState("controllers");
  const sn = activeSn();

  return (
    <Screen>
      <Segmented
        value={tab}
        onChange={setTab}
        options={[
          { id: "controllers", label: "Controllers" },
          { id: "devices", label: "Devices" },
          { id: "rules", label: "Rules" },
          { id: "insights", label: "Insights" },
        ]}
      />
      {tab === "controllers" ? <Controllers sn={sn} /> : null}
      {tab === "devices" ? <KasaDevices /> : null}
      {tab === "rules" ? <Rules /> : null}
      {tab === "insights" ? <Insights sn={sn} /> : null}
    </Screen>
  );
}

function Controllers({ sn }: { sn?: string }) {
  const [sc, setSc] = useState<Record<string, unknown>>({});
  const [sol, setSol] = useState<Record<string, unknown>>({});
  const [plugs, setPlugs] = useState<{ host: string; alias?: string }[]>([]);
  const [msg, setMsg] = useState<string | null>(null);
  const [history, setHistory] = useState<unknown[]>([]);
  const [solarHist, setSolarHist] = useState<unknown[]>([]);

  const load = useCallback(async () => {
    const [c, s, k, st, sh] = await Promise.allSettled([
      endpoints.smartConfig(sn),
      endpoints.solarConfig(sn),
      endpoints.kasaSaved(false, sn),
      endpoints.smartStatus(sn),
      endpoints.solarHistory(sn),
    ]);
    if (c.status === "fulfilled") {
      const v = c.value as { config?: Record<string, unknown> };
      setSc(v.config || (c.value as Record<string, unknown>));
    }
    if (s.status === "fulfilled") {
      const v = s.value as { config?: Record<string, unknown> };
      setSol(v.config || (s.value as Record<string, unknown>));
    }
    if (k.status === "fulfilled") {
      const v = k.value as { devices?: { host: string; alias?: string }[] };
      setPlugs(v.devices || []);
    }
    if (st.status === "fulfilled") {
      const v = st.value as { history?: unknown[] };
      setHistory(v.history || []);
    }
    if (sh.status === "fulfilled") {
      const v = sh.value as { history?: unknown[] };
      setSolarHist(v.history || []);
    }
  }, [sn]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <Card>
        <Title>Smart charge</Title>
        <Hint>Grid plug on cloudy nights when forecast says you won't make sunrise SOC.</Hint>
        <Field label="Mode (off/test/active)" value={String(sc.mode ?? "off")} onChangeText={(v) => setSc({ ...sc, mode: v })} />
        <Field
          label="Kasa host"
          value={String(sc.kasa_device_host ?? "")}
          onChangeText={(v) => setSc({ ...sc, kasa_device_host: v })}
        />
        {plugs.map((p) => (
          <Btn key={p.host} kind="ghost" title={`${p.alias || p.host}`} onPress={() => setSc({ ...sc, kasa_device_host: p.host })} />
        ))}
        <Field
          label="Target sunrise SOC %"
          keyboardType="number-pad"
          value={String(sc.target_sunrise_soc_pct ?? 25)}
          onChangeText={(v) => setSc({ ...sc, target_sunrise_soc_pct: Number(v) })}
        />
        <Field
          label="Max charge W"
          keyboardType="number-pad"
          value={String(sc.max_charge_w ?? 800)}
          onChangeText={(v) => setSc({ ...sc, max_charge_w: Number(v) })}
        />
        <Field
          label="Max ON minutes"
          keyboardType="number-pad"
          value={String(sc.max_on_duration_minutes ?? 480)}
          onChangeText={(v) => setSc({ ...sc, max_on_duration_minutes: Number(v) })}
        />
        <Btn
          title="Save"
          onPress={() => void endpoints.saveSmartConfig(sc, sn).then(() => setMsg("Smart charge saved"))}
        />
        <Btn title="Evaluate now" kind="ghost" onPress={() => void endpoints.smartEvaluate(sn).then(load)} />
        <Btn title="Backtest 7d" kind="ghost" onPress={() => void endpoints.smartBacktest({ days: 7, device_sn: sn }).then((j) => setMsg(JSON.stringify(j).slice(0, 400)))} />
        {history.slice(0, 8).map((h, i) => (
          <Hint key={i}>{JSON.stringify(h).slice(0, 180)}</Hint>
        ))}
      </Card>
      <Card>
        <Title>Excess diversion</Title>
        <Hint>Dump surplus to a downstream Kasa load when sunrise SOC has headroom.</Hint>
        <Field label="Mode (off/test/active)" value={String(sol.mode ?? "off")} onChangeText={(v) => setSol({ ...sol, mode: v })} />
        <Field
          label="Kasa host"
          value={String(sol.kasa_device_host ?? "")}
          onChangeText={(v) => setSol({ ...sol, kasa_device_host: v })}
        />
        <Field
          label="Load draw W"
          keyboardType="number-pad"
          value={String(sol.car_load_w ?? 1400)}
          onChangeText={(v) => setSol({ ...sol, car_load_w: Number(v) })}
        />
        <Field
          label="Restart SOC %"
          keyboardType="number-pad"
          value={String(sol.comfort_high_pct ?? 70)}
          onChangeText={(v) => setSol({ ...sol, comfort_high_pct: Number(v) })}
        />
        <Field
          label="Hard SOC floor %"
          keyboardType="number-pad"
          value={String(sol.comfort_low_pct ?? 30)}
          onChangeText={(v) => setSol({ ...sol, comfort_low_pct: Number(v) })}
        />
        <Btn title="Save" onPress={() => void endpoints.saveSolarConfig(sol, sn).then(() => setMsg("Diversion saved"))} />
        <Btn title="Evaluate now" kind="ghost" onPress={() => void endpoints.solarEvaluate(sn).then(load)} />
        {solarHist.slice(0, 8).map((h, i) => (
          <Hint key={i}>{JSON.stringify(h).slice(0, 180)}</Hint>
        ))}
      </Card>
      {msg ? <Hint>{msg}</Hint> : null}
    </>
  );
}

function KasaDevices() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [host, setHost] = useState("");
  const [alias, setAlias] = useState("");
  const [saved, setSaved] = useState<{ host: string; alias?: string; on?: boolean }[]>([]);
  const [msg, setMsg] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = (await endpoints.kasaSaved(true)) as { devices?: typeof saved };
      setSaved(r.devices || []);
    } catch {
      setSaved([]);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <Card>
        <Title>Kasa account</Title>
        <Hint>Needed for newer SMART/KLAP plugs even for local control.</Hint>
        <Field label="Email" value={email} onChangeText={setEmail} keyboardType="email-address" />
        <Field label="Password" value={password} onChangeText={setPassword} secureTextEntry />
        <Btn title="Save credentials" onPress={() => void endpoints.saveKasaCreds(email, password).then(() => setMsg("Saved"))} />
        <Btn title="Forget" kind="ghost" onPress={() => void endpoints.forgetKasaCreds()} />
      </Card>
      <Card>
        <Title>Saved plugs</Title>
        {saved.map((d) => (
          <Card key={d.host}>
            <Eyebrow>{d.alias || d.host}</Eyebrow>
            <Hint>{d.host}</Hint>
            <Btn title="On" kind="ghost" onPress={() => void endpoints.kasaTest(d.host, true)} />
            <Btn title="Off" kind="ghost" onPress={() => void endpoints.kasaTest(d.host, false)} />
            <Btn title="Remove" kind="danger" onPress={() => void endpoints.deleteKasaDevice(d.host).then(load)} />
          </Card>
        ))}
        <Field label="Host / IP" value={host} onChangeText={setHost} />
        <Field label="Alias" value={alias} onChangeText={setAlias} />
        <Btn
          title="Save plug"
          onPress={() => void endpoints.saveKasaDevice({ host, alias }).then(load)}
        />
        <Btn
          title="Discover LAN"
          kind="ghost"
          onPress={() =>
            void endpoints.kasaDiscover().then((j) => setMsg(JSON.stringify(j).slice(0, 400)))
          }
        />
        {msg ? <Hint>{msg}</Hint> : null}
      </Card>
    </>
  );
}

function Rules() {
  const [rules, setRules] = useState<Rule[]>([]);
  const [name, setName] = useState("");
  const [op, setOp] = useState("<");
  const [value, setValue] = useState("20");
  const [action, setAction] = useState("off");
  const [host, setHost] = useState("");

  const load = useCallback(async () => {
    const r = await endpoints.rules();
    setRules((r.rules as Rule[]) || []);
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <Card>
      <Title>SOC rules</Title>
      <Hint>Edge-triggered: fire once when SOC crosses the threshold.</Hint>
      {rules.map((r) => (
        <Card key={r.id}>
          <Text style={{ color: colors.text }}>
            {r.name} · SOC {r.operator} {r.value} → {r.action} {r.kasa_host}
          </Text>
          <Btn title="Delete" kind="danger" onPress={() => void endpoints.deleteRule(r.id).then(load)} />
        </Card>
      ))}
      <Field label="Name" value={name} onChangeText={setName} />
      <Field label="Operator < <= = >= >" value={op} onChangeText={setOp} />
      <Field label="SOC value" value={value} onChangeText={setValue} keyboardType="number-pad" />
      <Field label="Action on/off" value={action} onChangeText={setAction} />
      <Field label="Kasa host" value={host} onChangeText={setHost} />
      <Btn
        title="Add rule"
        onPress={() =>
          void endpoints
            .saveRule({
              name,
              operator: op,
              value: Number(value),
              action,
              kasa_host: host,
              jackery_device_sn: activeSn(),
              enabled: true,
            })
            .then(load)
        }
      />
    </Card>
  );
}

function Insights({ sn }: { sn?: string }) {
  const [items, setItems] = useState<{ id: string; title?: string; summary?: string; body?: string }[]>([]);
  const [changes, setChanges] = useState<unknown[]>([]);
  const [status, setStatus] = useState<string>("");

  const load = useCallback(async () => {
    const [s, c] = await Promise.allSettled([
      endpoints.suggestions(sn, "pending"),
      endpoints.advisorChanges(sn),
    ]);
    if (s.status === "fulfilled") {
      const v = s.value as { suggestions?: typeof items };
      setItems(v.suggestions || []);
    }
    if (c.status === "fulfilled") {
      const v = c.value as { changes?: unknown[] };
      setChanges(v.changes || []);
    }
  }, [sn]);
  useEffect(() => {
    void load();
  }, [load]);

  async function review() {
    setStatus("Starting review…");
    await endpoints.reviewNow(sn);
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 2000));
      const st = (await endpoints.reviewStatus(sn)) as { status?: string; error?: string };
      setStatus(st.status || JSON.stringify(st));
      if (st.status === "done" || st.status === "error" || st.error) break;
    }
    await load();
  }

  return (
    <Card>
      <Title>AI insights</Title>
      <Hint>Nothing applies automatically. Review, then Apply or Dismiss.</Hint>
      <Btn title="Run review now" onPress={() => void review()} />
      <Btn title="Show advisor context" kind="ghost" onPress={() => void endpoints.advisorPreview(sn).then((j) => setStatus(JSON.stringify(j).slice(0, 800)))} />
      {status ? <Hint>{status}</Hint> : null}
      {items.map((s) => (
        <Card key={s.id}>
          <Eyebrow>{s.title || s.id}</Eyebrow>
          <Hint>{s.summary || s.body}</Hint>
          <Btn title="Apply" onPress={() => void endpoints.applySuggestion(s.id).then(load)} />
          <Btn title="Dismiss" kind="ghost" onPress={() => void endpoints.dismissSuggestion(s.id).then(load)} />
        </Card>
      ))}
      {!items.length ? <Hint>No pending suggestions.</Hint> : null}
      {changes.slice(0, 8).map((ch, i) => (
        <Hint key={i}>{JSON.stringify(ch).slice(0, 200)}</Hint>
      ))}
    </Card>
  );
}
