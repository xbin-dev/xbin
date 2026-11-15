// model/harness-heads.js — a coding harness's tool call (`acp:<kind>`,
// D147 §4.3.5) in a few words, as tool-heads.js says a
// built-in call: its family (the card's icon), its headline, the command
// under it, what it came to, and its state. tool-heads.js hands every
// `acp:*` name here.
//
// The words are xbind's chat kit's (/vendor/chat/tools.js, D224 — the Agent
// tab says a call the same way). This module is the adapter from the
// template's row shape to the kit's tool record, and keeps every export it
// had: a call's arguments are rawInput's own fields plus `summary` (the
// adapter's label, else its title, else the kind); its tool row's `acp`
// (the lift of Meta.harness) says the rest — status, exit code and output,
// diffs, locations, the subagent it runs under — under the record's own
// field names, so the kit reads an `acp` as it reads a record (acpRecord
// makes the whole one). Pure (no DOM).
import { newToolRecord } from '/vendor/chat/blocks.js';
import { headline, reading, subline, outcome, oneLine } from '/vendor/chat/tools.js';

// What the kit says of a record, said of a tool row's `acp`: its output
// (ANSI stripped; the last n characters — {text, cut, dropped}), its diffs
// ({path, status, add, del, patch, truncated} each; their totals; as one
// unified diff; a patch's lines with their classes), its places, a subagent
// call's task, its status as a chip. pathOf / commandOf pick what adapters
// name them among a call's arguments.
export {
  stripAnsi, outputTail, diffTotals, diffFiles, joinPatches, patchLines, placesOf, taskOf,
  stateChip as acpChip, inputPath as pathOf, inputCommand as commandOf,
} from '/vendor/chat/tools.js';

// ACP's ToolKinds and the family each is drawn as.
export const ACP_FAMILY = {
  read: 'file', edit: 'edit', delete: 'del', move: 'move', search: 'search',
  execute: 'box', think: 'think', fetch: 'web', switch_mode: 'mode', other: 'other',
};
// The families only harness calls have: the web's glyphs (bx-icons names,
// D184) and the native icons (tool-heads.js ICON and native/ui.js
// FAMILY_ICON take them in).
export const ACP_ICON = { edit: 'pencil', search: 'search', del: 'trash', move: 'arrow-right', think: 'thought', mode: 'shield' };
export const ACP_NATIVE_ICON = { edit: 'pencil', search: 'search', del: 'trash', move: 'link', think: 'cpu', mode: 'shield' };

export const isAcp = (name) => String(name || '').startsWith('acp:');
// acpKind: the ToolKind of a call name ('acp:edit' → 'edit'); unknown → 'other'.
export function acpKind(name) {
  const k = String(name || '').slice(4);
  return k in ACP_FAMILY ? k : 'other';
}
export const acpFamily = (name) => ACP_FAMILY[acpKind(name)];

// --- the row as the kit's record ---------------------------------------------------------

// callIdOf: the agent's own id of a stored call id — "h<gen>:<id>", and
// "h<gen>:<id>#<n>" for its n-th use (an id that itself ends in #<n> can't
// be told from a reuse).
export const callIdOf = (stored) => String(stored || '').replace(/^h\d+:/, '').replace(/#\d+$/, '');

// rawInputOf: a call's (parsed) arguments back as the agent's rawInput — its
// fields without `summary`; a rawInput `summary` is stored as rawSummary; one
// that isn't an object as {summary, rawInput}. No arguments and an empty
// object are both null.
export function rawInputOf(a) {
  const { summary: _s, rawSummary, ...rest } = (a && typeof a === 'object') ? a : {};
  if ('rawInput' in rest && Object.keys(rest).length === 1 && (rest.rawInput == null || typeof rest.rawInput !== 'object' || Array.isArray(rest.rawInput))) return rest.rawInput;
  if (rawSummary !== undefined) rest.summary = rawSummary;
  return Object.keys(rest).length ? rest : null;
}

const over = (s) => s === 'completed' || s === 'failed' || s === 'cancelled';

// the call's text content, read back from its tool row: the row holds it
// only when it has nothing better to say (an edit's row is "edited …", a
// command's its output; a failed call's is "error: …")
function textOf(result, acp) {
  if (typeof acp.text === 'string') return acp.text;
  const c = String(result || '');
  if (!over(acp.status) || acp.status === 'cancelled') return '';
  if (acp.status === 'failed') {
    const m = c.startsWith('error: ') ? c.slice(7) : '';
    return m === 'the call failed' || (acp.output && acp.output.endsWith(m)) ? '' : m;
  }
  if ((acp.diffs && acp.diffs.length) || acp.output || acp.exitCode != null) return '';
  return c === (acp.title || 'done') ? '' : c;
}

// acpRecord: a harness call as the kit's ToolRecord — b: its {name, callId,
// acp, result} (the fold's tool block, or those parts of it), a: its parsed
// arguments. The summary is the adapter's label, else its title, else the
// kind: one that is neither the title nor the kind is the label (a row
// whose lift doesn't say it).
export function acpRecord(b, a = {}) {
  const acp = (b && b.acp) || {};
  const kind = acp.kind || acpKind(b && b.name);
  const said = typeof a.summary === 'string' ? a.summary.trim() : '';
  const rec = newToolRecord(callIdOf(b && b.callId));
  rec.kind = kind in ACP_FAMILY ? kind : 'other';
  rec.title = acp.title || '';
  rec.label = acp.label || (said && said !== (acp.title || kind) ? said : '');
  rec.name = acp.tool || '';
  rec.status = acp.status || 'pending';
  rec.parent = callIdOf(acp.parent);
  rec.subagent = !!acp.subagent;
  rec.planReview = !!acp.planReview;
  rec.rawInput = acp.rawInput !== undefined ? acp.rawInput : rawInputOf(a);
  rec.locations = (acp.locations || []).filter((l) => l && l.path).map((l) => (l.line != null ? { path: l.path, line: l.line } : { path: l.path }));
  rec.files = [...(acp.files || [])];
  rec.text = textOf(b && b.result, acp);
  rec.terminal = typeof acp.output === 'string'; // (all a row knows of one: output came)
  rec.output = acp.output || '';
  rec.outputTruncated = Number(acp.outputTruncated) || 0;
  rec.exitCode = acp.exitCode ?? null;
  rec.diffs = (acp.diffs || []).map((d) => ({ path: d.path, status: d.status || 'modified', add: d.add || 0, del: d.del || 0, patch: d.patch || '', truncated: !!d.truncated }));
  return rec;
}

// --- its words ---------------------------------------------------------------------------

// acpHeadline: what a call's card is called (the kit's headline: the
// adapter's label, a description among the arguments, a shell command read
// deterministically, the title's first line, a reading by kind) — from its
// tool row's acp; without one (a row not lifted) its summary, else the reading.
export function acpHeadline(name, a = {}, acp = null) {
  if (!acp) {
    const s = typeof a.summary === 'string' ? a.summary.trim() : '';
    return s ? oneLine(s, 100) : acpReading(name, a);
  }
  return oneLine(headline(acpRecord({ name, acp }, a)), 100);
}

// acpWords: a call's headline and the command under it, together — the
// subline is judged against the whole headline, not its first hundred
// characters (a long command that is its own headline has none).
export function acpWords(name, a = {}, acp = null) {
  if (!acp) return { headline: acpHeadline(name, a), sub: acpSubline(name, a) };
  const rec = acpRecord({ name, acp }, a);
  const said = headline(rec);
  return { headline: oneLine(said, 100), sub: subline(rec, said) };
}

// acpReading describes a call from its arguments (its tool row's acp when
// known) — the headline when nothing better says it.
export function acpReading(name, a = {}, acp = null) {
  return reading({ kind: acpKind(name), rawInput: a, locations: acp ? acp.locations : null, title: acp ? acp.title : '', name: acp ? acp.tool : '' });
}

// acpSubline: an execute's command under a headline that says something
// else — `said`: the headline (default: the adapter's summary); '' when it
// is the command itself.
export function acpSubline(name, a = {}, said = undefined) {
  return subline({ kind: acpKind(name), rawInput: a }, said !== undefined ? said : typeof a.summary === 'string' ? a.summary : '');
}

// acpOutcome: what a finished harness call came to — an exit code, +a −d, a
// count — as {text, tone: ok | bad | ''}, or null (nothing to say; a
// failure is already the card's state).
export function acpOutcome(name, content, acp) {
  return acp ? outcome({ ...acp, kind: acpKind(name), text: content }) : null;
}

// acpState: the card's state (model/fold.js) — the tool row's placeholder
// says running or parked, as for any call (state, from resultState); ACP's
// own status says failed and cancelled.
export function acpState(acp, state) {
  if (acp && acp.status === 'failed') return 'error';
  if (acp && acp.status === 'cancelled') return 'stopped';
  return state;
}

// --- the cards (harness-cards.js on the web, native/harness-cards.js) --------------------

// isSubagentCall: a harness-internal subagent (a Claude Task) — the call
// says so, or blocks run under it (the fold's kids).
export const isSubagentCall = (b) => !!(b && ((b.acp && b.acp.subagent) || (b.kids && b.kids.length)));

// stepsWords: "3 steps" — the calls a subagent made (its kids that are calls).
export function stepsWords(kids) {
  const n = (kids || []).filter((k) => k.k === 'tool' || k.k === 'agent').length;
  return n ? `${n} step${n === 1 ? '' : 's'}` : '';
}

// resultText: what a finished call answered — '' while the tool row holds
// its placeholder (running, parked).
export const resultText = (b) => (b && b.result && !['running', 'approval', 'writing'].includes(b.state) ? b.result : '');
