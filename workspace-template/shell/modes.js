// shell/modes.js — how a screen is arranged (D187, D193): its display mode
// and the screen-level layout the modes keep beside the tiles. Pure, so
// hack/modes.test.mjs runs it under node (`make js-test`).
//
// A screen's `mode` (a personal screen's in the layout pref, an org
// screen's on the server, admin-plane):
//   absent  Canvas — the grid, open to the right (every screen before D187)
//   fixed   Fixed width — the grid cut to 48 columns that stretch with the
//           window; each tile keeps its place there in `fixed` (D193)
//   doc     Document — rows that grow with their content (D187)
//   tiled   Tiled — one tile fills the screen, each next one splits a tile's
//           space in two (bspwm-like, D193); the tree is arrange.tiling
//           (tiling.js), the view shell-tiled.js
// Any other value reads as the canvas, so a screen saved by a newer shell
// still shows. Switching never rewrites the tiles: each mode reads its own
// fields, so switching back shows that mode exactly as it was.
//
// `arrange` (D193) is the screen-level part of the layout: {docWidth?: px —
// Document mode's column limit; tiling?: the Tiled mode's split tree}. It is
// content like the tiles: an org screen's travels in its draft and is
// published with them, under the same revision.

export const MODES = ['canvas', 'fixed', 'doc', 'tiled'];

// screenMode(screen) → one of MODES.
export const screenMode = (s) => (MODES.includes(s?.mode) ? s.mode : 'canvas');
// gridMode(mode): a mode drawn on the grid (Canvas or Fixed width).
export const gridMode = (m) => m === 'canvas' || m === 'fixed';

// arrangeOf(screen) → its arrange object, or {} — never anything but an object.
export const arrangeOf = (s) => (s?.arrange && typeof s.arrange === 'object' && !Array.isArray(s.arrange) ? s.arrange : {});

// layoutFields(screen): what a copy of a screen carries beside its tiles and
// blocks — its mode (absent for the canvas) and its arrange (when any).
export function layoutFields(s) {
  const m = screenMode(s), a = arrangeOf(s);
  return { ...(m !== 'canvas' ? { mode: m } : {}), ...(Object.keys(a).length ? { arrange: a } : {}) };
}

// The Layout menu's lines and the tab's glyph, in menu order.
export const MODE_INFO = {
  canvas: { label: 'Canvas', icon: 'grid', title: 'tiles anywhere on a grid, each its own size; the page grows to the right' },
  fixed: { label: 'Fixed width', icon: 'columns', title: 'the grid cut to 48 columns that stretch to the window: no sideways scrolling' },
  doc: { label: 'Document', icon: 'doc', title: 'a scrolling page of rows (1, 2 or 4 tiles wide); each tile grows to its content' },
  tiled: { label: 'Tiled', icon: 'tiling', title: 'one tile fills the screen; each next one splits a tile\'s space in two' },
};

// ---- Document mode's column limit (arrange.docWidth) ----
export const DOC_MIN_W = 480;
// docWidth(arrange) → the limit in px, or 0 (none: the full pane).
export const docWidth = (a) => (Number(a?.docWidth) >= DOC_MIN_W ? Math.round(Number(a.docWidth)) : 0);
// withDocWidth(arrange, w, pane) → arrange with the limit set — none when w
// reaches the pane's width (within 16px) or w is falsy.
export function withDocWidth(a, w, pane = Infinity) {
  const n = { ...(a ?? {}) };
  delete n.docWidth;
  if (w > 0 && w < pane - 16) n.docWidth = Math.max(DOC_MIN_W, Math.round(w));
  return n;
}
