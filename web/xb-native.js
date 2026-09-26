/**
 * xb-native.js — the template layer of a tile's native UI in the xbin app.
 *
 * A tile opts in by shipping `native.js` beside its xbin.json; the app runs
 * it in a hidden WebKit document (the tile's own sandbox, frame token and
 * `window.xbin`) and draws what it renders with platform UI. The API is
 * lit-shaped over the native vocabulary:
 *
 *   import { html, render, repeat, nothing } from '/vendor/xb-native.js';
 *   render(html`
 *     <screen title="Counter" style="form">
 *       <section>
 *         <row title="Count" detail=${count}/>
 *         <button role="primary" @tap=${inc}>+1</button>
 *       </section>
 *     </screen>`);
 *
 * render() coalesces per frame, diffs against what the app shows and posts
 * patches — an unchanged re-render costs nothing. Events arrive as
 * {type, value, …payload}. `xbin.native` (caps, supports, meta, copy, share,
 * open, state, saveState) is the small app API; it is also exported here as
 * `native`. The vocabulary is /vendor/xb/vocab.js; the wire contract the app
 * implements is native/spec/tree.md in the xbin repository.
 *
 * Transports: the app's WKScriptMessageHandler `xbn` (messages posted as
 * JSON strings); otherwise a preview host that called attach(post) or set
 * globalThis.xbnHost = {post}; otherwise messages queue until attach().
 * The app talks back through globalThis.xbn = {event, visibility, resolve,
 * frame}. createRuntime({post}) makes an independent runtime (node, tests);
 * with {global: true} it becomes the one render() and globalThis.xbn use.
 *
 * FROZEN once shipped: the exports, the vocabulary and the wire format change
 * additively only (docs/compat.md).
 */
import { html, repeat, nothing } from '/vendor/xb/rt-template.js';
import { createRuntime as makeRuntime } from '/vendor/xb/rt-runtime.js';
import { VOCAB } from '/vendor/xb/vocab.js';
import { applyOps } from '/vendor/xb/rt-diff.js';

export { html, repeat, nothing, VOCAB, applyOps };

const G = globalThis;
let current = null;
let hostPost = null;
const outbox = [];

function transport(msg) {
  const wk = G.webkit?.messageHandlers?.xbn;
  if (wk) { wk.postMessage(JSON.stringify(msg)); return; }
  const p = hostPost || (typeof G.xbnHost?.post === 'function' ? G.xbnHost.post : null);
  if (p) p(msg);
  else if (outbox.length < 10000) outbox.push(msg);
}

// attach(post): a preview host (the reference renderer, bx preview) takes the
// default runtime's messages; queued ones are replayed first.
export function attach(post) {
  hostPost = post;
  for (const m of outbox.splice(0)) post(m);
}

// The app injects `window.xbin = {native: {caps, state}}` at document start;
// xbin-client.js carries `native` over into the frozen xbin object.
const injected = () => (G.xbin && typeof G.xbin.native === 'object' ? G.xbin.native : null);

function current$() {
  if (!current) {
    const inj = injected();
    install(makeRuntime({ post: transport, caps: inj?.caps, state: inj?.state,
      document: typeof G.document === 'object' ? G.document : null }));
    listenForErrors();
  }
  return current;
}

// install(rt): rt becomes the runtime render() and globalThis.xbn talk to, and
// its API becomes xbin.native (merged into the app-injected object).
function install(rt) {
  current = rt;
  const inj = injected();
  if (inj && Object.isExtensible(inj)) {
    try { Object.defineProperties(inj, Object.getOwnPropertyDescriptors(rt.native)); } catch { /* frozen by someone */ }
  } else if (G.xbin && typeof G.xbin === 'object' && Object.isExtensible(G.xbin) && !G.xbin.native) {
    G.xbin.native = rt.native;
  }
}

export function createRuntime(opts = {}) {
  const rt = makeRuntime(opts);
  if (opts.global) install(rt);
  return rt;
}

export const render = (value) => current$().render(value);

// native — xbin.native, also for documents whose xbin object could not take it.
export const native = {
  get caps() { return current$().native.caps; },
  get state() { return current$().native.state; },
  supports: (name, rev) => current$().native.supports(name, rev),
  meta: (m) => current$().native.meta(m),
  copy: (text) => current$().native.copy(text),
  share: (o) => current$().native.share(o),
  open: (url) => current$().native.open(url),
  saveState: (obj) => current$().native.saveState(obj),
};

// globalThis.xbn — how the app calls in (callAsyncJavaScript):
//   xbn.event(k, type, payload, n?)  a user action on node k
//   xbn.visibility('visible'|'hidden')
//   xbn.resolve(id, value, error?)   answers a {op:"call"}
//   xbn.frame()                      the renderer's frame clock: flush a pending render
G.xbn = {
  event: (k, type, payload, n) => current$().xbn.event(k, type, payload, n),
  visibility: (s) => current$().xbn.visibility(s),
  resolve: (id, v, err) => current$().xbn.resolve(id, v, err),
  frame: () => current$().xbn.frame(),
};

let listening = false;
function listenForErrors() {
  if (listening || typeof G.addEventListener !== 'function') return;
  listening = true;
  G.addEventListener('error', (e) => current?.uncaught(e.error ?? e.message, e.filename ? `${e.filename}:${e.lineno}` : ''));
  G.addEventListener('unhandledrejection', (e) => current?.uncaught(e.reason, 'unhandled rejection'));
}

// boot(entry): import the tile's native module, reporting a load failure as
// {op:"error", kind:"module"} (the app then shows the web page). `entry`
// resolves against the document, e.g. boot('./native.js').
export async function boot(entry) {
  const rt = current$();
  const base = G.document?.baseURI ?? G.location?.href;
  try {
    await import(base ? new URL(entry, base).href : entry);
  } catch (e) {
    rt.moduleError(e, String(entry));
  }
}
