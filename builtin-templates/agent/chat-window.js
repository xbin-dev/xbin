// chat-window.js — the open conversation as a window of its blocks (D130).
//
// The session holds a run of consecutive pages of the transcript
// (model/session.js: the newest when the chat opens, older ones read as the
// reader nears the top, pages far from the reader let go in either
// direction); of the blocks those fold into, the timeline renders a window,
// grown and trimmed around the view. Nothing the reader looks at moves, and
// while the live tail is let go (the reader is far up) the pill says how
// much is new and brings it back.
//
// The bookkeeping is xbind's chat kit's ChatWindow (/vendor/chat/window.js,
// D224 — the Agent tab's too), with this chat's numbers and rules as its
// options; this class is the Session's side of it — the kit's `source` over
// the session and what it shows — under the name and the methods agent.js
// always called. (The page needs the kit's xbind — boot.js — so the
// every-block fallback for an xbind without /vendor/scroll-window.js is
// gone with the xbinds it was for.)
import { ChatWindow as KitWindow } from '/vendor/chat/window.js';

const PAGE = 40;   // rows the window grows by (and the tail it opens on)
const TRIM = 160;  // a rendered window longer than this is trimmed on the side far from the view
const UNLOAD = 3;  // messages about this many views beyond the rendered rows are let go

export class ChatWindow {
  // session: the Session (model/session.js)
  constructor(session) {
    this.session = session;
    this.s = {};   // what the session shows, as of the last place()
    this.sc = null;
    const self = this;
    this.kit = new KitWindow({
      source: {
        get version() { return session.version; },
        get hasOlder() { return !!self.s.hasOlder; },
        get hasNewer() { return !!self.s.hasNewer; },
        get fresh() { return self.s.fresh || 0; },
        // one page read at a time; its rows render when it lands, whatever it brought
        loadOlder: () => Promise.resolve(session.loadOlder()).then(() => true, () => true),
        loadNewer: () => Promise.resolve(session.loadNewer()).then(() => true, () => true),
        // (the session says itself when letting go changed what it holds)
        keep: (lo, hi, dropTail) => { session.keep(lo, hi, dropTail); return false; },
        follow: (at) => session.follow(at),
      },
      changed: () => session.changed(),
      keyOf: (b) => b.id,
      page: PAGE, trim: TRIM, unload: UNLOAD,
      // never fewer than two steps of the window's growth beyond it; a grown window renders before anything
      // goes; nothing goes while a page is being read; a trim that is due renders again wherever the reader is
      minMargin: 2 * PAGE, growFirst: true, holdWhileBusy: true, retrim: 'any',
      paused: () => !self.kit.sw.height, // nothing to measure yet (the timeline is hidden)
    });
    this.reset();
  }

  // a conversation opened: its tail, followed
  reset() {
    this.kit.reset();
    this.kit.drawn = -1;
    this.s = {};
    this.pill = '';
  }

  attach(sc) { this.sc = sc; this.kit.sw.attach(sc); }
  detach() { this.sc = null; this.kit.detach(); }

  get atBottom() { return this.kit.atBottom; }
  get start() { return this.kit.start; }
  get end() { return this.kit.end; }
  get sw() { return this.kit.sw; }

  // ---- the update protocol: place() right before the render, after() right after ----

  // place picks the window of s.blocks (the kit's place) and returns what
  // sessionTpl draws: {start, end, pill, older(), latest()}.
  place(s) {
    this.s = s;
    const { start, end } = this.kit.place(s.blocks);
    const words = this.kit.pill();
    this.pill = words ? `↓ ${words}` : '';
    return { start, end, pill: this.pill, older: () => this.older(), latest: () => this.latest() };
  }

  after() { this.kit.after(this.sc); }

  // ---- the reader ----

  // "load earlier": a page of rows above the window, read first if need be
  older() {
    if (this.kit.start === 0) this.kit.sw.keepView = true;
    this.kit.older();
  }

  // "↓ N new — jump to latest": the newest page (read again when the tail
  // was let go), followed
  async latest() {
    const kit = this.kit;
    if (this.s.hasNewer || this.s.detached) {
      await kit.busy;
      const p = kit.busy = this.session.latest().catch(() => { /* the pill stays */ }).finally(() => { if (kit.busy === p) kit.busy = null; });
      await p;
    }
    kit.fromKey = kit.toKey = null;
    this.toBottom();
    this.session.changed();
  }

  // the reader opened or closed something: keep their view, even at the bottom
  keepView() { this.kit.keepView(); }

  toBottom() {
    this.session.follow(true);
    this.kit.toBottom();
  }

  // testApi: what the UI harness reads (hack/ui-harness: agentTemplateLong)
  // — the window, what the session holds, the scroller — and late(key), a
  // late event for the message of block `key`: its text grows.
  testApi() {
    const s = this.s, sc = this.sc, session = this.session, v = session.views.get(session.sel);
    return {
      windowed: true, start: this.kit.start, end: this.kit.end, total: (this.kit.blocks || []).length,
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
