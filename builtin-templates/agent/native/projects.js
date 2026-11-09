// native/projects.js — Projects in the native view (API.md §Projects in the
// UI), the web's projects.js and project-new.js with the app's primitives,
// as screens of the stack (native/nav.js):
//
//   drawer               Projects atop your conversations (Mine, not while
//                        searching; D209): your active projects — those with
//                        tasks that need you first, at most PINNED — each with
//                        how many need you (#56: the badge, danger, what
//                        really waits on you; the ready-for-review ones
//                        beside it), a tap to its board, All projects
//                        (how many more; D212); the section folds (kept in
//                        your prefs) — folded it says how many tasks need you,
//                        and All projects stays under it
//   main                 Projects in the list's ⋯ menu, with how many tasks
//                        need you (from Shared and Archived too)
//   'projects'           the list — yours, team projects, archived — New project
//   'project' {pid}      a project, as the web's overview orders it (D214):
//                        what waits on you first (native/project-needs.js,
//                        #56: a task's question, approval, sign-in or
//                        failure, your coordinator's question — a tap opens
//                        it); its status (native/project-status.js: the
//                        coordinator, workers, subagents, tokens, source
//                        control, the sandbox; it folds), the board as a
//                        section per column — a row per task: #n, title,
//                        state, branch, its PRs (their checks only while it
//                        has no CI summary), its agent and subagents, the
//                        words other modules add (ext.card: CI's) — a tap
//                        opens its conversation, a swipe cancels it; Find a
//                        task and Mine; the latest activity; the coordinator
//                        (its state, goal — idle: what wakes it — its newest
//                        messages, open it; the composer docked at the bottom
//                        writes to it — D212); ⊕ New task, From issues…,
//                        Warm; ⚙ Settings (native/project-settings.js); your
//                        half of a team project leads to the team's board and
//                        its changes to review (native/project-team.js). A
//                        backend without the overview: the coordinator (a
//                        small chat) first, then the board, as before
//   'project-task-new'   what to do, a title, small or big, who works on it,
//                        which repos
//   'project-issues'     the batch picker: a repo's issues (plain, untrusted),
//                        up to 20 picked, size and who works on them
//   'project-events'     the project's activity, newest first
//   'project-new'        the new-project wizard, a screen per step
//                        (native/project-new.js)
//
// The list and a project are places (the menu's Projects, #proj,
// #proj=<id>, a task's Project: ‹name›): on top, the model has the page —
// and the project — open (nav.follow); a task's conversation is pushed over
// its board, so back returns to it. The state is app.projects
// (model/projects.js), projectFeed(app) (model/project-feed.js) and
// projectTeam(app).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, ui, push, top, when } from './ui.js';
import { can, agentChoices, pinWords, PINNED } from '../model/projects.js';
import { cardWords, issueWords, prWords } from '../model/project-task.js';
import { projectFeed, feedWords, COORD_EMPTY, COORD_TAIL } from '../model/project-feed.js';
import { projectOverview, coordPlaceholder, coordWords, fmtTokens } from '../model/project-overview.js';
import { overviewOf, statusTpl, agentWords, coordBadge, goalTpl, aboutSheet } from './project-status.js';
import { projectTeam } from '../model/project-team.js';
import { openUrl } from './project-task.js';
import { forkBaseOffer, forkBase } from '../model/project-upgrade.js';
import { openProjects, projectEntry, wide } from './nav.js';
import { needsSectionTpl } from './project-needs.js';
import { needSplit } from '../model/project-pins.js';

export const TONE = { run: 'accent', ok: 'ok', bad: 'danger', warn: 'warn', idle: 'muted' };
const pj = () => ctx.app.projects;
const errTpl = (p) => html`${p.err ? html`<section><notice tone="danger" text=${p.err}/></section>` : nothing}
  ${p.flash ? html`<section><notice tone="ok" text=${p.flash}/></section>` : nothing}`;
const drop = (s) => { const i = ui.stack.indexOf(s); if (i >= 0) ui.stack.splice(i); ctx.paint(); };

// --- the page on the stack ---------------------------------------------------------------------------

// The list and a project are places of the stack (native/nav.js): the model
// opens the page — and the project — when one comes on top (nav.follow).

// openProject pushes a project's screen (the model opens it: nav.follow).
export function openProject(pid) {
  push(projectEntry(pid));
}

// follow: when the stack goes back to a screen (another one covered it,
// and is gone), the model opens what it shows again — its project, or none
// for the list. A screen being pushed has opened it already.
export function follow(s) {
  if (top() !== s) { s.covered = true; return; }
  if (!s.covered) return;
  s.covered = false;
  const p = pj();
  const want = s.kind === 'projects' ? null : s.pid;
  if (p.opened !== want) p.open(want);
}

ext.register({
  drawer() {
    const p = ctx.app.projects;
    const pin = p.pinned(PINNED);
    if (!pin.shown) return null;
    if (pin.none) return html`<section><row title="Projects" subtitle="a sandbox, its repos and a conversation per task" icon="folder" nav @tap=${() => openProjects()}/></section>`; // none yet: one row to the page
    const all = html`<row title="All projects" subtitle=${pin.more && !p.folded ? `${pin.more} more` : nothing} detail=${pin.total ? String(pin.total) : nothing}
      icon="list" nav @tap=${() => openProjects()}/>`;
    return html`<section title="Projects" collapsible ?collapsed=${p.folded} badge=${p.folded && pin.need ? String((pin.split && pin.asks) || pin.need) : nothing}
        @toggle=${(e) => p.setFolded(!!e.collapsed)}>
      ${repeat(pin.items, (pv) => pv.id, (pv) => pinRow(pv))}
      ${p.folded ? nothing : all}
    </section>
    ${p.folded ? html`<section>${all}</section>` : nothing}`;
  },
  main() {
    const p = ctx.app.projects;
    if (!p.supported) return null;
    const n = p.needsYou();
    return html`<button icon="folder" @tap=${() => openProjects()}>${n ? `Projects (${n} need you)` : 'Projects'}</button>`;
  },
  screen(s) {
    switch (s.kind) {
      case 'projects': return listTpl(s);
      case 'project': return projectTpl(s);
      case 'project-task-new': return taskFormTpl(s);
      case 'project-issues': return pickerTpl(s);
      case 'project-events': return eventsTpl(s);
    }
    return null;
  },
});

// --- the list ---------------------------------------------------------------------------------------------

const COUNTS = [['needs-you', 'need you'], ['working', 'working'], ['queued', 'queued'], ['pr', 'in PR'], ['done', 'done']];

function listTpl(s) {
  const p = pj();
  follow(s);
  if (!p.loaded && !s.loading) { s.loading = true; p.load().catch(() => {}); }
  return html`<screen title="Projects" subtitle="a sandbox, its repos and task conversations" style="list" refreshable @refresh=${() => p.load()}>
    ${p.supported ? html`<toolbar><button icon="plus" @tap=${() => { p.newProject(); push({ kind: 'project-new' }); }}>New project</button></toolbar>` : nothing}
    ${errTpl(p)}
    ${!p.supported ? html`<section><empty icon="folder" title="This agent has no Projects yet"/></section>`
      : repeat(p.sections(), (x) => x.key, (x) => html`<section title=${x.title}>
        ${x.items.length ? repeat(x.items, (pv) => pv.id, (pv) => projRow(pv))
          : html`<empty title=${p.loaded ? 'none yet — a project groups tasks around one sandbox and its repos' : 'loading…'}/>`}
      </section>`)}
  </screen>`;
}

// a project atop your conversations: its name, what really waits on you (the badge, danger — #56;
// else the pull requests ready for review, warn), the ready ones beside it, what is under way
function pinRow(pv) {
  const w = pinWords(pv);
  const ready = w.asks && w.ready ? `${w.ready} ready for review` : '';
  const n = w.asks || w.ready;
  return html`<row title=${w.name} subtitle=${[ready, w.detail].filter(Boolean).join(' · ') || nothing} icon=${w.team ? 'people' : 'folder'}
    badge=${n ? String(n) : nothing} tone=${w.asks && w.split ? 'danger' : n ? 'warn' : nothing} nav @tap=${() => openProject(pv.id)}/>`;
}

function projRow(pv) {
  const s = needSplit(pv); // what really waits on you first (#56)
  const counts = [...(s.split && s.asks ? [`${s.asks} waiting for you`] : []), ...COUNTS.filter(([k]) => (pv.counts || {})[k]).map(([k, w]) => `${pv.counts[k]} ${w}`)];
  const need = s.asks || s.ready;
  const kind = pv.kind === 'team' ? 'team' : pv.kind === 'membership' ? 'team · yours' : '';
  const sub = [(pv.repos || []).map((r) => r.repo).join(', ') || 'no repos yet', pv.slots ? `${pv.slots.used}/${pv.slots.max} at work` : '', ...counts,
    pv.owner && pv.level !== 'owner' ? `${pv.owner}'s` : ''].filter(Boolean).join(' · ');
  return html`<row title=${pv.name} subtitle=${sub} detail=${[kind, pv.state !== 'active' ? pv.state : ''].filter(Boolean).join(' · ') || nothing}
    icon="folder" badge=${need ? String(need) : nothing} tone=${s.asks && s.split ? 'danger' : need ? 'warn' : nothing} nav @tap=${() => openProject(pv.id)}/>`;
}

// --- a project ------------------------------------------------------------------------------------------

function projectTpl(s) {
  const app = ctx.app;
  const p = pj();
  follow(s);
  const pv = p.find(s.pid);
  if (!pv) return html`<screen title="Project" style="list">${errTpl(p)}<section><progress label="loading…"/></section></screen>`;
  if (pv.kind === 'team') { s.kind = 'project-team'; ctx.paint(); } // a team project's definition (known once read): the team board
  if (top() === s && p.tab !== 'board') p.tab = 'board'; // back from its settings: the board's reads only
  const c = can(pv);
  const list = p.taskList(pv.id);
  const team = projectTeam(app);
  const def = pv.kind === 'membership' ? team.definitionOf(pv) : null;
  const review = team.ensureReview(pv);
  const f = projectFeed(app);
  const recent = f.items(pv.id, 4); // the latest activity once its screen has read it (the read walks oldest first)
  const ov = overviewOf(pv, s); // null: a backend without the overview — the screen as before (D214)
  return html`<screen title=${pv.name} subtitle=${[(pv.repos || []).map((r) => r.repo).join(', '), pv.state !== 'active' ? pv.state : ''].filter(Boolean).join(' · ')}
      style="list" refreshable @refresh=${() => { p.refresh(pv.id); if (ov) projectOverview(app).load(pv.id); if ((f.feeds.get(+pv.id) || {}).loaded) f.load(pv.id); }}>
    <toolbar>
      ${c.act ? html`<menu icon="plus" label="New">
        <button icon="pencil" @tap=${() => { p.newTask(); push({ kind: 'project-task-new', pid: pv.id }); }}>New task</button>
        <button icon="list" ?disabled=${!(pv.repos || []).length} @tap=${() => { p.openPicker(); push({ kind: 'project-issues', pid: pv.id }); }}>From issues…</button>
        <button icon="bolt" @tap=${() => p.warm(pv.id)}>Warm</button>
        ${forkBaseOffer(pv) ? html`<button icon="box" confirm=${{ title: 'Snapshot the project\'s sandbox for big tasks now?', message: 'It stops while the snapshot is taken; running tasks wait.', label: 'Fork base now' }}
          @tap=${() => p.act(() => forkBase(pv.id), pv.id).then((r) => { if (r) { p.flash = 'The fork base is being taken.'; p.changed(); } })}>Fork base now</button>` : nothing}
      </menu>` : nothing}
      <button icon="gear" @tap=${() => { p.showTab('settings'); push({ kind: 'project-settings', pid: pv.id }); }}>Project settings</button>
    </toolbar>
    ${errTpl(p)}
    ${needsSectionTpl(pv)}
    ${pv.kind === 'membership' ? html`<section title="Team project">
      ${def ? html`<row title="The team's board" subtitle=${def.name} icon="people" nav @tap=${() => openProject(def.id)}/>` : nothing}
      ${review ? html`<row title="Review the team project's changes" subtitle="until you accept, your tasks run what you accepted before" icon="warning" tone="warn" nav
        @tap=${() => push({ kind: 'project-team', pid: pv.id })}/>` : nothing}
    </section>` : nothing}
    ${ov ? statusTpl(pv, s, ov) : coordTpl(pv, c, s, null)}
    <section><picker style="segmented" value=${p.filter.mine ? 'mine' : 'all'} options=${[{ value: 'all', label: 'All tasks' }, { value: 'mine', label: 'Mine' }]}
      @change=${(e) => p.setFilter({ mine: e.value === 'mine' })}/>
      <field kind="search" placeholder="Find a task" submit="search" value=${p.filter.q} @submit=${(e) => p.setFilter({ q: e.value || '' })}
        @change=${(e) => { if ((e.value || '') !== p.filter.q) p.setFilter({ q: e.value || '' }); }}/></section>
    ${list.err ? html`<section><notice tone="danger" text=${list.err}/></section>` : nothing}
    ${repeat(p.board(pv.id), (col) => col.key, (col) => html`<section title=${`${col.title}${col.tasks.length ? ` (${col.tasks.length})` : ''}`}>
      ${col.tasks.length ? repeat(col.tasks, (t) => t.n, (t) => taskRow(pv, t, c, ov)) : html`<empty text="none"/>`}
    </section>`)}
    ${list.next ? html`<section><row title="More tasks" icon="expand" @tap=${() => p.tasks(pv.id, p.filter, true)}/></section>` : nothing}
    <section title="Activity" footer="Text from the scm provider is shown as it came, plain.">
      ${recent.length ? repeat(recent, (ev) => ev.id, (ev) => eventRow(pv, ev)) : nothing}
      <row title="All activity" icon="clock" nav @tap=${() => push({ kind: 'project-events', pid: pv.id })}/>
      <row title="Wiki" subtitle="the project's shared notes" icon="doc" nav @tap=${() => push({ kind: 'project-wiki', pid: pv.id })}/>
    </section>
    ${ov ? coordTpl(pv, c, s, ov) : nothing}
    ${ov ? aboutSheet(s, ov) : nothing}
    ${coordComposerTpl(pv, c)}
  </screen>`;
}

// a task's row: a tap opens its conversation (whose More menu comes back here);
// its agent from the overview (ov), else a coding agent's name
function taskRow(pv, t, c, ov) {
  const w = cardWords(t);
  const sub = [w.state.text, w.branch ? `branch ${w.branch}` : '', ...w.prs.map(prWords), agentWords(ov, t.n) || w.agent, w.size ? 'big' : '', ...(ext.card(t) || [])].filter(Boolean).join(' · ');
  const open = !['merged', 'closed', 'done', 'deleted'].includes(t.phase) && !['cancelled', 'failed'].includes(t.state);
  return html`<row title=${`${w.n} ${w.title}`} subtitle=${sub} tone=${TONE[w.state.tone] || nothing} ?disabled=${!t.run} nav @tap=${() => { if (t.run) ctx.app.select(t.run); }}>
    ${c.act && open ? html`<actions><button icon="stop" role="destructive" confirm=${{ title: `Cancel task ${w.n}?`, message: 'It stops, with everything it started; its conversation, worktree and branch stay.', label: 'Cancel task', destructive: true }}
      @tap=${() => pj().cancelTask(pv.id, t.n)}>Cancel task</button></actions>` : nothing}
  </row>`;
}

// the coordinator, a small chat (D209): open it (made on first use), what it
// does now, its last messages (the screen's composer writes to it, D212).
// With the overview (ov, D214) it ends the screen, its state as the badge: its
// goal (idle: what wakes it), its newest COORD_MORE messages, then Open.
const ABOUT = 'Your agent for this project: it makes tasks, follows them and tells you what needs you — it never merges or answers a task\'s question for you.';
const COORD_MORE = 8;
function coordTpl(pv, c, s, ov) {
  if (pv.kind === 'team' || !c.act) return nothing;
  const f = projectFeed(ctx.app);
  const k = f.coord(pv.id);
  if (k.missing) return nothing;
  const pr = f.preview(pv.id, ov ? COORD_MORE : COORD_TAIL);
  const st = pr.shown ? pr.state : null;
  const open = async () => { const run = await f.openCoordinator(pv.id); if (run && run.id) ctx.app.select(run.id); };
  const d = ov && ov.data && ov.data.coordinator ? coordWords(ov.data) : null;
  const row = html`<row title="Open the coordinator" icon="agent" nav ?disabled=${!!k.busy} @tap=${open}
    subtitle=${[pr.shown && pr.line, d && d.model, d && d.tokensToday && `${fmtTokens(d.tokensToday)} tokens today`].filter(Boolean).join(' · ') || nothing}
    tone=${st && st.tone === 'warn' ? 'warn' : st && st.tone === 'bad' ? 'danger' : nothing}/>`;
  const badge = ov ? coordBadge(pr, ov) : st && st.word;
  return html`<section title="Coordinator" badge=${badge || nothing} footer=${k.err || k.note || ABOUT}>
    ${ov ? goalTpl(s, ov) : row}
    ${pr.shown ? coordPreviewTpl(pv, pr, f) : nothing}
    ${ov ? row : nothing}
    ${k.busy === 'send' ? html`<progress label="Sending…"/>` : nothing}
  </section>`;
}

// the composer docked at the bottom of a project's screen (D212): a message
// to its coordinator — never a new chat; no attachments (it takes text)
function coordComposerTpl(pv, c) {
  if (pv.kind === 'team' || !c.act) return nothing;
  const f = projectFeed(ctx.app);
  const k = f.coord(pv.id);
  if (k.missing) return nothing;
  const pr = f.preview(pv.id);
  return html`<composer value=${k.text} placeholder=${coordPlaceholder(pr, !wide())}
    @input=${(e) => { k.text = e.value; }} @send=${(e) => f.messageCoordinator(pv.id, e.value)}/>`;
}

// one message of the preview: the chat's message, without its actions (its chat has them)
const saidTpl = (b) => (b.k === 'user' ? html`<message role="user" text=${b.text}/>`
  : html`<message role="assistant" markdown ?streaming=${b.k === 'draft'} text=${b.text}/>`);

// its last messages, or why there are none
function coordPreviewTpl(pv, pr, f) {
  if (!pr.run) return pr.err ? html`<notice tone="danger" text=${pr.err}/>` : pr.loading ? html`<progress label="loading…"/>` : html`<text tone="muted">${COORD_EMPTY}</text>`;
  if (!pr.tail) {
    return pr.err ? html`<notice tone="danger" text=${`Couldn't read its latest messages: ${pr.err}`}/>
      <button icon="refresh" @tap=${() => f.readTail(pv.id)}>Read them again</button>` : html`<progress label="loading…"/>`;
  }
  return html`${pr.more ? html`<text style="footnote" tone="muted">… earlier messages are in its chat</text>` : nothing}
    ${pr.tail.length ? repeat(pr.tail, (b) => b.id, saidTpl) : html`<text tone="muted">Nothing said yet.</text>`}`;
}

function eventRow(pv, ev) {
  const w = feedWords(ev);
  const t = w.n ? (pj().taskList(pv.id).items || []).find((x) => x.n === w.n) : null;
  return html`<row title=${`${w.n ? `#${w.n} ` : ''}${w.label}`} subtitle=${[w.text, w.by, when(w.when)].filter(Boolean).join(' · ')}
    tone=${TONE[w.tone] || nothing} badge=${w.wake ? 'coordinator' : nothing} ?nav=${!!(t && t.run)}
    @tap=${() => { if (t && t.run) ctx.app.select(t.run); else if (w.url) openUrl(w.url); }}>
    ${w.url ? html`<actions><button icon="external" @tap=${() => openUrl(w.url)}>Open on the platform</button></actions>` : nothing}
  </row>`;
}

function eventsTpl(s) {
  const pv = pj().find(s.pid);
  const f = projectFeed(ctx.app);
  const st = f.ensure(s.pid);
  const items = f.items(s.pid);
  return html`<screen title="Activity" subtitle=${pv ? pv.name : ''} style="list" refreshable @refresh=${() => f.load(s.pid)}>
    ${st.err ? html`<section><notice tone="danger" text=${st.err}/></section>` : nothing}
    ${st.more ? html`<section footer="The oldest of this project's events are shown — newer ones wait."><row title="Read newer" icon="refresh" ?disabled=${st.loading} @tap=${() => f.load(s.pid)}/></section>` : nothing}
    <section footer="Tasks, workspaces, pull requests, CI and reviews, newest first. Text from the scm provider is shown as it came, plain.">
      ${items.length ? repeat(items, (ev) => ev.id, (ev) => eventRow(pv || { id: s.pid }, ev)) : html`<empty title=${st.loading ? 'loading…' : 'Nothing has happened yet'}/>`}
    </section>
  </screen>`;
}

// --- a new task ---------------------------------------------------------------------------------------------

function agentOptions(pv) {
  const app = ctx.app;
  app.harness.ensure();
  return agentChoices(pv, app.harness.catalog.harnesses || []).filter((c) => !c.disabled).map((c) => ({ value: c.value, label: c.label }));
}
const SIZES = [{ value: 'small', label: 'small — a worktree in the project\'s sandbox' }, { value: 'big', label: 'big — its own sandbox, forked' }];

function taskFormTpl(s) {
  const p = pj();
  const pv = p.find(s.pid);
  const f = p.taskForm;
  if (!f || !pv) return html`<screen title="New task" style="form"><section><empty title="closed"/></section></screen>`;
  const submit = async () => { const r = await p.submitTask(); if (r) drop(s); };
  return html`<screen title="New task" subtitle=${pv.name} style="form">
    <toolbar><button role="primary" ?busy=${f.busy} @tap=${submit}>Create</button></toolbar>
    ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
    <section>
      <field kind="multiline" label="What to do" placeholder="Fix the login redirect loop on Safari" value=${f.text} @input=${(e) => { f.text = e.value; }}/>
      <field label="Title" placeholder="from what to do" value=${f.title} @input=${(e) => { f.title = e.value; }}/>
      <picker label="Size" value=${f.size} options=${SIZES} @change=${(e) => p.setTask('size', e.value)}/>
      <picker label="Who works on it" value=${f.agent || ''} options=${agentOptions(pv)} @change=${(e) => p.setTask('agent', e.value)}/>
    </section>
    ${(pv.repos || []).length > 1 ? html`<section title="Repos">${repeat(pv.repos, (r) => r.slug, (r) => html`<toggle label=${r.repo} value=${f.repos.includes(r.slug)}
      @change=${(e) => p.setTask('repos', e.value ? [...f.repos, r.slug] : f.repos.filter((x) => x !== r.slug))}/>`)}</section>` : nothing}
  </screen>`;
}

// --- tasks from issues ----------------------------------------------------------------------------------------

function pickerTpl(s) {
  const p = pj();
  const pv = p.find(s.pid);
  const k = p.picker;
  if (!k || !pv) return html`<screen title="From issues" style="list"><section><empty title="closed"/></section></screen>`;
  const make = async () => { const r = await p.submitBatch(); if (r && !p.picker) drop(s); };
  return html`<screen title="From issues" subtitle=${pv.name} style="list" search=${k.q} @search=${(e) => { p.setPicker('q', e.value || ''); p.searchIssues(); }}>
    <toolbar><button role="primary" ?disabled=${!k.picked.size || k.loading} ?busy=${k.loading && !!k.picked.size} @tap=${make}>${`Make ${k.picked.size || ''} task${k.picked.size === 1 ? '' : 's'}`}</button></toolbar>
    ${k.err ? html`<section><notice tone="danger" text=${k.err}/></section>` : nothing}
    ${k.result ? html`<section><notice tone="warn" text=${k.result.errors.map((x) => `${x.issue && typeof x.issue === 'object' ? `${x.issue.repo}#${x.issue.number}` : x.issue}: ${x.error}`).join('\n')}/></section>` : nothing}
    <section footer="Issue text comes from the issue tracker — anyone may have written it. Each picked issue becomes a task (at most 20 at once).">
      <picker label="Repo" value=${k.repo} options=${(pv.repos || []).map((r) => ({ value: r.repo, label: r.repo }))} @change=${(e) => { p.setPicker('repo', e.value); p.searchIssues(); }}/>
      <picker style="segmented" value=${k.state} options=${[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }]} @change=${(e) => { p.setPicker('state', e.value); p.searchIssues(); }}/>
      <picker label="Size" value=${k.size} options=${SIZES.map((x) => ({ ...x, label: x.value }))} @change=${(e) => p.setPicker('size', e.value)}/>
      <picker label="Who works on them" value=${k.agent || ''} options=${agentOptions(pv)} @change=${(e) => p.setPicker('agent', e.value)}/>
    </section>
    <section title="Issues">
      ${repeat(k.items, (i) => `${i.repo}#${i.number}`, (i) => {
        const w = issueWords(i);
        const on = k.picked.has(w.key);
        return html`<row title=${`#${w.number} ${w.title}`} subtitle=${[w.labels.join(', '), w.body].filter(Boolean).join(' · ') || nothing}
          icon=${on ? 'check' : 'minus'} ?selected=${on} ?disabled=${!on && k.picked.size >= 20} @tap=${() => p.togglePick(i)}>
          ${w.url ? html`<actions><button icon="external" @tap=${() => openUrl(w.url)}>Open the issue</button></actions>` : nothing}
        </row>`;
      })}
      ${k.loading ? html`<progress label="loading…"/>` : !k.items.length ? html`<empty title="No issues"/>` : nothing}
      ${k.next && !k.loading ? html`<row title="More" icon="expand" @tap=${() => p.searchIssues(true)}/>` : nothing}
    </section>
  </screen>`;
}

