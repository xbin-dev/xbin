// hack/agent-template-homes.test.mjs — the agent template's two homes
// (builtin-templates/agent/model/homes.js, home-api.js; API.md "Partitioned
// instances" → "Shared conversations"): in a person's partition a
// conversation's id says where it lives — below 2^40 the global instance
// (reached with xbin.fetch's {partition: 'global'}), from 2^40 their own
// partition — and the model sends every call, and the stream following it,
// there; the list merges both homes; the global instance's stream runs only
// while something shared is open or listed. Unpartitioned pages never ask for
// a partition. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const MODEL = new URL('../builtin-templates/agent/model/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const B = 2 ** 40;
const homes = await import(new URL('homes.js', MODEL).href);
const { byActivity } = await import(new URL('conv-groups.js', MODEL).href);

test('the home of an id, a path, a list view', () => {
  const { homeOf, publishes, runOfPath, listHomes, twoHomes, at } = homes;
  assert.deepEqual(['legacy', 'global', 'user'].map((s) => twoHomes(s)), [false, false, true]);
  assert.equal(homeOf(1, 'user'), 'global');
  assert.equal(homeOf(B - 1, 'user'), 'global');
  assert.equal(homeOf(B, 'user'), '');
  assert.equal(homeOf(B + 7, 'user'), '');
  assert.equal(homeOf(null, 'user'), '');
  assert.equal(homeOf('12', 'user'), 'global');
  for (const s of ['legacy', 'global']) assert.equal(homeOf(1, s), '', `${s}: one home`);
  assert.deepEqual([publishes(B + 1, 'user'), publishes(3, 'user'), publishes(B + 1, 'legacy'), publishes(null, 'user')], [true, false, false, false]);
  assert.equal(runOfPath('/runs/42/view?limit=5'), 42);
  assert.equal(runOfPath('/runs/42'), 42);
  assert.equal(runOfPath('/runs/42?x=1'), 42);
  assert.equal(runOfPath('/runs'), null);
  assert.equal(runOfPath('/runs/4x'), null);
  assert.equal(runOfPath('/conversations?scope=mine'), null);
  assert.deepEqual(listHomes('mine', 'user'), ['', 'global']);
  assert.deepEqual(listHomes('shared', 'user'), ['global']);
  assert.deepEqual(listHomes('shared', 'legacy'), ['']);
  assert.deepEqual(at('global', { method: 'POST' }), { method: 'POST', partition: 'global' });
  const o = { method: 'GET' };
  assert.equal(at('', o), o, 'the own home: the options as they were');
});

test('rows of two homes: newest first, nothing below a row a later page could still come above', () => {
  const r = (id, ms) => ({ id, activityMs: ms });
  const own = [r(B + 3, 900), r(B + 2, 500)];     // more pages follow
  const shared = [r(4, 800), r(3, 300), r(2, 100)]; // the last page
  let { shown, held } = homes.splitRows([...own, ...shared], [own[1]], byActivity);
  assert.deepEqual(shown.map((x) => x.id), [B + 3, 4, B + 2]);
  assert.deepEqual(held.map((x) => x.id), [3, 2], 'older than the own home\'s last row: held until its next page');
  ({ shown, held } = homes.splitRows([...own, ...shared], [], byActivity));
  assert.deepEqual(shown.map((x) => x.id), [B + 3, 4, B + 2, 3, 2]);
  assert.deepEqual(held, []);
});

// --- the model against two scripted homes ---------------------------------------------

const calls = [];
const streams = [];
let seq = 100;
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const view = (id) => ({ cursor: 'g.' + seq, run: { id, title: 'run ' + id, status: 'idle', rootId: id, pendingState: {} },
  messages: [], steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {}, config: {}, messageFiles: {}, access: 'owner' });
const row = (id, ms, extra = {}) => ({ id, title: 'c' + id, status: 'idle', access: 'owner', mine: true, activityMs: ms, readMs: ms, ...extra });
const pages = {
  // alice's own: two pages
  '': { first: { pinned: [], items: [row(B + 3, 9000), row(B + 2, 5000)], next: 'o2' }, more: { pinned: [], items: [row(B + 1, 1000)], next: '' } },
  // the shared space: one page, a pinned one
  global: { first: { pinned: [row(7, 100, { pinnedAt: 5 })], items: [row(5, 8000), row(4, 3000)], next: '' } },
};
const routes = [
  ['GET', /\/me$/, () => json({ kind: 'user', user: 'alice', manager: false, epochMs: 0, partition: 'user:alice' })],
  ['GET', /\/halt$/, () => json({ on: false })],
  ['GET', /\/classes$/, () => json({ default: 'internal', classes: [{ id: 'internal', name: 'Internal', toolsets: ['internal'], lane: 'private' }] })],
  ['GET', /\/needs$/, (m, o) => json({ items: [{ reason: 'question', run: { id: o.partition ? 5 : B + 2, title: 'q' } }] })],
  ['GET', /\/conversations\?(.*)$/, (m, o) => {
    const p = pages[o.partition || ''];
    if (/q=/.test(m[1])) return json({ items: [row(o.partition ? 5 : B + 3, 1)] });
    return json(/cursor=/.test(m[1]) ? p.more : /scope=shared/.test(m[1]) ? { pinned: [], items: [row(5, 8000, { visibility: 'team' })], next: '' } : p.first);
  }],
  ['GET', /\/runs\/(\d+)\/view$/, (m) => json(view(+m[1]))],
  ['POST', /\/runs\/(\d+)\/read$/, () => json({ readMs: Date.now() })],
  ['POST', /\/runs\/(\d+)\/message$/, () => json({ ok: 'true', inboxId: 1, queued: false })],
  ['POST', /\/runs\/(\d+)\/publish$/, () => json({ run: { id: 8, title: 'copy', owner: 'alice' }, deleted: false })],
  ['POST', /\/copy$/, () => json({ id: B + 9, title: 'a private copy' })],
  ['POST', /\/ask$/, (m, o) => json({ id: o.partition ? 6 : B + 6, title: 'new', status: 'running', rootId: o.partition ? 6 : B + 6 })],
  ['GET', /\/stream\?(.*)$/, (m, o) => new Response(new ReadableStream({
    start(c) {
      const s = { home: o.partition || '', q: new URLSearchParams(m[1]), c, open: true };
      streams.push(s);
      c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'hello', data: { cursor: 'g.' + seq } })}\n\n`));
      o.signal?.addEventListener('abort', () => { s.open = false; try { c.close(); } catch { /* closed */ } });
    },
  }), { headers: { 'Content-Type': 'text/event-stream' } })],
];
async function fake(url, opt = {}) {
  const method = opt.method || 'GET';
  calls.push({ method, url: String(url), home: opt.partition || '', body: typeof opt.body === 'string' ? opt.body : undefined });
  for (const [meth, re, fn] of routes) {
    const m = meth === method && String(url).match(re);
    if (m) return fn(m, opt);
  }
  return json({});
}
globalThis.fetch = fake;
globalThis.window = globalThis;
globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: () => {}, partition: 'user:alice' };

const { createApp } = await import(new URL('app.js', MODEL).href);
const actions = await import(new URL('actions.js', MODEL).href);
const until = async (cond, ms = 3000) => {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + cond);
    await new Promise((r) => setTimeout(r, 5));
  }
};
const open = (home) => streams.filter((s) => s.open && s.home === home);
const sent = (home, s) => {
  const line = `id: g.${++seq}\ndata: ${JSON.stringify({ seq, ts: Date.now(), ...s })}\n\n`;
  for (const x of open(home)) x.c.enqueue(new TextEncoder().encode(line));
};

test('a person\'s page: the list merges both homes; the router opens each at its home; streams follow', async () => {
  const app = createApp({ frame: (f) => f() });
  app.start();
  await until(() => app.convs.items.length >= 3 && !app.convs.loading);
  // Mine: both homes, newest first, cut at the own home's second page
  assert.deepEqual(app.convs.items.map((r) => r.id), [B + 3, 5, B + 2]);
  assert.deepEqual(app.convs.pinned.map((r) => r.id), [7]);
  assert.ok(app.convs.next, 'more to read');
  const lists = calls.filter((c) => c.url.includes('/conversations?'));
  assert.deepEqual(lists.map((c) => c.home).sort(), ['', 'global']);
  await app.convs.more();
  assert.deepEqual(app.convs.items.map((r) => r.id), [B + 3, 5, B + 2, 4, B + 1], 'the held shared row shows once the own home reached past it');
  assert.equal(app.convs.next, '');
  assert.ok(calls.some((c) => c.url.includes('cursor=o2') && c.home === ''), 'the next page of the own home, at the own home');
  assert.ok(!calls.some((c) => c.url.includes('cursor=') && c.home === 'global'), 'nothing more asked of a home at its end');
  // the list holds shared rows: the shared space's stream follows the run list too
  await until(() => open('').length === 1 && open('global').length === 1);
  assert.equal(open('global')[0].q.get('run'), null);

  // a shared conversation, by address (#c=5): read and followed at global
  await app.follow('#c=5');
  assert.ok(calls.some((c) => c.url.endsWith('/runs/5/view?limit=50') || (c.url.includes('/runs/5/view') && c.home === 'global')));
  assert.ok(calls.filter((c) => c.url.includes('/runs/5/')).every((c) => c.home === 'global'), 'every call about it at global');
  await until(() => open('global').some((s) => s.q.get('run') === '5') && open('').some((s) => s.q.get('run') === null));
  // what global streams reaches the open view
  sent('global', { type: 'message', run: 5, root: 5, data: { id: 1, runId: 5, seq: 1, role: 'assistant', content: 'from the team' } });
  await until(() => (app.session.views.get(5)?.messages || []).length === 1);
  await app.session.send('hello team');
  assert.ok(calls.some((c) => c.url.endsWith('/runs/5/message') && c.home === 'global'));
  assert.equal(app.uploadTarget().path, '/api/apps/agent/runs/5/upload?name={name}&xbin-partition=global', 'an app\'s own upload into it asks xbind for global');

  // one of her own: read at her partition; the shared stream goes back to the list
  await app.select(B + 3);
  assert.ok(calls.some((c) => c.url.includes(`/runs/${B + 3}/view`) && c.home === ''));
  await until(() => open('').some((s) => s.q.get('run') === String(B + 3)) && open('global').some((s) => s.q.get('run') === null));

  // the Shared view: the shared space's alone
  app.convs.view('shared', false);
  await until(() => !app.convs.loading && app.convs.items.length === 1);
  assert.equal(calls.filter((c) => c.url.includes('scope=shared')).map((c) => c.home).join(), 'global');

  // search, needs: both homes
  await app.convs.search('x');
  assert.deepEqual(app.convs.results.map((r) => r.id), [B + 3, 5]);
  const needs = await actions.needs();
  assert.deepEqual(needs.map((n) => n.run.id), [B + 2, 5]);

  // a new shared chat is made at global; a private one at home
  assert.equal((await actions.ask({ text: 'for the team', share: { visibility: 'team' } })).id, 6);
  assert.equal((await actions.ask({ text: 'mine' })).id, B + 6);
  // publishing and copying are the own partition's
  await actions.publish(B + 3, { share: { visibility: 'team' } });
  assert.equal(calls.filter((c) => c.url.endsWith(`/runs/${B + 3}/publish`)).map((c) => c.home).join(), '');
  assert.equal((await actions.copyToMine(5)).id, B + 9);
  assert.ok(calls.some((c) => c.url.endsWith('/copy') && c.home === '' && JSON.parse(c.body).from === 5));
  // home again, the Mine view without shared rows: the shared stream closes
  pages.global.first = { pinned: [], items: [], next: '' };
  app.convs.view('mine', false);
  await until(() => !app.convs.loading);
  app.home();
  await until(() => open('global').length === 0);
  assert.equal(open('').length, 1);
  app.session.live.close();
});

test('a row held below the horizon: live events reach it, and each conversation is listed once', async () => {
  const { ConvList } = await import(new URL('conv-list.js', MODEL).href);
  pages.global.first = { pinned: [], items: [row(5, 8000), row(4, 3000)], next: '' };
  const list = new ConvList({ change() {}, epoch: () => 0 });
  await list.load();
  assert.deepEqual(list.items.map((r) => r.id), [B + 3, 5, B + 2]);
  assert.deepEqual(list.held.map((r) => r.id), [4]);
  list.apply({ type: 'ustate', data: { id: 4, readMs: 3001 } });
  assert.equal(list.held[0].readMs, 3001, 'its read state (another tab) reaches it');
  list.apply({ type: 'run', run: 4, root: 4, data: { id: 4, activityMs: 4000, mine: true } });
  assert.deepEqual([list.items.map((r) => r.id), list.held.map((r) => r.id)], [[B + 3, 5, B + 2], [4]], 'still below the horizon: held, not listed');
  list.apply({ type: 'run', run: 4, root: 4, data: { id: 4, activityMs: 99999, mine: true } }); // bob writes in it
  assert.deepEqual([list.items.map((r) => r.id), list.held.map((r) => r.id)], [[4, B + 3, 5, B + 2], []], 'above the horizon: listed');
  await list.more();
  const ids = list.items.map((r) => r.id);
  assert.deepEqual(ids, [4, B + 3, 5, B + 2, B + 1]);
  assert.equal(new Set(ids).size, ids.length, 'each conversation once');
});

test('with nothing shared listed, the page catches up with the shared space when looked at again', async () => {
  const { ConvList } = await import(new URL('conv-list.js', MODEL).href);
  pages.global.first = { pinned: [], items: [], next: '' };
  const list = new ConvList({ change() {}, epoch: () => 0 });
  await list.load();
  assert.equal(list.wantsGlobal(), false);
  assert.equal(await list.catchUp(100000), false, 'nothing shared yet');
  pages.global.first = { pinned: [], items: [row(9, 7000, { mine: false, visibility: 'private' })], next: '' }; // shared with her since
  assert.equal(await list.catchUp(101000), false, 'at most every 15 s');
  assert.equal(await list.catchUp(116000), true);
  assert.ok(list.items.some((r) => r.id === 9), 'listed');
  assert.equal(list.wantsGlobal(), true, 'the shared space\'s stream follows the list now');
  assert.equal(await list.catchUp(200000), false, 'while it does, its events keep the list');
});

test('the native view\'s images and exports of a shared conversation ask xbind for global', async () => {
  const nui = await import(new URL('../native/ui.js', MODEL).href);
  nui.ctx.app = { base: '/api/apps/agent' };
  assert.equal(nui.thumb(5, 'a.png'), '/api/apps/agent/runs/5/thumb?path=a.png&w=480&xbin-partition=global');
  assert.equal(nui.raw(5, 'a b.png'), '/api/apps/agent/runs/5/raw?path=a%20b.png&xbin-partition=global');
  assert.equal(nui.thumb(B + 3, 'a.png', 1024), `/api/apps/agent/runs/${B + 3}/thumb?path=a.png&w=1024`, 'her own: at home');
  const was = globalThis.xbin.partition;
  delete globalThis.xbin.partition;
  assert.equal(nui.raw(5, 'a.png'), '/api/apps/agent/runs/5/raw?path=a.png', 'unpartitioned: as ever');
  globalThis.xbin.partition = was;
});

test('an unpartitioned page (and the global instance\'s own) never asks for a partition', async () => {
  for (const p of [undefined, 'global']) {
    calls.length = 0;
    globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: () => {}, ...(p ? { partition: p } : {}) };
    const app = createApp({ frame: (f) => f() });
    app.start();
    await until(() => !app.convs.loading && calls.some((c) => c.url.includes('/conversations?')));
    await app.select(5);
    await actions.ask({ text: 'x', share: { visibility: 'team' } });
    await actions.needs();
    assert.equal(app.session.liveG, null, `${p}: one stream`);
    assert.deepEqual(calls.filter((c) => c.home), [], `${p}: no call carries a partition`);
    app.session.live.close();
  }
});

// --- coding agents (D147) in a partitioned agent: only in a person's own conversations -----------
// (model/harness-homes.js; API.md "Coding agents" → "In a partitioned instance (the UI)")

const HH = await import(new URL('harness-homes.js', MODEL).href);
const HS = await import(new URL('harness-start.js', MODEL).href);
const T = await import(new URL('terminals.js', MODEL).href);
const R = await import(new URL('rules.js', MODEL).href);
const S = await import(new URL('sandboxes.js', MODEL).href);
const { catalogOf } = await import(new URL('harness.js', MODEL).href);
const { steerWords } = await import(new URL('harness-ask.js', MODEL).href);
const { createHarnessStore } = await import(new URL('harness-store.js', MODEL).href);
const { harnessSeed, SBX, API_DEV } = await import(new URL('../test/harness-fixtures.mjs', MODEL).href);

const as = (partition) => { globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: () => {}, ...(partition ? { partition } : {}) }; };
// alice's own sandbox (homed in her partition) and the team's (the agent's global identity: shared, as her partition sees it)
const OWN = { ref: `${SBX}|sb-own`, provider: SBX, manager: 'Coding sandboxes', id: 'sb-own', name: 'my-dev', state: 'running', egress: 'internet',
  visibility: 'private', image: { id: 'base' }, owner: { user: 'alice', via: 'apps/agent', partitionId: 'p-alice', partition: 'user:alice' }, mine: true, canUse: true };
const TEAM = { ref: API_DEV, provider: SBX, manager: 'Coding sandboxes', id: 'sb-7f3a', name: 'api-dev', state: 'running', egress: 'internet',
  visibility: 'team', shared: true, image: { id: 'base' }, owner: { user: 'admin', via: 'apps/agent' }, mine: false, canUse: true };
const MGR = { provider: SBX, title: 'Coding sandboxes', ok: true };
const cat = () => catalogOf({ harnesses: harnessSeed().harnesses });
const storeApp = () => ({ emit() {}, session: { views: new Map(), runs: new Map(), clearApproveNote() {}, noteApprove() {} } });

test('coding agents in a partitioned agent, the rules: where one starts, its sandbox, its sign-in, a shared new chat, moving', () => {
  as('user:alice');
  assert.deepEqual(['legacy', 'user', 'global'].map((s) => HH.harnessesHere(s)), [true, true, false], 'never at the global instance\'s own page');
  assert.equal(HH.homedWhy(OWN), '', 'her own sandbox');
  assert.equal(HH.homedWhy(TEAM), HH.notOwn('api-dev'), 'the team\'s: seen through a share');
  assert.match(HH.homedWhy(TEAM), /isn't a sandbox of your own space/);
  assert.ok(HH.homedWhy({ ...OWN, owner: { ...OWN.owner, partition: 'user:bob' } }), 'someone else\'s partition');
  assert.ok(HH.homedWhy({ ...OWN, owner: { ...OWN.owner, via: 'apps/other' } }), 'another tile\'s');
  assert.ok(HH.homedWhy({ ...OWN, owner: { user: 'alice' } }), 'a manager that says nothing of its home');
  assert.equal(HH.homedWhy({ ...TEAM, homed: true }), '', 'the backend\'s word on a row wins');
  assert.equal(HH.homedWhy({ ...OWN, homed: false, bindWhy: 'not here' }), 'not here');
  assert.equal(HH.homedWhy({ ...OWN, homed: false }), HH.notOwn('my-dev'));
  assert.deepEqual(['legacy', 'global'].map((s) => HH.homedWhy(TEAM, s)), ['', ''], 'one home: any sandbox');
  assert.deepEqual([HH.signInAway(5, 'user'), HH.signInAway(B + 5, 'user'), HH.signInAway(5, 'global'), HH.signInAway(5, 'legacy')],
    [HH.SIGNIN_SHARED, '', HH.SIGNIN_GLOBAL, ''], 'a sign-in only where the credentials stay the person\'s');
  assert.deepEqual([HH.sharedNewChat('team-participant', 'user'), HH.sharedNewChat('people', 'user'), HH.sharedNewChat('mine', 'user'),
    HH.sharedNewChat(undefined, 'user'), HH.sharedNewChat('team-viewer', 'legacy')], [HH.SHARED_BUILTIN, HH.SHARED_BUILTIN, '', '', '']);
  assert.deepEqual([{ engine: 'harness', parentId: 0 }, { engine: 'harness', parentId: 25 }, { engine: '' }, null].map(HH.keepsHome), [true, false, false, false],
    'a coding agent\'s conversation (a child is its root\'s: the built-in agent\'s)');
});

test('a person\'s partition: a coding agent\'s calls and the app\'s run terminal go to its home', async () => {
  as('user:alice');
  calls.length = 0;
  const hs = createHarnessStore(storeApp());
  await hs.steer(5, 'hi');
  await hs.steer(5, 'now', { interrupt: true });
  await hs.permit(5, { option: 'allow', park: 'p1' });
  await hs.answer(5, 'decline', null, 'p1');
  await hs.authenticate(5, 'key', { apiKey: 'sk-1' });
  await hs.log(5);
  await hs.stop(5);
  await hs.cancel(5);
  await hs.retry(5);
  await hs.setMode(5, 'plan');
  await hs.setOptionOf(5, 'model', 'haiku');
  await hs.get(5);
  const shared = calls.filter((c) => c.url.includes('/runs/5/'));
  assert.equal(shared.length, 12);
  assert.deepEqual([...new Set(shared.map((c) => c.home))], ['global'], 'a shared conversation\'s run: every call at global');
  calls.length = 0;
  await hs.steer(B + 3, 'mine');
  await hs.permit(B + 3, { approve: true, park: 'p2' });
  await hs.load();
  assert.deepEqual([...new Set(calls.map((c) => c.home))], [''], 'her own run, the catalog and her settings: her partition');
  assert.ok(calls.some((c) => c.url.endsWith('/harnesses')) && calls.some((c) => c.url.endsWith('/prefs/harness-mode')));
  assert.equal(T.runTerminalSrc(5), 'runs/5/harness/terminal?xbin-partition=global');
  assert.equal(T.runTerminalSrc(5, { login: true }), 'runs/5/harness/terminal?login=1&xbin-partition=global');
  assert.equal(T.runTerminalSrc(B + 3), `runs/${B + 3}/harness/terminal`);
  assert.equal(T.runTerminalSrc(B + 3, { login: true }), `runs/${B + 3}/harness/terminal?login=1`);
});

test('a person\'s partition: the sandbox a coding agent starts in is her own; its sign-in only in her own conversations; no copy, no move', () => {
  as('user:alice');
  const claude = cat().harnesses.find((h) => h.id === 'claude');
  const list = S.listOf({ sandboxes: [TEAM, OWN], managers: [MGR] });
  const opts = HS.sandboxOptions(claude, list);
  assert.deepEqual(opts.map((o) => [o.name, o.disabled]), [['my-dev', false], ['api-dev', true]], 'the team\'s is disabled…');
  assert.equal(opts[1].why, HH.notOwn('api-dev'), '…saying why');
  assert.equal(HS.fitsWhy(claude)(TEAM), HH.notOwn('api-dev'), 'the composer\'s ▣ picker says the same');
  assert.equal(HS.preferredSandbox(claude, list, API_DEV, API_DEV).value, OWN.ref, 'a remembered team sandbox is passed over');
  const p = HS.agentPicker(cat(), 'claude', { list, remembered: { claude: API_DEV } });
  assert.ok(p.shown && p.harness && p.rows.length === 5, 'her partition: coding agents answer');
  assert.ok(!p.rows.find((r) => r.value === 'claude').detail.includes('api-dev'), 'no "signed in on" a sandbox she can\'t start it in');
  const onlyTeam = S.listOf({ sandboxes: [TEAM], managers: [MGR] });
  const card = HS.setupOf(claude, onlyTeam, API_DEV, null);
  assert.equal(card.kind, 'create', 'none of her own: the setup card offers Create');
  assert.equal(card.title, 'Claude Code needs a coding sandbox of your own');
  assert.match(card.text, /The team's sandboxes, and ones shared with you, are for shared chats/);
  assert.equal(card.create.label, 'Create claude-dev');
  assert.doesNotMatch(HS.setupOf(claude, S.listOf({ sandboxes: [], managers: [MGR] }), '', null).text, /team's/, 'no team sandbox: not said');
  as('');
  assert.equal(HS.setupOf(claude, onlyTeam, API_DEV, null), null, 'unpartitioned: the team\'s fits, as ever');
  as('user:alice');

  // sign-in: #24 (below 2^40) is a shared conversation's run, at the global instance
  const seed = harnessSeed();
  const lst = S.listOf({ sandboxes: seed.sandboxes, managers: [MGR] });
  const away = T.signIn(seed.views[24], { list: lst, me: 'admin' });
  assert.deepEqual([away.talk, away.away, away.view], [false, HH.SIGNIN_SHARED, HH.SIGNIN_SHARED], 'a read-only card, saying why');
  assert.match(away.title, /is waiting for a sign-in/);
  const own = structuredClone(seed.views[24]);
  Object.assign(own.run, { id: B + 24, rootId: B + 24 });
  const c = T.signIn(own, { list: lst, me: 'admin' });
  assert.deepEqual([c.talk, c.away, c.view, c.methods.length], [true, '', '', 3], 'her own: the sign-in, as ever');
  assert.equal(steerWords(seed.views[24]).placeholder, 'Codex is waiting for a sign-in — your message waits with it…', 'the composer doesn\'t ask for one');
  assert.equal(steerWords(own).placeholder, 'sign in to Codex first — then message it…');

  // a coding agent's conversation stays in her own space
  const hrow = { id: B + 21, title: 'fix it', access: 'owner', mine: true, engine: 'harness', harness: { provider: 'claude' }, parentId: 0 };
  const labels = (r) => R.rowMenu(r, { publish: true }).map((i) => i.label);
  assert.ok(!labels(hrow).includes('Share a copy…') && !labels(hrow).includes('Share…'), labels(hrow).join());
  assert.ok(labels({ ...hrow, engine: '' }).includes('Share a copy…'), 'the built-in agent\'s: a copy, as ever');
  const hv = { run: { ...hrow, rootId: B + 21, status: 'idle', harness: seed.views[21].run.harness }, access: 'owner', config: {}, files: [], memory: {}, links: [] };
  const t = R.topBar(hv, null, { user: 'alice' });
  assert.deepEqual([t.sharing, t.publish, t.shareRun], [false, false, { id: B + 21, title: 'fix it', engine: 'harness' }], 'no Share a copy in its top bar');
  assert.match(HS.topChip(hv).title, /stays in your own space/);
  const kid = { ...hv, run: { ...hv.run, id: B + 26, rootId: B + 25, parentId: B + 25 } };
  assert.deepEqual([R.topBar(kid, null, { user: 'alice' }).publish, R.topBar(kid, null, { user: 'alice' }).shareRun.engine], [true, undefined],
    'a coding agent the agent started: its root (the agent\'s) is shared by a copy');
  assert.doesNotMatch(HS.topChip({ ...hv, run: { ...hv.run, id: 21, rootId: 21 } }).title, /own space/, 'a shared one isn\'t in her own space');
});

test('the global instance\'s own page: no coding agent answers or signs in; an unpartitioned page as ever', async () => {
  as('global');
  const p = HS.agentPicker(cat(), 'claude', {});
  assert.deepEqual([p.shown, p.value, p.harness, p.rows.length], [false, HS.AGENT, null, 1], 'only the built-in agent, and nothing shown');
  const hs = createHarnessStore(storeApp());
  hs.catalog = cat();
  hs.pick = 'claude';
  assert.deepEqual([hs.picked(), hs.askPart()], [null, {}], 'a remembered pick doesn\'t start one');
  const app = { harness: hs, classId: 'coding', model: 'm1' };
  assert.deepEqual(HS.newChatPick(app, 'claude', API_DEV), { harness: undefined, model: 'm1' }, 'the new-chat dialog\'s: the built-in agent');
  const seed = harnessSeed();
  const c = T.signIn(seed.views[24], { me: 'admin' });
  assert.deepEqual([c.talk, c.away], [false, HH.SIGNIN_GLOBAL], 'no sign-in at the global instance');
  assert.equal(T.runTerminalSrc(24), 'runs/24/harness/terminal', 'one home: the path as ever');
  calls.length = 0;
  await hs.steer(24, 'x');
  assert.deepEqual(calls.map((x) => x.home), [''], 'its own runs at its own backend');
  as('');
  assert.equal(hs.picked().id, 'claude', 'unpartitioned: the pick answers');
  assert.equal(HS.agentPicker(cat(), 'claude', {}).shown, true);
  assert.equal(T.signIn(seed.views[24], { me: 'admin' }).talk, true);
  const hrow = { id: 21, access: 'owner', engine: 'harness', parentId: 0 };
  assert.ok(R.rowMenu(hrow, { publish: true }).some((i) => i.label === 'Share…'), 'unpartitioned: a coding agent\'s conversation is shared as on master');
});
