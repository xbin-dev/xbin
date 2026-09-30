// hack/agent-template-harness-term.test.mjs — terminals and a coding
// agent's sign-in in the agent template (plans/agtt-harness.md §8 U5): the
// dock's tabs and the sign-in card in words (model/terminals.js), and the
// native view over the harness fixtures — the sign-in notice, button and
// menu items only for a login park, the Sign in screen (the terminal
// login through the run's relay and Retry, an API key in a secure field
// that is never a prop, a device code, the shared-HOME confirm, whom to
// ask), a shell at the agent's cwd, and a sandbox's terminal through the
// tile's relay. The web's are test/terminal.mjs and test/harness-term.mjs.
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const T = await import(new URL('model/terminals.js', TPL));
const S = await import(new URL('model/sandboxes.js', TPL));
const { harnessSeed, SBX, API_DEV } = await import(new URL('test/harness-fixtures.mjs', TPL));

// --- the dock -----------------------------------------------------------------------

test('the dock: tabs open, show, hide behind a pill, close to a neighbour; a New shell in place', () => {
  let n = 0;
  const d = T.createTerms(() => n++);
  assert.equal(d.open({ ref: 'x' }), null, 'no src: no tab');
  const a = d.open({ ref: API_DEV, id: 'sb-7f3a', name: 'api-dev', cwd: '/work/api', src: '/m/tty?cwd=%2Fwork%2Fapi', base: '/m', manager: 'Coding sandboxes', shown: true, why: '' });
  assert.deepEqual([a.key, a.purpose, a.gen, a.session, a.ended, 'shown' in a], [1, 'shell', 1, '', '', false], 'only a tab\'s own fields');
  const b = d.open({ ref: API_DEV, name: 'api-dev', src: '/m/tty?cmd=codex%20login', purpose: 'login', run: 24, harness: 'Codex' });
  assert.equal(d.active, b.key, 'a new tab is shown');
  assert.deepEqual([T.tabLabel(a), T.tabLabel(b)], ['▣ api-dev', 'Sign in · Codex']);
  const hb = T.tabHead(b);
  assert.deepEqual([hb.title, hb.where, hb.retry, hb.done], ['Sign in · Codex · ▣ api-dev', 'its workdir', 'Retry Codex', '']);
  assert.match(hb.hint, /copy it from the terminal/);
  assert.deepEqual([T.tabHead(a).title, T.tabHead(a).where, T.tabHead(a).retry, T.tabHead(a).hint], ['▣ api-dev', '/work/api', '', '']);
  d.hide();
  assert.deepEqual([d.hidden, d.pill()], [true, '2 terminals']);
  d.select(a.key);
  assert.deepEqual([d.hidden, d.current, d.pill()], [false, a, ''], 'showing a tab shows the dock');
  d.session(b.key, 'e7');
  d.ended(b.key);
  d.ended(b.key, 'again');
  assert.equal(b.ended, 'ended', 'ended once');
  assert.equal(T.tabHead(b).done, 'Finished. Signed in?');
  d.again(b.key, { src: '/m/tty', base: '/m' });
  assert.deepEqual([b.purpose, b.run, b.harness, b.gen, b.session, b.ended, b.src], ['shell', 0, '', 2, '', '', '/m/tty'], 'a New shell: the login shell, a fresh element');
  d.toggleMax();
  const c = d.open({ name: 'web', src: '/m/tty2' });
  d.select(b.key);
  assert.equal(d.close(b.key), b, 'close answers the tab (the view ends its shell)');
  assert.equal(d.active, c.key, 'the neighbour after it');
  d.close(c.key);
  assert.equal(d.active, a.key, 'else the one before');
  d.hide();
  d.close(a.key);
  assert.deepEqual([d.tabs.length, d.current, d.hidden, d.max, d.pill()], [0, null, false, false, ''], 'empty: nothing hidden, nothing max');
  assert.equal(d.close(99), null);
  assert.ok(n > 8, 'each change is said');
  const app = { emitted: [], emit(t) { this.emitted.push(t); } };
  assert.equal(T.termsOf(app), T.termsOf(app), 'one dock per page');
  T.termsOf(app).open({ src: '/x' });
  assert.deepEqual(app.emitted, ['terms']);
  assert.equal(T.runTerminalSrc(24), 'runs/24/harness/terminal');
  assert.equal(T.runTerminalSrc(24, { login: true }), 'runs/24/harness/terminal?login=1');
});

// --- the sign-in card ---------------------------------------------------------------------

const seed = harnessSeed();
const view = (id, over = {}) => ({ ...seed.views[id], run: { ...seed.views[id].run, ...over } });
const list = (extra = []) => S.listOf({ sandboxes: [...seed.sandboxes, ...extra], managers: [] });

test('the sign-in card: only for a login park; what it offers and says', () => {
  for (const id of [21, 22, 23, 25]) assert.equal(T.signIn(seed.views[id]), null, `run ${id}: not a login park`);
  assert.equal(T.signIn(view(24, { status: 'running' })), null, 'no longer waiting');
  const c = T.signIn(seed.views[24]);
  assert.deepEqual([c.run, c.park, c.name, c.command, c.shared, c.canUse, c.ask, c.device], [24, 'Xq3login', 'Codex', 'codex login', true, null, '', null]);
  assert.deepEqual(c.sandbox, { ref: API_DEV, name: 'api-dev', cwd: '/work/api' });
  assert.deepEqual(c.methods.map((m) => m.kind), ['terminal', 'api-key', 'device-code']);
  assert.equal(c.title, 'Codex needs you to sign in (in ▣ api-dev).');
  assert.match(c.warn, /^The credentials land in api-dev's home: anyone who may use it acts as you with Codex there/);
  assert.equal(c.confirmLabel, 'api-dev is shared — sign in anyway');
  assert.equal(T.signIn(seed.views[24], { list: list() }).canUse, true, 'a sandbox you may use');

  // someone else's: whom to ask
  const theirs = { ref: `${SBX}|sb-b0b`, provider: SBX, id: 'sb-b0b', name: 'bobs', state: 'running', canUse: false, owner: { user: 'bob' }, visibility: 'private' };
  const v = view(24);
  v.run.harness = { ...v.run.harness, sandbox: { ref: theirs.ref, name: 'bobs', cwd: '/w', shared: false } };
  v.config = { ...v.config, sandbox: { ref: theirs.ref, name: 'bobs', cwd: '/w', by: 'carol' } };
  const t = T.signIn(v, { list: list([theirs]) });
  assert.deepEqual([t.canUse, t.ask, t.shared], [false, 'Ask carol to sign in — the sandbox is theirs.', false], 'the binder, else the owner');
  delete v.config.sandbox.by;
  assert.equal(T.signIn(v, { list: list([theirs]) }).ask, 'Ask bob to sign in — the sandbox is theirs.');
  assert.equal(T.signIn(v, { list: list() }).canUse, false, 'not in the list read: you may not use it');
  // shared by its row when the summary doesn't say
  assert.equal(T.signIn(v, { list: list([{ ...theirs, canUse: true, visibility: 'team' }]) }).shared, true);
  assert.equal(T.signIn(v, { list: list([{ ...theirs, canUse: true, shares: [{ consumer: 'apps/sandbox-terminal' }] }]) }).shared, true);

  // no methods: its login command in a terminal; no command in the park: the catalog's
  const bare = view(24);
  bare.run.pendingState = { kind: 'login', park: 'p', harness: { login: {} } };
  bare.run.harness = { ...bare.run.harness, login: undefined };
  assert.deepEqual(T.signIn(bare).methods, [], 'nothing to offer');
  const viaCatalog = T.signIn(bare, { entry: { login: { command: 'codex login' } } });
  assert.deepEqual([viaCatalog.command, viaCatalog.methods], ['codex login', [{ id: '', name: 'Sign in to Codex', kind: 'terminal' }]]);
  // a device code waiting (the summary's, as a reload finds it); an unknown kind is left out
  const dev = view(24);
  dev.run.harness = { ...dev.run.harness, login: { ...dev.run.harness.login, device: { url: 'https://example.invalid/device', message: 'Enter code AB12-CD34' } } };
  dev.run.pendingState = { ...dev.run.pendingState, harness: { login: { ...dev.run.pendingState.harness.login, methods: [...dev.run.pendingState.harness.login.methods, { id: 'x', name: 'X', kind: 'oauth' }] } } };
  const cd = T.signIn(dev);
  assert.deepEqual(cd.device, { url: 'https://example.invalid/device', message: 'Enter code AB12-CD34' });
  assert.equal(cd.methods.length, 3);
  assert.deepEqual(cd.methods.map(T.methodLabel), ['Sign in with ChatGPT — in a terminal', 'OpenAI API key', 'Sign in with a device code']);
  assert.deepEqual(['https://a.b/c', 'http://a.b', 'javascript:alert(1)', 'https://a b'].map(T.isHttps), [true, false, false, false]);
});

// --- the native view -------------------------------------------------------------------------

const TTY_MGR = { provider: SBX, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none', 'internet'],
  images: [{ id: 'base', title: 'Debian', default: true }], sizes: [], limits: {} };

async function run(steps, hash, mutate = (s) => s) {
  const s = harnessSeed();
  s.sbxManagers = [TTY_MGR];
  const r = await runNative({ entry: new URL('native.js', TPL).pathname,
    data: { now: Date.UTC(2026, 8, 30, 12), self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: mutate(s) },
    steps, state: hash ? { hash } : null });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
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
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };
const posts = (r, re) => r.calls.filter((c) => c.method === 'POST' && re.test(c.url)).map((c) => (c.body ? JSON.parse(c.body) : null));

test('native: a login park — the notice, Sign in in the composer and the menu; the Sign in screen; a login terminal and Retry', async () => {
  const signInBtn = { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } };
  const r = await run([
    { wait: 50 },
    { snapshot: 'chat' },
    { tap: signInBtn },
    { snapshot: 'screen' },
    { event: [{ t: 'toggle' }, 'change', { value: true }] },
    { snapshot: 'confirmed' },
    { tap: { t: 'row', p: { title: 'Open a login terminal' } } },
    { snapshot: 'term' },
    { tap: { t: 'button', p: { label: 'Signed in? Retry Codex' } } },
    { wait: 20 },
    { snapshot: 'retried' },
  ], 'c=24');
  const chat = r.snapshots.chat;
  const notice = find(chat, { t: 'notice', p: { title: 'Sign in to Codex' } });
  assert.ok(notice, 'the notice in the transcript');
  assert.equal(notice.p.text, 'Codex needs you to sign in (in ▣ api-dev). Tap Sign in below.');
  assert.ok(find(chat, { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } }), 'Sign in in the composer');
  assert.ok(find(chat, { t: 'button', p: { label: 'Sign in…' }, in: { t: 'menu' } }), '…and the menu');
  assert.ok(find(chat, { t: 'button', p: { label: 'Terminal' }, in: { t: 'menu' } }), 'a shell at its cwd in the menu');
  assert.equal(find(chat, { t: 'approval' }), null, 'no built-in card for it');

  const scr = topScreen(r.snapshots.screen);
  assert.deepEqual([scr.p.title, scr.p.subtitle], ['Sign in to Codex', '▣ api-dev']);
  assert.match(find(scr, { t: 'notice', p: { tone: 'warn' } }).p.text, /credentials land in api-dev's home/);
  assert.equal(find(scr, { t: 'toggle' }).p.label, 'api-dev is shared — sign in anyway');
  assert.equal(find(scr, { t: 'row', p: { title: 'Open a login terminal' } }).p.disabled, true, 'shared: the confirm first');
  assert.equal(find(scr, { t: 'field', p: { kind: 'secure' } }).p.label, 'OpenAI API key');
  assert.ok(find(topScreen(r.snapshots.confirmed), { t: 'row', p: { title: 'Open a login terminal' } }).p.disabled !== true, 'confirmed: offered');

  const term = topScreen(r.snapshots.term);
  assert.deepEqual([term.p.title, term.p.subtitle], ['Sign in · Codex', '▣ api-dev']);
  assert.deepEqual(find(term, { t: 'terminal' }).p, { src: 'runs/24/harness/terminal?login=1', title: 'Sign in · Codex' }, 'the run\'s relay, tile-relative');
  assert.deepEqual(posts(r, /\/runs\/24\/resume$/), [null], 'Retry: POST /resume');
  assert.equal(find(r.snapshots.retried, { t: 'terminal' }), null, '…and the login terminal goes');
});

test('native: an API key is sent once and is never a prop; a device code; the confirm the backend asks for', async () => {
  const KEY = 'sk-live-native-0123456789';
  const push = (label) => ({ tap: { t: 'button', p: { label }, in: { t: 'composer' } } });
  const r = await run([
    { wait: 50 },
    push('Sign in'),
    { event: [{ t: 'toggle' }, 'change', { value: true }] },
    { tap: { t: 'button', p: { label: 'Sign in with a device code' } } },
    { wait: 20 },
    { snapshot: 'device' },
    { input: [{ t: 'field', p: { kind: 'secure' } }, KEY] },
    { snapshot: 'typed' },
    { tap: { t: 'button', p: { label: 'Sign in' }, in: { t: 'section', p: { title: 'OpenAI API key' } } } },
    { wait: 20 },
    { snapshot: 'sent' },
  ], 'c=24');
  const d = topScreen(r.snapshots.device);
  const open = find(d, { t: 'row', p: { title: 'Open the sign-in page' } });
  assert.equal(open.p.subtitle, 'https://example.invalid/device', 'the page to open');
  assert.ok(find(d, { t: 'text', has: 'FAKE-1234' }), 'the code');
  assert.equal(find(d, { t: 'button', p: { label: 'Copy the code' } }).p.copy, 'FAKE-1234');
  assert.ok(find(r.snapshots.device, { t: 'markdown', has: 'https://example.invalid/device' }), 'the transcript shows the page too');
  assert.deepEqual(posts(r, /\/harness\/authenticate$/), [{ method: 'device-code', confirm: true }, { method: 'openai-api-key', apiKey: KEY, confirm: true }],
    'the key once, with the confirm');
  // (while typed, the field holds it: the app's own state, not a prop the tile drew)
  assert.equal(JSON.stringify(r.snapshots.typed).includes(KEY), true, 'the runner played the typing');
  assert.equal(JSON.stringify(r.snapshots.sent).includes(KEY), false, 'sent: the field is emptied, the key in no prop');
  assert.equal(JSON.stringify(r.messages).includes(KEY), false, 'the tile never drew it: in no mount or patch the app was sent');
  assert.equal(find(r.snapshots.sent, { t: 'notice', p: { title: 'Sign in to Codex' } }), null, 'signed in: the notice goes');

  // a sandbox the view thought private, which the backend says is shared
  const r2 = await run([
    { wait: 50 },
    { call: ['route', 'POST', '/runs/24/harness/authenticate$', { error: 'anyone who may use api-dev acts as you with Codex there — confirm to sign in', confirm: true }, 409] },
    push('Sign in'),
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Sign in with a device code' } } },
    { wait: 20 },
    { snapshot: 'asked' },
  ], 'c=24', (s) => { s.views[24].run.harness.sandbox = { ...s.views[24].run.harness.sandbox, shared: false }; s.sandboxes[0].visibility = 'private'; return s; });
  assert.equal(find(topScreen(r2.snapshots.open), { t: 'toggle' }), null, 'private: no confirm asked');
  const asked = topScreen(r2.snapshots.asked);
  assert.ok(find(asked, { t: 'toggle' }), 'the backend asks: the confirm appears');
  assert.match(find(asked, { t: 'notice', p: { tone: 'danger' } }).p.text, /confirm to sign in/);
});

test('native: whom to ask; a shell at the agent\'s cwd; a sandbox\'s terminal through the relay; other parks are not the sign-in\'s', async () => {
  const theirs = (s) => {
    const ref = `${SBX}|sb-b0b`;
    s.sandboxes.push({ ref, provider: SBX, manager: 'Coding sandboxes', id: 'sb-b0b', name: 'bobs', state: 'running', egress: 'internet', visibility: 'private',
      image: { id: 'base' }, owner: { user: 'bob' }, mine: false, canUse: false, canManage: false, canEdit: false, workdir: '/work' });
    s.views[24].run.harness.sandbox = { ref, name: 'bobs', cwd: '/work', shared: false };
    s.views[24].config = { ...s.views[24].config, sandbox: { ref, name: 'bobs', cwd: '/work', by: 'bob' } };
    return s;
  };
  const r = await run([{ wait: 50 }, { snapshot: 'chat' }, { tap: { t: 'button', p: { label: 'Sign in…' }, in: { t: 'menu' } } }, { snapshot: 'screen' }], 'c=24', theirs);
  assert.equal(find(r.snapshots.chat, { t: 'notice', p: { title: 'Sign in to Codex' } }).p.text,
    'Codex needs you to sign in (in ▣ bobs). Ask bob to sign in — the sandbox is theirs.');
  assert.equal(find(r.snapshots.chat, { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } }), null, 'nothing to tap');
  assert.equal(find(r.snapshots.chat, { t: 'button', p: { label: 'Terminal' }, in: { t: 'menu' } }), null, 'no shell there either');
  const scr = topScreen(r.snapshots.screen);
  assert.equal(find(scr, { t: 'notice', p: { tone: 'info' } }).p.text, 'Ask bob to sign in — the sandbox is theirs.');
  assert.equal(find(scr, { t: 'field' }), null, 'no methods');

  // a shell at the agent's cwd (⋯ → Terminal), and the ▣ screen and Sandboxes rows (the tile's relay)
  const r2 = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Terminal' }, in: { t: 'menu' } } },
    { snapshot: 'shell' },
  ], 'c=21');
  assert.deepEqual(find(topScreen(r2.snapshots.shell), { t: 'terminal' }).p, { src: 'runs/21/harness/terminal', title: 'Terminal · api-dev' });
  const r3 = await run([
    { wait: 50 },
    { tap: { t: 'button', has: 'Sandbox: api-dev', in: { t: 'menu' } } },
    { wait: 20 },
    { snapshot: 'box' },
    { tap: { t: 'row', p: { title: 'Open terminal' } } },
    { snapshot: 'boxTerm' },
  ], 'c=21');
  assert.deepEqual(find(topScreen(r3.snapshots.boxTerm), { t: 'terminal' }).p,
    { src: `sandboxes/apps/coding-sandbox%7Csb-7f3a/terminal?cwd=${encodeURIComponent('/work/api')}`, title: 'Terminal · api-dev' });
  const r4 = await run([
    { wait: 50 },
    { tap: { t: 'button', has: 'Sandbox: api-dev', in: { t: 'menu' } } },
    { wait: 20 },
    { tap: { t: 'row', p: { title: 'Manage sandboxes…' } } },
    { wait: 50 },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Terminal' }, in: { t: 'row', p: { title: 'api-dev' } } } },
    { snapshot: 'rowTerm' },
  ], 'c=21');
  const rows = all(topScreen(r4.snapshots.list), { t: 'row', in: { t: 'section' } }).filter((x) => x.p.icon === 'box')
    .map((x) => [x.p.title, all(x, { t: 'button' }).some((b) => b.p.label === 'Terminal')]);
  assert.deepEqual(rows.sort(), [['api-dev', true], ['go-dev', true], ['scratch', true]], 'a row\'s Terminal where the manager has tty');
  assert.deepEqual(find(topScreen(r4.snapshots.rowTerm), { t: 'terminal' }).p,
    { src: `sandboxes/apps/coding-sandbox%7Csb-7f3a/terminal?cwd=${encodeURIComponent('/work/api')}`, title: 'Terminal · api-dev' }, 'at the cwd this conversation has it at');

  // an approval park is the built-in card's (or its own module's), never the sign-in's
  const r5 = await run([{ wait: 50 }, { snapshot: 'chat' }], 'c=22');
  assert.equal(find(r5.snapshots.chat, { t: 'notice', p: { title: 'Sign in to Claude Code' } }), null);
  assert.equal(find(r5.snapshots.chat, { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } }), null);
  assert.ok(find(r5.snapshots.chat, { t: 'approval' }), 'the built-in approval stays');
});
