// hack/agent-template-native-hosted.test.mjs — a non-secure (hosted)
// conversation in the agent template's native view (native/hosted.js,
// model/hosted.js; API.md "Non-secure conversations"): its transcript opens
// with the warning (whose private resources, who can read it), the composer
// is locked until "Start anyway" in this app session, its host confirms a
// wider audience from the composer, and its list row says ⚠ not private.
// Run like agent-template-native.test.mjs, through hack/xbn/node.mjs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runNative } from './xbn/node.mjs';

const TPL = new URL('../builtin-templates/agent/', import.meta.url).pathname;
const NOW = Date.UTC(2026, 8, 30, 12);
const H = 2 ** 39 + 1;
const me = (user) => ({ kind: 'user', user, level: 'read', manager: false, halted: false, epochMs: 0, partition: 'user:' + user });

async function run(seed, steps, state) {
  const r = await runNative({ entry: TPL + 'native.js', data: { now: NOW, self: 'apps/agent', setup: TPL + 'test/native-stub.mjs', seed }, steps, state });
  assert.equal(r.fatal, null);
  assert.deepEqual(r.errors, [], 'no runtime errors');
  assert.deepEqual(r.diagnostics.filter((d) => d.level !== 'info'), [], 'no diagnostics');
  r.calls = (r.extra && r.extra.calls) || [];
  return r;
}
function all(root, m, out = []) {
  if (!root) return out;
  if ((!m.t || root.t === m.t) && Object.entries(m.p || {}).every(([k, v]) => JSON.stringify((root.p || {})[k]) === JSON.stringify(v))) out.push(root);
  for (const c of root.c || []) all(c, m, out);
  return out;
}
const find = (tree, m) => all(tree.root || tree, m)[0] || null;

const seedFor = (who, hosted) => ({
  me: me(who), partition: 'user:' + who,
  runs: [{ id: H, title: 'the hosted plan', status: 'idle', owner: 'bob', hosted }],
  views: { [H]: { run: { id: H, title: 'the hosted plan', status: 'idle', owner: 'bob', rootId: H, hosted }, access: 'participant',
    acl: { owner: 'bob', visibility: 'private', teamRole: 'viewer', members: [{ user: 'alice', role: 'participant' }] }, hosted } },
});

test('a hosted conversation: the warning heads it, the composer is locked until Start anyway', async () => {
  const hosted = { host: 'bob', state: 'active', resources: ['sandboxes', 'tiles', 'vault'] };
  const r = await run(seedFor('alice', hosted), [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Start anyway' } } },
    { snapshot: 'started' },
  ], { hash: 'c=' + H });
  const open = r.snapshots.open;
  const notice = find(open, { t: 'notice', p: { title: '⚠ Not private' } });
  assert.ok(notice, 'the warning heads the transcript');
  assert.match(notice.p.text, /bob's private sandboxes, data in other tiles and vault/);
  assert.match(notice.p.text, /its members: bob, alice/);
  assert.match(notice.p.text, /anyone who can change this agent's code/);
  const c = find(open, { t: 'composer' });
  assert.equal(c.p.disabled, true, 'locked until started');
  assert.match(c.p.placeholder, /without sending/);
  const c2 = find(r.snapshots.started, { t: 'composer' });
  assert.ok(!c2.p.disabled, 'Start anyway unlocks it');
  assert.equal(find(r.snapshots.started, { t: 'button', p: { label: 'Start anyway' } }), null);
});

test('paused for its host: locked for a member; its host confirms from the composer', async () => {
  const pendingKey = '{"members":{"alice":"participant","carol":"viewer"},"owner":"bob","teamRole":"viewer","visibility":"private"}';
  const hosted = { host: 'bob', state: 'paused', reason: 'confirm', pending: ['carol'], pendingKey, resources: ['sandboxes'] };
  const member = await run(seedFor('alice', hosted), [{ snapshot: 'open' }], { hash: 'c=' + H });
  const c = find(member.snapshots.open, { t: 'composer' });
  assert.equal(c.p.disabled, true);
  assert.match(c.p.placeholder, /waiting for bob to confirm who is in it now \(new: carol\)/);
  assert.equal(find(member.snapshots.open, { t: 'button', p: { label: 'Confirm carol' } }), null, 'a member isn\'t asked');
  const host = await run(seedFor('bob', hosted), [
    { tap: { t: 'button', p: { label: 'Confirm carol' } } },
    { snapshot: 'confirmed' },
  ], { hash: 'c=' + H });
  const post = host.calls.find((x) => x.method === 'POST' && /\/hosting\/\d+\/confirm$/.test(x.url));
  assert.ok(post, 'Confirm goes to the host\'s own partition');
  assert.equal(JSON.parse(post.body).seen, pendingKey);
});

test('the warning is a modal the first time: open without sending, read it again, start', async () => {
  const hosted = { host: 'bob', state: 'active', resources: ['sandboxes', 'tiles', 'vault'] };
  const r = await run(seedFor('alice', hosted), [
    { snapshot: 'open' },
    { tap: { t: 'button', p: { label: 'Open without sending' } } },
    { snapshot: 'closed' },
    { tap: { t: 'button', p: { label: 'Read the warning…' } } },
    { snapshot: 'again' },
    { tap: { t: 'button', p: { label: 'Start anyway' } } },
    { snapshot: 'started' },
  ], { hash: 'c=' + H });
  const sheetOf = (snap) => all(snap.root || snap, { t: 'sheet' }).find((s) => /is not private/.test(s.p.title || ''));
  const open = sheetOf(r.snapshots.open);
  assert.ok(open, 'the warning opens by itself');
  assert.ok(find(open, { t: 'row', p: { title: 'its members: bob, alice' } }), 'who can read it');
  assert.ok(find(open, { t: 'row', p: { title: "anyone who can change this agent's code" } }));
  assert.equal(sheetOf(r.snapshots.closed), undefined, 'Open without sending closes it');
  assert.equal(find(r.snapshots.closed, { t: 'composer' }).p.disabled, true, '…and leaves the composer locked');
  assert.ok(sheetOf(r.snapshots.again), 'Read the warning… opens it again');
  assert.equal(sheetOf(r.snapshots.started), undefined);
  assert.ok(!find(r.snapshots.started, { t: 'composer' }).p.disabled, 'Start anyway unlocks it');
});

test('a host is never asked to confirm what the page doesn\'t know', async () => {
  const hosted = { host: 'bob', state: 'paused', reason: 'confirm', pending: ['carol'], resources: ['sandboxes'] };
  const host = await run(seedFor('bob', hosted), [{ snapshot: 'open' }], { hash: 'c=' + H });
  assert.equal(find(host.snapshots.open, { t: 'button', p: { label: 'Confirm carol' } }), null);
  assert.ok(find(host.snapshots.open, { t: 'button', p: { label: 'Decline' } }));
});
