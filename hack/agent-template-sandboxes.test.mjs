// hack/agent-template-sandboxes.test.mjs — coding sandboxes (D115) in the
// agent template's shared model: the composer's picker
// (builtin-templates/agent/model/sandboxes.js sandboxPicker), the ▣ badge
// and why a binding no longer resolves, the Sandboxes dialog's rows and
// their actions, the create form, the sandbox tool cards
// (model/tool-heads.js: the box family, its sublines and outcomes), and the
// app's store (model/sandbox-store.js: picking, binding, detaching,
// creating, lifecycle, the run events that carry a binding) and terminals
// (a manager's `tty`: whether one is offered, its route, ending its shell).
// Both views draw from these; the browser tests (test/sandbox.mjs,
// test/terminal.mjs) check the drawing. Run by `make js-test`.
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
  // a person's partition: the team's sandboxes are listed there (homed false,
  // the backend's why) — shown, not offered for a conversation of hers
  const why = 'team isn\'t a sandbox of your own space (the team\'s, …): create one, or open this one\'s terminal instead';
  const P = list([sb('mine-new', { homed: true }), sb('team', { mine: false, visibility: 'team', owner: { user: '' }, canManage: false,
    canEdit: false, shared: true, homed: false, why })]);
  const pp = S.sandboxPicker(P, conv({}), { user: 'alice' });
  const trow = pp.groups.flatMap((g) => g.rows).find((r) => r.name === 'team');
  assert.equal(trow.disabled, true, 'a sandbox not homed in her partition');
  assert.equal(trow.why, why, 'the backend\'s words');
  assert.equal(pp.groups.flatMap((g) => g.rows).find((r) => r.name === 'mine-new').disabled, false, 'her own');
  const drows = S.sandboxRows(P, { user: 'alice' }, { conv: conv({}) });
  assert.ok(!drows.find((r) => r.name === 'team').actions.some((a) => a.id === 'use'), 'the dialog offers no Use here for it');
  assert.ok(drows.find((r) => r.name === 'mine-new').actions.some((a) => a.id === 'use'), '…and does for her own');

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
  assert.equal(cold.stale, '', 'not read yet: nothing to say about the pick');
  assert.equal(cold.groups[0].rows[0].why, '');
  // a pick the loaded list no longer has says why (review: a stale new-chat pick)
  const gone = S.sandboxPicker(L, null, {}, { cls: coding, pick: { ref: `${MGR}|x`, name: 'x' } });
  assert.match(gone.stale, /^gone/);
  assert.deepEqual(gone.groups[0].rows.map((r) => [r.value, r.on, r.label]), [[`${MGR}|x`, true, 'x · unavailable']]);
  assert.match(gone.groups[0].rows[0].why, /^gone/);
  assert.match(gone.notes[0], /^x: gone .* — pick another$/);
  // listed, but no longer yours to use (made private, you removed): not "gone"
  const locked = S.sandboxPicker(list([sb('x', { mine: false, canUse: false, canManage: false })]), null, {}, { cls: coding, pick: { ref: `${MGR}|x`, name: 'x' } });
  assert.equal(locked.stale, 'you may no longer use it');
  assert.deepEqual(locked.groups.map((g) => g.id), ['picked']);
  const unbound = S.sandboxPicker(list([], []), null, {}, { cls: coding, pick: { ref: `${MGR}|x`, name: 'x' } });
  assert.match(unbound.stale, /its manager \(apps\/coding-sandbox\) is no longer bound/);
  // a viewer's New says why (review: a viewer was offered New "for this conversation")
  const ro = S.sandboxPicker(L, { ...v, access: 'viewer' }, {});
  assert.deepEqual([ro.actions[0].disabled, ro.actions[0].why], [true, S.VIEW_ONLY]);
});

test('who may make one here; binding a private one into a shared conversation; what an ask refused', () => {
  const L = list([sb('priv'), sb('team', { visibility: 'team' }), sb('held', { boundTo: [5] })]);
  assert.equal(S.createWhy(L, conv({})), '');
  assert.equal(S.createWhy(L, null), '');
  assert.equal(S.createWhy(L, conv({}, { access: 'viewer' })), S.VIEW_ONLY, 'a viewer: a sandbox made here is made for it');
  assert.equal(S.createWhy(S.listOf(null), null), 'the sandboxes are still being read');
  assert.equal(S.createWhy(list([], []), null), 'no sandbox manager is bound');
  assert.equal(S.createWhy(list([], [{ provider: MGR, ok: false }]), null), 'no sandbox manager is available right now');
  // a conversation other people are in: a private one asks first
  const team = conv({}, { run: { id: 5, rootId: 5, status: 'idle', visibility: 'team' } });
  assert.equal(S.sharedConv(team), true);
  assert.equal(S.sharedConv(conv({}, { acl: { visibility: 'private', members: [{ user: 'bob', role: 'participant' }] } })), true, 'shared with people');
  assert.equal(S.sharedConv(conv({})), false);
  assert.match(S.bindConfirm(L, team, `${MGR}|priv`), /^“priv” is private — people in this conversation will be able to work in it/);
  assert.equal(S.bindConfirm(L, team, `${MGR}|team`), '', 'a team one: nothing to ask');
  assert.equal(S.bindConfirm(L, team, `${MGR}|held`), '', 'one it already holds: nothing to ask');
  assert.equal(S.bindConfirm(L, conv({}), `${MGR}|priv`), '', 'a private conversation: nothing to ask');
  assert.equal(S.bindConfirm(L, null, `${MGR}|priv`), '', 'at home: nothing to ask');
  // an ask refused for its sandbox, or for something else
  const err = (status, message, refusal) => Object.assign(new Error(message), { status }, refusal ? { refusal } : {});
  assert.equal(S.askRefusal(err(404, 'no sandbox web', 'not-found')), 'no sandbox web');
  assert.equal(S.askRefusal(err(502, 'the manager is down', 'unavailable')), 'the manager is down');
  assert.equal(S.askRefusal(err(403, 'this conversation\'s class (Coding) doesn\'t allow a sandbox with egress "open"')), 'this conversation\'s class (Coding) doesn\'t allow a sandbox with egress "open"');
  assert.equal(S.askRefusal(err(403, 'you may not use this sandbox (web) — its owner can add you as a member')), 'you may not use this sandbox (web) — its owner can add you as a member');
  const held = 'this sandbox has held data from an internal-reach conversation, so a conversation of a class that reaches outside (Coding) can\'t work in it — use another sandbox';
  assert.equal(S.askRefusal(err(403, held)), held, 'the internal-data mark (the access review fixes)');
  assert.equal(S.askRefusal(err(429, 'too many', 'limit')), '', 'a limit is not the pick\'s fault');
  assert.equal(S.askRefusal(err(400, 'need {text}')), '', 'not about the sandbox');
  assert.equal(S.askRefusal(err(403, 'the Sandbox class is for the agent\'s managers')), '', 'a class\'s refusal, whatever its name');
  assert.equal(S.askRefusal(err(400, 'sandbox.cwd: /nope doesn\'t exist in the sandbox')), 'sandbox.cwd: /nope doesn\'t exist in the sandbox');
  assert.equal(S.askRefusal(new Error('offline')), '', 'no status: not a refusal');
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
  assert.deepEqual([b.fixed, b.advice], [false, 'pick another, or detach it']);
  // a coding agent's conversation keeps its sandbox (the backend refuses a change): nothing to change, a new chat instead
  const f = S.sandboxBadge(v, list([sb('web')]), undefined, { fixed: 'Codex' });
  assert.deepEqual([f.canChange, f.fixed, f.advice], [false, true, 'start a new chat with Codex in another sandbox']);
  assert.deepEqual([f.talk, b.talk, S.sandboxBadge({ ...v, access: 'viewer' }, list([sb('api')]), undefined, { fixed: 'Codex' }).talk], [true, true, false],
    'a participant still talks there (the ▣ popover\'s Ports, D135) — a viewer does not');
  assert.match(f.broken, /^gone/);
  assert.match(S.sandboxBadge(v, list([sb('api')]), undefined, { fixed: 'Codex' }).title, /— fixed for this conversation$/);
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
  assert.deepEqual(acts('run'), ['stop', 'archive', 'team', 'shareTerm', 'delete'], 'the active one: no "use"');
  assert.deepEqual(acts('stop'), ['use', 'start', 'archive', 'team', 'shareTerm', 'delete']);
  assert.deepEqual(acts('arch'), ['use', 'thaw', 'team', 'shareTerm', 'delete']);
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

  // one bound here you may neither use nor manage: start/stop/thaw through the
  // conversation, never archive (review: ?conversation= was never offered)
  const bobs = { mine: false, owner: { user: 'bob' }, canUse: false, canManage: false, canEdit: false, boundTo: [5] };
  const T = list([sb('b-stopped', { ...bobs, state: 'stopped' }), sb('b-running', { ...bobs }), sb('b-archived', { ...bobs, state: 'archived' }),
    sb('b-elsewhere', { ...bobs, state: 'stopped', boundTo: [6] })]);
  const through = Object.fromEntries(S.sandboxRows(T, { user: 'carol' }, { conv: conv({ sandbox: bind('b-running') }) }).map((r) => [r.name, r.actions]));
  assert.deepEqual(through['b-stopped'].map((a) => [a.id, a.via]), [['start', true]]);
  assert.deepEqual(through['b-running'].map((a) => [a.id, a.via]), [['stop', true]]);
  assert.deepEqual(through['b-archived'].map((a) => [a.id, a.via]), [['thaw', true]]);
  assert.deepEqual(through['b-elsewhere'], [], 'bound to another conversation: nothing');
  assert.ok(S.sandboxRows(T, {}, { conv: conv({ sandbox: bind('b-running') }, { access: 'viewer' }) }).every((r) => !r.actions.length), 'a viewer: nothing through it');
  assert.equal(S.viaConv(T.sandboxes[0], conv({})), 5);
  assert.equal(S.viaConv(sb('mine'), conv({})), undefined, 'one you may use: as yourself');

  // "Use here" of a private one in a conversation other people are in asks first
  const team = conv({ sandbox: bind('run') }, { run: { id: 5, rootId: 5, status: 'idle', visibility: 'team' } });
  const tr = Object.fromEntries(S.sandboxRows(L, {}, { conv: team }).map((r) => [r.name, r.actions.find((a) => a.id === 'use')]));
  assert.match(tr.stop.confirm, /^“stop” is private — people in this conversation will be able to work in it/);
  assert.equal(tr.theirs.confirm, undefined, 'a team one: no question');

  // the order a view shows them in is kept; the ones it hasn't shown come after
  const shown = ['noarch', 'run', 'theirs'].map((n) => `${MGR}|${n}`);
  const kept = S.sandboxRows(L, {}, { conv: conv({ sandbox: bind('run') }), order: shown }).map((r) => r.name);
  assert.deepEqual(kept, ['noarch', 'run', 'theirs', 'arch', 'stop'], 'shown first as shown, new ones after in their own order');
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
  assert.equal(T.ICON.box, 'box', 'a /vendor/bx-icons.js name (D184)');
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
  // D134: a kill-by-name command isn't run; bash_kill ends on its footer; an
  // interrupted bash keeps its output (stopped, not done); jobs counts
  assert.deepEqual(T.outcome('bash', 'not run: stop jobs with bash_kill {"job": N}; pkill -f/killall match …\nThis conversation has no jobs.'),
    { text: 'not run: kills by name', tone: 'bad' });
  assert.deepEqual(T.outcome('bash_kill', 'line 1999\nline 2000\n[job 1 stopped · killed by TERM]'), { text: 'job 1 stopped · killed by TERM', tone: '' });
  assert.deepEqual(T.outcome('bash_kill', 'sent TERM to job 2; it is still running — bash_kill {"job": 2, "signal": "KILL"} forces it').tone, 'run');
  const cut = 'partial-out\n[interrupted by the owner · job 1 got TERM (then KILL, if it outlives a few seconds) — bash_output {"job": 1} shows the rest and how it ended]';
  assert.equal(T.resultState(cut), 'stopped', 'an interrupted command with its output is still stopped');
  assert.deepEqual(T.outcome('jobs', 'job 3 · running · 4s so far · sleep 30 (in /w)\njob 2 · exit 0 · ran 1s · ended 2s ago · make (in /w)\n[bash_output …]'),
    { text: '1 running', tone: 'run' });
  assert.equal(T.headline('jobs', a({})), 'List the jobs');
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
  // D136: browser_check's headline, sandbox_download in place
  const bc = 'browser_check file:///w/index.html — loaded in 12 ms · status 200 · 3 console message(s), 1 error(s) · 0 page error(s) · 2 failed request(s)\n{}';
  assert.deepEqual(T.outcome('browser_check', bc), { text: '1 console error · 2 failed requests', tone: 'bad' });
  assert.deepEqual(T.outcome('browser_check', 'browser_check http://localhost:8080/ — loaded in 9 ms · status 200 · 0 console message(s), 0 error(s) · 0 page error(s) · 0 failed request(s)\n{}'),
    { text: 'no errors', tone: 'ok' });
  assert.deepEqual(T.outcome('browser_check', 'browser_check http://localhost:1/ — did not load: net::ERR_CONNECTION_REFUSED · 0 console message(s), 0 error(s) · 0 page error(s) · 1 failed request(s)'),
    { text: 'did not load', tone: 'bad' });
  assert.equal(T.headline('browser_check', '{"target":"./index.html","script":"return 1"}'), 'Check ./index.html in a browser (+ script)');
  assert.equal(T.family('browser_check'), 'box');
  assert.equal(T.outcome('sandbox_download', 'unchanged: the session file r.html already holds /w/r.html (sha abc, v2) — nothing written').text, 'unchanged');
  assert.equal(T.outcome('sandbox_download', 'downloaded /w/r.html to the session file r.html (text, 21 B) — v2, replacing v1, copied from …; file_diff {"a": "r.html"} shows what changed\n\nsession files: …').text, 'v2 (was v1)');
  assert.equal(T.outcome('sandbox_download', 'downloaded /w/r.html to the session file r.html (text, 21 B)\n\nsession files: …').text, 'text, 21 B');
  assert.equal(T.headline('file_diff', '{"a":"r.html"}'), 'Diff r.html → its previous version');
  assert.equal(T.family('file_info'), 'file');
});

test('the store: pick at home, bind, cwd, detach, create, lifecycle, run events', async () => {
  const calls = [];
  const cfgs = { 5: { sandbox: bind('api'), attached: [bind('api')] } };
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const L = { sandboxes: [sb('api'), sb('web', { boundTo: [], workdir: '/work' }), sb('theirs', { mine: false, canUse: false, canManage: false, canEdit: false, state: 'stopped', boundTo: [5] }),
    sb('wide', { egress: 'open' })], managers: managers() };
  let viewClass = coding; // the conversation's class as the backend resolves it now (by id)
  const fake = async (url, opt = {}) => {
    const method = opt.method || 'GET';
    const u = String(url);
    calls.push({ method, url: u, body: opt.body });
    if (u.endsWith('/prefs/class')) return json('coding');
    if (/\/prefs\//.test(u)) return json({}, 404);
    if (u.endsWith('/classes') && method === 'PUT') {
      const b = JSON.parse(opt.body);
      viewClass = b.classes.find((c) => c.id === 'coding');
      return json({ default: 'internal', classes: [internal, viewClass] });
    }
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
    if (vw) return json({ cursor: 'g.1', run: { id: +vw[1], rootId: +vw[1], status: 'idle', visibility: 'team' }, access: 'owner', class: viewClass,
      config: JSON.parse(JSON.stringify(cfgs[vw[1]] || {})), messages: [], steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {} });
    if (u.endsWith('/ask')) {
      const b = JSON.parse(opt.body);
      if (b.sandbox && b.sandbox.ref.endsWith('|gone')) return json({ error: 'sandbox gone: no such sandbox', refusal: 'not-found' }, 404);
      return json({ id: 9, title: 'x', status: 'running', rootId: 9 });
    }
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
  assert.deepEqual([app.sbx.badge().canChange, app.sbx.badge().fixed], [true, false]);
  const cv = app.session.current();
  const hb = app.sbx.badge({ ...cv, run: { ...cv.run, engine: 'harness', harness: { provider: 'claude', name: 'Claude Code' } } });
  assert.deepEqual([hb.label, hb.canChange, hb.fixed, hb.advice], ['▣ api · /work', false, true, 'start a new chat with Claude Code in another sandbox'],
    'a coding agent\'s conversation: its sandbox is fixed');
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
  const lastPatch = () => JSON.parse(calls.filter((c) => c.method === 'PATCH').pop().body);
  assert.deepEqual(lastPatch(), { sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } });
  assert.equal(app.sbx.badge().label, '▣ web · /work/web');
  // re-picking an attached one keeps its working directory — from the picker
  // and from "Use here" (review: both reset it to the workdir)
  await app.sbx.choose(`${MGR}|api`);
  await app.sbx.choose(`${MGR}|web`);
  assert.deepEqual(lastPatch(), { sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } }, 'the picker sends its stored cwd');
  assert.equal(app.sbx.badge().label, '▣ web · /work/web');
  await app.sbx.perform(`${MGR}|api`, 'use');
  await app.sbx.perform(`${MGR}|web`, 'use');
  assert.deepEqual(lastPatch(), { sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } }, '"Use here" too');
  await app.sbx.choose(`${MGR}|api`, '/srv');
  assert.deepEqual(lastPatch(), { sandbox: { ref: `${MGR}|api`, cwd: '/srv' } }, 'a cwd named wins');
  await app.sbx.choose(`${MGR}|web`);
  // an empty directory is the sandbox's workdir, named when the list knows it
  await app.sbx.setCwd('');
  assert.deepEqual(lastPatch(), { sandbox: { ref: `${MGR}|web`, cwd: '/work' } });
  await app.sbx.setCwd('/work/web');
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

  // lifecycle: as yourself, or through the conversation for one bound here
  // you may not use — as the dialog's row offers it
  await app.sbx.act(`${MGR}|api`, 'stop');
  let life = calls.filter((c) => /\/(stop|start)\?/.test(c.url)).pop();
  assert.equal(life.url, '/api/apps/agent/sandboxes/apps/coding-sandbox%7Capi/stop?wait=20', 'slashes as they are, | encoded');
  const theirs = app.sbx.rows().find((r) => r.name === 'theirs');
  assert.deepEqual(theirs.actions.map((a) => [a.id, a.via]), [['start', true]], 'the row offers Start through the conversation');
  await app.sbx.perform(theirs.ref, theirs.actions[0].id, theirs.name);
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

  // a class edit reaches the open conversation's class: its warning, and
  // what its sandboxes may be (review: they stayed as the view was loaded)
  const wideWhy = () => app.sbx.picker().groups.flatMap((g) => g.rows).find((r) => r.name === 'wide').why;
  assert.match(wideWhy(), /doesn't allow a sandbox with open network/);
  const edited = { ...coding, toolsets: [...coding.toolsets, 'internal'], sandboxEgress: ['none', 'internet', 'open'], mixed: true };
  await app.saveClasses({ classes: [edited], default: 'internal', confirmMixed: true });
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(app.session.current().class.mixed, true, 'the view\'s class is read again');
  assert.equal(wideWhy(), '', 'an open-network sandbox is allowed now');

  // a view-only conversation: no sandbox is made for it (review: New was offered, then refused)
  app.session.views.get(5).access = 'viewer';
  const posts = calls.filter((c) => c.method === 'POST' && c.url.endsWith('/sandboxes')).length;
  assert.equal(app.sbx.createWhy(), S.VIEW_ONLY);
  await assert.rejects(app.sbx.create({ name: 'nope', provider: MGR, egress: 'none', visibility: 'private' }), /You may only read this conversation/);
  assert.equal(calls.filter((c) => c.method === 'POST' && c.url.endsWith('/sandboxes')).length, posts, 'nothing sent');
  app.session.views.get(5).access = 'owner';

  // a stale new-chat pick: the ask is refused for it — the typed message
  // stays, the pick is dropped and says why; the next ask goes without it
  // (review: every new chat failed, and the first message was thrown away)
  app.home();
  await app.sbx.choose(`${MGR}|gone`);
  assert.equal(app.sbx.pick.ref, `${MGR}|gone`);
  assert.match(app.sbx.picker().stale, /^gone/, 'the loaded list says why before it is sent');
  let failed = null;
  app.on('error', (e) => { failed = e; });
  let cleared = 0;
  await app.send('a long first message', () => { cleared++; });
  assert.equal(cleared, 0, 'the composer keeps the text');
  assert.equal(app.sbx.pick, null, 'the pick is dropped');
  assert.match(failed.message, /^The sandbox gone can't be used: sandbox gone: no such sandbox\. Your next new chat starts without one/);
  await app.send('a long first message', () => { cleared++; });
  ask = JSON.parse(calls.filter((c) => c.url.endsWith('/ask')).pop().body);
  assert.equal(ask.sandbox, undefined, 'the next ask names none');
  assert.equal(cleared, 1, 'sent: the composer empties');
  await new Promise((r) => setTimeout(r, 20));
});

test('terminals: offered where the manager has tty and the page is bound to it; the route; the rows', () => {
  const tty = () => managers().map((m) => ({ ...m, caps: [...m.caps, 'tty'] }));
  const EPS = [{ provider: MGR, url: '/api/apps/coding-sandbox' }, { provider: 'apps/other', instance: 'eu', url: '/api/apps/other/eu/' }];
  assert.equal(S.endpointOf(EPS, MGR).url, '/api/apps/coding-sandbox');
  assert.equal(S.endpointOf(EPS, 'apps/other#eu').url, '/api/apps/other/eu/', 'an instance: <tile>#<inst>');
  assert.equal(S.endpointOf(EPS, 'apps/other'), null);
  assert.equal(S.endpointOf(null, MGR), null);
  assert.equal(S.terminalSrc({ url: '/api/apps/other/eu/' }, 'b 1', '/work/my dir'), '/api/apps/other/eu/sbx/sandboxes/b%201/tty?cwd=%2Fwork%2Fmy%20dir');
  assert.equal(S.terminalSrc({ url: '/api/m' }, 'b1', ' '), '/api/m/sbx/sandboxes/b1/tty', 'no cwd: its workdir');
  assert.equal(S.execSrc({ url: '/api/m/' }, 'b1', 'e3'), '/api/m/sbx/sandboxes/b1/execs/e3');

  const L = list([sb('run', { caps: undefined }), sb('stop', { state: 'stopped', caps: undefined }), sb('arch', { state: 'archived', caps: undefined }),
    sb('bobs', { mine: false, canUse: false, canManage: true, caps: undefined }), sb('notty', { caps: ['exec', 'files'] }),
    sb('busy', { state: 'deleting', caps: undefined })], tty());
  const t = S.terminal(L, `${MGR}|run`, EPS, '/work/api');
  assert.deepEqual([t.shown, t.why, t.src, t.base, t.name, t.cwd], [true, '', '/api/apps/coding-sandbox/sbx/sandboxes/run/tty?cwd=%2Fwork%2Fapi',
    '/api/apps/coding-sandbox', 'run', '/work/api']);
  assert.equal(S.terminal(L, `${MGR}|stop`, EPS).why, '', 'a stopped one starts on it');
  assert.match(S.terminal(L, `${MGR}|arch`, EPS).why, /^it is archived — thaw it first$/);
  assert.match(S.terminal(L, `${MGR}|busy`, EPS).why, /^it is deleting…$/);
  assert.equal(S.terminal(L, `${MGR}|bobs`, EPS).why, 'you may not use it yourself', 'managing it is not using it: the manager checks you');
  assert.deepEqual([S.terminal(L, `${MGR}|notty`, EPS).shown], [false], 'the sandbox leaves tty out');
  assert.equal(S.terminal(L, `${MGR}|run`, []).why, 'this page is not bound to its manager — reload it');
  assert.equal(S.terminal(L, `${MGR}|gone`, EPS).why, 'gone — its manager no longer has it');
  assert.equal(S.terminal(L, `${MGR}|run`, null).shown, false, 'a view without terminals (the native one): not shown');
  assert.equal(S.terminal(list([sb('run', { caps: undefined })]), `${MGR}|run`, EPS).shown, false, 'no tty in hello: not shown');
  assert.equal(S.terminal(null, `${MGR}|run`, EPS).shown, false, 'not read yet: not shown');

  // the dialog's rows: "Terminal" where it is offered, at the cwd the open conversation has it at
  const v = conv({ sandbox: bind('run', { cwd: '/work/api' }), attached: [bind('run', { cwd: '/work/api' })] });
  const rows = Object.fromEntries(S.sandboxRows(L, { user: 'alice' }, { conv: v, tty: EPS }).map((r) => [r.name, r.actions.find((a) => a.id === 'terminal')]));
  assert.deepEqual(rows.run, { id: 'terminal', label: 'Terminal', cwd: '/work/api' });
  assert.deepEqual(rows.stop, { id: 'terminal', label: 'Terminal', cwd: '' }, 'not in this conversation: its workdir');
  assert.deepEqual([rows.arch, rows.bobs, rows.notty, rows.busy], [undefined, undefined, undefined, undefined]);
  assert.ok(!S.sandboxRows(L, {}, { conv: v }).some((r) => r.actions.some((a) => a.id === 'terminal')), 'no tty endpoints: never');

  // a command (a coding agent's sign-in) and the tile's own relay (D147 §4.2.8: the native view's)
  assert.equal(S.terminal(L, `${MGR}|run`, EPS, '/work/api', 'codex login').src,
    '/api/apps/coding-sandbox/sbx/sandboxes/run/tty?cwd=%2Fwork%2Fapi&cmd=codex%20login');
  assert.equal(S.terminalSrc({ url: '/api/m' }, 'b1', '', 'CLAUDE_CODE_REMOTE=1 claude /login'), '/api/m/sbx/sandboxes/b1/tty?cmd=CLAUDE_CODE_REMOTE%3D1%20claude%20%2Flogin');
  const r = S.terminal(L, `${MGR}|run`, S.RELAY, '/work/api');
  assert.deepEqual([r.shown, r.why, r.relay, r.base, r.src], [true, '', true, '', 'sandboxes/apps/coding-sandbox%7Crun/terminal?cwd=%2Fwork%2Fapi'], 'tile-relative');
  assert.equal(S.relaySrc(`${MGR}|run`, '', 'gemini'), 'sandboxes/apps/coding-sandbox%7Crun/terminal?cmd=gemini');
  assert.equal(S.terminal(L, `${MGR}|bobs`, S.RELAY).why, 'you may not use it yourself', 'the relay checks you the same');
  assert.match(S.terminal(L, `${MGR}|arch`, S.RELAY).why, /thaw it first/);
  assert.equal(S.terminal(L, `${MGR}|notty`, S.RELAY).shown, false, 'no tty: no relay either');
  const relayed = Object.fromEntries(S.sandboxRows(L, { user: 'alice' }, { conv: v, tty: S.RELAY }).map((r) => [r.name, r.actions.find((a) => a.id === 'terminal')]));
  assert.deepEqual([relayed.run, relayed.bobs], [{ id: 'terminal', label: 'Terminal', cwd: '/work/api' }, undefined], 'the native view\'s rows');
});

test('the store: terminals only where a view set tty; ending one DELETEs its exec at the manager', async () => {
  const calls = [];
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  const mgrs = managers().map((m) => ({ ...m, caps: [...m.caps, 'tty'] }));
  const fake = async (url, opt = {}) => {
    const u = String(url);
    calls.push({ method: opt.method || 'GET', url: u });
    if (/\/sandboxes(\?fresh=1)?$/.test(u)) return json({ sandboxes: [sb('api', { caps: undefined })], managers: mgrs });
    if (u.includes('/sbx/sandboxes/api/execs/gone')) return json({ error: 'no such exec', refusal: 'not-found' }, 404);
    if (u.includes('/sbx/sandboxes/api/execs/deny')) return json({ error: 'not yours', refusal: 'not-allowed' }, 403);
    if (u.endsWith('/classes')) return json({ default: 'coding', classes: [coding] });
    if (u.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
    return json({});
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const { createApp } = await import(new URL('model/app.js', TPL).href);
  const app = createApp({ frame: (fn) => setTimeout(fn, 0) });
  await app.sbx.load();
  assert.equal(app.sbx.tty, null, 'no view asked for terminals');
  assert.equal(app.sbx.terminal(`${MGR}|api`).shown, false);
  assert.ok(!app.sbx.rows().some((r) => r.actions.some((a) => a.id === 'terminal')));
  app.sbx.tty = [{ provider: MGR, url: '/api/apps/coding-sandbox' }];
  const t = app.sbx.terminal(`${MGR}|api`, '/work');
  assert.equal(t.src, '/api/apps/coding-sandbox/sbx/sandboxes/api/tty?cwd=%2Fwork');
  assert.ok(app.sbx.rows().find((r) => r.name === 'api').actions.some((a) => a.id === 'terminal'));
  await app.sbx.endTerminal(t, 'e4');
  assert.deepEqual(calls.filter((c) => c.method === 'DELETE').map((c) => c.url), ['/api/apps/coding-sandbox/sbx/sandboxes/api/execs/e4'],
    'straight to the manager, as the page (not through the agent\'s backend)');
  await app.sbx.endTerminal(t, 'gone');
  await assert.rejects(app.sbx.endTerminal(t, 'deny'), /error 403/);
  await app.sbx.endTerminal(t, '');
  await app.sbx.endTerminal({ ...t, base: '' }, 'e5');
  assert.equal(calls.filter((c) => c.method === 'DELETE').length, 3, 'nothing to end without an exec or a manager');
});

test('sharing with a terminal tile (D121): who may, for whom, the body; stop sharing', () => {
  const mine = sb('mine', { shares: [{ consumer: 'apps/other', users: '*' }] });
  const team = sb('team', { visibility: 'team', shares: [] });
  const theirs = sb('theirs', { mine: false, owner: { user: 'bob' }, canEdit: false });
  const passed = sb('passed', { shared: true }); // another consumer shared it with this agent
  assert.deepEqual([mine, team, theirs, passed].map(S.canShareOut), [true, true, false, false]);
  const acts = (s) => S.sandboxRows(list([s]), { user: 'alice' }, {})[0].actions.map((a) => a.id);
  assert.ok(acts(mine).includes('shareTerm') && !acts(theirs).includes('shareTerm') && !acts(passed).includes('shareTerm'),
    'offered only where you own it and this agent is its home');
  assert.deepEqual(S.sandboxRows(list([mine]), { user: 'alice' }, {})[0].sharedWith, ['apps/other']);

  // a private one: for you; the default tile path; the other shares kept
  let vm = S.shareForm(mine, { user: 'alice' }, {}, 'apps/agent');
  assert.deepEqual([vm.tile, vm.users, vm.usersLabel, vm.ok], [S.TERMINAL_TILE, ['alice'], 'you', true]);
  assert.deepEqual(vm.body, { shares: [{ consumer: 'apps/other', users: '*' }, { consumer: 'apps/sandbox-terminal', users: ['alice'] }] });
  assert.deepEqual(vm.current, [{ consumer: 'apps/other', users: '*', usersLabel: 'everyone who may use it' }]);
  // one already shared with that tile for others: you join them; "*" stays "*"
  const had = sb('had', { shares: [{ consumer: 'apps/sandbox-terminal', users: ['bob'] }] });
  assert.deepEqual(S.shareForm(had, { user: 'alice' }).body.shares, [{ consumer: 'apps/sandbox-terminal', users: ['bob', 'alice'] }]);
  assert.equal(S.shareForm(had, { user: 'alice' }).current[0].usersLabel, 'bob');
  assert.equal(S.shareForm(sb('star', { shares: [{ consumer: 'apps/term', users: '*' }] }), { user: 'alice' }, { tile: 'apps/term' }).users, '*');
  // a team one: everyone who may use it
  vm = S.shareForm(team, { user: 'alice' }, { tile: ' apps/term/ ' });
  assert.deepEqual([vm.tile, vm.users, vm.usersLabel], ['apps/term', '*', 'everyone who may use it (a team sandbox)']);
  assert.deepEqual(vm.body, { shares: [{ consumer: 'apps/term', users: '*' }] });
  // what is wrong
  assert.match(S.shareForm(mine, { user: 'alice' }, { tile: '' }).error, /Name the terminal tile/);
  assert.match(S.shareForm(mine, { user: 'alice' }, { tile: 'apps/../x' }).error, /path is like apps\/sandbox-terminal/);
  assert.match(S.shareForm(mine, { user: 'alice' }, { tile: 'apps/x y' }).error, /path is like/);
  assert.match(S.shareForm(mine, { user: 'alice' }, { tile: 'apps/agent' }, 'apps/agent').error, /That is this agent/);
  assert.match(S.shareForm(theirs, { user: 'alice' }).error, /only its owner/);
  assert.match(S.shareForm(passed, { user: 'alice' }).error, /only its home can share it on/);
  assert.match(S.shareForm(null, { user: 'alice' }).error, /^gone/);
  assert.match(S.shareForm(mine, {}).error, /Who you are/);
  assert.deepEqual(S.unshareBody(mine, 'apps/other'), { shares: [] });
  assert.deepEqual(S.unshareBody(sb('none'), 'apps/other'), { shares: [] });
});

test('the store: sharing with a terminal tile PATCHes the sandbox\'s shares (its owner)', async () => {
  const calls = [];
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  let box = sb('api', { shares: [] });
  const fake = async (url, opt = {}) => {
    const u = String(url);
    const method = opt.method || 'GET';
    calls.push({ method, url: u, body: opt.body });
    if (/\/sandboxes(\?fresh=1)?$/.test(u)) return json({ sandboxes: [box], managers: managers() });
    if (method === 'PATCH' && u.includes('/sandboxes/')) { box = { ...box, ...JSON.parse(opt.body) }; return json(box); }
    if (u.endsWith('/me')) return json({ kind: 'user', user: 'alice', manager: false });
    if (u.endsWith('/classes')) return json({ default: 'coding', classes: [coding] });
    if (u.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
    return json({});
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const { createApp } = await import(new URL('model/app.js', TPL).href + '?share');
  const app = createApp({ frame: (fn) => setTimeout(fn, 0) });
  app.me = { user: 'alice' };
  await app.sbx.load();
  const ref = `${MGR}|api`;
  assert.match(app.sbx.shareForm(ref, { tile: 'apps/agent' }).error, /That is this agent/, 'the agent knows its own path');
  const said = await app.sbx.shareTerminal(ref, {});
  assert.match(said, /^api is shared with apps\/sandbox-terminal — you can open terminals onto it there/);
  const patch = calls.filter((c) => c.method === 'PATCH');
  assert.deepEqual(patch.map((c) => [c.url, JSON.parse(c.body)]),
    [[`/api/apps/agent/sandboxes/apps/coding-sandbox%7Capi`, { shares: [{ consumer: 'apps/sandbox-terminal', users: ['alice'] }] }]]);
  assert.deepEqual(app.sbx.rows()[0].sharedWith, ['apps/sandbox-terminal'], 'the answer lands in the list');
  await assert.rejects(app.sbx.shareTerminal(ref, { tile: '' }), /^Error: Name the terminal tile/);
  await app.sbx.unshare(ref, 'apps/sandbox-terminal');
  assert.deepEqual(JSON.parse(calls.filter((c) => c.method === 'PATCH').pop().body), { shares: [] });
  assert.deepEqual(app.sbx.rows()[0].sharedWith, []);
});

test('the store: a share goes with the version it was read at; a 412 is read again and retried once', async () => {
  const calls = [];
  const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
  let box = sb('api', { shares: [], version: 3 });
  let stale = 0; // PATCHes still to refuse whatever they carry (a manager that keeps moving)
  const fake = async (url, opt = {}) => {
    const u = String(url);
    const method = opt.method || 'GET';
    calls.push({ method, url: u, body: opt.body });
    if (/\/sandboxes(\?fresh=1)?$/.test(u)) return json({ sandboxes: [box], managers: managers() });
    if (u.endsWith('/sandboxes/apps/coding-sandbox%7Capi')) {
      if (method === 'GET') return json(box);
      if (method === 'PATCH') {
        const b = JSON.parse(opt.body);
        if (stale > 0 || b.version !== box.version) {
          stale--;
          return json({ error: `apps/coding-sandbox: version ${b.version} is not ${box.version}`, refusal: 'precondition' }, 412);
        }
        box = { ...box, shares: b.shares, version: box.version + 1 };
        return json(box);
      }
    }
    if (u.endsWith('/me')) return json({ kind: 'user', user: 'alice', manager: false });
    if (u.endsWith('/classes')) return json({ default: 'coding', classes: [coding] });
    if (u.includes('/stream')) return new Response(new ReadableStream({ start() {} }), { headers: { 'Content-Type': 'text/event-stream' } });
    return json({});
  };
  globalThis.window = globalThis;
  globalThis.xbin = { self: 'apps/agent', fetch: fake };
  globalThis.fetch = fake;
  const { createApp } = await import(new URL('model/app.js', TPL).href + '?share-version');
  const app = createApp({ frame: (fn) => setTimeout(fn, 0) });
  app.me = { user: 'alice' };
  await app.sbx.load();
  const ref = `${MGR}|api`;
  const sent = () => calls.filter((c) => c.method === 'PATCH').map((c) => JSON.parse(c.body));

  // the pure bodies carry it
  assert.deepEqual(S.shareForm(box, { user: 'alice' }).body, { shares: [{ consumer: 'apps/sandbox-terminal', users: ['alice'] }], version: 3 });
  assert.deepEqual(S.unshareBody(box, 'x'), { shares: [], version: 3 });

  // someone shares it with another tile after the list was read
  box = { ...box, shares: [{ consumer: 'apps/other', users: '*' }], version: 4 };
  await app.sbx.shareTerminal(ref, {});
  assert.deepEqual(sent(), [
    { shares: [{ consumer: 'apps/sandbox-terminal', users: ['alice'] }], version: 3 },
    { shares: [{ consumer: 'apps/other', users: '*' }, { consumer: 'apps/sandbox-terminal', users: ['alice'] }], version: 4 },
  ], 'refused at the version read, then computed afresh from the sandbox read again — the other share kept');
  assert.ok(calls.some((c) => c.method === 'GET' && c.url.endsWith('/sandboxes/apps/coding-sandbox%7Capi')), 'read again');
  assert.deepEqual(box.shares.map((x) => x.consumer), ['apps/other', 'apps/sandbox-terminal']);
  assert.deepEqual(app.sbx.rows()[0].sharedWith, ['apps/other', 'apps/sandbox-terminal'], 'the answer lands in the list');

  // Stop sharing: the version it has now
  await app.sbx.unshare(ref, 'apps/sandbox-terminal');
  assert.deepEqual(sent().pop(), { shares: [{ consumer: 'apps/other', users: '*' }], version: 5 });

  // only once: a second 412 is the caller's
  stale = 2;
  const before = sent().length;
  await assert.rejects(app.sbx.unshare(ref, 'apps/other'), (e) => e.status === 412 && /version/.test(e.message));
  assert.equal(sent().length - before, 2, 'one retry');
  // other refusals aren't retried
  stale = 0;
  const f = globalThis.fetch;
  let n = 0;
  globalThis.xbin.fetch = globalThis.fetch = async (url, opt = {}) => (opt.method === 'PATCH' ? (n++, json({ error: 'only its owner shares it', refusal: 'not-allowed' }, 403)) : f(url, opt));
  await assert.rejects(app.sbx.shareTerminal(ref, {}), /only its owner/);
  assert.equal(n, 1);
});
