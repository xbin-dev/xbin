// model/harness-homes.js — coding agents (D147) in a partitioned agent, as
// both views follow it (API.md "Coding agents" → "In a partitioned
// instance"): a coding agent works only in a person's own conversations.
// Its sign-in lives in its sandbox's HOME, so a shared or hosted chat, the
// global instance's channels and triggers, and the global instance's own
// page never start one or sign one in, and a coding agent's conversation
// never moves between homes (no Share a copy, no Copy to my own space, no
// hosting). So:
//
//   - where one starts (harnessesHere): an unpartitioned page and a
//     person's partition — never the global instance's page (the owner
//     token), whose "Who answers" is the built-in agent alone;
//   - the sandbox it starts in (homedWhy): in a person's partition, one
//     homed there — the team's sandboxes and ones shared with them don't
//     fit, and say why (the pick then offers Create);
//   - its sign-in (signInAway): only where the credentials stay the
//     person's — unpartitioned as ever, and in a person's partition for a
//     run homed there; elsewhere the card is read-only and says why;
//   - the new-chat dialog (sharedNewChat): a chat shared with others is
//     the built-in agent's, and takes along no sandbox the shared space
//     can't see (sharedSees);
//   - moving (keepsHome): a coding agent's conversation stays where it
//     started.
//
// An unpartitioned page gets '' / true everywhere: today's behaviour. Pure
// functions, no lit, no DOM; node-tested in
// hack/agent-template-homes.test.mjs.
import { partitionState } from './partition.js';
import { homeOf } from './homes.js';

/** harnessesHere: may this page start a coding agent (and pick one to answer new chats)? */
export const harnessesHere = (state = partitionState()) => state !== 'global';

/** notOwn: why sandbox `name` can't hold a coding agent's conversation in a person's partition. */
export const notOwn = (name) => `${name || 'it'} isn't a sandbox of your own space (the team's, or shared with you) — ` +
  'a coding agent works only in one of your own, where its sign-in stays yours';

/**
 * homedWhy: '' when sandbox row s (GET /sandboxes, where you are) may hold a
 * new coding agent's conversation started from this page, else why not. Only
 * a person's partition asks: there a conversation works only in a sandbox
 * homed in it (the backend's partitionBoxRefusal). The backend's own word on
 * a row wins when it sends one (`homed`, with `why`: sandbox_routes.go
 * sandboxItem; a `bindWhy` is read too); else it is derived as the backend
 * checks it — not seen through a share (`shared`), and its manager's owner
 * names this partition (owner.partitionId and owner.partition) and this
 * tile (owner.via).
 */
export function homedWhy(s, state = partitionState(), x = globalThis.xbin) {
  if (state !== 'user' || !s) return '';
  const name = s.name || s.id || s.ref || '';
  if (typeof s.homed === 'boolean') return s.homed ? '' : String(s.why || s.bindWhy || '') || notOwn(name);
  const o = s.owner || {};
  const own = !s.shared && !!o.partitionId && !!o.partition && o.partition === (x && x.partition) && !!o.via && o.via === (x && x.self);
  return own ? '' : notOwn(name);
}

/** SIGNIN_GLOBAL / SIGNIN_SHARED: why a sign-in card is read-only there. */
export const SIGNIN_GLOBAL = 'Coding agents sign in only in a person\'s own conversations — this shared instance holds no one\'s credentials. Sign in as a person and start one in your own space.';
export const SIGNIN_SHARED = 'This conversation is in the shared space: a coding agent signs in only in your own conversations, where its credentials stay yours — start one there.';

/**
 * signInAway: '' when the sign-in of run id may be offered on this page,
 * else why not (the card then offers nothing): the global instance's page
 * never; a person's partition only for a run homed there (not a shared
 * conversation's, at the global instance).
 */
export function signInAway(id, state = partitionState()) {
  if (state === 'global') return SIGNIN_GLOBAL;
  if (state === 'user' && homeOf(id, state) === 'global') return SIGNIN_SHARED;
  return '';
}

/** SHARED_BUILTIN: why a new shared chat's "Who answers" is the built-in agent. */
export const SHARED_BUILTIN = 'A chat shared with others is answered by the built-in agent — coding agents work only in your own conversations';

/** sharedNewChat: the new-chat dialog's "Who can see it" (homes-ui.js
 * mountNewShare: 'mine' or a shared choice) → why "Who answers" is fixed
 * to the built-in agent ('' = any may answer). */
export const sharedNewChat = (vis, state = partitionState()) => (state === 'user' && vis && vis !== 'mine' ? SHARED_BUILTIN : '');

/**
 * sharedSees: may a chat shared with others — made at the global instance —
 * carry sandbox row s of your partition's list (the next new chat's pick,
 * which the ask would send)? Only one the shared space sees too: the
 * team's, or one shared with the agent (`shared`). One homed in your
 * partition, or one the list doesn't have, stays behind: the global
 * instance can't see it, and would refuse the ask.
 */
export const sharedSees = (s) => !!(s && s.shared);

/** keepsHome: a coding agent's conversation (a harness run at its root:
 * a row, a run, or a share dialog's {id, title, engine}) — it never moves
 * between homes, so neither Share a copy, Copy to my own space nor hosting
 * is offered on it. */
export const keepsHome = (r) => !!r && r.engine === 'harness' && !r.parentId;

/** KEEPS_HOME: what a coding agent's conversation says of its sharing, in a person's partition. */
export const KEEPS_HOME = 'A coding agent\'s conversation stays in your own space: it can\'t be shared, copied or moved — its sandbox and sign-in are yours';
