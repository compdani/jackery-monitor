import { View } from "react-native";
import Svg, { Polyline, Line, Text as SvgText } from "react-native-svg";
import { colors } from "../theme";

export type Series = {
  id: string;
  color: string;
  values: { x: number; y: number }[];
  min?: number;
  max?: number;
};

export function LineChart({
  series,
  height = 180,
}: {
  series: Series[];
  height?: number;
}) {
  const W = 320;
  const H = height;
  const pad = { l: 36, r: 8, t: 10, b: 18 };
  const innerW = W - pad.l - pad.r;
  const innerH = H - pad.t - pad.b;

  const xs = series.flatMap((s) => s.values.map((v) => v.x));
  const minX = xs.length ? Math.min(...xs) : 0;
  const maxX = xs.length ? Math.max(...xs) : 1;

  function scaleX(x: number) {
    if (maxX === minX) return pad.l;
    return pad.l + ((x - minX) / (maxX - minX)) * innerW;
  }
  function scaleY(y: number, min: number, max: number) {
    if (max === min) return pad.t + innerH / 2;
    return pad.t + innerH - ((y - min) / (max - min)) * innerH;
  }

  return (
    <View>
      <Svg width="100%" height={H} viewBox={`0 0 ${W} ${H}`}>
        <Line x1={pad.l} y1={pad.t} x2={pad.l} y2={H - pad.b} stroke={colors.border} />
        <Line x1={pad.l} y1={H - pad.b} x2={W - pad.r} y2={H - pad.b} stroke={colors.border} />
        {series.map((s) => {
          const ys = s.values.map((v) => v.y).filter((n) => Number.isFinite(n));
          const min = s.min ?? (ys.length ? Math.min(...ys, 0) : 0);
          const max = s.max ?? (ys.length ? Math.max(...ys, 1) : 1);
          const pts = s.values
            .filter((v) => Number.isFinite(v.y))
            .map((v) => `${scaleX(v.x)},${scaleY(v.y, min, max)}`)
            .join(" ");
          if (!pts) return null;
          return (
            <Polyline
              key={s.id}
              points={pts}
              fill="none"
              stroke={s.color}
              strokeWidth={1.75}
            />
          );
        })}
        <SvgText x={4} y={pad.t + 8} fill={colors.textMute} fontSize="8">
          {series[0]?.max ?? ""}
        </SvgText>
      </Svg>
    </View>
  );
}
