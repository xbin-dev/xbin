// project-settings.js — a project's Settings tab on the web (API.md
// §Projects in the UI):
//
//   status    its sandbox, repos (fetched, head, protected), credentials
//             (whose, until when, why one is blocked — never the token),
//             jobs and warnings; Warm; signing in to the provider (your
//             own sign-in, its device code shown to you only — offered in
//             your own partition, as the provider lets you) and Forget
//   repos     add (owner/name), remove (confirmed; again when open tasks
//             use it), each one's setup script and checkout
//   policy    every key, grouped (model/projects.js POLICY); the owner edits
//             and saves with the version read — a stale one is read again
//   members   who besides the owner, and what team visibility grants —
//             where sharing is possible (an unpartitioned agent; never a
//             person's own project in their partition, which is private)
//   project   rename, archive, delete (confirmed; its sandbox kept or deleted
//             when the project made it)
//
// Everyone who may see the project reads it; only its owner changes it.
import { html, nothing } from '/vendor/lit-all.min.js';
import { ctx } from './web-ext.js';
import { POLICY, policyGet, fieldValue, fieldText, can, sharable, canSignin, taskClassChoices } from './model/projects.js';
import { ago } from './model/auto.js';
import { signinTpl } from './project-new.js';

const seen = new Set(); // projects whose provider and sign-in were read for the tab

export function settingsTpl(p, pv) {
  const c = can(pv);
  if (!seen.has(pv.id)) {
    seen.add(pv.id);
    p.providers().then(() => { if (canSignin(p.provider(pv.scm))) p.signinState(pv.scm); }).catch(() => {});
  }
  return html`<div class="psettings">
    ${statusTpl(p, pv, c)}
    ${reposTpl(p, pv, c)}
    ${policyTpl(p, pv, c)}
    ${membersTpl(p, pv, c)}
    ${projectTpl(p, pv, c)}
  </div>`;
}

// --- status ------------------------------------------------------------------------------

const when = (ms) => (ms ? ago(Math.round(ms / 1000)) : 'never');
const until = (ms) => (!ms ? '' : ms < Date.now() ? 'expired' : `until ${new Date(ms).toLocaleTimeString()}`);
const prot = (b) => (b === true ? 'protected' : b === false ? 'not protected' : 'protection unknown');

function statusTpl(p, pv, c) {
  const s = p.statusOf.get(pv.id);
  const prov = p.provider(pv.scm);
  return html`<section class="pset" id="pset-status"><h5>Status</h5>
    ${!s ? html`<div class="muted small">loading…</div>` : html`
      ${(s.warnings || []).map((w) => html`<div class="note pwarn" data-kind=${w.kind}>${w.repo ? html`<b class="mono">${w.repo}</b>: ` : nothing}${w.text}</div>`)}
      <div class="pkv"><span>Sandbox</span><span>${s.sandbox && s.sandbox.ref ? html`<b>${s.sandbox.name || s.sandbox.ref}</b> · ${s.sandbox.state}
        ${s.sandbox.workdir ? html` · <span class="mono">${pv.dir || s.sandbox.workdir}</span>` : nothing}${s.sandbox.shared ? html` · <span class="err">shared — no credentials go in</span>` : nothing}` : 'none yet'}</span></div>
      <div class="pkv"><span>Slots</span><span>${s.slots ? `${s.slots.used} of ${s.slots.max} at work` : ''}</span></div>
      <table class="ptable" id="pstatus-repos"><tr><th>Repo</th><th>State</th><th>Fetched</th><th>Head</th><th>Base branch</th></tr>
        ${(s.repos || []).map((r) => html`<tr data-repo=${r.slug}><td class="mono">${r.slug}</td><td>${r.state}${r.error ? html` <span class="err">${r.error}</span>` : nothing}</td>
          <td>${when(r.fetchedMs)}</td><td class="mono">${(r.head || '').slice(0, 9)}</td><td>${prot(r.protected)}</td></tr>`)}</table>
      <table class="ptable" id="pstatus-creds"><tr><th>Credential</th><th>Whose</th><th>State</th><th>Until</th></tr>
        ${(s.creds || []).length ? s.creds.map((k) => html`<tr><td>${k.host} in ${k.sandbox}</td><td>${k.identity === 'bot' ? 'the bot' : k.login || 'you'}${k.identity === 'bot' && k.login ? ` (${k.login})` : ''}</td>
          <td>${k.state}${k.why ? html` <span class="muted">— ${k.why}</span>` : nothing}</td><td>${until(k.expiresMs)}</td></tr>`)
          : html`<tr><td colspan="4" class="muted">none written yet</td></tr>`}</table>
      <table class="ptable" id="pstatus-jobs"><tr><th>Job</th><th>State</th><th>Doing</th><th>When</th></tr>
        ${(s.jobs || []).length ? s.jobs.map((j) => html`<tr data-job=${j.id}><td>${j.kind}${j.repo ? ` · ${j.repo}` : ''}${j.task ? ` · task` : ''}</td>
          <td>${j.state}${j.attempts > 1 ? ` (try ${j.attempts})` : ''}</td><td>${j.step || ''}${j.error ? html` <span class="err">${j.error}</span>` : nothing}</td><td>${when(j.updated)}</td></tr>`)
          : html`<tr><td colspan="4" class="muted">none</td></tr>`}</table>`}
    <div class="pbtns">
      ${c.act ? html`<button class="btn ghost btnsm" id="pset-warm" title="start the sandbox, fetch the repos and refresh the credentials" @click=${() => p.warm(pv.id)}>Warm</button>` : nothing}
      <button class="btn ghost btnsm" @click=${() => p.status(pv.id).catch((e) => p.fail(e))}>Refresh</button>
    </div>
    ${canSignin(prov) ? html`<div class="pkv"><span>Your sign-in</span><span>${signinTpl(p, pv.scm, prov.title)}</span></div>` : nothing}
  </section>`;
}

// --- repos ---------------------------------------------------------------------------------

const repoEdits = new Map(); // "pid:slug" → the setup script being edited
const newRepo = new Map(); // pid → the repo being typed: a project's, never carried to the next one's form

function reposTpl(p, pv, c) {
  const key = (r) => `${pv.id}:${r.slug}`;
  const remove = async (r) => {
    if (!confirm(`Remove ${r.repo} from the project? Its base clone goes; tasks' branches stay on the remote.`)) return;
    const res = await p.removeRepo(pv.id, r.slug);
    if (res.busy && confirm(`${res.busy}\n\nRemove it anyway? Open tasks that use it lose their checkout.`)) await p.removeRepo(pv.id, r.slug, true);
  };
  return html`<section class="pset" id="pset-repos"><h5>Repos</h5>
    ${(pv.repos || []).map((r) => {
      const edit = repoEdits.has(key(r)) ? repoEdits.get(key(r)) : r.setup || '';
      return html`<div class="prepo" data-repo=${r.slug}>
        <div class="pnrh"><b class="mono">${r.repo}</b><span class="muted small">${r.slug} · ${r.state}${r.defaultBranch ? ` · ${r.defaultBranch}` : ''}${r.mode === 'adopted' ? ' · adopted' : ''}</span>
          <span style="flex:1"></span>
          ${c.settings ? html`<select title="how a task checks it out" @change=${(e) => p.setCheckout(pv.id, r.slug, e.target.value)}>
            <option value="worktree" ?selected=${r.checkout !== 'clone'}>worktree</option><option value="clone" ?selected=${r.checkout === 'clone'}>clone</option></select>
            <button class="btn rm btnsm" data-act="remove" @click=${() => remove(r)}>Remove</button>` : nothing}</div>
        ${r.error ? html`<div class="err">${r.error}</div>` : nothing}
        <div class="field"><label>Setup script — runs in each task's checkout</label>
          <textarea rows="2" class="mono" .value=${edit} ?disabled=${!c.settings} placeholder="npm ci"
            @input=${(e) => { repoEdits.set(key(r), e.target.value); ctx.paint(); }}></textarea></div>
        ${c.settings && edit !== (r.setup || '') ? html`<button class="btn btnsm" data-act="setup" @click=${async () => { await p.setSetup(pv.id, r.slug, edit); repoEdits.delete(key(r)); }}>Save setup</button>` : nothing}
      </div>`;
    })}
    ${c.settings ? html`<div class="pbtns"><input id="pset-addrepo" placeholder="owner/name" .value=${newRepo.get(pv.id) || ''} @input=${(e) => { newRepo.set(pv.id, e.target.value); }}>
      <button class="btn btnsm" id="pset-addrepo-go" @click=${async () => { const r = (newRepo.get(pv.id) || '').trim(); if (!r) return; newRepo.delete(pv.id); await p.addRepo(pv.id, r); }}>Add repo</button></div>` : nothing}
  </section>`;
}

// --- the policy ------------------------------------------------------------------------------

function fieldTpl(p, f, val, editable) {
  const set = (raw) => p.setPolicy(f.path, fieldValue(f, raw));
  const id = 'pol-' + f.path.replace(/\./g, '-');
  if (f.type === 'bool') {
    return html`<label class="chk"><input type="checkbox" id=${id} .checked=${!!val} ?disabled=${!editable} @change=${(e) => set(e.target.checked)}> ${f.label}</label>`;
  }
  let input;
  if (f.type === 'area' || f.type === 'lines') {
    input = html`<textarea id=${id} rows="2" class=${f.type === 'lines' ? 'mono' : ''} .value=${fieldText(f, val)} ?disabled=${!editable} @change=${(e) => set(e.target.value)}></textarea>`;
  } else if (f.type === 'number') {
    input = html`<input id=${id} type="number" min=${f.min ?? ''} max=${f.max ?? ''} .value=${fieldText(f, val)} ?disabled=${!editable} @change=${(e) => set(e.target.value)}>`;
  } else if (f.type === 'select' || f.type === 'class' || f.type === 'harness') {
    const opts = f.type === 'select' ? f.options
      : f.type === 'class' ? taskClassChoices(ctx.app.classes, val).map((o) => [o.value, o.label])
        : [['', 'the one you used last'], ...(ctx.app.harness.catalog.harnesses || []).map((h) => [h.id, h.name])];
    const has = opts.some(([v]) => v === (val ?? ''));
    input = html`<select id=${id} ?disabled=${!editable} @change=${(e) => set(e.target.value)}>
      ${has ? nothing : html`<option value=${val ?? ''} selected>${val ?? ''}</option>`}
      ${opts.map(([v, l]) => html`<option value=${v} ?selected=${v === (val ?? '')}>${l}</option>`)}</select>`;
  } else {
    input = html`<input id=${id} .value=${fieldText(f, val)} ?disabled=${!editable} @change=${(e) => set(e.target.value)}>`;
  }
  return html`<div class="field"><label for=${id}>${f.label}</label>${input}${f.hint ? html`<div class="muted small">${f.hint}</div>` : nothing}</div>`;
}

function policyTpl(p, pv, c) {
  const d = p.draft && p.draft.pid === pv.id ? p.draft : null;
  const pol = d ? d.policy : pv.policy || {};
  if (c.settings) ctx.app.harness.ensure();
  return html`<section class="pset" id="pset-policy"><h5>Policy</h5>
    ${POLICY.map((g) => html`<details class="pgroup" data-group=${g.key} ?open=${g.key === 'tasks'}><summary>${g.title}</summary>
      <div class="pfields">${g.fields.map((f) => fieldTpl(p, f, policyGet(pol, f.path), c.settings))}</div></details>`)}
    ${d && d.err ? html`<div class="err" id="pol-err">${d.err}</div>` : nothing}
    ${d && d.saved ? html`<div class="muted small" id="pol-saved">Saved.</div>` : nothing}
    ${c.settings ? html`<div class="pbtns"><button class="btn btnsm" id="pol-save" ?disabled=${!(d && d.dirty)} @click=${() => p.savePolicy()}>Save policy</button>
      ${d && d.dirty ? html`<button class="btn ghost btnsm" @click=${() => p.editPolicy(pv.id)}>Undo changes</button>` : nothing}</div>`
      : html`<div class="muted small">Only the project's owner changes its policy.</div>`}
  </section>`;
}

// --- members ---------------------------------------------------------------------------------

const newMember = new Map(); // pid → {user, role} being typed: an ACL change is only ever made to the project it was typed for

function membersTpl(p, pv, c) {
  if (!sharable(pv)) {
    return html`<section class="pset" id="pset-members"><h5>Members</h5>
      <div class="muted small">${pv.kind === 'membership' ? 'Your own half of a team project: the team project\'s owner keeps its members.'
        : 'A project in your own space is yours alone — its tasks use your own sign-in. A team project is shared instead.'}</div></section>`;
  }
  const m = p.membersOf.get(pv.id);
  const nm = newMember.get(pv.id) || { user: '', role: 'participant' };
  const setNm = (k, v) => newMember.set(pv.id, { ...(newMember.get(pv.id) || nm), [k]: v });
  return html`<section class="pset" id="pset-members"><h5>Members</h5>
    <div class="pkv"><span>Owner</span><span>${(m && m.owner) || pv.owner}</span></div>
    ${((m && m.members) || []).map((x) => html`<div class="pkv pmember" data-user=${x.user}><span>${x.user}</span><span>${x.role === 'viewer' ? 'reads' : 'makes and steers tasks'}
      ${c.settings || x.user === ctx.app.me.user ? html`<button class="btn ghost btnsm" @click=${() => p.removeMember(pv.id, x.user)}>${x.user === ctx.app.me.user ? 'Leave' : 'Remove'}</button>` : nothing}</span></div>`)}
    ${c.settings ? html`<div class="pbtns"><input id="pset-member" placeholder="person" .value=${nm.user} @input=${(e) => setNm('user', e.target.value)}>
      <select id="pset-member-role" @change=${(e) => setNm('role', e.target.value)}><option value="participant" ?selected=${nm.role === 'participant'}>makes and steers tasks</option>
        <option value="viewer" ?selected=${nm.role === 'viewer'}>reads</option></select>
      <button class="btn btnsm" id="pset-member-add" @click=${async () => {
        const cur = newMember.get(pv.id) || nm; const u = cur.user.trim(); if (!u) return;
        newMember.set(pv.id, { user: '', role: cur.role }); await p.addMember(pv.id, u, cur.role); }}>Add</button></div>
      <div class="row2"><div class="field"><label>Everyone who can open this agent</label>
        <select id="pset-vis" @change=${(e) => p.share(pv.id, e.target.value === 'private' ? 'private' : 'team', e.target.value === 'participant' ? 'participant' : 'viewer', pv.version)}>
          <option value="private" ?selected=${pv.visibility !== 'team'}>doesn't see it</option>
          <option value="viewer" ?selected=${pv.visibility === 'team' && pv.teamRole !== 'participant'}>reads it</option>
          <option value="participant" ?selected=${pv.visibility === 'team' && pv.teamRole === 'participant'}>makes and steers tasks</option></select></div></div>` : nothing}
  </section>`;
}

// --- the project itself ------------------------------------------------------------------------

const delSandbox = new Map(); // pid → 'keep' | 'delete': a project's choice, never the next one's
let newName = null; // {pid, text, version}: the name being typed, and the version it began at

function projectTpl(p, pv, c) {
  if (!c.settings) return nothing;
  const del = () => {
    const sbx = delSandbox.get(pv.id) || 'keep';
    const sb = pv.sandboxMade && sbx === 'delete' ? ' and its sandbox' : ' (its sandbox stays)';
    if (!confirm(`Delete the project "${pv.name}"${sb}? Its task conversations are deleted, their workspaces cleaned up.`)) return;
    p.remove(pv.id, pv.sandboxMade ? sbx : 'keep');
  };
  const nm = newName && newName.pid === pv.id ? newName : null;
  // a stale version (someone renamed it meanwhile) is said; the typed name stays, at the version read now
  const rename = async () => {
    const cur = newName && newName.pid === pv.id ? newName : null;
    if (!cur || !cur.text.trim() || cur.text.trim() === pv.name) { newName = null; return; }
    const r = await p.rename(pv.id, cur.text.trim(), cur.version);
    const now = p.find(pv.id);
    newName = r || !now ? null : { ...cur, version: now.version };
    ctx.paint();
  };
  return html`<section class="pset" id="pset-project"><h5>The project</h5>
    <div class="pbtns"><input id="pset-name" .value=${nm ? nm.text : pv.name} @input=${(e) => { const c = newName && newName.pid === pv.id ? newName : null; newName = { pid: pv.id, text: e.target.value, version: c ? c.version : pv.version }; }}>
      <button class="btn ghost btnsm" @click=${rename}>Rename</button></div>
    <div class="pbtns">
      ${pv.state === 'archived' ? html`<button class="btn ghost btnsm" id="pset-unarchive" @click=${() => p.archive(pv.id, false)}>Unarchive</button>`
        : html`<button class="btn ghost btnsm" id="pset-archive" title="its credentials are removed, its fetches and queue stop; everything else is kept"
          @click=${() => { if (confirm(`Archive "${pv.name}"? Its credentials are removed from the sandbox; nothing else goes.`)) p.archive(pv.id, true); }}>Archive</button>`}
    </div>
    <div class="pbtns">
      ${pv.sandboxMade ? html`<select id="pset-del-sbx" @change=${(e) => { delSandbox.set(pv.id, e.target.value); }}>
        <option value="keep" ?selected=${delSandbox.get(pv.id) !== 'delete'}>keep its sandbox</option>
        <option value="delete" ?selected=${delSandbox.get(pv.id) === 'delete'}>delete its sandbox too</option></select>` : nothing}
      <button class="btn rm btnsm" id="pset-delete" @click=${del}>Delete project</button></div>
  </section>`;
}

const style = document.createElement('style');
style.textContent = `
  .psettings .pset { border-top: 1px solid var(--bx-border); padding: 4px 0 10px; }
  .psettings .pkv { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 8px; font-size: 12.5px; padding: 3px 0; align-items: baseline; }
  .psettings .pkv > span:first-child { color: var(--bx-muted); }
  .psettings .ptable { width: 100%; border-collapse: collapse; font-size: 12px; margin: 6px 0; table-layout: fixed; }
  .psettings .ptable th { text-align: left; font-weight: 600; color: var(--bx-muted); font-size: 10.5px; }
  .psettings .ptable td, .psettings .ptable th { padding: 3px 4px; border-bottom: 1px solid var(--bx-border); overflow-wrap: anywhere; }
  .psettings .pbtns { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 6px 0; }
  .psettings .pbtns input { max-width: 240px; }
  .psettings .prepo { border: 1px solid var(--bx-border); border-radius: 6px; padding: 6px 8px; margin-bottom: 6px; background: var(--bx-panel); }
  .psettings .pgroup { margin: 4px 0; }
  .psettings .pgroup summary { cursor: pointer; font-weight: 600; font-size: 12.5px; padding: 3px 0; }
  .psettings .pfields { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 4px 12px; padding: 4px 0 6px; }
  .psettings .pwarn { border-left-color: var(--bx-yellow, #d9a441); }
`;
document.head.append(style);
