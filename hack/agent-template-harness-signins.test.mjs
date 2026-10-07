// hack/agent-template-harness-signins.test.mjs — a coding agent's sign-ins
// in the agent template (D179): the words (model/harness-signins.js) — saved
// sign-ins' states, groups and keys, a conversation's account and its switch,
// where Remember is offered, the guided sign-in's steps — and the native view
// over the harness fixtures: the guided Sign in screen (start, the link,
// Copy link, a code refused as partial, Finish), Remember only in a person's
// own partition in a sandbox of theirs no one else uses, the Coding-agent
// sign-ins screens (rows, Make default, Rename, Forget, Add a key or token —
// the secret in no prop) and a conversation's Account menu. Run by `make
// js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { runNative } from './xbn/node.mjs';
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const M = await import(new URL('model/harness-signins.js', TPL));
const T = await import(new URL('model/terminals.js', TPL));
const { harnessSeed, SBX, API_DEV } = await import(new URL('test/harness-fixtures.mjs', TPL));

const DAY = 86400000;
const NOW = Date.UTC(2026, 9, 1, 12);
const CLAUDE_KEYS = [{ env: 'CLAUDE_CODE_OAUTH_TOKEN', label: 'Claude subscription token (claude setup-token)', kind: 'setup-token', prefix: 'sk-ant-oat' },
  { env: 'ANTHROPIC_API_KEY', label: 'Anthropic API key', kind: 'api-key' }];
const HARNESSES = {
  claude: { name: 'Claude Code', keys: CLAUDE_KEYS, mint: true },
  codex: { name: 'Codex', keys: [{ env: 'CODEX_API_KEY', label: 'OpenAI API key', kind: 'api-key' }], mint: false },
  opencode: { name: 'OpenCode', keys: [{ env: 'ANTHROPIC_API_KEY', label: 'Anthropic API key', kind: 'api-key', prefix: 'sk-ant-' },
    { env: 'OPENROUTER_API_KEY', label: 'OpenRouter API key', kind: 'api-key', prefix: 'sk-or-' },
    { env: 'OPENAI_API_KEY', label: 'OpenAI API key', kind: 'api-key', prefix: 'sk-' }], mint: false },
};
const SIGNINS = [
  { id: 'hs1', harness: 'claude', name: 'Personal', kind: 'setup-token', env: 'CLAUDE_CODE_OAUTH_TOKEN', mintedAt: NOW - 300 * DAY, expiresAt: NOW + 65 * DAY, isDefault: true },
  { id: 'hs2', harness: 'claude', name: 'Work', kind: 'setup-token', env: 'CLAUDE_CODE_OAUTH_TOKEN', mintedAt: NOW - 360 * DAY, expiresAt: NOW + 5 * DAY, isDefault: false },
  { id: 'hs3', harness: 'claude', name: 'Old', kind: 'api-key', env: 'ANTHROPIC_API_KEY', refusedAt: NOW - DAY, refused: 'OAuth token has been revoked', isDefault: false },
  { id: 'hs4', harness: 'codex', name: 'Personal', kind: 'api-key', env: 'CODEX_API_KEY', isDefault: true },
];
const st = () => M.signinsOf({ available: true, signins: structuredClone(SIGNINS), harnesses: HARNESSES, warnDays: 14 });

// --- the words ------------------------------------------------------------------------------

test('saved sign-ins: read, their state, what they are, grouped per coding agent', () => {
  assert.deepEqual(M.signinsOf(null), { loaded: false, available: false, why: '', list: [], harnesses: {}, warnDays: 14 }, 'not read yet');
  const legacy = M.signinsOf({ available: false, why: 'saved sign-ins need a partitioned agent', signins: [], harnesses: HARNESSES });
  assert.deepEqual([legacy.loaded, legacy.available, legacy.why], [true, false, 'saved sign-ins need a partitioned agent']);
  const s = st();
  assert.deepEqual(SIGNINS.map((x) => M.statusOf(x, NOW).state), ['ok', 'expiring', 'refused', 'ok']);
  assert.equal(M.statusOf(SIGNINS[0], NOW).text, `valid until ${new Date(NOW + 65 * DAY).toISOString().slice(0, 10)}`);
  assert.deepEqual(M.statusOf(SIGNINS[1], NOW), { state: 'expiring', tone: 'warn', text: 'expires in 5 days — sign in again soon' });
  assert.equal(M.statusOf({ ...SIGNINS[1], expiresAt: NOW + 3600000 }, NOW).text, 'expires in 1 day — sign in again soon', 'never "0 days"');
  assert.deepEqual(M.statusOf({ ...SIGNINS[1], expiresAt: NOW - 1 }, NOW), { state: 'expired', tone: 'bad', text: 'expired — sign in again' });
  assert.deepEqual(M.statusOf(SIGNINS[2], NOW), { state: 'refused', tone: 'bad', text: 'refused — sign in again' });
  assert.equal(M.statusOf(SIGNINS[3], NOW).text, 'saved', 'a key: no expiry');
  assert.deepEqual(SIGNINS.map((x) => M.usable(x, NOW)), [true, true, false, true]);
  assert.equal(M.usable({ ...SIGNINS[0], expiresAt: NOW - 1 }, NOW), false, 'expired: not usable');
  assert.equal(M.attention(s, NOW), 2, 'the expiring one and the refused one');
  assert.equal(M.keyLabel(s, SIGNINS[0]), 'Claude subscription token (claude setup-token)');
  assert.equal(M.keyLabel(s, SIGNINS[2]), 'Anthropic API key');
  const g = M.signinGroups(s, NOW);
  assert.deepEqual(g.map((x) => [x.harness, x.rows.length, x.mint]), [['claude', 3, true], ['codex', 1, false], ['opencode', 0, false]], 'ones with sign-ins first');
  assert.deepEqual(g[0].rows.map((r) => [r.name, r.isDefault, r.status.state]), [['Personal', true, 'ok'], ['Work', false, 'expiring'], ['Old', false, 'refused']]);
  assert.equal(M.nameFor(s, 'claude'), '', 'a harness with some: no suggested name');
  assert.equal(M.nameFor(s, 'opencode'), 'Personal', 'the first is Personal');
});

test('a pasted value: the key its prefix says, else the one that takes any — or say which', () => {
  const s = st();
  assert.equal(M.keyFor(s, 'claude', 'sk-ant-oat01-abc').env, 'CLAUDE_CODE_OAUTH_TOKEN');
  assert.equal(M.keyFor(s, 'claude', '  sk-ant-api03-abc ').env, 'ANTHROPIC_API_KEY');
  assert.equal(M.keyFor(s, 'codex', 'sk-proj-x').env, 'CODEX_API_KEY');
  assert.equal(M.keyFor(s, 'opencode', 'sk-or-v1-x').env, 'OPENROUTER_API_KEY');
  assert.equal(M.keyFor(s, 'opencode', 'sk-proj-x').env, 'OPENAI_API_KEY');
  assert.equal(M.keyFor(s, 'opencode', 'AIza-what'), null, 'opencode: say which');
  assert.equal(M.keyFor(s, 'nope', 'x'), null);
});

test('a conversation\'s account: what it uses, and the switch', () => {
  const s = st();
  const h = (over) => ({ provider: 'claude', name: 'Claude Code', state: 'ready', signin: { pick: 'default', using: { id: 'hs1', name: 'Personal' } }, ...over });
  assert.equal(M.accountOf({ provider: 'claude', state: 'ready' }, s).shown, false, 'no signin on the summary (unpartitioned): no account');
  assert.equal(M.accountOf(h(), M.signinsOf({ available: false })).shown, false, 'none here');
  const a = M.accountOf(h(), s, NOW);
  assert.deepEqual([a.shown, a.label, a.warn], [true, 'using Personal', '']);
  assert.deepEqual(a.choices.map((c) => [c.value, c.label, c.current, c.disabled]), [
    ['default', 'Default (Personal)', true, false], ['hs1', 'Personal', false, false], ['hs2', 'Work', false, false],
    ['hs3', 'Old', false, true], ['sandbox', 'This sandbox\'s own sign-in', false, false]]);
  assert.equal(a.choices[2].why, 'expires in 5 days — sign in again soon');
  assert.equal(a.choices[3].why, 'refused — sign in again');
  const w = M.accountOf(h({ signin: { pick: 'hs2', using: { id: 'hs2', name: 'Work' } } }), s, NOW);
  assert.deepEqual([w.label, w.warn, w.choices.find((c) => c.current).value], ['using Work', 'Work: expires in 5 days — sign in again soon', 'hs2']);
  assert.equal(M.accountOf(h({ signin: { pick: 'sandbox', using: null } }), s, NOW).label, 'this sandbox\'s sign-in', 'live, nothing in its environment');
  assert.equal(M.accountOf(h({ state: 'stopped', signin: { pick: 'hs2', using: { id: 'hs1', name: 'Personal' } } }), s, NOW).label, 'Work',
    'stopped: what its next start takes');
  assert.equal(M.accountOf(h({ state: 'stopped', signin: { pick: 'default', using: null } }), s, NOW).label, 'Personal');
  assert.equal(M.accountOf(h({ state: 'stopped', signin: { pick: 'sandbox', using: null } }), s, NOW).label, 'this sandbox\'s sign-in');
  assert.equal(M.accountOf(h({ signin: { pick: 'default', using: { id: 'gone', name: '', forgotten: true } } }), s, NOW).label, 'using a forgotten sign-in');
  const none = M.accountOf({ provider: 'opencode', state: 'stopped', signin: { pick: 'default', using: null } }, s, NOW);
  assert.deepEqual([none.label, none.choices.map((c) => c.label)], ['', ['Default (none saved: this sandbox\'s)', 'This sandbox\'s own sign-in']]);
  assert.equal(M.switchWords('Claude Code', { label: 'Work' }), 'Switched to Work — Claude Code resumes this conversation with it at your next message.');
});

test('Remember: only in a person\'s own partition, for an agent that mints, in a sandbox of theirs no one else uses', () => {
  const c = { shared: false, sandbox: { name: 'my-dev' } };
  const entry = { login: { command: 'claude auth login', guided: true, mint: true } };
  const row = { owner: { user: 'alice' } };
  assert.deepEqual(M.rememberOf(c, { entry, row, me: 'alice', state: 'user' }), { offered: true, why: '' });
  assert.deepEqual(M.rememberOf(c, { entry, row, me: 'alice', state: 'legacy' }), { offered: false, why: '' }, 'unpartitioned: none');
  assert.deepEqual(M.rememberOf(c, { entry, row, me: 'alice', state: 'global' }), { offered: false, why: '' });
  assert.deepEqual(M.rememberOf(c, { entry: { login: { guided: true } }, row, me: 'alice', state: 'user' }), { offered: false, why: '' }, 'no mint');
  assert.match(M.rememberOf({ ...c, shared: true }, { entry, row, me: 'alice', state: 'user' }).why, /my-dev is shared/);
  assert.match(M.rememberOf(c, { entry, row: { owner: { user: 'bob' } }, me: { user: 'alice' }, state: 'user' }).why, /my-dev is bob's/);
  const card = { talk: true, gone: '', ask: '' };
  assert.equal(M.guidedOf(card, entry), true);
  assert.equal(M.guidedOf(card, { login: { command: 'codex login' } }), false, 'no guided sign-in for it');
  assert.equal(M.guidedOf({ ...card, talk: false }, entry), false);
  assert.equal(M.guidedOf({ ...card, ask: 'Ask bob' }, entry), false);
  assert.equal(M.guidedOf({ ...card, gone: 'gone' }, entry), false);
});

test('the guided sign-in\'s steps and words', () => {
  const c = { name: 'Claude Code', sandbox: { name: 'my-dev' } };
  let g = M.newGuided();
  assert.deepEqual(M.guidedWords(g, c), { status: '', busy: false, start: 'Sign in to Claude Code' });
  assert.equal(M.guidedWords({ ...g, remember: true }, c).start, 'Sign in and remember');
  assert.deepEqual(M.guidedWords({ ...g, phase: 'starting' }, c), { status: 'Starting Claude Code\'s sign-in in my-dev…', busy: true });
  g = M.guidedStarted({ ...g, phase: 'starting' }, { ok: 'true', signin: { url: 'https://claude.com/x', paste: true } });
  assert.deepEqual([g.phase, g.url, g.paste], ['waiting', 'https://claude.com/x', true]);
  assert.equal(M.guidedWords(g, c).status, 'Open the sign-in page, sign in, then paste the code it shows here.');
  assert.equal(M.guidedStarted(g, { signin: { url: 'javascript:alert(1)' } }).url, '', 'never anything but https');
  assert.deepEqual([M.guidedStarted(g, {}).phase, M.guidedStarted(g, {}).err], ['idle', 'No sign-in link came.']);
  // a partial code: the CLI asks again — the link stands
  const partial = Object.assign(new Error('Claude Code says that isn\'t the whole code'), { status: 409 });
  const again = M.guidedFailed({ ...g, phase: 'finishing' }, partial);
  assert.deepEqual([again.phase, again.url, again.err], ['waiting', 'https://claude.com/x', partial.message]);
  assert.equal(M.guidedWords(again, c).status, partial.message);
  // refused: start over, saying why
  const refused = M.guidedFailed({ ...g, phase: 'finishing' }, Object.assign(new Error('Login failed: Request failed with status code 400'), { status: 502 }));
  assert.deepEqual([refused.phase, refused.url, refused.err], ['idle', '', 'Login failed: Request failed with status code 400']);
  const done = M.guidedFinished(g, { ok: 'true', state: 'ready' }, 'Claude Code');
  assert.deepEqual([done.phase, done.msg], ['done', 'Signed in to Claude Code. Sending your message again…']);
  assert.equal(M.guidedFinished(g, { saved: { name: 'Work' } }, 'Claude Code').msg,
    'Signed in to Claude Code — saved as Work for your other sandboxes. Sending your message again…');
  assert.equal(M.cleanCode(' abc#\n def\x07 '), 'abc#def');
});

test('the sign-in card says whether the guided sign-in and Remember are offered', () => {
  const seed = harnessSeed();
  const entry = { login: { command: 'codex login', guided: true, mint: true } };
  const c = T.signIn(seed.views[24], { entry });
  assert.deepEqual([c.guided, c.remember], [true, { offered: false, why: '' }], 'unpartitioned: guided, no Remember');
  assert.equal(T.signIn(seed.views[24], { entry: { login: { command: 'codex login' } } }).guided, false);
});

// --- the native view ------------------------------------------------------------------------------

const B = 2 ** 40;
const TTY_MGR = { provider: SBX, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none', 'internet'],
  images: [{ id: 'base', title: 'Debian', default: true }], sizes: [], limits: {} };

async function run(steps, hash, mutate = (s) => s) {
  const s = harnessSeed();
  s.sbxManagers = [TTY_MGR];
  const r = await runNative({ caps: PHONE, entry: new URL('native.js', TPL).pathname,
    data: { now: NOW, self: 'apps/agent', setup: new URL('test/native-stub.mjs', TPL).pathname, seed: mutate(s) },
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
const sent = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url)).map((c) => (c.body ? JSON.parse(c.body) : null));

// the guided sign-in for Codex (the fixtures' #24, a login park): its catalog entry offers one, and Remember
const guided = (s) => { s.harnesses.find((h) => h.id === 'codex').login = { command: 'codex login', guided: true, mint: true }; return s; };
// #24 as a person's own (2^40 + 24: homed in their partition), in a sandbox of theirs no one else uses
const ownPartition = (s) => {
  guided(s);
  s.partition = 'user:admin';
  s.sandboxes[0].visibility = 'private';
  const v = structuredClone(s.views[24]);
  Object.assign(v.run, { id: B + 24, rootId: B + 24 });
  v.run.harness.sandbox = { ...v.run.harness.sandbox, shared: false };
  s.views[B + 24] = v;
  s.runs.push(v.run);
  s.sandboxes[0].boundTo.push(B + 24);
  return s;
};

test('native: the guided Sign in — start (after the shared sandbox\'s confirm), the link, Copy link, a partial code, Finish', async () => {
  const CODE = { t: 'field', p: { label: 'Code' } };
  const r = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } } },
    { snapshot: 'screen' },
    { event: [{ t: 'toggle', p: { label: 'api-dev is shared — sign in anyway' } }, 'change', { value: true }] },
    { tap: { t: 'button', p: { label: 'Sign in to Codex' } } },
    { wait: 20 },
    { snapshot: 'link' },
    { input: [CODE, 'garbage'] },
    { tap: { t: 'button', p: { label: 'Finish' } } },
    { wait: 20 },
    { snapshot: 'partial' },
    { input: [CODE, ' good-1#stub-state\n'] },
    { tap: { t: 'button', p: { label: 'Finish' } } },
    { wait: 50 },
    { snapshot: 'done' },
  ], 'c=24', guided);
  const scr = topScreen(r.snapshots.screen);
  const start = find(scr, { t: 'button', p: { label: 'Sign in to Codex' } });
  assert.ok(start, 'the guided sign-in first');
  assert.equal(start.p.disabled, true, 'a shared sandbox: the confirm first');
  assert.equal(find(scr, { t: 'toggle', p: { label: 'Remember for my other sandboxes' } }), null, 'unpartitioned: no Remember');
  assert.ok(find(scr, { t: 'row', p: { title: 'Use a terminal instead' } }), 'the terminal is the other way');
  assert.equal(find(scr, { t: 'row', p: { title: 'Open a login terminal' } }), null);
  const link = topScreen(r.snapshots.link);
  const URL0 = 'https://claude.com/cai/oauth/authorize?code=true&client_id=stub&response_type=code&scope=user%3Ainference&state=stub-state';
  assert.equal(find(link, { t: 'row', p: { title: 'Open the sign-in page' } }).p.subtitle, URL0);
  assert.equal(find(link, { t: 'button', p: { label: 'Copy link' } }).p.copy, URL0);
  assert.equal(find(link, CODE).p.kind, 'text', 'a code is no secret field');
  assert.match(find(link, { t: 'section', p: { title: 'Sign in to Codex' } }).p.footer, /paste the code it shows here/);
  const partial = topScreen(r.snapshots.partial);
  assert.match(find(partial, { t: 'section', p: { title: 'Sign in to Codex' } }).p.footer, /isn't the whole code/, 'a partial code: asked again');
  assert.ok(find(partial, CODE), '…the link and the field stay');
  assert.deepEqual(sent(r, 'POST', /\/runs\/24\/harness\/authenticate$/), [
    { method: 'guided', confirm: true }, { method: 'guided', code: 'garbage' }, { method: 'guided', code: 'good-1#stub-state' }], 'the code cleaned');
  assert.equal(find(r.snapshots.done, { t: 'notice', p: { title: 'Sign in to Codex' } }), null, 'signed in: the park goes');
  const after = topScreen(r.snapshots.done);
  assert.ok(find(after, { t: 'empty', p: { title: 'Signed in' } }), 'the screen says so');
  assert.equal(JSON.stringify(r.snapshots.done).includes('good-1#'), false, 'the code is in no prop once sent');
});

test('native: Remember — a person\'s own partition, a sandbox of theirs: minted as a saved sign-in, read again; elsewhere said why', async () => {
  const id = B + 24;
  const r = await run([
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } } },
    { snapshot: 'screen' },
    { event: [{ t: 'toggle', p: { label: 'Remember for my other sandboxes' } }, 'change', { value: true }] },
    { snapshot: 'remember' },
    { input: [{ t: 'field', p: { label: 'Name it' } }, 'Work'] },
    { tap: { t: 'button', p: { label: 'Sign in and remember' } } },
    { wait: 20 },
    { input: [{ t: 'field', p: { label: 'Code' } }, 'good-W#stub-state'] },
    { tap: { t: 'button', p: { label: 'Finish' } } },
    { wait: 50 },
  ], `c=${id}`, ownPartition);
  const scr = topScreen(r.snapshots.screen);
  assert.equal(find(scr, { t: 'toggle', p: { label: 'Remember for my other sandboxes' } }).p.value, false);
  assert.equal(find(scr, { t: 'toggle', has: 'sign in anyway' }), null, 'her own, unshared: no confirm');
  const rem = topScreen(r.snapshots.remember);
  assert.equal(find(rem, { t: 'field', p: { label: 'Name it' } }).p.placeholder, 'Personal', 'the first is Personal');
  assert.notEqual(find(rem, { t: 'button', p: { label: 'Sign in and remember' } }).p.disabled, true);
  assert.deepEqual(sent(r, 'POST', new RegExp(`/runs/${id}/harness/authenticate$`)), [
    { method: 'guided', remember: true, name: 'Work' }, { method: 'guided', code: 'good-W#stub-state' }]);
  assert.ok(r.calls.filter((c) => c.method === 'GET' && /\/prefs\/harness-signins$/.test(c.url)).length >= 1, 'the saved sign-ins read again');

  // the same in a shared sandbox: no Remember, saying why
  const shared = (s) => { ownPartition(s); s.sandboxes[0].visibility = 'team'; s.views[id].run.harness.sandbox.shared = true; return s; };
  const r2 = await run([{ wait: 50 }, { tap: { t: 'button', p: { label: 'Sign in' }, in: { t: 'composer' } } }, { snapshot: 's' }], `c=${id}`, shared);
  const s2 = topScreen(r2.snapshots.s);
  assert.equal(find(s2, { t: 'toggle', p: { label: 'Remember for my other sandboxes' } }), null);
  assert.match(find(s2, { t: 'section', p: { title: 'Sign in to Codex' } }).p.footer, /Remember works only in a sandbox of your own that no one else uses — api-dev is shared/);
});

test('native: Coding-agent sign-ins — rows, Make default, Rename, Forget; Add a key or token, the secret in no prop', async () => {
  const SECRET = 'sk-ant-api03-native-secret-0123';
  const withSignins = (s) => { s.signins = structuredClone(SIGNINS); return s; };
  const r = await run([
    { tap: { t: 'button', has: 'Coding agent settings' } }, { wait: 20 },
    { tap: { t: 'row', p: { title: 'Coding-agent sign-ins' } } }, { wait: 50 },
    { snapshot: 'list' },
    { tap: { t: 'button', p: { label: 'Make default' }, in: { t: 'row', p: { title: 'Work' } } } }, { wait: 50 },
    { snapshot: 'defaulted' },
    { tap: { t: 'row', p: { title: 'Old' } } }, { wait: 20 },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'Older'] },
    { tap: { t: 'button', p: { label: 'Rename' }, in: { t: 'screen', p: { title: 'Old' } } } }, { wait: 50 },
    { snapshot: 'renamed' },
    { tap: { t: 'button', p: { label: 'Forget' }, in: { t: 'screen', p: { title: 'Older' } } } }, { wait: 50 },
    { snapshot: 'forgot' },
    { tap: { t: 'row', p: { title: 'Add a key or token' }, in: { t: 'section', p: { title: 'Claude Code' } } } }, { wait: 20 },
    { snapshot: 'add' },
    { input: [{ t: 'field', p: { label: 'Name' } }, 'Team key'] },
    { input: [{ t: 'field', p: { label: 'Key or token' } }, SECRET] },
    { tap: { t: 'button', p: { label: 'Save' } } }, { wait: 50 },
    { snapshot: 'added' },
  ], '', withSignins);
  const list = topScreen(r.snapshots.list);
  assert.equal(list.p.title, 'Coding-agent sign-ins');
  const claude = find(list, { t: 'section', p: { title: 'Claude Code' } });
  const rows = all(claude, { t: 'row' }).map((x) => [x.p.title, x.p.detail || '', x.p.tone || '']);
  assert.deepEqual(rows, [['Personal', 'Default', ''], ['Work', '', 'warn'], ['Old', '', 'danger'], ['Add a key or token', '', '']]);
  assert.match(find(claude, { t: 'row', p: { title: 'Work' } }).p.subtitle, /^Claude subscription token \(claude setup-token\) · expires in/);
  assert.ok(find(list, { t: 'section', p: { title: 'OpenCode' } }), 'one with none saved: Add only');
  assert.deepEqual(sent(r, 'PUT', /\/prefs\/harness-signins\/hs2$/), [{ default: true }]);
  assert.equal(find(topScreen(r.snapshots.defaulted), { t: 'row', p: { title: 'Work' } }).p.detail, 'Default');
  assert.deepEqual(sent(r, 'PUT', /\/prefs\/harness-signins\/hs3$/), [{ name: 'Older' }]);
  assert.equal(topScreen(r.snapshots.renamed).p.title, 'Older');
  assert.deepEqual(sent(r, 'DELETE', /\/prefs\/harness-signins\/hs3$/), [null]);
  assert.equal(topScreen(r.snapshots.forgot).p.title, 'Coding-agent sign-ins', 'Forget leaves its screen');
  assert.equal(find(topScreen(r.snapshots.forgot), { t: 'row', p: { title: 'Older' } }), null, '…and it is gone');
  const add = topScreen(r.snapshots.add);
  assert.equal(find(add, { t: 'field', p: { label: 'Key or token' } }).p.kind, 'secure');
  assert.ok(find(add, { t: 'picker', p: { label: 'Which key' } }), 'Claude Code takes several keys: which');
  assert.deepEqual(sent(r, 'POST', /\/prefs\/harness-signins$/), [{ harness: 'claude', secret: SECRET, name: 'Team key', env: 'ANTHROPIC_API_KEY' }]);
  assert.equal(topScreen(r.snapshots.added).p.title, 'Coding-agent sign-ins', 'saved: back to the list');
  assert.ok(find(topScreen(r.snapshots.added), { t: 'row', p: { title: 'Team key' } }));
  assert.equal(JSON.stringify(r.snapshots.added).includes(SECRET), false, 'the secret is in no prop');
  assert.equal(JSON.stringify(r.messages).includes(SECRET), false, 'the tile never drew it');

  // an unpartitioned agent: none, saying why
  const legacy = (s) => { s.signinsAvailable = false; s.signinsWhy = 'saved sign-ins need a partitioned agent, where each person has a space of their own'; return s; };
  const r2 = await run([{ tap: { t: 'button', has: 'Coding agent settings' } }, { wait: 20 },
    { tap: { t: 'row', p: { title: 'Coding-agent sign-ins' } } }, { wait: 50 }, { snapshot: 's' }], '', legacy);
  assert.match(find(topScreen(r2.snapshots.s), { t: 'notice' }).p.text, /need a partitioned agent/);
});

test('native: a conversation\'s Account — "using Personal", the switch, a refused one left out', async () => {
  const acct = (s) => {
    s.signins = structuredClone(SIGNINS);
    s.views[21].run.harness = { ...s.views[21].run.harness, signin: { pick: 'default', using: { id: 'hs1', name: 'Personal' } } };
    return s;
  };
  const MENU = { t: 'menu', has: 'Account: ' };
  const r = await run([
    { wait: 80 },
    { snapshot: 'chat' },
    { tap: { t: 'button', p: { label: 'Work — expires in 5 days — sign in again soon' }, in: MENU } },
    { wait: 50 },
    { snapshot: 'switched' },
  ], 'c=21', acct);
  const menu = find(r.snapshots.chat, MENU);
  assert.ok(menu, 'the Account menu in the toolbar');
  assert.equal(menu.p.label, 'Account: using Personal');
  const labels = all(menu, { t: 'button' }).map((b) => [b.p.label, b.p.icon || '']);
  assert.deepEqual(labels.slice(0, 4), [['Default (Personal)', 'check'], ['Personal', ''], ['Work — expires in 5 days — sign in again soon', ''],
    ['This sandbox\'s own sign-in', '']], 'the refused one left out');
  assert.ok(labels.some(([l]) => l === 'Saved sign-ins…'));
  assert.deepEqual(sent(r, 'PUT', /\/runs\/21\/harness\/signin$/), [{ signin: 'hs2' }]);
  assert.equal(find(r.snapshots.switched, MENU).p.label, 'Account: using Personal', 'still the running one\'s until it restarts');
  const picked = all(find(r.snapshots.switched, MENU), { t: 'button', p: { icon: 'check' } }).map((b) => b.p.label);
  assert.deepEqual(picked, ['Work — expires in 5 days — sign in again soon'], 'the pick is Work now');
  // unpartitioned (no signin on the summary): no Account menu
  const r2 = await run([{ wait: 80 }, { snapshot: 'chat' }], 'c=21');
  assert.equal(find(r2.snapshots.chat, MENU), null);
  assert.equal(r2.calls.some((c) => /\/prefs\/harness-signins/.test(c.url)), false, 'nor a read of saved sign-ins');
});
