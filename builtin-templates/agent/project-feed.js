// project-feed.js — a project's coordinator and its event feed on the web
// (API.md §Projects in the UI), drawn in a project's page (projects.js):
//
//   coordCardTpl(p, pv)  the coordinator card above the board, a small chat
//                        (D209): what it is and what it does now, its last
//                        messages (model/project-feed.js preview — none yet:
//                        your first message starts it), Open ↗ (your
//                        coordinator, made on first use — its conversation
//                        opens) and right under them a line to write to it
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
import { projectFeed, feedWords, COORD_EMPTY } from './model/project-feed.js';
import { can } from './model/projects.js';
import { blocksTpl } from './chat-cards.js';

const feed = () => projectFeed(ctx.app);
const ago = (ms) => (ms ? new Date(ms).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' }) : '');

/** coordCardTpl(p, pv): the coordinator card (participants of a project with tasks). */
export function coordCardTpl(p, pv) {
  if (!pv || pv.kind === 'team' || !can(pv).act) return nothing;
  const f = feed();
  const c = f.coord(pv.id);
  if (c.missing) return nothing;
  const pr = f.preview(pv.id);
  const open = async () => { const run = await f.openCoordinator(pv.id); if (run && run.id) ctx.app.select(run.id); };
  const send = () => f.messageCoordinator(pv.id, c.text);
  const st = pr.shown ? pr.state : null;
  return html`<div class="pcoord" id="pcoord" data-state=${st ? st.key : nothing}>
    <div class="pcoordh"><b>Coordinator</b>
      ${st && st.word ? html`<span class="pcst" id="pcoord-state" data-tone=${st.tone}>${st.tone === 'run' ? html`<span class="spin"></span>` : nothing}${st.word}</span>` : nothing}
      <span style="flex:1"></span>
      <button class="btn ghost btnsm" id="pcoord-open" title="its whole conversation" ?disabled=${!!c.busy} @click=${open}>${c.busy === 'open' ? 'Opening…' : html`Open<bx-icon name="popout"></bx-icon>`}</button>
      <span class="muted small pcabout">your agent for this project: it makes tasks, follows them and tells you what needs you — it never merges or answers a task's question for you</span></div>
    ${pr.shown ? previewTpl(pv, pr, f, open) : nothing}
    <div class="pcoordrow">
      <input id="pcoord-text" aria-label="A message to the coordinator" placeholder=${pr.shown && !pr.run ? 'Ask it something — this starts it: make tasks for the open bugs…' : 'Ask it something: make tasks for the open bugs, tell me what failed…'} .value=${c.text}
        @input=${(e) => { c.text = e.target.value; }} @keydown=${(e) => { if (e.key === 'Enter' && !e.isComposing) send(); }}>
      <button class="btn btnsm" id="pcoord-send" ?disabled=${!!c.busy} @click=${send}>${c.busy === 'send' ? 'Sending…' : 'Send'}</button></div>
    ${c.err ? html`<div class="err" id="pcoord-err">${c.err}</div>` : c.note ? html`<div class="muted small" id="pcoord-note">${c.note}${c.run && c.run.id && !pr.shown ? html` <a class="lnk" @click=${() => ctx.app.select(c.run.id)}>open it</a>` : nothing}</div>` : nothing}
  </div>`;
}

// previewTpl: its last messages, newest at the bottom (the box keeps its end
// in view), and what it does now — or, before it exists, what Send does.
function previewTpl(pv, pr, f, open) {
  if (!pr.run) {
    return html`<div class="pcprev empty" id="pcoord-prev"><div class="pcin">
      ${pr.err ? html`<span class="err small">${pr.err}</span>` : html`<span class="muted small">${pr.loading ? 'loading…' : COORD_EMPTY}</span>`}</div></div>`;
  }
  const ui = ctx.app.session.ui;
  return html`<div class="pcprev" id="pcoord-prev"><div class="pcin">
    ${pr.more ? html`<div class="muted small pcmore">… <button class="lnk" @click=${open}>earlier messages are in its chat</button></div>` : nothing}
    ${pr.tail ? (pr.tail.length ? blocksTpl(pr.tail, ui, 1) : html`<div class="muted small">Nothing said yet.</div>`)
      : pr.err ? html`<div class="small" role="status"><span class="err">Couldn't read its latest messages: ${pr.err}</span>
          <button class="lnk" data-act="retry" @click=${() => f.readTail(pv.id)}>Retry</button></div>`
      : html`<div class="muted small">loading…</div>`}
    ${pr.line ? html`<div class="pcline small" data-tone=${pr.state.tone}>${pr.line}</div>` : nothing}
  </div></div>`;
}

// an event's glyph by tone (D184: bx-icons names; idle: a muted square)
const ICON = { ok: 'ok', bad: 'error', warn: 'warning', run: 'live' };

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
        <span class="pevg">${ICON[w.tone] ? html`<bx-icon name=${ICON[w.tone]}></bx-icon>` : html`<span class="pevsq"></span>`}</span>
        <span class="pevb"><span class="pevh">${w.n ? (t && t.run ? html`<a class="lnk" title=${t.title || ''} @click=${() => ctx.app.select(t.run)}>#${w.n}</a>` : html`<span>#${w.n}</span>`) : html`<span class="muted">project</span>`}
          <b>${w.label}</b>${w.by ? html`<span class="muted"> · ${w.by}</span>` : nothing}<span class="muted small"> · ${ago(w.when)}</span>
          ${w.wake ? html`<span class="badge" title="its coordinator was woken for it">coordinator</span>` : nothing}
          ${w.url ? html`<a href=${w.url} target="_blank" rel="noopener noreferrer" title="open it on the platform">↗</a>` : nothing}</span>
          ${w.text ? html`<span class="pevt">${w.text}</span>` : nothing}</span>
      </div>`;
    })}
    ${st.more ? html`<div class="small" id="pfeed-more"><span class="muted">The oldest of this project's events are shown — newer ones wait.</span>
      <button class="btn ghost btnsm" ?disabled=${st.loading} @click=${() => f.load(pv.id)}>Read newer</button></div>` : nothing}
    ${st.loading && !items.length ? html`<div class="muted small">loading…</div>` : !items.length && !st.err ? html`<div class="muted small empty-line">Nothing has happened yet.</div>` : nothing}
  </div>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pcoord { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px 12px; margin: 0 0 12px; background: var(--bx-panel); }
  .projs-page .pcoordh { display: flex; flex-wrap: wrap; gap: 4px 8px; align-items: baseline; }
  .projs-page .pcoordh .pcabout { flex-basis: 100%; }
  .projs-page .pcoordrow { display: flex; gap: 6px; margin-top: 6px; }
  .projs-page .pcst { display: inline-flex; align-items: center; gap: 4px; font: var(--bx-font-meta); color: var(--bx-muted); }
  .projs-page .pcst[data-tone="run"] { color: var(--bx-info); }
  .projs-page .pcst[data-tone="warn"] { color: var(--bx-warn); }
  .projs-page .pcst[data-tone="bad"] { color: var(--bx-danger); }
  .projs-page .pcprev { display: flex; flex-direction: column-reverse; max-height: 320px; overflow: auto; margin-top: 8px; padding: 0 8px;
    border: 1px solid var(--bx-border); border-radius: var(--bx-radius); background: var(--bx-bg); }
  .projs-page .pcprev.empty { padding: 8px; }
  .projs-page .pcprev .msg { margin: 6px 0; }
  .projs-page .pcmore { padding-top: 6px; }
  .projs-page .pcline { color: var(--bx-muted); padding-bottom: 6px; }
  .projs-page .pcline[data-tone="warn"] { color: var(--bx-warn); }
  .projs-page .pcprev .lnk, .projs-page .pcmore .lnk { border: 0; background: none; padding: 0; min-height: 0; font: inherit; font-weight: normal; }
  .projs-page .pcoordrow input { flex: 1; min-width: 0; }
  .projs-page .lnk { cursor: pointer; color: var(--bx-link); }
  .projs-page .pfeed .pev { display: flex; gap: 8px; padding: 4px 2px; border-bottom: 1px solid var(--bx-border); min-width: 0; }
  .projs-page .pfeed .pevg { flex: none; display: inline-flex; width: 16px; height: var(--bx-text-line, 18px); align-items: center; justify-content: center; color: var(--bx-muted); }
  .projs-page .pfeed .pevsq { width: 4px; height: 4px; background: currentColor; }
  .projs-page .pfeed .pev[data-tone="ok"] .pevg { color: var(--bx-ok); }
  .projs-page .pfeed .pev[data-tone="bad"] .pevg { color: var(--bx-danger); }
  .projs-page .pfeed .pev[data-tone="warn"] .pevg { color: var(--bx-warn); }
  .projs-page .pfeed .pev[data-tone="run"] .pevg { color: var(--bx-accent); }
  .projs-page .pfeed .pevb { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
  .projs-page .pfeed .pevh { display: flex; flex-wrap: wrap; gap: 4px; align-items: baseline; }
  .projs-page .pfeed .pevt { color: var(--bx-muted); white-space: pre-wrap; overflow-wrap: anywhere; }
`;
document.head.append(style);
