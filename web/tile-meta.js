// tile-meta.js (/vendor/tile-meta.js) — a tile's title, icon and badge (D194). Pure, so
// hack/tile-meta.test.mjs runs it under node.
//
// Two sources, the second winning:
//   - its manifest: xbin.json `title` and `icon` (the /components row's
//     `title`/`icon`), for everyone who sees the tile;
//   - its page: xbin.meta({title, icon, badge}) (the `xbin:meta` message),
//     for this viewer while that window is open — a document title that
//     follows what the tile shows.
// A tile names itself, so the words are bounded (the xbin app's caps: a
// title 80 characters, an icon name 40, a badge 8), plain text only, and
// the shell keeps the tile's path beside the title everywhere it shows it:
// a tile can't pass itself off as another (or as the shell).

export const CAPS = Object.freeze({ title: 80, icon: 40, badge: 8 });
const ICON_RE = /^[a-z]+(-[a-z]+)*$/;
// eslint-disable-next-line no-control-regex
const clean = (v, n) => String(v).replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').replace(/[\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g, '')
  .replace(/\s+/g, ' ').trim().slice(0, n);

// cleanMeta(m) → the fields m sets, cleaned: {title?, icon?, badge?}, a
// field null where m clears it, absent where m leaves it alone. An icon
// that isn't an icon name (lowercase words joined by '-') is dropped.
export function cleanMeta(m) {
  const out = {};
  if (!m || typeof m !== 'object') return out;
  for (const k of ['title', 'icon', 'badge']) {
    if (!(k in m)) continue;
    const v = m[k];
    if (v == null || v === '') { out[k] = null; continue; }
    if (typeof v !== 'string' && typeof v !== 'number') continue;
    const s = clean(v, CAPS[k]);
    if (k === 'icon' && !ICON_RE.test(s)) continue;
    out[k] = s || null;
  }
  return out;
}

// mergeMeta(prev, m) → prev with m's fields applied (null clears one).
export function mergeMeta(prev, m) {
  const next = { ...(prev ?? {}) };
  for (const [k, v] of Object.entries(cleanMeta(m))) { if (v == null) delete next[k]; else next[k] = v; }
  return next;
}

// nameOf(path) → the path's last segment, the name a tile without a title shows.
export const nameOf = (path) => String(path ?? '').slice(String(path ?? '').lastIndexOf('/') + 1);

// titleOf(path, row, meta) → what to call the tile: its page's title, else
// its manifest's, else its name.
export const titleOf = (path, row, meta) => meta?.title || cleanMeta({ title: row?.title }).title || nameOf(path);

// iconOf(row, meta, known) → the icon name to draw, or '' (known(name):
// whether the workspace's icon set has it).
export function iconOf(row, meta, known = () => true) {
  const n = meta?.icon || cleanMeta({ icon: row?.icon }).icon || '';
  return n && known(n) ? n : '';
}
