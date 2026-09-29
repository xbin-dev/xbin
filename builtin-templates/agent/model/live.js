// model/live.js — the live preview's shared half (D135; live.js draws the
// web's frame, native/tools.js the app's canvas island): the URL of a
// preview_port step's page, below an xbind path ticket (docs/auth.md §Path
// tickets) — the frame carries no token or cookie, so the ticket rides in
// the URL, and the page's relative loads resolve below it. The ticket
// reaches only /runs/{id}/live/{sandbox}/{port}/ of this tile's backend,
// which serves the run's participants only.

// liveURL mints a path ticket for the step's prefix and answers the page's
// URL below it. An xbind from before path tickets answers 404/405.
export async function liveURL(runId, det) {
  const prefix = `runs/${Number(runId)}/live/${det.sandbox}/${Number(det.port)}`;
  const r = await xbin.fetch('/api/xbin/path-tickets', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path: prefix }),
  });
  if (r.status === 404 || r.status === 405) throw new Error('this workspace\'s xbind is too old for live previews');
  if (!r.ok) {
    let msg = `HTTP ${r.status}`;
    try { msg = (await r.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  const { url } = await r.json();
  return url + String(det.path || '/').replace(/^\/+/, '');
}

// liveLabel is the pane's header for a live step.
export const liveLabel = (det) => `● live from the sandbox — ${det.name || det.sandbox}:${det.port}${det.path || '/'}`;
