/**
 * <bx-admin-orgs> — the admin console's organisations tab (docs/auth.md
 * §Ownership, D24–D28): a LIST of orgs to drill into (#orgs/<id>), and one
 * org's page laid out as titled panels — members with role presets and
 * single-row membership writes (D53), IdP-group rules, delegation sets plus
 * the org's extra allowances, network sets (D54), owned tiles, its policy
 * ceiling, deletion — with the workspace-wide knobs under the list: default
 * visibility (D27), new-account defaults + tile creation (D52), the
 * workspace policy ceiling (D20). Orgs are flat member lists with org-wide
 * roles on org-OWNED tiles; the ws-admin delegates approval via
 * allowances/permission sets. This is the workspace-admin console — org
 * admins use tiles/organisations. Data arrives as properties; the drill-in
 * is the router's hash (sub); every write goes through the API and the
 * router reloads the shared lists on bx-admin-refresh.
 */
import { LitElement, html, nothing, repeat } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { ruleLabel } from '/vendor/bx-netrules.js';
import { parseAllow, fmtAllow, allowProblem, describeAllow } from '/vendor/bx-allow.js';
import '/vendor/bx-multiselect.js';
import { base, orgsCss } from '../admin-css.js';
import { targetDatalist, serviceDatalist, groupsDatalist, allowRows, WithDrafts, WithRouter, PRESETS, presetOf } from '../shared.js';

export class BxAdminOrgs extends WithRouter(WithDrafts(LitElement)) {
  static properties = {
    sub: { attribute: false },          // the router's drill-in: '' (the list) or an org id (#orgs/<id>)
    orgs: { attribute: false },         // /orgs (members, sets, allow, netSets, ownedTiles, policy, ssoGroups)
    users: { attribute: false },        // /users — the add-member picker
    wsPolicy: { attribute: false },     // workspace policy-ceiling rows
    permsets: { attribute: false },     // {sets, attachedTo} (D28) — the delegation picker
    netsets: { attribute: false },      // {sets, attachedTo} (D54) — the network picker
    defaults: { attribute: false },     // defaultTiles map (D27)
    newUsers: { attribute: false },     // new-account defaults {tiles, canCreate, termApi, termNet, orgs} (D52)
    tileCreation: { attribute: false }, // 'any' | 'org-only' (D52)
    authSettings: { attribute: false }, // sso preset + groups seen, for the IdP-group rule editors
    targets: { attribute: false },      // tile-target datalist options
    services: { attribute: false },     // http services (shared.serviceOptions)
    _org: { state: true },     // the org page shown, null for the list (follows sub)
    _polEdit: { state: true }, // policy-editor drafts, keyed '' (workspace) / org id
    _drafts: { state: true },  // click-through editor drafts (orgallow:<org>, ws:defaults, ws:newusers:*)
    _err: { state: true },     // the last API refusal (also reported to the router's slot)
  };
  static styles = [base, orgsCss];

  // The harness surface (routed here by the admin router's testApi).
  testApi() { return { ...this.draftApi(), org: this._org ?? null, openOrg: (id) => this._open(id) }; }

  // ---- list ⇄ org page: the router owns the hash; we follow its sub ----
  willUpdate(changed) {
    if (changed.has('sub')) this._org = this.sub || null;
  }
  _open(id) { this._emit('bx-admin-tab', `orgs/${id}`); }
  _back() { this._emit('bx-admin-tab', 'orgs'); }

  // A titled panel: the org page's sections and the workspace knobs.
  _panel(title, desc, body, { cls = '', extra = nothing } = {}) {
    return html`<section class="panel ${cls}">
      <div class="ph"><h3>${title}</h3>${desc ? html`<span class="desc">${desc}</span>` : nothing}<span class="sp"></span>${extra}</div>
      ${body}
    </section>`;
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
        ${!(rows ?? []).length ? html`<div class="empty">no rows (no ceiling)</div>` : nothing}
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

  async _deleteOrg(o) {
    if (!confirm(`Delete org ${o.id}?`)) return;
    await this._orgAPI('DELETE', `/orgs/${encodeURIComponent(o.id)}`);
    if (!this._err) this._back();
  }

  // One membership row on the org page: every control is one PUT on the
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
            const p = PRESETS[e.target.value];
            if (p) save(p);
          }}>
          ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${presetOf(m) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
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
          @click=${async () => { await save({ via: '' }); if (!this._err) this._emit('bx-admin-notice', `${o.id}: ${m.id} is now a manual member`); }}>detach</button>` : nothing}
        <button class="act rm" title=${synced ? 'removes now — comes back at their next sign-in while the rule stands' : 'remove from the org'}
          @click=${() => this._dropMembership(o.id, m.id)}>remove</button></td>
    </tr>`;
  }

  // Add-member row: pick a person and a preset (not just "developer").
  _addMemberRow(o, addable) {
    return html`<div class="kv" style="margin-top:6px">
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
        this._setMembership(o.id, sel.value, PRESETS[p]);
      }}>add</button>
      <span class="empty">or add an IdP-group rule below</span>
    </div>`;
  }

  // IdP groups → members: the org's rules (docs/auth.md §Group sync, D53).
  _idpGroupsEditor(o) {
    const rules = o.ssoGroups ?? [];
    const sso = this.authSettings?.sso ?? {};
    const hint = this._idpGroupHint(sso.preset);
    const known = (sso.groupSync?.knownGroups ?? []).filter((g) => !rules.some((r) => r.group.toLowerCase() === g.toLowerCase()));
    const save = (next) => this._orgAPI('PUT', `/orgs/${encodeURIComponent(o.id)}/sso-groups`, { rules: next });
    const knobs = (r) => ({ level: r.level, create: !!r.create, admin: !!r.admin });
    return html`
      <div>
        ${rules.map((r) => html`<span class="rule">
          <span class="mono">${r.group}</span> →
          <select title="role preset for members synced from this group" @change=${(e) => {
              const p = PRESETS[e.target.value];
              if (p) save(rules.map((x) => (x.group === r.group ? { group: r.group, ...p } : x)));
            }}>
            ${['admin', 'developer', 'viewer', 'custom'].map((p) => html`<option value=${p} ?selected=${presetOf(knobs(r)) === p} ?disabled=${p === 'custom'}>${p}</option>`)}
          </select>
          <button class="act rm" title="delete the rule (synced members stay until their next sign-in)"
            @click=${() => save(rules.filter((x) => x.group !== r.group))}>✕</button>
        </span>`)}
        ${!rules.length ? html`<span class="empty">no rules — members are added by hand</span>` : nothing}
      </div>
      <div class="kv" style="margin-top:6px">
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
          if (rules.some((x) => x.group.toLowerCase() === g.toLowerCase())) { this._fail(`rule for ${g} already exists`); return; }
          save([...rules, { group: g, ...PRESETS[p] }]).then(() => { if (!this._err && inp) inp.value = ''; });
        }}>add rule</button>
        ${known.length ? html`<span class="empty">${known.length} unmapped group${known.length === 1 ? '' : 's'} seen at sign-ins — start typing</span>` : nothing}
      </div>
      <div class="foot">${hint.text} Synced members show ⟳ and follow the group at
        every sign-in — leave the group, lose the membership. ${!sso.enabled ? 'SSO is off — rules take effect once a provider is active (sign-in tab).' : ''}</div>`;
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

  // ---- the list ----
  _orgRow(o) {
    const members = o.members ?? [];
    const synced = members.filter((m) => m.via === 'sso').length;
    const admins = members.filter((m) => m.admin && !m.suspended).length;
    const sets = o.sets ?? [];
    const allow = (o.allow ?? []).length;
    const nets = o.netSets ?? [];
    const owned = (o.ownedTiles ?? []).length;
    const pol = (o.policy ?? []).length;
    return html`<tr class="row">
      <td><a class="link org mono" @click=${() => this._open(o.id)}>${o.id}</a>${o.name && o.name !== o.id ? html` <span class="muted">${o.name}</span>` : nothing}</td>
      <td class="n">${members.length}${synced ? html` <span class="muted" title="synced from IdP groups">· ⟳ ${synced}</span>` : nothing}</td>
      <td class="n">${admins}</td>
      <td>${sets.map((s) => html`<span class="pill mono">${s}</span>`)}${allow ? html`<span class="pill" title="extra allowances on top of its sets">+${allow} extra</span>` : nothing}${!sets.length && !allow ? html`<span class="empty">none</span>` : nothing}</td>
      <td>${nets.map((s) => html`<span class="pill mono">${s}</span>`)}${o.netHost ? html`<span class="pill pol" title="a set grants host networking">⚠ host</span>` : nothing}${!nets.length ? html`<span class="empty">none</span>` : nothing}</td>
      <td class="n">${owned || html`<span class="empty">0</span>`}</td>
      <td class="n">${pol ? `${pol} row${pol === 1 ? '' : 's'}` : html`<span class="empty">none</span>`}</td>
      <td style="text-align:right"><button class="act go" data-org=${o.id} @click=${() => this._open(o.id)}>manage →</button></td>
    </tr>`;
  }

  _orgList() {
    const orgs = this.orgs ?? [];
    return html`
      <h4>organisations <span class="muted" style="font-weight:400; text-transform:none; letter-spacing:0">· ${orgs.length}</span></h4>
      ${orgs.length ? html`<table class="orglist">
        <tr><th>org</th><th class="n">members</th><th class="n">admins</th><th>delegation</th><th>network</th><th class="n">owned tiles</th><th class="n">policy</th><th></th></tr>
        ${repeat(orgs, (o) => o.id, (o) => this._orgRow(o))}
      </table>`
        : html`<p class="muted">No orgs. An org is a flat member list with org-wide roles on the
          tiles the org <b>owns</b> (ownership is assigned at create and transferable — D24/D25).
          Attach permission sets to delegate grant/binding approval to its admins.</p>`}
      <form class="inline" @submit=${(e) => { e.preventDefault(); this._createOrg(e.target); }}>
        <input name="id" placeholder="org id" size="14" required>
        <input name="orgname" placeholder="display name" size="14">
        <button class="act go">add org</button>
      </form>

      <h4 style="margin-top:18px">workspace-wide</h4>
      ${this._panel('workspace defaults', 'baseline visibility every user gets (D27) — pattern → level', this._defaultsEditor())}
      ${this._panel('new accounts', 'what every NEW account starts with (D52)', html`
        <p class="muted" style="font-size:11px; max-width:70ch; margin:0 0 6px">Copied onto the row at creation (admin-added, invited, or SSO
          auto-provisioned) on top of what the creator specifies; editable per user afterwards. This is where "everyone from the
          SSO domain lands in org X as a developer" lives. Never grants admin.</p>
        ${this._newUsersEditor()}`)}
      ${this._panel('tile creation', 'who may create tiles outside an organisation (D52)', html`
        <select @change=${(e) => this._putDefaults({ tileCreation: e.target.value })}>
          <option value="any" ?selected=${(this.tileCreation ?? 'any') === 'any'}>any — users create personal tiles, and org-owned ones where they hold Create</option>
          <option value="org-only" ?selected=${this.tileCreation === 'org-only'}>org-only — non-admins may only create organisation-owned tiles (needs Create in an org)</option>
        </select>
        <div class="foot">workspace-owned tiles are always an admin act; this governs non-admins' personal tiles.</div>`)}
      ${this._panel('workspace policy', 'pattern-keyed ceiling on what tiles may be granted, applied to EVERY tile (D20)', html`
        ${this._policyEditor('', this.wsPolicy)}
        <div class="foot">org and permission-set rows add on top; any deny wins; deny beats every allowance.</div>`)}
      <p class="muted" style="font-size:11px; margin-top:10px; max-width:70ch">
        Effective access is a union: workspace admin · tile OWNER (terminal) · org member level /
        org-admin terminal on org-owned tiles · org shares · a user's own entries · workspace
        defaults. Org admins manage members and org-tile ACLs in the
        <span class="mono">tiles/organisations</span> tile; permission sets, allowances, policy
        and org create/delete stay here.</p>`;
  }

  // ---- one org's page ----
  _orgPage(o) {
    const opath = `/orgs/${encodeURIComponent(o.id)}`;
    const members = o.members ?? [];
    const memberIds = new Set(members.map((m) => m.id));
    const addable = (this.users ?? []).filter((u) => !memberIds.has(u.id));
    const synced = members.filter((m) => m.via === 'sso').length;
    const owned = o.ownedTiles ?? [];
    return html`
      <div class="crumbs"><a class="link" data-orgs-back @click=${() => this._back()}>← organisations</a> › <span class="mono">${o.id}</span></div>
      <div class="orghead">
        <span class="id mono">${o.id}</span>
        ${o.name && o.name !== o.id ? html`<span class="muted">${o.name}</span>` : nothing}
        <span class="muted" style="font-size:11px">${members.length} member${members.length === 1 ? '' : 's'}${synced ? ` · ${synced} synced` : ''}
          · ${owned.length} owned tile${owned.length === 1 ? '' : 's'}</span>
      </div>

      ${this._panel('members', 'org-wide roles on the tiles this org owns', html`
        <table>
          ${members.length ? html`<tr><th>member</th><th>preset</th><th>level</th><th>create</th><th>admin</th><th>susp</th><th></th></tr>` : nothing}
          ${repeat(members, (m) => m.id, (m) => this._memberRow(o, m))}
        </table>
        ${!members.length ? html`<div class="empty">no members yet</div>` : nothing}
        ${this._addMemberRow(o, addable)}`)}

      ${this._panel('IdP groups → members', 'rules that add members at sign-in (D53)', this._idpGroupsEditor(o))}

      ${this._panel('delegation', 'what its admins may approve on their own tiles without a workspace admin (D26/D28)', this._delegationBody(o, opath))}

      ${this._panel('network', 'the network sets this org holds — what its tiles reach (D54)', this._orgNetBlock(o, opath))}

      ${this._panel('owned tiles', 'tiles the org owns (D24) — ⇄ transfers ownership', owned.length
        ? html`<div>${owned.map((p) => html`
            <span class="pill mono">${p} <a class="link" title="transfer ownership" @click=${() => this._transferTile(p)}>⇄</a></span>`)}</div>`
        : html`<div class="empty">none — tiles members create with Create land here, and transfers bring others in</div>`)}

      ${this._panel('policy ceiling', 'applies to the org\'s owned tiles on top of the workspace policy (D20)', this._policyEditor(o.id, o.policy))}

      ${this._panel('danger zone', '', html`<div class="kv">
        <button class="act rm" @click=${() => this._deleteOrg(o)}>delete org…</button>
        <span class="empty">${owned.length ? 'transfer its owned tiles away first — the server refuses while it owns any' : 'members lose their org roles; accounts and tiles stay'}</span>
      </div>`, { cls: 'danger' })}`;
  }

  // Delegation: sets + the org's extra allow entries + what that resolves to.
  _delegationBody(o, opath) {
    const setNames = Object.keys(this.permsets?.sets ?? {});
    return html`
      <div class="kv">
        <label>sets
          <bx-multiselect style="min-width:130px"
            .options=${setNames.map((n) => ({ value: n, label: n }))}
            .selected=${o.sets ?? []} placeholder="— none —"
            @change=${(e) => this._orgAPI('PATCH', opath, { sets: e.detail.selected })}></bx-multiselect></label>
        <label>extra allow
          ${(o.allow ?? []).length ? o.allow.map((a) => html`<span class="pill mono" title=${describeAllow(a)}>${a}</span>`) : html`<span class="empty">none</span>`}
          <button class="act" data-edit-allow ?disabled=${!!this._draft(`orgallow:${o.id}`)}
            @click=${() => this._setDraft(`orgallow:${o.id}`, { rows: (o.allow ?? []).map(parseAllow), err: '' })}>edit</button></label>
      </div>
      ${this._draft(`orgallow:${o.id}`) ? this._orgAllowEditor(o, opath) : nothing}
      ${(o.resolvedAllow ?? []).length ? html`<div style="margin-top:5px">
        <span class="muted" style="font-size:10.5px">org admins may self-approve:</span>
        ${o.resolvedAllow.map((a) => html`<span class="pill mono" title=${describeAllow(a)}>${a}</span>`)}</div>`
        : html`<div class="empty" style="margin-top:5px">no allowances — every grant/binding goes through a workspace admin</div>`}
      <div class="foot">allowance grammar: res:/gpu:/cap:/net:internet|host|lan:…|provider:…/iface:&lt;svc&gt;/ingress:host|zone|listen:&lt;range&gt;/tile:&lt;pat&gt; — xbin is never delegable</div>`;
  }

  // Network (D54): which network sets the org holds, what its own tiles
  // therefore reach, and the one-line semantics. ws-admin only (the server
  // refuses the field from org admins).
  _orgNetBlock(o, opath) {
    const names = Object.keys(this.netsets?.sets ?? {}).sort();
    const sets = o.netSets ?? [];
    const rules = o.resolvedNet ?? [];
    return html`
      <div class="kv">
        <label>network sets
          <bx-multiselect style="min-width:130px"
            .options=${names.map((n) => ({ value: n, label: n }))}
            .selected=${sets} placeholder="— none —"
            @change=${(e) => this._orgAPI('PATCH', opath, { netSets: e.detail.selected })}></bx-multiselect></label>
        ${!names.length ? html`<a class="link" style="font-size:11px" @click=${() => this._emit('bx-admin-tab', 'netsets')}>create one in network sets →</a>` : nothing}
      </div>
      ${sets.length ? html`
        <div style="margin-top:5px">
          <span class="muted" style="font-size:10.5px">org tiles reach:</span>
          ${rules.filter((r) => r !== 'host').map((r) => html`<span class="pill mono" title=${r}>${ruleLabel(r)}</span>`)}
          ${o.netHost ? html`<span class="pill pol" title="a set grants host networking: every org-bound tile and terminal shares the host's network stack — no relay, no filtering, no metering">⚠ host networking</span>` : nothing}
          ${!rules.length ? html`<span class="empty">nothing — the attached sets carry no rules (airgapped, incl. DNS)</span>` : nothing}
        </div>
        <div class="foot">org-owned tiles that declare
          <span class="mono">net</span> bind to <span class="mono">org</span> by default; org admins may bind
          anything inside it; terminals on org tiles get the same reach — no term-net needed.</div>`
        : html`<div class="foot">no network sets — org tiles'
          <span class="mono">net</span> slots stay unbound until a workspace admin binds them explicitly;
          terminals on them fall back to term-net.</div>`}`;
  }

  // An org's extra allow entries (on top of its sets): the same typed rows.
  _orgAllowEditor(o, opath) {
    const key = `orgallow:${o.id}`;
    const d = this._draft(key);
    const wire = d.rows.map(fmtAllow);
    const ok = !d.rows.some((r) => allowProblem(r)) && new Set(wire).size === wire.length;
    return html`<div class="editor" style="margin-top:6px">
      <div class="muted" style="margin-bottom:2px">Extra entries for <b>this org only</b> (on top of its sets) — its admins may approve, on their own tiles:</div>
      ${allowRows(d.rows, (rows) => this._setDraft(key, { ...this._draft(key), rows }), { gotoTab: (x) => this._emit('bx-admin-tab', x) })}
      ${d.err ? html`<div class="err" role="alert">${d.err}</div>` : nothing}
      <div class="orow" style="margin-top:6px">
        <button class="act go" data-save-allow ?disabled=${!ok} @click=${async () => {
          try {
            await api(opath, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allow: wire.filter(Boolean) }) });
            this._ok(); this._dropDraft(key);
          } catch (e) { this._setDraft(key, { ...this._draft(key), err: String(e.message ?? e) }); }
          this._emit('bx-admin-refresh');
        }}>save</button>
        <button class="act" @click=${() => this._dropDraft(key)}>cancel</button>
      </div>
    </div>`;
  }

  render() {
    const org = this._org ? (this.orgs ?? []).find((o) => o.id === this._org) : null;
    return html`
      ${targetDatalist(this.targets)}
      ${groupsDatalist(this.authSettings?.sso?.groupSync?.knownGroups)}
      ${serviceDatalist(this.services)}
      ${!this._org ? this._orgList()
        : org ? this._orgPage(org)
        : html`<div class="crumbs"><a class="link" data-orgs-back @click=${() => this._back()}>← organisations</a> › <span class="mono">${this._org}</span></div>
          <p class="muted">${this.orgs ? `no organisation "${this._org}" — deleted, or a stale bookmark.` : 'loading…'}</p>`}`;
  }

  _defaultsEditor() {
    const key = 'ws:defaults';
    const d = this._draft(key);
    if (!d) {
      return html`
        ${Object.entries(this.defaults ?? {}).map(([p, l]) => html`<span class="pill lv-${l}">${p} · ${l}</span>`)}
        ${!Object.keys(this.defaults ?? {}).length ? html`<span class="empty">none</span>` : nothing}
        <button class="act" style="margin-left:4px" @click=${() => this._toggleDraft(key,
          () => Object.entries(this.defaults ?? {}).map(([target, level]) => ({ target, level })))}>edit</button>`;
    }
    return this._tilesEditor(key, (tiles) => this._orgAPI('PUT', '/defaults', { defaultTiles: tiles }));
  }

  // ---- new-account defaults + tile-creation policy (D52) ----
  // PUT /defaults replaces only the keys given, so each control saves its
  // own setting without clobbering the others.
  async _putDefaults(patch) {
    try {
      const d = await api('/defaults', jbody(patch, 'PUT'));
      this.defaults = d.defaultTiles ?? {}; this.newUsers = d.newUsers ?? {};
      this.tileCreation = d.tileCreation ?? 'any'; this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  _newUsersEditor() {
    const nu = this.newUsers ?? {};
    const tilesKey = 'ws:newusers:tiles';
    const createKey = 'ws:newusers:create';
    const rows = nu.orgs ?? [];
    const unused = (this.orgs ?? []).filter((o) => !rows.some((r) => r.org === o.id));
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

customElements.define('bx-admin-orgs', BxAdminOrgs);
