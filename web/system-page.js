/**
 * <bx-system-page> — xbind's system components page, /xbin/system (D227,
 * /docs/sealed.md): the surface that is there at all times — it ships with
 * xbind, so a workspace whose shell and admin console predate system
 * components (their listings leave them out for that reason) still shows
 * an admin each one. Served as it is (no HTML transform) and only
 * top-level: framed, it renders nothing.
 *
 * A system component is a part of xbin that runs like a tile: its code is
 * the installed xbin release's, so there is nothing to open, edit, deploy
 * or delete. What an admin decides is here: who has access (read, or
 * none), whether it is on, its resource limits, and what its interface
 * slots are bound to. Everything is read (GET /api/xbin/sealed) and
 * written (the routes a tile has: /access, /lifecycle, /bindings) with the
 * person's own session; xbind judges every act.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';

const pageCss = css`
  :host { display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  main { max-width: 760px; margin: 0 auto; padding: 24px 16px 64px; box-sizing: border-box; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
  header .grow { flex: 1; }
  h1 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-heading, -0.01em); margin: 0; }
  h2 { font: inherit; font-weight: 600; margin: 0; overflow-wrap: anywhere; }
  h3 { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6);
    text-transform: uppercase; letter-spacing: 0.04em; margin: 16px 0 4px; }
  .lead { color: var(--bx-muted, #A3A6B6); max-width: 72ch; margin: 8px 0 16px; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .muted { color: var(--bx-muted, #A3A6B6); }
  .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  .banner { border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 12px 0;
    background: var(--bx-danger-bg, #3A2B32); overflow-wrap: anywhere; }
  .card { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028);
    padding: 12px; margin: 12px 0; }
  .top { display: flex; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
  .top .grow { flex: 1; min-width: 14em; }
  .badge { display: inline-block; padding: 0 8px; border-radius: var(--bx-radius, 2px); border: 1px solid var(--bx-border-strong, #666A7E);
    font: var(--bx-font-meta, 400 12px/18px "Instrument Sans", system-ui, sans-serif); white-space: nowrap; }
  .badge.ok { border-color: var(--bx-ok, #A3CF5E); color: var(--bx-ok, #A3CF5E); }
  .badge.held { border-color: var(--bx-danger, #FF7A7A); color: var(--bx-danger, #FF7A7A); }
  .badge.off { color: var(--bx-muted, #A3A6B6); }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: 2px 12px; margin: 8px 0 0; }
  dt { color: var(--bx-muted, #A3A6B6); }
  dd { margin: 0; overflow-wrap: anywhere; }
  ul { list-style: none; margin: 0; padding: 0; }
  li { display: flex; align-items: center; gap: 8px; padding: 4px 0; flex-wrap: wrap; }
  li .grow { flex: 1; min-width: 10em; overflow-wrap: anywhere; }
  .row { display: flex; gap: 6px; margin-top: 6px; flex-wrap: wrap; align-items: center; }
  button, select, input { box-sizing: border-box; min-height: var(--bx-control-h, 28px); font: inherit;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  button { padding: 0 12px; cursor: pointer; font-weight: 600; }
  button:hover { background: var(--bx-hover, #2A2B34); }
  button.rm { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  select, input { padding: 0 8px; }
  input { flex: 1; min-width: 12em; }
  .empty { color: var(--bx-muted, #A3A6B6); }
`;

// req(path, init) → {status, ok, body}: an /api/xbin call with the
// person's own session (the same-origin cookie).
async function req(path, init = {}) {
  const r = await fetch(`/api/xbin${path}`, {
    ...init, headers: { Accept: 'application/json', ...(init.body ? { 'Content-Type': 'application/json' } : {}) },
  });
  return { status: r.status, ok: r.ok, body: (await r.json().catch(() => ({}))) ?? {} };
}
const post = (path, body, method = 'POST') => req(path, { method, body: JSON.stringify(body) });

// stateOf(c) → {label, cls}: what one line says about a component now.
export function stateOf(c) {
  if (c.state && c.state !== 'enabled') return { label: 'Off', cls: 'off' };
  if (c.hold) return { label: 'Held', cls: 'held' };
  switch (c.backend) {
    case 'healthy': return { label: 'Running', cls: 'ok' };
    case 'building': case 'starting': return { label: 'Starting', cls: '' };
    case 'failed': return { label: 'Failed', cls: 'held' };
  }
  return { label: 'Ready', cls: 'ok' };
}

export class BxSystemPage extends LitElement {
  static properties = {
    _data: { state: true },    // GET /sealed {release, components}; null until loaded
    _access: { state: true },  // path → GET /access entries
    _defaults: { state: true }, // path → GET /access defaults: what reaches everyone in the workspace
    _agent: { state: true },   // the component that is the workspace agent here ('' when none)
    _people: { state: true },  // {users: [{id, name}], orgs: [{id, name}]}
    _denied: { state: true },  // not an admin
    _err: { state: true },
  };
  static styles = pageCss;

  constructor() {
    super();
    this._framed = false;
    try { this._framed = window.top !== window.self; } catch { this._framed = true; }
    this._data = null;
    this._access = {};
    this._defaults = {};
    this._agent = '';
    this._people = { users: [], orgs: [] };
  }

  connectedCallback() {
    super.connectedCallback();
    if (!this._framed) this._load();
  }

  /** The test surface (hack/ui-harness): what the page shows. */
  testApi() {
    return {
      components: () => (this._data?.components || []).map((c) => ({ path: c.path, state: stateOf(c).label, hold: c.hold?.code || '' })),
      denied: () => !!this._denied,
      reload: () => this._load(),
    };
  }

  async _load() {
    this._err = '';
    try {
      const r = await req('/sealed');
      if (r.status === 403) { this._denied = true; this._data = { components: [] }; return; }
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
      this._data = r.body;
      const access = {}, defaults = {};
      await Promise.all((r.body.components || []).map(async (c) => {
        const a = await req(`/access?tile=${encodeURIComponent(c.path)}`);
        access[c.path] = a.ok ? (a.body.entries || []) : [];
        defaults[c.path] = a.ok ? (a.body.defaults || []) : [];
      }));
      this._access = access;
      this._defaults = defaults;
      const ws = await req('/delegate/workspace');
      this._agent = ws.ok ? (ws.body.agent || '') : '';
      const [u, o] = await Promise.all([req('/users-directory'), req('/orgs')]);
      const list = (r, key) => (!r.ok ? [] : Array.isArray(r.body) ? r.body : Array.isArray(r.body[key]) ? r.body[key] : []);
      this._people = { users: list(u, 'users'), orgs: list(o, 'orgs') };
    } catch (e) { this._err = e.message; this._data ??= { components: [] }; }
  }

  async _act(run) {
    this._err = '';
    try {
      const r = await run();
      if (!r.ok) throw new Error(r.body.error || `error ${r.status}`);
    } catch (e) { this._err = e.message; }
    await this._load();
  }

  _setState(c, state) { return this._act(() => post('/lifecycle', { component: c.path, state })); }
  _setAccess(c, kind, id, level) { return this._act(() => post('/access', { tile: c.path, kind, id, level }, 'PUT')); }
  _bind(c, slot, provider) {
    if (!provider) return undefined;
    return this._act(() => post('/bindings', { component: c.path, slot: slot.slot, provider, ...(slot.multi ? { add: true } : {}) }));
  }
  _unbind(c, slot, provider) { return this._act(() => post('/bindings', { component: c.path, slot: slot.slot, provider }, 'DELETE')); }

  _grant(c, e) {
    e.preventDefault();
    const f = e.target;
    const [kind, id] = (f.elements.who.value || '').split(':');
    if (kind && id) this._setAccess(c, kind, id, 'read');
  }

  _renderAccess(c) {
    const entries = this._access[c.path] || [];
    const everyone = (this._defaults[c.path] || []).length > 0;
    const has = new Set(entries.filter((e) => e.source === 'exact').map((e) => `${e.kind}:${e.id}`));
    const pick = [
      ...this._people.users.map((u) => ({ v: `user:${u.id}`, t: u.name && u.name !== u.id ? `${u.name} (${u.id})` : u.id })),
      ...this._people.orgs.map((o) => ({ v: `org:${o.id}`, t: `${o.name || o.id} — everyone in the organisation` })),
    ].filter((p) => !has.has(p.v));
    return html`
      <h3>Who has access</h3>
      ${everyone ? html`<div class="everyone">Everyone in this workspace
          <span class="small muted">— ${c.path === this._agent ? html`set by Workspace agents: who gets one (Everyone), which` : 'the workspace\'s default access'}
            gives it to every person. Change it in the admin console, <a href="/c/tiles/admin/#orgs">organisations → workspace defaults</a>${c.path === this._agent
              ? html`, or with <span class="mono">PUT /api/xbin/delegate/workspace {who}</span> (<a href="/docs/workspace-agent.md#setting-it-up">how</a>)` : nothing}.</span></div>` : nothing}
      ${entries.length ? html`<ul>${entries.map((e) => html`<li>
          <span class="grow">${e.kind === 'org' ? 'Organisation' : 'Person'} <b>${e.id}</b>
            <span class="small muted">${e.source === 'exact' ? '' : `· from the pattern ${String(e.source).replace(/^pattern:/, '')}`}</span></span>
          ${e.source === 'exact' && e.level !== 'none'
            ? html`<button class="rm" @click=${() => this._setAccess(c, e.kind, e.id, '')}>Remove</button>` : nothing}
        </li>`)}</ul>` : everyone ? nothing : html`<div class="empty">Nobody but workspace admins.</div>`}
      ${pick.length ? html`<form class="row" @submit=${(e) => this._grant(c, e)}>
          <select name="who" aria-label="Who gets access to ${c.title || c.path}">
            ${pick.map((p) => html`<option value=${p.v}>${p.t}</option>`)}
          </select>
          <button type="submit">Give access</button>
        </form>` : nothing}`;
  }

  _renderSlots(c) {
    if (!c.slots?.length) return nothing;
    return html`
      <h3>Connections</h3>
      <ul>${c.slots.map((s) => html`<li>
        <span class="grow"><b>${s.slot}</b> <span class="small muted">${s.service || s.kind}</span><br>
          ${s.bound.length ? s.bound.map((b) => html`<span class="mono">${b}</span>
              <button class="rm" @click=${() => this._unbind(c, s, b)}>Disconnect</button> `)
            : html`<span class="empty">Not connected</span>`}
        </span>
        <form class="row" @submit=${(e) => { e.preventDefault(); this._bind(c, s, e.target.elements.provider.value.trim()); }}>
          <input name="provider" placeholder="what provides it, e.g. apps/llm-gw" aria-label="What ${s.slot} of ${c.title || c.path} connects to">
          <button type="submit">Connect</button>
        </form>
      </li>`)}</ul>`;
  }

  _renderOne(c) {
    const st = stateOf(c);
    const on = !c.state || c.state === 'enabled';
    const logs = `/api/xbin/logs?component=${encodeURIComponent(c.path)}&tail=65536`;
    return html`<section class="card" data-path=${c.path}>
      <div class="top">
        <div class="grow">
          <h2>${c.title || c.name}</h2>
          <div class="small muted">System component · managed by xbin · <span class="mono">${c.path}</span></div>
        </div>
        <span class="badge ${st.cls}">${st.label}</span>
        ${on ? html`<button @click=${() => this._setState(c, 'disabled')}>Turn off</button>`
             : html`<button @click=${() => this._setState(c, 'enabled')}>Turn on</button>`}
      </div>
      ${c.hold && on ? html`<div class="banner" role="alert">${c.title || c.name} ${c.hold.reason}</div>` : nothing}
      <dl>
        <dt>Version</dt><dd>xbin ${this._data.release} <span class="small muted mono">code ${c.digest}</span></dd>
        ${c.partition?.length ? html`<dt>People's data</dt><dd>${c.partitioned ? 'Kept apart: one instance per person' : 'One shared instance (a development xbin without sandboxes)'}</dd>` : nothing}
        ${c.stamp ? html`<dt>Data</dt><dd>format ${c.stamp.data}${c.stamp.release ? html` <span class="small muted">last written by xbin ${c.stamp.release}</span>` : nothing}</dd>` : nothing}
        <dt>Logs</dt><dd><a href=${logs} target="_blank" rel="noopener">Open the latest log</a></dd>
        <dt>Limits</dt><dd><a href="/xbin/limits?tile=${encodeURIComponent(c.path)}">Memory and disk</a></dd>
      </dl>
      ${this._renderAccess(c)}
      ${this._renderSlots(c)}
    </section>`;
  }

  render() {
    if (this._framed) return nothing;
    const comps = this._data?.components;
    return html`<main>
      <header><h1>System components</h1><span class="grow"></span><a href="/">Back to the workspace</a></header>
      <p class="lead">Parts of xbin that run like tiles. Their code is the installed xbin release's — there is nothing to edit,
        and they update when xbin does. You decide who has access, whether each is on, its limits and what it connects to.</p>
      ${this._err ? html`<div class="banner" role="alert">${this._err}</div>` : nothing}
      ${this._denied ? html`<div class="card">Only workspace admins manage system components.</div>`
        : !comps ? html`<div class="card empty">Loading…</div>`
        : comps.length ? comps.map((c) => this._renderOne(c))
        : html`<div class="card empty">This xbin release has no system components.</div>`}
    </main>`;
  }
}
customElements.define('bx-system-page', BxSystemPage);
