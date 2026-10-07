/**
 * bx-theme.js — a document's appearance (D184): the person's theme and
 * density, read from the document and kept current. Dependency-free: tile
 * documents, the shell and core elements all import it.
 *
 * A document opts in to following the person with <html data-bx-theme="auto">
 * and links /vendor/theme.css; the sheet does the rest at first paint from
 * two metas in <head>, which xbind's document injection writes when the
 * person chose something other than the default:
 *
 *   <meta name="xbin-theme" content="light|dark">     absent: follow the system
 *   <meta name="xbin-density" content="comfortable">  absent: compact
 *
 * This module is how they change afterwards:
 *
 *   appearance(doc)          → {theme: 'system'|'light'|'dark', density: 'compact'|'comfortable'}
 *   setAppearance(next, doc) — rewrite the metas (the sheet restyles at once),
 *                              refresh the hint cookie, and dispatch
 *                              'xbin-appearance' on the window; false when
 *                              nothing changed. Unknown values are ignored.
 *   follows(doc)             — the document opted in
 *   scheme(el)               → 'light'|'dark': what el renders in now (theme.css's
 *                              --bx-scheme; 'dark' without the sheet, as the
 *                              var() fallbacks are Night)
 *   token(name, el)          — a token's current value at el, for code that
 *                              paints (xterm, canvases)
 *   onAppearance(cb, win)    — cb(appearance) after a change of the person's
 *                              choice, the system's light/dark or its contrast
 *                              setting; re-read tokens there. Returns an unsubscribe.
 *   appearanceMessage(doc), applyAppearanceMessage(data, doc)
 *                            — the embedder → frame message
 *                              {type: 'xbin:appearance', theme, density}
 *                              (docs/protocol.md): <bx-frame> posts it to its
 *                              iframe on every load and change, xbin-client.js
 *                              applies it, so frames follow the shell live.
 *
 * The hint cookie (xbin_theme=light|dark, absent for system) is for xbind's
 * own pages that have no injection — the sign-in pages read it server-side,
 * the partitions page through /vendor/theme-boot.js. It is a UI hint, never
 * a credential; in a sandboxed document cookies throw and nothing is written.
 *
 * A device override (D188): this browser shows Light or Dark whatever the
 * person's theme says — localStorage `xbin-theme-device`, set from the
 * shell's settings menu. The order is device → person → system
 * (effectiveTheme). In a document that follows the person, the theme meta
 * then holds the device's theme (what the page paints, and what <bx-frame>
 * relays to its tiles), and the person's own choice waits on <html
 * data-bx-theme-person>; /vendor/theme-boot.js does that before the first
 * paint, syncDeviceTheme afterwards. While it is set the hint cookie holds
 * the device's theme too, and <bx-frame> asks for it in a tile's URL
 * (?xbin-appearance=light|dark, docs/protocol.md), so a tile's first frame
 * is right:
 *
 *   deviceTheme(win)          → 'light'|'dark'|'' (no override)
 *   setDeviceTheme(v, doc)    — set ('light'|'dark') or clear ('') it, and apply
 *   syncDeviceTheme(doc)      — apply what this browser has stored now
 *   personTheme(doc), personAppearance(doc)
 *                             — the person's choice, with or without an override
 *   setPersonAppearance(next, doc)
 *                             — the person changed their choice: setAppearance,
 *                               but under an override the theme only waits
 *   effectiveTheme(person, device) → what a document shows
 *   deviceUrl(url, doc)       — url with the override's query parameter
 */

export const THEMES = Object.freeze(['system', 'light', 'dark']);
export const DENSITIES = Object.freeze(['compact', 'comfortable']);
export const MESSAGE = 'xbin:appearance';
export const EVENT = 'xbin-appearance';
export const COOKIE = 'xbin_theme';

const META_THEME = 'xbin-theme';
const META_DENSITY = 'xbin-density';

const metaOf = (doc, name) => doc.querySelector(`head > meta[name="${name}"]`);

export function appearance(doc = document) {
  const t = metaOf(doc, META_THEME)?.content;
  const d = metaOf(doc, META_DENSITY)?.content;
  return {
    theme: t === 'light' || t === 'dark' ? t : 'system',
    density: d === 'comfortable' ? 'comfortable' : 'compact',
  };
}

export const follows = (doc = document) => doc.documentElement?.getAttribute('data-bx-theme') === 'auto';

function writeMeta(doc, name, value) {
  const all = doc.querySelectorAll(`head > meta[name="${name}"]`);
  if (value == null) { all.forEach((m) => m.remove()); return; }
  let m = all[0];
  for (const extra of [...all].slice(1)) extra.remove();
  if (!m) {
    m = doc.createElement('meta');
    m.setAttribute('name', name);
    doc.head.prepend(m);
  }
  m.setAttribute('content', value);
}

export function setAppearance(next = {}, doc = document) {
  const cur = appearance(doc);
  const theme = THEMES.includes(next.theme) ? next.theme : cur.theme;
  const density = DENSITIES.includes(next.density) ? next.density : cur.density;
  if (theme === cur.theme && density === cur.density) return false;
  writeMeta(doc, META_THEME, theme === 'system' ? null : theme);
  writeMeta(doc, META_DENSITY, density === 'compact' ? null : density);
  rememberTheme(theme, doc);
  doc.defaultView?.dispatchEvent(new CustomEvent(EVENT, { detail: { theme, density } }));
  return true;
}

// rememberTheme keeps the hint cookie equal to the person's theme — or,
// while this browser overrides it, the device's.
export function rememberTheme(theme, doc = document) {
  theme = deviceTheme(doc.defaultView) || theme;
  try {
    const secure = doc.location?.protocol === 'https:' ? '; Secure' : '';
    doc.cookie = theme === 'light' || theme === 'dark'
      ? `${COOKIE}=${theme}; Path=/; Max-Age=34560000; SameSite=Lax${secure}`
      : `${COOKIE}=; Path=/; Max-Age=0; SameSite=Lax${secure}`;
  } catch { /* an opaque-origin document has no cookies */ }
}

export function scheme(el = document.documentElement) {
  const cs = getComputedStyle(el);
  const v = cs.getPropertyValue('--bx-scheme').trim();
  if (v === 'light' || v === 'dark') return v;
  return /\blight\b/.test(cs.colorScheme) && !/\bdark\b/.test(cs.colorScheme) ? 'light' : 'dark';
}

export const token = (name, el = document.documentElement) => getComputedStyle(el).getPropertyValue(name).trim();

export function onAppearance(cb, win = window) {
  const fire = () => cb(appearance(win.document));
  const queries = ['(prefers-color-scheme: light)', '(prefers-contrast: more)']
    .map((q) => win.matchMedia?.(q)).filter(Boolean);
  win.addEventListener(EVENT, fire);
  for (const q of queries) q.addEventListener?.('change', fire);
  return () => {
    win.removeEventListener(EVENT, fire);
    for (const q of queries) q.removeEventListener?.('change', fire);
  };
}

export const appearanceMessage = (doc = document) => ({ type: MESSAGE, ...appearance(doc) });

export function applyAppearanceMessage(data, doc = document) {
  if (!data || data.type !== MESSAGE) return false;
  return setAppearance({ theme: data.theme, density: data.density }, doc);
}

// ---- a device override (D188; the header above) ----
export const DEVICE_KEY = 'xbin-theme-device';
export const DEVICE_PARAM = 'xbin-appearance';
const PERSON = 'data-bx-theme-person';
const isScheme = (t) => t === 'light' || t === 'dark';

// effectiveTheme(person, device): the device's light or dark, else the
// person's choice, else the system's ('system').
export const effectiveTheme = (person, device) => (isScheme(device) ? device : THEMES.includes(person) ? person : 'system');

export function deviceTheme(win = globalThis.window) {
  try {
    const v = win?.localStorage?.getItem(DEVICE_KEY);
    return isScheme(v) ? v : '';
  } catch { return ''; /* storage off, or an opaque origin: no override */ }
}

export function personTheme(doc = document) {
  const p = doc.documentElement?.getAttribute(PERSON);
  return THEMES.includes(p) ? p : appearance(doc).theme;
}

export const personAppearance = (doc = document) => ({ ...appearance(doc), theme: personTheme(doc) });

// syncDeviceTheme(doc): make a document that follows the person show what
// this browser stores now — the override (the person's choice set aside on
// <html>), or, once it is cleared, the person's choice again. True when
// the page changed.
export function syncDeviceTheme(doc = document) {
  if (!follows(doc)) return false;
  const html = doc.documentElement, d = deviceTheme(doc.defaultView);
  let changed = false;
  if (d) {
    if (!html.hasAttribute(PERSON)) html.setAttribute(PERSON, appearance(doc).theme);
    changed = setAppearance({ theme: d }, doc);
  } else if (html.hasAttribute(PERSON)) {
    const p = personTheme(doc);
    html.removeAttribute(PERSON);
    changed = setAppearance({ theme: p }, doc);
  }
  rememberTheme(personTheme(doc), doc);
  return changed;
}

// setDeviceTheme(v, doc): this browser's override — 'light' or 'dark', or
// '' to follow the person again — stored and applied. False when the
// browser can't store it.
export function setDeviceTheme(v, doc = document) {
  try {
    const ls = doc.defaultView.localStorage;
    if (isScheme(v)) ls.setItem(DEVICE_KEY, v); else ls.removeItem(DEVICE_KEY);
  } catch { return false; }
  syncDeviceTheme(doc);
  return true;
}

// setPersonAppearance(next, doc): the person's choice changed (their pick,
// or another device's). Without an override, setAppearance; under one the
// page keeps the device's theme and the person's waits on <html>.
export function setPersonAppearance(next = {}, doc = document) {
  const html = doc.documentElement;
  if (!html?.hasAttribute(PERSON) || !deviceTheme(doc.defaultView)) return setAppearance(next, doc);
  let changed = false;
  if (THEMES.includes(next.theme) && next.theme !== personTheme(doc)) {
    html.setAttribute(PERSON, next.theme);
    changed = true;
  }
  return setAppearance({ density: next.density }, doc) || changed;
}

// deviceUrl(url, doc): a tile's URL, asking xbind for the device's theme
// while there is an override and the embedding document follows the person.
export function deviceUrl(url, doc = document) {
  const d = follows(doc) ? deviceTheme(doc.defaultView) : '';
  return d ? `${url}${url.includes('?') ? '&' : '?'}${DEVICE_PARAM}=${d}` : url;
}
