// hack/frame-build.test.mjs — a <bx-frame>'s build events (web/frame-build.js,
// D196) and the retry of a failed start (D240), run by `make js-test`: the
// module is lit-free, so a plain object stands in for the frame.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { onBuildEvent } from '../web/frame-build.js';

const frame = () => ({ src: 'apps/x', deployment: '', _buildError: null, _building: false, _phase: '', _retry: null });
const ev = (type, extra = {}) => ({ type, component: 'apps/x', ...extra });
const retry = { attempt: 2, of: 3, at: '2026-11-16T10:42:13Z', inSec: 60 };

test('a backend-retry after a build-error keeps the frame failed and remembers the retry', () => {
  const f = frame();
  assert.equal(onBuildEvent(f, ev('build-error', { text: 'backend did not become healthy' })), true);
  assert.equal(onBuildEvent(f, ev('backend-retry', { data: retry })), true);
  assert.equal(f._buildError, 'backend did not become healthy');
  assert.deepEqual(f._retry, retry);
  // the retry's build starts: building, the retry gone
  onBuildEvent(f, ev('build-start'));
  assert.equal(f._building, true);
  assert.equal(f._retry, null);
  // a failure without a retry after it is plain failed
  onBuildEvent(f, ev('build-error', { text: 'x' }));
  assert.equal(f._retry, null);
});

test('a backend-retry while the frame is not failed changes nothing', () => {
  const f = frame();
  onBuildEvent(f, ev('build-ok'));
  onBuildEvent(f, ev('backend-retry', { data: retry }));
  assert.equal(f._retry, null);
});

test("another tile's or a deployment frame's backend-retry is not this frame's", () => {
  const f = frame();
  onBuildEvent(f, ev('build-error', { text: 'x' }));
  assert.equal(onBuildEvent(f, { type: 'backend-retry', component: 'apps/y', data: retry }), false);
  assert.equal(f._retry, null);
  const d = { ...frame(), deployment: 'dev', _buildError: 'x' };
  assert.equal(onBuildEvent(d, ev('backend-retry', { data: retry })), false);
});
