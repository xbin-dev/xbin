// model/tool-heads.js — what a tool call is, in a few words, for the chat.
// (tool-heads.js at the tile's root re-exports it.)
//
// Pure (no imports, no DOM): node-tested in hack/agent-template-chat.test.mjs.
// A call's headline is the one-line `summary` the model wrote for it (every
// tool schema asks for one), else a reading of its arguments, else its name.

// The families a tool belongs to — the card's icon and colour.
const FAMILY = {
  xbin_call: 'net', web_search: 'web', web_fetch: 'web',
  file_write: 'file', file_read: 'file', file_edit: 'file', file_list: 'file', file_view: 'file', render_html: 'file',
  js_eval: 'code', js_run: 'code', js_reset: 'code',
  memory_set: 'mem', memory_get: 'mem', recall: 'mem', note: 'note',
  skills_list: 'skill', skill_view: 'skill', skill_manage: 'skill',
  schedule: 'time', unschedule: 'time', yield: 'time', schedules_list: 'time', schedule_inspect: 'time',
  threads_list: 'thread', thread_inspect: 'thread',
  subagent_spawn: 'agent', spawn_subagent: 'agent', workflow_spawn: 'agent', subagent_wait: 'agent',
  subagent_status: 'agent', workflow_status: 'agent', subagent_result: 'agent', workflow_result: 'agent',
  subagent_message: 'agent', subagent_cancel: 'agent', workflow_cancel: 'agent',
  finish: 'done', ask_user: 'ask', state_changed: 'note',
  // the coding sandbox (D115): commands, its files, moving files in and out
  bash: 'box', bash_output: 'box', bash_kill: 'box', jobs: 'box', read: 'box', write: 'box', edit: 'box', ls: 'box', glob: 'box', grep: 'box',
  sandbox_upload: 'box', sandbox_download: 'box', sandbox_copy: 'box', sandbox_info: 'box', sandbox_create: 'box',
};

export const ICON = {
  net: '⇄', web: '🌐', file: '📄', code: '{ }', mem: '🧠', note: '✎', skill: '✦', time: '⏱',
  agent: '⑂', done: '✓', ask: '?', mcp: '⚙', thread: '☰', box: '▣', other: '•',
};

export function family(name) {
  if (String(name || '').startsWith('mcp:')) return 'mcp';
  return FAMILY[name] || 'other';
}

const SPAWN = new Set(['subagent_spawn', 'spawn_subagent', 'workflow_spawn']);
export const isSpawn = (name) => SPAWN.has(name);

// parseArgs reads a call's JSON arguments; a stream in flight hands us a
// PREFIX of the JSON, so a failed parse falls back to picking out the string
// fields that are already complete.
export function parseArgs(raw) {
  if (raw && typeof raw === 'object') return raw;
  const s = String(raw || '');
  try { const v = JSON.parse(s); return v && typeof v === 'object' ? v : {}; } catch { /* partial */ }
  const out = {};
  for (const m of s.matchAll(/"([A-Za-z_][A-Za-z0-9_]*)"\s*:\s*"((?:[^"\\]|\\.)*)"/g)) {
    try { out[m[1]] = JSON.parse(`"${m[2]}"`); } catch { out[m[1]] = m[2]; }
  }
  return out;
}

const one = (s, n = 90) => {
  const t = String(s ?? '').replace(/\s+/g, ' ').trim();
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};
const ids = (v) => (Array.isArray(v) ? v : v != null ? [v] : []).map((i) => '#' + i).join(', ');

// reading describes a call from its arguments when it carries no summary.
function reading(name, a) {
  switch (name) {
    case 'xbin_call': return `${String(a.method || 'GET').toUpperCase()} ${a.path || ''}`;
    case 'web_search': return `Search the web: ${one(a.query, 70)}`;
    case 'web_fetch': return `Fetch ${one(a.url, 80)}`;
    case 'file_write': return `Write ${a.path || 'a file'}`;
    case 'file_read': return `Read ${a.path || 'a file'}`;
    case 'file_edit': return `Edit ${a.path || 'a file'}`;
    case 'file_list': return 'List session files';
    case 'file_view': return `Look at ${a.path || 'an image'}`;
    case 'render_html': return `Render ${a.path || 'a page'}`;
    case 'js_eval': {
      const n = String(a.code || '').split('\n').length;
      return n > 1 ? `Run JavaScript (${n} lines)` : `Run ${one(a.code, 60)}`;
    }
    case 'js_run': return `Run ${a.path || 'a script'}`;
    case 'js_reset': return 'Reset the JavaScript sandbox';
    case 'memory_set': return `Remember ${a.key || ''}`.trim();
    case 'memory_get': return `Recall memory ${a.key || ''}`.trim();
    case 'recall': return `Search history: ${one(a.query, 60)}`;
    case 'note': return `Note: ${one(a.text, 80)}`;
    case 'state_changed': return `Changed: ${one(a.summary, 80)}`;
    case 'skills_list': return 'List skills';
    case 'skill_view': return `Load skill ${a.name || ''}`.trim();
    case 'skill_manage': return `${a.action === 'remove' ? 'Remove' : 'Save'} skill ${a.name || ''}`.trim();
    case 'schedule': return `Schedule: ${one(a.goal, 60)} (${a.cron || '?'})`;
    case 'unschedule': return `Remove schedule #${a.id ?? '?'}`;
    case 'schedules_list': return a.scope === 'all' ? 'List all your schedules' : 'List this conversation\'s schedules';
    case 'schedule_inspect': return `Look at schedule #${a.id ?? '?'}`;
    case 'threads_list': return (a.scope === 'all' ? 'List your conversations' : 'List this conversation\'s threads') + (a.q ? `: ${one(a.q, 50)}` : '');
    case 'thread_inspect': return `Read thread #${a.id ?? '?'}`;
    case 'yield': return `Sleep ${a.seconds ?? '?'}s`;
    case 'finish': return 'Finish';
    case 'ask_user': return `Ask: ${one(a.question, 80)}`;
    case 'subagent_spawn': case 'spawn_subagent': case 'workflow_spawn':
      return one(a.label || a.task, 80) || 'Start a subagent';
    case 'subagent_wait': return `Wait for ${ids(a.ids)}${a.mode === 'any' ? ' (first)' : ''}`;
    case 'subagent_status': case 'workflow_status': return a.ids ? `Check on ${ids(a.ids)}` : 'Check on subagents';
    case 'subagent_result': case 'workflow_result': return `Read #${a.id ?? '?'}'s answer`;
    case 'subagent_message': return `Message #${a.id ?? '?'}: ${one(a.text, 60)}`;
    case 'subagent_cancel': case 'workflow_cancel': return `Stop ${ids(a.ids)}`;
  }
  if (FAMILY[name] === 'box') return boxReading(name, a);
  if (String(name).startsWith('mcp:')) {
    const [, srv, tool] = String(name).split(':');
    return `${tool || name} · ${srv || 'mcp'}`;
  }
  return name || 'tool';
}

// --- the coding sandbox (D115) ------------------------------------------------------

const spot = (x) => (x && typeof x === 'object' ? `${x.sandbox ? x.sandbox + ':' : ''}${x.path || '?'}` : '?');

// boxReading: a sandbox call from its arguments — the command, the path, the
// pattern, what moves where.
function boxReading(name, a) {
  switch (name) {
    case 'bash': return `$ ${one(a.command, 90)}${a.background ? ' &' : ''}`;
    case 'bash_output': return `Output of job ${a.job ?? '?'}${Number(a.wait_s) > 0 ? ` (waits ${a.wait_s}s)` : ''}`;
    case 'bash_kill': return `Stop job ${a.job ?? '?'}${a.signal ? ` (${a.signal})` : ''}`;
    case 'jobs': return 'List the jobs';
    case 'read': return `Read ${a.path || 'a file'}${a.offset ? ` from line ${a.offset}` : ''}`;
    case 'write': return `Write ${a.path || 'a file'}`;
    case 'edit': return `Edit ${a.path || 'a file'}: ${one(a.old_string, 28) || '…'} → ${one(a.new_string, 28) || '∅'}${a.replace_all ? ' (all)' : ''}`;
    case 'ls': return `List ${a.path || 'the working directory'}`;
    case 'glob': return `Find ${a.pattern || '?'}${a.path ? ` in ${a.path}` : ''}`;
    case 'grep': return `Search /${one(a.pattern, 50)}/${a.glob ? ` in ${a.glob}` : ''}${a.path ? ` under ${a.path}` : ''}`;
    case 'sandbox_upload': return `Upload ${a.file || 'a file'} → ${a.path || 'the working directory'}`;
    case 'sandbox_download': return `Download ${a.path || 'a file'}${a.name ? ` as ${a.name}` : ''}`;
    case 'sandbox_copy': return `Copy ${spot(a.from)} → ${spot(a.to)}`;
    case 'sandbox_info': return 'Look at the sandboxes';
    case 'sandbox_create': return `Create sandbox ${a.name || ''}`.trim();
  }
  return name;
}

// subline: the sandbox call's own words (the command, old → new, the
// pattern) under a headline that is the model's summary; '' otherwise — the
// headline already says them.
export function subline(name, rawArgs) {
  if (FAMILY[name] !== 'box') return '';
  const a = parseArgs(rawArgs);
  return typeof a.summary === 'string' && a.summary.trim() ? boxReading(name, a) : '';
}

// outcome: what a finished sandbox call came to, read from its result —
// bash's footer ([exit 1 · 14s · job 3], still running · job 3), a command
// that went on as a job across a restart, how many matches, entries or
// files, a size. {text, tone: ok | bad | run | ''} or null (none to say, or
// not a sandbox call).
export function outcome(name, content) {
  if (FAMILY[name] !== 'box') return null;
  const c = String(content ?? '');
  const st = resultState(c);
  if (st !== 'done') return null;
  const lines = c.replace(/\n+$/, '').split('\n');
  const first = lines[0] || '';
  const last = lines[lines.length - 1] || '';
  const paren = (s) => { const m = /\(([^()]+)\)\s*$/.exec(s); return m ? { text: m[1], tone: '' } : null; };
  switch (name) {
    case 'bash': case 'bash_output': {
      if (/^not run: /.test(first)) return { text: 'not run: kills by name', tone: 'bad' };
      const moved = MOVED.exec(c);
      if (moved) return { text: `went on as job ${moved[1]}`, tone: 'run' };
      const started = /^started job (\d+)/.exec(first);
      if (started) return { text: `job ${started[1]} started`, tone: 'run' };
      const m = /^\[(.*)\]$/.exec(last);
      if (!m) return null;
      const f = m[1].split(' — ')[0].replace(/ · read to byte \d+$/, '').replace(/^still running after /, 'still running · ');
      const tone = /^exit 0\b/.test(f) ? 'ok' : /^(still )?running\b/.test(f) ? 'run' : 'bad';
      return { text: f, tone };
    }
    case 'bash_kill': {
      // D134: the job's last output, then [job 3 stopped · killed by TERM]
      const m = /^\[(job \d+ stopped[^\]]*)\]$/.exec(last);
      if (m) return { text: m[1], tone: '' };
      return { text: one(first, 48), tone: /still running/.test(first) ? 'run' : '' };
    }
    case 'jobs': {
      const n = lines.filter((l) => /^job \d+ · running/.test(l)).length;
      return /^no jobs/.test(first) ? { text: 'none', tone: '' } : { text: `${n} running`, tone: n ? 'run' : '' };
    }
    case 'grep': {
      if (/^no matches /.test(first)) return { text: 'no matches', tone: '' };
      const n = lines.filter((l) => /^[^\s:][^:]*:\d+: /.test(l)).length;
      const more = /… \[(\d+)(\+)? (more )?matching lines/.exec(c);
      const total = more ? (more[3] ? n + +more[1] : +more[1]) : n;
      return { text: `${total}${more && more[2] ? '+' : ''} match${total === 1 && !(more && more[2]) ? '' : 'es'}`, tone: '' };
    }
    case 'glob': {
      if (/^no files match /.test(first)) return { text: 'no files', tone: '' };
      const more = /… and (\d+) more/.exec(c);
      const n = lines.filter((l) => l && !l.startsWith('… ')).length + (more ? +more[1] : 0);
      return { text: `${n} file${n === 1 ? '' : 's'}`, tone: '' };
    }
    case 'ls': {
      if (/: \(empty\)$/.test(first)) return { text: 'empty', tone: '' };
      const n = lines.slice(1).filter((l) => l && !l.startsWith('… ')).length;
      return { text: `${n}${/… \[more entries/.test(c) ? '+' : ''} entr${n === 1 ? 'y' : 'ies'}`, tone: '' };
    }
    case 'read': {
      const n = lines.filter((l) => /^\s*\d+\t/.test(l)).length;
      return n ? { text: `${n} line${n === 1 ? '' : 's'}`, tone: '' } : null;
    }
    case 'sandbox_info': return null;
  }
  return paren(first);
}

// headline is a call's one-line description.
export function headline(name, rawArgs) {
  const a = parseArgs(rawArgs);
  const s = typeof a.summary === 'string' && name !== 'state_changed' ? a.summary.trim() : '';
  return s ? one(s, 100) : reading(name, a);
}

// argsShown is what the expanded card lists: the arguments minus the
// summary (already the headline).
export function argsShown(rawArgs) {
  const a = { ...parseArgs(rawArgs) };
  delete a.summary;
  return a;
}

// MOVED: a bash command a backend restart cut off, which went on in the
// sandbox as a job (_backend/sandbox_jobs.go lostResultText) — answered, like
// one that outlived its timeout, not stopped.
const MOVED = /^\(no result: the backend restarted while this command ran\. It went on in the sandbox as job (\d+)/;

// resultState reads a tool result's content: the engine's placeholders
// and error prefix mean the call is still going, parked, or failed.
export function resultState(content) {
  const c = String(content ?? '');
  if (c === '' || c === '(running…)') return 'running';
  if (c === '(awaiting your approval)') return 'approval';
  if (c.startsWith('(waiting for ')) return 'waiting';
  if (c.startsWith('error:')) return 'error';
  if (MOVED.test(c)) return 'done';
  if (/^\((interrupted|cancelled|not executed|denied|no result:)/.test(c)) return 'stopped';
  // an interrupted bash keeps its output, then [interrupted by the owner · job 3 …] (D134)
  if (/\n\[(interrupted by the owner|cancelled|stopped) · job \d+ [^\n]*\]$/.test(c)) return 'stopped';
  return 'done';
}
