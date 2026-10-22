// hack/fixed-layout.test.mjs — unit tests for Fixed width's layout
// (workspace-template/shell/fixed-layout.js, D193), run by `make js-test`:
// a stored place is clamped into the 48 columns, a tile without one takes
// its canvas place when it fits and is free and is packed in below when it
// doesn't, the order is deterministic, materialize and placeFixed write
// `fixed` on every grid item, and nothing ever touches the canvas fields.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { FIXED_COLS, FIXED_W, clampRect, fixedRects, fixedLayout, materialize, firstFree, placeFixed } from '../workspace-template/shell/fixed-layout.js';
import { overlaps } from '../workspace-template/shell/grid-layout.js';

const T = (path, x, y, w = 576, h = 384, extra = {}) => ({ path, x, y, w, h, ...extra });
const F = (x, y, w = 576, h = 384) => ({ x, y, w, h });
const canvas = (items) => items.map(({ path, x, y, w, h }) => JSON.stringify({ path, x, y, w, h }));
const disjoint = (rects) => rects.every((a, i) => rects.every((b, j) => i === j || !overlaps(a, b)));

test('48 columns of 48 logical px', () => {
  assert.equal(FIXED_COLS, 48);
  assert.equal(FIXED_W, 2304);
});

test('clampRect: on the grid, inside the columns, at least one cell', () => {
  assert.deepEqual(clampRect(F(2200, 10, 576, 384)), F(1728, 0));
  assert.deepEqual(clampRect(F(-50, -50, 9999, 5)), { x: 0, y: 0, w: 2304, h: 48 });
  assert.deepEqual(clampRect(F(70, 73, 100, 100)), { x: 48, y: 96, w: 96, h: 96 });
});

test('a tile without `fixed` takes its canvas place when it fits; beyond the columns it is packed below', () => {
  const items = [T('a', 0, 0), T('b', 576, 0), T('far', 2400, 0), T('wide', 0, 384, 3000), T('c', 0, 384)];
  const at = fixedRects(items);
  assert.deepEqual(at.get('a'), F(0, 0));
  assert.deepEqual(at.get('b'), F(576, 0));
  assert.deepEqual(at.get('c'), F(0, 384));
  // what doesn't fit goes below everything, left to right in reading order
  assert.deepEqual(at.get('far'), F(0, 768));
  assert.deepEqual(at.get('wide'), { x: 0, y: 1152, w: 2304, h: 384 }, 'capped at the full width, a row of its own');
  assert.ok(disjoint([...at.values()]));
});

test('a stored `fixed` wins and is placed first; a canvas place it covers is packed below', () => {
  const items = [T('a', 0, 0, 576, 384, { fixed: F(0, 0, 1152, 384) }), T('b', 576, 0), T('c', 1152, 0)];
  const at = fixedRects(items);
  assert.deepEqual(at.get('a'), F(0, 0, 1152, 384));
  assert.deepEqual(at.get('c'), F(1152, 0));
  assert.deepEqual(at.get('b'), F(0, 384), 'b\'s canvas place is taken: below');
  // a stored place past the edge is clamped in; garbage reads as none
  const at2 = fixedRects([T('a', 0, 0, 576, 384, { fixed: F(2000, 0) }), T('b', 0, 0, 576, 384, { fixed: { x: 'no' } })]);
  assert.deepEqual(at2.get('a'), F(1728, 0));
  assert.deepEqual(at2.get('b'), F(0, 0));
});

test('deterministic: the same items in any order give the same places; floats stay out', () => {
  const items = [T('a', 0, 0), T('b', 2400, 0), T('c', 2400, 384), T('d', 600, 900), T('f', 0, 0, 576, 384, { float: { x: 1, y: 1, w: 300, h: 200, z: 100 } })];
  const one = JSON.stringify([...fixedRects(items)].sort());
  const two = JSON.stringify([...fixedRects([...items].reverse())].sort());
  assert.equal(one, two);
  assert.equal(fixedRects(items).has('f'), false);
});

test('fixedLayout: x, y, w, h are the fixed place; the canvas fields of the input are untouched', () => {
  const items = [T('a', 0, 0), T('far', 2400, 0)];
  const before = canvas(items);
  const out = fixedLayout(items);
  assert.deepEqual(out.map(({ x, y, w, h }) => ({ x, y, w, h })), [F(0, 0), F(0, 384)]);
  assert.deepEqual(out[1].fixed, F(0, 384));
  assert.deepEqual(canvas(items), before);
});

test('materialize writes `fixed` on every grid item, a moved one at its new rect, never the canvas fields', () => {
  const items = [T('a', 0, 0), T('far', 2400, 0), T('f', 0, 0, 576, 384, { float: { x: 1, y: 1, w: 3, h: 4 } })];
  const out = materialize(items, new Map([['a', F(1728, 0)]]));
  assert.deepEqual(out.map((t) => t.fixed ?? null), [F(1728, 0), F(0, 384), null]);
  assert.deepEqual(canvas(out), canvas(items));
});

test('firstFree: top-most, then left-most, inside the columns', () => {
  assert.deepEqual(firstFree([], 576, 384), { x: 0, y: 0 });
  assert.deepEqual(firstFree([T('a', 0, 0)], 576, 384), { x: 576, y: 0 });
  const row = [0, 576, 1152, 1728].map((x, i) => T(`t${i}`, x, 0));
  assert.deepEqual(firstFree(row, 576, 384), { x: 0, y: 384 });
});

test('placeFixed: the new item at a free spot, everyone else written down where they are', () => {
  const items = [T('a', 0, 0), T('far', 2400, 0), T('n', 0, 0)];
  const out = placeFixed(items, 'n');
  const f = Object.fromEntries(out.map((t) => [t.path, t.fixed]));
  assert.deepEqual(f.a, F(0, 0));
  assert.deepEqual(f.far, F(0, 384), 'as it was seen before n came');
  assert.deepEqual(f.n, F(576, 0));
  assert.deepEqual(canvas(out), canvas(items));
  // near a point: spotNear inside the columns
  const near = placeFixed(items, 'n', { x: 2280, y: 0 });
  assert.deepEqual(near.find((t) => t.path === 'n').fixed, F(1728, 0));
  assert.equal(placeFixed(items, 'nope'), items);
});
