// project-team.js — team projects on the web (API.md §Projects in the UI),
// drawn in a project's page (projects.js):
//
//   teamBoardTpl(p, pv)  a team project's definition: its members' tasks by
//                        column — each row's member, number, title, state,
//                        branch, pull requests and CI, as plain text (it
//                        comes from each member's space), its conversation
//                        openable by its own member only ("open (yours)"),
//                        a member who left greyed ("no longer a member"),
//                        Hide for the owner — and, from your own space,
//                        "Work on this": your own half in your own space,
//                        made only after you have read the definition's
//                        security part (its setup scripts and the policy
//                        that runs code and pushes) and accepted it
//   reviewCardTpl(p, pv) your half of a team project, when the team changed
//                        its setup or policy: "Review the team project's
//                        changes" — what you accepted and what it has now,
//                        side by side, each changed key and setup script
//                        marked — then Accept (until then your half runs
//                        what you accepted)
//   teamLinkTpl(p, pv)   between a definition and your half of it
//   the seed sandbox     the definition's owner sets one of their sandboxes
//                        the team can see (POST /projects/{pid}/seed)
//
// The state is model/project-team.js (projectTeam(app)).
import { html, nothing, repeat } from '/vendor/lit-all.min.js';
import { ctx } from './web-ext.js';
import { projectTeam, boardColumns, securityDiff } from './model/project-team.js';

const team = () => projectTeam(ctx.app);

/** teamLinkTpl(p, pv): a definition's "your half" / Work on this; a membership's way to the team board. */
export function teamLinkTpl(p, pv) {
  const t = team();
  if (pv.kind === 'membership') {
    const def = t.definitionOf(pv);
    return html`<div class="pteamlink muted small" id="pteam-def">Your half of a team project${def ? html` — <a class="lnk" @click=${() => p.open(def.id)}>the team's board ›</a>` : nothing}</div>`;
  }
  if (pv.kind !== 'team') return nothing;
  const mine = t.membershipOf(pv.id);
  if (mine) return html`<div class="pteamlink small" id="pteam-mine">You work on this in your own space: <a class="lnk" @click=${() => p.open(mine.id)}>${mine.name} ›</a></div>`;
  if (!t.canWork(pv)) return nothing;
  const former = t.formerOf(pv.id);
  return t.work(pv.id) ? workTpl(p, pv) : html`<div class="pteamlink"><button class="btn btnsm" id="pteam-work" @click=${() => t.startWork(pv.id)}>${former ? 'Work on this again' : 'Work on this'}</button>
    <span class="muted small">${former ? `your half (${former.name}) is archived: taken up again, with the definition as it is now` : 'your own half, in your own space: your sandbox, your tasks, your sign-in'}</span></div>`;
}

// "Work on this": your sandbox, then the definition's security part to accept
function workTpl(p, pv) {
  const t = team();
  const w = t.work(pv.id);
  const app = ctx.app;
  app.sbx.ensure('', '');
  const mine = (app.sbx.listAt('').sandboxes || []).filter((s) => s.mine && s.visibility !== 'team' && !['deleting', 'archived', 'error'].includes(s.state));
  const former = !!t.formerOf(pv.id); // taken up again: it keeps its own sandbox, so there is nothing to choose
  const loading = !former && t.workLoading();
  const seeded = !!t.seedProvider(pv);
  const managers = seeded || former ? [] : t.workManagers();
  return html`<div class="pform" id="pteam-form">
    <b>Work on ${pv.name} in your own space</b>
    ${former ? html`<div class="muted small" id="pteam-sbx-keep">Your half keeps its own sandbox.</div>` : html`<div class="field"><label>Its sandbox</label>
      <label class="chk"><input type="radio" name="pteam-sbx" .checked=${w.sandbox.mode !== 'pick'} @change=${() => t.setWork(pv.id, 'mode', 'auto')}> a new one${seeded ? ' (forked from the team\'s seed where that works for you)' : ''}</label>
      ${w.sandbox.mode !== 'pick' && !seeded ? (managers.length ? html`<select id="pteam-sbx-mgr" @change=${(e) => t.setWork(pv.id, 'provider', e.target.value)}>
        ${managers.map((m, i) => html`<option value=${m.provider} ?selected=${w.sandbox.provider ? m.provider === w.sandbox.provider : i === 0}>${m.title || m.provider}</option>`)}</select>`
        : loading ? html`<span class="muted small" id="pteam-sbx-loading">loading…</span>`
        : html`<span class="muted small" id="pteam-sbx-none">no sandbox manager is bound in your space</span>`) : nothing}
      <label class="chk"><input type="radio" name="pteam-sbx" .checked=${w.sandbox.mode === 'pick'} ?disabled=${!mine.length} @change=${() => t.setWork(pv.id, 'mode', 'pick')}>
        one of your own private sandboxes${mine.length ? '' : ' (you have none)'}</label>
      ${w.sandbox.mode === 'pick' ? html`<select id="pteam-sbx-ref" @change=${(e) => t.setWork(pv.id, 'ref', e.target.value)}>
        <option value="" ?selected=${!w.sandbox.ref}>pick one…</option>
        ${mine.map((s) => html`<option value=${s.ref} ?selected=${s.ref === w.sandbox.ref}>${s.name} · ${s.state}</option>`)}</select>` : nothing}</div>`}
    ${w.defHash ? securityTpl(null, w.definition, 'What you accept — it runs in your sandbox, with your sign-in') : nothing}
    ${w.note ? html`<div class="note">${w.note}</div>` : nothing}
    ${w.err ? html`<div class="err" id="pteam-err">${w.err}</div>` : nothing}
    <div><button class="btn btnsm" id="pteam-go" ?disabled=${w.busy || loading} @click=${() => t.submitWork(pv.id)}>${w.busy ? 'Working…' : w.defHash ? 'Accept and start' : 'Continue'}</button>
      <button class="btn ghost btnsm" @click=${() => t.closeWork(pv.id)}>Cancel</button></div>
  </div>`;
}

// securityTpl(accepted, pending, title): a definition's security part —
// setup scripts and the policy keys that follow only on acceptance — as
// plain text; with both, side by side and each change marked
function securityTpl(accepted, pending, title) {
  const d = securityDiff(accepted, pending);
  const two = !!accepted && !!pending;
  return html`<div class="psec" id="psec">
    <div class="small"><b>${title}</b></div>
    <table class="psect"><thead><tr><th></th>${two ? html`<th>you accepted</th><th>the team has now</th>` : html`<th></th>`}</tr></thead><tbody>
      ${d.repos.map((r) => html`<tr data-repo=${r.repo} data-kind=${r.kind} class=${r.kind !== 'same' ? 'chg' : ''}><th>setup · ${r.repo}${r.kind !== 'same' ? html` <span class="badge">${r.kind}</span>` : nothing}</th>
        ${two ? html`<td><pre>${r.accepted ?? '—'}</pre></td><td><pre>${r.pending ?? '—'}</pre></td>` : html`<td><pre>${(r.pending ?? r.accepted) || '(no setup script)'}</pre></td>`}</tr>`)}
      ${d.keys.map((k) => html`<tr data-key=${k.key} class=${k.changed ? 'chg' : ''}><th>${k.key}${k.changed ? html` <span class="badge">changed</span>` : nothing}</th>
        ${two ? html`<td><pre>${k.accepted || '—'}</pre></td><td><pre>${k.pending || '—'}</pre></td>` : html`<td><pre>${(k.pending || k.accepted) || '—'}</pre></td>`}</tr>`)}
    </tbody></table></div>`;
}

/** reviewCardTpl(p, pv): "Review the team project's changes" on your half of it. */
export function reviewCardTpl(p, pv) {
  const t = team();
  const r = t.ensureReview(pv);
  if (!r) return nothing;
  const owner = pv.level === 'owner';
  return html`<div class="pform pteamreview" id="pteam-review">
    <b>Review the team project's changes</b>
    <div class="muted small">The team changed what runs in your half — setup scripts, instructions, checks, who it works as. Until you accept, your tasks run what you accepted before.</div>
    ${r.loading && !r.hash ? html`<div class="muted small">loading…</div>` : nothing}
    ${r.pending ? securityTpl(r.accepted, r.pending, 'Changes') : !r.loading && !r.err ? html`<div class="muted small">Nothing waits now.</div>` : nothing}
    ${r.note ? html`<div class="note">${r.note}</div>` : nothing}
    ${r.err ? html`<div class="err" id="pteam-review-err">${r.err}</div>` : nothing}
    ${r.pending && owner ? html`<div><button class="btn btnsm" id="pteam-review-accept" ?disabled=${r.busy} @click=${() => t.acceptReview(pv.id)}>${r.busy ? 'Accepting…' : 'Accept these changes'}</button></div>` : nothing}
  </div>`;
}

// the seed sandbox: the definition's owner sets one the team can see (once:
// the backend keeps the first, so a set seed is shown read-only)
function seedTpl(pv) {
  const t = team();
  if (pv.kind === 'team' && pv.level === 'owner' && pv.sandboxRef) {
    return html`<div class="pteamlink small" id="pteam-seed"><span>Seed sandbox: <b class="mono">${pv.sandboxRef}</b></span>
      <span class="muted">${t.seed(pv.id).note || 'members\' sandboxes fork from it; it holds no sign-in'}</span></div>`;
  }
  const mine = t.seedChoices(pv);
  if (!mine) return nothing;
  const sd = t.seed(pv.id);
  const ref = sd.ref || '';
  return html`<div class="pteamlink small" id="pteam-seed"><span>Seed sandbox: <b class="mono">none</b></span>
    <select id="pteam-seed-ref" @change=${(e) => t.setSeedRef(pv.id, e.target.value)}><option value="" ?selected=${!ref}>${mine.length ? 'one of yours shared with the team…' : '(you have no sandbox shared with the team)'}</option>
      ${mine.map((x) => html`<option value=${x.ref} ?selected=${x.ref === ref}>${x.name} · ${x.state}</option>`)}</select>
    <button class="btn ghost btnsm" id="pteam-seed-set" ?disabled=${sd.busy || !ref} @click=${() => t.setSeed(pv.id)}>Set the seed</button>
    <span class="muted">members' sandboxes fork from it; it holds no sign-in</span>
    ${sd.err ? html`<span class="err">${sd.err}</span>` : sd.note ? html`<span class="muted">${sd.note}</span>` : nothing}</div>`;
}

/** teamBoardTpl(p, pv): a team project's definition — Work on this, its seed, and the team board. */
export function teamBoardTpl(p, pv) {
  const t = team();
  const b = t.ensureBoard(pv.id);
  const cols = boardColumns(t.rows(pv.id));
  return html`
    ${teamLinkTpl(p, pv)}
    ${seedTpl(pv)}
    <div class="pbar"><span class="muted small">The team board: each member's tasks, run in their own space. Only a task's own member opens its conversation.</span>
      <span style="flex:1"></span><button class="btn ghost btnsm" id="pteam-refresh" @click=${() => t.load(pv.id)}>Refresh</button></div>
    ${b.err ? html`<div class="err" id="pteam-board-err">${b.err}</div>` : nothing}
    <div class="pteamboard">${cols.map((col) => html`<div class="pcol" data-col=${col.key}>
        <div class="pcolh">${col.title} <span class="muted">${col.rows.length || ''}</span></div>
        ${repeat(col.rows, (w) => w.key, (w) => rowTpl(pv, w))}
      </div>`)}</div>
    ${!b.items.length && !b.loading && !b.err ? html`<div class="muted small empty-line">No member has a task here yet.</div>` : nothing}`;
}

function rowTpl(pv, w) {
  const t = team();
  return html`<div class="ptask tbrow ${w.stale ? 'stale' : ''}" data-key=${w.key} title=${w.stale ? 'no longer a member' : ''}>
    <div class="pth"><span class="ptn">${w.n}</span><span class="ptt">${w.title}</span></div>
    <div class="ptm"><span class="muted">${w.member}</span>${w.stale ? html`<span class="badge">no longer a member</span>` : nothing}
      <span class="ptst" data-tone=${w.state.tone}>${w.state.text}</span></div>
    ${w.branch ? html`<div class="ptb mono">⎇ ${w.branch}</div>` : nothing}
    ${w.prs.length || w.ci ? html`<div class="ptc">
      ${w.prs.map((c) => (c.url ? html`<a class="badge pchip" data-tone=${c.tone} href=${c.url} target="_blank" rel="noopener noreferrer" title=${c.title}>${c.text} ↗</a>`
        : html`<span class="badge pchip" data-tone=${c.tone} title=${c.title}>${c.text}</span>`))}
      ${w.ci ? html`<span class="badge pchip" data-tone=${w.ci.tone}>${w.ci.text}</span>` : nothing}</div>` : nothing}
    ${w.open || w.hide ? html`<div class="ptc">
      ${w.open ? html`<button class="btn ghost btnsm tbopen" @click=${() => ctx.app.select(w.open)}>open (yours)</button>` : nothing}
      ${w.hide ? html`<button class="btn ghost btnsm tbhide" title="hide it from the board" @click=${() => { if (confirm(`Hide ${w.member}'s task ${w.n} from the board?`)) t.hide(pv.id, w.who, w.num); }}>Hide</button>` : nothing}</div>` : nothing}
  </div>`;
}

const style = document.createElement('style');
style.textContent = `
  .projs-page .pteamlink { margin: 0 0 10px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .projs-page .pteamboard { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; align-items: start; }
  @media (max-width: 900px) { .projs-page .pteamboard { grid-template-columns: minmax(0, 1fr); } }
  .projs-page .tbrow { cursor: default; }
  .projs-page .tbrow.stale { opacity: .55; }
  .projs-page .psec { margin: 8px 0; overflow-x: auto; }
  .projs-page .psect { border-collapse: collapse; width: 100%; font-size: 12px; }
  .projs-page .psect th, .projs-page .psect td { text-align: left; vertical-align: top; border-bottom: 1px solid var(--bx-border); padding: 4px 6px; }
  .projs-page .psect th { font-weight: 600; white-space: nowrap; }
  .projs-page .psect pre { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 11.5px; max-height: 12em; overflow: auto; }
  .projs-page .psect tr.chg th, .projs-page .psect tr.chg td { background: color-mix(in srgb, var(--bx-yellow, #d9a441) 12%, transparent); }
  .projs-page .pteamreview { border-left: 3px solid var(--bx-yellow, #d9a441); }
`;
document.head.append(style);
