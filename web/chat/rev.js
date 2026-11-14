/**
 * chat/rev.js — the chat kit's revision (/vendor/chat/rev.js).
 *
 * The kit (/vendor/chat/*: the block model, and later the tool and ask
 * models, the views, the composer) is shared by xbind's Agent tab and by
 * tiles that ship their own chat, whose code may be newer or older than the
 * xbind serving it. A tile that needs a kit feature imports this module
 * first — a guarded dynamic import: an xbind without the kit answers 404 —
 * and compares KIT with the revision it was written against.
 *
 * KIT goes up by one whenever a tile written for the new kit could not run
 * on the old one: a new module, a new export, a new block kind or field a
 * reducer must produce. It never goes down, and nothing is removed or
 * re-shaped under a revision (docs/compat.md: shipped URLs and exports only
 * grow).
 *
 *   1  /vendor/chat/blocks.js: the block model (kinds, the tool record,
 *      keys and versions, nesting)
 */
export const KIT = 1;
