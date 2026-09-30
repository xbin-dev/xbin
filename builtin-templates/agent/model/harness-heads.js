// model/harness-heads.js — a coding harness's tool call (`acp:<kind>`,
// D-harness §4.3.5) in a few words, as tool-heads.js says a
// built-in call: its family (the card's icon), a reading of its arguments
// when it has no summary, the command under a summary, what it came to, and
// its state. tool-heads.js hands every `acp:*` name here.
//
// A call's arguments are rawInput's own fields plus `summary` (the adapter's
// label, else its title, else the kind); its tool row's `acp` (the lift of
// Meta.harness) says the rest: status, exit code and output, diffs,
// locations, the subagent it runs under. Pure (no imports, no DOM).

// ACP's ToolKinds and the family each is drawn as.
export const ACP_FAMILY = {
  read: 'file', edit: 'edit', delete: 'del', move: 'move', search: 'search',
  execute: 'box', think: 'think', fetch: 'web', switch_mode: 'mode', other: 'other',
};
// The families only harness calls have: the web's glyphs and the native icons
// (tool-heads.js ICON and native/ui.js FAMILY_ICON take them in).
export const ACP_ICON = { edit: '✎', search: '🔎', del: '🗑', move: '↪', think: '💭', mode: '⇄' };
export const ACP_NATIVE_ICON = { edit: 'pencil', search: 'search', del: 'trash', move: 'link', think: 'sparkles', mode: 'shield' };

export const isAcp = (name) => String(name || '').startsWith('acp:');
// acpKind: the ToolKind of a call name ('acp:edit' → 'edit'); unknown → 'other'.
export function acpKind(name) {
  const k = String(name || '').slice(4);
  return k in ACP_FAMILY ? k : 'other';
}
export const acpFamily = (name) => ACP_FAMILY[acpKind(name)];

const one = (s, n = 90) => {
  const t = String(s ?? '').replace(/\s+/g, ' ').trim();
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};
// pathOf / commandOf pick what adapters name them in rawInput.
export function pathOf(a) {
  const p = a && (a.file_path ?? a.path ?? a.filePath ?? a.notebook_path ?? a.abs_path);
  return typeof p === 'string' ? p : '';
}
export function commandOf(a) {
  const c = a && (a.command ?? a.cmd);
  return Array.isArray(c) ? c.join(' ') : typeof c === 'string' ? c : '';
}

// acpReading describes a call from its arguments (its tool row's acp when
// known) — the headline when the call carries no summary.
export function acpReading(name, a = {}, acp = null) {
  const kind = acpKind(name);
  const path = pathOf(a) || (acp && acp.locations && acp.locations[0] && acp.locations[0].path) || '';
  switch (kind) {
    case 'execute': { const c = commandOf(a); return c ? `$ ${one(c, 90)}` : acp?.title || 'Run a command'; }
    case 'read': {
      const from = Number(a.offset) || 0, n = Number(a.limit) || 0;
      return `Read ${path || 'a file'}${n ? `:${from || 1}–${(from || 1) + n - 1}` : from ? `:${from}–` : ''}`;
    }
    case 'edit': return `Edit ${path || 'a file'}`;
    case 'delete': return `Delete ${path || 'a file'}`;
    case 'move': return `Move ${a.source || a.from || path || '?'} → ${a.destination || a.to || '?'}`;
    case 'search': return `Search /${one(a.pattern ?? a.query ?? '', 50)}/${a.path ? ` in ${a.path}` : ''}`;
    case 'fetch': return one(a.url || a.query || acp?.title || 'Fetch', 90);
    case 'think': return one(a.description || acp?.title || 'Think', 90);
    case 'switch_mode': return 'Switch mode';
  }
  return one(acp?.title || acp?.tool || 'tool', 90);
}

// acpSubline: an execute's command under a headline that is the adapter's
// summary ('' when the headline already says it).
export function acpSubline(name, a = {}) {
  if (acpKind(name) !== 'execute') return '';
  const c = commandOf(a);
  const s = typeof a.summary === 'string' ? a.summary.trim() : '';
  return c && s && s !== c ? `$ ${one(c, 90)}` : '';
}

// diffTotals: the files an edit changed and the lines added and deleted
// (the backend's exact counts per file).
export function diffTotals(acp) {
  const ds = (acp && Array.isArray(acp.diffs)) ? acp.diffs : [];
  return { files: ds.length, add: ds.reduce((n, d) => n + (Number(d.add) || 0), 0), del: ds.reduce((n, d) => n + (Number(d.del) || 0), 0) };
}

// acpOutcome: what a finished harness call came to — an exit code, +a −d, a
// count — as {text, tone: ok | bad | ''}, or null (nothing to say; a
// failure is already the card's state).
export function acpOutcome(name, content, acp) {
  if (!acp || acp.status !== 'completed') return null;
  switch (acpKind(name)) {
    case 'execute': {
      if (acp.exitCode == null) return null;
      const n = Number(acp.exitCode);
      return { text: `exit ${n}`, tone: n === 0 ? 'ok' : 'bad' };
    }
    case 'edit': {
      const t = diffTotals(acp);
      if (!t.files) return null;
      return { text: `${t.files > 1 ? `${t.files} files ` : ''}+${t.add} −${t.del}`, tone: '' };
    }
    case 'delete': {
      const t = diffTotals(acp);
      return t.del ? { text: `−${t.del}`, tone: '' } : null;
    }
    case 'read': {
      const c = String(content ?? '').replace(/\n+$/, '');
      if (!c) return null;
      const n = c.split('\n').length;
      return { text: `${n} line${n === 1 ? '' : 's'}`, tone: '' };
    }
    case 'search': {
      const n = Array.isArray(acp.locations) ? acp.locations.length : 0;
      return n ? { text: `${n} match${n === 1 ? '' : 'es'}`, tone: '' } : null;
    }
  }
  return null;
}

// acpState: the card's state (model/fold.js) — the tool row's placeholder
// says running or parked, as for any call (state, from resultState); ACP's
// own status says failed and cancelled.
export function acpState(acp, state) {
  if (acp && acp.status === 'failed') return 'error';
  if (acp && acp.status === 'cancelled') return 'stopped';
  return state;
}

// ANSI: an execute's output keeps its colour codes (§4.3.5); a view that
// draws plain text strips them.
const ANSI = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;
export const stripAnsi = (s) => String(s ?? '').replace(ANSI, '');

// outputTail: an execute's output, ANSI stripped, its last n characters —
// {text, cut, dropped}: cut the characters left out here, dropped the bytes
// the backend kept out (outputTruncated: it keeps the last 64 KiB).
export function outputTail(acp, n = 20000) {
  const t = stripAnsi(acp && acp.output);
  const dropped = Number(acp && acp.outputTruncated) || 0;
  return t.length > n ? { text: t.slice(-n), cut: t.length - n, dropped } : { text: t, cut: 0, dropped };
}

// --- the cards (harness-cards.js on the web, native/harness-cards.js) --------------------

// acpChip: a call's status as its card's chip — {text, tone: warn | bad |
// run | ''} — or null once it completed (its outcome says what it came to).
// The tool row's placeholder says it is parked on your approval; ACP's
// status says pending, running, failed and cancelled.
export function acpChip(acp, state) {
  const s = acp && acp.status;
  if (state === 'approval') return { text: 'needs approval', tone: 'warn' };
  if (s === 'failed' || state === 'error') return { text: 'failed', tone: 'bad' };
  if (s === 'cancelled' || state === 'stopped') return { text: 'cancelled', tone: '' };
  if (state === 'writing') return { text: 'writing', tone: 'run' };
  if (s === 'pending') return { text: 'pending', tone: '' };
  if (s === 'in_progress' || state === 'running') return { text: 'running', tone: 'run' };
  return null;
}

// isSubagentCall: a harness-internal subagent (a Claude Task) — the call
// says so, or blocks run under it (the fold's kids).
export const isSubagentCall = (b) => !!(b && ((b.acp && b.acp.subagent) || (b.kids && b.kids.length)));

// stepsWords: "3 steps" — the calls a subagent made (its kids that are calls).
export function stepsWords(kids) {
  const n = (kids || []).filter((k) => k.k === 'tool' || k.k === 'agent').length;
  return n ? `${n} step${n === 1 ? '' : 's'}` : '';
}

// taskOf: what a subagent call was asked (Task's prompt, else its description).
export function taskOf(a) {
  const t = a && (a.prompt ?? a.task ?? a.description);
  return typeof t === 'string' ? t : '';
}

// diffFiles: an edit's diffs (§4.3.5), each {path, status, add, del, patch,
// truncated} with every field present.
export function diffFiles(acp) {
  return ((acp && Array.isArray(acp.diffs)) ? acp.diffs : []).filter((d) => d && d.path).map((d) => ({
    path: String(d.path), status: d.status || 'modified', add: Number(d.add) || 0, del: Number(d.del) || 0,
    patch: typeof d.patch === 'string' ? d.patch : '', truncated: !!d.truncated,
  }));
}

// joinPatches: an edit's patches as one unified diff (the native `diff`
// takes one), whole files only while it stays under max characters —
// {patch, files, left}: left the files whose patch was left out.
export function joinPatches(acp, max = Infinity) {
  const files = diffFiles(acp);
  let patch = '', left = 0;
  for (const d of files) {
    if (!d.patch) continue;
    const p = d.patch.endsWith('\n') ? d.patch : d.patch + '\n';
    if (patch && patch.length + p.length > max) { left++; continue; }
    if (!patch && p.length > max) { left++; continue; }
    patch += p;
  }
  return { patch, files, left };
}

// patchLines: a unified diff's lines, each with its class — fh (a file
// header), h (a hunk header), d (added), a (deleted), ctx — the classes
// /vendor/bx-code.js's diffHTML gives them, so one stylesheet draws both.
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

// placesOf: the paths (and lines) a call touched — "path:line" for each of
// its locations, else its files.
export function placesOf(acp) {
  const locs = (acp && Array.isArray(acp.locations)) ? acp.locations.filter((l) => l && l.path) : [];
  if (locs.length) return locs.map((l) => (l.line ? `${l.path}:${l.line}` : l.path));
  return (acp && Array.isArray(acp.files)) ? acp.files.map(String) : [];
}

// resultText: what a finished call answered — '' while the tool row holds
// its placeholder (running, parked).
export const resultText = (b) => (b && b.result && !['running', 'approval', 'writing'].includes(b.state) ? b.result : '');
