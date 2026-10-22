// shell/tiling.js — the Tiled mode's layout (D193): a screen as one tile
// filling it, each next tile splitting a tile's space in two, bspwm-like.
// Pure functions over a binary tree, so hack/tiling.test.mjs runs them
// under node (`make js-test`), like doc-layout.js and grid-layout.js.
//
// The tree is the screen's `arrange.tiling` (modes.js):
//   a leaf  {leaf: path}                         one tile
//   a node  {split, ratio, a, b}                 two parts of a space:
//           split 'row' — a left of b (side by side); 'col' — a above b;
//           ratio — a's share, 0.1–0.9
// A node is named by its path from the root: '' the root, 'a' its first
// part, 'ab' that part's second… — what a divider carries.
//
// It comes from the network (another shell, an older one, the iOS app
// editing the tiles beside it), so every reader goes through clean():
// garbage, cycles of the same leaf, unknown shapes and trees past the
// depth/size bounds come out as a valid tree or none. normalise() then fits
// it to the tiles actually on the screen — a tile closed elsewhere drops
// out, a tile opened elsewhere is put in — so what is drawn is always every
// placed tile exactly once. The canvas geometry (x, y, w, h) is never read
// for placement here except for the order new tiles go in, and never
// written: switching the screen back to Canvas shows it as it was.

export const MIN_RATIO = 0.1;
export const MAX_RATIO = 0.9;
export const MAX_DEPTH = 64; // a tree of MAX_LEAVES tiles each splitting the last is this deep
export const MAX_LEAVES = 64;
const DEF_RECT = { x: 0, y: 0, w: 1600, h: 900 };

const ratioOf = (r) => (Number.isFinite(Number(r)) && r !== null && r !== '' ? Math.min(MAX_RATIO, Math.max(MIN_RATIO, Number(r))) : 0.5);
const isLeaf = (t) => !!t && typeof t.leaf === 'string';

// clean(tree) → a valid tree, or null: leaves are non-empty strings, each
// path once (the first wins), nodes have both parts (a node left with one
// is that part), ratios are clamped, splits are 'row' or 'col'.
export function clean(t) {
  const seen = new Set();
  const walk = (n, depth) => {
    if (!n || typeof n !== 'object' || Array.isArray(n) || depth > MAX_DEPTH) return null;
    if ('leaf' in n) {
      if (typeof n.leaf !== 'string' || !n.leaf || seen.has(n.leaf) || seen.size >= MAX_LEAVES) return null;
      seen.add(n.leaf);
      return { leaf: n.leaf };
    }
    const a = walk(n.a, depth + 1), b = walk(n.b, depth + 1);
    if (!a || !b) return a ?? b;
    return { split: n.split === 'col' ? 'col' : 'row', ratio: ratioOf(n.ratio), a, b };
  };
  return walk(t, 0);
}

// leaves(tree) → the tiles' paths in tree order (a before b).
export function leaves(t) {
  const out = [];
  const walk = (n) => { if (!n) return; if (isLeaf(n)) out.push(n.leaf); else { walk(n.a); walk(n.b); } };
  walk(t);
  return out;
}

// nodeAt(tree, nodePath) → the node or leaf at that path, or null.
export function nodeAt(t, np = '') {
  let n = t;
  for (const c of np) { if (!n || isLeaf(n)) return null; n = c === 'a' ? n.a : c === 'b' ? n.b : null; }
  return n ?? null;
}

// pathOf(tree, path) → the node path of the tile's leaf, or null.
export function pathOf(t, path) {
  const walk = (n, np) => {
    if (!n) return null;
    if (isLeaf(n)) return n.leaf === path ? np : null;
    return walk(n.a, np + 'a') ?? walk(n.b, np + 'b');
  };
  return walk(t, '');
}

// replaceAt(tree, nodePath, fn) → the tree with fn(node) at that path.
function replaceAt(t, np, fn) {
  if (!np) return fn(t);
  if (!t || isLeaf(t)) return t;
  const k = np[0];
  return { ...t, [k]: replaceAt(t[k], np.slice(1), fn) };
}

// split(rect, node) → [rect of a, rect of b].
function split(r, n) {
  if (n.split === 'col') {
    const h = r.h * n.ratio;
    return [{ x: r.x, y: r.y, w: r.w, h }, { x: r.x, y: r.y + h, w: r.w, h: r.h - h }];
  }
  const w = r.w * n.ratio;
  return [{ x: r.x, y: r.y, w, h: r.h }, { x: r.x + w, y: r.y, w: r.w - w, h: r.h }];
}

// layout(tree, rect) → {rects: Map(path → {x, y, w, h}), dividers: [{node,
// split, x, y, w, h, box}]}: each tile's share of `rect`, and each node's
// divider — a line (w 0 for a 'row' node, h 0 for a 'col' one) at the
// boundary between its parts, with the node's own rect as `box` (what a
// dragged divider measures its ratio in).
export function layout(t, rect = DEF_RECT) {
  const rects = new Map(), dividers = [];
  const walk = (n, r, np) => {
    if (!n) return;
    if (isLeaf(n)) { rects.set(n.leaf, r); return; }
    const [ra, rb] = split(r, n);
    dividers.push(n.split === 'col'
      ? { node: np, split: 'col', x: r.x, y: rb.y, w: r.w, h: 0, box: r }
      : { node: np, split: 'row', x: rb.x, y: r.y, w: 0, h: r.h, box: r });
    walk(n.a, ra, np + 'a');
    walk(n.b, rb, np + 'b');
  };
  walk(t, rect, '');
  return { rects, dividers };
}

// insert(tree, path, focus, rect) → the tree with the tile `path` in it: the
// focused tile's space (else the last tile's) split in two along its longer
// side, half each, the new tile second (right of it, or below). An empty
// tree is the tile alone; a tile already in it stays where it is.
export function insert(t, path, focus, rect = DEF_RECT) {
  if (typeof path !== 'string' || !path) return t;
  if (!t) return { leaf: path };
  if (pathOf(t, path) != null) return t;
  if (leaves(t).length >= MAX_LEAVES) return t;
  const all = leaves(t), target = focus && all.includes(focus) ? focus : all[all.length - 1];
  const np = pathOf(t, target);
  if (np.length >= MAX_DEPTH) return t;
  const r = layout(t, rect).rects.get(target) ?? rect;
  return replaceAt(t, np, (leaf) => ({ split: r.w >= r.h ? 'row' : 'col', ratio: 0.5, a: leaf, b: { leaf: path } }));
}

// remove(tree, path) → the tree without the tile: its sibling takes the
// space they shared (null when it was the only tile).
export function remove(t, path) {
  const walk = (n) => {
    if (!n) return null;
    if (isLeaf(n)) return n.leaf === path ? null : n;
    const a = walk(n.a), b = walk(n.b);
    if (!a || !b) return a ?? b;
    return a === n.a && b === n.b ? n : { ...n, a, b };
  };
  return walk(t);
}

// setRatio(tree, nodePath, r) → the node's ratio set, clamped to 0.1–0.9.
export function setRatio(t, np, r) {
  const n = nodeAt(t, np);
  if (!n || isLeaf(n)) return t;
  return replaceAt(t, np, (x) => ({ ...x, ratio: ratioOf(r) }));
}

// swap(tree, p, q) → the two tiles trade places.
export function swap(t, p, q) {
  if (!p || !q || p === q || pathOf(t, p) == null || pathOf(t, q) == null) return t;
  const walk = (n) => (isLeaf(n) ? (n.leaf === p ? { leaf: q } : n.leaf === q ? { leaf: p } : n) : { ...n, a: walk(n.a), b: walk(n.b) });
  return walk(t);
}

// parentOf(tree, path) → the node path of the tile's parent, or null (the
// tile alone, or not in the tree).
export function parentOf(t, path) {
  const np = pathOf(t, path);
  return np ? np.slice(0, -1) : null;
}

// rotate(tree, path) → the tile's parent split the other way (side by side
// ⇄ one above the other).
export function rotate(t, path) {
  const pp = parentOf(t, path);
  if (pp == null) return t;
  return replaceAt(t, pp, (n) => ({ ...n, split: n.split === 'col' ? 'row' : 'col' }));
}

// swapSibling(tree, path) → the tile and what shares its parent's space (a
// tile, or a group of them) trade sides; the ratio follows, so each keeps
// its size.
export function swapSibling(t, path) {
  const pp = parentOf(t, path);
  if (pp == null) return t;
  return replaceAt(t, pp, (n) => ({ ...n, ratio: ratioOf(1 - n.ratio), a: n.b, b: n.a }));
}

// normalise(tree, paths, rect) → the tree fitted to the tiles on the
// screen, in `paths` order for the ones it lacks: cleaned, every leaf not
// in `paths` removed, every path not in it inserted after the last tile.
// null when there are no paths.
export function normalise(t, paths, rect = DEF_RECT) {
  const want = [...new Set((paths ?? []).filter((p) => typeof p === 'string' && p))].slice(0, MAX_LEAVES);
  const keep = new Set(want);
  let n = clean(t);
  for (const p of leaves(n)) if (!keep.has(p)) n = remove(n, p);
  const have = new Set(leaves(n));
  for (const p of want) if (!have.has(p)) n = insert(n, p, null, rect);
  return n;
}

// fromPaths(paths, rect) → the first tree for a screen switched to Tiled:
// the tiles in order, each splitting the last one's space.
export const fromPaths = (paths, rect = DEF_RECT) => normalise(null, paths, rect);

// same(a, b): two trees alike (no write when an edit changes nothing).
export const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
