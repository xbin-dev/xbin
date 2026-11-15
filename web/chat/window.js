/**
 * chat/window.js — ChatWindow, a long transcript as a window of its rows
 * (/vendor/chat/window.js), over /vendor/scroll-window.js.
 *
 * A host holds some of a transcript's blocks (the pages it loaded) and
 * renders a window of those — grown where the reader is heading, trimmed on
 * the side far from the view, following the bottom while the reader is
 * there. Nothing the reader looks at moves: ScrollWindow measures right
 * before a render and corrects right after it, in the same frame. This
 * class is the bookkeeping both of xbind's chats had around that (the Agent
 * tab's, the agent template's): which rows to render, when to render or
 * load more, when to let far pages go, the "↓ N new — jump to latest" pill.
 *
 * The update protocol, every render:
 *     const { start, end } = win.place(blocks);   // right before (lit: willUpdate)
 *     …render blocks.slice(start, end) as the scroller's direct children, keyed (rowsView)…
 *     win.after(scroller);                        // right after (lit: updated)
 *
 * `source` is what the host loads from (duck-typed; only `version` is
 * required):
 *     version              a number that moves whenever the blocks changed
 *     hasOlder, hasNewer   more lies above / below what is held
 *     fresh                entries that arrived while the reader was away
 *     paged                false: nothing can be let go (default true)
 *     loadOlder(limit?), loadNewer(limit?)   → Promise<changed>
 *     latest()             bring the newest page back → Promise
 *     keep(lo, hi, dropTail)   let go of the pages outside block indices [lo, hi) → changed
 *     follow(atBottom)     the reader reached, or left, the bottom
 * `changed()` asks the host for a render.
 *
 * Options: keyOf(block) (default its k), page (rows the window grows by:
 * 30), trim (a window longer than this is trimmed: 120), unload (pages
 * about this many views beyond the rendered rows are let go: 3), rows (the
 * rows' selector for ScrollWindow), paused() (true: skip filling — a hidden
 * host), onUnload(lo, hi) (after pages were let go: the host drops what it
 * memoized outside [lo, hi)). Where the two chats differed, the Agent
 * tab's rule is the default and the template's an option: minMargin (never
 * keep fewer rows than this beyond the window: 0), growFirst (after growing
 * the window, let nothing go until that render landed: false),
 * holdWhileBusy (let nothing go while a page is being read: false), retrim
 * ('pinned': render again for a trim only while following the bottom;
 * 'any': whenever one is due).
 */
import { ScrollWindow } from '/vendor/scroll-window.js';

export class ChatWindow {
  constructor(opts = {}) {
    this.source = opts.source || { version: 0 };
    this.changed = opts.changed || (() => {});
    this.keyOf = opts.keyOf || ((b) => b.k);
    this.page = opts.page ?? 30;
    this.trim = opts.trim ?? 120;
    this.unloadViews = opts.unload ?? 3;
    this.minMargin = opts.minMargin ?? 0;
    this.growFirst = !!opts.growFirst;
    this.holdWhileBusy = !!opts.holdWhileBusy;
    this.retrim = opts.retrim || 'pinned';
    this.paused = opts.paused || (() => false);
    this.onUnload = opts.onUnload || null;
    this.sw = new ScrollWindow({ onScroll: () => this.scrolled(), onResize: () => this.fill(), ...(opts.rows ? { rows: opts.rows } : {}) });
    this.busy = null; // a page load in flight
    this.blocks = [];
    this.start = 0; // the window [start, end) of blocks, as of the last place()
    this.end = 0;
    this.drawn = -1; // the source's version the window was placed for
    this.pillAt = true;
    this.reset();
  }

  /** reset(): another transcript, or this one from its tail: the last page, followed. */
  reset() {
    this.fromKey = this.toKey = null; // the window's first and last rendered block (null: the last page, the end)
    this.all = false; // "load all": everything loaded and rendered (find)
  }

  detach() { this.sw.detach(); }

  get atBottom() { return this.sw.atBottom; }

  // the index of the block with key k (-1 none); a row's data-k is text, a host's keys may be numbers
  _at(k) {
    if (k == null) return -1;
    const blocks = this.blocks, keyOf = this.keyOf;
    if (blocks.length && typeof keyOf(blocks[0]) === 'number') { const n = Number(k); return blocks.findIndex((b) => keyOf(b) === n); }
    const s = String(k);
    return blocks.findIndex((b) => String(keyOf(b)) === s);
  }

  // ---- the update protocol ----

  /**
   * place(blocks) → {start, end}: the window of blocks to render, by key —
   * following the bottom it reaches the end; a long rendered part is
   * trimmed on the side far from the view (ScrollWindow: KEEP > FILL, no
   * ping-pong). Call right before the render.
   */
  place(blocks) {
    const n = blocks.length, sw = this.sw;
    this.blocks = blocks;
    const pinned = sw.atBottom && !sw.keepView;
    let end = pinned || this.toKey == null ? n : this._at(this.toKey) + 1;
    if (end <= 0) end = n;
    let start = this._at(this.fromKey);
    if (start < 0 || start >= end) start = Math.max(0, end - this.page);
    if (this.all) { start = 0; end = n; } else if (end - start > this.trim) {
      const a = this._at(sw.trimAbove());
      if (a > start && a < end) start = a;
      const b = pinned ? -1 : this._at(sw.trimBelow());
      if (b >= start && b + 1 < end) end = b + 1;
    }
    this.start = start;
    this.end = end;
    this.fromKey = blocks[start] ? this.keyOf(blocks[start]) : null;
    this.toKey = end < n ? this.keyOf(blocks[end - 1]) : null;
    this.pillAt = sw.atBottom;
    this.drawn = this.source.version; // what start/end index
    sw.before();
    return { start, end };
  }

  /** after(scroller): right after the render — keep the reader's view, then see whether more should render or load. */
  after(sc) {
    const sw = this.sw;
    if (!sc) return;
    sw.attach(sc);
    const pinned = sw.atBottom && !sw.keepView;
    sw.after();
    // place() measured the view before this render's rows landed: a burst
    // folded into one frame would stay rendered whole until the next change,
    // so measure again with it in place
    if (this.end - this.start > this.trim && (this.retrim === 'any' ? this._trimDue(pinned) : pinned && sw.trimAbove() != null)) this.changed();
    this.fill();
  }

  // the window, as rendered now, would be trimmed on a side
  _trimDue(pinned) {
    const a = this._at(this.sw.trimAbove());
    if (a > this.start && a < this.end) return true;
    const b = pinned ? -1 : this._at(this.sw.trimBelow());
    return b >= this.start && b + 1 < this.end;
  }

  // ---- the reader ----

  /** scrolled(): the reader scrolled (a frame later; a touch scroll once settled). */
  scrolled() {
    const at = this.sw.atBottom;
    this.source.follow?.(at);
    if (at !== this.pillAt) this.changed(); // the pill
    this.fill();
  }

  /**
   * fill(): grow the window where the reader is heading — rows already
   * held, else a page (older above; below, one let go earlier, or the tail
   * back) — and let go of what is far away.
   */
  fill() {
    const sw = this.sw, src = this.source, blocks = this.blocks;
    if (this.all || this.paused() || !sw.sc || src.version !== this.drawn) return; // a render is due: it fills after
    if (sw.wantsAbove()) {
      if (this.start > 0) { this.fromKey = this.keyOf(blocks[Math.max(0, this.start - this.page)]); this.changed(); if (this.growFirst) return; } else if (src.hasOlder && src.loadOlder) this.load(() => src.loadOlder());
    } else if (sw.wantsBelow()) {
      const e = Math.min(blocks.length, this.end + this.page);
      if (this.end < blocks.length) { this.toKey = e < blocks.length ? this.keyOf(blocks[e - 1]) : null; this.changed(); if (this.growFirst) return; } else if (src.hasNewer && src.loadNewer) this.load(() => src.loadNewer());
    }
    this.unload();
  }

  /** unload(): pages about `unload` views beyond the rendered rows go (the live tail too, while the reader is away from the bottom). */
  unload() {
    const src = this.source;
    if (src.paged === false || this.all || (this.holdWhileBusy && this.busy)) return;
    const m = Math.max(this.minMargin, Math.ceil(this.sw.rowsPerView() * this.unloadViews));
    if (src.keep && src.keep(this.start - m, this.end + m, !this.sw.atBottom)) this.changed();
    if (this.onUnload) this.onUnload(this.start - m, this.end + m);
  }

  /** load(fn): one page load at a time; its rows render when it lands. */
  load(fn) {
    if (this.busy) return;
    this.busy = Promise.resolve().then(fn).then((changed) => { if (changed) this.changed(); }).catch(() => { }).finally(() => { this.busy = null; });
  }

  /** older(): "load earlier" — a page of rows above the window, loaded first if need be. */
  older() {
    const src = this.source;
    if (this.start > 0) { this.fromKey = this.keyOf(this.blocks[Math.max(0, this.start - this.page)]); this.changed(); } else if (src.hasOlder && src.loadOlder) this.load(() => src.loadOlder());
  }

  /** latest(): "↓ N new — jump to latest" — the tail (fetched again when it was let go), followed. */
  async latest() {
    const src = this.source;
    this.all = false;
    if (src.hasNewer && src.latest) {
      try { await src.latest(); } catch { /* the pill stays */ }
    }
    this.fromKey = this.toKey = null;
    this.sw.toBottom();
    src.follow?.(true);
    this.changed();
  }

  /** loadAll(): every page, rendered — an explicit mode (find), left by latest(); nothing unloads meanwhile. */
  async loadAll(limit = 2000) {
    const src = this.source;
    this.all = true;
    this.changed();
    try {
      while (this.all && src.hasOlder && src.loadOlder) await src.loadOlder(limit);
      while (this.all && src.hasNewer && src.loadNewer) await src.loadNewer(limit);
    } catch { /* what loaded shows */ }
    this.changed();
  }

  /** keepView(): the reader opened or closed something: keep their view, even at the bottom. */
  keepView() { this.sw.keepView = true; }

  /** toBottom(): go to the end and follow it. */
  toBottom() { this.sw.toBottom(); }

  /**
   * pill(): what the "jump to latest" pill says — '' while the reader is at
   * the bottom or nothing is below: new entries, or rows or pages not shown.
   */
  pill() {
    const src = this.source;
    if (this.sw.atBottom || this.all || !(src.fresh || src.hasNewer || this.end < this.blocks.length)) return '';
    return `${src.fresh ? `${src.fresh} new — ` : ''}jump to latest`;
  }
}
