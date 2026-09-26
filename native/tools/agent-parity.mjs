// native/tools/agent-parity.mjs — generates js-parity.json for the XbinAgent
// Swift package: the web's own outputs (web/agent-tools.js,
// web/agent-slash.js, bx-agent.js _blocks()) over a corpus, for the Swift
// port's parity tests (Tests/XbinAgentTests/ParityTests.swift). Run after
// the captured sessions change or the web modules do:
//
//   node native/tools/agent-parity.mjs . native/ios/Packages/XbinAgent/Tests/XbinAgentTests/Fixtures
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const [repo, fixDir] = process.argv.slice(2);
const tools = await import(pathToFileURL(path.join(repo, 'web/agent-tools.js')));
const slash = await import(pathToFileURL(path.join(repo, 'web/agent-slash.js')));

// _blocks() from bx-agent.js, evaluated with its helpers in scope
const src = fs.readFileSync(path.join(repo, 'web/bx-agent.js'), 'utf8');
const start = src.indexOf('  _blocks() {');
let depth = 0, i = src.indexOf('{', start), end = -1;
for (; i < src.length; i++) { if (src[i] === '{') depth++; else if (src[i] === '}') { depth--; if (depth === 0) { end = i; break; } } }
const body = src.slice(src.indexOf('{', start) + 1, end);
const blocksFn = new Function('newTool', 'foldTool', `return function() {${body}}`)(tools.newTool, tools.foldTool);
function blocksOf(events) {
  const self = { _events: [...events].sort((a, b) => a.seq - b.seq) };
  return blocksFn.call(self);
}
function summarize(b) {
  const o = { kind: b.kind };
  switch (b.kind) {
    case 'msg': Object.assign(o, { role: b.role, text: b.text, mid: b.mid }); if (b.files) o.files = b.files.map((f) => f.name); break;
    case 'thought': Object.assign(o, { text: b.text, done: b.done, ms: (b.t1 || 0) - (b.t0 || 0) }); break;
    case 'tool': Object.assign(o, { id: b.id, title: b.title, tk: b.tk, status: b.status, name: b.name, label: b.label, parent: b.parent,
      subagent: b.subagent, output: b.output, exitCode: b.exitCode, headline: tools.headline(b), command: tools.commandOf(b),
      plan: tools.isPlanApproval(b), files: b.files ? b.files.changes.map((c) => c.path) : null,
      children: b.children ? b.children.map(summarize) : null }); break;
    case 'plan': Object.assign(o, { entries: b.entries.map((e) => e.content + ':' + e.status) }); break;
    case 'perm': Object.assign(o, { pid: b.pid, by: b.by, optionId: b.optionId, plan: tools.isPlanApproval(b.tool) }); break;
    case 'ask': Object.assign(o, { eid: b.eid, action: b.action, by: b.by, fields: tools.formFields(b.schema).map((f) => f.key) }); break;
    case 'changes': Object.assign(o, { turn: b.turn, files: b.changes.map((c) => c.path) }); break;
    case 'turn': Object.assign(o, { turn: b.turn, stopReason: b.stopReason, error: b.error || null }); break;
  }
  return o;
}

const commands = [
  "python3 - <<'EOF'\np='backend/main.go'\ns=open(p).read()\ndef rep(old,new,count=1):\n    global s\n    s=s.replace(old,new)\nopen(p,'w').write(s)\nEOF",
  "cd /w/apps/x && node - <<EOF\nconst fs=require('fs');fs.writeFileSync('a.json','{}')\nEOF",
  "python3 - <<'PY'\nprint(1)\nPY",
  "sed -i 's/a/b/' src/x.go", "cd /w && perl -pi -e 's/x/y/' a.txt", "cat > notes.md <<'EOF'\nhi\nEOF",
  'echo hi | tee out.txt', 'echo hi > made.txt', 'rm -rf build', 'mv a.txt b.txt', 'go test ./...', "sed -n '1,5p' file.go",
  'ls -la 2>/dev/null', 'make build\nmake test', 'echo x > /dev/null', 'ls 2> err.txt', 'rm -f a b', 'mkdir -p x/y', 'mv -f a b',
  'echo hi | tee -a log.txt', "perl -i.bak -pe 's/a/b/' conf.ini", "sed -e 's/x/y/' -i file.txt", 'cat >> "my notes.md" <<EOF\nx\nEOF',
  "bash <<'SH'\nset -e\necho hi\nSH", "deno run - <<EOF\nawait Deno.writeTextFile('out.txt','x')\nEOF",
  "python3 - <<'EOF'\nopen('a.py'); open('b.py'); open('c.py'); Path('d/e.txt')\nEOF",
  "ruby <<-RB\n  File.write('x.rb', 1)\n  RB", 'cd sub && rm old.log', '  git status  ', 'echo "a > b"', "printf 'x' >> 'log file.txt'",
  'make build\r\nmake test', 'echo héllo > ünï.txt', '', 'mv a b c', 'sed -i.bak s/a/b/ f.go && echo done',
  "python3 - <<'EOF'\r\nprint(1)\r\nEOF", "node <<EOF\nconst target = 'dist/bundle.js'\nEOF", "sh -c 'echo hi' > /tmp/out",
];
const describe = commands.map((c) => ({ in: c, out: tools.describeCommand(c) }));

const toolCases = [
  [{ title: 'npm test', kind: 'execute', status: 'pending', name: 'Bash', rawInput: { command: 'npm test' } }, { status: 'in_progress', outputDelta: 'a' }, { outputDelta: 'b' }, { status: 'completed', output: 'whole', exitCode: 1 }],
  [{ kind: 'execute', title: "python3 - <<'EOF'\nopen('x.py')\nEOF", rawInput: { command: "python3 - <<'EOF'\nopen('x.py')\nEOF" } }],
  [{ kind: 'execute', title: "python3 - <<'EOF'\nopen('x.py')\nEOF", rawInput: { command: "python3 - <<'EOF'\nopen('x.py')\nEOF" } }, { label: 'Rewrite the handler' }],
  [{ kind: 'execute', title: 'ls', rawInput: { command: 'ls', description: 'List files' } }],
  [{ kind: 'execute', status: 'pending', title: 'ls -la', content: [{ type: 'content', content: { type: 'text', text: '[in /w] (List the files)' } }] }],
  [{ kind: 'execute', status: 'completed', title: 'ls -la', content: [{ type: 'content', content: { type: 'text', text: '[in /w] (List the files)' } }] }],
  [{ kind: 'read', title: 'Read src/main.go\n(lines 1-40)' }],
  [{ kind: 'execute', rawInput: { command: ['git', 'status', '--short'] } }],
  [{ kind: 'execute', rawInput: { cmd: 'ls', args: ['-l', 'x'] } }],
  [{ kind: 'execute', rawInput: { script: 'echo 1\necho 2' } }],
  [{ kind: 'other', name: 'WebSearch' }],
  [{ kind: 'other' }],
  [{ kind: 'edit', title: 'Edit main.go', content: [{ type: 'diff', path: 'main.go', oldText: 'a\nb\n', newText: 'a\nc\n' }] }],
  [{ kind: 'think', title: 'Explore', subagent: true, rawInput: { description: 'Explore the repo', prompt: 'Find main', subagent_type: 'Explore' } }],
  [{ kind: 'switch_mode', title: 'Ready to code?', rawInput: { plan: '# P' } }],
  [{ kind: 'execute', title: 'x', rawInput: { command: null, cmd: 'fallback' } }],
];
const headlines = toolCases.map((evs, k) => {
  const t = tools.newTool('t' + k);
  for (const d of evs) tools.foldTool(t, d);
  return { events: evs, headline: tools.headline(t), command: tools.commandOf(t), plan: tools.isPlanApproval(t), planText: tools.planText(t), output: t.output, raw: tools.rawText(t.rawInput) };
});

const rawTexts = [null, 'plain', { command: 'ls' }, { cmd: 'a', args: ['b'] }, { path: 'x', n: 1, deep: { a: [1, 2, true, null] } }, [1, 'two'], 42, true, { command: ['a', 'b'] }]
  .map((r) => ({ in: r, out: tools.rawText(r) }));

const other = (q) => ({ type: 'string', title: 'Other', description: 'Type your own', _meta: { _askUserQuestionCustomAnswer: { questionId: q, isCustomAnswer: true } } });
const schemas = [
  { type: 'object', properties: {
    question_0: { type: 'string', title: 'DB', description: 'Which database?', oneOf: [{ const: 'Postgres', title: 'Postgres', description: 'default' }, { const: 'SQLite', title: 'SQLite' }] },
    question_0_custom: other('question_0'),
    question_1: { type: 'array', title: 'Extras', items: { anyOf: [{ const: 'Metrics', title: 'Metrics' }, { const: 'Tracing', title: 'Tracing' }] } },
    question_1_custom: other('question_1'),
    port: { type: 'integer', title: 'Port' }, tls: { type: 'boolean', title: 'TLS' }, name: { type: 'string', title: 'Name' },
    mode: { type: 'string', enum: ['a', 'b'] },
  }, required: ['name'] },
  null,
  { type: 'object', properties: { orphan: other('nope'), n: { type: 'number' }, e: { enum: [1, 2] }, o: { oneOf: [{ title: 'Only title' }, { value: 'v', title: 'V' }] } }, required: ['orphan', 'n'] },
];
const values = [
  { question_0: 'SQLite', question_0_custom: '  ', question_1: ['Metrics'], question_1_custom: ' Admin UI ', port: '8080', tls: false, name: '' },
  {},
  { orphan: 'x', n: '3.5', e: 2, o: 'v' },
  { n: 'abc', orphan: '   ' },
];
const forms = [];
for (const s of schemas) {
  const f = tools.formFields(s);
  for (const v of values) {
    const c = tools.formContent(f, v);
    forms.push({ schema: s, values: v, fields: f, content: c, missing: tools.missingRequired(f, c) });
  }
}

const diffs = [['a\nb\nc', 'a\nc\nd'], ['', 'x'], ['x', ''], ['same', 'same'], ['a\r\nb', 'a\r\nc'], ['a\nb\n', 'a\nb'],
  [Array.from({ length: 700 }, (_, k) => 'l' + k).join('\n'), Array.from({ length: 700 }, (_, k) => 'm' + k).join('\n')],
  ['one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten', 'one\ntwo\nTHREE\nfour\nfive\nsix\nseven\neight\nnine\nTEN\neleven'], [null, 'new file\n']]
  .map(([o, n]) => ({ old: o, new: n, path: 'f.txt', out: tools.unifiedDiff('f.txt', o, n) }));

const ansi = ['\x1b[31mred\x1b[0m ok\x1b[2K', '\x1b]0;title\x07text', '\x1b[1;2:3mx', 'a\x1b(Bb', '\x1b[?25lhidden\x1b[?25h', 'plain', '\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\']
  .map((s) => ({ in: s, out: tools.stripAnsi(s) }));

const cmds = [
  { name: 'review', description: 'Review the pending changes', hint: 'what to focus on' },
  { name: 'compact', description: 'Summarize the conversation' },
  { name: 'init', description: 'Write a CLAUDE.md' },
  { name: 'pr-comments', description: 'Show pull request comments' },
];
const drafts = ['/', '/Rev', '/review ', 'hi /review', '', '/c', '/comm', '/zzz', '/review tests', '/compact ', '/pr-', '/review\n', '/ré'];
const slashOut = drafts.map((d) => {
  const q = slash.slashQuery(d);
  return { draft: d, query: q, menu: q == null ? [] : slash.matchCommands(cmds, q).map((c) => c.name), hint: slash.commandHint(cmds, d) };
});

const transcripts = {};
for (const f of fs.readdirSync(fixDir).filter((f) => f.endsWith('.json') && f !== 'js-parity.json')) {
  const d = JSON.parse(fs.readFileSync(path.join(fixDir, f), 'utf8'));
  if (!d.events) continue;
  transcripts[d.name] = blocksOf(d.events).map(summarize);
}

fs.writeFileSync(path.join(fixDir, 'js-parity.json'), JSON.stringify({ about: 'outputs of the web modules over a corpus (generated by the parity script; native/tools/agent-parity.mjs)', describe, headlines, rawTexts, forms, diffs, ansi, slash: { commands: cmds, cases: slashOut }, transcripts }, null, 1) + '\n');
console.log('ok', Object.keys(transcripts));
