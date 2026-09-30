// model/rules.js — who may do what, and what the controls say: the top bar of
// an open conversation, the composer's state, the halt switch, a conversation
// row's menu and glyphs, the share dialog's rights. Pure functions of the data
// the backend sends (a run's view, GET /me, conversation rows), so both views
// show the same controls to the same person in the same state.
import { busy } from './fold.js';
import { badge } from './classes.js';
import { sharing } from './partition.js';
import { publishes } from './homes.js';
import { hostingOf } from './hosted.js';
import { keepsHome } from './harness-homes.js';

// access: what you may do in a conversation (its view's `access`: owner |
// system | participant | viewer; absent from an older backend = everything).
export function access(v) {
  const talk = !!v && v.access !== 'viewer';
  const own = !!v && (!v.access || v.access === 'owner' || v.access === 'system');
  return { talk, own, viewOnly: !!v && !talk };
}

// topBar describes the open conversation's header. row is its list row when
// the list has it (how many people it is shared with lives there).
export function topBar(v, row, me) {
  const r = v.run;
  const { talk, own } = access(v);
  const web = (v.config && v.config.toolset) === 'web';
  // a hosted (non-secure) conversation: its global instance answers compact and learn 409 (API.md)
  const hosted = !!hostingOf(v);
  // a coding agent's conversation (D147 §2.1): memory and skills are the
  // built-in agent's (the engine turns them off), and Compact is its own
  // /compact, offered only when it advertises one
  const harness = r.engine === 'harness';
  const hasCompact = harness && ((r.harness && r.harness.commands) || []).some((c) => c && c.name === 'compact');
  return {
    // an automation's run links back to it
    crumb: ['schedule', 'watcher'].includes(r.origin) && r.originId ? { kind: r.origin, id: r.originId } : null,
    title: r.title || 'run ' + r.id,
    status: r.status,
    // the tool mode — not who may see it (that is Share)
    lane: web ? 'web' : 'private',
    laneLabel: web ? '🌐 web' : '🔒 internal',
    // its class (D116, fixed for its life): icon + name, and the warning of
    // a class that can move internal data out (model/classes.js badge)
    cls: badge(v),
    viewOnly: !talk,
    talk, own,
    // (a coding agent cut off or that couldn't start: Retry resumes its session)
    retry: talk && (r.status === 'error' || r.status === 'canceled' || (harness && ['lost', 'failed'].includes((r.harness || {}).state))),
    compact: talk && !hosted && (!harness || hasCompact),
    learn: talk && !hosted && !harness,
    memory: harness ? null : Object.keys(v.memory || {}).length, // null: no Memory
    files: (v.files || []).length,
    tree: !!(r.parentId || (v.links || []).length || v.linkCount), // linkCount: a paged view's total
    // the model it was switched to (a pick); '' = the agent's default
    model: modelName((v.config && v.config.pick) || ''),
    // its task, pinned (D133): pinnedTask below; null when it has none
    task: pinnedTask(v),
    // sharing is per conversation: a subagent shares its root (a coding
    // agent's conversation says so: its dialog offers no copy, no hosting)
    shareRun: { id: r.rootId || r.id, title: r.title, ...(keepsHome(r) ? { engine: r.engine } : {}) },
    share: shareStatus(v, row),
    // Share — or, for a person's own conversation in their partition, which
    // only a copy in the shared space can share, publish (model/homes.js);
    // never for a coding agent's, which stays in their own space
    // (model/harness-homes.js keepsHome)
    sharing: sharing() && !publishes(r.rootId || r.id),
    publish: publishes(r.rootId || r.id) && !keepsHome(r),
    // what the owner let the agent read here, for now (D111)
    grants: grantChips(v, me),
    del: own,
  };
}

// --- the pinned task (D133) -------------------------------------------------------

// pinnedTask is the open conversation's task as its header shows it: the
// CURRENT request — the latest it was given (the backend sends `latest`
// once there is more than one), else the one that started it — verbatim,
// folded to one line, and how many others there are (the whole ledger:
// actions.asks). null when there is none — a watcher's (its job is its
// instructions), or an older backend's. (The model's own "# Your task"
// keeps the first request: that is the backend's, asks.go.)
export function pinnedTask(v) {
  const t = v && v.run && v.run.task;
  if (!t || !t.first) return null;
  const cur = t.latest || t.first;
  const text = cur.text || '';
  const flat = text.replace(/\s+/g, ' ').trim();
  return {
    line: flat.length > 160 ? flat.slice(0, 159) + '…' : flat,
    text, cut: !!cur.cut, from: askFrom(cur),
    more: Math.max(0, (t.count || 1) - 1),
    count: t.count || 1,
  };
}

// askFrom says who a request came from: a person, the parent run, an
// automation (schedule, trigger, channel), or the Learn skill button.
export function askFrom(a) {
  switch (a.source) {
    case 'human': case '': case undefined: return a.who || 'a person';
    case 'parent': return `its parent ${a.who || ''}`.trim();
    case 'learn': return 'Learn skill';
  }
  return a.who ? `${a.source} ${a.who}` : a.source;
}

// compactionWords is a compaction step's line (D133): what was summarised,
// what was hidden behind stubs.
export function compactionWords(d) {
  d = d || {};
  const parts = [];
  if (d.messages) parts.push(`compacted ${d.messages} message(s) into the summary`);
  if (d.masked) parts.push(`hid ${d.masked} old tool output(s) — the agent can restore them`);
  return parts.join('; ') || `compacted ${d.messages || 0} message(s) into the summary`;
}

// --- grants (D111) ----------------------------------------------------------------

// What a grant lets the agent do, in words: the ask and the chip. The backend's
// registry (_backend/grants.go grantDefs) sends them with the pending ask
// (pendingState.grantAsk) and each live grant ({ask, chip}); this table is the
// fallback for an older backend.
export const GRANTS = {
  threads: { ask: 'read your other conversations and automations', chip: 'reads your threads' },
};
const grantWords = (cap, sent = {}) => {
  const w = GRANTS[cap] || { ask: `use “${cap}”`, chip: cap };
  return { ask: sent.ask || w.ask, chip: sent.chip || w.chip };
};

// grantAsk: a parked call that needs the owner's grant — what it asks, and
// whether you may allow it (only the conversation's owner, whose threads
// they are; anyone who may steer it may deny). null when none is asked.
export function grantAsk(run, me) {
  const ps = (run && run.pendingState) || {};
  if (ps.kind !== 'approval' || !ps.grant) return null;
  const owner = run.owner || '';
  const canAllow = !!(me && me.kind === 'user' && !me.viewedBy && owner && me.user === owner);
  return {
    cap: ps.grant,
    lead: `The agent asks to ${grantWords(ps.grant, { ask: ps.grantAsk }).ask}`,
    canAllow,
    note: canAllow ? 'Allow it once, or in this conversation for an hour.' : `Only ${owner || 'its owner'} can allow this — you may deny it.`,
  };
}

const clock = (ms) => {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
};

// grantChips: the grants in force on the open conversation (a conversation's
// own; a subagent's view has none). Expiry is read here — nothing ticks.
export function grantChips(v, me, now = Date.now()) {
  const r = v.run;
  const { own } = access(v);
  return (r.grants || []).filter((g) => g.expiresMs > now).map((g) => {
    const w = grantWords(g.cap, g);
    return {
      cap: g.cap,
      label: `🔓 ${w.chip} · until ${clock(g.expiresMs)}`,
      title: `${g.grantedBy || 'The owner'} let the agent ${w.ask} in this conversation until ${clock(g.expiresMs)}`,
      revoke: own,
      run: r.rootId || r.id,
    };
  });
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
// rowGlyph: '?' waiting for you (the conversation, or a run below it: the
// row's `waiting`, D147 §4.3.8), '!' failed, a spinner while it works.
export const rowGlyph = (r) => r.status === 'waiting_input' || r.waiting ? 'ask' : r.status === 'error' ? 'error' : SPINNING.has(r.status) ? 'spin' : '';
// rowShared: how a shared row is shared, as chips — from whom (someone else's),
// with the team (to read or to write), with how many people — and whether it
// is one you shared; null for a private one.
export function rowShared(r) {
  const team = r.visibility === 'team';
  const n = r.members || 0;
  if (!team && !n) return null;
  const byMe = r.access === 'owner' || r.access === 'system';
  const chips = [];
  if (!byMe) chips.push({ kind: 'from', label: `from ${r.owner || 'the team'}` });
  if (team) chips.push({ kind: 'team', label: r.teamRole === 'participant' ? 'team · can write' : 'team · can read' });
  if (n) chips.push({ kind: 'people', label: n === 1 ? '1 person' : `${n} people` });
  return { byMe, chips, title: byMe ? `you shared it: ${chips.map((c) => c.label).join(', ')}` : `shared with you by ${r.owner || 'the team'}` };
}

// shareStatus: who can see the open conversation, said plainly for the top
// bar — Private, the team (to read or write), how many people, or whose it is
// when it was shared with you. Its owner changes it from there.
export function shareStatus(v, row) {
  const r = v.run;
  const { own } = access(v);
  const vis = (row && row.visibility) || r.visibility;
  const role = (row && row.teamRole) || r.teamRole;
  const n = (row && row.members) || 0;
  const people = n === 1 ? '1 person' : `${n} people`;
  if (!own) return { icon: '👥', label: `from ${r.owner || 'the team'}`, tone: 'in', title: 'Shared with you — see who else can see it' };
  if (vis === 'team') {
    return { icon: '👥', label: `team can ${role === 'participant' ? 'write' : 'read'}${n ? ` · ${people}` : ''}`, tone: 'on',
      title: 'Everyone who can open this agent can see it — change who can see it' };
  }
  if (n) return { icon: '👥', label: `shared with ${people}`, tone: 'on', title: 'Shared with people — change who can see it' };
  if (publishes(r.rootId || r.id)) return { icon: '🔒', label: 'private', tone: '', title: 'Only you can see it — share a copy of it in the shared space' };
  return { icon: '🔒', label: 'private', tone: '', title: 'Only you can see it — share it' };
}

// rowMenu: a row's actions — its owner renames, shares and deletes; anyone
// pins and archives for themselves; someone it was shared with may leave.
// A person's own conversation in their partition is shared by a copy —
// offered where the view can publish one (opts.publish: the web) — except
// a coding agent's, which stays there (model/harness-homes.js keepsHome).
export function rowMenu(r, opts = {}) {
  const own = r.access === 'owner' || r.access === 'system';
  const items = [];
  if (own) items.push({ label: 'Rename', action: 'rename' });
  items.push({ label: r.pinnedAt ? 'Unpin' : 'Pin', action: 'pin' });
  if (own && sharing() && !publishes(r.id)) items.push({ label: 'Share…', action: 'share' });
  else if (own && opts.publish && publishes(r.id) && !keepsHome(r)) items.push({ label: 'Share a copy…', action: 'share' });
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

// --- the model -----------------------------------------------------------------------

// modelName: a model reference without its provider ("apps/b|m" → "m").
export const modelName = (ref) => { const i = (ref || '').indexOf('|'); return i >= 0 ? ref.slice(i + 1) : ref || ''; };
const providerName = (path) => String(path || '').replace(/^apps\//, '');

// modelPicker: the composer's model — the open conversation's pick (its
// view's config; from its next turn) or, at home, the person's pick for new
// chats — out of the bound providers' models (GET /models), grouped by
// provider when there are several. '' is the agent's default.
export function modelPicker(v, homePick, catalog) {
  const data = (catalog && catalog.data) || [];
  const provs = [...new Set(data.map((m) => m.provider))];
  const value = v ? ((v.config && v.config.pick) || '') : (homePick || '');
  const options = [{ value: '', label: 'auto — the agent\'s default', group: '' },
    ...data.map((m) => ({ value: m.ref || m.id, label: provs.length > 1 ? `${m.id} · ${providerName(m.provider)}` : m.id, group: m.provider || '' }))];
  if (value && !options.some((o) => o.value === value)) options.push({ value, label: `${modelName(value)} (not listed now)`, group: '' });
  return {
    value, options,
    groups: provs.length > 1 ? provs.map((p) => ({ path: p, label: providerName(p) })) : [],
    shown: (data.length > 0 || !!value) && !(v && v.run.engine === 'harness'), // a coding agent's model is its option (#hctl)
    disabled: !!v && !access(v).talk,
    title: v ? 'the model this conversation uses from its next turn' : 'the model for your next new chat',
  };
}
