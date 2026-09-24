/**
 * bx-agent.js — the Agent tab in the terminal window (<bx-agent
 * session=<id> component=<cwd>>). An agent session is a terminal session
 * whose sandbox runs a coding agent over the Agent Client Protocol (D74,
 * docs/overview/09-terminals.md §Agent sessions); this element drives it
 * through the agent API and renders its typed event log.
 *
 * Source of truth is the session's event log: on mount the element replays
 * it (`GET …/events?since=0`), then applies live `session` events for its
 * topic. Because the events hub drops a slow subscriber and reconnects
 * silently, the element never trusts the live stream alone — it re-fetches
 * `?since=<last>` whenever a live seq skips, when the tab becomes visible,
 * and every 2 s while a turn runs. Any attached client (this tab, another
 * browser, `bx agent`) sees the same stream, and the first to answer a
 * permission request wins.
 *
 * Events: 'bx-session' (detail {id, kind:'agent'}) when it creates the
 * session, so the frame records the id; 'bx-exit' when the session ends.
 */
import { LitElement, html, css, nothing } from 'lit';
import { repeat } from 'lit';
import { onEvent } from '/vendor/events-socket.js';
import { md } from '/vendor/bx-md.js';
import { newTool, foldTool, headline, isPlanApproval, rawText, formFields, missingRequired } from '/vendor/agent-tools.js';
import { toolCard, permCard, changesCard, askCard, cardsCss } from '/vendor/agent-cards.js';
import { slashQuery, matchCommands, commandHint } from '/vendor/agent-slash.js';

export class BxAgent extends LitElement {
  static properties = {
    session: { type: String },
    component: { type: String },
    provider: { type: String }, // set by the launcher: create eagerly so the model picker loads before the first prompt
    mode: { type: String },
    history: { type: String }, // a PAST session id (GET /agent/history): its persisted transcript, read-only
    resume: { type: String }, // a past session id to reopen when creating (POST /term/sessions {resume})
    ended: { type: Boolean }, // the session is gone (the frame keeps the tab): no polling, the transcript stays
    _historyMeta: { state: true },
    _events: { state: true },
    _providers: { state: true },
    _provider: { state: true },
    _mode: { state: true },
    _draft: { state: true },
    _truncated: { state: true },
    _error: { state: true },
    _authErr: { state: true }, // the last create/turn failed auth (show the sign-in banner)
    _followUp: { state: true }, // {text, after}: plan feedback to send once the rejected turn settles
    _slashSel: { state: true }, // the highlighted slash command
    _slashOff: { state: true }, // Escape closed the menu (until the draft changes)
  };

  static styles = [cardsCss, css`
    :host { display: flex; flex-direction: column; height: 100%; min-height: 0;
      background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      font: 13px/1.5 var(--bx-sans, system-ui, sans-serif); }
    .scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 10px 12px; }
    .row { margin: 0 0 10px; }
    .who { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase;
      letter-spacing: .04em; color: var(--bx-muted, #868f9a); margin-bottom: 2px; }
    .user .bubble { background: var(--bx-panel-2, #2b3038); border-radius: 8px; padding: 6px 10px; white-space: pre-wrap; }
    .agent .bubble > :first-child { margin-top: 0; }
    .agent .bubble > :last-child { margin-bottom: 0; }
    .bubble :is(pre, code) { font-family: var(--bx-mono, ui-monospace, monospace); }
    .bubble pre { background: var(--bx-term-bg, #262c36); padding: 8px 10px; border-radius: 6px; overflow-x: auto; }
    .bubble :not(pre) > code { background: var(--bx-term-bg, #262c36); padding: .1em .3em; border-radius: 3px; }
    .bubble a { color: var(--bx-accent, #f5a623); }
    .md-img { color: var(--bx-muted, #868f9a); font-style: italic; }
    .thought { color: var(--bx-muted, #868f9a); border-left: 2px solid var(--bx-border, #363c45); padding-left: 8px; }
    .thought > summary { list-style: none; cursor: pointer; font: 11px var(--bx-mono, ui-monospace, monospace); }
    .thought > summary::-webkit-details-marker { display: none; }
    .thought > summary::before { content: '▸ '; } .thought[open] > summary::before { content: '▾ '; }
    .activity { font: 11px var(--bx-mono, ui-monospace, monospace); margin: 2px 0 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .shimmer { color: var(--bx-muted, #868f9a); background: linear-gradient(90deg, var(--bx-muted, #868f9a) 30%, var(--bx-text, #d4d9e0) 50%, var(--bx-muted, #868f9a) 70%);
      background-size: 250% 100%; -webkit-background-clip: text; background-clip: text; -webkit-text-fill-color: transparent; animation: shimmer 1.8s linear infinite; }
    @keyframes shimmer { from { background-position: 100% 0; } to { background-position: -150% 0; } }
    @media (prefers-reduced-motion: reduce) { .shimmer { animation: none; -webkit-text-fill-color: currentColor; background: none; } }
    .thought .md { font-style: italic; font-size: 12px; }
    .thought .md > :first-child { margin-top: 4px; } .thought .md > :last-child { margin-bottom: 0; }
    .plan { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 10px; margin: 0 0 8px; }
    .plan .h { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; color: var(--bx-muted, #868f9a); margin-bottom: 4px; }
    .plan li { list-style: none; margin: 1px 0; }
    .plan .done { color: var(--bx-green, #4caf50); text-decoration: line-through; opacity: .7; }
    .gap { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); text-align: center; margin: 4px 0; }
    .turn { border-top: 1px dashed var(--bx-border, #363c45); margin: 10px 0; padding-top: 4px;
      font: 10px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); text-align: center; }
    .foot { flex: none; border-top: 1px solid var(--bx-border, #363c45); padding: 6px 8px; }
    .status { font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a);
      display: flex; align-items: center; gap: 8px; margin-bottom: 5px; min-height: 14px; }
    .status .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--bx-muted, #868f9a); flex: none; }
    .status .dot.running, .status .dot.waiting_permission { background: var(--bx-amber, #f2a71b); }
    .status .dot.idle { background: var(--bx-green, #4caf50); }
    .status .dot.error, .status .dot.exited { background: var(--bx-red, #ef5350); }
    .status .err { color: var(--bx-red, #ef5350); }
    .compose { display: flex; gap: 6px; align-items: flex-end; }
    .slash { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; background: var(--bx-panel-2, #2b3038);
      margin-bottom: 5px; max-height: 220px; overflow-y: auto; font-size: 12px; }
    .slash .sc { display: flex; gap: 8px; align-items: baseline; padding: 3px 8px; cursor: pointer; }
    .slash .sc.on { background: color-mix(in srgb, var(--bx-accent, #f5a623) 18%, transparent); }
    .slash .sc b { font: 600 12px var(--bx-mono, ui-monospace, monospace); color: var(--bx-text, #d4d9e0); white-space: nowrap; }
    .slash .sc .d { color: var(--bx-muted, #868f9a); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
    .slash .sc .h { color: var(--bx-muted, #868f9a); font: 10.5px var(--bx-mono, ui-monospace, monospace); white-space: nowrap; }
    .slash-hint { font: 11px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); margin-bottom: 4px; }
    .compose textarea { flex: 1; resize: none; background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 8px;
      font: 13px var(--bx-sans, system-ui); max-height: 40vh; }
    .compose button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0);
      border-radius: 6px; padding: 6px 12px; cursor: pointer; font: 12px var(--bx-sans, system-ui); }
    .compose button.cancel { border-color: var(--bx-red, #ef5350); }
    .chooser { display: flex; gap: 6px; margin-bottom: 6px; flex-wrap: wrap; }
    .chooser label { display: inline-flex; align-items: center; gap: 4px; font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); }
    .chooser select { background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 5px; padding: 3px 6px; font: 12px var(--bx-mono, ui-monospace, monospace); }
    .hint { color: var(--bx-muted, #868f9a); font-size: 12px; }
    .signin { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; padding: 6px 8px;
      border: 1px solid var(--bx-amber, #f2a71b); border-radius: 5px; background: var(--bx-panel-2, #2b3038);
      font: 11px var(--bx-mono, ui-monospace, monospace); color: var(--bx-text, #d4d9e0); }
    .signin .msg { flex: 1; }
    .signin button { border: 1px solid var(--bx-amber, #f2a71b); background: var(--bx-amber, #f2a71b); color: #1b1e24;
      border-radius: 5px; padding: 3px 10px; font-weight: 700; cursor: pointer; font: inherit; white-space: nowrap; }
    .status.ended button { margin-left: auto; border: 1px solid var(--bx-accent, #f5a623); background: transparent; color: var(--bx-accent, #f5a623);
      border-radius: 5px; padding: 2px 8px; cursor: pointer; font: 11px var(--bx-mono, ui-monospace, monospace); font-weight: 600; white-space: nowrap; }
    .status.ended button:hover { background: var(--bx-accent, #f5a623); color: #1b1e24; }
  `];

  constructor() {
    super();
    this._events = [];
    this._providers = null;
    this._provider = '';
    this._mode = '';
    this._draft = '';
    this._truncated = false;
    this._error = '';
    this._authErr = false;
    this._followUp = null;
    this._slashSel = 0;
    this._slashOff = false;
    this._askVals = {}; // eid → the form's values while a question is open
    this._planFeedback = {}; // pid → the feedback typed beside "keep planning"
    this._started = false; // guard: create the eager session at most once
    this._lastSeq = 0;
    this._off = null;
    this._poll = null;
    this._creating = false;
    this._onVisible = () => { if (document.visibilityState === 'visible' && this.session) this._refetch(); };
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => this._live(e));
    document.addEventListener('visibilitychange', this._onVisible);
    if (this.history) { this._loadHistory(); return; }
    if (this.session) { this._load(0); return; }
    this._loadProviders(); // for the sign-in command and mode names, both paths
    this._maybeEager();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    document.removeEventListener('visibilitychange', this._onVisible);
    clearInterval(this._poll); this._poll = null;
  }

  updated(ch) {
    if (ch.has('ended') && this.ended) this._maybePoll();
    // a reattach (the frame set our session after a listing): start replaying
    if (ch.has('session') && this.session && !this._lastSeq && !this._events.length && !this.ended) this._load(0);
    if (ch.has('provider')) this._maybeEager();
    if (this._followUp) this._maybeFollowUp();
    const sc = this.renderRoot?.querySelector('.scroll');
    if (sc && this._atBottom !== false) sc.scrollTop = sc.scrollHeight;
  }

  // ---- data ----

  async _loadProviders() {
    try {
      const r = await fetch('/api/xbin/agent/providers');
      if (!r.ok) return;
      const ps = await r.json();
      this._providers = ps;
      if (ps.length && !this._provider) { this._provider = ps[0].id; this._mode = ps[0].defaultMode || ''; }
    } catch { /* offline; the composer still shows */ }
  }

  // Launched from the + menu / chooser with a provider chosen: create the
  // session now (with an empty prompt) so the agent runs session/new and its
  // config options (model, effort, …) land — the model picker then shows
  // before the first prompt, which is the whole point of the eager create.
  _maybeEager() {
    if (this._started || this.session || this.history || this.ended || this._creating || !this.provider) return;
    this._started = true;
    this._provider = this.provider;
    this._mode = this.mode || this._mode || '';
    this._create();
  }

  // A PAST session (the history attribute): the persisted transcript, read-
  // only — no process, no polling. The foot offers Resume where the agent can
  // reopen it, else a fresh start on this tile.
  async _loadHistory() {
    try {
      const r = await fetch(`/api/xbin/agent/history/${encodeURIComponent(this.history)}/events`);
      if (!r.ok) this._error = r.status === 404 ? 'this past session is gone' : `could not load it (${r.status})`;
      else { const { meta, events } = await r.json(); this._historyMeta = meta || null; this._merge(events || [], false); }
    } catch (e) { this._error = String(e.message || e); }
    this.ended = true;
  }

  _doResume() { if (this._historyMeta) this.dispatchEvent(new CustomEvent('bx-resume', { detail: this._historyMeta, bubbles: true })); }
  _doNewHere() { if (this._historyMeta) this.dispatchEvent(new CustomEvent('bx-new-agent', { detail: { provider: this._historyMeta.provider }, bubbles: true })); }

  async _load(since) {
    if (!this.session) return;
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/events?since=${since}`);
      if (!r.ok) { if (r.status === 404) this._end(); return; }
      const { events, next, truncated } = await r.json();
      this._merge(events || [], !!truncated);
      if (typeof next === 'number' && next > this._lastSeq) this._lastSeq = next;
      this._maybePoll();
    } catch { /* transient; the live stream or the next poll recovers */ }
  }

  _refetch() { this._load(this._lastSeq); }

  // merge new events by seq (dedup; ignore anything already applied)
  _merge(events, truncated) {
    if (truncated) this._truncated = true;
    const seen = new Set(this._events.map((e) => e.seq));
    let added = false;
    for (const e of events) {
      if (e.seq <= this._lastSeq && seen.has(e.seq)) continue;
      if (seen.has(e.seq)) continue;
      seen.add(e.seq);
      this._events = [...this._events, e];
      added = true;
      if (e.seq > this._lastSeq) this._lastSeq = e.seq;
    }
    if (added) { this._events = [...this._events].sort((a, b) => a.seq - b.seq); this.requestUpdate(); this._maybePoll(); }
  }

  // a live event for our session; a skipped seq means the hub dropped one,
  // so re-fetch from the last we have rather than trust the gap
  _live(e) {
    if (e.type !== 'session' || !this.session || e.topic !== 'session.' + this.session) return;
    const ev = e.data;
    if (!ev || typeof ev.seq !== 'number') return;
    if (ev.seq <= this._lastSeq) return;
    if (ev.seq === this._lastSeq + 1) this._merge([ev], false);
    else this._refetch(); // a gap: catch up from the log
  }

  _maybePoll() {
    const busy = !this.ended && (this._status() === 'running' || this._status() === 'waiting_permission' || this._status() === 'cancelling');
    if (busy && !this._poll) this._poll = setInterval(() => this._refetch(), 2000);
    else if (!busy && this._poll) { clearInterval(this._poll); this._poll = null; }
  }

  _end() {
    // the session is gone server-side (exited, removed): the frame keeps the
    // tab as ended so the transcript — and the reason — stay readable
    this.ended = true;
    this._maybePoll();
    this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true }));
  }

  // ---- actions ----

  async _submit() {
    const text = this._draft.trim();
    if (this._creating) return;
    if (!this.session) {
      if (!(await this._create())) return;
      if (!text) return; // started; the settings pickers show now, the first prompt can wait
    }
    if (!text) return;
    if (await this._prompt(text)) this._draft = ''; // else keep the draft to retry
  }

  async _prompt(text) {
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/prompt`,
        { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text }) });
      if (!r.ok) { this._error = (await r.json().catch(() => ({}))).error || `prompt failed (${r.status})`; this._authErr = this._looksAuth(this._error); return false; }
      this._error = ''; this._authErr = false; this._refetch();
      return true;
    } catch (e) { this._error = String(e.message || e); return false; }
  }

  async _create() {
    this._creating = true; this._error = '';
    try {
      const r = await fetch('/api/xbin/term/sessions', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cwd: this.component, kind: 'agent', provider: this._provider, mode: this._mode, resume: this.resume || undefined }),
      });
      const info = await r.json().catch(() => ({}));
      if (!r.ok) { this._error = info.error || `could not start (${r.status})`; this._authErr = this._looksAuth(this._error); return false; }
      this.session = info.id;
      this.setAttribute('session', info.id);
      this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: info.id, kind: 'agent', provider: info.provider, name: info.name }, bubbles: true }));
      this._lastSeq = 0; this._events = [];
      this._load(0);
      return true;
    } catch (e) { this._error = String(e.message || e); return false; }
    finally { this._creating = false; }
  }

  _cancel() {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/cancel`, { method: 'POST' }).catch(() => { });
  }

  _permit(pid, decision, optionId) {
    const body = optionId ? { optionId } : { decision };
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/permissions/${encodeURIComponent(pid)}`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })
      .then(async (r) => { if (r.ok) { this._error = ''; this._refetch(); } else this._error = (await r.json().catch(() => ({}))).error || `could not answer (${r.status})`; });
  }

  // "keep planning" with feedback: reject the plan, then send the text as the
  // next prompt once the turn the reject ends has settled (Claude ends it as
  // cancelled; the prompt route refuses while a turn runs)
  _rejectPlan(pid, optionId) {
    const text = (this._planFeedback[pid] || '').trim();
    delete this._planFeedback[pid];
    if (text) this._followUp = { text, after: this._lastSeq };
    this._permit(pid, null, optionId);
  }

  _maybeFollowUp() {
    const f = this._followUp;
    let st = '';
    for (const e of this._events) if (e.seq > f.after && e.type === 'status' && e.data?.status) st = e.data.status;
    if (st === 'idle') {
      this._followUp = null;
      this._prompt(f.text).then((ok) => { if (!ok && !this._draft) this._draft = f.text; });
    } else if (this.ended || st === 'error' || st === 'exited') {
      this._followUp = null;
      if (!this._draft) this._draft = f.text; // not lost: the user resends it
    }
  }

  // answer a question the agent asked: accept with the form's values,
  // decline (skip)
  _answer(eid, action, content, fields) {
    if (action === 'accept' && fields) {
      const miss = missingRequired(fields, content || {});
      if (miss.length) { this._error = 'answer ' + miss.join(', '); return; }
    }
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/elicitations/${encodeURIComponent(eid)}`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(action === 'accept' ? { action, content: content || {} } : { action }) })
      .then(async (r) => { if (r.ok) { this._error = ''; delete this._askVals[eid]; this._refetch(); } else this._error = (await r.json().catch(() => ({}))).error || `could not answer (${r.status})`; });
  }

  _setOption(id, value) {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/options`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id, value }) })
      .then(async (r) => { if (!r.ok) this._error = (await r.json().catch(() => ({}))).error || `could not set ${id}`; else { this._error = ''; this._refetch(); } });
  }

  // the agent's session settings (model, effort, …): the last status event's
  // options list, as the agent reported it
  // the agent's slash commands: the last status that carried them
  _commands() {
    let cmds = [];
    for (const e of this._events) if (e.type === 'status' && Array.isArray(e.data?.commands)) cmds = e.data.commands;
    return cmds;
  }

  // the slash menu's items for the current draft ([] = closed)
  _slashItems() {
    if (this._slashOff || this.ended) return [];
    const q = slashQuery(this._draft);
    return q == null ? [] : matchCommands(this._commands(), q);
  }

  _pickSlash(c) {
    this._draft = '/' + c.name + ' ';
    this._slashSel = 0;
    const ta = this.renderRoot?.querySelector('.compose textarea');
    if (ta) { ta.value = this._draft; ta.focus(); }
  }

  _options() {
    let opts = [];
    for (const e of this._events) if (e.type === 'status' && Array.isArray(e.data?.options)) opts = e.data.options;
    return opts;
  }

  // the agent's permission modes: from the last status that carried them, else
  // the provider's advertised list (a fallback before the first status lands)
  _modes() {
    let ms = [];
    for (const e of this._events) if (e.type === 'status' && Array.isArray(e.data?.modes)) ms = e.data.modes;
    if (!ms.length) { const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider)); ms = (p && p.modes) || []; }
    return ms;
  }

  _looksAuth(s) { return /sign[\s-]?in|authenticat|not logged in|-32000/i.test(String(s || '')); }

  // the sign-in prompt to show, if any: the backend marks a status with
  // login:{needed,provider,command} when the agent says it is signed out (or a
  // turn hit -32000); a create/prompt error that reads like auth is a fallback
  _login() {
    let last = null;
    for (const e of this._events) if (e.type === 'status') last = e.data;
    if (last && last.login && last.login.needed) return last.login;
    if (this._authErr) {
      const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider));
      if (p && p.login) return { needed: true, provider: p.name, command: p.login };
    }
    return null;
  }

  _provName() {
    const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider));
    return (p && p.name) || this.provider || this._provider || 'the agent';
  }

  // open a shell tab in the same window that runs the provider's login command
  // in the agent's home ($HOME is shared): the printed URL is clickable (the
  // web-links addon), far nicer than copying it out of the agent transcript
  _doSignIn(lg) {
    if (!lg || !lg.command) return;
    this.dispatchEvent(new CustomEvent('bx-open-terminal', { detail: { run: lg.command }, bubbles: true }));
  }

  _key(ev) {
    const items = this._slashItems();
    if (items.length && !ev.isComposing) {
      const n = items.length, sel = Math.min(this._slashSel, n - 1);
      if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') { ev.preventDefault(); this._slashSel = (sel + (ev.key === 'ArrowDown' ? 1 : n - 1)) % n; return; }
      if (ev.key === 'Tab' || (ev.key === 'Enter' && !ev.shiftKey)) { ev.preventDefault(); this._pickSlash(items[sel]); return; }
      if (ev.key === 'Escape') { ev.preventDefault(); this._slashOff = true; return; }
    }
    if (ev.key === 'Enter' && !ev.shiftKey && !ev.isComposing) { ev.preventDefault(); this._submit(); } // isComposing: don't send on an IME confirm
  }

  // ---- view model ----

  // fold the event log into ordered blocks (streaming-tolerant: deltas of one
  // run concatenate, tool updates land on their call, a plan replaces).
  _blocks() {
    const blocks = [];
    const tools = new Map();
    const byId = new Map(); // a call's latest record: files.changed may land after its turn ended
    const perms = new Map();
    const asks = new Map();
    let plan = null, cur = null, turn = 0;
    for (const e of this._events) {
      const d = e.data || {};
      // a thought ends when anything else arrives: that is its duration
      if (cur && cur.kind === 'thought' && e.type !== 'thought.delta' && e.type !== 'status' && e.type !== 'files.changed') { cur.t1 = e.ts || cur.t1; cur.done = true; }
      switch (e.type) {
        case 'message.delta': case 'thought.delta': {
          // a subagent's text goes into its call's card (Part of D77: nesting)
          const box = d.parent ? byId.get(d.parent) : null;
          const into = box && box.children ? box : null;
          const list = into ? into.children : blocks;
          let c = into ? into.cur : cur;
          if (c && c.kind === 'thought' && e.type !== 'thought.delta') { c.t1 = e.ts || c.t1; c.done = true; }
          if (e.type === 'thought.delta') {
            if (c && c.kind === 'thought') { c.text += d.text || ''; c.t1 = e.ts || c.t1; }
            else { c = { kind: 'thought', text: d.text || '', t0: e.ts || 0, t1: e.ts || 0, done: false }; list.push(c); }
          } else {
            const role = d.role || 'agent';
            if (c && c.kind === 'msg' && c.role === role && c.mid === (d.messageId || '')) c.text += d.text || '';
            else { c = { kind: 'msg', role, mid: d.messageId || '', text: d.text || '' }; list.push(c); }
          }
          if (into) into.cur = c; else cur = c;
          break;
        }
        case 'tool.call': case 'tool.update': {
          const tkey = turn + '/' + d.id; // scope ids to the turn: an agent may reuse them
          let t = tools.get(tkey);
          const box = d.parent ? byId.get(d.parent) : null;
          const into = box && box.children && box !== t ? box : null;
          if (into) { if (into.cur && into.cur.kind === 'thought') into.cur.done = true; into.cur = null; } else cur = null;
          if (!t) { t = newTool(d.id); t.t0 = e.ts || 0; tools.set(tkey, t); (into ? into.children : blocks).push(t); }
          foldTool(t, d);
          byId.set(d.id, t);
          if (t.children && t.status !== 'pending' && t.status !== 'in_progress') {
            for (const ch of t.children) if (ch.kind === 'thought') ch.done = true; // a finished subagent thinks no more
          }
          break;
        }
        case 'files.changed': { // a snapshot diff: of one call, or of a whole turn
          const f = { changes: d.changes || [], patch: d.patch || null };
          if (d.toolCallId) { const t = tools.get(turn + '/' + d.toolCallId) || byId.get(d.toolCallId); if (t) t.files = f; break; }
          const blk = { kind: 'changes', turn: d.turn, ...f };
          const at = blocks.findLastIndex((b) => b.kind === 'turn' && b.turn === d.turn); // before its turn's end marker
          if (at >= 0) blocks.splice(at, 0, blk); else blocks.push(blk);
          break;
        }
        case 'plan':
          cur = null;
          if (!plan) { plan = { kind: 'plan', entries: [] }; blocks.push(plan); }
          plan.entries = d.entries || [];
          break;
        case 'permission.request': {
          cur = null;
          const p = { kind: 'perm', pid: d.pid, tool: d.toolCall || {}, options: d.options || [], rule: d.rule || null, meta: d.meta || null, by: null, optionId: null };
          perms.set(d.pid, p); blocks.push(p);
          break;
        }
        case 'elicitation.request': {
          cur = null;
          const q = { kind: 'ask', eid: d.eid, toolCallId: d.toolCallId || '', message: d.message || '', schema: d.schema || null, action: null, by: null, content: null };
          asks.set(d.eid, q); blocks.push(q);
          break;
        }
        case 'elicitation.resolved': {
          const q = asks.get(d.eid);
          if (q) { q.action = d.action; q.by = d.by; q.content = d.content || null; }
          break;
        }
        case 'permission.resolved': {
          const p = perms.get(d.pid);
          if (p) { p.by = d.by; p.optionId = d.optionId; }
          break;
        }
        case 'turn.end':
          if (cur && cur.kind === 'thought') cur.done = true;
          cur = null;
          plan = null; // a new turn starts a fresh plan
          turn = (d.turn || turn) + 0.5; // tool ids in the next turn don't collide with this one's
          blocks.push({ kind: 'turn', turn: d.turn, stopReason: d.stopReason, usage: d.usage, error: d.error });
          break;
        case 'gap':
          cur = null;
          blocks.push({ kind: 'gap' });
          break;
      }
    }
    return blocks;
  }

  _status() {
    let s = this.session ? 'starting' : 'new';
    for (const e of this._events) if (e.type === 'status' && e.data?.status) s = e.data.status;
    return s;
  }

  _statusDetail() {
    let detail = '';
    for (const e of this._events) if (e.type === 'status') detail = e.data?.detail || detail;
    return detail;
  }

  _curMode() {
    let m = this._mode;
    for (const e of this._events) if (e.type === 'status' && e.data?.currentMode) m = e.data.currentMode;
    return m;
  }

  // ---- render ----

  render() {
    const status = this.ended ? 'exited' : this._status();
    const busy = !this.ended && (status === 'running' || status === 'waiting_permission' || status === 'cancelling');
    const lg = this._login();
    const blocks = this._blocks();
    return html`
      <div class="scroll" @scroll=${this._onScroll}>
        ${this._truncated ? html`<div class="gap">… earlier events dropped (log limit)</div>` : nothing}
        ${!this.session && !this.provider && !this.history && !this._events.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${!this.session && this.provider && !this.history && !lg ? html`<div class="hint">Starting ${this._provName()}…</div>` : nothing}
        ${repeat(blocks, (b, i) => b.pid || b.eid || b.id || i, (b) => this._block(b))}
        ${this._activity(status, blocks)}
      </div>
      <div class="foot">
        ${this.ended ? html`<div class="status ended"><span class="dot exited"></span>
          <span>${this.history ? 'a past session — read-only' : 'this session has ended — the transcript stays until you close the tab'}</span>
          ${this.history && this._historyMeta?.loadable ? html`<button @click=${this._doResume} title="reopen this conversation: the agent replays these turns, then continues">Resume</button>` : nothing}
          ${this.history && this._historyMeta && !this._historyMeta.loadable ? html`<button @click=${this._doNewHere} title="this agent cannot reopen a session — start a fresh one on this tile">Start a new session here</button>` : nothing}
        </div>` : nothing}
        <div class="status">
          <span class="dot ${status}"></span>
          <span>${this.history ? 'past session' : this.session ? (status === 'waiting_permission' ? 'waiting for you' : status.replace('_', ' ')) : 'not started'}</span>
          ${this._curMode() && !this._options().length ? html`<span>· ${this._curMode()}</span>` : nothing}
          ${this._usage()}
          ${this._followUp ? html`<span title=${this._followUp.text}>· your feedback goes in when the turn ends</span>` : nothing}
          ${this._error || this._statusDetail() ? html`<span class="err">${this._error || this._statusDetail()}</span>` : nothing}
        </div>
        ${lg ? html`<div class="signin">
          <span class="msg">Not signed in to ${lg.provider}.</span>
          <button @click=${() => this._doSignIn(lg)} title="open a terminal that runs the sign-in command in this agent's home">Sign in to ${lg.provider}</button>
        </div>` : nothing}
        ${!this.session ? (this.provider ? nothing : this._chooser()) : this._settings()}
        ${this._slashMenu()}
        <div class="compose">
          <textarea rows="1" ?disabled=${this.ended} placeholder=${this.ended ? 'the session has ended' : busy ? 'A turn is running…' : 'Message the agent (Enter to send, Shift+Enter for a newline)'}
            .value=${this._draft} @input=${(e) => { this._draft = e.target.value; this._slashOff = false; this._slashSel = 0; this._autosize(e.target); }}
            @keydown=${this._key}></textarea>
          ${busy
            ? html`<button class="cancel" @click=${this._cancel} title="interrupt the running turn">Stop</button>`
            : html`<button @click=${this._submit} ?disabled=${this._creating || this.ended} title=${this.session ? 'send (Enter)' : 'start the agent — with a message it sends it too; without one you can pick the model first'}>${this.session ? 'Send' : 'Start'}</button>`}
        </div>
      </div>`;
  }

  // what the running turn is doing right now, under the transcript: the
  // in-flight tool, else "Working…" (an open thought already says it)
  _activity(status, blocks) {
    if (this.ended || status !== 'running') return nothing;
    const last = blocks[blocks.length - 1];
    if (last && last.kind === 'thought' && !last.done) return nothing;
    let tool = null;
    for (let i = blocks.length - 1; i >= 0 && !tool; i--) if (blocks[i].kind === 'tool' && blocks[i].status === 'in_progress') tool = blocks[i];
    return html`<div class="activity"><span class="shimmer">${tool ? `Running ${headline(tool)}…` : 'Working…'}</span></div>`;
  }

  // the slash-command menu over the composer, or the input hint of the
  // command just completed
  _slashMenu() {
    const items = this._slashItems();
    if (items.length) {
      const sel = Math.min(this._slashSel, items.length - 1);
      return html`<div class="slash" role="listbox">${items.map((c, i) => html`<div class="sc ${i === sel ? 'on' : ''}" role="option" aria-selected=${i === sel}
        @mousedown=${(e) => { e.preventDefault(); this._pickSlash(c); }}><b>/${c.name}</b><span class="d">${c.description || ''}</span>${c.hint ? html`<span class="h">${c.hint}</span>` : nothing}</div>`)}</div>`;
    }
    const h = commandHint(this._commands(), this._draft);
    return h ? html`<div class="slash-hint">/${h.name} — ${h.hint}</div>` : nothing;
  }

  _chooser() {
    const ps = this._providers || [];
    const prov = ps.find((p) => p.id === this._provider);
    return html`<div class="chooser">
      <select title="provider" @change=${(e) => { this._provider = e.target.value; const p = ps.find((x) => x.id === e.target.value); this._mode = p?.defaultMode || ''; }}>
        ${ps.length ? ps.map((p) => html`<option value=${p.id} ?selected=${p.id === this._provider}>${p.name}</option>`)
          : html`<option>loading…</option>`}
      </select>
      ${prov?.modes?.length ? html`<select title="mode" @change=${(e) => { this._mode = e.target.value; }}>
        ${prov.modes.map((m) => html`<option value=${m.id} ?selected=${m.id === this._curMode()}>${m.name}${m.explicit ? ' ⚠' : ''}</option>`)}
      </select>` : nothing}
    </div>`;
  }

  // _settings: one select per setting the agent advertised (model, effort,
  // mode, …), live — changing one calls set_config_option for the next turn.
  _settings() {
    const opts = this._options().filter((o) => o.type === 'select' && Array.isArray(o.options) && o.options.length);
    // the agent exposes its permission mode EITHER as availableModes
    // (session/set_mode) OR as a config option (category "mode") — show one
    // picker, never both, or a "mode" agent renders the select twice
    const hasModeOpt = opts.some((o) => o.category === 'mode' || o.id === 'mode' || (o.name || '').toLowerCase() === 'mode');
    const modes = hasModeOpt ? [] : this._modes();
    if (!opts.length && !modes.length) {
      // eager-created and still starting: the config options (model, effort, …)
      // have not landed yet — say so rather than render an empty row
      const st = this._status();
      return this.session && !this.ended && (st === 'starting' || st === 'running')
        ? html`<div class="chooser settings"><span class="hint">starting the agent…</span></div>` : nothing;
    }
    const cur = this._curMode();
    return html`<div class="chooser settings">
      ${modes.length ? html`<label title="permission mode"><span class="lbl">mode</span>
        <select @change=${(e) => this._setOption('mode', e.target.value)}>
          ${modes.map((m) => html`<option value=${m.id} ?selected=${m.id === cur} title=${m.description || ''}>${m.name || m.id}${m.explicit ? ' ⚠' : ''}</option>`)}
        </select></label>` : nothing}
      ${opts.map((o) => html`<label title=${o.description || o.name}><span class="lbl">${o.name}</span>
        <select @change=${(e) => this._setOption(o.id, e.target.value)}>
          ${o.options.map((v) => html`<option value=${v.value} ?selected=${v.value === o.currentValue} title=${v.description || ''}>${v.name || v.value}</option>`)}
        </select></label>`)}
    </div>`;
  }

  _usage() {
    let u = null;
    for (const e of this._events) if (e.type === 'turn.end' && e.data?.usage) u = e.data.usage;
    if (!u) return nothing;
    return html`<span>· ${fmtN(u.used)}/${fmtN(u.size)} tokens</span>`;
  }

  _block(b) {
    switch (b.kind) {
      case 'msg':
        return b.role === 'user'
          ? html`<div class="row user"><div class="who">you</div><div class="bubble">${b.text}</div></div>`
          : html`<div class="row agent"><div class="who">agent</div><div class="bubble" .innerHTML=${md(b.text)}></div></div>`;
      case 'thought': {
        // open while it streams (the last block of a running turn), then
        // folded to its duration — the Zed/Claude Code pattern
        const live = !b.done && !this.ended && this._status() === 'running';
        const secs = Math.max(1, Math.round(((b.t1 || 0) - (b.t0 || 0)) / 1000));
        return html`<div class="row"><details class="thought" ?open=${live}>
          <summary>${live ? html`<span class="shimmer">Thinking…</span>` : `Thought for ${secs}s`}</summary>
          <div class="md" .innerHTML=${md(b.text)}></div></details></div>`;
      }
      case 'tool':
        return toolCard(this, b);
      case 'plan':
        return html`<div class="plan"><div class="h">plan</div><ul>
          ${(b.entries || []).map((en) => html`<li class="${en.status === 'completed' ? 'done' : ''}">${en.status === 'completed' ? '✓' : en.status === 'in_progress' ? '▸' : '○'} ${en.content}</li>`)}
        </ul></div>`;
      case 'perm':
        return permCard(this, b);
      case 'changes':
        return changesCard(b);
      case 'ask':
        return askCard(this, b);
      case 'turn':
        return html`<div class="turn">turn ${b.turn ?? ''} · ${b.stopReason || 'done'}${b.error ? html` — <span class="err">${b.error}</span>` : nothing}</div>`;
      case 'gap':
        return html`<div class="gap">… earlier events dropped (log limit)</div>`;
      default:
        return nothing;
    }
  }

  _onScroll(e) {
    const el = e.target;
    this._atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  _autosize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, window.innerHeight * 0.4) + 'px';
  }

  // Test hook (the harness): the transcript as plain records, and the actions.
  testApi() {
    const a = this;
    return {
      get status() { return a._status(); },
      get sessionId() { return a.session || null; },
      get provider() { return a._provider; },
      get blocks() {
        return a._blocks().map((b) => ({ kind: b.kind, role: b.role, text: b.text, status: b.status, pid: b.pid, by: b.by, optionId: b.optionId, stopReason: b.stopReason,
          ...(b.kind === 'tool' ? { id: b.id, name: b.name, tk: b.tk, headline: headline(b), output: b.output, exitCode: b.exitCode, files: b.files ? b.files.changes.map((c) => c.path) : null,
            children: b.children ? b.children.map((c) => ({ kind: c.kind, text: c.text, id: c.id, done: c.done })) : null } : {}),
          ...(b.kind === 'changes' ? { turn: b.turn, files: b.changes.map((c) => c.path) } : {}),
          ...(b.kind === 'ask' ? { eid: b.eid, action: b.action, fields: formFields(b.schema).map((f) => f.key), content: b.content } : {}),
          ...(b.kind === 'perm' ? { plan: isPlanApproval(b.tool) } : {}),
          ...(b.kind === 'thought' ? { done: b.done, ms: (b.t1 || 0) - (b.t0 || 0) } : {}) }));
      },
      get pending() {
        return a._blocks().filter((b) => b.kind === 'perm' && !b.by).map((b) => ({ pid: b.pid, cmd: rawText(b.tool?.rawInput), options: (b.options || []).map((o) => o.optionId),
          scoped: b.rule ? b.rule.scoped : true, plan: isPlanApproval(b.tool) }));
      },
      get followUp() { return a._followUp ? a._followUp.text : null; },
      get commands() { return a._commands().map((c) => c.name); },
      get questions() { return a._blocks().filter((b) => b.kind === 'ask' && !b.action).map((b) => ({ eid: b.eid, message: b.message, fields: formFields(b.schema).map((f) => ({ key: f.key, kind: f.kind, other: f.other, options: f.options.map((o) => o.value) })) })); },
      answer(eid, action, content) { a._answer(eid, action, content); },
      get slashMenu() { return a._slashItems().map((c) => c.name); },
      get draft() { return a._draft; },
      setProvider(id) { a._provider = id; const p = (a._providers || []).find((x) => x.id === id); a._mode = p?.defaultMode || ''; },
      get options() { return a._options().map((o) => ({ id: o.id, current: o.currentValue, values: (o.options || []).map((v) => v.value) })); },
      get modes() { return a._modes().map((m) => ({ id: m.id, name: m.name })); },
      get login() { return a._login(); },
      get history() { return a._historyMeta || null; }, // the past session shown read-only (history mode)
      resumeHistory() { a._doResume(); },
      signIn() { const lg = a._login(); if (lg) a._doSignIn(lg); },
      setOption(id, value) { a._setOption(id, value); },
      start() { return a._create(); },
      send(text) { a._draft = text; return a._submit(); },
      permit(pid, decision, optionId) { a._permit(pid, decision, optionId); },
      rejectPlan(pid, optionId, feedback) { if (feedback) a._planFeedback[pid] = feedback; a._rejectPlan(pid, optionId); },
      cancel() { a._cancel(); },
    };
  }
}

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

customElements.define('bx-agent', BxAgent);
