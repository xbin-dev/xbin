// model/harness-ask.js — what a coding harness asks and how its controls read
// (D-harness §4.2.4, §4.2.5, §4.2.9, §4.2.10, §4.3.4, §4.3.12), in words both
// views draw:
//   permission(ps)  a permission park as the harness's own options (reject
//                   first when it defaults to no; an explicit one — it raises
//                   the session to a bypass mode — only for the owner, marked),
//                   the call, a preview of its diff, what "always" remembers;
//                   a plan approval (a plan-mode exit) with its plan
//   question(ps)    a question park: a form from its schema (formFields), or
//                   a page to open (url mode)
//   controls(h)     the live mode and config options (#hctl, the native toolbar)
//   settingOf(e)    the person's Auto / Always approve for a harness
//   slashMatches    the slash-command menu while "/" is typed
//   steerWords(v)   what the composer says while a turn runs; steerTrack
//                   notices a message steered into it (from the stream)
// Pure (no DOM): node-tested in hack/agent-template-harness-ask.test.mjs.
import { isHarness, nameOf, modeOf } from './harness.js';
import { busy } from './fold.js';

const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
export const isAllow = (o) => /^allow/.test((o && o.kind) || '');
export const isReject = (o) => /^reject/.test((o && o.kind) || '');

// What a call of each ACP kind asks to do.
const VERB = {
  execute: 'run a command', edit: 'edit a file', delete: 'delete a file', move: 'move a file', read: 'read a file',
  search: 'search', fetch: 'fetch a page', think: 'start a subagent', switch_mode: 'switch mode', other: 'use a tool',
};

// owner: the conversation's owner, a person (explicit options are theirs);
// talk: may answer at all (a participant).
export const ownerOf = (v, me) => !!(v && (!v.access || v.access === 'owner' || v.access === 'system') && me && me.kind === 'user' && !me.viewedBy);

// permission: a harness's permission park (pendingState.kind approval with
// harness) as a card — null for anything else. acp: the call's tool row's
// acp (§4.3.5), whose backend-made patches preview an edit when the park's
// own content has no diff.
export function permission(ps, { owner = false, talk = true, name = 'the coding agent', acp = null } = {}) {
  const hs = ps && ps.kind === 'approval' && ps.harness;
  if (!hs) return null;
  const all = (hs.options || []).filter((o) => o && o.optionId);
  const shown = all.filter((o) => !o.explicit || owner);
  const ordered = hs.defaultToNo ? [...shown.filter(isReject), ...shown.filter((o) => !isReject(o))] : shown;
  const tool = hs.tool || {};
  const plan = !!hs.planApproval;
  const always = ordered.find((o) => o.kind === 'allow_always' && !o.explicit);
  const rule = !plan && always && hs.rule && hs.rule.title
    ? `‘${always.name || 'Always allow'}’ lets ${name} run later ${hs.rule.kind || tool.kind || ''} calls titled ‘${clip(hs.rule.title, 80)}’ without asking in this conversation`.replace(/ {2}/g, ' ')
    : '';
  const options = ordered.map((o) => ({
    id: o.optionId, name: o.name || o.optionId, kind: o.kind || '', allow: isAllow(o), reject: isReject(o), explicit: !!o.explicit,
    title: o.explicit ? `raises ${name} to a mode where it stops asking — only the owner may` : o.kind === 'allow_always' && rule ? rule : '',
  }));
  const title = tool.title || (ps.toolCalls && ps.toolCalls[0] && ps.toolCalls[0].function && ps.toolCalls[0].function.name) || '';
  const label = tool.label && tool.label !== title ? tool.label : '';
  const command = tool.command && tool.command !== title ? tool.command : '';
  return {
    park: ps.park || '', plan, planText: plan ? String(hs.plan || '') : '',
    lead: plan ? `${name} has a plan` : `${name} asks to ${VERB[tool.kind] || VERB.other}`,
    title, label, command,
    raw: !command && !plan ? rawOf(tool.rawInput) : '',
    preview: contentPreview(tool.content) || diffsPreview(acp && acp.diffs),
    description: String(hs.description || ''),
    defaultToNo: !!hs.defaultToNo,
    rule, options, talk,
    hidden: all.length - shown.length, // explicit options a non-owner doesn't see
    reject: (ordered.find(isReject) || {}).optionId || '', // the rejection feedback goes with
  };
}

// rawOf: a call's raw input for the card — '' when nothing worth showing.
function rawOf(raw) {
  if (raw == null || raw === '') return '';
  if (typeof raw === 'string') return clip(raw, 600);
  if (typeof raw !== 'object' || !Object.keys(raw).length) return '';
  return clip(JSON.stringify(raw, null, 2), 600);
}

// --- the diff preview -----------------------------------------------------------------

const PREVIEW_LINES = 60;

// contentPreview: ACP tool content (a park's tool.content) as what a card
// shows — [{path, lines: [{t: '+'|'-'|' '|'@', text}], add, del, more}] per
// diff, or [{text}] for text content; null when there is none.
export function contentPreview(content) {
  const out = [];
  for (const c of Array.isArray(content) ? content : []) {
    if (!c || typeof c !== 'object') continue;
    if (c.type === 'diff' && c.path) out.push(lineDiff(c.path, c.oldText, c.newText));
    else if (c.type === 'content' && c.content && c.content.type === 'text' && c.content.text) out.push({ text: clip(c.content.text, 2000) });
  }
  return out.length ? out : null;
}

// diffsPreview: the tool row's backend-made patches (acp.diffs) the same way.
export function diffsPreview(diffs) {
  const out = (Array.isArray(diffs) ? diffs : []).filter((d) => d && d.patch).map((d) => {
    const lines = String(d.patch).split('\n').filter((l) => l && !/^(---|\+\+\+) /.test(l))
      .map((l) => ({ t: l[0] === '@' ? '@' : '+- '.includes(l[0]) ? l[0] : ' ', text: l[0] === '@' ? l : l.slice(1) }));
    return { path: d.path, lines: lines.slice(0, PREVIEW_LINES), add: d.add || 0, del: d.del || 0, more: Math.max(0, lines.length - PREVIEW_LINES) };
  });
  return out.length ? out : null;
}

// lineDiff: old → new as the lines that changed, with two lines of context
// each side — the common head and tail cut (enough for a preview; the tool
// row carries the exact patch once the call ran).
export function lineDiff(path, oldText, newText) {
  const a = oldText == null || oldText === '' ? [] : String(oldText).split('\n');
  const b = newText == null || newText === '' ? [] : String(newText).split('\n');
  let p = 0;
  while (p < a.length && p < b.length && a[p] === b[p]) p++;
  let s = 0;
  while (s < a.length - p && s < b.length - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++;
  const del = a.slice(p, a.length - s), add = b.slice(p, b.length - s);
  const ctx = 2, from = Math.max(0, p - ctx);
  const lines = [
    ...(a.length || b.length ? [{ t: '@', text: `@@ -${from + 1} +${from + 1} @@` }] : []),
    ...a.slice(from, p).map((text) => ({ t: ' ', text })),
    ...del.map((text) => ({ t: '-', text })),
    ...add.map((text) => ({ t: '+', text })),
    ...a.slice(a.length - s, Math.min(a.length, a.length - s + ctx)).map((text) => ({ t: ' ', text })),
  ];
  return { path, lines: lines.slice(0, PREVIEW_LINES), add: add.length, del: del.length, more: Math.max(0, lines.length - PREVIEW_LINES) };
}

// patchOf: a preview's diff as unified patch text (native's `diff` primitive).
export const patchOf = (d) => [`--- a${d.path}`, `+++ b${d.path}`, ...d.lines.map((l) => (l.t === '@' ? l.text : l.t + l.text))].join('\n');

// --- a question (elicitation) ---------------------------------------------------------

// question: a harness's question park — {park, message, mode: 'form', fields}
// or {mode: 'url', url} (a page to open, then Done) — null for anything else.
export function question(ps) {
  const hs = ps && ps.kind === 'question' && ps.harness;
  if (!hs) return null;
  const message = String(hs.message || '');
  const url = hs.url || (hs.mode === 'url' && hs.schema && hs.schema.url) || '';
  if (url || hs.mode === 'url') return { park: ps.park || '', message, mode: 'url', url: /^https?:\/\//i.test(url) ? url : '', text: url };
  return { park: ps.park || '', message, mode: 'form', fields: formFields(hs.schema) };
}

// formFields: a form-mode schema (a flat object of primitives, ACP form
// mode) as what a form draws, in property order — the shape of the shell's
// /vendor/agent-tools.js formFields (a single choice → radio, a multiple one
// → check, boolean, number, else text; a field marked as another's custom
// answer, `_meta._askUserQuestionCustomAnswer`, rides on it as `other`),
// plus `enumNames` for an enum's labels and each property's `default`.
export function formFields(schema) {
  const props = schema && typeof schema === 'object' && schema.properties && typeof schema.properties === 'object' ? schema.properties : {};
  const required = new Set(Array.isArray(schema && schema.required) ? schema.required : []);
  const opts = (list, names) => (Array.isArray(list) ? list : []).map((o, i) => (o && typeof o === 'object'
    ? { value: o.const ?? o.value ?? o.title, title: o.title ?? String(o.const ?? o.value ?? ''), description: o.description || '' }
    : { value: o, title: String((Array.isArray(names) && names[i]) ?? o), description: '' }));
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

// formContent: an accept's content from the form's values (key → value; a
// check field's value is a list): empty answers are left out.
export function formContent(fields, vals) {
  const out = {}, v = vals || {};
  for (const f of fields) {
    const x = v[f.key];
    if (f.kind === 'check') { if (Array.isArray(x) && x.length) out[f.key] = x; }
    else if (f.kind === 'bool') { if (typeof x === 'boolean') out[f.key] = x; }
    else if (f.kind === 'number') { if (x !== '' && x != null && !Number.isNaN(Number(x))) out[f.key] = Number(x); }
    else if (x != null && String(x).trim() !== '') out[f.key] = x;
    if (f.other && v[f.other] != null && String(v[f.other]).trim() !== '') out[f.other] = String(v[f.other]).trim();
  }
  return out;
}

// missingRequired: the required fields a content leaves out (a choice
// answered by its "Other" box counts), by title.
export const missingRequired = (fields, content) =>
  fields.filter((f) => f.required && !(f.key in content) && !(f.other && f.other in content)).map((f) => f.title || f.key);

// nativeSchema: the fields as the native `question` primitive's flat schema
// (strings, numbers, booleans; choices as oneOf consts) — a multiple choice,
// which it can't draw, becomes a yes/no per choice (`<key>.<i>`), which
// nativeContent folds back.
export function nativeSchema(fields, description = '') {
  const properties = {}, required = [];
  for (const f of fields) {
    const base = { title: f.title || f.key, ...(f.description ? { description: f.description } : {}) };
    if (f.kind === 'check') {
      f.options.forEach((o, i) => { properties[`${f.key}.${i}`] = { type: 'boolean', title: o.title, ...(o.description ? { description: o.description } : {}) }; });
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

// nativeContent: the native question's answers as the form's content.
export function nativeContent(fields, content) {
  const c = content || {}, vals = { ...c };
  for (const f of fields) {
    if (f.kind === 'check') vals[f.key] = f.options.filter((o, i) => c[`${f.key}.${i}`] === true).map((o) => o.value);
    else if (f.kind === 'radio' && c[f.key] != null) vals[f.key] = (f.options.find((o) => String(o.value) === String(c[f.key])) || { value: c[f.key] }).value;
  }
  return formContent(fields, vals);
}

// --- the mode and config options (§4.2.4) ---------------------------------------------

// controls: the live session's mode and options as the #hctl popover and the
// native toolbar draw them — null for a run that isn't a harness's. A mode
// that raises the session to a bypass mode (explicit) is the owner's only.
export function controls(h, entry = null, { owner = false, talk = true } = {}) {
  if (!h) return null;
  const m = modeOf(h, entry);
  const modes = m.available.map((x) => ({
    id: x.id, name: x.name || x.id, description: x.description || '', explicit: !!x.explicit, current: x.id === m.current,
    allowed: talk && (!x.explicit || owner),
  }));
  const options = (h.options || []).filter((o) => o && o.id && o.category !== 'mode').map((o) => {
    const choices = (o.options || []).map((c) => ({ value: c.value, name: c.name || String(c.value), description: c.description || '' }));
    const cur = choices.find((c) => c.value === o.currentValue);
    return { id: o.id, name: o.name || o.id, category: o.category || '', description: o.description || '', value: o.currentValue ?? '', valueName: cur ? cur.name : String(o.currentValue ?? ''), choices };
  });
  const label = [m.name ? (m.explicit ? '⚠ ' : '') + m.name : '', ...options.map((o) => o.valueName)].filter(Boolean).join(' · ') || 'Mode';
  return { name: nameOf(h), mode: { current: m.current, name: m.name, explicit: m.explicit }, modes, options, label: clip(label, 48), talk, owner };
}

// modeConfirm: the words that confirm switching to an explicit (bypass) mode.
export const modeConfirm = (name, mode) => `Switch ${name} to ${mode.name}? It stops asking before it edits files or runs commands in this conversation.`;
// optionConfirm: …and allowing a permission option that raises it there.
export const optionConfirm = (name, o) => `${o.name}: ${name} then stops asking before it edits files or runs commands in this conversation. Go ahead?`;

// --- Auto / Always approve (§4.3.12) ------------------------------------------------------

// settingOf: the person's setting for a harness (a catalog entry) — its value
// and the two choices; Auto is off for a harness without an auto mode.
export function settingOf(entry, setting = 'approve') {
  const name = nameOf(entry);
  const canAuto = !!(entry && entry.autoMode);
  const mode = (id) => ((entry && entry.modes) || []).find((x) => x.id === id)?.name || id || '';
  const ask = mode(entry && (entry.approveMode || entry.defaultMode));
  return {
    provider: (entry && entry.id) || '', name, canAuto,
    value: setting === 'auto' && canAuto ? 'auto' : 'approve',
    choices: [
      { value: 'approve', label: 'Always approve', title: `${name} asks you before it edits or runs anything${ask ? ` (${ask})` : ''}` },
      { value: 'auto', label: 'Auto', disabled: !canAuto,
        title: canAuto ? `${name} edits files without asking (${mode(entry.autoMode)}); commands still ask` : `${name} has no auto mode — it asks as its own settings say` },
    ],
    note: `How ${name} starts for you — your new conversations and the coding agents started for you. A conversation's own mode is switched above.`,
  };
}

// --- slash commands -------------------------------------------------------------------

// slashCommands: a harness's advertised commands ({name, description, hint}).
export const slashCommands = (h) => ((h && h.commands) || []).filter((c) => c && c.name)
  .map((c) => ({ name: String(c.name).replace(/^\//, ''), description: c.description || '', hint: c.hint || (c.input && c.input.hint) || '' }));

// slashMatches: the commands the draft names so far — null while the draft
// isn't a command being typed ("/" and a name, no space yet); names that
// start with it first, then those containing it.
export function slashMatches(cmds, draft) {
  const m = /^\/([^\s/]*)$/.exec(String(draft ?? ''));
  if (!m) return null;
  const q = m[1].toLowerCase();
  const list = cmds || [];
  const starts = list.filter((c) => c.name.toLowerCase().startsWith(q));
  const has = q ? list.filter((c) => !c.name.toLowerCase().startsWith(q) && c.name.toLowerCase().includes(q)) : [];
  return [...starts, ...has].slice(0, 12);
}
export const slashText = (c) => `/${c.name} `;

// --- steering (§3.5, §4.2.10) -------------------------------------------------------------

// steerWords: what the composer says on a harness conversation (null: the
// built-in words) — the placeholder, and how a queued chip is labelled: while
// a turn runs a message steers it (an adapter that steers) or waits for it
// to end; ⌘/Ctrl+Enter (native: Send now) interrupts it.
export function steerWords(v, { native = false } = {}) {
  const r = v && v.run;
  if (!isHarness(r) || v.access === 'viewer') return null;
  const h = r.harness || {};
  const name = nameOf(h);
  const cut = native ? 'Send now interrupts' : '⌘/Ctrl+Enter interrupts';
  if (busy(r.status)) {
    return h.steering
      ? { busy: true, steering: true, placeholder: `steer ${name} — sent into its running turn (${cut})`, label: 'steering', title: `being sent into ${name}'s running turn` }
      : { busy: true, steering: false, placeholder: `queued — sent to ${name} when this turn ends (${cut})`, label: `queued for ${name}`, title: `sent to ${name} when its current turn ends` };
  }
  const ps = r.status === 'waiting_input' ? r.pendingState || {} : {};
  if (ps.kind === 'login') return null;
  const words = (placeholder) => ({ busy: false, steering: false, placeholder, label: `queued for ${name}`, title: `sent to ${name} next` });
  if (ps.kind === 'approval') return words(`reply — rejects the request, then goes to ${name}`);
  if (ps.kind === 'question') return words(`reply — skips the question, then goes to ${name}`);
  return words(`message ${name}…`);
}

// steerTrack: a tracker that notices, from the stream, a message of the
// composer's steered into a running turn: a queued message of an adapter
// that steers left the queue while the turn ran and then showed up in the
// transcript. track(v, blocks, now) → the notes to show ([{text, until}]),
// each for `ms`. A taken-back message never shows up, so never counts.
export function steerTrack(ms = 6000) {
  let run = null, seen = new Map(), gone = [], notes = [];
  const count = (blocks, text) => (blocks || []).reduce((n, b) => n + (b.k === 'user' && b.text === text ? 1 : 0), 0);
  return (v, blocks, now) => {
    const r = v && v.run;
    if (!isHarness(r)) { run = null; seen = new Map(); gone = []; notes = []; return []; }
    if (r.id !== run) { run = r.id; seen = new Map(); gone = []; notes = []; }
    const steering = !!(r.harness && r.harness.steering);
    // each queued message with how often its text was in the transcript when it was queued
    const q = new Map((v.queued || []).map((x) => [x.id, seen.get(x.id) || { text: x.text || '', had: count(blocks, x.text || '') }]));
    for (const [id, x] of seen) if (!q.has(id) && x.text && steering) gone.push({ text: x.text, at: now, had: x.had });
    seen = q;
    gone = gone.filter((g) => {
      if (count(blocks, g.text) > g.had) { notes.push({ text: g.text, until: now + ms }); return false; }
      return now - g.at < 15000;
    });
    notes = notes.filter((n) => n.until > now);
    return notes.slice();
  };
}
