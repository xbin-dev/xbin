// board.js — the onboarding tracker: a card per customer we're onboarding,
// soonest go-live first, with progress, the next step and the checklist.
import { LitElement, html, nothing } from 'lit';
import '/vendor/bx-icons.js';
import { styles } from './styles.js';
import { loadCustomers, saveCustomer } from './store.js';
import { crmAccount } from './crm.js';
import { checklist } from './checklist.js';

const DAY = 864e5;
const day = (t) => new Date(t).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
const until = (t) => { const d = Math.ceil((t - Date.now()) / DAY); return d <= 0 ? 'today' : d === 1 ? 'tomorrow' : `in ${d} days`; };
// the CRM's health: a status (its colour), its icon and its word
const HEALTH = { good: ['ok', 'ok', 'healthy'], watch: ['warn', 'warning', 'watch'], risk: ['bad', 'error', 'at risk'] };
const initials = (n) => String(n || '?').split(/\s+/).slice(0, 2).map((w) => w[0]).join('').toUpperCase();

class OnboardingBoard extends LitElement {
  static properties = { _cs: { state: true }, _crm: { state: true }, _open: { state: true }, _shown: { state: true }, _err: { state: true } };
  static styles = styles;

  constructor() { super(); this._cs = null; this._crm = {}; this._open = {}; this._shown = {}; }
  connectedCallback() { super.connectedCallback(); this._load(); }

  async _load() {
    try {
      const cs = await loadCustomers();
      cs.sort((a, b) => a.goLive - b.goLive);
      this._cs = cs;
      for (const c of cs) crmAccount(c.id).then((a) => { if (a) this._crm = { ...this._crm, [c.id]: a }; });
    } catch (e) { this._err = e.message; }
  }

  async _toggle(c, s, on) {
    s.done = on;
    s.doneAt = on ? Date.now() : undefined;
    this.requestUpdate();
    try { await saveCustomer(c); } catch (e) { this._err = e.message; }
  }

  render() {
    if (this._err) return html`<div class="wrap err">${this._err}</div>`;
    if (!this._cs) return html`<div class="wrap sub">Loading…</div>`;
    const next = this._cs[0];
    return html`<div class="wrap">
      <header>
        <h1>Onboarding</h1>
        <span class="sub">${this._cs.length} customers in flight${next ? html` · next go-live: ${next.name}, ${until(next.goLive)}` : nothing}</span>
      </header>
      <div class="grid">${this._cs.map((c) => this._card(c))}</div>
      <footer>Steps save as you tick them. Plan, fleet and health come from the CRM.</footer>
    </div>`;
  }

  _card(c) {
    const a = this._crm[c.id];
    const done = c.steps.filter((s) => s.done).length;
    const nx = c.steps.find((s) => !s.done);
    const late = nx?.due && nx.due < Date.now();
    const h = HEALTH[a?.health];
    return html`<div class="card">
      <div class="top">
        <div><div class="name">${c.name}</div><div class="what">${c.title}</div></div>
        <span class="av" title=${c.csmName}>${initials(c.csmName)}</span>
      </div>
      <div class="facts">
        ${a ? html`<span class="chip">${a.status === 'customer' ? a.plan : 'Prospect'}</span><span class="chip">${a.vans} vans · ${a.depots} depot${a.depots > 1 ? 's' : ''}</span>` : nothing}
        ${h ? html`<span class="chip ${h[0]}"><bx-icon name=${h[1]}></bx-icon>${h[2]}</span>` : nothing}
        <span class="chip">go-live ${day(c.goLive)}</span>
      </div>
      <div class="bar"><i style="width:${Math.round(100 * done / c.steps.length)}%"></i></div>
      <div class="prog"><span><b>${done}</b> of ${c.steps.length} steps</span><span>go-live ${until(c.goLive)}</span></div>
      ${nx ? html`<div class="next ${late ? 'late' : ''}">${late ? html`<bx-icon name="error"></bx-icon> ` : nothing}Next: <b>${nx.title}</b> <span>· ${late ? `late, was due ${day(nx.due)}` : `due ${day(nx.due)}`}</span></div>` : html`<div class="next">Ready for go-live</div>`}
      ${this._open[c.id] ? checklist(c, {
        open: !!this._shown[c.id],
        onToggle: (cc, s, on) => this._toggle(cc, s, on),
        onShowDone: () => { this._shown = { ...this._shown, [c.id]: !this._shown[c.id] }; },
      }) : nothing}
      <button class="toggle" @click=${() => { this._open = { ...this._open, [c.id]: !this._open[c.id] }; }}>${this._open[c.id] ? 'Hide checklist' : 'Show checklist'}</button>
    </div>`;
  }
}

customElements.define('onboarding-board', OnboardingBoard);
