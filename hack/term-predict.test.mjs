// hack/term-predict.test.mjs — unit tests for the terminal's predictive echo
// engine (web/term-predict.js: mosh's PredictionEngine, experimental
// flavour), run by `make js-test`. A fake framebuffer plays the screen.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  Predictor, srttUpdate, wcwidth, SRTT_SHOW, SRTT_HIDE, SRTT_FLAG, GLITCH_THRESHOLD, GLITCH_REPAIR_COUNT, GLITCH_FLAG_THRESHOLD, MAX_CHUNK,
} from '../web/term-predict.js';

// A rows × cols screen of glyphs ('' = never written) with a cursor. `echo`
// is what a shell does with typed text: print it at the cursor and advance.
function screen(rows = 4, cols = 10) {
  const g = Array.from({ length: rows }, () => new Array(cols).fill(''));
  const wide = new Set();
  const fb = {
    rows, cols, cursor: { row: 0, col: 0 }, g,
    charAt: (r, c) => g[r]?.[c] ?? '',
    widthAt: (r, c) => (wide.has(`${r}:${c}`) ? 2 : 1),
    put(r, c, s) { [...s].forEach((ch, i) => { g[r][c + i] = ch; }); },
    echo(s) { for (const ch of s) { g[fb.cursor.row][fb.cursor.col] = ch; fb.cursor.col++; } },
    line: (r) => g[r].map((c) => c || ' ').join(''),
    markWide: (r, c) => wide.add(`${r}:${c}`),
  };
  return fb;
}
// A predictor in 'on' mode with the caller counting frames like bx-terminal does.
function rig(fb, mode = 'on') {
  const p = new Predictor(); p.setMode(mode);
  let seq = 0;
  const type = (s, now = 0) => { p.newUserData(s, fb, now); p.setLocalFrameSent(++seq); return seq; };
  const ack = (n, now = 0) => { p.setLateAck(n); p.cull(fb, now); };
  return { p, type, ack, seq: () => seq };
}
const texts = (p, fb) => p.render(fb).cells.map((c) => `${c.row}:${c.col}:${c.text}`);

test('constants and helpers', () => {
  assert.equal(SRTT_SHOW, 100); assert.equal(SRTT_HIDE, 60); assert.equal(SRTT_FLAG, 160);
  assert.equal(srttUpdate(null, 120), 120);
  assert.equal(srttUpdate(120, 200), 130);
  assert.equal(wcwidth('a'.codePointAt(0)), 1); assert.equal(wcwidth('ł'.codePointAt(0)), 1);
  assert.equal(wcwidth('漢'.codePointAt(0)), 2); assert.equal(wcwidth('🙂'.codePointAt(0)), 2);
  assert.equal(wcwidth(0x0301), 0);
});

test('typing predicts the glyph at the cursor and the cursor one cell right', () => {
  const fb = screen(); const { p, type } = rig(fb);
  type('a');
  const r = p.render(fb);
  assert.deepEqual(r.cells, [{ row: 0, col: 0, text: 'a', underline: false }]);
  assert.deepEqual(r.cursor, { row: 0, col: 1 });
  type('b');
  assert.deepEqual(texts(p, fb), ['0:0:ab'], 'consecutive predictions coalesce into one run');
  assert.deepEqual(p.render(fb).cursor, { row: 0, col: 2 });
});

test('an insert shifts the rest of the row right; the last column is unknown and not drawn', () => {
  const fb = screen(1, 6); fb.put(0, 0, 'abcdef'); fb.cursor.col = 2;
  const { p, type } = rig(fb);
  type('X');
  assert.deepEqual(texts(p, fb), ['0:2:Xcd'], 'X then the shifted c d; the last column is unknown (a wrap? a lost e?) and left alone');
});

test('backspace moves the cursor left and shifts the row left', () => {
  const fb = screen(1, 8); fb.put(0, 0, 'abcd'); fb.cursor.col = 4;
  const { p, type } = rig(fb);
  type('\x7f');
  const r = p.render(fb);
  assert.deepEqual(r.cursor, { row: 0, col: 3 });
  assert.deepEqual(r.cells, [{ row: 0, col: 3, text: ' ', underline: false }], 'the d is predicted gone');
  type('\x7f');
  assert.deepEqual(p.render(fb).cursor, { row: 0, col: 2 });
  assert.deepEqual(texts(p, fb), ['0:2:  ']);
  const fb2 = screen(); const two = rig(fb2);
  two.type('\x7f');
  assert.deepEqual(two.p.render(fb2).cursor, { row: 0, col: 0 }, 'backspace at column 0 moves nothing (the cursor prediction just pins the place)');
  assert.equal(two.p.pending(), 0);
});

test('Enter moves the cursor to the next row; on the bottom row it predicts that row blank', () => {
  const fb = screen(3, 6); fb.put(2, 0, 'prompt'); fb.cursor.row = 1; fb.cursor.col = 3;
  const { p, type } = rig(fb);
  type('\r');
  assert.deepEqual(p.render(fb).cursor, { row: 2, col: 0 });
  type('\r');
  assert.deepEqual(p.render(fb).cursor, { row: 2, col: 0 }, 'no scroll prediction');
  assert.deepEqual(texts(p, fb), ['2:0:      '], 'the bottom row is drawn blank');
  assert.equal(p.render(fb).cells[0].text.length, 6);
});

test('arrows move the predicted cursor, in normal and application mode; other escapes and pastes predict nothing', () => {
  const fb = screen(); fb.cursor.col = 3;
  const { p, type } = rig(fb);
  type('\x1b[C'); assert.deepEqual(p.render(fb).cursor, { row: 0, col: 4 });
  type('\x1bOD'); type('\x1b[D'); assert.deepEqual(p.render(fb).cursor, { row: 0, col: 2 });
  p.reset();
  type('\x1b[3~'); type('\x1b[1;5C'); type('\x1b[200~abc\x1b[201~');
  assert.equal(p.active(), false, 'delete, ctrl-arrow, bracketed paste');
  type('x'.repeat(MAX_CHUNK + 1));
  assert.equal(p.active(), false, 'a long chunk is a paste');
  type('\x03'); type('\n'); type('\t');
  assert.equal(p.active(), false, 'other controls');
});

test('wide characters predict no cell; a row with a wide glyph in the way predicts the cursor only', () => {
  const fb = screen(); const { p, type } = rig(fb);
  type('漢');
  assert.equal(p.active(), false);
  fb.markWide(0, 4);
  type('a');
  assert.deepEqual(p.render(fb).cells, [], 'no cell prediction: the shift would misplace the wide glyph');
  assert.deepEqual(p.render(fb).cursor, { row: 0, col: 1 });
});

test('a confirmed prediction is dropped once the ack covers it; a pending one stays', () => {
  const fb = screen(); const { p, type, ack } = rig(fb);
  const n = type('a');
  fb.echo('a');
  assert.deepEqual(texts(p, fb), [], 'the echo landed: nothing differs, nothing drawn');
  assert.ok(p.pending() > 0, 'but the prediction is still pending');
  ack(n - 1);
  assert.ok(p.pending() > 0, 'an older ack does not cover it');
  ack(n);
  assert.equal(p.pending(), 0);
  assert.equal(p.render(fb).cursor, null, 'the cursor prediction was confirmed and released');
});

test('a wrong prediction drops that cell only (experimental); its neighbours survive', () => {
  const fb = screen(1, 12); const { p, type, ack } = rig(fb);
  type('a'); type('b'); const n = type('c');
  fb.echo('aXc'); // the application printed X for b
  ack(n);
  assert.equal(p.pending(), 0, 'a and c confirmed, b incorrect — all three gone');
  fb.echo(''); fb.cursor.col = 3;
  type('d'); const m = type('e');
  fb.echo('d'); // e not answered yet within the ack window? the ack says it was — so e is wrong
  ack(m);
  assert.deepEqual(texts(p, fb), []);
  assert.equal(p.pending(), 0);
  // a mismatch on one of two cells with a later frame pending keeps the pending one
  const fb2 = screen(1, 12); const r2 = rig(fb2);
  const k1 = r2.type('p'); r2.type('q');
  fb2.echo('Z');
  r2.ack(k1);
  assert.ok(r2.p.pending() > 0, 'q (frame 2) is still pending');
  assert.deepEqual(texts(r2.p, fb2), ['0:1:q'], 'p was wrong and is gone; q still drawn');
});

test('the cursor prediction goes on a mismatch', () => {
  const fb = screen(); const { p, type, ack } = rig(fb);
  const n = type('a');
  fb.echo('a'); fb.cursor.col = 5; // the app moved the cursor elsewhere
  ack(n);
  assert.equal(p.render(fb).cursor, null);
});

test('a resize resets everything', () => {
  const fb = screen(); const { p, type } = rig(fb);
  type('abc');
  assert.ok(p.active());
  fb.cols = 20;
  p.cull(fb, 0);
  assert.equal(p.active(), false);
});

test('auto mode follows the RTT with hysteresis; off mode predicts nothing', () => {
  const fb = screen(); const { p, type } = rig(fb, 'auto');
  p.setSrtt(80); p.cull(fb, 0);
  assert.equal(p.shown(), false, 'below the threshold');
  p.setSrtt(120); p.cull(fb, 0);
  assert.equal(p.shown(), true, 'above 100 ms');
  p.setSrtt(80); p.cull(fb, 0);
  assert.equal(p.shown(), true, 'hysteresis: still on at 80');
  type('a');
  p.setSrtt(50); p.cull(fb, 0);
  assert.equal(p.shown(), true, 'a prediction on screen keeps it on even at 50');
  p.reset(); p.setSrtt(50); p.cull(fb, 0);
  assert.equal(p.shown(), false, 'nothing pending and ≤ 60: off');
  p.setMode('off'); type('a');
  assert.equal(p.active(), false); assert.equal(p.shown(), false);
  p.setMode('on'); assert.equal(p.shown(), true);
});

test('a prediction pending 250 ms is a glitch: shown on a fast link until ten quick confirmations', () => {
  const fb = screen(1, 40); const { p, type, ack } = rig(fb, 'auto');
  p.setSrtt(10);
  let now = 1000;
  const n = type('a', now);
  p.cull(fb, now + 100);
  assert.equal(p.shown(), false);
  p.cull(fb, now + GLITCH_THRESHOLD);
  assert.equal(p.shown(), true, 'glitch trigger set');
  assert.equal(p.glitchTrigger, GLITCH_REPAIR_COUNT);
  assert.equal(p.flagging, false, 'a plain glitch shows but does not underline');
  fb.echo('a'); ack(n, now + 300);
  // ten quick confirmations, at least 150 ms apart, cure it
  for (let i = 0; i < GLITCH_REPAIR_COUNT; i++) {
    now += 200;
    const k = type('b', now); fb.echo('b'); ack(k, now + 20);
  }
  assert.equal(p.glitchTrigger, 0);
  assert.equal(p.shown(), false);
  // a very long wait underlines too
  const m = type('c', now); p.cull(fb, now + GLITCH_FLAG_THRESHOLD);
  assert.equal(p.glitchTrigger, GLITCH_REPAIR_COUNT * 2);
  p.cull(fb, now + GLITCH_FLAG_THRESHOLD + 1); // the underline follows on the next cull, as in mosh
  assert.equal(p.flagging, true);
  assert.equal(p.render(fb).cells[0]?.underline, true);
  fb.echo('c'); ack(m, now + GLITCH_FLAG_THRESHOLD + 1);
});

test('flagging (underline) follows the RTT: on above 160 ms, off at 100', () => {
  const fb = screen(); const { p, type } = rig(fb, 'on');
  p.setSrtt(200); type('a');
  assert.equal(p.render(fb).cells[0].underline, true);
  p.setSrtt(120); p.cull(fb, 0);
  assert.equal(p.flagging, true, 'hysteresis');
  p.setSrtt(100); p.cull(fb, 0);
  assert.equal(p.flagging, false);
});

test('render skips what already matches the screen and never draws unknown cells', () => {
  const fb = screen(2, 4); fb.put(0, 0, 'ab'); fb.cursor.col = 2;
  const { p, type } = rig(fb);
  type('c'); type('d'); // d lands in the last column → wraps the predicted cursor
  const r = p.render(fb);
  assert.deepEqual(r.cells, [{ row: 0, col: 2, text: 'cd', underline: false }]);
  assert.deepEqual(r.cursor, { row: 1, col: 0 }, 'a character in the last column predicts a wrap');
  fb.put(0, 2, 'c');
  assert.deepEqual(texts(p, fb), ['0:3:d'], 'the confirmed-looking c is no longer drawn');
});

test('the caller can drive the ack out of order with the ECHO_TIMEOUT model: a restored original earns no credit', () => {
  const fb = screen(1, 6); fb.put(0, 0, 'a'); fb.cursor.col = 0;
  const { p, type, ack } = rig(fb);
  const n = type('a'); // predicts a over a: the screen already shows it
  ack(n);
  assert.equal(p.pending(), 0, 'dropped without credit');
});
