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
