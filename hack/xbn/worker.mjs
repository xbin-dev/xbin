// hack/xbn/worker.mjs — runs one tile native.js in a worker thread against a
// scripted `xbin` stub, on a virtual clock, and posts back the rendered tree.
// Started by node.mjs (runNative); workerData: {entry, data, steps, caps, state}.
// See node.mjs for the data/steps formats.
import { workerData, parentPort } from 'node:worker_threads';
import { pathToFileURL } from 'node:url';
import { installHooks } from './hooks.mjs';
import { selectKey, findNode } from './select.mjs';
import { VOCAB } from '../../web/xb/vocab.js';

installHooks();

const { entry, data = {}, steps = [], caps, state } = workerData;
const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);
const realImmediate = setImmediate;
let activity = 0;

// ── the virtual clock: Date, timers and rAF only move when a step waits ──────
let now = Number(data.now ?? Date.UTC(2026, 0, 1, 12));
const RealDate = Date;
class VirtualDate extends RealDate {
  constructor(...a) { if (a.length === 0) super(now); else super(...a); }
  static now() { return now; }
}
globalThis.Date = VirtualDate;
if (data.tz) process.env.TZ = data.tz;

const timers = new Map();
let timerSeq = 0;
const addTimer = (fn, ms, args, every) => {
  const id = ++timerSeq;
  const d = Math.max(0, Number(ms) || 0);
  timers.set(id, { id, due: now + d, fn, args, every: every ? Math.max(1, d) : 0 });
  return id;
};
globalThis.setTimeout = (fn, ms, ...args) => addTimer(fn, ms, args, false);
globalThis.setInterval = (fn, ms, ...args) => addTimer(fn, ms, args, true);
globalThis.clearTimeout = globalThis.clearInterval = (id) => { timers.delete(id); };
globalThis.requestAnimationFrame = (fn) => addTimer(() => fn(now), 0, [], false);
globalThis.cancelAnimationFrame = (id) => { timers.delete(id); };

function nextTimer(limit) {
  let best = null;
  for (const t of timers.values()) if (t.due <= limit && (!best || t.due < best.due || (t.due === best.due && t.id < best.id))) best = t;
  return best;
}
function runTimer(t) {
  if (t.every) t.due += t.every; else timers.delete(t.id);
  activity++;
  try { if (typeof t.fn === 'function') t.fn(...t.args); } catch (e) { rt.uncaught(e, 'timer'); }
}

// ── the document and the xbin stub ───────────────────────────────────────────
const self = String(data.self ?? 'apps/tile');
const metas = { 'xbin-component': self, 'xbin-sandbox': 'allow-scripts allow-downloads', 'xbin-native': '1', 'xbin-frame-token': 'node-runner' };
const docListeners = new Map();
globalThis.window = globalThis;
globalThis.location = new URL(`https://xbin.test/c/${self}/`);
globalThis.document = {
  visibilityState: 'visible',
  hidden: false,
  baseURI: pathToFileURL(entry).href,
  querySelector(sel) {
    const m = String(sel).match(/^meta\[name=["']?([^"'\]]+)["']?\]$/);
    return m && own(metas, m[1]) ? { content: metas[m[1]], getAttribute: (a) => (a === 'content' ? metas[m[1]] : null) } : null;
  },
  addEventListener(type, fn) { if (!docListeners.has(type)) docListeners.set(type, new Set()); docListeners.get(type).add(fn); },
  removeEventListener(type, fn) { docListeners.get(type)?.delete(fn); },
  dispatchEvent(ev) { for (const fn of docListeners.get(ev.type) || []) fn(ev); return true; },
};

const requests = [];
const unmatched = [];
const routeUse = new Map();
function routeFor(method, url) {
  const routes = data.routes || {};
  const path = url.split('?')[0];
  for (const k of [`${method} ${url}`, `${method} ${path}`, url, path]) if (own(routes, k)) return [k, routes[k]];
  return [null, null];
}
function responseOf(spec) {
  const headers = new Headers(spec.headers || {});
  let body = null;
  if (own(spec, 'json')) { body = JSON.stringify(spec.json); if (!headers.has('content-type')) headers.set('content-type', 'application/json'); }
  else if (own(spec, 'sse')) {
    const frame = (f) => `${f.event ? `event: ${f.event}\n` : ''}${f.id ? `id: ${f.id}\n` : ''}data: ${typeof f.data === 'string' ? f.data : JSON.stringify(f.data)}\n\n`;
    body = spec.open || spec.sse.some((f) => f.after) ? sseStream(spec.sse, frame, !!spec.open) : spec.sse.map(frame).join('');
    if (!headers.has('content-type')) headers.set('content-type', 'text/event-stream');
  } else if (own(spec, 'text')) body = String(spec.text);
  const status = spec.status ?? 200;
  return new Response(status === 204 || status === 304 ? null : body, { status, headers });
}
// A scripted event stream: each frame is written `after` virtual ms after the
// previous one (so a tile sees tokens arrive as the clock moves); `open`
// leaves the stream open after the last frame (a turn still streaming).
function sseStream(frames, frame, open) {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(ctl) {
      let at = 0;
      frames.forEach((f, i) => {
        at += Math.max(0, Number(f.after) || 0);
        setTimeout(() => {
          activity++;
          ctl.enqueue(enc.encode(frame(f)));
          if (i === frames.length - 1 && !open) ctl.close();
        }, at);
      });
      if (!frames.length && !open) ctl.close();
    },
  });
}

async function stubFetch(input, opts = {}) {
  const url = String(input?.url ?? input);
  const method = String(opts.method || input?.method || 'GET').toUpperCase();
  let body = opts.body;
  if (typeof body === 'string') { try { body = JSON.parse(body); } catch { /* keep text */ } }
  requests.push({ method, url, body: body ?? null, at: now });
  activity++;
  const [key, route] = routeFor(method, url);
  if (!key) { unmatched.push(`${method} ${url}`); return new Response(JSON.stringify({ error: `no stub for ${method} ${url}` }), { status: 404 }); }
  let spec = route;
  if (Array.isArray(route)) { const i = routeUse.get(key) ?? 0; routeUse.set(key, i + 1); spec = route[Math.min(i, route.length - 1)]; }
  if (spec?.error) throw new TypeError(String(spec.error));
  if (spec?.delay) await new Promise((r) => setTimeout(r, spec.delay));
  return responseOf(spec || {});
}

const eventHandlers = new Set();
const published = [];
globalThis.xbin = {
  self,
  iface: (slot) => data.iface?.[slot] ?? null,
  fetch: stubFetch,
  url: (p) => `https://xbin.test${p}`,
  ws() { throw new Error('xbin.ws is not available in the node runner'); },
  download() {},
  bus: {
    on(prefix, cb) {
      const h = (e) => { if (e.type === 'bus' && e.topic?.startsWith(prefix)) cb(e.topic, e.data); };
      eventHandlers.add(h);
      return () => eventHandlers.delete(h);
    },
    async publish(resource, topic, payload) { published.push({ resource, topic, data: payload }); },
  },
  events: { on(cb) { eventHandlers.add(cb); return () => eventHandlers.delete(cb); } },
  dialog: async () => data.dialog ?? null,
  window: async () => null,
  status: async () => {},
  clearStatus: async () => {},
  notify: async () => {},
  native: { caps, state },
};

// ── the runtime ──────────────────────────────────────────────────────────────
const messages = [];
const xb = await import(new URL('../../web/xb-native.js', import.meta.url).href);
const rt = xb.createRuntime({
  global: true,
  log: false, // diagnostics and errors come back as messages
  caps,
  state,
  document: globalThis.document,
  schedule: () => { activity++; }, // settle() flushes
  post(m) {
    messages.push(m);
    activity++;
    if (m.op === 'call' && data.calls !== false) {
      const answer = data.calls?.[m.what] ?? null;
      queueMicrotask(() => rt.xbn.resolve(m.id, answer));
    }
  },
});
process.on('unhandledRejection', (e) => rt.uncaught(e, 'unhandled rejection'));

async function settle() {
  let quiet = 0;
  for (let rounds = 0; quiet < 3; rounds++) {
    if (rounds > 20000) throw new Error('native.js never settles (a loop at the pinned time?)');
    const before = activity;
    await new Promise((r) => realImmediate(r));
    for (let t = nextTimer(now), guard = 0; t; t = nextTimer(now)) {
      if (++guard > 10000) throw new Error('timers at the pinned time never stop');
      runTimer(t);
    }
    if (rt.pending) rt.flush();
    quiet = activity === before ? quiet + 1 : 0;
  }
}
async function advance(ms) {
  const target = now + Math.max(0, Number(ms) || 0);
  for (let t = nextTimer(target), guard = 0; t; t = nextTimer(target)) {
    if (++guard > 100000) throw new Error('too many timers while waiting');
    now = Math.max(now, t.due);
    runTimer(t);
    await settle();
  }
  now = target;
  await settle();
}

// A step may name its node by key or by a matcher: {t?, p?: {prop: value}
// (each equal as JSON), has?: text (a substring of the node's props as JSON),
// in?: <matcher of an ancestor>, nth?: n (the n-th match, from 0)}. The
// first match in tree order is taken; none is a fatal error naming it.
function matches(n, m) {
  if (m.t && n.t !== m.t) return false;
  for (const [k, v] of Object.entries(m.p || {})) if (JSON.stringify(n.p?.[k]) !== JSON.stringify(v)) return false;
  if (m.has != null && !JSON.stringify(n.p || {}).includes(String(m.has))) return false;
  return true;
}
function findAll(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  if (inside && matches(root, m)) out.push(root);
  const deeper = inside || matches(root, m.in);
  for (const c of root.c || []) findAll(c, m, out, deeper);
  return out;
}
function keyOf(x) {
  if (typeof x === 'string') return x;
  const hits = findAll(rt.tree.root, x);
  const hit = hits[x.nth ?? 0];
  if (!hit) throw new Error(`no node matches ${JSON.stringify(x)}`);
  return hit.k;
}

// target(e): the key of a {k | select, type} event step (k: a key or a
// matcher, keyOf), checked against the tree the app shows now — the node
// exists and takes the event (listens to it, or the event reports one of its
// controlled props).
function target(e) {
  const root = rt.tree?.root;
  const k = e.select != null ? selectKey(root, e.select) : keyOf(e.k);
  const node = findNode(root, k);
  if (!node) throw new Error(`event ${JSON.stringify(e.type)}: no node ${JSON.stringify(k)} in the tree`);
  const rep = VOCAB.prims[node.t]?.events?.[e.type]?.reports;
  if (!(node.e || []).includes(e.type) && !(rep && own(node.p, rep.prop))) {
    throw new Error(`event ${JSON.stringify(e.type)} on ${k} (${node.t}): the node does not listen to it (e: ${JSON.stringify(node.e || [])})`);
  }
  return k;
}

async function step(s) {
  const ev = (k, type, payload, n) => { activity++; rt.xbn.event(keyOf(k), type, payload ?? {}, n); };
  if (own(s, 'wait')) return advance(s.wait);
  if (own(s, 'snapshot')) { snapshots[s.snapshot] = rt.tree; return undefined; }
  if (own(s, 'call')) {
    const [name, ...args] = s.call;
    if (typeof setupMod?.[name] !== 'function') throw new Error(`the setup module has no export ${name}`);
    activity++;
    await setupMod[name](...args);
    return settle();
  }
  if (own(s, 'tap')) ev(s.tap, 'tap');
  else if (own(s, 'event')) { const e = s.event; Array.isArray(e) ? ev(...e) : ev(target(e), e.type, e.payload, e.n); }
  else if (own(s, 'input')) ev(s.input[0], 'input', { value: s.input[1] });
  else if (own(s, 'bus')) { const [topic, payload] = s.bus; for (const h of [...eventHandlers]) h({ type: 'bus', topic, data: payload }); activity++; }
  else if (own(s, 'visibility')) rt.xbn.visibility(s.visibility);
  else if (own(s, 'resolve')) rt.xbn.resolve(...s.resolve);
  else throw new Error(`unknown step ${JSON.stringify(s)}`);
  return settle();
}

// data.setup: a module (a path) imported before the tile — its default export
// is called with {data, touch} and may replace parts of `xbin` (a test's own
// fake backend); `{call: [name, …args]}` steps call its other exports, and
// its `result()` (if any) comes back as `extra`. `touch()` counts as activity
// (settling waits for it to stop).
const snapshots = {};
let setupMod = null;
let fatal = null;
try {
  if (data.setup) {
    setupMod = await import(pathToFileURL(String(data.setup)).href);
    await setupMod.default?.({ data, touch: () => { activity++; } });
  }
  try { await import(pathToFileURL(entry).href); } catch (e) { rt.moduleError(e, entry); }
  await settle();
  for (const s of steps) await step(s);
} catch (e) { fatal = String(e?.stack ?? e); }

let extra = null;
try { extra = (await setupMod?.result?.()) ?? null; } catch (e) { extra = { error: String(e) }; }

parentPort.postMessage({
  tree: rt.tree,
  snapshots,
  extra,
  messages,
  diagnostics: rt.diagnostics,
  errors: messages.filter((m) => m.op === 'error'),
  requests,
  unmatched,
  published,
  now,
  markdown: rt.markdownStats,
  fatal,
});
