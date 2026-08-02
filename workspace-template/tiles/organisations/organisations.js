/**
 * <bx-organisations> — the delegated management surface (plans/ownership.md,
 * D24–D28). Raw fetch = the signed-in USER's cookie principal, so the server's
 * ownership/org-admin gates decide per person; sections render by capability
 * and 403s degrade to friendly notes.
 *
 *   everyone      my orgs (role), my tiles (owned, D24) with sharing (ACL),
 *                 where to create org tiles (the Tile Manager's Owner picker)
 *   org admins    members (role knobs), org tiles (ACL + transfer), and the
 *                 pending grants/bindings their allowance covers — one-click
 *                 approve (D26), with the resolved allowance shown read-only
 */
import { LitElement, html, css, nothing } from 'lit';

const api = async (path, opts) => {
  const r = await fetch('/api/xbin' + path, opts);
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error ?? `error ${r.status}`);
  return d;
};
const jbody = (method, body) => ({
  method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
});

export class BxOrganisations extends LitElement {
  static properties = {
    _who: { state: true },      // whoami (orgs, owned)
    _orgs: { state: true },     // manageable orgs (org admins; [] for members)
    _dir: { state: true },      // users-directory (org admins)
    _grants: { state: true },   // org-scoped {grants, pending} (org admins)
    _binds: { state: true },    // org-scoped {pending} bindings (org admins)
    _acl: { state: true },      // per-tile expanded ACL {tile, owner, entries}
    _err: { state: true },
    _note: { state: true },
  };

  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/1.5 system-ui, sans-serif);
      color: var(--bx-text, #33414e); padding: 12px 16px 24px; }
    h3 { font-size: 14px; margin: 14px 0 6px; }
    h3:first-of-type { margin-top: 2px; }
    .muted { color: var(--bx-muted, #8794a1); }
    .mono { font-family: var(--bx-mono, ui-monospace, monospace); font-size: 12px; }
    .pill { display: inline-block; border: 1px solid var(--bx-border, #e4e8ed);
      border-radius: 999px; padding: 0 8px; font-size: 11px; margin: 1px 2px; }
    .pill.on { border-color: var(--bx-accent, #f5a623);
      background: color-mix(in srgb, var(--bx-accent, #f5a623) 12%, transparent); }
    .card { border: 1px solid var(--bx-border, #e4e8ed); border-radius: 6px;
      padding: 8px 10px; margin: 6px 0; background: var(--bx-panel, #fff); }
    table { border-collapse: collapse; width: 100%; }
    th { text-align: left; font-size: 10.5px; text-transform: uppercase;
      letter-spacing: .05em; color: var(--bx-muted, #8794a1); padding: 2px 6px; }
    td { padding: 3px 6px; border-top: 1px solid color-mix(in srgb, var(--bx-border, #e4e8ed) 55%, transparent); }
    button { font: inherit; font-size: 12px; border: 1px solid var(--bx-border, #e4e8ed);
      background: var(--bx-panel, #fff); border-radius: 5px; padding: 2px 9px; cursor: pointer; }
    button:hover { border-color: var(--bx-muted, #8794a1); }
    button.go { background: var(--bx-green, #43a047); border-color: var(--bx-green, #43a047); color: #fff; }
    button.rm { color: var(--bx-red, #e5484d); }
    select, input { font: inherit; font-size: 12px; padding: 2px 6px;
      border: 1px solid var(--bx-border, #e4e8ed); border-radius: 5px;
      background: var(--bx-panel, #fff); color: inherit; }
    .err { color: var(--bx-red, #e5484d); font-size: 12px; margin: 6px 0; }
    .note { color: var(--bx-green, #2f9e44); font-size: 12px; margin: 6px 0; }
    .row { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  `;

  connectedCallback() {
    super.connectedCallback();
    this._load();
  }

  async _load() {
    try {
      this._who = await api('/whoami');
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); return; }
    // Admin-plane extras: each degrades independently (403 → absent section).
    this._orgs = (await api('/orgs').catch(() => ({ orgs: [] }))).orgs ?? [];
    if (this._adminOrgIds().length || this._who?.admin) {
      this._dir = (await api('/users-directory').catch(() => ({ users: [] }))).users ?? [];
      this._grants = await api('/grants').catch(() => null);
      this._binds = await api('/bindings').catch(() => null);
    }
  }

  _adminOrgIds() {
    return (this._who?.orgs ?? []).filter((o) => o.admin).map((o) => o.id);
  }

  async _do(fn, okNote) {
    try {
      await fn();
      this._err = ''; this._note = okNote ?? '';
      setTimeout(() => { this._note = ''; }, 2500);
    } catch (e) { this._err = String(e.message ?? e); this._note = ''; }
    await this._load();
    if (this._acl) this._openAcl(this._acl.tile); // keep the open editor fresh
  }

  // ---- per-tile ACL editor (owner right, D24) ----
  async _openAcl(tile) {
    try { this._acl = await api('/access?tile=' + encodeURIComponent(tile)); this._err = ''; }
    catch (e) { this._err = String(e.message ?? e); }
  }

  _aclEditor() {
    const a = this._acl;
    if (!a) return nothing;
    const myOrgs = (this._who?.orgs ?? []).map((o) => o.id);
    return html`<div class="card">
      <div class="row"><b class="mono">${a.tile}</b>
        <span class="pill">owner: ${a.owner || 'workspace'}</span>
        <span style="flex:1"></span>
        <button title="transfer ownership" @click=${() => this._transfer(a.tile)}>transfer…</button>
        <button @click=${() => { this._acl = null; }}>close</button></div>
      <table>
        ${(a.entries ?? []).length ? html`<tr><th>kind</th><th>who</th><th>level</th><th>source</th><th></th></tr>` : nothing}
        ${(a.entries ?? []).map((e) => html`<tr>
          <td>${e.kind}</td><td class="mono">${e.id}</td>
          <td>${e.source === 'exact' ? html`<select @change=${(ev) =>
              this._do(() => api('/access', jbody('PUT', { tile: a.tile, kind: e.kind, id: e.id, level: ev.target.value })), 'updated')}>
              ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${e.level === l}>${l}</option>`)}
            </select>` : e.level}</td>
          <td class="muted">${e.source}</td>
          <td>${e.source === 'exact' ? html`<button class="rm" @click=${() =>
              this._do(() => api('/access', jbody('PUT', { tile: a.tile, kind: e.kind, id: e.id, level: '' })), 'removed')}>✕</button>` : nothing}</td>
        </tr>`)}
      </table>
      <div class="row" style="margin-top:6px">
        <select id="acl-kind">
          <option value="user">user</option>
          <option value="org">org</option>
        </select>
        <input id="acl-id" size="14" placeholder="who" list="acl-people">
        <datalist id="acl-people">
          ${(this._dir ?? []).map((u) => html`<option value=${u.id}></option>`)}
          ${myOrgs.map((o) => html`<option value=${o}></option>`)}
        </datalist>
        <select id="acl-level">${['read', 'write', 'terminal'].map((l) => html`<option>${l}</option>`)}</select>
        <button class="go" @click=${() => {
          const g = (id) => this.renderRoot.getElementById(id).value;
          if (!g('acl-id')) return;
          this._do(() => api('/access', jbody('PUT', {
            tile: a.tile, kind: g('acl-kind'), id: g('acl-id'), level: g('acl-level') })), 'shared');
        }}>share</button>
        <span class="muted" style="font-size:11px">read = see it · write = use/edit · terminal = shell on it</span>
      </div>
    </div>`;
  }

  async _transfer(tile) {
    const to = prompt(`Transfer ${tile} to (user:<id>, org:<id>, or "workspace"):`);
    if (to == null) return;
    await this._do(() => api('/owner', jbody('POST', { tile, to: to.trim() === 'workspace' ? '' : to.trim() })), 'transferred');
  }

  // ---- org admin: member management ----
  _memberEditor(o) {
    const save = (members, note) => this._do(() =>
      api('/orgs/' + encodeURIComponent(o.id), jbody('PATCH', { members })), note);
    const memberIds = new Set((o.members ?? []).map((m) => m.id));
    const addable = (this._dir ?? []).filter((u) => !memberIds.has(u.id));
    return html`
      <table>
        <tr><th>member</th><th>level</th><th>create</th><th>admin</th><th></th></tr>
        ${(o.members ?? []).map((m) => html`<tr>
          <td class="mono">${m.id}</td>
          <td><select @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, level: e.target.value } : x), 'updated')}>
            ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${m.level === l}>${l}</option>`)}
          </select></td>
          <td><input type="checkbox" .checked=${!!m.create} title="may create org-owned tiles"
            @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, create: e.target.checked } : x), 'updated')}></td>
          <td><input type="checkbox" .checked=${!!m.admin} title="org management + allowance approvals"
            @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, admin: e.target.checked } : x), 'updated')}></td>
          <td><button class="rm" @click=${() => save((o.members ?? []).filter((x) => x.id !== m.id), 'removed')}>remove</button></td>
        </tr>`)}
      </table>
      <div class="row" style="margin-top:5px">
        <select id="add-${o.id}">
          <option value="">add member…</option>
          ${addable.map((u) => html`<option value=${u.id}>${u.id}${u.name && u.name !== u.id ? ` — ${u.name}` : ''}</option>`)}
        </select>
        <button class="go" @click=${(e) => {
          const sel = e.target.previousElementSibling;
          if (!sel.value) return;
          save([...(o.members ?? []), { id: sel.value, level: 'terminal', create: true }], 'added');
        }}>add as developer</button>
      </div>`;
  }

  // ---- org admin: pending approvals (D26) ----
  _approvalsView() {
    const pending = (this._grants?.pending ?? []).filter((p) => p.approvable);
    const held = (this._grants?.pending ?? []).filter((p) => !p.approvable);
    const bindPending = this._binds?.pending ?? [];
    if (!pending.length && !held.length && !bindPending.length) return nothing;
    return html`
      <h3>pending approvals</h3>
      ${pending.length ? html`<div class="card">
        ${pending.map((p) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${p.from}</span> → <span class="mono">${p.target}</span>
          <span class="pill">${p.role}</span>
          <span style="flex:1"></span>
          <button class="go" @click=${() => this._do(() =>
            api('/grants', jbody('POST', { from: p.from, target: p.target, role: p.role })), 'approved')}>approve</button>
        </div>`)}
      </div>` : nothing}
      ${held.length ? html`<p class="muted" style="font-size:11px">
        ${held.length} more pending request(s) need a workspace admin — outside this org's allowance.</p>` : nothing}
      ${bindPending.length ? html`<div class="card">
        ${bindPending.map((p) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${p.component}</span> · <span class="pill">${p.slot}</span>
          <span class="muted">${p.service ? `${p.kind}:${p.service}` : p.kind}</span>
          ${(p.options ?? []).length ? html`
            <select id="bp-${p.component}-${p.slot}">
              ${p.options.map((op) => html`<option value=${op.id}>${op.label}</option>`)}
            </select>
            <button class="go" @click=${(e) => {
              const sel = this.renderRoot.getElementById(`bp-${p.component}-${p.slot}`);
              this._do(() => api('/bindings', jbody('POST',
                { component: p.component, slot: p.slot, provider: sel.value })), 'bound');
            }}>bind</button>` : html`<span class="muted">no provider available</span>`}
        </div>`)}
      </div>` : nothing}`;
  }

  render() {
    const who = this._who;
    if (!who) return html`${this._err ? html`<div class="err">${this._err}</div>` : html`<p class="muted">loading…</p>`}`;
    if (who.kind !== 'user' && !who.admin) {
      return html`<p class="muted">Sign in as a workspace user to see your organisations.</p>`;
    }
    const myOrgs = who.orgs ?? [];
    const owned = who.owned ?? [];
    const adminOrgs = (this._orgs ?? []).filter((o) =>
      who.admin || myOrgs.some((m) => m.id === o.id && m.admin));
    return html`
      ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
      ${this._note ? html`<div class="note">${this._note}</div>` : nothing}

      <h3>my organisations</h3>
      ${myOrgs.length ? myOrgs.map((o) => html`
        <span class="pill ${o.admin ? 'on' : ''}" title="level ${o.level}${o.create ? ' · may create org tiles' : ''}${o.admin ? ' · org admin' : ''}">
          ${o.admin ? '★ ' : ''}${o.id} · ${o.level}${o.create ? ' +create' : ''}</span>`)
        : html`<p class="muted">You're in no organisation yet — an org admin or workspace admin adds you.</p>`}
      ${myOrgs.some((o) => o.create || o.admin) ? html`<p class="muted" style="font-size:11px">
        Create org-owned tiles from the <b>Tile Manager</b> — pick the org in its <i>Owner</i> field.</p>` : nothing}

      ${owned.length ? html`
        <h3>my tiles</h3>
        <p class="muted" style="font-size:11px">Tiles you own (D24) — sharing them is your call.</p>
        ${owned.map((t) => html`<div class="row" style="margin:2px 0">
          <span class="mono">${t}</span>
          <button @click=${() => this._openAcl(t)}>sharing…</button>
          <button @click=${() => this._transfer(t)}>transfer…</button>
        </div>`)}` : nothing}

      ${this._approvalsView()}

      ${adminOrgs.map((o) => html`
        <h3>org ${o.id} <span class="muted" style="font-weight:400">${o.name !== o.id ? o.name : ''}</span></h3>
        ${(o.resolvedAllow ?? []).length ? html`<p class="muted" style="font-size:11px">
          may self-approve: ${o.resolvedAllow.map((a) => html`<span class="pill mono">${a}</span>`)}
          <span class="muted">(set by a workspace admin)</span></p>`
          : html`<p class="muted" style="font-size:11px">no allowances — grants/bindings for this
            org's tiles go through a workspace admin.</p>`}
        <div class="card">${this._memberEditor(o)}</div>
        ${(o.ownedTiles ?? []).length ? html`<div class="card">
          <b style="font-size:12px">org tiles</b>
          ${o.ownedTiles.map((t) => html`<div class="row" style="margin:2px 0">
            <span class="mono">${t}</span>
            <button @click=${() => this._openAcl(t)}>sharing…</button>
            <button @click=${() => this._transfer(t)}>transfer…</button>
          </div>`)}
        </div>` : nothing}`)}

      ${this._aclEditor()}

      ${!myOrgs.length && !owned.length && !adminOrgs.length ? html`
        <p class="muted" style="margin-top:10px">Nothing to manage yet. When you own tiles or join an
          organisation, this is where you'll share tiles, manage members, and approve requests.</p>` : nothing}
    `;
  }
}
customElements.define('bx-organisations', BxOrganisations);
