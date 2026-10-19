// @ts-nocheck
/** Canvas chart renderers ported from web/app.js — keep visual output identical. */

import { fmt } from '../format';

let lastStatus = null;

export const SERIES_COLORS = {
  battery: '#fbbf24',
  output: '#4ade80',
  input: '#38bdf8',
} as const;

export type SeriesVisible = {
  battery?: boolean;
  output?: boolean;
  input?: boolean;
  soc?: boolean;
  load?: boolean;
  solar?: boolean;
};

function $(id: string): HTMLElement | null {
  return document.getElementById(id);
}


let _seriesVisible: Record<string, SeriesVisible> = {
  live: { battery: true, output: true, input: true },
  energy: { battery: true, output: true, input: true },
  forecast: { soc: true, load: true, solar: true },
};

export function setSeriesVisible(chart: string, series: SeriesVisible) {
  _seriesVisible[chart] = { ..._seriesVisible[chart], ...series };
}

export function toggleSeries(chart: string, series: string) {
  const cur = _seriesVisible[chart] || {};
  (cur as any)[series] = !(cur as any)[series];
  _seriesVisible[chart] = cur;
  return !!(cur as any)[series];
}

export function isSeriesVisible(chart: string, series: string): boolean {
  const cur = _seriesVisible[chart] as any;
  return cur?.[series] !== false;
}

// energyCompareColor — deterministic palette for multi-device compare
const COMPARE_PALETTE = ['#38bdf8', '#a78bfa', '#f472b6', '#fb923c', '#4ade80', '#fbbf24'];
export function energyCompareColor(deviceSn: string): string {
  let h = 0;
  for (let i = 0; i < deviceSn.length; i++) h = (h * 31 + deviceSn.charCodeAt(i)) | 0;
  return COMPARE_PALETTE[Math.abs(h) % COMPARE_PALETTE.length];
}

// ============================================================
// CHART RENDERERS (canvas, no deps)
// ============================================================
export function setCanvasSize(canvas) {
  const dpr = window.devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  canvas.width = Math.max(1, Math.floor(rect.width * dpr));
  canvas.height = Math.max(1, Math.floor(rect.height * dpr));
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, w: rect.width, h: rect.height };
}

function chartCss(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

function chartMuted(): string {
  return chartCss('--muted-foreground', '#6b7280');
}

function chartBorder(): string {
  return chartCss('--border', '#232a33');
}

function chartGrid(): string {
  // Dark gridlines vanish on a light canvas; light ones vanish in dark mode.
  return document.documentElement.classList.contains('dark')
    ? 'rgba(35,42,51,.7)'
    : 'rgba(15,23,42,.08)';
}

function chartCrosshair(): string {
  return document.documentElement.classList.contains('dark')
    ? 'rgba(255,255,255,.22)'
    : 'rgba(15,23,42,.35)';
}

function drawAxes(ctx, w, h, padL, padR, padT, padB) {
  ctx.strokeStyle = chartBorder();
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(padL, padT);
  ctx.lineTo(padL, h - padB);
  ctx.lineTo(w - padR, h - padB);
  ctx.stroke();
}

// Monotone cubic (Fritsch–Carlson) through `pts` ([x,y] or null).
// Smooth, no x-reversal, no overshoot past neighbouring samples.
// Nulls lift the pen. `resume` skips moveTo on the first run so a fill
// that already lineTo'd the first point stays one subpath.
function _monotoneTangents(pts) {
  const n = pts.length;
  const m = new Array(n).fill(0);
  if (n < 2) return m;
  const d = new Array(n - 1);
  for (let i = 0; i < n - 1; i++) {
    const dx = pts[i + 1][0] - pts[i][0];
    d[i] = dx === 0 ? 0 : (pts[i + 1][1] - pts[i][1]) / dx;
  }
  m[0] = d[0];
  m[n - 1] = d[n - 2];
  for (let i = 1; i < n - 1; i++) {
    m[i] = (d[i - 1] * d[i] <= 0) ? 0 : (d[i - 1] + d[i]) / 2;
  }
  for (let i = 0; i < n - 1; i++) {
    if (d[i] === 0) {
      m[i] = 0;
      m[i + 1] = 0;
      continue;
    }
    const a = m[i] / d[i];
    const b = m[i + 1] / d[i];
    const s = a * a + b * b;
    if (s > 9) {
      const t = 3 / Math.sqrt(s);
      m[i] = t * a * d[i];
      m[i + 1] = t * b * d[i];
    }
  }
  return m;
}

function _strokeRun(ctx, run, resume) {
  if (!run.length) return;
  if (!resume) ctx.moveTo(run[0][0], run[0][1]);
  if (run.length === 1) return;
  if (run.length === 2) {
    ctx.lineTo(run[1][0], run[1][1]);
    return;
  }
  const m = _monotoneTangents(run);
  for (let i = 0; i < run.length - 1; i++) {
    const p0 = run[i], p1 = run[i + 1];
    const dx = p1[0] - p0[0];
    ctx.bezierCurveTo(
      p0[0] + dx / 3, p0[1] + (m[i] * dx) / 3,
      p1[0] - dx / 3, p1[1] - (m[i + 1] * dx) / 3,
      p1[0], p1[1],
    );
  }
}

function _smoothPath(ctx, pts, opts) {
  const resumeFirst = !!(opts && opts.resume);
  let i = 0;
  let isFirstRun = true;
  while (i < pts.length) {
    if (!pts[i]) { i++; continue; }
    let j = i + 1;
    while (j < pts.length && pts[j]) j++;
    _strokeRun(ctx, pts.slice(i, j), resumeFirst && isFirstRun);
    isFirstRun = false;
    i = j;
  }
}

// Stroke a smooth line through a series of values. `xs(i)`/`ys(v)` map
// data-space to canvas-space; null/NaN values lift the pen.
function drawSmoothLine(ctx, points, xs, ys, color, lineWidth = 2.5) {
  const pts = points.map((v, i) =>
    (v == null || Number.isNaN(v)) ? null : [xs(i), ys(v)]);
  ctx.strokeStyle = color;
  ctx.lineWidth = lineWidth;
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';
  ctx.beginPath();
  _smoothPath(ctx, pts);
  ctx.stroke();
}

// Same shape as drawSmoothLine, but fills the area below the curve down to
// `baseY` with a vertical gradient (color at top, transparent at bottom).
function drawAreaFill(ctx, points, xs, ys, baseY, color, alphaTop = 0.25) {
  const pts = points.map((v, i) =>
    (v == null || Number.isNaN(v)) ? null : [xs(i), ys(v)]);
  // Walk segments — we may have null gaps; fill each contiguous run
  // separately so a gap doesn't yield a polygon spanning the whole gap.
  let runStart = -1;
  for (let i = 0; i <= pts.length; i++) {
    const here = pts[i];
    if (here && runStart < 0) runStart = i;
    if ((!here || i === pts.length) && runStart >= 0) {
      const run = pts.slice(runStart, i);
      if (run.length >= 1) {
        const minY = Math.min(...run.map((p) => p[1]));
        const grad = ctx.createLinearGradient(0, minY, 0, baseY);
        grad.addColorStop(0, _withAlpha(color, alphaTop));
        grad.addColorStop(1, _withAlpha(color, 0));
        ctx.fillStyle = grad;
        ctx.beginPath();
        ctx.moveTo(run[0][0], baseY);
        ctx.lineTo(run[0][0], run[0][1]);
        _smoothPath(ctx, run, { resume: true });
        ctx.lineTo(run[run.length - 1][0], baseY);
        ctx.closePath();
        ctx.fill();
      }
      runStart = -1;
    }
  }
}

function _withAlpha(color, a) {
  // accept "#RRGGBB" or "#RGB"
  if (color[0] === '#') {
    let hex = color.slice(1);
    if (hex.length === 3) hex = hex.split('').map((c) => c + c).join('');
    const r = parseInt(hex.slice(0, 2), 16);
    const g = parseInt(hex.slice(2, 4), 16);
    const b = parseInt(hex.slice(4, 6), 16);
    return `rgba(${r},${g},${b},${a})`;
  }
  return color;  // best-effort passthrough
}

// Backwards-compat wrapper used by the energy chart's overlay line. Same
// signature as the old drawSeries; just routes through the new smooth path.
function drawSeries(ctx, points, color, padL, padR, padT, padB, w, h, minY, maxY) {
  if (!points.length) return;
  const xs = (i) => padL + (i / Math.max(1, points.length - 1)) * (w - padL - padR);
  const ys = (v) => h - padB - ((v - minY) / Math.max(1e-6, maxY - minY)) * (h - padT - padB);
  drawSmoothLine(ctx, points, xs, ys, color);
}

// Hover tooltip — created on demand, shared by all charts. Returns a setter
// (call with {x, y, html} to position+show, or null to hide).
let _tooltipEl = null;
function _ensureTooltip() {
  if (_tooltipEl) return _tooltipEl;
  const t = document.createElement('div');
  t.className = 'chart-tooltip';
  t.hidden = true;
  document.body.appendChild(t);
  _tooltipEl = t;
  return t;
}
export function chartTooltip(target) {
  const el = _ensureTooltip();
  if (!target) { el.hidden = true; return; }
  el.hidden = false;
  el.innerHTML = target.html;
  // Position relative to viewport, but offset above the cursor.
  const left = Math.min(window.innerWidth - el.offsetWidth - 8, target.x + 12);
  const top  = Math.max(8, target.y - el.offsetHeight - 12);
  el.style.left = left + 'px';
  el.style.top  = top + 'px';
}

export function drawLiveChart(s) {
  lastStatus = s;
  const canvas = $('chart-live');
  if (!canvas) return;
  const { ctx, w, h } = setCanvasSize(canvas);
  ctx.clearRect(0, 0, w, h);
  const padL = 44, padR = 44, padT = 14, padB = 28;

  const hist = ((s?.history) || []).slice()
    .filter((p) => p && p.ts != null)
    .sort((a, b) => (a.ts || 0) - (b.ts || 0));
  if (!hist.length) {
    ctx.fillStyle = chartMuted(); ctx.font = '12px Inter';
    ctx.fillText('Waiting for data…', padL + 8, padT + 16);
    return;
  }
  const clampW = (v) => (v == null || Number.isNaN(Number(v))) ? 0 : Math.max(0, Number(v));
  const clampPct = (v) => {
    if (v == null || Number.isNaN(Number(v))) return null;
    return Math.max(0, Math.min(100, Number(v)));
  };
  const out = hist.map(p => clampW(p.output_power_w));
  const inp = hist.map(p => clampW(p.input_power_w));
  const bat = hist.map(p => clampPct(p.battery_percent));
  const maxW = Math.max(50, ...out, ...inp);

  const t0 = hist[0].ts || 0;
  const t1 = hist[hist.length - 1].ts || t0;
  const span = Math.max(1e-6, t1 - t0);
  const xs = (i) => padL + (((hist[i]?.ts || t0) - t0) / span) * (w - padL - padR);
  const yWatts = (v) => (h - padB) - (v / Math.max(1e-6, maxW)) * (h - padT - padB);
  const yPct   = (v) => (h - padB) - (v / 100) * (h - padT - padB);
  const baseY  = h - padB;
  const indexForX = (x) => {
    const t = (x - padL) / Math.max(1e-6, w - padL - padR);
    const ts = t0 + t * (t1 - t0);
    let best = 0, bestD = Infinity;
    for (let i = 0; i < hist.length; i++) {
      const d = Math.abs((hist[i].ts || 0) - ts);
      if (d < bestD) { bestD = d; best = i; }
    }
    return best;
  };

  // Subtle horizontal gridlines (4)
  ctx.strokeStyle = chartGrid();
  ctx.lineWidth = 1;
  for (let i = 1; i <= 4; i++) {
    const y = padT + ((h - padT - padB) * i) / 5;
    ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(w - padR, y); ctx.stroke();
  }
  drawAxes(ctx, w, h, padL, padR, padT, padB);

  // Left Y-axis labels (Watts)
  ctx.fillStyle = chartMuted(); ctx.font = '11px Inter';
  ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const v = (maxW * (4 - i)) / 4;
    const y = padT + ((h - padT - padB) * i) / 4;
    ctx.fillText(`${Math.round(v)}`, padL - 6, y + 3);
  }
  // Right Y-axis labels (Battery %)
  ctx.textAlign = 'left';
  ctx.fillStyle = 'rgba(251,191,36,.65)';
  for (const v of [0, 50, 100]) {
    ctx.fillText(`${v}%`, w - padR + 4, yPct(v) + 3);
  }
  ctx.textAlign = 'start';

  // X-axis time ticks — first sample timestamp ... last (clamped to "now").
  if (hist.length >= 2) {
    const fmtMs = (ms) => new Date(ms * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    ctx.fillStyle = chartMuted();
    ctx.textAlign = 'center';
    const ticks = 5;
    for (let i = 0; i <= ticks; i++) {
      const ts = t0 + span * (i / ticks);
      const x = padL + ((ts - t0) / span) * (w - padL - padR);
      ctx.fillText(fmtMs(ts), x, h - 8);
    }
    ctx.textAlign = 'start';
  }

  if (_seriesVisible.live.input) {
    drawAreaFill(ctx, inp, xs, yWatts, baseY, SERIES_COLORS.input, .22);
    drawSmoothLine(ctx, inp, xs, yWatts, SERIES_COLORS.input, 2.5);
  }
  if (_seriesVisible.live.output) {
    drawAreaFill(ctx, out, xs, yWatts, baseY, SERIES_COLORS.output, .25);
    drawSmoothLine(ctx, out, xs, yWatts, SERIES_COLORS.output, 2.5);
  }
  // Battery on its own right axis, dashed to make the different scale obvious.
  if (_seriesVisible.live.battery) {
    ctx.save();
    ctx.setLineDash([4, 4]);
    drawSmoothLine(ctx, bat, xs, yPct, SERIES_COLORS.battery, 2);
    ctx.restore();
  }

  // Hover state stored on the canvas element
  _attachChartHover(canvas, hist, (i, evt) => {
    const p = hist[i];
    const ts = p.ts ? new Date(p.ts * 1000).toLocaleTimeString() : '';
    return `<div class="cht-ts">${ts}</div>
            <div class="cht-row"><i style="background:${SERIES_COLORS.output}"></i> Output <b>${fmt(p.output_power_w)}</b> W</div>
            <div class="cht-row"><i style="background:${SERIES_COLORS.input}"></i> Input <b>${fmt(p.input_power_w)}</b> W</div>
            <div class="cht-row"><i style="background:${SERIES_COLORS.battery}"></i> Battery <b>${fmt(p.battery_percent)}</b> %</div>`;
  }, () => ({ xs, padL, padR, w, h, baseY: h - padB, padT, padB, indexForX }),
  () => drawLiveChart(lastStatus));
}

// Generic chart-hover binder. Stores the per-render data + geometry +
// tooltip-builder on the canvas element so the (one-time) listener always
// reads the freshest closures, not stale ones from when it was bound.
function _attachChartHover(canvas, hist, htmlFn, geomFn, redrawFn) {
  // Refresh per-render state every call.
  canvas._chartData = hist;
  canvas._chartHtml = htmlFn;
  canvas._chartGeom = geomFn();
  if (redrawFn) canvas._redraw = redrawFn;
  if (canvas._hoverBound) return;
  canvas._hoverBound = true;
  canvas.style.cursor = 'crosshair';
  const onMove = (e) => {
    const data = canvas._chartData;
    const geom = canvas._chartGeom;
    const html = canvas._chartHtml;
    if (!data?.length || !geom || !html) return;
    const rect = canvas.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const t = (x - geom.padL) / (geom.w - geom.padL - geom.padR);
    const idx = geom.indexForX
      ? geom.indexForX(x)
      : Math.max(0, Math.min(data.length - 1, Math.round(t * (data.length - 1))));
    // Redraw the chart, then overlay the crosshair.
    if (canvas._redraw) canvas._redraw();
    const ctx = canvas.getContext('2d');
    const xPos = typeof geom.xs === 'function' ? geom.xs(idx) : geom.xs[idx];
    ctx.strokeStyle = chartCrosshair();
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(xPos, geom.padT); ctx.lineTo(xPos, geom.baseY); ctx.stroke();
    chartTooltip({ x: e.clientX, y: e.clientY, html: html(idx, e) });
  };
  const onLeave = () => {
    chartTooltip(null);
    if (canvas._redraw) canvas._redraw();
  };
  canvas.addEventListener('mousemove', onMove);
  canvas.addEventListener('mouseleave', onLeave);
  if (!canvas._redraw) canvas._redraw = () => drawLiveChart(lastStatus);
}
export function energyChartSeries(j) {
  if (Array.isArray(j?.series) && j.series.length) return j.series;
  if (Array.isArray(j?.history)) {
    return [{ device_sn: j.device_sn, name: '', history: j.history }];
  }
  return [];
}

let energyCache = null;

/** 'bar' | 'line' — single-device default is bars; user can switch to lines. */
let _energyChartStyle = 'bar';

export function setEnergyChartStyle(style) {
  _energyChartStyle = style === 'line' ? 'line' : 'bar';
}

export function getEnergyChartStyle() {
  return _energyChartStyle;
}

function _energyFmtTs(ts, span) {
  const d = new Date(ts * 1000);
  if (span > 24 * 3600) return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function drawEnergyChart(j) {
  energyCache = j;
  const canvas = $('chart-energy');
  if (!canvas) return;
  const { ctx, w, h } = setCanvasSize(canvas);
  ctx.clearRect(0, 0, w, h);
  const padL = 42, padR = 16, padT = 14, padB = 28;
  drawAxes(ctx, w, h, padL, padR, padT, padB);

  const series = energyChartSeries(j);
  const compare = series.length > 1;
  const style = (j?.chart_style === 'line' || j?.chart_style === 'bar')
    ? j.chart_style
    : _energyChartStyle;
  const anyHist = series.some((s) => (s.history || []).length);
  if (!anyHist) {
    ctx.fillStyle = chartMuted(); ctx.font = '12px Inter';
    ctx.fillText('No energy data yet — keep the monitor running.', padL + 8, padT + 16);
    return;
  }

  const tsSet = new Set();
  let maxWh = 1;
  for (const s of series) {
    for (const p of (s.history || [])) {
      if (p.ts != null) tsSet.add(p.ts);
      if ((p.output_wh || 0) > maxWh) maxWh = p.output_wh;
      if ((p.input_wh || 0) > maxWh) maxWh = p.input_wh;
    }
  }
  const tsAxis = compare
    ? [...tsSet].sort((a, b) => a - b)
    : (series[0].history || []).map((p) => p.ts);
  if (!tsAxis.length) {
    ctx.fillStyle = chartMuted(); ctx.font = '12px Inter';
    ctx.fillText('No energy data yet — keep the monitor running.', padL + 8, padT + 16);
    return;
  }

  // Grid + y-axis labels (Wh per bucket)
  ctx.strokeStyle = chartGrid(); ctx.lineWidth = 1;
  ctx.fillStyle = chartMuted(); ctx.font = '11px Inter';
  ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const y = padT + ((h - padT - padB) * i) / 4;
    ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(w - padR, y); ctx.stroke();
    const v = (maxWh * (4 - i)) / 4;
    ctx.fillText(`${Math.round(v)}`, padL - 6, y + 3);
  }
  ctx.textAlign = 'start';

  const first = tsAxis[0], last = tsAxis[tsAxis.length - 1];
  const span = Math.max(1, last - first);
  if (tsAxis.length >= 2) {
    const ticks = 4;
    ctx.fillStyle = chartMuted();
    for (let i = 0; i <= ticks; i++) {
      const idx = Math.floor((tsAxis.length - 1) * (i / ticks));
      const x = padL + (i / ticks) * (w - padL - padR);
      ctx.fillText(_energyFmtTs(tsAxis[idx], span), x - 18, h - 8);
    }
  }

  const n = tsAxis.length;
  const totalW = w - padL - padR;
  const slot = totalW / Math.max(1, n);
  const xs = (i) => padL + slot * (i + 0.5);
  const yWh = (v) => (h - padB) - (v / maxWh) * (h - padT - padB);
  const yPct = (v) => (h - padB) - (v / 100) * (h - padT - padB);
  const yBase = h - padB;
  const indexForX = (x) =>
    Math.max(0, Math.min(n - 1, Math.round((x - padL) / Math.max(1e-6, slot) - 0.5)));

  const seriesByTs = series.map((s) => ({
    sn: s.device_sn,
    name: s.name || s.device_sn,
    color: compare ? energyCompareColor(s.device_sn) : null,
    byTs: new Map((s.history || []).map((p) => [p.ts, p])),
  }));

  if (style === 'bar' && !compare) {
    const hist = series[0].history || [];
    const out = hist.map(p => p.output_wh || 0);
    const inp = hist.map(p => p.input_wh || 0);
    const bat = hist.map(p => (p.system_soc ?? p.avg_battery_percent ?? p.battery_pct));
    const barW = Math.max(1, Math.min(slot * 0.4, 16));
    for (let i = 0; i < n; i++) {
      const xCenter = xs(i);
      if (_seriesVisible.energy.output) {
        const yOut = yBase - (out[i] / maxWh) * (h - padT - padB);
        ctx.fillStyle = SERIES_COLORS.output;
        ctx.fillRect(xCenter - barW - 1, yOut, barW, yBase - yOut);
      }
      if (_seriesVisible.energy.input) {
        const yIn = yBase - (inp[i] / maxWh) * (h - padT - padB);
        ctx.fillStyle = SERIES_COLORS.input;
        ctx.fillRect(xCenter + 1, yIn, barW, yBase - yIn);
      }
    }
    if (_seriesVisible.energy.battery && bat.some(v => v != null)) {
      ctx.setLineDash([]);
      drawSmoothLine(ctx, bat, xs, yPct, SERIES_COLORS.battery, 2);
    }
  } else if (style === 'bar' && compare) {
    // Grouped bars per timestamp: one pair (out/in) per device when visible.
    const active = seriesByTs;
    const groupN = Math.max(1, active.length);
    const groupW = Math.min(slot * 0.85, 28 * groupN);
    const barW = Math.max(1, (groupW / groupN) * 0.4);
    for (let i = 0; i < n; i++) {
      const ts = tsAxis[i];
      const xCenter = xs(i);
      const groupLeft = xCenter - groupW / 2;
      active.forEach((s, si) => {
        const p = s.byTs.get(ts);
        if (!p) return;
        const cx = groupLeft + (si + 0.5) * (groupW / groupN);
        if (_seriesVisible.energy.output) {
          const out = p.output_wh || 0;
          const yOut = yWh(out);
          ctx.fillStyle = s.color;
          ctx.fillRect(cx - barW - 0.5, yOut, barW, yBase - yOut);
        }
        if (_seriesVisible.energy.input) {
          const inp = p.input_wh || 0;
          const yIn = yWh(inp);
          ctx.globalAlpha = 0.55;
          ctx.fillStyle = s.color;
          ctx.fillRect(cx + 0.5, yIn, barW, yBase - yIn);
          ctx.globalAlpha = 1;
        }
      });
    }
    for (const s of seriesByTs) {
      const bat = tsAxis.map((ts) => {
        const p = s.byTs.get(ts);
        if (!p) return null;
        const v = p.system_soc ?? p.avg_battery_percent ?? p.battery_pct;
        return v == null ? null : v;
      });
      if (_seriesVisible.energy.battery && bat.some(v => v != null)) {
        ctx.setLineDash([]);
        drawSmoothLine(ctx, bat, xs, yPct, s.color, 1.4);
      }
    }
  } else {
    // Line mode (single or compare)
    for (const s of seriesByTs) {
      const color = s.color || SERIES_COLORS.output;
      const out = tsAxis.map((ts) => {
        const p = s.byTs.get(ts);
        return p ? (p.output_wh || 0) : null;
      });
      const inp = tsAxis.map((ts) => {
        const p = s.byTs.get(ts);
        return p ? (p.input_wh || 0) : null;
      });
      const bat = tsAxis.map((ts) => {
        const p = s.byTs.get(ts);
        if (!p) return null;
        const v = p.system_soc ?? p.avg_battery_percent ?? p.battery_pct;
        return v == null ? null : v;
      });
      if (!compare) {
        if (_seriesVisible.energy.output) {
          ctx.setLineDash([]);
          drawSmoothLine(ctx, out, xs, yWh, SERIES_COLORS.output, 2.5);
        }
        if (_seriesVisible.energy.input) {
          ctx.setLineDash([]);
          drawSmoothLine(ctx, inp, xs, yWh, SERIES_COLORS.input, 2.5);
        }
        if (_seriesVisible.energy.battery && bat.some(v => v != null)) {
          ctx.setLineDash([]);
          drawSmoothLine(ctx, bat, xs, yPct, SERIES_COLORS.battery, 2);
        }
      } else {
        if (_seriesVisible.energy.output) {
          ctx.setLineDash([]);
          drawSmoothLine(ctx, out, xs, yWh, color, 2.5);
        }
        if (_seriesVisible.energy.input) {
          ctx.setLineDash([6, 4]);
          drawSmoothLine(ctx, inp, xs, yWh, color, 2.5);
          ctx.setLineDash([]);
        }
        if (_seriesVisible.energy.battery && bat.some(v => v != null)) {
          ctx.setLineDash([]);
          drawSmoothLine(ctx, bat, xs, yPct, color, 1.4);
        }
      }
    }
  }

  // Hover: one vertical guide + tooltip for the bucket under the cursor.
  const hoverPts = tsAxis.map((ts) => ({ ts }));
  _attachChartHover(canvas, hoverPts, (i) => {
    const ts = tsAxis[i];
    const rows = [];
    for (const s of seriesByTs) {
      const p = s.byTs.get(ts);
      if (!p) continue;
      const color = s.color || SERIES_COLORS.output;
      const label = compare ? (s.name || s.sn) : null;
      if (label) {
        rows.push(`<div class="cht-row"><i style="background:${color}"></i> <b>${label}</b></div>`);
      }
      if (_seriesVisible.energy.output) {
        rows.push(
          `<div class="cht-row"><i style="background:${compare ? color : SERIES_COLORS.output}"></i> Consumed <b>${fmt(p.output_wh)}</b> Wh</div>`,
        );
      }
      if (_seriesVisible.energy.input) {
        rows.push(
          `<div class="cht-row"><i style="background:${compare ? color : SERIES_COLORS.input}"></i> Charged <b>${fmt(p.input_wh)}</b> Wh</div>`,
        );
      }
      const soc = p.system_soc ?? p.avg_battery_percent ?? p.battery_pct;
      if (_seriesVisible.energy.battery && soc != null) {
        rows.push(
          `<div class="cht-row"><i style="background:${compare ? color : SERIES_COLORS.battery}"></i> Battery <b>${fmt(soc)}</b> %</div>`,
        );
      }
    }
    if (!rows.length) {
      return `<div class="cht-ts">${_energyFmtTs(ts, span)}</div><div class="cht-row">No data</div>`;
    }
    return `<div class="cht-ts">${_energyFmtTs(ts, span)}</div>${rows.join('')}`;
  }, () => ({ xs, padL, padR, w, h, baseY: yBase, padT, padB, indexForX }),
  () => drawEnergyChart(energyCache));
}

let forecastCache = null;

export function drawForecastChart(j) {
  forecastCache = j;
  const canvas = $('chart-forecast');
  if (!canvas) return;
  const { ctx, w, h } = setCanvasSize(canvas);
  ctx.clearRect(0, 0, w, h);
  const padL = 42, padR = 50, padT = 14, padB = 28;
  drawAxes(ctx, w, h, padL, padR, padT, padB);

  const fc = j?.forecast || [];
  if (!fc.length) {
    ctx.fillStyle = chartMuted(); ctx.font = '12px Inter';
    ctx.fillText('No forecast yet — keep the monitor running and check back.', padL + 8, padT + 16);
    return;
  }

  const totalW = w - padL - padR;
  const innerH = h - padT - padB;
  const n = fc.length;
  const xAt = (i) => padL + (i / (n - 1)) * totalW;
  const ts0 = fc[0].ts, ts1 = fc[fc.length - 1].ts;

  // Threshold band — shade where predicted SOC dips below user's low-batt %.
  const threshold = j.low_battery_threshold || 20;
  const yThreshold = padT + innerH * (1 - threshold / 100);
  ctx.fillStyle = 'rgba(248, 113, 113, 0.08)';
  ctx.fillRect(padL, yThreshold, totalW, h - padB - yThreshold);
  ctx.strokeStyle = 'rgba(248, 113, 113, 0.4)';
  ctx.setLineDash([4, 4]);
  ctx.beginPath();
  ctx.moveTo(padL, yThreshold); ctx.lineTo(w - padR, yThreshold);
  ctx.stroke();
  ctx.setLineDash([]);

  const vis = _seriesVisible.forecast;
  // Right-axis grid: power scale based on whichever power series are
  // currently visible. If both are hidden, fall back to 1 so the SOC-only
  // view still draws cleanly.
  const powerSamples = [1];
  if (vis.solar) powerSamples.push(...fc.map(p => p.solar_w || 0));
  if (vis.load)  powerSamples.push(...fc.map(p => p.load_w || 0));
  const maxPower = Math.max(...powerSamples);
  const niceMax = Math.max(100, Math.ceil(maxPower / 100) * 100);

  // Left-axis: SOC % grid + labels
  ctx.strokeStyle = chartGrid(); ctx.lineWidth = 1;
  ctx.fillStyle = chartMuted(); ctx.font = '11px Inter';
  ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const y = padT + (innerH * i) / 4;
    ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(w - padR, y); ctx.stroke();
    ctx.fillText(`${100 - i * 25}%`, padL - 6, y + 3);
    ctx.fillText(`${Math.round(niceMax * (4 - i) / 4)}W`, w - padR + 36, y + 3);
  }
  ctx.textAlign = 'start';

  // X-axis labels
  const fmtTs = (ts) => {
    const d = new Date(ts * 1000);
    const span = ts1 - ts0;
    if (span > 24 * 3600) return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  };
  ctx.fillStyle = chartMuted();
  const ticks = 5;
  for (let i = 0; i <= ticks; i++) {
    const idx = Math.floor((n - 1) * (i / ticks));
    ctx.fillText(fmtTs(fc[idx].ts), xAt(idx) - 18, h - 8);
  }

  if (vis.solar) {
    ctx.strokeStyle = SERIES_COLORS.input;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    fc.forEach((p, i) => {
      const x = xAt(i);
      const y = padT + innerH * (1 - (p.solar_w || 0) / niceMax);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.stroke();
  }

  if (vis.load) {
    // Load is dashed so it visually reads as "demand" not "production".
    ctx.strokeStyle = SERIES_COLORS.output;
    ctx.lineWidth = 1.5;
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    fc.forEach((p, i) => {
      const x = xAt(i);
      const y = padT + innerH * (1 - (p.load_w || 0) / niceMax);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.stroke();
    ctx.setLineDash([]);
  }

  if (vis.soc) {
    ctx.strokeStyle = SERIES_COLORS.battery;
    ctx.lineWidth = 2.5;
    ctx.beginPath();
    fc.forEach((p, i) => {
      const x = xAt(i);
      const y = padT + innerH * (1 - (p.predicted_soc || 0) / 100);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.stroke();
  }

  _attachChartHover(canvas, fc, (i) => {
    const p = fc[i];
    const ts = p.ts
      ? new Date(p.ts * 1000).toLocaleString([], {
          weekday: 'short', hour: '2-digit', minute: '2-digit',
        })
      : '';
    const rows = [`<div class="cht-ts">${ts}</div>`];
    if (vis.solar) {
      rows.push(`<div class="cht-row"><i style="background:${SERIES_COLORS.input}"></i> Solar <b>${fmt(p.solar_w)}</b> W</div>`);
    }
    if (vis.load) {
      rows.push(`<div class="cht-row"><i style="background:${SERIES_COLORS.output}"></i> Load <b>${fmt(p.load_w)}</b> W</div>`);
    }
    if (vis.soc) {
      rows.push(`<div class="cht-row"><i style="background:${SERIES_COLORS.battery}"></i> SOC <b>${fmt(p.predicted_soc)}</b> %</div>`);
    }
    return rows.join('');
  }, () => ({
    xs: xAt, padL, padR, w, h, baseY: h - padB, padT, padB,
  }), () => forecastCache && drawForecastChart(forecastCache));
}

// Redraw on resize
