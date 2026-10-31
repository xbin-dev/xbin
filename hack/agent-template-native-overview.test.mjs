// hack/agent-template-native-overview.test.mjs — a project's overview on the
// agent template's native screen (D214, API.md §Projects in the UI): the
// status first (the coordinator, worker agents, subagents, tokens with Day |
// Week and a chart, source control, the sandbox with its sparklines), the
// board with each task's agent, the coordinator last (its goal; idle, what
// wakes it; its newest messages) over the docked composer; the status's fold
// kept in your prefs; a part's tip as a sheet; the reads while the screen is on
// top; what older apps get; and a backend without the overview (the screen as
// before). Out of agent-template-native-projects.test.mjs (its size budget),
// over the same stubs (test/native-projects-stub.mjs).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { PHONE, FULL, OLD } from './agent-template-native-caps.mjs';
import { moreSeed, coordMsgs, overviewSeed } from '../builtin-templates/agent/test/projects-more-stub.mjs';
import { coordPlaceholder, TIPS } from '../builtin-templates/agent/model/project-overview.js';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);

// ovAt(): project 7's overview (day and week) as of the tests' clock (overviewSeed reads Date.now())
function ovAt(edit = (o) => o) {
  const real = Date.now;
  Date.now = () => NOW;
  try { return { 7: edit(overviewSeed(7, 'day')), '7:week': edit(overviewSeed(7, 'week')) }; } finally { Date.now = real; }
}
const seedOf = (extra = {}) => ({ ...moreSeed(), overview: ovAt(), ...extra });

async function run(seed, steps = [], caps = PHONE) {
  const r = await runNative({ caps, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-projects-stub.mjs', seed },
    steps: [{ wait: 60 }, ...steps], state: { hash: 'proj=7' } });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  assert.deepEqual(r.messages.filter((m) => m.op === 'error'), [], 'nothing the app could not draw');
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
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const bodies = (r, method, re) => called(r, method, re).map((c) => JSON.parse(c.body));
const nav = (tree) => find(tree, { t: 'nav' });
const topScreen = (tree) => { const n = nav(tree); return n.c[n.c.length - 1]; };
const sections = (screen) => all(screen, { t: 'section' }).map((s) => (s.p || {}).title).filter(Boolean);
const STATUS = { t: 'section', p: { title: 'Status' } };
const COORD = { t: 'section', p: { title: 'Coordinator' } };
const inStatus = (title) => ({ t: 'row', p: { title }, in: STATUS });
const reads = (r, range = 'day') => called(r, 'GET', new RegExp(`/projects/7/overview\\?range=${range}$`)).length;
const sub = (screen, title) => find(screen, { t: 'row', p: { title } }).p.subtitle;

test('the overview (D214): the status first — its rows and charts — the board with each task\'s agent, the coordinator last with its goal; read once', async () => {
  const r = await run(seedOf(), [{ snapshot: 'board' }, { tap: inStatus('acme/web') }, { wait: 20 }]);
  const s = topScreen(r.snapshots.board);
  assert.deepEqual(sections(s), ['Status', 'Queued (1)', 'Working (1)', 'Needs you (3)', 'PR (1)', 'Done (1)', 'Activity', 'Coordinator'], 'the status, the board, the coordinator');
  const status = find(s, STATUS);
  assert.equal(status.p.collapsible, true);
  assert.equal(status.p.footer, 'Tap a part for what it counts.');
  assert.deepEqual(all(status, { t: 'row' }).map((x) => x.p.title), ['Coordinator', 'Worker agents', 'Subagents', 'Tokens', 'GitHub', 'acme/web', 'acme/api', 'Sandbox'],
    'the parts in the web strip\'s order; source control under its provider\'s title');
  const row = (t) => find(status, { t: 'row', p: { title: t } }).p;
  assert.deepEqual([row('Coordinator').detail, row('Coordinator').tone, row('Coordinator').subtitle],
    ['Working', 'accent', 'turn just started · making a task for the flaky upload test · last: CI failed on #1 · 3 m ago · 6 turns today · 11 tasks / 7 d']);
  assert.deepEqual([row('Worker agents').detail, row('Worker agents').badge, row('Worker agents').tone, row('Worker agents').subtitle],
    ['2/3 slots', '3', 'warn', '7 tasks · 2 working · 3 need you · 2 idle · 1 queued · 4 Claude Code, 3 agent']);
  assert.deepEqual([row('Subagents').detail, row('Subagents').subtitle], ['4 running', '1 waiting · 17 finished in 24 h · 2 failed · under #2 ×2, #1, coordinator']);
  assert.deepEqual([row('Tokens').detail, row('Tokens').subtitle], ['1.84 M', 'last 24 h · workers 1.21 M · coordinator 380 k · subagents 250 k · + $6.40 coding agents reported']);
  assert.deepEqual([row('GitHub').tone, row('GitHub').subtitle], ['danger', '3 PRs of its tasks open · 1 failing · 1 awaiting review · 14 open issues · 5 merged in 7 d']);
  assert.deepEqual([row('acme/web').tone, row('acme/web').nav, row('acme/web').mono, row('acme/web').subtitle], ['ok', true, 'title', 'main · checks passed · 2 open PRs · 9 open issues · 4 merged in 7 d']);
  assert.deepEqual([row('acme/api').tone, row('acme/api').subtitle], ['danger', 'main · checks failed · 1 open PR · 5 open issues · 1 merged in 7 d']);
  assert.deepEqual([row('Sandbox').detail, row('Sandbox').tone, row('Sandbox').subtitle], ['running', 'ok', 'sb-web · 2 vCPU · 4.0 GiB · disk 3.1 GiB of 20 GiB · 2 commands running']);
  assert.deepEqual(r.messages.filter((m) => m.op === 'call' && m.what === 'open').map((m) => m.args.url), ['https://github.com/acme/web'], 'a repo opens its page');

  // the token chart: stacked (each line the top of its band), 24 hours
  assert.ok(find(status, { t: 'picker', p: { style: 'segmented', value: 'day' } }), 'Day | Week');
  const chart = find(status, { t: 'chart', p: { kind: 'area' } });
  assert.deepEqual([chart.p.x, chart.p.y, chart.p.series.map((x) => x.name)], ['time', 'number', ['worker agents', 'coordinator', 'subagents']]);
  const b = r.snapshots.board && seedOf().overview[7].tokens.buckets;
  assert.equal(chart.p.series[0].points.length, 24);
  assert.deepEqual(chart.p.series[0].points[3][1], b[3].workers + b[3].coordinator + b[3].subagents, 'workers on top of the coordinator on top of subagents');
  assert.deepEqual(chart.p.series[2].points[3][1], b[3].subagents);
  // the sandbox's sparklines, in its row
  const sparks = all(find(status, { t: 'row', p: { title: 'Sandbox' } }), { t: 'chart', p: { kind: 'spark' } });
  assert.deepEqual(sparks.map((x) => [x.p.series[0].name, x.p.series[0].points.length]), [['cpu', 60], ['mem', 60]]);
  assert.ok(find(status, { t: 'text', p: { text: 'CPU 63%' } }) && find(status, { t: 'text', p: { text: 'Memory 2.6 GiB' } }));

  // the board: each task's agent, what it does, its subagents
  assert.equal(sub(s, '#2 Add dark mode'), 'working · branch xbin/k3x9qa/2-task-2 · agent: working · 2 subagents · 120 k tokens');
  assert.match(sub(s, '#1 Fix the login loop'), / · Claude Code: working · 1 subagent · 210 k tokens$/);
  assert.match(sub(s, '#4 Prepare me'), / · agent: idle$/, 'no count where there is none');

  // the coordinator: its goal, then Open; the composer at the bottom writes to it
  const coord = find(s, COORD);
  assert.deepEqual([coord.c[0].p.title, coord.c[0].p.subtitle, coord.c[0].p.icon],
    ['Goal: Triage open bugs into tasks and keep main green; #9 first.', 'now: making a task for the flaky upload test', 'pin']);
  assert.equal(s.c[s.c.length - 1].t, 'composer');
  assert.equal(reads(r), 1, 'read once');
  // an iPad's composer has room for the longer words
  const wide = await run(seedOf(), [{ snapshot: 'board' }], { ...FULL, width: 'regular' });
  assert.equal(find(wide.snapshots.board, { t: 'composer' }).p.placeholder, 'Message the coordinator — your first message starts it…');
});

test('the overview: Day | Week reads the week; an app without area charts draws a line each; one without charts says the totals', async () => {
  const week = { t: 'picker', has: '"Week"' };
  const r = await run(seedOf(), [{ event: [week, 'change', { value: 'week' }] }, { wait: 30 }, { snapshot: 'week' }]);
  const s = topScreen(r.snapshots.week);
  assert.equal(reads(r, 'week'), 1);
  assert.equal(find(s, week).p.value, 'week');
  assert.equal(find(s, { t: 'row', p: { title: 'Tokens' } }).p.detail, '11.6 M');
  assert.equal(find(s, { t: 'chart', p: { kind: 'area' } }).p.series[0].points.length, 7, 'a point a day');

  const noArea = { ...PHONE, features: PHONE.features.filter((f) => f !== 'chart.area') };
  const l = await run(seedOf(), [{ snapshot: 'board' }], noArea);
  const line = find(l.snapshots.board, { t: 'chart', in: STATUS, p: { kind: 'line' } });
  const b = seedOf().overview[7].tokens.buckets;
  assert.equal(line.p.series[0].points[3][1], b[3].workers, 'lines: each its own, not stacked');
  assert.equal(find(l.snapshots.board, { t: 'chart', p: { kind: 'area' } }), null);

  const noChart = { ...PHONE, prims: { ...PHONE.prims } };
  delete noChart.prims.chart;
  const t = await run(seedOf(), [{ snapshot: 'board' }], noChart);
  assert.equal(find(t.snapshots.board, { t: 'chart' }), null, 'no chart where the app draws none');
  assert.match(find(t.snapshots.board, { t: 'row', p: { title: 'Sandbox' } }).p.subtitle, / · CPU 63% · memory 2\.6 GiB$/, 'the sandbox\'s numbers in words');

  const old = await run(seedOf(), [{ snapshot: 'board' }], OLD);
  assert.ok(find(old.snapshots.board, STATUS), 'an app of vocabulary rev 1 draws it all');
});

test('the status folds to one row, kept in your prefs; a tap on the row unfolds it', async () => {
  const r = await run(seedOf(), [
    { event: [STATUS, 'toggle', { collapsed: true }] },
    { wait: 30 },
    { snapshot: 'folded' },
    { tap: { t: 'row', has: 'coordinator working' } },
    { wait: 30 },
    { snapshot: 'open' },
  ]);
  const s = topScreen(r.snapshots.folded);
  assert.equal(find(s, STATUS).p.collapsed, true);
  const i = s.c.indexOf(find(s, STATUS));
  const line = find(s.c[i + 1], { t: 'row' });
  assert.deepEqual([line.p.title, line.p.subtitle, line.p.tone], ['coordinator working · workers 2 working · 3 need you',
    'subagents 4 · tokens 1.84 M/24 h · PRs 3 · 1 failing · sandbox running · CPU 63%', 'danger'], 'the strip as one row');
  assert.deepEqual(bodies(r, 'PUT', /\/api\/xbin\/prefs\/project-status-folded$/), [true, false]);
  assert.equal(find(topScreen(r.snapshots.open), STATUS).p.collapsed, false);
  assert.equal(find(topScreen(r.snapshots.open), { t: 'row', has: 'coordinator working' }), null);
  const again = await run(seedOf({ prefs: { 'project-status-folded': true } }), [{ snapshot: 'board' }]);
  assert.equal(find(topScreen(again.snapshots.board), STATUS).p.collapsed, true, 'read back');
});

test('a part\'s tip: a tap on it opens what it counts, its numbers in full and (the sandbox) its last hour; Done closes it', async () => {
  const r = await run(seedOf(), [
    { tap: inStatus('Sandbox') },
    { wait: 20 },
    { snapshot: 'sandbox' },
    { tap: { t: 'button', p: { label: 'Done' }, in: { t: 'sheet' } } },
    { wait: 20 },
    { snapshot: 'closed' },
    { tap: inStatus('Worker agents') },
    { wait: 20 },
    { snapshot: 'workers' },
    { event: [{ t: 'sheet' }, 'dismiss', {}] },
    { wait: 20 },
    { tap: { t: 'row', has: 'Goal: ', in: COORD } },
    { wait: 20 },
    { snapshot: 'goal' },
  ]);
  const sheet = find(topScreen(r.snapshots.sandbox), { t: 'sheet' });
  assert.equal(sheet.p.title, 'Sandbox');
  assert.equal(find(sheet, { t: 'text' }).p.text, TIPS.sandbox.slice('Sandbox. '.length), 'the web\'s tooltip');
  const fact = (sh, title) => { const x = find(sh, { t: 'row', p: { title } }); return x && (x.p.detail || x.p.subtitle); };
  assert.deepEqual(['Name', 'State', 'Size', 'Disk', 'Commands running', 'CPU now', 'Memory now'].map((k) => fact(sheet, k)),
    ['sb-web', 'running', '2 vCPU · 4.0 GiB', '3.1 GiB of 20 GiB', '2', '63%', '2.6 GiB of 4.0 GiB']);
  const charts = all(sheet, { t: 'chart', p: { kind: 'line' } });
  assert.deepEqual(charts.map((c) => c.p.y), ['percent', 'bytes']);
  assert.ok(charts[0].p.series[0].points.every(([, y]) => y >= 0 && y <= 1), 'CPU as a fraction of one core');
  assert.equal(find(topScreen(r.snapshots.closed), { t: 'sheet' }), null);
  const w = find(topScreen(r.snapshots.workers), { t: 'sheet' });
  assert.equal(w.p.title, 'Worker agents');
  assert.deepEqual([fact(w, 'Need you'), fact(w, 'Slots in use'), fact(w, 'Claude Code'), fact(w, 'agent')], ['3', '2 of 3', '4 tasks', '3 tasks']);
  const g = find(topScreen(r.snapshots.goal), { t: 'sheet' });
  assert.equal(g.p.title, 'Current goal');
  assert.deepEqual([fact(g, 'Goal'), fact(g, 'Now'), fact(g, 'Said by')],
    ['Triage open bugs into tasks and keep main green; #9 first.', 'making a task for the flaky upload test', 'the coordinator']);
});

test('the coordinator idle: the goal row says what wakes it, then the goal (yours, before it set one); the badge and the status say idle', async () => {
  const idle = ovAt((o) => ({ ...o, coordinator: { ...o.coordinator, state: 'idle', run: { id: 900, status: 'idle' }, goal: { text: 'fix the flaky test', by: 'you' } } }));
  const msgs = coordMsgs(900, [['user', 'fix the flaky test'], ['assistant', 'Made #12 for it.']]);
  const r = await run(seedOf({ overview: idle, coordinator: { 7: { status: 'idle', messages: msgs } } }), [{ snapshot: 'board' }]);
  const s = topScreen(r.snapshots.board);
  const coord = find(s, COORD);
  assert.equal(coord.p.badge, 'idle');
  assert.deepEqual([coord.c[0].p.title, coord.c[0].p.subtitle, coord.c[0].p.icon], ['Wakes for CI on #1, review on #42, new issues, your message', 'Goal: fix the flaky test', 'clock']);
  const st = find(s, inStatus('Coordinator')).p;
  assert.equal(st.detail, 'Idle');
  assert.match(st.subtitle, /^waiting for: CI on #1, review on #42, new issues, your message · last: /);
});

test('the overview is read again every minute while its screen is on top and on its project\'s events — not while covered or gone; back on top, at once', async () => {
  const event = { call: ['push', { type: 'project', run: 0, root: 0, data: { id: 7, change: 'coordinator' } }] };
  const shown = await run(seedOf(), [{ wait: 61000 }, event, { wait: 700 }]);
  assert.equal(reads(shown), 3, 'first, after a minute, after its event');
  const cover = [{ tap: { t: 'row', p: { title: '#1 Fix the login loop' } } }, { wait: 60 }, { snapshot: 'covered' }, { wait: 125000 }, event, { wait: 700 }];
  const covered = await run(seedOf(), cover);
  assert.equal(topScreen(covered.snapshots.covered).p.title, 'Fix the login loop', 'the task\'s conversation covers the board');
  assert.equal(reads(covered), 1, 'covered: nothing read for it');
  const back = await run(seedOf(), [...cover, { event: [{ t: 'nav' }, 'pop', { depth: 3 }] }, { wait: 60 }, { snapshot: 'back' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] }, { wait: 125000 }, event, { wait: 700 }]);
  assert.equal(topScreen(back.snapshots.back).p.title, 'Web');
  assert.equal(reads(back), 2, 'back on top: read at once; gone: nothing read');
});

test('a backend without the overview (404): the screen as before — the coordinator first, no status, no agent words; not read again', async () => {
  const r = await run(seedOf({ noOverview: true }), [{ snapshot: 'board' }, { wait: 61000 }]);
  const s = topScreen(r.snapshots.board);
  assert.deepEqual(sections(s), ['Coordinator', 'Queued (1)', 'Working (1)', 'Needs you (3)', 'PR (1)', 'Done (1)', 'Activity']);
  assert.equal(sub(s, '#2 Add dark mode'), 'working · branch xbin/k3x9qa/2-task-2');
  assert.equal(find(s, { t: 'composer' }).p.placeholder, 'Message the coordinator — this starts it…', 'as before (D212)');
  assert.equal(called(r, 'GET', /\/projects\/7\/overview/).length, 1, 'asked once');
});

test('the composer\'s words come from the model: before a coordinator exists, that your message starts it; short on a phone', () => {
  assert.equal(coordPlaceholder({ shown: true, run: null }), 'Message the coordinator — your first message starts it…');
  assert.equal(coordPlaceholder({ shown: true, run: null }, true), 'Message the coordinator — this starts it…');
  assert.equal(coordPlaceholder({ shown: false }, true), 'Message the coordinator…', 'a backend that can\'t say: as if it exists');
  assert.equal(coordPlaceholder({ shown: true, run: { id: 9 } }), 'Message the coordinator: make tasks for the open bugs, tell me what failed…');
});
