/**
 * events-socket.js — shared /ws/events connection for the main document
 * (bx-frame and friends). One socket per page, auto-reconnecting.
 *
 * Also keeps the registry of mounted <bx-frame> elements used for
 * longest-prefix reload targeting: a change inside apps/calendar/widgets/x
 * reloads only the most specific mounted frame.
 */

const handlers = new Set();
const reconnects = new Set();
let ws = null;
let backoff = 500;
let opened = false;

function connect() {
  if (ws) return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws/events`);
  ws.onmessage = (m) => {
    let e; try { e = JSON.parse(m.data); } catch { return; }
    for (const h of handlers) { try { h(e); } catch (err) { console.error('bx event handler', err); } }
  };
  ws.onopen = () => {
    backoff = 500;
    // A reconnect may have missed events: whoever keeps state from them reloads it.
    if (opened) for (const cb of reconnects) { try { cb(); } catch (err) { console.error('bx reconnect handler', err); } }
    opened = true;
  };
  ws.onclose = () => { ws = null; setTimeout(connect, (backoff = Math.min(backoff * 2, 15000))); };
}

export function onEvent(cb) {
  handlers.add(cb);
  connect();
  return () => handlers.delete(cb);
}

/** cb runs after every reconnect of the socket (not the first open): events
 *  sent while it was down are lost, so state kept from them is reloaded. */
export function onReconnect(cb) {
  reconnects.add(cb);
  return () => reconnects.delete(cb);
}

/** Mounted frames, for reload targeting. Values: elements with a .src string. */
export const mountedFrames = new Set();

/**
 * True if `frame` is the most specific mounted frame for a change in
 * `component` (exact match or ancestor; longest src wins; ties broken
 * arbitrarily but deterministically by first registration).
 */
export function isReloadTarget(frame, component) {
  if (!frame.src) return false;
  const covers = (f) => component === f.src || component.startsWith(f.src + '/');
  if (!covers(frame)) return false;
  let best = null;
  for (const f of mountedFrames) {
    if (covers(f) && (best === null || f.src.length > best.src.length)) best = f;
  }
  return best === frame;
}
