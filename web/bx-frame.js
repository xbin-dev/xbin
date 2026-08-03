/**
 * <bx-frame src="apps/calendar"> — the core xbin element: renders a
 * component's index.html in an iframe, carries the always-visible
 * 7×7 edit button, live-reloads on source changes, shows build errors as an
 * overlay, and hosts the terminal pop-up (persistent PTY sessions cwd'd to
 * the component's source directory) plus a code browser / git-review panel
 * (bx-code) that can share the window with the terminal (layout: terminal /
 * code / split).
 *
 * Attributes:
 *   src     — component path (workspace-relative)
 *   height  — fixed CSS height; omit for auto-height (the framed document
 *             reports its size via xbin-client.js)
 *   no-edit — hide the edit button
 *
 * Browser-plane isolation (plans/auth.md §6): non-chrome components load in a
 * SANDBOXED iframe (opaque origin: no DOM access either way, no storage, no
 * ambient cookie — the tile's only credential is its injected frame token).
 * Chrome components (root, shell, manifest chrome:true) run unsandboxed and
 * act as the signed-in human. Where the browser supports it, sandboxed frames
 * are also credentialless (no cookies even on the navigation, so the document
 * load authenticates with a bootstrap frame token in the URL).
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
import { repeat } from 'lit';
import { onEvent, mountedFrames, isReloadTarget } from '/vendor/events-socket.js';
import '/vendor/bx-terminal.js';
import '/vendor/bx-code.js';
import '/vendor/bx-logs.js';

// Shared z-order for all terminal windows on the page.
let zTop = 2000;

const uid = () => Math.random().toString(36).slice(2, 9);

// Sandbox tokens for tile frames: scripts + forms + modals, never
// allow-same-origin (that plus allow-scripts would void the sandbox).
const SANDBOX = 'allow-scripts allow-forms allow-modals';

// <iframe credentialless> (Chromium 110+): loads the frame in an ephemeral
// credential context — no ambient cookie even on the document navigation.
const CREDENTIALLESS = 'credentialless' in HTMLIFrameElement.prototype;

// Chrome components run UNSandboxed — they act as the signed-in human
// (the shell itself, and manifest-flagged trusted chrome like
// tiles/organisations). Fetched once; frames await it before creating their
// iframe so the sandbox attribute applies to the FIRST load (changing it
// later would not re-sandbox a loaded document).
let _chromeSet;
function chromeSet() {
  _chromeSet ??= fetch('/api/xbin/components')
    .then((r) => (r.ok ? r.json() : []))
    .then((list) => new Set(list.filter((c) => c.chrome).map((c) => c.path)))
    .catch(() => new Set());
  return _chromeSet;
}

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
    _layout: { state: true },  // 'term' | 'code' | 'split'
    _codeW: { state: true },   // code panel width % in split
    _frame: { state: true },   // {url, sandboxed, credentialless} | null
  };

  static styles = css`
    :host { display: block; position: relative; }
    /* height:100% is what lets a fixed-height embedder (the shell grid tiles /
       floating windows pin the host with position:absolute; inset:0) flow a
       definite height down to the iframe: iframe height:100% resolves against
       .frame-wrap, so .frame-wrap must itself fill the host — otherwise it
       stays content-height (auto) and the iframe collapses. In auto-height mode
       the host is content-sized, so 100%-of-auto is just auto — no change. */
    .frame-wrap { position: relative; height: 100%; }
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
    /* On phones the draggable pop-up becomes a full-screen sheet. */
    @media (max-width: 820px) {
      .pop { inset: 0 !important; width: auto !important; height: auto !important;
        resize: none !important; border-radius: 0; min-width: 0; min-height: 0; }
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
    .titlebar button.upgrade {
      color: #23272e; background: var(--bx-amber, #f2a71b); font-weight: 600;
      border-radius: 5px; padding: 1px 8px; white-space: nowrap;
    }
    .titlebar button.upgrade:hover { color: #23272e; filter: brightness(1.06); }
    /* Tabs are spans (not buttons) so each can hold a close button — nested
       buttons are invalid HTML. Styled like the titlebar buttons. */
    .titlebar .tab {
      display: inline-flex; align-items: center; gap: 2px; max-width: 150px;
      border: 1px solid transparent; border-radius: 4px; padding: 1px 3px 1px 7px;
      color: var(--bx-muted, #8794a1);
      font: 11px var(--bx-mono, ui-monospace, monospace); cursor: pointer;
    }
    .titlebar .tab.on {
      background: var(--bx-panel, #fff);
      border-color: var(--bx-border, #e4e8ed);
      color: var(--bx-text, #33414e);
    }
    .titlebar .tab:hover { color: var(--bx-text, #33414e); }
    .titlebar .tab .lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .titlebar .tab .tabx {
      flex: none; padding: 0 3px; border: 0; border-radius: 3px;
      background: transparent; color: inherit; opacity: .45;
      font-size: 12px; line-height: 1; cursor: pointer;
    }
    .titlebar .tab .tabx:hover { opacity: 1; background: var(--bx-border, #e4e8ed); }
    .titlebar select.scope {
      margin-left: 2px; border: 1px solid var(--bx-border, #e4e8ed);
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      font: 11px var(--bx-mono, ui-monospace, monospace);
      padding: 1px 4px; border-radius: 4px; cursor: pointer;
    }
    .panels { display: flex; flex: 1; min-height: 0; }
    bx-code { min-width: 0; overflow: hidden; border-right: 1px solid var(--bx-border, #e4e8ed); }
    .vsplit { flex: none; width: 5px; cursor: col-resize; background: var(--bx-border, #e4e8ed); }
    .vsplit:hover { background: var(--bx-accent, #f2a71b); }
    .term-host { flex: 1; min-height: 0; min-width: 0; background: var(--bx-term-bg, #262c36); }
    .lyt { display: inline-flex; margin-left: 2px; }
    .lyt button { padding: 1px 6px; }
    .lyt button.on { background: var(--bx-panel, #fff); border-color: var(--bx-border, #e4e8ed); color: var(--bx-text, #33414e); }
  `;

  constructor() {
    super();
    this._termOpen = false;
    this._layout = 'term';
    this._codeW = 55;
    this._sessions = []; // [{id: string|null, net, gpu}] — id null until server assigns
    this._active = 0;
    this._gpus = []; // host GPU inventory (empty unless a GPU host)
    this._buildError = null;
    this._autoHeight = false;
    this._pop = null; // {x, y, w, h} — owned imperatively after open
    this._offEvents = null;
    this._frame = null; // {url, sandboxed, credentialless} — null until resolved
    this._onMsg = (e) => this._message(e);
  }

  connectedCallback() {
    super.connectedCallback();
    mountedFrames.add(this);
    this._autoHeight = !this.height && !this.style.height;
    this._offEvents = onEvent((e) => this._event(e));
    window.addEventListener('message', this._onMsg);
    this._restoreTerm();
    this._prepareFrame();
  }

  // Resolve how this frame must load (sandboxed? credentialless?) before the
  // iframe exists. Credentialless navigations carry no cookie, so they
  // authenticate with a bootstrap frame token in the URL (?frame= is consumed
  // by xbind, never forwarded) — minted here, in chrome context, where the
  // cookie principal may mint for any tile the human can read.
  async _prepareFrame() {
    const sandboxed = !(await chromeSet()).has(this.src);
    let url = this._url(), credentialless = false;
    if (sandboxed && CREDENTIALLESS) {
      const tok = await fetch(`/api/xbin/frame-token?component=${encodeURIComponent(this.src)}`)
        .then((r) => (r.ok ? r.json() : null)).then((d) => d?.token || '').catch(() => '');
      if (tok) {
        url += `?frame=${encodeURIComponent(tok)}`;
        credentialless = true;
      }
      // No token (e.g. nested inside another tile): load WITHOUT
      // credentialless so the navigation can still authenticate by cookie.
    }
    this._frame = { url, sandboxed, credentialless };
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
        .map((s) => ({ id: s.id, net: s.net, gpu: s.gpu, api: s.api, name: s.name || '', key: s.key }));
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
      key: s.key || uid(), id: s.id ?? null, net: s.net || 'internet', gpu: s.gpu || 'none',
      api: s.api !== false, name: s.name || '',
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
    // Sandboxed frames are opaque origins — we can't reach contentWindow —
    // so reload by re-navigation (re-minting the bootstrap token when
    // credentialless, since the old one may have expired).
    if (this._frame?.credentialless) { this._prepareFrame(); return; }
    if (this._frame?.sandboxed) { const f = this._iframe; if (f) f.src = this._url(); return; }
    try { this._iframe?.contentWindow?.location.reload(); }
    catch { if (this._iframe) this._iframe.src = this._url(); }
  }

  _message(e) {
    // Only trust messages from OUR iframe — the sender window IS the identity,
    // so a tile can't spoof another component's requests.
    if (e.source !== this._iframe?.contentWindow) return;
    const d = e.data;
    if (typeof d?.type !== 'string' || !d.type.startsWith('xbin:')) return;

    if (d.type === 'xbin:resize') {
      if (!this._autoHeight) return;
      this.style.setProperty('--bx-frame-height', Math.max(24, Math.min(d.height, 20000)) + 'px');
      return;
    }

    // Dialog / pop-out window requests — relayed to <bx-shell> with the VERIFIED
    // component id (this.src, not anything the tile claimed). `reply` posts the
    // result back to this exact iframe, keyed by the request id.
    if (d.type === 'xbin:dialog' || d.type === 'xbin:window') {
      this.dispatchEvent(new CustomEvent('bx-spawn', {
        bubbles: true, composed: true,
        detail: {
          kind: d.type.slice('xbin:'.length), // 'dialog' | 'window'
          id: d.id, from: this.src, spec: d.spec || {},
          reply: (result) => this._iframe?.contentWindow?.postMessage(
            // targetOrigin '*' : a sandboxed tile is an opaque origin, so no
            // origin string ever matches it. Delivery is confined to THIS
            // iframe's window regardless; xbin-client verifies e.source.
            { type: 'xbin:reply', id: d.id, result }, '*'),
        },
      }));
    } else if (d.type === 'xbin:window-close') {
      this.dispatchEvent(new CustomEvent('bx-spawn-close', {
        bubbles: true, composed: true, detail: { id: d.id },
      }));
    }
  }

  _url() { return `/c/${this.src}/`; }

  // ---- terminal window ----

  // toggleTerminal opens/closes this frame's terminal — the public entry the
  // shell's tile-header button uses (the 7x7 corner button stays for
  // standalone embeds; the shell hides it via no-edit).
  toggleTerminal() { this._toggleTerm(); }

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
    // Don't start a window drag from an interactive control in the titlebar —
    // buttons, selects, or a terminal tab (a <span>, so it needs naming).
    if (ev.button !== 0 || ev.target.closest('button, select, .tab')) return;
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
    this._sessions = [...this._sessions, { key: uid(), id: null, net: 'internet', gpu: 'none', name: '' }];
    this._active = this._sessions.length - 1;
  }

  // Rename the terminal on tab i (blank clears back to its number). Names are
  // per-component and persist like the session list.
  _renameTerm(i) {
    const cur = this._sessions[i];
    if (!cur) return;
    const n = prompt('Terminal name (blank to number it):', cur.name || '');
    if (n === null) return;
    const s = [...this._sessions];
    s[i] = { ...cur, name: n.trim() };
    this._sessions = s;
  }

  // Close terminal i. ended=true means the shell already exited (no DELETE
  // needed); otherwise this is a user close and we end the server session.
  // Closing the last one closes the window.
  _closeTerm(i, ended = false) {
    const s = this._sessions[i];
    if (!s) return;
    if (!ended && s.id) fetch(`/ws/term?session=${encodeURIComponent(s.id)}`, { method: 'DELETE' }).catch(() => { });
    const rest = this._sessions.filter((_, j) => j !== i);
    if (!rest.length) { this._sessions = []; this._termOpen = false; return; }
    this._sessions = rest;
    if (this._active >= rest.length) this._active = rest.length - 1;
    else if (this._active > i) this._active -= 1;
  }

  _gotSession(i, ev) {
    const s = [...this._sessions];
    const cur = s[i] || {};
    s[i] = { ...cur, key: cur.key ?? uid(), id: ev.detail.id, net: ev.detail.net || cur.net || 'internet',
             baseOutdated: !!ev.detail.baseOutdated };
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
    s[i] = { ...cur, id: null, gpu };
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
        if (s[this._active]) s[this._active] = { ...s[this._active], id: null };
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
    s[i] = { ...cur, id: null, net };
    this._sessions = s;
  }

  // Switch the pop-up layout: terminal only, code browser/review only, or a
  // resizable split of the two. The terminal stays mounted (hidden in 'code')
  // so its session survives; bx-code mounts lazily on first non-'term' view.
  _setLayout(l) {
    this._layout = l;
    if ((l === 'code' || l === 'split') && this._pop && this._pop.w < 760) {
      this._pop = { ...this._pop, w: 960 }; // widen for the code panel
      this._saveTerm?.();
    }
  }

  // Drag the split divider (a shield keeps the frame iframe from stealing the
  // pointer when the cursor races ahead).
  _splitStart(e) {
    e.preventDefault();
    const panels = e.currentTarget.parentElement;
    const rect = panels.getBoundingClientRect();
    const un = dragShield('col-resize');
    const move = (ev) => {
      this._codeW = Math.max(20, Math.min(80, ((ev.clientX - rect.left) / rect.width) * 100));
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      un();
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  // Toggle whether this terminal can call the live tile (and xbin) API. The
  // per-session token is minted at spawn, so like net/GPU this restarts the
  // session: api=false → no token → the shell can read/edit code but every API
  // call is unauthorized. Mirrors _setNet.
  _setApi(i, on) {
    const cur = this._sessions[i];
    if (!cur || (cur.api !== false) === on) return;
    if (cur.id) {
      fetch(`/ws/term?session=${encodeURIComponent(cur.id)}`, { method: 'DELETE' }).catch(() => { });
    }
    const s = [...this._sessions];
    s[i] = { ...cur, id: null, api: on };
    this._sessions = s;
  }

  render() {
    const style = this._autoHeight ? nothing
      : `--bx-frame-height: ${this.height || this.style.height}`;
    return html`
      <div class="frame-wrap" style=${style ?? nothing}>
        ${this._frame ? html`
          <iframe src=${this._frame.url} title=${this.src}
                  sandbox=${this._frame.sandboxed ? SANDBOX : nothing}
                  credentialless=${this._frame.credentialless ? '' : nothing}></iframe>` : nothing}
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
              <span class="tab ${i === this._active ? 'on' : ''}"
                    @click=${() => { this._active = i; }}
                    @dblclick=${() => this._renameTerm(i)}
                    title=${s.name ? `${s.name} — double-click to rename` : 'double-click to rename'}>
                <span class="lbl">${s.name || (i + 1)}</span>
                <button class="tabx" title="close this terminal"
                        @click=${(e) => { e.stopPropagation(); this._closeTerm(i); }}>✕</button>
              </span>`)}
            <button title="new terminal" @click=${this._newTerm}>+</button>
            <span class="lyt">
              <button class=${this._layout === 'term' ? 'on' : ''} title="terminal only"
                      @click=${() => this._setLayout('term')}>&gt;_</button>
              <button class=${this._layout === 'code' ? 'on' : ''} title="code browser + review"
                      @click=${() => this._setLayout('code')}>{ }</button>
              <button class=${this._layout === 'split' ? 'on' : ''} title="code + terminal side by side"
                      @click=${() => this._setLayout('split')}>⇋</button>
              <button class=${this._layout === 'logs' ? 'on' : ''} title="backend logs (read-only)"
                      @click=${() => this._setLayout('logs')}>▤</button>
            </span>
            <span class="spacer"></span>
            <select class="scope" title="network scope (switching restarts the terminal)"
                    .value=${this._sessions[this._active]?.net || 'internet'}
                    @change=${(e) => this._setNet(this._active, e.target.value)}>
              <option value="internet">🌐 internet</option>
              <option value="host">🖧 host net</option>
              <option value="none">⛔ offline</option>
            </select>
            <select class="scope" title="live tile API access — off = the shell can read/edit code but every API call is unauthorized (switching restarts the terminal)"
                    .value=${this._sessions[this._active]?.api === false ? 'off' : 'on'}
                    @change=${(e) => this._setApi(this._active, e.target.value === 'on')}>
              <option value="on">🔌 tile API</option>
              <option value="off">⛔ no API</option>
            </select>
            ${this._gpus.length ? html`
              <select class="scope" title="GPU (switching restarts the terminal)"
                      .value=${this._sessions[this._active]?.gpu || 'none'}
                      @change=${(e) => this._setGpu(this._active, e.target.value)}>
                <option value="none">no GPU</option>
                ${this._gpus.map((g) => html`<option value=${g.index}>🎮 GPU ${g.index}</option>`)}
                ${this._gpus.length > 1 ? html`<option value="all">🎮 all</option>` : nothing}
              </select>` : nothing}
            ${this._sessions[this._active]?.baseOutdated ? html`
              <button class="upgrade" title="a newer base image is installed — upgrade rebuilds this terminal on it (wipes installed packages; your files & $HOME are kept)"
                      @click=${this._resetEnv}>⬆ base update</button>` : nothing}
            <button title="reset this component's sandbox (wipe installed packages)"
                    @click=${this._resetEnv}>⟲</button>
            <button title="close (session keeps running)"
                    @click=${() => { this._termOpen = false; }}>✕</button>
          </div>
          <div class="panels">
            ${this._layout === 'code' || this._layout === 'split' ? html`<bx-code src=${this.src}
                style="flex-basis:${this._layout === 'split' ? this._codeW + '%' : '100%'}"></bx-code>` : nothing}
            ${this._layout === 'split' ? html`<div class="vsplit" @pointerdown=${this._splitStart}></div>` : nothing}
            ${this._layout === 'logs' ? html`<bx-logs component=${this.src} style="flex:1; min-width:0"></bx-logs>` : nothing}
            <div class="term-host" style="display:${this._layout === 'term' || this._layout === 'split' ? 'flex' : 'none'}; flex-direction:column">
            ${repeat(this._sessions, (s) => s.key, (s, i) => html`
              <bx-terminal style="height:100%; display:${i === this._active ? 'block' : 'none'}"
                cwd=${this.src} session=${s.id ?? nothing} net=${s.net || 'internet'} gpu=${s.gpu || 'none'} api=${s.api === false ? '0' : '1'}
                @bx-session=${(ev) => this._gotSession(i, ev)}
                @bx-exit=${() => this._closeTerm(i, true)}></bx-terminal>`)}
            </div>
          </div>
        </div>` : nothing}
    `;
  }
}

customElements.define('bx-frame', BxFrame);
