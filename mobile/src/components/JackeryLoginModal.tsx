import { useState } from "react";
import { Modal, View } from "react-native";
import { endpoints } from "../api/client";
import { Btn, Card, ErrorText, Field, Hint, Segmented, Title } from "./ui";

export function JackeryLoginModal({
  visible,
  onDone,
}: {
  visible: boolean;
  onDone: () => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [region, setRegion] = useState("US");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    setBusy(true);
    setErr(null);
    try {
      await endpoints.setCloudCreds(email.trim(), password, region);
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal visible={visible} animationType="slide" transparent>
      <View style={{ flex: 1, backgroundColor: "rgba(0,0,0,0.65)", justifyContent: "center", padding: 20 }}>
        <Card>
          <Title>Jackery cloud</Title>
          <Hint>
            Sign in with your Jackery account. Credentials are encrypted on the server — they never leave that host except to Jackery.
          </Hint>
          <Field label="Email" value={email} onChangeText={setEmail} keyboardType="email-address" />
          <Field label="Password" value={password} onChangeText={setPassword} secureTextEntry />
          <Segmented
            value={region}
            onChange={setRegion}
            options={[
              { id: "US", label: "US" },
              { id: "EU", label: "EU" },
              { id: "CN", label: "CN" },
            ]}
          />
          <ErrorText>{err}</ErrorText>
          <Btn title="Sign in" onPress={submit} loading={busy} disabled={!email || !password} />
        </Card>
      </View>
    </Modal>
  );
}
