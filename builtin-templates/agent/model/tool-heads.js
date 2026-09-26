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
  schedule: 'time', unschedule: 'time', yield: 'time',
  subagent_spawn: 'agent', spawn_subagent: 'agent', workflow_spawn: 'agent', subagent_wait: 'agent',
  subagent_status: 'agent', workflow_status: 'agent', subagent_result: 'agent', workflow_result: 'agent',
  subagent_message: 'agent', subagent_cancel: 'agent', workflow_cancel: 'agent',
  finish: 'done', ask_user: 'ask', state_changed: 'note',
};

export const ICON = {
  net: '⇄', web: '🌐', file: '📄', code: '{ }', mem: '🧠', note: '✎', skill: '✦', time: '⏱',
  agent: '⑂', done: '✓', ask: '?', mcp: '⚙', other: '•',
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
  if (String(name).startsWith('mcp:')) {
    const [, srv, tool] = String(name).split(':');
    return `${tool || name} · ${srv || 'mcp'}`;
  }
  return name || 'tool';
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

// resultState reads a tool result's content: the engine's placeholders
// and error prefix mean the call is still going, parked, or failed.
export function resultState(content) {
  const c = String(content ?? '');
  if (c === '' || c === '(running…)') return 'running';
  if (c === '(awaiting your approval)') return 'approval';
  if (c.startsWith('(waiting for ')) return 'waiting';
  if (c.startsWith('error:')) return 'error';
  if (/^\((interrupted|cancelled|not executed|denied|no result:)/.test(c)) return 'stopped';
  return 'done';
}
