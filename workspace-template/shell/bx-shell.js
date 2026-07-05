/**
 * <bx-shell> — the workspace shell: top bar, screen tabs, component sidebar,
 * and a dense, draggable card canvas. Lives in YOUR workspace (component
 * `shell/`), not in xbin's core — open a terminal here and restyle it live.
 *
 * Usage (see root/index.html):
 *
 *   <script type="module" src="/c/shell/bx-shell.js"></script>
 *   <bx-shell name="my workspace">
 *     <bx-frame src="apps/welcome"></bx-frame>   <!-- seeds the first screen -->
 *   </bx-shell>
 *
 * Layout is **persisted per user** via the prefs API (server-side, so it
 * follows you across browsers/devices). Organise work into named **screens**
 * (the tabs at the top) — each screen holds its own set of tiles laid out in
 * vertical columns; drag a card by its title bar to reorder within a column
 * or move it between columns. Open tiles from the sidebar; close with ✕.
 * Unpin a card (⧉) to pop it out as a floating, draggable + resizable window
 * (pin back with ▣); its position/size is saved in the layout like everything
 * else. Floating windows are per-screen.
 * The <bx-frame> children of <bx-shell> seed the first screen on first run;
 * after that your saved layout is the source of truth. Theme tokens come from
 * /vendor/theme.css and can be overridden here.
 */
import { LitElement, html, css, nothing, repeat } from 'lit';
import '/vendor/bx-frame.js';
import '/vendor/bx-grants.js';
import '/vendor/bx-bindings.js';

const COL_WIDTH = 700; // min column width; column count = floor(canvas / this).
// Tiles must be usable at this width with NO horizontal scroll — see AGENTS.md.
const LAYOUT_PREF = 'layout';

// dragShield lays a transparent full-viewport layer over the page for the
// duration of a pointer drag, so tile <iframe>s can't swallow the pointer when
// the cursor races over them (which otherwise stalls window pointermove until
// the cursor leaves the iframe). Returns a cleanup fn.
function dragShield(cursor = 'grabbing') {
  const el = document.createElement('div');
  el.style.cssText = `position:fixed; inset:0; z-index:2147483647; cursor:${cursor};`;
  document.body.appendChild(el);
  return () => el.remove();
}

const RUNTIME_COLOR = {
  '': 'var(--bx-muted, #8794a1)',
  static: 'var(--bx-muted, #8794a1)',
  go: 'var(--bx-accent, #1e88e5)',
  node: 'var(--bx-green, #43a047)',
  python: 'var(--bx-amber, #f2a71b)',
  cgi: 'var(--bx-red, #e5484d)',
};

const uid = () => Math.random().toString(36).slice(2, 9);

// Shared z-order for floating (unpinned) tile windows. Kept below bx-frame's
// terminal pop-ups (which start at 2000) so a terminal always sits on top.
let zTop = 100;

export class BxShell extends LitElement {
  static properties = {
    name: { type: String },
    _components: { state: true },
    _screens: { state: true }, // [{id, name, tiles: [{path, col, height?, float?:{x,y,w,h,z}}]}]
    _active: { state: true },  // active screen id
    _cols: { state: true },
    _drag: { state: true },    // {path} while dragging
    _drop: { state: true },    // {col, idx} target slot
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
    .logo { display: flex; align-items: center; gap: 8px; font-weight: 800; font-size: 14px; letter-spacing: .04em; }
    .logo .mark { flex: none; }
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

    /* ---- screen tabs ---- */
    .tabs {
      display: flex; align-items: stretch; gap: 2px; flex: none;
      background: var(--bx-panel-2, #f7f8fa);
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      padding: 4px 8px 0; overflow-x: auto;
    }
    .tab {
      display: flex; align-items: center; gap: 6px; cursor: pointer;
      font-size: 12.5px; color: var(--bx-muted, #8794a1);
      background: transparent; border: 1px solid transparent; border-bottom: none;
      border-radius: 6px 6px 0 0; padding: 4px 10px; white-space: nowrap; user-select: none;
    }
    .tab.on {
      background: var(--bx-bg, #f0f2f5); color: var(--bx-text, #33414e);
      border-color: var(--bx-border, #e4e8ed); margin-bottom: -1px;
    }
    .tab .x {
      border: 0; background: transparent; color: var(--bx-muted, #8794a1);
      cursor: pointer; font-size: 12px; line-height: 1; padding: 0 1px; opacity: .6;
    }
    .tab .x:hover { opacity: 1; color: var(--bx-red, #e5484d); }
    .tab.add { color: var(--bx-muted, #8794a1); font-weight: 600; }

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
    .grants { margin-bottom: 12px; display: flex; flex-direction: column; gap: 8px; }

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

    /* ---- floating (unpinned) tile windows ---- */
    .float {
      position: fixed; z-index: 100;
      display: flex; flex-direction: column;
      border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: var(--bx-radius, 6px);
      background: var(--bx-panel, #fff);
      box-shadow: 2px 6px 22px rgba(16, 24, 40, 0.22);
      overflow: hidden; resize: both; min-width: 220px; min-height: 120px;
    }
    .float > .card {
      flex: 1; min-height: 0; display: flex; flex-direction: column;
      border: 0; border-radius: 0; box-shadow: none;
    }
    .float .fbody { flex: 1; min-height: 0; overflow: auto; }
  `;

  constructor() {
    super();
    this.name = 'workspace';
    this._components = [];
    this._screens = [];
    this._active = '';
    this._cols = 2;
    this._drag = null;
    this._drop = null;
    this._seeds = [];        // {path, height} from slotted <bx-frame> children
    this._layoutLoaded = false;
    this._saveTimer = null;
    this._onMove = (e) => this._dragMove(e);
    this._onUp = (e) => this._dragEnd(e);
    this._onBlur = () => this._raiseFocusedFloat();
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._loadLayout();
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'reload' || e.type === 'grants') this._load();
    });
    window.addEventListener('blur', this._onBlur);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    this._ro?.disconnect();
    window.removeEventListener('pointermove', this._onMove);
    window.removeEventListener('pointerup', this._onUp);
    window.removeEventListener('blur', this._onBlur);
  }

  firstUpdated() {
    const main = this.renderRoot.querySelector('main');
    this._ro = new ResizeObserver(() => {
      const cols = Math.max(1, Math.floor(main.clientWidth / COL_WIDTH));
      if (cols !== this._cols) {
        this._cols = cols;
        this._mutateTiles((tiles) => tiles.map((o) => o.col >= cols ? { ...o, col: cols - 1 } : o));
      }
    });
    this._ro.observe(main);

    const slot = this.renderRoot.querySelector('slot');
    const adopt = () => {
      for (const f of slot.assignedElements()) {
        if (f.tagName !== 'BX-FRAME' || !f.getAttribute('src')) continue;
        const path = f.getAttribute('src');
        if (!this._seeds.some((s) => s.path === path)) {
          this._seeds.push({ path, height: f.getAttribute('height') ?? undefined });
        }
        f.remove();
      }
      this._ensureScreen();
    };
    slot.addEventListener('slotchange', adopt);
    adopt();
  }

  // ---- persistence ----
  async _loadLayout() {
    try {
      const r = await window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`);
      if (r?.ok) {
        const l = await r.json();
        if (Array.isArray(l?.screens) && l.screens.length) {
          this._screens = l.screens;
          this._active = l.screens.some((s) => s.id === l.active) ? l.active : l.screens[0].id;
        }
      }
    } catch { /* offline / restarting — fall through to seed */ }
    this._layoutLoaded = true;
    this._ensureScreen();
  }

  // Seed a default screen from the slotted <bx-frame> pins, but only once the
  // saved layout has been consulted and found empty.
  _ensureScreen() {
    if (!this._layoutLoaded || this._screens.length) return;
    const tiles = this._seeds.map((s, i) => ({ ...s, col: i % this._cols, pinned: true }));
    this._screens = [{ id: uid(), name: 'Home', tiles }];
    this._active = this._screens[0].id;
    this._save();
  }

  _save() {
    clearTimeout(this._saveTimer);
    this._saveTimer = setTimeout(() => {
      window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ screens: this._screens, active: this._active }),
      }).catch(() => { /* best-effort; retried on next change */ });
    }, 400);
  }

  // ---- active screen helpers ----
  get _screen() { return this._screens.find((s) => s.id === this._active); }
  get _tiles() { return this._screen?.tiles ?? []; }

  // Replace the active screen's tiles via fn(copy) → new array, then persist
  // (debounced, so rapid changes like drag/resize coalesce).
  _mutateTiles(fn) {
    if (!this._screen) return;
    const tiles = fn(this._tiles.map((t) => ({ ...t })));
    this._screens = this._screens.map((s) => s.id === this._active ? { ...s, tiles } : s);
    this._save();
  }

  async _load() {
    try {
      const r = await (window.xbin?.fetch ?? fetch)('/api/xbin/components');
      if (r.ok) this._components = await r.json();
    } catch { /* xbind restarting; next event retries */ }
  }

  get _groups() {
    const g = new Map();
    for (const c of this._components) {
      if (c.path === 'root') continue; // framing root inside root recurses
      if (c.template) continue; // blueprints aren't openable tiles (instantiate via Tile Manager)
      const top = c.path.includes('/') ? c.path.split('/')[0] : 'workspace';
      if (!g.has(top)) g.set(top, []);
      g.get(top).push(c);
    }
    return [...g.entries()].sort(([a], [b]) => a.localeCompare(b));
  }

  _isOpen(path) { return this._tiles.some((o) => o.path === path); }

  _shortestCol() {
    const counts = Array(this._cols).fill(0);
    for (const o of this._tiles) if (!o.float && o.col < this._cols) counts[o.col]++;
    return counts.indexOf(Math.min(...counts));
  }

  _toggle(path) {
    const col = this._shortestCol();
    this._mutateTiles((tiles) => this._isOpen(path)
      ? tiles.filter((o) => o.path !== path)
      : [{ path, col }, ...tiles]);
  }

  _runtimeOf(path) {
    return this._components.find((c) => c.path === path)?.runtime ?? '';
  }

  // ---- screens ----
  _switchScreen(id) { this._active = id; this._save(); }
  _addScreen() {
    const s = { id: uid(), name: `Screen ${this._screens.length + 1}`, tiles: [] };
    this._screens = [...this._screens, s];
    this._active = s.id;
    this._save();
  }
  _renameScreen(id) {
    const s = this._screens.find((x) => x.id === id);
    const name = prompt('Screen name:', s?.name ?? '');
    if (name == null || !name.trim()) return;
    this._screens = this._screens.map((x) => x.id === id ? { ...x, name: name.trim() } : x);
    this._save();
  }
  _closeScreen(id, ev) {
    ev.stopPropagation();
    if (this._screens.length <= 1) return; // keep at least one
    const s = this._screens.find((x) => x.id === id);
    if (s.tiles.length && !confirm(`Close screen "${s.name}" and its ${s.tiles.length} tile(s)?`)) return;
    const remaining = this._screens.filter((x) => x.id !== id);
    this._screens = remaining;
    if (this._active === id) this._active = remaining[0].id;
    this._save();
  }

  // ---- drag ----
  _dragStart(ev, path) {
    if (ev.button !== 0 || ev.target.closest('button')) return;
    ev.preventDefault();
    this._drag = { path };
    this._drop = null;
    this._shield = dragShield();
    window.addEventListener('pointermove', this._onMove);
    window.addEventListener('pointerup', this._onUp);
  }

  _dragMove(ev) {
    if (!this._drag) return;
    const cols = [...this.renderRoot.querySelectorAll('.col')];
    if (cols.length === 0) return;
    let colIdx = 0, best = Infinity;
    cols.forEach((el, i) => {
      const r = el.getBoundingClientRect();
      const d = Math.abs(ev.clientX - (r.left + r.width / 2));
      if (ev.clientX >= r.left - 7 && ev.clientX <= r.right + 7 && d < best) { best = d; colIdx = i; }
    });
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
    this._shield?.(); this._shield = null;
    const drag = this._drag, drop = this._drop;
    this._drag = null; this._drop = null;
    if (!drag || !drop) return;

    this._mutateTiles((tiles) => {
      const moved = tiles.find((o) => o.path === drag.path);
      if (!moved) return tiles;
      const rest = tiles.filter((o) => o.path !== drag.path);
      const inCol = rest.filter((o) => o.col === drop.col);
      const before = inCol[drop.idx];
      const target = { ...moved, col: drop.col };
      const out = [];
      let inserted = false;
      for (const o of rest) {
        if (!inserted && o === before) { out.push(target); inserted = true; }
        out.push(o);
      }
      if (!inserted) out.push(target);
      return out;
    });
  }

  _cardTemplate(o, floating = false) {
    const frame = html`<bx-frame src=${o.path} height=${floating ? nothing : (o.height ?? nothing)}></bx-frame>`;
    return html`
      <div class="card ${this._drag?.path === o.path ? 'dragging' : ''}" data-path=${o.path}>
        <div class="head" @pointerdown=${(e) => (floating ? this._floatDragStart(e, o.path) : this._dragStart(e, o.path))}>
          <span class="c" style="background:${RUNTIME_COLOR[this._runtimeOf(o.path)] ?? RUNTIME_COLOR['']}"></span>
          <span class="t">${o.path}</span>
          <span class="spacer"></span>
          <button title=${floating ? 'pin back into the column layout' : 'unpin into a floating window'}
                  @click=${() => this._togglePin(o.path)}>${floating ? '▣' : '⧉'}</button>
          <button title="open full page" @click=${() => window.open(`/c/${o.path}/`, '_blank')}>⤢</button>
          <button title="close" @click=${() => this._toggle(o.path)}>✕</button>
        </div>
        ${floating ? html`<div class="fbody">${frame}</div>` : frame}
      </div>`;
  }

  // ---- floating (unpinned) windows ----
  // A tile with a `float:{x,y,w,h,z}` is rendered as a viewport-fixed window
  // instead of in a column; the geometry is part of the tile, so it persists in
  // the saved layout. Pinning/unpinning re-creates the tile's <bx-frame> (moving
  // between two DOM containers) — a brief reload, but any open terminal on it
  // reattaches via bx-frame's session persistence.
  _floatTemplate(o) {
    const f = o.float;
    return html`
      <div class="float" data-path=${o.path}
           style="left:${f.x}px; top:${f.y}px; width:${f.w}px; height:${f.h}px; z-index:${f.z ?? 100};"
           @pointerdown=${() => this._floatFront(o.path)}
           @pointerup=${(e) => this._floatCommit(e, o.path)}>
        ${this._cardTemplate(o, true)}
      </div>`;
  }

  _togglePin(path) {
    // Read the current on-screen rect before the mutation re-renders.
    const init = this._initialFloat(path);
    this._mutateTiles((tiles) => tiles.map((o) => {
      if (o.path !== path) return o;
      if (o.float) { const { float, ...rest } = o; return rest; } // pin back to its column
      return { ...o, float: init };                                // unpin → floating window
    }));
  }

  _initialFloat(path) {
    const el = this.renderRoot.querySelector(`.card[data-path="${path}"]`);
    const r = el?.getBoundingClientRect();
    const w = Math.round(Math.min(r?.width || 480, window.innerWidth - 16));
    const h = Math.round(Math.min(r?.height || 340, 520, window.innerHeight - 16));
    const x = Math.max(8, Math.min(Math.round((r?.left ?? 120) + 28), window.innerWidth - w - 8));
    const y = Math.max(8, Math.min(Math.round((r?.top ?? 90) + 20), window.innerHeight - h - 8));
    return { x, y, w, h, z: ++zTop };
  }

  _floatWin(path) { return this.renderRoot.querySelector(`.float[data-path="${path}"]`); }

  // Raise a floating window to the top — but only if it isn't already there, so
  // repeatedly clicking the front window doesn't churn the layout. z is part of
  // the tile, so the stacking order persists.
  _floatFront(path) {
    const floats = this._tiles.filter((o) => o.float);
    if (floats.length < 2) return;
    const o = floats.find((t) => t.path === path);
    if (!o) return;
    const maxZ = Math.max(...floats.map((t) => t.float.z ?? 100));
    if ((o.float.z ?? 100) >= maxZ) return; // already on top
    zTop = maxZ + 1;
    this._setFloat(path, { z: zTop });
  }

  // A click inside a tile's <iframe> focuses it and blurs the top window (the
  // iframe swallows the pointerdown, so .float's own handler can't fire). Walk
  // the shadow roots to the focused iframe and raise its floating window, so
  // clicking anywhere in a window — not just its title bar — brings it forward.
  _raiseFocusedFloat() {
    setTimeout(() => {
      let el = document.activeElement;
      while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
      if (el?.tagName !== 'IFRAME') return;
      const win = el.getRootNode()?.host?.closest?.('.float');
      if (win) this._floatFront(win.dataset.path);
    }, 0);
  }

  // Commit a resize (via the CSS resize handle) back into the tile; skip clicks
  // that didn't change the size, so buttons don't churn the layout.
  _floatCommit(e, path) {
    const win = e.currentTarget;
    const o = this._tiles.find((t) => t.path === path);
    if (!o?.float) return;
    if (win.offsetWidth === o.float.w && win.offsetHeight === o.float.h) return;
    this._setFloat(path, { w: win.offsetWidth, h: win.offsetHeight });
  }

  _floatDragStart(ev, path) {
    if (ev.button !== 0 || ev.target.closest('button, select')) return;
    ev.preventDefault();
    this._floatFront(path);
    const win = this._floatWin(path);
    if (!win) return;
    const dx = ev.clientX - win.offsetLeft, dy = ev.clientY - win.offsetTop;
    const shield = dragShield();
    const move = (e) => {
      const x = Math.max(-win.offsetWidth + 60, Math.min(e.clientX - dx, window.innerWidth - 40));
      const y = Math.max(0, Math.min(e.clientY - dy, window.innerHeight - 24));
      win.style.left = x + 'px'; win.style.top = y + 'px';
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      shield();
      this._setFloat(path, { x: win.offsetLeft, y: win.offsetTop });
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _setFloat(path, patch) {
    this._mutateTiles((tiles) => tiles.map((o) =>
      o.path === path && o.float ? { ...o, float: { ...o.float, ...patch } } : o));
  }

  _column(colIdx) {
    const cards = this._tiles.filter((o) => o.col === colIdx && !o.float);
    const showDrop = this._drag && this._drop?.col === colIdx;
    // The dragged card stays MOUNTED (dimmed ghost in place) rather than being
    // filtered out — unmounting it destroys its <bx-frame>, which kills any
    // terminal window the user has open on that tile. The drop indicator is
    // positioned among the non-dragged cards (idx from _dragMove).
    let vi = 0;
    return html`
      <div class="col" data-col=${colIdx}>
        ${cards.map((o) => {
          const dragged = this._drag?.path === o.path;
          const drop = showDrop && !dragged && this._drop.idx === vi;
          if (!dragged) vi++;
          return html`
            ${drop ? html`<div class="drop"></div>` : nothing}
            ${repeat([o], (x) => x.path, (x) => this._cardTemplate(x))}
          `;
        })}
        ${showDrop && this._drop.idx >= vi ? html`<div class="drop"></div>` : nothing}
      </div>`;
  }

  render() {
    return html`
      <div class="top">
        <span class="logo">
          <svg class="mark" viewBox="0 0 64 64" width="20" height="20" aria-hidden="true">
            <path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="var(--bx-accent,#1e88e5)"></path>
            <path d="M21 21 43 43M43 21 21 43" stroke="#fff" stroke-width="9" stroke-linecap="butt"></path>
            <circle cx="53" cy="11" r="2.6" fill="#fff" opacity=".7"></circle>
            <circle cx="11" cy="53" r="2.6" fill="#fff" opacity=".7"></circle>
          </svg>
          X/BIN</span>
        <span class="ws-chip">${this.name}</span>
        <span class="spacer"></span>
        <a class="chip" href="/docs/" target="_blank"><span class="c" style="background:var(--bx-green,#43a047)"></span>docs</a>
        <a class="chip" href="/logout" @click=${(e) => { e.preventDefault(); fetch('/logout', { method: 'POST' }).then(() => location.reload()); }}><span class="c" style="background:var(--bx-red,#e5484d)"></span>sign out</a>
      </div>

      <div class="tabs">
        ${this._screens.map((s) => html`
          <div class="tab ${s.id === this._active ? 'on' : ''}"
               @click=${() => this._switchScreen(s.id)}
               @dblclick=${() => this._renameScreen(s.id)}
               title="double-click to rename">
            <span>${s.name}</span>
            ${this._screens.length > 1
              ? html`<button class="x" @click=${(e) => this._closeScreen(s.id, e)}>✕</button>` : nothing}
          </div>`)}
        <div class="tab add" @click=${() => this._addScreen()} title="new screen">+</div>
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
          <div class="grants"><bx-grants></bx-grants><bx-bindings></bx-bindings></div>
          <div class="canvas">
            ${Array.from({ length: this._cols }, (_, i) => this._column(i))}
          </div>
          ${this._tiles.length === 0 ? html`<div class="empty">empty screen — open a tile from the sidebar</div>` : nothing}
          <slot style="display:none"></slot>
        </main>
      </div>

      ${repeat(this._tiles.filter((o) => o.float), (o) => o.path, (o) => this._floatTemplate(o))}
    `;
  }
}

customElements.define('bx-shell', BxShell);
