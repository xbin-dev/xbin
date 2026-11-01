// model/mcp-direct.js — MCP servers connected by URL (API.md "Direct MCP
// servers"), as both views draw them (mcp-direct.js on the web,
// native/mcp-direct.js): where each scope lives, the list read from both
// homes, the calls, the sign-in's return address and what a row says.
//
// Homes: in a person's partition their own servers are its ('') and the
// global ones the global instance's ('global'); at the global instance and in
// an unpartitioned agent everything is the page's own backend. A server
// carries the home it was read from, and every call about it goes there.
import { jbody } from '/vendor/bx-kit.js';
import { homeApi } from './home-api.js';
import { partitionState } from './partition.js';

/** homesOf(state): the homes the list is read from. */
export const homesOf = (state = partitionState()) => (state === 'user' ? ['', 'global'] : ['']);

/** addHome(scope, state): where a server of that scope is added. */
export const addHome = (scope, state = partitionState()) => (state === 'user' && scope === 'global' ? 'global' : '');

/**
 * listDirect(state): {servers: [server + {home}], personal, canGlobal, errors}
 * — every home's list (a home that fails is said in errors, the others drawn);
 * personal: may this page add one of the person's own; canGlobal: a global one.
 */
export async function listDirect(state = partitionState()) {
  const out = { servers: [], personal: false, canGlobal: false, errors: [] };
  await Promise.all(homesOf(state).map(async (home) => {
    try {
      const r = await homeApi(home, '/mcp/servers');
      for (const s of r.servers || []) {
        if (state === 'user' && home === '' && s.scope !== 'personal') continue;
        out.servers.push({ ...s, home });
      }
      out.personal = out.personal || !!r.personal;
      out.canGlobal = out.canGlobal || !!r.canGlobal;
    } catch (e) { out.errors.push(home === 'global' ? `the shared space: ${e.message}` : e.message); }
  }));
  out.servers.sort((a, b) => (a.scope === b.scope ? a.name.localeCompare(b.name) : a.scope === 'personal' ? -1 : 1));
  return out;
}

/** redirectUri(): where the server's authorization page sends the person back
 * — this tile's mcp-callback.html at the workspace's address (location.origin
 * is "null" in the tile's sandboxed frame; protocol and host are not). */
export function redirectUri(loc = globalThis.location, self = globalThis.xbin?.self) {
  return `${loc.protocol}//${loc.host}/c/${self}/mcp-callback.html`;
}

export const addDirect = (body, state = partitionState()) =>
  homeApi(addHome(body.scope, state), '/mcp/servers', jbody(body, 'POST')).then((r) => ({ ...r.server, home: addHome(body.scope, state) }));
export const removeDirect = (s) => homeApi(s.home, `/mcp/servers/${encodeURIComponent(s.id)}`, { method: 'DELETE' });
/** signinDirect(s): the authorization page's address ({url}) to open. */
export const signinDirect = (s) => homeApi(s.home, `/mcp/servers/${encodeURIComponent(s.id)}/signin`, jbody({ redirectUri: redirectUri() }, 'POST'));
export const signoutDirect = (s) => homeApi(s.home, `/mcp/servers/${encodeURIComponent(s.id)}/signout`, jbody({}, 'POST'));
export const refreshDirect = (s) => homeApi(s.home, `/mcp/servers/${encodeURIComponent(s.id)}/refresh`, jbody({}, 'POST'));

/** stateOf(s): {text, tone: ok|warn|bad} — what its row says of it. */
export function stateOf(s) {
  const n = (s.tools || []).length;
  const tools = `${n} tool${n === 1 ? '' : 's'}`;
  if (s.state === 'signin') return { text: s.error ? `needs a sign-in — ${s.error}` : 'needs a sign-in', tone: 'warn' };
  if (s.auth === 'oauth') return { text: `signed in · ${tools}`, tone: 'ok' };
  if (s.auth === 'bearer') return { text: `token · ${tools}`, tone: 'ok' };
  return { text: tools, tone: 'ok' };
}

/** scopeWords(s): who it is for, as its row says it. */
export const scopeWords = (s) => (s.scope === 'global' ? 'everyone' : 'only you');

/** the sign-in waits this long for the person to come back before the page stops looking */
export const SIGNIN_WATCH_MS = 5 * 60 * 1000;
