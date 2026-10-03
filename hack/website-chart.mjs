// hack/website-chart.mjs — draw the home page's curve (visual D-1) from
// website/data/software-per-year.json into website/index.html, between the
// `<!-- chart D-1 … -->` and `<!-- /chart D-1 -->` markers (`make website-chart`).
// The output is committed: the page stays plain HTML with no script. Edit the data,
// then run this; never edit the block by hand.
//
// The plot is an SVG stretched to the frame (21:9, 4:3 on phones) with
// non-scaling strokes; its labels are HTML set over it, so they keep their size on
// any screen. A linear scale, an ink line, the years before the inflection in
// concrete and after it in cobalt, a rule at the inflection, a square on the latest
// point. A projection is drawn only when the data file carries one.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const site = process.env.WEBSITE_DIR || join(root, 'website');
const data = JSON.parse(readFileSync(join(site, 'data/software-per-year.json'), 'utf8'));
const pts = data.series.points.map((p) => ({ ...p })).sort((a, b) => a.year - b.year);
const last = pts[pts.length - 1];
const inf = data.inflection;

// scales: x over whole years around the data, y from zero to a round top
const x0 = Math.floor(pts[0].year);
const x1 = Math.ceil(last.year) + 0.4;
const step = niceStep(Math.max(...pts.map((p) => p.value)) / 4);
const yTop = step * 4;
const X = (year) => ((year - x0) / (x1 - x0)) * 1000;
const Y = (v) => 1000 - (v / yTop) * 1000;
const f = (n) => (Math.round(n * 10) / 10).toString();

function niceStep(raw) {
  const e = 10 ** Math.floor(Math.log10(raw));
  for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 7.5, 10]) if (m * e >= raw) return m * e;
  return 10 * e;
}
function at(year) { // the line's value at a year, between two points
  for (let i = 1; i < pts.length; i++) {
    const a = pts[i - 1], b = pts[i];
    if (year <= b.year) return a.value + ((b.value - a.value) * (year - a.year)) / (b.year - a.year);
  }
  return last.value;
}
const short = (v) => (v === 0 ? '0' : v >= 1e9 ? `${f(v / 1e9)}B` : `${f(v / 1e6)}M`);
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const pct = (v) => `${(Math.round(v * 100) / 100).toFixed(2)}%`;

const line = pts.map((p, i) => `${i ? 'L' : 'M'}${f(X(p.year))} ${f(Y(p.value))}`).join(' ');
const before = pts.filter((p) => p.year < inf.year);
const after = pts.filter((p) => p.year > inf.year);
const yi = at(inf.year);
// an area under the line from one point to another, through the points between
const area = (from, between, to) => `M${f(X(from.year))} 1000 ${[from, ...between, to].map((p) => `L${f(X(p.year))} ${f(Y(p.value))}`).join(' ')} L${f(X(to.year))} 1000 Z`;
const knee = { year: inf.year, value: yi };
const pre = area(before[0], before.slice(1), knee);
const post = area(knee, after.slice(0, -1), last);

const grid = [1, 2, 3, 4].map((k) => `<line class="chart-grid" x1="0" x2="1000" y1="${f(Y(step * k))}" y2="${f(Y(step * k))}" vector-effect="non-scaling-stroke"/>`).join('');
const yTicks = [0, 1, 2, 3, 4].map((k) => `<span class="chart-ytick" style="--y:${pct(Y(step * k) / 10)}">${short(step * k)}</span>`).join('');
const xTicks = [];
for (let yr = x0 + 1; yr <= Math.floor(x1); yr += 2) xTicks.push(`<span class="chart-xtick" style="--x:${pct(X(yr) / 10)}">${yr}</span>`);
const name = `${data.series.title}, ${Math.floor(pts[0].year)} to ${Math.floor(last.year)}: from ${short(pts[0].value)} in ${Math.floor(pts[0].year)} to ${short(pts[pts.length - 2].value)} in the year to ${monthYear(pts[pts.length - 2].date)}, then ${short(last.value)} in ${monthYear(last.date)}${data.latest_note ? ` (${data.latest_note})` : ''}.`;

function monthYear(d) {
  const [y, m] = d.split('-');
  return `${['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'][+m - 1]} ${y}`;
}
const host = (u) => new URL(u).host.replace(/^www\./, '');
const fmt = (v) => v.toLocaleString('en-US');

const rows = pts.map((p) => `            <tr><td class="num">${esc(p.date)}</td><td class="num">${fmt(p.value)}</td><td>${esc(host(p.source))}${p.note ? ` · ${esc(p.note)}` : ''}</td></tr>`).join('\n');

const block = `<!-- chart D-1: drawn by hack/website-chart.mjs from data/software-per-year.json; edit the data, then run make website-chart -->
        <p class="chart-y">Software made per year: ${esc(data.series.title.charAt(0).toLowerCase() + data.series.title.slice(1))}</p>
        <div class="chart-frame">
          <svg class="chart-svg" viewBox="0 0 1000 1000" preserveAspectRatio="none" role="img" aria-labelledby="d1-name">
            <title id="d1-name">${esc(name)}</title>
            ${grid}
            <path class="chart-pre" d="${pre}"/>
            <path class="chart-post" d="${post}"/>
            <line class="chart-rule" x1="${f(X(inf.year))}" x2="${f(X(inf.year))}" y1="0" y2="1000" vector-effect="non-scaling-stroke"/>
            <path class="chart-line" d="${line}" vector-effect="non-scaling-stroke"/>
          </svg>
          <div class="chart-labels" aria-hidden="true">
            ${yTicks}
            ${xTicks.join('')}
            <span class="chart-marker" style="--x:${pct(X(inf.year) / 10)}">${esc(inf.label)}</span>
            <i class="chart-dot" style="--x:${pct(X(last.year) / 10)};--y:${pct(Y(last.value) / 10)}"></i>
            <span class="chart-today" style="--x:${pct(X(last.year) / 10)};--y:${pct(Y(last.value) / 10)}">Today${data.latest_note ? `<small>${esc(data.latest_note)}</small>` : ''}</span>
          </div>
        </div>
        <details class="numbers">
          <summary>Show the numbers</summary>
          <table>
            <caption class="sr">${esc(data.series.title)} (${esc(data.series.unit)}). ${esc(data.series.definition)}</caption>
            <thead><tr><th scope="col">Date</th><th scope="col">${esc(data.series.title)}</th><th scope="col">Source</th></tr></thead>
            <tbody>
${rows}
            </tbody>
          </table>
        </details>
        <!-- /chart D-1 -->`;

const file = join(site, 'index.html');
const html = readFileSync(file, 'utf8');
const re = /<!-- chart D-1\b[\s\S]*?<!-- \/chart D-1 -->/;
if (!re.test(html)) {
  console.error('website-chart: no <!-- chart D-1 … --> … <!-- /chart D-1 --> block in website/index.html');
  process.exit(1);
}
writeFileSync(file, html.replace(re, block));
console.log(`>> website/index.html: chart D-1, ${pts.length} points, ${x0}–${Math.floor(x1)}, top ${short(yTop)}`);
