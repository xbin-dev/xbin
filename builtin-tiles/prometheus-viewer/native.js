// native.js — the Prometheus viewer for the xbin app (docs/frontend-kit.md).
// Logic stays in JS and is the page's own: prom.js (shared with index.html)
// scrapes every source bound to the "sources" interface, keeps the rolling
// history, parses the text format and formats values and rates. Each source is
// a section, each metric a disclosure, each series a row with its value, its
// per-second rate (counters) and a native sparkline. No backend: scraping runs
// in the tile, exactly as on the page.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { asEndpoints, epLabel, POLL_MS, histKey, scrape, fmtN, fmtRate, rateOf, labelStr } from './prom.js';

const ENDPOINTS = asEndpoints(xbin.iface('sources'));
const hist = new Map();                      // histKey -> [{t, v}]
let sources = ENDPOINTS.map(() => null), live = false;   // null → not scraped yet

const tone = (type) => (type === 'counter' ? 'accent' : type === 'gauge' ? 'ok' : 'muted');
const series = (i, m, s) => {
  const h = hist.get(histKey(i, s.key)) ?? [];
  const rate = m.type === 'counter' ? rateOf(h) : null;
  const points = h.filter((p) => Number.isFinite(p.v)).map((p) => [p.t, p.v]);   // JSON has no Inf/NaN
  return html`
    <row title=${labelStr(s.labels) || m.name} mono="all"
         detail=${`${fmtN(s.value)}${rate != null ? ` · ${fmtRate(rate)}/s` : ''}`}>
      <chart kind="spark" series=${[{ name: s.key, points }]} x="time"/>
    </row>`;
};
const metrics = (i, ms) => {
  const names = [...ms.keys()].sort();
  if (!names.length) return html`<empty text="no metrics exported"/>`;
  return repeat(names, (n) => n, (n) => {
    const m = ms.get(n);
    return html`
      <disclosure title=${n}>
        <row title=${n} mono="title" subtitle=${m.help} badge=${m.type} tone=${tone(m.type)}/>
        ${repeat(m.samples, (s) => s.key, (s) => series(i, m, s))}
      </disclosure>`;
  });
};
const source = (e, i) => {
  const src = sources[i];
  return html`
    <section title=${epLabel(e)} footer=${e.url}>
      ${src == null ? html`<progress label="scraping…"/>`
        : src.error ? html`<notice tone="danger" text=${`⚠ ${src.error}`}/>`
        : metrics(i, src.metrics)}
    </section>`;
};
const paint = () => render(html`
  <screen title="Prometheus" style="list">
    <toolbar><badge tone=${live ? 'ok' : 'muted'} ?pulse=${live}>${live ? `live · every ${POLL_MS / 1000}s` : 'idle'}</badge></toolbar>
    ${ENDPOINTS.length === 0 ? html`<empty icon="chart" title="No Prometheus source bound"
        text="Bind this tile's sources interface to one or more components that export a prometheus metrics service (e.g. apps/llm-gw) in the workspace Interfaces tab."/>`
      : repeat(ENDPOINTS, (e) => e.url, source)}
  </screen>`);

async function poll() {
  sources = await scrape(ENDPOINTS, hist);
  live = sources.some((s) => s && !s.error);
  paint();
}
paint();
if (ENDPOINTS.length) {
  (function loop() {   // never overlapping; slower while off screen
    poll().finally(() => setTimeout(loop, document.visibilityState === 'visible' ? POLL_MS : POLL_MS * 5));
  })();
}
