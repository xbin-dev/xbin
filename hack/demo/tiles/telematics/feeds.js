// feeds.js — the telematics feeds page: each provider and what dispatch
// will read from it. The tile is new: until an admin binds its network,
// every feed waits.
import { LitElement, html, css } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, baseCss } from './ui.js';

class TelematicsFeeds extends LitElement {
  static properties = { _feeds: { state: true }, _err: { state: true } };

  static styles = [scrollCss, baseCss, css`
    :host { overflow: auto; }
    .wrap { padding: 14px 18px 20px; max-width: 900px; }
    h1 { margin: 0; font-size: 17px; font-weight: 650; }
    .sub { color: var(--bx-muted); font-size: 12px; margin: 3px 0 14px; line-height: 1.45; }
    .list { border: 1px solid var(--bx-border); border-radius: 9px; overflow: hidden; }
    .f { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 3px 12px; padding: 10px 13px;
         border-bottom: 1px solid color-mix(in srgb, var(--bx-border) 55%, transparent); background: var(--bx-panel); }
    .f:last-child { border-bottom: 0; }
    .n { font-weight: 600; font-size: 13px; }
    .w { color: var(--bx-muted); font-size: 12px; }
    .h { font: 11.5px var(--bx-mono); color: var(--bx-muted); }
    .st { grid-row: 1 / span 2; grid-column: 2; align-self: center; text-align: right; }
    .t { font-size: 11px; color: var(--bx-muted); margin-top: 4px; }
    .chip { font-size: 11px; padding: 2px 9px; }
  `];

  constructor() { super(); this._feeds = null; }
  connectedCallback() { super.connectedCallback(); this._load(); }

  async _load() {
    try { this._feeds = (await getJSON('/feeds')).feeds; } catch (e) { this._err = e.message; }
  }

  render() {
    if (this._err) return html`<div class="err">${this._err}</div>`;
    if (!this._feeds) return html`<div class="empty">Loading…</div>`;
    return html`<div class="wrap">
      <h1>Telematics feeds</h1>
      <div class="sub">Van positions from our customers' telematics providers and weather alerts for every depot's county, for live dispatch.
        The feeds start once an admin decides where this tile's network goes.</div>
      <div class="list">${this._feeds.map((f) => html`<div class="f">
        <div><span class="n">${f.name}</span> <span class="w">· ${f.what}</span></div>
        <div class="st"><span class="chip warn"><span class="dot warn"></span>waiting for network</span><div class="t">${f.every}</div></div>
        <div class="h">${f.host}</div>
      </div>`)}</div>
    </div>`;
  }
}

customElements.define('telematics-feeds', TelematicsFeeds);
