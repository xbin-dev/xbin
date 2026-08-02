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
    _reqs: { state: true },     // human access requests, scoped mine/manage (D36)
    _acl: { state: true },      // per-tile expanded ACL {tile, owner, entries}
    _xfer: { state: true },     // transfer-in-progress {tile, to} (inline confirm)
    _policies: { state: true }, // org → ceiling rows (read-only; ws-admin-set)
    _overrides: { state: true },// exact per-user entries on org tiles (clamp/exclude view)
    _screens: { state: true },  // org screens (D37): [{id,org,name,edit,canEdit}]
    _invite: { state: true },   // reset-by-link result {id, link} (copy field)
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
    this._reqs = (await api('/access-requests').catch(() => ({ requests: [] }))).requests ?? [];
    this._screens = (await api('/screens').catch(() => ({ org: [] }))).org ?? [];
    if (this._adminOrgIds().length || this._who?.admin) {
      this._dir = (await api('/users-directory').catch(() => ({ users: [] }))).users ?? [];
      await this._loadAdminDepth();
    }
  }

  // Org-admin depth (D31/D33 legibility): the ceilings this org's approvals
  // can trip on, and the exact per-user entries sitting on its tiles — the
  // clamp/exclude picture that otherwise only doctor sees.
  async _loadAdminDepth() {
    const rank = { read: 1, write: 2, terminal: 3 };
    const pol = {}; const ov = [];
    for (const o of this._orgs ?? []) {
      pol[o.id] = (await api('/orgs/' + encodeURIComponent(o.id) + '/policy').catch(() => ({ policy: [] }))).policy ?? [];
      const lvl = {};
      for (const m of o.members ?? []) if (!m.suspended) lvl[m.id] = m.admin ? 'terminal' : m.level;
      for (const t of o.ownedTiles ?? []) {
        const a = await api('/access?tile=' + encodeURIComponent(t)).catch(() => null);
        for (const e of a?.entries ?? []) {
          if (e.kind !== 'user' || e.source !== 'exact') continue;
          ov.push({ org: o.id, tile: t, user: e.id, level: e.level,
            excluded: e.level === 'none',
            clamps: e.level !== 'none' && (rank[lvl[e.id]] ?? 0) > (rank[e.level] ?? 0) });
        }
      }
    }
    this._policies = pol; this._overrides = ov;
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

  // Transfer runs preview-first (D39, plans/transfer.md): pick a target →
  // the impact report (your access after, bindings that will be UNBOUND,
  // grants going inert) renders in the confirm → transfer. The static
  // consequence line stays as the fallback when preview isn't available.
  _transfer(tile) { this._xfer = { tile, to: '', rep: null, perr: null }; }

  _xferReport(rep) {
    if (!rep) return nothing;
    const lv = rep.callerLevel;
    return html`<div style="margin-top:5px; font-size:11.5px">
      ${lv && lv.before !== lv.after ? html`<div>your access will drop: <b>${lv.before || 'none'}</b> → <b>${lv.after || 'none'}</b></div>` : nothing}
      ${(rep.deadBindings ?? []).map((b) => html`<div style="color:var(--bx-red,#e5484d)">
        binding <span class="mono">${b.slot}</span> will be <b>UNBOUND</b>: ${b.reason}</div>`)}
      ${(rep.deadGrants ?? []).map((g) => html`<div style="color:var(--bx-red,#e5484d)">
        grant <span class="mono">${g.target}:${g.role}</span> becomes inert: ${g.reason}</div>`)}
      ${(rep.planeChanges ?? []).map((s) => html`<div class="muted">${s}</div>`)}
    </div>`;
  }

  _transferCard() {
    const x = this._xfer;
    if (!x) return nothing;
    // Suggest only orgs the D39 receive rule accepts: Create or admin.
    const myOrgs = (this._who?.orgs ?? []).filter((o) => o.create || o.admin).map((o) => o.id);
    const to = x.to.trim() === 'workspace' ? '' : x.to.trim();
    return html`<div class="card" style="border-color: var(--bx-accent, #f5a623)">
      <div class="row"><b>transfer</b> <span class="mono">${x.tile}</span></div>
      <div class="row" style="margin:6px 0">
        <input size="18" placeholder="user:&lt;id&gt;, org:&lt;id&gt;, workspace" .value=${x.to}
          list="xfer-targets" @input=${(e) => { this._xfer = { ...x, to: e.target.value, rep: null, perr: null }; }}>
        <datalist id="xfer-targets">
          ${myOrgs.map((o) => html`<option value=${'org:' + o}></option>`)}
          <option value="workspace"></option>
        </datalist>
        ${!x.rep ? html`<button class="go" ?disabled=${!x.to.trim()} @click=${async () => {
          try {
            const rep = await api(`/owner/preview?tile=${encodeURIComponent(x.tile)}&to=${encodeURIComponent(to)}`);
            if (rep.allowed === false) { this._xfer = { ...x, rep: null, perr: rep.error || 'not allowed' }; return; }
            this._xfer = { ...x, rep, perr: null };
          } catch (e) { this._xfer = { ...x, rep: null, perr: String(e.message ?? e) }; }
        }}>preview…</button>` : html`<button class="go" @click=${() => {
          this._xfer = null;
          this._do(() => api('/owner', jbody('POST', { tile: x.tile, to })), 'transferred');
        }}>confirm transfer</button>`}
        <button @click=${() => { this._xfer = null; }}>cancel</button>
      </div>
      ${x.perr ? html`<div class="err">${x.perr}</div>` : nothing}
      ${this._xferReport(x.rep)}
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
        <tr><th>member</th><th>preset</th><th>level</th><th>create</th><th>admin</th><th>suspended</th><th></th></tr>
        ${(o.members ?? []).map((m) => html`<tr style=${m.suspended ? 'opacity:.55' : ''}>
          <td class="mono">${m.id}${m.suspended ? html` <span class="pill">suspended</span>` : nothing}</td>
          <td><select title="apply a role preset (sets the three knobs at once)"
            @change=${(e) => { const p = e.target.value; e.target.value = ''; if (!p) return;
              const knobs = p === 'admin' ? { level: 'terminal', create: true, admin: true }
                : p === 'developer' ? { level: 'terminal', create: true, admin: false }
                : { level: 'read', create: false, admin: false };
              save((o.members ?? []).map((x) => x.id === m.id ? { ...x, ...knobs } : x), p + ' preset applied'); }}>
            <option value="">preset…</option>
            <option value="admin">Admin</option>
            <option value="developer">Developer</option>
            <option value="viewer">Viewer</option>
          </select></td>
          <td><select @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, level: e.target.value } : x), 'updated')}>
            ${['read', 'write', 'terminal'].map((l) => html`<option ?selected=${m.level === l}>${l}</option>`)}
          </select></td>
          <td><input type="checkbox" .checked=${!!m.create} title="may create org-owned tiles"
            @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, create: e.target.checked } : x), 'updated')}></td>
          <td><input type="checkbox" .checked=${!!m.admin} title="org management + allowance approvals"
            @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, admin: e.target.checked } : x), 'updated')}></td>
          <td><input type="checkbox" .checked=${!!m.suspended}
            title="pause this membership — it confers nothing while suspended, but keeps its knobs for reinstatement (D34)"
            @change=${(e) => save((o.members ?? []).map((x) => x.id === m.id ? { ...x, suspended: e.target.checked } : x),
              e.target.checked ? 'suspended' : 'reinstated')}></td>
          <td style="white-space:nowrap">
            <button title="mint a single-use password-reset link (D38; refuses for admin accounts)"
              @click=${() => this._resetByLink(m.id)}>reset link</button>
            <button class="rm" @click=${() => save((o.members ?? []).filter((x) => x.id !== m.id), 'removed')}>remove</button></td>
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
        <span class="muted" style="font-size:11px">New people are invited by a workspace admin.</span>
      </div>`;
  }

  async _resetByLink(id) {
    try {
      const d = await api('/users/' + encodeURIComponent(id) + '/invite', { method: 'POST' });
      this._invite = { id, link: d.inviteLink || location.origin + d.inviteUrl };
      this._err = '';
    } catch (e) { this._err = String(e.message ?? e); }
  }

  _inviteBox() {
    const inv = this._invite;
    if (!inv) return nothing;
    return html`<div class="card" style="border-color: var(--bx-green, #43a047)">
      <div class="row"><b style="font-size:12px">reset link for ${inv.id}</b>
        <input class="mono" size="42" readonly .value=${inv.link} @focus=${(e) => e.target.select()}>
        <button @click=${() => navigator.clipboard?.writeText(inv.link)}>copy</button>
        <span class="muted" style="font-size:10.5px">single-use · 72h · their current password
          works until they redeem it</span>
        <button @click=${() => { this._invite = null; }}>✕</button></div>
    </div>`;
  }

  // Ceilings (read-only): the policy rows this org's approvals still run
  // under — visible so a refused approval isn't a mystery (D20/D33).
  _ceilingsView(o) {
    const rows = this._policies?.[o.id] ?? [];
    if (!rows.length) return nothing;
    return html`<div class="card">
      <b style="font-size:12px">ceilings</b>
      <span class="muted" style="font-size:11px">(set by a workspace admin — approvals can't cross these)</span>
      ${rows.map((r) => html`<div class="row" style="margin:2px 0">
        <span class="pill mono">${r.tiles}</span>
        ${(r.deny ?? []).map((d) => html`<span class="pill" style="color:var(--bx-red,#e5484d)">deny ${d}</span>`)}
        ${(r.mayCall ?? []).length ? html`<span class="muted" style="font-size:11px">may call only:
          ${r.mayCall.map((m) => html`<span class="pill mono">${m}</span>`)}</span>` : nothing}
      </div>`)}
    </div>`;
  }

  // Exact-entry overrides on org tiles: authoritative rows (D31) that clamp
  // or exclude — the "why is sam still read-only" answer, in prose.
  _overridesView(o) {
    const rows = (this._overrides ?? []).filter((x) => x.org === o.id);
    if (!rows.length) return nothing;
    return html`<div class="card">
      <b style="font-size:12px">per-user overrides on org tiles</b>
      ${rows.map((x) => html`<div class="row" style="margin:2px 0">
        <span class="mono">${x.user}</span> · <span class="mono">${x.tile}</span>
        <span class="pill">${x.level}</span>
        ${x.excluded ? html`<span class="pill" style="color:var(--bx-red,#e5484d)" title="an exact none entry — deliberate exclusion (D31)">excluded</span>` : nothing}
        ${x.clamps ? html`<span class="pill" style="color:var(--bx-amber,#f2a71b)"
          title="this exact entry is BELOW the member's org level and overrides it (D31) — remove it in the tile's sharing editor to follow the org">clamps org level</span>` : nothing}
      </div>`)}
    </div>`;
  }

  // Org screens (D37): shared layouts for every member; the edit knob picks
  // who may rearrange. Created from the shell ("share this screen to org…").
  _screensView(o) {
    const rows = (this._screens ?? []).filter((x) => x.org === o.id);
    if (!rows.length) return nothing;
    return html`<div class="card">
      <b style="font-size:12px">org screens</b>
      ${rows.map((x) => html`<div class="row" style="margin:2px 0">
        <span class="mono">${x.name}</span>
        <span class="muted" style="font-size:11px">editable by</span>
        <select @change=${(e) => this._do(() => api('/screens/org',
            jbody('PUT', { id: x.id, org: x.org, edit: e.target.value, tiles: x.tiles ?? [] })), 'updated')}>
          ${['admins', 'write', 'members'].map((v) => html`<option value=${v} ?selected=${x.edit === v}>${v === 'write' ? 'write-level members' : v === 'members' ? 'all members' : 'org admins'}</option>`)}
        </select>
        <span style="flex:1"></span>
        <button class="rm" @click=${() => this._do(() =>
          api('/screens/org', jbody('DELETE', { id: x.id, org: x.org })), 'deleted')}>delete</button>
      </div>`)}
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
    if (!pending.length && !mine.length && !held.length) return nothing;
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
        ${[...new Set(held.map((p) => this._askWho(p)))].join('; ')}.</p>` : nothing}`;
  }

  // ---- wiring & ingress (D26/D41): the org admin's bindings surface ----
  // Pending slots for org tiles get the picker — and, for EXPOSED endpoints,
  // the route editor the bind call requires (http: host or zone; stream: an
  // optional listen). Existing bindings unbind here too (always allowed for
  // org admins; the slot then reappears above to re-route).
  _wiringView() {
    const bindPending = this._binds?.pending ?? [];
    const orgTiles = new Set((this._orgs ?? []).flatMap((o) => o.ownedTiles ?? []));
    const bound = [];
    for (const [comp, slots] of Object.entries(this._binds?.bindings ?? {})) {
      if (!orgTiles.has(comp)) continue;
      for (const [slot, refs] of Object.entries(slots ?? {})) {
        for (const ref of [].concat(refs ?? [])) {
          const o = (ref && typeof ref === 'object') ? ref : { ref };
          const route = o.host ?? o.zone ?? o.listen ?? '';
          bound.push({ comp, slot, ref: o.ref ?? '', route });
        }
      }
    }
    if (!bindPending.length && !bound.length) return nothing;
    const routeId = (p, f) => `bp-${p.component}-${p.slot}-${f}`;
    return html`
      <h3>wiring &amp; ingress</h3>
      ${bindPending.length ? html`<div class="card">
        ${bindPending.map((p) => html`<div class="row" style="margin:3px 0; flex-wrap:wrap">
          <span class="mono">${p.component}</span> · <span class="pill">${p.slot}</span>
          <span class="muted">${p.expose ? `publish ${p.kind}` : p.service ? `${p.kind}:${p.service}` : p.kind}</span>
          ${(p.options ?? []).length ? html`
            <select id="bp-${p.component}-${p.slot}">
              ${p.options.map((op) => html`<option value=${op.id}>${op.label}</option>`)}
            </select>
            ${p.expose && p.kind === 'http' ? html`
              <select id=${routeId(p, 'mode')} title="exact hostname, or a delegated wildcard zone">
                <option value="host">host</option><option value="zone">zone</option>
              </select>
              <input id=${routeId(p, 'val')} size="22" placeholder="app.example.com or *.zone…">` : nothing}
            ${p.expose && p.kind !== 'http' ? html`
              <input id=${routeId(p, 'listen')} size="10" placeholder=":8443 (opt)">` : nothing}
            <button class="go" @click=${() => {
              const get = (f) => this.renderRoot.getElementById(routeId(p, f))?.value?.trim() ?? '';
              const sel = this.renderRoot.getElementById(`bp-${p.component}-${p.slot}`);
              const body = { component: p.component, slot: p.slot, provider: sel.value };
              if (p.expose && p.kind === 'http') {
                const v = get('val');
                if (!v) { this._err = 'an http publish needs a hostname (host) or wildcard zone'; return; }
                if (get('mode') === 'zone') body.zone = v; else body.host = v;
              } else if (p.expose && get('listen')) {
                body.listen = get('listen');
              }
              this._do(() => api('/bindings', jbody('POST', body)), p.expose ? 'published' : 'bound');
            }}>${p.expose ? 'publish' : 'bind'}</button>` : html`<span class="muted">no provider available</span>`}
        </div>`)}
      </div>` : nothing}
      ${bound.length ? html`<div class="card">
        <b style="font-size:12px">active bindings on org tiles</b>
        ${bound.map((b) => html`<div class="row" style="margin:2px 0">
          <span class="mono">${b.comp}</span> · <span class="pill">${b.slot}</span>
          <span class="muted">→ ${b.ref}${b.route ? ` (${b.route})` : ''}</span>
          <span style="flex:1"></span>
          <button class="rm" title="unbind — the slot reappears above to re-route" @click=${() => this._do(() =>
            api('/bindings', jbody('DELETE', { component: b.comp, slot: b.slot })), 'unbound')}>unbind</button>
        </div>`)}
      </div>` : nothing}
      <p class="muted" style="font-size:11px; margin:2px 0 0">
        Publishing through your org's own terminator needs no allowance (D41);
        host ports and the builtin listener do.</p>`;
  }

  // ---- human access requests (D36) ----
  _requestsView() {
    const all = this._reqs ?? [];
    const manage = all.filter((q) => q.manage);
    const mine = all.filter((q) => q.mine && !q.manage);
    if (!manage.length && !mine.length) return nothing;
    return html`
      <h3>access requests</h3>
      ${manage.length ? html`<div class="card">
        ${manage.map((q) => html`<div class="row" style="margin:3px 0">
          <span class="mono">${q.user}</span> wants
          <select id="rq-${q.user}-${q.tile}">
            ${['read', 'write', 'terminal'].map((l) => html`<option value=${l} ?selected=${q.level === l}>${l}</option>`)}
          </select>
          on <span class="mono">${q.tile}</span>
          ${q.note ? html`<span class="muted" style="font-size:11px">— ${q.note}</span>` : nothing}
          <span style="flex:1"></span>
          <button class="go" @click=${() => {
            const sel = this.renderRoot.getElementById(`rq-${q.user}-${q.tile}`);
            this._do(() => api('/access-requests/approve',
              jbody('POST', { user: q.user, tile: q.tile, level: sel?.value || q.level })), 'granted');
          }}>approve</button>
          <button class="rm" @click=${() => this._do(() =>
            api('/access-requests', jbody('DELETE', { user: q.user, tile: q.tile })), 'dismissed')}>dismiss</button>
        </div>`)}
        <p class="muted" style="font-size:11px; margin:2px 0 0">
          Approving writes an exact entry at the chosen level (authoritative, D31).</p>
      </div>` : nothing}
      ${mine.length ? html`<div class="card">
        ${mine.map((q) => html`<div class="row" style="margin:3px 0">
          <span class="muted" style="font-size:12px">your request:</span>
          <span class="pill">${q.level}</span> on <span class="mono">${q.tile}</span>
          <span class="muted" style="font-size:11px">— pending with the tile's owner/org admins</span>
          <span style="flex:1"></span>
          <button @click=${() => this._do(() =>
            api('/access-requests', jbody('DELETE', { tile: q.tile })), 'withdrawn')}>withdraw</button>
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
      ${myOrgs.length ? myOrgs.map((o) => o.suspended
        ? html`<span class="pill" style="opacity:.6" title="this membership is paused — it confers nothing until an org admin reinstates it (D34)">
            ${o.id} · membership suspended</span>`
        : html`<span class="pill ${o.admin ? 'on' : ''}" title="level ${o.level}${o.create ? ' · may create org tiles' : ''}${o.admin ? ' · org admin' : ''}">
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
      ${this._wiringView()}
      ${this._requestsView()}
      ${this._consumersView()}
      ${this._transferCard()}
      ${this._inviteBox()}

      ${adminOrgs.map((o) => html`
        <h3>org ${o.id} <span class="muted" style="font-weight:400">${o.name !== o.id ? o.name : ''}</span></h3>
        ${(o.resolvedAllow ?? []).length ? html`<p class="muted" style="font-size:11px">
          may self-approve: ${o.resolvedAllow.map((a) => html`<span class="pill mono">${a}</span>`)}
          <span class="muted">(set by a workspace admin)</span></p>`
          : html`<p class="muted" style="font-size:11px">no allowances — grants/bindings for this
            org's tiles go through a workspace admin.</p>`}
        <div class="card">${this._memberEditor(o)}</div>
        ${this._ceilingsView(o)}
        ${this._overridesView(o)}
        ${this._screensView(o)}
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
