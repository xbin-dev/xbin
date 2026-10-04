// native/harness-child.js — a coding agent the agent started, as its card in
// the parent's chat in the native view (D147 §4.3.7, §4.3.13, §8 U6),
// the web's harness-child.js with the chat family:
//
//   block  the spawn's `toolcard` (family agent): its monogram, #id and state
//          as chips, what it does now, where it works and its counters, and —
//          open — its task, plan, last 3 blocks (the child's newest page, read
//          once the card is drawn open; a read that failed says why and is
//          tried again when the card is opened again — not at every paint)
//          and its answer; its permission,
//          plan approval or question (native/harness-ask.js's cards) inside
//          it, answered on the CHILD's run — the card opens by itself while
//          it waits — and its sign-in as a notice (Sign in is in its own chat)
//   menu   in a harness child's own chat: Cancel task (confirmed; for good)
//
// ↗ opens the child's chat: its composer messages it directly (the backend
// tells the parent, whose chat shows the notice), and Stop interrupts its
// turn — a toolcard holds no field or button, so steering lives there.
// The words are model/harness-child.js's.
import { html, repeat, nothing } from '/vendor/xb-native.js';
import { ext } from './ext.js';
import { ctx, guard } from './ui.js';
import { blockTpl, openChild } from './chat.js';
import { permissionTpl, questionTpl } from './harness-ask.js';
import { isHarnessChild, childRun, childCard, tailOf, loadTail, tailError, cancelWords, isChildRun } from '../model/harness-child.js';
import { permission, question, ownerOf } from '../model/harness-ask.js';
import { signIn } from '../model/terminals.js';
import { HARNESSES, findHarness, planEntries } from '../model/harness.js';

const TONE = { ok: 'ok', bad: 'danger', warn: 'warn', run: 'accent' };
const STATE = { starting: 'running', working: 'running', approval: 'running', question: 'running', login: 'running',
  lost: 'error', failed: 'error', canceled: 'canceled', done: 'ok', idle: 'ok' };

const isOpen = (id, dflt) => ctx.app.session.ui.isOpen(id, dflt);
const setOpen = (id) => (e) => { ctx.app.session.open.set(id, !!e.open); ctx.paint(); };
// reopen: opening a card again is its Retry when its child could not be read
// (a toolcard holds no button)
const reopen = (id, child) => (e) => { if (e.open && child) ctx.app.session.failed.clear(child); setOpen(id)(e); };

ext.register({
  block: (b, depth) => (b.k === 'agent' && isHarnessChild(b) ? cardTpl(b, depth) : null),
  menu(v) {
    const r = v && v.run;
    if (!isChildRun(r) || !ctx.app.rules.access(v).talk) return null;
    if (!['running', 'awaiting', 'sleeping', 'queued', 'blocked', 'waiting_input'].includes(r.status)) return null;
    const c = { name: (r.harness && r.harness.name) || 'the coding agent', id: r.id, parent: r.parentId };
    return html`<button icon="xmark" role="destructive" confirm=${{ title: cancelWords(c), label: 'Cancel task', destructive: true }}
      @tap=${guard(() => ctx.app.harness.cancel(r.id))}>Cancel task</button>`;
  },
});

// the call's tool row (its acp carries the backend's patches), in the child's held blocks
function acpOf(blocks, callId) {
  for (const b of blocks || []) {
    if (b.k === 'tool' && b.id === 'c' + callId) return b.acp || null;
    if (b.kids) { const a = acpOf(b.kids, callId); if (a) return a; }
  }
  return null;
}

// parkTpl: the child's park, drawn with its own module's card and answered on the child
function parkTpl(b, run, c, w) {
  const ps = run.status === 'waiting_input' ? run.pendingState || {} : {};
  if (ps.harness && ps.kind === 'approval') return permissionTpl(run, permission(ps, { ...w, acp: acpOf(b.blocks, ps.harness.callId) }), w) || nothing;
  if (ps.harness && ps.kind === 'question') return questionTpl(run, question(ps), w) || nothing;
  if (ps.kind === 'login') {
    const app = ctx.app;
    const held = app.session.merged(run.id);
    const si = signIn(held ? { ...held, run: { ...held.run, ...run } } : { run, access: w.access, config: {} },
      { list: app.sbx.list, entry: findHarness(app.harness.catalog, c.provider), me: app.me });
    if (si) app.sbx.ensure(si.sandbox.ref); // one the list read before lacks: read again, not "gone"
    if (si) return html`<notice tone="warn" title=${`Sign in to ${c.name}`} text=${`${si.title} ${si.view || si.goneText || si.ask || 'Open it (↗) and tap Sign in.'}`}/>`;
  }
  return c.park ? html`<notice tone="warn" title=${c.name} text=${`${c.status} — open it (↗) to answer.`}/>` : nothing;
}

// Built again every render (rowTpl asks the seams without its memo): a card
// that waits for no one is kept while its block, its summary and open state are.
const memo = new WeakMap(); // block → {held, open, tpl}
function cardTpl(b, depth) {
  const app = ctx.app;
  const held = app.session.runs.get(b.childId) || null;
  const run = childRun(b, held);
  const c = childCard(b, run);
  const open = isOpen(b.id, !!c.park); // opens by itself while it waits for you
  if (open) loadTail(app.session, c.id); // once: a read that failed waits (model/session.js Failures)
  const err = open && !b.blocks ? tailError(app.session, c.id) : '';
  const m = memo.get(b);
  const ci = ext.childStatus(run); // CI's words for what it pushed (native/ci.js keeps one template per words): part of the memo's key
  if (!c.park && m && m.held === held && m.open === open && m.err === err && (m.ci || [])[0] === (ci || [])[0]) return m.tpl;
  const v = app.session.current();
  const w = { owner: ownerOf(v, app.me), talk: app.rules.access(v).talk, name: c.name, access: v && v.access };
  const tail = open ? tailOf(b) : null;
  const chips = [{ text: c.mono }, ...(c.id ? [{ text: '#' + c.id }] : []), { text: c.state.word, ...(TONE[c.state.tone] ? { tone: TONE[c.state.tone] } : {}) }];
  const tpl = html`<toolcard title=${c.title} icon=${(HARNESSES[c.provider] || {}).icon || 'agent'} family="agent" state=${STATE[c.state.key] || 'running'}
      chips=${chips} open=${open} @toggle=${reopen(b.id, c.id)} @open=${c.id ? () => openChild(c.id) : nothing}>
    <text tone=${c.state.tone === 'warn' ? 'warn' : c.state.tone === 'bad' ? 'danger' : nothing}>${c.status}</text>
    ${ci || nothing}
    <text style="footnote" tone="muted">${[c.name, c.where, c.meta].filter(Boolean).join(' · ')}</text>
    ${c.task ? html`<text style="footnote" tone="muted" lines=${3}>${'task: ' + c.task}</text>` : nothing}
    <transcript>
      ${c.plan ? html`<plan entries=${planEntries(run.harness.plan)}/>` : nothing}
      ${open ? (tail ? repeat(tail, (x) => x.id, (x) => blockTpl(x, depth + 1))
        : err ? html`<notice tone="danger" text=${`${err} — fold the card and open it again to retry.`}/>` : html`<progress label="loading…"/>`) : nothing}
      ${parkTpl(b, run, c, w)}
    </transcript>
    ${c.answer ? html`<text style="caption" tone="muted">answer</text><markdown source=${c.answer}/>` : nothing}
  </toolcard>`;
  memo.set(b, { held, open, err, ci, tpl });
  return tpl;
}

// the Coding agents board draws a parked child's park with this (native/harness-board.js)
export { parkTpl };
