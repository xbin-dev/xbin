// hack/agent-template-harness-cards.test.mjs — a coding harness's
// transcript (D-harness §8 U3): the words the cards are drawn from
// (model/harness-heads.js, model/harness.js) and the native view's cards
// (native/harness-cards.js) over the harness fixtures — a toolcard per ACP
// family with its chips, a command's output and exit code, an edit's `diff`,
// a Claude Task's steps in a nested transcript, the toolbar's badge, the
// Progress screen (`plan`), one call in full, and the `harness` stream event.
// The web's cards are test/harness-cards.mjs (a browser). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const HH = await import(new URL('model/harness-heads.js', TPL));
const H = await import(new URL('model/harness.js', TPL));
const { outcome } = await import(new URL('model/tool-heads.js', TPL));
const { harnessSeed } = await import(new URL('test/harness-fixtures.mjs', TPL));

// --- the words ------------------------------------------------------------------------------

test('a call\'s status chip: parked, failed, cancelled, running, pending; none once done', () => {
  assert.deepEqual(HH.acpChip({ status: 'pending' }, 'approval'), { text: 'needs approval', tone: 'warn' });
  assert.deepEqual(HH.acpChip({ status: 'failed' }, 'error'), { text: 'failed', tone: 'bad' });
  assert.deepEqual(HH.acpChip({ status: 'cancelled' }, 'stopped'), { text: 'cancelled', tone: '' });
  assert.deepEqual(HH.acpChip({ status: 'in_progress' }, 'running'), { text: 'running', tone: 'run' });
  assert.deepEqual(HH.acpChip({ status: 'pending' }, 'running'), { text: 'pending', tone: '' });
  assert.deepEqual(HH.acpChip({}, 'writing'), { text: 'writing', tone: 'run' });
  assert.equal(HH.acpChip({ status: 'completed' }, 'done'), null);
  assert.deepEqual(outcome('acp:delete', 'deleted x', { status: 'completed', diffs: [{ path: 'x', status: 'deleted', del: 14 }] }), { text: '−14', tone: '' });
});

test('a subagent\'s steps, its task; what a call answered', () => {
  assert.equal(HH.isSubagentCall({ acp: { subagent: true } }), true);
  assert.equal(HH.isSubagentCall({ kids: [{ k: 'tool' }] }), true);
  assert.equal(HH.isSubagentCall({ acp: {} }), false);
  assert.equal(HH.stepsWords([{ k: 'assistant' }, { k: 'tool' }, { k: 'tool' }]), '2 steps');
  assert.equal(HH.stepsWords([{ k: 'tool' }]), '1 step');
  assert.equal(HH.stepsWords([]), '');
  assert.equal(HH.taskOf({ prompt: 'p', description: 'd' }), 'p');
  assert.equal(HH.taskOf({ description: 'd' }), 'd');
  assert.equal(HH.resultText({ result: '(running…)', state: 'running' }), '');
  assert.equal(HH.resultText({ result: 'ok', state: 'done' }), 'ok');
  assert.deepEqual(HH.placesOf({ locations: [{ path: 'a', line: 3 }, { path: 'b' }] }), ['a:3', 'b']);
  assert.deepEqual(HH.placesOf({ files: ['c'] }), ['c']);
});

test('an edit\'s diffs: per file, one patch for native, each line\'s class', () => {
  const acp = { diffs: [{ path: 'a.go', add: 2, del: 1, patch: '--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n-x\n+y\n+z\n ctx' },
    { path: 'b.go', status: 'added', add: 1, patch: '+++ b/b.go\n+new', truncated: true }, { path: 'c.go', status: 'deleted', del: 4, patch: '' }, { add: 9 }] };
  const files = HH.diffFiles(acp);
  assert.deepEqual(files.map((d) => [d.path, d.status, d.add, d.del, d.truncated]),
    [['a.go', 'modified', 2, 1, false], ['b.go', 'added', 1, 0, true], ['c.go', 'deleted', 0, 4, false]], 'a diff without a path is dropped');
  const all = HH.joinPatches(acp);
  assert.equal(all.left, 0);
  assert.equal(all.patch, acp.diffs[0].patch + '\n' + acp.diffs[1].patch + '\n');
  const cut = HH.joinPatches(acp, 60);
  assert.equal(cut.patch, acp.diffs[0].patch + '\n', 'whole files only');
  assert.equal(cut.left, 1);
  assert.equal(HH.joinPatches(acp, 5).patch, '', 'none fits');
  assert.deepEqual(HH.patchLines(acp.diffs[0].patch).map((l) => l.cls), ['fh', 'fh', 'h', 'a', 'd', 'd', 'ctx']);
  assert.deepEqual(HH.patchLines('--- not a header\n').map((l) => l.cls), ['a'], 'a deleted "-- " line is not a header');
});

test('the usage badge and the plan\'s entries', () => {
  assert.deepEqual(H.usageBadge({ used: 52000, size: 200000, cost: { amount: 0.41, currency: 'USD' } }),
    { text: 'ctx 26% · $0.41', head: 'ctx 26%', pct: 26, tone: '', title: '52 000 of 200 000 tokens of context · $0.41' });
  assert.equal(H.usageBadge({ used: 160000, size: 200000 }).tone, 'warn');
  assert.equal(H.usageBadge({ used: 190000, size: 200000 }).tone, 'bad');
  assert.deepEqual(H.usageBadge({ used: 12400 }), { text: '12k tokens', head: '12k tokens', pct: null, tone: '', title: '12 400 tokens of context' });
  assert.equal(H.usageBadge({ cost: { amount: 1.5, currency: 'EUR' } }).text, 'EUR 1.50');
  assert.equal(H.usageBadge({ cost: { amount: 1.5, currency: 'EUR' } }).head, 'EUR 1.50', 'the cost, when that is all it says');
  assert.equal(H.usageBadge({}), null);
  assert.equal(H.usageBadge(null), null);
  assert.deepEqual(H.planEntries(H.planOf({ plan: { entries: [{ content: 'a', status: 'completed' }, { content: 'b', status: 'in_progress' }, { content: 'c', status: 'odd' }] } })),
    [{ text: 'a', status: 'completed' }, { text: 'b', status: 'in_progress' }, { text: 'c', status: 'pending' }]);
  assert.equal(H.PLAN_MARK.in_progress, '◐');
});

// --- the native view --------------------------------------------------------------------------

async function run(steps, hash) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: Date.UTC(2026, 8, 30, 12), self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: harnessSeed() },
    steps, state: hash ? { hash } : null });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics: the vocabulary holds');
  return r;
}
function all(root, m, out = []) {
  if (!root) return out;
  if ((!m.t || root.t === m.t) && (m.has == null || JSON.stringify(root.p || {}).includes(m.has))) out.push(root);
  for (const c of root.c || []) all(c, m, out);
  return out;
}
const card = (t, title) => all(t, { t: 'toolcard' }).find((c) => c.p.title === title);
const kids = (n, t) => (n.c || []).filter((c) => !t || c.t === t);

test('native: a toolcard per ACP call — the command and output, an edit\'s diff, a Task\'s steps inside it', async () => {
  const r = await run([{ snapshot: 'chat' }], 'c=21');
  const t = r.snapshots.chat.root;
  const transcript = all(t, { t: 'transcript' })[0];
  assert.equal(kids(transcript, 'toolcard').length, 11, 'eleven calls at the top; the Task\'s steps are inside it');

  const ex = card(t, 'Run the flaky test 20 times');
  assert.equal(ex.p.icon, 'terminal');
  assert.deepEqual(ex.p.chips, [{ text: 'exit 1', tone: 'danger' }]);
  const codes = kids(ex, 'code').map((c) => c.p.text);
  assert.equal(codes[0], '$ go test ./... -run TestClientRetry -count=20');
  assert.ok(codes[1].startsWith('--- FAIL: TestClientRetry') && !codes[1].includes('\x1b'), 'the output, ANSI stripped');
  assert.ok(kids(ex, 'text').some((x) => JSON.stringify(x).includes('exit 1') && x.p.tone === 'danger'));

  const edit = card(t, 'Retry the request');
  assert.equal(edit.p.family, 'edit');
  const diff = kids(edit, 'diff')[0];
  assert.deepEqual(diff.p.files, [{ path: '/work/api/client.go', status: 'modified', add: 5, del: 1 }]);
  assert.match(diff.p.patch, /^--- a\/work\/api\/client\.go\n\+\+\+ b.*\n@@ -10,7 \+10,9 @@/);
  const del = card(t, 'Delete old_retry.go');
  assert.deepEqual(kids(del, 'diff')[0].p.files, [{ path: '/work/api/old_retry.go', status: 'deleted', add: 0, del: 14 }]);
  assert.deepEqual(del.p.chips, [{ text: '−14' }]);

  const task = card(t, 'Find every caller of Do');
  assert.equal(task.p.icon, 'sparkles');
  assert.deepEqual(task.p.chips, [{ text: '2 steps' }]);
  const inner = kids(task, 'transcript')[0];
  assert.deepEqual(kids(inner, 'toolcard').map((c) => c.p.title), ['Search for .Do(', 'Read sync.go'], 'its steps nest');
  assert.ok(kids(inner, 'message').some((m) => m.p.text === 'Looking for callers.'), 'its text too');
  assert.ok(kids(task, 'code').some((c) => c.p.text === 'Do is called from sync.go and fetch.go.'), 'its answer');
  assert.ok(kids(card(t, 'Read the net/http docs'), 'text').some((x) => JSON.stringify(x).includes('https://pkg.go.dev/net/http#Client.Do')));
  assert.equal(all(t, { t: 'plan' }).length, 0, 'the plan is not at the end: the end seam is a park\'s');
});

test('native: the toolbar badge, Progress (the plan) and the harness event', async () => {
  const seed = harnessSeed();
  const h = seed.runs.find((x) => x.id === 21).harness;
  const next = { ...h, usage: { used: 190000, size: 200000 },
    plan: { entries: [{ content: 'Find why', status: 'completed' }, { content: 'Fix it', status: 'in_progress' }, { content: 'Test it', status: 'pending' }] } };
  const r = await run([
    { snapshot: 'chat' },
    { tap: { t: 'button', has: 'Progress (3/3)' } }, { snapshot: 'progress' },
    { call: ['push', { type: 'harness', run: 21, root: 21, data: next }] }, { wait: 50 }, { snapshot: 'after' },
  ], 'c=21');
  const badge = all(r.snapshots.chat.root, { t: 'toolbar' })[0].c.find((c) => c.t === 'badge');
  assert.deepEqual(badge.p, { text: '📋 3/3 · ctx 26%', tone: 'muted' }, 'short: the cost is on Progress (a phone\'s bar keeps its title and ⋯)');
  const scr = all(r.snapshots.progress.root, { t: 'screen', has: 'Progress' }).pop();
  assert.equal(scr.p.subtitle, 'Claude Code · 3/3');
  assert.deepEqual(all(scr, { t: 'plan' })[0].p.entries.map((e) => e.status), ['completed', 'completed', 'completed']);
  assert.ok(all(scr, { t: 'step', has: '13 tool calls · 4 files +5 −15' }).length, 'what it changed');
  assert.ok(all(scr, { t: 'step', has: '52 000 of 200 000 tokens' }).length, 'the usage');
  const after = r.snapshots.after.root;
  const s2 = all(after, { t: 'screen', has: 'Progress' }).pop();
  assert.deepEqual(all(s2, { t: 'plan' })[0].p.entries, [{ text: 'Find why', status: 'completed' }, { text: 'Fix it', status: 'in_progress' }, { text: 'Test it', status: 'pending' }],
    'the harness event: the plan follows');
  assert.equal(s2.p.subtitle, 'Claude Code · 1/3 · now: Fix it');
});

test('native: one call in full (↗); a parked call; a failed one opens by itself', async () => {
  const msg = (data) => ({ call: ['push', { type: 'message', run: 21, root: 21, data: { runId: 21, created: 1790000000, ...data } }] });
  const r = await run([
    { event: [{ t: 'toolcard', p: { title: 'Run the flaky test 20 times' } }, 'open'] }, { snapshot: 'call' },
    msg({ id: 40, seq: 40, role: 'assistant', content: '', toolCalls: [{ id: 'h1:toolu_20', type: 'function', function: { name: 'acp:execute', arguments: '{"command":"make test"}' } }] }),
    msg({ id: 41, seq: 41, role: 'tool', toolCallId: 'h1:toolu_20', name: 'acp:execute', content: 'error: exit status 2',
      acp: { kind: 'execute', status: 'failed', exitCode: 2, output: 'FAIL pkg/c\n' } }),
    { wait: 50 }, { snapshot: 'failed' },
  ], 'c=21');
  const scr = all(r.snapshots.call.root, { t: 'screen', has: 'Run the flaky test 20 times' }).pop();
  assert.equal(scr.p.subtitle, 'acp:execute · Bash');
  assert.ok(all(scr, { t: 'code', has: 'got 503' }).length, 'the output');
  assert.ok(all(scr, { t: 'code', has: 'TestClientRetry -count=20' }).length >= 2, 'the command and the arguments');

  const p = await run([{ snapshot: 'p' }], 'c=22');
  const parked = card(p.snapshots.p.root, 'Run the tests');
  assert.deepEqual(parked.p.chips, [{ text: 'needs approval', tone: 'warn' }]);
  assert.equal(parked.p.state, 'running');

  const failed = card(r.snapshots.failed.root, '$ make test');
  assert.equal(failed.p.state, 'error');
  assert.equal(failed.p.open, true, 'a failed call opens by itself');
  assert.deepEqual(failed.p.chips, [{ text: 'failed', tone: 'danger' }]);
  assert.ok(all(failed, { t: 'text', has: 'exit 2' }).length);
});
