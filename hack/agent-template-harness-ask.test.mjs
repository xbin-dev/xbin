// hack/agent-template-harness-ask.test.mjs — a coding harness asking and
// driven (plans/agtt-harness.md §8 U4): the words (model/harness-ask.js —
// a permission's options and order, a plan approval, a question's form and
// its native schema, the diff preview, the mode/options control, Auto /
// Always approve, slash matching, the composer's steering words and the
// steered tracker), and the native view over the fixtures
// (native/harness-ask.js: approval, question, toolbar, composer, the
// settings screen, and what each sends to the STUB). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const A = await import(new URL('model/harness-ask.js', TPL));
const { harnessSeed } = await import(new URL('test/harness-fixtures.mjs', TPL));

const seed = harnessSeed();
const ps22 = seed.views[22].run.pendingState;
const ps27 = seed.runs.find((r) => r.id === 27).pendingState;
const ps23 = seed.views[23].run.pendingState;

// --- a permission request --------------------------------------------------------------

test('permission: the harness\'s options in its order, the call, the rule', () => {
  const p = A.permission(ps22, { owner: true, name: 'Claude Code' });
  assert.deepEqual(p.options.map((o) => o.id), ['allow', 'allow_always', 'reject']);
  assert.equal(p.lead, 'Claude Code asks to run a command');
  assert.equal(p.title, 'go test ./...');
  assert.equal(p.label, 'Run the tests');
  assert.equal(p.command, '', 'the command is the title: not said twice');
  assert.equal(p.raw, '', '…nor a third time in the raw input');
  const noCmd = structuredClone(ps22);
  delete noCmd.harness.tool.command;
  assert.match(A.permission(noCmd).raw, /"command": "go test/, 'no lifted command: the raw input says what it runs');
  assert.match(p.rule, /^‘Always allow’ lets Claude Code run later execute calls titled ‘go test \.\/\.\.\.’ without asking in this conversation$/);
  assert.equal(p.options.find((o) => o.id === 'allow_always').title, p.rule);
  assert.equal(p.reject, 'reject');
  assert.equal(p.plan, false);
  assert.equal(A.permission({ kind: 'approval', toolCalls: [] }), null, 'a built-in park is not one');
  assert.equal(A.permission({ kind: 'question', harness: {} }), null);
});

test('permission: reject first when it defaults to no; explicit options the owner\'s only', () => {
  const ps = structuredClone(ps22);
  ps.harness.defaultToNo = true;
  ps.harness.options.push({ optionId: 'yolo', name: 'Allow everything', kind: 'allow_always', explicit: true });
  const own = A.permission(ps, { owner: true });
  assert.deepEqual(own.options.map((o) => o.id), ['reject', 'allow', 'allow_always', 'yolo']);
  assert.equal(own.options[3].explicit, true);
  assert.match(own.options[3].title, /only the owner/);
  assert.equal(own.hidden, 0);
  const other = A.permission(ps, { owner: false });
  assert.deepEqual(other.options.map((o) => o.id), ['reject', 'allow', 'allow_always'], 'a non-owner never sees it');
  assert.equal(other.hidden, 1);
  ps.harness.rule = undefined;
  assert.equal(A.permission(ps).rule, '', 'switch_mode-like: no rule');
});

test('plan approval: the plan, every option, the rejection feedback goes with', () => {
  const p = A.permission(ps27, { owner: true, name: 'Codex' });
  assert.equal(p.plan, true);
  assert.match(p.planText, /Backfill in batches of 1000/);
  assert.equal(p.lead, 'Codex has a plan');
  assert.deepEqual(p.options.map((o) => o.id), ['acceptEdits', 'default', 'bypassPermissions', 'plan']);
  assert.equal(p.reject, 'plan');
  assert.equal(p.rule, '', 'a plan approval never becomes a rule');
});

test('the diff preview: from the park\'s content, else the tool row\'s patches', () => {
  const d = A.lineDiff('/a.go', 'a\nb\nc\nd\ne\nf', 'a\nb\nc\nX\nY\ne\nf');
  assert.deepEqual(d.lines.map((l) => l.t + l.text), ['@@@ -2 +2 @@', ' b', ' c', '-d', '+X', '+Y', ' e', ' f']);
  assert.equal(d.add, 2);
  assert.equal(d.del, 1);
  const created = A.lineDiff('/n.go', null, 'x\ny');
  assert.deepEqual(created.lines.map((l) => l.t), ['@', '+', '+']);
  assert.equal(A.patchOf(d).split('\n')[0], '--- a/a.go');
  const pv = A.contentPreview([{ type: 'diff', path: '/a.go', oldText: 'a', newText: 'b' }, { type: 'content', content: { type: 'text', text: 'hi' } }, { type: 'terminal' }]);
  assert.equal(pv.length, 2);
  assert.equal(pv[1].text, 'hi');
  assert.equal(A.contentPreview([]), null);
  const fromRow = A.diffsPreview([{ path: '/c.go', add: 1, del: 1, patch: '--- a/c.go\n+++ b/c.go\n@@ -1 +1 @@\n-x\n+y\n' }]);
  assert.deepEqual(fromRow[0].lines.map((l) => l.t + l.text), ['@@@ -1 +1 @@', '-x', '+y']);
  const p = A.permission({ kind: 'approval', park: 'p', harness: { options: [], tool: { kind: 'edit', title: 'Edit c.go' } } }, { acp: { diffs: [{ path: '/c.go', patch: '@@ -1 +1 @@\n-x\n+y' }] } });
  assert.equal(p.preview[0].path, '/c.go');
  assert.equal(p.lead, 'the coding agent asks to edit a file');
});

// --- a question ------------------------------------------------------------------------

test('question: the form (enumNames, required, "Other"), its content, what is missing', () => {
  const q = A.question(ps23);
  assert.equal(q.mode, 'form');
  assert.equal(q.message, 'Which JSON library should I use?');
  const [lib, other] = q.fields;
  assert.equal(lib.kind, 'radio');
  assert.deepEqual(lib.options.map((o) => o.title), ['encoding/json (stdlib)', 'jsoniter', 'go-json']);
  assert.equal(lib.required, true);
  assert.equal(other.kind, 'text');
  assert.deepEqual(A.missingRequired(q.fields, A.formContent(q.fields, {})), ['Library']);
  assert.deepEqual(A.formContent(q.fields, { library: 'jsoniter', _askUserQuestionCustomAnswer: ' ' }), { library: 'jsoniter' });
  // claude's custom answer riding on its question, a multiple choice, yes/no, a number
  const fs = A.formFields({ type: 'object', required: ['q'], properties: {
    q: { type: 'string', oneOf: [{ const: 'a', title: 'A' }, { const: 'b', title: 'B', description: 'bee' }] },
    q_other: { type: 'string', description: 'or say', _meta: { _askUserQuestionCustomAnswer: { questionId: 'q' } } },
    many: { type: 'array', items: { anyOf: [{ const: 'x', title: 'X' }, { const: 'y', title: 'Y' }] } },
    sure: { type: 'boolean', title: 'Sure?' }, n: { type: 'integer', default: 3 },
  } });
  assert.deepEqual(fs.map((f) => f.kind), ['radio', 'check', 'bool', 'number']);
  assert.equal(fs[0].other, 'q_other');
  assert.deepEqual(A.missingRequired(fs, A.formContent(fs, { q_other: 'c' })), [], 'answered by its Other box');
  assert.deepEqual(A.formContent(fs, { q: 'b', many: ['y'], sure: false, n: '4' }), { q: 'b', many: ['y'], sure: false, n: 4 });
  assert.deepEqual(A.formContent(fs, { q: 'a' }), { q: 'a', n: 3 }, 'a field left as drawn answers its default');
  assert.deepEqual(A.formContent(fs, { q: 'a', n: '' }), { q: 'a' }, 'a default cleared is no answer');
  const dq = A.formFields({ type: 'object', required: ['pick'], properties: { pick: { type: 'string', enum: ['x', 'y'], default: 'y' } } });
  assert.deepEqual(A.missingRequired(dq, A.formContent(dq, {})), [], 'a required choice drawn chosen by its default is answered');
  // the native question primitive's flat schema, and back
  const ns = A.nativeSchema(fs);
  assert.deepEqual(ns.properties.q.oneOf, [{ const: 'a', title: 'A' }, { const: 'b', title: 'B' }]);
  assert.equal(ns.properties['many.1'].type, 'boolean', 'a multiple choice: a yes/no per choice');
  assert.deepEqual([ns.properties['many.0'].title, ns.properties['many.1'].title], ['many: X', 'many: Y'], '…each named for its question');
  const nx = A.nativeSchema(A.formFields({ type: 'object', properties: {
    extras: { type: 'array', title: 'Extras', description: 'What else?', items: { anyOf: [{ const: 'm', title: 'Metrics' }, { const: 't', title: 'Tracing', description: 'spans' }] } } } }));
  assert.deepEqual([nx.properties['extras.0'], nx.properties['extras.1']], [
    { type: 'boolean', title: 'Extras: Metrics', description: 'What else?' },
    { type: 'boolean', title: 'Extras: Tracing', description: 'spans' }], 'the first choice says what is asked (the question\'s title was lost)');
  assert.equal(ns.properties.q_other.title, 'Other');
  assert.equal(ns.properties.n.default, 3);
  assert.equal(ns.required, undefined, 'a choice with an Other box is not required natively (either answers it)');
  assert.deepEqual(A.nativeContent(fs, { q: 'a', 'many.0': true, 'many.1': false, sure: true }), { q: 'a', many: ['x'], sure: true, n: 3 });
  assert.deepEqual(A.nativeSchema(A.question(ps23).fields).required, ['library']);
});

test('question: url mode — the page (http(s) only), then Done', () => {
  const q = A.question({ kind: 'question', park: 'u', harness: { mode: 'url', url: 'https://x.test/v', message: 'Verify' } });
  assert.deepEqual([q.mode, q.url, q.message], ['url', 'https://x.test/v', 'Verify']);
  const bad = A.question({ kind: 'question', park: 'u', harness: { mode: 'url', url: 'javascript:alert(1)' } });
  assert.equal(bad.url, '', 'never a link to anything but a web page');
  assert.equal(bad.text, 'javascript:alert(1)');
  assert.equal(A.question({ kind: 'approval', harness: {} }), null);
});

// --- controls, the setting, slash, steering -----------------------------------------------

test('controls: the modes (bypass marked, the owner\'s only), the options, the label', () => {
  const h = seed.views[21].run.harness;
  const entry = seed.harnesses.find((x) => x.id === 'claude');
  const c = A.controls(h, entry, { owner: true });
  assert.equal(c.label, 'Accept edits · Default (Opus) · Medium');
  assert.deepEqual(c.modes.map((m) => [m.id, m.current, m.explicit, m.allowed]),
    [['default', false, false, true], ['acceptEdits', true, false, true], ['plan', false, false, true], ['bypassPermissions', false, true, true]]);
  assert.deepEqual(c.options.map((o) => [o.id, o.valueName, o.choices.length]), [['model', 'Default (Opus)', 3], ['effort', 'Medium', 3]]);
  const other = A.controls(h, entry, { owner: false });
  assert.equal(other.modes.find((m) => m.explicit).allowed, false);
  const viewer = A.controls(h, entry, { owner: false, talk: false });
  assert.ok(viewer.modes.every((m) => !m.allowed));
  const bypass = A.controls({ ...h, mode: { ...h.mode, current: 'bypassPermissions' } }, entry, {});
  assert.match(bypass.label, /^Bypass permissions/);
  assert.equal(bypass.mode.explicit, true, 'the views mark it with the warning glyph (D184)');
  const noMode = A.controls({ ...h, options: [...h.options, { id: 'mode', category: 'mode', options: [] }] }, entry, {});
  assert.equal(noMode.options.length, 2, 'a category-mode option is the mode picker\'s');
  assert.match(A.modeConfirm('Claude Code', { name: 'Bypass permissions' }), /stops asking/);
  assert.equal(A.ownerOf({ access: 'owner' }, { kind: 'user' }), true);
  assert.equal(A.ownerOf({ access: 'participant' }, { kind: 'user' }), false);
  assert.equal(A.ownerOf({ access: 'owner' }, { kind: 'user', viewedBy: 'admin' }), false, 'viewing as someone is not them');
  assert.equal(A.ownerOf({ access: 'owner' }, { kind: 'element' }), false);
});

test('Auto / Always approve: the choices; no auto mode, no Auto', () => {
  const claude = A.settingOf(seed.harnesses.find((x) => x.id === 'claude'), 'auto');
  assert.equal(claude.value, 'auto');
  assert.deepEqual(claude.choices.map((c) => [c.value, c.label, !!c.disabled]), [['approve', 'Always approve', false], ['auto', 'Auto', false]]);
  assert.match(claude.choices[1].title, /Accept edits/);
  const oc = A.settingOf(seed.harnesses.find((x) => x.id === 'opencode'), 'auto');
  assert.equal(oc.value, 'approve', 'an auto setting without an auto mode reads as approve');
  assert.equal(oc.choices[1].disabled, true);
  assert.match(oc.choices[1].title, /has no auto mode/);
});

test('slash: the draft\'s command being typed, names that start with it first', () => {
  const cmds = A.slashCommands({ commands: [{ name: 'review', description: 'r', hint: 'what' }, { name: '/compact' }, { name: 'preview' }, {}] });
  assert.deepEqual(cmds.map((c) => c.name), ['review', 'compact', 'preview']);
  assert.deepEqual(A.slashMatches(cmds, '/').map((c) => c.name), ['review', 'compact', 'preview']);
  assert.deepEqual(A.slashMatches(cmds, '/rev').map((c) => c.name), ['review', 'preview']);
  assert.deepEqual(A.slashMatches(cmds, '/co').map((c) => c.name), ['compact']);
  assert.deepEqual(A.slashMatches(cmds, '/view').map((c) => c.name), ['review', 'preview'], 'containing it, after');
  assert.equal(A.slashMatches(cmds, '/review '), null, 'a space: the command is picked');
  assert.equal(A.slashMatches(cmds, 'hi /r'), null);
  assert.equal(A.slashText(cmds[0]), '/review ');
});

test('steering: the composer\'s words by state; the chip\'s label', () => {
  const v = (status, extra = {}, h = {}) => ({ access: 'owner', run: { id: 1, engine: 'harness', status, harness: { name: 'Codex', steering: false, ...h }, ...extra } });
  assert.equal(A.steerWords({ run: { status: 'running' } }), null, 'the built-in agent\'s words stand');
  assert.equal(A.steerWords({ ...v('running'), access: 'viewer' }), null);
  const q = A.steerWords(v('running'));
  assert.equal(q.placeholder, 'queued — sent to Codex when this turn ends (⌘/Ctrl+Enter interrupts)');
  assert.equal(q.label, 'queued for Codex');
  assert.equal(q.busy, true);
  const s = A.steerWords(v('running', {}, { steering: true }), { native: true });
  assert.equal(s.placeholder, 'steer Codex — sent into its running turn (Send now interrupts)');
  assert.equal(s.label, 'steering');
  assert.match(A.steerWords(v('waiting_input', { pendingState: { kind: 'approval' } })).placeholder, /rejects the request/);
  assert.match(A.steerWords(v('waiting_input', { pendingState: { kind: 'question' } })).placeholder, /skips the question/);
  assert.equal(A.steerWords(v('waiting_input', { pendingState: { kind: 'login' } })).placeholder, 'sign in to Codex first — then message it…', 'a sign-in: not "answer the question"');
  assert.equal(A.steerWords(v('idle')).placeholder, 'message Codex…');
});

test('steered: a queued message that left the queue and showed up while it steers', () => {
  const t = A.steerTrack(1000);
  const run = (queued, steering = true) => ({ run: { id: 3, engine: 'harness', status: 'running', harness: { steering, state: 'working' } }, queued });
  const u = (text) => ({ k: 'user', text });
  assert.deepEqual(t(run([{ id: 1, text: 'use tabs' }]), [u('use tabs')], 0), [], 'the same text earlier doesn\'t count');
  assert.deepEqual(t(run([]), [u('use tabs')], 10), [], 'gone from the queue, not yet in the transcript');
  assert.deepEqual(t(run([]), [u('use tabs'), u('use tabs')], 20), [{ text: 'use tabs', until: 1020 }]);
  assert.deepEqual(t(run([]), [u('use tabs'), u('use tabs')], 2000), [], 'for a moment');
  // taken back: never shows up; an adapter that doesn't steer: queued, then the next prompt
  const t2 = A.steerTrack();
  t2(run([{ id: 2, text: 'x' }]), [], 0);
  assert.deepEqual(t2(run([]), [], 5), []);
  assert.deepEqual(t2(run([]), [], 20000), []);
  const t3 = A.steerTrack();
  t3(run([{ id: 2, text: 'x' }], false), [], 0);
  assert.deepEqual(t3(run([], false), [u('x')], 5), []);
  assert.deepEqual(t3({ run: { id: 9 } }, [], 6), [], 'not a harness run');
  // sent while no turn ran: the real backend lists it queued until it is the
  // next prompt, whose turn then runs — that is no steer
  const t4 = A.steerTrack();
  const idle = (queued) => ({ run: { id: 3, engine: 'harness', status: 'idle', harness: { steering: true } }, queued });
  t4(idle([{ id: 4, text: 'todo' }]), [], 0);
  assert.deepEqual(t4(run([]), [u('todo')], 5), [], 'queued while idle, then its own turn: not steered');
  // a park: the message answers it first — not "into its running turn"
  const t5 = A.steerTrack();
  const parked = (queued) => ({ run: { id: 3, engine: 'harness', status: 'waiting_input', harness: { steering: true } }, queued });
  t5(parked([{ id: 5, text: 'no, use tabs' }]), [], 0);
  assert.deepEqual(t5(run([]), [u('no, use tabs')], 5), []);
  // a run whose summary doesn't say the agent is working (a run row without
  // its harness yet): no steer
  const t8 = A.steerTrack();
  const bare = (queued) => ({ run: { id: 8, engine: 'harness', status: 'running', harness: { steering: true } }, queued });
  t8(bare([{ id: 9, text: 'first' }]), [], 0);
  assert.deepEqual(t8(run([]), [u('first')], 5), []);
  // a new conversation's first message: running while the agent starts
  const t6 = A.steerTrack();
  const starting = (queued, state) => ({ run: { id: 6, engine: 'harness', status: 'running', harness: { steering: true, state } }, queued });
  t6(starting([{ id: 7, text: 'hello' }], 'starting'), [], 0);
  assert.deepEqual(t6(starting([], 'working'), [u('hello')], 5), [], 'the first prompt is no steer');
  // …even once the session is up (the real summary says working before the prompt goes)
  const t9 = A.steerTrack();
  t9(starting([{ id: 7, text: 'hello' }], 'working'), [], 0);
  assert.deepEqual(t9(starting([], 'working'), [u('hello')], 5), [], 'no earlier turn to steer');
  // …while one queued during a working turn is
  const t7 = A.steerTrack();
  t7(starting([{ id: 8, text: 'use tabs' }], 'working'), [u('fix it')], 0);
  assert.deepEqual(t7(starting([], 'working'), [u('fix it'), u('use tabs')], 5), [{ text: 'use tabs', until: 6005 }]);
});

// --- the native view over the fixtures ---------------------------------------------------------

async function runSeed(steps, hash, s = harnessSeed()) {
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: Date.UTC(2026, 8, 30, 12), self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: s },
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
const sent = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url)).map((c) => JSON.parse(c.body || 'null'));

test('native: a permission — the harness\'s options, the note, feedback with a rejection', async () => {
  const r = await runSeed([
    { snapshot: 'p' },
    { event: [{ t: 'approval' }, 'choose', { id: 'reject', feedback: 'only unit tests' }] }, { wait: 50 },
  ], 'c=22');
  const [card] = all(r.snapshots.p.root, { t: 'approval' });
  assert.deepEqual(card.p.options.map((o) => o.id), ['allow', 'allow_always', 'reject']);
  assert.equal(card.p.title, 'Claude Code asks to run a command: go test ./...');
  assert.match(card.p.text, /Run the tests/);
  assert.match(card.p.note, /Always allow/);
  assert.equal(card.p.feedback, true);
  assert.deepEqual(sent(r, 'POST', /\/runs\/22\/approve$/), [{ park: 'Xq3perm', option: 'reject', feedback: 'only unit tests' }]);
});

test('native: defaultToNo puts reject first; an allow sends no feedback; the diff previewed', async () => {
  const s = harnessSeed();
  const h = s.views[22].run.pendingState.harness;
  h.defaultToNo = true;
  h.tool.content = [{ type: 'diff', path: '/w/a.go', oldText: 'a\nb', newText: 'a\nB' }];
  const r = await runSeed([{ snapshot: 'p' }, { event: [{ t: 'approval' }, 'choose', { id: 'allow', feedback: 'ignored' }] }, { wait: 50 }], 'c=22', s);
  const [card] = all(r.snapshots.p.root, { t: 'approval' });
  assert.deepEqual(card.p.options.map((o) => o.id), ['reject', 'allow', 'allow_always']);
  const [d] = all(r.snapshots.p.root, { t: 'diff' });
  assert.deepEqual(d.p.files, [{ path: '/w/a.go', status: 'modified', add: 1, del: 1 }]);
  assert.match(d.p.patch, /-b\n\+B/);
  assert.deepEqual(sent(r, 'POST', /\/runs\/22\/approve$/), [{ park: 'Xq3perm', option: 'allow' }]);
});

test('native: a plan approval — the plan, ⚠ bypass confirmed by a second card, keep planning', async () => {
  const r = await runSeed([
    { snapshot: 'p' },
    { event: [{ t: 'approval' }, 'choose', { id: 'bypassPermissions' }] }, { wait: 20 }, { snapshot: 'confirm' },
    { event: [{ t: 'approval' }, 'choose', { id: 'no' }] }, { wait: 20 }, { snapshot: 'back' },
    { event: [{ t: 'approval' }, 'choose', { id: 'plan', feedback: 'batch in transactions' }] }, { wait: 50 },
  ], 'c=27');
  const t = r.snapshots.p.root;
  assert.ok(all(t, { t: 'markdown', has: 'Backfill in batches' }).length, 'the plan');
  const [card] = all(t, { t: 'approval' });
  assert.equal(card.p.options.find((o) => o.id === 'bypassPermissions').label, 'Yes, and bypass permissions');
  assert.equal(card.p.feedback, true);
  const [conf] = all(r.snapshots.confirm.root, { t: 'approval' });
  assert.match(conf.p.title, /Allow “Yes, and bypass permissions”\?/);
  assert.match(conf.p.text, /stops asking/);
  assert.equal(all(r.snapshots.back.root, { t: 'approval' })[0].p.options.length, 4, 'Back: the card again');
  assert.deepEqual(sent(r, 'POST', /\/runs\/27\/approve$/), [{ park: 'Xq3plan', option: 'plan', feedback: 'batch in transactions' }], 'nothing for the bypass');
});

test('native: a question — the schema, Submit, Skip; url mode', async () => {
  const r = await runSeed([
    { snapshot: 'q' },
    { event: [{ t: 'question' }, 'submit', { content: { library: 'go-json' } }] }, { wait: 50 },
  ], 'c=23');
  const [q] = all(r.snapshots.q.root, { t: 'question' });
  assert.equal(q.p.title, 'Which JSON library should I use?');
  assert.deepEqual(q.p.schema.properties.library.oneOf.map((o) => o.title), ['encoding/json (stdlib)', 'jsoniter', 'go-json']);
  assert.deepEqual(sent(r, 'POST', /\/runs\/23\/harness\/answer$/), [{ park: 'Xq3ask', action: 'accept', content: { library: 'go-json' } }]);
  const skip = await runSeed([{ event: [{ t: 'question' }, 'skip', {}] }, { wait: 50 }], 'c=23');
  assert.deepEqual(sent(skip, 'POST', /\/harness\/answer$/), [{ park: 'Xq3ask', action: 'decline' }]);
  const s = harnessSeed();
  s.views[23].run.pendingState = { kind: 'question', park: 'U1', harness: { eid: 'e', mode: 'url', url: 'https://x.test/v', message: 'Verify' } };
  const u = await runSeed([{ snapshot: 'u' }, { event: [{ t: 'question' }, 'submit', { content: {} }] }, { wait: 50 }], 'c=23', s);
  assert.ok(all(u.snapshots.u.root, { t: 'markdown', has: 'https://x.test/v' }).length, 'the page as a link');
  assert.deepEqual(sent(u, 'POST', /\/harness\/answer$/), [{ park: 'U1', action: 'accept', content: {} }]);
});

test('native: the toolbar — Mode (bypass confirmed, the other options, your setting), the Model picker', async () => {
  const r = await runSeed([
    { snapshot: 'c' },
    { tap: { t: 'button', has: '"Plan"' } }, { wait: 50 },
    { tap: { t: 'button', has: 'Always approve — your setting' } }, { wait: 50 },
    { event: [{ t: 'picker', p: { label: 'Model' } }, 'change', { value: 'haiku' }] }, { wait: 50 },
    { tap: { t: 'button', has: 'Reasoning effort: High' } }, { wait: 50 },
  ], 'c=21');
  const t = r.snapshots.c.root;
  const [menu] = all(t, { t: 'menu', has: 'Mode: Accept edits' });
  assert.ok(menu, 'the Mode menu');
  const bypass = all(menu, { t: 'button', has: 'Bypass permissions' })[0];
  assert.ok(bypass.p.confirm && bypass.p.confirm.destructive, 'marked and confirmed');
  assert.ok(all(menu, { t: 'button', has: 'check' }).length >= 2, 'the current mode and setting are checked');
  assert.deepEqual(all(t, { t: 'picker' }).filter((p) => ['Model', 'Reasoning effort'].includes(p.p.label)).map((p) => p.p.value), ['default'],
    'the model a picker in the bar; the other options in the menu — a phone\'s bar holds only so much');
  assert.deepEqual(all(menu, { t: 'button', has: 'Reasoning effort:' }).map((b) => [b.p.label, b.p.icon || '']),
    [['Reasoning effort: Low', ''], ['Reasoning effort: Medium', 'check'], ['Reasoning effort: High', '']]);
  const [bar] = all(t, { t: 'toolbar', has: '' }).filter((b) => all(b, { t: 'menu', has: 'Mode:' }).length);
  assert.ok(bar.c.length <= 6, `the conversation's bar: ${bar.c.map((c) => c.t + ':' + (c.p.label || c.p.icon)).join(', ')}`);
  assert.equal(all(t, { t: 'picker', has: '"label":"Model"' }).filter((p) => JSON.stringify(p.p.options).includes('opus')).length, 0,
    'the built-in model picker hides (a coding agent\'s model is an option)');
  assert.deepEqual(sent(r, 'PATCH', /\/runs\/21\/harness$/), [{ mode: 'plan' }, { option: { id: 'model', value: 'haiku' } }, { option: { id: 'effort', value: 'high' } }]);
  assert.deepEqual(sent(r, 'PUT', /\/prefs\/harness-mode\/claude$/), [{ mode: 'approve' }]);
});

test('native: the composer — slash commands, the steering words, Send now interrupts', async () => {
  const s = harnessSeed();
  s.views[21].run.status = 'running';
  s.views[21].run.harness.state = 'working';
  const r = await runSeed([
    { snapshot: 'c' },
    { event: [{ t: 'composer' }, 'input', { value: 'use tabs' }] },
    { tap: { t: 'button', has: 'Send now' } }, { wait: 50 },
  ], 'c=21', s);
  const [c] = all(r.snapshots.c.root, { t: 'composer' });
  assert.deepEqual(c.p.slash.map((x) => x.name), ['review', 'compact']);
  assert.equal(c.p.placeholder, 'steer Claude Code — sent into its running turn (Send now interrupts)');
  const [msg] = sent(r, 'POST', /\/runs\/21\/message$/);
  assert.equal(msg.text, 'use tabs');
  assert.equal(msg.interrupt, true);
});

test('native: at home — Coding agent settings → your Auto / Always approve per harness', async () => {
  const r = await runSeed([
    { tap: { t: 'button', has: 'Coding agent settings' } }, { wait: 20 }, { snapshot: 's' },
    { event: [{ t: 'picker', p: { label: 'Codex' } }, 'change', { value: 'auto' }] }, { wait: 50 },
  ]);
  const [t] = all(r.snapshots.s.root, { t: 'screen', has: '"title":"Coding agent settings"' });
  assert.ok(t, 'the screen');
  const pickers = all(t, { t: 'picker' });
  assert.deepEqual(pickers.map((p) => [p.p.label, p.p.value]), [['Claude Code', 'auto'], ['Codex', 'approve']]);
  assert.deepEqual(sent(r, 'PUT', /\/prefs\/harness-mode\/codex$/), [{ mode: 'auto' }]);
});
