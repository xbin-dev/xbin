/**
 * <bx-admin-branding> — the admin console's branding tab (D76): the
 * workspace's title and icon. The title replaces the word "workspace" in the
 * shell header and the browser tab; the icon replaces xbin's mark as the
 * favicon and the logo — on the workspace page and the sign-in pages. Both
 * are optional; clearing one brings xbin's own back. Saves go through
 * PUT /branding (admin); every open shell updates on the `branding` event.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import { base } from '../admin-css.js';
import { WithRouter } from '../shared.js';

const MAX_ICON = 256 * 1024;
const ICON_TYPES = 'image/svg+xml,image/png,image/jpeg,image/webp,image/x-icon';

export class BxAdminBranding extends WithRouter(LitElement) {
  static properties = {
    _brand: { state: true }, // {title, icon, hasIcon} from GET /branding (null until loaded)
    _title: { state: true }, // the title field's draft
    _err: { state: true },
  };
  static styles = [base, css`
    .brand { display: grid; gap: 16px; max-width: 560px; }
    .card { border: 1px solid var(--bx-border, #363c45); border-radius: 8px; padding: 12px 14px; background: var(--bx-panel, #23272e); }
    .card h4 { margin: 0 0 6px; }
    .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
    .row input[type=text] { flex: 1; min-width: 200px; }
    .preview { display: flex; align-items: center; gap: 10px; margin-top: 8px; }
    .preview img, .preview svg { width: 32px; height: 32px; object-fit: contain; border-radius: 6px; background: var(--bx-panel-2, #2b3038); padding: 2px; }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
  `];

  constructor() { super(); this._brand = null; this._title = ''; this._err = ''; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._off = window.xbin?.events.on((e) => { if (e.type === 'branding') this._load(); });
  }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _load() {
    try { this._brand = await api('/branding'); this._title = this._brand.title || ''; } catch (e) { this._fail(e); }
  }

  async _put(patch, notice) {
    try {
      this._brand = await api('/branding', jbody(patch, 'PUT'));
      this._title = this._brand.title || '';
      this._ok(); this._emit('bx-admin-notice', notice);
    } catch (e) { this._fail(e); }
  }

  _pickIcon(e) {
    const f = e.target.files?.[0];
    e.target.value = '';
    if (!f) return;
    if (f.size > MAX_ICON) { this._fail(new Error(`that file is ${Math.round(f.size / 1024)} KiB; the limit is ${MAX_ICON / 1024} KiB`)); return; }
    const rd = new FileReader();
    rd.onload = () => this._put({ icon: String(rd.result) }, 'workspace icon set');
    rd.onerror = () => this._fail(new Error('could not read that file'));
    rd.readAsDataURL(f);
  }

  render() {
    const b = this._brand;
    if (!b) return html`<div class="hint">loading…</div>`;
    return html`<div class="brand">
      <div class="card">
        <h4>Workspace title</h4>
        <div class="hint">Replaces the word "workspace" in the shell header and the browser tab ("&lt;title&gt; · xbin"), and names the sign-in page. Up to 64 characters.</div>
        <div class="row">
          <input type="text" maxlength="64" placeholder="e.g. Acme Ops" .value=${this._title} @input=${(e) => { this._title = e.target.value; }}
            @keydown=${(e) => { if (e.key === 'Enter') this._put({ title: this._title }, 'workspace title saved'); }}>
          <button @click=${() => this._put({ title: this._title }, 'workspace title saved')}>Save</button>
          ${b.title ? html`<button @click=${() => this._put({ title: '' }, 'workspace title cleared — xbin\'s own again')}>Clear</button>` : nothing}
        </div>
      </div>
      <div class="card">
        <h4>Workspace icon</h4>
        <div class="hint">Replaces xbin's mark as the favicon and the header logo, here and on the sign-in page. SVG, PNG, JPEG, WebP or ICO, up to 256 KiB — a square works best.</div>
        <div class="preview">
          ${b.hasIcon ? html`<img src=${b.icon} alt="">` : html`<svg viewBox="0 0 64 64" aria-hidden="true"><path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="var(--bx-accent,#f5a623)"></path><path d="M21 21 43 43M43 21 21 43" stroke="#23272e" stroke-width="9"></path></svg>`}
          <span class="hint">${b.hasIcon ? 'the custom icon' : "xbin's own mark"}</span>
        </div>
        <div class="row">
          <input type="file" accept=${ICON_TYPES} hidden @change=${this._pickIcon}>
          <button @click=${() => this.renderRoot.querySelector('input[type=file]').click()}>Upload an image…</button>
          ${b.hasIcon ? html`<button @click=${() => this._put({ icon: '' }, 'workspace icon removed — xbin\'s own again')}>Remove</button>` : nothing}
        </div>
      </div>
    </div>`;
  }
}
customElements.define('bx-admin-branding', BxAdminBranding);
