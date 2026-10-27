/**
 * <bx-limits tile="apps/foo" view="prompt|section"> — one tile's resource
 * limits (D203, docs/isolation.md §Resource limits): what its manifest asks
 * above the defaults, the caps it met, and the approvers' decisions.
 *
 *   view="prompt"  the shell's popover on the tile's window (D195): each
 *                  pending ask (Approve / Dismiss) and each cap hit (+25 /
 *                  +50 / +100 %, +50 the primary) this person may decide or
 *                  whose tile is theirs. A choice that doesn't fit, or that
 *                  this person may not approve, is greyed with the reason in
 *                  its title and in a line under the buttons; a person who
 *                  approves nothing reads who to ask. Reads the page's one
 *                  copy of GET /limits?pending=1 and the dismissals
 *                  (/vendor/bx-requests.js); a hit's choices come from the
 *                  tile's row (GET /limits?tile=). Dismiss is the person's
 *                  own (bx-dismiss.js, kind `limits`); an approver's Dismiss
 *                  on a hit clears it for the workspace (POST /limits/dismiss).
 *   view="section" every kind that applies to the tile: the limit in force,
 *                  the default, the decisions (who, when, which source), the
 *                  manifest's ask, usage, when a value applies — with set…
 *                  for approvers (canDecide), revert (canRevert), and restart
 *                  now for a VM's size. The tile's ⚙ (shell), the admin
 *                  console and xbind's /xbin/limits page use it.
 *
 * `.fetcher` is the fetch every call goes through: plain fetch by default
 * (the signed-in person's session — the shell, the page), the admin
 * console's xbin.fetch there. xbind judges every act again.
 *
 * Also exported for limits-table.js and the page: <bx-limit-edit> (the
 * typed editor), choicesTpl and limitsCss. Imports Lit by its /vendor URL
 * (no import map on xbind's own page). testApi() is the harness's surface.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import { dismiss } from '/vendor/bx-dismiss.js';
import { limitsStore, dismissalStore } from '/vendor/bx-requests.js';
import {
  meta, fmt, isVM, parseValue, step, choiceLabel, choiceState, primaryChoice, askWho, decisionText, hitText,
  promptItems, kindsOf, flags, appliesText, raiseBody, targetQuery, limitsCall, limitKey,
} from '/vendor/limits-kit.js';

const plainFetch = (...a) => fetch(...a);

// followEvents(cb): the page's events — the in-frame client's where there
// is one (the shell, a tile: a sandboxed frame can't open its own socket),
// else xbind's own page's socket. → unsubscribe
export function followEvents(cb) {
  if (window.xbin?.events?.on) return window.xbin.events.on(cb) ?? (() => {});
  const offs = [onEvent(cb), onReconnect(() => cb({ type: 'reload' }))];
  return () => offs.forEach((f) => f());
}

// ---- shared styles: plates, rows, buttons (bx-grants' look, D184) ----
export const limitsCss = css`
  :host { display: block; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); font-variant-numeric: tabular-nums; }
  :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
  [hidden] { display: none !important; }
  .muted { color: var(--bx-muted, #A3A6B6); }
  .meta { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); overflow-wrap: anywhere; }
  .err { color: var(--bx-danger, #FF7A7A); display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; padding: 2px 0 6px; }
  .note { color: var(--bx-muted, #A3A6B6); }
  /* a request: the sign plate — an ink header bar, label and value rows */
  .plate { margin: 0 0 8px; border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); overflow: hidden; }
  .plate:last-child { margin-bottom: 0; }
  .ph { display: flex; align-items: center; gap: 8px; padding: 4px 12px; background: var(--bx-text, #E9EAF0); color: var(--bx-panel, #1F2028);
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
    font-family: var(--bx-display, "Bricolage Grotesque", "Arial Black", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .ph .dir { margin-left: auto; color: inherit; border-color: currentColor; }
  .pr { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 12px; align-items: baseline;
    padding: 5px 12px; border-top: 1px solid var(--bx-border, #33353F); }
  .ph + .pr { border-top: 0; }
  .k, .pr .k { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif);
    letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .pacts { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 8px 12px; border-top: 1px solid var(--bx-border, #33353F); }
  .pacts .quiet.end { margin-left: auto; }
  .whys { padding: 0 12px 8px; margin: -2px 0 0; list-style: none; color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .whys li { padding: 1px 0; }
  .whys b { font-weight: 600; color: var(--bx-text, #E9EAF0); }
  .ask { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
  .dir, .badge {
    display: inline-flex; align-items: center; gap: 4px; box-sizing: border-box; height: 20px; padding: 0 6px; white-space: nowrap;
    border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); color: var(--bx-muted, #A3A6B6);
    font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase;
  }
  .badge.warn { color: var(--bx-warn, #F2994A); border-color: var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
  .badge.crit { color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); background: var(--bx-danger-bg, #3A2B32); }
  .badge.on { color: var(--bx-ok, #A3CF5E); border-color: var(--bx-ok, #A3CF5E); background: var(--bx-ok-bg, #2F352E); }
  .badge.info { color: var(--bx-info, #A9B4C6); border-color: var(--bx-info, #A9B4C6); background: var(--bx-info-bg, #30323B); }
  /* buttons (product-ui §6): secondary by default, one primary */
  button {
    box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; white-space: nowrap;
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    font: inherit; font-weight: 600;
  }
  button:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); }
  button.primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  button.primary:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  button:disabled { opacity: .5; cursor: default; }
  button.quiet { background: transparent; border-color: transparent; color: var(--bx-muted, #A3A6B6); font-weight: 400; }
  button.quiet:hover:not(:disabled) { background: transparent; border-color: var(--bx-border-strong, #666A7E); color: var(--bx-text, #E9EAF0); }
  button.small { min-height: 24px; padding: 0 8px; }
  input, select { box-sizing: border-box; height: var(--bx-control-h, 28px); padding: 0 8px; max-width: 100%; font: inherit;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); }
  .choices { display: inline-flex; flex-wrap: wrap; gap: 6px; align-items: center; }
`;

// choicesTpl(choices, kind, pick, {busy}) → the +25 / +50 / +100 buttons:
// the primary one (the default when live), the greyed ones disabled with
// their reason in the title; whysTpl lists the reasons under them.
export function choicesTpl(choices, kind, pick, { busy = false, label = '' } = {}) {
  const prim = primaryChoice(choices);
  return html`<span class="choices" role="group" aria-label=${label || `raise ${meta(kind).label}`}>${(choices ?? []).map((c) => {
    const st = choiceState(c);
    return html`<button class=${c === prim ? 'primary' : ''} data-pct=${c.pct} ?disabled=${!st.live || busy}
      aria-pressed=${c === prim ? 'true' : 'false'}
      title=${st.live ? `raise ${meta(kind).label} to ${fmt(kind, c.value)}` : st.why} @click=${() => pick(c)}>${choiceLabel(c, kind)}</button>`;
  })}</span>`;
}
export function whysTpl(choices, kind) {
  const grey = (choices ?? []).filter((c) => !choiceState(c).live);
  if (!grey.length) return nothing;
  return html`<ul class="whys" data-whys>${grey.map((c) => html`<li><b>${choiceLabel(c, kind)}</b>: ${choiceState(c).why}</li>`)}</ul>`;
}

// A row's choices, or — a row read before the server offered them — the
// three steps with the server's word to come (it re-checks every POST).
const choicesOf = (kind, l) => (l?.choices?.length ? l.choices
  : [25, 50, 100].map((pct) => ({ pct, value: step(kind, l?.effective ?? 0, pct), fits: true, approvable: true, default: pct === 50 })));

// ---- <bx-limit-edit>: a typed value for one kind (source admin-set) ----
export class BxLimitEdit extends LitElement {
  static properties = {
    tile: { type: String }, scope: { type: String }, kind: { type: String },
    lim: { attribute: false },      // the row's limit for the kind
    capacity: { attribute: false }, // limits.Capacity for the kind (approvers), or null
    fetcher: { attribute: false },
    _text: { state: true }, _restart: { state: true }, _err: { state: true }, _busy: { state: true },
    _offerForce: { state: true }, // a 409 (doesn't fit): a workspace admin may set it anyway (force)
  };
  static styles = [limitsCss, css`
    :host { margin-top: 6px; padding: 8px 10px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730); }
    .line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
    .line + .line { margin-top: 6px; }
    input.v { width: 9em; }
    label.ck { display: inline-flex; gap: 6px; align-items: center; }
  `];
  constructor() { super(); this._text = ''; this._restart = false; this._err = ''; this._busy = false; this._offerForce = false; }
  firstUpdated() { this.renderRoot.querySelector('input.v')?.focus(); }
  _parsed() { return this._text.trim() ? parseValue(this.kind, this._text) : null; }
  async _save(force = false) {
    const p = this._parsed();
    if (!p || p.error) { this._err = p?.error ?? 'type a value'; return; }
    this._busy = true;
    const r = await limitsCall(this.fetcher, 'POST', '/limits',
      raiseBody({ tile: this.tile, scope: this.scope }, this.kind, p.value, 'admin-set', { restart: this._restart, force }));
    this._busy = false;
    if (!r.ok) { this._err = r.error; this._offerForce = r.status === 409 && !force; return; }
    this._err = '';
    this.dispatchEvent(new CustomEvent('limit-saved', { detail: r.data, bubbles: true, composed: true }));
  }
  _cancel() { this.dispatchEvent(new CustomEvent('limit-cancel', { bubbles: true, composed: true })); }
  render() {
    const k = this.kind, l = this.lim ?? {}, p = this._parsed(), cap = this.capacity;
    const room = cap && cap.total ? Math.max(0, (cap.free ?? 0) - (cap.reserve ?? 0) - (cap.promised ?? 0)) : null;
    return html`
      <div class="line">
        <label class="k" for="v">new ${meta(k).label}</label>
        <input id="v" class="v" .value=${this._text} placeholder=${`e.g. ${fmt(k, step(k, l.effective ?? 0, 50))}`}
          aria-describedby="pv" @input=${(e) => { this._text = e.target.value; this._err = ''; this._offerForce = false; }}
          @keydown=${(e) => { if (e.key === 'Enter') this._save(); if (e.key === 'Escape') this._cancel(); }}>
        <span id="pv" class="meta">${p?.value ? `= ${fmt(k, p.value)}` : p?.error ? p.error : `now ${fmt(k, l.effective)}, default ${fmt(k, l.default)}`}</span>
      </div>
      ${room != null && !meta(k).unit.startsWith('vCPU') ? html`<div class="line meta">room for raises: ${fmt(k, room)} (${fmt(k, cap.free)} free, ${fmt(k, cap.reserve)} kept back${cap.promised ? `, ${fmt(k, cap.promised)} granted to other tiles but unused` : ''})</div>` : nothing}
      ${isVM(k) ? html`<div class="line"><label class="ck"><input type="checkbox" .checked=${this._restart} @change=${(e) => { this._restart = e.target.checked; }}>
        restart the backend now — it stops first (downtime); otherwise the size applies at its next start</label></div>` : nothing}
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span>
        ${this._offerForce ? html`<button class="small" title="workspace admins only: set it although it doesn't fit"
          @click=${() => this._save(true)}>set it anyway</button>` : nothing}</div>` : nothing}
      <div class="line">
        <button class="primary" ?disabled=${this._busy || !p?.value} @click=${() => this._save()}>Set</button>
        <button class="quiet" @click=${() => this._cancel()}>Cancel</button>
      </div>`;
  }
}
customElements.define('bx-limit-edit', BxLimitEdit);

// ---- <bx-limits> ----
export class BxLimits extends LitElement {
  static properties = {
    tile: { type: String, reflect: true },
    view: { type: String, reflect: true }, // prompt | section
    fetcher: { attribute: false },
    _pending: { state: true },   // the /limits?pending=1 answer (prompt)
    _dismissed: { state: true }, // the person's dismissals (prompt)
    _row: { state: true },       // GET /limits?tile= → tile (a Row)
    _capacity: { state: true },  // … → capacity (approvers)
    _loadErr: { state: true },
    _err: { state: true },
    _busy: { state: true },      // a key or kind while its call runs
    _edit: { state: true },      // the kind whose typed editor is open
    _revert: { state: true },    // the kind whose revert asks to be confirmed
  };

  static styles = [limitsCss, css`
    :host([view="prompt"]) .wrap { display: block; }
    .kind { padding: 8px 0; border-top: 1px solid var(--bx-border, #33353F); }
    .kind:first-child { border-top: 0; padding-top: 2px; }
    .kh { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 8px; min-height: var(--bx-control-h, 28px); }
    .kh .kl { min-width: 7.5em; }
    .kh .eff { font-weight: 600; }
    .kh .acts { margin-left: auto; display: inline-flex; gap: 6px; flex-wrap: wrap; }
    .kd { padding: 1px 0; }
    .kd.dormant { color: var(--bx-muted, #A3A6B6); text-decoration: line-through; text-decoration-thickness: 1px; }
    .hit { margin-top: 4px; padding: 6px 10px; border-left: 3px solid var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
    .hit .choices { margin-top: 6px; }
    .hit .whys { padding: 4px 0 0; }
    .confirm { margin-top: 6px; display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
    .empty { color: var(--bx-muted, #A3A6B6); padding: 2px 0; }
  `];

  constructor() {
    super();
    this.view = 'section';
    this._pending = null;
    this._dismissed = null;
    this._row = null;
    this._err = '';
    this._offs = [];
  }

  get _fetch() { return this.fetcher ?? plainFetch; }

  connectedCallback() {
    super.connectedCallback();
    if (this.view === 'prompt') {
      this._offs.push(limitsStore().subscribe((d) => { this._pending = d; this._maybeRow(); }),
        dismissalStore().subscribe((d) => { this._dismissed = d; }));
    } else {
      this._load();
      this._offs.push(followEvents((e) => { if (e?.type === 'limits' || e?.type === 'reload') this._soon(); }));
    }
  }
  disconnectedCallback() { super.disconnectedCallback(); for (const f of this._offs.splice(0)) f(); clearTimeout(this._t); }
  updated(changed) {
    if (changed.has('tile') && changed.get('tile') !== undefined) { this._row = null; this.view === 'prompt' ? this._maybeRow() : this._load(); }
  }

  _soon() { clearTimeout(this._t); this._t = setTimeout(() => this._load(), 250); }

  // GET /limits?tile=: the row (and, for approvers, choices and capacity)
  async _load() {
    if (!this.tile) return;
    const r = await limitsCall(this._fetch, 'GET', `/limits?tile=${encodeURIComponent(this.tile)}`);
    if (r.ok) { this._row = r.data?.tile ?? null; this._capacity = r.data?.capacity ?? null; this._loadErr = ''; } else this._loadErr = r.error;
  }

  // the prompt reads the row only for a hit this person may raise (its choices)
  _maybeRow() {
    if (this.view !== 'prompt' || !this.tile) return;
    if (this._items().some((p) => p.cap && p.approvable)) this._load();
  }

  _items() { return promptItems(this._pending, { tile: this.tile, dismissed: this._dismissed }); }

  // POST /limits; then the stores and the row are read again (the event
  // also says so, a moment later)
  async _post(body, busy) {
    this._busy = busy; this._err = '';
    const r = await limitsCall(this._fetch, 'POST', '/limits', body);
    this._busy = null;
    if (!r.ok) { this._err = r.error; return false; }
    if (r.data?.tile) this._row = r.data.tile;
    this._edit = null;
    this._refreshPending();
    return true;
  }

  _approve(p) { return this._post(raiseBody(p, p.kind, p.ask, 'manifest-ask'), p.key ?? p.kind); }
  _raise(x, kind, c, busy) { return this._post(raiseBody(x, kind, c.value, 'raise'), busy); }

  // Dismiss: an approver clears a hit for everyone (the workspace route);
  // otherwise it is the person's own, on all their devices.
  async _dismiss(p) {
    if (p.cap && p.approvable) {
      this._busy = p.key;
      const r = await limitsCall(this._fetch, 'POST', `/limits/dismiss?${targetQuery(p)}&kind=${encodeURIComponent(p.kind)}`);
      this._busy = null;
      if (r.ok) { this._refreshPending(); return; }
      if (r.status !== 404) { this._err = r.error; return; }
    }
    dismissalStore().change((d) => dismiss(d, 'limits', p.key));
  }

  async _doRevert(kind) {
    this._busy = kind; this._err = '';
    const r = await limitsCall(this._fetch, 'DELETE', `/limits?tile=${encodeURIComponent(this.tile)}&kind=${encodeURIComponent(kind)}`);
    this._busy = null; this._revert = null;
    if (!r.ok) { this._err = r.error; return; }
    if (r.data?.tile) this._row = r.data.tile;
    this._refreshPending();
  }

  // the page's pending store (the prompts, the badge) reads again — on the
  // person's own pages; the admin console (its own fetcher) has none
  _refreshPending() { if (!this.fetcher) limitsStore().load(); }

  // restart now (a VM's size): the same decision again, with restart
  _restart(kind, l) {
    const d = l.set ?? l.approved;
    return this._post(raiseBody({ tile: this.tile }, kind, l.effective, d?.source ?? 'admin-set', { restart: true }), kind);
  }

  // ---- prompt ----
  _promptTpl() {
    const items = this._items();
    if (!items.length && !this._err) return nothing;
    return html`<div class="wrap">
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      ${items.map((p) => this._plate(p))}
    </div>`;
  }

  _plate(p) {
    const l = this._row?.tile === p.tile ? this._row?.limits?.[p.kind] : null;
    const busy = this._busy === p.key;
    let acts;
    if (!p.approvable) {
      acts = html`<span class="ask" title=${`who can decide: ${(p.approvers ?? []).join(', ') || 'workspace-admin'}`}>ask ${p.askWho}</span>`;
    } else if (!p.cap) {
      acts = html`<button class="primary" data-approve ?disabled=${busy} @click=${() => this._approve(p)}>Approve ${fmt(p.kind, p.ask)}</button>`;
    } else if (!l) {
      acts = html`<span class="ask">${this._loadErr || 'reading the choices…'}</span>`;
    } else {
      acts = choicesTpl(choicesOf(p.kind, l), p.kind, (c) => this._raise(p, p.kind, c, p.key), { busy });
    }
    const dismissTitle = p.cap && p.approvable ? 'clear this hit for everyone — it shows again at the next one'
      : 'hide this for you, on all your devices — it stays pending, and the approvers still see it';
    return html`<div class="plate" data-limit=${p.key} data-why=${p.why}>
      <div class="ph"><bx-icon name=${p.cap ? 'warning' : 'chart'}></bx-icon>${p.head}<span class="dir">${p.label}</span></div>
      ${this.tile ? nothing : html`<div class="pr"><span class="k">tile</span><span class="mono">${p.tile}</span></div>`}
      <div class="pr"><span class="k">${p.cap ? 'hit' : 'asks'}</span><span>${p.what}</span></div>
      <div class="pr"><span class="k">now</span><span>${p.current}${l?.usage != null ? html` <span class="meta">· uses ${fmt(p.kind, l.usage)}</span>` : nothing}</span></div>
      <div class="pacts">${acts}
        <button class="quiet end" data-dismiss ?disabled=${busy} title=${dismissTitle} @click=${() => this._dismiss(p)}>Dismiss</button></div>
      ${p.cap && p.approvable && l ? whysTpl(choicesOf(p.kind, l), p.kind) : nothing}
    </div>`;
  }

  // ---- section ----
  _sectionTpl() {
    if (this._loadErr && !this._row) return html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._loadErr}</span></div>`;
    if (!this._row) return html`<div class="empty">…</div>`;
    const kinds = kindsOf(this._row);
    return html`
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      ${kinds.length ? html`<div class="kinds">${kinds.map(([k, l]) => this._kindTpl(k, l))}</div>`
        : html`<div class="empty">No limits apply to this tile: it runs no backend, VM or sandboxes, or none is enforced on this host.</div>`}`;
  }

  _kindTpl(k, l) {
    const fl = flags(l), busy = this._busy === k, applies = appliesText(l, k);
    const mayRestart = isVM(k) && l.canDecide && fl.later && Number(l.effective) > Number(l.default);
    const hit = { tile: this.tile, kind: k, why: 'cap', cap: true, hit: l.hit, approvable: !!l.canDecide };
    hit.key = limitKey(hit);
    return html`<div class="kind" data-kind=${k}>
      <div class="kh">
        <span class="k kl">${meta(k).label}</span>
        <span class="eff" title="the limit in force">${fmt(k, l.effective)}</span>
        ${fl.raised ? html`<span class="badge on">raised</span>` : nothing}
        ${fl.pending ? html`<span class="badge info">asks ${fmt(k, l.ask)}</span>` : nothing}
        ${fl.cap ? html`<span class="badge warn">at its limit</span>` : nothing}
        ${l.enforced === false ? html`<span class="badge" title="this host doesn't enforce it (no cgroup delegation, no VM runtime…)">not enforced</span>` : nothing}
        <span class="acts">
          ${l.canDecide && this._edit !== k ? html`<button class="small" data-edit @click=${() => { this._edit = k; this._revert = null; }}>set…</button>` : nothing}
          ${l.canRevert && (l.set || l.approved) ? html`<button class="small quiet" data-revert ?disabled=${busy}
            title=${`back to the default (${fmt(k, l.default)})`} @click=${() => { this._revert = k; this._edit = null; }}>revert</button>` : nothing}
        </span>
      </div>
      <div class="meta">default ${fmt(k, l.default)}${l.max ? ` · at most ${fmt(k, l.max)}` : ''}${l.usage != null ? ` · uses ${fmt(k, l.usage)}` : ''}</div>
      ${l.set ? html`<div class="kd" data-decision="set">${decisionText(l.set, k)}</div>` : nothing}
      ${l.approved ? html`<div class="kd ${l.dormant ? 'dormant' : ''}" data-decision="ask"
        title=${l.dormant ? 'the manifest no longer asks: kept, and it applies again if the manifest asks up to it' : ''}>${decisionText(l.approved, k)}</div>
        ${l.dormant ? html`<div class="meta">dormant — the manifest no longer asks</div>` : nothing}` : nothing}
      ${l.ask ? html`<div class="kd">the manifest asks ${fmt(k, l.ask)}${l.pending ? ' — waiting for approval' : ''}
        ${l.pending && l.canDecide ? html`<button class="small primary" data-approve ?disabled=${busy}
          @click=${() => this._post(raiseBody({ tile: this.tile }, k, l.ask, 'manifest-ask'), k)}>Approve ${fmt(k, l.ask)}</button>` : nothing}
        ${l.pending && !l.canDecide ? html`<span class="ask"> — ask ${askWho(l.approvers)}</span>` : nothing}</div>` : nothing}
      ${l.hit ? html`<div class="hit" data-hit>
        <div>at its limit: ${hitText(l.hit, k)}</div>
        ${l.canDecide ? html`${choicesTpl(choicesOf(k, l), k, (c) => this._raise({ tile: this.tile }, k, c, k), { busy })}
          <button class="quiet small" ?disabled=${busy} title="clear this hit for everyone — it shows again at the next one" @click=${() => this._dismiss(hit)}>dismiss</button>
          ${whysTpl(choicesOf(k, l), k)}` : html`<div class="ask">ask ${askWho(l.approvers)} to raise it</div>`}
      </div>` : nothing}
      ${applies ? html`<div class="meta" data-applies>${applies}
        ${mayRestart ? html`<button class="small" ?disabled=${busy} title="restart the backend now so the size applies — it stops first (downtime)"
          @click=${() => this._restart(k, l)}>restart now</button>` : nothing}</div>` : nothing}
      ${l.note ? html`<div class="meta note">${l.note}</div>` : nothing}
      ${this._revert === k ? html`<div class="confirm">Back to the default, ${fmt(k, l.default)}?${l.ask ? ' The manifest’s ask is pending again.' : ''}
        <button class="small primary" data-confirm-revert ?disabled=${busy} @click=${() => this._doRevert(k)}>Revert</button>
        <button class="small quiet" @click=${() => { this._revert = null; }}>Cancel</button></div>` : nothing}
      ${this._edit === k ? html`<bx-limit-edit .tile=${this.tile} .kind=${k} .lim=${l} .capacity=${this._capacity?.[k] ?? null} .fetcher=${this.fetcher}
        @limit-saved=${(e) => { if (e.detail?.tile) this._row = e.detail.tile; this._edit = null; this._refreshPending(); }}
        @limit-cancel=${() => { this._edit = null; }}></bx-limit-edit>` : nothing}
    </div>`;
  }

  render() { return this.view === 'prompt' ? this._promptTpl() : this._sectionTpl(); }

  // ---- test surface (hack/ui-harness) ----
  testApi() {
    const s = this;
    return {
      get view() { return s.view; },
      get tile() { return s.tile; },
      get items() { return s._items(); },
      get row() { return s._row; },
      get error() { return s._err || s._loadErr || ''; },
      reload: () => s._load(),
      edit: (kind) => { s._edit = kind; },
      approve: (kind) => { const p = s._items().find((x) => x.kind === kind && !x.cap); return p ? s._approve(p) : false; },
      raise: (kind, value, source = 'raise') => s._post(raiseBody({ tile: s.tile }, kind, value, source), kind),
      revert: (kind) => s._doRevert(kind),
    };
  }
}
customElements.define('bx-limits', BxLimits);
