// model/session.js — the chat's state: the selected run, the views it shows
// (the run and any subagent whose card is open), the model calls in flight,
// and the run list — all kept current by ONE live stream (stream.js), never
// polled. No lit, no DOM: shown() is what a view draws (chat-view.js adds the
// web's template(); a native view draws the same blocks).
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { fold, activity, busy } from './fold.js';
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
   *                       (API.md "Paging the view"; loadOlder() reads the next older one).
   *                       Off by default: the web reads whole views and full drafts.
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
    this.loading = new Set();
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
    this.ui.act.approve = (id, yes) => this.approve(id, yes);
    this.pending = false;
  }

  // --- loading ------------------------------------------------------------

  // start opens the stream; the conversation list (conv-list.js) loads itself.
  async start() {
    this.live.follow(null, '');
  }

  async select(id) {
    this.sel = id;
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
  // held, older pages included (a reset or resync starts over from the newest).
  async fetchView(id, { paged = !!this.pageSize && id === this.sel } = {}) {
    const v = await api(`/runs/${id}/view${paged ? `?limit=${this.pageSize}` : ''}`);
    v.messages = v.messages || [];
    v.steps = v.steps || [];
    v.links = v.links || [];
    v.queued = v.queued || [];
    v.paged = paged;
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
      const p = await api(`/runs/${id}/view?limit=${this.pageSize}&before=${v.nextBefore}`);
      if (this.views.get(id) !== v) return; // re-read meanwhile: that page starts over
      for (const m of p.messages || []) upsert(v.messages, m);
      for (const s of p.steps || []) upsert(v.steps, s);
      for (const l of p.links || []) upsert(v.links, l);
      v.messageFiles = { ...(p.messageFiles || {}), ...(v.messageFiles || {}) };
      v.hasOlder = !!p.hasOlder;
      v.nextBefore = p.nextBefore;
    } finally {
      this.loading.delete(key);
      this.changed();
    }
  }

  loadChild(id) {
    if (!id || this.views.has(id) || this.loading.has(id)) return;
    this.loading.add(id);
    this.fetchView(id, { paged: false }).catch(() => {}).finally(() => { this.loading.delete(id); this.changed(); });
  }

  // reload re-reads every view shown (the stream said it cannot replay).
  reload() {
    this.on.reset?.();
    for (const id of [...this.views.keys()]) this.fetchView(id).then(() => this.changed()).catch(() => {});
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
      case 'message':
        if (v) upsert(v.messages, d);
        break;
      case 'step':
        if (v && !v.steps.some((s) => s.id === d.id)) v.steps.push(d);
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
        if (v) this.fetchView(ev.run).then(() => this.changed()).catch(() => {});
        break;
    }
    this.changed();
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
    if (this.pending) return;
    this.pending = true;
    (this.on.frame || nextFrame)(() => { this.pending = false; this.on.change?.(); });
  }

  // --- what the page draws ------------------------------------------------------

  // merged is a view with its call in flight attached.
  merged(id) {
    const v = this.views.get(id);
    if (!v) return null;
    return { ...v, run: { ...v.run, ...(this.runs.get(id) || {}) }, draft: this.drafts.get(id) || null };
  }

  current() { return this.sel == null ? null : this.merged(this.sel); }

  // shown is what the chat of the selected run shows: its run, the breadcrumb
  // chain (a subagent's parents), the blocks (fold.js), the activity line, the
  // connection state, and whether compaction hid earlier turns.
  shown() {
    const v = this.current();
    if (!v) return { blocks: [], run: {} };
    const blocks = fold(v, (id) => this.merged(id));
    return {
      run: v.run, chain: v.chain, blocks, activity: activity(v, blocks), conn: this.conn,
      // a page leaves compacted messages out and counts them instead
      olderHidden: v.paged ? (v.compacted || 0) > 0 : v.messages.some((m) => m.compacted && m.role !== 'system'),
      hasOlder: !!v.hasOlder,
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

  async approve(runId, yes) {
    await api(`/runs/${runId}/approve`, jbody({ approve: yes }, 'POST'));
  }
}

function upsert(list, item) {
  const i = list.findIndex((x) => x.id === item.id);
  if (i >= 0) list[i] = { ...list[i], ...item };
  else list.push(item);
}

function draftOf(d) {
  const tools = {};
  for (const t of d.tools || []) tools[t.index] = t;
  return { text: d.text || '', thinking: d.thinking || '', thinkStart: d.thinkStart || 0, thinkEnd: d.thinkEnd || 0, tools };
}
