// project-chips.js — a project's conversation on the web (API.md §Projects
// in the UI): what a task's conversation shows of its project.
//
//   top    the task's branch chip (↗ to it on the platform), its pull
//          requests (↗; their checks only while the task has no CI summary —
//          then the CI chip says it), the setup outcome, and "Open PR" — shown
//          once the backend is known to have that route, on a task with a
//          branch and no open pull request, for people who may act on it
//   crumb  "‹project› ›" before the title: back to its project's board
//   end    the prep card while its workspace is prepared — the steps per repo,
//          what is under way, Retry when it failed — and the sign-in card:
//          the provider's device code and page, shown only to the person
//          who must sign in (it is their own sign-in)
//   task   in the unfolded pinned task: its project, number, size, repos,
//          issue, checkouts and ports
//
// The words are model/project-task.js; the calls app.projects
// (model/projects.js), which also reads the task again on a `project` event.
import { html, nothing } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import * as rules from './model/rules.js';
import { taskOf, taskChips, prepCard, prButton, crumb, taskSection } from './model/project-task.js';
import { homeOf } from './model/homes.js';

const pj = () => ctx.app.projects;
const polled = new Set(); // sign-in polls started, by pollId
const refreshed = new Set(); // `${runId}:${pollId}`: a task refreshed after that sign-in was done
const acts = new Map();   // runId → {busy, note}: an action under way on that task ('pr', 'retry'), what it said
const actOf = (v) => acts.get(v.run.id) || { busy: '', note: '' };

ext.register({
  top: (v) => chipsTpl(v),
  crumb: (v) => crumbTpl(v),
  end: () => prepTpl(ctx.app && ctx.app.session.current()),
  task: (v) => sectionTpl(v),
});

// --- the top bar ---------------------------------------------------------------------------

function chipsTpl(v) {
  const t = taskOf(v);
  if (!t || !ctx.app) return null;
  const p = pj();
  const pv = v.project ? p.ensure(v.project.id) : null;
  const chips = taskChips(v, pv);
  const pr = prButton(v, p.prRouteAt(homeOf(v.run.id), v.run.id), rules.access(v).talk);
  const { busy, note } = actOf(v);
  const stop = (e) => e.stopPropagation();
  return html`<span class="ptchips" id="ptchips">
    ${chips.map((c) => (c.url ? html`<a class="badge pchip" data-kind=${c.kind} data-tone=${c.tone} href=${c.url} target="_blank" rel="noopener noreferrer" title=${c.title} @click=${stop}>${c.text} ↗</a>`
      : html`<span class="badge pchip" data-kind=${c.kind} data-tone=${c.tone} title=${c.title}>${c.text}</span>`))}
    ${pr.shown ? html`<button class="btn ghost btnsm" id="ptask-pr" ?disabled=${pr.disabled || busy === 'pr'} title=${pr.title} @click=${() => openPR(v, t)}>${busy === 'pr' ? 'Opening…' : 'Open PR'}</button>` : nothing}
    ${note ? html`<span class="muted small" id="ptask-note">${note}</span>` : nothing}
  </span>`;
}

async function act(v, what, opts) {
  const id = v.run.id;
  acts.set(id, { busy: what, note: '' });
  ctx.paint();
  let note = '';
  try { await pj().taskAction(id, what, opts); note = what === 'pr' ? 'the pull request is being opened…' : ''; } catch (e) {
    note = e.status === 404 && what === 'pr' ? '' : e.message;
  }
  acts.set(id, { busy: '', note, err: !!note && !(what === 'pr' && note.startsWith('the pull request')) });
  ctx.paint();
}

function openPR(v, t) {
  if (!confirm(`Open a pull request for ${t.branch}? People outside xbin will see it.`)) return;
  act(v, 'pr', {});
}

// --- the crumb -------------------------------------------------------------------------------

function crumbTpl(v) {
  const c = crumb(v);
  if (!c || !ctx.app) return null;
  return html`<a class="crumb" id="projcrumb" title=${c.title} @click=${() => ctx.app.openProjects(c.pid)}>${c.text}</a>`;
}

// --- the prep and sign-in cards -----------------------------------------------------------------

function prepTpl(v) {
  if (!v || !ctx.app) return null;
  const card = prepCard(v, ctx.app.me);
  if (!card) return null;
  const canAct = rules.access(v).talk;
  const { busy, note, err } = actOf(v);
  const pollId = card.signin && card.signin.pollId;
  const pv = card.signin && v.project ? pj().ensure(v.project.id) : null;
  if (pollId && !polled.has(pollId) && pv && pv.scm) { polled.add(pollId); pj().pollSignin(pv.scm, card.signin); }
  // this card's sign-in only: a state of another (earlier) sign-in never counts
  const st0 = pv && pv.scm ? pj().signinOf(pv.scm) : null;
  const st = st0 && pollId && st0.pollId === pollId ? st0 : null;
  // signed in: the task's credentials and workspace are looked at again now, once per sign-in
  if (st && st.state === 'done' && !refreshed.has(`${v.run.id}:${pollId}`)) { refreshed.add(`${v.run.id}:${pollId}`); pj().taskAction(v.run.id, 'refresh').catch(() => {}); }
  const signinWords = !st ? 'Only you see this code. The task goes on once you approve it there.'
    : st.state === 'done' ? 'Signed in — the task goes on.'
    : st.state === 'error' ? html`<span class="err">Couldn't learn whether you signed in: ${st.err}.</span>
      <button class="btn ghost btnsm" id="ptask-signin-again" @click=${() => pj().pollSignin(pv.scm, card.signin)}>Check again</button>`
    : ['denied', 'expired'].includes(st.state) ? `The sign-in was ${st.state}.`
    : st.err ? `Only you see this code. Still waiting (checking again: ${st.err}).`
    : 'Only you see this code. The task goes on once you approve it there.';
  return html`<div class="pprep" id="pprep" data-ws=${card.ws} data-tone=${card.tone}>
    <div class="pprh">${card.tone === 'run' ? html`<span class="spin"></span>` : html`<span class="pglyph">${card.tone === 'bad' ? '✗' : '!'}</span>`}
      <b>${card.title}</b>${card.step ? html`<span class="muted"> — ${card.step}</span>` : nothing}</div>
    ${card.detail ? html`<div class="small">${card.detail}</div>` : nothing}
    ${card.steps.length ? html`<div class="psteps">${card.steps.map((s) => html`<div class="pstep" data-repo=${s.repo} data-tone=${s.tone}>
        <span class="pglyph">${s.glyph}</span><span class="mono">${s.repo}</span><span class="muted">${s.text}</span>
        ${s.error ? html`<span class="err">${s.error}</span>` : nothing}</div>`)}</div>` : nothing}
    ${card.error ? html`<div class="err">${card.error}</div>` : nothing}
    ${card.retry && canAct ? html`<div><button class="btn btnsm" id="pprep-retry" ?disabled=${busy === 'retry'} @click=${() => act(v, 'retry')}>Retry</button>
      <span class="muted small">the workspace's failed steps run again</span></div>` : nothing}
    ${card.signin ? html`<div class="psignin" id="ptask-signin">
      <div>To push, this task needs your own sign-in. Open
        ${card.signin.url ? html`<a href=${card.signin.url} target="_blank" rel="noopener noreferrer" id="ptask-signin-link">${card.signin.url}</a>` : 'the sign-in page'}
        and enter <b class="mono pcode" id="ptask-signin-code">${card.signin.userCode}</b></div>
      <div class="muted small" id="ptask-signin-state">${signinWords}</div>
    </div>` : card.signinElsewhere ? html`<div class="muted small" id="ptask-signin-other">${card.signinElsewhere}</div>` : nothing}
    ${note && err && !busy ? html`<div class="err">${note}</div>` : nothing}
  </div>`;
}

// --- the pinned task's project section ------------------------------------------------------------

function sectionTpl(v) {
  const s = taskSection(v);
  if (!s || !ctx.app) return null;
  return html`<div class="ptasksec" id="ptasksec">
    <div class="askhead">Project — <a class="lnk" @click=${() => ctx.app.openProjects(s.pid)}>${s.project}</a>, task #${s.n}</div>
    <div class="small">${s.size} · repos: ${s.repos}${s.ports ? ` · ports ${s.ports}` : ''}</div>
    ${s.issue ? html`<div class="small">from ${s.issueUrl ? html`<a href=${s.issueUrl} target="_blank" rel="noopener noreferrer">${s.issue} ↗</a>` : s.issue}</div>` : nothing}
    ${s.checkouts.map((c) => html`<div class="small mono" data-repo=${c.repo}>${c.repo}: ${c.path} (${c.mode}, ${c.state})</div>`)}
  </div>`;
}

const style = document.createElement('style');
style.textContent = `
  .ptchips { display: contents; }
  .top a.badge.pchip { text-decoration: none; text-transform: none; letter-spacing: 0; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .top .badge.pchip { text-transform: none; letter-spacing: 0; }
  .top .badge.pchip[data-kind="branch"] { font-family: var(--bx-mono); }
  .pprep { margin: 10px 0; padding: 10px 12px; border: 1px solid var(--bx-border); border-left: 3px solid var(--bx-accent); border-radius: 7px; background: var(--bx-panel); font-size: 12.5px; }
  .pprep[data-tone="bad"] { border-left-color: var(--bx-red); }
  .pprep[data-tone="warn"] { border-left-color: var(--bx-yellow, #d9a441); }
  .pprep .pprh { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .pprep .psteps { margin: 6px 0; display: grid; gap: 2px; }
  .pprep .pstep { display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; min-width: 0; }
  .pprep .pstep[data-tone="ok"] .pglyph { color: var(--bx-green); }
  .pprep .pstep[data-tone="bad"] .pglyph { color: var(--bx-red); }
  .pprep .pstep[data-tone="run"] .pglyph { color: var(--bx-accent); }
  .pprep .pstep[data-tone="warn"] .pglyph { color: var(--bx-yellow, #d9a441); }
  .top .taskpin .ptasksec { display: flex; flex-direction: column; gap: 2px; padding-top: 6px; border-top: 1px dashed var(--bx-border); }
  .top .taskpin .ptasksec .lnk { cursor: pointer; color: var(--bx-accent); }
`;
document.head.append(style);
