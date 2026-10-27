// hack/code-panels.test.mjs — web/code-shared.js, the rules the tile
// window's code panels (D202: code-tree.js, code-view.js) and <bx-code> share,
// run by `make js-test`: which events mean a tile's work tree changed, a
// working-tree diff split per file (status, counts, its own text), the
// commit-time words, the activity summary, a commit found by a prefix.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { savedEvent, fileDiffs, statusMap, relTime, activitySummary, commitOf } from '../web/code-shared.js';

const DIFF = [
  'diff --git a/backend/main.go b/backend/main.go',
  'index 1111111..2222222 100644',
  '--- a/backend/main.go',
  '+++ b/backend/main.go',
  '@@ -1,3 +1,4 @@',
  ' package main',
  '+import "os"',
  '-// old',
  '+// new',
  'diff --git a/notes.md b/notes.md',
  'new file mode 100644',
  'index 0000000..3333333',
  '--- /dev/null',
  '+++ b/notes.md',
  '@@ -0,0 +1,2 @@',
  '+# Notes',
  '+++ a line that starts with two pluses',
  'diff --git a/gone.txt b/gone.txt',
  'deleted file mode 100644',
  '--- a/gone.txt',
  '+++ /dev/null',
  '@@ -1 +0,0 @@',
  '-bye',
  'diff --git a/old.js b/new.js',
  'similarity index 90%',
  'rename from old.js',
  'rename to new.js',
  '',
].join('\n');

test('a save, a build, or the paused tile’s deployments event is a work-tree change — for the tile or a path inside it', () => {
  assert.ok(savedEvent({ type: 'reload', component: 'apps/x' }, 'apps/x'));
  assert.ok(savedEvent({ type: 'build-ok', component: 'apps/x/sub' }, 'apps/x'));
  assert.ok(savedEvent({ type: 'deployments', component: 'apps/x', data: { op: 'work-tree' } }, 'apps/x'));
  assert.ok(savedEvent({ type: 'deployments', component: 'apps/x', data: { op: 'build', phase: 'ok' } }, 'apps/x'));
  assert.ok(!savedEvent({ type: 'deployments', component: 'apps/x', data: { op: 'build', phase: 'start' } }, 'apps/x'));
  assert.ok(!savedEvent({ type: 'reload', component: 'apps/xy' }, 'apps/x'), 'a sibling with the same prefix is not the tile');
  assert.ok(!savedEvent({ type: 'reload', component: 'apps/x' }, ''), 'no tile: nothing');
  assert.ok(!savedEvent({ type: 'status', component: 'apps/x' }, 'apps/x'));
});

test('a working-tree diff splits per file: path, status, counts, its own text', () => {
  const f = fileDiffs(DIFF);
  assert.deepEqual(f.map((x) => [x.path, x.status, x.add, x.del]), [
    ['backend/main.go', 'M', 2, 1], ['notes.md', 'A', 1, 0], ['gone.txt', 'D', 0, 1], ['new.js', 'R', 0, 0]]);
  assert.ok(f[0].text.startsWith('diff --git a/backend/main.go') && f[0].text.includes('+// new') && !f[0].text.includes('notes.md'));
  assert.ok(f[1].text.endsWith('two pluses\n'), 'a section keeps its own lines, ending in one newline');
  assert.equal(f[3].from, 'old.js');
  assert.deepEqual(statusMap(DIFF), { 'backend/main.go': 'M', 'notes.md': 'A', 'gone.txt': 'D', 'new.js': 'R' });
  assert.deepEqual(fileDiffs(''), []);
  assert.deepEqual(fileDiffs(undefined), []);
});

test('commit times read as words', () => {
  const now = 1_800_000_000;
  assert.equal(relTime(0, now), '—');
  assert.equal(relTime(now - 30, now), '1m ago');
  assert.equal(relTime(now - 7200, now), '2h ago');
  assert.equal(relTime(now - 3 * 86400, now), '3d ago');
  assert.equal(relTime(now - 400 * 86400, now), '1y ago');
});

test('the activity summary: totals, authors, 52 weekly buckets (the last one this week)', () => {
  const now = 1_800_000_000, day = 86400;
  const a = { repo: true, local: [{ t: now - day, a: 'ana' }, { t: now - 2 * day, a: 'ana' }, { t: now - 40 * day, a: 'bo' }], upstream: [{ t: now - day }] };
  const s = activitySummary(a, now);
  assert.equal(s.total, 3);
  assert.equal(s.last30, 2);
  assert.deepEqual(s.topAuthors, [['ana', 2], ['bo', 1]]);
  assert.equal(s.weeksLocal.length, 52);
  assert.equal(s.weeksLocal[51], 2);
  assert.equal(s.weeksUp[51], 1);
  assert.equal(s.upstreamTotal, 1);
  assert.equal(activitySummary({ repo: false }), null);
  assert.equal(activitySummary(null), null);
});

test('a commit is found by its hash or a prefix of it', () => {
  const log = { commits: [{ hash: 'abcdef0123456789', short: 'abcdef0', subject: 'first' }, { hash: '0123456789abcdef', short: '0123456', subject: 'second' }] };
  assert.equal(commitOf(log, 'abcdef0123456789').subject, 'first');
  assert.equal(commitOf(log, '0123456').subject, 'second');
  assert.equal(commitOf(log, 'ffff'), null);
  assert.equal(commitOf(log, ''), null);
  assert.equal(commitOf(null, 'abc'), null);
});
