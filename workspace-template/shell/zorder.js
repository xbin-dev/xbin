// shell/zorder.js — one z-order counter for what the shell stacks above the
// grid: floating (unpinned) tile windows and tile-spawned pop-out windows
// share it, so the last one touched is on top of both kinds. Kept below
// bx-frame's terminal pop-ups (which start at 2000) so a terminal always
// sits on top. A module so the counter survives the canvas becoming its
// own element.
let top = 100;

// nextZ(): a z above everything handed out so far.
export const nextZ = () => ++top;

// raiseTo(z): continue counting from z (a float's persisted z from an
// earlier session can be above the counter) and return it.
export function raiseTo(z) { top = z; return top; }

// The active window (product-ui 3; D184): the one brought to the front
// last wears the active title bar and edge. Every kind of window says so
// with one page-wide event, `bx-window-front` {key} — grid cards and float
// tiles ('tile:<path>'), spawned windows ('spawn:<id>'), and bx-frame's
// terminal pop-ups, which dispatch it themselves (/vendor/bx-frame.js) — so
// each knows whether it is the one, whoever stacks it.
export const FRONT_EVENT = 'bx-window-front';
let active = '';
if (typeof window !== 'undefined') window.addEventListener(FRONT_EVENT, (e) => { active = e.detail?.key ?? ''; });

// frontWindow(key): the window `key` is the active one now.
export function frontWindow(key) {
  if (key === active || typeof window === 'undefined') return;
  window.dispatchEvent(new CustomEvent(FRONT_EVENT, { detail: { key } }));
}
// activeWindow() → the active window's key ('' before any).
export const activeWindow = () => active;
// onWindowFront(cb) → unsubscribe: cb(key) whenever another window comes to the front.
export function onWindowFront(cb) {
  const f = (e) => cb(e.detail?.key ?? '');
  window.addEventListener(FRONT_EVENT, f);
  return () => window.removeEventListener(FRONT_EVENT, f);
}
