// hack/agent-template-partition.test.mjs — the agent template's three
// layouts (builtin-templates/agent/model/partition.js): which one
// xbin.partition picks, what each shows, and the live stream closing while a
// partitioned instance's page is hidden (model/stream.js).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { partitionState, sharing, pausesHidden, notices, appNotices, oldManagers, staticMcp, mcpNote, MCP_GLOBAL_ONLY } from '../builtin-templates/agent/model/partition.js';
import { rowMenu, topBar } from '../builtin-templates/agent/model/rules.js';
import { Live } from '../builtin-templates/agent/model/stream.js';

const withPartition = (p, fn) => {
  const before = globalThis.xbin;
  globalThis.xbin = p === undefined ? {} : { partition: p };
  try { return fn(); } finally { globalThis.xbin = before; }
};

test('xbin.partition picks the layout; anything else is today\'s', () => {
  assert.equal(partitionState(undefined), 'legacy');
  assert.equal(partitionState(''), 'legacy');
  assert.equal(partitionState('global'), 'global');
  assert.equal(partitionState('user:alice'), 'user');
  assert.equal(partitionState('user:'), 'legacy');
  assert.equal(partitionState('org:acme'), 'legacy');
  withPartition(undefined, () => assert.equal(partitionState(), 'legacy'));
  withPartition('user:bob', () => assert.equal(partitionState(), 'user'));
});

test('sharing is off only in a person\'s own partition; hidden pages pause only when partitioned', () => {
  assert.deepEqual(['legacy', 'global', 'user'].map((s) => sharing(s)), [true, true, false]);
  assert.deepEqual(['legacy', 'global', 'user'].map((s) => pausesHidden(s)), [false, true, true]);
  const row = { id: 1, access: 'owner', title: 't' };
  withPartition(undefined, () => assert.ok(rowMenu(row).some((i) => i.action === 'share')));
  withPartition('user:alice', () => assert.ok(!rowMenu(row).some((i) => i.action === 'share')));
  const v = { run: { id: 1, title: 't', status: 'idle' }, access: 'owner', config: {} };
  withPartition(undefined, () => assert.equal(topBar(v, null, {}).sharing, true));
  withPartition('user:alice', () => assert.equal(topBar(v, null, {}).sharing, false));
});

test('notices: the global note; one banner for old sandbox managers in a person\'s partition', () => {
  const list = {
    loaded: true, managers: [
      { provider: 'apps/cs', ok: true },
      { provider: 'apps/old', ok: false, refusal: 'partitions', error: 'sandbox manager apps/old: too old' },
      { provider: 'apps/down', ok: false, refusal: 'unreachable' },
    ],
  };
  assert.deepEqual(notices('legacy', list), []);
  assert.deepEqual(oldManagers(list.managers).map((m) => m.provider), ['apps/old']);
  const g = notices('global', list);
  assert.equal(g.length, 1);
  assert.match(g[0].text, /sign in as a person/);
  const u = notices('user', list);
  assert.equal(u.length, 1);
  assert.equal(u[0].kind, 'sandbox');
  assert.match(u[0].text, /apps\/old/);
  assert.match(u[0].text, /bx template updates/);
  assert.doesNotMatch(u[0].text, /apps\/down/);
  assert.deepEqual(notices('user', { managers: [{ provider: 'apps/cs', ok: true }] }), []);
  const two = notices('user', { managers: [list.managers[1], { ...list.managers[1], provider: 'apps/old2' }] });
  assert.equal(two.length, 1, 'one banner, however many managers');
  assert.match(two[0].text, /apps\/old, apps\/old2 aren’t available/);
});

test('appNotices reads the sandbox list once in a person\'s partition with managers bound', () => {
  let ensured = 0;
  const app = { sbx: { list: { loaded: false, managers: [] }, ensure: () => { ensured++; } } };
  const before = globalThis.xbin;
  try {
    globalThis.xbin = { partition: 'user:alice', iface: () => ({ endpoints: [{ provider: 'apps/cs' }] }) };
    appNotices(app);
    assert.equal(ensured, 1);
    app.sbx.list.loaded = true;
    appNotices(app);
    assert.equal(ensured, 1);
    globalThis.xbin = { iface: () => ({ endpoints: [{ provider: 'apps/cs' }] }) };
    app.sbx.list.loaded = false;
    appNotices(app);
    assert.equal(ensured, 1, 'an unpartitioned page reads nothing for it');
  } finally { globalThis.xbin = before; }
});

test('static MCP servers: a partitioned instance marks the ones with headers as shared-only; unpartitioned lists none', () => {
  const cfg = { mcp: [
    { name: 'gh', url: 'https://mcp.example/gh', headers: { Authorization: 'Bearer x' } },
    { name: 'docs', url: 'https://mcp.example/docs' },
    { name: 'empty', url: 'https://mcp.example/e', headers: {} },
  ] };
  assert.deepEqual(staticMcp(cfg, 'legacy'), [], 'unpartitioned: today\'s page, nothing listed');
  for (const st of ['user', 'global']) {
    const list = staticMcp(cfg, st);
    assert.deepEqual(list.map((s) => [s.name, s.globalOnly]), [['gh', true], ['docs', false], ['empty', false]]);
    assert.ok(!JSON.stringify(list).includes('Bearer'), 'never the header itself');
    assert.equal(mcpNote(list), `gh: ${MCP_GLOBAL_ONLY}.`);
  }
  assert.match(MCP_GLOBAL_ONLY, /shared \(global\) conversations only.*bind it as a tile or a personal bind/);
  assert.equal(mcpNote(staticMcp({ mcp: [{ name: 'docs', url: 'u' }] }, 'user')), '');
  assert.deepEqual(staticMcp(null, 'user'), []);
  withPartition('user:alice', () => assert.equal(staticMcp(cfg).length, 3));
  withPartition(undefined, () => assert.equal(staticMcp(cfg).length, 0));
});

// a stream stand-in: xbin.fetch answers a body that stays open until aborted
function fakeStreams() {
  const calls = [];
  const fetch = (url, init) => {
    const call = { url, signal: init.signal };
    calls.push(call);
    const body = new ReadableStream({
      start(c) { init.signal.addEventListener('abort', () => c.error(new Error('aborted'))); },
    });
    return Promise.resolve(new Response(body, { status: 200 }));
  };
  return { calls, fetch };
}

function fakeDocument() {
  const d = new EventTarget();
  d.hidden = false;
  d.set = (hidden) => { d.hidden = hidden; d.dispatchEvent(new Event('visibilitychange')); };
  return d;
}

const tick = () => new Promise((r) => setTimeout(r, 20));

test('a partitioned page\'s stream closes while hidden and resumes from its cursor', async () => {
  const before = { xbin: globalThis.xbin, document: globalThis.document };
  const s = fakeStreams();
  globalThis.xbin = { fetch: s.fetch, partition: 'user:alice' };
  globalThis.document = fakeDocument();
  try {
    const states = [];
    const live = new Live('/api/apps/agent', { state: (x) => states.push(x) });
    assert.equal(live.pauseHidden, true);
    live.follow(7, 'g1.5');
    await tick();
    assert.equal(s.calls.length, 1);
    assert.match(s.calls[0].url, /run=7/);
    assert.match(s.calls[0].url, /since=g1\.5/);
    document.set(true);
    await tick();
    assert.ok(s.calls[0].signal.aborted, 'hidden: the stream closed');
    assert.equal(s.calls.length, 1, 'no reconnect while hidden');
    live.follow(8, 'g1.9'); // opening another conversation while hidden waits too
    await tick();
    assert.equal(s.calls.length, 1);
    document.set(false);
    await tick();
    assert.equal(s.calls.length, 2, 'shown: it reconnects');
    assert.match(s.calls[1].url, /run=8/);
    assert.match(s.calls[1].url, /since=g1\.9/);
    live.close();
    document.set(true);
    document.set(false);
    await tick();
    assert.equal(s.calls.length, 2, 'a closed stream stays closed');
    assert.ok(!states.includes('reconnecting'), 'pausing is not reconnecting');
  } finally {
    globalThis.xbin = before.xbin;
    globalThis.document = before.document;
  }
});

test('an unpartitioned page\'s stream ignores visibility, as ever', async () => {
  const before = { xbin: globalThis.xbin, document: globalThis.document };
  const s = fakeStreams();
  globalThis.xbin = { fetch: s.fetch };
  globalThis.document = fakeDocument();
  try {
    const live = new Live('/api/apps/agent', {});
    assert.equal(live.pauseHidden, false);
    document.set(true);
    live.follow(null, '');
    await tick();
    assert.equal(s.calls.length, 1, 'it connects while hidden');
    document.set(true);
    await tick();
    assert.ok(!s.calls[0].signal.aborted, 'and stays connected');
    live.close();
  } finally {
    globalThis.xbin = before.xbin;
    globalThis.document = before.document;
  }
});
