/**
 * <bx-admin-sessions> — the admin console's sessions tab: live browser
 * sessions with their client IPs, the attribution view for the /c/ warm-IP
 * gate (a tile's credential-less subresource loads are served only from an
 * IP that authenticated within the last hour; behind a reverse proxy set
 * --trusted-proxies or every row shows the proxy). The router loads and
 * polls /sessions and passes the list down; sign-out reports and refreshes
 * through the router's events.
 */
import { LitElement, html, nothing, repeat } from 'lit';
import { base } from '../admin-css.js';
import { fmtDur, WithRouter } from '../shared.js';

export class BxAdminSessions extends WithRouter(LitElement) {
  static properties = {
    sessions: { attribute: false }, // /sessions
    _sessQ: { state: true },        // table user filter
    _err: { state: true },
  };
  static styles = [base];

  _ago(unixSec) {
    const s = Math.max(0, (Date.now() - unixSec * 1000) / 1000);
    return (s < 60 ? (s | 0) + 's' : fmtDur(s)) + ' ago';
  }
  render() {
    const all = this.sessions ?? [];
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
          <td class="mono">${s.user}${s.name && s.name !== s.user ? html` <span class="muted">${s.name}</span>` : nothing}${s.impersonatedBy
            ? html` <span class="pill" title="an admin's read-only view of this user (D64) — ends when they exit the banner">👁 viewed by ${s.impersonatedBy}</span>` : nothing}</td>
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
}

customElements.define('bx-admin-sessions', BxAdminSessions);
