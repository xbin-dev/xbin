// hack/partitions-realtime.test.mjs — the in-frame half of docs/partitions.md
// §Realtime between partitions, run by `make js-test`: its JavaScript blocks,
// taken from the page itself, run against web/xbin-client.js in a person's
// partition's document (fetch and the event socket stubbed as xbind answers
// them). The Go half is the SDK's example and its test
// (sdk/example_realtime_test.go); internal/docscheck keeps the page's Go
// quotes true to it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const doc = readFileSync(new URL('../docs/partitions.md', import.meta.url), 'utf8');
const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');

// The section's ```js blocks, in order.
function blocks() {
  const start = doc.indexOf('\n## Realtime between partitions\n');
  assert.ok(start >= 0, 'docs/partitions.md has its §Realtime between partitions');
  const end = doc.indexOf('\n## ', start + 1);
  const section = doc.slice(start, end < 0 ? undefined : end);
  return [...section.matchAll(/```js\n([\s\S]*?)```/g)].map((m) => m[1]);
}

// A fresh xbin-client in alice's partition of apps/rooms (the way
// hack/xbin-client-partition.test.mjs loads it), with fetch answered by
// answer(url, init) and every WebSocket recorded.
let n = 0;
async function load(answer) {
  const calls = [];
  const sockets = [];
  globalThis.WebSocket = class { constructor(url) { this.url = url; sockets.push(this); } };
  globalThis.location = new URL('https://ws.example/c/apps/rooms/');
  globalThis.window = globalThis;
  globalThis.parent = globalThis;
  const metas = { 'xbin-component': 'apps/rooms', 'xbin-frame-token': 'T', 'xbin-partition': 'user:alice' };
  globalThis.document = {
    querySelector: (sel) => { const name = /meta\[name="([^"]+)"\]/.exec(sel)?.[1]; return name in metas ? { content: metas[name] } : null; },
    addEventListener() {},
  };
  globalThis.fetch = async (url, init = {}) => { calls.push({ url: String(url), method: init.method || 'GET', body: init.body }); return answer(String(url), init); };
  delete globalThis.xbin;
  const realInterval = globalThis.setInterval;
  globalThis.setInterval = () => 0;
  try {
    await import(`data:text/javascript,${encodeURIComponent(`${src}\n// realtime ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  return { calls, sockets };
}

// run evaluates a block as the body of an async function of params, and
// answers the names it defines.
const AsyncFunction = (async () => {}).constructor;
function run(block, params, names) {
  const f = new AsyncFunction(...Object.keys(params), `${block}\nreturn { ${names.join(', ')} };`);
  return f(...Object.values(params));
}

const json = (v) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });
const posts = [
  { room: '7', from: 'alice', text: 'the deploy is green, @bob' },
  { room: '7', from: 'bob', text: 'thanks' },
];

test('the page has the three patterns\' JavaScript', () => {
  assert.equal(blocks().length, 3);
});

test('1. tile-wide: the board, then the shared bus', async () => {
  const { calls, sockets } = await load((url) =>
    url === '/api/apps/rooms/board' ? json({ alice: { text: 'in a meeting' } }) : new Response(null, { status: 204 }));
  const renders = [];
  const { board } = await run(blocks()[0], { render: (b) => renders.push(structuredClone(b)) }, ['board']);
  assert.deepEqual(renders, [{ alice: { text: 'in a meeting' } }]);
  assert.deepEqual(calls.map((c) => `${c.method} ${c.url} ${c.body ?? ''}`), [
    'GET /api/apps/rooms/board ',
    'POST /api/apps/rooms/status {"text":"in a meeting"}', // your own partition: no xbin-partition
  ]);
  // xbind's event socket delivers a shared bus event: bob's line changed
  assert.equal(sockets.length, 1);
  assert.match(sockets[0].url, /\/ws\/events\?frame=T$/);
  const ev = { type: 'bus', topic: 'res:apps/rooms/live/status/bob', data: { text: 'out' } };
  sockets[0].onmessage({ data: JSON.stringify(ev) });
  sockets[0].onmessage({ data: JSON.stringify({ ...ev, topic: 'res:apps/rooms/other/x' }) });
  assert.deepEqual(renders.at(-1), { alice: { text: 'in a meeting' }, bob: { text: 'out' } });
  assert.equal(renders.length, 2);
  assert.equal(board.bob.text, 'out');
});

test('2. member-scoped: follow and post at the global instance', async () => {
  const enc = new TextEncoder();
  const text = posts.map((p) => JSON.stringify(p) + '\n').join('');
  const cut = text.indexOf('green'); // lines arrive in pieces, split mid-line
  const { calls } = await load((url) => {
    if (url.startsWith('/api/apps/rooms/rooms/7/follow')) {
      return new Response(new ReadableStream({
        start(c) { c.enqueue(enc.encode(text.slice(0, cut))); c.enqueue(enc.encode(text.slice(cut))); c.close(); },
      }), { headers: { 'Content-Type': 'application/x-ndjson' } });
    }
    return new Response(null, { status: url.includes('/rooms/8/') ? 403 : 204 });
  });
  const { follow, post } = await run(blocks()[1], {}, ['follow', 'post']);
  const shown = [];
  await follow('7', (p) => shown.push(p)); // returns once the stream ends
  assert.deepEqual(shown, posts);
  await assert.rejects(follow('8', () => {}), /8: 403/);
  const r = await post('7', 'hi');
  assert.equal(r.status, 204);
  assert.deepEqual(calls.map((c) => `${c.method} ${c.url} ${c.body ?? ''}`), [
    'GET /api/apps/rooms/rooms/7/follow?xbin-partition=global ',
    'GET /api/apps/rooms/rooms/8/follow?xbin-partition=global ',
    'POST /api/apps/rooms/rooms/7/posts?xbin-partition=global {"text":"hi","mentions":[]}',
  ]);
});

test('3. a mention: posted through global, read from your own partition', async () => {
  const { calls } = await load((url) =>
    url === '/api/apps/rooms/mentions' ? json([posts[0]]) : new Response(null, { status: 204 }));
  const { post } = await run(blocks()[1], {}, ['post']);
  const { mine } = await run(blocks()[2], { post }, ['mine']);
  assert.deepEqual(mine, [posts[0]]);
  assert.deepEqual(calls.map((c) => `${c.method} ${c.url} ${c.body ?? ''}`), [
    'POST /api/apps/rooms/rooms/7/posts?xbin-partition=global {"text":"the deploy is green, @bob","mentions":["bob"]}',
    'GET /api/apps/rooms/mentions ',
  ]);
});
