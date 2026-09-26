/**
 * xb/rt-diff.js — the tree diff of the native runtime and the reference
 * patch application (native/spec/tree.md "Patches").
 *
 * A node is {k, t, p?, e?, c?}. diff(a, b) returns the ops that turn tree a
 * into tree b (same root key and type); applyOps(root, ops) applies them.
 * Ops apply in order; indexes refer to the parent's children at the time the
 * op applies:
 *   ["set",    k, {prop: value, …}]   merge props
 *   ["unset",  k, [prop, …]]          delete props
 *   ["events", k, [type, …]]          replace the listened-to events
 *   ["insert", parentK, index, node]  node (a whole subtree) lands at index
 *   ["remove", k]                     the node and its subtree go
 *   ["move",   k, parentK, index]     a child moves within its parent to index
 * A child whose type changed under the same key is removed and re-inserted.
 * Reordering emits the fewest moves (the children kept in place are a longest
 * increasing subsequence).
 */

export function deepEqual(a, b) {
  if (a === b) return true;
  if (typeof a !== 'object' || typeof b !== 'object' || a === null || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!deepEqual(a[i], b[i])) return false;
    return true;
  }
  if (Array.isArray(b)) return false;
  const ka = Object.keys(a), kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) if (!Object.prototype.hasOwnProperty.call(b, k) || !deepEqual(a[k], b[k])) return false;
  return true;
}

// cloneJSON(v): a JSON-safe deep copy (what the bridge would carry): functions,
// symbols and undefined drop out, non-finite numbers become null, Dates ISO
// strings, other objects their own enumerable fields.
export function cloneJSON(v) {
  switch (typeof v) {
    case 'string': case 'boolean': return v;
    case 'number': return Number.isFinite(v) ? v : null;
    case 'bigint': return String(v);
    case 'object': {
      if (v === null) return null;
      if (Array.isArray(v)) return v.map((x) => { const c = cloneJSON(x); return c === undefined ? null : c; });
      if (typeof v.toJSON === 'function') return cloneJSON(v.toJSON());
      const o = {};
      for (const k of Object.keys(v)) { const c = cloneJSON(v[k]); if (c !== undefined) o[k] = c; }
      return o;
    }
    default: return undefined;
  }
}

const sameList = (a, b) => {
  a = a || []; b = b || [];
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
};

export function diff(a, b, ops = []) {
  diffNode(a, b, ops);
  return ops;
}

function diffNode(a, b, ops) {
  const k = b.k;
  const ap = a.p || {}, bp = b.p || {};
  let set = null, unset = null;
  for (const name of Object.keys(bp)) {
    if (!Object.prototype.hasOwnProperty.call(ap, name) || !deepEqual(ap[name], bp[name])) (set ||= {})[name] = bp[name];
  }
  for (const name of Object.keys(ap)) if (!Object.prototype.hasOwnProperty.call(bp, name)) (unset ||= []).push(name);
  if (set) ops.push(['set', k, set]);
  if (unset) ops.push(['unset', k, unset]);
  if (!sameList(a.e, b.e)) ops.push(['events', k, b.e ? [...b.e] : []]);
  diffChildren(k, a.c || [], b.c || [], ops);
}

function diffChildren(pk, oc, nc, ops) {
  // fast path: same keys, same order
  if (oc.length === nc.length) {
    let same = true;
    for (let i = 0; i < oc.length; i++) if (oc[i].k !== nc[i].k || oc[i].t !== nc[i].t) { same = false; break; }
    if (same) { for (let i = 0; i < oc.length; i++) diffNode(oc[i], nc[i], ops); return; }
  }
  const newByKey = new Map();
  for (const n of nc) newByKey.set(n.k, n);
  // 1. remove what went (or changed type)
  const cur = [];
  for (const o of oc) {
    const n = newByKey.get(o.k);
    if (!n || n.t !== o.t) ops.push(['remove', o.k]);
    else cur.push(o);
  }
  // 2. the kept children that stay in place: a longest increasing subsequence
  // of their positions in the new order
  const newIndex = new Map();
  nc.forEach((n, i) => newIndex.set(n.k, i));
  const stay = lisKeys(cur.map((o) => newIndex.get(o.k)), cur);
  const oldByKey = new Map();
  for (const o of cur) oldByKey.set(o.k, o);
  // 3. right to left: insert the new, move the rest in front of the placed anchor
  const keys = cur.map((o) => o.k);
  for (let i = nc.length - 1; i >= 0; i--) {
    const n = nc[i];
    const anchor = i + 1 < nc.length ? nc[i + 1].k : null;
    if (!oldByKey.has(n.k)) {
      const at = anchor === null ? keys.length : keys.indexOf(anchor);
      keys.splice(at, 0, n.k);
      ops.push(['insert', pk, at, n]);
    } else if (!stay.has(n.k)) {
      keys.splice(keys.indexOf(n.k), 1);
      const at = anchor === null ? keys.length : keys.indexOf(anchor);
      keys.splice(at, 0, n.k);
      ops.push(['move', n.k, pk, at]);
    }
  }
  // 4. recurse into the kept ones
  for (const n of nc) { const o = oldByKey.get(n.k); if (o) diffNode(o, n, ops); }
}

// lisKeys(seq, items): the keys of items whose seq values form a longest
// strictly increasing subsequence (O(n log n)).
function lisKeys(seq, items) {
  const tails = [], prev = new Array(seq.length);
  for (let i = 0; i < seq.length; i++) {
    let lo = 0, hi = tails.length;
    while (lo < hi) { const m = (lo + hi) >> 1; if (seq[tails[m]] < seq[i]) lo = m + 1; else hi = m; }
    prev[i] = lo > 0 ? tails[lo - 1] : -1;
    tails[lo] = i;
  }
  const out = new Set();
  for (let i = tails.length ? tails[tails.length - 1] : -1; i >= 0; i = prev[i]) out.add(items[i].k);
  return out;
}

// applyOps(root, ops) — the reference patch application (what XbinCore and
// the reference renderer implement). Mutates and returns root; nodes in
// insert ops are copied, never shared with the message.
export function applyOps(root, ops) {
  const index = new Map(); // k -> {node, parent}
  const walk = (n, parent) => { index.set(n.k, { node: n, parent }); for (const c of n.c || []) walk(c, n); };
  const unwalk = (n) => { index.delete(n.k); for (const c of n.c || []) unwalk(c); };
  walk(root, null);
  const get = (k, op) => {
    const e = index.get(k);
    if (!e) throw new Error(`${op[0]}: no node ${JSON.stringify(k)}`);
    return e;
  };
  for (const op of ops) {
    switch (op[0]) {
      case 'set': { const { node } = get(op[1], op); node.p = Object.assign(node.p || {}, cloneJSON(op[2])); break; }
      case 'unset': {
        const { node } = get(op[1], op);
        for (const name of op[2]) if (node.p) delete node.p[name];
        if (node.p && Object.keys(node.p).length === 0) delete node.p;
        break;
      }
      case 'events': { const { node } = get(op[1], op); if (op[2].length) node.e = [...op[2]]; else delete node.e; break; }
      case 'insert': {
        const { node: parent } = get(op[1], op);
        const n = cloneJSON(op[3]);
        (parent.c ||= []).splice(op[2], 0, n);
        walk(n, parent);
        break;
      }
      case 'remove': {
        const { node, parent } = get(op[1], op);
        if (!parent) throw new Error('remove: cannot remove the root');
        parent.c.splice(parent.c.indexOf(node), 1);
        unwalk(node);
        break;
      }
      case 'move': {
        const { node, parent } = get(op[1], op);
        if (!parent || parent.k !== op[2]) throw new Error(`move: ${op[1]} is not a child of ${op[2]}`);
        parent.c.splice(parent.c.indexOf(node), 1);
        parent.c.splice(op[3], 0, node);
        break;
      }
      default: throw new Error(`unknown op ${op[0]}`);
    }
  }
  return root;
}

// normalize(node): absent p/e/c and empty ones are the same thing on the
// wire; this drops the empty ones so two trees compare with deepEqual.
export function normalize(n) {
  const o = { k: n.k, t: n.t };
  if (n.p && Object.keys(n.p).length) o.p = n.p;
  if (n.e && n.e.length) o.e = n.e;
  if (n.c && n.c.length) o.c = n.c.map(normalize);
  return o;
}
