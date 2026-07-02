/**
 * <bx-admin> — the workspace admin console (tiles/admin). Full owner-view
 * into the running system, powered by buxond's admin-capable endpoints via
 * the buxon:admin capability (see buxon.json / API.md).
 *
 * Tabs: overview · vault · roles & grants · cron. All reads go through
 * buxon.fetch (admin identity attributed by frame token) and refresh on the
 * grants/reload event stream.
 */
import { LitElement, html, css, nothing } from 'lit';

const api = async (path, opts) => {
  const r = await buxon.fetch('/api/buxon' + path, opts);
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error(data?.error ?? r.status);
  return data;
};

export class BxAdmin extends LitElement {
  static properties = {
    _tab: { state: true },
    _ov: { state: true },       // auth-overview
    _vaults: { state: true },   // [{component, keys}]
    _reveal: { state: true },   // "comp\0key" -> value (revealed secrets)
    _cron: { state: true },
    _users: { state: true },
    _err: { state: true },
    _denied: { state: true },
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
            color: var(--bx-text, #33414e); background: var(--bx-panel, #fff); }
    .tabs { display: flex; gap: 2px; padding: 6px 8px 0;
            border-bottom: 1px solid var(--bx-border, #e4e8ed);
            background: var(--bx-panel-2, #f7f8fa); position: sticky; top: 0; z-index: 1; }
    .tabs button { border: 1px solid transparent; border-bottom: none; background: none;
      font: inherit; font-size: 12px; padding: 4px 12px; cursor: pointer;
      color: var(--bx-muted, #8794a1); border-radius: 5px 5px 0 0; }
    .tabs button.on { background: var(--bx-panel, #fff); color: var(--bx-text, #33414e);
      border-color: var(--bx-border, #e4e8ed); margin-bottom: -1px; }
    .body { padding: 12px 14px; }
    .err { color: var(--bx-red, #e5484d); font-size: 12px; margin: 4px 0; }
    .denied { padding: 20px 14px; }
    .denied code { background: var(--bx-panel-2); border: 1px solid var(--bx-border);
      border-radius: 4px; padding: 0 4px; font: 11.5px var(--bx-mono); }

    h4 { margin: 14px 0 6px; font-size: 10.5px; font-weight: 600; letter-spacing: .08em;
         text-transform: uppercase; color: var(--bx-muted, #8794a1); }
    h4:first-child { margin-top: 0; }
    .cards { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 4px; }
    .stat { background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 6px; padding: 6px 12px; min-width: 74px; }
    .stat .n { font: 700 18px var(--bx-mono, monospace); color: var(--bx-accent, #1e88e5); }
    .stat .l { font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
               color: var(--bx-muted, #8794a1); }
    .stat.warn .n { color: var(--bx-amber, #f2a71b); }

    table { border-collapse: collapse; width: 100%; font-size: 12px; }
    th { text-align: left; font-size: 10px; text-transform: uppercase; letter-spacing: .06em;
         color: var(--bx-muted, #8794a1); font-weight: 600; padding: 3px 8px 3px 0; }
    td { padding: 3px 8px 3px 0; border-top: 1px solid var(--bx-border, #e4e8ed);
         vertical-align: top; }
    .mono { font-family: var(--bx-mono, monospace); }
    .pill { display: inline-block; font-size: 11px; padding: 0 6px; border-radius: 999px;
      background: var(--bx-panel-2, #f7f8fa); border: 1px solid var(--bx-border, #e4e8ed);
      margin: 1px 2px 1px 0; }
    .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; margin-right: 5px; }
    .st-healthy { color: var(--bx-green, #43a047); }
    .st-failed  { color: var(--bx-red, #e5484d); }
    .st-idle    { color: var(--bx-muted, #8794a1); }

    button.act { border: 1px solid var(--bx-border, #e4e8ed); background: var(--bx-panel, #fff);
      color: var(--bx-text, #33414e); border-radius: 5px; font: inherit; font-size: 11px;
      padding: 1px 8px; cursor: pointer; }
    button.act:hover { background: var(--bx-panel-2, #f7f8fa); }
    button.go { background: var(--bx-green, #43a047); color: #fff; border-color: transparent; }
    button.rm { color: var(--bx-red, #e5484d); border-color: color-mix(in srgb, var(--bx-red) 40%, transparent); }
    input, select { font: inherit; font-size: 12px; padding: 2px 6px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 5px;
      background: var(--bx-panel, #fff); color: var(--bx-text, #33414e); }
    .secret { font-family: var(--bx-mono, monospace); }
    .muted { color: var(--bx-muted, #8794a1); }
    form.inline { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
  `;

  // {id, label} — ids stay URL-safe (no spaces/&) for hash deep-links.
  static TABS = [
    { id: 'overview', label: 'overview' },
    { id: 'users', label: 'users' },
    { id: 'vault', label: 'vault' },
    { id: 'grants', label: 'roles & grants' },
    { id: 'cron', label: 'cron' },
  ];

  constructor() {
    super();
    const h = location.hash.replace(/^#/, '');
    this._tab = BxAdmin.TABS.some((t) => t.id === h) ? h : 'overview';
    this._reveal = {};
    this._err = '';
    this._denied = false;
  }

  _setTab(t) {
    this._tab = t;
    try { history.replaceState(null, '', '#' + t); } catch { /* sandboxed */ }
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = window.buxon?.events.on((e) => {
      if (e.type === 'grants' || e.type === 'reload' || e.type === 'build-ok') this._refresh();
    });
    this._refresh();
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _refresh() {
    try {
      const [ov, vaults, cron, users] = await Promise.all([
        api('/auth-overview'), api('/vaults'), api('/cron/jobs'),
        api('/users').catch(() => ({ users: [] })),
      ]);
      this._ov = ov; this._vaults = vaults; this._cron = cron.jobs ?? [];
      this._users = users.users ?? [];
      this._err = ''; this._denied = false;
    } catch (e) {
      if (String(e.message).includes('admin')) this._denied = true;
      else this._err = String(e.message ?? e);
    }
  }

  // ---- vault ----
  async _revealKey(comp, key) {
    try {
      const d = await api(`/vault/${comp}/${encodeURIComponent(key)}`);
      this._reveal = { ...this._reveal, [comp + '\0' + key]: d.value };
    } catch (e) { this._err = String(e.message); }
  }
  _hideKey(comp, key) {
    const r = { ...this._reveal }; delete r[comp + '\0' + key]; this._reveal = r;
  }
  async _setSecret(comp, key, value) {
    await api(`/vault/${comp}/${encodeURIComponent(key)}`,
      { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) });
    this._hideKey(comp, key); this._refresh();
  }
  async _delSecret(comp, key) {
    if (!confirm(`Delete secret ${comp} / ${key}?`)) return;
    await api(`/vault/${comp}/${encodeURIComponent(key)}`, { method: 'DELETE' });
    this._refresh();
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

  render() {
    if (this._denied) return html`<div class="denied">
      <b>No admin access.</b> This tile needs the <code>buxon:admin</code> grant.
      Approve <span class="mono">tiles/admin → buxon : admin</span> in the grants
      panel, or run <code>bx grant tiles/admin buxon:admin</code>.
      See <a href="/docs/auth.md" target="_blank">docs/auth.md</a>.</div>`;
    const tab = this._tab;
    return html`
      <div class="tabs">
        ${BxAdmin.TABS.map((t) => html`
          <button class=${t.id === tab ? 'on' : ''} @click=${() => this._setTab(t.id)}>${t.label}</button>`)}
      </div>
      <div class="body">
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${tab === 'users' ? this._usersView()
          : tab === 'overview' ? this._overview()
          : tab === 'vault' ? this._vaultView()
          : tab === 'grants' ? this._rolesView()
          : this._cronView()}
      </div>`;
  }

  _overview() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    const c = ov.counts;
    return html`
      <div class="cards">
        <div class="stat"><div class="n">${c.components}</div><div class="l">components</div></div>
        <div class="stat"><div class="n">${c.exposed}</div><div class="l">expose APIs</div></div>
        <div class="stat"><div class="n">${c.grants}</div><div class="l">grants</div></div>
        <div class="stat ${c.pending ? 'warn' : ''}"><div class="n">${c.pending}</div><div class="l">pending</div></div>
      </div>
      <h4>principals</h4>
      <table>
        <tr><th>component</th><th>runtime</th><th>exposes</th><th>uses</th><th>vault</th></tr>
        ${ov.components.map((k) => html`<tr>
          <td class="mono">${k.path}${k.manifestError ? html` <span class="st-failed" title=${k.manifestError}>⚠</span>` : nothing}</td>
          <td class="muted">${k.runtime || 'static'}</td>
          <td>${k.roles ? Object.keys(k.roles).map((r) => html`<span class="pill">${r}</span>`) : html`<span class="muted">—</span>`}</td>
          <td>${(k.uses ?? []).map((u) => html`<span class="pill">${u.target}:${u.role}</span>`)}</td>
          <td>${k.hasVault ? '🔑' : ''}</td>
        </tr>`)}
      </table>`;
  }

  _vaultView() {
    const vs = this._vaults ?? [];
    return html`
      ${vs.length === 0 ? html`<span class="muted">no vaults hold secrets yet — set one with
        <span class="mono">bx vault set &lt;component&gt; &lt;key&gt;</span> or below.</span>` : nothing}
      ${vs.map((v) => html`
        <h4>${v.component}</h4>
        <table>
          ${v.keys.map((k) => {
            const shown = this._reveal[v.component + '\0' + k];
            return html`<tr>
              <td class="mono" style="width:30%">${k}</td>
              <td class="secret">${shown !== undefined ? shown : '••••••••'}</td>
              <td style="text-align:right; white-space:nowrap">
                ${shown !== undefined
                  ? html`<button class="act" @click=${() => navigator.clipboard?.writeText(shown)}>copy</button>
                         <button class="act" @click=${() => this._hideKey(v.component, k)}>hide</button>
                         <button class="act" @click=${() => { const nv = prompt(`New value for ${k}`, shown); if (nv != null) this._setSecret(v.component, k, nv); }}>edit</button>`
                  : html`<button class="act" @click=${() => this._revealKey(v.component, k)}>reveal</button>`}
                <button class="act rm" @click=${() => this._delSecret(v.component, k)}>del</button>
              </td></tr>`;
          })}
        </table>`)}
      <form class="inline" @submit=${(e) => { e.preventDefault();
          const f = e.target;
          if (f.comp.value && f.key.value) this._setSecret(f.comp.value.trim(), f.key.value.trim(), f.val.value);
          f.reset(); }}>
        <input name="comp" placeholder="component" size="16" list="admin-comps">
        <input name="key" placeholder="key" size="12">
        <input name="val" placeholder="value" size="18" type="password">
        <button class="act go">set secret</button>
      </form>
      <datalist id="admin-comps">
        ${(this._ov?.components ?? []).map((k) => html`<option value=${k.path}></option>`)}
      </datalist>`;
  }

  _rolesView() {
    const ov = this._ov; if (!ov) return html`<span class="muted">loading…</span>`;
    // Empty slices arrive as null over JSON — coalesce before touching them.
    const comps = ov.components ?? [];
    const pending = ov.pending ?? [];
    const grants = ov.grants ?? [];
    return html`
      ${pending.length ? html`<h4>pending requests</h4>
        <table>${pending.map((g) => html`<tr>
          <td class="mono">${g.from} → ${g.target}</td>
          <td><span class="pill">${g.role}</span></td>
          <td style="text-align:right"><button class="act go" @click=${() => this._grant(g.from, g.target, g.role)}>approve</button></td>
        </tr>`)}</table>` : nothing}

      <h4>active grants</h4>
      <table>${grants.length ? grants.map((g) => html`<tr>
        <td class="mono">${g.from} → ${g.target}</td>
        <td><span class="pill">${g.role}</span></td>
        <td style="text-align:right"><button class="act rm" @click=${() => this._revoke(g)}>revoke</button></td>
      </tr>`) : html`<tr><td class="muted">none</td></tr>`}</table>

      <h4>add grant</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); const f = e.target;
          if (f.from.value && f.target.value && f.role.value) this._grant(f.from.value, f.target.value.trim(), f.role.value.trim());
          f.reset(); }}>
        <select name="from">${comps.map((k) => html`<option>${k.path}</option>`)}</select>
        <span class="muted">→</span>
        <input name="target" placeholder="apps/other or res:…/… or buxon" size="20">
        <span class="muted">:</span>
        <input name="role" placeholder="reader" size="8" value="reader">
        <button class="act go">grant</button>
      </form>

      <h4>exposed roles</h4>
      <table>
        <tr><th>component</th><th>role</th><th>description</th></tr>
        ${comps.filter((k) => k.roles).flatMap((k) =>
          Object.entries(k.roles).map(([role, desc]) => html`<tr>
            <td class="mono">${k.path}</td><td><span class="pill">${role}</span></td>
            <td class="muted">${desc}</td></tr>`))}
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
  async _createUser(f) {
    const tiles = f.tiles.value.split(',').map((s) => s.trim()).filter(Boolean);
    await api('/users', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: f.id.value.trim(), name: f.name.value.trim(), role: f.role.value,
        tiles, terminal: f.terminal.checked, password: f.password.value }) });
    f.reset(); this._refresh();
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._refresh();
  }
  async _resetPw(id) {
    const pw = prompt(`New password for ${id}:`);
    if (!pw) return;
    await this._patchUser(id, { password: pw });
    this._err = `password reset for ${id}`;
  }
  async _editTiles(u) {
    const v = prompt(`Allowed tiles for ${u.id} (comma-separated paths or prefix/*; * = all):`,
      (u.tiles || []).join(', '));
    if (v == null) return;
    this._patchUser(u.id, { tiles: v.split(',').map((s) => s.trim()).filter(Boolean) });
  }
  async _delUser(id) {
    if (!confirm(`Delete user ${id}? Their sessions are revoked immediately.`)) return;
    await api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
    this._refresh();
  }

  _usersView() {
    const users = this._users ?? [];
    return html`
      <h4>users</h4>
      <table>
        <tr><th>id</th><th>name</th><th>role</th><th>tiles</th><th>terminal</th><th></th></tr>
        ${users.length ? users.map((u) => html`<tr>
          <td class="mono">${u.id}</td>
          <td>${u.name}</td>
          <td><span class="pill">${u.role}</span></td>
          <td>${u.role === 'admin' ? html`<span class="muted">all</span>`
            : (u.tiles || []).length ? (u.tiles || []).map((t) => html`<span class="pill">${t}</span>`)
            : html`<span class="muted">none</span>`}</td>
          <td>${u.terminal || u.role === 'admin' ? '✓' : ''}</td>
          <td style="text-align:right; white-space:nowrap">
            <button class="act" @click=${() => this._patchUser(u.id, { role: u.role === 'admin' ? 'user' : 'admin' })}>${u.role === 'admin' ? 'demote' : 'make admin'}</button>
            ${u.role === 'admin' ? nothing : html`<button class="act" @click=${() => this._editTiles(u)}>tiles</button>
              <button class="act" @click=${() => this._patchUser(u.id, { terminal: !u.terminal })}>${u.terminal ? '− term' : '+ term'}</button>`}
            <button class="act" @click=${() => this._resetPw(u.id)}>pw</button>
            <button class="act rm" @click=${() => this._delUser(u.id)}>del</button>
          </td>
        </tr>`) : html`<tr><td class="muted" colspan="6">no users — the root token is the only admin. Add one below.</td></tr>`}
      </table>

      <h4>add user</h4>
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createUser(e.target); }}>
        <input name="id" placeholder="username" size="12" required>
        <input name="name" placeholder="display name" size="14">
        <select name="role"><option value="user">user</option><option value="admin">admin</option></select>
        <input name="tiles" placeholder="apps/chat, lib/*  (blank = none)" size="22">
        <label class="muted" style="font-size:11px"><input type="checkbox" name="terminal"> terminal</label>
        <input name="password" type="password" placeholder="password" size="12" required>
        <button class="act go">create</button>
      </form>
      <p class="muted" style="font-size:11px;margin-top:6px">
        Terminal = a <b>root shell</b> in a tile's directory; grant it only to trusted users.</p>`;
  }
}

customElements.define('bx-admin', BxAdmin);
