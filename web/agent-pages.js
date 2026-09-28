/**
 * agent-pages.js — the part of an agent session's log a client holds (D130).
 *
 * The server pages the log (GET …/events?limit= the tail, ?before=<seq> the
 * page before it; docs/protocol.md §Agent session events → Pages), each page
 * cut where no card spans the cut. A Transcript holds a run of consecutive
 * SEGMENTS — each a page (or a piece split off the live tail at a cut the
 * server gave) folded on its own (agent-fold.js) — so it can load pages
 * above the reader, drop whole segments far from them in either direction,
 * and fetch them back, with the same blocks and keys every time.
 *
 * The last segment is the live tail while `detached` is false: live events
 * fold into it. Once it is dropped (the reader is far up), live events only
 * move the status digest and the `fresh` count, until loadNewer() fetches the
 * tail back or openTail() starts over from it. A late event (a seq below the
 * newest applied, never seen) refolds only the segment it belongs to.
 *
 * Against an xbind that does not page (it ignores the parameters and answers
 * the whole replay, without `hasOlder`), the transcript is one segment holding
 * everything, and nothing unloads. Pure (fetch is passed in): node-tested in
 * hack/agent-fold.test.mjs.
 */
import { Fold, foldStatus, newStatus } from './agent-fold.js';

export const PAGE_LIMIT = 200; // events per page asked for

// whether an event opens an entry (the "N new" count while the tail is not
// folded; a message run counts once)
const opens = (e, prev) => {
  switch (e.type) {
    case 'tool.call': case 'permission.request': case 'elicitation.request': case 'turn.end': return !(e.data || {}).parent;
    case 'message.delta': case 'thought.delta': {
      const d = e.data || {}, p = (prev && prev.data) || {};
      return !d.parent && (!prev || prev.type !== e.type || p.role !== d.role || p.messageId !== d.messageId);
    }
  }
  return false;
};

export class Transcript {
  // get(query) → the events route's JSON for a query string ('limit=200',
  // 'before=41&limit=200', 'since=40'); it throws on a failed request
  constructor(get) {
    this.get = get;
    this.reset();
  }

  reset() {
    this.segs = []; // {first: the seq it starts at, events, fold}, oldest first, consecutive
    this.st = newStatus(); // the session's status digest now (the tail's, and every live event's)
    this.statusSeq = 0; // the seq of the last status event with a status
    this.lastSeq = 0; // the newest seq applied (the follow cursor)
    this.seen = new Set();
    this.hasOlder = false; // the server holds events before the first segment
    this.truncated = false; // …and the ring dropped some before those
    this.paged = true; // the server pages (an older xbind answers everything)
    this.detached = false; // the live tail is not loaded
    this.below = []; // the first seqs of dropped segments after the last loaded one, ascending
    this.fresh = 0; // entries that arrived while the reader was away from the bottom
    this.follow = true; // the reader is at the bottom (the host says)
    this.meta = null; // a past session's meta (history)
    this.version = 0; // moves on every change the host renders
    this._blocks = null;
    this._last = null; // the last live event (for the entry count)
    return this;
  }

  // ---- loading ----

  // openTail: start over from the tail page — on open, on a replay, on
  // "jump to latest". An xbind that does not page answers the whole log.
  async openTail(limit = PAGE_LIMIT) {
    const r = await this.get(`limit=${limit}`);
    this.reset();
    this.meta = r.meta || null;
    const evs = r.events || [];
    if (!('hasOlder' in r)) { // an older xbind: the whole replay
      this.paged = false;
      this.truncated = !!r.truncated;
      this.segs = [this._seg(evs, null)];
      for (const e of evs) this._status(e);
    } else {
      this.hasOlder = !!r.hasOlder;
      this.truncated = !!r.truncated;
      const st = r.state || {};
      if (st.status) this._status({ seq: 0, type: 'status', data: st.status });
      if (st.usage) this.st.usage = st.usage;
      this.segs = [this._seg(evs, st, evs.length ? evs[0].seq : (r.next || 0) + 1)];
      for (const e of evs) this._status(e);
    }
    for (const e of evs) this.seen.add(e.seq);
    const next = typeof r.next === 'number' ? r.next : 0;
    this.lastSeq = Math.max(next, evs.length ? evs[evs.length - 1].seq : 0);
    return this._changed();
  }

  // loadOlder: the page before the first segment, prepended (false: nothing
  // older, or an xbind that does not page)
  async loadOlder(limit = PAGE_LIMIT) {
    if (!this.paged || !this.hasOlder || !this.segs.length) return false;
    const first = this.segs[0].first;
    const r = await this.get(`before=${first}&limit=${limit}`);
    if (!this.segs.length || this.segs[0].first !== first) return false; // reset meanwhile
    const evs = (r.events || []).filter((e) => e.seq < first && !this.seen.has(e.seq));
    this.hasOlder = !!r.hasOlder && evs.length > 0;
    this.truncated = !this.hasOlder && !!r.truncated;
    if (!evs.length) return this._changed();
    for (const e of evs) this.seen.add(e.seq);
    this.segs.unshift(this._seg(evs, null));
    return this._changed();
  }

  // loadNewer: fetch back the first dropped segment below the last loaded
  // one — a page ending where the next one starts, or, for the old tail,
  // everything after it (the tail is live again)
  async loadNewer(limit = PAGE_LIMIT) {
    if (!this.below.length || !this.segs.length) return false;
    const from = this.below[0];
    const until = this.below[1];
    const r = until ? await this.get(`before=${until}&limit=${Math.max(1, until - from)}`) : await this.get(`since=${from - 1}`);
    if (this.below[0] !== from) return false; // moved meanwhile
    const evs = (r.events || []).filter((e) => e.seq >= from && (!until || e.seq < until));
    const seg = this._seg(evs.length && evs[0].seq > from ? [{ type: 'gap', data: {} }, ...evs] : evs, null, from); // the ring lost some: say so
    for (const e of evs) this.seen.add(e.seq);
    this.segs.push(seg);
    this.below.shift();
    if (!until) {
      this.detached = false;
      this.below = [];
      const last = evs.length ? evs[evs.length - 1].seq : 0;
      if (last > this.lastSeq) this.lastSeq = last;
    }
    return this._changed();
  }

  // splitTail: the live tail grew past a few pages while followed — ask the
  // server where its tail page starts and split the segment there, so its
  // older part can unload like any page. The blocks move; none refolds.
  async splitTail(limit = PAGE_LIMIT) {
    const tail = this.segs[this.segs.length - 1];
    if (!this.paged || this.detached || !tail || tail.events.length <= 3 * limit) return false;
    const r = await this.get(`limit=${limit}`);
    const cut = r.nextBefore;
    if (this.segs[this.segs.length - 1] !== tail || !r.hasOlder || !(cut > tail.first) || !tail.events.some((e) => e.seq >= cut)) return false;
    const i = tail.events.findIndex((e) => e.seq >= cut);
    const before = (b) => b && b.key > 0 && b.key < cut;
    const old = new Fold();
    old.blocks = tail.fold.blocks.filter(before);
    tail.fold.blocks = tail.fold.blocks.filter((b) => !before(b));
    for (const m of [tail.fold.tools, tail.fold.byId, tail.fold.perms, tail.fold.asks]) for (const [k, b] of m) if (before(b)) m.delete(k);
    this.segs.splice(this.segs.length - 1, 0, { first: tail.first, events: tail.events.slice(0, i), fold: old, state: tail.state });
    tail.events = tail.events.slice(i);
    tail.first = cut;
    tail.state = r.state || null; // a refold of the tail starts from the cut's state
    return this._changed();
  }

  // ---- live events ----

  // apply: events from the live stream or a ?since= catch-up. New ones fold
  // into the tail (or, detached, only move the digest and the count); a
  // late one refolds its own segment. True when anything changed.
  apply(events, truncated) {
    let changed = false, late = null;
    const tail0 = this.segs[this.segs.length - 1];
    if (truncated && tail0 && !this.detached && events && events.length) tail0.fold.push({ type: 'gap', data: {} }); // the ring moved past our cursor
    for (const e of events || []) {
      if (typeof e.seq !== 'number' || this.seen.has(e.seq)) continue;
      if (e.seq > this.lastSeq) {
        this.seen.add(e.seq);
        this.lastSeq = e.seq;
        this._status(e);
        const tail = this.segs[this.segs.length - 1];
        if (this.detached || !tail) {
          if (opens(e, this._last)) this.fresh++;
        } else {
          const n = tail.fold.blocks.length;
          tail.events.push(e);
          tail.fold.push(e);
          if (!this.follow) this.fresh += tail.fold.blocks.length - n;
        }
        this._last = e;
        changed = true;
        continue;
      }
      const seg = this._segOf(e.seq);
      if (!seg) continue; // before or after what is loaded: a page will bring it
      this.seen.add(e.seq);
      let i = seg.events.length;
      while (i > 0 && seg.events[i - 1].seq > e.seq) i--;
      seg.events.splice(i, 0, e);
      (late ||= new Set()).add(seg);
      changed = true;
    }
    for (const seg of late || []) this._refold(seg);
    if (truncated && !this.paged) this.truncated = true;
    return changed ? !!this._changed() : false;
  }

  // the reader reached the bottom: nothing is new any more
  seenAll() { if (this.fresh) { this.fresh = 0; this._changed(); } }

  // ---- unloading ----

  // keep(lo, hi): drop the segments whose blocks all lie outside block
  // indices [lo, hi) of blocks(); the tail only while detached is allowed
  // (canDetach). Returns whether anything went.
  keep(lo, hi, canDetach) {
    if (!this.paged || this.segs.length < 2) return false;
    let i = 0, dropTop = 0, dropBottom = this.segs.length;
    const ends = this.segs.map((s) => (i += s.fold.blocks.length));
    while (dropTop < this.segs.length - 1 && ends[dropTop] <= lo) dropTop++;
    while (dropBottom - 1 > dropTop && ends[dropBottom - 2] >= hi) dropBottom--;
    if (dropBottom < this.segs.length && !canDetach && !this.detached) dropBottom = this.segs.length;
    if (!dropTop && dropBottom === this.segs.length) return false;
    const gone = [...this.segs.slice(0, dropTop), ...this.segs.slice(dropBottom)];
    if (dropBottom < this.segs.length) {
      const tailGoes = !this.detached;
      this.below = [...this.segs.slice(dropBottom).map((s) => s.first), ...this.below];
      if (tailGoes) this.detached = true;
    }
    if (dropTop) { this.hasOlder = true; this.truncated = false; }
    this.segs = this.segs.slice(dropTop, dropBottom);
    for (const s of gone) for (const e of s.events) this.seen.delete(e.seq);
    return !!this._changed();
  }

  // ---- reading ----

  // every loaded block, in order (cached until the next change)
  blocks() {
    if (!this._blocks) this._blocks = this.segs.length === 1 ? this.segs[0].fold.blocks : this.segs.flatMap((s) => s.fold.blocks);
    return this._blocks;
  }

  // whether data lies beyond the loaded blocks below them
  get hasNewer() { return this.detached || this.below.length > 0; }

  // the status after seq `after`, if a status event came since (the plan
  // feedback waits for the rejected turn to settle)
  statusAfter(after) { return this.statusSeq > after ? this.st.status : ''; }

  // ---- inside ----

  _seg(events, state, first) {
    const fold = new Fold();
    if (state) fold.seed(state);
    for (const e of events) fold.push(e);
    const f = first ?? (events.find((e) => typeof e.seq === 'number')?.seq || 0);
    return { first: f, events: events.filter((e) => typeof e.seq === 'number'), fold, state };
  }

  // the loaded segment a seq belongs to (null: before them, or in a
  // dropped one below)
  _segOf(seq) {
    const n = this.segs.length;
    for (let i = n - 1; i >= 0; i--) {
      const end = i < n - 1 ? this.segs[i + 1].first : this.below.length ? this.below[0] : Infinity;
      if (seq >= this.segs[i].first) return seq < end ? this.segs[i] : null;
    }
    return null;
  }

  // a segment folded again (a late event): same keys, new objects; the
  // tail's folding state goes with it
  _refold(seg) {
    const fold = new Fold();
    if (seg.state) fold.seed(seg.state);
    for (const e of seg.events) fold.push(e);
    seg.fold = fold;
  }

  _status(e) {
    foldStatus(this.st, e);
    if (e.type === 'status' && e.data && e.data.status && e.seq) this.statusSeq = e.seq;
  }

  _changed() { this._blocks = null; this.version++; return true; }
}
