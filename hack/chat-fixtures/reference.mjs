// hack/chat-fixtures/reference.mjs — the tool record's rules, executable:
// an event log (xbind's served shape) → the ToolRecords of
// /vendor/chat/blocks.js, in the order the calls began. This is the
// specification the shared fixtures' <name>.records.json are made from
// (hack/chat-fixtures.test.mjs) and what the one implementation to come —
// sdk/acp's tool record in Go, /vendor/chat/tools.js in the browser — must
// reproduce; until then it lives here, with the tests, and ships nowhere.
//
// The rules (each the better of what the Agent tab's fold and the agent
// template's backend do today; the spec says which was whose):
//
//   - a field an event carries replaces the record's; `outputDelta` appends
//     to the output, `output` replaces what was gathered; `parent` is the
//     first one said;
//   - `content` (ACP's list) is read into `text` (its text items, joined by
//     a blank line), `diffs` (its diff items) and `terminal`; a list with
//     no text keeps the text there was, one with no diffs keeps the diffs;
//   - `files` is the paths of the locations, then of the diffs, each once;
//   - a tool.call for an id whose record is over (completed, failed,
//     cancelled) or was made in an earlier turn starts a new record; any
//     other event for a held id updates it;
//   - a files.changed naming a call sets that call's `changed` — the newest
//     record of the id, the turn may be over.
import { newToolRecord, toolDone, TOOL_KINDS } from '../../web/chat/blocks.js';

export const OUTPUT_MAX = 64 << 10; // bytes of output a capping reducer keeps (the newest)

export const startsNewCall = (rec, type, sameTurn = true) => !rec || (type === 'tool.call' && (toolDone(rec.status) || !sameTurn));

export function mergeTool(rec, d, opts = {}) {
  if (d.title != null) rec.title = String(d.title);
  if (d.kind != null) rec.kind = TOOL_KINDS.includes(d.kind) ? d.kind : 'other';
  if (d.status) rec.status = d.status;
  if (d.name) rec.name = d.name;
  if (d.label) rec.label = d.label;
  if (d.parent && !rec.parent) rec.parent = d.parent;
  if (d.subagent) rec.subagent = true;
  if (d.planReview) rec.planReview = true;
  if (d.rawInput != null) rec.rawInput = d.rawInput;
  if (Array.isArray(d.locations)) rec.locations = d.locations.filter((l) => l && l.path).map((l) => (l.line != null ? { path: l.path, line: l.line } : { path: l.path }));
  if (Array.isArray(d.content)) {
    const text = [], diffs = [];
    for (const it of d.content) {
      if (!it || typeof it !== 'object') continue;
      if (it.type === 'diff') diffs.push(toolDiff(it, rec));
      else if (it.type === 'terminal') rec.terminal = true;
      else {
        const s = it.content?.text ?? (it.type === 'text' ? it.text : undefined);
        if (typeof s === 'string' && s !== '') text.push(s);
      }
    }
    if (text.length) rec.text = text.join('\n\n');
    if (diffs.length) rec.diffs = diffs;
  }
  if (typeof d.output === 'string') { rec.output = ''; rec.outputTruncated = 0; addOutput(rec, d.output, opts.cap); }
  if (typeof d.outputDelta === 'string') addOutput(rec, d.outputDelta, opts.cap);
  if (d.exitCode != null) rec.exitCode = d.exitCode;
  const files = [];
  for (const x of [...rec.locations, ...rec.diffs]) if (x.path && !files.includes(x.path)) files.push(x.path);
  rec.files = files;
  return rec;
}

const utf8 = (c) => (c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4);

// the output gathered, capped to its newest OUTPUT_MAX bytes when asked (cut
// between characters; the Go side counts bytes too)
function addOutput(rec, s, cap) {
  rec.output += s;
  if (!cap) return;
  let size = 0;
  for (let i = 0; i < rec.output.length; i++) { const c = rec.output.codePointAt(i); size += utf8(c); if (c > 0xffff) i++; }
  let i = 0, dropped = 0;
  while (size - dropped > OUTPUT_MAX) { const c = rec.output.codePointAt(i); dropped += utf8(c); i += c > 0xffff ? 2 : 1; }
  rec.output = rec.output.slice(i);
  rec.outputTruncated += dropped;
}

// toolDiff: an ACP diff item as a ToolDiff with both texts — added when it
// has no old text, deleted when the call is a delete, else modified
export function toolDiff(item, rec) {
  const isNew = item.oldText == null;
  const deleted = !isNew && !!rec && rec.kind === 'delete';
  const { add, del } = lineCounts(isNew ? [] : textLines(item.oldText), deleted ? [] : textLines(item.newText));
  return { path: String(item.path ?? ''), status: isNew ? 'added' : deleted ? 'deleted' : 'modified', add, del, oldText: isNew ? null : String(item.oldText), newText: String(item.newText ?? '') };
}

// a text's lines: a final newline ends the last line, it doesn't start another
export function textLines(s) {
  const t = String(s ?? '');
  if (t === '') return [];
  const out = t.split('\n');
  if (out[out.length - 1] === '') out.pop();
  return out;
}

const DIFF_CELLS = 4000000; // past this an edit's changed middle counts as replaced whole

// lineCounts: lines of b not in a (add) and of a not in b (del), by the
// longest common subsequence between their common head and tail
export function lineCounts(a, b) {
  let p = 0;
  while (p < a.length && p < b.length && a[p] === b[p]) p++;
  let s = 0;
  while (s < a.length - p && s < b.length - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++;
  const n = a.length - p - s, m = b.length - p - s;
  if (!n || !m || n * m > DIFF_CELLS) return { add: m, del: n };
  let prev = new Uint32Array(m + 1), cur = new Uint32Array(m + 1);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) cur[j] = a[p + i] === b[p + j] ? prev[j + 1] + 1 : Math.max(prev[j], cur[j + 1]);
    [prev, cur] = [cur, prev];
    cur.fill(0);
  }
  return { add: m - prev[0], del: n - prev[0] };
}

// commandOf: the shell command a call's rawInput names ('' none)
export function commandOf(rawInput) {
  if (!rawInput || typeof rawInput !== 'object') return '';
  const c = rawInput.command ?? rawInput.cmd;
  return Array.isArray(c) ? c.join(' ') : typeof c === 'string' ? c : '';
}

/**
 * records(events, opts) → [{seq, turn, rec}]: every call of the log, in the
 * order they began — seq the event that began it (the Agent tab's block
 * key), turn the turn it was made in (1, 2, …).
 */
export function records(events, opts = {}) {
  const out = [];
  const held = new Map(); // id → its newest entry
  let turn = 1;
  for (const e of events) {
    const d = e.data || {};
    switch (e.type) {
      case 'tool.call': case 'tool.update': {
        if (!d.id) break;
        let h = held.get(d.id);
        if (startsNewCall(h && h.rec, e.type, !h || h.turn === turn)) {
          h = { seq: e.seq, turn, rec: newToolRecord(d.id) };
          held.set(d.id, h);
          out.push(h);
        }
        mergeTool(h.rec, d, opts);
        break;
      }
      case 'files.changed': {
        const h = d.toolCallId ? held.get(d.toolCallId) : null;
        if (h) h.rec.changed = changeSet(d);
        break;
      }
      case 'turn.end':
        turn = (d.turn || turn) + 1;
        break;
    }
  }
  return out;
}

// changeSet: a files.changed event's data as a ChangeSet
export function changeSet(d) {
  return {
    files: (d.changes || []).map((c) => ({ ...c })),
    patch: d.patch && typeof d.patch.text === 'string' ? { text: d.patch.text, truncated: !!d.patch.truncated } : null,
  };
}

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
