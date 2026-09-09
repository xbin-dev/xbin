/**
 * bx-kit — the small helpers every shipped frontend used to re-implement:
 * an API call that throws on error, a JSON body, HTML escaping, the deep
 * active element across shadow roots, composed-path matching, and the
 * viewport clamp for floating windows. Import by absolute URL from any
 * document (sandboxed tiles included — /vendor/ is credential-less):
 *
 *   import { xbinApi, jbody, esc } from '/vendor/bx-kit.js';
 *
 * Zero dependencies; every export is a plain function. `make js-check`
 * refuses a second definition of these names elsewhere (docs/frontend-kit.md).
 */

// sandboxed(): is this document a sandboxed tile frame? xbind injects
// <meta name="xbin-sandbox"> into exactly those (internal/server/static.go).
export const sandboxed = () => typeof document !== 'undefined' && !!document.querySelector('meta[name="xbin-sandbox"]');

// api(url, opts): the request carries the credential the document HAS —
// in a sandboxed tile the frame token via the in-frame client (the tile's
// own identity; the opaque origin holds no cookie), in chrome (the shell,
// tiles/organisations, any `chrome: true` component) the session cookie, so
// the caller is the signed-in human. That is the identity split every
// hand-rolled helper encoded before the kit (a chrome tile calling through
// xbin.fetch would act as the tile, not the person — and lose its rights).
// Resolves the parsed JSON (or the text when the body is not JSON, null
// when empty); rejects with the server's `error` message on a non-2xx.
export async function api(url, opts) {
  const f = sandboxed() && window.xbin?.fetch ? window.xbin.fetch : fetch;
  const r = await f(url, opts);
  const text = await r.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { data = text; }
  if (!r.ok) throw new Error((data && typeof data === 'object' && data.error) || `error ${r.status}`);
  return data;
}

// xbinApi('/grants', opts): the built-in API under /api/xbin.
export const xbinApi = (path, opts) => api('/api/xbin' + path, opts);

// selfApi('/runs', opts): this component's own backend (the xbin global names it).
export const selfApi = (path, opts) => api(`/api/${window.xbin?.self ?? ''}` + path, opts);

// jbody(value, method?): a JSON request body (+ method) to spread into opts.
export const jbody = (body, method) => ({
  ...(method ? { method } : {}),
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

// esc(s): HTML-escape for text dropped into markup strings.
export const esc = (s) => String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

// deepActive(): the focused element, descending through open shadow roots.
export function deepActive() {
  let el = document.activeElement;
  while (el?.shadowRoot?.activeElement) el = el.shadowRoot.activeElement;
  return el;
}

// pathHas(event, selector): does any element on the event's composed path
// match? e.target is retargeted at shadow boundaries, so an <input> inside
// a nested element never matches a plain e.target.closest().
export const pathHas = (e, sel) => e.composedPath().some((n) => n instanceof Element && n.matches(sel));

// clampBox(box, {minW, minH, margin, defW, defH}): keep a viewport-fixed
// window reachable — never wider/taller than the viewport minus the margin,
// never off-screen; a missing size falls back to defW×defH.
export function clampBox(box, { minW = 200, minH = 140, margin = 8, defW = 560, defH = 320 } = {}) {
  const W = window.innerWidth, H = window.innerHeight;
  const n = (v, d) => (Number.isFinite(Number(v)) ? Number(v) : d);
  const w = Math.max(Math.min(minW, W - 2 * margin), Math.min(n(box?.w, defW), W - 2 * margin));
  const h = Math.max(Math.min(minH, H - 2 * margin), Math.min(n(box?.h, defH), H - 2 * margin));
  const x = Math.max(margin, Math.min(n(box?.x, margin), W - w - margin));
  const y = Math.max(margin, Math.min(n(box?.y, margin), H - h - margin));
  return { ...box, x, y, w, h };
}
