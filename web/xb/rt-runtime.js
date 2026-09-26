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
 *   log            false: no console lines for diagnostics and errors (they
 *                  are messages either way)
 */
import { VOCAB, fullCaps } from '/vendor/xb/vocab.js';
import { buildTree } from '/vendor/xb/rt-build.js';
import { diff, cloneJSON } from '/vendor/xb/rt-diff.js';
import { MarkdownCache } from '/vendor/xb/rt-markdown.js';

const own = (o, k) => o != null && Object.prototype.hasOwnProperty.call(o, k);
export const TREE_V = 1;
const STATE_MAX = 64 << 10;

// Which props each primitive's events report (controlled state), from the vocabulary.
const REPORTED = {};
for (const [name, p] of Object.entries(VOCAB.prims)) {
  const set = new Set();
  for (const ev of Object.values(p.events)) if (ev.reports) set.add(ev.reports.prop);
  REPORTED[name] = set;
}

export function normalizeCaps(c) {
  if (!c || typeof c !== 'object' || !c.prims) return fullCaps();
  return { v: Number(c.v) || VOCAB.v, renderer: String(c.renderer ?? ''), app: c.app ?? null,
    prims: { ...c.prims }, features: Array.isArray(c.features) ? [...c.features] : [] };
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
  const log = opts.log !== false && typeof console !== 'undefined';
  let state = opts.state === undefined ? null : cloneJSON(opts.state);

  const md = new MarkdownCache();
  let tree = null; // the shadow: what the app shows (reported controlled values included)
  let nodes = new Map(); // k -> shadow node
  let handlers = new Map(); // k -> {type: fn}
  let pending = false;
  let scheduled = false;
  let value;
  let n = 0; // tree messages sent (mount/patch carry it)
  const sentAt = new Map(); // `${k}\0${prop}` -> n of the patch that last set a controlled prop
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
  function diag(level, code, message, where = '') {
    if (!once(`d\0${code}\0${message}\0${where}`)) return;
    const d = { op: 'diag', level, code, message, where };
    if (diagnostics.length < 1000) diagnostics.push(d);
    if (log) {
      const line = `[xb-native] ${message}${where ? ` (${where})` : ''}`;
      if (level === 'error') console.error(line); else if (level === 'warn') console.warn(line); else console.info(line);
    }
    send(d);
  }
  function unsupported(message, where = '') {
    if (!once(`u\0${message}\0${where}`)) return;
    send({ op: 'error', kind: 'unsupported', message, where });
  }
  function fail(kind, e, where = '') {
    const message = String(e?.message ?? e);
    if (log) console.error(`[xb-native] ${kind}: ${message}${where ? ` (${where})` : ''}`, e);
    const m = { op: 'error', kind, message, where };
    if (e?.stack) m.stack = String(e.stack);
    send(m);
  }
  const env = { caps, diag, unsupported, md };

  function render(v) {
    value = v;
    pending = true;
    if (!scheduled) { scheduled = true; schedule(flush); }
  }

  // flush(): render what is pending now; returns the tree message sent, or null.
  function flush() {
    scheduled = false;
    if (!pending) return null;
    pending = false;
    const v = value;
    value = undefined;
    let built;
    md.begin();
    try { built = buildTree(v, env); } catch (e) { fail('exception', e); return null; }
    md.end();
    handlers = built.handlers;
    const root = built.root;
    let msg;
    if (!tree || tree.k !== root.k || tree.t !== root.t) {
      msg = { op: 'mount', v: TREE_V, n: ++n, root };
    } else {
      const ops = diff(tree, root);
      if (!ops.length) { adopt(root); return null; }
      msg = { op: 'patch', n: ++n, ops };
      for (const op of ops) {
        if (op[0] !== 'set') continue;
        const t = nodes.get(op[1])?.t; // a set targets a node kept from the old tree
        for (const prop of Object.keys(op[2])) if (REPORTED[t]?.has(prop)) sentAt.set(`${op[1]}\0${prop}`, n);
      }
    }
    adopt(root);
    const out = cloneJSON(msg); // the shadow keeps changing; the message must not
    send(out);
    return out;
  }
  function adopt(root) {
    tree = root;
    nodes = new Map();
    const walk = (x) => { nodes.set(x.k, x); for (const c of x.c || []) walk(c); };
    walk(root);
    for (const key of sentAt.keys()) if (!nodes.has(key.slice(0, key.indexOf('\0')))) sentAt.delete(key);
  }

  // xbn.event(k, type, payload, n?) — the app reports a user action. A payload
  // value for a controlled prop updates the shadow first (so an unchanged
  // re-render sends nothing back), unless the app says (n) it produced the
  // event before it applied the patch that last set that prop.
  function event(k, type, payload, seenN) {
    const pl = payload && typeof payload === 'object' ? payload : {};
    const node = nodes.get(k);
    if (node) {
      const rep = VOCAB.prims[node.t]?.events?.[type]?.reports;
      if (rep && own(node.p, rep.prop)) {
        const v = own(rep, 'value') ? rep.value : pl[rep.from || rep.prop];
        const stale = typeof seenN === 'number' && (sentAt.get(`${k}\0${rep.prop}`) ?? 0) > seenN;
        if (v !== undefined && !stale) node.p[rep.prop] = cloneJSON(v);
      }
    }
    const h = handlers.get(k)?.[type];
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

  const native = {
    caps,
    get state() { return state; },
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

  // remount(): the app lost its copy (a recreated view, a patch it could not
  // apply) — send the whole tree again as a mount.
  function remount() {
    flush();
    if (!tree) return false;
    const out = cloneJSON({ op: 'mount', v: TREE_V, n: ++n, root: tree });
    send(out);
    return true;
  }

  const xbn = {
    event,
    visibility: setVisibility,
    resolve,
    frame: () => flush() !== null,
    remount,
  };

  return {
    render, flush, xbn, native, diagnostics,
    get tree() { return tree ? { v: TREE_V, root: cloneJSON(tree) } : null; },
    get pending() { return pending; },
    get visibility() { return visibility; },
    get markdownStats() { return { ...md.stats }; },
    // uncaught(e, where): report an error thrown outside a render or handler.
    uncaught: (e, where) => fail('uncaught', e, where),
    moduleError: (e, where) => fail('module', e, where),
  };
}
