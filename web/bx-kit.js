/**
 * bx-kit — the small helpers every shipped frontend used to re-implement:
 * an API call that throws on error, a JSON body, HTML escaping, the deep
 * active element across shadow roots, composed-path matching, the viewport
 * clamp for floating windows and the anchored-window geometry. Import by absolute URL from any
 * document (sandboxed tiles included — /vendor/ is credential-less):
 *
 *   import { xbinApi, jbody, esc } from '/vendor/bx-kit.js';
 *
 * Zero dependencies; every export is a plain function. `make js-check`
 * refuses a second definition of these names elsewhere (docs/frontend-kit.md).
 */

// sandboxed(): is this document a sandboxed tile frame? xbind injects
// <meta name="xbin-sandbox"> into exactly those (internal/server/static.go).
export const sandboxed = () => typeof document !== 'undefined' && !!document.querySelector('meta[name="xbin-sandbox"]');

// api(url, opts): the request carries the credential the document HAS —
// in a sandboxed tile the frame token via the in-frame client (the tile's
// own identity; the opaque origin holds no cookie), in chrome (the shell,
// tiles/organisations, an admin-approved `chrome: true` tile) the session cookie, so
// the caller is the signed-in human. That is the identity split every
// hand-rolled helper encoded before the kit (a chrome tile calling through
// xbin.fetch would act as the tile, not the person — and lose its rights).
// Resolves the parsed JSON (or the text when the body is not JSON, null
// when empty); rejects with the server's `error` message on a non-2xx.
export async function api(url, opts) {
  const f = sandboxed() && window.xbin?.fetch ? window.xbin.fetch : fetch;
  const r = await f(url, opts);
  const text = await r.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error((data && typeof data === 'object' && data.error) || `error ${r.status}`);
  return data;
}

// xbinApi('/grants', opts): the built-in API under /api/xbin.
export const xbinApi = (path, opts) => api('/api/xbin' + path, opts);

// selfApi('/runs', opts): this component's own backend (the xbin global names it).
export const selfApi = (path, opts) => api(`/api/${window.xbin?.self ?? ''}` + path, opts);

// jbody(value, method?): a JSON request body (+ method) to spread into opts.
export const jbody = (body, method) => ({
  ...(method ? { method } : {}),
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

// esc(s): HTML-escape for text dropped into markup strings — in TEXT or in
// ATTRIBUTE position. Quotes are escaped too: `title="${esc(json)}"` with
// &<> alone lets '{"a":"x" onmouseover="…"}' close the attribute and add a
// handler (a live XSS in the agent template's tool-call titles, fixed
// 2026-09-19). Escaped quotes render identically in text, so one function
// covers both and there is no second escAttr() for a future edit to forget.
const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);

// deepActive(): the focused element, descending through open shadow roots.
export function deepActive() {
  let el = document.activeElement;
  while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
  return el;
}

// pathHas(event, selector): does any element on the event's composed path
// match? e.target is retargeted at shadow boundaries, so an <input> inside
// a nested element never matches a plain e.target.closest().
export const pathHas = (e, sel) => e.composedPath().some((n) => n instanceof Element && n.matches(sel));

// clampBox(box, {minW, minH, margin, defW, defH}): keep a viewport-fixed
// window reachable — never wider/taller than the viewport minus the margin,
// never off-screen; a missing size falls back to defW×defH.
export function clampBox(box, { minW = 200, minH = 140, margin = 8, defW = 560, defH = 320 } = {}) {
  const W = window.innerWidth, H = window.innerHeight;
  const n = (v, d) => (Number.isFinite(Number(v)) ? Number(v) : d);
  const w = Math.max(Math.min(minW, W - 2 * margin), Math.min(n(box?.w, defW), W - 2 * margin));
  const h = Math.max(Math.min(minH, H - 2 * margin), Math.min(n(box?.h, defH), H - 2 * margin));
  const x = Math.max(margin, Math.min(n(box?.x, margin), W - w - margin));
  const y = Math.max(margin, Math.min(n(box?.y, margin), H - h - margin));
  return { ...box, x, y, w, h };
}

// anchorBox(rect, pop, bounds?): a window anchored to an element — offsets
// {dx, dy} from the element's top-left plus a size {w, h} — as a viewport
// box. With `bounds` (a viewport rect) the top-left is kept inside it:
// never left of or above it, and at least 60×24 px of the window over it —
// the shell hands the canvas's tile extent so a terminal pop-up is always
// reachable by scrolling (D66).
export function anchorBox(rect, pop, bounds) {
  let x = rect.left + pop.dx, y = rect.top + pop.dy;
  if (bounds) {
    x = Math.max(bounds.left, Math.min(x, Math.max(bounds.left, bounds.right - 60)));
    y = Math.max(bounds.top, Math.min(y, Math.max(bounds.top, bounds.bottom - 24)));
  }
  return { x: Math.round(x), y: Math.round(y), w: pop.w, h: pop.h };
}

// anchorOffsets(rect, box): the inverse — a viewport box as offsets from the element.
export const anchorOffsets = (rect, box) => ({ dx: box.x - rect.left, dy: box.y - rect.top, w: box.w, h: box.h });

// followBox(target, box, alive): keep a viewport-fixed element where box()
// says, every animation frame while alive() — the anchored window's follow
// loop (the anchor scrolls or is dragged; the window moves in the same
// paint). One rect read per frame, a style write only when it changed.
// Returns the stop function.
export function followBox(target, box, alive) {
  let id = 0;
  const tick = () => {
    if (!alive()) { id = 0; return; }
    const el = target(), b = box();
    if (el && b) {
      const l = b.x + 'px', t = b.y + 'px';
      if (el.style.left !== l) el.style.left = l;
      if (el.style.top !== t) el.style.top = t;
    }
    id = requestAnimationFrame(tick);
  };
  id = requestAnimationFrame(tick);
  return () => cancelAnimationFrame(id);
}

// dragShield(cursor): a transparent full-viewport layer for the duration of
// a pointer drag, so tile <iframe>s cannot swallow the pointer when the cursor
// races over them (which otherwise stalls window pointermove until the cursor
// leaves the iframe). Returns the cleanup.
export function dragShield(cursor = 'grabbing') {
  const el = shieldEl(cursor);
  return () => el.remove();
}
function shieldEl(cursor) {
  const el = document.createElement('div');
  el.style.cssText = `position:fixed; inset:0; z-index:2147483647; cursor:${cursor}; touch-action:none; outline:none;`;
  el.dataset.dragShield = ''; // "a drag is under way" for whoever asks (the shell's layout reload)
  document.body.appendChild(el);
  return el;
}

// dragPointer({ev, cursor, onMove, onUp, onCancel}): the window-level pointer
// drag every draggable window, grid tile and divider shares (D217). The
// shield goes up and holds the pointer — captured from `ev` (the pointer
// event that starts the drag), else from the first move — so neither an
// <iframe> under a fast cursor nor the browser window's edge can take it;
// onMove gets each pointermove; on pointerup the listeners and the shield go
// and onUp(e) commits (also on a mouse move with no button held: a release
// nobody saw). Every other end takes the shield down too — Escape (kept from
// what is under it), the pointer cancelled or its capture lost, the window
// losing focus — then onCancel() runs, or, without one, onUp(e) with the
// last move, so a caller's drag state never outlives the drag. A tile's page
// that had the keyboard gets it back when the drag ends. Guards (which
// button, which target, mobile) stay with the caller. Returns a function
// that ends the drag early (onUp).
export function dragPointer({ ev = null, cursor = 'grabbing', onMove, onUp, onCancel } = {}) {
  const kbd = deepActive()?.tagName === 'IFRAME' ? deepActive() : null;
  const shield = shieldEl(cursor);
  let last = ev, held = false, over = false;
  const hold = (e) => {
    if (held || e?.pointerId == null) return;
    try { shield.setPointerCapture(e.pointerId); held = true; } catch { /* not an active pointer (a synthetic event) */ }
  };
  const end = (e, cancel) => {
    if (over) return;
    over = true;
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', up);
    window.removeEventListener('pointercancel', lost);
    window.removeEventListener('blur', lost);
    window.removeEventListener('keydown', key, true);
    shield.removeEventListener('lostpointercapture', lost);
    shield.remove(); // first: whatever the callbacks do, the iframes take the pointer again
    if (cancel && onCancel) onCancel();
    else onUp?.(e);
    // the keyboard back to the page it was taken from, unless a callback put it elsewhere
    if (kbd?.isConnected && [shield, document.body, null].includes(deepActive() ?? null)) kbd.focus({ preventScroll: true });
  };
  const move = (e) => {
    if (e.pointerType === 'mouse' && e.buttons === 0) { end(last, false); return; }
    hold(e);
    last = e;
    onMove?.(e);
  };
  const up = (e) => end(e, false);
  const lost = () => end(last, true);
  // ended after the event, so a caller's own Escape (window, capture) is seen first
  const key = (k) => {
    if (k.key !== 'Escape') return;
    k.preventDefault(); k.stopPropagation();
    setTimeout(lost, 0);
  };
  hold(ev);
  // a tile's page with the keyboard keeps it through a press on the chrome
  // (the drag's pointerdown is cancelled): the shield takes it for the drag,
  // so Escape reaches this document, and end() gives it back
  if (kbd) { shield.tabIndex = -1; shield.focus({ preventScroll: true }); }
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', up);
  window.addEventListener('pointercancel', lost);
  window.addEventListener('blur', lost);
  window.addEventListener('keydown', key, true);
  shield.addEventListener('lostpointercapture', lost);
  return (e) => end(e ?? last, false);
}

// dragWindow(ev, el, {bounds, onMove, onUp}): a title-bar drag of a
// position:fixed window `el` from pointerdown `ev`: the window follows the
// pointer, fenced by `bounds` (a viewport rect its top-left stays inside —
// the shell's canvas) or, without one, kept reachable on screen; onMove(x, y)
// gets each placement, onUp() the release. A cancelled drag (Escape…) puts
// the window back, then calls onUp. Ignores anything but the main button
// and any drag starting on a control (button, select, .tab).
export function dragWindow(ev, el, { bounds = null, onMove, onUp } = {}) {
  if (!el || ev.button !== 0 || ev.target.closest('button, select, .tab')) return;
  ev.preventDefault();
  const x0 = el.offsetLeft, y0 = el.offsetTop, sx = ev.clientX - x0, sy = ev.clientY - y0;
  const place = (x, y) => { el.style.left = x + 'px'; el.style.top = y + 'px'; onMove?.(x, y); };
  dragPointer({
    ev,
    onMove: (e) => {
      let x = e.clientX - sx, y = e.clientY - sy;
      if (bounds) ({ x, y } = anchorBox({ left: 0, top: 0 }, { dx: x, dy: y }, bounds));
      else { x = Math.max(-el.offsetWidth + 60, Math.min(x, window.innerWidth - 40)); y = Math.max(0, Math.min(y, window.innerHeight - 24)); }
      place(x, y);
    },
    onUp,
    onCancel: () => { place(x0, y0); onUp?.(); },
  });
}

// dragResize(ev, el, edge, {maxW, maxH, onMove, onUp}): a window's resize
// grip (D217) — `edge` 'e' (the right edge: the width), 's' (the bottom: the
// height) or 'se' (the corner: both) of a position:fixed window `el`, from
// pointerdown `ev`; its top-left stays. The size follows the pointer between
// the element's CSS min-width/min-height and maxW/maxH (by default 8 px
// short of the viewport's right and bottom edges, or the size it has if
// that is more); onMove(w, h) gets each size, onUp(w, h) the release. A
// cancelled drag puts the size back, then calls onUp with it. Main button
// only.
export function dragResize(ev, el, edge, { maxW, maxH, onMove, onUp } = {}) {
  if (!el || ev.button !== 0) return;
  ev.preventDefault();
  const r = el.getBoundingClientRect(), cs = getComputedStyle(el);
  const w0 = el.offsetWidth, h0 = el.offsetHeight, sx = ev.clientX, sy = ev.clientY;
  const minW = parseFloat(cs.minWidth) || 0, minH = parseFloat(cs.minHeight) || 0;
  const hiW = maxW ?? Math.max(w0, window.innerWidth - 8 - r.left), hiH = maxH ?? Math.max(h0, window.innerHeight - 8 - r.top);
  const clamp = (v, lo, hi) => Math.round(Math.max(lo, Math.min(v, hi)));
  let w = w0, h = h0;
  const size = (nw, nh) => { w = nw; h = nh; el.style.width = w + 'px'; el.style.height = h + 'px'; onMove?.(w, h); };
  dragPointer({
    ev,
    cursor: edge === 'e' ? 'ew-resize' : edge === 's' ? 'ns-resize' : 'nwse-resize',
    onMove: (e) => size(edge === 's' ? w0 : clamp(w0 + e.clientX - sx, minW, hiW), edge === 'e' ? h0 : clamp(h0 + e.clientY - sy, minH, hiH)),
    onUp: () => onUp?.(w, h),
    onCancel: () => { size(w0, h0); onUp?.(w, h); },
  });
}
