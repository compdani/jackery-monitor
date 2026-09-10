import { Ionicons } from "@expo/vector-icons";
import { Tabs } from "expo-router";
import type { ColorValue } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { colors } from "../../../src/theme";

const TAB_BAR_CONTENT_H = 56;

export default function TabsLayout() {
  const insets = useSafeAreaInsets();
  const bottom = Math.max(insets.bottom, 8);
  return (
    <Tabs
      screenOptions={{
        headerStyle: { backgroundColor: colors.bg },
        headerTintColor: colors.text,
        headerShadowVisible: false,
        tabBarStyle: {
          backgroundColor: colors.bgElev,
          borderTopColor: colors.border,
          height: TAB_BAR_CONTENT_H + bottom,
          paddingBottom: bottom,
          paddingTop: 6,
        },
        tabBarActiveTintColor: colors.accent,
        tabBarInactiveTintColor: colors.textMute,
        tabBarLabelStyle: { fontSize: 10 },
        tabBarHideOnKeyboard: true,
      }}
    >
      <Tabs.Screen name="index" options={{ title: "Live", tabBarIcon: icon("flash") }} />
      <Tabs.Screen name="energy" options={{ title: "Energy", tabBarIcon: icon("stats-chart") }} />
      <Tabs.Screen name="forecast" options={{ title: "Forecast", tabBarIcon: icon("sunny") }} />
      <Tabs.Screen name="device" options={{ title: "Device", tabBarIcon: icon("hardware-chip") }} />
      <Tabs.Screen name="automation" options={{ title: "Auto", tabBarIcon: icon("pulse") }} />
      <Tabs.Screen name="logs" options={{ title: "Logs", tabBarIcon: icon("list") }} />
      <Tabs.Screen name="settings" options={{ title: "Settings", tabBarIcon: icon("settings") }} />
    </Tabs>
  );
}

function icon(name: keyof typeof Ionicons.glyphMap) {
  return ({ color, size }: { color: ColorValue; size: number }) => (
    <Ionicons name={name} color={color} size={size} />
  );
}
