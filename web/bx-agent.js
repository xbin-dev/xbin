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
import { diffHTML, diffStats } from '/vendor/bx-code.js';

const KIND_ICON = { read: '📖', edit: '✏️', delete: '🗑️', move: '↪', search: '🔎', execute: '⚙', think: '💭', fetch: '🌐', other: '•' };

export class BxAgent extends LitElement {
  static properties = {
    session: { type: String },
    component: { type: String },
    provider: { type: String }, // set by the launcher: create eagerly so the model picker loads before the first prompt
    mode: { type: String },
    ended: { type: Boolean }, // the session is gone (the frame keeps the tab): no polling, the transcript stays
    _events: { state: true },
    _providers: { state: true },
    _provider: { state: true },
    _mode: { state: true },
    _draft: { state: true },
    _truncated: { state: true },
    _error: { state: true },
    _authErr: { state: true }, // the last create/turn failed auth (show the sign-in banner)
  };

  static styles = css`
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
    .thought > summary { list-style: none; cursor: pointer; font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; letter-spacing: .04em; }
    .thought > summary::-webkit-details-marker { display: none; }
    .thought .md { font-style: italic; font-size: 12px; }
    .thought .md > :first-child { margin-top: 4px; } .thought .md > :last-child { margin-bottom: 0; }
    .tool { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; margin: 0 0 8px; overflow: hidden; }
    .tool > summary { list-style: none; cursor: pointer; padding: 5px 9px; display: flex; align-items: center; gap: 6px;
      font: 11px var(--bx-mono, ui-monospace, monospace); }
    .tool > summary::-webkit-details-marker { display: none; }
    .tool .title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .chip { font: 9.5px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; letter-spacing: .03em;
      padding: 1px 5px; border-radius: 3px; background: var(--bx-panel-2, #2b3038); color: var(--bx-muted, #868f9a); }
    .chip.completed { color: var(--bx-green, #4caf50); }
    .chip.failed, .chip.cancelled { color: var(--bx-red, #ef5350); }
    .chip.in_progress, .chip.pending { color: var(--bx-amber, #f2a71b); }
    .tool .body { padding: 6px 9px; border-top: 1px solid var(--bx-border, #363c45); }
    .tool pre { margin: 0; font: 11px var(--bx-mono, ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; }
    .diff { font: 11px var(--bx-mono, ui-monospace, monospace); }
    .diff .fh { color: var(--bx-muted, #868f9a); display: block; }
    .diff .h { color: var(--bx-accent, #f5a623); display: block; }
    .diff .d { color: var(--bx-green, #4caf50); display: block; background: color-mix(in srgb, var(--bx-green, #4caf50) 12%, transparent); }
    .diff .a { color: var(--bx-red, #ef5350); display: block; background: color-mix(in srgb, var(--bx-red, #ef5350) 12%, transparent); }
    .diff .ctx { display: block; color: var(--bx-text, #d4d9e0); }
    .plan { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 10px; margin: 0 0 8px; }
    .plan .h { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; color: var(--bx-muted, #868f9a); margin-bottom: 4px; }
    .plan li { list-style: none; margin: 1px 0; }
    .plan .done { color: var(--bx-green, #4caf50); text-decoration: line-through; opacity: .7; }
    .perm { border: 1px solid var(--bx-amber, #f2a71b); border-radius: 6px; padding: 8px 10px; margin: 0 0 10px;
      background: color-mix(in srgb, var(--bx-amber, #f2a71b) 8%, var(--bx-panel, #23272e)); }
    .perm .q { margin-bottom: 6px; }
    .perm .desc { color: var(--bx-muted, #868f9a); font-size: 12px; margin-top: 2px; }
    .perm .cmd { background: var(--bx-term-bg, #262c36); border-radius: 5px; padding: 6px 8px; margin: 0 0 6px;
      font: 11.5px var(--bx-mono, ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; max-height: 200px; }
    .perm .body { margin-bottom: 6px; }
    .perm .body pre, .perm .body .diff { margin: 0; font: 11px var(--bx-mono, ui-monospace, monospace); white-space: pre-wrap; overflow-x: auto; max-height: 240px; }
    .perm .rulenote { color: var(--bx-muted, #868f9a); font-size: 11.5px; margin-bottom: 6px; }
    .perm.settled-card { opacity: .8; }
    .perm .btns { display: flex; gap: 6px; flex-wrap: wrap; }
    .perm button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel, #23272e); color: var(--bx-text, #d4d9e0);
      border-radius: 5px; padding: 3px 10px; cursor: pointer; font: 12px var(--bx-sans, system-ui); }
    .perm button.allow { border-color: var(--bx-green, #4caf50); }
    .perm button.deny { border-color: var(--bx-red, #ef5350); }
    .perm .settled { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); }
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
  `;

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
    if (this._started || this.session || this.ended || this._creating || !this.provider) return;
    this._started = true;
    this._provider = this.provider;
    this._mode = this.mode || this._mode || '';
    this._create();
  }

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
    try {
      const r = await fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/prompt`,
        { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text }) });
      if (!r.ok) { this._error = (await r.json().catch(() => ({}))).error || `prompt failed (${r.status})`; this._authErr = this._looksAuth(this._error); return; } // keep the draft to retry
      this._error = ''; this._authErr = false; this._draft = ''; this._refetch();
    } catch (e) { this._error = String(e.message || e); }
  }

  async _create() {
    this._creating = true; this._error = '';
    try {
      const r = await fetch('/api/xbin/term/sessions', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cwd: this.component, kind: 'agent', provider: this._provider, mode: this._mode }),
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

  _setOption(id, value) {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/options`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id, value }) })
      .then(async (r) => { if (!r.ok) this._error = (await r.json().catch(() => ({}))).error || `could not set ${id}`; else { this._error = ''; this._refetch(); } });
  }

  // the agent's session settings (model, effort, …): the last status event's
  // options list, as the agent reported it
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
    if (ev.key === 'Enter' && !ev.shiftKey && !ev.isComposing) { ev.preventDefault(); this._submit(); } // isComposing: don't send on an IME confirm
  }

  // ---- view model ----

  // fold the event log into ordered blocks (streaming-tolerant: deltas of one
  // run concatenate, tool updates land on their call, a plan replaces).
  _blocks() {
    const blocks = [];
    const tools = new Map();
    const perms = new Map();
    let plan = null, cur = null, turn = 0;
    for (const e of this._events) {
      const d = e.data || {};
      switch (e.type) {
        case 'message.delta': {
          const role = d.role || 'agent';
          if (cur && cur.kind === 'msg' && cur.role === role && cur.mid === (d.messageId || '')) cur.text += d.text || '';
          else { cur = { kind: 'msg', role, mid: d.messageId || '', text: d.text || '' }; blocks.push(cur); }
          break;
        }
        case 'thought.delta':
          if (cur && cur.kind === 'thought') cur.text += d.text || '';
          else { cur = { kind: 'thought', text: d.text || '' }; blocks.push(cur); }
          break;
        case 'tool.call': case 'tool.update': {
          cur = null;
          const tkey = turn + '/' + d.id; // scope ids to the turn: an agent may reuse them
          let t = tools.get(tkey);
          if (!t) { t = { kind: 'tool', id: d.id, title: '', tk: 'other', status: 'pending', content: null }; tools.set(tkey, t); blocks.push(t); }
          if (d.title != null) t.title = d.title;
          if (d.kind != null) t.tk = d.kind;
          if (d.status != null) t.status = d.status;
          if (d.content != null) t.content = d.content;
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
        case 'permission.resolved': {
          const p = perms.get(d.pid);
          if (p) { p.by = d.by; p.optionId = d.optionId; }
          break;
        }
        case 'turn.end':
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
    return html`
      <div class="scroll" @scroll=${this._onScroll}>
        ${this._truncated ? html`<div class="gap">… earlier events dropped (log limit)</div>` : nothing}
        ${!this.session && !this.provider && !this._events.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${!this.session && this.provider && !lg ? html`<div class="hint">Starting ${this._provName()}…</div>` : nothing}
        ${repeat(this._blocks(), (b, i) => b.pid || b.id || i, (b) => this._block(b))}
      </div>
      <div class="foot">
        ${this.ended ? html`<div class="status ended"><span class="dot exited"></span><span>this session has ended — the transcript stays until you close the tab</span></div>` : nothing}
        <div class="status">
          <span class="dot ${status}"></span>
          <span>${this.session ? status.replace('_', ' ') : 'not started'}</span>
          ${this._curMode() && !this._options().length ? html`<span>· ${this._curMode()}</span>` : nothing}
          ${this._usage()}
          ${this._error || this._statusDetail() ? html`<span class="err">${this._error || this._statusDetail()}</span>` : nothing}
        </div>
        ${lg ? html`<div class="signin">
          <span class="msg">Not signed in to ${lg.provider}.</span>
          <button @click=${() => this._doSignIn(lg)} title="open a terminal that runs the sign-in command in this agent's home">Sign in to ${lg.provider}</button>
        </div>` : nothing}
        ${!this.session ? (this.provider ? nothing : this._chooser()) : this._settings()}
        <div class="compose">
          <textarea rows="1" ?disabled=${this.ended} placeholder=${this.ended ? 'the session has ended' : busy ? 'A turn is running…' : 'Message the agent (Enter to send, Shift+Enter for a newline)'}
            .value=${this._draft} @input=${(e) => { this._draft = e.target.value; this._autosize(e.target); }}
            @keydown=${this._key}></textarea>
          ${busy
            ? html`<button class="cancel" @click=${this._cancel} title="interrupt the running turn">Stop</button>`
            : html`<button @click=${this._submit} ?disabled=${this._creating || this.ended} title=${this.session ? 'send (Enter)' : 'start the agent — with a message it sends it too; without one you can pick the model first'}>${this.session ? 'Send' : 'Start'}</button>`}
        </div>
      </div>`;
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
    const modes = this._modes();
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
      case 'thought':
        return html`<div class="row"><details class="thought"><summary>thinking</summary><div class="md" .innerHTML=${md(b.text)}></div></details></div>`;
      case 'tool':
        return html`<details class="tool" ?open=${b.status === 'failed'}>
          <summary><span>${KIND_ICON[b.tk] || KIND_ICON.other}</span>
            <span class="title">${b.title || b.id}</span>
            <span class="chip ${b.status}">${b.status}${this._diffStat(b.content)}</span></summary>
          ${this._toolBody(b)}
        </details>`;
      case 'plan':
        return html`<div class="plan"><div class="h">plan</div><ul>
          ${(b.entries || []).map((en) => html`<li class="${en.status === 'completed' ? 'done' : ''}">${en.status === 'completed' ? '✓' : en.status === 'in_progress' ? '▸' : '○'} ${en.content}</li>`)}
        </ul></div>`;
      case 'perm':
        return this._permCard(b);
      case 'turn':
        return html`<div class="turn">turn ${b.turn ?? ''} · ${b.stopReason || 'done'}${b.error ? html` — <span class="err">${b.error}</span>` : nothing}</div>`;
      case 'gap':
        return html`<div class="gap">… earlier events dropped (log limit)</div>`;
      default:
        return nothing;
    }
  }

  _toolBody(b) {
    const items = Array.isArray(b.content) ? b.content : null;
    if (!items || !items.length) return nothing;
    return html`<div class="body">${items.map((it) => this._contentItem(it))}</div>`;
  }

  // One tool-content item: a real diff (reusing bx-code's renderer), a
  // terminal reference, or text.
  _contentItem(it) {
    if (it.type === 'diff') {
      return html`<pre class="diff" .innerHTML=${diffHTML(unifiedDiff(it.path, it.oldText, it.newText))}></pre>`;
    }
    if (it.type === 'terminal') return html`<pre class="term">[terminal ${it.terminalId || ''}]</pre>`;
    const text = it.content?.text ?? it.text ?? '';
    return html`<pre>${String(text)}</pre>`;
  }

  // +added/-removed across a tool's diff content (real counts, via a proper
  // diff — not the whole-file line totals the old code reported).
  _diffStat(content) {
    const items = Array.isArray(content) ? content : [];
    let add = 0, del = 0;
    for (const it of items) if (it.type === 'diff') { const st = diffStats(unifiedDiff(it.path, it.oldText, it.newText)); add += st.add; del += st.del; }
    return add || del ? html` +${add}/-${del}` : nothing;
  }

  _permCard(b) {
    const tc = b.tool || {};
    const title = (b.meta && b.meta.title) || tc.title || tc.kind || tc.id || 'a tool call';
    const cmd = rawText(tc.rawInput);
    if (b.by) {
      const who = b.by === 'auto' ? 'a session rule' : b.by === 'cancel' ? 'cancel' : b.by.replace('user:', '');
      const opt = (b.options || []).find((o) => o.optionId === b.optionId);
      const denied = opt ? /reject/.test(opt.kind || '') : false;
      const verb = b.by === 'cancel' ? 'cancelled' : denied ? 'denied' : 'allowed';
      return html`<div class="perm settled-card"><div class="q">${title}</div><div class="settled">${verb} by ${who}</div></div>`;
    }
    // the agent's real options (name + a kind badge), reject first when the
    // adapter asked us to default to no; the exact command/diff is shown so
    // the user judges what they approve
    const opts = (b.options || []).slice();
    const defNo = !!(b.meta && b.meta.defaultToNo);
    if (defNo) opts.sort((a, c) => (/reject/.test(a.kind || '') ? -1 : 0) - (/reject/.test(c.kind || '') ? -1 : 0));
    const scoped = b.rule ? b.rule.scoped : true; // hide "for the session" when it can't be scoped
    return html`<div class="perm">
      <div class="q"><b>Permission</b> — ${title}${b.meta && b.meta.description ? html`<div class="desc">${b.meta.description}</div>` : nothing}</div>
      ${cmd ? html`<pre class="cmd">${cmd}</pre>` : nothing}
      ${this._toolBody(b.tool)}
      ${scoped && b.rule && (b.rule.kind || b.rule.title) ? html`<div class="rulenote">“Allow for the session” auto-approves later ${b.rule.kind || ''} calls${b.rule.title ? html` titled “${b.rule.title}”` : ''}.</div>` : nothing}
      <div class="btns">
        ${opts.length
          ? opts.filter((o) => scoped || o.kind !== 'allow_always').map((o) => html`<button class="${/reject/.test(o.kind || '') ? 'deny' : 'allow'}" @click=${() => this._permit(b.pid, null, o.optionId)}>${o.name || o.optionId}</button>`)
          : html`<button class="allow" @click=${() => this._permit(b.pid, 'allow_once')}>Allow once</button>
             ${scoped ? html`<button class="allow" @click=${() => this._permit(b.pid, 'allow_always')}>Allow for the session</button>` : nothing}
             <button class="deny" @click=${() => this._permit(b.pid, 'reject_once')}>Deny</button>`}
      </div>
    </div>`;
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
      get blocks() { return a._blocks().map((b) => ({ kind: b.kind, role: b.role, text: b.text, status: b.status, pid: b.pid, by: b.by, stopReason: b.stopReason })); },
      get pending() { return a._blocks().filter((b) => b.kind === 'perm' && !b.by).map((b) => ({ pid: b.pid, cmd: rawText(b.tool?.rawInput), options: (b.options || []).map((o) => o.optionId), scoped: b.rule ? b.rule.scoped : true })); },
      setProvider(id) { a._provider = id; const p = (a._providers || []).find((x) => x.id === id); a._mode = p?.defaultMode || ''; },
      get options() { return a._options().map((o) => ({ id: o.id, current: o.currentValue, values: (o.options || []).map((v) => v.value) })); },
      get modes() { return a._modes().map((m) => ({ id: m.id, name: m.name })); },
      get login() { return a._login(); },
      signIn() { const lg = a._login(); if (lg) a._doSignIn(lg); },
      setOption(id, value) { a._setOption(id, value); },
      start() { return a._create(); },
      send(text) { a._draft = text; return a._submit(); },
      permit(pid, decision) { a._permit(pid, decision); },
      cancel() { a._cancel(); },
    };
  }
}

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

// rawText renders a tool's rawInput for the permission card: a shell
// command verbatim (the common {command}/{cmd} shapes), else compact JSON.
function rawText(raw) {
  if (raw == null) return '';
  if (typeof raw === 'string') return raw;
  if (typeof raw === 'object') {
    const cmd = raw.command ?? raw.cmd ?? raw.script;
    if (typeof cmd === 'string') return cmd + (Array.isArray(raw.args) ? ' ' + raw.args.join(' ') : '');
    try { return JSON.stringify(raw, null, 1); } catch { return ''; }
  }
  return String(raw);
}

// unifiedDiff(path, old, new): a git-style unified diff from an ACP diff
// block's whole old/new text, via a line LCS — so bx-code's diffHTML gives
// the same syntax-highlighted +/- view the code panel uses. Cheap: ACP diff
// blocks are a single file's before/after, not a whole tree.
function unifiedDiff(path, oldText, newText) {
  const a = String(oldText ?? '').split('\n'), b = String(newText ?? '').split('\n');
  if ((oldText ?? '') === (newText ?? '')) return `diff --git a/${path || 'file'} b/${path || 'file'}\n`;
  const n = a.length, m = b.length;
  // LCS table (bounded: skip the O(nm) table for very large inputs, fall back to replace-all)
  let body;
  if (n * m > 400000) {
    body = a.map((l) => '-' + l).concat(b.map((l) => '+' + l));
  } else {
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    body = []; let i = 0, j = 0;
    while (i < n && j < m) {
      if (a[i] === b[j]) { body.push(' ' + a[i]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) { body.push('-' + a[i]); i++; }
      else { body.push('+' + b[j]); j++; }
    }
    while (i < n) body.push('-' + a[i++]);
    while (j < m) body.push('+' + b[j++]);
  }
  const p = path || 'file';
  return `diff --git a/${p} b/${p}\n--- a/${p}\n+++ b/${p}\n@@ -1,${n} +1,${m} @@\n` + body.join('\n') + '\n';
}

customElements.define('bx-agent', BxAgent);
