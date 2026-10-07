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
  DEVICE_KEY, DEVICE_PARAM, effectiveTheme, deviceTheme, personTheme, personAppearance, syncDeviceTheme,
  setDeviceTheme, setPersonAppearance, deviceUrl,
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

// ---- a device override (D188): device → person → system ----

// storage(init, {throws}): the localStorage a window has
function storage(init = {}, { throws = false } = {}) {
  const m = new Map(Object.entries(init));
  const guard = () => { if (throws) throw new DOMException('storage off', 'SecurityError'); };
  return {
    getItem: (k) => { guard(); return m.has(k) ? m.get(k) : null; },
    setItem: (k, v) => { guard(); m.set(k, String(v)); },
    removeItem: (k) => { guard(); m.delete(k); },
    map: m,
  };
}
function devicePage({ theme, density, device, auto = true, throws = false } = {}) {
  const doc = makeDocument({ theme, density, auto });
  const win = makeWindow(doc);
  win.localStorage = storage(device ? { [DEVICE_KEY]: device } : {}, { throws });
  return { doc, win };
}

test('effectiveTheme(): the device, then the person, then the system', () => {
  assert.equal(DEVICE_KEY, 'xbin-theme-device');
  assert.equal(DEVICE_PARAM, 'xbin-appearance');
  for (const [person, device, want] of [
    ['system', '', 'system'], ['light', '', 'light'], ['dark', '', 'dark'],
    ['system', 'light', 'light'], ['dark', 'light', 'light'], ['light', 'dark', 'dark'], ['system', 'dark', 'dark'],
    ['light', 'system', 'light'], ['dark', 'purple', 'dark'], ['purple', '', 'system'], [undefined, undefined, 'system'],
  ]) assert.equal(effectiveTheme(person, device), want, `${person} + ${device}`);
});

test('deviceTheme(): exactly light or dark from this browser; else none', () => {
  for (const [v, want] of [['light', 'light'], ['dark', 'dark'], ['system', ''], ['Dark', ''], ['', ''], [undefined, '']]) {
    assert.equal(deviceTheme(devicePage({ device: v }).win), want, String(v));
  }
  assert.equal(deviceTheme(devicePage({ device: 'dark', throws: true }).win), '', 'storage off: none');
  assert.equal(deviceTheme(undefined), '', 'no window: none');
  assert.equal(deviceTheme({}), '', 'no storage: none');
});

test('syncDeviceTheme(): the page shows the device; the person waits on <html>; clearing brings them back', () => {
  const { doc, win } = devicePage({ theme: 'light', density: 'comfortable', device: 'dark' });
  const heard = [];
  win.addEventListener(EVENT, (e) => heard.push(e.detail.theme));
  assert.equal(syncDeviceTheme(doc), true);
  assert.deepEqual(metasOf(doc), { 'xbin-theme': 'dark', 'xbin-density': 'comfortable' });
  assert.deepEqual(appearance(doc), { theme: 'dark', density: 'comfortable' }, 'what the page paints (and bx-frame relays)');
  assert.deepEqual(personAppearance(doc), { theme: 'light', density: 'comfortable' }, "the person's own choice");
  assert.equal(doc.documentElement.getAttribute('data-bx-theme-person'), 'light');
  assert.match(doc.cookies.at(-1), /^xbin_theme=dark;/, 'the hint cookie is the device');
  assert.deepEqual(heard, ['dark']);
  assert.equal(syncDeviceTheme(doc), false, 'again: nothing changes');
  assert.equal(personTheme(doc), 'light', '…and the device never overwrites the person');

  // the person changes their theme elsewhere: the page keeps the device
  assert.equal(setPersonAppearance({ theme: 'system' }, doc), true);
  assert.equal(appearance(doc).theme, 'dark');
  assert.equal(personTheme(doc), 'system');
  assert.deepEqual(heard, ['dark'], 'no restyle, no relay');
  // the density still applies at once
  assert.equal(setPersonAppearance({ density: 'compact' }, doc), true);
  assert.deepEqual(appearance(doc), { theme: 'dark', density: 'compact' });

  // Follow my setting: the person's choice again
  assert.equal(setDeviceTheme('', doc), true);
  assert.equal(win.localStorage.map.has(DEVICE_KEY), false);
  assert.deepEqual(appearance(doc), { theme: 'system', density: 'compact' });
  assert.equal(doc.documentElement.hasAttribute('data-bx-theme-person'), false);
  assert.match(doc.cookies.at(-1), /^xbin_theme=; .*Max-Age=0/, 'the cookie follows the person (system: cleared)');
  assert.deepEqual(heard, ['dark', 'dark', 'system']);
  // without an override setPersonAppearance is setAppearance
  assert.equal(setPersonAppearance({ theme: 'light' }, doc), true);
  assert.equal(appearance(doc).theme, 'light');
});

test('setDeviceTheme(): stored and applied; a page that does not follow the person is left alone', () => {
  const { doc, win } = devicePage({ theme: 'dark' });
  assert.equal(setDeviceTheme('light', doc), true);
  assert.equal(win.localStorage.map.get(DEVICE_KEY), 'light');
  assert.equal(appearance(doc).theme, 'light');
  assert.equal(personTheme(doc), 'dark');
  assert.equal(setDeviceTheme('dark', doc), true, 'another device pick');
  assert.equal(appearance(doc).theme, 'dark');
  assert.equal(personTheme(doc), 'dark', "still the person's dark, set aside once");
  assert.equal(setDeviceTheme('purple', doc), true, 'junk clears it');
  assert.equal(win.localStorage.map.has(DEVICE_KEY), false);

  const off = devicePage({ theme: 'dark', throws: true });
  assert.equal(setDeviceTheme('light', off.doc), false, 'storage off: not stored');
  assert.equal(appearance(off.doc).theme, 'dark');

  const old = devicePage({ theme: 'dark', device: 'light', auto: false });
  assert.equal(syncDeviceTheme(old.doc), false, 'a root page from before D184');
  assert.equal(appearance(old.doc).theme, 'dark');
});

test('deviceUrl(): a tile URL asks for the device theme, only under an override in a following page', () => {
  assert.equal(deviceUrl('/c/apps/a/', devicePage({ device: 'dark' }).doc), '/c/apps/a/?xbin-appearance=dark');
  assert.equal(deviceUrl('/c/apps/a/?frame=t', devicePage({ device: 'light' }).doc), '/c/apps/a/?frame=t&xbin-appearance=light');
  assert.equal(deviceUrl('/c/apps/a/', devicePage({}).doc), '/c/apps/a/');
  assert.equal(deviceUrl('/c/apps/a/', devicePage({ device: 'dark', auto: false }).doc), '/c/apps/a/');
  assert.equal(deviceUrl('/c/apps/a/', devicePage({ device: 'dark', throws: true }).doc), '/c/apps/a/');
});

test('theme-boot.js: the device override wins over the injected meta and the cookie, before the first paint', () => {
  const run = (doc, ls) => {
    vm.runInNewContext(readFileSync(new URL('../web/theme-boot.js', import.meta.url), 'utf8'), { document: doc, localStorage: ls });
    return metasOf(doc);
  };
  for (const [injected, device, jar, want, person] of [
    ['light', 'dark', '', 'dark', 'light'],
    [undefined, 'light', '', 'light', 'system'],
    [undefined, 'dark', 'xbin_theme=light', 'dark', 'system'],
    ['dark', 'purple', '', 'dark', null],
    [undefined, 'system', 'xbin_theme=light', 'light', null],
  ]) {
    const doc = makeDocument({ theme: injected });
    doc.jar = jar;
    assert.deepEqual(run(doc, storage({ [DEVICE_KEY]: device })), { 'xbin-theme': want }, `${injected} / ${device} / ${jar}`);
    assert.equal(doc.documentElement.getAttribute('data-bx-theme-person'), person, `${injected} / ${device}: the person set aside`);
    assert.equal(personAppearance(doc).theme, person ?? want);
  }
  const off = makeDocument({ theme: 'light' });
  assert.deepEqual(run(off, storage({ [DEVICE_KEY]: 'dark' }, { throws: true })), { 'xbin-theme': 'light' }, 'storage off: as before');
});
