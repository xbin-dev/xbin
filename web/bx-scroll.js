/**
 * bx-scroll.js — thin themed scrollbars and the focused-scroll tint (D123).
 *
 * The workspace nests scrollers two or three deep (the shell canvas, a tile's
 * document, a textarea or transcript inside it), and a native bar says nothing
 * about which one the next wheel or key moves. Two pieces:
 *
 *   scrollCssText        — the scrollbar CSS: a 6px bar whose thumb is drawn
 *                          3px at the outer edge and fattens while hovered or
 *                          dragged (the width never changes, so xterm's one-time
 *                          measurement holds); the scroller carrying
 *                          [data-bx-scroll] gets a hazard-amber thumb. Fine
 *                          pointers only — touch keeps its native overlay bars.
 *                          Shadow roots include it (lit: unsafeCSS); the same
 *                          rules sit in /vendor/theme.css for documents.
 *   installScrollFocus() — keeps [data-bx-scroll] on the scroller the next
 *                          scroll would move: the innermost scrollable under
 *                          the mouse, the one a wheel actually latches onto
 *                          (direction-aware, as chaining is), or the focused
 *                          element's on a scroll key. Runs on import, once per
 *                          document. A tile document gets it from
 *                          xbin-client.js when it links theme.css.
 *
 * One tint per screen, across documents: a framed document's tracker tells its
 * embedder when the pointer arrives (postMessage {type:'xbin:scroll-focus'},
 * docs/protocol.md), and the embedder's tracker drops its own tint — browsers
 * do not reliably send the parent a pointerout when the pointer crosses into
 * an (out-of-process) iframe. Leaving the iframe, the framed document gets its
 * pointerout and drops its tint.
 *
 * Chromium 121+ ignores ::-webkit-scrollbar on an element whose scrollbar-color
 * or scrollbar-width is set (and scrollbar-color inherits), so the standard
 * properties sit behind @supports not selector(::-webkit-scrollbar): Firefox
 * gets them, Chromium never does. Dependency-free: tile documents load it.
 */

const THUMB = 'color-mix(in srgb, var(--bx-muted, #868f9a) 40%, transparent)';
const THUMB_HOT = 'color-mix(in srgb, var(--bx-muted, #868f9a) 75%, transparent)';
const TINT = 'color-mix(in srgb, var(--bx-accent, #f5a623) 60%, transparent)';
const TINT_HOT = 'color-mix(in srgb, var(--bx-accent, #f5a623) 90%, transparent)';

export const scrollCssText = `
@media (hover: hover) and (pointer: fine) {
  ::-webkit-scrollbar { width: 6px; height: 6px; }
  ::-webkit-scrollbar-track, ::-webkit-scrollbar-corner { background: transparent; }
  ::-webkit-scrollbar-thumb { background: ${THUMB}; background-clip: padding-box;
    border: 0 solid transparent; border-left-width: 3px; border-radius: 6px; }
  ::-webkit-scrollbar-thumb:horizontal { border-left-width: 0; border-top-width: 3px; }
  ::-webkit-scrollbar-thumb:hover, ::-webkit-scrollbar-thumb:active { border-width: 0; background-color: ${THUMB_HOT}; }
  [data-bx-scroll]::-webkit-scrollbar-thumb { background-color: ${TINT}; }
  [data-bx-scroll]::-webkit-scrollbar-thumb:hover, [data-bx-scroll]::-webkit-scrollbar-thumb:active { background-color: ${TINT_HOT}; }
  @supports not selector(::-webkit-scrollbar) {
    * { scrollbar-width: thin; scrollbar-color: ${THUMB} transparent; }
    [data-bx-scroll] { scrollbar-color: ${TINT} transparent; }
  }
}
`;

const ATTR = 'data-bx-scroll';
const LATCH_MS = 300; // a wheel gesture stays on the scroller it latched onto (Chromium does the same)
const SCROLL_KEYS = new Set(['PageUp', 'PageDown', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', ' ', 'Home', 'End']);
const SCROLLS = new Set(['auto', 'scroll', 'overlay']);

// el scrolls along axis: a scroll container whose content overflows it. The
// document's own scroller is the root element (its overflow, else a propagated
// body overflow, decides); a body whose overflow went to the viewport is not one.
function scrollable(el, axis) {
  const doc = el.ownerDocument;
  const prop = axis === 'x' ? 'overflowX' : 'overflowY';
  if (el === doc.documentElement) {
    let ov = getComputedStyle(el)[prop];
    if (ov === 'visible' && doc.body) ov = getComputedStyle(doc.body)[prop];
    if (ov === 'hidden' || ov === 'clip') return false;
  } else {
    if (el === doc.body && getComputedStyle(doc.documentElement)[prop] === 'visible') return false;
    if (!SCROLLS.has(getComputedStyle(el)[prop])) return false;
  }
  return axis === 'x' ? el.scrollWidth > el.clientWidth + 1 : el.scrollHeight > el.clientHeight + 1;
}

// el can still move along axis in direction sign (-1 up/left, +1 down/right)
function canMove(el, axis, sign) {
  const pos = axis === 'x' ? Math.abs(el.scrollLeft) : el.scrollTop; // RTL scrollLeft runs negative
  const max = axis === 'x' ? el.scrollWidth - el.clientWidth : el.scrollHeight - el.clientHeight;
  return sign < 0 ? pos > 0.5 : pos < max - 1;
}

const contains = (el, axis) => /^(contain|none)$/.test(getComputedStyle(el)[axis === 'x' ? 'overscrollBehaviorX' : 'overscrollBehaviorY']);
// composedPath() is empty once dispatch ends: take it in the handler
const elems = (path) => path.filter((n) => n instanceof Element);

export function installScrollFocus(win = globalThis.window) {
  if (!win || !win.document) return;
  const KEY = Symbol.for('bx-scroll-focus');
  if (win[KEY]) return;
  win[KEY] = true;
  let marked = null;
  let latch = null; // {el, until}: the wheel gesture's scroller
  let lastTarget = null;
  let pending = null; // the last pointermove's composed path, handled once per frame
  let inside = false; // the pointer is over this document (not one of its iframes)
  const framed = win.parent && win.parent !== win;

  const mark = (el) => {
    if (el === marked) return;
    if (marked) marked.removeAttribute(ATTR);
    marked = el && el.isConnected ? el : null;
    if (marked) marked.setAttribute(ATTR, '');
  };
  const innermost = (path) => path.find((el) => scrollable(el, 'y') || scrollable(el, 'x')) || null;

  // the pointer rests over: the innermost scroller (onto a tile's iframe, the
  // tile's own tracker takes over — this document marks nothing)
  const hover = () => {
    const raw = pending; pending = null;
    if (!raw) return;
    const path = elems(raw);
    const t = path[0] || null;
    if (t && t.tagName === 'IFRAME') { lastTarget = t; latch = null; mark(null); return; }
    if (latch && performance.now() < latch.until && path.includes(latch.el)) return;
    if (t === lastTarget && (!marked || marked.isConnected)) return;
    lastTarget = t;
    mark(innermost(path));
  };

  win.addEventListener('pointermove', (e) => {
    if (e.pointerType === 'touch') return;
    if (!inside) {
      inside = true;
      if (framed) { try { win.parent.postMessage({ type: 'xbin:scroll-focus' }, '*'); } catch { /* cosmetic */ } }
    }
    if (!pending) win.requestAnimationFrame(hover);
    pending = e.composedPath();
  }, { capture: true, passive: true });

  const leave = () => { inside = false; lastTarget = null; latch = null; pending = null; mark(null); };
  win.addEventListener('pointerout', (e) => { if (e.pointerType !== 'touch' && !e.relatedTarget) leave(); }, { capture: true, passive: true }); // left the document
  // into a tile's iframe: that document's own tracker takes over
  win.addEventListener('pointerover', (e) => { if (e.pointerType !== 'touch' && e.composedPath()[0]?.tagName === 'IFRAME') leave(); }, { capture: true, passive: true });
  win.addEventListener('message', (e) => { if (e.source && e.source !== win && e.data && e.data.type === 'xbin:scroll-focus') leave(); });

  // a wheel moves the first scroller along the path that can still go that
  // way, unless one on the way contains its overscroll
  win.addEventListener('wheel', (e) => {
    const path = elems(e.composedPath());
    const now = performance.now();
    if (latch && now < latch.until && latch.el.isConnected && path.includes(latch.el)) { latch.until = now + LATCH_MS; return; }
    const axis = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? 'x' : 'y';
    const sign = Math.sign(axis === 'x' ? e.deltaX : e.deltaY);
    if (!sign) return;
    let pick = null;
    for (const el of path) {
      if (!scrollable(el, axis)) continue;
      if (canMove(el, axis, sign) || contains(el, axis)) { pick = el; break; }
    }
    if (!pick) return; // nothing here moves (the wheel may chain out of this document)
    latch = { el: pick, until: now + LATCH_MS };
    lastTarget = path[0] || null;
    mark(pick);
  }, { capture: true, passive: true });

  // the keyboard scrolls what holds focus
  const byFocus = (e) => { const s = innermost(elems(e.composedPath())); if (s) { latch = null; mark(s); } };
  win.addEventListener('keydown', (e) => { if (SCROLL_KEYS.has(e.key) && !e.ctrlKey && !e.metaKey && !e.altKey) byFocus(e); }, { capture: true, passive: true });
  win.addEventListener('focusin', byFocus, { capture: true, passive: true });
}

installScrollFocus();
