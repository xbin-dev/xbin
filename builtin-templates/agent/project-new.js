// project-new.js — the new project on the web (API.md §Projects in the
// UI, "A new project"), drawn in the Projects page: a wizard over
// app.projects.form (model/projects.js; its steps model/project-wizard.js)
// — a step header, a step at a time, Back and Next (Next disabled, saying
// why, until the step can go on), Review last with an Edit link to each
// step, then Create:
//
//   1 Code          the scm provider (signing in to it, in your own
//                   partition, when it lets you work as yourself and you
//                   haven't), then the repos — a combobox of what you can
//                   reach (project-repos.js); choosing one adds it
//   2 Name          from the first repo until you type one; in a person's
//                   space who it is for (just you, or a team project's
//                   definition — made in the shared space)
//   3 Sandbox       a new one or one of your own private ones; at the
//                   shared space a team's seed (one of yours shared with
//                   the team, or none); a definition made from your own
//                   space has none: the step is skipped
//   4 How it works  tasks at once, who answers, pull requests, whose
//                   identity, the wiki's dream rounds; who can see it where
//                   it may be shared
//   5 Get started   the starter work (POST /projects starter): task #1
//                   builds and runs the tests, the coordinator builds the
//                   initial wiki — each said in a line, disabled saying why
//                   where it can't apply; a definition has none: skipped
//   6 Review        everything chosen, then Create
//
// "Make this a project…" (the upgrade dialog, #pupg): an action of the ▣
// sandbox popover (ext.sbx) on a conversation of yours with a sandbox and
// no project — the sandbox's git repos are read (detect), you pick which
// (their remotes' credentials dropped; an ssh one may switch to https),
// name it, keep its branch or start a new one, and the conversation
// becomes the project's first task (model/project-upgrade.js).
//
// signinTpl (the provider's sign-in, its device code shown to you only) is
// shared with the settings' status.
import { html, nothing, render } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { projectTeam } from './model/project-team.js';
import { upgradeOffer, projectUpgrade } from './model/project-upgrade.js';
import { partitionState } from './model/partition.js';
import { safeUrl } from './model/project-task.js';
import { repoSlug, canSignin } from './model/projects.js';
import { EGRESS, projectSandbox } from './model/sandboxes.js';
import { STEPS, wizardSteps, stepAt, nextStep, prevStep, stepProblem, STARTER } from './model/project-wizard.js';
import { repoPickerTpl, closeRepoPicker, repoPickerCss } from './project-repos.js';

// the sandbox part's effective choice (model/sandboxes.js projectSandbox):
// what the form says, else the first manager, its default image and size,
// internet when it offers it (a project clones and fetches)
const sandboxOf = (f, managers) => projectSandbox(f.sandbox, managers);

const ENGINES = { auto: 'auto — your pick per task', builtin: 'the built-in agent', harness: 'a coding agent' };
const PRS = { off: 'opened by hand (Open PR)', draft: 'a draft when a task rests', ready: 'ready for review when a task rests' };

// the step drawn last: a new step moves the focus to its heading
let shownStep = '';

export function newProjectTpl(p) {
  const app = ctx.app;
  const f = p.form;
  app.sbx.ensure('', '');
  const list = app.sbx.listAt('');
  const sb = sandboxOf(f, list.managers);
  const state = partitionState();
  const team = state === 'global';
  const teamDef = state === 'user' && !!f.teamDef; // a team project's definition, made from your own space
  // yours to pick: a private one for a project (its credentials are yours);
  // a team-visible one for a team's seed — members' sandboxes fork from it
  // only when they can see it (API.md §Projects in the UI), and an existing sandbox
  // keeps its own visibility
  const mine = (list.sandboxes || []).filter((s) => s.mine && (s.visibility === 'team') === team && !['deleting', 'archived', 'error'].includes(s.state));
  const provs = (p.scm && p.scm.providers) || [];
  const steps = wizardSteps(f, state);
  const step = stepAt(f, f.step || 'code', state);
  const cur = steps.find((s) => s.key === step);
  const pctx = { sb, state, providers: p.scm && !p.scm.error ? provs.length : undefined };
  const why = stepProblem(f, step, pctx);
  const order = (k) => STEPS.findIndex((s) => s.key === k);
  f.reach = Math.max(f.reach || 0, order(step)); // the furthest step seen: the header leads back to any before it
  const go = (key) => { closeRepoPicker(f); p.formStep(key); };
  const next = () => { if (!why && nextStep(f, step, state)) go(nextStep(f, step, state)); };
  const create = () => {
    if (why) return;
    if (!teamDef) p.form.sandbox = { mode: sb.mode, ref: sb.ref, provider: sb.provider, image: sb.image, size: sb.size, egress: sb.egress };
    if (teamDef) projectTeam(app).saveTeam(p); else p.saveProject();
  };
  if (shownStep !== step) {
    const first = !shownStep;
    shownStep = step;
    if (!first) requestAnimationFrame(() => document.getElementById('pn-stephead')?.focus());
  }
  const body = { code: codeTpl, name: nameTpl, sandbox: sandboxTpl, how: howTpl, start: startTpl, review: reviewTpl }[step];
  const back = prevStep(f, step, state);
  return html`<div class="autos-page projs-page" id="proj-form" data-step=${step}>
    <div class="ahd"><a class="crumb" role="button" tabindex="0" @click=${() => p.closeForm()} @keydown=${(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); p.closeForm(); } }}>Projects</a> › <b>New project</b></div>
    <nav aria-label="Steps of the new project"><ol class="pnsteps" id="pn-steps">${steps.map((s) => {
      const done = s.n < cur.n;
      const can = order(s.key) <= f.reach && s.key !== step;
      return html`<li class=${`pnstep${s.key === step ? ' on' : ''}${done ? ' done' : ''}`} data-step=${s.key}>${can
        ? html`<button type="button" class="pnstepb" @click=${() => go(s.key)}><span class="pnn">${s.n}</span>${s.label}</button>`
        : html`<span class="pnstepb" aria-current=${s.key === step ? 'step' : nothing}><span class="pnn">${s.n}</span>${s.label}</span>`}</li>`;
    })}</ol></nav>
    <form id="pn-step" @submit=${(e) => { e.preventDefault(); if (step === 'review') create(); else next(); }}>
      <h4 class="pnhead" id="pn-stephead" tabindex="-1">${cur.n}. ${cur.label}</h4>
      ${body({ p, f, sb, list, mine, provs, team, teamDef, state, go })}
      ${f.err ? html`<div class="err" id="pn-err">${f.err}</div>` : nothing}
      <div class="pnfoot">
        ${back ? html`<button type="button" class="btn ghost" id="pn-back" @click=${() => go(back)}>Back</button>` : nothing}
        <span style="flex:1"></span>
        ${why ? html`<span class="muted small" id="pn-why">${why}</span>` : nothing}
        ${step === 'review'
          ? html`<button type="submit" class="btn" id="pn-create" ?disabled=${f.busy || !!why} aria-describedby=${why ? 'pn-why' : nothing}>${f.busy ? 'Creating…' : teamDef || team ? 'Create the team project' : 'Create project'}</button>`
          : html`${f.reach >= order('review') && !why ? html`<button type="button" class="btn ghost" id="pn-toreview" @click=${() => go('review')}>Review</button>` : nothing}
            <button type="submit" class="btn" id="pn-next" ?disabled=${!!why} aria-describedby=${why ? 'pn-why' : nothing}>Next</button>`}
        <button type="button" class="btn ghost" @click=${() => p.closeForm()}>Cancel</button>
      </div>
    </form>
  </div>`;
}

// --- the steps -------------------------------------------------------------------------------------------

function codeTpl({ p, f, provs }) {
  const prov = p.provider(f.scm);
  return html`
    ${p.scm && p.scm.error ? html`<div class="err">${p.scm.error}</div>` : nothing}
    ${p.scm && !p.scm.error && !provs.length ? html`<div class="note">No scm provider is bound — bind one (such as the scm-github template) to this agent's scm slot.</div>` : nothing}
    ${provs.length ? html`<div class="field"><label for="pn-scm">Provider</label><select id="pn-scm" @change=${(e) => { p.setForm('scm', e.target.value); p.searchRepos(); }}>
      ${provs.map((x) => html`<option value=${x.scm} ?selected=${x.scm === f.scm} ?disabled=${!!x.error}>${x.title || x.scm}${x.error ? ' — ' + x.error : ''}</option>`)}</select>
      ${prov ? providerNoteTpl(p, prov) : nothing}</div>` : nothing}
    ${f.scm ? html`<div class="field"><label for="pn-q">Repos</label>${repoPickerTpl(p, f)}</div>` : nothing}`;
}

function nameTpl({ p, f, teamDef, state }) {
  return html`<div class="field"><label for="pn-name">Name</label><input id="pn-name" maxlength="80" .value=${f.name} @input=${(e) => p.setFormName(e.target.value)} placeholder="Web">
      <div class="muted small">${f.name && f.name === f.autoName ? `From its first repo, ${f.repos[0] ? f.repos[0].repo : ''}, until you type one.` : f.name ? '' : 'From its first repo, until you type one.'}</div></div>
    ${state === 'user' ? html`<fieldset class="field pnfs" id="pn-kind"><legend>Who it is for</legend>
      <label class="chk"><input type="radio" name="pn-kind" value="mine" .checked=${!teamDef} @change=${() => p.setForm('teamDef', false)}> just you — in your own space, with your own sandbox and sign-in</label>
      <label class="chk"><input type="radio" name="pn-kind" value="team" .checked=${teamDef} @change=${() => p.setForm('teamDef', true)}> a team project — its definition in the shared space; each member works on it in their own space</label></fieldset>
      ${teamDef ? html`<div class="muted small" id="pn-team-note">A team project's definition holds the repos, the policy and its members; it has no tasks and no sandbox of yours.
        A seed sandbox members' sandboxes fork from can be set on its page.</div>` : nothing}` : nothing}`;
}

function sandboxTpl({ p, sb, mine, team }) {
  const setSb = (k) => (e) => p.setFormPart('sandbox', k, e.target.value);
  return html`
    ${team ? html`<div class="muted small">A team project's tasks run in each member's own space; a seed sandbox (optional) is the start their sandboxes fork from. It holds no sign-in.</div>` : nothing}
    <fieldset class="field pnfs"><legend>${team ? 'Its seed sandbox' : 'Its sandbox'}</legend>
      ${team ? html`<label class="chk"><input type="radio" name="pn-sbx" value="none" .checked=${sb.mode === 'none'} @change=${() => p.setFormPart('sandbox', 'mode', 'none')}> none</label>` : nothing}
      <label class="chk"><input type="radio" name="pn-sbx" value="new" .checked=${sb.mode !== 'pick' && !(team && sb.mode === 'none')} @change=${() => p.setFormPart('sandbox', 'mode', 'new')}> a new sandbox</label>
      <label class="chk"><input type="radio" name="pn-sbx" value="pick" .checked=${sb.mode === 'pick'} ?disabled=${!mine.length}
        @change=${() => p.setFormPart('sandbox', 'mode', 'pick')}> one of your own${mine.length ? '' : team ? ' (you have no sandbox shared with the team)' : ' (you have no private sandbox)'}</label>
    </fieldset>
    ${team && sb.mode === 'none' ? nothing : sb.mode === 'pick' ? html`<div class="field"><label for="pn-sbx-ref">Sandbox</label><select id="pn-sbx-ref" @change=${setSb('ref')}>
        <option value="" ?selected=${!sb.ref}>pick one…</option>
        ${mine.map((s) => html`<option value=${s.ref} ?selected=${s.ref === sb.ref}>${s.name} · ${s.manager || s.provider} · ${s.state}</option>`)}</select>
        <div class="muted small">${team ? 'Shared with the team, so members\' sandboxes can fork from it; it holds no sign-in. A private sandbox of yours must be shared with the team first.' : 'Its credentials are yours: a project works only in a private sandbox of your own.'}</div></div>`
      : sb.usable.length ? html`<div class="row2">
        <div class="field"><label for="pn-sbx-mgr">Manager</label><select id="pn-sbx-mgr" @change=${setSb('provider')}>${sb.usable.map((m) => html`<option value=${m.provider} ?selected=${m.provider === sb.provider}>${m.title || m.provider}</option>`)}</select></div>
        <div class="field"><label for="pn-sbx-img">Image</label><select id="pn-sbx-img" @change=${setSb('image')}>${((sb.m && sb.m.images) || []).map((i) => html`<option value=${i.id} ?selected=${i.id === sb.image}>${i.title || i.id}</option>`)}</select></div>
        <div class="field"><label for="pn-sbx-size">Size</label><select id="pn-sbx-size" @change=${setSb('size')}>${((sb.m && sb.m.sizes) || []).map((z) => html`<option value=${z.id} ?selected=${z.id === sb.size}>${z.id}</option>`)}</select></div>
        <div class="field"><label for="pn-sbx-net">Network</label><select id="pn-sbx-net" @change=${setSb('egress')}>${sb.egressOpts.map((e) => html`<option value=${e} ?selected=${e === sb.egress}>${EGRESS[e] || e}</option>`)}</select></div>
      </div>` : html`<div class="note">No sandbox manager is bound — bind one to this agent's sandboxes slot.</div>`}`;
}

function howTpl({ p, f, team, teamDef, state }) {
  const app = ctx.app;
  const prov = p.provider(f.scm);
  const setPol = (k) => (e) => p.setFormPart('policy', k, e.target.value);
  return html`<div class="row2">
      <div class="field"><label for="pn-max">Tasks at work at once</label><input id="pn-max" type="number" min="1" max="16" .value=${String(f.policy.maxTasks)} @input=${setPol('maxTasks')}></div>
      <div class="field"><label for="pn-engine">Who answers tasks</label><select id="pn-engine" @change=${setPol('engine')}>
        ${Object.entries(ENGINES).map(([v, l]) => html`<option value=${v} ?selected=${f.policy.engine === v}>${l}</option>`)}</select></div>
      ${f.policy.engine === 'harness' ? html`<div class="field"><label for="pn-harness">Coding agent</label><select id="pn-harness" @change=${setPol('harness')}>
        <option value="" ?selected=${!f.policy.harness}>the one you used last</option>
        ${(app.harness.catalog.harnesses || []).map((h) => html`<option value=${h.id} ?selected=${h.id === f.policy.harness}>${h.name}</option>`)}</select></div>` : nothing}
      <div class="field"><label for="pn-pr">Pull requests</label><select id="pn-pr" @change=${setPol('autoPR')}>
        ${Object.entries(PRS).map(([v, l]) => html`<option value=${v} ?selected=${f.policy.autoPR === v}>${l}</option>`)}</select></div>
      ${prov && ((prov.you && prov.you.identities) || []).length > 1 ? html`<div class="field"><label for="pn-as">Work as</label><select id="pn-as" @change=${setPol('as')}>
        <option value="" ?selected=${!f.policy.as}>the default (${prov.you.default || 'person'})</option>
        ${prov.you.identities.map((i) => html`<option value=${i} ?selected=${f.policy.as === i}>${i === 'bot' ? 'the provider\'s bot' : 'you'}</option>`)}</select></div>` : nothing}
    </div>
    ${p.wikiRoute ? html`<div class="field"><label class="chk"><input type="checkbox" id="pn-dream" .checked=${f.policy.dream !== false}
        @change=${(e) => p.setFormPart('policy', 'dream', e.target.checked)}> Dream rounds: a task adds what it learned to the project wiki</label>
      <div class="muted small">Once when its pull request opens, and at its end after 3 more turns — a turn of its own. Change it later in Settings.</div></div>` : nothing}
    ${state === 'legacy' || team || teamDef ? html`<div class="row2">
      <div class="field"><label for="pn-vis">Who can see it</label><select id="pn-vis" @change=${(e) => p.setFormPart('share', 'visibility', e.target.value)}>
        <option value="private" ?selected=${f.share.visibility !== 'team'}>${team || teamDef ? 'only the members you add next' : 'only you'}</option>
        <option value="team" ?selected=${f.share.visibility === 'team'}>everyone who can open this agent</option></select></div>
      ${f.share.visibility === 'team' ? html`<div class="field"><label for="pn-role">They may</label><select id="pn-role" @change=${(e) => p.setFormPart('share', 'teamRole', e.target.value)}>
        <option value="viewer" ?selected=${f.share.teamRole !== 'participant'}>read</option>
        <option value="participant" ?selected=${f.share.teamRole === 'participant'}>make and steer tasks</option></select></div>` : nothing}
    </div>` : nothing}`;
}

function startTpl({ p, f }) {
  const offer = p.starterOffer(f);
  return html`<div class="muted small">Work the project starts with, once its sandbox is ready and its repos are cloned. Both are on by default; untick what you don't want.</div>
    <fieldset class="field pnfs" id="pn-starter"><legend class="sr">Starter work</legend>
      ${STARTER.map((s) => {
        const o = offer[s.key];
        return html`<div class="pnstart" data-key=${s.key}>
          <label class="chk"><input type="checkbox" id=${`pn-st-${s.key}`} .checked=${o.ok && !!f.starter[s.key]} ?disabled=${!o.ok}
            aria-describedby=${`pn-st-${s.key}-d`} @change=${(e) => p.setFormPart('starter', s.key, e.target.checked)}> <b>${s.label}</b></label>
          <div class="muted small" id=${`pn-st-${s.key}-d`}>${s.hint}</div>
          ${o.ok ? nothing : html`<div class="small pnoff" id=${`pn-st-${s.key}-why`}>${o.why}</div>`}</div>`;
      })}
    </fieldset>`;
}

function reviewTpl({ p, f, sb, list, team, teamDef, state, go }) {
  const app = ctx.app;
  const steps = wizardSteps(f, state);
  const has = (k) => steps.some((s) => s.key === k);
  const prov = p.provider(f.scm);
  const offer = p.starterOffer(f);
  const mgr = (sb.usable || []).find((m) => m.provider === sb.provider);
  const picked = sb.mode === 'pick' ? ((list.sandboxes || []).find((s) => s.ref === sb.ref) || { name: sb.ref }) : null;
  const harness = f.policy.engine === 'harness' ? ((app.harness.catalog.harnesses || []).find((h) => h.id === f.policy.harness) || {}).name || 'the one you used last' : '';
  const row = (key, label, body) => html`<div class="pnrev" data-step=${key}><div class="pnrevh"><b>${label}</b><span style="flex:1"></span>
      <button type="button" class="btn ghost btnsm pnedit" data-edit=${key} aria-label=${`Edit ${label}`} @click=${() => go(key)}>Edit</button></div>${body}</div>`;
  const chosen = ['build', 'wiki'].filter((k) => offer[k].ok && f.starter[k]);
  return html`<div class="pnreview" id="pn-review">
    ${row('code', 'Code', html`<div>${prov ? prov.title || prov.scm : f.scm}</div><ul class="pnrevl">${f.repos.map((r) => html`<li><span class="mono">${r.repo}</span>${r.setup.trim() ? html` <span class="muted small">with a setup script</span>` : nothing}</li>`)}</ul>`)}
    ${row('name', 'Name', html`<div>${f.name}</div>${state === 'user' ? html`<div class="muted small">${teamDef ? 'a team project — its definition in the shared space' : 'just you'}</div>` : nothing}`)}
    ${has('sandbox') ? row('sandbox', team ? 'Seed sandbox' : 'Sandbox', html`<div>${team && sb.mode === 'none' ? 'none'
      : picked ? html`one of your own: <b>${picked.name}</b>`
      : html`a new one — ${mgr ? mgr.title || mgr.provider : sb.provider}${sb.image ? `, ${sb.image}` : ''}${sb.size ? `, ${sb.size}` : ''}, ${EGRESS[sb.egress] || sb.egress}`}</div>`) : nothing}
    ${row('how', 'How it works', html`<ul class="pnrevl">
      <li>${f.policy.maxTasks} task${+f.policy.maxTasks === 1 ? '' : 's'} at work at once</li>
      <li>answered by ${ENGINES[f.policy.engine] || f.policy.engine}${harness ? ` (${harness})` : ''}</li>
      <li>pull requests ${PRS[f.policy.autoPR] || f.policy.autoPR}</li>
      ${f.policy.as ? html`<li>works as ${f.policy.as === 'bot' ? 'the provider\'s bot' : 'you'}</li>` : nothing}
      ${p.wikiRoute ? html`<li>dream rounds ${f.policy.dream === false ? 'off' : 'on'}</li>` : nothing}
      ${state === 'legacy' || team || teamDef ? html`<li>${f.share.visibility === 'team' ? `everyone who can open this agent may ${f.share.teamRole === 'participant' ? 'make and steer tasks' : 'read it'}` : team || teamDef ? 'seen by the members you add next' : 'seen by you only'}</li>` : nothing}
    </ul>`)}
    ${has('start') ? row('start', 'Get started', chosen.length ? html`<ul class="pnrevl">${chosen.map((k) => html`<li>${STARTER.find((s) => s.key === k).label}</li>`)}</ul>`
      : html`<div class="muted">nothing — the project starts empty</div>`) : nothing}
  </div>`;
}

// what the provider says of you: signed in as whom, or a way to sign in —
// offered only where you may sign in (your own partition, the provider
// letting you work as yourself); elsewhere projects work as its bot, and at
// the shared space each member signs in from their own
function providerNoteTpl(p, prov) {
  const you = prov.you || {};
  const name = prov.title || prov.scm;
  const offer = canSignin(prov);
  const elsewhere = partitionState() === 'global' && (prov.identities || []).includes('person');
  return html`<div class="muted small pnyou">
    ${you.person ? html`You are ${you.person.login} there.` : offer ? html`You haven't signed in to ${name}.`
      : elsewhere ? html`The shared space works as the provider's bot; each member signs in to ${name} from their own space.` : html`Projects here work as the provider's bot.`}
    ${(prov.notes || []).map((n) => html`<div>${n}</div>`)}
  </div>
  ${offer && !you.person ? signinTpl(p, prov.scm, prov.title) : nothing}`;
}

/**
 * signinTpl(p, scm, title): signing in to a provider — Sign in, then the
 * device code and the page to enter it on (shown to you only: it is your own
 * sign-in, read from your own space), its state while it is pending, and
 * Forget once signed in.
 */
export function signinTpl(p, scm, title = '') {
  const s = p.signinOf(scm);
  const name = title || scm;
  if (s && s.state === 'pending' && s.signin) {
    const url = safeUrl(s.signin.url);
    return html`<div class="psignin" id="psignin" data-state="pending">
      <div>Open ${url ? html`<a href=${url} target="_blank" rel="noopener noreferrer" id="psignin-link">${url}</a>` : 'the sign-in page'} and enter
        <b class="mono pcode" id="psignin-code">${s.signin.userCode}</b></div>
      <div class="muted small">Waiting for you to approve it there… (only you see this code)</div></div>`;
  }
  if (s && s.state === 'done') {
    return html`<div class="psignin" data-state="done">Signed in to ${name}${s.identity ? ` as ${s.identity.login}` : ''}.
      <button type="button" class="btn ghost btnsm" id="psignin-forget" title="forget your sign-in: your projects' credentials are removed from their sandboxes first"
        @click=${() => { if (confirm(`Forget your sign-in to ${name}? Your projects' credentials are removed from their sandboxes first.`)) p.forget(scm); }}>Forget</button></div>`;
  }
  return html`<div class="psignin" data-state=${(s && s.state) || 'none'}>
    ${s && (s.err || s.error) ? html`<div class="err">${s.err || s.error}</div>` : s && ['denied', 'expired'].includes(s.state) ? html`<div class="err">The sign-in was ${s.state}.</div>` : nothing}
    <button type="button" class="btn btnsm" id="psignin-start" @click=${() => p.signin(scm)}>Sign in to ${name}</button></div>`;
}

// --- "Make this a project…" ----------------------------------------------------------------------

ext.register({ sbx: (b, close) => upgradeAction(close) });

function upgradeAction(close) {
  const app = ctx.app;
  const v = app && app.session.current();
  const o = upgradeOffer(v);
  if (!o.shown) return null;
  return html`<button class="btn ghost btnsm" id="sbx-upgrade" title=${o.title} @click=${() => { close(); openUpgrade(v.run.id); }}>Make this a project…</button>`;
}

let dlg = null;
let dlgRun = null;
function openUpgrade(runId) {
  const app = ctx.app;
  if (!dlg) {
    dlg = document.createElement('dialog');
    dlg.id = 'pupg';
    dlg.className = 'pupg';
    document.body.append(dlg);
    dlg.addEventListener('close', () => { if (dlgRun != null) projectUpgrade(app).close(dlgRun); dlgRun = null; render(nothing, dlg); });
    app.on('projects', drawUpgrade);
  }
  dlgRun = runId;
  projectUpgrade(app).open(runId);
  drawUpgrade();
  if (!dlg.open) dlg.showModal();
}
function drawUpgrade() { if (dlg && dlgRun != null) render(upgradeTpl(projectUpgrade(ctx.app).form(dlgRun)), dlg); }

function upgradeTpl(f) {
  if (!f) return nothing;
  const u = projectUpgrade(ctx.app);
  const id = f.runId;
  const close = () => dlg.close();
  if (f.done) {
    const pv = f.done.project;
    return html`<div class="dlg-hd">Made a project</div>
      <div class="dlg-bd" id="pupg-done">This conversation is task #${(f.done.task && f.done.task.n) || 1} of ${pv ? pv.name : 'the project'}: its branch and pull requests show in its top bar, and the project's board holds it.</div>
      <div class="dlg-ft">${pv ? html`<button class="btn ghost" @click=${() => { close(); ctx.app.openProjects(pv.id); }}>Open the project</button>` : nothing}
        <button class="btn" @click=${close}>Done</button></div>`;
  }
  const picked = f.candidates.filter((c) => f.picked.has(c.path));
  return html`<div class="dlg-hd">Make this a project</div>
    <div class="dlg-bd">
      <div class="muted small">The conversation becomes the project's first task; its sandbox${f.sandbox ? html` (<b>${f.sandbox}</b>)` : nothing} becomes the project's, and the repos you pick are its repos. A project's tasks push with credentials of their own, never what a remote held.</div>
      ${f.loading ? html`<div class="muted small">reading the sandbox's repos…</div>` : nothing}
      ${!f.loading && !f.err && !f.candidates.length ? html`<div class="note">No git repo in ${f.cwd || 'its working directory'}.</div>` : nothing}
      ${f.candidates.length ? html`<div class="field"><label>Repos</label>${f.candidates.map((c) => html`<div class="pupgc" data-path=${c.path}>
          <label class="chk"><input type="checkbox" .checked=${f.picked.has(c.path)} ?disabled=${!c.usable} @change=${() => u.toggle(id, c.path)}>
            <b class="mono">${c.title}</b></label>
          <div class="muted small">${c.detail}${c.why ? html` — <span class="err">${c.why}</span>` : nothing}</div>
          ${c.https && f.picked.has(c.path) ? html`<label class="chk small"><input type="checkbox" .checked=${f.https.has(c.path)} @change=${() => u.toggleHttps(id, c.path)}> switch its remote to https (the project's own credentials serve it)</label>` : nothing}
        </div>`)}</div>` : nothing}
      ${picked.length ? html`<div class="field"><label>Name</label><input id="pupg-name" .value=${f.name} @input=${(e) => u.set(id, 'name', e.target.value)}></div>
        <div class="field"><label>Its branch</label><select id="pupg-branch" @change=${(e) => u.set(id, 'branch', e.target.value)}>
          <option value="keep" ?selected=${f.branch !== 'new'}>keep the branch it is on</option>
          <option value="new" ?selected=${f.branch === 'new'}>a new branch for the task</option></select></div>` : nothing}
      ${f.err ? html`<div class="err" id="pupg-err">${f.err}</div>` : nothing}
    </div>
    <div class="dlg-ft"><button class="btn ghost" @click=${close}>Cancel</button>
      <button class="btn" id="pupg-make" ?disabled=${f.busy || f.loading || !picked.length} @click=${() => u.submit(id)}>${f.busy ? 'Making…' : 'Make the project'}</button></div>`;
}

const style = document.createElement('style');
style.textContent = repoPickerCss + `
  .projs-page .pnsteps { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 4px 2px; margin: 0 0 12px; padding: 0; list-style: none; border-bottom: 1px solid var(--bx-border); }
  .projs-page .pnstep { display: flex; }
  .projs-page .pnstep .pnstepb { display: inline-flex; gap: 6px; align-items: center; box-sizing: border-box; height: 32px; min-height: 0; padding: 0 8px; margin: 0 0 -1px;
    border: 0; border-bottom: 2px solid transparent; border-radius: 0; background: transparent; color: var(--bx-muted); font: var(--bx-font); line-height: 1; box-shadow: none; }
  .projs-page .pnstep button.pnstepb { cursor: pointer; color: var(--bx-text); }
  .projs-page .pnstep button.pnstepb:hover { background: var(--bx-hover); }
  .projs-page .pnstep.on .pnstepb { color: var(--bx-text); font-weight: 600; border-bottom-color: var(--bx-accent); }
  .projs-page .pnn { display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; border-radius: 50%;
    border: 1px solid currentColor; font-size: 11px; font-weight: 600; flex: none; }
  .projs-page .pnstep.on .pnn { background: var(--bx-accent); border-color: var(--bx-accent); color: var(--bx-accent-ink, #fff); }
  .projs-page .pnstep.done .pnn { border-color: var(--bx-ok); color: var(--bx-ok); }
  .projs-page .pnhead { margin: 4px 0 12px; font: var(--bx-font-title); outline: none; }
  .projs-page .pnfs { border: 0; padding: 0; margin: 0 0 12px; min-width: 0; }
  .projs-page .pnfs legend { padding: 0; margin-bottom: 4px; font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted); }
  .projs-page .pnfoot { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--bx-border); }
  .projs-page .pnstart { padding: 8px 0; border-bottom: 1px solid var(--bx-border); }
  .projs-page .pnstart:last-child { border-bottom: 0; }
  .projs-page .pnstart .muted.small { margin-left: 22px; }
  .projs-page .pnoff { margin: 2px 0 0 22px; color: var(--bx-warn); }
  .projs-page .pnrev { padding: 8px 0; border-bottom: 1px solid var(--bx-border); min-width: 0; overflow-wrap: anywhere; }
  .projs-page .pnrevh { display: flex; align-items: center; gap: 8px; margin-bottom: 2px; }
  .projs-page .pnrevl { margin: 2px 0 0; padding-left: 18px; }
  .projs-page .pnyou { margin-top: 4px; }
  .psignin { margin: 8px 0; padding: 8px 12px; border: 1px solid var(--bx-border); border-radius: var(--bx-radius); background: var(--bx-panel-2); }
  .psignin .pcode { font-size: 15px; letter-spacing: .12em; padding: 0 4px; }
  .psignin a { overflow-wrap: anywhere; }
  dialog.pupg { width: min(560px, calc(100vw - 32px)); }
  dialog.pupg .pupgc { padding: 4px 0; border-bottom: 1px solid var(--bx-border); min-width: 0; overflow-wrap: anywhere; }
`;
document.head.append(style);
