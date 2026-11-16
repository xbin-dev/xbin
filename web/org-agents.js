/**
 * org-agents.js — <bx-org-agents>: the editor of what agents may do on an
 * org's tiles (D229, docs/auth.md §Agent constraints): whether people's
 * workspace agents may work there, which coding-agent harnesses an agent
 * session may run, which models may see the tiles' data.
 *
 *   <bx-org-agents org="finance" .rules=${org.agents} .pin=${org.agentsPin}>
 *       the org's own rules — an admin of the org, or a workspace admin.
 *       What a workspace admin pinned on the org shows above them: the
 *       org's rules narrow below a pin, never past it
 *   <bx-org-agents org="finance" pinned .pin=${org.agentsPin}>
 *       the pin itself (workspace admins)
 *   <bx-org-agents>
 *       the workspace's bounds, and whose credentials serve a coding agent
 *       (workspace admins)
 *
 * A rule left at its default narrows nothing. The workspace's bounds show
 * beside each rule; the server enforces bounds and pin, and its refusal is
 * shown as it words it. Saving sends only what changed (PUT
 * /api/xbin/orgs/<org>/agents, …/agents/pin, or /api/xbin/agents/policy)
 * and fires `org-agents-saved` {org, agents?, pin?}. The admin console's
 * organisations tab and the organisations tile both use it; `bx org
 * agents` does the same from a shell. Requests go through bx-kit's api():
 * a sandboxed tile's own credential, or the signed-in person's session.
 * Lit by its /vendor URL (shared modules use no import map).
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import { xbinApi as api, jbody } from '/vendor/bx-kit.js';
import '/vendor/bx-icons.js';

// allowOf(v) → {kind: 'any' | 'private' | 'list', ids}: the wire's "any" |
// "private" | [ids].
const allowOf = (v) => (Array.isArray(v) ? { kind: 'list', ids: v } : { kind: v === 'private' ? 'private' : 'any', ids: [] });
const wireOf = (a) => (a.kind === 'list' ? a.ids : a.kind);
const sameAllow = (a, b) => JSON.stringify(wireOf(a)) === JSON.stringify(wireOf(b));
const words = (v) => (Array.isArray(v) ? (v.length ? v.join(', ') : 'none') : (v ?? 'any'));
const splitIds = (s) => [...new Set(String(s ?? '').split(/[\s,]+/).filter(Boolean))].sort();
const MODES = ['own-first', 'connector-first', 'connector-only'];

// rulesText(r) → a set of rules in a line, only what narrows; '' for none.
export function rulesText(r) {
  if (!r) return '';
  const out = [];
  if (r.workspaceAgent === 'off') out.push('workspace agent off');
  if (r.harnesses != null && r.harnesses !== 'any') out.push(`coding agents: ${words(r.harnesses)}`);
  if (r.models != null && r.models !== 'any') out.push(r.models === 'private' ? 'private models only' : `models: ${words(r.models)}`);
  if (r.web === 'never') out.push('data never to the web');
  else if (r.web === 'allow') out.push('web allowed');
  else if (r.web === 'ask') out.push('web asks');
  if (r.mcp === 'org') out.push('MCP: the org’s own servers');
  else if (r.mcp === 'listed') out.push('MCP: listed outside servers trusted');
  else if (r.mcp === 'never') out.push('MCP: none');
  return out.join(' · ');
}

export class BxOrgAgents extends LitElement {
  static properties = {
    org: { type: String, reflect: true }, // '' or absent: the workspace's bounds
    pinned: { type: Boolean, reflect: true }, // edit the rules pinned on the org, not its own
    rules: { attribute: false },          // the org's own rules as GET /orgs carries them (absent: none)
    pin: { attribute: false },            // the rules pinned on it (GET /orgs: agentsPin; absent: none)
    _policy: { state: true },             // GET /agents/policy: the bounds, the harness ids, canSet
    _draft: { state: true }, _busy: { state: true }, _err: { state: true }, _saved: { state: true },
  };
  static styles = css`
    :host { display: block; font: var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif); color: var(--bx-text, #E9EAF0); }
    button, input, select { font: inherit; }
    :focus-visible { outline: var(--bx-focus-outline, 3px solid #3DD6F5); outline-offset: var(--bx-focus-offset, 2px); box-shadow: var(--bx-focus-halo, 0 0 0 2px #0B0C12); }
    .grid { display: grid; grid-template-columns: minmax(9em, max-content) minmax(0, 1fr); gap: 8px 12px; align-items: start; }
    .grid > .k { padding-top: 4px; color: var(--bx-muted, #A3A6B6); }
    .row { display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: center; }
    .row label { display: inline-flex; align-items: center; gap: 4px; }
    .row input[type="text"] { flex: 1; min-width: 14em; }
    select, input[type="text"] { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 8px; max-width: 100%;
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    .hint { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif); }
    .pinline { display: flex; gap: 6px; align-items: baseline; margin: 0 0 10px; padding: 6px 10px;
      border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-info-bg, #30323B); }
    .pinline bx-icon { color: var(--bx-info, #A9B4C6); align-self: center; }
    .line { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: 12px; }
    button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; white-space: nowrap; font-weight: 600;
      background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
      border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); }
    button:hover { background: var(--bx-hover, #2A2B34); }
    button:disabled { opacity: .5; cursor: default; }
    button.go { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
    button.go:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
    button.quiet { background: transparent; border-color: transparent; }
    .ok { color: var(--bx-ok, #A3CF5E); display: inline-flex; align-items: center; gap: 4px; }
    .err { color: var(--bx-danger, #FF7A7A); display: flex; gap: 6px; align-items: center; margin-top: 8px; }
    @media (max-width: 560px) { .grid { grid-template-columns: minmax(0, 1fr); } .grid > .k { padding-top: 8px; } }
  `;

  constructor() { super(); this.org = ''; this.pinned = false; this.rules = null; this.pin = null; this._policy = null; this._draft = {}; this._err = ''; this._saved = false; }
  connectedCallback() { super.connectedCallback(); this._load(); }
  willUpdate(changed) { if (changed.has('org') || changed.has('pinned')) { this._draft = {}; this._err = ''; this._saved = false; } }

  async _load() {
    try { this._policy = await api('/agents/policy'); } catch (e) { this._err = `the workspace's rules could not be read: ${e.message || e}`; }
  }

  // the rules being edited, as stored: the org's own, its pin, or the bounds
  get _stored() {
    const r = (!this.org ? this._policy : this.pinned ? this.pin : this.rules) ?? {};
    const mode = r.credentials === 'connector' ? 'connector-only' : r.credentials; // (the name connector-only had)
    return { workspaceAgent: r.workspaceAgent === 'off' ? 'off' : 'on', harnesses: allowOf(r.harnesses), models: allowOf(r.models), credentials: MODES.includes(mode) ? mode : 'own-first',
      web: ['ask', 'allow', 'never'].includes(r.web) ? r.web : '', mcp: ['listed', 'org', 'never'].includes(r.mcp) ? r.mcp : '' };
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
    if (now.web !== was.web) body.web = now.web || null; // D239: '' is no rule of its own (null puts it back)
    if (now.mcp !== was.mcp) body.mcp = now.mcp || null;
    return body;
  }

  async _save() {
    const body = this._body();
    if (!Object.keys(body).length) { this._draft = {}; return; }
    this._busy = true; this._err = ''; this._saved = false;
    try {
      const path = !this.org ? '/agents/policy' : `/orgs/${encodeURIComponent(this.org)}/agents${this.pinned ? '/pin' : ''}`;
      const out = await api(path, jbody(body, 'PUT'));
      if (this.org) { this.rules = out.agents ?? null; this.pin = out.pin ?? this.pin; this._policy = { ...(this._policy ?? {}), ...(out.bounds ?? {}) }; } else this._policy = out;
      this._draft = {}; this._saved = true;
      this.dispatchEvent(new CustomEvent('org-agents-saved', { detail: { org: this.org || '', agents: out.agents, pin: out.pin }, bubbles: true, composed: true }));
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
        ${key === 'models' && a.kind !== 'any' ? html`<div class="hint">agent sessions on these tiles then run on the workspace’s LLM connector only — a coding agent that can’t isn’t offered there</div>` : nothing}
      </div>`;
  }

  render() {
    const now = this._now, ws = !this.org, known = this._policy?.knownHarnesses ?? [];
    const dirty = Object.keys(this._body()).length > 0;
    const harnessIds = [...new Set([...known, ...now.harnesses.ids])];
    const pinText = !ws && !this.pinned ? rulesText(this.pin) : '';
    const what = ws ? 'the workspace’s bounds on agents' : this.pinned ? `agent rules pinned on org ${this.org}` : `agent rules of org ${this.org}`;
    return html`
      ${pinText ? html`<div class="pinline" data-pin><bx-icon name="lock"></bx-icon>
        <span>pinned by a workspace admin: <b>${pinText}</b> <span class="hint">— this org’s own rules can narrow these, not loosen them</span></span></div>` : nothing}
      <div class="grid" role="group" aria-label=${what}>
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
        <span class="k">the web</span>
        <div>
          <div class="row">
            <select data-rule="web" aria-label="the web" @change=${(e) => this._set({ web: e.target.value })}>
              <option value="" ?selected=${!now.web}>no rule — a conversation holding ${ws ? 'tiles’' : 'this org’s'} data asks its person for each page</option>
              <option value="allow" ?selected=${now.web === 'allow'}>allow — such a conversation reads the web freely</option>
              <option value="ask" ?selected=${now.web === 'ask'}>ask — its person approves each page</option>
              <option value="never" ?selected=${now.web === 'never'}>never — its data never goes to the web</option>
            </select>
          </div>
          <div class="hint">for a workspace agent’s conversation that has read ${ws ? 'a tile' : 'this org’s tiles'}: an address can carry their data out</div>
        </div>
        <span class="k">MCP servers</span>
        <div class="row">
          <select data-rule="mcp" aria-label="MCP servers" @change=${(e) => this._set({ mcp: e.target.value })}>
            <option value="" ?selected=${!now.mcp}>any MCP tile; an outside server asks each call</option>
            <option value="listed" ?selected=${now.mcp === 'listed'}>also the outside servers an admin listed, as approved</option>
            <option value="org" ?selected=${now.mcp === 'org'}>only ${ws ? 'the MCP tiles in no org' : 'this org’s own MCP tiles and the servers listed for it'}</option>
            <option value="never" ?selected=${now.mcp === 'never'}>none — no MCP server</option>
          </select>
        </div>
        <div class="hint">what a call sends an outside server leaves the workspace</div>
        ${ws ? html`
          <span class="k">credentials</span>
          <div class="row">
            <select data-rule="credentials" aria-label="credentials" @change=${(e) => this._set({ credentials: e.target.value })}>
              <option value="own-first" ?selected=${now.credentials === 'own-first'}>a person’s own sign-in first, else the LLM connector</option>
              <option value="connector-first" ?selected=${now.credentials === 'connector-first'}>the LLM connector, unless a person chose their own sign-in</option>
              <option value="connector-only" ?selected=${now.credentials === 'connector-only'}>the LLM connector only — own sign-ins are not used</option>
            </select>
          </div>` : nothing}
      </div>
      ${this._err ? html`<div class="err" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
      <div class="line">
        <button class="go" data-save ?disabled=${!dirty || this._busy} @click=${() => this._save()}>Save</button>
        ${dirty ? html`<button class="quiet" @click=${() => { this._draft = {}; this._err = ''; }}>Cancel</button>` : nothing}
        ${this._saved ? html`<span class="ok"><bx-icon name="ok"></bx-icon> saved</span>` : nothing}
        <span class="hint">${ws ? 'every tile falls under these; an org can only narrow them'
          : this.pinned ? 'the org’s admins can narrow these, never loosen them'
          : 'applies to the tiles this org owns and the tiles shared into it; a tile under several orgs gets the strictest'} · sessions already running are never ended</span>
      </div>`;
  }
}
customElements.define('bx-org-agents', BxOrgAgents);
