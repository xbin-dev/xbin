// hack/logs-partition.test.mjs — unit tests for the log view's partition
// switcher (web/logs-partition.js, owner answer I5), run by `make js-test`:
// the query each choice adds to GET /logs, the echo rule, the switcher's
// entries from GET /partitions?tile= and what the default answered (the
// global instance's only for who may read it, shares for admins and
// managers, never an orphan's), and the corner badge. An unpartitioned tile, and an older xbind (no listing, no
// header), get no entries and the badge they always had.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { logsQuery, echoOK, defaultLabel, logChoices, badgeText, globalProbe } from '../web/logs-partition.js';

test('each choice asks GET /logs for its log; the default asks for nothing', () => {
  assert.equal(logsQuery(''), '');
  assert.equal(logsQuery(undefined), '');
  assert.equal(logsQuery('global'), '&xbin-partition=global');
  assert.equal(logsQuery('user:alice'), '&user=alice');
  assert.equal(logsQuery('user:a b&c'), '&user=a%20b%26c');
  assert.equal(logsQuery('user:'), '', 'a partition key without a person asks for nothing');
  assert.equal(logsQuery('partition=user:bob'), '', 'never a ?partition= (xbind refuses it)');
});

test('the echo rule: a named choice must come back named', () => {
  assert.ok(echoOK('', null), 'the default takes whatever the credential reaches');
  assert.ok(echoOK('', 'user:alice'));
  assert.ok(echoOK('global', 'global'));
  assert.ok(echoOK('user:bob', 'user:bob'));
  assert.ok(!echoOK('global', null), 'an xbind that ignored the parameter answered another log');
  assert.ok(!echoOK('user:bob', 'user:alice'));
});

test('nothing to switch where the tile is not partitioned, or the xbind is older', () => {
  assert.deepEqual(logChoices(null, ''), []);
  assert.deepEqual(logChoices(undefined), []);
  assert.deepEqual(logChoices({ state: 'unpartitioned', spec: { user: false, global: false } }, ''), []);
  assert.deepEqual(logChoices({ state: 'unpartitioned', spec: null }, ''), []);
  assert.equal(badgeText([]), 'read-only logs', 'the badge an unpartitioned tile always had');
  assert.equal(badgeText([], 'dev'), 'read-only logs · dev');
});

test('a person: their own partition, and the global instance when the tile runs one', () => {
  const listing = { state: 'partitioned', spec: { user: true, global: true },
    partitions: [{ user: 'alice', partition: 'user:alice', logShare: { until: '2026-10-07T00:00:00Z' } }] };
  const c = logChoices(listing, 'user:alice');
  assert.deepEqual(c.map((x) => x.value), ['', 'global'], 'her own share is not a second entry');
  assert.equal(c[0].label, 'yours');
  assert.equal(badgeText(c), 'read-only logs', 'the switcher beside the badge names the log');
  const userOnly = logChoices({ state: 'partitioned', spec: { user: true, global: false }, partitions: [] }, 'user:alice');
  assert.deepEqual(userOnly.map((x) => x.value), [''], 'a tile without a global instance: nothing else to pick');
  assert.equal(badgeText(userOnly), 'read-only logs · your partition', 'a lone choice is named in the badge');
  // the default refused (she never had a partition, so no row of her own
  // either): the listing still says partitioned, and global's is offered
  assert.deepEqual(logChoices({ ...listing, partitions: [] }, '').map((x) => x.value), ['', 'global']);
});

test('an admin: each person who shares their log now, besides their own and global', () => {
  const listing = { state: 'partitioned', spec: { user: true, global: true }, partitions: [
    { user: 'alice', partition: 'user:alice', logShare: { until: '2026-10-07T12:00:00Z' } },
    { user: 'bob', partition: 'user:bob' },
    { user: 'root2', partition: 'user:root2', logShare: { until: '2026-10-02T00:00:00Z' } },
    { user: 'zed', partition: 'user:zed', logShare: {} },
    null,
  ] };
  const c = logChoices(listing, 'user:root2');
  assert.deepEqual(c.map((x) => x.value), ['', 'global', 'user:alice']);
  assert.equal(c[2].label, "alice's log (shared with you)");
  assert.match(c[2].title, /until 2026-10-07/);
  // the root token reaches global by default: no second global entry
  const root = logChoices(listing, 'global');
  assert.equal(defaultLabel('global'), 'global');
  assert.deepEqual(root.map((x) => x.value), ['', 'user:alice', 'user:root2']);
  assert.equal(root[0].label, 'global');
  assert.equal(badgeText(logChoices({ spec: { user: true, global: true }, partitions: [] }, 'global')), 'read-only logs · global',
    'the root token on a tile nobody shares with: global alone, named');
  // an orphan's share (its person deleted, the record not swept yet): never offered
  const orphan = { ...listing, orphans: [], partitions: [...listing.partitions, { user: 'porphan', partition: 'user:porphan', state: 'orphaned', logShare: { until: '2026-10-07T00:00:00Z' } }] };
  assert.deepEqual(logChoices(orphan, 'user:root2').map((x) => x.value), ['', 'global', 'user:alice']);
});

test('the global instance\'s log is offered only to who may read it', () => {
  const person = { state: 'partitioned', spec: { user: true, global: true }, partitions: [] };
  assert.ok(globalProbe(person, 'user:wendy'), "a person's listing doesn't say: the view asks xbind");
  assert.ok(globalProbe(person, ''), 'a refused default too');
  assert.ok(!globalProbe({ ...person, orphans: [] }, 'user:admin'), "an admin's listing (orphans): an admin reads it");
  assert.ok(!globalProbe(person, 'global'), 'the root token: it is the default');
  assert.ok(!globalProbe({ ...person, spec: { user: true, global: false } }, 'user:wendy'), 'no global instance: nothing to ask');
  assert.ok(!globalProbe(null, ''));
  assert.deepEqual(logChoices(person, 'user:wendy', { globalOK: false }).map((x) => x.value), [''], 'refused (403): not offered');
  assert.deepEqual(logChoices(person, 'user:wendy', { globalOK: true }).map((x) => x.value), ['', 'global']);
});

test('a tile manager: the people who share their log with them (logShares)', () => {
  const listing = { state: 'partitioned', spec: { user: true, global: false }, partitions: [{ user: 'mona', partition: 'user:mona' }],
    logShares: [{ user: 'alice', until: '2026-10-07T12:00:00Z' }, { user: 'mona', until: '2026-10-03T00:00:00Z' }, { user: 'x' }, null] };
  const c = logChoices(listing, 'user:mona');
  assert.deepEqual(c.map((x) => x.value), ['', 'user:alice'], 'their own share and a share without an end are no entries');
  assert.equal(c[1].label, "alice's log (shared with you)");
  // an admin's rows and logShares naming the same person: one entry
  const both = { ...listing, partitions: [{ user: 'alice', partition: 'user:alice', logShare: { until: '2026-10-07T12:00:00Z' } }] };
  assert.deepEqual(logChoices(both, 'user:mona').map((x) => x.value), ['', 'user:alice']);
});
