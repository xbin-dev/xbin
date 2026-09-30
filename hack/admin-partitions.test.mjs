// hack/admin-partitions.test.mjs — unit tests for the words and request
// bodies of the admin console's runtime → partitions view
// (workspace-template/tiles/admin/tabs/partitions-view.js), run by `make
// js-test`: modes and states, a request in words, what a switch deletes (in
// step with xbind's registry.SwitchDeletes and the shell's
// partition-mode.js), the mode act's body, a dry run's confirmation text,
// the typed reset confirmation, the rows' metadata in words (never more than
// the counts), the history, the limits' body, an overview row's flags, the
// purge's bodies (exactly the confirmed orphans) and the on-demand untracked
// check merged into the plain polls.
// A field an older xbind doesn't send is left out, never read as zero.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  modeName, specOf, stateWords, requestText, switchDeletes, deletesNothing, modeBody, wipedText, switchLines,
  resetConfirm, partitionOp, regsText, mailText, runningText, historyText, limitsBody, tileNotes, bytesText, shortId,
  orphanWhy, purgeBodies, untrackedOf, withUntracked,
} from '../workspace-template/tiles/admin/tabs/partitions-view.js';
import { switchDeletes as shellSwitchDeletes } from '../workspace-template/shell/partition-mode.js';

test('modes and states in words', () => {
  assert.equal(modeName({ user: true, global: true }), 'user + global');
  assert.equal(modeName({ user: true }), 'user');
  assert.equal(modeName({ global: true }), 'global');
  assert.equal(modeName(null), 'unpartitioned');
  assert.equal(specOf(null), null);
  assert.equal(specOf({ user: false, global: false }), null);
  assert.deepEqual(specOf({ user: 1 }), { user: true, global: false });
  assert.equal(stateWords('pending'), 'waiting for a manager');
  assert.equal(stateWords('partitioned'), 'partitioned');
  assert.equal(stateWords('something-new'), 'something-new', 'a state a newer xbind adds shows as it is');
});

test('a request in words: open, declined, none', () => {
  const open = { spec: null, request: { spec: { user: true, global: false }, since: '2026-09-30T10:00:00Z', declined: false } };
  assert.match(requestText('apps/docs', open), /^apps\/docs's code asks for unpartitioned → user since 2026-09-30 10:00; it runs nothing until a manager/);
  const declined = { spec: { user: true }, request: { spec: null, declined: true } };
  assert.match(requestText('apps/docs', declined), /declined: apps\/docs runs user, nothing was deleted/);
  assert.equal(requestText('apps/docs', { spec: { user: true }, request: null }), '');
  assert.equal(requestText('apps/docs', { spec: { user: true } }), '', 'an older xbind: no request field');
});

test('what a switch deletes, as xbind and the shell say it (H1)', () => {
  const cases = [[null, { user: true }], [{ user: true }, null], [{ user: true, global: true }, { user: true }], [{ user: true }, { user: true, global: true }]];
  for (const [from, to] of cases) assert.equal(switchDeletes(from, to), shellSwitchDeletes(from, to));
  assert.ok(deletesNothing({ user: true }, { user: true, global: true }));
  assert.ok(!deletesNothing(null, { user: true }));
});

test('the mode act names the request the view showed', () => {
  const x = { spec: null, request: { spec: { user: true, global: true } } };
  assert.deepEqual(modeBody('apps/docs', x, 'keep'), { tile: 'apps/docs', act: 'keep', from: null, to: { user: true, global: true } });
  assert.deepEqual(modeBody('apps/docs', x, 'switch', { confirm: 'apps/docs' }),
    { tile: 'apps/docs', act: 'switch', from: null, to: { user: true, global: true }, confirm: 'apps/docs' });
});

test('a dry run in words: counts, keeps, sandbox managers', () => {
  const x = { spec: null, request: { spec: { user: true } } };
  const lines = switchLines('apps/docs', x, {
    deletes: 'all data in this tile', wiped: { namespaces: 2, partitions: 0, vaultKeys: 1, registrations: 3, bytes: 2048, subkeys: 2 },
    keeps: ['the tile\'s source and its backups of it'], people: 0, managers: ['apps/mgr'],
  });
  assert.equal(lines[0], 'Switching apps/docs from unpartitioned to user deletes all data in this tile.');
  assert.equal(lines[1], 'It deletes: 2 data namespaces, 0 people\'s partitions, 1 vault key, 3 registrations, 2.0 KB; 2 backup keys erased.');
  assert.ok(lines.includes('Keeps: the tile\'s source and its backups of it'));
  assert.ok(lines.some((l) => l.includes('apps/mgr')));
  assert.equal(lines.at(-1), 'This can\'t be undone.');
  const add = switchLines('apps/docs', { spec: { user: true }, request: { spec: { user: true, global: true } } }, { deletes: 'nothing' });
  assert.deepEqual(add, ['Switching apps/docs from user to user + global deletes nothing.', 'Nothing is deleted.']);
  assert.equal(wipedText(), '0 data namespaces, 0 people\'s partitions, 0 vault keys, 0 registrations, 0 bytes');
});

test('a reset names the person\'s partition and needs it typed', () => {
  assert.equal(resetConfirm('apps/docs', 'alice'), 'apps/docs user:alice');
  assert.deepEqual(partitionOp('apps/docs', { user: 'alice', partition: 'user:alice' }, { confirm: 'apps/docs user:alice' }),
    { tile: 'apps/docs', partition: 'user:alice', confirm: 'apps/docs user:alice' });
  assert.deepEqual(partitionOp('apps/docs', { user: 'bob' }), { tile: 'apps/docs', partition: 'user:bob' });
});

test('a row\'s metadata in words: counts only, absent fields left out', () => {
  assert.equal(regsText({ cronJobs: 2, busSubscriptions: 1, ifaceInstances: 0, ingressHosts: 0, missedTicks: 0, dormantDrops: 3, vaultKeys: 4 }),
    '2 cron jobs · 1 bus subscription · 4 vault keys · 3 dropped deliveries');
  assert.equal(regsText(undefined), '');
  assert.equal(mailText({ pending: 2, bytes: 3000, expired: 1 }), '2 waiting (2.9 KB) · 1 expired');
  assert.equal(mailText({ pending: 0, bytes: 0, expired: 0, undeliverable: 2 }), '0 waiting · 2 undeliverable');
  assert.equal(mailText(undefined), '', 'an inbox that never held an item: no mail field');
  assert.equal(runningText({ running: false }), 'stopped');
  assert.equal(runningText({ running: false, crashLoop: true }), 'stopped (crash loop)');
  assert.equal(runningText({ running: true, instance: { state: 'running', uptimeSec: 3700, rssKb: 2048, restarts: 1, errorClass: 'exit' } }),
    'running · 1h1m · 2.0 MB · 1 restart · error: exit');
  assert.equal(bytesText(0), '0 bytes');
  assert.equal(shortId('u-0123456789abcdef0123456789abcdef'), 'u-01234567…');
});

test('the mode history in words', () => {
  assert.equal(historyText({ op: 'switch', from: null, to: { user: true }, by: 'root2', wiped: { namespaces: 1, partitions: 0, vaultKeys: 0, registrations: 0, bytes: 10 } }),
    'switched unpartitioned → user · by root2 · deleted 1 data namespace, 0 people\'s partitions, 0 vault keys, 0 registrations, 10 bytes');
  assert.equal(historyText({ op: 'keep', from: { user: true }, to: null, by: 'mona' }), 'kept user, declining unpartitioned · by mona');
  assert.equal(historyText({ op: 'backup-erase', wiped: { subkeys: 1 }, partition: 'u-0123456789abcdef0123456789abcdef', reason: 'partition reset: apps/pg user:alice', by: 'bob' }),
    'backup keys erased · by bob · 1 key · partition u-01234567… · partition reset: apps/pg user:alice');
  assert.equal(historyText({ op: 'auto', from: null, to: { user: true } }), 'recorded user (the tile held no data)');
  assert.equal(historyText(null), '');
});

test('the limits\' body: empty leaves a value, 0 clears it', () => {
  assert.deepEqual(limitsBody('apps/pg', { maxRunning: '', partitionMiB: '' }), { tile: 'apps/pg' });
  assert.deepEqual(limitsBody('apps/pg', { maxRunning: '3', partitionMiB: '' }), { tile: 'apps/pg', maxRunning: 3 });
  assert.deepEqual(limitsBody('apps/pg', { maxRunning: '0', partitionMiB: '2' }), { tile: 'apps/pg', maxRunning: 0, partitionBytes: 2 * 1024 * 1024 });
  assert.deepEqual(limitsBody('apps/pg', { maxRunning: 'x' }), { tile: 'apps/pg' });
});

test('an overview row\'s flags', () => {
  assert.deepEqual(tileNotes({ tile: 'apps/pg', state: 'partitioned' }), [], 'an older xbind\'s row: nothing flagged');
  const notes = tileNotes({
    reviewedOnly: { on: true }, trust: ['live reload is on while wendy can change the code'],
    capsHit: { at: '2026-09-30T08:00:00Z', kind: 'evicted', count: 3 }, globalBinds: { llm: ['apps/llm-gw'] },
    boundWithoutGlobal: ['apps/x'], untrackedCount: 2,
  });
  assert.deepEqual(notes.map((n) => n.kind), ['ok', 'warn', 'warn', 'info', 'warn', 'warn']);
  assert.equal(notes[3].text, 'global binds: llm → apps/llm-gw');
  assert.match(notes[2].text, /^caps hit: evicted ×3/);
});

test('a purge deletes exactly the orphans confirmed', () => {
  const rows = [
    { tile: 'apps/pg', user: 'zed', partition: 'u-zed1', deployment: 'main', reason: 'user-deleted' },
    { tile: 'apps/pg', user: 'zed', partition: 'u-zed1', deployment: 'beta', reason: 'user-deleted' },
    { tile: 'apps/gone', user: 'amy', partition: 'u-amy1', reason: 'tile-removed' },
    { tile: 'apps/x' }, null,
  ];
  assert.deepEqual(purgeBodies(rows), [{ tile: 'apps/pg', partition: 'u-zed1' }, { tile: 'apps/gone', partition: 'u-amy1' }],
    'one body per listed tile and partition, never an empty one (which purges every orphan there is)');
  assert.deepEqual(purgeBodies([]), []);
  assert.deepEqual(purgeBodies(undefined), []);
  assert.equal(orphanWhy('user-deleted'), 'the person was deleted');
  assert.equal(orphanWhy('tile-removed'), 'the tile was removed');
  assert.equal(orphanWhy('something-new'), 'something-new', 'a reason a newer xbind adds shows as it is');
});

test('the untracked check runs once per click; the polls keep its answer', () => {
  const checked = untrackedOf({ tiles: [
    { tile: 'apps/pg', state: 'partitioned', untracked: ['notes.txt'], untrackedCount: 1 },
    { tile: 'apps/pu', state: 'partitioned', untrackedCount: 0 },
    { tile: 'apps/bad', state: 'partitioned', untrackedError: 'listing failed' },
    { tile: 'apps/wait', state: 'pending' },
  ] });
  assert.deepEqual(Object.keys(checked).sort(), ['apps/bad', 'apps/pg', 'apps/pu']);
  const poll = [{ tile: 'apps/pg', state: 'partitioned', totals: { people: 2 } }, { tile: 'apps/wait', state: 'pending' }, { tile: 'apps/new', state: 'partitioned' }];
  const rows = withUntracked(poll, checked);
  assert.deepEqual(rows[0], { tile: 'apps/pg', state: 'partitioned', totals: { people: 2 }, untracked: ['notes.txt'], untrackedCount: 1 });
  assert.equal(rows[1], poll[1]);
  assert.equal(rows[2], poll[2], 'a tile the check didn\'t see has nothing merged');
  assert.equal(withUntracked(poll, null), poll, 'never checked: the poll as it is');
  assert.deepEqual(untrackedOf({}), {});
});
