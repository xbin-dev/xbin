// hack/xbin-client-appearance.test.mjs — web/xbin-client.js's side of the
// theme mechanism (D184), run by `make js-test`: a framed document applies
// xbin:appearance from its parent window only — and, on a tile's own origin,
// only from the workspace origin — with bx-theme.js's validation, in the
// order the messages came; a top-level chrome document (the shell)
// refreshes the hint cookie from the injected choice; a sandboxed one never
// touches cookies; and a page whose bx-theme.js can't load keeps its xbin.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { makeDocument, makeWindow, metasOf } from './theme-dom.mjs';

// A fresh instance per load (a data: URL is never cached). The client
// imports /vendor/bx-theme.js beside itself; pointed at the file here (a
// data: module resolves absolute URLs only), or left as it is to see a
// page that can't load it.
const raw = readFileSync(new URL('../web/xbin-client.js', import.meta.url), 'utf8');
const src = raw.replace("'/vendor/bx-theme.js'", JSON.stringify(new URL('../web/bx-theme.js', import.meta.url).href));
let n = 0;

// until(fn): wait for a condition (the theme module loads asynchronously).
async function until(fn, label) {
  for (const deadline = Date.now() + 5000; !fn();) {
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

// load({framed, metas, theme, href, cookieThrows, source}): the client in a
// stub page. The window is globalThis (the client's bare addEventListener,
// window, parent); framed puts a parent window above it that records what
// it is sent.
async function load({ framed = true, metas = {}, theme, href = 'https://ws.example/c/apps/x/', cookieThrows = false, source = src } = {}) {
  const doc = makeDocument({ theme, metas: { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T', ...metas }, href, cookieThrows });
  const win = makeWindow(doc);
  const parent = { posted: [], postMessage(m, origin) { this.posted.push({ m, origin }); } };
  for (const k of ['addEventListener', 'removeEventListener', 'dispatchEvent']) globalThis[k] = win[k].bind(win);
  globalThis.matchMedia = win.matchMedia;
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
    await import(`data:text/javascript,${encodeURIComponent(`${source}\n// instance ${++n}`)}`);
  } finally {
    globalThis.setInterval = realInterval;
  }
  const post = (data, { from = parent, origin = 'null' } = {}) => {
    const e = new Event('message');
    Object.assign(e, { data, source: from, origin });
    win.dispatchEvent(e);
  };
  return { doc, win, parent, post, xbin: globalThis.xbin };
}

const SANDBOX = { 'xbin-sandbox': 'allow-scripts allow-forms allow-modals allow-downloads' };
const theme = (doc) => metasOf(doc)['xbin-theme'];

test('a frame applies xbin:appearance from its parent, in order, restyling at once', async () => {
  const { doc, post } = await load({ metas: SANDBOX, cookieThrows: true }); // a tile: an opaque origin, no cookies
  const heard = [];
  globalThis.addEventListener('xbin-appearance', (e) => heard.push(e.detail));
  post({ type: 'xbin:appearance', theme: 'light', density: 'comfortable' }); // may arrive before bx-theme.js has loaded
  post({ type: 'xbin:appearance', theme: 'dark', density: 'comfortable' });
  await until(() => theme(doc) === 'dark', 'the second message applied');
  assert.deepEqual(metasOf(doc), { 'xbin-component': 'apps/x', 'xbin-frame-token': 'T', ...SANDBOX, 'xbin-theme': 'dark', 'xbin-density': 'comfortable' });
  assert.deepEqual(heard, [{ theme: 'light', density: 'comfortable' }, { theme: 'dark', density: 'comfortable' }], 'its own <bx-frame>s and painters hear each, in order');
  post({ type: 'xbin:appearance', theme: 'system', density: 'compact' });
  await until(() => theme(doc) === undefined, 'back to the system');
  assert.equal(metasOf(doc)['xbin-density'], undefined);
  // chrome framed in the shell (the admin console) has the shell's origin:
  // its cookie write is the shell's own value
  const chrome = await load();
  chrome.post({ type: 'xbin:appearance', theme: 'dark', density: 'compact' });
  await until(() => chrome.doc.cookies.length > 0, 'the chrome frame\'s cookie');
  assert.deepEqual(chrome.doc.cookies, ['xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax; Secure']);
});

test('anything but the parent, or a bad value, changes nothing', async () => {
  const { doc, post } = await load({ theme: 'dark', metas: SANDBOX, cookieThrows: true });
  post({ type: 'xbin:appearance', theme: 'light' }, { from: { postMessage() {} } }); // a sibling or child window
  post({ type: 'xbin:appearance', theme: 'light' }, { from: null });
  post({ type: 'xbin:appearance', theme: 'mauve', density: 'cosy' });
  post({ type: 'xbin:resize', theme: 'light' });
  post('xbin:appearance');
  post({ type: 'xbin:appearance', theme: 'dark', density: 'comfortable' }); // the control: applied after all of the above
  await until(() => metasOf(doc)['xbin-density'] === 'comfortable', 'the control message');
  assert.equal(theme(doc), 'dark');
});

test('on a tile\'s own origin, only the workspace origin is heard', async () => {
  const { doc, post } = await load({ metas: { 'xbin-workspace-origin': 'https://ws.example' }, href: 'https://t-abc.tiles.example/c/apps/x/' });
  post({ type: 'xbin:appearance', theme: 'dark' }, { origin: 'https://t-other.tiles.example' });
  post({ type: 'xbin:appearance', theme: 'light' }, { origin: 'https://ws.example' });
  await until(() => theme(doc) === 'light', 'the workspace\'s message');
  assert.notEqual(theme(doc), 'dark');
  assert.ok(!doc.cookies.includes('xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax; Secure'), 'the other origin\'s never applied');
});

test('the shell (top level, unsandboxed) keeps the hint cookie equal to the injected choice', async () => {
  const dark = await load({ framed: false, theme: 'dark', href: 'https://ws.example/c/shell/' });
  await until(() => dark.doc.cookies.length > 0, 'the shell\'s cookie');
  assert.deepEqual(dark.doc.cookies, ['xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax; Secure']);
  const sys = await load({ framed: false, href: 'http://127.0.0.1:9411/c/shell/' });
  await until(() => sys.doc.cookies.length > 0, 'the cleared cookie');
  assert.deepEqual(sys.doc.cookies, ['xbin_theme=; Path=/; Max-Age=0; SameSite=Lax'], 'no choice: cleared');
  // a top-level sandboxed tile (a direct-tab open) never writes one (the
  // client decides that before anything loads), and hears no xbin:appearance
  const tab = await load({ framed: false, theme: 'light', metas: SANDBOX, cookieThrows: true });
  tab.post({ type: 'xbin:appearance', theme: 'dark' }, { from: globalThis });
  await new Promise((r) => setTimeout(r, 50)); // a negative: nothing is scheduled to change it
  assert.deepEqual(tab.doc.cookies, []);
  assert.equal(theme(tab.doc), 'light');
});

test('a page that can\'t load bx-theme.js keeps its xbin', async () => {
  const { xbin, post, doc } = await load({ source: raw }); // the import fails in a data: module, as a 404 would
  assert.equal(xbin.self, 'apps/x');
  post({ type: 'xbin:appearance', theme: 'light' });
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(theme(doc), undefined);
});
