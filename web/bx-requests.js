/**
 * bx-requests.js — this page's one read of the decisions waiting for the
 * signed-in person (D195): the grant requests (GET /api/xbin/grants), the
 * interfaces to bind (GET /api/xbin/bindings) and the tiles' limit asks and
 * cap hits (GET /api/xbin/limits?pending=1, D203). Every element that shows
 * them — the shell's prompt on each tile, the sidebar's flags, the
 * organisations badge, a page's own <bx-grants> / <bx-bindings> /
 * <bx-limits> — reads them from here instead of fetching its own copy. They
 * are read again on their events (`grants` or `reload`; `limits`, `reload`
 * or `users` for the limits) and whenever the event socket reconnects (a
 * page that slept, or lost xbind for a while, missed events).
 *
 * Each fresh, successful read tells the person's dismissals what the server
 * still lists (bx-dismiss.js seen(): a dismissal goes only when two such
 * reads in a row — the second 5 s after the first — no longer name its
 * item), and a `prefs` event about the
 * dismissals reads them again — the dismissals are never pruned against a
 * list this page read before another page's change (#11).
 *
 *   grantsStore()   → {value, subscribe(f), load()}   value: the /grants answer
 *   bindingsStore() → {value, subscribe(f), load()}   value: the /bindings answer
 *   limitsStore()   → {value, subscribe(f), load()}   value: the /limits?pending=1 answer
 *                     (null while unread, or from an xbind without the route)
 *   dismissalStore() → bx-dismiss.js dismissals(), loaded and followed
 *
 * The reads are the person's own (plain fetch: the session cookie), as the
 * elements' were.
 */
import { onEvent, onReconnect } from '/vendor/events-socket.js';
import { dismissals, dismissedEvent, grantKey, bindingKey, limitKey } from '/vendor/bx-dismiss.js';

const xfetch = (...a) => (window.xbin?.fetch ?? fetch)(...a);

let followed = false;
// The person's dismissals: read once, and again when another page changes them.
export function dismissalStore() {
  const st = dismissals(xfetch);
  if (!followed) {
    followed = true;
    st.load();
    onEvent((e) => { if (dismissedEvent(e)) st.load(); });
    onReconnect(() => st.load());
  }
  return st;
}

// listStore(url, kind, keyOf, events): one endpoint's answer, read on the
// first subscription and on the event types it follows; overlapping reads
// collapse into one more.
const RECHECK_MS = 5000;
function listStore(url, kind, keyOf, events = ['grants', 'reload']) {
  let value = null, busy = false, again = false, started = false, recheck = 0;
  const subs = new Set();
  const store = {
    get value() { return value; },
    subscribe(f) {
      subs.add(f);
      if (value) f(value);
      if (!started) {
        started = true;
        onEvent((e) => { if (events.includes(e.type)) store.load(); });
        onReconnect(() => store.load());
        store.load();
      }
      return () => subs.delete(f);
    },
    async load() {
      if (busy) { again = true; return; }
      busy = true;
      try {
        do {
          again = false;
          try {
            const r = await fetch(url);
            if (!r.ok) continue; // not admin, or xbind restarting: keep what we had, prune nothing
            value = await r.json();
            for (const f of subs) f(value);
            const ds = dismissalStore();
            ds.seen(kind, (value?.pending ?? []).map(keyOf));
            // a dismissal this read didn't list: read again soon, so one gone
            // for good is pruned and one briefly gone (a half-written
            // xbin.json) is kept (bx-dismiss.js seen)
            if (ds.waiting(kind) && !recheck) recheck = setTimeout(() => { recheck = 0; store.load(); }, RECHECK_MS);
          } catch { /* the next event reads again */ }
        } while (again);
      } finally { busy = false; }
    },
  };
  return store;
}

let grants = null, bindings = null, limits = null;
export const grantsStore = () => (grants ??= listStore('/api/xbin/grants', 'grants', grantKey));
export const bindingsStore = () => (bindings ??= listStore('/api/xbin/bindings', 'bindings', bindingKey));
// a decision, a hit or a new manifest ask publishes `limits`; a membership
// change (`users`) changes who may approve
export const limitsStore = () => (limits ??= listStore('/api/xbin/limits?pending=1', 'limits', limitKey, ['limits', 'reload', 'users']));
