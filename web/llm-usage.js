/**
 * llm-usage.js — what was used through the LLM connector (D226,
 * docs/llm.md §Usage; GET /llm/usage):
 *
 *   <bx-llm-usage .client [own]>  this month's counters, summed per person
 *       and per component (a workspace admin: every row), for today and
 *       for the month — tokens, cost, requests; a click on a column header
 *       sorts by it. Calls a tile made for nobody are a row of their own
 *       ("no person"). `own`: the viewer's own rows, per tile and per model.
 *   <bx-llm-me .client>  what a person who is not an admin sees on
 *       /xbin/llm: their own quota — a plain sentence when a limit is
 *       reached, each limit's bar — and their own usage. Nothing of the
 *       connector's settings, and nobody else's numbers.
 *
 * Days and months are UTC, as the quotas' windows are. Only counts exist:
 * no prompt and no answer is kept anywhere.
 */
import { LitElement, html, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { limitsCss } from '/vendor/bx-limits.js';
import { llmCss, errTpl } from '/vendor/llm-css.js';
import { limitsTpl } from '/vendor/llm-quotas.js';
import { aggregate, totalsOf, sortGroups, USAGE_COLS, fmtTokens, costWords, tokensTitle, quotaMessage, today } from '/vendor/llm-kit.js';

export class BxLlmUsage extends LitElement {
  static properties = {
    client: { attribute: false },
    own: { type: Boolean },      // the viewer's own rows only
    _rows: { state: true },      // GET /llm/usage's rows (per UTC day, this month)
    _sort: { state: true },      // table id → [column, direction]
    _err: { state: true }, _busy: { state: true },
  };
  static styles = [limitsCss, llmCss];
  constructor() { super(); this._rows = null; this._sort = {}; this._err = ''; }
  connectedCallback() { super.connectedCallback(); this.reload(); }
  async reload() {
    this._busy = true;
    const r = await this.client.usage({ period: 'day' });
    this._busy = false;
    if (!r.ok) { this._err = r.error; return; }
    this._rows = r.data?.rows ?? []; this._err = '';
  }
  _by(table, col) {
    const [c, d] = this._sort[table] ?? ['monthTokens', 'desc'];
    const dir = c === col ? (d === 'desc' ? 'asc' : 'desc') : (col === 'label' ? 'asc' : 'desc');
    this._sort = { ...this._sort, [table]: [col, dir] };
  }

  _table(id, by, first) {
    const groups = aggregate(this._rows, by, today());
    if (!groups.length) return html`<div class="empty" data-usage-empty=${id}>Nothing yet this month.</div>`;
    const [col, dir] = this._sort[id] ?? ['monthTokens', 'desc'];
    const rows = sortGroups(groups, col, dir), t = totalsOf(groups);
    const cells = (g) => html`
      <td class="num" data-l="tokens today" title=${tokensTitle(g.today)}>${fmtTokens(g.today.tokens)}</td>
      <td class="num" data-l="cost today">${costWords(g.today)}</td>
      <td class="num" data-l="tokens this month" title=${tokensTitle(g.month)}>${fmtTokens(g.month.tokens)}</td>
      <td class="num" data-l="cost this month">${costWords(g.month)}</td>
      <td class="num" data-l="requests">${g.month.requests}${g.month.failed ? html` <span class="meta">(${g.month.failed} failed)</span>` : nothing}</td>`;
    return html`<table data-usage=${id}>
      <thead><tr>${USAGE_COLS.map(([c, head]) => html`<th class=${c === 'label' ? '' : 'num'} aria-sort=${c === col ? (dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
        <button class="sort" data-sort=${c} title="sort by this column" @click=${() => this._by(id, c)}>${head || first}${c === col
          ? html`<bx-icon name=${dir === 'asc' ? 'chevron-up' : 'chevron-down'}></bx-icon>` : nothing}</button></th>`)}</tr></thead>
      <tbody>${rows.map((g) => html`<tr data-row=${g.key}><td class=${g.key === '' ? 'muted' : 'mono'}>${g.label}</td>${cells(g)}</tr>`)}</tbody>
      ${rows.length > 1 ? html`<tfoot><tr data-total><td>total</td>${cells(t)}</tr></tfoot>` : nothing}
    </table>`;
  }

  render() {
    if (!this._rows) return this._err ? errTpl(this._err) : html`<div class="empty">…</div>`;
    return html`
      ${errTpl(this._err)}
      ${this.own ? html`<h4>By tile</h4>${this._table('component', 'component', 'tile')}<h4>By model</h4>${this._table('model', 'model', 'model')}`
        : html`<h4>By person</h4>${this._table('user', 'user', 'person')}<h4>By component</h4>${this._table('component', 'component', 'component')}`}
      <div class="line"><button class="small" data-refresh ?disabled=${this._busy} @click=${() => this.reload()}><bx-icon name="refresh"></bx-icon> Refresh</button>
        <span class="meta">This month so far; days and months are UTC. Tokens are input plus output; cost covers the models that have a price.</span></div>`;
  }
  testApi() { const s = this; return { get rows() { return s._rows; }, reload: () => s.reload(), sort: (table) => s._sort[table] ?? ['monthTokens', 'desc'] }; }
}
customElements.define('bx-llm-usage', BxLlmUsage);

export class BxLlmMe extends LitElement {
  static properties = {
    client: { attribute: false },
    _st: { state: true },   // GET /llm
    _err: { state: true },
  };
  static styles = [limitsCss, llmCss];
  constructor() { super(); this._st = null; this._err = ''; }
  connectedCallback() { super.connectedCallback(); this._load(); }
  async _load() {
    const r = await this.client.status();
    if (!r.ok) { this._err = r.error; return; }
    this._st = r.data ?? {}; this._err = '';
  }
  render() {
    const st = this._st;
    if (!st) return this._err ? errTpl(this._err) : html`<div class="empty">…</div>`;
    const q = st.quota, msg = quotaMessage(q);
    const mine = q?.limits ?? [], ws = q?.workspace ?? [];
    return html`<div data-me=${st.user ?? ''}>
      ${!st.configured ? html`<div class="note" data-unconfigured><bx-icon name="info"></bx-icon><span>This workspace has no LLM set up. A workspace admin sets one up on this page.</span></div>`
        : !st.enabled ? html`<div class="note"><bx-icon name="power"></bx-icon><span>This workspace’s LLM connector is switched off.</span></div>` : nothing}
      ${msg ? html`<div class="note ${msg.level === 'stop' ? 'crit' : 'warn'}" role="status" data-quota-message=${msg.level}>
        <bx-icon name=${msg.level === 'stop' ? 'error' : 'warning'}></bx-icon><span>${msg.text}</span></div>` : nothing}
      <h3>Your quota</h3>
      ${mine.length || ws.length ? html`${limitsTpl(mine)}${limitsTpl(ws)}`
        : html`<div class="empty" data-no-quota>No limit applies to you.</div>`}
      <h3>Your usage</h3>
      <div class="hint">What the workspace’s models were used for on your behalf — by you directly and by the tiles acting for you. Only counts are kept, never a prompt or an answer.</div>
      <bx-llm-usage own .client=${this.client}></bx-llm-usage>
    </div>`;
  }
  testApi() { const s = this; return { get status() { return s._st; } }; }
}
customElements.define('bx-llm-me', BxLlmMe);
