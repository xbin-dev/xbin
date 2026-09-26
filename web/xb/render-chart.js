/**
 * xb/render-chart.js — <xb-chart>, the `chart` primitive as inline SVG (the
 * app draws it with Swift Charts). `.node` is the tree node:
 *   kind    line | bar | area | spark (a bare trend line, no axes)
 *   series  [{name, points: [[x, y], …]}]
 *   x       time (ms since the epoch) | number | category (x is a label)
 *   y       number | bytes | percent (a fraction: 0.42 → 42%)
 *   height  a height token (default: spark 36px, others m = 160px)
 * Colours are --xb-chart-1…6 in series order; labels use the caption size,
 * so the chart follows the text scale. Width follows the element (resize
 * observer), so the SVG is drawn 1:1 and text never stretches.
 */
import { LitElement, html, svg, css, nothing } from '/vendor/lit-all.min.js';

const HEIGHTS = { xs: 48, s: 96, m: 160, l: 240, xl: 360 };

function compact(v) {
  const a = Math.abs(v);
  if (a >= 1e12) return `${+(v / 1e12).toPrecision(3)}T`;
  if (a >= 1e9) return `${+(v / 1e9).toPrecision(3)}G`;
  if (a >= 1e6) return `${+(v / 1e6).toPrecision(3)}M`;
  if (a >= 1e3) return `${+(v / 1e3).toPrecision(3)}K`;
  if (a === 0) return '0';
  if (a < 0.01) return v.toExponential(1);
  return String(+v.toPrecision(3));
}
function bytes(v) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0; let x = Math.abs(v);
  while (x >= 1024 && i < u.length - 1) { x /= 1024; i++; }
  return `${v < 0 ? '-' : ''}${i === 0 ? Math.round(x) : +x.toPrecision(3)} ${u[i]}`;
}
export function fmtY(kind, v) {
  if (kind === 'bytes') return bytes(v);
  if (kind === 'percent') return `${+(v * 100).toPrecision(3)}%`;
  return compact(v);
}
function fmtX(kind, v, span) {
  if (kind === 'category') return String(v);
  if (kind === 'time') {
    const d = new Date(Number(v));
    if (Number.isNaN(d.getTime())) return '';
    if (span < 120e3) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    if (span < 2 * 86400e3) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }
  return compact(Number(v));
}

// nice(lo, hi, n): round tick values covering [lo, hi]
function nice(lo, hi, n = 4) {
  if (!(hi > lo)) { const d = Math.abs(lo) || 1; lo -= d / 2; hi += d / 2; }
  const raw = (hi - lo) / n;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) || raw;
  const a = Math.floor(lo / step) * step;
  const b = Math.ceil(hi / step) * step;
  const ticks = [];
  for (let v = a; v <= b + step / 2; v += step) ticks.push(+v.toPrecision(12));
  return ticks;
}

function clean(series, xKind) {
  const out = [];
  for (const s of Array.isArray(series) ? series : []) {
    const pts = [];
    for (const p of Array.isArray(s?.points) ? s.points : []) {
      if (!Array.isArray(p) || p.length < 2) continue;
      const y = Number(p[1]);
      if (!Number.isFinite(y)) continue;
      const x = xKind === 'category' ? String(p[0]) : Number(p[0]);
      if (xKind !== 'category' && !Number.isFinite(x)) continue;
      pts.push([x, y]);
    }
    out.push({ name: String(s?.name ?? ''), pts });
  }
  return out;
}

export class XbChart extends LitElement {
  static properties = { node: { attribute: false }, _w: { state: true } };
  static styles = css`
    :host { display: block; min-width: 0; }
    svg { display: block; overflow: visible; }
    .lbl { font: var(--xb-font-caption-2); fill: var(--xb-muted); font-variant-numeric: tabular-nums; }
    .grid { stroke: var(--xb-separator); stroke-width: 1; }
    .base { stroke: var(--xb-border); stroke-width: 1; }
    .legend { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 6px; font: var(--xb-font-caption); color: var(--xb-muted); }
    .legend span { display: inline-flex; align-items: center; gap: 6px; }
    .legend i { width: 10px; height: 10px; border-radius: 3px; display: inline-block; }
    .none { font: var(--xb-font-footnote); color: var(--xb-muted); display: flex; align-items: center; justify-content: center; }
  `;

  constructor() { super(); this.node = null; this._w = 0; }
  connectedCallback() {
    super.connectedCallback();
    this._ro = new ResizeObserver(() => { const w = Math.floor(this.clientWidth); if (w && w !== this._w) this._w = w; });
    this._ro.observe(this);
  }
  disconnectedCallback() { this._ro?.disconnect(); super.disconnectedCallback(); }

  render() {
    const p = this.node?.p || {};
    const kind = ['line', 'bar', 'area', 'spark'].includes(p.kind) ? p.kind : 'line';
    const xKind = ['time', 'number', 'category'].includes(p.x) ? p.x : 'number';
    const yKind = ['number', 'bytes', 'percent'].includes(p.y) ? p.y : 'number';
    const H = HEIGHTS[p.height] ?? (kind === 'spark' ? 36 : 160);
    const W = this._w || 300;
    const series = clean(p.series, xKind);
    const pts = series.flatMap((s) => s.pts);
    const label = `${kind} chart${series.length ? `: ${series.map((s) => s.name).filter(Boolean).join(', ')}` : ''}`;
    if (!pts.length) return html`<div class="none" style=${`height:${H}px`} role="img" aria-label=${label}>no data</div>`;
    const color = (i) => `var(--xb-chart-${(i % 6) + 1})`;
    const body = kind === 'spark' ? this.spark(series, W, H, xKind, color)
      : kind === 'bar' ? this.bars(series, W, H, xKind, yKind, color)
        : this.lines(series, W, H, xKind, yKind, color, kind === 'area');
    return html`<svg width=${W} height=${H} viewBox=${`0 0 ${W} ${H}`} role="img" aria-label=${label}>${body}</svg>
      ${series.length > 1 ? html`<div class="legend">${series.map((s, i) => html`<span><i style=${`background:${color(i)}`}></i>${s.name}</span>`)}</div>` : nothing}`;
  }

  // x positions for time/number (linear) or category (band centres)
  xScale(series, xKind, x0, x1) {
    if (xKind === 'category') {
      const cats = [];
      for (const s of series) for (const [x] of s.pts) if (!cats.includes(x)) cats.push(x);
      const band = (x1 - x0) / Math.max(1, cats.length);
      return { f: (x) => x0 + band * (cats.indexOf(x) + 0.5), cats, band, lo: 0, hi: 0 };
    }
    let lo = Infinity; let hi = -Infinity;
    for (const s of series) for (const [x] of s.pts) { lo = Math.min(lo, x); hi = Math.max(hi, x); }
    const span = hi - lo || 1;
    return { f: (x) => (hi === lo ? (x0 + x1) / 2 : x0 + ((x - lo) / span) * (x1 - x0)), lo, hi };
  }

  spark(series, W, H, xKind, color) {
    const pad = 3;
    const sx = this.xScale(series, xKind, pad, W - pad);
    let lo = Infinity; let hi = -Infinity;
    for (const s of series) for (const [, y] of s.pts) { lo = Math.min(lo, y); hi = Math.max(hi, y); }
    const fy = (y) => (hi === lo ? H / 2 : H - pad - ((y - lo) / (hi - lo)) * (H - 2 * pad));
    return series.map((s, i) => {
      if (!s.pts.length) return nothing;
      const d = s.pts.map(([x, y], j) => `${j ? 'L' : 'M'}${sx.f(x).toFixed(1)},${fy(y).toFixed(1)}`).join('');
      const [lx, ly] = s.pts[s.pts.length - 1];
      const area = `${d}L${sx.f(lx).toFixed(1)},${H}L${sx.f(s.pts[0][0]).toFixed(1)},${H}Z`;
      return svg`<path d=${area} fill=${color(i)} opacity="0.12"/>
        <path d=${d} fill="none" stroke=${color(i)} stroke-width="1.75" stroke-linejoin="round" stroke-linecap="round"/>
        <circle cx=${sx.f(lx)} cy=${fy(ly)} r="2.5" fill=${color(i)}/>`;
    });
  }

  frame(W, H, yKind, lo, hi, zero) {
    const fs = parseFloat(getComputedStyle(this).getPropertyValue('--xb-size-caption-2')) || 11;
    // bytes tick in steps of their own unit (…, 2 MB, 4 MB), not of 10^n bytes
    let unit = 1;
    if (yKind === 'bytes') { const m = Math.max(Math.abs(lo), Math.abs(hi)); while (unit * 1024 <= m) unit *= 1024; }
    const ticks = nice((zero ? Math.min(0, lo) : lo) / unit, (zero ? Math.max(0, hi) : hi) / unit, H < 120 ? 2 : 4).map((t) => t * unit);
    const labels = ticks.map((t) => fmtY(yKind, t));
    const left = Math.ceil(Math.max(...labels.map((l) => l.length)) * fs * 0.62) + 8;
    const top = Math.ceil(fs * 0.6); const bottom = Math.ceil(fs * 1.9);
    const y0 = H - bottom; const y1 = top;
    const a = ticks[0]; const b = ticks[ticks.length - 1];
    const fy = (y) => y0 - ((y - a) / (b - a || 1)) * (y0 - y1);
    const grid = ticks.map((t, i) => svg`<line class=${i === 0 ? 'base' : 'grid'} x1=${left} x2=${W} y1=${fy(t)} y2=${fy(t)} stroke-dasharray=${i === 0 ? '' : '2 3'}/>
      <text class="lbl" x=${left - 6} y=${fy(t)} dy="0.35em" text-anchor="end">${labels[i]}</text>`);
    return { left, fy, grid, y0, fs };
  }

  xLabels(sx, xKind, left, W, y0, fs) {
    const out = [];
    if (xKind === 'category') {
      const every = Math.max(1, Math.ceil((sx.cats.length * fs * 4) / (W - left)));
      sx.cats.forEach((c, i) => { if (i % every === 0) out.push(svg`<text class="lbl" x=${sx.f(c)} y=${y0 + fs * 1.4} text-anchor="middle">${fmtX(xKind, c)}</text>`); });
      return out;
    }
    const span = sx.hi - sx.lo;
    const vals = span ? [sx.lo, sx.lo + span / 2, sx.hi] : [sx.lo];
    vals.forEach((v, i) => {
      const anchor = vals.length === 1 ? 'middle' : i === 0 ? 'start' : i === vals.length - 1 ? 'end' : 'middle';
      out.push(svg`<text class="lbl" x=${sx.f(v)} y=${y0 + fs * 1.4} text-anchor=${anchor}>${fmtX(xKind, v, span)}</text>`);
    });
    return out;
  }

  lines(series, W, H, xKind, yKind, color, area) {
    let lo = Infinity; let hi = -Infinity;
    for (const s of series) for (const [, y] of s.pts) { lo = Math.min(lo, y); hi = Math.max(hi, y); }
    const { left, fy, grid, y0, fs } = this.frame(W, H, yKind, lo, hi, area);
    const sx = this.xScale(series, xKind, left + 4, W - 4);
    const paths = series.map((s, i) => {
      if (!s.pts.length) return nothing;
      const pts = xKind === 'category' ? s.pts : [...s.pts].sort((a, b) => a[0] - b[0]);
      const d = pts.map(([x, y], j) => `${j ? 'L' : 'M'}${sx.f(x).toFixed(1)},${fy(y).toFixed(1)}`).join('');
      const fill = area ? svg`<path d=${`${d}L${sx.f(pts[pts.length - 1][0]).toFixed(1)},${y0}L${sx.f(pts[0][0]).toFixed(1)},${y0}Z`} fill=${color(i)} opacity="0.18"/>` : nothing;
      const dots = pts.length <= 24 ? pts.map(([x, y]) => svg`<circle cx=${sx.f(x)} cy=${fy(y)} r="2.25" fill=${color(i)}/>`) : nothing;
      return svg`${fill}<path d=${d} fill="none" stroke=${color(i)} stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>${dots}`;
    });
    return svg`${grid}${paths}${this.xLabels(sx, xKind, left, W, y0, fs)}`;
  }

  bars(series, W, H, xKind, yKind, color) {
    let lo = Infinity; let hi = -Infinity;
    for (const s of series) for (const [, y] of s.pts) { lo = Math.min(lo, y); hi = Math.max(hi, y); }
    const { left, fy, grid, y0, fs } = this.frame(W, H, yKind, lo, hi, true);
    // bars sit in bands: categories, or the distinct x values in order
    const xs = [];
    for (const s of series) for (const [x] of s.pts) if (!xs.includes(x)) xs.push(x);
    if (xKind !== 'category') xs.sort((a, b) => a - b);
    const band = (W - left - 4) / Math.max(1, xs.length);
    const gap = Math.min(8, band * 0.25);
    const bw = Math.max(1, (band - gap) / Math.max(1, series.length));
    const base = fy(Math.max(0, Math.min(...[0, lo].filter(Number.isFinite))));
    const rects = series.map((s, i) => s.pts.map(([x, y]) => {
      const bx = left + 4 + band * xs.indexOf(x) + gap / 2 + bw * i;
      const top = Math.min(fy(y), base); const h = Math.max(1, Math.abs(fy(y) - base));
      return svg`<rect x=${bx.toFixed(1)} y=${top.toFixed(1)} width=${Math.max(1, bw - 1).toFixed(1)} height=${h.toFixed(1)} rx="2" fill=${color(i)}/>`;
    }));
    const every = Math.max(1, Math.ceil((xs.length * fs * 4) / (W - left)));
    const span = xKind === 'category' ? 0 : xs[xs.length - 1] - xs[0];
    const labels = xs.map((x, j) => (j % every ? nothing
      : svg`<text class="lbl" x=${left + 4 + band * (j + 0.5)} y=${y0 + fs * 1.4} text-anchor="middle">${fmtX(xKind, x, span)}</text>`));
    return svg`${grid}${rects}${labels}`;
  }
}

if (!customElements.get('xb-chart')) customElements.define('xb-chart', XbChart);
