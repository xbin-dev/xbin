// conv-list.js — the conversation list's state (D83): your conversations from
// GET /conversations, paged, kept current by the one live stream (run rows,
// your own pin/archive/read state, revocations) — never polled.
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { byActivity, isUnread } from './conv-groups.js';

const CHAT = new Set(['', 'chat', 'api']); // the origins that are conversations

export class ConvList {
  /**
   * @param {object} on  {change(), epoch() → the unread floor (GET /me)}
   */
  constructor(on) {
    this.on = on;
    this.pinned = [];
    this.items = [];
    this.next = '';
    this.loading = false;
    this.scope = 'mine';     // mine | team
    this.archived = false;
    this.q = '';
    this.results = null;     // search hits, while searching
    this.seq = 0;            // the latest load; older answers are dropped
  }

  params(extra = {}) {
    const p = new URLSearchParams({ scope: this.scope, ...extra });
    if (this.archived) p.set('archived', '1');
    return p.toString();
  }

  changed() { this.on.change?.(); }

  async load() {
    const my = ++this.seq;
    this.loading = true;
    try {
      const p = await api(`/conversations?${this.params()}`);
      if (my !== this.seq) return;
      this.pinned = (p.pinned || []).map((r) => this.fix(r));
      this.items = (p.items || []).map((r) => this.fix(r));
      this.next = p.next || '';
    } finally {
      if (my === this.seq) this.loading = false;
      this.changed();
    }
  }

  async more() {
    if (!this.next || this.loading) return;
    const my = this.seq;
    this.loading = true;
    try {
      const p = await api(`/conversations?${this.params({ cursor: this.next })}`);
      if (my !== this.seq) return;
      const have = new Set(this.items.map((r) => r.id));
      this.items.push(...(p.items || []).filter((r) => !have.has(r.id)).map((r) => this.fix(r)));
      this.next = p.next || '';
    } finally {
      this.loading = false;
      this.changed();
    }
  }

  async search(q) {
    this.q = (q || '').trim();
    const my = ++this.seq;
    if (!this.q) { this.results = null; this.changed(); return; }
    const p = await api(`/conversations?q=${encodeURIComponent(this.q)}`);
    if (my === this.seq && this.q) { this.results = (p.items || []).map((r) => this.fix(r)); this.changed(); }
  }

  view(scope, archived) {
    this.scope = scope;
    this.archived = archived;
    this.results = null;
    this.q = '';
    this.load();
  }

  fix(r) { r.unread = isUnread(r, this.on.epoch?.()); return r; }

  find(id) { return this.pinned.find((r) => r.id === id) || this.items.find((r) => r.id === id); }
  all() { return [...this.pinned, ...this.items]; }

  remove(id) {
    this.pinned = this.pinned.filter((r) => r.id !== id);
    this.items = this.items.filter((r) => r.id !== id);
    if (this.results) this.results = this.results.filter((r) => r.id !== id);
  }

  // belongs: would a conversation first seen on the stream be in this view?
  belongs(d) {
    if (this.archived || !CHAT.has(d.origin ?? '')) return false;
    return this.scope === 'team' ? !d.mine && d.visibility === 'team' && d.owner !== '' : d.mine || d.owner === '' || d.access === 'system';
  }

  // apply takes a stream event (chat-view.js hands every one over).
  apply(ev) {
    const d = ev.data || {};
    if (ev.type === 'revoked') { this.remove(d.id ?? ev.run); this.changed(); return; }
    if (ev.type === 'ustate') {
      const r = this.find(d.id);
      if (!r) { if (d.pinnedAt) this.load(); return; }
      const moved = !!r.pinnedAt !== !!d.pinnedAt || !!r.archivedAt !== !!d.archivedAt;
      Object.assign(r, { pinnedAt: d.pinnedAt, archivedAt: d.archivedAt, readMs: d.readMs });
      this.fix(r);
      if (moved) this.reshelve(r);
      this.changed();
      return;
    }
    if (ev.type !== 'run' || ev.run !== ev.root) return;
    if (d.deleted) { this.remove(ev.run); this.changed(); return; }
    const r = this.find(ev.run);
    if (r) {
      Object.assign(r, d);
      this.fix(r);
      this.items.sort(byActivity);
    } else if (this.belongs(d)) {
      this.items.unshift(this.fix({ ...d }));
      this.items.sort(byActivity);
    } else return;
    this.changed();
  }

  // reshelve puts a row where its own state says: pinned, listed, or gone
  // (archived, while not looking at the archive).
  reshelve(r) {
    this.remove(r.id);
    if (!!r.archivedAt !== this.archived) return;
    if (r.pinnedAt && !this.archived) this.pinned = [r, ...this.pinned].sort((a, b) => b.pinnedAt - a.pinnedAt);
    else this.items = [...this.items, r].sort(byActivity);
  }

  async patch(id, body) {
    const r = await api(`/runs/${id}`, jbody(body, 'PATCH'));
    const cur = this.find(id);
    if (cur && r) {
      Object.assign(cur, r);
      this.fix(cur);
      this.reshelve(cur);
    }
    this.changed();
    return r;
  }

  // read: you have seen it (the selected conversation, while you look).
  async read(id) {
    const r = this.find(id);
    if (r && !r.unread) return;
    if (r) { r.readMs = Date.now(); r.unread = false; this.changed(); }
    await api(`/runs/${id}/read`, { method: 'POST' }).catch(() => {});
  }
}
