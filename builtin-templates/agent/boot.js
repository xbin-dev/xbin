// boot.js — the page's entry: is this xbind new enough for the page, then
// agent.js (or, for a workspace agent, ws/page.js: below).
//
// The chat is drawn with xbind's chat kit (/vendor/chat/…, D224), and an
// instance of this template is a copy in a workspace: it can be newer than
// the xbind it runs on (a template merged in before xbind was updated). A
// module importing something an older xbind doesn't serve would fail to load
// and leave an empty page, so the kit says its revision
// (/vendor/chat/rev.js: KIT, which only grows) and this file asks first:
//
//   - the revision is there and at least NEEDS.kit: agent.js is loaded, as
//     index.html's own <script> did before. A failure in agent.js is
//     agent.js's — it is reported as an uncaught error, never as "needs
//     xbind";
//   - it is missing (an xbind from before the kit) or lower: the page says
//     which xbind it needs, and nothing else is loaded.
//
// NEEDS: raise `kit` when the page starts to use something a later kit
// revision brought (rev.js lists them), and `xbind` to the first release
// that serves it. Revision 2: the models say a coding agent's calls and
// requests with the kit's /vendor/chat/tools.js and /vendor/chat/ask.js
// (model/harness-heads.js, model/harness-ask.js). Revision 4: the chat is
// drawn with the kit's views (chat-kit.js: /vendor/chat/views.js, css.js),
// as its second host — its own tools' words on the kit's card, its hook
// classes on the kit's rows.
//
// The native view (native.js) imports those models too, and a module can't
// ask first: on an older xbind its import of the kit is a 404, native.js
// fails to load, and the app shows this page instead (/docs/native.md →
// Fallback) — which says which xbind it needs.
//
// Workspace mode (API.md §Workspace mode; D231, D235): the same backend is a
// person's workspace agent when xbind starts it so, and then its page is
// another one — ws/page.js, a small tree of its own over the same models —
// in place of agent.js, whose routes that backend doesn't serve. A
// workspace agent is a sealed component, and those live at xbin/<name>, a
// path no workspace can hold (docs/protocol/sealed.md): only there is the
// backend asked — once the kit is known to be there — GET /ws/state, which
// answers its mode. A normal instance is asked nothing more than before.
// The workspace agent's page needs `ws` (revision 5: a plan and a question
// in the host's words); it ships with xbind, so the two always match.
export const NEEDS = Object.freeze({ kit: 4, ws: 5, xbind: 'v0.3.80' });

// What xbin's chrome said before the page listened: the dock tells a workspace agent's page what it was opened about
// (xbin:agent-context) once, when its frame has loaded — and ws/page.js is asked for only after /ws/state answered, later
// than that. The last such message from the framing window is kept here; ws/dock.js listen() takes it first.
if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
  window.addEventListener('message', (e) => { if (e.source === window.parent && e.data && e.data.type === 'xbin:agent-context') globalThis.wsEarlyContext = e.data; });
}

// pageMode(fetch, self): 'workspace' | 'normal' — model/mode.js, which the native view (native.js) asks the same way.
export { pageMode } from './model/mode.js';
import { pageMode } from './model/mode.js';

// kitRevision(): the KIT this xbind serves; 0 when it serves none.
export async function kitRevision(load = () => import('/vendor/chat/rev.js')) {
  try {
    const n = Number((await load()).KIT);
    return Number.isFinite(n) && n > 0 ? n : 0;
  } catch { return 0; }
}

// needsXbind(doc, kit): the page, in place of the app.
export function needsXbind(doc, kit, need = NEEDS.kit) {
  // the docs are the workspace's: a tile on its own origin links there
  const ws = doc.querySelector('meta[name="xbin-workspace-origin"]')?.content || '';
  const box = doc.createElement('div');
  box.className = 'needs';
  box.id = 'needs-xbind';
  box.setAttribute('role', 'alert');
  box.dataset.kit = String(kit);
  const head = doc.createElement('b');
  head.textContent = `This agent needs xbind ≥ ${NEEDS.xbind} — update xbind`;
  const why = doc.createElement('p');
  why.append(kit
    ? `This workspace's xbind serves revision ${kit} of the chat kit this page is built on; the page needs revision ${need}. `
    : 'This workspace runs an xbind from before the chat kit this page is built on. ',
  'Nothing is lost: update xbind, then reload this page. ');
  const how = doc.createElement('a');
  how.href = `${ws}/docs/install.md`;
  how.target = '_blank';
  how.rel = 'noopener noreferrer';
  how.textContent = 'How to update';
  why.append(how);
  box.append(head, why);
  doc.body.replaceChildren(box);
}

async function boot() {
  const kit = await kitRevision();
  if (kit < NEEDS.kit) {
    console.warn(`[agent] this page needs xbind ≥ ${NEEDS.xbind} (chat kit revision ${NEEDS.kit}; this xbind serves ${kit || 'none'})`);
    needsXbind(document, kit);
    return;
  }
  // a workspace agent's page (ws/page.js), or the normal one; their own failure is said the way a failing
  // <script type="module"> says it
  const ws = (await pageMode()) === 'workspace';
  if (ws && kit < NEEDS.ws) {
    console.warn(`[agent] the workspace agent's page needs chat kit revision ${NEEDS.ws}; this xbind serves ${kit}`);
    needsXbind(document, kit, NEEDS.ws);
    return;
  }
  const fail = (e) => { if (typeof reportError === 'function') reportError(e); else setTimeout(() => { throw e; }); };
  if (ws) import('./ws/page.js').catch(fail);
  else import('./agent.js').catch(fail);
}
if (typeof document !== 'undefined') boot();
