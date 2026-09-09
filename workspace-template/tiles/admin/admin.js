/**
 * <bx-admin> — the workspace admin console (tiles/admin). Full owner-view
 * into the running system, powered by xbind's admin-capable endpoints via
 * the xbin:admin capability (see xbin.json / API.md).
 *
 * Tabs: overview · code & history · users · orgs & teams · vault ·
 * roles & grants · cron.
 * All reads go through xbin.fetch (admin identity attributed by frame token)
 * and refresh on the grants/reload event stream. The code & history tab browses
 * a component's files and git log/diffs (syntax-highlighted via vendored
 * highlight.js), scoped to its path in the single workspace repo.
 */
import { LitElement, html, css, nothing, svg, repeat } from 'lit';
import { unsafeHTML } from 'lit';
import '/vendor/bx-multiselect.js';

// "stale" for the users table's offboarding chip: no sign-in for 30 days.
const STALE_SEC = 30 * 86400;

import { xbinApi as api } from '/vendor/bx-kit.js';
import { diffHTML, hl, langFor } from '/vendor/bx-code.js';
import { base } from './admin-css.js';
// Tab elements (tiles/admin/tabs/*): each owns its data, endpoints and CSS
// slice; the router keeps the nav, the shared lists and the global err /
// notice slots, which the tabs feed through composed events.
import './tabs/map.js';
import './tabs/netsets.js';
import './tabs/permsets.js';
import './tabs/vault.js';
import './tabs/cron.js';
import './tabs/backup.js';
import './tabs/binding.js';
import './tabs/ingress.js';
import './tabs/orgs.js';
import { targetOptions, targetDatalist, serviceOptions, serviceDatalist, fmtBytes, fmtDur, setLifecycle, WithDrafts, PRESETS, presetOf, groupsDatalist } from './shared.js';

export class BxAdmin extends WithDrafts(LitElement) {
  static properties = {
    _tab: { state: true },
    _ov: { state: true },       // auth-overview
    _vaults: { state: true },      // [{component, keys}] (null while sealed)
    _vaultStatus: { state: true }, // {initialized, sealed, mode, insecure}
    _cron: { state: true },
    _users: { state: true },
    _sessions: { state: true }, // live browser sessions with IPs (sessions tab)
    _orgs: { state: true },     // orgs & teams (docs/auth.md)
    _wsPolicy: { state: true }, // workspace policy-ceiling rows
    _permsets: { state: true }, // {sets, attachedTo} (D28)
    _netsets: { state: true },  // {sets: {name: {rules, created}}, attachedTo} — org network sets (D54)
    _invite: { state: true },   // last minted invite link {id, url} (D22)
    _token: { state: true },    // freshly rotated owner token (copy-field box)
    _pwEdit: { state: true },   // user id whose password is being reset inline
    _notice: { state: true },   // green success line (never the red .err slot)
    _reqs: { state: true },     // pending human access requests (D36)
    _defaults: { state: true }, // defaultTiles map (D27)
    _newUsers: { state: true }, // new-account defaults {tiles, canCreate, termApi, termNet, orgs} (D52)
    _tileCreation: { state: true }, // 'any' | 'org-only' (D52)
    _newSignin: { state: true }, // add-user form: 'password' | 'invite' | 'sso'
    _usersQ: { state: true },    // users table text filter (reactive, unlike _q)
    _usersChips: { state: true }, // users table chips: Set of admins|disabled|invited|never|stale|noorg|org:<id>
    _sessQ: { state: true },     // sessions table user filter
    _ssoTest: { state: true },   // last "test connection" result (null | {busy} | report)
    _bulkBusy: { state: true },  // bulk disable in flight
    _drafts: { state: true },   // click-through editor drafts, keyed by context
    _showHidden: { state: true }, // reveal hidden (state=hidden) tiles in lists (D42)
    _authSettings: { state: true },
    _alerts: { state: true }, // {tokenLoginDisabled, hasAdminUser, canDisable}
    _ifaces: { state: true },   // {bindings, components} — interface wiring
    _busy: { state: true },      // comp path mid heavy op (offload/restore/backup)
    _err: { state: true },
    _denied: { state: true },
    _codeComp: { state: true }, // component being browsed in the code tab
    _codeTree: { state: true }, // its files
    _codeFile: { state: true }, // {path, content|binary|truncated}
    _codeLog: { state: true },  // its commits
    _codeDiff: { state: true }, // {rev, diff}
    _codeMode: { state: true }, // 'file' | 'diff'
    _rt: { state: true },       // /runtime snapshot {host, backends}
    _rtOpen: { state: true },   // set of expanded backend paths
    _stSort: { state: true },   // live-stats table sort {col, dir}
    _stTotOrg: { state: true }, // totals charts: split lines per org
    _resType: { state: true },  // resources table: active type tab
    _stFilter: { state: true }, // live-stats name-prefix filter
    _stGroup: { state: true },  // live-stats: group rows by org
    _stOpen: { state: true },   // live-stats: tile expanded into big charts
  };

  static styles = [base, css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
            color: var(--bx-text, #d4d9e0); background: var(--bx-panel, #23272e); }
    /* two-level nav: a primary group row + a sub-tab row under it */
    .groups { display: flex; gap: 4px; padding: 6px 8px 0; flex-wrap: wrap;
              background: var(--bx-panel-2, #2b3038); position: sticky; top: 0; z-index: 2; }
    .groups button { border: 0; background: none; font: inherit; font-size: 12px; font-weight: 600;
      padding: 5px 12px; cursor: pointer; color: var(--bx-muted, #868f9a); border-radius: 6px;
      letter-spacing: .01em; }
    .groups button.on { background: var(--bx-accent, #f5a623); color: #fff; }
    .groups button:not(.on):hover { background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); }
    .tabs { display: flex; gap: 2px; padding: 4px 8px 0; flex-wrap: wrap;
            border-bottom: 1px solid var(--bx-border, #363c45);
            background: var(--bx-panel-2, #2b3038); position: sticky; top: 33px; z-index: 1; }
    .tabs.sub { top: 33px; }
    .tabs button { border: 1px solid transparent; border-bottom: none; background: none;
      font: inherit; font-size: 12px; padding: 4px 12px; cursor: pointer;
      color: var(--bx-muted, #868f9a); border-radius: 5px 5px 0 0; }
    .tabs button.on { background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      border-color: var(--bx-border, #363c45); margin-bottom: -1px; }

    /* ---- code & history ---- */
    .code { display: grid; grid-template-columns: 240px 1fr; gap: 12px; align-items: start; }
    .code .side { min-width: 0; }
    .code .main { min-width: 0; }
    .code .files, .code .hist { border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      overflow: hidden; margin-bottom: 10px; }
    .code .files .row, .code .hist .row { padding: 3px 8px; cursor: pointer; font-size: 12px;
      border-top: 1px solid var(--bx-border, #363c45); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .code .files .row:first-child, .code .hist .row:first-child { border-top: 0; }
    .code .files .row.on, .code .hist .row.on { background: var(--bx-panel-2, #2b3038); }
    .code .files .row:hover, .code .hist .row:hover { background: var(--bx-panel-2, #2b3038); }
    .code .hist .row .s { font-family: var(--bx-mono, monospace); color: var(--bx-muted, #868f9a); font-size: 10.5px; }
    .code .hd { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
    .code .hd .path { font-family: var(--bx-mono, monospace); font-size: 12px; }
    .code pre { margin: 0; padding: 10px 12px; background: var(--bx-panel-2, #2b3038);
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px; overflow: auto; max-height: 70vh;
      font: 11.5px/1.5 var(--bx-mono, monospace); white-space: pre;
      color: var(--bx-text, #d4d9e0); tab-size: 4; }
    /* diff: tint add/del lines (a line is a direct-child span), keep syntax colors */
    .code pre.diff > span { display: block; }
    .code pre.diff > .d { background: color-mix(in srgb, var(--bx-green, #4caf50) 14%, transparent); }
    .code pre.diff > .a { background: color-mix(in srgb, var(--bx-red, #ef5350) 14%, transparent); }
    .code pre.diff > .h { color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 8%, transparent); }
    .code pre.diff > .fh { color: var(--bx-muted, #868f9a); }
    .grouphd { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
      color: var(--bx-muted, #868f9a); padding: 4px 8px; background: var(--bx-panel-2, #2b3038); }

    /* ---- live tile stats (resources tab) ---- */
    .strip { display: flex; gap: 10px; align-items: center; margin: 4px 0 8px; flex-wrap: wrap; }
    table.stats th.sortable { cursor: pointer; user-select: none; white-space: nowrap; }
    table.stats th.sortable:hover { color: var(--bx-text, #d4d9e0); }
    table.stats .strow { cursor: pointer; }
    table.stats .strow:hover td, table.stats .strow.on td { background: var(--bx-panel-2, #2b3038); }
    .stcell { display: flex; flex-direction: column; gap: 1px; min-width: 90px; }
    .stcell .stval { font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
    svg.spark { display: block; opacity: .85; }
    td.stbig { padding: 8px 0 10px; }
    td.stbig > .stchart { display: inline-block; margin: 0 18px 4px 0; vertical-align: top; }
    .stchart b { font-variant-numeric: tabular-nums; font-weight: 600; }
    table.stats .grouphd { padding: 4px 8px; }

    /* highlight.js — dark palette (Atom-One-Dark-ish) scoped to this shadow */
    .hljs-comment, .hljs-quote { color: #7f8896; font-style: italic; }
    .hljs-keyword, .hljs-selector-tag, .hljs-doctag, .hljs-formula { color: #c678dd; }
    .hljs-name, .hljs-section, .hljs-tag, .hljs-deletion { color: #e06c75; }
    .hljs-string, .hljs-regexp, .hljs-addition, .hljs-meta .hljs-string { color: #98c379; }
    .hljs-number, .hljs-literal, .hljs-type, .hljs-attr, .hljs-attribute,
    .hljs-variable, .hljs-template-variable, .hljs-selector-attr,
    .hljs-selector-pseudo, .hljs-selector-class { color: #d19a66; }
    .hljs-title, .hljs-title.function_, .hljs-built_in, .hljs-title.class_ { color: #61afef; }
    .hljs-symbol, .hljs-bullet, .hljs-link, .hljs-meta, .hljs-selector-id { color: #56b6c2; }
    .hljs-emphasis { font-style: italic; }
    .hljs-strong { font-weight: 600; }

    /* ---- runtime ---- */
    .hostcard { display: flex; flex-wrap: wrap; gap: 8px 18px; background: var(--bx-panel-2, #2b3038);
      border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 10px 14px; margin-bottom: 12px; }
    .hostcard .kv { font-size: 12px; }
    .hostcard .kv b { font-family: var(--bx-mono, monospace); color: var(--bx-accent, #f5a623); }
    .hostcard .kv span { color: var(--bx-muted, #868f9a); }
    .bk { border: 1px solid var(--bx-border, #363c45); border-radius: 7px; margin-bottom: 7px; overflow: hidden; }
    .bk .row { display: grid; grid-template-columns: 16px minmax(110px,1.3fr) 70px repeat(5, minmax(44px, .7fr)) 84px 1.1fr;
      gap: 8px; align-items: center; padding: 6px 10px; cursor: pointer; font-size: 12px; }
    .bk .row.rrow { grid-template-columns: minmax(150px, 2fr) 64px 78px 1fr; }
    .bk .row:hover { background: var(--bx-panel-2, #2b3038); }
    .bk .row .caret { color: var(--bx-muted, #868f9a); transition: transform .1s; }
    .bk.open .row .caret { transform: rotate(90deg); }
    .bk .p { font-family: var(--bx-mono, monospace); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .bk .num { font-family: var(--bx-mono, monospace); text-align: right; }
    .bk .hdr { text-transform: uppercase; font-size: 9.5px; letter-spacing: .05em; color: var(--bx-muted, #868f9a);
      cursor: default; background: var(--bx-panel-2, #2b3038); }
    .bk .hdr:hover { background: var(--bx-panel-2, #2b3038); }
    .state { font-size: 10px; padding: 0 6px; border-radius: 999px; border: 1px solid var(--bx-border); text-align: center; }
    .state.healthy { color: var(--bx-green, #4caf50); border-color: color-mix(in srgb, var(--bx-green) 45%, var(--bx-border)); }
    .state.building { color: var(--bx-accent, #f5a623); }
    .state.failed  { color: var(--bx-red, #ef5350); }
    .state.idle    { color: var(--bx-muted, #868f9a); }
    .lock { font-size: 11px; }
    .detail { border-top: 1px solid var(--bx-border, #363c45); padding: 8px 12px; background: var(--bx-panel-2, #2b3038);
      display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
    .detail h5 { margin: 0 0 4px; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; color: var(--bx-muted); }
    .detail .mono { font-family: var(--bx-mono, monospace); font-size: 11px; }
    .nsrow { font-size: 11px; }
    .nsrow .iso { color: var(--bx-green, #4caf50); }
    .nsrow .shared { color: var(--bx-muted, #868f9a); }
    .flowtab { width: 100%; font-size: 11px; }
    .flowtab td { padding: 1px 6px 1px 0; }

    /* per-row "more ▾" menu: a native <details>, no JS state (users table) */
    details.menu { position: relative; display: inline-block; }
    details.menu > summary { list-style: none; display: inline-block; border: 1px solid var(--bx-border, #363c45);
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0); border-radius: 5px;
      font-size: 11px; padding: 1px 8px; cursor: pointer; user-select: none; }
    details.menu > summary::-webkit-details-marker { display: none; }
    details.menu > summary:hover, details.menu[open] > summary { background: var(--bx-panel-2, #2b3038); }
    details.menu > .items { position: absolute; right: 0; top: calc(100% + 3px); z-index: 30; min-width: 14em;
      display: flex; flex-direction: column; padding: 3px; text-align: left; white-space: nowrap;
      background: var(--bx-panel, #23272e); border: 1px solid var(--bx-border, #363c45); border-radius: 6px;
      box-shadow: 0 8px 24px rgba(0, 0, 0, .18); }
    details.menu > .items button { border: 0; background: none; color: inherit; font: inherit; font-size: 12px;
      text-align: left; padding: 4px 8px; border-radius: 4px; cursor: pointer; }
    details.menu > .items button:hover { background: var(--bx-panel-2, #2b3038); }
    details.menu > .items button.rm { color: var(--bx-red, #ef5350); }
    details.menu > .items button:disabled { opacity: .45; cursor: not-allowed; }
    details.menu > .items hr { border: 0; border-top: 1px solid var(--bx-border, #363c45); margin: 3px 0; }
    /* users table */
    td.user .sub { font-size: 10.5px; color: var(--bx-muted, #868f9a); font-family: var(--bx-mono, ui-monospace, monospace); }
    .pill.off { color: var(--bx-red, #ef5350); border-color: var(--bx-red, #ef5350); }
    .pill.sync { border-style: dashed; }   /* membership / role synced from an IdP group */
    .never { color: var(--bx-amber, #f2a71b); font-weight: 600; }
    .chip .n { opacity: .7; margin-left: 3px; }
  `];

  // Two-level nav (deployments run to thousands of tiles, so the flat tab row
  // no longer scales). Primary GROUPS, each with sub-tabs; sub-tab ids stay
  // URL-safe for hash deep-links and unique across groups.
  static GROUPS = [
    { id: 'runtime', label: 'runtime', tabs: [
      { id: 'components', label: 'components' },
      { id: 'resources', label: 'resources' },
      { id: 'backup', label: 'backup' },
      { id: 'cron', label: 'cron' },
    ] },
    { id: 'usermgmt', label: 'user management', tabs: [
      { id: 'users', label: 'users' },
      { id: 'sign-in', label: 'sign-in' },
      { id: 'sessions', label: 'sessions' },
      { id: 'orgs', label: 'organisations' },
      { id: 'permsets', label: 'permission sets' },
      { id: 'netsets', label: 'network sets' },
      { id: 'map', label: 'access map' },
    ] },
    { id: 'vaultgrp', label: 'vault', tabs: [{ id: 'vault', label: 'vault' }] },
    { id: 'binding', label: 'binding', tabs: [
      { id: 'roles', label: 'roles' },
      { id: 'grants', label: 'grants' },
      { id: 'providers', label: 'interface providers' },
      { id: 'wiring', label: 'binding' },
    ] },
    { id: 'ingressgrp', label: 'ingress', tabs: [
      { id: 'endpoints', label: 'endpoints' },
      { id: 'expose', label: 'services / expose' },
    ] },
  ];
  static tabsFlat() { return BxAdmin.GROUPS.flatMap((g) => g.tabs); }
  _grpOf(tab) { return BxAdmin.GROUPS.find((g) => g.tabs.some((t) => t.id === tab)) || BxAdmin.GROUPS[0]; }

  constructor() {
    super();
    const h = location.hash.replace(/^#/, '');
    // Back-compat: honor a couple of old hash ids so bookmarks don't 404.
    const alias = { overview: 'components', runtime: 'components', interfaces: 'providers' };
    const want = alias[h] || h;
    this._tab = BxAdmin.tabsFlat().some((t) => t.id === want) ? want : 'components';
    this._err = '';
    this._alerts = [];
    this._denied = false;
    this._rtOpen = new Set();
    this._q = '';          // per-view text filter (reset on tab change)
    this._cats = new Set(); // active category chips
    this._access = {};      // per-component access relations, lazily loaded
    this._accOpen = new Set();
    this._stSort = { col: 'cpu', dir: -1 };
    this._stFilter = '';
    this._stGroup = false;
    this._stOpen = null;
  }

  _setGroup(g) { this._setTab(g.tabs[0].id); }

  _setTab(t) {
    this._tab = t;
    this._q = ''; this._cats = new Set();
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
    // Leaving/clicking the component list drops any code drill-in (back to the
    // list); _openCode re-sets _codeComp right after calling this to drill in.
    this._codeComp = null;
    if (t === 'components' || t === 'resources') this._loadRuntime();
    if (t === 'permsets' || t === 'orgs') this._loadIfaces(); // the service datalist
    if (t === 'sessions') this._loadSessions();
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.xbin?.events.on((e) => {
      // Coalesced: a bulk disable or an SSO sign-in's group sync fires a burst.
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok' || e.type === 'users') this._refreshSoon();
    });
    // Row menus are native <details>; close them on outside click / Escape.
    this._onDocDown = (e) => {
      for (const d of this.renderRoot.querySelectorAll('details.menu[open]')) {
        if (!e.composedPath().includes(d)) d.removeAttribute('open');
      }
    };
    this._onKey = (e) => { if (e.key === 'Escape') this._closeMenus(); };
    document.addEventListener('pointerdown', this._onDocDown, true);
    document.addEventListener('keydown', this._onKey);
    // Tab elements report into the router's global slots.
    this.addEventListener('bx-admin-err', (e) => { this._err = e.detail; });
    this.addEventListener('bx-admin-notice', (e) => { this._err = ''; this._flash(e.detail, 6000); });
    this.addEventListener('bx-admin-refresh', () => this._refreshSoon());
    this.addEventListener('bx-admin-tab', (e) => this._setTab(e.detail));
    this._refresh();
    // Prime the data the initial tab needs (constructor set _tab from the hash
    // but doesn't fetch; _setTab does that on later clicks).
    if (this._tab === 'components' || this._tab === 'resources') this._loadRuntime();
    if (this._tab === 'permsets' || this._tab === 'orgs') this._loadIfaces();
    if (this._tab === 'sessions') this._loadSessions();
    // Live backend/resource data: poll while a runtime-data tab is active.
    // The sessions tab polls too (logins/logouts raise no event), at 1/5 rate.
    this._rtTimer = setInterval(() => {
      if (this._tab === 'components' || this._tab === 'resources') this._loadRuntime();
      if (this._tab === 'sessions' && (this._sessTick = (this._sessTick || 0) + 1) % 5 === 0) this._loadSessions();
    }, 2000);
  }
  disconnectedCallback() {
    super.disconnectedCallback(); this._off?.(); clearInterval(this._rtTimer); clearTimeout(this._refreshT);
    document.removeEventListener('pointerdown', this._onDocDown, true);
    document.removeEventListener('keydown', this._onKey);
  }
  _refreshSoon() { clearTimeout(this._refreshT); this._refreshT = setTimeout(() => this._refresh(), 150); }
  _closeMenus() { for (const d of this.renderRoot.querySelectorAll('details.menu[open]')) d.removeAttribute('open'); }
  _menuDone(e) { e.target.closest('details')?.removeAttribute('open'); }
  // Green self-clearing notice (the red .err slot is for failures).
  _flash(msg, ms = 4000) {
    this._notice = msg; clearTimeout(this._flashT);
    this._flashT = setTimeout(() => { this._notice = ''; }, ms);
  }

  async _loadRuntime() {
    try {
      const rt = await api('/runtime');
      // Sample per-backend network totals for live rate sparklines.
      this._hist = this._hist || {};
      const now = Date.now();
      // Bus resources: ring of cumulative event counts → events/min.
      this._busRing = this._busRing || new Map();
      for (const r of rt.resources || []) {
        if (r.type !== 'bus') continue;
        const ring = this._busRing.get(r.id) || [];
        ring.push({ t: now, n: r.events || 0 });
        while (ring.length > 40) ring.shift();
        this._busRing.set(r.id, ring);
      }
      for (const b of rt.backends || []) {
        const a = b.activity;
        const h = (this._hist[b.path] = this._hist[b.path] || []);
        h.push({ t: now, tx: a ? a.txBytes : 0, rx: a ? a.rxBytes : 0 });
        if (h.length > 40) h.shift();
      }
      this._rt = rt;
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // rateSeries turns cumulative byte samples into a bytes/sec series.
  _rateSeries(path) {
    const h = (this._hist || {})[path] || [];
    const out = [];
    for (let i = 1; i < h.length; i++) {
      const dt = (h[i].t - h[i - 1].t) / 1000 || 1;
      out.push(Math.max(0, (h[i].tx + h[i].rx - h[i - 1].tx - h[i - 1].rx) / dt));
    }
    return out;
  }
  _spark(path, w = 76, ht = 16) {
    const s = this._rateSeries(path);
    if (s.length < 2 || Math.max(...s) === 0) return html`<span class="muted" style="font-size:10px">idle</span>`;
    const max = Math.max(...s);
    const step = w / (s.length - 1);
    const pts = s.map((v, i) => `${(i * step).toFixed(1)},${(ht - (v / max) * (ht - 2) - 1).toFixed(1)}`).join(' ');
    const peak = fmtBytes(max) + '/s';
    return html`<svg width=${w} height=${ht} viewBox="0 0 ${w} ${ht}" title=${'peak ' + peak}>
      <polyline points=${pts} fill="none" stroke="var(--bx-accent,#f5a623)" stroke-width="1.2"></polyline></svg>`;
  }

  async _refresh() {
    try {
      const [ov, vaults, cron, users, authSettings, vaultStatus, alerts, orgs, wsPolicy, permsets, defaults, reqs, sessions, netsets] = await Promise.all([
        api('/auth-overview'),
        api('/vaults').catch(() => null), // 503 while the barrier is sealed
        api('/cron/jobs'),
        api('/users').catch(() => ({ users: [] })),
        api('/auth-settings').catch(() => null),
        api('/vault-status').catch(() => null),
        api('/alerts').catch(() => ({ alerts: [] })),
        api('/orgs').catch(() => ({ orgs: [] })),
        api('/policy').catch(() => ({ policy: [] })),
        api('/permission-sets').catch(() => ({ sets: {}, attachedTo: {} })),
        api('/defaults').catch(() => ({ defaultTiles: {} })),
        api('/access-requests').catch(() => ({ requests: [] })),
        api('/sessions').catch(() => ({ sessions: [] })),
        api('/net-sets').catch(() => ({ sets: {}, attachedTo: {} })),
      ]);
      this._ov = ov; this._vaults = vaults; this._cron = cron.jobs ?? [];
      this._alerts = alerts.alerts ?? [];
      this._users = users.users ?? [];
      this._orgs = orgs.orgs ?? [];
      this._wsPolicy = wsPolicy.policy ?? [];
      this._permsets = permsets; this._netsets = netsets; this._defaults = defaults.defaultTiles ?? {};
      this._newUsers = defaults.newUsers ?? {}; this._tileCreation = defaults.tileCreation ?? 'any';
      this._reqs = reqs.requests ?? [];
      this._sessions = sessions.sessions ?? [];
      this._authSettings = authSettings; this._vaultStatus = vaultStatus;
      this._err = ''; this._denied = false;
      this.renderRoot.querySelector('bx-admin-map')?.refresh(); // keep the matrix current
    } catch (e) {
      if (String(e.message).includes('admin')) this._denied = true;
      else this._err = String(e.message ?? e);
    }
  }

  // ---- code & history (a drill-in from the overview; no separate tab) ----
  async _openCode(comp) {
    this._setTab('overview');
    this._codeComp = comp; this._codeFile = null; this._codeDiff = null; this._codeMode = 'file';
    await this._loadCode();
    const files = this._codeTree?.files ?? [];
    const def = files.find((f) => f.path === 'index.html') || files.find((f) => f.path === 'xbin.json') || files[0];
    if (def) this._loadFile(def.path);
  }
  async _loadCode() {
    const c = encodeURIComponent(this._codeComp);
    try {
      const [tree, log] = await Promise.all([api(`/code/tree?component=${c}`), api(`/git/log?component=${c}`)]);
      this._codeTree = tree; this._codeLog = log;
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _loadFile(path) {
    try {
      this._codeFile = await api(`/code/file?component=${encodeURIComponent(this._codeComp)}&file=${encodeURIComponent(path)}`);
      this._codeMode = 'file';
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _loadDiff(rev) {
    try {
      const d = await api(`/git/diff?component=${encodeURIComponent(this._codeComp)}&rev=${encodeURIComponent(rev)}`);
      this._codeDiff = { rev, diff: d.diff ?? '' }; this._codeMode = 'diff';
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _fmtDate(iso) { try { return new Date(iso).toLocaleDateString(); } catch { return iso; } }

  _codeView() {
    if (!this._codeComp) return this._componentsView(); // reached only defensively; the list is the picker
    const tree = this._codeTree?.files ?? [];
    const log = this._codeLog?.commits ?? [];
    const noRepo = this._codeLog?.repo === false;
    return html`
      <div class="hd">
        <a class="link" @click=${() => { this._codeComp = null; }}>← components</a>
        <span class="path">${this._codeComp}</span>
        ${this._codeLog?.remote ? html`<span class="muted" style="font-size:11px" title="git remote (origin)">${this._codeLog.remote.replace(/^https:\/\/|\.git$/g, '')}</span>` : nothing}
      </div>
      <div class="code">
        <div class="side">
          <div class="files">
            <div class="grouphd">files</div>
            ${tree.length ? tree.map((f) => html`
              <div class="row ${this._codeMode === 'file' && this._codeFile?.path === f.path ? 'on' : ''}"
                   @click=${() => this._loadFile(f.path)}>${f.path}</div>`)
              : html`<div class="row muted">—</div>`}
          </div>
          <div class="hist">
            <div class="grouphd">history</div>
            <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === '' ? 'on' : ''}"
                 @click=${() => this._loadDiff('')}>● uncommitted changes</div>
            ${noRepo ? html`<div class="row muted">not a git repo</div>`
              : log.length ? log.map((c) => html`
                <div class="row ${this._codeMode === 'diff' && this._codeDiff?.rev === c.hash ? 'on' : ''}"
                     @click=${() => this._loadDiff(c.hash)}>
                  <div>${c.subject}</div><div class="s">${c.short} · ${c.author} · ${this._fmtDate(c.date)}</div>
                </div>`)
              : html`<div class="row muted">no commits touch this component</div>`}
          </div>
        </div>
        <div class="main">
          ${this._codeMode === 'diff'
            ? html`<pre class="diff hljs">${unsafeHTML(diffHTML(this._codeDiff?.diff))}</pre>`
            : this._codeFile
              ? (this._codeFile.binary ? html`<span class="muted">binary file (${this._codeFile.size} bytes)</span>`
                : this._codeFile.truncated ? html`<span class="muted">file too large to display (${this._codeFile.size} bytes)</span>`
                : html`<pre class="hljs"><code>${unsafeHTML(hl(this._codeFile.content ?? '', langFor(this._codeFile.path ?? '')))}</code></pre>`)
              : html`<span class="muted">select a file or a commit</span>`}
        </div>
      </div>`;
  }

  render() {
    if (this._denied) return html`<div class="denied">
      <b>No admin access.</b> This tile needs the <code>xbin:admin</code> grant.
      Approve <span class="mono">tiles/admin → xbin : admin</span> in the grants
      panel, or run <code>bx grant tiles/admin xbin:admin</code>.
      See <a href="/docs/auth.md" target="_blank">docs/auth.md</a>.</div>`;
    const tab = this._tab;
    const grp = this._grpOf(tab);
    return html`
      ${(this._alerts || []).length ? html`<div class="alertbar">
        ${this._alerts.map((a) => html`<div class="al ${a.level}">
          <b>${a.level === 'crit' ? '\u26A0' : '\u26A1'}</b> ${a.message}</div>`)}
      </div>` : nothing}
      <div class="groups">
        ${BxAdmin.GROUPS.map((g) => html`
          <button class=${g.id === grp.id ? 'on' : ''} @click=${() => this._setGroup(g)}>${g.label}</button>`)}
      </div>
      ${grp.tabs.length > 1 ? html`<div class="tabs sub">
        ${grp.tabs.map((t) => html`
          <button class=${t.id === tab ? 'on' : ''} @click=${() => this._setTab(t.id)}>${t.label}</button>`)}
      </div>` : nothing}
      <div class="body">
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${this._notice ? html`<div class="notice">${this._notice}</div>` : nothing}
        ${tab === 'users' ? this._usersView()
          : tab === 'sign-in' ? this._signInView()
          : tab === 'sessions' ? this._sessionsView()
          : tab === 'orgs' ? html`<bx-admin-orgs .orgs=${this._orgs} .users=${this._users} .wsPolicy=${this._wsPolicy}
              .permsets=${this._permsets} .netsets=${this._netsets} .defaults=${this._defaults} .newUsers=${this._newUsers}
              .tileCreation=${this._tileCreation} .authSettings=${this._authSettings}
              .targets=${this._targetOptions()} .services=${serviceOptions(this._ifaces)}></bx-admin-orgs>`
          : tab === 'permsets' ? html`<bx-admin-permsets .permsets=${this._permsets} .orgs=${this._orgs}
              .targets=${this._targetOptions()} .services=${serviceOptions(this._ifaces)}></bx-admin-permsets>`
          : tab === 'netsets' ? html`<bx-admin-netsets .netsets=${this._netsets} .targets=${this._targetOptions()}></bx-admin-netsets>`
          : tab === 'map' ? html`<bx-admin-map .users=${this._users} .orgs=${this._orgs} .wsPolicy=${this._wsPolicy}
              .showHidden=${this._showHidden} @bx-admin-show-hidden=${(e) => { this._showHidden = e.detail; }}></bx-admin-map>`
          : tab === 'components' ? (this._codeComp ? this._codeView() : this._componentsView())
          : tab === 'resources' ? this._resourcesView()
          : tab === 'vault' ? html`<bx-admin-vault .vaults=${this._vaults} .vaultStatus=${this._vaultStatus} .components=${this._ov?.components ?? []}></bx-admin-vault>`
          : ['roles', 'grants', 'providers', 'wiring'].includes(tab) ? html`<bx-admin-binding view=${tab} .ov=${this._ov} .orgs=${this._orgs}></bx-admin-binding>`
          : tab === 'endpoints' || tab === 'expose' ? html`<bx-admin-ingress view=${tab}></bx-admin-ingress>`
          : tab === 'backup' ? html`<bx-admin-backup .components=${this._ov?.components ?? []}></bx-admin-backup>`
          : html`<bx-admin-cron .cron=${this._cron}></bx-admin-cron>`}
      </div>`;
  }

  // ---- shared filter primitive (list views scale to 1000s of tiles) ----
  // _match tests any of the given strings against the current query (case-
  // insensitive substring). _filterBar renders the query input + optional
  // category chips + a result count; chips are toggled in this._cats.
  _match(...parts) {
    const q = (this._q || '').trim().toLowerCase();
    if (!q) return true;
    return parts.some((p) => (p || '').toString().toLowerCase().includes(q));
  }
  _catActive(c) { return this._cats.size === 0 || this._cats.has(c); }
  _toggleCat(c) {
    const s = new Set(this._cats); s.has(c) ? s.delete(c) : s.add(c); this._cats = s;
  }
  _filterBar(placeholder, cats, shown, total) {
    return html`<div class="filterbar">
      <input class="q" type="search" placeholder=${placeholder} .value=${this._q}
             @input=${(e) => { this._q = e.target.value; }}>
      ${cats && cats.length ? html`<div class="chips">
        ${cats.map((c) => html`<span class="chip ${this._cats.has(c) ? 'on' : ''}"
          @click=${() => this._toggleCat(c)}>${c}</span>`)}
      </div>` : nothing}
      ${total != null ? html`<span class="count-note">${shown}/${total}</span>` : nothing}
    </div>`;
  }

  // The scope/org category a component path falls in (for chips): its org
  // (o/<org>), else its top-level segment ("apps", "lib", \u2026).
  _catOf(path) {
    const s = (path || '').split('/');
    if (s[0] === 'o' && s[1]) return 'org:' + s[1];
    if (s[1] === 'o' && s[2]) return 'org:' + s[2];
    return s[0] || '\u2014';
  }

  // ---- runtime ----
  _toggleBk(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
  }
  _mem(b) {
    if (b.cgroup && b.cgroup.memCurrent) return fmtBytes(b.cgroup.memCurrent);
    if (b.rssKb) return fmtBytes(b.rssKb * 1024);
    return '—';
  }
  _flowTime(f) {
    const ageS = Math.max(0, (Date.now() - f.start) / 1000);
    const age = ageS < 60 ? (ageS | 0) + 's ago' : fmtDur(ageS) + ' ago';
    if (!f.end) return age + ' · open';
    const dur = (f.end - f.start) / 1000;
    return age + (dur >= 0.05 ? ' · ' + dur.toFixed(1) + 's' : '');
  }

  // Terminal secret-mask guards (docs/isolation.md): mount guard = seccomp
  // (masks can't be umounted), read guard = Landlock (secret files can't be
  // read even if a mask is peeled). Green when the kernel supports each.
  _guardStatus(p) {
    p = p || {};
    const mark = (on) => (on ? '✓' : '✗');
    const land = p.landlock ? `✓ (ABI ${p.landlockAbi})` : '✗';
    return html`<span title="seccomp mount guard · Landlock read guard"
      >mount ${mark(p.seccomp)} · read ${land}</span>`;
  }

  // One tab per resource type; per-type columns (bus: live events/min from
  // the cumulative counter sampled every poll — see _loadRuntime).
  static resTypeOrder = ['filesystem', 'sqlite', 'kv', 'blob', 'bus', 'cron'];

  _busRate(id) {
    const ring = this._busRing?.get(id);
    if (!ring || ring.length < 2) return null;
    // events in the trailing ≤60s window, scaled to a minute
    const last = ring[ring.length - 1];
    let first = ring[0];
    for (const s of ring) { if (last.t - s.t <= 65000) { first = s; break; } }
    const dtMin = (last.t - first.t) / 60000;
    if (dtMin <= 0) return null;
    return Math.max(0, (last.n - first.n) / dtMin);
  }

  _resourcesSection(resources) {
    if (!resources || !resources.length) return nothing;
    const types = BxAdmin.resTypeOrder.filter((t) => resources.some((r) => r.type === t))
      .concat([...new Set(resources.map((r) => r.type))].filter((t) => !BxAdmin.resTypeOrder.includes(t)));
    const active = types.includes(this._resType) ? this._resType : types[0];
    const rows = resources.filter((r) => r.type === active);
    const cols = active === 'bus' ? ['id', 'events/min', 'events total']
      : active === 'cron' ? ['id', 'jobs']
      : active === 'kv' ? ['id', 'size', 'keys']
      : ['id', 'size', 'detail'];
    const cell = (r, c) => {
      switch (c) {
        case 'id': return html`<span class="p" title=${r.id}>${r.id}</span>`;
        case 'size': return html`<span class="num">${r.size ? fmtBytes(r.size) : '—'}</span>`;
        case 'events/min': { const v = this._busRate(r.id);
          return html`<span class="num">${v == null ? '…' : v < 10 ? v.toFixed(1) : Math.round(v)}</span>`; }
        case 'events total': return html`<span class="num">${r.events || 0}</span>`;
        default: return html`<span class="muted">${r.detail || ''}</span>`;
      }
    };
    return html`
      <h4>resources</h4>
      <div class="strip" style="gap:2px">
        ${types.map((t) => html`<button class="act ${t === active ? 'on' : ''}"
          style=${t === active ? 'font-weight:600' : ''}
          @click=${() => { this._resType = t; }}>${t}
          <span class="muted">${resources.filter((r) => r.type === t).length}</span></button>`)}
      </div>
      <div class="bk"><div class="row hdr rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => html`<span class=${c === 'id' ? '' : 'num'}>${c}</span>`)}</div></div>
      ${rows.map((r) => html`<div class="bk"><div class="row rrow" style="grid-template-columns: 1fr ${cols.slice(1).map(() => '110px').join(' ')}">
        ${cols.map((c) => cell(r, c))}
      </div></div>`)}`;
  }

  _bkDetail(b) {
    const act = b.activity;
    return html`<div class="detail">
      <div>
        <h5>process</h5>
        <div class="mono">runtime ${b.runtime || 'static'} · gen ${b.gen} · up ${fmtDur(b.uptimeSec)}</div>
        <div class="mono">threads ${b.threads || '—'} · restarts ${b.restarts} · last req ${b.lastReqSec < 0 ? 'never' : fmtDur(b.lastReqSec) + ' ago'}</div>
        ${b.cgroup ? html`<div class="mono">cgroup: ${fmtBytes(b.cgroup.memCurrent)}${b.cgroup.memMax > 0 ? ' / ' + fmtBytes(b.cgroup.memMax) : ''} · cpu ${(b.cgroup.cpuUsec / 1e6).toFixed(1)}s · ${b.cgroup.pidsCurrent} pid(s)</div>` : nothing}
        ${b.error ? html`<div class="err-pill">${b.error}</div>` : nothing}
      </div>
      <div>
        <h5>namespaces</h5>
        ${b.namespaces
          ? Object.entries(b.namespaces).map(([k, v]) => html`<div class="nsrow">${k}: <span class=${v.isolated ? 'iso' : 'shared'}>${v.isolated ? 'isolated' : 'shared'}</span> <span class="muted mono">${v.id}</span></div>`)
          : html`<span class="muted">shared with host (not sandboxed)</span>`}
      </div>
      <div>
        <h5>egress ${act ? html`· ${fmtBytes(act.txBytes)}↑ ${fmtBytes(act.rxBytes)}↓ · ${act.active} active` : nothing}</h5>
        ${b.netRef ? html`<div class="mono" style="font-size:11px">net ${b.netRef === 'org'
            ? html`<span class="pill" title=${(b.netRules ?? []).join('\n') || 'org network (no relay rules)'}>🏢 ${b.netSource || 'org network'}</span>`
            : b.netRef}${b.net ? html` <span class="muted">· ${b.net}</span>` : nothing}</div>` : nothing}
        ${b.netNote ? html`<div class="warn-line">⚠ ${b.netNote}</div>` : nothing}
        ${(b.egress && b.egress.length) ? html`<div class="mono">${b.egress.join(', ')}</div>` : html`<span class="muted">${b.isolated ? 'no egress granted (deny-all)' : 'unrestricted (host network)'}</span>`}
        ${act && act.recent && act.recent.length ? html`
          <table class="flowtab"><tbody>
            ${act.recent.slice(0, 12).map((f) => html`<tr>
              <td class=${f.allowed ? 'flow-allow' : 'flow-deny'}>${f.allowed ? '✓' : '⛔'}</td>
              <td class="mono">${f.proto} ${f.dst}:${f.port}</td>
              <td class="mono">${fmtBytes(f.txBytes)}↑ ${fmtBytes(f.rxBytes)}↓</td>
              <td class="muted">${this._flowTime(f)}</td>
            </tr>`)}
          </tbody></table>` : nothing}
      </div>
    </div>`;
  }

  // Vault banner at the top of the overview: unmissable when the barrier is
  // sealed/unconfigured (stateful components are held), quiet when healthy.
  _vaultBanner() {
    const st = this._vaultStatus;
    if (!st) return nothing;
    const goVault = () => this._setTab('vault');
    if (st.mode === 'sealed') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to unseal">
        🔒 VAULT SEALED — encrypted resources are unmounted and stateful components are HELD.
        Click to unseal.</div>`;
    }
    if (st.mode === 'unconfigured') {
      return html`<div class="vault-banner sealed" @click=${goVault}
        title="open the vault tab to set a passphrase">
        🔒 VAULT UNCONFIGURED — secret & resource storage is refused until a passphrase is set.
        Click to set one.</div>`;
    }
    if (st.mode === 'plaintext') {
      return html`<div class="vault-banner warn" @click=${goVault}>
        ⚠ vault: plaintext at rest (dev mode) — click to encrypt.</div>`;
    }
    return html`<div class="vault-banner ok">vault unsealed — encryption at rest active</div>`;
  }

  // ---- components (runtime → components): the tile roster ----
  // Merges the manifest/principal view (_ov) with live backend state (_rt) and
  // adds per-component access relations on expand. Filterable + category-chipped
  // so it scales to thousands of tiles.
  _bkByPath() {
    const m = {};
    for (const b of (this._rt?.backends ?? [])) m[b.path] = b;
    return m;
  }
  _toggleComp(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
    if (s.has(path) && this._access[path] === undefined) this._loadAccess(path);
  }
  async _loadAccess(path) {
    this._access = { ...this._access, [path]: null }; // mark loading
    try {
      const d = await api('/access?tile=' + encodeURIComponent(path));
      this._access = { ...this._access, [path]: d };
    } catch (e) { this._access = { ...this._access, [path]: { error: String(e.message ?? e) } }; }
  }

  _componentsView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    const bk = this._bkByPath();
    const all = ov.components ?? [];
    const cats = [...new Set(all.map((k) => this._catOf(k.path)))].sort();
    const rows = all.filter((k) => this._catActive(this._catOf(k.path)) &&
      this._match(k.path, k.runtime, (k.uses ?? []).map((u) => u.target).join(' ')));
    const hiddenN = rows.filter((k) => k.state === 'hidden').length;
    const live = rows.filter((k) => !this._isOffloaded(k) && (this._showHidden || k.state !== 'hidden'));
    const off = rows.filter((k) => this._isOffloaded(k));
    return html`
      ${this._vaultBanner()}
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      ${this._filterBar('filter tiles by path, runtime or use…', cats, rows.length, all.length)}
      ${hiddenN ? html`<label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center;margin:2px 0 6px">
        <input type="checkbox" .checked=${!!this._showHidden}
          @change=${(e) => { this._showHidden = e.target.checked; }}> show hidden (${hiddenN})</label>` : nothing}
      <table>
        <tr><th></th><th>component</th><th>runtime</th><th>state</th><th>exposes</th><th>uses</th><th>vault</th><th>lifecycle</th></tr>
        ${live.map((k) => this._compRow(k, bk[k.path]))}
        ${live.length === 0 ? html`<tr><td></td><td class="muted" colspan="7">no matching components</td></tr>` : nothing}
      </table>
      ${off.length ? html`<h4>offloaded <span class="muted" style="font-weight:400;text-transform:none;letter-spacing:0">— archived, not running</span></h4>
        <table>
          <tr><th>component</th><th>state</th><th></th></tr>
          ${off.map((k) => html`<tr>
            <td class="mono">${k.path}</td>
            <td><span class="pill st-failed">${k.state}</span></td>
            <td style="text-align:right"><a class="link" @click=${() => this._setTab('backup')}>restore in Backup →</a></td>
          </tr>`)}
        </table>` : nothing}`;
  }

  _compRow(k, b) {
    const open = this._rtOpen.has(k.path);
    const state = b ? html`<span class="state ${b.state}">${b.state}</span>${b.isolated ? html` <span class="lock" title="sandboxed">🔒</span>` : nothing}`
      : html`<span class="muted">${(k.runtime && k.runtime !== 'static') ? 'idle' : 'static'}</span>`;
    return html`
      <tr>
        <td><span class="caret ${open ? 'o' : ''}" style="cursor:pointer" @click=${() => this._toggleComp(k.path)}>▶</span></td>
        <td class="mono"><a class="link" @click=${() => this._openCode(k.path)} title="view code & history">${k.path}</a>${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
        <td class="muted">${k.runtime || 'static'}</td>
        <td>${state}</td>
        <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
        <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
        <td>${k.hasVault ? '🔑' : ''}</td>
        <td>${this._lifecycleCell(k)}</td>
      </tr>
      ${open ? html`<tr><td></td><td colspan="7">${this._compDetail(k, b)}</td></tr>` : nothing}`;
  }

  // Component detail (expanded): who can reach it (access relations) + live
  // backend runtime. The access relations are the "user relations" view — the
  // reverse of the access map, per tile.
  _compDetail(k, b) {
    const acc = this._access[k.path];
    return html`<div class="detail" style="grid-template-columns:1fr">
      <div>
        <h5>access — who can reach this tile</h5>
        ${acc === undefined || acc === null ? html`<span class="muted">loading…</span>`
          : acc.error ? html`<span class="err-pill">${acc.error}</span>`
          : html`
            ${acc.org ? html`<div class="mono" style="font-size:11px;margin-bottom:3px">org: ${acc.org}</div>` : nothing}
            ${(acc.entries ?? []).length === 0 ? html`<span class="muted">no users or teams have access (admins always do)</span>` : html`
            <table class="tbl"><tr><th>who</th><th>level</th><th>via</th></tr>
              ${acc.entries.map((e) => html`<tr>
                <td>${e.kind === 'team' ? '👥' : '👤'} <span class="mono">${e.id}</span></td>
                <td><span class="pill">${e.level}</span></td>
                <td class="muted">${e.source}</td></tr>`)}
            </table>`}
            <a class="link" @click=${() => this._setTab('map')}>full access map →</a>`}
      </div>
      ${b ? html`<div style="margin-top:8px"><h5>runtime</h5>${this._bkDetail(b)}</div>` : nothing}
    </div>`;
  }

  _isOffloaded(k) { return k.state === 'offloaded' || k.state === 'offloaded-full'; }

  // ---- live per-tile stats (runtime → resources) --------------------------
  // CPU / memory / I/O rates sampled by xbind (cgroup leaves, /proc fallback;
  // internal/runner/stats.go), polled with the rest of /runtime every 2s.

  // Multi-line SVG sparkline over a stats series. keys/colors pick up to two
  // fields of each point; scaled to the window max (shared across lines).
  _stSpark(series, keys, colors, w = 84, ht = 18) {
    if (!series || series.length < 2) return html`<span class="muted" style="font-size:10px">—</span>`;
    let max = 0;
    for (const p of series) for (const k of keys) max = Math.max(max, p[k] || 0);
    const step = w / (series.length - 1);
    const pts = (k) => series.map((p, i) =>
      `${(i * step).toFixed(1)},${(ht - (max ? ((p[k] || 0) / max) : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ');
    const p1 = pts(keys[0]);
    const p2 = keys[1] ? pts(keys[1]) : '';
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      <polyline points=${p1} fill="none" stroke=${colors[0]} stroke-width="1.2"></polyline>
      <polyline points=${p2} fill="none" stroke=${colors[1] || 'none'} stroke-width="1.2"></polyline>
    </svg>`;
  }

  // One metric cell: current value over its sparkline.
  _stCell(t, keys, colors, fmt) {
    return html`<div class="stcell">
      <span class="stval">${fmt(t.cur)}</span>
      ${this._stSpark(t.series, keys, colors)}
    </div>`;
  }

  // Sort on a ~1-minute moving average (last 30 points at 2s), not the
  // instantaneous sample — otherwise rows reshuffle on every poll.
  _stAvg(t, keys) {
    const s = t.series || [];
    const tail = s.slice(-30);
    if (!tail.length) return 0;
    let sum = 0;
    for (const p of tail) for (const k of keys) sum += p[k] || 0;
    return sum / tail.length;
  }

  _stSortKey(t) {
    switch (this._stSort.col) {
      case 'tile': return t.path;
      case 'org': return t.owner || '';
      case 'mem': return this._stAvg(t, ['mem']);
      case 'io': return this._stAvg(t, ['rbps', 'wbps']);
      case 'iops': return this._stAvg(t, ['riops', 'wiops']);
      case 'pids': return this._stAvg(t, ['pids']);
      default: return this._stAvg(t, ['cpu']);
    }
  }

  _stTh(col, label, title) {
    const s = this._stSort;
    return html`<th class="sortable" title=${title || ''}
      @click=${() => { this._stSort = { col, dir: s.col === col ? -s.dir : (col === 'tile' || col === 'org' ? 1 : -1) }; }}>
      ${label}${s.col === col ? (s.dir > 0 ? ' ▲' : ' ▼') : ''}</th>`;
  }

  // The four chart specs shared by cells and the expanded view. I/O series
  // are syscall-level (all file activity incl. FUSE-backed resources).
  static stMetrics = [
    { label: 'cpu', keys: ['cpu'], colors: ['var(--bx-accent,#f5a623)'], fmt: (c) => `${(c.cpu || 0).toFixed(1)}%` },
    { label: 'mem', keys: ['mem'], colors: ['var(--bx-green, #4caf50)'], fmt: (c, el) => fmtBytes(c.mem || 0) },
    { label: 'i/o r+w', keys: ['rbps', 'wbps'], colors: ['#5b8def', 'var(--bx-red, #ef5350)'], fmt: (c, el) => `${fmtBytes(c.rbps || 0)}/s · ${fmtBytes(c.wbps || 0)}/s` },
    { label: 'iops r+w', keys: ['riops', 'wiops'], colors: ['#5b8def', 'var(--bx-red, #ef5350)'], fmt: (c) => `${Math.round(c.riops || 0)} · ${Math.round(c.wiops || 0)}` },
  ];

  // N-line sparkline over precomputed numeric arrays (shared max). Used by
  // the totals charts, where one line per org can exceed _stSpark's two.
  _stSparkN(lines, w = 220, ht = 44) {
    const len = Math.max(0, ...lines.map((l) => l.vals.length));
    if (len < 2) return html`<span class="muted" style="font-size:10px">gathering…</span>`;
    let max = 0;
    for (const l of lines) for (const v of l.vals) max = Math.max(max, v);
    const step = w / (len - 1);
    return html`<svg class="spark" width=${w} height=${ht} viewBox="0 0 ${w} ${ht}">
      ${lines.map((l) => svg`<polyline fill="none" stroke=${l.color} stroke-width="1.3"
        points=${l.vals.map((v, i) => `${((i + (len - l.vals.length)) * step).toFixed(1)},${(ht - (max ? v / max : 0) * (ht - 2) - 1).toFixed(1)}`).join(' ')}></polyline>`)}
    </svg>`;
  }

  static orgPalette = ['#5b8def', '#43a047', '#f5a623', '#e5484d', '#9c27b0',
    '#00acc1', '#8d6e63', '#7cb342'];

  // Workspace totals: each metric summed across tiles point-by-point
  // (series share the sampler's cadence; aligned on the tail). "by org"
  // splits the sum into one line per owner.
  _stTotals(stats) {
    const tiles = stats?.tiles ?? [];
    if (!tiles.length) return nothing;
    const M = BxAdmin.stMetrics;
    const val = (p, keys) => keys.reduce((s, k) => s + (p[k] || 0), 0);
    const sum = (list, keys) => {
      const len = Math.max(0, ...list.map((t) => (t.series || []).length));
      const out = new Array(len).fill(0);
      for (const t of list) {
        const ser = t.series || [];
        for (let i = 0; i < ser.length; i++) out[len - ser.length + i] += val(ser[i], keys);
      }
      return out;
    };
    let orgs = null;
    if (this._stTotOrg) {
      const buckets = new Map();
      for (const t of tiles) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      orgs = [...buckets.entries()];
    }
    const chart = (m) => {
      const totalNow = tiles.reduce((s, t) => s + val(t.cur || {}, m.keys), 0);
      const lines = orgs
        ? orgs.map(([org, list], i) => ({ org, color: BxAdmin.orgPalette[i % BxAdmin.orgPalette.length], vals: sum(list, m.keys) }))
        : [{ color: m.colors[0], vals: sum(tiles, m.keys) }];
      return html`<div class="stchart">
        <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
          <b style="text-transform:none;letter-spacing:0"> ${m.label === 'cpu' ? `${totalNow.toFixed(1)}%`
            : m.label.startsWith('iops') ? `${Math.round(totalNow)}/s`
            : `${fmtBytes(totalNow)}${m.label === 'mem' ? '' : '/s'}`}</b></div>
        ${this._stSparkN(lines)}
      </div>`;
    };
    return html`
      <h4 style="display:flex;align-items:center;gap:12px">workspace totals
        <label class="muted" style="font-size:11px;font-weight:400;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stTotOrg}
            @change=${(e) => { this._stTotOrg = e.target.checked; }}>
          by org</label></h4>
      <div class="stbig" style="padding:4px 0 2px">${M.map(chart)}</div>
      ${orgs && orgs.length > 1 ? html`<div class="strip" style="gap:10px;flex-wrap:wrap">
        ${orgs.map(([org], i) => html`<span class="muted mono" style="font-size:10.5px">
          <span style="display:inline-block;width:9px;height:9px;border-radius:2px;background:${BxAdmin.orgPalette[i % BxAdmin.orgPalette.length]}"></span>
          ${org}</span>`)}
      </div>` : nothing}`;
  }

  _liveStatsSection(stats) {
    const tiles = stats?.tiles ?? [];
    const M = BxAdmin.stMetrics;
    const f = (this._stFilter || '').trim();
    let rows = f ? tiles.filter((t) => t.path.startsWith(f) || (t.owner || '').startsWith(f)) : tiles.slice();
    const dir = this._stSort.dir;
    rows.sort((a, b) => {
      const ka = this._stSortKey(a); const kb = this._stSortKey(b);
      const c = typeof ka === 'string' ? ka.localeCompare(kb) : ka - kb;
      return c ? c * dir : a.path.localeCompare(b.path);
    });

    const head = html`<tr>
      ${this._stTh('tile', 'tile')}
      ${this._stGroup ? nothing : this._stTh('org', 'owner')}
      ${this._stTh('cpu', 'cpu %')}
      ${this._stTh('mem', 'memory')}
      ${this._stTh('io', 'i/o', 'read + write, syscall-level (includes resource/FUSE I/O)')}
      ${this._stTh('iops', 'iops', 'read + write ops/s')}
      ${this._stTh('pids', 'pids')}
    </tr>`;

    const row = (t) => {
      const open = this._stOpen === t.path;
      return html`<tr class="strow ${open ? 'on' : ''}" @click=${() => { this._stOpen = open ? null : t.path; }}>
        <td class="mono">${t.path}</td>
        ${this._stGroup ? nothing : html`<td class="muted mono" style="font-size:11px">${t.owner || '—'}</td>`}
        <td>${this._stCell(t, M[0].keys, M[0].colors, (c) => M[0].fmt(c, this))}</td>
        <td>${this._stCell(t, M[1].keys, M[1].colors, (c) => M[1].fmt(c, this))}</td>
        <td>${this._stCell(t, M[2].keys, M[2].colors, (c) => M[2].fmt(c, this))}</td>
        <td>${this._stCell(t, M[3].keys, M[3].colors, (c) => M[3].fmt(c, this))}</td>
        <td>${t.cur?.pids || 0}</td>
      </tr>
      ${open ? html`<tr><td colspan=${this._stGroup ? 6 : 7} class="stbig">
        ${M.map((m) => html`<div class="stchart">
          <div class="muted" style="font-size:10px;text-transform:uppercase;letter-spacing:.06em">${m.label}
            <b style="text-transform:none;letter-spacing:0"> ${m.fmt(t.cur || {}, this)}</b></div>
          ${this._stSpark(t.series, m.keys, m.colors, 300, 56)}
        </div>`)}
      </td></tr>` : nothing}`;
    };

    // Group by org: bucket per owner ref ("org:…", "user:…", or workspace).
    let body;
    if (this._stGroup) {
      const buckets = new Map();
      for (const t of rows) {
        const k = t.owner || 'workspace';
        if (!buckets.has(k)) buckets.set(k, []);
        buckets.get(k).push(t);
      }
      const agg = (list, key) => list.reduce((s, t) => s + (this._aggVal(t, key)), 0);
      body = [...buckets.entries()].map(([org, list]) => html`
        <tr><td colspan="6" class="grouphd mono">${org} <span style="float:right;font-weight:400">
          ${list.length} tile${list.length === 1 ? '' : 's'} · ${agg(list, 'cpu').toFixed(1)}% ·
          ${fmtBytes(agg(list, 'mem'))} · ${fmtBytes(agg(list, 'io'))}/s</span></td></tr>
        ${list.map(row)}`);
    } else {
      body = rows.map(row);
    }

    return html`
      <h4>live tiles</h4>
      <div class="strip">
        <input placeholder="filter by name prefix…" .value=${this._stFilter}
          @input=${(e) => { this._stFilter = e.target.value; }} style="width:200px">
        <label class="muted" style="font-size:11px;display:inline-flex;gap:5px;align-items:center">
          <input type="checkbox" .checked=${this._stGroup}
            @change=${(e) => { this._stGroup = e.target.checked; this._stSort = this._stGroup && this._stSort.col === 'org' ? { col: 'cpu', dir: -1 } : this._stSort; }}>
          group by org</label>
        ${stats && !stats.cgroup ? html`<span class="muted" style="font-size:10.5px" title="run under the installed service (systemd Delegate=yes) for exact whole-tree accounting">process-tree sampling</span>` : nothing}
      </div>
      ${rows.length === 0 ? html`<p class="muted">${tiles.length === 0
        ? 'no running backends — live stats appear when a tile’s backend runs.'
        : 'no tiles match the filter.'}</p>`
      : html`<table class="stats">${head}${body}</table>`}`;
  }

  _aggVal(t, key) {
    const c = t.cur || {};
    if (key === 'io') return (c.rbps || 0) + (c.wbps || 0);
    return c[key] || 0;
  }

  // ---- resources (runtime → resources): host health + brokered state ----
  _resourcesView() {
    const rt = this._rt; if (!rt) return html`<span class="muted">loading…</span>`;
    const h = rt.host || {};
    const kv = (label, val) => html`<div class="kv"><span>${label}</span> <b>${val}</b></div>`;
    return html`
      <div class="hostcard">
        ${kv('xbind', h.version)}
        ${kv('kernel', h.kernel || '—')}
        ${kv('pid', h.pid)}
        ${kv('euid', h.uid)}
        ${kv('cpus', h.numCPU)}
        ${kv('goroutines', h.goroutines)}
        ${kv('heap', fmtBytes((h.heapMB || 0) * 1e6))}
        ${kv('uptime', fmtDur(h.uptimeSec))}
        ${kv('isolation', h.isolate ? 'on (tier 3)' : (h.scopeUids ? 'uids (tier 2)' : 'off (tier 1)'))}
        ${h.isolate ? kv('rootfs', h.rootfs) : nothing}
        ${h.isolate ? kv('terminal guard', this._guardStatus(h.protections)) : nothing}
      </div>
      ${this._stTotals(rt.stats)}
      ${this._liveStatsSection(rt.stats)}
      ${this._resourcesSection(rt.resources)}
      ${(!rt.resources || !rt.resources.length) ? html`<p class="muted">no brokered resources provisioned yet — declare them in a <span class="mono">scope.json</span> (kv, blob, bus, cron, sqlite, filesystem). See <a href="/docs/resources.md" target="_blank">docs/resources.md</a>.</p>` : nothing}`;
  }

  // Lifecycle toggle (docs/overview/14-lifecycle.md). Static/CGI components with no backend
  // still list, but only a running-backend runtime benefits — offer the toggle
  // for any runtime the owner may want paused.
  _lifecycleCell(k) {
    const st = k.state || 'enabled';
    const disabled = st !== 'enabled';
    return html`${disabled ? html`<span class="pill st-failed" title="not running">${st}</span> ` : nothing}
      <a class="link" @click=${() => this._setLifecycle(k.path, disabled ? 'enabled' : 'disabled')}>${st === 'hidden' ? 'unhide' : disabled ? 'enable' : 'disable'}</a>
      ${st !== 'hidden' ? html` · <a class="link" title="disabled + removed from sidebars until unhidden (D42)"
        @click=${() => this._setLifecycle(k.path, 'hidden')}>hide</a>` : nothing}`;
  }

  async _setLifecycle(path, state) {
    this._busy = path;
    try {
      if (!await setLifecycle(path, state)) { this._refresh(); return; } // declined: revert the <select>
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  // ---- users ----
  // Tile access is per-path levels (read < write < terminal, D16), edited
  // with the click-through row editors (_tilesEditor/_patternsEditor).
  // Sign-in modes (D22/D52): password (set here), invite link (credential-
  // less + a single-use link), or SSO (credential-less, NO link — the bound
  // email signs in through the IdP). The server seeds the new-account
  // defaults on top of whatever is given here.
  async _createUser(f) {
    const signin = f.signin.value;
    const body = { id: f.id.value.trim(), name: f.name.value.trim(), role: f.role.value,
      email: f.email.value.trim(), termApi: f.termApi.checked, termNet: f.termNet.checked };
    if (signin === 'sso') body.sso = true;
    else if (signin === 'password') body.password = f.password.value;
    const org = f.org?.value;
    if (org) body.orgs = [{ org, ...PRESETS[f.orgPreset?.value || 'developer'] }];
    try {
      const d = await api('/users', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body) });
      if (d?.inviteUrl) this._invite = { id: body.id, url: location.origin + d.inviteUrl };
      f.reset(); this._newIdTouched = false; this._err = ''; this._refresh(); // only clear the form on success
      const where = org ? `, member of ${org}` : '';
      this._flash(signin === 'sso' ? `${body.id} created — signs in via SSO as ${body.email}${where}` : `${body.id} created${where}`, 5000);
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _editEmail(u) {
    const v = prompt(`Email bound to ${u.id} — a verified SSO sign-in for it lands on this account (empty clears):`, u.email ?? '');
    if (v == null) return;
    this._patchUser(u.id, { email: v.trim() }).catch((e) => { this._err = String(e.message ?? e); });
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._refresh();
  }
  async _resetPw(id, pw) {
    if (!pw) return;
    if (pw.length < 8) { this._err = 'password too short (min 8 characters)'; return; }
    try {
      await this._patchUser(id, { password: pw });
      this._err = ''; this._notice = `password reset for ${id}`;
      setTimeout(() => { this._notice = ''; }, 3000);
    } catch (e) { this._err = String(e.message ?? e); this._notice = ''; }
  }
  async _setDisabled(u) {
    if (!u.disabled && !confirm(`Disable ${u.id}? Their sessions and terminals stop working now; everything is kept for re-enable.`)) return;
    try {
      await this._patchUser(u.id, { disabled: !u.disabled });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); } // self/last-admin guards land here
  }
  async _delUser(id) {
    if (!confirm(`Delete user ${id}? Their sessions are revoked immediately.`)) return;
    await api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
    this._refresh();
  }

  async _setTokenLogin(disabled) {
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenLoginDisabled: disabled }) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
    this.requestUpdate();
  }

  // ---- sign-in tab: token login, SSO (+ group sync, test), SSO-only mode ----
  _signInView() {
    const s = this._authSettings;
    if (!s) return nothing;
    const off = !!s.tokenLoginDisabled;      // token login currently OFF
    // canDisable is computed server-side (same predicate as the PATCH guard):
    // an admin user exists AND this session is a signed-in admin user — the
    // human behind the frame token, not the tile's own grants.
    const canDisable = !!s.canDisable;       // re-enabling is always allowed
    const pwOff = !!s.passwordLoginDisabled;
    const canPwOff = !!s.canDisablePassword;
    return html`
      <h4>sign-in security</h4>
      <label style="display:flex; gap:8px; align-items:flex-start; font-size:12px; max-width:52ch">
        <input type="checkbox" .checked=${off} ?disabled=${!off && !canDisable}
          @change=${(e) => this._setTokenLogin(e.target.checked)}>
        <span>
          <b>Disable token-URL login.</b> Turns off the bootstrap
          <span class="mono">/login?token=…</span> URL and the owner-token cookie —
          everyone signs in with an account. The <span class="mono">bx</span> CLI
          token (<span class="mono">Authorization: Bearer</span>) is unaffected.
          ${off ? html`<br><span class="muted">Token login is off. Uncheck to allow it again.</span>`
            : !s.hasAdminUser ? html`<br><span class="muted">Create an admin user first.</span>`
            : !canDisable ? html`<br><span class="muted">Sign in as an admin user (not the root token) to enable this.</span>`
            : nothing}
        </span>
      </label>
      <div style="margin-top:10px; font-size:12px; max-width:52ch">
        <button class="act" @click=${() => this._rotateToken()}>rotate owner token</button>
        <span class="muted"> Replaces <span class="mono">.xbin/token</span> — the old
        token (and any leaked copy, e.g. in pre-2026-07-09 agent transcripts)
        stops working immediately. Update host-side
        <span class="mono">XBIN_TOKEN</span> afterwards.</span>
      </div>
      ${this._tokenBox()}
      ${this._ssoView(s.sso)}
      <h4 style="margin-top:16px">sign-in policy</h4>
      <label style="display:flex; gap:8px; align-items:flex-start; font-size:12px; max-width:52ch">
        <input type="checkbox" .checked=${pwOff} ?disabled=${!pwOff && !canPwOff}
          @change=${(e) => this._setPasswordLogin(e.target.checked)}>
        <span>
          <b>SSO-only sign-in.</b> Non-admin accounts can't use a password or an
          invite link — only the identity provider. Admins keep password sign-in as
          the break-glass path, so a broken IdP config can never lock everyone out.
          ${pwOff ? html`<br><span class="muted">Password sign-in is off for non-admins. Uncheck to allow it again.</span>`
            : !canPwOff ? html`<br><span class="muted">Needs an active SSO provider (configured + --external-url).</span>`
            : nothing}
        </span>
      </label>`;
  }

  async _setPasswordLogin(disabled) {
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passwordLoginDisabled: disabled }) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
    this.requestUpdate();
  }

  // ---- SSO sign-in (docs/auth.md §SSO, D51) ----
  // Generic OIDC (Google/Keycloak/Okta/Entra/authentik/custom) or GitHub
  // OAuth2. The client secret is write-only: the server only reports whether
  // one is set. Who gets in stays the admin's call — bind emails on user rows
  // and/or set the domain allow-rule for JIT provisioning.
  _ssoView(sso) {
    const c = sso || {};
    const presets = [
      ['', '— off —'], ['google', 'Google Workspace'], ['github', 'GitHub'],
      ['keycloak', 'Keycloak'], ['okta', 'Okta'], ['entra', 'Microsoft Entra'],
      ['authentik', 'authentik'], ['custom', 'custom OIDC'],
    ];
    const preset = c.enabled ? (c.preset || 'custom') : (this._ssoPreset ?? '');
    const needsIssuer = preset && preset !== 'google' && preset !== 'github';
    return html`
      <h4 style="margin-top:16px">single sign-on</h4>
      <div style="font-size:12px; max-width:56ch">
        ${c.enabled ? html`<div style="margin-bottom:6px">
            <span class="dot" style="background:${c.ready ? 'var(--bx-green, #4caf50)' : 'var(--bx-amber,#f2a71b)'}"></span>
            ${c.ready ? 'active' : 'configured but NOT active — the daemon needs --external-url (XBIN_EXTERNAL_URL) for the redirect URI'}
            ${c.externalUrl ? html` · callback <span class="mono">${c.externalUrl}/login/sso/callback</span>` : nothing}
          </div>`
          : html`<div class="muted" style="margin-bottom:6px">Off — accounts sign in with passwords.
            Configure a provider to put a "Sign in with …" button on the login page.
            Requires the daemon flag <span class="mono">--external-url</span>${c.externalUrl ? html` (set: <span class="mono">${c.externalUrl}</span>)` : ' (not set)'}.</div>`}
        <select @change=${(e) => { this._ssoPreset = e.target.value; this._ssoTest = null; this.requestUpdate(); }}>
          ${presets.map(([v, l]) => html`<option value=${v} ?selected=${preset === v}>${l}</option>`)}
        </select>
        ${preset ? html`
          ${needsIssuer ? html`<input id="sso-issuer" placeholder="issuer URL (https://idp.example/realms/x)"
              value=${c.issuer || ''} style="margin-top:6px; width:100%">` : nothing}
          <input id="sso-cid" placeholder="client id" value=${c.clientId || ''} style="margin-top:6px; width:100%">
          <input id="sso-csec" type="password" style="margin-top:6px; width:100%"
            placeholder=${c.clientSecretSet ? 'client secret (unchanged if left empty)' : 'client secret'}>
          <input id="sso-domains" placeholder="allowed domains for auto-provisioning, comma-separated (empty = pre-bound emails only)"
            value=${(c.allowedDomains || []).join(', ')} style="margin-top:6px; width:100%">
          <input id="sso-label" placeholder="button label (default per provider)" value=${c.buttonLabel || ''}
            style="margin-top:6px; width:100%">
          ${this._ssoGroupsFields(c, preset)}
          <div style="margin-top:8px; display:flex; gap:6px; flex-wrap:wrap; align-items:center">
            <button class="act go" @click=${() => this._saveSSO(preset)}>save SSO config</button>
            <button class="act" title="OIDC discovery + JWKS fetch (or GitHub reachability) with the form as it is — nothing is saved"
              @click=${() => this._testSSO(preset)}>test connection</button>
            ${c.enabled ? html`<button class="act rm" @click=${() => this._clearSSO()}>disable SSO</button>` : nothing}
          </div>
          ${this._ssoTestResult()}
          ${c.groupSync?.lastError ? html`<div class="err" style="margin-top:6px">⚠ group sync failed for
            <span class="mono">${c.groupSync.lastError.user}</span> ${this._agoCoarse(c.groupSync.lastError.at)}: ${c.groupSync.lastError.error}</div>` : nothing}
          ${this._groupsSeenView(c)}
          <div class="muted" style="margin-top:6px">Users match by the <b>email</b> on their account
            (the users table row menu's <b>set email</b>, the add-user form's <b>sign-in: SSO</b> mode for
            pre-provisioning, or <span class="mono">bx user add --sso --email</span>); unknown emails
            under an allowed domain are auto-provisioned as role <b>user</b> with the
            <b>new accounts</b> defaults (organisations tab — never admin). Org membership from IdP groups
            is set per org (organisations tab › <b>IdP groups → members</b>). GitHub uses the
            account's verified primary email. Apple is not supported (no static client secret).</div>` : nothing}
      </div>`;
  }

  // Group-sync settings inside the SSO form: the claim (OIDC), an extra
  // scope some IdPs need, and the workspace-admin groups with the
  // self-joinable-group warning.
  _ssoGroupsFields(c, preset) {
    const hint = this._claimHint(preset);
    const admins = (c.adminGroups || []).join(', ');
    return html`
      <div style="margin-top:10px">
        <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">group sync</span>
        ${preset !== 'github' && preset !== 'google' ? html`
          <div style="display:flex; gap:6px; margin-top:4px">
            <input id="sso-gclaim" placeholder="groups claim (default: groups)" value=${c.groupsClaim || ''} style="flex:1">
            <input id="sso-gscope" placeholder="extra scope, if the IdP needs one (Okta: groups)" value=${c.groupsScope || ''} style="flex:1">
          </div>` : nothing}
        <div class="muted" style="margin-top:3px">${hint}</div>
        <input id="sso-admins" list="idp-groups-seen" value=${admins} style="margin-top:6px; width:100%"
          placeholder="workspace-admin groups, comma-separated (empty = admins are promoted by hand)"
          @input=${(e) => { this._ssoAdminsDirty = !!e.target.value.trim(); this.requestUpdate(); }}>
        ${admins || this._ssoAdminsDirty ? html`<div class="warn-line">⚠ Members of these groups become workspace admins
          at sign-in and are demoted when they leave (never the last admin, never a hand-promoted one). Use a
          group only IdP admins can edit — never one people can join themselves.</div>` : nothing}
        ${this._groupsDatalist()}
      </div>`;
  }
  _claimHint(preset) {
    switch (preset) {
      case 'google': return 'Google Workspace: groups are read at sign-in through the Cloud Identity API (enable it on the OAuth client’s project; the groups scope is requested only while rules exist). Rules name a group by its email address.';
      case 'github': return 'GitHub: groups are the account’s teams as org/team-slug (acme/infra) and its orgs (acme); needs the read:org scope — requested only while rules exist.';
      case 'okta': return 'Okta: add a Groups claim to the app’s ID token (filter: matches regex .*) and put "groups" in the extra scope; values are group names.';
      case 'entra': return 'Entra: enable the groups claim on the app registration; values are group object IDs unless the app emits names.';
      case 'keycloak': return 'Keycloak: add a Group Membership mapper to the client scope; values are paths like /sales.';
      default: return 'Values arrive exactly as the IdP emits them (ID token, then UserInfo) — check "groups seen" below for the spelling.';
    }
  }
  _groupsDatalist() { return groupsDatalist(this._authSettings?.sso?.groupSync?.knownGroups); }
  // What the IdP has actually been sending, with where each group lands.
  _groupsSeenView(c) {
    const known = c.groupSync?.knownGroups ?? [];
    const adminGroups = (c.adminGroups || []).map((g) => g.toLowerCase());
    const users = this._users ?? [];
    return html`<div style="margin-top:8px">
      <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">groups seen</span>
      <span class="muted"> — what the IdP actually sent, over everyone’s last sign-in</span>
      <div style="margin-top:3px">
        ${known.length ? known.map((g) => {
          const who = users.filter((u) => (u.ssoGroups ?? []).some((x) => x.toLowerCase() === g.toLowerCase())).map((u) => u.id);
          const org = this._ruleFor(g);
          const isAdmin = adminGroups.includes(g.toLowerCase());
          return html`<span class="pill mono" title=${who.join(', ')}>${g} <span class="muted">· ${who.length}</span>${org ? html` → ${org}` : nothing}${isAdmin ? html` → workspace admin` : nothing}${!org && !isAdmin ? html` <span class="muted">unmapped</span>` : nothing}</span>`;
        }) : html`<span class="muted">none yet — groups appear after the first SSO sign-in that carries them (with at least one rule configured).</span>`}
      </div>
    </div>`;
  }
  _ssoTestResult() {
    const t = this._ssoTest;
    if (!t) return nothing;
    if (t.busy) return html`<div class="muted" style="margin-top:6px">testing…</div>`;
    return html`<div style="margin-top:6px">
      ${t.ok ? html`<span class="st-healthy">✓ reachable</span> — ${t.kind === 'github' ? 'GitHub API answers' : html`issuer <span class="mono">${t.issuer}</span> · ${t.jwksKeys} signing key${t.jwksKeys === 1 ? '' : 's'}`}${t.ready ? '' : ' · not active yet (see above)'}`
        : html`<span class="st-failed">✗ ${t.error}</span>`}
      ${(t.warnings ?? []).map((w) => html`<div class="warn-line">⚠ ${w}</div>`)}
    </div>`;
  }
  _ssoPayload(preset) {
    const g = (id) => this.renderRoot.querySelector('#' + id)?.value?.trim() ?? '';
    const sso = {
      kind: preset === 'github' ? 'github' : 'oidc',
      preset,
      issuer: preset === 'google' ? 'https://accounts.google.com' : g('sso-issuer'),
      clientId: g('sso-cid'),
      clientSecret: this.renderRoot.querySelector('#sso-csec')?.value ?? '',
      allowedDomains: g('sso-domains').split(',').map((d) => d.trim()).filter(Boolean),
      buttonLabel: g('sso-label'),
      groupsClaim: g('sso-gclaim'),
      groupsScope: g('sso-gscope'),
      adminGroups: g('sso-admins').split(',').map((d) => d.trim()).filter(Boolean),
    };
    if (preset === 'github') sso.issuer = '';
    return sso;
  }

  async _saveSSO(preset) {
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sso: this._ssoPayload(preset) }) });
      this._flash('SSO configuration saved'); this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    this._refresh();
  }

  async _testSSO(preset) {
    this._ssoTest = { busy: true };
    try {
      this._ssoTest = await api('/auth-settings/sso/test', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sso: this._ssoPayload(preset) }) });
      this._err = '';
    } catch (e) { this._ssoTest = null; this._err = String(e.message ?? e); }
  }

  async _clearSSO() {
    if (!confirm('Disable SSO sign-in? The login page drops the SSO button; existing sessions stay.')) return;
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sso: null }) });
      this._ssoPreset = ''; this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    this._refresh();
  }

  async _rotateToken() {
    if (!confirm('Rotate the owner token? The current token stops working immediately (bearer + cookie). Host-side bx/automation must switch to the new one.')) return;
    try {
      const d = await api('/auth-rotate-token', { method: 'POST' });
      this._token = d.token; // rendered in a copy-field box (like invites)
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // The rotated-token box: a copy field that stays until dismissed — a
  // prompt() you can accidentally dismiss is no place for a credential.
  _tokenBox() {
    if (!this._token) return nothing;
    return html`<div style="margin:8px 0; padding:8px 10px; border:1px solid var(--bx-green, #4caf50);
        border-radius:6px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <b style="font-size:12px">new owner token</b>
      <input class="mono" size="40" readonly .value=${this._token} @focus=${(e) => e.target.select()}>
      <button class="act" @click=${() => navigator.clipboard?.writeText(this._token)}>copy</button>
      <span class="muted" style="font-size:10.5px">also written to &lt;workspace&gt;/.xbin/token —
        update host-side XBIN_TOKEN</span>
      <button class="act" @click=${() => { this._token = null; }}>✕</button>
    </div>`;
  }

  // ---- membership helpers (single-row API, D53) ----
  _membershipOf(orgId, uid) {
    const o = (this._orgs ?? []).find((x) => x.id === orgId);
    return (o?.members ?? []).find((m) => m.id === uid) ?? null;
  }
  _presetLabel(m) {
    const p = presetOf(m);
    return p !== 'custom' ? p : `${m.level}${m.create ? '+create' : ''}`;
  }
  _setMembership(orgId, uid, patch) {
    return this._orgAPI('PUT', `/orgs/${encodeURIComponent(orgId)}/members/${encodeURIComponent(uid)}`, patch);
  }
  _dropMembership(orgId, uid) {
    return this._orgAPI('DELETE', `/orgs/${encodeURIComponent(orgId)}/members/${encodeURIComponent(uid)}`);
  }
  _ruleFor(group) { // first org whose IdP-group rules name this group
    const g = String(group).toLowerCase();
    return (this._orgs ?? []).find((o) => (o.ssoGroups ?? []).some((r) => r.group.toLowerCase() === g))?.id ?? null;
  }
  _agoCoarse(unixSec) {
    const s = Math.max(0, (Date.now() / 1000) - unixSec);
    if (s < 90) return 'just now';
    if (s < 3600) return `${Math.round(s / 60)}m ago`;
    if (s < 86400 * 2) return `${Math.round(s / 3600)}h ago`;
    if (s < 86400 * 60) return `${Math.round(s / 86400)}d ago`;
    return `${Math.round(s / (86400 * 30))}mo ago`;
  }
  // The users table's org pills: manual vs ⟳ synced (dashed), ★ org admin.
  _userOrgsCell(u) {
    const pills = [];
    for (const o of (this._orgs ?? [])) {
      const m = (o.members ?? []).find((x) => x.id === u.id);
      if (!m) continue;
      const synced = m.via === 'sso';
      pills.push(html`<span class="pill ${m.admin ? 'crown' : ''} ${synced ? 'sync' : ''}" style=${m.suspended ? 'opacity:.55' : ''}
        title=${synced ? `synced from IdP group ${(m.viaGroups ?? []).join(', ')} — follows the group at every sign-in` : 'manual membership'}>
        ${m.admin ? '★ ' : ''}${synced ? '⟳ ' : ''}${o.id} · ${this._presetLabel(m)}${m.suspended ? ' · suspended' : ''}</span>`);
    }
    return pills.length ? pills : html`<span class="muted">—</span>`;
  }
  _lastLoginCell(u) {
    if (u.lastLogin === undefined) return html`<span class="muted">—</span>`;
    if (!u.lastLogin) {
      return html`<span class="never" title="has never signed in">never</span>${u.invitePending ? html` <span class="muted">· invite out</span>` : nothing}`;
    }
    const stale = (Date.now() / 1000) - u.lastLogin > STALE_SEC;
    const groups = (u.ssoGroups ?? []).length ? `groups at last sign-in: ${u.ssoGroups.join(', ')}` : '';
    return html`<span class=${stale ? 'muted' : ''} title=${new Date(u.lastLogin * 1000).toLocaleString()}>${this._agoCoarse(u.lastLogin)}</span>
      <span class="muted" title=${groups}>· ${u.lastLoginVia || ''}</span>`;
  }
  async _signOutUser(id) {
    if (!confirm(`Sign out ${id} everywhere? All their browser sessions and terminal tokens end now; they can sign in again.`)) return;
    try {
      const d = await api(`/users/${encodeURIComponent(id)}/sessions`, { method: 'DELETE' });
      this._err = ''; this._flash(`signed out ${id} (${d.dropped} session${d.dropped === 1 ? '' : 's'})`);
    } catch (e) { this._err = String(e.message ?? e); }
    this._loadSessions();
  }
  // Bulk offboarding: disable every account the current filter shows.
  async _disableShown(rows) {
    const ids = rows.filter((u) => !u.disabled).map((u) => u.id);
    if (!ids.length) return;
    if (!confirm(`Disable ${ids.length} account${ids.length === 1 ? '' : 's'}?\n\n${ids.join(', ')}\n\nSign-in, sessions and terminals stop now. Nothing is deleted — enable restores everything.`)) return;
    this._bulkBusy = true;
    const failed = [];
    for (const id of ids) {
      try {
        await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ disabled: true }) });
      } catch (e) { failed.push(`${id}: ${e.message ?? e}`); }
    }
    this._bulkBusy = false;
    await this._refresh();
    this._flash(`disabled ${ids.length - failed.length} account${ids.length - failed.length === 1 ? '' : 's'}`);
    this._err = failed.length ? 'could not disable — ' + failed.join('; ') : '';
  }

  async _mintInvite(id) {
    try {
      const d = await api(`/users/${encodeURIComponent(id)}/invite`, { method: 'POST' });
      this._invite = { id, url: d.inviteLink || location.origin + d.inviteUrl };
      this._err = ''; this._flash(`invite link minted for ${id}`, 3000);
    } catch (e) { this._err = String(e.message ?? e); }
    this._refresh();
  }

  // The one-time invite link box: shown after creating a user without a
  // password or minting a re-invite — copy it and send it however you like
  // (no self-signup: links only ever come from an admin).
  _inviteBox() {
    const inv = this._invite;
    if (!inv) return nothing;
    return html`<div style="margin:8px 0; padding:8px 10px; border:1px solid var(--bx-green, #4caf50);
        border-radius:6px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <b style="font-size:12px">invite link for ${inv.id}</b>
      <input class="mono" size="46" readonly .value=${inv.url} @focus=${(e) => e.target.select()}>
      <button class="act" @click=${() => navigator.clipboard?.writeText(inv.url)}>copy</button>
      <span class="muted" style="font-size:10.5px">single-use · expires in 72h · send it to them yourself</span>
      <button class="act" @click=${() => { this._invite = null; }}>✕</button>
    </div>`;
  }

  // Pending human access requests (D36) — approve writes an exact entry at
  // the chosen level (authoritative, D31) and clears the row.
  _requestsView() {
    const reqs = (this._reqs ?? []).filter((q) => q.manage);
    if (!reqs.length) return nothing;
    return html`
      <h4>access requests</h4>
      ${reqs.map((q) => html`<div style="display:flex; gap:6px; align-items:center; flex-wrap:wrap; margin:3px 0; font-size:12px">
        <span class="mono">${q.user}</span> wants
        <select id="rq-${q.user}-${q.tile}">
          ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${q.level === l}>${l}</option>`)}
        </select>
        on <span class="mono">${q.tile}</span>
        ${q.note ? html`<span class="muted">— ${q.note}</span>` : nothing}
        <button class="act go" @click=${async () => {
          const sel = this.renderRoot.getElementById(`rq-${q.user}-${q.tile}`);
          try {
            await api('/access-requests/approve', { method: 'POST', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ user: q.user, tile: q.tile, level: sel?.value || q.level }) });
            this._err = '';
          } catch (e) { this._err = String(e.message ?? e); }
          this._refresh();
        }}>approve</button>
        <button class="act rm" @click=${async () => {
          try {
            await api('/access-requests', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ user: q.user, tile: q.tile }) });
            this._err = '';
          } catch (e) { this._err = String(e.message ?? e); }
          this._refresh();
        }}>dismiss</button>
      </div>`)}`;
  }

  // Chips AND-combine; counts run on the full list so a chip never flickers
  // away while you narrow, and zero-count chips stay hidden.
  _userChipDefs() {
    const now = Date.now() / 1000;
    const defs = [
      { id: 'admins', label: 'admins', test: (u) => u.role === 'admin' },
      { id: 'disabled', label: 'disabled', test: (u) => !!u.disabled },
      { id: 'invited', label: 'invited', test: (u) => !!u.invitePending },
      { id: 'never', label: 'never signed in', test: (u) => u.lastLogin !== undefined && !u.lastLogin },
      { id: 'stale', label: 'stale 30d+', test: (u) => !!u.lastLogin && now - u.lastLogin > STALE_SEC },
      { id: 'noorg', label: 'no org', test: (u) => !(this._orgs ?? []).some((o) => (o.members ?? []).some((m) => m.id === u.id)) },
    ];
    for (const o of (this._orgs ?? [])) {
      defs.push({ id: `org:${o.id}`, label: o.id, test: (u) => (o.members ?? []).some((m) => m.id === u.id) });
    }
    return defs;
  }
  _userMatches(u, q, chips, defs) {
    if (q) {
      const orgIds = (this._orgs ?? []).filter((o) => (o.members ?? []).some((m) => m.id === u.id)).map((o) => o.id);
      const hay = [u.id, u.name, u.email, u.role, ...orgIds].filter(Boolean).join(' ').toLowerCase();
      if (!hay.includes(q)) return false;
    }
    for (const id of chips) {
      const d = defs.find((x) => x.id === id);
      if (d && !d.test(u)) return false;
    }
    return true;
  }
  _toggleUserChip(id) {
    const s = new Set(this._usersChips ?? []);
    s.has(id) ? s.delete(id) : s.add(id);
    this._usersChips = s;
  }

  _usersView() {
    const users = this._users ?? [];
    const q = (this._usersQ ?? '').trim().toLowerCase();
    const chips = this._usersChips ?? new Set();
    const defs = this._userChipDefs().map((d) => ({ ...d, n: users.filter(d.test).length }));
    const rows = users.filter((u) => this._userMatches(u, q, chips, defs));
    const narrowed = rows.length < users.length;
    const nAdmins = users.filter((u) => u.role === 'admin').length;
    const nDisabled = users.filter((u) => u.disabled).length;
    const nInvited = users.filter((u) => u.invitePending).length;
    const enabledShown = rows.filter((u) => !u.disabled).length;
    return html`
      ${this._targetDatalist()}
      <h4>users ${users.length ? html`<span class="muted" style="text-transform:none; letter-spacing:0">— ${users.length} account${users.length === 1 ? '' : 's'}
        · ${nAdmins} admin${nAdmins === 1 ? '' : 's'}${nDisabled ? ` · ${nDisabled} disabled` : ''}${nInvited ? ` · ${nInvited} invited` : ''}</span>` : nothing}</h4>
      ${users.length > 1 ? html`
      <div class="filterbar" style="margin:4px 0 6px">
        <input class="q" type="search" placeholder="filter by id, name, email or org…" .value=${this._usersQ ?? ''}
          @input=${(e) => { this._usersQ = e.target.value; }}>
        <div class="chips">
          ${defs.filter((d) => d.n > 0).map((d) => html`<span class="chip ${chips.has(d.id) ? 'on' : ''}"
            @click=${() => this._toggleUserChip(d.id)}>${d.label}<span class="n">${d.n}</span></span>`)}
        </div>
        <span class="count-note">${rows.length}/${users.length}</span>
        ${narrowed && enabledShown ? html`<button class="act rm" ?disabled=${this._bulkBusy}
          title="disable every account the filter shows (offboarding)"
          @click=${() => this._disableShown(rows)}>disable all shown (${enabledShown})</button>` : nothing}
      </div>` : nothing}
      <table>
        <tr><th>user</th><th>role</th><th>orgs</th><th>access</th><th>last sign-in</th><th></th></tr>
        ${this._users == null ? html`<tr><td class="muted" colspan="6">loading…</td></tr>`
          : !users.length ? html`<tr><td class="muted" colspan="6">no users — the root token is the only admin. Add one below.</td></tr>`
          : !rows.length ? html`<tr><td class="muted" colspan="6">no users match — <a class="link"
              @click=${() => { this._usersQ = ''; this._usersChips = new Set(); }}>clear filters</a></td></tr>`
          : repeat(rows, (u) => u.id, (u) => this._userRow(u))}
      </table>

      ${this._requestsView()}

      <h4>add user</h4>
      ${(() => {
        const ssoOn = !!this._authSettings?.sso?.enabled;
        const ssoReady = !!this._authSettings?.sso?.ready;
        const signin = this._newSignin ?? (ssoReady ? 'sso' : 'password');
        const orgs = this._orgs ?? [];
        return html`
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createUser(e.target); }}>
        <input name="id" placeholder="username" size="12" required @input=${() => { this._newIdTouched = true; }}>
        <input name="name" placeholder="display name" size="14">
        <input name="email" type="email" size="20" ?required=${signin === 'sso'}
          placeholder=${signin === 'sso' ? 'email (the IdP identity)' : 'email (optional — SSO binding)'}
          @input=${(e) => { // the id follows the email's local-part until typed by hand
            if (this._newIdTouched) return;
            const local = e.target.value.split('@')[0].toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[._-]+|[._-]+$/g, '');
            e.target.form.id.value = local.slice(0, 32);
          }}>
        <select name="role"><option value="user">user</option><option value="admin">admin</option></select>
        ${orgs.length ? html`
          <select name="org" title="join an organisation at creation (more via the row's orgs… later)">
            <option value="">join org: none</option>
            ${orgs.map((o) => html`<option value=${o.id}>join ${o.id}</option>`)}
          </select>
          <select name="orgPreset" title="role in that org">
            <option value="developer">as developer</option>
            <option value="viewer">as viewer</option>
            <option value="admin">as org admin</option>
          </select>` : nothing}
        <label class="muted" style="font-size:11px"><input type="checkbox" name="termApi"> term-api</label>
        <label class="muted" style="font-size:11px" title="internet in terminals on personal/workspace tiles (org tiles follow their org's network sets)"><input type="checkbox" name="termNet"> term-net</label>
        <select name="signin" title="how this account signs in" @change=${(e) => { this._newSignin = e.target.value; }}>
          <option value="password" ?selected=${signin === 'password'}>sign-in: password</option>
          <option value="invite" ?selected=${signin === 'invite'}>sign-in: invite link</option>
          <option value="sso" ?selected=${signin === 'sso'} ?disabled=${!ssoOn}>sign-in: SSO${ssoOn ? '' : ' (not configured)'}</option>
        </select>
        ${signin === 'password' ? html`<input name="password" type="password" placeholder="password (min 8)" size="16" minlength="8" required>` : nothing}
        <button class="act go">create</button>
      </form>`;
      })()}
      ${this._inviteBox()}
      <p class="muted" style="font-size:11px;margin-top:6px; max-width:80ch">
        <b>SSO</b>: no password, no link — the bound email's IdP sign-in lands on this account.
        <b>Invite link</b>: a single-use link they open to set a password (there is no self-signup).
        Every new account also gets the <b>new accounts</b> seed (organisations tab); more orgs
        later via the row's <b>orgs…</b>. Levels: <b>read</b> = see the tile + its source ·
        <b>write</b> = edit/drive it · <b>terminal</b> = a root shell in its directory. A non-admin's
        terminals get no live tile-API token without <b>term-api</b> and, on personal/workspace tiles,
        no internet egress without <b>term-net</b> — terminals on org-owned tiles follow the org's
        <b>network sets</b> instead. Sign-in security and SSO live in the <b>sign-in</b> tab.</p>`;
  }

  _userRow(u) {
    const tilesKey = `user:${u.id}:tiles`;
    const createKey = `user:${u.id}:create`;
    const orgsKey = `user:${u.id}:orgs`;
    const synced = u.roleVia === 'sso';
    return html`<tr style=${u.disabled ? 'opacity:.55' : ''}>
      <td class="user"><span class="mono">${u.id}</span>
        ${u.name !== u.id || u.email ? html`<div class="sub">${u.name !== u.id ? u.name : ''}${u.email ? html` <span
          title="SSO binding — a verified ${u.email} sign-in lands on this account">&lt;${u.email}&gt;</span>` : nothing}</div>` : nothing}</td>
      <td><span class="pill">${u.role}</span>${synced ? html`<span class="pill sync"
          title="workspace admin via an IdP group rule (sign-in › group sync) — demote by removing them from the group">⟳ synced</span>` : nothing}${u.disabled ? html`<span class="pill off" title="account disabled — can't sign in; everything is kept for re-enable (D34)">disabled</span>` : nothing}${u.invitePending ? html`<span class="pill" title="an unredeemed invite link is out">invited</span>` : nothing}</td>
      <td>${this._userOrgsCell(u)}</td>
      <td>${u.role === 'admin' ? html`<span class="muted">all</span>`
        : html`${Object.entries(u.tiles || {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
          ${(u.canCreate || []).map((c) => html`<span class="pill">create·${c}</span>`)}
          ${u.termApi ? html`<span class="pill">term-api</span>` : nothing}
          ${u.termNet ? html`<span class="pill">term-net</span>` : nothing}
          ${!Object.keys(u.tiles || {}).length && !(u.canCreate || []).length
            ? html`<span class="muted">—</span>` : nothing}`}</td>
      <td style="white-space:nowrap">${this._lastLoginCell(u)}</td>
      <td style="text-align:right; white-space:nowrap">
        <button class="act" title="org memberships — join, leave, level, detach from IdP sync" @click=${() => this._toggleDraft(orgsKey, () => true)}>orgs…</button>
        ${u.role === 'admin' ? nothing : html`
          <button class="act" title="per-tile access outside orgs" @click=${() => this._toggleDraft(tilesKey,
            () => Object.entries(u.tiles ?? {}).map(([target, level]) => ({ target, level })))}>tiles…</button>`}
        ${this._pwEdit === u.id ? html`
          <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
              const pw = e.target.pw.value; this._pwEdit = null; this._resetPw(u.id, pw); }}>
            <input name="pw" type="password" size="12" placeholder="new password (min 8)" autofocus>
            <button class="act" type="submit">set</button>
            <button class="act" type="button" @click=${() => { this._pwEdit = null; }}>✕</button>
          </form>` : nothing}
        ${this._userMenu(u, createKey)}
      </td>
    </tr>
    ${this._draft(orgsKey) ? html`<tr><td colspan="6">${this._userOrgsEditor(u, orgsKey)}</td></tr>` : nothing}
    ${this._draft(tilesKey) ? html`<tr><td colspan="6">
      ${this._tilesEditor(tilesKey, (tiles) => this._orgAPI('PATCH', `/users/${encodeURIComponent(u.id)}`, { tiles }))}
    </td></tr>` : nothing}
    ${this._draft(createKey) ? html`<tr><td colspan="6">
      ${this._patternsEditor(createKey, (canCreate) => this._orgAPI('PATCH', `/users/${encodeURIComponent(u.id)}`, { canCreate }))}
    </td></tr>` : nothing}`;
  }

  // The rare actions, in a native <details> menu — every item closes it.
  _userMenu(u, createKey) {
    const synced = u.roleVia === 'sso';
    const live = (this._sessions ?? []).filter((s) => s.user === u.id).length;
    return html`<details class="menu">
      <summary>more ▾</summary>
      <div class="items" @click=${(e) => this._menuDone(e)}>
        <button ?disabled=${synced} title=${synced ? 'role comes from an IdP group rule — change it in sign-in › group sync' : ''}
          @click=${() => this._patchUser(u.id, { role: u.role === 'admin' ? 'user' : 'admin' })}>${u.role === 'admin' ? 'demote to user' : 'make admin'}</button>
        ${u.role === 'admin' ? nothing : html`
          <button @click=${() => this._toggleDraft(createKey, () => [...(u.canCreate ?? [])])}>create patterns…</button>
          <button @click=${() => this._patchUser(u.id, { termApi: !u.termApi })}>${u.termApi ? 'revoke term-api' : 'allow term-api'}</button>
          <button title="internet in terminals on personal/workspace tiles — org tiles follow their org's network sets (D54)"
            @click=${() => this._patchUser(u.id, { termNet: !u.termNet })}>${u.termNet ? 'revoke term-net' : 'allow term-net'}</button>`}
        <hr>
        <button @click=${() => this._editEmail(u)}>set email…</button>
        <button @click=${() => this._mintInvite(u.id)}>mint invite link</button>
        <button @click=${() => { this._pwEdit = u.id; }}>set password…</button>
        <hr>
        <button ?disabled=${!live} title=${live ? '' : 'no live sessions'} @click=${() => this._signOutUser(u.id)}>sign out everywhere${live ? ` (${live})` : ''}</button>
        <button class=${u.disabled ? '' : 'rm'} @click=${() => this._setDisabled(u)}>${u.disabled ? 'enable account' : 'disable account'}</button>
        <button class="rm" @click=${() => this._delUser(u.id)}>delete…</button>
      </div>
    </details>`;
  }

  // orgs… expansion: one line per org, one request per click (single-
  // membership API). Synced rows are read-mostly — detach to edit by hand.
  _userOrgsEditor(u, key) {
    const orgs = this._orgs ?? [];
    const groups = u.ssoGroups ?? [];
    return html`<div class="editor">
      ${!orgs.length ? html`<span class="muted">no organisations yet — create one in the organisations tab.</span>` : nothing}
      ${orgs.map((o) => {
        const m = (o.members ?? []).find((x) => x.id === u.id);
        const synced = !!m && m.via === 'sso';
        const ruleMatches = !!m && !synced && groups.some((g) => (o.ssoGroups ?? []).some((r) => r.group.toLowerCase() === g.toLowerCase()));
        return html`<div class="orow">
          <label style="min-width:16ch"><input type="checkbox" .checked=${!!m} @change=${(e) => {
              if (e.target.checked) return this._setMembership(o.id, u.id, PRESETS.developer);
              if (synced && !confirm(`${u.id} is in ${o.id} via IdP group ${(m.viaGroups ?? []).join(', ')}. Removing them here lasts until their next sign-in — remove them from the group, or delete the rule on the org card. Remove anyway?`)) { e.target.checked = true; return; }
              return this._dropMembership(o.id, u.id);
            }}> <span class="mono">${o.id}</span>${o.name && o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}</label>
          ${m ? html`
            <select title="role preset" ?disabled=${synced} @change=${(e) => { const p = PRESETS[e.target.value]; if (p) this._setMembership(o.id, u.id, p); }}>
              ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${presetOf(m) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
            </select>
            <select title="org-wide level on tiles the org owns" ?disabled=${synced} @change=${(e) => this._setMembership(o.id, u.id, { level: e.target.value })}>
              ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${m.level === l}>${l}</option>`)}
            </select>
            <label class="muted"><input type="checkbox" .checked=${!!m.create} ?disabled=${synced} @change=${(e) => this._setMembership(o.id, u.id, { create: e.target.checked })}> create</label>
            <label class="muted"><input type="checkbox" .checked=${!!m.admin} ?disabled=${synced} @change=${(e) => this._setMembership(o.id, u.id, { admin: e.target.checked })}> org admin</label>
            <label class="muted"><input type="checkbox" .checked=${!!m.suspended} @change=${(e) => this._setMembership(o.id, u.id, { suspended: e.target.checked })}> suspended</label>
            ${synced ? html`<span class="pill sync" title="synced from IdP group — knobs follow the rule at every sign-in">⟳ ${(m.viaGroups ?? []).join(', ')}</span>
              <button class="act" title="stop syncing this membership; it becomes manual" @click=${async () => { await this._setMembership(o.id, u.id, { via: '' }); if (!this._err) this._flash(`${o.id}: ${u.id} is now a manual member`); }}>detach</button>` : nothing}
            ${ruleMatches ? html`<span class="muted" title="a group rule also matches this user; remove this manual row to let sync manage it">manual (rule also matches)</span>` : nothing}`
            : html`<span class="muted">not a member — tick to join as developer</span>`}
        </div>`;
      })}
      <div class="orow" style="margin-top:4px">
        <span class="muted">IdP groups at last sign-in:</span>
        ${groups.length ? groups.map((g) => { const org = this._ruleFor(g); return html`<span class="pill mono" title=${org ? `rule → ${org}` : 'no rule maps this group'}>${g}${org ? ` → ${org}` : ''}</span>`; })
          : html`<span class="muted">none seen yet</span>`}
        <span style="flex:1"></span>
        <span class="muted" style="font-size:10.5px">changes save immediately · one request per click</span>
        <button class="act" @click=${() => this._dropDraft(key)}>close</button>
      </div>
    </div>`;
  }

  // ---- sessions tab ----
  // Live browser sessions with their client IPs — the attribution view for
  // the /c/ warm-IP gate: a tile's credential-less subresource loads (its
  // JS/CSS/images, which can't carry credentials from a sandboxed frame) are
  // served only from an IP that authenticated within the last hour. If
  // xbind sits behind a reverse proxy, --trusted-proxies must be set or
  // every row here shows the proxy's IP and the gate keys on that.
  async _loadSessions() {
    try {
      const d = await api('/sessions');
      this._sessions = d.sessions ?? [];
    } catch (e) { this._err = String(e.message ?? e); }
  }
  _ago(unixSec) {
    const s = Math.max(0, (Date.now() - unixSec * 1000) / 1000);
    return (s < 60 ? (s | 0) + 's' : fmtDur(s)) + ' ago';
  }
  _sessionsView() {
    const all = this._sessions ?? [];
    const q = (this._sessQ ?? '').trim().toLowerCase();
    const ss = q ? all.filter((s) => `${s.user} ${s.name || ''}`.toLowerCase().includes(q)) : all;
    const perUser = {};
    for (const s of all) perUser[s.user] = (perUser[s.user] || 0) + 1;
    return html`
      <h4>sessions</h4>
      ${all.length > 1 ? html`<div class="filterbar" style="margin:4px 0 6px">
        <input class="q" type="search" placeholder="filter by user…" .value=${this._sessQ ?? ''} @input=${(e) => { this._sessQ = e.target.value; }}>
        <span class="count-note">${ss.length}/${all.length} session${all.length === 1 ? '' : 's'} · ${Object.keys(perUser).length} user${Object.keys(perUser).length === 1 ? '' : 's'}</span>
      </div>` : nothing}
      <table>
        <tr><th>user</th><th>signed in</th><th>last active</th><th>login IP</th><th>last IP</th><th></th></tr>
        ${!all.length ? html`<tr><td class="muted" colspan="6">no live sessions</td></tr>`
          : !ss.length ? html`<tr><td class="muted" colspan="6">no sessions match</td></tr>`
          : repeat(ss, (s) => `${s.user}:${s.created}:${s.ip}`, (s) => html`<tr>
          <td class="mono">${s.user}${s.name && s.name !== s.user ? html` <span class="muted">${s.name}</span>` : nothing}</td>
          <td title=${new Date(s.created * 1000).toLocaleString()}>${this._ago(s.created)}</td>
          <td title=${new Date(s.lastActive * 1000).toLocaleString()}>${this._ago(s.lastActive)}</td>
          <td class="mono">${s.ip || '—'}</td>
          <td class="mono">${s.lastIP || '—'}</td>
          <td style="text-align:right">${s.current ? html`<span class="pill" title="the session you are signed in with right now">this session</span>`
            : html`<button class="act rm" title="ends ALL of ${s.user}'s sessions (${perUser[s.user]})" @click=${() => this._signOutUser(s.user)}>sign out</button>`}</td>
        </tr>`)}
      </table>
      <p class="muted" style="font-size:11px;margin-top:6px;max-width:72ch">
        Bootstrap <b>token logins</b> (<span class="mono">/login?token=…</span>) are
        stateless and don't appear here. An IP with activity in the last hour counts as
        <b>recently authenticated</b>: that's the second half of the rule serving tile
        subresources (JS/CSS/images) without credentials — sandboxed tile frames can't
        attach any, so xbind asks for the browser's Fetch-Metadata fingerprint <i>and</i>
        a warm source IP, which keeps drive-by internet scanners (no login) out of tile
        source. Behind a reverse proxy, set <span class="mono">--trusted-proxies</span>
        (or <span class="mono">XBIN_TRUSTED_PROXIES</span>) or these IPs all show the
        proxy's address and the gate keys on it. Sessions die after 12 h idle
        (30 d absolute); a deleted or disabled user's sessions die immediately.
        <b>sign out</b> ends every session of that user (they can sign in again);
        disabling the account (users tab) also blocks future sign-ins.</p>`;
  }

  // ---- click-through editors (the users tab; the orgs tab has its own) ----
  // Workspaces are small, so everything about permissions is enumerable and
  // clickable: tile targets come from a datalist of real paths + patterns —
  // no free-text specs to mistype. The draft plumbing and the row editors
  // (_tilesEditor, _patternsEditor) are shared.js's WithDrafts mixin.

  // ---- test surface (hack/ui-harness) ----
  // Stable names over the tile's private state (see bx-shell's testApi).
  testApi() {
    const a = this;
    // Drafts live on the tab element that owns the namespace; the rest here.
    const owner = (k) => {
      const tag = k.startsWith('permset:') ? 'bx-admin-permsets' : k.startsWith('netset:') ? 'bx-admin-netsets'
        : k.startsWith('bindcustom:') ? 'bx-admin-binding' : k.startsWith('orgallow:') || k.startsWith('ws:') ? 'bx-admin-orgs' : null;
      return (tag && a.renderRoot.querySelector(tag)?.testApi()) || { draft: (x) => a._draft(x), setDraft: (x, v) => a._setDraft(x, v), dropDraft: (x) => a._dropDraft(x) };
    };
    return {
      get tab() { return a._tab; },
      draft: (k) => owner(k).draft(k),
      setDraft: (k, v) => owner(k).setDraft(k, v),
      dropDraft: (k) => owner(k).dropDraft(k),
    };
  }

  // The service datalist (permission-set and org editors) needs the wiring
  // data; the binding/ingress tabs load their own.
  async _loadIfaces() {
    try { this._ifaces = await api('/bindings'); } catch (e) { this._err = String(e.message ?? e); }
  }

  // Target / service suggestions (shared.js) rendered once per view as a
  // <datalist> the row editors' inputs attach to.
  _targetOptions() { return targetOptions(this._ov); }
  _targetDatalist() { return targetDatalist(this._targetOptions()); }
  _serviceDatalist() { return serviceDatalist(serviceOptions(this._ifaces)); }

}

customElements.define('bx-admin', BxAdmin);
