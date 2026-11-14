/**
 * chat/ask.js — what an agent asks a person, and what a person types to it,
 * for the chat kit (/vendor/chat/ask.js): a permission request as its
 * card's words and options, a question as a form (or a page to open), the
 * slash-command menu, what the composer says while a turn runs, a pasted
 * sign-in code.
 *
 * The inputs are the block model's (/vendor/chat/blocks.js): a perm block,
 * an ask block. A reducer that holds something else converts first (the
 * agent template's model/harness-ask.js: its park → these). Pure — no lit,
 * no DOM — so a native view and node tests load it; exports only grow
 * (/vendor/chat/rev.js).
 */
import { contentPreview, diffsPreview } from './tools.js';

const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };

// ---- a permission request ------------------------------------------------------------------

/** An option allows / rejects (ACP's kinds: allow_once, allow_always, reject_once, reject_always). */
export const isAllow = (o) => /^allow/.test((o && o.kind) || '');
export const isReject = (o) => /^reject/.test((o && o.kind) || '');

/** What a call of each tool kind asks to do. */
export const PERM_VERB = Object.freeze({
  execute: 'run a command', edit: 'edit a file', delete: 'delete a file', move: 'move a file', read: 'read a file',
  search: 'search', fetch: 'fetch a page', think: 'start a subagent', switch_mode: 'switch mode', other: 'use a tool',
});

/**
 * permission(perm, opts): a perm block as what its card says and offers.
 *   opts.name        how the agent is called (ChatCtx.agentName)
 *   opts.can         {answer, raise} (ChatCtx.can): `raise` — the reader may
 *                    pick an `explicit` option (one that raises what the agent
 *                    does without asking); the others never see those
 *   opts.diffs       the call's own diffs (its record's), previewed when the
 *                    request's content has none
 *   opts.scopedOnly  the host's "always" is its own rule: with no rule to
 *                    scope it to (perm.rule null) the allow_always options
 *                    are left out — never a plan's, whose options are modes
 *   opts.where       the rule's reach, in words ("in this conversation")
 * → {plan, planText, planPath, lead, title, label, command, raw, preview,
 *    description, defaultToNo, rule, options, hidden, reject}
 *   options  [{id, name, kind, allow, reject, explicit, title}] in the
 *            agent's order — the rejections first when it defaults to no
 *   rule     what "always" will also allow, in words ('' none)
 *   hidden   how many explicit options the reader is not shown
 *   reject   the option a rejection's feedback goes with ('' none)
 */
export function permission(perm, opts = {}) {
  if (!perm) return null;
  const { name = 'the agent', can = {}, diffs = null, scopedOnly = false, where = 'in this conversation' } = opts;
  const tool = perm.tool || {};
  const plan = !!perm.plan;
  const all = (perm.options || []).filter((o) => o && o.optionId);
  const seen = all.filter((o) => !o.explicit || can.raise);
  const shown = scopedOnly && !plan && !perm.rule ? seen.filter((o) => o.kind !== 'allow_always') : seen;
  const ordered = perm.defaultToNo ? [...shown.filter(isReject), ...shown.filter((o) => !isReject(o))] : shown;
  const always = ordered.find((o) => o.kind === 'allow_always' && !o.explicit);
  const rule = !plan && always && perm.rule && perm.rule.title
    ? `‘${always.name || 'Always allow'}’ lets ${name} run later ${perm.rule.kind || tool.kind || ''} calls titled ‘${clip(perm.rule.title, 80)}’ without asking ${where}`.replace(/ {2}/g, ' ')
    : '';
  const options = ordered.map((o) => ({
    id: o.optionId, name: o.name || o.optionId, kind: o.kind || '', allow: isAllow(o), reject: isReject(o), explicit: !!o.explicit,
    title: o.explicit ? `raises ${name} to a mode where it stops asking — only the owner may` : o.kind === 'allow_always' && rule ? rule : '',
  }));
  const title = tool.title || (perm.calls && perm.calls[0]) || '';
  return {
    plan, planText: plan ? String(perm.plan.text || '') : '', planPath: plan ? String(perm.plan.path || '') : '',
    lead: plan ? `${name} has a plan` : `${name} asks to ${PERM_VERB[tool.kind] || PERM_VERB.other}`,
    title,
    label: tool.label && tool.label !== title ? tool.label : '',
    command: tool.command && tool.command !== title ? tool.command : '',
    raw: !tool.command && !plan ? rawOf(tool.rawInput) : '', // a command says it (in the title when they are one)
    preview: contentPreview(tool.content) || diffsPreview(diffs),
    description: String(perm.description || ''),
    defaultToNo: !!perm.defaultToNo,
    rule, options,
    hidden: all.length - seen.length,
    reject: (ordered.find(isReject) || {}).optionId || '',
  };
}

// a call's raw input for the card — '' when nothing worth showing
function rawOf(raw) {
  if (raw == null || raw === '') return '';
  if (typeof raw === 'string') return clip(raw, 600);
  if (typeof raw !== 'object' || !Object.keys(raw).length) return '';
  return clip(JSON.stringify(raw, null, 2), 600);
}

/**
 * decisions(perm): the answers a host that takes a bare decision offers when
 * the agent sent no options — Allow once, Allow for the session (only with
 * a rule to scope it to), Deny — each {decision, name, allow, reject}.
 */
export function decisions(perm) {
  return [
    { decision: 'allow_once', name: 'Allow once', allow: true, reject: false },
    ...(perm && perm.rule ? [{ decision: 'allow_always', name: 'Allow for the session', allow: true, reject: false }] : []),
    { decision: 'reject_once', name: 'Deny', allow: false, reject: true },
  ];
}

/**
 * permAnswer(option, feedback): what choosing an option of permission()
 * (or of decisions()) answers — {optionId | decision, verdict: 'allow' |
 * 'reject', feedback}: the shape ChatCtx.permit takes. Feedback goes only
 * with a rejection (a plan kept: what should change), trimmed; the host
 * sends it the way its agent takes it (a field of the answer, or the next
 * message).
 */
export function permAnswer(option, feedback = '') {
  const o = option || {};
  const fb = o.reject ? String(feedback ?? '').trim() : '';
  return { ...(o.id ? { optionId: o.id } : { decision: o.decision || (o.reject ? 'reject_once' : 'allow_once') }), verdict: o.reject ? 'reject' : 'allow', feedback: fb };
}

/** optionConfirm(name, option): the words that confirm an `explicit` option before it is sent. */
export const optionConfirm = (name, o) => `${o.name}: ${name} then stops asking before it edits files or runs commands in this conversation. Go ahead?`;
/** modeConfirm(name, mode): …and switching the session to a mode that stops asking. */
export const modeConfirm = (name, mode) => `Switch ${name} to ${mode.name}? It stops asking before it edits files or runs commands in this conversation.`;

// ---- a question ---------------------------------------------------------------------------

/**
 * question(ask): an ask block as what its card draws — by mode:
 *   form  {mode, message, fields}  a form from its schema (formFields)
 *   url   {mode, message, url, text}  a page to open, then Done: `url` only
 *         when it is http(s) (a link), `text` what the agent sent
 *   text  {mode, message}  only its message: the next message answers it
 */
export function question(ask) {
  if (!ask) return null;
  const message = String(ask.message || '');
  const url = ask.url || (ask.mode === 'url' && ask.schema && ask.schema.url) || '';
  if (url || ask.mode === 'url') return { message, mode: 'url', url: /^https?:\/\//i.test(url) ? url : '', text: url };
  if (ask.mode === 'text') return { message, mode: 'text' };
  return { message, mode: 'form', fields: formFields(ask.schema) };
}

/**
 * formFields(schema): a form-mode schema (a flat JSON Schema object of
 * primitives, ACP's form mode) as what a form draws, in property order —
 * [{key, kind, title, description, required, options, other, otherHint,
 * dflt}]:
 *   radio   a single choice: `oneOf`, `anyOf` or `enum` (labels from the
 *           options' titles, else `enumNames`)
 *   check   a multiple one: an array of `anyOf` / `oneOf` / `enum` items
 *   bool · number (number, integer) · text (everything else)
 * `dflt` is the property's `default` (the form shows it chosen). A text
 * field marked as another question's custom answer
 * (`_meta._askUserQuestionCustomAnswer` — Claude's and Codex's "Other" box)
 * rides on that question as `other`, with `otherHint` its placeholder.
 */
export function formFields(schema) {
  const props = schema && typeof schema === 'object' && schema.properties && typeof schema.properties === 'object' ? schema.properties : {};
  const required = new Set(Array.isArray(schema && schema.required) ? schema.required : []);
  const opts = (list, names) => (Array.isArray(list) ? list : []).map((o, i) => (o && typeof o === 'object'
    ? { value: o.const ?? o.value ?? o.title, title: o.title ?? String(o.const ?? o.value ?? ''), description: o.description || '' }
    : { value: o, title: String((Array.isArray(names) ? names[i] : null) ?? o), description: '' }));
  const fields = [], byKey = {}, others = [];
  for (const [key, p] of Object.entries(props)) {
    if (!p || typeof p !== 'object') continue;
    const custom = p._meta && p._meta._askUserQuestionCustomAnswer;
    if (custom && custom.questionId) { others.push([custom.questionId, key, p]); continue; }
    const f = { key, title: p.title || '', description: p.description || '', required: required.has(key), options: [], other: null, otherHint: '', dflt: p.default };
    if (p.type === 'array') { f.kind = 'check'; f.options = opts((p.items && (p.items.anyOf || p.items.oneOf || p.items.enum)) || [], p.items && p.items.enumNames); }
    else if (p.oneOf || p.anyOf || p.enum) { f.kind = 'radio'; f.options = opts(p.oneOf || p.anyOf || p.enum, p.enumNames); }
    else if (p.type === 'boolean') f.kind = 'bool';
    else if (p.type === 'number' || p.type === 'integer') f.kind = 'number';
    else f.kind = 'text';
    fields.push(f); byKey[key] = f;
  }
  for (const [qid, key, p] of others) {
    if (byKey[qid]) { byKey[qid].other = key; byKey[qid].otherHint = p.description || p.title || ''; }
    else fields.push({ key, kind: 'text', title: p.title || 'Other', description: p.description || '', required: required.has(key), options: [], other: null, otherHint: '' });
  }
  return fields;
}

/**
 * formContent(fields, vals): an accept's content from the form's values
 * (key → value; a check field's is a list). Empty answers are left out; a
 * field left as it was drawn answers its schema default; an "Other" box's
 * text goes under its own key, trimmed.
 */
export function formContent(fields, vals) {
  const out = {}, v = vals || {};
  for (const f of fields) {
    const x = v[f.key] ?? f.dflt;
    if (f.kind === 'check') { if (Array.isArray(x) && x.length) out[f.key] = x; }
    else if (f.kind === 'bool') { if (typeof x === 'boolean') out[f.key] = x; }
    else if (f.kind === 'number') { if (x !== '' && x != null && !Number.isNaN(Number(x))) out[f.key] = Number(x); }
    else if (x != null && String(x).trim() !== '') out[f.key] = x;
    if (f.other && v[f.other] != null && String(v[f.other]).trim() !== '') out[f.other] = String(v[f.other]).trim();
  }
  return out;
}

/** missingRequired(fields, content): the required fields a content leaves out, by title — a choice answered by its "Other" box counts as answered. */
export const missingRequired = (fields, content) =>
  fields.filter((f) => f.required && !(f.key in content) && !(f.other && f.other in content)).map((f) => f.title || f.key);

/**
 * nativeSchema(fields, description): the fields as the flat schema the
 * native `question` primitive draws (/docs/native.md: strings, numbers,
 * booleans; choices as oneOf consts). A multiple choice, which it can't
 * draw, becomes a yes/no per choice (`<key>.<i>`) that nativeContent folds
 * back.
 */
export function nativeSchema(fields, description = '') {
  const properties = {}, required = [];
  for (const f of fields) {
    const base = { title: f.title || f.key, ...(f.description ? { description: f.description } : {}) };
    if (f.kind === 'check') {
      // a yes/no per choice, each named for the question it answers (the
      // first also says it): a flat form has no field over the choices
      f.options.forEach((o, i) => {
        const d = [i === 0 ? f.description : '', o.description].filter(Boolean).join(' — ');
        properties[`${f.key}.${i}`] = { type: 'boolean', title: `${base.title}: ${o.title}`, ...(d ? { description: d } : {}) };
      });
    } else if (f.kind === 'radio') properties[f.key] = { ...base, type: 'string', oneOf: f.options.map((o) => ({ const: String(o.value), title: o.title })) };
    else if (f.kind === 'bool') properties[f.key] = { ...base, type: 'boolean' };
    else if (f.kind === 'number') properties[f.key] = { ...base, type: 'number' };
    else properties[f.key] = { ...base, type: 'string' };
    if (f.dflt !== undefined && properties[f.key]) properties[f.key].default = f.dflt;
    if (f.other) properties[f.other] = { type: 'string', title: 'Other', ...(f.otherHint ? { description: f.otherHint } : {}) };
    if (f.required && f.kind !== 'check' && !f.other) required.push(f.key);
  }
  return { type: 'object', ...(description ? { description } : {}), properties, ...(required.length ? { required } : {}) };
}

/** nativeContent(fields, content): a native question's answers (nativeSchema's keys) as the form's content. */
export function nativeContent(fields, content) {
  const c = content || {}, vals = { ...c };
  for (const f of fields) {
    if (f.kind === 'check') vals[f.key] = f.options.filter((o, i) => c[`${f.key}.${i}`] === true).map((o) => o.value);
    else if (f.kind === 'radio' && c[f.key] != null) vals[f.key] = (f.options.find((o) => String(o.value) === String(c[f.key])) || { value: c[f.key] }).value;
  }
  return formContent(fields, vals);
}

// ---- slash commands -----------------------------------------------------------------------
//
// The agent advertises its commands (ACP available_commands_update); typing
// "/" at the start of the composer offers them. A command goes to the agent
// as plain prompt text — the agent parses "/name args" — so there is no
// protocol here, only the menu's one rule: names that start with what is
// typed first (in the agent's order), then names or descriptions containing
// it; at most SLASH_MAX.

/** How many commands the menu offers. */
export const SLASH_MAX = 8;

/** slashCommands(list): an agent's advertised commands as {name, description, hint} (a leading "/" dropped; ACP's input.hint read). */
export const slashCommands = (list) => (Array.isArray(list) ? list : []).filter((c) => c && c.name)
  .map((c) => ({ name: String(c.name).replace(/^\//, ''), description: c.description || '', hint: c.hint || (c.input && c.input.hint) || '' }));

/** slashQuery(draft): the command name being typed ("/rev" → "rev", "/" → ""), or null when the draft is not a bare slash command in progress. */
export function slashQuery(draft) {
  const m = /^\/([\w:.-]*)$/.exec(String(draft ?? ''));
  return m ? m[1].toLowerCase() : null;
}

/** matchCommands(cmds, q, max): the menu for a query — prefix matches first, then names or descriptions containing it; at most max. */
export function matchCommands(cmds, q, max = SLASH_MAX) {
  const list = Array.isArray(cmds) ? cmds.filter((c) => c && c.name) : [];
  const query = String(q ?? '').toLowerCase();
  const pre = list.filter((c) => c.name.toLowerCase().startsWith(query));
  const sub = query ? list.filter((c) => !pre.includes(c) && (c.name.toLowerCase().includes(query) || String(c.description || '').toLowerCase().includes(query))) : [];
  return pre.concat(sub).slice(0, max);
}

/** slashMenu(cmds, draft, max): the menu for a draft — null while it is not a command being typed. */
export function slashMenu(cmds, draft, max = SLASH_MAX) {
  const q = slashQuery(draft);
  return q == null ? null : matchCommands(cmds, q, max);
}

/** commandHint(cmds, draft): what to type after a completed "/name " whose command takes input ({name, hint}), while nothing is typed yet. */
export function commandHint(cmds, draft) {
  const m = /^\/([\w:.-]+)\s+$/.exec(String(draft ?? ''));
  if (!m) return null;
  const c = (Array.isArray(cmds) ? cmds : []).find((x) => x && x.name === m[1]);
  return c && c.hint ? { name: c.name, hint: c.hint } : null;
}

/** slashText(command): the draft after picking a command from the menu. */
export const slashText = (c) => `/${c.name} `;

// ---- the composer while a turn runs -------------------------------------------------------

/**
 * composerWords(s): what the composer says, and how a message that waits is
 * labelled — {busy, steering, placeholder, label, title}.
 *   s.name      the agent's name
 *   s.busy      a turn runs: a message steers it (s.steering: the agent
 *               takes messages mid-turn) or waits for its end; ⌘/Ctrl+Enter
 *               (s.native: "Send now") interrupts it
 *   s.waits     what the session waits on a person for: 'login' |
 *               'approval' | 'question' | ''
 *   s.barred    why nothing can be sent at all ('' it can): said instead
 */
export function composerWords(s = {}) {
  const name = s.name || 'the agent';
  const cut = s.native ? 'Send now interrupts' : '⌘/Ctrl+Enter interrupts';
  if (s.barred) return { busy: false, steering: false, placeholder: s.barred, label: `queued for ${name}`, title: s.barred };
  if (s.busy) {
    return s.steering
      ? { busy: true, steering: true, placeholder: `steer ${name} — sent into its running turn (${cut})`, label: 'steering', title: `being sent into ${name}'s running turn` }
      : { busy: true, steering: false, placeholder: `queued — sent to ${name} when this turn ends (${cut})`, label: `queued for ${name}`, title: `sent to ${name} when its current turn ends` };
  }
  const words = (placeholder) => ({ busy: false, steering: false, placeholder, label: `queued for ${name}`, title: `sent to ${name} next` });
  if (s.waits === 'login') return words(`sign in to ${name} first — then message it…`);
  if (s.waits === 'approval') return words(`reply — rejects the request, then goes to ${name}`);
  if (s.waits === 'question') return words(`reply — skips the question, then goes to ${name}`);
  return words(`message ${name}…`);
}

/**
 * steerTracker(ms): notices a message of the composer's steered into a
 * running turn — it waited while a turn ran, left the queue while that turn
 * still ran, and then showed up in the transcript. One sent while no turn
 * runs waits too until it becomes the next prompt (no steer); one taken
 * back never shows up, so never counts. → track(s, now) → the notes to show
 * ([{text, until}]), each for ms.
 *   s.run       the conversation's id (another one starts over); s null: none
 *   s.steering  the agent takes messages mid-turn
 *   s.turn      a turn that already has a person's message runs now
 *   s.queued    [{id, text}] the messages waiting
 *   s.count     (text) → how often the transcript holds a person's message with that text
 */
export function steerTracker(ms = 6000) {
  let run = null, seen = new Map(), gone = [], notes = [];
  return (s, now) => {
    if (!s) { run = null; seen = new Map(); gone = []; notes = []; return []; }
    if (s.run !== run) { run = s.run; seen = new Map(); gone = []; notes = []; }
    // each waiting message with how often its text was in the transcript when it began to wait, and whether a turn ran then
    const q = new Map((s.queued || []).map((x) => [x.id, seen.get(x.id) || { text: x.text || '', had: s.count(x.text || ''), turn: !!s.turn }]));
    for (const [id, x] of seen) if (!q.has(id) && x.text && s.steering && x.turn && s.turn) gone.push({ text: x.text, at: now, had: x.had });
    seen = q;
    gone = gone.filter((g) => {
      if (s.count(g.text) > g.had) { notes.push({ text: g.text, until: now + ms }); return false; }
      return now - g.at < 15000;
    });
    notes = notes.filter((n) => n.until > now);
    return notes.slice();
  };
}

// ---- a sign-in code -----------------------------------------------------------------------

/** cleanCode(s): a pasted code as a CLI reads it — one line, nothing but what a code is made of (a paste may carry spaces, a newline, a control character). */
// eslint-disable-next-line no-control-regex
export const cleanCode = (s) => String(s || '').replace(/[\s\x00-\x1f\x7f]/g, '');
