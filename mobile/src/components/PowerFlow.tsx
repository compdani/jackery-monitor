import { View } from "react-native";
import Svg, { Circle, Line, Text as SvgText } from "react-native-svg";
import { headlineSoc } from "../lib/format";
import { colors } from "../theme";
import type { Telemetry } from "../api/types";

export function PowerFlow({ t }: { t: Telemetry | null | undefined }) {
  const solar = Number(t?.solar_input_w ?? 0);
  const grid = Number(t?.ac_input_w ?? 0);
  const load = Number(t?.output_power_w ?? 0);
  const soc = headlineSoc(t);
  const W = 340;
  const H = 150;
  return (
    <View>
      <Svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`}>
        <Line x1={50} y1={40} x2={170} y2={75} stroke={solar > 5 ? colors.solar : colors.border} strokeWidth={solar > 5 ? 3 : 1.5} />
        <Line x1={50} y1={110} x2={170} y2={75} stroke={grid > 5 ? colors.grid : colors.border} strokeWidth={grid > 5 ? 3 : 1.5} />
        <Line x1={170} y1={75} x2={290} y2={75} stroke={load > 5 ? colors.load : colors.border} strokeWidth={load > 5 ? 3 : 1.5} />
        <Circle cx={50} cy={40} r={18} fill={colors.bgElev2} stroke={colors.solar} strokeWidth={2} />
        <Circle cx={50} cy={110} r={18} fill={colors.bgElev2} stroke={colors.grid} strokeWidth={2} />
        <Circle cx={170} cy={75} r={28} fill={colors.bgElev2} stroke={colors.accent2} strokeWidth={2} />
        <Circle cx={290} cy={75} r={18} fill={colors.bgElev2} stroke={colors.load} strokeWidth={2} />
        <SvgText x={50} y={18} fill={colors.textMute} fontSize="9" textAnchor="middle">SOLAR</SvgText>
        <SvgText x={50} y={144} fill={colors.textMute} fontSize="9" textAnchor="middle">GRID</SvgText>
        <SvgText x={170} y={38} fill={colors.textMute} fontSize="9" textAnchor="middle">BATTERY</SvgText>
        <SvgText x={290} y={48} fill={colors.textMute} fontSize="9" textAnchor="middle">LOADS</SvgText>
        <SvgText x={50} y={44} fill={colors.text} fontSize="11" textAnchor="middle">☀</SvgText>
        <SvgText x={50} y={114} fill={colors.text} fontSize="11" textAnchor="middle">⚡</SvgText>
        <SvgText x={170} y={80} fill={colors.text} fontSize="13" fontWeight="700" textAnchor="middle">
          {soc == null ? "—" : `${Math.round(soc)}%`}
        </SvgText>
        <SvgText x={290} y={79} fill={colors.text} fontSize="11" textAnchor="middle">⌂</SvgText>
        <SvgText x={50} y={66} fill={colors.solar} fontSize="9" textAnchor="middle">{Math.round(solar)} W</SvgText>
        <SvgText x={50} y={136} fill={colors.grid} fontSize="9" textAnchor="middle">{Math.round(grid)} W</SvgText>
        <SvgText x={290} y={108} fill={colors.text} fontSize="9" textAnchor="middle">{Math.round(load)} W</SvgText>
      </Svg>
    </View>
  );
}
