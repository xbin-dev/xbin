/**
 * <bx-canvas> — the shell's tile surface: the fixed snappable grid of cards
 * and the floating (unpinned) windows over it. Owns the pointer gestures on
 * them (grid drag/resize, float drag/raise/resize-commit, pin/unpin, the
 * touch long-press and the right-click that open menus) and the per-card
 * chrome (head buttons, the >_ terminal toggle, the ⇄ badge).
 *
 * The tiles array is a property; every geometry change comes back as one
 * `bx-tiles` event carrying the new array — the shell persists it (a shared
 * org screen mutates its draft, never the store). What needs the shell
 * stays with it and arrives as events: `bx-tile-menu` {at, path, anchor,
 * selection}, `bx-canvas-menu` {clientX, clientY}, `bx-admin-win` path,
 * `bx-toggle-tile` path. frameFor / frameOpen / frames / rectOf /
 * raiseFocusedFloat / togglePin are the shell's handles on the cards.
 */
import { LitElement, html, nothing, repeat } from 'lit';
import '/vendor/bx-frame.js';
import { clampBox, dragPointer, pathHas } from '/vendor/bx-kit.js';
import { GRID, GAP, MIN_W, MIN_H, snap, RUNTIME_COLOR, LongPress, selectedText, prBadge } from './shell-kit.js';
import { nextZ, raiseTo } from './zorder.js';
import { canvasCss, prbCss } from './shell-css.js';

export class BxCanvas extends LitElement {
  static properties = {
    tiles: { attribute: false },        // the active screen's tiles [{path, x, y, w, h, float?}]
    components: { attribute: false },   // /components — the runtime colour dot
    prs: { attribute: false },          // {path: open change proposals}
    canMutate: { attribute: false },    // layout changes allowed (personal screen, or an org draft)
    mobile: { attribute: false },       // narrow layout: stacked cards, sheets, long-press menus
    menuOpen: { attribute: false },     // the shell's menu is open (Android's post-long-press contextmenu is swallowed)
    canAdminTile: { attribute: false }, // (path) → boolean: whether the ⚙ button shows
    emptyText: { attribute: false },    // what an empty screen says
  };
  static styles = [canvasCss, prbCss];

  constructor() {
    super();
    this.tiles = []; this.components = []; this.prs = {};
    this.canMutate = true; this.mobile = false; this.menuOpen = false; this.emptyText = '';
    this._press = new LongPress();
    this._pending = new Map(); // path → layout to open once its card exists
  }

  _emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  _all() { return this.tiles ?? []; }
  // Every change goes out as the whole array; the shell owns persistence.
  _mutate(fn) { this._emit('bx-tiles', fn(this._all().map((t) => ({ ...t })))); }
  _setGeom(path, patch) {
    this._mutate((tiles) => tiles.map((o) => (o.path === path && !o.float ? { ...o, ...patch } : o)));
  }
  _setFloat(path, patch) {
    this._mutate((tiles) => tiles.map((o) => (o.path === path && o.float ? { ...o, float: { ...o.float, ...patch } } : o)));
  }
  _runtimeOf(path) { return (this.components ?? []).find((c) => c.path === path)?.runtime ?? ''; }

  // ---- the shell's handles ----
  frameFor(path) { return this.renderRoot.querySelector(`.card[data-path="${CSS.escape(path)}"] bx-frame`); }
  frames() { return [...this.renderRoot.querySelectorAll('bx-frame')]; }
  // Open a tile's pop-up (terminal / logs / source / proposals). Returns
  // false when the tile has no card yet: the caller opens it, and the call
  // runs once the card exists (see updated()).
  frameOpen(path, layout) {
    const fr = this.frameFor(path);
    if (fr) { fr.open?.(layout); return true; }
    this._pending.set(path, layout);
    return false;
  }
  updated() {
    if (!this._pending.size) return;
    for (const [p, l] of [...this._pending]) {
      const fr = this.frameFor(p);
      if (!fr) continue;
      this._pending.delete(p);
      fr.updateComplete?.then(() => fr.open?.(l));
    }
  }
  // The card's or window's on-screen rect (the tile admin popover opens beside it).
  rectOf(path) { return (this._gtile(path) ?? this._floatWin(path))?.getBoundingClientRect() ?? null; }

  // ---- context menus ----
  // A right-click on a card head opens the TILE menu, on the empty canvas
  // the CANVAS menu; inputs, links, the PR badge and a frame's pop-ups keep
  // the native menu, as does any selected shell text. Handled here and
  // stopped, so the shell's <main> handler never sees it twice.
  _onContextMenu(e) {
    e.stopPropagation();
    if (this.mobile && this.menuOpen) { e.preventDefault(); return; } // Android fires one after a long-press
    if (pathHas(e, 'input, textarea, select, a, [contenteditable]:not([contenteditable="false"]), .prb, bx-menu, bx-dialog')) return;
    if (pathHas(e, '.pop, bx-terminal, bx-code, bx-logs, bx-prs')) return;
    if (selectedText(this.renderRoot)) return;
    const card = e.target.closest('.card');
    if (card) { this._tileMenu(e, card.dataset.path); return; }
    if (pathHas(e, 'button, bx-frame')) return;
    e.preventDefault();
    this._emit('bx-canvas-menu', { clientX: e.clientX, clientY: e.clientY });
  }
  _tileMenu(e, path, anchorEl = null, selection = '') {
    e?.preventDefault?.(); e?.stopPropagation?.();
    this._emit('bx-tile-menu', {
      at: e ? { clientX: e.clientX, clientY: e.clientY } : null, path,
      anchor: anchorEl?.getBoundingClientRect?.() ?? null, selection,
    });
  }
  // Long-press on the empty canvas (touch): the canvas menu.
  _bgPress(e) {
    if (e.target.closest('.card, button, input, select, a, bx-frame')) return;
    this._press.start(e, () => this._emit('bx-canvas-menu', { clientX: e.clientX, clientY: e.clientY }), this.mobile);
  }

  // ---- grid drag + resize ----
  // Both manipulate the tile's DOM directly during the gesture (so its
  // <bx-frame> isn't re-rendered/reloaded mid-move) and commit snapped geometry
  // on release. The tile is a `.gtile` at (x, y) sized (w−GAP)×(h−GAP) — the GAP
  // is the gutter — so committed w/h add GAP back.
  _gtile(path) { return this.renderRoot.querySelector(`.gtile[data-path="${CSS.escape(path)}"]`); }

  _gridDragStart(ev, path) {
    if (this.mobile) return; // tiles are stacked (no free grid) on mobile
    if (!this.canMutate) return; // shared screen in view mode (D55)
    if (ev.button !== 0 || ev.target.closest('button, select, .rz')) return;
    ev.preventDefault();
    const el = this._gtile(path);
    if (!el) return;
    el.classList.add('dragging');
    const dx = ev.clientX - el.offsetLeft, dy = ev.clientY - el.offsetTop;
    dragPointer({
      onMove: (e) => {
        el.style.left = snap(Math.max(0, e.clientX - dx)) + 'px';
        el.style.top = snap(Math.max(0, e.clientY - dy)) + 'px';
      },
      onUp: () => {
        el.classList.remove('dragging');
        this._setGeom(path, { x: el.offsetLeft, y: el.offsetTop });
      },
    });
  }

  _gridResizeStart(ev, path) {
    if (this.mobile || ev.button !== 0) return;
    if (!this.canMutate) return; // shared screen in view mode (D55)
    ev.preventDefault(); ev.stopPropagation();
    const el = this._gtile(path);
    if (!el) return;
    const sx = ev.clientX, sy = ev.clientY;
    const w0 = el.offsetWidth + GAP, h0 = el.offsetHeight + GAP; // full cell size
    dragPointer({
      cursor: 'nwse-resize',
      onMove: (e) => {
        el.style.width = (snap(Math.max(MIN_W, w0 + (e.clientX - sx))) - GAP) + 'px';
        el.style.height = (snap(Math.max(MIN_H, h0 + (e.clientY - sy))) - GAP) + 'px';
      },
      onUp: () => this._setGeom(path, { w: el.offsetWidth + GAP, h: el.offsetHeight + GAP }),
    });
  }

  _gridCard(o) {
    return html`
      <div class="gtile" data-path=${o.path}
           style="left:${o.x}px; top:${o.y}px; width:${o.w - GAP}px; height:${o.h - GAP}px;">
        ${this._cardTemplate(o, 'grid')}
        <div class="rz" title="drag to resize" @pointerdown=${(e) => this._gridResizeStart(e, o.path)}></div>
      </div>`;
  }

  // Content bounds so the (absolute-positioned) canvas scrolls to fit its tiles,
  // floored to the visible pane so the dot field fills it even on a near-empty
  // screen. main's padding (14px) and the grants bar sit above the canvas.
  _gridExtent() {
    const g = this._all().filter((o) => !o.float);
    const main = this.closest('main');
    const grants = main?.querySelector('.grants');
    const vw = main ? main.clientWidth - 28 : 0;
    const vh = main ? main.clientHeight - 28 - (grants?.offsetHeight ?? 0) : 0;
    return {
      w: Math.max(vw, g.reduce((m, o) => Math.max(m, o.x + o.w), 0) + GRID),
      h: Math.max(vh, g.reduce((m, o) => Math.max(m, o.y + o.h), 0) + GRID),
    };
  }

  // Open/close the terminal of the card's own frame (the header >_ button —
  // integrated here so tiles don't need the tiny corner button).
  _cardTerm(e) {
    e.stopPropagation();
    e.currentTarget.closest('.card')?.querySelector('bx-frame')?.toggleTerminal?.();
  }

  // kind: 'grid' (on the snappable grid) | 'float' (a free-floating window).
  // Both are fixed-size: the frame fills a fixed body and scrolls inside.
  _cardTemplate(o, kind = 'grid') {
    const floating = kind === 'float';
    const frame = html`<bx-frame src=${o.path} no-edit height="100%"></bx-frame>`;
    return html`
      <div class="card" data-path=${o.path}
           @bx-contextmenu=${(e) => { e.stopPropagation(); this._tileMenu({ clientX: e.detail.x, clientY: e.detail.y }, o.path, null, e.detail.selection || ''); }}>
        <div class="head"
             @pointerdown=${(e) => { this._press.start(e, () => this._tileMenu(null, o.path), this.mobile); (floating ? this._floatDragStart(e, o.path) : this._gridDragStart(e, o.path)); }}
             @pointermove=${(e) => this._press.move(e)}
             @pointerup=${() => this._press.cancel()} @pointercancel=${() => this._press.cancel()} @pointerleave=${() => this._press.cancel()}>
          <span class="c" style="background:${RUNTIME_COLOR[this._runtimeOf(o.path)] ?? RUNTIME_COLOR['']}"></span>
          <span class="t">${o.path}</span>
          ${prBadge(this.prs?.[o.path], () => this.frameOpen(o.path, 'prs'))}
          <span class="spacer"></span>
          <button class="term" title="terminal on ${o.path}"
                  @pointerdown=${(e) => e.stopPropagation()}
                  @click=${(e) => this._cardTerm(e)}>&gt;_</button>
          ${!this.mobile && this.canAdminTile?.(o.path) ? html`<button title="tile admin (lifecycle · access · runtime · vault · grants · interfaces · backup · cron)"
                  @pointerdown=${(e) => e.stopPropagation()}
                  @click=${(e) => { e.stopPropagation(); this._emit('bx-admin-win', o.path); }}>⚙</button>` : nothing}
          ${!this.mobile && this.canMutate ? html`<button title=${floating ? 'pin back onto the grid' : 'unpin into a floating window'}
                  @click=${() => this.togglePin(o.path)}>${floating ? '▣' : '⧉'}</button>` : nothing}
          <button title="tile menu (terminal · logs · source · proposals · admin)"
                  @pointerdown=${(e) => e.stopPropagation()}
                  @click=${(e) => this._tileMenu(e, o.path, e.currentTarget)}>⋯</button>
          ${!this.mobile && this.canMutate ? html`<button title="close" @click=${() => this._emit('bx-toggle-tile', o.path)}>✕</button>` : nothing}
        </div>
        <div class="cbody">${frame}</div>
      </div>`;
  }

  // ---- floating (unpinned) windows ----
  // A tile with a `float:{x,y,w,h,z}` is rendered as a viewport-fixed window
  // instead of on the grid; the geometry is part of the tile, so it persists in
  // the saved layout. Pinning/unpinning re-creates the tile's <bx-frame> (moving
  // between two DOM containers) — a brief reload, but any open terminal on it
  // reattaches via bx-frame's session persistence.
  _floatTemplate(o) {
    // Rendered clamped to the CURRENT viewport (the saved geometry may come
    // from a bigger monitor); dragging commits the on-screen position.
    const f = this.mobile ? o.float : clampBox(o.float, { minW: MIN_W, minH: MIN_H });
    return html`
      <div class="float" data-path=${o.path}
           style="left:${f.x}px; top:${f.y}px; width:${f.w}px; height:${f.h}px; z-index:${f.z ?? 100};"
           @contextmenu=${(e) => this._onContextMenu(e)}
           @pointerdown=${() => this._floatFront(o.path)}
           @pointerup=${(e) => this._floatCommit(e, o.path)}>
        ${this._cardTemplate(o, 'float')}
      </div>`;
  }

  togglePin(path) {
    // Read the current on-screen rect before the mutation re-renders.
    const init = this._initialFloat(path);
    this._mutate((tiles) => tiles.map((o) => {
      if (o.path !== path) return o;
      if (o.float) { const { float, ...rest } = o; return rest; } // pin back to its column
      return { ...o, float: init };                                // unpin → floating window
    }));
  }

  _initialFloat(path) {
    const el = this.renderRoot.querySelector(`.card[data-path="${CSS.escape(path)}"]`);
    const r = el?.getBoundingClientRect();
    const w = Math.round(Math.min(r?.width || 480, window.innerWidth - 16));
    const h = Math.round(Math.min(r?.height || 340, 520, window.innerHeight - 16));
    const x = Math.max(8, Math.min(Math.round((r?.left ?? 120) + 28), window.innerWidth - w - 8));
    const y = Math.max(8, Math.min(Math.round((r?.top ?? 90) + 20), window.innerHeight - h - 8));
    return { x, y, w, h, z: nextZ() };
  }

  _floatWin(path) { return this.renderRoot.querySelector(`.float[data-path="${CSS.escape(path)}"]`); }

  // Raise a floating window to the top — but only if it isn't already there, so
  // repeatedly clicking the front window doesn't churn the layout. z is part of
  // the tile, so the stacking order persists.
  _floatFront(path) {
    const floats = this._all().filter((o) => o.float);
    if (floats.length < 2) return;
    const o = floats.find((t) => t.path === path);
    if (!o) return;
    const maxZ = Math.max(...floats.map((t) => t.float.z ?? 100));
    if ((o.float.z ?? 100) >= maxZ) return; // already on top
    this._setFloat(path, { z: raiseTo(maxZ + 1) });
  }

  // A click inside a tile's <iframe> focuses it and blurs the top window (the
  // iframe swallows the pointerdown, so .float's own handler can't fire). Walk
  // the shadow roots to the focused iframe and raise its floating window, so
  // clicking anywhere in a window — not just its title bar — brings it forward.
  // A window blur with an iframe focused = the person clicked into that
  // tile (the click itself never reaches us), so its float comes to the
  // front — unless the frame is mid-reload: a reloaded document that focuses
  // an input would otherwise hoist its tile over the terminal someone is
  // typing in (bx-frame also hands that stolen focus back).
  raiseFocusedFloat() {
    setTimeout(() => {
      let el = document.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      if (el?.tagName !== 'IFRAME') return;
      const host = el.getRootNode()?.host;
      if (host?.reloading && !host.hovered) return; // a reload's focus grab, not a click
      const win = host?.closest?.('.float');
      if (win) this._floatFront(win.dataset.path);
    }, 0);
  }

  // Commit a resize (via the CSS resize handle) back into the tile; skip clicks
  // that didn't change the size, so buttons don't churn the layout.
  _floatCommit(e, path) {
    const win = e.currentTarget;
    const o = this._all().find((t) => t.path === path);
    if (!o?.float) return;
    if (win.offsetWidth === o.float.w && win.offsetHeight === o.float.h) return;
    this._setFloat(path, { w: win.offsetWidth, h: win.offsetHeight });
  }

  _floatDragStart(ev, path) {
    if (this.mobile) return; // floats are full-screen sheets on mobile
    if (ev.button !== 0 || ev.target.closest('button, select')) return;
    ev.preventDefault();
    this._floatFront(path);
    const win = this._floatWin(path);
    if (!win) return;
    const dx = ev.clientX - win.offsetLeft, dy = ev.clientY - win.offsetTop;
    dragPointer({
      onMove: (e) => {
        const x = Math.max(-win.offsetWidth + 60, Math.min(e.clientX - dx, window.innerWidth - 40));
        const y = Math.max(0, Math.min(e.clientY - dy, window.innerHeight - 24));
        win.style.left = x + 'px'; win.style.top = y + 'px';
      },
      onUp: () => this._setFloat(path, { x: win.offsetLeft, y: win.offsetTop }),
    });
  }

  render() {
    const grid = this._all().filter((o) => !o.float);
    const floats = this._all().filter((o) => o.float);
    const ext = this._gridExtent();
    return html`
      <div class="canvas ${this.canMutate ? '' : 'ro'}" style="min-height:${ext.h}px; min-width:${ext.w}px"
           @contextmenu=${(e) => this._onContextMenu(e)}
           @pointerdown=${(e) => this._bgPress(e)}
           @pointermove=${(e) => this._press.move(e)}
           @pointerup=${() => this._press.cancel()} @pointercancel=${() => this._press.cancel()}>
        ${repeat(grid, (o) => o.path, (o) => this._gridCard(o))}
      </div>
      ${grid.length === 0 && floats.length === 0 ? html`<div class="empty">${this.emptyText}</div>` : nothing}
      ${repeat(floats, (o) => o.path, (o) => this._floatTemplate(o))}`;
  }
}

customElements.define('bx-canvas', BxCanvas);
