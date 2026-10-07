// model/homes.js — a conversation's home in a partitioned agent (API.md
// "Partitioned instances" → "Shared conversations"). A person's page is
// their own partition; their private conversations live there (ids from
// 2^40), shared ones at the agent's global instance (ids below 2^40), which
// the page reaches through xbin.fetch's {partition: 'global'} — xbind
// attributes those calls to the person, and the global instance applies the
// sharing rules to them as ever. So an id alone says where a conversation
// lives: the router, push links and the iOS app's saved places need nothing
// more.
//
// Only a person's partition (partitionState() 'user') has two homes. An
// unpartitioned instance's page and the global instance's own (the owner
// token) have one: every call goes where it always went, unchanged.
//
// Pure functions, no lit, no DOM; the calls that reach a home are
// model/home-api.js.
import { partitionState } from './partition.js';

/** PARTITION_BASE: where a person's partition numbers its conversations. */
export const PARTITION_BASE = 2 ** 40;

/** twoHomes: does this page have two homes — its person's own partition, and the shared space? */
export const twoHomes = (state = partitionState()) => state === 'user';

/** homeOf(id): 'global' when conversation id lives at the global instance and
 * this page is a person's partition; '' (this page's own backend) otherwise. */
export function homeOf(id, state = partitionState()) {
  if (!twoHomes(state) || id == null || id === '') return '';
  const n = Number(id);
  return Number.isInteger(n) && n > 0 && n < PARTITION_BASE ? 'global' : '';
}

/** publishes(id): is Share on this conversation "share a copy" — a person's
 * own conversation, which only a copy in the shared space can share? */
export const publishes = (id, state = partitionState()) => twoHomes(state) && id != null && homeOf(id, state) === '';

/** shareOf(vis, people): the share a form's choices make — {visibility,
 * teamRole} for the team (vis team-viewer | team-participant) and/or
 * {members} for the people named (user ids, comma- or space-separated; they
 * can write); null: nobody besides you (both views' "Share a copy" and
 * "Who can see it" forms). */
export function shareOf(vis, people) {
  const members = String(people || '').split(/[\s,]+/).map((u) => u.trim().toLowerCase()).filter(Boolean)
    .map((user) => ({ user, role: 'participant' }));
  const team = vis === 'team-viewer' || vis === 'team-participant';
  if (!team && !members.length) return null;
  return { ...(team ? { visibility: 'team', teamRole: vis === 'team-participant' ? 'participant' : 'viewer' } : {}), ...(members.length ? { members } : {}) };
}

/** runOfPath: the conversation a path of this backend's API names (/runs/<id>…), or null. */
export function runOfPath(path) {
  const m = /^\/runs\/(\d+)(?=[/?#]|$)/.exec(String(path || ''));
  return m ? +m[1] : null;
}

/** at(home, opts): request options reaching that home. */
export const at = (home, opts = {}) => (home === 'global' ? { ...opts, partition: 'global' } : opts);

/** listHomes(scope): the homes a conversation list view reads — in a
 * person's partition, Mine (and its archive) both: their own conversations
 * and the shared ones they take part in; Shared the shared space alone. */
export function listHomes(scope, state = partitionState()) {
  if (!twoHomes(state)) return [''];
  return scope === 'mine' ? ['', 'global'] : ['global'];
}

/**
 * splitRows: rows read from several homes, newest activity first (cmp:
 * conv-groups.js byActivity), cut at the horizon — the newest of lasts, the
 * oldest row read from each home that has more pages — so nothing shows
 * below a row a later page could still come above: {shown, held}. With no
 * more pages anywhere everything shows.
 */
export function splitRows(rows, lasts, cmp) {
  const all = [...rows].sort(cmp);
  let horizon = null;
  for (const last of lasts) if (last && (!horizon || cmp(last, horizon) < 0)) horizon = last;
  if (!horizon) return { shown: all, held: [] };
  return { shown: all.filter((r) => cmp(r, horizon) <= 0), held: all.filter((r) => cmp(r, horizon) > 0) };
}
