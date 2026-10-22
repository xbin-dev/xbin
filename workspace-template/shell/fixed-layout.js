// shell/fixed-layout.js — Fixed width's layout (D193): the canvas grid cut
// to 48 columns, which stretch to the window (the columns only: a row stays
// 48 px). Pure functions over the screen's items (tiles, and blocks as
// `block:<id>` items, blocks.js itemsOf), so hack/fixed-layout.test.mjs
// runs them under node (`make js-test`), like grid-layout.js.
//
// A tile or block may carry `fixed: {x, y, w, h}` beside its canvas
// geometry: its place in Fixed width, in the canvas's logical px (multiples
// of GRID) with x + w ≤ FIXED_W. The canvas fields (x, y, w, h) are never
// written here: switching the screen back to Canvas shows the canvas exactly
// as it was. An item without `fixed` (opened on the canvas, or by an older
// shell) takes its canvas place when that fits inside the 48 columns and
// clear of what is already placed, else it is packed in below everything.
// The first change made in Fixed width writes `fixed` on every item
// (materialize), so the canvas moving later never moves them here.
import { GRID, overlaps, spotNear } from './grid-layout.js';

export const FIXED_COLS = 48;
export const FIXED_W = FIXED_COLS * GRID; // 2304 logical px

const num = (v) => Number.isFinite(Number(v));
const valid = (f) => !!f && typeof f === 'object' && num(f.x) && num(f.y) && num(f.w) && num(f.h);
const toG = (v) => Math.round(Number(v) / GRID) * GRID;
const byPos = (a, b) => a.y - b.y || a.x - b.x || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
const reading = (a, b) => (a.y ?? 0) - (b.y ?? 0) || (a.x ?? 0) - (b.x ?? 0) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);

// clampRect(r) → r on the grid and inside the 48 columns: at least one cell,
// at most the full width.
export function clampRect(r) {
  const w = Math.min(FIXED_W, Math.max(GRID, toG(r.w))), h = Math.max(GRID, toG(r.h));
  return { x: Math.min(FIXED_W - w, Math.max(0, toG(r.x))), y: Math.max(0, toG(r.y)), w, h };
}

// fixedRects(items) → Map path → {x, y, w, h}: every grid item's place in
// Fixed width (floats aren't on the grid: not in the map). Deterministic:
// the items with `fixed` first (clamped, in place order), then the others
// in reading order — at their canvas place when it fits and is free, and
// what doesn't fit packed left to right in rows below everything placed.
export function fixedRects(items) {
  const grid = (items ?? []).filter((t) => !t.float);
  const out = new Map(), placed = [];
  const put = (path, r) => { out.set(path, r); placed.push(r); };
  grid.filter((t) => valid(t.fixed)).map((t) => ({ path: t.path, ...clampRect(t.fixed) })).sort(byPos)
    .forEach(({ path, ...r }) => put(path, r));
  const over = [];
  for (const t of grid.filter((x) => !valid(x.fixed)).sort(reading)) {
    const r = clampRect({ x: t.x ?? 0, y: t.y ?? 0, w: t.w ?? GRID, h: t.h ?? GRID });
    const fits = (t.x ?? 0) + (t.w ?? 0) <= FIXED_W && r.x === toG(t.x ?? 0);
    if (fits && !placed.some((p) => overlaps(r, p))) put(t.path, r); else over.push({ t, r });
  }
  let x = 0, y = placed.reduce((m, p) => Math.max(m, p.y + p.h), 0), row = 0;
  for (const { t, r } of over) {
    if (x > 0 && x + r.w > FIXED_W) { x = 0; y += row; row = 0; }
    put(t.path, { x, y, w: r.w, h: r.h });
    x += r.w; row = Math.max(row, r.h);
  }
  return out;
}

// fixedLayout(items) → the items with each grid item's x, y, w, h replaced
// by its place in Fixed width (and `fixed` set to it): what the push, the
// drag and the drawing run on. Floats as they are.
export function fixedLayout(items) {
  const at = fixedRects(items);
  return (items ?? []).map((t) => {
    const r = !t.float && at.get(t.path);
    return r ? { ...t, ...r, fixed: { ...r } } : t;
  });
}

// materialize(items, moved) → the items with `fixed` written on every grid
// item: its place now, or — for a key of `moved` (path → rect) — that rect.
// The canvas fields are untouched.
export function materialize(items, moved = new Map()) {
  const at = fixedRects(items);
  return (items ?? []).map((t) => {
    if (t.float) return t;
    const r = moved.get(t.path) ?? at.get(t.path);
    return r ? { ...t, fixed: clampRect(r) } : t;
  });
}

// firstFree(items, w, h) → {x, y}: the top-most, then left-most spot inside
// the 48 columns where a w×h item overlaps none of the items' places.
export function firstFree(items, w, h) {
  const rects = [...fixedRects(items).values()];
  w = Math.min(FIXED_W, w);
  const bottom = rects.reduce((m, r) => Math.max(m, r.y + r.h), 0);
  for (let y = 0; y <= bottom; y += GRID) {
    for (let x = 0; x + w <= FIXED_W; x += GRID) {
      if (!rects.some((r) => overlaps({ x, y, w, h }, r))) return { x, y };
    }
  }
  return { x: 0, y: bottom };
}

// placeFixed(items, path, at) → items: the item `path` (already among them,
// with its canvas geometry) given a place in Fixed width — the free spot
// nearest `at` (a menu's click, Fixed width's logical px, with its `view`),
// else the first free one — and every other grid item its current place
// written down (materialize), so nothing shifts around it.
export function placeFixed(items, path, at = null) {
  const it = (items ?? []).find((t) => t.path === path && !t.float);
  if (!it) return items;
  const others = items.filter((t) => t !== it);
  const w = Math.min(FIXED_W, toG(it.w ?? GRID)), h = Math.max(GRID, toG(it.h ?? GRID));
  const was = fixedRects(others); // what the person sees now, without it
  const { x, y } = at ? spotNear([...was.values()], at, w, h, FIXED_W) : firstFree(others, w, h);
  return items.map((t) => {
    if (t === it) return { ...t, fixed: { x, y, w, h } };
    const r = !t.float && was.get(t.path);
    return r ? { ...t, fixed: r } : t;
  });
}
