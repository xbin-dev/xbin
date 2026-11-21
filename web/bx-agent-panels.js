/**
 * bx-agent-panels.js — the smaller pieces of xbin's chrome for the
 * workspace agent (/vendor/bx-agent-panels.js, D232), each an element the
 * dock (and /xbin/agent) hosts, each reading the document's store
 * (agent-chrome-store.js) — trusted chrome, the person's own session:
 *
 *   <bx-agent-needs>      the one batch of what waits for the person (D250):
 *                         "Waiting for you" and its count, each pending act
 *                         once, oldest first, one action each — requests,
 *                         plans and admin changes, a start that failed to
 *                         open (Try again), tile agent sessions that wait —
 *                         and Review all when more than one opens a review.
 *                         A follow-up (a message, a continue) is allowed on
 *                         its row (D251): its chips, its first lines and
 *                         Show all, Send and Deny beside Review…
 *   <bx-agent-approvals>  the standing approvals, each with Take back
 *   <bx-agent-activity>   "What my agent did": the audit log in sentences
 *                         (Changes · Refused · Everything)
 *   <bx-agent-prefs>      what a tile agent the agent starts gets — the coding
 *                         agent, each one's mode and model, what was
 *                         remembered for tiles (D250) —, whether it
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
 * Review…), bx-agent-review-all {items: [{kind, id}]} (the batch's Review
 * all), bx-agent-open-session {tile, session} (a waiting session's Open),
 * bx-agent-back (a panel's Back).
 */
import { LitElement, html, css, nothing, repeat } from '/vendor/lit-all.min.js';
import '/vendor/bx-icons.js';
import { chromeCss } from '/vendor/agent-chrome-css.js';
import { agentStore } from '/vendor/agent-chrome-store.js';
import { WidenGuard, shown, hiddenLine, signinName } from '/vendor/bx-agent-approve.js';
import { uncovered } from '/vendor/agent-armed.js';
import '/vendor/bx-agent-permits.js';
import { standingRow, STANDING_NOTE, didByDay, DID_FILTERS, DID_NOTE, clock, stateWords, setupSteps, SETUP_INTRO, dockState, sessionModes, notOffered, MODE_DEFAULT, netPrefs, CHOICES } from '/vendor/agent-chrome-words.js';
import { batch, batchTitle, rowText, withSeenText, SHEET } from '/vendor/agent-start-words.js';

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
  static properties = { _err: { state: true }, _busy: { state: true }, _all: { state: true } };
  static styles = [chromeCss, css`:host { display: block; background: var(--bx-panel-2, #262730); border-bottom: 1px solid var(--bx-border-strong, #666A7E);
    box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); max-height: 60vh; overflow: auto; }
    .hd { padding: 8px 12px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; } .hd h3 { color: var(--bx-text, #E9EAF0); } .hd .grow { flex: 1; }
    .list > li .tx { display: flex; flex-direction: column; gap: 2px; } .list > li > button { flex: none; }
    .list > li.inl { flex-wrap: wrap; } .list > li.inl .tx { flex: 1 1 100%; min-width: 0; }
    .list > li.inl .acts { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; flex: 1 1 100%; }
    pre.body.row { margin: 2px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; max-height: 40vh; overflow: auto; }
    .opening { margin-top: 4px; }`];

  // _over: the rows whose text box overflows as laid out now (measured after each render and on every resize — a row
  // not measured yet counts as overflowing); _end: the shown-whole rows whose box was scrolled to its end (D252)
  constructor() { super(); this._err = ''; this._busy = ''; this._all = new Set(); this._shownAt = new WeakMap(); this._order = ''; this._over = new Map(); this._end = new Set(); this._fitsWas = new Map(); }
  connectedCallback() {
    super.connectedCallback(); this.store.readSessions();
    this._ro = new ResizeObserver(() => this._measure());
  }
  disconnectedCallback() { super.disconnectedCallback(); this._ro?.disconnect(); this._ro = null; }
  /** rearm(): every permission card, and every row's Send and Deny, waits again (something brought the tray into view — a page's message may). */
  rearm() { for (const p of this.renderRoot.querySelectorAll('bx-agent-permits')) p.rearm(); this._shownAt = new WeakMap(); this.requestUpdate(); }
  // when each row's Send and Deny came into view: they answer as Allow on the sheet does — not before CHOICES.armMs.
  // The list changing — a row gone (the agent can withdraw its own request), one come, the order — moves rows under
  // the pointer: every row's buttons wait again then (D251 review L1)
  willUpdate() {
    const order = batch(this.store).map((r) => `${r.kind}:${r.id}`).join(' ');
    if (order !== this._order) { this._order = order; this._shownAt = new WeakMap(); }
  }
  updated() {
    for (const el of this.renderRoot.querySelectorAll('button[data-send], button[data-deny]')) if (!this._shownAt.has(el)) this._shownAt.set(el, performance.now());
    for (const el of this.renderRoot.querySelectorAll('pre[data-inline-text]')) {
      this._ro?.observe(el);
      // a Send that becomes clickable (its text scrolled to the end, or the box now showing all of it) waits as a new one
      const li = el.closest('li'), fits = el.dataset.fits, send = li.querySelector('button[data-send]');
      if (fits === 'yes' && this._fitsWas.get(li.dataset.id) !== 'yes' && send) this._shownAt.set(send, performance.now());
      this._fitsWas.set(li.dataset.id, fits);
    }
    this._measure();
  }
  // whether each row's text box overflows as it is laid out now — a row whose text the counting calls whole still
  // offers Show all, not Send, when its box doesn't show all of it on this screen (D252)
  _measure() {
    let changed = false;
    for (const el of this.renderRoot.querySelectorAll('li[data-id] pre[data-inline-text]')) {
      // (not laid out — the tray hidden: not known to fit, so not offered)
      const id = el.closest('li').dataset.id, over = el.clientHeight === 0 || el.scrollHeight > el.clientHeight + 1;
      if (this._over.get(id) !== over) { this._over.set(id, over); changed = true; }
      if (!over) this._atEnd(el, id);
    }
    if (changed) this.requestUpdate();
  }
  _atEnd(el, id) {
    if (this._end.has(id) || el.scrollTop + el.clientHeight < el.scrollHeight - 2) return;
    this._end = new Set([...this._end, id]);
  }
  // a row's state for Send: 'send' — all of its text is on screen (it fits its box, or it was shown whole and its box
  // scrolled to its end); 'scroll' — shown whole in a box that must be scrolled to its end first; 'show' — Show all first
  _sendable(r) {
    const all = this._all.has(r.id), over = this._over.get(r.id) !== false;
    if (!rowText(r.inline.text, all, r.inline.byXbin).whole) return 'show';
    if (!over) return 'send';
    if (!all) return 'show';
    return this._end.has(r.id) ? 'send' : 'scroll';
  }
  _armedClick(e) {
    const el = e.currentTarget, at = this._shownAt.get(el);
    return e.isTrusted && !this._busy && at != null && performance.now() - at >= CHOICES.armMs && document.hasFocus() && uncovered(el);
  }

  _act(r) {
    if (r.kind === 'session') return this.fire('bx-agent-open-session', { tile: r.tile, session: r.id });
    return this.fire('bx-agent-review', { kind: r.kind === 'retry' ? 'request' : r.kind, id: r.id });
  }

  // Send on a follow-up's row (D251): the person's own click, on a button in view for a moment, uncovered, in a window
  // that has their attention — never Enter, never a script. What the row showed changed since: its sheet shows it as it
  // is now (nothing was decided)
  async _send(e, r) {
    if (!this._armedClick(e) || this._sendable(r) !== 'send') return; // all of it on screen, or no Send (D251 review H1, D252)
    this._busy = r.id; this._err = '';
    // with how much of the text was on screen when its person clicked: xbind allows only all of it (D252)
    const res = await this.store.approve(r.id, withSeenText(r.inline.body, rowText(r.inline.text, this._all.has(r.id), r.inline.byXbin).shown));
    this._busy = '';
    if (res.ok || res.gone) return;
    if (res.changed) { this.fire('bx-agent-review', { kind: 'request', id: r.id }); return; }
    this._err = res.error;
  }

  async _deny(e, r) {
    if (!this._armedClick(e)) return;
    this._busy = r.id; this._err = '';
    const res = await this.store.deny(r.id);
    this._busy = '';
    if (!res.ok && !res.gone) this._err = res.error;
  }

  // the follow-up's text: whole when it fits the row (rowText) or once Show all — the goal first, the brief's
  // standing first paragraph after it — else its first lines and how long it is
  _inline(r) {
    const t = rowText(r.inline.text, this._all.has(r.id), r.inline.byXbin);
    const state = this._sendable(r);
    return html`${hiddenLine(r.inline.text)}<pre class="body row small" data-inline-text data-whole=${t.whole ? 'yes' : 'no'} data-fits=${state === 'send' ? 'yes' : 'no'} tabindex="-1"
        @scroll=${(e) => { const was = this._end.has(r.id); this._atEnd(e.currentTarget, r.id); if (!was && this._end.has(r.id)) this.requestUpdate(); }}>${shown(t.lead)}${t.whole ? '' : '…'}</pre>
      ${t.whole && t.opening ? html`<div class="small muted opening" data-inline-opening>${SHEET.opensWith}${shown(t.opening)}</div>` : nothing}
      ${t.note ? html`<span class="small muted" data-inline-note>${t.note}</span>` : nothing}
      ${state === 'scroll' ? html`<span class="small muted" data-inline-scroll>Scroll to the end of the text to send it</span>` : nothing}`;
  }

  // Show all: the whole text, in place; Send comes in its place (armed a moment after it shows)
  async _showAll(e, r) {
    if (!e.isTrusted) return;
    this._all = new Set([...this._all, r.id]);
    await this.updateComplete;
    this.renderRoot.querySelector(`li[data-id="${r.id}"] [data-inline-text]`)?.focus();
  }

  render() {
    const rows = batch(this.store);
    const t = batchTitle(rows);
    return html`<div class="hd"><h3 data-batch-title>${t.title}</h3>${t.count ? html`<span class="count" data-batch-count>${t.count}</span>` : nothing}
        <span class="small muted grow">oldest first</span>
        ${t.reviewAll ? html`<button class="primary" data-review-all @click=${() => this.fire('bx-agent-review-all', { items: rows.filter((r) => r.review).map((r) => ({ kind: r.kind, id: r.id })) })}>${t.reviewAll}</button>` : nothing}</div>
      ${rows.length ? html`<ul class="list" aria-label=${t.title}>${repeat(rows, (r) => `${r.kind}:${r.id}`, (r) => html`<li data-kind=${r.kind} data-id=${r.id} class=${r.inline ? 'inl' : ''}
          data-inline=${r.inline ? r.inline.kind : nothing}>
        <span class="tx"><b>${r.title}</b>
          ${r.chips.length ? html`<span class="chips" data-chips>${r.chips.map((c) => html`<span class="chip">${c}</span>`)}${signinName(r.signInId)}</span>` : nothing}
          ${r.inline ? this._inline(r) : nothing}
          ${r.parts.length ? html`<span class="parts small muted" data-parts>${r.parts.map((p) => html`<span>${p}</span>`)}</span>` : nothing}
          ${r.permits ? html`<bx-agent-permits data-permits .session=${r.id} .agentName=${r.agentName} @bx-agent-permitted=${() => this.store.readSessions()}></bx-agent-permits>` : nothing}</span>
        ${r.inline ? html`<span class="acts"><button data-act @click=${() => this._act(r)}>Review…</button>
            <button data-deny ?disabled=${this._busy === r.id} @click=${(e) => this._deny(e, r)} @keydown=${(e) => { if (e.key === 'Enter') e.preventDefault(); }}>Deny</button>
            ${this._sendable(r) !== 'show'
              ? html`<button class="primary" data-send ?disabled=${this._busy === r.id || this._sendable(r) !== 'send'} @click=${(e) => this._send(e, r)} @keydown=${(e) => { if (e.key === 'Enter') e.preventDefault(); }}>${r.action}</button>`
              : html`<button class="primary" data-inline-all @click=${(e) => this._showAll(e, r)}>Show all</button>`}</span>`
          : r.kind === 'retry' ? html`<button data-retry @click=${() => this.fire('bx-agent-review', { kind: 'request', id: r.id })}>${r.action}</button>`
          : html`<button data-act @click=${() => this._act(r)}>${r.action}</button>`}</li>`)}</ul>`
        : html`<div class="empty" data-empty>Nothing waits for you</div>`}
      ${this._err ? html`<div class="banner bad" role="alert"><bx-icon name="error"></bx-icon><span>${this._err}</span></div>` : nothing}`;
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
    label.mode select, label.mode input { flex: 1 1 160px; min-width: 0; max-width: 100%; } label.mode.sub { padding-left: 16px; }
    ul.outside { margin: 0 0 0 24px; padding-left: 16px; overflow-wrap: anywhere; }`];
  // what widens the agent's reach — starting without asking, the internet, other tiles' APIs, the admin opt-in — answers
  // only a deliberate click, as Allow does (bx-agent-approve.js WidenGuard)
  constructor() { super(); this._prefs = null; this._provs = []; this._err = ''; this._widen = new WidenGuard(this); }
  connectedCallback() {
    super.connectedCallback();
    this.store.prefs().then((p) => { this._prefs = p || {}; });
    this.store.providers().then((p) => { this._provs = p; });
  }

  async _save(patch, top = false) {
    this._err = '';
    const r = await this.store.setPrefs(top ? patch : { agentSessions: patch });
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
          <span>Start without asking<div class="small muted">Only where the mode below asks before acting, with the network below; in any other mode each start still comes to you. You are told in the chat.</div></span></label></div>
      <div class="sec" data-modes><h3>Sessions my workspace agent starts</h3>
        <div class="small muted">What its approval preselects, and what a start without asking runs. On the approval you can always pick another; a choice you remember for a tile comes first there.</div>
        <label class="mode"><span class="nm">Coding agent</span>
          <select data-pref-harness aria-label="The coding agent sessions my agent starts run on" @change=${(e) => this._save({ harness: e.target.value || null })}>
            <option value="" ?selected=${!a.harness}>The workspace's default</option>
            ${(this._provs || []).map((p) => html`<option value=${p.id} ?selected=${p.id === a.harness}>${p.name || p.id}</option>`)}</select></label>
        ${sessionModes(this._provs, a.modes).map((r) => html`<div class="small muted">${r.name}</div>
          <label class="mode sub"><span class="nm">Mode</span>
          <select data-mode=${r.id} aria-label=${`Mode for ${r.name}`} @change=${(e) => this._save({ modes: { [r.id]: e.target.value || null } })}>
            <option value="" ?selected=${!r.value}>${MODE_DEFAULT}</option>
            ${r.options.map((o) => html`<option value=${o.id} ?selected=${o.id === r.value}>${o.name}</option>`)}</select></label>
          <label class="mode sub"><span class="nm">Model</span>
            <input data-model=${r.id} aria-label=${`Model for ${r.name}`} placeholder="its default" .value=${((this._prefs.harnesses || {})[r.id] || {}).model || ''}
              @change=${(e) => this._save({ harnesses: { [r.id]: { model: e.target.value.trim() || null } } }, true)}></label>
          ${r.never.length ? html`<div class="small muted" data-never=${r.id}>${notOffered(r.never)}</div>` : nothing}`)}
        <div class="small muted">A model here is also what your own sessions of that coding agent start on.</div>
        <label class="row"><input type="checkbox" data-net data-widen="off" .checked=${!a.net || a.net === 'none'} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${(e) => this._save({ net: e.target.checked ? 'none' : null })}><span>Its model only. No internet.</span></label>
        <label class="row"><input type="checkbox" data-api data-widen="on" .checked=${a.api !== false} @click=${this._widen.click} @keydown=${this._widen.key}
          @change=${(e) => this._save({ api: e.target.checked })}><span>Other tiles: it may call the tile APIs its tile is granted</span></label></div>
      ${Object.keys(a.tiles || {}).length ? html`<div class="sec" data-remembered><h3>Remembered for tiles</h3>
        <div class="small muted">What you asked an approval to remember for a tile. It comes first on that tile's approvals.</div>
        <ul class="list">${Object.entries(a.tiles).sort(([x], [y]) => x.localeCompare(y)).map(([tile, c]) => html`<li data-tile=${tile}>
          <span class="tx"><b class="mono">${tile}</b><span class="chips">${[c.harness, c.model ? `model ${c.model}` : '', c.mode ? `mode ${c.mode}` : ''].filter(Boolean).map((x) => html`<span class="chip">${x}</span>`)}</span></span>
          <button data-forget=${tile} @click=${() => this._save({ tiles: { [tile]: null } })}>Forget</button></li>`)}</ul></div>` : nothing}
      <div class="sec" data-net-tools><h3>The web and MCP tools</h3>
        ${netPrefs(st).map((x) => html`<label class="row ${x.sub ? 'sub' : ''}"><input type="checkbox" data-net-pref=${x.id} data-widen="on" .checked=${x.on} ?disabled=${!x.allowed}
            @click=${this._widen.click} @keydown=${this._widen.key} @change=${(e) => this._net(e, x.id)}>
          <span><b>${x.label}</b><div class="small muted">${x.allowed ? x.note : x.why}</div></span></label>
        ${x.allowed && x.servers && x.servers.length ? html`<ul class="small outside" data-mcp-servers aria-label="MCP servers outside this workspace">${x.servers.map((s) => html`<li><b>${s.name}</b> <span class="mono muted">${s.host}</span></li>`)}</ul>` : nothing}`)}</div>
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
    const w = stateWords(kind, { admins: st.admins, at: kind === 'paused' ? st.pausedAt : st.workspaceAt, held: st.held, why: st.heldWhy, host: !!(st.locks && st.locks.enabled) });
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
