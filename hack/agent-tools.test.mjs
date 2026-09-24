// hack/agent-tools.test.mjs — the Agent tab's tool-call model
// (web/agent-tools.js, D77), run by `make js-test`: how a call's events fold
// into one record, what its headline says (the harness's description, else a
// deterministic reading of the command), plan-approval detection.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { newTool, foldTool, headline, describeCommand, isPlanApproval, planText, stripAnsi, commandOf } from '../web/agent-tools.js';

test('describeCommand: a heredoc script names its language, size and the files it touches', () => {
  const py = "python3 - <<'EOF'\np='backend/main.go'\ns=open(p).read()\ndef rep(old,new,count=1):\n    global s\n    s=s.replace(old,new)\nopen(p,'w').write(s)\nEOF";
  assert.equal(describeCommand(py), 'Python script (6 lines) → backend/main.go');
  assert.equal(describeCommand("cd /w/apps/x && node - <<EOF\nconst fs=require('fs');fs.writeFileSync('a.json','{}')\nEOF"), 'Node script (1 line) → a.json');
  assert.equal(describeCommand("python3 - <<'PY'\nprint(1)\nPY"), 'Python script (1 line)');
});

test('describeCommand: in-place edits and writes name the file', () => {
  assert.equal(describeCommand("sed -i 's/a/b/' src/x.go"), 'Edit src/x.go (sed)');
  assert.equal(describeCommand("cd /w && perl -pi -e 's/x/y/' a.txt"), 'Edit a.txt (perl)');
  assert.equal(describeCommand("cat > notes.md <<'EOF'\nhi\nEOF"), 'Write notes.md');
  assert.equal(describeCommand('echo hi | tee out.txt'), 'Write out.txt');
  assert.equal(describeCommand('echo hi > made.txt'), 'Write made.txt');
  assert.equal(describeCommand('rm -rf build'), 'Remove build');
  assert.equal(describeCommand('mv a.txt b.txt'), 'Move a.txt → b.txt');
});

test('describeCommand: everything else is its first line; a read-only sed or a /dev/null redirect is not a write', () => {
  assert.equal(describeCommand('go test ./...'), 'go test ./...');
  assert.equal(describeCommand("sed -n '1,5p' file.go"), "sed -n '1,5p' file.go");
  assert.equal(describeCommand('ls -la 2>/dev/null'), 'ls -la 2>/dev/null');
  assert.equal(describeCommand('make build\nmake test'), 'make build …');
});

test('foldTool: fields replace, outputDelta appends, output replaces', () => {
  const t = newTool('t1');
  foldTool(t, { title: 'npm test', kind: 'execute', status: 'pending', name: 'Bash', rawInput: { command: 'npm test' } });
  foldTool(t, { status: 'in_progress', outputDelta: 'a' });
  foldTool(t, { outputDelta: 'b' });
  assert.equal(t.output, 'ab');
  foldTool(t, { status: 'completed', output: 'whole', exitCode: 1 });
  assert.deepEqual([t.output, t.exitCode, t.status, t.name, t.tk], ['whole', 1, 'completed', 'Bash', 'execute']);
  assert.equal(commandOf(t), 'npm test');
});

test('headline: the harness description first, then gemini-style content, then the command reading', () => {
  const t = foldTool(newTool('t'), { kind: 'execute', title: "python3 - <<'EOF'\nopen('x.py')\nEOF", rawInput: { command: "python3 - <<'EOF'\nopen('x.py')\nEOF" } });
  assert.equal(headline(t), 'Python script (1 line) → x.py');
  foldTool(t, { label: 'Rewrite the handler' });
  assert.equal(headline(t), 'Rewrite the handler');
  const d = foldTool(newTool('d'), { kind: 'execute', title: 'ls', rawInput: { command: 'ls', description: 'List files' } });
  assert.equal(headline(d), 'List files');
  const g = foldTool(newTool('g'), { kind: 'execute', status: 'pending', title: 'ls -la', content: [{ type: 'content', content: { type: 'text', text: '[in /w] (List the files)' } }] });
  assert.equal(headline(g), '[in /w] (List the files)');
  const r = foldTool(newTool('r'), { kind: 'read', title: 'Read src/main.go\n(lines 1-40)' });
  assert.equal(headline(r), 'Read src/main.go …');
});

test('plan approval: claude (switch_mode + content), codex (planReview, rawInput only), a bare rawInput.plan', () => {
  const claude = { kind: 'switch_mode', title: 'Approve Plan', rawInput: { plan: '# P', planFilePath: '/p.md' }, content: [{ type: 'content', content: { type: 'text', text: '# P\n\n1. x' } }] };
  assert.ok(isPlanApproval(claude));
  assert.equal(planText(claude), '# P\n\n1. x');
  const codex = { kind: 'switch_mode', rawInput: { plan: '## Codex plan' } };
  assert.equal(planText(codex), '## Codex plan');
  assert.ok(isPlanApproval({ kind: 'other', rawInput: { plan: 'x' } }));
  assert.ok(isPlanApproval(foldTool(newTool('c'), { planReview: true })));
  assert.ok(!isPlanApproval({ kind: 'execute', rawInput: { command: 'ls' } }));
});

test('stripAnsi removes colour and cursor escapes', () => {
  assert.equal(stripAnsi('\x1b[31mred\x1b[0m ok\x1b[2K'), 'red ok');
});

test('formFields/formContent: Claude\'s AskUserQuestion form — radios, checks, each with its Other box', async () => {
  const { formFields, formContent, missingRequired } = await import('../web/agent-tools.js');
  const other = (q) => ({ type: 'string', title: 'Other', description: 'Type your own', _meta: { _askUserQuestionCustomAnswer: { questionId: q, isCustomAnswer: true } } });
  const schema = { type: 'object', properties: {
    question_0: { type: 'string', title: 'DB', description: 'Which database?', oneOf: [{ const: 'Postgres', title: 'Postgres', description: 'default' }, { const: 'SQLite', title: 'SQLite' }] },
    question_0_custom: other('question_0'),
    question_1: { type: 'array', title: 'Extras', items: { anyOf: [{ const: 'Metrics', title: 'Metrics' }, { const: 'Tracing', title: 'Tracing' }] } },
    question_1_custom: other('question_1'),
    port: { type: 'integer', title: 'Port' }, tls: { type: 'boolean', title: 'TLS' }, name: { type: 'string', title: 'Name' },
    mode: { type: 'string', enum: ['a', 'b'] },
  }, required: ['name'] };
  const f = formFields(schema);
  assert.deepEqual(f.map((x) => [x.key, x.kind, x.other]), [['question_0', 'radio', 'question_0_custom'], ['question_1', 'check', 'question_1_custom'],
    ['port', 'number', null], ['tls', 'bool', null], ['name', 'text', null], ['mode', 'radio', null]]);
  assert.deepEqual(f[0].options[0], { value: 'Postgres', title: 'Postgres', description: 'default' });
  assert.deepEqual(f[5].options.map((o) => o.value), ['a', 'b']);
  const c = formContent(f, { question_0: 'SQLite', question_0_custom: '  ', question_1: ['Metrics'], question_1_custom: ' Admin UI ', port: '8080', tls: false, name: '' });
  assert.deepEqual(c, { question_0: 'SQLite', question_1: ['Metrics'], question_1_custom: 'Admin UI', port: 8080, tls: false });
  assert.deepEqual(missingRequired(f, c), ['Name']);
  assert.deepEqual(formFields(null), []);
});
