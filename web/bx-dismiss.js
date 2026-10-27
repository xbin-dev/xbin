/**
 * bx-dismiss.js — a person's dismissed grant requests and "interfaces to
 * bind" rows (D188), and the shell's warning banners: the shell's decision
 * strip (<bx-grants>, <bx-bindings>), its banners and its organisations
 * badge leave them out. Nothing
 * changes for the request, the tile or another admin: the dismissal is the
 * person's own, kept in their prefs bucket (the shell's, from the
 * workspace's root page) under one key, so it follows them to every
 * device, and a `prefs` event about it tells their other open pages:
 *
 *   GET|PUT /api/xbin/prefs/dismissed
 *   {"grants":   {"<from>|<target>|<role>": "<dismissed at>"},
 *    "bindings": {"<component>|<slot>|<kind>": "<dismissed at>"},
 *    "alerts":   {"<kind>|<message>": "<dismissed at>"},
 *    "limits":   {"<tile>|<kind>|<ask>" or "<tile>|<kind>|cap:<since>": "<dismissed at>"}}
 *
 * A request whose key changes (another role, another target) is another
 * request and shows again. Dismissals of what the server no longer lists
 * (approved, bound, withdrawn) are pruned (prune), as the partition
 * consent prompts' are (keepDismissed, D163). The admin console and the
 * organisations tile list everything, whatever was dismissed.
 *
 * Every reader on a page shares one value and one save chain (dismissals(),
 * #11): changes are stored one at a time, so two in flight never undo each
 * other, and a dismissal is pruned only once two fresh reads of the
 * server's list in a row no longer name its item (seen()).
 *
 * Imports nothing: hack/bx-dismiss.test.mjs runs it under node. The fetch
 * a caller passes decides the bucket (window.xbin.fetch in the shell).
 */

export const PREF = 'dismissed';
// limits (D203): a tile's pending limit ask or its at-cap hit. An older
// shell imports this file too: it reads only the kinds it knows, and the
// restore-all and count over KINDS take the new one in.
export const KINDS = Object.freeze(['grants', 'bindings', 'alerts', 'limits']);
const API = `/api/xbin/prefs/${PREF}`;

// The keys: what identifies a request, so a changed one shows again.
export const grantKey = (p) => `${p?.from ?? ''}|${p?.target ?? ''}|${p?.role ?? ''}`;
export const bindingKey = (p) => `${p?.component ?? ''}|${p?.slot ?? ''}|${p?.kind ?? ''}`;
// A banner's key is its kind and its words: one whose message changes (more
// people, another tile) is news and shows again.
export const alertKey = (a) => `${a?.kind ?? ''}|${a?.message ?? ''}`;
// A limit prompt (GET /limits?pending=1, D203) is its tile, its kind and
// what it asks: a manifest's ask by its value (a bigger ask shows again), a
// cap hit by when it began (`since`; `at` for a hit without one) — a new hit
// after a raise or an approver's dismissal is news.
const hitStart = (h) => (typeof h?.since === 'string' && h.since && !h.since.startsWith('0001-') ? h.since : h?.at ?? '');
export const limitKey = (p) => `${p?.tile ?? p?.scope ?? ''}|${p?.kind ?? ''}|${p?.why === 'cap' ? `cap:${hitStart(p.hit)}` : (p?.ask ?? '')}`;

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

// pendingCount(grants, bindings, requests, d, limits?) → the organisations
// badge: the actionable pending items — grant requests this person may
// approve (D26/D33) or their own tiles' still waiting (direction "mine"),
// unbound slots they may wire, people's access requests they can grant
// (D36), limit asks and cap hits they may decide or whose tile is theirs
// (D203) — less what they dismissed. The arguments are GET /grants,
// /bindings, /access-requests and /limits?pending=1 (null when one failed;
// an older shell passes four).
export function pendingCount(g, b, q, d, l) {
  let n = 0;
  const scoped = !!g?.scope;
  for (const p of g?.pending ?? []) {
    if (p.blocked || isDismissed(d, 'grants', grantKey(p))) continue;
    if (!scoped || p.approvable || p.direction === 'mine') n += 1;
  }
  n += (b?.pending ?? []).filter((p) => p.approvable !== false && !isDismissed(d, 'bindings', bindingKey(p))).length;
  n += (q?.requests ?? []).filter((x) => x.manage).length;
  n += (l?.pending ?? []).filter((p) => (p.approvable || p.mine) && !isDismissed(d, 'limits', limitKey(p))).length;
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

// ---- this page's one store (#11) ----
// Every reader of the dismissals on a page — the decision prompts, the
// banners, the organisations badge, the sidebar's flags — shares one value,
// and every change goes through one chain: read, change, write, one at a
// time. Two changes in flight (Dismiss on two rows in quick succession, a
// banner and a grant) used to each read the stored value before the other's
// write, and the second write undid the first. A change shows at once; the
// stored answer replaces it with the changes still queued applied on top. A
// write that fails is tried again after each of retry's waits, then given
// up: the change is undone here (the stored value, the rest of the queue on
// top) and onError's callbacks hear of it — never a row hidden here and
// stored nowhere.
export const RETRY_MS = Object.freeze([500, 2000, 5000]);

export function dismissStore(fetchFn, { retry = RETRY_MS, sleep = (ms) => new Promise((r) => setTimeout(r, ms)) } = {}) {
  let value = null, stored = null, chain = Promise.resolve();
  const queued = [];                    // changes not stored yet, oldest first
  const subs = new Set(), errs = new Set();
  const missing = new Map();            // kind → keys the last fresh read didn't list (seen)
  const emit = () => { for (const f of subs) f(value); };
  const settle = (d) => {
    stored = normDismissed(d);
    value = normDismissed(queued.reduce((v, f) => f(v), stored));
    emit();
  };
  const store = {
    get value() { return value; },
    // subscribe(f): f(value) now (once read) and on every change; → unsubscribe
    subscribe(f) { subs.add(f); if (value) f(value); return () => subs.delete(f); },
    // onError(f): f(error) when a change couldn't be stored; → unsubscribe
    onError(f) { errs.add(f); return () => errs.delete(f); },
    // load(): read what is stored (on start, and when a `prefs` event says
    // another page changed it); the changes still queued stay applied.
    async load() {
      const d = await loadDismissed(fetchFn);
      if (d) settle(d);
      return value;
    },
    // change(fn) → the stored value after fn, or null when it couldn't be stored.
    change(fn) {
      queued.push(fn);
      if (value) { value = normDismissed(fn(value)); emit(); }
      const run = chain.then(async () => {
        let d = null;
        for (let i = 0; ; i++) {
          d = await updateDismissed(fetchFn, fn);
          if (d || i >= retry.length) break;
          await sleep(retry[i]);
        }
        queued.splice(queued.indexOf(fn), 1);
        if (d) { settle(d); return d; }
        const cur = await loadDismissed(fetchFn); // undo it here: what is stored, the rest of the queue on top
        if (cur || stored) settle(cur ?? stored);
        for (const f of errs) f(new Error('your dismissal could not be saved'));
        return null;
      });
      chain = run.catch(() => null);
      return run;
    },
    // seen(kind, live): a fresh, successful read of what the server lists for
    // kind (live: their keys). A dismissal whose item two such reads in a row
    // didn't list goes (approved, bound, withdrawn); one missing from a single
    // read stays — a tile briefly gone (a half-written xbin.json) or a list
    // read before another page's dismissal reached this one keeps it.
    seen(kind, live) {
      const keep = new Set(live), was = missing.get(kind) ?? new Set();
      const now = new Set(Object.keys(value?.[kind] ?? {}).filter((k) => !keep.has(k)));
      missing.set(kind, now);
      const gone = [...now].filter((k) => was.has(k));
      if (gone.length) store.change((d) => restore(d, kind, gone));
      return gone;
    },
    // waiting(kind) → how many dismissals one fresh read didn't list: the
    // next read prunes them if it doesn't either (the reader reads again soon).
    waiting(kind) { return missing.get(kind)?.size ?? 0; },
  };
  return store;
}

// dismissals(fetchFn): this page's store — made by the first caller, whose
// fetch decides the bucket (every caller on a page passes the same one).
let shared = null;
export const dismissals = (fetchFn) => (shared ??= dismissStore(fetchFn));
