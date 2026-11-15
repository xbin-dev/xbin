// ws-native-stub.mjs — ws-stub.mjs's fake of a workspace agent's backend
// (WS_STUB: the /ws/ routes, the run view, the live stream, xbind's one
// read) for the native view's tree tests: hack/xbn/node.mjs imports this as
// `data.setup` in the worker that runs native.js, so the seeds the web
// page's browser scripts use drive the native view too
// (hack/agent-template-native-ws.test.mjs).
//
//   runNative({entry: 'native.js', data: {self: 'xbin/wsagent', setup: '<this file>', seed}, steps: [{call: ['agent', msg]}, …]})
//
// Exports for {call} steps: agent(msg) (the app's chrome tells the view
// something: xbn.agent), push(ev) (a stream event), event(ev) (xbind's
// /ws/events), drop(on) (the connection is lost until drop(false)), bye()
// (the backend is being replaced), seed(patch) (change what the routes
// answer); result() → {calls} (what the view sent).
import { WS_STUB } from './ws-stub.mjs';

let touch = () => {};

export default function setup({ data, touch: t }) {
  touch = t;
  const keep = globalThis.xbin;
  WS_STUB(data.seed || {}); // sets window.xbin (window is globalThis here)
  const stub = globalThis.xbin;
  globalThis.xbin = Object.assign(keep, {
    self: stub.self, events: stub.events,
    fetch: async (u, o) => { touch(); try { return await stub.fetch(u, o); } finally { touch(); } },
  });
}

export function agent(msg) { touch(); return globalThis.xbn.agent(msg); }
export function push(ev) { touch(); globalThis.__push(ev); }
export function event(ev) { touch(); globalThis.__event(ev); }
export function drop(on) { touch(); globalThis.__drop(on); }
export function bye() { touch(); globalThis.__bye(); }
export function seed(patch) { touch(); Object.assign(globalThis.__seed, patch); }

export const result = () => ({ calls: globalThis.__calls });
