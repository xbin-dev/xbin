// hack/agent-template-harness-child.test.mjs — coding agents the agent started,
// as their cards in the parent's chat (plans/agtt-harness.md §8 U6): the words
// (model/harness-child.js — which agent blocks are a harness child, the
// child's newest summary, each state's word, status line and counters, the
// actions it offers, the tail read once, a list row's coding agents), the
// row's `?` for a run waiting below it, and the native view over kidsSeed()
// (native/harness-child.js: the three cards, a permission answered on the
// child, the sign-in notice, the tail read when a card opens, ↗, Cancel task
// in a child's own chat, the drawer's ⧉ N). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const K = await import(new URL('model/harness-child.js', TPL));
const { Session, Failures } = await import(new URL('model/session.js', TPL));
const { fold } = await import(new URL('model/fold.js', TPL));
const { rowGlyph } = await import(new URL('model/rules.js', TPL));
const { harnessSeed, kidsSeed, NOW } = await import(new URL('test/harness-fixtures.mjs', TPL));

const agents = (v) => fold(v, () => null).filter((b) => b.k === 'agent');
const cardOf = (s, id, held = null) => {
  const b = agents(s.views[25]).find((x) => x.childId === id);
  return K.childCard(b, K.childRun(b, held), NOW);
};

// --- the words --------------------------------------------------------------------------

test('isHarnessChild: a spawn whose child a harness drives, or one that asked for a harness', () => {
  const bs = agents(harnessSeed().views[25]);
  assert.deepEqual(bs.map((b) => K.isHarnessChild(b)), [true, true, true]);
  const call = (args, extra = {}) => ({ k: 'agent', args: JSON.stringify(args), ...extra });
  assert.equal(K.isHarnessChild(call({ task: 'x', harness: 'codex' })), true, 'the call asked for one: before its link');
  assert.equal(K.isHarnessChild(call({ task: 'x' })), false, 'a built-in subagent');
  assert.equal(K.isHarnessChild(call({ task: 'x', harness: 'codex' }, { child: { engine: '' } })), false, 'its summary says otherwise');
  assert.equal(K.isHarnessChild({ k: 'tool', harness: {} }), false);
});

test('childRun: the link\'s child with what the stream said since on top', () => {
  const b = agents(harnessSeed().views[25])[0];
  const r = K.childRun(b, { status: 'waiting_input', harness: { provider: 'claude', state: 'working', activity: { kind: 'thinking' } } });
  assert.equal(r.id, 26);
  assert.equal(r.status, 'waiting_input');
  assert.equal(r.harness.activity.kind, 'thinking');
  assert.equal(r.title, 'Split the router', 'the rest is the link\'s');
  const early = K.childRun({ k: 'agent', childId: 0, args: '{"task":"t","harness":"codex"}' });
  assert.deepEqual([early.engine, early.harness.provider, early.harness.state], ['harness', 'codex', 'starting']);
});

test('childCard: working through its plan — identity, the status line, where, counters, time', () => {
  const c = cardOf(kidsSeed(), 26);
  assert.deepEqual([c.id, c.parent, c.provider, c.mono, c.name, c.title], [26, 25, 'claude', 'CC', 'Claude Code', 'Split the router']);
  assert.equal(c.task, 'Split the router into one file per resource');
  assert.deepEqual(c.state, { key: 'working', word: 'working', tone: 'run' });
  assert.equal(c.status, 'Running: Run go vet ./...');
  assert.equal(c.where, 'api-dev:/work/api');
  assert.equal(c.whereIcon, 'box', 'the web draws the sandbox glyph before it');
  assert.equal(c.meta, '7 tool calls · 2 files +31 −4 · 19m', 'counters, then the time since it started');
  assert.equal(c.plan.text, '1/3 · now: Split the router');
  assert.equal(c.park, null);
  assert.deepEqual(c.can, { stop: true, cancel: true, message: true });
  const thinking = cardOf(kidsSeed(), 26, { harness: { ...kidsSeed().runs.find((r) => r.id === 26).harness, activity: { kind: 'thinking' } } });
  assert.equal(thinking.status, 'Thinking…', 'a harness event moves it');
});

test('childCard: parked — a permission, a plan approval, a question, a sign-in', () => {
  const k = kidsSeed();
  const p = cardOf(k, 27);
  assert.deepEqual(p.state, { key: 'approval', word: 'needs approval', tone: 'warn' });
  assert.equal(p.status, 'waiting for your approval: psql -f migrations/0007_users.sql');
  assert.equal(p.park.park, 'Xq3kid');
  assert.equal(p.park.harness.callId, 'h1:k21', 'the full card data: the summary carries pendingState');
  assert.equal(p.can.stop, true, 'Stop settles the park');
  assert.equal(cardOf(harnessSeed(), 27).status, 'waiting for you to approve its plan');
  const l = cardOf(k, 28);
  assert.deepEqual([l.state.key, l.state.word, l.status], ['login', 'needs sign-in', 'needs you to sign in to Claude Code']);
  assert.deepEqual(l.can, { stop: false, cancel: true, message: true }, 'a sign-in waits for no turn: nothing to stop');
  const q = kidsSeed();
  Object.assign(q.runs.find((r) => r.id === 27), { pendingState: { kind: 'question', park: 'Q1', harness: { message: 'Which schema?', schema: {} } } });
  q.runs.find((r) => r.id === 27).harness.pending = { park: 'Q1', kind: 'question', title: 'Which schema?' };
  assert.deepEqual([cardOf(q, 27).state.word, cardOf(q, 27).status], ['asks you', 'asks: Which schema?']);
});

test('childCard: done, canceled, failed, cut off, starting', () => {
  const d = cardOf(harnessSeed(), 28);
  assert.deepEqual([d.state.key, d.status, d.answer], ['done', 'Added the 2026-09-30 entry.', 'Added the 2026-09-30 entry.']);
  assert.deepEqual(d.can, { stop: false, cancel: false, message: true }, 'a message starts its next turn');
  const s = harnessSeed();
  Object.assign(s.views[25].links[0], { state: 'canceled' });
  const x = cardOf(s, 26);
  assert.deepEqual([x.state.key, x.status, x.can.cancel, x.can.message], ['canceled', 'canceled', false, false]);
  const f = harnessSeed();
  Object.assign(f.runs.find((r) => r.id === 26), { status: 'error' });
  f.runs.find((r) => r.id === 26).harness.error = 'claude-agent-acp exited (code 1)';
  assert.deepEqual([cardOf(f, 26).state.word, cardOf(f, 26).status], ['failed', 'claude-agent-acp exited (code 1)']);
  const lost = harnessSeed();
  Object.assign(lost.runs.find((r) => r.id === 26).harness, { state: 'lost', error: 'the sandbox stopped' });
  lost.runs.find((r) => r.id === 26).status = 'idle';
  assert.equal(cardOf(lost, 26).status, 'Claude Code stopped (the sandbox stopped) — its next message resumes it');
  const b = { k: 'agent', id: 'cs9', childId: 0, args: '{"task":"write the docs","harness":"codex"}', headline: 'write the docs', state: 'running' };
  const st = K.childCard(b, K.childRun(b), NOW);
  assert.deepEqual([st.state.key, st.status, st.title, st.mono, st.id], ['starting', 'Starting Codex…', 'write the docs', 'CX', 0]);
});

test('the words of Stop, Cancel and Message', () => {
  const c = cardOf(kidsSeed(), 26);
  assert.match(K.cancelWords(c), /^Cancel Claude Code's task \(#26\)\? It stops for good/);
  const w = K.messageWords(c);
  assert.equal(w.placeholder, 'Message Claude Code directly — the agent is told');
  assert.equal(w.sent(false), 'Sent to Claude Code — #25\'s agent is told.');
  assert.equal(w.sent(true), 'Sent to Claude Code, its turn interrupted — #25\'s agent is told.');
  // a coding agent's own conversation (the home board's row): no agent to tell
  const root = { ...c, id: 22, parent: 0 };
  assert.equal(K.cancelWords(root), 'Cancel Claude Code\'s task (#22)? It stops for good.');
  assert.deepEqual([K.messageWords(root).placeholder, K.messageWords(root).sent(true)], ['Message Claude Code', 'Sent to Claude Code, its turn interrupted.']);
});

test('tailOf and loadTail: the last 3 blocks, read once, with a small page', async () => {
  assert.equal(K.tailOf({ blocks: null }), null);
  assert.deepEqual(K.tailOf({ blocks: [1, 2, 3, 4, 5] }), [3, 4, 5]);
  assert.deepEqual(K.tailOf({ blocks: [1] }), [1]);
  const reads = [];
  let changed = 0;
  const s = { views: new Map(), loading: new Set(), failed: new Failures(), changed: () => { changed++; },
    fetchView: async (id, o) => { reads.push([id, o]); s.views.set(id, {}); } };
  assert.equal(K.loadTail(s, 26), true);
  assert.equal(K.loadTail(s, 26), false, 'one read at a time');
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(K.loadTail(s, 26), false, 'held: the stream keeps it');
  assert.equal(K.loadTail(s, 0), false);
  assert.deepEqual(reads, [[26, { paged: true, limit: 8 }]]);
  assert.equal(changed, 1);
});

test('a tail read that fails is not tried again at every paint: it waits, doubling; an event, Retry or a success clears it', async () => {
  let now = 1e6;
  const reads = [];
  let fails = true;
  let changed = 0;
  const s = { views: new Map(), loading: new Set(), failed: new Failures(() => now), changed: () => { changed++; },
    fetchView: async (id) => {
      reads.push(id);
      if (fails) throw new Error('bad gateway');
      s.views.set(id, {});
      s.failed.clear(id); // as Session.fetchView does
    } };
  const tick = () => new Promise((r) => setTimeout(r, 0));
  assert.equal(K.loadTail(s, 27), true);
  await tick();
  assert.deepEqual([reads.length, changed, K.tailError(s, 27)], [1, 1, "Couldn't read its latest steps: bad gateway"], 'it failed: the card says why (and repaints)');
  for (let i = 0; i < 50; i++) assert.equal(K.loadTail(s, 27), false, 'the paints that follow read nothing');
  now += 14e3;
  assert.equal(K.loadTail(s, 27), false, 'not before 15 s');
  now += 1e3;
  assert.equal(K.loadTail(s, 27), true, 'after 15 s: once more');
  await tick();
  now += 15e3;
  assert.equal(K.loadTail(s, 27), false, 'a second failure in a row waits 30 s');
  now += 15e3;
  assert.equal(K.loadTail(s, 27), true);
  await tick();
  for (let i = 0; i < 6; i++) { now += 200e3; K.loadTail(s, 27); await tick(); }
  now += 119e3;
  assert.equal(K.loadTail(s, 27), false, 'at most 2 min…');
  now += 1e3;
  assert.equal(K.loadTail(s, 27), true, '…and no more');
  await tick();
  s.failed.soon(27);
  now += 1e3;
  assert.equal(K.loadTail(s, 27), false, 'its run moved: 2 s after it failed…');
  now += 1e3;
  assert.equal(K.loadTail(s, 27), true, '…not the whole wait');
  await tick();
  s.failed.clear(27); // Retry
  fails = false;
  assert.equal(K.loadTail(s, 27), true, 'Retry: at once');
  await tick();
  assert.deepEqual([s.views.has(27), K.tailError(s, 27), K.loadTail(s, 27)], [true, '', false], 'read: nothing to say, nothing to read');
  assert.equal(K.tailError(s, 0), '');
});

test('Session: a subagent card\'s read (loadChild) that fails waits too; a stream reset, an event and Retry read it again; a good one is read as before', async () => {
  let now = 5e6;
  const s = new Session('/api/apps/agent', { change() {}, frame: (f) => f() });
  s.failed = new Failures(() => now);
  const real = s.fetchView.bind(s);
  const reads = [];
  let answer = { status: 502, body: { error: 'bad gateway' } };
  const had = { window: globalThis.window, fetch: globalThis.fetch };
  globalThis.window = { xbin: { self: 'apps/agent' } };
  globalThis.fetch = async (url) => { reads.push(url); return new Response(JSON.stringify(answer.body), { status: answer.status }); };
  const tick = () => new Promise((r) => setTimeout(r, 0));
  try {
    s.fetchView = (id, o) => real(id, o);
    s.loadChild(40);
    await tick(); await tick();
    assert.deepEqual(reads, ['/api/apps/agent/runs/40/view'], 'one whole-view read');
    assert.equal(s.ui.readError(40), 'bad gateway', 'the card says why');
    for (let i = 0; i < 20; i++) s.ui.act.loadChild(40);
    await tick();
    assert.equal(reads.length, 1, 'the paints that follow read nothing');
    s.apply({ type: 'run', run: 40, root: 1, data: { id: 40, status: 'running' } });
    now += 2e3;
    s.loadChild(40);
    await tick(); await tick();
    assert.equal(reads.length, 2, 'its run moved: read again 2 s after it failed');
    s.loadChild(40);
    s.reload(); // the stream could not replay: the backend is back
    s.loadChild(40);
    await tick(); await tick();
    assert.equal(reads.length, 3, 'a stream reset: read again');
    s.ui.act.retryRead(40);
    answer = { status: 200, body: { run: { id: 40, status: 'done' }, messages: [] } };
    s.loadChild(40);
    await tick(); await tick();
    assert.deepEqual([reads.length, s.views.has(40), s.ui.readError(40)], [4, true, ''], 'Retry: read, held, nothing to say');
    s.loadChild(41);
    await tick(); await tick();
    assert.deepEqual([reads.length, s.views.has(41), s.ui.readError(41)], [5, true, ''], 'a child that answers is read at once, as before');
  } finally {
    globalThis.window = had.window;
    globalThis.fetch = had.fetch;
  }
});

test('a list row: ? for a run waiting below it; the agents glyph and N coding agents at work', () => {
  assert.equal(rowGlyph({ status: 'awaiting', waiting: true }), 'ask');
  assert.equal(rowGlyph({ status: 'awaiting' }), 'spin');
  assert.equal(K.kidsWords({}), null);
  assert.equal(K.kidsWords({ kids: { harness: 0, waiting: 2 } }), null, 'only waiting: the ? says it');
  assert.deepEqual(K.kidsWords({ kids: { harness: 3, waiting: 2 } }),
    { text: '3', icon: 'agent', label: '3 coding agents', title: '3 coding agents at work in this conversation · 2 runs below wait for you' });
  assert.equal(K.kidsWords({ kids: { harness: 1, waiting: 1 } }).title, '1 coding agent at work in this conversation · one run below waits for you');
});

// --- the native view over kidsSeed() -----------------------------------------------------------

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
const reads = (r, re) => r.calls.filter((c) => c.method === 'GET' && re.test(c.url)).map((c) => c.url);
const CARD = (title) => ({ t: 'toolcard', p: { family: 'agent', title } });

test('native: three cards — identity as chips, the status line, where; a park opens its card, drawn on the child', async () => {
  const r = await runSeed([
    { snapshot: 'p' },
    { event: [{ t: 'approval', in: CARD('Plan the users migration') }, 'choose', { id: 'approved' }] }, { wait: 50 },
  ], 'c=25');
  const t = r.snapshots.p.root;
  const cards = all(t, { t: 'toolcard', p: { family: 'agent' } });
  assert.deepEqual(cards.map((c) => [c.p.title, c.p.icon, c.p.state, c.p.open]), [
    ['Split the router', 'sparkles', 'running', false], ['Plan the users migration', 'terminal', 'running', true], ['Write the changelog', 'sparkles', 'running', true]]);
  assert.deepEqual(cards[0].p.chips, [{ text: 'CC' }, { text: '#26' }, { text: 'working', tone: 'accent' }]);
  assert.deepEqual(cards[1].p.chips[2], { text: 'needs approval', tone: 'warn' });
  assert.ok(find(cards[0], { t: 'text', has: 'Running: Run go vet ./...' }) || JSON.stringify(cards[0]).includes('Running: Run go vet ./...'), 'the status line');
  assert.ok(JSON.stringify(cards[0]).includes('Claude Code · in api-dev:/work/api · 7 tool calls · 2 files +31 −4'), 'who, where, counters');
  assert.ok(find(cards[0], { t: 'plan' }), 'its plan');
  const ap = find(cards[1], { t: 'approval' });
  assert.deepEqual(ap.p.options.map((o) => o.id), ['approved', 'approved-for-session', 'abort']);
  assert.match(ap.p.title, /^Codex asks to run a command: psql -f migrations\/0007_users\.sql$/);
  const si = find(cards[2], { t: 'notice' });
  assert.equal(si.p.tone, 'warn');
  assert.match(si.p.text, /^Claude Code needs you to sign in \(in api-dev\)\. Open it \(↗\) and tap Sign in\.$/);
  assert.deepEqual(reads(r, /\/runs\/26\/view/), [], 'a closed card reads nothing');
  assert.deepEqual(sent(r, 'POST', /\/runs\/27\/approve$/), [{ park: 'Xq3kid', option: 'approved' }], 'answered on the child');
  assert.deepEqual(sent(r, 'POST', /\/runs\/25\/approve$/), []);
});

test('native: a card\'s sign-in in a sandbox the list read lacks (just made: the backend\'s cached list) reads it again once — not "gone"', async () => {
  const s = kidsSeed();
  const FRESH = 'apps/coding-sandbox|sb-fresh';
  const c28 = s.runs.find((r) => r.id === 28);
  c28.harness = { ...c28.harness, sandbox: { ref: FRESH, name: 'fresh-box', cwd: '/work' } };
  const cached = { sandboxes: s.sandboxes.slice(), managers: [{ provider: 'apps/coding-sandbox', title: 'Coding sandboxes', ok: true, caps: ['exec', 'files'] }] };
  s.sandboxes.push({ ...s.sandboxes[0], ref: FRESH, id: 'sb-fresh', name: 'fresh-box', boundTo: [28] });
  s.routes = [['GET', '/sandboxes$', cached]]; // ?fresh=1 is the stub's: it has it
  const r = await runSeed([{ wait: 100 }, { snapshot: 'p' }], 'c=25', s);
  const si = find(find(r.snapshots.p.root, CARD('Write the changelog')), { t: 'notice' });
  assert.equal(si.p.text, 'Claude Code needs you to sign in (in fresh-box). Open it (↗) and tap Sign in.');
  assert.equal(reads(r, /\/sandboxes\?fresh=1$/).length, 1, 'read again once');
});

test('native: an open card reads the child\'s newest page and draws its last 3 blocks; ↗ opens its chat', async () => {
  const r = await runSeed([
    { event: [CARD('Split the router'), 'toggle', { open: true }] }, { wait: 50 },
    { snapshot: 'open' },
    { event: [CARD('Split the router'), 'open', {}] }, { wait: 50 },
    { snapshot: 'child' },
  ], 'c=25');
  assert.deepEqual(reads(r, /\/runs\/26\/view/).slice(0, 1), ['/api/apps/agent/runs/26/view?limit=8']);
  const card = find(r.snapshots.open.root, CARD('Split the router'));
  const inner = all(card, { t: 'transcript' })[0];
  const drawn = (inner.c || []).filter((c) => c.t !== 'plan').map((c) => (c.t === 'toolcard' ? c.p.title : c.t));
  assert.deepEqual(drawn, ['Mount users.go', 'message', 'Run go vet ./...'], 'the last 3 blocks');
  const nav = find(r.snapshots.child.root, { t: 'nav' });
  assert.deepEqual(nav.c.map((s) => s.p.title), ['Refactor the API', 'Split the router'], '↗: the child\'s chat over its parent');
});

test('native: a card whose child can\'t be read says why — read once, not at every paint; opening it again retries', async () => {
  const s = kidsSeed();
  s.routes = [['GET', '/runs/(26|27|28)/view', { error: 'bad gateway' }, 502]];
  const r = await runSeed([
    { wait: 100 }, { snapshot: 'failed' },
    { wait: 2000 }, { snapshot: 'later' },
    { event: [CARD('Plan the users migration'), 'toggle', { open: false }] }, { wait: 50 },
    { event: [CARD('Plan the users migration'), 'toggle', { open: true }] }, { wait: 50 },
    { snapshot: 'retried' },
  ], 'c=25', s);
  const per = (re) => reads(r, re).length;
  // the parked ones (27, 28) open by themselves: one read each, then nothing for as long as the test runs
  assert.equal(per(/\/runs\/28\/view/), 1, 'one read of 28 in 2 s');
  assert.equal(per(/\/runs\/27\/view/), 2, 'one read of 27 in 2 s — and one more when its card is opened again');
  assert.equal(per(/\/runs\/26\/view/), 0, 'a closed card reads nothing');
  for (const k of ['failed', 'later']) {
    const card = find(r.snapshots[k].root, CARD('Plan the users migration'));
    const why = find(card, { t: 'notice', p: { tone: 'danger' } });
    assert.equal(why && why.p.text, "Couldn't read its latest steps: bad gateway — fold the card and open it again to retry.", `${k}: it says why`);
    assert.equal(find(card, { t: 'progress' }), null, `${k}: not "loading…" for ever`);
  }
  assert.ok(find(find(r.snapshots.retried.root, CARD('Plan the users migration')), { t: 'notice', p: { tone: 'danger' } }), 'still failing: said again');
});

test('native: a harness child\'s own chat offers Cancel task (confirmed); the drawer says ⧉ N', async () => {
  const r = await runSeed([
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Cancel task' } } }, { wait: 50 },
    { tap: { t: 'button', p: { label: 'Conversations' } } }, { wait: 50 },
    { snapshot: 'drawer' },
  ], 'c=26');
  const b = find(r.snapshots.chat.root, { t: 'button', p: { label: 'Cancel task' } });
  assert.equal(b.p.confirm.label, 'Cancel task');
  assert.match(b.p.confirm.title, /^Cancel Claude Code's task \(#26\)\?/);
  assert.equal(r.calls.filter((c) => c.method === 'POST' && /\/runs\/26\/cancel$/.test(c.url)).length, 1);
  const row = find(r.snapshots.drawer.root, { t: 'row', p: { title: 'Refactor the API' } });
  assert.equal(row.p.subtitle, '3 coding agents');
  assert.equal(row.p.badge, 'waiting for you', 'a run below it waits: ?');
  const own = await runSeed([{ snapshot: 'chat' }], 'c=21', harnessSeed());
  assert.equal(find(own.snapshots.chat.root, { t: 'button', p: { label: 'Cancel task' } }), null, 'not a child: none');
});
