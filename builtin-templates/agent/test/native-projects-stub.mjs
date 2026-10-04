// native-projects-stub.mjs — native-stub.mjs's fake backend plus Projects'
// routes (projects-stub.mjs's PROJ_STUB and projects-more-stub.mjs's
// PROJ_MORE_STUB, the same init scripts the browser tests install) for the
// native view's Projects tests (hack/agent-template-native-projects.test.mjs):
//
//   runNative({entry: 'native.js', data: {setup: '<this file>', seed: projSeed()}, …})
//   seed.cardProbe: a module's ext.card words ("CI ‹state›") on the board's rows
//
// Exports for {call} steps as native-stub.mjs does: push(ev), route(…), result().
import setupBase, { route, push, result } from './native-stub.mjs';
import { PROJ_STUB } from './projects-stub.mjs';
import { PROJ_MORE_STUB } from './projects-more-stub.mjs';
import { ext } from '../native/ext.js';

export default function setup(arg) {
  setupBase(arg);
  const seed = arg.data.seed || {};
  PROJ_STUB(seed);
  PROJ_MORE_STUB(seed);
  // seed.cardProbe: another module's words on a board row (CI's, native/ci.js) — ext.card
  if (seed.cardProbe) ext.register({ card: (t) => (t.ci ? `CI ${t.ci.state}` : null) });
}

export { route, push, result };
