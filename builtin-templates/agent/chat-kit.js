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
import { html } from '/vendor/lit-all.min.js';
import { rowView, innerView, blockView } from '/vendor/chat/views.js';
import { blockOf, busy } from './model/fold.js';
import { isAcp, isSubagentCall } from './model/harness-heads.js';
import { parkBlock, ownerOf } from './model/harness-ask.js';
import { nameOf } from './model/harness.js';
import { access, grantAsk } from './model/rules.js';
import { ctx as page } from './web-ext.js';

const SKIN = `
/* the kit's rows in this page's timeline */
#timeline { --bxc-gap: 8px; }
#timeline .bxc-tool { margin: 4px 0; background: var(--bx-code-bg); }
#timeline .bxc-tool .bxc-body { padding: 4px 12px 8px 32px; }
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
  }
  return null;
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
    rev: () => rev,
    state: (b) => { let bag = bags.get(b.k); if (!bag) { if (bags.size > 40) bags.delete(bags.keys().next().value); bags.set(b.k, (bag = {})); } return bag; },
    copy: (text) => navigator.clipboard.writeText(text),
    repaint: () => page.paint(),
    rowAttrs: (kb) => (kb.$src ? hooksOf(kb.$src, ui.isOpen) : null),
    // a block inside a card: a feature module's own way first (web-ext.js ext.block), then a kind the kit
    // does not draw yet (the page's own template); a row of the timeline was asked before it came here
    other: (kb, depth) => {
      const b = kb.$src;
      if (!b || !depth) return null;
      return ui.ext?.block(b, ui, depth) || (KIT.has(b.k) ? null : own(b, ui, depth));
    },
  };
  made.set(ui, c);
  return c;
}

/** The fold's kinds the kit draws; the rest are still this page's own templates (chat-cards.js). */
export const KIT = new Set(['tool']);
let own = () => null;
/** ownViews(fn): chat-cards.js hands over its templates for the kinds above's complement. */
export function ownViews(fn) { own = fn; }

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
