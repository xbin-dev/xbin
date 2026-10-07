/**
 * xb/rt-runtime.js — one native runtime: render scheduling, the shadow of
 * the tree the app shows, diffs to patches, event dispatch (with controlled
 * props), the xbin.native API and the messages to the app. The wire
 * contract is native/spec/tree.md; /vendor/xb-native.js wires a runtime to
 * the document (the app's message handler or a preview host).
 *
 * createRuntime({post, caps, state, schedule, document}):
 *   post(msg)      receives every runtime → app message (a fresh JSON-safe object)
 *   caps           what the app supports (default: the full vocabulary)
 *   state          the blob the app kept for this tile (xbin.native.state)
 *   schedule(fn)   run fn at the next frame (default: requestAnimationFrame,
 *                  with a 50 ms timer as a backstop; setTimeout 0 without rAF);
 *                  xbn.frame() flushes a pending render at once
 *   document       the document whose visibilityState xbn.visibility drives
 *   window         the window whose location.hash xbn.navigate sets (and
 *                  that hears its `hashchange`; default: none — no deep links)
 *   log            false: no console lines for diagnostics and errors (they
 *                  are messages either way)
 *
 * Two trees (targets): the main one — render(), the tile's screen — and,
 * when the app's caps list the feature "widget", the widget — widget(), a
 * card on the app's screens, drawn from a small vocabulary (VOCAB.widget).
 * Each has its own shadow, handlers, keys and message counter `n`; widget
 * messages carry target:"widget" (tree.md §13). Without the feature,
 * widget() does nothing and nothing about the main tree changes.
 */
import { VOCAB, fullCaps } from '/vendor/xb/vocab.js';
import { buildTree } from '/vendor/xb/rt-build.js';
import { diff, cloneJSON } from '/vendor/xb/rt-diff.js';
import { MarkdownCache } from '/vendor/xb/rt-markdown.js';

const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);
export const TREE_V = 1;
const STATE_MAX = 64 << 10;
// The horizontal size classes a screen comes in (xbin.native.width).
const WIDTHS = ['compact', 'regular'];

// Which props each primitive's events report (controlled state), from the vocabulary.
const REPORTED = {};
for (const [name, p] of Object.entries(VOCAB.prims)) {
  const set = new Set();
  for (const ev of Object.values(p.events)) if (ev.reports) set.add(ev.reports.prop);
  REPORTED[name] = set;
}

export function normalizeCaps(c) {
  if (!c || typeof c !== 'object' || !c.prims) return fullCaps();
  const out = { v: Number(c.v) || VOCAB.v, renderer: String(c.renderer ?? ''), app: c.app ?? null,
    prims: { ...c.prims }, features: Array.isArray(c.features) ? [...c.features] : [] };
  if (VOCAB.widget.sizes.includes(c.widgetSize)) out.widgetSize = c.widgetSize;
  if (WIDTHS.includes(c.width)) out.width = c.width;
  return out;
}

function defaultSchedule(fn) {
  let done = false;
  const once = () => { if (!done) { done = true; fn(); } };
  if (typeof requestAnimationFrame === 'function') { requestAnimationFrame(once); setTimeout(once, 50); }
  else setTimeout(once, 0);
}

export function createRuntime(opts = {}) {
  const post = typeof opts.post === 'function' ? opts.post : () => {};
  const caps = normalizeCaps(opts.caps);
  const schedule = typeof opts.schedule === 'function' ? opts.schedule : defaultSchedule;
  const doc = opts.document || null;
  const win = opts.window || null;
  const log = opts.log !== false && typeof console !== 'undefined';
  let state = opts.state === undefined ? null : cloneJSON(opts.state);

  const md = new MarkdownCache();
  // One per tree: the main one (render) and the widget (widget(), only when
  // the app's caps list the "widget" feature).
  const target = (name) => ({
    name, // null (main) | 'widget'
    tree: null, // the shadow: what the app shows (reported controlled values included)
    nodes: new Map(), // k -> shadow node
    handlers: new Map(), // k -> {type: fn}
    pending: false,
    value: undefined,
    n: 0, // tree messages sent for this tree (mount/patch carry it)
    sentAt: new Map(), // `${k}\0${prop}` -> n of the patch that last set a controlled prop
  });
  const main = target(null);
  const wdg = target('widget');
  const widgetOn = caps.features.includes(VOCAB.widget.feature);
  const targetOf = (t) => (t === undefined || t === null || t === '' ? main : t === 'widget' && widgetOn ? wdg : null);
  let widgetSize = caps.widgetSize ?? VOCAB.widget.sizes[0];
  const sizeListeners = new Set();
  let width = caps.width ?? null; // null: the app doesn't say (an older app, a preview)
  const widthListeners = new Set();
  let building = null; // the target a flush is building: its diag/error messages name it
  let scheduled = false;
  const seen = new Set();
  const diagnostics = [];
  const calls = new Map();
  let callSeq = 0;
  let visibility = 'visible';

  const send = (m) => {
    try { post(m); } catch (e) { if (typeof console !== 'undefined') console.error('[xb-native] post failed', e); }
  };
  const once = (key) => {
    if (seen.has(key)) return false;
    if (seen.size >= 10000) seen.clear(); // a tile that invents new invalid values forever
    seen.add(key);
    return true;
  };
  const tagged = (m) => { if (building?.name) m.target = building.name; return m; };
  function diag(level, code, message, where = '') {
    if (!once(`d\0${building?.name ?? ''}\0${code}\0${message}\0${where}`)) return;
    const d = tagged({ op: 'diag', level, code, message, where });
    if (diagnostics.length < 1000) diagnostics.push(d);
    if (log) {
      const line = `[xb-native] ${message}${where ? ` (${where})` : ''}`;
      if (level === 'error') console.error(line); else if (level === 'warn') console.warn(line); else console.info(line);
    }
    send(d);
  }
  function unsupported(message, where = '') {
    if (!once(`u\0${building?.name ?? ''}\0${message}\0${where}`)) return;
    send(tagged({ op: 'error', kind: 'unsupported', message, where }));
  }
  function fail(kind, e, where = '') {
    const message = String(e?.message ?? e);
    if (log) console.error(`[xb-native] ${kind}: ${message}${where ? ` (${where})` : ''}`, e);
    const m = tagged({ op: 'error', kind, message, where });
    if (e?.stack) m.stack = String(e.stack);
    send(m);
  }
  const env = { caps, diag, unsupported, md };
  // the widget's own markdown cache: a build evicts what it did not touch
  const widgetEnv = { ...env, md: new MarkdownCache(), allow: VOCAB.widget.prims };

  function renderTo(T, v) {
    T.value = v;
    T.pending = true;
    if (!scheduled) { scheduled = true; schedule(flush); }
  }
  const render = (v) => renderTo(main, v);
  // widget(v): the widget tree — dropped (silently) unless the app shows widgets.
  function widget(v) { if (widgetOn) renderTo(wdg, v); }

  // flush(): render what is pending now (the main tree, then the widget);
  // returns the tree message sent (the main tree's first), or null.
  function flush() {
    scheduled = false;
    const a = flushTarget(main);
    const b = widgetOn ? flushTarget(wdg) : null;
    return a ?? b;
  }
  function flushTarget(T) {
    if (!T.pending) return null;
    T.pending = false;
    const v = T.value;
    T.value = undefined;
    let built;
    const e$ = T === wdg ? widgetEnv : env;
    building = T;
    try {
      e$.md.begin();
      try { built = buildTree(v, e$); } catch (e) { fail('exception', e); return null; }
      e$.md.end();
    } finally { building = null; }
    T.handlers = built.handlers;
    const root = built.root;
    const tgt = T.name ? { target: T.name } : {};
    let msg;
    if (!T.tree || T.tree.k !== root.k || T.tree.t !== root.t) {
      msg = { op: 'mount', ...tgt, v: TREE_V, n: ++T.n, root };
    } else {
      const ops = diff(T.tree, root);
      if (!ops.length) { adopt(T, root); return null; }
      msg = { op: 'patch', ...tgt, n: ++T.n, ops };
      for (const op of ops) {
        if (op[0] !== 'set') continue;
        const t = T.nodes.get(op[1])?.t; // a set targets a node kept from the old tree
        for (const prop of Object.keys(op[2])) if (REPORTED[t]?.has(prop)) T.sentAt.set(`${op[1]}\0${prop}`, T.n);
      }
    }
    adopt(T, root);
    const out = cloneJSON(msg); // the shadow keeps changing; the message must not
    send(out);
    return out;
  }
  function adopt(T, root) {
    T.tree = root;
    T.nodes = new Map();
    const walk = (x) => { T.nodes.set(x.k, x); for (const c of x.c || []) walk(c); };
    walk(root);
    for (const key of T.sentAt.keys()) if (!T.nodes.has(key.slice(0, key.indexOf('\0')))) T.sentAt.delete(key);
  }

  // xbn.event(k, type, payload, n?, target?) — the app reports a user action
  // on node k of the main tree, or (target "widget") of the widget. A payload
  // value for a controlled prop updates the shadow first (so an unchanged
  // re-render sends nothing back), unless the app says (n) it produced the
  // event before it applied the patch that last set that prop.
  function event(k, type, payload, seenN, targetName) {
    const T = targetOf(targetName);
    if (!T) return false;
    const pl = payload && typeof payload === 'object' ? payload : {};
    const node = T.nodes.get(k);
    if (node) {
      const rep = VOCAB.prims[node.t]?.events?.[type]?.reports;
      if (rep && own(node.p, rep.prop)) {
        const v = own(rep, 'value') ? rep.value : pl[rep.from || rep.prop];
        const stale = typeof seenN === 'number' && (T.sentAt.get(`${k}\0${rep.prop}`) ?? 0) > seenN;
        if (v !== undefined && !stale) node.p[rep.prop] = cloneJSON(v);
      }
    }
    const h = T.handlers.get(k)?.[type];
    if (typeof h !== 'function') return false;
    const ev = { type, value: pl.value, ...pl };
    try {
      const r = h(ev);
      if (r && typeof r.then === 'function') r.then(null, (e) => fail('uncaught', e, `@${type} on ${k}`));
    } catch (e) { fail('uncaught', e, `@${type} on ${k}`); }
    return true;
  }

  function setVisibility(s) {
    visibility = s === 'hidden' ? 'hidden' : 'visible';
    if (!doc) return;
    try {
      Object.defineProperty(doc, 'visibilityState', { configurable: true, get: () => visibility });
      Object.defineProperty(doc, 'hidden', { configurable: true, get: () => visibility === 'hidden' });
    } catch { /* a document that refuses the override keeps its own state */ }
    try { if (typeof doc.dispatchEvent === 'function' && typeof Event === 'function') doc.dispatchEvent(new Event('visibilitychange')); } catch { /* no events here */ }
  }

  // xbn.navigate(hash) — the app opens a deep link into the native view (its
  // URL's fragment, `#c=42`): location.hash becomes `hash` and `hashchange`
  // fires, also when it is the hash already (the user asked to go there
  // again). The first fragment needs no call: the app loads the runtime
  // document with it, so a tile reads location.hash as it starts.
  function navigate(hash) {
    const loc = win?.location;
    if (!loc) return false;
    let h = String(hash ?? '');
    if (h && !h.startsWith('#')) h = `#${h}`;
    if (h === '#') h = '';
    const oldURL = String(loc.href);
    const newURL = oldURL.split('#')[0] + h;
    const hist = win.history;
    if (hist && typeof hist.replaceState === 'function') {
      try { hist.replaceState(hist.state ?? null, '', newURL); } catch {
        // a document that refuses the rewrite: the browser fires hashchange
        // itself when the hash changes
        if ((loc.hash || '') !== h) { try { loc.hash = h; } catch { return false; } return true; }
      }
    } else {
      try { loc.hash = h; } catch { return false; } // no history (node): a plain URL
    }
    try {
      let ev;
      if (typeof HashChangeEvent === 'function') ev = new HashChangeEvent('hashchange', { oldURL, newURL });
      else { ev = new Event('hashchange'); Object.defineProperties(ev, { oldURL: { value: oldURL }, newURL: { value: newURL } }); }
      if (typeof win.dispatchEvent === 'function') win.dispatchEvent(ev);
    } catch (e) { fail('uncaught', e, 'hashchange'); }
    return true;
  }

  function resolve(id, v, error) {
    const c = calls.get(id);
    if (!c) return false;
    calls.delete(id);
    if (error != null && error !== false) c.reject(new Error(String(error)));
    else c.resolve(v);
    return true;
  }

  function call(what, args) {
    const id = `c${++callSeq}`;
    return new Promise((res, rej) => {
      calls.set(id, { resolve: res, reject: rej });
      send({ op: 'call', id, what, args });
    });
  }

  // xbn.widgetSize(size) — the app shows the widget at another size class.
  function setWidgetSize(size) {
    if (!widgetOn || !VOCAB.widget.sizes.includes(size) || size === widgetSize) return false;
    widgetSize = size;
    for (const fn of [...sizeListeners]) {
      try {
        const r = fn(size);
        if (r && typeof r.then === 'function') r.then(null, (e) => fail('uncaught', e, 'widgetsize listener'));
      } catch (e) { fail('uncaught', e, 'widgetsize listener'); }
    }
    return true;
  }

  // xbn.width(w) — the screen the tile is drawn on changed width class.
  function setWidth(w) {
    if (!WIDTHS.includes(w) || w === width) return false;
    width = w;
    for (const fn of [...widthListeners]) {
      try {
        const r = fn(w);
        if (r && typeof r.then === 'function') r.then(null, (e) => fail('uncaught', e, 'width listener'));
      } catch (e) { fail('uncaught', e, 'width listener'); }
    }
    return true;
  }

  const native = {
    caps,
    get state() { return state; },
    // the size class the app shows the widget at: "small" (one column) | "wide" (two)
    get widgetSize() { return widgetSize; },
    // the screen's horizontal size class: "compact" (a phone) | "regular"
    // (an iPad) — null when the app doesn't say
    get width() { return width; },
    // on('widgetsize' | 'width', fn(value)) → an unsubscribe function
    on(type, fn) {
      const set = type === 'widgetsize' ? sizeListeners : type === 'width' ? widthListeners : null;
      if (!set) throw new Error(`xbin.native.on: unknown event ${JSON.stringify(type)} (widgetsize, width)`);
      if (typeof fn !== 'function') throw new Error('xbin.native.on: the listener must be a function');
      set.add(fn);
      return () => { set.delete(fn); };
    },
    supports(name, rev = 1) {
      if (own(caps.prims, name)) return caps.prims[name] >= rev;
      return caps.features.includes(name);
    },
    meta(m = {}) {
      const o = { op: 'meta' };
      for (const f of ['title', 'icon', 'badge']) if (m[f] !== undefined) o[f] = m[f] === null ? null : String(m[f]);
      send(o);
    },
    copy: (text) => call('copy', { text: String(text ?? '') }),
    share(o = {}) {
      const args = {};
      for (const f of ['text', 'url', 'file']) if (o[f] != null) args[f] = String(o[f]);
      return call('share', args);
    },
    open(url) {
      url = String(url ?? '');
      if (!/^https:\/\//i.test(url)) return Promise.reject(new Error('xbin.native.open: https URLs only'));
      return call('open', { url });
    },
    saveState(obj) {
      const c = cloneJSON(obj ?? null);
      if ((JSON.stringify(c) ?? '').length > STATE_MAX) throw new Error('xbin.native.saveState: the state is over 64 KiB');
      state = c;
      send({ op: 'state', state: c });
    },
  };

  // remount(target?): the app lost its copy of a tree (a recreated view, a
  // patch it could not apply) — send it again as a mount.
  function remount(targetName) {
    const T = targetOf(targetName);
    if (!T) return false;
    flush();
    if (!T.tree) return false;
    const out = cloneJSON({ op: 'mount', ...(T.name ? { target: T.name } : {}), v: TREE_V, n: ++T.n, root: T.tree });
    send(out);
    return true;
  }

  const xbn = {
    event,
    visibility: setVisibility,
    resolve,
    frame: () => flush() !== null,
    remount,
    widgetSize: setWidgetSize,
    width: setWidth,
    navigate,
  };

  return {
    render, widget, flush, xbn, native, diagnostics,
    get tree() { return main.tree ? { v: TREE_V, root: cloneJSON(main.tree) } : null; },
    get widgetTree() { return wdg.tree ? { v: TREE_V, root: cloneJSON(wdg.tree) } : null; },
    get pending() { return main.pending || wdg.pending; },
    get visibility() { return visibility; },
    get markdownStats() { return { ...md.stats }; },
    // uncaught(e, where): report an error thrown outside a render or handler.
    uncaught: (e, where) => fail('uncaught', e, where),
    moduleError: (e, where) => fail('module', e, where),
  };
}
