// native/project-settings.js — a project's settings in the native view
// (API.md §Projects in the UI), the web's project-settings.js with the
// app's primitives — the pushed screen 'project-settings' {pid}:
//
//   Status     its sandbox, repos (fetched, head, protected), credentials
//              (whose, until when, why one is blocked — never the token),
//              jobs and warnings; Warm; your sign-in to the provider (its
//              device code shown to you only — in your own partition, as the
//              provider lets you) and Forget
//   Repos      each one's setup script and checkout, Remove (confirmed; again,
//              with force, when open tasks use it); Add repo
//   Policy     every key in its group (model/projects.js POLICY); the owner
//              edits and saves with the version the edit began at
//   Members    where sharing is possible, and what team visibility grants
//   The project  rename, archive or unarchive, delete (its sandbox kept or
//              deleted when the project made it), confirmed
//
// signinTpl is shared with the new-project screen (native/projects.js).
// Everyone who may see the project reads it; only its owner changes it.
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, ui, when } from './ui.js';
import { POLICY, policyGet, fieldValue, fieldText, can, sharable, canSignin, taskClassChoices } from '../model/projects.js';
import { openUrl } from './project-task.js';

const pj = () => ctx.app.projects;

ext.register({ screen: (s) => (s.kind === 'project-settings' ? settingsTpl(s) : null) });

const until = (ms) => (!ms ? '' : ms < Date.now() ? 'expired' : `until ${new Date(ms).toLocaleTimeString()}`);
const prot = (b) => (b === true ? 'protected' : b === false ? 'not protected' : 'protection unknown');

function settingsTpl(s) {
  const p = pj();
  const pv = p.find(s.pid);
  if (!pv) return html`<screen title="Settings" style="form"><section><progress label="loading…"/></section></screen>`;
  const c = can(pv);
  if (!s.loaded) {
    s.loaded = true;
    p.loadSettings(pv.id).catch(() => {});
    p.providers().then(() => { if (canSignin(p.provider(pv.scm))) p.signinState(pv.scm); }).catch(() => {});
  }
  return html`<screen title="Settings" subtitle=${pv.name} style="form" refreshable @refresh=${() => p.loadSettings(pv.id)}>
    ${p.err ? html`<section><notice tone="danger" text=${p.err}/></section>` : nothing}
    ${statusTpl(p, pv, c)}
    ${reposTpl(s, p, pv, c)}
    ${policyTpl(s, p, pv, c)}
    ${membersTpl(s, p, pv, c)}
    ${projectTpl(s, p, pv, c)}
  </screen>`;
}

// --- status -------------------------------------------------------------------------------------

function statusTpl(p, pv, c) {
  const st = p.statusOf.get(pv.id);
  const prov = p.provider(pv.scm);
  if (!st) return html`<section title="Status"><progress label="loading…"/></section>`;
  const sb = st.sandbox || {};
  return html`<section title="Status" footer=${st.slots ? `${st.slots.used} of ${st.slots.max} at work` : ''}>
    ${(st.warnings || []).map((w) => html`<notice tone="warn" title=${w.repo || nothing} text=${w.text}/>`)}
    <row title="Sandbox" subtitle=${sb.ref ? [sb.name || sb.ref, sb.state, pv.dir || sb.workdir, sb.shared ? 'shared — no credentials go in' : ''].filter(Boolean).join(' · ') : 'none yet'} icon="box"/>
    ${repeat(st.repos || [], (r) => r.slug, (r) => html`<row title=${r.slug} subtitle=${[r.state, r.fetchedMs ? `fetched ${when(r.fetchedMs)}` : 'never fetched', (r.head || '').slice(0, 9), prot(r.protected), r.error].filter(Boolean).join(' · ')}
      icon="folder" tone=${r.error ? 'danger' : r.protected === false ? 'warn' : nothing}/>`)}
    ${(st.creds || []).length ? st.creds.map((k) => html`<row title=${`${k.host} in ${k.sandbox}`}
      subtitle=${[k.identity === 'bot' ? `the bot${k.login ? ` (${k.login})` : ''}` : k.login || 'you', k.state, k.why, until(k.expiresMs)].filter(Boolean).join(' · ')} icon="key"/>`)
      : html`<row title="Credentials" subtitle="none written yet" icon="key"/>`}
    ${repeat(st.jobs || [], (j) => j.id, (j) => html`<row title=${`${j.kind}${j.repo ? ` · ${j.repo}` : ''}${j.task ? ' · task' : ''}`}
      subtitle=${[j.state + (j.attempts > 1 ? ` (try ${j.attempts})` : ''), j.step, j.error, when(j.updated)].filter(Boolean).join(' · ')} icon="clock" tone=${j.state === 'failed' ? 'danger' : nothing}/>`)}
    ${c.act ? html`<button icon="bolt" @tap=${() => p.warm(pv.id)}>Warm</button>` : nothing}
    ${canSignin(prov) ? signinTpl(p, prov) : nothing}
  </section>`;
}

/**
 * signinTpl(p, provider): signing in to a provider — Sign in, then its page
 * (a link) and the device code (yours only: your own sign-in, read from your
 * own space), its state while pending, and Forget once signed in. Nothing
 * where you may not sign in (model/projects.js canSignin).
 */
export function signinTpl(p, prov) {
  if (!canSignin(prov)) return nothing;
  const scm = prov.scm;
  const name = prov.title || scm;
  const s = p.signinOf(scm);
  if (s && s.state === 'pending' && s.signin) {
    const url = /^https:/i.test(s.signin.url || '') ? s.signin.url : '';
    return html`<row title=${`Enter ${s.signin.userCode}`} subtitle=${`at ${url || 'the sign-in page'} — waiting for you to approve it there (only you see this code)`} icon="key" tone="accent"
      ?nav=${!!url} @tap=${() => openUrl(url)}>
      <actions><button icon="copy" copy=${s.signin.userCode}>Copy the code</button></actions></row>`;
  }
  if ((s && s.state === 'done') || (prov.you && prov.you.person)) {
    const login = (s && s.identity && s.identity.login) || (prov.you && prov.you.person && prov.you.person.login) || '';
    return html`<row title=${`Signed in to ${name}${login ? ` as ${login}` : ''}`} icon="check" tone="ok">
      <actions><button icon="xmark" role="destructive" confirm=${{ title: `Forget your sign-in to ${name}?`, message: 'Your projects\' credentials are removed from their sandboxes first.', label: 'Forget', destructive: true }}
        @tap=${() => p.forget(scm)}>Forget</button></actions></row>`;
  }
  const why = s && (s.err || s.error) ? s.err || s.error : s && ['denied', 'expired'].includes(s.state) ? `The sign-in was ${s.state}.` : '';
  return html`${why ? html`<notice tone="danger" text=${why}/>` : nothing}<button icon="key" @tap=${() => p.signin(scm)}>${`Sign in to ${name}`}</button>`;
}

// --- repos ------------------------------------------------------------------------------------------

function reposTpl(s, p, pv, c) {
  s.setup = s.setup || {};
  const remove = (r, force) => async () => {
    const res = await p.removeRepo(pv.id, r.slug, force);
    s.busy = res && res.busy ? { slug: r.slug, text: res.busy } : null;
    ctx.paint();
  };
  return html`<section title="Repos" footer=${c.settings ? 'A setup script runs in each task\'s checkout.' : 'Only the project\'s owner changes its repos.'}>
    ${repeat(pv.repos || [], (r) => r.slug, (r) => {
      const edit = r.slug in s.setup ? s.setup[r.slug] : r.setup || '';
      return html`<row title=${r.repo} subtitle=${[r.slug, r.state, r.defaultBranch, r.mode === 'adopted' ? 'adopted' : '', r.error].filter(Boolean).join(' · ')} icon="folder" tone=${r.error ? 'danger' : nothing}>
        ${c.settings ? html`<actions><button icon="trash" role="destructive" confirm=${{ title: `Remove ${r.repo} from the project?`, message: 'Its base clone goes; tasks\' branches stay on the remote.', label: 'Remove', destructive: true }}
          @tap=${remove(r, false)}>Remove</button></actions>` : nothing}</row>
        ${s.busy && s.busy.slug === r.slug ? html`<notice tone="warn" text=${s.busy.text}/>
          <button role="destructive" confirm=${{ title: `Remove ${r.repo} anyway?`, message: 'Open tasks that use it lose their checkout.', label: 'Remove anyway', destructive: true }} @tap=${remove(r, true)}>Remove anyway</button>` : nothing}
        ${c.settings ? html`<picker label="Checkout" value=${r.checkout === 'clone' ? 'clone' : 'worktree'} options=${[{ value: 'worktree', label: 'a git worktree' }, { value: 'clone', label: 'a clone' }]}
          @change=${(e) => p.setCheckout(pv.id, r.slug, e.value)}/>` : nothing}
        <field kind="multiline" label=${`Setup for ${r.slug}`} placeholder="npm ci" value=${edit} ?disabled=${!c.settings} @input=${(e) => { s.setup[r.slug] = e.value; ctx.paint(); }}/>
        ${c.settings && edit !== (r.setup || '') ? html`<button @tap=${async () => { await p.setSetup(pv.id, r.slug, edit); delete s.setup[r.slug]; ctx.paint(); }}>${`Save setup for ${r.slug}`}</button>` : nothing}`;
    })}
    ${c.settings ? html`<field label="Add a repo" placeholder="owner/name" value=${s.newRepo || ''} @input=${(e) => { s.newRepo = e.value; }}/>
      <button icon="plus" @tap=${async () => { const r = String(s.newRepo || '').trim(); if (!r) return; s.newRepo = ''; await p.addRepo(pv.id, r); }}>Add repo</button>` : nothing}
  </section>`;
}

// --- the policy ---------------------------------------------------------------------------------------

function fieldTpl(p, f, val, editable) {
  const set = (raw) => p.setPolicy(f.path, fieldValue(f, raw));
  if (f.type === 'bool') return html`<toggle label=${f.label} value=${!!val} ?disabled=${!editable} @change=${(e) => set(e.value)}/>`;
  if (f.type === 'select' || f.type === 'class' || f.type === 'harness') {
    const opts = f.type === 'select' ? f.options.map(([value, label]) => ({ value, label }))
      : f.type === 'class' ? taskClassChoices(ctx.app.classes, val).map((o) => ({ value: o.value, label: o.label }))
        : [{ value: '', label: 'the one you used last' }, ...(ctx.app.harness.catalog.harnesses || []).map((h) => ({ value: h.id, label: h.name }))];
    const v = val ?? '';
    const all = opts.some((o) => o.value === v) ? opts : [{ value: v, label: String(v) }, ...opts];
    return html`<picker label=${f.label} value=${v} options=${all} @change=${(e) => { if (editable) set(e.value); }}/>`;
  }
  const kind = f.type === 'area' || f.type === 'lines' ? 'multiline' : f.type === 'number' ? 'number' : 'text';
  return html`<field kind=${kind} label=${f.label} hint=${f.hint || nothing} value=${fieldText(f, val)} ?disabled=${!editable} @change=${(e) => set(e.value)}/>`;
}

function policyTpl(s, p, pv, c) {
  const d = p.draft && p.draft.pid === pv.id ? p.draft : null;
  s.open = s.open || new Set(['tasks']); // the groups unfolded (the first one at first)
  const pol = d ? d.policy : pv.policy || {};
  if (c.settings) ctx.app.harness.ensure();
  return html`${repeat(POLICY, (g) => g.key, (g, i) => html`<section title=${`Policy — ${g.title}`} collapsible ?collapsed=${!s.open.has(g.key)}
      @toggle=${(e) => { if (e.collapsed) s.open.delete(g.key); else s.open.add(g.key); ctx.paint(); }}
      footer=${i === POLICY.length - 1 ? (d && d.err) || (d && d.saved ? 'Saved.' : c.settings ? '' : 'Only the project\'s owner changes its policy.') : nothing}>
      ${repeat(g.fields, (f) => f.path, (f) => fieldTpl(p, f, policyGet(pol, f.path), c.settings))}
    </section>`)}
    ${c.settings ? html`<section>
      ${d && d.err ? html`<notice tone="danger" text=${d.err}/>` : nothing}
      <button role="primary" ?disabled=${!(d && d.dirty)} @tap=${() => p.savePolicy()}>Save policy</button>
      ${d && d.dirty ? html`<button @tap=${() => p.editPolicy(pv.id)}>Undo changes</button>` : nothing}
    </section>` : nothing}`;
}

// --- members ----------------------------------------------------------------------------------------------

function membersTpl(s, p, pv, c) {
  if (!sharable(pv)) {
    return html`<section title="Members" footer=${pv.kind === 'membership' ? 'Your own half of a team project: the team project\'s owner keeps its members.'
      : 'A project in your own space is yours alone — its tasks use your own sign-in. A team project is shared instead.'}/>`;
  }
  const m = p.membersOf.get(pv.id);
  const me = ctx.app.me.user;
  s.member = s.member || { user: '', role: 'participant' };
  return html`<section title="Members">
    <row title=${(m && m.owner) || pv.owner} subtitle="owner" icon="person"/>
    ${repeat((m && m.members) || [], (x) => x.user, (x) => html`<row title=${x.user} subtitle=${x.role === 'viewer' ? 'reads' : 'makes and steers tasks'} icon="person">
      ${c.settings || x.user === me ? html`<actions><button icon="xmark" role="destructive"
        confirm=${x.user === me ? { title: `Leave ${pv.name}?`, message: 'Only its owner can add you back.', label: 'Leave', destructive: true }
          : { title: `Remove ${x.user} from ${pv.name}?`, message: 'They lose their access; you can add them back.', label: 'Remove', destructive: true }}
        @tap=${() => p.removeMember(pv.id, x.user)}>${x.user === me ? 'Leave' : 'Remove'}</button></actions>` : nothing}</row>`)}
    ${c.settings ? html`<field label="Add a person" placeholder="person" value=${s.member.user} @input=${(e) => { s.member.user = e.value; }}/>
      <picker label="They may" value=${s.member.role} options=${[{ value: 'participant', label: 'make and steer tasks' }, { value: 'viewer', label: 'read' }]} @change=${(e) => { s.member.role = e.value; ctx.paint(); }}/>
      <button icon="plus" @tap=${async () => { const u = s.member.user.trim(); if (!u) return; const role = s.member.role; s.member = { user: '', role }; await p.addMember(pv.id, u, role); }}>Add</button>
      <picker label="Everyone who can open this agent" value=${pv.visibility !== 'team' ? 'private' : pv.teamRole === 'participant' ? 'participant' : 'viewer'}
        options=${[{ value: 'private', label: 'doesn\'t see it' }, { value: 'viewer', label: 'reads it' }, { value: 'participant', label: 'makes and steers tasks' }]}
        @change=${(e) => p.share(pv.id, e.value === 'private' ? 'private' : 'team', e.value === 'participant' ? 'participant' : 'viewer', pv.version)}/>` : nothing}
  </section>`;
}

// --- the project itself ---------------------------------------------------------------------------------------

function projectTpl(s, p, pv, c) {
  if (!c.settings) return nothing;
  const name = s.name && s.name.pid === pv.id ? s.name : null;
  const rename = async () => {
    const cur = s.name;
    if (!cur || !cur.text.trim() || cur.text.trim() === pv.name) { s.name = null; return; }
    const r = await p.rename(pv.id, cur.text.trim(), cur.version);
    const now = p.find(pv.id);
    s.name = r || !now ? null : { ...cur, version: now.version };
    ctx.paint();
  };
  const sbx = s.delSandbox || 'keep';
  const del = async () => {
    const ok = await p.remove(pv.id, pv.sandboxMade ? sbx : 'keep');
    if (ok) { const i = ui.stack.findIndex((x) => x.kind === 'projects'); ui.stack.length = i >= 0 ? i + 1 : 0; ctx.paint(); }
  };
  return html`<section title="The project">
    <field label="Name" value=${name ? name.text : pv.name} @input=${(e) => { s.name = { pid: pv.id, text: e.value, version: name ? name.version : pv.version }; }}/>
    <button @tap=${rename}>Rename</button>
    ${pv.state === 'archived' ? html`<button icon="archive" @tap=${() => p.archive(pv.id, false)}>Unarchive</button>`
      : html`<button icon="archive" confirm=${{ title: `Archive "${pv.name}"?`, message: 'Its credentials are removed from the sandbox; its fetches and queue stop; nothing else goes.', label: 'Archive' }}
        @tap=${() => p.archive(pv.id, true)}>Archive</button>`}
    ${pv.sandboxMade ? html`<picker label="When it is deleted" value=${sbx} options=${[{ value: 'keep', label: 'keep its sandbox' }, { value: 'delete', label: 'delete its sandbox too' }]}
      @change=${(e) => { s.delSandbox = e.value; ctx.paint(); }}/>` : nothing}
    <button icon="trash" role="destructive" confirm=${{ title: `Delete the project "${pv.name}"${pv.sandboxMade && sbx === 'delete' ? ' and its sandbox' : ' (its sandbox stays)'}?`,
      message: 'Its task conversations are deleted, their workspaces cleaned up.', label: 'Delete project', destructive: true }} @tap=${del}>Delete project</button>
  </section>`;
}
