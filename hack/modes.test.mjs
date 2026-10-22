// hack/modes.test.mjs — unit tests for workspace-template/shell/modes.js
// (D187, D193), run by `make js-test`: a screen's mode reads as one of the
// four (anything else as the canvas), a copy carries mode and arrange, and
// Document mode's column limit.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { MODES, screenMode, gridMode, arrangeOf, layoutFields, MODE_INFO, docWidth, withDocWidth, DOC_MIN_W } from '../workspace-template/shell/modes.js';

test('a screen reads as one of the four modes; anything else is the canvas', () => {
  assert.deepEqual(MODES, ['canvas', 'fixed', 'doc', 'tiled']);
  for (const m of MODES) assert.equal(screenMode({ mode: m }), m);
  for (const s of [undefined, null, {}, { mode: '' }, { mode: 'slides' }, { mode: 7 }]) assert.equal(screenMode(s), 'canvas');
  assert.ok(gridMode('canvas') && gridMode('fixed') && !gridMode('doc') && !gridMode('tiled'));
  assert.deepEqual(Object.keys(MODE_INFO), MODES, 'the Layout menu lists every mode, in order');
});

test('arrange is an object or nothing', () => {
  assert.deepEqual(arrangeOf({ arrange: { docWidth: 900 } }), { docWidth: 900 });
  for (const a of [undefined, null, [], 'x', 3]) assert.deepEqual(arrangeOf({ arrange: a }), {});
});

test('a copy carries the mode (none for the canvas) and the arrange (when any)', () => {
  assert.deepEqual(layoutFields({ mode: 'canvas', arrange: {} }), {});
  assert.deepEqual(layoutFields({ mode: 'bogus' }), {});
  assert.deepEqual(layoutFields({ mode: 'tiled', arrange: { tiling: { leaf: 'a' } } }), { mode: 'tiled', arrange: { tiling: { leaf: 'a' } } });
  assert.deepEqual(layoutFields({ arrange: { docWidth: 900 } }), { arrange: { docWidth: 900 } }, 'a canvas keeps its other modes\' arrangement');
});

test('Document mode\'s column limit: at least the minimum, none at the pane\'s width', () => {
  assert.equal(docWidth({}), 0);
  assert.equal(docWidth({ docWidth: 100 }), 0, 'below the minimum reads as none');
  assert.equal(docWidth({ docWidth: 900.4 }), 900);
  assert.deepEqual(withDocWidth({ tiling: 1 }, 900.6, 1400), { tiling: 1, docWidth: 901 });
  assert.deepEqual(withDocWidth({ docWidth: 900 }, 200, 1400), { docWidth: DOC_MIN_W });
  assert.deepEqual(withDocWidth({ docWidth: 900 }, 1390, 1400), {}, 'dragged out to the edge: no limit');
  assert.deepEqual(withDocWidth({ docWidth: 900, tiling: 1 }, 0), { tiling: 1 });
});
