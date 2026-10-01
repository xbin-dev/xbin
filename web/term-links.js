// web/term-links.js — what a link in <bx-terminal> does (D178): URLs a CLI
// prints open in one click, the whole URL, however the CLI drew it.
//
// - An OSC 8 hyperlink (Claude Code draws its sign-in URL so) opens its
//   target through xterm's linkHandler: http(s) only, a new tab without an
//   opener — and a TUI that hard-wraps a long URL into one OSC 8 link per
//   row, each pointing at the whole URL, opens the whole URL from any row.
//   It opens at once only when what the row shows is part of the target
//   (and, where the text names a host, the target's host): an OSC 8 link
//   can show one thing and point at another, so any other link first says
//   where it really goes (a confirm naming its host and its whole URL) —
//   the security review of D178 (linkTrust).
// - A URL in plain text that a program broke over rows with real line
//   breaks (Ink does, at the terminal's width) is joined back: one that
//   reaches the right edge continues onto the following rows made only of
//   URL characters (joinedLinksAt). This provider is registered before the
//   web-links addon, which still finds the rest (one row, or wrapped by the
//   terminal itself).
// - No link with a user part (`https://claude.ai@evil.example`) ever opens:
//   joined rows could make one, and it shows one host while going to another.
// - A program's OSC 52 copy ("c to copy") reaches the clipboard through our
//   own OSC 52 handler (parseOsc52): writes only, only while this terminal
//   has the focus, at most 1 MiB; a read (`?`) is ignored — nothing is
//   written back to the program.
//
// joinedLinksAt, linkTrust and parseOsc52 are pure: hack/term-links.test.mjs.

// safeURL(uri): uri as a URL when it may be opened — http(s), no user part —
// else null.
function safeURL(uri) {
  let u;
  try { u = new URL(uri); } catch { return null; }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
  if (u.username || u.password) return null;
  return u;
}

// openLink(uri): an http(s) URL with no user part in a new tab with no
// opener; anything else is ignored.
export function openLink(uri) {
  const u = safeURL(uri);
  if (!u) return false;
  window.open(u.href, '_blank', 'noopener,noreferrer');
  return true;
}

// the host a link's visible text names, when it starts with one
// ("https://claude.ai/…", "claude.ai/…"); '' when it names none.
function hostNamed(text) {
  const m = /^[a-z][a-z0-9+.-]*:\/\/([^/?#\s]+)/i.exec(text) || /^((?:[a-z0-9-]+\.)+[a-z][a-z0-9-]*)(?=[/:?#]|$)/i.exec(text);
  if (!m) return '';
  return m[1].replace(/^.*@/, '').replace(/:\d+$/, '').toLowerCase();
}

// linkTrust(uri, visible) → 'open' | 'confirm' | 'refuse': what a click on a
// link whose target is uri and whose cells show visible does. refuse: not
// http(s), or a user part; open: visible (trimmed) is part of uri and, when
// it starts with a host, that is uri's host — a TUI's per-row piece of its
// own URL; confirm: anything else (it shows one thing and goes elsewhere,
// or shows nothing).
export function linkTrust(uri, visible) {
  const u = safeURL(uri);
  if (!u) return 'refuse';
  const v = String(visible ?? '').trim();
  if (!v || !String(uri).includes(v)) return 'confirm';
  const h = hostNamed(v);
  if (h && h !== u.hostname.toLowerCase()) return 'confirm';
  return 'open';
}

// confirmWords(uri, visible): what the confirm says before a link that shows
// something else opens.
export function confirmWords(uri, visible) {
  const u = safeURL(uri);
  const v = String(visible ?? '').trim();
  return `This link goes to ${u ? u.host : 'another site'} — not necessarily what it shows.\n\n` +
    `It shows: ${v || '(nothing)'}\nIt opens: ${uri}\n\nOpen it?`;
}

// rangeText(term, range): the text a link's cells show (xterm's 1-based,
// end-inclusive buffer range).
function rangeText(term, range) {
  if (!range || !range.start || !range.end) return '';
  const b = term.buffer.active;
  let out = '';
  for (let y = range.start.y; y <= range.end.y; y++) {
    const line = b.getLine(y - 1);
    if (!line) continue;
    const from = y === range.start.y ? range.start.x - 1 : 0;
    const to = y === range.end.y ? range.end.x : term.cols;
    out += line.translateToString(true, Math.max(0, from), Math.max(0, to));
  }
  return out;
}

// activateLink(term, uri, range, ask): an OSC 8 link's click.
function activateLink(term, uri, range, ask = (msg) => window.confirm(msg)) {
  const visible = rangeText(term, range);
  switch (linkTrust(uri, visible)) {
    case 'open': return openLink(uri);
    case 'confirm': return ask(confirmWords(uri, visible)) ? openLink(uri) : false;
  }
  return false;
}

// parseOsc52(data) → {read: true} | {sel, text} | null: an OSC 52 sequence's
// payload ("<selection>;<base64>"). read: the program asks for the clipboard
// (`?`) — never answered. text: the decoded UTF-8 (null when the base64 is
// bad or over max bytes decoded).
export function parseOsc52(data, max = 1 << 20) {
  const s = String(data ?? '');
  const i = s.indexOf(';');
  if (i < 0) return null;
  const sel = s.slice(0, i), b64 = s.slice(i + 1);
  if (b64 === '?') return { read: true, sel };
  if (b64.length > Math.ceil(max / 3) * 4 + 4 || !/^[A-Za-z0-9+/]*={0,2}$/.test(b64)) return { sel, text: null };
  let bin;
  try { bin = atob(b64); } catch { return { sel, text: null }; }
  if (bin.length > max) return { sel, text: null };
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  let text;
  try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); } catch { return { sel, text: null }; }
  return { sel, text };
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

// wireLinks(term, {focused, ask}): the link behaviour above on an xterm.js
// Terminal (5.x) whose addons are loaded (loadXterm). focused() says
// whether this terminal has the focus (the clipboard write's gate); ask is
// the confirm before a link that shows something other than its target.
export function wireLinks(term, { focused = () => true, ask } = {}) {
  term.options.linkHandler = { activate: (ev, uri, range) => activateLink(term, uri, range, ask), allowNonHttpProtocols: false };
  // the text of these links is the URL itself: what they show is where they go
  term.registerLinkProvider({
    provideLinks(y, cb) {
      const ls = joinedLinksAt((j) => rowOf(term, j), term.cols, y - 1).filter((l) => safeURL(l.text));
      cb(ls.length ? ls.map((l) => ({ range: { start: l.start, end: l.end }, text: l.text, activate: (ev, t) => openLink(t) })) : undefined);
    },
  });
  if (window.WebLinksAddon) term.loadAddon(new window.WebLinksAddon.WebLinksAddon((ev, uri) => openLink(uri)));
  // OSC 52: writes only, only while focused, at most 1 MiB; a read (`?`) is
  // ignored — consumed, never answered into the program's input
  term.parser?.registerOscHandler?.(52, (data) => {
    const r = parseOsc52(data);
    if (!r || r.read || r.text == null || !r.text) return true;
    if (!r.sel.includes('c') || !focused()) return true; // the clipboard selection only, as before
    navigator.clipboard?.writeText(r.text).catch(() => { });
    return true;
  });
}
