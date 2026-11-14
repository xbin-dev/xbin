/**
 * llm-quotas.js — the LLM connector's quotas (D226, docs/llm.md §Quotas):
 *
 *   <bx-llm-quota subject [standalone] [effective]>  one subject's quota —
 *       `workspace` (everyone together), `default` (each person),
 *       `user:<id>` or `org:<id>` (an override): tokens and cost per UTC
 *       day and month, and what reaching one does (stop, or warn). Save is
 *       PUT /llm/quotas for that subject; an empty form removes it.
 *       `.rules` / `.locks` come from the list below; `standalone` (the
 *       admin console's users and organisations tabs) reads them itself,
 *       and draws nothing on an xbind without the connector. `effective`
 *       adds a person's limits as they stand, with what is used.
 *   <bx-llm-quotas .client .people .orgs>  all four parts: the workspace
 *       total, the default per person, and the per-person and per-org
 *       overrides (add, edit, remove).
 *   limitsTpl(limits, now, whose)  limits in force as bars: used of the limit,
 *       where it comes from, when it resets.
 *
 * A part the host's file sets is read-only, "Set by your host". Whose quota
 * applies to a person — their own, else the most generous of their orgs',
 * else the default, each limit on its own — is the server's to decide.
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { limitsCss } from '/vendor/bx-limits.js';
import { llmCss, lockTpl, errTpl } from '/vendor/llm-css.js';
import { llmClient, quotaOf, quotaLockPath } from '/vendor/llm-client.js';
import { LIMITS, LIMIT_META, lockOf, quotaDraft, quotaBody, quotaSummary, limitPct, usedWords, resetWords, sourceWords } from '/vendor/llm-kit.js';

// limitsTpl(limits, now, whose): each limit in force — its bar, what is
// used, its source and reset. whose: "your" (a person's own page) or
// "their" (an admin reading someone's).
export function limitsTpl(limits, now = Date.now(), whose = 'your') {
  if (!(limits ?? []).length) return nothing;
  return html`<div data-limits>${limits.map((l) => html`<div class="use" data-limit=${l.name} data-state=${l.state}>
    <span>${LIMIT_META[l.name]?.label ?? l.name}</span>
    <span class="track ${l.state === 'ok' ? '' : l.state}" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow=${limitPct(l)}
      aria-label=${`${LIMIT_META[l.name]?.label ?? l.name}: ${usedWords(l)}`}><i style=${`width:${limitPct(l)}%`}></i></span>
    <span class="meta">${usedWords(l)} · from ${sourceWords(l.source, whose)}${l.action === 'warn' ? ' · warns only' : ''}${l.resets ? ` · resets ${resetWords(l.resets, now)}` : ''}</span>
  </div>`)}</div>`;
}

const PLACE = { tokensPerDay: '2M', tokensPerMonth: '40M', costPerDay: '5', costPerMonth: '50' };

export class BxLlmQuota extends LitElement {
  static properties = {
    subject: { type: String, reflect: true }, // workspace | default | user:<id> | org:<id>
    rules: { attribute: false },     // GET /llm/quotas' `quotas`
    locks: { attribute: false },
    client: { attribute: false },
    fetcher: { attribute: false },   // standalone: the fetch to read and write with
    standalone: { type: Boolean },
    effective: { type: Boolean },    // user:<id>: also their limits as they stand
    _d: { state: true }, _busy: { state: true }, _err: { state: true }, _saved: { state: true },
    _own: { state: true },           // standalone: the GET's answer; false: no connector here
  };
  static styles = [limitsCss, llmCss, css`:host([hidden]) { display: none; }`];
  constructor() { super(); this._d = null; this._err = ''; this._saved = false; this._own = null; }
  get _llm() { return this.client ?? (this._c ??= llmClient(this.fetcher)); }
  get _rules() { return this.rules ?? this._own?.quotas ?? null; }
  get _locks() { return this.locks ?? this._own?.locks ?? null; }

  connectedCallback() { super.connectedCallback(); if (this.standalone) this._load(); }
  willUpdate(changed) {
    if (changed.has('subject')) { this._d = null; this._err = ''; this._saved = false; if (this.standalone && changed.get('subject') !== undefined) this._load(); }
  }
  async _load() {
    const s = String(this.subject ?? '');
    const r = await this._llm.quotas(this.effective && s.startsWith('user:') ? s.slice(5) : undefined);
    this._own = r.ok ? r.data : false; // an xbind without the connector, or not an admin's to read: nothing is drawn
    this.hidden = !r.ok;
  }

  async _save(quota) {
    this._busy = true; this._err = ''; this._saved = false;
    const r = await this._llm.putQuota(this.subject, quota);
    this._busy = false;
    if (!r.ok) { this._err = r.error; return; }
    this._d = null; this._saved = true;
    if (this.standalone) await this._load();
    this.dispatchEvent(new CustomEvent('llm-quotas', { detail: r.data, bubbles: true, composed: true }));
  }
  _submit() {
    const { quota, error } = quotaBody(this._d ?? quotaDraft(quotaOf(this._rules, this.subject)));
    if (error) { this._err = error; return; }
    this._save(quota);
  }

  render() {
    if (this.standalone && !this._own) return nothing;
    const cur = quotaOf(this._rules, this.subject), lock = lockOf(this._locks, quotaLockPath(this.subject));
    const d = this._d ?? quotaDraft(cur), dirty = !!this._d, off = lock.locked || this._busy;
    const set = (patch) => { this._d = { ...d, ...patch }; this._saved = false; };
    return html`
      <form class="limits" data-quota=${this.subject} ?data-host=${lock.locked} @submit=${(e) => { e.preventDefault(); this._submit(); }}>
        ${LIMITS.map((k) => html`<label>${LIMIT_META[k].label}${LIMIT_META[k].cost ? ' (USD)' : ''}
          <input data-limit=${k} .value=${d[k]} placeholder=${cur ? 'not set' : PLACE[k]} ?disabled=${off} autocomplete="off" spellcheck="false"
            title=${LIMIT_META[k].cost ? 'USD, like 50; "unlimited" lifts a limit set further down; empty: not set' : 'tokens in and out, like 200k or 2M; "unlimited" lifts a limit set further down; empty: not set'}
            @input=${(e) => set({ [k]: e.target.value })}></label>`)}
        <label>at a limit
          <select data-action ?disabled=${off} @change=${(e) => set({ action: e.target.value })}>
            <option value="stop" ?selected=${d.action !== 'warn'}>stop calls</option>
            <option value="warn" ?selected=${d.action === 'warn'}>warn only</option>
          </select></label>
      </form>
      ${errTpl(this._err)}
      <div class="line">
        ${lock.locked ? lockTpl(lock) : html`
          <button class="primary small" data-save ?disabled=${!dirty || this._busy} @click=${() => this._submit()}>Save</button>
          ${dirty ? html`<button class="quiet small" @click=${() => { this._d = null; this._err = ''; }}>Cancel</button>` : nothing}
          ${cur && !dirty ? html`<button class="quiet small" data-clear ?disabled=${this._busy} title="remove this quota" @click=${() => this._save(null)}>remove</button>` : nothing}`}
        ${this._saved ? html`<span class="ok" data-saved><bx-icon name="ok"></bx-icon> saved</span>` : nothing}
        <span class="meta" data-summary>${quotaSummary(cur)}</span>
      </div>
      ${this.effective && this._own?.effective ? html`<div class="meta" style="margin-top:8px">in force now${(this._own.effective.orgs ?? []).length
        ? ` (orgs: ${this._own.effective.orgs.join(', ')})` : ''}:${(this._own.effective.limits ?? []).length ? '' : ' no limit of their own; the workspace total still holds'}</div>
        ${limitsTpl(this._own.effective.limits, Date.now(), 'their')}` : nothing}`;
  }
}
customElements.define('bx-llm-quota', BxLlmQuota);

export class BxLlmQuotas extends LitElement {
  static properties = {
    client: { attribute: false },
    people: { attribute: false },  // user ids to offer (optional)
    orgs: { attribute: false },    // org ids to offer (optional)
    _data: { state: true },        // GET /llm/quotas
    _add: { state: true },         // {users: 'bob', orgs: ''}: the subject last added in each part (its row is open)
    _err: { state: true },
  };
  static styles = [limitsCss, llmCss];
  constructor() { super(); this._data = null; this._add = {}; this._err = ''; }
  connectedCallback() { super.connectedCallback(); this.reload(); }
  async reload() {
    const r = await this.client.quotas();
    if (!r.ok) { this._err = r.error; return; }
    this._data = r.data; this._err = '';
  }
  _saved(e) { if (e.detail?.quotas) this._data = { ...this._data, ...e.detail }; }

  _one(subject) {
    return html`<bx-llm-quota subject=${subject} .rules=${this._data.quotas} .locks=${this._data.locks} .client=${this.client}></bx-llm-quota>`;
  }
  // the overrides of one part: users | orgs
  _part(part, noun, known) {
    const map = this._data.quotas?.[part] ?? {};
    const lock = lockOf(this._data.locks, `quotas.${part}`), prefix = part === 'users' ? 'user:' : 'org:';
    // the one just added keeps its place and stays open across its first save
    const adding = lock.locked ? '' : this._add[part], listId = `llm-${part}`;
    const ids = [...new Set([...Object.keys(map), ...(adding ? [adding] : [])])].sort();
    const open = (id) => { const v = String(id ?? '').trim(); if (v) this._add = { ...this._add, [part]: v }; };
    return html`
      ${ids.map((id) => html`<details class="sub" data-override=${prefix + id} ?open=${id === adding}>
        <summary><span class="mono">${id}</span><span class="meta">${map[id] ? quotaSummary(map[id]) : 'new override'}</span></summary>${this._one(prefix + id)}</details>`)}
      ${!ids.length ? html`<div class="empty">${lock.locked ? `No ${noun} has an override, and none can be added.` : `No ${noun} has an override.`}</div>` : nothing}
      ${lock.locked ? nothing : html`<form class="line" data-add=${part} @submit=${(e) => { e.preventDefault(); open(e.target.id_.value); e.target.reset(); }}>
        <input name="id_" list=${listId} placeholder=${`${noun} id`} aria-label=${`${noun} id`} autocomplete="off" spellcheck="false" required>
        <datalist id=${listId}>${(known ?? []).filter((k) => !map[k]).map((k) => html`<option value=${k}></option>`)}</datalist>
        <button class="small" type="submit">Add an override</button></form>`}`;
  }

  render() {
    if (!this._data) return this._err ? errTpl(this._err) : html`<div class="empty">…</div>`;
    const L = (p) => lockTpl(lockOf(this._data.locks, `quotas.${p}`));
    return html`<div @llm-quotas=${(e) => this._saved(e)}>
      ${errTpl(this._err)}
      <h4>Workspace total ${L('workspace')}</h4>
      <div class="hint">Everyone’s calls together, and the only limit on calls a tile makes for nobody in particular.</div>
      ${this._one('workspace')}
      <h4>Default for each person ${L('default')}</h4>
      <div class="hint">What a person gets unless an override below says otherwise.</div>
      ${this._one('default')}
      <h4>Per person ${L('users')}</h4>
      <div class="hint">A person’s own override wins over their orgs’ and the default, limit by limit — upwards or downwards.</div>
      ${this._part('users', 'person', this.people)}
      <h4>Per organisation ${L('orgs')}</h4>
      <div class="hint">An org’s override replaces the default for its members; someone in several orgs gets the most generous of them.</div>
      ${this._part('orgs', 'organisation', this.orgs)}
    </div>`;
  }
  testApi() { const s = this; return { get data() { return s._data; }, reload: () => s.reload() }; }
}
customElements.define('bx-llm-quotas', BxLlmQuotas);
