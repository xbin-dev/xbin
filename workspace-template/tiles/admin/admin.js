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
import { ruleLabel, netOptions } from '/vendor/bx-netrules.js';
import { parseAllow, fmtAllow, allowProblem, describeAllow, capInfo } from '/vendor/bx-allow.js';

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
import { targetOptions, targetDatalist, serviceOptions, serviceDatalist, allowRows } from './shared.js';

export class BxAdmin extends LitElement {
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
    _secretEdit: { state: true }, // {comp, key} vault secret being re-set inline
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
    _polEdit: { state: true },  // policy-editor drafts, keyed '' (workspace) / org id
    _drafts: { state: true },   // click-through editor drafts, keyed by context
    _showHidden: { state: true }, // reveal hidden (state=hidden) tiles in lists (D42)
    _authSettings: { state: true },
    _alerts: { state: true }, // {tokenLoginDisabled, hasAdminUser, canDisable}
    _ifaces: { state: true },   // {bindings, components} — interface wiring
    _ingress: { state: true },  // {exposes, routes, streams, …} — published endpoints
    _ingEdit: { state: true },  // per-row route edits before publish (comp\x00slot → {…})
    _schedules: { state: true }, // [{component, schedule, retention}]
    _versions: { state: true },  // comp -> [{version,time,size}] (lazy)
    _verOpen: { state: true },   // set of comps whose version list is expanded
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
    this._versions = {};
    this._verOpen = new Set();
    this._schedules = [];
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
    if (t === 'providers' || t === 'wiring' || t === 'endpoints' || t === 'expose' || t === 'permsets' || t === 'orgs') this._loadIfaces(); // permsets/orgs: the service datalist
    if (t === 'backup') this._loadBackup();
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
    if (['providers', 'wiring', 'endpoints', 'expose'].includes(this._tab)) this._loadIfaces();
    if (this._tab === 'backup') this._loadBackup();
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
    const peak = this._fmtBytes(max) + '/s';
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

  // ---- vault ----
  // The admin console never reads secret values back — they're private to the
  // owning element (the vault lockdown). It can only list keys and set/rotate.
  async _setSecret(comp, key, value) {
    await api(`/vault/${comp}/${encodeURIComponent(key)}`,
      { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) });
    this._refresh();
  }
  async _delSecret(comp, key) {
    if (!confirm(`Delete secret ${comp} / ${key}?`)) return;
    await api(`/vault/${comp}/${encodeURIComponent(key)}`, { method: 'DELETE' });
    this._refresh();
  }

  // ---- barrier (seal state / unseal / passphrase) ----
  async _unseal(pass) {
    try {
      await api('/vault-unseal', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passphrase: pass }) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }
  async _sealVault() {
    if (!confirm('Seal the vault? Encrypted resources unmount and stateful components stop until an admin unseals again.')) return;
    try { await api('/vault-seal', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }); this._err = ''; }
    catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }
  async _rekeyVault(current, nw) {
    try {
      await api('/vault-rekey', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current, new: nw }) });
      this._err = '';
      alert('Passphrase changed (data key unchanged — nothing re-encrypted).');
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }

  _barrierView() {
    const st = this._vaultStatus;
    if (!st) return nothing;
    const badge = {
      unsealed:     ['unsealed — encryption at rest active', 'var(--bx-green, #4caf50)'],
      sealed:       ['sealed — encrypted and locked', 'var(--bx-amber, #f2a71b)'],
      unconfigured: ['unconfigured — no passphrase set, secret storage refused', 'var(--bx-red, #ef5350)'],
      plaintext:    ['plaintext — NO encryption at rest (dev mode)', 'var(--bx-red, #ef5350)'],
    }[st.mode] ?? [st.mode, 'var(--bx-muted, #868f9a)'];
    const firstTime = st.mode === 'unconfigured' || st.mode === 'plaintext';
    return html`
      <h4>encryption barrier</h4>
      <p style="margin:0 0 8px"><span class="dot" style="background:${badge[1]}"></span>${badge[0]}</p>

      ${st.mode === 'sealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value) this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="vault passphrase" size="24"
            autocomplete="off" required>
          <button class="act go">unseal</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Encrypted resources and secrets
          come back once unsealed. Also works from a terminal: <span class="mono">bx vault unseal</span>.</p>` : nothing}

      ${firstTime ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.pass.value !== f.confirm.value) { this._err = 'passphrases do not match'; return; }
            this._unseal(f.pass.value); f.reset(); }}>
          <input name="pass" type="password" placeholder="new vault passphrase" size="20"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="12"
            autocomplete="new-password" required>
          <button class="act go">${st.mode === 'plaintext' ? 'encrypt now' : 'set passphrase & unseal'}</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Creates the barrier and encrypts
          existing secrets. <b>The passphrase cannot be recovered</b> — losing it loses the data.
          To have xbind unseal itself on boot, put <span class="mono">XBIN_VAULT_PASSPHRASE</span>
          in <span class="mono">/etc/xbin/xbin.env</span> (mode 600).</p>` : nothing}

      ${st.mode === 'unsealed' ? html`
        <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
            if (f.nw.value !== f.confirm.value) { this._err = 'new passphrases do not match'; return; }
            this._rekeyVault(f.cur.value, f.nw.value); f.reset(); }}>
          <input name="cur" type="password" placeholder="current passphrase" size="17"
            autocomplete="off" required>
          <input name="nw" type="password" placeholder="new passphrase" size="15"
            autocomplete="new-password" required>
          <input name="confirm" type="password" placeholder="repeat" size="10"
            autocomplete="new-password" required>
          <button class="act">change passphrase</button>
          <button class="act rm" type="button" @click=${() => this._sealVault()}>seal now</button>
        </form>
        <p class="muted" style="font-size:11px;margin-top:6px">Changing the passphrase re-wraps the
          data key — nothing is re-encrypted. If auto-unseal is configured, update
          <span class="mono">/etc/xbin/xbin.env</span> to match.</p>` : nothing}`;
  }

  // ---- grants ----
  async _grant(from, target, role) {
    await api('/grants', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from, target, role }) });
    this._refresh();
  }
  async _revoke(g) {
    await api('/grants', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(g) });
    this._refresh();
  }

  // ---- cron ----
  async _delCron(j) {
    if (!confirm(`Delete cron job ${j.name} (${j.component})?`)) return;
    await api(`/cron/jobs/${encodeURIComponent(j.name)}?component=${encodeURIComponent(j.component)}`,
      { method: 'DELETE' });
    this._refresh();
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
          : tab === 'orgs' ? this._orgsView()
          : tab === 'permsets' ? html`<bx-admin-permsets .permsets=${this._permsets} .orgs=${this._orgs}
              .targets=${this._targetOptions()} .services=${serviceOptions(this._ifaces)}></bx-admin-permsets>`
          : tab === 'netsets' ? html`<bx-admin-netsets .netsets=${this._netsets} .targets=${this._targetOptions()}></bx-admin-netsets>`
          : tab === 'map' ? html`<bx-admin-map .users=${this._users} .orgs=${this._orgs} .wsPolicy=${this._wsPolicy}
              .showHidden=${this._showHidden} @bx-admin-show-hidden=${(e) => { this._showHidden = e.detail; }}></bx-admin-map>`
          : tab === 'components' ? (this._codeComp ? this._codeView() : this._componentsView())
          : tab === 'resources' ? this._resourcesView()
          : tab === 'vault' ? this._vaultView()
          : tab === 'roles' ? this._rolesCatalogView()
          : tab === 'grants' ? this._grantsView()
          : tab === 'providers' ? this._providersView()
          : tab === 'wiring' ? this._bindingView()
          : tab === 'endpoints' ? this._ingressEndpointsView()
          : tab === 'expose' ? this._ingressExposeView()
          : tab === 'backup' ? this._backupView()
          : this._cronView()}
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
  _fmtBytes(n) {
    n = n || 0; const u = ['B', 'K', 'M', 'G', 'T']; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i === 0 ? Math.round(n) : n.toFixed(1)) + u[i];
  }
  _fmtDur(s) {
    s = Math.max(0, s | 0);
    if (s < 60) return s + 's';
    if (s < 3600) return (s / 60 | 0) + 'm' + (s % 60) + 's';
    if (s < 86400) return (s / 3600 | 0) + 'h' + ((s % 3600) / 60 | 0) + 'm';
    return (s / 86400 | 0) + 'd' + ((s % 86400) / 3600 | 0) + 'h';
  }
  _toggleBk(path) {
    const s = new Set(this._rtOpen); s.has(path) ? s.delete(path) : s.add(path); this._rtOpen = s;
  }
  _mem(b) {
    if (b.cgroup && b.cgroup.memCurrent) return this._fmtBytes(b.cgroup.memCurrent);
    if (b.rssKb) return this._fmtBytes(b.rssKb * 1024);
    return '—';
  }
  _flowTime(f) {
    const ageS = Math.max(0, (Date.now() - f.start) / 1000);
    const age = ageS < 60 ? (ageS | 0) + 's ago' : this._fmtDur(ageS) + ' ago';
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
        case 'size': return html`<span class="num">${r.size ? this._fmtBytes(r.size) : '—'}</span>`;
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
        <div class="mono">runtime ${b.runtime || 'static'} · gen ${b.gen} · up ${this._fmtDur(b.uptimeSec)}</div>
        <div class="mono">threads ${b.threads || '—'} · restarts ${b.restarts} · last req ${b.lastReqSec < 0 ? 'never' : this._fmtDur(b.lastReqSec) + ' ago'}</div>
        ${b.cgroup ? html`<div class="mono">cgroup: ${this._fmtBytes(b.cgroup.memCurrent)}${b.cgroup.memMax > 0 ? ' / ' + this._fmtBytes(b.cgroup.memMax) : ''} · cpu ${(b.cgroup.cpuUsec / 1e6).toFixed(1)}s · ${b.cgroup.pidsCurrent} pid(s)</div>` : nothing}
        ${b.error ? html`<div class="err-pill">${b.error}</div>` : nothing}
      </div>
      <div>
        <h5>namespaces</h5>
        ${b.namespaces
          ? Object.entries(b.namespaces).map(([k, v]) => html`<div class="nsrow">${k}: <span class=${v.isolated ? 'iso' : 'shared'}>${v.isolated ? 'isolated' : 'shared'}</span> <span class="muted mono">${v.id}</span></div>`)
          : html`<span class="muted">shared with host (not sandboxed)</span>`}
      </div>
      <div>
        <h5>egress ${act ? html`· ${this._fmtBytes(act.txBytes)}↑ ${this._fmtBytes(act.rxBytes)}↓ · ${act.active} active` : nothing}</h5>
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
              <td class="mono">${this._fmtBytes(f.txBytes)}↑ ${this._fmtBytes(f.rxBytes)}↓</td>
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
    { label: 'mem', keys: ['mem'], colors: ['var(--bx-green, #4caf50)'], fmt: (c, el) => el._fmtBytes(c.mem || 0) },
    { label: 'i/o r+w', keys: ['rbps', 'wbps'], colors: ['#5b8def', 'var(--bx-red, #ef5350)'], fmt: (c, el) => `${el._fmtBytes(c.rbps || 0)}/s · ${el._fmtBytes(c.wbps || 0)}/s` },
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
            : `${this._fmtBytes(totalNow)}${m.label === 'mem' ? '' : '/s'}`}</b></div>
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
          ${this._fmtBytes(agg(list, 'mem'))} · ${this._fmtBytes(agg(list, 'io'))}/s</span></td></tr>
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
        ${kv('heap', this._fmtBytes((h.heapMB || 0) * 1e6))}
        ${kv('uptime', this._fmtDur(h.uptimeSec))}
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
    // Offload removes local bytes (after archiving) — confirm before the flip.
    if ((state === 'offloaded' || state === 'offloaded-full') &&
        !confirm(`Offload ${path}? Its ${state === 'offloaded-full' ? 'data + source' : 'data'} will be archived, then removed locally.`)) {
      this._refresh(); // revert the <select>
      return;
    }
    this._busy = path;
    try {
      await api('/lifecycle', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: path, state }) });
      await this._refresh();
      if (this._tab === 'backup') await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  _vaultView() {
    const sealedOff = this._vaults == null && !!this._vaultStatus?.sealed;
    const vs = this._vaults ?? [];
    return html`
      ${this._barrierView()}
      ${sealedOff ? html`<h4>secrets</h4><span class="muted">unavailable while sealed — unseal above to browse and edit.</span>` : nothing}
      ${!sealedOff && vs.length === 0 ? html`<h4>secrets</h4><span class="muted">no vaults hold secrets yet — set one with
        <span class="mono">bx vault set &lt;component&gt; &lt;key&gt;</span> or below.</span>` : nothing}
      ${vs.length && !sealedOff ? html`<p class="muted" style="font-size:11px">
        Secret <b>values are private to the element that owns them</b> — the admin
        console can list and set/rotate secrets but can't read them back.</p>` : nothing}
      ${vs.map((v) => html`
        <h4>${v.component}</h4>
        <table>
          ${v.keys.map((k) => html`<tr>
              <td class="mono" style="width:30%">${k}</td>
              <td class="secret">${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? html`
                <form style="display:inline-flex; gap:4px" @submit=${(e) => { e.preventDefault();
                    const nv = e.target.nv.value; this._secretEdit = null;
                    if (nv) this._setSecret(v.component, k, nv); }}>
                  <input name="nv" type="password" size="16" placeholder="new value (can't read the old one)" autofocus>
                  <button class="act" type="submit">save</button>
                  <button class="act" type="button" @click=${() => { this._secretEdit = null; }}>cancel</button>
                </form>` : '••••••••'}</td>
              <td style="text-align:right; white-space:nowrap">
                ${this._secretEdit?.comp === v.component && this._secretEdit?.key === k ? nothing
                  : html`<button class="act" @click=${() => { this._secretEdit = { comp: v.component, key: k }; }}>set</button>`}
                <button class="act rm" @click=${() => this._delSecret(v.component, k)}>del</button>
              </td></tr>`)}
        </table>`)}
      ${sealedOff ? nothing : html`<form class="inline" @submit=${(e) => { e.preventDefault();
          const f = e.target;
          if (f.comp.value && f.key.value) this._setSecret(f.comp.value.trim(), f.key.value.trim(), f.val.value);
          f.reset(); }}>
        <input name="comp" placeholder="component" size="16" list="admin-comps">
        <input name="key" placeholder="key" size="12">
        <input name="val" placeholder="value" size="18" type="password">
        <button class="act go">set secret</button>
      </form>`}
      <datalist id="admin-comps">
        ${(this._ov?.components ?? []).map((k) => html`<option value=${k.path}></option>`)}
      </datalist>`;
  }

  // ---- binding → grants: the grant table + approvals ----
  _grantsView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const comps = ov.components ?? [];
    const pending = (ov.pending ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const grants = (ov.grants ?? []).filter((g) => this._match(g.from, g.target, g.role));
    const total = (ov.grants ?? []).length + (ov.pending ?? []).length;
    return html`
      ${this._filterBar('filter grants by caller, target or role…', null, pending.length + grants.length, total)}
      ${pending.length ? html`<h4>pending requests</h4>
        <table>${pending.map((g) => html`<tr>
          <td class="mono" title=${capInfo(g.target)?.desc ?? ''}>${g.from} → ${g.target}</td>
          <td><span class="pill">${g.role}</span></td>
          <td style="text-align:right">${g.blocked
            ? html`<span class="err-pill" title=${g.blocked}>⛔ blocked by policy</span>`
            : html`<button class="act go" @click=${() => this._grant(g.from, g.target, g.role)}>approve</button>`}</td>
        </tr>`)}</table>` : nothing}

      <h4>active grants</h4>
      <table>${grants.length ? grants.map((g) => html`<tr>
        <td class="mono">${g.from} → ${g.target}</td>
        <td><span class="pill">${g.role}</span></td>
        <td style="text-align:right"><button class="act rm" @click=${() => this._revoke(g)}>revoke</button></td>
      </tr>`) : html`<tr><td class="muted">${this._q ? 'no matching grants' : 'none'}</td></tr>`}</table>

      <h4>add grant</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
          if (f.from.value && f.target.value && f.role.value) this._grant(f.from.value, f.target.value.trim(), f.role.value.trim());
          f.reset(); }}>
        <select name="from">${comps.map((k) => html`<option>${k.path}</option>`)}</select>
        <span class="muted">→</span>
        <input name="target" placeholder="apps/other or res:…/… or xbin" size="20">
        <span class="muted">:</span>
        <input name="role" placeholder="reader" size="8" value="reader">
        <button class="act go">grant</button>
      </form>`;
  }

  // ---- binding → roles: the exposed-role catalog ----
  _rolesCatalogView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const rows = (ov.components ?? []).filter((k) => k.roles)
      .flatMap((k) => Object.entries(k.roles).map(([role, desc]) => ({ path: k.path, role, desc })))
      .filter((r) => this._match(r.path, r.role, r.desc));
    return html`
      <p class="muted">Every role a component <b>exposes</b> for others to be granted (manifest
        <code>expose.roles</code>). Callers request them in <code>uses</code>; you approve in
        <a class="link" @click=${() => this._setTab('grants')}>grants</a>.</p>
      ${this._filterBar('filter roles by component, role or description…', null, rows.length, rows.length)}
      <table>
        <tr><th>component</th><th>role</th><th>description</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.path}</td><td><span class="pill">${r.role}</span></td>
          <td class="muted">${r.desc}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="3">no exposed roles${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- interfaces (typed capability wiring; docs/overview/11-interfaces.md) ----
  async _loadIfaces() {
    try {
      const [b, ing] = await Promise.all([api('/bindings'), api('/ingress')]);
      this._ifaces = b; this._ingress = ing; this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }
  // Resolves true when the bind went through. A refusal (400 "not covered",
  // 403) is shown, and the caller snaps its <select> back — lit re-renders
  // the same `selected` attributes, so the browser would keep showing the
  // refused pick as if it had been applied.
  async _bindSet(component, slot, provider) {
    try {
      await api('/bindings', {
        method: provider ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component, slot, provider }),
      });
      await this._loadIfaces();
      return true;
    } catch (e) { this._err = String(e.message ?? e); return false; }
  }
  // Replace a multi slot's whole set (bx-multiselect emits the full selection).
  async _bindSetMulti(component, slot, providers) {
    try {
      await api('/bindings', {
        method: providers.length ? 'POST' : 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(providers.length ? { component, slot, providers } : { component, slot }),
      });
      await this._loadIfaces();
    } catch (e) { this._err = String(e.message ?? e); }
  }
  // Shared iface model: the provider roster (by kind, service-aware, instances
  // expanded) and the request list — consumed by both providers + binding views.
  _ifaceModel() {
    const d = this._ifaces || {};
    const comps = d.components || [];
    const instances = d.instances || {};
    const providersByKind = {};
    for (const c of comps)
      for (const def of Object.values(c.provides || {})) {
        const list = (providersByKind[def.kind] ||= []);
        if (def.instances) {
          for (const id of Object.keys(instances[c.component] || {}).sort())
            list.push({ ref: `${c.component}#${id}`, service: def.service });
        } else {
          list.push({ ref: c.component, service: def.service });
        }
      }
    // Net builtins are NOT a fixed list any more: the owning org's network
    // sets decide (org / none / "not covered") — see _netBindRow (D54).
    const builtins = {};
    const requests = comps.flatMap((c) =>
      Object.entries(c.interfaces || {}).map(([slot, def]) => ({ comp: c.component, slot, def })));
    return { d, comps, instances, providersByKind, builtins, requests };
  }

  // The org that owns a tile (null for personal/workspace tiles) — the org
  // list already carries every org's ownedTiles.
  _orgOfTile(comp) {
    return (this._orgs ?? []).find((o) => (o.ownedTiles ?? []).includes(comp)) ?? null;
  }

  // One net slot's row (D54): options come from bx-netrules (the org's sets
  // decide what is offered and what is "not covered"), an unlisted bound ref
  // (lan:… / internet:… from `custom…`) shows as its own option, an inert
  // binding carries the server's reason, and `custom…` reveals a free-text
  // input for the D35 filtered forms.
  _netBindRow(r, d, bound, providers) {
    const pend = (d.pending ?? []).find((p) => p.component === r.comp && p.slot === r.slot);
    const org = this._orgOfTile(r.comp);
    const opts = netOptions({ org, providers, pending: pend });
    const cur = bound[0] ?? '';
    const known = opts.some((o) => o.id === cur);
    const inert = d.inert?.[r.comp]?.[r.slot];
    const ck = `bindcustom:${r.comp}:${r.slot}`;
    const custom = this._draft(ck);
    return html`<tr>
      <td class="mono">${r.comp}</td><td>${r.slot}</td><td><span class="pill">net</span></td>
      <td>
        <select @change=${(e) => {
          const v = e.target.value, el = e.target;
          if (v === '__custom') { this._setDraft(ck, cur && !known ? cur : ''); el.value = cur; return; }
          this._dropDraft(ck);
          this._bindSet(r.comp, r.slot, v).then((ok) => { if (!ok && el.isConnected) el.value = cur; });
        }}>
          ${opts.map((o) => html`<option value=${o.id} title=${o.title} ?selected=${o.id === cur} ?disabled=${!!o.disabled}>${o.label}</option>`)}
          ${cur && !known ? html`<option value=${cur} selected>${cur}</option>` : nothing}
        </select>
        ${custom !== undefined ? html`<form class="inline" style="display:inline-flex; gap:4px; margin-left:4px"
            @submit=${(e) => { e.preventDefault(); const v = e.target.ref.value.trim(); if (!v) return; this._dropDraft(ck); this._bindSet(r.comp, r.slot, v); }}>
            <input name="ref" size="28" placeholder="lan:10.0.0.0/8 · internet:api.example.com:443" .value=${custom}
              title="filtered egress (D35): lan:<ip|cidr>[:port] or internet:<host|ip|cidr>[:port][,…] — hostnames are DNS-pinned; no globs in bindings">
            <button class="act go">bind</button>
            <button class="act" type="button" @click=${() => this._dropDraft(ck)}>✕</button></form>` : nothing}
        ${inert ? html`<span class="pill pol" title=${inert}>inert</span> <span class="warn-line" style="display:inline">${inert}</span>` : nothing}
        ${org ? html`<span class="muted" style="font-size:10.5px" title="owned by org:${org.id} — its network sets bound this list">🏢 ${org.id}</span>` : nothing}
      </td></tr>`;
  }

  // ---- binding → interface providers ----
  _providersView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { comps, instances, providersByKind } = this._ifaceModel();
    const rows = comps.flatMap((c) => Object.entries(c.provides || {})
      .map(([slot, def]) => ({ comp: c.component, slot, def })))
      .filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Tiles that <b>provide</b> a typed interface others can bind to — net providers,
        service (<code>http</code>) endpoints, ingress terminators. See
        <a href="/docs/elements.md" target="_blank">docs/elements.md</a>.</p>
      ${this._filterBar('filter providers by tile, slot, kind or service…', null, rows.length, rows.length)}
      <table class="tbl">
        <tr><th>tile</th><th>slot</th><th>kind</th><th>instances</th></tr>
        ${rows.map((r) => html`<tr>
          <td class="mono">${r.comp}</td><td>${r.slot}</td>
          <td><span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}</span></td>
          <td class="mono">${r.def.instances
            ? (Object.keys(instances[r.comp] || {}).sort().map((id) => html`<span class="pill">#${id}</span>`) || nothing)
            : html`<span class="muted">—</span>`}</td></tr>`)}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no provider tiles${this._q ? ' match' : ''}${!this._q && Object.keys(providersByKind).length === 0 ? '' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- binding → binding: wire each requested slot to a provider ----
  _bindingView() {
    if (!this._ifaces) return html`<div class="muted">loading…</div>`;
    const { d, providersByKind, builtins, requests } = this._ifaceModel();
    const rows = requests.filter((r) => this._match(r.comp, r.slot, r.def.kind, r.def.service));
    return html`
      <p class="muted">Each component <b>requests</b> typed interface slots; you <b>bind</b> each to a
        provider. The binding is the authorization — unbound means no capability. Public exposure is
        under <a class="link" @click=${() => this._setTab('expose')}>ingress → services / expose</a>.</p>
      ${this._filterBar('filter by component, slot, kind or service…', null, rows.length, requests.length)}
      <table class="tbl">
        <tr><th>component</th><th>slot</th><th>kind</th><th>bound to</th></tr>
        ${rows.map((r) => {
          const raw = d.bindings?.[r.comp]?.[r.slot];
          const bound = [].concat(raw ?? []).map((x) => (x && x.ref) ? x.ref : x); // string|{ref}|array → refs
          const own = (p) => p === r.comp || p.startsWith(r.comp + '#');
          const opts = [...(builtins[r.def.kind] || []),
            ...(providersByKind[r.def.kind] || [])
              .filter((e) => !own(e.ref) &&
                (r.def.kind !== 'http' || !r.def.service || e.service === r.def.service))
              .map((e) => e.ref)];
          const kind = html`<span class="pill">${r.def.kind}${r.def.service ? ':' + r.def.service : ''}${r.def.multi ? ' ×N' : ''}</span>`;
          if (r.def.kind === 'net' && !r.def.multi) return this._netBindRow(r, d, bound, opts);
          if (r.def.multi) {
            return html`<tr>
              <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
              <td><bx-multiselect .options=${opts} .selected=${bound} placeholder="— unbound —"
                  @change=${(e) => this._bindSetMulti(r.comp, r.slot, e.detail.selected)}></bx-multiselect></td></tr>`;
          }
          return html`<tr>
            <td class="mono">${r.comp}</td><td>${r.slot}</td><td>${kind}</td>
            <td><select @change=${(e) => this._bindSet(r.comp, r.slot, e.target.value)}>
              <option value="" ?selected=${bound.length === 0}>— unbound —</option>
              ${opts.map((p) => html`<option value=${p} ?selected=${bound[0] === p}>${p}</option>`)}
            </select></td></tr>`;
        })}
        ${rows.length === 0 ? html`<tr><td class="muted" colspan="4">no components request interfaces${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>`;
  }

  // ---- ingress (published endpoints; docs/ingress.md) ----
  _ingKey(comp, slot) { return comp + '\x00' + slot; }
  _ingEditFor(e) {
    // The working row state: pending edits over the current binding.
    return this._ingEdit?.[this._ingKey(e.component, e.slot)]
      ?? { source: e.source || '', host: e.host || '', zone: e.zone || '', listen: e.listen || '' };
  }
  _ingSetEdit(e, patch) {
    const k = this._ingKey(e.component, e.slot);
    this._ingEdit = { ...(this._ingEdit || {}), [k]: { ...this._ingEditFor(e), ...patch } };
  }
  async _ingPublish(e) {
    const ed = this._ingEditFor(e);
    if (!ed.source) return;
    const body = { component: e.component, slot: e.slot, provider: ed.source };
    if (e.kind === 'http') {
      // Exactly one of host/zone — send whichever is filled (server validates).
      if (ed.zone) body.zone = ed.zone; else body.host = ed.host;
    } else if (ed.listen) body.listen = ed.listen;
    try {
      await api('/bindings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const k = this._ingKey(e.component, e.slot);
      const { [k]: _, ...rest } = this._ingEdit || {}; this._ingEdit = rest;
      await this._loadIfaces();
    } catch (err) { this._err = String(err.message ?? err); }
  }
  async _ingUnpublish(e) {
    try {
      await api('/bindings', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: e.component, slot: e.slot }) });
      await this._loadIfaces();
    } catch (err) { this._err = String(err.message ?? err); }
  }
  // ---- ingress → services / expose: publish tiles to the outside ----
  _ingressExposeView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const streams = d.streams || [];
    const sources = (kind) => kind === 'http' ? ['runtime', ...(d.terminators || [])] : ['runtime'];
    const all = d.exposes || [];
    const exposes = all.filter((e) => this._match(e.component, e.slot, e.kind, e.host, e.zone));
    return html`
      <p class="muted">Tiles declare <code>exposes</code> in their manifest; <b>binding a slot to an
        ingress source publishes it</b> to the outside — anonymous traffic, confined to the tile's
        declared public paths. Unbound = unreachable, exactly like interfaces.
        See <a href="/docs/ingress.md" target="_blank">docs/ingress.md</a>.</p>
      ${all.length === 0 ? html`<div class="muted">No tile declares <code>exposes</code> yet. Add an
        <span class="mono">exposes</span> block to a tile's <span class="mono">xbin.json</span>
        (http paths, or a tcp/udp port), or import the <b>Public HTTPS (Traefik)</b> tile.</div>` : html`
      ${this._filterBar('filter exposed endpoints…', null, exposes.length, all.length)}
      <table class="tbl">
        <tr><th>tile</th><th>endpoint</th><th>source</th><th>route</th><th></th><th>state</th></tr>
        ${exposes.map((e) => {
          const ed = this._ingEditFor(e);
          const bound = !!e.source;
          const dirty = ed.source !== (e.source || '') || ed.host !== (e.host || '')
            || ed.zone !== (e.zone || '') || ed.listen !== (e.listen || '');
          const endpoint = e.kind === 'http'
            ? html`<span class="pill">http</span> <span class="muted">${(e.paths || []).join(' ')}</span>`
            : html`<span class="pill">${e.proto}:${e.port}</span>`;
          const routeEd = e.kind === 'http'
            ? html`<input class="mono" style="width:12em" placeholder="host (blog.example.com)"
                     .value=${ed.zone ? '' : ed.host} ?disabled=${!!ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { host: ev.target.value.trim(), zone: '' })}>
                   <input class="mono" style="width:11em" placeholder="or zone (*.sites.…)"
                     .value=${ed.zone}
                     @input=${(ev) => this._ingSetEdit(e, { zone: ev.target.value.trim() })}>`
            : html`<input class="mono" style="width:8em" placeholder=":${e.port} (host port)"
                     .value=${ed.listen}
                     @input=${(ev) => this._ingSetEdit(e, { listen: ev.target.value.trim() })}>`;
          let state = html`<span class="muted">unbound — not reachable</span>`;
          if (e.blocked) state = html`<span class="st-failed">⛔ ${e.blocked}</span>`;
          else if (bound && e.kind === 'http') state = html`<span class="st-healthy">public: ${e.zone || e.host}</span>`;
          else if (bound) {
            const st = streams.find((s) => s.component === e.component && s.slot === e.slot);
            state = st?.error ? html`<span class="st-failed">⚠ ${st.error}</span>`
              : html`<span class="st-healthy">host ${e.listen || ':' + e.port} → :${e.port}${st ? ` (${st.active} active)` : ''}</span>`;
          }
          return html`<tr>
            <td class="mono">${e.component}</td><td>${e.slot} ${endpoint}</td>
            <td><select ?disabled=${!!e.blocked} @change=${(ev) => this._ingSetEdit(e, { source: ev.target.value })}>
              <option value="" ?selected=${!ed.source}>— unbound —</option>
              ${sources(e.kind).map((s) => html`<option value=${s} ?selected=${ed.source === s}>${s}</option>`)}
            </select></td>
            <td>${routeEd}</td>
            <td>
              ${ed.source && (dirty || !bound) ? html`<button class="act go" @click=${() => this._ingPublish(e)}>publish</button>` : nothing}
              ${bound ? html`<button class="act rm" @click=${() => this._ingUnpublish(e)}>unpublish</button>` : nothing}
            </td>
            <td>${state}</td></tr>`;
        })}
        ${exposes.length === 0 ? html`<tr><td class="muted" colspan="6">no matching endpoints</td></tr>` : nothing}
      </table>`}`;
  }

  // ---- ingress → endpoints: live routes, listeners, terminators ----
  _ingressEndpointsView() {
    const d = this._ingress;
    if (!d) return html`<span class="muted">loading…</span>`;
    const routes = (d.routes || []).filter((r) => this._match(r.host, r.component, r.slot, r.source));
    const streams = d.streams || [];
    const forwards = d.forwards || [];
    const lst = d.httpListener || {};
    return html`
      <p class="muted">The live public routing table — what the outside can reach right now. Publish
        or unpublish under <a class="link" @click=${() => this._setTab('expose')}>services / expose</a>.</p>
      <div class="muted" style="margin:2px 0 10px">
        builtin HTTP listener: ${lst.listen ? html`<b>${lst.listen}</b> (${lst.tls ? 'TLS' : 'no TLS — front it, or use the Traefik tile'})` : 'off — start xbind with --ingress-listen'}
      </div>
      <h4>HTTP routes</h4>
      ${this._filterBar('filter routes by host or tile…', null, routes.length, (d.routes || []).length)}
      <table class="tbl">
        <tr><th>public host</th><th>→ tile</th><th>via</th></tr>
        ${routes.map((r) => html`<tr>
          <td class="mono">${r.host}</td>
          <td class="mono">${r.component}.${r.slot}</td>
          <td>${r.source}${r.zone ? html` <span class="muted">(zone ${r.zone})</span>` : nothing}</td></tr>`)}
        ${routes.length === 0 ? html`<tr><td class="muted" colspan="3">no HTTP routes${this._q ? ' match' : ''}</td></tr>` : nothing}
      </table>
      ${streams.length ? html`<h4>stream listeners (tcp / udp)</h4>
        <table class="tbl">
          <tr><th>host listen</th><th>→ tile</th><th>proto</th><th>state</th></tr>
          ${streams.map((s) => html`<tr>
            <td class="mono">${s.listen}</td>
            <td class="mono">${s.component}.${s.slot} → :${s.port}</td>
            <td>${s.proto}</td>
            <td>${s.error ? html`<span class="st-failed">⚠ ${s.error}</span>` : html`<span class="st-healthy">${s.active} active</span>`}</td></tr>`)}
        </table>` : nothing}
      ${forwards.length ? html`<h4>terminator forward doors</h4>
        <table class="tbl">
          <tr><th>terminator tile</th><th>state</th></tr>
          ${forwards.map((f) => html`<tr>
            <td class="mono">${f.source}</td>
            <td>${f.error ? html`<span class="st-failed">⚠ ${f.error}</span>` : html`<span class="st-healthy">up</span>`}</td></tr>`)}
        </table>` : nothing}`;
  }

  // ---- backup (docs/overview/14-lifecycle.md) ----
  async _loadBackup() {
    try {
      const [ifaces, sched] = await Promise.all([api('/bindings'), api('/backup-schedule')]);
      this._ifaces = ifaces;
      this._schedules = sched.schedules || [];
      this._err = '';
      // Load versions for disabled components so the offload gate is computable
      // (does a post-disable backup exist?) without expanding each one.
      const disabled = (this._ov?.components || []).filter((c) => c.state === 'disabled');
      await Promise.all(disabled.map((c) => this._loadVersions(c.path)));
    } catch (e) { this._err = String(e.message ?? e); }
  }

  // Components that provide an `archive` interface (candidate archivers).
  _archivers() {
    const out = [];
    for (const c of (this._ifaces?.components || []))
      for (const def of Object.values(c.provides || {}))
        if (def.kind === 'archive') out.push(c.component);
    return out;
  }

  // '*' sets the workspace default; provider '' clears an override.
  async _setArchiver(comp, provider) {
    try {
      const body = JSON.stringify(provider ? { component: comp, slot: '@archive', provider } : { component: comp, slot: '@archive' });
      await api('/bindings', { method: provider ? 'POST' : 'DELETE', headers: { 'Content-Type': 'application/json' }, body });
      await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _toggleVersions(comp) {
    const s = new Set(this._verOpen);
    if (s.has(comp)) { s.delete(comp); this._verOpen = s; return; }
    s.add(comp); this._verOpen = s;
    await this._loadVersions(comp);
  }
  async _loadVersions(comp) {
    try {
      const d = await api('/backups?component=' + encodeURIComponent(comp));
      this._versions = { ...this._versions, [comp]: d.versions || [] };
    } catch (e) { this._versions = { ...this._versions, [comp]: [] }; this._err = String(e.message ?? e); }
  }

  async _backupNow(comp) {
    this._busy = comp;
    try {
      await api('/backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp }) });
      this._verOpen = new Set(this._verOpen).add(comp);
      await this._loadVersions(comp);
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  async _restoreVersion(comp, version) {
    if (!confirm(`Restore ${comp} from ${version}? This replaces its current data/source.`)) return;
    this._busy = comp;
    try {
      await api('/restore', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ component: comp, version }) });
      await this._refresh();
    } catch (e) { this._err = String(e.message ?? e); }
    finally { this._busy = null; }
  }

  // Restore one file from a version — streamed back and offered as a download.
  async _restoreFile(comp, version) {
    const path = prompt('File path within the archive (e.g. source/index.html or data/kv.json):');
    if (!path) return;
    try {
      const r = await xbin.fetch('/api/xbin/restore', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, version, file: path }),
      });
      if (!r.ok) throw new Error((await r.json()).error || r.status);
      xbin.download(path.split('/').pop() || 'file', await r.blob());
    } catch (e) { this._err = String(e.message ?? e); }
  }

  async _setSchedule(comp, every, keep) {
    if (!every.trim()) return;
    try {
      await api('/backup-schedule', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ component: comp, schedule: '@every ' + every.trim(), retention: parseInt(keep, 10) || 0 }) });
      await this._loadBackup();
    } catch (e) { this._err = String(e.message ?? e); }
  }
  async _clearSchedule(comp) {
    try { await api('/backup-schedule?component=' + encodeURIComponent(comp), { method: 'DELETE' }); await this._loadBackup(); }
    catch (e) { this._err = String(e.message ?? e); }
  }

  _backupView() {
    const ov = this._ov, ifaces = this._ifaces;
    if (!ov || !ifaces) return html`<div class="muted">loading…</div>`;
    const archivers = this._archivers();
    if (archivers.length === 0)
      return html`<p class="muted">No archiver installed. Import the <b>S3 Archiver</b> tile (or another
        <code>archive</code> provider) from the Tile Manager, then pick it as the default below.</p>`;
    const defArch = ifaces.bindings?.['*']?.['@archive'] || '';
    const comps = (ov.components || []).filter((c) => !archivers.includes(c.path)); // an archiver isn't its own target
    const schedFor = (p) => this._schedules.find((s) => s.component === p);
    return html`
      <p class="muted">Back up a component (its source + data + terminal layer) to an archiver, offload to
        free disk, or restore a version/file. Vault is not backed up. See
        <a href="/docs/overview/14-lifecycle.md" target="_blank">the lifecycle overview</a>.</p>
      <h3>Default archiver</h3>
      <select @change=${(e) => this._setArchiver('*', e.target.value)}>
        <option value="" ?selected=${!defArch}>— none —</option>
        ${archivers.map((a) => html`<option value=${a} ?selected=${defArch === a}>${a}</option>`)}
      </select>
      <span class="muted" style="margin-left:8px">used unless a component overrides it</span>

      <h3>Components</h3>
      <table class="tbl">
        <tr><th>component</th><th>lifecycle</th><th>archiver</th><th>schedule</th><th></th></tr>
        ${comps.map((c) => this._backupRow(c, archivers, defArch, schedFor(c.path)))}
      </table>`;
  }

  _backupRow(c, archivers, defArch, sched) {
    const override = this._ifaces.bindings?.[c.path]?.['@archive'] || '';
    const busy = this._busy === c.path;
    const open = this._verOpen.has(c.path);
    return html`
      <tr>
        <td class="mono">${c.path}</td>
        <td>${this._lifecycleControls(c)}</td>
        <td><select @change=${(e) => this._setArchiver(c.path, e.target.value)}>
          <option value="" ?selected=${!override}>default${defArch ? ' (' + defArch + ')' : ''}</option>
          ${archivers.map((a) => html`<option value=${a} ?selected=${override === a}>${a}</option>`)}
        </select></td>
        <td class="mono">${sched
          ? html`${sched.schedule}${sched.retention ? ' ·keep ' + sched.retention : ''}
              <a class="link" title="remove schedule" @click=${() => this._clearSchedule(c.path)}>✕</a>`
          : this._scheduleForm(c.path)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => !busy && this._backupNow(c.path)}>${busy ? 'working…' : 'back up'}</a>
          · <a class="link" @click=${() => this._toggleVersions(c.path)}>versions${open ? ' ▾' : ''}</a>
        </td>
      </tr>
      ${open ? html`<tr><td colspan="5">${this._versionsList(c.path)}</td></tr>` : nothing}`;
  }

  // Guided lifecycle controls (docs/overview/14-lifecycle.md). Offload is deliberately a
  // two-step, safe flow: you must DISABLE first (stops the backend → a consistent
  // db), then take a backup, and only then does offload un-gray — so you never
  // free local data without a verified, stopped-state snapshot.
  _lifecycleControls(c) {
    const st = c.state || 'enabled';
    const busy = this._busy === c.path;
    const act = (label, state, opts = {}) => html`<a
      class="link ${opts.gated ? 'gated' : ''}" title=${opts.title || ''}
      @click=${() => !busy && !opts.gated && this._setLifecycle(c.path, state)}>${busy ? '…' : label}</a>`;

    if (st === 'enabled') return html`enabled · ${act('disable', 'disabled')}`;
    if (st === 'disabled') {
      const ready = this._hasPostDisableBackup(c);
      const why = ready ? 'Archive + remove local data (source kept).'
        : 'Back up first — offload needs a backup taken while disabled (a consistent snapshot).';
      const whyFull = ready ? 'Archive + remove data AND source.' : why;
      return html`disabled · ${act('enable', 'enabled')}
        · ${act('offload', 'offloaded', { gated: !ready, title: why })}
        · ${act('offload+src', 'offloaded-full', { gated: !ready, title: whyFull })}
        ${ready ? nothing : html`<span class="muted" style="font-size:11px"> (back up to enable offload)</span>`}`;
    }
    // offloaded / offloaded-full
    return html`${st} · ${act('restore', 'enabled')}`;
  }

  // Offload is allowed only once a backup exists that was taken AFTER the tile was
  // disabled (its snapshot is consistent because the backend is stopped).
  _hasPostDisableBackup(c) {
    if ((c.state || 'enabled') !== 'disabled' || !c.stateAt) return false;
    const since = Date.parse(c.stateAt);
    const vers = this._versions[c.path];
    return Array.isArray(vers) && vers.some((v) => Date.parse(v.time) >= since);
  }

  _scheduleForm(comp) {
    return html`<span>
      <input class="ev" placeholder="24h" style="width:48px">
      <input class="kp" placeholder="keep" style="width:44px">
      <a class="link" @click=${(e) => { const s = e.target.parentElement; this._setSchedule(comp, s.querySelector('.ev').value, s.querySelector('.kp').value); }}>set</a>
    </span>`;
  }

  _versionsList(comp) {
    const vers = this._versions[comp];
    if (!vers) return html`<span class="muted">loading…</span>`;
    if (vers.length === 0) return html`<span class="muted">no backups yet</span>`;
    return html`<table class="tbl" style="margin:2px 0 4px 16px">
      ${vers.map((v) => html`<tr>
        <td class="mono">${v.version}</td>
        <td class="muted">${v.time}</td>
        <td class="mono">${this._fmtBytes(v.size)}</td>
        <td style="white-space:nowrap">
          <a class="link" @click=${() => this._restoreVersion(comp, v.version)}>restore</a>
          · <a class="link" @click=${() => this._restoreFile(comp, v.version)}>file…</a>
        </td>
      </tr>`)}
    </table>`;
  }

  _cronView() {
    const jobs = this._cron ?? [];
    return html`
      ${jobs.length === 0 ? html`<span class="muted">no scheduled jobs.</span>` : html`
        <table>
          <tr><th>name</th><th>component</th><th>schedule</th><th>path</th><th>role</th><th></th></tr>
          ${jobs.map((j) => html`<tr>
            <td class="mono">${j.name}</td>
            <td class="mono">${j.component}</td>
            <td class="mono">${j.schedule}</td>
            <td class="mono">${j.path}</td>
            <td><span class="pill">${j.role}</span></td>
            <td style="text-align:right"><button class="act rm" @click=${() => this._delCron(j)}>delete</button></td>
          </tr>`)}
        </table>`}`;
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
    if (org) body.orgs = [{ org, ...BxAdmin.PRESETS[f.orgPreset?.value || 'developer'] }];
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
  _groupsDatalist() {
    return html`<datalist id="idp-groups-seen">
      ${(this._authSettings?.sso?.groupSync?.knownGroups ?? []).map((g) => html`<option value=${g}></option>`)}
    </datalist>`;
  }
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
    const p = this._presetOf(m);
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
              if (e.target.checked) return this._setMembership(o.id, u.id, BxAdmin.PRESETS.developer);
              if (synced && !confirm(`${u.id} is in ${o.id} via IdP group ${(m.viaGroups ?? []).join(', ')}. Removing them here lasts until their next sign-in — remove them from the group, or delete the rule on the org card. Remove anyway?`)) { e.target.checked = true; return; }
              return this._dropMembership(o.id, u.id);
            }}> <span class="mono">${o.id}</span>${o.name && o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}</label>
          ${m ? html`
            <select title="role preset" ?disabled=${synced} @change=${(e) => { const p = BxAdmin.PRESETS[e.target.value]; if (p) this._setMembership(o.id, u.id, p); }}>
              ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${this._presetOf(m) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
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
    return (s < 60 ? (s | 0) + 's' : this._fmtDur(s)) + ' ago';
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

  // ---- click-through editors (shared by the users + orgs tabs) ----
  // Workspaces are small, so everything about permissions is enumerable and
  // clickable: people are picked from chip dropdowns, tile targets from a
  // datalist of real paths + patterns — no free-text specs to mistype.

  // _peoplePicker: chip dropdown over the workspace's users. onChange fires
  // with the full selection on every toggle (immediate save — click-through).
  _peoplePicker(selected, onChange, only = null) {
    const ids = only ?? (this._users ?? []).map((u) => u.id);
    const opts = ids.map((id) => {
      const u = (this._users ?? []).find((x) => x.id === id);
      return { value: id, label: u?.name && u.name !== id ? `${id} — ${u.name}` : id };
    });
    return html`<bx-multiselect style="min-width:120px" .options=${opts} .selected=${selected ?? []}
      placeholder="— nobody —" @change=${(e) => onChange(e.detail.selected)}></bx-multiselect>`;
  }

  // Draft plumbing for the row editors, keyed by a context id
  // ("user:bob:tiles", "team:sales/backend:create", …).
  _draft(k) { return this._drafts?.[k]; }
  _setDraft(k, v) { this._drafts = { ...(this._drafts ?? {}), [k]: v }; }
  _dropDraft(k) { const d = { ...(this._drafts ?? {}) }; delete d[k]; this._drafts = d; }

  // ---- test surface (hack/ui-harness) ----
  // Stable names over the tile's private state (see bx-shell's testApi).
  testApi() {
    const a = this;
    // Drafts live on the tab element that owns the namespace; the rest here.
    const owner = (k) => {
      const tag = k.startsWith('permset:') ? 'bx-admin-permsets' : k.startsWith('netset:') ? 'bx-admin-netsets' : null;
      return (tag && a.renderRoot.querySelector(tag)?.testApi()) || { draft: (x) => a._draft(x), setDraft: (x, v) => a._setDraft(x, v), dropDraft: (x) => a._dropDraft(x) };
    };
    return {
      get tab() { return a._tab; },
      draft: (k) => owner(k).draft(k),
      setDraft: (k, v) => owner(k).setDraft(k, v),
      dropDraft: (k) => owner(k).dropDraft(k),
    };
  }
  _toggleDraft(k, seed) { this._draft(k) ? this._dropDraft(k) : this._setDraft(k, seed()); }

  // Target / service suggestions (shared.js) rendered once per view as a
  // <datalist> the row editors' inputs attach to.
  _targetOptions() { return targetOptions(this._ov); }
  _targetDatalist() { return targetDatalist(this._targetOptions()); }
  _serviceDatalist() { return serviceDatalist(serviceOptions(this._ifaces)); }

  // _tilesEditor: rows of [target (datalist)] [level] [×] editing a
  // pattern→level map; save calls onSave(map). orgID adds the inert warning.
  _tilesEditor(ctx, onSave, orgID = null) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, patch) => this._setDraft(ctx, d.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    return html`
      <div style="padding:6px 8px; background:var(--bx-panel-2, #2b3038); border-radius:6px">
        ${d.map((r, i) => html`<div style="display:flex; gap:5px; align-items:center; margin-bottom:4px">
          <input list="tile-targets" size="26" placeholder="path, prefix/* or *" .value=${r.target}
            @input=${(e) => upd(i, { target: e.target.value })}>
          <select @change=${(e) => upd(i, { level: e.target.value })}>
            ${['read', 'write', 'terminal', 'none'].map((l) => html`<option value=${l} ?selected=${r.level === l}
              title=${l === 'none' ? 'authoritative: overrides org membership, patterns and defaults (D31)' : ''}>${l === 'none' ? 'none (exclude)' : l}</option>`)}
          </select>
          <button class="act rm" title="remove entry" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
        </div>`)}
        <div style="display:flex; gap:5px; align-items:center">
          <button class="act" @click=${() => this._setDraft(ctx, [...d, { target: '', level: 'write' }])}>+ entry</button>
          <button class="act go" @click=${async () => {
            const tiles = {};
            for (const r of d) if (r.target.trim()) tiles[r.target.trim()] = r.level;
            await onSave(tiles);
            if (!this._err) this._dropDraft(ctx);
          }}>save</button>
          <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
          <span class="muted" style="font-size:10.5px">read = see it · write = use/edit · terminal = root shell on it ·
            exact entries are authoritative (none = exclude, D31)</span>
        </div>
      </div>`;
  }

  // _patternsEditor: same, for plain pattern lists (canCreate).
  _patternsEditor(ctx, onSave, orgID = null) {
    const d = this._draft(ctx) ?? [];
    const upd = (i, v) => this._setDraft(ctx, d.map((r, j) => (j === i ? v : r)));
    return html`
      <div style="padding:6px 8px; background:var(--bx-panel-2, #2b3038); border-radius:6px">
        ${d.map((r, i) => html`<div style="display:flex; gap:5px; align-items:center; margin-bottom:4px">
          <input list="tile-targets" size="26" placeholder="prefix/* (create namespace)" .value=${r}
            @input=${(e) => upd(i, e.target.value)}>
          <button class="act rm" @click=${() => this._setDraft(ctx, d.filter((_, j) => j !== i))}>✕</button>
        </div>`)}
        <div style="display:flex; gap:5px; align-items:center">
          <button class="act" @click=${() => this._setDraft(ctx, [...d, ''])}>+ pattern</button>
          <button class="act go" @click=${async () => {
            await onSave(d.map((s) => s.trim()).filter(Boolean));
            if (!this._err) this._dropDraft(ctx);
          }}>save</button>
          <button class="act" @click=${() => this._dropDraft(ctx)}>cancel</button>
          <span class="muted" style="font-size:10.5px">creating a tile auto-grants the creator terminal on it</span>
        </div>
      </div>`;
  }

  // ---- ownership & organisations (docs/auth.md §Ownership, D24–D28) ----
  // Orgs are flat member lists with org-wide roles on org-OWNED tiles; the
  // ws-admin delegates approval via allowances/permission sets. This tab is
  // the workspace-admin console — org admins use tiles/organisations.
  async _orgAPI(method, path, body) {
    try {
      await api(path, body === undefined ? { method }
        : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
    await this._refresh();
  }

  // ---- policy-row editor (workspace + per-org ceilings, D20) ----
  _polDraft(key) { return this._polEdit?.[key]; }
  _polSet(key, rows) { this._polEdit = { ...(this._polEdit ?? {}), [key]: rows }; }
  _polStop(key) { const e = { ...(this._polEdit ?? {}) }; delete e[key]; this._polEdit = e; }

  async _polSave(key) {
    const rows = (this._polDraft(key) ?? [])
      .map((r) => {
        const mayCall = r.mayCallText.split(',').map((s) => s.trim()).filter(Boolean);
        const row = { tiles: r.tiles.trim() };
        if (r.deny.length) row.deny = r.deny;
        if (mayCall.length) row.mayCall = mayCall;
        return row;
      })
      .filter((r) => r.tiles);
    await this._orgAPI('PUT', key ? `/orgs/${encodeURIComponent(key)}/policy` : '/policy', { policy: rows });
    if (!this._err) this._polStop(key);
  }

  _policyEditor(key, rows) {
    const draft = this._polDraft(key);
    if (!draft) {
      return html`
        ${(rows ?? []).map((r) => html`<div class="mono" style="font-size:11px">
          tiles=${r.tiles}${r.deny?.length ? ` deny=${r.deny.join(',')}` : ''}${r.mayCall?.length ? ` mayCall=${r.mayCall.join(',')}` : ''}</div>`)}
        ${!(rows ?? []).length ? html`<div class="muted" style="font-size:11px">no rows (no ceiling)</div>` : nothing}
        <button class="act" style="margin-top:3px" @click=${() => this._polSet(key,
          (rows ?? []).map((r) => ({ tiles: r.tiles, deny: [...(r.deny ?? [])], mayCallText: (r.mayCall ?? []).join(', ') })))}>edit</button>`;
    }
    const upd = (i, patch) => this._polSet(key, draft.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    return html`
      <table class="flowtab" style="margin-top:4px">
        ${draft.map((r, i) => html`<tr>
          <td><input size="14" placeholder="* (all covered tiles)" .value=${r.tiles}
                @input=${(e) => upd(i, { tiles: e.target.value })}></td>
          <td style="white-space:nowrap">${['net', 'gpu', 'xbin-caps', 'ingress'].map((k) => html`
            <label class="muted" style="font-size:10.5px; margin-right:5px">
              <input type="checkbox" .checked=${r.deny.includes(k)}
                @change=${(e) => upd(i, { deny: e.target.checked ? [...r.deny, k] : r.deny.filter((d) => d !== k) })}>deny ${k}</label>`)}</td>
          <td><input size="20" placeholder="mayCall: a/*, res:a/* (empty = any)" .value=${r.mayCallText}
                @input=${(e) => upd(i, { mayCallText: e.target.value })}></td>
          <td><button class="act rm" title="remove row" @click=${() => this._polSet(key, draft.filter((_, j) => j !== i))}>✕</button></td>
        </tr>`)}
      </table>
      <div style="margin-top:4px">
        <button class="act" @click=${() => this._polSet(key, [...draft, { tiles: '*', deny: [], mayCallText: '' }])}>+ row</button>
        <button class="act go" @click=${() => this._polSave(key)}>save</button>
        <button class="act" @click=${() => this._polStop(key)}>cancel</button>
        <span class="muted" style="font-size:10.5px; margin-left:6px">
          deny strips the capability; mayCall allow-lists external call targets
          (a tile's own scope is always exempt); deny beats every allowance</span>
      </div>`;
  }

  async _createOrg(f) {
    await this._orgAPI('POST', '/orgs', { id: f.id.value.trim(), name: f.orgname.value.trim() });
    if (!this._err) f.reset();
  }

  // Member presets (D25 UI): admin / developer / viewer over the three knobs.
  static PRESETS = {
    admin: { level: 'terminal', create: true, admin: true },
    developer: { level: 'terminal', create: true, admin: false },
    viewer: { level: 'read', create: false, admin: false },
  };
  _presetOf(m) {
    for (const [name, p] of Object.entries(BxAdmin.PRESETS)) {
      if (m.level === p.level && !!m.create === p.create && !!m.admin === p.admin) return name;
    }
    return 'custom';
  }

  // One membership row on the org card: every control is one PUT on the
  // single-membership route. Synced rows (⟳) are read-mostly — their knobs
  // follow the IdP-group rule at every sign-in — so they're disabled until
  // detached; suspension stays editable (it survives a re-sync).
  _memberRow(o, m) {
    const save = (patch) => this._setMembership(o.id, m.id, patch);
    const synced = m.via === 'sso';
    const lock = synced ? 'synced from an IdP group — detach to edit by hand' : '';
    return html`<tr style=${m.suspended ? 'opacity:.55' : ''}>
      <td class="mono">${m.id}${m.suspended ? html` <span class="pill">suspended</span>` : nothing}${synced ? html` <span class="pill sync"
          title="synced from IdP group ${(m.viaGroups ?? []).join(', ')} — follows the group at every sign-in">⟳ ${(m.viaGroups ?? []).join(', ')}</span>` : nothing}</td>
      <td><select title=${lock || 'role preset'} ?disabled=${synced} @change=${(e) => {
            const p = BxAdmin.PRESETS[e.target.value];
            if (p) save(p);
          }}>
          ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${this._presetOf(m) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
        </select></td>
      <td><select title=${lock || 'org-wide level on tiles the org OWNS'} ?disabled=${synced} @change=${(e) => save({ level: e.target.value })}>
          ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${m.level === l}>${l}</option>`)}
        </select></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.create} ?disabled=${synced}
            @change=${(e) => save({ create: e.target.checked })} title=${lock || 'may create org-owned tiles'}> create</label></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.admin} ?disabled=${synced}
            @change=${(e) => save({ admin: e.target.checked })} title=${lock || 'org management: members, ACLs, transfers, allowance approvals'}> admin</label></td>
      <td><label class="muted" style="font-size:11px"><input type="checkbox" .checked=${!!m.suspended}
            @change=${(e) => save({ suspended: e.target.checked })}
            title="pause this membership — it confers nothing while suspended, but keeps its knobs (D34)"> susp</label></td>
      <td style="text-align:right; white-space:nowrap">
        ${synced ? html`<button class="act" title="stop syncing this membership; it becomes manual"
          @click=${async () => { await save({ via: '' }); if (!this._err) this._flash(`${o.id}: ${m.id} is now a manual member`); }}>detach</button>` : nothing}
        <button class="act rm" title=${synced ? 'removes now — comes back at their next sign-in while the rule stands' : 'remove from the org'}
          @click=${() => this._dropMembership(o.id, m.id)}>remove</button></td>
    </tr>`;
  }

  // Add-member row: pick a person and a preset (not just "developer").
  _addMemberRow(o, addable) {
    return html`<div style="margin-top:4px; display:flex; gap:6px; align-items:center; flex-wrap:wrap">
      <select id="add-${o.id}">
        <option value="">add member…</option>
        ${addable.map((u) => html`<option value=${u.id}>${u.id}${u.name && u.name !== u.id ? ` — ${u.name}` : ''}</option>`)}
      </select>
      <select id="addp-${o.id}" title="role in the org">
        <option value="developer">as developer</option>
        <option value="viewer">as viewer</option>
        <option value="admin">as org admin</option>
      </select>
      <button class="act go" @click=${() => {
        const sel = this.renderRoot.querySelector(`#add-${CSS.escape(o.id)}`);
        const p = this.renderRoot.querySelector(`#addp-${CSS.escape(o.id)}`)?.value || 'developer';
        if (!sel?.value) return;
        this._setMembership(o.id, sel.value, BxAdmin.PRESETS[p]);
      }}>add</button>
      <span class="muted" style="font-size:10.5px">or add an IdP-group rule below</span>
    </div>`;
  }

  // IdP groups → members: the org's rules (docs/auth.md §Group sync, D53).
  _idpGroupsEditor(o) {
    const rules = o.ssoGroups ?? [];
    const sso = this._authSettings?.sso ?? {};
    const hint = this._idpGroupHint(sso.preset);
    const known = (sso.groupSync?.knownGroups ?? []).filter((g) => !rules.some((r) => r.group.toLowerCase() === g.toLowerCase()));
    const save = (next) => this._orgAPI('PUT', `/orgs/${encodeURIComponent(o.id)}/sso-groups`, { rules: next });
    const knobs = (r) => ({ level: r.level, create: !!r.create, admin: !!r.admin });
    return html`<div style="margin-top:8px">
      <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">IdP groups → members</span>
      <div style="margin-top:3px">
        ${rules.map((r) => html`<span class="rule">
          <span class="mono">${r.group}</span> →
          <select title="role preset for members synced from this group" @change=${(e) => {
              const p = BxAdmin.PRESETS[e.target.value];
              if (p) save(rules.map((x) => (x.group === r.group ? { group: r.group, ...p } : x)));
            }}>
            ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${this._presetOf(knobs(r)) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
          </select>
          <button class="act rm" title="delete the rule (synced members stay until their next sign-in)"
            @click=${() => save(rules.filter((x) => x.group !== r.group))}>✕</button>
        </span>`)}
        ${!rules.length ? html`<span class="muted" style="font-size:11px">no rules — members are added by hand</span>` : nothing}
      </div>
      <div style="margin-top:4px; display:flex; gap:6px; align-items:center; flex-wrap:wrap">
        <input id="rule-${o.id}" list="idp-groups-seen" size="28" placeholder=${hint.placeholder}
          @keydown=${(e) => { if (e.key === 'Enter') e.target.nextElementSibling?.nextElementSibling?.click(); }}>
        <select id="rulep-${o.id}" title="role for members synced from the group">
          <option value="developer">as developer</option>
          <option value="viewer">as viewer</option>
          <option value="admin">as org admin</option>
        </select>
        <button class="act go" @click=${() => {
          const inp = this.renderRoot.querySelector(`#rule-${CSS.escape(o.id)}`);
          const p = this.renderRoot.querySelector(`#rulep-${CSS.escape(o.id)}`)?.value || 'developer';
          const g = inp?.value.trim();
          if (!g) return;
          if (rules.some((x) => x.group.toLowerCase() === g.toLowerCase())) { this._err = `rule for ${g} already exists`; return; }
          save([...rules, { group: g, ...BxAdmin.PRESETS[p] }]).then(() => { if (!this._err && inp) inp.value = ''; });
        }}>add rule</button>
        ${known.length ? html`<span class="muted" style="font-size:10.5px">${known.length} unmapped group${known.length === 1 ? '' : 's'} seen at sign-ins — start typing</span>` : nothing}
      </div>
      <div class="muted" style="font-size:10.5px; margin-top:3px">${hint.text} Synced members show ⟳ and follow the group at
        every sign-in — leave the group, lose the membership. ${!sso.enabled ? 'SSO is off — rules take effect once a provider is active (sign-in tab).' : ''}</div>
    </div>`;
  }
  _idpGroupHint(preset) {
    switch (preset) {
      case 'google': return { placeholder: 'sales@corp.com', text: 'Google Workspace: name the group by its email address.' };
      case 'github': return { placeholder: 'acme/infra', text: 'GitHub: name a team as org/team-slug (or an org by its login).' };
      case 'entra': return { placeholder: 'group object id', text: 'Entra: match the group values in the ID token (object IDs unless the app emits names).' };
      case 'keycloak': return { placeholder: '/sales', text: 'Keycloak: group paths as emitted by the Group Membership mapper.' };
      default: return { placeholder: 'group name', text: 'Rules match the groups claim exactly as sent — see sign-in › groups seen for the spelling.' };
    }
  }

  async _transferTile(tile) {
    const to = prompt(`Transfer ${tile} to (user:<id>, org:<id>, or "workspace"):`);
    if (to == null) return;
    await this._orgAPI('POST', '/owner', { tile, to: to.trim() === 'workspace' ? '' : to.trim() });
  }

  _orgCard(o) {
    const opath = `/orgs/${encodeURIComponent(o.id)}`;
    const memberIds = new Set((o.members ?? []).map((m) => m.id));
    const addable = (this._users ?? []).filter((u) => !memberIds.has(u.id));
    const setNames = Object.keys(this._permsets?.sets ?? {});
    const allowKey = `org:${o.id}:allow`;
    const members = o.members ?? [];
    const synced = members.filter((m) => m.via === 'sso').length;
    return html`
      <div style="border:1px solid var(--bx-border, #363c45); border-radius:6px; padding:8px 10px; margin:8px 0">
        <div style="display:flex; align-items:baseline; gap:8px; flex-wrap:wrap">
          <b class="mono">${o.id}</b>
          <span class="muted">${o.name !== o.id ? o.name : ''}</span>
          <span class="muted" style="font-size:11px">${members.length} member${members.length === 1 ? '' : 's'}${synced ? ` · ${synced} synced` : ''}</span>
          <span style="flex:1"></span>
          <button class="act rm" title=${(o.ownedTiles ?? []).length ? 'transfer its owned tiles away first' : 'delete the org'}
            @click=${() => confirm(`Delete org ${o.id}?`) && this._orgAPI('DELETE', opath)}>del</button>
        </div>

        <table style="margin-top:6px">
          ${members.length ? html`<tr><th>member</th><th>preset</th><th>level</th><th>create</th><th>admin</th><th>susp</th><th></th></tr>` : nothing}
          ${repeat(members, (m) => m.id, (m) => this._memberRow(o, m))}
        </table>
        ${this._addMemberRow(o, addable)}
        ${this._idpGroupsEditor(o)}

        <div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">delegation (ws-admin, D26/D28)</span>
          <div style="display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:3px">
            <label class="muted" style="font-size:11px">sets
              <bx-multiselect style="min-width:130px"
                .options=${setNames.map((n) => ({ value: n, label: n }))}
                .selected=${o.sets ?? []} placeholder="— none —"
                @change=${(e) => this._orgAPI('PATCH', opath, { sets: e.detail.selected })}></bx-multiselect></label>
            <span class="muted" style="font-size:11px">extra allow
              ${(o.allow ?? []).length ? o.allow.map((a) => html`<span class="pill mono" title=${describeAllow(a)}>${a}</span>`) : html`<span class="muted">none</span>`}
              <button class="act" data-edit-allow ?disabled=${!!this._draft(`orgallow:${o.id}`)}
                @click=${() => this._setDraft(`orgallow:${o.id}`, { rows: (o.allow ?? []).map(parseAllow), err: '' })}>edit</button></span>
          </div>
          ${this._draft(`orgallow:${o.id}`) ? this._orgAllowEditor(o, opath) : nothing}
          ${(o.resolvedAllow ?? []).length ? html`<div style="margin-top:3px">
            <span class="muted" style="font-size:10.5px">org admins may self-approve:</span>
            ${o.resolvedAllow.map((a) => html`<span class="pill mono" title=${describeAllow(a)}>${a}</span>`)}</div>`
            : html`<div class="muted" style="font-size:10.5px; margin-top:3px">no allowances — every grant/binding goes through a workspace admin</div>`}
        </div>

        ${this._orgNetBlock(o, opath)}

        ${(o.ownedTiles ?? []).length ? html`<div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">owned tiles</span>
          <div style="margin-top:3px">${o.ownedTiles.map((p) => html`
            <span class="pill mono">${p} <a class="link" title="transfer ownership"
              @click=${() => this._transferTile(p)}>⇄</a></span>`)}</div>
        </div>` : nothing}

        <div style="margin-top:8px">
          <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">org policy ceiling (applies to owned tiles)</span>
          ${this._policyEditor(o.id, o.policy)}
        </div>
        <span class="muted" style="display:block; margin-top:4px; font-size:10.5px" data-k=${allowKey}>
          allowance grammar: res:/gpu:/cap:/net:internet|host|lan:…|provider:…/iface:&lt;svc&gt;/ingress:host|zone|listen:&lt;range&gt;/tile:&lt;pat&gt; — xbin is never delegable</span>
      </div>`;
  }

  // Org card → network (D54): which network sets the org holds, what its own
  // tiles therefore reach, and the one-line semantics. ws-admin only (the
  // server refuses the field from org admins).
  _orgNetBlock(o, opath) {
    const names = Object.keys(this._netsets?.sets ?? {}).sort();
    const sets = o.netSets ?? [];
    const rules = o.resolvedNet ?? [];
    return html`<div style="margin-top:8px">
      <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">network (ws-admin, D54)</span>
      <div style="display:flex; gap:8px; align-items:center; flex-wrap:wrap; margin-top:3px">
        <label class="muted" style="font-size:11px">network sets
          <bx-multiselect style="min-width:130px"
            .options=${names.map((n) => ({ value: n, label: n }))}
            .selected=${sets} placeholder="— none —"
            @change=${(e) => this._orgAPI('PATCH', opath, { netSets: e.detail.selected })}></bx-multiselect></label>
        ${!names.length ? html`<a class="link" style="font-size:11px" @click=${() => this._setTab('netsets')}>create one in network sets →</a>` : nothing}
      </div>
      ${sets.length ? html`
        <div style="margin-top:3px">
          <span class="muted" style="font-size:10.5px">org tiles reach:</span>
          ${rules.filter((r) => r !== 'host').map((r) => html`<span class="pill mono" title=${r}>${ruleLabel(r)}</span>`)}
          ${o.netHost ? html`<span class="pill pol" title="a set grants host networking: every org-bound tile and terminal shares the host's network stack — no relay, no filtering, no metering">⚠ host networking</span>` : nothing}
          ${!rules.length ? html`<span class="muted" style="font-size:11px">nothing — the attached sets carry no rules (airgapped, incl. DNS)</span>` : nothing}
        </div>
        <div class="muted" style="font-size:10.5px; margin-top:3px">org-owned tiles that declare
          <span class="mono">net</span> bind to <span class="mono">org</span> by default; org admins may bind
          anything inside it; terminals on org tiles get the same reach — no term-net needed.</div>`
        : html`<div class="muted" style="font-size:10.5px; margin-top:3px">no network sets — org tiles'
          <span class="mono">net</span> slots stay unbound until a workspace admin binds them explicitly;
          terminals on them fall back to term-net.</div>`}
    </div>`;
  }

  // An org's extra allow entries (on top of its sets): the same typed rows.
  _orgAllowEditor(o, opath) {
    const key = `orgallow:${o.id}`;
    const d = this._draft(key);
    const wire = d.rows.map(fmtAllow);
    const ok = !d.rows.some((r) => allowProblem(r)) && new Set(wire).size === wire.length;
    return html`<div class="editor" style="margin-top:6px">
      <div class="muted" style="margin-bottom:2px">Extra entries for <b>this org only</b> (on top of its sets) — its admins may approve, on their own tiles:</div>
      ${allowRows(d.rows, (rows) => this._setDraft(key, { ...this._draft(key), rows }), { gotoTab: (x) => this._setTab(x) })}
      ${d.err ? html`<div class="err" role="alert">${d.err}</div>` : nothing}
      <div class="orow" style="margin-top:6px">
        <button class="act go" data-save-allow ?disabled=${!ok} @click=${async () => {
          try {
            await api(opath, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allow: wire.filter(Boolean) }) });
            this._err = ''; this._dropDraft(key);
          } catch (e) { this._setDraft(key, { ...this._draft(key), err: String(e.message ?? e) }); }
          await this._refresh();
        }}>save</button>
        <button class="act" @click=${() => this._dropDraft(key)}>cancel</button>
      </div>
    </div>`;
  }

  _orgsView() {
    const orgs = this._orgs ?? [];
    return html`
      ${this._targetDatalist()}
      ${this._groupsDatalist()}
      ${this._serviceDatalist()}
      <h4>organizations</h4>
      ${orgs.length ? repeat(orgs, (o) => o.id, (o) => this._orgCard(o))
        : html`<p class="muted">No orgs. An org is a flat member list with org-wide roles on the
          tiles the org <b>owns</b> (ownership is assigned at create and transferable — D24/D25).
          Attach permission sets to delegate grant/binding approval to its admins.</p>`}

      <h4>add org</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createOrg(e.target); }}>
        <input name="id" placeholder="org id" size="14" required>
        <input name="orgname" placeholder="display name" size="14">
        <button class="act go">create</button>
      </form>

      <h4>workspace defaults</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Baseline visibility every user gets (D27) — pattern → level.</p>
      ${this._defaultsEditor()}

      <h4>new accounts</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        What every NEW account starts with (D52) — copied onto the row at creation
        (admin-added, invited, or SSO auto-provisioned) on top of what the creator
        specifies; editable per user afterwards. This is where "everyone from the
        SSO domain lands in org X as a developer" lives. Never grants admin.</p>
      ${this._newUsersEditor()}

      <h4>tile creation</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Who may create tiles outside an organisation (D52). Workspace-owned tiles
        are always an admin act; this governs non-admins' personal tiles.</p>
      <select @change=${(e) => this._putDefaults({ tileCreation: e.target.value })}>
        <option value="any" ?selected=${(this._tileCreation ?? 'any') === 'any'}>any — users create personal tiles, and org-owned ones where they hold Create</option>
        <option value="org-only" ?selected=${this._tileCreation === 'org-only'}>org-only — non-admins may only create organisation-owned tiles (needs Create in an org)</option>
      </select>

      <h4>workspace policy</h4>
      <p class="muted" style="font-size:11px; max-width:60ch">
        Pattern-keyed ceiling on what tiles may be granted, applied to EVERY tile (org and
        permission-set rows add on top; any deny wins; deny beats every allowance).</p>
      ${this._policyEditor('', this._wsPolicy)}

      <p class="muted" style="font-size:11px; margin-top:10px; max-width:60ch">
        Effective access is a union: workspace admin · tile OWNER (terminal) · org member level /
        org-admin terminal on org-owned tiles · org shares · a user's own entries · workspace
        defaults. Org admins manage members and org-tile ACLs in the
        <span class="mono">tiles/organisations</span> tile; permission sets, allowances, policy
        and org create/delete stay here.</p>`;
  }

  _defaultsEditor() {
    const key = 'ws:defaults';
    const d = this._draft(key);
    if (!d) {
      return html`
        ${Object.entries(this._defaults ?? {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
        ${!Object.keys(this._defaults ?? {}).length ? html`<span class="muted" style="font-size:11px">none</span>` : nothing}
        <button class="act" style="margin-left:4px" @click=${() => this._toggleDraft(key,
          () => Object.entries(this._defaults ?? {}).map(([target, level]) => ({ target, level })))}>edit</button>`;
    }
    return this._tilesEditor(key, (tiles) => this._orgAPI('PUT', '/defaults', { defaultTiles: tiles }));
  }

  // ---- new-account defaults + tile-creation policy (D52) ----
  // PUT /defaults replaces only the keys given, so each control saves its
  // own setting without clobbering the others.
  async _putDefaults(patch) {
    try {
      const d = await api('/defaults', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(patch) });
      this._defaults = d.defaultTiles ?? {}; this._newUsers = d.newUsers ?? {};
      this._tileCreation = d.tileCreation ?? 'any'; this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  _newUsersEditor() {
    const nu = this._newUsers ?? {};
    const tilesKey = 'ws:newusers:tiles';
    const createKey = 'ws:newusers:create';
    const rows = nu.orgs ?? [];
    const unused = (this._orgs ?? []).filter((o) => !rows.some((r) => r.org === o.id));
    const saveOrgs = (orgs) => this._putDefaults({ newUsers: { ...nu, orgs } });
    const row = (label, body) => html`<div style="display:flex; gap:6px; align-items:center; flex-wrap:wrap; margin-bottom:4px">
      <span style="min-width:9ch; font-weight:600">${label}</span>${body}</div>`;
    return html`
      <div style="font-size:12px; max-width:64ch">
        ${row('tiles', html`
          ${Object.entries(nu.tiles ?? {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
          ${!Object.keys(nu.tiles ?? {}).length ? html`<span class="muted">none</span>` : nothing}
          <button class="act" @click=${() => this._toggleDraft(tilesKey,
            () => Object.entries(nu.tiles ?? {}).map(([target, level]) => ({ target, level })))}>edit</button>`)}
        ${this._draft(tilesKey) ? this._tilesEditor(tilesKey, (tiles) => this._putDefaults({ newUsers: { ...nu, tiles } })) : nothing}
        ${row('create', html`
          ${(nu.canCreate ?? []).map((c) => html`<span class="pill">create·${c}</span>`)}
          ${!(nu.canCreate ?? []).length ? html`<span class="muted">none</span>` : nothing}
          <button class="act" @click=${() => this._toggleDraft(createKey, () => [...(nu.canCreate ?? [])])}>edit</button>`)}
        ${this._draft(createKey) ? this._patternsEditor(createKey, (canCreate) => this._putDefaults({ newUsers: { ...nu, canCreate } })) : nothing}
        ${row('terminals', html`
          <label class="muted"><input type="checkbox" .checked=${!!nu.termApi}
            @change=${(e) => this._putDefaults({ newUsers: { ...nu, termApi: e.target.checked } })}> term-api</label>
          <label class="muted"><input type="checkbox" .checked=${!!nu.termNet}
            @change=${(e) => this._putDefaults({ newUsers: { ...nu, termNet: e.target.checked } })}> term-net</label>`)}
        ${row('orgs', html`
          ${rows.map((r) => html`<span style="display:inline-flex; gap:4px; align-items:center; border:1px solid var(--bx-border, #363c45); border-radius:6px; padding:2px 6px">
            <span class="mono">${r.org}</span>
            <select title="org-wide level on tiles the org owns"
              @change=${(e) => saveOrgs(rows.map((x) => (x.org === r.org ? { ...x, level: e.target.value } : x)))}>
              ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${r.level === l}>${l}</option>`)}
            </select>
            <label class="muted"><input type="checkbox" .checked=${!!r.create} title="may create org-owned tiles"
              @change=${(e) => saveOrgs(rows.map((x) => (x.org === r.org ? { ...x, create: e.target.checked } : x)))}> create</label>
            <button class="act rm" title="stop auto-joining this org" @click=${() => saveOrgs(rows.filter((x) => x.org !== r.org))}>✕</button>
          </span>`)}
          ${!rows.length ? html`<span class="muted">none — new accounts join no org</span>` : nothing}
          ${unused.length ? html`<select @change=${(e) => { const id = e.target.value; e.target.value = ''; if (id) saveOrgs([...rows, { org: id, level: 'read' }]); }}>
            <option value="">+ org…</option>
            ${unused.map((o) => html`<option value=${o.id}>${o.id}${o.name && o.name !== o.id ? ` — ${o.name}` : ''}</option>`)}
          </select>` : nothing}`)}
      </div>`;
  }
}

customElements.define('bx-admin', BxAdmin);
