// shell/route.js — the grammar of a link into the workspace (#7): the shell
// page's query names a screen, a tile and a place inside the tile's page —
//
//   ?screen=<screen id>&tile=<tile path>&at=<the tile page's fragment>
//
// each optional (`at` only with a tile), readable (a tile path keeps its
// slashes). A link may also name one of the viewer's sessions on the tile
// (D229) —
//
//   ?tile=<tile path>&session=<session id>
//
// which opens the tile's terminal window on it. That part is a request, not
// a place: it is taken once on landing and never written back (the window's
// state isn't in the URL). Pure, no imports: hack/shell-route.test.mjs;
// shell-route.js follows it.

const MAX_AT = 2048;
const ctl = /[\u0000-\u001f\u007f]/g, hasCtl = /[\u0000-\u001f\u007f]/;

// tilePath(p): a workspace-relative tile path as a link carries it, slashes
// at either end dropped; '' for none.
const tilePath = (p) => String(p ?? '').replace(/^\/+|\/+$/g, '');
// validTile(p): a well-formed tile path (no empty, `.` or `..` segment) —
// what <bx-frame> lets through to the shell too.
export const validTile = (p) => !!p && p.length <= 512 && !hasCtl.test(p) && p.split('/').every((s) => s && s !== '.' && s !== '..');
// cleanAt(h): a fragment without its '#', control characters out, clamped.
export const cleanAt = (h) => String(h ?? '').replace(/^#/, '').replace(ctl, '').slice(0, MAX_AT);

// sessionId(v): a session id as a link carries it; '' for anything else.
export const sessionId = (v) => (/^[A-Za-z0-9_-]{1,64}$/.test(String(v ?? '')) ? String(v) : '');

// parseRoute(search) → {screen, tile, at} ('' for absent); `at` only with a
// tile — and `session`, only when the link names one (with a tile).
export function parseRoute(search) {
  const q = new URLSearchParams(search ?? '');
  const tile = tilePath(q.get('tile'));
  const r = { screen: String(q.get('screen') ?? '').replace(ctl, '').slice(0, 128), tile, at: tile ? cleanAt(q.get('at')) : '' };
  const session = tile ? sessionId(q.get('session')) : '';
  return session ? { ...r, session } : r;
}

// Readable values: a tile path keeps its slashes; everything a query value
// can't hold as is (&, =, +, #, space, …) stays escaped.
const enc = (v) => encodeURIComponent(v).replace(/%2F/gi, '/').replace(/%3A/gi, ':').replace(/%40/g, '@').replace(/%2C/gi, ',');
const OURS = ['screen', 'tile', 'at', 'session'];

// buildRoute(route, search) → the query string ('?…', or '' when empty) for
// route, keeping any other parameter `search` carries, after ours. A
// route's `session` is written only when it is given (a link to hand out:
// the shell's own URL never carries one).
export function buildRoute(r, search = '') {
  const parts = [];
  if (r?.screen) parts.push(`screen=${enc(r.screen)}`);
  if (r?.tile) {
    parts.push(`tile=${enc(r.tile)}`);
    if (r.at) parts.push(`at=${enc(r.at)}`);
    if (sessionId(r.session)) parts.push(`session=${r.session}`);
  }
  for (const [k, v] of new URLSearchParams(search ?? '')) if (!OURS.includes(k)) parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(v)}`);
  return parts.length ? `?${parts.join('&')}` : '';
}

export const sameRoute = (a, b) => a.screen === b.screen && a.tile === b.tile && (a.at || '') === (b.at || '');
