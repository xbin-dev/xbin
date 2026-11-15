// native/win.js — the window of rows a native transcript draws (D130), for
// both native views: the template's conversation (native/chat.js) and a
// workspace agent's (native/ws-chat.js). The state is ui.win (native/ui.js);
// the conversation is ctx.app.session's open one (ctx.app.sel).
import { native } from '/vendor/xb-native.js';
import { ui, ctx, guard } from './ui.js';

// --- a window of the transcript (D130) ------------------------------------------------
//
// The chat renders a window of its blocks: the tail when it opens, growing a
// page of rows at a time as the loader at its top fires `more` (a page is read
// from the backend when no held rows are left above it). The app's transcript
// keeps its bottom still while it follows it, so rows above go — the window
// trimmed from the top, and the messages far above let go (Session.keep) —
// only while the reader is at the bottom, as `scrolled` says; scrolled up,
// the window only grows.
//
// Where the app's transcript has the anchors of vocabulary rev 2 (D189), the
// reader far up lets the live end go too, as the web's window does
// (chat.jumpLatest): while they are away from the end (its `edge`), the row
// the window grew up from is the transcript's `anchor` — kept in place while
// rows come and go around it — so the window is cut below it too, and the
// messages there are let go (the live tail with them: what arrives is
// counted). The composer's "↓ N new — jump to latest" reads the newest page
// again and `scrollTo`s the end; reading down to the window's end grows it
// back a page at a time. From an app of rev 1 nothing below the window goes
// (that would move what the reader looks at) and there is no pill.
export const PAGE = 40;  // rows the window opens on, and grows by
export const TRIM = 120; // a window longer than this is cut back to KEEP rows — at the bottom from the top; away, below the anchor
export const KEEP = 60;

export const anchors = () => { try { return !!native.supports?.('transcript', 2); } catch { return false; } };

// winOf: the window of s.blocks for the open run, placed for this render.
export function winOf(s, run) {
  let w = ui.win;
  if (!w || w.run !== run) w = ui.win = { run, fromKey: null, toKey: null, atBottom: true, anchor: null, jumps: 0, start: 0, end: 0, n: 0 };
  const blocks = s.blocks, n = blocks.length;
  w.start = startOf(w, blocks);
  w.end = endOf(w, blocks);
  if (w.end <= w.start) { w.toKey = null; w.end = n; }
  w.n = n;
  w.fromKey = n ? blocks[w.start].id : null;
  return w;
}

// startOf: where window w starts in blocks — at its first row, else on the
// tail; at the bottom, a window grown past TRIM rows is cut back to KEEP.
function startOf(w, blocks) {
  const n = blocks.length;
  let start = w.fromKey == null ? -1 : blocks.findIndex((b) => b.id === w.fromKey);
  if (start < 0) start = Math.max(0, n - PAGE);
  if (w.atBottom && n - start > TRIM) start = n - KEEP;
  return start;
}

// endOf: where it ends — its last row (cut below the anchor), else the end.
function endOf(w, blocks) {
  if (w.toKey == null) return blocks.length;
  const i = blocks.findIndex((b) => b.id === w.toKey);
  return i < 0 ? blocks.length : i + 1;
}

// more: the loader at the transcript's top is on screen — a page of held
// rows joins the window, else the next older page is read and joins it. The
// row it grew up from keeps its place (rev 2: the anchor, while away).
export const more = guard(async () => {
  const w = ui.win, s = ctx.app.session;
  if (!w || w.run !== ctx.app.sel) return;
  const blocks = s.shown().blocks;
  if (anchors() && !w.atBottom && w.fromKey != null) w.anchor = w.fromKey;
  if (w.start > 0) { w.fromKey = blocks[Math.max(0, w.start - PAGE)].id; return; }
  if (!s.shown().hasOlder) return;
  await s.loadOlder();
  const now = s.shown().blocks, i = now.findIndex((b) => b.id === w.fromKey);
  if (i > 0) w.fromKey = now[Math.max(0, i - PAGE)].id;
});

// atEnd: the reader is at the live end (true) or left it — the model counts
// what arrives meanwhile (Session.follow); at the end no row is anchored.
function atEnd(w, at) {
  if (at === w.atBottom) return;
  w.atBottom = at;
  if (at) w.anchor = null;
  ctx.app.session.follow(at);
  ctx.paint();
}

// scrolled (rev 1): the reader left the bottom, or came back to it (the
// window is trimmed at the next render).
export function scrolled(e) {
  const w = ui.win;
  if (w) atEnd(w, !!e.atBottom);
}

// edge (rev 2): the transcript's end came into view or left it. The end of a
// window cut below (or of the held pages, the live tail let go) is not the
// live end: the window grows a page down, read back when need be.
export function edge(e) {
  const w = ui.win, s = ctx.app.session;
  if (!w || e.edge !== 'end' || w.run !== ctx.app.sel) return;
  const v = s.shown();
  if (e.at && (w.end < v.blocks.length || v.hasNewer || v.detached)) return below(w, v);
  atEnd(w, !!e.at);
}

const below = guard(async (w, v) => {
  const n = v.blocks.length;
  if (w.end < n) { const e = w.end + PAGE; w.toKey = e < n ? v.blocks[e - 1].id : null; return; }
  if (v.hasNewer) await ctx.app.session.loadNewer();
  else await ctx.app.session.latest();
  w.toKey = null;
});

// jump: "↓ N new — jump to latest" — the newest page again when the tail was
// let go, the window back on it, scrolled to the end and followed.
export const jump = guard(async () => {
  const w = ui.win, s = ctx.app.session;
  if (!w) return;
  const v = s.shown();
  if (v.hasNewer || v.detached) await s.latest();
  w.fromKey = w.toKey = w.anchor = null;
  w.jumps++;
  atEnd(w, true);
});

// pillOf: the pill's words while the reader is away and something is below ('' else).
export function pillOf(w, v) {
  if (!anchors() || w.atBottom || !(v.fresh || v.hasNewer || v.detached || w.end < v.blocks.length)) return '';
  return `↓ ${v.fresh ? `${v.fresh} new — ` : ''}jump to latest`;
}

// chatDrawn (native/main.js, after a render): at the bottom, what lies far above
// the window is let go (a page or more at a time); away from it with a row
// anchored (rev 2), a window grown long is cut below the anchor and what lies
// below it let go — the live tail too: what arrives is counted.
export function chatDrawn() {
  const w = ui.win, app = ctx.app;
  if (!w || app.sel !== w.run) return;
  const blocks = app.session.shown().blocks;
  if (!w.atBottom) {
    if (!anchors() || w.anchor == null) return;
    const a = blocks.findIndex((b) => b.id === w.anchor);
    if (a < 0 || endOf(w, blocks) - a <= TRIM) return;
    const e = Math.min(blocks.length, a + KEEP);
    w.toKey = blocks[e - 1].id;
    if (!app.session.keep(0, e, true)) ctx.paint();
    return;
  }
  const i = startOf(w, blocks); // where the render this paint asked for places it
  if (i <= KEEP) return;
  w.fromKey = blocks[i].id;
  app.session.keep(i - KEEP, blocks.length, false);
}
