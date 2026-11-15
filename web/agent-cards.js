/**
 * agent-cards.js — the Agent tab's tool, permission and plan cards (D77), at
 * the URL they always had. The cards are the chat kit's now (D224):
 * /vendor/chat/cards.js (a tool call, a change set), /vendor/chat/asks.js (a
 * permission request, a plan's approval, a question) and
 * /vendor/chat/views.js, all functions of (block, ctx) — the kit's block and
 * a host's ChatCtx — with one stylesheet, /vendor/chat/css.js.
 *
 * This module keeps the names it exported. Each takes what it took — the
 * <bx-agent> element and one of its fold's blocks — says the block in the
 * kit's model (agent-fold.js blockOf) and hands the element's ChatCtx
 * (bx-agent.js chatCtx) to the kit's view. New code imports the kit.
 */
import { unsafeCSS } from '/vendor/lit-all.min.js';
import { blockOf } from '/vendor/agent-fold.js';
import { changeSet, filesStat } from '/vendor/chat/tools.js';
import { blockView } from '/vendor/chat/views.js';
import { toolView, changesView, filesView } from '/vendor/chat/cards.js';
import { permView, askView } from '/vendor/chat/asks.js';
import { chatCssText } from '/vendor/chat/css.js';

export { mdLive } from '/vendor/chat/views.js';
export { KIND_ICON } from '/vendor/chat/cards.js';

/** The cards' styles: the kit's stylesheet (its classes all start with `bxc-`). */
export const cardsCss = [unsafeCSS(chatCssText)];

// the card for one tool call (its body renders once opened)
export const toolCard = (a, t) => toolView(blockOf(t), a.chatCtx, 0, blockView);
// a permission request: the plan card for a plan approval, else the call's headline, what would run, the agent's own options
export const permCard = (a, b) => permView(blockOf(b), a.chatCtx);
// a question the agent asks: a form, or a page to open
export const askCard = (a, b) => askView(blockOf(b), a.chatCtx);
// what a whole turn changed, after its last card
export const changesCard = (a, b) => changesView(blockOf(b), a.chatCtx);
// a snapshot diff reviewed a file at a time; owner: the block it hangs on, f: its {changes, patch}
export const filesBlock = (a, owner, f, label) => filesView(blockOf(owner), changeSet(f), a.chatCtx, label);
// " +added/-removed" across a tool's diffs and its snapshot diff ('' none)
export function diffStat(t) {
  const rec = blockOf(t).rec;
  let add = 0, del = 0;
  for (const d of rec.diffs || []) { add += d.add || 0; del += d.del || 0; }
  if (rec.changed) { const st = filesStat(rec.changed); add += st.add; del += st.del; }
  return add || del ? ` +${add}/-${del}` : '';
}
