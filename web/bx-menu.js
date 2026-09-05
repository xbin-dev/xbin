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
 *     { kind: 'input', placeholder?, empty? },   // filters its SIBLING items;
 *   ]                                             // Enter picks the first match
 *
 * Point mode (`x`/`y`, a right-click) flips left/up at the viewport edges;
 * anchor mode (`.anchor` = a DOMRect, e.g. a ⋯ button) opens below it,
 * right-aligned. Keyboard: arrows / Home / End, Enter / Space, ArrowRight /
 * ArrowLeft for submenus, Escape, Tab closes, type-ahead on labels. Fires
 * `bx-menu-close` (dismissed or chosen — before the action runs) and
 * `bx-menu-select` {item} for items without an action.
 */
import { LitElement, html, css, nothing } from 'lit';

const isItem = (it) => !it.kind || it.kind === 'item';

// The focused element, seen through open shadow roots.
function deepActive() {
  let a = document.activeElement;
  while (a?.shadowRoot?.activeElement) a = a.shadowRoot.activeElement;
  return a;
}

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
    _q: { state: true },            // the current level's filter query
  };

  static styles = css`
    :host { position: fixed; inset: 0; z-index: var(--bx-menu-z, 3800); display: none;
      font: var(--bx-font, 13px/1.45 -apple-system, system-ui, sans-serif); color: var(--bx-text, #33414e); }
    :host([open]) { display: block; }
    .backdrop { position: absolute; inset: 0; }
    :host([sheet]) .backdrop { background: rgba(0, 0, 0, .45); }

    /* ---- desktop panels ---- */
    .panel {
      position: fixed; min-width: 200px; max-width: min(360px, 92vw);
      max-height: calc(100vh - 16px); overflow-y: auto; overscroll-behavior: contain;
      padding: 4px; box-sizing: border-box; outline: none;
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px; box-shadow: 0 10px 30px rgba(0, 0, 0, .45);
    }
    .panel.hidden { visibility: hidden; }

    /* ---- rows ---- */
    .it {
      display: flex; align-items: center; gap: 8px; width: 100%; box-sizing: border-box;
      text-align: left; border: 0; background: transparent; color: var(--bx-text, #33414e);
      font: inherit; font-size: 12.5px; padding: 6px 10px; border-radius: 5px; cursor: pointer;
      white-space: nowrap;
    }
    .it:hover, .it:focus-visible, .it.open { background: var(--bx-panel-2, #f7f8fa); color: var(--bx-accent, #f5a623); outline: none; }
    .it[disabled] { opacity: .45; cursor: default; }
    .it[disabled]:hover { background: transparent; color: var(--bx-text, #33414e); }
    .it.danger { color: var(--bx-red, #e5484d); }
    .it .ic { flex: none; width: 16px; text-align: center; color: var(--bx-muted, #8794a1); }
    .it.mono .ic, .it.mono .lb { font-family: var(--bx-mono, ui-monospace, monospace); }
    .it .lb { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
    .it .hint { flex: none; font-size: 10.5px; color: var(--bx-muted, #8794a1); max-width: 40%;
      overflow: hidden; text-overflow: ellipsis; }
    .it .arrow { flex: none; color: var(--bx-muted, #8794a1); font-size: 10px; }
    .it .chk { flex: none; width: 12px; color: var(--bx-accent, #f5a623); }
    .badge { flex: none; padding: 0 5px; border-radius: 3px; font-size: 9.5px; line-height: 15px;
      letter-spacing: .02em; color: var(--bx-amber, #f2a71b);
      border: 1px solid color-mix(in srgb, var(--bx-amber, #f2a71b) 45%, transparent);
      background: color-mix(in srgb, var(--bx-amber, #f2a71b) 10%, transparent); }
    .sep { height: 1px; margin: 4px 6px; background: var(--bx-border, #e4e8ed); }
    .hd { padding: 6px 10px 2px; font-size: 10px; font-weight: 600; letter-spacing: .06em;
      text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    .empty { padding: 6px 10px; color: var(--bx-muted, #8794a1); font-size: 11.5px; }
    .ttl { padding: 4px 10px 6px; font: 600 11px var(--bx-mono, ui-monospace, monospace);
      color: var(--bx-muted, #8794a1); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

    /* the squares row */
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(52px, 1fr)); gap: 4px; padding: 2px 4px 6px; }
    .cell {
      position: relative; display: flex; flex-direction: column; align-items: center; justify-content: center;
      gap: 3px; height: 52px; border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); cursor: pointer; font: inherit;
    }
    .cell:hover, .cell:focus-visible { border-color: var(--bx-accent, #f5a623); color: var(--bx-accent, #f5a623); outline: none; }
    .cell[disabled] { opacity: .45; cursor: default; }
    .cell .ic { font-size: 15px; line-height: 1; }
    .cell.mono .ic { font-family: var(--bx-mono, ui-monospace, monospace); font-weight: 700; font-size: 12px; letter-spacing: -.5px; }
    .cell .lb { font-size: 10px; color: var(--bx-muted, #8794a1); }
    .cell .badge { position: absolute; top: 3px; right: 4px; }

    .q {
      display: block; width: calc(100% - 8px); margin: 2px 4px 4px; box-sizing: border-box;
      font: inherit; font-size: 12px; padding: 4px 8px; border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 5px; background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
    }
    .q:focus { outline: 2px solid color-mix(in srgb, var(--bx-accent, #f5a623) 35%, transparent); }

    /* ---- bottom sheet ---- */
    .sheet {
      position: fixed; left: 0; right: 0; bottom: 0; max-height: 84vh;
      display: flex; flex-direction: column; box-sizing: border-box;
      background: var(--bx-panel, #fff); border-top: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 12px 12px 0 0; box-shadow: 0 -10px 30px rgba(0, 0, 0, .35);
      padding-bottom: env(safe-area-inset-bottom); outline: none;
      animation: bx-sheet-in .18s ease-out;
    }
    @keyframes bx-sheet-in { from { transform: translateY(24px); opacity: .6; } to { transform: none; opacity: 1; } }
    @media (prefers-reduced-motion: reduce) { .sheet { animation: none; } }
    @media (max-height: 500px) { .sheet { max-height: 100vh; border-radius: 0; } }
    .shead { flex: none; display: flex; align-items: center; gap: 8px; padding: 10px 12px;
      border-bottom: 1px solid var(--bx-border, #e4e8ed); font-size: 12.5px; }
    .shead .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
      font: 600 12px var(--bx-mono, ui-monospace, monospace); }
    .shead button { border: 0; background: transparent; color: var(--bx-text, #33414e); font: inherit;
      font-size: 14px; padding: 6px 8px; cursor: pointer; border-radius: 6px; }
    .shead button:hover { background: var(--bx-panel-2, #f7f8fa); }
    .sbody { overflow-y: auto; overscroll-behavior: contain; padding: 6px; }
    .sheet .it { padding: 11px 12px; font-size: 14px; min-height: 44px; }
    .sheet .grid { grid-template-columns: repeat(4, 1fr); gap: 8px; padding: 6px 4px 10px; }
    .sheet .cell { height: 64px; }
    .sheet .cell .lb { font-size: 11px; }
    .sheet .q { font-size: 14px; padding: 8px 10px; }
    .sheet .hd { padding-top: 10px; }
  `;

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
    this._hover = null;   // flyout intent timer
    this._type = '';      // type-ahead buffer
    this._typeT = null;
    this._opener = null;
    this._lvl = [];       // per level: the visible items as rendered (rows carry data-l/data-i)
    this._onScroll = (e) => { if (e.target === this || this.contains?.(e.target)) return; this._close(); };
    this._onResize = () => { this._place(); if (this._sub) this._placeSub(); };
  }

  connectedCallback() {
    super.connectedCallback();
    this._opener = deepActive();
    document.addEventListener('scroll', this._onScroll, { capture: true, passive: true });
    window.addEventListener('resize', this._onResize);
    this.updateComplete.then(() => {
      this._place();
      this.renderRoot.querySelector('.panel.main, .sheet')?.focus({ preventScroll: true });
    });
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('scroll', this._onScroll, { capture: true });
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
    this._sub = item; this._subPos = null; this._q = '';
    this._placeSub();
    if (el) el.dataset.sub = '1';
  }
  _closeSub() { clearTimeout(this._hover); this._sub = null; this._subPos = null; this._q = ''; }
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
  _visible(items, q) {
    const ql = q.trim().toLowerCase();
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
        if (inInput) { stop(); const f = this._firstChoice(items, this._q); if (f) this._choose(f, cur); }
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
      ${it.checked != null ? html`<span class="chk">${it.checked ? '✓' : ''}</span>` : nothing}
      <span class="ic">${it.icon ?? ''}</span>
      <span class="lb">${it.label}</span>
      ${it.badge ? html`<span class="badge">${it.badge}</span>` : nothing}
      ${it.hint ? html`<span class="hint">${it.hint}</span>` : nothing}
      ${submenu ? html`<span class="arrow">${this.sheet ? '›' : '▸'}</span>` : nothing}
    </button>`;
  }
  _grid(g) {
    return html`<div class="grid">${(g.cells ?? []).map((c) => html`
      <button class="cell ${c.mono ? 'mono' : ''}" ?disabled=${!!c.disabled} title=${c.title ?? c.label ?? nothing}
          @click=${() => this._choose(c)}>
        <span class="ic">${c.icon ?? ''}</span>
        <span class="lb">${c.label ?? ''}</span>
        ${c.badge ? html`<span class="badge">${c.badge}</span>` : nothing}
      </button>`)}</div>`;
  }
  // One level of the menu. Rows carry their (level, index) into the visible
  // list so the keyboard handler can look the item up from a focused button.
  _list(items, level) {
    const vis = this._visible(items, this._q);
    this._lvl[level] = vis;
    const input = items.find((it) => it.kind === 'input');
    const anyItem = vis.some((it) => isItem(it));
    return html`
      ${vis.map((it, idx) => {
        if (it.kind === 'sep') return html`<div class="sep"></div>`;
        if (it.kind === 'header') return html`<div class="hd">${it.label}</div>`;
        if (it.kind === 'grid') return this._grid(it);
        if (it.kind === 'input') return html`<input class="q" type="search" placeholder=${it.placeholder ?? 'filter…'}
          .value=${this._q} autocomplete="off" spellcheck="false"
          @input=${(e) => { this._q = e.target.value; }} @pointerenter=${() => this._hoverItem(null, null, level)}>`;
        return this._row(it, level, idx);
      })}
      ${input && !anyItem ? html`<div class="empty">${input.empty ?? 'no matches'}</div>` : nothing}`;
  }

  render() {
    if (this.sheet) {
      const top = this._stack.length ? this._stack[this._stack.length - 1] : null;
      const items = this._level();
      return html`
        <div class="backdrop" @pointerdown=${() => this._close()} @contextmenu=${(e) => { e.preventDefault(); this._close(); }}></div>
        <div class="sheet" role="menu" tabindex="-1" @keydown=${(e) => this._onKey(e, e.currentTarget, items)}>
          <div class="shead">
            ${top ? html`<button title="back" @click=${() => this._back()}>‹</button><span class="t">${top.label}</span>`
              : html`<span class="t">${this.title || ''}</span>`}
            <button title="close" @click=${() => this._close()}>✕</button>
          </div>
          <div class="sbody">${this._list(items, 0)}</div>
        </div>`;
    }
    const items = this.items ?? [];
    return html`
      <div class="backdrop" @pointerdown=${() => this._close()} @contextmenu=${(e) => { e.preventDefault(); this._close(); }}></div>
      <div class="panel main ${this._pos ? '' : 'hidden'}" role="menu" tabindex="-1"
           style=${this._pos ? `left:${this._pos.left}px; top:${this._pos.top}px` : nothing}
           @keydown=${(e) => this._onKey(e, e.currentTarget, items)}>
        ${this.title && this.hasAttribute('show-title') ? html`<div class="ttl">${this.title}</div>` : nothing}
        ${this._list(items, 0)}
      </div>
      ${this._sub ? html`
        <div class="panel sub ${this._subPos ? '' : 'hidden'}" role="menu" tabindex="-1"
             style=${this._subPos ? `left:${this._subPos.left}px; top:${this._subPos.top}px` : nothing}
             @pointerenter=${() => clearTimeout(this._hover)}
             @keydown=${(e) => this._onKey(e, e.currentTarget, this._sub.items)}>
          ${this._list(this._sub.items ?? [], 1)}
        </div>` : nothing}`;
  }
}

customElements.define('bx-menu', BxMenu);
