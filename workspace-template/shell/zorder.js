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
