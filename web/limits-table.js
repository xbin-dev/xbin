/**
 * limits-table.js — the workspace's tile resource limits as a list (D203,
 * docs/isolation.md §Resource limits), for the admin console's runtime →
 * limits tab and xbind's /xbin/limits page:
 *
 *   <bx-limits-table [tile] [ceilings]>  GET /limits: every tile and kind the
 *       viewer may see (a workspace admin: all; an org admin: the org's; a
 *       person: their own tiles), filtered by raised / pending / at its
 *       limit and by org, with inline set…, revert and approve (each as the
 *       server's canDecide / canRevert say). `tile` narrows to one tile (the
 *       console's #limits/<tile> deep link). `ceilings` adds the per-org
 *       ceilings under the list: editable for a workspace admin, read-only
 *       for an org admin.
 *   <bx-org-limits org .limits>  one org's per-tile ceilings — the most an
 *       admin of the org may approve for each of its tiles — PUT
 *       /orgs/{org}/limits (workspace admins). An empty field: no ceiling,
 *       so the org's admins can't raise that kind above the default.
 *
 * `.fetcher`: plain fetch by default (the person's session), the admin
 * console's xbin.fetch there. Lit by its /vendor URL (xbind's page has no
 * import map). Words: /vendor/limits-kit.js.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { limitsCss, choicesTpl, whysTpl, followEvents } from '/vendor/bx-limits.js';
import {
  KINDS, meta, fmt, sizeText, decisionText, hitText, askWho, flags, appliesText, tableEntries, counts, raiseBody, limitsCall, ceilingsBody, step,
} from '/vendor/limits-kit.js';

const plainFetch = (...a) => fetch(...a);
const q = encodeURIComponent;

// ---- <bx-org-limits> ----
export class BxOrgLimits extends LitElement {
  static properties = {
    org: { type: String, reflect: true },
    limits: { attribute: false },   // the org's ceilings: {memMiB: 4096, …}
    defaults: { attribute: false }, // the workspace defaults, when known
    readonly: { type: Boolean },
    fetcher: { attribute: false },
    _draft: { state: true }, _busy: { state: true }, _err: { state: true }, _saved: { state: true },
  };
  static styles = [limitsCss, css`
    .grid { display: grid; grid-template-columns: minmax(8em, max-content) minmax(0, 12em); gap: 4px 12px; align-items: center; }
    .grid.defs { grid-template-columns: minmax(8em, max-content) minmax(5em, max-content) minmax(0, 12em); }
    .grid .meta { white-space: nowrap; }
    .grid input { width: 100%; }
    .line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 8px; }
    .ok { color: var(--bx-ok, #A3CF5E); }
    @media (max-width: 560px) { .grid, .grid.defs { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); } .grid .def { display: none; } }
  `];
  constructor() { super(); this.limits = {}; this._draft = {}; this._err = ''; this._saved = false; }
  get _fetch() { return this.fetcher ?? plainFetch; }
  willUpdate(changed) { if (changed.has('org')) { this._draft = {}; this._err = ''; } }

  async _save() {
    const { body, error } = ceilingsBody(this._draft, this.limits ?? {});
    if (error) { this._err = error; return; }
    if (!Object.keys(body).length) { this._draft = {}; return; }
    this._busy = true; this._err = ''; this._saved = false;
    const r = await limitsCall(this._fetch, 'PUT', `/orgs/${q(this.org)}/limits`, body);
    this._busy = false;
    if (!r.ok) { this._err = r.error; return; }
    this.limits = r.data?.limits ?? {};
    this._draft = {}; this._saved = true;
    this.dispatchEvent(new CustomEvent('org-limits-saved', { detail: { org: this.org, limits: this.limits }, bubbles: true, composed: true }));
  }

  render() {
    const cur = this.limits ?? {}, d = this._draft, dirty = Object.keys(d).length > 0, defs = !!this.defaults;
    if (this.readonly) {
      const set = KINDS.filter((k) => cur[k]);
      return html`<div class="meta">${set.length ? set.map((k) => `${meta(k).label} ${fmt(k, cur[k])}`).join(' · ')
        : 'no ceilings: the org’s admins can’t raise its tiles above the defaults'}</div>`;
    }
    return html`
      <div class="grid ${defs ? 'defs' : ''}" role="group" aria-label=${`per-tile ceilings for org ${this.org}`}>
        <span class="k">limit</span>${defs ? html`<span class="k def">default</span>` : nothing}<span class="k">ceiling per tile</span>
        ${KINDS.map((k) => html`
          <span>${meta(k).label}</span>
          ${defs ? html`<span class="meta def">${this.defaults[k] != null ? fmt(k, this.defaults[k]) : ''}</span>` : nothing}
          <input data-kind=${k} aria-label=${`${meta(k).label} ceiling`} placeholder="none" title=${cur[k] ? `now ${fmt(k, cur[k])}` : 'no ceiling'}
            .value=${k in d ? d[k] : sizeText(k, cur[k])}
            @input=${(e) => { this._draft = { ...this._draft, [k]: e.target.value }; this._saved = false; }}
            @keydown=${(e) => { if (e.key === 'Enter') this._save(); }}>`)}
      </div>
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      <div class="line">
        <button class="primary" data-save ?disabled=${!dirty || this._busy} @click=${() => this._save()}>Save ceilings</button>
        ${dirty ? html`<button class="quiet" @click=${() => { this._draft = {}; this._err = ''; }}>Cancel</button>` : nothing}
        ${this._saved ? html`<span class="ok"><bx-icon name="ok"></bx-icon> saved</span>` : nothing}
        <span class="meta">sizes like 4G or 512M; empty removes a ceiling</span>
      </div>`;
  }
}
customElements.define('bx-org-limits', BxOrgLimits);

// ---- <bx-limits-table> ----
export class BxLimitsTable extends LitElement {
  static properties = {
    tile: { type: String },               // narrow to this tile (a deep link)
    ceilings: { type: Boolean },          // show the per-org ceilings under the list
    fetcher: { attribute: false },
    _data: { state: true },               // the GET /limits answer
    _orgs: { state: true },               // GET /orgs (a workspace admin's ceilings)
    _q: { state: true }, _chips: { state: true }, _org: { state: true },
    _edit: { state: true }, _revert: { state: true }, _busy: { state: true },
    _err: { state: true }, _loadErr: { state: true },
  };
  static styles = [limitsCss, css`
    .bar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 0 0 10px; }
    .bar input[type=search] { flex: 1; min-width: 12em; }
    .chip { box-sizing: border-box; min-height: var(--bx-control-h, 28px); display: inline-flex; align-items: center; gap: 6px;
      padding: 0 10px; cursor: pointer; user-select: none; font-weight: 400; border: 1px solid var(--bx-border-strong, #666A7E);
      border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6); }
    .chip:hover { background: var(--bx-hover, #2A2B34); color: var(--bx-text, #E9EAF0); }
    .chip[aria-pressed="true"] { background: var(--bx-selection, #262C5C); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-selection-text, #E9EAF0); }
    .count { font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); color: var(--bx-muted, #A3A6B6); white-space: nowrap; }
    table { border-collapse: collapse; width: 100%; }
    th { text-align: left; padding: 4px 8px 4px 0; color: var(--bx-muted, #A3A6B6); border-bottom: 1px solid var(--bx-border-strong, #666A7E);
      font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
    td { padding: 4px 8px 4px 0; vertical-align: top; border-top: 1px solid var(--bx-border, #33353F); }
    tr.cont > td { border-top-color: transparent; }
    tr.sub > td { border-top: 0; padding-top: 0; }
    tr.focus > td.tile .mono { color: var(--bx-accent, #8C9BFF); }
    td.tile { max-width: 18em; }
    td.eff { font-weight: 600; white-space: nowrap; }
    td.acts { text-align: right; white-space: nowrap; }
    td.acts button + button { margin-left: 6px; }
    td .badge { margin: 0 4px 2px 0; }
    .hit { padding: 6px 10px; border-left: 3px solid var(--bx-warn, #F2994A); background: var(--bx-warn-bg, #382F2C); }
    .hit .choices { margin-top: 6px; }
    .hit .whys { padding: 4px 0 0; }
    .confirm { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; justify-content: flex-end; }
    .empty { color: var(--bx-muted, #A3A6B6); padding: 12px 0; }
    h3 { margin: 24px 0 8px; font: var(--bx-font-title, 600 15px/20px "Instrument Sans", system-ui, sans-serif); }
    details.org { border-top: 1px solid var(--bx-border, #33353F); padding: 6px 0; }
    details.org summary { cursor: pointer; display: flex; gap: 8px; align-items: baseline; flex-wrap: wrap; }
    details.org bx-org-limits { margin: 8px 0 4px; }
    /* a phone: each tile-kind is a block of labelled lines */
    @media (max-width: 720px) {
      thead { display: none; }
      table, tbody, tr, td { display: block; }
      tr.row { border-top: 1px solid var(--bx-border, #33353F); padding: 6px 0; }
      tr.row.cont { border-top: 1px dashed var(--bx-border, #33353F); }
      td { border: 0; padding: 1px 0; max-width: none; }
      td.tile:empty { display: none; }
      td[data-l]::before { content: attr(data-l) " "; color: var(--bx-muted, #A3A6B6);
        font: var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif); letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
      td.acts { text-align: left; padding-top: 4px; }
      td.blank { display: none; }
    }
  `];

  constructor() {
    super();
    this._data = null; this._q = ''; this._chips = new Set(); this._org = ''; this._err = ''; this._loadErr = '';
    this._offs = [];
  }
  get _fetch() { return this.fetcher ?? plainFetch; }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._offs.push(followEvents((e) => { if (['limits', 'reload', 'users'].includes(e?.type)) this._soon(); }));
  }
  disconnectedCallback() { super.disconnectedCallback(); for (const f of this._offs.splice(0)) f(); clearTimeout(this._t); }
  willUpdate(changed) {
    if (changed.has('tile') && this.tile) { this._q = this.tile; this._chips = new Set(); this._org = ''; }
  }
  _soon() { clearTimeout(this._t); this._t = setTimeout(() => this._load(), 250); }

  async _load() {
    const r = await limitsCall(this._fetch, 'GET', '/limits');
    if (!r.ok) { this._loadErr = r.error; return; }
    this._data = r.data ?? {}; this._loadErr = '';
    if (this.ceilings && this._data.canSetCeilings) {
      const o = await limitsCall(this._fetch, 'GET', '/orgs');
      if (o.ok) this._orgs = o.data?.orgs ?? [];
    }
    this.dispatchEvent(new CustomEvent('limits-loaded', { detail: this._data }));
  }

  // one write; the answer's row replaces the tile's
  async _call(method, path, body, key) {
    this._busy = key; this._err = '';
    const r = await limitsCall(this._fetch, method, path, body);
    this._busy = null;
    if (!r.ok) { this._err = `${key.split('|')[0]}: ${r.error}`; return false; }
    this._edit = null; this._revert = null;
    if (r.data?.tile) this._put(r.data.tile); else this._soon();
    return true;
  }
  _put(row) {
    if (!this._data) return;
    this._data = { ...this._data, tiles: (this._data.tiles ?? []).map((t) => (t.tile === row.tile ? row : t)) };
  }

  _toggle(c) { const s = new Set(this._chips); s.has(c) ? s.delete(c) : s.add(c); this._chips = s; }

  _bar(rows, shown) {
    const n = counts(rows);
    const orgs = [...new Set(rows.map((r) => r.org).filter(Boolean))].sort();
    const chip = (id, label) => html`<button class="chip" data-chip=${id} aria-pressed=${this._chips.has(id) ? 'true' : 'false'}
      @click=${() => this._toggle(id)}>${label}<span class="count">${n[id]}</span></button>`;
    return html`<div class="bar">
      <input type="search" placeholder="filter by tile or org" aria-label="filter by tile or org" .value=${this._q} @input=${(e) => { this._q = e.target.value; }}>
      ${chip('raised', 'raised')}${chip('pending', 'pending')}${chip('cap', 'at its limit')}
      ${orgs.length ? html`<select aria-label="org" @change=${(e) => { this._org = e.target.value; }}>
        <option value="" ?selected=${!this._org}>every org</option>
        ${orgs.map((o) => html`<option value=${o} ?selected=${this._org === o}>org ${o}</option>`)}
      </select>` : nothing}
      <span class="count">${shown} of ${rows.reduce((a, r) => a + Object.keys(r.limits ?? {}).length, 0)}</span>
    </div>`;
  }

  _entry(e) {
    const { row, kind: k, l } = e, key = `${row.tile}|${k}`, busy = this._busy === key, fl = flags(l), applies = appliesText(l, k);
    const focus = this.tile && row.tile === this.tile;
    const x = { tile: row.tile };
    return html`
      <tr class="row ${e.first ? '' : 'cont'} ${focus ? 'focus' : ''}" data-tile=${row.tile} data-kind=${k}>
        <td class="tile">${e.first ? html`<span class="mono">${row.tile}</span>${row.org ? html` <span class="badge">org ${row.org}</span>` : nothing}` : nothing}</td>
        <td class="k">${meta(k).label}</td>
        <td class="eff" data-l="in force">${fmt(k, l.effective)}${l.usage != null ? html`<div class="meta">uses ${fmt(k, l.usage)}</div>` : nothing}</td>
        <td class="meta" data-l="default">${fmt(k, l.default)}</td>
        <td data-l=${l.set || l.approved ? 'decided' : ''} class=${l.set || l.approved ? '' : 'blank'}>
          ${l.set ? html`<div>${decisionText(l.set, k)}</div>` : nothing}
          ${l.approved ? html`<div class=${l.dormant ? 'meta' : ''}>${decisionText(l.approved, k)}${l.dormant ? ' (dormant)' : ''}</div>` : nothing}</td>
        <td>
          ${fl.pending ? html`<span class="badge info" title=${l.canDecide ? '' : `ask ${askWho(l.approvers)}`}>asks ${fmt(k, l.ask)}</span>` : nothing}
          ${fl.cap ? html`<span class="badge warn">at its limit</span>` : nothing}
          ${applies ? html`<span class="badge" title=${applies}>next start</span>` : nothing}
          ${l.enforced === false ? html`<span class="badge">not enforced</span>` : nothing}
          ${l.note ? html`<div class="meta">${l.note}</div>` : nothing}
          ${fl.pending && l.askBlocked ? html`<div class="meta" data-blocked>${l.askBlocked}</div>` : nothing}</td>
        <td class="acts">
          ${fl.pending && l.canDecide ? html`<button class="small primary" data-approve ?disabled=${busy || !!l.askBlocked} title=${l.askBlocked || ''}
            @click=${() => this._call('POST', '/limits', raiseBody(x, k, l.ask, 'manifest-ask'), key)}>Approve ${fmt(k, l.ask)}</button>` : nothing}
          ${l.canDecide ? html`<button class="small" data-edit ?disabled=${busy} @click=${() => { this._edit = key; this._revert = null; }}>set…</button>` : nothing}
          ${l.canRevert && (l.set || l.approved) ? html`<button class="small quiet" data-revert ?disabled=${busy}
            title=${`back to the default (${fmt(k, l.default)})`} @click=${() => { this._revert = key; this._edit = null; }}>revert</button>` : nothing}
        </td>
      </tr>
      ${l.hit ? html`<tr class="sub"><td class="blank"></td><td colspan="6"><div class="hit" data-hit>
        <div>at its limit: ${hitText(l.hit, k)}</div>
        ${l.canDecide ? html`${choicesTpl(l.choices?.length ? l.choices : [25, 50, 100].map((pct) => ({ pct, value: step(k, l.effective, pct), fits: true, approvable: true, default: pct === 50 })),
          k, (c) => this._call('POST', '/limits', raiseBody(x, k, c.value, 'raise'), key), { busy })}
          <button class="quiet small" ?disabled=${busy} title="clear this hit for everyone — it shows again at the next one"
            @click=${() => this._call('POST', `/limits/dismiss?tile=${q(row.tile)}&kind=${q(k)}`, undefined, key)}>dismiss</button>
          ${whysTpl(l.choices, k)}` : html`<div class="ask">ask ${askWho(l.approvers)} to raise it</div>`}
      </div></td></tr>` : nothing}
      ${this._revert === key ? html`<tr class="sub"><td colspan="7"><div class="confirm">${row.tile}: ${meta(k).label} back to the default, ${fmt(k, l.default)}?
        <button class="small primary" data-confirm-revert ?disabled=${busy}
          @click=${() => this._call('DELETE', `/limits?tile=${q(row.tile)}&kind=${q(k)}`, undefined, key)}>Revert</button>
        <button class="small quiet" @click=${() => { this._revert = null; }}>Cancel</button></div></td></tr>` : nothing}
      ${this._edit === key ? html`<tr class="sub"><td colspan="7"><bx-limit-edit .tile=${row.tile} .kind=${k} .lim=${l} .fetcher=${this.fetcher}
        @limit-saved=${(ev) => { if (ev.detail?.tile) this._put(ev.detail.tile); this._edit = null; }}
        @limit-cancel=${() => { this._edit = null; }}></bx-limit-edit></td></tr>` : nothing}`;
  }

  _ceilingsTpl() {
    const d = this._data ?? {};
    if (!this.ceilings) return nothing;
    if (d.canSetCeilings) {
      const orgs = this._orgs ?? Object.keys(d.orgCeilings ?? {}).map((id) => ({ id, limits: d.orgCeilings[id] }));
      return html`<h3>Org ceilings</h3>
        <div class="meta">The most an org’s admins may approve for each of its tiles; workspace admins are bound only by the workspace.</div>
        ${orgs.length ? orgs.map((o) => { const lim = d.orgCeilings?.[o.id] ?? o.limits ?? {}; return html`<details class="org" data-org=${o.id}>
          <summary><span class="mono">org ${o.id}</span><span class="meta">${KINDS.filter((k) => lim[k]).map((k) => `${meta(k).label} ${fmt(k, lim[k])}`).join(' · ') || 'no ceilings'}</span></summary>
          <bx-org-limits org=${o.id} .limits=${lim} .defaults=${d.defaults} .fetcher=${this.fetcher} @org-limits-saved=${() => this._soon()}></bx-org-limits>
        </details>`; }) : html`<div class="empty">no organisations</div>`}`;
    }
    const mine = Object.entries(d.orgCeilings ?? {});
    if (!mine.length) return nothing;
    return html`<h3>Your orgs’ ceilings</h3>${mine.map(([id, lim]) => html`<div class="org"><span class="mono">org ${id}</span>
      <bx-org-limits org=${id} .limits=${lim} readonly></bx-org-limits></div>`)}`;
  }

  render() {
    if (this._loadErr && !this._data) return html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._loadErr}</span></div>`;
    if (!this._data) return html`<div class="empty">…</div>`;
    const rows = this._data.tiles ?? [];
    const entries = tableEntries(rows, { q: this._q, org: this._org, raised: this._chips.has('raised'), pending: this._chips.has('pending'), cap: this._chips.has('cap') });
    return html`
      ${this._bar(rows, entries.length)}
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      ${entries.length ? html`<table>
        <thead><tr><th>tile</th><th>limit</th><th>in force</th><th>default</th><th>decision</th><th>state</th><th></th></tr></thead>
        <tbody>${entries.map((e) => this._entry(e))}</tbody>
      </table>` : html`<div class="empty">${rows.length ? 'Nothing matches the filters.' : 'No tile has limits you can see.'}</div>`}
      ${this._ceilingsTpl()}`;
  }

  testApi() {
    const s = this;
    return {
      get data() { return s._data; },
      get rows() { return s._data?.tiles ?? []; },
      get error() { return s._err || s._loadErr || ''; },
      setFilter: ({ q: qq, chips, org } = {}) => { if (qq != null) s._q = qq; if (chips) s._chips = new Set(chips); if (org != null) s._org = org; },
      edit: (tile, kind) => { s._edit = `${tile}|${kind}`; },
      reload: () => s._load(),
    };
  }
}
customElements.define('bx-limits-table', BxLimitsTable);
