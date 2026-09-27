// hack/agent-template-sandboxes.test.mjs — coding sandboxes (D115) in the
// agent template's shared model: the composer's picker
// (builtin-templates/agent/model/sandboxes.js sandboxPicker), the ▣ badge
// and why a binding no longer resolves, the Sandboxes dialog's rows and
// their actions, the create form, the sandbox tool cards
// (model/tool-heads.js: the box family, its sublines and outcomes), and the
// app's store (model/sandbox-store.js: picking, binding, detaching,
// creating, lifecycle, the run events that carry a binding). Both views draw
// from these; the browser test (test/sandbox.mjs) checks the drawing. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const S = await import(new URL('model/sandboxes.js', TPL).href);
const T = await import(new URL('model/tool-heads.js', TPL).href);

const MGR = 'apps/coding-sandbox';
const sb = (id, extra = {}) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running',
  egress: 'none', visibility: 'private', owner: { user: 'alice' }, mine: true, canUse: true, canManage: true, canEdit: true,
  caps: ['exec', 'files', 'tar', 'archive'], lastActive: 1000, image: { id: 'base', title: 'Debian' }, ...extra });
const managers = () => [{ provider: MGR, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'archive'], egress: ['none', 'internet', 'open'],
  images: [{ id: 'base', title: 'Debian' }, { id: 'go', title: 'Go', default: true }],
  sizes: [{ id: 'small', memMiB: 2048, vcpus: 2, diskGiB: 20, default: true }, { id: 'big', memMiB: 8192, vcpus: 8, diskGiB: 80 }], limits: {} }];
const list = (boxes, mgrs = managers()) => S.listOf({ sandboxes: boxes, managers: mgrs });
const coding = { id: 'coding', name: 'Coding', toolsets: ['sandbox', 'web'], managers: 'all', sandboxEgress: ['none', 'internet'] };
const internal = { id: 'internal', name: 'Internal', toolsets: ['internal'], managers: [], sandboxEgress: [] };
const conv = (cfg = {}, extra = {}) => ({ run: { id: 5, rootId: 5, status: 'idle' }, access: 'owner', class: coding, config: cfg, ...extra });
const bind = (id, extra = {}) => ({ ref: `${MGR}|${id}`, name: id, cwd: '/work', manager: 'Coding sandboxes', egress: 'none', by: 'alice', ...extra });

test('GET /sandboxes as the views keep it; references; what a class allows', () => {
  assert.deepEqual(S.listOf(null), { sandboxes: [], managers: [], loaded: false });
  assert.equal(S.listOf({ sandboxes: [sb('a')], managers: [] }).loaded, true);
  assert.deepEqual(S.splitRef('apps/x#2|sb-1'), { provider: 'apps/x#2', id: 'sb-1' });
  assert.equal(S.classAllows(coding, MGR, 'internet'), '');
  assert.match(S.classAllows(coding, MGR, 'open'), /doesn't allow a sandbox with open network/);
  assert.match(S.classAllows(internal, MGR, ''), /has no coding sandbox/);
  const only = { ...coding, managers: ['apps/other'] };
  assert.match(S.classAllows(only, MGR, '', 'Coding sandboxes'), /doesn't allow sandboxes from Coding sandboxes/);
  assert.equal(S.classAllows({ ...coding, managers: ['apps/other'] }, 'apps/other#2', ''), '', 'an instance of a listed tile');
});

test('the picker: only where the class has the sandbox toolset; grouped; the reasons', () => {
  const L = list([
    sb('mine-old', { lastActive: 1 }), sb('mine-new', { lastActive: 9 }),
    sb('bobs', { mine: false, owner: { user: 'bob' }, canEdit: false, canManage: false }),
    sb('team', { mine: false, owner: { user: 'carol' }, visibility: 'team', canEdit: false, canManage: false }),
    sb('wide', { egress: 'open', lastActive: 3 }),
    sb('private-other', { mine: false, owner: { user: 'dan' }, canUse: false, canEdit: false }),
    sb('bound', { mine: false, owner: { user: 'erin' }, canUse: false, canManage: false, canEdit: false, boundTo: [5] }),
  ]);
  assert.equal(S.sandboxPicker(L, conv({}, { class: internal }), {}).shown, false, 'a class without the sandbox toolset: hidden');
  assert.equal(S.sandboxPicker(L, null, {}, { cls: internal }).shown, false, 'at home too');

  const v = conv({ sandbox: bind('mine-old'), attached: [bind('mine-old'), bind('gone-one', { name: 'gone' })] });
  const p = S.sandboxPicker(L, v, { user: 'alice' });
  assert.equal(p.shown, true);
  assert.equal(p.value, `${MGR}|mine-old`);
  assert.equal(p.label, '▣ mine-old');
  assert.deepEqual(p.groups.map((g) => g.id), ['here', 'mine', 'shared', 'team']);
  const here = p.groups[0].rows;
  assert.deepEqual(here.map((r) => r.name), ['mine-old', 'gone', 'bound'], 'what it has attached, and what it is bound to');
  assert.equal(here[0].on, true);
  assert.equal(here[1].disabled, true, 'an attached one its manager no longer has');
  assert.match(here[1].why, /gone/);
  assert.equal(here[2].disabled, true, 'someone else\'s: only they can make it active');
  assert.match(here[2].why, /someone else bound it/);
  assert.deepEqual(p.groups[1].rows.map((r) => r.name), ['mine-new', 'wide'], 'yours, most recently active first');
  assert.equal(p.groups[1].rows[1].disabled, true, 'an egress the class does not allow');
  assert.deepEqual(p.groups[2].rows.map((r) => r.name), ['bobs']);
  assert.deepEqual(p.groups[3].rows.map((r) => r.name), ['team']);
  assert.ok(!p.groups.flatMap((g) => g.rows).some((r) => r.name === 'private-other'), 'one you may not use is not offered');
  assert.deepEqual(p.actions.map((a) => [a.id, a.disabled]), [['new', false], ['manage', false]]);
  assert.equal(S.sandboxPicker(L, { ...v, access: 'viewer' }, {}).disabled, true, 'view only');

  // at home: the next new chat's pick
  const h = S.sandboxPicker(L, null, {}, { cls: coding, pick: { ref: `${MGR}|team`, name: 'team' } });
  assert.equal(h.value, `${MGR}|team`);
  assert.deepEqual(h.groups.map((g) => g.id), ['mine', 'shared', 'team']);
  assert.match(h.title, /next new chat/);
  // a pick not listed (not read yet) still says itself; a manager down is a note
  const cold = S.sandboxPicker(S.listOf(null), null, {}, { cls: coding, pick: { ref: `${MGR}|x`, name: 'x' } });
  assert.equal(cold.loading, true);
  assert.deepEqual(cold.groups[0].rows.map((r) => [r.value, r.on]), [[`${MGR}|x`, true]]);
  const down = S.sandboxPicker(list([], [{ provider: MGR, title: 'Coding sandboxes', ok: false, error: 'unreachable' }]), null, {}, { cls: coding });
  assert.deepEqual(down.notes, ['Coding sandboxes: unreachable']);
  assert.equal(down.actions[0].disabled, true, 'nothing to create at');
});

test('the firewall\'s egress: the less restrictive of now and the next start; what a sandbox has held', () => {
  for (const [now, next, want] of [['none', '', 'none'], ['none', 'internet', 'internet'], ['internet', 'none', 'internet'],
    ['', '', 'open'], ['none', 'lan', 'open'], ['toString', '', 'open']]) {
    assert.equal(S.firewallEgress({ egress: now, egressNext: next }), want, `${now} → ${next}`);
  }
  assert.equal(S.egressWords({ egress: 'none' }), 'no network');
  assert.equal(S.egressWords({ egress: 'none', egressNext: 'internet' }), 'no network → internet at the next start');
  const held = sb('vault', { labels: { [S.INTERNAL_LABEL]: '1' } });
  const intsbx = { id: 'intsbx', name: 'Internal coding', toolsets: ['internal', 'sandbox'], sandboxEgress: ['none'] };
  const quiet = { id: 'quiet', name: 'Quiet', toolsets: ['sandbox'], sandboxEgress: ['none'] };
  assert.match(S.taintWhy(coding, held), /held data from an internal-reach conversation/);
  assert.equal(S.taintWhy(intsbx, held), '', 'internal reach may');
  assert.equal(S.taintWhy(quiet, held), '', 'no egress may');
  assert.equal(S.taintWhy(coding, sb('clean')), '');
  // the picker disables them, the dialog offers no "use", the badge says why
  const p = S.sandboxPicker(list([held, sb('pending', { egressNext: 'open' }), sb('ok')]), conv({}), { user: 'alice' });
  const rows = Object.fromEntries(p.groups.flatMap((g) => g.rows).map((r) => [r.name, r]));
  assert.ok(rows.vault.disabled && /internal-reach/.test(rows.vault.why), JSON.stringify(rows.vault));
  assert.ok(rows.pending.disabled && /open network/.test(rows.pending.why) && /at the next start/.test(rows.pending.detail), JSON.stringify(rows.pending));
  assert.equal(rows.ok.disabled, false);
  const acts = Object.fromEntries(S.sandboxRows(list([held, sb('ok')]), { user: 'alice' }, { conv: conv({}) }).map((r) => [r.name, r.actions.map((a) => a.id)]));
  assert.ok(!acts.vault.includes('use') && acts.ok.includes('use'), JSON.stringify(acts));
  const v = conv({ sandbox: bind('vault') });
  assert.match(S.sandboxBadge(v, list([held])).broken, /^not allowed: .*internal-reach/);
  assert.match(S.sandboxBadge(v, list([sb('vault', { egressNext: 'open' })])).broken, /^not allowed: .*open network/);
});

test('the badge: name · cwd, the attached ones, and why a binding no longer resolves', () => {
  assert.equal(S.sandboxBadge(conv({}), list([])), null);
  const v = conv({ sandbox: bind('api', { cwd: '/work/api' }), attached: [bind('api', { cwd: '/old' }), bind('web')] });
  let b = S.sandboxBadge(v, list([sb('api'), sb('web')]));
  assert.equal(b.label, '▣ api · /work/api');
  assert.equal(b.broken, '');
  assert.deepEqual(b.attached.map((a) => [a.name, a.cwd, a.on]), [['api', '/work/api', true], ['web', '/work', false]], 'the active one with its newest cwd');
  assert.equal(b.canChange, true);
  assert.equal(S.sandboxBadge(v, S.listOf(null)).broken, '', 'not read yet: nothing to say');
  assert.match(S.sandboxBadge(v, list([sb('web')])).broken, /^gone/);
  assert.match(S.sandboxBadge(v, list([], [])).broken, /its manager \(Coding sandboxes\) is no longer bound/);
  assert.match(S.sandboxBadge(v, list([], [{ provider: MGR, title: 'CS', ok: false, error: 'down' }])).broken, /unavailable: down/);
  b = S.sandboxBadge({ ...v, class: { ...coding, sandboxEgress: ['none'] }, config: { sandbox: bind('api', { egress: 'internet' }) } }, list([sb('api')]));
  assert.match(b.broken, /^not allowed: .*internet/);
  assert.match(S.sandboxBadge({ ...v, class: internal }, list([sb('api')])).broken, /no coding sandbox/);
  assert.equal(S.sandboxBadge({ ...v, access: 'viewer' }, list([sb('api')])).canChange, false);
});

test('the dialog\'s rows: state, owner, the actions your rights allow', () => {
  const L = list([
    sb('run'), sb('stop', { state: 'stopped', lastActive: 5 }), sb('arch', { state: 'archived' }),
    sb('theirs', { mine: false, owner: { user: 'bob' }, visibility: 'team', canManage: false, canEdit: false, boundTo: [5, 6] }),
    sb('noarch', { state: 'stopped', caps: ['exec', 'files'] }),
  ]);
  const rows = S.sandboxRows(L, { user: 'alice' }, { conv: conv({ sandbox: bind('run') }), now: 1000 + 120e3 });
  const by = Object.fromEntries(rows.map((r) => [r.name, r]));
  const acts = (n) => by[n].actions.map((a) => a.id);
  assert.deepEqual(rows.map((r) => r.name).slice(-1), ['theirs'], 'yours first');
  assert.deepEqual(acts('run'), ['stop', 'archive', 'team', 'delete'], 'the active one: no "use"');
  assert.deepEqual(acts('stop'), ['use', 'start', 'archive', 'team', 'delete']);
  assert.deepEqual(acts('arch'), ['use', 'thaw', 'team', 'delete']);
  assert.deepEqual(acts('theirs'), ['use', 'stop'], 'a team one: use it, start/stop it — not delete or share');
  assert.ok(!acts('noarch').includes('archive'), 'no archive capability: no archive');
  assert.equal(by.run.active, true);
  assert.equal(by.theirs.here, true);
  assert.equal(by.theirs.owner, 'bob');
  assert.equal(by.run.owner, 'you');
  assert.equal(by.run.lastLabel, '2 min ago');
  assert.match(by.theirs.actions[0].label, /Use here/);
  const del = by.run.actions.find((a) => a.id === 'delete');
  assert.equal(del.danger, true);
  assert.match(del.confirm, /Delete the sandbox “run”\? Everything in it is gone for good/);
  assert.deepEqual(S.sandboxRows(L, {}, {}).find((r) => r.name === 'theirs').actions.map((a) => a.id), ['stop'], 'at home: no "use"');
  assert.ok(!S.sandboxRows(L, {}, {}).some((r) => r.actions.some((a) => a.id === 'use')), 'at home without a sandbox class: no "use"');
  assert.match(S.sandboxRows(L, {}, { cls: coding }).find((r) => r.name === 'stop').actions[0].label, /new chat/);
  // where it is used: here, or — at home — the next new chat's pick (never "active here" with no conversation)
  assert.deepEqual([by.run.where, by.stop.where, by.theirs.where], ['active here', '', 'attached here'], 'in a conversation: its active and attached ones say so');
  const home = S.sandboxRows(L, {}, { cls: coding, pick: { ref: `${MGR}|stop` } });
  assert.deepEqual(home.filter((r) => r.where).map((r) => [r.name, r.where, r.active]), [['stop', 'next new chat', true]], 'at home: the pick is the next new chat\'s');
  assert.ok(!S.sandboxRows(L, {}, { conv: conv({}, { access: 'viewer' }) }).some((r) => r.actions.some((a) => a.id === 'use')), 'view only: no "use"');
});

test('the create form: the class filters managers and egress; defaults; checks; the body', () => {
  let vm = S.createForm(managers(), coding, {});
  assert.deepEqual(vm.f, { provider: MGR, name: '', image: 'go', size: 'small', egress: 'none', visibility: 'private', cwd: '' }, 'the defaults');
  assert.deepEqual(vm.egress.map((e) => [e.value, e.disabled]), [['none', false], ['internet', false], ['open', true]]);
  assert.equal(vm.error, 'Name the sandbox.');
  vm = S.createForm(managers(), coding, { ...vm.f, name: ' api ', egress: 'open', image: 'nope', cwd: 'rel' });
  assert.equal(vm.f.egress, 'none', 'an egress the class does not allow falls back');
  assert.equal(vm.f.image, 'go', 'an image the manager does not have falls back');
  assert.match(vm.error, /absolute path/);
  vm = S.createForm(managers(), coding, { ...vm.f, cwd: '/work/api', egress: 'internet', size: 'big' });
  assert.equal(vm.ok, true);
  assert.deepEqual(S.createBody(vm.f, { conversation: 5, clientId: 'k' }), { name: 'api', provider: MGR, egress: 'internet', visibility: 'private',
    image: 'go', size: 'big', cwd: '/work/api', conversation: 5, clientId: 'k' });
  assert.equal(S.createBody(vm.f, { conversation: 5, bind: false }).bind, false);
  assert.equal(S.createForm(managers(), null, {}, { team: true }).f.visibility, 'team', 'a team conversation\'s default');
  assert.equal(S.createForm(managers(), null, { egress: 'open' }).f.egress, 'open', 'no class: anything the manager offers');
  assert.match(S.createForm(managers(), { ...coding, managers: ['apps/other'] }, { name: 'x' }).error, /No manager the Coding class allows/);
  assert.match(S.createForm([], coding, { name: 'x' }).error, /No sandbox manager is bound/);
  assert.match(S.createForm(managers(), coding, { name: 'x'.repeat(65) }).error, /64/);
  assert.equal(S.cwdCheck(''), '');
  assert.equal(S.ago(0), '');
  assert.equal(S.ago(1000, 1000 + 3 * 3600e3), '3 h ago');
});

test('the tool cards: the box family, its sublines and what a call came to', () => {
  const a = (o) => JSON.stringify(o);
  assert.equal(T.family('bash'), 'box');
  assert.equal(T.family('sandbox_create'), 'box');
  assert.equal(T.ICON.box, '▣');
  assert.equal(T.headline('bash', a({ command: 'go test ./...' })), '$ go test ./...');
  assert.equal(T.headline('bash', a({ command: 'make', background: true })), '$ make &');
  assert.equal(T.headline('bash', a({ command: 'go test ./...', summary: 'Run the tests' })), 'Run the tests');
  assert.equal(T.subline('bash', a({ command: 'go test ./...', summary: 'Run the tests' })), '$ go test ./...', 'the command under a summary');
  assert.equal(T.subline('bash', a({ command: 'go test ./...' })), '', 'no summary: the headline says it');
  assert.equal(T.subline('web_fetch', a({ url: 'x', summary: 's' })), '', 'not a sandbox call');
  assert.equal(T.headline('edit', a({ path: '/w/a.go', old_string: 'foo()', new_string: 'bar()' })), 'Edit /w/a.go: foo() → bar()');
  assert.equal(T.headline('grep', a({ pattern: 'TODO', path: 'src' })), 'Search /TODO/ under src');
  assert.equal(T.headline('glob', a({ pattern: '**/*.go' })), 'Find **/*.go');
  assert.equal(T.headline('sandbox_copy', a({ from: { sandbox: 'api', path: '/a' }, to: { path: '/b' } })), 'Copy api:/a → /b');
  assert.equal(T.headline('bash_output', a({ job: 3, wait_s: 30 })), 'Output of job 3 (waits 30s)');
  assert.equal(T.headline('sandbox_create', a({ name: 'api' })), 'Create sandbox api');
  // bash's footer
  assert.deepEqual(T.outcome('bash', 'ok\n[exit 0 · 14s · job 3]'), { text: 'exit 0 · 14s · job 3', tone: 'ok' });
  assert.deepEqual(T.outcome('bash', 'FAIL\n[exit 1 · 2s · job 4]'), { text: 'exit 1 · 2s · job 4', tone: 'bad' });
  assert.deepEqual(T.outcome('bash', 'x\n[still running after 2m00s · job 3 — bash_output {"job": 3} follows it, bash_kill {"job": 3} stops it]'),
    { text: 'still running · 2m00s · job 3', tone: 'run' });
  assert.deepEqual(T.outcome('bash_output', 'x\n[running · 14s so far · job 3 · read to byte 1234]'), { text: 'running · 14s so far · job 3', tone: 'run' });
  assert.deepEqual(T.outcome('bash', 'started job 4 in "api": make\n[bash_output {"job": 4} reads its output · bash_kill {"job": 4} stops it]'),
    { text: 'job 4 started', tone: 'run' });
  assert.deepEqual(T.outcome('bash', 'x\n[killed by TERM · 3s · job 5]'), { text: 'killed by TERM · 3s · job 5', tone: 'bad' });
  // a restart cut it off: it went on as a job (sandbox_jobs.go lostResultText) — answered, not stopped
  const moved = '(no result: the backend restarted while this command ran. It went on in the sandbox as job 5 — bash_output {"job": 5} '
    + 'shows its output from the start and whether it has finished; bash_kill {"job": 5} stops it.)';
  assert.equal(T.resultState(moved), 'done', 'a command a restart cut off went on as a job: the card is not struck through');
  assert.deepEqual(T.outcome('bash', moved), { text: 'went on as job 5', tone: 'run' });
  assert.equal(T.resultState('(no result: the backend restarted while this tool was running)'), 'stopped', 'any other call a restart cut off stays stopped');
  assert.equal(T.outcome('bash', '(running…)'), null, 'still going: nothing yet');
  assert.equal(T.outcome('bash', 'error: no sandbox'), null, 'an error says itself');
  // counts and sizes
  assert.equal(T.outcome('grep', 'a.go:3: foo\nb.go:9: foo\n… [5 more matching lines — narrow the pattern, the path or the glob]').text, '7 matches');
  assert.equal(T.outcome('grep', 'a.go:3: foo').text, '1 match');
  assert.equal(T.outcome('grep', 'no matches for "x" under /w').text, 'no matches');
  assert.equal(T.outcome('glob', 'a.go\nb.go\n… and 3 more — narrow the pattern or the path').text, '5 files');
  assert.equal(T.outcome('ls', '/work:\nsrc/\nREADME  (1.2 KiB)').text, '2 entries');
  assert.equal(T.outcome('ls', '/work: (empty)').text, 'empty');
  assert.equal(T.outcome('read', '     1\tpackage main\n     2\t').text, '2 lines');
  assert.equal(T.outcome('write', 'wrote /work/a.go (1.2 KiB)').text, '1.2 KiB');
  assert.equal(T.outcome('edit', 'edited /work/a.go (1 replacement)\n   3\tfoo').text, '1 replacement');
  assert.equal(T.outcome('web_fetch', 'x (y)'), null);
});

test('the store: pick at home, bind, cwd, detach, create, lifecycle, run events', async () => {
  const calls = [];
  const cfgs = { 5: { sandbox: bind('api'), attached: [bind('api')] } };
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const L = { sandboxes: [sb('api'), sb('web', { boundTo: [] }), sb('theirs', { mine: false, canUse: false, canManage: false, boundTo: [5] })], managers: managers() };
  const fake = async (url, opt = {}) => {
    const method = opt.method || 'GET';
    const u = String(url);
    calls.push({ method, url: u, body: opt.body });
    if (u.endsWith('/prefs/class')) return json('coding');
    if (/\/prefs\//.test(u)) return json({}, 404);
    if (u.endsWith('/classes')) return json({ default: 'internal', classes: [internal, coding] });
    if (/\/sandboxes(\?fresh=1)?$/.test(u) && method === 'GET') return json(L);
    if (u.endsWith('/sandboxes') && method === 'POST') {
      const b = JSON.parse(opt.body);
      if (b.conversation) cfgs[b.conversation] = { sandbox: bind(b.name), attached: [...cfgs[b.conversation].attached, bind(b.name)] };
      return json({ ...sb(b.name), ...(b.conversation ? { boundTo: [b.conversation] } : {}) }, 201);
    }
    if (/\/sandboxes\/.+\/(start|stop|archive|thaw)\?/.test(u)) return json(sb(decodeURIComponent(u.split('|').pop().split('/')[0].replace(/%7C/i, '')), { state: 'stopped' }));
    const run = /\/runs\/(\d+)$/.exec(u);
    if (run && method === 'PATCH') {
      const b = JSON.parse(opt.body);
      const c = cfgs[run[1]];
      if (b.detach) { c.attached = c.attached.filter((a) => a.ref !== b.detach); if (c.sandbox && c.sandbox.ref === b.detach) delete c.sandbox; }
      if (b.sandbox === null) delete c.sandbox;
      else if (b.sandbox) {
        if (b.sandbox.cwd === '/nope') return json({ error: 'sandbox.cwd: /nope doesn\'t exist in the sandbox' }, 400);
        c.sandbox = bind(S.splitRef(b.sandbox.ref).id, { cwd: b.sandbox.cwd || '/work' });
        c.attached = [...c.attached.filter((a) => a.ref !== b.sandbox.ref), c.sandbox];
      }
      return json({ id: +run[1] });
    }
    const vw = /\/runs\/(\d+)\/view(\?limit=1)?$/.exec(u);
    if (vw) return json({ cursor: 'g.1', run: { id: +vw[1], rootId: +vw[1], status: 'idle', visibility: 'team' }, access: 'owner', class: coding,
      config: JSON.parse(JSON.stringify(cfgs[vw[1]] || {})), messages: [], steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {} });
    if (u.endsWith('/ask')) return json({ id: 9, title: 'x', status: 'running', rootId: 9 });
    if (u.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
    return json({});
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const { createApp } = await import(new URL('model/app.js', TPL).href);
  const app = createApp({ frame: (fn) => setTimeout(fn, 0) });
  let heard = 0;
  app.on('sandboxes', () => heard++);
  await app.loadClasses();
  assert.equal(app.classId, 'coding');

  // at home: the next new chat's sandbox, sent while its class has the toolset
  app.sbx.ensure();
  await app.sbx.load();
  assert.equal(app.sbx.list.loaded, true);
  assert.equal(app.sbx.picker().shown, true);
  await app.sbx.choose(`${MGR}|web`);
  assert.deepEqual(app.sbx.pick, { ref: `${MGR}|web`, cwd: '', name: 'web' });
  await app.ask({ text: 'hi', class: 'coding' });
  let ask = JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body);
  assert.deepEqual(ask.sandbox, { ref: `${MGR}|web` }, 'the pick goes with a new chat');
  await app.ask({ text: 'hi', class: 'internal' });
  ask = JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body);
  assert.equal(ask.sandbox, undefined, 'not in a class without the sandbox toolset');
  await app.ask({ text: 'old', toolset: 'web' });
  assert.equal(JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body).sandbox, undefined, 'a legacy lane ask names no sandbox');

  // in a conversation: a pick binds it (PATCH its root), then its config is read again
  await app.select(5);
  assert.equal(app.sbx.badge().label, '▣ api · /work');
  assert.equal(app.sbx.picker().groups[0].id, 'here');
  await app.sbx.choose(`${MGR}|web`);
  const patch = calls.filter((c) => c.method === 'PATCH').pop();
  assert.equal(patch.url, '/api/apps/agent/runs/5');
  assert.deepEqual(JSON.parse(patch.body), { sandbox: { ref: `${MGR}|web` } });
  assert.ok(calls.some((c) => c.url.endsWith('/runs/5/view?limit=1')), 'the binding is read again');
  assert.equal(app.sbx.badge().name, 'web');
  assert.deepEqual(app.sbx.badge().attached.map((a) => a.name), ['api', 'web']);
  // the working directory: checked here, refused there, set
  await assert.rejects(app.sbx.setCwd('rel'), /absolute path/);
  await assert.rejects(app.sbx.setCwd('/nope'), /doesn't exist/);
  await app.sbx.setCwd(' /work/web ');
  assert.deepEqual(JSON.parse(calls.filter((c) => c.method === 'PATCH').pop().body), { sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } });
  assert.equal(app.sbx.badge().label, '▣ web · /work/web');
  // detach; no sandbox
  await app.sbx.detach(`${MGR}|web`);
  assert.deepEqual(JSON.parse(calls.filter((c) => c.method === 'PATCH').pop().body), { detach: `${MGR}|web` });
  assert.equal(app.sbx.badge(), null);
  await app.sbx.choose('');
  assert.deepEqual(JSON.parse(calls.filter((c) => c.method === 'PATCH').pop().body), { sandbox: null });

  // create for the conversation: made for it and bound there; a team conversation's form defaults to team
  const vm = app.sbx.form({ name: 'fresh' });
  assert.equal(vm.f.visibility, 'team');
  const made = await app.sbx.create(vm.f);
  const post = JSON.parse(calls.filter((c) => c.method === 'POST' && c.url.endsWith('/sandboxes')).pop().body);
  assert.equal(post.conversation, 5);
  assert.ok(post.clientId, 'a retry returns the same sandbox');
  assert.equal(made.name, 'fresh');
  assert.equal(app.sbx.badge().name, 'fresh');
  assert.ok(app.sbx.list.sandboxes.some((s) => s.ref === `${MGR}|fresh`), 'in the list at once');

  // lifecycle: as yourself, or through the conversation for one bound here you may not use
  await app.sbx.act(`${MGR}|api`, 'stop');
  let life = calls.filter((c) => /\/(stop|start)\?/.test(c.url)).pop();
  assert.equal(life.url, '/api/apps/agent/sandboxes/apps/coding-sandbox%7Capi/stop?wait=20', 'slashes as they are, | encoded');
  await app.sbx.act(`${MGR}|theirs`, 'start');
  life = calls.filter((c) => /\/(stop|start)\?/.test(c.url)).pop();
  assert.match(life.url, /theirs\/start\?wait=20&conversation=5$/);

  // a run event that carries the binding: the view follows; a changed count reads it again
  const reads = () => calls.filter((c) => c.url.endsWith('/runs/5/view?limit=1')).length;
  const before = reads();
  app.event({ type: 'run', run: 5, root: 5, data: { id: 5, sandbox: { ref: `${MGR}|fresh`, name: 'fresh', cwd: '/srv' }, attached: 2 } });
  assert.equal(app.sbx.badge().label, '▣ fresh · /srv', 'at once');
  assert.equal(reads(), before, 'the same attached count: nothing to read');
  app.event({ type: 'run', run: 5, root: 5, data: { id: 5, sandbox: null, attached: 1 } });
  assert.equal(app.sbx.badge(), null);
  assert.equal(reads(), before + 1, 'the count changed: read again');
  app.event({ type: 'run', run: 5, root: 5, data: { id: 5, status: 'running' } });
  assert.equal(reads(), before + 1, 'an event without a binding changes nothing');
  assert.ok(heard > 5);
  await new Promise((r) => setTimeout(r, 20));
});
