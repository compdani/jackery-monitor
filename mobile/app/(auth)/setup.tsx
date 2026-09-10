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
  const [host, setHost] = useState("");
  const [share, setShare] = useState("");
  const [smbUser, setSmbUser] = useState("");
  const [smbPass, setSmbPass] = useState("");
  const [snaps, setSnaps] = useState<string[]>([]);
  const [restoreMsg, setRestoreMsg] = useState<string | null>(null);
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

  const creds = {
    transport: "smb",
    host,
    share,
    username: smbUser,
    password: smbPass,
  };

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
      <Card>
        <Title>Restore from backup</Title>
        <Hint>Optional. Pull a snapshot from SMB before creating the admin account.</Hint>
        <Field label="NAS host" value={host} onChangeText={setHost} />
        <Field label="Share" value={share} onChangeText={setShare} />
        <Field label="Username" value={smbUser} onChangeText={setSmbUser} />
        <Field label="Password" value={smbPass} onChangeText={setSmbPass} secureTextEntry />
        <Btn
          title="List snapshots"
          kind="ghost"
          onPress={() =>
            void endpoints.setupRestoreSnapshots(creds).then((j) => {
              const r = j as { snapshots?: { name?: string }[] | string[] };
              const list = r.snapshots || [];
              setSnaps(list.map((x) => (typeof x === "string" ? x : x.name || JSON.stringify(x))));
              setRestoreMsg(`Found ${list.length} snapshot(s)`);
            }).catch((e) => setRestoreMsg(e instanceof Error ? e.message : "Failed"))
          }
        />
        {snaps.map((s) => (
          <Btn
            key={s}
            title={`Restore ${s}`}
            kind="ghost"
            onPress={() =>
              void endpoints.setupRestore({ ...creds, name: s }).then(() => setRestoreMsg("Restore started"))
            }
          />
        ))}
        {restoreMsg ? <Hint>{restoreMsg}</Hint> : null}
      </Card>
      <Link href="/(auth)/login">
        <Text style={{ color: colors.accent2 }}>Already set up? Sign in</Text>
      </Link>
      <Link href="/(auth)/server">
        <Text style={{ color: colors.accent2 }}>Change server URL</Text>
      </Link>
    </Screen>
  );
}
