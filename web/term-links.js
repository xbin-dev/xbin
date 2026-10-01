// web/term-links.js — what a link in <bx-terminal> does (D178): URLs a CLI
// prints open in one click, the whole URL, however the CLI drew it.
//
// - An OSC 8 hyperlink (Claude Code draws its sign-in URL so) opens its
//   target through xterm's linkHandler: http(s) only, a new tab without an
//   opener, no confirm() — and a TUI that hard-wraps a long URL into one
//   OSC 8 link per row, each pointing at the whole URL, opens the whole
//   URL from any row.
// - A URL in plain text that a program broke over rows with real line
//   breaks (Ink does, at the terminal's width) is joined back: one that
//   reaches the right edge continues onto the following rows made only of
//   URL characters (joinedLinksAt). This provider is registered before the
//   web-links addon, which still finds the rest (one row, or wrapped by the
//   terminal itself).
// - A program's OSC 52 copy ("c to copy") reaches the clipboard
//   (@xterm/addon-clipboard), only while this terminal has the focus, and
//   it never reads the clipboard back.
//
// joinedLinksAt is pure (rows in, links out): hack/term-links.test.mjs.

// openLink(uri): an http(s) URL in a new tab with no opener; anything else
// is ignored.
export function openLink(uri) {
  let u;
  try { u = new URL(uri); } catch { return false; }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return false;
  window.open(u.href, '_blank', 'noopener,noreferrer');
  return true;
}

const URL_CHAR = /[A-Za-z0-9\-._~:/?#[\]@!$&'()*+,;=%]/;
const allURL = (s) => s !== '' && [...s].every((c) => URL_CHAR.test(c));
const FIND = /https?:\/\/[A-Za-z0-9\-._~:/?#[\]@!$&'()*+,;=%]+/g;
const MAX_ROWS = 40; // the most rows one joined URL spans

// joinedLinksAt(rowAt, cols, i) → [{text, start:{x,y}, end:{x,y}}]: the URLs
// broken over rows by real line breaks that touch buffer row i (0-based);
// x/y are xterm's 1-based buffer coordinates, end inclusive. rowAt(j) is
// row j as {text, xs, wrapped} — its characters, the cell each sits in,
// and whether the terminal wrapped onto it — or null past the buffer.
export function joinedLinksAt(rowAt, cols, i) {
  const row = (j) => (j >= 0 ? rowAt(j) : null);
  const reachesEdge = (r) => {
    const t = r.text.replace(/ +$/, '');
    return t.length > 0 && r.xs[t.length - 1] >= cols - 1 && URL_CHAR.test(t[t.length - 1]);
  };
  // b continues a: the terminal wrapped onto it, or a's text runs to the
  // right edge and b is URL characters alone
  const continues = (a, b) => !!a && !!b && (b.wrapped || (reachesEdge(a) && allURL(b.text.trim())));
  const me = row(i);
  if (!me) return [];
  let s = i, e = i;
  while (i - s < MAX_ROWS && continues(row(s - 1), row(s))) s--;
  while (e - s < MAX_ROWS && continues(row(e), row(e + 1))) e++;
  if (s === e) return [];
  // the joined text, each character's cell, and whether a hard break precedes it
  let text = '';
  const at = [], hard = [];
  for (let j = s; j <= e; j++) {
    const r = row(j);
    let t = r.text.replace(/ +$/, ''), from = 0;
    if (j > s && !r.wrapped) { const lead = t.length - t.trimStart().length; t = t.slice(lead); from = lead; }
    for (let k = 0; k < t.length; k++) {
      text += t[k];
      at.push({ x: r.xs[from + k], y: j });
      hard.push(k === 0 && j > s && !r.wrapped);
    }
  }
  const out = [];
  for (const m of text.matchAll(FIND)) {
    const url = m[0].replace(/[.,;:!?'"]+$/, '');
    const a = m.index, z = a + url.length - 1;
    if (url.length < 10 || !hard.slice(a + 1, z + 1).includes(true)) continue; // no real break inside: the web-links addon's
    if (at[a].y > i || at[z].y < i) continue; // not on this row
    try { new URL(url); } catch { continue; }
    out.push({ text: url, start: { x: at[a].x + 1, y: at[a].y + 1 }, end: { x: at[z].x + 1, y: at[z].y + 1 } });
  }
  return out;
}

// rowOf(term, j): buffer row j of xterm's active buffer as joinedLinksAt
// reads it (wide characters take their first cell).
export function rowOf(term, j) {
  const b = term.buffer.active, line = b.getLine(j);
  if (!line) return null;
  const cell = b.getNullCell();
  let text = '';
  const xs = [];
  for (let x = 0; x < term.cols; x++) {
    const c = line.getCell(x, cell);
    if (!c || c.getWidth() === 0) continue;
    const ch = c.getChars() || ' ';
    for (const u of ch) { text += u; xs.push(x); }
  }
  return { text, xs, wrapped: line.isWrapped };
}

// wireLinks(term, {focused}): the link behaviour above on an xterm.js
// Terminal (5.x) whose addons are loaded (loadXterm). focused() says
// whether this terminal has the focus (the clipboard write's gate).
export function wireLinks(term, { focused = () => true } = {}) {
  term.options.linkHandler = { activate: (ev, uri) => openLink(uri), allowNonHttpProtocols: false };
  term.registerLinkProvider({
    provideLinks(y, cb) {
      const ls = joinedLinksAt((j) => rowOf(term, j), term.cols, y - 1);
      cb(ls.length ? ls.map((l) => ({ range: { start: l.start, end: l.end }, text: l.text, activate: (ev, t) => openLink(t) })) : undefined);
    },
  });
  if (window.WebLinksAddon) term.loadAddon(new window.WebLinksAddon.WebLinksAddon((ev, uri) => openLink(uri)));
  // OSC 52: writes only (a read answers empty), only while focused, bounded.
  // addon-clipboard 0.1.0 takes (base64, provider) — its typings say otherwise.
  if (window.ClipboardAddon) {
    term.loadAddon(new window.ClipboardAddon.ClipboardAddon(undefined, {
      readText: () => '',
      writeText: (sel, text) => {
        if (sel !== 'c' || !focused() || typeof text !== 'string' || !text || text.length > 1 << 20) return undefined;
        return navigator.clipboard?.writeText(text).catch(() => { });
      },
    }));
  }
}
