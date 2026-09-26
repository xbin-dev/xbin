// model/rules.js — who may do what, and what the controls say: the top bar of
// an open conversation, the composer's state, the halt switch, a conversation
// row's menu and glyphs, the share dialog's rights. Pure functions of the data
// the backend sends (a run's view, GET /me, conversation rows), so both views
// show the same controls to the same person in the same state.
import { busy } from './fold.js';

// access: what you may do in a conversation (its view's `access`: owner |
// system | participant | viewer; absent from an older backend = everything).
export function access(v) {
  const talk = !!v && v.access !== 'viewer';
  const own = !!v && (!v.access || v.access === 'owner' || v.access === 'system');
  return { talk, own, viewOnly: !!v && !talk };
}

// topBar describes the open conversation's header.
export function topBar(v) {
  const r = v.run;
  const { talk, own } = access(v);
  const web = (v.config && v.config.toolset) === 'web';
  return {
    // an automation's run links back to it
    crumb: ['schedule', 'watcher'].includes(r.origin) && r.originId ? { kind: r.origin, id: r.originId } : null,
    title: r.title || 'run ' + r.id,
    status: r.status,
    // the tool mode — not who may see it (that is Share)
    lane: web ? 'web' : 'private',
    laneLabel: web ? '🌐 web' : '🔒 internal',
    viewOnly: !talk,
    talk, own,
    retry: talk && (r.status === 'error' || r.status === 'canceled'),
    compact: talk,
    learn: talk,
    memory: Object.keys(v.memory || {}).length,
    files: (v.files || []).length,
    tree: !!(r.parentId || (v.links || []).length || v.linkCount), // linkCount: a paged view's total
    // sharing is per conversation: a subagent shares its root
    shareRun: { id: r.rootId || r.id, title: r.title },
    share: own ? 'Share' : 'Shared',
    shareTitle: own ? 'Who can see this conversation' : 'Who this is shared with',
    del: own,
  };
}

// composer: the message box's state — no conversation (home: a new ask), view
// only, steering a working run, answering a question, or following up.
export function composer(v, HOME) {
  const isBusy = !!(v && busy(v.run.status));
  const viewOnly = !!(v && v.access === 'viewer');
  return {
    busy: isBusy,
    stop: isBusy,
    disabled: viewOnly,
    placeholder: !v ? HOME.placeholder
      : viewOnly ? 'view only — shared with you to read'
      : isBusy ? 'steer — delivered at the agent\'s next step…'
      : v.run.status === 'waiting_input' && (v.run.pendingState || {}).kind !== 'approval' ? 'answer the question…' : 'follow up…',
  };
}

// The halt switch: a manager's brake, shown while it is on or while anything
// runs (waiting for a person is not running).
const ACTIVE = new Set(['running', 'awaiting', 'sleeping', 'waiting_input', 'queued', 'blocked']);
export function halt(me, on, rows) {
  return {
    shown: !!me.manager && (!!on || rows.some((r) => ACTIVE.has(r.status) && r.status !== 'waiting_input')),
    label: on ? '⏻ HALTED' : '⏻',
    title: on ? 'Resume — the agent is halted' : 'Stop every running agent now',
  };
}

// --- a conversation row ------------------------------------------------------

const SPINNING = new Set(['running', 'awaiting', 'sleeping', 'queued', 'blocked']);
// rowGlyph: '?' waiting for you, '!' failed, a spinner while it works.
export const rowGlyph = (r) => r.status === 'waiting_input' ? 'ask' : r.status === 'error' ? 'error' : SPINNING.has(r.status) ? 'spin' : '';
// rowShared: shared with the team or with people (⇆), and what to say about it.
export const rowShared = (r) => (r.visibility === 'team' || (r.members || 0) > 0
  ? { title: r.mine ? 'shared' : `shared by ${r.owner || 'the team'}` } : null);

// rowMenu: a row's actions — its owner renames, shares and deletes; anyone
// pins and archives for themselves; someone it was shared with may leave.
export function rowMenu(r) {
  const own = r.access === 'owner' || r.access === 'system';
  const items = [];
  if (own) items.push({ label: 'Rename', action: 'rename' });
  items.push({ label: r.pinnedAt ? 'Unpin' : 'Pin', action: 'pin' });
  if (own) items.push({ label: 'Share…', action: 'share' });
  items.push({ label: r.archivedAt ? 'Unarchive' : 'Archive', action: 'archive' });
  if (own) items.push({ label: 'Delete', action: 'delete', cls: 'rm' });
  else if (r.mine) items.push({ label: 'Leave', action: 'leave', cls: 'rm' });
  return items;
}

// --- the share dialog ---------------------------------------------------------------

// share: d is GET /runs/{id}/members. You manage it when it is yours (an
// ownerless one is the managers'); someone it was shared with may leave.
export function share(d, me) {
  const own = d && (d.owner === me.user || (d.owner === '' && me.manager) || me.kind === 'system');
  return {
    own,
    vis: !d ? '' : d.visibility === 'team' ? 'team-' + d.teamRole : 'private',
    leave: !!(d && !own && me.user && d.members.some((m) => m.user === me.user)),
  };
}
