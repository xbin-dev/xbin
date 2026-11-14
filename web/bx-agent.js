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
 * What it draws is the chat kit's (D224, docs/frontend-kit.md → the chat
 * kit): the fold's blocks said in the kit's block model (agent-fold.js
 * blockOf), its views for the rows (/vendor/chat/views.js: messages, tool
 * cards, what the agent asks), <bx-chat-composer> for the message box, and
 * ChatWindow for the window of rows. This element is the host: it owns the
 * session, the pages of its log and the actions, and the views reach it
 * only through the ChatCtx it hands them (chatCtx).
 *
 * Events: 'bx-session' (detail {id, kind:'agent', provider, name, net,
 * scopes, label, gpu, api}) when it creates the
 * session, so the frame records the id; 'bx-exit' when the session ends.
 */
import { LitElement, html, nothing, unsafeCSS } from '/vendor/lit-all.min.js';
import { scrollCssText } from '/vendor/bx-scroll.js';
import '/vendor/bx-icons.js';
import '/vendor/bx-md.js'; // the markdown highlighter up front: the kit's renderer colours code from its first paint here
import { diffHTML, langFor } from '/vendor/bx-code.js';
import { onEvent } from '/vendor/events-socket.js';
import { Transcript, PAGE_LIMIT } from '/vendor/agent-pages.js';
import { blockOf } from '/vendor/agent-fold.js';
import { block } from '/vendor/chat/blocks.js';
import { rowsView, activityView, usageView } from '/vendor/chat/views.js';
import { signinView } from '/vendor/chat/asks.js';
import { useDiffHighlighter } from '/vendor/chat/cards.js';
import { chatCssText } from '/vendor/chat/css.js';
import { ChatWindow } from '/vendor/chat/window.js';
import '/vendor/chat/composer.js';
import { agentCss } from '/vendor/agent-css.js';
import { FileDiffs } from '/vendor/agent-diffs.js';
import { agentTestApi } from '/vendor/agent-testapi.js';
import { queueURL, stop, takeBack } from '/vendor/agent-queue.js';
import { agentPrefs } from '/vendor/agent-prefs.js';
import '/vendor/agent-signin.js';

useDiffHighlighter(diffHTML, langFor); // the code panels' highlighter is here already: diffs are coloured at once

export class BxAgent extends LitElement {
  static properties = {
    session: { type: String },
    component: { type: String },
    provider: { type: String }, // set by the launcher: create eagerly so the model picker loads before the first prompt
    mode: { type: String },
    history: { type: String }, // a PAST session id (GET /agent/history): its persisted transcript, read-only
    resume: { type: String }, // a past session id to reopen when creating (POST /term/sessions {resume})
    ask: { attribute: false, hasChanged: () => false }, // (spec) → Promise: the window's themed dialog (bx-frame's), for a confirmation; absent, the browser's
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
    _notOffered: { state: true }, // the harnesses this tile's rules keep out, each with why (D229)
  };

  // product-ui §8 (D184): the chat kit's one stylesheet for what it draws
  // (css.js), agent-css.js for this element's own chrome around it
  static styles = [unsafeCSS(scrollCssText), unsafeCSS(chatCssText), agentCss];

  constructor() {
    super();
    const tx = this._tx = new Transcript((q) => this._get(q)); // the pages of the log held (agent-pages.js)
    // the window of rows rendered (chat/window.js) over those pages
    this._win = new ChatWindow({
      source: {
        get version() { return tx.version; }, get hasOlder() { return tx.hasOlder; }, get hasNewer() { return tx.hasNewer; },
        get fresh() { return tx.fresh; }, get paged() { return tx.paged; },
        loadOlder: (n) => tx.loadOlder(n), loadNewer: (n) => tx.loadNewer(n),
        latest: () => (this.history ? tx.openTail() : this._open()),
        keep: (lo, hi, dropTail) => tx.keep(lo, hi, dropTail),
        follow: (at) => { tx.follow = at; if (at) tx.seenAll(); },
      },
      changed: () => this.requestUpdate(), keyOf: (b) => b.key, page: PAGE, trim: TRIM, unload: UNLOAD,
      paused: () => !!this._stale, onUnload: (lo, hi) => this._unloaded(lo, hi),
    });
    this._fetches = []; // the queries this element made (the harness counts them)
    this._opened = new Set(); // '<key>:<slot>' of lazy <details> bodies the user opened
    this._ui = new Map(); // block key → a counter that moves when something kept here for it changed (ChatCtx.rev)
    this._bags = new Map(); // block key → what a widget of its card keeps (a form's values)
    // the files' diffs the reader opened (D210); a load lands on the block's row
    this._diffs = new FileDiffs({ onLoad: (o) => this._touch(o.block || { k: String(o.key) }) });
    this._ctx = this._makeCtx();
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
    this._notOffered = [];
    this.ask = null;
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
    this._providersP = this._loadProviders(); // for the sign-in (D178) and mode names, every path
    if (this.session) { this._open(); return; }
    this._maybeEager();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    document.removeEventListener('visibilitychange', this._onVisible);
    clearInterval(this._poll); this._poll = null;
    this._win.detach();
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

  // the window [start, end) of the loaded blocks is placed right before the
  // render and the reader's view corrected right after it (chat/window.js)
  willUpdate() { this._win.place(this._blocks()); }

  updated(ch) {
    this._react(ch);
    this._win.after(this._sc());
  }

  // after pages far from the reader were let go: the memos (and the kit's
  // blocks) of loaded blocks that far out go too; a live tail grown past a
  // few pages is split so its top can go as well
  _unloaded(lo, hi) {
    const tx = this._tx, win = this._win;
    const key = `${win.fromKey}:${win.toKey}:${tx.version}`;
    if (key !== this._memoKey) {
      this._memoKey = key;
      const blocks = this._blocks();
      for (let i = 0; i < blocks.length; i++) if ((i < lo || i >= hi) && blocks[i].$memo) { delete blocks[i].$memo; delete blocks[i].$block; }
    }
    // (tried again only after another page: a turn that cannot be cut yet — an
    // open plan spans it — must not ask on every frame)
    const tail = tx.segs[tx.segs.length - 1], n = tail ? tail.events.length : 0;
    if (!win.busy && win.atBottom && !tx.detached && n > Math.max(3 * PAGE_LIMIT, (this._splitTried || 0) + PAGE_LIMIT)) {
      this._splitTried = n;
      win.load(() => tx.splitTail().then((ok) => { if (ok) this._splitTried = 0; return ok; }));
    }
  }

  // ---- data ----

  // The catalog for a session that exists (its names, modes and sign-in);
  // for one about to start, what this tile's orgs allow (?cwd=, D229) — the
  // chooser offers the person's own harness first when it is among them.
  async _loadProviders() {
    try {
      const fresh = !this.session && !this.resume;
      // &all=1: the harnesses this tile's rules keep out come along, each with why (`unavailable`) — the chooser says it
      const r = await fetch('/api/xbin/agent/providers' + (fresh && this.component ? `?cwd=${encodeURIComponent(this.component)}&all=1` : ''));
      if (!r.ok) return;
      const rows = await r.json();
      const ps = rows.filter((p) => !p.unavailable);
      this._notOffered = rows.filter((p) => p.unavailable);
      this._providers = ps;
      if (!ps.length || this._provider || this.session) return;
      await agentPrefs().load();
      if (this._provider || this.session) return; // chosen meanwhile
      const p = ps.find((x) => x.id === agentPrefs().harness) || ps[0];
      this._provider = p.id; this._mode = p.defaultMode || '';
    } catch { /* offline; the composer still shows */ }
  }

  // the provider row of this tab's harness (a bare id while the catalog loads)
  _provRow() {
    const id = this._provider || this.provider || this._tx.meta?.provider || '';
    return (this._providers || []).find((p) => p.id === id) || { id, modes: [] };
  }

  // What a new session starts with (D229): the mode picked in the chooser,
  // else the person's saved mode, model and settings for this harness
  // (agent-prefs.js; never a bypass mode), else the provider's default. A
  // resumed session keeps its own.
  async _startWith() {
    if (this.resume) return { mode: this._mode };
    await Promise.all([this._providersP, agentPrefs().load()]).catch(() => { });
    const saved = agentPrefs().startWith(this._provRow());
    return { ...saved, mode: this._modePicked ? this._mode : (this.mode || saved.mode || this._mode) };
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
      this._win.reset();
      this._win.toBottom();
      this.requestUpdate();
      this._maybePoll();
      this._refetch();
    } catch (e) { if (e.status === 404) this._gone(); /* else transient: the live stream or the next poll recovers */ }
  }

  async _refetch() {
    if (!this.session || this.history || !this._tx.segs.length) return;
    try {
      const r = await this._get(`since=${this._tx.lastSeq}`);
      if (this._tx.apply(r.events || [], !!r.truncated)) this.requestUpdate();
      this._maybePoll();
    } catch (e) { if (e.status === 404) this._gone(); }
  }

  _reset() { this._tx.reset(); this._win.reset(); this._opened.clear(); this._ui.clear(); this._bags.clear(); }

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

  // "↓ N new — jump to latest", "load all" (find) and "load earlier": the window's (chat/window.js)
  _jumpLatest() { return this._win.latest(); }
  _loadAll() { return this._win.loadAll(); }
  _older() { this._win.older(); }

  // Poll while the session's state can still move without this tab hearing
  // of it: a turn that runs, and a session that is starting — its `idle`
  // arrives as one live event, and the events hub drops a slow subscriber
  // without a word, so a tab that missed it would say "starting" for ever
  // (seen on a loaded machine: a resumed session never became idle here).
  _maybePoll() {
    const st = this._status();
    const busy = !this.ended && !this.history && !!this.session && (st === 'running' || st === 'waiting_permission' || st === 'cancelling' || st === 'starting');
    if (busy && !this._poll) this._poll = setInterval(() => this._refetch(), 2000);
    else if (!busy && this._poll) { clearInterval(this._poll); this._poll = null; }
  }

  _gone() {
    // the session is gone server-side (exited, removed): the frame keeps the
    // tab as ended so the transcript — and the reason — stay readable. (Not
    // _end: that name is the rendered window's end index, a field that
    // shadowed the method, so a 404 threw instead of ending the tab.)
    this.ended = true;
    this._maybePoll();
    this.dispatchEvent(new CustomEvent('bx-exit', { bubbles: true }));
  }

  // ---- actions ----

  // interrupt (Ctrl/⌘+Enter while a turn runs): stop it, then send (D197)
  // files: what the composer staged ([{name, mime, data}], base64); sent(): tell it they went
  async _submit(interrupt = false, files = [], sent = null) {
    const text = this._draft.trim();
    if (this._creating || this.restarting) return; // restarting: the frame brings the new session
    if (!this.session) {
      if (!(await this._create())) return;
      if (!text && !files.length) return; // started; the settings pickers show now, the first prompt can wait
    }
    if (!text && !files.length) return;
    if (await this._prompt(text, interrupt, files)) { this._draft = ''; if (sent) sent(); } // else keep the draft (and the files) to retry
  }

  // every message goes through …/queue (D197): idle, it starts a turn; while
  // one runs the agent takes it mid-turn, or xbind holds it for the turn's
  // end. Its files go with it as attachments (the session API writes them
  // into the agent's sandbox; an image the model can see goes inline too).
  async _prompt(text, interrupt = false, files = []) {
    try {
      const attachments = files.map((f) => ({ name: f.name, ...(f.mime ? { mime: f.mime } : {}), data: f.data }));
      const r = await fetch(queueURL(this),
        { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text, interrupt, ...(attachments.length ? { attachments } : {}) }) });
      if (!r.ok) { this._error = (await r.json().catch(() => ({}))).error || `prompt failed (${r.status})`; this._authErr = this._looksAuth(this._error); return false; }
      this._error = ''; this._authErr = false; this._signedNote = ''; this._refetch();
      return true;
    } catch (e) { this._error = String(e.message || e); return false; }
  }

  async _create() {
    this._creating = true; this._error = '';
    try {
      const start = await this._startWith();
      const r = await fetch('/api/xbin/term/sessions', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ cwd: this.component, kind: 'agent', provider: this._provider, mode: start.mode, model: start.model, options: start.options, resume: this.resume || undefined, vm: this.vm || undefined }),
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

  // → Promise<boolean>: the answer went
  _permit(pid, decision, optionId) {
    const body = optionId ? { optionId } : { decision };
    return fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/permissions/${encodeURIComponent(pid)}`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })
      .then(async (r) => {
        if (r.ok) { this._error = ''; this._refetch(); return true; }
        this._error = (await r.json().catch(() => ({}))).error || `could not answer (${r.status})`;
        return false;
      }, (e) => { this._error = String(e.message || e); return false; });
  }

  // "keep planning" with feedback: reject the plan, then send the text as the
  // next prompt once the turn the reject ends has settled (Claude ends it as
  // cancelled; the prompt route refuses while a turn runs)
  _rejectPlan(pid, optionId, feedback = '') {
    const text = String(feedback || '').trim();
    if (text) this._followUp = { text, after: this._tx.lastSeq };
    return this._permit(pid, null, optionId);
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
  // decline (skip) → Promise<boolean>. (What a form requires is the form's
  // to say: chat/asks.js <bx-chat-form>.)
  _answer(eid, action, content) {
    return fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/elicitations/${encodeURIComponent(eid)}`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(action === 'accept' ? { action, content: content || {} } : { action }) })
      .then(async (r) => {
        if (r.ok) { this._error = ''; this._refetch(); return true; }
        this._error = (await r.json().catch(() => ({}))).error || `could not answer (${r.status})`;
        return false;
      }, (e) => { this._error = String(e.message || e); return false; });
  }

  _setOption(id, value) {
    if (!this.session) return;
    fetch(`/api/xbin/term/sessions/${encodeURIComponent(this.session)}/options`,
      { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ id, value }) })
      .then(async (r) => {
        if (!r.ok) { this._error = (await r.json().catch(() => ({}))).error || `could not set ${id}`; return; }
        this._error = ''; this._refetch();
        // the next session of this harness starts the same way (D229)
        agentPrefs().rememberOption(this._provRow(), id === 'mode' ? { id } : this._options().find((o) => o.id === id) || { id }, String(value));
      });
  }

  // the agent's slash commands: the last status that carried them
  _commands() { return this._tx.st.commands || []; }

  // the agent's session settings (model, effort, …): the last status event's
  // options list, as the agent reported it
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
    // the kit says the agent is signed out; how to sign in is this element's (ChatCtx.signin)
    this._signinFill = () => html`<button class="bxc-primary" @click=${() => this._doSignIn(lg)} title="open a terminal that runs the sign-in command in this agent's home">Sign in to ${lg.provider}</button>
      ${spec ? html`<button @click=${() => { this._signinTerm = false; }} title="sign in here: a link to open and a code to paste">Guided sign-in</button>` : nothing}`;
    return signinView(block('signin', 'signin', { provider: lg.provider || '', command: lg.command || '' }), this._ctx);
  }

  // ---- what the views may ask of this element (ChatCtx, /vendor/chat/blocks.js) ----

  /** chatCtx: the ChatCtx this element hands the chat kit's views. */
  get chatCtx() { return this._ctx; }

  _makeCtx() {
    const a = this;
    return {
      get running() { return !a.ended && a._status() === 'running'; },
      get ended() { return !!a.ended; },
      get readonly() { return !!a.history; },
      get agentName() { return a._provName(); },
      role: 'owner', // whoever may open the tab may answer (the session API decides: creator or admin)
      sessionRules: true, // "always" is xbind's own session rule: offered only when the request can be scoped to one
      feedbackHint: 'Tell the agent what to change (optional — sent as your next message)',
      isOpen: (b, slot, dflt) => !!dflt || a._opened.has(b.k + ':' + slot),
      setOpen: (b, slot, open) => { if (open) a._openSlot(b, slot); }, // (a fold the reader closed keeps its body: opening it again is instant)
      rev: (b) => a._ui.get(b.k) || 0,
      state: (b) => a._bag(b),
      // an option's answer; a plan kept with a word: the word goes in as the next message
      permit: (b, ans) => (b.plan && ans.verdict === 'reject' ? a._rejectPlan(b.pid, ans.optionId, ans.feedback) : a._permit(b.pid, ans.decision || null, ans.optionId)),
      answer: (b, ans) => a._answer(b.eid, ans.action, ans.content),
      // an option that stops the agent asking is confirmed in the window's own dialog (bx-frame's), else the browser's
      confirm: async (text) => (a.ask
        ? (await a.ask({ title: 'Let the agent stop asking?', message: text, buttons: [{ label: 'Cancel', value: null }, { label: 'Allow it', value: 'ok', primary: true }] }))?.button === 'ok'
        : window.confirm(text)),
      fileDiff: (b, set, c) => a._fileDiff(b, set, c),
      signin: () => (a._signinFill ? a._signinFill() : null),
      repaint: () => a.requestUpdate(),
    };
  }

  // what a card's widget keeps for a block (a form's values, a busy answer): by key, the newest few
  _bag(b) {
    let bag = this._bags.get(b.k);
    if (!bag) {
      if (this._bags.size > 64) this._bags.delete(this._bags.keys().next().value);
      this._bags.set(b.k, (bag = {}));
    }
    return bag;
  }

  // open a lazy body (or a "Show all") of b's, keeping the reader's view
  _openSlot(b, slot) {
    this._opened.add(b.k + ':' + slot);
    this._win.keepView(); // the reader opened it: keep their view, don't chase the bottom
    this._touch(b);
  }
  // b's row (and the rows it is nested in) render again: what is kept here for it changed
  _touch(b) {
    for (let x = b; x; x = x.$up) this._ui.set(x.k, (this._ui.get(x.k) || 0) + 1);
    this.requestUpdate();
  }
  // one file's diff of a change set (agent-diffs.js): the event's own patch, else the route while the session runs.
  // b: the block the change set hangs on — a turn's `changes`, or the call that made it
  _fileDiff(b, set, c) {
    const sel = b.kind === 'changes' ? { turn: b.turn } : { toolCallId: b.rec.id };
    return this._diffs.get({ session: this.session, live: !!this.session && !this.history && !this.ended }, { key: b.k, block: b }, set, c, sel);
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
    const blocks = this._blocks(), tx = this._tx, win = this._win;
    const from = win.start, end = win.end; // willUpdate placed the window
    const rows = (from || end < blocks.length ? blocks.slice(from, end) : blocks).map(blockOf); // the kit's blocks of the rows in the window
    const queue = this.ended || this.history ? [] : tx.st.queue || [];
    const pill = win.pill();
    return html`
      <div class="scroll">
        ${tx.truncated && !from ? html`<div class="bxc-gap">… earlier events dropped (log limit)</div>` : nothing}
        ${from || tx.hasOlder ? html`<div class="bxc-gap bxc-earlier">… ${from ? `${from}${tx.hasOlder ? '+' : ''} ` : ''}earlier ${from === 1 && !tx.hasOlder ? 'entry' : 'entries'}
          <button @click=${() => this._older()}>load earlier</button><button @click=${() => this._loadAll()}>load all</button></div>` : nothing}
        ${this.restarting ? html`<div class="hint">Restarting the agent in a new sandbox — the conversation resumes where the agent can reopen it…</div>` : nothing}
        ${!this.session && !this.provider && !this.history && !this.restarting && !blocks.length ? html`<div class="hint">Start a coding agent in this tile's sandbox. Pick a provider, then send a message.</div>` : nothing}
        ${!this.session && this.provider && !this.history && !lg ? html`<div class="hint">Starting ${this._provName()}…</div>` : nothing}
        ${rowsView(rows, this._ctx)}
        ${end >= blocks.length && !tx.hasNewer ? activityView(rows, this._ctx) : nothing}
      </div>
      <div class="foot">
        ${pill ? html`<button class="bxc-pill" @click=${() => this._jumpLatest()}><bx-icon name="chevron-down"></bx-icon>${pill}</button>` : nothing}
        ${this.ended ? html`<div class="status ended"><span class="dot exited"></span>
          <span>${this.history ? 'a past session — read-only' : 'this session has ended — the transcript stays until you close the tab'}</span>
          ${this.history && this._historyMeta?.loadable ? html`<button @click=${this._doResume} title="reopen this conversation: the agent replays these turns, then continues">Resume</button>` : nothing}
          ${this.history && this._historyMeta && !this._historyMeta.loadable ? html`<button @click=${this._doNewHere} title="this agent cannot reopen a session — start a fresh one on this tile">Start a new session here</button>` : nothing}
        </div>` : nothing}
        <div class="status">
          <span class="dot ${status}"></span>
          <span>${this.history ? 'past session' : this.session ? (status === 'waiting_permission' ? 'waiting for you' : status.replace('_', ' ')) : 'not started'}</span>
          ${this._curMode() && !this._options().length ? html`<span>· ${this._curMode()}</span>` : nothing}
          ${tx.st.usage ? html`<span>· ${usageView(tx.st.usage)}</span>` : nothing}
          ${this._followUp ? html`<span title=${this._followUp.text}>· your feedback goes in when the turn ends</span>` : nothing}
          ${this._error || this._statusDetail() ? html`<span class="err">${this._error || this._statusDetail()}</span>` : nothing}
        </div>
        ${lg ? this._signin(lg) : this._signedNote ? html`<div class="status signed"><bx-icon name="ok"></bx-icon>${this._signedNote}</div>` : nothing}
        <bx-chat-composer .draft=${this._draft} .fit=${this} .commands=${this._commands()} .queue=${queue}
          .pickers=${this._pickers()} .unavailable=${this.session || this.provider || this.restarting ? [] : this._notOffered.map((p) => ({ id: p.id, name: p.name, reason: p.unavailable }))}
          ?disabled=${this.ended} ?busy=${busy} ?sending=${this._creating || this.restarting} ?attach=${!this.ended && !this.history}
          placeholder=${this.ended ? 'the session has ended' : busy ? 'Add to the running turn (Enter) · Ctrl+Enter stops it, then sends' : 'Message the agent (Enter to send, Shift+Enter for a newline)'}
          sendLabel=${this.session ? 'Send' : 'Start'}
          sendTitle=${!this.session ? 'start the agent — with a message it sends it too; without one you can pick the model first' : busy ? 'add it to the running turn (Enter): the agent takes it at its next step, or it waits for the turn to end · Ctrl+Enter: stop the turn, then send it' : 'send (Enter)'}
          stopTitle="interrupt the running turn — messages waiting for it come back here first"
          @bx-chat-draft=${(e) => { this._draft = e.detail.text; }}
          @bx-chat-send=${(e) => this._submit(e.detail.interrupt, e.detail.files, e.detail.sent)}
          @bx-chat-stop=${() => stop(this)}
          @bx-chat-take-back=${(e) => takeBack(this, e.detail.id)}
          @bx-chat-pick=${(e) => this._picked(e.detail.id, e.detail.value)}></bx-chat-composer>
      </div>`;
  }

  // The pickers over the message box (the composer draws them). Before a
  // session: the provider and the mode it starts in. With one: a select per
  // setting the agent advertised (model, effort, mode, …), live — changing
  // one calls set_config_option for the next turn.
  _pickers() {
    if (!this.session) {
      if (this.provider || this.restarting) return [];
      const ps = this._providers || [];
      const prov = ps.find((p) => p.id === this._provider);
      return [
        { id: 'provider', title: 'provider', value: this._provider, options: ps.length ? ps.map((p) => ({ value: p.id, name: p.name })) : [{ value: '', name: 'loading…' }] },
        prov?.modes?.length ? { id: 'start-mode', title: 'mode', value: this._curMode(), options: prov.modes.map((m) => ({ value: m.id, name: `${m.name}${m.explicit ? ' (caution)' : ''}` })) } : null,
      ];
    }
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
      return !this.ended && (st === 'starting' || st === 'running') ? [{ id: 'starting', note: 'starting the agent…' }] : [];
    }
    return [
      modes.length ? { id: 'mode', label: 'mode', title: 'permission mode', value: this._curMode(),
        options: modes.map((m) => ({ value: m.id, name: `${m.name || m.id}${m.explicit ? ' (caution)' : ''}`, title: m.description || '' })) } : null,
      ...opts.map((o) => ({ id: o.id, label: o.name, title: o.description || o.name, value: o.currentValue,
        options: o.options.map((v) => ({ value: v.value, name: v.name || v.value, title: v.description || '' })) })),
    ];
  }

  _picked(id, value) {
    if (id === 'provider') {
      this._provider = value;
      this._mode = (this._providers || []).find((x) => x.id === value)?.defaultMode || '';
      this._modePicked = false;
    } else if (id === 'start-mode') { this._mode = value; this._modePicked = true; } else this._setOption(id, value);
  }

  _sc() { return this.renderRoot?.querySelector('.scroll'); }

  // Test hook (the harness): the transcript as plain records, and the actions.
  testApi() { return agentTestApi(this); }
}

const PAGE = 30; // rows the window grows by: the initial tail, and each step up or down
const TRIM = 120; // a window longer than this (and 6 views past the view) is trimmed on that side
const UNLOAD = 3; // pages about this many views beyond the rendered rows are let go

customElements.define('bx-agent', BxAgent);
