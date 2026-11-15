/**
 * chat/rev.js — the revision of the chat kit this xbind serves (D224): the
 * modules under /vendor/chat/ and the shared ones they stand on.
 *
 * KIT goes up by one whenever the kit gains something a page can import — a
 * module, an export, a behaviour a caller has to be written for. It never
 * goes down and nothing is ever taken away (docs/compat.md rule 3), so "KIT
 * is at least N" means "everything a page written for revision N imports is
 * here".
 *
 * A page that ships apart from xbind (the agent template: its instances are
 * copies in workspaces) imports this module first, guarded, and compares KIT
 * with the revision it was written for before it loads anything else from
 * the kit — on an xbind from before the kit this URL is a 404, and the page
 * says which xbind it needs instead of failing on a missing export.
 *
 *   1  /vendor/chat/md.js; /vendor/bx-md.js gains closedFence, mdEpoch and
 *      onHighlighter; /vendor/chat/blocks.js: the block model (kinds, the
 *      tool record, keys and versions, nesting)
 *   2  /vendor/chat/tools.js: a tool call from its record (the merge of an
 *      agent's events, headline, outcome and state chips, output tail,
 *      diffs, plan approval); /vendor/chat/ask.js: a permission request's
 *      card, a question's form, the slash menu, the composer's words
 *   3  the views: /vendor/chat/views.js (rows, messages, dispatch),
 *      cards.js (tool, subagent, sub-conversation, changes, plan),
 *      asks.js (permission, plan approval, question, sign-in; the element
 *      <bx-chat-form>), composer.js (<bx-chat-composer>), window.js
 *      (ChatWindow), css.js (chatCssText); blocks.js gains the `host` kind,
 *      usageBadge, cached and ChatCtx's readonly, role, rev, state, copy,
 *      openDiff, signin, host; tools.js DIFF_LINES, lineCount, headLines,
 *      diffBody; ask.js stageFiles, fmtSize, ATTACH_LIMITS
 */
export const KIT = 3;
