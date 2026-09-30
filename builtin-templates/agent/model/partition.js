// model/partition.js — which of the agent's three layouts the page shows
// (API.md "Partitioned instances"): `xbin.partition`, which xbind sets only in
// a partitioned tile's documents, decides.
//
//   legacy  no xbin.partition: an unpartitioned instance — today's page,
//           unchanged;
//   user    "user:<id>": the viewer's own partition — their conversations,
//           with no sharing (a conversation here is theirs alone), and a
//           banner when a bound sandbox manager can't keep people apart;
//   global  "global": the global instance (the owner token, --no-auth) — its
//           conversations, with a note to sign in as a person for private
//           ones.
//
// Pure functions of xbin.partition and the data the backend sends; both
// views read them. No lit, no DOM.

/** state: 'legacy' | 'user' | 'global' for a partition key (default: this document's). */
export function partitionState(p = globalThis.xbin?.partition) {
  if (typeof p !== 'string' || p === '') return 'legacy';
  if (p === 'global') return 'global';
  if (p.startsWith('user:') && p.length > 5) return 'user';
  return 'legacy';
}

/** sharing: may a conversation be shared from this page (its menu, top bar and the Shared view)? */
export const sharing = (state = partitionState()) => state !== 'user';

/** pausesHidden: do this page's live streams close while it is hidden? Only a
 * partitioned instance's: a background tab mustn't keep a person's partition
 * running (an unpartitioned instance's streams stay as they always were). */
export const pausesHidden = (state = partitionState()) => state !== 'legacy';

/** MCP_GLOBAL_ONLY: what a partitioned instance says of a static MCP server
 * with `headers` — they can carry tokens, so the settings people's
 * partitions read (conf) never carry it (API.md "Partitioned instances"). */
export const MCP_GLOBAL_ONLY = 'works in shared (global) conversations only — to use it in your own conversations, bind it as a tile or a personal bind';

/**
 * staticMcp: the config's static MCP servers (GET /config's `mcp`, as a
 * manager reads it: headers and all) as a partitioned instance's settings
 * list them, [{name, url, globalOnly}]; [] unpartitioned, whose settings list
 * none (the page is today's there).
 */
export function staticMcp(cfg, state = partitionState()) {
  if (state === 'legacy') return [];
  return ((cfg && cfg.mcp) || []).filter(Boolean).map((s) => ({
    name: String(s.name || ''), url: String(s.url || ''),
    globalOnly: !!(s.headers && typeof s.headers === 'object' && Object.keys(s.headers).length),
  }));
}

/** mcpNote: one line naming the global-only servers of staticMcp's list ('' when none). */
export function mcpNote(list) {
  const names = (list || []).filter((s) => s.globalOnly).map((s) => s.name || s.url);
  return names.length ? `${names.join(', ')}: ${MCP_GLOBAL_ONLY}.` : '';
}

// oldManagers: the bound sandbox managers a person's partition doesn't use —
// hello refused with refusal `partitions` (GET /sandboxes' managers).
export function oldManagers(managers) {
  return (managers || []).filter((m) => m && m.ok === false && m.refusal === 'partitions');
}

/**
 * appNotices: notices() for app (model/app.js), where you are. In a person's
 * partition with sandbox managers bound it reads GET /sandboxes once (the
 * managers' hello says which are too old), so the banner shows before the
 * sandbox picker is ever opened.
 */
export function appNotices(app, state = partitionState()) {
  if (state === 'user' && app.sbx && !app.sbx.list.loaded) {
    const slot = globalThis.xbin?.iface ? globalThis.xbin.iface('sandboxes') : null;
    if (slot && (slot.endpoints || []).length) app.sbx.ensure();
  }
  return notices(state, app.sbx && app.sbx.list);
}

/**
 * notices: what the page says above everything, as [{kind, text, title?}].
 * @param state   partitionState()
 * @param list    GET /sandboxes as model/sandboxes.js listOf keeps it (managers), or null
 */
export function notices(state, list) {
  if (state === 'global') {
    return [{ kind: 'global', text: 'This is the agent’s shared instance: sign in as a person for your private conversations.' }];
  }
  if (state !== 'user') return [];
  const old = oldManagers(list && list.managers);
  if (!old.length) return [];
  const names = old.map((m) => m.provider).join(', ');
  const which = old.length === 1 ? 'isn’t' : 'aren’t';
  return [{
    kind: 'sandbox',
    text: `Sandboxes from ${names} ${which} available in your conversations: ${old.length === 1 ? 'it can’t' : 'they can’t'} keep each person’s sandboxes apart yet. ` +
      `Update ${old.length === 1 ? 'it' : 'them'} (the Tile Manager’s Updates, or bx template updates), or ask a workspace admin to.`,
    title: old.map((m) => `${m.provider}: ${m.error || 'no "partitions" capability'}`).join('\n'),
  }];
}
