// shell/blocks.js — screen blocks (D192): headings and text placed on a
// screen without a tile. Pure functions over plain objects, so
// hack/blocks.test.mjs runs them under node (`make js-test`); the view and
// the gestures are shell-blocks.js.
//
// A screen keeps its blocks in an array of their own beside `tiles`:
//
//   blocks: [{id, kind: 'heading'|'text', text, level?, x, y, w, h, doc?}]
//
// in the `layout` pref for a personal screen, on the org screen (`blocks`,
// PUT /screens/org) for a shared one. Never inside `tiles`: the iOS app and
// every shell before D192 read `tiles` and expect a `path`, and they keep
// the screen's other fields as they are when they save it. x, y, w, h are
// the canvas geometry in logical px on the grid, like a tile's; `doc` is
// its Document-mode place ({row, col, cols}, doc-layout.js); `level` (1–3,
// default 2) only means something on a heading; `text` is one line for a
// heading, Markdown for a text block.
//
// The canvas push (grid-layout.js) and Document mode's rows (doc-layout.js)
// work on items keyed by `path`. A block joins them as an ITEM keyed
// `block:<id>` (no tile path holds a colon) — itemsOf() — and the result is
// split back: the tiles as they came out, the blocks as a patch over the
// stored list (splitItems), so an entry this shell doesn't draw (a kind
// from a later shell) is kept exactly as it was.
import { GRID, spotNear } from './grid-layout.js';

export const KINDS = ['heading', 'text'];
export const LEVELS = [1, 2, 3];
const PREFIX = 'block:';
export const isBlockKey = (k) => typeof k === 'string' && k.startsWith(PREFIX);
export const keyOf = (id) => PREFIX + id;
export const idOf = (k) => (isBlockKey(k) ? k.slice(PREFIX.length) : '');

// Sizes in logical px: a heading is one cell tall (an H1's 36 px line fits
// the 40 px a cell draws), a text block three; either may shrink to one
// cell high and two wide.
export const MIN = { w: 2 * GRID, h: GRID };
export const DEF = { heading: { w: 8 * GRID, h: GRID }, text: { w: 8 * GRID, h: 3 * GRID } };

export const levelOf = (b) => (LEVELS.includes(Number(b?.level)) ? Number(b.level) : 2);
const num = (v) => Number.isFinite(Number(v));

// drawable(b): a block this shell draws — a known kind, an id, a geometry.
export function drawable(b) {
  return !!b && typeof b === 'object' && typeof b.id === 'string' && b.id !== '' && KINDS.includes(b.kind)
    && num(b.x) && num(b.y) && num(b.w) && num(b.h);
}

// blocksOf(list) → the drawable blocks, read for drawing: text a string,
// the geometry on the canvas and at least the minimum. Never written back
// as such — an edit patches the stored entry (patchBlock).
export function blocksOf(list) {
  return (Array.isArray(list) ? list : []).filter(drawable).map((b) => ({
    ...b, text: typeof b.text === 'string' ? b.text : '',
    x: Math.max(0, Number(b.x)), y: Math.max(0, Number(b.y)),
    w: Math.max(MIN.w, Number(b.w)), h: Math.max(MIN.h, Number(b.h)),
  }));
}

// itemsOf(tiles, blocks) → the tiles, then each drawable block as an item
// {path: 'block:<id>', x, y, w, h, doc?, block: kind} — what the push and
// the rows run on.
export function itemsOf(tiles, blocks) {
  const items = (tiles ?? []).map((t) => ({ ...t }));
  for (const b of blocksOf(blocks)) {
    const it = { path: keyOf(b.id), x: b.x, y: b.y, w: b.w, h: b.h, block: b.kind };
    if (b.doc && typeof b.doc === 'object') it.doc = { ...b.doc };
    items.push(it);
  }
  return items;
}

// gridItems(screen) → what a new tile or block must not land on: the
// screen's grid tiles (no floats) and its blocks.
export const gridItems = (sc) => itemsOf(sc?.tiles, sc?.blocks).filter((t) => !t.float);

// splitItems(items) → {tiles, patch, blocks}: the tiles among the items (in
// their order), and patch(list) — the stored blocks with each one's
// geometry and doc place taken from its item (entries without an item —
// not drawn here — as they were). `blocks` says whether any block was
// among the items.
export function splitItems(items) {
  const at = new Map();
  const tiles = [];
  for (const it of items ?? []) {
    if (isBlockKey(it.path)) at.set(idOf(it.path), it);
    else tiles.push(it);
  }
  const patch = (list) => (Array.isArray(list) ? list : []).map((b) => {
    const it = b && at.get(b.id);
    if (!it) return b;
    const n = { ...b, x: it.x, y: it.y, w: it.w, h: it.h };
    if (it.doc) n.doc = { ...it.doc };
    return n;
  });
  return { tiles, patch, blocks: at.size > 0 };
}

// patchBlock(list, id, fn) → the list with block `id` replaced by fn(copy);
// fn returning null removes it.
export function patchBlock(list, id, fn) {
  const out = [];
  for (const b of Array.isArray(list) ? list : []) {
    if (b?.id !== id) { out.push(b); continue; }
    const n = fn({ ...b });
    if (n) out.push(n);
  }
  return out;
}

const uid = () => Math.random().toString(36).slice(2, 9);

// newBlock(kind, sc, at) → a new empty block for screen sc: the default
// size, at the free spot nearest `at` (a menu's click, logical px, with its
// `view`) or the top-left (spotNear, as a menu's new tile), clear of the
// grid tiles and blocks.
export function newBlock(kind, sc, at = null, id = uid()) {
  const k = KINDS.includes(kind) ? kind : 'text';
  const { w, h } = DEF[k];
  const { x, y } = spotNear(gridItems(sc), at ?? { x: 0, y: 0 }, w, h);
  const b = { id, kind: k, text: '', x, y, w, h };
  if (k === 'heading') b.level = 2;
  return b;
}

// mobileOrder(tiles, blocks) → Map item key → CSS `order` for the phone's
// stacked canvas: grid tile i (array order, as the phone always stacked
// them) at 2i+2; a block just before the first of those tiles that comes
// after it in reading order (y, then x), or after them all.
export function mobileOrder(tiles, blocks) {
  const grid = (tiles ?? []).filter((t) => !t.float);
  const m = new Map(grid.map((t, i) => [t.path, 2 * i + 2]));
  const after = (b, t) => t.y > b.y || (t.y === b.y && t.x > b.x);
  for (const b of blocksOf(blocks)) {
    const i = grid.findIndex((t) => after(b, t));
    m.set(keyOf(b.id), 2 * (i < 0 ? grid.length : i) + 1);
  }
  return m;
}

// blockMenuItems(b, s, a) → the block's menu (bx-menu items): Edit, the
// heading's level, Document mode's Row lines (s.rowItems, menus.js), Delete.
// s: {canMutate, rowItems?}; a: {edit(), level(n), remove()}. Nothing when
// the screen can't change (a shared screen in view mode).
export function blockMenuItems(b, s, a) {
  if (!b || !s.canMutate) return [];
  const items = [{ icon: 'pencil', label: 'Edit', hint: 'double-click', action: a.edit }];
  if (b.kind === 'heading') {
    const lv = levelOf(b);
    items.push({ icon: 'doc', label: 'Heading level', hint: `H${lv}`,
      items: LEVELS.map((n) => ({ label: `Heading ${n}`, checked: n === lv, action: () => a.level(n) })) });
  }
  if (s.rowItems) items.push(s.rowItems);
  items.push({ kind: 'sep' }, { icon: 'trash', label: b.kind === 'heading' ? 'Delete heading' : 'Delete text', danger: true, action: a.remove });
  return items;
}

// addItems(s, a) → the "Add heading" / "Add text" lines of the canvas and
// tab menus. s: {orgScreen, draft} (an org screen without a draft opens one
// for an editor, and is read-only for anyone else); a.addBlock(kind).
export function addItems(s, a) {
  const os = s.orgScreen, ro = !!os && !s.draft && !os.canEdit;
  const hint = ro ? 'view mode' : os && !s.draft ? 'starts a draft' : '';
  return [
    { icon: 'doc', label: 'Add heading', hint, disabled: ro, action: () => a.addBlock('heading') },
    { icon: 'list', label: 'Add text', hint, disabled: ro, action: () => a.addBlock('text') },
  ];
}
