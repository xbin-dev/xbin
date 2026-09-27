// model/auto-channels.js — chat channels on the Automations page (D86), the
// state and the actions: a channel appears when an adapter tile bound to this
// agent (a messaging bridge) says hello; a manager claims it; its owner
// decides who may talk (pairing codes, allowlists), which lane and class its
// conversations run in, and sees its sessions and the replies that could not
// be delivered. auto-channels.js draws it on the web. The adapter side is
// /docs/agent-inbox.md.
import { selfApi as api, jbody } from '/vendor/bx-kit.js';
import { registerKind } from './auto.js';

// st: what the open channel's detail shows (open() loads it); code and add
// are the pairing-code and allow-someone inputs, note what the last action said.
export const st = { id: 0, peers: [], sessions: [], failed: [], draft: null, code: '', add: '', note: '' };

export async function open(id, page) {
  if (st.id !== id) Object.assign(st, { id, peers: [], sessions: [], failed: [], draft: null, code: '', add: '', note: '' });
  const it = page.items.find((i) => i.kind === 'channel' && i.id === id);
  if (!it) return;
  const owner = it.access === 'owner';
  const get = (path, key) => api(`/channels/${id}/${path}`).then((r) => r[key] || []).catch(() => []);
  [st.sessions, st.peers, st.failed] = await Promise.all([
    it.access === 'claim' ? [] : get('sessions', 'sessions'),
    owner ? get('peers', 'peers') : [],
    owner ? get('outbox?state=failed', 'items') : [],
  ]);
  if (!st.draft) st.draft = draftOf(it);
}

// The rules form edits a flat draft of the policy (docs/agent-inbox.md);
// orig is the policy as it was — PUT replaces the whole policy, so what the
// form does not show goes back as it was.
export function draftOf(it) {
  const pol = (it.config || {}).policy || {};
  const dm = pol.dm || {}, g = pol.groups || {};
  return {
    name: it.name, visibility: it.visibility || 'private',
    dmPolicy: dm.policy || 'pairing', dmScope: dm.scope || '',
    groupPolicy: g.policy || 'allowlist', allow: (g.allow || []).join(', '),
    requireMention: g.requireMention !== false, followThreads: g.followThreads !== false, groupThreads: g.threads || '',
    linkedOnly: !!g.linkedOnly, trustLinked: !!pol.trustLinked,
    privateLane: !!pol.privateLane, trustedGroups: (pol.trustedGroups || []).join(', '),
    // the classes its conversations get (D116): '' = the lane's built-in
    privateClass: pol.privateClass || '', webClass: pol.webClass || '',
    reset: pol.reset || '', system: pol.system || '', ratePerMin: pol.ratePerMin || '',
    deny: pol.deny ? pol.deny.join(', ') : null, // null: the default list
    orig: pol,
  };
}

// policyOf: the policy a save sends — the one it was, with the form's fields.
export function policyOf(d) {
  const list = (s) => s.split(/[\s,]+/).filter(Boolean);
  const o = d.orig || {};
  const p = {
    ...o,
    dm: { ...(o.dm || {}), policy: d.dmPolicy },
    groups: { ...(o.groups || {}), policy: d.groupPolicy, allow: list(d.allow), requireMention: d.requireMention, followThreads: d.followThreads },
  };
  // a field the form cleared goes (the backend's default)
  const put = (obj, k, v) => { if (v) obj[k] = v; else delete obj[k]; };
  put(p.dm, 'scope', d.dmScope);
  put(p.groups, 'threads', d.groupThreads);
  put(p.groups, 'linkedOnly', !!d.linkedOnly);
  put(p, 'trustLinked', !!d.trustLinked);
  put(p, 'privateLane', !!d.privateLane);
  put(p, 'trustedGroups', d.privateLane ? list(d.trustedGroups) : null);
  put(p, 'privateClass', d.privateClass);
  put(p, 'webClass', d.webClass);
  put(p, 'reset', d.reset);
  put(p, 'system', d.system.trim());
  put(p, 'ratePerMin', +d.ratePerMin > 0 ? +d.ratePerMin : 0);
  put(p, 'deny', d.deny != null ? list(d.deny) : null);
  return p;
}

// channelCan: its owner manages people, sessions and rules; a manager
// overseeing it may switch it off or remove it; an announced one is claimed.
export function channelCan(it) {
  const owner = it.access === 'owner';
  return { owner, claim: it.access === 'claim', manage: owner || it.access === 'oversee', live: it.enabled || it.access === 'claim' };
}

// The pairing queue (codes still valid) and the people it already knows.
export const pendingPeers = () => st.peers.filter((x) => x.state === 'pending' && x.codeExpires * 1000 > Date.now());
export const knownPeers = () => st.peers.filter((x) => x.state !== 'pending');
export const codeMinutes = (x) => Math.max(1, Math.round((x.codeExpires * 1000 - Date.now()) / 60000));

// --- actions ---------------------------------------------------------------

// act runs a change and reloads; the note is what it says afterwards (fn may
// return its own).
async function act(page, fn, note = '') {
  page.err = '';
  try {
    const said = await fn();
    st.note = typeof said === 'string' ? said : note;
    await page.load();
  } catch (e) { page.err = e.message; page.changed(); }
}

const path = (it, rest = '') => `/channels/${it.id}${rest}`;
const body = () => ({ name: st.draft.name.trim(), visibility: st.draft.visibility, policy: policyOf(st.draft) });

export function claim(it, page) {
  return act(page, async () => { await api(path(it, '/claim'), jbody(body(), 'POST')); st.draft = null; },
    'Claimed — it is yours now. Messages to the bot are answered by the rules below.');
}
export function save(it, page) {
  return act(page, async () => { await api(path(it), jbody(body(), 'PUT')); st.draft = null; }, 'Saved.');
}
export const toggle = (it, page) => act(page, () => api(path(it), jbody({ enabled: !it.enabled }, 'PUT')));
// del removes a channel (its conversations stay). The view confirms first.
export async function del(it, page) {
  await act(page, () => api(path(it), { method: 'DELETE' }));
  page.show(null);
}
export function pair(it, page) {
  const code = st.code.trim();
  if (!code) return;
  return act(page, async () => {
    const r = await api(path(it, '/pair'), jbody({ code }, 'POST'));
    st.code = '';
    return `Paired with ${r.name || r.peerId}.`;
  });
}
export const peer = (it, page, id, patch) => act(page, () => api(path(it, `/peers/${encodeURIComponent(id)}`), jbody(patch, 'PUT')));
export const forget = (it, page, id) => act(page, () => api(path(it, `/peers/${encodeURIComponent(id)}`), { method: 'DELETE' }));
export const resetSession = (it, page, key) => act(page, () => api(path(it, '/sessions/reset'), jbody({ key }, 'POST')),
  'That session starts afresh with its next message.');
export const retry = (it, page, oid) => act(page, () => api(path(it, `/outbox/${oid}/retry`), { method: 'POST' }), 'Queued again.');

registerKind('channel', {
  label: 'Channels', order: 0, open, runsLabel: 'Conversations',
  empty: 'none — bind a messaging bridge (the agent-messaging-bridge template) to this agent and it shows up here to claim',
});
