// harness-cards.js — a coding harness's conversation on the web, beside its
// transcript (D147 §3.4, §8 U3), hooked on the web's seams (web-ext.js):
//
//   top    the usage badge (context in use, cost), what the conversation
//          changed, and the 📋 plan pin — the harness's live plan, folded to
//          a line under the task pin (D133's pattern), unfolding to its entries
//
// Its calls' cards are xbind's chat kit's (D224: /vendor/chat/cards.js,
// through chat-kit.js) — one card per ACP tool kind (execute: the command,
// its output's tail, the exit code; edit: its patches, +a −d; read, search,
// fetch, delete / move, think, switch_mode, other), a harness-internal
// subagent (a Claude Task) with the blocks that ran under it nested inside —
// the same card the template's own tools get. The words are the kit's, read
// from the call's record (model/harness-heads.js acpRecord).
import { html, nothing } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { harnessOf, planOf, usageBadge, countsWords } from './model/harness.js';

const icon = (name) => (name ? html`<bx-icon name=${name}></bx-icon>` : nothing);
// a plan entry's state, said by its square's title (the square's fill is the colour)
const PLAN_WORD = { pending: 'to do', in_progress: 'in progress', completed: 'done' };

ext.register({
  top: (v) => (v ? topTpl(v) : null),
});

// --- the top bar: usage, what changed, the plan pin ---------------------------------------------

let planOpen = 0; // the run whose plan is unfolded

function topTpl(v) {
  const h = harnessOf(v.run);
  if (!h) return null;
  const u = usageBadge(h.usage);
  const c = countsWords(h.counts);
  const p = planOf(h);
  if (!u && !c && !p) return null;
  return html`${u ? html`<span class="badge husage ${u.tone}" title=${u.title}>${u.text}</span>` : nothing}
    ${c ? html`<span class="badge hcounts" title="what this conversation's coding agent did (across its restarts); each edit's patch is on its card">${c}</span>` : nothing}
    ${p ? planTpl(v.run.id, p) : nothing}`;
}

function planTpl(id, p) {
  const open = planOpen === id;
  const toggle = () => { planOpen = open ? 0 : id; ctx.paint(); };
  const next = p.entries.find((e) => e.status !== 'completed');
  const line = p.now ? `now: ${p.now}` : next ? `next: ${next.content}` : 'all done';
  return html`<div class="taskpin planpin ${open ? 'open' : ''}">
    <button class="tasktoggle" @click=${toggle} title=${open ? 'fold the plan' : 'the coding agent\'s plan, as it keeps it'}>${icon('clipboard')}Plan · ${p.done}/${p.total}${icon(open ? 'caret-down' : 'caret-right')}</button>
    ${open ? html`<ol class="planlist">${p.entries.map((e) => html`<li class="pe ${e.status || 'pending'}">
        <span class="pg" title=${PLAN_WORD[e.status] || PLAN_WORD.pending}></span><span class="pt">${e.content}</span>${e.priority === 'high' ? html`<span class="pp">high</span>` : nothing}</li>`)}</ol>`
      : html`<span class="taskline" title=${line}>${line}</span>`}
  </div>`;
}

// --- styles -----------------------------------------------------------------------------------

const CSS = `
.top .planpin { order: 1; }
.top .planpin .planlist { margin: 2px 0 0; padding: 0; list-style: none; display: grid; gap: 2px; max-height: 40vh; overflow: auto; }
.top .planpin .pe { display: flex; gap: 8px; align-items: baseline; }
/* a plan entry's square: hollow to do, the accent in progress, filled done */
.top .planpin .pg { flex: none; align-self: center; box-sizing: border-box; width: 8px; height: 8px; border: 1px solid var(--bx-muted); }
.top .planpin .pe.in_progress .pg { border-color: var(--bx-accent); background: var(--bx-accent); }
.top .planpin .pe.completed .pg { border-color: var(--bx-ok); background: var(--bx-ok); }
.top .planpin .pe.completed .pt { color: var(--bx-muted); }
.top .planpin .pp { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); text-transform: uppercase; color: var(--bx-muted);
  border: 1px solid var(--bx-border-strong); border-radius: var(--bx-radius); padding: 0 4px; }
.badge.husage, .badge.hcounts { text-transform: none; letter-spacing: 0; font: var(--bx-font-code); }
.badge.husage.warn { color: var(--bx-warn); border-color: var(--bx-warn); }
.badge.husage.bad { color: var(--bx-danger); border-color: var(--bx-danger); }
`;
const style = document.createElement('style');
style.dataset.of = 'harness-cards';
style.textContent = CSS;
document.head.append(style);
