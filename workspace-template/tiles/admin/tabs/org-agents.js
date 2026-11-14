/**
 * <bx-org-agents> — the editor of an org's constraints on agents (D229,
 * docs/auth.md §Agent constraints), and of the workspace's outer bounds
 * when it is given no org: whether people's workspace agents may work on
 * the tiles, which coding-agent harnesses an agent session there may run,
 * which models may see their data — and, for the workspace only, whose
 * credentials serve a coding agent by default.
 *
 *   <bx-org-agents org="finance" .rules=${org.agents}></bx-org-agents>
 *   <bx-org-agents></bx-org-agents>                    the workspace's bounds
 *
 * A rule left at its default narrows nothing; an org only narrows the
 * workspace's bounds, which the editor shows beside each rule and the
 * server enforces (its refusal is shown as it words it). Saving sends only
 * what changed (PUT /api/xbin/orgs/<org>/agents, or PUT
 * /api/xbin/agents/policy) and fires `org-agents-saved`. The orgs tab
 * imports this file relatively; `bx org agents` does the same from a shell,
 * for a workspace whose admin console predates it.
 */
import { LitElement, html, css, nothing } from 'lit';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import '/vendor/bx-icons.js';
import { base } from '../admin-css.js';

// allowOf(v) → {kind: 'any' | 'private' | 'list', ids}: the wire's "any" |
// "private" | [ids].
const allowOf = (v) => (Array.isArray(v) ? { kind: 'list', ids: v } : { kind: v === 'private' ? 'private' : 'any', ids: [] });
const wireOf = (a) => (a.kind === 'list' ? a.ids : a.kind);
const sameAllow = (a, b) => JSON.stringify(wireOf(a)) === JSON.stringify(wireOf(b));
const words = (v) => (Array.isArray(v) ? (v.length ? v.join(', ') : 'none') : (v ?? 'any'));
const splitIds = (s) => [...new Set(String(s ?? '').split(/[\s,]+/).filter(Boolean))].sort();

export class BxOrgAgents extends LitElement {
  static properties = {
    org: { type: String, reflect: true }, // '' or absent: the workspace's bounds
    rules: { attribute: false },          // the org's rules as GET /orgs carries them (absent: none)
    _policy: { state: true },             // GET /agents/policy: the bounds, the harness ids, canSet
    _draft: { state: true }, _busy: { state: true }, _err: { state: true }, _saved: { state: true },
  };
  static styles = [base, css`
    .grid { display: grid; grid-template-columns: minmax(9em, max-content) minmax(0, 1fr); gap: 8px 12px; align-items: start; }
    .grid > .k { padding-top: 4px; color: var(--bx-muted); }
    .row { display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: center; }
    .row label { display: inline-flex; align-items: center; gap: 4px; }
    .row input[type="text"] { flex: 1; min-width: 14em; }
    .hint { color: var(--bx-muted); font: var(--bx-font-meta, 400 12px/16px system-ui, sans-serif); }
    .line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 12px; }
    .ok { color: var(--bx-ok); display: inline-flex; align-items: center; gap: 4px; }
    .err { color: var(--bx-err); display: flex; gap: 6px; align-items: center; margin-top: 8px; }
    @media (max-width: 560px) { .grid { grid-template-columns: minmax(0, 1fr); } .grid > .k { padding-top: 8px; } }
  `];

  constructor() { super(); this.org = ''; this.rules = null; this._policy = null; this._draft = {}; this._err = ''; this._saved = false; }
  connectedCallback() { super.connectedCallback(); this._load(); }
  willUpdate(changed) { if (changed.has('org')) { this._draft = {}; this._err = ''; this._saved = false; } }

  async _load() {
    try { this._policy = await api('/agents/policy'); } catch (e) { this._err = `the workspace's rules could not be read: ${e.message || e}`; }
  }

  // the rules being edited, as stored: the org's, or the bounds
  get _stored() {
    const r = (this.org ? this.rules : this._policy) ?? {};
    return { workspaceAgent: r.workspaceAgent === 'off' ? 'off' : 'on', harnesses: allowOf(r.harnesses), models: allowOf(r.models), credentials: r.credentials === 'connector' ? 'connector' : 'own-first' };
  }
  get _now() { return { ...this._stored, ...this._draft }; }
  _set(patch) { this._draft = { ...this._draft, ...patch }; this._saved = false; this._err = ''; }

  // what changed, as the PUT body ({} = nothing)
  _body() {
    const was = this._stored, now = this._now, body = {};
    if (now.workspaceAgent !== was.workspaceAgent) body.workspaceAgent = now.workspaceAgent;
    if (!sameAllow(now.harnesses, was.harnesses)) body.harnesses = wireOf(now.harnesses);
    if (!sameAllow(now.models, was.models)) body.models = wireOf(now.models);
    if (!this.org && now.credentials !== was.credentials) body.credentials = now.credentials;
    return body;
  }

  async _save() {
    const body = this._body();
    if (!Object.keys(body).length) { this._draft = {}; return; }
    this._busy = true; this._err = ''; this._saved = false;
    try {
      const out = await api(this.org ? `/orgs/${encodeURIComponent(this.org)}/agents` : '/agents/policy', jbody(body, 'PUT'));
      if (this.org) { this.rules = out.agents ?? null; this._policy = { ...(this._policy ?? {}), ...(out.bounds ?? {}) }; } else this._policy = out;
      this._draft = {}; this._saved = true;
      this.dispatchEvent(new CustomEvent('org-agents-saved', { detail: { org: this.org || '' }, bubbles: true, composed: true }));
    } catch (e) { this._err = String(e.message || e); }
    this._busy = false;
  }

  // one allow rule: a kind picker, and its list
  _allow(key, label, kinds, list) {
    const a = this._now[key];
    const bound = this.org && this._policy ? words(this._policy[key]) : '';
    return html`
      <span class="k">${label}</span>
      <div>
        <div class="row">
          <select data-rule=${key} aria-label=${label} @change=${(e) => this._set({ [key]: { kind: e.target.value, ids: e.target.value === 'list' ? a.ids : [] } })}>
            ${kinds.map(([v, text]) => html`<option value=${v} ?selected=${a.kind === v}>${text}</option>`)}
          </select>
          ${a.kind === 'list' ? list(a) : nothing}
        </div>
        ${bound && bound !== 'any' ? html`<div class="hint">the workspace allows: ${bound}</div>` : nothing}
      </div>`;
  }

  render() {
    const now = this._now, ws = !this.org, known = this._policy?.knownHarnesses ?? [];
    const dirty = Object.keys(this._body()).length > 0;
    // a workspace admin edits anything; an org's admin their org (the server
    // decides: the console is driven by admins, and says what it refused)
    const harnessIds = [...new Set([...known, ...now.harnesses.ids])];
    return html`
      <div class="grid" role="group" aria-label=${ws ? 'the workspace’s bounds on agents' : `agent rules of org ${this.org}`}>
        <span class="k">workspace agent</span>
        <div>
          <div class="row">
            <select data-rule="workspaceAgent" aria-label="workspace agent" @change=${(e) => this._set({ workspaceAgent: e.target.value })}>
              <option value="on" ?selected=${now.workspaceAgent === 'on'}>on — people’s workspace agents may work on ${ws ? 'tiles' : 'these tiles'}</option>
              <option value="off" ?selected=${now.workspaceAgent === 'off'}>off — no workspace agent reaches ${ws ? 'any tile' : 'these tiles'}</option>
            </select>
          </div>
          ${!ws && this._policy?.workspaceAgent === 'off' ? html`<div class="hint">the workspace has it turned off for every tile</div>` : nothing}
        </div>
        ${this._allow('harnesses', 'coding agents', [['any', 'any harness'], ['list', 'only these…']], (a) => html`
          ${harnessIds.map((id) => html`<label><input type="checkbox" data-harness=${id} .checked=${a.ids.includes(id)}
              @change=${(e) => this._set({ harnesses: { kind: 'list', ids: e.target.checked ? [...new Set([...a.ids, id])].sort() : a.ids.filter((x) => x !== id) } })}>${id}</label>`)}
          ${a.ids.length ? nothing : html`<span class="hint">none ticked: no agent session can start</span>`}`)}
        ${this._allow('models', 'models', [['any', 'any model'], ['private', 'private models only'], ['list', 'only these…']], (a) => html`
          <input type="text" data-models aria-label="allowed models" placeholder="upstream, upstream/model, …" .value=${a.ids.join(', ')}
            @change=${(e) => this._set({ models: { kind: 'list', ids: splitIds(e.target.value) } })}>
          <span class="hint">LLM connector ids: an upstream for all its models, or upstream/model</span>`)}
        ${ws ? html`
          <span class="k">credentials</span>
          <div class="row">
            <select data-rule="credentials" aria-label="credentials" @change=${(e) => this._set({ credentials: e.target.value })}>
              <option value="own-first" ?selected=${now.credentials === 'own-first'}>a person’s own sign-in first, else the LLM connector</option>
              <option value="connector" ?selected=${now.credentials === 'connector'}>the LLM connector, for everyone</option>
            </select>
          </div>` : nothing}
      </div>
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      <div class="line">
        <button class="act go" data-save ?disabled=${!dirty || this._busy} @click=${() => this._save()}>Save</button>
        ${dirty ? html`<button class="act quiet" @click=${() => { this._draft = {}; this._err = ''; }}>Cancel</button>` : nothing}
        ${this._saved ? html`<span class="ok"><bx-icon name="ok"></bx-icon> saved</span>` : nothing}
        <span class="hint">${ws ? 'every tile falls under these; an org can only narrow them' : 'applies to the tiles this org owns and the tiles shared into it; a tile under several orgs gets the strictest'} · sessions already running are never ended</span>
      </div>`;
  }
}
customElements.define('bx-org-agents', BxOrgAgents);
