// hack/chat-tools.test.mjs — the chat kit's model of one tool call
// (web/chat/tools.js, /vendor/chat/tools.js, D224), run by `make js-test`:
// what its card says read from the ToolRecord — headline, the command under
// it, outcome and state chips, the output's tail, diffs, plan approval — and
// that the modules it was made from are it: web/agent-tools.js (the Agent
// tab's old record and names; hack/agent-tools.test.mjs holds their outputs)
// and the agent template's model/harness-heads.js (its row shape;
// hack/agent-template-harness*.test.mjs). The record's merge rules are held
// by the shared transcripts (hack/chat-fixtures.test.mjs).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { newToolRecord } from '../web/chat/blocks.js';
import * as T from '../web/chat/tools.js';
import * as old from '../web/agent-tools.js';
import { installHooks } from './xbn/hooks.mjs';

installHooks(); // /vendor/<name> as xbind serves it: the template's models import the kit by URL
const HH = await import('../builtin-templates/agent/model/harness-heads.js');
const TH = await import('../builtin-templates/agent/model/tool-heads.js');

const rec = (fields) => ({ ...newToolRecord('t1'), ...fields });

test('headline: label, a description, the command read deterministically, the title, a reading by kind, the name, the id', () => {
  const script = "python3 - <<'EOF'\nopen('x.py')\nEOF";
  assert.equal(T.headline(rec({ kind: 'execute', label: 'Rewrite the handler', title: script, rawInput: { command: script } })), 'Rewrite the handler');
  assert.equal(T.headline(rec({ kind: 'execute', title: 'ls', rawInput: { command: 'ls', description: 'List files' } })), 'List files');
  assert.equal(T.headline(rec({ kind: 'execute', title: script, rawInput: { command: script } })), 'Python script (1 line) → x.py');
  assert.equal(T.headline(rec({ kind: 'execute', title: 'go test ./...' })), 'go test ./...', 'an execute call\'s title is its command');
  assert.equal(T.headline(rec({ kind: 'read', title: 'Read src/main.go\n(lines 1-40)' })), 'Read src/main.go …');
  // nothing better: the arguments, read by kind
  assert.equal(T.headline(rec({ kind: 'read', rawInput: { file_path: '/w/a.go', offset: 10, limit: 40 } })), 'Read /w/a.go:10–49');
  assert.equal(T.headline(rec({ kind: 'search', rawInput: { pattern: 'retry\\(', path: 'src' } })), 'Search /retry\\(/ in src');
  assert.equal(T.headline(rec({ kind: 'edit', locations: [{ path: 'b.go', line: 3 }] })), 'Edit b.go');
  assert.equal(T.headline(rec({ kind: 'execute' })), 'Run a command');
  assert.equal(T.headline(rec({ kind: 'other', name: 'WebSearch' })), 'WebSearch');
  assert.equal(T.headline(rec({ kind: 'other' })), 't1');
  assert.equal(T.headline({ kind: 'other' }), 'a tool call');
  assert.equal(T.headline(null), '');
  // while it runs, a short one-line text says a shell call first (gemini's "[in dir] (description)")
  const g = rec({ kind: 'execute', title: 'ls -la', status: 'pending', text: '[in /w] (List the files)' });
  assert.equal(T.headline(g), '[in /w] (List the files)');
  assert.equal(T.headline({ ...g, status: 'completed' }), 'ls -la');
  // without the reading by kind: what /vendor/agent-tools.js headline always said
  assert.equal(T.headline(rec({ kind: 'read', name: 'Read', rawInput: { file_path: '/w/a.go' } }), { reading: false }), 'Read');
});

test('reading, subline: a call from its arguments; an execute\'s command under a headline that says something else', () => {
  assert.equal(T.reading({ kind: 'execute', rawInput: { command: 'go   test\n./...' } }), '$ go test ./...');
  assert.equal(T.reading({ kind: 'execute', title: 'Run it' }), 'Run it');
  assert.equal(T.reading({ kind: 'read', rawInput: { path: 'a', offset: 5 } }), 'Read a:5–');
  assert.equal(T.reading({ kind: 'delete' }), 'Delete a file');
  assert.equal(T.reading({ kind: 'move', rawInput: { source: 'a', destination: 'b' } }), 'Move a → b');
  assert.equal(T.reading({ kind: 'fetch', rawInput: { url: 'https://x.test/' } }), 'https://x.test/');
  assert.equal(T.reading({ kind: 'think', title: 'Explore' }), 'Explore');
  assert.equal(T.reading({ kind: 'switch_mode' }), 'Switch mode');
  assert.equal(T.reading({ kind: 'other', name: 'TodoWrite' }), 'TodoWrite');
  assert.equal(T.reading({}), 'tool');

  const run = rec({ kind: 'execute', label: 'Run the tests', rawInput: { command: 'go test ./...' } });
  assert.equal(T.subline(run), '$ go test ./...');
  assert.equal(T.subline(rec({ kind: 'execute', title: 'go test ./...', rawInput: { command: 'go test ./...' } })), '', 'the headline is the command');
  assert.equal(T.subline(run, 'go test ./...'), '');
  assert.equal(T.subline(rec({ kind: 'read', label: 'x', rawInput: { command: 'cat a' } })), '', 'only an execute');
  assert.equal(T.subline(rec({ kind: 'execute', label: 'x' })), '', 'no command among the arguments');
});

test('the command and the path among a call\'s arguments', () => {
  assert.equal(T.inputCommand({ command: 'ls' }), 'ls');
  assert.equal(T.inputCommand({ command: ['git', 'status'] }), 'git status');
  assert.equal(T.inputCommand({ cmd: 'ls', args: ['-l', 'x'] }), 'ls -l x');
  assert.equal(T.inputCommand({ script: 'echo 1' }), 'echo 1');
  assert.equal(T.inputCommand({ command: null, cmd: 'fallback' }), 'fallback');
  assert.equal(T.inputCommand(null), '');
  assert.equal(T.inputCommand('ls'), '');
  assert.equal(T.commandOf(rec({ kind: 'execute', title: 'make' })), 'make');
  assert.equal(T.commandOf(rec({ kind: 'read', title: 'make' })), '');
  assert.equal(T.commandOf(rec({ kind: 'execute', title: 'x', rawInput: { command: '' } })), '', 'an empty command is the command');
  for (const k of ['file_path', 'path', 'filePath', 'notebook_path', 'abs_path']) assert.equal(T.inputPath({ [k]: '/a' }), '/a');
  assert.equal(T.inputPath({ path: 3 }), '');
  assert.equal(T.rawText({ cmd: 'a', args: ['b'] }), 'a b');
  assert.equal(T.rawText({ path: 'x' }), '{\n "path": "x"\n}');
  assert.equal(T.taskOf({ prompt: 'p', description: 'd' }), 'p');
  assert.deepEqual(T.placesOf({ locations: [{ path: 'a', line: 3 }, { path: 'b' }] }), ['a:3', 'b']);
  assert.deepEqual(T.placesOf({ files: ['c'] }), ['c']);
});

test('outcome: what a completed call came to; stateChip: its state until then', () => {
  const done = (fields) => rec({ status: 'completed', ...fields });
  assert.deepEqual(T.outcome(done({ kind: 'execute', exitCode: 0 })), { text: 'exit 0', tone: 'ok' });
  assert.deepEqual(T.outcome(done({ kind: 'execute', exitCode: 3 })), { text: 'exit 3', tone: 'bad' });
  assert.equal(T.outcome(done({ kind: 'execute' })), null);
  assert.deepEqual(T.outcome(done({ kind: 'edit', diffs: [{ path: 'a', add: 2, del: 1 }] })), { text: '+2 −1', tone: '' });
  assert.deepEqual(T.outcome(done({ kind: 'edit', diffs: [{ path: 'a', add: 2, del: 1 }, { path: 'b', add: 1, del: 0 }] })), { text: '2 files +3 −1', tone: '' });
  assert.deepEqual(T.outcome(done({ kind: 'delete', diffs: [{ path: 'a', add: 0, del: 4 }] })), { text: '−4', tone: '' });
  assert.deepEqual(T.outcome(done({ kind: 'read', text: 'a\nb\n\n' })), { text: '2 lines', tone: '' });
  assert.deepEqual(T.outcome(done({ kind: 'search', locations: [{ path: 'a' }] })), { text: '1 match', tone: '' });
  assert.equal(T.outcome(rec({ kind: 'execute', status: 'failed', exitCode: 1 })), null, 'a failure is the card\'s state');
  assert.equal(T.outcome(null), null);

  assert.deepEqual(T.stateChip(rec({ status: 'pending' }), 'approval'), { text: 'needs approval', tone: 'warn' });
  assert.deepEqual(T.stateChip(rec({ status: 'failed' }), 'error'), { text: 'failed', tone: 'bad' });
  assert.deepEqual(T.stateChip(rec({ status: 'cancelled' }), 'stopped'), { text: 'cancelled', tone: '' });
  assert.deepEqual(T.stateChip(rec({ status: 'pending' }), 'writing'), { text: 'writing', tone: 'run' });
  assert.deepEqual(T.stateChip(rec({ status: 'pending' }), 'pending'), { text: 'pending', tone: '' });
  assert.deepEqual(T.stateChip(rec({ status: 'in_progress' }), 'running'), { text: 'running', tone: 'run' });
  assert.equal(T.stateChip(rec({ status: 'completed' }), 'done'), null);
});

test('stripAnsi: one rule — CSI with any parameter byte, an OSC up to its own terminator, two-byte escapes', () => {
  assert.equal(T.stripAnsi('\x1b[31mred\x1b[0m ok\x1b[2K'), 'red ok');
  assert.equal(T.stripAnsi('\x1b]0;title\x07text'), 'text');
  assert.equal(T.stripAnsi('\x1b[1;2:3mx'), 'x', 'colon sub-parameters');
  assert.equal(T.stripAnsi('\x1b[?25lhidden\x1b[?25h'), 'hidden');
  assert.equal(T.stripAnsi('\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\'), 'link', 'a hyperlink keeps its text');
  assert.equal(T.stripAnsi('\x1b[38;2;1;2;3mrgb\x1b[m \x1bMx'), 'rgb x', 'ESC M: a two-byte escape');
  assert.equal(T.stripAnsi('a\x1b(Bb\x9b1mc'), 'abc', 'a character-set designation; the 8-bit CSI');
  assert.equal(T.stripAnsi('a\r\nb\x07'), 'a\r\nb\x07', 'nothing but escapes goes');
  assert.equal(T.stripAnsi(null), '');
});

test('outputTail: the newest characters, what was cut here and what the reducer dropped before', () => {
  assert.equal(T.OUTPUT_TAIL, 20000);
  assert.deepEqual(T.outputTail({ output: '\x1b[1mabc\x1b[0mdef' }, 4), { text: 'cdef', cut: 2, dropped: 0 });
  assert.deepEqual(T.outputTail({ output: 'abc', outputTruncated: 9 }), { text: 'abc', cut: 0, dropped: 9 });
  assert.deepEqual(T.outputTail(null), { text: '', cut: 0, dropped: 0 });
  assert.equal(T.outputTail({ output: 'x'.repeat(T.OUTPUT_TAIL + 5) }).cut, 5);
});

test('diffs: a patch or the two texts — totals, one patch per file, all of them as one, a patch\'s lines', () => {
  const texts = T.toolDiff({ type: 'diff', path: 'a.txt', oldText: 'a\nb\n', newText: 'a\nc\nd\n' }, rec({ kind: 'edit' }));
  assert.deepEqual(texts, { path: 'a.txt', status: 'modified', add: 2, del: 1, oldText: 'a\nb\n', newText: 'a\nc\nd\n' });
  assert.equal(T.toolDiff({ path: 'n', newText: 'x' }, null).status, 'added');
  assert.deepEqual([T.toolDiff({ path: 'd', oldText: 'x\ny', newText: '' }, rec({ kind: 'delete' }))].map((d) => [d.status, d.add, d.del]), [['deleted', 0, 2]]);
  const patch = { path: 'b.go', status: 'added', add: 1, del: 0, patch: '+++ b/b.go\n+new', truncated: true };
  const r = rec({ kind: 'edit', diffs: [texts, patch, { add: 9 }] });
  assert.deepEqual(T.diffTotals(r), { files: 3, add: 12, del: 1 });
  const files = T.diffFiles(r);
  assert.deepEqual(files.map((d) => [d.path, d.status, d.truncated, 'oldText' in d]), [['a.txt', 'modified', false, true], ['b.go', 'added', true, false]], 'a diff without a path is dropped');
  assert.equal(T.diffPatch(patch), patch.patch);
  assert.equal(T.diffPatch(texts), T.unifiedDiff('a.txt', 'a\nb\n', 'a\nc\nd\n'));
  assert.equal(T.diffPatch({ path: 'x', status: 'deleted' }), '');
  assert.match(T.diffPatch(texts), /^diff --git a\/a\.txt b\/a\.txt\n--- a\/a\.txt\n\+\+\+ b\/a\.txt\n@@ -1,3 \+1,4 @@\n a\n-b\n\+c\n\+d\n \n$/);
  const all = T.joinPatches(r);
  assert.equal(all.patch, T.diffPatch(texts) + patch.patch + '\n');
  assert.deepEqual([all.left, all.files.length], [0, 2]);
  assert.deepEqual([T.joinPatches(r, 80).left, T.joinPatches(r, 5).patch], [1, '']);
  assert.deepEqual(T.patchLines('--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n-x\n+y\n ctx\n').map((l) => l.cls), ['fh', 'fh', 'h', 'a', 'd', 'ctx']);
  assert.deepEqual(T.filesStat({ files: [{ add: 2, del: 1 }, { add: 1 }] }), { n: 2, add: 3, del: 1 });
  assert.deepEqual(T.filesStat({ changes: [{ add: 2, del: 1 }] }), { n: 1, add: 2, del: 1 }, 'a files.changed record');
  assert.deepEqual(T.filesStat(null), { n: 0, add: 0, del: 0 });
});

test('a request\'s preview: the changed lines of a diff, a text, a record\'s patches', () => {
  const d = T.lineDiff('/a.go', 'one\ntwo\nthree\nfour\nfive', 'one\ntwo\nTHREE\nfour\nfive');
  assert.deepEqual(d.lines.map((l) => l.t + l.text), ['@@@ -1 +1 @@', ' one', ' two', '-three', '+THREE', ' four', ' five']);
  assert.deepEqual([d.add, d.del, d.more], [1, 1, 0]);
  assert.equal(T.patchOf(d), '--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n one\n two\n-three\n+THREE\n four\n five');
  assert.equal(T.lineDiff('n', null, Array.from({ length: 100 }, (_, i) => 'l' + i).join('\n')).more, 101 - T.PREVIEW_LINES);
  assert.deepEqual(T.contentPreview([{ type: 'diff', path: 'p', oldText: 'a', newText: 'b' }, { type: 'content', content: { type: 'text', text: 'why' } }, null]).map((x) => x.path || x.text), ['p', 'why']);
  assert.equal(T.contentPreview([]), null);
  assert.deepEqual(T.diffsPreview([{ path: 'p', add: 1, del: 1, patch: '--- a/p\n+++ b/p\n@@ -1 +1 @@\n-a\n+b\n' }])[0].lines, [{ t: '@', text: '@@ -1 +1 @@' }, { t: '-', text: 'a' }, { t: '+', text: 'b' }]);
  assert.equal(T.diffsPreview([{ path: 'p' }]), null);
});

test('plan approval: a mode switch, a plan review, a plan among the arguments — its text and its file', () => {
  const claude = { kind: 'switch_mode', title: 'Approve Plan', rawInput: { plan: '# P', planFilePath: '/p.md' }, content: [{ type: 'content', content: { type: 'text', text: '# P\n\n1. x' } }] };
  assert.ok(T.isPlanApproval(claude));
  assert.equal(T.planText(claude), '# P\n\n1. x', 'a request\'s tool: its content list');
  assert.equal(T.planPath(claude), '/p.md');
  assert.equal(T.planText(rec({ kind: 'switch_mode', text: '# From the record' })), '# From the record');
  assert.equal(T.planText({ kind: 'switch_mode', rawInput: { plan: '## Codex plan' } }), '## Codex plan');
  assert.ok(T.isPlanApproval(rec({ planReview: true })));
  assert.ok(T.isPlanApproval({ kind: 'other', rawInput: { plan: 'x' } }));
  assert.ok(!T.isPlanApproval(rec({ kind: 'execute', rawInput: { command: 'ls' } })));
  assert.equal(T.planPath(null), '');
});

// ---- the old modules are the kit ----------------------------------------------------------

test('/vendor/agent-tools.js: its helpers are the kit\'s own functions; its record carries the kit\'s', () => {
  for (const name of ['firstLine', 'describeCommand', 'filesStat', 'stripAnsi', 'rawText', 'unifiedDiff']) assert.equal(old[name], T[name], name);
  assert.deepEqual(Object.keys(old).sort(), ['commandOf', 'describeCommand', 'filesStat', 'firstLine', 'foldTool', 'formContent', 'formFields', 'headline', 'isPlanApproval',
    'missingRequired', 'newTool', 'planText', 'rawText', 'stripAnsi', 'unifiedDiff'], 'every export it had, nothing taken away');
  const t = old.newTool('t9');
  assert.deepEqual(t.rec, newToolRecord('t9'));
  old.foldTool(t, { kind: 'edit', status: 'in_progress', title: 'Edit a.txt', locations: [{ path: 'a.txt', line: 2 }], content: [{ type: 'diff', path: 'a.txt', oldText: 'a\n', newText: 'b\n' }] });
  old.foldTool(t, { status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'done' } }] });
  assert.deepEqual([t.tk, t.status, t.content.length], ['edit', 'completed', 1], 'the old record: the last content list, whole');
  assert.deepEqual([t.rec.kind, t.rec.status, t.rec.text, t.rec.diffs.length, t.rec.files, t.rec.locations], ['edit', 'completed', 'done', 1, ['a.txt'], [{ path: 'a.txt', line: 2 }]]);
  assert.equal(old.headline(t), T.headline(t.rec));
  assert.deepEqual(T.outcome(t.rec), { text: '+1 −1', tone: '' });
  // an object that is not newTool's folds as it always did
  assert.deepEqual(old.foldTool({ output: '' }, { outputDelta: 'x', kind: 'read' }), { output: 'x', tk: 'read' });
  assert.deepEqual(old.foldTool({ output: 'gone' }, { output: 'W', outputDelta: 'd' }), { output: 'Wd' }, 'a whole output, then the delta');
  // the old headline says what it always said: no reading by kind
  assert.equal(old.headline(old.foldTool(old.newTool('r'), { kind: 'read', name: 'Read', rawInput: { file_path: '/w/a.go' } })), 'Read');
  assert.equal(old.headline(null), '');
  assert.equal(old.commandOf(undefined), '');
  assert.equal(old.planText(undefined), '');
  assert.equal(old.isPlanApproval(undefined), false);
});

test('the agent template\'s model/harness-heads.js: its helpers are the kit\'s; a row becomes the kit\'s record', () => {
  for (const [name, kit] of Object.entries({ stripAnsi: 'stripAnsi', outputTail: 'outputTail', diffTotals: 'diffTotals', diffFiles: 'diffFiles', joinPatches: 'joinPatches',
    patchLines: 'patchLines', placesOf: 'placesOf', taskOf: 'taskOf', acpChip: 'stateChip', pathOf: 'inputPath', commandOf: 'inputCommand' })) assert.equal(HH[name], T[kit], name);
  for (const name of ['ACP_FAMILY', 'ACP_ICON', 'ACP_NATIVE_ICON', 'isAcp', 'acpKind', 'acpFamily', 'pathOf', 'commandOf', 'acpReading', 'acpSubline', 'diffTotals', 'acpOutcome', 'acpState',
    'stripAnsi', 'outputTail', 'acpChip', 'isSubagentCall', 'stepsWords', 'taskOf', 'diffFiles', 'joinPatches', 'patchLines', 'placesOf', 'resultText']) {
    assert.ok(name in HH, `${name}: an export it had`);
  }
  const acp = { kind: 'execute', title: 'go test ./...', label: 'Run the tests', tool: 'Bash', status: 'completed', exitCode: 1, output: 'FAIL\n', parent: 'h1:task#2', locations: [{ path: 'a.go', line: 0 }] };
  const r = HH.acpRecord({ name: 'acp:execute', callId: 'h2:toolu_7#3', acp, result: 'FAIL' }, { command: 'go test ./...', summary: 'Run the tests' });
  assert.deepEqual([r.id, r.parent, r.kind, r.name, r.label, r.title, r.status, r.exitCode, r.output, r.terminal, r.rawInput, r.text],
    ['toolu_7', 'task', 'execute', 'Bash', 'Run the tests', 'go test ./...', 'completed', 1, 'FAIL\n', true, { command: 'go test ./...' }, '']);
  assert.deepEqual(Object.keys(r), Object.keys(newToolRecord('')), 'a ToolRecord, nothing else');
  assert.equal(HH.acpHeadline('acp:execute', { command: 'go test ./...', summary: 'Run the tests' }, acp), 'Run the tests');
  assert.equal(HH.acpSubline('acp:execute', { command: 'go test ./...', summary: 'Run the tests' }), '$ go test ./...');
  assert.deepEqual(HH.acpOutcome('acp:execute', 'FAIL', acp), { text: 'exit 1', tone: 'bad' });
  // a summary that is neither the title nor the kind is the adapter's label (a row whose lift does not say it)
  assert.equal(HH.acpRecord({ name: 'acp:read', acp: { title: 'Read /w/a.go' } }, { summary: 'Read a.go' }).label, 'Read a.go');
  assert.equal(HH.acpRecord({ name: 'acp:read', acp: { title: 'Read /w/a.go' } }, { summary: 'Read /w/a.go' }).label, '');
  assert.equal(HH.acpRecord({ name: 'acp:read', acp: {} }, { summary: 'read' }).label, '');
  // the text content: only what the row holds of it
  assert.equal(HH.acpRecord({ name: 'acp:fetch', acp: { status: 'completed', title: 'Fetch' }, result: 'the page' }, {}).text, 'the page');
  assert.equal(HH.acpRecord({ name: 'acp:fetch', acp: { status: 'in_progress' }, result: '(running…)' }, {}).text, '');
  assert.equal(HH.acpRecord({ name: 'acp:fetch', acp: { status: 'failed' }, result: 'error: 404' }, {}).text, '404');
  assert.equal(HH.acpRecord({ name: 'acp:fetch', acp: { status: 'completed', text: 'lifted' }, result: 'row' }, {}).text, 'lifted', 'a lift that carries the text says it');
});

test('the agent template\'s headline of a coding agent\'s call: the kit\'s, from its tool row', () => {
  const args = (o) => JSON.stringify(o);
  const script = "python3 - <<'EOF'\nopen('x.py')\nEOF";
  // a label: as before
  assert.equal(TH.headline('acp:execute', args({ command: 'go test ./...', summary: 'Run the tests' }), { kind: 'execute', title: 'go test ./...', label: 'Run the tests' }), 'Run the tests');
  // no label: the command read deterministically, not its first hundred characters
  assert.equal(TH.headline('acp:execute', args({ command: script, summary: script }), { kind: 'execute', title: script }), 'Python script (1 line) → x.py');
  assert.equal(TH.subline('acp:execute', args({ command: script, summary: script }), 'Python script (1 line) → x.py'), "$ python3 - <<'EOF' open('x.py') EOF");
  assert.equal(TH.headline('acp:execute', args({ command: 'go test ./...', summary: 'go test ./...' }), { kind: 'execute', title: 'go test ./...' }), 'go test ./...');
  assert.equal(TH.subline('acp:execute', args({ command: 'go test ./...', summary: 'go test ./...' }), 'go test ./...'), '');
  // neither label nor title (the summary is the kind): the reading by kind
  assert.equal(TH.headline('acp:read', args({ file_path: '/w/a.go', summary: 'read' }), { kind: 'read' }), 'Read /w/a.go');
  // a title of several lines: its first
  assert.equal(TH.headline('acp:read', args({ summary: 'Read a.go\n(lines 1-40)' }), { kind: 'read', title: 'Read a.go\n(lines 1-40)' }), 'Read a.go …');
  // without the tool row's acp (a caller from before; a call being written): the summary, else the reading
  assert.equal(TH.headline('acp:execute', args({ command: 'ls', summary: 'List' })), 'List');
  assert.equal(TH.headline('acp:execute', args({ command: 'ls' })), '$ ls');
  assert.equal(TH.subline('acp:execute', args({ command: 'ls', summary: 'List' })), '$ ls');
  // the fold's pair: a long command that is its own headline has no line under it
  const long = 'go test ./... -run ' + 'TestSomethingVeryLongIndeed|'.repeat(6) + 'X';
  const w = HH.acpWords('acp:execute', { command: long, summary: long }, { kind: 'execute', title: long });
  assert.deepEqual([w.headline.length, w.headline.endsWith('…'), w.sub], [100, true, '']);
  assert.deepEqual(HH.acpWords('acp:execute', { command: 'go test ./...', summary: 'Run the tests' }, { kind: 'execute', title: 'go test ./...', label: 'Run the tests' }),
    { headline: 'Run the tests', sub: '$ go test ./...' });
  assert.deepEqual(HH.acpWords('acp:execute', { command: 'ls', summary: 'List' }), { headline: 'List', sub: '$ ls' }, 'no tool row yet');
  // a call of the template's own tools is not the kit's business
  assert.equal(TH.headline('bash', args({ command: 'ls' }), null), '$ ls');
});
