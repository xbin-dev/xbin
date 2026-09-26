// native/tools/fixture-lib.mjs — the pieces of the fixture runner that are
// plain functions (tested by fixture.test.mjs): reading a fixture's data.json
// into a runNative run, the canonical expected.json layout, and a readable
// diff between two trees.
import { readFileSync, existsSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

export const ROOT = new URL('../..', import.meta.url).pathname.replace(/\/$/, '');
export const FIXTURES = join(ROOT, 'native/fixtures');

// Defaults that keep a fixture independent of the machine running it.
export const DEFAULT_NOW = '2026-09-21T14:13:20Z';
export const DEFAULT_TZ = 'UTC';
export const DEFAULT_LOCALE = 'en-US';

const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);

// data.json keys: what the runner (hack/xbn/node.mjs) takes, plus the
// fixture's own. Anything else is a typo and an error.
const RUN_KEYS = ['self', 'now', 'tz', 'locale', 'iface', 'routes', 'calls', 'dialog'];
const OWN_KEYS = ['about', 'caps', 'state', 'interactions', 'allowDiagnostics'];
const STEP_KEYS = ['after', 'k', 'select', 'type', 'payload', 'n', 'bus', 'visibility', 'resolve', 'note'];

export function listFixtures(dir = FIXTURES) {
  if (!existsSync(dir)) return [];
  return readdirSync(dir).filter((n) => !n.startsWith('.') && statSync(join(dir, n)).isDirectory()
    && existsSync(join(dir, n, 'native.js'))).sort();
}

// loadFixture(name) → {name, dir, entry, data, run: {entry, data, steps, caps, state}, expected|null}
export function loadFixture(name, dir = FIXTURES) {
  const fdir = join(dir, name);
  const entry = join(fdir, 'native.js');
  const dataFile = join(fdir, 'data.json');
  const data = existsSync(dataFile) ? JSON.parse(readFileSync(dataFile, 'utf8')) : {};
  const expFile = join(fdir, 'expected.json');
  const expected = existsSync(expFile) ? JSON.parse(readFileSync(expFile, 'utf8')) : null;
  return { name, dir: fdir, entry, data, run: toRun(entry, data, name), expected, expectedText: expected ? readFileSync(expFile, 'utf8') : null };
}

// toRun(entry, data) → the runNative options for a fixture.
export function toRun(entry, data, name = 'fixture') {
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error(`${name}: data.json must be an object`);
  for (const k of Object.keys(data)) {
    if (!RUN_KEYS.includes(k) && !OWN_KEYS.includes(k)) throw new Error(`${name}: data.json has an unknown key ${JSON.stringify(k)} (known: ${[...RUN_KEYS, ...OWN_KEYS].join(', ')})`);
  }
  const run = {};
  for (const k of RUN_KEYS) if (own(data, k)) run[k] = data[k];
  const now = own(data, 'now') ? data.now : DEFAULT_NOW;
  run.now = typeof now === 'string' ? Date.parse(now) : Number(now);
  if (!Number.isFinite(run.now)) throw new Error(`${name}: data.json "now" is not a time: ${JSON.stringify(now)}`);
  run.tz = data.tz ?? DEFAULT_TZ;
  run.locale = data.locale ?? DEFAULT_LOCALE;
  return { entry, data: run, steps: toSteps(data.interactions ?? [], name), caps: data.caps ?? undefined, state: data.state ?? null };
}

// toSteps(interactions) → runNative steps. An interaction:
//   {after?: ms, k?|select?, type?, payload?, n?, bus?, visibility?, resolve?, note?}
// `after` moves the virtual clock first; then at most one action: an event on
// a node (k or select + type), a bus event ([topic, data] or {topic, data}),
// a visibility change, or answering a call ([id, value]). Only `after`: a wait.
export function toSteps(list, name = 'fixture') {
  if (!Array.isArray(list)) throw new Error(`${name}: "interactions" must be an array`);
  const steps = [];
  list.forEach((it, i) => {
    const at = `${name}: interactions[${i}]`;
    if (!it || typeof it !== 'object') throw new Error(`${at} must be an object`);
    for (const k of Object.keys(it)) if (!STEP_KEYS.includes(k)) throw new Error(`${at} has an unknown key ${JSON.stringify(k)} (known: ${STEP_KEYS.join(', ')})`);
    const after = Number(it.after ?? 0);
    if (!Number.isFinite(after) || after < 0) throw new Error(`${at}.after must be a non-negative number of ms`);
    if (after > 0) steps.push({ wait: after });
    const acts = ['k', 'select', 'bus', 'visibility', 'resolve'].filter((k) => own(it, k));
    if (acts.includes('k') && acts.includes('select')) throw new Error(`${at}: give k or select, not both`);
    const kinds = acts.filter((k) => k !== 'select' || !acts.includes('k'));
    if (kinds.length > 1) throw new Error(`${at}: one action per interaction (got ${kinds.join(', ')})`);
    if (own(it, 'k') || own(it, 'select')) {
      if (typeof it.type !== 'string' || !it.type) throw new Error(`${at}: an event needs a "type"`);
      const ev = { type: it.type, payload: it.payload ?? {} };
      if (own(it, 'k')) ev.k = String(it.k); else ev.select = it.select;
      if (own(it, 'n')) ev.n = it.n;
      steps.push({ event: ev });
    } else if (own(it, 'bus')) {
      const b = it.bus;
      const pair = Array.isArray(b) ? b : [b?.topic, b?.data];
      if (typeof pair[0] !== 'string') throw new Error(`${at}.bus needs a topic`);
      steps.push({ bus: [pair[0], pair[1] ?? null] });
    } else if (own(it, 'visibility')) {
      if (it.visibility !== 'hidden' && it.visibility !== 'visible') throw new Error(`${at}.visibility is "hidden" or "visible"`);
      steps.push({ visibility: it.visibility });
    } else if (own(it, 'resolve')) {
      if (!Array.isArray(it.resolve)) throw new Error(`${at}.resolve is [callId, value]`);
      steps.push({ resolve: it.resolve });
    } else if (own(it, 'type') || own(it, 'payload')) {
      throw new Error(`${at}: an event needs "k" or "select"`);
    }
  });
  return steps;
}

// format(tree) → expected.json text: one node per line, children indented,
// keys in wire order (k, t, p, e, c) — reviewable in a diff, and valid JSON.
export function format(tree) {
  const v = JSON.stringify(tree.v);
  if (!tree.root) return `{"v":${v},"root":null}\n`;
  const lines = [];
  // markdown tokens (the runtime's, often kilobytes) get a line per block
  const props = (p, ind) => `{${Object.entries(p).map(([k, x]) => (k === 'tokens' && Array.isArray(x) && x.length
    ? `"tokens":[\n${x.map((b) => `${ind}    ${JSON.stringify(b)}`).join(',\n')}\n${ind}  ]`
    : `${JSON.stringify(k)}:${JSON.stringify(x)}`)).join(',')}}`;
  const head = (n, ind) => {
    let h = `{"k":${JSON.stringify(n.k)},"t":${JSON.stringify(n.t)}`;
    if (n.p !== undefined) h += `,"p":${props(n.p, ind)}`;
    if (n.e !== undefined) h += `,"e":${JSON.stringify(n.e)}`;
    return h;
  };
  const node = (n, ind, tail, prefix = '') => {
    const h = `${ind}${prefix}${head(n, ind)}`;
    if (n.c === undefined) lines.push(`${h}}${tail}`);
    else if (!n.c.length) lines.push(`${h},"c":[]}${tail}`);
    else {
      lines.push(`${h},"c":[`);
      n.c.forEach((c, i) => node(c, `${ind}  `, i === n.c.length - 1 ? '' : ','));
      lines.push(`${ind}]}${tail}`);
    }
  };
  node(tree.root, '', '}', `{"v":${v},"root":`);
  return `${lines.join('\n')}\n`;
}

// ── diffs ────────────────────────────────────────────────────────────────────

const clip = (v, max = 100) => {
  const s = v === undefined ? 'absent' : JSON.stringify(v);
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
};

// firstDiff(a, b) → [path, a-value, b-value] at the first place two JSON values differ, or null.
export function firstDiff(a, b, path = '') {
  if (a === b) return null;
  if (typeof a !== typeof b || a === null || b === null || typeof a !== 'object' || Array.isArray(a) !== Array.isArray(b)) {
    return Number.isNaN(a) && Number.isNaN(b) ? null : [path, a, b];
  }
  if (Array.isArray(a)) {
    for (let i = 0; i < Math.max(a.length, b.length); i++) {
      const d = firstDiff(a[i], b[i], `${path}[${i}]`);
      if (d) return d;
    }
    return null;
  }
  const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])];
  for (const k of keys) {
    const d = firstDiff(a[k], b[k], `${path}.${k}`);
    if (d) return d;
  }
  return null;
}

function flatten(root) {
  const m = new Map();
  const walk = (n, parent) => { m.set(n.k, { n, parent }); for (const c of n.c || []) walk(c, n.k); };
  if (root) walk(root, null);
  return m;
}

// treeDiff(expected, actual) → readable lines (empty when equal). Nodes are
// matched by key (keys are unique and stable); a whole missing or extra
// subtree is one line.
export function treeDiff(expected, actual) {
  const out = [];
  if (expected?.v !== actual?.v) out.push(`v: expected ${clip(expected?.v)}, got ${clip(actual?.v)}`);
  const E = flatten(expected?.root), A = flatten(actual?.root);
  const name = (n) => `${n.k} (${n.t})`;
  for (const [k, { n, parent }] of E) {
    if (A.has(k)) continue;
    if (parent != null && !A.has(parent)) continue; // its parent is already reported
    out.push(`- ${name(n)} missing${parent != null ? ` from ${parent}` : ''}${n.c?.length ? ` (with ${countNodes(n) - 1} descendants)` : ''}`);
  }
  for (const [k, { n, parent }] of A) {
    if (E.has(k)) continue;
    if (parent != null && !E.has(parent)) continue;
    out.push(`+ ${name(n)} unexpected${parent != null ? ` in ${parent}` : ''}: ${clip(n.p ?? {}, 80)}`);
  }
  for (const [k, { n: e, parent: ep }] of E) {
    const a = A.get(k)?.n;
    if (!a) continue;
    const at = name(e);
    if (A.get(k).parent !== ep) out.push(`~ ${at} moved from parent ${ep} to ${A.get(k).parent}`);
    if (e.t !== a.t) { out.push(`~ ${k}: type expected ${e.t}, got ${a.t}`); continue; }
    const ep_ = e.p ?? {}, ap = a.p ?? {};
    for (const p of [...new Set([...Object.keys(ep_), ...Object.keys(ap)])]) {
      const d = firstDiff(ep_[p], ap[p]);
      if (!d) continue;
      if (!Object.prototype.hasOwnProperty.call(ap, p)) out.push(`~ ${at} p.${p}: expected ${clip(ep_[p])}, got absent`);
      else if (!Object.prototype.hasOwnProperty.call(ep_, p)) out.push(`~ ${at} p.${p}: unexpected ${clip(ap[p])}`);
      else if (d[0]) out.push(`~ ${at} p.${p}${d[0]}: expected ${clip(d[1])}, got ${clip(d[2])}`);
      else out.push(`~ ${at} p.${p}: expected ${clip(d[1])}, got ${clip(d[2])}`);
    }
    if (firstDiff(e.e ?? [], a.e ?? [])) out.push(`~ ${at} e: expected ${clip(e.e ?? [])}, got ${clip(a.e ?? [])}`);
    if (e.p === undefined && a.p !== undefined && !Object.keys(a.p).length) out.push(`~ ${at} p: expected absent, got {}`);
    if (e.p !== undefined && a.p === undefined && !Object.keys(e.p).length) out.push(`~ ${at} p: expected {}, got absent`);
    if (e.e === undefined && a.e !== undefined && !a.e.length) out.push(`~ ${at} e: expected absent, got []`);
    if (e.e !== undefined && a.e === undefined && !e.e.length) out.push(`~ ${at} e: expected [], got absent`);
    if ((e.c === undefined) !== (a.c === undefined) && !(e.c?.length || a.c?.length)) {
      out.push(`~ ${at} c: expected ${e.c === undefined ? 'absent' : '[]'}, got ${a.c === undefined ? 'absent' : '[]'}`);
    }
    const ek = (e.c || []).map((c) => c.k).filter((x) => A.get(x)?.parent === k);
    const ak = (a.c || []).map((c) => c.k).filter((x) => E.get(x)?.parent === k);
    if (ek.join('\n') !== ak.join('\n')) out.push(`~ ${at} children order: expected ${clip(ek, 160)}, got ${clip(ak, 160)}`);
  }
  if (!out.length && firstDiff(expected, actual)) {
    const d = firstDiff(expected, actual);
    out.push(`~ ${d[0] || '(tree)'}: expected ${clip(d[1])}, got ${clip(d[2])}`);
  }
  return out;
}

export function countNodes(n) {
  let c = 1;
  for (const x of n?.c || []) c += countNodes(x);
  return n ? c : 0;
}
