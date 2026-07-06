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
 *   xbin.events.on(cb)     — raw event stream (reload/build/bus)
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

// --- height reporting to the embedding bx-frame ---
if (window.parent !== window) {
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

window.xbin = Object.freeze({ self, iface, fetch: bfetch, ws: bws, bus, events });
