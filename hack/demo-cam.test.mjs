// hack/demo-cam.test.mjs — the promo camera's pure parts (hack/demo/cam):
// the motion math (human mouse paths, typing rhythm) and the frame-counter
// pattern + capture analysis (framecheck). The browser and ffmpeg ends have
// their own end-to-end checks (framecheck/selftest.js, README).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const M = require('./demo/cam/motion.js');
const P = require('./demo/cam/framecheck/pattern.js');
const { analyze } = require('./demo/cam/framecheck/check.js');

test('rng: a seed replays, another seed does not', () => {
  const a = M.rng('shot:1'), b = M.rng('shot:1'), c = M.rng('shot:2');
  const xs = Array.from({ length: 8 }, () => a());
  assert.deepEqual(Array.from({ length: 8 }, () => b()), xs);
  assert.notDeepEqual(Array.from({ length: 8 }, () => c()), xs);
  for (const x of xs) assert.ok(x >= 0 && x < 1);
});

test('plan: a move starts and ends where asked, eases in and out, and takes a human time', () => {
  const r = M.rng('plan');
  for (const [from, to, width] of [[[100, 100], [140, 120], 30], [[1800, 900], [300, 200], 24], [[0, 0], [900, 40], 600]]) {
    const segs = M.plan(from, to, { r, width });
    const dur = M.duration(segs);
    assert.deepEqual(M.pointAt(segs, 0).map(Math.round), from);
    assert.deepEqual(M.pointAt(segs, dur).map((v) => Math.round(v)), to);
    assert.ok(dur >= 240 && dur <= 1400, `duration ${dur}`);
    // continuous across segments
    for (let i = 1; i < segs.length; i++) assert.deepEqual(segs[i].p0, segs[i - 1].p1);
    // slow at both ends (minimum jerk), fastest in the middle
    const step = (t) => Math.hypot(...M.pointAt(segs, t + 8).map((v, k) => v - M.pointAt(segs, t)[k]));
    assert.ok(step(0) < step(segs[0].dur / 2 - 4) / 4, 'eases in');
  }
  assert.equal(M.plan([5, 5], [5.2, 5.1]).length, 0, 'no move for a sub-pixel distance');
  assert.equal(M.plan([0, 0], [600, 0], { r }).length, 2, 'a long throw is a primary move and a correction');
});

test('aimPoint: inside the box, near the middle', () => {
  const r = M.rng('aim');
  for (let i = 0; i < 200; i++) {
    const [x, y] = M.aimPoint({ x: 10, y: 20, width: 80, height: 24 }, r);
    assert.ok(x > 10 + 80 * 0.25 && x < 10 + 80 * 0.75 && y > 20 + 24 * 0.25 && y < 20 + 24 * 0.75, `${x},${y}`);
  }
});

test('typingPlan: ~10 chars/s on average, with jitter and longer beats where people pause', () => {
  const text = 'Which of our services had errors overnight? Summarize them.\n';
  const plan = M.typingPlan(text, { cps: 10, r: M.rng('t') });
  assert.equal(plan.length, [...text].length);
  assert.equal(plan[0].at, 0);
  const total = plan[plan.length - 1].at;
  assert.ok(Math.abs(total - (plan.length - 1) * 100) <= 2, `total ${total}`);
  const gaps = plan.slice(1).map((k, i) => k.at - plan[i].at);
  assert.ok(gaps.every((g) => g > 0), 'strictly increasing');
  assert.ok(new Set(gaps).size > 10, 'not metronomic');
  const enter = gaps[gaps.length - 1], median = [...gaps].sort((a, b) => a - b)[gaps.length >> 1];
  assert.ok(enter > median * 1.5, `a beat before Enter (${enter} vs ${median})`);
});

test('pattern: every counter decodes back; a torn frame fails its CRC; no pattern is blank', () => {
  const levels = (bits, noise = 0) => bits.map((b, i) => (b ? 230 : 18) + ((i * 37) % 11 - 5) * noise);
  for (let n = 0; n < 5000; n += 7) assert.deepEqual(P.decode(levels(P.bits(n), 3)), { status: 'ok', value: n });
  let caught = 0, tries = 0;
  for (let n = 1000; n < 3000; n++) {
    const a = P.bits(n), b = P.bits(n + 1);
    if (a.slice(0, 16).join() === b.slice(0, 16).join()) continue;
    // top two rows from frame n, bottom two from n + 1: a capture mid-update
    const torn = [...a.slice(0, 16), ...b.slice(16)];
    tries++;
    if (P.decode(levels(torn)).status === 'torn') caught++;
  }
  assert.ok(caught / tries > 0.95, `torn caught ${caught}/${tries}`);
  assert.equal(P.decode(new Array(32).fill(16)).status, 'blank');
});

test('analyze: repeats, skips, torn frames and a lead-in are counted exactly', () => {
  const ok = (v) => ({ status: 'ok', value: v });
  const seq = [
    { status: 'blank' }, { status: 'blank' }, // lead-in: not a fault
    ok(10), ok(11), ok(11), ok(11), ok(12), // a frame held for 3
    ok(14), // one dropped (13)
    { status: 'torn' }, ok(16), // torn between 14 and 16: no skip
    ok(20), // 3 dropped (17–19)
    ok(21),
  ];
  const r = analyze(seq, 60);
  assert.equal(r.leadingBlank, 2);
  assert.equal(r.dups, 2);
  assert.equal(r.longestHold, 3);
  assert.equal(r.dropped, 4);
  assert.equal(r.dropEvents, 2);
  assert.equal(r.torn, 1);
  assert.equal(r.perfect, false);
  const clean = analyze([ok(5), ok(6), ok(7), ok(8)], 60);
  assert.equal(clean.perfect, true);
  assert.equal(clean.unique, 4);
});
