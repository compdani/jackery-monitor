import { useRouter } from "expo-router";
import { useState } from "react";
import { probeServer } from "../../src/api/client";
import { Btn, Card, ErrorText, Field, Hint, Screen, Title } from "../../src/components/ui";
import { useConnection } from "../../src/store/connection";

export default function ServerScreen() {
  const existing = useConnection((s) => s.baseUrl);
  const [url, setUrl] = useState(existing || "http://");
  const [msg, setMsg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const router = useRouter();

  async function testAndSave() {
    setBusy(true);
    setErr(null);
    setMsg(null);
    try {
      const saved = await useConnection.getState().setBaseUrl(url);
      const result = await probeServer(saved);
      if (result.kind === "unreachable") {
        setErr(result.error);
        return;
      }
      if (result.kind === "setup") {
        setMsg("Reachable — first-run setup required.");
        router.replace("/(auth)/setup");
        return;
      }
      if (result.kind === "ok") {
        setMsg("Reachable — no login required on this server.");
        router.replace("/(app)/(tabs)");
        return;
      }
      setMsg("Reachable — sign in with your dashboard account.");
      router.replace("/(auth)/login");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Invalid URL");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Screen safe>
      <Title large>Jackery Monitor</Title>
      <Hint>
        This app talks to your self-hosted dashboard. Enter the server URL — LAN
        (http://192.168.x.x:8123) or a Cloudflare Tunnel hostname.
      </Hint>
      <Card>
        <Field
          label="Server URL"
          value={url}
          onChangeText={setUrl}
          autoComplete="off"
          keyboardType="url"
          placeholder="http://192.168.1.10:8123"
        />
        <Hint>
          Simulator: http://localhost:8000 (iOS) or http://10.0.2.2:8000 (Android emulator). Physical device: your NAS LAN IP.
        </Hint>
        <ErrorText>{err}</ErrorText>
        {msg ? <Hint>{msg}</Hint> : null}
        <Btn title="Test connection" onPress={testAndSave} loading={busy} />
      </Card>
    </Screen>
  );
}
