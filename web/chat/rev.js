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
 *      onHighlighter
 */
export const KIT = 1;
