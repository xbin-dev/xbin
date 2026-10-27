/**
 * <bx-prs component="apps/x"> — the change-proposal ("code PR") panel for the
 * terminal pop-up (docs/bx.md §code pr): the list and one proposal side by
 * side. Since D202 it is pr-panels.js's two elements, <bx-pr-list> and
 * <bx-pr-view> (the tile window shows them as a side panel and a tab); this
 * element stays for every page that embeds it. It opens the newest open
 * proposal first, as it always did. Merged and Reject ask for their note in
 * a line of the view (no browser prompt()); applying is still the terminal's
 * `bx code pr fetch N | git am --3way`, shown in the view.
 */
import { LitElement, html, css } from 'lit';
import '/vendor/pr-panels.js';

export class BxPrs extends LitElement {
  static properties = {
    component: { type: String },
    _sel: { state: true },  // the open proposal's number
  };

  static styles = css`
    :host { display: flex; height: 100%; min-height: 0; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
      color: var(--bx-text, #E9EAF0); background: var(--bx-panel, #1F2028); }
    .side { width: 230px; flex: none; display: flex; flex-direction: column; border-right: 1px solid var(--bx-border, #33353F); min-height: 0; }
    .side bx-pr-list, .main bx-pr-view { flex: 1; min-height: 0; }
    .main { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; }
    .muted { color: var(--bx-muted, #A3A6B6); padding: 12px; display: block; }
  `;

  constructor() { super(); this._sel = null; }
  updated(ch) { if (ch.has('component') && ch.get('component') !== undefined) this._sel = null; }

  // the list's events are for this element: the tile window, when it embeds
  // the list itself, listens for the same names
  _open(e) {
    e.stopPropagation();
    const n = Number(String(e.detail?.ref || '').replace(/^pr:/, ''));
    if (n) this._sel = n;
  }
  _count(e) {
    e.stopPropagation();
    if (this._sel == null && e.detail?.newest != null) this._sel = e.detail.newest;
  }

  render() {
    return html`
      <div class="side"><bx-pr-list target=${this.component ?? ''} .active=${this._sel}
        @bx-open-tab=${(e) => this._open(e)} @bx-pr-count=${(e) => this._count(e)}></bx-pr-list></div>
      <div class="main">${this._sel == null
        ? html`<span class="muted">select a proposal — or open one from another tile's terminal with: bx code pr ${this.component} --title … *.patch</span>`
        : html`<bx-pr-view target=${this.component} n=${this._sel}></bx-pr-view>`}</div>`;
  }
}
customElements.define('bx-prs', BxPrs);
