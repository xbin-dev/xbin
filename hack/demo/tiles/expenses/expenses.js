// expenses.js — your own expense book: what's in draft, waiting for
// approval, approved and paid; add an expense, submit drafts.
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, sendJSON, api, cents as money, dateShort, baseCss } from './ui.js';

const ICON = { travel: '✈', lodging: '⌂', meals: '◔', mileage: '⛟', events: '★', software: '⌘', office: '✎', other: '•' };
const SECTIONS = [
  ['draft', 'Drafts', 'only you see these until you submit them'],
  ['submitted', 'Waiting for approval', 'Elena approves expenses on Tuesdays and Fridays'],
  ['approved', 'Approved', 'paid with the next payroll'],
  ['reimbursed', 'Paid', 'the last month'],
];

class MyExpenses extends LitElement {
  static properties = { _d: { state: true }, _err: { state: true }, _adding: { state: true }, _busy: { state: true } };

  static styles = [scrollCss, baseCss, css`
    :host { overflow: auto; }
    .wrap { padding: 14px 18px 22px; max-width: 980px; }
    header { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
    h1 { margin: 0; font-size: 17px; font-weight: 650; }
    .sub { color: var(--bx-muted); font-size: 12px; margin-top: 2px; }
    .sp { flex: 1; }
    .cards { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin-bottom: 16px; }
    .card { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 9px; padding: 9px 12px; border-top: 3px solid var(--c); }
    .card b { display: block; font-size: 19px; font-weight: 650; margin-top: 3px; }
    section { margin-bottom: 14px; }
    .sh { display: flex; align-items: baseline; gap: 8px; padding: 0 2px 6px; }
    .sh b { font-size: 13px; }
    .sh .muted { font-size: 11.5px; }
    .sh .tot { margin-left: auto; font-weight: 600; }
    .list { border: 1px solid var(--bx-border); border-radius: 9px; overflow: hidden; }
    .it { display: grid; grid-template-columns: 52px 28px 1fr auto auto; gap: 10px; align-items: center; padding: 8px 12px; border-bottom: 1px solid color-mix(in srgb, var(--bx-border) 55%, transparent); background: var(--bx-panel); }
    .it:last-child { border-bottom: 0; }
    .it .dt { color: var(--bx-muted); font-size: 11.5px; }
    .it .ic { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 7px; background: var(--bx-panel-2); color: var(--bx-accent); font-size: 13px; }
    .it .m { font-weight: 600; font-size: 12.5px; }
    .it .n { color: var(--bx-muted); font-size: 11.5px; }
    .it .trip { font-size: 10.5px; margin-left: 6px; }
    .it .rc { font-size: 11px; color: var(--bx-muted); }
    .it .rc.miss { color: var(--warn); }
    .it .amt { font-weight: 650; font-size: 13px; text-align: right; min-width: 78px; }
    .none { padding: 26px 18px; border: 1px dashed var(--bx-border); border-radius: 9px; color: var(--bx-muted); text-align: center; font-size: 12.5px; }
    .none b { color: var(--bx-text); }
    form { display: grid; grid-template-columns: 120px 1fr 120px 110px auto; gap: 8px; padding: 10px 12px; margin-bottom: 14px; border: 1px dashed var(--bx-border); border-radius: 9px; }
    form input, form select { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: 6px; color: inherit; font: inherit; font-size: 12px; padding: 5px 8px; min-width: 0; }
  `];

  connectedCallback() { super.connectedCallback(); this._load(); }
  async _load() {
    try { this._d = await getJSON('/expenses'); this._err = ''; } catch (e) { this._err = e.message; }
  }
  async _submitDrafts() {
    this._busy = true;
    try { this._d = await sendJSON('/submit', 'POST', {}); } catch (e) { this._err = e.message; } finally { this._busy = false; }
  }
  async _add(e) {
    e.preventDefault();
    const f = new FormData(e.target);
    const cat = f.get('category');
    const body = { date: f.get('date'), merchant: f.get('merchant'), category: cat, note: f.get('note') || '', receipt: true };
    if (cat === 'mileage') body.miles = Number(f.get('amount')); else body.amount = Number(f.get('amount'));
    try { await sendJSON('/expenses', 'POST', body); this._adding = false; await this._load(); } catch (err) { this._err = err.message; }
  }
  async _remove(id) { await api(`/expenses/${id}`, { method: 'DELETE' }); this._load(); }

  render() {
    if (this._err) return html`<div class="err">${this._err}</div>`;
    const d = this._d;
    if (!d) return html`<div class="empty">Loading your expenses…</div>`;
    const drafts = d.items.filter((x) => x.status === 'draft');
    return html`<div class="wrap">
      <header>
        <div><h1>My expenses</h1><div class="sub">${d.name ? `${d.name} · ` : ''}only you can see this book</div></div>
        <span class="sp"></span>
        ${drafts.length ? html`<button class="btn" ?disabled=${this._busy} @click=${this._submitDrafts}>Submit ${drafts.length} draft${drafts.length > 1 ? 's' : ''}</button>` : nothing}
        <button class="btn primary" @click=${() => { this._adding = !this._adding; }}>${this._adding ? 'Cancel' : '+ New expense'}</button>
      </header>
      <div class="cards">
        <div class="card" style="--c:var(--bx-muted)"><span class="label">Drafts</span><b class="num">${money(d.totals.draft)}</b></div>
        <div class="card" style="--c:var(--blue)"><span class="label">Awaiting approval</span><b class="num">${money(d.totals.submitted)}</b></div>
        <div class="card" style="--c:var(--bx-accent)"><span class="label">Approved, to be paid</span><b class="num">${money(d.totals.approved)}</b></div>
        <div class="card" style="--c:var(--ok)"><span class="label">Paid, last 30 days</span><b class="num">${money(d.totals.reimbursed)}</b></div>
      </div>
      ${this._adding ? html`<form @submit=${this._add}>
        <input name="date" type="date" required .value=${new Date().toISOString().slice(0, 10)}>
        <input name="merchant" placeholder="Merchant" required>
        <select name="category">${d.categories.map((c) => html`<option value=${c}>${c}</option>`)}</select>
        <input name="amount" type="number" step="0.01" min="0" placeholder="Amount (or miles)" required>
        <button class="btn primary">Add</button>
        <input name="note" placeholder="What was it for?" style="grid-column: 1 / -1">
      </form>` : nothing}
      ${d.items.length ? nothing : html`<div class="none"><b>No expenses yet.</b> Add a receipt with <i>New expense</i>; drafts stay yours until you submit them.</div>`}
      ${SECTIONS.map(([st, title, hint]) => {
        const xs = d.items.filter((x) => x.status === st);
        if (!xs.length) return nothing;
        return html`<section>
          <div class="sh"><b>${title}</b><span class="muted">${hint}</span><span class="tot num">${money(xs.reduce((t, x) => t + x.amount, 0))}</span></div>
          <div class="list">${xs.map((x) => html`<div class="it">
            <span class="dt">${dateShort(x.date)}</span>
            <span class="ic" title=${x.category}>${ICON[x.category] ?? '•'}</span>
            <div><div class="m">${x.merchant}${x.trip ? html`<span class="chip trip">${x.trip}</span>` : nothing}</div>
              <div class="n">${x.note}${x.category === 'mileage' ? ` · ${x.miles} mi at $${d.rate.toFixed(2)}` : ''}</div></div>
            <span class="rc ${x.receipt || x.category === 'mileage' ? '' : 'miss'}">${x.category === 'mileage' ? '' : x.receipt ? '⎘ receipt' : 'receipt missing'}</span>
            <span class="amt num">${money(x.amount)}${st === 'draft' ? html`<button title="Delete draft" style="background:none;border:0;color:var(--bx-muted);margin-left:6px" @click=${() => this._remove(x.id)}>×</button>` : nothing}</span>
          </div>`)}</div>
        </section>`;
      })}
    </div>`;
  }
}

customElements.define('my-expenses', MyExpenses);
