/**
 * shell-tiled.js — the Tiled mode's view (D193): one tile fills the screen,
 * each next one splits a tile's space in two, bspwm-like. The tree and its
 * rules are tiling.js; this is the view, the gestures and the shell's ends.
 *
 * <bx-canvas> renders tiledView(this) when its `mode` is 'tiled'. The tiles
 * fill the visible pane exactly — the page never scrolls — each a window
 * as on the grid (a fixed-size frame that scrolls inside), with a gutter
 * between. The cards keep a stable DOM order (by path) and are placed
 * absolutely, so a split, a swap or a divider never moves an iframe in the
 * DOM (which would reload the tile).
 *
 * The tree is the screen's arrange.tiling (modes.js), read through
 * tiling.js normalise() against the tiles placed on the screen, in their
 * canvas reading order: a tile opened or closed elsewhere (another device,
 * an older shell, the iOS app) is in or out of it here too. Every change
 * writes the fitted tree back through the canvas's `bx-arrange`.
 *
 *   - a divider between two parts: drag it to share the space anew; two
 *     clicks in a row on it give each half again;
 *   - a card's title bar dragged onto another tile: the two trade places
 *     (an accent edge marks the one it lands on);
 *   - the tile menu's Rotate split (side by side ⇄ one above the other)
 *     and Swap with sibling (tiledMenu);
 *   - opening a tile splits the focused tile's space — the active window
 *     (zorder.js) — else the last tile's; closing one gives its space to
 *     what shared it (tiledToggle, from the shell's _toggle).
 *
 * Floating windows stay floating over the tiles and are not in the tree;
 * the screen's headings and text (D192) are kept but not drawn. On a phone
 * one tile fills the pane, a strip of the tiles' names above it picks
 * which. The focused tile holds no place (D191): nothing here scrolls.
 */
import { html, css, nothing, repeat } from 'lit';
import { dragPointer } from '/vendor/bx-kit.js';
import { activeWindow } from './zorder.js';
import { screenMode } from './modes.js';
import { titleOf } from '/vendor/tile-meta.js';
import { normalise, layout, insert, setRatio, swap, rotate, swapSibling, parentOf, leaves, same } from './tiling.js';

const GUT = 4;       // half the gutter between two tiles
const SWITCH_H = 40; // a phone's tile switcher

// The order tiles go into a tree that lacks them: the canvas's reading
// order (y, then x), as Document mode reads its rows.
const reading = (a, b) => (a.y ?? 0) - (b.y ?? 0) || (a.x ?? 0) - (b.x ?? 0) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
export const tiledPaths = (tiles) => (tiles ?? []).filter((o) => o?.path && !o.float).sort(reading).map((o) => o.path);
const byPath = (a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
// what a tile calls itself (D194): its page's title, its manifest's, else its name
const nameOf = (c, p) => titleOf(p, (c.components ?? []).find((x) => x.path === p), c.frameFor?.(p)?.tileMeta);

// paneOf(canvas) → the tile area {x: 0, y: 0, w, h}: <main>'s content box
// below where the canvas starts — what sits above it in <main> (an org
// screen's bar) takes its own. Anything there taller than 40 % of the pane
// would leave the tiles a sliver: then the tiles keep the whole pane's
// height below it, and the page scrolls past it to them.
export function paneOf(c) {
  const main = c?.closest?.('main');
  if (!main) return c?._tpane ?? { x: 0, y: 0, w: 1200, h: 800 };
  const cs = getComputedStyle(main), px = (v) => parseFloat(v) || 0;
  const top = c.getBoundingClientRect().top - main.getBoundingClientRect().top + main.scrollTop;
  const w = main.clientWidth - px(cs.paddingLeft) - px(cs.paddingRight);
  const full = main.clientHeight - px(cs.paddingTop) - px(cs.paddingBottom), rest = main.clientHeight - top - px(cs.paddingBottom);
  return { x: 0, y: 0, w: Math.max(240, Math.floor(w)), h: Math.max(200, Math.floor(rest >= full * 0.6 ? rest : full)) };
}

// treeOf(screen-ish, pane) → the tree to draw and edit: the stored one
// fitted to the placed tiles.
export const treeOf = (tiles, arrange, pane) => normalise(arrange?.tiling, tiledPaths(tiles), pane);

// The pane follows the window and whatever sits above the canvas in <main>.
function watch(c) {
  if (c._tro !== undefined) return;
  const main = c.closest('main');
  if (!main || typeof ResizeObserver === 'undefined') { c._tro = null; return; }
  c._tro = new ResizeObserver(() => {
    if (c.mode !== 'tiled') return;
    const p = paneOf(c), q = c._tpane;
    if (!q || p.w !== q.w || p.h !== q.h) c.requestUpdate();
  });
  c._tro.observe(main);
  for (const el of main.children) if (el !== c && el.tagName !== 'SLOT') c._tro.observe(el);
}

// commit(c, fn): fn over the fitted tree, written back as the screen's
// arrange.tiling — nothing when it changes nothing.
function commit(c, fn) {
  const pane = c._tpane ?? paneOf(c), t0 = treeOf(c.tiles, c.arrange, pane);
  if (same(fn(t0), t0)) return;
  c._arrange((a) => ({ ...a, tiling: fn(treeOf(c.tiles, a, pane)) }));
}

// tiledView(c): the tiles. c is the <bx-canvas>; its _cardTemplate(o,
// 'tiled') draws each card (the same window chrome as on the grid).
export function tiledView(c) {
  watch(c);
  const pane = paneOf(c);
  c._tpane = pane;
  const placed = (c.tiles ?? []).filter((o) => o?.path && !o.float);
  if (!placed.length) return nothing;
  let tree = treeOf(placed, c.arrange, pane);
  if (c._tdiv) tree = setRatio(tree, c._tdiv.node, c._tdiv.ratio);
  const order = leaves(tree), td = c._tdrag, ed = c.canMutate && !c.mobile;
  // the phone: one tile, the switcher above it
  const front = c._front?.startsWith('tile:') ? c._front.slice(5) : '';
  const shown = c.mobile ? (order.includes(front) ? front : order[0]) : null;
  const top = c.mobile ? SWITCH_H : 0;
  // the gutters: laid out on a rect one half-gutter bigger all round, each
  // tile then inset by it — the outer edges meet the pane's
  const { rects, dividers } = c.mobile ? { rects: new Map(), dividers: [] }
    : layout(tree, { x: -GUT, y: -GUT, w: pane.w + 2 * GUT, h: pane.h + 2 * GUT });
  const box = (p) => {
    if (c.mobile) return p === shown ? { x: 0, y: top, w: pane.w, h: pane.h - top } : null;
    const r = rects.get(p);
    if (!r) return null;
    const x = Math.round(r.x + GUT), y = Math.round(r.y + GUT);
    return { x, y, w: Math.round(r.x + r.w - GUT) - x, h: Math.round(r.y + r.h - GUT) - y };
  };
  return html`
    <div class="tiled ${c.canMutate ? '' : 'ro'} ${c.mobile ? 'phone' : ''}" style="height:${pane.h}px"
         @contextmenu=${(e) => c._onContextMenu(e)}>
      ${repeat([...placed].sort(byPath), (o) => o.path, (o) => {
        const b = box(o.path);
        return html`<div class="ttile ${b ? '' : 'off'} ${td?.path === o.path ? 'dragging' : ''} ${td?.over === o.path ? 'drop' : ''}"
            data-path=${o.path} style=${b ? `left:${b.x}px; top:${b.y}px; width:${b.w}px; height:${b.h}px` : ''}>
          ${c._cardTemplate(o, 'tiled')}
        </div>`;
      })}
      ${ed ? dividers.map((d) => divider(c, d)) : nothing}
      ${c.mobile ? html`<div class="tswitch" role="tablist" aria-label="tiles on this screen">${order.map((p) => html`
        <button type="button" role="tab" class=${p === shown ? 'on' : ''} aria-selected=${p === shown ? 'true' : 'false'} title=${p}
          @click=${() => c._activate(p)}>${nameOf(c, p)}</button>`)}</div>` : nothing}
    </div>`;
}

// ---- a divider: drag to share the space anew, two clicks: half each ----
// Told apart here, not by dblclick: the drag shield takes the first click's
// events (as Document mode's handles do).
let lastClick = { node: null, at: 0 };
function divider(c, d) {
  const row = d.split === 'row', on = c._tdiv?.node === d.node;
  const style = row
    ? `left:${Math.round(d.x) - GUT}px; top:${Math.round(d.y + GUT)}px; width:${2 * GUT}px; height:${Math.round(d.h - 2 * GUT)}px`
    : `left:${Math.round(d.x + GUT)}px; top:${Math.round(d.y) - GUT}px; width:${Math.round(d.w - 2 * GUT)}px; height:${2 * GUT}px`;
  return html`<div class="tdiv ${d.split} ${on ? 'on' : ''}" data-node=${d.node} style=${style}
    title="drag to share the space · two clicks: half each" @pointerdown=${(e) => divStart(c, e, d)}></div>`;
}
function divStart(c, ev, d) {
  if (ev.button !== 0) return;
  ev.preventDefault(); ev.stopPropagation();
  const host = c.renderRoot.querySelector('.tiled')?.getBoundingClientRect();
  if (!host) return;
  const row = d.split === 'row', b = d.box, sx = ev.clientX, sy = ev.clientY;
  let ratio = null;
  dragPointer({
    cursor: row ? 'ew-resize' : 'ns-resize',
    onMove: (e) => {
      if (ratio == null && Math.hypot(e.clientX - sx, e.clientY - sy) < 3) return;
      ratio = row ? (e.clientX - host.left - b.x) / b.w : (e.clientY - host.top - b.y) / b.h;
      c._tdiv = { node: d.node, ratio };
    },
    onUp: () => {
      c._tdiv = null;
      if (ratio != null) { lastClick = { node: null, at: 0 }; commit(c, (t) => setRatio(t, d.node, ratio)); return; }
      const now = Date.now(), second = lastClick.node === d.node && now - lastClick.at < 450;
      lastClick = second ? { node: null, at: 0 } : { node: d.node, at: now };
      if (second) commit(c, (t) => setRatio(t, d.node, 0.5));
    },
  });
}

// ---- a card dragged by its title bar onto another tile: they swap ----
function overTile(c, x, y, path) {
  for (const el of c.renderRoot.querySelectorAll('.ttile[data-path]:not(.off)')) {
    if (el.dataset.path === path) continue;
    const r = el.getBoundingClientRect();
    if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return el.dataset.path;
  }
  return null;
}
export function tiledDragStart(c, ev, path) {
  if (c.mobile || !c.canMutate) return; // phones pick a tile; a shared screen in view mode stays as it is
  if (ev.button !== 0 || ev.target.closest('button, select, a')) return;
  ev.preventDefault();
  const sx = ev.clientX, sy = ev.clientY;
  let live = false;
  dragPointer({
    onMove: (e) => {
      if (!live && Math.hypot(e.clientX - sx, e.clientY - sy) < 6) return; // a click, not a drag
      live = true;
      c._tdrag = { path, over: overTile(c, e.clientX, e.clientY, path) };
    },
    onUp: () => {
      const d = c._tdrag;
      c._tdrag = null;
      if (d?.over) commit(c, (t) => swap(t, path, d.over));
    },
  });
}

// ---- the shell's ends (s is <bx-shell>) ----
const tiledScreen = (s) => screenMode(s._screen) === 'tiled';
const paneFor = (s) => s._canvas?._tpane ?? (s._canvas ? paneOf(s._canvas) : undefined);
function mutateTree(s, fn) {
  const pane = paneFor(s);
  s._mutateTiles((a) => ({ ...a, tiling: fn(treeOf(s._screen?.tiles, a, pane)) }), 'arrange');
}

// tiledToggle(s, path, open): the shell's _toggle opened or closed a tile
// on a Tiled screen — it splits the focused tile's space (the active
// window, else the last tile), or gives its space back. The tiles are
// already changed; the tree is fitted to the others first so the new tile
// goes where the focus is, not after the last one.
export function tiledToggle(s, path, open) {
  if (!tiledScreen(s) || !s._canMutate) return;
  const f = activeWindow(), focus = f?.startsWith('tile:') ? f.slice(5) : null, pane = paneFor(s);
  const others = tiledPaths(s._screen?.tiles).filter((p) => p !== path);
  s._mutateTiles((a) => {
    const t = normalise(a.tiling, others, pane);
    return { ...a, tiling: open ? insert(t, path, focus, pane) : t };
  }, 'arrange');
}

// tiledMenu(s) → (path) → the tile menu's Tiled lines (menus.js
// tiledItems), or null off a Tiled screen.
export function tiledMenu(s) {
  if (!tiledScreen(s)) return null;
  return (path) => {
    const ro = !s._canMutate || s._mobile;
    const alone = parentOf(treeOf(s._screen?.tiles, s._screen?.arrange, paneFor(s)), path) == null;
    const hint = ro ? 'view mode' : alone ? 'one tile' : '';
    return [
      { icon: 'tiling', label: 'Rotate split', hint, disabled: ro || alone, title: 'this tile and what shares its space: side by side, or one above the other',
        action: () => mutateTree(s, (t) => rotate(t, path)) },
      { icon: 'split', label: 'Swap with sibling', hint, disabled: ro || alone, title: 'this tile and what shares its space trade sides',
        action: () => mutateTree(s, (t) => swapSibling(t, path)) },
    ];
  };
}

// Base Two (D184): the windows sit flat as on the grid; a divider is the
// gutter between them, a grip under the pointer; the swap target wears an
// accent edge.
export const tiledCss = css`
    .tiled { position: relative; overflow: hidden; }
    .ttile { position: absolute; display: flex; }
    .ttile.off { display: none; }
    .ttile.dragging { opacity: 0.6; }
    .ttile.drop > .card { outline: 2px solid var(--bx-accent, #8C9BFF); outline-offset: -2px; }
    .tiled.ro .card .head, .tiled.phone .card .head { cursor: default; }
    .tdiv { position: absolute; z-index: 12; touch-action: none; }
    .tdiv.row { cursor: ew-resize; }
    .tdiv.col { cursor: ns-resize; }
    .tdiv::after { content: ''; position: absolute; background: var(--bx-border-strong, #666A7E); opacity: 0; }
    .tdiv.row::after { left: 3px; width: 2px; top: 50%; height: 32px; margin-top: -16px; }
    .tdiv.col::after { top: 3px; height: 2px; left: 50%; width: 32px; margin-left: -16px; }
    .tdiv:hover::after, .tdiv.on::after { opacity: 1; background: var(--bx-accent, #8C9BFF); }
    .tswitch {
      position: absolute; left: 0; right: 0; top: 0; height: ${SWITCH_H - 6}px; display: flex; gap: 4px; overflow-x: auto;
      scrollbar-width: none;
    }
    .tswitch button {
      flex: none; max-width: 40vw; padding: 0 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6);
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    }
    .tswitch button.on { color: var(--bx-text, #E9EAF0); border-color: var(--bx-window-border-active, #9396A4); }
`;

// The harness's handles (hack/ui-harness, through testApi()).
export const tiledTestApi = (s) => ({
  tiling: () => (tiledScreen(s) ? treeOf(s._screen?.tiles, s._screen?.arrange, paneFor(s)) : null),
});
