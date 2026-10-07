/**
 * shell-blocks.js — screen blocks (D192): headings and text on a screen
 * without a tile. The data and the pure rules are blocks.js; this is the
 * view, the gestures and the shell's ends of them.
 *
 * On the canvas a block sits on the grid like a tile: absolutely placed at
 * its x, y, w, h, pushing tiles (and being pushed by them) through the same
 * drag state as a card (<bx-canvas>._drag, keyed `block:<id>`), resized by
 * its corner. It has no window: no card, no title bar, no frame — a quiet
 * outline under the pointer, and a small tool strip (the grip to drag it,
 * ⋯ for its menu). A double-click edits it in place (an input for a
 * heading, a textarea for Markdown); Enter (a heading), Ctrl/Cmd+Enter,
 * Escape or leaving the field saves, and a block saved empty goes. In
 * Document mode a block is a row cell like a tile's (shell-doc.js draws it
 * with docBlock), dragged by the same grip; on a phone it stacks between
 * the tiles in reading order (mobileOrder).
 *
 * <bx-canvas> changes the blocks with a `bx-blocks` event whose detail is a
 * function (stored list → new list), so the shell applies it to the stored
 * array and an entry this shell doesn't draw is kept. The block's menu goes
 * to the shell's menu (`bx-block-menu` {items, x, y, anchor, title}).
 *
 * Text is Markdown through /vendor/bx-md.js — the hardened renderer: raw
 * HTML escaped, images as their text, links to safe schemes only and in a
 * new tab with no opener — because a shared screen's text is written by
 * one member and read by all.
 */
import { html, css, nothing, unsafeCSS, unsafeHTML } from 'lit';
import { dragPointer } from '/vendor/bx-kit.js';
import { md, mdCssText } from '/vendor/bx-md.js';
import { GAP, snap } from './grid-layout.js';
import { MIN, blocksOf, itemsOf, gridItems, splitItems, patchBlock, newBlock, keyOf, idOf, isBlockKey, levelOf,
  blockMenuItems, mobileOrder } from './blocks.js';
import { placeNew, setCols, rowOf, step } from './doc-layout.js';
import { rowItems } from './menus.js';

const docMode = (sc) => sc?.mode === 'doc';
const emit = (c, type, detail) => c.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
const find = (c, id) => blocksOf(c.blocks).find((b) => b.id === id) ?? null;
// a block change: the shell applies fn to the stored list
const change = (c, fn) => emit(c, 'bx-blocks', fn);

// ---- the canvas's ends ----
// canvasItems(c): the grid's tiles and blocks (no floats), copies — the
// layout a drag's push is computed from.
export const canvasItems = (c) => gridItems({ tiles: c.tiles, blocks: c.blocks });
// docItems(c): the tiles and blocks Document mode's rows are read from.
export const docItems = (c) => itemsOf(c.tiles, c.blocks);
// mutateCanvas(c, fn): fn over the tiles and blocks as one list (Document
// mode's moves and heights); the tiles go out as `bx-tiles`, the blocks'
// places as a `bx-blocks` patch.
export function mutateCanvas(c, fn) {
  const { tiles, patch, blocks } = splitItems(fn(docItems(c)));
  emit(c, 'bx-tiles', tiles);
  if (blocks) change(c, patch);
}
// commitMoves(c, at): a released grid drag — `at` maps item key → its rect.
export function commitMoves(c, at) {
  const geom = (t, n) => ({ ...t, x: n.x, y: n.y, w: n.w, h: n.h });
  if ([...at.keys()].some((k) => !isBlockKey(k))) c._mutate((tiles) => tiles.map((t) => (!t.float && at.has(t.path) ? geom(t, at.get(t.path)) : t)));
  if ([...at.keys()].some(isBlockKey)) change(c, (list) => list.map((b) => (b && at.has(keyOf(b.id)) ? geom(b, at.get(keyOf(b.id))) : b)));
}
// ghostLabel(path): what a push ghost says over a block ('' for a tile).
export const ghostLabel = (c, path) => {
  const b = isBlockKey(path) ? find(c, idOf(path)) : null;
  return b ? (b.text.split('\n')[0].slice(0, 40) || b.kind) : '';
};

// ---- gestures ----
function dragStart(c, ev, b) {
  if (c.mobile || !c.canMutate || ev.button !== 0) return;
  ev.preventDefault(); ev.stopPropagation();
  const base = canvasItems(c), k = c._k, dx = ev.clientX - b.x * k, dy = ev.clientY - b.y * k;
  c._drag = { path: keyOf(b.id), rect: { x: b.x, y: b.y, w: b.w, h: b.h }, moves: [], dirs: null, orig: b };
  dragPointer({
    onMove: (e) => c._dragTo(base, { x: snap(Math.max(0, (e.clientX - dx) / k)), y: snap(Math.max(0, (e.clientY - dy) / k)), w: b.w, h: b.h }),
    onUp: () => c._commitDrag(),
  });
}
function resizeStart(c, ev, b) {
  if (c.mobile || !c.canMutate || ev.button !== 0) return;
  ev.preventDefault(); ev.stopPropagation();
  const base = canvasItems(c), k = c._k, sx = ev.clientX, sy = ev.clientY;
  c._drag = { path: keyOf(b.id), rect: { x: b.x, y: b.y, w: b.w, h: b.h }, moves: [], dirs: null, orig: b, positive: true };
  dragPointer({
    cursor: 'nwse-resize',
    onMove: (e) => c._dragTo(base, { x: b.x, y: b.y, w: snap(Math.max(MIN.w, b.w + (e.clientX - sx) / k)), h: snap(Math.max(MIN.h, b.h + (e.clientY - sy) / k)) }),
    onUp: () => c._commitDrag(),
  });
}

// ---- editing in place ----
export function startEdit(c, id) {
  if (!c?.canMutate || !find(c, id)) return;
  c._bedit = { id };
  c.requestUpdate();
  c.updateComplete.then(() => {
    const el = c.renderRoot.querySelector(`[data-block="${CSS.escape(id)}"] .bedit`);
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange?.(el.value.length, el.value.length);
  });
}
function endEdit(c, id, value) {
  if (c._bedit?.id !== id) return; // saved already (Escape, then the blur it causes)
  c._bedit = null;
  c.requestUpdate();
  const b = find(c, id);
  if (!b) return;
  const v = b.kind === 'heading' ? value.replace(/\s+/g, ' ').trim() : value.replace(/\s+$/, '');
  if (!v.trim()) { change(c, (list) => patchBlock(list, id, () => null)); return; } // saved empty: it goes
  if (v !== b.text) change(c, (list) => patchBlock(list, id, (x) => ({ ...x, text: v })));
}
function editKey(c, e, b) {
  e.stopPropagation();
  const done = e.key === 'Escape' || (e.key === 'Enter' && (b.kind === 'heading' || e.ctrlKey || e.metaKey));
  if (!done) return;
  e.preventDefault();
  endEdit(c, b.id, e.currentTarget.value);
}
function editor(c, b) {
  const on = { keydown: (e) => editKey(c, e, b), blur: (e) => endEdit(c, b.id, e.currentTarget.value) };
  return b.kind === 'heading'
    ? html`<input class="bedit" aria-label="heading" placeholder="Heading" .value=${b.text} @keydown=${on.keydown} @blur=${on.blur}>`
    : html`<textarea class="bedit" aria-label="text (Markdown)" placeholder="Text — Markdown: **bold**, _italic_, [links](https://…), lists"
        rows=${Math.max(3, b.text.split('\n').length + 1)} .value=${b.text} @keydown=${on.keydown} @blur=${on.blur}></textarea>`;
}

// ---- the menu ----
// blockItems(c, id): its lines — in Document mode with the Row submenu a
// tile's menu has (1 · 2 · 4 columns, Move up / down: the keyboard's way).
export function blockItems(c, id) {
  const b = find(c, id);
  const s = { canMutate: !!c?.canMutate, rowItems: null };
  if (b && c._doc) {
    s.rowItems = rowItems(keyOf(id), { tiles: docItems(c), canMutate: c.canMutate && !c.mobile }, {
      docCols: (p, n) => mutateCanvas(c, (t) => setCols(t, rowOf(t, p), n)),
      docStep: (p, d) => mutateCanvas(c, (t) => step(t, p, d)),
      docFit: () => {},
    });
  }
  return blockMenuItems(b, s, {
    edit: () => startEdit(c, id),
    level: (n) => change(c, (list) => patchBlock(list, id, (x) => ({ ...x, level: n }))),
    remove: () => change(c, (list) => patchBlock(list, id, () => null)),
  });
}
// blockMenu(c, e, id) → whether a menu opened (none in view mode: the
// canvas menu then opens, as on the empty canvas).
export function blockMenu(c, e, id, anchorEl = null) {
  const b = find(c, id), items = blockItems(c, id);
  if (!items.length) return false;
  e?.preventDefault?.(); e?.stopPropagation?.();
  emit(c, 'bx-block-menu', { items, x: e?.clientX ?? 0, y: e?.clientY ?? 0,
    anchor: anchorEl?.getBoundingClientRect?.() ?? null, title: b.kind === 'heading' ? 'heading' : 'text' });
  return true;
}

// ---- drawing ----
function content(c, b) {
  if (c._bedit?.id === b.id && c.canMutate) return editor(c, b);
  if (b.kind === 'heading') return html`<div class="bh l${levelOf(b)}" role="heading" aria-level=${levelOf(b)} title=${b.text}>${b.text}</div>`;
  return html`<div class="bt">${unsafeHTML(md(b.text))}</div>`;
}
// The tool strip: the grip (a drag handle, not a button: the keyboard's
// way is the menu) and ⋯ — none while the block is being edited (they
// would sit over the text).
function tools(c, b, onGrip) {
  if (c._bedit?.id === b.id) return nothing;
  return html`<div class="btools">
    <span class="wc bgrip" title="drag to move" aria-hidden="true" @pointerdown=${onGrip}><bx-icon name="grip"></bx-icon></span>
    <button class="wc" title="${b.kind} menu (edit · level · delete)" aria-label="${b.kind} menu"
      @pointerdown=${(e) => e.stopPropagation()} @click=${(e) => blockMenu(c, e, b.id, e.currentTarget)}><bx-icon name="ellipsis"></bx-icon></button>
  </div>`;
}
const press = (c, b) => (e) => {
  c._press.start(e, () => blockMenu(c, { clientX: e.clientX, clientY: e.clientY }, b.id), c.mobile);
  e.stopPropagation(); // a long-press on a block is its menu, not the canvas's
};

// canvasBlocks(c): the blocks on the grid (inside .canvas). A block being
// dragged draws at its live rect, like a card.
export function canvasBlocks(c) {
  const k = c._k, ed = c.canMutate && !c.mobile, ord = c.mobile ? mobileOrder(c.tiles, c.blocks) : new Map();
  return blocksOf(c.blocks).map((b) => {
    const key = keyOf(b.id), d = c._drag?.path === key ? c._drag : null, r = d ? d.rect : b;
    return html`<div class="gblock k-${b.kind} ${d ? 'dragging' : ''} ${ed ? 'ed' : ''}" data-block=${b.id} data-path=${key}
        style="left:${r.x * k}px; top:${r.y * k}px; width:${(r.w - GAP) * k}px; height:${(r.h - GAP) * k}px; --ord:${ord.get(key) ?? 0}"
        @dblclick=${() => startEdit(c, b.id)} @pointerdown=${press(c, b)}>
      ${content(c, b)}
      ${ed ? html`${tools(c, b, (e) => dragStart(c, e, b))}
        <div class="rz" title="drag to resize" @pointerdown=${(e) => resizeStart(c, e, b)}></div>` : nothing}
    </div>`;
  });
}
// The order a grid card takes on a phone's stacked canvas (--ord).
export const cardOrder = (c, path) => (c.mobile && c.blocks?.length ? mobileOrder(c.tiles, c.blocks).get(path) ?? 0 : 0);

// docBlock(c, x, dragStart): a block's cell in Document mode (x: the cell
// shell-doc.js lays out; dragStart its card drag).
export function docBlock(c, x, docDrag) {
  const b = find(c, idOf(x.t.path));
  if (!b) return nothing;
  const ed = c.canMutate && !c.mobile;
  return html`<div class="dcell dblock k-${b.kind} ${x.cols === 1 ? 'one' : ''} ${c._ddrag?.path === x.t.path ? 'dragging' : ''} ${ed ? 'ed' : ''}"
      data-path=${x.t.path} data-block=${b.id} data-row=${x.row} data-col=${x.col} data-cols=${x.cols} data-n=${x.n} style=${x.style}
      @dblclick=${() => startEdit(c, b.id)} @pointerdown=${press(c, b)}>
    ${content(c, b)}
    ${ed ? tools(c, b, (e) => { e.stopPropagation(); docDrag(c, e, x.t.path); }) : nothing}
  </div>`;
}

// Base Two (D184, docs/design.md): no card chrome — the page's own text.
// Headings in the display face (H1 the hero size, H2 the heading size, H3
// the title size), text in the UI face; an outline and the tool strip
// under the pointer while the screen can change.
export const blocksCss = css`
    .gblock { position: absolute; box-sizing: border-box; display: flex; flex-direction: column; min-width: 0; color: var(--bx-text, #E9EAF0); }
    .gblock.ed:hover, .gblock:focus-within, .dblock.ed:hover, .dblock:focus-within { outline: 1px dashed var(--bx-border-strong, #666A7E); outline-offset: 2px; }
    .gblock.dragging { opacity: 0.85; z-index: 50; outline: 1px dashed var(--bx-accent, #8C9BFF); }
    .bh { min-width: 0; padding: 0 4px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
    .gblock .bh { margin: auto 0; }
    .bh.l1 { font: var(--bx-font-hero, 800 32px/36px "Bricolage Grotesque", system-ui, sans-serif); }
    .bh.l2 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", system-ui, sans-serif); }
    .bh.l3 { font: 600 16px/22px var(--bx-display, "Bricolage Grotesque", system-ui, sans-serif); }
    .bt { flex: 1; min-height: 0; padding: 0 4px; overflow: auto; font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); overflow-wrap: anywhere; }
    .bt > :first-child { margin-top: 0; }
    .bt > :last-child { margin-bottom: 0; }
    .bt p, .bt ul, .bt ol { margin: 0 0 8px; }
    .bt ul, .bt ol { padding-left: 20px; }
    .bt h1, .bt h2, .bt h3, .bt h4 { margin: 12px 0 4px; font: var(--bx-font-title, 600 16px/22px "Instrument Sans", system-ui, sans-serif); }
    ${unsafeCSS(mdCssText('.bt'))}
    .bedit {
      flex: 1; box-sizing: border-box; width: 100%; min-width: 0; margin: 0; padding: 0 4px; resize: none;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    }
    input.bedit { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", system-ui, sans-serif); }
    textarea.bedit { font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); padding: 4px; }
    .btools {
      position: absolute; top: 0; right: 0; z-index: 13; display: none;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border, #33353F);
    }
    .ed:hover > .btools, .ed:focus-within > .btools { display: flex; }
    .bgrip { cursor: grab; touch-action: none; }
    .gblock .rz {
      position: absolute; right: 0; bottom: 0; width: 12px; height: 12px; cursor: nwse-resize; z-index: 12; touch-action: none; display: none;
    }
    .gblock.ed:hover .rz { display: block; }
    .gblock .rz::after {
      content: ''; position: absolute; right: 2px; bottom: 2px; width: 6px; height: 6px; box-sizing: border-box;
      border-right: 2px solid var(--bx-border-strong, #666A7E); border-bottom: 2px solid var(--bx-border-strong, #666A7E);
    }
    .ghost[data-label]:not([data-label=""])::after { content: attr(data-label); font-family: var(--bx-sans, "Instrument Sans", system-ui, sans-serif); }
    /* Document mode: a row of the page, as tall as its text */
    .dblock { position: relative; display: flex; flex-direction: column; min-width: 0; padding: 4px 0; color: var(--bx-text, #E9EAF0); }
    .dblock .bt { overflow: visible; }
    .dblock .bh { white-space: normal; }
    .dblock textarea.bedit { min-height: 120px; }
    @media (max-width: 820px) {
      .gtile, .gblock { order: var(--ord, 0); }
      .gblock { position: static !important; width: 100% !important; height: auto !important; min-height: 40px; }
      .gblock .bt { overflow: visible; }
      .gblock .bh { white-space: normal; }
    }
`;

// ---- the shell's ends (s is <bx-shell>) ----
// mutateItems(s, fn): fn over the active screen's tiles and blocks as one
// list — Document mode's Row lines and a tile opened on a Document screen
// (its rows count the blocks).
export function mutateItems(s, fn) {
  const sc = s._screen;
  if (!sc) return;
  const { tiles, patch, blocks } = splitItems(fn(itemsOf(sc.tiles, sc.blocks)));
  s._mutateTiles(() => tiles);
  if (blocks) s._mutateTiles(patch, 'blocks');
}
// addBlock(s, kind, at): a new empty heading or text on the active screen,
// at the free spot nearest `at` (a menu's click) — on a Document screen the
// last row — and straight into editing. An org screen in view mode opens
// its draft for an editor (as opening a tile there does); nobody else
// gets here (addItems disables the lines).
export function addBlock(s, kind, at = null) {
  const os = s._activeOrgScreen;
  if (os && !s._orgDrafts?.[os.id]) {
    if (!os.canEdit) return null;
    s._enterEdit(os.id);
  }
  const sc = s._screen;
  if (!sc) return null;
  const b = newBlock(kind, sc, at);
  s._mutateTiles((list) => [...list, b], 'blocks');
  if (docMode(sc)) mutateItems(s, (items) => placeNew(items, keyOf(b.id)));
  s.updateComplete.then(() => startEdit(s._canvas, b.id));
  return b.id;
}
// The harness's handles (hack/ui-harness, through testApi()).
export const blockTestApi = (s) => ({
  blocks: () => s._screen?.blocks ?? [],
  addBlock: (kind, at = null) => addBlock(s, kind, at),
  editBlock: (id) => startEdit(s._canvas, id),
  blockMenuItems: (id) => (s._canvas ? blockItems(s._canvas, id) : []),
});
