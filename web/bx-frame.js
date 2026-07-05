/**
 * <bx-frame src="apps/calendar"> — the core xbin element: renders a
 * component's index.html in a same-origin iframe, carries the always-visible
 * 7×7 edit button, live-reloads on source changes, shows build errors as an
 * overlay, and hosts the terminal pop-up (persistent PTY sessions cwd'd to
 * the component's source directory).
 *
 * Attributes:
 *   src     — component path (workspace-relative)
 *   height  — fixed CSS height; omit for auto-height (the framed document
 *             reports its size via xbin-client.js)
 *   no-edit — hide the edit button
 *
 * The edit button opens a floating terminal window: anchored at the frame's
 * top-right corner when opened, draggable by its title bar, resizable by the
 * native bottom-right handle (ctrl+scroll inside adjusts the font). It uses
 * viewport-fixed positioning so container overflow clipping (e.g. shell
 * cards) can't cut it off; windows share a bring-to-front z-order.
 *
 * See /docs/elements.md.
 */
import { LitElement, html, css, nothing } from 'lit';
import { onEvent, mountedFrames, isReloadTarget } from '/vendor/events-socket.js';
import '/vendor/bx-terminal.js';

// Shared z-order for all terminal windows on the page.
let zTop = 2000;

// Host GPU inventory (shared, fetched once) — populates the terminal GPU picker.
let _gpuInv;
function gpuInventory() {
  _gpuInv ??= fetch('/api/xbin/gpus')
    .then((r) => (r.ok ? r.json() : { gpus: [] }))
    .then((d) => d.gpus || [])
    .catch(() => []);
  return _gpuInv;
}

// dragShield lays a transparent full-viewport layer over the page during a
// title-bar drag, so tile <iframe>s (this one and any others) can't capture the
// pointer when the cursor races ahead of the window — which otherwise stalls
// the drag until the cursor re-enters the workspace background. Returns cleanup.
function dragShield(cursor = 'grabbing') {
  const el = document.createElement('div');
  el.style.cssText = `position:fixed; inset:0; z-index:2147483647; cursor:${cursor};`;
  document.body.appendChild(el);
  return () => el.remove();
}

export class BxFrame extends LitElement {
  static properties = {
    src: { type: String },
    height: { type: String },
    _termOpen: { state: true },
    _sessions: { state: true },
    _active: { state: true },
    _gpus: { state: true },
    _buildError: { state: true },
    _autoHeight: { state: true },
  };

  static styles = css`
    :host { display: block; position: relative; }
    .frame-wrap { position: relative; }
    iframe {
      display: block; width: 100%; border: 0;
      height: var(--bx-frame-height, 100%);
      background: transparent;
    }
    .edit {
      position: absolute; top: 2px; right: 2px;
      width: 7px; height: 7px; padding: 0; border: 0; border-radius: 2px;
      background: var(--bx-accent, #f5a623);
      opacity: 0.35; cursor: pointer; z-index: 10;
    }
    .edit:hover { opacity: 1; }
    .overlay {
      position: absolute; inset: 0; z-index: 9; overflow: auto;
      background: color-mix(in srgb, var(--bx-panel, #fff) 96%, var(--bx-red, #e5484d));
      color: var(--bx-red, #b3261e);
      font: 11.5px/1.55 var(--bx-mono, ui-monospace, monospace);
      padding: 10px 12px; margin: 0; white-space: pre-wrap;
      border-top: 2px solid var(--bx-red, #e5484d);
    }
    .overlay b { color: var(--bx-red, #b3261e); }

    /* ---- floating terminal window ---- */
    .pop {
      position: fixed;
      display: flex; flex-direction: column;
      background: var(--bx-panel, #fff);
      border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 8px;
      box-shadow: 0 8px 28px rgba(16, 24, 40, 0.20), var(--bx-shadow, 0 1px 2px rgba(16,24,40,.05));
      resize: both; overflow: hidden;
      min-width: 380px; min-height: 220px;
    }
    .titlebar {
      display: flex; align-items: center; gap: 2px;
      background: var(--bx-panel-2, #f7f8fa);
      border-bottom: 1px solid var(--bx-border, #e4e8ed);
      padding: 3px 6px; user-select: none; cursor: grab;
      touch-action: none; flex: none;
    }
    .titlebar:active { cursor: grabbing; }
    .titlebar .path {
      color: var(--bx-text, #33414e); font-weight: 600;
      font: 11px var(--bx-mono, ui-monospace, monospace);
      padding: 0 8px 0 4px; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis;
    }
    .titlebar .spacer { flex: 1; }
    .titlebar button {
      border: 1px solid transparent; background: transparent;
      color: var(--bx-muted, #8794a1);
      font: 11px var(--bx-mono, ui-monospace, monospace); padding: 1px 7px;
      border-radius: 4px; cursor: pointer;
    }
    .titlebar button.on {
      background: var(--bx-panel, #fff);
      border-color: var(--bx-border, #e4e8ed);
      color: var(--bx-text, #33414e);
    }
    .titlebar button:hover { color: var(--bx-text, #33414e); }
    .titlebar select.scope {
      margin-left: 2px; border: 1px solid var(--bx-border, #e4e8ed);
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      font: 11px var(--bx-mono, ui-monospace, monospace);
      padding: 1px 4px; border-radius: 4px; cursor: pointer;
    }
    .term-host { flex: 1; min-height: 0; background: #1b1e24; }
  `;

  constructor() {
    super();
    this._termOpen = false;
    this._sessions = []; // [{id: string|null, net, gpu}] — id null until server assigns
    this._active = 0;
    this._gpus = []; // host GPU inventory (empty unless a GPU host)
    this._buildError = null;
    this._autoHeight = false;
    this._pop = null; // {x, y, w, h} — owned imperatively after open
    this._offEvents = null;
    this._onMsg = (e) => this._message(e);
  }

  connectedCallback() {
    super.connectedCallback();
    mountedFrames.add(this);
    this._autoHeight = !this.height && !this.style.height;
    this._offEvents = onEvent((e) => this._event(e));
    window.addEventListener('message', this._onMsg);
    this._restoreTerm();
  }

  // Persist terminal session ids + window state per component, and save whenever
  // the reactive terminal state changes, so a page reload reattaches to the
  // still-running server-side session(s) with scrollback instead of orphaning
  // them and opening a fresh shell. (Pop geometry is imperative → saved in the
  // drag/resize handlers.)
  updated(changed) {
    if (changed.has('_sessions') || changed.has('_active') || changed.has('_termOpen')) {
      this._saveTerm();
    }
  }

  _termKey() { return `bx-term:${this.src}`; }

  _loadTerm() {
    try { const r = localStorage.getItem(this._termKey()); return r ? JSON.parse(r) : null; }
    catch { return null; }
  }

  _saveTerm() {
    try {
      const sessions = this._sessions
        .filter((s) => s.id) // only server-assigned sessions can be reattached
        .map((s) => ({ id: s.id, net: s.net, gpu: s.gpu }));
      if (!sessions.length && !this._termOpen) { localStorage.removeItem(this._termKey()); return; }
      localStorage.setItem(this._termKey(), JSON.stringify({
        open: !!this._termOpen, active: this._active, pop: this._pop, sessions,
      }));
    } catch { /* storage disabled/full — non-fatal */ }
  }

  // Restore persisted sessions on mount; reopen the pop-up if it was open so the
  // <bx-terminal>s reattach (a stale id falls back to a fresh session there).
  _restoreTerm() {
    const saved = this._loadTerm();
    if (!saved?.sessions?.length) return;
    this._sessions = saved.sessions.map((s) => ({
      id: s.id ?? null, net: s.net || 'internet', gpu: s.gpu || 'none',
    }));
    this._active = Math.min(Math.max(0, saved.active | 0), this._sessions.length - 1);
    if (saved.pop) this._pop = { ...saved.pop };
    if (saved.open) {
      this.updateComplete.then(() => {
        this._termOpen = true;
        this.updateComplete.then(() => this._front());
      });
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    mountedFrames.delete(this);
    this._offEvents?.();
    window.removeEventListener('message', this._onMsg);
  }

  get _iframe() { return this.renderRoot?.querySelector('iframe'); }
  get _popEl() { return this.renderRoot?.querySelector('.pop'); }

  _event(e) {
    if (!e.component) return;
    const mine = e.component === this.src || e.component.startsWith(this.src + '/');
    if (!mine) return;
    switch (e.type) {
      case 'reload':
        if (isReloadTarget(this, e.component)) this._reload();
        break;
      case 'build-error':
        if (e.component === this.src) this._buildError = e.text || 'build failed';
        break;
      case 'build-ok':
        if (e.component === this.src) this._buildError = null;
        break;
      case 'grants':
        // A grant affecting this component changed — reload so a frontend
        // that was 403'ing retries against the new permissions.
        if (e.component === this.src) this._reload();
        break;
    }
  }

  _reload() {
    this._buildError = null;
    try { this._iframe?.contentWindow?.location.reload(); }
    catch { if (this._iframe) this._iframe.src = this._url(); }
  }

  _message(e) {
    if (!this._autoHeight) return;
    const d = e.data;
    if (d?.type !== 'xbin:resize') return;
    if (e.source !== this._iframe?.contentWindow) return;
    const h = Math.max(24, Math.min(d.height, 20000));
    this.style.setProperty('--bx-frame-height', h + 'px');
  }

  _url() { return `/c/${this.src}/`; }

  // ---- terminal window ----

  _toggleTerm() {
    if (this._termOpen) { this._termOpen = false; return; }
    if (!this._pop) {
      // Anchor at the frame's top-right corner, clamped to the viewport.
      const r = this.getBoundingClientRect();
      const w = 560, h = 320;
      this._pop = {
        x: Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8)),
        y: Math.max(8, Math.min(r.top + 8, window.innerHeight - h - 8)),
        w, h,
      };
    }
    this._termOpen = true;
    if (this._gpus.length === 0) gpuInventory().then((g) => { this._gpus = g; });
    if (this._sessions.length === 0) this._newTerm();
    this.updateComplete.then(() => this._front());
  }

  _front() {
    const el = this._popEl;
    if (el) el.style.zIndex = String(++zTop);
  }

  // Title-bar drag; the native CSS resize handle owns width/height, and we
  // read the final geometry back into _pop so reopening keeps it.
  _dragStart(ev) {
    if (ev.button !== 0 || ev.target.closest('button, select')) return;
    const el = this._popEl;
    if (!el) return;
    ev.preventDefault();
    const startX = ev.clientX - el.offsetLeft;
    const startY = ev.clientY - el.offsetTop;
    const shield = dragShield();
    const move = (e) => {
      const x = Math.max(-el.offsetWidth + 60, Math.min(e.clientX - startX, window.innerWidth - 40));
      const y = Math.max(0, Math.min(e.clientY - startY, window.innerHeight - 24));
      el.style.left = x + 'px';
      el.style.top = y + 'px';
      this._pop.x = x; this._pop.y = y;
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      shield();
      this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight;
      this._saveTerm();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  _popDown() {
    this._front();
    const el = this._popEl; // capture size after native resizes too
    if (el && this._pop) { this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight; this._saveTerm(); }
  }

  _newTerm() {
    this._sessions = [...this._sessions, { id: null, net: 'internet', gpu: 'none' }];
    this._active = this._sessions.length - 1;
  }

  _gotSession(i, ev) {
    const s = [...this._sessions];
    s[i] = { id: ev.detail.id, net: ev.detail.net || s[i]?.net || 'internet', gpu: s[i]?.gpu || 'none' };
    this._sessions = s;
  }

  // Switch the active terminal's GPU (device binds are fixed at spawn, so this
  // restarts the session), mirroring _setNet.
  _setGpu(i, gpu) {
    const cur = this._sessions[i];
    if (!cur || (cur.gpu || 'none') === gpu) return;
    if (cur.id) {
      fetch(`/ws/term?session=${encodeURIComponent(cur.id)}`, { method: 'DELETE' }).catch(() => { });
    }
    const s = [...this._sessions];
    s[i] = { id: null, net: cur.net || 'internet', gpu };
    this._sessions = s;
  }

  // Reset the component's persistent terminal sandbox layer (installed packages,
  // system configs). Wipes it server-side, then restarts the active terminal on
  // the now-clean layer.
  _resetEnv() {
    if (!confirm(`Reset the sandbox for ${this.src}? Installed packages and system changes in this component's terminal will be wiped (your workspace files and $HOME are untouched).`)) return;
    fetch(`/ws/term/env?cwd=${encodeURIComponent(this.src)}`, { method: 'DELETE' })
      .catch(() => { })
      .finally(() => {
        const s = [...this._sessions];
        if (s[this._active]) s[this._active] = { id: null, net: s[this._active].net || 'internet', gpu: s[this._active].gpu || 'none' };
        this._sessions = s;
        this.renderRoot?.querySelectorAll('bx-terminal')[this._active]?.restartFresh?.();
      });
  }

  // Switch the active terminal's network scope. The netns/relay is fixed at
  // spawn, so this restarts the session: end the old one and drop to a fresh
  // session in the new scope (bx-terminal reconnects on the net change).
  _setNet(i, net) {
    const cur = this._sessions[i];
    if (!cur || cur.net === net) return;
    if (cur.id) {
      fetch(`/ws/term?session=${encodeURIComponent(cur.id)}`, { method: 'DELETE' })
        .catch(() => { });
    }
    const s = [...this._sessions];
    s[i] = { id: null, net, gpu: cur.gpu || 'none' };
    this._sessions = s;
  }

  render() {
    const style = this._autoHeight ? nothing
      : `--bx-frame-height: ${this.height || this.style.height}`;
    return html`
      <div class="frame-wrap" style=${style ?? nothing}>
        <iframe src=${this._url()} title=${this.src}></iframe>
        ${this._buildError !== null ? html`
          <pre class="overlay"><b>build failed — ${this.src}</b>\n\n${this._buildError}</pre>` : nothing}
        ${this.hasAttribute('no-edit') ? nothing : html`
          <button class="edit" title="edit ${this.src}" @click=${this._toggleTerm}></button>`}
      </div>
      ${this._termOpen ? html`
        <div class="pop"
             style="left:${this._pop.x}px; top:${this._pop.y}px; width:${this._pop.w}px; height:${this._pop.h}px"
             @pointerdown=${this._popDown}>
          <div class="titlebar" @pointerdown=${this._dragStart}>
            <span class="path">${this.src}</span>
            ${this._sessions.map((s, i) => html`
              <button class=${i === this._active ? 'on' : ''}
                      @click=${() => { this._active = i; }}>${i + 1}</button>`)}
            <button title="new terminal" @click=${this._newTerm}>+</button>
            <span class="spacer"></span>
            <select class="scope" title="network scope (switching restarts the terminal)"
                    .value=${this._sessions[this._active]?.net || 'internet'}
                    @change=${(e) => this._setNet(this._active, e.target.value)}>
              <option value="internet">🌐 internet</option>
              <option value="host">🖧 host net</option>
              <option value="none">⛔ offline</option>
            </select>
            ${this._gpus.length ? html`
              <select class="scope" title="GPU (switching restarts the terminal)"
                      .value=${this._sessions[this._active]?.gpu || 'none'}
                      @change=${(e) => this._setGpu(this._active, e.target.value)}>
                <option value="none">no GPU</option>
                ${this._gpus.map((g) => html`<option value=${g.index}>🎮 GPU ${g.index}</option>`)}
                ${this._gpus.length > 1 ? html`<option value="all">🎮 all</option>` : nothing}
              </select>` : nothing}
            <button title="reset this component's sandbox (wipe installed packages)"
                    @click=${this._resetEnv}>⟲</button>
            <button title="close (session keeps running)"
                    @click=${() => { this._termOpen = false; }}>✕</button>
          </div>
          <div class="term-host">
            ${this._sessions.map((s, i) => html`
              <bx-terminal style="height:100%; display:${i === this._active ? 'block' : 'none'}"
                cwd=${this.src} session=${s.id ?? nothing} net=${s.net || 'internet'} gpu=${s.gpu || 'none'}
                @bx-session=${(ev) => this._gotSession(i, ev)}></bx-terminal>`)}
          </div>
        </div>` : nothing}
    `;
  }
}

customElements.define('bx-frame', BxFrame);
