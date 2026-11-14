// hack/chat-ask.test.mjs — the chat kit's model of what an agent asks
// (web/chat/ask.js, /vendor/chat/ask.js, D224), run by `make js-test`: a
// permission request as its card (option order, who sees an explicit
// option, what "always" remembers), a question as a form or a page to
// open, the slash menu's one rule, the composer's words, a pasted code —
// and that the modules it was made from are it: web/agent-tools.js and
// web/agent-slash.js (the Agent tab; hack/agent-tools.test.mjs and
// hack/agent-slash.test.mjs hold their outputs), web/signin-scan.js, and
// the agent template's model/harness-ask.js and model/harness-signins.js
// (hack/agent-template-harness-ask.test.mjs holds theirs).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as A from '../web/chat/ask.js';
import * as oldTools from '../web/agent-tools.js';
import * as oldSlash from '../web/agent-slash.js';
import { cleanCode as scanClean } from '../web/signin-scan.js';
import { Fold } from '../web/agent-fold.js';
import { installHooks } from './xbn/hooks.mjs';

installHooks(); // /vendor/<name> as xbind serves it: the template's models import the kit by URL
const HA = await import('../builtin-templates/agent/model/harness-ask.js');
const HS = await import('../builtin-templates/agent/model/harness-signins.js');

const opt = (optionId, kind, more = {}) => ({ optionId, name: optionId, kind, ...more });
const perm = (fields = {}) => ({
  kind: 'perm', k: 'p1', v: 0, pid: 'p1', callId: 't1',
  tool: { title: 'go test ./...', kind: 'execute', name: 'Bash', label: 'Run the tests', command: 'go test ./...', rawInput: { command: 'go test ./...' }, content: null },
  options: [opt('allow', 'allow_once'), opt('always', 'allow_always', { name: 'Always allow' }), opt('reject', 'reject_once')],
  rule: { kind: 'execute', title: 'go test ./...' }, description: '', defaultToNo: false, plan: null, by: null, optionId: null, ...fields,
});

test('permission: the agent\'s options in its order, the call, what "always" remembers', () => {
  const p = A.permission(perm(), { name: 'Claude Code' });
  assert.deepEqual(p.options.map((o) => [o.id, o.allow, o.reject, o.explicit]), [['allow', true, false, false], ['always', true, false, false], ['reject', false, true, false]]);
  assert.equal(p.lead, 'Claude Code asks to run a command');
  assert.deepEqual([p.title, p.label, p.command, p.raw], ['go test ./...', 'Run the tests', '', ''], 'the command is the title: said once');
  assert.equal(p.rule, '‘Always allow’ lets Claude Code run later execute calls titled ‘go test ./...’ without asking in this conversation');
  assert.equal(p.options[1].title, p.rule, 'the option that remembers says what it remembers');
  assert.equal(p.reject, 'reject');
  assert.deepEqual([p.plan, p.planText, p.hidden, p.defaultToNo], [false, '', 0, false]);
  assert.equal(A.permission(perm(), { where: 'in this session' }).rule, '‘Always allow’ lets the agent run later execute calls titled ‘go test ./...’ without asking in this session');
  assert.equal(A.permission(null), null);
  // no lifted command: the raw input says what it runs
  assert.match(A.permission(perm({ tool: { title: 'Bash', kind: 'execute', rawInput: { command: 'go test' } } })).raw, /"command": "go test"/);
  assert.equal(A.permission(perm({ tool: { kind: 'fetch' } })).lead, 'the agent asks to fetch a page');
  assert.equal(A.permission(perm({ tool: {}, calls: ['bash', 'write_file'] })).title, 'bash', 'a host\'s own request: its first call names it');
});

test('permission: rejections first when it defaults to no; an explicit option only for who may raise', () => {
  const p = A.permission(perm({ defaultToNo: true, options: [opt('allow', 'allow_once'), opt('no', 'reject_once'), opt('never', 'reject_always')] }));
  assert.deepEqual(p.options.map((o) => o.id), ['no', 'never', 'allow']);
  const raise = perm({ options: [opt('allow', 'allow_once'), opt('bypass', 'allow_always', { explicit: true, name: 'Yes, and bypass permissions' }), opt('reject', 'reject_once')] });
  const guest = A.permission(raise, { name: 'Codex', can: { answer: true, raise: false } });
  assert.deepEqual([guest.options.map((o) => o.id), guest.hidden], [['allow', 'reject'], 1]);
  const owner = A.permission(raise, { name: 'Codex', can: { answer: true, raise: true } });
  assert.deepEqual([owner.options.map((o) => o.id), owner.hidden], [['allow', 'bypass', 'reject'], 0]);
  assert.deepEqual([owner.options[1].explicit, owner.options[1].title], [true, 'raises Codex to a mode where it stops asking — only the owner may']);
  assert.equal(owner.rule, '', 'an explicit "always" is no rule to describe');
  assert.equal(A.optionConfirm('Codex', owner.options[1]), 'Yes, and bypass permissions: Codex then stops asking before it edits files or runs commands in this conversation. Go ahead?');
  assert.equal(A.modeConfirm('Codex', { name: 'Full access' }), 'Switch Codex to Full access? It stops asking before it edits files or runs commands in this conversation.');
});

test('permission: a host whose "always" is its own rule offers none it cannot scope; a plan\'s options are modes', () => {
  const unscoped = perm({ rule: null });
  assert.deepEqual(A.permission(unscoped).options.map((o) => o.id), ['allow', 'always', 'reject'], 'the agent remembers by its own rule: offered');
  assert.deepEqual(A.permission(unscoped, { scopedOnly: true }).options.map((o) => o.id), ['allow', 'reject']);
  assert.deepEqual(A.permission(perm(), { scopedOnly: true }).options.map((o) => o.id), ['allow', 'always', 'reject']);
  const plan = perm({ rule: null, plan: { text: '# Plan\n1. x', path: '/p.md' }, tool: { title: 'Ready to code?', kind: 'switch_mode' },
    options: [opt('auto', 'allow_always', { name: 'Yes, auto-accept edits' }), opt('manual', 'allow_once'), opt('keep', 'reject_once', { name: 'No, keep planning' })] });
  const p = A.permission(plan, { name: 'Claude Code', scopedOnly: true });
  assert.deepEqual([p.plan, p.planText, p.planPath, p.lead, p.rule, p.raw], [true, '# Plan\n1. x', '/p.md', 'Claude Code has a plan', '', '']);
  assert.deepEqual(p.options.map((o) => o.id), ['auto', 'manual', 'keep'], 'never "remember this"');
  assert.equal(p.reject, 'keep');
});

test('permission: a diff in the request is previewed, else the call\'s own patches', () => {
  const edit = perm({ tool: { title: 'Edit a.go', kind: 'edit', content: [{ type: 'diff', path: '/a.go', oldText: 'a\nb', newText: 'a\nc' }] } });
  assert.deepEqual(A.permission(edit).preview.map((d) => [d.path, d.add, d.del]), [['/a.go', 1, 1]]);
  const patches = [{ path: 'b.go', add: 1, del: 0, patch: '@@ -0,0 +1 @@\n+x' }];
  assert.equal(A.permission(perm(), { diffs: patches }).preview[0].path, 'b.go');
  assert.equal(A.permission(perm()).preview, null);
});

test('answering: an option or a bare decision, a verdict, and feedback only with a rejection', () => {
  const p = A.permission(perm());
  assert.deepEqual(A.permAnswer(p.options[0], 'ignored'), { optionId: 'allow', verdict: 'allow', feedback: '' });
  assert.deepEqual(A.permAnswer(p.options[2], '  use the fake clock  '), { optionId: 'reject', verdict: 'reject', feedback: 'use the fake clock' });
  assert.deepEqual(A.decisions(perm()).map((d) => d.decision), ['allow_once', 'allow_always', 'reject_once']);
  assert.deepEqual(A.decisions(perm({ rule: null })).map((d) => [d.decision, d.name]), [['allow_once', 'Allow once'], ['reject_once', 'Deny']]);
  assert.deepEqual(A.permAnswer(A.decisions(perm())[2], 'no'), { decision: 'reject_once', verdict: 'reject', feedback: 'no' });
  assert.deepEqual([A.isAllow({ kind: 'allow_always' }), A.isReject({ kind: 'reject_once' }), A.isAllow({}), A.isReject(null)], [true, true, false, false]);
});

test('question: a form, a page to open, or only its message', () => {
  const ask = (fields) => ({ kind: 'ask', eid: 'e1', callId: '', message: 'Which?', mode: 'form', schema: null, url: '', action: null, by: null, content: null, ...fields });
  const form = A.question(ask({ schema: { type: 'object', properties: { db: { type: 'string', enum: ['pg', 'sqlite'] } } } }));
  assert.deepEqual([form.mode, form.message, form.fields.map((f) => [f.key, f.kind])], ['form', 'Which?', [['db', 'radio']]]);
  assert.deepEqual(A.question(ask({ mode: 'url', url: 'https://example.test/device' })), { message: 'Which?', mode: 'url', url: 'https://example.test/device', text: 'https://example.test/device' });
  assert.deepEqual(A.question(ask({ mode: 'url', url: 'javascript:alert(1)' })), { message: 'Which?', mode: 'url', url: '', text: 'javascript:alert(1)' }, 'a link only when it is http(s)');
  assert.deepEqual(A.question(ask({ mode: 'url', schema: { url: 'http://x.test' } })).url, 'http://x.test');
  assert.deepEqual(A.question(ask({ mode: 'url' })), { message: 'Which?', mode: 'url', url: '', text: '' });
  assert.deepEqual(A.question(ask({ mode: 'text' })), { message: 'Which?', mode: 'text' });
  assert.equal(A.question(null), null);
});

test('the Agent tab\'s fold keeps a question\'s mode and url', () => {
  const fold = new Fold([
    { seq: 1, ts: 1, type: 'elicitation.request', data: { eid: 'e1', message: 'Enter code AB-12 at https://example.test/device', schema: null, mode: 'url', url: 'https://example.test/device' } },
    { seq: 2, ts: 2, type: 'elicitation.request', data: { eid: 'e2', message: 'Which?', schema: { type: 'object', properties: {} } } },
  ]);
  assert.deepEqual(fold.blocks.map((b) => [b.eid, b.mode, b.url]), [['e1', 'url', 'https://example.test/device'], ['e2', 'form', '']]);
  assert.deepEqual(A.question(fold.blocks[0]), { message: 'Enter code AB-12 at https://example.test/device', mode: 'url', url: 'https://example.test/device', text: 'https://example.test/device' });
});

const other = (q, more = {}) => ({ type: 'string', title: 'Other', description: 'Type your own', _meta: { _askUserQuestionCustomAnswer: { questionId: q, isCustomAnswer: true } }, ...more });

test('formFields: one choice (oneOf, anyOf, enum with enumNames), several, booleans, numbers, text — defaults and "Other"', () => {
  const schema = { type: 'object', required: ['db', 'name'], properties: {
    db: { type: 'string', title: 'DB', description: 'Which database?', oneOf: [{ const: 'pg', title: 'Postgres', description: 'default' }, { const: 'sqlite', title: 'SQLite' }], default: 'pg' },
    db_other: other('db'),
    tier: { title: 'Tier', anyOf: [{ const: 1, title: 'One' }, { const: 2 }, { value: 'v' }] },
    region: { type: 'string', enum: ['eu', 'us'], enumNames: ['Europe', 'United States'] },
    plain: { type: 'string', enum: ['a', 'b'] },
    extras: { type: 'array', title: 'Extras', items: { anyOf: [{ const: 'metrics', title: 'Metrics' }, { const: 'tracing', title: 'Tracing' }] }, default: ['metrics'] },
    tags: { type: 'array', items: { enum: ['x', 'y'], enumNames: ['Ex', 'Why'] } },
    port: { type: 'integer', title: 'Port', default: 8080 }, tls: { type: 'boolean', title: 'TLS', default: true }, name: { type: 'string', title: 'Name' },
    orphan: other('nope', { description: '' }), skipped: null,
  } };
  const f = A.formFields(schema);
  assert.deepEqual(f.map((x) => [x.key, x.kind, x.required, x.other]), [['db', 'radio', true, 'db_other'], ['tier', 'radio', false, null], ['region', 'radio', false, null], ['plain', 'radio', false, null],
    ['extras', 'check', false, null], ['tags', 'check', false, null], ['port', 'number', false, null], ['tls', 'bool', false, null], ['name', 'text', true, null], ['orphan', 'text', false, null]]);
  assert.deepEqual(f[0].options, [{ value: 'pg', title: 'Postgres', description: 'default' }, { value: 'sqlite', title: 'SQLite', description: '' }]);
  assert.deepEqual([f[0].dflt, f[0].otherHint, f[4].dflt, f[6].dflt, f[7].dflt, f[8].dflt], ['pg', 'Type your own', ['metrics'], 8080, true, undefined]);
  assert.deepEqual(f[1].options.map((o) => [o.value, o.title]), [[1, 'One'], [2, '2'], ['v', 'v']], 'anyOf is a single choice too; a choice without a title is called by its value');
  assert.deepEqual(f[2].options.map((o) => [o.value, o.title]), [['eu', 'Europe'], ['us', 'United States']]);
  assert.deepEqual(f[3].options.map((o) => [o.value, o.title]), [['a', 'a'], ['b', 'b']], 'an enum without enumNames is labelled by its values');
  assert.deepEqual(f[5].options.map((o) => o.title), ['Ex', 'Why']);
  assert.deepEqual([f[9].title, f[9].kind], ['Other', 'text'], 'an "Other" whose question is not there stands alone');
  assert.deepEqual(A.formFields(null), []);
  assert.deepEqual(A.formFields({ properties: 'no' }), []);

  // untouched: every default answers; "Other" answers the required choice
  assert.deepEqual(A.formContent(f, {}), { db: 'pg', extras: ['metrics'], port: 8080, tls: true });
  const c = A.formContent(f, { db: '', db_other: '  cockroach ', extras: [], port: '', tls: false, name: ' ', region: 'us' });
  assert.deepEqual(c, { db_other: 'cockroach', tls: false, region: 'us' }, 'a cleared field is left out, never its default');
  assert.deepEqual(A.missingRequired(f, c), ['Name'], 'the choice is answered by its Other box');
  assert.deepEqual(A.missingRequired(f, {}), ['DB', 'Name']);
  assert.deepEqual(A.formContent(f, { port: 'abc', name: 'x' }), { db: 'pg', extras: ['metrics'], tls: true, name: 'x' });
});

test('nativeSchema / nativeContent: the form as the native question\'s flat schema, and its answers back', () => {
  const f = A.formFields({ type: 'object', required: ['db', 'n'], properties: {
    db: { title: 'DB', description: 'Which?', oneOf: [{ const: 1, title: 'One' }, { const: 2, title: 'Two' }], default: 1 }, db_other: other('db'),
    extras: { type: 'array', title: 'Extras', description: 'Pick', items: { anyOf: [{ const: 'm', title: 'Metrics', description: 'counters' }, { const: 't', title: 'Tracing' }] } },
    n: { type: 'number' }, ok: { type: 'boolean', title: 'OK' } } });
  const s = A.nativeSchema(f, 'Answer these');
  assert.deepEqual(Object.keys(s.properties), ['db', 'db_other', 'extras.0', 'extras.1', 'n', 'ok']);
  assert.deepEqual(s.properties.db, { title: 'DB', description: 'Which?', type: 'string', oneOf: [{ const: '1', title: 'One' }, { const: '2', title: 'Two' }], default: 1 });
  assert.deepEqual(s.properties['extras.0'], { type: 'boolean', title: 'Extras: Metrics', description: 'Pick — counters' });
  assert.deepEqual([s.description, s.required], ['Answer these', ['n']], 'a choice with an Other box is not required of the flat form: either answers it');
  assert.deepEqual(A.nativeContent(f, { db: '2', 'extras.1': true, 'extras.0': false, n: '3', ok: true }), { db: 2, extras: ['t'], n: 3, ok: true });
});

const cmds = [
  { name: 'review', description: 'Review the pending changes', hint: 'what to focus on' },
  { name: 'compact', description: 'Summarize the conversation' },
  { name: 'init', description: 'Write a CLAUDE.md' },
  { name: 'pr-comments', description: 'Show pull request comments' },
];

test('the slash menu: names that start with what is typed, then names or descriptions containing it, at most eight', () => {
  assert.equal(A.SLASH_MAX, 8);
  assert.deepEqual(A.slashMenu(cmds, '/').map((c) => c.name), ['review', 'compact', 'init', 'pr-comments']);
  assert.deepEqual(A.slashMenu(cmds, '/c').map((c) => c.name), ['compact', 'review', 'init', 'pr-comments'], '"c" is in three descriptions');
  assert.deepEqual(A.slashMenu(cmds, '/PULL').map((c) => c.name), ['pr-comments'], 'a description matches');
  assert.deepEqual(A.slashMenu(cmds, '/zzz'), []);
  assert.equal(A.slashMenu(cmds, '/review '), null, 'completed: its arguments follow');
  assert.equal(A.slashMenu(cmds, 'hi /review'), null);
  assert.equal(A.slashMenu(cmds, ''), null);
  const many = Array.from({ length: 20 }, (_, i) => ({ name: 'cmd' + i }));
  assert.equal(A.slashMenu(many, '/').length, 8);
  assert.equal(A.slashMenu(many, '/', 12).length, 12, 'a host with room asks for more');
  assert.deepEqual(A.slashCommands([{ name: '/review', description: 'r', input: { hint: 'what' } }, { description: 'no name' }, null]), [{ name: 'review', description: 'r', hint: 'what' }]);
  assert.deepEqual(A.slashCommands(null), []);
  assert.equal(A.slashText({ name: 'review' }), '/review ');
  assert.deepEqual(A.commandHint(cmds, '/review '), { name: 'review', hint: 'what to focus on' });
  assert.equal(A.commandHint(cmds, '/compact '), null);
});

test('the composer\'s words: steering or waiting while a turn runs, what a reply does while the agent waits', () => {
  assert.deepEqual(A.composerWords({ name: 'Claude Code', busy: true, steering: true }),
    { busy: true, steering: true, placeholder: 'steer Claude Code — sent into its running turn (⌘/Ctrl+Enter interrupts)', label: 'steering', title: 'being sent into Claude Code\'s running turn' });
  assert.deepEqual(A.composerWords({ name: 'Codex', busy: true, native: true }),
    { busy: true, steering: false, placeholder: 'queued — sent to Codex when this turn ends (Send now interrupts)', label: 'queued for Codex', title: 'sent to Codex when its current turn ends' });
  assert.equal(A.composerWords({ name: 'Codex' }).placeholder, 'message Codex…');
  assert.equal(A.composerWords({ name: 'Codex', waits: 'login' }).placeholder, 'sign in to Codex first — then message it…');
  assert.equal(A.composerWords({ name: 'Codex', waits: 'approval' }).placeholder, 'reply — rejects the request, then goes to Codex');
  assert.equal(A.composerWords({ name: 'Codex', waits: 'question' }).placeholder, 'reply — skips the question, then goes to Codex');
  assert.deepEqual(A.composerWords({ name: 'Codex', busy: true, barred: 'It is over.' }), { busy: false, steering: false, placeholder: 'It is over.', label: 'queued for Codex', title: 'It is over.' });
  assert.equal(A.composerWords().placeholder, 'message the agent…');
});

test('steerTracker: a waiting message that left the queue mid-turn and showed up in the transcript was steered', () => {
  const track = A.steerTracker(6000);
  let transcript = ['fix it'];
  const s = (queued, more = {}) => ({ run: 1, steering: true, turn: true, queued, count: (text) => transcript.filter((t) => t === text).length, ...more });
  assert.deepEqual(track(s([{ id: 'q1', text: 'also the docs' }]), 1000), []);
  assert.deepEqual(track(s([]), 2000), [], 'gone from the queue, not in the transcript yet');
  transcript = ['fix it', 'also the docs'];
  assert.deepEqual(track(s([]), 3000), [{ text: 'also the docs', until: 9000 }]);
  assert.deepEqual(track(s([]), 8000), [{ text: 'also the docs', until: 9000 }]);
  assert.deepEqual(track(s([]), 9000), [], 'a note lasts ms');
  // waited while no turn ran: the next prompt, no steer
  track(s([{ id: 'q2', text: 'next' }], { turn: false }), 10000);
  transcript.push('next');
  assert.deepEqual(track(s([]), 11000), []);
  // an agent that does not steer; a message taken back; another conversation
  const plain = A.steerTracker();
  plain(s([{ id: 'q3', text: 'x' }], { steering: false }), 0);
  transcript.push('x');
  assert.deepEqual(plain(s([], { steering: false }), 1), []);
  const back = A.steerTracker();
  back(s([{ id: 'q4', text: 'never sent' }]), 0);
  assert.deepEqual(back(s([]), 1), []);
  assert.deepEqual(back(null, 2), []);
});

test('cleanCode: a pasted code as a CLI reads it', () => {
  assert.equal(A.cleanCode('  AB12-\n cd34\t\x07 '), 'AB12-cd34');
  assert.equal(A.cleanCode(null), '');
});

// ---- the old modules are the kit ----------------------------------------------------------

test('/vendor/agent-tools.js, /vendor/agent-slash.js, /vendor/signin-scan.js: the kit\'s own functions under their names', () => {
  for (const name of ['formFields', 'formContent', 'missingRequired']) assert.equal(oldTools[name], A[name], name);
  for (const name of ['slashQuery', 'matchCommands', 'commandHint']) assert.equal(oldSlash[name], A[name], name);
  assert.deepEqual(Object.keys(oldSlash).sort(), ['commandHint', 'matchCommands', 'slashQuery']);
  assert.equal(scanClean, A.cleanCode);
});

test('the agent template\'s model/harness-ask.js and model/harness-signins.js: the kit\'s functions; a park becomes the kit\'s block', () => {
  for (const name of ['isAllow', 'isReject', 'formFields', 'formContent', 'missingRequired', 'nativeSchema', 'nativeContent', 'modeConfirm', 'optionConfirm', 'slashText']) assert.equal(HA[name], A[name], name);
  assert.equal(HS.cleanCode, A.cleanCode);
  for (const name of ['isAllow', 'isReject', 'ownerOf', 'permission', 'contentPreview', 'diffsPreview', 'lineDiff', 'patchOf', 'question', 'formFields', 'formContent', 'missingRequired',
    'nativeSchema', 'nativeContent', 'controls', 'modeConfirm', 'optionConfirm', 'settingOf', 'slashCommands', 'slashMatches', 'slashText', 'steerWords', 'steerTrack']) {
    assert.ok(name in HA, `${name}: an export it had`);
  }
  const ps = { kind: 'approval', park: 'pk1', harness: { pid: 'p7', callId: 'h1:toolu_02', defaultToNo: true, description: 'why',
    tool: { title: 'go test ./...', kind: 'execute', name: 'Bash', label: 'Run the tests', command: 'go test ./...', rawInput: { command: 'go test ./...' } },
    options: [{ optionId: 'allow', name: 'Allow', kind: 'allow_once' }, { optionId: 'bypass', name: 'Bypass', kind: 'allow_always', explicit: true }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }],
    rule: { kind: 'execute', title: 'go test ./...' } } };
  const block = HA.permBlock(ps);
  assert.deepEqual([block.kind, block.k, block.pid, block.park, block.callId, block.plan, block.rule, block.defaultToNo], ['perm', 'park:pk1', 'p7', 'pk1', 'toolu_02', null, { kind: 'execute', title: 'go test ./...' }, true]);
  const card = HA.permission(ps, { owner: false, talk: true, name: 'Claude Code' });
  assert.deepEqual(card, { park: 'pk1', ...A.permission(block, { name: 'Claude Code', can: { answer: true, raise: false } }), talk: true });
  assert.deepEqual([card.park, card.talk, card.options.map((o) => o.id), card.hidden], ['pk1', true, ['reject', 'allow'], 1]);
  assert.equal(HA.permBlock({ kind: 'question', harness: {} }), null);
  const q = { kind: 'question', park: 'pk2', harness: { eid: 'e3', callId: 'h1:toolu_03#2', message: 'Open it', mode: 'url', url: 'https://example.test/x' } };
  assert.deepEqual(HA.askBlock(q), { k: 'park:pk2', v: 0, kind: 'ask', eid: 'e3', park: 'pk2', callId: 'toolu_03', message: 'Open it', mode: 'url', schema: null, url: 'https://example.test/x', action: null, by: null, content: null });
  assert.deepEqual(HA.question(q), { park: 'pk2', ...A.question(HA.askBlock(q)) });
  // the template's slash menu is the kit's rule: descriptions match, at most eight
  const list = HA.slashCommands({ commands: [...cmds, ...Array.from({ length: 10 }, (_, i) => ({ name: 'x' + i }))] });
  assert.deepEqual(HA.slashMatches(list, '/pull').map((c) => c.name), ['pr-comments']);
  assert.equal(HA.slashMatches(list, '/').length, 8);
  assert.equal(HA.slashMatches(list, '/review now'), null);
});
