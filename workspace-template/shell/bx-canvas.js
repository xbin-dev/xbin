/**
 * <bx-canvas> — the shell's tile surface: the fixed snappable grid of cards
 * and the floating (unpinned) windows over it. Owns the pointer gestures on
 * them (grid drag/resize, float drag/raise/resize-commit, pin/unpin, the
 * touch long-press and the right-click that open menus) and the per-card
 * chrome (head buttons, the >_ terminal toggle, the ⇄ and ⇈ badges, the
 * deployment a window shows: ⇈ picks it, a +name tag says so; and on
 * partitioned tiles the marker, the chip saying whose partition the window
 * shows, and a pending switch's overlay, where a tile manager keeps the
 * mode or switches it).
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
import '/vendor/bx-menu.js';
import '/vendor/bx-dialog.js';
import { clampBox, dragPointer, pathHas } from '/vendor/bx-kit.js';
import { GRID, GAP, MIN_W, MIN_H, snap, RUNTIME_COLOR, LongPress, selectedText, prBadge,
  followDeployments, onDeployChange, wantDeployState, deployState, deployIcon, deployBadge, partitionMark, chipTag } from './shell-kit.js';
import { shownDeployment, deployMenu } from './menus.js';
import { pushLayout } from './grid-layout.js';
import { nextZ, raiseTo } from './zorder.js';
import { canvasCss, prbCss, partCss } from './shell-css.js';
import { partitionView, requestKey, pruneDecisions, pendingText, switchLabel, modeName, modeBody, postMode, errorText,
  switchSpec, switchResolve, staleRefusal, deletesNothing, keptText, switchedText, whoDecides, noteText,
  partitionChip } from './partition-mode.js';

export class BxCanvas extends LitElement {
  static properties = {
    tiles: { attribute: false },        // the active screen's tiles [{path, x, y, w, h, float?}]
    components: { attribute: false },   // /components — the runtime colour dot, the deployments summary
    prs: { attribute: false },          // {path: open change proposals}
    canMutate: { attribute: false },    // layout changes allowed (personal screen, or an org draft)
    personal: { attribute: false },     // the screen is the viewer's own (a window's deployment pick is kept in it)
    mobile: { attribute: false },       // narrow layout: stacked cards, sheets, long-press menus
    menuOpen: { attribute: false },     // the shell's menu is open (Android's post-long-press contextmenu is swallowed)
    canAdminTile: { attribute: false }, // (path) → boolean: whether the ⚙ button shows
    emptyText: { attribute: false },    // what an empty screen says
    scale: { attribute: false },        // px per logical px of the grid — the per-browser grid scale (D68); 1 = 48px cells
    alerts: { attribute: false },       // /alerts rows (optional): a pending tile's card says its partition-switch message
    reload: { attribute: false },       // () → the shell reloads /components and /alerts (optional; after a decision)
    who: { attribute: false },          // /whoami (optional): whose partition a partitioned tile's window shows (its chip)
    _drag: { state: true },             // a grid drag/resize in flight: {path, rect, moves, dirs, orig, positive}
    _dmenu: { state: true },            // a window head's ⇈ menu: {items, anchor, title}
    _part: { state: true },             // path → a pending card's decision {key, busy, err, done}
    _pdlg: { state: true },             // the Switch… typed confirmation: {path, v, dry, spec}
  };
  static styles = [canvasCss, prbCss, partCss];

  constructor() {
    super();
    this.tiles = []; this.components = []; this.prs = {};
    this.canMutate = true; this.personal = true; this.mobile = false; this.menuOpen = false; this.emptyText = ''; this.scale = 1;
    this._press = new LongPress();
    this._pending = new Map(); // path → layout to open once its card exists
    this._drag = null; this._dmenu = null; this.alerts = []; this._part = {}; this._pdlg = null;
    this._shown = new Map(); // path → the deployment its window shows, where the layout doesn't keep it (a shared screen)
    // The rect a card's terminal pop-up must stay inside (D66): the canvas's
    // tile extent, in viewport coordinates — never left of / above the scroll
    // origin, never off past the tiles. Floats are viewport windows; theirs is null.
    this._popBounds = () => {
      const c = this.renderRoot.querySelector('.canvas');
      if (!c) return null;
      const r = c.getBoundingClientRect(), ext = this._tileExtentPx();
      return { left: r.left, top: r.top, right: r.left + ext.w, bottom: r.top + ext.h };
    };
    // A pop-up opened, moved, resized or closed: the scroll area follows.
    this.addEventListener('bx-pop', () => this.requestUpdate());
  }

  // Tile deployments (optional): a window whose tile has another deployment
  // the viewer may show, whose primary is pinned, or whose last deploy onto
  // it failed carries ⇈ in its head (shell-kit.js deployIcon): its menu
  // picks the deployment the window shows — kept in the personal screen's
  // tile entry (`deployment`, an additive field older shells and the app
  // ignore), for this page only on a shared (org) screen, whose layout
  // reaches everyone — and a +name tag says so while it isn't the primary.
  connectedCallback() {
    super.connectedCallback();
    followDeployments();
    this._offDeploy = onDeployChange(() => this.requestUpdate());
  }
  disconnectedCallback() { super.disconnectedCallback(); this._offDeploy?.(); }
  _deployIcon(o, shown) {
    const c = (this.components ?? []).find((x) => x.path === o.path);
    wantDeployState(o.path, c);
    return deployIcon(c, deployState(o.path), shown);
  }
  get _keepsPick() { return this.personal && this.canMutate; }
  _shownDep(o) { return shownDeployment((this._keepsPick ? o.deployment : this._shown.get(o.path)) || '', deployState(o.path)); }
  _showDeployment(path, dep) {
    if (!this._keepsPick) {
      if (dep) this._shown.set(path, dep); else this._shown.delete(path);
      this.requestUpdate();
      return;
    }
    this._mutate((tiles) => tiles.map((o) => {
      if (o.path !== path) return o;
      const n = { ...o };
      if (dep) n.deployment = dep; else delete n.deployment;
      return n;
    }));
  }
  _deployMenu(e, o) {
    const c = (this.components ?? []).find((x) => x.path === o.path);
    this._dmenu = { anchor: e.currentTarget.getBoundingClientRect(), title: o.path,
      items: deployMenu(o.path, c, deployState(o.path), this._shownDep(o), {
        show: (d) => this._showDeployment(o.path, d),
        openPanel: () => this.frameOpen(o.path, 'deployments'),
        openPage: (ref) => window.open(`/c/${ref}/`, '_blank'),
      }) };
  }

  _emit(type, detail) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  _all() { return this.tiles ?? []; }
  // The grid scale (D68): the layout is logical (multiples of GRID), the
  // render multiplies by k. Pointer deltas divide by k, so a drag on a
  // scaled screen snaps to the same cells everyone else sees. Mobile stacks
  // cards and ignores geometry, so it renders at 1.
  get _k() { return this.mobile ? 1 : (Number(this.scale) || 1); }
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
  // The gesture lives in `_drag` (state): the dragged card renders from its
  // live snapped rect, and the neighbours it would displace render as ghosts
  // at their landing spots (D66). The push is recomputed from the layout as
  // it was at pointerdown, so backing off restores everyone, and release
  // commits the rect and the moves in one bx-tiles event.
  _gtile(path) { return this.renderRoot.querySelector(`.gtile[data-path="${CSS.escape(path)}"]`); }

  _gridDragStart(ev, path) {
    if (this.mobile) return; // tiles are stacked (no free grid) on mobile
    if (!this.canMutate) return; // shared screen in view mode (D55)
    if (ev.button !== 0 || ev.target.closest('button, select, .rz')) return;
    ev.preventDefault();
    const el = this._gtile(path), o = this._all().find((t) => t.path === path && !t.float);
    if (!el || !o) return;
    const base = this._all().filter((t) => !t.float).map((t) => ({ ...t }));
    const k = this._k, dx = ev.clientX - o.x * k, dy = ev.clientY - o.y * k;
    this._drag = { path, rect: { x: o.x, y: o.y, w: o.w, h: o.h }, moves: [], dirs: null, orig: o };
    dragPointer({
      onMove: (e) => this._dragTo(base, { x: snap(Math.max(0, (e.clientX - dx) / k)), y: snap(Math.max(0, (e.clientY - dy) / k)), w: o.w, h: o.h }),
      onUp: () => this._commitDrag(),
    });
  }

  _gridResizeStart(ev, path) {
    if (this.mobile || ev.button !== 0) return;
    if (!this.canMutate) return; // shared screen in view mode (D55)
    ev.preventDefault(); ev.stopPropagation();
    const o = this._all().find((t) => t.path === path && !t.float);
    if (!o) return;
    const base = this._all().filter((t) => !t.float).map((t) => ({ ...t }));
    const sx = ev.clientX, sy = ev.clientY, k = this._k;
    // a resize grows from its top-left corner: it only ever pushes right/down
    this._drag = { path, rect: { x: o.x, y: o.y, w: o.w, h: o.h }, moves: [], dirs: null, orig: o, positive: true };
    dragPointer({
      cursor: 'nwse-resize',
      onMove: (e) => this._dragTo(base, { x: o.x, y: o.y, w: snap(Math.max(MIN_W, o.w + (e.clientX - sx) / k)), h: snap(Math.max(MIN_H, o.h + (e.clientY - sy) / k)) }),
      onUp: () => this._commitDrag(),
    });
  }

  // One step: only when the snapped rect changed (a render per cell crossed,
  // not per pixel), recompute the push from the pointerdown layout.
  _dragTo(base, rect) {
    const d = this._drag;
    if (!d) return;
    const r = d.rect;
    if (r.x === rect.x && r.y === rect.y && r.w === rect.w && r.h === rect.h) return;
    const { moves, dirs } = pushLayout(base, d.path, rect, { dirs: d.dirs, positive: !!d.positive });
    this._drag = { ...d, rect, moves, dirs };
  }

  _commitDrag() {
    const d = this._drag;
    this._drag = null;
    if (!d) return;
    const o = d.orig, r = d.rect;
    if (!d.moves.length && o.x === r.x && o.y === r.y && o.w === r.w && o.h === r.h) return; // a click: nothing to save
    const at = new Map([[d.path, r], ...d.moves.map((m) => [m.path, m])]);
    this._mutate((tiles) => tiles.map((t) => {
      const n = !t.float && at.get(t.path);
      return n ? { ...t, x: n.x, y: n.y, w: n.w, h: n.h } : t;
    }));
  }

  _gridCard(o) {
    const d = this._drag?.path === o.path ? this._drag : null;
    const r = d ? d.rect : o, k = this._k;
    return html`
      <div class="gtile ${d ? 'dragging' : ''}" data-path=${o.path}
           style="left:${r.x * k}px; top:${r.y * k}px; width:${(r.w - GAP) * k}px; height:${(r.h - GAP) * k}px;">
        ${this._cardTemplate(o, 'grid')}
        <div class="rz" title="drag to resize" @pointerdown=${(e) => this._gridResizeStart(e, o.path)}></div>
      </div>`;
  }

  // The tiles' logical extent (+ a drag in flight), in grid units.
  _tileExtent() {
    const rects = this._all().filter((o) => !o.float);
    if (this._drag) rects.push(this._drag.rect, ...this._drag.moves);
    return {
      w: rects.reduce((m, o) => Math.max(m, o.x + o.w), 0) + GRID,
      h: rects.reduce((m, o) => Math.max(m, o.y + o.h), 0) + GRID,
    };
  }

  // The same in pixels, floored to the visible pane so the dot field fills it
  // even on a near-empty screen. Measured from where the canvas sits in main
  // (whatever is above it) down to main's bottom padding.
  _tileExtentPx() {
    const k = this._k, lg = this._tileExtent();
    const main = this.closest('main');
    let vw = 0, vh = 0;
    if (main) {
      const top = this.getBoundingClientRect().top - main.getBoundingClientRect().top + main.scrollTop;
      vw = main.clientWidth - 28;
      vh = main.clientHeight - top - 14;
    }
    return { w: Math.max(vw, lg.w * k), h: Math.max(vh, lg.h * k) };
  }

  // A viewport point in the layout's logical px, clamped into `view` — the
  // part of the canvas visible in <main> — so a right-click over the
  // sidebar or the org strip lands on the nearest visible canvas edge
  // (spotNear places a tile opened from a menu there, D80).
  gridPoint(clientX, clientY) {
    const c = this.renderRoot.querySelector('.canvas'), main = this.closest('main');
    if (!c || !main) return null;
    const k = this._k, cr = c.getBoundingClientRect(), mr = main.getBoundingClientRect();
    const x0 = Math.max(0, (mr.left - cr.left) / k), y0 = Math.max(0, (mr.top - cr.top) / k);
    const view = { x: x0, y: y0,
      w: Math.max(0, (mr.left + main.clientWidth - cr.left) / k - x0), h: Math.max(0, (mr.top + main.clientHeight - cr.top) / k - y0) };
    const clamp = (v, lo, span) => Math.min(Math.max(v, lo), lo + span);
    return { x: clamp((clientX - cr.left) / k, view.x, view.w), y: clamp((clientY - cr.top) / k, view.y, view.h), view };
  }

  // Content bounds for the (absolute-positioned) canvas: the tiles, plus the
  // open terminal pop-ups of grid cards (D66) — they live inside the canvas,
  // so the scroll area grows to contain them and shrinks when they close.
  _gridExtent() {
    const ext = this._tileExtentPx();
    const c = this.renderRoot?.querySelector('.canvas');
    const cr = c?.getBoundingClientRect();
    if (cr) {
      for (const f of this.frames()) {
        const b = f.closest('.gtile') ? f.popBox?.() : null;
        if (b) {
          ext.w = Math.max(ext.w, b.x + b.w - cr.left + GRID);
          ext.h = Math.max(ext.h, b.y + b.h - cr.top + GRID);
        }
      }
    }
    return ext;
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
    const floating = kind === 'float', shown = this._shownDep(o);
    const c = this._rowOf(o.path), pv = partitionView(c);
    // a window on another deployment shows no partitioned marker: that
    // deployment's one instance is shared by the tile's writers (01 §2.8);
    // the chip says whose partition the window shows (yours/shared/global)
    const mark = shown ? null : partitionMark(c), chip = partitionChip(pv, { shown, who: this.who });
    const frame = html`<bx-frame src=${o.path} deployment=${shown || nothing} no-edit height="100%" .popBounds=${floating ? null : this._popBounds}></bx-frame>`;
    return html`
      <div class="card" data-path=${o.path}
           @bx-contextmenu=${(e) => { e.stopPropagation(); this._tileMenu({ clientX: e.detail.x, clientY: e.detail.y }, o.path, null, e.detail.selection || ''); }}>
        <div class="head"
             @pointerdown=${(e) => { this._press.start(e, () => this._tileMenu(null, o.path), this.mobile); (floating ? this._floatDragStart(e, o.path) : this._gridDragStart(e, o.path)); }}
             @pointermove=${(e) => this._press.move(e)}
             @pointerup=${() => this._press.cancel()} @pointercancel=${() => this._press.cancel()} @pointerleave=${() => this._press.cancel()}>
          ${mark ?? html`<span class="c" style="background:${RUNTIME_COLOR[this._runtimeOf(o.path)] ?? RUNTIME_COLOR['']}"></span>`}
          <span class="t">${o.path}</span>
          ${shown ? html`<span class="dtag" title=${`this window shows ${o.path}'s deployment ${shown} (/c/${o.path}+${shown}/), not the primary`}>+${shown}</span>` : nothing}
          ${chipTag(chip)}
          ${prBadge(this.prs?.[o.path], () => this.frameOpen(o.path, 'prs'))}
          ${deployBadge(this._deployIcon(o, shown), (e) => this._deployMenu(e, o))}
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
        <div class="cbody">${frame}${this._partOverlay(o.path, c, pv, shown)}</div>
      </div>`;
  }

  // ---- partitioned tiles (docs/partitions.md §The mode) ----
  // A pending tile's card greys out under the switch request's words (its
  // /alerts row, as the banner says them), and a tile manager decides there:
  // Keep the current mode, or Switch… — the dry run's counts and keep list,
  // then the tile's path typed (POST /api/xbin/partitions/mode, as the
  // signed-in person; xbind judges who may). Beneath, the frame shows xbind's
  // own page either way. A window showing another deployment isn't paused,
  // so it has no overlay; a row without a pending request draws nothing.
  // A card's decision state lives while its row shows that request pending:
  // pruned whenever /components changes, never set once it doesn't.
  _rowOf(path) { return (this.components ?? []).find((x) => x.path === path); }
  willUpdate(changed) {
    if (changed.has('components')) this._part = pruneDecisions(this._part, (p) => this._rowOf(p));
  }
  _partOf(path, v) {
    const st = this._part[path];
    return st && st.key === requestKey(v) ? st : null;
  }
  _setPart(path, v, patch) {
    const cur = partitionView(this._rowOf(path));
    if (!cur?.pending || requestKey(cur) !== requestKey(v)) return; // decided, withdrawn or changed meanwhile
    this._part = { ...this._part, [path]: { ...(this._partOf(path, v) ?? { key: requestKey(v) }), ...patch } };
  }
  _partOverlay(path, c, v, shown) {
    if (!v?.pending || shown) return nothing;
    const st = this._partOf(path, v) ?? {}, busy = !!st.busy, note = noteText(path, v);
    const buttons = this.canAdminTile?.(path) ? html`<div class="pbtns">
        <button class="pkeep" ?disabled=${busy} title="the tile runs again in its current mode (${modeName(v.from)}); nothing is deleted"
          @click=${() => this._partKeep(path, v)}>${st.busy === 'keep' ? 'Keeping…' : 'Keep the current mode'}</button>
        <button class=${deletesNothing(v.from, v.to) ? 'pswitch' : 'pswitch pdel'} ?disabled=${busy} title="shows what the switch deletes and keeps, then asks you to type ${path}"
          @click=${() => this._partSwitch(path, v)}>${st.busy === 'count' ? 'Counting…' : st.busy === 'switch' ? 'Switching…' : switchLabel(v.from, v.to) + '…'}</button>
      </div>` : html`<p class="pwho">Who decides: ${whoDecides(c?.owner)}.</p>`;
    return html`<div class="pover"><div class="pbox" role="alert">
      <div class="phead"><span class="pdot"></span>Paused until a manager decides</div>
      <p class="pmsg">${pendingText(path, v, this.alerts)}</p>
      ${note ? html`<p class="pnote">${note}</p>` : nothing}
      ${st.done ? html`<p class="pdone">${st.done}</p>` : buttons}
      ${st.err ? html`<p class="perr">${st.err}</p>` : nothing}
    </div></div>`;
  }
  async _partKeep(path, v) {
    this._setPart(path, v, { busy: 'keep', err: '' });
    const res = await postMode((u, i) => fetch(u, i), modeBody(path, 'keep', v));
    this._setPart(path, v, res.ok ? { busy: '', done: keptText(path, v) } : { busy: '', err: errorText(res) });
    this.reload?.();
  }
  // Switch…: the dry run first (the counts, the keep list, the sandbox
  // managers that don't keep people apart), then the typed confirmation.
  async _partSwitch(path, v) {
    this._setPart(path, v, { busy: 'count', err: '' });
    const res = await postMode((u, i) => fetch(u, i), modeBody(path, 'switch', v, { dryRun: true }));
    this._setPart(path, v, res.ok ? { busy: '' } : { busy: '', err: errorText(res) });
    if (res.ok) this._pdlg = { path, v, dry: res.body, spec: switchSpec(path, v, res.body) };
    else if (res.status === 409) this.reload?.(); // the request changed: look again
  }
  async _partResolve(detail) {
    const d = this._pdlg;
    if (!d) return;
    const r = switchResolve(d.path, detail, d.dry);
    if (r.cancel) { this._pdlg = null; return; }
    if (r.again) { this._pdlg = { ...d, spec: switchSpec(d.path, d.v, d.dry, { typed: r.typed, yes: r.yes, error: r.again }) }; return; }
    this._pdlg = null;
    this._setPart(d.path, d.v, { busy: 'switch', err: '' });
    const res = await postMode((u, i) => fetch(u, i), modeBody(d.path, 'switch', d.v, r.extra));
    if (res.ok) {
      this._setPart(d.path, d.v, { busy: '', done: switchedText(d.path, d.v, res.body) });
      this.reload?.();
      return;
    }
    if (staleRefusal(res)) { // the request changed or was decided meanwhile: the card says why, and looks again
      this._setPart(d.path, d.v, { busy: '', err: errorText(res) });
      this.reload?.();
      return;
    }
    this._setPart(d.path, d.v, { busy: '' });
    const dry = Array.isArray(res.body?.managers) ? { ...d.dry, managers: res.body.managers } : d.dry;
    this._pdlg = { ...d, dry, spec: switchSpec(d.path, d.v, dry, { typed: r.typed, yes: r.yes, error: errorText(res) }) };
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
      <div class="canvas ${this.canMutate ? '' : 'ro'}" style="min-height:${ext.h}px; min-width:${ext.w}px; --grid-px:${GRID * this._k}px"
           @contextmenu=${(e) => this._onContextMenu(e)}
           @pointerdown=${(e) => this._bgPress(e)}
           @pointermove=${(e) => this._press.move(e)}
           @pointerup=${() => this._press.cancel()} @pointercancel=${() => this._press.cancel()}>
        ${repeat(grid, (o) => o.path, (o) => this._gridCard(o))}
        ${(this._drag?.moves ?? []).map((m) => html`<div class="ghost" data-path=${m.path}
          style="left:${m.x * this._k}px; top:${m.y * this._k}px; width:${(m.w - GAP) * this._k}px; height:${(m.h - GAP) * this._k}px;"></div>`)}
      </div>
      ${grid.length === 0 && floats.length === 0 ? html`<div class="empty">${this.emptyText}</div>` : nothing}
      ${repeat(floats, (o) => o.path, (o) => this._floatTemplate(o))}
      ${this._dmenu ? html`<bx-menu open .items=${this._dmenu.items} .anchor=${this._dmenu.anchor} ?sheet=${this.mobile}
          title=${this._dmenu.title} @bx-menu-close=${() => { this._dmenu = null; }}></bx-menu>` : nothing}
      ${this._pdlg ? html`<bx-dialog open .spec=${this._pdlg.spec}
          @bx-dialog-resolve=${(e) => { e.stopPropagation(); this._partResolve(e.detail); }}></bx-dialog>` : nothing}`;
  }
}

customElements.define('bx-canvas', BxCanvas);
