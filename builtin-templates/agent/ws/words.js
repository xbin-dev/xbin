// ws/words.js — what the workspace agent's page says (docs/workspace-agent.md
// §The agent's page; D232): the product's copy deck in the page's words,
// and the pure readings of the backend's shapes that choose among them — a
// tile agent's card, a request's state, a session's line, the scope chip,
// the plan's steps, the states of the page (quota, paused, offline). No lit,
// no DOM: node-tested (hack/agent-template-ws-page.test.mjs).
import { nameOf } from '../model/harness.js';

/**
 * CHOICES — the design's recommendations for questions the owner has not
 * answered yet, each in one place (D232 lists them):
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
  answer: 'Answer',
  skip: 'Skip — let the agent look around first',
  skipSent: '(Skipped the question: look around first, and ask again only if you still need to.)',
  somethingElse: 'Something else',
  somethingElseHint: 'Say what you mean…',
  goAhead: 'Go ahead',
  changePlan: 'Change the plan',
  review: 'Review…',
  open: 'Open',
  reviewStart: 'Review and start…',
  start: 'Start',
  onlyYou: 'Only you can answer.',
  needsConnection: 'Approvals need a connection.',
  scopeFoot: 'Scope is where this session looks. It is not a permission — blocked stays blocked whatever you tick.',
  sessionsFoot: 'A new session starts empty, with its own scope and model. The notebook is shared by all of them, within each one’s scope.',
  notebookFoot: 'You can open, edit or delete any page. Deleting a page makes the agent forget it.',
  whatCanItDo: 'What can it do?',
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

// how the backend words an approval in an outcome (ws_notify.go: "approved by your person[, for an hour | , from now on]")
const HOW = [[/approved by your person, for an hour/, 'Allowed for an hour by you'], [/approved by your person, from now on/, 'Allowed always by you'], [/approved by your person/, 'Allowed once by you']];
const KIND_WORD = { call: 'change something in', session: 'start a tile agent in', template: 'create a tile', changeset: 'propose admin changes' };

/**
 * requestCard(r, now, tileName): what a request's card says — {head, what,
 * glyph, tone, waits, settled, line, result, button, kind}. While it waits:
 * what, for which tile, how long it has, and Review… (the chrome opens it;
 * the page never draws an Allow). Settled: who allowed it, what ran, the
 * result — or Denied / Expired / Withdrawn.
 */
export function requestCard(r, now = Date.now(), tileName = (p) => p) {
  const tile = r.tile ? tileName(r.tile) || r.tile : '';
  const kind = r.kind || 'call';
  const local = !r.ref || /^(brief|resume):/.test(r.ref);
  const what = r.title || (kind === 'template' ? 'Create a tile' : `${KIND_WORD[kind] || 'a request'}${tile ? ' ' + tile : ''}`);
  const call = r.method && r.path ? `${r.method} ${r.path}` : '';
  const out = { kind, tile, what, call, local, ref: r.ref || '', waits: false, settled: true, button: '', glyph: 'shield', tone: '', head: '', line: '', result: '' };
  const left = r.createdMs ? 24 * 3600e3 - (now - r.createdMs) : 0;
  switch (r.state) {
    case 'pending':
      return { ...out, waits: true, settled: false, head: 'Waiting for your approval', tone: 'warn', button: local ? '' : W.review,
        line: [r.createdMs ? `asked ${clock(r.createdMs)}` : '', left > 0 ? `expires in ${inWords(left)}` : ''].filter(Boolean).join(' · ') };
    case 'proposed':
      return { ...out, waits: true, settled: false, head: r.title || 'Admin changes proposed', tone: 'warn', button: W.review, line: r.createdMs ? `proposed ${clock(r.createdMs)}` : '' };
    case 'approved': case 'running': case 'applying':
      return { ...out, head: 'Allowed by you', glyph: 'clock', line: 'running now…' };
    case 'used': {
      const how = (HOW.find(([re]) => re.test(r.outcome || '')) || [null, 'Allowed by you'])[1];
      return { ...out, head: how, glyph: 'check', tone: 'ok', line: [r.decidedMs ? clock(r.decidedMs) : '', call].filter(Boolean).join(' · '), result: r.result ? firstLine(r.result) : resultOf(r.outcome) };
    }
    case 'failed':
      return { ...out, head: 'Allowed by you — it failed', glyph: 'error', tone: 'bad', line: [r.decidedMs ? clock(r.decidedMs) : '', call].filter(Boolean).join(' · '), result: failureOf(r.outcome) };
    case 'denied': return { ...out, head: 'Denied by you', glyph: 'xmark', line: r.decidedMs ? clock(r.decidedMs) : '' };
    case 'expired': return { ...out, head: 'Expired — not approved within 24 hours.', glyph: 'clock', button: 'Ask again' };
    case 'withdrawn': return { ...out, head: 'Withdrawn by the agent', glyph: 'xmark' };
  }
  // a change set's end (applied, partial, rejected, …): xbin's word for it, and the backend's one line of what came of it
  // (`result`: "2 of 3 changes applied")
  return { ...out, head: r.state ? `Decided — ${r.state}` : 'Decided', line: r.decidedMs ? clock(r.decidedMs) : '', result: r.result ? firstLine(r.result) : '' };
}

// what ran, from the outcome the backend wrote for the agent: "— it ran: <the tile's answer>"
function resultOf(outcome) {
  const s = String(outcome || '');
  const i = s.indexOf(' — it ran: ');
  if (i >= 0) return firstLine(s.slice(i + 11));
  const t = s.indexOf(' — tile agent #');
  if (t >= 0) return firstLine(s.slice(t + 3).replace(/\. It is your person's session.*$/s, ''));
  const c = s.indexOf(' — the tile ');
  return c >= 0 ? firstLine(s.slice(c + 3).replace(/\. It is your person's\..*$/s, '')) : '';
}
const failureOf = (outcome) => firstLine(String(outcome || '').replace(/^approved by your person[^,]*, but /, ''));
const firstLine = (s) => { const l = String(s || '').trim().split('\n')[0]; return l.length > 200 ? l.slice(0, 199) + '…' : l; };

// ---- the brief ("scope, then hand off") --------------------------------------------------

export const BRIEF_FIELDS = Object.freeze([['goal', 'Goal'], ['doneWhen', 'Done when'], ['leaveAlone', 'Leave alone'], ['checkBy', 'Check by']]);

/**
 * briefCard(b, tileName, sd): {title, fields, editable, button, state,
 * runsOn, harnesses, picked, note}. sd is the backend's start-defaults for
 * the brief (GET …/briefs/{bid}/start-defaults: what the start would run
 * on, from xbind — {line, harness, harnesses: [{id, name, asks, net,
 * needsTicket}], ticket}). The one button is the backend's: "Review and
 * start…" (xbind's approval opens) or, where the person's preferences let
 * this start through without a ticket (`ticket: false`), "Start" — the
 * click is then the confirmation. The brief is what the tile agent is told
 * first; what the agent sends it later is not approved one by one.
 */
export function briefCard(b, tileName = (p) => p, sd = null) {
  const tile = tileName(b.tile) || b.tile;
  const st = b.state || 'draft';
  const words = { draft: '', asked: 'Waiting for your approval', started: 'Started', denied: 'Denied by you', replaced: 'Replaced by a newer brief' };
  const harnesses = (sd && sd.defaults && Array.isArray(sd.defaults.harnesses) ? sd.defaults.harnesses : []).map((h) => ({ id: h.id, name: h.name || harness(h.id) }));
  const runsOn = sd ? sd.line || '' : [b.harness ? harness(b.harness) : ''].filter(Boolean).join('');
  const free = !!sd && sd.ticket === false && !!sd.harness;
  return {
    title: `Brief for ${possessive(tile)} agent`, tile, state: st, status: words[st] ?? st,
    fields: BRIEF_FIELDS.map(([k, label]) => ({ key: k, label, value: b[k] || '' })),
    editable: st === 'draft' || st === 'asked',
    button: st === 'draft' ? (free ? W.start : W.reviewStart) : st === 'asked' ? W.review : '',
    runsOn, harnesses, picked: (sd && sd.harness) || b.harness || '', none: !!sd && !sd.harness,
    note: `This is what the tile agent is told first. What your agent sends it later isn’t approved message by message${sd && sd.asks === false ? ', and in this mode it acts without asking you.' : ': the tile agent asks you before it acts.'}`,
    edited: !!b.edited,
  };
}

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

// ---- refusals said in a tool's answer (ws_xbin.go wsRefusal) -------------------------------

/**
 * refusalOf(text): what a tool's answer says xbin refused, as the card the
 * page shows — {kind: 'policy' | 'cannot' | 'hidden' | 'slow', reason} — or
 * null. The phrases are the backend's (wsRefusal); a node test pins them.
 */
export function refusalOf(text) {
  const s = String(text || '');
  let m;
  if ((m = /refused for this tile — (.+?)\. This is a rule/s.exec(s))) return { kind: 'policy', reason: m[1].trim(), private: /private/i.test(m[1]) };
  if ((m = /an agent can't do this, whatever is approved — (.+?)\. Tell your person/s.exec(s))) return { kind: 'cannot', reason: m[1].trim() };
  if (/this tile is outside what your person let this session see/.test(s)) return { kind: 'hidden', reason: '' };
  if (/too many actions in a minute/.test(s)) return { kind: 'slow', reason: '' };
  return null;
}

// ---- the context chip ---------------------------------------------------------------------

/**
 * aboutLine(ctx) / splitAbout(text): what the chip adds to the message it
 * goes with — one line before the person's words, "About tile "Orders"
 * (sales/orders):" or "About the screen "Sales desk":" — and the message
 * split back into the chip and the words (the transcript shows the chip
 * above them). The backend's message route takes text only, so the line is
 * the context the agent reads.
 */
export function aboutLine(c) {
  if (!c) return '';
  const q = (s) => String(s || '').replace(/["\n]/g, '’').slice(0, 200);
  if (c.tile) return `About tile "${q(c.name || c.tile)}" (${String(c.tile).replace(/[()\n]/g, '')}):`;
  if (c.screen) return `About the screen "${q(c.screen)}":`;
  return '';
}
const ABOUT = /^About (?:tile "([^"\n]{1,200})" \(([^()\n]{1,512})\)|the screen "([^"\n]{1,200})"):\n/;
export function splitAbout(text) {
  const s = String(text || '');
  const m = ABOUT.exec(s);
  if (!m) return { about: null, text: s };
  return { about: m[1] ? { tile: m[2], name: m[1] } : { screen: m[3] }, text: s.slice(m[0].length) };
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
  if (s.status === 'waiting_input' || s.requests > 0) status = { glyph: 'bell', word: 'waiting for you', tone: 'warn' };
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
export function chipWords(counts, model, narrow = false, priv = false) {
  const c = counts || {};
  const t = c.tiles || 0, o = c.orgs || 0, n = c.notConnected || 0;
  const m = short(model) + (priv ? ' · private' : '');
  const conn = narrow ? `${t} connected` : `${t} tile${t === 1 ? '' : 's'} · ${o} org${o === 1 ? '' : 's'}`;
  const not = n ? (narrow ? `${n} not` : `${n} not connected`) : '';
  const label = `Scope: ${t} tile${t === 1 ? '' : 's'} in ${o} org${o === 1 ? '' : 's'} connected${n ? `, ${n} not connected` : ''}. Model ${m || 'not set'}.`;
  return { conn, not, model: m, label, summary: c.summary || '' };
}

// ---- the page's states (docs/workspace-agent.md §The agent's page) ------------------------------------------

/**
 * quotaOf(state, run): a stopping quota that holds the agent — from the
 * state's `quota` when the backend says it ({stopped, resets, limit,
 * used}), else from the run's last error, which carries the connector's
 * code (xbin_llm_quota_exceeded, "resets …") — as {resets, words, until}; null when none.
 */
export function quotaOf(state, run) {
  const q = state && state.quota;
  let resets = 0, limit = '';
  if (q && q.stopped) { resets = Date.parse(q.resets) || 0; limit = q.limitWords || ''; }
  else {
    const err = String((run && run.status === 'error' && (run.result || run.error)) || '');
    if (!/quota/i.test(err) || !/(exceeded|insufficient_quota|used up|reached)/i.test(err)) return null;
    const m = /resets?(?: at)?[ "=:]+(\d{4}-\d\d-\d\dT[\d:.]+Z?)/i.exec(err);
    resets = m ? Date.parse(m[1]) || 0 : 0;
  }
  const at = resets ? clock(resets) : '';
  return {
    resets, until: at ? `Sending resumes at ${at}` : 'Sending resumes when the limit resets',
    words: `${limit ? `You've used ${limit}. ` : ''}${at ? `It resets at ${at}. ` : ''}You can still read everything here; tile agents on your own sign-in keep working.`,
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
