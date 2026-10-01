/**
 * bx-agent.js — the Agent tab in the terminal window (<bx-agent
 * session=<id> component=<cwd>>). An agent session is a terminal session
 * whose sandbox runs a coding agent over the Agent Client Protocol (D74,
 * docs/overview/09-terminals.md §Agent sessions); this element drives it
 * through the agent API and renders its typed event log.
 *
 * Source of truth is the session's event log: on mount the element reads its
 * tail page (`GET …/events?limit=`; the whole log from an xbind that does not
 * page), then applies live `session` events for its topic. Because the events
 * hub drops a slow subscriber and reconnects silently, the element never
 * trusts the live stream alone — it re-fetches `?since=<last>` whenever a
 * live seq skips, when the tab becomes visible, and every 2 s while a turn
 * runs; a `replayed` event (a resume's replay is in the log) re-reads the
 * tail. Any attached client (this tab, another browser, `bx agent`) sees the
 * same stream, and the first to answer a permission request wins.
 *
 * A long transcript is windowed twice (D124, D130). The element holds only
 * some pages of the log (agent-pages.js: segments folded on their own, block
 * keys from seqs), loading older ones as the reader nears the top and
 * dropping whole pages about three views beyond the rendered rows in either
 * direction — the live tail too while the reader is far up, when a "↓ N new"
 * pill brings the latest back. Of those it renders a window of rows, grown
 * and trimmed around the view (scroll-window.js), at most once per frame,
 * and not at all while the tab is hidden. Nothing jumps: every update keeps
 * the first visible row where it was (or follows the bottom), measured before
 * lit commits and corrected in updated(), before the frame paints. A
 * streaming message re-parses only its last paragraph (mdLive).
 *
 * Events: 'bx-session' (detail {id, kind:'agent', provider, name, net,
 * scopes, label, gpu, api}) when it creates the
 * session, so the frame records the id; 'bx-exit' when the session ends.
 */
import { LitElement, html, css, nothing } from 'lit';
import { scrollCss } from '/vendor/scroll-css.js';
import { repeat, guard } from 'lit';
import { onEvent } from '/vendor/events-socket.js';
import { headline, missingRequired } from '/vendor/agent-tools.js';
import { Transcript, PAGE_LIMIT } from '/vendor/agent-pages.js';
import { ScrollWindow } from '/vendor/scroll-window.js';
import { toolCard, permCard, changesCard, askCard, cardsCss, mdLive } from '/vendor/agent-cards.js';
import { agentTestApi } from '/vendor/agent-testapi.js';
import { slashQuery, matchCommands, commandHint } from '/vendor/agent-slash.js';
import '/vendor/agent-signin.js';

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
    _error: { state: true },
    _authErr: { state: true }, // the last create/turn failed auth (show the sign-in banner)
    _signinTerm: { state: true }, // the person chose a terminal over the guided sign-in
    _signedNote: { state: true }, // a guided sign-in just worked: what to do now
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
    .md-b { display: contents; }
    .agent .bubble > :first-child, .agent .bubble > .md-b:first-child > :first-child { margin-top: 0; }
    .agent .bubble > :last-child, .agent .bubble > .md-b:last-child > :last-child { margin-bottom: 0; }
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
    .thought .md > .md-b:first-child > :first-child { margin-top: 4px; } .thought .md > .md-b:last-child > :last-child { margin-bottom: 0; }
    .plan { border: 1px solid var(--bx-border, #363c45); border-radius: 6px; padding: 6px 10px; margin: 0 0 8px; }
    .plan .h { font: 10px var(--bx-mono, ui-monospace, monospace); text-transform: uppercase; color: var(--bx-muted, #868f9a); margin-bottom: 4px; }
    .plan li { list-style: none; margin: 1px 0; }
    .plan .done { color: var(--bx-green, #4caf50); text-decoration: line-through; opacity: .7; }
    .gap { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); text-align: center; margin: 4px 0; }
    .notice { color: var(--bx-muted, #868f9a); font: 11px var(--bx-mono, ui-monospace, monospace); margin: 4px 0; white-space: pre-wrap; }
    .turn { border-top: 1px dashed var(--bx-border, #363c45); margin: 10px 0; padding-top: 4px;
      font: 10px var(--bx-mono, ui-monospace, monospace); color: var(--bx-muted, #868f9a); text-align: center; }
    .foot { flex: none; border-top: 1px solid var(--bx-border, #363c45); padding: 6px 8px; position: relative; }
    .pill { position: absolute; bottom: calc(100% + 8px); left: 50%; transform: translateX(-50%); z-index: 1; white-space: nowrap;
      border: 1px solid var(--bx-accent, #f5a623); border-radius: 12px; padding: 3px 12px; cursor: pointer;
      background: var(--bx-panel-2, #2b3038); color: var(--bx-accent, #f5a623); font: 11px var(--bx-mono, ui-monospace, monospace); }
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
    .status.signed { color: var(--bx-green, #4caf50); }
    .status.ended button { margin-left: auto; border: 1px solid var(--bx-accent, #f5a623); background: transparent; color: var(--bx-accent, #f5a623);
      border-radius: 5px; padding: 2px 8px; cursor: pointer; font: 11px var(--bx-mono, ui-monospace, monospace); font-weight: 600; white-space: nowrap; }
    .status.ended button:hover { background: var(--bx-accent, #f5a623); color: #1b1e24; }
  `];

  constructor() {
    super();
    this._tx = new Transcript((q) => this._get(q)); // the pages of the log held (agent-pages.js)
    this._sw = new ScrollWindow({ onScroll: () => this._scrolled(), onResize: () => this._fill() });
    this._fromKey = null; // the window's first rendered block; null = the last PAGE
    this._toKey = null; // its last; null = to the end
    this._start = 0; // their indices, as of the last render
    this._end = 0;
    this._all = false; // "load all": everything loaded and rendered (find)
    this._busy = null; // a page load in flight
    this._fetches = []; // the queries this element made (the harness counts them)
    this._opened = new Set(); // '<key>:<slot>' of lazy <details> bodies the user opened
    this._providers = null;
    this._provider = '';
    this._mode = '';
    this._draft = '';
    this._error = '';
    this._authErr = false;
    this._signinTerm = false;
    this._signedNote = '';
    this._signedSeq = null; // the log's end when a guided sign-in worked
    this._followUp = null;
    this._slashSel = 0;
    this._slashOff = false;
    this._askVals = {}; // eid → the form's values while a question is open
    this._planFeedback = {}; // pid → the feedback typed beside "keep planning"
    this._started = false; // guard: create the eager session at most once
    this._off = null;
    this._poll = null;
    this._creating = false;
    this._onVisible = () => { if (document.visibilityState === 'visible' && this.session) this._refetch(); };
  }

  connectedCallback() {
    super.connectedCallback();
    this._off = onEvent((e) => this._live(e));
    document.addEventListener('visibilitychange', this._onVisible);
    // a hidden tab folds but does not render (shouldUpdate); shown, it catches up
    this._hostRO = new ResizeObserver(() => { if (this._stale && !this._hidden()) this.requestUpdate(); });
    this._hostRO.observe(this);
    if (this.history) { this._loadHistory(); return; }
    this._loadProviders(); // for the sign-in (D178) and mode names, every path
    if (this.session) { this._open(); return; }
    this._maybeEager();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    document.removeEventListener('visibilitychange', this._onVisible);
    clearInterval(this._poll); this._poll = null;
    this._sw.detach();
    this._hostRO?.disconnect(); this._hostRO = null;
  }

  // at most one render per frame: a replay's burst of events (or a keystroke
  // plus an event) folds as it arrives and paints once, and updated() — the
  // scroll correction — runs inside the frame, before it paints
  async scheduleUpdate() {
    await new Promise((r) => requestAnimationFrame(r));
    super.scheduleUpdate();
  }

  _hidden() { return !this.isConnected || this.getClientRects().length === 0; }

  // a hidden tab (display:none — another tab is active) keeps folding what
  // arrives, but renders nothing until it is shown (the host's resize)
  shouldUpdate(ch) {
    if (!this._hidden()) { this._stale = false; return true; }
    this._react(ch);
    this._stale = true;
    return false;
  }

  // what the properties set in motion, rendered or not
  _react(ch) {
    if (ch.has('ended') && this.ended) this._maybePoll();
    // a reattach (the frame set our session after a listing): read the tail
    if (ch.has('session') && this.session && !this._tx.segs.length && !this.ended && !this.history) this._open();
    if (ch.has('provider')) this._maybeEager();
    if (this._followUp) this._maybeFollowUp();
  }

  willUpdate() {
    // the window [start, end) of the loaded blocks, by key: following the
    // bottom it reaches the end; a long rendered part is trimmed on the side
    // far from the view (scroll-window.js; KEEP > FILL: no ping-pong)
    const blocks = this._blocks(), n = blocks.length, sw = this._sw;
    const at = (k) => (k == null ? -1 : blocks.findIndex((b) => b.key === k));
    const pinned = sw.atBottom && !sw.keepView;
    let end = pinned || this._toKey == null ? n : at(this._toKey) + 1;
    if (end <= 0) end = n;
    let start = at(this._fromKey);
    if (start < 0 || start >= end) start = Math.max(0, end - PAGE);
    if (this._all) { start = 0; end = n; } else if (end - start > TRIM) {
      const ka = sw.trimAbove(), a = ka == null ? -1 : at(Number(ka));
      if (a > start && a < end) start = a;
      const kb = pinned ? null : sw.trimBelow(), b = kb == null ? -1 : at(Number(kb));
      if (b >= start && b + 1 < end) end = b + 1;
    }
    this._start = start;
    this._end = end;
    this._fromKey = blocks[start]?.key ?? null;
    this._toKey = end < n ? blocks[end - 1].key : null;
    this._pillAt = sw.atBottom;
    this._drawn = this._tx.version; // what _start/_end index
    sw.before();
  }

  updated(ch) {
    this._react(ch);
    const sc = this._sc();
    if (!sc) return;
    this._sw.attach(sc);
    const pinned = this._sw.atBottom && !this._sw.keepView;
    this._sw.after();
    // willUpdate measured the view before this render's blocks landed: a
    // burst folded into one frame would stay rendered whole until the next
    // event, so measure again with it in place
    if (pinned && this._end - this._start > TRIM && this._sw.trimAbove() != null) this.requestUpdate();
    this._fill();
  }

  // the reader scrolled (a frame later; a touch scroll once settled)
  _scrolled() {
    const at = this._sw.atBottom;
    this._tx.follow = at;
    if (at) this._tx.seenAll();
    if (at !== this._pillAt) this.requestUpdate(); // the pill
    this._fill();
  }

  // grow the window where the reader is heading — rows already loaded, else
  // a page (older above; below, one dropped earlier, or the tail back) —
  // and let go of what is far away
  _fill() {
    if (this._all || this._stale || !this._sc() || this._tx.version !== this._drawn) return; // a render is due: it fills after
    const sw = this._sw, tx = this._tx, blocks = this._blocks();
    if (sw.wantsAbove()) {
      if (this._start > 0) { this._fromKey = blocks[Math.max(0, this._start - PAGE)].key; this.requestUpdate(); } else if (tx.hasOlder) this._load(() => tx.loadOlder());
    } else if (sw.wantsBelow()) {
      const e = Math.min(blocks.length, this._end + PAGE);
      if (this._end < blocks.length) { this._toKey = e < blocks.length ? blocks[e - 1].key : null; this.requestUpdate(); } else if (tx.hasNewer) this._load(() => tx.loadNewer());
    }
    this._unload();
  }

  // pages about UNLOAD views beyond the rendered rows go (the live tail too,
  // while the reader is away from the bottom), and the memos of loaded
  // blocks that far out; a live tail grown past a few pages is split so its
  // top can go too
  _unload() {
    const tx = this._tx;
    if (!tx.paged || this._all) return;
    const m = Math.ceil(this._sw.rowsPerView() * UNLOAD);
    if (tx.keep(this._start - m, this._end + m, !this._sw.atBottom)) this.requestUpdate();
    const key = `${this._fromKey}:${this._toKey}:${tx.version}`;
    if (key !== this._memoKey) {
      this._memoKey = key;
      const blocks = this._blocks(), a = this._start - m, z = this._end + m;
      for (let i = 0; i < blocks.length; i++) if ((i < a || i >= z) && blocks[i].$memo) delete blocks[i].$memo;
    }
    // (tried again only after another page: a turn that cannot be cut yet — an
    // open plan spans it — must not ask on every frame)
    const tail = tx.segs[tx.segs.length - 1], n = tail ? tail.events.length : 0;
    if (!this._busy && this._sw.atBottom && !tx.detached && n > Math.max(3 * PAGE_LIMIT, (this._splitTried || 0) + PAGE_LIMIT)) {
      this._splitTried = n;
      this._load(() => tx.splitTail().then((ok) => { if (ok) this._splitTried = 0; return ok; }));
    }
  }

  // one page load at a time; its blocks render when it lands
  _load(fn) {
    if (this._busy) return;
    this._busy = fn().then((changed) => { if (changed) this.requestUpdate(); }).catch(() => { }).finally(() => { this._busy = null; });
  }

  // ---- data ----

  async _loadProviders() {
    try {
      const r = await fetch('/api/xbin/agent/providers');
      if (!r.ok) return;
      const ps = await r.json();
      this._providers = ps;
      if (ps.length && !this._provider && !this.session) { this._provider = ps[0].id; this._mode = ps[0].defaultMode || ''; }
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
  // only — no process, no polling, paged like a live one. The foot offers
  // Resume where the agent can reopen it, else a fresh start on this tile.
  async _loadHistory() {
    try {
      await this._tx.openTail();
      this._historyMeta = this._tx.meta;
      this.requestUpdate();
    } catch (e) { this._error = e.status === 404 ? 'this past session is gone' : e.status ? `could not load it (${e.status})` : String(e.message || e); }
    this.ended = true;
  }

  _doResume() { if (this._historyMeta) this.dispatchEvent(new CustomEvent('bx-resume', { detail: this._historyMeta, bubbles: true })); }
  _doNewHere() { if (this._historyMeta) this.dispatchEvent(new CustomEvent('bx-new-agent', { detail: { provider: this._historyMeta.provider }, bubbles: true })); }

  // the events route of this session (or past session), with a query
  async _get(q) {
    this._fetches.push(q);
    const r = await fetch(this.history ? `/api/xbin/agent/history/${encodeURIComponent(this.history)}/events?${q}`
      : `/api/xbin/term/sessions/${encodeURIComponent(this.session)}/events?${q}`);
    if (!r.ok) throw Object.assign(new Error(`${r.status}`), { status: r.status });
    return r.json();
  }

  // read the tail and follow the bottom from it (open, a replay, "jump to
  // latest"); then catch up with whatever the live stream said meanwhile
  _open() {
    if (!this.session) return null;
    return (this._opening ||= this._openTail().finally(() => { this._opening = null; }));
  }

  async _openTail() {
    try {
      await this._tx.openTail();
      this._fromKey = this._toKey = null;
      this._all = false;
      this._sw.toBottom();
      this.requestUpdate();
      this._maybePoll();
      this._refetch();
    } catch (e) { if (e.status === 404) this._end(); /* else transient: the live stream or the next poll recovers */ }
  }

  async _refetch() {
    if (!this.session || this.history || !this._tx.segs.length) return;
    try {
      const r = await this._get(`since=${this._tx.lastSeq}`);
      if (this._tx.apply(r.events || [], !!r.truncated)) this.requestUpdate();
      this._maybePoll();
    } catch (e) { if (e.status === 404) this._end(); }
  }

  _reset() { this._tx.reset(); this._fromKey = this._toKey = null; this._all = false; this._opened.clear(); }

  // a live event for our session; a skipped seq means the hub dropped one,
  // so re-fetch from the last we have rather than trust the gap. A resume's
  // replay arrives as one `replayed` event: it is in the log, read the tail.
  _live(e) {
    if (e.type !== 'session' || !this.session || e.topic !== 'session.' + this.session) return;
    const ev = e.data;
    if (ev && ev.type === 'replayed') { this._open(); return; }
    if (!ev || typeof ev.seq !== 'number' || !this._tx.segs.length) return;
    if (ev.seq <= this._tx.lastSeq) return;
    if (ev.seq === this._tx.lastSeq + 1) { if (this._tx.apply([ev])) this.requestUpdate(); this._maybePoll(); } else this._refetch(); // a gap: catch up from the log
  }

  // "↓ N new — jump to latest": the tail (fetched again when it was let go),
  // followed
  async _jumpLatest() {
    this._all = false;
    if (this._tx.hasNewer) {
      try { await (this.history ? this._tx.openTail() : this._open()); } catch { /* the pill stays */ }
    }
    this._fromKey = this._toKey = null;
    this._sw.toBottom();
    this._tx.follow = true;
    this._tx.seenAll();
    this.requestUpdate();
  }

  // "load all" (find): every page, rendered — an explicit mode, left by the
  // pill; nothing unloads meanwhile
  async _loadAll() {
    this._all = true;
    this.requestUpdate();
    const tx = this._tx;
    try {
      while (this._all && tx.hasOlder) await tx.loadOlder(2000);
      while (this._all && tx.hasNewer) await tx.loadNewer(2000);
    } catch { /* what loaded shows */ }
    this.requestUpdate();
  }

  // "load earlier": a page of rows above the window, loaded first if need be
  _older() {
    const blocks = this._blocks();
    if (this._start > 0) { this._fromKey = blocks[Math.max(0, this._start - PAGE)].key; this.requestUpdate(); } else if (this._tx.hasOlder) this._load(() => this._tx.loadOlder());
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
      this._error = ''; this._authErr = false; this._signedNote = ''; this._refetch();
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
      this._open();
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
    if (text) this._followUp = { text, after: this._tx.lastSeq };
    this._permit(pid, null, optionId);
  }

  _maybeFollowUp() {
    const f = this._followUp;
    const st = this._tx.statusAfter(f.after);
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
  _commands() { return this._tx.st.commands || []; }

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

  _options() { return this._tx.st.options || []; }

  // the agent's permission modes: from the last status that carried them, else
  // the provider's advertised list (a fallback before the first status lands)
  _modes() {
    let ms = this._tx.st.modes || [];
    if (!ms.length) { const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider)); ms = (p && p.modes) || []; }
    return ms;
  }

  _looksAuth(s) { return /sign[\s-]?in|authenticat|not logged in|-32000/i.test(String(s || '')); }

  // the sign-in prompt to show, if any: the backend marks a status with
  // login:{needed,provider,command} when the agent says it is signed out (or a
  // turn hit -32000); a create/prompt error that reads like auth is a fallback
  _login() {
    // signed in here (D178): the agent says "signed out" until a turn works,
    // so the prompt stays down unless one fails again
    if (this._signedSeq != null && this._tx.statusAfter(this._signedSeq) !== 'error') return null;
    const last = this._tx.st.last;
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

  // the provider's guided sign-in (D178: GET /agent/providers `signin`), if any
  _signinSpec() {
    const p = (this._providers || []).find((x) => x.id === (this.provider || this._provider));
    return (p && p.signin && p.signin.command) ? p.signin : null;
  }

  // the guided sign-in worked: the prompt goes, the person sends again
  _signedIn(lg) {
    this._signedSeq = this._tx.lastSeq;
    this._authErr = false;
    this._signinTerm = false;
    const asked = this._blocks().some((b) => b.kind === 'msg' && b.role === 'user');
    this._signedNote = `Signed in to ${lg.provider}.${asked ? ' Send your message again.' : ''}`;
  }

  // "use a terminal instead": today's shell tab, running the login — or the
  // CLI's fallback when it is too old for the guided command
  _signinTerminal(lg, ev) {
    this._signinTerm = true;
    const spec = this._signinSpec();
    this._doSignIn({ ...lg, command: ev.detail?.fallback && spec?.fallback ? spec.fallback : lg.command });
  }

  _signin(lg) {
    const spec = this._signinSpec();
    if (spec && !this._signinTerm) {
      return html`<bx-agent-signin .spec=${spec} provider=${lg.provider} component=${this.component}
        @bx-signin-done=${() => this._signedIn(lg)} @bx-signin-terminal=${(ev) => this._signinTerminal(lg, ev)}></bx-agent-signin>`;
    }
    return html`<div class="signin">
      <span class="msg">Not signed in to ${lg.provider}.</span>
      <button @click=${() => this._doSignIn(lg)} title="open a terminal that runs the sign-in command in this agent's home">Sign in to ${lg.provider}</button>
      ${spec ? html`<button @click=${() => { this._signinTerm = false; }} title="sign in here: a link to open and a code to paste">Guided sign-in</button>` : nothing}
    </div>`;
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
  _blocks() { return this._tx.blocks(); }

  _status() { return this._tx.st.status || (this.session ? 'starting' : 'new'); }
  _statusDetail() { return this._tx.st.detail; }
  _curMode() { return this._tx.st.currentMode || this._mode; }

  // ---- render ----

  render() {
    const status = this.ended ? 'exited' : this._status();
    const busy = !this.ended && (status === 'running' || status === 'waiting_permission' || status === 'cancelling');
    const lg = this._login();
    const blocks = this._blocks(), tx = this._tx;
    const from = this._start; // willUpdate placed the window
    const win = from || this._end < blocks.length ? blocks.slice(from, this._end) : blocks;
    const running = !this.ended && status === 'running';
    return html`
      <div class="scroll">
        ${tx.truncated && !from ? html`<div class="gap">… earlier events dropped (log limit)</div>` : nothing}
        ${from || tx.hasOlder ? html`<div class="gap earlier">… ${from ? `${from}${tx.hasOlder ? '+' : ''} ` : ''}earlier ${from === 1 && !tx.hasOlder ? 'entry' : 'entries'}
          <button @click=${() => this._older()}>load earlier</button><button @click=${() => this._loadAll()}>load all</button></div>` : nothing}
        ${this.restarting ? html`<div class="hint">Restarting the agent in a new sandbox — the conversation resumes where the agent can reopen it…</div>` : nothing}
        ${!this.session && !this.provider && !this.history && !this.restarting && !blocks.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${!this.session && this.provider && !this.history && !lg ? html`<div class="hint">Starting ${this._provName()}…</div>` : nothing}
        ${repeat(win, (b) => b.key, (b) => this._row(b, running))}
        ${this._end >= blocks.length && !tx.hasNewer ? this._activity(status, blocks) : nothing}
      </div>
      <div class="foot">
        ${this._pill(blocks)}
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
        ${lg ? this._signin(lg) : this._signedNote ? html`<div class="status signed">${this._signedNote}</div>` : nothing}
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

  // "↓ N new — jump to latest", while the reader is away from the bottom and
  // something is below: new entries, or rows or pages not shown
  _pill(blocks) {
    const tx = this._tx;
    if (this._sw.atBottom || this._all || !(tx.fresh || tx.hasNewer || this._end < blocks.length)) return nothing;
    return html`<button class="pill" @click=${() => this._jumpLatest()}>↓ ${tx.fresh ? `${tx.fresh} new — ` : ''}jump to latest</button>`;
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
    const u = this._tx.st.usage;
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
    this._sw.keepView = true; // the reader opened it: keep their view, don't chase the bottom
    this.requestUpdate();
  }

  _block(b) {
    switch (b.kind) {
      case 'msg':
        return b.role === 'user'
          ? html`<div class="row user"><div class="who">you</div><div class="bubble">${b.text}${b.files?.length ? html`<div class="files ${b.text ? 'below' : ''}">${b.files.map((f) =>
              html`<span class="file" title=${`${f.mime || ''} · ${fmtN(f.size || 0)} bytes`}>${f.name}</span>`)}</div>` : nothing}</div></div>`
          : html`<div class="row agent"><div class="who">agent</div><div class="bubble" ${mdLive(b.text, b)}></div></div>`;
      case 'thought': {
        // open while it streams (the last block of a running turn), then
        // folded to its duration — the Zed/Claude Code pattern
        const live = !b.done && !this.ended && this._status() === 'running';
        const secs = Math.max(1, Math.round(((b.t1 || 0) - (b.t0 || 0)) / 1000));
        return html`<div class="row"><details class="thought" ?open=${live} @toggle=${(e) => this._toggled(e, b, 'body', live)}>
          <summary>${live ? html`<span class="shimmer">Thinking…</span>` : `Thought for ${secs}s`}</summary>
          ${live || this._isOpen(b, 'body') ? html`<div class="md" ${mdLive(b.text, b)}></div>` : nothing}</details></div>`;
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
      case 'notice': // xbin's own line (a `notice` event), not the agent's
        return html`<div class="notice">${b.text}</div>`;
      default:
        return nothing;
    }
  }

  _sc() { return this.renderRoot?.querySelector('.scroll'); }

  _autosize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, window.innerHeight * 0.4) + 'px';
  }

  // Test hook (the harness): the transcript as plain records, and the actions.
  testApi() { return agentTestApi(this); }
}

const PAGE = 30; // rows the window grows by: the initial tail, and each step up or down
const TRIM = 120; // a window longer than this (and 6 views past the view) is trimmed on that side
const UNLOAD = 3; // pages about this many views beyond the rendered rows are let go

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

customElements.define('bx-agent', BxAgent);
