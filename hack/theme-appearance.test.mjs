// hack/theme-appearance.test.mjs — the client side of the theme mechanism
// (D184), run by `make js-test`: web/bx-theme.js reads and changes a
// document's appearance through the two metas xbind injects, keeps the hint
// cookie, validates the xbin:appearance message and tells painting code when
// to re-read tokens; web/theme-boot.js copies the cookie into the meta for
// xbind's pages that get no injection. The DOM is hack/theme-dom.mjs's.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { makeDocument, makeWindow, metasOf } from './theme-dom.mjs';
import {
  THEMES, DENSITIES, MESSAGE, EVENT, COOKIE, appearance, follows, setAppearance, rememberTheme,
  scheme, token, onAppearance, appearanceMessage, applyAppearanceMessage,
} from '../web/bx-theme.js';

test('the contract: names and values', () => {
  assert.deepEqual([...THEMES], ['system', 'light', 'dark']);
  assert.deepEqual([...DENSITIES], ['compact', 'comfortable']);
  assert.ok(Object.isFrozen(THEMES) && Object.isFrozen(DENSITIES));
  assert.equal(MESSAGE, 'xbin:appearance');
  assert.equal(EVENT, 'xbin-appearance');
  assert.equal(COOKIE, 'xbin_theme');
});

test('appearance() reads the injected metas; anything else is the default', () => {
  assert.deepEqual(appearance(makeDocument()), { theme: 'system', density: 'compact' });
  assert.deepEqual(appearance(makeDocument({ theme: 'light' })), { theme: 'light', density: 'compact' });
  assert.deepEqual(appearance(makeDocument({ theme: 'dark', density: 'comfortable' })), { theme: 'dark', density: 'comfortable' });
  for (const [theme, density] of [['purple', 'huge'], ['system', 'compact'], ['LIGHT', 'Comfortable'], ['', '']]) {
    assert.deepEqual(appearance(makeDocument({ metas: { 'xbin-theme': theme, 'xbin-density': density } })), { theme: 'system', density: 'compact' }, `${theme}/${density}`);
  }
  // only a direct child of <head> counts (theme.css's :has(> head > meta) reads the same)
  const doc = makeDocument();
  const stray = doc.createElement('meta');
  stray.setAttribute('name', 'xbin-theme');
  stray.setAttribute('content', 'light');
  doc.body.append(stray);
  assert.equal(appearance(doc).theme, 'system');
});

test('follows() is the opt-in attribute, exactly', () => {
  assert.equal(follows(makeDocument()), true);
  assert.equal(follows(makeDocument({ auto: false })), false);
  const doc = makeDocument({ auto: false });
  doc.documentElement.setAttribute('data-bx-theme', 'light'); // reserved: not an opt-in
  assert.equal(follows(doc), false);
});

test('setAppearance() rewrites the metas, the cookie, and tells the window', () => {
  const doc = makeDocument();
  const win = makeWindow(doc);
  const heard = [];
  win.addEventListener(EVENT, (e) => heard.push(e.detail));

  assert.equal(setAppearance({ theme: 'light' }, doc), true);
  assert.deepEqual(metasOf(doc), { 'xbin-theme': 'light' });
  assert.equal(doc.head.children[0].getAttribute('name'), 'xbin-theme', 'prepended: before the stylesheet');
  assert.match(doc.cookies.at(-1), /^xbin_theme=light; Path=\/; Max-Age=34560000; SameSite=Lax; Secure$/);

  assert.equal(setAppearance({ theme: 'light' }, doc), false, 'nothing changed');
  assert.equal(setAppearance({ theme: 'purple', density: 'huge' }, doc), false, 'unknown values are ignored');

  assert.equal(setAppearance({ density: 'comfortable' }, doc), true);
  assert.deepEqual(metasOf(doc), { 'xbin-density': 'comfortable', 'xbin-theme': 'light' });

  assert.equal(setAppearance({ theme: 'system', density: 'compact' }, doc), true);
  assert.deepEqual(metasOf(doc), {}, 'the defaults are the metas\' absence');
  assert.match(doc.cookies.at(-1), /^xbin_theme=; Path=\/; Max-Age=0; SameSite=Lax; Secure$/);

  assert.deepEqual(heard, [
    { theme: 'light', density: 'compact' },
    { theme: 'light', density: 'comfortable' },
    { theme: 'system', density: 'compact' },
  ]);

  // a duplicate meta (a tile that copied one in) is folded into one
  const dup = makeDocument({ theme: 'dark' });
  const extra = dup.createElement('meta');
  extra.setAttribute('name', 'xbin-theme');
  extra.setAttribute('content', 'dark');
  dup.head.append(extra);
  makeWindow(dup);
  setAppearance({ theme: 'light' }, dup);
  assert.deepEqual(dup.head.children.filter((c) => c.getAttribute('name') === 'xbin-theme').map((m) => m.content), ['light']);
});

test('rememberTheme(): the hint cookie — light or dark, else cleared; http has no Secure', () => {
  const doc = makeDocument({ href: 'http://127.0.0.1:9411/c/shell/' });
  rememberTheme('dark', doc);
  rememberTheme('system', doc);
  rememberTheme('nonsense', doc);
  assert.deepEqual(doc.cookies, [
    'xbin_theme=dark; Path=/; Max-Age=34560000; SameSite=Lax',
    'xbin_theme=; Path=/; Max-Age=0; SameSite=Lax',
    'xbin_theme=; Path=/; Max-Age=0; SameSite=Lax',
  ]);
  // 400 days, the cap browsers allow
  assert.equal(34560000, 400 * 24 * 3600);
  // a sandboxed (opaque-origin) document: cookies throw, nothing breaks
  const opaque = makeDocument({ cookieThrows: true });
  makeWindow(opaque);
  assert.doesNotThrow(() => rememberTheme('light', opaque));
  assert.equal(setAppearance({ theme: 'light' }, opaque), true, 'the metas change even where the cookie can\'t');
});

test('the relay message: built from a document, applied after validation', () => {
  const shell = makeDocument({ theme: 'dark', density: 'comfortable' });
  assert.deepEqual(appearanceMessage(shell), { type: 'xbin:appearance', theme: 'dark', density: 'comfortable' });
  assert.deepEqual(appearanceMessage(makeDocument()), { type: 'xbin:appearance', theme: 'system', density: 'compact' });

  const frame = makeDocument();
  makeWindow(frame);
  assert.equal(applyAppearanceMessage(null, frame), false);
  assert.equal(applyAppearanceMessage({ type: 'xbin:resize', theme: 'light' }, frame), false);
  assert.equal(applyAppearanceMessage({ type: 'xbin:appearance', theme: '<script>', density: 1 }, frame), false);
  assert.deepEqual(metasOf(frame), {});
  assert.equal(applyAppearanceMessage(appearanceMessage(shell), frame), true);
  assert.deepEqual(metasOf(frame), { 'xbin-density': 'comfortable', 'xbin-theme': 'dark' });
  assert.equal(applyAppearanceMessage({ type: 'xbin:appearance', theme: 'system', density: 'compact' }, frame), true);
  assert.deepEqual(metasOf(frame), {});
});

test('onAppearance(): the person\'s change, the system\'s scheme and contrast; unsubscribe', () => {
  const doc = makeDocument();
  const win = makeWindow(doc);
  const seen = [];
  const off = onAppearance((a) => seen.push(a.theme), win);
  setAppearance({ theme: 'dark' }, doc);
  win.flip('(prefers-color-scheme: light)', true);
  win.flip('(prefers-contrast: more)', true);
  assert.deepEqual(seen, ['dark', 'dark', 'dark']);
  off();
  setAppearance({ theme: 'light' }, doc);
  win.flip('(prefers-color-scheme: light)', false);
  assert.equal(seen.length, 3, 'nothing after unsubscribing');
});

test('scheme() and token() read the computed style', () => {
  const styles = new Map();
  globalThis.getComputedStyle = (el) => styles.get(el) ?? { getPropertyValue: () => '', colorScheme: 'normal' };
  const el = {};
  const style = (props, colorScheme = 'normal') => styles.set(el, { getPropertyValue: (n) => props[n] ?? '', colorScheme });
  try {
    style({ '--bx-scheme': ' light' });
    assert.equal(scheme(el), 'light');
    style({ '--bx-scheme': 'dark' });
    assert.equal(scheme(el), 'dark');
    style({}, 'light');
    assert.equal(scheme(el), 'light', 'no sheet but a light color-scheme');
    style({}, 'light dark');
    assert.equal(scheme(el), 'dark', 'ambiguous: the fallbacks are Night');
    style({});
    assert.equal(scheme(el), 'dark', 'a bare document is Night');
    style({ '--bx-term-bg': '  #0B0C12 ' });
    assert.equal(token('--bx-term-bg', el), '#0B0C12');
  } finally {
    delete globalThis.getComputedStyle;
  }
});

// theme-boot.js is a classic script: run it in a context with a document.
function boot(doc) {
  vm.runInNewContext(readFileSync(new URL('../web/theme-boot.js', import.meta.url), 'utf8'), { document: doc });
  return metasOf(doc);
}

test('theme-boot.js copies the hint cookie into the meta, before anything paints', () => {
  for (const [jar, want] of [
    ['xbin_theme=light', { 'xbin-theme': 'light' }],
    ['a=1; xbin_theme=dark; b=2', { 'xbin-theme': 'dark' }],
    ['xbin_theme=', {}],
    ['xbin_theme=purple', {}],
    ['xbin_theme=lightish', {}],
    ['not_xbin_theme=light', {}],
    ['', {}],
  ]) {
    const doc = makeDocument();
    doc.jar = jar;
    assert.deepEqual(boot(doc), want, jar);
    if (want['xbin-theme']) assert.equal(doc.head.children[0].getAttribute('name'), 'xbin-theme', 'first in <head>');
  }
  const injected = makeDocument({ theme: 'dark' });
  injected.jar = 'xbin_theme=light';
  assert.deepEqual(boot(injected), { 'xbin-theme': 'dark' }, 'a meta already there wins');
  const opaque = makeDocument({ cookieThrows: true });
  assert.deepEqual(boot(opaque), {}, 'no cookies: the system');
});
