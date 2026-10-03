/**
 * <bx-menu> — the shell's context menu: an anchored popover on desktop (with
 * flyout submenus), a bottom sheet with drill-in submenus on narrow screens.
 * Items are plain objects carrying action closures; the host renders the
 * element while a menu is open and drops it on `bx-menu-close`. Shell chrome
 * — not (yet) a tile API: tiles float things over the workspace through
 * xbin.dialog / xbin.window (docs/elements.md).
 *
 *   <bx-menu open .items=${items} .x=${e.clientX} .y=${e.clientY}
 *            ?sheet=${mobile} title="apps/crawler"
 *            @bx-menu-close=${() => { this._menu = null; }}></bx-menu>
 *
 *   items: [
 *     { label, icon?, hint?, badge?, mono?, disabled?, danger?, checked?,
 *       keywords?,   // extra text an `input` sibling matches (e.g. the full path)
 *       quiet?,      // rendered only while an `input` sibling has a query
 *       action?,     // () => void — runs AFTER the menu has closed
 *       items? },    // → submenu (flyout on desktop, drill-in on a sheet)
 *     { kind: 'sep' },
 *     { kind: 'header', label },
 *     { kind: 'grid', cells: [{ icon, label, badge?, mono?, disabled?, title?, action }] },
 *     { kind: 'input', placeholder?, empty?, hint? },  // filters its SIBLING items;
 *   ]                       // Enter picks the first match; `hint` shows while empty
 *
 * Point mode (`x`/`y`, a right-click) flips left/up at the viewport edges;
 * anchor mode (`.anchor` = a DOMRect, e.g. a ⋯ button) opens below it,
 * right-aligned. Keyboard: arrows / Home / End, Enter / Space, ArrowRight /
 * ArrowLeft for submenus, Escape, Tab closes, type-ahead on labels. Fires
 * `bx-menu-close` (dismissed or chosen — before the action runs) and
 * `bx-menu-select` {item} for items without an action.
 *
 * An item's or a cell's `icon` is a /vendor/bx-icons.js name (D184:
 * 'terminal', 'pencil', …), drawn as <bx-icon>; any other string is shown
 * as text, as before (an older shell's glyph characters keep working).
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { deepActive } from '/vendor/bx-kit.js';
import { hasIcon } from '/vendor/bx-icons.js';

const isItem = (it) => !it.kind || it.kind === 'item';
// an item's icon: a glyph of the set, else its text
const glyph = (icon) => (icon && hasIcon(icon) ? html`<bx-icon name=${icon}></bx-icon>` : (icon ?? ''));

export class BxMenu extends LitElement {
  static properties = {
    items: { attribute: false },
    x: { type: Number },
    y: { type: Number },
    anchor: { attribute: false },   // {left, top, right, bottom}
    sheet: { type: Boolean, reflect: true },
    title: { type: String },
    open: { type: Boolean, reflect: true },
    _pos: { state: true },          // main panel {left, top} once measured
    _sub: { state: true },          // the item whose flyout is open (desktop)
    _subPos: { state: true },
    _stack: { state: true },        // sheet drill-in: [{items, label}]
    _q: { state: true },            // the main panel's (or the sheet level's) filter query
    _subQ: { state: true },         // the flyout's — each input filters its own level only
  };

  static styles = [scrollCss, css`
    :host { position: fixed; inset: 0; z-index: var(--bx-menu-z, 3800); display: none;
      font: var(--bx-font, 13px/18px system-ui, sans-serif); color: var(--bx-text, #E9EAF0); }
    :host([open]) { display: block; }
    button, input { font: inherit; color: inherit; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    .panel:focus-visible, .sheet:focus-visible { outline: none; box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
    ::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
    bx-icon { flex: none; }
    .backdrop { position: absolute; inset: 0; }
    :host([sheet]) .backdrop { background: var(--bx-scrim, rgba(0, 0, 0, 0.55)); }

    /* ---- desktop panels: a popover (product-ui 6), square, 28 px rows ---- */
    .panel {
      position: fixed; min-width: 200px; max-width: min(360px, 92vw);
      max-height: calc(100vh - 16px); overflow-y: auto; overscroll-behavior: contain;
      padding: 4px; box-sizing: border-box; outline: none;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: var(--bx-radius, 2px); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
    }
    .panel.hidden { visibility: hidden; }
    .panel:has(.grid) { min-width: 280px; }

    /* ---- rows: hovered and keyboard-focused in the hover tint; the focus
       ring sits inside (the panel clips) ---- */
    .it {
      display: flex; align-items: center; gap: 8px; width: 100%; box-sizing: border-box; min-height: var(--bx-row, 28px);
      text-align: left; border: 0; background: transparent; color: var(--bx-text, #E9EAF0);
      padding: 0 8px; border-radius: var(--bx-radius, 2px); cursor: pointer;
      white-space: nowrap;
    }
    .it:hover, .it:focus-visible { background: var(--bx-hover, #2A2B34); }
    .it.open { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); }
    .it:focus-visible, .cell:focus-visible, .q:focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: calc(-1 * var(--bx-focus-width, 3px)); box-shadow: none; }
    .it[disabled] { opacity: 0.5; cursor: default; }
    .it[disabled]:hover { background: transparent; }
    .it.danger { color: var(--bx-danger, #FF7A7A); }
    .it .ic { flex: none; display: inline-flex; align-items: center; justify-content: center; width: 16px; color: var(--bx-muted, #A3A6B6); }
    .it.danger .ic { color: inherit; }
    .it.mono .ic, .it.mono .lb { font-family: var(--bx-mono, ui-monospace, monospace); }
    .it .lb { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
    .it .hint { flex: none; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); max-width: 40%;
      overflow: hidden; text-overflow: ellipsis; }
    .it .arrow { flex: none; display: inline-flex; color: var(--bx-muted, #A3A6B6); }
    .it .chk { flex: none; display: inline-flex; width: 16px; color: var(--bx-accent, #8C9BFF); }
    .badge { flex: none; box-sizing: border-box; height: 18px; padding: 0 4px; border-radius: var(--bx-radius, 2px);
      font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); font-weight: 600; line-height: 16px; font-variant-numeric: tabular-nums;
      color: var(--bx-text, #E9EAF0); border: 1px solid var(--bx-border-strong, #666A7E); }
    .sep { height: 1px; margin: 4px; background: var(--bx-border, #33353F); }
    .hd { padding: 8px 8px 2px; font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em);
      text-transform: uppercase; color: var(--bx-muted, #A3A6B6); }
    .empty { padding: 6px 8px; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
    .ttl { padding: 4px 8px 6px; font: var(--bx-font-code, 12px/18px ui-monospace, monospace); font-weight: 600;
      color: var(--bx-muted, #A3A6B6); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

    /* the squares row */
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(56px, 1fr)); gap: 4px; padding: 2px 0 6px; }
    .cell {
      position: relative; display: flex; flex-direction: column; align-items: center; justify-content: center;
      gap: 4px; height: 52px; box-sizing: border-box; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); cursor: pointer;
    }
    .cell:hover, .cell:focus-visible { background: var(--bx-hover, #2A2B34); border-color: var(--bx-border-strong, #666A7E); }
    .cell[disabled] { opacity: 0.5; cursor: default; }
    /* icon and label sit in fixed-height rows so labels align across cells
       whatever the glyph's own height */
    .cell .ic { height: 18px; display: flex; align-items: center; justify-content: center; line-height: 1; }
    .cell.mono .ic { font-family: var(--bx-mono, ui-monospace, monospace); font-weight: 700; }
    .cell .lb { height: 16px; font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); }
    .cell .badge { position: absolute; top: 2px; right: 2px; }

    .q {
      display: block; width: calc(100% - 8px); height: var(--bx-control-h, 28px); margin: 2px 4px 4px; box-sizing: border-box;
      padding: 0 8px; border: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    }

    /* ---- bottom sheet ---- */
    .sheet {
      position: fixed; left: 0; right: 0; bottom: 0; max-height: 84vh;
      display: flex; flex-direction: column; box-sizing: border-box;
      background: var(--bx-panel, #1F2028); border-top: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: 0; box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6));
      padding-bottom: env(safe-area-inset-bottom); outline: none;
      animation: bx-sheet-in var(--bx-dur-panel, 200ms) var(--bx-ease-out, cubic-bezier(0.16, 1, 0.3, 1));
    }
    @keyframes bx-sheet-in { from { transform: translateY(24px); opacity: 0.6; } to { transform: none; opacity: 1; } }
    @media (prefers-reduced-motion: reduce) { .sheet { animation: none; } }
    @media (max-height: 500px) { .sheet { max-height: 100vh; } }
    .shead { flex: none; display: flex; align-items: center; gap: 8px; padding: 4px 4px 4px 12px;
      border-bottom: 1px solid var(--bx-border, #33353F); }
    .shead .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: var(--bx-font-code, 12px/18px ui-monospace, monospace); font-weight: 600; }
    .shead button { display: inline-flex; align-items: center; justify-content: center; width: 40px; height: 40px; padding: 0;
      border: 0; background: transparent; color: var(--bx-text, #E9EAF0); cursor: pointer; border-radius: var(--bx-radius, 2px); }
    .shead button:hover { background: var(--bx-control-hover, #33353F); }
    .sbody { overflow-y: auto; overscroll-behavior: contain; padding: 6px; }
    .sheet .it { padding: 0 12px; font-size: 14px; min-height: 44px; }
    .sheet .grid { grid-template-columns: repeat(4, 1fr); gap: 8px; padding: 6px 4px 10px; }
    .sheet .cell { height: 64px; }
    .sheet .q { height: 40px; font-size: 14px; padding: 0 10px; }
    .sheet .hd { padding-top: 10px; }
  `];

  constructor() {
    super();
    this.items = [];
    this.x = 0; this.y = 0;
    this.anchor = null;
    this.sheet = false;
    this.title = '';
    this.open = false;
    this._pos = null;
    this._sub = null;
    this._subPos = null;
    this._stack = [];
    this._q = '';
    this._subQ = '';
    this._hover = null;   // flyout intent timer
    this._type = '';      // type-ahead buffer
    this._typeT = null;
    this._opener = null;
    this._lvl = [];       // per level: the visible items as rendered (rows carry data-l/data-i)
    this._onScroll = (e) => { if (e.target === this || this.contains?.(e.target)) return; this._close(); };
    this._onResize = () => { this._place(); if (this._sub) this._placeSub(); };
    // Escape closes even when focus wandered off the menu (a flyout that held
    // the focused input just closed, the opener kept focus, …).
    this._onDocKey = (e) => {
      if (e.key !== 'Escape' || !this.open || e.composedPath().includes(this)) return;
      e.preventDefault(); e.stopPropagation();
      this._close();
    };
  }

  connectedCallback() {
    super.connectedCallback();
    this._opener = deepActive();
    document.addEventListener('scroll', this._onScroll, { capture: true, passive: true });
    document.addEventListener('keydown', this._onDocKey, true);
    window.addEventListener('resize', this._onResize);
    this.updateComplete.then(() => {
      this._place();
      // A menu opened from inside a tile's iframe: pull keyboard focus out of
      // the frame so Escape / arrows reach the menu, not the tile document.
      if (this._opener?.tagName === 'IFRAME') { try { this._opener.blur(); } catch { /* fine */ } }
      this.renderRoot.querySelector('.panel.main, .sheet')?.focus({ preventScroll: true });
    });
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('scroll', this._onScroll, { capture: true });
    document.removeEventListener('keydown', this._onDocKey, true);
    window.removeEventListener('resize', this._onResize);
    clearTimeout(this._hover);
    clearTimeout(this._typeT);
  }

  // ---- geometry ----
  async _place() {
    await this.updateComplete;
    if (this.sheet) return;
    const p = this.renderRoot.querySelector('.panel.main');
    if (!p) return;
    const w = p.offsetWidth, h = p.offsetHeight;
    const W = window.innerWidth, H = window.innerHeight, M = 8;
    let left, top;
    const a = this.anchor;
    if (a) {
      left = a.right - w; top = a.bottom + 4;
      if (top + h > H - M && a.top - 4 - h >= M) top = a.top - 4 - h;
    } else {
      left = this.x; top = this.y;
      if (left + w > W - M) left = this.x - w >= M ? this.x - w : W - M - w;
      if (top + h > H - M) top = this.y - h >= M ? this.y - h : H - M - h;
    }
    this._pos = { left: Math.max(M, Math.min(left, W - M - w)), top: Math.max(M, Math.min(top, H - M - h)) };
  }
  async _placeSub() {
    await this.updateComplete;
    const s = this.renderRoot.querySelector('.panel.sub');
    const it = this.renderRoot.querySelector('.panel.main .it.open');
    if (!s || !it) return;
    const r = it.getBoundingClientRect();
    const w = s.offsetWidth, h = s.offsetHeight;
    const W = window.innerWidth, H = window.innerHeight, M = 8;
    let left = r.right - 2;
    if (left + w > W - M) left = Math.max(M, r.left - w + 2);
    let top = r.top - 4;
    if (top + h > H - M) top = Math.max(M, H - M - h);
    this._subPos = { left, top };
  }

  // ---- lifecycle of a choice ----
  _close() {
    if (!this.open) return;
    this.open = false;
    this.dispatchEvent(new CustomEvent('bx-menu-close', { bubbles: true, composed: true }));
    const o = this._opener;
    if (o?.isConnected && typeof o.focus === 'function') { try { o.focus({ preventScroll: true }); } catch { /* fine */ } }
  }
  _choose(item, el) {
    if (!item || item.disabled) return;
    if (item.items) {
      if (this.sheet) { this._stack = [...this._stack, { items: item.items, label: item.label }]; this._q = ''; }
      else this._openSub(item, el);
      return;
    }
    this._close();
    if (typeof item.action === 'function') item.action();
    else this.dispatchEvent(new CustomEvent('bx-menu-select', { detail: { item }, bubbles: true, composed: true }));
  }
  _openSub(item, el) {
    clearTimeout(this._hover);
    if (this._sub === item) return;
    this._sub = item; this._subPos = null; this._subQ = '';
    this._placeSub();
    if (el) el.dataset.sub = '1';
  }
  _closeSub() { clearTimeout(this._hover); this._sub = null; this._subPos = null; this._subQ = ''; }
  // Hover intent on a ROOT row: open its flyout, or close the open one when
  // the pointer settles on a plain row. Rows inside the flyout only cancel a
  // pending close.
  _hoverItem(item, el, level) {
    if (this.sheet) return;
    clearTimeout(this._hover);
    if (level > 0) return;
    this._hover = setTimeout(() => { if (item?.items) this._openSub(item, el); else if (this._sub) this._closeSub(); }, 120);
  }
  _itemOf(el) { return el?.dataset?.l != null ? this._lvl[+el.dataset.l]?.[+el.dataset.i] : null; }

  // ---- what a level shows ----
  _level() {
    if (this.sheet && this._stack.length) return this._stack[this._stack.length - 1].items;
    return this.items ?? [];
  }
  // A query filters only a level that holds the input: typing in the
  // flyout's find box never thins out the menu it hangs off.
  _visible(items, q) {
    const ql = items.some((it) => it.kind === 'input') ? q.trim().toLowerCase() : '';
    return items.filter((it) => {
      if (!isItem(it)) return !(ql && it.kind === 'header');
      if (ql) return `${it.label ?? ''} ${it.keywords ?? ''}`.toLowerCase().includes(ql);
      return !it.quiet;
    });
  }
  _firstChoice(items, q) {
    return this._visible(items, q).find((it) => isItem(it) && !it.disabled) ?? null;
  }

  // ---- keyboard ----
  _focusables(panel) {
    return [...panel.querySelectorAll('.it:not([disabled]), .cell:not([disabled]), .q')];
  }
  _onKey(e, panel, items) {
    const list = this._focusables(panel);
    const cur = deepActive();
    const i = list.indexOf(cur);
    const focusAt = (n) => { const el = list[(n + list.length) % list.length]; el?.focus({ preventScroll: true }); };
    const inInput = cur?.classList?.contains('q');
    const stop = () => { e.preventDefault(); e.stopPropagation(); };
    switch (e.key) {
      case 'Escape':
        stop();
        if (!this.sheet && this._sub && panel.classList.contains('sub')) { this._closeSub(); this._focusOpenItem(); return; }
        if (this.sheet && this._stack.length) { this._back(); return; }
        this._close(); return;
      case 'Tab': stop(); this._close(); return;
      case 'ArrowDown': stop(); focusAt(i < 0 ? 0 : i + 1); return;
      case 'ArrowUp': stop(); focusAt(i < 0 ? list.length - 1 : i - 1); return;
      case 'Home': if (inInput) return; stop(); focusAt(0); return;
      case 'End': if (inInput) return; stop(); focusAt(list.length - 1); return;
      case 'ArrowRight': {
        if (inInput) return;
        if (cur?.classList?.contains('cell')) { stop(); focusAt(i + 1); return; }
        const it = this._itemOf(cur);
        if (it?.items && !this.sheet) { stop(); this._openSub(it, cur); this.updateComplete.then(() => this._focusables(this.renderRoot.querySelector('.panel.sub'))[0]?.focus()); }
        else if (it?.items) { stop(); this._choose(it, cur); }
        return;
      }
      case 'ArrowLeft':
        if (inInput) return;
        if (cur?.classList?.contains('cell')) { stop(); focusAt(i - 1); return; }
        if (!this.sheet && panel.classList.contains('sub')) { stop(); this._closeSub(); this._focusOpenItem(); }
        else if (this.sheet && this._stack.length) { stop(); this._back(); }
        return;
      case 'Enter':
        if (inInput) { stop(); const f = this._firstChoice(items, panel.classList.contains('sub') ? this._subQ : this._q); if (f) this._choose(f, cur); }
        return; // buttons activate natively
      default:
        if (inInput || e.ctrlKey || e.metaKey || e.altKey || e.key.length !== 1) return;
        stop();
        this._type += e.key.toLowerCase();
        clearTimeout(this._typeT);
        this._typeT = setTimeout(() => { this._type = ''; }, 500);
        const hit = list.find((el) => (this._itemOf(el)?.label ?? '').toLowerCase().startsWith(this._type));
        hit?.focus({ preventScroll: true });
    }
  }
  _focusOpenItem() { this.renderRoot.querySelector('.panel.main .it.open, .panel.main .it')?.focus({ preventScroll: true }); }
  _back() { this._stack = this._stack.slice(0, -1); this._q = ''; }

  // ---- templates ----
  _row(it, level, idx) {
    const submenu = !!it.items;
    return html`<button class="it ${it.danger ? 'danger' : ''} ${it.mono ? 'mono' : ''} ${this._sub === it ? 'open' : ''}"
        role="menuitem" ?disabled=${!!it.disabled} title=${it.title ?? nothing}
        data-l=${level} data-i=${idx}
        @pointerenter=${(e) => this._hoverItem(it, e.currentTarget, level)}
        @click=${(e) => this._choose(it, e.currentTarget)}>
      ${it.checked != null ? html`<span class="chk">${it.checked ? html`<bx-icon name="check" label="selected"></bx-icon>` : ''}</span>` : nothing}
      <span class="ic">${glyph(it.icon)}</span>
      <span class="lb">${it.label}</span>
      ${it.badge ? html`<span class="badge">${it.badge}</span>` : nothing}
      ${it.hint ? html`<span class="hint">${it.hint}</span>` : nothing}
      ${submenu ? html`<span class="arrow"><bx-icon name="chevron-right"></bx-icon></span>` : nothing}
    </button>`;
  }
  _grid(g) {
    return html`<div class="grid">${(g.cells ?? []).map((c) => html`
      <button class="cell ${c.mono && !hasIcon(c.icon) ? 'mono' : ''}" ?disabled=${!!c.disabled} title=${c.title ?? c.label ?? nothing}
          @click=${() => this._choose(c)}>
        <span class="ic">${glyph(c.icon)}</span>
        <span class="lb">${c.label ?? ''}</span>
        ${c.badge ? html`<span class="badge">${c.badge}</span>` : nothing}
      </button>`)}</div>`;
  }
  // One level of the menu, filtered by its own query `q`. Rows carry their
  // (level, index) into the visible list so the keyboard handler can look
  // the item up from a focused button.
  _list(items, level, q) {
    const vis = this._visible(items, q);
    this._lvl[level] = vis;
    const input = items.find((it) => it.kind === 'input');
    const anyItem = vis.some((it) => isItem(it));
    return html`
      ${vis.map((it, idx) => {
        if (it.kind === 'sep') return html`<div class="sep"></div>`;
        if (it.kind === 'header') return html`<div class="hd">${it.label}</div>`;
        if (it.kind === 'grid') return this._grid(it);
        if (it.kind === 'input') return html`<input class="q" type="search" placeholder=${it.placeholder ?? 'filter…'}
          .value=${q} autocomplete="off" spellcheck="false"
          @input=${(e) => { if (level) this._subQ = e.target.value; else this._q = e.target.value; }} @pointerenter=${() => this._hoverItem(null, null, level)}>`;
        return this._row(it, level, idx);
      })}
      ${input && !anyItem ? html`<div class="empty">${q.trim() ? (input.empty ?? 'no matches') : (input.hint ?? '')}</div>` : nothing}`;
  }

  render() {
    if (this.sheet) {
      const top = this._stack.length ? this._stack[this._stack.length - 1] : null;
      const items = this._level();
      return html`
        <div class="backdrop" @pointerdown=${() => this._close()} @contextmenu=${(e) => { e.preventDefault(); this._close(); }}></div>
        <div class="sheet" role="menu" tabindex="-1" @keydown=${(e) => this._onKey(e, e.currentTarget, items)}>
          <div class="shead">
            ${top ? html`<button title="back" aria-label="back" @click=${() => this._back()}><bx-icon name="chevron-left"></bx-icon></button><span class="t">${top.label}</span>`
              : html`<span class="t">${this.title || ''}</span>`}
            <button title="close" aria-label="close" @click=${() => this._close()}><bx-icon name="xmark"></bx-icon></button>
          </div>
          <div class="sbody">${this._list(items, 0, this._q)}</div>
        </div>`;
    }
    const items = this.items ?? [];
    return html`
      <div class="backdrop" @pointerdown=${() => this._close()} @contextmenu=${(e) => { e.preventDefault(); this._close(); }}></div>
      <div class="panel main ${this._pos ? '' : 'hidden'}" role="menu" tabindex="-1"
           style=${this._pos ? `left:${this._pos.left}px; top:${this._pos.top}px` : nothing}
           @keydown=${(e) => this._onKey(e, e.currentTarget, items)}>
        ${this.title && this.hasAttribute('show-title') ? html`<div class="ttl">${this.title}</div>` : nothing}
        ${this._list(items, 0, this._q)}
      </div>
      ${this._sub ? html`
        <div class="panel sub ${this._subPos ? '' : 'hidden'}" role="menu" tabindex="-1"
             style=${this._subPos ? `left:${this._subPos.left}px; top:${this._subPos.top}px` : nothing}
             @pointerenter=${() => clearTimeout(this._hover)}
             @keydown=${(e) => this._onKey(e, e.currentTarget, this._sub.items)}>
          ${this._list(this._sub.items ?? [], 1, this._subQ)}
        </div>` : nothing}`;
  }
}

customElements.define('bx-menu', BxMenu);
