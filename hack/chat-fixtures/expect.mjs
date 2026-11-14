// hack/chat-fixtures/expect.mjs — the shared transcripts' expectations
// (sdk/acp/testdata/transcripts): how <name>.records.json and
// <name>.blocks.json are made, and — GAPS — every field in which a
// reducer's blocks cannot equal them today, each with its reason. The two
// tests (hack/chat-fixtures.test.mjs, hack/chat-fixtures-agtt.test.mjs)
// compare a reducer's transcript with the expectation after putting both
// through that reducer's gaps; a gap closed is a line deleted here.
//
// The expectation is the block model's (/vendor/chat/blocks.js) reading of
// a log, at each of its checkpoints: the Agent tab's fold through its
// adapter (web.mjs), with what the model asks for and that fold has dropped
// put back from the log itself:
//   - a tool block's record is the one /vendor/chat/tools.js makes of the
//     log's events (records(): the id-reuse rule included);
//   - its state is `approval` while an unanswered permission request names
//     its call;
//   - a permission request names its call's label; the options that raise
//     the mode are marked in the log itself (`explicit`, by sdk/acp's
//     Provider.OptionRaises — the rule the agent template's backend parks
//     with too, so its views agree);
//   - a question says its mode and its url.
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { Fold } from '../../web/agent-fold.js';
import { walk, toolState, toolDone, plain } from '../../web/chat/blocks.js';
import { records } from '../../web/chat/tools.js';
import { webTranscript } from './web.mjs';

/**
 * compact(x): x as the fixtures hold it — a field at its zero value ('' / 0
 * / false / null / []) left out, at every depth: absent means zero. Never
 * left out: a block's k and kind, a checkpoint's seq and blocks, a tool
 * record's id, kind, title and status — and an exitCode of 0, whose zero
 * value is null (no exit status yet). What the agent sent whole (a call's
 * rawInput, a form's schema, an answer's content, a request's content list)
 * is kept as it came.
 */
const zero = (v, key) => v == null || v === '' || (v === 0 && key !== 'exitCode') || v === false || (Array.isArray(v) && v.length === 0);
const opaque = (key, x) => key === 'rawInput' || key === 'schema' || (key === 'content' && (x.kind === 'ask' || 'rawInput' in x || 'command' in x));

export function compact(x) {
  if (Array.isArray(x)) return x.map(compact);
  if (!x || typeof x !== 'object') return x;
  const record = 'id' in x && 'status' in x && 'kind' in x && !('k' in x);
  const out = {};
  for (const [key, val] of Object.entries(x)) {
    if (key[0] === '$' || val === undefined) continue;
    const keep = key === 'k' || key === 'kind' || key === 'seq' || key === 'blocks' || (record && (key === 'id' || key === 'title' || key === 'status'));
    if (zero(val, key) && !keep) continue;
    out[key] = opaque(key, x) ? val : compact(val);
  }
  return out;
}

export const DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'sdk', 'acp', 'testdata', 'transcripts');

const read = (f) => JSON.parse(readFileSync(path.join(DIR, f), 'utf8'));

/** load() → the transcripts: [{name, log, records, blocks, agtt}] (a file not there yet: null). */
export function load() {
  return readdirSync(DIR).filter((f) => /^[^.]+\.json$/.test(f)).sort().map((f) => {
    const name = f.slice(0, -5);
    const opt = (suffix) => (existsSync(path.join(DIR, `${name}.${suffix}.json`)) ? read(`${name}.${suffix}.json`) : null);
    return { name, log: read(f), records: opt('records'), blocks: opt('blocks'), agtt: opt('agtt') };
  });
}

const upTo = (log, seq) => log.events.filter((e) => e.seq <= seq);

/** explicitOf(log) → pid → Set of the option ids the log's permission requests mark explicit. */
export function explicitOf(log) {
  const out = new Map();
  for (const e of (log && log.events) || []) {
    if (e.type === 'permission.request') out.set(e.data.pid, new Set((e.data.options || []).filter((o) => o.explicit).map((o) => o.optionId)));
  }
  return out;
}

/** expectedAt(log, seq, explicit) → the expected Transcript after the log's events up to seq (not yet compact). */
export function expectedAt(log, seq, explicit = new Map()) {
  const events = upTo(log, seq);
  const tr = webTranscript(new Fold(events));
  const recs = new Map(records(events).map((r) => [String(r.seq), r.rec]));
  const waits = new Set(); // call ids an unanswered permission request names
  walk(tr.blocks, (b) => { if (b.kind === 'perm' && b.by == null && b.callId) waits.add(b.callId); });
  const newest = new Map(); // call id → its newest tool block
  walk(tr.blocks, (b) => {
    if (b.kind !== 'tool') return;
    const rec = recs.get(b.k);
    if (!rec) throw new Error(`${log.name}: no record begins at seq ${b.k}`);
    b.rec = rec;
    b.state = toolState(rec);
    newest.set(rec.id, b);
  });
  for (const id of waits) { const b = newest.get(id); if (b && !toolDone(b.rec.status)) b.state = toolState(b.rec, 'approval'); }
  walk(tr.blocks, (b) => {
    if (b.kind === 'perm') {
      const call = newest.get(b.callId);
      b.tool.label = call ? call.rec.label : '';
      const ex = explicit.get(b.pid);
      for (const o of b.options) o.explicit = !!(ex && ex.has(o.optionId));
    }
  });
  return tr;
}

/** shown(tr, seq): a Transcript as a checkpoint of <name>.blocks.json — plain, compact, without versions. */
export function shown(tr, seq) {
  const out = compact(plain({ seq, blocks: tr.blocks, tail: tr.tail, plan: tr.plan ? tr.plan.k : '', usage: tr.usage }));
  const strip = (x) => {
    if (Array.isArray(x)) return x.forEach(strip);
    if (x && typeof x === 'object') { if ('k' in x && 'kind' in x) { delete x.v; delete x.at; } Object.values(x).forEach(strip); }
  };
  strip(out);
  return out;
}

/** expectedFiles(t) → {records, blocks}: the two files beside a log, as objects. */
export function expectedFiles(t) {
  const explicit = explicitOf(t.log);
  return {
    records: {
      name: t.name,
      about: `the tool calls of ${t.name}.json after its last event, as ToolRecords (/vendor/chat/blocks.js), in the order they began — seq: the event that began the call; ` +
        'turn: the turn it was made in. A field at its zero value is left out.',
      records: records(t.log.events).map((r) => ({ seq: r.seq, turn: r.turn, rec: compact(r.rec) })),
    },
    blocks: {
      name: t.name,
      about: `the transcript of ${t.name}.json in the chat kit's block model (/vendor/chat/blocks.js) after each of its checkpoints — blocks; tail: what waits on a ` +
        "person beside them; plan: the key of the newest plan block; usage. A block's k is the seq of the event that opened it (the Agent tab's key); versions (v) and " +
        'clock times (at) are left out, and a field at its zero value too.',
      checkpoints: t.log.checkpoints.map((seq) => shown(expectedAt(t.log, seq, explicit), seq)),
    },
  };
}

export const json = (x) => JSON.stringify(x, null, 1) + '\n';

/** canon(x): x as JSON with every object's keys in order — two values are deep-equal exactly when their canon is the same text. */
export const canon = (x) => JSON.stringify(x, (_, v) => (v && typeof v === 'object' && !Array.isArray(v) ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => (a < b ? -1 : 1))) : v));

// ---- gaps ----------------------------------------------------------------------------

// each(x, fn): fn(block) for every block of a compact transcript or list, at any depth
export function each(x, fn) {
  if (Array.isArray(x)) return x.forEach((y) => each(y, fn));
  if (!x || typeof x !== 'object') return;
  if (typeof x.kind === 'string') fn(x); // (a checkpoint has no kind; everything in its lists is a block)
  for (const key of ['blocks', 'tail', 'children']) if (x[key]) each(x[key], fn);
}

// prune(x, drop): the compact transcript without the blocks drop(block) says, at any depth
function prune(x, drop) {
  for (const key of ['blocks', 'tail', 'children']) {
    if (!Array.isArray(x[key])) continue;
    x[key] = x[key].filter((b) => !drop(b));
    if (!x[key].length && key !== 'blocks') delete x[key];
    else x[key].forEach((b) => prune(b, drop));
  }
  return x;
}

/**
 * GAPS — where a reducer's blocks cannot equal the expectation today.
 * Each gap: id, why, and what puts both sides in a form that can be
 * compared — `both` runs on the reducer's transcript and on the
 * expectation, `expected` and `actual` on one. All work on compact
 * checkpoints ({seq, blocks, tail, plan, usage}).
 */
export const GAPS = {
  // the Agent tab's fold (web/agent-fold.js, web/agent-tools.js)
  web: [
    // (none: W1–W4 are closed — the fold keeps the kit's record, a question's mode, `explicit`, and ties a request to its call's card)
  ],
  // the agent template (its backend's rows, model/fold.js)
  agtt: [
    { id: 'A1', why: 'keys are the template\'s own row ids (m<id>, c<call id>, draft-text), not event seqs; `ref` is its handle of a message',
      both: (t) => each(t, (b) => { delete b.k; delete b.ref; }) },
    { id: 'A2', why: 'a turn\'s end and a turn\'s change set are never rows: the run\'s status and the summary\'s counts say them',
      expected: (t) => prune(t, (b) => b.kind === 'turn' || b.kind === 'changes') },
    { id: 'A3', why: 'the plan is the summary\'s (pinned in the top bar), not a row: compared as Transcript.plan',
      expected: (t) => prune(t, (b) => b.kind === 'plan') },
    { id: 'A4', why: 'a request is the run\'s park: drawn under the transcript while it waits (Transcript.tail), not drawn once answered',
      expected: (t) => {
        const waiting = [];
        each(t.blocks, (b) => { if ((b.kind === 'perm' && b.by == null) || (b.kind === 'ask' && b.action == null)) waiting.push(b); });
        prune(t, (b) => b.kind === 'perm' || b.kind === 'ask');
        if (waiting.length) t.tail = [...waiting, ...(t.tail || [])];
      } },
    { id: 'A5', why: 'a url question is answered by the backend itself (declined, or shown as the sign-in\'s device code), never asked in the transcript',
      expected: (t) => { t.tail = (t.tail || []).filter((b) => !(b.kind === 'ask' && b.mode === 'url')); if (!t.tail.length) delete t.tail; } },
    { id: 'A6', why: 'journal lines (`step`: an error, a note) are the template\'s own kind; the test pins which appear',
      actual: (t) => prune(t, (b) => b.kind === 'step') },
    { id: 'A7', why: 'the answer being written is a draft (its row does not exist yet), and a row carries no message id',
      both: (t) => each(t, (b) => { if (b.kind === 'draft') b.kind = 'assistant'; if (b.kind === 'assistant') delete b.mid; }) },
    { id: 'A8', why: 'a user row says who sent it; the event log does not',
      both: (t) => each(t, (b) => { if (b.kind === 'user') delete b.sender; }) },
    { id: 'A9', why: 'a coding agent\'s thinking is not timed (no reasoningMs on its rows)',
      both: (t) => each(t, (b) => { if (b.kind === 'thought') delete b.ms; }) },
    { id: 'A10', why: 'a call\'s arguments are rawInput\'s fields plus a summary: none and {} read the same; the lift has no terminal mark, and no text while the call runs',
      both: (t) => each(t, (b) => {
        if (b.kind !== 'tool') return;
        delete b.rec.terminal;
        if (b.rec.rawInput && typeof b.rec.rawInput === 'object' && !Object.keys(b.rec.rawInput).length) delete b.rec.rawInput;
        if (b.rec.status === 'pending' || b.rec.status === 'in_progress') delete b.rec.text;
      }) },
    { id: 'A11', why: 'an edit\'s diff is the backend\'s patch (the texts are not kept); the log\'s is the two texts',
      both: (t) => each(t, (b) => { if (b.kind === 'tool') for (const d of b.rec.diffs || []) { delete d.patch; delete d.truncated; delete d.oldText; delete d.newText; } }) },
    { id: 'A12', why: 'what a call changed on disk (files.changed) is xbind\'s; the template has no snapshots',
      expected: (t) => each(t, (b) => { if (b.kind === 'tool') delete b.rec.changed; }) },
    { id: 'A13', why: 'the park keeps neither the request\'s own heading (meta.title) nor, for a plan, the content list (the plan is said once)',
      both: (t) => each(t, (b) => { if (b.kind === 'perm') { delete b.title; delete b.park; if (b.plan) delete b.tool.content; } if (b.kind === 'ask') delete b.park; }) },
    { id: 'A14', why: 'the sign-in is a park too: it has the park\'s token',
      both: (t) => each(t, (b) => { if (b.kind === 'signin') delete b.park; }) },
  ],
};

/** through(side, t, which, without): a compact checkpoint put through side's gaps (which: 'expected' | 'actual') — all but the gap `without`. */
export function through(side, t, which, without = '') {
  const out = structuredClone(t);
  for (const g of GAPS[side]) {
    if (g.id === without) continue;
    if (g.both) g.both(out);
    if (g[which]) g[which](out);
  }
  return out;
}
