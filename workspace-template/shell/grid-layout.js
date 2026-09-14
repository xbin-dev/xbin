// shell/grid-layout.js — the grid module the shell's layout is measured in,
// and the pure layout math on it: the overlap test and the push a drag or
// resize performs on its neighbours (the canvas previews it as a "push
// ghost", D66). Imports nothing, so hack/grid-layout.test.mjs runs it under
// node (`make js-test`); shell-kit.js re-exports the constants.
//
// Fixed snappable grid. Tiles are absolutely positioned + sized in multiples of
// GRID px, so resizing the browser window never reflows them, and a tile's own
// content can't stretch it (fixed size — the frame scrolls inside). GAP is the
// gutter drawn between neighbouring tiles. Tiles must be usable at MIN_W with no
// horizontal scroll (see AGENTS.md).
export const GRID = 48;
export const GAP = 8;
export const DEF_W = 12 * GRID; // default new-tile size: 576×384
export const DEF_H = 8 * GRID;
export const MIN_W = 4 * GRID; // resize floor: 192×144
export const MIN_H = 3 * GRID;
export const snap = (v) => Math.max(0, Math.round(v / GRID) * GRID);

// overlaps(a, b): do two grid rects intersect? Half-open on the gutter-
// inclusive w/h — tiles touching at the gutter (a.x + a.w === b.x) do not.
export const overlaps = (a, b) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;

const byPos = (a, b) => a.y - b.y || a.x - b.x || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
const ceilG = (v) => Math.ceil(v / GRID) * GRID;
const floorG = (v) => Math.max(0, Math.floor(v / GRID) * GRID);

// contactDir(p, b, positive): which way p pushes b — along the axis of the
// SMALLER overlap (the side p came in from), away from p's centre; ties go
// vertical, then positive. `positive` forces right/down (a resize grows
// from its top-left corner, so it only ever pushes that way).
function contactDir(p, b, positive) {
  const ox = Math.min(p.x + p.w, b.x + b.w) - Math.max(p.x, b.x);
  const oy = Math.min(p.y + p.h, b.y + b.h) - Math.max(p.y, b.y);
  const horizontal = ox < oy;
  if (positive) return horizontal ? 'r' : 'd';
  if (horizontal) return b.x + b.w / 2 >= p.x + p.w / 2 ? 'r' : 'l';
  return b.y + b.h / 2 >= p.y + p.h / 2 ? 'd' : 'u';
}

// place(b, p, d): move b just clear of p in direction d, snapped IN that
// direction so an unaligned neighbour can never be rounded back into p.
function place(b, p, d) {
  if (d === 'r') b.x = ceilG(p.x + p.w);
  else if (d === 'l') b.x = floorG(p.x - b.w);
  else if (d === 'd') b.y = ceilG(p.y + p.h);
  else b.y = floorG(p.y - b.h);
}

/**
 * pushLayout(tiles, movingPath, rect, {dirs, positive, cap}) → {moves, dirs}
 *
 * The push a tile at `rect` (the dragged/resized one) performs on the other
 * grid tiles, computed from scratch from the ORIGINAL layout — so backing
 * off restores every neighbour, and nothing moves unless it still overlaps
 * at release. A pushed tile moves in the direction it was hit (contactDir),
 * or the direction its own pusher moved in (a cascade travels as one), and
 * keeps that direction across calls through `dirs` (the previous result's
 * map) so a wiggling pointer can't flip the preview. A left/up push that
 * would leave the canvas flips to right/down. Floats and the moving tile are
 * never pushed. `moves` lists only the tiles that end up elsewhere, with
 * their size (for the ghost); `cap` bounds the pushes — pathological layouts
 * stop with some overlap left, as an unchecked drop did before.
 */
export function pushLayout(tiles, movingPath, rect, { dirs, positive = false, cap = 200 } = {}) {
  const orig = new Map(), pos = new Map();
  for (const t of tiles) {
    if (t.float || t.path === movingPath) continue;
    orig.set(t.path, t);
    pos.set(t.path, { path: t.path, x: t.x, y: t.y, w: t.w, h: t.h });
  }
  const dir = new Map(dirs ?? []);
  const queue = [{ path: movingPath, x: rect.x, y: rect.y, w: rect.w, h: rect.h }];
  let n = 0;
  while (queue.length && n < cap) {
    const p = queue.shift();
    for (const b of [...pos.values()].filter((t) => t.path !== p.path && overlaps(p, t)).sort(byPos)) {
      if (!overlaps(p, b)) continue; // cleared by an earlier push this round
      let d = dir.get(b.path) ?? dir.get(p.path) ?? contactDir(p, b, positive);
      if (d === 'l' && p.x - b.w < 0) d = 'r';
      if (d === 'u' && p.y - b.h < 0) d = 'd';
      dir.set(b.path, d);
      place(b, p, d);
      queue.push(b);
      if (++n >= cap) break;
    }
  }
  const moves = [...pos.values()].filter((b) => { const o = orig.get(b.path); return o.x !== b.x || o.y !== b.y; }).sort(byPos);
  return { moves, dirs: new Map(moves.map((m) => [m.path, dir.get(m.path)])) };
}
