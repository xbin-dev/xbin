/**
 * bx-dismiss.js — a person's dismissed grant requests and "interfaces to
 * bind" rows (D188): the shell's decision strip (<bx-grants>,
 * <bx-bindings>) and its organisations badge leave them out. Nothing
 * changes for the request, the tile or another admin: the dismissal is the
 * person's own, kept in their prefs bucket (the shell's, from the
 * workspace's root page) under one key, so it follows them to every
 * device, and a `prefs` event about it tells their other open pages:
 *
 *   GET|PUT /api/xbin/prefs/dismissed
 *   {"grants":   {"<from>|<target>|<role>": "<dismissed at>"},
 *    "bindings": {"<component>|<slot>|<kind>": "<dismissed at>"}}
 *
 * A request whose key changes (another role, another target) is another
 * request and shows again. Dismissals of what the server no longer lists
 * (approved, bound, withdrawn) are pruned (prune), as the partition
 * consent prompts' are (keepDismissed, D163). The admin console and the
 * organisations tile list everything, whatever was dismissed.
 *
 * Imports nothing: hack/bx-dismiss.test.mjs runs it under node. The fetch
 * a caller passes decides the bucket (window.xbin.fetch in the shell).
 */

export const PREF = 'dismissed';
export const KINDS = Object.freeze(['grants', 'bindings']);
const API = `/api/xbin/prefs/${PREF}`;

// The keys: what identifies a request, so a changed one shows again.
export const grantKey = (p) => `${p?.from ?? ''}|${p?.target ?? ''}|${p?.role ?? ''}`;
export const bindingKey = (p) => `${p?.component ?? ''}|${p?.slot ?? ''}|${p?.kind ?? ''}`;

const plain = (v) => !!v && typeof v === 'object' && !Array.isArray(v);

// normDismissed(v) → {grants:{key: at}, bindings:{key: at}}: a stored value
// as the code reads it — anything else (absent, an older shape, junk) is
// nothing dismissed. Other top-level keys are kept, for a later kind.
export function normDismissed(v) {
  const out = plain(v) ? { ...v } : {};
  for (const k of KINDS) {
    const m = {};
    if (plain(v?.[k])) for (const [key, at] of Object.entries(v[k])) if (typeof at === 'string' && key) m[key] = at;
    out[k] = m;
  }
  return out;
}

export const isDismissed = (d, kind, key) => !!d?.[kind] && Object.hasOwn(d[kind], key);

// dismiss(d, kind, key, at) → d with key dismissed.
export function dismiss(d, kind, key, at = new Date().toISOString()) {
  const n = normDismissed(d);
  return { ...n, [kind]: { ...n[kind], [key]: at } };
}

// restore(d, kind, keys) → d with those keys of kind (all of them when
// keys is absent) shown again.
export function restore(d, kind, keys) {
  const n = normDismissed(d);
  if (!keys) return { ...n, [kind]: {} };
  const m = { ...n[kind] };
  for (const k of keys) delete m[k];
  return { ...n, [kind]: m };
}

// prune(d, kind, live) → the dismissals of kind that still name an item
// the server lists (live: its keys); the same object when none goes.
export function prune(d, kind, live) {
  const keep = new Set(live);
  let out = d;
  for (const k of Object.keys(d?.[kind] ?? {})) {
    if (keep.has(k)) continue;
    if (out === d) out = { ...d, [kind]: { ...d[kind] } };
    delete out[kind][k];
  }
  return out;
}

// split(items, d, kind, keyOf) → {shown, hidden}: the items the strip
// shows and the dismissed ones.
export function split(items, d, kind, keyOf) {
  const shown = [], hidden = [];
  for (const it of items ?? []) (isDismissed(d, kind, keyOf(it)) ? hidden : shown).push(it);
  return { shown, hidden };
}

// pendingCount(grants, bindings, requests, d) → the organisations badge:
// the actionable pending items — grant requests this person may approve
// (D26/D33) or their own tiles' still waiting (direction "mine"), unbound
// slots they may wire, people's access requests they can grant (D36) —
// less what they dismissed. The arguments are GET /grants, /bindings and
// /access-requests (null when one failed).
export function pendingCount(g, b, q, d) {
  let n = 0;
  const scoped = !!g?.scope;
  for (const p of g?.pending ?? []) {
    if (p.blocked || isDismissed(d, 'grants', grantKey(p))) continue;
    if (!scoped || p.approvable || p.direction === 'mine') n += 1;
  }
  n += (b?.pending ?? []).filter((p) => p.approvable !== false && !isDismissed(d, 'bindings', bindingKey(p))).length;
  n += (q?.requests ?? []).filter((x) => x.manage).length;
  return n;
}

// dismissedEvent(e): a /ws/events frame saying a `dismissed` pref changed.
export const dismissedEvent = (e) => e?.type === 'prefs' && e.data?.key === PREF;

// loadDismissed(fetchFn) → the person's dismissals; null when they can't
// be read now (offline, xbind restarting): show everything, prune nothing.
export async function loadDismissed(fetchFn) {
  try {
    const r = await fetchFn(API);
    if (r.status === 404) return normDismissed(null);
    if (!r.ok) return null;
    return normDismissed(await r.json());
  } catch { return null; }
}

// updateDismissed(fetchFn, fn) → the stored value after fn(current): read,
// change, write — so the strip's two elements, each writing its own kind,
// don't undo each other. null when it couldn't be saved.
export async function updateDismissed(fetchFn, fn) {
  const cur = await loadDismissed(fetchFn);
  if (!cur) return null;
  const next = normDismissed(fn(cur));
  const empty = KINDS.every((k) => Object.keys(next[k]).length === 0) && Object.keys(next).length === KINDS.length;
  try {
    const r = await (empty ? fetchFn(API, { method: 'DELETE' })
      : fetchFn(API, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(next) }));
    return r.ok || (empty && r.status === 404) ? next : null;
  } catch { return null; }
}
