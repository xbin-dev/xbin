// web/term-sessions.js — the browser's view of the terminal session
// directory (D73). Which live sessions a user has on a tile is the SERVER's
// knowledge (GET /api/xbin/term/sessions, docs/protocol.md), so every
// browser the user signs into shows the same tabs and a user switch on one
// browser shows only that user's; a tab's name lives on the session; the
// window's own state (open, active tab, geometry) is a per-user pref.
// Nothing that identifies a session is kept in the browser any more — the
// one legacy record (`localStorage['bx-term:<tile>']`, D66 and before) is
// read once, adopted, and removed.
//
// Imports nothing and takes its fetch/storage as arguments, so
// hack/term-sessions.test.mjs runs it under node (`make js-test`).

export const legacyKey = (cwd) => `bx-term:${cwd}`;
// the pref key is one path segment of /api/xbin/prefs/<key>: no slashes
export const prefKey = (cwd) => `term:${cwd.replaceAll('/', ':')}`;
export const uid = () => Math.random().toString(36).slice(2, 9);

const JSON_HDR = { 'Content-Type': 'application/json' };

// makeStore({fetch, storage}) → the calls <bx-frame> makes.
export function makeStore({ fetch: f = globalThis.fetch, storage = globalThis.localStorage } = {}) {
  const json = async (url, init) => {
    const r = await f(url, init);
    if (!r.ok) throw new Error(`${r.status}`);
    const t = await r.text();
    return t ? JSON.parse(t) : null;
  };
  return {
    // the caller's live sessions on this tile, oldest first ([] on any failure)
    list: (cwd) => json(`/api/xbin/term/sessions?cwd=${encodeURIComponent(cwd)}`).then((l) => (Array.isArray(l) ? l : [])).catch(() => []),
    rename: (id, name) => f(`/api/xbin/term/sessions/${encodeURIComponent(id)}`, { method: 'PATCH', headers: JSON_HDR, body: JSON.stringify({ name }) }).catch(() => {}),
    // the window: {open, active, pop} per user (null = never saved)
    loadWindow: (cwd) => json(`/api/xbin/prefs/${encodeURIComponent(prefKey(cwd))}`).then((w) => (w && typeof w === 'object' ? w : null)).catch(() => null),
    saveWindow: (cwd, w) => f(`/api/xbin/prefs/${encodeURIComponent(prefKey(cwd))}`, w ? { method: 'PUT', headers: JSON_HDR, body: JSON.stringify(w) } : { method: 'DELETE' }).catch(() => {}),
    // the legacy browser record, read once and removed: its window state and
    // the tab names it held by session id (the caller adopts the names of
    // the ids the server still lists as the caller's)
    migrateLegacy(cwd) {
      let raw = null;
      try { raw = storage?.getItem(legacyKey(cwd)); if (raw) storage.removeItem(legacyKey(cwd)); } catch { /* storage off */ }
      if (!raw) return null;
      let v;
      try { v = JSON.parse(raw); } catch { return null; }
      const names = {};
      for (const s of v?.sessions ?? []) if (s?.id && s.name) names[s.id] = String(s.name);
      const window = v && ('open' in v || v.pop) ? { open: !!v.open, active: v.active | 0, pop: v.pop && 'dx' in v.pop ? v.pop : null } : null;
      return { window, names };
    },
  };
}

// tabsFrom(server, local) → the tab list after a listing: the server's
// rows in its order, each keeping the local tab's `key` (lit's repeat must
// not remount a live terminal), what the session frame told the local tab
// (baseOutdated) and — this matters — the pickers the local tab ASKED for
// (net/gpu/api): the server reports the effective, clamped values, and a
// changed picker attribute restarts a live terminal (bx-terminal), so a
// clamp must not read as a new request. A tab first seen here takes the
// server's values (it reattaches by id; the pickers are display then).
// Then the local tabs the server does not know yet — the ones still
// spawning (id null). A server row with no local tab is absorbed into the
// first spawning tab, if any: an "open" event can reach this browser before
// the socket that spawned it gets its session frame, and a fresh tab beside
// a spawning one would double it (_gotSession dedupes the rare
// mis-absorption when two browsers spawn at once).
export function tabsFrom(server, local) {
  const byId = new Map(local.filter((t) => t.id).map((t) => [t.id, t]));
  const pending = local.filter((t) => !t.id);
  // Absorb a server row with no id match into a pending tab of the SAME kind
  // first (an "open" event can arrive before the socket/element that spawned
  // it fires bx-session): otherwise a fresh shell tab beside a spawning agent
  // could swap kinds. Falls back to the first pending of any kind.
  const takePending = (kind) => {
    const i = pending.findIndex((t) => (t.kind || 'shell') === kind);
    return i >= 0 ? pending.splice(i, 1)[0] : pending.shift();
  };
  const tabs = [];
  for (const s of server) {
    const kind = s.kind || 'shell';
    const was = byId.get(s.id) ?? takePending(kind);
    tabs.push({
      key: was?.key ?? uid(), id: s.id, kind,
      provider: s.provider || was?.provider || '', status: s.status || was?.status || '',
      net: was ? (was.net ?? null) : (s.net || null), gpu: was ? (was.gpu || 'none') : (s.gpu || 'none'), api: was ? was.api !== false : s.api !== false,
      name: s.name || '', scopes: s.scopes ?? was?.scopes ?? null, label: s.label || '',
      baseOutdated: !!was?.baseOutdated,
    });
  }
  return [...tabs, ...pending];
}

// clampActive(i, n): the active index inside the tab list.
export const clampActive = (i, n) => (n ? Math.min(Math.max(0, i | 0), n - 1) : 0);
