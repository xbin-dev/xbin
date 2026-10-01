/**
 * <bx-admin-terminals> — the admin console's "terminals" tab under
 * workspace: base auto-update (D173). On (the default), a tile's terminal
 * layer built on an older base image moves to the current base at its next
 * session start — its apt installs and /etc changes are reset, files and
 * $HOME kept; a running terminal keeps its base until it ends. Off, the
 * terminal window offers the base update instead. Saves go through
 * PUT /workspace-settings (admin); docs/overview/09-terminals.md.
 */
import { LitElement, html, css } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';

export class BxAdminTerminals extends WithRouter(LitElement) {
  static properties = {
    _state: { state: true }, // {baseAutoUpdate, error?} from GET /workspace-settings (null until loaded)
    _busy: { state: true },
    _err: { state: true },
  };
  static styles = [base, css`
    .card { max-width: 560px; border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 12px 14px; background: var(--bx-panel, #23272e); }
    .card h4 { margin: 0 0 6px; }
    label.sw { display: flex; gap: 8px; align-items: flex-start; font-size: 12px; margin-top: 8px; }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
    .state { margin-top: 8px; font-size: 12px; }
    .bad { color: var(--bx-red, #ef5350); font-size: 12px; margin-top: 8px; }
  `];

  constructor() { super(); this._state = null; this._busy = false; }

  connectedCallback() { super.connectedCallback(); this._load(); }

  async _load() {
    try { this._state = await api('/workspace-settings'); } catch (e) { this._fail(e); }
  }

  async _set(on) {
    this._busy = true;
    try {
      this._state = await api('/workspace-settings', jbody({ baseAutoUpdate: on }, 'PUT'));
      this._ok();
      this._emit('bx-admin-notice', on ? 'base auto-update is on' : 'base auto-update is off — terminal windows offer the update');
    } catch (e) { this._fail(e); this._load(); }
    this._busy = false;
  }

  render() {
    const s = this._state;
    if (!s) return html`<div class="hint">loading…</div>`;
    const on = !!s.baseAutoUpdate;
    return html`<div class="card" data-base-auto-update=${on ? 'on' : 'off'}>
      <h4>Base image updates</h4>
      <div class="hint">A tile's terminals keep the system changes made in them (apt installs, /etc) on top of
        the base image they were built on. When xbin ships a newer base — a newer Go, say — a terminal moves to it
        only by resetting those changes; files and $HOME are kept.</div>
      <label class="sw">
        <input type="checkbox" .checked=${on} ?disabled=${this._busy} @change=${(e) => this._set(e.target.checked)}>
        <span><b>Move terminals to a new base image automatically</b>, at their next start. A running terminal keeps
          its base until it ends.</span>
      </label>
      <div class="state">
        <span class="dot" style="background:${on ? 'var(--bx-green, #4caf50)' : 'var(--bx-amber, #f2a71b)'}"></span>
        ${on ? 'on — a terminal on an older base starts on the new one and says so'
          : 'off — terminals stay on their base; the window offers ⬆ base update'}
      </div>
      ${s.error ? html`<div class="bad">${s.error} — base auto-update is off until the file is fixed</div>` : ''}
    </div>`;
  }
}
customElements.define('bx-admin-terminals', BxAdminTerminals);
