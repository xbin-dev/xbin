/**
 * <bx-limits-page> — xbind's tile limits page, /xbin/limits[?tile=<path>]
 * (D203, docs/isolation.md §Resource limits): the surface that is there at
 * all times — it ships with xbind, so a workspace whose shell and admin
 * console predate tile limits still has one, and the alerts about a cap
 * name it. Served as it is (no HTML transform) and only top-level: framed,
 * it renders nothing of the person's.
 *
 * With ?tile=: that tile's limits (<bx-limits view="section">) — the limit
 * in force, the decisions, the manifest's ask, the hit, set…, revert. Without:
 * every tile the person may see (<bx-limits-table>), and the per-org ceilings
 * (workspace admins edit them; an org admin reads their org's). Everything
 * is read and written with the person's own session; xbind judges every act.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import '/vendor/bx-limits.js';
import '/vendor/limits-table.js';

const pageCss = css`
  :host { display: block; color: var(--bx-text, #E9EAF0); font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  main { max-width: 1080px; margin: 0 auto; padding: 24px 16px 64px; box-sizing: border-box; }
  main.one { max-width: 760px; }
  header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
  header .grow { flex: 1; }
  h1 { font: var(--bx-font-heading, 600 20px/26px "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-heading, -0.01em); margin: 0; display: flex; align-items: center; gap: 8px; overflow-wrap: anywhere; }
  .lead { color: var(--bx-muted, #A3A6B6); max-width: 72ch; margin: 8px 0 16px; }
  a { color: var(--bx-link, #8C9BFF); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .small { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  .banner { border: 1px solid var(--bx-danger, #FF7A7A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 12px 0; background: var(--bx-danger-bg, #3A2B32); }
  .card { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); padding: 12px; }
`;

export class BxLimitsPage extends LitElement {
  static styles = pageCss;
  constructor() {
    super();
    this._framed = false;
    try { this._framed = window.top !== window.self; } catch { this._framed = true; }
    this._tile = new URLSearchParams(location.search).get('tile') || '';
  }
  render() {
    if (this._framed) {
      return html`<main><div class="banner" role="alert"><bx-icon name="error"></bx-icon> This page shows tile limits only when it is opened on its own, never inside another page.
        Open <code>/xbin/limits</code> in a tab of its own.</div></main>`;
    }
    const t = this._tile;
    return html`<main class=${t ? 'one' : ''}>
      <header>
        <h1><bx-icon name="chart"></bx-icon>${t ? html`Limits of <span class="mono">${t}</span>` : 'Tile limits'}</h1>
        <span class="grow"></span>
        ${t ? html`<a class="small" href="/xbin/limits">all tiles</a>` : nothing}
        <a class="small" href="/"><bx-icon name="arrow-left"></bx-icon> workspace</a>
      </header>
      <p class="lead">A tile runs within limits — its backend’s memory, its scope’s data disk, its VM’s size, its sandboxes —
        the workspace’s defaults unless an approver raised them. A tile’s manifest may ask for more (<code>"limits"</code>), and a
        tile at its limit (out of memory, out of disk) can be raised here by +25, +50 or +100 % as long as the workspace has the room
        (<a href="/docs/isolation.md" target="_blank" rel="noopener">how limits work</a>; <code>bx limits</code> does the same).</p>
      ${t ? html`<div class="card"><bx-limits tile=${t} view="section"></bx-limits></div>`
        : html`<bx-limits-table ceilings></bx-limits-table>`}
    </main>`;
  }
  testApi() { return { framed: () => this._framed, tile: () => this._tile }; }
}
customElements.define('bx-limits-page', BxLimitsPage);
