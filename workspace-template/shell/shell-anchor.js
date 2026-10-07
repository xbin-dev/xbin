// shell/shell-anchor.js — the focused tile holds its place (D191). While a
// tile's card is the active window (zorder.js: clicked, its frame focused,
// a control in its head focused), the top of its title bar stays at the
// same height in the browser window, whatever happens to the page around
// it: a tile above or below growing or shrinking to its content (Document
// mode, D187), a tile opened or closed, the stacked cards of a phone's
// canvas changing size, the decision strip above the canvas filling, the
// window resized. The focused tile itself growing or shrinking leaves its
// head — and so what is under it — where it was too; when the page would
// end too soon to keep it (the last tile shrank), a bottom spacer
// (padding under <bx-canvas>) makes the room, and goes as the person
// scrolls up past it or the page grows back — unless keeping the head
// would leave none of the tile in view (a tall tile read near its foot
// shrank): then the page ends where it ends and the hold starts again
// where the head lands.
//
// How: the head's viewport y is the baseline. A ResizeObserver on the
// canvas, <main> (the scroller) and main's other children, and every canvas
// render, measure the head again and move main's scrollTop by the
// difference — in the ResizeObserver's turn, after layout and before
// paint, so nothing visibly jumps. The person scrolling only moves the
// baseline with the page; scrolling the card fully out of view lets go
// until it is back in view or focused again. A drag (a card, a height
// handle) suspends the hold, and its drop is the person's own placement:
// the baseline is taken again after it. Native scroll anchoring is off on
// main while a hold is on (it would move the page under us), on again
// otherwise. Floating windows don't scroll with the page: never held.
//
// The arithmetic is pure and exported (hack/shell-anchor.test.mjs, `make
// js-test`); the controller is the DOM half.
import { activeWindow, onWindowFront } from './zorder.js';

// hold({y0, y1, top, natural, client, card, view}) → {top, spacer, kept}:
// the scroll position that puts the head (now at viewport y1) back at y0,
// from scroll position `top`; `natural` is the scroll height without a
// spacer, `client` the scroller's height. spacer: the extra room needed
// below the content to scroll that far. kept: false when the head can't be
// kept — the page's top stops it (nothing above to scroll into), or keeping
// it would leave the focused card (`card`, its viewport rect now) wholly
// outside the scroller (`view`): a tall tile read near its foot that
// shrank. The page then goes only as far as its natural end, no spacer
// (what is left of the tile shows), and the head stays where it lands.
export function hold({ y0, y1, top, natural, client, card = null, view = null }) {
  const want = top + (y1 - y0), max = Math.max(0, natural - client);
  const to = Math.max(0, want), d = to - top;
  if (card && view && !inView({ top: card.top - d, bottom: card.bottom - d }, view)) {
    return { top: Math.min(to, Math.max(top, max)), spacer: 0, kept: false };
  }
  return { top: to, spacer: Math.max(0, Math.ceil(to - max)), kept: to === want };
}

// scrolled(y0, s0, s) → the baseline after the page scrolled from s0 to s:
// the head moved with the page.
export const scrolled = (y0, s0, s) => y0 + (s0 - s);

// trim(spacer, top, natural, client) → the spacer still needed at scroll
// position `top`: only what lies above the viewport's bottom edge; never
// more than it was (the person can't scroll down into new room).
export const trim = (spacer, top, natural, client) =>
  Math.max(0, Math.min(spacer, Math.ceil(top - Math.max(0, natural - client))));

// clamped(s0, s, scrollHeight, client) → whether a scroll position change
// seen at a layout (not by a scroll event yet) is the layout's own — the
// extent shrank under it and the browser pulled it to the new end — rather
// than the person's.
export const clamped = (s0, s, scrollHeight, client) => s < s0 && s >= scrollHeight - client - 1;

// inView(card, view) → whether any of the card's rect is inside the view's.
export const inView = (r, v) => r.bottom > v.top && r.top < v.bottom;

// held(main) → the scroll position the anchor last set on that scroller
// (a scroll event at it is the anchor's, not the person's: the Document
// mode top bar ignores it).
const ours = new WeakMap();
export const heldScroll = (el) => ours.get(el);

// FocusAnchor: a reactive controller on <bx-canvas>. The host provides
// renderRoot (the cards), `dragging` (a card or handle drag in flight) and
// sits inside the shell's <main>, the scroller.
export class FocusAnchor {
  constructor(host) {
    this.host = host;
    this.main = null;
    this.path = '';      // the focused tile (a grid or Document-mode card), '' for none
    this.y0 = null;      // its head's baseline viewport y; null: not held
    this.s0 = 0;         // main's scrollTop when the baseline was last in step
    this.spacer = 0;     // px of padding under the canvas
    this._seen = new WeakSet();
    this._drag = false; this._settling = false;
    this._at = null;     // desktop canvas: the head's offset in the canvas (_place)
    this._ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => this.sync());
    this._scroll = () => this._onScroll();
    this._front = (key) => this._focus(key);
    host.addController(this);
  }
  hostConnected() { this._off = onWindowFront(this._front); this._focus(activeWindow()); }
  hostDisconnected() {
    this._off?.();
    this._ro?.disconnect(); this._seen = new WeakSet();
    this.main?.removeEventListener('scroll', this._scroll);
    this.main = null;
  }
  hostUpdated() {
    const m = this.host.closest('main');
    if (m !== this.main) {
      this.main?.removeEventListener('scroll', this._scroll);
      this.main = m;
      if (!m) return;
      m.addEventListener('scroll', this._scroll, { passive: true });
      this.s0 = m.scrollTop;
    }
    if (!m) return;
    for (const el of [this.host, m, ...m.children]) {
      if (this._seen.has(el) || el.tagName === 'SLOT') continue;
      this._seen.add(el); this._ro?.observe(el);
    }
    const d = !!this.host.dragging;
    if (this._drag && !d) { // a drop: the person placed it; take the baseline once the drop has rendered
      this._settling = true;
      requestAnimationFrame(() => { this._settling = false; this._engage(); });
    }
    this._drag = d;
    this.sync();
  }

  // state() → what the harness reads (testApi().anchor).
  state() { return { path: this.path, held: this.y0 != null, y: this.y0, spacer: this.spacer }; }

  _card() {
    if (!this.path) return null;
    const p = CSS.escape(this.path);
    return this.host.renderRoot?.querySelector(`.gtile > .card[data-path="${p}"], .dcell > .card[data-path="${p}"]`) ?? null;
  }
  _head() { return this._card()?.querySelector('.head') ?? null; }
  _setHeld(y) {
    this.y0 = y;
    if (this.main) this.main.style.overflowAnchor = y == null ? '' : 'none';
  }
  // take the baseline now: the head where it is, if its card is in view
  _engage() {
    const m = this.main, card = this._card();
    if (!m) return;
    this.s0 = m.scrollTop;
    const head = card?.querySelector('.head');
    this._setHeld(head && inView(card.getBoundingClientRect(), m.getBoundingClientRect()) ? head.getBoundingClientRect().top : null);
    this._at = head ? this._place(head) : null;
  }
  // On the desktop canvas a card sits where its tile's x/y put it: a change
  // there is the tile moved (a layout edit, another device's layout), not
  // the page shifting around it — taken as the new place, not held. Its
  // head's offset in the canvas tells (null in Document mode and on a
  // phone, where the cards flow and every move is the page's).
  _place(head) {
    const h = this.host;
    return h.mode === 'doc' || h.mobile ? null : head.getBoundingClientRect().top - h.getBoundingClientRect().top;
  }
  _focus(key) {
    this.path = key?.startsWith('tile:') ? key.slice(5) : '';
    this._engage();
  }
  _setSpacer(px) {
    if (px === this.spacer) return;
    this.spacer = px;
    this.host.style.paddingBottom = px ? `${px}px` : '';
  }
  _trim() {
    const m = this.main;
    if (this.spacer && m) this._setSpacer(trim(this.spacer, m.scrollTop, m.scrollHeight - this.spacer, m.clientHeight));
  }

  // The person scrolled (or the anchor did: then nothing changes): the
  // baseline moves with the page; out of view lets go, back in view holds.
  _onScroll() {
    const m = this.main;
    if (!m) return;
    const s = m.scrollTop;
    if (s === this.s0) return;
    if (this._drag || this._settling) { this.s0 = s; return; }
    const card = this._card();
    if (!card || !inView(card.getBoundingClientRect(), m.getBoundingClientRect())) this._setHeld(null);
    else if (this.y0 == null) this._setHeld(card.querySelector('.head').getBoundingClientRect().top);
    else this.y0 = scrolled(this.y0, this.s0, s);
    this.s0 = s;
    this._trim();
  }

  // After a layout change: put the head back at its baseline.
  sync() {
    const m = this.main;
    if (!m) return;
    if (this._drag || this._settling || this.host.dragging) { this._engage(); return; }
    const head = this._head();
    if (!head) this._setHeld(null);
    if (this.y0 == null) { this._trim(); return; } // s0 stays: the scroll event still to come may bring the card back into view
    let s = m.scrollTop;
    const client = m.clientHeight;
    // a scroll the scroll event hasn't told us of yet — unless it is the
    // layout's own clamp (the page got shorter under it), which is ours to undo
    if (s !== this.s0 && !clamped(this.s0, s, m.scrollHeight, client)) this.y0 = scrolled(this.y0, this.s0, s);
    const at = this._place(head);
    if (at != null && this._at != null && Math.abs(at - this._at) >= 0.5) { this._engage(); return; }
    this._at = at;
    const y1 = head.getBoundingClientRect().top;
    if (Math.abs(y1 - this.y0) < 0.5) { this.s0 = s; this._trim(); return; }
    const natural = m.scrollHeight - this.spacer;
    const h = hold({ y0: this.y0, y1, top: s, natural, client, card: head.parentElement.getBoundingClientRect(), view: m.getBoundingClientRect() });
    this._setSpacer(h.spacer);
    m.scrollTop = h.top;
    s = m.scrollTop;
    ours.set(m, s);
    this.s0 = s;
    if (!h.kept) this.y0 = head.getBoundingClientRect().top;
  }
}
