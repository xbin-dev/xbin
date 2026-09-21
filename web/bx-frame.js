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
import '/vendor/bx-terminal.js';
import '/vendor/bx-code.js';
import '/vendor/bx-logs.js';
import '/vendor/bx-prs.js';
import { deepActive, clampBox, dragWindow, dragPointer, anchorBox, anchorOffsets, followBox } from '/vendor/bx-kit.js';
import { makeStore, tabsFrom, activeIndex, uid } from '/vendor/term-sessions.js';
import { titlebar, toolsRow, titlebarCss } from '/vendor/frame-titlebar.js';
import { agentProviders, rememberKind, launcherItems, launcher, launcherCss } from '/vendor/frame-launcher.js';
import '/vendor/bx-agent.js';
import '/vendor/bx-dialog.js';
import '/vendor/bx-menu.js';

// Shared z-order for all terminal windows on the page.
let zTop = 2000;
// On phones the pop-up is a full-screen sheet (CSS) — no geometry to follow.
const SHEET = typeof matchMedia === 'function' ? matchMedia('(max-width: 820px)') : { matches: false };

// The terminal session directory (D73): which live sessions are the user's
// on a tile is asked of the server, never remembered in this browser.
const sessions = makeStore();

// clampBox lives in bx-kit; re-exported because importers of this module
// relied on it before the kit existed (docs/frontend-kit.md — kept).
export { clampBox };

// endSession: the API-side end of a session of either kind (audited; the
// twin of DELETE /ws/term?session=, which the pre-D74 browser used).
const endSession = (id) => fetch(`/api/xbin/term/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => { });

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
    _z: { state: true },       // this window's place in the shared z-order (in the style binding: a render never drops it)
    _narrow: { state: true },  // the pop is too narrow for the full bar: the pickers live in the tools row
    _tools: { state: true },   // the tools row is open (narrow only)
    _dialog: { state: true },  // an open <bx-dialog>: {spec, resolve}
    _providers: { state: true }, // the agent providers, for the launcher (null until fetched)
    _menu: { state: true },    // an open <bx-menu>: {items, anchor, sheet}
    // popBounds: () → a viewport rect the pop-up's top-left stays inside, or
    // null (the viewport clamps instead). The shell's canvas sets it (D66).
    popBounds: { attribute: false },
  };

  static styles = [titlebarCss, launcherCss, css`
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
      position: fixed; box-sizing: border-box;
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
    .panels { display: flex; flex: 1; min-height: 0; }
    bx-code { min-width: 0; overflow: hidden; border-right: 1px solid var(--bx-border, #363c45); }
    .vsplit { flex: none; width: 5px; cursor: col-resize; background: var(--bx-border, #363c45); }
    .vsplit:hover { background: var(--bx-accent, #f5a623); }
    .term-host { flex: 1; min-height: 0; min-width: 0; background: var(--bx-term-bg, #262c36); }
  `];

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
    this._restored = false; this._hadWindow = false; this._frameKey = 0;
    this._z = ++zTop; this._narrow = false; this._tools = false; this._dialog = null;
    this._activeKey = null; // the active tab's key: the selection tracks identity, not position
    this._ro = null; // ResizeObserver on the pop (narrow mode, size persistence)
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
    if (changed.has('_active') || changed.has('_termOpen') || changed.has('_layout') || changed.has('_codeW')) this._saveTerm();
    if (changed.has('_termOpen')) {
      if (this._termOpen) { this._follow(); this._observePop(); } else { this._ro?.disconnect(); this._ro = null; }
      this._popChanged();
    }
  }

  // A ResizeObserver on the pop: below ~640 px (or on the phone sheet) the
  // title bar degrades — the pickers move to the tools row — and a native
  // resize is remembered (the handle writes nothing; only a pointerdown or a
  // drag end used to read the size back).
  _observePop() {
    const el = this._popEl;
    if (!el || this._ro || typeof ResizeObserver !== 'function') return;
    this._ro = new ResizeObserver(() => {
      const w = el.offsetWidth, h = el.offsetHeight;
      this._narrow = SHEET.matches || w < 640;
      if (this._pop && !SHEET.matches && (Math.abs(this._pop.w - w) > 1 || Math.abs(this._pop.h - h) > 1)) { this._pop.w = w; this._pop.h = h; this._saveTerm(); }
    });
    this._ro.observe(el);
  }

  // The active tab: an index for rendering, a key for identity — a listing
  // that reorders or drops a tab never moves the selection onto a different
  // session (activeIndex, term-sessions.js).
  _setActive(i) {
    this._active = activeIndex(this._sessions, this._sessions[i]?.key, i);
    this._activeKey = this._sessions[this._active]?.key ?? null;
  }
  _reindex() {
    this._active = activeIndex(this._sessions, this._activeKey, this._active);
    this._activeKey = this._sessions[this._active]?.key ?? null;
  }
  get _isAgent() { return this._sessions[this._active]?.kind === 'agent'; }

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
    const w = this._termOpen || this._sessions.length
      ? { open: !!this._termOpen, active: this._active, activeId: this._sessions[this._active]?.id ?? null, pop: this._pop, layout: this._layout, codeW: this._codeW }
      : null;
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
      const byId = w.activeId ? this._sessions.findIndex((t) => t.id === w.activeId) : -1;
      this._setActive(byId >= 0 ? byId : (w.active | 0));
      if (w.layout) this._layout = w.layout;
      if (w.codeW) this._codeW = w.codeW;
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
    this._reindex();
    if (!this._sessions.length && this._termOpen) this._termOpen = false;
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    mountedFrames.delete(this);
    this._stopFollow?.();
    this._ro?.disconnect(); this._ro = null;
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
      get tabs() { return f._sessions.map((s) => ({ kind: s.kind || 'shell', id: s.id, name: s.name, provider: s.provider, status: s.status, ended: !!s.ended, net: s.net, api: s.api !== false, gpu: s.gpu })); },
      get activeTab() { return f._active; },
      setActiveTab(i) { f._setActive(i | 0); },
      get layout() { return f._layout; },
      get narrow() { return f._narrow; },
      newTerm() { f._newTerm(); },
      newAgent() { f._newAgent(); },
      startKind(kind, provider, opts) { f._startKind(kind, provider, opts || {}); }, // launcher path (a provider eager-creates)
      launcherItems() { return launcherItems(f).map((it) => it.label || it.kind || (it.kind === 'sep' ? '—' : '')); },
      closeTab(i) { f._closeTerm(i | 0); },
      get dialog() { return f._dialog?.spec ?? null; },
      answerDialog(button, values = {}) { f._dialogDone({ detail: { button, values } }); },
      // the <bx-agent> testApi for tab i (default: the active one); null for a shell tab
      agent(i = f._active) {
        const t = f._sessions[i];
        if (t?.kind !== 'agent') return null;
        const idx = f._sessions.filter((s) => s.kind === 'agent').indexOf(t);
        return f.renderRoot.querySelectorAll('bx-agent')[idx]?.testApi?.() ?? null;
      },
    };
  }

  _toggleTerm() {
    if (this._termOpen) { this._termOpen = false; return; }
    if (!this._pop) {
      // Anchor at the frame's top-right; when the tile is on screen, also inside the window.
      const r = this.getBoundingClientRect(), w = 680, h = 400;
      const box = anchorBox(r, { dx: r.width - w, dy: 8, w, h }, this._bounds());
      const visible = r.right > 0 && r.bottom > 0 && r.left < window.innerWidth && r.top < window.innerHeight;
      this._setPopBox(visible ? clampBox(box) : box);
    }
    this._termOpen = true;
    if (this._gpus.length === 0) gpuInventory().then((g) => { this._gpus = g; });
    if (!this._providers) agentProviders().then((p) => { this._providers = p; });
    // no auto-bash: an empty window shows the launcher chooser (render()).
    this._loadPRCount();
    this.updateComplete.then(() => this._front());
  }

  _front() { this._z = ++zTop; }

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
    if (!el || !this._pop || (Math.abs(this._pop.w - el.offsetWidth) <= 1 && Math.abs(this._pop.h - el.offsetHeight) <= 1)) return;
    this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight; this._saveTerm(); this._popChanged();
  }

  _newTerm() { this._startKind('shell'); }
  _newAgent() { this._startKind('agent'); }

  // Start a session of a kind from the launcher (a card or the + menu). A
  // shell opens a <bx-terminal> (optionally running `run` on first connect —
  // a sign-in command); an agent opens a <bx-agent> which creates the
  // session eagerly on the chosen provider so its model/mode pickers load
  // before the first prompt. Remembered as the "last choice".
  _startKind(kind, provider, opts = {}) {
    rememberKind(provider ? { kind, provider } : { kind });
    const tab = { key: uid(), id: null, kind, name: '' };
    if (kind === 'shell') { tab.net = null; tab.gpu = 'none'; if (opts.run) tab.run = opts.run; }
    else { tab.provider = provider; this._layout = 'term'; }
    this._sessions = [...this._sessions, tab];
    this._setActive(this._sessions.length - 1);
  }

  // open the + menu anchored under the button
  _openLauncher(e) {
    if (!this._providers) agentProviders().then((p) => { this._providers = p; });
    this._menu = { items: launcherItems(this), anchor: e.currentTarget.getBoundingClientRect(), sheet: SHEET.matches };
  }

  // a sign-in request from an agent tab: open a shell tab that runs the login
  // command (its output — a clickable URL — is right there; the shared $HOME
  // means the agent then picks up the login).
  _signIn(ev) {
    const run = ev.detail?.run;
    if (run) this._startKind('shell', null, { run });
  }

  // The term-host holds both the shells and the agents; it shows whenever the
  // active tab is an agent (agents have no code/logs panels) or a shell tab
  // is in a terminal-bearing layout.
  _panelVisible() { return this._isAgent || this._layout === 'term' || this._layout === 'split'; }


  // Rename the terminal on tab i (blank clears back to its number). Names are
  // per-component and persist like the session list.
  async _renameTerm(i) {
    const cur = this._sessions[i];
    if (!cur) return;
    const agent = cur.kind === 'agent';
    const n = await this._prompt(agent ? 'Name this agent tab' : 'Name this terminal',
      agent ? "blank shows the provider's name" : 'blank numbers it', cur.name || '');
    if (n === null) return;
    const s = [...this._sessions];
    s[i] = { ...cur, name: n.trim() };
    this._sessions = s;
    if (cur.id) sessions.rename(cur.id, n.trim()); // the name lives on the session (D73)
  }

  // Close terminal i. ended=true means the shell already exited (no DELETE
  // needed); otherwise this is a user close and we end the server session.
  // Closing the last one closes the window.
  // ref: an index (the title bar) or a key (an element's exit event).
  // ended=true: the session is already gone (no DELETE); an ended tab is only
  // dismissed. Closing the last tab closes the window.
  _closeTerm(ref, ended = false) {
    const i = typeof ref === 'number' ? ref : this._sessions.findIndex((t) => t.key === ref);
    const s = this._sessions[i];
    if (!s) return;
    if (!ended && !s.ended && s.id) endSession(s.id);
    const rest = this._sessions.filter((_, j) => j !== i);
    if (!rest.length) { this._sessions = []; this._termOpen = false; return; }
    const wasActive = i === this._active;
    this._sessions = rest;
    if (wasActive) this._setActive(Math.min(i, rest.length - 1)); else this._reindex();
  }

  // An agent tab's session ended (exit, crash, a login error): the tab and
  // its transcript stay, greyed, until the user dismisses it — the reason it
  // ended is exactly what must not vanish.
  _endTab(key) {
    this._sessions = this._sessions.map((t) => (t.key === key ? { ...t, ended: true } : t));
  }

  // Themed, harness-drivable dialogs (bx-dialog, rendered in the pop) in
  // place of the native confirm()/prompt(): one at a time.
  _ask(spec) { return new Promise((resolve) => { this._dialog = { spec, resolve }; }); }
  async _confirm(title, message, okLabel = 'OK') {
    const r = await this._ask({ title, message, buttons: [{ label: 'Cancel', value: null }, { label: okLabel, value: 'ok', primary: true }] });
    return r?.button === 'ok';
  }
  async _prompt(title, label, value) {
    const r = await this._ask({ title, fields: [{ name: 'v', label, value }] });
    return r?.button === 'ok' ? String(r.values?.v ?? '') : null;
  }
  _dialogDone(e) { const d = this._dialog; this._dialog = null; d?.resolve(e.detail); }

  // The element on tab `key` learned its session id (bx-terminal's session
  // frame, bx-agent's create). A listing may already have absorbed the same
  // id into another tab: one tab per session — the element's own tab wins.
  _gotSession(key, ev) {
    const d = ev.detail;
    const s = this._sessions.filter((t) => t.key === key || t.id !== d.id);
    const i = s.findIndex((t) => t.key === key);
    if (i < 0) return;
    const cur = s[i];
    // The server reports the EFFECTIVE scope plus the scopes this user may
    // pick on this tile — the select renders exactly that list (D54).
    s[i] = { ...cur, id: d.id, kind: d.kind || cur.kind || 'shell', name: cur.name || d.name || '',
             net: d.net || cur.net || null, scopes: d.scopes || cur.scopes || null, label: d.label || '',
             baseOutdated: !!d.baseOutdated };
    this._sessions = s;
    this._reindex();
  }

  // Restart tab i's session with a changed picker: the netns/relay, the
  // device binds and the per-session token are fixed at spawn. A live session
  // is ended only after the user confirms — it takes its shell, its jobs and
  // its scrollback with it. Resolves false when declined (the picker snaps
  // back; bx-terminal reconnects on the attribute change otherwise).
  async _respawn(i, patch, what) {
    const cur = this._sessions[i];
    if (!cur) return false;
    const live = cur.id && !cur.ended;
    if (live && !(await this._confirm(`Restart this terminal ${what}?`, 'Its shell and anything running in it end, and the scrollback is lost.', 'Restart'))) return false;
    if (live) endSession(cur.id);
    const s = [...this._sessions];
    s[i] = { ...cur, id: null, ended: false, ...patch };
    this._sessions = s;
    return true;
  }
  _setNet(i, net) { const c = this._sessions[i]; return !c || c.net === net ? Promise.resolve(false) : this._respawn(i, { net }, `on the ${net} network`); }
  _setGpu(i, gpu) { const c = this._sessions[i]; return !c || (c.gpu || 'none') === gpu ? Promise.resolve(false) : this._respawn(i, { gpu }, gpu === 'none' ? 'without a GPU' : `with GPU ${gpu}`); }
  _setApi(i, on) { const c = this._sessions[i]; return !c || (c.api !== false) === on ? Promise.resolve(false) : this._respawn(i, { api: on }, on ? 'with tile API access' : 'without API access'); }

  // Reset the tile's persistent terminal layer (installed packages, system
  // config) — or rebuild it on the newer base image. The server ends every
  // session on the layer, so every shell tab spawns fresh on the clean one
  // and an agent tab ends (the listing marks it).
  async _resetEnv(upgrade = false) {
    const ok = await this._confirm(
      upgrade ? `Rebuild ${this.src}'s terminals on the newer base image?` : `Reset the sandbox for ${this.src}?`,
      `${upgrade ? 'The persistent layer is rebuilt on the current base. ' : ''}Installed packages and system changes in this tile's terminals are wiped; your files and $HOME are kept. Every terminal and agent on this tile restarts.`,
      upgrade ? 'Rebuild' : 'Reset');
    if (!ok) return;
    fetch(`/ws/term/env?cwd=${encodeURIComponent(this.src)}`, { method: 'DELETE' })
      .catch(() => { })
      .finally(() => {
        this._sessions = this._sessions.map((t) => (t.kind === 'agent' ? t : { ...t, id: null }));
        this.renderRoot?.querySelectorAll('bx-terminal').forEach((el) => el.restartFresh?.());
      });
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
      this._saveTerm(); this._popChanged();
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
        <div class="pop ${this._narrow ? 'narrow' : ''}"
             style="left:${x}px; top:${y}px; width:${w}px; height:${h}px; z-index:${this._z}"
             @pointerdown=${this._popDown}>
          ${titlebar(this)}
          ${this._narrow && this._tools ? toolsRow(this) : nothing}
          ${this._dialog ? html`<bx-dialog open .spec=${this._dialog.spec} @bx-dialog-resolve=${this._dialogDone}></bx-dialog>` : nothing}
          ${this._menu ? html`<bx-menu open .items=${this._menu.items} .anchor=${this._menu.anchor} ?sheet=${this._menu.sheet}
              @bx-menu-close=${() => { this._menu = null; }}></bx-menu>` : nothing}
          <div class="panels">
            ${this._isAgent ? nothing : html`
            ${this._layout === 'code' || this._layout === 'split' ? html`<bx-code src=${this.src}
                style="flex-basis:${this._layout === 'split' ? this._codeW + '%' : '100%'}"></bx-code>` : nothing}
            ${this._layout === 'split' ? html`<div class="vsplit" @pointerdown=${this._splitStart}></div>` : nothing}
            ${this._layout === 'logs' ? html`<bx-logs component=${this.src} style="flex:1; min-width:0"></bx-logs>` : nothing}
            ${this._layout === 'prs' ? html`<bx-prs component=${this.src} style="flex:1; min-width:0"></bx-prs>` : nothing}`}
            <div class="term-host" style="display:${this._sessions.length === 0 || this._panelVisible() ? 'flex' : 'none'}; flex-direction:column">
            ${this._sessions.length === 0 ? launcher(this) : nothing}
            ${repeat(this._sessions, (s) => s.key, (s, i) => s.kind === 'agent'
              ? html`<bx-agent style="height:100%; display:${i === this._active ? 'flex' : 'none'}"
                  component=${this.src} session=${s.id ?? nothing} provider=${s.provider || nothing} ?ended=${!!s.ended}
                  @bx-session=${(ev) => this._gotSession(s.key, ev)}
                  @bx-open-terminal=${this._signIn}
                  @bx-exit=${() => this._endTab(s.key)}></bx-agent>`
              : html`<bx-terminal style="height:100%; display:${i === this._active ? 'block' : 'none'}"
                  cwd=${this.src} session=${s.id ?? nothing} net=${s.net || nothing} gpu=${s.gpu || 'none'} api=${s.api === false ? '0' : '1'} run=${s.run || nothing}
                  @bx-session=${(ev) => this._gotSession(s.key, ev)}
                  @bx-exit=${() => this._closeTerm(s.key, true)}></bx-terminal>`)}
            </div>
          </div>
        </div>`)(this._popBox()) : nothing}
    `;
  }
}

customElements.define('bx-frame', BxFrame);
