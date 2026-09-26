// Stand-in for builtin-tiles/prometheus-viewer/prom.js (not extracted yet):
// the page's parser, rate math and formatters, verbatim.
const NNBSP = '\u202f';
const group = (intStr) => intStr.replace(/\B(?=(\d{3})+(?!\d))/g, NNBSP);
export function fmtN(v) {
  if (!Number.isFinite(v)) return String(v);
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
const seriesKey = (name, labels) =>
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
