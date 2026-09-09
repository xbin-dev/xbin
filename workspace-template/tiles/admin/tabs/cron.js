/**
 * <bx-admin-cron> — the admin console's cron tab: every scheduled job across
 * the workspace, deletable. A tab element of tiles/admin (see admin.js).
 */
import { LitElement, html, nothing } from 'lit';
import { xbinApi as api } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';

export class BxAdminCron extends WithRouter(LitElement) {
  static properties = {
    cron: { attribute: false }, // [{name, component, schedule, path, role}]
    _err: { state: true }, // the last refusal (reported to the router's slot)
  };
  static styles = [base];

  // fail(e) reports a refusal to the router's global slot; ok() clears it.

  async _delCron(j) {
    if (!confirm(`Delete cron job ${j.name} (${j.component})?`)) return;
    await api(`/cron/jobs/${encodeURIComponent(j.name)}?component=${encodeURIComponent(j.component)}`,
      { method: 'DELETE' });
    this._emit('bx-admin-refresh');
  }

  render() {
    const jobs = this.cron ?? [];
    return html`
      ${jobs.length === 0 ? html`<span class="muted">no scheduled jobs.</span>` : html`
        <table>
          <tr><th>name</th><th>component</th><th>schedule</th><th>path</th><th>role</th><th></th></tr>
          ${jobs.map((j) => html`<tr>
            <td class="mono">${j.name}</td>
            <td class="mono">${j.component}</td>
            <td class="mono">${j.schedule}</td>
            <td class="mono">${j.path}</td>
            <td><span class="pill">${j.role}</span></td>
            <td style="text-align:right"><button class="act rm" @click=${() => this._delCron(j)}>delete</button></td>
          </tr>`)}
        </table>`}`;
  }
}

customElements.define('bx-admin-cron', BxAdminCron);
