// hack/xbn/worker.mjs — runs one tile native.js in a worker thread against a
// scripted `xbin` stub, on a virtual clock, and posts back the rendered tree.
// Started by node.mjs (runNative); workerData: {entry, data, steps, caps, state}.
// See node.mjs for the data/steps formats.
import { workerData, parentPort } from 'node:worker_threads';
import { pathToFileURL } from 'node:url';
import { installHooks } from './hooks.mjs';

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
    body = spec.sse.map((f) => `${f.event ? `event: ${f.event}\n` : ''}${f.id ? `id: ${f.id}\n` : ''}data: ${typeof f.data === 'string' ? f.data : JSON.stringify(f.data)}\n\n`).join('');
    if (!headers.has('content-type')) headers.set('content-type', 'text/event-stream');
  } else if (own(spec, 'text')) body = String(spec.text);
  const status = spec.status ?? 200;
  return new Response(status === 204 || status === 304 ? null : body, { status, headers });
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

async function step(s) {
  const ev = (k, type, payload, n) => { activity++; rt.xbn.event(k, type, payload ?? {}, n); };
  if (own(s, 'wait')) return advance(s.wait);
  if (own(s, 'tap')) ev(s.tap, 'tap');
  else if (own(s, 'event')) { const e = s.event; Array.isArray(e) ? ev(...e) : ev(e.k, e.type, e.payload, e.n); }
  else if (own(s, 'input')) ev(s.input[0], 'input', { value: s.input[1] });
  else if (own(s, 'bus')) { const [topic, payload] = s.bus; for (const h of [...eventHandlers]) h({ type: 'bus', topic, data: payload }); activity++; }
  else if (own(s, 'visibility')) rt.xbn.visibility(s.visibility);
  else if (own(s, 'resolve')) rt.xbn.resolve(...s.resolve);
  else throw new Error(`unknown step ${JSON.stringify(s)}`);
  return settle();
}

let fatal = null;
try {
  try { await import(pathToFileURL(entry).href); } catch (e) { rt.moduleError(e, entry); }
  await settle();
  for (const s of steps) await step(s);
} catch (e) { fatal = String(e?.stack ?? e); }

parentPort.postMessage({
  tree: rt.tree,
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
