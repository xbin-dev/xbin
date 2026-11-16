/**
 * bx-agent-panels.js — the smaller pieces of xbin's chrome for the
 * workspace agent (/vendor/bx-agent-panels.js, D232), each an element the
 * dock (and /xbin/agent) hosts, each reading the document's store
 * (agent-chrome-store.js) — trusted chrome, the person's own session:
 *
 *   <bx-agent-needs>      the Needs-you tray: requests, proposed change sets,
 *                         tile agent sessions that wait — oldest first
 *   <bx-agent-approvals>  the standing approvals, each with Take back
 *   <bx-agent-activity>   "What my agent did": the audit log in sentences
 *                         (Changes · Refused · Everything)
 *   <bx-agent-prefs>      what a tile agent the agent starts gets, whether it
 *                         reads the web and uses MCP tools, and — for
 *                         admins — the opt-in to proposing admin changes;
 *                         opened from the dock's own menu, never by the
 *                         agent's page, and every control that widens the
 *                         agent's reach armed like Allow (WidenGuard)
 *   <bx-agent-setup>      what the dock shows in place of the agent's page:
 *                         the three setup steps (an admin, before setup),
 *                         not available, turned off, paused, held
 *
 * Events (bubbling, composed): bx-agent-review {kind, id} (a row's
 * Review…), bx-agent-open-session {tile, session} (a waiting session's
 * Open), bx-agent-back (a panel's Back).
 */
import { LitElement, html, css, nothing } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { chromeCss } from '/vendor/agent-chrome-css.js';
import { agentStore } from '/vendor/agent-chrome-store.js';
import { WidenGuard } from '/vendor/bx-agent-approve.js';
import { needs, standingRow, STANDING_NOTE, didByDay, DID_FILTERS, DID_NOTE, clock, stateWords, setupSteps, SETUP_INTRO, dockState, sessionModes, neverAsks, netPrefs } from '/vendor/agent-chrome-words.js';

// an element that draws the store: it subscribes while it is in the page
class StoreView extends LitElement {
  constructor() { super(); this.store = agentStore(); this._off = null; }
  connectedCallback() { super.connectedCallback(); this._off = this.store.subscribe(() => this.requestUpdate()); }
  disconnectedCallback() { super.disconnectedCallback(); this._off?.(); this._off = null; }
  fire(type, detail = {}) { this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true })); }
  head(title, extra = nothing) {
    return html`<div class="ph"><button class="quiet" data-back aria-label="Back to the conversation" @click=${() => this.fire('bx-agent-back')}><bx-icon name="arrow-left"></bx-icon></button><h2>${title}</h2>${extra}</div>`;
  }
}

export class BxAgentNeeds extends StoreView {
  static styles = [chromeCss, css`:host { display: block; background: var(--bx-panel-2, #262730); border-bottom: 1px solid var(--bx-border-strong, #666A7E);
    box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); max-height: 60vh; overflow: auto; } .hd { padding: 8px 12px 0; display: flex; gap: 8px; align-items: baseline; }`];

  connectedCallback() { super.connectedCallback(); this.store.readSessions(); }

  render() {
    const rows = needs(this.store);
    return html`<div class="hd"><h3>Needs you</h3><span class="small muted">oldest first · approvals expire</span></div>
      ${rows.length ? html`<ul class="list" aria-label="Needs you">${rows.map((r) => html`<li data-kind=${r.kind} data-id=${r.id}>
        <span class="tx"><b>${r.title}</b>${r.note ? html`<div class="small muted">${r.note}</div>` : nothing}</span>
        <button @click=${() => (r.kind === 'session' ? this.fire('bx-agent-open-session', { tile: r.tile, session: r.id }) : this.fire('bx-agent-review', { kind: r.kind, id: r.id }))}>${r.action}</button></li>`)}</ul>`
        : html`<div class="empty" data-empty>Nothing needs you</div>`}`;
  }
}

export class BxAgentApprovals extends StoreView {
  static properties = { _busy: { state: true }, _err: { state: true } };
  static styles = [chromeCss, css`:host { display: flex; flex-direction: column; min-height: 0; flex: 1; }`];
  constructor() { super(); this._busy = ''; this._err = ''; }
  connectedCallback() { super.connectedCallback(); this.store.readApprovals(); }

  async _take(e, id) {
    if (!e.isTrusted || this._busy) return;
    this._busy = id; this._err = '';
    const r = await this.store.takeBack(id);
    this._busy = '';
    if (!r.ok) this._err = r.error;
  }

  render() {
    const list = this.store.approvals || [];
    return html`<div class="panel">${this.head('Approvals', html`<span class="small muted">${list.length} standing</span>`)}
      <div class="pb">${list.length ? html`<ul class="list">${list.map((a) => { const r = standingRow(a); return html`<li data-id=${a.id} data-scope=${a.scope}>
          <span class="tx"><b>${r.tile}</b> <span class="mono small muted">${r.path}</span>
            <div>${r.wider ? html`<bx-icon name="warning" label="wider"></bx-icon> ` : nothing}${r.text}</div><div class="small muted">${[r.since, r.until].filter(Boolean).join(' · ')}</div></span>
          <button ?disabled=${this._busy === a.id} @click=${(e) => this._take(e, a.id)}>Take back</button></li>`; })}</ul>`
        : html`<div class="empty" data-empty>No standing approvals. Your agent asks each time.</div>`}
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}</div>
      <div class="pf small muted">${STANDING_NOTE}</div></div>`;
  }
}

export class BxAgentActivity extends StoreView {
  static properties = { _filter: { state: true }, _rows: { state: true }, _err: { state: true }, _more: { state: true } };
  static styles = [chromeCss, css`:host { display: flex; flex-direction: column; min-height: 0; flex: 1; } .day { padding: 8px 12px 0; } .when { flex: none; font-variant-numeric: tabular-nums; }`];
  constructor() { super(); this._filter = 'change'; this._rows = null; this._err = ''; this._more = false; }
  connectedCallback() { super.connectedCallback(); this._load(); }

  async _load(before = '') {
    const r = await this.store.audit(before);
    if (!r.ok) { this._err = r.error; this._rows = this._rows || []; return; }
    this._rows = before ? [...(this._rows || []), ...r.calls] : r.calls;
    this._more = r.calls.length >= 200;
  }

  render() {
    const days = didByDay(this._rows || [], this._filter);
    return html`<div class="panel">${this.head('What my agent did')}
      <div class="ph"><div class="seg" role="group" aria-label="Show">${DID_FILTERS.map((f) => html`<button aria-pressed=${this._filter === f.id ? 'true' : 'false'} data-filter=${f.id}
        @click=${() => { this._filter = f.id; }}>${f.label}</button>`)}</div></div>
      <div class="pb">${this._rows == null ? html`<div class="empty">Reading…</div>` : !days.length ? html`<div class="empty" data-empty>Nothing here yet.</div>`
        : days.map((d) => html`<h3 class="day">${d.day}</h3><ul class="list">${d.rows.map((r) => html`<li data-group=${r.group}><bx-icon name=${r.icon}></bx-icon>
            <span class="tx">${r.text}<b>${r.strong}</b>${r.detail ? html`<div class="small muted mono">${r.detail}</div>` : nothing}</span><span class="when small muted">${r.at ? clock(r.at) : ''}</span></li>`)}</ul>`)}
        ${this._more ? html`<div class="empty"><button @click=${() => this._load(this._rows[this._rows.length - 1].ts)}>Earlier</button></div>` : nothing}
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}</div>
      <div class="pf small muted">${DID_NOTE}</div></div>`;
  }
}

export class BxAgentPrefs extends StoreView {
  static properties = { _prefs: { state: true }, _provs: { state: true }, _err: { state: true } };
  static styles = [chromeCss, css`:host { display: flex; flex-direction: column; min-height: 0; flex: 1; } .sec { padding: 12px; display: flex; flex-direction: column; gap: 8px; border-bottom: 1px solid var(--bx-border, #33353F); }
    label.row { display: flex; gap: 8px; align-items: flex-start; } label.row input { margin-top: 3px; } label.row.sub { padding-left: 24px; }
    label.mode { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; } label.mode .nm { flex: 1 1 120px; min-width: 0; }
    label.mode select { flex: 1 1 160px; min-width: 0; max-width: 100%; }`];
  // what widens the agent's reach — starting without asking, the internet, other tiles' APIs, the admin opt-in — answers
  // only a deliberate click, as Allow does (bx-agent-approve.js WidenGuard)
  constructor() { super(); this._prefs = null; this._provs = []; this._err = ''; this._widen = new WidenGuard(this); }
  connectedCallback() {
    super.connectedCallback();
    this.store.prefs().then((p) => { this._prefs = p || {}; });
    this.store.providers().then((p) => { this._provs = p; });
  }

  async _save(patch) {
    this._err = '';
    const r = await this.store.setPrefs({ agentSessions: patch });
    if (r.ok) this._prefs = r.prefs; else this._err = r.error;
  }

  async _admin(e) {
    if (!e.isTrusted) return;
    const r = await this.store.setAdminChanges(e.target.checked);
    if (!r.ok) { this._err = r.error; e.target.checked = !e.target.checked; }
  }

  // the web and MCP tools (D239): the person's own toggles, within what the workspace policy allows
  async _net(e, id) {
    if (!e.isTrusted) return;
    this._err = '';
    const r = await this.store.setNet({ [id]: e.target.checked });
    if (!r.ok) { this._err = r.error; e.target.checked = !e.target.checked; }
  }

  render() {
    const a = (this._prefs && this._prefs.agentSessions) || {};
    const st = this.store.state || {};
    const start = a.start === 'allow' ? 'allow' : 'ask';
    return html`<div class="panel">${this.head('Preferences')}<div class="pb">
      ${this._prefs == null ? html`<div class="empty">Reading…</div>` : html`
      <div class="sec" role="radiogroup" aria-labelledby="ps"><h3 id="ps">When my agent wants to start a tile agent</h3>
        <label class="row"><input type="radio" name="start" data-start="ask" .checked=${start === 'ask'} @change=${() => this._save({ start: 'ask' })}><span>Ask me each time</span></label>
        <label class="row"><input type="radio" name="start" data-start="allow" data-widen="on" .checked=${start === 'allow'} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${() => this._save({ start: 'allow' })}>
          <span>Start without asking<div class="small muted">Only in a mode that asks before acting, with the network below. You are told in the chat.</div></span></label></div>
      <div class="sec" data-modes><h3>Started tile agents get</h3>
        <div class="small muted">The mode each coding agent starts in. Only modes that ask you before acting are offered.</div>
        ${sessionModes(this._provs, a.modes).map((r) => html`<label class="mode"><span class="nm">${r.name}</span>
          <select data-mode=${r.id} aria-label=${`Mode for ${r.name}`} @change=${(e) => this._save({ modes: { [r.id]: e.target.value || null } })}>
            <option value="" ?selected=${!r.value}>Its own asking mode</option>
            ${r.options.map((o) => html`<option value=${o.id} ?selected=${o.id === r.value}>${o.name}</option>`)}</select></label>
          ${r.never.length ? html`<div class="small muted" data-never=${r.id}>${neverAsks(r.never)}</div>` : nothing}`)}
        <label class="row"><input type="checkbox" data-net data-widen="off" .checked=${!a.net || a.net === 'none'} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${(e) => this._save({ net: e.target.checked ? 'none' : null })}><span>Its model only. No internet.</span></label>
        <label class="row"><input type="checkbox" data-api data-widen="on" .checked=${a.api !== false} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${(e) => this._save({ api: e.target.checked })}><span>Other tiles: it may call the tile APIs its tile is granted</span></label></div>
      <div class="sec" data-net-tools><h3>The web and MCP tools</h3>
        ${netPrefs(st).map((x) => html`<label class="row ${x.sub ? 'sub' : ''}"><input type="checkbox" data-net-pref=${x.id} data-widen="on" .checked=${x.on} ?disabled=${!x.allowed}
            @click=${this._widen.click} @keydown=${this._widen.key} @change=${(e) => this._net(e, x.id)}>
          <span><b>${x.label}</b><div class="small muted">${x.allowed ? x.note : x.why}</div></span></label>`)}</div>
      ${st.canAdminChanges ? html`<div class="sec" data-admin><h3>Admin changes</h3>
        <label class="row"><input type="checkbox" data-admin-changes data-widen="on" .checked=${!!st.adminChanges} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${this._admin}><span><b>Let my agent propose admin changes</b>
          <div class="small muted">It can read grants, bindings, org shares and limits, and propose changes in a batch. Nothing is applied until you approve the batch here.</div></span></label></div>` : nothing}`}
      ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}
    </div></div>`;
  }
}

export class BxAgentSetup extends StoreView {
  static properties = { _busy: { state: true }, _err: { state: true }, _copied: { state: true } };
  static styles = [chromeCss, css`:host { display: block; padding: 16px 12px; overflow: auto; } .st { display: flex; flex-direction: column; gap: 12px; max-width: 560px; }
    ol { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
    ol > li { display: flex; gap: 12px; align-items: flex-start; padding: 8px 12px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel, #1F2028); }
    ol > li.off { opacity: .55; } ol > li .n { flex: none; width: 20px; height: 20px; display: inline-flex; align-items: center; justify-content: center; border: 1px solid var(--bx-border-strong, #666A7E); }
    ol > li.done .n { color: var(--bx-ok, #A3CF5E); border-color: currentColor; } ol > li .tx { flex: 1; min-width: 0; } a { color: var(--bx-link, #8C9BFF); }`];
  constructor() { super(); this._busy = false; this._err = ''; this._copied = false; }

  async _do(e, fn) {
    if (!e.isTrusted || this._busy) return;
    this._busy = true; this._err = '';
    const r = await fn();
    this._busy = false;
    if (r && r.ok === false) this._err = r.error;
  }

  render() {
    const s = this.store, kind = dockState(s);
    if (kind === 'page' || kind === 'hidden') return nothing;
    if (kind === 'setup') {
      const steps = setupSteps(s);
      return html`<div class="st" data-state="setup"><h2>${SETUP_INTRO.title}</h2><p class="muted">${SETUP_INTRO.text}</p>
        <ol>${steps.map((x, i) => html`<li class="${x.done ? 'done' : ''} ${x.enabled ? '' : 'off'}" data-step=${x.id}>
          <span class="n" aria-hidden="true">${x.done ? html`<bx-icon name="check"></bx-icon>` : i + 1}</span>
          <span class="tx"><b>${x.title}</b>${x.done ? html` <span class="tag ok">${x.tag || 'done'}</span>` : nothing}<div class="small muted">${x.text}</div></span>
          ${!x.action || !x.enabled ? nothing : x.id === 'model' ? html`<a href="/xbin/llm" target="_top">${x.action}</a>`
            : x.id === 'on' ? html`<button class="primary" ?disabled=${this._busy} @click=${(e) => this._do(e, () => s.setWorkspace(true))}>${x.action}</button>`
              : html`<a href="/docs/workspace-agent.md#running-it" target="_blank" rel="noopener">${x.action}</a>`}
          ${(x.choices || []).map((c) => html`<button class=${c.primary ? 'primary' : ''} data-who=${c.who} ?disabled=${this._busy} @click=${(e) => this._do(e, () => s.setWho(c.who))}>${c.label}</button>`)}</li>`)}</ol>
        ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}</div>`;
    }
    const st = s.state || {};
    const w = stateWords(kind, { admins: st.admins, at: kind === 'paused' ? st.pausedAt : st.workspaceAt, held: st.held, why: st.heldWhy });
    const ask = `The workspace agent needs a language model and none is connected. Could you connect one? (${location.origin}/xbin/llm)`;
    return html`<div class="st" data-state=${kind}>
      <div class="banner ${kind === 'paused' || kind === 'unavailable' || kind === 'held' ? 'warn' : ''}" role="status"><bx-icon name=${kind === 'paused' ? 'pause' : kind === 'soon' ? 'info' : 'warning'}></bx-icon>
        <span><b>${w.title}</b> ${w.text}${w.more ? html`<div class="small">${w.more}</div>` : nothing}</span></div>
      <div class="btns" style="justify-content:flex-start">
        ${kind === 'paused' ? html`<button class="primary" data-resume ?disabled=${this._busy} @click=${(e) => this._do(e, () => s.setPaused(false))}>Resume</button>` : nothing}
        ${kind === 'unavailable' ? html`<button data-copy @click=${() => navigator.clipboard?.writeText(ask).then(() => { this._copied = true; })}>${this._copied ? 'Copied' : 'Copy a message for an admin'}</button>` : nothing}
        ${kind === 'unavailable' || kind === 'held' || kind === 'offline' ? html`<button data-again @click=${() => s.refresh()}>${kind === 'offline' ? 'Try now' : 'Check again'}</button>` : nothing}
      </div>
      ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}</div>`;
  }
}

for (const [name, cls] of [['bx-agent-needs', BxAgentNeeds], ['bx-agent-approvals', BxAgentApprovals], ['bx-agent-activity', BxAgentActivity], ['bx-agent-prefs', BxAgentPrefs], ['bx-agent-setup', BxAgentSetup]]) {
  if (!customElements.get(name)) customElements.define(name, cls);
}
