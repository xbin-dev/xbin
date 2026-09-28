// model/session.js — the chat's state: the selected run, the views it shows
// (the run and any subagent whose card is open), the model calls in flight,
// and the run list — all kept current by ONE live stream (stream.js), never
// polled. No lit, no DOM: shown() is what a view draws (chat-view.js adds the
// web's template(); a native view draws the same blocks).
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { fold, activity, busy, FoldCache } from './fold.js';
import { Live } from './stream.js';

const cid = () => Math.random().toString(36).slice(2) + Date.now().toString(36);
// nextFrame batches repaints: one per display frame in a browser, ~60 Hz
// where there is no requestAnimationFrame (a view may pass its own: on.frame).
const nextFrame = (f) => (typeof requestAnimationFrame === 'function' ? requestAnimationFrame(f) : setTimeout(f, 16));

export class Session {
  /**
   * @param {string} base  this backend's prefix (/api/<self>)
   * @param {object} on    {change(), runs(), gone(id), event(ev), reset(), frame?(fn)} — the
   *                       page repaints on change; the conversation list takes every event
   * @param {object} opts  {deltas, page}: stream drafts as deltas (API.md "Deltas"), and
   *                       read the open conversation's view in pages of `page` messages
   *                       (API.md "Paging the view"; loadOlder() reads the next older one,
   *                       keep() lets go of what lies far from the reader, loadNewer()
   *                       reads it back). Off by default (whole views, full drafts);
   *                       both of this tile's views turn them on.
   */
  constructor(base, on, opts = {}) {
    this.on = on;
    this.base = base;
    this.deltas = !!opts.deltas;
    this.pageSize = Math.max(0, Math.floor(Number(opts.page) || 0));
    this.thumbs = new Map(); // "run:path" → object URL ('' while loading)
    this.sel = null;
    this.views = new Map();  // run id → view (GET /runs/{id}/view, kept current)
    this.drafts = new Map(); // run id → the model call in flight
    this.runs = new Map();   // run id → summary (the run list, plus what events told us)
    this.open = new Map();   // ui id → explicitly opened/closed
    this.folds = new Map();  // run id → its FoldCache (fold.js): blocks rebuilt only when they change
    this.loading = new Set();
    this.version = 0;        // moves on every change: a view drawing a window knows it is stale
    this.following = true;   // the reader is at the open conversation's end (the view says: follow())
    this.conn = 'live';
    this.live = new Live(base, {
      event: (ev) => this.apply(ev),
      reset: () => this.reload(),
      state: (s) => { if (s !== this.conn) { this.conn = s; this.changed(); } },
    }, { deltas: this.deltas });
    this.ui = {
      isOpen: (id, dflt) => (this.open.has(id) ? this.open.get(id) : dflt),
      toggle: (id, dflt) => { this.open.set(id, !this.ui.isOpen(id, dflt)); this.changed(); },
      act: {}, // filled by the page: select, approve, openFile, loadChild
    };
    this.ui.act.loadChild = (id) => this.loadChild(id);
    this.ui.file = (msgId, f) => this.fileState(msgId, f);
    // a refused verdict (the ask is gone: 409) is said by the card, never thrown at the page
    this.ui.act.approve = (id, yes, grant, park) => this.approve(id, yes, grant, park).catch((e) => this.noteApprove(id, e));
    this.approveNotes = new Map(); // run id → why its last verdict was refused ({text, timer})
    this.ui.approveNote = (id) => this.approveNotes.get(id)?.text || '';
    this.ui.who = () => null; // the page's GET /me (the app sets it): who may allow a grant
    this.pending = false;
  }

  // --- loading ------------------------------------------------------------

  // start opens the stream; the conversation list (conv-list.js) loads itself.
  async start() {
    this.live.follow(null, '');
  }

  async select(id) {
    this.sel = id;
    this.following = true;
    if (id == null) {
      this.live.follow(null, '');
      this.changed();
      return;
    }
    let v;
    try { v = await this.fetchView(id); } catch (e) { this.sel = null; throw e; }
    if (this.sel !== id) return;
    this.live.follow(id, v.cursor);
    this.changed();
  }

  // fetchView reads a run's view (paged: its newest page — the open
  // conversation's, when the session pages). A new read replaces what was
  // held, older pages included (opening a conversation, "jump to latest");
  // a reset or resync re-reads what is held instead (reread).
  async fetchView(id, { paged = !!this.pageSize && id === this.sel } = {}) {
    const v = await api(`/runs/${id}/view${paged ? `?limit=${this.pageSize}` : ''}`);
    v.messages = v.messages || [];
    v.steps = v.steps || [];
    v.links = v.links || [];
    v.queued = v.queued || [];
    v.paged = paged;
    v.below = [];       // pages let go below what is held ({from, n}: first message seq, shown messages), oldest first
    v.detached = false; // the live tail is among them: live messages are counted (fresh), not held
    v.fresh = 0;
    this.views.set(id, v);
    this.runs.set(id, { ...(this.runs.get(id) || {}), ...v.run });
    // With deltas the stream drives a live draft: a view read meanwhile must
    // not rewind it (a delta that no longer fits would force a reconnect).
    for (const d of v.drafts || []) if (!(this.deltas && this.drafts.has(d.run))) this.drafts.set(d.run, draftOf(d));
    return v;
  }

  // loadOlder reads the page before the oldest one held (the transcript's
  // "more") and merges it in: messages, steps and links upsert by id.
  async loadOlder(id = this.sel) {
    const v = this.views.get(id);
    const key = 'older:' + id;
    if (!v || !v.hasOlder || !this.pageSize || this.loading.has(key)) return;
    this.loading.add(key);
    try {
      const before = v.nextBefore;
      const p = await api(`/runs/${id}/view?limit=${this.pageSize}&before=${before}`);
      if (this.views.get(id) !== v || v.nextBefore !== before) return; // re-read, or let go, meanwhile
      merge(v, [p]);
      v.hasOlder = !!p.hasOlder;
      v.nextBefore = p.nextBefore;
    } finally {
      this.loading.delete(key);
      this.changed();
    }
  }

  // loadNewer reads back the first page let go below what is held (keep()):
  // a page ending where the next one starts — or, for the old live tail,
  // everything from where it started to the newest, and live messages are
  // held again. The run's messages and steps that arrive meanwhile wait and
  // apply after. True when a page came back.
  async loadNewer(id = this.sel) {
    const v = this.views.get(id);
    const key = 'newer:' + id;
    if (!v || !v.below || !v.below.length || this.loading.has(key)) return false;
    this.loading.add(key);
    v.held = [];
    const [c, next] = v.below;
    try {
      const pages = next ? [await api(`/runs/${id}/view?limit=${c.n}&before=${next.from}`)] : await this.pagesDownTo(id, null, c.from);
      if (this.views.get(id) !== v || v.below[0] !== c) return false;
      merge(v, pages, (m) => m.seq >= c.from && (!next || m.seq < next.from));
      v.below.shift();
      if (!next) { v.detached = false; v.fresh = 0; }
      return true;
    } finally {
      const held = v.held || [];
      v.held = null;
      for (const ev of held) this.take(v, ev);
      this.loading.delete(key);
      this.changed();
    }
  }

  // pagesDownTo reads pages newest first (before `top`, or from the newest)
  // until one reaches down to message seq `to`, or the first message: what
  // a re-read of the held range, or of the old live tail, needs. At most 40.
  async pagesDownTo(id, top, to) {
    const pages = [];
    let before = top;
    for (let i = 0; i < 40; i++) {
      const p = await api(`/runs/${id}/view?limit=${this.pageSize}${before != null ? `&before=${before}` : ''}`);
      pages.push(p);
      const ms = p.messages || [];
      if (!p.hasOlder || !ms.length || ms[0].seq <= to) break;
      before = p.nextBefore;
    }
    return pages;
  }

  // reread reads a held view again (a reset, a resync, an edit the stream
  // does not carry) and keeps what the reader has: a paged view reads the
  // pages it holds — from its newest (or from where the pages let go below
  // start) down to its oldest message — so the rows on screen stay, with
  // their keys; a whole view is read whole.
  async reread(id) {
    const v = this.views.get(id);
    if (!v || !v.paged || !this.pageSize) return this.fetchView(id, { paged: false });
    const held = v.messages.filter(shown);
    const to = v.hasOlder && held.length ? held[0].seq : -1;
    const top = v.detached && v.below.length ? v.below[0].from : null;
    const pages = await this.pagesDownTo(id, top, to);
    if (this.views.get(id) !== v) return v;
    const nv = { ...pages[0], messages: [], steps: [], links: [], messageFiles: {} };
    nv.queued = nv.queued || [];
    merge(nv, pages);
    const last = pages[pages.length - 1];
    Object.assign(nv, { paged: true, hasOlder: !!last.hasOlder, nextBefore: last.nextBefore, below: v.below, detached: v.detached, fresh: v.fresh });
    if (to >= 0) cutOlder(nv, to); // the last page reached below what was held
    this.views.set(id, nv);
    this.runs.set(id, { ...(this.runs.get(id) || {}), ...nv.run });
    for (const d of nv.drafts || []) if (!(this.deltas && this.drafts.has(d.run))) this.drafts.set(d.run, draftOf(d));
    return nv;
  }

  // refresh re-reads a view's state — memory, files, config, the run —
  // after an edit the stream does not carry, keeping its transcript (a paged
  // view reads one message's page for it).
  async refresh(id = this.sel) {
    const v = this.views.get(id);
    if (!v) return;
    if (!v.paged) { await this.fetchView(id, { paged: false }); return; }
    const p = await api(`/runs/${id}/view?limit=1`);
    if (this.views.get(id) !== v) return;
    for (const [k, x] of Object.entries(p)) if (!TRANSCRIPT.has(k)) v[k] = x;
    v.queued = v.queued || [];
    this.runs.set(id, { ...(this.runs.get(id) || {}), ...v.run });
    this.changed();
  }

  // latest: the newest page again, as when the conversation opened (the
  // "jump to latest" pill): whatever was held goes.
  async latest(id = this.sel) {
    if (id == null) return;
    await this.fetchView(id);
    this.changed();
  }

  // --- a window of the transcript ----------------------------------------------
  //
  // A view that draws a window of the open conversation's blocks lets go of
  // the messages far from it and reads them back as the reader nears them
  // (API.md "Paging the view": the union of the pages held folds as the
  // whole view does). Cuts fall at a message — never between a message's
  // blocks, nor between a call and its result — and only a page or more
  // goes at a time, so what goes is what a page read brings back.

  // keep(lo, hi, canDetach): let go of the messages whose blocks lie outside
  // block indices [lo, hi) of blocks(id) — older ones when a page or more of
  // them lies above lo; newer ones only when canDetach (the reader is away
  // from the bottom): the live tail goes with them, and live messages are
  // then counted (v.fresh) until loadNewer() or latest() brings it back.
  // True when anything went.
  keep(lo, hi, canDetach, id = this.sel) {
    const v = this.views.get(id);
    if (!v || !v.paged || !this.pageSize) return false;
    const blocks = this.blocks(id) || [];
    const seqOf = blockSeqs(v);
    let went = false;
    if (lo > 0 && lo < blocks.length) {
      // at a message block at or above lo (a step above it goes with what is above)
      let i = lo;
      while (i > 0 && seqOf.get(blocks[i].id) == null) i--;
      const s = seqOf.get(blocks[i].id);
      if (i > 0 && s != null && count(v, (m) => m.seq < s) >= this.pageSize) went = this.dropOlder(id, s);
    }
    if (canDetach && hi > 0 && hi < blocks.length) {
      // at the first message below hi none of whose blocks is above it
      let top = -Infinity, s = null;
      for (let i = 0; i < hi; i++) { const q = seqOf.get(blocks[i].id); if (q != null && q > top) top = q; }
      for (let i = hi; i < blocks.length && s == null; i++) { const q = seqOf.get(blocks[i].id); if (q != null && q > top) s = q; }
      if (s != null && count(v, (m) => m.seq >= s) >= this.pageSize) went = this.dropNewer(id, s) || went;
    }
    return went;
  }

  // dropOlder lets go of the messages before message seq `seq` and of the
  // steps before its time — what the page read with before=<seq> brings
  // back (loadOlder). A cut at a call's result is refused.
  dropOlder(id = this.sel, seq) {
    const v = this.views.get(id);
    if (!v || !v.paged || !cutOlder(v, seq)) return false;
    this.changed();
    return true;
  }

  // dropNewer lets go of the messages from message seq `seq` on, the live
  // tail with them, noted in v.below as pages to read back (loadNewer), and
  // of the steps from its time on. A cut at a call's result is refused.
  dropNewer(id = this.sel, seq) {
    const v = this.views.get(id);
    if (!v || !v.paged || !this.pageSize) return false;
    const cut = v.messages.find((m) => m.seq >= seq && shown(m));
    if (!cut || cut.role === 'tool') return false;
    const pages = [];
    let cur = null;
    for (const m of v.messages) {
      if (m.seq < cut.seq || !shown(m)) continue;
      if (!cur || (cur.n >= this.pageSize && m.role !== 'tool')) pages.push(cur = { from: m.seq, n: 0 });
      cur.n++;
    }
    v.messages = v.messages.filter((m) => m.seq < cut.seq);
    v.steps = v.steps.filter((s) => s.created < cut.created);
    v.messageFiles = filesOf(v.messageFiles, v.messages);
    v.below = [...pages, ...(v.below || [])];
    v.detached = true;
    this.changed();
    return true;
  }

  loadChild(id) {
    if (!id || this.views.has(id) || this.loading.has(id)) return;
    this.loading.add(id);
    this.fetchView(id, { paged: false }).catch(() => {}).finally(() => { this.loading.delete(id); this.changed(); });
  }

  // reload re-reads every view shown (the stream said it cannot replay),
  // keeping what each holds — the reader's place stays (reread).
  reload() {
    this.on.reset?.();
    for (const id of [...this.views.keys()]) this.reread(id).then(() => this.changed()).catch(() => {});
  }

  // --- events --------------------------------------------------------------

  apply(ev) {
    const d = ev.data || {};
    const v = this.views.get(ev.run);
    this.on.event?.(ev);
    switch (ev.type) {
      case 'run':
        if (d.deleted) {
          this.runs.delete(ev.run);
          this.views.delete(ev.run);
          if (this.sel === ev.run) this.on.gone?.(ev.run);
          this.on.runs?.();
          break;
        }
        this.runs.set(ev.run, { ...(this.runs.get(ev.run) || {}), ...d });
        if (v) v.run = { ...v.run, ...d };
        if (!d.parentId) this.on.runs?.();
        break;
      case 'message': case 'step':
        if (v) this.take(v, ev);
        break;
      case 'inbox':
        if (v) v.queued = d.queued || [];
        break;
      case 'link': {
        const pv = this.views.get(d.parentId);
        if (pv) upsert(pv.links, d);
        if (d.child) this.runs.set(d.childId, { ...(this.runs.get(d.childId) || {}), ...d.child });
        const cv = this.views.get(d.childId);
        if (cv && d.child) cv.run = { ...cv.run, ...d.child };
        break;
      }
      case 'text': case 'thinking': case 'tool':
        this.draft(ev);
        break;
      case 'text.delta': case 'thinking.delta': case 'tool.delta':
        this.delta(ev);
        break;
      case 'draft.end':
        this.drafts.delete(ev.run);
        break;
      case 'resync':
        if (v) this.reread(ev.run).then(() => this.changed()).catch(() => {});
        break;
    }
    this.changed();
  }

  // follow: the reader is at the open conversation's end (true), or away
  // from it — then new messages are counted in v.fresh (the view's "N new")
  // until they come back to it.
  follow(at) {
    at = !!at;
    if (at === this.following) return;
    this.following = at;
    const v = at && this.views.get(this.sel);
    if (v && v.fresh && !v.detached) { v.fresh = 0; this.changed(); }
  }

  // take holds a message or step event in its view. A paged view holds a
  // run of consecutive pages: a message older than the oldest held is left
  // for its page to bring; while the live tail is let go (detached), new
  // messages and steps are only counted — and while it is being read back
  // (loadNewer), they wait for it.
  take(v, ev) {
    const d = ev.data || {};
    if (v.held && v.detached) { v.held.push(ev); return; }
    if (ev.type === 'step') {
      if (v.detached || v.steps.some((s) => s.id === d.id)) return;
      v.steps.push(d);
      return;
    }
    const i = v.messages.findIndex((m) => m.id === d.id);
    if (i >= 0) { v.messages[i] = { ...v.messages[i], ...d }; return; }
    if (v.paged && v.hasOlder && v.messages.length && d.seq < v.messages[0].seq) return;
    const counted = shown(d) && d.role !== 'tool';
    if (v.detached) {
      if (counted) v.fresh++;
      return;
    }
    if (counted && !this.following && v.paged) v.fresh = (v.fresh || 0) + 1;
    if (v.messages.length && d.seq < v.messages[v.messages.length - 1].seq) {
      v.messages.push(d);
      v.messages.sort(bySeq);
    } else v.messages.push(d);
  }

  draft(ev) {
    const d = this.drafts.get(ev.run) || { text: '', thinking: '', tools: {}, thinkStart: 0, thinkEnd: 0 };
    const x = ev.data || {};
    if (ev.type === 'thinking') {
      d.thinking = x.text || '';
      if (!d.thinkStart) d.thinkStart = x.started || ev.ts;
    } else {
      if (d.thinkStart && !d.thinkEnd && (x.text || ev.type === 'tool')) d.thinkEnd = ev.ts;
      if (ev.type === 'text') d.text = x.text || '';
      else d.tools[x.index] = { index: x.index, id: x.id, name: x.name, args: x.args };
    }
    this.drafts.set(ev.run, d);
  }

  // delta appends what a draft added (API.md "Deltas"): `at` is the length
  // the text (a tool call's arguments) had before it. A delta that does not
  // fit what is held (a view replaced the draft meanwhile, an event was
  // coalesced away) reconnects the stream, which then sends every live draft
  // in full.
  delta(ev) {
    const x = ev.data || {};
    if (ev.type === 'tool.delta') {
      const d = this.drafts.get(ev.run);
      const t = d && d.tools[x.index];
      if (!t || (t.args || '').length !== x.at) { this.live.resync(); return; }
      d.tools[x.index] = { ...t, args: (t.args || '') + (x.delta || '') };
      if (d.thinkStart && !d.thinkEnd) d.thinkEnd = ev.ts;
      return;
    }
    const field = ev.type === 'text.delta' ? 'text' : 'thinking';
    let d = this.drafts.get(ev.run);
    if (!d && x.at === 0) d = { text: '', thinking: '', tools: {}, thinkStart: 0, thinkEnd: 0 };
    if (!d || d[field].length !== x.at) { this.live.resync(); return; }
    d[field] += x.delta || '';
    if (field === 'thinking') { if (!d.thinkStart) d.thinkStart = ev.ts; }
    else if (d.thinkStart && !d.thinkEnd && x.delta) d.thinkEnd = ev.ts;
    this.drafts.set(ev.run, d);
  }

  changed() {
    this.version++;
    if (this.pending) return;
    this.pending = true;
    (this.on.frame || nextFrame)(() => { this.pending = false; this.on.change?.(); });
  }

  // --- what the page draws ------------------------------------------------------

  // merged is a view with its call in flight attached (not while the live
  // tail is let go: the draft belongs after it, not after what is held).
  merged(id) {
    const v = this.views.get(id);
    if (!v) return null;
    return { ...v, run: { ...v.run, ...(this.runs.get(id) || {}) }, draft: (!v.detached && this.drafts.get(id)) || null };
  }

  current() { return this.sel == null ? null : this.merged(this.sel); }

  // blocks is a held run's transcript as blocks (fold.js), cached per block:
  // what did not change since the last paint is the same objects. The few
  // runs folded lately (the open one, a subagent's parents) keep a cache each.
  blocks(id, v = this.merged(id)) {
    if (!v) return null;
    const c = this.folds.get(id) || new FoldCache();
    this.folds.delete(id); // most recently used last
    this.folds.set(id, c);
    if (this.folds.size > 8) this.folds.delete(this.folds.keys().next().value);
    return fold(v, (x) => this.merged(x), 0, c);
  }

  // shown is what the chat of the selected run shows: its run, the breadcrumb
  // chain (a subagent's parents), the blocks (fold.js), the activity line, the
  // connection state, whether compaction hid earlier turns, and what is not
  // held: older pages (hasOlder), pages let go below (hasNewer), the live
  // tail among them (detached) and what arrived meanwhile (fresh).
  shown() {
    const v = this.current();
    if (!v) return { blocks: [], run: {} };
    const blocks = this.blocks(this.sel, v);
    return {
      run: v.run, chain: v.chain, blocks, activity: v.detached ? '' : activity(v, blocks), conn: this.conn,
      // a page leaves compacted messages out and counts them instead
      olderHidden: v.paged ? (v.compacted || 0) > 0 : v.messages.some((m) => m.compacted && m.role !== 'system'),
      hasOlder: !!v.hasOlder,
      hasNewer: !!(v.below && v.below.length), detached: !!v.detached, fresh: v.fresh || 0,
    };
  }

  queued() { const v = this.current(); return v ? v.queued : []; }
  busy() { const v = this.current(); return !!(v && busy(v.run.status)); }

  // fileState says whether a sent message's attachment still exists (the
  // message_files link) and, for an image, its thumbnail — fetched once from
  // the raw route as an object URL (a sandboxed frame has no other way to
  // authenticate an <img>), then kept.
  fileState(msgId, f) {
    const v = this.current();
    const linked = !!(v && ((v.messageFiles || {})[msgId] || []).includes(f.path));
    if (!linked || !/^image\/(png|jpeg|gif|webp)$/.test(f.mime)) return { linked, thumb: '' };
    const key = `${v.run.id}:${f.path}`;
    if (!this.thumbs.has(key)) {
      this.thumbs.set(key, '');
      xbin.fetch(`${this.base}/runs/${v.run.id}/raw?path=${encodeURIComponent(f.path)}`)
        .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(r.status))))
        .then((b) => { this.thumbs.set(key, URL.createObjectURL(b)); this.changed(); })
        .catch(() => {});
    }
    return { linked, thumb: this.thumbs.get(key) };
  }

  // --- actions ---------------------------------------------------------------------

  // send posts a message. While the run works it is queued (and shown above
  // the composer) until the agent's next step; a retried post is deduplicated
  // by its client id.
  async send(text, files) {
    const v = this.current();
    if (!v) throw new Error('no run selected');
    const r = await api(`/runs/${v.run.id}/message`, jbody({ text, files, clientId: cid() }, 'POST'));
    return r;
  }

  // stop interrupts the run; queued messages come back for the composer.
  async stop() {
    const v = this.current();
    if (!v) return [];
    const r = await api(`/runs/${v.run.id}/interrupt`, { method: 'POST' });
    return (r && r.returned) || [];
  }

  async removeQueued(iid) {
    if (this.sel == null) return;
    const id = this.sel;
    await api(`/runs/${id}/inbox/${iid}`, { method: 'DELETE' });
    const v = this.views.get(id); // the stored view, not current()'s merged copy
    if (v) v.queued = v.queued.filter((q) => q.id !== iid);
    this.changed();
  }

  // approve answers a parked approval; grant ('once' | 'hour') is how long
  // the owner allows a grant it asks for (D111). park names the ask it
  // answers (pendingState.park; else the one the run's view shows): if the
  // agent has moved on to another ask, the server refuses (409) rather than
  // spend the click on that one.
  async approve(runId, yes, grant, park) {
    this.clearApproveNote(runId);
    park = park || this.views.get(runId)?.run?.pendingState?.park;
    const body = { approve: yes };
    if (grant) body.grant = grant;
    if (park) body.park = park;
    await api(`/runs/${runId}/approve`, jbody(body, 'POST'));
  }

  // noteApprove keeps why a verdict was refused — typically a 409: the ask
  // it answered is gone (answered by someone else, or the agent moved on) —
  // for the view to say beside the card for a few seconds; the run event
  // that follows redraws the card itself.
  noteApprove(runId, e) {
    this.clearApproveNote(runId, false);
    const text = String((e && e.message) || e || 'the verdict was not sent');
    const timer = setTimeout(() => this.clearApproveNote(runId), 8000);
    this.approveNotes.set(runId, { text: text.charAt(0).toUpperCase() + text.slice(1), timer });
    this.changed();
  }

  clearApproveNote(runId, repaint = true) {
    const n = this.approveNotes.get(runId);
    if (!n) return;
    clearTimeout(n.timer);
    this.approveNotes.delete(runId);
    if (repaint) this.changed();
  }

  // revokeGrant takes a grant back before it expires; the run event that
  // follows clears it from the view.
  async revokeGrant(runId, cap) {
    await api(`/runs/${runId}/grants/${encodeURIComponent(cap)}`, { method: 'DELETE' });
  }
}

function upsert(list, item) {
  const i = list.findIndex((x) => x.id === item.id);
  if (i >= 0) list[i] = { ...list[i], ...item };
  else list.push(item);
}

// What a page holds of the transcript; the rest of a view is the run's state.
const TRANSCRIPT = new Set(['messages', 'steps', 'links', 'messageFiles', 'hasOlder', 'nextBefore', 'compacted', 'linkCount',
  'paged', 'below', 'detached', 'fresh', 'held']);
// shown: a message a page may hold (API.md "Paging the view").
const shown = (m) => m.role !== 'system' && !m.compacted;
const bySeq = (a, b) => a.seq - b.seq || a.id - b.id;
const count = (v, f) => v.messages.reduce((n, m) => n + (shown(m) && f(m) ? 1 : 0), 0);

// merge folds pages into a view: messages (those `keep` passes) and links
// upsert by id, steps are added once, message files join; messages stay in
// seq order.
function merge(v, pages, keep = () => true) {
  for (const p of pages) {
    for (const m of p.messages || []) if (keep(m)) upsert(v.messages, m);
    for (const s of p.steps || []) if (!v.steps.some((x) => x.id === s.id)) v.steps.push(s);
    for (const l of p.links || []) upsert(v.links, l);
    v.messageFiles = { ...(p.messageFiles || {}), ...(v.messageFiles || {}) };
  }
  for (let i = 1; i < v.messages.length; i++) if (bySeq(v.messages[i - 1], v.messages[i]) > 0) { v.messages.sort(bySeq); break; }
}

// cutOlder: v without its messages before the shown message at or after
// seq, and the steps before that one's time (a cut at a call's result is
// refused: false).
function cutOlder(v, seq) {
  const first = v.messages.find((m) => m.seq >= seq && shown(m));
  if (!first || first.role === 'tool' || !v.messages.some((m) => m.seq < first.seq)) return false;
  v.messages = v.messages.filter((m) => m.seq >= first.seq);
  v.steps = v.steps.filter((s) => s.created >= first.created);
  v.messageFiles = filesOf(v.messageFiles, v.messages);
  v.hasOlder = true;
  v.nextBefore = first.seq;
  return true;
}

// filesOf: the message_files entries of the messages held.
function filesOf(files, msgs) {
  const out = {};
  for (const m of msgs) if (files && files[m.id]) out[m.id] = files[m.id];
  return out;
}

// blockSeqs maps the ids of a view's message blocks (fold.js: m<id> the
// text, r<id> the reasoning, c<call> a call and its result) to their
// message's seq; a step's block has none.
function blockSeqs(v) {
  const out = new Map();
  for (const m of v.messages) {
    out.set('m' + m.id, m.seq);
    out.set('r' + m.id, m.seq);
    for (const c of m.toolCalls || []) out.set('c' + c.id, m.seq);
  }
  return out;
}

function draftOf(d) {
  const tools = {};
  for (const t of d.tools || []) tools[t.index] = t;
  return { text: d.text || '', thinking: d.thinking || '', thinkStart: d.thinkStart || 0, thinkEnd: d.thinkEnd || 0, tools };
}
