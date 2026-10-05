// native/projects.js — Projects in the native view (API.md §Projects in the
// UI), the web's projects.js and project-new.js with the app's primitives,
// as screens pushed over home:
//
//   drawer               the drawer's Projects row (after Automations), with
//                        how many tasks need you
//   'projects'           the list — yours, team projects, archived — New project
//   'project' {pid}      a project: the coordinator (open it, write to it), the
//                        board as a section per column — a row per task: #n,
//                        title, state, branch, its PRs (their checks only while
//                        it has no CI summary), the words other modules add
//                        (ext.card: CI's) — a tap opens its conversation, a
//                        swipe cancels it; a search field and Mine; the latest
//                        activity; ⊕ New task, From issues…, Warm; ⚙ Settings
//                        (native/project-settings.js); your half of a team
//                        project leads to the team's board and its changes
//                        to review (native/project-team.js)
//   'project-task-new'   what to do, a title, small or big, who works on it,
//                        which repos
//   'project-issues'     the batch picker: a repo's issues (plain, untrusted),
//                        up to 20 picked, size and who works on them
//   'project-events'     the project's activity, newest first
//   'project-new'        the new-project form (provider and sign-in, repos,
//                        name, sandbox, the basics; a team project's
//                        definition from your own space)
//
// app.page 'projects' (the drawer's row, #proj, #proj=<id>, a task's way
// back) puts the list — and the project open — on the stack; going back
// to home leaves the page. The state is app.projects (model/projects.js),
// projectFeed(app) (model/project-feed.js) and projectTeam(app).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, ui, push, top, when } from './ui.js';
import { can, agentChoices, repoSlug } from '../model/projects.js';
import { cardWords } from '../model/project-task.js';
import { projectFeed, feedWords } from '../model/project-feed.js';
import { projectTeam } from '../model/project-team.js';
import { partitionState } from '../model/partition.js';
import { projectSandbox } from '../model/sandboxes.js';
import { openUrl } from './project-task.js';
import { forkBaseOffer, forkBase } from '../model/project-upgrade.js';
import { signinTpl } from './project-settings.js';

export const TONE = { run: 'accent', ok: 'ok', bad: 'danger', warn: 'warn', idle: 'muted' };
const pj = () => ctx.app.projects;
const errTpl = (p) => html`${p.err ? html`<section><notice tone="danger" text=${p.err}/></section>` : nothing}
  ${p.flash ? html`<section><notice tone="ok" text=${p.flash}/></section>` : nothing}`;
const drop = (s) => { const i = ui.stack.indexOf(s); if (i >= 0) ui.stack.splice(i); ctx.paint(); };

// --- the page on the stack ---------------------------------------------------------------------------

let wired = false;
// wire: once the app exists, app.page 'projects' puts the list on the stack
// (after the page's own open(pid) has run: the project too).
function wire() {
  const app = ctx.app;
  if (wired || !app) return;
  wired = true;
  app.on('page', () => Promise.resolve().then(sync));
  sync();
}
function sync() {
  const app = ctx.app;
  if (app.page !== 'projects' || app.sel != null || ui.stack.some((s) => s.kind === 'projects')) return;
  ui.stack.length = 0;
  ui.stack.push({ kind: 'projects' });
  const pid = app.projects.opened;
  if (pid != null) ui.stack.push(screenOf(pid));
  ctx.paint();
}
const screenOf = (pid) => ((pj().find(pid) || {}).kind === 'team' ? { kind: 'project-team', pid: +pid } : { kind: 'project', pid: +pid });

// openProject pushes a project's screen and opens it in the model.
export function openProject(pid) {
  push(screenOf(pid));
  pj().open(+pid);
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
  drawer(close) {
    wire();
    const p = ctx.app.projects;
    if (!p.supported) return null;
    const n = p.needsYou();
    return html`<row title="Projects" icon="folder" badge=${n ? String(n) : nothing} tone=${n ? 'warn' : nothing} nav @tap=${() => { close(); ctx.app.openProjects(); }}/>`;
  },
  toolbar() { wire(); return null; }, // home's and a chat's bar: drawn at start, so the page is followed from the first paint
  screen(s) {
    switch (s.kind) {
      case 'projects': return listTpl(s);
      case 'project': return projectTpl(s);
      case 'project-task-new': return taskFormTpl(s);
      case 'project-issues': return pickerTpl(s);
      case 'project-events': return eventsTpl(s);
      case 'project-new': return newTpl(s);
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

function projRow(pv) {
  const counts = COUNTS.filter(([k]) => (pv.counts || {})[k]).map(([k, w]) => `${pv.counts[k]} ${w}`);
  const need = (pv.counts || {})['needs-you'] || 0;
  const kind = pv.kind === 'team' ? 'team' : pv.kind === 'membership' ? 'team · yours' : '';
  const sub = [(pv.repos || []).map((r) => r.repo).join(', ') || 'no repos yet', pv.slots ? `${pv.slots.used}/${pv.slots.max} at work` : '', ...counts,
    pv.owner && pv.level !== 'owner' ? `${pv.owner}'s` : ''].filter(Boolean).join(' · ');
  return html`<row title=${pv.name} subtitle=${sub} detail=${[kind, pv.state !== 'active' ? pv.state : ''].filter(Boolean).join(' · ') || nothing}
    icon="folder" badge=${need && pv.state === 'active' ? String(need) : nothing} tone=${need && pv.state === 'active' ? 'warn' : nothing} nav @tap=${() => openProject(pv.id)}/>`;
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
  return html`<screen title=${pv.name} subtitle=${[(pv.repos || []).map((r) => r.repo).join(', '), pv.state !== 'active' ? pv.state : ''].filter(Boolean).join(' · ')}
      style="list" search=${p.filter.q} @search=${(e) => p.setFilter({ q: e.value || '' })} refreshable @refresh=${() => { p.refresh(pv.id); if (f.feed(pv.id).loaded) f.load(pv.id); }}>
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
    ${pv.kind === 'membership' ? html`<section title="Team project">
      ${def ? html`<row title="The team's board" subtitle=${def.name} icon="people" nav @tap=${() => openProject(def.id)}/>` : nothing}
      ${review ? html`<row title="Review the team project's changes" subtitle="until you accept, your tasks run what you accepted before" icon="warning" tone="warn" nav
        @tap=${() => push({ kind: 'project-team', pid: pv.id })}/>` : nothing}
    </section>` : nothing}
    ${coordTpl(pv, c)}
    <section><picker style="segmented" value=${p.filter.mine ? 'mine' : 'all'} options=${[{ value: 'all', label: 'All tasks' }, { value: 'mine', label: 'Mine' }]}
      @change=${(e) => p.setFilter({ mine: e.value === 'mine' })}/></section>
    ${list.err ? html`<section><notice tone="danger" text=${list.err}/></section>` : nothing}
    ${repeat(p.board(pv.id), (col) => col.key, (col) => html`<section title=${`${col.title}${col.tasks.length ? ` (${col.tasks.length})` : ''}`}>
      ${col.tasks.length ? repeat(col.tasks, (t) => t.n, (t) => taskRow(pv, t, c)) : html`<empty text="none"/>`}
    </section>`)}
    ${list.next ? html`<section><row title="More tasks" icon="expand" @tap=${() => p.tasks(pv.id, p.filter, true)}/></section>` : nothing}
    <section title="Activity" footer="Text from the scm provider is shown as it came, plain.">
      ${recent.length ? repeat(recent, (ev) => ev.id, (ev) => eventRow(pv, ev)) : nothing}
      <row title="All activity" icon="clock" nav @tap=${() => push({ kind: 'project-events', pid: pv.id })}/>
    </section>
  </screen>`;
}

// a task's row: a tap opens its conversation (whose ⋯ comes back here)
function taskRow(pv, t, c) {
  const w = cardWords(t);
  const sub = [w.state.text, w.branch ? `⎇ ${w.branch}` : '', ...w.prs.map((x) => x.text), w.agent, w.size ? 'big' : '', ...(ext.card(t) || [])].filter(Boolean).join(' · ');
  const open = !['merged', 'closed', 'done', 'deleted'].includes(t.phase) && !['cancelled', 'failed'].includes(t.state);
  return html`<row title=${`${w.n} ${w.title}`} subtitle=${sub} tone=${TONE[w.state.tone] || nothing} ?disabled=${!t.run} nav @tap=${() => { if (t.run) ctx.app.select(t.run); }}>
    ${c.act && open ? html`<actions><button icon="stop" role="destructive" confirm=${{ title: `Cancel task ${w.n}?`, message: 'It stops, with everything it started; its conversation, worktree and branch stay.', label: 'Cancel task', destructive: true }}
      @tap=${() => pj().cancelTask(pv.id, t.n)}>Cancel task</button></actions>` : nothing}
  </row>`;
}

// the coordinator: open it (made on first use), write to it
function coordTpl(pv, c) {
  if (pv.kind === 'team' || !c.act) return nothing;
  const f = projectFeed(ctx.app);
  const k = f.coord(pv.id);
  if (k.missing) return nothing;
  const open = async () => { const run = await f.openCoordinator(pv.id); if (run && run.id) ctx.app.select(run.id); };
  return html`<section title="Coordinator" footer=${k.err || k.note || 'Your agent for this project: it makes tasks, follows them and tells you what needs you — it never merges or answers a task\'s question for you.'}>
    <row title="Open the coordinator" icon="agent" nav ?disabled=${!!k.busy} @tap=${open}/>
    <field kind="multiline" placeholder="Ask it something: make tasks for the open bugs…" value=${k.text} @input=${(e) => { k.text = e.value; }}/>
    <button icon="send" ?busy=${k.busy === 'send'} @tap=${() => f.messageCoordinator(pv.id, k.text)}>Send to the coordinator</button>
  </section>`;
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
        const key = `${i.repo}#${i.number}`;
        const on = k.picked.has(key);
        return html`<row title=${`#${i.number} ${i.title}`} subtitle=${[(i.labels || []).join(', '), String(i.body || '').replace(/\s+/g, ' ').slice(0, 200)].filter(Boolean).join(' · ') || nothing}
          icon=${on ? 'check' : 'minus'} ?selected=${on} ?disabled=${!on && k.picked.size >= 20} @tap=${() => p.togglePick(i)}>
          ${/^https:/i.test(i.url || '') ? html`<actions><button icon="external" @tap=${() => openUrl(i.url)}>Open the issue</button></actions>` : nothing}
        </row>`;
      })}
      ${k.loading ? html`<progress label="loading…"/>` : !k.items.length ? html`<empty title="No issues"/>` : nothing}
      ${k.next && !k.loading ? html`<row title="More" icon="expand" @tap=${() => p.searchIssues(true)}/>` : nothing}
    </section>
  </screen>`;
}

// --- a new project ------------------------------------------------------------------------------------------------

function newTpl(s) {
  const app = ctx.app;
  const p = pj();
  const f = p.form;
  if (!f) return html`<screen title="New project" style="form"><section><empty title="closed"/></section></screen>`;
  app.sbx.ensure('', '');
  const list = app.sbx.listAt('');
  const team = partitionState() === 'global';
  const teamDef = partitionState() === 'user' && !!f.teamDef;
  // the manager picked (else the first), its default image and size, and internet when it
  // offers it — the web's choice (model/sandboxes.js projectSandbox): a project clones and fetches
  const sb = projectSandbox(f.sandbox, list.managers);
  const managers = sb.usable;
  const mine = (list.sandboxes || []).filter((x) => x.mine && (x.visibility === 'team') === team && !['deleting', 'archived', 'error'].includes(x.state));
  const provs = (p.scm && p.scm.providers) || [];
  const prov = p.provider(f.scm);
  const create = async () => {
    if (f.sandbox.mode !== 'pick') f.sandbox = { ...f.sandbox, provider: sb.provider, image: sb.image, size: sb.size, egress: sb.egress };
    const r = teamDef ? await projectTeam(app).saveTeam(p) : await p.saveProject();
    if (r && r.project) { drop(s); const i = ui.stack.findIndex((x) => x.kind === 'projects'); if (i >= 0) ui.stack.splice(i + 1); openProject(r.project.id); }
  };
  const sbModes = [...(team ? [{ value: 'none', label: 'none' }] : []), { value: 'new', label: 'a new sandbox' }, ...(mine.length ? [{ value: 'pick', label: 'one of your own' }] : [])];
  return html`<screen title="New project" style="form">
    <toolbar><button role="primary" ?busy=${f.busy} @tap=${create}>Create</button></toolbar>
    ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
    <section title="Where the code is" footer=${prov ? youWords(prov) : p.scm && p.scm.error ? p.scm.error : 'No scm provider is bound — bind one (such as the scm-github template) to this agent\'s scm slot.'}>
      ${provs.length ? html`<picker label="Provider" value=${f.scm} options=${provs.map((x) => ({ value: x.scm, label: `${x.title || x.scm}${x.error ? ' — ' + x.error : ''}` }))}
        @change=${(e) => { p.setForm('scm', e.value); p.searchRepos(); }}/>` : nothing}
      ${prov ? signinTpl(p, prov) : nothing}
    </section>
    ${f.scm ? html`<section title="Repos" footer="Each with an optional setup script for each task's checkout.">
      ${repeat(f.repos, (r) => r.repo, (r) => html`<row title=${r.repo} subtitle=${`→ ${repoSlug(r.repo)}`} mono="title" icon="folder">
        <actions><button icon="xmark" role="destructive" @tap=${() => p.dropFormRepo(r.repo)}>Leave it out</button></actions></row>
        <field label=${`Setup for ${r.repo}`} placeholder="npm ci" value=${r.setup} @input=${(e) => { r.setup = e.value; }}/>`)}
      <field kind="search" label="Find a repo you can reach" value=${f.q} submit="search" @input=${(e) => { f.q = e.value; }} @submit=${() => p.searchRepos()}/>
      ${repeat(f.results, (r) => `${r.owner}/${r.name}`, (r) => {
        const name = `${r.owner}/${r.name}`;
        const added = f.repos.some((x) => x.repo === name);
        return html`<row title=${name} subtitle=${[r.private ? 'private' : '', r.permission && r.permission !== 'admin' ? r.permission : '', r.archived ? 'archived' : ''].filter(Boolean).join(' · ') || nothing}
          icon=${added ? 'check' : 'plus'} ?disabled=${added || r.archived} @tap=${() => p.addFormRepo(name)}/>`;
      })}
      ${f.loading ? html`<progress label="loading…"/>` : !f.results.length && !f.err ? html`<empty title="No repos found"/>` : nothing}
      ${f.next && !f.loading ? html`<row title="More" icon="expand" @tap=${() => p.searchRepos(true)}/>` : nothing}
    </section>` : nothing}
    <section><field label="Name" placeholder="Web" value=${f.name} @input=${(e) => { f.name = e.value; }}/></section>
    ${partitionState() === 'user' ? html`<section title="Who it is for" footer=${teamDef ? 'A team project\'s definition holds the repos, the policy and its members; it has no tasks and no sandbox of yours — each member works on it in their own space. A seed sandbox can be set on its page.' : 'In your own space, with your own sandbox and sign-in.'}>
      <picker style="segmented" value=${teamDef ? 'team' : 'mine'} options=${[{ value: 'mine', label: 'Just you' }, { value: 'team', label: 'A team project' }]}
        @change=${(e) => p.setForm('teamDef', e.value === 'team')}/></section>` : nothing}
    ${teamDef ? nothing : html`<section title=${team ? 'Its seed sandbox' : 'Its sandbox'} footer=${team ? 'Members\' sandboxes fork from a seed shared with the team; it holds no sign-in.' : 'Its credentials are yours: a project works only in a private sandbox of your own.'}>
      <picker style="segmented" value=${f.sandbox.mode} options=${sbModes} @change=${(e) => p.setFormPart('sandbox', 'mode', e.value)}/>
      ${f.sandbox.mode === 'pick' ? html`<picker label="Sandbox" value=${f.sandbox.ref} options=${[{ value: '', label: 'pick one…' }, ...mine.map((x) => ({ value: x.ref, label: `${x.name} · ${x.state}` }))]}
        @change=${(e) => p.setFormPart('sandbox', 'ref', e.value)}/>`
        : f.sandbox.mode === 'new' ? (managers.length ? html`<picker label="Manager" value=${sb.provider} options=${managers.map((m) => ({ value: m.provider, label: m.title || m.provider }))}
          @change=${(e) => p.setFormPart('sandbox', 'provider', e.value)}/>` : html`<notice tone="warn" text="No sandbox manager is bound — bind one to this agent's sandboxes slot."/>`) : nothing}
    </section>`}
    <section title="How it works">
      <field kind="number" label="Tasks at work at once" value=${String(f.policy.maxTasks)} @input=${(e) => { f.policy.maxTasks = e.value; }}/>
      <picker label="Who answers tasks" value=${f.policy.engine} options=${[{ value: 'auto', label: 'auto — your pick per task' }, { value: 'builtin', label: 'the built-in agent' }, { value: 'harness', label: 'a coding agent' }]}
        @change=${(e) => p.setFormPart('policy', 'engine', e.value)}/>
      <picker label="Pull requests" value=${f.policy.autoPR} options=${[{ value: 'off', label: 'opened by hand (Open PR)' }, { value: 'draft', label: 'a draft when a task rests' }, { value: 'ready', label: 'ready for review when a task rests' }]}
        @change=${(e) => p.setFormPart('policy', 'autoPR', e.value)}/>
      ${partitionState() === 'legacy' || team || teamDef ? html`<picker label="Who can see it" value=${f.share.visibility} options=${[{ value: 'private', label: team || teamDef ? 'only the members you add next' : 'only you' }, { value: 'team', label: 'everyone who can open this agent' }]}
        @change=${(e) => p.setFormPart('share', 'visibility', e.value)}/>` : nothing}
    </section>
  </screen>`;
}

// what the provider says of you (the web's providerNoteTpl)
function youWords(prov) {
  const you = prov.you || {};
  const name = prov.title || prov.scm;
  return [you.person ? `You are ${you.person.login} there.` : partitionState() === 'user' ? `You haven't signed in to ${name}.` : 'Projects here work as the provider\'s bot.', ...(prov.notes || [])].join(' ');
}
