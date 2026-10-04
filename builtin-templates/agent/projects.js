// projects.js — the Projects page on the web (API.md §Projects in the UI):
//
//   side   the sidebar's "Projects" entry, with how many tasks need you
//   page   'projects': the list (yours, then team ones, archived last) — or
//          one project's page: its board (a column per state: queued,
//          working, needs you, PR, done) of task cards — #n, title, state,
//          branch, its pull requests (their checks only while the task has
//          no CI summary) and the chips other modules add (ext.card: CI) —
//          "New task" (what to do, a title, small or big, who works on it,
//          which repos), "From issues…" (a batch picker of the project's
//          issues, a task each), Warm; its Settings tab (project-settings.js);
//          the new-project form (project-new.js). Above the board, the
//          coordinator card; an Activity tab, the project's event feed
//          (project-feed.js). A team project's definition (kind team, at the
//          shared space) has no tasks of its own: its board is the team
//          board — each member's tasks — with "Work on this" from your own
//          space; your half of it (kind membership) leads to it, and asks you
//          to review the team's changes when there are some (project-team.js)
//
// A card opens its task's conversation, whose crumb (project-chips.js)
// comes back here. The state is app.projects (model/projects.js); the words
// of a task model/project-task.js. Text from the scm provider (titles,
// issue bodies) is untrusted: drawn as plain text, never markdown or HTML.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { can, agentChoices } from './model/projects.js';
import { cardWords, safeUrl } from './model/project-task.js';
import { newProjectTpl } from './project-new.js';
import { settingsTpl } from './project-settings.js';
import { coordCardTpl, feedTpl } from './project-feed.js';
import { teamBoardTpl, teamLinkTpl, reviewCardTpl } from './project-team.js';

const pj = () => ctx.app.projects;
let shownKey = '';

ext.register({
  side: () => sideTpl(),
  page: (p) => (p === 'projects' ? { get top() { return topTpl(); }, get body() { return bodyTpl(); } } : null), // drawn when asked
  paint: (v) => toTop(v),
});

// --- the sidebar's entry ---------------------------------------------------------------

function sideTpl() {
  const app = ctx.app;
  if (!app || !app.projects || !app.projects.supported) return null;
  const n = app.projects.needsYou();
  return html`<div class="autos-entry projentry ${app.page === 'projects' ? 'on' : ''}" id="projentry" @click=${() => app.openProjects()}>
    <span>Projects</span>
    ${n ? html`<span class="badge unread" title="tasks that need you">${n}</span>` : nothing}
  </div>`;
}

// a page opens at its top (another project, a tab, the form)
function toTop(v) {
  const app = ctx.app;
  if (!app || v || app.page !== 'projects') { shownKey = ''; return; }
  const p = app.projects;
  const key = `${p.opened}:${p.tab}:${!!p.form}`;
  if (key !== shownKey) { const tl = document.getElementById('timeline'); if (tl) tl.scrollTop = 0; }
  shownKey = key;
}

// --- the top bar -----------------------------------------------------------------------

function topTpl() {
  const p = pj();
  const pv = p.view();
  if (p.form) return html`<a class="crumb" @click=${() => p.closeForm()}>Projects ›</a><span class="title">New project</span>`;
  if (p.opened == null) return html`<span class="title">Projects</span><span class="muted" style="font-size:11.5px">a sandbox, its repos and task conversations</span>`;
  return html`<a class="crumb" @click=${() => p.open(null)}>Projects ›</a><span class="title">${pv ? pv.name : '#' + p.opened}</span>
    ${pv && pv.state !== 'active' ? html`<span class="badge">${pv.state}</span>` : nothing}`;
}

// --- the page --------------------------------------------------------------------------

function bodyTpl() {
  const p = pj();
  if (p.form) return newProjectTpl(p);
  if (p.opened != null) return projectTpl(p, p.view());
  return listTpl(p);
}

const errTpl = (p) => html`${p.err ? html`<div class="err" id="proj-err">${p.err}</div>` : nothing}
  ${p.flash ? html`<div class="note pflash">${p.flash}</div>` : nothing}`;

function listTpl(p) {
  return html`<div class="autos-page projs-page">
    <div class="ahd"><h3>Projects</h3><span class="muted small">a coding sandbox, its repos, and a conversation per task</span>
      <span style="flex:1"></span>
      ${p.supported ? html`<button class="btn btnsm" id="proj-new" @click=${() => p.newProject()}>New project</button>` : nothing}</div>
    ${errTpl(p)}
    ${!p.supported ? html`<div class="muted small empty-line">This agent has no Projects yet.</div>`
      : p.sections().map((s) => html`<h5>${s.title}</h5>
        ${s.items.length ? repeat(s.items, (x) => x.id, (x) => projCardTpl(p, x))
          : html`<div class="muted small empty-line">${p.loaded ? 'none yet — a project groups tasks around one sandbox and its repos' : 'loading…'}</div>`}`)}
  </div>`;
}

const COUNTS = [['needs-you', 'need you'], ['working', 'working'], ['queued', 'queued'], ['pr', 'in PR'], ['done', 'done']];

function projCardTpl(p, x) {
  const counts = COUNTS.filter(([k]) => (x.counts || {})[k]).map(([k, w]) => html`<span class="pcnt" data-col=${k}>${x.counts[k]} ${w}</span>`);
  return html`<div class="acard2 pcard" data-pid=${x.id} @click=${() => p.open(x.id)}>
    <div class="ah"><span class="nm">${x.name}</span>
      ${x.kind !== 'personal' ? html`<span class="badge">${x.kind === 'team' ? 'team' : 'team · yours'}</span>` : nothing}
      ${x.state !== 'active' ? html`<span class="badge">${x.state}</span>` : nothing}
      ${x.visibility === 'team' ? html`<span class="badge" title="everyone who can open this agent sees it">shared</span>` : nothing}
      <span style="flex:1"></span>
      ${x.owner && x.level !== 'owner' ? html`<span class="muted small">${x.owner}'s</span>` : nothing}</div>
    <div class="as muted small">${(x.repos || []).map((r) => r.repo).join(', ') || 'no repos yet'}${x.slots ? ` · ${x.slots.used}/${x.slots.max} at work` : ''}</div>
    ${counts.length ? html`<div class="as small pcounts">${counts}</div>` : nothing}
  </div>`;
}

function projectTpl(p, pv) {
  if (!pv) return html`<div class="autos-page projs-page">${errTpl(p)}<div class="muted small">loading…</div></div>`;
  const c = can(pv);
  return html`<div class="autos-page projs-page wide">
    <div class="ahd"><h3>${pv.name}</h3>
      <span class="muted small">${(pv.repos || []).map((r) => r.repo).join(', ')}</span>
      <span style="flex:1"></span>
      <span class="ptabs" role="tablist">
        <button class="btn ghost btnsm" role="tab" data-tab="board" aria-selected=${p.tab === 'board' ? 'true' : 'false'} @click=${() => p.showTab('board')}>Board</button>
        ${pv.kind !== 'team' ? html`<button class="btn ghost btnsm" role="tab" data-tab="events" aria-selected=${p.tab === 'events' ? 'true' : 'false'} @click=${() => p.showTab('events')}>Activity</button>` : nothing}
        <button class="btn ghost btnsm" role="tab" data-tab="settings" aria-selected=${p.tab === 'settings' ? 'true' : 'false'} @click=${() => p.showTab('settings')}>Settings</button>
      </span></div>
    ${errTpl(p)}
    ${p.tab === 'settings' ? settingsTpl(p, pv) : p.tab === 'events' && pv.kind !== 'team' ? feedTpl(p, pv) : boardPageTpl(p, pv, c)}
  </div>`;
}

function boardPageTpl(p, pv, c) {
  if (pv.kind === 'team') { // a team project's definition: it has no tasks of its own — the team board
    return html`<div class="note" id="pdef-note">This is the team project's definition. Its tasks run in each member's own space,
      on their own board — the Settings tab keeps its repos, policy and members.</div>${teamBoardTpl(p, pv)}`;
  }
  const list = p.taskList(pv.id);
  return html`
    ${pv.kind === 'membership' ? html`${teamLinkTpl(p, pv)}${reviewCardTpl(p, pv)}` : nothing}
    ${coordCardTpl(p, pv)}
    <div class="pbar">
      ${c.act ? html`<button class="btn btnsm" id="ptask-new" @click=${() => p.newTask()}>New task</button>
        <button class="btn ghost btnsm" id="ptask-issues" @click=${() => p.openPicker()} ?disabled=${!(pv.repos || []).length}>From issues…</button>
        <button class="btn ghost btnsm" id="proj-warm" title="start the sandbox, fetch the repos and refresh the credentials" @click=${() => p.warm(pv.id)}>Warm</button>` : nothing}
      <span style="flex:1"></span>
      <input type="search" class="pq" placeholder="Find a task…" .value=${p.filter.q} @change=${(e) => p.setFilter({ q: e.target.value })}>
      <label class="chk small"><input type="checkbox" .checked=${p.filter.mine} @change=${(e) => p.setFilter({ mine: e.target.checked })}> mine</label>
      ${pv.slots ? html`<span class="muted small" title="tasks holding a slot now, of the policy's maxTasks">${pv.slots.used}/${pv.slots.max} at work</span>` : nothing}
    </div>
    ${p.taskForm ? taskFormTpl(p, pv) : nothing}
    ${p.picker ? pickerTpl(p, pv) : nothing}
    ${list.err ? html`<div class="err">${list.err}</div>` : nothing}
    <div class="pboard">${p.board(pv.id).map((col) => html`<div class="pcol" data-col=${col.key}>
        <div class="pcolh">${col.title} <span class="muted">${col.tasks.length || ''}</span></div>
        ${repeat(col.tasks, (t) => t.n, (t) => taskCardTpl(t))}
      </div>`)}</div>
    ${list.next ? html`<button class="btn ghost btnsm" @click=${() => p.tasks(pv.id, p.filter, true)}>more</button>` : nothing}
    ${!list.items.length && !list.loading ? html`<div class="muted small empty-line">No tasks yet${c.act ? ' — New task, or From issues…' : ''}.</div>` : nothing}`;
}

// a task's card: opens its conversation; links (its PRs) open on the platform
function taskCardTpl(t) {
  const w = cardWords(t);
  const open = () => { if (t.run) ctx.app.select(t.run); };
  const stop = (e) => e.stopPropagation();
  return html`<div class="ptask" data-n=${t.n} data-state=${t.state || ''} tabindex="0" title=${t.run ? 'open its conversation' : 'its conversation was deleted'}
      @click=${open} @keydown=${(e) => { if (e.key === 'Enter') open(); }}>
    <div class="pth"><span class="ptn">${w.n}</span><span class="ptt">${w.title}</span></div>
    <div class="ptm"><span class="ptst" data-tone=${w.state.tone}>${w.state.text}</span>
      ${w.size ? html`<span class="badge">big</span>` : nothing}
      ${w.agent ? html`<span class="badge" title="a coding agent works on it">${w.agent}</span>` : nothing}</div>
    ${w.branch ? html`<div class="ptb mono" title="its branch">⎇ ${w.branch}</div>` : nothing}
    ${w.prs.length || ext.has('card') ? html`<div class="ptc">
      ${w.prs.map((c) => (c.url ? html`<a class="badge pchip" data-tone=${c.tone} href=${c.url} target="_blank" rel="noopener noreferrer" title=${c.title} @click=${stop}>${c.text} ↗</a>`
        : html`<span class="badge pchip" data-tone=${c.tone} title=${c.title}>${c.text}</span>`))}
      ${ext.card(t) || nothing}</div>` : nothing}
  </div>`;
}

// --- a new task --------------------------------------------------------------------------

// who may answer: the project's default (said as what it does), or a coding agent by name
function agentOptions(f, pv) {
  const app = ctx.app;
  app.harness.ensure();
  return agentChoices(pv, app.harness.catalog.harnesses || []).map((c) => html`<option value=${c.value} ?selected=${(f.agent || '') === c.value} ?disabled=${c.disabled}>${c.label}</option>`);
}

function taskFormTpl(p, pv) {
  const f = p.taskForm;
  const set = (k) => (e) => p.setTask(k, e.target.value);
  const togRepo = (slug) => (e) => p.setTask('repos', e.target.checked ? [...f.repos, slug] : f.repos.filter((s) => s !== slug));
  return html`<div class="pform" id="ptask-form">
    <div class="field"><label>What to do</label><textarea rows="3" .value=${f.text} @input=${set('text')} placeholder="Fix the login redirect loop on Safari"></textarea></div>
    <div class="row2">
      <div class="field"><label>Title</label><input .value=${f.title} @input=${set('title')} placeholder="from what to do"></div>
      <div class="field"><label>Size</label><select @change=${set('size')}>
        <option value="small" ?selected=${f.size !== 'big'}>small — a worktree in the project's sandbox</option>
        <option value="big" ?selected=${f.size === 'big'}>big — its own sandbox, forked</option></select></div>
      <div class="field"><label>Who works on it</label><select data-f="agent" @change=${set('agent')}>${agentOptions(f, pv)}</select></div>
    </div>
    ${(pv.repos || []).length > 1 ? html`<div class="field"><label>Repos</label>${pv.repos.map((r) => html`<label class="chk">
      <input type="checkbox" data-repo=${r.slug} .checked=${f.repos.includes(r.slug)} @change=${togRepo(r.slug)}> ${r.repo}</label>`)}</div>` : nothing}
    ${f.err ? html`<div class="err">${f.err}</div>` : nothing}
    <div><button class="btn btnsm" id="ptask-create" ?disabled=${f.busy} @click=${() => p.submitTask()}>Create task</button>
      <button class="btn ghost btnsm" @click=${() => p.closeTask()}>Cancel</button></div>
  </div>`;
}

// --- tasks from issues (the batch picker) ----------------------------------------------------

function pickerTpl(p, pv) {
  const k = p.picker;
  const set = (key, reload) => (e) => { p.setPicker(key, e.target.value); if (reload) p.searchIssues(); };
  return html`<div class="pform" id="ppicker">
    <div class="row2">
      <div class="field"><label>Repo</label><select @change=${set('repo', true)}>${(pv.repos || []).map((r) => html`<option value=${r.repo} ?selected=${k.repo === r.repo}>${r.repo}</option>`)}</select></div>
      <div class="field"><label>Find</label><input type="search" .value=${k.q} @change=${set('q', true)} placeholder="words, or label:bug"></div>
      <div class="field"><label>State</label><select @change=${set('state', true)}>
        <option value="open" ?selected=${k.state === 'open'}>open</option><option value="closed" ?selected=${k.state === 'closed'}>closed</option></select></div>
    </div>
    <div class="muted small">Issue text comes from the issue tracker — anyone may have written it. Each picked issue becomes a task (at most 20 at once).</div>
    <div class="pissues">
      ${k.items.map((i) => {
        const key = `${i.repo}#${i.number}`;
        return html`<label class="pissue" data-issue=${i.number}>
          <input type="checkbox" .checked=${k.picked.has(key)} ?disabled=${!k.picked.has(key) && k.picked.size >= 20} @change=${() => p.togglePick(i)}>
          <span class="pin">#${i.number}</span>
          <span class="pibody"><span class="pititle">${i.title}</span>
            ${(i.labels || []).map((l) => html`<span class="badge">${l}</span>`)}
            ${i.body ? html`<span class="pitext untrusted" title="untrusted text from the issue tracker">${String(i.body).slice(0, 240)}</span>` : nothing}</span>
          ${safeUrl(i.url) ? html`<a href=${safeUrl(i.url)} target="_blank" rel="noopener noreferrer" title="open the issue">↗</a>` : nothing}
        </label>`;
      })}
      ${k.loading ? html`<div class="muted small">loading…</div>` : !k.items.length ? html`<div class="muted small">No issues.</div>` : nothing}
      ${k.next && !k.loading ? html`<button class="btn ghost btnsm" @click=${() => p.searchIssues(true)}>more</button>` : nothing}
    </div>
    <div class="row2">
      <div class="field"><label>Size</label><select @change=${set('size')}>
        <option value="small" ?selected=${k.size !== 'big'}>small</option><option value="big" ?selected=${k.size === 'big'}>big</option></select></div>
      <div class="field"><label>Who works on them</label><select @change=${set('agent')}>${agentOptions(k, pv)}</select></div>
    </div>
    ${k.err ? html`<div class="err">${k.err}</div>` : nothing}
    ${k.result ? html`<div class="err">${k.result.errors.map((x) => html`<div>${x.issue && typeof x.issue === 'object' ? `${x.issue.repo}#${x.issue.number}` : x.issue}: ${x.error}</div>`)}</div>` : nothing}
    <div><button class="btn btnsm" id="ppick-make" ?disabled=${!k.picked.size || k.loading} @click=${() => p.submitBatch()}>Make ${k.picked.size || ''} task${k.picked.size === 1 ? '' : 's'}</button>
      <button class="btn ghost btnsm" @click=${() => p.closePicker()}>Cancel</button></div>
  </div>`;
}

// the page's look (the tile's own sheet stays as it is): it builds on .autos-page's
const style = document.createElement('style');
style.textContent = `
  .projs-page.wide { max-width: 1180px; }
  .projs-page .pcounts { display: flex; flex-wrap: wrap; gap: 4px 10px; margin-top: 3px; }
  .projs-page .pcnt[data-col="needs-you"] { color: var(--bx-yellow, #d9a441); font-weight: 600; }
  .projs-page .ahd { flex-wrap: wrap; }
  .projs-page .ptabs { display: inline-flex; gap: 2px; }
  .projs-page .ptabs [aria-selected="true"] { background: var(--bx-panel-2); box-shadow: inset 0 -2px 0 var(--bx-accent); }
  .projs-page .pbar { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin: 4px 0 10px; }
  .projs-page .pbar .pq { width: 180px; max-width: 100%; }
  .projs-page .pform { border: 1px solid var(--bx-border); border-radius: 7px; padding: 10px; margin: 0 0 12px; background: var(--bx-panel); }
  .projs-page .row2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 8px; }
  .projs-page .pboard { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; align-items: start; }
  @media (max-width: 900px) { .projs-page .pboard { grid-template-columns: minmax(0, 1fr); } }
  .projs-page .pcol { min-width: 0; background: var(--bx-panel-2); border-radius: 7px; padding: 6px; }
  .projs-page .pcolh { font-size: 10.5px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase; color: var(--bx-muted); margin: 2px 2px 6px; }
  .projs-page .pcol[data-col="needs-you"] .pcolh { color: var(--bx-yellow, #d9a441); }
  .projs-page .ptask { border: 1px solid var(--bx-border); border-radius: 6px; padding: 6px 8px; margin-bottom: 6px; background: var(--bx-panel); cursor: pointer; min-width: 0; }
  .projs-page .ptask:hover, .projs-page .ptask:focus { border-color: var(--bx-accent); outline: none; }
  .projs-page .pth { display: flex; gap: 5px; align-items: baseline; min-width: 0; }
  .projs-page .ptn { color: var(--bx-muted); font-size: 11px; flex: none; }
  .projs-page .ptt { font-weight: 600; font-size: 12.5px; overflow-wrap: anywhere; }
  .projs-page .ptm, .projs-page .ptc { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; margin-top: 3px; font-size: 11px; }
  .projs-page .ptb { font-size: 10.5px; color: var(--bx-muted); margin-top: 3px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .badge.pchip { color: var(--bx-muted); }
  .ptst[data-tone="run"], .badge.pchip[data-tone="run"] { color: var(--bx-accent); }
  .ptst[data-tone="ok"], .badge.pchip[data-tone="ok"] { color: var(--bx-green); }
  .ptst[data-tone="bad"], .badge.pchip[data-tone="bad"] { color: var(--bx-red); }
  .ptst[data-tone="warn"], .badge.pchip[data-tone="warn"] { color: var(--bx-yellow, #d9a441); }
  .ptst[data-tone="idle"] { color: var(--bx-muted); }
  a.badge.pchip { text-decoration: none; text-transform: none; letter-spacing: 0; }
  .projs-page .pissues { max-height: 320px; overflow: auto; margin: 6px 0; border: 1px solid var(--bx-border); border-radius: 6px; padding: 4px; }
  .projs-page .pissue { display: flex; gap: 6px; align-items: flex-start; padding: 4px; border-bottom: 1px solid var(--bx-border); font-size: 12px; min-width: 0; }
  .projs-page .pissue .pin { color: var(--bx-muted); flex: none; }
  .projs-page .pissue .pibody { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; gap: 3px 6px; }
  .projs-page .pissue .pititle { font-weight: 600; overflow-wrap: anywhere; }
  .projs-page .untrusted { flex-basis: 100%; color: var(--bx-muted); font-size: 11px; white-space: pre-wrap; overflow-wrap: anywhere;
    border-left: 2px dashed var(--bx-border); padding-left: 6px; max-height: 4.5em; overflow: hidden; }
  .projs-page .pflash { margin: 6px 0; }
`;
document.head.append(style);
