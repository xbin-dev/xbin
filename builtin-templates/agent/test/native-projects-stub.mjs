// native-projects-stub.mjs — native-stub.mjs's fake backend plus Projects'
// routes (projects-stub.mjs's PROJ_STUB and projects-more-stub.mjs's
// PROJ_MORE_STUB, the same init scripts the browser tests install) for the
// native view's Projects tests (hack/agent-template-native-projects.test.mjs):
//
//   runNative({entry: 'native.js', data: {setup: '<this file>', seed: projSeed()}, …})
//
// Exports for {call} steps as native-stub.mjs does: push(ev), route(…), result().
import setupBase, { route, push, result } from './native-stub.mjs';
import { PROJ_STUB } from './projects-stub.mjs';
import { PROJ_MORE_STUB } from './projects-more-stub.mjs';

export default function setup(arg) {
  setupBase(arg);
  const seed = arg.data.seed || {};
  PROJ_STUB(seed);
  PROJ_MORE_STUB(seed);
}

export { route, push, result };
