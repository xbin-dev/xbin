/**
 * <bx-organisations> — the delegated management surface (plans/ownership.md,
 * D24–D28). Raw fetch = the signed-in USER's cookie principal, so the server's
 * ownership/org-admin gates decide per person; sections render by capability
 * and 403s degrade to friendly notes.
 *
 *   everyone      my orgs (role), my tiles (owned, D24) with sharing (ACL),
 *                 my tiles' pending requests with who-can-approve (D33),
 *                 where to create org tiles (the Tile Manager's Owner picker)
 *   org admins    members (role knobs), org tiles (ACL + transfer), the
 *                 pending grants/bindings they may approve — one-click
 *                 approve (D26/D33), consumers of their tiles (revocable),
 *                 with the resolved allowance shown read-only
 *
 * Live: subscribes to the events hub (users + grants) and re-loads, so an
 * open tile shows new requests/membership changes without a reload.
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
    _grants: { state: true },   // scoped {grants, pending, scope} (D26/D33)
    _binds: { state: true },    // scoped {bindings, pending} (D26/D33)
    _acl: { state: true },      // per-tile expanded ACL {tile, owner, entries}
    _xfer: { state: true },     // transfer-in-progress {tile, to} (inline confirm)
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
    // Live refresh: org mutations publish `users`, approvals AND newly
    // surfaced requests publish `grants` — debounce bursts into one reload.
    this._offEvents = window.xbin?.events.on((e) => {
      if (e.type !== 'users' && e.type !== 'grants') return;
      clearTimeout(this._reloadT);
      this._reloadT = setTimeout(() => this._load(), 300);
    });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._offEvents?.();
    clearTimeout(this._reloadT);
  }

  async _load() {
    try {
      this._who = await api('/whoami');
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); return; }
    // Admin-plane extras degrade independently (403 → absent section);
    // grants/bindings return a scoped view for EVERY signed-in user now
    // (approver rows for org admins, "mine" rows for requesters, D33).
    this._orgs = (await api('/orgs').catch(() => ({ orgs: [] }))).orgs ?? [];
    this._grants = await api('/grants').catch(() => null);
    this._binds = await api('/bindings').catch(() => null);
    if (this._adminOrgIds().length || this._who?.admin) {
      this._dir = (await api('/users-directory').catch(() => ({ users: [] }))).users ?? [];
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
              ${(e.kind === 'user' ? ['read', 'write', 'terminal', 'none'] : ['read', 'write', 'terminal'])
                .map((l) => html`<option value=${l} ?selected=${e.level === l}>${l === 'none' ? 'none (exclude)' : l}</option>`)}
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
        <select id="acl-level">${['read', 'write', 'terminal', 'none'].map((l) => html`<option value=${l}>${l === 'none' ? 'none (exclude)' : l}</option>`)}</select>
        <button class="go" @click=${() => {
          const g = (id) => this.renderRoot.getElementById(id).value;
          if (!g('acl-id')) return;
          this._do(() => api('/access', jbody('PUT', {
            tile: a.tile, kind: g('acl-kind'), id: g('acl-id'), level: g('acl-level') })), 'shared');
        }}>share</button>
        <span class="muted" style="font-size:11px">read = see it · write = use/edit · terminal = shell on it ·
          an exact user entry is authoritative (none = exclude, D31)</span>
      </div>
    </div>`;
  }

  // Transfer runs through an inline confirm card (not a raw prompt): the
  // consequence — org admins gain full control — deserves a beat of thought.
  _transfer(tile) { this._xfer = { tile, to: '' }; }

  _transferCard() {
    const x = this._xfer;
    if (!x) return nothing;
    const myOrgs = (this._who?.orgs ?? []).map((o) => o.id);
    return html`<div class="card" style="border-color: var(--bx-accent, #f5a623)">
      <div class="row"><b>transfer</b> <span class="mono">${x.tile}</span></div>
      <div class="row" style="margin:6px 0">
        <input size="18" placeholder="user:&lt;id&gt;, org:&lt;id&gt;, workspace" .value=${x.to}
          list="xfer-targets" @input=${(e) => { this._xfer = { ...x, to: e.target.value }; }}>
        <datalist id="xfer-targets">
          ${myOrgs.map((o) => html`<option value=${'org:' + o}></option>`)}
          <option value="workspace"></option>
        </datalist>
        <button class="go" ?disabled=${!x.to.trim()} @click=${() => {
          const to = x.to.trim() === 'workspace' ? '' : x.to.trim();
          this._xfer = null;
          this._do(() => api('/owner', jbody('POST', { tile: x.tile, to })), 'transferred');
        }}>transfer</button>
        <button @click=${() => { this._xfer = null; }}>cancel</button>
      </div>
      <p class="muted" style="font-size:11px; margin:2px 0 0">
        Transferring to an org gives <b>every admin of that org</b> full control
        of the tile — terminal, lifecycle, sharing. Secrets stay backend-only:
        vault values are never readable from terminals (D30).
      </p>
    </div>`;
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

  // askWho: "org:data admins · workspace admin" from the server's hints (D33).
  _askWho(p) {
    return (p.approvers ?? []).map((a) =>
      a === 'workspace-admin' ? 'workspace admin' : `${a} admins`).join(' · ') || 'a workspace admin';
  }

  // ---- pending approvals (D26/D33) + my waiting requests ----
  _approvalsView() {
    const all = this._grants?.pending ?? [];
    const pending = all.filter((p) => p.approvable);
    const mine = all.filter((p) => !p.approvable && p.direction === 'mine');
    const held = all.filter((p) => !p.approvable && p.direction !== 'mine');
    const bindPending = this._binds?.pending ?? [];
    if (!pending.length && !mine.length && !held.length && !bindPending.length) return nothing;
    const dir = (p) => p.direction ? html`<span class="pill" title="your relation: consumer = your org's tile asking · provider = targeting your org's property">${p.direction}</span>` : nothing;
    return html`
      <h3>pending approvals</h3>
      ${pending.length ? html`<div class="card">
        ${pending.map((p) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${p.from}</span> → <span class="mono">${p.target}</span>
          <span class="pill">${p.role}</span> ${dir(p)}
          <span style="flex:1"></span>
          <button class="go" @click=${() => this._do(() =>
            api('/grants', jbody('POST', { from: p.from, target: p.target, role: p.role })), 'approved')}>approve</button>
        </div>`)}
      </div>` : nothing}
      ${mine.length ? html`<div class="card">
        ${mine.map((p) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${p.from}</span> → <span class="mono">${p.target}</span>
          <span class="pill">${p.role}</span>
          <span class="muted" style="font-size:11px">waiting for: ${this._askWho(p)}</span>
        </div>`)}
        <p class="muted" style="font-size:11px; margin:2px 0 0">Your tiles' requests — ask the named admins to approve.</p>
      </div>` : nothing}
      ${held.length ? html`<p class="muted" style="font-size:11px">
        ${held.length} more pending request(s) can't be approved here —
        ${[...new Set(held.map((p) => this._askWho(p)))].join('; ')}.</p>` : nothing}
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

  // ---- provider view (D33): who consumes YOUR tiles ----
  _consumersView() {
    const rows = (this._grants?.grants ?? []).filter((g) =>
      g.direction === 'provider' || g.direction === 'both');
    // Binding rows wired INTO my orgs' provider tiles: the server includes
    // them in the scoped view; pick the ones whose refs point at my tiles.
    const myTiles = new Set([
      ...(this._who?.owned ?? []),
      ...(this._orgs ?? []).flatMap((o) => o.ownedTiles ?? []),
    ]);
    const refName = (v) => (typeof v === 'string' ? v : v?.ref ?? '').split('#')[0];
    const boundIn = [];
    for (const [comp, slots] of Object.entries(this._binds?.bindings ?? {})) {
      if (myTiles.has(comp)) continue;
      for (const [slot, b] of Object.entries(slots ?? {})) {
        for (const ref of [].concat(b ?? [])) {
          const prov = refName(ref);
          if (myTiles.has(prov)) boundIn.push({ comp, slot, prov });
        }
      }
    }
    if (!rows.length && !boundIn.length) return nothing;
    return html`
      <h3>consumers of your tiles</h3>
      <p class="muted" style="font-size:11px">Other tiles granted access to your
        org's tiles — you can approve requests targeting your tiles and
        withdraw access (D33).</p>
      ${rows.length ? html`<div class="card">
        ${rows.map((g) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${g.from}</span> → <span class="mono">${g.target}</span>
          <span class="pill">${g.role}</span>
          ${g.approvedBy ? html`<span class="muted" style="font-size:11px">approved by ${g.approvedBy}</span>` : nothing}
          <span style="flex:1"></span>
          <button class="rm" @click=${() => this._do(() =>
            api('/grants', jbody('DELETE', { from: g.from, target: g.target, role: g.role })), 'revoked')}>revoke</button>
        </div>`)}
      </div>` : nothing}
      ${boundIn.length ? html`<div class="card">
        ${boundIn.map((b) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${b.comp}</span> · <span class="pill">${b.slot}</span>
          <span class="muted" style="font-size:11px">bound to your <span class="mono">${b.prov}</span></span>
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
        : who.admin
          ? html`<p class="muted">No organisations exist yet — create them in the
              admin console (user management → organisations).</p>`
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
      ${this._consumersView()}
      ${this._transferCard()}

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

      ${!myOrgs.length && !owned.length && !adminOrgs.length && !who.admin ? html`
        <p class="muted" style="margin-top:10px">Nothing to manage yet. When you own tiles or join an
          organisation, this is where you'll share tiles, manage members, and approve requests.</p>` : nothing}
    `;
  }
}
customElements.define('bx-organisations', BxOrganisations);
