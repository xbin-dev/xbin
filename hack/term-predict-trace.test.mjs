// hack/term-predict-trace.test.mjs — keeps the Swift port of the predictive
// echo engine in step with web/term-predict.js (make js-test). The committed
// differential trace (hack/term-predict-trace.mjs) must be exactly what the
// JS engine produces today, and must replay from its on-disk form alone —
// which is what native/ios/Packages/XbinTerm's PredictorTraceTests does.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { Predictor } from '../web/term-predict.js';
import { TRACE_PATH, generate, serialize, apply, observe, expand, Screen } from './term-predict-trace.mjs';

test('the committed trace is what web/term-predict.js produces', () => {
  const committed = readFileSync(TRACE_PATH, 'utf8');
  assert.ok(committed === serialize(generate()),
    'web/term-predict.js changed: run `node hack/term-predict-trace.mjs`, port the change to ' +
    'native/ios/Packages/XbinTerm/Sources/XbinTerm/Predictor.swift, and run its swift test');
});

test('the trace replays from its on-disk form', () => {
  const t = JSON.parse(readFileSync(TRACE_PATH, 'utf8'));
  let steps = 0;
  for (const s of t.sessions) {
    const fb = new Screen(s.rows, s.cols), p = new Predictor(), st = { seq: 0 };
    for (const c of s.cells) fb.put(...c);
    let want = null;
    for (const [op, w] of s.steps) {
      apply(expand(op), p, fb, st);
      if (w !== null) want = w;
      assert.equal(observe(p, fb), want, `step ${steps}: ${JSON.stringify(op)}`);
      steps++;
    }
  }
  assert.ok(steps > 3000, `the trace has ${steps} steps`);
});
