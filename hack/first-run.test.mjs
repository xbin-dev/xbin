// hack/first-run.test.mjs — the first-run sheet's words and rules
// (web/first-run-words.js, D236), run by `make js-test`: the sign-in name a
// person's name gives, what the sheet asks in each state, what its button
// sends, the checks before anything is sent, and where a refusal is shown;
// and the host's locks as the agent chrome's setup draws them.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as F from '../web/first-run-words.js';
import * as W from '../web/agent-chrome-words.js';

test('a name gives a sign-in name: its first word, lowercase, without accents', () => {
  assert.equal(F.signInName('Ada Lovelace'), 'ada');
  assert.equal(F.signInName('  Łukasz Nowak '), 'lukasz');
  assert.equal(F.signInName('José-María'), 'jose');
  assert.equal(F.signInName('Søren Æbelø'), 'soren');
  assert.equal(F.signInName('Owner'), 'owner1', 'owner is the token\'s home: never a person\'s');
  assert.equal(F.signInName('Owner Smith'), 'owner.smith');
  assert.equal(F.signInName('李小龍'), '', 'nothing a sign-in name holds: the sheet asks for one');
  assert.equal(F.signInName('x'.repeat(40)).length, 32);
  assert.equal(F.signInName(''), '');
});

const llm = { configured: true, host: true };
const never = { enabled: false, at: '0001-01-01T00:00:00Z', agent: 'xbin/wsagent', who: { everyone: false, entries: 0, chosen: false } };

test('the bootstrap install: two vault fields, and the button that turns the agent on for me', () => {
  const p = F.plan({ vault: { mode: 'unconfigured' }, llm, ws: never });
  assert.equal(p.vault, 'new');
  assert.deepEqual(p.agent, { kind: 'offer', body: { enabled: true, who: 'me' } });
  assert.equal(p.primary, 'Create my account and turn on my workspace agent');
  assert.equal(p.quiet, 'Create my account without it');
  assert.equal(p.line, '');
  assert.ok(p.says);
});

test('the host unseals: no vault field; a locked vault: one, to unlock it', () => {
  assert.equal(F.plan({ vault: { mode: 'unsealed' }, llm, ws: never }).vault, '');
  assert.equal(F.plan({ vault: { mode: 'plaintext' }, llm, ws: never }).vault, '');
  assert.equal(F.plan({ vault: null, llm, ws: never }).vault, '', 'unread: nothing asked');
  assert.equal(F.plan({ vault: { mode: 'sealed' }, llm, ws: never }).vault, 'unlock');
});

test('no model: the agent needs one, and the button only makes the account', () => {
  for (const l of [null, { configured: false }]) {
    const p = F.plan({ vault: { mode: 'unsealed' }, llm: l, ws: never });
    assert.equal(p.agent.kind, 'no-model');
    assert.equal(p.primary, 'Create my account');
    assert.equal(p.quiet, '');
    assert.match(p.line, /\/xbin\/llm/);
  }
});

test('the agent is never turned on where it was switched before, "everyone" never chosen', () => {
  const set = (x) => F.plan({ vault: null, llm, ws: { ...never, at: '2026-11-16T10:00:00Z', ...x } }).agent;
  assert.equal(set({ enabled: false }).kind, 'set', 'an admin turned it off: left off');
  assert.equal(set({ enabled: true, who: { everyone: true, entries: 0, chosen: true } }).kind, 'set');
  assert.deepEqual(set({ enabled: true, who: { everyone: false, entries: 0, chosen: false } }), { kind: 'offer', body: { who: 'me' } }, 'on, nobody chosen: me');
  for (const ws of [never, { ...never, enabled: true, at: '2026-11-16T10:00:00Z' }]) {
    const a = F.plan({ vault: null, llm, ws }).agent;
    assert.notEqual(a.body && a.body.who, 'everyone');
  }
  assert.equal(F.plan({ vault: null, llm, ws: { enabled: false } }).agent.kind, 'none', 'an xbind that ships no agent says nothing of one');
  assert.equal(F.plan({ vault: null, llm, ws: null }).agent.kind, 'none');
  assert.ok(!F.plan({ vault: null, llm, ws: null }).says);
});

test('the host\'s preset: on for admins needs no click; on without who sends who: me; off says so', () => {
  const lock = { locked: true, reason: 'set by your host' };
  const on = (who) => ({ ...never, enabled: true, by: 'host', at: '2026-11-16T10:00:00Z', locks: who ? { enabled: lock, who: lock } : { enabled: lock } });
  assert.equal(F.plan({ vault: null, llm, ws: on(true) }).agent.kind, 'host-on');
  assert.equal(F.plan({ vault: null, llm, ws: on(true) }).primary, 'Create my account');
  assert.deepEqual(F.plan({ vault: null, llm, ws: on(false) }).agent, { kind: 'offer', body: { who: 'me' } });
  const off = F.plan({ vault: null, llm, ws: { ...never, by: 'host', at: '2026-11-16T10:00:00Z', locks: { enabled: lock } } });
  assert.equal(off.agent.kind, 'host-off');
  assert.equal(off.primary, 'Create my account');
  assert.match(off.line, /host/);
});

test('the checks before anything is sent: the same password rule as POST /users, the repeat, a sign-in name', () => {
  const p = F.plan({ vault: { mode: 'unconfigured' }, llm, ws: never });
  const ok = { name: 'Ada Admin', id: 'ada', password: 'twelve chars', vault: 'v', repeat: 'v' };
  assert.deepEqual(F.check(ok, p), {});
  assert.deepEqual(Object.keys(F.check({ ...ok, password: 'short' }, p)), ['password']);
  assert.deepEqual(Object.keys(F.check({ ...ok, password: '1234567' }, p)), ['password']);
  assert.deepEqual(F.check({ ...ok, password: 'ąęółśżźć' }, p), {}, 'eight characters, counted as characters');
  assert.deepEqual(Object.keys(F.check({ ...ok, repeat: 'w' }, p)), ['repeat']);
  assert.deepEqual(Object.keys(F.check({ ...ok, vault: '', repeat: '' }, p)), ['vault']);
  assert.deepEqual(Object.keys(F.check({ ...ok, name: ' ', id: '' }, p)), ['name'], 'no name: that is the one thing said about it');
  assert.deepEqual(Object.keys(F.check({ ...ok, id: '' }, p)), ['id'], 'a name with no sign-in name in it');
  assert.equal(F.check({ ...ok, id: 'owner' }, p).id, F.WORDS.errors.reserved);
  assert.deepEqual(Object.keys(F.check({ ...ok, id: '-ada' }, p)), ['id']);
  assert.deepEqual(F.check({ ...ok, vault: '', repeat: '' }, F.plan({ vault: { mode: 'unsealed' } })), {}, 'no vault fields: nothing to check');
  // one field at a time (Enter in it)
  assert.deepEqual(F.check({ ...ok, password: 'x' }, p, 'name'), {});
  assert.deepEqual(Object.keys(F.check({ ...ok, password: 'x' }, p, 'password')), ['password']);
  assert.deepEqual(Object.keys(F.check({ ...ok, repeat: 'w' }, p, 'repeat')), ['repeat']);
  assert.deepEqual(Object.keys(F.check({ ...ok, vault: '' }, F.plan({ vault: { mode: 'sealed' } }), 'vault')), ['vault']);
});

test('a refusal is shown next to its field', () => {
  assert.equal(F.fieldOf('account', 400, 'password too short (min 8 characters)'), 'password');
  assert.equal(F.fieldOf('account', 409, 'user already exists'), 'id');
  assert.equal(F.fieldOf('account', 400, 'invalid id "-x": 1–32 chars of a–z, …'), 'id');
  assert.equal(F.fieldOf('account', 400, 'id "owner" is reserved'), 'id');
  assert.equal(F.fieldOf('account', 0, "xbin can't be reached"), 'form');
  assert.equal(F.fieldOf('vault', 403, 'bad passphrase'), 'vault');
});

test('the host\'s locks in the setup steps: "Set by your host", no buttons; off says the host did it', () => {
  const lock = { locked: true, reason: 'set by your host' };
  const steps = W.setupSteps({ llm: { configured: true, host: true }, state: { workspace: true, locks: { enabled: lock, who: lock }, who: { everyone: false, entries: 2, chosen: true } } });
  assert.equal(steps[1].tag, 'Set by your host');
  assert.equal(steps[1].action, '');
  assert.equal(steps[2].tag, 'Set by your host');
  assert.deepEqual(steps[2].choices, []);
  const notChosen = W.setupSteps({ llm: { configured: true }, state: { workspace: true, locks: { enabled: lock, who: lock }, who: { chosen: false } } });
  assert.deepEqual(notChosen[2].choices, [], 'who is the host\'s: no choice offered');
  const open = W.setupSteps({ llm: { configured: true }, state: { workspace: false } });
  assert.equal(open[1].action, 'Turn on', 'no lock: as before');
  assert.equal(open[1].tag, '');
  assert.equal(W.stateWords('off', { host: true }).title, 'Turned off by your host.');
  assert.equal(W.stateWords('off', {}).title, 'Turned off for this workspace.');
});
