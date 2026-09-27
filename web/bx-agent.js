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
 * A long transcript renders windowed (D124): the event log folds
 * incrementally (agent-fold.js — stable block keys, per-block versions and
 * memoized markdown/diffs), renders at most once per frame, and shows only its
 * recent blocks; scrolling up loads earlier pages. Nothing jumps: the scroller
 * opts out of native scroll anchoring and every update keeps the first visible
 * block where it was (or follows the bottom), measured before lit commits and
 * corrected in updated(), before the frame paints.
 *
 * Events: 'bx-session' (detail {id, kind:'agent', provider, name, net,
 * scopes, label, gpu, api}) when it creates the
 * session, so the frame records the id; 'bx-exit' when the session ends.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { repeat, guard } from 'lit';
import { onEvent } from '/vendor/events-socket.js';
import { md } from '/vendor/bx-md.js';
import { headline, isPlanApproval, rawText, formFields, missingRequired } from '/vendor/agent-tools.js';
import { Fold, cached } from '/vendor/agent-fold.js';
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
    restarting: { type: Boolean }, // the frame is restarting this tab's agent with other sandbox pickers (frame-launcher.js restartAgent)
    vm: { type: Boolean }, // set by the launcher: create the session in a VM sandbox (the tile's choice, frame-launcher.js wantVM)
    _historyMeta: { state: true },
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

  static styles = [scrollCss, cardsCss, css`
    /* the terminal pane's surface, not a tile's: a floating agent window
       must stand apart from the tiles under it, as a shell's does */
    :host { display: flex; flex-direction: column; height: 100%; min-height: 0;
      background: var(--bx-term-bg, #262c36); color: var(--bx-text, #d4d9e0);
      font: 13px/1.5 var(--bx-sans, system-ui, sans-serif); }
    /* overflow-anchor: none — the element anchors itself, the same on every
       engine (Safari has no native scroll anchoring) */
    .scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 10px 12px; overflow-anchor: none; }
    .earlier { display: flex; gap: 8px; justify-content: center; align-items: baseline; }
    .earlier button { border: 0; background: none; padding: 0; cursor: pointer; color: var(--bx-accent, #f5a623); font: inherit; }
    .row { margin: 0 0 10px; }
    .who { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase;
      letter-spacing: .04em; color: var(--bx-muted, #868f9a); margin-bottom: 2px; }
    .user .bubble { background: var(--bx-panel-2, #2b3038); border-radius: 8px; padding: 6px 10px; white-space: pre-wrap; }
    .user .files { display: flex; flex-wrap: wrap; gap: 4px; white-space: normal; }
    .user .files.below { margin-top: 4px; }
    .user .file { border: 1px solid var(--bx-border, #363c45); border-radius: 4px; padding: 0 6px; font-size: 12px; opacity: .85; }
    .agent .bubble > :first-child { margin-top: 0; }
    .agent .bubble > :last-child { margin-bottom: 0; }
    .bubble :is(pre, code) { font-family: var(--bx-mono, ui-monospace, monospace); }
    .bubble pre { background: var(--bx-bg, #1b1e24); padding: 8px 10px; border-radius: 6px; overflow-x: auto; }
    .bubble :not(pre) > code { background: var(--bx-bg, #1b1e24); padding: .1em .3em; border-radius: 3px; }
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
    .compose textarea { flex: 1; resize: none; background: var(--bx-bg, #1b1e24); color: var(--bx-text, #d4d9e0);
      border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 8px;
      font: 13px var(--bx-sans, system-ui); max-height: 40vh; }
    .compose button { border: 1px solid var(--bx-border, #363c45); background: var(--bx-panel-2, #2b3038); color: var(--bx-text, #d4d9e0);
      border-radius: 6px; padding: 6px 12px; cursor: pointer; font: 12px var(--bx-sans, system-ui); }
    .compose button.cancel { border-color: var(--bx-red, #ef5350); }
    .chooser { display: flex; gap: 6px; margin-bottom: 6px; flex-wrap: wrap; }
    .chooser label { display: inline-flex; align-items: center; gap: 4px; font: 10.5px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); }
    .chooser select { background: var(--bx-bg, #1b1e24); color: var(--bx-text, #d4d9e0);
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
    this._events = []; // the log, in seq order (mutated in place; the fold is the view)
    this._seen = new Set(); // seqs applied
    this._fold = new Fold();
    this._from = null; // the first rendered block (the window's top); null = the last PAGE
    this._start = 0; // its index, as of the last render
    this._opened = new Set(); // '<key>:<slot>' of lazy <details> bodies the user opened
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
    this._ro?.disconnect(); this._ro = null;
  }

  // at most one render per frame: a replay's burst of events (or a keystroke
  // plus an event) folds as it arrives and paints once, and updated() — the
  // scroll correction — runs inside the frame, before it paints
  async scheduleUpdate() {
    await new Promise((r) => requestAnimationFrame(r));
    super.scheduleUpdate();
  }

  willUpdate() {
    // the window: from the first rendered block to the end. Following the
    // bottom with a long rendered tail drops what is far above the view (the
    // removal is above the pinned view, so nothing visible moves); what stays
    // above (2.5 views) clears _maybeOlder's 1.5 so the two never ping-pong.
    const blocks = this._fold.blocks;
    let i = this._from ? blocks.indexOf(this._from) : -1;
    if (i < 0) i = Math.max(0, blocks.length - PAGE);
    const pinned = this._atBottom !== false && !this._keepView;
    const sc = pinned && blocks.length - i > TRIM ? this._sc() : null;
    if (sc && sc.clientHeight && sc.scrollTop > sc.clientHeight * 6) {
      const row = this._rowAt(sc, sc.scrollTop - sc.clientHeight * 2.5);
      const k = row ? blocks.findIndex((b) => b.key === Number(row.dataset.k)) : -1;
      if (k > i) i = k;
    }
    this._start = i;
    this._from = blocks[i] || null;
    // not following the bottom: the first visible block keeps its place
    this._anchor = pinned ? null : this._firstVisible();
  }

  updated(ch) {
    if (ch.has('ended') && this.ended) this._maybePoll();
    // a reattach (the frame set our session after a listing): start replaying
    if (ch.has('session') && this.session && !this._lastSeq && !this._events.length && !this.ended) this._load(0);
    if (ch.has('provider')) this._maybeEager();
    if (this._followUp) this._maybeFollowUp();
    const sc = this._sc();
    if (!sc) return;
    if (!this._ro) { this._ro = new ResizeObserver(() => this._resized()); this._ro.observe(sc); }
    const a = this._anchor;
    this._anchor = null;
    if (this._atBottom !== false && !this._keepView) sc.scrollTop = sc.scrollHeight;
    else if (a && a.el.isConnected) {
      const d = a.el.getBoundingClientRect().top - a.top;
      if (Math.abs(d) >= 0.5) sc.scrollTop += d;
    }
    if (this._keepView) { this._keepView = false; this._atBottom = sc.scrollHeight - sc.scrollTop - sc.clientHeight < 40; }
    this._maybeOlder(sc);
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

  // merge new events by seq (dedup; ignore anything already applied): an
  // event after the last folds in place; one that lands before it (rare — a
  // re-fetch racing the stream) is inserted in order and the log refolds
  _merge(events, truncated) {
    if (truncated) this._truncated = true;
    const evs = this._events;
    let added = false, back = false;
    for (const e of events) {
      if (this._seen.has(e.seq)) continue;
      this._seen.add(e.seq);
      added = true;
      if (e.seq > this._lastSeq) this._lastSeq = e.seq;
      if (!evs.length || e.seq > evs[evs.length - 1].seq) { evs.push(e); if (!back) this._fold.push(e); continue; }
      let i = evs.length;
      while (i > 0 && evs[i - 1].seq > e.seq) i--;
      evs.splice(i, 0, e);
      back = true;
    }
    if (back) { this._fold.reset(evs); this._from = null; }
    if (added) { this.requestUpdate(); this._maybePoll(); }
  }

  _reset() { this._lastSeq = 0; this._events = []; this._seen.clear(); this._fold.reset(); this._from = null; this._opened.clear(); }

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
    if (this._creating || this.restarting) return; // restarting: the frame brings the new session
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
        body: JSON.stringify({ cwd: this.component, kind: 'agent', provider: this._provider, mode: this._mode, resume: this.resume || undefined, vm: this.vm || undefined }),
      });
      const info = await r.json().catch(() => ({}));
      if (!r.ok) { this._error = info.error || `could not start (${r.status})`; this._authErr = this._looksAuth(this._error); return false; }
      this.session = info.id;
      this.setAttribute('session', info.id);
      this.dispatchEvent(new CustomEvent('bx-session', { detail: { id: info.id, kind: 'agent', provider: info.provider, name: info.name,
        net: info.net, scopes: info.scopes, label: info.label, gpu: info.gpu, api: info.api }, bubbles: true }));
      this._reset();
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
  _commands() { return this._fold.st.commands || []; }

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

  _options() { return this._fold.st.options || []; }

  // the agent's permission modes: from the last status that carried them, else
  // the provider's advertised list (a fallback before the first status lands)
  _modes() {
    let ms = this._fold.st.modes || [];
    if (!ms.length) { const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider)); ms = (p && p.modes) || []; }
    return ms;
  }

  _looksAuth(s) { return /sign[\s-]?in|authenticat|not logged in|-32000/i.test(String(s || '')); }

  // the sign-in prompt to show, if any: the backend marks a status with
  // login:{needed,provider,command} when the agent says it is signed out (or a
  // turn hit -32000); a create/prompt error that reads like auth is a fallback
  _login() {
    const last = this._fold.st.last;
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

  // the transcript's blocks: the log folded incrementally (agent-fold.js)
  _blocks() { return this._fold.blocks; }

  _status() { return this._fold.st.status || (this.session ? 'starting' : 'new'); }
  _statusDetail() { return this._fold.st.detail; }
  _curMode() { return this._fold.st.currentMode || this._mode; }

  // ---- render ----

  render() {
    const status = this.ended ? 'exited' : this._status();
    const busy = !this.ended && (status === 'running' || status === 'waiting_permission' || status === 'cancelling');
    const lg = this._login();
    const blocks = this._blocks();
    const from = this._start; // willUpdate placed the window
    const win = from ? blocks.slice(from) : blocks;
    const running = !this.ended && status === 'running';
    return html`
      <div class="scroll" @scroll=${this._onScroll} @touchstart=${this._touchL}>
        ${this._truncated && !from ? html`<div class="gap">… earlier events dropped (log limit)</div>` : nothing}
        ${from ? html`<div class="gap earlier">… ${from} earlier ${from === 1 ? 'entry' : 'entries'}
          <button @click=${() => this._older()}>load earlier</button><button @click=${() => this._older(Infinity)}>load all</button></div>` : nothing}
        ${this.restarting ? html`<div class="hint">Restarting the agent in a new sandbox — the conversation resumes where the agent can reopen it…</div>` : nothing}
        ${!this.session && !this.provider && !this.history && !this.restarting && !this._events.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${!this.session && this.provider && !this.history && !lg ? html`<div class="hint">Starting ${this._provName()}…</div>` : nothing}
        ${repeat(win, (b) => b.key, (b) => this._row(b, running))}
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
        ${!this.session ? (this.provider || this.restarting ? nothing : this._chooser()) : this._settings()}
        ${this._slashMenu()}
        <div class="compose">
          <textarea rows="1" ?disabled=${this.ended} placeholder=${this.ended ? 'the session has ended' : busy ? 'A turn is running…' : 'Message the agent (Enter to send, Shift+Enter for a newline)'}
            .value=${this._draft} @input=${(e) => { this._draft = e.target.value; this._slashOff = false; this._slashSel = 0; this._autosize(e.target); }}
            @keydown=${this._key}></textarea>
          ${busy
            ? html`<button class="cancel" @click=${this._cancel} title="interrupt the running turn">Stop</button>`
            : html`<button @click=${this._submit} ?disabled=${this._creating || this.ended || this.restarting} title=${this.session ? 'send (Enter)' : 'start the agent — with a message it sends it too; without one you can pick the model first'}>${this.session ? 'Send' : 'Start'}</button>`}
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
    for (let i = blocks.length - 1; i >= 0 && !tool && blocks[i].kind !== 'turn'; i--) if (blocks[i].kind === 'tool' && blocks[i].status === 'in_progress') tool = blocks[i];
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
    const u = this._fold.st.usage;
    if (!u) return nothing;
    return html`<span>· ${fmtN(u.used)}/${fmtN(u.size)} tokens</span>`;
  }

  // one transcript row: the block, re-rendered only when it (b.v), its lazy
  // bodies (b.ui) or what it reads beside itself changed; an open question or
  // permission (live form state) always renders
  _row(b, running) {
    const live = (b.kind === 'perm' && !b.by) || (b.kind === 'ask' && !b.action);
    return html`<div class="blk" data-k=${b.key}>${live ? this._block(b) : guard([b.v, b.ui, this.ended, (b.kind === 'thought' || !!b.children) && running], () => this._block(b))}</div>`;
  }

  // lazy <details> bodies: rendered once opened (or open by default)
  _isOpen(b, slot) { return this._opened.has(b.key + ':' + slot); }
  _toggled(e, b, slot, dflt) {
    const k = b.key + ':' + slot;
    if (!e.target.open || e.target.open === !!dflt || this._opened.has(k)) return; // closing, or lit applying the default
    this._opened.add(k);
    for (let x = b; x; x = x.up) x.ui = (x.ui || 0) + 1;
    this._keepView = true; // the reader opened it: keep their view, don't chase the bottom
    this.requestUpdate();
  }

  _block(b) {
    switch (b.kind) {
      case 'msg':
        return b.role === 'user'
          ? html`<div class="row user"><div class="who">you</div><div class="bubble">${b.text}${b.files?.length ? html`<div class="files ${b.text ? 'below' : ''}">${b.files.map((f) =>
              html`<span class="file" title=${`${f.mime || ''} · ${fmtN(f.size || 0)} bytes`}>${f.name}</span>`)}</div>` : nothing}</div></div>`
          : html`<div class="row agent"><div class="who">agent</div><div class="bubble" .innerHTML=${cached(b, 'md', () => md(b.text))}></div></div>`;
      case 'thought': {
        // open while it streams (the last block of a running turn), then
        // folded to its duration — the Zed/Claude Code pattern
        const live = !b.done && !this.ended && this._status() === 'running';
        const secs = Math.max(1, Math.round(((b.t1 || 0) - (b.t0 || 0)) / 1000));
        return html`<div class="row"><details class="thought" ?open=${live} @toggle=${(e) => this._toggled(e, b, 'body', live)}>
          <summary>${live ? html`<span class="shimmer">Thinking…</span>` : `Thought for ${secs}s`}</summary>
          ${live || this._isOpen(b, 'body') ? html`<div class="md" .innerHTML=${cached(b, 'md', () => md(b.text))}></div>` : nothing}</details></div>`;
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
        return changesCard(this, b);
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

  _sc() { return this.renderRoot?.querySelector('.scroll'); }

  _onScroll(e) {
    const el = e.target;
    this._atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (this._touch) { // a touch scroll (and its momentum) is settled after 150 ms without a scroll event
      clearTimeout(this._idle);
      this._idle = setTimeout(() => { this._idle = 0; this._maybeOlder(el); }, 150);
    }
    if (!this._scrollRaf) this._scrollRaf = requestAnimationFrame(() => { this._scrollRaf = 0; this._maybeOlder(el); });
  }

  // passive: a touch listener must never hold up the scroll it starts
  _touchL = { handleEvent: () => { this._touch = true; }, passive: true };

  // the pane resized (a window drag, the composer growing, the tab shown):
  // keep following the bottom, and fill a taller view
  _resized() {
    const sc = this._sc();
    if (!sc) return;
    if (this._atBottom !== false) sc.scrollTop = sc.scrollHeight;
    this._maybeOlder(sc);
  }

  // load an earlier page when the reader nears the top of what is rendered,
  // or the rendered part does not fill two views. A touch scroll waits until
  // it settles unless the top is close: moving scrollTop under iOS momentum
  // stops it dead.
  _maybeOlder(sc) {
    if (!this._start || !sc.clientHeight) return;
    const short = sc.scrollHeight < sc.clientHeight * 2;
    if (!short && sc.scrollTop > sc.clientHeight * 1.5) return;
    if (this._touch && this._idle && !short && sc.scrollTop > sc.clientHeight * 0.5) return; // _onScroll's settle timer calls back
    this._older();
  }

  _older(n = PAGE) {
    const blocks = this._blocks();
    const i = Math.max(0, this._start - n);
    if (blocks[i] === this._from) return;
    this._from = blocks[i] || null;
    this.requestUpdate();
  }

  // the first rendered row whose bottom is below content offset y (a binary
  // search over the rows)
  _rowAt(sc, y) {
    const top = sc.getBoundingClientRect().top - sc.scrollTop + y;
    const rows = sc.querySelectorAll(':scope > .blk');
    let lo = 0, hi = rows.length - 1, at = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (rows[mid].getBoundingClientRect().bottom > top) { at = mid; hi = mid - 1; } else lo = mid + 1;
    }
    return at < 0 ? null : rows[at];
  }

  // the first visible block and where it is
  _firstVisible() {
    const sc = this._sc();
    const el = sc && sc.clientHeight ? this._rowAt(sc, sc.scrollTop) : null;
    return el ? { el, top: el.getBoundingClientRect().top } : null;
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
          ...(b.kind === 'msg' && b.files ? { files: b.files } : {}),
          ...(b.kind === 'ask' ? { eid: b.eid, action: b.action, fields: formFields(b.schema).map((f) => f.key), content: b.content } : {}),
          ...(b.kind === 'perm' ? { plan: isPlanApproval(b.tool) } : {}),
          ...(b.kind === 'thought' ? { done: b.done, ms: (b.t1 || 0) - (b.t0 || 0) } : {}) }));
      },
      get pending() {
        return a._blocks().filter((b) => b.kind === 'perm' && !b.by).map((b) => ({ pid: b.pid, cmd: rawText(b.tool?.rawInput), options: (b.options || []).map((o) => o.optionId),
          scoped: b.rule ? b.rule.scoped : true, plan: isPlanApproval(b.tool) }));
      },
      get followUp() { return a._followUp ? a._followUp.text : null; },
      // the rendered window (D124): total blocks, the first rendered index, rows in the DOM, the scroller
      get window() {
        const sc = a._sc();
        return { total: a._blocks().length, from: a._start, rendered: sc ? sc.querySelectorAll(':scope > .blk').length : 0, atBottom: a._atBottom !== false,
          scrollTop: sc ? sc.scrollTop : 0, scrollHeight: sc ? sc.scrollHeight : 0, clientHeight: sc ? sc.clientHeight : 0 };
      },
      scrollTo(y) { const sc = a._sc(); if (sc) sc.scrollTop = y; },
      firstVisible() { const f = a._firstVisible(); return f ? Number(f.el.dataset.k) : null; },
      topOf(key) { const sc = a._sc(); const el = sc && sc.querySelector(`:scope > .blk[data-k="${key}"]`); return el ? el.getBoundingClientRect().top - sc.getBoundingClientRect().top : null; },
      loadAll() { a._older(Infinity); },
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

const PAGE = 30; // blocks per rendered page: the initial tail, and each earlier load
const TRIM = 120; // following the bottom with more rendered than this (and 6 views above) drops the far top

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

customElements.define('bx-agent', BxAgent);
