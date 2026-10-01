/**
 * <bx-admin-settings> — the admin console's workspace → settings tab (D180):
 * every workspace setting, grouped by topic (settings-view.js has the
 * words, the groups and how an older xbind is told apart).
 *  - Terminals: base auto-update (D175). On (the default), a tile's
 *    terminal layer built on an older base image moves to the current base
 *    at its next session start — everything outside the workspace files and
 *    $HOME is reset; a running terminal keeps its base until it ends. Off,
 *    the terminal window offers the base update instead.
 *  - Partitioned tiles (PD-55), both off by default: ask each person before
 *    another partitioned tile uses their data — turning it on asks first,
 *    showing the edges it would start asking about (GET /partitions/edges,
 *    the people who used each in the last 30 days; an xbind without the
 *    route: nothing shown) — and credential resets wait for the person.
 * Reads GET /workspace-settings, saves with PUT (admin), one key at a time;
 * every open console reads again on the `workspace-settings` event. Against
 * an xbind before D180 the partitioned tiles' switches go through
 * /workspace-policies and its `policies` event; a group the xbind lacks says
 * so. `only` limits the tab to one group: <bx-admin-terminals> and
 * <bx-admin-policies> (an admin.js from before this tab) are that.
 * docs/overview/09-terminals.md, docs/partitions.md.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as api, jbody, sandboxed } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';
import { GROUPS, settingsOf, loadSettings, saveRequest, reloadOn } from './settings-view.js';

// call(path): GET /api/xbin<path> → {status, ok, body}, never throwing on a
// status (the tab tells an older xbind by its 404s).
async function call(path) {
  const f = sandboxed() && window.xbin?.fetch ? window.xbin.fetch : fetch;
  const r = await f('/api/xbin' + path);
  const text = await r.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = text; }
  return { status: r.status, ok: r.ok, body };
}

export class BxAdminSettings extends WithRouter(LitElement) {
  static properties = {
    only: { type: String },     // one group's id ('' every group)
    _state: { state: true },    // loadSettings() (null until loaded)
    _asking: { state: true },   // the key whose turn-on awaits confirmation
    _edges: { state: true },    // GET /partitions/edges' rows: null loading, undefined unavailable
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, css`
    section { max-width: 620px; margin-bottom: 18px; }
    section > h3 { margin: 0 0 4px; font-size: 13px; }
    .card { border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 12px 14px;
            background: var(--bx-panel, #23272e); margin: 8px 0 10px; }
    .card h4 { margin: 0 0 6px; }
    label.sw { display: flex; gap: 8px; align-items: flex-start; font-size: 12px; }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
    .line, .state { font-size: 12px; margin: 6px 0 0 24px; }
    .bad { color: var(--bx-red, #ef5350); font-size: 12px; margin: 8px 0 0 24px; }
    .scope { display: inline-block; margin-left: 6px; padding: 0 6px; border-radius: 8px; font-size: 11px; font-weight: 400;
             color: var(--bx-muted, #868f9a); border: 1px solid var(--bx-border, #363c45); }
    .ask { margin: 8px 0 0 24px; padding: 8px 10px; border-radius: 6px; font-size: 12px;
           background: color-mix(in srgb, var(--bx-amber, #f2a71b) 14%, transparent); }
    .ask .row { display: flex; gap: 6px; margin-top: 6px; }
    .ask ul { margin: 4px 0 0; padding-left: 18px; }
    .ask code { font-size: 11px; }
  `];

  constructor() { super(); this.only = ''; this._state = null; this._asking = ''; this._busy = false; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.xbin?.events.on((e) => { if (reloadOn(this._state, e)) this._load(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }
  refresh() { this._load(); }

  async _load() {
    try { this._state = await loadSettings(call); } catch (e) { this._fail(e); }
  }

  // A switch's checkbox: one with a confirmation asks before turning on.
  _toggle(s, on, input) {
    if (on && s.confirm) {
      input.checked = false; this._asking = s.key;
      if (s.key === 'partitionConsent') this._loadEdges();
      return;
    }
    this._set(s, on);
  }

  // The edges between partitioned tiles that turning consent on would start
  // asking about: granted now, or used in the last 30 days (06 §12.3).
  async _loadEdges() {
    this._edges = null;
    try {
      const r = await api('/partitions/edges?days=30');
      this._edges = (r.edges ?? []).filter((e) => e.granted || e.people > 0);
    } catch { this._edges = undefined; }
  }

  _edgesPreview() {
    const edges = this._edges;
    if (edges === undefined) return nothing;
    if (edges === null) return html`<div class="hint" data-setting-edges="loading">counting the edges…</div>`;
    if (edges.length === 0) {
      return html`<div data-setting-edges="none">No partitioned tile uses another's people's data: nobody will be asked.</div>`;
    }
    return html`<div data-setting-edges=${edges.length}>In the last 30 days:
      <ul>${edges.map((e) => html`<li data-edge=${e.from + '→' + e.to}><code>${e.from}</code> → <code>${e.to}</code>:
        ${e.people} ${e.people === 1 ? 'person' : 'people'}${e.consented ? `, ${e.consented} already allowed it` : ''}${e.granted ? '' : ' (no grant now)'}</li>`)}</ul>
    </div>`;
  }

  async _set(s, on) {
    this._busy = true; this._asking = '';
    const { path, body } = saveRequest(this._state, s.key, on);
    try {
      await api(path, jbody(body, 'PUT'));
      this._ok();
      this._emit('bx-admin-notice', s.notice(on));
    } catch (e) { this._fail(e); }
    await this._load();
    this._busy = false;
  }

  _card(s) {
    const on = !!this._state.values[s.key];
    const why = this._state.errors[s.key];
    return html`<div class="card" data-setting-card=${s.key} data-state=${on ? 'on' : 'off'}>
      ${s.key === 'baseAutoUpdate' ? html`<h4>Base image updates</h4>
        <div class="hint">A tile's terminal keeps everything outside the workspace files and $HOME — installed packages,
          /etc, /var, /opt…, a VM terminal's disk — in a layer on top of the base image it was built on. When xbin ships a
          newer base (a newer Go, say), a terminal moves to it only by resetting that layer, for good.</div>` : nothing}
      <label class="sw" style=${s.key === 'baseAutoUpdate' ? 'margin-top:8px' : ''}>
        <input type="checkbox" data-setting=${s.key} .checked=${on} ?disabled=${this._busy || !!this._asking}
          @change=${(e) => this._toggle(s, e.target.checked, e.target)}>
        <span><b>${s.label}</b>${s.detail ? ` ${s.detail}` : nothing}${s.group === 'partitions' ? html`<span class="scope">applies to partitioned tiles</span>` : nothing}</span>
      </label>
      ${s.key === 'baseAutoUpdate'
        ? html`<div class="state"><span class="dot" style="background:${on ? 'var(--bx-green, #4caf50)' : 'var(--bx-amber, #f2a71b)'}"></span>${on ? s.on : s.off}</div>`
        : html`<div class="line">${on ? s.on : s.off}</div>`}
      ${why ? html`<div class="bad" data-setting-error=${s.key}>${why} — ${s.unreadable}</div>` : nothing}
      ${this._asking === s.key ? html`<div class="ask" data-setting-confirm=${s.key}>
        ${s.confirm}
        ${s.key === 'partitionConsent' ? this._edgesPreview() : nothing}
        <div class="row">
          <button class="go" ?disabled=${this._busy} @click=${() => this._set(s, true)}>Turn on</button>
          <button @click=${() => { this._asking = ''; }}>cancel</button>
        </div>
      </div>` : nothing}
    </div>`;
  }

  _group(g) {
    const st = this._state.groups[g.id] ?? { state: 'absent' };
    return html`<section data-group=${g.id} data-group-state=${st.state}>
      <h3>${g.label}</h3>
      ${g.id === 'partitions' ? html`<p class="hint">Workspace-wide rules for <b>partitioned tiles</b> — tiles where each
        person has their own data. They change nothing for other tiles. The grant ceiling ("workspace policy") is in
        <a href="#orgs" @click=${(e) => { e.preventDefault(); this._emit('bx-admin-tab', 'orgs'); }}>organisations</a>.</p>` : nothing}
      ${st.state === 'ok' ? settingsOf(g.id).map((s) => this._card(s))
        : st.state === 'absent' ? html`<div class="hint" data-group-absent>${st.why ?? 'not on this xbind'}</div>`
          : html`<div class="bad">${st.why}</div>`}
    </section>`;
  }

  render() {
    if (!this._state) return html`<div class="hint">loading…</div>`;
    return html`<div data-settings>
      ${GROUPS.filter((g) => !this.only || g.id === this.only).map((g) => this._group(g))}
    </div>`;
  }
}
customElements.define('bx-admin-settings', BxAdminSettings);
