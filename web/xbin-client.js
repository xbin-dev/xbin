/**
 * xbin-client.js — injected into every component document served via /c/
 * (decision D4). Provides the in-frame side of the xbin contract:
 *
 *   xbin.self              — this component's path
 *   xbin.iface(slot)       — a bound http interface: { url, service } — or, for a
 *                             multi:true slot, { service, multi, endpoints: [...] }.
 *                             Call a typed, swappable dependency instead of a
 *                             hard-coded path, e.g. xbin.iface('llm').url
 *   xbin.fetch(url, opts)  — fetch with frame-token attribution attached;
 *                             REQUIRED for calling other elements' APIs
 *                             (streams fine: SSE / chunked responses work)
 *   xbin.ws(path)          — attributed WebSocket to an element API, e.g.
 *                             xbin.ws(`/api/apps/other/stream`) — browsers
 *                             can't set WS headers, so the frame token rides
 *                             a query param that xbind consumes (never
 *                             forwarded to the callee)
 *   xbin.bus.on(prefix,cb) — subscribe to bus topics (granted resources)
 *   xbin.bus.publish(resource, topic, data)
 *   xbin.events.on(cb)     — raw event stream (reload/build/bus/status)
 *   xbin.status(level,msg) — report this tile's condition to the workspace
 *                             (level ok|info|warn|error; 'ok'+'' clears it) —
 *                             the shell shows warn/error as a breathing sidebar
 *                             dot + tab tint. xbin.clearStatus() to clear.
 *   xbin.notify(level,msg) — one-shot user notification (toast)
 *
 * It also reports the document's height to the embedding <bx-frame> so
 * auto-sized frames work. See /docs/elements.md and /docs/protocol.md.
 */

const meta = (name) => document.querySelector(`meta[name="${name}"]`)?.content ?? '';

const self = meta('xbin-component');
let frameToken = meta('xbin-frame-token');

// Resolved http interface slots this component is bound to (plans/interfaces.md):
// { <slot>: { url, service } }. xbin.iface(slot) returns the bound provider so a
// component calls a *typed, swappable* dependency instead of a hard-coded path.
let ifaces = {};
try { ifaces = JSON.parse(meta('xbin-interfaces') || '{}'); } catch { ifaces = {}; }
const iface = (slot) => ifaces[slot] || null;

// --- token refresh (tokens are short-lived; see docs/auth.md) ---
async function refreshToken() {
  try {
    const r = await fetch(`/api/xbin/frame-token?component=${encodeURIComponent(self)}`, {
      headers: frameToken ? { 'X-XBin-Frame-Token': frameToken } : {},
    });
    if (r.ok) frameToken = (await r.json()).token;
  } catch { /* transient; next interval retries */ }
}
if (frameToken) setInterval(refreshToken, 10 * 60 * 1000);

// --- attributed fetch ---
function bfetch(url, opts = {}) {
  const headers = new Headers(opts.headers || {});
  if (frameToken) headers.set('X-XBin-Frame-Token', frameToken);
  return fetch(url, { ...opts, headers });
}

// --- attributed WebSocket (long-lived cross-element streams) ---
function bws(path) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const sep = path.includes('?') ? '&' : '?';
  return new WebSocket(
    `${proto}//${location.host}${path}${sep}frame=${encodeURIComponent(frameToken)}`);
}

// --- event stream (own WS, authenticated by frame token) ---
const eventHandlers = new Set();
let ws = null;
let wsBackoff = 500;

function ensureEvents() {
  if (ws) return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws/events?frame=${encodeURIComponent(frameToken)}`);
  ws.onmessage = (m) => {
    let e; try { e = JSON.parse(m.data); } catch { return; }
    for (const h of eventHandlers) { try { h(e); } catch (err) { console.error(err); } }
  };
  ws.onclose = () => {
    ws = null;
    if (eventHandlers.size > 0) setTimeout(ensureEvents, (wsBackoff = Math.min(wsBackoff * 2, 15000)));
  };
  ws.onopen = () => { wsBackoff = 500; };
}

const bus = {
  /** Subscribe to bus topics. prefix matches Event.topic ("res:scope/name/topic"). */
  on(prefix, cb) {
    const h = (e) => { if (e.type === 'bus' && e.topic?.startsWith(prefix)) cb(e.topic, e.data); };
    eventHandlers.add(h);
    ensureEvents();
    return () => eventHandlers.delete(h);
  },
  /** Publish to a granted bus resource, e.g. publish('res:apps/calendar/events', 'created', {...}). */
  async publish(resource, topic, data) {
    const r = await bfetch('/api/xbin/bus/publish', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ resource, topic, data }),
    });
    if (!r.ok) throw new Error(`bus publish: ${r.status} ${await r.text()}`);
  },
};

const events = {
  on(cb) { eventHandlers.add(cb); ensureEvents(); return () => eventHandlers.delete(cb); },
};

// --- workspace status & notifications (docs/elements.md · workspace AGENTS.md) ---
// Tell the shell how this tile is doing. status() sets a persistent, self-
// clearing condition (a breathing sidebar dot + tab tint for warn/error);
// notify() fires a one-shot toast.
async function report(level, message, transient) {
  const r = await bfetch('/api/xbin/tile-status', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ level, message: message ?? '', transient: !!transient }),
  });
  if (!r.ok) throw new Error(`status: ${r.status} ${await r.text()}`);
}
/** Set this tile's status. level: 'ok'|'info'|'warn'|'error'. 'ok' with no message clears it. */
const status = (level, message) => report(level, message, false);
/** Clear this tile's status (same as status('ok','')). */
const clearStatus = () => report('ok', '', false);
/** One-shot notification (toast) — does not change the persistent status. */
const notify = (level, message) => report(level, message, true);

// --- height reporting to the embedding bx-frame ---
const embedded = window.parent !== window;
if (embedded) {
  let last = 0;
  const report = () => {
    const h = Math.ceil(document.documentElement.getBoundingClientRect().height);
    if (Math.abs(h - last) <= 1) return; // hysteresis: avoid resize loops
    last = h;
    window.parent.postMessage({ type: 'xbin:resize', component: self, height: h }, location.origin);
  };
  new ResizeObserver(report).observe(document.documentElement);
  addEventListener('load', report);
}

// --- dialogs & pop-out windows (docs/elements.md §Dialogs & windows) ---
// A tile is an iframe, so anything that must float over the workspace is
// spawned by the SHELL: we postMessage a request up to our bx-frame, which
// tags it with our verified component id and relays it to <bx-shell>. Replies
// come back keyed by id.
const pending = new Map();
let reqSeq = 0;
if (embedded) {
  addEventListener('message', (e) => {
    if (e.source !== window.parent) return;               // only our own frame
    const d = e.data;
    if (d?.type === 'xbin:reply' && pending.has(d.id)) {
      const resolve = pending.get(d.id);
      pending.delete(d.id);
      resolve(d.result);
    }
  });
}
const nextId = () => `${self}#${++reqSeq}.${Math.random().toString(36).slice(2, 7)}`;

// xbin.dialog(spec) → Promise<{button, values}>. Shell-rendered trusted modal
// (title/message/fields/buttons — plain data, no markup). Resolves with the
// clicked button's value (null on dismiss) and the field values. Falls back to
// an in-frame modal when not embedded in the shell.
async function dialog(spec = {}) {
  if (embedded) {
    return new Promise((resolve) => {
      const id = nextId();
      pending.set(id, resolve);
      window.parent.postMessage({ type: 'xbin:dialog', id, spec }, location.origin);
    });
  }
  await import('/vendor/bx-dialog.js');
  return new Promise((resolve) => {
    const el = document.createElement('bx-dialog');
    el.spec = spec; el.open = true;
    el.addEventListener('bx-dialog-resolve', (e) => { el.remove(); resolve(e.detail); }, { once: true });
    document.body.appendChild(el);
  });
}

// xbin.window(spec) → handle. Opens a floating, top-level workspace window whose
// body is a real tile frame — by default one of THIS component's own sub-paths
// (spec.path, e.g. "editor" → /c/<self>/editor/), so it runs your own UI and
// talks to your backend with the usual xbin.fetch; the window escapes the tile's
// clipping. spec.src frames a full component path instead (subject to the same
// tile-access RBAC as any <bx-frame>). Needs the shell — a no-op standalone.
//   spec = { path? | src?, title?, width?, height?, x?, y? }
//   handle = { id, close(), closed: Promise<void> }
function openWindow(spec = {}) {
  if (!embedded) { console.warn('xbin.window needs the workspace shell'); return { id: null, close() {}, closed: Promise.resolve() }; }
  const id = nextId();
  const closed = new Promise((resolve) => pending.set(id, resolve));
  window.parent.postMessage({ type: 'xbin:window', id, spec }, location.origin);
  return {
    id,
    close() { window.parent.postMessage({ type: 'xbin:window-close', id }, location.origin); },
    closed,
  };
}

window.xbin = Object.freeze({ self, iface, fetch: bfetch, ws: bws, bus, events, dialog, window: openWindow, status, clearStatus, notify });
