// hack/shell-anchor.test.mjs — the focused tile holds its place (D191,
// workspace-template/shell/shell-anchor.js), run by `make js-test`: the
// scroll position that keeps the focused card's title bar at its viewport
// y, the bottom spacer when the page would end too soon, the baseline as
// the person scrolls, and the rules for a layout's own clamp.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hold, scrolled, trim, clamped, inView } from '../workspace-template/shell/shell-anchor.js';

test('a tile above grows: the page scrolls down by as much', () => {
  // head was at 300, a tile above grew 250 px: the head is now at 550
  assert.deepEqual(hold({ y0: 300, y1: 550, top: 1000, natural: 5000, client: 900 }), { top: 1250, spacer: 0, kept: true });
});

test('a tile above shrinks: the page scrolls up by as much', () => {
  assert.deepEqual(hold({ y0: 300, y1: 120, top: 1000, natural: 4000, client: 900 }), { top: 820, spacer: 0, kept: true });
});

test('nothing moved: nothing changes', () => {
  assert.deepEqual(hold({ y0: 300, y1: 300, top: 640, natural: 4000, client: 900 }), { top: 640, spacer: 0, kept: true });
});

test('the page top stops it: held as far as it goes', () => {
  // the strip above the canvas emptied (-200) with the page scrolled only 80
  assert.deepEqual(hold({ y0: 300, y1: 100, top: 80, natural: 4000, client: 900 }), { top: 0, spacer: 0, kept: false });
});

test('the last tile shrinks: a spacer makes the room', () => {
  // scrolled to the end (top 3100 of natural 4000 - 900); the last tile shrank
  // 600 px, the browser clamped the page to the new end (2500) and the head
  // moved down by the 600 the clamp scrolled back
  const h = hold({ y0: 200, y1: 800, top: 2500, natural: 3400, client: 900 });
  assert.deepEqual(h, { top: 3100, spacer: 600, kept: true });
});

test("holding would leave none of the tile in view: the page's own end, no spacer", () => {
  const view = { top: 40, bottom: 900 };
  // a 1100 px tile read near its foot (head at -400) shrank to 150 px: the
  // browser clamped the page to its new end and the card sits there, in view
  const card = { top: 550, bottom: 700 };
  assert.deepEqual(hold({ y0: -400, y1: 550, top: 2500, natural: 3400, client: 900, card, view }), { top: 2500, spacer: 0, kept: false });
  // the same shrink with the head in view: held, with a spacer
  assert.deepEqual(hold({ y0: 200, y1: 800, top: 2500, natural: 3400, client: 900, card: { top: 800, bottom: 950 }, view }), { top: 3100, spacer: 600, kept: true });
});

test('a spacer only covers what is past the natural end, and rounds up', () => {
  assert.equal(hold({ y0: 0, y1: 10.4, top: 990, natural: 1900, client: 900 }).spacer, 1);
  assert.equal(hold({ y0: 0, y1: 0, top: 500, natural: 1900, client: 900 }).spacer, 0);
  // content shorter than the viewport: everything scrolled is spacer
  assert.equal(hold({ y0: 0, y1: 300, top: 0, natural: 500, client: 900 }).spacer, 300);
});

test('the person scrolls: the baseline moves with the page', () => {
  assert.equal(scrolled(300, 1000, 1200), 100);
  assert.equal(scrolled(300, 1000, 700), 600);
  assert.equal(scrolled(300, 1000, 1000), 300);
});

test('the spacer shrinks as the person scrolls up past it, never grows', () => {
  // natural end 2500 (3400 - 900); spacer 600
  assert.equal(trim(600, 3100, 3400, 900), 600); // still at the end of the room
  assert.equal(trim(600, 2800, 3400, 900), 300); // half of it scrolled back
  assert.equal(trim(600, 2400, 3400, 900), 0);   // above the natural end: gone
  assert.equal(trim(600, 3500, 3400, 900), 600); // never more than it was
  // the page grew back under it: no room needed any more
  assert.equal(trim(600, 3100, 4200, 900), 0);
});

test("a layout's own clamp is told from the person's scroll", () => {
  // the page got shorter and the browser pulled scrollTop to the new end
  assert.equal(clamped(3100, 2500, 3400, 900), true);
  // the person scrolled up, not at the end
  assert.equal(clamped(3100, 2400, 3400, 900), false);
  // the person scrolled down
  assert.equal(clamped(2000, 2500, 3400, 900), false);
});

test('in view: any part of the card inside the scroller', () => {
  const v = { top: 50, bottom: 950 };
  assert.equal(inView({ top: -2000, bottom: 60 }, v), true);  // a tall card, its foot in view
  assert.equal(inView({ top: -2000, bottom: 50 }, v), false); // just above
  assert.equal(inView({ top: 950, bottom: 1400 }, v), false); // just below
  assert.equal(inView({ top: 400, bottom: 800 }, v), true);
});
