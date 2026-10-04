// project-new.js — the new-project form on the web (API.md §Projects in the
// UI), drawn in the Projects page: the scm provider (signing in to it, in
// your own partition, when it lets you work as yourself and you haven't), the repos — a picker of what
// you can reach through it (GET /projects/scm/repos), each with an optional
// setup script — the project's name, its sandbox (a new one, or one of your
// own private ones; for a team's seed, one of yours shared with the team)
// and the policy basics (tasks at once, who answers, pull
// requests, whose identity). In an unpartitioned agent it may be shared with
// the team at once; at a partitioned agent's shared space it makes a team
// project's definition — shared with the team or with the members added
// next, its seed sandbox optional. The state is app.projects.form
// (model/projects.js).
//
// signinTpl (the provider's sign-in, its device code shown to you only) is
// shared with the settings' status.
import { html, nothing } from '/vendor/lit-all.min.js';
import { ctx } from './web-ext.js';
import { partitionState } from './model/partition.js';
import { safeUrl } from './model/project-task.js';
import { repoSlug, canSignin } from './model/projects.js';
import { EGRESS } from './model/sandboxes.js';

// the sandbox part's effective choice: what the form says, else the first
// manager, its default image and size, internet when it offers it (a
// project clones and fetches)
function sandboxOf(f, managers) {
  const sb = f.sandbox;
  const usable = (managers || []).filter((m) => m.ok !== false);
  const m = usable.find((x) => x.provider === sb.provider) || usable[0] || null;
  const egress = (m && m.egress && m.egress.length ? m.egress : ['none']);
  return {
    ...sb,
    provider: m ? m.provider : '',
    image: sb.image || ((m && m.images) || []).find((i) => i.default)?.id || '',
    size: sb.size || ((m && m.sizes) || []).find((z) => z.default)?.id || '',
    egress: sb.egress && egress.includes(sb.egress) ? sb.egress : egress.includes('internet') ? 'internet' : egress[0],
    m, usable, egressOpts: egress,
  };
}

export function newProjectTpl(p) {
  const app = ctx.app;
  const f = p.form;
  app.sbx.ensure('', '');
  const list = app.sbx.listAt('');
  const sb = sandboxOf(f, list.managers);
  const team = partitionState() === 'global';
  // yours to pick: a private one for a project (its credentials are yours);
  // a team-visible one for a team's seed — members' sandboxes fork from it
  // only when they can see it (API.md §Projects in the UI), and an existing sandbox
  // keeps its own visibility
  const mine = (list.sandboxes || []).filter((s) => s.mine && (s.visibility === 'team') === team && !['deleting', 'archived', 'error'].includes(s.state));
  const provs = (p.scm && p.scm.providers) || [];
  const prov = p.provider(f.scm);
  const set = (k) => (e) => p.setForm(k, e.target.value);
  const setSb = (k) => (e) => p.setFormPart('sandbox', k, e.target.value);
  const setPol = (k) => (e) => p.setFormPart('policy', k, e.target.value);
  const create = () => { p.form.sandbox = { mode: sb.mode, ref: sb.ref, provider: sb.provider, image: sb.image, size: sb.size, egress: sb.egress }; p.saveProject(); };
  return html`<div class="autos-page projs-page" id="proj-form">
    <div class="ahd"><a class="crumb" @click=${() => p.closeForm()}>Projects</a> › <b>New project</b></div>

    <h5>Where the code is</h5>
    ${p.scm && p.scm.error ? html`<div class="err">${p.scm.error}</div>` : nothing}
    ${p.scm && !p.scm.error && !provs.length ? html`<div class="note">No scm provider is bound — bind one (such as the scm-github template) to this agent's scm slot.</div>` : nothing}
    ${provs.length ? html`<div class="field"><label>Provider</label><select id="pn-scm" @change=${(e) => { p.setForm('scm', e.target.value); p.searchRepos(); }}>
      ${provs.map((x) => html`<option value=${x.scm} ?selected=${x.scm === f.scm} ?disabled=${!!x.error}>${x.title || x.scm}${x.error ? ' — ' + x.error : ''}</option>`)}</select>
      ${prov ? providerNoteTpl(p, prov) : nothing}</div>` : nothing}

    ${f.scm ? html`<div class="field"><label>Repos</label>
      <div class="pnrepos">${f.repos.map((r) => html`<div class="pnrepo" data-repo=${r.repo}>
          <div class="pnrh"><b class="mono">${r.repo}</b><span class="muted small">→ ${repoSlug(r.repo)}</span><span style="flex:1"></span>
            <button class="btn ghost btnsm" title="leave it out" @click=${() => p.dropFormRepo(r.repo)}>✕</button></div>
          <textarea rows="2" class="mono" placeholder="a setup script for each task's checkout (optional): npm ci" .value=${r.setup}
            @input=${(e) => p.setFormSetup(r.repo, e.target.value)}></textarea></div>`)}</div>
      <input type="search" id="pn-q" placeholder="Find a repo you can reach…" .value=${f.q} @input=${set('q')} @change=${() => p.searchRepos()}>
      <div class="pnresults">
        ${f.results.map((r) => {
          const name = `${r.owner}/${r.name}`;
          const added = f.repos.some((x) => x.repo === name);
          return html`<div class="pnres" data-repo=${name}>
            <span class="mono">${name}</span>${r.private ? html`<span class="badge">private</span>` : nothing}
            ${r.permission && r.permission !== 'admin' ? html`<span class="muted small">${r.permission}</span>` : nothing}
            ${r.archived ? html`<span class="badge">archived</span>` : nothing}<span style="flex:1"></span>
            <button class="btn ghost btnsm" ?disabled=${added || r.archived} @click=${() => p.addFormRepo(name)}>${added ? 'Added' : 'Add'}</button></div>`;
        })}
        ${f.loading ? html`<div class="muted small">loading…</div>` : !f.results.length && !f.err ? html`<div class="muted small">No repos found.</div>` : nothing}
        ${f.next && !f.loading ? html`<button class="btn ghost btnsm" @click=${() => p.searchRepos(true)}>more</button>` : nothing}
      </div></div>` : nothing}

    <div class="field"><label>Name</label><input id="pn-name" .value=${f.name} @input=${set('name')} placeholder="Web"></div>

    <h5>${team ? 'Its seed sandbox' : 'Its sandbox'}</h5>
    ${team ? html`<div class="muted small">A team project's tasks run in each member's own space; a seed sandbox (optional) is the start their sandboxes fork from. It holds no sign-in.</div>` : nothing}
    <div class="field">
      ${team ? html`<label class="chk"><input type="radio" name="pn-sbx" value="none" .checked=${sb.mode === 'none'} @change=${() => p.setFormPart('sandbox', 'mode', 'none')}> none</label>` : nothing}
      <label class="chk"><input type="radio" name="pn-sbx" value="new" .checked=${sb.mode !== 'pick' && !(team && sb.mode === 'none')} @change=${() => p.setFormPart('sandbox', 'mode', 'new')}> a new sandbox</label>
      <label class="chk"><input type="radio" name="pn-sbx" value="pick" .checked=${sb.mode === 'pick'} ?disabled=${!mine.length}
        @change=${() => p.setFormPart('sandbox', 'mode', 'pick')}> one of your own${mine.length ? '' : team ? ' (you have no sandbox shared with the team)' : ' (you have no private sandbox)'}</label>
    </div>
    ${team && sb.mode === 'none' ? nothing : sb.mode === 'pick' ? html`<div class="field"><label>Sandbox</label><select id="pn-sbx-ref" @change=${setSb('ref')}>
        <option value="" ?selected=${!sb.ref}>pick one…</option>
        ${mine.map((s) => html`<option value=${s.ref} ?selected=${s.ref === sb.ref}>${s.name} · ${s.manager || s.provider} · ${s.state}</option>`)}</select>
        <div class="muted small">${team ? 'Shared with the team, so members\' sandboxes can fork from it; it holds no sign-in. A private sandbox of yours must be shared with the team first.' : 'Its credentials are yours: a project works only in a private sandbox of your own.'}</div></div>`
      : sb.usable.length ? html`<div class="row2">
        <div class="field"><label>Manager</label><select id="pn-sbx-mgr" @change=${setSb('provider')}>${sb.usable.map((m) => html`<option value=${m.provider} ?selected=${m.provider === sb.provider}>${m.title || m.provider}</option>`)}</select></div>
        <div class="field"><label>Image</label><select @change=${setSb('image')}>${((sb.m && sb.m.images) || []).map((i) => html`<option value=${i.id} ?selected=${i.id === sb.image}>${i.title || i.id}</option>`)}</select></div>
        <div class="field"><label>Size</label><select @change=${setSb('size')}>${((sb.m && sb.m.sizes) || []).map((z) => html`<option value=${z.id} ?selected=${z.id === sb.size}>${z.id}</option>`)}</select></div>
        <div class="field"><label>Network</label><select @change=${setSb('egress')}>${sb.egressOpts.map((e) => html`<option value=${e} ?selected=${e === sb.egress}>${EGRESS[e] || e}</option>`)}</select></div>
      </div>` : html`<div class="note">No sandbox manager is bound — bind one to this agent's sandboxes slot.</div>`}

    <h5>How it works</h5>
    <div class="row2">
      <div class="field"><label>Tasks at work at once</label><input type="number" min="1" max="16" .value=${String(f.policy.maxTasks)} @input=${setPol('maxTasks')}></div>
      <div class="field"><label>Who answers tasks</label><select @change=${setPol('engine')}>
        <option value="auto" ?selected=${f.policy.engine === 'auto'}>auto — your pick per task</option>
        <option value="builtin" ?selected=${f.policy.engine === 'builtin'}>the built-in agent</option>
        <option value="harness" ?selected=${f.policy.engine === 'harness'}>a coding agent</option></select></div>
      ${f.policy.engine === 'harness' ? html`<div class="field"><label>Coding agent</label><select @change=${setPol('harness')}>
        <option value="" ?selected=${!f.policy.harness}>the one you used last</option>
        ${(app.harness.catalog.harnesses || []).map((h) => html`<option value=${h.id} ?selected=${h.id === f.policy.harness}>${h.name}</option>`)}</select></div>` : nothing}
      <div class="field"><label>Pull requests</label><select @change=${setPol('autoPR')}>
        <option value="off" ?selected=${f.policy.autoPR === 'off'}>opened by hand (Open PR)</option>
        <option value="draft" ?selected=${f.policy.autoPR === 'draft'}>a draft when a task rests</option>
        <option value="ready" ?selected=${f.policy.autoPR === 'ready'}>ready for review when a task rests</option></select></div>
      ${prov && ((prov.you && prov.you.identities) || []).length > 1 ? html`<div class="field"><label>Work as</label><select @change=${setPol('as')}>
        <option value="" ?selected=${!f.policy.as}>the default (${prov.you.default || 'person'})</option>
        ${prov.you.identities.map((i) => html`<option value=${i} ?selected=${f.policy.as === i}>${i === 'bot' ? 'the provider\'s bot' : 'you'}</option>`)}</select></div>` : nothing}
    </div>
    ${partitionState() === 'legacy' || team ? html`<div class="row2">
      <div class="field"><label>Who can see it</label><select id="pn-vis" @change=${(e) => p.setFormPart('share', 'visibility', e.target.value)}>
        <option value="private" ?selected=${f.share.visibility !== 'team'}>${team ? 'only the members you add next' : 'only you'}</option>
        <option value="team" ?selected=${f.share.visibility === 'team'}>everyone who can open this agent</option></select></div>
      ${f.share.visibility === 'team' ? html`<div class="field"><label>They may</label><select @change=${(e) => p.setFormPart('share', 'teamRole', e.target.value)}>
        <option value="viewer" ?selected=${f.share.teamRole !== 'participant'}>read</option>
        <option value="participant" ?selected=${f.share.teamRole === 'participant'}>make and steer tasks</option></select></div>` : nothing}
    </div>` : nothing}
    ${f.err ? html`<div class="err" id="pn-err">${f.err}</div>` : nothing}
    <div><button class="btn" id="pn-create" ?disabled=${f.busy} @click=${create}>${f.busy ? 'Creating…' : 'Create project'}</button>
      <button class="btn ghost" @click=${() => p.closeForm()}>Cancel</button></div>
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
      <button class="btn ghost btnsm" id="psignin-forget" title="forget your sign-in: your projects' credentials are removed from their sandboxes first"
        @click=${() => { if (confirm(`Forget your sign-in to ${name}? Your projects' credentials are removed from their sandboxes first.`)) p.forget(scm); }}>Forget</button></div>`;
  }
  return html`<div class="psignin" data-state=${(s && s.state) || 'none'}>
    ${s && (s.err || s.error) ? html`<div class="err">${s.err || s.error}</div>` : s && ['denied', 'expired'].includes(s.state) ? html`<div class="err">The sign-in was ${s.state}.</div>` : nothing}
    <button class="btn btnsm" id="psignin-start" @click=${() => p.signin(scm)}>Sign in to ${name}</button></div>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pnrepos { display: grid; gap: 6px; margin-bottom: 6px; }
  .projs-page .pnrepo { border: 1px solid var(--bx-border); border-radius: 6px; padding: 6px 8px; background: var(--bx-panel); }
  .projs-page .pnrh { display: flex; gap: 6px; align-items: center; min-width: 0; }
  .projs-page .pnrh b { overflow-wrap: anywhere; }
  .projs-page .pnresults { max-height: 240px; overflow: auto; margin-top: 4px; }
  .projs-page .pnres { display: flex; gap: 6px; align-items: center; padding: 3px 2px; border-bottom: 1px solid var(--bx-border); min-width: 0; font-size: 12px; }
  .projs-page .pnres .mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .projs-page .pnyou { margin-top: 4px; }
  .psignin { margin: 6px 0; padding: 8px 10px; border: 1px solid var(--bx-border); border-radius: 6px; background: var(--bx-panel-2); font-size: 12.5px; }
  .psignin .pcode { font-size: 15px; letter-spacing: .12em; padding: 0 4px; }
  .psignin a { overflow-wrap: anywhere; }
`;
document.head.append(style);
