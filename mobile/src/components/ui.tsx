import { type ReactNode, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  View,
  type TextInputProps,
  type ViewStyle,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { colors, radius } from "../theme";

export function Screen({
  children,
  padded = true,
  safe = false,
}: {
  children: ReactNode;
  padded?: boolean;
  /** Pad for the status bar / home indicator. Use on headerless screens (auth). */
  safe?: boolean;
}) {
  const insets = useSafeAreaInsets();
  const pad = padded ? 16 : 0;
  return (
    <ScrollView
      style={styles.screen}
      contentContainerStyle={[
        styles.screenContent,
        padded && styles.padded,
        safe && {
          paddingTop: pad + insets.top,
          paddingBottom: 40 + insets.bottom,
          paddingLeft: pad + insets.left,
          paddingRight: pad + insets.right,
        },
      ]}
      keyboardShouldPersistTaps="handled"
      automaticallyAdjustKeyboardInsets
    >
      {children}
    </ScrollView>
  );
}

export function Card({
  children,
  style,
}: {
  children: ReactNode;
  style?: ViewStyle;
}) {
  return <View style={[styles.card, style]}>{children}</View>;
}

export function Eyebrow({ children }: { children: ReactNode }) {
  return <Text style={styles.eyebrow}>{children}</Text>;
}

export function Title({
  children,
  large,
}: {
  children: ReactNode;
  large?: boolean;
}) {
  return <Text style={[styles.title, large && styles.titleLarge]}>{children}</Text>;
}

export function Hint({ children }: { children: ReactNode }) {
  return <Text style={styles.hint}>{children}</Text>;
}

export function ErrorText({ children }: { children: ReactNode }) {
  if (!children) return null;
  return <Text style={styles.error}>{children}</Text>;
}

export function Pill({
  label,
  tone = "mute",
}: {
  label: string;
  tone?: "ok" | "warn" | "err" | "mute" | "info";
}) {
  const bg =
    tone === "ok"
      ? "rgba(74,222,128,0.16)"
      : tone === "err"
        ? "rgba(239,68,68,0.16)"
        : tone === "warn"
          ? "rgba(251,191,36,0.16)"
          : tone === "info"
            ? "rgba(56,189,248,0.16)"
            : colors.bgElev2;
  const fg =
    tone === "ok"
      ? colors.accent
      : tone === "err"
        ? colors.danger
        : tone === "warn"
          ? colors.accent3
          : tone === "info"
            ? colors.accent2
            : colors.textDim;
  return (
    <View style={[styles.pill, { backgroundColor: bg }]}>
      <Text style={[styles.pillText, { color: fg }]}>{label}</Text>
    </View>
  );
}

export function Btn({
  title,
  onPress,
  kind = "primary",
  disabled,
  loading,
}: {
  title: string;
  onPress: () => void;
  kind?: "primary" | "ghost" | "danger";
  disabled?: boolean;
  loading?: boolean;
}) {
  const bg =
    kind === "primary" ? colors.accent : kind === "danger" ? colors.danger : "transparent";
  const fg = kind === "ghost" ? colors.accent2 : colors.bg;
  return (
    <Pressable
      onPress={onPress}
      disabled={disabled || loading}
      style={({ pressed }) => [
        styles.btn,
        { backgroundColor: bg, borderColor: kind === "ghost" ? colors.border : bg, opacity: pressed || disabled ? 0.7 : 1 },
      ]}
    >
      {loading ? (
        <ActivityIndicator color={fg} />
      ) : (
        <Text style={[styles.btnText, { color: kind === "ghost" ? colors.accent2 : fg }]}>{title}</Text>
      )}
    </Pressable>
  );
}

export function Field({
  label,
  ...rest
}: { label: string } & TextInputProps) {
  return (
    <View style={styles.field}>
      <Text style={styles.fieldLabel}>{label}</Text>
      <TextInput
        placeholderTextColor={colors.textMute}
        style={styles.input}
        autoCapitalize="none"
        autoCorrect={false}
        {...rest}
      />
    </View>
  );
}

export function RowSwitch({
  label,
  hint,
  value,
  onValueChange,
  disabled,
}: {
  label: string;
  hint?: string;
  value: boolean;
  onValueChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <View style={styles.switchRow}>
      <View style={{ flex: 1 }}>
        <Text style={styles.fieldLabel}>{label}</Text>
        {hint ? <Text style={styles.hint}>{hint}</Text> : null}
      </View>
      <Switch
        value={value}
        onValueChange={onValueChange}
        disabled={disabled}
        trackColor={{ true: colors.accent, false: colors.border }}
      />
    </View>
  );
}

export function Segmented({
  options,
  value,
  onChange,
}: {
  options: { id: string; label: string }[];
  value: string;
  onChange: (id: string) => void;
}) {
  return (
    <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.seg}>
      {options.map((o) => {
        const on = o.id === value;
        return (
          <Pressable
            key={o.id}
            onPress={() => onChange(o.id)}
            style={[styles.segBtn, on && styles.segOn]}
          >
            <Text style={[styles.segText, on && styles.segTextOn]}>{o.label}</Text>
          </Pressable>
        );
      })}
    </ScrollView>
  );
}

export function Kpi({
  label,
  value,
  unit,
  sub,
}: {
  label: string;
  value: string;
  unit?: string;
  sub?: string;
}) {
  return (
    <Card>
      <Eyebrow>{label}</Eyebrow>
      <View style={styles.kpiRow}>
        <Text style={styles.kpiVal}>{value}</Text>
        {unit ? <Text style={styles.kpiUnit}>{unit}</Text> : null}
      </View>
      {sub ? <Text style={styles.hint}>{sub}</Text> : null}
    </Card>
  );
}

export function Collapsible({
  title,
  summary,
  defaultOpen = false,
  children,
}: {
  title: string;
  summary?: string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <Card>
      <Pressable
        onPress={() => setOpen((v) => !v)}
        accessibilityRole="button"
        accessibilityState={{ expanded: open }}
        style={styles.collapseHeader}
      >
        <View style={{ flex: 1, gap: 2 }}>
          <Text style={styles.title}>{title}</Text>
          {summary ? <Text style={styles.hint}>{summary}</Text> : null}
        </View>
        <Text style={styles.chevron}>{open ? "▾" : "▸"}</Text>
      </Pressable>
      {open ? <View style={styles.collapseBody}>{children}</View> : null}
    </Card>
  );
}

export function EnergyKpi({
  label,
  consumed,
  charged,
  sub,
  children,
}: {
  label: string;
  consumed: string;
  charged: string;
  sub?: string;
  children?: ReactNode;
}) {
  return (
    <Card>
      <Eyebrow>{label}</Eyebrow>
      <View style={styles.energyKpiRow}>
        <View style={styles.energyKpiCol}>
          <View style={styles.kpiRow}>
            <Text style={styles.kpiConsumed}>{consumed}</Text>
            <Text style={styles.kpiUnit}>kWh</Text>
          </View>
          <Text style={styles.hint}>consumed</Text>
        </View>
        <View style={styles.energyKpiCol}>
          <View style={styles.kpiRow}>
            <Text style={styles.kpiCharged}>{charged}</Text>
            <Text style={styles.kpiUnit}>kWh</Text>
          </View>
          <Text style={styles.hint}>charged</Text>
        </View>
      </View>
      {sub ? <Text style={styles.hint}>{sub}</Text> : null}
      {children}
    </Card>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.bg },
  screenContent: { paddingBottom: 40 },
  padded: { padding: 16, gap: 12 },
  card: {
    backgroundColor: colors.bgElev,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 14,
    gap: 8,
  },
  eyebrow: { color: colors.textDim, fontSize: 12, fontWeight: "600", textTransform: "uppercase", letterSpacing: 0.4 },
  title: { color: colors.text, fontSize: 17, fontWeight: "600" },
  titleLarge: { fontSize: 28, fontWeight: "700", letterSpacing: -0.4 },
  hint: { color: colors.textMute, fontSize: 12, lineHeight: 17 },
  error: { color: colors.danger, fontSize: 13 },
  pill: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: 999, alignSelf: "flex-start" },
  pillText: { fontSize: 11, fontWeight: "600" },
  btn: {
    paddingHorizontal: 14,
    paddingVertical: 10,
    borderRadius: radius.sm,
    borderWidth: 1,
    alignItems: "center",
    justifyContent: "center",
    minHeight: 40,
  },
  btnText: { fontWeight: "600", fontSize: 14 },
  field: { gap: 6 },
  fieldLabel: { color: colors.textDim, fontSize: 13, fontWeight: "500" },
  input: {
    backgroundColor: colors.bgElev2,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.sm,
    color: colors.text,
    paddingHorizontal: 12,
    paddingVertical: 10,
    fontSize: 15,
  },
  switchRow: { flexDirection: "row", alignItems: "center", gap: 12 },
  seg: { flexDirection: "row", gap: 6 },
  segBtn: {
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 999,
    backgroundColor: colors.bgElev2,
    borderWidth: 1,
    borderColor: colors.border,
  },
  segOn: { backgroundColor: "rgba(74,222,128,0.16)", borderColor: colors.accent },
  segText: { color: colors.textDim, fontSize: 12, fontWeight: "600" },
  segTextOn: { color: colors.accent },
  kpiRow: { flexDirection: "row", alignItems: "baseline", gap: 4 },
  kpiVal: { color: colors.text, fontSize: 28, fontWeight: "700" },
  kpiUnit: { color: colors.textDim, fontSize: 13 },
  energyKpiRow: { flexDirection: "row", gap: 12 },
  energyKpiCol: { flex: 1 },
  kpiConsumed: { color: colors.grid, fontSize: 22, fontWeight: "700" },
  kpiCharged: { color: colors.accent, fontSize: 22, fontWeight: "700" },
  collapseHeader: { flexDirection: "row", alignItems: "center", gap: 8 },
  collapseBody: { gap: 8 },
  chevron: { color: colors.textMute, fontSize: 16, paddingHorizontal: 4 },
});
