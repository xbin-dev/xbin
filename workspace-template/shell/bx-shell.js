/**
 * <bx-shell> — the workspace shell layout: top bar, component sidebar, and
 * a dense, draggable card canvas. This lives in YOUR workspace (component
 * `shell/`), not in buxon's core: open a terminal here and restyle your
 * whole workspace live. Shells nest like any element — see shell/index.html.
 *
 * Usage (see root/index.html):
 *
 *   <script type="module" src="/c/shell/bx-shell.js"></script>
 *   <bx-shell name="my workspace">
 *     <bx-frame src="apps/welcome"></bx-frame>   <!-- pinned cards -->
 *   </bx-shell>
 *
 * The canvas is a set of vertical columns (count follows the width). Cards
 * stack top-to-bottom in a column and can be **dragged by their title bar**
 * to reorder within a column or move between columns — they snap into place.
 * <bx-frame> children are the pinned cards, adopted into the same closeable
 * chrome as components opened from the sidebar. Closing hides a card for the
 * session (pinned ones return on reload; unpin by removing the line in
 * root/index.html). Layout is per-session; theme tokens come from
 * /vendor/theme.css and can be overridden here.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import '/vendor/bx-frame.js';
import '/vendor/bx-grants.js';

const COL_WIDTH = 360; // target column width; column count = floor(canvas / this)

const RUNTIME_COLOR = {
  '': 'var(--bx-muted, #8794a1)',
  static: 'var(--bx-muted, #8794a1)',
  go: 'var(--bx-accent, #1e88e5)',
  node: 'var(--bx-green, #43a047)',
  python: 'var(--bx-amber, #f2a71b)',
  cgi: 'var(--bx-red, #e5484d)',
};

export class BxShell extends LitElement {
  static properties = {
    name: { type: String },
    _components: { state: true },
    // Canvas cards. Each: {path, col, height?, pinned?}
    _opened: { state: true },
    _cols: { state: true },
    _drag: { state: true }, // {path} while dragging
    _drop: { state: true },  // {col, idx} target slot
  };

  static styles = css`
    :host {
      display: flex; flex-direction: column; height: 100vh;
      background: var(--bx-bg, #f0f2f5);
      color: var(--bx-text, #33414e);
      font: var(--bx-font, 13px/1.45 -apple-system, "Segoe UI", system-ui, sans-serif);
    }

    /* ---- top bar ---- */
    .top {
      display: flex; align-items: center; gap: 10px;
      background: var(--bx-panel, #fff);
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      padding: 7px 12px; flex: none;
    }
    .logo { display: flex; align-items: center; gap: 7px; font-weight: 700; font-size: 14px; }
    .logo .dot {
      width: 18px; height: 18px; border-radius: 6px; background: var(--bx-accent, #1e88e5);
      display: inline-flex; align-items: center; justify-content: center;
      color: #fff; font-size: 11px; font-weight: 800;
    }
    .ws-chip {
      font-size: 11.5px; color: var(--bx-muted, #8794a1);
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 999px; padding: 1px 10px;
    }
    .top .spacer { flex: 1; }
    .top a.chip {
      display: inline-flex; align-items: center; gap: 6px; font-size: 12px;
      color: var(--bx-text, #33414e); text-decoration: none;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      padding: 3px 10px; background: var(--bx-panel, #fff);
    }
    .top a.chip:hover { background: var(--bx-panel-2, #f7f8fa); }
    .top a.chip .c { width: 7px; height: 7px; border-radius: 2px; }

    /* ---- body ---- */
    .body { display: flex; flex: 1; min-height: 0; }
    aside {
      width: 224px; flex: none; overflow-y: auto;
      background: var(--bx-panel, #fff);
      border-right: 1px solid var(--bx-border, #e4e8ed); padding: 8px 0 16px;
    }
    .group {
      display: flex; align-items: baseline; gap: 6px; padding: 10px 12px 3px;
      font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
      text-transform: uppercase; color: var(--bx-muted, #8794a1);
    }
    .group .n { font-weight: 500; color: var(--bx-accent, #1e88e5); }
    .item {
      display: flex; align-items: center; gap: 8px; padding: 3px 12px 3px 16px;
      cursor: pointer; font-size: 12.5px; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis;
    }
    .item:hover { background: var(--bx-panel-2, #f7f8fa); }
    .item.open { color: var(--bx-accent, #1e88e5); font-weight: 600; }
    .item .c { width: 7px; height: 7px; border-radius: 50%; flex: none; }
    .item .err { color: var(--bx-red, #e5484d); font-size: 10px; }
    .item .rt { margin-left: auto; font-size: 10px; color: var(--bx-muted, #8794a1); }

    main { flex: 1; min-width: 0; overflow-y: auto; padding: 14px; }
    .grants { margin-bottom: 12px; }

    /* ---- draggable column canvas ---- */
    .canvas { display: flex; gap: 14px; align-items: flex-start; }
    .col { flex: 1 1 0; min-width: 0; display: flex; flex-direction: column; gap: 14px; }
    .card {
      background: var(--bx-panel, #fff); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(16,24,40,.05));
      overflow: hidden;
    }
    .card.dragging { opacity: .35; }
    .card .head {
      display: flex; align-items: center; gap: 8px; padding: 6px 8px 6px 10px;
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      background: var(--bx-panel-2, #f7f8fa);
      cursor: grab; user-select: none; touch-action: none;
    }
    .card .head:active { cursor: grabbing; }
    .card .head .c { width: 8px; height: 8px; border-radius: 3px; flex: none; }
    .card .head .t {
      font-size: 12px; font-weight: 600; font-family: var(--bx-mono, ui-monospace, monospace);
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .card .head .spacer { flex: 1; }
    .card .head button {
      border: 0; background: transparent; color: var(--bx-muted, #8794a1);
      cursor: pointer; font-size: 13px; padding: 0 4px; line-height: 1;
    }
    .card .head button:hover { color: var(--bx-text, #33414e); }
    .drop {
      height: 3px; border-radius: 2px; background: var(--bx-accent, #1e88e5);
      margin: -8px 0; /* fold into the column gap */
    }
    .empty { color: var(--bx-muted, #8794a1); font-size: 12.5px; padding: 24px; text-align: center; }
  `;

  constructor() {
    super();
    this.name = 'workspace';
    this._components = [];
    this._opened = [];
    this._cols = 2;
    this._drag = null;
    this._drop = null;
    this._onMove = (e) => this._dragMove(e);
    this._onUp = (e) => this._dragEnd(e);
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.buxon?.events.on((e) => {
      if (e.type === 'reload' || e.type === 'grants') this._load();
    });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    this._ro?.disconnect();
    window.removeEventListener('pointermove', this._onMove);
    window.removeEventListener('pointerup', this._onUp);
  }

  firstUpdated() {
    const main = this.renderRoot.querySelector('main');
    this._ro = new ResizeObserver(() => {
      const cols = Math.max(1, Math.floor(main.clientWidth / COL_WIDTH));
      if (cols !== this._cols) {
        this._cols = cols;
        // Clamp any card whose column no longer exists.
        this._opened = this._opened.map((o) => o.col >= cols ? { ...o, col: cols - 1 } : o);
      }
    });
    this._ro.observe(main);

    const slot = this.renderRoot.querySelector('slot');
    const adopt = () => {
      const frames = slot.assignedElements()
        .filter((el) => el.tagName === 'BX-FRAME' && el.getAttribute('src'));
      if (frames.length === 0) return;
      const add = [];
      for (const f of frames) {
        const path = f.getAttribute('src');
        if (![...this._opened, ...add].some((o) => o.path === path)) {
          add.push({ path, height: f.getAttribute('height') ?? undefined, pinned: true,
                     col: (this._opened.length + add.length) % this._cols });
        }
        f.remove();
      }
      if (add.length) this._opened = [...this._opened, ...add];
    };
    slot.addEventListener('slotchange', adopt);
    adopt();
  }

  async _load() {
    try {
      const r = await (window.buxon?.fetch ?? fetch)('/api/buxon/components');
      if (r.ok) this._components = await r.json();
    } catch { /* buxond restarting; next event retries */ }
  }

  get _groups() {
    const g = new Map();
    for (const c of this._components) {
      if (c.path === 'root') continue; // framing root inside root recurses
      const top = c.path.includes('/') ? c.path.split('/')[0] : 'workspace';
      if (!g.has(top)) g.set(top, []);
      g.get(top).push(c);
    }
    return [...g.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  _isOpen(path) { return this._opened.some((o) => o.path === path); }

  _shortestCol() {
    const counts = Array(this._cols).fill(0);
    for (const o of this._opened) if (o.col < this._cols) counts[o.col]++;
    return counts.indexOf(Math.min(...counts));
  }

  _toggle(path) {
    this._opened = this._isOpen(path)
      ? this._opened.filter((o) => o.path !== path)
      : [{ path, col: this._shortestCol() }, ...this._opened];
  }

  _runtimeOf(path) {
    return this._components.find((c) => c.path === path)?.runtime ?? '';
  }

  // ---- drag ----
  _dragStart(ev, path) {
    if (ev.button !== 0 || ev.target.closest('button')) return;
    ev.preventDefault();
    this._drag = { path };
    this._drop = null;
    window.addEventListener('pointermove', this._onMove);
    window.addEventListener('pointerup', this._onUp);
  }

  _dragMove(ev) {
    if (!this._drag) return;
    const cols = [...this.renderRoot.querySelectorAll('.col')];
    if (cols.length === 0) return;
    // Nearest column by x.
    let colIdx = 0, best = Infinity;
    cols.forEach((el, i) => {
      const r = el.getBoundingClientRect();
      const cx = Math.max(r.left, Math.min(ev.clientX, r.right));
      const d = Math.abs(ev.clientX - (r.left + r.width / 2));
      if (ev.clientX >= r.left - 7 && ev.clientX <= r.right + 7 && d < best) { best = d; colIdx = i; }
    });
    // Insertion index by y among that column's cards (excluding the dragged one).
    const colEl = cols[colIdx];
    const cards = [...colEl.querySelectorAll('.card')].filter((c) => c.dataset.path !== this._drag.path);
    let idx = cards.length;
    for (let i = 0; i < cards.length; i++) {
      const r = cards[i].getBoundingClientRect();
      if (ev.clientY < r.top + r.height / 2) { idx = i; break; }
    }
    this._drop = { col: colIdx, idx };
  }

  _dragEnd() {
    window.removeEventListener('pointermove', this._onMove);
    window.removeEventListener('pointerup', this._onUp);
    const drag = this._drag, drop = this._drop;
    this._drag = null; this._drop = null;
    if (!drag || !drop) return;

    const moved = this._opened.find((o) => o.path === drag.path);
    if (!moved) return;
    const rest = this._opened.filter((o) => o.path !== drag.path);
    // Cards already in the target column, in order, give us the insertion point.
    const inCol = rest.filter((o) => o.col === drop.col);
    const before = inCol[drop.idx]; // undefined => append to column
    const next = rest.filter((o) => o.col !== drop.col || o !== before);
    const out = [];
    let inserted = false;
    const target = { ...moved, col: drop.col };
    for (const o of rest) {
      if (!inserted && o === before) { out.push(target); inserted = true; }
      out.push(o);
    }
    if (!inserted) out.push(target);
    this._opened = out;
  }

  _cardTemplate(o) {
    return html`
      <div class="card ${this._drag?.path === o.path ? 'dragging' : ''}" data-path=${o.path}>
        <div class="head" @pointerdown=${(e) => this._dragStart(e, o.path)}>
          <span class="c" style="background:${RUNTIME_COLOR[this._runtimeOf(o.path)] ?? RUNTIME_COLOR['']}"></span>
          <span class="t">${o.path}</span>
          <span class="spacer"></span>
          <button title="open full page" @click=${() => window.open(`/c/${o.path}/`, '_blank')}>⤢</button>
          <button title=${o.pinned ? 'close — pinned in root/index.html, reload restores it' : 'close'}
                  @click=${() => this._toggle(o.path)}>✕</button>
        </div>
        <bx-frame src=${o.path} height=${o.height ?? nothing}></bx-frame>
      </div>`;
  }

  _column(colIdx) {
    const cards = this._opened.filter((o) => o.col === colIdx);
    const showDrop = this._drag && this._drop?.col === colIdx;
    // Cards in this column excluding the dragged one, for indicator placement.
    const visible = cards.filter((o) => o.path !== this._drag?.path);
    return html`
      <div class="col" data-col=${colIdx}>
        ${visible.map((o, i) => html`
          ${showDrop && this._drop.idx === i ? html`<div class="drop"></div>` : nothing}
          ${repeat([o], (x) => x.path, (x) => this._cardTemplate(x))}
        `)}
        ${showDrop && this._drop.idx >= visible.length ? html`<div class="drop"></div>` : nothing}
      </div>`;
  }

  render() {
    return html`
      <div class="top">
        <span class="logo"><span class="dot">b</span>buxon</span>
        <span class="ws-chip">${this.name}</span>
        <span class="spacer"></span>
        <a class="chip" href="/docs/" target="_blank"><span class="c" style="background:var(--bx-green,#43a047)"></span>docs</a>
        <a class="chip" href="/api/buxon/components" target="_blank"><span class="c" style="background:var(--bx-amber,#f2a71b)"></span>components</a>
      </div>
      <div class="body">
        <aside>
          ${this._groups.map(([top, comps]) => html`
            <div class="group">${top} <span class="n">${comps.length}</span></div>
            ${comps.map((c) => html`
              <div class="item ${this._isOpen(c.path) ? 'open' : ''}"
                   title=${c.manifestError ? `${c.path} — manifest error: ${c.manifestError}` : c.path}
                   @click=${() => this._toggle(c.path)}>
                <span class="c" style="background:${RUNTIME_COLOR[c.runtime ?? ''] ?? RUNTIME_COLOR['']}"></span>
                <span>${c.path.includes('/') ? c.path.slice(c.path.indexOf('/') + 1) : c.path}</span>
                ${c.manifestError ? html`<span class="err">⚠</span>` : nothing}
                <span class="rt">${c.runtime || ''}</span>
              </div>`)}
          `)}
          ${this._groups.length === 0 ? html`<div class="empty">no components yet<br>· mkdir one ·</div>` : nothing}
        </aside>
        <main>
          <div class="grants"><bx-grants></bx-grants></div>
          <div class="canvas">
            ${Array.from({ length: this._cols }, (_, i) => this._column(i))}
          </div>
          <slot></slot>
        </main>
      </div>
    `;
  }
}

customElements.define('bx-shell', BxShell);
