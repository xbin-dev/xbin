// shell/prompts.js — which tile each waiting decision belongs to (D195):
// the shell draws a grant request, an interface to bind, a partition
// consent ask and a limit ask or cap hit on the window of the tile that
// asked, and flags that tile — and the folders holding it — in the
// sidebar. Pure, so hack/prompts.test.mjs and hack/limits.test.mjs run it
// under node.
//
//   grant request        → its `from` (the tile that wants to call)
//   interface to bind    → its `component` (the tile with the unbound slot)
//   partition consent    → its `from` (the tile whose call was refused)
//   limit ask / cap hit  → its `tile` (D203: the tile asking for more, or at its cap)
//
// A tile that isn't open on the screen shows nothing but its flag.

// promptsByTile({grants, bindings, consents, limits}, hidden) → Map(path →
// {grants, bindings, consents, limits?, caps?, n}): what each tile has
// waiting that the person hasn't dismissed. grants, bindings and limits
// are the /grants, /bindings and /limits?pending=1 answers (null while
// unread); consents the asks still to answer; hidden(kind, item) says
// whether the person dismissed one. A limit item counts when the person
// may decide it or the tile is theirs; `caps` is how many of them are cap
// hits (the rest are asks).
export function promptsByTile({ grants, bindings, consents, limits } = {}, hidden = () => false) {
  const m = new Map();
  const add = (path, kind) => {
    if (typeof path !== 'string' || !path) return;
    const e = m.get(path) ?? { grants: 0, bindings: 0, consents: 0, n: 0 };
    e[kind] = (e[kind] || 0) + 1; e.n += 1;
    m.set(path, e);
  };
  for (const p of grants?.pending ?? []) if (!hidden('grants', p)) add(p.from, 'grants');
  // only the slots this person may wire: a tile they merely own is listed for information
  for (const p of bindings?.pending ?? []) if (p.approvable !== false && !hidden('bindings', p)) add(p.component, 'bindings');
  for (const a of consents ?? []) add(a.from, 'consents');
  for (const p of limits?.pending ?? []) {
    if (!(p?.approvable || p?.mine) || hidden('limits', p)) continue;
    add(p.tile, 'limits');
    if (p.why === 'cap' && m.has(p.tile)) m.get(p.tile).caps = (m.get(p.tile).caps || 0) + 1;
  }
  return m;
}

// flaggedIn(prompts, paths) → how many of these tiles have something waiting
// (a folder's count: its own tiles and its child folders', given flat).
export const flaggedIn = (prompts, paths) => (paths ?? []).reduce((n, p) => n + (prompts?.get(p)?.n ? 1 : 0), 0);

// promptWords(e) → the flag's and the badge's title: "2 grant requests,
// 1 interface to bind, 1 cap hit waiting for your decision".
export function promptWords(e) {
  if (!e?.n) return '';
  const part = (n, one, many) => (n ? `${n} ${n === 1 ? one : many}` : '');
  const what = [part(e.grants, 'grant request', 'grant requests'), part(e.bindings, 'interface to bind', 'interfaces to bind'),
    part(e.consents, 'consent ask', 'consent asks'), part((e.limits || 0) - (e.caps || 0), 'limit request', 'limit requests'),
    part(e.caps, 'cap hit', 'cap hits')].filter(Boolean).join(', ');
  return `${what} waiting for your decision`;
}
