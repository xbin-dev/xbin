// hack/blocks.test.mjs — unit tests for screen blocks (D192,
// workspace-template/shell/blocks.js), run by `make js-test`: what the
// shell draws and what it keeps, a new block's spot, blocks in the canvas
// push and in Document mode's rows (split back without touching an entry
// this shell doesn't draw), a phone's order, the menus — and that a screen
// with blocks reads to everything before D192 (tiles only) exactly as one
// without.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { blocksOf, drawable, itemsOf, gridItems, splitItems, patchBlock, newBlock, keyOf, idOf, isBlockKey, levelOf,
  mobileOrder, blockMenuItems, addItems, DEF, MIN } from '../workspace-template/shell/blocks.js';
import { pushLayout, spotNear, overlaps } from '../workspace-template/shell/grid-layout.js';
import { docRows, moveTile, placeNew, setCols, rowOf, step } from '../workspace-template/shell/doc-layout.js';
import { rowItems } from '../workspace-template/shell/menus.js';

const T = (path, x, y, extra = {}) => ({ path, x, y, w: 576, h: 384, ...extra });
const H = (id, x, y, extra = {}) => ({ id, kind: 'heading', text: id.toUpperCase(), level: 2, x, y, w: 384, h: 48, ...extra });
const X = (id, x, y, extra = {}) => ({ id, kind: 'text', text: `**${id}**`, x, y, w: 384, h: 144, ...extra });
const shape = (items) => docRows(items).map((r) => `${r.cols}:${r.tiles.map((t) => t.path).join(',')}`).join(' | ');
// apply a split back onto a screen, as the shell does
const apply = (sc, items) => { const s = splitItems(items); return { ...sc, tiles: s.tiles, blocks: s.patch(sc.blocks) }; };

test('keys: a block joins the tiles as block:<id>, never a tile path', () => {
  assert.equal(keyOf('ab12'), 'block:ab12');
  assert.equal(idOf('block:ab12'), 'ab12');
  assert.equal(idOf('apps/x'), '');
  assert.ok(isBlockKey('block:x') && !isBlockKey('apps/block') && !isBlockKey(undefined));
});

test('blocksOf draws known kinds only, with sane text and geometry; levels default to 2', () => {
  const list = [H('a', 0, 0), X('b', 0, 48, { text: 7 }), { id: 'c', kind: 'image', x: 0, y: 0, w: 96, h: 96 },
    { kind: 'heading', x: 0, y: 0, w: 96, h: 48 }, H('d', -48, 0, { w: 10, h: 1 }), null, 'junk', H('e', 0, 0, { x: 'nope' })];
  const out = blocksOf(list);
  assert.deepEqual(out.map((b) => b.id), ['a', 'b', 'd']);
  assert.equal(out[1].text, '', 'a non-string text draws empty');
  assert.deepEqual([out[2].x, out[2].w, out[2].h], [0, MIN.w, MIN.h], 'clamped onto the canvas and to the minimum');
  assert.equal(drawable({ id: 'c', kind: 'image', x: 0, y: 0, w: 1, h: 1 }), false);
  assert.deepEqual([levelOf({}), levelOf({ level: 1 }), levelOf({ level: '3' }), levelOf({ level: 9 })], [2, 1, 3, 2]);
  assert.deepEqual(blocksOf(undefined), []);
  assert.deepEqual(blocksOf({ not: 'an array' }), []);
});

test('newBlock: default size, the free spot nearest the click, clear of tiles and blocks', () => {
  const sc = { tiles: [T('apps/a', 0, 0), T('apps/f', 0, 0, { float: { x: 1, y: 1, w: 2, h: 2 } })], blocks: [H('h', 576, 0)] };
  const h = newBlock('heading', sc, { x: 10, y: 10 }, 'n1');
  assert.deepEqual(h, { id: 'n1', kind: 'heading', text: '', level: 2, x: h.x, y: h.y, w: DEF.heading.w, h: DEF.heading.h });
  for (const it of gridItems(sc)) assert.ok(!overlaps(h, it), `clear of ${it.path}`);
  const t = newBlock('text', sc, null, 'n2');
  assert.equal(t.level, undefined);
  assert.deepEqual([t.w, t.h], [DEF.text.w, DEF.text.h]);
  for (const it of gridItems(sc)) assert.ok(!overlaps(t, it), `clear of ${it.path}`);
  assert.equal(newBlock('bogus', sc, null, 'n3').kind, 'text');
  // a new tile's spot (spotNear over gridItems) avoids a block too
  const spot = spotNear(gridItems({ tiles: [], blocks: [X('x', 0, 0, { w: 576, h: 384 })] }), { x: 0, y: 0 });
  assert.ok(!(spot.x === 0 && spot.y === 0), 'a tile never lands on a block');
});

test('the canvas push: a dragged tile pushes a block, a dragged block pushes a tile; split back by id', () => {
  const sc = { tiles: [T('apps/a', 0, 0), T('apps/b', 0, 480)], blocks: [H('h', 0, 384, { w: 576 }), { id: 'z', kind: 'future', keep: 1 }] };
  // drag apps/a down by one cell: it lands on the heading, which yields into
  // the space the drag vacated (a swap, D69) — out of the tile's way either way
  const items = gridItems(sc);
  const { moves } = pushLayout(items, 'apps/a', { x: 0, y: 48, w: 576, h: 384 });
  const hm = moves.find((m) => m.path === 'block:h');
  assert.ok(hm && !overlaps(hm, { x: 0, y: 48, w: 576, h: 384 }), `the heading makes way (${JSON.stringify(moves)})`);
  // a resize never swaps: growing apps/a by a cell pushes the heading down
  // (to touch apps/b, which stays)
  const rz = pushLayout(items, 'apps/a', { x: 0, y: 0, w: 576, h: 432 }, { positive: true }).moves;
  assert.deepEqual(rz.map((m) => [m.path, m.y]), [['block:h', 432]]);
  // drag the heading onto apps/b: apps/b moves
  const r2 = pushLayout(items, 'block:h', { x: 0, y: 480, w: 576, h: 48 });
  assert.ok(r2.moves.some((m) => m.path === 'apps/b'), 'a block pushes a tile');
  // the result split back: tiles stay tiles, the block takes its new place, the unknown entry is untouched
  const at = new Map(r2.moves.map((m) => [m.path, m]));
  at.set('block:h', { x: 0, y: 480, w: 576, h: 48 });
  const moved = items.map((t) => (at.has(t.path) ? { ...t, ...at.get(t.path) } : t));
  const next = apply(sc, moved);
  assert.deepEqual(next.tiles.map((t) => t.path), ['apps/a', 'apps/b']);
  assert.ok(next.tiles.every((t) => !('block' in t)));
  assert.equal(next.blocks[0].y, 480);
  assert.equal(next.blocks[0].text, 'H', 'everything but geometry kept');
  assert.deepEqual(next.blocks[1], { id: 'z', kind: 'future', keep: 1 }, 'a block this shell does not draw is kept as is');
});

test('Document mode: blocks are rows in reading order with the tiles, full width; moves and widths across both', () => {
  const sc = { tiles: [T('apps/a', 0, 96), T('apps/b', 576, 96)], blocks: [H('h', 0, 0), X('t', 0, 600)] };
  let items = itemsOf(sc.tiles, sc.blocks);
  assert.equal(shape(items), '1:block:h | 1:apps/a | 1:apps/b | 1:block:t', 'no doc: one row each, reading order');
  // put the text beside apps/a (a 2-wide row), then make the heading's row 2 wide (it pulls apps/a? no — the next row)
  items = moveTile(items, 'block:t', { row: 1, col: 1 });
  assert.equal(shape(items), '1:block:h | 2:apps/a,block:t | 1:apps/b');
  let sc2 = apply(sc, items);
  assert.deepEqual(sc2.blocks.find((b) => b.id === 't').doc, { row: 1, col: 1, cols: 2 });
  assert.deepEqual(sc2.blocks.find((b) => b.id === 'h').doc, { row: 0, col: 0, cols: 1 });
  assert.deepEqual(sc2.tiles.find((t) => t.path === 'apps/b').doc, { row: 2, col: 0, cols: 1 });
  // canvas geometry untouched by the rows
  assert.deepEqual(sc2.blocks.map(({ x, y, w, h }) => [x, y, w, h]), sc.blocks.map(({ x, y, w, h }) => [x, y, w, h]));
  // a tile opened on a Document screen is the last row, after the blocks' rows
  items = placeNew([...itemsOf(sc2.tiles, sc2.blocks), T('apps/c', 0, 0)], 'apps/c');
  assert.equal(shape(items), '1:block:h | 2:apps/a,block:t | 1:apps/b | 1:apps/c');
  sc2 = apply(sc2, items);
  // a block added on a Document screen: the last row
  items = placeNew(itemsOf(sc2.tiles, [...sc2.blocks, H('n', 0, 0)]), 'block:n');
  assert.equal(shape(items).split(' | ').pop(), '1:block:n');
  // the Row lines on a block, and Move up
  const ri = rowItems('block:h', { tiles: itemsOf(sc2.tiles, sc2.blocks), canMutate: true }, { docCols() {}, docStep() {}, docFit() {} });
  assert.equal(ri.label, 'Row');
  assert.equal(ri.items.find((x) => x.label === 'Move up').disabled, true, 'the first row cannot move up');
  items = step(itemsOf(sc2.tiles, sc2.blocks), 'block:h', 1);
  assert.equal(shape(items).split(' | ')[0], '2:apps/a,block:t');
  items = setCols(itemsOf(sc2.tiles, sc2.blocks), rowOf(itemsOf(sc2.tiles, sc2.blocks), 'block:h'), 2);
  assert.equal(shape(items).split(' | ')[0], '2:block:h,apps/a', 'a wider heading row pulls the next one up');
});

test('patchBlock edits or removes one block; others as they were', () => {
  const list = [H('a', 0, 0), X('b', 0, 48), { id: 'z', kind: 'future' }];
  assert.deepEqual(patchBlock(list, 'a', (b) => ({ ...b, text: 'New' }))[0].text, 'New');
  assert.deepEqual(patchBlock(list, 'b', () => null).map((b) => b.id), ['a', 'z']);
  assert.equal(patchBlock(list, 'nope', () => null).length, 3);
  assert.equal(patchBlock(list, 'a', (b) => b)[2], list[2], 'untouched entries are the same objects');
});

test('a phone stacks blocks between the tiles: tiles in their array order, a block before the first tile after it', () => {
  const tiles = [T('apps/late', 0, 1000), T('apps/top', 0, 0), T('apps/f', 0, 0, { float: { x: 0, y: 0, w: 1, h: 1 } })];
  const blocks = [H('head', 0, 0), X('mid', 0, 500), X('end', 0, 2000)];
  const m = mobileOrder(tiles, blocks);
  const order = [...m.entries()].sort((a, b) => a[1] - b[1]).map(([k]) => k);
  // apps/late comes first in the array; 'head' (y 0) comes before both tiles; 'mid' before apps/late? apps/late is
  // after mid in reading order and is the first tile (array order) that is — so mid sits before it
  assert.deepEqual(order, ['block:head', 'block:mid', 'apps/late', 'apps/top', 'block:end']);
  assert.ok(!m.has('apps/f'), 'floats are not stacked');
});

test('the block menu: Edit, a heading level, the Row lines in Document mode, Delete; nothing in view mode', () => {
  const calls = [];
  const a = { edit: () => calls.push('edit'), level: (n) => calls.push(`level ${n}`), remove: () => calls.push('remove') };
  const items = blockMenuItems(H('h', 0, 0, { level: 1 }), { canMutate: true }, a);
  assert.deepEqual(items.map((i) => i.kind ? `<${i.kind}>` : i.label), ['Edit', 'Heading level', '<sep>', 'Delete heading']);
  const lv = items[1];
  assert.equal(lv.hint, 'H1');
  assert.deepEqual(lv.items.map((i) => [i.label, !!i.checked]), [['Heading 1', true], ['Heading 2', false], ['Heading 3', false]]);
  lv.items[2].action(); items[0].action(); items[3].action();
  assert.deepEqual(calls, ['level 3', 'edit', 'remove']);
  const txt = blockMenuItems(X('t', 0, 0), { canMutate: true, rowItems: { label: 'Row', items: [] } }, a);
  assert.deepEqual(txt.map((i) => i.kind ? `<${i.kind}>` : i.label), ['Edit', 'Row', '<sep>', 'Delete text']);
  assert.deepEqual(blockMenuItems(X('t', 0, 0), { canMutate: false }, a), []);
  assert.deepEqual(blockMenuItems(null, { canMutate: true }, a), []);
});

test('the Add lines: enabled on a personal screen and a draft, a draft for an editor, disabled for a reader', () => {
  const got = [];
  const a = { addBlock: (k) => got.push(k) };
  const p = addItems({ orgScreen: null, draft: null }, a);
  assert.deepEqual(p.map((i) => [i.label, i.disabled, i.hint]), [['Add heading', false, ''], ['Add text', false, '']]);
  p[0].action(); p[1].action();
  assert.deepEqual(got, ['heading', 'text']);
  assert.equal(addItems({ orgScreen: { canEdit: true }, draft: null }, a)[0].hint, 'starts a draft');
  assert.equal(addItems({ orgScreen: { canEdit: false }, draft: null }, a)[0].disabled, true);
});

test('never break users: tiles-only readers see a screen with blocks exactly as one without', () => {
  // what every reader before D192 (and the iOS app) reads: screens[].tiles with a path each
  const sc = { id: 's', name: 'S', tiles: [T('apps/a', 0, 0)], blocks: [H('h', 0, 400)], mode: 'doc' };
  const before = { id: 's', name: 'S', tiles: [T('apps/a', 0, 0)], mode: 'doc' };
  assert.deepEqual(sc.tiles, before.tiles);
  assert.ok(sc.tiles.every((t) => typeof t.path === 'string' && !isBlockKey(t.path)), 'no block ever sits in tiles');
  // an older shell re-saves a screen by spreading it: the blocks ride along untouched
  const resaved = JSON.parse(JSON.stringify({ ...sc, tiles: [...sc.tiles, T('apps/b', 576, 0)] }));
  assert.deepEqual(resaved.blocks, sc.blocks);
  // a tile edit by this shell (split) never moves blocks it wasn't given
  const s = splitItems(sc.tiles.map((t) => ({ ...t, x: 48 })));
  assert.equal(s.blocks, false);
  assert.deepEqual(s.patch(sc.blocks), sc.blocks);
});
