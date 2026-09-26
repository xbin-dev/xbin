// hack/agent-template-features.test.mjs — the agent template's feature parity
// contract (builtin-templates/agent/model/features.js): every feature key is
// implemented by each view (web-features.js for the web; a native view
// declares its own), or listed as an intended difference with its reason.
// Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { AREAS, FEATURES, DIFFERENCES, gaps } from '../builtin-templates/agent/model/features.js';
import { IMPLEMENTS as WEB } from '../builtin-templates/agent/web-features.js';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;

// The views held to the registry. A native view joins here with its own
// IMPLEMENTS (and its DIFFERENCES.native entries).
const VIEWS = { web: WEB };

test('feature keys are <area>.<feature>[.<detail>] in a known area, each described', () => {
  for (const [k, what] of Object.entries(FEATURES)) {
    assert.match(k, /^[a-z]+(\.[a-zA-Z]+){1,2}$/, `malformed key ${k}`);
    assert.ok(k.split('.')[0] in AREAS, `${k}: unknown area`);
    assert.ok(typeof what === 'string' && what.length > 3, `${k}: describe it`);
  }
  for (const area of Object.keys(AREAS)) {
    assert.ok(Object.keys(FEATURES).some((k) => k.startsWith(area + '.')), `area ${area} has no features`);
  }
});

for (const [view, implemented] of Object.entries(VIEWS)) {
  test(`the ${view} view implements every feature, or says why not`, () => {
    const g = gaps(view, implemented);
    assert.deepEqual(g.missing, [], `${view} misses features (implement them, or list them in DIFFERENCES.${view} with the reason)`);
    assert.deepEqual(g.unknown, [], `${view} implements keys model/features.js does not have`);
    assert.deepEqual(g.stale, [], `DIFFERENCES.${view} lists keys that are implemented or gone`);
  });
}

test('every intended difference says why', () => {
  for (const [view, diffs] of Object.entries(DIFFERENCES)) {
    for (const [k, why] of Object.entries(diffs)) {
      assert.ok(k in FEATURES, `DIFFERENCES.${view}: unknown key ${k}`);
      assert.ok(typeof why === 'string' && why.length > 10, `DIFFERENCES.${view}.${k}: say why`);
    }
  }
});

test('where the web view says a feature lives exists', () => {
  for (const [k, where] of Object.entries(WEB)) {
    const files = String(where).match(/[\w/-]+\.(?:js|html)\b/g) || [];
    assert.ok(files.length, `${k}: name the file that implements it`);
    for (const f of files) assert.ok(existsSync(TPL + f), `${k}: ${f} does not exist in the template`);
  }
});

test('gaps() reports what is missing, unknown and stale', () => {
  const keys = Object.keys(FEATURES).filter((k) => !(k in DIFFERENCES.web));
  const some = Object.fromEntries(keys.slice(1).map((k) => [k, 'x.js']));
  some['nope.key'] = 'x.js';
  const g = gaps('web', some);
  assert.deepEqual(g.missing, [keys[0]]);
  assert.deepEqual(g.unknown, ['nope.key']);
  assert.deepEqual(g.stale, []);
  const d0 = Object.keys(DIFFERENCES.web)[0];
  assert.deepEqual(gaps('web', { ...some, [d0]: 'x.js' }).stale, [d0]);
});
