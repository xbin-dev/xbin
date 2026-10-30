// hack/tiling.test.mjs — unit tests for the Tiled mode's tree
// (workspace-template/shell/tiling.js, D193), run by `make js-test`: one
// tile fills the screen and each next one splits the focused (else the
// last) tile's space along its longer side; closing gives the space to the
// sibling; dividers, swaps, rotations; a tile dropped on a half of another
// or of the screen (D205); and that a tree from the network —
// anything at all — comes out valid and fitted to the tiles on the screen.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { clean, leaves, layout, insert, remove, setRatio, swap, rotate, swapSibling, parentOf, pathOf, normalise, fromPaths,
  same, zoneOf, place, placeRoot, drop, SWAP_ZONE, MAX_DEPTH, MAX_LEAVES } from '../workspace-template/shell/tiling.js';

const R = { x: 0, y: 0, w: 1600, h: 900 };
const rectOf = (t, p, r = R) => layout(t, r).rects.get(p);
const round = (r) => r && Object.fromEntries(Object.entries(r).map(([k, v]) => [k, Math.round(v)]));

test('one tile fills the screen; a second splits it along the longer side, the new tile second', () => {
  let t = insert(null, 'a', null, R);
  assert.deepEqual(t, { leaf: 'a' });
  assert.deepEqual(rectOf(t, 'a'), R);
  t = insert(t, 'b', 'a', R);
  assert.deepEqual(t, { split: 'row', ratio: 0.5, a: { leaf: 'a' }, b: { leaf: 'b' } }, 'wide: side by side');
  assert.deepEqual(rectOf(t, 'b'), { x: 800, y: 0, w: 800, h: 900 });
  // a tall screen splits top/bottom
  assert.equal(insert({ leaf: 'a' }, 'b', 'a', { x: 0, y: 0, w: 400, h: 800 }).split, 'col');
});

test('the next tile splits the focused tile, else the last one', () => {
  let t = fromPaths(['a', 'b'], R); // a | b, each 800×900: taller than wide
  t = insert(t, 'c', 'a', R);
  assert.equal(pathOf(t, 'c'), 'ab', 'c went under a');
  assert.deepEqual(round(rectOf(t, 'c')), { x: 0, y: 450, w: 800, h: 450 });
  t = insert(t, 'd', 'nope', R); // no such focus: the last tile (b)
  assert.equal(pathOf(t, 'd'), 'bb');
  assert.deepEqual(leaves(t), ['a', 'c', 'b', 'd']);
  assert.equal(insert(t, 'a', 'b', R), t, 'a tile already in it stays where it is');
});

test('layout gives every tile its share and every node a divider', () => {
  const t = setRatio(fromPaths(['a', 'b', 'c'], R), '', 0.25);
  const { rects, dividers } = layout(t, R);
  assert.deepEqual(rects.get('a'), { x: 0, y: 0, w: 400, h: 900 });
  assert.deepEqual(rects.get('b'), { x: 400, y: 0, w: 1200, h: 450 });
  assert.deepEqual(rects.get('c'), { x: 400, y: 450, w: 1200, h: 450 });
  assert.deepEqual(dividers.map((d) => [d.node, d.split, d.x, d.y, d.w, d.h]), [['', 'row', 400, 0, 0, 900], ['b', 'col', 400, 450, 1200, 0]]);
  assert.deepEqual(dividers[1].box, { x: 400, y: 0, w: 1200, h: 900 });
});

test('remove gives the space to the sibling; the last tile leaves an empty tree', () => {
  const t = fromPaths(['a', 'b', 'c'], R); // a | (b / c)
  assert.deepEqual(remove(t, 'b'), { split: 'row', ratio: 0.5, a: { leaf: 'a' }, b: { leaf: 'c' } });
  assert.deepEqual(rectOf(remove(t, 'a'), 'b'), { x: 0, y: 0, w: 1600, h: 450 });
  assert.equal(remove({ leaf: 'a' }, 'a'), null);
  assert.equal(remove(t, 'zzz'), t, 'nothing to remove: the same tree');
});

test('setRatio clamps; swap, rotate and swapSibling rearrange without losing a tile', () => {
  const t = fromPaths(['a', 'b', 'c'], R);
  assert.equal(setRatio(t, '', 0.02).ratio, 0.1);
  assert.equal(setRatio(t, '', 7).ratio, 0.9);
  assert.equal(setRatio(t, 'a', 0.3), t, 'a leaf has no ratio');
  assert.deepEqual(leaves(swap(t, 'a', 'c')), ['c', 'b', 'a']);
  assert.equal(swap(t, 'a', 'zz'), t);
  assert.equal(rotate(t, 'b').b.split, 'row', 'b / c becomes b | c');
  assert.equal(rotate({ leaf: 'a' }, 'a').leaf, 'a', 'a tile alone has nothing to rotate');
  const s = swapSibling(setRatio(t, '', 0.3), 'a');
  assert.deepEqual(leaves(s), ['b', 'c', 'a']);
  assert.equal(s.ratio, 0.7, 'each side keeps its size');
  assert.equal(parentOf(t, 'c'), 'b');
  assert.equal(parentOf({ leaf: 'a' }, 'a'), null);
});

test('normalise fits the tree to the tiles on the screen', () => {
  const t = fromPaths(['a', 'b', 'c'], R);
  const n = normalise(t, ['a', 'c', 'd'], R); // b closed elsewhere, d opened elsewhere
  assert.deepEqual(leaves(n), ['a', 'c', 'd']);
  assert.equal(normalise(t, [], R), null);
  assert.ok(same(normalise(t, ['a', 'b', 'c'], R), t), 'nothing to fit: the same tree');
  assert.deepEqual(leaves(normalise(null, ['x', 'y', 'x'], R)), ['x', 'y'], 'duplicates once');
});

test('a tree from the network: anything in, a valid tree out', () => {
  for (const junk of [undefined, null, 3, 'x', [], {}, { leaf: '' }, { leaf: 7 }, { split: 'row' }, { a: [], b: 'q' }]) {
    assert.equal(clean(junk), null, JSON.stringify(junk));
  }
  // a node missing a part is that part; bad ratios and splits are mended; a leaf twice keeps the first
  assert.deepEqual(clean({ split: 'diagonal', ratio: 'lots', a: { leaf: 'a' }, b: { leaf: 'b' } }), { split: 'row', ratio: 0.5, a: { leaf: 'a' }, b: { leaf: 'b' } });
  assert.deepEqual(clean({ split: 'col', a: { leaf: 'a' }, b: null }), { leaf: 'a' });
  assert.deepEqual(clean({ split: 'col', ratio: 0.3, a: { leaf: 'a' }, b: { leaf: 'a' } }), { leaf: 'a' });
  assert.deepEqual(clean({ leaf: 'a', extra: { evil: true } }), { leaf: 'a' });
  // too deep: cut off; too many leaves: capped
  let deep = { leaf: 'z' };
  for (let i = 0; i < MAX_DEPTH + 10; i++) deep = { split: 'row', ratio: 0.5, a: { leaf: `p${i}` }, b: deep };
  assert.ok(leaves(clean(deep)).length <= MAX_DEPTH + 1);
  const many = fromPaths(Array.from({ length: MAX_LEAVES + 20 }, (_, i) => `t${i}`), R);
  assert.equal(leaves(many).length, MAX_LEAVES);
  // the same tile in two places, ratios out of range: rendering still has each tile once
  const n = normalise({ split: 'row', ratio: -4, a: { leaf: 'a' }, b: { split: 'col', ratio: 2, a: { leaf: 'a' }, b: { leaf: 'b' } } }, ['a', 'b'], R);
  assert.deepEqual(leaves(n), ['a', 'b']);
  assert.equal(n.ratio, 0.1);
});

test('rects tile the screen exactly: no gap, no overlap', () => {
  const t = setRatio(setRatio(fromPaths(['a', 'b', 'c', 'd', 'e'], R), 'b', 0.37), '', 0.61);
  const rs = [...layout(t, R).rects.values()];
  const area = rs.reduce((s, r) => s + r.w * r.h, 0);
  assert.ok(Math.abs(area - R.w * R.h) < 1e-6, `area ${area}`);
  for (let i = 0; i < rs.length; i++) for (let j = i + 1; j < rs.length; j++) {
    const a = rs[i], b = rs[j];
    const ox = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x), oy = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
    assert.ok(ox <= 1e-9 || oy <= 1e-9, `${i} and ${j} overlap`);
  }
});

// a | (b / c): the screen of the drop tests
const abc = () => fromPaths(['a', 'b', 'c'], R);

test('zoneOf: the centre box swaps, the four trapezoids around it are the sides', () => {
  const r = { x: 100, y: 100, w: 400, h: 200 }, at = (u, v) => zoneOf(r, r.x + u * r.w, r.y + v * r.h);
  assert.equal(SWAP_ZONE, 0.45);
  assert.equal(at(0.5, 0.5), 'swap');
  assert.equal(at(0.5 + SWAP_ZONE / 2 - 0.01, 0.5 - SWAP_ZONE / 2 + 0.01), 'swap', 'the box\'s corner');
  assert.equal(at(0.05, 0.5), 'left');
  assert.equal(at(0.95, 0.5), 'right');
  assert.equal(at(0.5, 0.05), 'top');
  assert.equal(at(0.5, 0.95), 'bottom');
  // past the box the diagonals decide: near the top-left corner, the nearer edge
  assert.equal(at(0.1, 0.2), 'left');
  assert.equal(at(0.2, 0.1), 'top');
  assert.equal(at(0.85, 0.7), 'right');
  assert.equal(at(0.7, 0.85), 'bottom');
  assert.equal(zoneOf(r, 300, 200, 0.1), 'swap', 'the box\'s size is the caller\'s');
  assert.equal(zoneOf(r, 300 + 0.2 * 400, 200, 0.3), 'right');
});

test('place: half of the target\'s space, the space the tile left going to what shared it', () => {
  const t = abc();
  // a onto c's left: a's half collapses into b / c, then c's space splits with a on its left
  const p = place(t, 'a', 'c', 'left');
  assert.deepEqual(p, { split: 'col', ratio: 0.5, a: { leaf: 'b' }, b: { split: 'row', ratio: 0.5, a: { leaf: 'a' }, b: { leaf: 'c' } } });
  assert.deepEqual(round(rectOf(p, 'a')), { x: 0, y: 450, w: 800, h: 450 });
  assert.deepEqual(round(rectOf(p, 'b')), { x: 0, y: 0, w: 1600, h: 450 }, 'b grew into a\'s space');
  // each side, half each, the tile on that side
  for (const [side, split, first] of [['left', 'row', true], ['right', 'row', false], ['top', 'col', true], ['bottom', 'col', false]]) {
    const n = place(t, 'a', 'b', side).a; // b / c is the root once a is out: b is its first part
    assert.deepEqual(n, { split, ratio: 0.5, a: { leaf: first ? 'a' : 'b' }, b: { leaf: first ? 'b' : 'a' } }, side);
  }
  // onto its own sibling: the two share their space again, the ratio half each
  const s = place(setRatio(t, 'b', 0.2), 'c', 'b', 'top');
  assert.deepEqual(s.b, { split: 'col', ratio: 0.5, a: { leaf: 'c' }, b: { leaf: 'b' } });
  assert.deepEqual(round(rectOf(s, 'c')), { x: 800, y: 0, w: 800, h: 450 });
  // what keeps it as it is
  assert.equal(place(t, 'a', 'a', 'left'), t, 'onto itself');
  assert.equal(place(t, 'zz', 'b', 'left'), t, 'a tile not in the tree');
  assert.equal(place(t, 'a', 'zz', 'left'), t, 'a target not in the tree');
  assert.equal(place(t, 'a', 'b', 'middle'), t, 'no such side');
  assert.deepEqual(leaves(place(t, 'a', 'b', 'bottom')).sort(), ['a', 'b', 'c'], 'no tile lost or doubled');
});

test('placeRoot: half of the screen, the rest as it was without the tile', () => {
  const t = setRatio(abc(), 'b', 0.3);
  const p = placeRoot(t, 'c', 'top');
  assert.deepEqual(p, { split: 'col', ratio: 0.5, a: { leaf: 'c' }, b: { split: 'row', ratio: 0.5, a: { leaf: 'a' }, b: { leaf: 'b' } } });
  assert.deepEqual(round(rectOf(p, 'c')), { x: 0, y: 0, w: 1600, h: 450 });
  const q = placeRoot(t, 'a', 'right');
  assert.deepEqual(q.a, { split: 'col', ratio: 0.3, a: { leaf: 'b' }, b: { leaf: 'c' } }, 'the rest keeps its splits');
  assert.equal(q.b.leaf, 'a');
  assert.equal(placeRoot({ leaf: 'a' }, 'a', 'left').leaf, 'a', 'a tile alone stays');
  assert.equal(placeRoot(t, 'zz', 'left'), t);
  assert.equal(placeRoot(t, 'a', 'up'), t);
  // a tree already MAX_DEPTH deep without the tile can't go one deeper
  let deep = { leaf: 'z' };
  for (let i = 0; i < MAX_DEPTH; i++) deep = { split: 'row', ratio: 0.5, a: deep, b: { leaf: `p${i}` } };
  const full = { split: 'row', ratio: 0.5, a: { leaf: 'x' }, b: deep };
  assert.equal(placeRoot(full, 'x', 'left'), full);
  assert.equal(place(full, 'x', 'z', 'left'), full, 'nor past it at the target');
});

test('drop: swap, a half of the screen or of a tile, nothing', () => {
  const t = abc();
  assert.deepEqual(drop(t, 'a', { target: 'c', zone: 'swap' }), swap(t, 'a', 'c'));
  assert.deepEqual(drop(t, 'a', { target: 'c', zone: 'right' }), place(t, 'a', 'c', 'right'));
  assert.deepEqual(drop(t, 'b', { screen: true, zone: 'bottom' }), placeRoot(t, 'b', 'bottom'));
  assert.equal(drop(t, 'a', null), t);
  // the result is a tree clean() keeps as it is
  for (const d of [{ target: 'b', zone: 'top' }, { screen: true, zone: 'left' }]) assert.deepEqual(clean(drop(t, 'c', d)), drop(t, 'c', d));
});
