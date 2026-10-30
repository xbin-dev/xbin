// model/preview-zoom.js — the render preview's zoom (#49, D207), both views'
// words for it: the steps − and + walk, the label, and where it is kept —
// per person and tile, by the platform's prefs API (/api/xbin/prefs/
// preview-zoom), since a tile frame has no localStorage. It scales the
// report as it is: the page is not reloaded, so the frame keeps its scroll.

/** ZOOMS: the steps, as fractions of the report's own size. */
export const ZOOMS = [0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];

/** clampZoom(z): a valid zoom (1 for anything else). */
export function clampZoom(z) {
  const n = Number(z);
  if (!Number.isFinite(n) || n <= 0) return 1;
  return Math.min(ZOOMS[ZOOMS.length - 1], Math.max(ZOOMS[0], n));
}

/** stepZoom(z, dir): the next step in (+1) or out (-1) from z. */
export function stepZoom(z, dir) {
  const cur = clampZoom(z);
  if (dir > 0) return ZOOMS.find((s) => s > cur + 1e-6) ?? ZOOMS[ZOOMS.length - 1];
  return [...ZOOMS].reverse().find((s) => s < cur - 1e-6) ?? ZOOMS[0];
}

/** zoomLabel(z): "125%". */
export const zoomLabel = (z) => `${Math.round(clampZoom(z) * 100)}%`;

const PREF = '/api/xbin/prefs/preview-zoom';

/** loadZoom(): the zoom kept for this person and tile (1 when none, or unreadable). */
export async function loadZoom() {
  try {
    const r = await xbin.fetch(PREF);
    return r.ok ? clampZoom(await r.json()) : 1;
  } catch { return 1; }
}

/** saveZoom(z): keep it (best effort: a zoom that isn't kept is no error). */
export function saveZoom(z) {
  try {
    return Promise.resolve(xbin.fetch(PREF, { method: 'PUT', body: JSON.stringify(clampZoom(z)) })).catch(() => {});
  } catch { return Promise.resolve(); }
}
