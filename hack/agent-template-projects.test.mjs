// hack/agent-template-projects.test.mjs — Projects' model for both views
// (API.md §Projects in the UI): the router's #proj; a task's words
// (model/project-task.js — the board's columns, a card, the chips with a
// PR's checks only while the task has no CI, the prep and sign-in cards, Open
// PR, the crumb, the pinned task's section); the policy's keys
// (model/projects.js POLICY covers every key of ProjectPolicy in
// projects_types.go, the frozen seam); the calls' homes (model/project-api.js:
// a project's id says where it lives); and the store (app.projects: the list
// from both homes, needs-you, a project's page, a task's spec, the
// version-checked PATCH — a draft's own version, a 412 keeping both
// changes — the new-project body (a team definition at global), who may
// answer a task, every page of the list, a repo's busy refusal, only the
// latest answer kept — the board's and the list's, a deleted project not
// brought back — a team definition reading no tasks, where signing in is
// offered, the class of new tasks without internal reach, a sign-in
// followed by its pollId — a pending one read too, an earlier poll's late
// answer dropped, the later of two overlapping starts kept — the "Open PR" probe, the `project` event
// coalesced). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { readFileSync } from 'node:fs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const R = await import(new URL('model/router.js', TPL));
const W = await import(new URL('model/project-task.js', TPL));
const M = await import(new URL('model/projects.js', TPL));
const A = await import(new URL('model/project-api.js', TPL));
const F = await import(new URL('test/projects-stub.mjs', TPL));

const B = 2 ** 40;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// a scripted backend: xbin.fetch over a route table; every call is kept with its home
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
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

// a fake app: what the store reads of it
function fakeApp() {
  const emitted = [];
  const views = new Map();
  let changes = 0;
  const app = {
    page: 'projects', emitted, views,
    emit: (t) => emitted.push(t),
    session: { views, changed: () => { changes++; }, current: () => app.cur || null },
    changes: () => changes,
  };
  return app;
}

// --- the router -------------------------------------------------------------------------------

test('#proj and #proj=<id>', () => {
  assert.deepEqual(R.parse('#proj').proj, { id: null });
  assert.deepEqual(R.parse('#proj=1099511627783').proj, { id: 1099511627783 });
  assert.equal(R.parse('#c=5').proj, undefined, 'only when the address names it: the other addresses read as ever');
  assert.equal(R.parse('#project').proj, undefined);
  assert.equal(R.parse('#proj=7').conv, null);
  assert.equal(R.projHash(7), 'proj=7');
  assert.equal(R.projHash(null), 'proj');
  assert.equal(R.parse('#' + R.projHash(42)).proj.id, 42);
});

// --- a task's words -----------------------------------------------------------------------------

test('columnOf: the backend\'s column, else derived the same way', () => {
  assert.equal(W.columnOf({ column: 'pr', ws: 'queued' }), 'pr');
  const d = (x) => W.columnOf({ ws: 'ready', phase: 'open', runStatus: 'idle', ...x });
  assert.equal(d({ ws: 'pending' }), 'queued');
  assert.equal(d({ ws: 'preparing' }), 'queued');
  assert.equal(d({ runStatus: 'running' }), 'working');
  assert.equal(d({ runStatus: 'sleeping' }), 'working');
  assert.equal(d({ runStatus: 'waiting_input' }), 'needs-you');
  assert.equal(d({ ws: 'signin' }), 'needs-you');
  assert.equal(d({ ws: 'blocked', phase: 'merged' }), 'done');
  assert.equal(d({ phase: 'pr' }), 'pr');
  assert.equal(d({ phase: 'closed' }), 'done');
  const cols = W.columns([{ n: 1, column: 'done' }, { n: 3, column: 'done' }, { n: 2, column: 'queued' }]);
  assert.deepEqual(cols.map((c) => c.key), ['queued', 'working', 'needs-you', 'pr', 'done']);
  assert.deepEqual(cols[4].tasks.map((t) => t.n), [3, 1], 'newest first');
});

test('stateWords and a card', () => {
  assert.deepEqual(W.stateWords({ state: 'ci-failed' }), { text: 'CI failed', tone: 'bad' });
  assert.deepEqual(W.stateWords({ state: 'queued', waitingFor: 'slot' }), { text: 'queued · waiting for a free slot', tone: 'idle' });
  assert.equal(W.stateWords({ state: 'signin', waitingFor: 'signin' }).text, 'needs a sign-in');
  const c = W.cardWords(F.task(7, 2, { engine: 'harness', harness: 'claude', size: 'big' }));
  assert.equal(c.n, '#2');
  assert.equal(c.agent, 'claude');
  assert.equal(c.size, 'big');
});

test('an issue in the picker and a task\'s title on a card: plain — direction and zero-width characters out, clipped', () => {
  const w = W.issueWords({ repo: 'acme/web', number: 12, title: 'Fix \u202egol\u202c the\u200b login', labels: ['bug\u2066', ''], body: 'line one\n\nline\u0007 two ' + 'x'.repeat(400),
    url: 'http://evil.example/12' });
  assert.equal(w.key, 'acme/web#12');
  assert.equal(w.title, 'Fix gol the login');
  assert.deepEqual(w.labels, ['bug']);
  assert.ok(w.body.startsWith('line one line two x') && w.body.length === 240, w.body);
  assert.equal(w.url, '', 'https only');
  assert.equal(W.cardWords(F.task(7, 1, { title: 'a\u202eb' })).title, 'ab');
  assert.equal(W.cardWords(F.task(7, 1, { title: 'y'.repeat(300) })).title.length, 200);
});

test('prChip: a PR\'s checks only while the task has no CI summary; links only https', () => {
  const pr = { repo: 'acme/web', number: 42, url: 'https://github.com/acme/web/pull/42', state: 'open', draft: false, checks: 'failure' };
  assert.deepEqual(W.prChip(pr), { kind: 'pr', text: 'PR #42 open ✗', tone: 'bad', title: 'acme/web#42: open — checks failed', url: pr.url, checks: 'failure' });
  const withCI = W.prChip(pr, { state: 'failure' });
  assert.equal(withCI.text, 'PR #42 open');
  assert.equal(withCI.checks, '');
  assert.equal(W.prChip({ ...pr, draft: true, checks: 'none' }).text, 'PR #42 draft');
  assert.equal(W.prChip({ ...pr, url: 'javascript:alert(1)' }).url, '');
  assert.equal(W.prChip({ ...pr, url: 'http://github.com/acme/web/pull/42' }).url, '', 'https only, as the native view draws it');
  assert.equal(W.safeUrl('http://github.com/login/device'), '', 'a sign-in page where a code is typed: https only');
  assert.equal(W.safeUrl('https://github.com/login/device'), 'https://github.com/login/device');
  assert.equal(W.prChip({ ...pr, state: 'merged', checks: 'success' }).tone, 'ok', 'a merged PR is green whatever its checks');
});

test('taskChips: branch (a link when the repos are known), PRs, the setup outcome', () => {
  const t = F.task(7, 1, { prs: [{ repo: 'acme/web', number: 42, url: 'https://github.com/acme/web/pull/42', state: 'open', checks: 'pending' }] });
  const v = { projectTask: t, project: { id: 7, name: 'Web', n: 1, role: 'task' } };
  assert.deepEqual(W.taskChips({}), []);
  const bare = W.taskChips(v);
  assert.deepEqual(bare.map((c) => c.kind), ['branch', 'pr', 'setup']);
  assert.equal(bare[0].url, '');
  const pv = { repos: [F.repo('web'), F.repo('api')] };
  const ch = W.taskChips(v, pv);
  assert.equal(ch[0].url, 'https://github.com/acme/web/tree/xbin/k3x9qa/1-task-1');
  assert.deepEqual(ch[0].links.map((l) => l.repo), ['acme/web'], 'only the task\'s repos');
  assert.equal(ch[1].text, 'PR #42 open ●');
  assert.equal(ch[2].text, 'setup ✓');
  const bad = W.setupOutcome({ checkouts: [{ repo: 'web', setupExit: 0 }, { repo: 'api', setupExit: 2 }] });
  assert.equal(bad.text, 'setup ✗ api');
  assert.equal(bad.tone, 'bad');
  assert.equal(W.setupOutcome({ checkouts: [{ repo: 'web' }] }), null, 'no setup ran');
});

test('prepCard: while the workspace isn\'t ready; the sign-in card for the person who must sign in only', () => {
  const ready = { projectTask: F.task(7, 2), run: { pendingState: {} } };
  assert.equal(W.prepCard(ready, 'alice'), null);
  const prep = { projectTask: F.task(7, 4, { ws: 'preparing', step: 'cloning', checkouts: [{ repo: 'web', state: 'ready', setupExit: 0 }, { repo: 'api', state: 'setup' }, { repo: 'x', state: 'failed', setupExit: 3, error: 'boom' }] }), run: { pendingState: {} } };
  const c = W.prepCard(prep, 'alice');
  assert.equal(c.title, 'preparing the workspace');
  assert.equal(c.tone, 'run');
  assert.equal(c.step, 'cloning');
  assert.deepEqual(c.steps.map((s) => [s.repo, s.glyph, s.tone]), [['web', '✓', 'ok'], ['api', '●', 'run'], ['x', '✗', 'warn']]);
  assert.equal(c.steps[2].error, 'boom');
  assert.equal(c.retry, false);
  assert.equal(W.prepCard({ projectTask: F.task(7, 8, { ws: 'failed', error: 'x' }), run: {} }, 'alice').retry, true);
  const si = { url: 'https://github.com/login/device', userCode: 'ABCD-1234', pollId: 'p', intervalMs: 5000, expiresAt: 1 };
  const parked = { projectTask: F.task(7, 6, { ws: 'signin' }), run: { pendingState: { kind: 'project', project: { ws: 'signin', step: 'waiting', signin: si } } } };
  assert.equal(W.prepCard(parked, 'alice').signin.userCode, 'ABCD-1234');
  assert.equal(W.prepCard(parked, { user: 'alice' }).signin.userCode, 'ABCD-1234', 'me as GET /me');
  const other = W.prepCard(parked, 'bob');
  assert.equal(other.signin, null, 'never shown to another person');
  assert.equal(other.signinElsewhere, 'waiting for alice to sign in');
  const noCode = { ...parked, run: { pendingState: { kind: 'project', project: { ws: 'signin' } } } };
  assert.match(W.prepCard(noCode, 'alice').signinElsewhere, /Sign in to the provider/);
  assert.equal(W.prepCard({ ...parked, run: { pendingState: { kind: 'project', project: { ws: 'signin', signin: { ...si, url: 'javascript:x' } } } } }, 'alice').signin.url, '');
});

test('prButton, crumb, taskSection', () => {
  const v = { projectTask: F.task(7, 2), project: { id: 7, name: 'Web', n: 2, role: 'task' } };
  assert.equal(W.prButton(v, null).shown, false, 'unknown route: hidden');
  assert.equal(W.prButton(v, false).shown, false, 'no route: hidden');
  assert.deepEqual(W.prButton(v, true), { shown: true, disabled: false, title: 'open a pull request for this task\'s branch' });
  assert.equal(W.prButton(v, true, false).shown, false, 'a viewer');
  assert.equal(W.prButton({ projectTask: F.task(7, 2, { prs: [{ state: 'open', number: 1 }] }) }, true).shown, false, 'an open PR');
  assert.equal(W.prButton({ projectTask: F.task(7, 2, { ws: 'preparing' }) }, true).disabled, true);
  assert.equal(W.prButton({ projectTask: F.task(7, 2, { phase: 'merged' }) }, true).shown, false);
  assert.deepEqual(W.crumb(v), { pid: 7, text: 'Web ›', title: 'back to the project (this conversation is task #2)' });
  assert.match(W.crumb({ project: { id: 7, name: 'Web', role: 'coordinator' } }).title, /its coordinator/);
  assert.equal(W.crumb({}), null);
  const s = W.taskSection({ ...v, projectTask: F.task(7, 2, { ports: { base: 20010, span: 10 }, issue: { repo: 'acme/web', number: 11, title: 'Login', url: 'https://github.com/acme/web/issues/11' } }) });
  assert.equal(s.ports, '20010–20019');
  assert.equal(s.issue, 'acme/web#11 Login');
  assert.equal(s.repos, 'web');
});

// --- the policy ------------------------------------------------------------------------------------

// leafKeys: ProjectPolicy's JSON keys from projects_types.go, nested structs as paths
function leafKeys() {
  const src = readFileSync(new URL('_backend/projects_types.go', TPL), 'utf8');
  const structs = {};
  for (const m of src.matchAll(/type (\w+) struct \{([\s\S]*?)\n\}/g)) {
    structs[m[1]] = [...m[2].matchAll(/^\s+\w+\s+([\w.[\]*]+)\s+`json:"([^",]+)/gm)].map((f) => ({ type: f[1], key: f[2] }));
  }
  const walk = (name, pre) => structs[name].flatMap((f) => (structs[f.type] ? walk(f.type, pre + f.key + '.') : [pre + f.key]));
  return walk('ProjectPolicy', '');
}

test('POLICY shows every key of ProjectPolicy, each once', () => {
  const want = leafKeys();
  assert.ok(want.length > 30, `ProjectPolicy read: ${want.length}`);
  const have = M.POLICY.flatMap((g) => g.fields.map((f) => f.path));
  assert.deepEqual([...have].sort(), [...want].sort());
  assert.equal(new Set(have).size, have.length);
  for (const g of M.POLICY) for (const f of g.fields) assert.ok(f.label && ['text', 'area', 'lines', 'number', 'bool', 'select', 'class', 'harness'].includes(f.type), f.path);
});

test('policySet keeps every other key, unknown ones too; fieldValue', () => {
  const p = { ci: { autoFix: true, maxPerDay: 5 }, futureKey: { kept: 1 } };
  const q = M.policySet(p, 'ci.autoFix', false);
  assert.deepEqual(q, { ci: { autoFix: false, maxPerDay: 5 }, futureKey: { kept: 1 } });
  assert.equal(p.ci.autoFix, true, 'a copy');
  assert.deepEqual(M.policySet({}, 'ports.base', 1), { ports: { base: 1 } });
  assert.equal(M.policyGet(q, 'ci.maxPerDay'), 5);
  assert.equal(M.policyGet(q, 'nope.x'), undefined);
  assert.deepEqual(M.fieldValue({ type: 'lines' }, ' make test \n\n go vet ./... '), ['make test', 'go vet ./...']);
  assert.equal(M.fieldValue({ type: 'number' }, '7.9'), 7);
  assert.equal(M.fieldValue({ type: 'bool' }, 'x'), true);
  assert.equal(M.fieldText({ type: 'lines' }, ['a', 'b']), 'a\nb');
});

test('can and sharable', () => {
  assert.deepEqual(M.can({ level: 'viewer', state: 'active' }), { owner: false, act: false, read: true, settings: false, archived: false });
  assert.equal(M.can({ level: 'participant', state: 'active' }).act, true);
  assert.equal(M.can({ level: 'owner', state: 'archived' }).act, false, 'an archived project takes no tasks');
  assert.equal(M.sharable({ kind: 'personal' }, 'legacy'), true);
  assert.equal(M.sharable({ kind: 'personal' }, 'user'), false, 'a person\'s own project is private');
  assert.equal(M.sharable({ kind: 'team' }, 'global'), true);
  assert.equal(M.sharable({ kind: 'membership' }, 'user'), false);
  assert.equal(M.repoSlug('acme/My.Repo'), 'my-repo');
});

// --- the calls' homes ----------------------------------------------------------------------------------

test('a project\'s id says its home; a refusal is kept whole', async () => {
  const calls = backend([
    ['GET', /^\/projects\/\d+$/, () => json({ project: { id: 1 } })],
    ['POST', /^\/projects\/\d+\/tasks$/, () => json({ error: 'too many open tasks', refusal: 'limit' }, 429)],
    ['GET', /\/task\/pr$/, () => json({ error: 'method not allowed' }, 405)],
  ], 'user:alice');
  await A.projectApi(null, B + 7).get();
  await A.projectApi(null, 9).get();
  assert.deepEqual(calls.map((c) => c.home), ['', 'global']);
  const e = await A.projectApi(null, B + 7).createTask({ text: 'x' }).catch((x) => x);
  assert.equal(e.status, 429);
  assert.equal(e.refusal, 'limit');
  assert.equal(e.message, 'too many open tasks');
  assert.equal(await A.taskApi(B + 101).prProbe(), true, '405: the route exists');
  backend([], 'user:alice');
  assert.equal(await A.taskApi(B + 101).prProbe(), false, '404: it doesn\'t');
  assert.equal(A.qs({ a: 1, b: '', c: true, d: false, e: null }), '?a=1&c=1');
});

// --- the store ------------------------------------------------------------------------------------------

function projectsBackend(partition = 'user:alice') {
  const seed = F.projSeed();
  const mine = { ...seed.projects[0], id: B + 7, home: undefined };
  const team = { ...seed.projects[0], id: 9, name: 'Team', kind: 'team', updatedMs: 1, home: undefined };
  let version = 3;
  let taskReads = 0;
  const stored = { name: mine.name, policy: { ...mine.policy } }; // what PATCH changed
  const calls = backend([
    ['GET', /^\/projects(\?.*)?$/, (m, o) => json({ items: o.partition === 'global' ? [team] : [mine, { ...mine, id: B + 8, state: 'archived', counts: { 'needs-you': 5 } }] })],
    ['GET', /^\/projects\/(\d+)$/, () => json({ project: { ...mine, ...stored, version } })],
    ['PATCH', /^\/projects\/(\d+)$/, (m, o) => {
      const b = JSON.parse(o.body);
      if (b.version !== version) return json({ error: 'stale', version }, 412);
      version++;
      for (const k of ['name', 'policy']) if (k in b) stored[k] = b[k];
      return json({ project: { ...mine, ...stored, version } });
    }],
    ['DELETE', /^\/projects\/\d+\/repos\/(\w+)/, (m) => (m[1] === 'api' ? json({ error: 'open tasks use api', refusal: 'busy' }, 409) : json({ error: 'the project is archived' }, 409))],
    ['GET', /^\/projects\/\d+\/tasks/, () => { taskReads++; return json({ items: seed.tasks[7], next: '' }); }],
    ['POST', /^\/projects\/\d+\/tasks$/, () => json({ task: { n: 9 }, run: { id: 1 } }, 201)],
    ['POST', /^\/projects$/, (m, o) => json({ project: { id: B + 50, ...JSON.parse(o.body) }, jobs: [] }, 201)],
    ['GET', /^\/runs\/\d+\/task$/, () => json({ ...seed.tasks[7][1], state: 'ci' })],
  ], partition);
  for (const p of [mine]) p.counts = { 'needs-you': 3, working: 1 };
  team.counts = { 'needs-you': 2 };
  // another writer: a change saved meanwhile (a new version)
  const other = (ch) => { Object.assign(stored, ch); version++; };
  return { calls, stored, other, setVersion: (v) => { version = v; }, taskReads: () => taskReads };
}

test('the list from both homes; needs-you counts live projects', async () => {
  const b = projectsBackend();
  const app = fakeApp();
  const pj = M.createProjects(app);
  await pj.load();
  assert.deepEqual(b.calls.map((c) => c.home).sort(), ['', 'global']);
  assert.deepEqual(pj.sections().map((s) => [s.key, s.items.map((p) => p.id)]), [['yours', [B + 7]], ['team', [9]], ['archived', [B + 8]]]);
  assert.equal(pj.needsYou(), 5, 'the archived one\'s are not counted');
  assert.ok(app.emitted.includes('projects'));
  assert.equal(pj.supported, true);
  backend([], 'user:alice');
  const pj2 = M.createProjects(fakeApp());
  await pj2.load();
  assert.equal(pj2.supported, false, 'a backend without Projects: no entry');
});

test('a project\'s page: open, the board, a new task\'s spec, a stale PATCH', async () => {
  const b = projectsBackend();
  const app = fakeApp();
  const routed = [];
  const pj = M.createProjects(app, { route: (pid) => routed.push(pid) });
  await pj.open(B + 7);
  assert.deepEqual(routed, [B + 7]);
  assert.deepEqual(pj.board().map((c) => c.tasks.length), [1, 1, 3, 1, 1]);
  pj.newTask();
  assert.deepEqual(pj.taskForm.repos, ['web', 'api'], 'every repo, at first');
  pj.setTask('text', '  Make it fast ');
  pj.setTask('agent', 'claude');
  pj.setTask('repos', ['api']);
  assert.deepEqual(pj.specOf(pj.taskForm), { text: 'Make it fast', agent: { provider: 'claude' }, repos: ['api'] });
  pj.setTask('repos', ['web', 'api']);
  pj.setTask('agent', '');
  assert.deepEqual(pj.specOf(pj.taskForm), { text: 'Make it fast' }, 'all repos and the project\'s default: nothing named');
  pj.setTask('repos', []);
  assert.equal(await pj.submitTask(), null);
  assert.equal(pj.taskForm.err, 'Pick a repo to work in.');
  pj.setTask('repos', ['web']);
  const r = await pj.submitTask();
  assert.equal(r.task.n, 9);
  assert.equal(pj.taskForm, null);
  assert.equal(pj.flash, 'Task #9 made.');
  // a stale version: read again, said, nothing overwritten
  b.setVersion(9);
  await assert.rejects(pj.patch(B + 7, { name: 'x' }), (e) => e.status === 412 && /changed this project meanwhile/.test(e.message));
  assert.equal(pj.find(B + 7).version, 9, 'read again');
  const ok = await pj.patch(B + 7, { name: 'x' });
  assert.equal(ok.project.version, 10);
});

test('the policy draft keeps its version: a change made meanwhile is never saved over', async () => {
  const b = projectsBackend();
  const pj = M.createProjects(fakeApp());
  await pj.open(B + 7, 'settings');
  b.stored.policy = { ...b.stored.policy, maxTasks: 3, autoPR: 'off' };
  await pj.refresh(B + 7);
  pj.editPolicy(B + 7);
  assert.equal(pj.draft.version, 3);
  pj.setPolicy('maxTasks', 5);
  // someone sets autoPR meanwhile, and a `project` event reads it in
  b.other({ policy: { ...b.stored.policy, autoPR: 'ready' } });
  await pj.refresh(B + 7);
  assert.equal(pj.find(B + 7).version, 4);
  await pj.savePolicy();
  const sent = b.calls.filter((c) => c.method === 'PATCH').at(-1).body;
  assert.equal(sent.version, 3, 'the draft\'s version, not the one read since');
  assert.match(pj.draft.err, /changed this policy meanwhile/);
  assert.equal(b.stored.policy.autoPR, 'ready', 'nothing saved over');
  assert.equal(pj.draft.policy.autoPR, 'ready', 'their change shown…');
  assert.equal(pj.draft.policy.maxTasks, 5, '…with yours');
  assert.equal(pj.draft.version, 4);
  await pj.savePolicy();
  assert.equal(pj.draft.saved, true);
  assert.deepEqual([b.stored.policy.autoPR, b.stored.policy.maxTasks], ['ready', 5], 'both changes kept');
  // a rename typed at an older version: 412, said
  const old = pj.find(B + 7).version;
  b.other({ name: 'Theirs' });
  await pj.rename(B + 7, 'Mine', old);
  assert.match(pj.err, /changed this project meanwhile/);
  assert.equal(b.stored.name, 'Theirs');
  // a repo: busy re-asks; another 409 is said
  assert.deepEqual(await pj.removeRepo(B + 7, 'api'), { busy: 'open tasks use api' });
  assert.deepEqual(await pj.removeRepo(B + 7, 'web'), { ok: false });
  assert.equal(pj.err, 'the project is archived');
});

test('who answers a task: the built-in agent by name only where the default means it', () => {
  const hs = [{ id: 'claude', name: 'Claude Code', available: true }, { id: 'codex', name: 'Codex', available: false }];
  const c = (engine, harness, list = hs) => M.agentChoices({ policy: { engine, harness } }, list);
  assert.deepEqual(c('auto', '').map((x) => x.value), ['', 'claude', 'codex'], 'no "builtin" value: a TaskSpec can\'t ask for it');
  assert.match(c('auto', '')[0].label, /the project's default: the coding agent you used last, else the built-in agent/);
  assert.match(c('harness', 'claude')[0].label, /default: Claude Code$/);
  assert.match(c('auto', 'claude')[0].label, /default: Claude Code$/, 'auto takes the policy\'s harness first');
  assert.match(c('builtin', '')[0].label, /^the built-in agent/);
  assert.match(c('auto', '', [{ id: 'codex', name: 'Codex', available: false }])[0].label, /^the built-in agent \(no coding agent is available\)/);
  assert.equal(c('auto', '')[2].disabled, true);
});

test('the list: every page of every home, no limit sent', async () => {
  const pages = { '': { items: [{ id: B + 1, counts: { 'needs-you': 1 }, state: 'active' }], next: 'p2' }, p2: { items: [{ id: B + 2, counts: { 'needs-you': 2 }, state: 'active' }] } };
  const calls = backend([['GET', /^\/projects(\?cursor=(\w+))?$/, (m, o) => json(o.partition === 'global' ? { items: [] } : pages[m[2] || ''])]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  await pj.load();
  assert.deepEqual(pj.list.map((p) => p.id).sort(), [B + 1, B + 2]);
  assert.equal(pj.needsYou(), 3);
  assert.ok(calls.every((c) => !/limit=/.test(c.path)));
});

test('only the latest answer is kept: an older one arriving last is dropped', async () => {
  const gates = [];
  backend([['GET', /^\/projects\/\d+\/tasks\?(.*)$/, (m) => new Promise((res) => gates.push(() => res(json({ items: [{ n: /q=fix/.test(m[1]) ? 1 : 2, title: m[1] }] }))))]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  const older = pj.tasks(B + 7, { q: '', mine: false });
  const newer = pj.tasks(B + 7, { q: 'fix', mine: false });
  await wait(5);
  gates[1]();
  await newer;
  gates[0]();
  await older;
  assert.deepEqual(pj.taskList(B + 7).items.map((t) => t.n), [1], 'the filtered answer stays');
  assert.equal(pj.taskList(B + 7).loading, false);
});

test('the list: an older answer arriving last is dropped; a deleted project stays gone', async () => {
  const gates = [];
  const lists = [[{ id: 1, state: 'active', counts: { 'needs-you': 1 } }, { id: 2, state: 'active', counts: { 'needs-you': 4 } }], [{ id: 1, state: 'active', counts: { 'needs-you': 1 } }]];
  backend([['GET', /^\/projects(\?.*)?$/, () => { const items = lists[Math.min(gates.length, 1)]; return new Promise((res) => gates.push(() => res(json({ items })))); }],
    ['DELETE', /^\/projects\/2\?sandbox=keep$/, () => json({ state: 'deleting' }, 202)]]);
  const pj = M.createProjects(fakeApp());
  const older = pj.load(); // lists 1 and 2
  await wait(5);
  const newer = pj.load(); // lists 1 only
  await wait(5);
  gates[1]();
  await newer;
  gates[0]();
  await older;
  assert.deepEqual(pj.list.map((p) => p.id), [1], 'the older list never replaces the newer one');
  assert.equal(pj.needsYou(), 1);
  // a read in flight while a project is deleted doesn't bring it back
  lists[1] = [{ id: 1, state: 'active' }, { id: 2, state: 'active' }];
  const inflight = pj.load();
  await wait(5);
  assert.equal(await pj.remove(2), true);
  await wait(5);
  for (const g of gates.slice(2)) g();
  await inflight;
  await wait(5);
  for (const g of gates.slice(2)) g();
  await wait(5);
  assert.ok(!pj.list.some((p) => p.id === 2), JSON.stringify(pj.list.map((p) => p.id)));
});

test('a team project\'s definition: its page reads no tasks', async () => {
  const calls = backend([['GET', /^\/projects\/9$/, () => json({ project: { id: 9, kind: 'team', level: 'participant', state: 'active', version: 1 } })]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  await pj.open(9);
  assert.equal(pj.view(9).kind, 'team');
  assert.deepEqual(calls.map((c) => `${c.method} ${c.path} ${c.home}`), ['GET /projects/9 global'], 'no GET of its tasks: it has none');
});

test('signing in is offered only in your own partition, as the provider lets you', () => {
  const gh = (you, ids = ['person', 'bot']) => ({ scm: 'gh', identities: ids, you });
  assert.equal(M.canSignin(gh({ identities: ['person', 'bot'] }), 'user'), true);
  assert.equal(M.canSignin(gh({ identities: ['person', 'bot'] }), 'legacy'), false, 'an unpartitioned agent: the routes answer 409');
  assert.equal(M.canSignin(gh({ identities: ['person', 'bot'] }), 'global'), false, 'the shared space: each member signs in from their own');
  assert.equal(M.canSignin(gh({ identities: ['bot'] }), 'user'), false, 'what you may use, not what it hands out (identities)');
  assert.equal(M.canSignin(gh(undefined), 'user'), false);
  assert.equal(M.canSignin(null, 'user'), false);
});

test('the class of new tasks: never one with internal reach', () => {
  const st = { classes: [
    { id: 'internal', name: 'Internal', toolsets: ['files', 'internal'] },
    { id: 'coding', name: 'Coding', toolsets: ['files', 'sandbox'], sandboxEgress: ['internet'] },
    { id: 'web', name: 'Web', toolsets: ['web'] },
    { id: 'mixed', name: 'Mixed', toolsets: ['internal', 'web'], mixed: true },
  ] };
  assert.deepEqual(M.taskClassChoices(st, 'coding').map((r) => r.value), ['coding', 'web'], 'internal reach is refused (class-internal): not offered');
  const kept = M.taskClassChoices(st, 'internal');
  assert.deepEqual(kept.map((r) => r.value), ['internal', 'coding', 'web'], 'the stored one stays shown');
  assert.match(kept[0].label, /has internal reach: pick another/);
  assert.deepEqual(M.taskClassChoices(st, 'gone').map((r) => r.value), ['coding', 'web', 'gone'], 'one you may not use stays as it is');
});

test('a sign-in followed by its pollId; a poll error is tried again', async () => {
  let fail = 2, polls = 0;
  backend([['GET', /^\/projects\/scm\/signin\/p2/, () => { polls++; return fail-- > 0 ? json({ error: 'busy upstream' }, 502) : json({ state: 'done' }); }],
    ['GET', /^\/projects\/scm(\?.*)?$/, () => json({ providers: [] })]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  pj.signins.set('gh', { state: 'done', pollId: 'p1' }); // an earlier sign-in
  const realSet = globalThis.setTimeout;
  globalThis.setTimeout = (fn) => realSet(fn, 1); // no waiting in a test
  try {
    pj.pollSignin('gh', { pollId: 'p2', intervalMs: 5000 });
    assert.deepEqual([pj.signinOf('gh').state, pj.signinOf('gh').pollId], ['pending', 'p2'], 'an earlier done never counts for this one');
    for (let i = 0; i < 100 && pj.signinOf('gh').state !== 'done'; i++) await new Promise((r) => realSet(r, 5));
  } finally { globalThis.setTimeout = realSet; }
  assert.equal(polls, 3, 'two errors, then the answer');
  assert.deepEqual([pj.signinOf('gh').state, pj.signinOf('gh').pollId], ['done', 'p2']);
});

test('a pending sign-in read by GET is followed to done', async () => {
  const si = { pollId: 'g1', userCode: 'WXYZ-0001', url: 'https://github.com/login/device', intervalMs: 5000 };
  let polls = 0;
  backend([['GET', /^\/projects\/scm\/signin\/g1/, () => { polls++; return json(polls < 2 ? { state: 'pending' } : { state: 'done', identity: { login: 'alice' } }); }],
    ['GET', /^\/projects\/scm\/signin(\?.*)?$/, () => json({ state: 'pending', signin: si })],
    ['GET', /^\/projects\/scm(\?.*)?$/, () => json({ providers: [] })]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  const realSet = globalThis.setTimeout;
  globalThis.setTimeout = (fn) => realSet(fn, 1);
  try {
    const s = await pj.signinState('gh');
    assert.deepEqual([s.state, s.pollId, s.signin.userCode], ['pending', 'g1', 'WXYZ-0001']);
    assert.equal(pj.polls.size, 1, 'a parked task\'s sign-in (or one started elsewhere) is polled here too');
    await pj.signinState('gh'); // the tab read again: the same pollId is not followed twice
    for (let i = 0; i < 100 && pj.signinOf('gh').state !== 'done'; i++) await new Promise((r) => realSet(r, 5));
  } finally { globalThis.setTimeout = realSet; }
  assert.deepEqual([pj.signinOf('gh').state, pj.signinOf('gh').pollId], ['done', 'g1']);
  assert.equal(polls, 2);
  assert.equal(pj.polls.size, 0);
});

test('a sign-in that fails to start keeps following the one before it (a parked task\'s)', async () => {
  const seen = [];
  backend([['GET', /^\/projects\/scm\/signin\/(p\w)/, (x) => { seen.push(x[1]); return json({ state: 'pending' }); }],
    ['POST', /^\/projects\/scm\/signin$/, () => json({ error: 'upstream down' }, 502)],
    ['GET', /^\/projects\/scm(\?.*)?$/, () => json({ providers: [] })]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  const realSet = globalThis.setTimeout;
  const timers = [];
  globalThis.setTimeout = (fn) => { timers.push(fn); return timers.length; }; // ticks run by hand
  const settle = () => new Promise((r) => realSet(r, 5));
  try {
    pj.pollSignin('gh', { pollId: 'pA', userCode: 'AAAA-1111' });
    const s = await pj.signin('gh');
    assert.equal(s.state, 'error');
    const cur = pj.signinOf('gh');
    assert.deepEqual([cur.state, cur.pollId, cur.signin.userCode], ['pending', 'pA', 'AAAA-1111'], 'still the parked task\'s sign-in');
    assert.match(cur.err, /upstream down/, 'the failed start is said');
    assert.equal(timers.length, 1, 'its poll goes on');
    timers.shift()();
    await settle();
    assert.deepEqual(seen, ['pA']);
  } finally { globalThis.setTimeout = realSet; }
});

test('two overlapping sign-in starts: the later one is followed, whichever answers first', async () => {
  for (const order of [[0, 1], [1, 0]]) {
    let n = 0; const gates = [];
    const seen = [];
    backend([['POST', /^\/projects\/scm\/signin$/, async () => { const id = 'p' + (++n); await new Promise((r) => gates.push(r)); return json({ state: 'pending', signin: { pollId: id, userCode: id, url: 'https://x' } }); }],
      ['GET', /^\/projects\/scm\/signin\/(p\w)/, (x) => { seen.push(x[1]); return json({ state: 'pending' }); }],
      ['GET', /^\/projects\/scm(\?.*)?$/, () => json({ providers: [] })]], 'user:alice');
    const pj = M.createProjects(fakeApp());
    const realSet = globalThis.setTimeout;
    const timers = [];
    globalThis.setTimeout = (fn) => { timers.push(fn); return timers.length; }; // ticks run by hand
    const settle = () => new Promise((r) => realSet(r, 5));
    try {
      const starts = [pj.signin('gh'), null];
      await settle();
      starts[1] = pj.signin('gh'); // a double click: p2 is the provider's pending flow
      await settle();
      for (const i of order) { gates[i](); await starts[i]; await settle(); }
      assert.equal(pj.signinOf('gh').pollId, 'p2', `answers in order ${order}: the later start is shown`);
      assert.equal(pj.following.get('gh'), 'p2');
      assert.equal(timers.length, 1, 'one poll, of p2');
      timers.shift()();
      await settle();
      assert.deepEqual(seen, ['p2']);
    } finally { globalThis.setTimeout = realSet; }
  }
});

test('a late answer of an earlier sign-in poll changes nothing', async () => {
  const gates = [];
  const seen = [];
  backend([['GET', /^\/projects\/scm\/signin\/(p\w)/, (x) => { seen.push(x[1]); return x[1] === 'pA' ? new Promise((r) => gates.push(r)) : json({ state: 'pending' }); }],
    ['POST', /^\/projects\/scm\/signin$/, () => json({ state: 'pending', signin: { pollId: 'pB', userCode: 'BBBB-2222', url: 'https://x/d' } })],
    ['DELETE', /^\/projects\/scm\/signin/, () => json({ ok: true })],
    ['GET', /^\/projects\/scm(\?.*)?$/, () => json({ providers: [] })]], 'user:alice');
  const pj = M.createProjects(fakeApp());
  const realSet = globalThis.setTimeout;
  const timers = [];
  globalThis.setTimeout = (fn) => { timers.push(fn); return timers.length; }; // ticks run by hand
  const settle = () => new Promise((r) => realSet(r, 5));
  try {
    pj.pollSignin('gh', { pollId: 'pA', userCode: 'AAAA-1111' });
    timers.shift()(); // pA's tick: its answer is on its way
    await settle();
    await pj.signin('gh'); // a new sign-in meanwhile → pB
    gates.shift()(json({ state: 'pending', signin: { pollId: 'pA', userCode: 'AAAA-1111' } }));
    await settle();
    assert.deepEqual([pj.signinOf('gh').pollId, pj.signinOf('gh').signin.userCode], ['pB', 'BBBB-2222'], 'the older code is not shown');
    assert.equal(timers.length, 1, 'pA does not go on: only pB is polled');
    timers.shift()();
    await settle();
    assert.deepEqual(seen, ['pA', 'pB']);
    // Forget while a poll's answer is on its way: it stays forgotten
    timers.length = 0;
    pj.pollSignin('gh', { pollId: 'pA', userCode: 'AAAA-1111' });
    timers.shift()();
    await settle();
    await pj.forget('gh');
    gates.shift()(json({ error: 'busy upstream' }, 502));
    await settle();
    assert.equal(pj.signinOf('gh').state, 'none', 'a stale poll\'s error does not undo Forget');
    assert.equal(timers.length, 0, 'nor does it go on');
  } finally { globalThis.setTimeout = realSet; }
});

test('the new-project form: what is missing, then the body', async () => {
  projectsBackend('');
  const pj = M.createProjects(fakeApp());
  pj.form = { name: '', scm: '', repos: [], sandbox: { mode: 'new', ref: '', provider: '' }, policy: { maxTasks: '4', autoPR: 'draft', engine: 'auto', harness: '', as: '' },
    share: { visibility: 'team', teamRole: 'participant' } };
  assert.equal(pj.formBody().error, 'Name the project.');
  pj.form.name = 'Web';
  assert.equal(pj.formBody().error, 'Pick a provider.');
  pj.form.scm = 'apps/scm-github';
  assert.equal(pj.formBody().error, 'Add a repo.');
  pj.addFormRepo('acme/web');
  pj.setFormSetup('acme/web', 'npm ci');
  assert.equal(pj.formBody().error, 'No sandbox manager to make a sandbox with.');
  pj.form.sandbox = { mode: 'pick', ref: '' };
  assert.equal(pj.formBody().error, 'Pick a sandbox, or make a new one.');
  pj.form.sandbox = { mode: 'pick', ref: 'apps/coding-sandbox|sb-1' };
  assert.deepEqual(pj.formBody().body, { name: 'Web', scm: 'apps/scm-github', repos: [{ repo: 'acme/web', setup: 'npm ci' }], sandbox: { ref: 'apps/coding-sandbox|sb-1' },
    policy: { maxTasks: 4, autoPR: 'draft', engine: 'auto' }, share: { visibility: 'team', teamRole: 'participant', members: [] } });
  globalThis.xbin.partition = 'user:alice';
  assert.equal(pj.formBody().body.share, undefined, 'a person\'s own project is never shared');
  globalThis.xbin.partition = 'global';
  assert.equal(pj.formBody().body.kind, 'team', 'a partitioned global instance holds team definitions only');
  assert.deepEqual(pj.formBody().body.share, { visibility: 'team', teamRole: 'participant', members: [] }, 'a team definition is sent shared');
  pj.form.sandbox = { mode: 'none' };
  assert.equal('sandbox' in pj.formBody().body, false, 'its seed sandbox is optional');
  pj.form.share.visibility = 'private';
  assert.equal(pj.formBody().body.share.visibility, 'private', 'the members added next');
  globalThis.xbin.partition = 'user:alice';
  assert.equal(pj.formBody().error, 'No sandbox manager to make a sandbox with.', 'a project of your own needs its sandbox');
});

test('a `project` event: coalesced, the list, the open project and an open task read again', async () => {
  const b = projectsBackend();
  const app = fakeApp();
  const pj = M.createProjects(app);
  await pj.open(B + 7);
  const runId = B + 102;
  app.views.set(runId, { run: { id: runId }, project: { id: B + 7, n: 2 }, projectTask: { n: 2, state: 'working' } });
  app.cur = { run: { id: runId }, project: { id: B + 7, n: 2 }, projectTask: { n: 2 } };
  const reads = b.taskReads();
  const before = b.calls.length;
  pj.take({ type: 'project', data: { id: B + 7, change: 'task', n: 2 } });
  pj.take({ type: 'project', data: { id: B + 7, change: 'task', n: 2 } });
  pj.take({ type: 'run', data: {} });
  assert.equal(b.calls.length, before, 'nothing read at once');
  await wait(300);
  await wait(20);
  assert.equal(b.taskReads() - reads, 1, 'the board once');
  assert.equal(b.calls.filter((c, i) => i >= before && c.path === `/runs/${runId}/task`).length, 1, 'the task once');
  assert.equal(app.views.get(runId).projectTask.state, 'ci', 'into the held view');
  pj.take({ type: 'project', data: { id: B + 7, change: 'deleted' } });
  assert.equal(pj.opened, null, 'a deleted project closes');
  assert.match(pj.flash, /deleted/);
});

test('"Open PR": probed once per home, a 404 of the mux hides it', async () => {
  const calls = backend([['GET', /\/task\/pr$/, () => json({ error: 'method not allowed' }, 405)]], 'legacy');
  const app = fakeApp();
  const pj = M.createProjects(app);
  assert.equal(pj.prRouteAt('', 101), null, 'unknown at first');
  assert.equal(pj.prRouteAt('', 102), null);
  await wait(10);
  assert.equal(pj.prRouteAt('', 101), true);
  assert.equal(calls.length, 1, 'one probe per home');
  backend([], 'legacy');
  await assert.rejects(pj.taskAction(101, 'pr', {}), (e) => e.status === 404);
  assert.equal(pj.prRouteAt('', 101), false, 'the route is gone: hidden');
});
