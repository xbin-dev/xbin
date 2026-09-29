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

// probeWords says what a check of a live page found — the web pane's fetch
// of its own URL (live-status.js) or the backend's probe (actions.ports,
// actions.probePort): {tone: ok | bad, text, hint}. The refusals it knows:
// the manager's (not-listening, state, unsupported — a sandbox agent from
// before ports), the live route's (not-attached, not-allowed; viewers get a
// bare 403) and xbind's path-ticket answers (401: an expired link or one
// used away from where you signed in; 403: the tile-origin check).
export function probeWords(p) {
  const type = String(p.contentType || '').split(';')[0];
  const head = p.status ? `HTTP ${p.status}${type ? ' · ' + type : ''}` : p.refusal || 'no answer';
  const ms = p.ms != null ? ` · ${p.ms} ms` : '';
  if (p.ok) return { tone: 'ok', text: head + ms, hint: '' };
  return { tone: 'bad', text: `${head}${ms}${p.error ? ' — ' + p.error : ''}`, hint: probeHint(p) };
}

function probeHint(p) {
  const e = String(p.error || '');
  switch (p.refusal) {
    case 'not-listening': return 'nothing listens on that port in the sandbox: its server isn\'t running (the agent starts it with bash, background:true)';
    case 'state': return 'the sandbox isn\'t running: start it (▣ → Manage…), then its server';
    case 'unsupported': return /predates|restart/.test(e)
      ? 'the sandbox was started before its runtime served ports: restart it (▣ → Manage…: Stop, then Start), then its server'
      : 'its manager doesn\'t serve ports (it, or xbind, predates live previews)';
    case 'not-attached': case 'not-found': case 'none': return 'that sandbox isn\'t bound to this conversation any more';
    case 'not-allowed': return 'whoever bound the sandbox may no longer use it here';
    case 'invalid': return 'the page path isn\'t one this route takes';
  }
  if (p.status === 401) return /expired|sign-in ended/.test(e) ? 'the link expired, or your sign-in did: ↻ Reload mints a new one'
    : 'the link works only from an address that signed in within the hour: sign in again here, then ↻ Reload';
  if (p.status === 403) return /origin/.test(e) ? 'the tile-origin check refused it: the link is another tile\'s'
    : 'only the people taking part in this conversation may open its sandbox\'s pages';
  if (p.status === 502 || p.status === 503 || p.status === 504) return 'the sandbox manager didn\'t answer: it, or the sandbox, may be stopped';
  if (p.status >= 400) return 'the server in the sandbox answered with an error: check the path';
  return 'no answer at all: the network, or xbind, is unreachable';
}
