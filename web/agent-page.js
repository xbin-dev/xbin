/**
 * agent-page.js — /xbin/agent (web/agent.html, D232): xbin's chrome for the
 * person's workspace agent as a page of its own — the dock, full width —
 * for a workspace whose shell has no chip for it, and for direct links:
 *
 *   /xbin/agent                the dock
 *   /xbin/agent?view=approvals | activity | prefs | needs
 *   /xbin/agent?request=<id>   the approval of one request
 *   /xbin/agent?changes=<id>   the review of one change set (where signing
 *                              in again for it comes back to)
 *
 * The title carries what waits ("(2) Workspace agent — xbin").
 */
import '/vendor/bx-agent-dock.js';
import { agentStore } from '/vendor/agent-chrome-store.js';
import { needs } from '/vendor/agent-chrome-words.js';

const dock = document.querySelector('bx-agent-dock');
const q = new URLSearchParams(location.search);
const ID = /^[0-9a-f]{24}$/; // a request's or change set's id, as xbind mints them (agent-chrome-words.js pageMessage)
const store = agentStore();
store.subscribe(() => {
  const n = needs(store).length;
  document.title = `${n ? `(${n}) ` : ''}Workspace agent — xbin`;
});
// a waiting tile agent: this page has no canvas, so it is the link to the
// tile and its session — followed only on a click (the tray's row, or one
// inside the agent's page): a page can't navigate the workspace by itself
dock.addEventListener('bx-agent-open-session', (e) => {
  const { tile, session } = e.detail || {};
  if (!tile || !session || !navigator.userActivation?.isActive) return;
  location.assign(`/?tile=${encodeURIComponent(tile).replace(/%2F/gi, '/')}&session=${encodeURIComponent(session)}`);
});
customElements.whenDefined('bx-agent-dock').then(() => {
  const view = q.get('view');
  if (view) dock.show(view);
  const req = q.get('request'), cs = q.get('changes');
  if (req && ID.test(req)) dock.review('request', req);
  else if (cs && ID.test(cs)) dock.review('changeset', cs);
});
