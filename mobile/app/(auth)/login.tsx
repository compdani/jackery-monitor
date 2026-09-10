import { Link, useRouter } from "expo-router";
import { useState } from "react";
import { Text } from "react-native";
import { endpoints } from "../../src/api/client";
import { Btn, Card, ErrorText, Field, Hint, Screen, Title } from "../../src/components/ui";
import { useSession } from "../../src/store/session";
import { colors } from "../../src/theme";

export default function LoginScreen() {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const router = useRouter();

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      const r = await endpoints.login(username.trim(), password);
      await useSession.getState().setSession(r.token, r.username);
      router.replace("/(app)/(tabs)");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Login failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Screen safe>
      <Title large>Sign in</Title>
      <Hint>Dashboard username and password (the one you created at first boot — not your Jackery cloud account).</Hint>
      <Card>
        <Field label="Username" value={username} onChangeText={setUsername} autoComplete="username" />
        <Field label="Password" value={password} onChangeText={setPassword} secureTextEntry autoComplete="password" />
        <ErrorText>{err}</ErrorText>
        <Btn title="Sign in" onPress={submit} loading={busy} disabled={!username || !password} />
      </Card>
      <Link href="/(auth)/setup">
        <Text style={{ color: colors.accent2 }}>First boot? Create the admin account</Text>
      </Link>
      <Link href="/(auth)/server">
        <Text style={{ color: colors.accent2 }}>Change server URL</Text>
      </Link>
    </Screen>
  );
}
