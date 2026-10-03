// harness-child.js — a coding agent the agent started, as its card in the
// parent's chat on the web (D147 §4.3.6–§4.3.8, §4.3.13, §8 U6): where
// the subagent_spawn call is, instead of the built-in subagent card, when the
// child is a coding harness (the fold's agent block carries its `harness`).
//
//   header   its monogram and name, the link's label (else the task), #id,
//            its state; under it what it does now (the status line) and
//            where it works (sandbox:cwd, after the box glyph) with its counters — tool calls,
//            files +a −d, cost, time
//   park     its permission request, plan approval or question
//            (harness-ask.js's cards) and its sign-in (signin.js's) — drawn
//            on the card and answered on the CHILD's run, open or not
//   body     (the card open) the task, its plan, its last 3 blocks — the
//            child's newest page, read once the card is open AND on screen
//            (an IntersectionObserver), kept current by the stream; a read
//            that failed says why, with Retry, and is not read again at every
//            paint — and its answer once done
//   actions  Open ↗ (its own chat), Stop (interrupts its turn), Cancel
//            (confirmed; for good) and Message: a person's message straight
//            to it (POST /runs/{child}/message; Enter queues or steers,
//            ⌘/Ctrl+Enter interrupts first) — the backend tells the parent,
//            whose chat shows that notice when it is delivered (fold.js NOTICE)
//
// The words are model/harness-child.js's; the summary is what the tile
// already holds (the link's child, run and harness events) — no request per
// card but the one tail read. The list row's ⧉ N (coding agents at work below
// a conversation, §4.3.8) is sidebar.js's; its look is here.
import { html, nothing, unsafeHTML, classMap, directive, Directive, noChange } from '/vendor/lit-all.min.js';
import { ext, ctx } from './web-ext.js';
import { md } from './chat-md.js';
import { blocksTpl } from './chat-cards.js';
import { permissionTpl, questionTpl } from './harness-ask.js';
import { signInTpl } from './signin.js';
import { isHarnessChild, childRun, childCard, tailOf, loadTail, tailError, stopWords, cancelWords, messageWords } from './model/harness-child.js';
import { permission, question, ownerOf } from './model/harness-ask.js';
import { signIn } from './model/terminals.js';
import { findHarness } from './model/harness.js';
import { access } from './model/rules.js';

ext.register({
  block: (b, ui, depth) => (b.k === 'agent' && isHarnessChild(b) ? cardTpl(b, ui, depth) : null),
});

// per child: its Message box and what an action said — {msg, text, busy, note, err}
const boxes = new Map();
const box = (id) => {
  let x = boxes.get(id);
  if (!x) boxes.set(id, (x = { msg: false, text: '', busy: '', note: '', err: '' }));
  return x;
};
const repaint = () => ctx.paint();

// --- reading the tail: once a card is open and on screen -------------------------------

const wanted = new WeakMap(); // an open card's body → its child's id
let io = null;
const observer = () => io || (io = new IntersectionObserver((es) => {
  for (const e of es) {
    if (!e.isIntersecting) continue;
    const id = wanted.get(e.target);
    io.unobserve(e.target);
    wanted.delete(e.target);
    if (id && ctx.app) loadTail(ctx.app.session, id);
  }
}));
// (not while a read that failed waits to be tried again: the card says why, with Retry)
class OnScreen extends Directive {
  render() { return noChange; }
  update(part, [id]) {
    const el = part.element;
    const s = ctx.app.session;
    if (id && wanted.get(el) !== id && !s.views.has(id) && s.failed.due(id)) { wanted.set(el, id); observer().observe(el); }
    return noChange;
  }
}
const onScreen = directive(OnScreen);

// --- the card ------------------------------------------------------------------------------

// the call's tool row (its acp carries the backend's patches), in the child's held blocks
function acpOf(blocks, callId) {
  for (const b of blocks || []) {
    if (b.k === 'tool' && b.id === 'c' + callId) return b.acp || null;
    if (b.kids) { const a = acpOf(b.kids, callId); if (a) return a; }
  }
  return null;
}

// parkTpl: the child's park, drawn with its own module's card and answered on the child
function parkTpl(app, b, run, c, who) {
  const ps = run.status === 'waiting_input' ? run.pendingState || {} : {};
  if (ps.harness && ps.kind === 'approval') return permissionTpl(run, permission(ps, { ...who, acp: acpOf(b.blocks, ps.harness.callId) }), who) || nothing;
  if (ps.harness && ps.kind === 'question') return questionTpl(run, question(ps), who) || nothing;
  if (ps.kind === 'login') {
    const held = app.session.merged(run.id);
    const v = held ? { ...held, run: { ...held.run, ...run } } : { run, access: who.access, config: {} };
    const si = signIn(v, { list: app.sbx.list, entry: findHarness(app.harness.catalog, c.provider), me: app.me });
    if (si) { app.sbx.ensure(si.sandbox.ref); return signInTpl(app, si); }
  }
  // a park the summary has only in brief: its own chat answers it
  return c.park ? html`<div class="hint hkopen">${c.status} — <button class="lnk" @click=${() => app.select(c.id)}>open it<bx-icon name="popout"></bx-icon></button> to answer</div>` : nothing;
}

async function act(c, what, fn) {
  const x = box(c.id);
  x.busy = what; x.err = ''; x.note = '';
  repaint();
  try { await fn(x); } catch (e) { x.err = (e && e.message) || String(e); }
  x.busy = '';
  repaint();
}

const stop = (c) => act(c, 'stop', async (x) => {
  const r = await ctx.app.harness.stop(c.id);
  // what was still queued for it comes back to the Message box
  const back = ((r && r.returned) || []).map((q) => q.text).filter(Boolean).join('\n');
  if (back) { x.msg = true; x.text = back; }
  x.note = `Stopped ${c.name}'s turn.`;
});

const cancel = (c) => (confirm(cancelWords(c)) ? act(c, 'cancel', async (x) => {
  await ctx.app.harness.cancel(c.id);
  x.msg = false;
  x.note = `Canceled${c.parent ? ' — the agent is told' : ''}.`;
}) : null);

const send = (c, interrupt) => act(c, 'send', async (x) => {
  const text = x.text.trim();
  if (!text) { x.err = 'Write the message first.'; return; }
  await ctx.app.harness.steer(c.id, text, { interrupt });
  x.text = '';
  x.note = messageWords(c).sent(interrupt);
});

function msgTpl(c, x) {
  const w = messageWords(c);
  return html`<div class="hkmsg">
    <input class="hkin" placeholder=${w.placeholder} title=${w.hint} .value=${x.text} ?disabled=${x.busy === 'send'}
      @input=${(e) => { x.text = e.target.value; }}
      @keydown=${(e) => { if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); send(c, e.metaKey || e.ctrlKey); } else if (e.key === 'Escape') { x.msg = false; repaint(); } }}>
    <button class="btn btnsm" data-act="send" ?disabled=${!!x.busy} @click=${() => send(c, false)}>Send</button>
    <button class="btn ghost btnsm" data-act="send-now" title="interrupt its turn, then send" ?disabled=${!!x.busy} @click=${() => send(c, true)}>Send now</button>
  </div>`;
}

function bodyTpl(b, ui, depth, c) {
  const tail = tailOf(b);
  const held = ctx.app.session.views.get(c.id);
  const more = !!tail && ((held && held.hasOlder) || b.blocks.length > tail.length); // older pages, or blocks past the last 3
  const failed = tail ? '' : tailError(ctx.app.session, c.id); // its read failed: why, and Retry (not "loading…" for ever)
  return html`<div class="acb hkbody" ${onScreen(tail ? 0 : c.id)}>
    ${c.task ? html`<div class="task ${ui.isOpen(b.id + ':task', false) ? 'on' : ''}" @click=${() => ui.toggle(b.id + ':task', false)}>
      <span class="k">task</span> ${c.task}</div>` : nothing}
    ${c.plan ? html`<div class="hkplan">${c.plan.entries.map((e) => html`<div class="pe ${e.status || 'pending'}"><span class="pm"></span>${e.content}</div>`)}</div>` : nothing}
    ${tail ? html`<div class="hktail">${more ? html`<div class="muted small hkmore">… <button class="lnk" @click=${() => ui.act.select(c.id)}>all of it in its chat<bx-icon name="popout"></bx-icon></button></div>` : nothing}
        ${blocksTpl(tail, ui, depth + 1)}</div>`
      : failed ? html`<div class="small readfail" role="status"><span class="err">${failed}</span>
          <button class="lnk" data-act="retry" title="read it again" @click=${() => ui.act.retryRead(c.id)}>Retry</button></div>`
      : html`<div class="muted small">loading…</div>`}
    ${c.answer ? html`<div class="answer"><span class="k">answer</span><div class="md">${unsafeHTML(md(c.answer))}</div></div>` : nothing}
  </div>`;
}

// cardTpl: a child's card — in the parent's chat, and on the Coding agents
// board (harness-board.js: opts.run, the row's summary; opts.view, whose
// access answers — at home its conversation's row)
function cardTpl(b, ui, depth, opts = {}) {
  const app = ctx.app;
  if (!app) return null;
  const run = opts.run || childRun(b, app.session.runs.get(b.childId));
  const c = childCard(b, run);
  const open = ui.isOpen(b.id, false); // collapsed: the parent's chat stays light
  const pv = opts.view || app.session.current();
  const talk = access(pv).talk;
  const who = { owner: ownerOf(pv, app.me), talk, name: c.name, access: pv && pv.access };
  const x = box(c.id);
  const note = c.id && ui.approveNote ? ui.approveNote(c.id) : '';
  const k = c.state.key;
  return html`<div class=${classMap({ acard: true, hkid: true, on: open, ['hk-' + k]: true })} data-k=${b.id} data-child=${c.id} data-state=${k}>
    <div class="ach" @click=${() => ui.toggle(b.id, false)}>
      <span class="ic"><span class="kind" data-kind=${c.provider} title=${c.name}>${c.mono}</span></span>
      <span class="hl"><b class="hkn">${c.name}</b> ${c.title}</span>
      ${c.id ? html`<span class="rid">#${c.id}</span>` : nothing}
      ${c.state.tone === 'run' ? html`<span class="spin"></span>` : nothing}
      <span class="st hkst" data-tone=${c.state.tone}>${c.state.word}</span>
      <span class="tw"><bx-icon name=${open ? 'caret-down' : 'caret-right'}></bx-icon></span>
    </div>
    <div class="hkline"><span class="hks" data-tone=${c.state.tone}>${c.status}</span></div>
    ${c.where || c.meta ? html`<div class="hkmeta">${c.where ? html`<span class="mono">${c.whereIcon ? html`<bx-icon name=${c.whereIcon}></bx-icon>` : nothing}${c.where}</span>` : nothing}${c.where && c.meta ? ' · ' : ''}${c.meta}</div>` : nothing}
    ${c.park ? html`<div class="hkpark">${parkTpl(app, b, run, c, who)}</div>` : nothing}
    ${note ? html`<div class="anote muted small" role="status"><bx-icon name="warning"></bx-icon><span>${note}</span></div>` : nothing}
    ${open ? bodyTpl(b, ui, depth, c) : nothing}
    ${c.id ? html`<div class="hkact">
      <button class="lnk" data-act="open" title="its own chat" @click=${() => ui.act.select(c.id)}>Open<bx-icon name="popout"></bx-icon></button>
      ${talk && c.can.stop ? html`<button class="lnk" data-act="stop" title=${stopWords(c)} ?disabled=${!!x.busy} @click=${() => stop(c)}>Stop</button>` : nothing}
      ${talk && c.can.cancel ? html`<button class="lnk" data-act="cancel" ?disabled=${!!x.busy} @click=${() => cancel(c)}>Cancel</button>` : nothing}
      ${talk && c.can.message ? html`<button class="lnk" data-act="message" @click=${() => { x.msg = !x.msg; x.note = ''; x.err = ''; repaint(); }}>Message</button>` : nothing}
      ${x.note ? html`<span class="muted small hknote" role="status">${x.note}</span>` : nothing}
      ${x.err ? html`<span class="err small hkerr">${x.err}</span>` : nothing}
    </div>` : nothing}
    ${talk && x.msg && c.can.message ? msgTpl(c, x) : nothing}
  </div>`;
}

// the card's look (the tile's own sheet stays as it is): theme.css's tokens (D184)
const style = document.createElement('style');
style.textContent = `
  .hkid .ach .ic { width: auto; }
  .hkid .hkn { font-weight: 600; margin-right: 4px; }
  .hkid [data-tone="run"] { color: var(--bx-text); } .hkid [data-tone="warn"] { color: var(--bx-warn); }
  .hkid [data-tone="bad"] { color: var(--bx-danger); } .hkid .hkst[data-tone="ok"] { color: var(--bx-ok); }
  .hkid .hks[data-tone="run"], .hkid .hks[data-tone="ok"] { color: inherit; }
  .hkid.hk-approval, .hkid.hk-question, .hkid.hk-login { border-color: var(--bx-warn); }
  .hkid.hk-done { border-left-color: var(--bx-ok); } .hkid.hk-failed, .hkid.hk-lost { border-left-color: var(--bx-danger); }
  .hkid.hk-canceled { border-left-color: var(--bx-border); } .hkid.hk-canceled .ach .hl { color: var(--bx-muted); }
  .hkid .hkline { padding: 0 12px 0 34px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hkid .hkmeta { padding: 1px 12px 0 34px; font: var(--bx-font-meta); color: var(--bx-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hkid .hkmeta .mono { font: var(--bx-font-code); }
  .hkid .hkmeta .mono > bx-icon { --bx-icon-size: 12px; margin-right: 4px; vertical-align: -1px; }
  .hkid .hkpark { padding: 2px 12px 0 34px; }
  .hkid .hkpark .ask { margin: 4px 0; }
  .hkid .hkact { display: flex; flex-wrap: wrap; gap: 4px 12px; align-items: baseline; padding: 4px 12px 8px 32px; }
  .hkid .hkact .lnk:disabled { color: var(--bx-muted); cursor: default; }
  .hkid .hkmsg { display: flex; gap: 8px; padding: 0 12px 8px 34px; }
  .hkid .hkmsg .hkin { flex: 1; min-width: 6em; }
  .hkid .hkmsg .btn { margin: 0; flex: none; }
  .hkid .hkplan { margin: 8px 0; }
  .hkid .hkplan .pe { display: flex; gap: 8px; align-items: baseline; }
  /* a plan entry's square: hollow to do, the accent in progress, filled done */
  .hkid .hkplan .pm { flex: none; align-self: center; box-sizing: border-box; width: 8px; height: 8px; border: 1px solid var(--bx-muted); }
  .hkid .hkplan .pe.in_progress .pm { border-color: var(--bx-accent); background: var(--bx-accent); }
  .hkid .hkplan .pe.completed .pm { border-color: var(--bx-ok); background: var(--bx-ok); }
  .hkid .hkplan .pe.completed { color: var(--bx-muted); text-decoration: line-through; }
  .hkid .hkplan .pe.in_progress { font-weight: 600; }
  .hkid .hktail .msg.assistant .md { max-height: 8.5em; overflow: hidden; }
  .hkid .hkmore { margin-top: 4px; }
  .run .kids { flex: none; display: inline-flex; align-items: center; gap: 2px; font: var(--bx-font-code); color: var(--bx-muted); }
`;
document.head.append(style);

// the Coding agents board draws its rows as these cards (harness-board.js)
export { cardTpl };
