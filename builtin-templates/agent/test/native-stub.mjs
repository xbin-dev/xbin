// native-stub.mjs — backend.mjs's fake backend (STUB) for the native view's
// tree tests: hack/xbn/node.mjs imports this as `data.setup` in the worker
// that runs native.js, so the same seeds that drive the web tests drive the
// native ones. The stub's own routes answer; this adds what only the native
// view asks for (paged views, deltas on the stream are the stub's SSE as is)
// and the seed's extra routes: seed.routes = [[method, regexp source, json, status?]].
//
//   runNative({entry: 'native.js', data: {setup: '<this file>', seed}, steps: [{call: ['push', ev]}, …]})
//
// Exports for {call} steps: push(ev) (a stream event), route(method, re, json,
// status?) (answer a route from now on); result() → {calls} (what the tile sent).
import { STUB } from './backend.mjs';

let touch = () => {};

export default function setup({ data, touch: t }) {
  touch = t;
  const keep = globalThis.xbin;
  STUB(data.seed || {}); // sets window.xbin (window is globalThis here)
  const stub = globalThis.xbin;
  const json = globalThis.__json;
  // a paged view (?limit=&before=) is the stub's whole view, with nothing older
  globalThis.__route('GET', /\/runs\/(\d+)\/view\?/, async (m) => {
    const r = await stub.fetch(`/api/${stub.self}/runs/${m[1]}/view`);
    const v = await r.json();
    const paged = (data.seed.pages || {})[m[1]];
    return json({ hasOlder: false, compacted: 0, linkCount: (v.links || []).length, ...v, ...(paged || {}) });
  });
  for (const [method, re, body, status] of data.seed.routes || []) route(method, re, body, status);
  // what the worker's own stub has that STUB lacks (native, events, dialogs)
  globalThis.xbin = Object.assign(keep, {
    self: stub.self, iface: data.seed.iface ? (slot) => data.seed.iface[slot] ?? null : stub.iface, download: stub.download,
    fetch: async (u, o) => { touch(); const r = await stub.fetch(u, o); touch(); return r; },
  });
}

export function route(method, re, body, status = 200) {
  globalThis.__route(method, new RegExp(re), () => globalThis.__json(body, status));
}

export function push(ev) { touch(); globalThis.__push(ev); }

export const result = () => ({ calls: globalThis.__calls });
