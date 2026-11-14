// project-needs.js — "Waiting for you" atop a project's page on the web
// (#56; API.md §Projects in the UI): what waits on you in it — a task's
// question, approval or sign-in (red), a failure (yellow), your
// coordinator's own question — each with who asks, what, how long ago and
// Open, which opens the conversation it waits in. Drawn only while
// something waits; the words are model/project-needs.js's.
//
//   needsStripTpl(app, pid)   the strip (#pneeds), or nothing
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { projectNeeds, needRows } from './model/project-needs.js';

/** needsStripTpl(app, pid): the strip, read the first time it is drawn. */
export function needsStripTpl(app, pid) {
  const it = projectNeeds(app).ensure(pid);
  const rows = needRows(it.items);
  if (!rows.length) return nothing;
  const red = rows.some((r) => r.tone === 'bad');
  return html`<section class="pneeds" id="pneeds" data-tone=${red ? 'bad' : 'warn'} role="region" aria-label="Waiting for you">
    <h4><bx-icon name=${red ? 'question' : 'warning'}></bx-icon><span>Waiting for you</span><span class="pncount">${rows.length}</span></h4>
    ${repeat(rows, (r) => r.key, (r) => html`<div class="pneed" data-tone=${r.tone} data-reason=${r.reason} data-run=${r.run}>
      <bx-icon name=${r.tone === 'bad' ? 'question' : 'warning'}></bx-icon>
      <div class="pnbody">
        <div class="pnhead"><b class="pnwho ${r.n ? 'mono' : ''}">${r.who}</b>${r.title ? html`<span class="pntitle">${r.title}</span>` : nothing}<span class="pnwhy">${r.words}</span>
          ${r.age ? html`<span class="pnage muted">${r.age}</span>` : nothing}</div>
        ${r.text ? html`<div class="pntext">${r.text}</div>` : nothing}
      </div>
      <button class="btn btnsm pnopen" title="open the conversation it waits in" @click=${() => app.select(r.run)}>Open</button>
    </div>`)}
  </section>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pneeds { border: 1px solid var(--bx-warn); background: var(--bx-warn-bg); border-radius: var(--bx-radius); padding: 8px 10px; margin: 0 0 10px; }
  .projs-page .pneeds[data-tone="bad"] { border-color: var(--bx-danger); background: var(--bx-danger-bg); }
  .projs-page .pneeds h4 { display: flex; align-items: center; gap: 6px; margin: 0 0 6px; font: var(--bx-font-title); color: var(--bx-warn); }
  .projs-page .pneeds[data-tone="bad"] h4 { color: var(--bx-danger); }
  .projs-page .pneeds .pncount { font: var(--bx-font-meta); border: 1px solid currentColor; border-radius: var(--bx-radius); padding: 0 5px; }
  .projs-page .pneed { display: flex; align-items: flex-start; gap: 8px; padding: 6px 8px; margin-top: 4px; background: var(--bx-panel);
    border-left: 3px solid var(--bx-warn); border-radius: var(--bx-radius); }
  .projs-page .pneed[data-tone="bad"] { border-left-color: var(--bx-danger); }
  .projs-page .pneed > bx-icon { flex: none; margin-top: 2px; color: var(--bx-warn); }
  .projs-page .pneed[data-tone="bad"] > bx-icon { color: var(--bx-danger); }
  .projs-page .pnbody { flex: 1; min-width: 0; }
  .projs-page .pnhead { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; }
  .projs-page .pnwho.mono { font-family: var(--bx-mono); }
  .projs-page .pntitle { font-weight: 600; overflow-wrap: anywhere; }
  .projs-page .pnwhy { color: var(--bx-muted); }
  .projs-page .pnage { font: var(--bx-font-meta); }
  .projs-page .pntext { margin-top: 2px; overflow-wrap: anywhere; white-space: pre-wrap; }
  .projs-page .pnopen { flex: none; align-self: center; }
`;
document.head.append(style);
