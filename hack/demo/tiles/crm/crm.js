// crm.js — the CRM page: a pipeline board (drag a card to move its stage),
// the accounts table, the activity feed, and an account drawer.
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, sendJSON, money, num, ago, dateShort, inDays, avatar, badge, baseCss } from './ui.js';

const BOARD = ['lead', 'qualified', 'demo', 'proposal', 'negotiation'];
const HEALTH = { good: ['ok', 'Healthy'], watch: ['warn', 'Watch'], risk: ['bad', 'At risk'] };
// what an activity was: its icon (bx-icons)
const TYPE_ICON = { call: 'call', email: 'mail', meeting: 'calendar', note: 'pencil', stage: 'arrow-right' };

class LarkspanCrm extends LitElement {
  static properties = {
    _view: { state: true }, _sum: { state: true }, _deals: { state: true }, _accounts: { state: true },
    _activity: { state: true }, _open: { state: true }, _detail: { state: true }, _q: { state: true },
    _me: { state: true }, _err: { state: true }, _drag: { state: true }, _over: { state: true },
  };

  static styles = [scrollCss, baseCss, css`
    .app { display: grid; grid-template-rows: auto auto 1fr; height: 100%; min-width: 0; container-type: inline-size; }
    header { display: flex; align-items: center; gap: 16px; padding: 0 16px; border-bottom: 1px solid var(--bx-border); }
    .brand { display: flex; align-items: center; gap: 8px; font: var(--bx-font-title); padding: 12px 0; }
    /* the app's mark: a diamond on an ink square, flat */
    .brand i { position: relative; flex: none; width: 20px; height: 20px; border-radius: var(--bx-radius); background: var(--bx-text); }
    .brand i::after { content: ''; position: absolute; inset: 6px; background: var(--bx-panel); transform: rotate(45deg); }
    nav { display: flex; gap: 4px; align-self: stretch; }
    nav button { background: none; border: 0; border-bottom: 2px solid transparent; padding: 2px 10px 0; color: var(--bx-muted); font-weight: 600; }
    nav button:hover { color: var(--bx-text); }
    nav button[aria-current='true'] { color: var(--bx-text); border-bottom-color: var(--bx-accent); }
    .sp { flex: 1; }
    .search { width: 210px; }
    .kpis { display: flex; padding: 12px 16px; border-bottom: 1px solid var(--bx-border); background: var(--bx-panel-2); overflow-x: auto; }
    .kpi { padding: 0 20px 0 0; margin-right: 20px; border-right: 1px solid var(--bx-border); white-space: nowrap; }
    .kpi:last-child { border-right: 0; }
    .kpi b { display: block; margin-top: 2px; font: var(--bx-font-heading); font-family: var(--bx-sans); font-variant-numeric: tabular-nums; }
    .kpi small { font: var(--bx-font-meta); color: var(--bx-muted); margin-left: 6px; }
    /* a narrow window keeps the three headline numbers rather than a cut row */
    @container (max-width: 760px) { .kpi.more { display: none; } .kpi.arr { border-right: 0; margin-right: 0; } }
    main { overflow: auto; min-height: 0; position: relative; }

    /* pipeline board: neutral columns under a text-coloured rule */
    .board { display: grid; grid-template-columns: repeat(5, minmax(150px, 1fr)); gap: 12px; padding: 12px 16px 16px; min-width: 780px; }
    .col { background: color-mix(in srgb, var(--bx-bg) 50%, var(--bx-panel)); border: 1px solid var(--bx-border); border-top: 2px solid var(--bx-text);
           border-radius: var(--bx-radius); padding: 8px; min-height: 120px; transition: background var(--bx-dur-ui) var(--bx-ease-out); }
    .col.over { background: var(--bx-selection); border-top-color: var(--bx-accent); }
    .colh { display: flex; align-items: baseline; gap: 6px; padding: 0 4px 8px; }
    .colh b { font-weight: 600; }
    .colh .n, .colh .amt { font: var(--bx-font-meta); font-variant-numeric: tabular-nums; color: var(--bx-muted); }
    .colh .amt { margin-left: auto; }
    .card { background: var(--bx-panel); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 10px; margin-bottom: 8px; cursor: pointer;
            box-shadow: var(--bx-shadow); transition: border-color var(--bx-dur-ui) var(--bx-ease-out); }
    .card:hover { border-color: var(--bx-border-strong); }
    .card.dragging { opacity: 0.45; }
    .card .acc { font-weight: 600; }
    .card .dn { color: var(--bx-muted); margin: 2px 0 8px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
    .card .foot { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
    .card .amt { font-weight: 600; }
    .card .when { font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 4px; }
    .card .when.soon { color: var(--bx-text); font-weight: 600; }
    .closed { display: flex; gap: 8px; padding: 0 16px 16px; flex-wrap: wrap; }
    .closed button { display: inline-flex; align-items: center; gap: 6px; min-height: var(--bx-control-h); padding: 2px 10px; background: var(--bx-panel);
                     border: 1px solid var(--bx-border); border-radius: var(--bx-radius); color: var(--bx-muted); font-variant-numeric: tabular-nums; }
    .closed button:hover { border-color: var(--bx-border-strong); }
    .closed button b, .closed button bx-icon { color: var(--st); font-weight: 600; }
    /* (after the rules it narrows) a phone: the numbers two to a row, the stages one under another */
    @container (max-width: 560px) {
      .kpis { display: grid; grid-template-columns: 1fr 1fr; gap: 12px 16px; overflow: visible; }
      .kpi { border-right: 0; margin-right: 0; padding-right: 0; }
      .board { grid-template-columns: minmax(0, 1fr); min-width: 0; }
      .col { min-height: 0; }
      .search { display: none; }
    }

    /* accounts */
    table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
    th { text-align: left; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted);
         padding: 10px 10px 8px; border-bottom: 2px solid var(--bx-text); position: sticky; top: 0; background: var(--bx-panel); z-index: 1; }
    td { height: 40px; padding: 4px 10px; border-bottom: 1px solid var(--bx-border); vertical-align: middle; }
    tr.row { cursor: pointer; }
    tr.row:hover td { background: var(--bx-hover); }
    td .nm { font-weight: 600; }
    td .sub { font: var(--bx-font-meta); color: var(--bx-muted); }
    td.r, th.r { text-align: right; }
    .badge.dashed { border-style: dashed; }
    .who { display: inline-flex; align-items: center; gap: 6px; }

    /* activity */
    .feed { padding: 4px 16px 16px; max-width: 900px; }
    .ev { display: grid; grid-template-columns: 32px 1fr auto; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--bx-border); }
    .ev .meta { display: flex; align-items: center; gap: 6px; color: var(--bx-muted); margin-bottom: 2px; }
    .ev .meta b { color: var(--bx-text); font-weight: 600; }
    .ev .t { line-height: 1.45; }
    .ev .ago { font: var(--bx-font-meta); color: var(--bx-muted); white-space: nowrap; }
    .ev .acc { cursor: pointer; }
    .ev .acc:hover { text-decoration: underline; }

    /* drawer */
    .scrim { position: absolute; inset: 0; background: var(--bx-scrim); z-index: 5; }
    aside { position: absolute; top: 0; right: 0; bottom: 0; width: min(440px, 92%); z-index: 6; background: var(--bx-panel); border-left: 1px solid var(--bx-border-strong);
            box-shadow: var(--bx-shadow-pop); overflow: auto; padding: 16px 20px 20px; }
    aside h2 { margin: 0; padding-right: 36px; font: var(--bx-font-title); }
    aside .x { position: absolute; top: 12px; right: 12px; }
    aside .line { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 8px 0 12px; }
    aside p.about { margin: 0 0 12px; line-height: 1.5; }
    .facts { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin-bottom: 16px; }
    .fact { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 10px; }
    .fact b { display: block; margin-top: 2px; font: var(--bx-font-title); font-variant-numeric: tabular-nums; }
    aside section { margin-top: 16px; }
    aside .label { margin-bottom: 6px; display: block; }
    .contact { display: grid; grid-template-columns: 32px 1fr; gap: 10px; padding: 6px 0; align-items: center; }
    .contact .nm { display: flex; align-items: center; gap: 6px; font-weight: 600; }
    .contact .sub { font: var(--bx-font-meta); color: var(--bx-muted); }
    .deal { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 10px; margin-bottom: 6px; }
    .deal .top { display: flex; gap: 8px; align-items: baseline; }
    .deal .top b { font-weight: 600; }
    .deal .top .amt { margin-left: auto; font-weight: 600; font-variant-numeric: tabular-nums; }
    .deal .nx { font: var(--bx-font-meta); color: var(--bx-muted); margin-top: 4px; }
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
        <div class="brand"><i></i>Customers</div>
        <nav>
          ${[['pipeline', 'Pipeline'], ['accounts', 'Accounts'], ['activity', 'Activity']].map(([id, label]) => html`
            <button aria-current=${this._view === id ? 'true' : 'false'} @click=${() => this._go(id)}>${label}</button>`)}
        </nav>
        <span class="sp"></span>
        <input class="search" type="search" placeholder="Search accounts and deals" .value=${this._q} @input=${(e) => { this._q = e.target.value; }}>
      </header>
      <div class="kpis">
        ${s ? html`
          <div class="kpi"><span class="label">Open pipeline</span><b>${money(s.pipeline, true)}<small>${s.openDeals} deals</small></b></div>
          <div class="kpi"><span class="label">Weighted forecast</span><b>${money(s.weighted, true)}</b></div>
          <div class="kpi more"><span class="label">Won, last 30 days</span><b>${money(s.won30, true)}</b></div>
          <div class="kpi arr"><span class="label">ARR</span><b>${money(s.arr, true)}<small>${s.customers} customers</small></b></div>
          <div class="kpi more"><span class="label">Win rate</span><b>${s.winRate == null ? '—' : s.winRate + '%'}</b></div>` : html`<span class="muted">Loading…</span>`}
      </div>
      <main>
        ${this._err ? html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>` : nothing}
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
          return html`<div class="col ${this._over === st ? 'over' : ''}"
              @dragover=${(e) => { if (this._drag) { e.preventDefault(); this._over = st; } }}
              @dragleave=${() => { if (this._over === st) this._over = null; }}
              @drop=${(e) => this._drop(e, st)}>
            <div class="colh"><b>${this._stageName(st)}</b><span class="n">${ds.length}</span><span class="amt">${money(amt, true)}</span></div>
            ${ds.map((d) => this._card(d))}
          </div>`;
        })}
      </div>
      <div class="closed">
        ${won.map((d) => html`<button class="ok" @click=${() => this._openAccount(d.account)}><bx-icon name="ok"></bx-icon><b>Won</b> · ${d.accountName} · ${money(d.amount)} · ${dateShort(d.close)}</button>`)}
        ${lost.map((d) => html`<button class="bad" @click=${() => this._openAccount(d.account)}><bx-icon name="error"></bx-icon><b>Lost</b> · ${d.accountName} · ${money(d.amount)}</button>`)}
      </div>`;
  }
  _stageName(st) { return { lead: 'Lead', qualified: 'Qualified', demo: 'Demo', proposal: 'Proposal', negotiation: 'Negotiation' }[st]; }
  _card(d) {
    const days = Math.round((d.close - Date.now()) / 864e5);
    return html`<div class="card ${this._drag === d.id ? 'dragging' : ''}" draggable=${this._me?.canWrite ? 'true' : 'false'}
        @dragstart=${(e) => { this._drag = d.id; e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', d.id); }}
        @dragend=${() => { this._drag = null; this._over = null; }}
        @click=${() => this._openAccount(d.account)}>
      <div class="acc">${d.accountName}</div>
      <div class="dn">${d.name}</div>
      <div class="foot">
        <span class="amt num">${money(d.amount)}</span>
        ${avatar(d.ownerName || d.owner)}
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
    const status = (a) => a.status === 'customer' ? html`<span class="badge">${a.plan}</span>`
      : a.status === 'lost' ? badge('bad', 'Lost') : html`<span class="badge dashed">Prospect</span>`;
    return html`<table>
      <thead><tr><th>Account</th><th>Plan</th><th class="r">Vans</th><th class="r">ARR</th><th class="r">Open pipeline</th><th>Health</th><th>Owner</th><th>Renewal</th><th>Last touch</th></tr></thead>
      <tbody>${rows.map((a) => html`<tr class="row" @click=${() => this._openAccount(a.id)}>
        <td><div class="nm">${a.name}</div><div class="sub">${a.industry} · ${a.city}</div></td>
        <td>${status(a)}</td>
        <td class="r">${num(a.vans)}</td>
        <td class="r">${a.arr ? money(a.arr) : html`<span class="muted">—</span>`}</td>
        <td class="r">${a.openAmount ? money(a.openAmount) : html`<span class="muted">—</span>`}</td>
        <td>${HEALTH[a.health] ? badge(...HEALTH[a.health]) : html`<span class="muted">—</span>`}</td>
        <td><span class="who">${avatar(a.ownerName || a.owner, 'sm')}<span>${(a.ownerName || '').split(' ')[0]}</span></span></td>
        <td>${a.renewal ? html`${dateShort(a.renewal)}` : html`<span class="muted">—</span>`}</td>
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
      ${avatar(x.whoName || x.who, 'lg')}
      <div>
        <div class="meta">${TYPE_ICON[x.type] ? html`<bx-icon name=${TYPE_ICON[x.type]} label=${x.type}></bx-icon>` : nothing}<b>${x.whoName}</b>
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
      <button class="x btn quiet icon" title="Close" aria-label="Close" @click=${this._close}><bx-icon name="xmark"></bx-icon></button>
      <h2>${a.name}</h2>
      <div class="line">
        <span class="badge">${a.status === 'customer' ? a.plan : a.status === 'lost' ? 'Lost' : 'Prospect'}</span>
        ${h ? badge(...h) : nothing}
        <span class="muted">${a.industry} · ${a.city}${a.since ? ` · customer since ${new Date(a.since).toLocaleDateString('en-US', { month: 'short', year: 'numeric' })}` : ''}</span>
      </div>
      <p class="about">${a.about}</p>
      <div class="facts">
        <div class="fact"><span class="label">Vans</span><b>${num(a.vans)}</b></div>
        <div class="fact"><span class="label">Depots</span><b>${num(a.depots)}</b></div>
        <div class="fact"><span class="label">ARR</span><b>${a.arr ? money(a.arr, true) : '—'}</b></div>
        <div class="fact"><span class="label">Renewal</span><b>${a.renewal ? dateShort(a.renewal) : '—'}</b></div>
      </div>
      <div class="line"><span class="who">${avatar(a.ownerName || a.owner)} <span>${a.ownerName}</span></span><span class="muted">owner</span>
        ${a.csmName ? html`<span class="who" style="margin-left:10px">${avatar(a.csmName)} <span>${a.csmName}</span></span><span class="muted">success</span>` : nothing}</div>
      ${d.deals.length ? html`<section><span class="label">Deals</span>
        ${d.deals.map((x) => html`<div class="deal">
          <div class="top"><b>${x.name}</b><span class="amt">${money(x.amount)}</span></div>
          <div class="nx">${x.stageName} · ${x.stage === 'won' || x.stage === 'lost' ? dateShort(x.close) : `closes ${inDays(x.close)}`}${x.next ? html` · ${x.next}` : nothing}</div>
        </div>`)}</section>` : nothing}
      ${d.contacts.length ? html`<section><span class="label">People</span>
        ${d.contacts.map((c) => html`<div class="contact">${avatar(c.name, 'lg')}<div>
          <div class="nm">${c.name}${c.primary ? html`<span class="badge">primary</span>` : nothing}</div>
          <div class="sub">${c.title} · ${c.phone}</div></div></div>`)}</section>` : nothing}
      ${d.activity.length ? html`<section><span class="label">Activity</span>${d.activity.map((x) => this._event(x, false))}</section>` : nothing}
    </aside>`;
  }
}

customElements.define('larkspan-crm', LarkspanCrm);
