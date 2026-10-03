// expenses.js — your own expense book: what's in draft, waiting for
// approval, approved and paid; add an expense, submit drafts.
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { getJSON, sendJSON, api, cents as money, dateShort, baseCss } from './ui.js';

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
    .wrap { padding: 16px 20px 24px; max-width: 980px; }
    header { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
    h1 { margin: 0; font: var(--bx-font-heading); letter-spacing: var(--bx-tracking-heading); }
    .sub { color: var(--bx-muted); margin-top: 2px; }
    .sp { flex: 1; }
    .cards { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-bottom: 20px; }
    .card { background: var(--bx-panel-2); border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 10px 12px; }
    .card b { display: block; margin-top: 4px; font: var(--bx-font-heading); font-family: var(--bx-sans); font-variant-numeric: tabular-nums; }
    section { margin-bottom: 16px; }
    .sh { display: flex; align-items: baseline; gap: 8px; padding: 0 2px 6px; }
    .sh b { font-weight: 600; }
    .sh .tot { margin-left: auto; font-weight: 600; font-variant-numeric: tabular-nums; }
    .list { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); overflow: hidden; }
    .it { display: grid; grid-template-columns: 56px 92px 1fr auto auto; gap: 12px; align-items: center; min-height: 48px; padding: 6px 12px;
          border-bottom: 1px solid var(--bx-border); background: var(--bx-panel); }
    .it:last-child { border-bottom: 0; }
    .it .dt { font: var(--bx-font-meta); color: var(--bx-muted); font-variant-numeric: tabular-nums; }
    .it .m { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 8px; font-weight: 600; }
    /* the trip: under the merchant where they don't fit side by side, cut short where it can't fit at all */
    .it .m .badge { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; line-height: 18px; }
    .it .n { color: var(--bx-muted); }
    .it .rc { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); }
    .it .rc.miss { color: var(--bx-warn); }
    .it .amt { display: flex; align-items: center; justify-content: flex-end; gap: 4px; min-width: 86px; font-weight: 600; font-variant-numeric: tabular-nums; }
    .it .amt .btn { min-height: 24px; width: 24px; color: var(--bx-muted); }
    .none { padding: 24px 20px; border: 1px dashed var(--bx-border-strong); border-radius: var(--bx-radius); color: var(--bx-muted); text-align: center; }
    .none b { color: var(--bx-text); }
    form { display: grid; grid-template-columns: 140px 1fr 120px 130px auto; gap: 8px; padding: 12px; margin-bottom: 16px;
           border: 1px dashed var(--bx-border-strong); border-radius: var(--bx-radius); }
    form input, form select { min-width: 0; }
    /* a phone: the totals two by two, an item's date and amount around what it was */
    @media (max-width: 560px) {
      .wrap { padding: 12px 12px 20px; }
      header { flex-wrap: wrap; }
      .cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .it { grid-template-columns: 48px minmax(0, 1fr) auto; gap: 8px; padding: 6px 10px; }
      .it .cat, .it .rc { display: none; }
      .sh { flex-wrap: wrap; }
    }
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
    if (this._err) return html`<div class="err"><bx-icon name="error"></bx-icon>${this._err}</div>`;
    const d = this._d;
    if (!d) return html`<div class="empty">Loading your expenses…</div>`;
    const drafts = d.items.filter((x) => x.status === 'draft');
    return html`<div class="wrap">
      <header>
        <div><h1>My expenses</h1><div class="sub">${d.name ? `${d.name} · ` : ''}only you can see this book</div></div>
        <span class="sp"></span>
        ${drafts.length ? html`<button class="btn" ?disabled=${this._busy} @click=${this._submitDrafts}>Submit ${drafts.length} draft${drafts.length > 1 ? 's' : ''}</button>` : nothing}
        <button class="btn primary" @click=${() => { this._adding = !this._adding; }}>${this._adding ? 'Cancel' : html`<bx-icon name="plus"></bx-icon>New expense`}</button>
      </header>
      <div class="cards">
        <div class="card"><span class="label">Drafts</span><b>${money(d.totals.draft)}</b></div>
        <div class="card"><span class="label">Awaiting approval</span><b>${money(d.totals.submitted)}</b></div>
        <div class="card"><span class="label">Approved, to be paid</span><b>${money(d.totals.approved)}</b></div>
        <div class="card"><span class="label">Paid, last 30 days</span><b>${money(d.totals.reimbursed)}</b></div>
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
          <div class="sh"><b>${title}</b><span class="muted">${hint}</span><span class="tot">${money(xs.reduce((t, x) => t + x.amount, 0))}</span></div>
          <div class="list">${xs.map((x) => html`<div class="it">
            <span class="dt">${dateShort(x.date)}</span>
            <span class="cat"><span class="badge">${x.category}</span></span>
            <div><div class="m">${x.merchant}${x.trip ? html`<span class="badge">${x.trip}</span>` : nothing}</div>
              <div class="n">${x.note}${x.category === 'mileage' ? ` · ${x.miles} mi at $${d.rate.toFixed(2)}` : ''}</div></div>
            <span class="rc ${x.receipt || x.category === 'mileage' ? '' : 'miss'}">${x.category === 'mileage' ? ''
              : x.receipt ? html`<bx-icon name="paperclip"></bx-icon>receipt` : html`<bx-icon name="warning"></bx-icon>receipt missing`}</span>
            <span class="amt">${money(x.amount)}${st === 'draft' ? html`<button class="btn quiet icon" title="Delete draft" aria-label="Delete draft" @click=${() => this._remove(x.id)}><bx-icon name="xmark"></bx-icon></button>` : nothing}</span>
          </div>`)}</div>
        </section>`;
      })}
    </div>`;
  }
}

customElements.define('my-expenses', MyExpenses);
