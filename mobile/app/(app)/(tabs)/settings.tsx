import { useRouter } from "expo-router";
import { useCallback, useEffect, useState } from "react";
import { Text, View } from "react-native";
import { endpoints } from "../../../src/api/client";
import type { SettingSpec } from "../../../src/api/types";
import { Btn, Card, Eyebrow, Field, Hint, RowSwitch, Screen, Segmented, Title } from "../../../src/components/ui";
import { useConnection } from "../../../src/store/connection";
import { usePrefs } from "../../../src/store/prefs";
import { useSession } from "../../../src/store/session";
import { colors } from "../../../src/theme";

export default function SettingsScreen() {
  const [tab, setTab] = useState("general");
  return (
    <Screen>
      <Segmented
        value={tab}
        onChange={setTab}
        options={[
          { id: "general", label: "General" },
          { id: "intelligence", label: "AI" },
          { id: "rates", label: "Rates" },
          { id: "backup", label: "Backup" },
          { id: "account", label: "Account" },
        ]}
      />
      {tab === "general" ? <General /> : null}
      {tab === "intelligence" ? <Intelligence /> : null}
      {tab === "rates" ? <Rates /> : null}
      {tab === "backup" ? <Backup /> : null}
      {tab === "account" ? <Account /> : null}
    </Screen>
  );
}

function General() {
  const [specs, setSpecs] = useState<SettingSpec[]>([]);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState<string | null>(null);
  const tempUnit = usePrefs((s) => s.tempUnit);
  const keepAwake = usePrefs((s) => s.keepAwake);

  const load = useCallback(async () => {
    const r = await endpoints.settings();
    setSpecs(r.settings || []);
    setVals(Object.fromEntries((r.settings || []).map((s) => [s.key, String(s.value)])));
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <Card>
        <Title>Runtime settings</Title>
        <Hint>Saved to the server; take effect on the next poll cycle.</Hint>
        {specs.map((s) => (
          <Field
            key={s.key}
            label={`${s.label} (${s.min}–${s.max})`}
            value={vals[s.key] ?? ""}
            onChangeText={(v) => setVals({ ...vals, [s.key]: v })}
            keyboardType="number-pad"
          />
        ))}
        <Btn
          title="Save"
          onPress={() => {
            const body: Record<string, number> = {};
            for (const [k, v] of Object.entries(vals)) body[k] = Number(v);
            void endpoints.saveSettings(body).then(() => setMsg("Saved"));
          }}
        />
        {msg ? <Hint>{msg}</Hint> : null}
      </Card>
      <Card>
        <Title>Display</Title>
        <RowSwitch
          label="Keep screen on"
          hint="For wall-mounted tablets"
          value={keepAwake}
          onValueChange={(v) => void usePrefs.getState().setKeepAwake(v)}
        />
        <Eyebrow>Temperature</Eyebrow>
        <Segmented
          value={tempUnit}
          onChange={(id) => void usePrefs.getState().setTempUnit(id as "C" | "F")}
          options={[
            { id: "C", label: "°C" },
            { id: "F", label: "°F" },
          ]}
        />
      </Card>
    </>
  );
}

function Intelligence() {
  const [provider, setProvider] = useState("anthropic");
  const [ak, setAk] = useState("");
  const [ok, setOk] = useState("");
  const [modelsA, setModelsA] = useState<string[]>([]);
  const [modelsO, setModelsO] = useState<string[]>([]);
  const [prefs, setPrefs] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const p = (await endpoints.aiProvider()) as { provider?: string };
      setProvider(p.provider || "anthropic");
    } catch {
      /* ignore */
    }
    try {
      const m = (await endpoints.anthropicModels()) as { models?: { id?: string }[] };
      setModelsA((m.models || []).map((x) => x.id || String(x)).filter(Boolean));
    } catch {
      /* ignore */
    }
    try {
      const m = (await endpoints.openaiModels()) as { models?: { id?: string }[] };
      setModelsO((m.models || []).map((x) => x.id || String(x)).filter(Boolean));
    } catch {
      /* ignore */
    }
    try {
      const pr = (await endpoints.anthropicPrefs()) as Record<string, string>;
      setPrefs(pr);
    } catch {
      /* ignore */
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <Card>
        <Title>AI provider</Title>
        <Segmented
          value={provider}
          onChange={(id) => {
            setProvider(id);
            void endpoints.setAiProvider(id);
          }}
          options={[
            { id: "anthropic", label: "Anthropic" },
            { id: "openai", label: "OpenAI" },
          ]}
        />
      </Card>
      <Card>
        <Title>Anthropic</Title>
        <Field label="API key" value={ak} onChangeText={setAk} secureTextEntry />
        <Btn title="Save & test" onPress={() => void endpoints.saveAnthropicKey(ak).then(() => setMsg("Anthropic key saved"))} />
        <Btn title="Forget" kind="ghost" onPress={() => void endpoints.deleteAnthropicKey()} />
        <Field
          label="Advisor model"
          value={prefs.advisor_model || modelsA[0] || ""}
          onChangeText={(v) => setPrefs({ ...prefs, advisor_model: v })}
        />
        <Field
          label="Narrator model"
          value={prefs.narrator_model || ""}
          onChangeText={(v) => setPrefs({ ...prefs, narrator_model: v })}
        />
        <Btn title="Save model prefs" kind="ghost" onPress={() => void endpoints.saveAnthropicPrefs(prefs)} />
        <Hint>{modelsA.slice(0, 8).join(" · ")}</Hint>
      </Card>
      <Card>
        <Title>OpenAI</Title>
        <Field label="API key" value={ok} onChangeText={setOk} secureTextEntry />
        <Btn title="Save & test" onPress={() => void endpoints.saveOpenaiKey(ok).then(() => setMsg("OpenAI key saved"))} />
        <Btn title="Forget" kind="ghost" onPress={() => void endpoints.deleteOpenaiKey()} />
        <Hint>{modelsO.slice(0, 8).join(" · ")}</Hint>
      </Card>
      {msg ? <Hint>{msg}</Hint> : null}
    </>
  );
}

function Rates() {
  const [rate, setRate] = useState("0.30");
  const [type, setType] = useState("flat");
  const [msg, setMsg] = useState<string | null>(null);
  useEffect(() => {
    void endpoints.costPlan().then((j) => {
      const p = j as { plan?: { type?: string; rate_per_kwh?: number } };
      setType(p.plan?.type || "flat");
      if (p.plan?.rate_per_kwh != null) setRate(String(p.plan.rate_per_kwh));
    });
  }, []);
  return (
    <Card>
      <Title>Electricity rates</Title>
      <Hint>Used to convert kWh into dollar savings.</Hint>
      <Segmented
        value={type}
        onChange={setType}
        options={[
          { id: "flat", label: "Flat" },
          { id: "tou", label: "TOU" },
        ]}
      />
      <Field label="$/kWh" value={rate} onChangeText={setRate} keyboardType="decimal-pad" />
      <Btn
        title="Save"
        onPress={() =>
          void endpoints
            .saveCostPlan({ type, rate_per_kwh: Number(rate), currency: "USD" })
            .then(() => setMsg("Saved"))
        }
      />
      {msg ? <Hint>{msg}</Hint> : null}
    </Card>
  );
}

function Backup() {
  const [transport, setTransport] = useState("smb");
  const [host, setHost] = useState("");
  const [share, setShare] = useState("");
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [status, setStatus] = useState<string>("");
  const [snaps, setSnaps] = useState<string[]>([]);

  const load = useCallback(async () => {
    try {
      const s = await endpoints.backupStatus();
      setStatus(JSON.stringify(s));
    } catch {
      /* ignore */
    }
    try {
      const r = (await endpoints.backupSnapshots()) as { snapshots?: { name?: string }[] | string[] };
      const list = r.snapshots || [];
      setSnaps(list.map((x) => (typeof x === "string" ? x : x.name || JSON.stringify(x))));
    } catch {
      setSnaps([]);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <Card>
        <Title>Backup destination</Title>
        <Segmented
          value={transport}
          onChange={setTransport}
          options={[
            { id: "smb", label: "SMB" },
            { id: "rsync_ssh", label: "rsync SSH" },
            { id: "rsyncd", label: "rsyncd" },
          ]}
        />
        <Field label="Host" value={host} onChangeText={setHost} />
        <Field label="Share / module" value={share} onChangeText={setShare} />
        <Field label="Username" value={user} onChangeText={setUser} />
        <Field label="Password" value={password} onChangeText={setPassword} secureTextEntry />
        <Btn
          title="Save"
          onPress={() =>
            void endpoints.saveBackupCreds({
              transport,
              host,
              share,
              username: user,
              password,
            })
          }
        />
        <Btn title="Test" kind="ghost" onPress={() => void endpoints.backupTest().then((j) => setStatus(JSON.stringify(j)))} />
        <Btn title="Discover SMB" kind="ghost" onPress={() => void endpoints.backupDiscover().then((j) => setStatus(JSON.stringify(j).slice(0, 500)))} />
        <Btn title="Run now" onPress={() => void endpoints.backupRun().then(load)} />
        <Hint>{status.slice(0, 500)}</Hint>
      </Card>
      <Card>
        <Eyebrow>Snapshots</Eyebrow>
        {snaps.map((s) => (
          <View key={s} style={{ marginBottom: 6 }}>
            <Text style={{ color: colors.text }}>{s}</Text>
            <Btn title="Restore" kind="ghost" onPress={() => void endpoints.backupRestore({ name: s })} />
          </View>
        ))}
        {!snaps.length ? <Hint>No snapshots yet.</Hint> : null}
      </Card>
    </>
  );
}

function Account() {
  const router = useRouter();
  const username = useSession((s) => s.username);
  const baseUrl = useConnection((s) => s.baseUrl);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [msg, setMsg] = useState<string | null>(null);

  return (
    <>
      <Card>
        <Title>Server</Title>
        <Hint>{baseUrl}</Hint>
        <Btn
          title="Change server URL"
          kind="ghost"
          onPress={() => {
            void useSession.getState().clear();
            router.replace("/(auth)/server");
          }}
        />
      </Card>
      <Card>
        <Title>Password</Title>
        <Hint>Signed in as {username || "—"}</Hint>
        <Field label="Current" value={current} onChangeText={setCurrent} secureTextEntry />
        <Field label="New (≥6 chars)" value={next} onChangeText={setNext} secureTextEntry />
        <Btn
          title="Change password"
          onPress={() =>
            void endpoints.changePassword(current, next).then(() => setMsg("Password updated"))
          }
        />
        {msg ? <Hint>{msg}</Hint> : null}
      </Card>
      <Card>
        <Btn
          title="Sign out"
          kind="danger"
          onPress={() => {
            void endpoints.logout().catch(() => {});
            void useSession.getState().clear();
            router.replace("/(auth)/login");
          }}
        />
      </Card>
    </>
  );
}
