// native/project-new.js — a new project in the native view (API.md
// §Projects in the UI, "A new project"): the web's wizard
// (project-new.js) as screens pushed on the stack, one per step, over
// app.projects.form (model/projects.js; the steps model/project-wizard.js):
//
//   'project-new' {step}   Code (the provider and its sign-in; the repos —
//                          a search field filtering what you can reach, a
//                          tap on a result adds it; the chosen ones with
//                          a swipe to remove and a folded setup script),
//                          Name (and who it is for), Sandbox, How it works,
//                          Get started (the starter work), Review (each
//                          part with Edit, then Create)
//
// Next (the toolbar) pushes the next step that applies, disabled — the
// reason said below — until this one can go on; back is the stack's own.
// Edit on Review goes back to that step's screen. Create replaces the
// wizard with the project's board.
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, ui, push } from './ui.js';
import { repoSlug } from '../model/projects.js';
import { projectTeam } from '../model/project-team.js';
import { partitionState } from '../model/partition.js';
import { projectSandbox, EGRESS } from '../model/sandboxes.js';
import { wizardSteps, stepAt, nextStep, stepProblem, STARTER } from '../model/project-wizard.js';
import { signinTpl } from './project-settings.js';
import { openProject } from './projects.js';

const pj = () => ctx.app.projects;
const ENGINES = [{ value: 'auto', label: 'auto — your pick per task' }, { value: 'builtin', label: 'the built-in agent' }, { value: 'harness', label: 'a coding agent' }];
const PRS = [{ value: 'off', label: 'opened by hand (Open PR)' }, { value: 'draft', label: 'a draft when a task rests' }, { value: 'ready', label: 'ready for review when a task rests' }];
const label = (opts, v) => (opts.find((o) => o.value === v) || { label: v }).label;

ext.register({ screen: (s) => (s.kind === 'project-new' ? newTpl(s) : null) });

// goStep: back to that step's screen when it is on the stack, else pushed
function goStep(key) {
  const i = ui.stack.findIndex((x) => x.kind === 'project-new' && x.step === key);
  pj().formStep(key);
  if (i >= 0) { ui.stack.splice(i + 1); ctx.paint(); } else push({ kind: 'project-new', step: key });
}

function newTpl(s) {
  const app = ctx.app;
  const p = pj();
  const f = p.form;
  if (!f) return html`<screen title="New project" style="form"><section><empty title="closed"/></section></screen>`;
  app.sbx.ensure('', '');
  const list = app.sbx.listAt('');
  const state = partitionState();
  const team = state === 'global';
  const teamDef = state === 'user' && !!f.teamDef;
  // the manager picked (else the first), its default image and size, and internet when it
  // offers it — the web's choice (model/sandboxes.js projectSandbox): a project clones and fetches
  const sb = projectSandbox(f.sandbox, list.managers);
  const mine = (list.sandboxes || []).filter((x) => x.mine && (x.visibility === 'team') === team && !['deleting', 'archived', 'error'].includes(x.state));
  const provs = (p.scm && p.scm.providers) || [];
  const steps = wizardSteps(f, state);
  const step = stepAt(f, s.step || 'code', state);
  const cur = steps.find((x) => x.key === step);
  const why = stepProblem(f, step, { sb, state, providers: p.scm && !p.scm.error ? provs.length : undefined });
  const next = nextStep(f, step, state);
  const create = async () => {
    if (why) return;
    if (!teamDef && f.sandbox.mode !== 'pick') f.sandbox = { ...f.sandbox, provider: sb.provider, image: sb.image, size: sb.size, egress: sb.egress };
    const r = teamDef ? await projectTeam(app).saveTeam(p) : await p.saveProject();
    if (r && r.project) {
      const at = ui.stack.findIndex((x) => x.kind === 'projects');
      ui.stack.splice(at >= 0 ? at + 1 : Math.max(0, ui.stack.findIndex((x) => x.kind === 'project-new')));
      openProject(r.project.id);
    }
  };
  const c = { p, f, sb, list, mine, provs, team, teamDef, state };
  const body = { code: codeTpl, name: nameTpl, sandbox: sandboxTpl, how: howTpl, start: startTpl, review: reviewTpl }[step](c);
  return html`<screen title="New project" subtitle=${`${cur.n} of ${steps.length} · ${cur.label}`} style="form">
    <toolbar>${step === 'review'
      ? html`<button role="primary" ?busy=${f.busy} ?disabled=${!!why} @tap=${create}>Create</button>`
      : html`<button role="primary" ?disabled=${!!why || !next} @tap=${() => goStep(next)}>Next</button>`}</toolbar>
    <section><text style="footnote" tone="muted" text=${steps.map((x) => `${x.n} ${x.label}`).join(' · ')}/></section>
    ${f.err ? html`<section><notice tone="danger" text=${f.err}/></section>` : nothing}
    ${body}
    ${why ? html`<section><notice tone="muted" title="To go on" text=${why}/></section>` : nothing}
  </screen>`;
}

// --- the steps -------------------------------------------------------------------------------------------

function codeTpl({ p, f, provs }) {
  const prov = p.provider(f.scm);
  return html`<section title="Where the code is" footer=${prov ? youWords(prov) : p.scm && p.scm.error ? p.scm.error : 'No scm provider is bound — bind one (such as the scm-github template) to this agent\'s scm slot.'}>
      ${provs.length ? html`<picker label="Provider" value=${f.scm} options=${provs.map((x) => ({ value: x.scm, label: `${x.title || x.scm}${x.error ? ' — ' + x.error : ''}` }))}
        @change=${(e) => { p.setForm('scm', e.value); p.searchRepos(); }}/>` : nothing}
      ${prov ? signinTpl(p, prov) : nothing}
    </section>
    ${f.scm ? html`${f.repos.length ? html`<section title="Repos" footer="Swipe one to remove it.">
      ${repeat(f.repos, (r) => r.repo, (r) => html`<row title=${r.repo} subtitle=${`→ ${repoSlug(r.repo)}`} mono="title" icon="folder">
          <actions><button icon="xmark" role="destructive" @tap=${() => p.dropFormRepo(r.repo)}>Remove</button></actions></row>
        <disclosure title=${`setup script (optional)${r.setup ? ' — set' : ''}`} ?open=${!!r.setup}>
          <field kind="multiline" label=${`Setup for ${r.repo}`} placeholder="npm ci" value=${r.setup} @input=${(e) => { r.setup = e.value; }}/></disclosure>`)}
    </section>` : nothing}
    <section title=${f.repos.length ? 'Add another' : 'Choose a repo'} footer="What you can reach through the provider; a tap adds it.">
      <field kind="search" label="Find a repo you can reach" placeholder="owner/name" value=${f.q} @input=${(e) => p.setRepoQuery(e.value || '')}/>
      ${repeat(f.results, (r) => `${r.owner}/${r.name}`, (r) => {
        const name = `${r.owner}/${r.name}`;
        const added = f.repos.some((x) => x.repo === name);
        const words = [r.private ? 'private' : '', r.permission || '', r.archived ? 'archived — read-only: a task can\'t push to it' : '', added ? 'chosen' : ''].filter(Boolean).join(' · ');
        return html`<row title=${name} subtitle=${words || nothing} mono="title" icon=${added ? 'check' : r.archived ? 'archive' : 'plus'}
          ?disabled=${added || !!r.archived} @tap=${() => p.addFormRepo(name)}/>`;
      })}
      ${f.loading ? html`<progress label="loading…"/>` : f.err ? html`<notice tone="danger" text=${f.err}/>` : !f.results.length ? html`<empty title=${f.q ? 'No repo matches' : 'No repos found'}/>` : nothing}
      ${f.next && !f.loading ? html`<row title="More" icon="expand" @tap=${() => p.searchRepos(true)}/>` : nothing}
    </section>` : nothing}`;
}

function nameTpl({ p, f, teamDef, state }) {
  return html`<section footer=${f.name && f.name === f.autoName && f.repos.length ? `From its first repo, ${f.repos[0].repo}, until you type one.` : nothing}>
      <field label="Name" placeholder="Web" value=${f.name} @input=${(e) => p.setFormName(e.value || '')}/></section>
    ${state === 'user' ? html`<section title="Who it is for" footer=${teamDef ? 'A team project\'s definition holds the repos, the policy and its members; it has no tasks and no sandbox of yours — each member works on it in their own space. A seed sandbox can be set on its page.' : 'In your own space, with your own sandbox and sign-in.'}>
      <picker style="segmented" value=${teamDef ? 'team' : 'mine'} options=${[{ value: 'mine', label: 'Just you' }, { value: 'team', label: 'A team project' }]}
        @change=${(e) => p.setForm('teamDef', e.value === 'team')}/></section>` : nothing}`;
}

function sandboxTpl({ p, f, sb, mine, team }) {
  const sbModes = [...(team ? [{ value: 'none', label: 'none' }] : []), { value: 'new', label: 'a new sandbox' }, ...(mine.length ? [{ value: 'pick', label: 'one of your own' }] : [])];
  return html`<section title=${team ? 'Its seed sandbox' : 'Its sandbox'} footer=${team ? 'Members\' sandboxes fork from a seed shared with the team; it holds no sign-in.' : 'Its credentials are yours: a project works only in a private sandbox of your own.'}>
    <picker style="segmented" value=${f.sandbox.mode} options=${sbModes} @change=${(e) => p.setFormPart('sandbox', 'mode', e.value)}/>
    ${f.sandbox.mode === 'pick' ? html`<picker label="Sandbox" value=${f.sandbox.ref} options=${[{ value: '', label: 'pick one…' }, ...mine.map((x) => ({ value: x.ref, label: `${x.name} · ${x.state}` }))]}
      @change=${(e) => p.setFormPart('sandbox', 'ref', e.value)}/>`
      : f.sandbox.mode === 'new' ? (sb.usable.length ? html`<picker label="Manager" value=${sb.provider} options=${sb.usable.map((m) => ({ value: m.provider, label: m.title || m.provider }))}
        @change=${(e) => p.setFormPart('sandbox', 'provider', e.value)}/>
        <picker label="Network" value=${sb.egress} options=${sb.egressOpts.map((e) => ({ value: e, label: EGRESS[e] || e }))} @change=${(e) => p.setFormPart('sandbox', 'egress', e.value)}/>`
        : html`<notice tone="warn" text="No sandbox manager is bound — bind one to this agent's sandboxes slot."/>`) : nothing}
  </section>`;
}

function howTpl({ p, f, team, teamDef, state }) {
  return html`<section title="How it works">
      <field kind="number" label="Tasks at work at once" value=${String(f.policy.maxTasks)} @input=${(e) => p.setFormPart('policy', 'maxTasks', e.value)}/>
      <picker label="Who answers tasks" value=${f.policy.engine} options=${ENGINES} @change=${(e) => p.setFormPart('policy', 'engine', e.value)}/>
      <picker label="Pull requests" value=${f.policy.autoPR} options=${PRS} @change=${(e) => p.setFormPart('policy', 'autoPR', e.value)}/>
      ${state === 'legacy' || team || teamDef ? html`<picker label="Who can see it" value=${f.share.visibility} options=${[{ value: 'private', label: team || teamDef ? 'only the members you add next' : 'only you' }, { value: 'team', label: 'everyone who can open this agent' }]}
        @change=${(e) => p.setFormPart('share', 'visibility', e.value)}/>` : nothing}
    </section>
    ${p.wikiRoute ? html`<section title="The project wiki" footer="A task adds what it learned to the wiki once its pull request opens, and at its end after 3 more turns.">
      <toggle label="Dream rounds" value=${f.policy.dream !== false} @change=${(e) => p.setFormPart('policy', 'dream', !!e.value)}/></section>` : nothing}`;
}

function startTpl({ p, f }) {
  const offer = p.starterOffer(f);
  return html`<section><text style="footnote" tone="muted" text="Work the project starts with, once its sandbox is ready and its repos are cloned."/></section>
    ${STARTER.map((x) => {
      const o = offer[x.key];
      return html`<section footer=${o.ok ? x.hint : `${x.hint} ${o.why}`}>
        <toggle label=${x.label} value=${o.ok && !!f.starter[x.key]} ?disabled=${!o.ok} @change=${(e) => p.setFormPart('starter', x.key, !!e.value)}/></section>`;
    })}`;
}

function reviewTpl({ p, f, sb, list, team, teamDef, state }) {
  const steps = wizardSteps(f, state);
  const has = (k) => steps.some((x) => x.key === k);
  const prov = p.provider(f.scm);
  const offer = p.starterOffer(f);
  const picked = f.sandbox.mode === 'pick' ? ((list.sandboxes || []).find((x) => x.ref === f.sandbox.ref) || { name: f.sandbox.ref }) : null;
  const edit = (key, title) => html`<row title=${`Edit ${title}`} icon="pencil" nav @tap=${() => goStep(key)}/>`;
  const chosen = STARTER.filter((x) => offer[x.key].ok && f.starter[x.key]);
  return html`<section title="Code">
      <row title=${prov ? prov.title || prov.scm : f.scm} subtitle="provider"/>
      ${repeat(f.repos, (r) => r.repo, (r) => html`<row title=${r.repo} subtitle=${r.setup.trim() ? 'with a setup script' : nothing} mono="title" icon="folder"/>`)}
      ${edit('code', 'Code')}</section>
    <section title="Name"><row title=${f.name} subtitle=${state === 'user' ? (teamDef ? 'a team project — its definition in the shared space' : 'just you') : nothing}/>${edit('name', 'Name')}</section>
    ${has('sandbox') ? html`<section title=${team ? 'Seed sandbox' : 'Sandbox'}><row title=${team && f.sandbox.mode === 'none' ? 'none' : picked ? `one of your own: ${picked.name}`
      : `a new one — ${sb.provider}${sb.image ? `, ${sb.image}` : ''}${sb.size ? `, ${sb.size}` : ''}, ${EGRESS[sb.egress] || sb.egress}`}/>${edit('sandbox', 'Sandbox')}</section>` : nothing}
    <section title="How it works">
      <row title=${`${f.policy.maxTasks} task${+f.policy.maxTasks === 1 ? '' : 's'} at work at once`}/>
      <row title=${`answered by ${label(ENGINES, f.policy.engine)}`}/>
      <row title=${`pull requests ${label(PRS, f.policy.autoPR)}`}/>
      ${p.wikiRoute ? html`<row title=${`dream rounds ${f.policy.dream === false ? 'off' : 'on'}`}/>` : nothing}
      ${edit('how', 'How it works')}</section>
    ${has('start') ? html`<section title="Get started">${chosen.length ? chosen.map((x) => html`<row title=${x.label} icon="check"/>`) : html`<row title="nothing — the project starts empty"/>`}
      ${edit('start', 'Get started')}</section>` : nothing}`;
}

// what the provider says of you (the web's providerNoteTpl)
function youWords(prov) {
  const you = prov.you || {};
  const name = prov.title || prov.scm;
  return [you.person ? `You are ${you.person.login} there.` : partitionState() === 'user' ? `You haven't signed in to ${name}.` : 'Projects here work as the provider\'s bot.', ...(prov.notes || [])].join(' ');
}
