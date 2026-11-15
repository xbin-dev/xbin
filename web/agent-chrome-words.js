/**
 * agent-chrome-words.js — what xbin's own chrome for the workspace agent
 * says, and the rules it decides by (/vendor/agent-chrome-words.js, D232).
 * Pure: no DOM, no lit, no imports — the elements draw what these return
 * (bx-agent-dock.js, bx-agent-approve.js, bx-agent-changes.js,
 * bx-agent-panels.js), and node tests hold them.
 *
 * Everything here is said from xbind's own answers
 * (docs/protocol/delegation.md): a request's `preview`, a change set's
 * `line` / `before` / `after` / `warnings` / `blocked`, the audit rows. The
 * agent's own text (`summary`, `why`, a set's `title`) is never mixed into
 * a sentence: the elements quote it, labelled as unchecked.
 *
 * The choices the design left to its recommendations (the open questions
 * of the UX plan) are the constants of CHOICES below — one place.
 */

/** The design's recommendations, where the owner has not decided otherwise. */
export const CHOICES = Object.freeze({
  dockWidth: 400, dockMin: 360, dockMax: 720, // the dock: default, and how far it resizes (px)
  narrow: 640,                                // under this the dock is the whole page
  armMs: 600,                                 // Allow is not armed before this long after the sheet is shown
  duration: Object.freeze({ call: 'always', tile: 'hour' }), // a standing choice's default duration (Q5)
  warnedStartUnticked: true,                  // a change with a warning starts unticked (Q7)
  typeWhilePaused: false,                     // the page is not shown while paused (Q10)
  shortcut: Object.freeze({ key: 'j', ctrlOrMeta: true }), // Ctrl+J / ⌘J, when the shell has it free (Q11)
  adminsNamed: 2,                             // "an admin can fix this: A or B" (Q15)
});

// ---- small words -------------------------------------------------------------------------

/** toMs(v): a time as the API gives it (RFC 3339, unix seconds or ms) → ms since the epoch (0: none). */
export function toMs(v) {
  if (v == null || v === '') return 0;
  if (typeof v === 'number') return v > 1e12 ? v : v * 1000;
  const n = Date.parse(v);
  return Number.isFinite(n) && n > 0 ? n : 0; // (xbind writes a time that was never set as year 1: "0001-01-01T00:00:00Z")
}

const pad = (n) => String(n).padStart(2, '0');
/** clock(ms): "10:14" in local time. */
export const clock = (ms) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; };

/** left(ms): how long from now, in the deck's words: "23 h 58 min", "7 days", "4 min", "less than a minute". */
export function left(ms) {
  const m = Math.floor(ms / 60000);
  if (m < 1) return 'less than a minute';
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  return `${Math.floor(h / 24)} days`;
}

/** asked(req, now): "asked 10:14 · expires in 23 h 58 min" (what of it is known). */
export function asked(x, now = Date.now()) {
  const c = toMs(x && x.created), e = toMs(x && x.expires);
  return [c ? `asked ${clock(c)}` : '', e ? (e > now ? `expires in ${left(e - now)}` : 'expired') : ''].filter(Boolean).join(' · ');
}

/** bytes(n): "48 bytes", "14 KiB", "3.2 MiB". */
export function bytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return `${n} byte${n === 1 ? '' : 's'}`;
  if (n < 1024 * 1024) return `${+(n / 1024).toFixed(n < 10240 ? 1 : 0)} KiB`;
  return `${+(n / 1048576).toFixed(1)} MiB`;
}

const plural = (n, one, many = one + 's') => `${n} ${n === 1 ? one : many}`;

// a tile as a preview names it: a path, or {name, path, org}
function tileOf(req) {
  const p = (req && req.preview) || {};
  const t = p.tile ?? (p.session && p.session.tile) ?? req.tile ?? '';
  if (t && typeof t === 'object') return { name: t.name || t.title || lastOf(t.path) || '', path: t.path || '', org: t.org || '' };
  return { name: lastOf(t), path: String(t || ''), org: '' };
}
const lastOf = (path) => { const s = String(path || '').split('/').filter(Boolean).pop() || ''; return s ? s[0].toUpperCase() + s.slice(1) : ''; };

/** tile(req): {name, path, org} of the tile a request is about. */
export const tile = tileOf;

// (xbind's levels are read | write | terminal — terminal holds write)
const LEVEL = { owner: 'you own it', admin: 'you manage it', terminal: 'you can write here', write: 'you can write here', full: 'you can write here', read: 'you can only read here' };
/** levelWords(level): the person's own level on the tile, in words. */
export const levelWords = (level) => LEVEL[level] || (level ? `your level: ${level}` : '');

// ---- a request ---------------------------------------------------------------------------

/** needTitle(req): a request as a row of Needs you. */
export function needTitle(req) {
  const t = tileOf(req).name || 'a tile';
  if (req.kind === 'session') return req.preview && req.preview.session && req.preview.session.resume ? `Continue the tile agent in ${t}` : `Start a tile agent in ${t}`;
  if (req.kind === 'template') return 'Create a tile';
  return `Approve a change to ${t}`;
}

/** heading(req): the sheet's heading — {before, strong, after}: the tile's name is the strong part. */
export function heading(req) {
  const t = tileOf(req).name || 'a tile';
  if (req.kind === 'session') {
    return req.preview && req.preview.session && req.preview.session.resume
      ? { before: 'Continue the tile agent in ', strong: t, after: '?' } : { before: 'Start a tile agent in ', strong: t, after: '?' };
  }
  if (req.kind === 'template') return { before: 'Your agent wants to create a tile', strong: '', after: '' };
  return { before: 'Your agent wants to change something in ', strong: t, after: '' };
}

/**
 * sends(req): what the call carries — {text, note, binary}: the body as
 * text with "48 bytes · shown in full" (or how much of it is shown), or,
 * for bytes nobody can read, no text and "not text — 3.2 MiB, sha256 …".
 */
export function sends(req) {
  const size = Number(req.bodySize) || 0;
  const pb = req.preview && req.preview.body;
  if (req.bodyBinary || (pb && pb.binary)) {
    const sha = (pb && pb.sha256) || req.bodySha256 || '';
    return { text: '', binary: true, note: `not text — ${bytes((pb && pb.size) || size)}${sha ? `, sha256 ${sha}` : ''}` };
  }
  if (!size && !req.body) return { text: '', binary: false, note: 'nothing: the call has no body' };
  const text = String(req.body ?? '');
  // xbind's bodyShown is a boolean — true: `body` is the whole body, as text (delegation.Request); a count is a part of it
  const shown = req.bodyShown === true ? size : Number(req.bodyShown) || 0;
  const partial = shown > 0 && shown < size;
  return { text, binary: false, note: partial ? `showing the first ${bytes(shown)} of ${bytes(size)}` : `${bytes(size)} · shown in full` };
}

/** undoWords(req): what xbin can say about undoing it. */
export const undoWords = (req) => `xbin can't undo this. Whether ${tileOf(req).name || 'the tile'} can is up to ${tileOf(req).name || 'the tile'}.`;

/**
 * grantChoices(req): the three ways to allow a tile call — once, always
 * this exact action, always every change to the tile (wider) — each
 * {id, scope, label, text, wider}. A session start and a tile create have
 * one: they are asked every time.
 */
export function grantChoices(req) {
  if (req.kind !== 'call') return [];
  const t = tileOf(req).name || 'this tile';
  const st = (req.preview && req.preview.standing) || {};
  return [
    { id: 'once', scope: '', label: 'Once', text: 'This request only. The agent has 15 minutes to use it.', wider: false },
    { id: 'call', scope: 'call', label: 'Always, this exact action', text: st.call || `${req.method} to this path on ${t}, whatever it sends, without asking again.`, wider: false },
    { id: 'tile', scope: 'tile', label: `Always, every change to ${t}`, text: st.tile || `Any create, change or delete in ${t} that you could do yourself, without asking again.`, wider: true },
  ];
}

/** DURATIONS: how long a standing approval lasts — the contract's `hour` and `always`. */
export const DURATIONS = Object.freeze([{ id: 'hour', label: 'For one hour' }, { id: 'always', label: 'Until I take it back' }]);

/** allowLabel(req, choice): the Allow button's words — they follow the choice. */
export function allowLabel(req, choice = 'once') {
  if (req.kind === 'session') return req.preview && req.preview.session && req.preview.session.asks === true ? 'Start' : 'Start without asking'; // only xbind's word that the mode asks
  if (req.kind === 'template') return 'Create the tile';
  return choice === 'once' ? 'Allow once' : choice === 'call' ? 'Allow always' : `Allow every change to ${tileOf(req).name || 'the tile'}`;
}

/** allowSpoken(req, choice): …and as a screen reader says them. */
export function allowSpoken(req, choice = 'once') {
  if (req.kind !== 'call') return allowLabel(req, choice);
  return choice === 'once' ? 'Allow once' : choice === 'call' ? 'Allow always, this exact action' : `Allow every change to ${tileOf(req).name || 'the tile'}`;
}

/**
 * approveBody(req, choice, duration): what POST …/approve sends —
 * {grant, scope?, unseen?}. A body nobody could read is approved only by
 * saying so (unseen).
 */
export function approveBody(req, choice = 'once', duration = '') {
  const unseen = sends(req).binary ? { unseen: true } : {};
  if (req.kind !== 'call' || choice === 'once') return { grant: 'once', ...unseen };
  const d = duration === 'hour' || duration === 'always' ? duration : CHOICES.duration[choice] || 'hour';
  return { grant: d, scope: choice === 'tile' ? 'tile' : 'call', ...unseen };
}

/** widerWords(req): the second step of the wider grant. */
export function widerWords(req) {
  const t = tileOf(req).name || 'this tile';
  return {
    heading: `Allow every change to ${t}?`,
    means: `From now on your agent can create, change and delete anything in ${t} that you can, without asking.`,
    ends: 'It ends by itself after the time you pick. You can take it back sooner in Approvals.',
    back: 'Go back', allow: 'Allow every change',
  };
}

/**
 * sessionRows(req): a tile agent start as the sheet's rows —
 * {agent, mode: {tag, tone, text}, network, others, brief, danger}: the
 * mode leads; one that never asks is the danger wording, and Deny becomes
 * the primary button.
 */
export function sessionRows(req) {
  const s = (req.preview && req.preview.session) || {};
  const t = tileOf(req).name || 'the tile';
  // only xbind's word that the mode asks (a harness's ask or plan mode) earns the green tag: any other mode, and one
  // xbind doesn't vouch for, leads with the danger wording
  const asks = s.asks === true;
  const who = s.providerName || s.provider || 'A coding agent';
  const model = s.model ? ` · model ${s.model}` : '';
  const cred = s.credentials === 'own' ? ', on your own sign-in' : s.credentials === 'connector' || s.model ? ", through the workspace's connector" : '';
  let brief = '';
  try { const b = JSON.parse(req.body || '{}'); brief = String(b.prompt ?? b.brief ?? b.message ?? ''); } catch { brief = String(req.body || ''); }
  return {
    agent: `${who}${model}${cred}`,
    mode: asks ? { tag: 'asks before acting', tone: 'ok', text: 'It asks you before it edits a file or runs a command.', name: s.mode || '' }
      : { tag: 'acts without asking', tone: 'bad', text: `It edits files and runs commands in ${t} without asking you.`, name: s.mode || '' },
    network: !s.net || s.net === 'none' ? 'Its model only. No internet.' : `Network: ${s.net}`,
    others: s.api === false ? 'It can call no other tile.' : 'It can call the tile APIs this tile is granted.',
    brief,
    danger: asks ? '' : 'Your workspace agent asked for a mode that never asks. You will not get a chance to stop a single edit or command.',
    comes: `Its questions and permission requests come to you, in ${t} and in Needs you. Your workspace agent can't answer them.`,
  };
}

/** settled(req): a decided request in a few words. */
export function settled(req) {
  const at = toMs(req.decided) ? ` · ${clock(toMs(req.decided))}` : '';
  switch (req.state) {
    case 'approved': return `Allowed${req.grant === 'once' || !req.grant ? ' once' : ''} by you${at}`;
    case 'used': return `Allowed by you, and used${at}`;
    case 'denied': return `Denied by you${at}`;
    case 'expired': return 'Expired — not approved within 24 hours.';
    case 'withdrawn': return req.withdrawnBy === 'xbin' ? 'Withdrawn by xbin when your agent was stopped' : 'Withdrawn by the agent';
  }
  return 'Waiting for your approval';
}

// ---- when nothing can be allowed ---------------------------------------------------------

/**
 * stopRefusal(state): why the person can't ALLOW anything for their agent
 * now, read from GET /delegate/state — the refusal xbind answers Allow,
 * Always and Apply with then (409 {refusal}): 'workspace-off', 'no-agent',
 * 'paused', 'held'; '' when nothing stops it. Deny and Reject always work.
 */
export function stopRefusal(state) {
  if (!state) return '';
  if (!state.workspace) return 'workspace-off';
  if (state.access === false) return 'no-agent';
  if (state.paused) return 'paused';
  if (state.held) return 'held';
  return '';
}

const STOPPED = {
  'workspace-off': 'Workspace agents are turned off for this workspace, so nothing can be allowed until an admin turns them on again.',
  paused: 'Your agent is paused. Resume it first; then this can be allowed.',
  held: 'Your agent is held on this xbin right now, so nothing can be allowed.',
  'no-agent': 'You no longer have a workspace agent, so nothing can be allowed for it.',
};
/**
 * stopWords(refusal, kind): what a sheet says in place of Allow (kind
 * 'request') or Apply ('changeset') while its agent is stopped —
 * {title, text, still}, or null for no refusal. Any refusal xbind names
 * (a 409's) is said; one this page doesn't know, in general words.
 */
export function stopWords(refusal, kind = 'request') {
  if (!refusal) return null;
  const sets = kind === 'changeset';
  return {
    title: sets ? "Can't be applied now" : "Can't be allowed now",
    text: STOPPED[refusal] || "Your agent can't act on this xbin right now, so nothing can be allowed.",
    still: sets ? 'You can still reject it.' : 'You can still deny it.',
  };
}

// ---- standing approvals ------------------------------------------------------------------

/** standingRow(a, now): one standing approval — {tile, text, wider, since, until}. */
export function standingRow(a, now = Date.now()) {
  const until = toMs(a.until);
  return {
    tile: lastOf(a.tile) || a.tile || '',
    path: a.tile || '',
    wider: a.scope === 'tile',
    text: a.scope === 'tile' ? 'Always · every change' : `Always · this action: ${a.method || ''} ${a.path || ''}`.trim(),
    since: toMs(a.created) ? `since ${new Date(toMs(a.created)).toLocaleDateString()} ${clock(toMs(a.created))}` : '',
    until: until ? (until > now ? `ends in ${left(until - now)}` : 'ended') : 'until you take it back',
  };
}
export const STANDING_NOTE = 'Starting a tile agent and creating a tile are asked every time. Taking an approval back stops a change that is running under it.';

// ---- change sets -------------------------------------------------------------------------

const KIND_TAG = { 'grant.add': 'grant', 'grant.revoke': 'grant', 'binding.set': 'binding', 'binding.remove': 'binding', 'org.share': 'org share', 'limits.set': 'limit' };
/** kindTag(kind): "grant" · "binding" · "org share" · "limit". */
export const kindTag = (kind) => KIND_TAG[kind] || String(kind || '').split('.')[0];

const WARN_ICON = { PUBLIC: 'globe', 'OPEN PORT': 'network', EGRESS: 'external' };
/** warning(w): xbind's warning as {tag, icon, text} — "PUBLIC: anyone on the internet…" → the caps tag and its sentence. */
export function warning(w) {
  const s = String(w || '');
  const m = /^([A-Z][A-Z ]{1,20}):\s*(.*)$/s.exec(s);
  const tag = m ? m[1].trim() : 'WARNING';
  return { tag, icon: WARN_ICON[tag] || 'warning', text: m ? m[2] : s };
}

/** tickable(item): the person may tick it (xbind did not block it). */
export const tickable = (item) => !item.blocked;

/** initialTicks(cs): the items ticked when the review opens — never a blocked one, never (Q7) one with a warning. */
export function initialTicks(cs) {
  return ((cs && cs.items) || []).filter((it) => tickable(it) && !(CHOICES.warnedStartUnticked && it.warnings && it.warnings.length)).map((it) => it.n);
}

/** reviewWords(cs, ticked): the review's heading, sub line, footer and Apply button. */
export function reviewWords(cs, ticked) {
  const total = ((cs && cs.items) || []).length, n = ticked.length;
  return {
    heading: `Your agent proposes ${plural(total, 'admin change')}`,
    intro: 'Each line, its before and after and its warnings are written by xbin from the change itself. Nothing has been applied.',
    footer: `${n} of ${total} ticked · applied in order as you. If one fails, later ones are skipped and earlier ones stay.`,
    apply: n ? `Apply ${plural(n, 'change')}…` : 'Apply none',
    counts: [((cs && cs.items) || []).filter((it) => it.warnings && it.warnings.length).length, ((cs && cs.items) || []).filter((it) => it.blocked).length],
  };
}

/** needChangeTitle(cs): a proposal as a row of Needs you — and what is special about it. */
export function needChangeTitle(cs) {
  const items = (cs && cs.items) || [];
  const w = items.filter((it) => it.warnings && it.warnings.length).length, b = items.filter((it) => it.blocked).length;
  return { title: `${plural(items.length, 'admin change')} proposed`, note: [w ? `${w} ${w === 1 ? 'has' : 'have'} a warning` : '', b ? `${b} can't be applied` : ''].filter(Boolean).join(' · ') };
}

const RESULT = {
  applied: { word: 'applied', icon: 'ok', tone: 'ok' }, failed: { word: 'failed', icon: 'error', tone: 'bad' }, skipped: { word: 'skipped', icon: 'minus', tone: '' },
  stale: { word: 'out of date', icon: 'refresh', tone: 'warn', text: 'Someone changed this after the agent proposed it. What you ticked no longer matches, so xbin left it alone.' },
  rejected: { word: 'left out by you', icon: 'minus', tone: '' }, blocked: { word: "couldn't be applied", icon: 'lock', tone: 'warn' },
};
/** resultOf(item): an item's result as {word, icon, tone, text} ("xbin said: …" for a failure). */
export function resultOf(item) {
  const r = RESULT[item.result] || { word: item.result || '', icon: '', tone: '' };
  const text = item.result === 'failed' ? `xbin said: “${item.error || 'it failed'}”` : item.result === 'blocked' ? String(item.blocked || item.error || '') : r.text || '';
  return { ...r, text };
}

const NUM = ['none', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten'];
const num = (n) => NUM[n] || String(n);
/** resultsWords(cs): the results' title, the banner of a partial outcome, and the Undo button ('' when nothing can be undone). */
export function resultsWords(cs) {
  const items = (cs && cs.items) || [];
  const chosen = items.filter((it) => ['applied', 'failed', 'skipped', 'stale'].includes(it.result)); // what the person ticked (not what they left out, nor what xbind blocked)
  const applied = items.filter((it) => it.result === 'applied');
  const failed = chosen.filter((it) => it.result === 'failed').length, skipped = chosen.filter((it) => it.result === 'skipped').length;
  const title = !applied.length ? 'Nothing was applied' : applied.length === chosen.length ? `All ${applied.length} change${applied.length === 1 ? '' : 's'} applied`
    : `${applied.length} of ${chosen.length} changes applied`;
  let banner = '';
  if (failed) {
    const cap = (s) => s[0].toUpperCase() + s.slice(1);
    banner = `${cap(num(failed))} change${failed === 1 ? '' : 's'} failed${skipped ? `, so the ${skipped === 1 ? 'one' : num(skipped)} after ${failed === 1 ? 'it' : 'them'} ${skipped === 1 ? 'was' : 'were'} skipped` : ''}.`
      + (applied.length ? ` The ${applied.length === 1 ? 'one that was' : `${num(applied.length)} that were`} applied ${applied.length === 1 ? 'stays' : 'stay'}.` : '');
  }
  const undoable = applied.filter((it) => it.inverse).length;
  return { title, banner, undo: undoable ? `Undo the ${undoable} applied…` : '' };
}

/** endedWords(cs): how a set that was not applied ended ('' for one applied, in part or whole, or still waiting). */
export function endedWords(cs) {
  switch (cs && cs.state) {
    case 'rejected': return 'Rejected by you. Nothing was applied.';
    case 'expired': return 'Expired. Nothing was applied.';
    case 'withdrawn': return cs.withdrawnBy === 'xbin' ? 'Withdrawn by xbin when your agent was stopped. Nothing was applied.' : 'Withdrawn by the agent. Nothing was applied.';
  }
  return '';
}

/** stepUp(kind): the recent-sign-in step, as the server asked for it. */
export function stepUp(kind) {
  return kind === 'signin'
    ? { heading: 'Sign in again first.', text: 'Applying admin changes needs a sign-in from the last ten minutes.', action: 'Sign in again', password: false }
    : { heading: "Confirm it's you", text: 'Applying admin changes needs your password, or a sign-in from the last ten minutes.', action: 'Continue', password: true,
      sso: 'Use sign in again instead; your ticks are kept.' };
}

// ---- needs you ---------------------------------------------------------------------------

/**
 * needs({requests, changesets, sessions}, now): the tray's rows, oldest
 * first — {kind: 'request' | 'changeset' | 'session', id, title, note,
 * action, at}: a pending request, a proposed change set, a tile agent
 * session of the person's that waits for them.
 */
export function needs({ requests = [], changesets = [], sessions = [] } = {}, now = Date.now()) {
  const rows = [];
  for (const r of requests) if (r.state === 'pending') rows.push({ kind: 'request', id: r.id, title: needTitle(r), note: asked(r, now), action: 'Review…', at: toMs(r.created) });
  for (const c of changesets) {
    if (c.state !== 'proposed') continue;
    const w = needChangeTitle(c), e = toMs(c.expires);
    rows.push({ kind: 'changeset', id: c.id, title: w.title, note: [w.note, e > now ? `expires in ${left(e - now)}` : ''].filter(Boolean).join(' · '), action: 'Review…', at: toMs(c.created) });
  }
  for (const s of sessions) {
    if (!(s.pending > 0 || s.questions > 0 || s.status === 'waiting_permission')) continue;
    const t = lastOf(s.cwd || s.tile) || 'A tile';
    rows.push({ kind: 'session', id: s.id, tile: s.cwd || s.tile || '', title: `${t} · ${s.providerName || s.provider || 'the tile agent'} ${s.questions > 0 ? 'asks a question' : 'asks permission'}`,
      note: s.origin === 'agent' ? 'started by your agent' : '', action: 'Open', at: toMs(s.waitingSince || s.updated || s.created) });
  }
  return rows.sort((a, b) => a.at - b.at);
}

// ---- what my agent did -------------------------------------------------------------------

/**
 * did(row): one row of the audit log as a sentence — {text, strong, detail,
 * icon, group: 'change' | 'refused' | 'read', at}. `strong` is the tile's
 * name inside the sentence, said after `text`.
 */
// xbind's rows (delegation.Call): `effect` is a boolean — a side effect — and `under` is the standing approval a call ran under
// ("approval:<id>"), "preference" (a tile agent started unasked) or, on a decision, the grant and its scope ("once", "hour call",
// "always tile"). A word in `effect`, a scope in `under`, are read too.
const GRANT = { once: 'once', hour: 'for one hour', always: 'until you take it back' };
const grantWords = (u) => { const [g, sc] = String(u || '').split(' '); return GRANT[g] ? `${GRANT[g]}${sc === 'tile' ? ', every change' : sc === 'call' ? ', this action' : ''}` : ''; };
const underWords = (row) => { const u = String(row.under || '');
  return u === 'preference' ? 'without asking: your preference' : u.startsWith('approval:') ? 'under a standing approval of yours' : u ? `under “always, ${u === 'tile' ? 'every change' : 'this action'}”` : row.ticket ? 'you allowed it once' : ''; };
// the agent's own asking and record-keeping at xbind — a request filed or withdrawn, a change set proposed, its session's
// handle opened, kept and closed: nothing of the person's was changed ('' for a row that is none of these)
function asking(row) {
  const m = /^\/api\/xbin\/delegate\/(requests|changesets|sessions)(\/.*)?$/.exec(row.path || '');
  if (!m || row.dryRun || !row.method || row.method === 'GET') return '';
  const [, what, rest] = m;
  if (what === 'sessions') return row.method === 'POST' && !rest ? 'Began a session' : row.method === 'DELETE' ? 'Ended a session' : "Updated its session's record";
  if (row.method === 'POST' && !rest) return what === 'requests' ? 'Asked for your approval' : 'Proposed admin changes';
  return row.method === 'DELETE' ? (what === 'requests' ? 'Withdrew a request' : 'Withdrew proposed admin changes') : '';
}

export function did(row) {
  const t = lastOf(row.tile) || '';
  const call = `${row.method || ''} ${row.path || ''}`.trim();
  const at = toMs(row.ts);
  const cls = String(row.class || '');
  const effect = typeof row.effect === 'string' ? row.effect : '';
  if (cls.startsWith('decision:')) {
    const d = cls.slice(9);
    return { text: d.startsWith('den') ? 'You denied a request' : d.startsWith('revoke') ? 'You took an approval back' : 'You allowed a request', strong: '', detail: [t, effect || grantWords(row.under)].filter(Boolean).join(' · '), icon: 'person', group: 'change', at };
  }
  if (cls.startsWith('changeset:')) return { text: 'Applied an admin change as you', strong: '', detail: `${kindTag(cls.slice(10))}${row.status >= 400 ? ' · failed' : ''}`, icon: 'shield', group: 'change', at };
  if (row.refused) return { text: 'Was refused: ', strong: t || call, detail: String(row.refused), icon: 'lock', group: 'refused', at };
  const under = underWords(row);
  if (cls === 'session' || /\/term\/sessions$/.test(row.path || '')) return { text: 'Started a tile agent in ', strong: t, detail: [effect, under].filter(Boolean).join(' · '), icon: 'agent', group: 'change', at };
  const own = asking(row);
  if (own) return { text: own, strong: '', detail: '', icon: /session/.test(own) ? 'agent' : 'shield', group: 'read', at };
  const write = row.method && row.method !== 'GET' && !row.dryRun;
  if (write) return { text: 'Changed ', strong: t || call, detail: [call, under].filter(Boolean).join(' · '), icon: 'pencil', group: 'change', at };
  return { text: row.dryRun ? 'Checked what a change would do in ' : 'Read ', strong: t || call, detail: call + (row.n ? ` · ${row.n} more before this weren't listed` : ''), icon: 'eye', group: 'read', at };
}

/** DID_FILTERS: Changes (default) · Refused · Everything. */
export const DID_FILTERS = Object.freeze([{ id: 'change', label: 'Changes' }, { id: 'refused', label: 'Refused' }, { id: 'all', label: 'Everything' }]);

/** didByDay(rows, filter): the log's rows as sentences, newest first, grouped by day: [{day, rows}]. */
export function didByDay(rows, filter = 'change') {
  const out = [];
  for (const r of rows || []) {
    const d = did(r);
    if (filter !== 'all' && d.group !== filter) continue;
    const day = d.at ? new Date(d.at).toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' }) : 'Earlier';
    if (!out.length || out[out.length - 1].day !== day) out.push({ day, rows: [] });
    out[out.length - 1].rows.push(d);
  }
  return out;
}
export const DID_NOTE = "Kept by xbin for 13 months. Your agent and its page can't read or change this list. It never holds what was sent, only what was called.";

// ---- the dock's own state ----------------------------------------------------------------

/**
 * dockState({state, llm, me, error}): what the dock shows in place of the
 * agent's page, or 'page' —
 *   hidden       nothing at all (a non-admin before setup, and anyone who
 *                was not given an agent — state.access false: no chip either;
 *                an admin too, once the setup is done: the admin role reads
 *                the component's system page and is no agent of their own)
 *   setup        an admin before setup: the three steps — until someone was
 *                given an agent (state.who.chosen; who is an admin's alone)
 *   unavailable  it was on, and no language model is connected now
 *   off          an admin turned workspace agents off (it was set up once)
 *   paused       the person paused their agent
 *   held         the sealed component is held (state.held: 'locked' | 'older',
 *                or 'unavailable' with xbind's reason in state.heldWhy)
 *   offline      xbind can't be reached
 *   soon         everything is on, and this xbin ships no agent yet (state.agent is empty)
 *   page         the agent's page
 */
export function dockState({ state = null, llm = null, me = null, error = '' } = {}) {
  if (error || !state) return error ? 'offline' : 'hidden';
  const wsAdmin = !!(me && (me.admin || me.owner)) || !!state.who;
  const admin = wsAdmin || !!state.canAdminChanges;
  if (state.access === false && !wsAdmin) return 'hidden'; // nobody without an agent sees anything of it
  const model = !llm || llm.configured !== false;
  if (!state.workspace) return state.workspaceAt ? 'off' : admin ? 'setup' : 'hidden';
  if (state.who && !state.who.chosen) return 'setup'; // an admin: on, and nobody was given one yet (the third step)
  if (state.access === false) return 'hidden'; // an admin who gave others one and not themselves: administering it isn't having one
  if (!model) return 'unavailable';
  if (state.paused) return 'paused';
  if (state.held) return 'held';
  return state.agent ? 'page' : 'soon';
}

/** chip({state, llm, me, count}): the top-bar chip — {shown, label, spoken, count, paused}. */
export function chip(x) {
  const st = dockState(x);
  if (st === 'hidden') return { shown: false, label: '', spoken: '', count: 0, paused: false };
  const count = Number(x.count) || 0;
  if (st === 'setup') return { shown: true, label: 'set up workspace agent', spoken: 'Set up the workspace agent', count: 0, paused: false };
  const paused = st === 'paused';
  return { shown: true, label: 'workspace agent', spoken: paused ? 'Workspace agent, paused' : count ? `Workspace agent, ${count} need${count === 1 ? 's' : ''} you` : 'Workspace agent', count, paused };
}

/** needCount(state): what waits for the person because of the agent, from GET /delegate/state. */
export const needCount = (state) => (state ? (Number(state.pendingRequests) || 0) + (Number(state.openChangeSets) || 0) : 0);

/** admins(names): "Mira Okafor or Jonas Lind" (the first two, "and 3 more"); 'an admin' when none are known. */
export function admins(names) {
  const n = (names || []).filter(Boolean);
  if (!n.length) return 'an admin';
  const shown = n.slice(0, CHOICES.adminsNamed).join(' or ');
  return n.length > CHOICES.adminsNamed ? `${shown} and ${n.length - CHOICES.adminsNamed} more` : shown;
}

/**
 * STATES: §10's words for what the dock shows in place of the page —
 * {title, text, more?}; `{admins}` and `{at}` are filled by stateWords.
 */
const STATES = {
  unavailable: { title: 'Not available right now.', text: 'The workspace agent needs a language model, and this workspace has none connected at the moment. Your earlier conversations and the notebook are kept.', more: 'An admin can fix this: {admins}.' },
  off: { title: 'Turned off for this workspace.', text: 'An admin turned workspace agents off{at}. Nothing an agent started is running on your behalf. Your conversations and notebook are kept for when it is turned on again.' },
  paused: { title: 'Paused by you{at}.', text: "Your agent can't read or change anything in the workspace until you resume. Tile agents that are already running keep going; their questions still come to you." },
  locked: { title: 'Locked after a restart.', text: "Your agent's conversations and notebook are encrypted, and the workspace was restarted. They stay locked until an admin unlocks the workspace. Nothing is lost.", more: 'An admin can fix this: {admins}.' },
  older: { title: 'Waiting for a newer xbin.', text: "This workspace is running an older xbin than the one that last saved your agent's data. To keep that data safe the agent stays off until xbin is updated again. Nothing has been changed or removed." },
  offline: { title: "Can't reach xbin.", text: 'Showing what was loaded before. Approvals need a connection.' },
  heldOther: { title: 'Not available on this xbin.', text: "The workspace agent can't run here right now{why}. Your conversations and notebook are kept.", more: 'An admin can fix this: {admins}.' },
  soon: { title: 'Workspace agents are on.', text: "Approvals, what your agent did and your preferences work here already. The agent's own conversation is not part of this xbin yet: it arrives with an update." },
};
/** stateWords(kind, {admins, at, held}): a state's {title, text, more}. */
export function stateWords(kind, x = {}) {
  const w = STATES[kind === 'held' ? (x.held === 'older' ? 'older' : x.held === 'unavailable' ? 'heldOther' : 'locked') : kind];
  if (!w) return null;
  // (no route names the admins to a non-admin yet: without names the sentence ends at "this")
  const known = (x.admins || []).some(Boolean);
  const why = typeof x.why === 'string' && x.why ? `: ${x.why.replace(/\.$/, '')}` : '';
  const fill = (s) => s.replace(': {admins}', known ? `: ${admins(x.admins)}` : '').replace('{at}', x.at ? ` at ${clock(toMs(x.at))}` : '').replace('{why}', why);
  return { title: fill(w.title), text: fill(w.text), more: w.more ? fill(w.more) : '' };
}

/**
 * setupSteps({llm, state}): the three steps an admin sees before setup — each
 * {title, text, done, enabled, action}; the third has `choices` in place of
 * an action: [{who, label, primary}] for PUT /delegate/workspace {who}.
 */
export function setupSteps({ llm = null, state = null } = {}) {
  const model = !!(llm && llm.configured), hosted = !!(llm && llm.host), on = !!(state && state.workspace);
  const who = (state && state.who) || null, chosen = !!(who && who.chosen), n = who ? Number(who.entries) || 0 : 0;
  return [
    { id: 'model', title: 'Connect a language model', done: model, enabled: true, tag: hosted ? 'Set by your host' : '',
      text: model ? (hosted ? "Your host provides the models and sets the limits; those can't be changed here." : 'Connected.') : 'None is connected. Use a provider key, or a model your organisation runs itself.', action: model ? '' : 'Connect…' },
    { id: 'on', title: 'Turn workspace agents on', done: on, enabled: model, text: on ? 'On. You can turn it off again at any time; every agent stops at once.' : 'Off. You can turn it off again at any time; every agent stops at once.', action: on ? '' : 'Turn on' },
    { id: 'who', title: 'Choose who gets one', done: chosen, enabled: on, action: '',
      text: chosen ? (who.everyone ? 'Everyone in this workspace.' : `${n} ${n === 1 ? 'person or org' : 'people and orgs'} so far.`) + ' Change it under Admin → People: access to “Workspace agent”.'
        : 'Nobody has one yet. Give everyone one, or start with yourself and add people and orgs later (Admin → People). Orgs can add their own rules.',
      choices: on && !chosen ? [{ who: 'everyone', label: 'Everyone', primary: true }, { who: 'me', label: 'Just me for now', primary: false }] : [] },
  ];
}
export const SETUP_INTRO = { title: 'Set up the workspace agent', text: 'One agent per person that plans work across tiles and asks before anything changes. It is off until you finish these three steps. Nobody else sees it yet.' };

// ---- Preferences: the mode a tile agent your agent starts gets -------------------------------

/**
 * sessionModes(providers, modes): the Preferences picker for the modes of
 * tile agents the agent starts (agentSessions.modes of GET /agent/prefs),
 * one row per coding agent of GET /agent/providers that lists modes —
 * {id, name, value, options: [{id, name}], never: [name]}. options are the
 * modes that ask before acting (the catalog's non-explicit ones: the only
 * ones xbind saves there); never names the ones that don't, which xbind
 * refuses — shown with why, never offered. value '' is the coding agent's
 * own asking mode (also for a stored mode that isn't offered).
 */
export function sessionModes(providers, modes) {
  const saved = modes && typeof modes === 'object' ? modes : {};
  return (Array.isArray(providers) ? providers : []).filter((p) => p && p.id && Array.isArray(p.modes) && p.modes.length).map((p) => {
    const ms = p.modes.filter((m) => m && m.id);
    const options = ms.filter((m) => !m.explicit).map((m) => ({ id: m.id, name: m.name || m.id }));
    const cur = typeof saved[p.id] === 'string' ? saved[p.id] : '';
    return { id: p.id, name: p.name || p.id, value: options.some((o) => o.id === cur) ? cur : '', options, never: ms.filter((m) => m.explicit).map((m) => m.name || m.id) };
  });
}

/** neverAsks(names): why those modes aren't offered for tile agents the agent starts. */
export const neverAsks = (names) => `Not offered: ${names.join(', ')} — ${names.length === 1 ? 'it acts' : 'they act'} without asking you first, and xbin doesn't let a tile agent your agent starts run that way.`;

// ---- what the page may ask of the dock -----------------------------------------------------

// a request's or change set's id, as xbind mints them (24 lowercase hex digits): nothing else — never a dot segment or a
// path — is read from the page, and it goes into a URL the dock sends with the person's session
const ID = /^[0-9a-f]{24}$/;
const SESSION = /^[A-Za-z0-9_-]{1,64}$/;
const tilePath = (v) => {
  const t = typeof v === 'string' ? v.replace(/^\/+|\/+$/g, '') : '';
  return t && t.length <= 512 && !t.split('/').some((x) => !x || x === '.' || x === '..') ? t : '';
};
/** DRAFT_MAX: the most of a person's words a new session's draft carries, in characters. */
export const DRAFT_MAX = 8000;
// control characters but newline and tab, and the invisible ones that reorder or hide text
const UNSEEN = /[\u0000-\u0008\u000B-\u001F\u007F-\u009F\u200B-\u200F\u202A-\u202E\u2066-\u2069\uFEFF]/g;
/**
 * DRAFT_FROM: how a new session's draft from the agent's page is labelled
 * wherever the chrome shows it. The page renders what a model wrote and
 * its message is untrusted: the text is what the page SUGGESTS, for the
 * person to read and edit — never presented as their own words — and it
 * waits, unsent, until they press Send.
 */
export const DRAFT_FROM = Object.freeze({
  label: 'Suggested by your agent’s page — not your words',
  text: 'Read it and change it as you like. It waits in the new session’s message box: nothing is sent until you press Send.',
  paste: 'paste the text your agent’s page suggests, once you have read it',
  copy: 'Copy the suggested text',
});

/**
 * plainDraft(v): words for a new session's composer, as plain text — never
 * markup, never a command: line ends made \n, control and invisible
 * characters dropped, trimmed, at most DRAFT_MAX characters; '' for
 * anything that isn't a string.
 */
export function plainDraft(v) {
  return typeof v === 'string' ? v.replace(/\r\n?/g, '\n').replace(UNSEEN, '').trim().slice(0, DRAFT_MAX) : '';
}

/** handoffLink(tile): the plain link to a tile, for a shell that can't open a new session of its agent itself. */
export const handoffLink = (tile) => `/?tile=${encodeURIComponent(tilePath(tile)).replace(/%2F/gi, '/')}`;

/**
 * pageMessage(data): a message from the agent's page, read strictly — the
 * whole of what the page may ask of the chrome:
 *
 *   xbin:agent-open-request {id}      → {type: 'review', kind: 'request', id}
 *   xbin:agent-open-changeset {id}    → {type: 'review', kind: 'changeset', id}
 *   xbin:open-session {session, tile} → {type: 'session', tile, session}
 *   xbin:open-session {session: "new", tile, draft}
 *                                     → {type: 'new-session', tile, draft}  (draft: plainDraft)
 *   xbin:agent-open {view}            → {type: 'open', view}  (needs | approvals | activity — never prefs)
 *
 * or null. Ids and a word — and a new session's draft, the person's own
 * words, kept as plain text for its composer: no other field of a message
 * is ever read (the dock fetches the request from xbind itself), and none
 * of them decides anything — a draft is never sent by being handed over.
 */
export function pageMessage(data) {
  if (!data || typeof data !== 'object') return null;
  const id = typeof data.id === 'string' && ID.test(data.id) ? data.id : '';
  if (data.type === 'xbin:agent-open-request') return id ? { type: 'review', kind: 'request', id } : null;
  if (data.type === 'xbin:agent-open-changeset') return id ? { type: 'review', kind: 'changeset', id } : null;
  if (data.type === 'xbin:open-session') {
    const tile = tilePath(data.tile), session = typeof data.session === 'string' && SESSION.test(data.session) ? data.session : '';
    if (tile && session === 'new') return { type: 'new-session', tile, draft: plainDraft(data.draft) };
    return tile && session ? { type: 'session', tile, session } : null;
  }
  // only views that change nothing: Preferences — where the agent's reach widens — opens from the chrome's own menu alone
  if (data.type === 'xbin:agent-open') return PAGE_VIEWS.includes(data.view) ? { type: 'open', view: data.view } : null;
  return null;
}

/** PAGE_VIEWS: the dock's views the agent's page may ask for (xbin:agent-open {view}) — the ones that change nothing. */
export const PAGE_VIEWS = Object.freeze(['needs', 'approvals', 'activity']);

/** contextMessage(context): what the dock tells the page it was opened about — xbin:agent-context {tile} or {screen} — or null. */
export function contextMessage(context) {
  if (!context || typeof context !== 'object') return null;
  const tile = tilePath(context.tile);
  if (tile) return { type: 'xbin:agent-context', tile };
  return typeof context.screen === 'string' && context.screen && context.screen.length <= 200 ? { type: 'xbin:agent-context', screen: context.screen } : null;
}

/** clampDock(w, viewport): the dock's width — within its bounds, the whole page when the viewport is narrow. */
export function clampDock(w, viewport) {
  if (viewport && viewport < CHOICES.narrow) return viewport;
  const n = Math.round(Number(w) || CHOICES.dockWidth);
  return Math.max(CHOICES.dockMin, Math.min(CHOICES.dockMax, viewport ? Math.min(n, viewport - 240) : n));
}
