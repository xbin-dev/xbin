/**
 * chat/tools.js — one tool call, for the chat kit (/vendor/chat/tools.js).
 *
 * Everything the kit says about a call is read from its ToolRecord
 * (/vendor/chat/blocks.js): this module makes that record from an agent's
 * tool.call / tool.update events, and derives what a card shows — its
 * headline, the command under it, what it came to, its state chip, its
 * output's tail, its diffs, whether it asks for a plan's approval. Both
 * reducers stand on it: the Agent tab's fold merges events with mergeTool
 * (/vendor/agent-tools.js keeps its old record beside it), a tile with rows
 * of its own converts them to records (the agent template's
 * model/harness-heads.js) and reads the same words.
 *
 * Pure — no lit, no DOM, one import (the block model) — so a native view
 * and node tests load it too. Every function takes plain data and tolerates
 * a record with fields left out (absent means the zero value); exports only
 * grow (/vendor/chat/rev.js).
 */
import { newToolRecord, toolDone, TOOL_KINDS } from './blocks.js';

// ---- the record: an agent's events merged ----------------------------------------------

/** Bytes of output a capping reducer keeps: the newest (the bytes dropped are counted in outputTruncated). */
export const OUTPUT_MAX = 64 << 10;

/**
 * startsNewCall(rec, type, sameTurn): does this event begin another call?
 * An agent may use an id again: a tool.call for an id whose record is over
 * (completed, failed, cancelled), or was made in an earlier turn, starts a
 * new record; a tool.update never does.
 */
export const startsNewCall = (rec, type, sameTurn = true) => !rec || (type === 'tool.call' && (toolDone(rec.status) || !sameTurn));

/**
 * mergeTool(rec, d, opts): one tool.call / tool.update event's data into the
 * record.
 *   - a field the event carries replaces the record's; `parent` is the first
 *     one said; an unknown kind reads as 'other';
 *   - `content` (ACP's list) is read into `text` (its text items, joined by
 *     a blank line), `diffs` (its diff items) and `terminal`; a list with no
 *     text keeps the text there was, one with no diffs keeps the diffs;
 *   - `output` replaces what was gathered, then `outputDelta` appends;
 *     opts.cap keeps the newest OUTPUT_MAX bytes;
 *   - `files` is the paths of the locations, then of the diffs, each once.
 */
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
        const s = itemText(it);
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

// a content item's text: ACP's {type:'content', content:{type:'text', text}}, or a bare {type:'text', text}
const itemText = (it) => it && (it.content?.text ?? (it.type === 'text' ? it.text : undefined));

/** firstText(content): the first text item of an ACP content list that says something ('' none). */
export function firstText(content) {
  for (const it of Array.isArray(content) ? content : []) {
    const s = itemText(it);
    if (typeof s === 'string' && s.trim()) return s;
  }
  return '';
}

const utf8 = (c) => (c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4);

// the output gathered, capped to its newest OUTPUT_MAX bytes when asked (cut between characters)
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

/**
 * toolDiff(item, rec): an ACP diff item as a ToolDiff in its two-texts form
 * — added when it has no old text, deleted when the call is a delete, else
 * modified — with its line counts.
 */
export function toolDiff(item, rec) {
  const isNew = item.oldText == null;
  const deleted = !isNew && !!rec && rec.kind === 'delete';
  const { add, del } = lineCounts(isNew ? [] : textLines(item.oldText), deleted ? [] : textLines(item.newText));
  return { path: String(item.path ?? ''), status: isNew ? 'added' : deleted ? 'deleted' : 'modified', add, del, oldText: isNew ? null : String(item.oldText), newText: String(item.newText ?? '') };
}

/** textLines(s): a text's lines — a final newline ends the last line, it doesn't start another. */
export function textLines(s) {
  const t = String(s ?? '');
  if (t === '') return [];
  const out = t.split('\n');
  if (out[out.length - 1] === '') out.pop();
  return out;
}

const DIFF_CELLS = 4000000; // past this an edit's changed middle counts as replaced whole

/** lineCounts(a, b): lines of b not in a (add) and of a not in b (del), by the longest common subsequence between their common head and tail. */
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

/** changeSet(d): a files.changed event's data ({changes, patch}) as a ChangeSet. */
export function changeSet(d) {
  return {
    files: ((d && d.changes) || []).map((c) => ({ ...c })),
    patch: d && d.patch && typeof d.patch.text === 'string' ? { text: d.patch.text, truncated: !!d.patch.truncated } : null,
  };
}

/**
 * ToolRecords folds a session's tool events into records, the id-reuse rule
 * included. apply(e) takes any event ({seq, type, data}) and answers the
 * entry it touched — {seq, turn, rec}: seq the event that began the call,
 * turn the turn it was made in (1, 2, …) — or null; `list` is every entry in
 * the order the calls began. opts.cap: keep each output's newest OUTPUT_MAX
 * bytes.
 */
export class ToolRecords {
  constructor(opts = {}) {
    this.opts = opts;
    this.list = [];
    this.held = new Map(); // id → its newest entry
    this.turn = 1;
  }

  apply(e) {
    const d = (e && e.data) || {};
    switch (e && e.type) {
      case 'tool.call': case 'tool.update': {
        if (!d.id) return null;
        let h = this.held.get(d.id);
        if (startsNewCall(h && h.rec, e.type, !h || h.turn === this.turn)) {
          h = { seq: e.seq, turn: this.turn, rec: newToolRecord(d.id) };
          this.held.set(d.id, h);
          this.list.push(h);
        }
        mergeTool(h.rec, d, this.opts);
        return h;
      }
      case 'files.changed': { // what a call changed on disk: its newest record's (the turn may be over)
        const h = d.toolCallId ? this.held.get(d.toolCallId) : null;
        if (h) h.rec.changed = changeSet(d);
        return h || null;
      }
      case 'turn.end':
        this.turn = (d.turn || this.turn) + 1;
        return null;
    }
    return null;
  }
}

/** records(events, opts) → [{seq, turn, rec}]: every call of an event log, in the order they began. */
export function records(events, opts = {}) {
  const r = new ToolRecords(opts);
  for (const e of events) r.apply(e);
  return r.list;
}

// ---- words: what a call is ----------------------------------------------------------------

/** firstLine(s): a text's first line, " …" after it when more follow. */
export const firstLine = (s) => { const t = String(s ?? '').trim(); const i = t.indexOf('\n'); return i < 0 ? t : t.slice(0, i) + ' …'; };

/** oneLine(s, n): a text on one line (whitespace runs as one space), at most n characters. */
export const oneLine = (s, n = 90) => {
  const t = String(s ?? '').replace(/\s+/g, ' ').trim();
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};

/**
 * inputCommand(rawInput): the shell command a call's arguments name ('' none)
 * — `command` (a string, or an argv list), else `cmd` or `script`; a string
 * command's `args` list follows it.
 */
export function inputCommand(rawInput) {
  if (!rawInput || typeof rawInput !== 'object') return '';
  const c = rawInput.command ?? rawInput.cmd ?? rawInput.script;
  if (typeof c === 'string') return c + (Array.isArray(rawInput.args) ? ' ' + rawInput.args.join(' ') : '');
  return Array.isArray(c) ? c.join(' ') : '';
}

/** commandOf(rec): the command of a shell call — its arguments', else an execute call's title (claude, opencode and codex title one with the command). */
export function commandOf(rec) {
  const r = rec && rec.rawInput;
  if (r && typeof r === 'object') {
    const c = r.command ?? r.cmd ?? r.script;
    if (typeof c === 'string' || Array.isArray(c)) return inputCommand(r);
  }
  return rec && rec.kind === 'execute' ? String(rec.title || '') : '';
}

/** inputPath(rawInput): the file a call's arguments name, under the names adapters use ('' none). */
export function inputPath(rawInput) {
  const p = rawInput && (rawInput.file_path ?? rawInput.path ?? rawInput.filePath ?? rawInput.notebook_path ?? rawInput.abs_path);
  return typeof p === 'string' ? p : '';
}

/**
 * headline(rec): what a call's card is called. In order: the harness's own
 * description (`label`; a `description` among the arguments), a shell
 * command read deterministically (describeCommand — while the call runs, a
 * short one-line text content says it first: gemini's "[in dir]
 * (description)"), the title's first line, a reading of the arguments by
 * kind (reading), the tool's name, the call's id. opts.reading false leaves
 * the reading by kind out.
 */
export function headline(rec, opts = {}) {
  if (!rec) return '';
  if (rec.label) return rec.label;
  const desc = rec.rawInput && typeof rec.rawInput.description === 'string' ? rec.rawInput.description : '';
  if (desc) return desc;
  const cmd = commandOf(rec);
  if (cmd) {
    if (rec.kind === 'execute' && (rec.status === 'pending' || rec.status === 'in_progress')) {
      const c = String(rec.text || '');
      if (c.trim() && !c.includes('\n') && c.length < 160) return c;
    }
    return describeCommand(cmd);
  }
  return firstLine(rec.title) || (opts.reading !== false && rec.kind && rec.kind !== 'other' ? reading(rec) : '') || rec.name || rec.id || 'a tool call';
}

/**
 * reading(rec): a call said from its arguments, by kind — "Read a.go:10–49",
 * "Search /pat/ in src", "$ ls" — for a call nothing better describes (no
 * label, no title).
 */
export function reading(rec) {
  const a = rec && rec.rawInput && typeof rec.rawInput === 'object' ? rec.rawInput : {};
  const title = (rec && rec.title) || '';
  const path = inputPath(a) || (rec && rec.locations && rec.locations[0] && rec.locations[0].path) || '';
  switch (rec && rec.kind) {
    case 'execute': { const c = inputCommand(a); return c ? `$ ${oneLine(c, 90)}` : title || 'Run a command'; }
    case 'read': {
      const from = Number(a.offset) || 0, n = Number(a.limit) || 0;
      return `Read ${path || 'a file'}${n ? `:${from || 1}–${(from || 1) + n - 1}` : from ? `:${from}–` : ''}`;
    }
    case 'edit': return `Edit ${path || 'a file'}`;
    case 'delete': return `Delete ${path || 'a file'}`;
    case 'move': return `Move ${a.source || a.from || path || '?'} → ${a.destination || a.to || '?'}`;
    case 'search': return `Search /${oneLine(a.pattern ?? a.query ?? '', 50)}/${a.path ? ` in ${a.path}` : ''}`;
    case 'fetch': return oneLine(a.url || a.query || title || 'Fetch', 90);
    case 'think': return oneLine(a.description || title || 'Think', 90);
    case 'switch_mode': return 'Switch mode';
  }
  return oneLine(title || (rec && rec.name) || 'tool', 90);
}

/**
 * subline(rec, said): an execute call's command, as "$ …", for under a
 * headline that says something else ('' when `said` — default: its headline
 * — is the command itself, or the call names none).
 */
export function subline(rec, said = headline(rec)) {
  if (!rec || rec.kind !== 'execute') return '';
  const c = inputCommand(rec.rawInput);
  const s = String(said ?? '').trim();
  return c && s && s !== c ? `$ ${oneLine(c, 90)}` : '';
}

/**
 * describeCommand(cmd): a shell command read deterministically — a heredoc
 * script becomes "Python script (N lines) → main.go"; sed -i / perl -pi /
 * cat > / tee / > name the file they write; rm, mkdir and mv say so;
 * anything else is its first line.
 */
export function describeCommand(cmd) {
  let c = String(cmd ?? '').trim();
  const cd = c.match(/^cd\s+(\S+)\s*&&\s*/);
  if (cd) c = c.slice(cd[0].length);
  const here = c.match(/^([\w./-]*?(python3?|node|deno|bun|ruby|perl|php|bash|sh|zsh))\b[^\n]*?<<-?\s*(['"]?)(\w+)\3[^\n]*\n([\s\S]*?)\n\4\s*$/);
  if (here) {
    const lang = { python: 'Python', python3: 'Python', node: 'Node', deno: 'Deno', bun: 'Bun', ruby: 'Ruby', perl: 'Perl', php: 'PHP', bash: 'Shell', sh: 'Shell', zsh: 'Shell' }[here[2]] || here[2];
    const body = here[5];
    const n = body.split('\n').length;
    return `${lang} script (${n} line${n === 1 ? '' : 's'})${targets(scriptTargets(body))}`;
  }
  const catW = c.match(/^cat\s+>>?\s*(['"]?)([^\s'"<>|;&]+)\1\s*<</);
  if (catW) return `Write ${catW[2]}`;
  const inPlace = c.match(/^(sed|perl)\b[^\n|;&]*\s-[a-zA-Z]*[ip][a-zA-Z]*\b[^\n|;&]*?\s(['"]?)([^\s'"|;&]+)\2\s*$/);
  if (inPlace && /\s-[a-zA-Z]*i/.test(c)) return `Edit ${inPlace[3]} (${inPlace[1]})`;
  const tee = c.match(/\|\s*tee\s+(?:-a\s+)?(['"]?)([^\s'"|;&]+)\1\s*$/);
  if (tee) return `Write ${tee[2]}`;
  const redir = c.match(/^[^\n]*?[^2&0-9]>>?\s*(['"]?)([^\s'"|;&<>]+)\1\s*$/);
  if (redir && !c.includes('\n') && redir[2] !== '/dev/null') return `Write ${redir[2]}`;
  const rm = c.match(/^rm\s+(?:-\w+\s+)*(.+)$/);
  if (rm && !c.includes('\n')) return `Remove ${rm[1]}`;
  const mk = c.match(/^mkdir\s+(?:-\w+\s+)*(.+)$/);
  if (mk && !c.includes('\n')) return `Create ${mk[1]}`;
  const mv = c.match(/^mv\s+(?:-\w+\s+)*(\S+)\s+(\S+)$/);
  if (mv) return `Move ${mv[1]} → ${mv[2]}`;
  return firstLine(c);
}

// the files a script body names (open('x'), Path('x'), p='x', read/writeFileSync)
function scriptTargets(body) {
  const out = [];
  const re = /(?:open|Path|readFileSync|writeFileSync|readFile|writeFile|read_text|write_text)\(\s*(['"])([^'"\n]+)\1|(?:^|[\s;(])(?:p|path|fn|file|filename|target)\s*=\s*(['"])([^'"\n]+)\3/gm;
  for (const m of body.matchAll(re)) {
    const f = m[2] || m[4];
    if (f && /[./]/.test(f) && !out.includes(f)) out.push(f);
  }
  return out;
}

const targets = (fs) => (!fs.length ? '' : ' → ' + fs.slice(0, 2).join(', ') + (fs.length > 2 ? ` +${fs.length - 2}` : ''));

/** rawText(raw): a call's arguments for display — a shell command verbatim (the common {command}/{cmd}/{script} shapes), else compact JSON. */
export function rawText(raw) {
  if (raw == null) return '';
  if (typeof raw === 'string') return raw;
  if (typeof raw === 'object') {
    const cmd = raw.command ?? raw.cmd ?? raw.script;
    if (typeof cmd === 'string') return cmd + (Array.isArray(raw.args) ? ' ' + raw.args.join(' ') : '');
    try { return JSON.stringify(raw, null, 1); } catch { return ''; }
  }
  return String(raw);
}

/** taskOf(rawInput): what a subagent call was asked (a Task's prompt, else its description). */
export function taskOf(rawInput) {
  const t = rawInput && (rawInput.prompt ?? rawInput.task ?? rawInput.description);
  return typeof t === 'string' ? t : '';
}

/** placesOf(rec): the paths (and lines) a call touched — "path:line" for each location, else its files. */
export function placesOf(rec) {
  const locs = (rec && Array.isArray(rec.locations)) ? rec.locations.filter((l) => l && l.path) : [];
  if (locs.length) return locs.map((l) => (l.line ? `${l.path}:${l.line}` : l.path));
  return (rec && Array.isArray(rec.files)) ? rec.files.map(String) : [];
}

// ---- what a call came to, and its state ---------------------------------------------------

/** diffTotals(rec): the files an edit changed and the lines added and deleted. */
export function diffTotals(rec) {
  const ds = (rec && Array.isArray(rec.diffs)) ? rec.diffs : [];
  return { files: ds.length, add: ds.reduce((n, d) => n + (Number(d.add) || 0), 0), del: ds.reduce((n, d) => n + (Number(d.del) || 0), 0) };
}

/**
 * outcome(rec): what a completed call came to, for a chip on its card —
 * {text, tone: 'ok' | 'bad' | ''}: an execute's exit code, an edit's
 * "+a −d" (with the file count past one), a delete's "−d", the lines a read
 * answered (its text), a search's matches (its locations) — or null:
 * nothing to say (a failure is the card's state, not an outcome).
 */
export function outcome(rec) {
  if (!rec || rec.status !== 'completed') return null;
  switch (rec.kind) {
    case 'execute': {
      if (rec.exitCode == null) return null;
      const n = Number(rec.exitCode);
      return { text: `exit ${n}`, tone: n === 0 ? 'ok' : 'bad' };
    }
    case 'edit': {
      const t = diffTotals(rec);
      if (!t.files) return null;
      return { text: `${t.files > 1 ? `${t.files} files ` : ''}+${t.add} −${t.del}`, tone: '' };
    }
    case 'delete': {
      const t = diffTotals(rec);
      return t.del ? { text: `−${t.del}`, tone: '' } : null;
    }
    case 'read': {
      const c = String(rec.text ?? '').replace(/\n+$/, '');
      if (!c) return null;
      const n = c.split('\n').length;
      return { text: `${n} line${n === 1 ? '' : 's'}`, tone: '' };
    }
    case 'search': {
      const n = Array.isArray(rec.locations) ? rec.locations.length : 0;
      return n ? { text: `${n} match${n === 1 ? '' : 'es'}`, tone: '' } : null;
    }
  }
  return null;
}

/**
 * stateChip(rec, state): a call's state as its card's chip — {text, tone:
 * 'warn' | 'bad' | 'run' | ''} — or null once it completed (its outcome
 * says what it came to). `state` is the tool block's (one of TOOL_STATES):
 * what the reducer knows beside the record's status.
 */
export function stateChip(rec, state = '') {
  const s = rec && rec.status;
  if (state === 'approval') return { text: 'needs approval', tone: 'warn' };
  if (s === 'failed' || state === 'error') return { text: 'failed', tone: 'bad' };
  if (s === 'cancelled' || state === 'stopped') return { text: 'cancelled', tone: '' };
  if (state === 'writing') return { text: 'writing', tone: 'run' };
  if (s === 'pending') return { text: 'pending', tone: '' };
  if (s === 'in_progress' || state === 'running') return { text: 'running', tone: 'run' };
  return null;
}

// ---- output -------------------------------------------------------------------------------

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[@-_]|\x9b[0-?]*[ -/]*[@-~]/g;

/**
 * stripAnsi(s): terminal output without its escapes — CSI sequences (colour,
 * cursor; parameters 0x30–0x3F, so colon sub-parameters too; the 8-bit CSI
 * as well), OSC strings up to their BEL or ST (a title, a hyperlink — the
 * linked text stays), character-set designations (ESC ( B) and the two-byte
 * escapes ESC @ … ESC _. Everything else is kept as it came (line endings,
 * other control characters: a view's business).
 */
export const stripAnsi = (s) => String(s ?? '').replace(ANSI, '');

/** The characters of output a card shows before "show all". */
export const OUTPUT_TAIL = 20000;

/**
 * outputTail(rec, n): a call's output, escapes stripped, its last n
 * characters — {text, cut, dropped}: cut the characters left out here,
 * dropped the bytes the reducer kept out before them (outputTruncated).
 */
export function outputTail(rec, n = OUTPUT_TAIL) {
  const t = stripAnsi(rec && rec.output);
  const dropped = Number(rec && rec.outputTruncated) || 0;
  return t.length > n ? { text: t.slice(-n), cut: t.length - n, dropped } : { text: t, cut: 0, dropped };
}

// ---- diffs --------------------------------------------------------------------------------

/** filesStat(set): a change set's totals — {n, add, del} of its files (a ChangeSet's `files`; a files.changed record's `changes`). */
export function filesStat(set) {
  const cs = set && Array.isArray(set.files) ? set.files : set && Array.isArray(set.changes) ? set.changes : [];
  let add = 0, del = 0;
  for (const c of cs) { add += c.add || 0; del += c.del || 0; }
  return { n: cs.length, add, del };
}

/**
 * diffFiles(rec): a call's diffs with every field present — {path, status,
 * add, del, patch, truncated}, and oldText / newText on one that came as
 * its two texts (diffPatch makes either form a patch).
 */
export function diffFiles(rec) {
  return ((rec && Array.isArray(rec.diffs)) ? rec.diffs : []).filter((d) => d && d.path).map((d) => ({
    path: String(d.path), status: d.status || 'modified', add: Number(d.add) || 0, del: Number(d.del) || 0,
    patch: typeof d.patch === 'string' ? d.patch : '', truncated: !!d.truncated,
    ...('newText' in d || 'oldText' in d ? { oldText: d.oldText ?? null, newText: d.newText ?? '' } : {}),
  }));
}

/** diffPatch(d): one ToolDiff as unified diff text — its patch, else made from its two texts ('' when it has neither). */
export function diffPatch(d) {
  if (!d) return '';
  if (typeof d.patch === 'string' && d.patch) return d.patch;
  return 'newText' in d || 'oldText' in d ? unifiedDiff(d.path, d.oldText, d.newText) : '';
}

/**
 * unifiedDiff(path, oldText, newText): a git-style unified diff of two whole
 * texts, by a line LCS — one hunk, every line. Very large inputs fall back
 * to replace-all instead of an O(nm) table.
 */
export function unifiedDiff(path, oldText, newText) {
  const a = String(oldText ?? '').split('\n'), b = String(newText ?? '').split('\n');
  const p = path || 'file';
  if ((oldText ?? '') === (newText ?? '')) return `diff --git a/${p} b/${p}\n`;
  const n = a.length, m = b.length;
  let body;
  if (n * m > 400000) {
    body = a.map((l) => '-' + l).concat(b.map((l) => '+' + l));
  } else {
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    body = []; let i = 0, j = 0;
    while (i < n && j < m) {
      if (a[i] === b[j]) { body.push(' ' + a[i]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) { body.push('-' + a[i]); i++; }
      else { body.push('+' + b[j]); j++; }
    }
    while (i < n) body.push('-' + a[i++]);
    while (j < m) body.push('+' + b[j++]);
  }
  return `diff --git a/${p} b/${p}\n--- a/${p}\n+++ b/${p}\n@@ -1,${n} +1,${m} @@\n` + body.join('\n') + '\n';
}

/**
 * joinPatches(rec, max): a call's diffs as one unified diff, whole files
 * only while it stays under max characters — {patch, files, left}: files
 * the diffs (diffFiles), left how many were left out of the patch.
 */
export function joinPatches(rec, max = Infinity) {
  const files = diffFiles(rec);
  let patch = '', left = 0;
  for (const d of files) {
    const text = diffPatch(d);
    if (!text) continue;
    const p = text.endsWith('\n') ? text : text + '\n';
    if (patch && patch.length + p.length > max) { left++; continue; }
    if (!patch && p.length > max) { left++; continue; }
    patch += p;
  }
  return { patch, files, left };
}

/**
 * patchLines(patch): a unified diff's lines, each with its class — fh (a
 * file header), h (a hunk header), d (added), a (deleted), ctx — the classes
 * /vendor/bx-code.js's diffHTML gives them: a page draws a patch plain with
 * these until (or without) that highlighter, under one stylesheet.
 */
export function patchLines(patch) {
  const out = [];
  for (const raw of String(patch || '').replace(/\n$/, '').split('\n')) {
    let cls = 'fh';
    if (/^(--- |\+\+\+ )(a\/|b\/|\/dev\/null)|^(diff --git|index |new file|deleted file|similarity |rename )/.test(raw)) cls = 'fh';
    else if (raw.startsWith('@@')) cls = 'h';
    else if (raw[0] === '+') cls = 'd';
    else if (raw[0] === '-') cls = 'a';
    else if (raw[0] === ' ' || raw === '') cls = 'ctx';
    out.push({ cls, text: raw });
  }
  return out;
}

const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };

/** The lines of a diff a request's card previews. */
export const PREVIEW_LINES = 60;

/**
 * lineDiff(path, oldText, newText): old → new as the lines that changed,
 * with two lines of context each side, the common head and tail cut —
 * {path, lines: [{t: '+' | '-' | ' ' | '@', text}], add, del, more}: enough
 * for a preview (at most PREVIEW_LINES lines; `more` counts the rest).
 */
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

/**
 * contentPreview(content): an ACP content list (a request's tool.content) as
 * what a card previews — a lineDiff per diff item, {text} per text item —
 * or null when it has neither.
 */
export function contentPreview(content) {
  const out = [];
  for (const c of Array.isArray(content) ? content : []) {
    if (!c || typeof c !== 'object') continue;
    if (c.type === 'diff' && c.path) out.push(lineDiff(c.path, c.oldText, c.newText));
    else if (c.type === 'content' && c.content && c.content.type === 'text' && c.content.text) out.push({ text: clip(c.content.text, 2000) });
  }
  return out.length ? out : null;
}

/** diffsPreview(diffs): a record's patch-form diffs previewed the same way (null: none has a patch). */
export function diffsPreview(diffs) {
  const out = (Array.isArray(diffs) ? diffs : []).filter((d) => d && d.patch).map((d) => {
    const lines = String(d.patch).split('\n').filter((l) => l && !/^(---|\+\+\+) /.test(l))
      .map((l) => ({ t: l[0] === '@' ? '@' : '+- '.includes(l[0]) ? l[0] : ' ', text: l[0] === '@' ? l : l.slice(1) }));
    return { path: d.path, lines: lines.slice(0, PREVIEW_LINES), add: d.add || 0, del: d.del || 0, more: Math.max(0, lines.length - PREVIEW_LINES) };
  });
  return out.length ? out : null;
}

/** patchOf(d): a preview's diff (lineDiff, diffsPreview) as unified patch text. */
export const patchOf = (d) => [`--- a${d.path}`, `+++ b${d.path}`, ...d.lines.map((l) => (l.t === '@' ? l.text : l.t + l.text))].join('\n');

// ---- plans --------------------------------------------------------------------------------

/**
 * isPlanApproval(call): the call asks for a plan's approval — a mode switch
 * (Claude's ExitPlanMode "Ready to code?"), a plan review (Codex's
 * "Implement this plan?"), or any call whose arguments carry a plan. `call`
 * is a record, or a permission request's tool.
 */
export function isPlanApproval(call) {
  if (!call) return false;
  return call.kind === 'switch_mode' || !!call.planReview ||
    !!(call.rawInput && typeof call.rawInput === 'object' && typeof call.rawInput.plan === 'string');
}

/** planText(call): the plan's markdown — the call's text (claude sends it as content: a record's `text`, a request tool's `content`), else its `plan` argument (codex). */
export function planText(call) {
  if (!call) return '';
  const said = typeof call.text === 'string' && call.text.trim() ? call.text : firstText(call.content);
  return said || (call.rawInput && typeof call.rawInput.plan === 'string' ? call.rawInput.plan : '');
}

/** planPath(call): the file the plan was written to, when the agent says ('' otherwise). */
export const planPath = (call) => (call && call.rawInput && typeof call.rawInput.planFilePath === 'string' ? call.rawInput.planFilePath : '');
