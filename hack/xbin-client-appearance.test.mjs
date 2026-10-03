// hack/xbin-client-appearance.test.mjs — web/xbin-client.js's side of the
// theme mechanism (D184), run by `make js-test`: a framed document applies
// xbin:appearance from its parent window only — and, on a tile's own origin,
// only from the workspace origin — with bx-theme.js's validation; a
// top-level chrome document (the shell) refreshes the hint cookie from the
// injected choice; a sandboxed one never touches cookies.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { makeDocument, makeWindow, metasOf } from './theme-dom.mjs';

// A fresh instance per load (a data: URL is never cached), its bx-theme.js
// import pointed at the file (a data: module resolves absolute URLs only).
const src = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8')
  .replace("'/vendor/bx-theme.js'", JSON.stringify(new URL('../web/bx-theme.js', import.meta.url).href));
let n = 0;

// load({framed, metas, theme, workspace}): the client in a stub page. The
// window is globalThis (the client's bare addEventListener/window/parent);
// framed puts a parent window above it that records what it is sent.
async function load({ framed = true, metas = {}, theme, href = 'https://ws.example/c/apps/x/', cookieThrows = false } = {}) {
  const doc = makeDocument({ theme, metas: { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T', ...metas }, href, cookieThrows });
  const win = makeWindow(doc);
  const parent = { posted: [], postMessage(m, origin) { this.posted.push({ m, origin }); } };
  for (const k of ['addEventListener', 'removeEventListener', 'dispatchEvent', 'matchMedia']) globalThis[k] = win[k].bind ? win[k].bind(win) : win[k];
  doc.defaultView = globalThis;
  globalThis.window = globalThis;
  globalThis.parent = framed ? parent : globalThis;
  globalThis.document = doc;
  globalThis.location = new URL(href);
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
  const post = (data, { source = parent, origin = 'null' } = {}) => {
    const e = new Event('message');
    Object.assign(e, { data, source, origin });
    win.dispatchEvent(e);
  };
  return { doc, win, parent, post };
}

const SANDBOX = { 'xbin-sandbox': 'allow-scripts allow-forms allow-modals allow-downloads' };

test('a frame applies xbin:appearance from its parent, restyling at once', async () => {
  const { doc, post } = await load({ metas: SANDBOX, cookieThrows: true }); // a tile: an opaque origin, no cookies
  const heard = [];
  globalThis.addEventListener('xbin-appearance', (e) => heard.push(e.detail));
  post({ type: 'xbin:appearance', theme: 'light', density: 'comfortable' });
  assert.deepEqual(metasOf(doc), { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T', ...SANDBOX, 'xbin-theme': 'light', 'xbin-density': 'comfortable' });
  assert.deepEqual(heard, [{ theme: 'light', density: 'comfortable' }], 'its own <bx-frame>s and painters hear it');
  post({ type: 'xbin:appearance', theme: 'system', density: 'compact' });
  assert.equal(metasOf(doc)['xbin-theme'], undefined);
  assert.equal(metasOf(doc)['xbin-density'], undefined);
  // chrome framed in the shell (the admin console) has the shell's origin:
  // its cookie write is the shell's own value
  const chrome = await load();
  chrome.post({ type: 'xbin:appearance', theme: 'dark', density: 'compact' });
  assert.deepEqual(chrome.doc.cookies, ['xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax; Secure']);
});

test('anything but the parent, or a bad value, changes nothing', async () => {
  const { doc, post } = await load({ theme: 'dark' });
  const before = metasOf(doc);
  post({ type: 'xbin:appearance', theme: 'light' }, { source: { postMessage() {} } }); // a sibling or child window
  post({ type: 'xbin:appearance', theme: 'light' }, { source: null });
  post({ type: 'xbin:appearance', theme: 'mauve', density: 'cosy' });
  post({ type: 'xbin:resize', theme: 'light' });
  post('xbin:appearance');
  assert.deepEqual(metasOf(doc), before);
});

test('on a tile\'s own origin, only the workspace origin is heard', async () => {
  const { doc, post } = await load({ metas: { 'xbin-workspace-origin': 'https://ws.example' }, href: 'https://t-abc.tiles.example/c/apps/x/' });
  post({ type: 'xbin:appearance', theme: 'light' }, { origin: 'https://t-other.tiles.example' });
  assert.equal(metasOf(doc)['xbin-theme'], undefined, 'another origin, even as the parent');
  post({ type: 'xbin:appearance', theme: 'light' }, { origin: 'https://ws.example' });
  assert.equal(metasOf(doc)['xbin-theme'], 'light');
});

test('the shell (top level, unsandboxed) keeps the hint cookie equal to the injected choice', async () => {
  const dark = await load({ framed: false, theme: 'dark', href: 'https://ws.example/c/shell/' });
  assert.deepEqual(dark.doc.cookies, ['xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax; Secure']);
  const sys = await load({ framed: false, href: 'http://127.0.0.1:9411/c/shell/' });
  assert.deepEqual(sys.doc.cookies, ['xbin_theme=; Path=/; Max-Age=0; SameSite=Lax'], 'no choice: cleared');
  // a top-level sandboxed tile (a direct-tab open) leaves cookies alone
  const tab = await load({ framed: false, theme: 'light', metas: { 'xbin-sandbox': 'allow-scripts allow-forms allow-modals allow-downloads' }, cookieThrows: true });
  assert.deepEqual(tab.doc.cookies, []);
  // and a top-level page hears no xbin:appearance (it has no embedder)
  tab.post({ type: 'xbin:appearance', theme: 'dark' }, { source: globalThis });
  assert.equal(metasOf(tab.doc)['xbin-theme'], 'light');
});
