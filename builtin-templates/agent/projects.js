// projects.js — the Projects page on the web (API.md §Projects in the UI):
//
//   listTop  Projects atop your conversations (Mine, not while searching;
//            D209): your active projects — those with tasks that need you
//            first, at most PINNED — each with how many need you, a click to
//            its board, and All projects (how many more there are; D212);
//            the heading folds the projects (kept in your prefs) — folded it
//            says how many tasks need you, and All projects stays
//   page   'projects': the list (yours, then team ones, archived last) — or
//          one project's page: its board (a column per state: queued,
//          working, needs you, PR, done) of task cards — #n, title, state,
//          branch, its pull requests (their checks only while the task has
//          no CI summary) and the chips other modules add (ext.card: CI) —
//          "New task" (what to do, a title, small or big, who works on it,
//          which repos), "From issues…" (a batch picker of the project's
//          issues, a task each), Warm; its Settings tab (project-settings.js);
//          the new-project form (project-new.js). Above the board, the
//          coordinator card, and the bar at the bottom of the page writes to
//          the coordinator — never a new chat (D212); an Activity tab, the
//          project's event feed (project-feed.js). On a backend that serves
//          a project's overview (D214) the page is that: the status strip
//          (project-status.js), the board — each card with its agent's line
//          —, the bar and the coordinator's section (project-coord.js, which
//          also fits the page to a tall tile); its name, Status and the
//          tabs in the top bar; an older backend's page is drawn as before
//          (the coordinator card above the board). A team project's
//          definition (kind team, at the shared space) has no tasks of its
//          own: its board is the team board — each member's tasks — with
//          "Work on this" from your own space; your half of it (kind
//          membership) leads to it, and asks you to review the team's
//          changes when there are some (project-team.js)
//
// A card opens its task's conversation, whose crumb (project-chips.js)
// comes back here. The state is app.projects (model/projects.js); the words
// of a task model/project-task.js. Text from the scm provider (titles,
// issue bodies) is untrusted: drawn as plain text, never markdown or HTML.
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { can, agentChoices, pinWords, PINNED } from './model/projects.js';
import { cardWords, issueWords } from './model/project-task.js';
import { newProjectTpl } from './project-new.js';
import { settingsTpl } from './project-settings.js';
import { coordCardTpl, feedTpl } from './project-feed.js';
import { teamBoardTpl, teamLinkTpl, reviewCardTpl } from './project-team.js';
import { forkBaseOffer, forkBase } from './model/project-upgrade.js';
import { chipBody } from './project-chips.js';
import { projectOverview, cardAgent, TIPS } from './model/project-overview.js';
import { stripTpl, foldTpl, agentLineTpl, cardTip } from './project-status.js';
import { coordSecTpl, splitTpl, pageBar, onOverview } from './project-coord.js';

// keys(fn): Enter and Space on the element itself (not a control inside it) do what a click does —
// a clickable card, entry or crumb, reached by Tab
const keys = (fn) => (e) => { if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); fn(e); } };

const pj = () => ctx.app.projects;
let shownKey = '';

ext.register({
  listTop: () => listTopTpl(),
  page: (p) => (p === 'projects' ? { get top() { return topTpl(); }, get body() { return bodyTpl(); }, get composer() { return pageBar(pj()); } } : null), // drawn when asked
  paint: (v) => toTop(v),
});

// --- atop your conversations (D209) -------------------------------------------------------

const needBadge = (n, title) => (n ? html`<span class="badge unread" title=${title}>${n}</span>` : nothing);

function listTopTpl() {
  const app = ctx.app;
  const p = app && app.projects;
  if (!p) return null;
  const pin = p.pinned(PINNED);
  if (!pin.shown) return null;
  const at = app.page === 'projects' && app.sel == null;
  const fold = () => p.setFolded(!p.folded);
  if (pin.none) { // no project yet: one quiet row to the page, where New project is
    return html`<div class="projsec" id="projsec"><div class="prow projall ${at ? 'on' : ''}" id="projentry" role="button" tabindex="0"
        title="a sandbox, its repos and a conversation per task" @click=${() => app.openProjects()} @keydown=${keys(() => app.openProjects())}>
      <span class="pic"><bx-icon name="folder"></bx-icon></span><div class="t">Projects</div></div></div>`;
  }
  return html`<div class="projsec" id="projsec">
    <button class="grp projgrp" id="projfold" aria-expanded=${p.folded ? 'false' : 'true'} title=${p.folded ? 'show your projects' : 'fold your projects'} @click=${fold}>
      <bx-icon name=${p.folded ? 'caret-right' : 'caret-down'}></bx-icon><span>Projects</span>${p.folded ? needBadge(pin.need, 'tasks that need you, across your projects') : nothing}</button>
    ${p.folded ? nothing : repeat(pin.items, (x) => x.id, (x) => {
      const w = pinWords(x);
      const go = () => app.openProjects(x.id);
      return html`<div class="prow projrow ${at && p.opened === x.id ? 'on' : ''}" data-pid=${x.id} role="button" tabindex="0"
          title=${[w.repos, w.detail, x.kind === 'membership' ? 'a team project — your half' : x.kind === 'team' ? 'a team project' : ''].filter(Boolean).join(' · ') || w.name}
          @click=${go} @keydown=${keys(go)}>
        <span class="pic"><bx-icon name=${w.team ? 'people' : 'folder'}></bx-icon></span>
        <div class="t">${w.name}</div>
        ${needBadge(w.need, 'tasks that need you')}
      </div>`;
    })}
    <div class="prow projall ${at && p.opened == null ? 'on' : ''}" id="projentry" role="button" tabindex="0" @click=${() => app.openProjects()} @keydown=${keys(() => app.openProjects())}>
      <span class="pic"><bx-icon name="list"></bx-icon></span><div class="t">All projects${pin.total ? html` <span class="muted">(${pin.total})</span>` : nothing}</div>
      ${pin.more && !p.folded ? html`<span class="muted small pmore" title="active projects not shown above">${pin.more} more</span>` : nothing}
    </div>
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
  if (p.form) return html`<a class="crumb" role="button" tabindex="0" @click=${() => p.closeForm()} @keydown=${keys(() => p.closeForm())}>Projects ›</a><span class="title">New project</span>`;
  if (p.opened == null) return html`<span class="title">Projects</span><span class="muted small">a sandbox, its repos and task conversations</span>`;
  const crumb = html`<a class="crumb" role="button" tabindex="0" @click=${() => p.open(null)} @keydown=${keys(() => p.open(null))}>Projects ›</a>`;
  const badge = pv && pv.state !== 'active' ? html`<span class="badge">${pv.state}</span>` : nothing;
  if (!onOverview(p)) return html`${crumb}<span class="title">${pv ? pv.name : '#' + p.opened}</span>${badge}`;
  // the overview (D214): its repos beside the name, Status (the strip's fold) and the tabs up here
  return html`${crumb}<span class="title">${pv.name}<span class="prepos">${(pv.repos || []).map((r) => r.repo).join(', ')}</span></span>${badge}
    ${p.tab === 'board' ? foldTpl(projectOverview(ctx.app)) : nothing}${tabsTpl(p, pv, true)}`;
}

// the tabs: Board (the overview's: Overview), Activity (not a team definition's), Settings
function tabsTpl(p, pv, ov) {
  const tab = (key, word, tip) => html`<button class="btn ghost btnsm" role="tab" data-tab=${key} aria-selected=${p.tab === key ? 'true' : 'false'}
    data-tip=${ov ? tip : nothing} @click=${() => p.showTab(key)}>${word}</button>`;
  return html`<span class="ptabs" role="tablist">${tab('board', ov ? 'Overview' : 'Board', TIPS.overview)}${pv.kind !== 'team' ? tab('events', 'Activity', TIPS.activity) : nothing}${tab('settings', 'Settings', TIPS.settings)}</span>`;
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
  return html`<div class="acard2 pcard" data-pid=${x.id} role="button" tabindex="0" @click=${() => p.open(x.id)} @keydown=${keys(() => p.open(x.id))}>
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
  if (pv.kind !== 'team') projectOverview(ctx.app).ensure(pv.id); // read once: whether this backend serves the overview
  if (onOverview(p)) return overviewTpl(p, pv, c);
  return html`<div class="autos-page projs-page wide">
    <div class="ahd"><h3>${pv.name}</h3>
      <span class="muted small">${(pv.repos || []).map((r) => r.repo).join(', ')}</span>
      <span style="flex:1"></span>
      ${tabsTpl(p, pv, false)}</div>
    ${errTpl(p)}
    ${p.tab === 'settings' ? settingsTpl(p, pv) : p.tab === 'events' && pv.kind !== 'team' ? feedTpl(p, pv) : boardPageTpl(p, pv, c)}
  </div>`;
}

function boardPageTpl(p, pv, c) {
  if (pv.kind === 'team') { // a team project's definition: it has no tasks of its own — the team board
    return html`<div class="note" id="pdef-note">This is the team project's definition. Its tasks run in each member's own space,
      on their own board — the Settings tab keeps its repos, policy and members.</div>${teamBoardTpl(p, pv)}`;
  }
  return html`
    ${pv.kind === 'membership' ? html`${teamLinkTpl(p, pv)}${reviewCardTpl(p, pv)}` : nothing}
    ${coordCardTpl(p, pv)}
    ${taskBarTpl(p, pv, c, false)}
    ${boardTpl(p, pv, c, null)}`;
}

// overviewTpl: a project's page as its overview (D214) — the strip, the board, the bar, the coordinator;
// its Activity and Settings tabs as before, their tabs in the top bar
function overviewTpl(p, pv, c) {
  if (p.tab !== 'board') return html`<div class="autos-page projs-page wide povtab">${errTpl(p)}${p.tab === 'settings' ? settingsTpl(p, pv) : feedTpl(p, pv)}</div>`;
  const o = projectOverview(ctx.app);
  const it = o.ensure(pv.id);
  return html`<div class="autos-page projs-page pov" id="pov">
    ${errTpl(p)}
    ${pv.kind === 'membership' ? html`${teamLinkTpl(p, pv)}${reviewCardTpl(p, pv)}` : nothing}
    ${stripTpl(o, it, pv.id)}
    ${taskBarTpl(p, pv, c, true)}
    <div class="povboard" id="pov-board">${boardTpl(p, pv, c, it)}</div>
    ${splitTpl(pv)}
    ${coordSecTpl(p, pv, it)}
  </div>`;
}

// the board's bar: New task, From issues…, Warm, Fork base now, find, mine, the slots (ov: the overview's —
// headed Tasks, each part with its tooltip)
function taskBarTpl(p, pv, c, ov) {
  const tip = (k) => (ov ? TIPS[k] : nothing);
  const title = (t) => (ov ? nothing : t);
  return html`<div class="pbar">
      ${ov ? html`<h3 data-tip=${TIPS.board}>Tasks</h3>` : nothing}
      ${c.act ? html`<button class="btn btnsm" id="ptask-new" data-tip=${tip('newTask')} @click=${() => p.newTask()}>New task</button>
        <button class="btn ghost btnsm" id="ptask-issues" data-tip=${tip('fromIssues')} @click=${() => p.openPicker()} ?disabled=${!(pv.repos || []).length}>From issues…</button>
        <button class="btn ghost btnsm" id="proj-warm" title=${title('start the sandbox, fetch the repos and refresh the credentials')} data-tip=${tip('warm')} @click=${() => p.warm(pv.id)}>Warm</button>` : nothing}
      ${forkBaseOffer(pv) ? html`<button class="btn ghost btnsm" id="proj-forkbase" title=${title('big tasks fork a snapshot of the project\'s sandbox, taken while it is quiet')} data-tip=${tip('forkBase')}
        @click=${() => { if (confirm('Snapshot the project\'s sandbox for big tasks now? It stops while the snapshot is taken; running tasks wait.')) p.act(() => forkBase(pv.id), pv.id).then((r) => { if (r) { p.flash = 'The fork base is being taken.'; p.changed(); } }); }}>Fork base now</button>` : nothing}
      <span style="flex:1"></span>
      <input type="search" class="pq" aria-label="Find a task" placeholder="Find a task…" data-tip=${tip('find')} .value=${p.filter.q} @change=${(e) => p.setFilter({ q: e.target.value })}>
      <label class="chk small" data-tip=${tip('mine')}><input type="checkbox" .checked=${p.filter.mine} @change=${(e) => p.setFilter({ mine: e.target.checked })}> mine</label>
      ${pv.slots ? html`<span class="muted small" title=${title('tasks holding a slot now, of the policy\'s maxTasks')} data-tip=${tip('slots')}>${pv.slots.used}/${pv.slots.max} at work</span>` : nothing}
    </div>`;
}

// the board: the forms, a column per state, more, none yet (it: the overview's item — each card says its agent)
function boardTpl(p, pv, c, it) {
  const list = p.taskList(pv.id);
  return html`
    ${p.taskForm ? taskFormTpl(p, pv) : nothing}
    ${p.picker ? pickerTpl(p, pv) : nothing}
    ${list.err ? html`<div class="err">${list.err}</div>` : nothing}
    <div class="pboard">${p.board(pv.id).map((col) => html`<div class="pcol" data-col=${col.key}>
        <div class="pcolh">${col.title} <span class="muted">${col.tasks.length || ''}</span></div>
        ${repeat(col.tasks, (t) => t.n, (t) => taskCardTpl(t, it))}
      </div>`)}</div>
    ${list.next ? html`<button class="btn ghost btnsm" @click=${() => p.tasks(pv.id, p.filter, true)}>more</button>` : nothing}
    ${!list.items.length && !list.loading ? html`<div class="muted small empty-line">No tasks yet${c.act ? ' — New task, or From issues…' : ''}.</div>` : nothing}`;
}

// a task's card: opens its conversation; links (its PRs) open on the platform. On the overview (it) its
// agent's line (model/project-overview.js cardAgent) and a tooltip instead of a title
function taskCardTpl(t, it = null) {
  const w = cardWords(t);
  const a = it ? cardAgent(it.data, t.n) : null;
  const open = () => { if (t.run) ctx.app.select(t.run); };
  const stop = (e) => e.stopPropagation();
  return html`<div class="ptask" data-n=${t.n} data-state=${t.state || ''} tabindex="0" title=${it ? nothing : t.run ? 'open its conversation' : 'its conversation was deleted'}
      data-tip=${it ? cardTip(t, a) : nothing} data-tip-title=${it ? `${w.n} ${w.title}` : nothing}
      @click=${open} @keydown=${(e) => { if (e.key === 'Enter') open(); }}>
    <div class="pth"><span class="ptn">${w.n}</span><span class="ptt">${w.title}</span></div>
    <div class="ptm"><span class="ptst" data-tone=${w.state.tone}>${w.state.text}</span>
      ${w.size ? html`<span class="badge">big</span>` : nothing}
      ${w.agent && !a ? html`<span class="badge" title="a coding agent works on it">${w.agent}</span>` : nothing}</div>
    ${a ? agentLineTpl(a) : nothing}
    ${w.branch ? html`<div class="ptb mono" title="its branch"><bx-icon name="branch"></bx-icon>${w.branch}</div>` : nothing}
    ${w.prs.length || ext.has('card') ? html`<div class="ptc">
      ${w.prs.map((c) => (c.url ? html`<a class="badge pchip" data-tone=${c.tone} href=${c.url} target="_blank" rel="noopener noreferrer" title=${c.title} @click=${stop}>${chipBody(c)} ↗</a>`
        : html`<span class="badge pchip" data-tone=${c.tone} title=${c.title}>${chipBody(c)}</span>`))}
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
    <div class="field"><label>What to do</label><textarea aria-label="What to do" rows="3" .value=${f.text} @input=${set('text')} placeholder="Fix the login redirect loop on Safari"></textarea></div>
    <div class="row2">
      <div class="field"><label>Title</label><input aria-label="Title" .value=${f.title} @input=${set('title')} placeholder="from what to do"></div>
      <div class="field"><label>Size</label><select aria-label="Size" @change=${set('size')}>
        <option value="small" ?selected=${f.size !== 'big'}>small — a worktree in the project's sandbox</option>
        <option value="big" ?selected=${f.size === 'big'}>big — its own sandbox, forked</option></select></div>
      <div class="field"><label>Who works on it</label><select aria-label="Who works on it" data-f="agent" @change=${set('agent')}>${agentOptions(f, pv)}</select></div>
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
      <div class="field"><label>Repo</label><select aria-label="Repo" @change=${set('repo', true)}>${(pv.repos || []).map((r) => html`<option value=${r.repo} ?selected=${k.repo === r.repo}>${r.repo}</option>`)}</select></div>
      <div class="field"><label>Find</label><input type="search" aria-label="Find issues" .value=${k.q} @change=${set('q', true)} placeholder="words, or label:bug"></div>
      <div class="field"><label>State</label><select aria-label="State" @change=${set('state', true)}>
        <option value="open" ?selected=${k.state === 'open'}>open</option><option value="closed" ?selected=${k.state === 'closed'}>closed</option></select></div>
    </div>
    <div class="muted small">Issue text comes from the issue tracker — anyone may have written it. Each picked issue becomes a task (at most 20 at once).</div>
    <div class="pissues">
      ${k.items.map((i) => {
        const w = issueWords(i);
        return html`<label class="pissue" data-issue=${i.number}>
          <input type="checkbox" .checked=${k.picked.has(w.key)} ?disabled=${!k.picked.has(w.key) && k.picked.size >= 20} @change=${() => p.togglePick(i)}>
          <span class="pin">#${w.number}</span>
          <span class="pibody"><span class="pititle">${w.title}</span>
            ${w.labels.map((l) => html`<span class="badge">${l}</span>`)}
            ${w.body ? html`<span class="pitext untrusted" title="untrusted text from the issue tracker">${w.body}</span>` : nothing}</span>
          ${w.url ? html`<a href=${w.url} target="_blank" rel="noopener noreferrer" title="open the issue">↗</a>` : nothing}
        </label>`;
      })}
      ${k.loading ? html`<div class="muted small">loading…</div>` : !k.items.length ? html`<div class="muted small">No issues.</div>` : nothing}
      ${k.next && !k.loading ? html`<button class="btn ghost btnsm" @click=${() => p.searchIssues(true)}>more</button>` : nothing}
    </div>
    <div class="row2">
      <div class="field"><label>Size</label><select aria-label="Size" @change=${set('size')}>
        <option value="small" ?selected=${k.size !== 'big'}>small</option><option value="big" ?selected=${k.size === 'big'}>big</option></select></div>
      <div class="field"><label>Who works on them</label><select aria-label="Who works on them" @change=${set('agent')}>${agentOptions(k, pv)}</select></div>
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
  .projsec { margin: 0 0 2px; }
  .bx .projgrp { display: flex; align-items: center; gap: 4px; width: 100%; box-sizing: border-box; border: 0; border-radius: 0; background: none;
    min-height: 0; text-align: left; cursor: pointer; padding: 12px 12px 4px 8px;
    font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
  .bx .projgrp:hover { color: var(--bx-text); background: none; }
  .bx .projgrp .badge { margin-left: 4px; text-transform: none; letter-spacing: 0; }
  /* a project's row looks like a conversation's (index.html .run) without being one */
  .projsec .prow { box-sizing: border-box; min-height: var(--bx-row); padding: 4px 6px 4px 10px; cursor: pointer; display: flex; align-items: center; gap: 4px;
    border-radius: var(--bx-radius); margin: 0 4px; }
  .projsec .prow:hover { background: var(--bx-hover); }
  .projsec .prow.on { background: var(--bx-selection); color: var(--bx-selection-text); box-shadow: inset 2px 0 0 var(--bx-accent); }
  .projsec .prow .t { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; min-width: 0; }
  .projsec .pic { display: inline-flex; flex: none; color: var(--bx-muted); }
  .projsec .on .pic { color: inherit; }
  .projall .t { color: var(--bx-muted); }
  .projall.on .t, .projall:hover .t { color: inherit; }
  .projs-page.wide { max-width: 1180px; }
  .projs-page .pcounts { display: flex; flex-wrap: wrap; gap: 4px 10px; margin-top: 3px; }
  .projs-page .pcnt[data-col="needs-you"] { color: var(--bx-warn); font-weight: 600; }
  .projs-page .ahd { flex-wrap: wrap; }
  .projs-page .ptabs { display: inline-flex; gap: 2px; }
  .projs-page .ptabs [aria-selected="true"] { background: var(--bx-panel-2); box-shadow: inset 0 -2px 0 var(--bx-accent); }
  .projs-page .pbar { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin: 4px 0 10px; }
  .projs-page .pbar .pq { width: 180px; max-width: 100%; }
  .projs-page .pform { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 12px; margin: 0 0 12px; background: var(--bx-panel); }
  .projs-page .row2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 8px; }
  .projs-page .pboard { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; align-items: start; }
  @media (max-width: 900px) { .projs-page .pboard { grid-template-columns: minmax(0, 1fr); } }
  .projs-page .pcol { min-width: 0; background: var(--bx-panel-2); border-radius: var(--bx-radius); padding: 8px; }
  .projs-page .pcolh { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); margin: 2px 2px 8px; }
  .projs-page .pcol[data-col="needs-you"] .pcolh { color: var(--bx-warn); }
  .projs-page .ptask { border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 8px; margin-bottom: 6px; background: var(--bx-panel); cursor: pointer; min-width: 0; }
  .projs-page .ptask:hover { border-color: var(--bx-border-strong); }
  .projs-page .pth { display: flex; gap: 5px; align-items: baseline; min-width: 0; }
  .projs-page .ptn { color: var(--bx-muted); font: var(--bx-font-meta); font-family: var(--bx-mono); flex: none; }
  .projs-page .ptt { font-weight: 600; overflow-wrap: anywhere; }
  .projs-page .ptm, .projs-page .ptc { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; margin-top: 4px; font: var(--bx-font-meta); }
  .projs-page .ptb { display: flex; align-items: center; gap: 4px; font-size: var(--bx-mono-size, 12px); color: var(--bx-muted); margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .badge.pchip { color: var(--bx-muted); }
  .ptst[data-tone="run"], .badge.pchip[data-tone="run"] { color: var(--bx-info); }
  .ptst[data-tone="ok"], .badge.pchip[data-tone="ok"] { color: var(--bx-ok); }
  .ptst[data-tone="bad"], .badge.pchip[data-tone="bad"] { color: var(--bx-danger); }
  .ptst[data-tone="warn"], .badge.pchip[data-tone="warn"] { color: var(--bx-warn); }
  .ptst[data-tone="idle"] { color: var(--bx-muted); }
  a.badge.pchip { text-decoration: none; text-transform: none; letter-spacing: 0; }
  .projs-page .pissues { max-height: 320px; overflow: auto; margin: 6px 0; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); padding: 4px; }
  .projs-page .pissue { display: flex; gap: 6px; align-items: flex-start; padding: 4px; border-bottom: 1px solid var(--bx-border); min-width: 0; }
  .projs-page .pissue .pin { color: var(--bx-muted); flex: none; }
  .projs-page .pissue .pibody { flex: 1; min-width: 0; display: flex; flex-wrap: wrap; gap: 3px 6px; }
  .projs-page .pissue .pititle { font-weight: 600; overflow-wrap: anywhere; }
  .projs-page .untrusted { flex-basis: 100%; color: var(--bx-muted); font: var(--bx-font-meta); white-space: pre-wrap; overflow-wrap: anywhere;
    border-left: 2px solid var(--bx-border); padding-left: 8px; max-height: 4.5em; overflow: hidden; }
  .projs-page .pflash { margin: 6px 0; }
`;
document.head.append(style);
