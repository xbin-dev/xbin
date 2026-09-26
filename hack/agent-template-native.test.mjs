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
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, setup: TPL + 'test/native-stub.mjs', seed, ...data }, steps, state });
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
  assert.equal(qn.p.schema.description, 'Which **vendor**?');
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
