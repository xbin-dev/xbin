// native/project-team.js — team projects in the native view (API.md
// §Projects in the UI), the web's project-team.js with the app's
// primitives — the pushed screen 'project-team' {pid}:
//
//   a team project's definition: its members' tasks by column (each row's
//   member, number, title, state, branch, PRs and CI, as plain text — it
//   comes from each member's space; a tap opens it only for its own member,
//   in their own space; a member who left greyed, "no longer a member";
//   Hide for the owner), and, from your own space, Work on this: your
//   sandbox, then the definition's security part — its setup scripts and
//   the policy that runs code or pushes — to accept before your half is
//   made (or your half, once you have it)
//
//   your half of a team project (a membership): "Review the team project's
//   changes" — each key and setup script as you accepted it and as the team
//   has it now, the changed ones marked — then Accept; and the way to the
//   team's board
//
// The state is projectTeam(app) (model/project-team.js).
import { html, nothing, repeat } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, push } from './ui.js';
import { projectTeam, boardColumns, securityDiff } from '../model/project-team.js';
import { openUrl } from './project-task.js';
import { openProject, follow, TONE } from './projects.js';

ext.register({ screen: (s) => (s.kind === 'project-team' ? teamTpl(s) : null) });

function teamTpl(s) {
  const app = ctx.app;
  const p = app.projects;
  const pv = p.find(s.pid);
  follow(s); // the stack went back to this one: the model has it open again
  if (!pv) return html`<screen title="Team project" style="list"><section><progress label="loading…"/></section></screen>`;
  return pv.kind === 'membership' ? reviewTpl(pv) : boardTpl(pv);
}

function boardTpl(pv) {
  const app = ctx.app;
  const t = projectTeam(app);
  const b = t.ensureBoard(pv.id);
  const cols = boardColumns(t.rows(pv.id));
  const mine = t.membershipOf(pv.id);
  return html`<screen title=${pv.name} subtitle="the team board" style="list" refreshable @refresh=${() => t.load(pv.id)}>
    <toolbar><button icon="gear" @tap=${() => { app.projects.showTab('settings'); push({ kind: 'project-settings', pid: pv.id }); }}>Project settings</button></toolbar>
    ${b.err ? html`<section><notice tone="danger" text=${b.err}/></section>` : nothing}
    ${mine ? html`<section><row title="Your half of it" subtitle=${mine.name} icon="folder" nav @tap=${() => openProject(mine.id)}/></section>`
      : t.canWork(pv) ? workTpl(pv) : nothing}
    ${seedTpl(pv)}
    <section footer="Each member's tasks run in their own space; only a task's own member opens its conversation."/>
    ${repeat(cols, (c) => c.key, (c) => html`<section title=${`${c.title}${c.rows.length ? ` (${c.rows.length})` : ''}`}>
      ${c.rows.length ? repeat(c.rows, (w) => w.key, (w) => rowTpl(pv, w)) : html`<empty text="none"/>`}
    </section>`)}
  </screen>`;
}

// the seed sandbox: the definition's owner sets one of their sandboxes the team can see
function seedTpl(pv) {
  const t = projectTeam(ctx.app);
  const mine = t.seedChoices(pv);
  if (!mine) return nothing;
  const sd = t.seed(pv.id);
  return html`<section title="Seed sandbox" footer=${sd.err || sd.note || 'Members\' sandboxes fork from it; it holds no sign-in. Only one shared with the team can be forked.'}>
    <row title=${pv.sandboxRef || 'none'} subtitle="the seed now" icon="box" mono="title"/>
    <picker label="Seed" value=${sd.ref || ''} options=${[{ value: '', label: mine.length ? 'one of yours shared with the team…' : '(none shared with the team)' }, ...mine.map((x) => ({ value: x.ref, label: `${x.name} · ${x.state}` }))]}
      @change=${(e) => t.setSeedRef(pv.id, e.value)}/>
    <button icon="box" ?busy=${sd.busy} ?disabled=${!sd.ref} @tap=${() => t.setSeed(pv.id)}>${pv.sandboxRef ? 'Change the seed' : 'Set the seed'}</button>
  </section>`;
}

function rowTpl(pv, w) {
  const t = projectTeam(ctx.app);
  const sub = [w.member, w.stale ? 'no longer a member' : '', w.state.text, w.branch ? `⎇ ${w.branch}` : '', ...w.prs.map((x) => x.text), w.ci ? w.ci.text : ''].filter(Boolean).join(' · ');
  const links = [...w.prs.filter((x) => x.url).map((x) => ({ text: `${x.text} ↗`, url: x.url })), ...(w.ci && w.ci.url ? [{ text: 'CI ↗', url: w.ci.url }] : [])];
  return html`<row title=${`${w.n} ${w.title}`} subtitle=${sub} tone=${w.stale ? 'muted' : TONE[w.state.tone] || nothing} ?nav=${!!w.open} ?disabled=${w.stale}
      @tap=${() => { if (w.open) ctx.app.select(w.open); }}>
    ${links.length || w.hide ? html`<actions>
      ${links.map((l) => html`<button icon="external" @tap=${() => openUrl(l.url)}>${l.text}</button>`)}
      ${w.hide ? html`<button icon="eye-slash" confirm=${{ title: `Hide ${w.member}'s task ${w.n} from the board?`, label: 'Hide' }} @tap=${() => t.hide(pv.id, w.who, w.num)}>Hide</button>` : nothing}
    </actions>` : nothing}
  </row>`;
}

// Work on this: your sandbox, then the security part to accept
function workTpl(pv) {
  const app = ctx.app;
  const t = projectTeam(app);
  const w = t.work(pv.id);
  if (!w) {
    return html`<section footer="Your own half, in your own space: your sandbox, your tasks, your sign-in.">
      <button icon="play" role="primary" @tap=${() => t.startWork(pv.id)}>Work on this</button></section>`;
  }
  app.sbx.ensure('', '');
  const mine = (app.sbx.listAt('').sandboxes || []).filter((x) => x.mine && x.visibility !== 'team' && !['deleting', 'archived', 'error'].includes(x.state));
  const modes = [{ value: 'auto', label: 'a new one' }, ...(mine.length ? [{ value: 'pick', label: 'one of yours' }] : [])];
  return html`<section title=${`Work on ${pv.name}`} footer=${w.err || w.note || (w.defHash ? 'What you accept runs in your sandbox, with your sign-in.' : 'A new sandbox is forked from the team\'s seed where that works for you.')}>
      <picker label="Its sandbox" style="segmented" value=${w.sandbox.mode === 'pick' ? 'pick' : 'auto'} options=${modes} @change=${(e) => t.setWork(pv.id, 'mode', e.value)}/>
      ${w.sandbox.mode === 'pick' ? html`<picker label="Sandbox" value=${w.sandbox.ref} options=${[{ value: '', label: 'pick one…' }, ...mine.map((x) => ({ value: x.ref, label: `${x.name} · ${x.state}` }))]}
        @change=${(e) => t.setWork(pv.id, 'ref', e.value)}/>` : nothing}
      <button role="primary" ?busy=${w.busy} @tap=${async () => { const r = await t.submitWork(pv.id); if (r && r.project) openProject(r.project.id); }}>${w.defHash ? 'Accept and start' : 'Continue'}</button>
      <button role="plain" @tap=${() => t.closeWork(pv.id)}>Cancel</button>
    </section>
    ${w.defHash ? securityTpl(null, w.definition, 'What you accept') : nothing}`;
}

// a definition's security part, as sections: setup scripts, then the policy keys (side by side when both)
function securityTpl(accepted, pending, title) {
  const d = securityDiff(accepted, pending);
  const two = !!accepted && !!pending;
  const pair = (a, b) => (two ? `you accepted: ${a || '—'}\nthe team has now: ${b || '—'}` : (b || a) || '—');
  return html`<section title=${`${title} — setup scripts`}>
      ${repeat(d.repos, (r) => r.repo, (r) => html`<row title=${r.repo} subtitle=${r.kind !== 'same' ? r.kind : nothing} icon="terminal" tone=${r.kind !== 'same' ? 'warn' : nothing}/>
        <code text=${pair(r.accepted, r.pending) || '(no setup script)'} wrap/>`)}
    </section>
    <section title=${`${title} — policy`}>
      ${repeat(d.keys, (k) => k.key, (k) => html`<row title=${k.key} subtitle=${k.changed ? 'changed' : nothing} icon="gear" tone=${k.changed ? 'warn' : nothing}/>
        <code text=${pair(k.accepted, k.pending)} wrap/>`)}
    </section>`;
}

function reviewTpl(pv) {
  const app = ctx.app;
  const t = projectTeam(app);
  const r = t.ensureReview(pv);
  const def = t.definitionOf(pv);
  return html`<screen title="The team's changes" subtitle=${pv.name} style="list" refreshable @refresh=${() => t.loadReview(pv.id, pv.defPending)}>
    ${r && r.pending && pv.level === 'owner' ? html`<toolbar><button role="primary" ?busy=${r.busy} @tap=${() => t.acceptReview(pv.id)}>Accept</button></toolbar>` : nothing}
    ${app.projects.flash ? html`<section><notice tone="ok" text=${app.projects.flash}/></section>` : nothing}
    ${r && r.err ? html`<section><notice tone="danger" text=${r.err}/></section>` : nothing}
    ${r && r.note ? html`<section><notice tone="warn" text=${r.note}/></section>` : nothing}
    <section footer="The team changed what runs in your half — setup scripts, instructions, checks, who it works as. Until you accept, your tasks run what you accepted before.">
      ${def ? html`<row title="The team's board" subtitle=${def.name} icon="people" nav @tap=${() => openProject(def.id)}/>` : nothing}
      ${!r ? html`<empty title="Nothing waits now"/>` : r.loading && !r.hash ? html`<progress label="loading…"/>` : !r.pending ? html`<empty title="Nothing waits now"/>` : nothing}
    </section>
    ${r && r.pending ? securityTpl(r.accepted, r.pending, 'Changes') : nothing}
  </screen>`;
}
