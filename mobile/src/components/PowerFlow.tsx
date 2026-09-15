import { View } from "react-native";
import Svg, { Circle, Line, Text as SvgText } from "react-native-svg";
import { headlineSoc } from "../lib/format";
import { colors } from "../theme";
import type { Telemetry } from "../api/types";

function watts(n: number): string {
  return `${Math.round(n)} W`;
}

export function PowerFlow({ t }: { t: Telemetry | null | undefined }) {
  const solar = Number(t?.solar_input_w ?? 0);
  const grid = Number(t?.ac_input_w ?? 0);
  const load = Number(t?.output_power_w ?? 0);
  const soc = headlineSoc(t);

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

  return (
    <View>
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
          SOLAR
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
  );
}
