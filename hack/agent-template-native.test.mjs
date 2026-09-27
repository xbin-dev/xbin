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
  assert.match(composer.p.upload.path, /^\/api\/apps\/agent\/ask\/upload\?draft=[\w-]{8,64}&name=\{name\}$/, 'at home the app uploads into the new ask\'s draft');
  const cls = find(home, { t: 'picker', p: { label: 'Class' } });
  assert.equal(cls.p.value, 'internal', 'the class for new chats, in the toolbar');
  assert.deepEqual(cls.p.options.map((o) => [o.label, o.icon]), [['Internal', 'lock'], ['Web', 'globe'], ['Coding', 'terminal']]);
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

test('a new ask with attachments from home: the app uploads into the draft, Send sends it', async () => {
  const seed = { me: ME,
    runs: [{ id: 2, title: 'which vendor?', status: 'waiting_input' }, { id: 9, title: 'look at this', status: 'running' }],
    needs: [{ reason: 'question', run: { id: 2, title: 'which vendor?' } }],
    views: { 9: { access: 'owner', run: { id: 9, rootId: 9, parentId: 0, title: 'look at this', status: 'running' } } },
    routes: [['POST', '/ask$', { id: 9, title: 'look at this', status: 'running', rootId: 9, parentId: 0 }]] };
  const r = await run(seed, [
    { snapshot: 'home' },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'shot.png', response: { path: 'shot.png', mime: 'image/png', bytes: 1234, binary: true, run: 41 } }] },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'huge.mov', response: { error: 'file too large (max 16 MiB)' } }] },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'b.txt', response: { path: 'b.txt', mime: 'text/plain', bytes: 3, binary: false, run: 41 } }] },
    { snapshot: 'attached' },
    { tap: { t: 'row', p: { title: 'which vendor?' } } },
    { snapshot: 'elsewhere' },
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { snapshot: 'back' },
    { event: [{ t: 'composer' }, 'remove', { id: '2' }] },
    { event: [{ t: 'composer' }, 'remove', { id: '3' }] },
    { event: [{ t: 'composer' }, 'send', { value: 'look at this' }] },
    { snapshot: 'sent' },
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { snapshot: 'again' },
  ]);
  const up = find(r.snapshots.home, { t: 'composer' }).p.upload;
  assert.equal(up.method, 'PUT');
  const key = /draft=([\w-]+)&/.exec(up.path)[1];
  const chips = (snap) => find(r.snapshots[snap], { t: 'composer' }).p.attachments.map((a) => a.name);
  assert.deepEqual(chips('attached'), ['shot.png', 'huge.mov — too large (max 16.0 MB)', 'b.txt']);
  assert.deepEqual(chips('elsewhere'), [], 'what was picked at home stays at home');
  assert.deepEqual(find(r.snapshots.elsewhere, { t: 'composer' }).p.upload, { method: 'PUT', path: '/api/apps/agent/runs/2/upload?name={name}' });
  assert.deepEqual(chips('back'), ['shot.png', 'huge.mov — too large (max 16.0 MB)', 'b.txt'], '…and is there when you come back');
  const asks = called(r, 'POST', /\/ask$/);
  assert.equal(asks.length, 1);
  assert.deepEqual(JSON.parse(asks[0].body), { text: 'look at this', class: 'internal', toolset: 'private', draft: key, files: ['shot.png'] }, 'Send sends the draft with the chips still there');
  assert.equal(called(r, 'PUT', /upload/).length, 0, 'the tile uploads nothing itself: the app did');
  assert.equal(topScreen(r.snapshots.sent).p.title, 'look at this', 'the new conversation opens');
  assert.deepEqual(chips('sent'), []);
  const next = find(r.snapshots.again, { t: 'composer' });
  assert.deepEqual(next.p.attachments, [], 'the sent chips are gone');
  assert.ok(!next.p.upload.path.includes(key), 'the next ask gets a draft of its own');
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

// D111: reading the owner's other conversations is theirs to allow — once or
// here for an hour; someone else may only deny. A grant in force shows in
// the header and the owner can revoke it from the menu.
test('a grant: the owner allows it once or for an hour, others may deny; it shows until revoked', async () => {
  const ask = { title: 'catch me up', status: 'waiting_input', owner: 'admin',
    pendingState: { kind: 'approval', grant: 'threads', toolCalls: [{ function: { name: 'threads_list' } }] } };
  const r = await run(oneSeed({ run: ask }), [
    { snapshot: 'asked' },
    { event: [{ t: 'approval' }, 'choose', { id: 'hour', feedback: '' }] },
  ], { state: { hash: 'c=9' } });
  const a = find(r.snapshots.asked, { t: 'approval' });
  assert.equal(a.p.title, 'The agent asks to read your other conversations and automations');
  assert.deepEqual(a.p.options.map((o) => [o.label, o.kind]), [['Allow once', 'allow_once'], ['Allow here for 1 hour', 'allow_always'], ['Deny', 'reject_once']]);
  assert.match(a.p.text, /threads_list/);
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/runs\/9\/approve$/)[0].body), { approve: true, grant: 'hour' });

  const other = await run(oneSeed({ access: 'participant', run: { ...ask, owner: 'bob' } }), [
    { snapshot: 'asked' },
    { event: [{ t: 'approval' }, 'choose', { id: 'deny', feedback: '' }] },
  ], { state: { hash: 'c=9' } });
  const o = find(other.snapshots.asked, { t: 'approval' });
  assert.deepEqual(o.p.options.map((x) => x.label), ['Deny'], 'only its owner may allow');
  assert.match(o.p.text, /Only bob can allow this/);
  assert.deepEqual(JSON.parse(called(other, 'POST', /\/runs\/9\/approve$/)[0].body), { approve: false });

  const live = { title: 'catch me up', status: 'idle', owner: 'admin',
    grants: [{ cap: 'threads', grantedBy: 'admin', expiresMs: NOW + 30 * 60e3 }, { cap: 'threads', grantedBy: 'admin', expiresMs: NOW - 1 }] };
  const g = await run(oneSeed({ run: live }), [
    { snapshot: 'granted' },
    { tap: { t: 'button', has: 'Revoke: reads your threads', in: { t: 'menu' } } },
  ], { state: { hash: 'c=9' } });
  const screen = find(g.snapshots.granted, { t: 'screen', p: { title: 'catch me up' } });
  assert.equal((screen.p.subtitle.match(/🔓 reads your threads · until \d\d:\d\d/g) || []).length, 1, 'the live one, not the expired one');
  assert.equal(called(g, 'DELETE', /\/runs\/9\/grants\/threads$/).length, 1);
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
  assert.equal(row('their one').p.subtitle, '👥 from bob · team · can read', 'a shared row says how, and whose');
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
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/ask$/)[0].body), { text: 'summarise the week', title: 'weekly', system: '', class: 'web', toolset: 'web' });
  assert.match(find(r.snapshots.form, { t: 'section', p: { title: 'Class' } }).p.footer, /^Searches and reads the web/, 'the class says what it is for');
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
  assert.deepEqual(all(set, { t: 'row' }).map((x) => x.p.title), ['Config', 'Features', 'Classes', 'Skills', 'MCP servers']);
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

test('classes (D116): the picker remembers your pick, the badge warns, managers edit them', async () => {
  const mixedView = { run: { title: 'bridged', status: 'idle' },
    class: { id: 'bridge', name: 'Bridge', icon: '🌉', description: 'both worlds', toolsets: ['internal', 'web'], mixed: true, lane: 'private' } };
  const r = await run(oneSeed(mixedView, { classes: { classes: [
    { id: 'ops', name: 'Ops', icon: '🛡', toolsets: ['internal', 'files'], who: 'managers', mcp: ['apps/pg'] }], default: 'ops' } }), [
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'New chat' } } },
    { snapshot: 'home' },
    { event: [{ t: 'picker', p: { label: 'Class' } }, 'change', { value: 'coding' }] },
    { tap: { t: 'button', p: { label: 'Settings' } } },
    { tap: { t: 'row', p: { title: 'Classes' } } },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'New class' } } },
    { input: [{ t: 'field', p: { label: 'Id' } }, 'bridge'] },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'Bridge'] },
    { event: [{ t: 'toggle', has: 'Internal systems' }, 'change', { value: true }] },
    { event: [{ t: 'toggle', has: 'Web —' }, 'change', { value: true }] },
    { snapshot: 'form' },
    { tap: { t: 'button', p: { label: 'Save' } } },
    { snapshot: 'saved' },
    { tap: { t: 'button', p: { label: 'Delete' }, in: { t: 'row', p: { title: '🛡 Ops' } } } },
    { snapshot: 'deleted' },
  ], { state: { hash: 'c=9' } });
  // the badge: the conversation's class, and the warning of a mixed one
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /idle · 🌉 Bridge · ⚠ can move internal data out/);
  assert.equal(find(r.snapshots.chat, { t: 'picker', p: { label: 'Class' } }), null, 'an open conversation\'s class is fixed: no picker');
  // the picker: the tile's default first (a manager may use a managers' class), a pick is remembered
  assert.equal(find(r.snapshots.home, { t: 'picker', p: { label: 'Class' } }).p.value, 'ops');
  assert.deepEqual(JSON.parse(called(r, 'PUT', /\/prefs\/class$/)[0].body), 'coding');
  // the list: tags, and Delete only where it means something
  const list = topScreen(r.snapshots.list);
  assert.deepEqual(all(list, { t: 'row' }).map((x) => [x.p.title, x.p.detail || '']),
    [['🔒 Internal', 'built-in'], ['🌐 Web', 'built-in'], ['▣ Coding', 'built-in'], ['🛡 Ops', 'default · managers only']]);
  assert.equal(all(list, { t: 'button', p: { role: 'destructive' } }).length, 1, 'a built-in at its default has nothing to reset');
  // the form: a mixed class warns and its Save asks first
  const form = topScreen(r.snapshots.form);
  assert.equal(find(form, { t: 'notice', p: { tone: 'warn' } }).p.title, 'This class can move internal data out');
  assert.equal(find(form, { t: 'button', p: { label: 'Save' } }).p.confirm.title, 'Save a class that can move internal data out?');
  assert.ok(find(form, { t: 'section', p: { title: 'MCP servers' } }), 'internal reach: which MCP servers');
  const puts = called(r, 'PUT', /\/classes$/).map((c) => JSON.parse(c.body));
  assert.deepEqual(puts[0].classes.map((c) => c.id), ['ops', 'bridge'], 'only the stored classes go back, and the new one');
  assert.equal(puts[0].confirmMixed, true, 'confirmed on the button');
  assert.equal(puts[0].default, 'ops');
  assert.deepEqual(puts[0].classes[1], { id: 'bridge', name: 'Bridge', icon: '', description: '', toolsets: ['files', 'repl', 'web', 'internal', 'subagents', 'skills'],
    model: '', system: '', who: 'everyone', mcp: 'all' });
  assert.equal(topScreen(r.snapshots.saved).p.title, 'Classes', 'saved: back on the list');
  assert.ok(find(r.snapshots.saved, { t: 'row', p: { title: 'Bridge' } }), 'with the new class in it');
  assert.deepEqual(puts[1].classes.map((c) => c.id), ['bridge'], 'Delete sends the rest');
  assert.equal(puts[1].default, '', 'a deleted default hands over to the backend\'s');
  assert.equal(puts[1].confirmMixed, true, 'the mixed class that stays was confirmed before');
});

// --- coding sandboxes (D115) ---------------------------------------------------------------

const MGR = 'apps/coding-sandbox';
const CODING = { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web', 'files'], managers: 'all', sandboxEgress: ['none', 'internet'] };
const sb = (id, extra = {}) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
  caps: ['exec', 'files', 'tar', 'archive'], image: { id: 'base', title: 'Debian' }, lastActive: NOW - 60e3, ...extra });
const bound = (id, extra = {}) => ({ ref: `${MGR}|${id}`, name: id, cwd: '/work', manager: 'Coding sandboxes', egress: 'none', by: 'admin', ...extra });
const boxes = () => [sb('api', { boundTo: [9] }), sb('web', { state: 'stopped', lastActive: NOW - 3600e3 }),
  sb('wide', { egress: 'open' }), sb('team-box', { mine: false, owner: { user: 'carol' }, visibility: 'team', canManage: false, canEdit: false })];
const bodies = (r, method, re) => called(r, method, re).map((c) => JSON.parse(c.body));

test('coding sandboxes (D115): the picker, the ▣ badge and its screen, the tool cards', async () => {
  const view = { run: { title: 'fix the build', status: 'idle' }, class: CODING,
    config: { sandbox: bound('api'), attached: [bound('api'), bound('web')] },
    messages: [msg(1, 'user', 'make the tests pass', { runId: 9 }), msg(2, 'assistant', '', { runId: 9, toolCalls: [
      call('b1', 'bash', { command: 'go test ./...', summary: 'Run the tests' }),
      call('b2', 'grep', { pattern: 'TODO', path: 'src' })] }),
    msg(3, 'tool', '--- FAIL: TestX\nFAIL\n[exit 1 · 14s · job 3]', { runId: 9, toolCallId: 'b1', name: 'bash' }),
    msg(4, 'tool', 'src/a.go:3: // TODO\nsrc/b.go:9: // TODO', { runId: 9, toolCallId: 'b2', name: 'grep' })] };
  const picker = { t: 'picker', p: { label: 'Sandbox' } };
  const r = await run(oneSeed(view, { sandboxes: boxes() }), [
    { wait: 50 },
    { snapshot: 'chat' },
    { event: [picker, 'change', { value: `${MGR}|wide` }] },
    { snapshot: 'refused' },
    { event: [picker, 'change', { value: `${MGR}|web` }] },
    { snapshot: 'bound' },
    { tap: { t: 'button', p: { label: 'Sandbox: web' } } },
    { snapshot: 'box' },
    { input: [{ t: 'field', p: { label: 'Working directory' } }, 'src'] },
    { tap: { t: 'button', p: { label: 'Set' } } },
    { snapshot: 'relative' },
    { input: [{ t: 'field', p: { label: 'Working directory' } }, '/work/web'] },
    { tap: { t: 'button', p: { label: 'Set' } } },
    { tap: { t: 'row', p: { title: 'api' }, in: { t: 'section', p: { title: 'Attached' } } } },
    { snapshot: 'switched' },
    { tap: { t: 'button', p: { label: 'Detach' } } },
    { snapshot: 'detached' },
  ], { state: { hash: 'c=9' } });

  // the picker: beside the model, the conversation's sandbox; what it may not use says why when picked
  const p = find(r.snapshots.chat, picker);
  assert.equal(p.p.value, `${MGR}|api`);
  assert.deepEqual(p.p.options.map((o) => [o.label, o.icon]), [
    ['No sandbox', 'minus'], ['api', 'box'], ['web (stopped)', 'box'], ['wide — unavailable', 'lock'],
    ['team-box · team', 'box'], ['＋ New sandbox…', 'plus'], ['Manage sandboxes…', 'list']], 'short: the bar shows the current one beside the model');
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /idle · ▣ Coding · ▣ api · \/work/, 'the ▣ badge in the header');
  assert.equal(called(r, 'GET', /\/sandboxes$/).length, 1, 'the list is read once');
  const note = texts(r.snapshots.refused, 'notice', 'text').find((t) => /wide/.test(t || ''));
  assert.match(note, /^wide: the Coding class doesn't allow a sandbox with open network/);
  // the tool cards: ▣ as a terminal, the command inside, what it came to on the card
  const bash = find(r.snapshots.chat, { t: 'toolcard', p: { title: 'Run the tests' } });
  assert.equal(bash.p.family, 'box');
  assert.equal(bash.p.icon, 'terminal');
  assert.deepEqual(bash.p.chips, [{ text: 'exit 1 · 14s · job 3', tone: 'danger' }]);
  assert.ok(find(bash, { t: 'text', p: { text: '$ go test ./...' } }), 'the command under the summary');
  assert.deepEqual(find(r.snapshots.chat, { t: 'toolcard', has: 'Search /TODO/' }).p.chips, [{ text: '2 matches' }]);
  // a pick binds it from the next turn, and the header follows
  const patches = bodies(r, 'PATCH', /\/runs\/9$/);
  assert.deepEqual(patches[0], { sandbox: { ref: `${MGR}|web` } }, 'the refused pick sent nothing');
  assert.match(topScreen(r.snapshots.bound).p.subtitle, /▣ web · \/work/);
  // ⋯ → Sandbox: its working directory, the attached ones, Detach
  const box = topScreen(r.snapshots.box);
  assert.equal(box.p.title, 'web');
  assert.equal(find(box, { t: 'field', p: { label: 'Working directory' } }).p.value, '/work');
  assert.deepEqual(all(find(box, { t: 'section', p: { title: 'Attached' } }), { t: 'row' }).map((x) => [x.p.title, !!x.p.selected]), [['api', false], ['web', true]]);
  assert.match(texts(r.snapshots.relative, 'notice', 'text').join('|'), /an absolute path/, 'a relative directory is refused before it is sent');
  assert.deepEqual(patches.slice(1), [{ sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } }, { sandbox: { ref: `${MGR}|api`, cwd: '/work' } }, { detach: `${MGR}|api` }]);
  assert.equal(topScreen(r.snapshots.switched).p.title, 'api', 'switching makes another the active one');
  assert.equal(topScreen(r.snapshots.detached).p.title, 'fix the build', 'detached: back on the conversation');
  assert.doesNotMatch(topScreen(r.snapshots.detached).p.subtitle, /▣ api/);
  assert.equal(find(r.snapshots.detached, { t: 'button', p: { label: 'Sandbox: api' } }), null);
});

test('coding sandboxes (D115): no picker without the toolset; the Sandboxes screens at home; a new chat starts in the pick', async () => {
  const seed = { me: ME, runs: [{ id: 9, title: 'build it', status: 'running' }], sandboxes: boxes(),
    views: { 9: { access: 'owner', class: CODING, run: { id: 9, rootId: 9, parentId: 0, title: 'build it', status: 'running' } } },
    routes: [['POST', '/ask$', { id: 9, title: 'build it', status: 'running', rootId: 9, parentId: 0 }]] };
  const row = (name) => ({ t: 'row', p: { title: name }, in: { t: 'screen', p: { title: 'Sandboxes' } } });
  const r = await run(seed, [
    { snapshot: 'home' },
    { event: [{ t: 'picker', p: { label: 'Class' } }, 'change', { value: 'coding' }] },
    { wait: 50 },
    { snapshot: 'coding' },
    { event: [{ t: 'picker', p: { label: 'Sandbox' } }, 'change', { value: '+manage' }] },
    { wait: 50 },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Start' }, in: row('web') } },
    { snapshot: 'started' },
    { tap: { t: 'button', p: { label: 'Delete' }, in: row('web') } },
    { snapshot: 'deleted' },
    { tap: { t: 'button', p: { label: 'New sandbox' } } },
    { tap: { t: 'button', p: { label: 'Create' } } },
    { snapshot: 'unnamed' },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'scratch'] },
    { event: [{ t: 'picker', p: { label: 'Network' } }, 'change', { value: 'open' }] },
    { snapshot: 'open' },
    { event: [{ t: 'picker', p: { label: 'Network' } }, 'change', { value: 'internet' }] },
    { tap: { t: 'button', p: { label: 'Create' } } },
    { snapshot: 'created' },
    { event: [{ t: 'composer' }, 'send', { value: 'build it' }] },
  ]);
  assert.equal(find(r.snapshots.home, { t: 'picker', p: { label: 'Sandbox' } }), null, 'the internal class has no sandbox: no picker');
  const p = find(r.snapshots.coding, { t: 'picker', p: { label: 'Sandbox' } });
  assert.equal(p.p.value, '', 'the coding class: the picker, no sandbox yet');
  // the list: yours first, each with what your rights allow
  const list = topScreen(r.snapshots.list);
  assert.equal(list.p.subtitle, 'for your next new chat');
  assert.ok(called(r, 'GET', /\/sandboxes\?fresh=1$/).length, 'Manage reads them afresh');
  const acts = (tree, name) => all(find(tree, row(name)), { t: 'button' }).map((b) => b.p.label);
  assert.deepEqual(acts(r.snapshots.list, 'web'), ['Use for a new chat', 'Start', 'Archive', 'Share with the team', 'Delete']);
  assert.deepEqual(acts(r.snapshots.list, 'team-box'), ['Use for a new chat', 'Stop'], 'another\'s team sandbox: use it, no managing');
  assert.deepEqual(acts(r.snapshots.list, 'wide'), ['Stop', 'Archive', 'Share with the team', 'Delete'], 'one the class does not allow is not offered for use');
  const del = find(find(r.snapshots.list, row('web')), { t: 'button', p: { label: 'Delete' } });
  assert.match(del.p.confirm.title, /^Delete the sandbox “web”\?/);
  assert.equal(del.p.role, 'destructive');
  assert.match(find(r.snapshots.list, row('web')).p.subtitle, /^stopped · private · Debian · no network · owner: you · active 1 h ago$/);
  assert.equal(find(r.snapshots.list, row('api')).p.subtitle.split(' · ')[0], 'running');
  assert.equal(called(r, 'POST', /\/sandboxes\/apps\/coding-sandbox%7Cweb\/start\?wait=20$/).length, 1);
  assert.match(find(r.snapshots.started, row('web')).p.subtitle, /^running · /);
  assert.equal(called(r, 'DELETE', /\/sandboxes\/apps\/coding-sandbox%7Cweb$/).length, 1);
  assert.equal(find(r.snapshots.deleted, row('web')), null);
  assert.ok(texts(r.snapshots.deleted, 'notice', 'text').includes('deleted web'));
  // the create form: its checks, the class's egress, then back on the list
  assert.ok(texts(r.snapshots.unnamed, 'notice', 'text').includes('Name the sandbox.'));
  const form = topScreen(r.snapshots.open);
  assert.equal(form.p.subtitle, 'for your next new chat');
  assert.deepEqual(find(form, { t: 'picker', p: { label: 'Network' } }).p.options.map((o) => o.label), ['no network', 'internet', 'open network — not in this class']);
  assert.match(texts(r.snapshots.open, 'notice', 'text').join('|'), /the Coding class doesn't allow a sandbox with open network/);
  const made = bodies(r, 'POST', /\/sandboxes$/);
  assert.deepEqual({ ...made[0], clientId: 'x' }, { name: 'scratch', provider: MGR, egress: 'internet', visibility: 'private', image: 'base', size: 'small', clientId: 'x' });
  assert.equal(topScreen(r.snapshots.created).p.title, 'Sandboxes');
  assert.ok(texts(r.snapshots.created, 'notice', 'text').includes('created scratch — your next new chat starts in it ✓'));
  assert.equal(find(r.snapshots.created, { t: 'picker', p: { label: 'Sandbox' } }).p.value, `${MGR}|sb-scratch`, 'the picker has it');
  // the new chat starts in it
  const ask = bodies(r, 'POST', /\/ask$/)[0];
  assert.equal(ask.class, 'coding');
  assert.deepEqual(ask.sandbox, { ref: `${MGR}|sb-scratch` });
});

test('coding sandboxes (D115): a binding that no longer resolves is marked; a viewer may not change it', async () => {
  const view = { access: 'viewer', run: { title: 'lost box', status: 'idle' }, class: CODING, config: { sandbox: bound('vanished', { cwd: '/srv' }) } };
  const r = await run(oneSeed(view, { sandboxes: boxes() }), [
    { wait: 50 },
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Sandbox: vanished ⚠' } } },
    { snapshot: 'box' },
  ], { state: { hash: 'c=9' } });
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /▣ vanished · \/srv ⚠/);
  const warn = find(r.snapshots.chat, { t: 'notice', p: { tone: 'warn', title: '▣ vanished' } });
  assert.match(warn.p.text, /^gone — its manager no longer has it — pick another sandbox, or detach it/);
  assert.equal(find(r.snapshots.chat, { t: 'picker', p: { label: 'Sandbox' } }), null, 'a viewer gets no picker');
  const box = topScreen(r.snapshots.box);
  assert.equal(find(box, { t: 'notice', p: { tone: 'warn' } }).p.title, 'The binding no longer resolves');
  assert.equal(find(box, { t: 'field', p: { label: 'Working directory' } }).p.disabled, true);
  assert.equal(find(box, { t: 'button', p: { label: 'Detach' } }).p.disabled, true);
});
