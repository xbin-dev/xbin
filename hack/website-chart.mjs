// hack/website-chart.mjs — draw the home page's curve (visual D-1) from
// website/data/software-per-year.json into website/index.html, between the
// `<!-- chart D-1 … -->` and `<!-- /chart D-1 -->` markers (`make website-chart`).
// The output is committed: the page stays plain HTML with no script. Edit the data,
// then run this; never edit the block by hand.
//
// The block is the whole figure but its frame: the axis title, the plot, the numbers
// table, the figures under it and the caption, so every sentence about the chart comes
// from the same data as the chart. The plot is an SVG stretched to the frame (21:9, 4:3
// on phones) with non-scaling strokes; its labels are HTML set over it, so they keep
// their size on any screen. A linear scale, an ink line, the years before the inflection
// in concrete and after it in cobalt, a rule at the inflection, a square on the latest
// point. A point marked "annualised" (a month's rate × 12, not a completed year) is
// reached by a dotted segment with no fill and ends in a hollow square, and the caption
// says so (brand §7.3: the bend reads as it is). A projection (data.projection) is drawn
// only when the data file carries one: a dashed cobalt line, labelled "If the curve
// holds", with its sentence in the caption and its figure under the chart; without one,
// neither the line nor the words exist.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const site = process.env.WEBSITE_DIR || join(root, 'website');
const data = JSON.parse(readFileSync(join(site, 'data/software-per-year.json'), 'utf8'));
const pts = data.series.points.map((p) => ({ ...p })).sort((a, b) => a.year - b.year);
const last = pts[pts.length - 1];
const inf = data.inflection;
const proj = data.projection || null;
// the measured years end at the last point that is not an annualised rate
const done = pts.filter((p) => !p.annualised);
const rate = pts.filter((p) => p.annualised);
if (rate.length > 1 || (rate.length && rate[0] !== last)) {
  console.error('website-chart: only the latest point may be an annualised rate');
  process.exit(1);
}
const lastDone = done[done.length - 1];

// scales: x over whole years around the data (and the projection), y from zero to a round top
const x0 = Math.floor(pts[0].year);
const x1 = Math.ceil(proj ? proj.year : last.year) + 0.4;
const step = niceStep(Math.max(...pts.map((p) => p.value), proj ? proj.value : 0) / 4);
const yTop = step * 4;
const X = (year) => ((year - x0) / (x1 - x0)) * 1000;
const Y = (v) => 1000 - (v / yTop) * 1000;
const f = (n) => (Math.round(n * 10) / 10).toString();

function niceStep(raw) {
  const e = 10 ** Math.floor(Math.log10(raw));
  for (const m of [1, 1.5, 2, 2.5, 3, 4, 5, 7.5, 10]) if (m * e >= raw) return m * e;
  return 10 * e;
}
function at(year) { // the line's value at a year, between two measured points
  for (let i = 1; i < done.length; i++) {
    const a = done[i - 1], b = done[i];
    if (year <= b.year) return a.value + ((b.value - a.value) * (year - a.year)) / (b.year - a.year);
  }
  return lastDone.value;
}
const short = (v) => (v === 0 ? '0' : v >= 1e9 ? `${f(v / 1e9)}B` : `${f(v / 1e6)}M`);
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const pct = (v) => `${(Math.round(v * 100) / 100).toFixed(2)}%`;
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
const monthYear = (d) => { const [y, m] = d.split('-'); return `${MONTHS[+m - 1]} ${y}`; };
const pathOf = (list) => list.map((p, i) => `${i ? 'L' : 'M'}${f(X(p.year))} ${f(Y(p.value))}`).join(' ');

const line = pathOf(done);
const dotted = rate.length ? pathOf([lastDone, last]) : '';
const dashed = proj ? pathOf([last, proj]) : '';
const before = done.filter((p) => p.year < inf.year);
const after = done.filter((p) => p.year > inf.year);
// an area under the line from one point to another, through the points between
const area = (from, between, to) => `M${f(X(from.year))} 1000 ${[from, ...between, to].map((p) => `L${f(X(p.year))} ${f(Y(p.value))}`).join(' ')} L${f(X(to.year))} 1000 Z`;
const knee = { year: inf.year, value: at(inf.year) };
const pre = area(before[0], before.slice(1), knee);
const post = area(knee, after.slice(0, -1), lastDone);

const grid = [1, 2, 3, 4].map((k) => `<line class="chart-grid" x1="0" x2="1000" y1="${f(Y(step * k))}" y2="${f(Y(step * k))}" vector-effect="non-scaling-stroke"/>`).join('');
const yTicks = [0, 1, 2, 3, 4].map((k) => `<span class="chart-ytick" style="--y:${pct(Y(step * k) / 10)}">${short(step * k)}</span>`).join('');
const xTicks = [];
for (let yr = x0 + 1; yr <= Math.floor(x1); yr += 2) xTicks.push(`<span class="chart-xtick" style="--x:${pct(X(yr) / 10)}">${yr}</span>`);

// the plot's accessible name: the series, the marker, the latest point as what it is
const name = [
  `${data.series.title}, ${Math.floor(pts[0].year)} to ${Math.floor(last.year)}: from ${short(pts[0].value)} in ${Math.floor(pts[0].year)} to ${short(lastDone.value)} in the year to ${monthYear(lastDone.date)}`,
  rate.length ? `, then ${short(last.value)} in ${monthYear(last.date)}${data.latest_note ? ` (${data.latest_note})` : ''}` : '',
  `; marked: ${inf.label}, ${monthYear(inf.date)}`,
  proj ? `; ${proj.label || 'if the curve holds'}, ${short(proj.value)} in ${Math.floor(proj.year)} (a projection)` : '',
  '.',
].join('');

const host = (u) => new URL(u).host.replace(/^www\./, '');
const fmt = (v) => v.toLocaleString('en-US');
const rows = pts.map((p) => `            <tr><td class="num">${esc(p.date)}</td><td class="num">${fmt(p.value)}</td><td>${esc(host(p.source))}${p.note ? ` · ${esc(p.note)}` : ''}</td></tr>`).join('\n');

// the figures under the chart: the data's own, then the projection's when there is one
const figs = [...(data.figures || []), ...(proj && proj.figure ? [proj.figure] : [])];
const figItems = figs.map((g) => `          <li class="fig">${g.source ? `<!-- ${g.source.replace(/--/g, '—')} -->` : ''}<span class="fig-num">${esc(g.num)}</span><p class="fig-claim">${esc(g.claim)}</p></li>`).join('\n');
const caption = [data.caption, rate.length ? data.latest_caption : '', proj ? proj.caption : ''].filter(Boolean).map(esc).join(' ');

const block = `<!-- chart D-1: drawn by hack/website-chart.mjs from data/software-per-year.json; edit the data, then run make website-chart -->
        <p class="chart-y">Software made per year: ${esc(data.series.title.charAt(0).toLowerCase() + data.series.title.slice(1))}</p>
        <div class="chart-frame">
          <svg class="chart-svg" viewBox="0 0 1000 1000" preserveAspectRatio="none" role="img" aria-labelledby="d1-name">
            <title id="d1-name">${esc(name)}</title>
            ${grid}
            <path class="chart-pre" d="${pre}"/>
            <path class="chart-post" d="${post}"/>
            <line class="chart-rule" x1="${f(X(inf.year))}" x2="${f(X(inf.year))}" y1="0" y2="1000" vector-effect="non-scaling-stroke"/>
            <path class="chart-line" d="${line}" vector-effect="non-scaling-stroke"/>${dotted ? `
            <path class="chart-rate" d="${dotted}" vector-effect="non-scaling-stroke"/>` : ''}${dashed ? `
            <path class="chart-proj" d="${dashed}" vector-effect="non-scaling-stroke"/>` : ''}
          </svg>
          <div class="chart-labels" aria-hidden="true">
            ${yTicks}
            ${xTicks.join('')}
            <span class="chart-marker" style="--x:${pct(X(inf.year) / 10)}">${esc(inf.label)}</span>
            <i class="chart-dot${rate.length ? ' chart-dot-open' : ''}" style="--x:${pct(X(last.year) / 10)};--y:${pct(Y(last.value) / 10)}"></i>
            <span class="chart-today" style="--x:${pct(X(last.year) / 10)};--y:${pct(Y(last.value) / 10)}">Today${data.latest_note ? `<small>${esc(data.latest_note)}</small>` : ''}</span>${proj ? `
            <span class="chart-proj-label" style="--x:${pct(X(proj.year) / 10)};--y:${pct(Y(proj.value) / 10)}">${esc(proj.label || 'If the curve holds')}</span>` : ''}
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
        </details>${figItems ? `
        <ul class="figs">
${figItems}
        </ul>` : ''}
        <figcaption class="caption" id="curve-cap">${caption}</figcaption>
        <!-- /chart D-1 -->`;

const file = join(site, 'index.html');
const html = readFileSync(file, 'utf8');
const re = /<!-- chart D-1\b[\s\S]*?<!-- \/chart D-1 -->/;
if (!re.test(html)) {
  console.error('website-chart: no <!-- chart D-1 … --> … <!-- /chart D-1 --> block in website/index.html');
  process.exit(1);
}
writeFileSync(file, html.replace(re, block));
console.log(`>> website/index.html: chart D-1, ${pts.length} points${rate.length ? ' (the latest annualised)' : ''}${proj ? ', a projection' : ', no projection'}, ${x0}–${Math.floor(x1)}, top ${short(yTop)}`);
