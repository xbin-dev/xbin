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
const state = { uploadFails: false, draftGone: false };
const routes = [
  // no class picked yet: the lane picked before classes (D116) says which
  ['GET', /\/api\/xbin\/prefs\/toolset$/, () => json('web')],
  ['GET', /\/api\/xbin\/prefs\/class$/, () => json({}, 404)],
  ['GET', /\/classes$/, () => json({ default: 'internal', classes: [
    { id: 'internal', name: 'Internal', icon: '🔒', toolsets: ['internal'], lane: 'private' },
    { id: 'web', name: 'Web', icon: '🌐', toolsets: ['web'], lane: 'web', egress: true },
    { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web'], lane: 'web', egress: true }] })],
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
  // run 5 in pages of two (the native view's paged reads, API.md "Paging the view")
  ['GET', /\/runs\/5\/view\?limit=2(?:&before=(\d+))?$/, (m) => {
    const all = [1, 2, 3, 4, 5, 6].map((i) => ({ id: i, seq: i, role: i % 2 ? 'user' : 'assistant', content: 'm' + i, created: 100 + i }));
    const before = m[1] ? +m[1] : 99;
    const older = all.filter((x) => x.seq < before);
    const page = older.slice(-2);
    const hasOlder = older.length > 2;
    return json(view(5, { messages: page, hasOlder, ...(hasOlder ? { nextBefore: page[0].seq } : {}), compacted: 0, linkCount: 0,
      steps: before === 99 ? [{ id: 1, kind: 'note', detail: '{"text":"n"}', created: 106 }] : [] }));
  }],
  ['GET', /\/runs\/1\/view$/, () => json(view(1, { access: 'owner', queued: [{ id: 7, text: 'queued one' }] }))],
  ['GET', /\/runs\/2\/view$/, () => json(view(2, { run: { id: 2, title: 'q', status: 'waiting_input', rootId: 2, pendingState: { kind: 'question' }, result: 'which?' } }))],
  ['GET', /\/runs\/(\d+)\/view$/, (m) => json(view(+m[1]))],
  ['POST', /\/runs\/(\d+)\/read$/, () => json({ readMs: Date.now() })],
  ['POST', /\/runs\/(\d+)\/message$/, () => json({ ok: 'true', inboxId: 1, queued: false })],
  ['POST', /\/runs\/(\d+)\/interrupt$/, () => json({ ok: 'true', returned: [{ text: 'first' }, { text: '' }, { text: 'second' }] })],
  ['PUT', /\/runs\/(\d+)\/upload\?name=(.*)$/, (m) => (state.uploadFails ? json({ error: 'disk full' }, 507) : json({ path: decodeURIComponent(m[2]) }))],
  ['DELETE', /\/runs\/(\d+)$/, () => json({ ok: 'true' })],
  ['POST', /\/ask$/, () => (state.draftGone ? json({ error: 'those attachments are gone (the draft was sent or expired) — remove them and attach them again' }, 409)
    : json({ id: 9, title: 'new one', status: 'running', rootId: 9 }))],
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
  const heard = new Set();
  const off = app.on('*', (type) => heard.add(type));
  for (const t of ['me', 'list', 'needs', 'halt', 'toolset', 'autos', 'select', 'selected', 'home', 'page', 'sending', 'change', 'error']) {
    app.on(t, (x) => seen.push(x === undefined ? t : `${t}:${x instanceof Error ? x.message : x}`));
  }
  assert.equal(app.base, '/api/apps/agent');
  assert.equal(typeof off, 'function');

  app.start();
  await until(() => app.convs.items.length === 2 && seen.includes('me') && seen.includes('needs') && seen.includes('toolset'));
  assert.ok(['me', 'needs', 'toolset', 'list'].every((t) => heard.has(t)), `'*' hears every event with its type (${[...heard]})`);
  assert.equal(app.classId, 'web', 'with no class picked, the lane picked before classes names the class');
  assert.equal(app.toolset, 'web', '…and the lane follows it');
  assert.equal(app.me.user, 'alice');
  assert.equal(app.needs.length, 1);
  assert.equal(app.autos.summary.unread, 2);
  assert.deepEqual(calls.slice(0, 2).map((c) => c.url).sort(), ['/api/apps/agent/classes', '/api/xbin/prefs/class'],
    'the classes and your pick are read first, as the lane always was');
  assert.ok(calls.some((c) => c.url.endsWith('/api/xbin/prefs/toolset')), 'no pick yet: the old lane is read');
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
  assert.equal(rules.topBar(v).share.label, 'private');

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
  assert.deepEqual(ask, { text: 'see attached', class: 'web', toolset: 'web', hold: true });
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

test('the native view\'s options: drafts as deltas, the open conversation in pages', async () => {
  const app = createApp({ deltas: true, page: 2 });
  const before = streams.size;
  app.start();
  await until(() => streams.size === before + 1);
  assert.ok(calls.filter((c) => c.url.includes('/stream?')).pop().url.includes('deltas=1'), 'the stream asks for deltas');
  await app.select(5);
  assert.ok(called('GET', '/runs/5/view').pop().url.endsWith('/runs/5/view?limit=2'), 'the open conversation is read from its newest page');
  let s = app.session.shown();
  assert.equal(s.hasOlder, true);
  assert.equal(s.olderHidden, false, 'a page counts compacted messages instead of holding them');
  assert.deepEqual(s.blocks.map((b) => b.k + ':' + (b.text || b.kind)), ['user:m5', 'assistant:m6', 'step:note'],
    'a page with older ones holds no opening message: its first user message is shown');
  await app.session.loadOlder();
  assert.ok(called('GET', '/runs/5/view').pop().url.endsWith('before=5'));
  await app.session.loadOlder();
  s = app.session.shown();
  assert.equal(s.hasOlder, false);
  assert.deepEqual(s.blocks.filter((b) => b.k !== 'step').map((b) => b.text), ['m1', 'm2', 'm3', 'm4', 'm5', 'm6'], 'the pages merge into the whole transcript');
  await app.session.loadOlder(); // nothing older: no request
  assert.equal(called('GET', '/runs/5/view').length, 3);

  // the open conversation folds through a per-block cache: nothing changed, nothing new
  const held = app.session.shown().blocks;
  assert.equal(app.session.shown().blocks, held, 'the same blocks, the same list');
  assert.ok(app.session.folds.get(5).stats.reused > 0);

  // deltas append; one that does not fit what is held reconnects the stream
  await until(() => streams.size === before + 1);
  push({ type: 'thinking', run: 5, root: 5, ts: 1000, data: { text: 'hm' } });
  push({ type: 'thinking.delta', run: 5, root: 5, ts: 1001, data: { delta: 'm…', at: 2 } });
  push({ type: 'text', run: 5, root: 5, ts: 1002, data: { text: 'He' } });
  push({ type: 'text.delta', run: 5, root: 5, ts: 1003, data: { delta: 'llo', at: 2 } });
  await until(() => app.session.shown().blocks.some((b) => b.k === 'draft' && b.text === 'Hello'));
  const think = app.session.shown().blocks.find((b) => b.k === 'think');
  assert.ok(held.every((b) => app.session.shown().blocks.includes(b)), 'a streamed draft leaves every other block the same object');
  assert.equal(think.text, 'hmm…');
  assert.equal(think.live, false, 'text after thinking ends it');
  const streamsAsked = calls.filter((c) => c.url.includes('/stream?')).length;
  push({ type: 'text.delta', run: 5, root: 5, data: { delta: '!', at: 99 } });
  await until(() => calls.filter((c) => c.url.includes('/stream?')).length === streamsAsked + 1);
  const again = calls.filter((c) => c.url.includes('/stream?')).pop().url;
  assert.match(again, /run=5&since=g\.\d+&deltas=1/, 'it reconnects from its cursor');
  assert.equal(app.session.shown().blocks.find((b) => b.k === 'draft').text, 'Hello', 'the misfit is not appended');

  // a tool call's arguments stream as tool.delta, per call index
  await until(() => streams.size === before + 1);
  push({ type: 'tool', run: 5, root: 5, ts: 1004, data: { index: 0, id: 'c1', name: 'file_write', args: '{"path":' } });
  push({ type: 'tool.delta', run: 5, root: 5, ts: 1005, data: { index: 0, delta: '"a.txt"', at: 8 } });
  push({ type: 'tool', run: 5, root: 5, ts: 1006, data: { index: 1, id: 'c2', name: 'shell', args: '' } });
  push({ type: 'tool.delta', run: 5, root: 5, ts: 1007, data: { index: 1, delta: '{"cmd":"ls"}', at: 0 } });
  push({ type: 'tool.delta', run: 5, root: 5, ts: 1008, data: { index: 0, delta: '}', at: 15 } });
  await until(() => app.session.shown().blocks.filter((b) => b.k === 'tool' && b.state === 'writing').map((b) => b.args).join(' ') === '{"path":"a.txt"} {"cmd":"ls"}');
  const tools = app.session.shown().blocks.filter((b) => b.k === 'tool');
  assert.deepEqual(tools.map((b) => [b.name, b.callId]), [['file_write', 'c1'], ['shell', 'c2']], 'the calls keep their id and name');
  const asked = calls.filter((c) => c.url.includes('/stream?')).length;
  push({ type: 'tool.delta', run: 5, root: 5, data: { index: 0, delta: 'x', at: 3 } }); // does not fit
  push({ type: 'tool.delta', run: 5, root: 5, data: { index: 7, delta: 'x', at: 0 } }); // a call it never saw
  await until(() => calls.filter((c) => c.url.includes('/stream?')).length >= asked + 1);
  assert.equal(app.session.shown().blocks.find((b) => b.callId === 'c1').args, '{"path":"a.txt"}', 'the misfit is not appended');
  assert.ok(!app.session.shown().blocks.some((b) => b.id === 'draft-tool-7'), 'nor is a delta for a call it never saw');
  app.session.live.close();
});

test('a native app\'s uploads: chips stay where they were picked; at home Send sends the draft', async () => {
  const app = createApp();
  const seen = [];
  app.on('*', (type, e) => seen.push(type === 'error' ? 'error:' + e.message : type));
  const home = app.uploadTarget();
  assert.equal(home.method, 'PUT');
  assert.match(home.path, /^\/api\/apps\/agent\/ask\/upload\?draft=[\w-]{8,64}&name=\{name\}$/, 'at home: into the new ask\'s draft');
  const key = app.draft;
  app.attach.uploaded({ name: 'a.png', size: 3, type: 'image/png', path: 'a.png', at: 'home' });
  await app.select(1);
  assert.deepEqual(app.uploadTarget(), { method: 'PUT', path: '/api/apps/agent/runs/1/upload?name={name}' });
  assert.deepEqual(app.attach.here(app.place), [], 'not in a conversation');
  app.attach.uploaded({ name: 'b.txt', size: 1, type: 'text/plain', path: 'b.txt', at: 1 });
  await app.send('with b');
  const msg = called('POST', '/runs/1/message').pop();
  assert.deepEqual(JSON.parse(msg.body).files, ['b.txt'], 'a conversation sends its own chips only');
  app.home();
  assert.deepEqual(app.attach.here(app.place).map((a) => a.name), ['a.png'], 'home kept its chip');

  state.draftGone = true;
  await app.send('look');
  assert.ok(seen.includes('error:those attachments are gone (the draft was sent or expired) — remove them and attach them again'));
  assert.deepEqual(app.attach.items, [], 'a draft that is gone takes its chips with it');
  assert.notEqual(app.draft, key, 'and the next ask starts a draft of its own');
  state.draftGone = false;

  app.attach.uploaded({ name: 'c.png', size: 3, type: 'image/png', path: 'c.png', at: 'home' });
  const draft = app.draft;
  await app.send('what is this?');
  const ask = called('POST', '/ask').pop();
  // no classes read in this app (it never started): no class — the backend gives the caller's default
  assert.deepEqual(JSON.parse(ask.body), { text: 'what is this?', draft, files: ['c.png'] });
  assert.equal(app.sel, 9, 'the new conversation opens');
  assert.deepEqual(app.attach.items, []);
  assert.notEqual(app.draft, draft);
  app.session.live.close();
});

// D111: a grant is its owner's to allow; the chips show what is in force
// until it expires (read at render time — nothing ticks).
test('rules: a grant asked for, and the grants in force', () => {
  const run = (extra) => ({ id: 4, rootId: 4, owner: 'alice', status: 'waiting_input',
    pendingState: { kind: 'approval', grant: 'threads', toolCalls: [] }, ...extra });
  const alice = { kind: 'user', user: 'alice' };
  assert.equal(rules.grantAsk(run({ pendingState: { kind: 'approval', toolCalls: [] } }), alice), null, 'a plain approval asks no grant');
  const mine = rules.grantAsk(run(), alice);
  assert.equal(mine.lead, 'The agent asks to read your other conversations and automations');
  assert.equal(mine.canAllow, true);
  assert.equal(rules.grantAsk(run(), { kind: 'user', user: 'bob' }).canAllow, false, 'not your threads');
  assert.equal(rules.grantAsk(run(), { kind: 'user', user: 'alice', viewedBy: 'mgr' }).canAllow, false, 'an admin viewing as alice');
  assert.equal(rules.grantAsk(run(), { kind: 'system' }).canAllow, false);
  assert.match(rules.grantAsk(run(), { kind: 'user', user: 'bob' }).note, /Only alice can allow/);

  const now = Date.UTC(2026, 8, 27, 10);
  const v = (access, grants) => ({ access, run: { id: 4, rootId: 4, owner: 'alice', grants }, config: {} });
  const g = [{ cap: 'threads', grantedBy: 'alice', expiresMs: now + 60e3 }, { cap: 'threads', grantedBy: 'alice', expiresMs: now - 1 }];
  const chips = rules.grantChips(v('owner', g), alice, now);
  assert.equal(chips.length, 1, 'expired ones are gone');
  assert.match(chips[0].label, /^🔓 reads your threads · until \d\d:\d\d$/);
  assert.deepEqual([chips[0].revoke, chips[0].run, chips[0].cap], [true, 4, 'threads']);
  assert.equal(rules.grantChips(v('participant', g), alice, now)[0].revoke, false, 'only the owner revokes');
  assert.deepEqual(rules.grantChips(v('owner', undefined), alice, now), []);
  assert.equal(rules.topBar(v('owner', g), null, alice).grants.length, rules.grantChips(v('owner', g), alice).length);
  // a capability this table doesn't know takes the backend registry's words
  const sent = rules.grantAsk(run({ pendingState: { kind: 'approval', grant: 'widgets', grantAsk: 'turn the widgets', toolCalls: [] } }), alice);
  assert.equal(sent.lead, 'The agent asks to turn the widgets');
  const sc = rules.grantChips(v('owner', [{ cap: 'widgets', ask: 'turn the widgets', chip: 'turns widgets', expiresMs: now + 60e3 }]), alice, now);
  assert.match(sc[0].label, /^🔓 turns widgets · until/);
  assert.match(sc[0].title, /let the agent turn the widgets in this conversation/);
});

test('rules: who may do what', () => {
  const v = (access, run = {}) => ({ access, run: { id: 4, title: 't', status: 'idle', ...run }, config: {}, memory: { a: 1 }, files: [], links: [] });
  const viewer = rules.topBar(v('viewer', { status: 'error' }));
  assert.equal(viewer.viewOnly, true);
  assert.equal(viewer.retry, false);
  assert.equal(viewer.share.label, 'from the team', 'someone else\'s says whose');
  // who can see it, said plainly: private, the team (read/write), people — the list row counts them
  const mine = (run, row) => rules.topBar(v('owner', run), row).share.label;
  assert.equal(mine({ visibility: 'private' }), 'private');
  assert.equal(mine({ visibility: 'team', teamRole: 'viewer' }), 'team can read');
  assert.equal(mine({ visibility: 'team', teamRole: 'participant' }, { members: 2 }), 'team can write · 2 people');
  assert.equal(mine({ visibility: 'private' }, { members: 1 }), 'shared with 1 person');
  assert.equal(rules.topBar(v('viewer', { owner: 'bob' })).share.label, 'from bob');
  // a shared row's chips
  assert.equal(rules.rowShared({ visibility: 'private', members: 0 }), null);
  assert.deepEqual(rules.rowShared({ access: 'owner', visibility: 'team', teamRole: 'viewer', members: 3 }).chips.map((c) => c.label), ['team · can read', '3 people']);
  assert.deepEqual(rules.rowShared({ access: 'viewer', owner: 'bob', visibility: 'private', members: 2 }).chips.map((c) => c.label), ['from bob', '2 people']);
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
  assert.equal(rules.rowShared({ visibility: 'team', owner: 'bob' }).title, 'shared with you by bob');

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

// The model (D111): the bound providers' models, grouped; your last pick is
// your default for new chats; in a conversation a pick switches it (PATCH)
// from its next turn — and a viewer gets no picker.
test('the model: the picker, a pick per conversation, your default for new chats', async () => {
  routes.unshift(
    ['GET', /\/models$/, () => json({ data: [{ id: 'm1', provider: 'apps/a', ref: 'apps/a|m1' }, { id: 'm2', provider: 'apps/b', ref: 'apps/b|m2' }],
      providers: [{ path: 'apps/a', ok: true }, { path: 'apps/b', ok: true }] })],
    ['GET', /\/api\/xbin\/prefs\/model$/, () => json('apps/b|m2')],
    ['PUT', /\/api\/xbin\/prefs\/model$/, () => json({})],
    ['PATCH', /\/runs\/1$/, () => json({ id: 1 })],
  );
  const app = createApp({ route: () => {} });
  app.start();
  await until(() => app.catalog && (app.catalog.data || []).length === 2 && app.model === 'apps/b|m2');
  const home = rules.modelPicker(null, app.model, app.catalog);
  assert.equal(home.value, 'apps/b|m2', 'at home: your last pick');
  assert.deepEqual(home.groups.map((g) => g.label), ['a', 'b'], 'several providers: grouped');
  assert.deepEqual(home.options.map((o) => o.value), ['', 'apps/a|m1', 'apps/b|m2'], 'the agent\'s default first');
  assert.equal(rules.modelPicker(null, 'apps/gone|x', app.catalog).options.at(-1).label, 'x (not listed now)');
  assert.equal(rules.modelPicker(null, '', { data: [] }).shown, false, 'no models, no pick: no picker');

  // a new chat carries your pick
  const asks = called('POST', '/ask').length;
  await app.send('hello model', () => {});
  const ask = JSON.parse(called('POST', '/ask')[asks].body);
  assert.equal(ask.model, 'apps/b|m2', 'the new ask asks for it');

  // in a conversation: its pick, switched with PATCH, and your default follows
  await app.select(1);
  let v = app.session.current();
  assert.equal(rules.modelPicker(v, app.model, app.catalog).value, '', 'a conversation without a pick: the default');
  await app.pickModel('apps/a|m1');
  const patch = called('PATCH', '/runs/1').at(-1);
  assert.deepEqual(JSON.parse(patch.body), { model: 'apps/a|m1' });
  v = app.session.current();
  assert.equal(rules.modelPicker(v, app.model, app.catalog).value, 'apps/a|m1', 'the picker shows the switch');
  assert.equal(rules.topBar(v).model, 'm1', 'and the top bar names it');
  assert.equal(app.model, 'apps/a|m1', 'your default for new chats follows your last pick');
  assert.equal(JSON.parse(called('PUT', '/api/xbin/prefs/model').at(-1).body), 'apps/a|m1');
  assert.equal(rules.modelPicker({ ...v, access: 'viewer' }, app.model, app.catalog).disabled, true, 'a viewer can\'t switch it');
  app.home();
});
