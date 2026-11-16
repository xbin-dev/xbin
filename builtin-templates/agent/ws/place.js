// ws/place.js — where the workspace agent's own cards sit in its
// transcript. The conversation is the template's run view, folded as ever
// (model/fold.js); what the agent started or asked for lives beside it, in
// the session's cards (GET /ws/sessions/{id}/cards: requests, tile agents,
// briefs, the plan). compose() puts each card where the conversation made
// it — in place of the tool call that made it (the card's callId), after
// the request that started it (a tile agent's `request`), or after the
// line it follows in time — and says which rows the cards make
// redundant (the journal's "asked: …" beside a question card, a plan's
// later updates). The window of
// rows stays the fold's (a row may draw several cards, or nothing), so the
// session's paging is untouched. Pure: node-tested.
import { planBlock, refusalCard, clock } from './words.js';

/** UPDATES: how the backend heads what xbin and the tile agents report (coord_workspace.go wsUpdatesHead). */
export const UPDATES = '[workspace updates — ';

// a small stable hash of a value: a card's version moves when what it shows does
function hash(v) {
  const s = JSON.stringify(v) || '';
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h * 33) ^ s.charCodeAt(i)) >>> 0;
  return h;
}
/** host(type, k, data): a host block of the kit (kind 'host'), versioned by its data. */
export const host = (type, k, data, title = '') => ({ k, v: hash([type, data]), kind: 'host', type, data, title });

const num = (re, s) => { const m = re.exec(String(s || '')); return m ? +m[1] : 0; };
const parse = (s) => { try { const v = JSON.parse(s || '{}'); return v && typeof v === 'object' ? v : {}; } catch { return {}; } };

/**
 * askOf(b, ask): an ask_user call's question in fields — {kind: choices |
 * redirect | cant | question, question, choices, tile} — from the cards'
 * `ask` when it is this call's, else from the call's own arguments; an
 * unknown kind is read as choices (API.md §Workspace mode, "In fields").
 */
export function askOf(b, ask) {
  const a = ask && ask.callId && ask.callId === b.callId ? ask : parse(b.args);
  const choices = (Array.isArray(a.choices) ? a.choices : []).map((c) => String(c || '').trim()).filter(Boolean).slice(0, 4);
  let kind = ['choices', 'redirect', 'cant', 'question'].includes(a.kind) ? a.kind : 'choices';
  if (kind === 'choices' && !choices.length) kind = 'question';
  return { kind, question: String(a.question || ''), choices, tile: kind === 'redirect' ? String(a.tile || '').replace(/^\/+|\/+$/g, '') : '', callId: b.callId || '' };
}

/**
 * askBlock(b, answer, live): an ask_user call (its question and choices)
 * as the kit's ask block — a form of the choices with "Something else"
 * riding on it when it has choices, else its words alone (the next message
 * answers it). answer: the person's next message ('' none yet); live: it
 * waits now.
 */
export function askBlock(b, answer, live, words, q = askOf(b, null)) {
  const choices = q.choices;
  const k = 'ws:ask:' + (b.callId || b.id);
  const base = { k, v: hash([choices, q.question, answer, live]), kind: 'ask', eid: '', callId: b.callId || '', message: q.question, park: '' };
  const settled = !live && answer != null;
  if (!choices.length) {
    return { ...base, mode: 'text', schema: null, action: settled ? 'accept' : !live ? 'cancel' : null, by: settled ? 'you' : !live ? 'agent' : null, content: null };
  }
  const schema = { type: 'object', properties: {
    answer: { type: 'string', title: '', enum: choices },
    other: { type: 'string', title: words.somethingElse, description: words.somethingElseHint, _meta: { _askUserQuestionCustomAnswer: { questionId: 'answer' } } },
  } };
  const picked = settled ? (choices.includes(answer) ? { answer } : { other: answer }) : null;
  return { ...base, mode: 'form', schema, submit: words.answer, skip: words.skip,
    action: settled ? 'accept' : !live ? 'cancel' : null, by: settled ? 'you' : !live ? 'agent' : null, content: picked };
}

/**
 * compose(blocks, cards, run, opts) → {at, tail, live, plan}:
 *   at    fold block id → {hide, replace: [block], after: [block], rev}
 *   tail  cards with no line held to sit after (the transcript is paged, or
 *         empty): drawn at its end
 *   live  what waits in the conversation: {ask: the question's block,
 *         brief: a brief waits for the person}
 *   plan  the plan block (null: none), with waitsForGo
 * blocks are the fold's (model/fold.js: k, id, name, callId, args, result,
 * created, text); cards GET /ws/sessions/{id}/cards; run the run (status,
 * pendingState); opts {words, goAheadAt} — W of words.js, and (for a test)
 * the time the person said go ahead, which the plan's meta line says.
 */
export function compose(blocks, cards, run, opts = {}) {
  const words = opts.words || {};
  const c = cards || {};
  const at = new Map();
  const tail = [];
  const slot = (id) => { let e = at.get(id); if (!e) at.set(id, (e = { hide: false, replace: null, after: [] })); return e; };
  const list = blocks || [];
  const r = run || {};

  // times: a block without one has the time of the line before it
  const times = new Map();
  let t = 0;
  for (const b of list) { if (b.created) t = b.created * 1000; times.set(b.id, t); }
  const anchorAt = (ms) => {
    let id = null;
    for (const b of list) { if (b.k === 'draft' || String(b.id).startsWith('draft-')) continue; if (times.get(b.id) <= ms) id = b.id; else if (times.get(b.id)) break; }
    return id;
  };
  // where each card goes: an anchor id (replace or after), or the tail
  const place = (blk, anchor, how = 'after') => {
    if (!anchor) { tail.push(blk); return; }
    const e = slot(anchor);
    if (how === 'replace') (e.replace ||= []).push(blk);
    else e.after.push(blk);
  };

  const tools = list.filter((b) => b.k === 'tool');
  const byCall = new Map(tools.map((b) => [b.callId, b]));

  // the plan: in place of its first call, its later calls hidden
  let plan = null;
  const steps = Array.isArray(c.plan) ? c.plan : [];
  const planCalls = tools.filter((b) => b.name === 'plan');
  if (steps.length) {
    // "you said go ahead · 10:02": the person's first go-ahead after the plan was shown
    const from = planCalls.length ? list.indexOf(planCalls[0]) : 0;
    const go = list.slice(from).find((x) => x.k === 'user' && /^\s*go ahead\b/i.test(x.text || ''));
    const goAt = opts.goAheadAt || (go && times.get(go.id) ? clock(times.get(go.id)) : go ? 'earlier' : '');
    plan = planBlock(steps, 'ws:plan', goAt ? `you said go ahead · ${goAt}` : '');
    const busy = ['running', 'awaiting', 'sleeping'].includes(r.status);
    const blks = [plan.block, ...(plan.waitsForGo && !busy ? [host('replies', 'ws:go', { choices: [{ label: words.goAhead || 'Go ahead', send: words.goAhead || 'Go ahead', primary: true }, { label: words.changePlan || 'Change the plan', fill: (words.changePlan || 'Change the plan') + ': ' }] })] : [])];
    if (planCalls.length) for (const blk of blks) place(blk, planCalls[0].id, 'replace');
    else tail.push(...blks);
  }
  for (const b of planCalls.slice(plan && planCalls.length ? 1 : 0)) slot(b.id).hide = true;

  // questions (ask_user): the kit's question card in place of the call; the journal's "asked: …" goes
  // the newest question nobody answered yet waits — whatever the run does meanwhile, a message answers it;
  // an older one a newer question replaced is over
  // A question's kind is a field (cards.ask for the newest, the call's own `kind` before it): choices, a redirect
  // (the request was about code: the tile's own agent, or the outcome), can't (the nearest things it can), or a
  // question with no choices.
  let live = null;
  const asks = tools.filter((b) => b.name === 'ask_user');
  const person = (x) => x.k === 'user' && !String(x.text || '').startsWith(UPDATES);
  for (const b of asks) {
    const i = list.indexOf(b);
    const next = list.slice(i + 1).find(person);
    const isLast = b === asks[asks.length - 1];
    const waits = isLast && !next;
    const q = askOf(b, c.ask);
    if (q.kind === 'redirect' || q.kind === 'cant') {
      // the person's own words, as they wrote them: what a redirect hands to the tile's agent
      const before = list.slice(0, i).reverse().find(person);
      const blk = host(q.kind, 'ws:ask:' + (b.callId || b.id), { ...q, draft: before ? before.text : '', answer: next ? next.text : null, waits });
      place(blk, b.id, 'replace');
      if (waits) live = blk;
      continue;
    }
    const ab = askBlock(b, next ? next.text : null, waits, words, q);
    place(ab, b.id, 'replace');
    if (!ab.action && ab.mode) live = ab;
  }
  for (const b of list) if (b.k === 'step' && b.kind === 'ask' && (!b.detail || b.detail.kind === 'ask_user' || !b.detail.kind)) slot(b.id).hide = true;

  // briefs: in place of the tile_agent_brief call that wrote them (their callId), else by time
  const briefAt = new Map(); // brief id → its card's anchor
  const briefs = Array.isArray(c.briefs) ? c.briefs : [];
  for (const br of briefs) {
    const call = br.callId && byCall.get(br.callId);
    const blk = host('brief', 'ws:brief:' + br.id, br);
    if (call) { place(blk, call.id, 'replace'); briefAt.set(br.id, call.id); } else { const a = anchorAt(br.createdMs || 0); place(blk, a); briefAt.set(br.id, a); }
  }

  // requests: in place of the tool call that filed them (their callId), else after their brief's card (the brief's
  // Start filed it), else by time
  const reqAt = new Map();
  const reqs = (Array.isArray(c.requests) ? c.requests : []).slice().sort((a, b) => (a.createdMs || 0) - (b.createdMs || 0) || a.id - b.id);
  for (const q of reqs) {
    const blk = host('request', 'ws:req:' + q.id, q);
    const call = q.callId && byCall.get(q.callId);
    if (call && !(at.get(call.id) && at.get(call.id).replace)) { place(blk, call.id, 'replace'); reqAt.set(q.id, call.id); continue; }
    const a = call ? call.id : q.brief && briefAt.has(q.brief) ? briefAt.get(q.brief) : anchorAt(q.createdMs || 0);
    place(blk, a);
    reqAt.set(q.id, a);
  }

  // tile agents: after the request that started them — by its id (the agent's `request`), else after the
  // tile_agent_start call that filed it (the agent's `callId`: the request may be off the list of the newest) —,
  // else their brief's card, else by time. A row from before those fields names neither: its request is then
  // found as it was before them, by its outcome's words ("tile agent #N started") or the brief it started from.
  for (const a of Array.isArray(c.agents) ? c.agents : []) {
    const blk = host('wsession', 'ws:agent:' + a.id, a);
    const call = a.callId && byCall.get(a.callId);
    const q = a.request ? reqs.find((x) => x.id === a.request)
      : a.callId ? null
      : reqs.find((x) => num(/tile agent #(\d+) started/, x.outcome) === a.id) || reqs.find((x) => x.kind === 'session' && a.brief && x.brief === a.brief && x.state === 'used');
    const anchor = q ? reqAt.get(q.id) : call ? call.id : a.brief && briefAt.has(a.brief) ? briefAt.get(a.brief) : anchorAt(a.startedMs || 0);
    place(blk, anchor);
  }

  // what xbin refused (cards.refusals: the call it answers, its kind): a card after the call, else by time
  for (const f of (Array.isArray(c.refusals) ? c.refusals : []).slice().reverse()) {
    const call = f.callId && byCall.get(f.callId);
    place(host('refusal', 'ws:ref:' + (f.id || f.callId), f), call ? call.id : anchorAt(f.createdMs || 0));
  }

  for (const [, e] of at) e.rev = [e.hide ? 'h' : '', ...(e.replace || []).map((x) => x.k + '@' + x.v), ...e.after.map((x) => x.k + '@' + x.v)].join(',');
  const briefWaits = briefs.some((b) => b.state === 'draft' || b.state === 'asked');
  return { at, tail, live: { ask: live, brief: briefWaits }, plan };
}
