// hack/agent-template-model.test.mjs — the agent template's shared model
// (builtin-templates/agent/model/) runs with no DOM and no lit: the web view
// (agent.js) and a native view both drive it. This loads it in node against a
// scripted backend (the kit's api() over a fake fetch, the live stream over a
// fake SSE body) and walks what a view does: start, open a conversation,
// stream, send, stop, go home, follow addresses, join, ask with attachments.
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';
import { readFileSync, readdirSync } from 'node:fs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const MODEL = new URL('model/', TPL);
// The model imports the frontend kit by its served URL; here it is the file.
const KIT = new URL('../web/bx-kit.js', import.meta.url).href;
registerHooks({ resolve: (spec, ctx, next) => (spec === '/vendor/bx-kit.js' ? { url: KIT, shortCircuit: true } : next(spec, ctx)) });

// --- a scripted backend ---------------------------------------------------------

const calls = [];
const streams = new Set();
let seq = 100;
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const push = (ev) => {
  ev.seq = ev.seq || ++seq;
  ev.ts = ev.ts || Date.now();
  const line = `id: g.${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`;
  for (const c of streams) { try { c.enqueue(new TextEncoder().encode(line)); } catch { streams.delete(c); } }
};
const view = (id, extra = {}) => ({ cursor: 'g.' + seq, run: { id, title: 'run ' + id, status: 'idle', rootId: id, pendingState: {} },
  messages: [], steps: [], links: [], queued: [], drafts: [], chain: [], files: [], memory: {}, config: {}, messageFiles: {}, ...extra });
const state = { uploadFails: false };
const routes = [
  ['GET', /\/api\/xbin\/prefs\/toolset$/, () => json('web')],
  ['GET', /\/me$/, () => json({ kind: 'user', user: 'alice', manager: true, epochMs: 0 })],
  ['GET', /\/halt$/, () => json({ on: false })],
  ['GET', /\/needs$/, () => json({ items: [{ reason: 'question', run: { id: 2, title: 'q' } }] })],
  ['GET', /\/automations\?summary=1$/, () => json({ count: 1, unread: 2, attention: 1, failing: 0 })],
  ['GET', /\/automations$/, () => json({ items: [{ kind: 'schedule', id: 3, name: 'digest', access: 'owner', enabled: true, config: { cron: '0 9 * * *', goal: 'g' } }] })],
  ['GET', /\/automations\/schedule\/3\/runs/, () => json({ items: [{ id: 20, title: 'a digest' }], next: '' })],
  ['POST', /\/automations\/schedule\/3\/read$/, () => json({ ok: 'true' })],
  ['GET', /\/triggers\/unmatched$/, () => json({ items: [] })],
  ['GET', /\/conversations\?/, () => json({ pinned: [], next: '', items: [
    { id: 1, title: 'plan', status: 'idle', access: 'owner', mine: true, activityMs: 2000, readMs: 1000 },
    { id: 2, title: 'q', status: 'waiting_input', access: 'owner', mine: true, activityMs: 1000, readMs: 1000 }] })],
  ['GET', /\/runs\/1\/view$/, () => json(view(1, { access: 'owner', queued: [{ id: 7, text: 'queued one' }] }))],
  ['GET', /\/runs\/2\/view$/, () => json(view(2, { run: { id: 2, title: 'q', status: 'waiting_input', rootId: 2, pendingState: { kind: 'question' }, result: 'which?' } }))],
  ['GET', /\/runs\/(\d+)\/view$/, (m) => json(view(+m[1]))],
  ['POST', /\/runs\/(\d+)\/read$/, () => json({ readMs: Date.now() })],
  ['POST', /\/runs\/(\d+)\/message$/, () => json({ ok: 'true', inboxId: 1, queued: false })],
  ['POST', /\/runs\/(\d+)\/interrupt$/, () => json({ ok: 'true', returned: [{ text: 'first' }, { text: '' }, { text: 'second' }] })],
  ['PUT', /\/runs\/(\d+)\/upload\?name=(.*)$/, (m) => (state.uploadFails ? json({ error: 'disk full' }, 507) : json({ path: decodeURIComponent(m[2]) }))],
  ['DELETE', /\/runs\/(\d+)$/, () => json({ ok: 'true' })],
  ['POST', /\/ask$/, () => json({ id: 9, title: 'new one', status: 'running', rootId: 9 })],
  ['POST', /\/join$/, () => json({ runId: 2 })],
  ['GET', /\/stream\b/, (m, o) => new Response(new ReadableStream({
    start(c) {
      streams.add(c);
      c.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ type: 'hello', data: { cursor: 'g.' + seq } })}\n\n`));
      o.signal?.addEventListener('abort', () => { streams.delete(c); try { c.close(); } catch { /* closed */ } });
    },
  }), { headers: { 'Content-Type': 'text/event-stream' } })],
];
async function fake(url, opt = {}) {
  const method = opt.method || 'GET';
  calls.push({ method, url: String(url), body: typeof opt.body === 'string' ? opt.body : undefined });
  for (const [meth, re, fn] of routes) {
    const m = meth === method && String(url).match(re);
    if (m) return fn(m, opt);
  }
  return json({});
}
// What a tile frame gives the model: the xbin client (no document, no window
// beyond the global the kit reads), and fetch.
globalThis.fetch = fake;
globalThis.window = globalThis;
const notes = [];
globalThis.xbin = { self: 'apps/agent', fetch: fake, notify: (kind, text) => notes.push(text) };

const { createApp } = await import(new URL('app.js', MODEL).href);
const rules = await import(new URL('rules.js', MODEL).href);
const { HOME } = await import(new URL('home.js', MODEL).href);

const until = async (cond, ms = 3000) => {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting for ' + cond);
    await new Promise((r) => setTimeout(r, 5));
  }
};
const called = (method, tail) => calls.filter((c) => c.method === method && c.url.split('?')[0].endsWith(tail));

// --- the model, no DOM ----------------------------------------------------------------

test('model modules import no lit and touch no DOM', () => {
  for (const f of readdirSync(MODEL).filter((n) => n.endsWith('.js'))) {
    const src = readFileSync(new URL(f, MODEL), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`\\])\/\/.*$/gm, '$1'); // code, not comments
    assert.doesNotMatch(src, /lit-all|from '\.\.\//, `${f}: the model imports only itself and the kit`);
    assert.doesNotMatch(src, /\b(document|window|history|location)\.|\b(alert|confirm|prompt)\(|localStorage|sessionStorage|innerHTML|querySelector|customElements/,
      `${f}: no DOM in the model`);
  }
});

test('the old module paths re-export the model', async () => {
  const pairs = [['chat-fold.js', 'fold.js'], ['tool-heads.js', 'tool-heads.js'], ['conv-groups.js', 'conv-groups.js'],
    ['stream.js', 'stream.js'], ['conv-list.js', 'conv-list.js']];
  for (const [old, now] of pairs) {
    const a = await import(new URL(old, TPL).href);
    const b = await import(new URL(now, MODEL).href);
    assert.deepEqual(Object.keys(a).sort(), Object.keys(b).sort(), `${old} re-exports all of model/${now}`);
    for (const k of Object.keys(b)) assert.equal(a[k], b[k], `${old}: ${k}`);
  }
});

test('the app: start, a conversation, the stream, send, stop, home, addresses', async () => {
  const routed = [];
  const seen = [];
  const app = createApp({ route: (h) => routed.push(h) });
  app.on('*', () => {});
  for (const t of ['me', 'list', 'needs', 'halt', 'toolset', 'autos', 'select', 'selected', 'home', 'page', 'sending', 'change', 'error']) {
    app.on(t, (x) => seen.push(x === undefined ? t : `${t}:${x instanceof Error ? x.message : x}`));
  }
  assert.equal(app.base, '/api/apps/agent');

  app.start();
  await until(() => app.convs.items.length === 2 && seen.includes('me') && seen.includes('needs') && seen.includes('toolset'));
  assert.equal(app.toolset, 'web', 'the tool mode comes from the per-person prefs');
  assert.equal(app.me.user, 'alice');
  assert.equal(app.needs.length, 1);
  assert.equal(app.autos.summary.unread, 2);
  assert.ok(calls[0].url.endsWith('/api/xbin/prefs/toolset'), 'the prefs are read first, as the web always did');
  await until(() => streams.size === 1);

  // a conversation
  await app.select(1);
  assert.equal(app.sel, 1);
  assert.deepEqual(routed.slice(-1), ['c=1']);
  assert.ok(seen.includes('select:1') && seen.includes('selected:1'));
  assert.equal(called('POST', '/runs/1/read').length, 1, 'an unread conversation you open is read');
  const v = app.session.current();
  assert.equal(app.root, 1);
  assert.deepEqual(app.session.queued().map((q) => q.text), ['queued one']);
  assert.equal(rules.composer(v, HOME).placeholder, 'follow up…');
  assert.equal(rules.topBar(v).share, 'Share');

  // the stream: a draft streams into the blocks, a status change reaches the list
  await until(() => streams.size === 1);
  push({ type: 'thinking', run: 1, root: 1, data: { text: 'hmm' } });
  push({ type: 'text', run: 1, root: 1, data: { text: 'Hello' } });
  push({ type: 'run', run: 1, root: 1, data: { status: 'running' } });
  await until(() => app.session.shown().blocks.some((b) => b.k === 'draft' && b.text === 'Hello'));
  const shown = app.session.shown();
  assert.deepEqual(shown.blocks.map((b) => b.k), ['think', 'draft']);
  assert.equal(shown.activity, 'Writing…');
  assert.equal(app.convs.find(1).status, 'running');
  await until(() => seen.includes('change'));
  const busy = app.session.current();
  assert.equal(rules.composer(busy, HOME).placeholder, 'steer — delivered at the agent\'s next step…');
  assert.equal(rules.composer(busy, HOME).stop, true);

  // send (queued while it works), stop gives the queue back
  await app.send('  steer this  ', () => seen.push('cleared'));
  const msg = called('POST', '/runs/1/message').pop();
  assert.equal(JSON.parse(msg.body).text, 'steer this');
  assert.ok(seen.includes('cleared'));
  assert.equal(seen.filter((s) => s === 'sending').length, 2, 'sending starts and settles');
  assert.equal(await app.stop(), 'first\n\nsecond');

  // home
  app.home();
  assert.equal(app.sel, null);
  assert.equal(routed.at(-1), '');
  assert.ok(seen.includes('home'));

  // addresses: the Automations page, one automation; a join link
  await app.follow('#auto=schedule:3');
  assert.equal(app.page, 'automations');
  assert.deepEqual(app.autos.open, { kind: 'schedule', id: 3 });
  assert.equal(app.autos.runs.length, 1);
  assert.equal(routed.at(-1), 'auto=schedule:3');
  assert.ok(seen.includes('page'));
  await app.follow('#join=TOKEN_1');
  await until(() => app.session.current()?.run.id === 2);
  assert.equal(JSON.parse(called('POST', '/join').pop().body).token, 'TOKEN_1');
  assert.equal(rules.composer(app.session.current(), HOME).placeholder, 'answer the question…');
  await app.follow('#c=1');
  assert.equal(app.sel, 1);

  // a revoked conversation you look at sends you home, and says so
  push({ type: 'revoked', run: 1, root: 1, data: { id: 1 } });
  await until(() => app.sel === null);
  assert.deepEqual(notes, ['That conversation is no longer shared with you.']);
  assert.equal(app.convs.find(1), undefined);

  // a new ask with attachments: created held, uploaded into, then sent
  app.attach.add([new File(['hi'], 'a.txt', { type: 'text/plain' })]);
  const cleared = [];
  await app.send('see attached', () => cleared.push(1));
  const ask = JSON.parse(called('POST', '/ask').pop().body);
  assert.deepEqual(ask, { text: 'see attached', toolset: 'web', hold: true });
  assert.equal(called('PUT', '/runs/9/upload').length, 1);
  assert.deepEqual(JSON.parse(called('POST', '/runs/9/message').pop().body), { text: 'see attached', files: ['a.txt'] });
  assert.equal(app.attach.items.length, 0);
  assert.equal(cleared.length, 1);
  assert.equal(app.sel, 9);

  // …and when the upload fails: the empty run goes, the chips stay, the person hears why
  app.home();
  state.uploadFails = true;
  app.attach.add([new File(['x'], 'b.txt', { type: 'text/plain' })]);
  await app.send('again', () => cleared.push(2));
  assert.equal(called('DELETE', '/runs/9').length, 1, 'the held run is deleted');
  assert.equal(app.attach.items.length, 1);
  assert.equal(app.attach.items[0].state, 'bad');
  assert.ok(seen.includes('error:b.txt: disk full'));
  assert.equal(cleared.length, 1, 'the text stays in the composer');
  state.uploadFails = false;

  app.session.live.close();
});

test('rules: who may do what', () => {
  const v = (access, run = {}) => ({ access, run: { id: 4, title: 't', status: 'idle', ...run }, config: {}, memory: { a: 1 }, files: [], links: [] });
  const viewer = rules.topBar(v('viewer', { status: 'error' }));
  assert.equal(viewer.viewOnly, true);
  assert.equal(viewer.retry, false);
  assert.equal(viewer.share, 'Shared');
  assert.equal(viewer.del, false);
  const owner = rules.topBar(v('owner', { status: 'error', origin: 'schedule', originId: 3, parentId: 1, rootId: 1 }));
  assert.deepEqual(owner.crumb, { kind: 'schedule', id: 3 });
  assert.equal(owner.retry, true);
  assert.equal(owner.tree, true);
  assert.deepEqual(owner.shareRun, { id: 1, title: 't' });
  assert.equal(owner.memory, 1);
  assert.equal(rules.composer(v('viewer'), HOME).disabled, true);
  assert.equal(rules.composer(null, HOME).placeholder, HOME.placeholder);

  assert.deepEqual(rules.rowMenu({ access: 'owner' }).map((i) => i.label), ['Rename', 'Pin', 'Share…', 'Archive', 'Delete']);
  assert.deepEqual(rules.rowMenu({ access: 'viewer', mine: true, pinnedAt: 1, archivedAt: 1 }).map((i) => i.label), ['Unpin', 'Unarchive', 'Leave']);
  assert.deepEqual(rules.rowMenu({ access: 'participant', mine: false }).map((i) => i.label), ['Pin', 'Archive']);
  assert.equal(rules.rowGlyph({ status: 'waiting_input' }), 'ask');
  assert.equal(rules.rowGlyph({ status: 'sleeping' }), 'spin');
  assert.equal(rules.rowShared({ visibility: 'team', owner: 'bob' }).title, 'shared by bob');

  assert.equal(rules.halt({ manager: false }, true, []).shown, false);
  assert.equal(rules.halt({ manager: true }, false, [{ status: 'waiting_input' }]).shown, false, 'waiting for a person is not running');
  assert.equal(rules.halt({ manager: true }, false, [{ status: 'running' }]).shown, true);
  assert.equal(rules.halt({ manager: true }, true, []).label, '⏻ HALTED');

  const d = { owner: 'bob', visibility: 'team', teamRole: 'viewer', members: [{ user: 'alice', role: 'viewer' }] };
  assert.deepEqual(rules.share(d, { user: 'alice' }), { own: false, vis: 'team-viewer', leave: true });
  assert.equal(rules.share({ ...d, owner: '' }, { user: 'carol', manager: true }).own, true);
});

test('router: addresses', async () => {
  const router = await import(new URL('router.js', MODEL).href);
  assert.deepEqual(router.parse('#c=12'), { conv: 12, join: '', auto: null });
  assert.deepEqual(router.parse('#auto=trigger:4'), { conv: null, join: '', auto: { kind: 'trigger', id: '4' } });
  assert.deepEqual(router.parse('#auto').auto, { kind: undefined, id: undefined });
  assert.equal(router.parse('#join=abc').join, '#join=abc');
  assert.equal(router.autoHash('channel', 7), 'auto=channel:7');
  assert.equal(router.autoHash(), 'auto');
  assert.equal(router.convHash(3), 'c=3');
});
