/**
 * shell-first-run.js — the shell's one hook for the first-run sheet (D237):
 * the sheet itself is xbind's own chrome (/vendor/bx-first-run.js); this
 * only asks whether to show it, mounts it, and keeps the shell's own
 * first-run hints out from under it — one thing at a time.
 *
 *   firstRun(shell)       for the bootstrap token's session (whoami kind
 *                         root): GET /api/xbin/first-run says whether this is
 *                         the bootstrap state; unless this browser said "Not
 *                         now", the sheet goes over the shell, which is inert
 *                         and holds its setup card and alerts (shell._firstRun)
 *                         until the sheet is done or skipped; never in a
 *                         framed shell (it says nothing there: the person
 *                         opens the workspace directly)
 *   afterFirstRun(shell)  the reload the sheet ends with, now as the new
 *                         account: when it turned the workspace agent on,
 *                         the dock opens and the agent's composer has the keys
 *
 * On an xbind without the sheet (no /vendor/first-run-words.js) neither
 * does anything.
 */
import { openAgent } from './shell-agent.js';

const words = () => import('/vendor/first-run-words.js').catch(() => null);

// framed — the shell inside another page (a same-site tile origin can frame
// it): the sheet asks for a password, so it is offered top-level only
const framed = () => { try { return window.top !== window.self; } catch { return true; } };

export async function firstRun(shell) {
  if (shell._firstRun || !shell._who || shell._who.kind !== 'root' || framed()) return;
  const w = await words();
  if (!w) return;
  try { if (localStorage.getItem(w.SKIP_KEY)) return; } catch { /* no storage: ask */ }
  const st = await fetch('/api/xbin/first-run', { credentials: 'same-origin' }).then((r) => (r.ok ? r.json() : null)).catch(() => null);
  if (!st || !st.bootstrap || shell._firstRun) return;
  shell._firstRun = true; // (claimed before the import: a second probe meanwhile mounts nothing)
  try { await import('/vendor/bx-first-run.js'); } catch { shell._firstRun = false; return; }
  shell.inert = true; // the sheet is modal: nothing behind it takes a click or the keys
  shell.requestUpdate();
  const el = document.createElement('bx-first-run');
  el.addEventListener('bx-first-run-skip', () => {
    shell._firstRun = false;
    shell.inert = false;
    shell.requestUpdate();
    shell._probeAdmin(); // everything as it was: the setup card comes back
  });
  document.body.append(el);
}

export async function afterFirstRun(shell) {
  const w = await words();
  if (!w) return;
  let v = null;
  try { v = sessionStorage.getItem(w.DONE_KEY); sessionStorage.removeItem(w.DONE_KEY); } catch { return; }
  if (v !== 'agent') return;
  // the chip's words and the state first (shell-agent.js), then the dock, then the keys
  for (let i = 0; i < 100 && !(shell._agent && shell._agent.words && shell._agent.state); i++) await new Promise((r) => setTimeout(r, 100));
  if (!shell._agent || !shell._agent.state) return;
  await openAgent(shell, null, true);
  await shell.updateComplete;
  const dock = shell.renderRoot.querySelector('bx-agent-dock');
  if (dock && typeof dock.focusComposer === 'function') dock.focusComposer();
}
