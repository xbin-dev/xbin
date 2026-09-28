// chat-window.js — the open conversation as a window of its blocks (D130).
//
// The session holds a run of consecutive pages of the transcript
// (model/session.js: the newest when the chat opens, older ones read as the
// reader nears the top, pages far from the reader let go in either
// direction); of the blocks those fold into, the timeline renders a window,
// grown and trimmed around the view. Nothing the reader looks at moves: every
// render keeps the first visible row where it was, or follows the bottom
// (xbind's /vendor/scroll-window.js measures right before and corrects right
// after, in the same frame). While the live tail is let go (the reader is far
// up), the pill says how much is new and brings it back.
//
// An xbind from before D130 serves no scroll-window.js: then every held
// block renders, the chat follows its bottom as it always did, older pages
// load from the "load earlier" line, and nothing is let go.

let ScrollWindow = null;
try { ({ ScrollWindow } = await import('/vendor/scroll-window.js')); } catch { /* an older xbind: no window */ }

const PAGE = 40;   // rows the window grows by (and the tail it opens on)
const TRIM = 160;  // a rendered window longer than this is trimmed on the side far from the view
const UNLOAD = 3;  // messages about this many views beyond the rendered rows are let go

export class ChatWindow {
  // session: the Session (model/session.js); paint(): repaint the chat
  constructor(session) {
    this.session = session;
    this.sw = ScrollWindow ? new ScrollWindow({ onScroll: () => this.scrolled(), onResize: () => this.fill() }) : null;
    this.busy = null; // a page read in flight
    this.reset();
  }

  // a conversation opened: its tail, followed
  reset() {
    this.fromKey = this.toKey = null; // the window's first and last rendered block (null: the tail, the end)
    this.start = this.end = 0;
    this.blocks = [];
    this.s = {};
    this.drawn = -1; // the session version the window was placed for
    this.pill = '';
    this.pillAt = true;
    this.followed = true; // the fallback's "at the bottom"
    this.pin = false;     // …and its next render goes to the bottom
  }

  attach(sc) { this.sc = sc; this.sw?.attach(sc); }
  detach() { this.sc = null; this.sw?.detach(); }

  get atBottom() { return this.sw ? this.sw.atBottom : this.followed; }

  // ---- the update protocol: place() right before the render, after() right after ----

  // place picks the window [start, end) of s.blocks, by key: following the
  // bottom it reaches the end; a long rendered part is trimmed on the side
  // far from the view (scroll-window.js; KEEP > FILL: no ping-pong). Returns
  // what sessionTpl draws.
  place(s) {
    const blocks = s.blocks, n = blocks.length, sw = this.sw;
    this.blocks = blocks;
    this.s = s;
    this.drawn = this.session.version;
    if (!sw) {
      const sc = this.sc;
      this.followed = this.pin || !sc || sc.scrollHeight - sc.scrollTop - sc.clientHeight < 40;
      this.pin = false;
      this.start = 0;
      this.end = n;
      return this.tpl(s);
    }
    const at = (k) => (k == null ? -1 : blocks.findIndex((b) => b.id === k));
    const pinned = sw.atBottom && !sw.keepView;
    let end = pinned || this.toKey == null ? n : at(this.toKey) + 1;
    if (end <= 0) end = n;
    let start = at(this.fromKey);
    if (start < 0 || start >= end) start = Math.max(0, end - PAGE);
    if (end - start > TRIM) {
      const a = at(sw.trimAbove());
      if (a > start && a < end) start = a;
      const b = pinned ? -1 : at(sw.trimBelow());
      if (b >= start && b + 1 < end) end = b + 1;
    }
    this.start = start;
    this.end = end;
    this.fromKey = blocks[start]?.id ?? null;
    this.toKey = end < n ? blocks[end - 1].id : null;
    this.pillAt = sw.atBottom;
    sw.before();
    return this.tpl(s);
  }

  tpl(s) {
    const n = s.blocks.length;
    const away = this.sw ? !this.sw.atBottom : false;
    this.pill = away && (s.fresh || s.hasNewer || this.end < n) ? `↓ ${s.fresh ? `${s.fresh} new — ` : ''}jump to latest` : '';
    return { start: this.start, end: this.end, pill: this.pill, older: () => this.older(), latest: () => this.latest() };
  }

  after() {
    const sw = this.sw;
    if (!sw) {
      if (this.followed && this.sc) this.sc.scrollTop = this.sc.scrollHeight;
      return;
    }
    const pinned = sw.atBottom && !sw.keepView;
    sw.after();
    // place() measured before this render's rows landed: a burst folded into
    // one frame, or a page of rows grown into, would stay rendered whole
    // until the next change — render again when a trim is due
    if (this.end - this.start > TRIM && this.trimDue(pinned)) this.session.changed();
    this.fill();
  }

  // trimDue: the window, as rendered now, would be trimmed on a side
  trimDue(pinned) {
    const sw = this.sw, blocks = this.blocks;
    const at = (k) => (k == null ? -1 : blocks.findIndex((b) => b.id === k));
    const a = at(sw.trimAbove());
    if (a > this.start && a < this.end) return true;
    const b = pinned ? -1 : at(sw.trimBelow());
    return b >= this.start && b + 1 < this.end;
  }

  // ---- the reader ----

  // the reader scrolled (a frame later; a touch scroll once it settled)
  scrolled() {
    this.session.follow(this.sw.atBottom); // away from the end, what arrives is counted
    if (this.sw.atBottom !== this.pillAt) this.session.changed(); // the pill
    this.fill();
  }

  // grow the window where the reader is heading — rows already held, else a
  // page (older above; below, one let go earlier or the live tail back) —
  // and let go of what is far away
  fill() {
    const sw = this.sw, s = this.s, blocks = this.blocks;
    if (!sw || !sw.sc || !sw.height || this.session.version !== this.drawn) return; // a render is due: it fills after
    if (sw.wantsAbove()) {
      // (grown, the window renders first: what goes is measured from the new one)
      if (this.start > 0) { this.fromKey = blocks[Math.max(0, this.start - PAGE)].id; this.session.changed(); return; }
      if (s.hasOlder) this.load(() => this.session.loadOlder());
    } else if (sw.wantsBelow()) {
      const e = Math.min(blocks.length, this.end + PAGE);
      if (this.end < blocks.length) { this.toKey = e < blocks.length ? blocks[e - 1].id : null; this.session.changed(); return; }
      if (s.hasNewer) this.load(() => this.session.loadNewer());
    }
    this.unload();
  }

  // messages about UNLOAD views beyond the rendered rows go — never fewer
  // than two steps of the window's growth — the live tail too while the
  // reader is away from the bottom; never while a page is being read
  unload() {
    if (this.busy) return;
    const m = Math.max(2 * PAGE, Math.ceil(this.sw.rowsPerView() * UNLOAD));
    this.session.keep(this.start - m, this.end + m, !this.sw.atBottom);
  }

  // one page read at a time; its rows render when it lands
  load(fn) {
    if (this.busy) return;
    this.busy = Promise.resolve().then(fn).catch(() => {}).finally(() => { this.busy = null; this.session.changed(); });
  }

  // "load earlier": a page of rows above the window, read first if need be
  older() {
    if (this.start > 0) { this.fromKey = this.blocks[Math.max(0, this.start - PAGE)].id; this.session.changed(); return; }
    if (this.sw) this.sw.keepView = true;
    this.load(() => this.session.loadOlder());
  }

  // "↓ N new — jump to latest": the newest page (read again when the tail
  // was let go), followed
  async latest() {
    if (this.s.hasNewer || this.s.detached) {
      await this.busy;
      const p = this.busy = this.session.latest().catch(() => { /* the pill stays */ }).finally(() => { if (this.busy === p) this.busy = null; });
      await p;
    }
    this.fromKey = this.toKey = null;
    this.toBottom();
    this.session.changed();
  }

  // the reader opened or closed something: keep their view, even at the bottom
  keepView() { if (this.sw) this.sw.keepView = true; }

  toBottom() {
    this.followed = this.pin = true;
    this.session.follow(true);
    if (this.sw) this.sw.toBottom();
    else if (this.sc) this.sc.scrollTop = this.sc.scrollHeight;
  }

  // testApi: what the UI harness reads (hack/ui-harness: agentTemplateLong)
  // — the window, what the session holds, the scroller — and late(key), a
  // late event for the message of block `key`: its text grows.
  testApi() {
    const s = this.s, sc = this.sc, session = this.session, v = session.views.get(session.sel);
    return {
      windowed: !!this.sw, start: this.start, end: this.end, total: (this.blocks || []).length,
      rendered: sc ? sc.querySelectorAll(':scope > [data-k]').length : 0,
      held: v ? v.messages.length : 0, hasOlder: !!s.hasOlder, hasNewer: !!s.hasNewer, detached: !!s.detached, fresh: s.fresh || 0,
      pill: this.pill, atBottom: this.atBottom,
      scrollTop: sc ? sc.scrollTop : 0, scrollHeight: sc ? sc.scrollHeight : 0, clientHeight: sc ? sc.clientHeight : 0,
      late: (key, more = 'A late edit makes this message longer.\n\n'.repeat(6)) => {
        const m = v && v.messages.find((x) => 'm' + x.id === key);
        if (!m) return false;
        session.apply({ type: 'message', run: session.sel, data: { ...m, content: m.content + '\n\n' + more } });
        return true;
      },
    };
  }
}
