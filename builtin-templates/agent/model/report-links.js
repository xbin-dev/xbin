// model/report-links.js — downloads of session files (D207), the shared half
// of both views: a report's links to its files (#46), one file's download
// route and the zip of several (#47).
//
// A render step's detail carries links: [{href, path}] — every link of the
// report (render_html) or of a document's preview (doc_write, slides_write)
// that names a file, as written (href) and as the session file it is
// (path); the backend copies a sandbox report's linked files in beside it
// (API.md "Session files + render"). The views turn exactly those links
// into downloads of GET /runs/{id}/dl/<path>; every other link stays inert.
// The web's preview frame reaches that route below an xbind path ticket
// (docs/auth.md §Path tickets), as the live preview does (model/live.js):
// the frame is sandboxed and carries no token or cookie.
import { homeOf } from './homes.js';

/** stepLinks(steps, path): the links of the newest render step showing path ([] when none). */
export function stepLinks(steps, path) {
  for (let i = (steps || []).length - 1; i >= 0; i--) {
    const s = steps[i];
    if (!s || s.kind !== 'render') continue;
    let d = null;
    try { d = JSON.parse(s.detail); } catch { continue; }
    if (d && d.path === path) return linksOf(d);
  }
  return [];
}

/** linksOf(detail): a render step detail's links, the well-formed ones. */
export const linksOf = (d) => (Array.isArray(d && d.links) ? d.links : [])
  .filter((l) => l && typeof l.href === 'string' && typeof l.path === 'string' && l.path);

/** encPath: a session file key as URL path segments. */
export const encPath = (p) => String(p).split('/').map(encodeURIComponent).join('/');

/** dlPath(runId, path): the route that downloads one session file, below this backend's prefix. */
export const dlPath = (runId, path) => `/runs/${Number(runId)}/dl/${encPath(path)}`;

/** zipPath(runId, paths, name?): the route that zips several (paths empty: every file of the run). */
export function zipPath(runId, paths, name) {
  const q = new URLSearchParams();
  for (const p of paths || []) q.append('path', p);
  if (name) q.set('name', name);
  const s = q.toString();
  return `/runs/${Number(runId)}/zip${s ? '?' + s : ''}`;
}

/** zipName(runId): what a zip of a run's files is called. */
export const zipName = (runId) => `run-${Number(runId)}-files.zip`;

// --- the web preview's links (a path ticket) -----------------------------------------------------

const tickets = new Map(); // run id → {url, expires} — one ticket covers all of a run's downloads

/** dropTicket(runId): forget a run's ticket (a link that failed mints a new one next time). */
export const dropTicket = (runId) => tickets.delete(Number(runId));

// ticketURL mints (or reuses, until 10 minutes before it expires) a path
// ticket for runs/{id}/dl — an absolute URL, so it resolves the same in the
// report's srcdoc frame. An xbind from before path tickets answers 404/405.
async function ticketURL(runId) {
  const id = Number(runId);
  const t = tickets.get(id);
  if (t && t.expires - Date.now() > 10 * 60e3) return t.url;
  const r = await xbin.fetch('/api/xbin/path-tickets', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path: `runs/${id}/dl` }),
  });
  if (r.status === 404 || r.status === 405) throw new Error('this workspace\'s xbind is too old for downloads from the preview');
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try { msg = (await r.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  const { url, expires } = await r.json();
  const abs = new URL(url, globalThis.location?.href).href;
  tickets.set(id, { url: abs, expires: Number(expires) || Date.now() + 3600e3 });
  return abs;
}

/**
 * frameLinks(runId, links) → {map, note}: what the web's frameDoc keeps of a
 * report's links — href (trimmed) → its download URL — or null, with why in
 * note: a shared conversation in a person's partition lives at the global
 * instance, which a ticket of this page's partition doesn't reach (Files
 * downloads them there); an xbind too old, or a refusal. Never throws.
 */
export async function frameLinks(runId, links) {
  if (!links || !links.length) return { map: null, note: '' };
  const files = new Set(links.map((l) => l.path)).size;
  const words = `${files} linked file${files > 1 ? 's' : ''}`;
  if (homeOf(runId) === 'global') return { map: null, note: `${words}: download from Files` };
  try {
    const base = await ticketURL(runId);
    const map = new Map();
    for (const l of links) if (!map.has(l.href.trim())) map.set(l.href.trim(), base + encPath(l.path));
    return { map, note: '' };
  } catch (e) {
    return { map: null, note: `${words} not downloadable here: ${e.message || e}` };
  }
}
