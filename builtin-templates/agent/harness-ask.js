// harness-ask.js — a coding harness asking, on the web (D147 §4.2.5,
// §4.2.9, §4.3.4): the card at the end of the chat for its permission
// request (the harness's own options as buttons, reject first when it
// defaults to no; an option that raises it to a bypass mode only for the
// owner, marked with the warning glyph and confirmed; the call, its diff,
// what "always" remembers; an optional word with a rejection), its plan
// approval (the plan, its options, a "keep planning" box sent as feedback)
// and its question (a form from the schema, Submit / Skip; url mode: the
// page, then Done).
//
// The cards are xbind's chat kit's (D224: /vendor/chat/asks.js permView and
// askView, through chat-kit.js kitPark — the same request card the Agent
// tab and the engine's own approval draw); the words and the rules are the
// kit's /vendor/chat/ask.js, reached through model/harness-ask.js.
//
// The `end` seam (web-ext.js): it answers only for a harness run parked on a
// permission or a question — the login park is the sign-in module's, and
// anything else is left to the built-in cards. Verdicts go through
// app.harness (model/harness-store.js); a refused one (the ask is gone: 409)
// is said under the card by the built-in note.
import { ext, ctx } from './web-ext.js';
import { isHarness } from './model/harness.js';
import { kitPark } from './chat-kit.js';

ext.register({
  end(s) {
    const r = s.run || {};
    const ps = r.pendingState || {};
    if (!isHarness(r) || r.status !== 'waiting_input' || !ps.harness) return null;
    if (ps.kind !== 'approval' && ps.kind !== 'question') return null;
    return kitPark(r, ctx.app.session.ui, s.blocks);
  },
});

// A coding agent's card in its parent's chat draws its child's park with
// these (harness-child.js): r is the child's run, so the answer is the
// child's; who: {talk, owner} of the reader there. (The second argument — the
// card's model — was this module's own drawing's; the kit reads the park itself.)
export const permissionTpl = (r, _card, who = null, blocks = null) => kitPark(r, ctx.app.session.ui, blocks, who);
export const questionTpl = (r, _card, who = null) => kitPark(r, ctx.app.session.ui, null, who);
