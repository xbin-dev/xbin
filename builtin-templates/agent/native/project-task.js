// native/project-task.js — a project's conversation in the native view
// (API.md §Projects in the UI), the web's project-chips.js with the app's
// primitives, and "Make this a project…":
//
//   toolbar   on a task: one menu titled with its branch — the branch, each
//             pull request with its state (↗ opens it on the platform; its
//             checks only while the task has no CI summary), the setup
//             outcome, and Open PR (once the backend is known to have it,
//             confirmed) — a phone's bar holds little
//   subtitle  "‹project› #n" (a coordinator: "‹project› · coordinator")
//   menu      Project: ‹name› — back to its board; Retry the workspace when
//             it failed; on a root conversation of yours with a sandbox and
//             no project, Make this a project…
//   end       the prep card while its workspace is prepared — what is under
//             way, a step per repo — and the sign-in card: the provider's
//             page and code, only for the person who must sign in, polled
//             until done, then the task is looked at again
//   composer  Retry, when the workspace failed
//   task      the Task screen's Project section
//   screen    'project-upgrade' {run}: the sandbox's repos (those a provider
//             serves picked; an ssh or credentialed remote switched to
//             https), a name, the branch kept or a new one — Make the project
//
// The words are model/project-task.js and model/project-upgrade.js; the
// calls app.projects (model/projects.js) and projectUpgrade(app).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, fail, push, ui } from './ui.js';
import { taskOf, taskChips, prepCard, prButton, crumb, taskSection } from '../model/project-task.js';
import { homeOf } from '../model/homes.js';
import { upgradeOffer, projectUpgrade } from '../model/project-upgrade.js';

const pj = () => ctx.app.projects;
const talk = (v) => ctx.app.rules.access(v).talk;
const polled = new Set();    // sign-in polls started, by pollId
const refreshed = new Set(); // `${runId}:${pollId}`: a task refreshed after that sign-in was done
const acts = new Map();      // runId → {busy, note, err}

export function openUrl(url) {
  if (!/^https:\/\//i.test(String(url || ''))) return;
  Promise.resolve().then(() => globalThis.xbin.native.open(url)).catch((e) => fail(e));
}

async function act(v, what, opts) {
  const id = v.run.id;
  acts.set(id, { busy: what, note: '', err: false });
  ctx.paint();
  let note = '', err = false;
  try { await pj().taskAction(id, what, opts); note = what === 'pr' ? 'The pull request is being opened…' : 'Its failed steps run again.'; } catch (e) {
    err = !(e.status === 404 && what === 'pr');
    note = err ? e.message : '';
  }
  acts.set(id, { busy: '', note: err ? '' : note, err });
  if (err) fail(note); // said at the top of the screen
  ctx.paint();
}

ext.register({
  toolbar(v) {
    const t = taskOf(v);
    if (!t || !ctx.app) return null;
    const pv = v.project ? pj().ensure(v.project.id) : null;
    const chips = taskChips(v, pv);
    const pr = prButton(v, pj().prRouteAt(homeOf(v.run.id), v.run.id), talk(v));
    if (!chips.length && !pr.shown) return null;
    const busy = (acts.get(v.run.id) || {}).busy;
    const label = (chips.find((c) => c.kind === 'pr') || chips[0] || { text: 'Pull request' }).text;
    return html`<menu icon="branch" label=${label}>
      ${chips.map((c) => html`<button icon=${c.kind === 'branch' ? 'branch' : c.kind === 'pr' ? 'link' : c.tone === 'bad' ? 'warning' : 'check'}
        ?disabled=${!/^https:/i.test(c.url || '')} @tap=${() => openUrl(c.url)}>${c.url ? `${c.text} ↗` : c.text}</button>`)}
      ${pr.shown ? html`<divider/><button icon="upload" ?disabled=${pr.disabled || busy === 'pr'}
        confirm=${{ title: `Open a pull request for ${t.branch}?`, message: 'People outside xbin will see it.', label: 'Open PR' }}
        @tap=${() => act(v, 'pr', {})}>${busy === 'pr' ? 'Opening…' : 'Open PR'}</button>` : nothing}
    </menu>`;
  },
  subtitle(v) {
    const c = crumb(v);
    if (!c) return null;
    const name = (v.project && v.project.name) || `project ${c.pid}`;
    return v.project.role === 'coordinator' ? `${name} · coordinator` : v.project.n ? `${name} #${v.project.n}` : name;
  },
  menu(v) {
    if (!ctx.app) return null;
    const c = crumb(v);
    const card = prepCard(v, ctx.app.me);
    const up = upgradeOffer(v);
    if (!c && !up.shown) return null;
    return html`${c ? html`<button icon="folder" @tap=${() => ctx.app.openProjects(c.pid)}>${`Project: ${(v.project && v.project.name) || c.pid}`}</button>` : nothing}
      ${card && card.retry && talk(v) ? html`<button icon="refresh" @tap=${() => act(v, 'retry')}>Retry the workspace</button>` : nothing}
      ${up.shown ? html`<button icon="plus" @tap=${() => openUpgrade(v.run.id)}>Make this a project…</button>` : nothing}`;
  },
  end: (v) => prepTpl(v),
  composer(v) {
    if (!ctx.app) return null;
    const card = prepCard(v, ctx.app.me);
    if (!card || !card.retry || !talk(v)) return null;
    const busy = (acts.get(v.run.id) || {}).busy === 'retry';
    return { tpl: () => html`<button icon="refresh" ?busy=${busy} @tap=${() => act(v, 'retry')}>Retry the workspace</button>` };
  },
  task(s) {
    const v = ctx.app.session.merged(s.run) || ctx.app.session.current();
    const x = taskSection(v);
    if (!x) return null;
    return html`<section title="Project" footer=${`${x.size} · repos: ${x.repos}${x.ports ? ` · ports ${x.ports}` : ''}`}>
      <row title=${x.project} subtitle=${`task #${x.n}`} icon="folder" nav @tap=${() => ctx.app.openProjects(x.pid)}/>
      ${x.issue ? html`<row title=${x.issue} subtitle="the issue it came from" icon="link" ?disabled=${!/^https:/i.test(x.issueUrl)} @tap=${() => openUrl(x.issueUrl)}/>` : nothing}
      ${repeat(x.checkouts, (c) => c.repo, (c) => html`<row title=${c.repo} subtitle=${`${c.path} (${c.mode}, ${c.state})`} mono="subtitle" icon="branch"/>`)}
    </section>`;
  },
  screen: (s) => (s.kind === 'project-upgrade' ? upgradeTpl(s) : null),
});

// --- the prep and sign-in cards ------------------------------------------------------------------

const NTONE = { run: 'info', bad: 'danger', warn: 'warn', ok: 'ok', idle: 'muted' };
const STONE = { run: 'accent', bad: 'danger', warn: 'warn', ok: 'ok', idle: 'muted' };

function prepTpl(v) {
  if (!v || !ctx.app) return null;
  const card = prepCard(v, ctx.app.me);
  if (!card) return null;
  const pollId = card.signin && card.signin.pollId;
  const pv = card.signin && v.project ? pj().ensure(v.project.id) : null;
  if (pollId && !polled.has(pollId) && pv && pv.scm) { polled.add(pollId); pj().pollSignin(pv.scm, card.signin); }
  const st0 = pv && pv.scm ? pj().signinOf(pv.scm) : null;
  const st = st0 && pollId && st0.pollId === pollId ? st0 : null; // this card's sign-in only
  if (st && st.state === 'done' && !refreshed.has(`${v.run.id}:${pollId}`)) { refreshed.add(`${v.run.id}:${pollId}`); pj().taskAction(v.run.id, 'refresh').catch(() => {}); }
  const words = !st ? 'Only you see this code. The task goes on once you approve it there.'
    : st.state === 'done' ? 'Signed in — the task goes on.'
    : st.state === 'error' ? `Couldn't learn whether you signed in: ${st.err}.`
    : ['denied', 'expired'].includes(st.state) ? `The sign-in was ${st.state}.`
    : 'Only you see this code. The task goes on once you approve it there.';
  const a = acts.get(v.run.id) || {};
  return html`<notice tone=${NTONE[card.tone] || 'info'} title=${card.title} text=${[card.step, card.detail, card.error].filter(Boolean).join(' — ') || ' '}/>
    ${repeat(card.steps, (x) => x.repo, (x) => html`<step glyph=${x.glyph} tone=${STONE[x.tone] || 'muted'} text=${`${x.repo}: ${x.text}${x.error ? ' — ' + x.error : ''}`}/>`)}
    ${card.retry && talk(v) ? html`<text tone="muted">Retry the workspace from the composer or ⋯.</text>` : nothing}
    ${card.signin ? html`<notice tone="warn" title="To push, this task needs your own sign-in" text=${words}/>
      <text style="title3" mono selectable>${card.signin.userCode}</text>
      ${/^https:\/\/[^\s()<>[\]]+$/i.test(card.signin.url) ? html`<markdown source=${`Open [${card.signin.url}](${card.signin.url}) and enter the code above.`} @link=${(e) => openUrl(e.href)}/>` : nothing}`
    : card.signinElsewhere ? html`<notice tone="muted" text=${card.signinElsewhere}/>` : nothing}
    ${a.note && !a.err && !a.busy ? html`<text tone="muted">${a.note}</text>` : nothing}`;
}

// --- "Make this a project…" ---------------------------------------------------------------------------

export function openUpgrade(runId) {
  projectUpgrade(ctx.app).open(runId);
  push({ kind: 'project-upgrade', run: runId });
}

function upgradeTpl(s) {
  const app = ctx.app;
  const u = projectUpgrade(app);
  const f = u.form(s.run);
  if (!f) return html`<screen title="Make this a project" style="form"><section><progress label="loading…"/></section></screen>`;
  const id = f.runId;
  const close = () => { const i = ui.stack.indexOf(s); if (i >= 0) ui.stack.splice(i); u.close(id); ctx.paint(); };
  if (f.done) {
    const pv = f.done.project;
    return html`<screen title="Made a project" style="form">
      <toolbar><button role="primary" @tap=${close}>Done</button></toolbar>
      <section><notice tone="ok" text=${`This conversation is task #${(f.done.task && f.done.task.n) || 1} of ${pv ? pv.name : 'the project'}: its branch and pull requests show in its bar, and the project's board holds it.`}/>
        ${pv ? html`<row title=${`Open ${pv.name}`} icon="folder" nav @tap=${() => { u.close(id); app.openProjects(pv.id); }}/>` : nothing}</section>
    </screen>`;
  }
  const picked = f.candidates.filter((c) => f.picked.has(c.path));
  return html`<screen title="Make this a project" subtitle=${f.sandbox || ''} style="form">
    <toolbar><button role="primary" ?busy=${f.busy} ?disabled=${f.loading || !picked.length} @tap=${() => u.submit(id)}>Make it</button></toolbar>
    ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
    <section footer="The conversation becomes the project's first task, its sandbox the project's. A project's tasks push with credentials of their own, never what a remote held.">
      ${f.loading ? html`<progress label="reading the sandbox's repos…"/>` : nothing}
      ${!f.loading && !f.err && !f.candidates.length ? html`<empty icon="folder" title=${`No git repo in ${f.cwd || 'its working directory'}`}/>` : nothing}
    </section>
    ${f.candidates.length ? html`<section title="Repos">
      ${repeat(f.candidates, (c) => c.path, (c) => html`<toggle label=${c.title} value=${f.picked.has(c.path)} ?disabled=${!c.usable} @change=${() => u.toggle(id, c.path)}/>
        <text tone=${c.usable ? 'muted' : 'warn'} style="footnote">${c.why ? `${c.detail} — ${c.why}` : c.detail}</text>
        ${c.https && f.picked.has(c.path) ? html`<toggle label="Switch its remote to https" value=${f.https.has(c.path)} @change=${() => u.toggleHttps(id, c.path)}/>` : nothing}`)}
    </section>` : nothing}
    ${picked.length ? html`<section title="The project">
      <field label="Name" value=${f.name} @input=${(e) => { f.name = e.value; }}/>
      <picker label="Its branch" value=${f.branch} options=${[{ value: 'keep', label: 'keep the branch it is on' }, { value: 'new', label: 'a new branch for the task' }]}
        @change=${(e) => u.set(id, 'branch', e.value)}/>
    </section>` : nothing}
  </screen>`;
}
