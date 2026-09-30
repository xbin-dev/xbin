// model/moves.js — a shared conversation that stops being shared moves to
// its owner's own space (API.md "Partitioned instances" → "Shared
// conversations"), and a person's page follows it there: the global
// instance's `run` event deleting it names where it went (`movedTo`), and an
// address of its old id (a push link, a saved place) asks the global
// instance's record of the move (GET /moves/{id}). Only a person's partition
// has two homes; anywhere else nothing here asks anything.
import { homeOf } from './homes.js';
import { homeApi } from './home-api.js';

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
