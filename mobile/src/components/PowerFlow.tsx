import { useState } from "react";
import { Pressable, Text, View } from "react-native";
import Svg, { Circle, Line, Text as SvgText } from "react-native-svg";
import { endpoints } from "../api/client";
import type { LoadInput, SolarInput, Telemetry } from "../api/types";
import { headlineSoc } from "../lib/format";
import { patchDevicePrefs } from "../lib/siseli";
import { colors } from "../theme";

function watts(n: number): string {
  return `${Math.round(n)} W`;
}

function selectedIds(
  pref: string[] | null | undefined,
  inputs: LoadInput[],
): Set<string> {
  if (Array.isArray(pref)) return new Set(pref.map(String));
  return new Set(inputs.map((row) => String(row.id)));
}

export function PowerFlow({
  t,
  deviceId,
  unified = false,
  loadSources,
}: {
  t: Telemetry | null | undefined;
  deviceId?: string;
  unified?: boolean;
  loadSources?: string[] | null;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const inputs = (Array.isArray(t?.solar_inputs) ? t!.solar_inputs : []) as SolarInput[];
  const loadInputs = (Array.isArray(t?.load_inputs) ? t!.load_inputs : []) as LoadInput[];
  const solarFromInputs = inputs.reduce((sum, row) => sum + Math.max(0, Number(row.watts) || 0), 0);
  const solar = inputs.length ? solarFromInputs : Number(t?.solar_input_w ?? 0);
  const grid = Number(t?.ac_input_w ?? 0);
  const selected = selectedIds(loadSources, loadInputs);
  const loadFromInputs = loadInputs.length
    ? loadInputs.reduce(
        (sum, row) => (selected.has(String(row.id)) ? sum + Math.max(0, Number(row.watts) || 0) : sum),
        0,
      )
    : Number(t?.output_power_w ?? 0);
  const load = loadFromInputs;
  const soc = headlineSoc(t);
  const showChips = !unified && inputs.length > 1;
  const showLoadGear = loadInputs.length > 1;

  const W = 360;
  const H = 228;
  const leftX = 64;
  const batX = 186;
  const loadX = 308;
  const solarY = 56;
  const gridY = 176;
  const midY = (solarY + gridY) / 2;
  const r = 22;
  const rBat = 36;

  async function toggleUnified() {
    if (!deviceId) return;
    const next = !unified;
    patchDevicePrefs({ solar_flow_unified: next });
    try {
      await endpoints.saveDevicePrefs({
        device_id: deviceId,
        solar_flow_unified: next,
      });
    } catch {
      patchDevicePrefs({ solar_flow_unified: unified });
    }
  }

  async function toggleLoadSource(id: string) {
    if (!deviceId) return;
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    const list = [...next];
    const prev = loadSources ?? null;
    patchDevicePrefs({ load_sources: list });
    try {
      await endpoints.saveDevicePrefs({
        device_id: deviceId,
        load_sources: list,
      });
    } catch {
      patchDevicePrefs({ load_sources: prev ?? undefined });
    }
  }

  return (
    <View>
      {inputs.length > 1 ? (
        <Pressable
          onPress={() => void toggleUnified()}
          style={{ alignSelf: "flex-end", marginBottom: 8, paddingVertical: 4 }}
        >
          <Text style={{ color: colors.accent, fontSize: 12 }}>
            {unified ? "Show separate solar inputs" : "Unified solar"}
          </Text>
        </Pressable>
      ) : null}
      {showChips ? (
        <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 8, marginBottom: 10 }}>
          {inputs.map((row) => (
            <View
              key={String(row.id || row.label)}
              style={{
                borderWidth: 1,
                borderColor: row.source === "ecoflow" ? colors.accent2 : colors.solar,
                borderRadius: 8,
                paddingHorizontal: 10,
                paddingVertical: 6,
                minWidth: 88,
              }}
            >
              <Text style={{ color: colors.textMute, fontSize: 11 }}>{row.label || "Solar"}</Text>
              <Text
                style={{
                  color: row.source === "ecoflow" ? colors.accent2 : colors.solar,
                  fontSize: 14,
                  fontWeight: "600",
                }}
              >
                {watts(Number(row.watts) || 0)}
              </Text>
            </View>
          ))}
        </View>
      ) : null}
      <View style={{ position: "relative" }}>
        {showLoadGear ? (
          <View style={{ position: "absolute", right: 4, top: 4, zIndex: 2 }}>
            <Pressable
              onPress={() => setMenuOpen((v) => !v)}
              accessibilityLabel="Choose load sources"
              style={{
                width: 28,
                height: 28,
                borderRadius: 8,
                borderWidth: 1,
                borderColor: colors.border,
                backgroundColor: colors.bgElev,
                alignItems: "center",
                justifyContent: "center",
              }}
            >
              <Text style={{ color: colors.textMute, fontSize: 14 }}>⚙</Text>
            </Pressable>
            {menuOpen ? (
              <View
                style={{
                  position: "absolute",
                  right: 0,
                  top: 34,
                  minWidth: 180,
                  padding: 8,
                  borderRadius: 10,
                  borderWidth: 1,
                  borderColor: colors.border,
                  backgroundColor: colors.bgElev,
                  gap: 4,
                }}
              >
                {loadInputs.map((row) => {
                  const id = String(row.id || "");
                  const on = selected.has(id);
                  return (
                    <Pressable
                      key={id}
                      onPress={() => void toggleLoadSource(id)}
                      style={{
                        flexDirection: "row",
                        alignItems: "center",
                        gap: 8,
                        paddingVertical: 6,
                        paddingHorizontal: 6,
                      }}
                    >
                      <Text style={{ color: on ? colors.accent : colors.textMute, fontSize: 14 }}>
                        {on ? "☑" : "☐"}
                      </Text>
                      <Text style={{ color: colors.text, fontSize: 12, flex: 1 }}>
                        {row.label || id}
                      </Text>
                      <Text style={{ color: colors.textMute, fontSize: 12 }}>
                        {watts(Number(row.watts) || 0)}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
            ) : null}
          </View>
        ) : null}
        <Svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`}>
          <Line
            x1={leftX}
            y1={solarY}
            x2={batX}
            y2={midY}
            stroke={solar > 5 ? colors.solar : colors.border}
            strokeWidth={solar > 5 ? 3 : 1.5}
          />
          <Line
            x1={leftX}
            y1={gridY}
            x2={batX}
            y2={midY}
            stroke={grid > 5 ? colors.grid : colors.border}
            strokeWidth={grid > 5 ? 3 : 1.5}
          />
          <Line
            x1={batX}
            y1={midY}
            x2={loadX}
            y2={midY}
            stroke={load > 5 ? colors.load : colors.border}
            strokeWidth={load > 5 ? 3 : 1.5}
          />

          <Circle cx={leftX} cy={solarY} r={r} fill={colors.bgElev2} stroke={colors.solar} strokeWidth={2} />
          <Circle cx={leftX} cy={gridY} r={r} fill={colors.bgElev2} stroke={colors.grid} strokeWidth={2} />
          <Circle cx={batX} cy={midY} r={rBat} fill={colors.bgElev2} stroke={colors.accent2} strokeWidth={2} />
          <Circle cx={loadX} cy={midY} r={r} fill={colors.bgElev2} stroke={colors.load} strokeWidth={2} />

          <SvgText x={leftX} y={solarY - r - 10} fill={colors.textMute} fontSize="11" textAnchor="middle">
            {showChips ? `SOLAR (${inputs.length})` : "SOLAR"}
          </SvgText>
          <SvgText
            x={leftX}
            y={solarY}
            fill={colors.text}
            fontSize="13"
            textAnchor="middle"
            alignmentBaseline="central"
          >
            ☀
          </SvgText>
          <SvgText x={leftX} y={solarY + r + 16} fill={colors.solar} fontSize="11" textAnchor="middle">
            {watts(solar)}
          </SvgText>

          <SvgText x={leftX} y={gridY - r - 10} fill={colors.textMute} fontSize="11" textAnchor="middle">
            GRID
          </SvgText>
          <SvgText
            x={leftX}
            y={gridY}
            fill={colors.text}
            fontSize="13"
            textAnchor="middle"
            alignmentBaseline="central"
          >
            ⚡
          </SvgText>
          <SvgText x={leftX} y={gridY + r + 16} fill={colors.grid} fontSize="11" textAnchor="middle">
            {watts(grid)}
          </SvgText>

          <SvgText x={batX} y={midY - rBat - 10} fill={colors.textMute} fontSize="11" textAnchor="middle">
            BATTERY
          </SvgText>
          <SvgText
            x={batX}
            y={midY}
            fill={colors.text}
            fontSize="16"
            fontWeight="700"
            textAnchor="middle"
            alignmentBaseline="central"
          >
            {soc == null ? "—" : `${Math.round(soc)}%`}
          </SvgText>

          <SvgText x={loadX} y={midY - rBat - 10} fill={colors.textMute} fontSize="11" textAnchor="middle">
            LOADS
          </SvgText>
          <SvgText
            x={loadX}
            y={midY}
            fill={colors.text}
            fontSize="13"
            textAnchor="middle"
            alignmentBaseline="central"
          >
            ⌂
          </SvgText>
          <SvgText x={loadX} y={midY + r + 16} fill={colors.text} fontSize="11" textAnchor="middle">
            {watts(load)}
          </SvgText>
        </Svg>
      </View>
    </View>
  );
}
