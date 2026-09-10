import * as Clipboard from "expo-clipboard";
import { useCallback, useEffect, useState } from "react";
import { Text } from "react-native";
import { endpoints } from "../../../src/api/client";
import { Btn, Card, Eyebrow, Hint, Screen, Segmented } from "../../../src/components/ui";
import { colors } from "../../../src/theme";

type Ev = { ts?: number; level?: string; category?: string; message?: string };

export default function LogsScreen() {
  const [level, setLevel] = useState("all");
  const [events, setEvents] = useState<Ev[]>([]);
  const [debug, setDebug] = useState<string>("");

  const load = useCallback(async () => {
    try {
      const r = await endpoints.events(250);
      setEvents((r.events as Ev[]) || []);
    } catch {
      setEvents([]);
    }
  }, []);

  useEffect(() => {
    void load();
    const id = setInterval(load, 8000);
    return () => clearInterval(id);
  }, [load]);

  const filtered = events.filter((e) => level === "all" || (e.level || "").toLowerCase() === level);

  async function copyDebug() {
    const parts = await Promise.allSettled([
      endpoints.forecastAccuracy(),
      endpoints.smartAnalytics(undefined, 14),
      endpoints.cloudProbe(),
    ]);
    const blob = JSON.stringify(
      parts.map((p) => (p.status === "fulfilled" ? p.value : String(p.reason))),
      null,
      2,
    );
    setDebug(blob);
    await Clipboard.setStringAsync(blob);
  }

  return (
    <Screen>
      <Card>
        <Eyebrow>Bridge events</Eyebrow>
        <Segmented
          value={level}
          onChange={setLevel}
          options={[
            { id: "all", label: "All" },
            { id: "info", label: "Info" },
            { id: "warn", label: "Warn" },
            { id: "error", label: "Error" },
          ]}
        />
        {filtered.slice(0, 80).map((e, i) => (
          <Text key={i} style={{ color: e.level === "error" ? colors.danger : colors.textDim, fontSize: 12 }}>
            {e.level || "info"} {e.category ? `[${e.category}] ` : ""}
            {e.message}
          </Text>
        ))}
        {!filtered.length ? <Hint>No events yet.</Hint> : null}
        <Btn title="Refresh" kind="ghost" onPress={() => void load()} />
      </Card>
      <Card>
        <Eyebrow>Debug dump</Eyebrow>
        <Hint>Copies forecast accuracy, smart-charge analytics, and cloud probe JSON.</Hint>
        <Btn title="Copy all debug" onPress={() => void copyDebug()} />
        {debug ? (
          <Text style={{ color: colors.textMute, fontSize: 10 }}>{debug.slice(0, 2500)}</Text>
        ) : null}
      </Card>
    </Screen>
  );
}
