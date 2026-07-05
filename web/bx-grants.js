/**
 * <bx-grants> — the owner's grant-approval panel (plans/auth.md §3).
 * Shows pending `uses` requests with the callee's role descriptions and
 * one-click approve; lists and revokes existing grants. Renders nothing when
 * there is nothing to decide, so it can sit permanently in the root page.
 */
import { LitElement, html, css, nothing } from 'lit';
import { onEvent } from '/vendor/events-socket.js';

export class BxGrants extends LitElement {
  static properties = {
    _grants: { state: true },
    _pending: { state: true },
    _roleDocs: { state: true },
    _showAll: { state: true },
  };

  static styles = css`
    :host {
      display: block;
      font: var(--bx-font, 13px/1.45 system-ui, sans-serif);
      color: var(--bx-text, #33414e);
    }
    .panel {
      background: var(--bx-panel, #fff);
      border: 1px solid var(--bx-border, #e4e8ed);
      border-left: 3px solid var(--bx-amber, #f2a71b);
      border-radius: var(--bx-radius, 6px);
      box-shadow: var(--bx-shadow, 0 1px 2px rgba(16,24,40,.05));
      padding: 8px 12px;
    }
    h4 {
      margin: 0 0 4px; font-size: 10.5px; font-weight: 600;
      letter-spacing: .08em; text-transform: uppercase;
      color: var(--bx-muted, #8794a1);
    }
    .row { display: flex; align-items: center; gap: 8px; padding: 3px 0; }
    .who { font-family: var(--bx-mono, ui-monospace, monospace); font-size: 12px; }
    .role { color: var(--bx-accent, #1e88e5); font-size: 12px; font-weight: 600; }
    .desc { color: var(--bx-muted, #8794a1); font-size: 12px; flex: 1;
            overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    button {
      background: var(--bx-green, #43a047); color: #fff; border: 0;
      border-radius: 5px; padding: 2px 10px; cursor: pointer;
      font: inherit; font-size: 12px; font-weight: 600;
    }
    button.rm {
      background: var(--bx-panel, #fff); color: var(--bx-red, #e5484d);
      border: 1px solid color-mix(in srgb, var(--bx-red, #e5484d) 40%, transparent);
      font-weight: 500;
    }
    a { color: var(--bx-muted, #8794a1); font-size: 12px; cursor: pointer; }
    a:hover { color: var(--bx-accent, #1e88e5); }
  `;

  constructor() {
    super();
    this._grants = [];
    this._pending = [];
    this._roleDocs = {};
    this._showAll = false;
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => { if (e.type === 'grants' || e.type === 'reload') this._load(); });
    this._load();
  }

  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); }

  async _load() {
    try {
      const r = await fetch('/api/xbin/grants');
      if (!r.ok) return;
      const d = await r.json();
      this._grants = d.grants ?? [];
      this._pending = d.pending ?? [];
      // Pull role descriptions for pending component targets.
      for (const p of this._pending) {
        if (p.target.startsWith('res:') || this._roleDocs[p.target] !== undefined) continue;
        const cr = await fetch(`/api/xbin/components/${p.target}`);
        this._roleDocs = {
          ...this._roleDocs,
          [p.target]: cr.ok ? (await cr.json()).component?.roles ?? {} : {},
        };
      }
    } catch { /* xbind restart etc.; next event reloads */ }
  }

  async _approve(g) {
    await fetch('/api/xbin/grants', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(g),
    });
    this._load();
  }

  async _revoke(g) {
    await fetch('/api/xbin/grants', {
      method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(g),
    });
    this._load();
  }

  render() {
    if (this._pending.length === 0 && !this._showAll) {
      return this._grants.length === 0 ? nothing
        : html`<a @click=${() => { this._showAll = true; }}>${this._grants.length} grant(s) active</a>`;
    }
    return html`<div class="panel">
      ${this._pending.length > 0 ? html`
        <h4>pending access requests</h4>
        ${this._pending.map((p) => html`
          <div class="row">
            <span class="who">${p.from} → ${p.target}</span>
            <span class="role">${p.role}</span>
            <span class="desc">${this._roleDocs[p.target]?.[p.role] ?? ''}</span>
            <button @click=${() => this._approve(p)}>approve</button>
          </div>`)}` : nothing}
      ${this._showAll ? html`
        <h4 style="margin-top:.6rem">active grants</h4>
        ${this._grants.map((g) => html`
          <div class="row">
            <span class="who">${g.from} → ${g.target}</span>
            <span class="role">${g.role}</span>
            <span class="desc"></span>
            <button class="rm" @click=${() => this._revoke(g)}>revoke</button>
          </div>`)}
        <a @click=${() => { this._showAll = false; }}>hide</a>` : html`
        <a @click=${() => { this._showAll = true; }}>show all grants</a>`}
    </div>`;
  }
}

customElements.define('bx-grants', BxGrants);
