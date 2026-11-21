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
import { needTitle, asked as askedJoined, left, toMs, clock, needChangeTitle, sessionRows, MODE_FACT, reveal } from './agent-chrome-words.js';

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
  const ch = req && req.state === 'failed' && req.chosen;
  if (ch) { // Try again (D250 review L3): the picks that were approved, preselected — never the sign-in tick
    return { harness: String(ch.provider || s.provider || ''), model: String(ch.model || ''), mode: String(ch.mode ?? ''), remember: false, cont: !!ch.continue, changed: [] };
  }
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
    // continuing the session there: its own sign-in, a fact of that session (D251: no tick again) — what seen says
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
 *   asks      the picked mode asks before acting
 *   modeChip  the mode as a fact: 'asks before acting' | 'acts without asking' (D251: never a warning)
 *   fact      the plain line under it — what a mode that acts without asking means ('' when it asks)
 *   label     the Allow button's words: Start | Send (continuing)
 *   details   the call, for the sheet's Details: "POST /api/xbin/term/sessions"
 */
/**
 * rememberWords: "Remember for this tile" as the sheet offers it (D250
 * review M1) — it keeps only what the person changed here: the coding
 * agent, the model, the mode, asking or not (D251) — never a bypass one.
 */
function rememberWords(continuing, hs, h, pick) {
  const mode = h && (h.modes || []).find((m) => m.id === pick.mode);
  const note = mode && mode.explicit ? 'Your next start here preselects the coding agent and model you changed. A mode that skips every check isn\'t remembered: you pick it each time.'
    : 'Your next start here preselects what you changed.';
  return { shown: !continuing && hs.length > 0 && !!h, label: 'Remember for this tile', note, keepsMode: !!(mode && !mode.explicit) };
}

/** modeChip(asks): a mode as the one fact the sheet, the batch and the app show of it (D251). */
export const modeChip = (asks) => (asks ? 'asks before acting' : 'acts without asking');

/**
 * contChips(c): a session the start (or a message) goes to, as its facts —
 * coding agent, model, mode, the sign-in it carries. Which saved sign-in
 * that is, the chrome draws beside them as an element of its own, from the
 * person's own list by its id (signInIdOf).
 */
export function contChips(c) {
  const name = (c && (c.providerName || c.provider)) || 'the tile agent';
  return [name, c && c.model ? `model ${c.model}` : '', modeChip(!!(c && c.asks === true)), c && c.signInId ? `uses your ${name} sign-in` : ''].filter(Boolean);
}

export function startView(req, pick) {
  const s = sess(req), from = s.from || {}, sug = s.suggested || {};
  const hs = harnessesOf(s);
  const h = hs.find((x) => x.id === pick.harness) || null;
  const c = s.continue && s.continue.session ? s.continue : null;
  const continuing = !!(c && pick.cont && pick.harness === c.provider);
  const changed = (f) => pick.changed.includes(f);
  // a field the person changed is theirs; one a coding agent they switched to reset is its default — not theirs (so
  // not what Remember keeps: the server's rule, D250 review M1) and no longer what the first chain said
  const fromWords = (f) => FROM[changed(f) ? 'you' : (pick.defaulted || []).includes(f) ? 'default' : from[f]] || '';
  const pickers = [];
  if (s.resume && h) {
    // a resume keeps its coding agent and model; its mode is picked again — a bypass one it ran in last is never
    // taken: it resumes in its coding agent's work mode, and says what it was in (D251 review M2)
    const m = (h.modes || []).find((x) => x.id === pick.mode);
    pickers.push({ id: 'mode', label: 'Mode', value: pick.mode, from: '',
      options: (h.modes || []).map((x) => ({ value: x.id, label: `${x.name || x.id || 'its own default'}, ${x.explicit ? 'skips every check' : modeChip(x.asks)}` })),
      note: m && m.explicit ? `${m.name || m.id} skips every check ${h.name} has of its own, even the ones it keeps in its other modes.` : '',
      warn: s.resumeModeNotTaken ? `It was in ${s.resumeModeNotTaken}, which skips every check; it resumes in ${s.mode || 'its own default mode'}. Pick it yourself if you want it.` : '' });
  } else if (!continuing && hs.length) {
    pickers.push({ id: 'harness', label: 'Coding agent', value: pick.harness, from: fromWords('harness'),
      options: hs.map((x) => ({ value: x.id, label: x.id === sug.harness && x.id !== pick.harness ? `${x.name} (suggested by your agent)` : x.name })), warn: '' });
    if (h) {
      const models = (h.models || []).map((m) => ({ value: m.id, label: !m.id ? 'its default' : m.from === 'agent' ? `${m.id} (suggested by your agent)` : m.id }));
      pickers.push({ id: 'model', label: 'Model', value: pick.model, from: fromWords('model'), options: models,
        note: h.modelsKnown ? '' : `${h.name}'s own list shows in its session once it runs: these are the models xbin knows of it.` });
      const notTaken = s.modeNotTaken && h.id === s.provider && pick.mode !== s.modeNotTaken ? s.modeNotTaken : '';
      const m = (h.modes || []).find((x) => x.id === pick.mode);
      pickers.push({ id: 'mode', label: 'Mode', value: pick.mode, from: fromWords('mode'),
        options: (h.modes || []).map((x) => ({ value: x.id, label: `${x.name || x.id || 'its own default'}, ${x.explicit ? 'skips every check' : modeChip(x.asks)}` })),
        // a bypass mode is the one still said as more than a fact: it skips the coding agent's own checks too
        note: m && m.explicit ? `${m.name || m.id} skips every check ${h.name} has of its own, even the ones it keeps in its other modes.` : '',
        // the agent's suggestion of a bypass mode, never preselected (only the person names one)
        warn: notTaken ? `Your agent suggested ${notTaken}, which skips every check. It isn't picked for you: pick it yourself if you want it.` : '' });
    }
  }
  const p = picked(req, pick);
  const ps = sess(p);
  const asks = continuing ? c.asks === true : ps.asks === true;
  return {
    pickers,
    // continuing: one plain approval (D251) — the session's facts as chips, the sign-in it carries one of them
    cont: c ? { session: c.session, label: `Continue the ${c.providerName || c.provider} session${c.started ? ` from ${clock(toMs(c.started))}` : ''} with this follow-up`,
      sub: 'Start a new session instead', chips: contChips(c), signInId: String(c.signInId || '') } : null,
    continuing,
    remember: s.resume ? { shown: false, label: '', note: '', keepsMode: false } : rememberWords(continuing, hs, h, pick),
    asks,
    modeChip: modeChip(asks),
    fact: asks ? '' : MODE_FACT,
    label: continuing ? 'Send' : 'Start',
    details: `${req.method || 'POST'} ${req.target || '/api/xbin/term/sessions'}`,
  };
}

/** choose(pick, req, field, value): the pick after the person changed one picker — a coding agent changed resets its model and mode to its own defaults. */
export function choose(pick, req, field, value) {
  const next = { ...pick, changed: [...new Set([...pick.changed, field])], defaulted: (pick.defaulted || []).filter((f) => f !== field) };
  if (field === 'harness') {
    const h = harnessesOf(sess(req)).find((x) => x.id === value);
    next.harness = value;
    if (h) { next.model = ''; next.mode = h.defaultMode ?? ''; next.changed = next.changed.filter((f) => f !== 'model' && f !== 'mode'); next.defaulted = ['model', 'mode']; }
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
  // (how much of what it would be told was on screen is the caller's to say: withSeenText — D252)
  const out = { knows: ['continue'], seen: { signInId: String(ps.signInId || ''), net: String(ps.net || '') } };
  if (s.resume) { // a resume picks its mode again, nothing else (D251 review M2)
    out.mode = pick.mode;
    return out;
  }
  const c = s.continue && s.continue.session ? s.continue : null;
  if (c && pick.cont && pick.harness === c.provider) {
    // continuing: the session keeps its own — held to it as shown: its coding agent and mode (D251)
    out.seen.harness = String(c.provider || '');
    out.seen.mode = String(c.mode ?? '');
    return out;
  }
  if (c) out.continue = false;
  Object.assign(out, { harness: pick.harness, model: pick.model, mode: pick.mode });
  if (pick.remember) out.remember = true;
  return out;
}

/** textBytes(text): its length as xbind counts it — UTF-8 bytes (seen.textLen, D251 review H1). */
export const textBytes = (text) => new TextEncoder().encode(String(text ?? '')).length;

/**
 * SEEN_TEXT: the approve's word (knows) that its seen.textLen is how much of
 * the text — a start's brief, a follow-up, a message — was on screen when
 * its person clicked: with it xbind requires the length, and allows only a
 * text that was all on screen (D252).
 */
export const SEEN_TEXT = 'seen-text';

/** withSeenText(body, onScreen): an approve body that says how much of the text was on screen (onScreen: the part shown) when its person clicked. */
export function withSeenText(body, onScreen) {
  return { ...body, knows: [...new Set([...(body.knows || []), SEEN_TEXT])], seen: { ...(body.seen || {}), textLen: textBytes(onScreen) } };
}

/** briefOf(req): what a start's session is told first — xbind's reading (preview.session.prompt), never the body's bytes (D252). */
export function briefOf(req) {
  return sessionRows(req).brief;
}

/**
 * startChips(req): a pending start as the batch's chips — the coding agent
 * it would run on and whether it asks ([] for anything else).
 */
export function startChips(req) {
  if (!req || req.kind !== 'session') return [];
  const s = sess(req);
  if (s.continue && s.continue.session) return contChips(s.continue);
  return [s.providerName || s.provider || '', s.model ? `model ${s.model}` : '', modeChip(s.asks === true)].filter(Boolean);
}

// ---- a message to a tile agent that acts without asking ---------------------------------------------

/** signInIdOf(req): the saved sign-in a follow-up's session carries — a message's, a continue's — by id: the chrome names it from the person's own list ('' none). */
export function signInIdOf(req) {
  if (!req) return '';
  if (req.kind === 'message') return messageOf(req).signInId;
  const c = sess(req).continue;
  return req.kind === 'session' && c && c.session ? String(c.signInId || '') : '';
}

/** messageOf(req): a message request as xbind previews it — {session, provider, name, mode, model, text, signInId}. */
export function messageOf(req) {
  const m = (req && req.preview && req.preview.message) || {};
  return { session: String(m.session || ''), provider: String(m.providerName || m.provider || 'the tile agent'), mode: String(m.mode || ''), model: String(m.model || ''), text: String(m.text ?? ''),
    signInId: String(m.signInId || '') };
}

/** messageView(req): the sheet's rows for a message — {heading, agent: [chips], text, why, label}. */
export function messageView(req) {
  const m = messageOf(req);
  const tile = String((req && req.tile) || '').split('/').filter(Boolean).pop() || 'the tile';
  return {
    heading: { before: `Send this message to the ${m.provider} session in `, strong: tile, after: '?' },
    title: `Send a message to ${m.provider} in ${tile}`,
    agent: [m.provider, m.mode ? `mode ${m.mode}` : '', modeChip(false), m.model ? `model ${m.model}` : '', m.signInId ? `uses your ${m.provider} sign-in` : ''].filter(Boolean),
    signInId: m.signInId,
    text: m.text,
    why: 'Send sends exactly this text to this session, once. It isn\'t sent if the session\'s mode or coding agent changed since.',
    label: 'Send',
  };
}

// ---- a follow-up allowed on its batch row (D251) -------------------------------------------------

/**
 * ROW_FIT_LINES, ROW_FIT_CHARS: a follow-up shown whole on its batch row —
 * at most 12 lines and 1,200 characters — so the usual short one is one
 * click (Send). A longer one shows ROW_LINES lines (at most ROW_CHARS
 * characters) and Show all; Send comes only once all of it is on screen
 * (D251 review H1).
 */
export const ROW_FIT_LINES = 12;
export const ROW_FIT_CHARS = 1200;
export const ROW_LINES = 3;
export const ROW_CHARS = 280;

// xbin's update brief opens with this line: the row leads with what follows (the goal), and shows the line after
// it, once the text is shown whole — only where xbind says xbin wrote it (briefBy), never by what the text says
const BRIEF_OPENING = 'This is a piece of work in this tile, handed to you by ';

/**
 * rowText(text, all, byXbin): a follow-up's text as its batch row shows it
 * — {whole, lead, opening, note, shown}. whole: all of it is shown (it
 * fits, or all) — lead, then opening (xbin's own brief's first paragraph,
 * its standing words, moved after the goal — only when byXbin: xbind says
 * xbin wrote it; '' otherwise) — and Send may be offered; otherwise lead is
 * its first lines and note says how long the text is. shown: the part of
 * the text on screen (all of it, or the first lines), what withSeenText
 * counts. Fitting counts lines at the line breaks a sheet draws as breaks
 * (\n: the others are drawn as escapes, reveal); whether it fits its box
 * on this screen is the row's to measure.
 */
export function rowText(text, all = false, byXbin = false) {
  const t = String(text ?? '');
  const chars = [...t].length, lines = t.split('\n').length;
  let lead = t, opening = '';
  const cut = t.indexOf('\n\n');
  if (byXbin && t.startsWith(BRIEF_OPENING) && cut > 0) {
    opening = t.slice(0, cut);
    lead = t.slice(cut).replace(/^\n+/, '');
  }
  const size = `${lines} ${lines === 1 ? 'line' : 'lines'}, ${chars.toLocaleString('en-US')} characters`;
  if (all || (lines <= ROW_FIT_LINES && chars <= ROW_FIT_CHARS)) return { whole: true, lead, opening, note: all && (lines > ROW_FIT_LINES || chars > ROW_FIT_CHARS) ? size : '', shown: t };
  let head = [...lead.split('\n').slice(0, ROW_LINES).join('\n')];
  if (head.length > ROW_CHARS) head = head.slice(0, ROW_CHARS);
  return { whole: false, lead: head.join(''), opening: '', note: `${size}: show all of it to send it`, shown: head.join('') };
}

/**
 * inPlace(req): a follow-up its person can allow on its batch row — a
 * message to a tile agent's session, or a start that continues the agent's
 * own session there (D251 B2): {kind: 'message' | 'continue', title, chips,
 * text, byXbin, body} — body is what Send sends (POST …/approve), what the
 * sheet sends for it as preselected; the row adds how much of the text was
 * on screen when its person clicked (withSeenText, rowText(…).shown);
 * byXbin: xbind says xbin wrote the text (its update brief), so the row may
 * move its opening line (rowText). null for anything else, and for one the
 * sheet must show:
 * text that holds hidden characters, a conversation that read the web, a
 * start xbind offers no choices for.
 */
export function inPlace(req) {
  if (!req || req.state !== 'pending' || (req.preview && req.preview.readWeb)) return null;
  const tile = String(req.tile || '').split('/').filter(Boolean).pop() || 'the tile';
  if (req.kind === 'message') {
    const m = messageView(req);
    return !m.text || reveal(m.text).hidden ? null : { kind: 'message', title: m.title, chips: m.agent, text: m.text, byXbin: false, body: { grant: 'once' } };
  }
  const s = sess(req), c = s.continue && s.continue.session ? s.continue : null;
  if (req.kind !== 'session' || !c || s.resume || !harnessesOf(s).length) return null;
  const text = briefOf(req);
  if (!text || reveal(text).hidden) return null;
  return { kind: 'continue', title: `Continue the ${c.providerName || c.provider} session in ${tile}`, chips: contChips(c), text, byXbin: s.briefBy === 'xbin',
    body: { grant: 'once', ...pickBody(req, pickOf(req)) } };
}

// ---- the one batch ------------------------------------------------------------------------------

/**
 * batch({requests, failed, changesets, sessions}, now): everything that
 * waits for the person, ONE row a pending act, oldest first — {kind:
 * 'request' | 'retry' | 'changeset' | 'session', id, title, chips, parts,
 * action, review, permits?, agentName?, at, waits?, inline?}. review: it
 * opens a review that Review all walks (a request's sheet, a plan's
 * review); retry: a start that was approved and didn't open, which the
 * person tries again; inline: a follow-up allowed on the row (inPlace) —
 * its action is Send, beside Deny and Review….
 */
export function batch({ requests = [], failed = [], changesets = [], sessions = [] } = {}, now = Date.now()) {
  const rows = [];
  for (const r of requests) {
    if (r.state !== 'pending') continue;
    const inline = inPlace(r);
    const title = inline ? inline.title : r.kind === 'message' ? messageView(r).title : needTitle(r);
    rows.push({ kind: 'request', id: r.id, title, chips: inline ? inline.chips : startChips(r), signInId: signInIdOf(r), parts: askedParts(r, now), action: inline ? 'Send' : 'Review…', review: true,
      at: toMs(r.created), ...(inline ? { inline } : {}) });
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
