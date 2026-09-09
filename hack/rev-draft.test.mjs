// hack/rev-draft.test.mjs — unit tests for the shell's revisioned-draft
// helpers (workspace-template/shell/rev-draft.js), run by `make js-test`:
// how a PUT's answer is classified, what the conflict dialog says and
// offers, the draft-map helpers and the "ago" labels.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ago, newDraft, withDraft, withoutDraft, publish, conflictDialog } from '../workspace-template/shell/rev-draft.js';

const T = Date.parse('2026-09-09T12:00:00Z');

test('ago', () => {
  assert.equal(ago('', T), '');
  assert.equal(ago('nonsense', T), '');
  assert.equal(ago('2026-09-09T11:59:40Z', T), 'just now');
  assert.equal(ago('2026-09-09T11:57:00Z', T), '3 min ago');
  assert.equal(ago('2026-09-09T09:30:00Z', T), '2 h ago');
  assert.equal(ago('2026-09-05T12:00:00Z', T), '4 d ago');
  assert.equal(ago('2026-09-10T12:00:00Z', T), 'just now', 'a clock ahead of ours is not negative');
});

test('draft maps are immutable', () => {
  const d = newDraft({ tiles: [{ path: 'a' }], name: 'HQ' }, 3);
  assert.deepEqual(d, { tiles: [{ path: 'a' }], name: 'HQ', baseRev: 3, dirty: false });
  const m1 = withDraft(undefined, 'x', d);
  const m2 = withDraft(m1, 'y', d);
  assert.deepEqual(Object.keys(m2), ['x', 'y']);
  assert.deepEqual(Object.keys(m1), ['x'], 'withDraft did not mutate its input');
  const m3 = withoutDraft(m2, 'x');
  assert.deepEqual(Object.keys(m3), ['y']);
  assert.deepEqual(Object.keys(m2), ['x', 'y'], 'withoutDraft did not mutate its input');
  assert.deepEqual(withoutDraft(undefined, 'x'), {});
});

const reply = (status, body) => async (url, init) => ({ status, ok: status < 300, json: async () => body, _url: url, _init: init });

test('publish classifies the answer', async () => {
  let seen;
  const ok = await publish('/api/x', { rev: 2, tiles: [] }, async (url, init) => { seen = { url, init }; return reply(200, { rev: 3 })(url, init); });
  assert.deepEqual(ok, { status: 'ok', body: { rev: 3 } });
  assert.equal(seen.url, '/api/x');
  assert.equal(seen.init.method, 'PUT');
  assert.equal(seen.init.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(seen.init.body), { rev: 2, tiles: [] });
  const conflict = await publish('/api/x', {}, reply(409, { rev: 5, screen: { updatedBy: 'bob' } }));
  assert.equal(conflict.status, 'conflict');
  assert.equal(conflict.body.rev, 5);
  assert.equal(conflict.message, 'saved by someone else first');
  const refused = await publish('/api/x', {}, reply(403, { error: 'not an org admin' }));
  assert.deepEqual(refused, { status: 'error', message: 'not an org admin', body: { error: 'not an org admin' } });
  const bare = await publish('/api/x', {}, async () => ({ status: 500, ok: false, json: async () => { throw new Error('no json'); } }));
  assert.equal(bare.status, 'error');
  assert.equal(bare.message, 'save failed (500)');
  const offline = await publish('/api/x', {}, async () => { throw new TypeError('fetch failed'); });
  assert.deepEqual(offline, { status: 'offline', message: 'offline — try again' });
});

test('conflict dialog: draft vs replace', () => {
  const at = '2026-09-09T11:57:00Z';
  const d = conflictDialog({ what: '"Sales board"', rev: 4, by: 'bob', at, baseRev: 3, now: T });
  assert.equal(d.title, 'Someone saved this first');
  assert.equal(d.message, '"Sales board" is now at rev 4, saved by bob 3 min ago. Your draft is based on rev 3.');
  assert.deepEqual(d.buttons.map((b) => [b.label, b.value, !!b.danger]), [
    ['Keep editing', null, false], ['Reload theirs (drop my draft)', 'reload', false], ['Overwrite with mine', 'force', true]]);
  const unknown = conflictDialog({ what: 'the workspace sidebar folders', rev: 2, by: 'someone', at: undefined, baseRev: undefined, now: T });
  assert.equal(unknown.message, 'the workspace sidebar folders is now at rev 2, saved by someone . Your draft is based on rev ?.');
  const r = conflictDialog({ what: '"Sales board"', rev: 4, by: 'you', at, baseRev: 1, replace: true, now: T });
  assert.equal(r.message, '"Sales board" is now at rev 4, saved by you 3 min ago; you were replacing rev 1.');
  assert.deepEqual(r.buttons.map((b) => b.label), ['Cancel', 'Reload theirs', 'Replace anyway']);
});
