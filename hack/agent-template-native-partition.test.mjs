// hack/agent-template-native-partition.test.mjs — the agent template's native
// view in a partitioned instance (model/partition.js; API.md "Partitioned
// instances"), beside agent-template-native.test.mjs (at its size cap); run
// like it, through hack/xbn/node.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';
import { PHONE } from './agent-template-native-caps.mjs'; // the phone stack (rev-1 split): the tests walk its nav

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 21, 12);
const ME = { kind: 'user', user: 'admin', level: 'terminal', manager: true, halted: false, epochMs: 0 };

async function run(seed, steps = [], { state = null, data = {} } = {}) {
  const r = await runNative({ caps: PHONE, entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed, ...data }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}

// find/all: nodes of a tree by type and props (a subset, compared as JSON),
// optionally inside an ancestor that matches `in`.
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
const called = (r, method, re) => r.calls.filter((c) => c.method === method && re.test(c.url));
const topScreen = (tree) => { const nav = find(tree, { t: 'nav' }); return nav.c[nav.c.length - 1]; };

test('MCP servers: a partitioned instance lists the static ones and marks one with headers as shared-only; unpartitioned, as ever', async () => {
  const mcp = [{ name: 'gh', url: 'https://mcp.example/gh', headers: { Authorization: 'Bearer secret-token' } }, { name: 'docs', url: 'https://mcp.example/docs' }];
  const steps = [
    { tap: { t: 'button', p: { label: 'Settings' } } },
    { tap: { t: 'row', p: { title: 'MCP servers' } } },
    { snapshot: 'mcp' },
  ];
  const seed = (partition) => ({ me: ME, runs: [], partition, routes: [['GET', '/config$', { models: {}, mcp }]] });
  const p = await run(seed('user:alice'), steps);
  const scr = topScreen(p.snapshots.mcp);
  assert.equal(scr.p.title, 'MCP servers');
  const sec = find(scr, { t: 'section', p: { title: 'Static servers (config)' } });
  assert.ok(sec, 'the static servers are listed');
  assert.deepEqual(all(sec, { t: 'row' }).map((x) => [x.p.title, x.p.detail ?? null]), [['gh', 'shared only'], ['docs', null]]);
  assert.match(sec.p.footer, /^gh: works in shared \(global\) conversations only — to use it in your own conversations, bind it as a tile or a personal bind/);
  assert.ok(!JSON.stringify(p.snapshots.mcp).includes('secret-token'), 'never the header itself');
  const u = await run(seed(undefined), steps);
  assert.equal(find(topScreen(u.snapshots.mcp), { t: 'section', p: { title: 'Static servers (config)' } }), null, 'unpartitioned: the bound ones only');
  assert.equal(called(u, 'GET', /\/config$/).length, 0, 'unpartitioned: the screen reads no config');
});

// a coding agent's conversation in the shared space (model/harness-homes.js unshareWhy): shared with no
// one it would move to the person's own space, and a coding agent's never moves — the sheet says why
test('share sheet: a coding agent\'s conversation in the shared space isn\'t made private; unpartitioned, as ever', async () => {
  const { harnessSeed } = await import(TPL + 'test/harness-fixtures.mjs');
  const { STAYS_SHARED } = await import(TPL + 'model/harness-homes.js');
  const acl = { owner: 'admin', visibility: 'team', teamRole: 'participant', members: [], links: [] };
  const seed = (partition) => ({ ...harnessSeed(), partition, routes: [['GET', '/runs/21/members$', acl]] });
  const steps = [
    { wait: 50 },
    { tap: { t: 'button', p: { label: 'Share' }, in: { t: 'menu' } } },
    { wait: 20 },
    { snapshot: 'sheet' },
    { event: [{ t: 'picker', p: { style: 'inline' } }, 'change', { value: 'private' }] },
    { wait: 20 },
    { snapshot: 'picked' },
  ];
  const p = await run(seed('user:admin'), steps, { state: { hash: 'c=21' } });
  const sec = find(p.snapshots.sheet, { t: 'section', p: { title: 'Who can see it' } });
  assert.equal(sec.p.footer, STAYS_SHARED, 'the sheet says why');
  assert.equal(find(sec, { t: 'picker' }).p.options[0].label, 'Only you and the people below — stays shared');
  assert.equal(find(p.snapshots.picked, { t: 'notice', p: { tone: 'danger' } }).p.text, STAYS_SHARED, 'picking it says why…');
  assert.equal(called(p, 'PATCH', /\/runs\/21$/).length, 0, '…and sends nothing');
  const u = await run(seed(undefined), steps, { state: { hash: 'c=21' } });
  assert.equal(find(u.snapshots.sheet, { t: 'section', p: { title: 'Who can see it' } }).p.footer, undefined, 'unpartitioned: no word of it');
  assert.equal(called(u, 'PATCH', /\/runs\/21$/).length, 1, 'unpartitioned: made private, as ever');
});
