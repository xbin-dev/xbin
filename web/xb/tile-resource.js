/**
 * xb/tile-resource.js — the app's confinement of what a native tile hands
 * over (XbinCore TileResources.swift, D103), for the browser that plays the
 * app (xb/preview-host.js, `bx preview --native`): an image `src`, a shared
 * file, a composer's upload target resolves only to the tile's OWN paths —
 * `/c/<self>/…` and `/api/<self>/…` — never another tile's, xbind's own API
 * (`/api/xbin/…`), a traversal (`..`, `%2e%2e`, an encoded slash), a path a
 * nested tile owns, or anything with a scheme or a host; anything else is
 * null. The same inputs give the same answers as the Swift (hack/
 * xb-tile-resource.test.mjs replays TileResourceTests' vectors).
 *
 *   apiPath(ref, tile, known?, name?)  an upload target: relative, or `/x`
 *                                      not under /api/, is under /api/<self>/;
 *                                      `{name}` → name, one encoded component
 *   assetPath(ref, tile, known?)       an image or file: relative is under
 *                                      /c/<self>/; /c/<self>/… or /api/<self>/…
 *   pagePath(ref, tile, known?)        a canvas page: /c/<self>/… (+ #fragment)
 *   uploadMethod(m)                    PUT (default), POST, PATCH — else null
 *
 * `known` is the workspace's tile paths (GET /api/xbin/components).
 */

const PATH_SAFE = new Set([...'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~!$&\'()*+,;=:@'].map((c) => c.charCodeAt(0)));
const QUERY_SAFE = new Set([...PATH_SAFE, '/'.charCodeAt(0), '?'.charCodeAt(0)]);
const PCT = '%'.charCodeAt(0);
const enc = new TextEncoder();

// a control character (or DEL)
const ctl = (s) => /[\u0000-\u001f\u007f]/.test(s);

// strict percent-decoding: null on a malformed escape or invalid UTF-8
function decode(seg) {
  try { return decodeURIComponent(seg); } catch { return null; }
}

// a `:` before any `/`, `?` or `#`: a scheme (https:, javascript:, data:)
function hasScheme(s) {
  const m = /[:/?#]/.exec(s);
  return !!m && m[0] === ':';
}

// a component as a server sees it: escapes kept, anything else that may not
// appear raw (spaces, non-ASCII, quotes) percent-encoded
function canonical(s, keep = PATH_SAFE) {
  let out = '';
  for (const b of enc.encode(s)) out += keep.has(b) || b === PCT ? String.fromCharCode(b) : `%${b.toString(16).toUpperCase().padStart(2, '0')}`;
  return out;
}

// the query, canonical, without any `frame` parameter (the host adds the
// tile's own token; a tile never names one)
function canonicalQuery(q) {
  return q.split('&').filter((p) => p !== '').filter((p) => {
    const key = p.split('=')[0];
    const k = decode(key.replaceAll('+', ' ')) ?? key;
    return k !== 'frame';
  }).map((p) => canonical(p, QUERY_SAFE)).join('&');
}

const trim = (s) => s.replace(/^[\t\p{Zs}]+|[\t\p{Zs}]+$/gu, '');

// relativeTo: 'api' | 'c' — the base of a relative ref; allowed: the bases an
// absolute ref may name; rootRelative: where `/x` (not under an allowed
// base) goes, or null (refused)
function resolve(ref, tile, known, relativeTo, allowed, rootRelative) {
  const tileSegs = String(tile ?? '').split('/');
  // `/api/xbin/…` is xbind's own API, whatever follows
  if (!tile || tileSegs.some((x) => x === '' || x === '.' || x === '..') || tileSegs[0] === 'xbin') return null;
  const s = trim(String(ref ?? ''));
  if (!s || ctl(s) || s.includes('\\') || hasScheme(s) || s.startsWith('//')) return null;

  let rest = s;
  let fragment = null;
  const h = rest.indexOf('#');
  if (h >= 0) { fragment = rest.slice(h + 1); rest = rest.slice(0, h); }
  let query = null;
  const q = rest.indexOf('?');
  if (q >= 0) { query = rest.slice(q + 1); rest = rest.slice(0, q); }

  // segments of the whole server path, still encoded
  const encodedTile = tileSegs.map((x) => encodeURIComponent(x));
  let raw;
  if (rest.startsWith('/')) {
    raw = rest.slice(1).split('/');
    const head = decode(raw[0]);
    const base = head === 'api' ? 'api' : head === 'c' ? 'c' : null;
    if (base && allowed.includes(base)) {
      // an absolute path: checked below to be the tile's own
    } else if (rootRelative && base !== 'api') {
      raw = [rootRelative, ...encodedTile, ...raw]; // `/x` — under the tile's API
    } else return null;
  } else {
    let r = rest;
    while (r.startsWith('./')) r = r.slice(2);
    if (r === '.') r = '';
    raw = [relativeTo, ...encodedTile, ...(r === '' ? [''] : r.split('/'))];
  }

  // every segment decodes to a plain name: no traversal, no encoded
  // separators; only the last may be empty (a trailing slash)
  const decoded = [];
  for (let i = 0; i < raw.length; i++) {
    const d = decode(raw[i]);
    if (d === null || d === '.' || d === '..' || d.includes('/') || d.includes('\\') || ctl(d)) return null;
    if (d === '' && i !== raw.length - 1) return null;
    decoded.push(d);
  }
  // the tile's own, and no longer known tile under it claims the rest
  if (decoded.length < 1 + tileSegs.length || (decoded[0] !== 'api' && decoded[0] !== 'c')) return null;
  for (let i = 0; i < tileSegs.length; i++) if (decoded[1 + i] !== tileSegs[i]) return null;
  const after = decoded.slice(1 + tileSegs.length).filter((x) => x !== '');
  for (const k of known ?? []) {
    if (!k.startsWith(`${tile}/`)) continue;
    const extra = k.split('/').filter((x) => x !== '').slice(tileSegs.length);
    if (extra.length && after.length >= extra.length && extra.every((x, i) => after[i] === x)) return null;
  }

  let path = `/${raw.map((x) => canonical(x)).join('/')}`;
  if (query !== null) {
    const cq = canonicalQuery(query);
    if (cq) path += `?${cq}`;
  }
  return { path, fragment: fragment === null ? null : canonical(fragment, QUERY_SAFE) };
}

export function apiPath(ref, tile, known = [], name = null) {
  const r = name === null ? String(ref ?? '') : String(ref ?? '').split('{name}').join(encodeURIComponent(name));
  return resolve(r, tile, known, 'api', ['api'], 'api')?.path ?? null;
}

export function assetPath(ref, tile, known = []) {
  return resolve(ref, tile, known, 'c', ['c', 'api'], null)?.path ?? null;
}

export function pagePath(ref, tile, known = []) {
  const r = resolve(ref, tile, known, 'c', ['c'], null);
  return r ? (r.fragment === null ? r.path : `${r.path}#${r.fragment}`) : null;
}

export function uploadMethod(m) {
  const u = String(m ?? '').replace(/^[\t\p{Zs}]+|[\t\p{Zs}]+$/gu, '').toUpperCase();
  if (!u) return 'PUT';
  return ['PUT', 'POST', 'PATCH'].includes(u) ? u : null;
}
