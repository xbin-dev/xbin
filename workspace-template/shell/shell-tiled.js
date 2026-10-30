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
 *   - a card's title bar dragged onto another tile (D205): its centre —
 *     the two trade places; one of the four trapezoids along its edges —
 *     the tile takes that half of the other's space (tiling.js place();
 *     the space it left goes to what shared it). Dragged to a band along
 *     the screen's edge, it takes that half of the screen. While dragging,
 *     only the outcome is drawn: where the tile lands, and where the tile
 *     under it goes (squeezed into the other half, grown when the two
 *     shared a split, or moved by a swap);
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
import { normalise, layout, insert, setRatio, rotate, swapSibling, parentOf, leaves, same, zoneOf, drop } from './tiling.js';

const GUT = 4;       // half the gutter between two tiles
const SWITCH_H = 40; // a phone's tile switcher
const EDGE = 26;     // the band along the screen's edge a tile is dropped on to take that half of the screen (D205)

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

// boxesOf(tree, pane) → Map(path → {x, y, w, h}): each tile's window in
// the pane, whole pixels. The gutters: the tree is laid out on a rect one
// half-gutter bigger all round (outer) and each tile inset by it, so the
// outer edges meet the pane's.
const outer = (pane) => ({ x: -GUT, y: -GUT, w: pane.w + 2 * GUT, h: pane.h + 2 * GUT });
function boxesOf(tree, pane) {
  const out = new Map();
  for (const [p, r] of layout(tree, outer(pane)).rects) {
    const x = Math.round(r.x + GUT), y = Math.round(r.y + GUT);
    out.set(p, { x, y, w: Math.round(r.x + r.w - GUT) - x, h: Math.round(r.y + r.h - GUT) - y });
  }
  return out;
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
  const order = leaves(tree), td = c._tdrag, ed = c.canMutate && !c.mobile, target = td?.drop && !td.drop.screen ? td.drop.target : null;
  // the phone: one tile, the switcher above it
  const front = c._front?.startsWith('tile:') ? c._front.slice(5) : '';
  const shown = c.mobile ? (order.includes(front) ? front : order[0]) : null;
  const top = c.mobile ? SWITCH_H : 0;
  const boxes = c.mobile ? new Map() : boxesOf(tree, pane);
  const dividers = c.mobile ? [] : layout(tree, outer(pane)).dividers;
  const box = (p) => (c.mobile ? (p === shown ? { x: 0, y: top, w: pane.w, h: pane.h - top } : null) : boxes.get(p) ?? null);
  return html`
    <div class="tiled ${c.canMutate ? '' : 'ro'} ${c.mobile ? 'phone' : ''}" style="height:${pane.h}px"
         @contextmenu=${(e) => c._onContextMenu(e)}>
      ${repeat([...placed].sort(byPath), (o) => o.path, (o) => {
        const b = box(o.path);
        return html`<div class="ttile ${b ? '' : 'off'} ${td?.path === o.path ? 'dragging' : ''} ${target === o.path ? 'drop' : ''}"
            data-path=${o.path} style=${b ? `left:${b.x}px; top:${b.y}px; width:${b.w}px; height:${b.h}px` : ''}>
          ${c._cardTemplate(o, 'tiled')}
        </div>`;
      })}
      ${ed ? dividers.map((d) => divider(c, d)) : nothing}
      ${td && !c.mobile ? landing(c, tree, pane, td) : nothing}
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

// ---- a card dragged by its title bar onto another tile, or an edge ----
// dropAt(c, x, y, path) → where a drop at the viewport point (x, y) puts
// the tile: {screen: true, zone} in the band along an edge of the screen
// (on it or just past it), {target, zone} over another tile (tiling.js
// zoneOf: 'swap' or a side), else null — over the tile itself, or a
// screen of one tile.
function dropAt(c, x, y, path) {
  const host = c.renderRoot.querySelector('.tiled')?.getBoundingClientRect(), pane = c._tpane;
  if (!host || !pane) return null;
  const tree = treeOf(c.tiles, c.arrange, pane);
  if (leaves(tree).length < 2) return null;
  const px = x - host.left, py = y - host.top;
  if (px >= -EDGE && py >= -EDGE && px <= pane.w + EDGE && py <= pane.h + EDGE) {
    const z = px < EDGE ? 'left' : px > pane.w - EDGE ? 'right' : py < EDGE ? 'top' : py > pane.h - EDGE ? 'bottom' : null;
    if (z) return { screen: true, zone: z };
  }
  for (const [p, r] of boxesOf(tree, pane)) {
    if (p !== path && px >= r.x && px <= r.x + r.w && py >= r.y && py <= r.y + r.h) return { target: p, zone: zoneOf(r, px, py) };
  }
  return null;
}
const sameDrop = (a, b) => a === b || (!!a && !!b && a.screen === b.screen && a.target === b.target && a.zone === b.zone);

export function tiledDragStart(c, ev, path) {
  if (c.mobile || !c.canMutate) return; // phones pick a tile; a shared screen in view mode stays as it is
  if (ev.button !== 0 || ev.target.closest('button, select, a')) return;
  ev.preventDefault();
  const sx = ev.clientX, sy = ev.clientY;
  let live = false, cancelled = false;
  const esc = (k) => { if (k.key === 'Escape') { k.preventDefault(); k.stopPropagation(); cancelled = true; end(); } }; // Escape: nothing moves
  const end = dragPointer({
    onMove: (e) => {
      if (!live && Math.hypot(e.clientX - sx, e.clientY - sy) < 6) return; // a click, not a drag
      if (!live) { live = true; window.addEventListener('keydown', esc, true); }
      const d = dropAt(c, e.clientX, e.clientY, path);
      // a new outcome redraws; the pointer's chip just follows
      if (!c._tdrag || !sameDrop(c._tdrag.drop, d)) c._tdrag = { path, drop: d, x: e.clientX, y: e.clientY };
      const chip = c.renderRoot.querySelector('.tchip');
      if (chip) { chip.style.left = `${e.clientX}px`; chip.style.top = `${e.clientY}px`; }
    },
    onUp: () => {
      window.removeEventListener('keydown', esc, true);
      const d = c._tdrag?.drop;
      c._tdrag = null;
      if (d && !cancelled) commit(c, (t) => drop(t, path, d));
    },
  });
}

// landing(c, tree, pane, td): what a drop would do, drawn from the tree it
// would make — where the dragged tile lands, filled; where the tile under
// it goes, dashed: squeezed into the other half, grown when the two shared
// a split (the space the dragged tile leaves goes to it first), moved by a
// swap. A screen-edge drop draws only the landing spot. And a chip with
// the tile's name at the pointer.
const area = (r) => r.w * r.h;
function landing(c, tree, pane, td) {
  const d = td.drop, me = nameOf(c, td.path);
  const chip = html`<div class="tchip" style="left:${td.x}px; top:${td.y}px">${me}</div>`;
  const after = d ? boxesOf(drop(tree, td.path, d), pane) : null, at = after?.get(td.path);
  if (!at) return chip;
  const ghost = (cls, b, text) => html`<div class="tghost ${cls}" style="left:${b.x}px; top:${b.y}px; width:${b.w}px; height:${b.h}px"><span>${text}</span></div>`;
  if (d.screen) return [ghost('land', at, `${me} · ${d.zone} half of the screen`), chip];
  const was = boxesOf(tree, pane).get(d.target), now = after.get(d.target);
  const verb = d.zone === 'swap' || !was || !now ? 'moves here'
    : area(now) > area(was) * 1.02 ? 'grows to here' : area(now) < area(was) * 0.98 ? 'squeezes here' : 'moves here';
  return [ghost('land', at, `${me} lands here`), now ? ghost('other', now, `${nameOf(c, d.target)} ${verb}`) : nothing, chip];
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
// gutter between them, a grip under the pointer. A drag (D205): the tile
// under the pointer wears an accent edge, the landing spot an accent fill,
// where that tile goes a dashed edge, the dragged tile's name an accent
// chip at the pointer.
export const tiledCss = css`
    .tiled { position: relative; overflow: hidden; }
    .ttile { position: absolute; display: flex; }
    .ttile.off { display: none; }
    .ttile.dragging { opacity: 0.6; }
    .ttile.drop > .card { outline: 2px solid var(--bx-accent, #8C9BFF); outline-offset: -2px; }
    .tiled.ro .card .head, .tiled.phone .card .head { cursor: default; }
    .tghost {
      position: absolute; z-index: 14; pointer-events: none; display: flex; justify-content: center; align-items: flex-start;
      padding-top: 40px; box-sizing: border-box; overflow: hidden; border-radius: var(--bx-radius, 2px);
      border: 2px solid var(--bx-accent, #8C9BFF); background: color-mix(in srgb, var(--bx-accent, #8C9BFF) 24%, transparent);
      font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); font-weight: 600; color: var(--bx-text, #E9EAF0);
    }
    .tghost.other { border-style: dashed; background: color-mix(in srgb, var(--bx-panel, #1F2028) 55%, transparent); }
    .tghost span {
      max-width: calc(100% - 16px); padding: 2px 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      background: var(--bx-panel, #1F2028); border-radius: var(--bx-radius, 2px);
    }
    .tchip {
      position: fixed; z-index: 15; pointer-events: none; transform: translate(12px, 14px); max-width: 240px;
      padding: 3px 10px; border-radius: var(--bx-radius, 2px); overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif); font-weight: 600;
      background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12);
    }
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
