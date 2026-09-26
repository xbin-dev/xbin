// hack/agent-template-native.test.mjs — the agent template's native view
// (builtin-templates/agent/native.js) rendered in node against the web
// tests' fake backend (test/backend.mjs STUB, through test/native-stub.mjs),
// with hack/xbn/node.mjs — xb-native's JSON target. The tests assert what a
// person gets, not the tree's shape: an approval card has Approve and Deny, a
// failed run offers Retry, a view-only conversation has a disabled composer,
// a shared one says who wrote what, the drawer lists and acts, and so on.
// Every run must end with no runtime error and no warning diagnostic.
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);

async function run(seed, steps = [], { state = null, data = {} } = {}) {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed, ...data }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

// find/all: nodes of a tree by type and props (a subset, compared as JSON),
// optionally inside an ancestor that matches `in`.
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
const texts = (tree, t, prop) => all(tree.root || tree, { t }).map((n) => (n.p || {})[prop]);
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
// the screen on top of the nav (what the person sees)
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };

const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 600 + id, ...extra });
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

// A seed in backend.mjs's shape: {runs, views, needs, automations, autoRuns, me} (+ routes, pages).
const chatSeed = () => ({
  me: ME,
  runs: [
    { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1, activityMs: NOW - 1000 },
    { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 },
  ],
  views: {
    1: {
      access: 'owner',
      run: { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1 },
      queued: [{ id: 7, text: 'queued one' }],
      messages: [
        msg(1, 'user', 'plan it'),
        msg(2, 'assistant', 'On it — **three** things first.', {
          reasoning: 'weighing the options', reasoningMs: 2100,
          toolCalls: [
            call('c1', 'xbin_call', { method: 'GET', path: '/api/apps/books/invoices', summary: 'Look up open invoices' }),
            call('c2', 'file_read', { path: 'notes.md' }),
            call('s1', 'subagent_spawn', { task: 'research vendor prices', label: 'research' }),
          ],
        }),
        msg(3, 'tool', 'HTTP 200\n' + 'x'.repeat(1500), { toolCallId: 'c1', name: 'xbin_call' }),
        msg(4, 'tool', '(running…)', { toolCallId: 'c2', name: 'file_read' }),
        msg(5, 'tool', '(waiting for subagent #2…)', { toolCallId: 's1', name: 'subagent_spawn' }),
        msg(6, 'user', '[message from your parent]\nkeep going'),
      ],
      links: [{ id: 1, parentId: 1, childId: 2, toolCallId: 's1', mode: 'fg', state: 'running', label: 'research', phase: 'working',
        child: { id: 2, title: 'research', status: 'running', parentId: 1, llmCalls: 1 } }],
    },
    2: {
      access: 'owner',
      run: { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 },
      chain: [{ id: 1, title: 'plan the quarter' }],
      messages: [
        msg(1, 'system', 'sys', { runId: 2 }), msg(2, 'user', 'research vendor prices', { runId: 2 }),
        msg(3, 'assistant', '', { runId: 2, toolCalls: [call('k1', 'web_fetch', { url: 'https://x', summary: 'Fetch the price list' })] }),
        msg(4, 'tool', 'prices…', { runId: 2, toolCallId: 'k1', name: 'web_fetch' }),
      ],
    },
  },
  routes: [['POST', '/runs/1/interrupt$', { ok: 'true', returned: [{ text: 'first' }, { text: 'second' }] }]],
});

test('home: the greeting, example asks, what needs you, and the composer', async () => {
  const seed = { me: ME, runs: [{ id: 2, title: 'which vendor?', status: 'waiting_input' }],
    needs: [{ reason: 'question', run: { id: 2, title: 'which vendor?' } }, { reason: 'failed', run: { id: 3, title: 'nightly digest' } }] };
  const r = await run(seed, [
    { snapshot: 'home' },
    { tap: { t: 'row', p: { title: 'What can you do in this workspace?' } } },
    { snapshot: 'picked' },
    { tap: { t: 'row', p: { title: 'which vendor?' } } },
  ]);
  const home = r.snapshots.home;
  const scr = topScreen(home);
  assert.equal(scr.p.title, 'Agent');
  assert.ok(texts(home, 'text', 'text').includes('What do you need?'));
  const needs = find(home, { t: 'section', p: { title: 'Needs you' } });
  assert.deepEqual(needs.c.map((c) => [c.p.title, c.p.subtitle, c.p.tone]),
    [['which vendor?', 'has a question for you', 'accent'], ['nightly digest', 'failed', 'danger']]);
  const composer = find(home, { t: 'composer' });
  assert.equal(composer.p.placeholder, 'ask anything…');
  assert.equal(composer.p.upload, undefined, 'at home there is no run to upload into');
  assert.ok(find(home, { t: 'button', p: { label: 'internal', icon: 'lock' } }), 'the tool-mode chip');
  assert.equal(find(r.snapshots.picked, { t: 'composer' }).p.value, 'What can you do in this workspace?', 'an example fills the composer');
  assert.equal(called(r, 'GET', /\/runs\/2\/view\?limit=50$/).length, 1, 'a need opens its conversation, read in pages');
  assert.equal(topScreen(r.tree).p.title, 'which vendor?');
});

test('a conversation: the transcript from the model\'s blocks, a subagent inside its card', async () => {
  const r = await run(chatSeed(), [{ snapshot: 'open' }], { state: { hash: 'c=1' } });
  const t = r.snapshots.open;
  const scr = topScreen(t);
  assert.equal(scr.p.title, 'plan the quarter');
  assert.match(scr.p.subtitle, /running · 🔒 internal/);
  const tr = find(scr, { t: 'transcript', p: { follow: true } });
  assert.ok(tr, 'the transcript follows its end');
  const kinds = tr.c.map((c) => c.t + (c.t === 'message' ? ':' + c.p.role : ''));
  assert.deepEqual(kinds.slice(0, 7), ['message:user', 'thinking', 'message:assistant', 'toolcard', 'toolcard', 'toolcard', 'toolcard']);
  const think = find(tr, { t: 'thinking' });
  assert.equal(think.p.seconds, 2);
  assert.equal(think.p.open, false, 'finished thinking is folded');
  assert.equal(find(tr, { t: 'message', p: { role: 'assistant' } }).p.markdown, true);
  assert.ok(find(tr, { t: 'message', p: { role: 'assistant' } }).p.tokens, 'the runtime lexes its markdown');
  const cards = all(tr, { t: 'toolcard' }).filter((c) => c.p.family !== 'notice');
  assert.deepEqual(cards.map((c) => c.p.title).slice(0, 3), ['Look up open invoices', 'Read notes.md', 'research']);
  const inv = cards[0];
  assert.equal(inv.p.state, 'ok');
  assert.ok(inv.e.includes('open'), 'a long result offers ↗ (show all)');
  assert.ok(find(inv, { t: 'code' }).p.text.endsWith('…'), 'a long result is cut');
  assert.equal(cards[1].p.state, 'running');
  assert.ok(!cards[1].e.includes('open'));
  const agent = cards[2];
  assert.equal(agent.p.family, 'agent');
  assert.equal(agent.p.open, true, 'a running subagent is open');
  assert.ok(agent.e.includes('open'), 'open ↗ pushes its session');
  const inner = find(agent, { t: 'transcript' });
  assert.deepEqual(all(inner, { t: 'toolcard' }).map((c) => c.p.title), ['Fetch the price list'], 'the child\'s own work, folded inside');
  const notice = find(tr, { t: 'toolcard', p: { family: 'notice' } });
  assert.equal(notice.p.title, '↵ message from your parent');
  assert.ok(find(scr, { t: 'activity', p: { live: true } }));
  assert.equal(r.messages.filter((m) => m.op === 'state').pop().state.hash, 'c=1', 'where you are is saved for the next runtime');
  assert.equal(r.messages.filter((m) => m.op === 'meta').pop().title, 'plan the quarter');
});

test('a subagent opened full screen: its parent under it, back goes up', async () => {
  const r = await run(chatSeed(), [
    { event: [{ t: 'toolcard', p: { title: 'research' } }, 'open', {}] },
    { snapshot: 'child' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
  ], { state: { hash: 'c=1' } });
  const nav = find(r.snapshots.child, { t: 'nav' });
  assert.deepEqual(nav.c.map((s) => s.p.title), ['plan the quarter', 'research']);
  assert.match(nav.c[1].p.subtitle, /^in plan the quarter/);
  assert.equal(topScreen(r.tree).p.title, 'plan the quarter');
  assert.equal(find(r.tree, { t: 'nav' }).c.length, 1);
});

test('streaming: deltas append to the answer being written', async () => {
  const r = await run(chatSeed(), [
    { call: ['push', { type: 'text', run: 1, root: 1, data: { text: 'He' } }] },
    { call: ['push', { type: 'text.delta', run: 1, root: 1, data: { delta: 'llo', at: 2 } }] },
    { snapshot: 'live' },
  ], { state: { hash: 'c=1' } });
  assert.ok(called(r, 'GET', /\/stream\?run=1&since=[^&]+&deltas=1$/).length, 'the stream asks for deltas');
  const m = find(r.snapshots.live, { t: 'message', p: { streaming: true } });
  assert.equal(m.p.text, 'Hello');
  assert.equal(find(r.snapshots.live, { t: 'activity' }).p.text, 'Writing…');
});

test('the composer: queued messages taken back, Stop returns queued text, attachments the app uploaded', async () => {
  const r = await run(chatSeed(), [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'queued: queued one' } } },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'shot.png', response: { path: 'shot.png', mime: 'image/png', bytes: 1234, binary: true } }] },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'huge.mov', response: { error: 'too large: over 16 MiB' } }] },
    { snapshot: 'attached' },
    { event: [{ t: 'composer' }, 'remove', { id: '2' }] },
    { event: [{ t: 'composer' }, 'stop', {}] },
    { snapshot: 'stopped' },
    { event: [{ t: 'composer' }, 'send', { value: 'look at this' }] },
    { snapshot: 'sent' },
  ], { state: { hash: 'c=1' } });
  const c = find(r.snapshots.open, { t: 'composer' });
  assert.equal(c.p.busy, true, 'Stop while it works');
  assert.equal(c.p.placeholder, 'steer — delivered at the agent\'s next step…');
  assert.deepEqual(c.p.upload, { method: 'PUT', path: '/api/apps/agent/runs/1/upload?name={name}' }, 'the app uploads into the run itself');
  assert.equal(called(r, 'DELETE', /\/runs\/1\/inbox\/7$/).length, 1, 'a queued message is taken back');
  assert.deepEqual(find(r.snapshots.attached, { t: 'composer' }).p.attachments.map((a) => a.name), ['shot.png', 'huge.mov — too large (max 16.0 MB)']);
  assert.equal(find(r.snapshots.stopped, { t: 'composer' }).p.value, 'first\n\nsecond', 'Stop gives the queued text back');
  const sent = called(r, 'POST', /\/runs\/1\/message$/).pop();
  assert.deepEqual({ ...JSON.parse(sent.body), clientId: 'x' }, { text: 'look at this', files: ['shot.png'], clientId: 'x' });
  assert.equal(find(r.snapshots.sent, { t: 'composer' }).p.value, '', 'the text box empties once it is on its way');
  assert.deepEqual(find(r.snapshots.sent, { t: 'composer' }).p.attachments, []);
});

// one conversation, alone, in a state
const oneSeed = (view, extra = {}) => ({ me: ME, runs: [{ id: 9, title: view.run.title || 'run 9', status: view.run.status }],
  views: { 9: { access: 'owner', ...view, run: { id: 9, rootId: 9, parentId: 0, ...view.run } } }, ...extra });

test('asking: the approval card has Approve and Deny; the question is answered by a message', async () => {
  const r = await run(oneSeed({ run: { title: 'send the invoices', status: 'waiting_input',
    pendingState: { kind: 'approval', toolCalls: [{ function: { name: 'xbin_call' } }, { function: { name: 'file_write' } }] } } }), [
    { snapshot: 'asked' },
    { event: [{ t: 'approval' }, 'choose', { id: 'approve', feedback: '' }] },
  ], { state: { hash: 'c=9' } });
  const a = find(r.snapshots.asked, { t: 'approval' });
  assert.equal(a.p.title, 'The agent wants to run');
  assert.equal(a.p.text, 'xbin_call\nfile_write');
  assert.deepEqual(a.p.options.map((o) => o.label), ['Approve', 'Deny']);
  assert.ok(a.e.includes('choose'));
  assert.equal(find(r.snapshots.asked, { t: 'activity' }).p.text, 'Waiting for your approval');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/runs\/9\/approve$/)[0].body), { approve: true });

  const q = await run(oneSeed({ run: { title: 'pick one', status: 'waiting_input', pendingState: { kind: 'ask' }, result: 'Which **vendor**?' } }), [
    { snapshot: 'asked' },
    { event: [{ t: 'question' }, 'submit', { content: { answer: 'Acme' } }] },
  ], { state: { hash: 'c=9' } });
  const qn = find(q.snapshots.asked, { t: 'question' });
  assert.equal(qn.p.title, 'The agent is asking');
  assert.equal(qn.p.schema.description, 'Which vendor?', 'the question as text');
  assert.equal(find(q.snapshots.asked, { t: 'composer' }).p.placeholder, 'answer the question…');
  assert.equal(JSON.parse(called(q, 'POST', /\/runs\/9\/message$/)[0].body).text, 'Acme');
});

test('a failed run offers Retry, inline and in the menu', async () => {
  const r = await run(oneSeed({ run: { title: 'broke', status: 'error' },
    steps: [{ id: 1, kind: 'error', detail: JSON.stringify({ error: 'model unreachable' }), created: now - 5 }] }), [
    { snapshot: 'failed' },
    { tap: { t: 'button', p: { label: 'Retry' }, in: { t: 'composer' } } },
  ], { state: { hash: 'c=9' } });
  const t = r.snapshots.failed;
  assert.ok(find(t, { t: 'button', p: { label: 'Retry' }, in: { t: 'composer' } }), 'Retry beside the composer');
  assert.ok(find(t, { t: 'button', p: { label: 'Retry' }, in: { t: 'menu' } }), '…and in the menu');
  assert.deepEqual(find(t, { t: 'step' }).p, { glyph: '⚠', tone: 'danger', text: 'model unreachable' });
  assert.equal(called(r, 'POST', /\/runs\/9\/resume$/).length, 1);
  const menu = all(t.root, { t: 'button', in: { t: 'menu', p: { icon: 'ellipsis' } } }).map((b) => b.p.label);
  assert.deepEqual(menu, ['Retry', 'Rename…', 'Compact', 'Learn skill', 'Memory (0)', 'Files (0)', 'Share', 'Delete']);
  assert.deepEqual(find(t, { t: 'button', p: { label: 'Delete' } }).p.confirm, { title: 'Delete this run and its history?', label: 'Delete', destructive: true });
});

test('view only: a disabled composer, no uploads, the words say so', async () => {
  const r = await run(oneSeed({ access: 'viewer', run: { title: 'their plan', status: 'idle', owner: 'bob' } }, { me: { ...ME, manager: false, user: 'carol' } }),
    [{ snapshot: 'viewer' }], { state: { hash: 'c=9' } });
  const t = r.snapshots.viewer;
  const c = find(t, { t: 'composer' });
  assert.equal(c.p.disabled, true);
  assert.equal(c.p.placeholder, 'view only — shared with you to read');
  assert.equal(c.p.upload, undefined);
  assert.match(topScreen(t).p.subtitle, /view only/);
  const menu = all(t.root, { t: 'button', in: { t: 'menu', p: { icon: 'ellipsis' } } }).map((b) => b.p.label);
  assert.deepEqual(menu, ['Memory (0)', 'Files (0)', 'Shared']);
});

test('a shared conversation says who wrote each message; attachments show as thumbnails', async () => {
  const r = await run(oneSeed({ run: { title: 'team plan', status: 'idle' },
    messages: [
      msg(1, 'user', 'first, from me', { runId: 9, sender: 'admin' }),
      msg(2, 'user', 'and from bob\n\n[attached: shot.png (image/png, 12 KB), old.txt (text/plain, 1 KB)]', { runId: 9, sender: 'bob' }),
    ],
    messageFiles: { 2: ['shot.png'] } }), [{ snapshot: 'shared' }], { state: { hash: 'c=9' } });
  const users = all(r.snapshots.shared.root, { t: 'message', p: { role: 'user' } });
  assert.deepEqual(users.map((m) => m.p.sender ?? null), [null, 'bob']);
  assert.deepEqual(users[1].p.files, [
    { name: 'shot.png · 12 KB', mime: 'image/png', src: '/api/apps/agent/runs/9/thumb?path=shot.png&w=480' },
    { name: 'old.txt — deleted', mime: 'text/plain' }]);
  assert.ok(find(users[1], { t: 'button', p: { label: 'Open shot.png' } }), 'an attachment opens in Files');
});

test('paging: a long conversation loads older pages as you scroll up', async () => {
  const r = await run(oneSeed({ run: { title: 'long', status: 'idle' }, messages: [msg(40, 'user', 'late', { runId: 9 })] },
    { pages: { 9: { hasOlder: true, nextBefore: 40, compacted: 3 } } }), [
    { snapshot: 'newest' },
    { event: [{ t: 'transcript', p: { follow: true } }, 'more', {}] },
  ], { state: { hash: 'c=9' } });
  const tr = find(r.snapshots.newest, { t: 'transcript', p: { follow: true } });
  assert.equal(tr.p.older, true);
  assert.ok(tr.e.includes('more'));
  assert.ok(find(tr, { t: 'notice', p: { text: 'earlier turns were compacted into the summary' } }));
  assert.equal(called(r, 'GET', /\/runs\/9\/view\?limit=50&before=40$/).length, 1);
});

test('a subagent\'s approval is given from its parent\'s card', async () => {
  const seed = chatSeed();
  seed.views[2].run = { ...seed.views[2].run, status: 'waiting_input', pendingState: { kind: 'approval', toolCalls: [{ function: { name: 'web_fetch' } }] } };
  const r = await run(seed, [
    { snapshot: 'card' },
    { event: [{ t: 'approval', in: { t: 'toolcard', p: { family: 'agent' } } }, 'choose', { id: 'deny', feedback: '' }] },
  ], { state: { hash: 'c=1' } });
  const a = find(r.snapshots.card, { t: 'approval', in: { t: 'toolcard', p: { family: 'agent' } } });
  assert.equal(a.p.title, 'The subagent wants to run');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/runs\/2\/approve$/)[0].body), { approve: false });
});

test('the drawer: groups, row actions, search with snippets, scope, a pasted invite link', async () => {
  const day = 86400000;
  const seed = { me: ME, runs: [
    { id: 1, title: 'today one', status: 'running', activityMs: NOW - 1000, readMs: 0 },
    { id: 2, title: 'pinned one', status: 'idle', activityMs: NOW - 3 * day, pinnedAt: NOW - 10, readMs: NOW },
    { id: 3, title: 'old one', status: 'error', activityMs: NOW - 40 * day, readMs: NOW },
    { id: 4, title: 'their one', status: 'waiting_input', activityMs: NOW - day, access: 'viewer', mine: true, owner: 'bob', visibility: 'team', readMs: NOW },
  ], routes: [['POST', '/join$', { runId: 4 }]] };
  const r = await run(seed, [
    { tap: { t: 'button', p: { label: 'Conversations' } } },
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Pin' }, in: { t: 'row', p: { title: 'today one' } } } },
    { snapshot: 'pinned' },
    { event: [{ t: 'screen', p: { title: 'Conversations' } }, 'search', { value: 'old' }] },
    { wait: 250 },
    { snapshot: 'searched' },
    { event: [{ t: 'screen', p: { title: 'Conversations' } }, 'search', { value: '' }] },
    { wait: 250 },
    { event: [{ t: 'picker', in: { t: 'sheet' } }, 'change', { value: 'archived' }] },
    { snapshot: 'archived' },
    { event: [{ t: 'screen', p: { title: 'Conversations' } }, 'search', { value: 'https://x/c/apps/agent/#join=TOK_1' }] },
  ]);
  const t = r.snapshots.open;
  const sheet = find(t, { t: 'sheet' });
  assert.equal(sheet.p.edge, 'leading', 'a drawer from the leading edge');
  const sections = all(sheet, { t: 'section' }).map((s) => (s.p || {}).title || '');
  assert.deepEqual(sections, ['', '', 'Pinned', 'Today', 'Yesterday', 'Previous 30 days', 'Older'].filter((x) => x !== 'Previous 30 days'));
  const row = (title) => find(sheet, { t: 'row', p: { title } });
  assert.equal(row('today one').p.badge, 'working');
  assert.equal(row('old one').p.badge, 'failed');
  assert.equal(row('old one').p.tone, 'danger');
  assert.equal(row('their one').p.badge, 'waiting for you');
  assert.match(row('their one').p.subtitle, /^⇆ shared/);
  assert.deepEqual(all(row('today one'), { t: 'button' }).map((b) => b.p.label), ['Rename', 'Pin', 'Share…', 'Archive', 'Delete']);
  assert.deepEqual(all(row('their one'), { t: 'button' }).map((b) => b.p.label), ['Pin', 'Archive', 'Leave']);
  assert.equal(find(row('their one'), { t: 'button', p: { label: 'Leave' } }).p.confirm.title, 'Leave "their one"?');
  assert.deepEqual(JSON.parse(called(r, 'PATCH', /\/runs\/1$/)[0].body), { pinned: true });
  assert.deepEqual(all(find(r.snapshots.pinned, { t: 'section', p: { title: 'Pinned' } }), { t: 'row' }).map((x) => x.p.title), ['today one', 'pinned one']);
  const results = find(r.snapshots.searched, { t: 'section', p: { title: 'Results' } });
  assert.deepEqual(all(results, { t: 'row' }).map((x) => x.p.title), ['old one']);
  assert.ok(called(r, 'GET', /\/conversations\?q=old$/).length);
  assert.ok(called(r, 'GET', /\/conversations\?scope=mine&archived=1$/).length, 'the archive scope');
  assert.ok(find(r.snapshots.archived, { t: 'empty', p: { title: 'nothing archived' } }));
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/join$/)[0].body), { token: 'TOK_1' });
  assert.equal(topScreen(r.tree).p.title, 'their one', 'a pasted invite link joins and opens the conversation');
  assert.equal(find(r.tree, { t: 'sheet' }), null, 'and closes the drawer');
});

test('new chat with options, rename and share sheets', async () => {
  const seed = { me: ME, runs: [{ id: 1, title: 'plan', status: 'idle' }], routes: [
    ['POST', '/ask$', { id: 5, title: 'new one', status: 'running', rootId: 5 }],
    ['GET', '/runs/1/members$', { owner: 'admin', visibility: 'private', teamRole: '', members: [{ user: 'bob', role: 'viewer' }], links: [{ id: 3, role: 'viewer', uses: 1 }] }],
    ['POST', '/runs/1/links$', { id: 4, token: 'TOKEN_9' }],
  ] };
  const r = await run(seed, [
    { tap: { t: 'button', p: { label: 'Conversations' } } },
    { tap: { t: 'row', p: { title: 'New chat with options…' } } },
    { input: [{ t: 'field', in: { t: 'section', p: { title: 'First message' } } }, 'summarise the week'] },
    { event: [{ t: 'picker', in: { t: 'sheet', p: { title: 'New chat' } } }, 'change', { value: 'web' }] },
    { input: [{ t: 'field', p: { label: 'Title' } }, 'weekly'] },
    { snapshot: 'form' },
    { tap: { t: 'button', p: { label: 'Start' } } },
    { snapshot: 'started' },
    { tap: { t: 'button', p: { label: 'Conversations' } } },
    { tap: { t: 'button', p: { label: 'Share…' }, in: { t: 'row', p: { title: 'plan' } } } },
    { snapshot: 'share' },
    { tap: { t: 'button', p: { label: 'Create link' } } },
    { snapshot: 'linked' },
  ]);
  assert.equal(find(r.snapshots.form, { t: 'sheet' }).p.title, 'New chat');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/ask$/)[0].body), { text: 'summarise the week', title: 'weekly', system: '', toolset: 'web' });
  assert.equal(find(r.snapshots.started, { t: 'sheet' }), null);
  const share = find(r.snapshots.share, { t: 'sheet' });
  assert.equal(share.p.title, 'Share “plan”');
  assert.equal(find(share, { t: 'picker', p: { style: 'inline' } }).p.value, 'private');
  assert.deepEqual(find(share, { t: 'row', p: { title: 'bob' } }).p.detail, 'can read');
  assert.ok(find(share, { t: 'button', p: { label: 'Revoke' } }));
  const link = find(r.snapshots.linked, { t: 'row', p: { title: 'https://xbin.test/c/apps/agent/#join=TOKEN_9' } });
  assert.ok(link, 'a new link is shown once, without the page\'s query');
  assert.equal(find(link, { t: 'button', p: { label: 'Copy' } }).p.copy, 'https://xbin.test/c/apps/agent/#join=TOKEN_9');
});

test('automations: the page, one schedule with its runs, the forms (all four kinds)', async () => {
  const seed = { me: ME, runs: [{ id: 20, title: 'a digest', status: 'idle' }],
    automations: [
      { kind: 'schedule', id: 3, name: 'digest', access: 'owner', enabled: true, mode: 'isolated', config: { cron: '0 9 * * *', goal: 'sum up' }, unread: 2, runs: 4 },
      { kind: 'watcher', id: 4, name: 'prices', access: 'oversee', owner: 'bob', enabled: false, config: { cron: '@every 1h', goal: 'watch' } },
      { kind: 'channel', id: 5, name: 'slack', access: 'claim', summary: 'Slack · acme', config: { adapter: 'apps/slack', platform: 'slack' } },
      { kind: 'trigger', id: 6, name: 'deploys', access: 'owner', enabled: true, summary: 'apps/webhooks pushes deploy', lastStatus: 'error: boom',
        config: { source: 'push', sourceRef: 'apps/webhooks', toolset: 'private', dataClass: 'public', goal: 'check it', mode: 'isolated' } },
    ],
    autoRuns: { 3: [{ id: 20, title: 'a digest', activityMs: NOW - 5000, status: 'idle', unread: true }] },
    routes: [['GET', '/triggers/unmatched$', { items: [{ from: 'apps/webhooks', name: 'release', count: 2 }] }],
      ['GET', '/triggers/6/events$', { events: [{ eventId: 'e1', topic: 'deploy.prod', at: now - 60, accepted: true, runId: 20 }, { eventId: 'e2', topic: 'deploy.dev', at: now - 30, accepted: false, reason: 'rate' }] }]],
  };
  const r = await run(seed, [
    { snapshot: 'detail' },
    { tap: { t: 'button', p: { label: 'Run now' }, in: { t: 'screen', p: { title: 'digest' } } } },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'New schedule' } } },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'standup'] },
    { input: [{ t: 'field', p: { label: 'What to do' } }, 'post the standup'] },
    { snapshot: 'form' },
    { tap: { t: 'button', p: { label: 'Create' } } },
    { snapshot: 'created' },
  ], { state: { hash: 'auto=schedule:3' } });
  const d = r.snapshots.detail;
  const nav = find(d, { t: 'nav' });
  assert.deepEqual(nav.c.map((s) => s.p.title), ['Agent', 'Automations', 'digest'], 'a deep link to one automation');
  const detail = topScreen(d);
  assert.ok(find(detail, { t: 'row', p: { title: 'every day at 9:00' } }));
  assert.deepEqual(all(find(detail, { t: 'section', p: { title: 'Runs' } }), { t: 'row' }).map((x) => x.p.title), ['a digest']);
  assert.equal(find(detail, { t: 'button', p: { label: 'Delete' } }).p.confirm.title, 'Delete "digest"?');
  assert.ok(called(r, 'POST', /\/automations\/schedule\/3\/read$/).length, 'opening it marks its runs read');
  assert.equal(called(r, 'POST', /\/schedules\/3\/trigger$/).length, 1);
  const list = topScreen(r.snapshots.list);
  assert.equal(list.p.title, 'Automations');
  assert.deepEqual(all(list, { t: 'section' }).map((s) => (s.p || {}).title), ['Channels', 'Schedules', 'Watchers', 'Triggers', 'Pushes nothing took']);
  const row = (title) => find(list, { t: 'row', p: { title } });
  assert.equal(row('digest').p.badge, '2 new');
  assert.equal(row('prices').p.badge, 'off');
  assert.match(row('prices').p.subtitle, /^bob's/);
  assert.equal(row('slack').p.badge, 'new — claim it');
  assert.equal(row('deploys').p.badge, 'error');
  assert.ok(find(list, { t: 'button', p: { label: 'Create a trigger' } }));
  assert.equal(topScreen(r.snapshots.form).p.title, 'New schedule');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/schedules$/)[0].body), { name: 'standup', cron: '0 9 * * *', goal: 'post the standup', mode: 'isolated',
    visibility: 'private', watcher: false, toolset: 'private', targetRun: 0 });
  assert.equal(topScreen(r.snapshots.created).p.title, 'standup', 'a new schedule opens');

  // a trigger: its events and actions; the form refuses a lane/data-class clash
  const t = await run(seed, [
    { snapshot: 'trigger' },
    { tap: { t: 'button', p: { label: 'Edit' } } },
    { event: [{ t: 'picker', p: { label: 'Tool mode' } }, 'change', { value: 'web' }] },
    { event: [{ t: 'picker', p: { label: 'The data it takes' } }, 'change', { value: 'private' }] },
    { snapshot: 'clash' },
  ], { state: { hash: 'auto=trigger:6' } });
  const tr = topScreen(t.snapshots.trigger);
  assert.equal(tr.p.title, 'deploys');
  assert.deepEqual(all(find(tr, { t: 'section', p: { title: 'Recent events' } }), { t: 'row' }).map((x) => [x.p.title, x.p.detail]),
    [['deploy.prod', 'ran #20'], ['deploy.dev', 'over its hourly cap']]);
  assert.ok(find(tr, { t: 'notice', p: { tone: 'info' } }).p.text.startsWith('Pushes come from apps/webhooks'));
  assert.ok(find(tr, { t: 'button', p: { label: 'Fire a test event' } }));
  const form = topScreen(t.snapshots.clash);
  assert.equal(form.p.title, 'Edit trigger');
  assert.equal(find(form, { t: 'button', p: { label: 'Save' } }).p.disabled, true, 'the firewall holds Save');
  assert.ok(find(form, { t: 'notice', p: { tone: 'danger' } }));

  // a channel to claim: its rules and Claim
  const c = await run(seed, [{ snapshot: 'channel' }], { state: { hash: 'auto=channel:5' } });
  const ch = topScreen(c.snapshots.channel);
  assert.ok(find(ch, { t: 'picker', p: { label: 'Direct messages' } }));
  assert.ok(find(ch, { t: 'button', p: { label: 'Claim' } }));
});

test('run tools: memory, files and the editor, skills, the workflow tree, settings', async () => {
  const seed = chatSeed();
  seed.views[1].memory = { goal: 'q3 plan' };
  seed.views[1].files = [{ path: 'report.html' }];
  seed.routes.push(
    ['GET', '/runs/1$', { memory: { goal: 'q3 plan' } }],
    ['GET', '/runs/1/files$', [{ path: 'report.html', mime: 'text/html', bytes: 2048, version: 2 }, { path: 'shot.png', mime: 'image/png', bytes: 99, version: 1, binary: true }]],
    ['GET', '/runs/1/file\\?path=report.html$', { path: 'report.html', content: '<h1>Q3</h1><img src="https://evil.example/x.png">', version: 2 }],
    ['PUT', '/runs/1/file$', { version: 3 }],
    ['GET', '/runs/1/tree$', { root: 1, nodes: [{ id: 1, title: 'plan the quarter', status: 'running', created: 1, promptTokens: 1000, completionTokens: 500, lastStep: 'thinking' },
      { id: 2, parentId: 1, depth: 1, title: 'research', status: 'blocked', created: 2, blockReason: 'dep', blockedOn: [3], promptTokens: 200 }],
    totals: { nodes: 2, byStatus: { running: 1, blocked: 1 }, promptTokens: 1200, completionTokens: 500, llmCalls: 7, active: 1, limit: 4 } }],
    ['GET', '/config$', { models: { general: 'm1' }, system: 'be brief', tokenBudget: 1000, maxIters: 5, toolTimeout: 30, subagents: true, approve: false, features: {} }],
    ['GET', '/models$', { data: [{ id: 'm1' }, { id: 'm2' }] }],
    ['GET', '/skills$', [{ name: 'weekly', description: 'the weekly digest', owner: 'bob' }]],
  );
  const r = await run(seed, [
    { tap: { t: 'button', p: { label: 'Memory (1)' } } },
    { snapshot: 'memory' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'Files (1)' } } },
    { snapshot: 'files' },
    { tap: { t: 'row', p: { title: 'report.html' } } },
    { input: [{ t: 'field', p: { label: 'Content' } }, '<h1>Q3!</h1>'] },
    { tap: { t: 'button', p: { label: 'Save' } } },
    { snapshot: 'saved' },
    { tap: { t: 'button', p: { label: 'Render' } } },
    { snapshot: 'render' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'Workflow tree' } } },
    { snapshot: 'tree' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'Conversations' } } },
    { tap: { t: 'row', p: { title: 'New chat' } } },
    { tap: { t: 'button', p: { label: 'Settings' } } },
    { snapshot: 'settings' },
    { tap: { t: 'row', p: { title: 'Config' } } },
    { snapshot: 'config' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { tap: { t: 'row', p: { title: 'Skills' } } },
    { snapshot: 'skills' },
  ], { state: { hash: 'c=1' } });
  const mem = topScreen(r.snapshots.memory);
  assert.equal(mem.p.title, 'Memory');
  assert.equal(find(mem, { t: 'section', p: { title: 'goal' } }).c[0].p.value, 'q3 plan');
  const files = topScreen(r.snapshots.files);
  assert.deepEqual(all(files, { t: 'row' }).map((x) => x.p.title), ['report.html', 'shot.png']);
  assert.ok(find(files, { t: 'button', p: { label: 'Render' } }), 'an HTML file renders');
  const put = JSON.parse(called(r, 'PUT', /\/runs\/1\/file$/)[0].body);
  assert.deepEqual(put, { path: 'report.html', content: '<h1>Q3!</h1>', version: 2 }, 'the version you loaded goes back (a conflict is a visible 409)');
  assert.equal(topScreen(r.snapshots.saved).p.subtitle, 'v3 · saved');
  const prev = topScreen(r.snapshots.render);
  const canvas = find(prev, { t: 'canvas' });
  assert.ok(canvas.p.html.startsWith('<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src \'none\';'),
    'the render preview loads nothing external');
  assert.equal(find(prev, { t: 'notice', p: { tone: 'warn' } }).p.text, '1 external resource blocked');
  const tree = topScreen(r.snapshots.tree);
  assert.equal(tree.p.title, 'plan the quarter');
  assert.equal(tree.p.subtitle, '2 nodes · 1 running · 1 blocked');
  assert.deepEqual(all(tree, { t: 'row' }).map((x) => x.p.title), ['Σ 1 200↑ 500↓', 'plan the quarter', '· research']);
  assert.equal(find(tree, { t: 'row', p: { title: '· research' } }).p.subtitle, '⛔ waiting on #3');
  assert.equal(find(tree, { t: 'button', p: { label: 'Stop' } }).p.confirm.title, 'Cancel this workflow and every run below it?');
  const set = topScreen(r.snapshots.settings);
  assert.deepEqual(all(set, { t: 'row' }).map((x) => x.p.title), ['Config', 'Features', 'Skills', 'MCP servers']);
  const cfg = topScreen(r.snapshots.config);
  assert.equal(find(cfg, { t: 'picker', p: { label: 'General' } }).p.value, 'm1');
  assert.equal(find(cfg, { t: 'toggle', p: { label: 'Subagents (expose spawn_subagent)' } }).p.value, true);
  assert.deepEqual(all(topScreen(r.snapshots.skills), { t: 'row' }).map((x) => [x.p.title, x.p.badge]), [['weekly', "bob's"]]);
});

test('a render that arrives while you look opens the preview; one you closed stays closed', async () => {
  const seed = oneSeed({ run: { title: 'report', status: 'running' }, steps: [{ id: 5, seq: 5, kind: 'render', detail: JSON.stringify({ path: 'r.html', version: 1 }), created: now - 2 }] },
    { routes: [['GET', '/runs/9/file\\?path=r.html$', { path: 'r.html', content: '<p>hi</p>', version: 1 }]] });
  const r = await run(seed, [
    { snapshot: 'opened' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { call: ['push', { type: 'step', run: 9, root: 9, data: { id: 6, seq: 6, kind: 'note', detail: '{"text":"n"}', created: now } }] },
    { snapshot: 'closed' },
  ], { state: { hash: 'c=9' } });
  assert.equal(topScreen(r.snapshots.opened).p.title, 'r.html');
  assert.ok(find(r.snapshots.opened, { t: 'canvas' }).p.html.includes('<p>hi</p>'));
  assert.equal(topScreen(r.snapshots.closed).p.title, 'report');
});

test('deep links: #join= joins, the list pages, the drawer holds settings and the brake', async () => {
  const runs = Array.from({ length: 3 }, (_, i) => ({ id: i + 1, title: 'conv ' + (i + 1), status: i ? 'idle' : 'running', activityMs: NOW - i * 1000 }));
  const conv = (r) => ({ access: 'owner', mine: true, origin: 'chat', ...r });
  const r = await run({ me: ME, runs, routes: [['POST', '/join$', { runId: 2 }],
    ['GET', '/conversations\\?scope=mine$', { pinned: [], items: runs.slice(0, 2).map(conv), next: 'c2' }],
    ['GET', '/conversations\\?scope=mine&cursor=c2$', { pinned: [], items: runs.slice(2).map(conv), next: '' }]] }, [
    { snapshot: 'joined' },
    { tap: { t: 'button', p: { label: 'Conversations' } } },
    { event: [{ t: 'list', in: { t: 'sheet' } }, 'more', {}] },
    { snapshot: 'drawer' },
    { tap: { t: 'button', p: { label: 'Halt every run' } } },
    { snapshot: 'halted' },
  ], { state: { hash: 'join=TOK_7' } });
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/join$/)[0].body), { token: 'TOK_7' });
  assert.equal(topScreen(r.snapshots.joined).p.title, 'conv 2');
  const drawer = find(r.snapshots.drawer, { t: 'sheet' });
  assert.deepEqual(all(drawer, { t: 'row', has: 'conv ' }).map((x) => x.p.title), ['conv 1', 'conv 2', 'conv 3'], '"more" reads the next page');
  const menu = all(drawer, { t: 'button', in: { t: 'menu' } }).map((b) => b.p.label);
  assert.deepEqual(menu, ['Automations', 'Settings', 'Halt every run'], 'the brake shows while something runs');
  assert.equal(find(drawer, { t: 'button', p: { label: 'Halt every run' } }).p.confirm.destructive, true);
  assert.deepEqual(JSON.parse(called(r, 'PUT', /\/halt$/)[0].body), { on: true });
  assert.ok(find(r.snapshots.halted, { t: 'notice', p: { title: 'Halted' } }), 'halted: the screen says so');
  assert.equal(find(r.snapshots.halted, { t: 'sheet' }), null, 'the drawer closed first');
});

test('live updates lost: the chat says it is reconnecting', async () => {
  const seed = oneSeed({ run: { title: 'quiet', status: 'idle' } }, { routes: [['GET', '/stream\\?', { error: 'down' }, 503]] });
  const r = await run(seed, [{ wait: 100 }, { snapshot: 'lost' }], { state: { hash: 'c=9' } });
  assert.ok(find(r.snapshots.lost, { t: 'notice', p: { text: 'live updates lost — reconnecting…' } }));
});

test('a failed action says why, where you look', async () => {
  const seed = oneSeed({ run: { title: 'halted one', status: 'idle' } }, { me: { ...ME, manager: false },
    routes: [['POST', '/runs/9/message$', { error: 'the agent is halted — a manager must resume it' }, 423]] });
  const r = await run(seed, [
    { input: [{ t: 'composer' }, 'hello?'] },
    { event: [{ t: 'composer' }, 'send', { value: 'hello?' }] },
    { snapshot: 'said' },
  ], { state: { hash: 'c=9' } });
  const tr = find(r.snapshots.said, { t: 'transcript', p: { follow: true } });
  assert.equal(tr.c[tr.c.length - 1].p.text, 'the agent is halted — a manager must resume it', 'the error is the last thing in the chat');
  assert.equal(find(r.snapshots.said, { t: 'composer' }).p.value, 'hello?', 'the text stays in the composer');
});

test('rename from the conversation\'s menu: the title follows at once', async () => {
  const r = await run(oneSeed({ run: { title: 'old name', status: 'idle' } }), [
    { tap: { t: 'button', p: { label: 'Rename…' } } },
    { input: [{ t: 'field', p: { label: 'Title' }, in: { t: 'sheet' } }, 'new name'] },
    { tap: { t: 'button', p: { label: 'Save' }, in: { t: 'sheet' } } },
  ], { state: { hash: 'c=9' } });
  assert.deepEqual(JSON.parse(called(r, 'PATCH', /\/runs\/9$/)[0].body), { title: 'new name' });
  assert.equal(topScreen(r.tree).p.title, 'new name');
  assert.equal(find(r.tree, { t: 'sheet' }), null);
});
