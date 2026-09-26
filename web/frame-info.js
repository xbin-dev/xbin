/**
 * frame-info.js — how a <bx-frame> loads a component (split out of
 * bx-frame.js): the per-component frame facts the server reports on
 * /api/xbin/components, and the iframe source they imply.
 *
 * Browser-plane isolation (docs/auth.md §Who is calling): non-chrome tiles
 * load sandboxed. Where the browser supports it the frame is also
 * credentialless, its document authenticated by a bootstrap frame token in
 * the URL. Under strict tile asset gating's origins mode (docs/auth.md §Tile
 * asset gating) the server reports each tile's own origin instead: the frame
 * loads the tile's WORKSPACE URL, which the server redirects to the tile's
 * origin with a one-time ticket bound to this browser session (no token
 * passes through here), sandboxed WITH allow-same-origin (the tile origin is
 * the isolation boundary, and its cookie credential needs a real origin) and
 * never credentialless. bx-frame then talks to the frame with that origin as
 * postMessage target, and only accepts messages from it.
 */

// Base sandbox tokens for tile frames: scripts + forms + modals + downloads,
// never allow-same-origin on the workspace origin (that plus allow-scripts
// would void the sandbox). Downloads are safe to allow (ND10): they cross no
// workspace/session/tile boundary and the browser's own download UI
// mediates. Popups need a grant: cap:open-links (ND11) adds allow-popups +
// allow-popups-to-escape-sandbox for THAT tile — the server decides and
// reports the extra tokens on /components, and this file appends exactly
// what it was sent, so the attribute and the CSP sandbox header
// (internal/server/static.go) never drift (browsers intersect the two). Top
// navigation stays blocked always.
export const SANDBOX = 'allow-scripts allow-forms allow-modals allow-downloads';

// <iframe credentialless> (Chromium 110+): loads the frame in an ephemeral
// credential context — no ambient cookie even on the document navigation.
export const CREDENTIALLESS = 'credentialless' in HTMLIFrameElement.prototype;

const facts = (c) => ({ chrome: !!c.chrome, sandbox: c.sandbox || [], origin: c.origin || '' });

// Per-component frame facts from /api/xbin/components: chrome (runs
// UNsandboxed — the shell itself and manifest-flagged trusted chrome like
// tiles/organisations act as the signed-in human), the extra sandbox tokens
// the tile's grants unlock, and its own origin (origins mode). Fetched once;
// frames await it before creating their iframe so the sandbox attribute is
// right for the FIRST load — a changed attribute only applies to the NEXT
// navigation.
let _info;
export function frameInfo() {
  _info ??= fetch('/api/xbin/components')
    .then((r) => (r.ok ? r.json() : []))
    .then((list) => new Map(list.map((c) => [c.path, facts(c)])))
    .catch(() => new Map());
  return _info;
}
// Longest-prefix lookup so an xbin.window() sub-path frame (apps/x/compose)
// inherits its component's facts, as the server's CSP already does. `path`
// is the owning component.
export async function infoFor(src) {
  const m = await frameInfo();
  let best = null;
  for (const [p, i] of m) {
    if ((src === p || src.startsWith(p + '/')) && (!best || p.length > best.p.length)) best = { p, i };
  }
  return best ? { ...best.i, path: best.p } : null;
}
// A grant changed for one component: refresh ITS entry in the shared map
// (write-through, so a frame mounted later sees the new tokens too).
export async function refreshFrameInfo(path) {
  const c = await fetch(`/api/xbin/components/${path}`)
    .then((r) => (r.ok ? r.json() : null)).then((d) => d?.component ?? null).catch(() => null);
  if (c) (await frameInfo()).set(path, facts(c));
}
export const sandboxAttr = (info) => (info?.chrome ? '' : [SANDBOX, ...(info?.sandbox ?? []), ...(info?.origin ? ['allow-same-origin'] : [])].join(' '));

// A bootstrap frame token, minted in chrome context where the cookie
// principal may mint for any tile the human can read ('' when it can't —
// e.g. a bx-frame nested inside another tile).
const mint = (component) => fetch(`/api/xbin/frame-token?component=${encodeURIComponent(component)}`)
  .then((r) => (r.ok ? r.json() : null)).then((d) => d?.token || '').catch(() => '');

// frameSource resolves how component `src` loads, given infoFor(src):
// {url, sandboxed, sandbox, credentialless, origin} — origin is the tile's
// own origin in origins mode ('' otherwise). ?frame= is consumed by xbind,
// never forwarded.
export async function frameSource(src, info) {
  const sandboxed = !info?.chrome;
  const sandbox = sandboxAttr(info);
  let url = `/c/${src}/`, credentialless = false, origin = '';
  if (sandboxed && info?.origin) {
    // The workspace URL: the server sends this (same-origin) navigation on
    // to the tile origin with a ticket bound to the session.
    origin = info.origin;
  } else if (sandboxed && CREDENTIALLESS) {
    const tok = await mint(src);
    if (tok) {
      url += `?frame=${encodeURIComponent(tok)}`;
      credentialless = true;
    }
    // No token (e.g. nested inside another tile): load WITHOUT
    // credentialless so the navigation can still authenticate by cookie.
  }
  return { url, sandboxed, sandbox, credentialless, origin };
}
