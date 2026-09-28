// hack/deploy-aware.test.mjs — covers P12 P17 NP-10-2 NP-12-2 — the
// binary-served components that learn about tile deployments, run by
// `make js-test`: how web/frame-info.js resolves a frame src qualified with a
// deployment (and mints its bootstrap token only on the server's echo), what
// web/term-sessions.js saves of the window's layout, and web/xbin-client.js's
// optional xbin.deployment. A zero-state tile answers exactly as before
// throughout.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolveSrc } from '../web/frame-info.js';
import { windowPref, makeStore } from '../web/term-sessions.js';

// /components rows as frame-info caches them: a tile with a record carries
// the primary summary; a zero-state tile doesn't.
const summary = { primary: 'main', pinned: true, protected: false };
const rows = (extra = {}) => new Map(Object.entries({
  'apps/crm': { chrome: false, sandbox: [], origin: '', deployments: summary },
  'apps/crm/widgets': { chrome: false, sandbox: [], origin: '', deployments: null },
  'apps/plain': { chrome: false, sandbox: [], origin: '', deployments: null },
  ...extra,
}));

test('resolveSrc: bare srcs resolve as today, by longest prefix', () => {
  const m = rows();
  assert.deepEqual(resolveSrc('apps/crm', m), { path: 'apps/crm', deployment: '', bare: 'apps/crm' });
  assert.deepEqual(resolveSrc('apps/crm/compose', m), { path: 'apps/crm', deployment: '', bare: 'apps/crm/compose' }, 'an xbin.window sub-path');
  assert.deepEqual(resolveSrc('apps/crm/widgets', m), { path: 'apps/crm/widgets', deployment: '', bare: 'apps/crm/widgets' }, 'a nested tile');
  assert.equal(resolveSrc('apps/nothing', m), null);
});

test('resolveSrc: a "+<name>" qualifies a tile with deployments', () => {
  const m = rows();
  assert.deepEqual(resolveSrc('apps/crm+dev', m), { path: 'apps/crm', deployment: 'dev', bare: 'apps/crm' });
  assert.deepEqual(resolveSrc('apps/crm+dev/compose', m), { path: 'apps/crm', deployment: 'dev', bare: 'apps/crm/compose' },
    'a window of a deployment frame stays inside the deployment');
  assert.deepEqual(resolveSrc('apps/crm+main', m), { path: 'apps/crm', deployment: 'main', bare: 'apps/crm' }, 'the primary alias');
  assert.deepEqual(resolveSrc('apps/crm/widgets+dev', m), { path: 'apps/crm', deployment: '', bare: 'apps/crm/widgets+dev', stale: 'apps/crm/widgets' },
    "a nested tile's own qualifier: never its parent's deployment; the nested row may predate its record");
  assert.equal(resolveSrc('apps/crm+Dev', m), null, 'not a deployment name: no component, as today');
  assert.equal(resolveSrc('apps/crm+', m), null);
  assert.deepEqual(resolveSrc('apps/crm/x+Dev', m), { path: 'apps/crm', deployment: '', bare: 'apps/crm/x+Dev' }, 'a sub-path, as today');
});

test('resolveSrc: a zero-state tile never splits, and a component at the full ref wins', () => {
  const m = rows();
  assert.deepEqual(resolveSrc('apps/plain+dev', m), { path: null, deployment: '', bare: 'apps/plain+dev', stale: 'apps/plain' },
    "'+' means nothing on a zero-state tile (its cached row is refreshed once in case it just got a record)");
  const shadow = rows({ 'apps/crm+dev': { chrome: false, sandbox: [], origin: '', deployments: null } });
  assert.deepEqual(resolveSrc('apps/crm+dev', shadow), { path: 'apps/crm+dev', deployment: '', bare: 'apps/crm+dev' }, 'a tile at <tile>+<name> wins');
  assert.deepEqual(resolveSrc('apps/crm+dev/x', shadow), { path: 'apps/crm+dev', deployment: '', bare: 'apps/crm+dev/x' });
});

// frameSource in a browser with credentialless iframes: a fresh instance of
// the module (a query string) loaded after the stub prototype exists.
globalThis.HTMLIFrameElement = function HTMLIFrameElement() { };
globalThis.HTMLIFrameElement.prototype.credentialless = false;
const browser = await import('../web/frame-info.js?credentialless');

// which token a frame asks for, and whether it keeps it
async function source(src, info, answer) {
  const asked = [];
  const real = globalThis.fetch;
  globalThis.fetch = async (url) => { asked.push(url); return { ok: true, json: async () => answer }; };
  try {
    return { ...(await browser.frameSource(src, info)), asked };
  } finally {
    globalThis.fetch = real;
  }
}
const devInfo = (extra = {}) => ({ chrome: false, sandbox: [], origin: '', deployments: summary, path: 'apps/crm', deployment: 'dev', bare: 'apps/crm', ...extra });

test('frameSource: a deployment document keeps its URL and uses only a token the server echoed', async () => {
  assert.equal(browser.CREDENTIALLESS, true);
  const ok = await source('apps/crm+dev', devInfo(), { token: 'X', deployment: 'dev' });
  assert.deepEqual([ok.url, ok.credentialless, ok.asked], ['/c/apps/crm+dev/?frame=X', true, ['/api/xbin/frame-token?component=apps%2Fcrm&deployment=dev']]);
  const old = await source('apps/crm+dev', devInfo(), { token: 'X' });
  assert.deepEqual([old.url, old.credentialless], ['/c/apps/crm+dev/', false], "no echo: the primary's token — unused, the frame loads by cookie");
  const other = await source('apps/crm+dev', devInfo(), { token: 'X', deployment: 'qa' });
  assert.equal(other.credentialless, false);
  const win = await source('apps/crm+dev/compose', devInfo({ bare: 'apps/crm/compose' }), { token: 'W', deployment: 'dev' });
  assert.deepEqual(win.asked, ['/api/xbin/frame-token?component=apps%2Fcrm%2Fcompose&deployment=dev'], 'a window mints for its bare path, in the deployment');
  const alias = await source('apps/crm+main', devInfo({ deployment: 'main' }), { token: 'M' });
  assert.equal(alias.url, '/c/apps/crm+main/?frame=M', "main's claim is the absent one (the name rule)");
});

test('frameSource: a zero-state frame asks exactly what it asked before', async () => {
  const zero = await source('apps/plain', { chrome: false, sandbox: [], origin: '', deployments: null, path: 'apps/plain' }, { token: 'Z' });
  assert.deepEqual([zero.url, zero.credentialless, zero.asked], ['/c/apps/plain/?frame=Z', true, ['/api/xbin/frame-token?component=apps%2Fplain']]);
  const none = await source('apps/crm+dev', null, { token: 'Z' });
  assert.deepEqual(none.asked, ['/api/xbin/frame-token?component=apps%2Fcrm%2Bdev'], 'no facts (an older xbind): the src as it is, as today');
  const chrome = await source('apps/crm+main', devInfo({ deployment: 'main', chrome: true }), { token: 'C' });
  assert.deepEqual([chrome.url, chrome.sandboxed, chrome.asked], ['/c/apps/crm+main/', false, []]);
});

test('qualifiedSrc: a deployment ref never goes into a query; a tile named with "+" does (P17)', async () => {
  const fresh = await import('../web/frame-info.js?qualified');
  const real = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: true,
    json: async () => [{ path: 'apps/crm', deployments: summary }, { path: 'notes+ideas' }, { path: 'apps/plain' }],
  });
  try {
    assert.equal(await fresh.qualifiedSrc('apps/crm+dev'), true);
    assert.equal(await fresh.qualifiedSrc('apps/crm+dev/compose'), true, 'a window of a deployment frame');
    assert.equal(await fresh.qualifiedSrc('apps/crm'), false);
    assert.equal(await fresh.qualifiedSrc('notes+ideas'), false, 'a tile whose own name holds "+": an exact match');
  } finally {
    globalThis.fetch = real;
  }
});

test('the window pref never records the Deployments layout', async () => {
  assert.deepEqual(windowPref({ open: true, layout: 'deployments', active: 0 }), { open: true, layout: 'term', active: 0 });
  for (const layout of ['term', 'code', 'split', 'logs', 'prs', undefined]) {
    const w = { open: true, layout };
    assert.equal(windowPref(w), w, `${layout}: saved as it is`);
  }
  assert.equal(windowPref(null), null);
  const calls = [];
  const st = makeStore({ fetch: async (url, init = {}) => { calls.push({ url, init }); return { ok: true, text: async () => '' }; } });
  await st.saveWindow('apps/crm', { open: true, active: 1, layout: 'deployments', codeW: 50 });
  assert.deepEqual(JSON.parse(calls[0].init.body), { open: true, active: 1, layout: 'term', codeW: 50 });
});

// xbin-client.js is a classic script with no exports: load a fresh instance
// into a stub document (hack/xbin-client-ws.test.mjs's way).
const client = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
let n = 0;
async function loadClient(metas) {
  globalThis.WebSocket = class { };
  globalThis.location = new URL('https://ws.example/c/apps/crm+dev/');
  globalThis.window = globalThis;
  globalThis.parent = globalThis;
  globalThis.document = {
    querySelector: (sel) => { const name = /meta\[name="([^"]+)"\]/.exec(sel)?.[1]; return name in metas ? { content: metas[name] } : null; },
    addEventListener() {},
  };
  delete globalThis.xbin;
  const realInterval = globalThis.setInterval;
  globalThis.setInterval = () => 0;
  try {
    await import(`data:text/javascript,${encodeURIComponent(`${client}\n// instance ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  return globalThis.xbin;
}

test('xbin.deployment exists only in a non-primary document', async () => {
  const dev = await loadClient({ 'xbin-component': 'apps/crm', 'xbin-frame-token': 'T', 'xbin-deployment': 'dev' });
  assert.equal(dev.deployment, 'dev');
  assert.equal(dev.self, 'apps/crm', 'xbin.self stays the tile path');
  assert.ok(Object.isFrozen(dev));
  const primary = await loadClient({ 'xbin-component': 'apps/crm', 'xbin-frame-token': 'T' });
  assert.equal('deployment' in primary, false, 'absent means the primary: no key at all');
});
