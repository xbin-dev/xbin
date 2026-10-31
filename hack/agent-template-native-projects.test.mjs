// hack/agent-template-native-projects.test.mjs — Projects in the agent
// template's native view (API.md §Projects in the UI): Projects atop your
// conversations (D209: needs-you first, All projects, the fold kept), the menu's row,
// the list, a project's board (ext.card's words), #proj and back, a new
// task, tasks from issues, the settings (status, the policy saved at its
// version, delete), the coordinator (its small chat), the activity, a new project, a task's
// conversation (its branch and PR menu, Open PR, the way back, the prep and
// sign-in cards, Retry), "Make this a project…", and team projects (the
// team board, its seed, Work on this, the team's changes) — and the model's words
// they share with the web (model/project-feed.js, project-team.js,
// project-upgrade.js). Rendered in node with hack/xbn/node.mjs over the
// browser tests' stubs (test/native-projects-stub.mjs). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav
import { projSeed, partitionSeed, task } from '../builtin-templates/agent/test/projects-stub.mjs';
import { moreSeed, teamSeed, ev, coordMsgs, overviewSeed } from '../builtin-templates/agent/test/projects-more-stub.mjs';
import { feedWords, plain, httpsUrl, projectFeed, coordTail, coordState } from '../builtin-templates/agent/model/project-feed.js';
import { boardWords, boardColumns, securityDiff, projectTeam } from '../builtin-templates/agent/model/project-team.js';
import { upgradeOffer, candidateWords } from '../builtin-templates/agent/model/project-upgrade.js';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const B = 2 ** 40;

async function run(seed, steps = [], hash = '') {
  const r = await runNative({ caps: PHONE, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-projects-stub.mjs', seed },
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
// ovAt(): project 7's overview (day and week) as of the tests' clock (overviewSeed reads Date.now())
function ovAt() {
  const real = Date.now;
  Date.now = () => NOW;
  try { return { 7: overviewSeed(7, 'day'), '7:week': overviewSeed(7, 'week') }; } finally { Date.now = real; }
}

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

test('Projects: the list\'s menu, the list, a project\'s board with ext.card\'s words', async () => {
  const r = await run(projSeed(), [
    { wait: 20 },
    { snapshot: 'drawer' },
    { tap: btn('Projects (3 need you)') },
    { wait: 60 },
    { snapshot: 'list' },
    { tap: rowIn('Web', 'Projects') },
    { wait: 60 },
    { snapshot: 'board' },
  ]);
  assert.ok(find(r.snapshots.drawer, { ...btn('Projects (3 need you)'), in: { t: 'menu' } }), 'in the list\'s menu, with the tasks that need you');
  assert.deepEqual(titles(r.snapshots.list), ['Agent', 'Projects']);
  assert.deepEqual(sections(topScreen(r.snapshots.list)), ['Projects', 'Archived']);
  const web = find(r.snapshots.list, rowIn('Web', 'Projects'));
  assert.match(web.p.subtitle, /^acme\/web, acme\/api · 1\/3 at work · 1 working · 1 queued · 1 in PR · 1 done|3 need you/);
  assert.equal(web.p.badge, '3');
  assert.deepEqual(titles(r.snapshots.board), ['Agent', 'Projects', 'Web']);
  const board = topScreen(r.snapshots.board);
  assert.deepEqual(sections(board), ['Coordinator', 'Queued (1)', 'Working (1)', 'Needs you (3)', 'PR (1)', 'Done (1)', 'Activity']);
  assert.equal(find(board, { t: 'row', p: { title: '#1 Fix the login loop' } }).p.subtitle, 'awaiting review · branch xbin/k3x9qa/1-task-1 · PR #42 open, checks failed');
  assert.equal(find(board, { t: 'row', p: { title: '#5 Merged one' } }).p.subtitle, 'merged · branch xbin/k3x9qa/5-task-5 · PR #40 merged · CI passed', 'checks only while no CI summary; ext.card (native/ci.js) last');
  assert.equal(lastHash(r), 'proj=7');
  assert.ok(called(r, 'GET', /\/projects\/7\/tasks\?/).length >= 1);
});

// many(): projSeed() with five more active projects, newer than Web — Web has tasks that need you
function many() {
  const s = projSeed();
  const web = s.projects[0];
  const extra = ['Docs', 'API', 'CLI', 'Mobile', 'Infra'].map((name, i) => ({ ...web, id: 20 + i, uid: `x${i}`, name, slug: name.toLowerCase(), updatedMs: web.updatedMs + 100 * (i + 1), repos: [] }));
  return { ...s, projects: [...s.projects, ...extra], tasks: { ...s.tasks, 24: [task(24, 1, { run: 301, column: 'working' }), task(24, 2, { run: 302, column: 'pr' })] } };
}
const LIST = { t: 'screen', p: { title: 'Agent' } };
const projSec = { t: 'section', p: { title: 'Projects' }, in: LIST };

test('Projects atop your conversations (D209): needs-you first, at most five, All projects; a tap opens the board; Mine only, not while searching', async () => {
  const r = await run(many(), [
    { wait: 40 },
    { snapshot: 'list' },
    { tap: { t: 'row', p: { title: 'Web' }, in: projSec } },
    { wait: 60 },
    { snapshot: 'board' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { wait: 30 },
    { event: [{ t: 'picker', p: { style: 'segmented' }, in: LIST }, 'change', { value: 'shared' }] },
    { wait: 30 },
    { snapshot: 'shared' },
    { event: [{ t: 'picker', p: { style: 'segmented' }, in: LIST }, 'change', { value: 'mine' }] },
    { event: [LIST, 'search', { value: 'chat' }] },
    { wait: 300 },
    { snapshot: 'search' },
    { event: [LIST, 'search', { value: '' }] },
    { wait: 300 },
    { tap: { t: 'row', p: { title: 'All projects' }, in: projSec } },
    { wait: 60 },
    { snapshot: 'all' },
  ]);
  const list = topScreen(r.snapshots.list);
  const sec = find(list, { t: 'section', p: { title: 'Projects' } });
  assert.ok(sec, 'the section, in the list');
  assert.deepEqual(all(sec, { t: 'row' }).map((x) => x.p.title), ['Web', 'Infra', 'Mobile', 'CLI', 'API', 'All projects'], 'needs you first, then by activity; at most five; the archived one left out');
  const web = find(sec, { t: 'row', p: { title: 'Web' } });
  assert.deepEqual([web.p.badge, web.p.tone, web.p.icon, web.p.nav], ['3', 'warn', 'folder', true]);
  assert.equal(find(sec, { t: 'row', p: { title: 'Infra' } }).p.subtitle, '1 working · 1 in PR', 'what is under way');
  assert.equal(find(sec, { t: 'row', p: { title: 'All projects' } }).p.detail, '7', 'every project the page lists');
  assert.equal(sec.p.collapsible, true);
  const order = all(list, { t: 'section' }).map((s) => (s.p || {}).title || (find(s, { t: 'picker' }) ? 'scope' : '?'));
  assert.ok(order.indexOf('scope') < order.indexOf('Projects'), `below the scope: ${order}`);
  assert.deepEqual(titles(r.snapshots.board), ['Agent', 'Web'], 'the project\'s board, over the list');
  assert.equal(find(topScreen(r.snapshots.shared), { t: 'section', p: { title: 'Projects' } }), null, 'not in Shared');
  assert.equal(find(topScreen(r.snapshots.search), { t: 'section', p: { title: 'Projects' } }), null, 'not while searching');
  assert.deepEqual(titles(r.snapshots.all), ['Agent', 'Projects'], 'All projects: the page');
  assert.ok(find(r.snapshots.list, { ...btn('Projects (3 need you)'), in: { t: 'menu' } }), 'the ⋯ menu keeps Projects (Shared, Archived)');
});

test('Projects atop your conversations: folding is kept in your prefs, and folded the section says how many tasks need you', async () => {
  const r = await run(many(), [
    { wait: 40 },
    { event: [projSec, 'toggle', { collapsed: true }] },
    { wait: 30 },
    { snapshot: 'folded' },
  ]);
  const sec = find(topScreen(r.snapshots.folded), { t: 'section', p: { title: 'Projects' } });
  assert.deepEqual([sec.p.collapsed, sec.p.badge], [true, '3']);
  assert.deepEqual(bodies(r, 'PUT', /\/api\/xbin\/prefs\/projects-folded$/), [true]);
  const again = await run({ ...many(), prefs: { 'projects-folded': true } }, [{ wait: 40 }, { snapshot: 'list' }]);
  const kept = find(topScreen(again.snapshots.list), { t: 'section', p: { title: 'Projects' } });
  assert.equal(kept.p.collapsed, true, 'read back at start');
});

test('Projects: a task opened from the board is pushed over it; back returns to the board (D190)', async () => {
  const r = await run(projSeed(), [
    { tap: rowIn('#1 Fix the login loop', 'Web') },
    { wait: 60 },
    { snapshot: 'task' },
    { event: [{ t: 'nav' }, 'pop', { depth: 3 }] },
    { wait: 60 },
    { snapshot: 'board' },
  ], 'proj=7');
  const t = titles(r.snapshots.task);
  assert.deepEqual(t.slice(0, 3), ['Agent', 'Projects', 'Web']);
  assert.equal(t.length, 4, 'the task\'s conversation on top');
  assert.ok(find(topScreen(r.snapshots.task), { t: 'composer' }), 'open');
  assert.ok(called(r, 'GET', /\/runs\/101\/view/).length, 'its conversation read');
  assert.deepEqual(titles(r.snapshots.board), ['Agent', 'Projects', 'Web'], 'back: the board');
  assert.ok(find(topScreen(r.snapshots.board), { t: 'section', p: { title: 'Coordinator' } }), 'the board, drawn');
  assert.equal(lastHash(r), 'proj=7', 'the project\'s address again');
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
  assert.deepEqual(find(find(s, { t: 'row', p: { title: 'bob' } }), btn('Remove')).p.confirm,
    { title: 'Remove bob from Web?', message: 'They lose their access; you can add them back.', label: 'Remove', destructive: true }, 'removing a member is confirmed');
  const [patch] = bodies(r, 'PATCH', /\/projects\/7$/);
  assert.equal(patch.version, 3);
  assert.equal(patch.policy.ci.autoFix, false);
  assert.deepEqual(patch.policy.futureKey, { kept: true }, 'a key this build doesn\'t know is kept');
  assert.equal(find(r.snapshots.saved, { t: 'section', p: { footer: 'Saved.' } }) !== null, true);
  assert.equal(called(r, 'DELETE', /\/projects\/7\?sandbox=keep$/).length, 1);
  assert.deepEqual(titles(r.snapshots.deleted), ['Agent', 'Projects']);
});

test('Projects: the coordinator — write to it (the composer docked at the bottom, D212), open it — Fork base now, and the activity (plain, https links, read since the last one)', async () => {
  const r = await run(moreSeed(), [
    { input: [{ t: 'composer' }, 'what failed?'] },
    { event: [{ t: 'composer' }, 'send', { value: 'what failed?' }] },
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
  assert.equal(find(topScreen(r.snapshots.sent), { t: 'composer' }).p.value, '', 'sent: the composer is empty again');
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

// the coordinator's section, a small chat (D209)
const coordSec = { t: 'section', p: { title: 'Coordinator' } };
const said = (sec) => all(sec, { t: 'message' }).map((m) => `${m.p.role}: ${m.p.text}`);

const openCoord = { t: 'row', p: { title: 'Open the coordinator' } };
const COORD_SAID = coordMsgs(900, [['user', 'make tasks for the open bugs'], ['assistant', 'On it — reading the board.'], ['tool', '#1 Fix the login loop …'],
  ['assistant', 'Made **3 tasks**: #9, #10 and #11.'], ['user', 'thanks'], ['assistant', 'Anything else?']]);

test('the coordinator as a small chat (a backend without the overview): its last three messages, what it does now, read again when its run moves — nothing made by reading', async () => {
  const r = await run({ ...moreSeed(), noOverview: true, coordinator: { 7: { status: 'running', messages: COORD_SAID } } }, [
    { wait: 60 },
    { snapshot: 'busy' },
    { call: ['push', { type: 'run', run: 900, root: 900, data: { id: 900, status: 'idle', parentId: 0, rootId: 900, origin: 'project', originId: 7 } }] },
    { wait: 1100 },
    { snapshot: 'idle' },
  ], 'proj=7');
  const sec = find(topScreen(r.snapshots.busy), coordSec);
  assert.deepEqual(said(sec), ['assistant: Made **3 tasks**: #9, #10 and #11.', 'user: thanks', 'assistant: Anything else?'], 'the last three said, its tool steps left to its chat');
  assert.equal(find(sec, { t: 'message', p: { role: 'assistant' } }).p.markdown, true);
  assert.ok(find(sec, { t: 'text', has: 'earlier messages are in its chat' }), 'more in its chat');
  assert.equal(sec.p.badge, 'working');
  assert.equal(find(sec, openCoord).p.subtitle, 'Working…');
  assert.deepEqual(sec.c.map((n) => n.t).filter((t) => t !== 'message'), ['row', 'text'], 'Open, then the messages — as before the overview');
  assert.equal(sections(topScreen(r.snapshots.busy))[0], 'Coordinator', 'first, as before');
  assert.equal(find(topScreen(r.snapshots.busy), { t: 'composer' }).p.placeholder, 'Message the coordinator…', 'the composer docked at the bottom writes to it (D212)');
  assert.equal(called(r, 'POST', /\/projects\/7\/coordinator$/).length, 0, 'reading makes no coordinator');
  assert.ok(called(r, 'GET', /\/projects\/7\/coordinator$/).length >= 1);
  const reads = called(r, 'GET', /\/runs\/900\/view\?limit=30$/).length;
  assert.ok(reads >= 2, `its run moved: read again (${reads})`);
  const idle = find(topScreen(r.snapshots.idle), coordSec);
  assert.equal(idle.p.badge, undefined, 'idle: no state said');
  assert.equal(find(idle, openCoord).p.subtitle, undefined);
});

test('the coordinator with the overview (D214): last on the screen, its goal, its newest messages (more than three), then Open with its model and tokens; idle said', async () => {
  const r = await run({ ...moreSeed(), overview: ovAt(), coordinator: { 7: { status: 'running', messages: COORD_SAID } } }, [
    { wait: 60 },
    { snapshot: 'busy' },
    { call: ['push', { type: 'run', run: 900, root: 900, data: { id: 900, status: 'idle', parentId: 0, rootId: 900, origin: 'project', originId: 7 } }] },
    { wait: 1100 },
    { snapshot: 'idle' },
  ], 'proj=7');
  const screen = topScreen(r.snapshots.busy);
  assert.equal(sections(screen).at(-1), 'Coordinator', 'the coordinator ends the screen, over its composer');
  const sec = find(screen, coordSec);
  assert.deepEqual(said(sec), ['user: make tasks for the open bugs', 'assistant: On it — reading the board.', 'assistant: Made **3 tasks**: #9, #10 and #11.', 'user: thanks', 'assistant: Anything else?'],
    'all five said (up to eight), its tool steps left to its chat');
  assert.equal(find(sec, { t: 'text', has: 'earlier messages are in its chat' }), null, 'none older');
  assert.equal(sec.p.badge, 'working');
  const order = sec.c.map((n) => (n.t === 'row' ? n.p.title.split(':')[0] : n.t)).filter((t, i, a) => t !== a[i - 1]);
  assert.deepEqual(order, ['Goal', 'message', 'Open the coordinator'], 'the goal, the messages, then Open');
  assert.equal(find(sec, openCoord).p.subtitle, 'Working… · Claude Opus 5.5 · 312 k tokens today');
  assert.equal(find(screen, { t: 'composer' }).p.placeholder, 'Message the coordinator…');
  const idle = find(topScreen(r.snapshots.idle), coordSec);
  assert.equal(idle.p.badge, 'idle', 'idle: said, as its run moves');
  assert.equal(find(idle, openCoord).p.subtitle, 'Claude Opus 5.5 · 312 k tokens today');
});

test('the coordinator before it exists: your first message starts it, then shows; an older backend shows the section as it was', async () => {
  const none = ovAt();
  for (const o of Object.values(none)) o.coordinator = { run: null, state: 'none', goal: null, wakesOn: [], lastEvent: null, turnsToday: 0, tasksMade7d: 0 };
  const r = await run({ ...moreSeed(), overview: none }, [
    { wait: 60 },
    { snapshot: 'none' },
    { input: [{ t: 'composer' }, 'what failed?'] },
    { event: [{ t: 'composer' }, 'send', { value: 'what failed?' }] },
    { wait: 60 },
    { snapshot: 'sent' },
  ], 'proj=7');
  const sec0 = find(topScreen(r.snapshots.none), coordSec);
  assert.ok(find(sec0, { t: 'text', has: 'your first message starts it' }));
  assert.equal(all(sec0, { t: 'message' }).length, 0);
  assert.equal(sec0.p.badge, 'not started');
  assert.equal(all(sec0, { t: 'row' }).length, 1, 'no goal yet: Open alone');
  assert.equal(find(topScreen(r.snapshots.none), { t: 'composer' }).p.placeholder, 'Message the coordinator — this starts it…');
  assert.equal(find(topScreen(r.snapshots.none), { t: 'row', p: { title: 'Coordinator' } }).p.detail, 'Not started', 'the status says so too');
  assert.deepEqual(bodies(r, 'POST', /\/projects\/7\/coordinator$/), [{ text: 'what failed?' }], 'made by the message, not before');
  const sent = find(topScreen(r.snapshots.sent), coordSec);
  assert.deepEqual(said(sent), ['user: what failed?']);
  assert.equal(sent.p.footer, 'Sent to the coordinator.');
  const old = await run({ ...moreSeed(), coordLookup: false, noOverview: true }, [{ wait: 60 }, { snapshot: 'board' }], 'proj=7');
  const sec = find(topScreen(old.snapshots.board), coordSec);
  assert.deepEqual(sec.c.map((n) => n.t), ['row'], 'Open — its messages need the lookup; the composer writes to it (D212)');
  assert.ok(find(topScreen(old.snapshots.board), { t: 'composer' }));
  assert.equal(called(old, 'GET', /\/runs\/\d+\/view/).length, 0);
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
  assert.equal(menu.p.label, 'xbin/k3x9qa/2-task-2');
  assert.deepEqual(all(menu, { t: 'button' }).map((b) => b.p.label), ['xbin/k3x9qa/2-task-2 ↗', 'setup passed', 'Open PR']);
  assert.equal(called(r, 'POST', /\/runs\/102\/task\/pr$/).length, 1);
  assert.deepEqual(titles(r.snapshots.back), ['Agent', 'Add dark mode', 'Web'], 'the project is pushed over its task: back returns to the task');
  // no route: no Open PR; a PR's chip with its checks
  const r2 = await run(projSeed(), [{ snapshot: 'pr' }], 'c=101');
  const m2 = find(topScreen(r2.snapshots.pr), { t: 'menu', p: { icon: 'branch' } });
  assert.equal(m2.p.label, 'PR #42 open');
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

test('the coordinator\'s preview: the last messages said, its state; while its chat is open the session\'s view, read by no one else; a backend without the lookup shows none', async () => {
  let n = 0;
  const blk = (k, text = 't') => ({ id: `${k}${n++}`, k, text });
  const blocks = [blk('user'), blk('assistant'), blk('tool'), blk('think'), blk('user', ''), blk('assistant'), blk('notice'), blk('user'), blk('draft')];
  assert.deepEqual(coordTail(blocks).map((x) => x.k), ['assistant', 'user', 'draft'], 'a person\'s and the agent\'s words; an empty user row (files only) and the steps left out');
  assert.deepEqual(['running', 'awaiting', 'waiting_input', 'error', 'idle', 'done'].map((status) => coordState({ status }).key), ['working', 'working', 'waiting', 'failed', 'idle', 'idle']);
  assert.equal(coordState({ status: 'idle', waiting: true }).word, 'waiting for you', 'a run below it waits');
  assert.equal(coordState(null).key, 'none');
  const g = globalThis.xbin;
  const seen = [];
  const answer = { lookup: () => new Response(JSON.stringify({ run: { id: 900, status: 'running' } }), { status: 200, headers: { 'Content-Type': 'application/json' } }) };
  globalThis.xbin = { self: 'apps/agent', fetch: async (url, opts = {}) => { seen.push(`${opts.method || 'GET'} ${url}`); return answer.lookup(url); } };
  const tick = () => new Promise((r) => setTimeout(r, 5));
  try {
    const live = { run: { id: 900, rootId: 900, status: 'running' }, messages: [], steps: [], links: [] };
    const session = { sel: 900, views: new Map([[900, live]]), runs: new Map(), merged: (id) => (id === 900 ? live : null), blocks: () => [blk('user', 'hi'), blk('assistant', 'hello')] };
    const app = { projects: { take() {}, opens: 1, opened: 7 }, page: 'projects', emit() {}, session };
    const f = projectFeed(app);
    f.preview(7);
    await tick();
    const p = f.preview(7);
    assert.deepEqual([p.shown, p.state.key, p.tail.map((b) => b.text)], [true, 'working', ['hi', 'hello']], 'its open chat, as the session holds it');
    assert.deepEqual(seen, ['GET /api/apps/agent/projects/7/coordinator'], 'looked up once; no view read while the stream keeps it');
    answer.lookup = () => new Response('Method Not Allowed\n', { status: 405 });
    const old = projectFeed({ projects: { take() {}, opens: 1, opened: 8 }, page: 'projects', emit() {}, session: { sel: null, views: new Map(), runs: new Map() } });
    old.preview(8);
    await tick();
    assert.deepEqual(old.preview(8), { shown: false }, 'a backend without GET …/coordinator: the card as it was');
  } finally { globalThis.xbin = g; }
});
