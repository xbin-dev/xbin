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
 *
 * Tile deployments (docs/tile-deployments.md): a frame's src may be a tile
 * ref qualified with a deployment — <bx-frame src="apps/crm+dev">, or a
 * window of it, "apps/crm+dev/compose" — and its iframe loads
 * /c/apps/crm+dev/. infoFor resolves such a src the way xbind does: a
 * component at least as deep wins, else the last "+<name>" inside a segment
 * qualifies a tile that has deployments (its /components row carries the
 * primary summary). A non-primary deployment's document is never chrome,
 * has its own origin in origins mode, and its bootstrap token is minted for
 * that deployment — kept only when the server echoes it. The per-page cache
 * follows each tile's `deployments` record events and is refetched when the
 * page becomes visible again, once some tile has deployments, so a frame
 * mounted later reads the current primary summary (pinned, protected).
 *
 * Imports nothing at load (the events socket comes in by dynamic import on
 * first use), so hack/deploy-aware.test.mjs runs resolveSrc under node.
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
export const CREDENTIALLESS = typeof HTMLIFrameElement !== 'undefined' && 'credentialless' in HTMLIFrameElement.prototype;

const facts = (c) => ({ chrome: !!c.chrome, sandbox: c.sandbox || [], origin: c.origin || '', deployments: c.deployments || null });

// Per-component frame facts from /api/xbin/components: chrome (runs
// UNsandboxed — the shell itself and trusted chrome (shipped or admin-approved) like
// tiles/organisations act as the signed-in human), the extra sandbox tokens
// the tile's grants unlock, its own origin (origins mode), and the primary
// summary of a tile with deployments. Fetched once; frames await it before
// creating their iframe so the sandbox attribute is right for the FIRST load
// — a changed attribute only applies to the NEXT navigation.
let _info;
export function frameInfo() {
  _info ??= fetchList().then((m) => { follow(m); return m; });
  return _info;
}
const fetchList = () => fetch('/api/xbin/components')
  .then((r) => (r.ok ? r.json() : []))
  .then((list) => new Map(list.map((c) => [c.path, facts(c)])))
  .catch(() => new Map());

// A deployment name (the glossary grammar): what may follow a "+" in a ref.
const NAME = /^[a-z][a-z0-9-]{0,23}$/;
const depth = (p) => p.split('/').length;

// resolveSrc(src, map) → {path, deployment, bare, stale?} | null: the
// component a frame src belongs to — today's longest prefix (an xbin.window()
// sub-path frame, apps/x/compose, is its component's) — or, for a qualified
// ref, the tile and the deployment it names ('' for none). bare is src
// without its qualifier, the path xbind's component= parameters take. A "+"
// qualifies only a tile whose row carries the primary summary (a zero-state
// tile: "+" means nothing), and never where a component at least as deep
// answers. stale names a tile a "+" would qualify but whose row has no
// summary: the cache may predate its first deployment.
export function resolveSrc(src, m) {
  let best = null, stale = '';
  for (const p of m.keys()) {
    if ((src === p || src.startsWith(p + '/')) && (!best || p.length > best.length)) best = p;
  }
  const segs = String(src).split('/');
  for (let i = segs.length - 1; i >= 0; i--) {
    const j = segs[i].lastIndexOf('+'), name = segs[i].slice(j + 1);
    if (j < 1 || !NAME.test(name)) continue;
    const tile = [...segs.slice(0, i), segs[i].slice(0, j)].join('/');
    if (!m.has(tile)) continue;
    if (best && depth(best) >= i + 1) break;
    if (!m.get(tile).deployments) { stale ||= tile; continue; }
    return { path: tile, deployment: name, bare: [tile, ...segs.slice(i + 1)].join('/') };
  }
  return best || stale ? { path: best, deployment: '', bare: src, ...(stale ? { stale } : {}) } : null;
}

// Longest-prefix lookup so an xbin.window() sub-path frame (apps/x/compose)
// inherits its component's facts, as the server's CSP already does. `path`
// is the owning component; `deployment` names the deployment a qualified src
// shows (absent for the tile's own URL), whose document is never chrome
// unless the ref names the primary (the alias).
export async function infoFor(src) {
  const m = await frameInfo();
  let r = resolveSrc(src, m);
  if (r?.stale) {
    await refreshFrameInfo(r.stale);
    r = resolveSrc(src, m);
  }
  if (!r?.path) return null;
  const i = m.get(r.path);
  if (!r.deployment) return { ...i, path: r.path };
  const alias = r.deployment === (i.deployments?.primary || 'main');
  const origin = alias || !i.origin ? i.origin : await deploymentOrigin(r.path, r.deployment);
  return { ...i, chrome: alias && i.chrome, origin, path: r.path, deployment: r.deployment, bare: r.bare };
}

// A deployment's own origin (origins mode): GET /deployments names it to the
// callers who see the deployment; '' when it doesn't. Cached per tile.
const origins = new Map();
function deploymentOrigin(tile, name) {
  if (!origins.has(tile)) {
    origins.set(tile, fetch(`/api/xbin/deployments?tile=${encodeURIComponent(tile)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((s) => new Map((s?.tile === tile ? s.deployments || [] : []).map((d) => [d.name, d.origin || ''])))
      .catch(() => new Map()));
  }
  return origins.get(tile).then((o) => o.get(name) || '');
}

// A grant changed for one component: refresh ITS entry in the shared map
// (write-through, so a frame mounted later sees the new tokens too). A
// qualified src refreshes its tile's row (/components takes bare paths).
export async function refreshFrameInfo(path) {
  const m = await frameInfo();
  const r = resolveSrc(path, m);
  const tile = r?.deployment ? r.path : path; // a stale tile is refreshed by its own name
  origins.delete(tile);
  const c = await fetch(`/api/xbin/components/${tile}`)
    .then((res) => (res.ok ? res.json() : null)).then((d) => d?.component ?? null).catch(() => null);
  if (c) m.set(tile, facts(c));
}

// follow(map): keep the cache current for frames mounted later — a tile's
// `deployments` record event (pause, resume, protect, reassign, the first
// deployment) refreshes its row; a page coming back into view refetches the
// list, at most every 30 s and only once some tile has deployments, so a
// workspace without any costs nothing.
let fetchedAt = 0;
function follow(m) {
  fetchedAt = Date.now();
  if (typeof document === 'undefined') return;
  const timers = new Map();
  import('/vendor/events-socket.js').then(({ onEvent }) => onEvent((e) => {
    if (e?.type !== 'deployments' || e.data?.op !== 'record' || !m.has(e.component)) return;
    clearTimeout(timers.get(e.component));
    timers.set(e.component, setTimeout(() => { timers.delete(e.component); refreshFrameInfo(e.component); }, 250));
  })).catch(() => { });
  document.addEventListener('visibilitychange', async () => {
    if (document.visibilityState !== 'visible' || Date.now() - fetchedAt < 30000) return;
    if (![...m.values()].some((i) => i.deployments)) return;
    fetchedAt = Date.now();
    const next = await fetchList();
    if (!next.size) return;
    origins.clear();
    m.clear();
    for (const [k, v] of next) m.set(k, v);
  });
}

export const sandboxAttr = (info) => (info?.chrome ? '' : [SANDBOX, ...(info?.sandbox ?? []), ...(info?.origin ? ['allow-same-origin'] : [])].join(' '));

// A bootstrap frame token, minted in chrome context where the cookie
// principal may mint for any tile the human can read ('' when it can't —
// e.g. a bx-frame nested inside another tile). For a deployment's document
// the token must be that deployment's: one the answer doesn't echo it for
// is the primary's (an older xbind ignores the parameter), so it is not
// used, and the frame loads by cookie instead.
const mint = (component, deployment) => fetch(`/api/xbin/frame-token?component=${encodeURIComponent(component)}${deployment ? `&deployment=${encodeURIComponent(deployment)}` : ''}`)
  .then((r) => (r.ok ? r.json() : null))
  .then((d) => (!deployment || (d?.deployment || 'main') === deployment ? d?.token || '' : ''))
  .catch(() => '');

// frameSource resolves how component `src` loads, given infoFor(src):
// {url, sandboxed, sandbox, credentialless, origin} — origin is the tile's
// own origin in origins mode ('' otherwise). ?frame= is consumed by xbind,
// never forwarded. A qualified src keeps its URL (/c/apps/crm+dev/).
export async function frameSource(src, info) {
  const sandboxed = !info?.chrome;
  const sandbox = sandboxAttr(info);
  let url = `/c/${src}/`, credentialless = false, origin = '';
  if (sandboxed && info?.origin) {
    // The workspace URL: the server sends this (same-origin) navigation on
    // to the tile origin with a ticket bound to the session.
    origin = info.origin;
  } else if (sandboxed && CREDENTIALLESS) {
    const tok = await (info?.deployment ? mint(info.bare, info.deployment) : mint(src));
    if (tok) {
      url += `?frame=${encodeURIComponent(tok)}`;
      credentialless = true;
    }
    // No token (e.g. nested inside another tile): load WITHOUT
    // credentialless so the navigation can still authenticate by cookie.
  }
  return { url, sandboxed, sandbox, credentialless, origin };
}
