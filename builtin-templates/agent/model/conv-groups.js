// model/conv-groups.js — pure helpers for the conversation list: the date group a
// conversation falls in (by LOCAL calendar day, like a chat app's sidebar)
// and keeping rows ordered by their last activity. Node-tested
// (hack/agent-template-conv.test.mjs).

export const GROUPS = ['Today', 'Yesterday', 'Previous 7 days', 'Previous 30 days', 'Older'];

const dayStart = (ms) => { const d = new Date(ms); d.setHours(0, 0, 0, 0); return d.getTime(); };

// groupOf names the group of a conversation last active at ms.
export function groupOf(ms, now = Date.now()) {
  // round(): a day across a DST change is 23 or 25 hours
  const days = Math.round((dayStart(now) - dayStart(ms || 0)) / 86400000);
  if (days <= 0) return GROUPS[0];
  if (days === 1) return GROUPS[1];
  if (days < 7) return GROUPS[2];
  if (days < 30) return GROUPS[3];
  return GROUPS[4];
}

// groupRows splits rows (already newest first) into their date groups, in
// order, skipping empty ones.
export function groupRows(rows, now = Date.now()) {
  const by = new Map(GROUPS.map((g) => [g, []]));
  for (const r of rows) by.get(groupOf(r.activityMs, now)).push(r);
  return GROUPS.filter((g) => by.get(g).length).map((label) => ({ label, rows: by.get(label) }));
}

// byActivity orders rows newest activity first, then newest id.
export const byActivity = (a, b) => (b.activityMs || 0) - (a.activityMs || 0) || b.id - a.id;

// unread: activity after the person last looked (and after the upgrade that
// introduced read state — epochMs — so old conversations don't all light up).
export const isUnread = (r, epochMs = 0) => (r.activityMs || 0) > Math.max(r.readMs || 0, epochMs || 0);
