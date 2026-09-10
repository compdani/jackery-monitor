import "react-native-gesture-handler";
import { Stack, useRouter, useSegments } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { useEffect, useState } from "react";
import { ActivityIndicator, View } from "react-native";
import { endpoints } from "../src/api/client";
import { clearWidgetSnapshot } from "../src/lib/widgetSync";
import { useConnection } from "../src/store/connection";
import { useLive } from "../src/store/live";
import { usePrefs } from "../src/store/prefs";
import { useSession } from "../src/store/session";
import { colors } from "../src/theme";

export default function RootLayout() {
  const [ready, setReady] = useState(false);
  const baseUrl = useConnection((s) => s.baseUrl);
  const token = useSession((s) => s.token);
  const connHydrated = useConnection((s) => s.hydrated);
  const sessHydrated = useSession((s) => s.hydrated);
  const segments = useSegments();
  const router = useRouter();

  useEffect(() => {
    void (async () => {
      await Promise.all([
        useConnection.getState().hydrate(),
        useSession.getState().hydrate(),
        usePrefs.getState().hydrate(),
      ]);
      const t = useSession.getState().token;
      if (t && useConnection.getState().baseUrl) {
        try {
          await endpoints.me();
        } catch {
          await useSession.getState().clear();
        }
      }
      if (!useLive.getState().status) clearWidgetSnapshot();
      setReady(true);
    })();
  }, []);

  useEffect(() => {
    if (!ready || !connHydrated || !sessHydrated) return;
    const inAuth = segments[0] === "(auth)";
    if (!baseUrl) {
      if (!inAuth) router.replace("/(auth)/server");
      return;
    }
    if (!token) {
      if (!inAuth) router.replace("/(auth)/login");
      return;
    }
    if (inAuth) router.replace("/(app)/(tabs)");
  }, [ready, connHydrated, sessHydrated, baseUrl, token, segments, router]);

  if (!ready) {
    return (
      <View style={{ flex: 1, backgroundColor: colors.bg, alignItems: "center", justifyContent: "center" }}>
        <ActivityIndicator color={colors.accent} />
        <StatusBar style="light" />
      </View>
    );
  }

  return (
    <>
      <StatusBar style="light" />
      <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: colors.bg } }} />
    </>
  );
}
