/**
 * <bx-grants> — the owner's grant-approval panel (docs/auth.md §Roles and grants).
 * Shows pending `uses` requests with the callee's role descriptions and
 * one-click approve; lists and revokes existing grants. Renders nothing when
 * there is nothing to decide, so it can sit permanently in the root page.
 * A pending request is a sign plate (D184, product-ui §8): an ink header
 * bar over label and value rows, Approve as the primary button.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { onEvent } from '/vendor/events-socket.js';
import { capInfo } from '/vendor/bx-allow.js';
import { grantArrow } from '/vendor/bx-grant-row.js';
import '/vendor/bx-icons.js';

// a link-like control that opens or closes a list: focusable, Enter and Space work
const toggle = (label, fn) => html`<a role="button" tabindex="0" @click=${fn}
  @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fn(); } }}>${label}</a>`;

// Reserved targets have no component to look role docs up on.
const reservedTarget = (t) => /^(res:|cap:|gpu:|net:|xbin$|xbin:|code$|code:)/.test(t);

export class BxGrants extends LitElement {
  static properties = {
    _grants: { state: true },
    _pending: { state: true },
    _roleDocs: { state: true },
    _showAll: { state: true },
    _scope: { state: true },   // "org" | "mine" on the non-admin filtered view (D26/D33)
    _err: { state: true },     // last approve/revoke failure
  };

  static styles = [scrollCss, css`
    :host {
      display: block;
      font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
      color: var(--bx-text, #E9EAF0);
    }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px);
      box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    /* a card: a border, no shadow (D184) */
    .panel {
      background: var(--bx-panel, #1F2028);
      border: 1px solid var(--bx-border, #33353F);
      border-radius: var(--bx-radius, 2px);
      padding: 8px var(--bx-pad, 12px) 12px;
    }
    h4 {
      margin: 4px 0 8px; color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    }
    /* a grant request: the sign plate — an ink header bar (it inverts in
       Night), label and value rows separated by hairlines */
    .plate { margin: 0 0 8px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      background: var(--bx-panel, #1F2028); overflow: hidden; }
    .ph { display: flex; align-items: center; gap: 8px; padding: 4px 12px; background: var(--bx-text, #E9EAF0); color: var(--bx-panel, #1F2028);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      font-family: var(--bx-display, "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
      letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    .ph .dir { margin-left: auto; color: inherit; border-color: currentColor; }
    .pr { display: grid; grid-template-columns: 88px minmax(0, 1fr); gap: 12px; align-items: baseline;
      padding: 5px 12px; border-top: 1px solid var(--bx-border, #33353F); }
    .ph + .pr { border-top: 0; }
    .pr .k { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
      letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    .pacts { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--bx-border, #33353F); }
    .row { display: flex; align-items: center; gap: 8px; min-height: var(--bx-row, 28px); border-top: 1px solid var(--bx-border, #33353F); }
    .who { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); overflow-wrap: anywhere; }
    .role { font-weight: 600; }
    .desc { color: var(--bx-muted, #A3A6B6); flex: 1; min-width: 0; }
    .row .desc { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    /* buttons (product-ui §6): secondary by default, Approve the primary */
    button {
      box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer;
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
      font: inherit; font-weight: 600;
    }
    button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
    button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
    button.primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    /* a row's revoke: a quiet button in the danger colour (R1: row actions are quiet) */
    button.rm { background: transparent; border-color: transparent; color: var(--bx-danger, #FF7A7A); }
    button.rm:hover:not(:disabled) { background: transparent; border-color: var(--bx-danger, #FF7A7A); }
    button:disabled { opacity: .5; cursor: default; }
    a { display: inline-block; color: var(--bx-muted, #A3A6B6); cursor: pointer; text-decoration: none; }
    a:hover { color: var(--bx-text, #E9EAF0); text-decoration: underline; }
    .panel > a { margin-top: 8px; }
    .dir {
      display: inline-flex; align-items: center; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    }
    .ask, .by { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); white-space: nowrap; }
    .err { color: var(--bx-danger, #FF7A7A); padding: 2px 0 8px; }
    .err bx-icon, .warn bx-icon { margin-right: 6px; }
    /* a partitioned tile asking for another's people's data (docs/partitions.md) */
    .warn { color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); padding: 5px 12px; border-top: 1px solid var(--bx-border, #33353F); }
  `];

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
      this._scope = d.scope ?? '';
      // Pull role descriptions for pending component targets.
      for (const p of this._pending) {
        if (reservedTarget(p.target) || this._roleDocs[p.target] !== undefined) continue;
        const cr = await fetch(`/api/xbin/components/${p.target}`);
        this._roleDocs = {
          ...this._roleDocs,
          [p.target]: cr.ok ? (await cr.json()).component?.roles ?? {} : {},
        };
      }
    } catch { /* xbind restart etc.; next event reloads */ }
  }

  // _post surfaces failures: a 403/400 from the approval gates (D26/D33)
  // carries a meaningful {error} — silently ignoring it renders as "the
  // button does nothing".
  async _post(method, g) {
    try {
      const r = await fetch('/api/xbin/grants', {
        method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(g),
      });
      if (!r.ok) {
        const d = await r.json().catch(() => ({}));
        this._err = d.error ?? `error ${r.status}`;
      } else {
        this._err = '';
      }
    } catch (e) { this._err = String(e.message ?? e); }
    this._load();
  }

  _approve(g) { return this._post('POST', { from: g.from, target: g.target, role: g.role }); }

  _revoke(g) { return this._post('DELETE', { from: g.from, target: g.target, role: g.role }); }

  // askWho: "org:data admins · workspace admin" from the server's hints.
  _askWho(p) {
    return (p.approvers ?? []).map((a) =>
      a === 'workspace-admin' ? 'workspace admin' : `${a} admins`).join(' · ');
  }

  render() {
    if (this._pending.length === 0 && !this._showAll) {
      // The count is an admin's way into the list (a workspace admin's, an
      // org admin's); a person's own tiles' grants (scope "mine"), with
      // nothing waiting on them, are no line on every screen.
      if (this._grants.length === 0 || this._scope === 'mine') return nothing;
      const n = this._grants.length;
      return toggle(`${n} ${n === 1 ? 'grant' : 'grants'} active`, () => { this._showAll = true; });
    }
    const scoped = !!this._scope; // non-admin filtered view: honor approvable
    return html`<div class="panel">
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon>${this._err}</div>` : nothing}
      ${this._pending.length > 0 ? html`
        <h4>pending access requests</h4>
        ${this._pending.map((p) => {
          const desc = p.blocked ? `blocked by policy — ${p.blocked}`
            : p.target.startsWith('cap:') ? (capInfo(p.target)?.label ?? '') : this._roleDocs[p.target]?.[p.role] ?? '';
          return html`
          <div class="plate" style=${p.blocked ? 'opacity:.55' : ''}>
            <div class="ph"><bx-icon name="key"></bx-icon>Grant request${p.direction ? html`<span class="dir">${p.direction}</span>` : nothing}</div>
            <div class="pr"><span class="k">request</span><span class="who">${grantArrow(p)}</span></div>
            <div class="pr"><span class="k">role</span><span class="role">${p.role}</span></div>
            ${desc ? html`<div class="pr"><span class="k">grants</span><span class="desc" title=${p.blocked ?? capInfo(p.target)?.desc ?? ''}>${desc}</span></div>` : nothing}
            ${p.warning ? html`<div class="warn" data-grant-warning><bx-icon name="warning"></bx-icon>${p.warning}</div>` : nothing}
            <div class="pacts">${p.blocked
              ? html`<button disabled title=${p.blocked}>blocked</button>`
              : (scoped && !p.approvable)
                ? html`<span class="ask" title="who can approve this request">ask: ${this._askWho(p) || 'a workspace admin'}</span>`
                : html`<button class="primary" @click=${() => this._approve(p)}>Approve</button>`}</div>
          </div>`;
        })}` : nothing}
      ${this._showAll ? html`
        <h4 style="margin-top:12px">active grants</h4>
        ${this._grants.map((g) => html`
          <div class="row">
            <span class="who">${grantArrow(g)}</span>
            <span class="role">${g.role}</span>
            ${g.direction ? html`<span class="dir">${g.direction}</span>` : nothing}
            <span class="desc">${g.approvedBy ? html`<span class="by">· approved by ${g.approvedBy}</span>` : nothing}</span>
            <button class="rm" @click=${() => this._revoke(g)}>revoke</button>
          </div>`)}
        ${toggle('hide', () => { this._showAll = false; })}` : toggle('show all grants', () => { this._showAll = true; })}
    </div>`;
  }
}

customElements.define('bx-grants', BxGrants);
