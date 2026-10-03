// crm.js — the CRM page: a pipeline board (drag a card to move its stage),
// the accounts table, the activity feed, and an account drawer.
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, sendJSON, money, num, ago, dateShort, inDays, initials, hue, baseCss } from './ui.js';

const BOARD = ['lead', 'qualified', 'demo', 'proposal', 'negotiation'];
const STAGE_COLOR = { lead: 'var(--bx-muted)', qualified: 'var(--blue)', demo: 'var(--teal)', proposal: 'var(--violet)', negotiation: 'var(--bx-accent)', won: 'var(--ok)', lost: 'var(--bad)' };
const HEALTH = { good: ['ok', 'Healthy'], watch: ['warn', 'Watch'], risk: ['bad', 'At risk'] };
const TYPE_ICON = { call: '☏', email: '✉', meeting: '◷', note: '✎', stage: '➜' };

const av = (id, name, big) => html`<span class="av ${big ? 'lg' : ''}" style="--h:${hue(id)}" title=${name || id}>${initials(name || id)}</span>`;

class LarkspanCrm extends LitElement {
  static properties = {
    _view: { state: true }, _sum: { state: true }, _deals: { state: true }, _accounts: { state: true },
    _activity: { state: true }, _open: { state: true }, _detail: { state: true }, _q: { state: true },
    _me: { state: true }, _err: { state: true }, _drag: { state: true }, _over: { state: true },
  };

  static styles = [scrollCss, baseCss, css`
    .app { display: grid; grid-template-rows: auto auto 1fr; height: 100%; min-width: 0; container-type: inline-size; }
    header { display: flex; align-items: center; gap: 14px; padding: 10px 16px 0; border-bottom: 1px solid var(--bx-border); }
    .brand { display: flex; align-items: center; gap: 8px; font-weight: 650; font-size: 14px; padding-bottom: 9px; }
    .brand i { width: 18px; height: 18px; border-radius: 5px; background: linear-gradient(135deg, #12857a, #0c5f58); display: grid; place-items: center;
               font-style: normal; font-size: 10px; color: #f7c25c; }
    nav { display: flex; gap: 2px; align-self: stretch; }
    nav button { background: none; border: 0; border-bottom: 2px solid transparent; padding: 0 10px 9px; color: var(--bx-muted); font-size: 12.5px; }
    nav button[aria-current='true'] { color: var(--bx-text); border-bottom-color: var(--bx-accent); }
    .sp { flex: 1; }
    .search { margin-bottom: 8px; background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 7px; color: inherit;
              padding: 5px 10px; font: inherit; font-size: 12px; width: 190px; }
    .search:focus { outline: 2px solid color-mix(in srgb, var(--bx-accent) 40%, transparent); }
    .kpis { display: flex; gap: 0; padding: 10px 16px; border-bottom: 1px solid var(--bx-border); background: color-mix(in srgb, var(--bx-bg) 35%, var(--bx-panel)); overflow-x: auto; }
    .kpi { padding: 0 18px 0 0; margin-right: 18px; border-right: 1px solid var(--bx-border); white-space: nowrap; }
    .kpi:last-child { border-right: 0; }
    .kpi b { display: block; font-size: 17px; font-weight: 650; letter-spacing: -.01em; margin-top: 1px; }
    .kpi small { color: var(--bx-muted); font-size: 11px; margin-left: 4px; font-weight: 400; }
    /* a narrow window keeps the three headline numbers rather than a cut row */
    @container (max-width: 760px) { .kpi.more { display: none; } .kpi.arr { border-right: 0; margin-right: 0; } }
    main { overflow: auto; min-height: 0; position: relative; }

    /* pipeline board */
    .board { display: grid; grid-template-columns: repeat(5, minmax(150px, 1fr)); gap: 10px; padding: 12px 14px 16px; min-width: 780px; }
    .col { background: color-mix(in srgb, var(--bx-bg) 55%, var(--bx-panel)); border: 1px solid var(--bx-border); border-radius: 9px; padding: 8px; min-height: 120px;
           border-top: 3px solid var(--sc); transition: background .12s; }
    .col.over { background: color-mix(in srgb, var(--sc) 12%, var(--bx-panel)); }
    .colh { display: flex; align-items: baseline; gap: 6px; padding: 1px 3px 8px; }
    .colh b { font-size: 12px; }
    .colh .n { font-size: 11px; color: var(--bx-muted); }
    .colh .amt { margin-left: auto; font-size: 11px; color: var(--bx-muted); }
    .card { background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: 7px; padding: 8px 9px; margin-bottom: 7px; cursor: pointer;
            box-shadow: 0 1px 2px rgba(0,0,0,.25); transition: border-color .12s, transform .12s; }
    .card:hover { border-color: color-mix(in srgb, var(--sc) 60%, var(--bx-border)); }
    .card.dragging { opacity: .45; }
    .card .acc { font-weight: 600; font-size: 12.3px; line-height: 1.3; }
    .card .dn { color: var(--bx-muted); font-size: 11.3px; line-height: 1.35; margin: 2px 0 7px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
    .card .foot { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
    .card .amt { font-weight: 650; font-size: 12.5px; }
    .card .when { font-size: 10.8px; color: var(--bx-muted); margin-top: 3px; }
    .card .when.soon { color: var(--bx-accent); }
    .closed { display: flex; gap: 8px; padding: 0 14px 14px; flex-wrap: wrap; }
    .closed .chip { font-size: 11px; padding: 3px 9px; }
    /* (after the rules it narrows) a phone: the numbers two to a row, the stages one under another */
    @container (max-width: 560px) {
      .kpis { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 14px; overflow: visible; }
      .kpi { border-right: 0; margin-right: 0; padding-right: 0; }
      .board { grid-template-columns: minmax(0, 1fr); min-width: 0; }
      .col { min-height: 0; }
      .search { display: none; }
    }

    /* accounts */
    table { width: 100%; border-collapse: collapse; font-size: 12.3px; }
    th { text-align: left; font-weight: 600; font-size: 10.5px; letter-spacing: .06em; text-transform: uppercase; color: var(--bx-muted);
         padding: 9px 10px; border-bottom: 1px solid var(--bx-border); position: sticky; top: 0; background: var(--bx-panel); z-index: 1; }
    td { padding: 8px 10px; border-bottom: 1px solid color-mix(in srgb, var(--bx-border) 60%, transparent); vertical-align: middle; }
    tr.row { cursor: pointer; }
    tr.row:hover td { background: var(--bx-panel-2); }
    td .nm { font-weight: 600; }
    td .sub { color: var(--bx-muted); font-size: 11px; }
    td.r, th.r { text-align: right; }
    .who { display: inline-flex; align-items: center; gap: 6px; }

    /* activity */
    .feed { padding: 6px 16px 16px; max-width: 900px; }
    .ev { display: grid; grid-template-columns: 30px 1fr auto; gap: 10px; padding: 10px 0; border-bottom: 1px solid color-mix(in srgb, var(--bx-border) 60%, transparent); }
    .ev .t { font-size: 12.3px; line-height: 1.45; }
    .ev .meta { font-size: 11px; color: var(--bx-muted); margin-bottom: 2px; }
    .ev .meta b { color: var(--bx-text); font-weight: 600; }
    .ev .ic { display: inline-grid; place-items: center; width: 16px; height: 16px; border-radius: 4px; font-size: 10px; background: var(--bx-panel-2); color: var(--bx-muted); margin-right: 4px; }
    .ev .ago { font-size: 11px; color: var(--bx-muted); white-space: nowrap; }
    .ev .acc { cursor: pointer; }
    .ev .acc:hover { text-decoration: underline; }

    /* drawer */
    .scrim { position: absolute; inset: 0; background: rgba(0,0,0,.28); z-index: 5; }
    aside { position: absolute; top: 0; right: 0; bottom: 0; width: min(440px, 92%); z-index: 6; background: var(--bx-panel); border-left: 1px solid var(--bx-border);
            box-shadow: -10px 0 30px rgba(0,0,0,.35); overflow: auto; padding: 16px 18px 20px; }
    aside h2 { margin: 0; font-size: 16px; }
    aside .x { position: absolute; top: 12px; right: 12px; background: none; border: 0; color: var(--bx-muted); font-size: 18px; }
    aside .line { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 6px 0 10px; }
    aside p.about { margin: 0 0 12px; color: color-mix(in srgb, var(--bx-text) 85%, var(--bx-muted)); line-height: 1.5; font-size: 12.3px; }
    .facts { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin-bottom: 14px; }
    .fact { background: var(--bx-panel-2); border-radius: 7px; padding: 7px 9px; }
    .fact b { display: block; font-size: 13.5px; margin-top: 2px; }
    aside section { margin-top: 14px; }
    aside .label { margin-bottom: 6px; display: block; }
    .contact { display: grid; grid-template-columns: 30px 1fr; gap: 9px; padding: 6px 0; align-items: center; }
    .contact .nm { font-weight: 600; font-size: 12.3px; }
    .contact .sub { font-size: 11px; color: var(--bx-muted); }
    .deal { border: 1px solid var(--bx-border); border-left: 3px solid var(--sc); border-radius: 7px; padding: 7px 10px; margin-bottom: 6px; }
    .deal .top { display: flex; gap: 8px; align-items: baseline; }
    .deal .top b { font-size: 12.3px; }
    .deal .top .amt { margin-left: auto; font-weight: 650; }
    .deal .nx { font-size: 11.3px; color: var(--bx-muted); margin-top: 3px; }
  `];

  constructor() {
    super();
    this._view = (location.hash.slice(1) || 'pipeline');
    this._q = '';
    this._accounts = []; this._deals = []; this._activity = [];
  }

  connectedCallback() {
    super.connectedCallback();
    this._load();
    this._timer = setInterval(() => this._load(true), 15000);
  }
  disconnectedCallback() { super.disconnectedCallback(); clearInterval(this._timer); }

  async _load(quiet) {
    try {
      const [sum, deals, accounts, activity, me] = await Promise.all([
        getJSON('/summary'), getJSON('/deals'), getJSON('/accounts'), getJSON('/activity?limit=60'), this._me ? this._me : getJSON('/me')]);
      this._sum = sum; this._deals = deals.deals; this._accounts = accounts.accounts; this._activity = activity.activity; this._me = me;
      this._err = '';
      if (this._open && !quiet) this._detail = await getJSON(`/accounts/${this._open}`);
    } catch (e) {
      if (!quiet) this._err = `Couldn't reach the CRM: ${e.message}`;
    }
  }

  _go(v) { this._view = v; history.replaceState(null, '', '#' + v); }
  async _openAccount(id) {
    this._open = id; this._detail = null;
    try { this._detail = await getJSON(`/accounts/${id}`); } catch (e) { this._err = e.message; }
  }
  _close() { this._open = null; this._detail = null; }

  _match(...vals) { const q = this._q.trim().toLowerCase(); return !q || vals.some((v) => String(v ?? '').toLowerCase().includes(q)); }

  render() {
    const s = this._sum;
    return html`<div class="app">
      <header>
        <div class="brand"><i>◆</i>Customers</div>
        <nav>
          ${[['pipeline', 'Pipeline'], ['accounts', 'Accounts'], ['activity', 'Activity']].map(([id, label]) => html`
            <button aria-current=${this._view === id ? 'true' : 'false'} @click=${() => this._go(id)}>${label}</button>`)}
        </nav>
        <span class="sp"></span>
        <input class="search" type="search" placeholder="Search accounts and deals" .value=${this._q} @input=${(e) => { this._q = e.target.value; }}>
      </header>
      <div class="kpis">
        ${s ? html`
          <div class="kpi"><span class="label">Open pipeline</span><b class="num">${money(s.pipeline, true)}<small>${s.openDeals} deals</small></b></div>
          <div class="kpi"><span class="label">Weighted forecast</span><b class="num">${money(s.weighted, true)}</b></div>
          <div class="kpi more"><span class="label">Won, last 30 days</span><b class="num">${money(s.won30, true)}</b></div>
          <div class="kpi arr"><span class="label">ARR</span><b class="num">${money(s.arr, true)}<small>${s.customers} customers</small></b></div>
          <div class="kpi more"><span class="label">Win rate</span><b class="num">${s.winRate == null ? '—' : s.winRate + '%'}</b></div>` : html`<span class="muted">Loading…</span>`}
      </div>
      <main>
        ${this._err ? html`<div class="err">${this._err}</div>` : nothing}
        ${this._view === 'accounts' ? this._renderAccounts() : this._view === 'activity' ? this._renderActivity() : this._renderBoard()}
        ${this._open ? html`<div class="scrim" @click=${this._close}></div>${this._renderDrawer()}` : nothing}
      </main>
    </div>`;
  }

  // ---- pipeline ----
  _renderBoard() {
    const deals = this._deals.filter((d) => this._match(d.accountName, d.name, d.ownerName));
    const won = deals.filter((d) => d.stage === 'won'), lost = deals.filter((d) => d.stage === 'lost');
    return html`
      <div class="board">
        ${BOARD.map((st) => {
          const ds = deals.filter((d) => d.stage === st).sort((a, b) => a.close - b.close);
          const amt = ds.reduce((t, d) => t + d.amount, 0);
          return html`<div class="col ${this._over === st ? 'over' : ''}" style="--sc:${STAGE_COLOR[st]}"
              @dragover=${(e) => { if (this._drag) { e.preventDefault(); this._over = st; } }}
              @dragleave=${() => { if (this._over === st) this._over = null; }}
              @drop=${(e) => this._drop(e, st)}>
            <div class="colh"><b>${this._stageName(st)}</b><span class="n">${ds.length}</span><span class="amt num">${money(amt, true)}</span></div>
            ${ds.map((d) => this._card(d))}
          </div>`;
        })}
      </div>
      <div class="closed">
        ${won.map((d) => html`<span class="chip ok" style="cursor:pointer" @click=${() => this._openAccount(d.account)}>✓ Won · ${d.accountName} · ${money(d.amount)} · ${dateShort(d.close)}</span>`)}
        ${lost.map((d) => html`<span class="chip bad" style="cursor:pointer" @click=${() => this._openAccount(d.account)}>✕ Lost · ${d.accountName} · ${money(d.amount)}</span>`)}
      </div>`;
  }
  _stageName(st) { return { lead: 'Lead', qualified: 'Qualified', demo: 'Demo', proposal: 'Proposal', negotiation: 'Negotiation' }[st]; }
  _card(d) {
    const days = Math.round((d.close - Date.now()) / 864e5);
    return html`<div class="card ${this._drag === d.id ? 'dragging' : ''}" style="--sc:${STAGE_COLOR[d.stage]}" draggable=${this._me?.canWrite ? 'true' : 'false'}
        @dragstart=${(e) => { this._drag = d.id; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', d.id); }}
        @dragend=${() => { this._drag = null; this._over = null; }}
        @click=${() => this._openAccount(d.account)}>
      <div class="acc">${d.accountName}</div>
      <div class="dn">${d.name}</div>
      <div class="foot">
        <span class="amt num">${money(d.amount)}</span>
        ${av(d.owner, d.ownerName)}
      </div>
      <div class="when ${days >= 0 && days <= 14 ? 'soon' : ''}" title="expected close">${days >= 0 ? `Closes ${inDays(d.close)}` : `Was due ${dateShort(d.close)}`}</div>
    </div>`;
  }
  async _drop(e, stage) {
    e.preventDefault();
    const id = this._drag; this._drag = null; this._over = null;
    const d = this._deals.find((x) => x.id === id);
    if (!d || d.stage === stage) return;
    d.stage = stage; this.requestUpdate();
    try { await sendJSON(`/deals/${id}`, 'PATCH', { stage }); } catch (err) { this._err = err.message; }
    this._load(true);
  }

  // ---- accounts ----
  _renderAccounts() {
    const rows = this._accounts.filter((a) => this._match(a.name, a.city, a.industry, a.domain))
      .sort((a, b) => (b.arr || 0) - (a.arr || 0) || (b.openAmount - a.openAmount));
    const status = (a) => a.status === 'customer' ? html`<span class="chip">${a.plan}</span>`
      : a.status === 'lost' ? html`<span class="chip bad">Lost</span>` : html`<span class="chip" style="border-style:dashed">Prospect</span>`;
    return html`<table>
      <thead><tr><th>Account</th><th>Plan</th><th class="r">Vans</th><th class="r">ARR</th><th class="r">Open pipeline</th><th>Health</th><th>Owner</th><th>Renewal</th><th>Last touch</th></tr></thead>
      <tbody>${rows.map((a) => html`<tr class="row" @click=${() => this._openAccount(a.id)}>
        <td><div class="nm">${a.name}</div><div class="sub">${a.industry} · ${a.city}</div></td>
        <td>${status(a)}</td>
        <td class="r num">${num(a.vans)}</td>
        <td class="r num">${a.arr ? money(a.arr) : html`<span class="muted">—</span>`}</td>
        <td class="r num">${a.openAmount ? money(a.openAmount) : html`<span class="muted">—</span>`}</td>
        <td>${HEALTH[a.health] ? html`<span class="chip ${HEALTH[a.health][0]}"><span class="dot ${HEALTH[a.health][0]}"></span>${HEALTH[a.health][1]}</span>` : html`<span class="muted">—</span>`}</td>
        <td><span class="who">${av(a.owner, a.ownerName)}<span>${(a.ownerName || '').split(' ')[0]}</span></span></td>
        <td class="num">${a.renewal ? html`${dateShort(a.renewal)}` : html`<span class="muted">—</span>`}</td>
        <td class="muted">${ago(a.lastActivity)}</td>
      </tr>`)}</tbody>
    </table>`;
  }

  // ---- activity ----
  _renderActivity() {
    const evs = this._activity.filter((x) => this._match(x.accountName, x.text, x.whoName));
    return html`<div class="feed">${evs.map((x) => this._event(x, true))}</div>`;
  }
  _event(x, withAccount) {
    return html`<div class="ev">
      ${av(x.who, x.whoName, true)}
      <div>
        <div class="meta"><span class="ic">${TYPE_ICON[x.type] ?? '•'}</span><b>${x.whoName}</b>
          ${withAccount ? html` · <span class="acc" @click=${() => this._openAccount(x.account)}>${x.accountName}</span>` : nothing}</div>
        <div class="t">${x.text}</div>
      </div>
      <span class="ago">${ago(x.at)}</span>
    </div>`;
  }

  // ---- account drawer ----
  _renderDrawer() {
    const d = this._detail;
    if (!d) return html`<aside><span class="muted">Loading…</span></aside>`;
    const a = d.account;
    const h = HEALTH[a.health];
    return html`<aside>
      <button class="x" title="Close" @click=${this._close}>×</button>
      <h2>${a.name}</h2>
      <div class="line">
        <span class="chip">${a.status === 'customer' ? a.plan : a.status === 'lost' ? 'Lost' : 'Prospect'}</span>
        ${h ? html`<span class="chip ${h[0]}"><span class="dot ${h[0]}"></span>${h[1]}</span>` : nothing}
        <span class="muted">${a.industry} · ${a.city}${a.since ? ` · customer since ${new Date(a.since).toLocaleDateString('en-US', { month: 'short', year: 'numeric' })}` : ''}</span>
      </div>
      <p class="about">${a.about}</p>
      <div class="facts">
        <div class="fact"><span class="label">Vans</span><b class="num">${num(a.vans)}</b></div>
        <div class="fact"><span class="label">Depots</span><b class="num">${num(a.depots)}</b></div>
        <div class="fact"><span class="label">ARR</span><b class="num">${a.arr ? money(a.arr, true) : '—'}</b></div>
        <div class="fact"><span class="label">Renewal</span><b>${a.renewal ? dateShort(a.renewal) : '—'}</b></div>
      </div>
      <div class="line"><span class="who">${av(a.owner, a.ownerName)} <span>${a.ownerName}</span></span><span class="muted">owner</span>
        ${a.csmName ? html`<span class="who" style="margin-left:10px">${av(a.csm, a.csmName)} <span>${a.csmName}</span></span><span class="muted">success</span>` : nothing}</div>
      ${d.deals.length ? html`<section><span class="label">Deals</span>
        ${d.deals.map((x) => html`<div class="deal" style="--sc:${STAGE_COLOR[x.stage]}">
          <div class="top"><b>${x.name}</b><span class="amt num">${money(x.amount)}</span></div>
          <div class="nx">${x.stageName} · ${x.stage === 'won' || x.stage === 'lost' ? dateShort(x.close) : `closes ${inDays(x.close)}`}${x.next ? html` · ${x.next}` : nothing}</div>
        </div>`)}</section>` : nothing}
      ${d.contacts.length ? html`<section><span class="label">People</span>
        ${d.contacts.map((c) => html`<div class="contact">${av(c.email, c.name, true)}<div>
          <div class="nm">${c.name}${c.primary ? html` <span class="chip" style="margin-left:4px">primary</span>` : nothing}</div>
          <div class="sub">${c.title} · ${c.phone}</div></div></div>`)}</section>` : nothing}
      ${d.activity.length ? html`<section><span class="label">Activity</span>${d.activity.map((x) => this._event(x, false))}</section>` : nothing}
    </aside>`;
  }
}

customElements.define('larkspan-crm', LarkspanCrm);
