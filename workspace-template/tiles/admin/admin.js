/**
 * <bx-admin> — the workspace admin console (tiles/admin). Full owner-view
 * into the running system, powered by xbind's admin-capable endpoints via
 * the xbin:admin capability (see xbin.json / API.md).
 *
 * This file is the router: the two-level nav (GROUPS), hash deep-links and
 * their alias map, the shared lists every tab reads (_refresh: overview,
 * users, orgs, policy, sets, defaults, requests, sessions, vault status),
 * and the global err / notice slots. Every tab is an element under tabs/
 * (imported relatively — a sandboxed tile may import its own siblings)
 * that takes its inputs as properties and reports back through composed
 * events: bx-admin-err (message; '' clears), bx-admin-notice, bx-admin-tab,
 * bx-admin-refresh, bx-admin-show-hidden. All reads go through xbin.fetch
 * (admin identity attributed by frame token) and refresh on the
 * grants/reload/users event stream. docs/maintenance.md → "The admin
 * console's tabs" is the contributor's guide.
 */
import { LitElement, html, css, nothing } from 'lit';

import { xbinApi as api } from '/vendor/bx-kit.js';
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
import './tabs/users.js';
import './tabs/signin.js';
import './tabs/sessions.js';
import './tabs/runtime.js';
import { targetOptions, serviceOptions, WithDrafts } from './shared.js';

export class BxAdmin extends WithDrafts(LitElement) {
  static properties = {
    _tab: { state: true },
    _sub: { state: true },      // the tab's drill-in from the hash (#orgs/<id>)
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
    _notice: { state: true },   // green success line (never the red .err slot)
    _reqs: { state: true },     // pending human access requests (D36)
    _defaults: { state: true }, // defaultTiles map (D27)
    _newUsers: { state: true }, // new-account defaults {tiles, canCreate, termApi, termNet, orgs} (D52)
    _tileCreation: { state: true }, // 'any' | 'org-only' (D52)
    _drafts: { state: true },   // click-through editor drafts, keyed by context
    _showHidden: { state: true }, // reveal hidden (state=hidden) tiles in lists (D42)
    _authSettings: { state: true },
    _alerts: { state: true }, // {tokenLoginDisabled, hasAdminUser, canDisable}
    _ifaces: { state: true },   // {bindings, components} — interface wiring
    _err: { state: true },
    _denied: { state: true },
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
    // #tab, or #tab/sub for a tab's drill-in (organisations: #orgs/<id>).
    const [h, ...rest] = location.hash.replace(/^#/, '').split('/');
    // Back-compat: honor a couple of old hash ids so bookmarks don't 404.
    const alias = { overview: 'components', runtime: 'components', interfaces: 'providers' };
    const want = alias[h] || h;
    this._tab = BxAdmin.tabsFlat().some((t) => t.id === want) ? want : 'components';
    this._sub = this._tab === want ? rest.join('/') : '';
    this._err = '';
    this._alerts = [];
    this._denied = false;
  }

  _setGroup(g) { this._setTab(g.tabs[0].id); }

  _setTab(t) {
    [this._tab, this._sub = ''] = t.split(/\/(.*)/s); // 'orgs/acme' → tab orgs, sub acme
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
    // Clicking the component list again drops any code drill-in (back to the list).
    this.renderRoot.querySelector('bx-admin-runtime')?.closeCode();
    if (t === 'permsets' || t === 'orgs') this._loadIfaces(); // the service datalist
    if (t === 'sessions') this._loadSessions();
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.xbin?.events.on((e) => {
      // Coalesced: a bulk disable or an SSO sign-in's group sync fires a burst.
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok' || e.type === 'users') this._refreshSoon();
    });
    // Tab elements report into the router's global slots.
    this.addEventListener('bx-admin-err', (e) => { this._err = e.detail; });
    this.addEventListener('bx-admin-notice', (e) => { this._err = ''; this._flash(e.detail, 6000); });
    this.addEventListener('bx-admin-refresh', () => this._refreshSoon());
    this.addEventListener('bx-admin-tab', (e) => this._setTab(e.detail));
    this._refresh();
    // Prime the data the initial tab needs (constructor set _tab from the hash
    // but doesn't fetch; _setTab does that on later clicks).
    if (this._tab === 'permsets' || this._tab === 'orgs') this._loadIfaces();
    if (this._tab === 'sessions') this._loadSessions();
    // The sessions tab polls (logins/logouts raise no event); the runtime
    // tabs poll /runtime from their own element.
    this._rtTimer = setInterval(() => { if (this._tab === 'sessions') this._loadSessions(); }, 10000);
  }
  disconnectedCallback() {
    super.disconnectedCallback(); this._off?.(); clearInterval(this._rtTimer); clearTimeout(this._refreshT);
  }
  _refreshSoon() { clearTimeout(this._refreshT); this._refreshT = setTimeout(() => this._refresh(), 150); }
  // Green self-clearing notice (the red .err slot is for failures).
  _flash(msg, ms = 4000) {
    this._notice = msg; clearTimeout(this._flashT);
    this._flashT = setTimeout(() => { this._notice = ''; }, ms);
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
        ${tab === 'users' ? html`<bx-admin-users .users=${this._users} .orgs=${this._orgs} .sessions=${this._sessions} .reqs=${this._reqs}
              .authSettings=${this._authSettings} .targets=${this._targetOptions()}></bx-admin-users>`
          : tab === 'sign-in' ? html`<bx-admin-signin .authSettings=${this._authSettings} .users=${this._users} .orgs=${this._orgs}></bx-admin-signin>`
          : tab === 'sessions' ? html`<bx-admin-sessions .sessions=${this._sessions}></bx-admin-sessions>`
          : tab === 'orgs' ? html`<bx-admin-orgs .sub=${this._sub} .orgs=${this._orgs} .users=${this._users} .wsPolicy=${this._wsPolicy}
              .permsets=${this._permsets} .netsets=${this._netsets} .defaults=${this._defaults} .newUsers=${this._newUsers}
              .tileCreation=${this._tileCreation} .authSettings=${this._authSettings}
              .targets=${this._targetOptions()} .services=${serviceOptions(this._ifaces)}></bx-admin-orgs>`
          : tab === 'permsets' ? html`<bx-admin-permsets .permsets=${this._permsets} .orgs=${this._orgs}
              .targets=${this._targetOptions()} .services=${serviceOptions(this._ifaces)}></bx-admin-permsets>`
          : tab === 'netsets' ? html`<bx-admin-netsets .netsets=${this._netsets} .targets=${this._targetOptions()}></bx-admin-netsets>`
          : tab === 'map' ? html`<bx-admin-map .users=${this._users} .orgs=${this._orgs} .wsPolicy=${this._wsPolicy}
              .showHidden=${this._showHidden} @bx-admin-show-hidden=${(e) => { this._showHidden = e.detail; }}></bx-admin-map>`
          : tab === 'components' || tab === 'resources' ? html`<bx-admin-runtime view=${tab} .ov=${this._ov} .vaultStatus=${this._vaultStatus}
              .showHidden=${this._showHidden} @bx-admin-show-hidden=${(e) => { this._showHidden = e.detail; }}></bx-admin-runtime>`
          : tab === 'vault' ? html`<bx-admin-vault .vaults=${this._vaults} .vaultStatus=${this._vaultStatus} .components=${this._ov?.components ?? []}></bx-admin-vault>`
          : ['roles', 'grants', 'providers', 'wiring'].includes(tab) ? html`<bx-admin-binding view=${tab} .ov=${this._ov} .orgs=${this._orgs}></bx-admin-binding>`
          : tab === 'endpoints' || tab === 'expose' ? html`<bx-admin-ingress view=${tab}></bx-admin-ingress>`
          : tab === 'backup' ? html`<bx-admin-backup .components=${this._ov?.components ?? []}></bx-admin-backup>`
          : html`<bx-admin-cron .cron=${this._cron}></bx-admin-cron>`}
      </div>`;
  }

  // ---- sessions (the view is tabs/sessions.js; the list is polled here) ----
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
  // ---- test surface (hack/ui-harness) ----
  // Stable names over the tile's private state (see bx-shell's testApi).
  testApi() {
    const a = this;
    // Drafts live on the tab element that owns the namespace; the rest here.
    const owner = (k) => {
      const tag = k.startsWith('permset:') ? 'bx-admin-permsets' : k.startsWith('netset:') ? 'bx-admin-netsets'
        : k.startsWith('bindcustom:') ? 'bx-admin-binding' : k.startsWith('orgallow:') || k.startsWith('ws:') ? 'bx-admin-orgs'
        : k.startsWith('user:') ? 'bx-admin-users' : null;
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

  // Tile-target suggestions (shared.js), passed to the tabs whose row
  // editors render the <datalist>.
  _targetOptions() { return targetOptions(this._ov); }

}

customElements.define('bx-admin', BxAdmin);
