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
 * (the tabs at the top) — each screen holds its own set of tiles on a **fixed
 * snappable grid** (48px). Drag a card by its title bar to move it (it snaps to
 * the grid); drag the bottom-right corner to resize it. Positions are absolute,
 * so resizing the browser window never rearranges anything, and a tile is a
 * fixed size — its content scrolls inside instead of stretching the card. Open
 * tiles from the sidebar; close with ✕.
 * The sidebar is collapsible («/»), resizable (drag its right edge), and
 * supports view-only **folders**: ＋ folder, then drag components in to
 * organise them — purely visual grouping, nothing moves on disk; drop a
 * component on empty sidebar space to unfile it. All persisted per user.
 * Unpin a card (⧉) to pop it out as a floating, draggable + resizable window
 * (pin back with ▣); its position/size is saved in the layout like everything
 * else. Floating windows are per-screen.
 * The <bx-frame> children of <bx-shell> seed the first screen on first run;
 * after that your saved layout is the source of truth. Theme tokens come from
 * /vendor/theme.css and can be overridden here.
 * **Org screens** (D37/D55) are shared tabs owned by an organisation: view
 * mode is read-only for everyone; "edit layout" opens a personal draft and
 * only "Save and update for everyone" publishes it (revisioned — a stale save
 * asks whether to reload theirs or overwrite). Members may hide, reorder and
 * copy them; org admins rename them from the tab.
 * **Appearance** (D184): the person's theme and density, the `theme` /
 * `density` keys of this bucket, picked in the settings menu and followed
 * across their tabs and devices: shell-appearance.js.
 */
import { LitElement, html, nothing, repeat } from 'lit';
import { appearance } from '/vendor/bx-theme.js';
import '/vendor/bx-frame.js';
import '/vendor/bx-grants.js';
import '/vendor/bx-bindings.js';
import { pendingCount, loadDismissed, dismissedEvent } from '/vendor/bx-dismiss.js';
import './bx-tile-admin.js';
import './bx-part-consent.js';
import '/vendor/bx-dialog.js';
import '/vendor/bx-menu.js';
import { loadBrand, applyFavicon, brandLogo } from './shell-brand.js';
import { openDevices } from './bx-devices.js';
import { accountMenu } from './shell-account.js'; // my account: password, devices…, your partitions

const LAYOUT_PREF = 'layout';
const SETTINGS_PREF = 'settings'; // per-user workspace settings (font size, …)
// The grid scale (D68) is per BROWSER, not per user: one shared layout, and
// each device picks how many pixels a grid unit renders as. localStorage.
const GRID_SCALE_KEY = 'xbin-grid-scale';
const ZOOM_TIP_KEY = 'xbin-zoom-tip';
const clampScale = (v) => Math.min(1.5, Math.max(0.5, Math.round((Number(v) || 1) * 20) / 20));
function loadGridScale() {
  try { const v = localStorage.getItem(GRID_SCALE_KEY); return v ? clampScale(v) : 1; } catch { return 1; }
}

const uid = () => Math.random().toString(36).slice(2, 9);

// deepActive: the focused element through open shadow roots.
import { deepActive, pathHas, clampBox, dragPointer } from '/vendor/bx-kit.js';
import { shellCss, statusCss, partCss } from './shell-css.js';
import './bx-canvas.js';
import './bx-side.js';
import { GRID, DEF_W, DEF_H, MIN_W, MIN_H, snap, LongPress, selectedText, isScreenItem, screenIdOf, sectionOf, ownerKeyOf, worstStatus, spawnTitle,
  STATUS, statusIcon, liveSquare } from './shell-kit.js';
import { overlaps, spotNear } from './grid-layout.js';
import { canvasMenuItems, tileMenuItems, offloaded, hidden } from './menus.js';
import { ago, newDraft, withDraft, withoutDraft, publish, conflictDialog } from './rev-draft.js';
import { nextZ, frontWindow, onWindowFront, activeWindow } from './zorder.js';
import { follow as followLayout, editing as layoutEditing } from './layout-sync.js';
import { framedTile } from './partition-mode.js';
import { appearanceRows, followAppearance } from './shell-appearance.js';
import { tabStrip, revealActiveTab, addScreen, hideOrgTab, screenMode, setScreenMode, layoutItems } from './shell-tabs.js'; // the screen tabs, in the top bar (D187)
import { TopReveal } from './shell-doc.js'; // Document mode's top bar (D187)
import { docRows, placeNew, setCols, rowOf, step, setHeight } from './doc-layout.js';

// Convert a legacy column-based tile ({col, height}) to a fixed-grid tile
// ({x,y,w,h}); tiles already in grid form pass through. Old columns become grid
// columns of DEF_W width, their tiles stacked top-to-bottom. Floating tiles get
// a grid home too (used when pinned back).
// clampBox (bx-kit) keeps a viewport-fixed window (float tile, spawned
// window, admin popover) reachable: no larger than the viewport minus an 8px
// margin, never positioned outside it. Geometry saved on a big monitor must
// not vanish on a small one.

function gridMigrate(tiles) {
  const nextY = {}; // col → next free y
  return (tiles ?? []).map((o) => {
    if (o.x != null && o.w != null) return o; // already grid
    const { col = 0, height, pinned, ...rest } = o;
    if (o.float) return { x: 0, y: 0, w: DEF_W, h: DEF_H, ...rest };
    const x = col * DEF_W;
    const y = nextY[col] ?? 0;
    const h = snap(Math.max(MIN_H, Math.min(height ?? DEF_H, 16 * GRID)));
    nextY[col] = y + h;
    return { x, y, w: DEF_W, h, ...rest };
  });
}

export class BxShell extends LitElement {
  static properties = {
    name: { type: String },
    _brand: { state: true }, // the workspace's title + icon (shell-brand.js, D76)
    _components: { state: true },
    _screens: { state: true }, // [{id, name, tiles: [{path, x, y, w, h, float?:{x,y,w,h,z}}]}]
    _active: { state: true },  // active screen id
    _side: { state: true },    // sidebar: {width, collapsed, folders:[{id,name,open,items}]}
    _sys: { state: true },        // status footer data (admin-only; null = hidden)
    _isAdmin: { state: true },    // shows the per-tile ⚙ mini-admin (probed via /whoami)
    _adminOrgs: { state: true },  // orgs this human administers (⚙ on their org's tiles)
    _ownedTiles: { state: true }, // tiles this human OWNS (⚙ on their own tiles, D24)
    _pendingN: { state: true },   // ⚑ badge: actionable pending approvals/requests
    _setupCard: { state: true },  // first-run hardening prompt (root token, no users)
    _dialogs: { state: true },    // shell-rendered dialogs a tile asked for
    _spawnWins: { state: true },  // pop-out windows a tile asked for
    _menu: { state: true },       // open context menu {items, x, y, anchor, sheet, title, tile} (null = closed)
    _adminPop: { state: true },   // the ⚙ admin popover {path, section, x, y, w, h} (null = closed)
    _create: { state: true },     // new-tile dialog spec (null = closed)
    _folderEdit: { state: true }, // folder name/icon dialog (null = closed)
    _settings: { state: true },     // per-user workspace settings {fontSize}
    _gridScale: { state: true },    // per-browser grid scale (D68): px per logical px, 0.5–1.5
    _settingsOpen: { state: true }, // the top bar's settings menu
    _showHidden: { state: true },   // sidebar: reveal hidden (state=hidden) tiles (D42)
    _alerts: { state: true },       // workspace health banners (/api/xbin/alerts)
    _status: { state: true },       // per-component status {path: {level,message,ts}} (/api/xbin/tile-report)
    _prs: { state: true },          // open change proposals {path: count} (/api/xbin/code/prs/summary)
    _toasts: { state: true },       // transient notifications from tiles (xbin.notify)
    _mobile: { state: true },       // narrow-screen layout (off-canvas sidebar, stacked tiles)
    _drawer: { state: true },       // mobile: sidebar drawer open
    _who: { state: true },        // whoami (id/name/role — my-account + owner sections)
    _orgScreens: { state: true }, // shared org screens (D37): [{id,org,name,edit,tiles,rev,updatedBy,updatedAt,canEdit}]
    _menuMsg: { state: true },    // settings-menu feedback line {ok, text}
    _orgDrafts: { state: true },  // org-screen drafts (D55): {id: {tiles, baseRev, dirty, name}} — edit mode == a draft exists
    _folderDrafts: { state: true }, // shared-folder drafts (D55): {scope: {folders, baseRev, dirty}}
    _sharedFolders: { state: true }, // shared folder sets from /screens: {scope: {folders,rev,updatedBy,updatedAt,canEdit}}
    _conflict: { state: true },   // stale-save dialog {kind, id, spec} (null = closed)
    _tabOrder: { state: true },   // tab order across personal + org screens (ids)
    _hiddenOrg: { state: true },  // org screens hidden from the tab bar {id: true}
    _shareOrg: { state: true },   // settings menu: org chosen for "share screen to org"
    _look: { state: true },       // the page's appearance {theme, density} (/vendor/bx-theme.js), for the settings menu
    _front: { state: true },      // the active window's key (zorder.js): 'spawn:<id>' is a spawned window of ours
  };

  static styles = [shellCss, statusCss, partCss]; // partCss: a pop-out's marker and partition chip

  constructor() {
    super();
    this.name = 'workspace';
    this._components = [];
    this._screens = [];
    this._active = '';
    this._side = { width: 224, collapsed: false, folders: [] }; // persisted with the layout
    this._sys = null;
    this._sysPrev = null; // previous traffic sample for req/s + MB/s deltas
    this._isAdmin = false;
    this._adminOrgs = new Set();
    this._dialogs = [];
    this._spawnWins = [];
    this._menu = null;
    this._adminPop = null;
    this._recent = [];             // recently opened tile paths, newest first (layout pref)
    this._press = new LongPress(); // sidebar rows + main's padding (the canvas has its own)
    this._create = null;
    this._folderEdit = null;
    this._settings = { fontSize: 13 };
    this._gridScale = loadGridScale();
    this._alerts = [];
    this._status = {};
    this._prs = {};
    this._toasts = [];
    this._mobile = false;
    this._drawer = false;
    this._settingsOpen = false;
    // Tile → shell requests (dialog / pop-out window). Composed events reach
    // window; the detail carries the VERIFIED component + a reply closure.
    this._onSpawn = (e) => this._spawn(e.detail);
    this._onSpawnClose = (e) => this._closeSpawn(e.detail.id);
    // xbin:open-deployments (the admin tile's link): a listed tile's Deployments panel, as the tile menu opens it.
    this._onOpenDeployments = (e) => { const p = e.detail?.tile; if (this._components.some((c) => c.path === p)) this._frameOpen(p, 'deployments'); };
    this._who = null;
    this._myId = null;
    this._orgScreens = [];   // shared org screens (D37)
    this._wsDefault = null;  // ws-admin-curated default screen tiles (D37)
    this._menuMsg = null;
    this._orgDrafts = {};
    this._folderDrafts = {};
    this._sharedFolders = {};
    this._conflict = null;
    this._tabOrder = [];
    this._hiddenOrg = {};
    this._shareOrg = '';
    this._seeds = [];        // {path, height} from slotted <bx-frame> children
    this._layoutLoaded = false;
    this._saveTimer = null;
    this._writer = uid();    // X-Prefs-Writer: tells this tab's own layout saves from other clients'
    this._onBlur = () => this._raiseFocusedFloat();
    this._look = appearance();
    this._front = activeWindow();
    this._top = new TopReveal(this); // Document mode: the bar slides away while reading
    // a spawned window's frame changed its build state: its live square follows
    this.addEventListener('bx-build', () => this.requestUpdate());
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._loadLayout();
    this._loadSettings();
    loadBrand().then((b) => this._setBrand(b));
    this._off = window.xbin?.events.on((e) => {
      if (e.type === 'reload' || e.type === 'grants') this._load();
      if (e.type === 'branding') loadBrand().then((b) => this._setBrand(b)); // the admin changed the title/icon
      if (e.type === 'users') { this._load(); this._probeAdmin(); this._loadShared(); } // org/ownership/screens changes
      if (e.type === 'grants' || e.type === 'users' || dismissedEvent(e)) this._loadPendingCount(); // ⚑ badge
      if (e.type === 'status') this._onStatusEvent(e); // tile health / notifications
      if (e.type === 'pr') this._loadPRs();            // change-proposal badges (⇄)
      if (e.type === 'prefs') followLayout(this, e, LAYOUT_PREF); // the app / another tab saved the layout
      if (e.type === 'prefs') followAppearance(this, e); // another tab or device changed the theme or density
    });
    this._loadStatuses();
    this._loadPRs();
    window.addEventListener('blur', this._onBlur);
    // focus moving from one tile's frame into another's never blurs this
    // window: its focusin does (the active window follows the click)
    window.addEventListener('focusin', this._onBlur);
    this._offFront = onWindowFront((key) => { this._front = key; });
    // Narrow-screen layout: switch to the mobile shell (off-canvas sidebar,
    // stacked tiles) under 820px. matchMedia so it flips live on rotate/resize.
    this._mq = window.matchMedia('(max-width: 820px)');
    this._mobile = this._mq.matches;
    this._onMq = (e) => { this._mobile = e.matches; if (!e.matches) this._drawer = false; };
    this._mq.addEventListener('change', this._onMq);
    this._probeAdmin();
    window.addEventListener('bx-spawn', this._onSpawn);
    window.addEventListener('bx-spawn-close', this._onSpawnClose);
    window.addEventListener('bx-open-deployments', this._onOpenDeployments);
    this._loadSys();
    this._sysTimer = setInterval(() => this._loadSys(), 5000);
    this._loadAlerts();
    this._alertTimer = setInterval(() => this._loadAlerts(), 20000);
    // Ctrl/Cmd+S publishes the active org-screen draft (D55).
    this._onKey = (e) => {
      if (e.key === 'Escape' && !e.defaultPrevented) {
        // Escape peels: menu → open list → dialog (all stop it) → the admin popover.
        if (this._adminPop) this._adminPop = null;
        return;
      }
      if (!(e.ctrlKey || e.metaKey) || e.key !== 's') return;
      const os = this._activeOrgScreen;
      if (os && this._orgDrafts?.[os.id]?.dirty) { e.preventDefault(); this._saveOrgDraft(os.id); }
    };
    window.addEventListener('keydown', this._onKey);
    // A shrinking browser window pulls every floating window back inside it
    // (spawned windows, the admin popover, float tiles via their render;
    // bx-frame pop-ups listen on their own).
    this._onResize = () => {
      this._fitWindows(false);
      // a browser zoom changes the pixel ratio with the screen unchanged
      if (window.devicePixelRatio !== this._dpr) { this._dpr = window.devicePixelRatio; this._zoomTip(); }
    };
    window.addEventListener('resize', this._onResize);
    // Browser zoom (ctrl/cmd +/−/0, ctrl-wheel, pinch) shrinks text along with
    // the layout; the grid scale is the knob for the layout alone — say so
    // once per browser (D68).
    this._dpr = window.devicePixelRatio;
    this._onZoomKey = (e) => { if ((e.ctrlKey || e.metaKey) && ['+', '-', '=', '0'].includes(e.key)) this._zoomTip(); };
    this._onZoomWheel = (e) => { if (e.ctrlKey) this._zoomTip(); };
    window.addEventListener('keydown', this._onZoomKey);
    window.addEventListener('wheel', this._onZoomWheel, { passive: true });
  }

  // ---- the grid scale (D68) ----
  _setGridScale(v) {
    const k = clampScale(v);
    this._gridScale = k;
    try { if (k === 1) localStorage.removeItem(GRID_SCALE_KEY); else localStorage.setItem(GRID_SCALE_KEY, String(k)); } catch { /* storage off: this session only */ }
  }
  _zoomTip() {
    if (this._gridScale !== 1 || this._zoomTipped) return;
    try { if (localStorage.getItem(ZOOM_TIP_KEY)) return; localStorage.setItem(ZOOM_TIP_KEY, '1'); } catch { /* storage off: once per load */ }
    this._zoomTipped = true;
    this._pushToast('grid scale', {
      level: 'info', action: () => { this._settingsOpen = true; },
      message: 'zooming the page? the workspace has its own grid scale in settings (top bar): it resizes the layout and keeps text sharp',
    }, 12000);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('resize', this._onResize);
    this._off?.();
    window.removeEventListener('bx-spawn', this._onSpawn);
    window.removeEventListener('bx-spawn-close', this._onSpawnClose);
    window.removeEventListener('bx-open-deployments', this._onOpenDeployments);
    clearInterval(this._sysTimer);
    clearInterval(this._alertTimer);
    window.removeEventListener('blur', this._onBlur);
    window.removeEventListener('focusin', this._onBlur);
    this._offFront?.();
    window.removeEventListener('keydown', this._onKey);
    window.removeEventListener('keydown', this._onZoomKey);
    window.removeEventListener('wheel', this._onZoomWheel);
    this._mq?.removeEventListener('change', this._onMq);
    clearTimeout(this._staleTimer);
  }

  updated() { revealActiveTab(this); }

  firstUpdated() {
    // The grid is absolute-positioned in fixed px, so window resize never
    // reflows it — no ResizeObserver on the canvas.
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

  // ---- shared screens (D37/D55): the ws default seed, org screens, shared folders ----
  // RAW fetch — the cookie principal is the signed-in user (xbin.fetch would
  // downgrade to the chrome element and see nothing). Safe to call any time:
  // edits live in personal drafts, so a refresh never clobbers anything.
  async _loadShared() {
    try {
      const r = await fetch('/api/xbin/screens');
      if (r.ok) {
        const d = await r.json();
        this._orgScreens = (d.org ?? []).map((x) => ({ ...x, tiles: gridMigrate(x.tiles ?? []) }));
        this._wsDefault = Array.isArray(d.default?.tiles) ? d.default.tiles : null;
        this._sharedFolders = d.folders ?? {};
        this._reconcileDrafts();
        // The active org screen vanished (deleted / membership lost) → first
        // tab. Only once the layout is in: before that the personal screens
        // aren't known yet, and picking the first org screen here rendered
        // it (and mounted its tiles) for a moment on every load.
        const vis = this._visibleTabs();
        if (this._layoutLoaded && vis.length && !vis.some((t) => t.id === this._active)) this._active = vis[0].id;
      }
    } catch { /* offline / restarting */ }
  }

  get _activeOrgScreen() { return (this._orgScreens ?? []).find((x) => x.id === this._active) ?? null; }

  // ---- org-screen drafts (D55): edit like a dashboard, publish explicitly ----
  // View mode is read-only for everyone. "edit layout" copies the shared
  // tiles into a personal draft; every gesture mutates the draft; only "Save
  // and update for everyone" writes the shared store, naming the revision the
  // draft was based on — a stale save comes back 409 and the human picks.
  get _canMutate() { const os = this._activeOrgScreen; return !os || !!this._orgDrafts?.[os.id]; }

  _enterEdit(id) {
    const os = (this._orgScreens ?? []).find((x) => x.id === id);
    if (!os?.canEdit || this._orgDrafts?.[id]) return;
    this._orgDrafts = withDraft(this._orgDrafts, id, newDraft({ tiles: os.tiles.map((t) => ({ ...t })), name: os.name }, os.rev ?? 1));
  }
  _dropDraft(id) {
    this._orgDrafts = withoutDraft(this._orgDrafts, id);
    this._save();
  }
  _discardDraft(id) {
    const d = this._orgDrafts?.[id];
    if (!d) return;
    if (d.dirty && !confirm(`Discard your unsaved changes to "${d.name}"?`)) return;
    this._dropDraft(id);
  }
  async _saveOrgDraft(id, force = false) {
    const d = this._orgDrafts?.[id];
    const os = (this._orgScreens ?? []).find((x) => x.id === id);
    if (!d || !os) return;
    const res = await publish('/api/xbin/screens/org', { id, org: os.org, tiles: d.tiles, rev: d.baseRev, force });
    if (res.status === 'conflict') { this._conflict = this._conflictSpec('screen', id, res.body); return; }
    if (res.status !== 'ok') { this._pushToast(os.org, { level: 'error', message: res.message }); return; }
    const body = res.body;
    this._orgScreens = this._orgScreens.map((x) => x.id === id
      ? { ...x, tiles: d.tiles, rev: body.rev, updatedBy: body.updatedBy, updatedAt: body.updatedAt } : x);
    this._dropDraft(id);
    this._pushToast(os.org, { level: 'ok', message: `saved — everyone in ${os.org} sees rev ${body.rev}` });
  }
  // Fork an org screen (any member, even read-only) into a personal screen.
  _copyOrgScreen(id) {
    const os = (this._orgScreens ?? []).find((x) => x.id === id);
    if (!os) return;
    const src = this._orgDrafts?.[id]?.tiles ?? os.tiles;
    const s = { id: uid(), name: `${os.name} (copy)`, tiles: src.map((t) => ({ ...t })), ...(os.mode ? { mode: os.mode } : {}) };
    this._screens = [...this._screens, s];
    this._active = s.id;
    this._save();
  }
  // Someone saved first: reload theirs (drop the draft), overwrite, or keep editing.
  _conflictSpec(kind, id, body) {
    const theirs = kind === 'screen' ? body.screen : body.folders;
    const mine = kind === 'screen' ? this._orgDrafts?.[id] : this._folderDrafts?.[id];
    const what = kind === 'screen' ? `"${mine?.name ?? id}"` : `the ${id === 'ws' ? 'workspace' : id} sidebar folders`;
    return { kind, id, spec: conflictDialog({ what, rev: body.rev, by: this._whoLabel(theirs?.updatedBy), at: theirs?.updatedAt, baseRev: mine?.baseRev }) };
  }
  async _onConflict({ button }) {
    const c = this._conflict;
    this._conflict = null;
    if (!c || !button) return;
    if (c.kind === 'screen') {
      if (button === 'force') { this._saveOrgDraft(c.id, true); return; }
      await this._loadShared(); // theirs = the live entry, not the 409 snapshot
      this._dropDraft(c.id);
    } else if (c.kind === 'folders') {
      if (button === 'force') { this._saveFolderDraft(c.id, true); return; }
      await this._loadShared();
      this._dropFolderDraft(c.id);
    } else if (c.kind === 'replace') {
      if (button === 'force') { this._shareToOrg(c.org, c.id, true); return; }
      await this._loadShared();
    }
  }
  // Drafts outlive refreshes. A draft whose org screen vanished (deleted,
  // membership lost) is forked into a personal screen when dirty and dropped
  // when clean — nothing is lost, no orphan UI. Folder drafts for scopes we no
  // longer see are dropped.
  _reconcileDrafts() {
    let changed = false;
    const drafts = { ...(this._orgDrafts ?? {}) };
    let screens = this._screens;
    for (const [id, d] of Object.entries(drafts)) {
      if ((this._orgScreens ?? []).some((s) => s.id === id)) continue;
      if (d.dirty) {
        screens = [...screens, { id: uid(), name: `${d.name || 'org screen'} (draft copy)`, tiles: d.tiles }];
        this._pushToast('screens', { level: 'warn', message: `org screen "${d.name || id}" is gone — your draft was copied to your screens` });
      }
      delete drafts[id]; changed = true;
    }
    const fdrafts = { ...(this._folderDrafts ?? {}) };
    for (const scope of Object.keys(fdrafts)) {
      if (!this._sharedFolders?.[scope]) { delete fdrafts[scope]; changed = true; }
    }
    if (changed) { this._orgDrafts = drafts; this._folderDrafts = fdrafts; this._screens = screens; this._save(); }
  }
  _whoLabel(id) { return !id ? 'someone' : id === this._myId ? 'you' : id; }

  // The strip under the screen tabs on a shared screen: what it is and who
  // saved it (view), or the draft's Save / Discard (edit). Part of the shell's
  // column, above the scrolling canvas — pinned, never over content.
  _orgBar() {
    const os = this._activeOrgScreen;
    if (!os) return nothing;
    const d = this._orgDrafts?.[os.id];
    if (!d) {
      return html`<div class="orgbar">
        <bx-icon class="ico" name="lock" label="shared with every member of ${os.org}" title="shared with every member of ${os.org}"></bx-icon>
        <span class="txt">shared org screen · <b>${os.org}</b>${os.updatedBy
          ? html` · last saved by ${this._whoLabel(os.updatedBy)} ${ago(os.updatedAt)} (rev ${os.rev ?? 1})` : nothing}</span>
        <span class="spacer"></span>
        <button class="act" title="fork this layout into a screen of your own" @click=${() => this._copyOrgScreen(os.id)}>copy to my screens</button>
        ${os.canEdit ? html`<button class="act go" title="open a draft — nothing changes for others until you save" @click=${() => this._enterEdit(os.id)}>edit layout</button>`
          : html`<span class="muted" title="editable by: ${os.edit === 'members' ? 'all members' : os.edit === 'write' ? 'write-level members' : 'org admins'}">read-only for you</span>`}
      </div>`;
    }
    const newer = (os.rev ?? 1) > d.baseRev;
    return html`<div class="orgbar editing">
      <bx-icon class="ico" name="pencil"></bx-icon>
      <span class="txt">editing <b>${os.name}</b> · based on rev ${d.baseRev}${d.dirty ? ' · unsaved changes' : ''}</span>
      ${newer ? html`<span class="newer"><bx-icon name="warning" label="Warning"></bx-icon> a newer version (rev ${os.rev}) was saved by ${this._whoLabel(os.updatedBy)} ${ago(os.updatedAt)} —
        <a @click=${() => { if (!d.dirty || confirm('Drop your draft and take the newer version?')) this._dropDraft(os.id); }}>reload theirs</a></span>` : nothing}
      <span class="spacer"></span>
      <button class="act" @click=${() => this._discardDraft(os.id)}>discard</button>
      <button class="act go" ?disabled=${!d.dirty} title="publish this draft as the org's screen (Ctrl/Cmd+S)"
        @click=${() => this._saveOrgDraft(os.id)}>Save and update for everyone</button>
    </div>`;
  }

  // ---- persistence ----
  async _loadLayout() {
    await this._loadShared(); // the ws default may seed the first screen
    try {
      const r = await window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`);
      if (r?.ok) {
        const l = await r.json();
        if (Array.isArray(l?.tabOrder)) this._tabOrder = l.tabOrder.filter((x) => typeof x === 'string');
        if (Array.isArray(l?.recent)) this._recent = l.recent.filter((x) => typeof x === 'string').slice(0, 20);
        if (l?.hiddenOrg && typeof l.hiddenOrg === 'object') this._hiddenOrg = l.hiddenOrg;
        // Dirty drafts come back after a reload (D55); clean ones were never saved.
        const dr = l?.drafts ?? {};
        this._orgDrafts = Object.fromEntries(Object.entries(dr.org ?? {}).map(([id, d]) =>
          [id, { tiles: gridMigrate(d.tiles ?? []), baseRev: d.baseRev ?? 1, dirty: true, name: d.name }]));
        this._folderDrafts = Object.fromEntries(Object.entries(dr.folders ?? {}).map(([scope, d]) =>
          [scope, { folders: Array.isArray(d.folders) ? d.folders : [], baseRev: d.baseRev ?? 0, dirty: true }]));
        if (Array.isArray(l?.screens) && l.screens.length) {
          // Migrate any old column-based layout to the fixed grid on load.
          this._screens = l.screens.map((s) => ({ ...s, tiles: gridMigrate(s.tiles ?? []) }));
          const known = l.screens.some((s) => s.id === l.active) || (this._orgScreens ?? []).some((s) => s.id === l.active);
          this._active = known ? l.active : l.screens[0].id;
          // Never leave a parked / hidden screen active (it isn't in the tab bar).
          const vis = this._visibleTabs();
          if (vis.length && !vis.some((t) => t.id === this._active)) this._active = vis[0].id;
        }
        if (l?.side && typeof l.side === 'object') {
          this._side = { width: 224, collapsed: false, folders: [], ...l.side };
        }
      }
    } catch { /* offline / restarting — fall through to seed */ }
    this._layoutLoaded = true;
    this._reconcileDrafts();
    this._ensureScreen();
  }

  // Seed a default screen from the slotted <bx-frame> pins, but only once the
  // saved layout has been consulted and found empty. Lay them out two per row.
  _ensureScreen() {
    if (!this._layoutLoaded || this._screens.length) return;
    // Seed only tiles this user can actually read (the components list is
    // server-filtered) — a non-admin shouldn't land on tiles/admin's 403.
    const readable = new Set(this._components.map((c) => c.path));
    // The ws-admin-curated default screen (D37) wins over the root/index.html
    // <bx-frame> pins; both filter to what this user may read.
    const def = (this._wsDefault ?? []).filter((t) => t.path && (readable.size === 0 || readable.has(t.path)));
    let tiles;
    if (def.length) {
      tiles = gridMigrate(def.map((t) => ({ ...t })));
    } else {
      const seeds = this._seeds.filter((s) => readable.size === 0 || readable.has(s.path));
      const perRow = 2;
      tiles = seeds.map((s, i) => ({
        path: s.path,
        x: (i % perRow) * DEF_W, y: Math.floor(i / perRow) * DEF_H,
        w: DEF_W, h: DEF_H,
      }));
    }
    this._screens = [{ id: uid(), name: 'Home', tiles }];
    this._active = this._screens[0].id;
    this._save();
  }

  _save() {
    clearTimeout(this._saveTimer);
    this._saveTimer = setTimeout(() => this._saveNow(), 400);
  }
  // The debounced write itself. Returns the request so a caller that must
  // know the layout reached the server (the harness before it closes a
  // browser context) can await it.
  _saveNow() {
    clearTimeout(this._saveTimer);
    this._saveTimer = null;
    // Only DIRTY drafts persist: a clean edit session isn't worth resurrecting.
    const dirty = (m, pick) => Object.fromEntries(Object.entries(m ?? {}).filter(([, d]) => d.dirty).map(([k, d]) => [k, pick(d)]));
    const drafts = {
      org: dirty(this._orgDrafts, (d) => ({ tiles: d.tiles, baseRev: d.baseRev, name: d.name })),
      folders: dirty(this._folderDrafts, (d) => ({ folders: d.folders, baseRev: d.baseRev })),
    };
    return (window.xbin?.fetch(`/api/xbin/prefs/${LAYOUT_PREF}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-Prefs-Writer': this._writer },
      body: JSON.stringify({ screens: this._screens, active: this._active, side: this._side,
        tabOrder: this._tabOrder, hiddenOrg: this._hiddenOrg, recent: this._recent, drafts }),
    }) ?? Promise.resolve()).catch(() => { /* best-effort; retried on next change */ });
  }

  // ---- active screen helpers ----
  // An org screen with a draft shows the draft's tiles (D55).
  get _screen() {
    const p = this._screens.find((s) => s.id === this._active);
    if (p) return p;
    const os = this._activeOrgScreen;
    if (!os) return undefined;
    const d = this._orgDrafts?.[os.id];
    return d ? { ...os, tiles: d.tiles } : os;
  }
  get _tiles() { return this._screen?.tiles ?? []; }

  // Replace the active screen's tiles via fn(copy) → new array, then persist
  // (debounced, so rapid changes like drag/resize coalesce). An ORG screen
  // (D37/D55) mutates its personal DRAFT — never the shared store, and never
  // in view mode; publishing is the explicit save.
  _mutateTiles(fn) {
    const os = this._activeOrgScreen;
    if (os) {
      const d = this._orgDrafts?.[os.id];
      if (!d) return; // view mode: a shared screen never changes by accident
      this._orgDrafts = { ...this._orgDrafts, [os.id]: { ...d, tiles: fn(d.tiles.map((t) => ({ ...t }))), dirty: true } };
      this._save();
      return;
    }
    if (!this._screen) return;
    const tiles = fn(this._tiles.map((t) => ({ ...t })));
    this._screens = this._screens.map((s) => s.id === this._active ? { ...s, tiles } : s);
    this._save();
  }

  async _load() {
    try {
      const r = await (window.xbin?.fetch ?? fetch)('/api/xbin/components');
      if (r.ok) {
        this._components = await r.json();
        this._pruneOffloaded(); // an offloaded tile disappears from open screens too
      }
    } catch { /* xbind restarting; next event retries */ }
  }

  // Offloaded tiles are archived — not openable; hidden from the sidebar and
  // closed if open. (offloaded / offloaded-full.)
  _offloaded(c) { return offloaded(c); }

  // Hidden tiles (D42): disabled + filtered out of the sidebar unless the
  // show-hidden toggle is on. Screens are left alone — a placed tile stays
  // placed and renders its disabled state.
  _hidden(c) { return hidden(c); }
  get _hiddenCount() {
    return this._components.filter((c) => c.path !== 'root' && !c.template && this._hidden(c)).length;
  }

  _pruneOffloaded() {
    const off = new Set(this._components.filter((c) => this._offloaded(c)).map((c) => c.path));
    if (!off.size) return;
    let changed = false;
    const screens = this._screens.map((s) => {
      const tiles = s.tiles.filter((t) => !off.has(t.path));
      if (tiles.length !== s.tiles.length) changed = true;
      return { ...s, tiles };
    });
    if (changed) { this._screens = screens; this._save(); }
  }

  _toggleOwnerSec(key) {
    const cur = { ...(this._side.ownerCollapsed ?? {}) };
    cur[key] = !cur[key];
    this._saveSide({ ownerCollapsed: cur });
  }

  // ---- folder contexts (D55): which folder list a tree renders and how it mutates ----
  // 'top'      — the user's own top-level folders (_side.folders): any tile,
  //              parked tabs, org-screen refs; always editable, saved at once.
  // 'ws' / 'org:<id>' — a shared, curated set under that owner section
  //              (/screens.folders): editable only through a draft, by its
  //              curators (ws-admins / the org's admins); read-only otherwise.
  _folderCtx(key) {
    if (key === 'top') {
      return { key, shared: false, folders: this._side.folders ?? [], canEdit: true, curator: true,
        mutate: (fn) => this._saveSide({ folders: fn(this._side.folders ?? []) }) };
    }
    const set = this._sharedFolders?.[key];
    const draft = this._folderDrafts?.[key];
    return { key, shared: true, set, draft,
      folders: draft?.folders ?? set?.folders ?? [],
      canEdit: !!draft, curator: !!set?.canEdit,
      mutate: (fn) => {
        const d = this._folderDrafts?.[key];
        if (!d) return;
        this._folderDrafts = { ...this._folderDrafts, [key]: { ...d, folders: fn(d.folders), dirty: true } };
        this._save();
      } };
  }
  // ---- shared folder drafts (D55): curate, then "Save and update for everyone" ----
  _enterFolderEdit(scope) {
    const set = this._sharedFolders?.[scope];
    if (!set?.canEdit || this._folderDrafts?.[scope]) return;
    this._folderDrafts = withDraft(this._folderDrafts, scope,
      newDraft({ folders: (set.folders ?? []).map((f) => ({ ...f, items: [...(f.items ?? [])] })) }, set.rev ?? 0));
    const sec = sectionOf(scope);
    if (this._side.ownerCollapsed?.[sec]) this._toggleOwnerSec(sec);
  }
  _dropFolderDraft(scope) {
    this._folderDrafts = withoutDraft(this._folderDrafts, scope);
    this._save();
  }
  _discardFolderDraft(scope) {
    const d = this._folderDrafts?.[scope];
    if (!d) return;
    if (d.dirty && !confirm('Discard your unsaved changes to the shared folders?')) return;
    this._dropFolderDraft(scope);
  }
  async _saveFolderDraft(scope, force = false) {
    const d = this._folderDrafts?.[scope];
    if (!d) return;
    const who = scope === 'ws' ? 'the workspace' : scope.slice(4);
    const res = await publish('/api/xbin/screens/folders', { scope, folders: d.folders, rev: d.baseRev, force });
    if (res.status === 'conflict') { this._conflict = this._conflictSpec('folders', scope, res.body); return; }
    if (res.status !== 'ok') { this._pushToast('sidebar', { level: 'error', message: res.message }); return; }
    const body = res.body;
    this._sharedFolders = { ...this._sharedFolders, [scope]: { ...(this._sharedFolders?.[scope] ?? {}),
      folders: d.folders, rev: body.rev, updatedBy: body.updatedBy, updatedAt: body.updatedAt, canEdit: true } };
    this._dropFolderDraft(scope);
    this._pushToast('sidebar', { level: 'ok', message: `shared folders saved — everyone in ${who} sees rev ${body.rev}` });
  }

  // ---- tile-spawned dialogs & pop-out windows (docs/elements.md) ----
  _spawn(d) {
    if (d.kind === 'dialog') {
      // One dialog per tile at a time: a rogue tile can't carpet-bomb modals,
      // and a stack of same-tile modals can't obscure the attribution. The
      // dropped request resolves as a dismiss so the caller's await unblocks.
      if (this._dialogs.some((x) => x.from === d.from)) { d.reply({ button: null, values: {} }); return; }
      this._dialogs = [...this._dialogs, { id: d.id, from: d.from, spec: d.spec, reply: d.reply }];
      return;
    }
    // Cap pop-out windows per tile too (its `closed` resolves immediately if
    // over the cap).
    if (this._spawnWins.filter((x) => x.from === d.from).length >= 6) { d.reply(); return; }
    // window: frame a sub-path of the caller (default) or an explicit component
    // path. Both go through <bx-frame>, so RBAC (frame-token/CanUseTile) still
    // applies; strip any traversal from a caller-supplied sub-path.
    const safe = (p) => String(p || '').replace(/\.\.(\/|$)/g, '').replace(/^\/+|\/+$/g, '');
    const src = d.spec.src ? safe(d.spec.src) : [d.from, safe(d.spec.path)].filter(Boolean).join('/');
    const w = Math.max(200, Math.min(d.spec.width || 480, window.innerWidth - 40));
    const h = Math.max(140, Math.min(d.spec.height || 360, window.innerHeight - 40));
    const win = {
      id: d.id, from: d.from, src, reply: d.reply,
      title: d.spec.title || src,
      ...clampBox({ // a caller-supplied x/y can't park the window off-screen
        x: d.spec.x ?? Math.round((window.innerWidth - w) / 2),
        y: d.spec.y ?? Math.round((window.innerHeight - h) / 2.4),
        w, h,
      }, { minW: 200, minH: 140 }),
      z: nextZ(),
    };
    this._spawnWins = [...this._spawnWins, win];
    frontWindow(`spawn:${d.id}`); // a new window is the active one
  }

  _resolveDialog(id, detail) {
    const d = this._dialogs.find((x) => x.id === id);
    if (d) d.reply(detail);
    this._dialogs = this._dialogs.filter((x) => x.id !== id);
  }

  // Close a spawned window (by the tile, its ✕, or the tile unmounting): tell
  // the caller its window closed (resolves handle.closed) and drop it.
  _closeSpawn(id) {
    const w = this._spawnWins.find((x) => x.id === id);
    if (!w) return;
    w.reply();
    this._spawnWins = this._spawnWins.filter((x) => x.id !== id);
    if (this._front === `spawn:${id}`) frontWindow(''); // the active window closed: none is now
  }

  _spawnFront(id) {
    const w = this._spawnWins.find((x) => x.id === id);
    if (w) { w.z = nextZ(); frontWindow(`spawn:${id}`); this.requestUpdate(); }
  }

  // _fitWindows brings every floating window back inside the viewport:
  // spawned windows and the admin popover (state), every tile's pop-up
  // (bx-frame.fitToViewport), and float tiles — those render clamped
  // already; with `persist` (the menu action) their saved geometry is fixed
  // up too, so the layout stops carrying an off-screen spot. Runs on window
  // resize (persist=false: a resize must not rewrite a shared layout) and
  // from the canvas menu's "Bring windows on-screen".
  _fitWindows(persist) {
    let moved = false;
    for (const w of this._spawnWins) {
      const c = clampBox(w, { minW: 200, minH: 140 });
      if (c.x !== w.x || c.y !== w.y || c.w !== w.w || c.h !== w.h) { Object.assign(w, c); moved = true; }
    }
    if (this._adminPop) {
      const c = clampBox(this._adminPop, { minW: 300, minH: 120 });
      if (c.x !== this._adminPop.x || c.y !== this._adminPop.y || c.w !== this._adminPop.w || c.h !== this._adminPop.h) this._adminPop = c;
    }
    for (const fr of [...this.renderRoot.querySelectorAll('bx-frame'), ...(this._canvas?.frames() ?? [])]) fr.fitToViewport?.();
    if (persist && this._canMutate && !this._mobile) {
      for (const o of this._tiles) {
        if (!o.float) continue;
        const c = clampBox(o.float, { minW: MIN_W, minH: MIN_H });
        if (c.x !== o.float.x || c.y !== o.float.y || c.w !== o.float.w || c.h !== o.float.h) this._setFloat(o.path, c);
      }
    }
    if (moved || !persist) this.requestUpdate(); // floats re-render clamped to the new viewport
  }

  // The per-tile admin popover (D56): a wide, resizable panel next to the
  // card hosting <bx-tile-admin> — context-menu manners: click outside or
  // Escape closes it, nothing to drag, one at a time; a sheet on phones.
  // Reopening for the same tile just jumps to `section`.
  _openAdminWin(path, section = null) {
    if (this._adminPop?.path === path) {
      if (section) { this._adminPop = { ...this._adminPop, section }; this.renderRoot.querySelector('bx-tile-admin.apop')?.show(section); }
      return;
    }
    const W = window.innerWidth, H = window.innerHeight;
    const w = Math.min(560, W - 24), h = Math.min(Math.round(H * 0.7), H - 24);
    const clamp = (v, lo, hi) => Math.max(lo, Math.min(v, hi));
    const r = this._canvas?.rectOf(path);
    const x = r ? clamp(r.right - w, 8, W - w - 8) : Math.round((W - w) / 2);
    const y = r ? clamp(r.top + 36, 8, H - h - 8) : Math.round((H - h) / 2.4);
    this._adminPop = { path, section, x, y, w, h };
  }

  _spawnDragStart(e, id) {
    if (e.button !== 0 || e.target.closest('button')) return;
    e.preventDefault();
    this._spawnFront(id);
    const w = this._spawnWins.find((x) => x.id === id);
    if (!w) return;
    const ox = e.clientX - w.x, oy = e.clientY - w.y;
    dragPointer({
      onMove: (ev) => {
        w.x = Math.max(-w.w + 60, Math.min(ev.clientX - ox, window.innerWidth - 40));
        w.y = Math.max(0, Math.min(ev.clientY - oy, window.innerHeight - 24));
        this.requestUpdate();
      },
    });
  }

  // A tile-spawned window: window chrome (product-ui 3) — the live square
  // of what it frames, its title, the tile that opened it, close.
  _spawnTemplate(w) {
    const fr = this.renderRoot?.querySelector(`.spawn[data-spawn="${CSS.escape(w.id)}"] bx-frame`);
    return html`
      <div class="spawn ${this._front === `spawn:${w.id}` ? 'active' : ''}" data-spawn=${w.id}
           style="left:${w.x}px; top:${w.y}px; width:${w.w}px; height:${w.h}px; z-index:${w.z}"
           @pointerdown=${() => this._spawnFront(w.id)}>
        <div class="shead" @pointerdown=${(e) => this._spawnDragStart(e, w.id)}>
          ${liveSquare(fr?.buildState || 'live', framedTile(w.src, this._components).row?.runtime)}
          ${spawnTitle(w.title, w.src, this._components, this._who)}
          <span class="sfrom">${w.from}</span>
          <button class="wc close" title="close" aria-label="close ${w.title}" @click=${() => this._closeSpawn(w.id)}><bx-icon name="xmark"></bx-icon></button>
        </div>
        <div class="sbody">
          <bx-frame src=${w.src} height="100%" no-edit style="position:absolute; inset:0"></bx-frame>
        </div>
      </div>`;
  }

  _adminPopTemplate() {
    const a = this._adminPop;
    if (!a) return nothing;
    return html`
      <div class="admin-pop-backdrop" @pointerdown=${() => { this._adminPop = null; }}
           @contextmenu=${(e) => { e.preventDefault(); this._adminPop = null; }}></div>
      <div class="admin-pop" style="left:${a.x}px; top:${a.y}px; width:${a.w}px; max-height:${a.h}px">
        <div class="ahead">
          <span class="t" title=${`tile admin: ${a.path}`}><bx-icon name="settings"></bx-icon><b>${a.path.slice(a.path.lastIndexOf('/') + 1)}</b><span class="path">${a.path}</span></span>
          ${this._mobile ? html`<button class="wc close" title="close" aria-label="close" @click=${() => { this._adminPop = null; }}><bx-icon name="xmark"></bx-icon></button>` : nothing}
        </div>
        <bx-tile-admin class="apop" .path=${a.path} .section=${a.section} no-title></bx-tile-admin>
      </div>`;
  }

  // ---- background context menu ----
  // One handler for every right-click the shell owns (D56): a card head or a
  // sidebar row opens the TILE menu; the empty canvas opens the CANVAS menu;
  // inputs, links and the PR badge keep the native menu. Right-clicks inside
  // a tile's iframe never reach here at all.
  _onContextMenu(e) {
    if (this._mobile && this._menu) { e.preventDefault(); return; } // Android fires one after a long-press
    // The native menu stays for inputs, links, editable text and the PR badge
    // (paste lives there) — through nested shadow roots too (pathHas) — and
    // for any selected shell text (copy lives there).
    if (pathHas(e, 'input, textarea, select, a, [contenteditable]:not([contenteditable="false"]), .prb, bx-menu, bx-dialog')) return;
    // Inside a frame's pop-up (terminal, code, logs, proposals) the native menu stays.
    if (pathHas(e, '.pop, bx-terminal, bx-code, bx-logs, bx-prs')) return;
    if (this._selectedText()) return;
    const card = e.target.closest('.card');
    const row = e.target.closest('.item[data-path]');
    if (card || row) { this._openTileMenu(e, (card ?? row).dataset.path); return; }
    if (pathHas(e, 'button, bx-frame, aside, bx-side, .spawn')) return;
    this._openCanvasMenu(e);
  }

  _selectedText() { return selectedText(this.renderRoot); }
  _pressStart(e, fire) { this._press.start(e, fire, this._mobile); }
  _pressMove(e) { this._press.move(e); }
  _pressCancel() { this._press.cancel(); }

  // Where a tile opened from this menu goes (D80): the click, in the canvas's
  // logical px; null on phones (cards stack) and without a point.
  _menuPoint(e) { return this._mobile || e?.clientX == null ? null : this._canvas?.gridPoint(e.clientX, e.clientY) ?? null; }
  _openCanvasMenu(e) {
    e?.preventDefault?.();
    this._menu = { items: this._canvasMenuItems(this._menuPoint(e)), x: e?.clientX ?? 0, y: e?.clientY ?? 0, anchor: null,
      sheet: this._mobile, title: this._screen?.name ?? '' };
  }
  _openTileMenu(e, path, anchorEl = null, opts = {}) {
    e?.preventDefault?.(); e?.stopPropagation?.();
    if (!this._components.some((c) => c.path === path)) return;
    // A touch long-press inside a tile may be followed by the platform's own
    // contextmenu ~50 ms later — don't reopen the same menu.
    if (this._menu?.tile === path && Date.now() - (this._menuAt ?? 0) < 700) return;
    this._menuAt = Date.now();
    const items = this._tileMenuItems(path, this._menuPoint(e));
    // Selected text inside the tile rode along with the right-click: lead
    // with Copy — the sandboxed frame has no clipboard of its own, the shell
    // writes it.
    if (opts.selection) {
      const one = opts.selection.replace(/\s+/g, ' ').trim();
      items.unshift(
        { icon: 'copy', label: 'Copy', hint: one.length > 40 ? one.slice(0, 39) + '…' : one,
          title: one.length > 200 ? one.slice(0, 200) + '…' : one, action: () => this._copyText(opts.selection, path) },
        { kind: 'sep' });
    }
    this._menu = { items, x: e?.clientX ?? 0, y: e?.clientY ?? 0,
      anchor: typeof anchorEl?.getBoundingClientRect === 'function' ? anchorEl.getBoundingClientRect() : anchorEl ?? null,
      sheet: this._mobile, title: path, tile: path };
  }

  // Runs synchronously from the menu's click (bx-menu closes, refocuses the
  // opener, THEN calls the action — still inside the user activation).
  // navigator.clipboard exists only in a secure context (https / localhost);
  // an http LAN deployment falls back to execCommand('copy') on a scratch
  // textarea. Either way the person sees a "copied" toast.
  _copyText(text, path) {
    const done = () => this._pushToast(path, { level: 'ok', message: 'copied' }, 2500);
    const legacy = () => {
      const prev = deepActive();
      const ta = Object.assign(document.createElement('textarea'), { value: text, readOnly: true });
      ta.setAttribute('aria-hidden', 'true');
      ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none';
      document.body.appendChild(ta);
      ta.focus({ preventScroll: true }); ta.select(); ta.setSelectionRange(0, text.length);
      let ok = false;
      try { ok = document.execCommand('copy'); } catch { /* no command */ }
      ta.remove();
      if (prev?.isConnected) { try { prev.focus({ preventScroll: true }); } catch { /* fine */ } }
      if (ok) done(); else this._pushToast(path, { level: 'warn', message: 'copy failed — select the text and press Ctrl+C' }, 4000);
    };
    if (navigator.clipboard?.writeText) navigator.clipboard.writeText(text).then(done, legacy);
    else legacy();
  }

  // ---- context menus: the item lists are pure builders (menus.js) ----
  // over this view of the shell's state and these actions, so the branches
  // (org-screen draft lines, open-tile listing, the admin block) are
  // unit-tested by `make check` without a browser.
  _menuState() {
    const os = this._activeOrgScreen;
    return {
      orgScreen: os, draft: os ? (this._orgDrafts?.[os.id] ?? null) : null, owners: this._ownerOptions(),
      ownerHint: this._who?.tileCreation === 'org-only' ? 'org-only policy — ask an org admin' : 'personal tiles are off for your account — ask an admin',
      components: this._components, tiles: this._tiles, recent: this._recent ?? [], showHidden: this._showHidden,
      canMutate: this._canMutate, prs: this._prs, canAdminTile: (p) => this._canAdminTile(p),
      docMode: screenMode(this._screen) === 'doc', layoutItems: layoutItems(this, this._screen, this._activeOrgScreen ? 'org' : 'personal'),
    };
  }
  _menuActions(at = null) {
    return {
      enterEdit: (id) => this._enterEdit(id), saveOrgDraft: (id) => this._saveOrgDraft(id),
      discardDraft: (id) => this._discardDraft(id), copyOrgScreen: (id) => this._copyOrgScreen(id),
      newTileDialog: (n, m, o, opts) => this._newTileDialog(n, m, o, { ...opts, at }), addScreen: () => addScreen(this),
      fitWindows: (persist) => this._fitWindows(persist), openTile: (p) => this._openFromMenu(p, at),
      toggle: (p) => this._toggle(p), togglePin: (p) => this._canvas?.togglePin(p), frameOpen: (p, l) => this._frameOpen(p, l, at),
      openFullPage: (p) => window.open(`/c/${p}/`, '_blank'), lifecycle: (p, st) => this._lifecycle(p, st),
      openAdminWin: (p, sec) => this._openAdminWin(p, sec), confirm: (m) => confirm(m),
      docCols: (p, n) => this._mutateTiles((t) => setCols(t, rowOf(t, p), n)), docStep: (p, d) => this._mutateTiles((t) => step(t, p, d)),
      docFit: (p) => this._mutateTiles((t) => setHeight(t, p, 0)),
    };
  }
  _canvasMenuItems(at) { return canvasMenuItems(this._menuState(), this._menuActions(at)); }
  _tileMenuItems(path, at) { return tileMenuItems(path, this._menuState(), this._menuActions(at)); }
  _openFromMenu(path, at = null) { if (!this._isOpen(path)) this._toggle(path, at); }
  _noteRecent(path) { this._recent = [path, ...(this._recent ?? []).filter((p) => p !== path)].slice(0, 20); }

  async _lifecycle(path, state) {
    try {
      const r = await fetch('/api/xbin/lifecycle', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: path, state }) });
      if (!r.ok) { const d = await r.json().catch(() => ({})); this._pushToast(path, { level: 'error', message: d.error ?? `failed (${r.status})` }); }
    } catch { this._pushToast(path, { level: 'error', message: 'offline — try again' }); }
  }
  // The cards live in <bx-canvas>; a pop-up (terminal / logs / source /
  // proposals) on a tile that isn't on the screen opens it first — the
  // canvas runs the call once the card exists.
  get _canvas() { return this.renderRoot.querySelector('bx-canvas'); }
  _frameOf(path) { return this._canvas?.frameFor(path) ?? null; }
  _frameOpen(path, layout, at = null) {
    if (!this._canvas?.frameOpen(path, layout)) this._openFromMenu(path, at);
  }

  // New-tile dialog: names a static tile under apps/, creates it, opens it on
  // the current screen. Owner picker (D24/D39): me / orgs where the caller
  // holds Create or admin / workspace (ws-admin only, their default) — same
  // semantics as the manager tile's picker. Re-opens with the server's refusal
  // in the dialog's alert box on failure, preserving the picked owner.
  _ownerOptions() {
    const opts = [];
    if (this._myId) opts.push({ value: 'user:' + this._myId, label: '— me (personal) —' });
    for (const o of (this._who?.orgs ?? [])) {
      if (o.create || o.admin) opts.push({ value: 'org:' + o.id, label: 'org: ' + (o.name || o.id) });
    }
    if (this._isAdmin) opts.push({ value: '', label: '— workspace —' });
    // Org-only (D52) or the account's personal tiles off (D88, folded into
    // whoami's personalTiles): personal ownership is refused server-side —
    // don't offer it (the manager tile applies the same rule).
    const personal = this._who?.personalTiles ?? this._who?.tileCreation !== 'org-only';
    if (!this._isAdmin && !personal) return opts.filter((o) => !o.value.startsWith('user:'));
    return opts;
  }
  // fixed: the owner was chosen up front (the context menu's per-owner
  // entries) — no owner select, the choice is stated in the message.
  _newTileDialog(name = '', error = '', owner = null, { fixed = false, at = null } = {}) {
    const opts = this._ownerOptions();
    const def = owner ?? (this._isAdmin ? '' : (this._myId ? 'user:' + this._myId : ''));
    const fields = [{ name: 'name', label: 'Tile name', value: name, placeholder: 'My Tile' }];
    if (opts.length > 1 && !fixed) fields.push({ name: 'owner', label: 'Owner', type: 'select', value: def, options: opts });
    const ownerLabel = fixed ? (opts.find((o) => o.value === (owner ?? ''))?.label ?? owner ?? 'workspace').replace(/^— | —$/g, '') : '';
    this._create = {
      title: 'Create a new tile',
      message: (fixed ? `Owner: ${ownerLabel}. ` : '') + 'Creates a static tile under apps/ and opens it here.'
        + (opts.length > 1 && !fixed ? ' Personal tiles: capability requests (net, containers, ports) need a workspace admin. Org-owned: the org’s admins can approve within their allowance.' : ''),
      error,
      fields,
      buttons: [{ label: 'Cancel', value: null }, { label: 'Create', value: 'create', primary: true }],
      owner: fixed ? (owner ?? '') : undefined, fixed, at,
    };
  }
  async _onNewTile({ button, values }) {
    const spec = this._create;
    this._create = null;
    if (button !== 'create') return;
    const name = (values.name || '').trim();
    const owner = values.owner ?? spec?.owner ?? null;
    const fixed = !!spec?.fixed, at = spec?.at ?? null;
    const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
    if (!slug) { this._newTileDialog(name, 'Enter a name with letters or digits.', owner, { fixed, at }); return; }
    const path = 'apps/' + slug;
    try {
      // Chrome runs as the owner cookie — raw fetch (xbin.fetch would attach a
      // frame token and downgrade). Needs owner/xbin:writer, which the owner is.
      const body = { path, title: name };
      if (owner) body.owner = owner; // "" (workspace) = the admin default, omit
      const r = await fetch('/api/xbin/create', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error ?? r.status);
      await this._load();                    // pick up the new component
      if (!this._isOpen(path)) this._toggle(path, at); // place it on this screen
    } catch (e) {
      this._newTileDialog(name, String(e.message ?? e), owner, { fixed, at });
    }
  }

  _isOpen(path) { return this._tiles.some((o) => o.path === path); }

  // ---- sidebar: folders (view-only grouping), collapse, resize ----
  _saveSide(patch) { this._side = { ...this._side, ...patch }; this._save(); }

  // ---- per-user workspace settings (font size, …) ----
  async _loadSettings() {
    try {
      const r = await window.xbin?.fetch(`/api/xbin/prefs/${SETTINGS_PREF}`);
      if (r?.ok) {
        const s = await r.json();
        if (s && typeof s === 'object') this._settings = { fontSize: 13, ...s };
      }
    } catch { /* defaults */ }
    this._applySettings();
  }

  _saveSettings(patch) {
    this._settings = { ...this._settings, ...patch };
    this._applySettings();
    try {
      window.xbin?.fetch(`/api/xbin/prefs/${SETTINGS_PREF}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(this._settings),
      });
    } catch { /* transient */ }
  }

  // The whole workspace scales via zoom (13px = 100%). Height/width are
  // compensated so the zoomed shell still fits the viewport exactly.
  //
  // Terminals are the exception: xterm measures its cell size on a canvas
  // (which ignores an ancestor's CSS zoom) but reads pointer coords that DO
  // honor it, so any ambient zoom drifts selection/right-click by that factor,
  // worse the further from the terminal's top-left. bx-terminal detects the
  // ambient zoom itself and counters it (re-scaling through its own font size),
  // so it needs no cooperation here — this event just lets it react instantly
  // instead of waiting for its ResizeObserver to notice the reflow.
  _applySettings() {
    const fs = Math.max(9, Math.min(20, this._settings?.fontSize || 13));
    const z = fs / 13;
    if (Math.abs(z - 1) < 0.01) {
      this.style.zoom = ''; this.style.height = ''; this.style.width = '';
    } else {
      this.style.zoom = String(z);
      this.style.height = `calc(100vh / ${z})`;
      this.style.width = `calc(100vw / ${z})`;
    }
    window.dispatchEvent(new CustomEvent('bx-ambient-zoom', { detail: { zoom: z } }));
  }

  // Every folder operation takes a context (see _folderCtx): the user's own
  // top-level folders, or a shared set being curated through a draft.
  _addFolder(ctx = this._folderCtx('top')) {
    if (!ctx.canEdit) return;
    const name = prompt('Folder name');
    if (!name?.trim()) return;
    const f = { id: uid(), name: name.trim(), items: [] };
    if (!ctx.shared) f.open = true;
    ctx.mutate((fs) => [...fs, f]);
  }
  _folderDialog(f, ctx) {
    this._folderEdit = { id: f.id, ctxKey: ctx.key, spec: {
      title: 'Folder', message: 'Name and an optional emoji icon.',
      fields: [
        { name: 'name', label: 'Name', value: f.name },
        { name: 'icon', label: 'Icon (emoji)', value: f.icon || '', placeholder: 'none: the folder glyph' },
      ],
      buttons: [{ label: 'Cancel', value: null }, { label: 'Save', value: 'save', primary: true }],
    } };
  }
  _onFolderEdit({ button, values }) {
    const edit = this._folderEdit;
    this._folderEdit = null;
    if (!edit || button !== 'save') return;
    const name = (values.name || '').trim();
    const icon = (values.icon || '').trim().slice(0, 4); // 1–2 emoji
    this._folderCtx(edit.ctxKey ?? 'top').mutate((fs) => fs.map((f) =>
      f.id === edit.id ? { ...f, name: name || f.name, icon } : f));
  }

  // Move a component into folderId, positioned before beforePath (or at the end).
  _moveInto(folderId, path, beforePath, ctx) {
    ctx.mutate((fs) => fs.map((f) => {
      let items = f.items.filter((p) => p !== path);
      if (f.id === folderId) {
        const i = beforePath ? items.indexOf(beforePath) : -1;
        const at = i < 0 ? items.length : i;
        items = [...items.slice(0, at), path, ...items.slice(at)];
      }
      return { ...f, items };
    }));
  }

  _deleteFolder(f, ctx) {
    if (!ctx.canEdit) return;
    // Components return to their section root; child folders re-parent up so
    // they aren't hidden; any screen parked only here re-opens as a tab.
    const parent = f.parent ?? null;
    ctx.mutate((fs) => fs.filter((x) => x.id !== f.id)
      .map((x) => (x.parent ?? null) === f.id ? { ...x, parent } : x));
    if (ctx.shared) return;
    const refs = f.items.filter((it) => isScreenItem(it)).map((it) => screenIdOf(it));
    if (refs.length) {
      const stillTracked = (id) => this._isTracked(id);
      this._screens = this._screens.map((s) =>
        (refs.includes(s.id) && s.parked && !stillTracked(s.id)) ? { ...s, parked: false } : s);
      this._save();
    }
  }
  _toggleFolder(f, ctx, open = !!f.open) {
    if (ctx.shared) { // open/closed is personal even for a shared folder
      this._saveSide({ sharedOpen: { ...(this._side.sharedOpen ?? {}), [f.id]: !open } });
      return;
    }
    ctx.mutate((fs) => fs.map((x) => x.id === f.id ? { ...x, open: !x.open } : x));
  }
  // File path into folder id ('' = unfile). A component lives in one folder
  // max within a context.
  _fileInto(folderId, path, ctx = this._folderCtx('top')) {
    if (!ctx.canEdit) return;
    ctx.mutate((fs) => fs.map((f) => {
      const items = f.items.filter((p) => p !== path);
      if (f.id === folderId) items.push(path);
      return { ...f, items };
    }));
  }
  _dropOnFolder(e, f, ctx) {
    e.preventDefault(); e.stopPropagation();
    const note = (message) => this._pushToast('sidebar', { level: 'info', message });
    const fid = e.dataTransfer.getData('application/bx-folder');
    if (fid) { // folder → nest under this one (never across contexts)
      const from = e.dataTransfer.getData('application/bx-folder-ctx') || 'top';
      if (from !== ctx.key || !ctx.canEdit) return;
      this._nestFolder(fid, f.id, ctx);
      return;
    }
    const sid = e.dataTransfer.getData('application/bx-screen');
    if (sid) { // tab → park in the tree (an org tab files as a reference)
      if (ctx.shared) { note('tabs park only in your own folders'); return; }
      const org = e.dataTransfer.types.includes('application/bx-orgscreen');
      this._fileInto(f.id, (org ? '#orgscreen:' : '#screen:') + sid, ctx);
      return;
    }
    const path = e.dataTransfer.getData('application/bx-comp') || e.dataTransfer.getData('text/plain');
    if (!path) return;
    if (!ctx.canEdit) {
      note(ctx.curator ? 'shared folders — click the pencil on the section to curate them' : 'this folder is shared and read-only for you');
      return;
    }
    if (ctx.shared) { // a shared tree holds its own section's tiles only
      const c = this._components.find((x) => x.path === path);
      if (ownerKeyOf(c, this._myId) !== sectionOf(ctx.key)) {
        note('a tile can only be filed under its own owner section — use a personal folder for cross-owner grouping');
        return;
      }
    }
    this._fileInto(f.id, path, ctx);
  }

  // ---- nested folders + screen refs in the tree ----
  // Is folder aId an ancestor of bId? (walk bId's parents up) — used to reject cycles.
  _isAncestorFolder(aId, bId, ctx) {
    const by = new Map(ctx.folders.map((f) => [f.id, f]));
    let p = by.get(bId)?.parent ?? null, guard = 0;
    while (p && guard++ < 200) { if (p === aId) return true; p = by.get(p)?.parent ?? null; }
    return false;
  }
  _nestFolder(dragId, targetId, ctx) {
    if (dragId === targetId || this._isAncestorFolder(dragId, targetId, ctx)) return;
    ctx.mutate((fs) => fs.map((f) => f.id === dragId ? { ...f, parent: targetId } : f));
  }
  _unnestFolder(dragId, ctx) {
    ctx.mutate((fs) => fs.map((f) => f.id === dragId ? { ...f, parent: null } : f));
  }

  // ---- screens: visible tabs vs. parked-in-tree ----
  _isTracked(id) { return (this._side.folders ?? []).some((f) => f.items.includes('#screen:' + id)); }
  // Every tab, personal and org, in the user's order (D55): ids in _tabOrder
  // first, then the rest (personal in _screens order, org in server order).
  _tabList() {
    const all = [
      ...this._screens.map((s) => ({ kind: 'personal', id: s.id, s })),
      ...(this._orgScreens ?? []).map((s) => ({ kind: 'org', id: s.id, s })),
    ];
    const by = new Map(all.map((t) => [t.id, t]));
    const out = [], seen = new Set();
    for (const id of this._tabOrder ?? []) {
      const t = by.get(id);
      if (t && !seen.has(id)) { out.push(t); seen.add(id); }
    }
    for (const t of all) if (!seen.has(t.id)) { out.push(t); seen.add(t.id); }
    return out;
  }
  _visibleTabs() {
    return this._tabList().filter((t) => (t.kind === 'personal' ? !t.s.parked : !this._hiddenOrg?.[t.id]));
  }
  _openOrgScreen(id) {
    if (!(this._orgScreens ?? []).some((s) => s.id === id)) return;
    const { [id]: _, ...rest } = this._hiddenOrg ?? {};
    this._hiddenOrg = rest;
    this._active = id;
    if (this._mobile) this._drawer = false;
    this._save();
  }
  // Open a screen from the tree: un-park it (if parked) and make it active — the
  // live screen, not a snapshot.
  _openScreen(id) {
    if (!this._screens.some((s) => s.id === id)) return;
    this._screens = this._screens.map((s) => s.id === id ? { ...s, parked: false } : s);
    this._active = id;
    if (this._mobile) this._drawer = false;
    this._save();
  }
  // Drop a screen's tree ref; if it was only reachable via the tree (parked),
  // re-open it as a tab so it isn't stranded.
  _removeScreenFromTree(id) {
    const wasParked = this._screens.find((s) => s.id === id)?.parked;
    this._side = { ...this._side, folders: this._side.folders.map((f) =>
      ({ ...f, items: f.items.filter((it) => it !== '#screen:' + id) })) };
    if (wasParked) this._screens = this._screens.map((s) => s.id === id ? { ...s, parked: false } : s);
    this._save();
  }

  // ---- sidebar: system status footer (admin-only; polls /status every 5s) ----
  async _loadAlerts(dismiss) { // dismiss: an alert's dismiss route, POSTed first
    if (document.hidden) return;
    try {
      if (dismiss) await window.xbin?.fetch('/api/xbin' + dismiss, { method: 'POST' });
      const r = await window.xbin?.fetch('/api/xbin/alerts');
      if (r?.ok) this._alerts = (await r.json()).alerts || [];
    } catch { /* transient */ }
  }

  // ---- component status & notifications (tiles → workspace) ----
  // RAW fetch: the cookie principal is the signed-in user, so the list is
  // read-filtered to their tiles (xbin.fetch would downgrade to the chrome
  // element and see nothing). Live updates arrive as `status` events.
  async _loadStatuses() {
    try {
      const r = await fetch('/api/xbin/tile-report');
      if (r.ok) { this._status = (await r.json()).statuses || {}; this._reflectTitle(); }
    } catch { /* transient */ }
  }
  _onStatusEvent(e) {
    if (!e.component) return;
    const d = e.data || {};
    if (d.transient) { this._pushToast(e.component, d); return; }
    const next = { ...this._status };
    if ((!d.level || d.level === 'ok') && !d.message) delete next[e.component];
    else next[e.component] = { level: d.level, message: d.message || '', ts: d.ts };
    this._status = next;
    this._reflectTitle();
  }

  // Open change proposals ("code PRs") per tile — the ⇄ badges. RAW fetch for
  // the same reason as statuses: the summary is read-filtered to the signed-in
  // user's tiles. Live updates arrive as `pr` events.
  async _loadPRs() {
    try {
      const r = await fetch('/api/xbin/code/prs/summary');
      if (r.ok) this._prs = (await r.json()).counts || {};
    } catch { /* transient */ }
  }
  // Ambient signal in the browser tab title when something needs attention:
  // the level's word (a title can't draw a glyph).
  _reflectTitle() {
    const worst = worstStatus(this._status, Object.keys(this._status));
    const mark = worst === 'error' || worst === 'warn' ? `${STATUS[worst].word} · ` : '';
    const nm = this._brand?.title || this.name; // the admin's title, else the name attribute (D76)
    document.title = mark + (nm ? `${nm} · xbin` : 'xbin');
  }
  _setBrand(b) { this._brand = b; applyFavicon(b); this._reflectTitle(); }
  _pushToast(comp, d, ttl = 6500) {
    const id = uid();
    this._toasts = [...this._toasts, { id, comp, level: d.level || 'info', message: d.message || '', action: d.action }];
    setTimeout(() => this._dismissToast(id), ttl);
  }
  _dismissToast(id) { this._toasts = this._toasts.filter((t) => t.id !== id); }

  async _loadSys() {
    if (this._side.collapsed || document.hidden) return;
    try {
      // RAW fetch on purpose: the cookie principal is the signed-in admin;
      // xbin.fetch would attach the chrome frame token and downgrade to a
      // non-admin element principal (403 on these endpoints).
      const [st, be, vs] = await Promise.all([
        fetch('/api/xbin/status'),
        fetch('/api/xbin/backends'),
        fetch('/api/xbin/vault-status'),
      ]);
      if (!st.ok) { this._sys = null; return; } // not admin — footer hidden
      const status = await st.json();
      const backends = be.ok ? await be.json() : {};
      const vault = vs.ok ? await vs.json() : null;

      // Rates from cumulative counters (delta between polls).
      let reqRate = 0, mbRate = 0;
      const t = status.traffic ?? {};
      const now = performance.now();
      if (this._sysPrev && now > this._sysPrev.at) {
        const dt = (now - this._sysPrev.at) / 1000;
        reqRate = Math.max(0, (t.reqs - this._sysPrev.reqs) / dt);
        mbRate = Math.max(0, (t.bytesOut - this._sysPrev.bytes) / dt / 1048576);
      }
      // CPU% from jiffy deltas.
      let cpu = null;
      const h = status.host ?? {};
      if (this._sysPrev && h.cpuTotal > this._sysPrev.cpuTotal) {
        cpu = (h.cpuBusy - this._sysPrev.cpuBusy) / (h.cpuTotal - this._sysPrev.cpuTotal);
      }
      this._sysPrev = { at: now, reqs: t.reqs ?? 0, bytes: t.bytesOut ?? 0,
                        cpuBusy: h.cpuBusy ?? 0, cpuTotal: h.cpuTotal ?? 0 };

      const states = Object.values(backends ?? {});
      this._sys = {
        cpu,
        mem: h.memTotal ? 1 - (h.memAvail ?? 0) / h.memTotal : null,
        disk: h.diskTotal ? 1 - (h.diskFree ?? 0) / h.diskTotal : null,
        diskFree: h.diskFree, diskTotal: h.diskTotal,
        services: states.length,
        running: states.filter((b) => b.state === 'healthy').length,
        components: status.components ?? 0,
        vault: vault?.mode ?? null,
        version: status.version || null, // running xbind build commit
        reqRate, mbRate,
      };
    } catch { /* xbind restarting; next tick */ }
  }

  // Probe once whether the signed-in human is an admin (raw fetch → cookie
  // principal). Gates the per-tile ⚙ mini-admin; its APIs 403 otherwise
  // anyway. Org admins get the ⚙ on THEIR org's tiles and user-owners on
  // their own (D24): the access + lifecycle sections work for them,
  // admin-only sections just show their 403s.
  // End an admin's view-as session (D64): the server hands the cookie back
  // to their own session; a full navigation reloads the shell as them.
  async _exitViewAs() {
    try { await fetch('/api/xbin/impersonate/stop', { method: 'POST' }); } catch { /* the reload tells */ }
    location.href = '/';
  }

  async _probeAdmin() {
    try {
      const r = await fetch('/api/xbin/whoami');
      if (r.ok) {
        const d = await r.json();
        this._who = d;
        this._myId = d.kind === 'user' ? d.id : null;
        this._isAdmin = !!d.admin;
        this._adminOrgs = new Set((d.orgs ?? []).filter((o) => o.admin).map((o) => o.id));
        this._ownedTiles = new Set(d.owned ?? []);
        this._orgish = !!((d.orgs ?? []).length || (d.owned ?? []).length);
        // First-run hardening card: running on the bootstrap token with no
        // accounts yet (the reusable token URL is the only credential).
        if (d.kind === 'root' && !localStorage.getItem('xbin-setup-dismissed')) {
          const u = await fetch('/api/xbin/users').then((x) => (x.ok ? x.json() : null)).catch(() => null);
          this._setupCard = !!u && (u.users ?? []).length === 0;
        } else {
          this._setupCard = false;
        }
      }
    } catch { /* xbind restarting */ }
    this._loadPendingCount();
  }

  // The ⚑ badge: ACTIONABLE pending items — requests this human may approve
  // (D26/D33), their own tiles' requests still waiting (direction "mine"),
  // and unbound interface slots in their view, less what they dismissed
  // (D188: pendingCount). Refetched on grants/users events and dismissals,
  // so a new request lights the badge without a reload.
  async _loadPendingCount() {
    try {
      const j = (r) => (r.ok ? r.json() : null);
      const [g, b, q, d] = await Promise.all([fetch('/api/xbin/grants').then(j), fetch('/api/xbin/bindings').then(j),
        fetch('/api/xbin/access-requests').then(j), loadDismissed(window.xbin?.fetch ?? fetch)]);
      this._pendingN = pendingCount(g, b, q, d);
    } catch { /* xbind restarting; next event refetches */ }
  }

  // _canAdminTile: the ⚙ mini-admin shows for workspace admins, for org
  // admins on tiles their org OWNS, and for the tile's USER-OWNER (D24 —
  // the /components list carries the owner ref; whoami carries `owned`).
  _canAdminTile(path) {
    if (this._isAdmin) return true;
    if (this._ownedTiles?.has(path)) return true;
    const owner = this._components.find((c) => c.path === path)?.owner ?? '';
    return owner.startsWith('org:') && !!this._adminOrgs?.has(owner.slice(4));
  }

  // Screen sharing (D37): a ws-admin pins the CURRENT personal screen as the
  // workspace default (what new users seed from); an org admin shares it to
  // an org they administer (members get it as a tab).
  _screenShareMenu() {
    if (this._activeOrgScreen || !this._screen) return nothing;
    const orgs = [...(this._adminOrgs ?? [])];
    if (!this._isAdmin && !orgs.length) return nothing;
    const org = orgs.includes(this._shareOrg) ? this._shareOrg : orgs[0];
    const targets = (this._orgScreens ?? []).filter((s) => s.org === org);
    return html`
      <div class="hd">this screen</div>
      ${this._isAdmin ? html`<div class="row">
        <span title="new users' first screen seeds from this layout (D37)">workspace default</span>
        <button class="act" @click=${() => this._saveWsDefault()}>save current</button>
      </div>` : nothing}
      ${orgs.length ? html`<div class="row share">
        <select id="share-org" aria-label="organisation" .value=${org} @change=${(e) => { this._shareOrg = e.target.value; }}>
          ${orgs.map((o) => html`<option value=${o} ?selected=${o === org}>${o}</option>`)}</select>
        <select id="share-target" aria-label="as a new screen, or replacing one" title="create a new org screen, or replace an existing one with this layout (D55)">
          <option value="">as a new org screen</option>
          ${targets.map((s) => html`<option value=${s.id}>replace "${s.name}" (rev ${s.rev ?? 1})</option>`)}
        </select>
        <button class="act" title="members of the org see it as a shared tab (read-only unless you widen its edit setting in the organisations tile)"
          @click=${() => this._shareToOrg(org, this.renderRoot.getElementById('share-target')?.value || '')}>share screen to org</button>
      </div>` : nothing}`;
  }

  async _saveWsDefault() {
    const res = await publish('/api/xbin/screens/default', { tiles: this._tiles });
    this._menuMsg = res.status === 'ok' ? { ok: true, text: 'saved — new users seed from this screen' } : { ok: false, text: res.message };
    setTimeout(() => { this._menuMsg = null; }, 4000);
  }

  // Share the current personal screen: as a new org screen, or replacing an
  // existing one in place (revisioned — a stale replace goes through the same
  // conflict dialog as a draft save).
  async _shareToOrg(org, targetId = '', force = false) {
    if (!org) return;
    const target = targetId ? (this._orgScreens ?? []).find((s) => s.id === targetId && s.org === org) : null;
    const body = target
      ? { id: target.id, org, tiles: this._tiles, rev: target.rev ?? 1, force }
      : { org, name: this._screen?.name || org, tiles: this._tiles };
    const res = await publish('/api/xbin/screens/org', body);
    const d = res.body ?? {};
    if (res.status === 'conflict' && target) {
      this._settingsOpen = false;
      this._conflict = { kind: 'replace', id: target.id, org, spec: conflictDialog({ what: `"${target.name}"`, rev: d.rev,
        by: this._whoLabel(d.screen?.updatedBy), at: d.screen?.updatedAt, baseRev: target.rev ?? 1, replace: true }) };
      return;
    }
    this._menuMsg = res.status === 'ok'
      ? { ok: true, text: target ? `replaced "${target.name}" — now rev ${d.rev}` : `shared to ${org} — appears as a tab for its members` }
      : { ok: false, text: res.message };
    if (res.status === 'ok') this._loadShared();
    setTimeout(() => { this._menuMsg = null; }, 4000);
  }

  _sideResizeStart(e) {
    if (e.button !== 0) return;
    e.preventDefault();
    const startX = e.clientX, startW = this._side.width || 224;
    dragPointer({
      cursor: 'col-resize',
      onMove: (ev) => { this._side = { ...this._side, width: Math.min(480, Math.max(140, startW + ev.clientX - startX)) }; },
      onUp: () => this._save(),
    });
  }

  // ---- the sidebar is <bx-side>: it renders from this view of the shell's ----
  // state and acts through these handlers, so layout persistence and the
  // draft flows keep one owner.
  _sideState() {
    return {
      components: this._components, screens: this._screens, orgScreens: this._orgScreens ?? [], active: this._active,
      side: this._side, hiddenOrg: this._hiddenOrg ?? {}, orgDrafts: this._orgDrafts ?? {}, folderDrafts: this._folderDrafts ?? {},
      who: this._who, myId: this._myId, status: this._status, prs: this._prs, showHidden: this._showHidden,
      hiddenCount: this._hiddenCount, sys: this._sys, mobile: this._mobile, menuOpen: !!this._menu,
      openPaths: new Set(this._tiles.map((o) => o.path)), orgButton: !!(this._orgish || this._adminOrgs?.size), pendingN: this._pendingN,
    };
  }
  _sideActions() {
    return {
      folderCtx: (k) => this._folderCtx(k), saveSide: (p) => this._saveSide(p), toggleOwnerSec: (k) => this._toggleOwnerSec(k),
      enterFolderEdit: (s) => this._enterFolderEdit(s), dropFolderDraft: (k) => this._dropFolderDraft(k),
      discardFolderDraft: (k) => this._discardFolderDraft(k), saveFolderDraft: (k) => this._saveFolderDraft(k),
      addFolder: (ctx) => this._addFolder(ctx), folderDialog: (f, ctx) => this._folderDialog(f, ctx),
      deleteFolder: (f, ctx) => this._deleteFolder(f, ctx), toggleFolder: (f, ctx, open) => this._toggleFolder(f, ctx, open),
      fileInto: (id, p, ctx) => this._fileInto(id, p, ctx), moveInto: (id, p, before, ctx) => this._moveInto(id, p, before, ctx),
      unnestFolder: (id, ctx) => this._unnestFolder(id, ctx), dropOnFolder: (e, f, ctx) => this._dropOnFolder(e, f, ctx),
      toggle: (p) => this._toggle(p), tileMenu: (e, p, anchor) => this._openTileMenu(e, p, anchor),
      openOrgScreen: (id) => this._openOrgScreen(id), openScreen: (id) => this._openScreen(id),
      removeScreenFromTree: (id) => this._removeScreenFromTree(id), toggleShowHidden: () => { this._showHidden = !this._showHidden; },
      openOrganisations: () => { if (!this._isOpen('tiles/organisations')) this._toggle('tiles/organisations'); },
    };
  }

  // Find a free-ish grid spot for a new tile: scan the top row left→right for a
  // gap wide enough for a default tile that overlaps nothing, else drop into a
  // new row below everything. Cheap and deterministic; the user rearranges.
  _freeSpot() {
    const placed = this._tiles.filter((o) => !o.float);
    const taken = (x, y) => placed.some((o) => overlaps({ x, y, w: DEF_W, h: DEF_H }, o));
    const viewW = this.renderRoot?.querySelector('main')?.clientWidth || 1200;
    const cols = Math.max(1, Math.floor(viewW / (DEF_W * (this._gridScale || 1))));
    for (let row = 0; row < 100; row++) {
      for (let c = 0; c < cols; c++) {
        const x = c * DEF_W, y = row * DEF_H;
        if (!taken(x, y)) return { x, y };
      }
    }
    const maxY = placed.reduce((m, o) => Math.max(m, o.y + o.h), 0);
    return { x: 0, y: snap(maxY) };
  }

  // `at` (a menu's click point, D80) places a new tile there; else _freeSpot.
  _toggle(path, at = null) {
    const os = this._activeOrgScreen;
    if (os && !this._orgDrafts?.[os.id]) {
      // A shared screen in view mode: an editor's deliberate add opens a draft
      // (still nothing published until saved); anyone else gets the tile on
      // their own screen instead.
      if (!os.canEdit) { this._openOnPersonal(path); return; }
      this._enterEdit(os.id);
      this._pushToast(os.org, { level: 'info', message: 'editing the org screen — "Save and update for everyone" publishes it' });
    }
    if (this._isOpen(path)) {
      this._mutateTiles((tiles) => tiles.filter((o) => o.path !== path));
      return;
    }
    const { x, y } = at ? spotNear(this._tiles, at) : this._freeSpot();
    this._noteRecent(path);
    const doc = screenMode(this._screen) === 'doc'; // Document mode: a new tile is the last row (D187)
    this._mutateTiles((tiles) => (doc ? placeNew : (t) => t)([...tiles, { path, x, y, w: DEF_W, h: DEF_H }], path));
    if (this._mobile) this._drawer = false; // tapping a tile closes the drawer
  }

  // Open a tile on the user's own screen: the first visible personal tab, or
  // a fresh Home when there is none.
  _openOnPersonal(path) {
    let s = this._visibleTabs().find((t) => t.kind === 'personal')?.s;
    if (!s) {
      s = { id: uid(), name: 'Home', tiles: [] };
      this._screens = [...this._screens, s];
    }
    this._active = s.id;
    this._noteRecent(path);
    if (!this._isOpen(path)) {
      const { x, y } = this._freeSpot();
      this._mutateTiles((tiles) => [...tiles, { path, x, y, w: DEF_W, h: DEF_H }]);
    } else {
      this._save();
    }
    this._pushToast(path, { level: 'info', message: 'read-only org screen — opened on your screen' });
    if (this._mobile) this._drawer = false;
  }

  // ---- screens: the tabs and their actions are shell-tabs.js ----

  // ---- the tile surface is <bx-canvas> (grid + floats); these are the shell's ends of it ----
  _setFloat(path, patch) {
    this._mutateTiles((tiles) => tiles.map((o) =>
      o.path === path && o.float ? { ...o, float: { ...o.float, ...patch } } : o));
  }
  _raiseFocusedFloat() { this._canvas?.raiseFocusedFloat(); }

  // ---- test surface (hack/ui-harness) ----
  // Stable names over the shell's private state so the harness never reaches
  // for a `_member` or a shadow-root path that a refactor renames. Reads and
  // writes existing state only — no test-only branches in production paths.
  // Inert in normal use; nothing in the shell calls it.
  testApi() {
    const s = this;
    return {
      get screens() { return s._screens; },
      get activeScreen() { return s._active; },
      get gridScale() { return s._gridScale; },
      setGridScale: (k) => s._setGridScale(k),
      setScreen(id) { s._active = id; s._save(); },
      // the layout save is debounced (400 ms): await this before closing a
      // browser context, or the last change never reaches the server
      flushSave: () => s._saveTimer ? s._saveNow() : Promise.resolve(),
      // the first personal (unparked) screen — where passes start so a shared
      // org screen never opens a draft by accident; returns its id
      usePersonalScreen() { const p = s._screens.find((x) => !x.parked); if (p) { s._active = p.id; s._save(); } return p?.id ?? null; },
      isOpen: (path) => s._isOpen(path),
      openTile(path) { if (!s._isOpen(path)) s._toggle(path); },
      closeTile(path) { if (s._isOpen(path)) s._toggle(path); },
      toggleTile: (path) => s._toggle(path),
      get openTiles() { return s._tiles; },
      floatOf: (path) => s._tiles.find((o) => o.path === path)?.float ?? null,
      setFloat: (path, patch) => s._setFloat(path, patch),
      setGeom: (fn) => s._mutateTiles(fn), // fn(tiles copy) → new tiles of the active screen
      get spawnWindows() { return s._spawnWins; },
      setSpawnWindows(v) { s._spawnWins = v; },
      fitWindows: (persist) => s._fitWindows(persist),
      raiseFocusedFloat: () => s._raiseFocusedFloat(),
      frameFor: (path) => s._frameOf(path),
      get adminWindow() { return s._adminPop; },
      openAdminWindow: (path, section) => s._openAdminWin(path, section),
      closeAdminWindow() { s._adminPop = null; },
      adminWindowElement: () => s.renderRoot.querySelector('.admin-pop'),
      tileAdminElement: () => s.renderRoot.querySelector('bx-tile-admin'),
      // an element of the shell's own DOM (.float[data-path], .spawn, bx-grants…)
      query: (sel) => s.renderRoot.querySelector(sel) ?? s._canvas?.renderRoot.querySelector(sel) ?? null,
      openCanvasMenu: (at) => s._openCanvasMenu({ clientX: at?.x ?? 0, clientY: at?.y ?? 0, preventDefault() {} }),
      canvasMenuItems: () => s._canvasMenuItems(),
      get menuOpen() { return !!s._menu; },
      closeMenu() { s._menu = null; },
      get layoutBusy() { return layoutEditing(s); }, // a prefs event now would wait
      openSettings() { s._settingsOpen = true; },
      setDrawer(v) { s._drawer = !!v; },
      get toasts() { return s._toasts ?? []; },
      selectedText: () => s._selectedText(),
      get orgScreens() { return s._orgScreens ?? []; },
      orgDraft: (id) => s._orgDrafts?.[id] ?? null,
      dropOrgDraft: (id) => s._dropDraft(id),
      openOrgScreen: (id) => s._openOrgScreen(id),
      hideOrgTab: (id) => hideOrgTab(s, id),
      folderCtx: (key) => s._folderCtx(key),
      fileInto: (folderId, path, ctx) => s._fileInto(folderId, path, ctx),
      // Document mode (D187): the active screen's mode, its rows, the top bar
      get screenMode() { return screenMode(s._screen); },
      addScreen() { addScreen(s); return s._active; },
      tileMenuItems: (path) => s._tileMenuItems(path),
      setScreenMode: (id, mode) => setScreenMode(s, id ?? s._active, mode),
      docRows: () => docRows(s._tiles).map((r) => ({ cols: r.cols, paths: r.tiles.map((t) => t.path) })),
      get topBar() { return { on: s._top.on, hidden: s._top.hidden }; },
    };
  }

  render() {
    const alertLevel = (l) => (l === 'crit' ? { icon: 'error', word: 'Critical' } : { icon: 'warning', word: 'Warning' });
    return html`
      ${this._alerts.length ? html`<div class="alerts">
        ${this._alerts.map((a) => html`<div class="alert ${a.level}" role="alert">
          <bx-icon class="ico" name=${alertLevel(a.level).icon}></bx-icon><span class="lvl">${alertLevel(a.level).word}</span>
          <span class="msg">${a.message}</span>${a.dismiss ? html`<button class="dismiss" @click=${() => this._loadAlerts(a.dismiss)}>dismiss</button>` : nothing}</div>`)}
      </div>` : nothing}
      ${this._setupCard ? html`<div class="alerts"><div class="alert warn" role="alert">
        <bx-icon class="ico" name="warning"></bx-icon><span class="lvl">Warning</span>
        <span class="msg"><b>Secure this workspace:</b> (1) create your admin account
          (admin tile → users), (2) then disable token sign-in (users →
          sign-in security). The bootstrap token URL in your server logs is a
          reusable credential — anyone who sees it gets in.</span>
        <button class="dismiss"
          @click=${() => { localStorage.setItem('xbin-setup-dismissed', '1'); this._setupCard = false; }}>dismiss</button>
      </div></div>` : nothing}
      ${this._toasts.length ? html`<div class="toasts">
        ${repeat(this._toasts.slice(-3), (t) => t.id, (t) => html`
          <div class="toast st-${STATUS[t.level] ? t.level : 'info'}" role="status" @click=${() => { t.action?.(); this._dismissToast(t.id); }} title=${t.action ? 'open' : 'dismiss'}>
            ${statusIcon(t.level)}<span class="lvl">${(STATUS[t.level] ?? STATUS.info).word}</span>
            <span class="tmsg"><b>${t.comp.includes('/') ? t.comp.slice(t.comp.indexOf('/') + 1) : t.comp}</b>${t.message ? ' — ' + t.message : ''}</span>
          </div>`)}
      </div>` : nothing}
      ${this._who?.impersonatedBy ? html`<div class="viewas" role="status">
        <bx-icon name="eye"></bx-icon>
        <span class="msg">viewing as <b>${this._who.name && this._who.name !== this._who.id ? `${this._who.name} (${this._who.id})` : this._who.id}</b>
          — read-only: this is what they see, in every tab of this browser, until you exit</span>
        <button class="chip" title="back to your own session" @click=${() => this._exitViewAs()}>exit view</button>
      </div>` : nothing}
      ${this._top.edge()}
      <div class="top ${this._top.on ? 'docmode' : ''} ${this._top.hidden ? 'away' : ''}">
        ${this._mobile ? html`<button class="ham" title="menu" aria-label="menu" aria-expanded=${this._drawer ? 'true' : 'false'}
          @click=${() => { this._drawer = !this._drawer; }}><bx-icon name="menu"></bx-icon></button>` : nothing}
        ${brandLogo(this)}
        ${this._mobile ? html`<span class="spacer"></span>` : tabStrip(this)}
        <button class="chip settings ${this._settingsOpen ? 'on' : ''}" title="workspace settings (per user)" aria-haspopup="true" aria-expanded=${this._settingsOpen ? 'true' : 'false'}
                @click=${() => { this._settingsOpen = !this._settingsOpen; if (this._settingsOpen) this._look = appearance(); }}>settings</button>
        ${this._settingsOpen ? html`
          <div class="ctx-backdrop" @pointerdown=${() => { this._settingsOpen = false; }}></div>
          <div class="wsmenu" role="dialog" aria-label="settings">
            <div class="hd">settings</div>
            ${this._who?.kind === 'user' ? html`<button class="act add-device" data-add-device title="the xbin app on a phone or tablet: a QR code to scan"
              @click=${() => { this._settingsOpen = false; openDevices({ add: true }); }}><bx-icon name="device" size="20"></bx-icon><b>add a device</b><span>the xbin app on your phone · QR code</span></button>` : nothing}
            ${appearanceRows(this)}
            <div class="row"><span>Font size</span>
              <span class="fs">
                <button class="step" title="smaller" aria-label="smaller text" @click=${() => this._saveSettings({ fontSize: (this._settings.fontSize || 13) - 1 })}><bx-icon name="minus"></bx-icon></button>
                <b>${this._settings.fontSize || 13}</b>
                <button class="step" title="larger" aria-label="larger text" @click=${() => this._saveSettings({ fontSize: (this._settings.fontSize || 13) + 1 })}><bx-icon name="plus"></bx-icon></button>
                ${(this._settings.fontSize || 13) !== 13 ? html`
                  <button class="step" title="reset"
                          @click=${() => this._saveSettings({ fontSize: 13 })}>reset</button>` : nothing}
              </span></div>
            <div class="row"><span>Grid scale</span>
              <span class="fs">
                <input type="range" min="0.5" max="1.5" step="0.05" .value=${String(this._gridScale)} aria-label="grid scale"
                       title="how large the tile layout renders in this browser; the layout itself is unchanged"
                       @input=${(e) => this._setGridScale(e.target.value)}>
                <b class="gs">${this._gridScale.toFixed(2)}× · ${Math.round(GRID * this._gridScale)} px</b>
                ${this._gridScale !== 1 ? html`
                  <button class="step" title="reset"
                          @click=${() => this._setGridScale(1)}>reset</button>` : nothing}
              </span></div>
            <div class="gshint">per browser: the layout stays the same for everyone</div>
            ${this._screenShareMenu()}
            ${accountMenu(this)}
            ${this._menuMsg ? html`<div class="menu-msg ${this._menuMsg.ok ? 'ok' : 'bad'}" role="status"><bx-icon name=${this._menuMsg.ok ? 'ok' : 'error'}></bx-icon><span>${this._menuMsg.text}</span></div>` : nothing}
          </div>` : nothing}
        <a class="chip" href="/docs/" target="_blank">docs</a>
        <a class="chip" href="/logout" @click=${(e) => { e.preventDefault(); fetch('/logout', { method: 'POST' }).then(() => location.reload()); }}>sign out</a>
      </div>

      ${this._mobile ? tabStrip(this) : nothing}
      ${this._orgBar()}

      <div class="body ${this._mobile ? 'mobile' : ''}">
        ${(this._side.collapsed && !this._mobile) ? html`
          <aside class="collapsed">
            <button class="expand" title="expand sidebar" aria-label="expand the sidebar"
                    @click=${() => this._saveSide({ collapsed: false })}><bx-icon name="chevron-right"></bx-icon></button>
          </aside>` : html`
          <bx-side class="${this._mobile ? 'drawer' : ''} ${this._drawer ? 'open' : ''}"
                   style=${this._mobile ? nothing : `width:${this._side.width || 224}px`}
                   .state=${this._sideState()} .actions=${this._sideActions()}></bx-side>
          ${this._mobile ? nothing : html`<div class="side-handle" title="drag to resize"
               @pointerdown=${(e) => this._sideResizeStart(e)}></div>`}`}
        ${this._mobile && this._drawer ? html`<div class="drawer-backdrop"
          @click=${() => { this._drawer = false; }}></div>` : nothing}
        <main @contextmenu=${(e) => this._onContextMenu(e)}
              @pointerdown=${(e) => { if (!e.target.closest('.card, button, input, select, a, bx-frame, bx-canvas, .grants, bx-menu')) this._pressStart(e, () => this._openCanvasMenu({ clientX: e.clientX, clientY: e.clientY })); }}
              @pointermove=${(e) => this._pressMove(e)}
              @pointerup=${() => this._pressCancel()} @pointercancel=${() => this._pressCancel()}>
          <div class="grants"><bx-grants></bx-grants><bx-bindings></bx-bindings><bx-part-consent .components=${this._components} .who=${this._who}></bx-part-consent></div>
          <bx-canvas .tiles=${this._tiles} .components=${this._components} .prs=${this._prs} .mode=${screenMode(this._screen)}
            .canMutate=${this._canMutate} .personal=${!this._activeOrgScreen} .mobile=${this._mobile} .menuOpen=${!!this._menu} .scale=${this._gridScale}
            .canAdminTile=${(p) => this._canAdminTile(p)} .who=${this._who} .alerts=${this._alerts} .reload=${() => { this._load(); this._loadAlerts(); }}
            .emptyText=${this._activeOrgScreen && !this._canMutate ? 'This shared screen is empty.' : 'This screen is empty. Open a tile from the sidebar.'}
            @bx-tiles=${(e) => this._mutateTiles(() => e.detail)}
            @bx-toggle-tile=${(e) => this._toggle(e.detail)}
            @bx-tile-menu=${(e) => this._openTileMenu(e.detail.at, e.detail.path, e.detail.anchor, { selection: e.detail.selection })}
            @bx-canvas-menu=${(e) => this._openCanvasMenu(e.detail)}
            @bx-admin-win=${(e) => this._openAdminWin(e.detail)}></bx-canvas>
          <slot style="display:none"></slot>
        </main>
      </div>

      ${repeat(this._spawnWins, (w) => w.id, (w) => this._spawnTemplate(w))}
      ${this._adminPopTemplate()}
      ${repeat(this._dialogs, (d) => d.id, (d) => html`
        <bx-dialog open .spec=${d.spec} from=${d.from}
          @bx-dialog-resolve=${(e) => this._resolveDialog(d.id, e.detail)}></bx-dialog>`)}

      ${this._menu ? html`
        <bx-menu open .items=${this._menu.items} .x=${this._menu.x} .y=${this._menu.y} .anchor=${this._menu.anchor}
          ?sheet=${this._menu.sheet} title=${this._menu.title || nothing}
          @bx-menu-close=${() => { this._menu = null; }}></bx-menu>` : nothing}

      ${this._conflict ? html`
        <bx-dialog open .spec=${this._conflict.spec}
          @bx-dialog-resolve=${(e) => this._onConflict(e.detail)}></bx-dialog>` : nothing}

      ${this._create ? html`
        <bx-dialog open .spec=${this._create}
          @bx-dialog-resolve=${(e) => this._onNewTile(e.detail)}></bx-dialog>` : nothing}

      ${this._folderEdit ? html`
        <bx-dialog open .spec=${this._folderEdit.spec}
          @bx-dialog-resolve=${(e) => this._onFolderEdit(e.detail)}></bx-dialog>` : nothing}
    `;
  }
}

customElements.define('bx-shell', BxShell);
