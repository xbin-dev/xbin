// <xb-copy cmd="…"> — a chamfered copy-to-clipboard button used next to the
// install one-liners. Confirms inline, then reverts.
import { LitElement, html, css } from 'lit';

class XbCopy extends LitElement {
  static properties = { cmd: { type: String }, _done: { state: true } };

  static styles = css`
    button {
      font: 12px ui-monospace, Menlo, Consolas, monospace; cursor: pointer;
      background: #23272e; color: #868f9a; border: none; padding: 6px 11px;
      clip-path: polygon(0 0, calc(100% - 6px) 0, 100% 6px, 100% 100%, 6px 100%, 0 calc(100% - 6px));
      outline: 1px solid #454d59; outline-offset: -1px;
      transition: background .15s, color .15s;
      white-space: nowrap;
    }
    button:hover { color: #d4d9e0; background: #2b3038; }
    button.done { background: #f5a623; color: #231a06; outline-color: #f5a623; }
    button:focus-visible { outline: 2px solid #f5a623; outline-offset: 2px; }
  `;

  constructor() { super(); this.cmd = ''; this._done = false; }

  async _copy() {
    try {
      await navigator.clipboard.writeText(this.cmd);
      this._done = true;
      clearTimeout(this._t);
      this._t = setTimeout(() => { this._done = false; }, 1400);
    } catch { /* clipboard unavailable — leave the label alone */ }
  }

  render() {
    return html`<button type="button" class=${this._done ? 'done' : ''}
      @click=${this._copy}>${this._done ? 'copied ✓' : 'copy'}</button>`;
  }
}

customElements.define('xb-copy', XbCopy);
