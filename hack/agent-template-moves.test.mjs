// hack/agent-template-moves.test.mjs — a person's page in a partitioned
// agent (builtin-templates/agent/model/moves.js, sandbox-store.js; API.md
// "Partitioned instances" → "Shared conversations"):
//   - a shared conversation that stopped being shared moved to its owner's
//     own space: the page follows it — the global instance's `run` event
//     deleting it names where it went, and opening its old id (a push link,
//     a saved place) asks the global instance's record of the move;
//   - the sandbox list is read where you are: while a shared conversation is
//     open, the global instance's (its sandboxes are global's), else your
//     own partition's — each kept apart, every call about a sandbox at that
//     home; the old-manager banner reads your own.
// Unpartitioned pages ask no home for anything. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const MODEL = new URL('../builtin-templates/agent/model/', import.meta.url);
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

const B = 2 ** 40;
const MGR = 'apps/coding-sandbox';
const calls = [];
const streams = [];
const moved = new Map([[5, B + 50]]); // global id → where it went
const gone = new Set([5]);            // global ids deleted (moved)
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const view = (id) => ({ cursor: 'g.1', run: { id, title: 'run ' + id, status: 'idle', rootId: id, owner: id === 9 ? 'bob' : 'alice', pendingState: {} },
  messages: [], steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {}, config: {}, messageFiles: {}, access: 'owner' });
const box = (id) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'alice' }, mine: true, canUse: true, canManage: true, canEdit: true, caps: ['exec'], lastActive: 1 });
const mgr = (ok = true) => ({ provider: MGR, title: 'Coding sandboxes', ok, ...(ok ? {} : { refusal: 'partitions' }), caps: ['exec'],
  egress: ['none'], images: [], sizes: [], limits: {} });
async function fake(url, opt = {}) {
  const method = opt.method || 'GET';
  const u = String(url);
  const home = opt.partition || '';
  calls.push({ method, url: u, home });
  let m;
  if ((m = /\/moves\/(\d+)$/.exec(u))) return home === 'global' && moved.has(+m[1]) ? json({ run: +m[1], state: 'moved', to: moved.get(+m[1]) }) : json({ error: 'no such move' }, 404);
  if ((m = /\/runs\/(\d+)\/view/.exec(u))) return gone.has(+m[1]) ? json({ error: 'no such run' }, 404) : json(view(+m[1]));
  if (/\/sandboxes(\?|$)/.test(u)) return json(home === 'global' ? { sandboxes: [box('team-box')], managers: [mgr()] } : { sandboxes: [box('mine')], managers: [mgr(false)] });
  if (/\/sandboxes\/.+\/start/.test(u)) return json(box('team-box'));
  if (u.includes('/me')) return json({ kind: 'user', user: 'alice', manager: false, epochMs: 0, partition: 'user:alice' });
  if (u.includes('/conversations?')) return json({ pinned: [], items: [], next: '' });
  if (u.includes('/stream')) {
    return new Response(new ReadableStream({ start(c) { streams.push({ home, c, url: u }); } }), { headers: { 'Content-Type': 'text/event-stream' } });
  }
  return json({});
}
globalThis.fetch = fake;
globalThis.window = globalThis;
globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: () => {}, partition: 'user:alice', iface: () => ({ endpoints: [{ url: 'x' }] }) };

const { createApp } = await import(new URL('app.js', MODEL).href);
const { movedTo } = await import(new URL('moves.js', MODEL).href);
const { appNotices } = await import(new URL('partition.js', MODEL).href);
const until = async (cond, ms = 3000) => {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + cond);
    await new Promise((r) => setTimeout(r, 5));
  }
};

test('where a moved conversation went: asked of the global instance, for shared ids only', async () => {
  calls.length = 0;
  assert.equal(await movedTo(5), B + 50);
  assert.deepEqual(calls.map((c) => [c.url, c.home]), [['/api/apps/agent/moves/5', 'global']]);
  assert.equal(await movedTo(6), null, 'no move on record');
  calls.length = 0;
  assert.equal(await movedTo(B + 3), null, 'one of your own: nothing to ask');
  assert.equal(calls.length, 0);
});

test('the page follows a moved conversation: its old address, and the event that deletes it', async () => {
  const app = createApp({ frame: (f) => f() });
  app.start();
  // #c=5 — a push link to the shared conversation, which moved since
  await app.follow('#c=5');
  assert.equal(app.sel, B + 50, 'opened where it went');
  assert.ok(calls.some((c) => c.url.includes(`/runs/${B + 50}/view`) && c.home === ''), 'read at your own partition');
  // an open shared conversation moves while you look at it
  gone.delete(7);
  await app.select(7);
  assert.equal(app.sel, 7);
  app.session.apply({ type: 'run', run: 7, root: 7, data: { id: 7, deleted: true, movedTo: B + 70 } });
  await until(() => app.sel === B + 70);
  // one deleted, not moved: home, as ever
  app.session.apply({ type: 'run', run: B + 70, root: B + 70, data: { id: B + 70, deleted: true } });
  await until(() => app.sel == null);
  // someone else's (bob shared it with you, and it moved to his space): home, not after it
  await app.select(9);
  assert.equal(app.sel, 9);
  app.session.apply({ type: 'run', run: 9, root: 9, data: { id: 9, deleted: true, movedTo: B + 90 } });
  await until(() => app.sel == null);
  app.session.live.close();
  app.session.liveG?.close();
});

test('a hosted conversation back at the shared instance (un-shared, or continued): its owner follows it there', async () => {
  const { followsMove } = await import(new URL('moves.js', MODEL).href);
  const T = 2 ** 39;
  const mine = { owner: 'alice' };
  // un-shared: team → the global instance (a plain id below 2^39) — followed; from there, on to her own space — followed again
  assert.equal(followsMove(T + 3, 42, mine, 'alice', 'user'), true);
  assert.equal(followsMove(42, B + 42, mine, 'alice', 'user'), true);
  // someone else's, or the owner token's page: home
  assert.equal(followsMove(T + 3, 42, { owner: 'bob' }, 'alice', 'user'), false);
  assert.equal(followsMove(T + 3, 42, mine, 'alice', 'global'), false);
  // a plain shared conversation deleted "to" another shared id isn't a move; nor is a hosted id "moved" into team again
  assert.equal(followsMove(41, 42, mine, 'alice', 'user'), false);
  assert.equal(followsMove(T + 3, T + 4, mine, 'alice', 'user'), false);
});

test('the sandbox list is read where you are, each home\'s apart; the banner reads your own', async () => {
  const app = createApp({ frame: (f) => f() });
  calls.length = 0;
  // at home: your partition's
  await app.sbx.load();
  assert.deepEqual(app.sbx.list.sandboxes.map((s) => s.name), ['mine']);
  assert.deepEqual(calls.filter((c) => c.url.includes('/sandboxes')).map((c) => c.home), ['']);
  // a shared conversation open: the global instance's
  await app.select(7);
  assert.equal(app.sbx.list.loaded, false, 'not read there yet');
  app.sbx.ensure();
  await until(() => app.sbx.list.loaded);
  assert.deepEqual(app.sbx.list.sandboxes.map((s) => s.name), ['team-box']);
  assert.equal(calls.filter((c) => c.url.includes('/sandboxes')).pop().home, 'global');
  await app.sbx.act(`${MGR}|team-box`, 'start');
  assert.equal(calls.filter((c) => c.url.includes('/start')).pop().home, 'global', 'a call about one of its sandboxes: at global');
  assert.deepEqual(app.sbx.listAt('').sandboxes.map((s) => s.name), ['mine'], 'your own list kept');
  // the banner: your partition's managers (one too old), wherever you are
  const n = appNotices(app, 'user');
  assert.equal(n.length, 1);
  assert.match(n[0].text, /apps\/coding-sandbox/);
  // back home: your own again, not read again
  const reads = calls.filter((c) => c.url.includes('/sandboxes?') || /\/sandboxes$/.test(c.url)).length;
  app.home();
  assert.deepEqual(app.sbx.list.sandboxes.map((s) => s.name), ['mine']);
  app.sbx.ensure();
  assert.equal(calls.filter((c) => c.url.includes('/sandboxes?') || /\/sandboxes$/.test(c.url)).length, reads);
  app.session.live.close();
  app.session.liveG?.close();
});

test('unpartitioned: one list, no home asked, no move looked up', async () => {
  globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: () => {} };
  calls.length = 0;
  const app = createApp({ frame: (f) => f() });
  await app.sbx.load();
  await app.select(5).catch(() => {});
  assert.equal(app.sel, null, 'a run that is gone: home, as ever');
  // the owner token's page at the global instance: a moved conversation's event sends it home
  await app.select(7);
  app.session.apply({ type: 'run', run: 7, root: 7, data: { id: 7, deleted: true, movedTo: B + 70 } });
  await until(() => app.sel == null);
  app.sbx.ensure();
  assert.deepEqual(calls.filter((c) => c.home), [], 'no call carries a partition');
  assert.ok(!calls.some((c) => c.url.includes('/moves/')), 'no move looked up');
  app.session.live.close();
});
