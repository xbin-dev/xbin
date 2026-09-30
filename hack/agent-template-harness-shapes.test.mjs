// hack/agent-template-harness-shapes.test.mjs — the STUB-drift guard (D147
// §4): every key path (and its JSON type) the UI's fake backend serves from
// its coding-agent fixtures — test/harness-fixtures.mjs's seeds, and what
// test/backend.mjs's STUB answers and pushes over them — must be one the
// real backend produces, as builtin-templates/agent/_backend/testdata/
// harness_shapes.json records it (TestHarnessShapes makes the real backend
// produce each shape and fails when it stops producing a path the file
// lists; see that test for how to regenerate the file). So the STUB can't
// drift from the backend the UI was built against. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url);
const { STUB } = await import(new URL('test/backend.mjs', TPL));
const { harnessSeed, kidsSeed, API_DEV } = await import(new URL('test/harness-fixtures.mjs', TPL));
const REAL = JSON.parse(readFileSync(new URL('_backend/testdata/harness_shapes.json', TPL), 'utf8')).shapes;

// The walk TestHarnessShapes does: a path per key ("a.b", arrays "a[]"),
// the adapter's own JSON (rawInput, schema) not entered, a map keyed by
// sandbox ref ("sandboxes") as "*"; a summary or a park inside a shape is a
// shape of its own (summary; summaryNode — a /tree node's or a /needs
// item's compact one; pending.<kind>), a pendingState of no kind only its type.
const OPAQUE = new Set(['rawInput', 'schema']);
const MAPS = new Set(['sandboxes']);
const typeOf = (x) => (x === null ? 'null' : Array.isArray(x) ? 'array' : typeof x === 'object' ? 'object' : typeof x);
function fold(shape, path, key, x) {
  if (x === null || typeof x !== 'object' || Array.isArray(x)) return undefined;
  if (key === 'harness' && x.provider != null) return shape === 'treeNode' || (shape === 'needsItem' && path === 'harness') ? 'summaryNode' : 'summary';
  if (key === 'data' && shape === 'harnessEvent') return 'summary';
  if (key === 'pendingState') return x.kind ? `pending.${x.kind}` : '';
  return undefined;
}
function walk(book, shape, path, key, x, keep) {
  if (x === undefined) return;
  if (path) {
    if (keep && !keep(path)) return;
    ((book[shape] ||= {})[path] ||= new Set()).add(typeOf(x));
    const to = fold(shape, path, key, x);
    if (to !== undefined) { if (to) walk(book, to, '', '', x); return; }
  }
  if (OPAQUE.has(key) || x === null) return;
  if (Array.isArray(x)) { for (const c of x) walk(book, shape, path + '[]', key, c, keep); return; }
  if (typeof x !== 'object') return;
  for (const [k, c] of Object.entries(x)) {
    const kk = MAPS.has(key) ? '*' : k;
    walk(book, shape, path ? `${path}.${kk}` : kk, kk, c, keep);
  }
}

// collect: add(name, value, keep?) records a value's shape (keep: which of
// its paths)
function collect() {
  const shapes = {};
  const add = (name, v, keep) => walk(shapes, name, '', '', JSON.parse(JSON.stringify(v)), keep);
  return { shapes, add };
}

// only the parts of a row or a view's run this program added (§4.3): the
// rest are the built-in agent's, whose fixtures predate this guard
const harnessPart = (p) => /^(engine|harness|pendingState|waiting|kids)(\.|\[|$)/.test(p);

async function served(seed) {
  const { shapes, add } = collect();
  globalThis.window = globalThis;
  STUB(seed);
  const pushed = [];
  const push = globalThis.__push;
  globalThis.__push = (ev) => { pushed.push(JSON.parse(JSON.stringify(ev))); push(ev); };
  const req = async (method, path, body) => {
    const r = await globalThis.xbin.fetch(`/api/apps/agent${path}`, body === undefined ? { method } : { method, body: JSON.stringify(body) });
    return { status: r.status, data: JSON.parse(await r.text()) };
  };
  // the fixtures as the STUB serves them: views, lists, the tree
  for (const id of Object.keys(seed.views)) {
    const v = (await req('GET', `/runs/${id}/view`)).data;
    const harness = v.run.engine === 'harness';
    add('viewRun', v.run, harnessPart);
    if (harness) add('viewConfig', v.config, (p) => /^(engine|harness|sandbox)(\.|$)/.test(p));
    for (const m of v.messages || []) add(`message.${m.role}`, m);
    for (const l of v.links || []) add('link', l);
  }
  for (const it of (await req('GET', '/conversations?limit=30')).data.items) add('convRow', it, harnessPart);
  for (const it of (await req('GET', '/needs')).data.items) {
    add('needsItem', it, (p) => p === 'reason' || p === 'subRun' || /^harness(\.|$)/.test(p) || /^run\.(id|title|status|engine|harness)(\.|$)/.test(p));
  }
  for (const [root, tree] of Object.entries(seed.trees || {})) for (const n of (await req('GET', `/runs/${root}/tree`)).data.nodes) add('treeNode', n);
  for (const h of (await req('GET', '/harnesses')).data.harnesses) add('catalog', h);
  for (const r of seed.runs.filter((x) => x.engine === 'harness')) {
    const g = (await req('GET', `/runs/${r.id}/harness`)).data;
    add('harnessGet', g);
  }
  return { shapes, add, req, pushed };
}

// the STUB's answers to the routes that change a coding agent, and its events
async function changes() {
  const { shapes, add, req, pushed } = await served(harnessSeed());
  add('patch', (await req('PATCH', '/runs/21/harness', { mode: 'default', option: { id: 'model', value: 'sonnet' } })).data);
  add('ok', (await req('POST', '/runs/23/harness/answer', { park: 'Xq3ask', action: 'accept', content: { library: 'jsoniter' } })).data);
  add('authenticate', (await req('POST', '/runs/24/harness/authenticate', { method: 'device-code' })).data);
  add('authenticate', (await req('POST', '/runs/24/harness/authenticate', { method: 'device-code', confirm: true })).data);
  add('authenticate', (await req('POST', '/runs/24/harness/authenticate', { method: 'openai-api-key', apiKey: 'k', confirm: true })).data);
  const ask = (await req('POST', '/ask', { text: 'fix it', harness: { provider: 'claude' }, sandbox: { ref: API_DEV, cwd: '/work/api' } })).data;
  add('askAnswer', ask, harnessPart);
  await req('POST', '/runs/27/cancel', {});
  for (const ev of pushed) {
    if (ev.type === 'harness') add('harnessEvent', ev);
    if (ev.type === 'run' && ev.data.engine === 'harness') add('runEvent', ev, (p) => !/^data\./.test(p) || harnessPart(p.slice(5)) || p === 'data.id');
    if (ev.type === 'link') add('linkEvent', ev);
  }
  return shapes;
}

// drift: every path (and type) a fixture shape uses that the real one lacks
function drift(shapes) {
  const out = [];
  for (const [name, paths] of Object.entries(shapes)) {
    const real = REAL[name];
    if (!real) { out.push(`${name}: no such shape in the dump`); continue; }
    for (const [p, ts] of Object.entries(paths)) {
      if (!real[p]) { out.push(`${name}: ${p}`); continue; }
      for (const t of ts) if (t !== 'null' && !real[p].includes(t)) out.push(`${name}: ${p} is ${t}, the backend's ${real[p].join('|')}`);
    }
  }
  return out.sort();
}

test('the harness fixtures serve only shapes the real backend produces', async () => {
  const merged = {};
  for (const shapes of [(await served(harnessSeed())).shapes, (await served(kidsSeed())).shapes, await changes()]) {
    for (const [name, paths] of Object.entries(shapes)) for (const [p, ts] of Object.entries(paths)) for (const t of ts) ((merged[name] ||= {})[p] ||= new Set()).add(t);
  }
  for (const name of ['summary', 'summaryNode', 'pending.approval', 'pending.question', 'pending.login', 'message.tool', 'message.assistant', 'treeNode', 'link',
    'convRow', 'needsItem', 'catalog', 'harnessGet', 'harnessEvent', 'runEvent', 'authenticate', 'patch', 'askAnswer']) {
    assert.ok(merged[name], `the fixtures serve no ${name}`);
  }
  assert.deepEqual(drift(merged), [], 'fixture paths the backend never produces (fix the side that is wrong vs D147 §4; a new backend path: regenerate the dump)');
});

test('the guard catches a drift', () => {
  const { shapes, add } = collect();
  add('summary', { provider: 'claude', stat: 'ready' });
  add('treeNode', { id: 1, harness: { provider: 'claude', options: [] } });
  add('link', { child: { id: 'x', pendingState: { kind: 'approval', harness: { opts: [] } } } });
  assert.deepEqual(drift(shapes), ['link: child.id is string, the backend\'s number', 'pending.approval: harness.opts', 'summary: stat', 'summaryNode: options']);
});
