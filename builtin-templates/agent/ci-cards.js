// ci-cards.js — CI's outcome cards at the end of the transcript on the web
// (ext.end; API.md §CI in the conversation): "CI passed on ‹branch›" / "CI
// failed on ‹branch› — test (ubuntu) › go test ./…", one per watch whose
// outcome you haven't dismissed (its close glyph: kept in your prefs per <watch>:<outcome>,
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
    <span class="cimark" data-tone=${c.tone}><bx-icon name=${c.tone === 'ok' ? 'ok' : 'error'}></bx-icon></span>
    <span class="citext">${c.text}</span>
    <button class="lnk" data-act="logs" @click=${() => openCI(root, { job: c.job, watch: c.watch })}>${c.job ? 'Open logs' : 'Open CI'}</button>
    <button class="lnk" data-act="dismiss" title="dismiss" aria-label="dismiss" @click=${() => app.ci.dismiss(c.key)}><bx-icon name="xmark"></bx-icon></button>
  </div>`)}</div>`;
}

const style = document.createElement('style');
style.textContent = `
  .cicards { display: flex; flex-direction: column; gap: 6px; margin: 8px 0; }
  .cicard-out { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border: 1px solid var(--bx-ok); background: var(--bx-ok-bg);
    border-radius: var(--bx-radius); min-width: 0; }
  .cicard-out[data-tone="bad"] { border-color: var(--bx-danger); background: var(--bx-danger-bg); }
  .cicard-out .cimark { display: inline-flex; flex: none; }
  .cicard-out .cimark[data-tone="ok"] { color: var(--bx-ok); } .cicard-out .cimark[data-tone="bad"] { color: var(--bx-danger); }
  .cicard-out .citext { flex: 1; min-width: 0; overflow-wrap: anywhere; }
`;
document.head.append(style);
