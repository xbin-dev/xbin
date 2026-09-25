// chat-view.js — the chat's state: the selected run, the views it shows
// (the run and any subagent whose card is open), the model calls in flight,
// and the run list — all kept current by ONE live stream (stream.js), never
// polled. agent.js owns the page and calls template() to draw the chat.
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { fold, activity, busy } from './chat-fold.js';
import { sessionTpl } from './chat-cards.js';
import { Live } from './stream.js';

const cid = () => Math.random().toString(36).slice(2) + Date.now().toString(36);

export class Session {
  /**
   * @param {string} base  this backend's prefix (/api/<self>)
   * @param {object} on    {change(), runs(), gone(id), event(ev), reset()} — the page
   *                       repaints on change; the conversation list takes every event
   */
  constructor(base, on) {
    this.on = on;
    this.base = base;
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
    });
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

  async fetchView(id) {
    const v = await api(`/runs/${id}/view`);
    v.messages = v.messages || [];
    v.steps = v.steps || [];
    v.links = v.links || [];
    v.queued = v.queued || [];
    this.views.set(id, v);
    this.runs.set(id, { ...(this.runs.get(id) || {}), ...v.run });
    for (const d of v.drafts || []) this.drafts.set(d.run, draftOf(d));
    return v;
  }

  loadChild(id) {
    if (!id || this.views.has(id) || this.loading.has(id)) return;
    this.loading.add(id);
    this.fetchView(id).catch(() => {}).finally(() => { this.loading.delete(id); this.changed(); });
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

  changed() {
    if (this.pending) return;
    this.pending = true;
    requestAnimationFrame(() => { this.pending = false; this.on.change?.(); });
  }

  // --- what the page draws ------------------------------------------------------

  // merged is a view with its call in flight attached.
  merged(id) {
    const v = this.views.get(id);
    if (!v) return null;
    return { ...v, run: { ...v.run, ...(this.runs.get(id) || {}) }, draft: this.drafts.get(id) || null };
  }

  current() { return this.sel == null ? null : this.merged(this.sel); }

  template() {
    const v = this.current();
    if (!v) return sessionTpl({ blocks: [], run: {} }, this.ui);
    const blocks = fold(v, (id) => this.merged(id));
    return sessionTpl({
      run: v.run, chain: v.chain, blocks, activity: activity(v, blocks), conn: this.conn,
      olderHidden: v.messages.some((m) => m.compacted && m.role !== 'system'),
    }, this.ui);
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
