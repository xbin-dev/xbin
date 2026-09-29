// hack/xbin-client-partition.test.mjs — web/xbin-client.js on partitioned
// tiles (docs/partitions.md), run by `make js-test`: xbin.partition exists
// only in a document the server marked with <meta name="xbin-partition">,
// and xbin.fetch's `partition: 'global'` option adds xbin-partition=global
// only in a user partition's document and only to the tile's own API — it is
// stripped everywhere, so it never reaches the network from any other
// document, and misuse is refused the same way in every document.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// xbin-client.js is a classic script with no exports: load a fresh instance
// into a stub document (hack/xbin-client-ws.test.mjs's way) and record what
// it hands to fetch.
const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
let n = 0;
async function load(metas, href = 'https://ws.example/c/apps/agent/') {
  const calls = [];
  globalThis.WebSocket = class { };
  globalThis.location = new URL(href);
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
const alice = { ...base, 'xbin-partition': 'user:alice' };

// What fetch finally sends for a recorded call: the Request fetch builds from
// (input, init), so init's overrides (headers) count as they would.
const sent = (c) => new Request(c.url instanceof Request ? c.url : new URL(String(c.url), location.href), c.init);

// Firefox has no Request.prototype.body (bug 1387483): hide it while fn runs,
// so a body taken through that getter is lost here as it is there.
async function withoutRequestBody(fn) {
  const desc = Object.getOwnPropertyDescriptor(Request.prototype, 'body');
  Object.defineProperty(Request.prototype, 'body', { get() { return undefined; }, configurable: true });
  try { return await fn(); } finally { Object.defineProperty(Request.prototype, 'body', desc); }
}

test('xbin.partition exists only in a partitioned tile\'s document', async () => {
  const a = await load(alice);
  assert.equal(a.xbin.partition, 'user:alice');
  assert.ok(Object.isFrozen(a.xbin));
  const root = await load({ ...base, 'xbin-partition': 'global' });
  assert.equal(root.xbin.partition, 'global');
  const plain = await load(base);
  assert.equal('partition' in plain.xbin, false, 'no meta: no key at all');
});

test('fetch {partition: "global"} reaches global from a user partition', async () => {
  const { xbin, calls } = await load(alice);
  await xbin.fetch('/api/apps/agent/runs/42', { partition: 'global' });
  await xbin.fetch('/api/apps/agent/runs?since=4', { partition: 'global', method: 'POST', headers: { 'X-A': '1' } });
  await xbin.fetch('/api/apps/agent/doc#top', { partition: 'global' });
  await xbin.fetch(new URL('https://ws.example/api/apps/agent/runs'), { partition: 'global' });
  await xbin.fetch('/api/apps/agent', { partition: 'global' });
  await xbin.fetch('/api/apps/agent/runs'); // no option: the viewer's own partition
  assert.deepEqual(calls.map((c) => String(c.url)), [
    '/api/apps/agent/runs/42?xbin-partition=global',
    '/api/apps/agent/runs?since=4&xbin-partition=global',
    '/api/apps/agent/doc?xbin-partition=global#top',
    'https://ws.example/api/apps/agent/runs?xbin-partition=global',
    '/api/apps/agent?xbin-partition=global',
    '/api/apps/agent/runs',
  ]);
  for (const c of calls) {
    assert.equal('partition' in c.init, false, 'the option never reaches fetch');
    assert.equal(c.init.headers.get('X-XBin-Frame-Token'), 'T');
  }
  assert.equal(calls[1].init.method, 'POST');
  assert.equal(calls[1].init.headers.get('X-A'), '1');

  const del = new Request('https://ws.example/api/apps/agent/runs', { method: 'DELETE' });
  await xbin.fetch(del, { partition: 'global' });
  const last = calls.at(-1);
  assert.equal(last.url.url, 'https://ws.example/api/apps/agent/runs?xbin-partition=global');
  assert.equal(last.url.method, 'DELETE');

  await assert.rejects(xbin.fetch('/api/apps/agent/runs', { partition: 'user:bob' }), TypeError);
  assert.equal(calls.length, 7, 'a refused option sends nothing');
});

test('a Request with a body keeps it, and its headers, on its way to global', async () => {
  const { xbin, calls } = await load(alice);
  const req = new Request('https://ws.example/api/apps/agent/runs?x=1', {
    method: 'POST', body: '{"n":1}', headers: { 'Content-Type': 'application/json', 'X-A': '1' },
  });
  await withoutRequestBody(() => xbin.fetch(req, { partition: 'global' }));
  assert.equal(calls.length, 1);
  const out = sent(calls[0]);
  assert.equal(out.url, 'https://ws.example/api/apps/agent/runs?x=1&xbin-partition=global');
  assert.equal(out.method, 'POST');
  assert.equal(await out.text(), '{"n":1}', 'the body arrives (not read through Request.prototype.body)');
  assert.equal(out.headers.get('Content-Type'), 'application/json');
  assert.equal(out.headers.get('X-A'), '1');
  assert.equal(out.headers.get('X-XBin-Frame-Token'), 'T');
  assert.equal(req.bodyUsed, false, 'the caller\'s Request stays unused');

  // opts.headers replaces a Request's own headers, as with fetch itself.
  await xbin.fetch(new Request('https://ws.example/api/apps/agent/runs', { method: 'PUT', body: 'b', headers: { 'X-A': '1' } }),
    { partition: 'global', headers: { 'X-B': '2' } });
  const put = sent(calls[1]);
  assert.equal(await put.text(), 'b');
  assert.equal(put.headers.get('X-A'), null);
  assert.equal(put.headers.get('X-B'), '2');
  assert.equal(put.headers.get('X-XBin-Frame-Token'), 'T');
});

test('a Request\'s own headers reach the network without the option too', async () => {
  for (const metas of [base, alice]) {
    const { xbin, calls } = await load(metas);
    await xbin.fetch(new Request('https://ws.example/api/apps/other/x', { headers: { 'X-A': '1' } }));
    const out = sent(calls[0]);
    assert.equal(out.headers.get('X-A'), '1', JSON.stringify(metas));
    assert.equal(out.headers.get('X-XBin-Frame-Token'), 'T');
    assert.equal(out.url, 'https://ws.example/api/apps/other/x');
  }
});

test('the option reaches only the tile\'s own API, in every document', async () => {
  const others = [
    '/api/apps/other/runs',                          // another tile
    '/api/apps/agentx/runs',                         // a sibling whose name starts alike
    '/api/apps/agent+dev/runs',                      // another deployment
    '/api/apps/agent/../other/runs',                 // normalizes to another tile
    '/api/xbin/tile-report',                         // xbind's own API
    '/c/apps/agent/index.html',                      // a document path
    'runs',                                          // relative to /c/apps/agent/
    'https://elsewhere.example/api/apps/agent/runs', // another origin
    new Request('https://elsewhere.example/api/apps/agent/runs', { method: 'POST', body: 'x' }),
  ];
  for (const metas of [alice, base, { ...base, 'xbin-partition': 'global' }]) {
    const { xbin, calls } = await load(metas);
    for (const u of others) {
      await assert.rejects(xbin.fetch(u, { partition: 'global' }), (e) => e instanceof TypeError && /own \/api\/apps\/agent\//.test(e.message),
        `${JSON.stringify(metas)} ${String(u.url ?? u)}`);
    }
    await assert.rejects(xbin.fetch('/api/apps/agent/runs', { partition: 'anything' }), TypeError, 'an unknown value is refused everywhere');
    assert.equal(calls.length, 0, 'a refused call sends nothing');
  }
});

test('on a tile origin, the workspace origin xbind names counts as the tile\'s own', async () => {
  const { xbin, calls } = await load({ ...alice, 'xbin-workspace-origin': 'https://ws.example' }, 'https://t-abc.ws.example/c/apps/agent/');
  await xbin.fetch('/api/apps/agent/runs', { partition: 'global' });
  await xbin.fetch('https://ws.example/api/apps/agent/runs', { partition: 'global' });
  await assert.rejects(xbin.fetch('https://t-def.ws.example/api/apps/agent/runs', { partition: 'global' }), TypeError);
  assert.deepEqual(calls.map((c) => c.url), [
    '/api/apps/agent/runs?xbin-partition=global',
    'https://ws.example/api/apps/agent/runs?xbin-partition=global',
  ]);
});

test('any falsy partition is the viewer\'s own, in every document', async () => {
  for (const metas of [alice, base]) {
    const { xbin, calls } = await load(metas);
    for (const want of [false, 0, '', null, undefined]) await xbin.fetch('/api/apps/agent/runs', { partition: want });
    await xbin.fetch('/api/apps/other/runs', { partition: false }); // any URL: nothing is asked of it
    assert.deepEqual(calls.map((c) => c.url), [...Array(5).fill('/api/apps/agent/runs'), '/api/apps/other/runs'], JSON.stringify(metas));
    for (const c of calls) assert.equal('partition' in c.init, false);
  }
});

test('a valid option is stripped and inert outside a user partition', async () => {
  for (const metas of [base, { ...base, 'xbin-partition': 'global' }]) {
    const { xbin, calls } = await load(metas);
    await xbin.fetch('/api/apps/agent/runs', { partition: 'global', method: 'PUT' });
    const req = new Request('https://ws.example/api/apps/agent/runs', { method: 'POST', body: 'x' });
    await xbin.fetch(req, { partition: 'global' });
    assert.deepEqual(calls.map((c) => String(c.url.url ?? c.url)), ['/api/apps/agent/runs', 'https://ws.example/api/apps/agent/runs'], JSON.stringify(metas));
    assert.equal(calls[1].url, req, 'the Request goes out as given');
    assert.equal('partition' in calls[0].init, false);
    assert.equal(calls[0].init.method, 'PUT');
  }
});

test('xbin.status() works in a user partition', async () => {
  const { xbin, calls } = await load(alice);
  await xbin.status('warn', 'disk almost full');
  await xbin.clearStatus();
  assert.deepEqual(calls.map((c) => c.url), ['/api/xbin/tile-report', '/api/xbin/tile-report'], 'xbind\'s own route, never a partition parameter');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers.get('X-XBin-Frame-Token'), 'T');
  assert.deepEqual(JSON.parse(calls[0].init.body), { level: 'warn', message: 'disk almost full', transient: false });
});
