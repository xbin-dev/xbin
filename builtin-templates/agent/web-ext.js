// web-ext.js — the web view's seams (model/ext.js): where a feature module
// draws into the chat, the top bar and the new-chat dialog without editing
// agent.js or chat-cards.js. Register from a module harness-web.js imports:
//
//   import { ext, ctx } from './web-ext.js';
//   ext.register({ block: (b, ui, depth) => …, top: (v) => … });
//
// ctx is what agent.js shares with them once it starts (read it in a hook,
// not at import): ctx.app (model/app.js — its 'harness' event repaints),
// ctx.paint().
//
// Each hook answers a lit template (or null: not mine).
//   block(b, ui, depth)  a transcript block (model/fold.js) drawn your way; the
//                        first answer replaces the built-in card (chat-cards.js)
//   end(s, ui)           after the transcript's last block (s = Session.shown()),
//                        before the built-in approval card, question and
//                        activity line — only while the window reaches the
//                        end. A harness park (run.pendingState.harness) is
//                        left to these hooks: while any answers, the built-in
//                        approval card and question are not drawn for it
//   top(v)               chips in the top bar (v the open view; null at home)
//   paint(v)             after every paint (agent.js paint()), for a module
//                        that keeps DOM of its own in sync; answers nothing
//   newChat(redraw)      when the new-chat dialog opens: {tpl(), body()} —
//                        tpl() is drawn into its fields (again on redraw()),
//                        body() is merged into the POST /ask it sends
//   task(v)              in the unfolded pinned task, after its requests
import { makeExt } from './model/ext.js';

export const ext = makeExt({ block: 'first', end: 'all', top: 'all', paint: 'each', newChat: 'all', task: 'all' });
export const ctx = { app: null, paint: () => {} };
