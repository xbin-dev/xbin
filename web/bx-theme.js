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

// rememberTheme keeps the hint cookie equal to the person's theme.
export function rememberTheme(theme, doc = document) {
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
