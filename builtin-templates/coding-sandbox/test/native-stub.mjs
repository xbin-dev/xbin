// native-stub.mjs — stub.mjs's fake backend for the native view's tree tests:
// hack/xbn/node.mjs imports this as `data.setup` in the worker that runs
// native.js, so the seeds that drive the web tests drive the native ones.
//
//   runNative({entry: 'native.js', data: {setup: '<this file>', seed}, steps})
//
// result() → {calls} (what the tile sent). What it hands the app
// (xbin.native.share) is in the run's messages ({op: "call", what: "share"}).
import { STUB } from './stub.mjs';

let touch = () => {};

export default function setup({ data, touch: t }) {
  touch = t;
  STUB(data.seed || {}); // onto the worker's own xbin (its native, ws, …)
  const f = globalThis.xbin.fetch;
  globalThis.xbin.fetch = async (u, o) => { touch(); const r = await f(u, o); touch(); return r; };
}

export const result = () => ({ calls: globalThis.__calls });
