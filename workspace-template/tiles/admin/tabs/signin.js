/**
 * <bx-admin-signin> — the admin console's sign-in tab: token-URL login
 * on/off, owner-token rotation (with the copy box), single sign-on
 * (generic OIDC or GitHub, docs/auth.md §SSO, D51) with group sync, a
 * connection test, the groups the IdP has been sending, and the SSO-only
 * policy. Settings arrive as properties; every save goes through
 * /auth-settings and the router reloads on bx-admin-refresh.
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { groupsDatalist, WithRouter, ruleFor, agoCoarse } from '../shared.js';

export class BxAdminSignin extends WithRouter(LitElement) {
  static properties = {
    authSettings: { attribute: false }, // /auth-settings (tokenLoginDisabled, canDisable, sso, …)
    users: { attribute: false },        // /users — who carries each IdP group
    orgs: { attribute: false },         // /orgs — which org an IdP group maps to
    _token: { state: true },   // freshly rotated owner token (copy-field box)
    _ssoTest: { state: true }, // last "test connection" result (null | {busy} | report)
    _err: { state: true },     // the last API refusal (also reported to the router's slot)
  };
  static styles = [base];

  async _setTokenLogin(disabled) {
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenLoginDisabled: disabled }) });
      this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  render() {
    const s = this.authSettings;
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
      this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
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
            <span class="mono">${c.groupSync.lastError.user}</span> ${agoCoarse(c.groupSync.lastError.at)}: ${c.groupSync.lastError.error}</div>` : nothing}
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
  _groupsDatalist() { return groupsDatalist(this.authSettings?.sso?.groupSync?.knownGroups); }
  // What the IdP has actually been sending, with where each group lands.
  _groupsSeenView(c) {
    const known = c.groupSync?.knownGroups ?? [];
    const adminGroups = (c.adminGroups || []).map((g) => g.toLowerCase());
    const users = this.users ?? [];
    return html`<div style="margin-top:8px">
      <span class="muted" style="font-size:10.5px; letter-spacing:.05em; text-transform:uppercase">groups seen</span>
      <span class="muted"> — what the IdP actually sent, over everyone’s last sign-in</span>
      <div style="margin-top:3px">
        ${known.length ? known.map((g) => {
          const who = users.filter((u) => (u.ssoGroups ?? []).some((x) => x.toLowerCase() === g.toLowerCase())).map((u) => u.id);
          const org = ruleFor(this.orgs, g);
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
      this._emit('bx-admin-notice', 'SSO configuration saved'); this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  async _testSSO(preset) {
    this._ssoTest = { busy: true };
    try {
      this._ssoTest = await api('/auth-settings/sso/test', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sso: this._ssoPayload(preset) }) });
      this._ok();
    } catch (e) { this._ssoTest = null; this._fail(e); }
  }

  async _clearSSO() {
    if (!confirm('Disable SSO sign-in? The login page drops the SSO button; existing sessions stay.')) return;
    try {
      await api('/auth-settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sso: null }) });
      this._ssoPreset = ''; this._ok();
    } catch (e) { this._fail(e); }
    this._emit('bx-admin-refresh');
  }

  async _rotateToken() {
    if (!confirm('Rotate the owner token? The current token stops working immediately (bearer + cookie). Host-side bx/automation must switch to the new one.')) return;
    try {
      const d = await api('/auth-rotate-token', { method: 'POST' });
      this._token = d.token; // rendered in a copy-field box (like invites)
      this._ok();
    } catch (e) { this._fail(e); }
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
}

customElements.define('bx-admin-signin', BxAdminSignin);
