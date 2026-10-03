/**
 * <bx-multiselect> — a normal-looking dropdown that lets you tick several
 * options once it's open (a checkbox menu), instead of the native
 * <select multiple>'s always-open, drag-to-select list box. Used wherever a
 * multi-value binding is wired: the admin Interfaces tab, the per-tile
 * mini-admin, and the bind-on-install prompt.
 *
 *   <bx-multiselect .options=${[{value,label}|'str']} .selected=${[value]}
 *                   placeholder="— unbound —"
 *                   @change=${e => save(e.detail.selected)}></bx-multiselect>
 *
 * Emits `change` with { selected: [value…] } on every toggle. The list is
 * viewport-fixed (positioned from the control's rect, flipped up when there
 * is no room below), so a scrolling or overflow-hidden container — a table
 * cell in the tile-admin window, a popover — never clips it.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import '/vendor/bx-icons.js';

export class BxMultiselect extends LitElement {
  static properties = {
    options: { attribute: false },   // [{value,label}] | [{id,label}] | [string]
    selected: { attribute: false },  // [value]
    placeholder: { type: String },
    _open: { state: true },
    _pos: { state: true },           // {left, top, minWidth} once measured
  };

  // product-ui §6 (D184): the control is an input (28px, border-strong, 2px
  // corners, the focus ring); its list a pop-over (border, --bx-shadow-pop)
  // of 28px rows.
  static styles = [scrollCss, css`
    :host { display: inline-block; position: relative; min-width: 150px; font: inherit; }
    .control {
      width: 100%; box-sizing: border-box; display: flex; align-items: center; gap: 6px; min-height: var(--bx-control-h, 28px);
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: var(--bx-radius, 2px); color: var(--bx-text, #E9EAF0); font: inherit;
      padding: 0 8px; cursor: pointer; text-align: left;
    }
    .control:focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
      box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    .sum { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .sum.ph { color: var(--bx-subtle, #8E91A2); }
    .caret { color: var(--bx-muted, #A3A6B6); flex: none; }
    .menu {
      position: fixed; z-index: 3900; box-sizing: border-box;
      /* Size to the widest option (at least the control's width) — long
         provider refs must not squeeze into the control column and scroll. */
      width: max-content; max-width: min(480px, 92vw);
      max-height: 300px; overflow: auto; overscroll-behavior: contain;
      background: var(--bx-panel, #1F2028); border: 1px solid var(--bx-border, #33353F);
      border-radius: var(--bx-radius, 2px); box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); padding: 4px 0;
    }
    .menu.hidden { visibility: hidden; }
    .opt {
      display: flex; align-items: center; gap: 8px; min-height: var(--bx-row, 28px); padding: 0 12px;
      font: inherit; cursor: pointer; user-select: none; white-space: nowrap;
    }
    .opt:hover { background: var(--bx-hover, #2A2B34); }
    .opt input { margin: 0; flex: none; accent-color: var(--bx-accent, #8C9BFF); }
    .opt input:focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); }
    .empty { padding: 4px 12px; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  `];

  constructor() {
    super();
    this.options = [];
    this.selected = [];
    this.placeholder = '— none —';
    this._open = false;
    this._pos = null;
    this._onDocDown = (e) => { if (!e.composedPath().includes(this)) this._close(); };
    // An open list eats the Escape (so a window behind it doesn't close too).
    this._onKey = (e) => { if (e.key === 'Escape' && this._open) { e.stopPropagation(); this._close(); } };
    // Scrolling anywhere (except inside the list) or resizing moves the anchor.
    this._onMove = (e) => { if (e.target === this || (e.target instanceof Node && this.renderRoot.contains(e.target))) return; this._place(); };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._detach();
  }

  // Pin the list to the control: below it, or above when the viewport has no
  // room below; never past the edges; at least as wide as the control.
  async _place() {
    await this.updateComplete;
    const c = this.renderRoot.querySelector('.control');
    const m = this.renderRoot.querySelector('.menu');
    if (!c || !m) return;
    const r = c.getBoundingClientRect();
    const mw = m.offsetWidth, mh = m.offsetHeight;
    const W = window.innerWidth, H = window.innerHeight, M = 8;
    let left = r.left, top = r.bottom + 3;
    if (left + mw > W - M) left = Math.max(M, r.right - mw);
    if (top + mh > H - M && r.top - 3 - mh >= M) top = r.top - 3 - mh;
    top = Math.max(M, Math.min(top, H - M - mh));
    this._pos = { left, top, minWidth: r.width };
  }

  _norm() {
    return (this.options || []).map((o) =>
      typeof o === 'string'
        ? { value: o, label: o }
        : { value: o.value ?? o.id, label: o.label ?? String(o.value ?? o.id) });
  }

  _attach() {
    document.addEventListener('pointerdown', this._onDocDown, true);
    document.addEventListener('keydown', this._onKey, true);
    document.addEventListener('scroll', this._onMove, { capture: true, passive: true });
    window.addEventListener('resize', this._onMove);
  }
  _detach() {
    document.removeEventListener('pointerdown', this._onDocDown, true);
    document.removeEventListener('keydown', this._onKey, true);
    document.removeEventListener('scroll', this._onMove, { capture: true });
    window.removeEventListener('resize', this._onMove);
  }

  _toggleOpen() {
    if (this._open) { this._close(); return; }
    this._pos = null;
    this._open = true;
    this._attach();
    this._place();
  }
  _close() { if (!this._open) return; this._open = false; this._pos = null; this._detach(); }

  _toggle(value) {
    const set = new Set(this.selected || []);
    set.has(value) ? set.delete(value) : set.add(value);
    this.selected = [...set];
    this.dispatchEvent(new CustomEvent('change', { detail: { selected: this.selected }, bubbles: true, composed: true }));
  }

  render() {
    const opts = this._norm();
    const sel = new Set(this.selected || []);
    const summary = !sel.size ? this.placeholder
      : sel.size === 1 ? (opts.find((o) => sel.has(o.value))?.label ?? [...sel][0])
      : `${sel.size} selected`;
    return html`
      <button class="control" @click=${(e) => { e.stopPropagation(); this._toggleOpen(); }}
              title=${(this.selected || []).join(', ')}>
        <span class="sum ${sel.size ? '' : 'ph'}">${summary}</span>
        <bx-icon class="caret" name="caret-down"></bx-icon>
      </button>
      ${this._open ? html`
        <div class="menu ${this._pos ? '' : 'hidden'}"
             style=${this._pos ? `left:${this._pos.left}px; top:${this._pos.top}px; min-width:${this._pos.minWidth}px` : nothing}>
          ${opts.length ? opts.map((o) => html`
            <label class="opt">
              <input type="checkbox" .checked=${sel.has(o.value)} @change=${() => this._toggle(o.value)}>
              <span>${o.label}</span>
            </label>`) : html`<div class="empty">no providers</div>`}
        </div>` : nothing}`;
  }
}

customElements.define('bx-multiselect', BxMultiselect);
