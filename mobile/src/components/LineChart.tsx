import { useEffect, useMemo, useRef, useState } from "react";
import { Text, View } from "react-native";
import Svg, { Circle, G, Line, Polygon, Polyline, Text as SvgText } from "react-native-svg";
import { colors } from "../theme";

export type Series = {
  id: string;
  color: string;
  values: { x: number; y: number }[];
  min?: number;
  max?: number;
  label?: string;
  unit?: string;
  axis?: "left" | "right";
  dashed?: boolean;
};

const TICKS = 4;
const CARD_W = 168;

function niceMax(n: number): number {
  if (!Number.isFinite(n) || n <= 0) return 1;
  const exp = 10 ** Math.floor(Math.log10(n));
  const f = n / exp;
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nice * exp;
}

function axisRange(group: Series[]): { min: number; max: number } {
  if (!group.length) return { min: 0, max: 1 };
  const pinnedMin = group.find((s) => s.min != null)?.min;
  const pinnedMax = group.find((s) => s.max != null)?.max;
  const ys = group.flatMap((s) => s.values.map((v) => v.y).filter((n) => Number.isFinite(n)));
  const dataMax = ys.length ? Math.max(...ys, 0) : 1;
  const min = pinnedMin ?? 0;
  const max = pinnedMax ?? niceMax(Math.max(dataMax, min + 1));
  return { min, max };
}

function fmtX(ts: number, spanS: number): string {
  if (!(ts > 1e9)) return String(Math.round(ts));
  const d = new Date(ts * 1000);
  if (spanS > 24 * 3600) {
    return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function fmtTooltipX(ts: number, spanS: number): string {
  if (!(ts > 1e9)) return String(Math.round(ts));
  const d = new Date(ts * 1000);
  if (spanS > 24 * 3600) {
    return d.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  }
  return d.toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fmtTick(n: number): string {
  if (!Number.isFinite(n)) return "";
  if (Math.abs(n) >= 100) return String(Math.round(n));
  if (Number.isInteger(n)) return String(n);
  return n.toFixed(1);
}

function fmtValue(n: number, unit?: string): string {
  if (!Number.isFinite(n)) return "—";
  if (unit === "%" || unit === "W") return String(Math.round(n));
  if (unit === "Wh") return n >= 100 ? String(Math.round(n)) : n.toFixed(1);
  return fmtTick(n);
}

function valueAt(s: Series, x: number): number | null {
  let best: { x: number; y: number } | null = null;
  let bestD = Infinity;
  for (const v of s.values) {
    if (!Number.isFinite(v.x) || !Number.isFinite(v.y)) continue;
    const d = Math.abs(v.x - x);
    if (d < bestD) {
      bestD = d;
      best = v;
    }
  }
  return best ? best.y : null;
}

export function LineChart({
  series,
  height = 180,
}: {
  series: Series[];
  height?: number;
}) {
  const [width, setWidth] = useState(0);
  const [selectedX, setSelectedX] = useState<number | null>(null);
  const gesture = useRef({ moved: false, pendingClear: false, startPx: 0 });

  const hasRight = series.some((s) => s.axis === "right");
  const pad = { l: 38, r: hasRight ? 36 : 10, t: 12, b: 22 };
  const W = Math.max(width, 1);
  const H = height;
  const innerW = Math.max(1, W - pad.l - pad.r);
  const innerH = Math.max(1, H - pad.t - pad.b);

  const left = series.filter((s) => (s.axis ?? "left") === "left");
  const right = series.filter((s) => s.axis === "right");
  const leftR = axisRange(left.length ? left : series);
  const rightR = axisRange(right.length ? right : []);

  const samples = useMemo(() => {
    const set = new Set<number>();
    for (const s of series) {
      for (const v of s.values) if (Number.isFinite(v.x)) set.add(v.x);
    }
    return [...set].sort((a, b) => a - b);
  }, [series]);

  const minX = samples.length ? samples[0] : 0;
  const maxX = samples.length ? samples[samples.length - 1] : 1;
  const spanX = maxX - minX;
  const hasData = series.some((s) => s.values.some((v) => Number.isFinite(v.y)));
  const labeled = series.filter((s) => s.label);

  useEffect(() => {
    setSelectedX((cur) => (cur == null || cur < minX || cur > maxX ? null : cur));
  }, [minX, maxX]);

  function scaleX(x: number) {
    if (maxX === minX) return pad.l + innerW / 2;
    return pad.l + ((x - minX) / (maxX - minX)) * innerW;
  }
  function scaleY(y: number, min: number, max: number) {
    if (max === min) return pad.t + innerH / 2;
    return pad.t + innerH - ((y - min) / (max - min)) * innerH;
  }

  const rangeFor = (s: Series) => ((s.axis ?? "left") === "right" ? rightR : leftR);
  const baseY = H - pad.b;

  function nearestSample(px: number): number | null {
    if (!samples.length) return null;
    const clamped = Math.max(pad.l, Math.min(W - pad.r, px));
    const target =
      maxX === minX ? minX : minX + ((clamped - pad.l) / innerW) * (maxX - minX);
    let best = samples[0];
    let bestD = Infinity;
    for (const x of samples) {
      const d = Math.abs(x - target);
      if (d < bestD) {
        bestD = d;
        best = x;
      }
    }
    return best;
  }

  function pick(px: number, moving: boolean) {
    if (px < pad.l - 8 || px > W - pad.r + 8) {
      if (!moving) setSelectedX(null);
      return;
    }
    const nx = nearestSample(px);
    if (nx == null) return;
    setSelectedX(nx);
  }

  const cx = selectedX != null ? scaleX(selectedX) : 0;
  const cardLeft =
    selectedX == null
      ? 0
      : cx > W / 2
        ? Math.max(8, cx - CARD_W - 10)
        : Math.min(cx + 10, Math.max(8, W - CARD_W - 8));

  return (
    <View onLayout={(e) => setWidth(e.nativeEvent.layout.width)}>
      {!hasData ? (
        <View style={{ height: H, justifyContent: "center" }}>
          <Text style={{ color: colors.textMute, fontSize: 12 }}>No data yet</Text>
        </View>
      ) : width < 8 ? (
        <View style={{ height: H }} />
      ) : (
        <View
          style={{ height: H, width: W }}
          onStartShouldSetResponder={() => true}
          onMoveShouldSetResponder={() => true}
          onResponderTerminationRequest={() => false}
          onResponderGrant={(e) => {
            const px = e.nativeEvent.locationX;
            gesture.current = {
              moved: false,
              pendingClear: selectedX != null && nearestSample(px) === selectedX,
              startPx: px,
            };
            pick(px, false);
          }}
          onResponderMove={(e) => {
            const px = e.nativeEvent.locationX;
            if (Math.abs(px - gesture.current.startPx) > 6) {
              gesture.current.moved = true;
              gesture.current.pendingClear = false;
            }
            pick(px, true);
          }}
          onResponderRelease={() => {
            if (gesture.current.pendingClear && !gesture.current.moved) {
              setSelectedX(null);
            }
          }}
        >
          <Svg width={W} height={H}>
            {Array.from({ length: TICKS + 1 }, (_, i) => {
              const y = pad.t + (innerH * i) / TICKS;
              return (
                <Line
                  key={`g${i}`}
                  x1={pad.l}
                  y1={y}
                  x2={W - pad.r}
                  y2={y}
                  stroke={colors.borderSoft}
                  strokeWidth={1}
                />
              );
            })}
            <Line x1={pad.l} y1={pad.t} x2={pad.l} y2={baseY} stroke={colors.border} />
            <Line x1={pad.l} y1={baseY} x2={W - pad.r} y2={baseY} stroke={colors.border} />
            {hasRight ? (
              <Line x1={W - pad.r} y1={pad.t} x2={W - pad.r} y2={baseY} stroke={colors.border} />
            ) : null}

            {Array.from({ length: TICKS + 1 }, (_, i) => {
              const frac = i / TICKS;
              const y = pad.t + innerH * frac;
              const lv = leftR.max - (leftR.max - leftR.min) * frac;
              return (
                <SvgText
                  key={`l${i}`}
                  x={pad.l - 4}
                  y={y + 3}
                  fill={colors.textMute}
                  fontSize="9"
                  textAnchor="end"
                >
                  {fmtTick(lv)}
                </SvgText>
              );
            })}
            {hasRight
              ? Array.from({ length: TICKS + 1 }, (_, i) => {
                  const frac = i / TICKS;
                  const y = pad.t + innerH * frac;
                  const rv = rightR.max - (rightR.max - rightR.min) * frac;
                  return (
                    <SvgText
                      key={`r${i}`}
                      x={W - pad.r + 4}
                      y={y + 3}
                      fill={colors.accent3}
                      fontSize="9"
                      textAnchor="start"
                    >
                      {fmtTick(rv)}
                    </SvgText>
                  );
                })
              : null}

            {Array.from({ length: TICKS + 1 }, (_, i) => {
              const x = minX + (spanX * i) / TICKS;
              return (
                <SvgText
                  key={`x${i}`}
                  x={scaleX(x)}
                  y={H - 6}
                  fill={colors.textMute}
                  fontSize="9"
                  textAnchor="middle"
                >
                  {fmtX(x, spanX)}
                </SvgText>
              );
            })}

            {series.map((s) => {
              const { min, max } = rangeFor(s);
              const pts = s.values.filter((v) => Number.isFinite(v.x) && Number.isFinite(v.y));
              if (pts.length < 2) return null;
              const mapped = pts.map((v) => ({ x: scaleX(v.x), y: scaleY(v.y, min, max) }));
              const line = mapped.map((p) => `${p.x},${p.y}`).join(" ");
              const fill =
                !s.dashed && (s.axis ?? "left") === "left"
                  ? `${mapped[0].x},${baseY} ${line} ${mapped[mapped.length - 1].x},${baseY}`
                  : null;
              return (
                <G key={s.id}>
                  {fill ? <Polygon points={fill} fill={s.color} fillOpacity={0.16} /> : null}
                  <Polyline
                    points={line}
                    fill="none"
                    stroke={s.color}
                    strokeWidth={s.dashed ? 1.75 : 2}
                    strokeDasharray={s.dashed ? "4 4" : undefined}
                  />
                </G>
              );
            })}

            {selectedX != null ? (
              <G>
                <Line
                  x1={cx}
                  y1={pad.t}
                  x2={cx}
                  y2={baseY}
                  stroke="rgba(255,255,255,0.28)"
                  strokeWidth={1}
                />
                {series.map((s) => {
                  const y = valueAt(s, selectedX);
                  if (y == null) return null;
                  const { min, max } = rangeFor(s);
                  return (
                    <Circle
                      key={`dot-${s.id}`}
                      cx={cx}
                      cy={scaleY(y, min, max)}
                      r={3.5}
                      fill={s.color}
                      stroke={colors.bg}
                      strokeWidth={1}
                    />
                  );
                })}
              </G>
            ) : null}
          </Svg>

          {selectedX != null ? (
            <View
              pointerEvents="none"
              style={{
                position: "absolute",
                top: pad.t,
                left: cardLeft,
                width: CARD_W,
                backgroundColor: colors.bgElev,
                borderColor: colors.border,
                borderWidth: 1,
                borderRadius: 12,
                paddingHorizontal: 12,
                paddingVertical: 10,
                gap: 6,
              }}
            >
              <Text style={{ color: colors.textDim, fontSize: 12 }}>
                {fmtTooltipX(selectedX, spanX)}
              </Text>
              <View style={{ height: 1, backgroundColor: colors.border }} />
              {series.map((s) => {
                const y = valueAt(s, selectedX);
                return (
                  <View
                    key={s.id}
                    style={{ flexDirection: "row", alignItems: "center", gap: 8 }}
                  >
                    <View
                      style={{
                        width: 8,
                        height: 8,
                        borderRadius: 2,
                        backgroundColor: s.color,
                      }}
                    />
                    <Text style={{ color: colors.textMute, fontSize: 12, flex: 1 }}>
                      {s.label || s.id}
                    </Text>
                    <Text style={{ color: colors.text, fontSize: 12, fontWeight: "600" }}>
                      {y == null ? "—" : fmtValue(y, s.unit)}
                    </Text>
                    {s.unit ? (
                      <Text style={{ color: colors.textMute, fontSize: 12 }}>{s.unit}</Text>
                    ) : null}
                  </View>
                );
              })}
            </View>
          ) : null}
        </View>
      )}
      {labeled.length ? (
        <View style={{ flexDirection: "row", flexWrap: "wrap", gap: 12, paddingTop: 8 }}>
          {labeled.map((s) => (
            <View key={s.id} style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
              <View
                style={{
                  width: s.dashed ? 14 : 10,
                  height: s.dashed ? 2 : 10,
                  borderRadius: s.dashed ? 0 : 2,
                  backgroundColor: s.color,
                }}
              />
              <Text style={{ color: colors.textMute, fontSize: 11 }}>{s.label}</Text>
            </View>
          ))}
        </View>
      ) : null}
    </View>
  );
}
