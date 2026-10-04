// project-feed.js — a project's coordinator and its event feed on the web
// (API.md §Projects in the UI), drawn in a project's page (projects.js):
//
//   coordCardTpl(p, pv)  the coordinator card above the board: what it is,
//                        Open (your coordinator, made on first use — its
//                        conversation opens) and a line to write to it
//                        (POST /projects/{pid}/coordinator {text})
//   feedTpl(p, pv)       the Activity tab: the project's events, newest
//                        first — tasks made and finished, workspaces,
//                        pull requests, CI, reviews and comments, merges —
//                        each with its task (a link to its conversation
//                        when the board holds it) and its ↗ link
//
// The state is model/project-feed.js (projectFeed(app)). Event text comes
// partly from the scm provider (anyone may have written a comment or an
// issue): drawn as plain text, clipped, never markdown or HTML.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { ctx } from './web-ext.js';
import { projectFeed, feedWords } from './model/project-feed.js';
import { can } from './model/projects.js';

const feed = () => projectFeed(ctx.app);
const ago = (ms) => (ms ? new Date(ms).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' }) : '');

/** coordCardTpl(p, pv): the coordinator card (participants of a project with tasks). */
export function coordCardTpl(p, pv) {
  if (!pv || pv.kind === 'team' || !can(pv).act) return nothing;
  const f = feed();
  const c = f.coord(pv.id);
  if (c.missing) return nothing;
  const open = async () => { const run = await f.openCoordinator(pv.id); if (run && run.id) ctx.app.select(run.id); };
  const send = () => f.messageCoordinator(pv.id, c.text);
  return html`<div class="pcoord" id="pcoord">
    <div class="pcoordh"><b>Coordinator</b>
      <span class="muted small">your agent for this project: it makes tasks, follows them and tells you what needs you — it never merges or answers a task's question for you</span>
      <span style="flex:1"></span>
      <button class="btn ghost btnsm" id="pcoord-open" ?disabled=${!!c.busy} @click=${open}>${c.busy === 'open' ? 'Opening…' : 'Open'}</button></div>
    <div class="pcoordrow">
      <input id="pcoord-text" placeholder="Ask it something: make tasks for the open bugs, tell me what failed…" .value=${c.text}
        @input=${(e) => { c.text = e.target.value; }} @keydown=${(e) => { if (e.key === 'Enter') send(); }}>
      <button class="btn btnsm" id="pcoord-send" ?disabled=${!!c.busy} @click=${send}>${c.busy === 'send' ? 'Sending…' : 'Send'}</button></div>
    ${c.err ? html`<div class="err" id="pcoord-err">${c.err}</div>` : c.note ? html`<div class="muted small" id="pcoord-note">${c.note}${c.run && c.run.id ? html` <a class="lnk" @click=${() => ctx.app.select(c.run.id)}>open it</a>` : nothing}</div>` : nothing}
  </div>`;
}

const ICON = { ok: '✓', bad: '✗', warn: '!', run: '●', idle: '·' };

/** feedTpl(p, pv): the Activity tab — the project's events, newest first. */
export function feedTpl(p, pv) {
  const f = feed();
  const st = f.ensure(pv.id);
  const items = f.items(pv.id);
  const tasks = new Map((p.taskList(pv.id).items || []).map((t) => [t.n, t]));
  return html`<div class="pfeed" id="pfeed">
    <div class="pbar"><span class="muted small">What happened in this project — tasks, workspaces, pull requests, CI and reviews. Text from the scm provider is shown as it came, plain.</span>
      <span style="flex:1"></span><button class="btn ghost btnsm" id="pfeed-refresh" @click=${() => f.load(pv.id)}>Refresh</button></div>
    ${st.err ? html`<div class="err">${st.err}</div>` : nothing}
    ${repeat(items, (ev) => ev.id, (ev) => {
      const w = feedWords(ev);
      const t = w.n ? tasks.get(w.n) : null;
      return html`<div class="pev" data-kind=${w.kind} data-tone=${w.tone} data-n=${w.n || ''}>
        <span class="pevg">${ICON[w.tone] || '·'}</span>
        <span class="pevb"><span class="pevh">${w.n ? (t && t.run ? html`<a class="lnk" title=${t.title || ''} @click=${() => ctx.app.select(t.run)}>#${w.n}</a>` : html`<span>#${w.n}</span>`) : html`<span class="muted">project</span>`}
          <b>${w.label}</b>${w.by ? html`<span class="muted"> · ${w.by}</span>` : nothing}<span class="muted small"> · ${ago(w.when)}</span>
          ${w.wake ? html`<span class="badge" title="its coordinator was woken for it">coordinator</span>` : nothing}
          ${w.url ? html`<a href=${w.url} target="_blank" rel="noopener noreferrer" title="open it on the platform">↗</a>` : nothing}</span>
          ${w.text ? html`<span class="pevt">${w.text}</span>` : nothing}</span>
      </div>`;
    })}
    ${st.loading && !items.length ? html`<div class="muted small">loading…</div>` : !items.length && !st.err ? html`<div class="muted small empty-line">Nothing has happened yet.</div>` : nothing}
  </div>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pcoord { border: 1px solid var(--bx-border); border-left: 3px solid var(--bx-accent); border-radius: 7px; padding: 8px 10px; margin: 0 0 10px; background: var(--bx-panel); }
  .projs-page .pcoordh { display: flex; flex-wrap: wrap; gap: 4px 8px; align-items: baseline; }
  .projs-page .pcoordrow { display: flex; gap: 6px; margin-top: 6px; }
  .projs-page .pcoordrow input { flex: 1; min-width: 0; }
  .projs-page .lnk { cursor: pointer; color: var(--bx-accent); }
  .projs-page .pfeed .pev { display: flex; gap: 8px; padding: 5px 2px; border-bottom: 1px solid var(--bx-border); font-size: 12.5px; min-width: 0; }
  .projs-page .pfeed .pevg { flex: none; width: 1em; text-align: center; color: var(--bx-muted); }
  .projs-page .pfeed .pev[data-tone="ok"] .pevg { color: var(--bx-green); }
  .projs-page .pfeed .pev[data-tone="bad"] .pevg { color: var(--bx-red); }
  .projs-page .pfeed .pev[data-tone="warn"] .pevg { color: var(--bx-yellow, #d9a441); }
  .projs-page .pfeed .pev[data-tone="run"] .pevg { color: var(--bx-accent); }
  .projs-page .pfeed .pevb { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
  .projs-page .pfeed .pevh { display: flex; flex-wrap: wrap; gap: 4px; align-items: baseline; }
  .projs-page .pfeed .pevt { color: var(--bx-muted); white-space: pre-wrap; overflow-wrap: anywhere; }
`;
document.head.append(style);
