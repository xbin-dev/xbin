/**
 * <bx-frame src="apps/calendar"> — the core xbin element: renders a
 * component's index.html in an iframe, carries the always-visible
 * 7×7 edit button, live-reloads on source changes, shows build errors as an
 * overlay, and hosts the terminal pop-up (persistent PTY sessions cwd'd to
 * the component's source directory) plus a code browser / git-review panel
 * (bx-code) that can share the window with the terminal (layout: terminal /
 * code / split), a read-only backend log view (bx-logs), and the
 * change-proposal panel (bx-prs — cross-tile "code PRs").
 *
 * Attributes:
 *   src     — component path (workspace-relative)
 *   height  — fixed CSS height; omit for auto-height (the framed document
 *             reports its size via xbin-client.js)
 *   no-edit — hide the edit button
 *
 * Browser-plane isolation (docs/auth.md §Who is calling): non-chrome components load in a
 * SANDBOXED iframe (opaque origin: no DOM access either way, no storage, no
 * ambient cookie — the tile's only credential is its injected frame token).
 * Chrome components (root, shell, manifest chrome:true) run unsandboxed and
 * act as the signed-in human. Where the browser supports it, sandboxed frames
 * are also credentialless (no cookies even on the navigation, so the document
 * load authenticates with a bootstrap frame token in the URL).
 *
 * The edit button opens a floating terminal window: anchored at the frame's
 * top-right corner when opened, draggable by its title bar, resizable by the
 * native bottom-right handle (ctrl+scroll inside adjusts the font). It is
 * viewport-fixed (container overflow can't clip it) but positioned RELATIVE
 * to this frame (D66): it follows the tile through scrolls and drags, and
 * stays inside the host's `popBounds` (the shell's canvas) so it is always
 * reachable by scrolling. Windows share a bring-to-front z-order.
 *
 * See /docs/elements.md.
 */
import { LitElement, html, css, nothing } from 'lit';
import { repeat, keyed } from 'lit';
import { onEvent, mountedFrames, isReloadTarget } from '/vendor/events-socket.js';
import { scopeIcon } from '/vendor/bx-netrules.js';
import '/vendor/bx-terminal.js';
import '/vendor/bx-code.js';
import '/vendor/bx-logs.js';
import '/vendor/bx-prs.js';
import { deepActive, clampBox, dragWindow, dragPointer, anchorBox, anchorOffsets, followBox } from '/vendor/bx-kit.js';
import { makeStore, tabsFrom, clampActive } from '/vendor/term-sessions.js';
import { titlebar } from '/vendor/frame-titlebar.js';
import '/vendor/bx-agent.js';

// Shared z-order for all terminal windows on the page.
let zTop = 2000;
// On phones the pop-up is a full-screen sheet (CSS) — no geometry to follow.
const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false };

const uid = () => Math.random().toString(36).slice(2, 9);
// The terminal session directory (D73): which live sessions are the user's
// on a tile is asked of the server, never remembered in this browser.
const sessions = makeStore();

// clampBox (bx-kit) keeps a viewport-fixed window reachable: never wider/
// taller than the viewport (minus an 8px margin), never positioned outside
// it. Geometry is persisted per tile and restored on another monitor, a
// smaller browser window or a different zoom — a saved {x:2270, y:1217} once
// rendered a perfectly working terminal nobody could see. Re-exported here
// because importers of this module relied on it before the kit existed.
export { clampBox };

// A browser window that shrinks pulls every open pop-up back inside it.
window.addEventListener('resize', () => {
  for (const f of mountedFrames) f.fitToViewport?.();
});

// Base sandbox tokens for tile frames: scripts + forms + modals + downloads,
// never allow-same-origin (that plus allow-scripts would void the sandbox).
// Downloads are safe to allow (ND10): they cross no workspace/session/tile
// boundary and the browser's own download UI mediates. Popups need a grant:
// cap:open-links (ND11) adds allow-popups + allow-popups-to-escape-sandbox
// for THAT tile — the server decides and reports the extra tokens on
// /components, and this file appends exactly what it was sent, so the
// attribute and the CSP sandbox header (internal/server/static.go) never
// drift (browsers intersect the two). Top navigation stays blocked always.
const SANDBOX = 'allow-scripts allow-forms allow-modals allow-downloads';

// <iframe credentialless> (Chromium 110+): loads the frame in an ephemeral
// credential context — no ambient cookie even on the document navigation.
const CREDENTIALLESS = 'credentialless' in HTMLIFrameElement.prototype;

// Per-component frame facts from /api/xbin/components: chrome (runs
// UNsandboxed — the shell itself and manifest-flagged trusted chrome like
// tiles/organisations act as the signed-in human) and the extra sandbox
// tokens the tile's grants unlock. Fetched once; frames await it before
// creating their iframe so the sandbox attribute is right for the FIRST load
// — a changed attribute only applies to the NEXT navigation.
let _info;
function frameInfo() {
  _info ??= fetch('/api/xbin/components')
    .then((r) => (r.ok ? r.json() : []))
    .then((list) => new Map(list.map((c) => [c.path, { chrome: !!c.chrome, sandbox: c.sandbox || [] }])))
    .catch(() => new Map());
  return _info;
}
// Longest-prefix lookup so an xbin.window() sub-path frame (apps/x/compose)
// inherits its component's facts, as the server's CSP already does.
async function infoFor(src) {
  const m = await frameInfo();
  let best = null;
  for (const [p, i] of m) {
    if ((src === p || src.startsWith(p + '/')) && (!best || p.length > best.p.length)) best = { p, i };
  }
  return best?.i ?? null;
}
// A grant changed for one component: refresh ITS entry in the shared map
// (write-through, so a frame mounted later sees the new tokens too).
async function refreshFrameInfo(path) {
  const c = await fetch(`/api/xbin/components/${path}`)
    .then((r) => (r.ok ? r.json() : null)).then((d) => d?.component ?? null).catch(() => null);
  if (c) (await frameInfo()).set(path, { chrome: !!c.chrome, sandbox: c.sandbox || [] });
}
const sandboxAttr = (info) => (info?.chrome ? '' : [SANDBOX, ...(info?.sandbox ?? [])].join(' '));

// Host GPU inventory (shared, fetched once) — populates the terminal GPU picker.
let _gpuInv;
function gpuInventory() {
  _gpuInv ??= fetch('/api/xbin/gpus')
    .then((r) => (r.ok ? r.json() : { gpus: [] }))
    .then((d) => d.gpus || [])
    .catch(() => []);
  return _gpuInv;
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
    _layout: { state: true },  // 'term' | 'code' | 'split' | 'logs' | 'prs'
    _codeW: { state: true },   // code panel width % in split
    _frame: { state: true },   // {url, sandboxed, credentialless} | null
    _prCount: { state: true }, // open change proposals targeting this tile
    // popBounds: () → a viewport rect the pop-up's top-left stays inside, or
    // null (the viewport clamps instead). The shell's canvas sets it (D66).
    popBounds: { attribute: false },
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
      background: color-mix(in srgb, var(--bx-panel, #23272e) 96%, var(--bx-red, #ef5350));
      color: var(--bx-red, #ef5350);
      font: 11.5px/1.55 var(--bx-mono, ui-monospace, monospace);
      padding: 10px 12px; margin: 0; white-space: pre-wrap;
      border-top: 2px solid var(--bx-red, #ef5350);
    }
    .overlay b { color: var(--bx-red, #ef5350); }

    /* ---- floating terminal window ---- */
    .pop {
      position: fixed;
      display: flex; flex-direction: column;
      background: var(--bx-panel, #23272e);
      border: 1px solid var(--bx-border, #363c45);
      border-radius: 8px;
      box-shadow: 0 8px 28px rgba(16, 24, 40, 0.20), var(--bx-shadow, 0 1px 2px rgba(0, 0, 0, 0.35));
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
      background: var(--bx-panel-2, #2b3038);
      border-bottom: 1px solid var(--bx-border, #363c45);
      padding: 3px 6px; user-select: none; cursor: grab;
      touch-action: none; flex: none;
    }
    .titlebar:active { cursor: grabbing; }
    .titlebar .path {
      color: var(--bx-text, #d4d9e0); font-weight: 600;
      font: 11px var(--bx-mono, ui-monospace, monospace);
      padding: 0 8px 0 4px; white-space: nowrap;
      overflow: hidden; text-overflow: ellipsis;
    }
    .titlebar .spacer { flex: 1; }
    .titlebar button {
      border: 1px solid transparent; background: transparent;
      color: var(--bx-muted, #868f9a);
      font: 11px var(--bx-mono, ui-monospace, monospace); padding: 1px 7px;
      border-radius: 4px; cursor: pointer;
    }
    .titlebar button.on {
      background: var(--bx-panel, #23272e);
      border-color: var(--bx-border, #363c45);
      color: var(--bx-text, #d4d9e0);
    }
    .titlebar button:hover { color: var(--bx-text, #d4d9e0); }
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
      color: var(--bx-muted, #868f9a);
      font: 11px var(--bx-mono, ui-monospace, monospace); cursor: pointer;
    }
    .titlebar .tab.on {
      background: var(--bx-panel, #23272e);
      border-color: var(--bx-border, #363c45);
      color: var(--bx-text, #d4d9e0);
    }
    .titlebar .tab:hover { color: var(--bx-text, #d4d9e0); }
    .titlebar .tab .lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .titlebar .tab .tabx {
      flex: none; padding: 0 3px; border: 0; border-radius: 3px;
      background: transparent; color: inherit; opacity: .45;
      font-size: 12px; line-height: 1; cursor: pointer;
    }
    .titlebar .tab .tabx:hover { opacity: 1; background: var(--bx-border, #363c45); }
    .titlebar select.scope {
      margin-left: 2px; border: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      font: 11px var(--bx-mono, ui-monospace, monospace);
      padding: 1px 4px; border-radius: 4px; cursor: pointer;
      /* the org scope's label names the sets ("org network (devs-net + …)")
         — cap it so the API/GPU pickers stay on the bar; the tooltip has it all */
      max-width: 24ch; text-overflow: ellipsis;
    }
    .panels { display: flex; flex: 1; min-height: 0; }
    bx-code { min-width: 0; overflow: hidden; border-right: 1px solid var(--bx-border, #363c45); }
    .vsplit { flex: none; width: 5px; cursor: col-resize; background: var(--bx-border, #363c45); }
    .vsplit:hover { background: var(--bx-accent, #f5a623); }
    .term-host { flex: 1; min-height: 0; min-width: 0; background: var(--bx-term-bg, #262c36); }
    .lyt { display: inline-flex; margin-left: 2px; }
    .lyt button { padding: 1px 6px; }
    .lyt button.on { background: var(--bx-panel, #23272e); border-color: var(--bx-border, #363c45); color: var(--bx-text, #d4d9e0); }
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
    this._pop = null; // {dx, dy, w, h} — offsets from this frame's box; owned imperatively after open
    this._stopFollow = null; // the follow loop's stop while the pop-up is open
    this._offEvents = null;
    this._frame = null; // {url, sandboxed, credentialless} — null until resolved
    this._onMsg = (e) => this._message(e);
    this._winTimer = null; // the debounced per-user window-state save
    this._onVisible = () => { if (document.visibilityState === 'visible') this._relist(); };
  }

  connectedCallback() {
    super.connectedCallback();
    mountedFrames.add(this);
    this._autoHeight = !this.height && !this.style.height;
    this._offEvents = onEvent((e) => this._event(e));
    window.addEventListener('message', this._onMsg);
    document.addEventListener('visibilitychange', this._onVisible);
    this._restoreTerm();
    this._prepareFrame();
  }

  // Resolve how this frame must load (sandboxed? credentialless?) before the
  // iframe exists. Credentialless navigations carry no cookie, so they
  // authenticate with a bootstrap frame token in the URL (?frame= is consumed
  // by xbind, never forwarded) — minted here, in chrome context, where the
  // cookie principal may mint for any tile the human can read.
  async _prepareFrame() {
    const info = await infoFor(this.src);
    const sandboxed = !info?.chrome;
    const sandbox = sandboxAttr(info);
    // A changed token set (a cap:open-links grant approved or revoked) must
    // reach a FRESH element: the attribute applies only to the next
    // navigation, so re-keying the iframe keeps the flags unambiguous.
    if (this._frame && this._frame.sandbox !== sandbox) this._frameKey = (this._frameKey ?? 0) + 1;
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
    this._frame = { url, sandboxed, sandbox, credentialless };
  }

  // A grant for this component changed. If it moved the sandbox token set
  // (cap:open-links), re-prepare — the re-keyed iframe loads the new document
  // with the new flags; otherwise the plain reload so a frontend that was
  // 403'ing retries against its new permissions.
  async _regrant() {
    const before = this._frame?.sandbox;
    await refreshFrameInfo(this.src);
    if (sandboxAttr(await infoFor(this.src)) !== before) {
      this._buildError = null;
      this._beginReload();
      await this._prepareFrame();
      return;
    }
    this._reload();
  }

  // The window's state (open, active tab, geometry) is a per-user pref, saved
  // whenever it changes (pop geometry is imperative → saved in the drag/resize
  // handlers); the sessions themselves are the server's to remember.
  updated(changed) {
    if (changed.has('_active') || changed.has('_termOpen')) this._saveTerm();
    if (changed.has('_termOpen')) { if (this._termOpen) this._follow(); this._popChanged(); }
  }

  // ---- pop-up geometry (D66): relative to this frame, inside the host's bounds ----
  _bounds() { return typeof this.popBounds === 'function' ? this.popBounds() : null; }
  _popBox(p = this._pop) { return anchorBox(this.getBoundingClientRect(), p, this._bounds()); }
  _setPopBox(box) { this._pop = anchorOffsets(this.getBoundingClientRect(), box); }
  // popBox(): the open pop-up's viewport box (null while closed); bx-pop announces a change.
  popBox() { return this._termOpen && this._pop ? this._popBox() : null; }
  _popChanged() { this.dispatchEvent(new CustomEvent('bx-pop', { bubbles: true, composed: true })); }
  // While open, the pop-up follows the frame's box (scroll, drag) every frame.
  _follow() {
    this._stopFollow?.();
    this._stopFollow = followBox(() => this._popEl, () => (this._pop && !SHEET.matches ? this._popBox() : null),
      () => this._termOpen && this.isConnected);
  }

  _saveTerm() {
    if (!this._restored) return; // lit's first update reports every field as changed; a frame that has not restored yet has nothing of the user's to say
    clearTimeout(this._winTimer);
    this._winTimer = setTimeout(() => this._flushWindow(), 400);
  }
  _flushWindow() {
    clearTimeout(this._winTimer); this._winTimer = null;
    const w = this._termOpen || this._sessions.length ? { open: !!this._termOpen, active: this._active, pop: this._pop } : null;
    if (w || this._hadWindow) sessions.saveWindow(this.src, w);
    this._hadWindow = !!w;
  }

  // On mount: the user's live sessions on this tile from the server (they are
  // the same in every browser the user signs into), the window as they left
  // it, and — once — whatever the browser's legacy record held (D73).
  async _restoreTerm() {
    const legacy = sessions.migrateLegacy(this.src);
    const [rows, win] = await Promise.all([sessions.list(this.src), sessions.loadWindow(this.src)]);
    if (!this.isConnected) return;
    for (const r of rows) if (legacy?.names?.[r.id] && !r.name) { r.name = legacy.names[r.id]; sessions.rename(r.id, r.name); }
    this._sessions = tabsFrom(rows, this._sessions);
    const w = win ?? legacy?.window;
    if (w) {
      this._hadWindow = !!win;
      this._active = clampActive(w.active, this._sessions.length);
      if (w.pop && 'dx' in w.pop) this._pop = w.pop;
      if (w.open && this._sessions.length) {
        await this.updateComplete;
        this._termOpen = true;
        this.updateComplete.then(() => this._front());
      }
    }
    this._restored = true;
    if (w && !win) this._saveTerm(); // adopted: now the server's
  }

  // A session of the user's opened, ended or was renamed — here or in another
  // browser: the directory says what the tabs are now.
  async _relist() {
    const rows = await sessions.list(this.src);
    if (!this.isConnected) return;
    this._sessions = tabsFrom(rows, this._sessions);
    this._active = clampActive(this._active, this._sessions.length);
    if (!this._sessions.length && this._termOpen) this._termOpen = false;
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    mountedFrames.delete(this);
    this._stopFollow?.();
    this._offEvents?.();
    window.removeEventListener('message', this._onMsg);
    document.removeEventListener('visibilitychange', this._onVisible);
    if (this._winTimer) this._flushWindow();
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
        // that was 403'ing retries against the new permissions (and, for
        // cap:open-links, re-create the iframe with the new sandbox tokens).
        if (e.component === this.src) this._regrant();
        break;
      case 'pr':
        // Change-proposal activity on this tile — keep the PR tab badge live
        // while the terminal window is open (the shell sidebar carries the
        // ambient badge when it isn't).
        if (e.component === this.src && this._termOpen) this._loadPRCount();
        break;
      case 'term': // the session directory changed for this tile (D73)
        if (e.component === this.src) this._relist();
        break;
    }
  }

  async _loadPRCount() {
    try {
      const r = await fetch(`/api/xbin/code/prs?target=${encodeURIComponent(this.src)}&state=open`);
      if (r.ok) this._prCount = ((await r.json()).prs || []).length;
    } catch { /* badge is best-effort */ }
  }

  _reload() {
    this._buildError = null;
    this._beginReload();
    // Sandboxed frames are opaque origins — we can't reach contentWindow —
    // so reload by re-navigation (re-minting the bootstrap token when
    // credentialless, since the old one may have expired).
    if (this._frame?.credentialless) { this._prepareFrame(); return; }
    if (this._frame?.sandboxed) { const f = this._iframe; if (f) f.src = this._url(); return; }
    try { this._iframe?.contentWindow?.location.reload(); }
    catch { if (this._iframe) this._iframe.src = this._url(); }
  }

  // A reload must not mess with focus or z-order. A reloaded document that
  // focuses an input (autofocus, a script) makes the window blur, which the
  // shell reads as "the user clicked into that tile" and fronts its float —
  // over the terminal the person was typing in. So: while a reload is in
  // flight (until the new document's load event, plus a beat for its own
  // scripts) `reloading` is true and the shell leaves the z-order alone; and
  // if the reload pulled focus into this iframe, it goes back to where it
  // was. A genuine click during that window is not this frame's — the
  // pointer isn't over it — so hover is the tie-breaker.
  get reloading() { return this._reloadingUntil > Date.now(); }
  // hovered: the pointer is over the tile's own area (not its pop-up) — a
  // focus change then is the person's click, reload or not.
  get hovered() { return !!this._hover; }

  _beginReload() {
    // A burst of reloads (an editor saving several files) keeps the FIRST
    // snapshot: by the second one the previous document may already hold
    // the focus we want to give back.
    if (!this.reloading) {
      const f = deepActive();
      this._focusBefore = f === this._iframe ? null : f; // null: it was ours already
    }
    this._inFlight = true;
    this._reloadingUntil = Date.now() + 15000; // until load; a cap if it never fires
  }

  _onFrameLoad() {
    if (!this._inFlight) return; // the initial load, not a reload
    this._inFlight = false;
    const gen = (this._loadGen = (this._loadGen || 0) + 1);
    // The document's own scripts run after load; give them a beat, then hand
    // focus back if they took it. A newer reload in the meantime supersedes
    // this timer (its own load will run the restore).
    this._reloadingUntil = Date.now() + 400;
    setTimeout(() => {
      if (gen !== this._loadGen || this._inFlight) return;
      this._reloadingUntil = 0;
      const prev = this._focusBefore;
      this._focusBefore = null;
      if (!prev || !prev.isConnected || this._hover) return;
      if (deepActive() === this._iframe) { try { prev.focus({ preventScroll: true }); } catch { /* not focusable any more */ } }
    }, 350);
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

    // A right-click / long-press inside the tile: relay it upward in viewport
    // coordinates so the shell can open the tile menu (the iframe swallows
    // the native event) — and, for a mouse right-click, the text selected in
    // the tile, so the menu can lead with Copy (the frame has no clipboard).
    if (d.type === 'xbin:contextmenu') {
      const r = this._iframe?.getBoundingClientRect();
      if (!r) return;
      const clampN = (v, hi) => Math.max(0, Math.min(Number(v) || 0, hi));
      this.dispatchEvent(new CustomEvent('bx-contextmenu', {
        bubbles: true, composed: true,
        detail: {
          x: r.left + clampN(d.x, r.width), y: r.top + clampN(d.y, r.height),
          // Clamped again here: the sender is a tile.
          selection: typeof d.selection === 'string' ? d.selection.slice(0, 65536) : '',
        },
      }));
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

  // open(layout) makes sure the pop-up is open and shows the given panel:
  // 'term' | 'code' | 'split' | 'logs' | 'prs' (omit to keep the current one).
  // The shell's tile menu uses it for "terminal / logs / source / proposals".
  open(layout) {
    if (!this._termOpen) this._toggleTerm();
    else this.fitToViewport(); // already open: make sure it can be seen
    if (layout) this._setLayout(layout);
    this.updateComplete.then(() => this._front());
  }

  // fitToViewport pulls an open pop-up back to a reachable spot (a resize, the
  // shell's "bring windows on-screen"): inside the host's bounds when it has
  // them (the canvas — reachable by scrolling), else inside the browser window.
  fitToViewport() {
    if (!this._termOpen || !this._pop) return;
    const el = this._popEl; // the native resize handle may have changed the size
    const cur = this._popBox(el ? { ...this._pop, w: el.offsetWidth, h: el.offsetHeight } : this._pop);
    const next = anchorOffsets(this.getBoundingClientRect(), this._bounds() ? cur : clampBox(cur));
    const p = this._pop;
    if (next.dx !== p.dx || next.dy !== p.dy || next.w !== p.w || next.h !== p.h) {
      this._pop = next;
      this.requestUpdate(); // _pop is a plain field — the inline style needs a render
      this._saveTerm();
      this._popChanged();
    }
  }

  // ---- test surface (hack/ui-harness) ----
  // Stable names over the frame's private state (see bx-shell's testApi).
  // Reads and writes existing state only; nothing in the frame calls it.
  testApi() {
    const f = this;
    return {
      get iframe() { return f._iframe; },
      get hovered() { return f.hovered; },
      setHover(v) { f._hover = !!v; },
      get reloading() { return f.reloading; },
      beginReload: () => f._beginReload(),
      notifyLoad: () => f._onFrameLoad(),
      get terminalOpen() { return f._termOpen; },
      closeTerminal() { f._termOpen = false; },
      open: (layout) => f.open(layout),
      get pop() { return f._pop ? f._popBox() : null; }, // the viewport box
      setPop(box) { f._setPopBox(box); f.requestUpdate(); f._popChanged(); },
      popElement: () => f.renderRoot.querySelector('.pop'),
      focusTerminal() { f.renderRoot.querySelector('bx-terminal')?.shadowRoot?.querySelector('textarea')?.focus(); },
      get tabs() { return f._sessions.map((s) => ({ kind: s.kind || 'shell', id: s.id, name: s.name })); },
      get activeTab() { return f._active; },
      setActiveTab(i) { f._active = i | 0; },
      newAgent() { f._newAgent(); },
      // the <bx-agent> testApi for tab i (default: the active one) — the agent
      // elements are in session order among agent tabs
      agent(i = f._active) {
        const idx = f._sessions.filter((s) => s.kind === 'agent').indexOf(f._sessions[i]);
        return f.renderRoot.querySelectorAll('bx-agent')[idx >= 0 ? idx : 0]?.testApi?.();
      },
    };
  }

  _toggleTerm() {
    if (this._termOpen) { this._termOpen = false; return; }
    if (!this._pop) {
      // Anchor at the frame's top-right; when the tile is on screen, also inside the window.
      const r = this.getBoundingClientRect(), w = 560, h = 320;
      const box = anchorBox(r, { dx: r.width - w, dy: 8, w, h }, this._bounds());
      const visible = r.right > 0 && r.bottom > 0 && r.left < window.innerWidth && r.top < window.innerHeight;
      this._setPopBox(visible ? clampBox(box) : box);
    }
    this._termOpen = true;
    if (this._gpus.length === 0) gpuInventory().then((g) => { this._gpus = g; });
    if (this._sessions.length === 0) this._newTerm();
    this._loadPRCount();
    this.updateComplete.then(() => this._front());
  }

  _front() {
    const el = this._popEl;
    if (el) el.style.zIndex = String(++zTop);
  }

  // Title-bar drag (kit dragWindow, fenced inside the canvas — D66); the
  // native CSS resize handle owns width/height, read back into _pop on release.
  _dragStart(ev) {
    const el = this._popEl, r0 = this.getBoundingClientRect(); // fixed for the drag (the shield blocks scrolling)
    dragWindow(ev, el, {
      bounds: this._bounds(),
      onMove: (x, y) => { this._pop.dx = x - r0.left; this._pop.dy = y - r0.top; },
      onUp: () => { this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight; this._saveTerm(); this._popChanged(); },
    });
  }

  _popDown() {
    this._front();
    const el = this._popEl; // capture size after native resizes too
    if (!el || !this._pop || (this._pop.w === el.offsetWidth && this._pop.h === el.offsetHeight)) return;
    this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight; this._saveTerm(); this._popChanged();
  }

  _newTerm() {
    // net null = the server picks this tile's default scope (D54).
    this._sessions = [...this._sessions, { key: uid(), id: null, kind: 'shell', net: null, gpu: 'none', name: '' }];
    this._active = this._sessions.length - 1;
  }

  // Open a new AGENT tab (D74): a session whose sandbox runs a coding agent
  // instead of a shell. <bx-agent> creates the server session when the user
  // picks a provider and sends the first prompt, then fires bx-session with
  // the id — the same lazy pattern <bx-terminal> uses.
  _newAgent() {
    this._layout = 'term';
    this._sessions = [...this._sessions, { key: uid(), id: null, kind: 'agent', name: '' }];
    this._active = this._sessions.length - 1;
  }

  // The term-host holds both the shells and the agents; it shows whenever the
  // active tab is an agent (agents have no code/logs panels) or a shell tab
  // is in a terminal-bearing layout.
  _panelVisible() {
    const c = this._sessions[this._active];
    return c?.kind === 'agent' || this._layout === 'term' || this._layout === 'split';
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
    if (cur.id) sessions.rename(cur.id, n.trim()); // the name lives on the session (D73)
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
    // a listing may already have absorbed this id into another tab (tabsFrom): one tab per session
    const s = this._sessions.filter((t, j) => j === i || t.id !== ev.detail.id);
    i = Math.min(i, s.length - 1);
    const cur = s[i] || {};
    // The server reports the EFFECTIVE scope plus the scopes this user may
    // pick on this tile — the select renders exactly that list (D54).
    s[i] = { ...cur, key: cur.key ?? uid(), id: ev.detail.id, kind: ev.detail.kind || cur.kind || 'shell',
             net: ev.detail.net || cur.net || null,
             scopes: ev.detail.scopes || cur.scopes || null, label: ev.detail.label || '',
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

  // Switch the pop-up layout: terminal only, code browser/review only, a
  // resizable split of the two, backend logs, or change proposals (PRs). The
  // terminal stays mounted (hidden in non-term views) so its session
  // survives; the panels mount lazily on first view.
  _setLayout(l) {
    this._layout = l;
    if ((l === 'code' || l === 'split' || l === 'prs') && this._pop && this._pop.w < 760) {
      // widen for the code panel, keeping the window reachable
      const box = { ...this._popBox(), w: 960 };
      this._setPopBox(this._bounds() ? box : clampBox(box));
      this._saveTerm?.(); this._popChanged();
    }
  }

  // Drag the split divider (a shield keeps the frame iframe from stealing the
  // pointer when the cursor races ahead).
  _splitStart(e) {
    e.preventDefault();
    const panels = e.currentTarget.parentElement;
    const rect = panels.getBoundingClientRect();
    dragPointer({
      cursor: 'col-resize',
      onMove: (ev) => { this._codeW = Math.max(20, Math.min(80, ((ev.clientX - rect.left) / rect.width) * 100)); },
    });
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
      <div class="frame-wrap" style=${style ?? nothing}
           @pointerenter=${() => { this._hover = true; }} @pointerleave=${() => { this._hover = false; }}>
        ${this._frame ? keyed(this._frameKey ?? 0, html`
          <iframe src=${this._frame.url} title=${this.src}
                  sandbox=${this._frame.sandboxed ? this._frame.sandbox : nothing}
                  credentialless=${this._frame.credentialless ? '' : nothing}
                  @load=${() => this._onFrameLoad()}></iframe>`) : nothing}
        ${this._buildError !== null ? html`
          <pre class="overlay"><b>build failed — ${this.src}</b>\n\n${this._buildError}</pre>` : nothing}
        ${this.hasAttribute('no-edit') ? nothing : html`
          <button class="edit" title="edit ${this.src}" @click=${this._toggleTerm}></button>`}
      </div>
      ${this._termOpen ? (({ x, y, w, h }) => html`
        <div class="pop"
             style="left:${x}px; top:${y}px; width:${w}px; height:${h}px"
             @pointerdown=${this._popDown}>
          ${titlebar(this)}
          <div class="panels">
            ${(() => { const c = this._sessions[this._active]; return c?.kind === 'agent'; })() ? nothing : html`
            ${this._layout === 'code' || this._layout === 'split' ? html`<bx-code src=${this.src}
                style="flex-basis:${this._layout === 'split' ? this._codeW + '%' : '100%'}"></bx-code>` : nothing}
            ${this._layout === 'split' ? html`<div class="vsplit" @pointerdown=${this._splitStart}></div>` : nothing}
            ${this._layout === 'logs' ? html`<bx-logs component=${this.src} style="flex:1; min-width:0"></bx-logs>` : nothing}
            ${this._layout === 'prs' ? html`<bx-prs component=${this.src} style="flex:1; min-width:0"></bx-prs>` : nothing}`}
            <div class="term-host" style="display:${this._panelVisible() ? 'flex' : 'none'}; flex-direction:column">
            ${repeat(this._sessions, (s) => s.key, (s, i) => s.kind === 'agent'
              ? html`<bx-agent style="height:100%; display:${i === this._active ? 'flex' : 'none'}"
                  component=${this.src} session=${s.id ?? nothing}
                  @bx-session=${(ev) => this._gotSession(i, ev)}
                  @bx-exit=${() => this._closeTerm(i, true)}></bx-agent>`
              : html`<bx-terminal style="height:100%; display:${i === this._active ? 'block' : 'none'}"
                  cwd=${this.src} session=${s.id ?? nothing} net=${s.net || nothing} gpu=${s.gpu || 'none'} api=${s.api === false ? '0' : '1'}
                  @bx-session=${(ev) => this._gotSession(i, ev)}
                  @bx-exit=${() => this._closeTerm(i, true)}></bx-terminal>`)}
            </div>
          </div>
        </div>`)(this._popBox()) : nothing}
    `;
  }
}

customElements.define('bx-frame', BxFrame);
