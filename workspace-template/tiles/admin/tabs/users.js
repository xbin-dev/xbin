/**
 * <bx-admin-users> — the admin console's users tab: the accounts table
 * (filter + chips, org pills, per-tile access, last sign-in, the per-row
 * "more ▾" menu), the click-through editors for a user's orgs / tiles /
 * create patterns, pending access requests (D36), the add-user form with
 * its three sign-in modes (D22/D52) and the one-time invite box. Data
 * arrives as properties from the router's shared lists; writes go through
 * the API and the router reloads on bx-admin-refresh.
 */
import { LitElement, html, nothing, repeat } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base, usersCss } from '../admin-css.js';
import { targetDatalist, WithDrafts, WithRouter, PRESETS, presetOf, ruleFor, agoCoarse } from '../shared.js';

// "stale" for the offboarding chip: no sign-in for 30 days.
const STALE_SEC = 30 * 86400;

export class BxAdminUsers extends WithRouter(WithDrafts(LitElement)) {
  static properties = {
    users: { attribute: false },        // /users
    orgs: { attribute: false },         // /orgs — memberships, chips, the add-user org picker
    sessions: { attribute: false },     // /sessions — the live count behind "sign out everywhere"
    reqs: { attribute: false },         // pending human access requests (D36)
    authSettings: { attribute: false }, // sso enabled/ready for the add-user sign-in mode
    targets: { attribute: false },      // tile-target datalist options
    _invite: { state: true },     // last minted invite link {id, url} (D22)
    _viewAs: { state: true },     // last minted view-as link {id, url, opened} (D64)
    _pwEdit: { state: true },     // user id whose password is being reset inline
    _newSignin: { state: true },  // add-user form: 'password' | 'invite' | 'sso'
    _usersQ: { state: true },     // table text filter
    _usersChips: { state: true }, // table chips: Set of admins|disabled|invited|never|stale|noorg|org:<id>
    _bulkBusy: { state: true },   // bulk disable in flight
    _drafts: { state: true },     // click-through editor drafts (user:<id>:{orgs|tiles|create})
    _err: { state: true },        // the last API refusal (also reported to the router's slot)
  };
  static styles = [base, usersCss];

  // The harness surface (routed here by the admin router's testApi).
  testApi() { return this.draftApi(); }

  // Row menus are native <details>; close them on outside click / Escape.
  connectedCallback() {
    super.connectedCallback();
    this._onDocDown = (e) => {
      for (const d of this.renderRoot.querySelectorAll('details.menu[open]')) {
        if (!e.composedPath().includes(d)) d.removeAttribute('open');
      }
    };
    this._onKey = (e) => { if (e.key === 'Escape') this._closeMenus(); };
    document.addEventListener('pointerdown', this._onDocDown, true);
    document.addEventListener('keydown', this._onKey);
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('pointerdown', this._onDocDown, true);
    document.removeEventListener('keydown', this._onKey);
  }
  _closeMenus() { for (const d of this.renderRoot.querySelectorAll('details.menu[open]')) d.removeAttribute('open'); }
  _menuDone(e) { e.target.closest('details')?.removeAttribute('open'); }

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
      f.reset(); this._newIdTouched = false; this._ok(); this._emit('bx-admin-refresh'); // only clear the form on success
      const where = org ? `, member of ${org}` : '';
      this._emit('bx-admin-notice', signin === 'sso' ? `${body.id} created — signs in via SSO as ${body.email}${where}` : `${body.id} created${where}`);
    } catch (e) { this._fail(e); }
  }
  _editEmail(u) {
    const v = prompt(`Email bound to ${u.id} — a verified SSO sign-in for it lands on this account (empty clears):`, u.email ?? '');
    if (v == null) return;
    this._patchUser(u.id, { email: v.trim() }).catch((e) => this._fail(e));
  }
  async _patchUser(id, patch) {
    await api(`/users/${encodeURIComponent(id)}`, { method: 'PATCH',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(patch) });
    this._emit('bx-admin-refresh');
  }
  async _resetPw(id, pw) {
    if (!pw) return;
    if (pw.length < 8) { this._fail('password too short (min 8 characters)'); return; }
    try {
      await this._patchUser(id, { password: pw });
      this._ok(); this._emit('bx-admin-notice', `password reset for ${id}`);
    } catch (e) { this._fail(e); }
  }
  async _setDisabled(u) {
    if (!u.disabled && !confirm(`Disable ${u.id}? Their sessions and terminals stop working now; everything is kept for re-enable.`)) return;
    try {
      await this._patchUser(u.id, { disabled: !u.disabled });
      this._ok();
    } catch (e) { this._fail(e); } // self/last-admin guards land here
  }
  async _delUser(id) {
    if (!confirm(`Delete user ${id}? Their sessions are revoked immediately.`)) return;
    await api(`/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
    this._emit('bx-admin-refresh');
  }

  _presetLabel(m) {
    const p = presetOf(m);
    return p !== 'custom' ? p : `${m.level}${m.create ? '+create' : ''}`;
  }
  // The users table's org pills: manual vs ⟳ synced (dashed), ★ org admin.
  _userOrgsCell(u) {
    const pills = [];
    for (const o of (this.orgs ?? [])) {
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
    return html`<span class=${stale ? 'muted' : ''} title=${new Date(u.lastLogin * 1000).toLocaleString()}>${agoCoarse(u.lastLogin)}</span>
      <span class="muted" title=${groups}>· ${u.lastLoginVia || ''}</span>`;
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
    this._emit('bx-admin-refresh');
    this._emit('bx-admin-notice', `disabled ${ids.length - failed.length} account${ids.length - failed.length === 1 ? '' : 's'}`);
    if (failed.length) this._fail('could not disable — ' + failed.join('; ')); else this._ok();
  }

  async _mintInvite(id) {
    try {
      const d = await api(`/users/${encodeURIComponent(id)}/invite`, { method: 'POST' });
      this._invite = { id, url: d.inviteLink || location.origin + d.inviteUrl };
      this._ok(); this._emit('bx-admin-notice', `invite link minted for ${id}`);
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  // View as user (docs/auth.md §Viewing the workspace as a user, D64): mint
  // the one-shot link and open it top-level. The tile runs sandboxed with
  // cap:open-links, so the new tab is a real one carrying the admin's own
  // cookie — which is what the link is bound to. The box stays for a
  // browser that blocked the popup: open or copy the link yourself.
  async _startViewAs(id) {
    try {
      const d = await api('/impersonate', jbody({ user: id }, 'POST'));
      const url = location.origin + d.url;
      let opened = false;
      try { opened = !!window.open(url, '_blank'); } catch { opened = false; }
      this._viewAs = { id, url, opened };
      this._ok();
    } catch (e) { this._fail(e); }
  }

  _viewAsBox() {
    const v = this._viewAs;
    if (!v) return nothing;
    return html`<div data-viewas style="margin:8px 0; padding:8px 10px; border:1px solid var(--bx-amber, #f2a71b);
        border-radius:6px; display:flex; gap:8px; align-items:center; flex-wrap:wrap">
      <b style="font-size:12px">👁 viewing as ${v.id}</b>
      <span class="muted" style="font-size:11px">${v.opened ? 'opened in a new tab — exit from the banner there.' : 'the browser blocked the new tab — open it yourself:'}</span>
      <a class="link" href=${v.url} target="_blank" rel="noopener">open</a>
      <input class="mono" size="40" readonly .value=${v.url} @focus=${(e) => e.target.select()}>
      <button class="act" @click=${() => navigator.clipboard?.writeText(v.url)}>copy</button>
      <span class="muted" style="font-size:10.5px">read-only · works for 2 minutes, in this browser only · every tab is them until you exit</span>
      <button class="act" @click=${() => { this._viewAs = null; }}>✕</button>
    </div>`;
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
    const reqs = (this.reqs ?? []).filter((q) => q.manage);
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
            this._ok();
          } catch (e) { this._fail(e); }
          this._emit('bx-admin-refresh');
        }}>approve</button>
        <button class="act rm" @click=${async () => {
          try {
            await api('/access-requests', { method: 'DELETE', headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ user: q.user, tile: q.tile }) });
            this._ok();
          } catch (e) { this._fail(e); }
          this._emit('bx-admin-refresh');
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
      { id: 'noorg', label: 'no org', test: (u) => !(this.orgs ?? []).some((o) => (o.members ?? []).some((m) => m.id === u.id)) },
    ];
    for (const o of (this.orgs ?? [])) {
      defs.push({ id: `org:${o.id}`, label: o.id, test: (u) => (o.members ?? []).some((m) => m.id === u.id) });
    }
    return defs;
  }
  _userMatches(u, q, chips, defs) {
    if (q) {
      const orgIds = (this.orgs ?? []).filter((o) => (o.members ?? []).some((m) => m.id === u.id)).map((o) => o.id);
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

  render() {
    const users = this.users ?? [];
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
      ${targetDatalist(this.targets)}
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
        ${this.users == null ? html`<tr><td class="muted" colspan="6">loading…</td></tr>`
          : !users.length ? html`<tr><td class="muted" colspan="6">no users — the root token is the only admin. Add one below.</td></tr>`
          : !rows.length ? html`<tr><td class="muted" colspan="6">no users match — <a class="link"
              @click=${() => { this._usersQ = ''; this._usersChips = new Set(); }}>clear filters</a></td></tr>`
          : repeat(rows, (u) => u.id, (u) => this._userRow(u))}
      </table>

      ${this._requestsView()}

      <h4>add user</h4>
      ${(() => {
        const ssoOn = !!this.authSettings?.sso?.enabled;
        const ssoReady = !!this.authSettings?.sso?.ready;
        const signin = this._newSignin ?? (ssoReady ? 'sso' : 'password');
        const orgs = this.orgs ?? [];
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
      ${this._viewAsBox()}
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
    const live = (this.sessions ?? []).filter((s) => s.user === u.id).length;
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
        <button ?disabled=${!!u.disabled} title=${u.disabled ? 'enable the account first' : 'open a new tab signed in as them — read-only, exit from its banner (D64)'}
          @click=${() => this._startViewAs(u.id)}>view as user…</button>
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
    const orgs = this.orgs ?? [];
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
              <button class="act" title="stop syncing this membership; it becomes manual" @click=${async () => { await this._setMembership(o.id, u.id, { via: '' }); if (!this._err) this._emit('bx-admin-notice', `${o.id}: ${u.id} is now a manual member`); }}>detach</button>` : nothing}
            ${ruleMatches ? html`<span class="muted" title="a group rule also matches this user; remove this manual row to let sync manage it">manual (rule also matches)</span>` : nothing}`
            : html`<span class="muted">not a member — tick to join as developer</span>`}
        </div>`;
      })}
      <div class="orow" style="margin-top:4px">
        <span class="muted">IdP groups at last sign-in:</span>
        ${groups.length ? groups.map((g) => { const org = ruleFor(this.orgs, g); return html`<span class="pill mono" title=${org ? `rule → ${org}` : 'no rule maps this group'}>${g}${org ? ` → ${org}` : ''}</span>`; })
          : html`<span class="muted">none seen yet</span>`}
        <span style="flex:1"></span>
        <span class="muted" style="font-size:10.5px">changes save immediately · one request per click</span>
        <button class="act" @click=${() => this._dropDraft(key)}>close</button>
      </div>
    </div>`;
  }
}

customElements.define('bx-admin-users', BxAdminUsers);
