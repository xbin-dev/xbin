// ws/dock.js — the workspace agent's page and xbin's chrome around it: the
// whole of what they say to each other (API.md §Workspace mode → "The
// page and the chrome"; D232).
//
// The page renders what a model wrote, so xbin treats it as untrusted: it
// never grants anything. It shows that something waits and asks the chrome
// — the dock that frames it (`<bx-agent-dock>`, or `/xbin/agent`) — to open
// it, by id. The chrome reads the id and nothing else, fetches the thing
// from xbind itself and draws the grant in a region this frame can't draw
// in. So every message out carries ids only:
//
//   page → chrome
//     xbin:agent-open-request   {id}               a request (a tile call, a
//                                                   tile agent's start, a new
//                                                   tile) xbind holds for the
//                                                   person's approval — its
//                                                   id is xbind's (`ref`)
//     xbin:agent-open-changeset {id}               an admin change set
//     xbin:open-session         {session, tile}    show a tile agent's
//                                                   session in its tile's
//                                                   window (D229; through
//                                                   xbin.openSession when the
//                                                   client has it)
//   chrome → page
//     xbin:agent-context        {tile} | {screen}  what the dock was opened
//                                                   about: a tile's path, or
//                                                   a screen — offered as
//                                                   the composer's context
//                                                   chip
//
// A message in is taken only from the window that frames this page, read
// strictly (a path, a short name), and decides nothing: a context chip is
// an offer the person can remove. Pure but for `post` and `listen`.

/** The message names, in one place. */
export const MSG = Object.freeze({
  openRequest: 'xbin:agent-open-request',
  openChangeset: 'xbin:agent-open-changeset',
  openSession: 'xbin:open-session',
  context: 'xbin:agent-context',
});

// what an id may look like (the chrome reads the same shapes)
const ID = /^[A-Za-z0-9_.:-]{1,80}$/;
const SESSION = /^[A-Za-z0-9_-]{1,64}$/;

/** tilePath(v): a tile's path as the chrome sends it, or '' when it isn't one. */
export function tilePath(v) {
  const t = typeof v === 'string' ? v.replace(/^\/+|\/+$/g, '') : '';
  return t && t.length <= 512 && !t.split('/').some((x) => !x || x === '.' || x === '..') ? t : '';
}

/** openRequest(id) / openChangeset(id) / openSession(session, tile): the message, or null when the ids aren't ids. */
export const openRequest = (id) => (typeof id === 'string' && ID.test(id) ? { type: MSG.openRequest, id } : null);
export const openChangeset = (id) => (typeof id === 'string' && ID.test(id) ? { type: MSG.openChangeset, id } : null);
export function openSession(session, tile) {
  const t = tilePath(tile);
  return typeof session === 'string' && SESSION.test(session) && t ? { type: MSG.openSession, session, tile: t } : null;
}

/**
 * contextOf(data): a context message from the chrome, read strictly —
 * {tile: path} or {screen: name} — or null. A cleared context ({} or
 * {tile: ''}) is {none: true}: the dock opened about nothing.
 */
export function contextOf(data) {
  if (!data || typeof data !== 'object' || data.type !== MSG.context) return null;
  const tile = tilePath(data.tile);
  if (tile) return { tile };
  if (typeof data.screen === 'string' && data.screen.trim() && data.screen.length <= 200) return { screen: data.screen.trim() };
  return { none: true };
}

/** post(msg, win): send a message to the chrome (nothing when msg is null or nothing frames the page). */
export function post(msg, win = globalThis.window) {
  if (!msg || !win || !win.parent) return false;
  // a tile frame's origin is opaque: identity is the window, never the origin (xbin-client.js)
  const origin = win.document?.querySelector('meta[name="xbin-workspace-origin"]')?.content || '*';
  win.parent.postMessage(msg, origin);
  return true;
}

/**
 * showSession(session, tile): ask the workspace to show a tile agent's
 * session — xbin.openSession (D229: the same message, with a reply) when
 * the client has it, else the bare message.
 */
export async function showSession(session, tile, win = globalThis.window) {
  const msg = openSession(session, tile);
  if (!msg) return false;
  const x = win && win.xbin;
  if (x && typeof x.openSession === 'function') {
    try { return await x.openSession({ tile: msg.tile, session: msg.session }); } catch { /* the bare message below */ }
  }
  return post(msg, win);
}

/** listen(onContext, win): take context messages from the framing window; returns the unsubscribe. */
export function listen(onContext, win = globalThis.window) {
  if (!win || typeof win.addEventListener !== 'function') return () => {};
  const h = (e) => {
    if (e.source !== win.parent) return; // only the chrome that frames this page
    const c = contextOf(e.data);
    if (c) onContext(c);
  };
  win.addEventListener('message', h);
  // what the chrome said before this page listened (boot.js kept the last one: the dock tells a frame when it has loaded)
  const early = contextOf(win.wsEarlyContext);
  if (early) onContext(early);
  return () => win.removeEventListener('message', h);
}
