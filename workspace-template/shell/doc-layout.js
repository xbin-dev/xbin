// shell/doc-layout.js — Document mode's layout (D187): a screen read as a
// page of rows, each row 1, 2 or 4 tiles wide. Pure functions over the
// screen's tiles, so hack/doc-layout.test.mjs runs them under node (`make
// js-test`), like grid-layout.js.
//
// A tile may carry `doc: {row, col, cols, h?}` beside its canvas geometry:
// row orders the rows (any numbers; only their order counts), col orders a
// tile within its row, cols is the row's width (1, 2 or 4), h a fixed height
// in px for a tile whose document can't grow to its content. The canvas
// fields (x, y, w, h, float) are never read for placement here except to
// order tiles without `doc`, and never written: switching a screen back to
// Canvas shows the canvas exactly as it was. Floating tiles stay windows and
// are left out of the rows (their `doc`, if any, is kept).
//
// A row here is {cols, tiles: [tile]}: its tiles packed left in col order,
// at most `cols` of them. Every edit below reads the rows, changes them and
// writes them back as normalised `doc` fields (row = index, col = index in
// the row) on every non-floating tile — normalise(tiles, rows).

export const COLS = [1, 2, 4];
const fitCols = (n) => (n > 2 ? 4 : n === 2 ? 2 : 1);
const colsOf = (v) => (COLS.includes(Number(v)) ? Number(v) : 1);
const reading = (a, b) => (a.y ?? 0) - (b.y ?? 0) || (a.x ?? 0) - (b.x ?? 0) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);

// docRows(tiles) → [{cols, tiles}]: the rows in order. Tiles with `doc`
// first, grouped by row; a group holding more tiles than its width spills
// into further rows of that width. Then every tile without `doc` (opened on
// the canvas, or by an older shell) as a row of its own, in reading order.
export function docRows(tiles) {
  const placed = (tiles ?? []).filter((t) => !t.float);
  const groups = new Map(), grouped = new Set();
  for (const t of placed) {
    if (!t.doc || typeof t.doc !== 'object' || !Number.isFinite(Number(t.doc.row))) continue;
    const k = Number(t.doc.row);
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(t);
    grouped.add(t);
  }
  const rows = [];
  for (const k of [...groups.keys()].sort((a, b) => a - b)) {
    const g = groups.get(k).sort((a, b) => (Number(a.doc.col) || 0) - (Number(b.doc.col) || 0) || reading(a, b));
    const cols = Math.max(...g.map((t) => colsOf(t.doc.cols)));
    for (let i = 0; i < g.length; i += cols) rows.push({ cols, tiles: g.slice(i, i + cols) });
  }
  const loose = placed.filter((t) => !grouped.has(t)).sort(reading);
  for (const t of loose) rows.push({ cols: 1, tiles: [t] });
  return rows;
}

// normalise(tiles, rows): the tiles with every row member's `doc` rewritten
// from `rows` (a tile's fixed height kept); floats and anything else as is.
export function normalise(tiles, rows) {
  const at = new Map();
  rows.filter((r) => r.tiles.length).forEach((r, row) => r.tiles.forEach((t, col) => at.set(t.path, { row, col, cols: r.cols })));
  return (tiles ?? []).map((t) => {
    const d = at.get(t.path);
    if (!d || t.float) return t;
    const h = Number(t.doc?.h);
    return { ...t, doc: h > 0 ? { ...d, h } : d };
  });
}

// where(rows, path) → {row, col} of a tile in the rows, or null.
export function where(rows, path) {
  for (let r = 0; r < rows.length; r++) {
    const c = rows[r].tiles.findIndex((t) => t.path === path);
    if (c >= 0) return { row: r, col: c };
  }
  return null;
}

// rowOf(tiles, path) → the index of the tile's row, or -1.
export const rowOf = (tiles, path) => where(docRows(tiles), path)?.row ?? -1;

const clone = (rows) => rows.map((r) => ({ cols: r.cols, tiles: [...r.tiles] }));

// spill(rows, i): a row holding more than its width keeps the first `cols`
// tiles and pushes the rest into new rows of the same width just below.
function spill(rows, i) {
  const r = rows[i];
  if (r.tiles.length <= r.cols) return;
  const extra = r.tiles.splice(r.cols);
  const more = [];
  for (let j = 0; j < extra.length; j += r.cols) more.push({ cols: r.cols, tiles: extra.slice(j, j + r.cols) });
  rows.splice(i + 1, 0, ...more);
}

// placeNew(tiles, path) → tiles: the tile `path` (already in tiles, without
// a place in the rows yet or anywhere) moved to a row of its own at the end.
export function placeNew(tiles, path) {
  const rows = clone(docRows(tiles)).map((r) => ({ ...r, tiles: r.tiles.filter((t) => t.path !== path) }));
  const t = (tiles ?? []).find((x) => x.path === path && !x.float);
  if (t) rows.push({ cols: 1, tiles: [t] });
  return normalise(tiles, rows.filter((r) => r.tiles.length));
}

// moveTile(tiles, path, {row, col, cols, insert}) → tiles.
//   insert: true — the tile becomes a new row of its own (`cols` wide,
//     default 1) placed before the row now at index `row` (past the end:
//     the last row).
//   otherwise — the tile joins row `row` at slot `col` (default: the end),
//     the row's width becoming `cols` when given; a row that would hold
//     more than its width widens to the next width (1 → 2 → 4) when no
//     `cols` is given, and a 4-wide row pushes its last tile into a new row
//     below.
// Indices are those of docRows(tiles) before the move. The row the tile
// leaves keeps its width; a row left empty goes.
export function moveTile(tiles, path, { row = 0, col, cols, insert = false } = {}) {
  const rows = clone(docRows(tiles));
  const from = where(rows, path);
  if (!from) return tiles;
  const t = rows[from.row].tiles[from.col];
  const target = rows[row] ?? null;
  // within its own row, slots after the tile shift left once it is lifted
  if (!insert && target === rows[from.row] && col != null && col > from.col) col -= 1;
  if (!insert && target === rows[from.row] && (col == null || col === from.col) && (cols == null || colsOf(cols) === target.cols)) return tiles;
  rows[from.row].tiles.splice(from.col, 1);
  if (insert) {
    const i = target ? rows.indexOf(target) : rows.length;
    rows.splice(i, 0, { cols: colsOf(cols ?? 1), tiles: [t] });
  } else {
    if (!target) return tiles;
    const at = Math.max(0, Math.min(col ?? target.tiles.length, target.tiles.length));
    target.tiles.splice(at, 0, t);
    if (cols != null) target.cols = colsOf(cols);
    else if (target.tiles.length > target.cols && target.cols < 4) target.cols = fitCols(target.tiles.length);
    spill(rows, rows.indexOf(target));
  }
  return normalise(tiles, rows.filter((r) => r.tiles.length));
}

// setCols(tiles, row, n) → tiles: row `row` becomes n wide (1, 2 or 4).
// Wider pulls the tiles that follow it up into it, in order, until it is
// full; narrower pushes its overflow into new rows of width n below.
export function setCols(tiles, row, n) {
  const rows = clone(docRows(tiles));
  const r = rows[row];
  if (!r) return tiles;
  const cols = colsOf(n);
  r.cols = cols;
  while (r.tiles.length < cols && rows.length > row + 1) {
    const next = rows[row + 1];
    r.tiles.push(next.tiles.shift());
    if (!next.tiles.length) rows.splice(row + 1, 1);
  }
  spill(rows, row);
  return normalise(tiles, rows.filter((x) => x.tiles.length));
}

// step(tiles, path, dir) → tiles: the keyboard's Move up (-1) / Move down
// (+1). A tile alone in its row swaps that row with its neighbour; a tile
// sharing a row leaves it, as a row of its own just above or below it.
export function step(tiles, path, dir) {
  const rows = clone(docRows(tiles));
  const at = where(rows, path);
  if (!at) return tiles;
  const r = rows[at.row];
  if (r.tiles.length === 1) {
    const j = at.row + (dir < 0 ? -1 : 1);
    if (j < 0 || j >= rows.length) return tiles;
    [rows[at.row], rows[j]] = [rows[j], rows[at.row]];
  } else {
    const [t] = r.tiles.splice(at.col, 1);
    rows.splice(dir < 0 ? at.row : at.row + 1, 0, { cols: 1, tiles: [t] });
  }
  return normalise(tiles, rows);
}

// setHeight(tiles, path, h) → tiles: the tile's fixed height in px (the
// Document mode handle), or none (h falsy: the tile fits its content again).
export function setHeight(tiles, path, h) {
  const rows = docRows(tiles);
  if (!where(rows, path)) return tiles;
  return normalise(tiles, rows).map((t) => {
    if (t.path !== path) return t;
    const d = { ...t.doc };
    delete d.h;
    return { ...t, doc: h > 0 ? { ...d, h: Math.round(h) } : d };
  });
}

// removeTile(tiles, path) → tiles without it, the rows closed up.
export function removeTile(tiles, path) {
  const rest = (tiles ?? []).filter((t) => t.path !== path);
  const rows = docRows(rest);
  return rows.some((r) => r.tiles.some((t) => t.doc)) ? normalise(rest, rows) : rest;
}
