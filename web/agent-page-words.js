/**
 * agent-page-words.js — what the agent's page may ask of xbin's chrome
 * (/vendor/agent-page-words.js): the strict reading of a message from the
 * page (pageMessage), a new session's draft as plain text (plainDraft,
 * DRAFT_FROM), what the dock tells the page (contextMessage,
 * SIGNINS_MESSAGE). Split from agent-chrome-words.js for the size budget
 * (D253, as agent-result-words.js was, D219) and re-exported there, so the
 * elements, the vectors (native/tools/delegation-words.mjs) and the app's
 * port (AgentBridge.swift) read it as part of the chrome's words. Pure: no
 * DOM, no lit, no imports.
 */

// a request's or change set's id, as xbind mints them (24 lowercase hex digits): nothing else — never a dot segment or a
// path — is read from the page, and it goes into a URL the dock sends with the person's session
const ID = /^[0-9a-f]{24}$/;
const SESSION = /^[A-Za-z0-9_-]{1,64}$/;
const tilePath = (v) => {
  const t = typeof v === 'string' ? v.replace(/^\/+|\/+$/g, '') : '';
  return t && t.length <= 512 && !t.split('/').some((x) => !x || x === '.' || x === '..') ? t : '';
};
/** DRAFT_MAX: the most of a person's words a new session's draft carries, in characters. */
export const DRAFT_MAX = 8000;
// control characters but newline and tab, and the invisible ones that reorder or hide text
const UNSEEN = /[\u0000-\u0008\u000B-\u001F\u007F-\u009F\u200B-\u200F\u202A-\u202E\u2066-\u2069\uFEFF]/g;
/**
 * DRAFT_FROM: how a new session's draft from the agent's page is labelled
 * wherever the chrome shows it. The page renders what a model wrote and
 * its message is untrusted: the text is what the page SUGGESTS, for the
 * person to read and edit — never presented as their own words — and it
 * waits, unsent, until they press Send.
 */
export const DRAFT_FROM = Object.freeze({
  label: 'Suggested by your agent’s page — not your words',
  text: 'Read it and change it as you like. It waits in the new session’s message box: nothing is sent until you press Send.',
  paste: 'paste the text your agent’s page suggests, once you have read it',
  copy: 'Copy the suggested text',
});

/**
 * plainDraft(v): words for a new session's composer, as plain text — never
 * markup, never a command: line ends made \n, control and invisible
 * characters dropped, trimmed, at most DRAFT_MAX characters; '' for
 * anything that isn't a string.
 */
export function plainDraft(v) {
  if (typeof v !== 'string') return '';
  const t = v.replace(/\r\n?/g, '\n').replace(UNSEEN, '').trim();
  // at most DRAFT_MAX UTF-16 units, never half a surrogate pair (a lone half is no character)
  const end = t.length > DRAFT_MAX && /[\uD800-\uDBFF]/.test(t[DRAFT_MAX - 1]) ? DRAFT_MAX - 1 : DRAFT_MAX;
  return t.slice(0, end);
}

/** handoffLink(tile): the plain link to a tile, for a shell that can't open a new session of its agent itself. */
export const handoffLink = (tile) => `/?tile=${encodeURIComponent(tilePath(tile)).replace(/%2F/gi, '/')}`;

/**
 * pageMessage(data): a message from the agent's page, read strictly — the
 * whole of what the page may ask of the chrome:
 *
 *   xbin:agent-open-request {id}      → {type: 'review', kind: 'request', id}
 *   xbin:agent-open-changeset {id}    → {type: 'review', kind: 'changeset', id}
 *   xbin:open-session {session, tile} → {type: 'session', tile, session}
 *   xbin:open-session {session: "new", tile, draft}
 *                                     → {type: 'new-session', tile, draft}  (draft: plainDraft)
 *   xbin:agent-open {view}            → {type: 'open', view}  (needs | approvals | activity — never prefs)
 *   xbin:agent-open-signin {harness}  → {type: 'signin', harness}  (a coding agent's id: the chrome draws the sign-in, D230)
 *
 * or null. Ids and a word — and a new session's draft, the person's own
 * words, kept as plain text for its composer: no other field of a message
 * is ever read (the dock fetches the request from xbind itself), and none
 * of them decides anything — a draft is never sent by being handed over.
 */
export function pageMessage(data) {
  if (!data || typeof data !== 'object') return null;
  const id = typeof data.id === 'string' && ID.test(data.id) ? data.id : '';
  if (data.type === 'xbin:agent-open-request') return id ? { type: 'review', kind: 'request', id } : null;
  if (data.type === 'xbin:agent-open-changeset') return id ? { type: 'review', kind: 'changeset', id } : null;
  if (data.type === 'xbin:open-session') {
    const tile = tilePath(data.tile), session = typeof data.session === 'string' && SESSION.test(data.session) ? data.session : '';
    if (tile && session === 'new') return { type: 'new-session', tile, draft: plainDraft(data.draft) };
    return tile && session ? { type: 'session', tile, session } : null;
  }
  // a coding agent's sign-in: the page names the harness, the chrome draws the card and talks to xbind (D230)
  if (data.type === 'xbin:agent-open-signin') return typeof data.harness === 'string' && HARNESS.test(data.harness) ? { type: 'signin', harness: data.harness } : null;
  // only views that change nothing: Preferences — where the agent's reach widens — opens from the chrome's own menu alone
  if (data.type === 'xbin:agent-open') return PAGE_VIEWS.includes(data.view) ? { type: 'open', view: data.view } : null;
  return null;
}

// a coding agent's id, as the catalog spells them
const HARNESS = /^[a-z][a-z0-9-]{0,31}$/;

/** SIGNINS_MESSAGE: what the dock tells the page after the person's saved sign-ins changed — re-read what a start would run on. */
export const SIGNINS_MESSAGE = Object.freeze({ type: 'xbin:agent-signins' });

/** PAGE_VIEWS: the dock's views the agent's page may ask for (xbin:agent-open {view}) — the ones that change nothing. */
export const PAGE_VIEWS = Object.freeze(['needs', 'approvals', 'activity']);

/** contextMessage(context): what the dock tells the page it was opened about — xbin:agent-context {tile} or {screen} — or null. */
export function contextMessage(context) {
  if (!context || typeof context !== 'object') return null;
  const tile = tilePath(context.tile);
  if (tile) return { type: 'xbin:agent-context', tile };
  return typeof context.screen === 'string' && context.screen && context.screen.length <= 200 ? { type: 'xbin:agent-context', screen: context.screen } : null;
}
