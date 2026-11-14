// hack/xbin-client-location.test.mjs — web/xbin-client.js's side of a link
// to a place in a tile (#7, D220), run by `make js-test`: a framed page
// reports its fragment to its parent (xbin:location) on start and on every
// change — hashchange, popstate, its own pushState/replaceState — once per
// change; it follows xbin:navigate from its parent only (on a tile's own
// origin, only from the workspace origin) by replacing the fragment; and a
// top-level page (the shell) does neither.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { makeDocument, makeWindow } from './theme-dom.mjs';

const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
let n = 0;

// load({framed, metas, href}): the client in a stub page whose location
// replace() moves the fragment and fires hashchange, as a browser's does.
async function load({ framed = true, metas = {}, href = 'https://ws.example/c/apps/x/?frame=T' } = {}) {
  const doc = makeDocument({ metas: { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T', ...metas }, href });
  const win = makeWindow(doc);
  const parent = { posted: [], postMessage(m, origin) { this.posted.push({ m, origin }); } };
  for (const k of ['addEventListener', 'removeEventListener', 'dispatchEvent']) globalThis[k] = win[k].bind(win);
  globalThis.matchMedia = win.matchMedia;
  doc.defaultView = globalThis;
  globalThis.window = globalThis;
  globalThis.parent = framed ? parent : globalThis;
  globalThis.document = doc;
  let url = new URL(href);
  const replaced = [];
  globalThis.location = {
    get href() { return url.href; }, get hash() { return url.hash; }, get pathname() { return url.pathname; }, get search() { return url.search; },
    replace(u) { replaced.push(u); url = new URL(u); win.dispatchEvent(new Event('hashchange')); },
  };
  // the page's own history calls move the URL, as a browser's do
  const moveTo = (u) => { if (u != null) url = new URL(String(u), url.href); };
  globalThis.history = { pushState(_s, _t, u) { moveTo(u); }, replaceState(_s, _t, u) { moveTo(u); } };
  globalThis.WebSocket = class { };
  globalThis.ResizeObserver = class { observe() {} };
  globalThis.fetch = async () => new Response('{}');
  delete globalThis.xbin;
  const realInterval = globalThis.setInterval;
  globalThis.setInterval = () => 0; // the token refresher would hold the test open
  try {
    await import(`data:text/javascript,${encodeURIComponent(`${src}\n// instance ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  const post = (data, { from = parent, origin = 'null' } = {}) => {
    const e = new Event('message');
    Object.assign(e, { data, source: from, origin });
    win.dispatchEvent(e);
  };
  const locs = () => parent.posted.filter((p) => p.m?.type === 'xbin:location').map((p) => p.m.hash);
  const setHash = (h) => { url.hash = h; win.dispatchEvent(new Event('hashchange')); };
  return { win, parent, post, locs, setHash, replaced, url: () => url };
}

test('a frame reports its fragment on start and on every change, once each', async () => {
  const p = await load({ href: 'https://ws.example/c/apps/x/#orgs/acme' });
  assert.deepEqual(p.locs(), ['orgs/acme']);
  p.setHash('users');
  history.replaceState(null, '', '#users'); // the same place again: no message
  history.pushState(null, '', '#orgs/b');
  history.replaceState(null, '', '/c/apps/x/'); // the fragment cleared
  p.win.dispatchEvent(new Event('popstate'));
  assert.deepEqual(p.locs(), ['orgs/acme', 'users', 'orgs/b', '']);
  assert.ok(p.parent.posted.filter((x) => x.m.type === 'xbin:location').every((x) => x.origin === '*'));
});

test('xbin:navigate from the parent replaces the fragment, without a reload', async () => {
  const p = await load();
  p.post({ type: 'xbin:navigate', hash: 'storage/kv' });
  assert.equal(p.url().hash, '#storage/kv');
  assert.equal(p.url().search, '?frame=T'); // the rest of the URL is the page's
  p.post({ type: 'xbin:navigate', hash: '' });
  assert.equal(p.replaced.at(-1), 'https://ws.example/c/apps/x/?frame=T#'); // '#' alone: a fragment move, never a reload
  assert.deepEqual(p.locs(), ['', 'storage/kv', '']);
});

test('xbin:navigate to where the page is says so again', async () => {
  const p = await load({ href: 'https://ws.example/c/apps/x/#a' });
  p.post({ type: 'xbin:navigate', hash: 'a' });
  assert.equal(p.replaced.length, 0);
  assert.deepEqual(p.locs(), ['a', 'a']);
});

test('only the parent — on a tile origin, only from the workspace origin — moves the page', async () => {
  const p = await load();
  p.post({ type: 'xbin:navigate', hash: 'x' }, { from: {} });
  assert.equal(p.replaced.length, 0);
  const o = await load({ metas: { 'xbin-workspace-origin': 'https://ws.example' }, href: 'https://apps-x.tiles.example/c/apps/x/' });
  o.post({ type: 'xbin:navigate', hash: 'x' }, { origin: 'https://evil.example' });
  assert.equal(o.replaced.length, 0);
  o.post({ type: 'xbin:navigate', hash: 'x' }, { origin: 'https://ws.example' });
  assert.equal(o.url().hash, '#x');
  assert.ok(o.parent.posted.filter((x) => x.m.type === 'xbin:location').every((x) => x.origin === 'https://ws.example'));
});

test('a top-level page neither reports nor follows', async () => {
  const p = await load({ framed: false, href: 'https://ws.example/c/root/#x' });
  p.post({ type: 'xbin:navigate', hash: 'y' }, { from: globalThis });
  assert.equal(p.replaced.length, 0);
  assert.equal(p.url().hash, '#x');
});
