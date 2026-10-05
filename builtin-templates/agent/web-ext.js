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
//   side()               entries in the sidebar under Automations
//   page(p)              the page app.page names when no conversation is open
//                        (not 'automations'): {top, body} templates — the
//                        first module that knows p answers
//   crumb(v)             a link before the open conversation's title ("Web ›")
//                        when no automation crumb is shown
//   dock(v)              sections of the right dock beside Coding agents:
//                        {key, title, badge?, tpl()} (harness-board.js hosts them)
//   card(task)           chips on a project board's task card (task a TaskView)
//   childStatus(r)       words after a coding agent card's status line (r the
//                        child run)
//   sbx(b, close)        actions at the end of the ▣ sandbox popover (#sbxpop,
//                        sandboxes.js; b the conversation's binding, close()
//                        closes the popover; the conversation is
//                        ctx.app.session.current())
// Their call sites: side, page and crumb in agent.js (the Projects page),
// dock in harness-board.js, card in projects.js (the board), childStatus in
// harness-child.js, sbx in sandboxes.js.
import { makeExt } from './model/ext.js';

export const ext = makeExt({ block: 'first', end: 'all', top: 'all', paint: 'each', newChat: 'all', task: 'all',
  side: 'all', page: 'first', crumb: 'first', dock: 'all', card: 'all', childStatus: 'all', sbx: 'all' });
export const ctx = { app: null, paint: () => {} };
