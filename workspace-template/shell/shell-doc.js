/**
 * shell-doc.js — Document mode's view (D187): a screen as a scrolling page
 * of rows, one tile per row or 2 or 4 side by side, each tile as tall as
 * its content; and the top bar that gets out of the way while reading.
 *
 * <bx-canvas> renders docView(this) instead of its grid when its `mode` is
 * 'doc' (floats stay floating windows). The rows come from doc-layout.js;
 * every cell sits on one 4-column CSS grid (a 1-wide row's tile spans it,
 * centred at most 1100 px; a 2-wide row's tiles span two columns each; a
 * 4-wide row's one) and the cells keep a stable DOM order — by path — with
 * the grid placing them, so rearranging never moves an iframe in the DOM
 * (which would reload the tile). Below 820px every row is one column, in
 * reading order.
 *
 * A tile grows to fit: its <bx-frame> has no height, so the frame takes the
 * height its document reports (xbin:resize; bx-frame's auto-height). A tile
 * whose document is pinned to the viewport (a terminal, a chat) reports the
 * frame's own height and settles at the initial 480px; the handle under
 * every card sets a fixed height (`doc.h`), and a double-click on it — or
 * the tile menu's Row ▸ Fit height to content — lets it fit again.
 *
 * Rearranging: drag a card by its title bar. Over the left or right quarter
 * of another card it joins that card's row, beside it (a 1-wide row
 * becomes 2, a full 2-wide row 4; a full 4-wide row pushes its last tile
 * down); over the middle it becomes a row of its own above or below that
 * card's row; below the last row, the last row. An accent bar marks where.
 * The tile menu's Row submenu does the same from the keyboard.
 */
import { html, nothing, repeat } from 'lit';
import { dragPointer } from '/vendor/bx-kit.js';
import { docRows, moveTile, setHeight } from './doc-layout.js';
import { screenMode } from './shell-tabs.js';

const MIN_DOC_H = 96;

// The cells, with their grid placement: row r (1-based), first column c,
// span; ord, the tile's place in reading order (the phone layout).
function cells(tiles) {
  const out = [];
  let ord = 0;
  docRows(tiles).forEach((r, ri) => {
    const span = 4 / r.cols;
    r.tiles.forEach((t, ci) => out.push({ t, row: ri, col: ci, cols: r.cols, n: r.tiles.length, style: `--row:${ri + 1}; --col:${ci * span + 1}; --span:${span}; --ord:${++ord}` }));
  });
  return out.sort((a, b) => (a.t.path < b.t.path ? -1 : a.t.path > b.t.path ? 1 : 0));
}

// docView(c): the page. c is the <bx-canvas>; its _cardTemplate(o, 'doc')
// draws each card (the same window chrome as on the canvas).
export function docView(c) {
  const list = cells(c.tiles ?? []), dd = c._ddrag, dh = c._dh;
  const ro = !c.canMutate || c.mobile;
  return html`
    <div class="doc ${c.canMutate ? '' : 'ro'}" @contextmenu=${(e) => c._onContextMenu(e)}>
      ${repeat(list, (x) => x.t.path, (x) => html`
        <div class="dcell ${x.cols === 1 ? 'one' : ''} ${dd?.path === x.t.path ? 'dragging' : ''}" data-path=${x.t.path}
             data-row=${x.row} data-col=${x.col} data-cols=${x.cols} data-n=${x.n} style=${x.style}>
          ${c._cardTemplate(dh?.path === x.t.path ? { ...x.t, doc: { ...x.t.doc, h: dh.h } } : x.t, 'doc')}
          ${ro ? nothing : html`<div class="dh" title="drag to set a fixed height · double-click: fit the content again"
            @pointerdown=${(e) => heightStart(c, e, x.t.path)}></div>`}
        </div>`)}
    </div>
    ${dd?.mark ? html`<div class="dmark ${dd.mark.v ? 'v' : 'h'}" style="left:${dd.mark.x}px; top:${dd.mark.y}px; ${dd.mark.v ? `height:${dd.mark.len}px` : `width:${dd.mark.len}px`}"></div>` : nothing}`;
}

// The frame a doc card holds: no height — it grows with its document —
// unless the tile has a fixed one.
export function docFrame(o, shown) {
  const h = Number(o.doc?.h) > 0 ? `${Math.round(o.doc.h)}px` : nothing;
  return html`<bx-frame src=${o.path} deployment=${shown || nothing} no-edit height=${h}></bx-frame>`;
}

// ---- dragging a card to another row or beside another tile ----
const rectOf = (el) => el.getBoundingClientRect();
// dropAt(c, x, y, path) → {move, mark} | null: the drop the pointer names
// (move: moveTile's target; mark: the accent bar, viewport px).
function dropAt(c, x, y, path) {
  const els = [...c.renderRoot.querySelectorAll('.dcell[data-path]')];
  if (!els.length) return null;
  const doc = c.renderRoot.querySelector('.doc'), dr = rectOf(doc);
  const rows = new Map(); // row → its extent
  for (const el of els) {
    const r = rectOf(el), i = Number(el.dataset.row);
    const e = rows.get(i) ?? { top: Infinity, bottom: -Infinity, n: Number(el.dataset.n), cols: Number(el.dataset.cols) };
    e.top = Math.min(e.top, r.top); e.bottom = Math.max(e.bottom, r.bottom);
    rows.set(i, e);
  }
  const across = (top) => ({ v: false, x: dr.left, y: top - 7, len: dr.width });
  for (const el of els) {
    const r = rectOf(el);
    if (x < r.left || x > r.right || y < r.top || y > r.bottom) continue;
    if (el.dataset.path === path) return null; // over itself: nothing changes
    const row = Number(el.dataset.row), col = Number(el.dataset.col), rx = (x - r.left) / r.width, ext = rows.get(row);
    if (rx < 0.25) return { move: { row, col }, mark: { v: true, x: r.left - 7, y: r.top, len: r.height } };
    if (rx > 0.75) return { move: { row, col: col + 1 }, mark: { v: true, x: r.right + 5, y: r.top, len: r.height } };
    return (y - r.top) / r.height < 0.5
      ? { move: { row, insert: true }, mark: across(ext.top) }
      : { move: { row: row + 1, insert: true }, mark: across(ext.bottom + 14) };
  }
  // in a row's band but beside its cards: the empty slot of a row that isn't full
  for (const [row, e] of rows) {
    if (y >= e.top && y <= e.bottom && e.n < e.cols) {
      const last = els.filter((el) => Number(el.dataset.row) === row).map(rectOf).sort((a, b) => b.right - a.right)[0];
      return { move: { row, col: e.n }, mark: { v: true, x: last.right + 5, y: e.top, len: e.bottom - e.top } };
    }
  }
  const lastRow = Math.max(...rows.keys()), le = rows.get(lastRow);
  if (y > le.bottom) return { move: { row: lastRow + 1, insert: true }, mark: across(le.bottom + 14) };
  if (y < rows.get(0)?.top) return { move: { row: 0, insert: true }, mark: across(rows.get(0).top) };
  return null;
}

// The page scrolls while a drag nears the pane's top or bottom edge.
function edgeScroll(c, y) {
  const main = c.closest('main');
  if (!main) return;
  const r = main.getBoundingClientRect();
  if (y < r.top + 40) main.scrollTop -= 16;
  else if (y > r.bottom - 40) main.scrollTop += 16;
}

export function docDragStart(c, ev, path) {
  if (c.mobile || !c.canMutate) return; // phones read; a shared screen in view mode stays as it is
  if (ev.button !== 0 || ev.target.closest('button, select, a')) return;
  ev.preventDefault();
  const sx = ev.clientX, sy = ev.clientY;
  let live = false;
  dragPointer({
    onMove: (e) => {
      if (!live && Math.hypot(e.clientX - sx, e.clientY - sy) < 6) return; // a click, not a drag
      live = true;
      edgeScroll(c, e.clientY);
      const at = dropAt(c, e.clientX, e.clientY, path);
      c._ddrag = { path, move: at?.move ?? null, mark: at?.mark ?? null };
    },
    onUp: () => {
      const d = c._ddrag;
      c._ddrag = null;
      if (d?.move) c._mutate((tiles) => moveTile(tiles, path, d.move));
    },
  });
}

// ---- the fixed-height handle ----
// A drag sets the height; two clicks in a row (no drag) fit the content
// again. Told apart here, not by dblclick: the drag shield takes the
// first click's events, so a dblclick never reaches the handle.
let lastClick = { path: '', at: 0 };
function heightStart(c, ev, path) {
  if (ev.button !== 0) return;
  ev.preventDefault(); ev.stopPropagation();
  const fr = c.frameFor(path);
  const h0 = fr?.getBoundingClientRect().height || 480, sy = ev.clientY;
  let h = h0;
  dragPointer({
    cursor: 'ns-resize',
    onMove: (e) => { h = Math.max(MIN_DOC_H, Math.round(h0 + e.clientY - sy)); c._dh = { path, h }; },
    onUp: () => {
      c._dh = null;
      if (Math.abs(h - h0) >= 2) { lastClick = { path: '', at: 0 }; c._mutate((tiles) => setHeight(tiles, path, h)); return; }
      const now = Date.now(), second = lastClick.path === path && now - lastClick.at < 450;
      lastClick = second ? { path: '', at: 0 } : { path, at: now };
      if (second) c._mutate((tiles) => setHeight(tiles, path, 0));
    },
  });
}

// ---- the top bar in Document mode ----
// A reactive controller on <bx-shell>: on a Document-mode screen (wider
// than 820px) the bar is fixed over the page and slides away while
// reading. It shows when the pointer comes within 8px of the top edge,
// when the page scrolls up, while focus is inside it and while a menu is
// open; it goes when the page scrolls down. The shell renders the bar's
// classes from `on` (docmode) and `hidden` (away); the motion is CSS
// (--bx-dur-panel; none under reduced motion).
export class TopReveal {
  constructor(host) {
    this.host = host; this.on = false; this.hidden = false; this._y = 0; this._main = null;
    this._scroll = () => {
      const y = this._main?.scrollTop ?? 0;
      if (y > this._y + 4) this._set(true);
      else if (y < this._y - 4) this._set(false);
      this._y = y;
    };
    this._move = (e) => { if (this.on && this.hidden && e.clientY <= 8) this._set(false); };
    this._focus = (e) => { if (this.on && e.composedPath().some((el) => el.classList?.contains('top'))) this._set(false); };
    host.addController(this);
  }
  hostConnected() {
    window.addEventListener('pointermove', this._move, { passive: true });
    this.host.renderRoot?.addEventListener?.('focusin', this._focus);
  }
  hostDisconnected() {
    window.removeEventListener('pointermove', this._move);
    this.host.renderRoot?.removeEventListener?.('focusin', this._focus);
    this._main?.removeEventListener('scroll', this._scroll);
    this._main = null;
  }
  hostUpdated() {
    const h = this.host;
    const main = h.renderRoot?.querySelector('main') ?? null;
    if (main !== this._main) {
      this._main?.removeEventListener('scroll', this._scroll);
      this._main = main;
      main?.addEventListener('scroll', this._scroll, { passive: true });
      this._y = main?.scrollTop ?? 0;
    }
    const on = !h._mobile && screenMode(h._screen) === 'doc';
    if (on !== this.on) {
      this.on = on; this.hidden = on; // entering Document mode: the bar starts out of the way
      h.requestUpdate();
    } else if (this.hidden && this._held()) {
      this.hidden = false;
      h.requestUpdate();
    }
  }
  // a menu open, or focus inside the bar, holds it out
  _held() {
    const h = this.host;
    return !!(h._settingsOpen || h._menu) || !!h.renderRoot?.querySelector('.top:focus-within');
  }
  _set(hidden) {
    const v = this.on && hidden && !this._held();
    if (v === this.hidden) return;
    this.hidden = v;
    this.host.requestUpdate();
  }
  reveal() { this._set(false); }
  // While the bar is away, an 8px strip along the top edge brings it back
  // when the pointer reaches it — over a tile, the pointer's moves go to
  // the tile's document, never to this page.
  edge() {
    return this.on && this.hidden ? html`<div class="topedge" aria-hidden="true" @pointerenter=${() => this.reveal()}></div>` : nothing;
  }
}
