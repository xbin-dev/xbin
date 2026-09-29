/**
 * <bx-admin-policies> — the admin console's workspace → policies tab: the
 * workspace policies for partitioned tiles (PD-55; docs/protocol.md
 * §workspace-policies). Two switches, both off by default:
 *  - partitionConsent: ask each person before another partitioned tile uses
 *    their data;
 *  - credentialResetConfirm: credential resets wait for the person.
 * Saves go through PUT /workspace-policies (admin), one key at a time;
 * every open console re-reads on the `policies` event. The D20 grant
 * ceiling ("workspace policy") is a different thing and stays in the
 * organisations tab.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';

const SWITCHES = [
  {
    key: 'partitionConsent',
    label: 'Ask each person before another partitioned tile uses their data',
    off: 'Off: a partitioned tile with a grant on another partitioned tile reaches the data kept there by every person who can open that other tile.',
    on: 'On: it reaches a person\'s data only once they allow it; each person is asked the first time a call is refused. Turning it off keeps their answers for later.',
    confirm: 'Calls between partitioned tiles that nobody has allowed yet are refused from the next call, and each person is asked on their first refusal.',
    notice: (v) => v ? 'people are now asked before another partitioned tile uses their data' : 'partitioned tiles no longer ask people first (their answers are kept)',
  },
  {
    key: 'credentialResetConfirm',
    label: 'Credential resets wait for the person',
    off: 'Off: a sign-in link, password or SSO email an admin sets works at once.',
    on: 'On: a sign-in link, password or SSO email set by an admin for someone who holds partitions works only after they confirm, or 24 h after they\'re notified.',
    notice: (v) => v ? 'credential resets now wait for the person' : 'credential resets work at once again',
  },
];

export class BxAdminPolicies extends WithRouter(LitElement) {
  static properties = {
    _state: { state: true },   // GET /workspace-policies (null until loaded)
    _asking: { state: true },  // the key whose turn-on awaits confirmation
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, css`
    .card { max-width: 620px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 12px 14px;
            background: var(--bx-panel, #23272e); margin-bottom: 10px; }
    .card h4 { margin: 0 0 6px; }
    label.sw { display: flex; gap: 8px; align-items: flex-start; font-size: 12px; }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
    .line { font-size: 12px; margin: 6px 0 0 24px; }
    .scope { display: inline-block; margin-left: 6px; padding: 0 6px; border-radius: 8px; font-size: 11px; font-weight: 400;
             color: var(--bx-muted, #868f9a); border: 1px solid var(--bx-border, #363c45); }
    .ask { margin: 8px 0 0 24px; padding: 8px 10px; border-radius: 6px; font-size: 12px;
           background: color-mix(in srgb, var(--bx-amber, #f2a71b) 14%, transparent); }
    .ask .row { display: flex; gap: 6px; margin-top: 6px; }
  `];

  constructor() { super(); this._state = null; this._asking = ''; this._busy = false; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.xbin?.events.on((e) => { if (e.type === 'policies') this._load(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }
  refresh() { this._load(); }

  async _load() {
    try { this._state = await api('/workspace-policies'); } catch (e) { this._fail(e); }
  }

  // A switch's checkbox: turning partitionConsent on asks first.
  _toggle(sw, on, input) {
    if (on && sw.confirm) { input.checked = false; this._asking = sw.key; return; }
    this._set(sw, on);
  }

  async _set(sw, on) {
    this._busy = true; this._asking = '';
    try {
      this._state = await api('/workspace-policies', jbody({ [sw.key]: on }, 'PUT'));
      this._ok();
      this._emit('bx-admin-notice', sw.notice(on));
    } catch (e) { this._fail(e); this._load(); }
    this._busy = false;
  }

  _switch(sw) {
    const on = !!this._state[sw.key];
    return html`<div class="card" data-policy-card=${sw.key} data-state=${on ? 'on' : 'off'}>
      <label class="sw">
        <input type="checkbox" data-policy=${sw.key} .checked=${on} ?disabled=${this._busy || !!this._asking}
          @change=${(e) => this._toggle(sw, e.target.checked, e.target)}>
        <span><b>${sw.label}</b><span class="scope">applies to partitioned tiles</span></span>
      </label>
      <div class="line">${on ? sw.on : sw.off}</div>
      ${this._asking === sw.key ? html`<div class="ask" data-policy-confirm=${sw.key}>
        ${sw.confirm}
        <div class="row">
          <button class="go" ?disabled=${this._busy} @click=${() => this._set(sw, true)}>Turn on</button>
          <button @click=${() => { this._asking = ''; }}>cancel</button>
        </div>
      </div>` : nothing}
    </div>`;
  }

  render() {
    if (!this._state) return html`<div class="hint">loading…</div>`;
    return html`<div data-policies>
      <p class="hint">Workspace-wide rules for <b>partitioned tiles</b> — tiles where each person has their own
        data. They change nothing for other tiles. The grant ceiling ("workspace policy") is in
        <a href="#orgs" @click=${(e) => { e.preventDefault(); this._emit('bx-admin-tab', 'orgs'); }}>organisations</a>.</p>
      ${SWITCHES.map((sw) => this._switch(sw))}
    </div>`;
  }
}
customElements.define('bx-admin-policies', BxAdminPolicies);
