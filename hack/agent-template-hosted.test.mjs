// hack/agent-template-hosted.test.mjs — the agent template's non-secure
// (hosted) conversations as a page sees them (builtin-templates/agent/
// model/hosted.js; API.md "Non-secure conversations"): hosted ids sit in
// [2^39, 2^40) — the shared space's range, so the page reaches them at the
// global instance; the warning names whose private resources it uses and who
// can read it (its people, then the managers, admins and code writers
// always); the composer is locked until the warning is accepted in this page
// session, while a wider audience waits for its host, and once hosting
// ended. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';

const MODEL = new URL('../builtin-templates/agent/model/', import.meta.url);
const hosted = await import(new URL('hosted.js', MODEL).href);

test('hosted ids are the shared space\'s', () => {
  const { hostedId, TEAM_BASE } = hosted;
  assert.equal(TEAM_BASE, 2 ** 39);
  assert.equal(hostedId(TEAM_BASE), true);
  assert.equal(hostedId(String(TEAM_BASE + 5)), true);
  assert.equal(hostedId(TEAM_BASE - 1), false); // the global instance's own
  assert.equal(hostedId(2 ** 40), false); // a person's own
  assert.equal(hostedId(null), false);
});

test('the warning: whose resources, who can read it', () => {
  const { exposed, readers, audienceOf } = hosted;
  assert.equal(exposed({ host: 'alice', resources: ['sandboxes', 'tiles', 'vault'] }),
    "alice's private sandboxes, data in other tiles and vault");
  assert.equal(exposed({ host: 'alice', resources: ['sandboxes'] }), "alice's private sandboxes");
  const people = readers({ owner: 'alice', visibility: 'private', members: [{ user: 'bob', role: 'viewer' }] });
  assert.deepEqual(people, ['its members: alice, bob', "the agent's managers (everyone with write access to it)", 'workspace admins',
    "anyone who can change this agent's code"]);
  const team = readers({ owner: 'alice', visibility: 'team', members: {} });
  assert.equal(team[0], 'everyone who can open this agent');
  assert.deepEqual(audienceOf({ owner: 'alice', visibility: 'team', teamRole: 'participant', members: [{ user: 'bob', role: 'viewer' }] }),
    { owner: 'alice', visibility: 'team', teamRole: 'participant', members: { bob: 'viewer' } });
});

test('the composer\'s lock', () => {
  const { lockOf } = hosted;
  const view = (h) => ({ run: { id: 2 ** 39 + 1, rootId: 2 ** 39 + 1, hosted: h } });
  assert.equal(lockOf({ run: { id: 7 } }, 'bob', new Set()), null); // not hosted
  const active = view({ host: 'alice', state: 'active', resources: [] });
  assert.equal(lockOf(active, 'bob', new Set()).kind, 'start'); // opened without sending
  assert.equal(lockOf(active, 'bob', new Set()).locked, true);
  assert.equal(lockOf(active, 'bob', new Set([2 ** 39 + 1])).locked, false); // started in this page session
  const paused = view({ host: 'alice', state: 'paused', pending: ['carol'] });
  const p = lockOf(paused, 'bob', new Set([2 ** 39 + 1]));
  assert.equal(p.kind, 'paused');
  assert.equal(p.locked, true);
  assert.equal(p.isHost, false);
  assert.match(p.why, /waiting for alice/);
  assert.deepEqual(lockOf(paused, 'alice', new Set()).pending, ['carol']);
  assert.equal(lockOf(paused, 'alice', new Set()).isHost, true);
  for (const state of ['dropped', 'gone']) {
    const e = lockOf(view({ host: 'alice', state }), 'bob', new Set([2 ** 39 + 1]));
    assert.equal(e.kind, 'ended');
    assert.equal(e.locked, true);
  }
});

test('the composer\'s lock while it moves in or out of its host\'s hands', () => {
  const { lockOf } = hosted;
  const view = (h) => ({ run: { id: 2 ** 39 + 1, rootId: 2 ** 39 + 1, hosted: h } });
  const started = new Set([2 ** 39 + 1]);
  const p = lockOf(view({ host: 'alice', state: 'pending' }), 'bob', started);
  assert.equal(p.kind, 'moving');
  assert.equal(p.locked, true);
  assert.match(p.why, /waiting for alice's own space/);
  const c = lockOf(view({ host: 'alice', state: 'continuing' }), 'bob', started);
  assert.equal(c.kind, 'moving');
  assert.match(c.why, /continuing it without alice/);
});
