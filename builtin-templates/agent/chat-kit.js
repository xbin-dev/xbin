// chat-kit.js — the chat drawn with xbind's chat kit (D224): the views of
// /vendor/chat/views.js over the fold's blocks said in the kit's block model
// (model/fold.js blockOf), and the ChatCtx they reach this page through.
//
// One renderer for a conversation whatever produced it: a coding agent's
// calls and the template's own tools are the same card, a harness's request
// and the engine's own approval the same request. What the kit does not
// know stays here, as its host's: the hook classes and data attributes a
// row carries for this page's tests and an instance's styles (rowAttrs —
// .tcard[data-tool], .msg.user, …), a journal line (the `step` kind), and
// the cards a feature module draws its own way (web-ext.js ext.block).
//
// The kit's stylesheet (`bxc-` classes) sits in the document once, beside
// index.html's own rules; SKIN below is this page's tuning of it.
import { chatCssText } from '/vendor/chat/css.js';
import { html, nothing } from '/vendor/lit-all.min.js';
import { rowView, innerView, blockView, noticeView } from '/vendor/chat/views.js';
import { agentView } from '/vendor/chat/cards.js';
import { blockOf, busy } from './model/fold.js';
import { isAcp, isSubagentCall } from './model/harness-heads.js';
import { parkBlock, ownerOf } from './model/harness-ask.js';
import { nameOf } from './model/harness.js';
import { access, grantAsk, compactionWords } from './model/rules.js';
import { copyAnswer } from './chat-copy.js'; // an answer's Copy: its markdown and its rendering (D199)
import { docStepTpl } from './doc-cards.js'; // a document the agent wrote (D201)
import { ctx as page } from './web-ext.js';

const SKIN = `
/* the kit's rows in this page's timeline */
#timeline { --bxc-gap: 8px; }
#timeline .bxc-tool { margin: 4px 0; background: var(--bx-code-bg); }
#timeline .bxc-tool .bxc-body { padding: 4px 12px 8px 32px; }
#timeline .bxc-tool.bxc-agent { background: var(--bx-panel); }
#timeline .bxc-tool.bxc-agent > .bxc-body { padding: 0 12px 8px 16px; border-top-style: dashed; }
#timeline .bxc-tool.bxc-agent > .bxc-body > .bxc-task { margin-top: 8px; }
#timeline .bxc-tool.bxc-agent .bxc-children { border-left: 0; padding-left: 0; }
/* an answer's Copy says how it went (chat-copy.js) */
.bxc-act.done { color: var(--bx-ok); }
/* a refused verdict, said under the card it was about */
.anote { display: flex; align-items: center; gap: 4px; }
`;
{
  const style = document.createElement('style');
  style.dataset.of = 'chat-kit';
  style.textContent = chatCssText + SKIN;
  document.head.append(style);
}

// ---- the host's hooks on a row -------------------------------------------------------------

// what a block's element carries beside the kit's own: the classes and data
// attributes this page's cards always had (b: the fold's block)
function hooksOf(b, open) {
  switch (b.k) {
    case 'tool': {
      // (a failed call opens by itself; a subagent's while it works)
      const dflt = b.state === 'error' || (isSubagentCall(b) && !['done', 'stopped'].includes(b.state));
      return { class: `tcard ${b.acp || isAcp(b.name) ? 'hcard ' : ''}${b.state || ''}${open(b.id, dflt) ? ' on' : ''}`,
        'data-fam': b.fam || '', 'data-tool': b.name || '', 'data-status': (b.acp && b.acp.status) || '' };
    }
    case 'user': return { class: `msg user${b.sender && page.app && b.sender !== meOf() ? ' other' : ''}` };
    case 'assistant': return { class: 'msg assistant' };
    case 'draft': return { class: 'msg assistant live' };
    case 'think': return { class: `think${b.live ? ' live' : ''}${open(b.id, b.live) ? ' on' : ''}` };
    case 'notice': return { class: `notice${open(b.id, false) ? ' on' : ''}` };
    case 'step': return b.kind === 'render' && b.detail && b.detail.doc ? null // (a document's line is doc-cards.js's own element)
      : { class: `step ${b.kind}${b.kind === 'finish' && b.detail && b.detail.result ? ' md-step' : ''}` };
    case 'agent': return { class: `acard ${b.state || ''}${open(b.id, b.state === 'running' || b.state === 'approval' || b.state === 'error') ? ' on' : ''}`, 'data-child': b.childId || '' };
  }
  return null;
}

// who reads: the id a message's sender is compared with
const meOf = () => { const ui = page.app && page.app.session.ui; return ui && ui.me ? ui.me() : ''; };

// ---- a journal line (the fold's `step` kind) as the kit's notice ------------------------------------

// finish's result is markdown (a model often puts its whole answer there); a
// render or a live page is a button that shows it again (the ChatCtx's act)
const lines = new WeakMap(); // a step's kit block → its notice
function lineOf(kb) {
  let n = lines.get(kb);
  if (n) return n;
  const d = kb.detail || {};
  const say = (icon, text, more = {}) => ({ k: kb.k, v: kb.v, kind: 'notice', icon, text, ...more });
  switch (kb.step) {
    case 'error': n = say('warning', d.error || d.text || '', { tone: 'bad' }); break;
    case 'compaction': n = say('compact', compactionWords(d)); break;
    case 'yield': n = say('pause', `slept ${d.seconds ?? ''}s`); break;
    case 'finish': n = d.result ? say('check', d.result, { md: true }) : say('check', 'finished'); break;
    case 'state_changed': n = say('info', `state changed${d.summary ? ': ' + d.summary : ''}`); break;
    case 'cancel': n = say('stop', `cancelled${d.reason ? ': ' + d.reason : ''}`); break;
    case 'ask': n = say('question', `asked: ${d.question || ''}`); break;
    case 'render': n = say('photo', '', { action: { label: `rendered ${d.path || ''} v${d.version || ''}`, title: 'show it in the preview pane' } }); break;
    case 'live': n = say('signal', '', { action: { label: `showing ${d.name || d.sandbox || 'the sandbox'}:${d.port || ''}${d.path || '/'} live`, title: 'show it live in the preview pane' } }); break;
    default: n = say('', d.text || '');
  }
  Object.defineProperty(n, '$step', { value: kb });
  lines.set(kb, n);
  return n;
}

// ---- the ChatCtx -----------------------------------------------------------------------------

const slotKey = (b, slot) => (slot === 'body' ? b.k : `${b.k}:${slot}`);
const made = new WeakMap(); // ui → its ChatCtx

/**
 * kitCtx(ui): the ChatCtx of this page's chat, over the Session's ui
 * (model/session.js: what is open, the page's actions) and the app.
 */
export function kitCtx(ui) {
  let c = made.get(ui);
  if (c) return c;
  // rev moves when the reader folds or unfolds anything (whoever asks: a row is drawn again)
  let rev = 0;
  const bags = new Map(); // a block's key → what is typed into its card (a form's values, a word with a rejection)
  const toggle = ui.toggle;
  ui.toggle = (id, dflt) => { rev++; toggle(id, dflt); };
  const run = () => (page.app && page.app.session.current() ? page.app.session.current().run : null) || {};
  c = {
    get running() { return busy(run().status); },
    ended: false,
    readonly: false,
    isOpen: (b, slot, dflt) => ui.isOpen(slotKey(b, slot), !!dflt),
    setOpen: (b, slot, open, dflt) => { if (ui.isOpen(slotKey(b, slot), !!dflt) !== !!open) ui.toggle(slotKey(b, slot), !!dflt); },
    // (…or the host's state for the block moved: a file's picture arrived, a subagent could not be read)
    rev: (b) => (b.kind === 'user' && b.files && b.files.length && ui.file ? rev + ':' + b.files.map((f) => { const st = ui.file(b.ref, f); return `${st.linked ? 1 : 0}${st.thumb || ''}`; }).join()
      : b.kind === 'agent' ? `${rev}:${ui.readError && b.ref ? ui.readError(b.ref) : ''}:${ui.approveNote && b.ref ? ui.approveNote(b.ref) : ''}` : rev),
    state: (b) => { let bag = bags.get(b.k); if (!bag) { if (bags.size > 40) bags.delete(bags.keys().next().value); bags.set(b.k, (bag = {})); } return bag; },
    copy: (text) => navigator.clipboard.writeText(text),
    repaint: () => page.paint(),
    get me() { return ui.me ? ui.me() : ''; },
    // a message's files: still there, with a picture? (model/session.js fileState) — open one in the Files tab
    fileState: (b, f) => (ui.file ? ui.file(b.ref, f) : null),
    openFile: (b, f) => ui.act.openFile(f.path),
    copyMessage: (b, e) => copyAnswer(e, b.text),
    // a journal line that shows something again
    act: (n) => {
      const kb = n.$step, d = (kb && kb.detail) || {};
      if (kb && kb.step === 'render') ui.act.openPreview?.(d.path, d.version, kb.run, d.links);
      else if (kb && kb.step === 'live') ui.act.openLive?.(d, kb.run);
    },
    // a subagent's card: its full session, its steps (read once it is open), why they could not be read
    openAgent: (b) => ui.act.select(b.ref),
    loadAgent: (b, again) => (again ? ui.act.retryRead(b.ref) : ui.act.loadChild(b.ref)),
    agentError: (b) => (b.ref && ui.readError ? ui.readError(b.ref) : ''),
    rowAttrs: (kb) => (kb.$src ? hooksOf(kb.$src, ui.isOpen) : null),
    // what the kit leaves to this page: a feature module's own way with a block inside a card (web-ext.js
    // ext.block; a row of the timeline was asked before it came here), a journal line, a document the agent
    // wrote, the request of a subagent (answered on its run), and the note under a subagent's card
    other: (kb, depth) => {
      if (kb.$run) return kitPark(kb.$run, ui) || nothing;
      const b = kb.$src;
      if (!b) return null;
      const x = depth ? ui.ext?.block(b, ui, depth) : null;
      if (x) return x;
      if (kb.kind === 'step') return kb.step === 'render' && kb.detail && kb.detail.doc ? docStepTpl(b, ui.act || {}) : noticeView(lineOf(kb), c);
      if (kb.kind === 'agent') {
        const note = kb.ref && ui.approveNote ? ui.approveNote(kb.ref) : '';
        return html`${agentView(kb, c, depth, innerView)}${note ? html`<div class="anote muted small" role="status"><bx-icon name="warning"></bx-icon><span>${note}</span></div>` : nothing}`;
      }
      return null;
    },
  };
  made.set(ui, c);
  return c;
}

/**
 * kitRow(b, ui, depth): a fold block drawn by the kit — a row of the
 * timeline (`.bxc-row[data-k]`), or inside another block's card.
 */
export function kitRow(b, ui, depth = 0) {
  const kb = blockOf(b);
  return depth ? innerView(kb, kitCtx(ui), depth) : rowView(kb, kitCtx(ui));
}

// ---- what a run waits on a person for ------------------------------------------------------

const parks = new WeakMap(); // a run's pendingState → its request's block (the same while the park is)

// the fold's block of a stored call id, searched in the held blocks (and under a subagent's call)
function callBlock(blocks, callId) {
  for (const b of blocks || []) {
    if (b.k === 'tool' && b.id === 'c' + callId) return b;
    if (b.kids) { const x = callBlock(b.kids, callId); if (x) return x; }
  }
  return null;
}

/**
 * kitPark(run, ui, blocks): what a run is parked on, as the kit's request
 * card — a coding agent's permission request, plan approval or question
 * (answered through app.harness, by who may: a viewer sees it without its
 * answers, an option that stops the agent asking is the owner's), the
 * engine's own approval of the calls it wants to run (Approve / Deny; a
 * capability only the conversation's owner may grant: once, or here for an
 * hour) and its question (the next message answers it). run may be a child's
 * (its card in the parent's chat answers on the child); blocks: the run's
 * held blocks, for the diff of the call a request is about. null: not one of
 * these (a sign-in is signin.js's). who: {talk, owner} when the run is not
 * the open conversation's (a card on the board: its parent's view says).
 *
 * The card keeps this page's hooks on its element: .ask — .hask for a
 * coding agent's (.hplan a plan, .hq a question), .approve for the engine's
 * own (.grant a capability) — and data-park.
 */
export function kitPark(run, ui, blocks = null, who = null) {
  const ps = run && run.status === 'waiting_input' ? run.pendingState : null;
  if (!ps) return null;
  let kb = parks.get(ps);
  if (!kb) {
    // a park nothing else answers (a kind this page does not know) still shows what the run says
    kb = parkBlock(run) || (run.result ? { k: 'park:' + (ps.park || ''), v: 0, kind: 'ask', eid: '', park: ps.park || '', callId: '', message: run.result, mode: 'text', schema: null, action: null, by: null, content: null } : null);
    if (!kb) return null;
    parks.set(ps, kb);
  }
  if (kb.kind !== 'perm' && kb.kind !== 'ask') return null;
  const app = page.app;
  const hs = ps.harness && (ps.kind === 'approval' || ps.kind === 'question') ? ps.harness : null;
  const v = app.session.current();
  let cls = 'ask', own;
  if (hs) {
    cls += ` hask${kb.plan ? ' hplan' : ''}${kb.kind === 'ask' ? ' hq' : ''}`;
    own = {
      agentName: nameOf(run.harness), rejectWord: true,
      can: who ? { answer: !!who.talk, raise: !!who.owner } : { answer: access(v).talk, raise: ownerOf(v, app.me) },
      callOf: () => { const b = hs.callId ? callBlock(blocks, hs.callId) : null; return b ? blockOf(b).rec : null; },
      permit: (b, a) => app.harness.permit(run.id, { option: a.optionId, park: b.park, ...(a.feedback ? { feedback: a.feedback } : {}) }),
      answer: (b, a) => app.harness.answer(run.id, a.action, a.content, b.park),
    };
  } else if (kb.kind === 'perm') {
    // the engine's own approval; a capability's grant is its owner's to allow (model/rules.js grantAsk)
    const grant = grantAsk(run, ui.who ? ui.who() : null);
    cls += ` approve${grant ? ' grant' : ''}`;
    if (grant) {
      kb.lead = grant.lead;
      kb.description = grant.note;
      kb.options = [...(grant.canAllow ? [{ optionId: 'once', name: 'Allow once', kind: 'allow_once', explicit: false }, { optionId: 'hour', name: 'Allow here for 1 hour', kind: 'allow_once', explicit: false }] : []),
        { optionId: 'deny', name: 'Deny', kind: 'reject_once', explicit: false }];
    }
    own = {
      agentName: run.id !== app.session.sel ? 'The subagent' : 'The agent', can: { answer: true, raise: false },
      permit: (b, a) => ui.act.approve(run.id, a.verdict === 'allow', a.optionId === 'once' || a.optionId === 'hour' ? a.optionId : undefined, b.park),
    };
  } else {
    own = { agentName: 'The agent', can: { answer: access(v).talk, raise: false } };
  }
  const c = Object.create(kitCtx(ui), Object.getOwnPropertyDescriptors(own));
  return html`<div class=${cls} data-park=${kb.park || ''}>${blockView(kb, c)}</div>`;
}
