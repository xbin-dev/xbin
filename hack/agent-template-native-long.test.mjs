// hack/agent-template-native-long.test.mjs — the agent template's native view
// on a long conversation (D130): read in pages, drawn as a window of its
// blocks that grows on `more` and is trimmed — and what lies far above let
// go — only while `scrolled` says the reader is at the bottom. Rendered in
// node like hack/agent-template-native.test.mjs (the web tests' fake backend
// through test/native-stub.mjs). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

async function run(seed, steps = [], { state = null } = {}) {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
// nodes of a tree by type and props (a subset, compared as JSON)
function nodes(root, m, out = []) {
  if (!root) return out;
  if ((!m.t || root.t === m.t) && Object.entries(m.p || {}).every(([k, v]) => JSON.stringify((root.p || {})[k]) === JSON.stringify(v))) out.push(root);
  for (const c of root.c || []) nodes(c, m, out);
  return out;
}
const find = (tree, m) => nodes(tree.root || tree, m)[0] || null;
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 600 + id, ...extra });
const oneSeed = (view, extra = {}) => ({ me: ME, runs: [{ id: 9, title: view.run.title || 'run 9', status: view.run.status }],
  views: { 9: { access: 'owner', ...view, run: { id: 9, rootId: 9, parentId: 0, ...view.run } } }, ...extra });
const TRANSCRIPT = { t: 'transcript', p: { follow: true } };

test('paging: a long conversation loads older pages as you scroll up', async () => {
  const oldest = { messages: [msg(30, 'assistant', 'early', { runId: 9 })], steps: [], links: [], messageFiles: {}, hasOlder: false, compacted: 3 };
  const r = await run(oneSeed({ run: { title: 'long', status: 'idle' }, messages: [msg(40, 'user', 'late', { runId: 9 })] },
    { pages: { 9: { hasOlder: true, nextBefore: 40, compacted: 3 } }, routes: [['GET', '/runs/9/view\\?limit=50&before=40$', oldest]] }), [
    { snapshot: 'newest' },
    { event: [TRANSCRIPT, 'more', {}] },
    { snapshot: 'all' },
  ], { state: { hash: 'c=9' } });
  const notice = { t: 'notice', p: { text: 'earlier turns were compacted into the summary' } };
  const tr = find(r.snapshots.newest, TRANSCRIPT);
  assert.equal(tr.p.older, true);
  assert.ok(tr.e.includes('more') && tr.e.includes('scrolled'), 'it loads more, and hears whether the reader is at the bottom');
  assert.ok(!find(tr, notice), 'older pages exist: the compacted line waits at the top');
  assert.equal(called(r, 'GET', /\/runs\/9\/view\?limit=50&before=40$/).length, 1);
  const whole = find(r.snapshots.all, TRANSCRIPT);
  assert.equal(whole.p.older, false);
  assert.ok(find(whole, notice), 'the top reached: the compacted line');
  assert.deepEqual(whole.c.filter((n) => n.t === 'message').map((n) => n.p.text), ['early', 'late'], 'the older page joins the window');
});

test('a long conversation: the chat renders a window, trimmed and let go only at the bottom', async () => {
  // 300 messages, all held at first (the stub answers a paged read with the whole view)
  const msgs = [];
  for (let i = 1; i <= 300; i++) msgs.push(msg(i, i % 2 ? 'user' : 'assistant', `m${i}`, { runId: 9 }));
  const r = await run(oneSeed({ run: { title: 'long', status: 'idle' }, messages: msgs }, { pages: { 9: { hasOlder: false } } }), [
    { snapshot: 'open' },
    // the page settles at the bottom: a paint there lets go of what lies far
    // above (whether one comes before the reader scrolls is the app's timing —
    // CI's dismissed cards read, a stream event — so the test waits for it)
    { wait: 100 },
    { event: [TRANSCRIPT, 'scrolled', { atBottom: false }] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { snapshot: 'up' },
    { event: [TRANSCRIPT, 'scrolled', { atBottom: true }] },
    { wait: 100 },
    { snapshot: 'back' },
    { event: [TRANSCRIPT, 'scrolled', { atBottom: false }] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
    { event: [TRANSCRIPT, 'more', {}] },
  ], { state: { hash: 'c=9' } });
  const rows = (snap) => find(r.snapshots[snap], TRANSCRIPT).c.filter((n) => n.t === 'message').map((n) => n.p.text);
  assert.equal(rows('open').length, 40, 'it opens on the tail');
  assert.equal(rows('open').at(-1), 'm300');
  const nums = (snap) => rows(snap).map((t) => +t.slice(1));
  assert.deepEqual(nums('up'), Array.from({ length: 140 }, (_, i) => 161 + i),
    'scrolled up, each `more` adds the held rows above (a page at most), then a page read again; nothing is trimmed');
  assert.equal(rows('back').length, 60, 'back at the bottom, the window is cut back');
  assert.equal(rows('back').at(-1), 'm300');
  assert.equal(find(r.snapshots.back, TRANSCRIPT).p.older, true, 'rows above the window: the loader stays');
  const older = called(r, 'GET', /\/runs\/9\/view\?limit=50&before=\d+$/);
  assert.equal(older.length, 2, 'what lay far above was let go at the bottom (once settled, once back): scrolling up reads it again each time');
  assert.ok(older.every((c) => +c.url.match(/before=(\d+)/)[1] > 150), older.map((c) => c.url).join(' '));
});
