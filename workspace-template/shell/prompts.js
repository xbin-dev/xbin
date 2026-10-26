// shell/prompts.js — which tile each waiting decision belongs to (D195):
// the shell draws a grant request, an interface to bind and a partition
// consent ask on the window of the tile that asked, and flags that tile —
// and the folders holding it — in the sidebar. Pure, so
// hack/prompts.test.mjs runs it under node.
//
//   grant request        → its `from` (the tile that wants to call)
//   interface to bind    → its `component` (the tile with the unbound slot)
//   partition consent    → its `from` (the tile whose call was refused)
//
// A tile that isn't open on the screen shows nothing but its flag.

// promptsByTile({grants, bindings, consents}, hidden) → Map(path →
// {grants, bindings, consents, n}): what each tile has waiting that the
// person hasn't dismissed. grants and bindings are the /grants and
// /bindings answers (null while unread); consents the asks still to
// answer; hidden(kind, item) says whether the person dismissed one.
export function promptsByTile({ grants, bindings, consents } = {}, hidden = () => false) {
  const m = new Map();
  const add = (path, kind) => {
    if (typeof path !== 'string' || !path) return;
    const e = m.get(path) ?? { grants: 0, bindings: 0, consents: 0, n: 0 };
    e[kind] += 1; e.n += 1;
    m.set(path, e);
  };
  for (const p of grants?.pending ?? []) if (!hidden('grants', p)) add(p.from, 'grants');
  // only the slots this person may wire: a tile they merely own is listed for information
  for (const p of bindings?.pending ?? []) if (p.approvable !== false && !hidden('bindings', p)) add(p.component, 'bindings');
  for (const a of consents ?? []) add(a.from, 'consents');
  return m;
}

// flaggedIn(prompts, paths) → how many of these tiles have something waiting
// (a folder's count: its own tiles and its child folders', given flat).
export const flaggedIn = (prompts, paths) => (paths ?? []).reduce((n, p) => n + (prompts?.get(p)?.n ? 1 : 0), 0);

// promptWords(e) → the flag's and the badge's title: "2 grant requests,
// 1 interface to bind waiting for your decision".
export function promptWords(e) {
  if (!e?.n) return '';
  const part = (n, one, many) => (n ? `${n} ${n === 1 ? one : many}` : '');
  const what = [part(e.grants, 'grant request', 'grant requests'), part(e.bindings, 'interface to bind', 'interfaces to bind'),
    part(e.consents, 'consent ask', 'consent asks')].filter(Boolean).join(', ');
  return `${what} waiting for your decision`;
}
