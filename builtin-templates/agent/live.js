// live.js — the live preview (D135): a page a program in the conversation's
// sandbox serves (preview_port's `live` step), shown in the render pane.
//
// Unlike render_html's static srcdoc, this page RUNS: it is the sandbox's,
// untrusted, so its frame is sandbox="allow-scripts allow-forms" — never
// allow-same-origin, so it is an opaque origin with no cookies, no storage
// and no reach into this tile, xbind or another tile — and the backend's
// answers carry a CSP sandbox header of the same tokens, so a direct open of
// its URL is confined too. It loads from this tile's backend
// (/runs/{id}/live/{sandbox}/{port}/…) below an xbind path ticket
// (model/live.js); only the run's participants get past the backend.
// test/live-policy.mjs drives a hostile page through it in Chromium.
import { liveURL, liveLabel } from './model/live.js';

export { liveURL, liveLabel };

export const LIVE_SANDBOX = 'allow-scripts allow-forms';

// liveFrame is the live page's iframe: its sandbox set before its src, so
// the first load is already confined.
export function liveFrame(src) {
  const f = document.createElement('iframe');
  f.setAttribute('sandbox', LIVE_SANDBOX);
  f.setAttribute('referrerpolicy', 'no-referrer');
  f.setAttribute('credentialless', '');
  f.setAttribute('allow', '');
  f.className = 'prevframe livefr';
  f.id = 'livefr';
  f.title = 'live from the sandbox — its scripts run, isolated from this workspace';
  f.src = src;
  return f;
}
