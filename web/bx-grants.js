/**
 * <bx-grants> — the owner's grant-approval panel (docs/auth.md §Roles and grants).
 * Shows pending `uses` requests with the callee's role descriptions and
 * one-click approve. Renders nothing when there is nothing to decide, so it
 * can sit permanently in the root page; the full list of grants is the
 * admin console's (D188: no "N grants active" line on every screen).
 * A pending request is a sign plate (D184, product-ui §8): an ink header
 * bar over label and value rows, Approve as the primary button, and a quiet
 * Dismiss that hides the request for this person on all their devices
 * (/vendor/bx-dismiss.js); the shell's settings menu ("Show dismissed")
 * brings them back. With nothing pending it renders nothing.
 *
 * `tile="<path>"` shows only the requests that tile made (`from`): the
 * shell puts one on each tile's window (D195) instead of a strip at the
 * top. Every instance on a page reads the same requests and dismissals
 * (/vendor/bx-requests.js), so a dozen of them cost one fetch.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { capInfo } from '/vendor/bx-allow.js';
import { grantArrow } from '/vendor/bx-grant-row.js';
import { grantKey, split, dismiss } from '/vendor/bx-dismiss.js';
import { grantsStore, dismissalStore } from '/vendor/bx-requests.js';
import '/vendor/bx-icons.js';

// a link-like control: focusable, Enter and Space work
const toggle = (label, fn, title = '') => html`<a role="button" tabindex="0" title=${title} @click=${fn}
  @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fn(); } }}>${label}</a>`;
// Reserved targets have no component to look role docs up on.
const reservedTarget = (t) => /^(res:|cap:|gpu:|net:|xbin$|xbin:|code$|code:)/.test(t);

// The callees' role descriptions, read once per page for every instance.
const roleDocs = new Map(); // target → Promise<{role: description}>
const docsOf = (target) => {
  if (!roleDocs.has(target)) {
    roleDocs.set(target, fetch(`/api/xbin/components/${target}`)
      .then(async (r) => (r.ok ? (await r.json()).component?.roles ?? {} : {})).catch(() => ({})));
  }
  return roleDocs.get(target);
};

export class BxGrants extends LitElement {
  static properties = {
    tile: { type: String, reflect: true }, // only this tile's requests (D195)
    _pending: { state: true },
    _roleDocs: { state: true },
    _dismissed: { state: true }, // the person's dismissals (bx-dismiss.js); null until read
    _scope: { state: true },   // "org" | "mine" on the non-admin filtered view (D26/D33)
    _err: { state: true },     // last approve/revoke failure
  };

  static styles = [scrollCss, css`
    :host {
      display: block;
      font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif);
      color: var(--bx-text, #E9EAF0);
    }
    /* on a tile's window the shell's popover is the card */
    :host([tile]) .panel { background: transparent; border: 0; padding: 0; }
    :host([tile]) h4 { display: none; }
    :host([tile]) .plate:last-child { margin-bottom: 0; }
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
    .who { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); overflow-wrap: anywhere; }
    .role { font-weight: 600; }
    .desc { color: var(--bx-muted, #A3A6B6); flex: 1; min-width: 0; }
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
    button:disabled { opacity: .5; cursor: default; }
    a { display: inline-block; color: var(--bx-muted, #A3A6B6); cursor: pointer; text-decoration: none; }
    a:hover { color: var(--bx-text, #E9EAF0); text-decoration: underline; }
    /* Dismiss: a quiet row action (R1), after the decision */
    button.quiet { background: transparent; border-color: transparent; color: var(--bx-muted, #A3A6B6); font-weight: 400; }
    button.quiet:hover:not(:disabled) { background: transparent; border-color: var(--bx-border-strong, #666A7E); color: var(--bx-text, #E9EAF0); }
    .pacts .quiet { margin-left: auto; }
    .dir {
      display: inline-flex; align-items: center; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
    }
    .ask { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); white-space: nowrap; }
    .err { color: var(--bx-danger, #FF7A7A); padding: 2px 0 8px; }
    .err bx-icon, .warn bx-icon { margin-right: 6px; }
    /* a partitioned tile asking for another's people's data (docs/partitions.md) */
    .warn { color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); padding: 5px 12px; border-top: 1px solid var(--bx-border, #33353F); }
  `];

  constructor() {
    super();
    this._pending = [];
    this._roleDocs = {};
    this._dismissed = null;
  }

  connectedCallback() {
    super.connectedCallback();
    this._offs = [
      grantsStore().subscribe((d) => this._got(d)),
      dismissalStore().subscribe((d) => { this._dismissed = d; }),
    ];
  }

  disconnectedCallback() { super.disconnectedCallback(); for (const f of this._offs ?? []) f(); }

  // _got(d): the page's read of /grants — this instance's part of it, and
  // the role descriptions of its component targets.
  _got(d) {
    this._pending = (d.pending ?? []).filter((p) => !this.tile || p.from === this.tile);
    this._scope = d.scope ?? '';
    for (const p of this._pending) {
      if (reservedTarget(p.target) || this._roleDocs[p.target] !== undefined) continue;
      docsOf(p.target).then((roles) => { this._roleDocs = { ...this._roleDocs, [p.target]: roles }; });
    }
  }

  updated(changed) {
    if (changed.has('tile') && grantsStore().value) this._got(grantsStore().value);
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
    grantsStore().load();
  }

  _approve(g) { return this._post('POST', { from: g.from, target: g.target, role: g.role }); }

  // Dismiss: at once on every page element, then stored through the page's
  // one save chain (bx-dismiss.js dismissals()); the requests the server no
  // longer lists are pruned there, after fresh reads (#11).
  _dismiss(p) { return dismissalStore().change((d) => dismiss(d, 'grants', grantKey(p))); }

  // askWho: "org:data admins · workspace admin" from the server's hints.
  _askWho(p) {
    return (p.approvers ?? []).map((a) =>
      a === 'workspace-admin' ? 'workspace admin' : `${a} admins`).join(' · ');
  }

  render() {
    const { shown, hidden } = split(this._pending, this._dismissed, 'grants', grantKey);
    // Nothing waiting that this person hasn't dismissed: no strip at all
    // (the shell's settings menu shows dismissed requests again, D188).
    if (shown.length === 0 && !this._err) return nothing;
    const scoped = !!this._scope; // non-admin filtered view: honor approvable
    return html`<div class="panel">
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon>${this._err}</div>` : nothing}
      ${shown.length > 0 ? html`
        <h4>pending access requests</h4>
        ${shown.map((p) => {
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
                : html`<button class="primary" @click=${() => this._approve(p)}>Approve</button>`}
              <button class="quiet" data-dismiss title="hide this request for you, on all your devices — it stays pending, and other admins still see it"
                @click=${() => this._dismiss(p)}>Dismiss</button></div>
          </div>`;
        })}` : nothing}
    </div>`;
  }
}

customElements.define('bx-grants', BxGrants);
