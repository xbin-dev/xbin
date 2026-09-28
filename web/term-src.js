/**
 * term-src.js — where `<bx-terminal src>` connects and when it reconnects
 * (docs/elements.md §`<bx-terminal>`): pure functions of URLs and close
 * codes, no DOM, node-tested (hack/term-src.test.mjs).
 *
 * A `src` is any endpoint speaking the terminal wire (docs/protocol.md §The
 * terminal wire): typically a sandbox manager's
 * `…/sbx/sandboxes/{id}/tty?cwd=` through the page's bound interface
 * (docs/sandbox-manager.md), or a tile's own pty route.
 */

// srcTarget: how a page at base (its document URL) dials src — {path} for
// one on this host (the terminal adds the page's credential: the frame token
// in a tile, the session cookie in chrome), {url} for a ws:/wss: URL
// elsewhere (dialled as it is); null for anything that is not a WebSocket
// or an http(s) URL.
export function srcTarget(src, base) {
  const s = String(src ?? '').trim();
  if (!s) return null;
  let u, b;
  try { b = new URL(base); u = new URL(s, b); } catch { return null; }
  const web = { 'http:': 'ws:', 'https:': 'wss:' };
  if (!(u.protocol in web) && u.protocol !== 'ws:' && u.protocol !== 'wss:') return null;
  if (u.host === b.host) return { path: u.pathname + u.search };
  if (u.protocol in web) u.protocol = web[u.protocol];
  return { url: u.href };
}

// SBX_TTY: a sandbox manager's terminal route, …/sbx/sandboxes/{id}/tty
// (the query aside).
const SBX_TTY = /^([^?#]*\/sbx\/sandboxes\/[^/?#]+)\/tty(?:[?#].*)?$/;

// reattachSrc: where to reconnect to the terminal session `session` (the id
// its session frame named) after a drop. A sandbox manager's route attaches
// to that exec (…/execs/{session}/tty: the ring replays, the command went
// on); any other src is dialled again — whether that is the same shell is
// its server's call.
export function reattachSrc(src, session) {
  const m = session ? SBX_TTY.exec(String(src)) : null;
  return m ? `${m[1]}/execs/${encodeURIComponent(session)}/tty` : String(src);
}

// canReattach: src names its sessions (a reconnect finds the same shell).
export const canReattach = (src) => SBX_TTY.test(String(src));

// endedByClose: a close that ends the terminal — no reconnect: a clean one
// (1000, or a close frame without a code, 1005), as the xbin app's terminal
// takes it (docs/native.md). A drop (1006), 1001 or an error code reconnects.
export const endedByClose = (code) => code === 1000 || code === 1005;

// RETRIES: reconnects before giving up; backoff(n): the wait before try n
// (from 0) — 500 ms doubling to 10 s. The count starts over only once a
// socket stayed open LIVED ms, so a server that keeps closing new sockets at
// once is given up on.
export const RETRIES = 6;
export const LIVED = 5000;
export const backoff = (n) => Math.min(500 * 2 ** n, 10000);

// exitWords: the grey line an exit frame ({op:"exit", code, signal}) prints.
export function exitWords(ctl) {
  const code = ctl && typeof ctl.code === 'number' ? ctl.code : null;
  const sig = ctl && ctl.signal ? String(ctl.signal) : '';
  return sig ? `exited on ${sig.startsWith('SIG') ? sig : 'SIG' + sig}` : code ? `exited with code ${code}` : 'exited';
}
