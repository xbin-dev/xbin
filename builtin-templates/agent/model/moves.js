// model/moves.js — a shared conversation that stops being shared moves to
// its owner's own space (API.md "Partitioned instances" → "Shared
// conversations"), and a person's page follows it there: the global
// instance's `run` event deleting it names where it went (`movedTo`), and an
// address of its old id (a push link, a saved place) asks the global
// instance's record of the move (GET /moves/{id}). Only a person's partition
// has two homes; anywhere else nothing here asks anything.
import { homeOf, twoHomes, PARTITION_BASE } from './homes.js';
import { homeApi } from './home-api.js';
import { partitionState } from './partition.js';
import { hostedId, TEAM_BASE } from './hosted.js';

/** followsMove(id, to, row, me): does the page follow open conversation id,
 * deleted with `movedTo: to`, to where it went? Only a person's own page
 * does — their partition's — and only for their own conversation (row: the
 * summary it had, `owner` me), moved from the shared space into their own
 * space, or a hosted one of theirs back at the shared instance (un-shared —
 * from where it moves on into their own space, followed again — or
 * continued without its host). Anyone else's page (the owner token's at the
 * global instance) goes home, as for any conversation deleted. */
export function followsMove(id, to, row, me, state = partitionState()) {
  const n = Number(to);
  const where = n >= PARTITION_BASE || (hostedId(id) && n > 0 && n < TEAM_BASE);
  return !!to && where && twoHomes(state) && homeOf(id, state) === 'global' && !!me && row?.owner === me;
}

/** movedTo(id): the id conversation id has in your own space since it moved there, or null. */
export async function movedTo(id) {
  if (homeOf(id) !== 'global') return null;
  try {
    const m = await homeApi('global', `/moves/${id}`);
    return m && m.state === 'moved' && m.to ? m.to : null;
  } catch {
    return null;
  }
}
