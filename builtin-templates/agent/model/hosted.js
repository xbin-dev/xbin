// model/hosted.js — non-secure (hosted) conversations as a page sees them
// (API.md "Non-secure conversations"): a shared conversation that uses one
// person's private resources lives in the agent's `team` database (ids from
// 2^39 — below 2^40, so the page reaches it at the global instance like any
// shared one) and its view says who hosts it and in what state. Pure
// functions, no lit, no DOM (hosted-ui.js draws them).

/** TEAM_BASE: where hosted conversations are numbered. */
export const TEAM_BASE = 2 ** 39;

/** hostedId(id): the id is a hosted conversation's. */
export function hostedId(id) {
  const n = Number(id);
  return Number.isInteger(n) && n >= TEAM_BASE && n < 2 ** 40;
}

/** hostingOf(v): a view's hosting ({host, state, reason, resources, pending, pendingKey, dropsAt}) or null. */
export function hostingOf(v) {
  return (v && v.run && v.run.hosted) || (v && v.hosted) || null;
}

const RESOURCE_WORDS = { sandboxes: 'sandboxes', tiles: 'data in other tiles', vault: 'vault' };

/** exposed(h): what a hosted conversation reaches, in words ("alice's private sandboxes, …"). */
export function exposed(h) {
  const list = ((h && h.resources) || []).map((r) => RESOURCE_WORDS[r] || r);
  const words = list.length > 1 ? `${list.slice(0, -1).join(', ')} and ${list.at(-1)}` : list[0] || 'resources';
  return `${(h && h.host) || 'its host'}'s private ${words}`;
}

/**
 * readers(acl): who can read a conversation, line by line — its people (the
 * owner and members, or everyone who can open the agent), then those who
 * always can: the agent's managers, workspace admins, anyone who can change
 * the agent's code. acl: {owner, visibility, teamRole, members: [{user, role}] | {user: role}}.
 */
export function readers(acl) {
  const a = acl || {};
  const members = Array.isArray(a.members) ? a.members.map((m) => m.user) : Object.keys(a.members || {});
  const people = [a.owner, ...members].filter(Boolean);
  const out = [];
  if (a.visibility === 'team') out.push('everyone who can open this agent');
  if (people.length) out.push(`its members: ${people.join(', ')}`);
  out.push("the agent's managers (everyone with write access to it)", 'workspace admins', "anyone who can change this agent's code");
  return out;
}

/** audienceOf(acl): the audience a person was shown, as POST /hosting's seen ({owner, visibility, teamRole, members: {user: role}}). */
export function audienceOf(acl) {
  const a = acl || {};
  const members = {};
  for (const m of (Array.isArray(a.members) ? a.members : [])) members[m.user] = m.role;
  if (!Array.isArray(a.members)) Object.assign(members, a.members || {});
  return { owner: a.owner || '', visibility: a.visibility || 'private', teamRole: a.teamRole || 'viewer', members };
}

/**
 * lockOf(v, me, started): the composer's lock for an open hosted
 * conversation — null when the view isn't one. {locked, kind, why, isHost,
 * pending}: kind 'paused' (a wider audience waits for its host), 'ended'
 * (hosting dropped or its host gone), 'start' (opened without sending: the
 * warning wasn't accepted in this page session), '' (started).
 */
export function lockOf(v, me, started) {
  const h = hostingOf(v);
  if (!h) return null;
  const root = v.run.rootId || v.run.id;
  const isHost = !!me && me === h.host;
  const pending = Object.keys(h.pending || {});
  if (h.state === 'paused') {
    return { locked: true, kind: 'paused', isHost, pending, root,
      why: `Paused: waiting for ${h.host} to confirm who is in it now${pending.length ? ` (${pending.join(', ')} and the rest)` : ''}` };
  }
  if (h.state === 'dropped' || h.state === 'gone') {
    return { locked: true, kind: 'ended', isHost, pending, root,
      why: h.state === 'gone' ? `Its host's private resources are gone: ${h.host} can't be reached`
        : `${h.host} no longer lets it use their private resources` };
  }
  if (!started || !started.has(root)) {
    return { locked: true, kind: 'start', isHost, pending, root,
      why: "Opened without sending: this conversation isn't private — start it to write" };
  }
  return { locked: false, kind: '', isHost, pending, root, why: '' };
}
