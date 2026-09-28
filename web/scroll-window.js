/**
 * scroll-window.js — a long list that renders a window of its rows and never
 * moves what the reader is looking at (D124, shared since D130). Framework
 * free: the Agent tab (bx-agent) and the agent template's chat
 * (/vendor/scroll-window.js) render their own rows; this measures and
 * corrects around each render and says when to grow or trim the window.
 *
 *   const sw = new ScrollWindow({ onScroll: () => host.fill(), onResize: … });
 *   sw.attach(scroller);          // once it exists
 *   sw.before();                  // right before the DOM changes
 *   …render…
 *   sw.after();                   // right after — the same frame, before paint
 *
 * The scroller opts out of native scroll anchoring (`overflow-anchor: none`,
 * set here: Safari has none, and a native correction beside this one would
 * double-correct). While the reader is at the bottom, an update keeps the
 * bottom pinned; otherwise before() records the first visible row's top and
 * after() moves scrollTop by however far it moved — which covers rows added
 * or dropped above, a row above growing, a block spliced in. Rows are the
 * scroller's direct children matching `rows`, each carrying its key.
 *
 * Window policy, in views (the scroller's height): ask for more rows above
 * when the reader is within FILL views of the rendered top (or the rows do
 * not fill SHORT views), below when within FILL of the rendered bottom and
 * not at it; past TRIM_AT views of rendered rows beyond the view on either
 * side, keep KEEP of them. KEEP > FILL, so trimming and filling never
 * ping-pong. A touch scroll grows the window only once it settles (writing
 * scrollTop under iOS momentum stops it dead), unless the edge is close.
 */
export const FILL = 1.5, SHORT = 2, TRIM_AT = 6, KEEP = 2.5;

export class ScrollWindow {
  // opts: rows (a selector for the row elements, direct children of the
  // scroller), keyOf(el) (a row's key), slack (px from the bottom that is
  // still "at the bottom"), onScroll(sw) (a frame after a scroll, and when a
  // touch scroll settles), onResize(sw)
  constructor(opts = {}) {
    this.rows = opts.rows || ':scope > [data-k]';
    this.keyOf = opts.keyOf || ((el) => el.dataset.k);
    this.slack = opts.slack ?? 40;
    this.onScroll = opts.onScroll || null;
    this.onResize = opts.onResize || null;
    this.atBottom = true; // following the bottom (the reader is there)
    this.keepView = false; // the next update keeps the view even at the bottom (the reader opened something)
    this.sc = null;
    this._anchor = null;
    this._pinned = false;
    this._touch = false;
    this._idle = 0;
    this._raf = 0;
  }

  attach(sc) {
    if (!sc || this.sc === sc) return;
    this.detach();
    this.sc = sc;
    sc.style.overflowAnchor = 'none';
    this._onScroll = () => {
      this.atBottom = this.gapBelow() < this.slack;
      if (this._touch) { // a touch scroll (and its momentum) has settled after 150 ms without a scroll event
        clearTimeout(this._idle);
        this._idle = setTimeout(() => { this._idle = 0; this.onScroll?.(this); }, 150);
      }
      if (!this._raf) this._raf = requestAnimationFrame(() => { this._raf = 0; this.onScroll?.(this); });
    };
    this._onTouch = () => { this._touch = true; };
    sc.addEventListener('scroll', this._onScroll, { passive: true });
    sc.addEventListener('touchstart', this._onTouch, { passive: true }); // passive: never hold up the scroll it starts
    this._ro = new ResizeObserver(() => {
      if (this.atBottom && !this.keepView) sc.scrollTop = sc.scrollHeight; // a taller composer, a window drag: stay at the bottom
      this.onResize?.(this);
    });
    this._ro.observe(sc);
  }

  detach() {
    if (!this.sc) return;
    this.sc.removeEventListener('scroll', this._onScroll);
    this.sc.removeEventListener('touchstart', this._onTouch);
    this._ro?.disconnect();
    cancelAnimationFrame(this._raf);
    clearTimeout(this._idle);
    this._raf = this._idle = 0;
    this.sc = null;
  }

  // ---- the update protocol ----

  // before the DOM changes: pin the bottom, or remember the first visible row
  before() {
    this._pinned = this.atBottom && !this.keepView;
    this._anchor = this._pinned ? null : this.firstVisible();
  }

  // after it changed, before paint: the bottom stays pinned, or the row the
  // reader was looking at goes back where it was. Returns the correction.
  after() {
    const sc = this.sc;
    if (!sc) return 0;
    const a = this._anchor;
    this._anchor = null;
    let d = 0;
    if (this._pinned) sc.scrollTop = sc.scrollHeight;
    else if (a && a.el.isConnected) {
      d = a.el.getBoundingClientRect().top - a.top;
      if (Math.abs(d) >= 0.5) sc.scrollTop += d;
    }
    if (this.keepView) { this.keepView = false; this.atBottom = this.gapBelow() < this.slack; }
    return d;
  }

  toBottom() {
    if (this.sc) this.sc.scrollTop = this.sc.scrollHeight;
    this.atBottom = true;
  }

  // ---- geometry ----

  get height() { return this.sc ? this.sc.clientHeight : 0; }
  gapBelow() { const sc = this.sc; return sc ? sc.scrollHeight - sc.scrollTop - sc.clientHeight : 0; }
  rowEls() { return this.sc ? this.sc.querySelectorAll(this.rows) : []; }

  // the first row whose bottom is below content offset y (binary search)
  rowAt(y) {
    const sc = this.sc;
    if (!sc) return null;
    const top = sc.getBoundingClientRect().top - sc.scrollTop + y;
    const rows = this.rowEls();
    let lo = 0, hi = rows.length - 1, at = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (rows[mid].getBoundingClientRect().bottom > top) { at = mid; hi = mid - 1; } else lo = mid + 1;
    }
    return at < 0 ? null : rows[at];
  }

  // the first visible row and where it is
  firstVisible() {
    const el = this.height ? this.rowAt(this.sc.scrollTop) : null;
    return el ? { el, top: el.getBoundingClientRect().top } : null;
  }

  // rendered rows per view, for sizing what lies outside the DOM in rows
  rowsPerView() {
    const n = this.rowEls().length, sc = this.sc;
    return n && sc && sc.scrollHeight ? Math.max(1, (n * sc.clientHeight) / sc.scrollHeight) : 10;
  }

  // ---- the window policy ----

  // the key of the first row to keep, once more than TRIM_AT views of rows
  // are above the view (keeping KEEP); null when nothing should go
  trimAbove() {
    const h = this.height;
    if (!h || this.sc.scrollTop <= h * TRIM_AT) return null;
    const row = this.rowAt(this.sc.scrollTop - h * KEEP);
    return row ? this.keyOf(row) : null;
  }

  // the key of the last row to keep, once more than TRIM_AT views of rows
  // are below the view (keeping KEEP); null when nothing should go
  trimBelow() {
    const h = this.height;
    if (!h || this.atBottom || this.gapBelow() <= h * TRIM_AT) return null;
    const row = this.rowAt(this.sc.scrollTop + h * (1 + KEEP));
    return row ? this.keyOf(row) : null;
  }

  // more rows wanted above: near the rendered top, or too few to fill
  wantsAbove() {
    const h = this.height, sc = this.sc;
    if (!h) return false;
    const short = sc.scrollHeight < h * SHORT;
    if (!short && sc.scrollTop > h * FILL) return false;
    if (this._touch && this._idle && !short && sc.scrollTop > h * 0.5) return false; // the settle timer calls back
    return true;
  }

  // more rows wanted below: near the rendered bottom, and not following it
  wantsBelow() {
    const h = this.height;
    if (!h || this.atBottom) return false;
    if (this._touch && this._idle && this.gapBelow() > h * 0.5) return false;
    return this.gapBelow() < h * FILL;
  }
}
