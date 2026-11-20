/**
 * agent-start-words.js — one approval, one place (D250): what xbin's chrome
 * says, and decides by, for a tile agent's start that the person picks on
 * the sheet, a message to a tile agent that acts without asking, and the
 * one batch of everything that waits (/vendor/agent-start-words.js). Pure:
 * no DOM, no lit — bx-agent-approve.js and bx-agent-panels.js draw what
 * these return, node tests hold them (hack/agent-start-words.test.mjs) and
 * the app's port is held to them by vectors (native/tools/delegation-words.mjs).
 *
 * A fact is never joined to another with "·" (the owner's rule): the
 * functions return lists of facts (`parts`, `chips`), each drawn as an
 * element of its own.
 *
 * Everything is read from xbind's answers (docs/protocol/delegation.md):
 * a start's preview.session — what it opens with, `choices` (what may be
 * picked), `from` (where each part came from), `suggested` and
 * `modeNotTaken` (the agent's suggestion), `continue` (the agent's own idle
 * session the start would continue) — and a message's preview.message.
 */
import { needTitle, asked as askedJoined, left, toMs, clock, needChangeTitle, sessionRows } from './agent-chrome-words.js';

/** FROM: where a start's choice came from, as the sheet says it under each picker. */
export const FROM = Object.freeze({
  tile: 'remembered for this tile', preference: 'your preference', agent: 'suggested by your agent', default: 'the default', you: 'your pick',
});

/** askedParts(x, now): when it was asked and when it expires — two facts, never one joined line ([] for none). */
export function askedParts(x, now = Date.now()) {
  const c = toMs(x && x.created), e = toMs(x && x.expires);
  return [c ? `asked ${clock(c)}` : '', e ? (e > now ? `expires in ${left(e - now)}` : 'expired') : ''].filter(Boolean);
}
export { askedJoined };

const sess = (req) => (req && req.preview && req.preview.session) || {};

/**
 * pickOf(req): the sheet's first pick for a start — what xbind preselected
 * (the coding agent, its model and mode), continuing the session there when
 * the start offers one; remember unticked. The person changes it on the sheet.
 */
export function pickOf(req) {
  const s = sess(req);
  const c = s.continue && s.continue.session ? s.continue : null;
  // a start that would continue the agent's own idle session there: continuing it is the first choice — that session's coding agent
  return { harness: String((c && c.provider) || s.provider || ''), model: String(s.model || ''), mode: String(s.mode ?? ''), remember: false, cont: !!c, changed: [] };
}

/** contChoice(pick, req, cont): the person continues the session there (true), or starts a new one — on what xbind preselected for a new one. */
export function contChoice(pick, req, cont) {
  const s = sess(req);
  if (cont && s.continue && s.continue.session) return { ...pick, cont: true, harness: s.continue.provider, changed: pick.changed.filter((f) => f !== 'harness') };
  return { ...pick, cont: false, harness: String(s.provider || ''), model: String(s.model || ''), mode: String(s.mode ?? ''), changed: [] };
}

const harnessesOf = (s) => (s.choices && Array.isArray(s.choices.harnesses) ? s.choices.harnesses : []);

/**
 * picked(req, pick): the start as the person has picked it so far — the
 * request with its preview.session drawn for that pick (the coding agent's
 * name, the mode and whether it asks, the model, and THAT coding agent's
 * sign-in offer), so every row of the sheet says what will run. A request
 * without choices (an older xbind's) is itself.
 */
export function picked(req, pick) {
  const s = sess(req);
  const c = s.continue && s.continue.session ? s.continue : null;
  if (c && pick.cont && pick.harness === c.provider) {
    // continuing the session there: its own sign-in is what a tick on this approval is for (D250 review L1)
    return { ...req, preview: { ...req.preview, session: { ...s, signInId: c.signInId || '', signInTrust: null } } };
  }
  const h = harnessesOf(s).find((x) => x.id === pick.harness);
  if (!h) return req;
  const mode = (h.modes || []).find((m) => m.id === pick.mode);
  const same = h.id === s.provider;
  return { ...req, preview: { ...req.preview, session: { ...s, provider: h.id, providerName: h.name, mode: pick.mode, asks: !!(mode && mode.asks), model: pick.model, net: h.net ?? s.net,
    signIn: h.signIn ?? (same ? s.signIn : ''), signInId: h.signInId || (same ? s.signInId || '' : ''), signInTrust: h.signInTrust || (same ? s.signInTrust || null : null) } } };
}

/**
 * startView(req, pick): the sheet's choosing part for a start —
 *   pickers   [{id: 'harness' | 'model' | 'mode', label, value, options: [{value, label}], from, note, warn}]
 *             (none while it continues a session there: that session keeps its own)
 *   cont      {session, label, sub, chips} — the session the start would continue, or null
 *   continuing  the person continues it
 *   remember  {shown, label, note}
 *   danger    a sentence when what is picked acts without asking ('' when it asks)
 *   asks      the picked mode asks before acting
 *   label     the Allow button's words: Start | Start without asking | Continue
 *   details   the call, for the sheet's Details: "POST /api/xbin/term/sessions"
 */
/**
 * rememberWords: "Remember for this tile" as the sheet offers it (D250
 * review M1) — it keeps only what the person changed here, and never a mode
 * that acts without asking, nor a coding agent with no mode that asks.
 */
function rememberWords(continuing, hs, h, pick) {
  const vouched = !!(h && (h.modes || []).some((m) => m.asks));
  const mode = h && (h.modes || []).find((m) => m.id === pick.mode);
  const note = !vouched ? `${h ? h.name : 'This coding agent'} has no mode that asks before it acts: nothing of it is remembered, you pick it on each approval.`
    : mode && !mode.asks ? 'This mode acts without asking: it isn\'t remembered. Only the coding agent and model you changed are.'
      : 'Your next start here preselects what you changed. A mode that acts without asking is never remembered.';
  return { shown: !continuing && hs.length > 0 && vouched, label: 'Remember for this tile', note, keepsMode: !!(mode && mode.asks) };
}

export function startView(req, pick) {
  const s = sess(req), from = s.from || {}, sug = s.suggested || {};
  const hs = harnessesOf(s);
  const h = hs.find((x) => x.id === pick.harness) || null;
  const c = s.continue && s.continue.session ? s.continue : null;
  const continuing = !!(c && pick.cont && pick.harness === c.provider);
  const changed = (f) => pick.changed.includes(f);
  const fromWords = (f) => FROM[changed(f) ? 'you' : from[f]] || '';
  const pickers = [];
  if (!continuing && hs.length) {
    const hNot = s.harnessNotTaken && pick.harness !== s.harnessNotTaken ? hs.find((x) => x.id === s.harnessNotTaken) : null;
    pickers.push({ id: 'harness', label: 'Coding agent', value: pick.harness, from: fromWords('harness'),
      options: hs.map((x) => ({ value: x.id, label: x.id === sug.harness && x.id !== pick.harness ? `${x.name} (suggested by your agent)` : x.name })),
      // a coding agent with no mode that asks (OpenCode, D250 review M2): the agent's suggestion of it is said, never taken
      warn: hNot ? `Your agent suggested ${hNot.name}, which acts without asking in every mode. It isn't picked for you: pick it yourself if you want it.` : '' });
    if (h) {
      const models = (h.models || []).map((m) => ({ value: m.id, label: !m.id ? 'its default' : m.from === 'agent' ? `${m.id} (suggested by your agent)` : m.id }));
      pickers.push({ id: 'model', label: 'Model', value: pick.model, from: fromWords('model'), options: models,
        note: h.modelsKnown ? '' : `${h.name}'s own list shows in its session once it runs: these are the models xbin knows of it.` });
      const notTaken = s.modeNotTaken && h.id === s.provider && pick.mode !== s.modeNotTaken ? s.modeNotTaken : '';
      pickers.push({ id: 'mode', label: 'Mode', value: pick.mode, from: fromWords('mode'),
        options: (h.modes || []).map((m) => ({ value: m.id, label: `${m.name || m.id || 'its own default'}, ${m.asks ? 'asks before acting' : m.explicit ? 'acts without asking (caution)' : 'acts without asking'}` })),
        note: h.defaultMode != null && pick.mode === h.defaultMode ? 'The default: in it, it asks you before it edits a file or runs a command, so nothing your agent tells it acts unseen.' : '',
        warn: notTaken ? `Your agent suggested ${notTaken}, which acts without asking. It isn't picked for you: pick it yourself if you want it.` : '' });
    }
  }
  const p = picked(req, pick);
  const ps = sess(p);
  const asks = continuing ? c.asks === true : ps.asks === true;
  const who = continuing ? c.providerName || c.provider : ps.providerName || ps.provider || 'The coding agent';
  const tile = (req && req.tile) || s.tile || '';
  const tileName = String(tile).split('/').filter(Boolean).pop() || 'the tile';
  return {
    pickers,
    cont: c ? { session: c.session, label: `Continue the ${c.providerName || c.provider} session${c.started ? ` from ${clock(toMs(c.started))}` : ''} with this follow-up`,
      sub: 'Start a new session instead', chips: [c.providerName || c.provider, c.model ? `model ${c.model}` : '', c.asks ? 'asks before acting' : 'acts without asking',
        c.signInId ? 'your saved sign-in' : ''].filter(Boolean),
      // it carries the person's saved sign-in: a follow-up into it uses it — their tick, each time (D230, D250 review L1)
      signIn: c.signInId ? { id: c.signInId, name: c.signInName || '', warn: 'This session carries your saved sign-in: a follow-up into it uses it, and the coding agent can read it.',
        line: 'Use my sign-in for this follow-up', untickedNote: 'Tick it to continue this session, or start a new session instead: a new session has no sign-in of yours.' } : null } : null,
    continuing,
    needsTick: !!(continuing && c.signInId),
    remember: rememberWords(continuing, hs, h, pick),
    danger: asks ? '' : `${who} in this mode acts without asking: it edits files and runs commands in ${tileName} without asking you. You will not get a chance to stop a single edit or command.`,
    asks,
    label: continuing ? 'Continue' : asks ? 'Start' : 'Start without asking',
    details: `${req.method || 'POST'} ${req.target || '/api/xbin/term/sessions'}`,
  };
}

/** choose(pick, req, field, value): the pick after the person changed one picker — a coding agent changed resets its model and mode to its own defaults. */
export function choose(pick, req, field, value) {
  const next = { ...pick, changed: [...new Set([...pick.changed, field])] };
  if (field === 'harness') {
    const h = harnessesOf(sess(req)).find((x) => x.id === value);
    next.harness = value;
    if (h) { next.model = ''; next.mode = h.defaultMode ?? ''; next.changed = [...new Set([...next.changed, 'model', 'mode'])]; }
  } else if (field === 'model') next.model = value;
  else if (field === 'mode') next.mode = value;
  next.remember = true; // a pick of their own: offered to keep — they can untick it
  return next;
}

/**
 * pickBody(req, pick): what the approval sends of the pick (POST …/approve):
 * {harness, model, mode, remember?, continue: false?, knows: ['continue']} —
 * only for a start with choices; {} for anything else, and for an older
 * xbind's start (it takes no pick).
 */
export function pickBody(req, pick) {
  const s = sess(req);
  if (req.kind !== 'session' || !harnessesOf(s).length) return {};
  // seen: what this sheet shows of what xbind resolves again when it is decided — the sign-in it offers, the
  // network — held to it there: a change since is refused, nothing decided, and the sheet looks again (D250 review L2)
  const ps = sess(picked(req, pick));
  const out = { knows: ['continue'], seen: { signInId: String(ps.signInId || ''), net: String(ps.net || '') } };
  const c = s.continue && s.continue.session ? s.continue : null;
  if (c && pick.cont && pick.harness === c.provider) return out; // continuing: the session keeps its own
  if (c) out.continue = false;
  Object.assign(out, { harness: pick.harness, model: pick.model, mode: pick.mode });
  if (pick.remember) out.remember = true;
  return out;
}

/**
 * startChips(req): a pending start as the batch's chips — the coding agent
 * it would run on and whether it asks ([] for anything else).
 */
export function startChips(req) {
  if (!req || req.kind !== 'session') return [];
  const s = sess(req);
  if (s.continue && s.continue.session) return [`continues ${s.continue.providerName || s.continue.provider}`];
  return [s.providerName || s.provider || '', s.model ? `model ${s.model}` : '', s.asks === true ? 'asks before acting' : 'acts without asking'].filter(Boolean);
}

// ---- a message to a tile agent that acts without asking ---------------------------------------------

/** messageOf(req): a message request as xbind previews it — {session, provider, name, mode, model, text}. */
export function messageOf(req) {
  const m = (req && req.preview && req.preview.message) || {};
  return { session: String(m.session || ''), provider: String(m.providerName || m.provider || 'the tile agent'), mode: String(m.mode || ''), model: String(m.model || ''), text: String(m.text ?? '') };
}

/** messageView(req): the sheet's rows for a message — {heading, agent: [chips], text, why, label}. */
export function messageView(req) {
  const m = messageOf(req);
  const tile = String((req && req.tile) || '').split('/').filter(Boolean).pop() || 'the tile';
  return {
    heading: { before: `Send this message to the ${m.provider} session in `, strong: tile, after: '?' },
    title: `Send a message to ${m.provider} in ${tile}`,
    agent: [m.provider, m.mode ? `mode ${m.mode}` : '', 'acts without asking', m.model ? `model ${m.model}` : ''].filter(Boolean),
    text: m.text,
    why: `This session acts without asking, so what your workspace agent tells it is an act of yours to approve. Allowing sends exactly this text, once, to this session; it is refused if the session's mode or coding agent changed since.`,
    label: 'Send it',
  };
}

// ---- the one batch ------------------------------------------------------------------------------

/**
 * batch({requests, failed, changesets, sessions}, now): everything that
 * waits for the person, ONE row a pending act, oldest first — {kind:
 * 'request' | 'retry' | 'changeset' | 'session', id, title, chips, parts,
 * action, review, permits?, agentName?, at, waits?}. review: it opens a
 * review that Review all walks (a request's sheet, a plan's review); retry:
 * a start that was approved and didn't open, which the person tries again.
 */
export function batch({ requests = [], failed = [], changesets = [], sessions = [] } = {}, now = Date.now()) {
  const rows = [];
  for (const r of requests) {
    if (r.state !== 'pending') continue;
    const title = r.kind === 'message' ? messageView(r).title : needTitle(r);
    rows.push({ kind: 'request', id: r.id, title, chips: startChips(r), parts: askedParts(r, now), action: 'Review…', review: true, at: toMs(r.created) });
  }
  for (const r of failed) {
    if (r.state !== 'failed' || !(r.failure && r.failure.retry)) continue;
    rows.push({ kind: 'retry', id: r.id, title: `Didn't start: ${needTitle(r)}`, chips: [], parts: [String(r.failure.why || ''), 'approved by you'].filter(Boolean), action: 'Try again', review: false, at: toMs(r.created) });
  }
  for (const c of changesets) {
    for (const it of c.state === 'proposed' ? [] : c.items || []) {
      if (it.result !== 'waiting' || !it.wait) continue;
      const u = toMs(it.wait.until);
      rows.push({ kind: 'changeset', id: c.id, waits: true, title: `Waiting: ${it.line || 'a line of your plan'}`, chips: [], review: false, action: 'Open', at: toMs(c.decided || c.created),
        parts: [it.wait.held ? `not bound: ${it.wait.held}` : `xbin applies it once ${it.wait.tile} provides ${it.wait.service}`, u > now ? `lapses in ${left(u - now)}` : ''].filter(Boolean) });
    }
    if (c.state !== 'proposed') continue;
    const w = needChangeTitle(c), e = toMs(c.expires);
    rows.push({ kind: 'changeset', id: c.id, title: w.title, chips: [], review: true, action: 'Review…', at: toMs(c.created),
      // (the shared words join a proposal's notes with "·": the batch, new, shows each as its own element)
      parts: [...String(w.note || '').split(' · ').filter(Boolean), e > now ? `expires in ${left(e - now)}` : ''].filter(Boolean) });
  }
  for (const s of sessions) {
    if (!(s.pending > 0 || s.questions > 0 || s.status === 'waiting_permission')) continue;
    const t = String(s.cwd || s.tile || '').split('/').filter(Boolean).pop() || 'A tile';
    const name = s.providerName || s.provider || 'the tile agent';
    rows.push({ kind: 'session', id: s.id, tile: s.cwd || s.tile || '', title: `${t[0].toUpperCase()}${t.slice(1)}: ${name} ${s.questions > 0 ? 'asks a question' : 'asks permission'}`,
      chips: s.origin === 'agent' ? ['started by your agent'] : [], parts: [], action: 'Open', review: false, at: toMs(s.waitingSince || s.updated || s.created),
      permits: !(s.questions > 0) && (s.pending > 0 || s.status === 'waiting_permission'), agentName: name });
  }
  return rows.sort((a, b) => a.at - b.at);
}

/** batchTitle(rows): the batch's title and its count, apart: {title: 'Waiting for you', count}. */
export const batchTitle = (rows) => ({ title: 'Waiting for you', count: rows.length, reviewAll: rows.filter((r) => r.review).length > 1 ? 'Review all' : '' });

/** sessionAgentLine(req): sessionRows' agent line for a picked start, unchanged for the rest (re-exported for the sheet). */
export { sessionRows };
