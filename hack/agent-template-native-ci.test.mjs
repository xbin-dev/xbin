// hack/agent-template-native-ci.test.mjs — CI in the conversation in the
// native view (builtin-templates/agent/native/ci.js; API.md §CI in the
// conversation), run through hack/xbn/node.mjs over kidsSeed()'s #25 with
// CI (test/ci-stub.mjs ciRoutes): the Coding agents toolbar button carrying
// CI's badge, the CI section of the Coding agents screen (a watch, its run,
// jobs with progress, a check, a status, Watch CI for…), the coding agent
// card's CI words, the ci-job screen (its steps; a running job's "ready when
// it finishes" and Open live log; a finished one's log as code, searched),
// the annotations screen, outcome cards, Watch CI for…, and the project
// board's CI words (ext.card). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav
import { installHooks } from './xbn/hooks.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const { kidsSeed, NOW } = await import(new URL('test/harness-fixtures.mjs', TPL));
const { ciRoutes, ciView } = await import(new URL('test/ci-stub.mjs', TPL));

function ciSeed(opts = {}) {
  const s = kidsSeed();
  const v = ciView(25, { run: 26, ...opts });
  s.views[25].ci = { summary: v.summary, canWatch: true };
  s.routes = [...(s.routes || []), ...ciRoutes(25, { run: 26, ...opts })];
  return s;
}

async function run(steps, seed = ciSeed()) {
  const r = await runNative({ caps: PHONE, entry: new URL('native.js', TPL).pathname,
    data: { now: NOW, self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed, calls: { open: null } },
    steps, state: { hash: 'c=25' } });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
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
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const BOARD = { t: 'screen', p: { title: 'Coding agents' } };
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const barBtn = { t: 'button', has: 'coding agents need you' };

test('native CI: the toolbar badge, the Coding agents screen\'s CI section, the child\'s CI words', async () => {
  const r = await run([
    { wait: 50 },
    { snapshot: 'chat' },
    { tap: barBtn }, { wait: 50 },
    { snapshot: 'board' },
  ]);
  const chat = r.snapshots.chat.root;
  const bar = find(chat, barBtn);
  assert.equal(bar.p.label, '2 coding agents need you · CI failed: codecov/patch', 'CI\'s badge on the Coding agents button');
  assert.equal(called(r, 'GET', /\/runs\/25\/ci$/).length >= 1, true, 'read once when the conversation opens');
  const kid = find(chat, { t: 'toolcard', p: { title: 'Split the router' } });
  assert.ok(find(kid, { t: 'text', has: 'CI failed — CI failed on feature' }), 'the coding agent that pushed: its CI words');
  assert.equal(find(find(chat, { t: 'toolcard', p: { title: 'Write the changelog' } }), { t: 'text', has: 'CI failed' }), null, 'the others: none');
  const b = find(r.snapshots.board.root, BOARD);
  const titles = all(b, { t: 'section' }).map((s) => s.p.title);
  assert.deepEqual(titles.slice(-2), ['CI · acme/web · feature', ''], 'after the coding agents: CI, and Watch CI for…');
  const sec = find(b, { t: 'section', p: { title: 'CI · acme/web · feature' } });
  assert.equal(sec.p.badge, 'failure');
  const rows = all(sec, { t: 'row' }).map((x) => [x.p.title, x.p.detail || '', x.p.icon || '', x.p.tone || '']);
  assert.deepEqual(rows, [['acme/web · feature', '', 'branch', 'danger'], ['ci', '', 'clock', 'accent'], ['lint', '1/1', 'check', 'ok'], ['test (ubuntu)', '1/3', 'clock', 'accent'],
    ['build', '', '', 'muted'], ['codecov/patch', '2 annotations', 'error', 'danger'], ['ci/jenkins', '', 'check', 'ok']], 'a status: the row\'s icon and tone (D184)');
  const job = find(sec, { t: 'row', p: { title: 'test (ubuntu)' } });
  assert.equal(job.p.subtitle.startsWith('go test ./...'), true, job.p.subtitle);
  assert.equal(find(job, { t: 'progress' }).p.value, 0.33);
  const w = find(sec, { t: 'row', p: { title: 'acme/web · feature' } });
  assert.deepEqual(all(w, { t: 'button' }).map((x) => x.p.label), ['Open the branch', 'Open PR #42', 'Stop watching']);
  assert.equal(all(find(sec, { t: 'row', p: { title: 'ci' } }), { t: 'button' }).map((x) => x.p.label).includes('Re-run failed'), false, 'a run still going: no Re-run');
  assert.ok(find(b, { t: 'row', p: { title: 'Watch CI for…' } }));
  assert.equal(called(r, 'GET', /\/runs\/25\/ci\?fresh=1$/).length, 1, 'the screen shown: a fresh read (live)');
});

test('native CI: back to the list stops the live reads the Coding agents screen started', async () => {
  const r = await run([
    { wait: 50 },
    { tap: barBtn }, { wait: 50 },
    { wait: 16000 },
    { snapshot: 'live' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] }, { wait: 50 },
    { snapshot: 'home' },
    { wait: 61000 },
  ]);
  const fresh = () => called(r, 'GET', /\/runs\/25\/ci\?fresh=1$/).length;
  assert.ok(fresh() >= 2, `live while the screen is shown: ${fresh()}`);
  assert.equal(find(r.snapshots.home, BOARD), null, 'on the list');
  assert.ok(fresh() <= 3, `no fresh reads in a minute on the list (every 15 s, they'd be 6 or more): ${fresh()}`);
});

test('native CI: following a log stops when a read fails, as on the web', async () => {
  const LOG = '/runs/25/ci/jobs/88001/log\\?';
  const r = await run([
    { call: ['route', 'GET', LOG, { text: 'step 1\n', bytes: 7, from: 0, complete: false, truncated: false, url: '' }] },
    { wait: 50 }, { tap: barBtn }, { wait: 50 },
    { tap: { t: 'row', p: { title: 'test (ubuntu)' }, in: BOARD } }, { wait: 50 },
    { tap: { t: 'button', p: { label: 'Follow' } } },
    { call: ['route', 'GET', LOG, { error: 'upstream down' }, 502] },
    { wait: 30000 },
    { snapshot: 'after' },
  ]);
  const follows = called(r, 'GET', /\/ci\/jobs\/88001\/log\?.*since=/).length;
  assert.equal(follows, 1, `one failed read, then no more: ${follows}`);
  assert.ok(find(topScreen(r.snapshots.after.root), { t: 'button', p: { label: 'Follow' } }), 'Follow is off again');
});

test('native CI: a running job (logs once it ends), then a finished one\'s log searched; annotations', async () => {
  const r = await run([
    { wait: 50 }, { tap: barBtn }, { wait: 50 },
    { tap: { t: 'row', p: { title: 'test (ubuntu)' }, in: BOARD } }, { wait: 50 },
    { snapshot: 'job' },
    { tap: { t: 'row', p: { title: 'Open live log' } } }, { wait: 20 },
  ]);
  const job = topScreen(r.snapshots.job.root);
  assert.equal(job.p.title, 'test (ubuntu)');
  assert.deepEqual(all(find(job, { t: 'section', p: { title: 'Steps' } }), { t: 'row' }).map((x) => [x.p.title, x.p.icon || '']),
    [['Set up job', 'check'], ['go test ./...', 'clock'], ['upload', '']]);
  assert.ok(find(job, { t: 'notice', p: { text: 'The job is still running: the log is ready when the job finishes.' } }));
  const opened = r.messages.filter((m) => m.op === 'call' && m.what === 'open').map((m) => m.args.url);
  assert.deepEqual(opened, ['https://github.com/acme/web/actions/runs/7001/job/88001'], 'Open live log: the app opens it');

  const done = await run([
    { wait: 50 }, { tap: barBtn }, { wait: 50 },
    { tap: { t: 'row', p: { title: 'test (ubuntu)' }, in: BOARD } }, { wait: 50 },
    { snapshot: 'log' },
    { event: [{ t: 'screen', p: { title: 'test (ubuntu)' } }, 'search', { value: 'TestLogin' }] }, { wait: 20 },
    { snapshot: 'search' },
  ], ciSeed({ failed: true }));
  const lg = topScreen(done.snapshots.log.root);
  const code = find(find(lg, { t: 'section', p: { title: 'Log' } }), { t: 'code' });
  assert.match(code.p.text, /--- FAIL: TestLogin/);
  assert.equal(code.p.text.includes('ghs_A'), false, 'the token masked');
  assert.equal(find(lg, { t: 'button', p: { label: 'Follow' } }), null, 'a finished job: no Follow');
  const m = find(topScreen(done.snapshots.search.root), { t: 'section', p: { title: 'Matches (1)' } });
  assert.match(find(m, { t: 'code' }).p.text, /^\d+: --- FAIL: TestLogin/);

  const notes = await run([
    { wait: 50 }, { tap: barBtn }, { wait: 50 },
    { tap: { t: 'row', p: { title: 'codecov/patch' }, in: BOARD } }, { wait: 50 },
    { snapshot: 'notes' },
  ]);
  const ns = topScreen(notes.snapshots.notes.root);
  assert.equal(ns.p.title, 'Annotations');
  assert.deepEqual(all(ns, { t: 'row' }).map((x) => [x.p.title, x.p.detail, x.p.tone]), [['auth/login.go:12', 'failure', 'danger'], ['auth/session.go:3', 'warning', 'warn']]);
  assert.equal(all(ns, { t: 'row' })[1].p.subtitle, '<img src=x onerror=alert(1)>', 'verbatim text');
});

test('native CI: an outcome card (Open logs, Dismiss), Re-run failed, Watch CI for…', async () => {
  const r = await run([
    { wait: 50 },
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Dismiss' } } }, { wait: 50 },
    { snapshot: 'dismissed' },
    { tap: barBtn }, { wait: 50 },
    { tap: { t: 'button', p: { label: 'Re-run failed' }, in: BOARD } }, { wait: 50 },
    { tap: { t: 'row', p: { title: 'Watch CI for…' }, in: BOARD } }, { wait: 20 },
    { input: [{ t: 'field', p: { label: 'Repo' } }, 'acme/web'] },
    { input: [{ t: 'field', p: { label: 'Branch or PR' } }, 'main'] }, { wait: 10 },
    { tap: { t: 'button', p: { label: 'Watch' } } }, { wait: 50 },
  ], ciSeed({ failed: true }));
  const card = find(r.snapshots.chat.root, { t: 'message', p: { role: 'system' } });
  assert.equal(card.p.text, 'CI failed on feature — test (ubuntu) › go test ./...');
  assert.deepEqual(all(card, { t: 'button' }).map((x) => x.p.label), ['Open logs', 'Dismiss']);
  assert.equal(find(r.snapshots.dismissed.root, { t: 'message', p: { role: 'system', text: card.p.text } }), null, 'dismissed');
  assert.equal(called(r, 'PUT', /\/prefs\/ci-dismissed$/).length, 1, 'kept in your prefs');
  assert.deepEqual(called(r, 'POST', /\/runs\/25\/ci\/rerun$/).map((c) => JSON.parse(c.body)), [{ watch: 4, runId: '7001', failedOnly: true }]);
  assert.deepEqual(called(r, 'POST', /\/runs\/25\/ci\/watch$/).map((c) => JSON.parse(c.body)), [{ repo: 'acme/web', ref: 'main' }]);
});

test('native CI: the project board\'s CI words (ext.card)', async () => {
  installHooks();
  const { ext } = await import(new URL('native/ext.js', TPL));
  await import(new URL('native/ci.js', TPL));
  assert.deepEqual(ext.card({ n: 1, ci: { state: 'failure', jobs: { total: 5, failed: 1 } } }), ['CI failed']);
  assert.deepEqual(ext.card({ n: 2, ci: { state: 'success', jobs: { total: 2 } } }), ['CI passed']);
  assert.equal(ext.card({ n: 3 }), null, 'no CI: no words');
});
