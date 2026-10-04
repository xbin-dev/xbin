// hack/agent-template-ci.test.mjs — CI in the conversation's model for both
// views (builtin-templates/agent/model/ci.js; API.md §CI in the
// conversation): the chip's words, a job's progress, the tones, ANSI
// stripped, the dock's rows, the child glyph, the outcome cards and their
// dismissal (kept, one per outcome), the store — one read in flight per
// conversation, the run view's summary before a read, a `ci` event's
// summary and outcomes (a coalesced one losing nothing), the dock's live
// re-read only while anything is pending, a running job's log, the routes'
// homes. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const C = await import(new URL('model/ci.js', TPL));
const F = await import(new URL('test/ci-stub.mjs', TPL));

const B = 2 ** 40;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

function backend(routes, partition) {
  const calls = [];
  globalThis.xbin = {
    self: 'apps/agent', partition,
    fetch: async (url, opts = {}) => {
      const method = opts.method || 'GET';
      const path = url.replace('/api/apps/agent', '');
      calls.push({ method, path, home: opts.partition || '', body: opts.body ? JSON.parse(opts.body) : null });
      for (const [m, re, fn] of routes) {
        const x = m === method && path.match(re);
        if (x) return fn(x, opts);
      }
      return new Response('404 page not found\n', { status: 404 });
    },
  };
  return calls;
}

function fakeApp(views = new Map()) {
  let changes = 0;
  return { session: { views, changed() { changes++; } }, changes: () => changes };
}

function memPrefs(start = []) {
  const p = { keys: start, saves: 0, load: async () => p.keys, save: async (keys) => { p.saves++; p.keys = keys; } };
  return p;
}

test('words: tones, elapsed, ANSI stripped, a job\'s progress', () => {
  assert.equal(C.toneOf('completed', 'success'), 'ok');
  for (const c of ['failure', 'timed_out', 'action_required', 'startup_failure']) assert.equal(C.toneOf('completed', c), 'bad', c);
  assert.equal(C.toneOf('completed', 'cancelled'), 'warn');
  assert.equal(C.toneOf('completed', 'skipped'), 'idle');
  assert.equal(C.toneOf('in_progress'), 'run');
  assert.equal(C.toneOf('queued'), 'idle');
  assert.equal(C.toneOf('pending'), 'run');
  assert.equal(C.toneOf('success'), 'ok');
  assert.equal(C.toneOf('failure'), 'bad');
  assert.equal(C.toneOf('gone'), 'idle');
  assert.equal(C.elapsed(42_000), '42s');
  assert.equal(C.elapsed(134_000), '2:14');
  assert.equal(C.elapsed(3_723_000), '1:02:03');
  assert.equal(C.elapsed(-1), '');
  assert.equal(C.elapsed(NaN), '');
  assert.equal(C.stripAnsi('\x1b[32mok\x1b[0m\r\nnext\x1b]0;title\x07 done\rredraw\x07'), 'ok\nnext done\nredraw');
  const snap = F.ciSnapshot();
  const p = C.jobProgress(snap.workflowRuns[0].jobs[1]);
  assert.deepEqual(p, { done: 1, total: 3, pct: 33, current: 'go test ./...' });
  assert.deepEqual(C.jobProgress(snap.workflowRuns[0].jobs[0]), { done: 1, total: 1, pct: 100, current: '' });
  assert.deepEqual(C.jobProgress({ status: 'queued', steps: [] }), { done: 0, total: 0, pct: 0, current: '' });
});

test('the chip: running, passed, failed, nothing yet', () => {
  const now = 1789990134000;
  const run = C.chipWords({ state: 'pending', jobs: { total: 5, done: 3 }, startedAt: 1789990000000, current: 'test › go test' }, now);
  assert.deepEqual(run, { text: 'CI ● 3/5 jobs · 2:14', tone: 'run', title: 'running: test › go test' });
  assert.equal(C.chipWords({ state: 'success', jobs: { total: 2 } }, now).text, 'CI ✓');
  assert.equal(C.chipWords({ state: 'success', jobs: {} }, now).tone, 'ok');
  const bad = C.chipWords({ state: 'failure', jobs: { total: 5, failed: 1 } }, now, 'test (ubuntu)');
  assert.equal(bad.text, 'CI ✗ test (ubuntu)');
  assert.equal(bad.tone, 'bad');
  assert.equal(C.chipWords({ state: 'none', jobs: {} }, now).text, 'CI —');
  assert.equal(C.chipWords(null, now), null);
});

test('rows: watches → runs → jobs (steps, progress, annotations), other checks, statuses; untouched text', () => {
  const v = F.ciView(9);
  const [w] = C.watchRows(v, 1789990100000);
  assert.equal(w.title, 'acme/web · feature');
  assert.equal(w.pr, 42);
  assert.equal(w.urls.pr, 'https://github.com/acme/web/pull/42');
  assert.equal(w.runs.length, 1);
  const r = w.runs[0];
  assert.deepEqual([r.name, r.event, r.attempt, r.tone, r.failed], ['ci', 'push', 1, 'run', false]);
  assert.equal(r.elapsedMs, 100000);
  assert.deepEqual(r.jobs.map((j) => [j.name, j.tone, j.progress.pct]), [['lint', 'ok', 100], ['test (ubuntu)', 'run', 33], ['build', 'idle', 0]]);
  const t = r.jobs[1];
  assert.deepEqual(t.steps.map((s) => C.GLYPH[s.tone]), ['✓', '●', '○']);
  assert.equal(t.steps[0].elapsedMs, 2000);
  assert.equal(t.check, '88001');
  assert.deepEqual(w.checks.map((k) => [k.name, k.tone, k.annotations, k.url]), [['codecov/patch', 'bad', 2, 'https://app.codecov.io/gh/acme/web']]);
  assert.equal(w.checks[0].summary, '**not markdown** <b>nor html</b>', 'kept as text, for the views to draw as text');
  assert.deepEqual(w.statuses.map((s) => [s.context, s.tone]), [['ci/jenkins', 'ok']]);
  assert.deepEqual(C.watchRows(null), []);
});

test('cards: one per outcome, the failing job and step, dismissed for good', async () => {
  const fail = F.ciView(9, { failed: true }).watches[0];
  const c = C.cardOf(fail);
  assert.deepEqual(c, { key: `4:${F.SHA1}:failure`, tone: 'bad', text: 'CI failed on feature — test (ubuntu) › go test ./...', watch: 4, job: '88001' });
  assert.equal(C.cardOf({ ...fail, outcome: `${F.SHA1}:success` }).text, 'CI passed on feature');
  assert.equal(C.cardOf({ ...fail, outcome: '' }), null);
  assert.equal(C.cardOf({ ...fail, outcome: `${F.SHA2}:failure` }).text, 'CI failed on feature', 'an older head: no job of this snapshot named');
  const prefs = memPrefs();
  const views = new Map();
  const app = fakeApp(views);
  const ci = C.createCI(app, { prefs, debounce: 0 });
  await ci.ready();
  ci.take({ type: 'ci', run: 9, root: 9, data: { root: 9, watch: 4, summary: { state: 'failure', jobs: {} }, state: 'failure', outcome: `${F.SHA1}:failure`,
    watches: [{ id: 4, state: 'failure', outcome: `${F.SHA1}:failure`, run: 9 }] } });
  assert.deepEqual(ci.cards(9).map((x) => x.key), [`4:${F.SHA1}:failure`], 'from an event alone');
  ci.dismiss(`4:${F.SHA1}:failure`);
  assert.deepEqual(ci.cards(9), []);
  await wait(1);
  assert.deepEqual(prefs.keys, [`4:${F.SHA1}:failure`], 'kept in the person\'s prefs');
  const again = C.createCI(app, { prefs });
  await again.ready();
  again.take({ type: 'ci', root: 9, data: { root: 9, watches: [{ id: 4, state: 'failure', outcome: `${F.SHA1}:failure` }] } });
  assert.deepEqual(again.cards(9), [], 'dismissed across pages');
  again.take({ type: 'ci', root: 9, data: { root: 9, watches: [{ id: 4, state: 'success', outcome: `${F.SHA1}:success` }] } });
  assert.deepEqual(again.cards(9).map((x) => [x.key, x.text]), [[`4:${F.SHA1}:success`, 'CI passed on its branch']], 'a new outcome is a new card');
  again.take({ type: 'ci', root: 9, data: { root: 9, watches: [{ id: 4, state: 'success', outcome: `${F.SHA1}:success`, ref: 'feature', repo: 'acme/web' }] } });
  assert.deepEqual(again.cards(9).map((x) => x.text), ['CI passed on feature'], 'the event names the branch');
  const broken = { load: async () => { throw new Error('denied'); }, save: async () => { throw new Error('denied'); } };
  const priv = C.createCI(app, { prefs: broken });
  await priv.ready();
  priv.take({ type: 'ci', root: 9, data: { root: 9, watches: [{ id: 4, state: 'failure', outcome: `${F.SHA1}:failure` }] } });
  priv.dismiss(`4:${F.SHA1}:failure`);
  assert.deepEqual(priv.cards(9), [], 'prefs refused: dismissed for the page');
});

test('the store: one read in flight, the run view before it, events, the child glyph, homes', async () => {
  let n = 0;
  let release;
  const gate = new Promise((r) => { release = r; });
  const calls = backend([
    ['GET', /^\/runs\/(\d+)\/ci(\?fresh=1)?$/, async (m) => { n++; await gate; return json(F.ciView(+m[1], { run: 12 })); }],
  ], 'user:alice');
  const views = new Map([[B + 9, { run: { id: B + 9 }, ci: { summary: { state: 'pending', jobs: { total: 2, done: 1 }, startedAt: 0 }, canWatch: true } }],
    [7, { run: { id: 7 }, ci: { summary: null, canWatch: true } }], [8, { run: { id: 8 }, ci: { summary: null, canWatch: false } }]]);
  const app = fakeApp(views);
  const ci = C.createCI(app, { prefs: memPrefs(), now: () => 1789990134000, debounce: 0 });
  assert.equal(ci.chip(B + 9).text, 'CI ● 1/2 jobs', 'the run view\'s summary before any read');
  assert.equal(ci.chip(7).text, 'CI —', 'nothing watched, but you may');
  assert.equal(ci.chip(8), null, 'neither');
  assert.equal(ci.chip(null), null, 'not at home');
  const a = ci.load(B + 9), b = ci.load(B + 9, { fresh: true });
  assert.equal(n, 1, 'one request in flight');
  release();
  await Promise.all([a, b]);
  await wait(5);
  assert.equal(n, 2, 'a fresh ask during a plain read reads fresh after it');
  assert.equal(calls.filter((c) => c.path.startsWith(`/runs/${B + 9}/ci`)).every((c) => c.home === ''), true, 'a person\'s own conversation: their partition');
  assert.equal(ci.chip(B + 9).text, 'CI ✗ codecov/patch', 'failed: the first failing job or check');
  assert.equal(ci.child(B + 9, 12).tone, 'bad');
  assert.match(ci.child(B + 9, 12).title, /^CI failed on feature/);
  assert.equal(ci.child(B + 9, 13), null, 'a coding agent that pushed nothing');
  // a shared conversation: at the shared space
  await ci.load(9);
  assert.equal(calls.at(-1).home, 'global');
  // an event: the summary at once, the held view read again
  const before = n;
  ci.take({ type: 'ci', run: 9, root: 9, data: { root: 9, watch: 4, summary: { state: 'success', jobs: { total: 5 } }, state: 'success', outcome: `${F.SHA1}:success`,
    watches: [{ id: 4, state: 'success', outcome: `${F.SHA1}:success`, run: 12 }] } });
  assert.equal(ci.chip(9).text, 'CI ✓');
  assert.equal(ci.view(9).watches[0].outcome, `${F.SHA1}:success`);
  await wait(10);
  assert.equal(n, before + 1, 'read again after the event');
  // deleted
  ci.take({ type: 'run', run: 9, root: 9, data: { id: 9, deleted: true } });
  assert.equal(ci.view(9), null);
});

test('live: fresh at once, then every 15 s only while anything is pending; one conversation at a time', async () => {
  const reads = [];
  let state = 'pending';
  backend([['GET', /^\/runs\/(\d+)\/ci(\?fresh=1)?$/, (m) => { reads.push(m[0]); return json({ ...F.ciView(+m[1]), watches: [{ ...F.ciView(+m[1]).watches[0], state }] }); }]]);
  const timers = [];
  const ci = C.createCI(fakeApp(), { prefs: memPrefs(), setInterval: (f, ms) => { timers.push({ f, ms, on: true }); return timers.length - 1; },
    clearInterval: (h) => { timers[h].on = false; } });
  ci.live(9, true);
  await wait(5);
  assert.deepEqual(reads, ['/runs/9/ci?fresh=1']);
  assert.equal(timers[0].ms, C.LIVE_MS);
  ci.live(9, true);
  assert.equal(timers.length, 1, 'already live');
  timers[0].f();
  await wait(5);
  assert.equal(reads.length, 2);
  state = 'success';
  timers[0].f();
  await wait(5);
  timers[0].f();
  await wait(5);
  assert.equal(reads.length, 3, 'nothing pending: no more reads');
  ci.live(10, true);
  assert.equal(timers[0].on, false, 'another conversation: the first stops');
  ci.live(10, false);
  assert.equal(timers[1].on, false);
  assert.equal(ci.isLive(10), false);
});

test('a log, a running job\'s, annotations, watch, unwatch, rerun', async () => {
  const calls = backend([
    ['GET', /^\/runs\/9\/ci\/jobs\/88000\/log\?(.*)$/, () => json({ text: '\x1b[1mlint ok\x1b[0m\n', bytes: 9, from: 0, complete: true, truncated: false, url: 'https://x/1' })],
    ['GET', /^\/runs\/9\/ci\/jobs\/88001\/log\?(.*)$/, () => json({ error: 'the job is still running', refusal: 'in-progress', url: 'https://github.com/acme/web/actions/runs/7001/job/88001' }, 409)],
    ['GET', /^\/runs\/9\/ci\/checks\/88100\/annotations\?watch=4$/, () => json({ items: [{ path: 'a.go', startLine: 1, level: 'failure', message: 'm' }], next: 'c2' })],
    ['GET', /^\/runs\/9\/ci\/checks\/88100\/annotations\?watch=4&cursor=c2$/, () => json({ items: [{ path: 'b.go', startLine: 2, level: 'notice', message: 'n' }], next: '' })],
    ['GET', /^\/runs\/9\/ci(\?fresh=1)?$/, () => json(F.ciView(9))],
    ['POST', /^\/runs\/9\/ci\/watch$/, () => json({ id: 5 }, 201)],
    ['DELETE', /^\/runs\/9\/ci\/watch\/5$/, () => new Response(null, { status: 204 })],
    ['POST', /^\/runs\/9\/ci\/rerun$/, () => json({ runId: '7001', attempt: 2 }, 202)],
  ]);
  const ci = C.createCI(fakeApp(), { prefs: memPrefs() });
  const lg = await ci.log(9, 4, '88000', { until: 100 });
  assert.equal(lg.text, 'lint ok\n');
  assert.equal(lg.inProgress, false);
  assert.match(calls.at(-1).path, /\?watch=4&tail=65536&until=100$/);
  const run = await ci.log(9, 4, '88001');
  assert.deepEqual([run.inProgress, run.url], [true, 'https://github.com/acme/web/actions/runs/7001/job/88001']);
  assert.deepEqual((await ci.annotations(9, 4, '88100')).map((a) => a.path), ['a.go', 'b.go'], 'every page');
  await ci.watch(9, { repo: 'acme/web', ref: 'main' });
  assert.deepEqual(calls.find((c) => c.method === 'POST').body, { repo: 'acme/web', ref: 'main' });
  await ci.unwatch(9, 5);
  const r = await ci.rerun(9, { watch: 4, runId: '7001', failedOnly: true });
  assert.equal(r.attempt, 2);
  assert.equal(calls.at(-1).path, '/runs/9/ci?fresh=1', 'read fresh after a rerun');
});
