// ws/words.js — what the workspace agent's page says (docs/workspace-agent.md
// §The agent's page; D235): the product's copy deck in the page's words,
// and the pure readings of the backend's shapes that choose among them — a
// tile agent's card, a request's state, a session's line, the scope chip,
// the plan's steps, the states of the page (quota, paused, offline). No lit,
// no DOM: node-tested (hack/agent-template-ws-page.test.mjs).
import { nameOf } from '../model/harness.js';

/**
 * CHOICES — the design's recommendations for questions the owner has not
 * answered yet, each in one place (D235 lists them):
 *   starterSends     a starter fills the composer instead of sending (Q8)
 *   typeWhilePaused  the composer takes no message while the agent is paused (Q10)
 *   sessionCap       no cap on sessions; archived ones fold under Older (Q17)
 *   offlineAfterMs   how long a dropped connection is "reconnecting" before the page says it can't reach the workspace
 *   narrow           below this width (px) the scope chip shortens to "14 connected · 6 not"
 *   tick             how often running times move (ms) — a running time is the only progress shown (no animation)
 */
export const CHOICES = Object.freeze({ starterSends: false, typeWhilePaused: false, sessionCap: 0, offlineAfterMs: 3000, narrow: 420, tick: 1000 });

/** W — fixed words of the page (the copy deck's, sentence case). */
export const W = Object.freeze({
  title: 'Workspace agent',
  placeholder: 'Ask for an outcome…',
  placeholderLater: 'Reply, or ask for something else…',
  placeholderQuestion: 'Or type your answer…',
  placeholderBrief: 'Or say what to change…',
  hint: 'Say what you want and how you’ll know it’s done. The agent works out the steps.',
  paused: 'Resume to send a message',
  reconnecting: 'Reconnecting…',
  keptOffline: 'Your message is kept here and is not sent until you’re connected.',
  keptPaused: 'Kept here, unsent. Resume your agent to send it.',
  answer: 'Answer',
  skip: 'Skip — let the agent look around first',
  skipSent: '(Skipped the question: look around first, and ask again only if you still need to.)',
  somethingElse: 'Something else',
  somethingElseHint: 'Say what you mean…',
  goAhead: 'Go ahead',
  changePlan: 'Change the plan',
  review: 'Review…',
  // the conversation's marker for what waits (D250): it opens the one item in xbin's chrome — never an Allow of its own
  marker: 'Waiting for your approval',
  markerOpen: 'open it',
  retryMarker: 'Try again in Waiting for you',
  open: 'Open',
  reviewStart: 'Review and start…',
  start: 'Start',
  onlyYou: 'Only you can answer.',
  needsConnection: 'Approvals need a connection.',
  scopeFoot: 'Scope is where this session looks. It is not a permission — blocked stays blocked whatever you tick.',
  sessionsFoot: 'A new session starts empty, with its own scope and model. The notebook is shared by all of them, within each one’s scope.',
  notebookFoot: 'You can open, edit or delete any page. Deleting a page makes the agent forget it.',
  // a page's org marks: nothing takes them off — the way to is to delete the page and have it written again (API.md §Workspace mode)
  marksNote: (orgs) => `Marked with ${orgs.join(', ')}: it may hold their data, so sessions on a model their rules keep out can’t read it. Your edits keep the marks. To clear them, delete the page and have a session that holds none of their data write it again.`,
  whatCanItDo: 'What can it do?',
  // the app's native view (native/ws.js; D233)
  itCan: 'It can',
  itCant: 'It can’t',
  answerBelow: 'Answer in the message box.',
  replaced: 'A newer question replaced this one.',
  updateToReview: 'Something waits for your approval. Update the app to review it.',
  needsApp: 'That opens in xbin’s own panels: update the app, or use xbin on the web.',
  startFromBrief: 'Start it from the brief.',
  readyToStart: 'Ready to start',
  askAgain: 'Ask again',
  updates: 'Updates from xbin and your tile agents',
  needsYou: 'Needs you',
  // the agent's switch, where xbind says it can't act (GET /ws/empty → off) and no other banner says so: a
  // notice's title above the backend's sentence (which switch, who changes it, where); offWhy when it gave none
  offTitle: Object.freeze({ 'no-agent': 'No workspace agent for you yet', held: 'Your agent is held on this xbin', 'no-sandbox': 'Your agent can’t act on this xbin' }),
  offWhy: Object.freeze({ 'no-agent': 'An admin chooses who has one: Admin → People → access, or the workspace agent’s set-up in the dock.',
    held: 'It does nothing until that is cleared. An admin can see why and clear it: Admin → Runtime, or the workspace agent’s dock.',
    'no-sandbox': 'This xbin runs tiles without sandboxes, where a workspace agent can’t act. An admin can run it with sandboxes.' }),
});

// ---- time ----------------------------------------------------------------------------

const pad = (n) => String(n).padStart(2, '0');
/** clock(ms): "10:02" (local), '' for no time. */
export const clock = (ms) => { if (!ms) return ''; const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; };
/** day(ms): "6 Nov". */
export const day = (ms) => (ms ? new Date(ms).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) : '');
/** running(ms): a running time — "4:10", "1:02:03". */
export function running(ms) {
  const s = Math.max(0, Math.floor((Number(ms) || 0) / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return h ? `${h}:${pad(m)}:${pad(sec)}` : `${m}:${pad(sec)}`;
}
/** inWords(ms): "23 h 58 min", "6 min", "40 s". */
export function inWords(ms) {
  const s = Math.max(0, Math.round((Number(ms) || 0) / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60), r = m % 60;
  if (h < 48) return r ? `${h} h ${r} min` : `${h} h`;
  return `${Math.round(h / 24)} days`;
}

/** short(model): a model reference as the chip says it — "hosted/big" → "big". */
export const short = (model) => { const s = String(model || ''); return s.includes('/') ? s.slice(s.lastIndexOf('/') + 1) : s; };
/** harness(id): a coding agent's name ("claude" → "Claude Code"). */
export const harness = (id) => (id ? nameOf({ provider: id }) : 'Coding agent');
/** possessive(name): "Orders’", "Team calendar’s". */
export const possessive = (name) => (/s$/i.test(name) ? `${name}’` : `${name}’s`);

// ---- a tile agent the agent started (API.md: GET /ws/sessions/{id}/cards → agents) ------------

/**
 * agentCard(a, now, tileName): what its card says — {tile, harness, glyph,
 * word, tone, line, note, button, waits, live}: status as a glyph and a
 * word ("working 4:10" moves with now), the last line, and the one button —
 * Answer when it waits for the person (and "Only you can answer."), else
 * Open.
 */
export function agentCard(a, now = Date.now(), tileName = (p) => p) {
  const live = ['starting', 'running', 'idle'].includes(a.status);
  const waits = !!a.waits && live;
  const base = { tile: tileName(a.tile) || a.tile, path: a.tile, harness: harness(a.harness), waits, live, line: a.last || '', note: '', button: W.open };
  if (waits) return { ...base, glyph: 'bell', word: 'waiting for you', tone: 'warn', line: a.waiting ? `Asks: ${a.waiting}` : 'It waits for your answer.', note: W.onlyYou, button: W.answer };
  switch (a.status) {
    case 'starting': return { ...base, glyph: 'clock', word: 'starting', tone: '' };
    case 'running': return { ...base, glyph: 'clock', word: `working ${running(now - (a.startedMs || now))}`, tone: '' };
    case 'idle': case 'ended': return { ...base, glyph: 'check', word: 'finished', tone: 'ok' };
    case 'failed': return { ...base, glyph: 'error', word: 'failed', tone: 'bad' };
    // an xbin restart cut it off mid-turn: it did not finish (the agent offers to continue it)
    case 'stopped': return { ...base, glyph: 'pause', word: 'stopped for the update', tone: 'warn', line: 'It didn’t finish. Its transcript is kept, and it can continue where it stopped.' };
    case 'lost': return { ...base, glyph: 'error', word: 'couldn’t continue', tone: 'bad', line: 'Its transcript is kept. Open it to see where it stopped.' };
  }
  return { ...base, glyph: 'pause', word: 'stopped', tone: '' };
}

// ---- a request for the person's approval (cards → requests) -----------------------------

const KIND_WORD = { call: 'change something in', session: 'start a tile agent in', template: 'create a tile', changeset: 'propose admin changes', web: 'read a web page', mcp: 'use a tool of', message: 'send a message to the tile agent in' };
// where the person decided (a request's `via`)
const VIA = { app: 'in the app', device: 'on a device', page: 'on the brief' };

/**
 * requestCard(r, now, tileName): what a request's card says — {head, what,
 * glyph, tone, waits, settled, line, result, button, kind}, from the
 * request's fields (API.md §Workspace mode, "In fields"): while it waits,
 * what, for which tile, when it expires (`expiresMs`), and Review… (the
 * chrome opens it; the page never draws an Allow); settled, who decided
 * (`by`), how wide the grant is (`grant`, `grantUntilMs`), where (`via`),
 * and what ran (`resultStatus`, `result`) — or Denied / Expired /
 * Withdrawn.
 */
export function requestCard(r, now = Date.now(), tileName = (p) => p) {
  const tile = r.tile ? tileName(r.tile) || r.tile : '';
  const kind = r.kind || 'call';
  const local = !r.ref || /^(brief|resume):/.test(r.ref);
  const what = r.title || (kind === 'template' ? 'Create a tile' : `${KIND_WORD[kind] || 'a request'}${tile ? ' ' + tile : ''}`);
  const call = r.method && r.path ? `${r.method} ${r.path}` : '';
  const out = { kind, tile, what, call, local, ref: r.ref || '', waits: false, settled: true, button: '', glyph: 'shield', tone: '', head: '', line: '', result: '', parts: [] };
  const when = (ms) => [ms ? clock(ms) : '', VIA[r.via] || ''].filter(Boolean).join(', ');
  switch (r.state) {
    case 'pending': {
      const left = r.expiresMs ? r.expiresMs - now : 0;
      // each fact its own element (D250): the parts; line joins them for a view that draws text
      const parts = [r.createdMs ? `asked ${clock(r.createdMs)}` : '', left > 0 ? `expires in ${inWords(left)}` : ''].filter(Boolean);
      return { ...out, waits: true, settled: false, head: W.marker, tone: 'warn', button: local ? '' : W.review, parts, line: parts.join(', ') };
    }
    case 'retry': // approved, and the tile agent didn't start: its person tries it again from xbin's batch (D250)
      return { ...out, waits: true, settled: false, head: 'Approved, but the tile agent didn’t start', glyph: 'error', tone: 'bad', result: r.result || '', button: W.retryMarker };
    case 'proposed': {
      const left = r.expiresMs ? r.expiresMs - now : 0;
      const parts = [r.createdMs ? `proposed ${clock(r.createdMs)}` : '', left > 0 ? `expires in ${inWords(left)}` : ''].filter(Boolean);
      return { ...out, waits: true, settled: false, head: r.title || 'Admin changes proposed', tone: 'warn', button: W.review, parts, line: parts.join(', ') };
    }
    case 'approved': case 'running': case 'applying':
      return { ...out, head: grantWords(r, tile), glyph: 'clock', line: 'running now…' };
    case 'used':
      return { ...out, head: grantWords(r, tile), glyph: 'check', tone: 'ok', parts: [when(r.decidedMs), call, r.resultStatus ? String(r.resultStatus) : ''].filter(Boolean),
        line: [when(r.decidedMs), call, r.resultStatus ? String(r.resultStatus) : ''].filter(Boolean).join(', '), result: r.result || '' };
    case 'failed':
      return { ...out, head: `${grantWords(r, tile)} — it failed`, glyph: 'error', tone: 'bad', parts: [when(r.decidedMs), call, r.resultStatus ? String(r.resultStatus) : ''].filter(Boolean),
        line: [when(r.decidedMs), call, r.resultStatus ? String(r.resultStatus) : ''].filter(Boolean).join(', '), result: r.result || '' };
    case 'denied': return { ...out, head: r.by === 'agent' ? 'Withdrawn by the agent' : 'Denied by you', glyph: 'xmark', line: when(r.decidedMs) };
    case 'expired': return { ...out, head: 'Expired — not approved within 24 hours.', glyph: 'clock', button: 'Ask again' };
    case 'withdrawn': return { ...out, head: 'Withdrawn by the agent', glyph: 'xmark' };
  }
  // a change set's end (applied, partial, rejected, …): its result line, else xbin's word. A plan's card keeps
  // each line's outcome (xbin's review closes itself after an Apply in which nothing failed), stays open on a
  // failure with what to do, and opens the results in xbin
  const lines = planLines(r);
  const open = lines.some((l) => l.open);
  return { ...out, head: r.result || (r.state ? `Decided — ${r.state}` : 'Decided'), line: when(r.decidedMs), lines,
    ...(lines.length && r.ref ? { button: W.open } : {}),
    ...(open ? { settled: false, tone: 'bad', glyph: 'error', todo: 'A line was not applied, so the lines after it were skipped; the earlier ones stay. Open it to read why in xbin’s words, then ask your agent for what is left.' } : {}) };
}

const LINE_WORD = Object.freeze({
  applied: ['check', 'ok', 'applied'], rejected: ['xmark', '', 'left out'], failed: ['error', 'bad', 'failed'],
  stale: ['error', 'bad', 'not applied — it changed after you saw it'], skipped: ['xmark', 'warn', 'skipped'], asked: ['clock', '', 'sent to an admin'],
  lapsed: ['clock', '', 'lapsed — never applied'], cancelled: ['xmark', '', 'cancelled — never applied'],
});

/**
 * planLines(r): a decided plan's lines as its card lists them (cards →
 * requests[].lines: xbind's sentence and the outcome of each) — [{n, text,
 * result, glyph, tone, word, note, open}]; open: it failed, went stale or
 * was skipped. [] for a row that is no decided plan.
 */
export function planLines(r) {
  return (Array.isArray(r && r.lines) ? r.lines : []).map((l) => {
    const result = l.result || '';
    // a line that waits: approved, and xbin applies it by itself once its tile provides what was approved
    const [glyph, tone, word] = result === 'waiting' ? ['clock', 'warn', `waits for ${l.waits || 'its tile'}`]
      : LINE_WORD[result] || (l.needsAdmin ? ['lock', 'warn', 'needs an admin'] : l.blocked || result === 'blocked' ? ['lock', 'warn', 'can’t be applied'] : ['xmark', '', result || 'not applied']);
    return { n: l.n, text: String(l.line || l.kind || ''), result, glyph, tone, word, note: ['failed', 'stale', 'lapsed', 'cancelled'].includes(result) ? String(l.error || '') : '', open: ['failed', 'stale', 'skipped'].includes(result) };
  });
}

/** grantWords(r, tile): who allowed a request and how wide — "Allowed once by you", "Allowed always, this exact action, by you", "Allowed every change to Invoices until 11:02, by you". */
export function grantWords(r, tile = '') {
  const who = r.by === 'xbin' ? 'by xbin' : 'by you';
  const until = r.grantUntilMs ? ` until ${clock(r.grantUntilMs)}` : '';
  switch (r.grant) {
    case 'call': return `Allowed always, this exact action${until}, ${who}`;
    case 'tile': return `Allowed every change to ${tile || 'the tile'}${until}, ${who}`;
    case 'once': return `Allowed once ${who}`;
  }
  return `Allowed ${who}`;
}

// ---- the brief ("scope, then hand off") --------------------------------------------------

export const BRIEF_FIELDS = Object.freeze([['goal', 'Goal'], ['doneWhen', 'Done when'], ['leaveAlone', 'Leave alone'], ['checkBy', 'Check by']]);

/**
 * briefCard(b, tileName, sd): {title, fields, editable, button, state,
 * runsOn, harnesses, picked, note}. sd is the backend's start-defaults for
 * the brief (GET …/briefs/{bid}/start-defaults: what the start would run
 * on, from xbind — {line, harness, harnesses: [{id, name, asks, net,
 * needsTicket, noSignIn, signIn}], ticket, needsSignIn}; a
 * harness that runs with no sign-in, on the person's saved sign-in or
 * only after they sign in is named so in the picker, and `signin` is the
 * harness to sign in to first — the card's button asks xbin's chrome). The one button is the backend's: "Review and
 * start…" (xbind's approval opens) or, where the person's preferences let
 * this start through without a ticket (`ticket: false`), "Start" — the
 * click is then the confirmation. The brief is what the tile agent is told
 * first; what the agent sends it later is not approved one by one.
 */
export function briefCard(b, tileName = (p) => p, sd = null) {
  const tile = tileName(b.tile) || b.tile;
  const st = b.state || 'draft';
  const words = { draft: '', asked: 'Waiting for your approval', started: 'Started', denied: 'Denied by you', replaced: 'Replaced by a newer brief' };
  // xbind says which run on the workspace's model with no sign-in (noSignIn): the picker says so
  const harnesses = (sd && sd.defaults && Array.isArray(sd.defaults.harnesses) ? sd.defaults.harnesses : [])
    .map((h) => ({ id: h.id, name: `${h.name || harness(h.id)}${h.signIn === 'saved' ? ' — your saved sign-in' : h.signIn === 'needed' ? ' — needs your sign-in' : h.noSignIn ? ' — no sign-in' : ''}`, noSignIn: !!h.noSignIn, signIn: h.signIn || '' }));
  const pickedId = (sd && sd.harness) || b.harness || '';
  const needs = sd && sd.needsSignIn && (st === 'draft' || st === 'asked') ? (sd.defaults.harnesses || []).find((h) => h.id === pickedId) : null;
  const runsOn = sd ? sd.line || '' : [b.harness ? harness(b.harness) : ''].filter(Boolean).join('');
  // what it would run on, a fact a chip (D250: never a line joined with "·"); the person picks on the approval
  const chips = sd && Array.isArray(sd.chips) ? sd.chips.map((c) => ({ text: String(c.text || ''), tone: c.tone || '' })) : runsOn ? [{ text: runsOn, tone: '' }] : [];
  const free = !!sd && sd.ticket === false && !!sd.harness;
  const q = b.request || null; // its latest request (ws/place.js): the one card a start
  const rc = q ? requestCard(q) : null;
  return {
    title: `Brief for ${possessive(tile)} agent`, tile, state: st, status: words[st] ?? st,
    fields: BRIEF_FIELDS.map(([k, label]) => ({ key: k, label, value: b[k] || '' })),
    editable: st === 'draft' || st === 'asked',
    // the brief's own button: Review and start… (or Start where its person lets it start unasked) — once asked, the
    // marker opens xbin's approval instead (D250: never a second button beside it); a start their preference lets
    // through is confirmed here; a brief whose request isn't in view keeps the old way to it
    button: st === 'draft' ? (free ? W.start : W.reviewStart) : st === 'asked' && rc && rc.local && rc.waits ? W.start : st === 'asked' && !q ? W.review : '',
    runsOn, harnesses, picked: pickedId, none: !!sd && !sd.harness,
    // xbind says a session of the picked coding agent would start signed out (D230): sign in first, in xbin's own card
    signin: needs ? { harness: needs.id, name: needs.name || harness(needs.id), button: `Sign in to ${needs.name || harness(needs.id)}…`,
      // where no chrome can draw the sign-in (the app's native view): the truth, and where to go
      elsewhere: `Needs your ${needs.name || harness(needs.id)} sign-in — set it up on the web: open this workspace in a browser and sign in to ${needs.name || harness(needs.id)} there. A saved sign-in is then used here too.` } : null,
    // only xbind's word that the mode asks (a harness's ask or plan mode) says it asks — unknown is not "asks"
    note: `This is what the tile agent is told first. What your agent sends it later isn’t approved message by message${sd && sd.asks === true ? ': the tile agent asks you before it acts.' : sd && sd.asks === false ? ', and in this mode it acts without asking you.' : '. Whether it asks you before it acts shows once xbin says what it runs on.'}`,
    edited: !!b.edited,
    // who wrote it (D248 review): xbind's update brief, word for word, or the agent's (a person's edit makes it theirs)
    by: b.xbin ? BRIEF_BY_XBIN : 'Written by your agent',
    chips, pickNote: st === 'draft' || st === 'asked' ? 'You pick the coding agent, its model and its mode on the approval.' : '',
    // the request it carries (D250): waiting — the marker that opens xbin's approval; failed to start — the way to
    // Try again; decided — what came of it
    request: q, waiting: !!(rc && rc.waits && rc.button === W.review), parts: rc ? rc.parts : [],
    retry: !!(rc && q.state === 'retry'),
    outcome: rc && !rc.waits ? `Its start: ${[rc.head.charAt(0).toLowerCase() + rc.head.slice(1), rc.result].filter(Boolean).join(' — ')}` : rc && q.state === 'retry' ? `Its start: approved, and the tile agent didn’t start — ${rc.result}` : '',
  };
}

/** briefCard's word for a brief xbind wrote: its start names it, and xbind sends the text. */
export const BRIEF_BY_XBIN = "xbin's update brief — written by xbin for this tile's update, not by your agent";

// ---- the plan ---------------------------------------------------------------------------

/**
 * planBlock(steps, k): the session's plan (cards → plan: [{text, kind:
 * reads | asks, state: todo | working | waiting | done | dropped}]) as the
 * kit's plan block — numbered, each step tagged with what it does or where
 * it stands — and whether it waits for a go-ahead.
 */
export function planBlock(steps, k = 'ws:plan', meta = '') {
  const list = Array.isArray(steps) ? steps : [];
  const started = list.some((s) => s.state && s.state !== 'todo');
  const asks = list.map((s, i) => (s.kind === 'asks' ? i + 1 : 0)).filter(Boolean);
  const entries = list.map((s) => ({
    content: s.text || '',
    status: s.state === 'done' || s.state === 'dropped' ? 'completed' : s.state === 'working' || s.state === 'waiting' ? 'in_progress' : 'pending',
    tag: stepTag(s),
  }));
  const note = !started && asks.length && list.length > 1
    ? `Saying go ahead starts step 1. It does not approve ${asks.length === 1 ? `step ${asks[0]}` : `steps ${asks.join(', ')}`}: ${asks.length === 1 ? 'it asks' : 'each asks'} when it is reached.` : '';
  return { block: { k, v: 0, kind: 'plan', title: 'Plan', numbered: true, meta, note, entries }, waitsForGo: !started && list.length > 0 };
}
function stepTag(s) {
  switch (s.state) {
    case 'done': return { text: 'done', icon: 'check', tone: 'ok' };
    case 'dropped': return { text: 'dropped', icon: 'xmark' };
    case 'working': return { text: 'working', icon: 'clock' };
    case 'waiting': return s.kind === 'asks' ? { text: 'needs your OK', icon: 'shield', tone: 'warn' } : { text: 'waiting for you', icon: 'bell', tone: 'warn' };
  }
  return s.kind === 'asks' ? { text: 'asks you', icon: 'shield', tone: 'warn' } : { text: 'reads only', icon: '' };
}

// ---- refusals (cards → refusals) ---------------------------------------------------------

/**
 * refusalCard(r, tileName): a tool call xbin refused, or that named a tile
 * the session isn't connected to ({callId, tool, kind, reason, tile}), as
 * its card — {kind, icon, title, text, note, ways: [what]} with the ways
 * forward the page offers: 'scope' (Open scope), 'model' (Change the
 * model…), 'whatCan' (What can it do?). paused, off and slow are a quiet
 * line: the page's banner says the rest.
 */
export function refusalCard(r, tileName = (p) => p) {
  const tile = r.tile ? tileName(r.tile) || r.tile : '';
  const reason = String(r.reason || '').replace(/\.$/, '');
  switch (r.kind) {
    case 'policy': return { kind: 'policy', icon: 'lock', title: tile ? `${tile} is blocked by a rule` : 'Blocked by a rule', text: reason ? `${reason}.` : '',
      note: 'This is a rule set by an admin, not something the agent can work around.', ways: ['model', 'scope'] };
    case 'cannot': return { kind: 'cannot', icon: 'info', title: 'Can’t do this', text: `${reason ? reason + '. ' : ''}It’s yours to do yourself${tile ? `, in ${tile}` : ''}.`, ways: ['whatCan'] };
    case 'hidden': return { kind: 'hidden', icon: 'eye-slash', title: 'Not connected in this session', text: `You left ${tile || 'this tile'} out of this session’s scope. Widen the scope, or use another session.`, ways: ['scope'] };
    case 'not-connected': return { kind: 'not-connected', icon: 'plug', title: `${tile || 'That tile'} isn’t connected in this session`, text: 'It isn’t there, or this session can’t see it.', ways: ['scope'] };
    case 'taint': return { kind: 'taint', icon: 'lock', title: 'This session’s model can’t hold that data', text: reason ? `${reason}.` : '', note: 'A session on a model that is allowed can.', ways: ['model'] };
    case 'brief-taint': return { kind: 'brief-taint', icon: 'lock', title: `${tile ? possessive(tile) : 'That tile’s'} agent can’t be told this`, text: reason ? `${reason}.` : '',
      note: 'This conversation holds data from an org whose rules don’t allow the model that tile agent runs on, so nothing from it may go there. Start the tile agent on the workspace’s connector, on a model the org allows — or ask from a new session that hasn’t read that data.', ways: [] };
    case 'mode': return { kind: 'mode', icon: 'lock', title: 'That tile agent takes no more messages from your agent', text: reason ? `${reason}.` : '',
      note: 'You switched it to a mode that doesn’t ask before it acts, so only you can drive it now. Switch it back to a mode that asks to let your agent message it again.', ways: [] };
    case 'command': return { kind: 'command', icon: 'lock', title: 'Not sent: it began with “/”', text: 'A coding agent reads a message that begins with “/” as one of its own commands, and those are yours to give. Your agent should say it in words.', ways: [] };
    // the web (D239): a page xbin didn't read — the policy, your setting, an org's rule, or the address itself
    case 'web': return { kind: 'web', icon: 'globe', title: 'xbin didn’t read that web page', text: reason ? `${reason}.` : '',
      note: 'What your agent may read of the web is set by an admin for the workspace, by you in Preferences, and by the orgs whose data this conversation holds.', ways: ['whatCan'] };
    case 'mcp': return { kind: 'mcp', icon: 'plug', title: 'xbin didn’t run that tool', text: reason ? `${reason}.` : '',
      note: 'What MCP tools your agent may use is set by an admin for the workspace, by you in Preferences, by the tile, and by the orgs whose data this conversation holds.', ways: ['whatCan'] };
    case 'no-agent': return { kind: 'no-agent', quiet: true, icon: 'power', text: 'You haven’t been given a workspace agent — an admin chooses who has one. Nothing was read or changed.' };
    case 'held': return { kind: 'held', quiet: true, icon: 'power', text: 'Your agent is held on this xbin — an admin can clear it. Nothing was read or changed.' };
    case 'paused': return { kind: 'paused', quiet: true, icon: 'pause', text: 'Paused — nothing was read or changed.' };
    case 'off': return { kind: 'off', quiet: true, icon: 'power', text: 'Workspace agents are turned off — nothing was read or changed.' };
    case 'slow': return { kind: 'slow', quiet: true, icon: 'clock', text: 'Slowing down — the agent is doing a lot at once.' };
  }
  return { kind: r.kind || 'refused', icon: 'lock', title: 'xbin refused this', text: reason ? `${reason}.` : '', ways: [] };
}

// ---- the context chip ---------------------------------------------------------------------

/**
 * aboutOf(c): a message's context as the chip draws it — the page sends
 * {tile} or {screen} (POST /runs/{id}/message {context}); the backend
 * answers it back on the message (`context`: {tile, name?, connected?} |
 * {screen}), resolved by xbind. null: none.
 */
export function aboutOf(c) {
  if (!c || typeof c !== 'object') return null;
  if (c.tile) return { tile: String(c.tile), name: String(c.name || c.tile), connected: c.connected };
  if (c.screen) return { screen: String(c.screen) };
  return null;
}

/** aboutLabel(c): the chip's words in one line — "About Orders", "About the Sales desk screen", "About Ledger — not connected" (c: aboutOf's, or the composer's {tile, name, not}). */
export function aboutLabel(c) {
  if (!c) return '';
  if (!c.tile) return `About the ${c.screen} screen`;
  return `About ${c.name || c.tile}${c.not || c.connected === false ? ' — not connected' : ''}`;
}

/** blankAt(text): where a starter's blank is ([start, end] of "___"), or null. */
export function blankAt(text) {
  const i = String(text || '').indexOf('___');
  return i < 0 ? null : [i, i + 3];
}

// ---- sessions -------------------------------------------------------------------------------

/** sessionRow(s, now): the switcher's line for a session — {name, scope, status: {glyph, word, tone}}. */
export function sessionRow(s, now = Date.now()) {
  const c = (s.connection && s.connection.counts) || {};
  const scope = [c.tiles != null ? `${c.tiles} tiles` : '', c.orgs != null ? `${c.orgs} orgs` : '', short(s.model)].filter(Boolean).join(' · ');
  let status;
  // held (GET /ws/sessions → held): nothing runs in it until it moves — its banner says why and the ways forward
  if (s.held) status = { glyph: 'lock', word: `held: can’t go on on ${short(s.held.model || s.model)}`, tone: 'bad' };
  else if (s.status === 'waiting_input' || s.requests > 0) status = { glyph: 'bell', word: 'waiting for you', tone: 'warn' };
  else if (['running', 'awaiting', 'sleeping'].includes(s.status)) status = { glyph: 'clock', word: `working${s.agents ? ` · ${s.agents} tile agent${s.agents === 1 ? '' : 's'}` : ''}`, tone: '' };
  else if (s.agents) status = { glyph: 'clock', word: `${s.agents} tile agent${s.agents === 1 ? '' : 's'} working`, tone: '' };
  else status = { glyph: '', word: `idle${s.activityMs ? ' · ' + day(s.activityMs) : ''}`, tone: '' };
  return { name: s.name || (s.main ? 'Main' : 'New session'), scope, status, main: !!s.main, archived: !!s.archivedAt };
}

// ---- the scope chip -------------------------------------------------------------------------

/**
 * chipWords(counts, model, narrow): the chip's segments — connected ("14
 * tiles · 3 orgs"), not connected ("6 not connected", absent at zero), the
 * model — xbin's own sentence where it gave one, shortened when narrow
 * ("14 connected · 6 not"); and the screen reader's one sentence.
 */
export function chipWords(counts, model, narrow = false, priv = false, reach = null) {
  const c = counts || {};
  const t = c.tiles || 0, o = c.orgs || 0, n = c.notConnected || 0;
  const m = short(model) + (priv ? ' · private' : '');
  const conn = narrow ? `${t} connected` : `${t} tile${t === 1 ? '' : 's'} · ${o} org${o === 1 ? '' : 's'}`;
  const not = n ? (narrow ? `${n} not` : `${n} not connected`) : '';
  const nw = netWords(reach);
  const label = `Scope: ${t} tile${t === 1 ? '' : 's'} in ${o} org${o === 1 ? '' : 's'} connected${n ? `, ${n} not connected` : ''}. Model ${m || 'not set'}.${nw.said ? ` ${nw.said}` : ''}`;
  return { conn, not, model: m, label, summary: c.summary || '', net: nw.seg, netTitle: nw.said };
}

/**
 * netWords(reach): the web and MCP tools in the connection line (the
 * session's `reach`, xbind's: {web, mcp: {state: on | asks | off, why}}) —
 * {seg: "web · MCP" | "web" | "MCP" | "MCP · 2 outside" | '', said: the sentence a screen
 * reader and the title say}. Off says nothing in the line: the empty state
 * and "What can it do?" say why.
 */
export function netWords(reach) {
  const on = (x) => !!x && (x.state === 'on' || x.state === 'asks');
  const web = on(reach && reach.web), mcp = on(reach && reach.mcp);
  const out = mcp ? Number(reach.mcp.listed) || 0 : 0; // the outside servers it may use (D240)
  const seg = [web ? 'web' : '', mcp ? `MCP${out ? ` · ${out} outside` : ''}` : ''].filter(Boolean).join(' · ');
  const said = [web ? (reach.web.state === 'asks' ? 'Reads web pages you approve.' : 'Reads the web.') : '',
    mcp ? `Uses MCP tools you approve${out ? `, ${out === 1 ? 'one server' : `${out} servers`} of them outside this workspace` : ''}.` : ''].filter(Boolean).join(' ');
  return { seg, said };
}

// ---- the page's states (docs/workspace-agent.md §The agent's page) ------------------------------------------

/**
 * quotaOf(state): the stopping quota that holds the agent (`state.quota`:
 * {stopped, limitWords, resets, raise, …}) as {resets, words, until, raise};
 * null when none.
 */
export function quotaOf(state) {
  const q = state && state.quota;
  if (!q || !q.stopped) return null;
  const resets = Date.parse(q.resets) || 0;
  const at = resets ? clock(resets) : '';
  return {
    resets, until: at ? `Sending resumes at ${at}` : 'Sending resumes when the limit resets',
    words: `${q.limitWords ? `You've used ${q.limitWords}. ` : ''}${at ? `It resets at ${at}. ` : ''}You can still read everything here; tile agents on your own sign-in keep working.`,
    raise: q.raise ? `${q.raise[0].toUpperCase()}${q.raise.slice(1)} can raise your limit.` : 'An admin can raise your limit.',
  };
}

/**
 * composerState(o): what the composer says and whether it sends —
 * {placeholder, disabled, note}, from the page's state: paused, a quota,
 * the connection, and the conversation (empty, a question, a brief).
 */
export function composerState({ paused = false, quota = null, conn = 'live', updating = false, offline = false, off = false, empty = true, asking = false, brief = false } = {}) {
  if (off) return { placeholder: 'Workspace agents are turned off', disabled: true, note: '' };
  if (paused && !CHOICES.typeWhilePaused) return { placeholder: W.paused, disabled: true, note: '' };
  if (quota) return { placeholder: quota.until, disabled: true, note: '' };
  if (updating) return { placeholder: W.reconnecting, disabled: true, note: '' };
  if (offline || conn !== 'live') return { placeholder: empty ? W.placeholder : W.placeholderLater, disabled: false, hold: true, note: offline ? W.keptOffline : '' };
  if (asking) return { placeholder: W.placeholderQuestion, disabled: false, note: '' };
  if (brief) return { placeholder: W.placeholderBrief, disabled: false, note: '' };
  return { placeholder: empty ? W.placeholder : W.placeholderLater, disabled: false, note: '' };
}

/** slug(title): a notebook page's slug from its title ("How Ana likes things done" → "how-ana-likes-things-done"). */
export const slug = (title) => String(title || '').toLowerCase().normalize('NFKD').replace(/[^\p{Letter}\p{Number}]+/gu, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'note';

/** pageTag(p): a notebook page's org tag — "yours", or its orgs. */
export const pageTag = (p) => (p.orgs && p.orgs.length ? p.orgs.join(', ') : 'yours');
/** pageClosed(p): {closed, why} — a page this session may not read (the backend's `locked`/`lockedWhy`, or `readable: false` with its `reason`). */
export function pageClosed(p) {
  const closed = p.readable === false || !!p.locked;
  return { closed, why: closed ? p.reason || p.lockedWhy || 'Not available in this session.' : '' };
}
