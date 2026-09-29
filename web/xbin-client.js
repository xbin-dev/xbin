/**
 * xbin-client.js — injected into every component document served via /c/
 * (decision D4). Provides the in-frame side of the xbin contract:
 *
 *   xbin.self              — this component's path (the tile's, in every
 *                             one of its deployments)
 *   xbin.deployment        — only in a document of a tile deployment other
 *                             than the tile's primary (/c/<tile>+<name>/):
 *                             its name. Absent means the primary
 *   xbin.partition         — only in a partitioned tile's document
 *                             (docs/partitions.md): the partition the viewer
 *                             reaches, "user:<id>" (their own) or "global"
 *   xbin.iface(slot)       — a bound http interface: { url, service } — or, for a
 *                             multi:true slot, { service, multi, endpoints: [...] }.
 *                             Call a typed, swappable dependency instead of a
 *                             hard-coded path, e.g. xbin.iface('llm').url
 *   xbin.fetch(url, opts)  — fetch with frame-token attribution attached;
 *                             REQUIRED for calling other elements' APIs
 *                             (streams fine: SSE / chunked responses work).
 *                             opts.partition = 'global' calls the tile's
 *                             global instance from a user partition — on
 *                             the tile's own /api/<self>/… only
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
 *   xbin.native            — only in the xbin app's runtime document (a tile's
 *                             native.js): caps, supports, meta, copy, share,
 *                             open, state/saveState — see /vendor/xb-native.js
 *
 * It also reports the document's height to the embedding <bx-frame> so
 * auto-sized frames work. See /docs/elements.md and /docs/protocol.md.
 *
 * Isolation (docs/auth.md §Who is calling): non-chrome tiles run in a SANDBOXED opaque
 * origin — no parent/sibling DOM, no localStorage/IDB/cookies, and the
 * ambient session cookie is worthless on requests out of a tile frame. The
 * frame token below is the tile's ONLY credential; xbin.fetch/xbin.ws attach
 * it, and the token alone authenticates (no cookie required). location.origin
 * here is "null", so all postMessage targets are '*': identity on both sides
 * is verified by comparing event.source windows, never origins. On a tile's
 * own origin (strict tile asset gating's origins mode) the server names the
 * workspace origin: messages go to it only, and replies must come from it.
 */

const meta = (name) => document.querySelector(`meta[name="${name}"]`)?.content ?? '';

// postMessage targetOrigin for talking to our embedder (see header comment).
const WORKSPACE = meta('xbin-workspace-origin');
const PARENT = WORKSPACE || '*';

const self = meta('xbin-component');
let frameToken = meta('xbin-frame-token');
// The tile deployment this document belongs to, when it isn't the primary:
// the server adds <meta name="xbin-deployment"> to those documents only.
// Token renewal needs nothing of it — the server copies the claim.
const deployment = meta('xbin-deployment');
// The partition of a partitioned tile this document's viewer reaches
// (docs/partitions.md): "user:<id>" — their own — or "global" (the root
// token, --no-auth). The server adds <meta name="xbin-partition"> to a
// partitioned tile's documents only; like the deployment, xbind decides it
// from the credential, and the frame token needs nothing of it.
const partition = meta('xbin-partition');
const inUserPartition = partition.startsWith('user:');

// Resolved http interface slots this component is bound to (docs/overview/11-interfaces.md):
// { <slot>: { url, service } }. xbin.iface(slot) returns the bound provider so a
// component calls a *typed, swappable* dependency instead of a hard-coded path.
let ifaces = {};
try { ifaces = JSON.parse(meta('xbin-interfaces') || '{}'); } catch { ifaces = {}; }
const iface = (slot) => ifaces[slot] || null;

// The sandbox this document runs in (absent on unsandboxed chrome). Without
// allow-popups a target="_blank" link or window.open is dropped by the
// browser with a console line that names the sandbox but not the fix — say
// which grant it is (cap:open-links, ND11), once, on the first such click.
const sandboxTokens = meta('xbin-sandbox');
if (sandboxTokens && !sandboxTokens.split(' ').includes('allow-popups')) {
  let warned = false;
  document.addEventListener('click', (e) => {
    if (warned) return;
    const a = e.composedPath?.()[0]?.closest?.('a[target="_blank"]'); // composedPath: shadow DOM retargets e.target
    if (!a) return;
    warned = true;
    console.warn(`[xbin] ${self}: links with target="_blank" (and window.open) are blocked by the tile sandbox — `
      + 'declare {"target":"cap:open-links","role":"writer"} in "uses" and have a workspace admin approve it (docs/auth.md).');
  }, true);
}

// --- token refresh (tokens are short-lived; see docs/auth.md) ---
// A token is bound to the sign-in that opened the page: a 401 here means
// that sign-in ended (signed out, device removed, expired) — say so once.
let loginEnded = false;
async function refreshToken() {
  try {
    const r = await fetch(`/api/xbin/frame-token?component=${encodeURIComponent(self)}`, {
      headers: frameToken ? { 'X-XBin-Frame-Token': frameToken } : {},
    });
    if (r.ok) frameToken = (await r.json()).token;
    else if (r.status === 401 && !loginEnded) {
      loginEnded = true;
      console.warn(`[xbin] ${self}: the sign-in this page was opened under has ended — reload to continue (docs/auth.md).`);
    }
  } catch { /* transient; next interval retries */ }
}
if (frameToken) setInterval(refreshToken, 10 * 60 * 1000);

// --- attributed fetch ---
// opts.partition: 'global' reaches the tile's global instance from a user
// partition's document (xbind attributes the call to the viewer there). It
// applies to this tile's own API only — /api/<self> and below, on this
// document's host — and any falsy value means the viewer's own partition. The
// option is always stripped, and it changes the request only in a user
// partition's document, so it never reaches the network otherwise — not on an
// older xbind either. Misuse (another value, another URL) rejects with a
// TypeError in every document, so it shows up before the tile is partitioned.
function bfetch(url, opts = {}) {
  const { partition: want, ...init } = opts;
  const isReq = url instanceof Request;
  // A Request's own headers stand unless opts.headers replaces them, as with
  // fetch itself (init.headers would otherwise drop them).
  const headers = new Headers(init.headers || (isReq ? url.headers : {}));
  if (frameToken) headers.set('X-XBin-Frame-Token', frameToken);
  if (want) {
    const target = isReq ? url.url : String(url);
    if (want !== 'global') return Promise.reject(new TypeError(`xbin.fetch: partition must be 'global', not ${JSON.stringify(want)}`));
    if (!ownApi(target)) return Promise.reject(new TypeError(`xbin.fetch: partition 'global' reaches this tile's own /api/${self}/… only, not ${target}`));
    if (inUserPartition) {
      if (isReq) return globalRequest(url).then((req) => fetch(req, { ...init, headers }));
      url = toGlobal(target);
    }
  }
  return fetch(url, { ...init, headers });
}
// Whether u, resolved against this document, is this tile's own API: on this
// document's host (or the workspace origin xbind names on a tile origin), at
// /api/<self> or below. location.origin is "null" in a sandboxed frame, so
// hosts are compared, as burl builds them.
function ownApi(u) {
  if (!self) return false;
  let abs;
  try { abs = new URL(u, location.href); } catch { return false; }
  const at = `${abs.protocol}//${abs.host}`;
  if (at !== `${location.protocol}//${location.host}` && at !== WORKSPACE) return false;
  const base = `/api/${self}`;
  return abs.pathname === base || abs.pathname.startsWith(`${base}/`);
}
// The same URL with xbin-partition=global added to its query.
function toGlobal(u) {
  const i = u.indexOf('#');
  const [head, hash] = i < 0 ? [u, ''] : [u.slice(0, i), u.slice(i)];
  return `${head}${head.includes('?') ? '&' : '?'}xbin-partition=global${hash}`;
}
// A Request's copy at toGlobal(its url). Its body is read out explicitly:
// passing the Request itself as the init would take the body from
// Request.prototype.body, which Firefox doesn't implement — the copy would go
// out empty. A clone is read, so the caller's Request stays unused.
async function globalRequest(req) {
  const body = /^(GET|HEAD)$/.test(req.method) ? undefined : await req.clone().arrayBuffer();
  return new Request(toGlobal(req.url), {
    method: req.method, headers: req.headers, body,
    ...(req.mode && req.mode !== 'navigate' ? { mode: req.mode } : {}),
    credentials: req.credentials, cache: req.cache, redirect: req.redirect,
    referrer: req.referrer, referrerPolicy: req.referrerPolicy, integrity: req.integrity,
    keepalive: req.keepalive, signal: req.signal,
  });
}

// --- attributed WebSocket (long-lived cross-element streams) ---
// The WebSocket origin: this document's own — or, in the xbin app, the
// injected xbin-ws-origin (a page the app loads from its custom scheme has
// no ws origin to derive; docs/protocol.md). Browsers never get the meta.
const wsOriginMeta = meta('xbin-ws-origin');
const wsOrigin = /^wss?:\/\/[^/?#]+$/.test(wsOriginMeta) ? wsOriginMeta
  : `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}`;

function bws(path) {
  const sep = path.includes('?') ? '&' : '?';
  return new WebSocket(`${wsOrigin}${path}${sep}frame=${encodeURIComponent(frameToken)}`);
}

// --- attributed URL (navigation downloads, <a href>, media src) ---
// Returns a same-host URL string carrying the CURRENT frame token as ?frame=
// — the only way a tag-driven request (which can't set headers) authenticates
// as this tile. Build it at click time, not page load: the module token
// refreshes every 10 minutes and a stale one 401s. Typical use is a
// backend-streamed download: <a href=... download> to an endpoint answering
// with Content-Disposition: attachment. xbind consumes ?frame= itself and
// never forwards it to the backend.
function burl(path) {
  const sep = path.includes('?') ? '&' : '?';
  return `${location.protocol}//${location.host}${path}${sep}frame=${encodeURIComponent(frameToken)}`;
}

// --- client-side file download ---
// Hands data to the browser as a named download (sandboxed tiles may trigger
// downloads — the sandbox carries allow-downloads, ND10). Call from a user
// gesture (a click handler): browsers throttle downloads with no activation.
// data: Blob | ArrayBuffer | TypedArray | string.
function download(filename, data, type = 'application/octet-stream') {
  const blob = data instanceof Blob ? data : new Blob([data], { type });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = filename || 'download';
  a.click();
  URL.revokeObjectURL(a.href);
}

// --- event stream (own WS, authenticated by frame token) ---
const eventHandlers = new Set();
let ws = null;
let wsBackoff = 500;

function ensureEvents() {
  if (ws) return;
  ws = new WebSocket(`${wsOrigin}/ws/events?frame=${encodeURIComponent(frameToken)}`);
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
  const r = await bfetch('/api/xbin/tile-report', {
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
const framed = window.parent !== window;
// In the xbin app a tile page is a top-level WebView, not a frame: the app
// registers an `xbin` WebKit message handler and relays this page's own
// xbin:dialog / xbin:window requests, answering with xbin:reply on this
// window (docs/protocol.md "Tile ↔ shell messaging") — so dialogs and
// windows go to it as they go to <bx-frame>.
const appBridge = !framed && !!window.webkit?.messageHandlers?.xbin;
const embedded = framed || appBridge;
if (framed) {
  let last = 0;
  const report = () => {
    const h = Math.ceil(document.documentElement.getBoundingClientRect().height);
    if (Math.abs(h - last) <= 1) return; // hysteresis: avoid resize loops
    last = h;
    window.parent.postMessage({ type: 'xbin:resize', component: self, height: h }, PARENT);
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
    if (WORKSPACE && e.origin !== WORKSPACE) return;      // …and, on a tile origin, the workspace
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
      window.parent.postMessage({ type: 'xbin:dialog', id, spec }, PARENT);
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
  window.parent.postMessage({ type: 'xbin:window', id, spec }, PARENT);
  return {
    id,
    close() { window.parent.postMessage({ type: 'xbin:window-close', id }, PARENT); },
    closed,
  };
}

// A right-click (or a touch long-press) on the tile's body opens the SHELL's
// tile menu — the iframe would otherwise swallow it. A tile that handles
// contextmenu itself (preventDefault) keeps its own menu; inputs, links and
// editable text keep the native one (paste lives there). Selected text rides
// along with a mouse right-click so the shell's menu can lead with Copy — a
// sandboxed (opaque-origin) frame has no navigator.clipboard of its own, the
// shell writes the clipboard on its behalf. On touch a live selection belongs
// to the platform's selection toolbar, not to our sheet.
if (framed) {
  const SEL_MAX = 65536; // 64 KiB: any prose selection, negligible to clone
  const native = (t) => !!(t instanceof Element && t.closest('input, textarea, select, a[href], [contenteditable]:not([contenteditable="false"])'));
  const selected = () => { const t = document.getSelection()?.toString() ?? ''; return t.trim() ? t.slice(0, SEL_MAX) : ''; };
  const send = (x, y, selection = '') => window.parent.postMessage({ type: 'xbin:contextmenu', x, y, selection }, PARENT);
  let lastType = 'mouse', sentAt = 0;
  document.addEventListener('contextmenu', (e) => {
    if (e.defaultPrevented || native(e.target)) return;
    const touch = (e.pointerType || lastType) !== 'mouse'; // keyboard (Shift+F10) counts as mouse
    const sel = selected();
    // Touch + a live selection: the platform toolbar owns it — unless this is
    // the contextmenu Android fires right after OUR long-press already sent.
    if (touch && sel && Date.now() - sentAt > 700) return;
    e.preventDefault();
    send(e.clientX, e.clientY, touch ? '' : sel);
  });
  let press = null;
  const cancel = () => { if (press) { clearTimeout(press.t); press = null; } };
  document.addEventListener('pointerdown', (e) => {
    lastType = e.pointerType || 'mouse';
    if (e.pointerType === 'mouse' || e.button !== 0 || native(e.target) || selected()) return;
    cancel();
    const x = e.clientX, y = e.clientY;
    press = { x, y, t: setTimeout(() => { press = null; sentAt = Date.now(); send(x, y); }, 450) };
  }, true);
  document.addEventListener('pointermove', (e) => { if (press && Math.hypot(e.clientX - press.x, e.clientY - press.y) > 8) cancel(); }, true);
  document.addEventListener('pointerup', cancel, true);
  document.addEventListener('pointercancel', cancel, true);
}

// --- strict tile asset gating, tokens mode (docs/elements.md §Asset URLs) ---
// The injection added <base href="/c/~<asset-token>/<tile>/<dir>/"> so this
// document's RELATIVE URLs carry a credential. Three side effects of a
// <base> are undone here, and failed loads are explained:
//   - a fragment-only link (href="#x") would resolve against the base and
//     navigate away — it scrolls, as it would without a <base>;
//   - a link resolving into the asset path (href="page2.html", "?q=1") would
//     ask the asset plane for a document, which it never serves — it
//     navigates to the same /c/ URL with this tile's frame token instead;
//   - history.pushState/replaceState resolve a relative URL against the
//     <base> — they resolve against the document URL, so the token never
//     enters location;
//   - a subresource that fails to load under /c/ gets a console line saying
//     why and how to fix it (relative URLs; `bx fix assets <tile>`).
const assetBase = meta('xbin-tile-assets') === 'tokens' ? document.querySelector('base[data-xbin-assets]') : null;
if (assetBase) {
  const tokPrefix = new URL(assetBase.href).pathname.match(/^\/c\/~[^/]+\//)?.[0] ?? '';
  const underToken = (u) => u.origin === location.origin && !!tokPrefix && u.pathname.startsWith(tokPrefix);
  const clean = (u) => `/c/${u.pathname.slice(tokPrefix.length)}${u.search}${u.hash}`;
  for (const m of ['pushState', 'replaceState']) {
    const orig = History.prototype[m];
    history[m] = function (state, title, url) {
      if (url !== undefined && url !== null) url = new URL(String(url), location.href).href;
      return orig.call(this, state, title, url);
    };
  }
  // Decided LAST: our window listener is added while the click is still at
  // the document, so it runs after every handler the tile registered — a
  // client-side router that handles the link (preventDefault) keeps it.
  const onLink = (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.composedPath?.().find((n) => n instanceof Element && n.matches('a[href], area[href]'));
    if (!a || a.hasAttribute('download')) return;
    const raw = a.getAttribute('href').trim();
    const target = (a.getAttribute('target') || '').toLowerCase();
    if (raw.startsWith('#')) {
      if (target && target !== '_self') return;
      e.preventDefault();
      if (location.hash === raw && raw.length > 1) document.getElementById(decodeURIComponent(raw.slice(1)))?.scrollIntoView();
      else location.hash = raw;
      return;
    }
    let u; try { u = new URL(a.href); } catch { return; }
    if (!underToken(u)) return;
    e.preventDefault();
    const dest = new URL(clean(u), location.href);
    if (frameToken) dest.searchParams.set('frame', frameToken);
    if (target && target !== '_self') window.open(dest.href, target, /\bnoopener\b/.test(a.rel) ? 'noopener' : '');
    else location.assign(dest.href);
  };
  document.addEventListener('click', (e) => {
    const late = (ev) => { window.removeEventListener('click', late); if (ev === e) onLink(ev); };
    window.addEventListener('click', late);
    setTimeout(() => window.removeEventListener('click', late)); // propagation stopped: never reached window
  });
  const warned = new Set();
  const explain = (url, what) => {
    let u; try { u = new URL(url, location.href); } catch { return; }
    if (u.origin !== location.origin || !u.pathname.startsWith('/c/') || warned.has(u.href)) return;
    warned.add(u.href);
    const why = underToken(u)
      ? 'the asset plane refused it (missing, a document, or a tile this user cannot read)'
      : 'an absolute /c/ URL carries no credential under strict tile asset gating — only relative URLs do';
    console.warn(`[xbin] ${self}: ${what} ${underToken(u) ? clean(u) : u.pathname} failed to load — ${why}. `
      + `Use a relative URL (\`bx fix assets ${self}\` rewrites them; /docs/elements.md#asset-urls).`);
  };
  addEventListener('error', (e) => {
    const el = e.target;
    if (el instanceof Element) explain(el.currentSrc || el.src || el.href?.baseVal || el.href || el.data || '', `<${el.localName}>`);
  }, true);
  // CSS url()s and @imports fire no error event: flag the ones that went out
  // absolute (they had no credential), from resource timing.
  try {
    new PerformanceObserver((list) => {
      for (const r of list.getEntries()) {
        let u; try { u = new URL(r.name); } catch { continue; }
        if (['css', 'link', 'img', 'script', 'other'].includes(r.initiatorType) && !underToken(u)) explain(r.name, r.initiatorType);
      }
    }).observe({ type: 'resource', buffered: true });
  } catch { /* no resource timing */ }
}

// --- focused-scroll tint (D123) ---
// A document that links /vendor/theme.css gets its thin themed scrollbars from
// the sheet; the tint on the scroller the next wheel or key would move needs
// the tracker, loaded here for those documents only (linking the theme is the
// opt-in; <meta name="xbin-scroll-focus" content="off"> opts out).
if (document.querySelector('link[rel~="stylesheet"][href*="/vendor/theme.css"]') && meta('xbin-scroll-focus') !== 'off') {
  import('/vendor/bx-scroll.js').catch(() => { /* cosmetic */ });
}

// xbin.native — the xbin app's small API for a tile's native UI, present only
// in the app's runtime document (<meta name="xbin-native">): the app injects
// {caps, state} as window.xbin.native before this script runs and
// /vendor/xb-native.js adds the methods (docs/frontend-kit.md).
const nativeApi = (window.xbin && typeof window.xbin.native === 'object' && window.xbin.native) || (meta('xbin-native') ? {} : null);

window.xbin = Object.freeze({ self, iface, fetch: bfetch, ws: bws, url: burl, download, bus, events, dialog, window: openWindow, status, clearStatus, notify, ...(deployment ? { deployment } : {}), ...(partition ? { partition } : {}), ...(nativeApi ? { native: nativeApi } : {}) });
