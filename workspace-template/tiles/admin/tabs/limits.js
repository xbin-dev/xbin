/**
 * <bx-admin-limits> — the admin console's runtime → limits tab (D203,
 * docs/isolation.md §Resource limits): every tile's resource limits — the
 * limit in force, the default, the decisions, a manifest's pending ask, a
 * cap met — with the filters (raised, pending, at its limit, org) and the
 * inline set…, revert and approve of /vendor/limits-table.js, read and
 * written through xbin.fetch (the console's identity: an admin driving it).
 * #limits/<tile> (the alert bar's "raise…" link) narrows it to that tile.
 * The per-org ceilings are on each organisation's page (tabs/orgs.js).
 */
import { LitElement, html } from 'lit';
import '/vendor/limits-table.js';
import { base } from '../admin-css.js';

const xfetch = (u, o) => window.xbin.fetch(u, o);

export class BxAdminLimits extends LitElement {
  static properties = { sub: { attribute: false } }; // the router's drill-in: a tile path, or ''
  static styles = [base];
  render() {
    return html`<p class="muted hint">A tile runs within the workspace’s defaults unless an approver raised them. Raises and a
      manifest’s approved ask apply live for memory and disk; a VM’s size at its next start. Org admins approve up to their
      org’s ceiling (user management → organisations → an org); <a class="link" href="/xbin/limits" target="_blank" rel="noopener">/xbin/limits</a>
      is the same list outside the console.</p>
      <bx-limits-table .fetcher=${xfetch} .tile=${this.sub || ''}></bx-limits-table>`;
  }
}
customElements.define('bx-admin-limits', BxAdminLimits);
