// native-shots.mjs — screenshots of the native view's key screens, drawn by
// the Lit reference renderer (web/xb/render.js) at 390×844, light and dark:
// the web half of the native contact sheet (native/AGENTS.md). Each scene
// runs native.js in node against backend.mjs's STUB (hack/xbn/node.mjs,
// through native-stub.mjs), keeps the tree, and hands the trees to
// native/tools/shots.mjs. LOOK at the pictures.
//
//   PLAYWRIGHT_DIR=~/lcad-wasm node test/native-shots.mjs [--out dir] [--only name] [--texts default,large] [--sheet]
//
// Needs the xbin checkout this template lives in (hack/xbn, native/tools).
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';

const here = dirname(fileURLToPath(import.meta.url));
const TPL = join(here, '..');
const ROOT = resolve(TPL, '..', '..');
const { runNative } = await import(join(ROOT, 'hack/xbn/node.mjs'));

const argv = process.argv.slice(2);
const opt = (name, dflt) => { const i = argv.indexOf(name); return i >= 0 ? argv[i + 1] : dflt; };
const OUT = resolve(opt('--out', join(tmpdir(), 'agent-native-shots')));
const only = opt('--only', '');

const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 600 + id * 5, ...extra });
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };
// a stand-in thumbnail (the app loads /thumb itself; the fixture page cannot)
const PNG = 'data:image/svg+xml,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="320" height="200"><defs><linearGradient id="g" x2="1" y2="1">' +
  '<stop offset="0" stop-color="#7aa7d8"/><stop offset="1" stop-color="#2d5b8f"/></linearGradient></defs><rect width="320" height="200" fill="url(#g)"/>' +
  '<rect x="24" y="28" width="120" height="14" rx="4" fill="#fff" opacity=".85"/><rect x="24" y="56" width="272" height="8" rx="4" fill="#fff" opacity=".5"/>' +
  '<rect x="24" y="74" width="220" height="8" rx="4" fill="#fff" opacity=".5"/><rect x="24" y="110" width="80" height="64" rx="6" fill="#fff" opacity=".35"/>' +
  '<rect x="120" y="110" width="80" height="64" rx="6" fill="#fff" opacity=".35"/><rect x="216" y="110" width="80" height="64" rx="6" fill="#fff" opacity=".35"/></svg>');
const ANSWER = 'Here is the plan for **Q3**:\n\n1. Close the open invoices (3 left, €12 400 in total)\n2. Renegotiate the two vendor contracts\n3. Hire for the platform team\n\n```sql\nselect vendor, sum(total) from invoices where open group by 1;\n```\n\nI started a subagent to compare vendor prices — its findings are below.';

const runs = [
  { id: 1, title: 'Plan the quarter', status: 'running', activityMs: NOW - 60000, readMs: NOW },
  { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 },
  { id: 3, title: 'Send the invoices', status: 'waiting_input', activityMs: NOW - 3600000, readMs: 0 },
  { id: 4, title: 'Nightly import', status: 'error', activityMs: NOW - 86400000 - 3600000, readMs: NOW },
  { id: 5, title: 'Team roadmap', status: 'idle', activityMs: NOW - 3 * 86400000, pinnedAt: NOW, readMs: NOW, visibility: 'team', members: 2 },
  { id: 6, title: 'Pick a vendor', status: 'waiting_input', activityMs: NOW - 8 * 86400000, readMs: NOW },
  { id: 7, title: 'Office move checklist', status: 'idle', activityMs: NOW - 45 * 86400000, readMs: NOW },
];

const views = {
  1: {
    access: 'owner', config: { toolset: 'private' }, memory: { goal: 'a Q3 plan the team agrees on', vendors: 'acme, globex' },
    files: [{ path: 'q3.html' }, { path: 'invoices.csv' }],
    run: { id: 1, title: 'Plan the quarter', status: 'running', parentId: 0, rootId: 1 },
    queued: [{ id: 7, text: 'also include the hiring budget' }],
    messages: [
      msg(1, 'user', 'Plan the quarter: invoices, vendors, hiring.\n\n[attached: board.png (image/png, 212 KB)]', { sender: 'admin' }),
      msg(2, 'assistant', '', { reasoning: 'Invoices first, then vendors — the subagent can compare prices meanwhile.', reasoningMs: 4200,
        toolCalls: [
          call('c1', 'xbin_call', { method: 'GET', path: '/api/apps/books/invoices?open=1', summary: 'Look up open invoices' }),
          call('c2', 'file_write', { path: 'q3.html', content: '<h1>Q3</h1>' }),
          call('s1', 'subagent_spawn', { task: 'compare vendor prices for acme and globex', label: 'vendor prices' }),
        ] }),
      msg(3, 'tool', 'HTTP 200\n[{"id":17,"total":4200},{"id":18,"total":3100},{"id":21,"total":5100}]', { toolCallId: 'c1', name: 'xbin_call' }),
      msg(4, 'tool', 'wrote q3.html (v1)', { toolCallId: 'c2', name: 'file_write' }),
      msg(5, 'tool', '(waiting for subagent #2…)', { toolCallId: 's1', name: 'subagent_spawn' }),
      msg(6, 'assistant', ANSWER),
    ],
    messageFiles: { 1: ['board.png'] },
    steps: [{ id: 1, kind: 'render', detail: JSON.stringify({ path: 'q3.html', version: 1 }), created: now - 3600 }],
    links: [{ id: 1, parentId: 1, childId: 2, toolCallId: 's1', mode: 'fg', state: 'running', label: 'vendor prices', phase: 'comparing',
      child: { id: 2, title: 'research', status: 'running', parentId: 1, llmCalls: 3 } }],
  },
  2: {
    access: 'owner', run: { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 }, chain: [{ id: 1, title: 'Plan the quarter' }],
    messages: [msg(1, 'user', 'compare vendor prices', { runId: 2 }),
      msg(2, 'assistant', '', { runId: 2, toolCalls: [call('k1', 'web_fetch', { url: 'https://acme.example/prices', summary: 'Fetch Acme\'s price list' }),
        call('k2', 'web_fetch', { url: 'https://globex.example/prices', summary: 'Fetch Globex\'s price list' })] }),
      msg(3, 'tool', 'acme: $12/seat', { runId: 2, toolCallId: 'k1', name: 'web_fetch' })],
  },
  3: {
    access: 'owner', run: { id: 3, title: 'Send the invoices', status: 'waiting_input', rootId: 3,
      pendingState: { kind: 'approval', toolCalls: [{ function: { name: 'xbin_call' } }, { function: { name: 'mcp:mail:send' } }] } },
    messages: [msg(1, 'user', 'Send the three open invoices to their customers.', { runId: 3 }),
      msg(2, 'assistant', 'I drafted the three emails. Sending needs your approval.', { runId: 3 })],
  },
  4: {
    access: 'owner', run: { id: 4, title: 'Nightly import', status: 'error', rootId: 4, origin: 'schedule', originId: 3 },
    messages: [msg(1, 'user', 'Import yesterday\'s orders.', { runId: 4, origin: 'schedule', label: 'nightly' })],
    steps: [{ id: 1, kind: 'error', detail: JSON.stringify({ error: 'llm-gw: 502 bad gateway (3 retries)' }), created: now - 60 }],
  },
  5: {
    access: 'viewer', run: { id: 5, title: 'Team roadmap', status: 'idle', rootId: 5, owner: 'bob' },
    messages: [msg(1, 'user', 'What ships in October?', { runId: 5, sender: 'bob' }),
      msg(2, 'assistant', 'October: **the native app** (TestFlight) and the calendar sync.', { runId: 5 }),
      msg(3, 'user', 'And the billing rework?', { runId: 5, sender: 'carol' }),
      msg(4, 'assistant', 'Billing moves to November — it waits on the vendor contract.', { runId: 5 })],
  },
  6: {
    access: 'owner', run: { id: 6, title: 'Pick a vendor', status: 'waiting_input', rootId: 6, pendingState: { kind: 'ask' },
      result: 'Acme is cheaper per seat, Globex has the better SLA. **Which matters more** for this contract?' },
    messages: [msg(1, 'user', 'Pick a vendor for the support contract.', { runId: 6 })],
  },
};

const seed = (extra = {}) => ({
  me: ME, runs, views, needs: [
    { reason: 'approval', run: { id: 3, title: 'Send the invoices' } },
    { reason: 'question', run: { id: 6, title: 'Pick a vendor' } },
    { reason: 'failed', run: { id: 4, title: 'Nightly import' } }],
  automations: [
    { kind: 'channel', id: 5, name: 'Slack · acme', access: 'claim', summary: 'Slack workspace acme', config: { adapter: 'apps/slack-bridge', platform: 'slack' } },
    { kind: 'schedule', id: 3, name: 'Nightly import', access: 'owner', enabled: true, mode: 'isolated', lastStatus: 'error: llm-gw 502', lastRunAt: now - 3600,
      config: { cron: '0 9 * * *', goal: 'Import yesterday\'s orders and flag anything unusual.' }, runs: 31 },
    { kind: 'schedule', id: 8, name: 'Morning digest', access: 'owner', enabled: true, mode: 'persistent', unread: 2, lastRunAt: now - 7200,
      config: { cron: '0 9 * * 1-5', goal: 'Summarise what changed overnight.' }, runs: 12 },
    { kind: 'watcher', id: 9, name: 'Price watch', access: 'oversee', owner: 'bob', enabled: false, config: { cron: '@every 1h', goal: 'Watch acme prices' } },
    { kind: 'trigger', id: 6, name: 'Deploys', access: 'owner', enabled: true, summary: 'apps/webhooks pushes deploy', lastRunAt: now - 600, runs: 4,
      config: { source: 'push', sourceRef: 'apps/webhooks', toolset: 'private', dataClass: 'public', goal: 'Check the {{topic}} deploy.', mode: 'isolated' } },
  ],
  autoRuns: { 3: [{ id: 4, title: 'Nightly import', activityMs: NOW - 3600000, status: 'error' }, { id: 41, title: 'Nightly import', activityMs: NOW - 90000000, status: 'idle' }] },
  routes: [
    ['GET', '/runs/1$', { memory: views[1].memory }],
    ['GET', '/runs/1/files$', [{ path: 'q3.html', mime: 'text/html', bytes: 2311, version: 3 }, { path: 'invoices.csv', mime: 'text/csv', bytes: 812, version: 1 },
      { path: 'board.png', mime: 'image/png', bytes: 217000, version: 1, binary: true }]],
    ['GET', '/runs/1/file\\?path=q3.html$', { path: 'q3.html', version: 3, content: '<h1>Q3 plan</h1><p>Three things, in order.</p><ol><li>Invoices</li><li>Vendors</li><li>Hiring</li></ol><img src="https://evil.example/pixel.png">' }],
    ['GET', '/runs/1/tree$', { root: 1, nodes: [
      { id: 1, title: 'Plan the quarter', status: 'running', created: 1, promptTokens: 42000, completionTokens: 5100, lastStep: 'waiting for subagents' },
      { id: 2, parentId: 1, depth: 1, title: 'vendor prices', status: 'running', created: 2, promptTokens: 18000, completionTokens: 2200, lastStep: 'fetching globex' },
      { id: 10, parentId: 1, depth: 1, title: 'hiring plan', status: 'blocked', created: 3, blockReason: 'dep', blockedOn: [2], promptTokens: 0 }],
    totals: { nodes: 3, byStatus: { running: 2, blocked: 1 }, promptTokens: 60000, completionTokens: 7300, llmCalls: 23, active: 2, limit: 4 } }],
    ['GET', '/runs/5/members$', { owner: 'bob', visibility: 'team', teamRole: 'viewer', members: [{ user: 'admin', role: 'viewer' }, { user: 'carol', role: 'participant' }], links: [] }],
    ['GET', '/runs/1/members$', { owner: 'admin', visibility: 'private', teamRole: '', members: [{ user: 'bob', role: 'participant' }], links: [{ id: 3, role: 'viewer', uses: 2, expires: now + 86400 * 5 }] }],
    ['GET', '/config$', { models: { general: 'qwen3-32b', code: 'qwen3-coder' }, system: 'Be brief. Say what you did.', tokenBudget: 200000, maxIters: 40, toolTimeout: 120, subagents: true, approve: true }],
    ['GET', '/models$', { data: [{ id: 'qwen3-32b' }, { id: 'qwen3-coder' }, { id: 'llava' }] }],
    ['GET', '/triggers/unmatched$', { items: [{ from: 'apps/webhooks', name: 'release', count: 3 }] }],
  ],
  ...extra,
});

const tap = (m) => ({ tap: m });
const SCENES = {
  home: [{}, []],
  chat: [{ hash: 'c=1' }, [{ call: ['push', { type: 'thinking', run: 2, root: 1, data: { text: 'Globex lists per-agent pricing…' } }] }]],
  'chat-subagent': [{ hash: 'c=1' }, [{ event: [{ t: 'toolcard', p: { family: 'agent' } }, 'open', {}] }]],
  approval: [{ hash: 'c=3' }, []],
  question: [{ hash: 'c=6' }, []],
  failed: [{ hash: 'c=4' }, []],
  'view-only': [{ hash: 'c=5' }, []],
  menu: [{ hash: 'c=1' }, []], // the menu is an overlay; the tree is the chat
  drawer: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Conversations' } })]],
  'new-chat': [{}, [tap({ t: 'button', p: { label: 'Conversations' } }), tap({ t: 'row', p: { title: 'New chat with options…' } })]],
  share: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Share' } })]],
  'share-readonly': [{ hash: 'c=5' }, [tap({ t: 'button', p: { label: 'Shared' } })]],
  automations: [{ hash: 'auto' }, []],
  'auto-schedule': [{ hash: 'auto=schedule:3' }, []],
  'auto-trigger-form': [{ hash: 'auto=trigger:6' }, [tap({ t: 'button', p: { label: 'Edit' } }),
    { event: [{ t: 'picker', p: { label: 'Tool mode' } }, 'change', { value: 'web' }] },
    { event: [{ t: 'picker', p: { label: 'The data it takes' } }, 'change', { value: 'private' }] }]],
  'auto-channel': [{ hash: 'auto=channel:5' }, []],
  memory: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Memory (2)' } })]],
  files: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Files (2)' } })]],
  'file-editor': [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Files (2)' } }), tap({ t: 'row', p: { title: 'q3.html' } })]],
  render: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Files (2)' } }), tap({ t: 'button', p: { label: 'Render' } })]],
  tree: [{ hash: 'c=1' }, [tap({ t: 'button', p: { label: 'Workflow tree' } })]],
  settings: [{}, [tap({ t: 'button', p: { label: 'Settings' } }), tap({ t: 'row', p: { title: 'Config' } })]],
};

const treesDir = join(OUT, 'trees');
mkdirSync(treesDir, { recursive: true });
const thumbs = (n) => {
  if (n && typeof n === 'object') {
    if (n.p && typeof n.p.src === 'string' && n.p.src.includes('/thumb?')) n.p.src = PNG;
    if (n.p && Array.isArray(n.p.files)) for (const f of n.p.files) if (f.src) f.src = PNG;
    for (const c of n.c || []) thumbs(c);
  }
  return n;
};
let bad = 0;
for (const [name, [state, steps]] of Object.entries(SCENES)) {
  if (only && !name.includes(only)) continue;
  const r = await runNative({ entry: join(TPL, 'native.js'), data: { now: NOW, self: 'apps/agent', setup: join(here, 'native-stub.mjs'), seed: seed() },
    steps: [...steps, { wait: 300 }], state: state.hash ? state : null });
  const problems = [...r.errors.map((e) => e.message), ...r.diagnostics.filter((d) => d.level !== 'info').map((d) => d.message)];
  if (r.fatal || problems.length) { bad++; console.error(`${name}: ${r.fatal || problems.join('; ')}`); }
  writeFileSync(join(treesDir, `agent-${name}.json`), JSON.stringify({ v: 1, root: thumbs(r.tree.root) }));
}

const shots = spawnSync(process.execPath, [join(ROOT, 'native/tools/shots.mjs'), '--trees', treesDir, '--out', join(OUT, 'png'),
  '--texts', opt('--texts', 'default'), '--schemes', 'light,dark', ...(argv.includes('--sheet') ? ['--sheet'] : [])], { stdio: 'inherit' });
if (shots.status) console.error(`shots.mjs exited ${shots.status} (a blocked external load in a render preview logs a CSP console error — expected)`);
process.exit(bad ? 1 : 0);
