// hack/prompts.test.mjs — the decisions waiting on each tile (D195,
// workspace-template/shell/prompts.js), run by `make js-test`: which tile a
// grant request, an interface to bind and a consent ask belong to, what a
// dismissal and an unapprovable slot leave out, a folder's count, the words.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { promptsByTile, flaggedIn, promptWords, foldsToLine, lineWords } from '../workspace-template/shell/prompts.js';

test('each decision lands on the tile that asked', () => {
  const m = promptsByTile({
    grants: { pending: [{ from: 'apps/a', target: 'apps/b', role: 'reader' }, { from: 'apps/a', target: 'cap:open-links', role: 'use' }] },
    bindings: { pending: [{ component: 'apps/b', slot: 'net', kind: 'net' }, { component: 'apps/c', slot: 'llm', kind: 'http', approvable: false }] },
    consents: [{ from: 'apps/d', to: 'apps/a', key: 'apps/d→apps/a' }],
  });
  assert.deepEqual(Object.fromEntries(m), {
    'apps/a': { grants: 2, bindings: 0, consents: 0, n: 2 },
    'apps/b': { grants: 0, bindings: 1, consents: 0, n: 1 },
    'apps/d': { grants: 0, bindings: 0, consents: 1, n: 1 },
  }, 'a grant on its from, a slot on its component (not one the person may not wire), a consent on its from');
});

test('dismissed decisions and unread lists count nothing', () => {
  const g = { pending: [{ from: 'apps/a', target: 'apps/b', role: 'reader' }, { from: 'apps/a', target: 'apps/b', role: 'writer' }] };
  const m = promptsByTile({ grants: g }, (kind, p) => kind === 'grants' && p.role === 'writer');
  assert.equal(m.get('apps/a').n, 1);
  assert.equal(promptsByTile({}).size, 0);
  assert.equal(promptsByTile({ grants: null, bindings: undefined, consents: null }).size, 0);
  assert.equal(promptsByTile({ grants: { pending: [{ target: 'x' }, { from: '' }] } }).size, 0, 'a request without a tile: nowhere');
});

test('a folder counts its flagged tiles; the words name what waits', () => {
  const m = promptsByTile({ grants: { pending: [{ from: 'apps/a' }] }, bindings: { pending: [{ component: 'apps/a' }, { component: 'apps/b' }, { component: 'apps/b' }] } });
  assert.equal(flaggedIn(m, ['apps/a', 'apps/b', 'apps/z']), 2);
  assert.equal(flaggedIn(m, []), 0);
  assert.equal(flaggedIn(null, ['apps/a']), 0);
  assert.equal(promptWords(m.get('apps/a')), '1 grant request, 1 interface to bind waiting for your decision');
  assert.equal(promptWords(m.get('apps/b')), '2 interfaces to bind waiting for your decision');
  assert.equal(promptWords(null), '');
});

test('a tile that works as it is folds its optional slots into one line (G10)', () => {
  const m = promptsByTile({
    grants: { pending: [{ from: 'apps/assistant', target: 'cap:open-links', role: 'writer' }] },
    bindings: { pending: [
      { component: 'apps/assistant', slot: 'mcp', optional: true }, { component: 'apps/assistant', slot: 'net', optional: true },
      { component: 'apps/assistant', slot: 'scm', optional: true },
      { component: 'apps/fresh', slot: 'llm', required: true }, { component: 'apps/fresh', slot: 'mcp' },
      { component: 'apps/old', slot: 'net' },
    ] },
  });
  const a = m.get('apps/assistant');
  assert.equal(a.optional, 3);
  assert.equal(foldsToLine(a), true, 'its required slots are bound: the rest are choices');
  assert.equal(lineWords(a), '1 grant request · 3 optional interfaces');
  assert.equal(foldsToLine(m.get('apps/fresh')), false, 'a required slot unbound: the popover');
  assert.equal(foldsToLine(m.get('apps/old')), false, 'a tile that marks nothing required: as before');
  assert.equal(foldsToLine({ grants: 1, bindings: 0, consents: 0, n: 1 }), false, 'grant requests alone: as before');
  assert.equal(foldsToLine({ ...a, limits: 1 }), false, 'a limit ask keeps the popover');
  assert.equal(lineWords({ optional: 1 }), '1 optional interface');
});
