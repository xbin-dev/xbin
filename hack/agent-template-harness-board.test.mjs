// hack/agent-template-harness-board.test.mjs — the Coding agents board
// (plans/agtt-harness.md §8 U7): the words (model/harness-board.js — the
// chip, the filter, the sections, what an empty board says, the pinned
// task's Delegated), the store (app.board: rows from the tree, the links held
// and the stream, in creation order whatever changes; the tree read once
// something says a coding agent is there, and again only for a run it lacks;
// home: yours that run or need you), Needs you's sign-in (model/home.js), and
// the native view over kidsSeed() (native/harness-board.js: the toolbar
// button and the ⋯ item, the screen's sections, a park answered on the child,
// Message, Cancel task, the Task screen's Delegated, home). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const B = await import(new URL('model/harness-board.js', TPL));
const { needWords, REASON } = await import(new URL('model/home.js', TPL));
const { harnessSeed, kidsSeed, NOW } = await import(new URL('test/harness-fixtures.mjs', TPL));

const tick = () => new Promise((r) => setTimeout(r, 0));

// a fake app: the session's views (those held) and runs, the list's rows (with
// kids as the backend computes them), Needs, and GET /runs/{root}/tree
function fakeApp(s, { sel = 25, held = [25] } = {}) {
  const reads = [];
  let changes = 0;
  const views = new Map(held.map((id) => [id, s.views[id]]));
  const LIVE = ['running', 'awaiting', 'sleeping', 'waiting_input'];
  const rows = s.runs.filter((r) => !r.parentId).map((r) => {
    const kids = s.runs.filter((x) => x.rootId === r.id && x.id !== r.id);
    const k = { harness: kids.filter((x) => x.engine === 'harness' && LIVE.includes(x.status)).length, waiting: kids.filter((x) => x.status === 'waiting_input').length };
    return { access: 'owner', mine: true, ...r, ...(k.harness || k.waiting ? { kids: k } : {}) };
  });
  const session = {
    views, runs: new Map(), changed: () => { changes++; },
    current: () => (sel != null && views.has(sel) ? views.get(sel) : null),
    merged: (id) => views.get(id) || null,
    blocks: () => [],
  };
  const app = {
    sel, session, needs: s.needs,
    actions: { tree: async (root) => { reads.push(root); return structuredClone(s.trees[root] || { root, nodes: [], totals: {} }); } },
    convs: { find: (id) => rows.find((r) => r.id === id) || null, all: () => rows },
  };
  app.board = B.createBoard(app, { now: () => NOW });
  return { app, reads, changes: () => changes };
}
const ids = (rows) => rows.map((r) => r.id);

// --- the words ------------------------------------------------------------------------------

test('sectionOf, chipWords, filterWords, sectioned, emptyWords', () => {
  assert.deepEqual(['approval', 'question', 'login', 'starting', 'working', 'lost', 'done', 'idle', 'canceled', 'failed'].map(B.sectionOf),
    ['needs', 'needs', 'needs', 'running', 'running', 'running', 'done', 'done', 'done', 'done']);
  const r = (id, section) => ({ id, section });
  assert.equal(B.chipWords([]), null);
  const c = B.chipWords([r(1, 'running'), r(2, 'needs'), r(3, 'done')]);
  assert.deepEqual([c.text, c.n, c.needs, c.running, c.done, c.tone], ['⌨ 3 coding agents · 1 needs you', 3, 1, 1, 1, 'warn']);
  assert.equal(c.title, '1 waiting for you · 1 running · 1 done — open the Coding agents board');
  assert.equal(B.chipWords([r(1, 'needs'), r(2, 'needs')]).text, '⌨ 2 coding agents · 2 need you');
  assert.deepEqual([B.chipWords([r(1, 'running')]).text, B.chipWords([r(1, 'running')]).tone], ['⌨ 1 coding agent', 'run']);
  assert.equal(B.chipWords([r(1, 'done')]).tone, '');
  assert.equal(B.filterWords([r(1, 'running')], false), null, 'nothing needs you: no filter');
  assert.deepEqual(B.filterWords([r(1, 'needs'), r(2, 'needs')], false), { n: 2, on: false, text: '2 need you', title: 'show only the ones waiting for you' });
  assert.equal(B.filterWords([r(1, 'running')], true).text, 'needs you', 'on: it stays, to be turned off');
  assert.deepEqual(B.sectioned([r(3, 'done'), r(1, 'needs'), r(2, 'done')]).map((x) => [x.title, ids(x.rows)]), [['Needs you', [1]], ['Done', [3, 2]]]);
  assert.equal(B.emptyWords(false, false), 'no coding agents in this conversation');
  assert.equal(B.emptyWords(true, false), 'none of your coding agents is running or needs you');
  assert.equal(B.emptyWords(false, true), 'none of them needs you now');
});

test('Needs you: a sign-in names the coding agent when the item says which', () => {
  const s = harnessSeed();
  assert.equal(REASON.login, 'needs you to sign in');
  assert.equal(needWords(s.needs[0]), 'needs you to sign in to Codex', 'its own conversation');
  assert.equal(needWords({ reason: 'login', run: { id: 25 }, subRun: 28 }), 'a coding agent needs you to sign in', 'one below: not named');
  assert.equal(needWords({ reason: 'login', run: { id: 25 }, subRun: 28, harness: { name: 'Claude Code' } }), 'needs you to sign in to Claude Code');
  assert.equal(needWords(s.needs[1]), 'wants your approval');
  assert.equal(needWords({ reason: 'somethingnew', run: {} }), 'somethingnew');
});

// --- the store ---------------------------------------------------------------------------------

test('rows: the coding agents below the conversation, from the links held, then the tree — read once', async () => {
  const { app, reads, changes } = fakeApp(kidsSeed());
  const first = app.board.rows(25);
  assert.deepEqual(ids(first), [26, 27, 28], 'the held links say at once');
  assert.deepEqual(reads, [25], 'the tree is read');
  await tick(); await tick();
  assert.ok(changes() >= 1, 'its answer repaints');
  const rows = app.board.rows(25);
  assert.deepEqual(ids(rows), [26, 27, 28]);
  assert.deepEqual(rows.map((r) => [r.card.state.key, r.section]), [['working', 'running'], ['approval', 'needs'], ['login', 'needs']]);
  assert.equal(rows[0].card.task, 'Split the router into one file per resource', 'the spawn\'s task');
  assert.equal(rows[0].card.title, 'Split the router');
  assert.equal(rows[0].b.id, 'hb26', 'its own card id: the chat\'s card opens apart');
  assert.equal(rows[1].card.park.harness.callId, 'h1:k21', 'the link\'s child carries the park in full: answered in place');
  assert.equal(rows[0].view, app.session.current(), 'the open view says who may answer');
  assert.deepEqual(app.board.chip(25).text, '⌨ 3 coding agents · 2 need you');
  app.board.rows(25);
  assert.deepEqual(reads, [25], 'not again');
});

test('rows: the order is creation\'s — a change or a new one never moves a row', async () => {
  const s = kidsSeed();
  const { app, reads } = fakeApp(s);
  app.board.rows(25);
  await tick(); await tick();
  const h26 = s.runs.find((r) => r.id === 26).harness;
  app.board.take({ type: 'run', run: 26, root: 25, data: { id: 26, status: 'waiting_input', pendingState: { kind: 'question', park: 'Q', harness: { message: 'Keep them?', schema: {} } },
    harness: { ...h26, pending: { park: 'Q', kind: 'question', title: 'Keep them?' } } } });
  assert.deepEqual(reads, [25], 'a change of a run it has: no read');
  let rows = app.board.rows(25);
  assert.deepEqual(ids(rows), [26, 27, 28]);
  assert.equal(rows[0].section, 'needs', 'it asks now — still first');
  app.board.take({ type: 'harness', run: 27, root: 25, data: { ...rows[1].run.harness, state: 'working', pending: undefined, activity: { kind: 'thinking' } } });
  app.board.take({ type: 'run', run: 27, root: 25, data: { id: 27, status: 'running', pendingState: {} } });
  rows = app.board.rows(25);
  assert.deepEqual([rows[1].card.state.key, rows[1].card.status], ['working', 'Thinking…'], 'a harness event moves its status line');
  s.trees[25].nodes.push({ id: 29, parentId: 25, depth: 1, title: 'Write the tests', status: 'running', rawStatus: 'running', engine: 'harness', harness: { provider: 'codex', state: 'starting' } });
  app.board.take({ type: 'link', run: 25, root: 25, data: { id: 4, parentId: 25, childId: 29, state: 'running', label: 'Write the tests',
    child: { id: 29, parentId: 25, rootId: 25, status: 'running', engine: 'harness', harness: { provider: 'codex', state: 'starting' } } } });
  app.board.take({ type: 'run', run: 29, root: 25, data: { id: 29, parentId: 25, rootId: 25, status: 'running', engine: 'harness' } });
  assert.deepEqual(reads, [25, 25], 'a run the tree lacks: read again — one request in flight');
  assert.deepEqual(ids(app.board.rows(25)), [26, 27, 28, 29], 'the new one comes last, at once');
  await tick(); await tick(); await tick();
  assert.deepEqual(reads, [25, 25, 25], '…and once more for what came while it was in flight');
  assert.deepEqual(ids(app.board.rows(25)), [26, 27, 28, 29]);
  app.board.take({ type: 'run', run: 99, root: 7, data: { id: 99, engine: 'harness' } });
  assert.deepEqual(reads, [25, 25, 25], 'a root it doesn\'t watch');
});

test('rows: nothing says a coding agent is there — no tree read', () => {
  const s = harnessSeed();
  s.views[25].links = [];
  const { app, reads } = fakeApp(s);
  s.runs.forEach((r) => { if (r.rootId === 25 && r.id !== 25) r.status = 'idle'; });
  assert.deepEqual(app.board.rows(21), []);
  assert.equal(app.board.chip(21), null, 'no chip');
  assert.deepEqual(reads, []);
});

test('rows: the tree wins over an older summary (updated)', async () => {
  const s = kidsSeed();
  s.trees[25].nodes.find((n) => n.id === 27).updated = 200;
  Object.assign(s.trees[25].nodes.find((n) => n.id === 27), { rawStatus: 'running', harness: { ...s.trees[25].nodes.find((n) => n.id === 27).harness, pending: undefined } });
  s.views[25].links[1].child.updated = 100; // the view read before it was answered
  const { app } = fakeApp(s);
  app.board.rows(25);
  await tick(); await tick();
  assert.equal(app.board.rows(25)[1].card.state.key, 'working');
});

test('home: your coding-agent conversations that run or wait, and the ones at work below yours', async () => {
  const s = kidsSeed();
  const { app, reads } = fakeApp(s, { sel: null, held: [] });
  assert.deepEqual(ids(app.board.rows(null)), [22, 23, 24], 'the conversations at once (21 is idle)');
  assert.deepEqual(reads, [25], 'the tree of a row with coding agents at work');
  await tick(); await tick();
  const rows = app.board.rows(null);
  assert.deepEqual(ids(rows), [22, 23, 24, 26, 27, 28]);
  assert.deepEqual(rows.map((r) => r.root), [22, 23, 24, 25, 25, 25]);
  assert.equal(rows[0].card.park.harness.callId, 'h2:toolu_02', 'its row carries the park');
  assert.equal(rows[3].card.park, null);
  assert.equal(rows[4].card.park.harness, null, 'a child at home: only the compact park (the view reads it)');
  assert.equal(rows[4].view.access, 'owner', 'who answers: its conversation\'s row');
  assert.equal(app.board.chip(null).text, '⌨ 6 coding agents · 5 need you');
  app.board.take({ type: 'run', run: 25, root: 25, data: { id: 25, status: 'running' } });
  assert.deepEqual(reads, [25, 25], 'at home a root\'s change reads its tree again');
  const other = fakeApp(s, { sel: null, held: [] });
  other.app.convs.all().forEach((r) => { r.mine = false; });
  other.app.needs = [];
  assert.deepEqual(ids(other.app.board.rows(null)), [], 'only yours');
});

test('delegated: the coding agents below the run (its pinned task)', async () => {
  const s = kidsSeed();
  const { app } = fakeApp(s, { held: [25, 26] });
  app.board.rows(25);
  await tick(); await tick();
  assert.deepEqual(ids(app.board.delegated(s.views[25])), [26, 27, 28]);
  assert.deepEqual(ids(app.board.delegated(s.views[26])), [], 'none below a coding agent');
  const d = B.delegatedWords(app.board.delegated(s.views[25])[0]);
  assert.deepEqual([d.id, d.mono, d.title, d.task, d.state.word], [26, 'CC', 'Split the router', 'Split the router into one file per resource', 'working']);
});

// --- the native view over kidsSeed() ------------------------------------------------------------------

async function runSeed(steps, hash, s = kidsSeed()) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: NOW, self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: s },
    steps, state: hash ? { hash } : null });
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
const sent = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url)).map((c) => JSON.parse(c.body || 'null'));
const BOARD = { t: 'screen', p: { title: 'Coding agents' } };
const ROW = (title) => ({ t: 'row', p: { title }, in: BOARD });
const btn = (label, inRow) => ({ t: 'button', p: { label }, ...(inRow ? { in: ROW(inRow) } : {}) });

test('native: the toolbar button and ⋯ item open the board — its sections, rows and a park answered on the child', async () => {
  const r = await runSeed([
    { snapshot: 'chat' },
    { tap: btn('Coding agents (2 waiting)') }, { wait: 50 },
    { snapshot: 'board' },
    { event: [{ t: 'approval', in: ROW('Plan the users migration') }, 'choose', { id: 'approved' }] }, { wait: 50 },
    { snapshot: 'after' },
  ], 'c=25');
  const chat = r.snapshots.chat.root;
  const bar = find(chat, { t: 'button', p: { label: '2 coding agents need you' } });
  assert.equal(bar.p.icon, 'bell', 'the toolbar: while any needs you');
  const b = find(r.snapshots.board.root, BOARD);
  assert.equal(b.p.subtitle, 'in Refactor the API');
  const secs = all(b, { t: 'section' }).map((s) => [s.p.title, all(s, { t: 'row' }).map((x) => x.p.title)]);
  assert.deepEqual(secs, [['Needs you (2)', ['Plan the users migration', 'Write the changelog']], ['Running (1)', ['Split the router']]]);
  const r26 = find(b, ROW('Split the router'));
  assert.equal(r26.p.subtitle, 'Running: Run go vet ./... · 7 tool calls · 2 files +31 −4 · 19m');
  assert.equal(r26.p.detail, 'CC · working');
  assert.deepEqual(all(r26, { t: 'button' }).map((x) => x.p.label), ['Stop', 'Message', 'Cancel task'], 'its swipe actions');
  const ap = find(b, { t: 'approval', in: ROW('Plan the users migration') });
  assert.deepEqual(ap.p.options.map((o) => o.id), ['approved', 'approved-for-session', 'abort'], 'its park in place');
  assert.match(JSON.stringify(find(b, ROW('Write the changelog'))), /needs you to sign in/, 'a sign-in says so');
  assert.deepEqual(sent(r, 'POST', /\/runs\/27\/approve$/), [{ park: 'Xq3kid', option: 'approved' }], 'answered on the child');
  const after = all(find(r.snapshots.after.root, BOARD), { t: 'section' }).map((s) => [s.p.title, all(s, { t: 'row' }).map((x) => x.p.title)]);
  assert.deepEqual(after, [['Needs you (1)', ['Write the changelog']], ['Running (2)', ['Split the router', 'Plan the users migration']]],
    'answered: it moves to Running, in the order they started');
});

test('native: a coding agent\'s own chat has no toolbar button (its bar is full) — ⋯ → Coding agents', async () => {
  const r = await runSeed([{ snapshot: 'chat' }], 'c=26');
  const chat = r.snapshots.chat.root;
  assert.equal(find(chat, { t: 'button', p: { label: '2 coding agents need you' } }), null);
  assert.ok(find(chat, btn('Coding agents (2 waiting)')), 'the ⋯ item');
});

test('native: Message (Send, Send now) and Cancel task from a row', async () => {
  const r = await runSeed([
    { tap: btn('Coding agents (2 waiting)') }, { wait: 50 },
    { tap: btn('Message', 'Split the router') }, { wait: 50 },
    { snapshot: 'msg' },
    { input: [{ t: 'field', in: { t: 'screen', p: { title: 'Message Claude Code' } } }, 'keep the old routes'] }, { wait: 20 },
    { tap: btn('Send') }, { wait: 50 },
    { snapshot: 'sent' },
    { tap: btn('Message', 'Split the router') }, { wait: 50 },
    { input: [{ t: 'field', in: { t: 'screen', p: { title: 'Message Claude Code' } } }, 'stop, run the tests'] }, { wait: 20 },
    { tap: btn('Send now') }, { wait: 50 },
    { tap: btn('Cancel task', 'Split the router') }, { wait: 50 },
  ], 'c=25');
  const m = find(r.snapshots.msg.root, { t: 'screen', p: { title: 'Message Claude Code' } });
  assert.equal(find(m, { t: 'field' }).p.placeholder, 'Message Claude Code directly — the agent is told');
  assert.deepEqual(sent(r, 'POST', /\/runs\/26\/message$/).map((b) => [b.text, !!b.interrupt]), [['keep the old routes', false], ['stop, run the tests', true]]);
  const back = find(r.snapshots.sent.root, BOARD);
  assert.ok(back, 'back on the board');
  assert.match(find(back, { t: 'notice' }).p.text, /^Sent to Claude Code — #25's agent is told\.$/);
  assert.equal(sent(r, 'POST', /\/runs\/26\/cancel$/).length, 1);
});

test('native: the Task screen\'s Delegated section; at home, yours at work and Needs you\'s sign-in', async () => {
  const r = await runSeed([
    { tap: btn('Task') }, { wait: 50 },
    { snapshot: 'task' },
  ], 'c=25');
  const d = find(r.snapshots.task.root, { t: 'section', p: { title: 'Delegated' } });
  assert.deepEqual(all(d, { t: 'row' }).map((x) => [x.p.title, x.p.subtitle, x.p.detail]), [
    ['#26 Split the router', 'working · Split the router into one file per resource', 'CC'],
    ['#27 Plan the users migration', 'needs approval · Plan the users table migration', 'CX'],
    ['#28 Write the changelog', 'needs sign-in · Add the changelog entry', 'CC']]);
  const h = await runSeed([
    { snapshot: 'home' },
    { tap: btn('Coding agents (5 waiting)') }, { wait: 50 },
    { snapshot: 'board' },
  ], '');
  assert.equal(find(h.snapshots.home.root, btn('5 coding agents need you')), null, 'not on home\'s bar: Needs you is on the page, the board in ⋯');
  const need = find(h.snapshots.home.root, { t: 'row', p: { title: 'Port the CLI' }, in: { t: 'section', p: { title: 'Needs you' } } });
  assert.deepEqual([need.p.subtitle, need.p.icon], ['needs you to sign in to Codex', 'key']);
  const b = find(h.snapshots.board.root, BOARD);
  assert.equal(b.p.subtitle, 'yours — running or waiting for you');
  const secs = all(b, { t: 'section' }).map((s) => [s.p.title, all(s, { t: 'row' }).map((x) => x.p.title)]);
  assert.deepEqual(secs, [['Needs you (5)', ['Add retries to the client', 'Pick a JSON library', 'Port the CLI', 'Plan the users migration', 'Write the changelog']],
    ['Running (1)', ['Split the router']]]);
  assert.match(find(b, ROW('Split the router')).p.subtitle, /^in Refactor the API · /, 'a child says which conversation');
});

test('native: at home, a parked row whose child can\'t be read is read once — not at every paint; its park says to open it', async () => {
  const s = kidsSeed();
  s.routes = [['GET', '/runs/(26|27|28)/view', { error: 'bad gateway' }, 502]];
  const r = await runSeed([
    { tap: btn('Coding agents (5 waiting)') }, { wait: 2000 },
    { snapshot: 'board' },
  ], '', s);
  const n = (id) => r.calls.filter((c) => c.method === 'GET' && c.url.includes(`/runs/${id}/view`)).length;
  assert.deepEqual([n(27), n(28)], [1, 1], 'one read each in 2 s (their parks in full, to answer here)');
  const park = find(find(r.snapshots.board.root, ROW('Plan the users migration')), { t: 'notice' });
  assert.match(park.p.text, /open it \(↗\) to answer\.$/, 'the compact park: its own chat answers it');
});
