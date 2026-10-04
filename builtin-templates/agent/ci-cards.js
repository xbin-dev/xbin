// ci-cards.js — CI's outcome cards at the end of the transcript on the web
// (ext.end; API.md §CI in the conversation): "CI passed on ‹branch›" / "CI
// failed on ‹branch› — test (ubuntu) › go test ./…", one per watch whose
// outcome you haven't dismissed (✕: kept in your prefs per <watch>:<outcome>,
// so the next outcome is a new card), from the conversation's CI read or any
// later `ci` event (app.ci.cards). Open logs opens the dock on that job's log
// (ci-dock.js openCI); a pass opens the dock on CI.
import { html, nothing } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { openCI } from './ci-dock.js';

ext.register({ end: () => cardsTpl() });

function cardsTpl() {
  const app = ctx.app;
  const v = app && app.session.current();
  if (!v || (v.run.status === 'waiting_input' && (v.run.pendingState || {}).harness)) return null; // a coding agent's park is its own (web-ext.js end)
  const root = v.run.rootId || v.run.id;
  const cards = app.ci.cards(root);
  if (!cards.length) return null;
  return html`<div class="cicards">${cards.map((c) => html`<div class="cicard-out" data-key=${c.key} data-tone=${c.tone} role="status">
    <span class="cimark" data-tone=${c.tone}>${c.tone === 'ok' ? '✓' : '✗'}</span>
    <span class="citext">${c.text}</span>
    <button class="lnk" data-act="logs" @click=${() => openCI(root, { job: c.job, watch: c.watch })}>${c.job ? 'Open logs' : 'Open CI'}</button>
    <button class="lnk" data-act="dismiss" title="dismiss" @click=${() => app.ci.dismiss(c.key)}>✕</button>
  </div>`)}</div>`;
}

const style = document.createElement('style');
style.textContent = `
  .cicards { display: flex; flex-direction: column; gap: 6px; margin: 8px 0; }
  .cicard-out { display: flex; align-items: baseline; gap: 8px; padding: 6px 10px; border: 1px solid var(--bx-border); border-left: 3px solid var(--bx-green);
    border-radius: 6px; background: var(--bx-panel); font-size: 12.5px; min-width: 0; }
  .cicard-out[data-tone="bad"] { border-left-color: var(--bx-red); }
  .cicard-out .cimark[data-tone="ok"] { color: var(--bx-green); } .cicard-out .cimark[data-tone="bad"] { color: var(--bx-red); }
  .cicard-out .citext { flex: 1; min-width: 0; overflow-wrap: anywhere; }
`;
document.head.append(style);
