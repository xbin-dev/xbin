// hack/xbin-client-ws.test.mjs — which origin web/xbin-client.js opens its
// WebSockets on (xbin.ws and the events socket), run by `make js-test`:
// the document's own in a browser; in the xbin app, whose pages load from a
// custom scheme with no ws origin to derive, the injected
// <meta name="xbin-ws-origin"> (docs/protocol.md) — and only a well-formed one.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The client has no import/export statements, so node would load the file
// as CommonJS (cached once per path); a data: URL is always a fresh module.
const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
let n = 0;
// Load a fresh instance of the client into a stub document and return the
// URLs of the sockets it opens.
async function load({ href, metas = {} }) {
  const opened = [];
  globalThis.WebSocket = class { constructor(url) { opened.push(url); } };
  globalThis.location = new URL(href);
  globalThis.window = globalThis;
  globalThis.parent = globalThis; // top level: not embedded
  globalThis.document = {
    querySelector: (sel) => {
      const name = /meta\[name="([^"]+)"\]/.exec(sel)?.[1];
      return name in metas ? { content: metas[name] } : null;
    },
    addEventListener() {},
  };
  const realInterval = globalThis.setInterval;
  globalThis.setInterval = () => 0; // the token refresher would hold the test open
  try {
    await import(`data:text/javascript,${encodeURIComponent(`${src}\n// instance ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  return { xbin: globalThis.xbin, opened };
}

const base = { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T' };

test('a browser page opens sockets on its own origin', async () => {
  const { xbin, opened } = await load({ href: 'https://ws.example/c/apps/x/', metas: base });
  xbin.ws('/api/apps/x/stream');
  xbin.ws('/api/apps/x/stream?since=4');
  xbin.events.on(() => {});
  assert.deepEqual(opened, [
    'wss://ws.example/api/apps/x/stream?frame=T',
    'wss://ws.example/api/apps/x/stream?since=4&frame=T',
    'wss://ws.example/ws/events?frame=T',
  ]);
  const plain = await load({ href: 'http://127.0.0.1:9988/c/apps/x/', metas: base });
  plain.xbin.ws('/api/apps/x/s');
  assert.deepEqual(plain.opened, ['ws://127.0.0.1:9988/api/apps/x/s?frame=T']);
});

test('an app page prefers the injected xbin-ws-origin', async () => {
  const { xbin, opened } = await load({
    href: 'xbin-ws://home/c/apps/x/?native=1',
    metas: { ...base, 'xbin-ws-origin': 'wss://xbin.example.org:8443' },
  });
  xbin.ws('/api/apps/x/stream');
  xbin.events.on(() => {});
  assert.deepEqual(opened, [
    'wss://xbin.example.org:8443/api/apps/x/stream?frame=T',
    'wss://xbin.example.org:8443/ws/events?frame=T',
  ]);
  // xbin.url stays on the page's own origin (the app's scheme handler
  // carries those requests).
  assert.equal(xbin.url('/api/apps/x/export'), 'xbin-ws://home/api/apps/x/export?frame=T');
});

test('a malformed xbin-ws-origin is ignored', async () => {
  for (const bad of ['https://evil.example', 'wss://host/path', 'javascript:alert(1)', 'wss://', ' wss://h']) {
    const { xbin, opened } = await load({ href: 'https://ws.example/c/apps/x/', metas: { ...base, 'xbin-ws-origin': bad } });
    xbin.ws('/api/apps/x/s');
    assert.deepEqual(opened, ['wss://ws.example/api/apps/x/s?frame=T'], bad);
  }
});
