// harness-fixtures.mjs — coding-harness conversations in exactly the
// shapes of D-harness §4, as a seed for backend.mjs's STUB (the
// web tests: ctx.addInitScript(STUB, harnessSeed())) and for the native
// view's tests (native-stub.mjs: data.seed) — plain data, a fresh copy per
// call. What it holds:
//
//   21  "Fix the flaky test" — Claude Code, idle: one card of each ACP kind
//       (read, edit with a diff, delete, move, search, execute exit 0 and 1,
//       fetch, switch_mode, other) and a Task subagent (think) with its own
//       text and calls nested under it (acp.parent); plan, usage, counts
//   22  "Add retries to the client" — Claude Code parked on a permission (execute)
//   23  "Pick a JSON library" — Claude Code parked on a question (a form)
//   24  "Port the CLI" — Codex waiting for a sign-in (login: terminal, api-key, device code)
//   25  "Refactor the API" — the built-in agent with three Claude Code / Codex
//       children: 26 working, 27 parked on a plan approval, 28 done — and a
//       direct message to #26 told to it (the notice), its task pinned (D133);
//       seed.trees[25] its tree (nodes as GET /runs/{id}/tree has them)
//       — kidsSeed() has them as a child card draws them: 26 working through
//       its plan, 27 parked on a permission, 28 signed out
//
// Also: the catalog (claude, codex available; gemini no-egress; opencode
// no-image), the caller's modes (claude: auto), sandboxes, needs (login,
// approval, question, a child's approval), a stderr log for run 22, and the
// classes with `harness` (the built-in coding class of §4.3.11).
export const NOW = Date.UTC(2026, 8, 30, 12);
const now = Math.floor(NOW / 1000);
export const SBX = 'apps/coding-sandbox';
export const API_DEV = `${SBX}|sb-7f3a`;

const acall = (id, kind, args) => ({ id, type: 'function', function: { name: 'acp:' + kind, arguments: JSON.stringify(args) } });
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });

// transcript: a harness run's messages from a list of steps — text (an
// assistant row), a call (one assistant row + its tool row, §4.3.5).
function transcript(runId, steps) {
  const out = [];
  let seq = 0;
  const at = (n) => now - 900 + n * 7;
  for (const s of steps) {
    seq++;
    if (s.user) { out.push({ id: seq, runId, seq, role: 'user', content: s.user, created: at(seq), sender: s.sender || 'admin' }); continue; }
    if (s.text != null && !s.call) {
      out.push({ id: seq, runId, seq, role: 'assistant', content: s.text, created: at(seq), ...(s.reasoning ? { reasoning: s.reasoning, reasoningMs: 2100 } : {}),
        ...(s.parent ? { acp: { parent: s.parent } } : {}) });
      continue;
    }
    const [id, kind, args, content, acp] = s.call;
    out.push({ id: seq, runId, seq, role: 'assistant', content: s.text || '', created: at(seq), toolCalls: [acall(id, kind, args)],
      ...(s.reasoning ? { reasoning: s.reasoning, reasoningMs: 1800 } : {}), ...(s.parent ? { acp: { parent: s.parent } } : {}) });
    seq++;
    out.push({ id: seq, runId, seq, role: 'tool', toolCallId: id, name: 'acp:' + kind, content, created: at(seq),
      acp: { kind, status: 'completed', ...(s.parent ? { parent: s.parent } : {}), ...acp } });
  }
  return out;
}

const MODES = [
  { id: 'default', name: 'Ask before acting', description: 'Asks before edits and commands' },
  { id: 'acceptEdits', name: 'Accept edits', description: 'Edits files without asking' },
  { id: 'plan', name: 'Plan', description: 'Plans, changes nothing' },
  { id: 'bypassPermissions', name: 'Bypass permissions', description: 'Never asks', explicit: true },
];
const CODEX_MODES = [
  { id: 'read-only', name: 'Read only' }, { id: 'agent', name: 'Agent' }, { id: 'full-access', name: 'Full access', explicit: true },
];
const MODEL = { id: 'model', name: 'Model', category: 'model', description: 'The model Claude Code uses', currentValue: 'default',
  options: [{ value: 'default', name: 'Default (Opus)', description: 'The account\'s default' }, { value: 'sonnet', name: 'Sonnet' }, { value: 'haiku', name: 'Haiku' }] };
const EFFORT = { id: 'effort', name: 'Reasoning effort', category: 'thought_level', currentValue: 'medium',
  options: [{ value: 'low', name: 'Low' }, { value: 'medium', name: 'Medium' }, { value: 'high', name: 'High' }] };
const COMMANDS = [{ name: 'review', description: 'Review the pending changes', hint: 'what to focus on' }, { name: 'compact', description: 'Compact the conversation' }];
const SANDBOX = { ref: API_DEV, name: 'api-dev', cwd: '/work/api', shared: true };
// the conversation's binding of it (config.sandbox): no `shared` — that is the summary's
const BINDING = { ref: API_DEV, name: 'api-dev', cwd: '/work/api', manager: 'Coding sandboxes' };

// summary: a harness run's `harness` (§4.3.2).
function summary(provider, state, extra = {}) {
  const claude = provider === 'claude';
  return {
    provider, name: claude ? 'Claude Code' : 'Codex', state, error: '',
    mode: { current: claude ? 'acceptEdits' : 'agent', available: claude ? MODES : CODEX_MODES },
    options: claude ? [MODEL, EFFORT] : [], commands: claude ? COMMANDS : [],
    counts: { tools: 0, files: 0, add: 0, del: 0 }, sandbox: SANDBOX, steering: claude, title: '', gen: 1, ...extra,
  };
}

const DIFF = '--- a/work/api/client.go\n+++ b/work/api/client.go\n@@ -10,7 +10,9 @@ func (c *Client) Do(req *Request) (*Response, error) {\n' +
  '-\tresp, err := c.http.Do(req)\n+\tvar resp *Response\n+\terr := retry(3, func() (err error) {\n+\t\tresp, err = c.http.Do(req)\n+\t\treturn err\n+\t})\n \tif err != nil {\n';

export function harnessSeed() {
  const cards = transcript(21, [
    { user: 'The TestClientRetry test is flaky — find out why and fix it.' },
    { text: 'I\'ll look at the test first.', reasoning: 'Read the test, then the client.',
      call: ['h1:toolu_01', 'read', { file_path: '/work/api/client_test.go', summary: 'Read client_test.go' },
        '1\tpackage api\n2\n3\tfunc TestClientRetry(t *testing.T) {\n4\t\tsrv := flaky(2)\n5\t}',
        { title: 'Read /work/api/client_test.go', tool: 'Read', locations: [{ path: '/work/api/client_test.go', line: 1 }], files: ['/work/api/client_test.go'] }] },
    { call: ['h1:toolu_02', 'search', { pattern: 'retry\\(', path: '/work/api', summary: 'Search for retry(' },
      'client.go:12: err := retry(3, func() (err error) {\nclient_test.go:4: srv := flaky(2)',
      { title: 'grep "retry\\(" /work/api', tool: 'Grep', locations: [{ path: '/work/api/client.go', line: 12 }, { path: '/work/api/client_test.go', line: 4 }] }] },
    { call: ['h1:toolu_03', 'think', { description: 'Find every caller of Do', prompt: 'List the callers of Client.Do', subagent_type: 'Explore', summary: 'Find every caller of Do' },
      'Do is called from sync.go and fetch.go.', { title: 'Task', tool: 'Task', subagent: true }] },
    { text: 'Looking for callers.', parent: 'h1:toolu_03' },
    { parent: 'h1:toolu_03', call: ['h1:toolu_04', 'search', { pattern: '\\.Do\\(', path: '/work/api', summary: 'Search for .Do(' },
      'sync.go:40: c.Do(req)\nfetch.go:22: c.Do(req)', { title: 'grep "\\.Do\\(" /work/api', tool: 'Grep', locations: [{ path: '/work/api/sync.go', line: 40 }, { path: '/work/api/fetch.go', line: 22 }] }] },
    { parent: 'h1:toolu_03', call: ['h1:toolu_05', 'read', { file_path: '/work/api/sync.go', offset: 30, limit: 20, summary: 'Read sync.go' },
      '30\tfunc sync() {\n40\t\tc.Do(req)\n49\t}', { title: 'Read /work/api/sync.go', tool: 'Read', files: ['/work/api/sync.go'] }] },
    { call: ['h1:toolu_06', 'edit', { file_path: '/work/api/client.go', old_string: 'resp, err := c.http.Do(req)', new_string: '…', summary: 'Retry the request' },
      'edited /work/api/client.go (+5 −1)',
      { title: 'Edit /work/api/client.go', tool: 'Edit', files: ['/work/api/client.go'], locations: [{ path: '/work/api/client.go', line: 10 }],
        diffs: [{ path: '/work/api/client.go', status: 'modified', add: 5, del: 1, patch: DIFF, truncated: false }] }] },
    { call: ['h1:toolu_07', 'delete', { file_path: '/work/api/old_retry.go', summary: 'Delete old_retry.go' }, 'deleted /work/api/old_retry.go',
      { title: 'Delete /work/api/old_retry.go', files: ['/work/api/old_retry.go'], diffs: [{ path: '/work/api/old_retry.go', status: 'deleted', add: 0, del: 14, patch: '', truncated: false }] }] },
    { call: ['h1:toolu_08', 'move', { source: '/work/api/util.go', destination: '/work/api/internal/util.go', summary: 'Move util.go into internal/' },
      'moved /work/api/util.go → /work/api/internal/util.go', { title: 'Move util.go', files: ['/work/api/util.go', '/work/api/internal/util.go'] }] },
    { call: ['h1:toolu_09', 'fetch', { url: 'https://pkg.go.dev/net/http#Client.Do', summary: 'Read the net/http docs' }, 'Do sends an HTTP request and returns an HTTP response…',
      { title: 'Fetch https://pkg.go.dev/net/http#Client.Do', tool: 'WebFetch' }] },
    { call: ['h1:toolu_10', 'execute', { command: 'go test ./... -run TestClientRetry -count=20', description: 'Run the flaky test 20 times', summary: 'Run the flaky test 20 times' },
      '--- FAIL: TestClientRetry (0.01s)\n    client_test.go:9: got 503\nFAIL\n[exit 1]',
      { title: 'go test ./... -run TestClientRetry -count=20', label: 'Run the flaky test 20 times', tool: 'Bash', exitCode: 1,
        output: '\x1b[31m--- FAIL: TestClientRetry (0.01s)\x1b[0m\n    client_test.go:9: got 503\nFAIL\n', outputTruncated: 0 }] },
    { call: ['h1:toolu_11', 'execute', { command: 'go test ./...', description: 'Run all the tests', summary: 'Run all the tests' },
      'ok  \texample.com/api\t0.412s\n[exit 0]',
      { title: 'go test ./...', label: 'Run all the tests', tool: 'Bash', exitCode: 0, output: 'ok  \texample.com/api\t0.412s\n', outputTruncated: 0 }] },
    { call: ['h1:toolu_12', 'switch_mode', { mode: 'acceptEdits', summary: 'Switch to Accept edits' }, 'switched to acceptEdits', { title: 'Switch mode' }] },
    { call: ['h1:toolu_13', 'other', { summary: 'TodoWrite' }, 'todos updated', { title: 'TodoWrite', tool: 'TodoWrite' }] },
    { text: 'Fixed: the client now retries a failed request up to three times, and the test passes 20 runs in a row.' },
  ]);
  const h21 = summary('claude', 'ready', {
    title: 'Fix the flaky retry test',
    usage: { used: 52000, size: 200000, cost: { amount: 0.41, currency: 'USD' } },
    plan: { entries: [{ content: 'Find why the test fails', status: 'completed', priority: 'high' }, { content: 'Add retries to the client', status: 'completed', priority: 'high' },
      { content: 'Run the tests 20 times', status: 'completed', priority: 'medium' }] },
    counts: { tools: 13, files: 4, add: 5, del: 15 }, activity: { kind: 'idle', at: NOW - 60000 },
  });

  // 22: a permission park — the call's tool row is its placeholder
  const park22 = 'Xq3perm';
  const h22 = summary('claude', 'working', { activity: { kind: 'waiting', at: NOW - 30000 }, counts: { tools: 2, files: 0, add: 0, del: 0 },
    pending: { park: park22, kind: 'approval', title: 'go test ./...' }, gen: 2 });
  const ps22 = { kind: 'approval', park: park22,
    toolCalls: [acall('h2:toolu_02', 'execute', { command: 'go test ./...', description: 'Run the tests', summary: 'Run the tests' })],
    harness: {
      callId: 'h2:toolu_02',
      options: [{ optionId: 'allow', name: 'Allow', kind: 'allow_once' }, { optionId: 'allow_always', name: 'Always allow', kind: 'allow_always' },
        { optionId: 'reject', name: 'Reject', kind: 'reject_once' }],
      tool: { title: 'go test ./...', kind: 'execute', name: 'Bash', label: 'Run the tests', command: 'go test ./...',
        rawInput: { command: 'go test ./...', description: 'Run the tests' }, content: [] },
      rule: { kind: 'execute', title: 'go test ./...' }, defaultToNo: false, description: '', planApproval: false, plan: '', pid: 'p3', rpcId: 'h12.2-17' } };
  const msgs22 = transcript(22, [
    { user: 'Add retries with backoff to the HTTP client.' },
    { call: ['h2:toolu_01', 'read', { file_path: '/work/api/client.go', summary: 'Read client.go' }, '1\tpackage api', { title: 'Read /work/api/client.go' }] },
  ]);
  msgs22.push({ id: 4, runId: 22, seq: 4, role: 'assistant', content: 'Now the tests.', created: now - 300, toolCalls: ps22.toolCalls },
    { id: 5, runId: 22, seq: 5, role: 'tool', toolCallId: 'h2:toolu_02', name: 'acp:execute', content: '(awaiting your approval)', created: now - 299,
      acp: { kind: 'execute', title: 'go test ./...', label: 'Run the tests', tool: 'Bash', status: 'pending' } });

  // 23: a question park (form elicitation, claude's "Other" field included)
  const park23 = 'Xq3ask';
  const q23 = 'Which JSON library should I use?';
  const ps23 = { kind: 'question', park: park23, harness: { eid: 'e1', message: q23, schema: {
    type: 'object', required: ['library'],
    properties: {
      library: { type: 'string', title: 'Library', enum: ['encoding/json', 'jsoniter', 'go-json'], enumNames: ['encoding/json (stdlib)', 'jsoniter', 'go-json'] },
      _askUserQuestionCustomAnswer: { type: 'string', title: 'Other' },
    } } } };
  const h23 = summary('claude', 'working', { activity: { kind: 'waiting', at: NOW - 20000 }, pending: { park: park23, kind: 'question', title: q23 } });

  // 24: Codex needs a sign-in
  const park24 = 'Xq3login';
  const login = { command: 'codex login', methods: [{ id: 'chatgpt', name: 'Sign in with ChatGPT', kind: 'terminal' },
    { id: 'openai-api-key', name: 'OpenAI API key', kind: 'api-key' }, { id: 'device-code', name: 'Sign in with a device code', kind: 'device-code' }] };
  const h24 = summary('codex', 'login', { login, pending: { park: park24, kind: 'login', title: 'Sign in to Codex' }, gen: 1 });
  const ps24 = { kind: 'login', park: park24, harness: { login } };

  // 25–28: the built-in agent and its three coding agents
  const spawn = (id, provider, task, label) => call(id, 'subagent_spawn', { task, label, harness: provider, summary: label });
  const h26 = summary('claude', 'working', { activity: { kind: 'tool', title: 'Run go vet ./...', at: NOW - 5000 }, counts: { tools: 7, files: 2, add: 31, del: 4 },
    plan: { entries: [{ content: 'Read the handlers', status: 'completed' }, { content: 'Split the router', status: 'in_progress' }, { content: 'Run the tests', status: 'pending' }] },
    sandbox: { ...SANDBOX, cwd: '/work/api' } });
  const park27 = 'Xq3plan';
  const plan27 = '## Plan\n\n1. Add a `migrations/0007_users.sql`\n2. Backfill in batches of 1000\n3. Drop the old column';
  const ps27 = { kind: 'approval', park: park27,
    toolCalls: [acall('h1:toolu_09', 'switch_mode', { plan: plan27, summary: 'Ready to code?' })],
    harness: { callId: 'h1:toolu_09',
      options: [{ optionId: 'acceptEdits', name: 'Yes, and auto-accept edits', kind: 'allow_always' }, { optionId: 'default', name: 'Yes, and manually approve edits', kind: 'allow_once' },
        { optionId: 'bypassPermissions', name: 'Yes, and bypass permissions', kind: 'allow_always', explicit: true }, { optionId: 'plan', name: 'No, keep planning', kind: 'reject_once' }],
      tool: { title: 'Ready to code?', kind: 'switch_mode', name: 'ExitPlanMode', label: '', rawInput: { plan: plan27 }, content: [] },
      defaultToNo: false, description: '', planApproval: true, plan: plan27, pid: 'p9', rpcId: 'h27.1-9' } };
  const h27 = summary('codex', 'working', { mode: { current: 'read-only', available: CODEX_MODES }, activity: { kind: 'waiting', at: NOW - 40000 },
    pending: { park: park27, kind: 'approval', title: 'Ready to code?' }, counts: { tools: 4, files: 0, add: 0, del: 0 },
    sandbox: { ...SANDBOX, cwd: '/work/migrations' } });
  const h28 = summary('claude', 'ready', { counts: { tools: 3, files: 1, add: 12, del: 0 }, usage: { used: 9000, size: 200000 }, sandbox: { ...SANDBOX, cwd: '/work/docs' } });
  const child = (id, title, status, harness, extra = {}) => ({ id, title, status, parentId: 25, rootId: 25, engine: 'harness', harness, created: now - 1216 + id, ...extra });
  const c26 = child(26, 'Split the router', 'running', h26);
  const c27 = child(27, 'Plan the users migration', 'waiting_input', h27, { pendingState: ps27 });
  const c28 = child(28, 'Write the changelog', 'idle', h28, { result: 'Added the 2026-09-30 entry.' });
  const link = (i, c, callId, label, state, result = '') => ({ id: i, parentId: 25, childId: c.id, toolCallId: callId, mode: 'bg', state, label, phase: '', result, child: c });
  const msgs25 = [
    { id: 1, runId: 25, seq: 1, role: 'user', content: 'Refactor the API: split the router, plan the users migration, update the changelog.', created: now - 1200, sender: 'admin' },
    { id: 2, runId: 25, seq: 2, role: 'assistant', content: 'Starting three coding agents.', created: now - 1190,
      toolCalls: [spawn('s1', 'claude', 'Split the router into one file per resource', 'Split the router'),
        spawn('s2', 'codex', 'Plan the users table migration', 'Plan the users migration'),
        spawn('s3', 'claude', 'Add the changelog entry', 'Write the changelog')] },
    { id: 3, runId: 25, seq: 3, role: 'tool', toolCallId: 's1', name: 'subagent_spawn', content: 'started #26 (background)', created: now - 1189 },
    { id: 4, runId: 25, seq: 4, role: 'tool', toolCallId: 's2', name: 'subagent_spawn', content: 'started #27 (background)', created: now - 1189 },
    { id: 5, runId: 25, seq: 5, role: 'tool', toolCallId: 's3', name: 'subagent_spawn', content: 'started #28 (background)', created: now - 1189 },
    { id: 6, runId: 25, seq: 6, role: 'user', content: '[direct message to #26 (Claude Code) from admin]\nKeep the old routes as aliases.', created: now - 600 },
  ];
  const childView = (c, task) => ({ access: 'owner', run: c, chain: [{ id: 25, title: 'Refactor the API' }], config: { sandbox: BINDING },
    messages: [{ id: 1, runId: c.id, seq: 1, role: 'user', content: task, created: now - 1180, sender: '' }] });

  const runs = [
    { id: 21, title: 'Fix the flaky test', status: 'idle', parentId: 0, rootId: 21, engine: 'harness', harness: h21, activityMs: NOW - 60000, readMs: NOW },
    { id: 22, title: 'Add retries to the client', status: 'waiting_input', parentId: 0, rootId: 22, engine: 'harness', harness: h22, pendingState: ps22, activityMs: NOW - 30000 },
    { id: 23, title: 'Pick a JSON library', status: 'waiting_input', parentId: 0, rootId: 23, engine: 'harness', harness: h23, pendingState: ps23, result: q23, activityMs: NOW - 20000 },
    { id: 24, title: 'Port the CLI', status: 'waiting_input', parentId: 0, rootId: 24, engine: 'harness', harness: h24, pendingState: ps24, activityMs: NOW - 10000 },
    { id: 25, title: 'Refactor the API', status: 'awaiting', parentId: 0, rootId: 25, engine: '', activityMs: NOW - 5000, created: now - 1200,
      task: { count: 1, first: { id: 1, seq: 1, source: 'human', who: 'admin', text: msgs25[0].content, at: now - 1200, live: true } } },
    c26, c27, c28,
  ];
  const cfg = (provider, mode) => ({ sandbox: BINDING, engine: 'harness', harness: { provider, mode, ref: API_DEV, cwd: '/work/api', by: 'admin' } });
  const views = {
    21: { access: 'owner', run: runs[0], messages: cards, config: cfg('claude', 'acceptEdits'), class: CODING,
      harnessSession: { gen: 1, execId: 'e-21', acpSessionId: 'sess-21', loadable: true, steering: true, startedAt: NOW - 900000, lastActive: NOW - 60000 },
      harnessRules: [{ kind: 'read', title: 'Read /work/api' }] },
    22: { access: 'owner', run: runs[1], messages: msgs22, config: cfg('claude', 'default'), class: CODING },
    23: { access: 'owner', run: runs[2], messages: transcript(23, [{ user: 'Parse the config as JSON — pick a library.' }]), config: cfg('claude', 'default'), class: CODING },
    24: { access: 'owner', run: runs[3], messages: transcript(24, [{ user: 'Port the CLI to Go.' }]), config: cfg('codex', 'agent'), class: CODING },
    25: { access: 'owner', run: runs[4], messages: msgs25, class: CODING,
      links: [link(1, c26, 's1', 'Split the router', 'running'), link(2, c27, 's2', 'Plan the users migration', 'running'),
        link(3, c28, 's3', 'Write the changelog', 'answered', '--- #28 Write the changelog (answered) ---\nAdded the 2026-09-30 entry.')] },
    26: childView(c26, 'Split the router into one file per resource'),
    27: childView(c27, 'Plan the users table migration'),
    28: childView(c28, 'Add the changelog entry'),
  };
  const node = treeNode;
  return structuredClone({
    me: { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 },
    runs, views,
    classes: { classes: [CODING], default: 'internal' },
    harnesses: CATALOG(),
    harnessModes: { claude: 'auto' },
    harnessLogs: { 22: 'claude-agent-acp 0.81.1 starting\nsession sess-22 ready\n' },
    sandboxes: [
      { ref: API_DEV, provider: SBX, manager: 'Coding sandboxes', id: 'sb-7f3a', name: 'api-dev', state: 'running', egress: 'internet', visibility: 'team',
        image: { id: 'base' }, owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work', boundTo: [21, 22, 23, 24, 26, 27, 28] },
      { ref: `${SBX}|sb-9c1d`, provider: SBX, manager: 'Coding sandboxes', id: 'sb-9c1d', name: 'scratch', state: 'running', egress: 'none', visibility: 'private',
        image: { id: 'base' }, owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work' },
      { ref: `${SBX}|sb-2b8e`, provider: SBX, manager: 'Coding sandboxes', id: 'sb-2b8e', name: 'go-dev', state: 'stopped', egress: 'internet', visibility: 'private',
        image: { id: 'go' }, owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work' },
    ],
    // each item names the coding agent that waits (§4.3.9: the waiting run's compact summary)
    needs: [
      { reason: 'login', run: { id: 24, title: 'Port the CLI', status: 'waiting_input', engine: 'harness', harness: h24 }, subRun: 24, harness: compact(h24) },
      { reason: 'approval', run: { id: 22, title: 'Add retries to the client', status: 'waiting_input', engine: 'harness', harness: h22 }, subRun: 22, harness: compact(h22) },
      { reason: 'question', run: { id: 23, title: 'Pick a JSON library', status: 'waiting_input', engine: 'harness', harness: h23 }, subRun: 23, harness: compact(h23) },
      { reason: 'approval', run: { id: 25, title: 'Refactor the API', status: 'awaiting', engine: '' }, subRun: 27, harness: compact(h27) },
    ],
    trees: { 25: { root: 25, nodes: [node(runs[4], 0), node(c26, 1), node(c27, 1), node(c28, 1)], totals: {} } },
  });
}

// compact: a summary as /tree nodes and /needs items carry it (§4.3.6) —
// without options, commands, mode.available and login.methods.
function compact(harness) {
  const h = { ...harness, mode: { current: harness.mode.current } };
  delete h.options; delete h.commands;
  if (h.login) { h.login = { ...h.login }; delete h.login.methods; }
  return h;
}

// treeNode: a run as GET /runs/{id}/tree's node — its status a word (the
// run's is rawStatus), its harness compact.
const WORD = { running: 'running', queued: 'running', awaiting: 'blocked', blocked: 'blocked', waiting_input: 'blocked', sleeping: 'sleeping',
  error: 'error', canceled: 'cancelled', done: 'done' };
function treeNode(r, depth) {
  const out = { id: r.id, parentId: r.parentId || 0, depth, created: r.created || r.id, title: r.title, status: WORD[r.status] || 'done', rawStatus: r.status,
    engine: r.engine || '' };
  if (r.engine !== 'harness') return out;
  return { ...out, harness: compact(r.harness) };
}

// kidsSeed: harnessSeed() with 25's three coding agents in the states a
// child card draws (D-harness §8 U6): 26 working through its plan (its own
// transcript: five blocks, the last a command still running), 27 Codex
// parked on a permission (a command, park Xq3kid), 28 Claude Code signed out
// (a login park, Xq3kidlogin; its link still open) — the links started 20 min
// ago, the tree to match.
export function kidsSeed() {
  const s = harnessSeed();
  const run = (id) => s.runs.find((r) => r.id === id); // one object with its link's child and its view's run
  const at = now - 1190;
  s.views[26].messages = transcript(26, [
    { user: 'Split the router into one file per resource' },
    { call: ['h1:k01', 'read', { file_path: '/work/api/router.go', summary: 'Read router.go' }, '1\tpackage api\n2\n3\tfunc routes() {', { title: 'Read /work/api/router.go', tool: 'Read' }] },
    { call: ['h1:k02', 'edit', { file_path: '/work/api/users.go', summary: 'Create users.go' }, 'edited /work/api/users.go (+24 −0)',
      { title: 'Write /work/api/users.go', tool: 'Write', diffs: [{ path: '/work/api/users.go', status: 'added', add: 24, del: 0, patch: '', truncated: false }] }] },
    { call: ['h1:k03', 'edit', { file_path: '/work/api/router.go', summary: 'Mount users.go' }, 'edited /work/api/router.go (+7 −4)',
      { title: 'Edit /work/api/router.go', tool: 'Edit', diffs: [{ path: '/work/api/router.go', status: 'modified', add: 7, del: 4, patch: '', truncated: false }] }] },
    { text: 'The users routes live in users.go now; checking the package with go vet.' },
    { call: ['h1:k04', 'execute', { command: 'go vet ./...', summary: 'Run go vet ./...' }, '(running…)',
      { title: 'go vet ./...', label: 'Run go vet ./...', tool: 'Bash', status: 'in_progress', output: '' }] },
  ]);

  const c27 = run(27);
  const call27 = acall('h1:k21', 'execute', { command: 'psql -f migrations/0007_users.sql', summary: 'Apply the migration' });
  const ps27 = { kind: 'approval', park: 'Xq3kid', toolCalls: [call27], harness: {
    callId: 'h1:k21',
    options: [{ optionId: 'approved', name: 'Yes', kind: 'allow_once' }, { optionId: 'approved-for-session', name: 'Yes, and don\'t ask again', kind: 'allow_always' },
      { optionId: 'abort', name: 'No, tell Codex what to do', kind: 'reject_once' }],
    tool: { title: 'psql -f migrations/0007_users.sql', kind: 'execute', name: 'shell', label: 'Apply the migration', command: 'psql -f migrations/0007_users.sql',
      rawInput: { command: ['psql', '-f', 'migrations/0007_users.sql'] }, content: [] },
    rule: { kind: 'execute', title: 'psql -f migrations/0007_users.sql' }, defaultToNo: false, description: '', planApproval: false, plan: '', pid: 'p4', rpcId: 'h27.1-4' } };
  Object.assign(c27, { status: 'waiting_input', pendingState: ps27 });
  c27.harness = { ...c27.harness, mode: { current: 'agent', available: c27.harness.mode.available }, activity: { kind: 'waiting', at: NOW - 40000 },
    pending: { park: 'Xq3kid', kind: 'approval', title: 'psql -f migrations/0007_users.sql' } };
  s.views[27].messages.push({ id: 2, runId: 27, seq: 2, role: 'assistant', content: 'The migration is written; applying it.', created: now - 60, toolCalls: [call27] },
    { id: 3, runId: 27, seq: 3, role: 'tool', toolCallId: 'h1:k21', name: 'acp:execute', content: '(awaiting your approval)', created: now - 59,
      acp: { kind: 'execute', title: 'psql -f migrations/0007_users.sql', label: 'Apply the migration', tool: 'shell', status: 'pending' } });

  const c28 = run(28);
  const login = { command: 'CLAUDE_CODE_REMOTE=1 claude /login', methods: [{ id: 'claude-login', name: 'Log in with Claude', kind: 'terminal' },
    { id: 'anthropic-api-key', name: 'Anthropic API key', kind: 'api-key' }] };
  Object.assign(c28, { status: 'waiting_input', result: '', pendingState: { kind: 'login', park: 'Xq3kidlogin', harness: { login } } });
  c28.harness = { ...c28.harness, state: 'login', login, counts: { tools: 0, files: 0, add: 0, del: 0 },
    pending: { park: 'Xq3kidlogin', kind: 'login', title: 'Sign in to Claude Code' } };
  delete c28.harness.usage;

  const links = s.views[25].links;
  for (const l of links) l.created = at;
  Object.assign(links[2], { state: 'running', result: '' });
  s.trees[25].nodes = [treeNode(s.runs.find((r) => r.id === 25), 0), ...[26, 27, 28].map((id) => treeNode(run(id), 1))];
  return s;
}

// The built-in coding class as §4.3.11 has it: the harness toolset, every harness.
const CODING = { id: 'coding', name: 'Coding', icon: '▣', description: 'Works in a coding sandbox, with the web — no internal systems.',
  toolsets: ['sandbox', 'web', 'files', 'subagents', 'skills', 'harness'], mcp: [], managers: 'all', sandboxEgress: ['none', 'internet'], harnesses: 'all' };

// CATALOG: GET /harnesses' entries (§4.3.10; the stub adds each `setting`).
function CATALOG() {
  const img = (image) => ({ provider: SBX, manager: 'Coding sandboxes', image, advertised: true, egress: ['internet', 'open'] });
  return [
    { id: 'claude', name: 'Claude Code', available: true, reason: '', why: '', classes: ['coding'], images: [img('base')], modes: MODES.map(({ description, ...m }) => m),
      defaultMode: 'default', autoMode: 'acceptEdits', approveMode: 'default', planMode: 'plan', login: { command: 'CLAUDE_CODE_REMOTE=1 claude /login' },
      options: [MODEL, EFFORT], sandboxes: { [API_DEV]: { installed: true, signedIn: true, at: NOW - 3600000 } } },
    { id: 'codex', name: 'Codex', available: true, reason: '', why: '', classes: ['coding'], images: [img('base')], modes: CODEX_MODES,
      defaultMode: 'read-only', autoMode: 'agent', approveMode: 'read-only', planMode: 'read-only', login: { command: 'codex login' },
      sandboxes: { [API_DEV]: { installed: true, signedIn: false, at: NOW - 600000 } } },
    { id: 'gemini', name: 'Gemini CLI', available: false, reason: 'no-egress', why: 'needs internet access — Coding sandboxes offers none (bind its internet class)',
      classes: ['coding'], images: [{ ...img('base'), egress: [] }], modes: [{ id: 'default', name: 'Default' }, { id: 'autoEdit', name: 'Auto edit' }, { id: 'yolo', name: 'YOLO', explicit: true }],
      defaultMode: 'default', autoMode: 'autoEdit', approveMode: 'default', planMode: 'plan', login: { command: 'gemini' } },
    { id: 'opencode', name: 'opencode', available: false, reason: 'no-image', why: "no bound sandbox manager's image has it", classes: ['coding'], images: [],
      modes: [], defaultMode: '', autoMode: '', approveMode: '', planMode: '', login: { command: 'opencode auth login' } },
  ];
}
