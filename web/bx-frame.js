/**
 * <bx-frame src="apps/calendar"> — the core xbin element: renders a
 * component's index.html in an iframe, carries the always-visible
 * 7×7 edit button, live-reloads on source changes, shows build errors as an
 * overlay, and hosts the terminal pop-up (persistent PTY sessions cwd'd to
 * the component's source directory) plus its panels — a code browser /
 * review panel (bx-code), a read-only backend log view (bx-logs), the
 * change-proposal panel (bx-prs — cross-tile "code PRs") and the
 * Deployments panel (bx-deployments) — each full width or beside the
 * terminal (frame-panels.js, D129). The tile's live reload state
 * (frame-deploy.js) shows on the window's bar and, while the tile's primary
 * is pinned, as a chip over the tile; a session's target deployment is its
 * tile API select's entry. With src="<tile>+<name>" the frame shows that
 * deployment, reloading on its `deployments` events.
 *
 * Attributes:
 *   src     — component path (workspace-relative)
 *   height  — fixed CSS height; omit for auto-height (the framed document
 *             reports its size via xbin-client.js). Reactive (D187): adding
 *             or removing it — or a CSS height in the element's style —
 *             switches the mode in place, without reloading the tile; a
 *             frame turning auto takes the height its document last said
 *   no-edit — hide the edit button
 *
 * Browser-plane isolation (docs/auth.md §Who is calling): non-chrome components load in a
 * SANDBOXED iframe (opaque origin: no DOM access either way, no storage, no
 * ambient cookie — the tile's only credential is its injected frame token).
 * Chrome components (root, shell, admin-approved chrome:true) run unsandboxed and
 * act as the signed-in human. Where the browser supports it, sandboxed frames
 * are also credentialless (no cookies even on the navigation, so the document
 * load authenticates with a bootstrap frame token in the URL). Under strict
 * tile asset gating's origins mode each tile loads on its own origin instead
 * (docs/auth.md §Tile asset gating) — frame-info.js decides.
 *
 * The edit button opens a floating terminal window: anchored at the frame's
 * top-right corner when opened, draggable by its title bar, resizable by the
 * native bottom-right handle (ctrl+scroll inside adjusts the font). It is
 * viewport-fixed (container overflow can't clip it) but positioned RELATIVE
 * to this frame (D66): it follows the tile through scrolls and drags, and
 * stays inside the host's `popBounds` (the shell's canvas) so it is always
 * reachable by scrolling. Windows share a bring-to-front z-order.
 *
 * Base Two (D184): the pop-up is a window (product-ui 3) — a 28 px title
 * bar, the live square, 1 px window edge, the window shadows; the one
 * brought to the front last on the page is the active one (the
 * `bx-window-front` event, which the shell's windows share), and a 3 px
 * part tab says what its active tab is: terminal or agent session. The
 * frame tells its build state (buildState: live | building | failed) with
 * a `bx-build` event, so an embedder's title bar can draw it. When the
 * frame's own document follows the person's appearance (data-bx-theme),
 * the frame posts it to its iframe on every load and change
 * (xbin:appearance, docs/protocol.md), so a tile document follows its
 * embedder live.
 *
 * See /docs/elements.md.
 */
import { LitElement, html, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { keyed } from 'lit';
import { appearanceMessage, follows, onAppearance } from '/vendor/bx-theme.js';
import '/vendor/bx-icons.js';
import { onEvent, mountedFrames, isReloadTarget } from '/vendor/events-socket.js';
import '/vendor/bx-terminal.js';
import '/vendor/bx-code.js';
import '/vendor/bx-logs.js';
import '/vendor/bx-prs.js';
import '/vendor/bx-deploy.js';
import { deepActive, clampBox, dragWindow, anchorBox, anchorOffsets, followBox } from '/vendor/bx-kit.js';
import { makeStore, tabsFrom, activeIndex, uid, visibleRows } from '/vendor/term-sessions.js';
import { titlebar, toolsRow, titlebarCss, fitBar, barKey } from '/vendor/frame-titlebar.js';
import { agentProviders, rememberKind, launcherItems, launcherCss, loadEnvStatus, loadTileState, restartAgent, wantVM } from '/vendor/frame-launcher.js';
import { panels, panelsCss, setLayout, revealTerm, restoreLayout, layoutPref, PANE_W } from '/vendor/frame-panels.js';
import '/vendor/bx-agent.js';
import '/vendor/bx-dialog.js';
import '/vendor/bx-menu.js';
import { infoFor, refreshFrameInfo, sandboxAttr, frameSource } from '/vendor/frame-info.js';
import { testApi } from '/vendor/frame-testapi.js';
import { deployCss, deployMount, onDeployEvent, keepTargets, setTarget, sessionEcho } from '/vendor/frame-deploy.js';
import { frameBaseCss, frameCss } from '/vendor/frame-css.js';

// Shared z-order for all terminal windows on the page.
let zTop = 2000;
// The page's active window (product-ui 3): the one brought to the front
// last. Every kind of window — these pop-ups, the shell's floats, cards and
// spawned windows — says so with this event {key}, so each knows whether
// it is the one.
const FRONT = 'bx-window-front';
let popN = 0;
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
    // deployment: the tile deployment the page shows ('' — the primary): the
    // shell's window picks it (docs/tile-deployments.md). Only the page
    // follows it — /c/<src>+<deployment>/, its reloads and build overlay
    // from the tile's `deployments` events; terminals, code, logs and
    // proposals stay the tile's.
    deployment: { type: String },
    height: { type: String },
    _termOpen: { state: true },
    _sessions: { state: true },
    _active: { state: true },
    _gpus: { state: true },
    _buildError: { state: true },
    _building: { state: true }, // a build-start without its build-ok / build-error yet
    _popActive: { state: true }, // the pop-up is the page's active window
    _autoHeight: { state: true },
    _layout: { state: true },  // 'term' | 'code' | 'logs' | 'prs' | 'deployments' (never saved: NP-10-2) — frame-panels.js
    _beside: { state: true },  // the terminal sits beside the panel
    _paneW: { state: true },   // the panel's width % beside the terminal
    _frame: { state: true },   // {url, sandboxed, sandbox, credentialless, origin} | null
    _prCount: { state: true }, // open change proposals targeting this tile
    _z: { state: true },       // this window's place in the shared z-order (in the style binding: a render never drops it)
    _narrow: { state: true },  // the pop is too narrow for the full bar: the pickers live in the tools row
    _tools: { state: true },   // the tools row is open (narrow only)
    _dialog: { state: true },  // an open <bx-dialog>: {spec, resolve}
    _providers: { state: true }, // the agent providers, for the launcher (null until fetched)
    _history: { state: true }, // the tile's past agent sessions (frame-launcher.js: recent sessions, resume)
    _envOld: { state: true }, // the tile's terminal layer is on an older base (frame-launcher.js loadTileState)
    _menu: { state: true },    // an open <bx-menu>: {items, anchor, sheet}
    // popBounds: () → a viewport rect the pop-up's top-left stays inside, or
    // null (the viewport clamps instead). The shell's canvas sets it (D66).
    popBounds: { attribute: false },
  };

  static styles = [scrollCss, frameBaseCss, titlebarCss, launcherCss, deployCss, panelsCss, frameCss];

  constructor() {
    super();
    this._termOpen = false;
    this._layout = 'term';
    this._beside = false;
    this._paneW = PANE_W;
    this._sessions = []; // [{id: string|null, net, gpu}] — id null until server assigns
    this._active = 0;
    this._gpus = []; // host GPU inventory (empty unless a GPU host)
    this._buildError = null;
    this._building = false;
    this._autoHeight = false;
    this._popActive = false;
    this._winKey = `pop:${++popN}`; // this pop-up's name in the page's active-window event
    this._onWinFront = (e) => { const on = e.detail?.key === this._winKey; if (on !== this._popActive) this._popActive = on; };
    this._toldBuild = 'live'; // the build state the last bx-build said
    this._pop = null; // {dx, dy, w, h} — offsets from this frame's box; owned imperatively after open
    this._stopFollow = null; // the follow loop's stop while the pop-up is open
    this._offEvents = null;
    this._frame = null; // {url, sandboxed, sandbox, credentialless, origin} — null until resolved (frame-info.js)
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
    this._syncAutoHeight();
    // the embedder sets or clears a CSS height: auto-height follows (D187)
    this._styleMo = new MutationObserver(() => this._syncAutoHeight());
    this._styleMo.observe(this, { attributes: true, attributeFilter: ['style'] });
    this._offEvents = onEvent((e) => this._event(e));
    window.addEventListener('message', this._onMsg);
    window.addEventListener(FRONT, this._onWinFront);
    document.addEventListener('visibilitychange', this._onVisible);
    // the person's appearance changed (or the system's): the framed document follows
    this._offAppearance = onAppearance(() => this._postAppearance(), this.ownerDocument.defaultView || window);
    this._restoreTerm();
    this._prepareFrame();
    deployMount(this); // the live reload state, when the tile's primary is pinned (frame-deploy.js)
  }

  // The appearance relay (D184, docs/protocol.md xbin:appearance): when this
  // frame's own document follows the person (data-bx-theme="auto"), its
  // theme and density go to the iframe — on every load and every change —
  // so the tile matches its embedder. A document that never opted in sends
  // nothing: a new tile inside it keeps what xbind injected. The message
  // carries no credential; on its own origin (origins mode) it goes to that
  // origin only.
  _postAppearance() {
    const doc = this.ownerDocument;
    if (!follows(doc)) return;
    const w = this._iframe?.contentWindow;
    if (!w) return;
    try { w.postMessage(appearanceMessage(doc), this._frame?.origin || '*'); } catch { /* the frame is navigating */ }
  }

  // buildState: what the tile's code is doing now — 'failed' (its last build
  // failed: the overlay shows why), 'building' (a build started), or 'live'.
  // A change is announced as `bx-build` {state} (bubbles, composed) for the
  // embedder's title bar (the shell's live square).
  get buildState() { return this._buildError !== null ? 'failed' : this._building ? 'building' : 'live'; }

  // Resolve how this frame must load (sandboxed? credentialless? its own
  // tile origin?) before the iframe exists — frame-info.js.
  get _page() { return this.deployment ? `${this.src}+${this.deployment}` : this.src; }
  async _prepareFrame() {
    const page = this._page;
    const info = await infoFor(page);
    const next = await frameSource(page, info);
    if (page !== this._page) return; // the window switched deployments meanwhile
    // A changed token set (a cap:open-links grant approved or revoked) must
    // reach a FRESH element: the attribute applies only to the next
    // navigation, so re-keying the iframe keeps the flags unambiguous.
    if (this._frame && this._frame.sandbox !== next.sandbox) this._frameKey = (this._frameKey ?? 0) + 1;
    this._frame = next;
  }

  // A grant for this component changed. If it moved the sandbox token set
  // (cap:open-links), re-prepare — the re-keyed iframe loads the new document
  // with the new flags; otherwise the plain reload so a frontend that was
  // 403'ing retries against its new permissions.
  async _regrant() {
    const before = this._frame?.sandbox;
    await refreshFrameInfo(this._page);
    if (sandboxAttr(await infoFor(this._page)) !== before) {
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
    if (changed.has('deployment') && changed.get('deployment') !== undefined) { // another deployment's page
      this._buildError = null; this._building = false; this._frameKey++; this._prepareFrame();
    }
    if ((changed.has('_buildError') || changed.has('_building')) && this.buildState !== this._toldBuild) {
      this._toldBuild = this.buildState;
      this.dispatchEvent(new CustomEvent('bx-build', { bubbles: true, composed: true, detail: { state: this._toldBuild } }));
    }
    if (changed.has('_active') || changed.has('_termOpen') || changed.has('_layout') || changed.has('_beside') || changed.has('_paneW')) this._saveTerm();
    if (changed.has('_termOpen')) {
      if (this._termOpen) { this._follow(); this._observePop(); this._loadWindowState(); } else { this._ro?.disconnect(); this._ro = null; }
      // the active window closed: none is, until another comes to the front
      if (!this._termOpen && this._popActive) window.dispatchEvent(new CustomEvent(FRONT, { detail: { key: '' } }));
      this._popChanged();
    }
    const el = this._termOpen && this._popEl;
    if (el) { // the bar's content may have changed: does the full bar still fit? (fitBar)
      const k = barKey(this);
      if (k !== this._barKey) { this._barKey = k; this._barNeed = 0; }
      const n = fitBar(this, el, SHEET.matches);
      if (n !== this._narrow) this._narrow = n;
    }
  }

  // What the window shows besides its tabs — the GPU picker, the launcher's
  // providers, the tile state (VM toggle, base update, history), the PR
  // badge — loaded whenever it opens: a click, open(), or a window restored
  // open from the pref (which never went through _toggleTerm).
  _loadWindowState() {
    if (this._gpus.length === 0) gpuInventory().then((g) => { this._gpus = g; });
    if (!this._providers) agentProviders().then((p) => { this._providers = p; });
    loadTileState(this);
    this._loadPRCount();
  }

  // A ResizeObserver on the pop: when the full title bar no longer fits
  // (fitBar: below ~640 px, the phone sheet, or measured) it degrades — the
  // pickers move to the tools row — and a native resize is remembered (the
  // handle writes nothing; only a pointerdown or a drag end used to read the
  // size back).
  _observePop() {
    const el = this._popEl;
    if (!el || this._ro || typeof ResizeObserver !== 'function') return;
    this._ro = new ResizeObserver(() => {
      const w = el.offsetWidth, h = el.offsetHeight;
      this._narrow = fitBar(this, el, SHEET.matches);
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
      ? { open: !!this._termOpen, active: this._active, activeId: this._sessions[this._active]?.id ?? null, pop: this._pop, ...layoutPref(this) }
      : null;
    if (w || this._hadWindow) sessions.saveWindow(this.src, w);
    this._hadWindow = !!w;
  }

  // On mount: the user's live sessions on this tile from the server (they are
  // the same in every browser the user signs into), the window as they left
  // it, and — once — whatever the browser's legacy record held (D73).
  async _restoreTerm() {
    const legacy = sessions.migrateLegacy(this.src);
    const [all, win] = await Promise.all([sessions.list(this.src), sessions.loadWindow(this.src)]);
    const rows = visibleRows(all); // never a sign-in's session (D178)
    if (!this.isConnected) return;
    for (const r of rows) if (legacy?.names?.[r.id] && !r.name) { r.name = legacy.names[r.id]; sessions.rename(r.id, r.name); }
    this._sessions = keepTargets(tabsFrom(rows, this._sessions), this._sessions, rows);
    const w = win ?? legacy?.window;
    if (w) {
      this._hadWindow = !!win;
      const byId = w.activeId ? this._sessions.findIndex((t) => t.id === w.activeId) : -1;
      this._setActive(byId >= 0 ? byId : (w.active | 0));
      restoreLayout(this, w);
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
    const gen = this._idGen;
    const rows = visibleRows(await sessions.list(this.src));
    if (!this.isConnected) return;
    if (gen !== this._idGen) return this._relist(); // a tab got its id meanwhile: this listing may predate that session
    this._sessions = keepTargets(tabsFrom(rows, this._sessions), this._sessions, rows);
    this._reindex();
    loadTileState(this); // a session that ended is history now; a reset/rebuilt layer is current
    if (!this._sessions.length && this._termOpen) this._termOpen = false;
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    mountedFrames.delete(this);
    this._styleMo?.disconnect(); this._styleMo = null;
    this._stopFollow?.();
    this._ro?.disconnect(); this._ro = null;
    this._offEvents?.();
    this._offAppearance?.();
    window.removeEventListener('message', this._onMsg);
    window.removeEventListener(FRONT, this._onWinFront);
    document.removeEventListener('visibilitychange', this._onVisible);
    if (this._winTimer) this._flushWindow();
  }

  get _iframe() { return this.renderRoot?.querySelector('iframe'); }
  get _popEl() { return this.renderRoot?.querySelector('.pop'); }

  _event(e) {
    if (e.type === 'deployments') return onDeployEvent(this, e); // every one: a frame of <tile>+<name> hears its tile's (frame-deploy.js)
    if (e.type === 'workspace-settings') return loadEnvStatus(this); // base auto-update on/off: what the chooser says (D175)
    if (!e.component) return;
    const mine = e.component === this.src || e.component.startsWith(this.src + '/');
    if (!mine) return;
    switch (e.type) {
      case 'reload': // (the primary's: a window on another deployment hears its own in `deployments`)
        if (!this.deployment && isReloadTarget(this, e.component)) this._reload();
        break;
      case 'build-start':
        if (e.component === this.src && !this.deployment) this._building = true;
        break;
      case 'build-error':
        if (e.component === this.src && !this.deployment) { this._buildError = e.text || 'build failed'; this._building = false; }
        break;
      case 'build-ok':
        if (e.component === this.src && !this.deployment) { this._buildError = null; this._building = false; }
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
      case 'term': // the session directory changed for this tile (D73); a status op carries its own row data
        if (e.component === this.src && e.data?.op !== 'status') this._relist();
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
    this._building = false;
    this._beginReload();
    // Sandboxed frames are opaque origins (or, in origins mode, another
    // origin) — we can't reach contentWindow — so reload by re-navigation:
    // re-minting the bootstrap token when credentialless (the old one may
    // have expired); in origins mode the workspace URL again (the server
    // sends it on to the tile origin with a fresh ticket).
    if (this._frame?.credentialless) { this._renavigate(); return; }
    if (this._frame?.sandboxed) { const f = this._iframe; if (f) f.src = this._url(); return; }
    try { this._iframe?.contentWindow?.location.reload(); }
    catch { if (this._iframe) this._iframe.src = this._url(); }
  }

  // A credentialless reload loads a freshly minted bootstrap URL. Tokens
  // minted within one second are identical, and an unchanged src binding
  // never navigates, so a second reload that soon (a save right after a
  // deploy's reload) would be lost: that URL is navigated to directly.
  async _renavigate() {
    const before = this._frame?.url;
    await this._prepareFrame();
    if (this._frame?.url === before && this._iframe) this._iframe.src = before;
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
    // every load, the first one too: a choice that changed while the frame
    // was loading is corrected (the relay; D184)
    this._postAppearance();
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

  // Auto-height (no `height`, no CSS height) is decided again whenever
  // either changes, not once on connect (D187): an embedder switches a tile
  // between a fixed and a grown-to-content frame without reloading it.
  willUpdate(changed) {
    if (changed.has('height')) this._syncAutoHeight();
  }
  _syncAutoHeight() {
    const auto = !this.height && !this.style.height;
    if (auto === this._autoHeight) return;
    this._autoHeight = auto;
    if (auto) this._applyDocHeight();
    else this.style.removeProperty('--bx-frame-height');
  }
  // the document's last reported height, clamped as ever (24–20000 px)
  _applyDocHeight() {
    if (this._docHeight > 0) this.style.setProperty('--bx-frame-height', Math.max(24, Math.min(this._docHeight, 20000)) + 'px');
  }

  _message(e) {
    // Only trust messages from OUR iframe — the sender window IS the identity,
    // so a tile can't spoof another component's requests. On its own origin
    // (origins mode) the document must also BE that origin: whatever else the
    // frame navigated to (another tile's refusal page, the workspace) is
    // not this tile.
    if (e.source !== this._iframe?.contentWindow) return;
    if (this._frame?.origin && e.origin !== this._frame.origin) return;
    const d = e.data;
    if (typeof d?.type !== 'string' || !d.type.startsWith('xbin:')) return;

    if (d.type === 'xbin:resize') {
      this._docHeight = Number(d.height) || 0; // kept while fixed: the frame may turn auto later
      if (this._autoHeight) this._applyDocHeight();
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
            // iframe's window regardless; xbin-client verifies e.source. On
            // its own origin (origins mode) the reply goes to that origin
            // only.
            { type: 'xbin:reply', id: d.id, result }, this._frame?.origin || '*'),
        },
      }));
    } else if (d.type === 'xbin:window-close') {
      this.dispatchEvent(new CustomEvent('bx-spawn-close', {
        bubbles: true, composed: true, detail: { id: d.id },
      }));
    } else if (d.type === 'xbin:open-deployments') {
      // A tile asks the shell to open ANOTHER tile's terminal window on its
      // Deployments layout (the admin tile's deployments tab links there).
      // Only a well-formed tile path goes up; the shell opens it only for a
      // tile its viewer lists, and the panel then runs as the viewer, so the
      // request grants nothing.
      const tile = typeof d.tile === 'string' ? d.tile.replace(/^\/+|\/+$/g, '') : '';
      if (!tile || tile.length > 512 || tile.split('/').some((s) => !s || s === '.' || s === '..')) return;
      this.dispatchEvent(new CustomEvent('bx-open-deployments', {
        bubbles: true, composed: true, detail: { from: this.src, tile },
      }));
    }
  }

  _url() { return `/c/${this._page}/`; }

  // ---- terminal window ----

  // toggleTerminal opens/closes this frame's terminal — the public entry the
  // shell's tile-header button uses (the 7x7 corner button stays for
  // standalone embeds; the shell hides it via no-edit).
  toggleTerminal() { this._toggleTerm(); }

  // open(layout) makes sure the pop-up is open and shows the given panel:
  // 'term' | 'code' | 'split' | 'logs' | 'prs' | 'deployments' (omit to keep
  // the current one), full width — 'split' is code beside the terminal. The
  // shell's tile menu uses it for "terminal / logs / source / proposals /
  // deployments".
  open(layout) {
    if (!this._termOpen) this._toggleTerm();
    else this.fitToViewport(); // already open: make sure it can be seen
    if (layout) setLayout(this, layout, false);
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
  testApi() { return testApi(this); }

  _toggleTerm() {
    if (this._termOpen) { this._termOpen = false; return; }
    if (!this._pop) {
      // Anchor at the frame's top-right; when the tile is on screen, also inside the window.
      const r = this.getBoundingClientRect(), w = 680, h = 400;
      const box = anchorBox(r, { dx: r.width - w, dy: 8, w, h }, this._bounds());
      const visible = r.right > 0 && r.bottom > 0 && r.left < window.innerWidth && r.top < window.innerHeight;
      this._setPopBox(visible ? clampBox(box) : box);
    }
    this._termOpen = true; // updated() loads what the window shows (_loadWindowState)
    // no auto-bash: an empty window shows the launcher chooser (render()).
    this.updateComplete.then(() => this._front());
  }

  // to the front: the top of the pop-ups' z-order, and the page's active window
  _front() {
    this._z = ++zTop;
    if (!this._popActive) window.dispatchEvent(new CustomEvent(FRONT, { detail: { key: this._winKey } }));
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
    if (!el || !this._pop || (Math.abs(this._pop.w - el.offsetWidth) <= 1 && Math.abs(this._pop.h - el.offsetHeight) <= 1)) return;
    this._pop.w = el.offsetWidth; this._pop.h = el.offsetHeight; this._saveTerm(); this._popChanged();
  }

  _newTerm() { this._startKind('shell'); }
  _newAgent() { this._startKind('agent'); }

  // Start a session of a kind from the launcher (a card or the + menu): a
  // shell opens a <bx-terminal> (maybe running `run` first — a sign-in); an
  // agent a <bx-agent> that creates the session eagerly so its model/mode
  // pickers load before the first prompt. Remembered as the "last choice";
  // either starts in a VM sandbox when the tile's choice says so (wantVM).
  _startKind(kind, provider, opts = {}) {
    rememberKind(provider ? { kind, provider } : { kind });
    const tab = { key: uid(), id: null, kind, name: '', vm: opts.vm ?? wantVM(this) };
    if (kind === 'shell') { tab.net = null; tab.gpu = 'none'; if (opts.run) tab.run = opts.run; }
    else tab.provider = provider;
    revealTerm(this); // a panel beside it stays
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

  // the terminal host shows (a past session opened or resumed: frame-launcher.js)
  _revealTerm() { revealTerm(this); }

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
    // The one-shot `run` (a sign-in) is spent: bx-terminal typed it before
    // it said so, and a new element for this tab (the window reopened)
    // must not type it into the live shell again.
    const { run, ...cur } = s[i];
    // The server reports the EFFECTIVE scope plus the scopes this user may
    // pick on this tile — the select renders exactly that list (D54).
    s[i] = { ...cur, id: d.id, kind: d.kind || cur.kind || 'shell', name: cur.name || d.name || '',
             net: d.net || cur.net || null, scopes: d.scopes || cur.scopes || null, label: d.label || '',
             baseOutdated: !!d.baseOutdated, ...sessionEcho(this, ev, key) };
    this._sessions = s;
    this._idGen = (this._idGen | 0) + 1; // a listing already in flight can't know this session (_relist)
    this._reindex();
  }

  // Restart tab i's session with a changed picker: the netns/relay, the
  // device binds and the per-session token are fixed at spawn. A live session
  // is ended only after the user confirms — it takes its shell, its jobs and
  // its scrollback with it. Resolves false when declined (the picker snaps
  // back; bx-terminal reconnects on the attribute change otherwise).
  async _respawn(i, patch, what, message) {
    if (this._sessions[i]?.kind === 'agent') return restartAgent(this, i, patch, what); // keeps the conversation
    const cur = this._sessions[i];
    if (!cur) return false;
    const live = cur.id && !cur.ended;
    if (live && !(await this._confirm(`Restart this terminal ${what}?`, message || 'Its shell and anything running in it end, and the scrollback is lost.', 'Restart'))) return false;
    if (live) endSession(cur.id);
    const s = [...this._sessions];
    const { run, ...rest } = cur; // a restart is not a sign-in again
    s[i] = { ...rest, id: null, ended: false, ...patch };
    this._sessions = s;
    return true;
  }
  _setNet(i, net) { const c = this._sessions[i]; return !c || c.net === net ? Promise.resolve(false) : this._respawn(i, { net }, `on the ${net} network`); }
  _setGpu(i, gpu) { const c = this._sessions[i]; return !c || (c.gpu || 'none') === gpu ? Promise.resolve(false) : this._respawn(i, { gpu }, gpu === 'none' ? 'without a GPU' : `with GPU ${gpu}`); }
  _setApi(i, value) { return setTarget(this, i, value); } // the tile API select: 'on' | 'off', or a target entry (frame-deploy.js)

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
        this._envOld = false; this._sessions = this._sessions.map((t) => (t.kind === 'agent' ? t : { ...t, id: null }));
        this.renderRoot?.querySelectorAll('bx-terminal').forEach((el) => el.restartFresh?.());
      });
  }

  // Switch the pop-up layout: the terminal, or a panel — full width or, with
  // `beside` (omitted: as it is), beside the terminal (frame-panels.js).
  _setLayout(l, beside) { setLayout(this, l, beside); }

  render() {
    const style = this._autoHeight ? nothing
      : `--bx-frame-height: ${this.height || this.style.height}`;
    return html`
      <div class="frame-wrap" style=${style ?? nothing}
           @pointerenter=${() => { this._hover = true; }} @pointerleave=${() => { this._hover = false; }}>
        ${this._frame ? keyed(this._frameKey ?? 0, html`
          <iframe src=${this._frame.url} title=${this._page}
                  sandbox=${this._frame.sandboxed ? this._frame.sandbox : nothing}
                  credentialless=${this._frame.credentialless ? '' : nothing}
                  @load=${() => this._onFrameLoad()}></iframe>`) : nothing}
        ${this._buildError !== null ? html`
          <pre class="overlay"><b><bx-icon name="error"></bx-icon>build failed — ${this._page}</b>\n\n${this._buildError}</pre>` : nothing}
        ${this.hasAttribute('no-edit') ? nothing : html`
          <button class="edit" title="edit ${this.src}" @click=${this._toggleTerm}></button>`}
      </div>
      ${this._termOpen ? (({ x, y, w, h }) => html`
        <div class="pop ${this._narrow ? 'narrow' : ''} ${this._popActive ? 'active' : ''} ${this._isAgent ? 'agent' : ''}"
             style="left:${x}px; top:${y}px; width:${w}px; height:${h}px; z-index:${this._z}"
             @pointerdown=${this._popDown}>
          ${titlebar(this)}
          ${this._narrow && this._tools ? toolsRow(this) : nothing}
          ${this._dialog ? html`<bx-dialog open .spec=${this._dialog.spec} @bx-dialog-resolve=${this._dialogDone}></bx-dialog>` : nothing}
          ${this._menu ? html`<bx-menu open .items=${this._menu.items} .anchor=${this._menu.anchor} ?sheet=${this._menu.sheet}
              @bx-menu-close=${() => { this._menu = null; }}></bx-menu>` : nothing}
          ${panels(this)}
        </div>`)(this._popBox()) : nothing}
    `;
  }
}

customElements.define('bx-frame', BxFrame);
