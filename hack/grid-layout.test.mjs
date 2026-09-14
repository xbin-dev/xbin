// hack/grid-layout.test.mjs — unit tests for the shell's grid math
// (workspace-template/shell/grid-layout.js), run by `make js-test`: the
// overlap test and the push a dragged/resized tile performs on its
// neighbours (the canvas's "push ghost", D66) and the swap a covered
// neighbour makes into the space the drag vacated (D69).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { GRID, GAP, DEF_W, DEF_H, snap, overlaps, pushLayout } from '../workspace-template/shell/grid-layout.js';

const T = (path, x, y, w = DEF_W, h = DEF_H, extra = {}) => ({ path, x, y, w, h, ...extra });
const R = (x, y, w = DEF_W, h = DEF_H) => ({ x, y, w, h });
const at = (res, path) => res.moves.find((m) => m.path === path);
const aligned = (res) => res.moves.every((m) => m.x % GRID === 0 && m.y % GRID === 0);

test('grid module', () => {
  assert.equal(GRID, 48); assert.equal(GAP, 8); assert.equal(DEF_W, 576); assert.equal(DEF_H, 384);
  assert.equal(snap(70), 48); assert.equal(snap(73), 96); assert.equal(snap(-30), 0);
});

test('overlaps is half-open on the gutter-inclusive size', () => {
  const a = R(0, 0);
  assert.equal(overlaps(a, R(576, 0)), false, 'touching at the gutter');
  assert.equal(overlaps(a, R(0, 384)), false, 'stacked');
  assert.equal(overlaps(a, R(576, 384)), false, 'corner to corner');
  assert.equal(overlaps(a, R(528, 0)), true);
  assert.equal(overlaps(a, R(96, 96, 192, 144)), true, 'contained');
});

test('a tile dragged onto the one below pushes it down, just clear', () => {
  const tiles = [T('a', 0, 0), T('b', 0, 384)];
  const res = pushLayout(tiles, 'a', R(0, 192));
  assert.deepEqual(res.moves, [{ path: 'b', x: 0, y: 576, w: 576, h: 384 }]);
  assert.equal(res.dirs.get('b'), 'd');
  assert.deepEqual(tiles[1], T('b', 0, 384), 'input untouched');
});

test('coming in from the left pushes right', () => {
  const res = pushLayout([T('a', 0, 0), T('b', 576, 0)], 'a', R(288, 0));
  assert.equal(at(res, 'b').x, 864); assert.equal(at(res, 'b').y, 0);
});

test('a cascade travels in the first push direction', () => {
  const res = pushLayout([T('a', 0, 0), T('b', 0, 384), T('c', 0, 768)], 'a', R(0, 192));
  assert.equal(at(res, 'b').y, 576);
  assert.equal(at(res, 'c').y, 960);
  assert.equal(res.dirs.get('c'), 'd');
});

test('a diagonal hit pushes along the smaller overlap', () => {
  const res = pushLayout([T('a', 0, 0), T('b', 576, 384)], 'a', R(480, 288));
  assert.deepEqual([at(res, 'b').x, at(res, 'b').y], [576, 672]);
});

test('a push that would leave the canvas flips to the other side', () => {
  const res = pushLayout([T('a', 1152, 0), T('b', 0, 0)], 'a', R(288, 0));
  assert.deepEqual([at(res, 'b').x, at(res, 'b').y], [864, 0]);
});

test('no overlap means no moves; the moving tile and floats are never moved', () => {
  const tiles = [T('a', 0, 0), T('b', 0, 384), T('f', 0, 0, DEF_W, DEF_H, { float: { x: 1, y: 1, w: 1, h: 1 } })];
  assert.deepEqual(pushLayout(tiles, 'a', R(0, 768)).moves, []);
  const res = pushLayout(tiles, 'a', R(0, 192));
  assert.deepEqual(res.moves.map((m) => m.path), ['b']);
});

test('a big tile over a small contained one pushes it out (flipping when the edge is the canvas)', () => {
  const res = pushLayout([T('a', 0, 0, 1152, 768), T('b', 96, 96, 192, 144)], 'a', R(0, 0, 1152, 768));
  assert.deepEqual([at(res, 'b').x, at(res, 'b').y], [96, 768]);
});

test('positive (resize) pushes right/down only', () => {
  // c holds the space below a, so the covered b cannot yield there (D69)
  const tiles = [T('a', 0, 480, 1152, 768), T('b', 480, 576, 192, 144), T('c', 0, 1248, 1152, 768)];
  assert.equal(at(pushLayout(tiles, 'a', R(0, 480, 1152, 768)), 'b').y, 336, 'a drag pushes up toward the nearer side');
  assert.equal(at(pushLayout(tiles, 'a', R(0, 480, 1152, 768), { positive: true }), 'b').y, 1248, 'a resize pushes down');
});

test('a direction hint from the previous call sticks', () => {
  const tiles = [T('a', 0, 0), T('b', 0, 384)];
  const res = pushLayout(tiles, 'a', R(0, 192), { dirs: new Map([['b', 'r']]) });
  assert.deepEqual([at(res, 'b').x, at(res, 'b').y], [576, 384]);
  assert.equal(pushLayout(tiles, 'a', R(0, 768), { dirs: res.dirs }).dirs.size, 0, 'a tile that stops moving forgets its hint');
});

test('unaligned neighbours end aligned and clear', () => {
  const res = pushLayout([T('a', 0, 0), T('b', 570, 0, 570, 384)], 'a', R(288, 0));
  assert.ok(aligned(res));
  assert.equal(overlaps(R(288, 0), at(res, 'b')), false);
  assert.equal(at(res, 'b').x, 864);
});

test('cap bounds the work', () => {
  assert.deepEqual(pushLayout([T('a', 0, 0), T('b', 0, 384)], 'a', R(0, 192), { cap: 0 }).moves, []);
});

// ---- yielding: the swap of two neighbours (D69) ----
const AB = () => [T('a', 0, 0), T('b', 576, 0)];
const R_ = new Map([['b', 'r']]); // b was first hit from the left (the sticky direction of the approach)

test('a tile dragged fully onto its right neighbour swaps with it', () => {
  const res = pushLayout(AB(), 'a', R(576, 0), { dirs: R_ });
  assert.deepEqual(res.moves, [{ path: 'b', x: 0, y: 0, w: 576, h: 384 }]);
  assert.equal(res.dirs.get('b'), 'R', 'recorded as a yield');
});

test('half-way over, the neighbour cannot fit on the far side and is pushed as before', () => {
  const res = pushLayout(AB(), 'a', R(288, 0), { dirs: R_ });
  assert.deepEqual([at(res, 'b').x, res.dirs.get('b')], [864, 'r']);
});

test('past the neighbour, the swapped tile follows the drag back and ends where it started', () => {
  assert.equal(at(pushLayout(AB(), 'a', R(624, 0), { dirs: R_ }), 'b').x, 48, 'hugs the drag');
  assert.deepEqual(pushLayout(AB(), 'a', R(1152, 0), { dirs: R_ }).moves, [], 'cleared: no move');
});

test('a yield sticks while its spot stays free, even below half coverage', () => {
  const tiles = [T('a', 0, 0), T('b', 1152, 0)]; // a gap between them
  const first = pushLayout(tiles, 'a', R(912, 0), { dirs: R_ }); // covers 336 of 576 → yields
  assert.deepEqual([at(first, 'b').x, first.dirs.get('b')], [336, 'R']);
  const back = pushLayout(tiles, 'a', R(720, 0), { dirs: first.dirs }); // covers 144: pushes without the memory
  assert.equal(at(back, 'b').x, 144, 'still yielding');
  assert.equal(at(pushLayout(tiles, 'a', R(720, 0), { dirs: R_ }), 'b').x, 1296, 'a fresh hit at 144 pushes');
});

test('a covered tile inside a big drag yields into the space the drag left', () => {
  const res = pushLayout([T('a', 0, 480, 1152, 768), T('b', 480, 576, 192, 144)], 'a', R(0, 480, 1152, 768), { dirs: new Map([['b', 'u']]) });
  assert.deepEqual([at(res, 'b').y, res.dirs.get('b')], [1248, 'U']);
});

test('no swap when the far side is taken, or for a resize, or down a cascade', () => {
  assert.equal(at(pushLayout([...AB(), T('c', 0, 384)], 'a', R(576, 0), { dirs: R_ }), 'b').x, 0, 'a tile below the freed spot does not block the swap');
  // b is taller than a: its far-side spot reaches c, below where a was
  const blocked = pushLayout([T('a', 576, 0), T('b', 1152, 0, 576, 768), T('c', 576, 384)], 'a', R(1152, 0), { dirs: R_ });
  assert.deepEqual([at(blocked, 'b').x, blocked.dirs.get('b')], [1728, 'r'], 'far side taken → push');
  const grow = pushLayout([T('a', 576, 0), T('b', 1152, 0)], 'a', R(576, 0, 1152, 384), { dirs: R_, positive: true });
  assert.equal(at(grow, 'b').x, 1728, 'a resize never swaps');
  const chain = pushLayout([T('a', 0, 0), T('b', 576, 0), T('c', 1152, 0)], 'a', R(576, 0), { dirs: new Map([['b', 'r'], ['c', 'r']]) });
  assert.deepEqual([at(chain, 'b').x, at(chain, 'c')], [0, undefined], 'b swaps, c untouched');
});

test('the same downwards: fully onto the tile below swaps them', () => {
  const tiles = [T('a', 0, 0), T('b', 0, 384)];
  const res = pushLayout(tiles, 'a', R(0, 384), { dirs: new Map([['b', 'd']]) });
  assert.deepEqual(res.moves, [{ path: 'b', x: 0, y: 0, w: 576, h: 384 }]);
  assert.equal(at(pushLayout(tiles, 'a', R(0, 192), { dirs: new Map([['b', 'd']]) }), 'b').y, 576, 'half-way: pushed');
});
