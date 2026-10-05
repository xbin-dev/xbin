// hack/agent-template-native-projects.test.mjs — Projects in the agent
// template's native view (API.md §Projects in the UI): the drawer's row,
// the list, a project's board (ext.card's words), #proj and back, a new
// task, tasks from issues, the settings (status, the policy saved at its
// version, delete), the coordinator, the activity, a new project, a task's
// conversation (its branch and PR menu, Open PR, the way back, the prep and
// sign-in cards, Retry), "Make this a project…", and team projects (the
// team board, its seed, Work on this, the team's changes) — and the model's words
// they share with the web (model/project-feed.js, project-team.js,
// project-upgrade.js). Rendered in node with hack/xbn/node.mjs over the
// browser tests' stubs (test/native-projects-stub.mjs). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { projSeed, partitionSeed } from '../builtin-templates/agent/test/projects-stub.mjs';
import { moreSeed, teamSeed, ev } from '../builtin-templates/agent/test/projects-more-stub.mjs';
import { feedWords, plain, httpsUrl, projectFeed } from '../builtin-templates/agent/model/project-feed.js';
import { boardWords, boardColumns, securityDiff, projectTeam } from '../builtin-templates/agent/model/project-team.js';
import { upgradeOffer, candidateWords } from '../builtin-templates/agent/model/project-upgrade.js';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const B = 2 ** 40;

async function run(seed, steps = [], hash = '') {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-projects-stub.mjs', seed },
    steps: [{ wait: 60 }, ...steps], state: hash ? { hash } : null });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

// all/find: nodes of a tree by type and props (a subset, compared as JSON),
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
const bodies = (r, method, re) => called(r, method, re).map((c) => JSON.parse(c.body));
const nav = (tree) => find(tree, { t: 'nav' });
const titles = (tree) => nav(tree).c.map((s) => s.p.title);
const topScreen = (tree) => { const n = nav(tree); return n.c[n.c.length - 1]; };
const sections = (screen) => all(screen, { t: 'section' }).map((s) => (s.p || {}).title).filter(Boolean);
const rowIn = (title, screen) => ({ t: 'row', p: { title }, in: { t: 'screen', p: { title: screen } } });
const btn = (label, extra = {}) => ({ t: 'button', p: { label }, ...extra });
const lastHash = (r) => (r.messages.filter((m) => m.op === 'state').pop() || { state: {} }).state.hash;

// --- the model's words, shared by both views -------------------------------------------------------------

test('the feed\'s words: plain, clipped, https links only, the kind said', () => {
  const w = feedWords({ id: 3, n: 1, kind: 'ci.failed', body: { text: 'x‮y\u0007 <b>z</b>', url: 'http://a.example' }, wake: true, created: 5 });
  assert.equal(w.text, 'xy <b>z</b>');
  assert.equal(w.url, '');
  assert.deepEqual([w.label, w.tone, w.wake], ['CI failed', 'bad', true]);
  assert.equal(feedWords({ id: 1, kind: 'pr.opened', body: '{"url":"https://github.com/a/b/pull/1"}' }).url, 'https://github.com/a/b/pull/1');
  assert.equal(feedWords({ id: 1, kind: 'weird.kind', body: {} }).label, 'weird.kind');
  assert.equal(plain('a'.repeat(400)).length, 300);
  assert.equal(httpsUrl('javascript:alert(1)'), '');
  assert.equal(httpsUrl('https://x.example/a b'), '', 'no spaces');
});

test('a team board row: plain, https links, opened only by its member from their own space, stale greyed', () => {
  const g = globalThis.xbin;
  globalThis.xbin = { partition: 'user:alice' };
  try {
    const row = { member: 'alice', n: 2, title: '<b>t</b>', col: 'pr', state: 'awaiting-review', waiting: 'review', branch: 'b', run: B + 5,
      prs: [{ repo: 'a/b', number: 1, url: 'http://evil.example', state: 'open' }], ci: { state: 'failure', current: 'test › go', url: 'https://ci.example' } };
    const w = boardWords(row, { user: 'alice' }, true);
    assert.equal(w.title, '<b>t</b>');
    assert.equal(w.state.text, 'awaiting review · waiting for a review');
    assert.equal(w.prs[0].url, '');
    assert.deepEqual(w.ci, { text: 'CI failure · test › go', tone: 'bad', url: 'https://ci.example' });
    assert.equal(w.open, B + 5);
    assert.equal(boardWords(row, { user: 'bob' }).open, 0, 'not another member\'s');
    assert.equal(boardWords({ ...row, run: 5 }, { user: 'alice' }).open, 0, 'never a run below 2^40');
    assert.equal(boardWords({ ...row, stale: true }, { user: 'alice' }).open, 0, 'not once they left');
    const cols = boardColumns([boardWords({ ...row, stale: true, n: 1 }, null), boardWords(row, null)]);
    assert.deepEqual(cols.find((c) => c.key === 'pr').rows.map((x) => x.num), [2, 1], 'stale rows last');
  } finally { globalThis.xbin = g; }
});

test('the security part side by side: changed keys and setup scripts marked', () => {
  const part = (setup, as) => ({ repos: [{ repo: 'a/web', setup }, { repo: 'a/api', setup: '' }], policy: { as, checks: ['go test'] } });
  const d = securityDiff(part('npm ci', 'person'), { ...part('npm ci && x', 'bot'), repos: [{ repo: 'a/web', setup: 'npm ci && x' }, { repo: 'a/new', setup: 'y' }] });
  assert.deepEqual(d.repos.map((r) => [r.repo, r.kind]), [['a/api', 'removed'], ['a/new', 'added'], ['a/web', 'changed']]);
  assert.deepEqual(d.keys.map((k) => [k.key, k.changed]), [['as', true], ['checks', false]]);
  assert.equal(d.keys[1].accepted, 'go test');
  const one = securityDiff(null, part('npm ci', 'person'));
  assert.ok(one.repos.every((r) => r.kind === 'same') && one.keys.every((k) => !k.changed), 'a first acceptance marks nothing');
});

test('Make this a project…: offered on a root conversation of yours with a sandbox, no project, not hosted', () => {
  const v = (run, extra = {}) => ({ access: 'owner', run: { id: 9, rootId: 9, parentId: 0, ...run }, config: { sandbox: { ref: 'm|a' } }, ...extra });
  assert.equal(upgradeOffer(v({})).shown, true);
  assert.equal(upgradeOffer(v({}, { config: {} })).shown, false, 'no sandbox');
  assert.equal(upgradeOffer(v({}, { project: { id: 1 } })).shown, false, 'a project already');
  assert.equal(upgradeOffer(v({ parentId: 3, rootId: 3 })).shown, false, 'a subagent');
  assert.equal(upgradeOffer(v({}, { access: 'participant' })).shown, false, 'not the owner');
  assert.equal(upgradeOffer(v({ hosted: { host: 'x' } })).shown, false, 'hosted');
  const c = candidateWords({ path: '/w/a', repo: 'acme/a', scm: 's', branch: 'main', dirty: 2, ssh: true });
  assert.deepEqual([c.usable, c.https, c.detail], [true, true, '/w/a · on main · 2 uncommitted changes · ssh remote']);
  assert.equal(candidateWords({ path: '/w/b', repo: 'x/y', host: 'gitlab.example' }).why, 'no scm provider serves gitlab.example');
});

// --- the screens -----------------------------------------------------------------------------------------

test('Projects: the drawer\'s row, the list, a project\'s board with ext.card\'s words', async () => {
  const r = await run(projSeed(), [
    { tap: btn('Conversations') },
    { wait: 20 },
    { snapshot: 'drawer' },
    { tap: { t: 'row', p: { title: 'Projects' } } },
    { wait: 60 },
    { snapshot: 'list' },
    { tap: rowIn('Web', 'Projects') },
    { wait: 60 },
    { snapshot: 'board' },
  ]);
  const row = find(r.snapshots.drawer, { t: 'row', p: { title: 'Projects' }, in: { t: 'sheet' } });
  assert.equal(row.p.badge, '3', 'the tasks that need you');
  assert.deepEqual(titles(r.snapshots.list), ['Agent', 'Projects']);
  assert.deepEqual(sections(topScreen(r.snapshots.list)), ['Projects', 'Archived']);
  const web = find(r.snapshots.list, rowIn('Web', 'Projects'));
  assert.match(web.p.subtitle, /^acme\/web, acme\/api · 1\/3 at work · 1 working · 1 queued · 1 in PR · 1 done|3 need you/);
  assert.equal(web.p.badge, '3');
  assert.deepEqual(titles(r.snapshots.board), ['Agent', 'Projects', 'Web']);
  const board = topScreen(r.snapshots.board);
  assert.deepEqual(sections(board), ['Coordinator', 'Queued (1)', 'Working (1)', 'Needs you (3)', 'PR (1)', 'Done (1)', 'Activity']);
  assert.equal(find(board, { t: 'row', p: { title: '#1 Fix the login loop' } }).p.subtitle, 'awaiting review · ⎇ xbin/k3x9qa/1-task-1 · PR #42 open ✗');
  assert.equal(find(board, { t: 'row', p: { title: '#5 Merged one' } }).p.subtitle, 'merged · ⎇ xbin/k3x9qa/5-task-5 · PR #40 merged · CI ✓', 'checks only while no CI summary; ext.card (native/ci.js) last');
  assert.equal(lastHash(r), 'proj=7');
  assert.ok(called(r, 'GET', /\/projects\/7\/tasks\?/).length >= 1);
});

test('Projects: #proj=<id> opens the list and the project; back leaves the project, then the page', async () => {
  const r = await run(projSeed(), [
    { snapshot: 'deep' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { wait: 30 },
    { snapshot: 'list' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { wait: 30 },
    { snapshot: 'home' },
  ], 'proj=7');
  assert.deepEqual(titles(r.snapshots.deep), ['Agent', 'Projects', 'Web']);
  assert.deepEqual(titles(r.snapshots.list), ['Agent', 'Projects']);
  assert.deepEqual(titles(r.snapshots.home), ['Agent']);
  assert.equal(lastHash(r), '', 'home has no address');
  assert.ok(r.messages.filter((m) => m.op === 'state').some((m) => m.state.hash === 'proj'), 'the list\'s address on the way');
});

test('Projects: a new task — what to do, big, a coding agent — and tasks from issues (plain, untrusted)', async () => {
  const r = await run(projSeed(), [
    { tap: btn('New task') },
    { wait: 20 },
    { snapshot: 'form' },
    { input: [{ t: 'field', p: { label: 'What to do' } }, 'fix the flaky test'] },
    { event: [{ t: 'picker', p: { label: 'Size' } }, 'change', { value: 'big' }] },
    { event: [{ t: 'toggle', p: { label: 'acme/api' } }, 'change', { value: false }] },
    { tap: btn('Create') },
    { wait: 60 },
    { snapshot: 'made' },
    { tap: btn('From issues…') },
    { wait: 60 },
    { snapshot: 'issues' },
    { tap: { t: 'row', p: { title: '#11 Login loops on Safari' } } },
    { tap: { t: 'row', p: { title: '#13 Locked one' } } },
    { tap: btn('Make 2 tasks') },
    { wait: 60 },
    { snapshot: 'batched' },
  ], 'proj=7');
  assert.equal(topScreen(r.snapshots.form).p.title, 'New task');
  assert.deepEqual(bodies(r, 'POST', /\/projects\/7\/tasks$/), [{ text: 'fix the flaky test', size: 'big', repos: ['web'] }]);
  assert.equal(topScreen(r.snapshots.made).p.title, 'Web', 'back on the board');
  assert.match(find(r.snapshots.made, { t: 'notice', p: { tone: 'ok' } }).p.text, /^Task #9 made\.$/);
  const issue = find(r.snapshots.issues, { t: 'row', p: { title: '#11 Login loops on Safari' } });
  assert.match(issue.p.subtitle, /<img src=x onerror="window.__pwned=1"> \*\*bold\*\*/, 'plain text, as it came');
  assert.match(find(r.snapshots.issues, { t: 'section', has: 'anyone may have written it' }).p.footer, /untrusted|anyone may/);
  assert.deepEqual(bodies(r, 'POST', /\/projects\/7\/tasks\/batch$/), [{ issues: [{ repo: 'acme/web', number: 11 }, { repo: 'acme/web', number: 13 }] }]);
  assert.equal(topScreen(r.snapshots.batched).p.title, 'From issues', 'a refused one keeps the picker open');
  assert.match(find(topScreen(r.snapshots.batched), { t: 'notice', p: { tone: 'warn' } }).p.text, /acme\/web#13: that issue is locked/);
});

test('Projects: settings — status, the policy saved at its version (unknown keys kept), members, delete', async () => {
  const r = await run(projSeed(), [
    { tap: btn('Project settings') },
    { wait: 60 },
    { snapshot: 'settings' },
    { event: [{ t: 'section', p: { title: 'Policy — CI and reviews' } }, 'toggle', { collapsed: false }] },
    { event: [{ t: 'toggle', p: { label: 'Tell a task when its CI fails' } }, 'change', { value: false }] },
    { tap: btn('Save policy') },
    { wait: 60 },
    { snapshot: 'saved' },
    { tap: btn('Delete project') },
    { wait: 60 },
    { snapshot: 'deleted' },
  ], 'proj=7');
  const s = topScreen(r.snapshots.settings);
  assert.equal(s.p.title, 'Settings');
  assert.ok(sections(s).includes('Status') && sections(s).includes('Repos') && sections(s).includes('Members') && sections(s).includes('Policy — CI and reviews'));
  assert.match(find(s, { t: 'row', p: { title: 'api' } }).p.subtitle, /not protected/);
  assert.match(find(s, { t: 'row', p: { title: 'github.com in apps/coding-sandbox|sb-web' } }).p.subtitle, /^alice-gh · live · until/);
  assert.match(find(s, { t: 'notice', p: { title: 'acme/api' } }).p.text, /no protection/);
  assert.equal(find(s, { t: 'button', p: { label: 'Sign in to GitHub' } }), null, 'no sign-in at an unpartitioned agent');
  assert.ok(find(s, { t: 'row', p: { title: 'bob', subtitle: 'makes and steers tasks' } }));
  const [patch] = bodies(r, 'PATCH', /\/projects\/7$/);
  assert.equal(patch.version, 3);
  assert.equal(patch.policy.ci.autoFix, false);
  assert.deepEqual(patch.policy.futureKey, { kept: true }, 'a key this build doesn\'t know is kept');
  assert.equal(find(r.snapshots.saved, { t: 'section', p: { footer: 'Saved.' } }) !== null, true);
  assert.equal(called(r, 'DELETE', /\/projects\/7\?sandbox=keep$/).length, 1);
  assert.deepEqual(titles(r.snapshots.deleted), ['Agent', 'Projects']);
});

test('Projects: the coordinator — write to it, open it — Fork base now, and the activity (plain, https links, read since the last one)', async () => {
  const r = await run(moreSeed(), [
    { input: [{ t: 'field', in: { t: 'section', p: { title: 'Coordinator' } } }, 'what failed?'] },
    { tap: btn('Send to the coordinator') },
    { wait: 30 },
    { snapshot: 'sent' },
    { tap: btn('Fork base now') },
    { wait: 30 },
    { tap: { t: 'row', p: { title: 'All activity' } } },
    { wait: 30 },
    { snapshot: 'feed' },
    { call: ['push', { type: 'project', run: 0, root: 0, data: { id: 7, change: 'event', n: 0 } }] },
    { wait: 400 },
    { event: [{ t: 'nav' }, 'pop', { depth: 3 }] },
    { tap: { t: 'row', p: { title: 'Open the coordinator' } } },
    { wait: 60 },
    { snapshot: 'coord' },
  ], 'proj=7');
  assert.deepEqual(bodies(r, 'POST', /\/projects\/7\/coordinator$/), [{ text: 'what failed?' }, {}]);
  assert.deepEqual(bodies(r, 'POST', /\/projects\/7\/fork-base$/), [{ now: true }], 'Fork base now (P2)');
  assert.equal(find(r.snapshots.sent, { t: 'section', p: { title: 'Coordinator' } }).p.footer, 'Sent to the coordinator.');
  const feed = topScreen(r.snapshots.feed);
  assert.equal(feed.p.title, 'Activity');
  assert.deepEqual(all(feed, { t: 'row' }).map((x) => x.p.title), ['note', '#1 comment (not forwarded)', '#1 CI failed', '#1 pull request opened', '#1 task made']);
  const ci = find(feed, { t: 'row', p: { title: '#1 CI failed' } });
  assert.match(ci.p.subtitle, /^test \(ubuntu\) failed <img src=x onerror="window.__pwned=1"> \*\*bold\*\*/);
  assert.equal(ci.p.badge, 'coordinator');
  assert.equal(find(ci, { t: 'button', p: { label: 'Open on the platform' } }), null, 'a javascript: link is none');
  assert.ok(find(find(feed, { t: 'row', p: { title: '#1 pull request opened' } }), { t: 'button', p: { label: 'Open on the platform' } }));
  assert.ok(!JSON.stringify(feed).includes('‮'));
  const reads = called(r, 'GET', /\/projects\/7\/events\?/).map((c) => c.url);
  assert.ok(reads.some((u) => /since=5/.test(u)), `a project event reads since the last one: ${reads}`);
  assert.equal(lastHash(r), 'c=900', 'the coordinator\'s conversation is open');
  assert.match(topScreen(r.snapshots.coord).p.subtitle, /^Web · coordinator/);
});

test('Projects: a new project — a repo, a name, a new sandbox — then its board', async () => {
  const seed = projSeed({ sbxManagers: [{ provider: 'apps/coding-sandbox', title: 'Coding sandboxes', ok: true, egress: ['none', 'internet'], images: [{ id: 'base', default: true }], sizes: [{ id: 's', default: true }] }] });
  const r = await run(seed, [
    { tap: btn('New project') },
    { wait: 60 },
    { snapshot: 'form' },
    { tap: { t: 'row', p: { title: 'acme/web' }, in: { t: 'screen', p: { title: 'New project' } } } },
    { wait: 20 },
    { tap: btn('Create') },
    { wait: 80 },
    { snapshot: 'made' },
  ], 'proj');
  const f = topScreen(r.snapshots.form);
  assert.equal(f.p.title, 'New project');
  assert.ok(find(f, { t: 'picker', p: { label: 'Provider', value: 'apps/scm-github' } }));
  assert.equal(find(f, { t: 'row', p: { title: 'acme/attic' } }).p.disabled, true, 'an archived repo can\'t be added');
  const [body] = bodies(r, 'POST', /\/projects$/);
  assert.equal(body.name, 'web');
  assert.deepEqual(body.repos, [{ repo: 'acme/web' }]);
  assert.deepEqual(body.sandbox.new, { provider: 'apps/coding-sandbox', image: 'base', size: 's', egress: 'internet' },
    'the manager\'s defaults, and internet: a sandbox made without an egress has no network, and the repo is cloned in it');
  assert.deepEqual(titles(r.snapshots.made), ['Agent', 'Projects', 'web']);
});

test('a task\'s conversation: its branch and PR menu, the way back, Open PR once the route exists', async () => {
  const r = await run({ ...projSeed(), prRoute: true }, [
    { snapshot: 'task' },
    { tap: btn('Open PR') },
    { wait: 30 },
    { tap: btn('Project: Web') },
    { wait: 60 },
    { snapshot: 'back' },
  ], 'c=102');
  const chat = topScreen(r.snapshots.task);
  assert.match(chat.p.subtitle, /^Web #2 · /, '‹project› #n');
  const menu = find(chat, { t: 'menu', p: { icon: 'branch' } });
  assert.equal(menu.p.label, '⎇ xbin/k3x9qa/2-task-2');
  assert.deepEqual(all(menu, { t: 'button' }).map((b) => b.p.label), ['⎇ xbin/k3x9qa/2-task-2 ↗', 'setup ✓', 'Open PR']);
  assert.equal(called(r, 'POST', /\/runs\/102\/task\/pr$/).length, 1);
  assert.deepEqual(titles(r.snapshots.back), ['Agent', 'Projects', 'Web']);
  // no route: no Open PR; a PR's chip with its checks
  const r2 = await run(projSeed(), [{ snapshot: 'pr' }], 'c=101');
  const m2 = find(topScreen(r2.snapshots.pr), { t: 'menu', p: { icon: 'branch' } });
  assert.equal(m2.p.label, 'PR #42 open ✗');
  assert.ok(!all(m2, { t: 'button' }).some((b) => b.p.label === 'Open PR'));
  assert.equal(called(r2, 'GET', /\/runs\/101\/task\/pr$/).length, 1, 'asked once, by GET');
});

test('a task\'s conversation: the prep card step by step, Retry when it failed; the sign-in card for its person, polled, then refreshed', async () => {
  const r = await run(projSeed(), [{ snapshot: 'prep' }], 'c=104');
  const prep = topScreen(r.snapshots.prep);
  assert.deepEqual(find(prep, { t: 'notice', p: { title: 'preparing the workspace' } }).p.text, 'cloning acme/api');
  assert.deepEqual(all(prep, { t: 'step' }).map((x) => x.p.text), ['web: ready', 'api: waiting']);
  const f = await run(projSeed(), [{ tap: btn('Retry the workspace', { in: { t: 'composer' } }) }, { wait: 30 }], 'c=108');
  assert.equal(called(f, 'POST', /\/runs\/108\/task\/retry$/).length, 1);
  const s = await run({ ...partitionSeed(), partition: 'user:alice' }, [{ snapshot: 'card' }, { wait: 2500 }, { snapshot: 'done' }], `c=${B + 106}`);
  const card = topScreen(s.snapshots.card);
  assert.ok(find(card, { t: 'text', p: { text: 'ABCD-1234' } }), 'the code, to the person who must sign in');
  assert.ok(JSON.stringify(find(card, { t: 'markdown' })).includes('https://github.com/login/device'), 'its page, a link');
  assert.ok(called(s, 'GET', /\/projects\/scm\/signin\/poll1\?scm=/).length >= 2, 'polled');
  assert.equal(called(s, 'POST', new RegExp(`/runs/${B + 106}/task/refresh$`)).length, 1, 'looked at again once it is done');
  assert.match(find(s.snapshots.done, { t: 'notice', p: { title: 'To push, this task needs your own sign-in' } }).p.text, /^Signed in/);
});

test('the sign-in card: after the poll gives up, Check again polls the same sign-in again', async () => {
  const POLL = '/projects/scm/signin/poll1\\?';
  const s = await run({ ...partitionSeed(), partition: 'user:alice' }, [
    { call: ['route', 'GET', POLL, { error: 'busy upstream' }, 502] },
    { wait: 400000 }, // eight failed polls, 5 s doubling to a minute
    { snapshot: 'gaveUp' },
    { call: ['route', 'GET', POLL, { state: 'done' }] },
    { tap: btn('Check again') },
    { wait: 2500 },
    { snapshot: 'done' },
  ], `c=${B + 106}`);
  const card = (snap) => find(snap, { t: 'notice', p: { title: 'To push, this task needs your own sign-in' } });
  assert.match(card(s.snapshots.gaveUp).p.text, /^Couldn't learn whether you signed in/);
  assert.equal(called(s, 'POST', new RegExp(`/runs/${B + 106}/task/refresh$`)).length, 1, 'looked at again once it is done');
  assert.match(card(s.snapshots.done).p.text, /^Signed in/);
  assert.equal(find(s.snapshots.done, btn('Check again')), null);
});

test('Make this a project…: the sandbox\'s repos, https, a name and a branch — then the conversation is task 1', async () => {
  const r = await run(moreSeed(), [
    { tap: btn('Make this a project…') },
    { wait: 40 },
    { snapshot: 'form' },
    { event: [{ t: 'toggle', p: { label: 'acme/web' } }, 'change', { value: false }] },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'API'] },
    { event: [{ t: 'picker', p: { label: 'Its branch' } }, 'change', { value: 'new' }] },
    { tap: btn('Make it') },
    { wait: 60 },
    { snapshot: 'done' },
  ], 'c=1');
  const f = topScreen(r.snapshots.form);
  assert.equal(f.p.title, 'Make this a project');
  assert.deepEqual(all(f, { t: 'toggle' }).map((t) => [t.p.label, t.p.value, !!t.p.disabled]), [
    ['acme/api', true, false], ['Switch its remote to https', true, false], ['acme/web', true, false], ['Switch its remote to https', true, false], ['x/y', false, true]]);
  assert.deepEqual(bodies(r, 'POST', /\/runs\/1\/project$/), [{ name: 'API', scm: 'apps/scm-github', repos: [{ path: '/work/api', repo: 'acme/api' }], branch: 'new', switchHttps: ['/work/api'] }]);
  assert.equal(topScreen(r.snapshots.done).p.title, 'Made a project');
  // a project's conversation is offered none
  const t = await run(projSeed(), [{ snapshot: 'm' }], 'c=102');
  assert.equal(find(t.snapshots.m, btn('Make this a project…')), null);
});

test('team projects: the team board, Work on this after the security part, the team\'s changes accepted', async () => {
  const seed = { ...teamSeed(), partition: 'user:alice' };
  const r = await run(seed, [
    { snapshot: 'board' },
    { event: [{ t: 'picker', p: { label: 'Seed' } }, 'change', { value: 'apps/coding-sandbox|seedbox' }] },
    { tap: btn('Set the seed') },
    { wait: 40 },
    { tap: btn('Work on this') },
    { tap: btn('Continue') },
    { wait: 40 },
    { snapshot: 'security' },
    { tap: btn('Accept and start') },
    { wait: 80 },
    { snapshot: 'mine' },
  ], 'proj=9');
  const b = topScreen(r.snapshots.board);
  assert.equal(b.p.subtitle, 'the team board');
  const alice = find(b, { t: 'row', p: { title: '#1 Alice\'s **task** <b>x</b>' } });
  assert.equal(alice.p.nav, true, 'yours: opens');
  const bob = find(b, { t: 'row', p: { title: '#3 Bob\'s task' } });
  assert.equal(!!bob.p.nav, false, 'another member\'s: never');
  assert.deepEqual(all(bob, { t: 'button' }).map((x) => x.p.label), ['PR #7 open ↗', 'CI ↗', 'Hide'], 'https links only (its checks are CI\'s), Hide for the owner');
  const carl = find(b, { t: 'row', p: { title: '#2 Carl left' } });
  assert.match(carl.p.subtitle, /no longer a member/);
  assert.equal(carl.p.tone, 'muted');
  assert.deepEqual(find(b, { t: 'picker', p: { label: 'Seed' } }).p.options.map((o) => o.value), ['', 'apps/coding-sandbox|seedbox'], 'the seed: yours shared with the team');
  assert.deepEqual(bodies(r, 'POST', /\/projects\/9\/seed$/), [{ sandbox: { ref: 'apps/coding-sandbox|seedbox' } }]);
  const made = { sandbox: { new: { provider: 'apps/coding-sandbox', image: 'base', size: 'small', egress: 'internet' } } }; // the seed's manager, internet: a sandbox made without an egress has no network
  assert.deepEqual(bodies(r, 'POST', /\/memberships$/), [{ team: 9, accept: '', ...made }, { team: 9, accept: 'd9', ...made }]);
  const sec = topScreen(r.snapshots.security);
  assert.ok(find(sec, { t: 'code', p: { text: 'npm ci && curl https://get.example | sh', wrap: true } }), 'the setup script, in full');
  assert.ok(find(sec, { t: 'row', p: { title: 'as' } }));
  assert.deepEqual(titles(r.snapshots.mine).slice(-1), ['Team site'], 'your half opens');
  assert.equal(lastHash(r), `proj=${B + 60}`);

  const v = await run(seed, [
    { tap: { t: 'row', p: { title: 'Review the team project\'s changes' } } },
    { wait: 40 },
    { snapshot: 'review' },
    { tap: btn('Accept') },
    { wait: 60 },
    { snapshot: 'accepted' },
  ], `proj=${B + 20}`);
  const rv = topScreen(v.snapshots.review);
  assert.equal(rv.p.title, 'The team\'s changes');
  assert.deepEqual(all(rv, { t: 'row', p: { tone: 'warn' } }).map((x) => x.p.title), ['acme/web', 'as']);
  assert.match(find(rv, { t: 'code', has: 'evil.sh' }).p.text, /^you accepted: npm ci\nthe team has now: npm ci && \.\/evil\.sh$/);
  assert.deepEqual(bodies(v, 'POST', new RegExp(`/memberships/${B + 20}/accept$`)), [{ hash: 'h2' }]);
  assert.match(find(topScreen(v.snapshots.accepted), { t: 'notice', p: { tone: 'ok' } }).p.text, /^Accepted/);
});

// --- fix round 1 ------------------------------------------------------------------------------------------

test('Work on this: a definition with no seed sends {new: {provider, …, egress: internet}}, the manager picked; a set seed is read-only, its manager\'s', async () => {
  const seed = { ...teamSeed(), partition: 'user:alice' };
  const r = await run(seed, [
    { tap: btn('Work on this') },
    { wait: 20 },
    { snapshot: 'form' },
    { tap: btn('Continue') },
    { wait: 40 },
    { tap: btn('Accept and start') },
    { wait: 80 },
  ], 'proj=9');
  const form = topScreen(r.snapshots.form);
  assert.deepEqual(find(form, { t: 'picker', p: { label: 'Manager' } }).p.options.map((o) => o.value), ['apps/coding-sandbox'], 'no seed: a manager of yours');
  assert.deepEqual(bodies(r, 'POST', /\/memberships$/), [
    { team: 9, accept: '', sandbox: { new: { provider: 'apps/coding-sandbox', image: 'base', size: 'small', egress: 'internet' } } },
    { team: 9, accept: 'd9', sandbox: { new: { provider: 'apps/coding-sandbox', image: 'base', size: 'small', egress: 'internet' } } }]);
  assert.equal(lastHash(r), `proj=${B + 60}`, 'your half is made (the backend refuses one with no sandbox and no seed)');

  const s2 = { ...teamSeed(), partition: 'user:alice' };
  s2.projects = s2.projects.map((p) => (p.id === 9 ? { ...p, sandboxRef: 'apps/coding-sandbox|seedbox' } : p));
  const v = await run(s2, [{ snapshot: 'board' }, { tap: btn('Work on this') }, { wait: 20 }, { snapshot: 'form' }, { tap: btn('Continue') }, { wait: 40 }], 'proj=9');
  const b = topScreen(v.snapshots.board);
  assert.equal(find(b, { t: 'picker', p: { label: 'Seed' } }), null, 'a set seed: nothing to pick');
  assert.equal(find(b, btn('Set the seed')), null);
  assert.equal(find(b, btn('Change the seed')), null);
  assert.ok(find(b, { t: 'row', p: { title: 'apps/coding-sandbox|seedbox', subtitle: 'the seed' } }), 'shown read-only');
  assert.equal(find(topScreen(v.snapshots.form), { t: 'picker', p: { label: 'Manager' } }), null, 'a seed served here: the backend forks it');
  assert.deepEqual(bodies(v, 'POST', /\/memberships$/), [{ team: 9, accept: '', sandbox: { new: { provider: 'apps/coding-sandbox', image: 'base', size: 'small', egress: 'internet' } } }], 'the seed\'s manager, with internet');
});

test('Work on this again: an archived membership is taken up again', async () => {
  const seed = { ...teamSeed(), partition: 'user:alice' };
  const gone = { ...seed.projects[3], id: B + 30, teamRef: 9, name: 'Team site', state: 'archived', defPending: '' };
  seed.projects = [...seed.projects, gone];
  seed.tasks = { ...seed.tasks, [B + 30]: [] };
  const r = await run(seed, [
    { snapshot: 'board' },
    { tap: btn('Work on this again') },
    { wait: 20 },
    { snapshot: 'form' },
    { tap: btn('Continue') },
    { wait: 40 },
    { tap: btn('Accept and start') },
    { wait: 80 },
  ], 'proj=9');
  const b = topScreen(r.snapshots.board);
  assert.equal(find(b, { t: 'row', p: { title: 'Your half of it' } }), null, 'an archived half is not "yours" now');
  assert.ok(find(b, btn('Work on this again')));
  assert.deepEqual(bodies(r, 'POST', /\/memberships$/), [{ team: 9, accept: '' }, { team: 9, accept: 'd9' }], 'it keeps its own sandbox: none is sent');
  const form = topScreen(r.snapshots.form);
  assert.equal(find(form, { t: 'picker', p: { label: 'Its sandbox' } }), null, 'no sandbox choice: the backend would ignore it');
  assert.equal(find(form, { t: 'picker', p: { label: 'Manager' } }), null);
  assert.equal(lastHash(r), `proj=${B + 30}`, 'the same half, active again');
});

test('a membership\'s page re-reads its definition when it opens: the team\'s changes found then are offered', async () => {
  const seed = { ...teamSeed(), partition: 'user:alice' };
  const found = seed.pending[B + 20];
  seed.projects = seed.projects.map((p) => (p.id === B + 20 ? { ...p, defPending: '' } : p));
  seed.pending = { [B + 20]: { hash: '', accepted: found.accepted, pending: null } };
  seed.syncOnRead = { [B + 20]: found };
  const r = await run(seed, [{ wait: 60 }, { snapshot: 'page' }], `proj=${B + 20}`);
  assert.equal(called(r, 'GET', new RegExp(`/memberships/${B + 20}/pending$`)).length, 1, 'read once as it opens');
  assert.ok(find(topScreen(r.snapshots.page), { t: 'row', p: { title: 'Review the team project\'s changes' } }), 'what the read found is offered');

  const quiet = { ...teamSeed(), partition: 'user:alice' };
  quiet.projects = quiet.projects.map((p) => (p.id === B + 20 ? { ...p, defPending: '' } : p));
  quiet.pending = {};
  const q = await run(quiet, [{ wait: 60 }, { snapshot: 'page' }], `proj=${B + 20}`);
  assert.equal(called(q, 'GET', new RegExp(`/memberships/${B + 20}/pending$`)).length, 1);
  assert.equal(find(topScreen(q.snapshots.page), { t: 'row', p: { title: 'Review the team project\'s changes' } }), null, 'nothing waits: no card');
});

test('the activity: a project\'s screen reads no events; Activity reads at most five pages, then Read newer goes on', async () => {
  const seed = moreSeed();
  seed.events = { 7: Array.from({ length: 1200 }, (_, i) => ev(i + 1, 7, 'note', 0, { text: `event ${i + 1}` })) };
  const r = await run(seed, [
    { snapshot: 'board' },
    { call: ['push', { type: 'project', run: 0, root: 0, data: { id: 7, change: 'event', n: 0 } }] },
    { wait: 400 },
    { snapshot: 'board2' },
    { tap: { t: 'row', p: { title: 'All activity' } } },
    { wait: 60 },
    { snapshot: 'feed' },
    { tap: { t: 'row', p: { title: 'Read newer' } } },
    { wait: 60 },
    { snapshot: 'newer' },
  ], 'proj=7');
  const reads = () => called(r, 'GET', /\/projects\/7\/events\?/).map((c) => new URL(c.url, 'http://x').searchParams.get('since'));
  assert.ok(!JSON.stringify(topScreen(r.snapshots.board2)).includes('event 1'), 'a project event while only the board is shown reads no events either');
  assert.deepEqual(reads(), ['0', '200', '400', '600', '800', '1000'], 'five pages, then the sixth on Read newer');
  assert.ok(find(topScreen(r.snapshots.feed), { t: 'row', p: { title: 'Read newer' } }), 'newer ones wait: said');
  const top = all(topScreen(r.snapshots.newer), { t: 'row' }).find((x) => /^note/.test(x.p.title));
  assert.match(top.p.subtitle, /^event 1200/, 'the newest first once read');
  assert.equal(find(topScreen(r.snapshots.newer), { t: 'row', p: { title: 'Read newer' } }), null);
});

test('board events for two definitions within 300 ms: each board is read again', async () => {
  const app = { projects: { take() {}, open() {}, opened: null, list: [], find: () => null }, emit() {} };
  const t = projectTeam(app);
  const loads = [];
  t.load = (pid) => { loads.push(pid); };
  t.boards.set(9, { items: [] });
  t.boards.set(10, { items: [] });
  app.projects.take({ type: 'project', data: { id: 9, change: 'board' } });
  app.projects.take({ type: 'project', data: { id: 10, change: 'board' } });
  app.projects.take({ type: 'project', data: { id: 9, change: 'board' } });
  await new Promise((res) => setTimeout(res, 350));
  assert.deepEqual(loads.sort((a, b) => a - b), [9, 10]);
});

test('Work on this before your sandbox managers are read: loading, not "no manager"; then the manager\'s defaults', () => {
  const g = globalThis.xbin;
  globalThis.xbin = { partition: 'user:alice' };
  try {
    let read = false;
    const mgr = { provider: 'apps/coding-sandbox', ok: true, egress: ['none', 'internet'], images: [{ id: 'base', default: true }], sizes: [{ id: 's', default: true }] };
    const sbx = { ensure() {}, answered: () => read, listAt: () => (read ? { sandboxes: [], managers: [mgr], loaded: true } : { sandboxes: [], managers: [], loaded: false }) };
    const def = { id: 9, kind: 'team', state: 'active', name: 'Team site', sandboxRef: '' };
    const app = { sbx, projects: { take() {}, open() {}, opened: null, list: [def], find: (id) => (+id === 9 ? def : null) }, emit() {} };
    const t = projectTeam(app);
    t.startWork(9);
    assert.equal(t.workLoading(), true);
    assert.match(t.workSandbox(9).error, /still loading/, 'not "no sandbox manager is bound"');
    read = true;
    assert.equal(t.workLoading(), false);
    assert.deepEqual(t.workSandbox(9), { sandbox: { new: { provider: 'apps/coding-sandbox', image: 'base', size: 's', egress: 'internet' } } });
  } finally { globalThis.xbin = g; }
});

test('the coordinator: a second send while one is under way queues nothing twice', async () => {
  const g = globalThis.xbin;
  const posts = [];
  let release;
  globalThis.xbin = { self: 'apps/agent', fetch: async (url, opts = {}) => {
    posts.push({ url, method: opts.method || 'GET' });
    await new Promise((r) => { release = r; });
    return new Response(JSON.stringify({ run: { id: 900 } }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  } };
  try {
    const app = { projects: { take() {} }, emit() {} };
    const f = projectFeed(app);
    const first = f.messageCoordinator(7, 'what failed?');
    assert.equal(await f.messageCoordinator(7, 'what failed?'), null, 'Enter again while it sends');
    release();
    assert.deepEqual(await first, { id: 900 });
    assert.equal(posts.filter((x) => x.method === 'POST').length, 1);
  } finally { globalThis.xbin = g; }
});
