// hack/agent-template-conv.test.mjs — the agent template's conversation list
// helpers (builtin-templates/agent/model/conv-groups.js): date groups by LOCAL
// calendar day, ordering, unread.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupOf, groupRows, byActivity, isUnread, GROUPS } from '../builtin-templates/agent/model/conv-groups.js';

const at = (y, m, d, h = 12, min = 0) => new Date(y, m - 1, d, h, min).getTime();

test('groups follow calendar days, not 24-hour windows', () => {
  const now = at(2026, 9, 26, 0, 30); // just after midnight
  assert.equal(groupOf(at(2026, 9, 26, 0, 5), now), 'Today');
  assert.equal(groupOf(at(2026, 9, 25, 23, 59), now), 'Yesterday'); // 31 minutes ago
  assert.equal(groupOf(at(2026, 9, 24, 12), now), 'Previous 7 days');
  assert.equal(groupOf(at(2026, 9, 20, 12), now), 'Previous 7 days');
  assert.equal(groupOf(at(2026, 9, 19, 12), now), 'Previous 30 days');
  assert.equal(groupOf(at(2026, 8, 28, 12), now), 'Previous 30 days');
  assert.equal(groupOf(at(2026, 8, 27, 12), now), 'Older');
  assert.equal(groupOf(0, now), 'Older');
  assert.equal(groupOf(at(2026, 9, 27, 9), now), 'Today', 'a clock skewed into tomorrow is still today');
});

test('a DST change does not shift a day', () => {
  // whatever the local zone, a day is still a day on both sides of a change
  const now = at(2026, 3, 30, 12);
  assert.equal(groupOf(at(2026, 3, 29, 12), now), 'Yesterday');
  assert.equal(groupOf(at(2026, 10, 25, 12), at(2026, 10, 26, 12)), 'Yesterday');
});

test('rows split into their groups in order, empty groups skipped', () => {
  const now = at(2026, 9, 26, 15);
  const rows = [
    { id: 3, activityMs: at(2026, 9, 26, 14) },
    { id: 2, activityMs: at(2026, 9, 26, 9) },
    { id: 1, activityMs: at(2026, 7, 1) },
  ];
  const g = groupRows(rows, now);
  assert.deepEqual(g.map((x) => x.label), [GROUPS[0], GROUPS[4]]);
  assert.deepEqual(g[0].rows.map((r) => r.id), [3, 2]);
});

test('newest activity first, then newest id', () => {
  const rows = [{ id: 1, activityMs: 5 }, { id: 3, activityMs: 5 }, { id: 2, activityMs: 9 }];
  assert.deepEqual(rows.sort(byActivity).map((r) => r.id), [2, 3, 1]);
});

test('unread is activity after you looked, floored at the upgrade', () => {
  assert.equal(isUnread({ activityMs: 10, readMs: 5 }), true);
  assert.equal(isUnread({ activityMs: 10, readMs: 10 }), false);
  assert.equal(isUnread({ activityMs: 10, readMs: 0 }, 20), false, 'older than the upgrade: not unread');
  assert.equal(isUnread({ activityMs: 30, readMs: 0 }, 20), true);
});
