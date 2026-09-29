// hack/xbin-client-partition.test.mjs — web/xbin-client.js on partitioned
// tiles (docs/partitions.md), run by `make js-test`: xbin.partition exists
// only in a document the server marked with <meta name="xbin-partition">,
// and xbin.fetch's `partition: 'global'` option adds xbin-partition=global
// only in a user partition's document — it is stripped everywhere, so it
// never reaches the network from any other document.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// xbin-client.js is a classic script with no exports: load a fresh instance
// into a stub document (hack/xbin-client-ws.test.mjs's way) and record what
// it hands to fetch.
const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
let n = 0;
async function load(metas) {
  const calls = [];
  globalThis.WebSocket = class { };
  globalThis.location = new URL('https://ws.example/c/apps/agent/');
  globalThis.window = globalThis;
  globalThis.parent = globalThis; // top level: not embedded
  globalThis.document = {
    querySelector: (sel) => { const name = /meta\[name="([^"]+)"\]/.exec(sel)?.[1]; return name in metas ? { content: metas[name] } : null; },
    addEventListener() {},
  };
  globalThis.fetch = async (url, init) => { calls.push({ url, init }); return new Response('{}'); };
  delete globalThis.xbin;
  const realInterval = globalThis.setInterval;
  globalThis.setInterval = () => 0; // the token refresher would hold the test open
  try {
    await import(`data:text/javascript,${encodeURIComponent(`${src}\n// instance ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  return { xbin: globalThis.xbin, calls };
}

const base = { 'xbin-component': 'apps/agent', 'xbin-frame-token': 'T' };

test('xbin.partition exists only in a partitioned tile\'s document', async () => {
  const alice = await load({ ...base, 'xbin-partition': 'user:alice' });
  assert.equal(alice.xbin.partition, 'user:alice');
  assert.ok(Object.isFrozen(alice.xbin));
  const root = await load({ ...base, 'xbin-partition': 'global' });
  assert.equal(root.xbin.partition, 'global');
  const plain = await load(base);
  assert.equal('partition' in plain.xbin, false, 'no meta: no key at all');
});

test('fetch {partition: "global"} reaches global from a user partition', async () => {
  const { xbin, calls } = await load({ ...base, 'xbin-partition': 'user:alice' });
  await xbin.fetch('/api/apps/agent/runs/42', { partition: 'global' });
  await xbin.fetch('/api/apps/agent/runs?since=4', { partition: 'global', method: 'POST', headers: { 'X-A': '1' } });
  await xbin.fetch('/api/apps/agent/doc#top', { partition: 'global' });
  await xbin.fetch(new URL('https://ws.example/api/apps/agent/runs'), { partition: 'global' });
  await xbin.fetch('/api/apps/agent/runs'); // no option: the viewer's own partition
  assert.deepEqual(calls.map((c) => String(c.url)), [
    '/api/apps/agent/runs/42?xbin-partition=global',
    '/api/apps/agent/runs?since=4&xbin-partition=global',
    '/api/apps/agent/doc?xbin-partition=global#top',
    'https://ws.example/api/apps/agent/runs?xbin-partition=global',
    '/api/apps/agent/runs',
  ]);
  for (const c of calls) {
    assert.equal('partition' in c.init, false, 'the option never reaches fetch');
    assert.equal(c.init.headers.get('X-XBin-Frame-Token'), 'T');
  }
  assert.equal(calls[1].init.method, 'POST');
  assert.equal(calls[1].init.headers.get('X-A'), '1');

  const req = new Request('https://ws.example/api/apps/agent/runs', { method: 'DELETE' });
  await xbin.fetch(req, { partition: 'global' });
  const last = calls.at(-1);
  assert.equal(last.url.url, 'https://ws.example/api/apps/agent/runs?xbin-partition=global');
  assert.equal(last.url.method, 'DELETE');

  await assert.rejects(xbin.fetch('/api/apps/agent/runs', { partition: 'user:bob' }), TypeError);
  assert.equal(calls.length, 6, 'a refused option sends nothing');
});

test('the option is stripped and inert outside a user partition', async () => {
  for (const metas of [base, { ...base, 'xbin-partition': 'global' }]) {
    const { xbin, calls } = await load(metas);
    await xbin.fetch('/api/apps/agent/runs', { partition: 'global', method: 'PUT' });
    await xbin.fetch('/api/apps/agent/runs', { partition: 'anything' });
    assert.deepEqual(calls.map((c) => c.url), ['/api/apps/agent/runs', '/api/apps/agent/runs'], JSON.stringify(metas));
    assert.equal('partition' in calls[0].init, false);
    assert.equal(calls[0].init.method, 'PUT');
  }
});
