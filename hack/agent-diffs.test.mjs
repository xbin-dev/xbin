// hack/agent-diffs.test.mjs — web/agent-diffs.js (D210, #45), run by `make
// js-test`: the Agent tab's change sets reviewed a file at a time — a file's
// diff from the event's own patch when it holds all of it, else from the
// full-diff route while the session runs, else what the patch holds, marked
// cut; one load per file, the cap and its line counts.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { FileDiffs, inlinePart, diffURL, lineCount, headLines, diffBody, DIFF_LINES } from '../web/agent-diffs.js';
import { fileDiffs } from '../web/code-shared.js';

const A = 'diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n';
const B = 'diff --git a/b/big.txt b/b/big.txt\nnew file mode 100644\n--- /dev/null\n+++ b/b/big.txt\n@@ -0,0 +1,3 @@\n+1\n+2\n';
const files = (truncated) => ({ changes: [{ path: 'a.go', status: 'modified', add: 1, del: 1 }, { path: 'b/big.txt', status: 'added', add: 3, del: 0 }, { path: 'c.txt', status: 'added', add: 9, del: 0 }],
  patch: { format: 'git_patch', text: A + B, truncated } });

test('lineCount and headLines', () => {
  assert.equal(lineCount(''), 0);
  assert.equal(lineCount('a\nb\n'), 2);
  assert.equal(lineCount('a\nb'), 2);
  assert.equal(headLines('a\nb\nc\n', 2), 'a\nb\n');
  assert.equal(headLines('a\nb', 5), 'a\nb');
  assert.equal(DIFF_LINES, 400);
});

test('inlinePart: a file of the patch, cut when the patch stops inside it', () => {
  const parts = fileDiffs(A + B);
  assert.deepEqual(inlinePart(parts, false, 'a.go'), { text: A, cut: false });
  assert.equal(inlinePart(parts, true, 'a.go').cut, false, 'a part before the last is whole');
  assert.equal(inlinePart(parts, true, 'b/big.txt').cut, true, 'the last part of a truncated patch is cut');
  assert.equal(inlinePart(parts, true, 'c.txt'), null, 'past the cap');
});

test('diffURL names the change set and the file', () => {
  assert.equal(diffURL('s 1', { turn: 3 }, 'b/c d.txt'), '/api/xbin/term/sessions/s%201/diff?turn=3&path=b%2Fc+d.txt');
  assert.equal(diffURL('s1', { toolCallId: 't/1' }, 'x'), '/api/xbin/term/sessions/s1/diff?toolCallId=t%2F1&path=x');
});

const fake = (answers) => {
  const asked = [];
  const fetch = async (u) => { asked.push(u); const a = answers(u); return { ok: a.status === 200, status: a.status, text: async () => a.body || '', headers: { get: (h) => (a.headers || {})[h] ?? null } }; };
  return { asked, fetch };
};
const tick = () => new Promise((r) => setTimeout(r, 0));

test('a whole inline part answers without a request', () => {
  const f = fake(() => ({ status: 500 }));
  const d = new FileDiffs({ fetch: f.fetch });
  const owner = { key: 7 };
  const e = d.get({ session: 's', live: true }, owner, files(false), files(false).changes[0], { turn: 1 });
  assert.deepEqual(e, { state: 'ok', text: A, cut: false, from: 'inline' });
  assert.equal(f.asked.length, 0);
});

test('past the patch: the route while the session runs, once; onLoad re-renders the owner', async () => {
  const full = 'diff --git a/c.txt b/c.txt\n+all of it\n';
  const f = fake((u) => (u.includes('path=c.txt') ? { status: 200, body: full, headers: { 'X-Truncated': 'true' } } : { status: 404 }));
  const landed = [];
  const d = new FileDiffs({ fetch: f.fetch, onLoad: (b) => landed.push(b) });
  const owner = { key: 9 }, fs = files(true);
  assert.deepEqual(d.get({ session: 's', live: true }, owner, fs, fs.changes[2], { turn: 2 }), { state: 'loading' });
  assert.deepEqual(d.get({ session: 's', live: true }, owner, fs, fs.changes[2], { turn: 2 }), { state: 'loading' }, 'asked once');
  await tick(); await tick();
  assert.deepEqual(landed, [owner]);
  assert.deepEqual(d.get({ session: 's', live: true }, owner, fs, fs.changes[2], { turn: 2 }), { state: 'ok', text: full, cut: true, from: 'server' });
  assert.deepEqual(f.asked, ['/api/xbin/term/sessions/s/diff?turn=2&path=c.txt']);
  assert.deepEqual(d.loads, f.asked);
});

test('the patch cut inside a file: the route for all of it, else the part marked cut', async () => {
  const f = fake(() => ({ status: 404 })); // the session ended meanwhile
  const d = new FileDiffs({ fetch: f.fetch });
  const owner = { key: 1 }, fs = files(true);
  assert.equal(d.get({ session: 's', live: true }, owner, fs, fs.changes[1], { toolCallId: 't1' }).state, 'loading');
  await tick(); await tick();
  assert.deepEqual(d.get({ session: 's', live: true }, owner, fs, fs.changes[1], { toolCallId: 't1' }), { state: 'ok', text: B, cut: true, from: 'inline' });
  assert.match(f.asked[0], /toolCallId=t1&path=b%2Fbig\.txt$/);
});

test('history or an ended session: no request, what the event holds', () => {
  const f = fake(() => ({ status: 200 }));
  const d = new FileDiffs({ fetch: f.fetch });
  const owner = { key: 1 }, fs = files(true);
  assert.equal(d.get({ session: '', live: false }, owner, fs, fs.changes[1], { turn: 1 }).cut, true);
  const none = d.get({ session: 's', live: false }, owner, fs, fs.changes[2], { turn: 1 });
  assert.equal(none.state, 'none');
  assert.match(none.note, /no longer has it/);
  assert.equal(f.asked.length, 0);
});

test('entries are kept per session, block and file, the most recent max of them', () => {
  const d = new FileDiffs({ fetch: async () => ({ ok: false, status: 404 }), max: 2 });
  const fs = files(false);
  const e1 = d.get({ session: 's', live: true }, { key: 1 }, fs, fs.changes[0], { turn: 1 });
  e1.$html = { n: 1 }; // what a card memoizes on it
  assert.equal(d.get({ session: 's', live: true }, { key: 1 }, fs, fs.changes[0], { turn: 1 }), e1, 'the same entry: its memo lives');
  d.get({ session: 's', live: true }, { key: 2 }, fs, fs.changes[0], { turn: 1 });
  d.get({ session: 't', live: true }, { key: 1 }, fs, fs.changes[0], { turn: 1 });
  assert.notEqual(d.get({ session: 's', live: true }, { key: 1 }, fs, fs.changes[0], { turn: 1 }), e1, 'dropped past max');
});

test('diffBody: a file\'s hunks without git\'s headers; a rename, a mode or a binary stays', () => {
  assert.equal(diffBody(A), '@@ -1 +1 @@\n-x\n+y\n');
  assert.equal(diffBody('diff --git a/x b/y\nsimilarity index 90%\nrename from x\nrename to y\n@@ -1 +1 @@\n-a\n+b\n'), 'rename from x\nrename to y\n@@ -1 +1 @@\n-a\n+b\n');
  assert.equal(diffBody('diff --git a/p.png b/p.png\nindex 1..2\nBinary files a/p.png and b/p.png differ\n'), 'Binary files a/p.png and b/p.png differ\n');
  assert.equal(diffBody('diff --git a/s b/s\nold mode 100644\nnew mode 100755\n'), 'old mode 100644\nnew mode 100755\n');
});
