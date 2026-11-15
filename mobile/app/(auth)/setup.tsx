import { Link, useRouter } from "expo-router";
import { useState } from "react";
import { Text } from "react-native";
import { endpoints } from "../../src/api/client";
import { Btn, Card, ErrorText, Field, Hint, Screen, Title } from "../../src/components/ui";
import { useSession } from "../../src/store/session";
import { colors } from "../../src/theme";

export default function SetupScreen() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const router = useRouter();

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      const r = await endpoints.setup(username.trim(), password);
      await useSession.getState().setSession(r.token, r.username);
      router.replace("/(app)/(tabs)");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Setup failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Screen safe>
      <Title large>Create admin</Title>
      <Hint>One-time setup on this server. Password must be at least 6 characters.</Hint>
      <Card>
        <Field label="Username" value={username} onChangeText={setUsername} />
        <Field label="Password" value={password} onChangeText={setPassword} secureTextEntry />
        <ErrorText>{err}</ErrorText>
        <Btn title="Create account" onPress={submit} loading={busy} disabled={!username || password.length < 6} />
      </Card>
      <Hint>Local backup restore during setup is pending migration. Remote backups are retired.</Hint>
      <Link href="/(auth)/login">
        <Text style={{ color: colors.accent2 }}>Already set up? Sign in</Text>
      </Link>
      <Link href="/(auth)/server">
        <Text style={{ color: colors.accent2 }}>Change server URL</Text>
      </Link>
    </Screen>
  );
}
