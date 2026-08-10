// <xb-lightbox> — zoom dialog for every <figure class="shot"> on the page.
// Collects the figures when it connects (they're plain light-DOM markup),
// opens on click, ‹/› or arrow keys browse, backdrop click or Esc closes.
import { LitElement, html, css } from 'lit';

class XbLightbox extends LitElement {
  static properties = { _cur: { state: true } };

  static styles = css`
    dialog {
      border: 1px solid #454d59; padding: 10px; background: #1b1e24;
      max-width: min(96vw, 1560px); box-shadow: 0 30px 90px rgba(0,0,0,.6);
      color: #868f9a;
    }
    dialog::backdrop { background: rgba(10,11,14,.78); backdrop-filter: blur(3px); }
    .row { display: flex; align-items: center; gap: 10px; }
    img { display: block; max-width: 100%; max-height: 80vh; min-width: 0; }
    .nav { flex: none; width: 34px; height: 34px; border: 1px solid #454d59;
      background: #23272e; color: #868f9a; font: 18px/1 sans-serif; cursor: pointer;
      display: grid; place-items: center; padding: 0 0 2px;
      clip-path: polygon(0 0, calc(100% - 8px) 0, 100% 8px, 100% 100%, 8px 100%, 0 calc(100% - 8px));
      transition: color .15s, border-color .15s; }
    .nav:hover { color: #d4d9e0; border-color: #868f9a; }
    .cap { display: flex; gap: 14px; align-items: baseline; padding: 10px 4px 2px;
      font-size: 13px; line-height: 1.5; }
    .txt { flex: 1; }
    .cnt { font: 11px ui-monospace, Menlo, Consolas, monospace; color: #5c6672; white-space: nowrap; }
  `;

  constructor() { super(); this._cur = 0; this._shots = []; }

  connectedCallback() {
    super.connectedCallback();
    this._shots = [...document.querySelectorAll('.shot')].map(f => {
      const img = f.querySelector('img');
      const fc = f.querySelector('figcaption');
      if (!fc) return { src: img.src, alt: img.alt, cap: img.alt };
      const sn = fc.querySelector('.sn');
      const step = sn ? sn.textContent.trim() : '';
      const txt = fc.textContent.replace(sn ? sn.textContent : '', '').trim();
      return { src: img.src, alt: img.alt, cap: (step && step !== '●' ? step + ' · ' : '') + txt };
    });
    this._clicks = this._shots.map((s, i) => {
      const img = document.querySelectorAll('.shot img')[i];
      const h = () => { this._show(i); this._dlg.showModal(); };
      img.addEventListener('click', h);
      return [img, h];
    });
  }

  disconnectedCallback() {
    this._clicks?.forEach(([img, h]) => img.removeEventListener('click', h));
    super.disconnectedCallback();
  }

  get _dlg() { return this.shadowRoot.querySelector('dialog'); }

  _show(i) { this._cur = (i + this._shots.length) % this._shots.length; }

  _onKey(e) {
    if (e.key === 'ArrowLeft') { e.preventDefault(); this._show(this._cur - 1); }
    if (e.key === 'ArrowRight') { e.preventDefault(); this._show(this._cur + 1); }
  }

  _onClick(e) { if (e.target === this._dlg) this._dlg.close(); }

  render() {
    const s = this._shots[this._cur] || { src: '', alt: '', cap: '' };
    return html`
    <dialog aria-label="screenshot zoom" @keydown=${this._onKey} @click=${this._onClick}>
      <div class="row">
        <button type="button" class="nav" aria-label="previous screenshot"
          @click=${() => this._show(this._cur - 1)}>‹</button>
        <img src=${s.src} alt=${s.alt}>
        <button type="button" class="nav" aria-label="next screenshot"
          @click=${() => this._show(this._cur + 1)}>›</button>
      </div>
      <div class="cap"><span class="txt">${s.cap}</span>
        <span class="cnt">${this._cur + 1} / ${this._shots.length}</span></div>
    </dialog>`;
  }
}

customElements.define('xb-lightbox', XbLightbox);
