// model/conv-list.js — the conversation list's state (D83): your conversations
// from GET /conversations, paged, kept current by the one live stream (run
// rows, your own pin/archive/read state, revocations) — never polled.
// (conv-list.js at the tile's root re-exports it.) In a person's partition a
// view reads both homes (model/homes.js): Mine and the archive merge their
// own conversations with the shared ones they take part in; Shared is the
// shared space's.
import { jbody } from '/vendor/bx-kit.js';
import { homeOf, listHomes, splitRows, twoHomes } from './homes.js';
import { runApi as api, homeApi } from './home-api.js';
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
    this.scope = 'mine';     // mine | shared (both ways: yours and others') | team (older: others' team ones)
    this.archived = false;
    this.q = '';
    this.results = null;     // search hits, while searching
    this.seq = 0;            // the latest load; older answers are dropped
    this.cursors = {};       // home → its next page's cursor ('' = no more)
    this.lasts = {};         // home → the oldest row read from it
    this.held = [];          // rows read below the horizon (model/homes.js splitRows)
    this.failed = null;      // the shared space didn't answer (the view shows the rest)
    this.caught = 0;         // when catchUp last read the shared space
    const d = typeof document !== 'undefined' ? document : null;
    if (d && twoHomes()) { // a person's partition: catch up with the shared space when the page is looked at again
      const again = () => { if (!d.hidden) this.catchUp().catch(() => {}); };
      d.addEventListener('visibilitychange', again);
      globalThis.addEventListener?.('focus', again);
    }
  }

  params(extra = {}) {
    const p = new URLSearchParams({ scope: this.scope, ...extra });
    if (this.archived) p.set('archived', '1');
    return p.toString();
  }

  changed() { this.on.change?.(); }

  // page reads one page of a view from a home. With two homes the shared
  // space may fail alone: its rows are missing and failed says why.
  page(home, homes, qs) {
    return homeApi(home, `/conversations?${qs}`).catch((e) => {
      if (home === '' || homes.length === 1) throw e;
      this.failed = e;
      return {};
    });
  }

  // took keeps what pages from homes said (their cursors and oldest rows —
  // as read: a live event moving that row on doesn't move the horizon) and
  // returns their rows.
  took(homes, ps) {
    const rows = [];
    homes.forEach((h, i) => {
      const items = (ps[i].items || []).map((r) => this.fix(r));
      this.cursors[h] = ps[i].next || '';
      if (items.length) { const l = items[items.length - 1]; this.lasts[h] = { id: l.id, activityMs: l.activityMs }; }
      rows.push(...items);
    });
    return rows;
  }

  // place shows rows with what is held, newest first, down to the horizon —
  // each conversation once (the newest of its rows).
  place(rows) {
    const byId = new Map();
    for (const r of [...this.items, ...this.held, ...rows]) {
      const had = byId.get(r.id);
      if (!had || (r.activityMs || 0) > (had.activityMs || 0)) byId.set(r.id, r);
    }
    const all = [...byId.values()];
    const lasts = Object.keys(this.cursors).filter((h) => this.cursors[h]).map((h) => this.lasts[h]).filter(Boolean);
    const { shown, held } = splitRows(all, lasts, byActivity);
    this.items = shown;
    this.held = held;
    this.next = Object.values(this.cursors).find(Boolean) || '';
  }

  async load() {
    const my = ++this.seq;
    this.loading = true;
    try {
      const homes = listHomes(this.scope);
      this.failed = null;
      const ps = await Promise.all(homes.map((h) => this.page(h, homes, this.params())));
      if (my !== this.seq) return;
      this.cursors = {};
      this.lasts = {};
      this.items = [];
      this.held = [];
      this.pinned = ps.flatMap((p) => p.pinned || []).map((r) => this.fix(r));
      if (homes.length > 1) this.pinned.sort((a, b) => (b.pinnedAt || 0) - (a.pinnedAt || 0));
      this.place(this.took(homes, ps));
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
      const homes = Object.keys(this.cursors).filter((h) => this.cursors[h]);
      const ps = await Promise.all(homes.map((h) => this.page(h, homes, this.params({ cursor: this.cursors[h] }))));
      if (my !== this.seq) return;
      this.place(this.took(homes, ps));
    } finally {
      this.loading = false;
      this.changed();
    }
  }

  async search(q) {
    this.q = (q || '').trim();
    const my = ++this.seq;
    if (!this.q) { this.results = null; this.changed(); return; }
    const homes = listHomes('mine');
    const ps = await Promise.all(homes.map((h) => this.page(h, homes, `q=${encodeURIComponent(this.q)}`)));
    if (my === this.seq && this.q) { this.results = ps.flatMap((p) => p.items || []).map((r) => this.fix(r)); this.changed(); }
  }

  // wantsGlobal: should the shared space's stream keep this list current —
  // a person's partition showing the Shared view, or any shared row.
  wantsGlobal() {
    if (!twoHomes()) return false;
    return this.scope !== 'mine' || [...this.pinned, ...this.items, ...this.held].some((r) => homeOf(r.id) === 'global');
  }

  // catchUp: while Mine lists nothing shared the shared space's stream is
  // closed (wantsGlobal), so a conversation shared with the person since —
  // or one they joined elsewhere — arrives by no event. When the page shows
  // again or gains focus (at most every 15 s) the shared space's first page
  // is read again; anything there reloads the list, and the stream opens.
  async catchUp(now = Date.now()) {
    if (!twoHomes() || this.scope !== 'mine' || this.archived || this.q || this.loading || this.wantsGlobal()) return false;
    if (now - this.caught < 15000) return false;
    this.caught = now;
    const my = this.seq;
    const p = await homeApi('global', `/conversations?${this.params()}`).catch(() => null);
    if (!p || my !== this.seq || this.wantsGlobal() || !((p.pinned || []).length || (p.items || []).length)) return false;
    await this.load();
    return true;
  }

  view(scope, archived) {
    this.scope = scope;
    this.archived = archived;
    this.results = null;
    this.q = '';
    this.load();
  }

  fix(r) { r.unread = isUnread(r, this.on.epoch?.()); return r; }

  // find: a row this view holds — listed, or held below the horizon.
  find(id) { return this.pinned.find((r) => r.id === id) || this.items.find((r) => r.id === id) || this.held.find((r) => r.id === id); }
  all() { return [...this.pinned, ...this.items]; }

  remove(id) {
    this.pinned = this.pinned.filter((r) => r.id !== id);
    this.items = this.items.filter((r) => r.id !== id);
    this.held = this.held.filter((r) => r.id !== id);
    if (this.results) this.results = this.results.filter((r) => r.id !== id);
  }

  // belongs: would a conversation first seen on the stream be in this view?
  // (Shared with people only arrives by a reload: the stream's row has no
  // member count.)
  belongs(d) {
    if (this.archived || !CHAT.has(d.origin ?? '')) return false;
    if (this.scope === 'shared') return d.visibility === 'team' && d.owner !== '';
    if (this.scope === 'team') return !d.mine && d.visibility === 'team' && d.owner !== '';
    return d.mine || d.owner === '' || d.access === 'system';
  }

  // apply takes a stream event (the app hands every one over, model/app.js).
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
      if (this.held.includes(r)) this.place([]); // it may be above the horizon now
      else this.items.sort(byActivity);
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
    else if (twoHomes()) this.place([r]); // listed, or held below the horizon (two homes)
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
