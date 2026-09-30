// hack/agent-template-harness.test.mjs — the agent template's foundation for
// coding harnesses (plans/agtt-harness.md §8 U1): the seams (model/ext.js),
// the harness model (model/harness.js, harness-heads.js, harness-store.js),
// the fold's `acp` blocks, nesting and notice, the session's `harness`
// event, the fake backend's §4 routes (test/backend.mjs STUB, seeded by
// test/harness-fixtures.mjs), and the native view over those fixtures with
// every native seam hooked. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const { makeExt } = await import(new URL('model/ext.js', TPL));
const H = await import(new URL('model/harness.js', TPL));
const R = await import(new URL('model/rules.js', TPL));
const T = await import(new URL('model/terminals.js', TPL));
const HH = await import(new URL('model/harness-heads.js', TPL));
const { headline, family, subline, outcome, ICON } = await import(new URL('model/tool-heads.js', TPL));
const { fold, activity, FoldCache } = await import(new URL('model/fold.js', TPL));
const { createHarnessStore, AGENT } = await import(new URL('model/harness-store.js', TPL));
const { Session } = await import(new URL('model/session.js', TPL));
const { STUB } = await import(new URL('test/backend.mjs', TPL));
const { harnessSeed, API_DEV } = await import(new URL('test/harness-fixtures.mjs', TPL));

const acall = (id, kind, args) => ({ id, type: 'function', function: { name: 'acp:' + kind, arguments: JSON.stringify(args) } });

// --- the seams -------------------------------------------------------------------

test('seams: first answers, all lists, each calls; unknown seams and throwing hooks', () => {
  const ext = makeExt({ block: 'first', end: 'all', paint: 'each' });
  assert.equal(ext.block({}), null, 'no hook: null');
  assert.equal(ext.end({}), null);
  const seen = [];
  const undo = ext.register({ block: (b) => (b.k === 'a' ? 'A' : null), end: () => 'one', paint: (v) => seen.push(v) });
  ext.register({ block: () => 'B', end: () => null });
  ext.register({ end: () => { throw new Error('boom'); } });
  const err = console.error;
  console.error = () => {};
  try {
    assert.equal(ext.block({ k: 'a' }), 'A', 'the first that answers');
    assert.equal(ext.block({ k: 'x' }), 'B');
    assert.deepEqual(ext.end({}), ['one'], 'every answer, a throw is no answer');
  } finally { console.error = err; }
  assert.equal(ext.paint(7), null);
  assert.deepEqual(seen, [7]);
  assert.ok(ext.has('block'));
  undo();
  assert.equal(ext.block({ k: 'a' }), 'B', 'undone');
  assert.throws(() => ext.register({ nope: () => 1 }), /no seam "nope"/);
  assert.throws(() => makeExt({ x: 'some' }), /first, all or each/);
});

// --- harness-heads: acp:* calls -----------------------------------------------------------

test('acp calls: families, icons, readings, sublines', () => {
  assert.equal(family('acp:execute'), 'box');
  assert.equal(family('acp:read'), 'file');
  assert.equal(family('acp:fetch'), 'web');
  assert.equal(family('acp:edit'), 'edit');
  assert.equal(family('acp:bogus'), 'other', 'unknown kind → other');
  assert.equal(ICON.edit, '✎');
  assert.equal(ICON.think, '💭');
  assert.equal(headline('acp:execute', '{"command":"go test ./...","summary":"Run the tests"}'), 'Run the tests', 'the adapter\'s label');
  assert.equal(headline('acp:execute', '{"command":["go","vet"]}'), '$ go vet');
  assert.equal(headline('acp:read', '{"file_path":"a.go","offset":10,"limit":5}'), 'Read a.go:10–14');
  assert.equal(headline('acp:read', '{"path":"a.go"}'), 'Read a.go');
  assert.equal(headline('acp:search', '{"pattern":"x+","path":"/w"}'), 'Search /x+/ in /w');
  assert.equal(headline('acp:move', '{"source":"a","destination":"b"}'), 'Move a → b');
  assert.equal(headline('acp:switch_mode', '{}'), 'Switch mode');
  assert.equal(subline('acp:execute', '{"command":"go test ./...","summary":"Run the tests"}'), '$ go test ./...');
  assert.equal(subline('acp:execute', '{"command":"ls","summary":"ls"}'), '', 'the headline says it');
  assert.equal(subline('acp:edit', '{"file_path":"a","summary":"Edit a"}'), '');
});

test('acp calls: outcomes and states from the tool row\'s acp', () => {
  const done = (x) => ({ status: 'completed', ...x });
  assert.deepEqual(outcome('acp:execute', 'x', done({ exitCode: 0 })), { text: 'exit 0', tone: 'ok' });
  assert.deepEqual(outcome('acp:execute', 'x', done({ exitCode: 2 })), { text: 'exit 2', tone: 'bad' });
  assert.equal(outcome('acp:execute', 'x', { status: 'in_progress', exitCode: 0 }), null, 'only when completed');
  assert.deepEqual(outcome('acp:edit', 'x', done({ diffs: [{ add: 3, del: 1 }] })), { text: '+3 −1', tone: '' });
  assert.deepEqual(outcome('acp:edit', 'x', done({ diffs: [{ add: 3, del: 1 }, { add: 1, del: 0 }] })), { text: '2 files +4 −1', tone: '' });
  assert.deepEqual(outcome('acp:read', 'a\nb\nc\n', done({})), { text: '3 lines', tone: '' });
  assert.deepEqual(outcome('acp:search', '', done({ locations: [{ path: 'a' }] })), { text: '1 match', tone: '' });
  assert.equal(outcome('acp:fetch', 'x', done({})), null);
  assert.equal(outcome('bash', 'x', done({ exitCode: 0 })), null, 'a built-in call reads its own result');
  assert.equal(HH.acpState({ status: 'failed' }, 'done'), 'error');
  assert.equal(HH.acpState({ status: 'cancelled' }, 'done'), 'stopped');
  assert.equal(HH.acpState({ status: 'pending' }, 'approval'), 'approval', 'the placeholder says parked');
  assert.equal(HH.stripAnsi('\x1b[31mred\x1b[0m'), 'red');
  assert.deepEqual(HH.outputTail({ output: '\x1b[1m0123456789', outputTruncated: 5 }, 4), { text: '6789', cut: 6, dropped: 5 });
});

// --- harness.js: the summary, the park, the catalog ---------------------------------------------

test('a harness run\'s summary in words', () => {
  const seed = harnessSeed();
  const r21 = seed.views[21].run, r22 = seed.views[22].run, r24 = seed.views[24].run;
  assert.equal(H.isHarness(r21), true);
  assert.equal(H.harnessOf({ run: r21 }).provider, 'claude');
  assert.equal(H.harnessOf(seed.views[25].links[0]).provider, 'claude', 'a link\'s child');
  assert.equal(H.harnessOf(seed.views[25].run), null, 'the built-in agent');
  assert.equal(H.nameOf(r21.harness), 'Claude Code');
  assert.equal(H.nameOf({ provider: 'gemini' }), 'Gemini CLI');
  assert.equal(H.monogram('codex'), 'CX');
  assert.deepEqual(H.stateWords(r24.harness), { state: 'login', word: 'needs sign-in', tone: 'warn', live: false, title: '' });
  assert.equal(H.stateWords({ state: 'lost', error: 'xbind restarted' }).title, 'xbind restarted');
  assert.equal(H.stateWords({ state: 'weird' }).state, 'stopped');
  const p22 = H.pendingOf(r22);
  assert.equal(p22.kind, 'approval');
  assert.equal(p22.park, 'Xq3perm');
  assert.equal(p22.harness.options.length, 3);
  assert.equal(H.pendingWords(p22), 'waiting for your approval: go test ./...');
  // a row or a node carries only the compact pending
  const row = { engine: 'harness', status: 'waiting_input', harness: r24.harness };
  assert.deepEqual(H.pendingOf(row), { kind: 'login', park: 'Xq3login', title: 'Sign in to Codex', harness: null });
  assert.equal(H.pendingWords(H.pendingOf(row), 'Codex'), 'needs you to sign in to Codex');
  assert.equal(H.pendingWords(H.pendingOf(seed.views[27].run)), 'waiting for you to approve its plan');
  assert.equal(H.pendingOf(r21), null);
  assert.equal(H.countsWords(r21.harness.counts), '13 tool calls · 4 files +5 −15');
  assert.equal(H.countsWords({ tools: 1 }), '1 tool call');
  assert.equal(H.countsWords({}), '');
  assert.deepEqual(H.usageWords(r21.harness.usage), { text: 'ctx 26%', pct: 26, title: '52 000 of 200 000 tokens of context · $0.41' });
  assert.equal(H.usageWords(null), null);
  const plan = H.planOf(seed.views[26].run.harness);
  assert.equal(plan.text, '1/3 · now: Split the router');
  assert.equal(H.planOf({}), null);
  const mode = H.modeOf(r21.harness);
  assert.equal(mode.name, 'Accept edits');
  assert.equal(mode.available.find((m) => m.id === 'bypassPermissions').explicit, true);
  assert.equal(H.modeOf({ mode: { current: 'x', available: [{ id: 'x', name: 'X' }] } }, { modes: [{ id: 'x', explicit: true }] }).explicit, true, 'explicit from the catalog');
  assert.equal(H.optionOf(r21.harness, 'model').currentValue, 'default');
  assert.equal(H.optionOf(r21.harness, 'thought_level').id, 'effort');
});

test('the activity line of a harness run', () => {
  const mk = (state, extra = {}) => ({ engine: 'harness', status: 'running', harness: { provider: 'claude', name: 'Claude Code', state, ...extra } });
  assert.equal(H.activityLine(mk('starting')), 'Starting Claude Code…');
  assert.equal(H.activityLine(mk('lost', { error: 'the sandbox stopped' })), 'Claude Code stopped (the sandbox stopped) — Retry resumes its session');
  assert.equal(H.activityLine(mk('failed', { error: 'claude-agent-acp not found' })), "Claude Code couldn't start: claude-agent-acp not found");
  assert.equal(H.activityLine(mk('working', { activity: { kind: 'tool', title: 'go test' } })), 'Running: go test');
  assert.equal(H.activityLine(mk('working', { activity: { kind: 'thinking' } })), 'Thinking…');
  assert.equal(H.activityLine(mk('working')), 'Waiting for Claude Code…');
  assert.equal(H.activityLine(mk('ready')), null, 'the built-in words');
  assert.equal(H.activityLine({ status: 'running' }), null, 'not a harness run');
  const seed = harnessSeed();
  assert.equal(activity({ run: seed.views[22].run }, []), 'Waiting for your approval');
  assert.equal(activity({ run: seed.views[24].run }, []), 'Codex needs you to sign in');
  assert.equal(activity({ run: { status: 'running' } }, []), 'Working…', 'the built-in agent as before');
});

test('the catalog: availability, classes, modes, sandboxes', () => {
  const cat = H.catalogOf({ harnesses: harnessSeed().harnesses });
  assert.equal(cat.harnesses.length, 4);
  const claude = H.findHarness(cat, 'claude'), gemini = H.findHarness(cat, 'gemini'), opencode = H.findHarness(cat, 'opencode');
  assert.equal(H.whyNot(claude), '');
  assert.match(H.whyNot(gemini), /needs internet access/);
  assert.equal(H.whyNot({ available: false, reason: 'no-class' }), 'no class you may use allows coding agents', 'the reason\'s words');
  assert.equal(H.whyNot(null), 'unknown coding agent');
  assert.deepEqual(opencode.sandboxes, {}, 'every list present');
  assert.equal(H.catalogOf(null).harnesses.length, 0);
  assert.equal(H.resolveClass(claude, 'coding'), 'coding');
  assert.equal(H.resolveClass(claude, 'internal'), 'coding', 'the first that allows it');
  assert.equal(H.resolveClass({ classes: [] }, 'internal'), '');
  assert.equal(H.providerMode(claude, 'auto'), 'acceptEdits');
  assert.equal(H.providerMode(claude, 'approve'), 'default');
  assert.equal(H.providerMode(claude, 'plan'), 'plan');
  assert.equal(H.providerMode(opencode, 'auto'), '', 'no auto mode');
  const [apiDev, scratch, goDev] = harnessSeed().sandboxes;
  assert.deepEqual(H.sandboxFits(claude, apiDev), { ok: true, why: '', signedIn: true });
  assert.equal(H.sandboxFits(claude, scratch).why, "Claude Code must reach its provider — scratch's egress is none");
  assert.equal(H.sandboxFits(claude, { ...scratch, egressNext: 'internet' }).ok, true, 'the less restrictive of egress/egressNext');
  assert.equal(H.sandboxFits(claude, goDev).why, "go-dev's image doesn't have Claude Code");
  assert.equal(H.sandboxFits({ ...claude, sandboxes: { [apiDev.ref]: { installed: false } } }, apiDev).why, "api-dev doesn't have Claude Code");
});

// --- the fold ---------------------------------------------------------------------------

test('fold: a harness call carries its acp; built-in blocks stay as they were', () => {
  const seed = harnessSeed();
  const blocks = fold(seed.views[21]);
  const ex = blocks.find((b) => b.callId === 'h1:toolu_10');
  assert.equal(ex.k, 'tool');
  assert.equal(ex.fam, 'box');
  assert.equal(ex.headline, 'Run the flaky test 20 times');
  assert.equal(ex.sub, '$ go test ./... -run TestClientRetry -count=20');
  assert.deepEqual(ex.outcome, { text: 'exit 1', tone: 'bad' });
  assert.equal(ex.acp.exitCode, 1);
  assert.equal(ex.state, 'done');
  const ed = blocks.find((b) => b.callId === 'h1:toolu_06');
  assert.deepEqual(ed.outcome, { text: '+5 −1', tone: '' });
  assert.equal(ed.fam, 'edit');
  // the parked call of 22 is the park's
  const parked = fold(seed.views[22]).find((b) => b.callId === 'h2:toolu_02');
  assert.equal(parked.state, 'approval');
  // a built-in call has neither acp nor parent
  const v = { run: { id: 1 }, messages: [
    { id: 1, seq: 1, role: 'user', content: 'hi', created: 1 },
    { id: 2, seq: 2, role: 'assistant', content: '', created: 2, toolCalls: [{ id: 'c1', type: 'function', function: { name: 'bash', arguments: '{"command":"ls"}' } }] },
    { id: 3, seq: 3, role: 'tool', toolCallId: 'c1', content: 'a\n[exit 0 · 1s]', created: 3 }] };
  const b = fold(v).find((x) => x.k === 'tool');
  assert.deepEqual(Object.keys(b).sort(), ['args', 'callId', 'created', 'fam', 'headline', 'id', 'k', 'name', 'outcome', 'result', 'resultId', 'state', 'sub']);
});

test('fold: a harness subagent\'s blocks nest under its call; an orphan stays flat', () => {
  const seed = harnessSeed();
  const cache = new FoldCache();
  const blocks = fold(seed.views[21], () => null, 0, cache);
  const task = blocks.find((b) => b.callId === 'h1:toolu_03');
  assert.equal(task.id, 'ch1:toolu_03', 'the call\'s own id');
  assert.equal(task.fam, 'think');
  assert.deepEqual(task.kids.map((k) => k.k + ':' + (k.callId || k.text)), ['assistant:Looking for callers.', 'tool:h1:toolu_04', 'tool:h1:toolu_05']);
  assert.equal(blocks.some((b) => b.callId === 'h1:toolu_04'), false, 'not at the top as well');
  assert.equal(blocks.filter((b) => b.k === 'tool').length, 11);
  // the same inputs give the same blocks, the Task included
  const again = fold(seed.views[21], () => null, 0, cache);
  assert.equal(again, blocks, 'the same list');
  // a nested call that changes rebuilds its parent, and only that
  const v = seed.views[21];
  v.messages = v.messages.map((m) => (m.toolCallId === 'h1:toolu_05' ? { ...m, content: 'changed' } : m));
  const third = fold(v, () => null, 0, cache);
  const task3 = third.find((b) => b.callId === 'h1:toolu_03');
  assert.notEqual(task3, task);
  assert.equal(task3.kids[0], task.kids[0], 'an unchanged kid is the same block');
  assert.equal(task3.kids[2].result, 'changed');
  assert.equal(third.find((b) => b.callId === 'h1:toolu_06'), blocks.find((b) => b.callId === 'h1:toolu_06'));
  // the Task paged out: its calls render flat
  const orphan = { ...v, hasOlder: true, messages: v.messages.filter((m) => m.toolCallId !== 'h1:toolu_03' && !(m.toolCalls || []).some((c) => c.id === 'h1:toolu_03')) };
  const flat = fold(orphan);
  assert.ok(flat.some((b) => b.callId === 'h1:toolu_04' && b.parent === 'h1:toolu_03'), 'flat, its parent named');
  // a parent loop loses nothing
  const loop = { run: { id: 1 }, messages: [
    { id: 1, seq: 1, role: 'assistant', content: '', created: 1, toolCalls: [acall('a', 'think', {})] },
    { id: 2, seq: 2, role: 'tool', toolCallId: 'a', content: 'x', created: 2, acp: { kind: 'think', status: 'completed', parent: 'b' } },
    { id: 3, seq: 3, role: 'assistant', content: '', created: 3, toolCalls: [acall('b', 'think', {})] },
    { id: 4, seq: 4, role: 'tool', toolCallId: 'b', content: 'y', created: 4, acp: { kind: 'think', status: 'completed', parent: 'a' } }] };
  assert.deepEqual(fold(loop).map((b) => b.callId).sort(), ['a', 'b']);
});

test('fold: the direct-steering notice; a harness child\'s agent block', () => {
  const seed = harnessSeed();
  const blocks = fold(seed.views[25]);
  const notice = blocks.find((b) => b.k === 'notice');
  assert.match(notice.text, /^\[direct message to #26 \(Claude Code\) from admin\]\nKeep the old routes/);
  const agents = blocks.filter((b) => b.k === 'agent');
  assert.deepEqual(agents.map((b) => [b.childId, b.harness && b.harness.provider, b.state]),
    [[26, 'claude', 'running'], [27, 'codex', 'approval'], [28, 'claude', 'done']]);
  const builtIn = fold({ run: { id: 1 }, links: [{ childId: 2, toolCallId: 's', state: 'running', child: { id: 2, status: 'running' } }], messages: [
    { id: 1, seq: 1, role: 'assistant', content: '', created: 1, toolCalls: [{ id: 's', type: 'function', function: { name: 'subagent_spawn', arguments: '{"task":"t"}' } }] }] });
  assert.equal('harness' in builtIn[0], false, 'a built-in child has no harness key');
});

// --- the session and the store -----------------------------------------------------------------------

test('the harness stream event replaces run.harness wherever the run is held', async () => {
  const seed = harnessSeed();
  const s = new Session('/api/apps/agent', { change() {}, frame: (f) => f() });
  s.views.set(25, structuredClone(seed.views[25]));
  s.views.set(21, structuredClone(seed.views[21]));
  s.runs.set(21, { id: 21, engine: 'harness', harness: { state: 'ready' } });
  s.apply({ type: 'harness', run: 21, root: 21, data: { provider: 'claude', state: 'working' } });
  assert.equal(s.merged(21).run.harness.state, 'working');
  const before = s.views.get(25).links[0];
  s.apply({ type: 'harness', run: 26, root: 25, data: { provider: 'claude', state: 'lost', error: 'cut' } });
  const after = s.views.get(25).links[0];
  assert.notEqual(after, before, 'a new link: the card is folded again');
  assert.equal(after.child.harness.state, 'lost');
  assert.equal(after.child.title, 'Split the router', 'the rest of the child stays');
  assert.equal(s.runs.has(26), false, 'no entry made up for a run the list never had');
});

// installStub runs backend.mjs's STUB in this process (window is globalThis), as native-stub.mjs does.
function installStub(seed) {
  globalThis.window = globalThis;
  STUB(seed);
  const calls = globalThis.__calls;
  const pushed = [];
  const push = globalThis.__push;
  globalThis.__push = (ev) => { pushed.push(ev); push(ev); };
  const req = async (method, path, body) => {
    const r = await globalThis.xbin.fetch(path.startsWith('/api/') ? path : `/api/apps/agent${path}`, body === undefined ? { method } : { method, body: JSON.stringify(body) });
    const text = await r.text();
    let data; try { data = JSON.parse(text); } catch { data = text; }
    return { status: r.status, data };
  };
  return { calls, pushed, req };
}

test('STUB: the catalog and the per-person modes (§4.2.1, §4.2.2)', async () => {
  const { req } = installStub(harnessSeed());
  const cat = await req('GET', '/harnesses');
  assert.deepEqual(cat.data.harnesses.map((h) => [h.id, h.available, h.setting]), [['claude', true, 'auto'], ['codex', true, 'approve'], ['gemini', false, 'approve'], ['opencode', false, 'approve']]);
  const probed = await req('GET', `/harnesses?probe=${encodeURIComponent('apps/coding-sandbox|sb-9c1d')}`);
  assert.deepEqual(Object.keys(probed.data.harnesses[0].sandboxes).sort(), [API_DEV, 'apps/coding-sandbox|sb-9c1d'].sort());
  assert.deepEqual((await req('GET', '/prefs/harness-mode')).data, { modes: { claude: 'auto' } });
  assert.deepEqual(await req('PUT', '/prefs/harness-mode/codex', { mode: 'auto' }), { status: 200, data: { provider: 'codex', mode: 'auto' } });
  assert.deepEqual(await req('PUT', '/prefs/harness-mode/codex', { mode: 'yolo' }), { status: 400, data: { error: 'mode is "auto" or "approve"' } });
  assert.deepEqual(await req('PUT', '/prefs/harness-mode/nope', { mode: 'auto' }), { status: 400, data: { error: 'no coding agent "nope"' } });
  assert.equal((await req('PUT', '/prefs/harness-mode/opencode', { mode: 'auto' })).data.error, 'opencode has no auto mode — it asks as its own settings say');
  assert.equal((await req('GET', '/harnesses')).data.harnesses[1].setting, 'auto', 'the setting follows');
});

test('STUB: a harness ask — refused as the backend refuses, else a harness run (§4.2.3)', async () => {
  const { req, pushed } = installStub(harnessSeed());
  const sb = { ref: API_DEV, cwd: '/work/api' };
  const err = async (body) => { const r = await req('POST', '/ask', body); return [r.status, r.data.error]; };
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'nope' }, sandbox: sb }), [400, 'harness.provider: no coding agent "nope" (GET /harnesses lists them)']);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' }, sandbox: sb, system: 'be nice' }), [400, 'system: a coding agent keeps its own instructions — system is for the built-in agent']);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' }, sandbox: sb, model: 'm' }), [400, "model: a coding agent's model is harness.options.model"]);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude', options: { mode: 'plan' } }, sandbox: sb }), [400, 'harness.options: the mode is harness.mode']);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' }, sandbox: sb, class: 'internal' }), [400, "class: the internal class doesn't allow Claude Code"]);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude', mode: 'fast' }, sandbox: sb }), [400, 'harness.mode: one of default, acceptEdits, plan, bypassPermissions']);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' } }), [400, 'a coding agent needs a sandbox: sandbox {ref, cwd?} whose image has Claude Code']);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' }, sandbox: { ref: 'apps/coding-sandbox|sb-9c1d' } }), [409, "Claude Code must reach its provider — scratch's egress is none"]);
  assert.deepEqual(await err({ text: 'x', harness: { provider: 'claude' }, sandbox: { ref: 'apps/coding-sandbox|sb-2b8e' } }), [409, "go-dev's image doesn't have Claude Code"]);
  const ok = await req('POST', '/ask', { text: 'fix it', harness: { provider: 'claude', options: { model: 'sonnet' } }, sandbox: sb });
  assert.equal(ok.status, 200);
  assert.equal(ok.data.engine, 'harness');
  assert.equal(ok.data.harness.mode.current, 'acceptEdits', 'the caller\'s setting (auto) mapped to the provider');
  assert.equal(ok.data.harness.sandbox.cwd, '/work/api');
  assert.equal(ok.data.class, 'coding', 'the class resolved');
  const v = (await req('GET', `/runs/${ok.data.id}/view`)).data;
  assert.equal(v.messages[0].content, 'fix it');
  assert.deepEqual(v.config.harness.options, { model: 'sonnet' });
  assert.equal(pushed.at(-1).type, 'run');
  const viaRuns = await req('POST', '/runs', { goal: 'plan it', harness: { provider: 'codex' }, sandbox: sb });
  assert.equal(viaRuns.data.harness.mode.current, 'read-only');
  assert.deepEqual(await req('POST', '/ask', { text: 'plain' }), { status: 200, data: {} }, 'a built-in ask answers as before');
});

test('STUB: GET|PATCH /runs/{id}/harness (§4.2.4)', async () => {
  const { req, pushed } = installStub(harnessSeed());
  const g = (await req('GET', '/runs/21/harness')).data;
  assert.equal(g.harness.provider, 'claude');
  assert.deepEqual(Object.keys(g.session).sort(), ['acpSessionId', 'execId', 'gen', 'lastActive', 'loadable', 'startedAt', 'steering']);
  assert.deepEqual(g.rules, [{ kind: 'read', title: 'Read /work/api' }]);
  assert.deepEqual(await req('GET', '/runs/25/harness'), { status: 409, data: { error: 'not a coding-agent conversation' } });
  const p = await req('PATCH', '/runs/21/harness', { mode: 'plan', option: { id: 'model', value: 'sonnet' } });
  assert.equal(p.data.harness.mode.current, 'plan');
  assert.equal(p.data.harness.options[0].currentValue, 'sonnet');
  assert.equal(pushed.at(-1).type, 'harness');
  assert.equal(pushed.at(-1).data.mode.current, 'plan', 'the whole summary');
  assert.equal((await req('PATCH', '/runs/21/harness', { mode: 'nope' })).data.error, 'mode: one of default, acceptEdits, plan, bypassPermissions');
  assert.equal((await req('PATCH', '/runs/21/harness', { option: { id: 'x', value: 'y' } })).data.error, 'option: one of model, effort');
  assert.equal((await req('PATCH', '/runs/21/harness', { option: { id: 'model', value: 'y' } })).data.error, 'value: one of default, sonnet, haiku');
});

test('STUB: approve a harness park by option; answer a question (§4.2.5, §4.2.9)', async () => {
  const { req, pushed } = installStub(harnessSeed());
  assert.equal((await req('POST', '/runs/22/approve', { park: 'Xq3perm', option: 'nope' })).data.error, 'option: one of allow, allow_always, reject');
  assert.equal((await req('POST', '/runs/22/approve', { park: 'Xq3perm', option: 'allow', feedback: 'x' })).data.error, 'feedback goes with a rejection');
  assert.equal((await req('POST', '/runs/22/approve', { park: 'old', option: 'allow' })).status, 409);
  assert.equal((await req('POST', '/runs/25/approve', { option: 'allow' })).data.error, "option is for a coding agent's permission request");
  assert.deepEqual(await req('POST', '/runs/22/approve', { park: 'Xq3perm', option: 'allow_always' }), { status: 200, data: { ok: 'true' } });
  const ev = pushed.at(-1);
  assert.equal(ev.type, 'run');
  assert.equal(ev.data.status, 'running');
  assert.equal(ev.data.harness.pending, undefined, 'the park is gone');
  assert.equal((await req('POST', '/runs/27/approve', { park: 'Xq3plan', option: 'plan', feedback: 'keep it smaller' })).status, 200, 'a plan rejected with feedback');
  assert.equal((await req('POST', '/runs/23/harness/answer', { park: 'Xq3ask', action: 'maybe' })).data.error, 'action is accept, decline or cancel');
  assert.equal((await req('POST', '/runs/23/harness/answer', { park: 'Xq3ask', action: 'accept', content: 'x' })).data.error, "content: an object with the form's fields");
  assert.equal((await req('POST', '/runs/23/harness/answer', { park: 'old', action: 'decline' })).status, 409);
  assert.equal((await req('POST', '/runs/22/harness/answer', { action: 'decline' })).data.error, 'no pending question');
  assert.deepEqual(await req('POST', '/runs/23/harness/answer', { park: 'Xq3ask', action: 'accept', content: { library: 'encoding/json' } }), { status: 200, data: { ok: 'true' } });
});

test('STUB: sign-in and the adapter\'s log (§4.2.6, §4.2.7)', async () => {
  const { req, pushed } = installStub(harnessSeed());
  const auth = (body) => req('POST', '/runs/24/harness/authenticate', body);
  assert.equal((await auth({ method: 'chatgpt' })).data.error, 'method: one of openai-api-key, device-code', 'a terminal method is the terminal\'s');
  assert.equal((await auth({ method: 'openai-api-key' })).data.error, 'apiKey: needed for OpenAI API key');
  assert.equal((await auth({ method: 'device-code', apiKey: 'k' })).data.error, 'apiKey: only for an API-key method');
  const shared = await auth({ method: 'openai-api-key', apiKey: 'sk-1' });
  assert.equal(shared.status, 409);
  assert.equal(shared.data.confirm, true, 'a shared sandbox: confirm first');
  assert.equal((await auth({ method: 'openai-api-key', apiKey: 'bad', confirm: true })).status, 502);
  const dev = await auth({ method: 'device-code', confirm: true });
  assert.equal(dev.status, 202);
  assert.match(dev.data.device.message, /FAKE-1234/);
  assert.equal(pushed.at(-1).data.login.device.url, 'https://example.invalid/device');
  assert.deepEqual(await auth({ method: 'openai-api-key', apiKey: 'sk-1', confirm: true }), { status: 200, data: { ok: 'true', state: 'ready' } });
  assert.equal((await auth({ method: 'openai-api-key', apiKey: 'sk-1', confirm: true })).data.error, 'Codex is signed in');
  assert.equal((await req('GET', '/runs/22/harness/log?max=10')).data, '-22 ready\n', 'its tail');
  assert.deepEqual(await req('GET', '/runs/21/harness/log'), { status: 404, data: { error: 'no log yet' } });
});

test('STUB: rows carry waiting and kids; the tree its harness nodes; needs a login (§4.3.6–§4.3.9)', async () => {
  const { req } = installStub(harnessSeed());
  const rows = (await req('GET', '/conversations?limit=50')).data.items;
  const row = (id) => rows.find((r) => r.id === id);
  assert.deepEqual([row(25).waiting, row(25).kids], [true, { harness: 2, waiting: 1 }]);
  assert.deepEqual([row(21).waiting, row(21).kids], [undefined, undefined], 'nothing below, nothing said');
  assert.equal(row(24).waiting, true);
  assert.equal(row(21).engine, 'harness');
  const tree = (await req('GET', '/runs/25/tree')).data;
  assert.deepEqual(tree.nodes.map((n) => [n.id, n.engine]), [[25, ''], [26, 'harness'], [27, 'harness'], [28, 'harness']]);
  const n27 = tree.nodes[2].harness;
  assert.deepEqual(n27.mode, { current: 'read-only' }, 'no mode.available');
  assert.equal('options' in n27 || 'commands' in n27, false);
  assert.equal(n27.pending.kind, 'approval');
  assert.equal((await req('GET', '/runs/1/tree')).data.nodes.length, 0, 'unseeded: as before');
  const needs = (await req('GET', '/needs')).data.items;
  assert.deepEqual(needs.map((n) => n.reason), ['login', 'approval', 'question', 'approval']);
});

// a fake app for the store: the session's notes, its views and runs
function fakeApp() {
  const notes = [];
  const emitted = [];
  return { notes, emitted, emit: (t) => emitted.push(t),
    session: { views: new Map(), runs: new Map(), clearApproveNote: () => {}, noteApprove: (id, e) => notes.push([id, e.message]) } };
}

test('app.harness: the catalog, the picks and what a new ask carries', async () => {
  const { req, calls } = installStub(harnessSeed());
  const app = fakeApp();
  const hs = createHarnessStore(app);
  assert.deepEqual(hs.askPart(), {}, 'the built-in agent answers');
  await hs.load();
  assert.equal(hs.catalog.harnesses.length, 4);
  assert.deepEqual(hs.modes, { claude: 'auto' });
  assert.equal(hs.pick, AGENT);
  assert.ok(app.emitted.includes('harness'));
  hs.choose('claude');
  assert.equal(globalThis.__prefs.agent, 'claude', 'prefs/agent (xbind prefs)');
  hs.setOption('claude', 'model', 'sonnet');
  assert.deepEqual(hs.askPart(), { harness: { provider: 'claude', options: { model: 'sonnet' } } });
  hs.choose('gemini');
  assert.deepEqual(hs.askPart(), {}, 'not available: the built-in agent');
  hs.rememberSandbox('claude', API_DEV);
  assert.deepEqual(globalThis.__prefs['harness-sandbox'], { claude: API_DEV });
  assert.equal(hs.setting('claude'), 'auto');
  assert.equal(hs.setting('codex'), 'approve');
  await hs.setSetting('codex', 'auto');
  assert.equal(hs.setting('codex'), 'auto');
  await assert.rejects(hs.setSetting('opencode', 'auto'), (e) => e.status === 400 && /no auto mode/.test(e.message));
  // a second store reads the picks back
  const hs2 = createHarnessStore(fakeApp());
  await hs2.load();
  assert.equal(hs2.pick, 'gemini');
  assert.deepEqual(hs2.sandboxes, { claude: API_DEV });
  assert.ok(calls.some((c) => c.method === 'GET' && c.url.endsWith('/api/xbin/prefs/harness-sandbox')));
  void req;
});

test('app.harness: a harness run\'s calls and their bodies', async () => {
  const seed = harnessSeed();
  const { calls } = installStub(seed);
  const app = fakeApp();
  app.session.views.set(22, seed.views[22]);
  const hs = createHarnessStore(app);
  const last = () => { const c = calls.at(-1); return [c.method, c.url.replace('/api/apps/agent', ''), c.body && JSON.parse(c.body)]; };
  assert.equal(await hs.permit(22, { option: 'allow' }), true);
  assert.deepEqual(last(), ['POST', '/runs/22/approve', { park: 'Xq3perm', option: 'allow' }], 'the park from the view');
  assert.equal(await hs.permit(22, { option: 'allow', park: 'gone' }), false, 'refused: said, not thrown');
  assert.equal(app.notes.at(-1)[0], 22);
  await hs.permit(27, { approve: false, feedback: 'smaller', park: 'Xq3plan' });
  assert.deepEqual(last()[2], { park: 'Xq3plan', approve: false, feedback: 'smaller' });
  assert.equal(await hs.answer(23, 'accept', { library: 'go-json' }, 'Xq3ask'), true);
  assert.deepEqual(last(), ['POST', '/runs/23/harness/answer', { park: 'Xq3ask', action: 'accept', content: { library: 'go-json' } }]);
  await hs.answer(23, 'decline', null, 'Xq3ask');
  assert.deepEqual(last()[2], { park: 'Xq3ask', action: 'decline' });
  await assert.rejects(hs.authenticate(24, 'openai-api-key', { apiKey: 'sk' }), (e) => e.status === 409 && e.data.confirm === true);
  assert.deepEqual(await hs.authenticate(24, 'device-code', { confirm: true }), { ok: 'true', device: { url: 'https://example.invalid/device', message: 'Enter code FAKE-1234 at https://example.invalid/device' } });
  assert.match(await hs.log(22, 1e9), /session sess-22 ready/);
  assert.match(calls.at(-1).url, /log\?max=65536$/);
  await hs.steer(26, 'use tabs', { interrupt: true });
  const [, path, body] = last();
  assert.equal(path, '/runs/26/message');
  assert.equal(body.text, 'use tabs');
  assert.equal(body.interrupt, true);
  assert.ok(body.clientId);
  await hs.steer(26, 'later');
  assert.equal('interrupt' in last()[2], false);
  await hs.setMode(21, 'plan');
  assert.deepEqual(last(), ['PATCH', '/runs/21/harness', { mode: 'plan' }]);
  await hs.setOptionOf(21, 'effort', 'high');
  assert.deepEqual(last(), ['PATCH', '/runs/21/harness', { option: { id: 'effort', value: 'high' } }]);
  const got = await hs.get(21);
  assert.equal(got.harness.options[1].currentValue, 'high');
  for (const [fn, what] of [['stop', 'interrupt'], ['cancel', 'cancel'], ['retry', 'resume']]) {
    await hs[fn](26);
    assert.deepEqual(last().slice(0, 2), ['POST', `/runs/26/${what}`]);
  }
});

// --- the native view over the fixtures, every seam hooked ---------------------------------------------

async function runNativeSeed(steps, hash, setup = 'test/native-ext-probe.mjs', seed = harnessSeed()) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: Date.UTC(2026, 8, 30, 12), self: 'apps/agent', setup: new URL(setup, TPL).pathname, seed },
    steps, state: hash ? { hash } : null });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
function all(root, m, out = []) {
  if (!root) return out;
  if ((!m.t || root.t === m.t) && (m.has == null || JSON.stringify(root.p || {}).includes(m.has))) out.push(root);
  for (const c of root.c || []) all(c, m, out);
  return out;
}
const texts = (tree) => JSON.stringify(tree);

test('native: a harness conversation — its cards, a seam\'s block, end, toolbar, subtitle, menu, composer', async () => {
  const r = await runNativeSeed([{ snapshot: 'chat' }], 'c=21');
  const t = r.snapshots.chat.root;
  const cards = all(t, { t: 'toolcard' });
  assert.ok(cards.some((c) => c.p.title === 'Read client_test.go' && c.p.icon === 'file'), 'a read card');
  const task = cards.find((c) => c.p.title === 'Find every caller of Do');
  assert.equal(task.p.icon, 'sparkles', 'think → sparkles');
  assert.ok(cards.some((c) => c.p.title === 'Retry the request' && c.p.family === 'edit' && JSON.stringify(c.p.chips).includes('+5 −1')));
  assert.equal(cards.some((c) => c.p.title === 'Run the flaky test 20 times'), false, 'execute is the seam\'s');
  assert.match(texts(t), /probe block: Run the flaky test 20 times/);
  assert.match(texts(t), /probe end: ready/);
  assert.ok(all(t, { t: 'button', has: 'probe toolbar' }).length, 'the toolbar hook');
  assert.ok(all(t, { t: 'button', has: 'probe menu #21' }).length, 'the menu hook');
  assert.match(all(t, { t: 'screen' })[0].p.subtitle, /^probe subtitle · CC ready 👥 · 📋 3\/3 · ctx 26% · idle · /, 'the subtitle hooks (in the order they registered), after the chain, before the status');
  const composer = all(t, { t: 'composer' })[0];
  assert.equal(composer.p.placeholder, 'message Claude Code…', 'the last placeholder given (U4\'s, registered after the probe)');
  assert.deepEqual(composer.p.slash[0], { name: 'probe', description: 'a probe command' }, 'slash commands add up');
});

test('native: parks — the seam\'s end takes a harness park; the built-in card stays for the rest', async () => {
  const r22 = await runNativeSeed([{ snapshot: 'p' }], 'c=22');
  const cards = all(r22.snapshots.p.root, { t: 'approval' });
  assert.equal(cards.length, 1, 'the end hooks answered: the park is theirs (U4\'s card; the probe\'s notice)');
  assert.doesNotMatch(cards[0].p.text, /acp:execute/, 'not the built-in card');
  assert.match(texts(r22.snapshots.p.root), /probe end: working/);
  const r25 = await runNativeSeed([{ snapshot: 'p' }], 'c=25');
  const t = r25.snapshots.p.root;
  const agents = all(t, { t: 'toolcard' }).filter((c) => c.p.family === 'agent');
  assert.equal(agents.length, 3, 'three children');
  assert.ok(all(t, { t: 'toolcard', has: 'direct message to #26' }).length, 'the notice');
  assert.ok(all(t, { t: 'button', has: 'probe menu #25' }).length);
  assert.equal(JSON.stringify(t).includes('probe end'), false, 'the built-in agent\'s: not the probe\'s');
});

test('native: a harness park of a kind no module answers falls back to the built-in card', async () => {
  const seed = harnessSeed();
  const ps = { kind: 'review', park: 'Xq3review', harness: { message: 'Review the retry policy?' } };
  for (const r of [seed.runs.find((x) => x.id === 22), seed.views[22].run]) Object.assign(r, { pendingState: ps, result: 'Review the retry policy?' });
  const r = await runNativeSeed([{ snapshot: 'p' }], 'c=22', 'test/native-stub.mjs', seed); // the modules only (no probe)
  const t = r.snapshots.p.root;
  assert.equal(all(t, { t: 'approval' }).length, 0, 'no module\'s card');
  const q = all(t, { t: 'question' });
  assert.equal(q.length, 1, 'the built-in question');
  assert.equal(q[0].p.title, 'The agent is asking');
  assert.match(JSON.stringify(q[0].p.schema), /Review the retry policy\?/);
  const plain = await runNativeSeed([{ snapshot: 'p' }], 'c=22', 'test/native-stub.mjs');
  assert.equal(all(plain.snapshots.p.root, { t: 'question' }).length, 0, 'an approval park is harness-ask.js\'s: no built-in question');
  assert.doesNotMatch(all(plain.snapshots.p.root, { t: 'approval' })[0].p.text || '', /acp:execute/, '…nor the built-in approval');
});

test('native: the home toolbar, a pushed screen and the new-chat sheet through the seams', async () => {
  const r = await runNativeSeed([
    { snapshot: 'home' },
    { call: ['probeScreen'] }, { snapshot: 'screen' },
    { call: ['newChat', 'hello'] }, { snapshot: 'sheet' },
    { tap: { t: 'button', p: { label: 'Start' } } }, { wait: 50 },
  ]);
  assert.ok(all(r.snapshots.home.root, { t: 'button', has: 'probe home toolbar' }).length);
  assert.ok(all(r.snapshots.home.root, { t: 'menu', has: '"label":"More"' }).some((m) => JSON.stringify(m).includes('probe main')), 'the main hook: home\'s ⋯');
  assert.ok(all(r.snapshots.screen.root, { t: 'screen', has: 'probe screen' }).length);
  assert.ok(all(r.snapshots.sheet.root, { t: 'section', has: 'probe section' }).length);
  const ask = r.calls.find((c) => c.method === 'POST' && /\/ask$/.test(c.url));
  assert.equal(JSON.parse(ask.body).probe, 'yes', 'the section\'s body joins the ask');
});

// --- the integration (UIint): what a harness conversation leaves out, sign-in words, view-only readers ------

test('the top bar of a coding agent\'s conversation: no Memory, Learn skill or Compact (unless it has /compact); Retry when it was cut off', () => {
  const rules = R;
  const v = (h, extra = {}) => ({ access: 'owner', memory: { goal: 'x' }, run: { id: 1, status: 'idle', engine: 'harness', harness: { provider: 'claude', state: 'ready', ...h }, ...extra } });
  const t = rules.topBar(v({}));
  assert.deepEqual([t.compact, t.learn, t.memory, t.retry], [false, false, null, false]);
  assert.equal(rules.topBar(v({ commands: [{ name: 'compact', description: 'Compact' }] })).compact, true, 'Compact: its own /compact');
  assert.equal(rules.topBar(v({ state: 'lost', error: 'the sandbox stopped' })).retry, true, 'cut off: Retry resumes its session');
  assert.equal(rules.topBar(v({ state: 'failed' })).retry, true);
  assert.equal(rules.topBar({ ...v({ state: 'lost' }), access: 'viewer' }).retry, false);
  const b = rules.topBar({ access: 'owner', memory: { goal: 'x' }, run: { id: 2, status: 'idle', engine: '' } });
  assert.deepEqual([b.compact, b.learn, b.memory], [true, true, 1], 'the built-in agent\'s: as before');
});

test('a login park: the activity line has no spinner, the sign-in card says who may act', () => {
  const r = (state, extra = {}) => ({ id: 1, status: 'waiting_input', engine: 'harness', harness: { provider: 'codex', name: 'Codex', state }, ...extra });
  const login = { kind: 'login', park: 'p', harness: { login: { command: 'codex login', methods: [{ id: 'k', name: 'API key', kind: 'api-key' }] } } };
  assert.equal(H.activityStill(r('login', { pendingState: login })), true);
  assert.equal(H.activityStill(r('lost', { status: 'idle' })), true, 'cut off: nothing runs');
  assert.equal(H.activityStill(r('working', { status: 'running' })), false);
  assert.equal(H.activityStill(r('working', { pendingState: { kind: 'approval', park: 'a', harness: {} } })), false, 'waiting for a verdict: as the built-in agent\'s');
  assert.equal(H.activityStill({ id: 2, status: 'waiting_input' }), false, 'the built-in agent\'s');
  const v = (access) => ({ access, run: r('login', { pendingState: login }), config: { harness: { ref: 'apps/coding-sandbox|sb-1', cwd: '/w' } } });
  const own = T.signIn(v('owner'));
  assert.deepEqual([own.talk, own.view, own.title], [true, '', 'Codex needs you to sign in (in ▣ sb-1).']);
  const ro = T.signIn(v('viewer'));
  assert.deepEqual([ro.talk, ro.title], [false, 'Codex is waiting for a sign-in (in ▣ sb-1).']);
  assert.match(ro.view, /only read this conversation/);
});

test('native: a login park says sign in (composer, activity); a view-only reader gets the notice and no actions', async () => {
  const r = await runNativeSeed([{ snapshot: 'p' }], 'c=24', 'test/native-stub.mjs');
  const t = r.snapshots.p.root;
  assert.equal(all(t, { t: 'composer' })[0].p.placeholder, 'sign in to Codex first — then message it…');
  const act = all(t, { t: 'activity' })[0];
  assert.equal(act.p.text, 'Codex needs you to sign in');
  assert.ok(!act.p.live, 'no spinner: it waits on you');
  assert.ok(all(all(t, { t: 'composer' })[0], { t: 'button', has: '"label":"Sign in"' }).length, 'the composer\'s Sign in');
  const more = all(t, { t: 'menu', has: '"label":"More"' })[0];
  assert.ok(all(more, { t: 'button', has: 'Sign in…' }).length);
  for (const x of ['Memory', 'Compact', 'Learn skill']) assert.equal(all(more, { t: 'button', has: x }).length, 0, `no ${x} for a coding agent`);
  const seed = harnessSeed();
  seed.views[24].access = 'viewer';
  const ro = (await runNativeSeed([{ snapshot: 'p' }], 'c=24', 'test/native-stub.mjs', seed)).snapshots.p.root;
  assert.match(all(ro, { t: 'notice', has: 'Sign in to Codex' })[0].p.text, /is waiting for a sign-in .* You may only read this conversation/);
  assert.equal(all(all(ro, { t: 'composer' })[0], { t: 'button' }).length, 0, 'no Sign in');
  const roMore = all(ro, { t: 'menu', has: '"label":"More"' })[0];
  assert.equal(all(roMore, { t: 'button', has: 'Sign in…' }).length + all(roMore, { t: 'button', has: '"label":"Terminal"' }).length, 0, 'no Sign in…, no Terminal (the run\'s relay is a participant\'s)');
});
