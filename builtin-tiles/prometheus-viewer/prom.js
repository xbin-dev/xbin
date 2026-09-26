// prom.js — the Prometheus viewer's logic, shared by its web page
// (index.html) and its native view (native.js): the interface binding, one
// scrape of every bound source into a rolling history, the text-format
// parser, rate math and number formatting. No DOM; the views only draw.

// ── interface binding (docs/overview/11-interfaces.md, multi:true) ─────────
// "sources": components exporting a Prometheus "/metrics" endpoint. The owner
// binds one or more providers in the admin Interfaces tab; each resolves to
// { provider, instance?, url, service }. Binding is also the scrape grant.
export const asEndpoints = (iface) =>
  iface?.endpoints ?? (iface?.url ? [{ url: iface.url, provider: iface.service }] : []);
export const epLabel = (e) => (e.instance ? `${e.provider}#${e.instance}` : e.provider);

export const POLL_MS = 3000;   // scrape cadence
export const HIST = 30;        // samples retained per series for the sparkline

// histKey(i, seriesKey): a series' history key — per source, so the same
// series scraped from two sources keeps two histories.
export const histKey = (i, key) => i + '' + key;

// scrape(endpoints, hist): fetch every source's /metrics once (in parallel)
// and append each sample to hist (Map histKey → [{t, v}], at most HIST long).
// Resolves to one {error, metrics} per endpoint, in order: metrics is the
// parsed Map, or null with the error.
export async function scrape(endpoints, hist, fetch = (url) => globalThis.xbin.fetch(url)) {
  const now = Date.now();
  return Promise.all(endpoints.map(async (ep, i) => {
    try {
      const url = ep.url.replace(/\/+$/, '') + '/metrics';
      const r = await fetch(url);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const metrics = parseProm(await r.text());
      for (const m of metrics.values())
        for (const s of m.samples) {
          const hk = histKey(i, s.key);
          let arr = hist.get(hk);
          if (!arr) hist.set(hk, (arr = []));
          arr.push({ t: now, v: s.value });
          if (arr.length > HIST) arr.shift();
        }
      return { error: '', metrics };
    } catch (e) {
      return { error: String(e.message ?? e), metrics: null };
    }
  }));
}

// ── number formatting ───────────────────────────────────────────────────────
// Group digits with a narrow no-break space so long counts stay readable and
// don't wrap mid-number: 123123123 → "123 123 123" (fmtN, as in llm-gw).
const NNBSP = '\u202f';
const group = (intStr) => intStr.replace(/\B(?=(\d{3})+(?!\d))/g, NNBSP);
export function fmtN(v) {
  if (!Number.isFinite(v)) return String(v);           // +Inf / -Inf / NaN
  const neg = v < 0, a = Math.abs(v);
  let out;
  if (Number.isInteger(a)) out = group(String(a));
  else if (a >= 100) out = group(a.toFixed(0));
  else if (a >= 1) out = a.toFixed(2);
  else out = a.toPrecision(3).replace(/\.?0+$/, '');
  return (neg ? '-' : '') + out;
}
export function fmtRate(r) {
  if (!Number.isFinite(r)) return String(r);
  if (r === 0) return '0';
  if (r >= 100) return fmtN(Math.round(r));
  if (r >= 1) return r.toFixed(1);
  return r.toPrecision(2).replace(/\.?0+$/, '');
}

// ── Prometheus text-format parser ───────────────────────────────────────────
// Handles `# HELP <name> <text>`, `# TYPE <name> <type>`, and sample lines
// `<name>{label="v",…} <value> [timestamp]` (also bare `<name> <value>`).
// Returns Map<name, { name, type, help, samples: [{ labels, key, value }] }>.
const SAMPLE_RE = /^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})?[ \t]+(.+)$/;
const LABEL_RE = /([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*"((?:[^"\\]|\\.)*)"/g;

const unescape = (s) => s.replace(/\\(["\\n])/g, (_, c) => (c === 'n' ? '\n' : c));

function parseLabels(inner) {
  const out = {};
  let m;
  LABEL_RE.lastIndex = 0;
  while ((m = LABEL_RE.exec(inner))) out[m[1]] = unescape(m[2]);
  return out;
}
function parseValue(s) {
  if (s === '+Inf' || s === 'Inf') return Infinity;
  if (s === '-Inf') return -Infinity;
  if (s === 'NaN') return NaN;
  return parseFloat(s);
}
// Stable identity for a series (metric + its label set), for history keying.
export const seriesKey = (name, labels) =>
  name + '{' + Object.keys(labels).sort().map((k) => `${k}=${labels[k]}`).join(',') + '}';

export function parseProm(text) {
  const metrics = new Map();
  const get = (name) => {
    let m = metrics.get(name);
    if (!m) { m = { name, type: 'untyped', help: '', samples: [] }; metrics.set(name, m); }
    return m;
  };
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    if (line[0] === '#') {
      const mm = line.match(/^#\s+(HELP|TYPE)\s+(\S+)\s+(.*)$/);
      if (!mm) continue;
      const m = get(mm[2]);
      if (mm[1] === 'HELP') m.help = unescape(mm[3].trim());
      else m.type = mm[3].trim();
      continue;
    }
    const sm = line.match(SAMPLE_RE);
    if (!sm) continue;
    const labels = sm[2] ? parseLabels(sm[2].slice(1, -1)) : {};
    const value = parseValue(sm[3].trim().split(/\s+/)[0]);
    get(sm[1]).samples.push({ labels, key: seriesKey(sm[1], labels), value });
  }
  return metrics;
}

// Average per-second rate across the retained window (smoother than the last
// two points). null when there is not enough history; 0 on a counter reset.
export function rateOf(hist) {
  if (hist.length < 2) return null;
  const a = hist[0], b = hist[hist.length - 1];
  const dt = (b.t - a.t) / 1000;
  if (dt <= 0) return null;
  const dv = b.v - a.v;
  if (!Number.isFinite(dv) || dv < 0) return 0;
  return dv / dt;
}

export const labelStr = (labels) => {
  const keys = Object.keys(labels);
  return keys.length ? '{' + keys.map((k) => `${k}="${labels[k]}"`).join(', ') + '}' : '';
};
