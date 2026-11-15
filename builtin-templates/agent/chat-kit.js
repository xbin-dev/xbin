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
import { rowView, innerView } from '/vendor/chat/views.js';
import { blockOf, busy } from './model/fold.js';
import { isAcp, isSubagentCall } from './model/harness-heads.js';
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
