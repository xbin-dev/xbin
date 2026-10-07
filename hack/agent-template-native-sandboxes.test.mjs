// hack/agent-template-native-sandboxes.test.mjs — coding sandboxes (D115)
// in the agent template's native view, beyond agent-template-native.test.mjs:
// what the phase-1 review found — a re-pick keeps its working directory, a
// private sandbox into a team conversation asks first (a sheet), the
// Sandboxes screen keeps its order while open, Start through the
// conversation, and a viewer makes no sandbox for it — and sharing one with
// a terminal tile (D121). Rendered in node with
// hack/xbn/node.mjs against the web tests' fake backend, as the other file
// does. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);

async function run(seed, steps = [], { state = null } = {}) {
  const r = await runNative({ caps: PHONE, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

// all/find: nodes of a tree by type and props (a subset, compared as JSON),
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
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const bodies = (r, method, re) => called(r, method, re).map((c) => JSON.parse(c.body));
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };

const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };
const now = Math.floor(NOW / 1000);
const texts = (tree, t, prop) => all(tree.root || tree, { t }).map((n) => (n.p || {})[prop]);
const NEW_CHAT = { t: 'button', p: { label: 'New chat' } };
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 600 + id, ...extra });
const oneSeed = (view, extra = {}) => ({ me: ME, runs: [{ id: 9, title: view.run.title || 'run 9', status: view.run.status }],
  views: { 9: { access: 'owner', ...view, run: { id: 9, rootId: 9, parentId: 0, ...view.run } } }, ...extra });
const MGR = 'apps/coding-sandbox';
const CODING = { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web', 'files'], managers: 'all', sandboxEgress: ['none', 'internet'] };
const sb = (id, extra = {}) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
  caps: ['exec', 'files', 'tar', 'archive'], image: { id: 'base', title: 'Debian' }, lastActive: NOW - 60e3, ...extra });
const bound = (id, extra = {}) => ({ ref: `${MGR}|${id}`, name: id, cwd: '/work', manager: 'Coding sandboxes', egress: 'none', by: 'admin', ...extra });
const boxes = () => [sb('api', { boundTo: [9] }), sb('web', { state: 'stopped', lastActive: NOW - 3600e3 }),
  sb('wide', { egress: 'open' }), sb('team-box', { mine: false, owner: { user: 'carol' }, visibility: 'team', canManage: false, canEdit: false })];

test('coding sandboxes (D115): a re-pick keeps its cwd; a private one into a team conversation asks; the list holds its order; through the conversation', async () => {
  const view = { run: { title: 'team build', status: 'idle', visibility: 'team' }, acl: { owner: 'admin', visibility: 'team', teamRole: 'participant', members: [] },
    class: CODING, config: { sandbox: bound('api', { cwd: '/work/api' }), attached: [bound('api', { cwd: '/work/api' }), bound('web', { cwd: '/work/web' })] } };
  const sandboxes = [...boxes(), sb('private-box'),
    sb('bobs', { mine: false, owner: { user: 'bob' }, canUse: false, canManage: false, canEdit: false, state: 'stopped', boundTo: [9] })];
  const picker = { t: 'picker', p: { label: 'Sandbox' } };
  const sheet = { t: 'sheet' };
  const row = (name) => ({ t: 'row', p: { title: name }, in: { t: 'screen', p: { title: 'Sandboxes' } } });
  const r = await run(oneSeed(view, { sandboxes }), [
    { wait: 50 },
    { event: [picker, 'change', { value: `${MGR}|web` }] },
    { wait: 20 },
    { event: [picker, 'change', { value: `${MGR}|private-box` }] },
    { snapshot: 'ask' },
    { tap: { t: 'button', p: { label: 'Cancel' }, in: sheet } },
    { snapshot: 'cancelled' },
    { event: [picker, 'change', { value: `${MGR}|private-box` }] },
    { tap: { t: 'button', p: { label: 'Use it here' }, in: sheet } },
    { wait: 20 },
    { snapshot: 'used' },
    { event: [picker, 'change', { value: '+manage' }] },
    { wait: 50 },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Start' }, in: row('web') } },
    { wait: 20 },
    { snapshot: 'started' },
    { tap: { t: 'button', p: { label: 'Start' }, in: row('bobs') } },
    { wait: 20 },
  ], { state: { hash: 'c=9' } });
  const patches = bodies(r, 'PATCH', /\/runs\/9$/);
  // the picker: an attached one comes back with its own directory (review: it was reset to the workdir)
  assert.deepEqual(patches[0], { sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } });
  // a private one into a team conversation: the sheet asks, Cancel binds nothing, Use it here binds it
  const ask = find(r.snapshots.ask, sheet);
  assert.equal(ask.p.title, 'Use “private-box” here?');
  assert.match(find(ask, { t: 'notice' }).p.text, /^“private-box” is private — people in this conversation will be able to work in it/);
  assert.equal(find(r.snapshots.cancelled, sheet), null, 'Cancel closes it');
  assert.deepEqual(patches.slice(1), [{ sandbox: { ref: `${MGR}|private-box` } }], 'only Use it here bound it');
  assert.equal(find(r.snapshots.used, sheet), null);
  assert.match(topScreen(r.snapshots.used).p.subtitle, /sandbox private-box · \/work/);
  // the Sandboxes screen: Start moves nothing under the finger (review: the rows re-sorted by activity)
  const titles = (tree) => all(topScreen(tree), { t: 'row' }).map((x) => x.p.title);
  assert.deepEqual(titles(r.snapshots.list), ['api', 'private-box', 'wide', 'web', 'bobs', 'team-box']);
  assert.match(find(r.snapshots.started, row('web')).p.subtitle, /^running · /);
  assert.deepEqual(titles(r.snapshots.started), titles(r.snapshots.list), 'the order holds while it is open');
  // one bound here that you may not use: Start through the conversation (review: never offered)
  assert.deepEqual(all(find(r.snapshots.list, row('bobs')), { t: 'button' }).map((b) => b.p.label), ['Start']);
  assert.equal(called(r, 'POST', /\/sandboxes\/apps\/coding-sandbox%7Cbobs\/start\?wait=20&conversation=9$/).length, 1);
});

test('coding sandboxes: a coding agent\'s conversation keeps its sandbox — its screen offers no cwd change, switch or Detach', async () => {
  const { harnessSeed } = await import(TPL + 'test/harness-fixtures.mjs');
  const r = await run(harnessSeed(), [
    { wait: 50 },
    { tap: { t: 'button', has: '"Sandbox: api-dev' } },
    { wait: 20 },
    { snapshot: 'box' },
  ], { state: { hash: 'c=21' } });
  const box = topScreen(r.snapshots.box);
  assert.equal(box.p.title, 'api-dev');
  assert.equal(find(box, { t: 'field' }), null, 'no working directory to type');
  assert.equal(find(box, { t: 'button', p: { label: 'Set' } }), null);
  assert.equal(find(box, { t: 'button', p: { label: 'Detach' } }), null, 'no Detach (the backend refuses it)');
  const cwd = find(box, { t: 'row', p: { title: 'Working directory' } });
  assert.equal(cwd.p.subtitle, '/work/api', 'its cwd, read-only');
  assert.ok(find(box, { t: 'row', p: { title: 'Manage sandboxes…' } }), 'Manage stays');
  assert.deepEqual(called(r, 'PATCH', /\/runs\/21$/), []);
  // its sandbox gone: the way out is a new chat, not "pick another, or detach it"
  const seed = harnessSeed();
  seed.sandboxes = seed.sandboxes.filter((x) => !x.ref.endsWith('|sb-7f3a'));
  const g = await run(seed, [{ wait: 50 }, { snapshot: 'chat' }], { state: { hash: 'c=21' } });
  const n = find(g.snapshots.chat, { t: 'notice', p: { title: 'Sandbox api-dev' } });
  assert.equal(n && n.p.text, 'gone — its manager no longer has it — start a new chat with Claude Code in another sandbox');
});

test('coding sandboxes (D115): a viewer makes no sandbox for the conversation', async () => {
  const view = { access: 'viewer', run: { title: 'read only', status: 'idle' }, class: CODING, config: { sandbox: bound('api'), attached: [bound('api')] } };
  const r = await run(oneSeed(view, { sandboxes: boxes() }), [
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Sandbox: api' } } },
    { tap: { t: 'row', p: { title: 'Manage sandboxes…' } } },
    { wait: 50 },
    { snapshot: 'list' },
  ], { state: { hash: 'c=9' } });
  const list = topScreen(r.snapshots.list);
  assert.equal(list.p.title, 'Sandboxes');
  assert.equal(find(list, { t: 'button', p: { label: 'New sandbox' } }).p.disabled, true, 'review: New was offered, then refused');
  assert.match(find(list, { t: 'notice', p: { tone: 'info' } }).p.text, /^You may only read this conversation/);
});

test('coding sandboxes (D121): Share with a terminal tile… pushes its screen; Share PATCHes the shares; Stop sharing takes one away', async () => {
  const view = { run: { title: 'build', status: 'idle' }, class: CODING, config: { sandbox: bound('api'), attached: [bound('api')] } };
  const sandboxes = [sb('api', { boundTo: [9], shares: [{ consumer: 'apps/old-term', users: ['admin'] }] }), sb('team-box', { visibility: 'team', shares: [] }),
    sb('theirs', { mine: false, owner: { user: 'carol' }, canEdit: false, canManage: false, visibility: 'team' })];
  const row = (name) => ({ t: 'row', p: { title: name }, in: { t: 'screen', p: { title: 'Sandboxes' } } });
  const shareScreen = { t: 'screen', p: { title: 'Share with a terminal tile' } };
  const r = await run(oneSeed(view, { sandboxes }), [
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Sandbox: api' } } },
    { tap: { t: 'row', p: { title: 'Manage sandboxes…' } } },
    { wait: 50 },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Share with a terminal tile…' }, in: row('api') } },
    { wait: 20 },
    { snapshot: 'form' },
    { event: [{ t: 'field', p: { label: 'The terminal tile\'s path' }, in: shareScreen }, 'input', { value: 'apps/agent' }] },
    { snapshot: 'self' },
    { event: [{ t: 'field', p: { label: 'The terminal tile\'s path' }, in: shareScreen }, 'input', { value: 'apps/sandbox-terminal' }] },
    { tap: { t: 'button', p: { label: 'Share' }, in: shareScreen } },
    { wait: 20 },
    { snapshot: 'shared' },
    { tap: { t: 'button', p: { label: 'Share with a terminal tile…' }, in: row('api') } },
    { wait: 20 },
    { tap: { t: 'button', p: { label: 'Stop sharing' }, in: { t: 'row', p: { title: 'apps/old-term' } } } },
    { wait: 20 },
    { snapshot: 'stopped' },
  ], { state: { hash: 'c=9' } });
  const labels = (tree, name) => all(find(tree, row(name)), { t: 'button' }).map((b) => b.p.label);
  assert.ok(labels(r.snapshots.list, 'api').includes('Share with a terminal tile…'));
  assert.ok(labels(r.snapshots.list, 'team-box').includes('Share with a terminal tile…'));
  assert.ok(!labels(r.snapshots.list, 'theirs').includes('Share with a terminal tile…'), 'not yours: not offered');
  assert.match(find(r.snapshots.list, row('api')).p.subtitle, /shared with apps\/old-term/);
  const form = topScreen(r.snapshots.form);
  assert.equal(form.p.title, 'Share with a terminal tile');
  assert.equal(find(form, { t: 'field' }).p.value, 'apps/sandbox-terminal', 'the builtin\'s path by default');
  assert.equal(find(form, { t: 'row', p: { title: 'For' } }).p.detail, 'you');
  assert.deepEqual(all(form, { t: 'row', in: { t: 'section', p: { title: 'Shared with now' } } }).map((x) => x.p.title), ['apps/old-term']);
  const self = topScreen(r.snapshots.self);
  assert.match(find(self, { t: 'notice', p: { tone: 'danger' } }).p.text, /That is this agent/);
  assert.equal(find(self, { t: 'button', p: { label: 'Share' } }).p.disabled, true);
  const patches = bodies(r, 'PATCH', /\/sandboxes\/apps\/coding-sandbox%7Capi$/);
  assert.deepEqual(patches[0], { shares: [{ consumer: 'apps/old-term', users: ['admin'] }, { consumer: 'apps/sandbox-terminal', users: ['admin'] }] });
  const back = topScreen(r.snapshots.shared);
  assert.equal(back.p.title, 'Sandboxes', 'Share pops back to the list');
  assert.match(find(back, { t: 'notice', p: { tone: 'ok' } }).p.text, /^api is shared with apps\/sandbox-terminal/);
  assert.deepEqual(patches[1], { shares: [{ consumer: 'apps/sandbox-terminal', users: ['admin'] }] }, 'Stop sharing keeps the others');
  assert.deepEqual(all(topScreen(r.snapshots.stopped), { t: 'row', in: { t: 'section', p: { title: 'Shared with now' } } }).map((x) => x.p.title),
    ['apps/sandbox-terminal']);
});

// moved from hack/agent-template-native.test.mjs (its size cap)
test('coding sandboxes (D115): the picker, the sandbox badge and its screen, the tool cards', async () => {
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
    ['team-box · team', 'box'], ['New sandbox…', 'plus'], ['Manage sandboxes…', 'list']], 'short: the bar shows the current one beside the model');
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /idle · ▣ Coding · sandbox api · \/work/, 'the sandbox in the header, in words');
  assert.equal(called(r, 'GET', /\/sandboxes$/).length, 1, 'the list is read once');
  const note = texts(r.snapshots.refused, 'notice', 'text').find((t) => /wide/.test(t || ''));
  assert.match(note, /^wide: the Coding class doesn't allow a sandbox with open network/);
  // the tool cards: a sandbox call as a terminal, the command inside, what it came to on the card
  const bash = find(r.snapshots.chat, { t: 'toolcard', p: { title: 'Run the tests' } });
  assert.equal(bash.p.family, 'box');
  assert.equal(bash.p.icon, 'terminal');
  assert.deepEqual(bash.p.chips, [{ text: 'exit 1 · 14s · job 3', tone: 'danger' }]);
  assert.ok(find(bash, { t: 'text', p: { text: '$ go test ./...' } }), 'the command under the summary');
  assert.deepEqual(find(r.snapshots.chat, { t: 'toolcard', has: 'Search /TODO/' }).p.chips, [{ text: '2 matches' }]);
  // a pick binds it from the next turn, and the header follows
  const patches = bodies(r, 'PATCH', /\/runs\/9$/);
  assert.deepEqual(patches[0], { sandbox: { ref: `${MGR}|web`, cwd: '/work' } }, 'the refused pick sent nothing; an attached one keeps its cwd');
  assert.match(topScreen(r.snapshots.bound).p.subtitle, /sandbox web · \/work/);
  // ⋯ → Sandbox: its working directory, the attached ones, Detach
  const box = topScreen(r.snapshots.box);
  assert.equal(box.p.title, 'web');
  assert.equal(find(box, { t: 'field', p: { label: 'Working directory' } }).p.value, '/work');
  assert.deepEqual(all(find(box, { t: 'section', p: { title: 'Attached' } }), { t: 'row' }).map((x) => [x.p.title, !!x.p.selected]), [['api', false], ['web', true]]);
  assert.match(texts(r.snapshots.relative, 'notice', 'text').join('|'), /an absolute path/, 'a relative directory is refused before it is sent');
  assert.deepEqual(patches.slice(1), [{ sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } }, { sandbox: { ref: `${MGR}|api`, cwd: '/work' } }, { detach: `${MGR}|api` }]);
  assert.equal(topScreen(r.snapshots.switched).p.title, 'api', 'switching makes another the active one');
  assert.equal(topScreen(r.snapshots.detached).p.title, 'fix the build', 'detached: back on the conversation');
  assert.doesNotMatch(topScreen(r.snapshots.detached).p.subtitle, /sandbox api/);
  assert.equal(find(r.snapshots.detached, { t: 'button', p: { label: 'Sandbox: api' } }), null);
});

test('coding sandboxes (D115): no picker without the toolset; the Sandboxes screens at home; a new chat starts in the pick', async () => {
  const seed = { me: ME, runs: [{ id: 9, title: 'build it', status: 'running' }], sandboxes: boxes(),
    views: { 9: { access: 'owner', class: CODING, run: { id: 9, rootId: 9, parentId: 0, title: 'build it', status: 'running' } } },
    routes: [['POST', '/ask$', { id: 9, title: 'build it', status: 'running', rootId: 9, parentId: 0 }]] };
  const row = (name) => ({ t: 'row', p: { title: name }, in: { t: 'screen', p: { title: 'Sandboxes' } } });
  const r = await run(seed, [
    { tap: NEW_CHAT },
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
  assert.deepEqual(acts(r.snapshots.list, 'web'), ['Use for a new chat', 'Start', 'Archive', 'Share with the team', 'Share with a terminal tile…', 'Delete']);
  assert.deepEqual(acts(r.snapshots.list, 'team-box'), ['Use for a new chat', 'Stop'], 'another\'s team sandbox: use it, no managing');
  assert.deepEqual(acts(r.snapshots.list, 'wide'), ['Stop', 'Archive', 'Share with the team', 'Share with a terminal tile…', 'Delete'], 'one the class does not allow is not offered for use');
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
  assert.ok(texts(r.snapshots.created, 'notice', 'text').includes('created scratch — your next new chat starts in it'));
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
    { tap: { t: 'button', p: { label: 'Sandbox: vanished (broken)' } } },
    { snapshot: 'box' },
  ], { state: { hash: 'c=9' } });
  assert.match(topScreen(r.snapshots.chat).p.subtitle, /sandbox vanished · \/srv · broken/);
  const warn = find(r.snapshots.chat, { t: 'notice', p: { tone: 'warn', title: 'Sandbox vanished' } });
  assert.match(warn.p.text, /^gone — its manager no longer has it — pick another sandbox, or detach it/);
  assert.equal(find(r.snapshots.chat, { t: 'picker', p: { label: 'Sandbox' } }), null, 'a viewer gets no picker');
  const box = topScreen(r.snapshots.box);
  assert.equal(find(box, { t: 'notice', p: { tone: 'warn' } }).p.title, 'The binding no longer resolves');
  assert.equal(find(box, { t: 'field', p: { label: 'Working directory' } }).p.disabled, true);
  assert.equal(find(box, { t: 'button', p: { label: 'Detach' } }).p.disabled, true);
});
