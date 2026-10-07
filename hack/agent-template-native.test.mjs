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
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const now = Math.floor(NOW / 1000);

async function run(seed, steps = [], { state = null, data = {} } = {}) {
  const r = await runNative({ caps: PHONE, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed, ...data }, steps, state });
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
// the screen on top of the nav (what the person sees), and the nav's titles
// (the stack: the conversation list is always its root)
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const titles = (tree) => find(tree, { t: 'nav' }).c.map((s) => s.p.title);
// the list's New chat (its bar's first button)
const NEW_CHAT = { t: 'button', p: { label: 'New chat' } };
// the buttons of the ⋯ menu on the screen on top
const menuOf = (tree) => all(topScreen(tree), { t: 'button', in: { t: 'menu', p: { icon: 'ellipsis' } } }).map((b) => b.p.label);

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

test('the list is the root: what needs you first; New chat pushes the greeting, example asks, the pickers and the composer', async () => {
  const seed = { me: ME, runs: [{ id: 2, title: 'which vendor?', status: 'waiting_input' }],
    needs: [{ reason: 'question', run: { id: 2, title: 'which vendor?' } }, { reason: 'failed', run: { id: 3, title: 'nightly digest' } }] };
  const r = await run(seed, [
    { snapshot: 'list' },
    { tap: NEW_CHAT },
    { snapshot: 'new' },
    { tap: { t: 'row', p: { title: 'What can you do in this workspace?' } } },
    { snapshot: 'picked' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { snapshot: 'back' },
    { tap: { t: 'row', p: { title: 'which vendor?' }, in: { t: 'section', p: { title: 'Needs you' } } } },
    { snapshot: 'need' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
  ]);
  const list = r.snapshots.list;
  assert.deepEqual(titles(list), ['Agent'], 'the conversation list is the root');
  const root = topScreen(list);
  assert.equal(root.p.search, '', 'with a search field');
  assert.deepEqual(all(root, { t: 'button', in: { t: 'toolbar' } }).map((b) => b.p.label).slice(0, 1), ['New chat']);
  const sections = all(root, { t: 'section' }).map((s) => (s.p || {}).title).filter(Boolean);
  assert.equal(sections[0], 'Needs you', 'what needs you heads the list');
  const needs = find(list, { t: 'section', p: { title: 'Needs you' } });
  assert.deepEqual(needs.c.map((c) => [c.p.title, c.p.subtitle, c.p.tone]),
    [['which vendor?', 'has a question for you', 'accent'], ['nightly digest', 'failed', 'danger']]);
  assert.equal(find(list, { t: 'composer' }), null, 'no composer on the list: New chat has it');
  const nw = r.snapshots.new;
  assert.deepEqual(titles(nw), ['Agent', 'New chat'], 'New chat is pushed');
  const scr = topScreen(nw);
  assert.ok(texts(scr, 'text', 'text').includes('What do you need?'));
  const composer = find(scr, { t: 'composer' });
  assert.equal(composer.p.placeholder, 'ask anything…');
  assert.match(composer.p.upload.path, /^\/api\/apps\/agent\/ask\/upload\?draft=[\w-]{8,64}&name=\{name\}$/, 'a new chat: the app uploads into the new ask\'s draft');
  const cls = find(scr, { t: 'picker', p: { label: 'Class' } });
  assert.equal(cls.p.value, 'internal', 'the class for new chats, in the toolbar');
  assert.deepEqual(cls.p.options.map((o) => [o.label, o.icon]), [['Internal', 'lock'], ['Web', 'globe'], ['Coding', 'terminal']]);
  assert.equal(find(r.snapshots.picked, { t: 'composer' }).p.value, 'What can you do in this workspace?', 'an example fills the composer');
  assert.deepEqual(titles(r.snapshots.back), ['Agent'], 'back returns to the list');
  assert.equal(called(r, 'GET', /\/runs\/2\/view\?limit=50$/).length, 1, 'a need opens its conversation, read in pages');
  assert.deepEqual(titles(r.snapshots.need), ['Agent', 'which vendor?'], '…pushed over the list');
  assert.deepEqual(titles(r.tree), ['Agent'], 'back returns to the list');
  assert.equal(r.messages.filter((m) => m.op === 'state').pop().state.hash, '', 'the list has no address');
});

test('a conversation: the transcript from the model\'s blocks, a subagent inside its card', async () => {
  const r = await run(chatSeed(), [{ snapshot: 'open' }], { state: { hash: 'c=1' } });
  const t = r.snapshots.open;
  const scr = topScreen(t);
  assert.equal(scr.p.title, 'plan the quarter');
  assert.match(scr.p.subtitle, /running · internal/);
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

test('a deep link builds the stack: #c= is the list and the conversation; back returns to the list', async () => {
  const r = await run(chatSeed(), [
    { snapshot: 'open' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { snapshot: 'list' },
  ], { state: { hash: 'c=1' } });
  assert.deepEqual(titles(r.snapshots.open), ['Agent', 'plan the quarter']);
  assert.deepEqual(titles(r.snapshots.list), ['Agent']);
  const hashes = r.messages.filter((m) => m.op === 'state').map((m) => m.state.hash);
  assert.equal(hashes.pop(), '', 'the list has no address');
  // a subagent's link: its parents under it, once its view says them
  const r2 = await run(chatSeed(), [{ wait: 30 }, { snapshot: 'child' }, { event: [{ t: 'nav' }, 'pop', { depth: 2 }] }, { wait: 30 }], { state: { hash: 'c=2' } });
  assert.deepEqual(titles(r2.snapshots.child), ['Agent', 'plan the quarter', 'research']);
  assert.deepEqual(titles(r2.tree), ['Agent', 'plan the quarter'], 'back goes up the chain');
  assert.equal(find(topScreen(r2.tree), { t: 'composer' }) != null, true, 'the parent is the open conversation again');
});

test('a subagent opened full screen: pushed over its parent, back goes up', async () => {
  const r = await run(chatSeed(), [
    { event: [{ t: 'toolcard', p: { title: 'research' } }, 'open', {}] },
    { snapshot: 'child' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { wait: 30 },
  ], { state: { hash: 'c=1' } });
  const nav = find(r.snapshots.child, { t: 'nav' });
  assert.deepEqual(titles(r.snapshots.child), ['Agent', 'plan the quarter', 'research']);
  assert.match(nav.c[2].p.subtitle, /^in plan the quarter/);
  assert.equal(find(nav.c[1], { t: 'composer' }), null, 'the parent under it is as last seen');
  assert.equal(topScreen(r.tree).p.title, 'plan the quarter');
  assert.deepEqual(titles(r.tree), ['Agent', 'plan the quarter']);
  assert.ok(find(topScreen(r.tree), { t: 'composer' }), 'back: the parent is open again');
  assert.equal(called(r, 'GET', /\/runs\/1\/view\?limit=50$/).length, 2, 'and read again');
});

test('the stack comes back in a restarted runtime; a list row, a board row and a run push and pop', async () => {
  // the state a runtime saved: the list, a conversation, its files
  const r = await run(chatSeed(), [
    { wait: 30 },
    { snapshot: 'restored' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { wait: 30 },
    { tap: { t: 'row', p: { title: 'plan the quarter' } } },
    { wait: 30 },
    { snapshot: 'pushed' },
  ], { state: { hash: 'c=1', nav: [{ kind: 'chat', run: 1 }, { kind: 'files', run: 1 }] } });
  assert.deepEqual(titles(r.snapshots.restored), ['Agent', 'plan the quarter', 'Files']);
  assert.deepEqual(titles(r.snapshots.pushed), ['Agent', 'plan the quarter']);
  const st = r.messages.filter((m) => m.op === 'state').pop().state;
  assert.deepEqual(st, { hash: 'c=1', nav: [{ kind: 'chat', run: 1 }] }, 'the stack is saved with the address');
});

test('a restored stack whose conversation is gone comes back as the list, with no error', async () => {
  // the saved stack names a conversation deleted since (found on a device:
  // the list then said "no such run")
  const gone = () => ({ ...chatSeed(), routes: [['GET', '/runs/99/view', { error: 'no such run' }, 404]] });
  const r = await run(gone(), [{ wait: 50 }], { state: { hash: 'c=99', nav: [{ kind: 'chat', run: 99 }] } });
  assert.deepEqual(titles(r.tree), ['Agent'], 'the gone conversation left the stack');
  assert.equal(find(r.tree, { t: 'notice', p: { tone: 'danger' } }), null, 'and nothing says it failed');
  // a link to it, asked for, still says so
  const l = await run(gone(), [{ wait: 50 }], { state: { hash: 'c=99' } });
  assert.ok(find(l.tree, { t: 'notice', p: { tone: 'danger' } }), 'a deep link to a gone conversation says why it did not open');
  // …and one whose automation is gone comes back on the Automations page
  const a = await run({ ...gone(), routes: [['GET', '/automations/schedule/99/runs', { error: 'no such automation' }, 404]] }, [{ wait: 50 }],
    { state: { hash: 'auto=schedule:99', nav: [{ kind: 'auto', at: { kind: 'schedule', id: 99 } }] } });
  assert.deepEqual(titles(a.tree), ['Agent', 'Automations'], 'the page, without the gone automation');
  assert.equal(find(a.tree, { t: 'notice', p: { tone: 'danger' } }), null, 'and nothing says it failed');
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
    { tap: NEW_CHAT },
    { snapshot: 'home' },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'shot.png', response: { path: 'shot.png', mime: 'image/png', bytes: 1234, binary: true, run: 41 } }] },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'huge.mov', response: { error: 'file too large (max 16 MiB)' } }] },
    { event: [{ t: 'composer' }, 'uploaded', { name: 'b.txt', response: { path: 'b.txt', mime: 'text/plain', bytes: 3, binary: false, run: 41 } }] },
    { snapshot: 'attached' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'row', p: { title: 'which vendor?' }, in: { t: 'section', p: { title: 'Needs you' } } } },
    { snapshot: 'elsewhere' },
    { tap: { t: 'button', p: { label: 'New chat' }, in: { t: 'screen', p: { title: 'which vendor?' } } } },
    { snapshot: 'back' },
    { event: [{ t: 'composer' }, 'remove', { id: '2' }] },
    { event: [{ t: 'composer' }, 'remove', { id: '3' }] },
    { event: [{ t: 'composer' }, 'send', { value: 'look at this' }] },
    { snapshot: 'sent' },
    { tap: { t: 'button', p: { label: 'New chat' }, in: { t: 'screen', p: { title: 'look at this' } } } },
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
  assert.deepEqual(titles(r.snapshots.sent), ['Agent', 'which vendor?', 'look at this'], '…in the new chat screen\'s place: back returns where it was started from');
  assert.deepEqual(titles(r.snapshots.again), ['Agent', 'which vendor?', 'look at this', 'New chat']);
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
  assert.equal((screen.p.subtitle.match(/reads your threads · until \d\d:\d\d/g) || []).length, 1, 'the live one, not the expired one');
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
  assert.deepEqual(find(t, { t: 'step' }).p, { glyph: '!', tone: 'danger', text: 'model unreachable' });
  assert.equal(called(r, 'POST', /\/runs\/9\/resume$/).length, 1);
  const menu = menuOf(t);
  assert.deepEqual(menu, ['Retry', 'Rename…', 'Compact', 'Learn skill', 'Memory (0)', 'Files (0)', 'Share', 'Delete']);
  assert.deepEqual(find(topScreen(t), { t: 'button', p: { label: 'Delete' } }).p.confirm, { title: 'Delete this run and its history?', label: 'Delete', destructive: true });
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
  const menu = menuOf(t);
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

test('the list: groups, row actions, search with snippets, scope, a pasted invite link', async () => {
  const day = 86400000;
  const seed = { me: ME, runs: [
    { id: 1, title: 'today one', status: 'running', activityMs: NOW - 1000, readMs: 0 },
    { id: 2, title: 'pinned one', status: 'idle', activityMs: NOW - 3 * day, pinnedAt: NOW - 10, readMs: NOW },
    { id: 3, title: 'old one', status: 'error', activityMs: NOW - 40 * day, readMs: NOW },
    { id: 4, title: 'their one', status: 'waiting_input', activityMs: NOW - day, access: 'viewer', mine: true, owner: 'bob', visibility: 'team', readMs: NOW },
  ], routes: [['POST', '/join$', { runId: 4 }]] };
  const LIST = { t: 'screen', p: { title: 'Agent' } };
  const r = await run(seed, [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Pin' }, in: { t: 'row', p: { title: 'today one' } } } },
    { snapshot: 'pinned' },
    { event: [LIST, 'search', { value: 'old' }] },
    { wait: 250 },
    { snapshot: 'searched' },
    { event: [LIST, 'search', { value: '' }] },
    { wait: 250 },
    { event: [{ t: 'picker', in: LIST }, 'change', { value: 'archived' }] },
    { snapshot: 'archived' },
    { event: [LIST, 'search', { value: 'https://x/c/apps/agent/#join=TOK_1' }] },
  ]);
  const t = r.snapshots.open;
  const sheet = topScreen(t);
  assert.equal(sheet.p.title, 'Agent', 'the list is the root screen');
  const sections = all(sheet, { t: 'section' }).map((s) => (s.p || {}).title || '');
  assert.deepEqual(sections, ['', 'Pinned', 'Today', 'Yesterday', 'Older']);
  const row = (title) => find(sheet, { t: 'row', p: { title } });
  assert.equal(row('today one').p.badge, 'working');
  assert.equal(row('old one').p.badge, 'failed');
  assert.equal(row('old one').p.tone, 'danger');
  assert.equal(row('their one').p.badge, 'waiting for you');
  assert.equal(row('their one').p.subtitle, 'from bob · team · can read', 'a shared row says how, and whose');
  assert.deepEqual(row('today one').c.map((a) => [(a.p || {}).edge || 'trailing', all(a, { t: 'button' }).map((b) => b.p.label)]),
    [['leading', ['Pin']], ['trailing', ['Rename', 'Share…', 'Archive', 'Delete']]], 'rev 2: Pin swipes in from the leading edge');
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
  assert.deepEqual(titles(r.tree), ['Agent', 'their one'], '…pushed over the list');
});

test('new chat with options, rename and share sheets', async () => {
  const seed = { me: ME, runs: [{ id: 1, title: 'plan', status: 'idle' }], routes: [
    ['POST', '/ask$', { id: 5, title: 'new one', status: 'running', rootId: 5 }],
    ['GET', '/runs/1/members$', { owner: 'admin', visibility: 'private', teamRole: '', members: [{ user: 'bob', role: 'viewer' }], links: [{ id: 3, role: 'viewer', uses: 1 }] }],
    ['POST', '/runs/1/links$', { id: 4, token: 'TOKEN_9' }],
  ] };
  const r = await run(seed, [
    { tap: NEW_CHAT },
    { tap: { t: 'button', p: { label: 'New chat with options…' } } },
    { input: [{ t: 'field', in: { t: 'section', p: { title: 'First message' } } }, 'summarise the week'] },
    { event: [{ t: 'picker', in: { t: 'sheet', p: { title: 'New chat' } } }, 'change', { value: 'web' }] },
    { input: [{ t: 'field', p: { label: 'Title' } }, 'weekly'] },
    { snapshot: 'form' },
    { tap: { t: 'button', p: { label: 'Start' } } },
    { snapshot: 'started' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { tap: { t: 'button', p: { label: 'Share…' }, in: { t: 'row', p: { title: 'plan' } } } },
    { snapshot: 'share' },
    { tap: { t: 'button', p: { label: 'Create link' } } },
    { snapshot: 'linked' },
  ]);
  assert.equal(find(r.snapshots.form, { t: 'sheet' }).p.title, 'New chat');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/ask$/)[0].body), { text: 'summarise the week', title: 'weekly', system: '', class: 'web', toolset: 'web' });
  assert.match(find(r.snapshots.form, { t: 'section', p: { title: 'Class' } }).p.footer, /^Searches and reads the web/, 'the class says what it is for');
  assert.equal(find(r.snapshots.started, { t: 'sheet' }), null);
  assert.equal(titles(r.snapshots.started).length, 2, 'the new chat takes the new chat screen\'s place');
  assert.notEqual(titles(r.snapshots.started)[1], 'New chat');
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
    { event: [{ t: 'picker', p: { label: 'Class' }, in: { t: 'screen', p: { title: 'New schedule' } } }, 'change', { value: 'coding' }] },
    { snapshot: 'form' },
    { tap: { t: 'button', p: { label: 'Create' } } },
    { snapshot: 'created' },
  ], { state: { hash: 'auto=schedule:3' } });
  const d = r.snapshots.detail;
  const nav = find(d, { t: 'nav' });
  assert.deepEqual(nav.c.map((s) => s.p.title), ['Agent', 'Automations', 'digest'], 'a deep link to one automation');
  const detail = topScreen(d);
  assert.ok(find(detail, { t: 'row', p: { title: 'every day at 9:00' } }));
  assert.equal(find(detail, { t: 'row', p: { title: 'Class' } }).p.detail, '🔒 Internal', 'a schedule from before classes: its lane\'s built-in');
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
  assert.deepEqual(find(topScreen(r.snapshots.form), { t: 'picker', p: { label: 'Class' } }).p.options.map((o) => o.value), ['internal', 'web', 'coding'],
    'the classes you may use');
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/schedules$/)[0].body), { name: 'standup', cron: '0 9 * * *', goal: 'post the standup', mode: 'isolated',
    visibility: 'private', watcher: false, class: 'coding', toolset: 'web', targetRun: 0 }, 'the class, and its lane as the legacy toolset');
  assert.equal(topScreen(r.snapshots.created).p.title, 'standup', 'a new schedule opens');

  // a trigger: its events and actions; the form refuses a lane/data-class clash
  const t = await run(seed, [
    { snapshot: 'trigger' },
    { tap: { t: 'button', p: { label: 'Edit' } } },
    { event: [{ t: 'picker', p: { label: 'Class' }, in: { t: 'screen', p: { title: 'Edit trigger' } } }, 'change', { value: 'web' }] },
    { event: [{ t: 'picker', p: { label: 'The data it takes' } }, 'change', { value: 'private' }] },
    { snapshot: 'clash' },
  ], { state: { hash: 'auto=trigger:6' } });
  const tr = topScreen(t.snapshots.trigger);
  assert.equal(tr.p.title, 'deploys');
  assert.deepEqual(all(find(tr, { t: 'section', p: { title: 'Recent events' } }), { t: 'row' }).map((x) => [x.p.title, x.p.detail]),
    [['deploy.prod', 'ran #20'], ['deploy.dev', 'over its hourly cap']]);
  assert.ok(find(tr, { t: 'notice', p: { tone: 'info' } }).p.text.startsWith('Pushes come from apps/webhooks'));
  assert.ok(find(tr, { t: 'button', p: { label: 'Fire a test event' } }));
  assert.equal(find(tr, { t: 'row', p: { title: 'Class' } }).p.detail, '🔒 Internal');
  const form = topScreen(t.snapshots.clash);
  assert.equal(form.p.title, 'Edit trigger');
  assert.equal(find(form, { t: 'button', p: { label: 'Save' } }).p.disabled, true, 'the firewall holds Save');
  assert.match(find(form, { t: 'notice', p: { tone: 'danger' } }).p.text, /^A class that reaches outside/);

  // a channel to claim: its rules and Claim
  const c = await run(seed, [{ snapshot: 'channel' }], { state: { hash: 'auto=channel:5' } });
  const ch = topScreen(c.snapshots.channel);
  assert.ok(find(ch, { t: 'picker', p: { label: 'Direct messages' } }));
  assert.ok(find(ch, { t: 'button', p: { label: 'Claim' } }));
  const web = find(ch, { t: 'picker', p: { label: 'Everyone else\'s class' } });
  assert.deepEqual([web.p.value, web.p.options.map((o) => o.value)], ['web', ['web', 'coding']], 'everyone else\'s: web-lane classes only');
  assert.equal(find(ch, { t: 'picker', p: { label: 'Trusted people\'s class' } }), null, 'no trusted class without the private lane');

  // a run is pushed over its automation; back returns to it (D190)
  const rr = await run(seed, [
    { tap: { t: 'row', p: { title: 'a digest' }, in: { t: 'section', p: { title: 'Runs' } } } },
    { wait: 30 },
    { snapshot: 'run' },
    { event: [{ t: 'nav' }, 'pop', { depth: 3 }] },
    { wait: 30 },
    { snapshot: 'back' },
  ], { state: { hash: 'auto=schedule:3' } });
  assert.deepEqual(titles(rr.snapshots.run), ['Agent', 'Automations', 'digest', 'a digest']);
  assert.ok(find(topScreen(rr.snapshots.run), { t: 'composer' }), 'the run, open');
  assert.deepEqual(titles(rr.snapshots.back), ['Agent', 'Automations', 'digest'], 'back: the automation, still open');
  const hashes = rr.messages.filter((m) => m.op === 'state').map((m) => m.state.hash);
  assert.ok(hashes.includes('c=20'), 'the run\'s address on the way');
  assert.equal(hashes.pop(), 'auto=schedule:3', 'and the automation\'s again');
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
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { tap: { t: 'button', p: { label: 'Files (1)' } } },
    { snapshot: 'files' },
    { tap: { t: 'row', p: { title: 'report.html' } } },
    { input: [{ t: 'field', p: { label: 'Content' } }, '<h1>Q3!</h1>'] },
    { tap: { t: 'button', p: { label: 'Save' } } },
    { snapshot: 'saved' },
    { tap: { t: 'button', p: { label: 'Render' } } },
    { snapshot: 'render' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { tap: { t: 'button', p: { label: 'Workflow tree' } } },
    { snapshot: 'tree' },
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { snapshot: 'chat' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
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
  assert.equal(find(tree, { t: 'row', p: { title: '· research' } }).p.subtitle, 'waiting on #3');
  assert.equal(find(tree, { t: 'button', p: { label: 'Stop' } }).p.confirm.title, 'Cancel this workflow and every run below it?');
  assert.deepEqual(titles(r.snapshots.tree), ['Agent', 'plan the quarter', 'plan the quarter'], 'the tree over its conversation');
  assert.deepEqual(titles(r.snapshots.chat), ['Agent', 'plan the quarter'], 'back to the conversation');
  assert.deepEqual(titles(r.snapshots.settings), ['Agent', 'Settings'], 'settings from the list\'s menu, over the list');
  const set = topScreen(r.snapshots.settings);
  assert.deepEqual(all(set, { t: 'row' }).map((x) => x.p.title), ['Config', 'Features', 'Classes', 'Coding agents', 'Skills', 'MCP servers']);
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
    { event: [{ t: 'nav' }, 'pop', { depth: 2 }] },
    { call: ['push', { type: 'step', run: 9, root: 9, data: { id: 6, seq: 6, kind: 'note', detail: '{"text":"n"}', created: now } }] },
    { snapshot: 'closed' },
  ], { state: { hash: 'c=9' } });
  assert.deepEqual(titles(r.snapshots.opened), ['Agent', 'report', 'r.html']);
  assert.ok(find(r.snapshots.opened, { t: 'canvas' }).p.html.includes('<p>hi</p>'));
  assert.equal(topScreen(r.snapshots.closed).p.title, 'report');
});

test('deep links: #join= joins, the list pages, its menu holds settings and the brake', async () => {
  const runs = Array.from({ length: 3 }, (_, i) => ({ id: i + 1, title: 'conv ' + (i + 1), status: i ? 'idle' : 'running', activityMs: NOW - i * 1000 }));
  const conv = (r) => ({ access: 'owner', mine: true, origin: 'chat', ...r });
  const r = await run({ me: ME, runs, routes: [['POST', '/join$', { runId: 2 }],
    ['GET', '/conversations\\?scope=mine$', { pinned: [], items: runs.slice(0, 2).map(conv), next: 'c2' }],
    ['GET', '/conversations\\?scope=mine&cursor=c2$', { pinned: [], items: runs.slice(2).map(conv), next: '' }]] }, [
    { snapshot: 'joined' },
    { event: [{ t: 'nav' }, 'pop', { depth: 1 }] },
    { event: [{ t: 'list' }, 'more', {}] },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Halt every run' } } },
    { snapshot: 'halted' },
  ], { state: { hash: 'join=TOK_7' } });
  assert.deepEqual(JSON.parse(called(r, 'POST', /\/join$/)[0].body), { token: 'TOK_7' });
  assert.equal(topScreen(r.snapshots.joined).p.title, 'conv 2');
  assert.deepEqual(titles(r.snapshots.joined), ['Agent', 'conv 2'], 'the list under it');
  const list = topScreen(r.snapshots.list);
  assert.deepEqual(all(list, { t: 'row', has: 'conv ' }).map((x) => x.p.title), ['conv 1', 'conv 2', 'conv 3'], '"more" reads the next page');
  assert.deepEqual(menuOf(r.snapshots.list), ['Automations', 'Projects', 'Settings', 'Halt every run'], 'the brake shows while something runs');
  assert.equal(find(list, { t: 'button', p: { label: 'Halt every run' } }).p.confirm.destructive, true);
  assert.deepEqual(JSON.parse(called(r, 'PUT', /\/halt$/)[0].body), { on: true });
  assert.ok(find(topScreen(r.snapshots.halted), { t: 'notice', p: { title: 'Halted' } }), 'halted: the list says so');
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
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /idle · 🌉 Bridge · can move internal data out/);
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

// The coding sandboxes' tests (D115) are hack/agent-template-native-sandboxes.test.mjs's.
