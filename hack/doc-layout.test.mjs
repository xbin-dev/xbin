// hack/doc-layout.test.mjs — unit tests for Document mode's rows
// (workspace-template/shell/doc-layout.js, D187), run by `make js-test`:
// reading the rows from tiles with and without `doc`, a new tile's row, the
// moves a drag or the keyboard makes, 1/2/4-wide rows pulling tiles up and
// pushing them down, the fixed height, and that no edit ever touches a
// tile's canvas geometry.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { docRows, normalise, where, placeNew, moveTile, setCols, step, setHeight, removeTile } from '../workspace-template/shell/doc-layout.js';

const T = (path, x, y, extra = {}) => ({ path, x, y, w: 576, h: 384, ...extra });
const D = (row, col = 0, cols = 1, h) => (h ? { row, col, cols, h } : { row, col, cols });
const shape = (tiles) => docRows(tiles).map((r) => `${r.cols}:${r.tiles.map((t) => t.path).join(',')}`).join(' | ');
const geom = (tiles) => tiles.map(({ path, x, y, w, h, float }) => JSON.stringify({ path, x, y, w, h, float }));
// every edit keeps each tile's canvas fields exactly
const sameCanvas = (before, after) => assert.deepEqual(geom(after).sort(), geom(before).sort());

const screen = () => [T('a', 0, 0), T('b', 576, 0), T('c', 0, 384), T('d', 576, 384), T('e', 0, 768)];

test('tiles without doc are rows of their own in reading order (y, then x)', () => {
  assert.equal(shape(screen()), '1:a | 1:b | 1:c | 1:d | 1:e');
  assert.equal(shape([T('z', 0, 400), T('y', 600, 0), T('x', 0, 0)]), '1:x | 1:y | 1:z');
});

test('floats stay out of the rows', () => {
  const tiles = [T('a', 0, 0), T('f', 0, 0, { float: { x: 1, y: 2, w: 300, h: 200, z: 100 } })];
  assert.equal(shape(tiles), '1:a');
});

test('doc rows come first, ordered by row then col; loose tiles follow', () => {
  const tiles = [T('a', 0, 0, { doc: D(5, 1, 2) }), T('b', 0, 0, { doc: D(5, 0, 2) }), T('c', 0, 0, { doc: D(1) }), T('d', 0, 900)];
  assert.equal(shape(tiles), '1:c | 2:b,a | 1:d');
});

test('an overfull row spills into further rows of its width; bad cols read as 1', () => {
  const tiles = ['a', 'b', 'c'].map((p, i) => T(p, 0, 0, { doc: D(0, i, 2) }));
  assert.equal(shape(tiles), '2:a,b | 2:c');
  assert.equal(shape([T('a', 0, 0, { doc: { row: 0, col: 0, cols: 3 } })]), '1:a');
  assert.equal(shape([T('a', 0, 100, { doc: { row: 'x' } }), T('b', 0, 0)]), '1:b | 1:a');
});

test('normalise writes row/col/cols, keeps a fixed height', () => {
  const tiles = [T('a', 0, 0, { doc: D(7, 0, 1, 300) }), T('b', 0, 50)];
  const out = normalise(tiles, docRows(tiles));
  assert.deepEqual(out.map((t) => t.doc), [D(0, 0, 1, 300), D(1, 0, 1)]);
  sameCanvas(tiles, out);
  assert.deepEqual(where(docRows(out), 'b'), { row: 1, col: 0 });
});

test('placeNew appends the tile as the last row', () => {
  const tiles = [...screen(), T('n', 0, 0)]; // a new tile placed at the canvas origin
  assert.equal(shape(tiles), '1:a | 1:n | 1:b | 1:c | 1:d | 1:e');
  const out = placeNew(tiles, 'n');
  assert.equal(shape(out), '1:a | 1:b | 1:c | 1:d | 1:e | 1:n');
  sameCanvas(tiles, out);
});

test('moveTile beside a tile widens its row 1 → 2 → 4, and a full 4-wide row pushes its last tile down', () => {
  let t = screen();
  t = moveTile(t, 'b', { row: 0, col: 1 });
  assert.equal(shape(t), '2:a,b | 1:c | 1:d | 1:e');
  t = moveTile(t, 'c', { row: 0, col: 2 });
  assert.equal(shape(t), '4:a,b,c | 1:d | 1:e');
  t = moveTile(t, 'd', { row: 0, col: 0 });
  assert.equal(shape(t), '4:d,a,b,c | 1:e');
  t = moveTile(t, 'e', { row: 0, col: 1 });
  assert.equal(shape(t), '4:d,e,a,b | 4:c');
  sameCanvas(screen(), t);
});

test('moveTile insert makes a row of its own; the row it left keeps its width, an empty one goes', () => {
  let t = setCols(screen(), 0, 2); // a,b
  assert.equal(shape(t), '2:a,b | 1:c | 1:d | 1:e');
  t = moveTile(t, 'b', { row: 2, insert: true });
  assert.equal(shape(t), '2:a | 1:c | 1:b | 1:d | 1:e');
  t = moveTile(t, 'e', { row: 0, insert: true, cols: 2 });
  assert.equal(shape(t), '2:e | 2:a | 1:c | 1:b | 1:d');
  t = moveTile(t, 'e', { row: 99, insert: true });
  assert.equal(shape(t), '2:a | 1:c | 1:b | 1:d | 1:e');
  // a no-op move hands the same array back
  assert.equal(moveTile(t, 'a', { row: 0, col: 0 }), t);
  assert.equal(moveTile(t, 'nope', { row: 0 }), t);
});

test('moveTile within its own row: slots are the row as drawn before the move', () => {
  const t = setCols(screen(), 0, 4); // a,b,c,d | e
  assert.equal(moveTile(t, 'a', { row: 0, col: 1 }), t, 'just before its right neighbour: where it is');
  assert.equal(shape(moveTile(t, 'a', { row: 0, col: 2 })), '4:b,a,c,d | 1:e');
  assert.equal(shape(moveTile(t, 'a', { row: 0, col: 4 })), '4:b,c,d,a | 1:e');
  assert.equal(shape(moveTile(t, 'd', { row: 0, col: 0 })), '4:d,a,b,c | 1:e');
});

test('moveTile with explicit cols sets the row width', () => {
  const t = moveTile(screen(), 'c', { row: 0, col: 1, cols: 4 });
  assert.equal(shape(t), '4:a,c | 1:b | 1:d | 1:e');
});

test('setCols wider pulls the following tiles up; narrower pushes the overflow into rows below', () => {
  let t = setCols(screen(), 1, 4);
  assert.equal(shape(t), '1:a | 4:b,c,d,e');
  t = setCols(t, 1, 2);
  assert.equal(shape(t), '1:a | 2:b,c | 2:d,e');
  t = setCols(t, 1, 1);
  assert.equal(shape(t), '1:a | 1:b | 1:c | 2:d,e');
  assert.equal(setCols(t, 42, 2), t);
  sameCanvas(screen(), t);
});

test('step: alone in a row swaps rows; sharing a row leaves it above or below', () => {
  let t = step(screen(), 'c', -1);
  assert.equal(shape(t), '1:a | 1:c | 1:b | 1:d | 1:e');
  assert.equal(step(t, 'a', -1), t, 'the first row cannot go up');
  t = setCols(screen(), 0, 2); // a,b | c | d | e
  t = step(t, 'b', 1);
  assert.equal(shape(t), '2:a | 1:b | 1:c | 1:d | 1:e');
  t = setCols(screen(), 0, 2);
  t = step(t, 'b', -1);
  assert.equal(shape(t), '1:b | 2:a | 1:c | 1:d | 1:e');
});

test('setHeight sets and clears the fixed height, nothing else', () => {
  let t = setHeight(screen(), 'c', 512.4);
  assert.deepEqual(t.find((x) => x.path === 'c').doc, D(2, 0, 1, 512));
  assert.equal(shape(t), shape(screen()));
  t = setHeight(t, 'c', 0);
  assert.deepEqual(t.find((x) => x.path === 'c').doc, D(2));
  sameCanvas(screen(), t);
});

test('removeTile closes the rows up; a screen with no doc stays without', () => {
  const plain = removeTile(screen(), 'b');
  assert.ok(plain.every((x) => !x.doc));
  const t = removeTile(setCols(screen(), 0, 2), 'a');
  assert.equal(shape(t), '2:b | 1:c | 1:d | 1:e');
  assert.deepEqual(t.map((x) => x.doc.row), [0, 1, 2, 3]);
});
