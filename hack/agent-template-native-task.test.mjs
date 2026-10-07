// hack/agent-template-native-task.test.mjs — the agent template's native
// view: the pinned task (D133), beside agent-template-native.test.mjs (at
// its size cap); run like it, through hack/xbn/node.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

async function run(seed, steps = [], { state = null, data = {} } = {}) {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed, ...data }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

// find/all: nodes of a tree by type and props (a subset, compared as JSON),
// optionally inside an ancestor that matches `in`.
function all(root, m, out = [], inside = !m.in) {
  if (!root) return out;
  const hit = (n, q) => (!q.t || n.t === q.t) && Object.entries(q.p || {}).every(([k, v]) => JSON.stringify((n.p || {})[k]) === JSON.stringify(v))
    && (q.has == null || JSON.stringify(n.p || {}).includes(q.has));
  if (inside && hit(root, m)) out.push(root);
  const deeper = inside || hit(root, m.in);
  for (const c of root.c || []) all(c, m, out, deeper);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const oneSeed = (view, extra = {}) => ({ me: ME, runs: [{ id: 9, title: view.run.title || 'run 9', status: view.run.status }],
  views: { 9: { access: 'owner', ...view, run: { id: 9, rootId: 9, parentId: 0, ...view.run } } }, ...extra });

test('the pinned task (D133): Task in the menu opens every request, read-only', async () => {
  const task = { count: 2, first: { seq: 1, source: 'human', who: 'admin', text: 'reconcile the ledger\nby Friday' },
    latest: { seq: 7, source: 'schedule', who: 'nudge', text: 'check again' } };
  const r = await run(oneSeed({ run: { title: 'ledger', status: 'idle', task } }, { routes: [['GET', '/runs/9/asks$', { asks: [
    { id: 1, seq: 1, source: 'human', who: 'admin', text: 'reconcile the ledger\nby Friday', at: 1700000000, live: false },
    { id: 2, seq: 7, source: 'schedule', who: 'nudge', text: 'check again', at: 1700000100, live: true }] }]] }), [
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Task (+1)' }, in: { t: 'menu' } } },
    { snapshot: 'task' },
  ], { state: { hash: 'c=9' } });
  const nav = all(r.snapshots.chat.root, { t: 'nav' })[0];
  const menu = all(nav.c[nav.c.length - 1], { t: 'button', in: { t: 'menu', p: { icon: 'ellipsis' } } }).map((b) => b.p.label);
  assert.deepEqual(menu.slice(0, 5), ['Rename…', 'Compact', 'Learn skill', 'Task (+1)', 'Memory (0)']);
  const screen = topScreen(r.snapshots.task);
  assert.equal(screen.p.title, 'Task');
  assert.deepEqual(all(screen, { t: 'section' }).map((x) => x.p.title), ['The request · #1 · admin', 'Then · #7 · schedule nudge']);
  assert.equal(find(screen, { t: 'text' }).p.text, 'reconcile the ledger\nby Friday', 'verbatim');
  assert.match(all(screen, { t: 'section' })[0].p.footer, /compacted — the agent sees it pinned$/);
  assert.equal(find(screen, { t: 'field' }), null, 'nothing to edit');
  assert.equal(called(r, 'GET', /\/runs\/9\/asks$/).length, 1);
});
