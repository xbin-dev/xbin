// model/classes.js — agent classes (D116) as both views show them: the
// composer's class picker (the classes you may start a conversation in; your
// last pick is your default), the open conversation's badge (with the
// warning a mixed class carries), and the managers' editor — a class as a
// form, its checks, and what a save sends (PUT /classes replaces the whole
// set; API.md "Agent classes"). Pure functions of GET /classes and a run's
// view: no lit, no DOM.

// The toolsets a class may hold, in the editor's order.
export const TOOLSETS = [
  { id: 'files', label: 'Files', hint: 'session files and render_html' },
  { id: 'repl', label: 'JavaScript', hint: 'the JavaScript sandbox (js_eval, js_run)' },
  { id: 'web', label: 'Web', hint: 'web_search, web_fetch — reaches outside the workspace' },
  { id: 'internal', label: 'Internal systems', hint: 'xbin_call and the MCP servers — the workspace\'s data' },
  { id: 'sandbox', label: 'Coding sandbox', hint: 'bash, files and search in a sandbox' },
  { id: 'harness', label: 'Coding agents', hint: 'Claude Code, Codex, Gemini CLI, opencode in a coding sandbox — needs Coding sandbox and an egress other than none' },
  { id: 'subagents', label: 'Subagents', hint: 'the subagent_* tools' },
  { id: 'schedule', label: 'Schedules', hint: 'schedule, unschedule' },
  { id: 'threads', label: 'Threads', hint: 'read its automations and threads' },
  { id: 'skills', label: 'Skills', hint: 'the skill library' },
];

// What a bound sandbox may reach (docs/sandbox-manager.md).
export const EGRESS = [
  { id: 'none', label: 'none', hint: 'no network at all' },
  { id: 'internet', label: 'internet', hint: 'the public internet only' },
  { id: 'open', label: 'open', hint: 'whatever the manager\'s network gives' },
];

export const MIXED = 'can move internal data out';
export const MIXED_WHY = 'This class holds internal reach together with egress: content read in one of its conversations can steer the agent into sending internal data outside the workspace.';

// A backend with no GET /classes: the two lanes it had, as classes — no
// emoji of ours: the views draw their glyph (lock, globe) from their reach.
const FALLBACK = [
  { id: 'internal', name: 'Internal', icon: '', description: 'Your workspace\'s systems and data — no web.',
    toolsets: ['files', 'repl', 'internal', 'subagents', 'schedule', 'threads', 'skills'], lane: 'private', builtin: true },
  { id: 'web', name: 'Web', icon: '', description: 'Searches and reads the web — no internal systems.',
    toolsets: ['files', 'repl', 'web', 'subagents', 'schedule', 'threads', 'skills'], lane: 'web', egress: true, builtin: true },
];
const BUILTIN = new Set(['internal', 'web', 'coding']);

// listOf: GET /classes as the views keep it — {classes, default}; the lanes
// when the backend lists none.
export function listOf(resp) {
  const list = resp && Array.isArray(resp.classes) && resp.classes.length ? resp.classes : null;
  if (!list) return { classes: FALLBACK.map((c) => ({ ...c })), default: 'internal', fallback: true };
  return { classes: list, default: list.some((c) => c.id === resp.default) ? resp.default : list[0].id };
}

export const find = (state, id) => (state && id ? state.classes.find((c) => c.id === id) || null : null);

// reach: what a class (GET's view, or a payload being saved) adds up to —
// as _backend/classes.go says it: egress is the web, or a sandbox that may
// have a network; mixed is internal reach together with egress; the lane is
// "web" when it reaches outside with no internal reach.
export function reach(c) {
  const ts = (c && c.toolsets) || [];
  const internal = ts.includes('internal');
  const egress = ts.includes('web') || (ts.includes('sandbox') && (c.sandboxEgress || []).some((e) => e !== 'none'));
  return { internal, egress, mixed: internal && egress, lane: egress && !internal ? 'web' : 'private' };
}
export const laneOf = (c) => (c ? c.lane || reach(c).lane : 'private');

// resolvePick: the class for your new chats — your pick while you may use it;
// with no pick yet the lane you picked before classes ('web' → web, else
// internal); else the tile's default for you.
export function resolvePick(state, pref, legacy) {
  if (pref && find(state, pref)) return pref;
  if (!pref && legacy) {
    const id = legacy === 'web' ? 'web' : 'internal';
    if (find(state, id)) return id;
  }
  return state.default;
}

export const label = (c) => [c.icon, c.name || c.id].filter(Boolean).join(' ');

// A class's icon — the emoji its admin chose — as the curated glyph names
// both views draw (docs/native.md Icons; the web's /vendor/bx-icons.js, D184).
// The emoji are the people's, not ours: a table of what they may type.
const ICONS = {
  // theme-ok: a data table mapping a person's emoji to glyph names (D184)
  '🔒': 'lock', '🔐': 'lock', '🌐': 'globe', '▣': 'terminal', '💻': 'terminal', '🖥': 'terminal', '⌨': 'terminal',
  // theme-ok: a data table mapping a person's emoji to glyph names (D184)
  '🔓': 'unlock', '🛡': 'shield', '☁': 'cloud', '📁': 'folder', '🗄': 'database', '✉': 'mail', '📅': 'calendar', '⚙': 'gear',
  // theme-ok: a data table mapping a person's emoji to glyph names (D184)
  '⚠': 'warning', '🔑': 'key', '⭐': 'star', '✨': 'sparkles', '📦': 'box', '🧑‍💻': 'code' };
// iconName: the curated name of a class's emoji — '' for one the table lacks
// (the web then shows the emoji as its admin typed it).
export const iconName = (icon) => ICONS[String(icon || '').replace(/\uFE0F/g, '')] || '';
// reachIcon: the glyph for what a class reaches, when its emoji says nothing.
const reachIcon = (c) => {
  const ts = (c && c.toolsets) || [];
  return ts.includes('sandbox') ? 'terminal' : ts.includes('internal') ? 'lock' : ts.includes('web') ? 'globe' : 'tag';
};
export function nativeIcon(c) {
  if (!c) return 'tag';
  return iconName(c.icon) || reachIcon(c);
}
// glyph: the glyph the web draws before a class's name — its emoji's name;
// with no emoji, a built-in's reach (the lanes had 🔒 and 🌐 before classes);
// '' for an emoji the table lacks, which the web shows as typed.
export const glyph = (c) => (!c ? '' : c.icon ? iconName(c.icon) : c.builtin ? reachIcon(c) : '');

// pickerRows: the classes you may start a conversation in, as a menu.
export function pickerRows(state, pick) {
  return ((state && state.classes) || []).map((c) => ({
    value: c.id, icon: c.icon || '', glyph: glyph(c), name: c.name || c.id, label: label(c), description: c.description || '',
    mixed: !!c.mixed, managers: c.who === 'managers', on: c.id === pick, nativeIcon: nativeIcon(c),
  }));
}

// classPicker: the composer's class — for a NEW chat, so it shows at home
// (an open conversation's class is fixed; the top bar says it).
export function classPicker(v, state, pick) {
  const cur = find(state, pick) || find(state, state && state.default);
  return {
    shown: !v && !!cur,
    value: cur ? cur.id : '',
    label: cur ? label(cur) : '',
    icon: cur ? cur.icon || '' : '',
    glyph: glyph(cur),
    name: cur ? cur.name || cur.id : '',
    mixed: !!(cur && cur.mixed),
    nativeIcon: nativeIcon(cur),
    title: cur ? `Class for your next new chat: ${cur.name || cur.id}${cur.description ? ' — ' + cur.description : ''}${cur.mixed ? ` (${MIXED})` : ''}` : '',
    rows: pickerRows(state, cur && cur.id),
  };
}

// badge: the open conversation's class (its view's `class`); a view without
// one (an older backend) says its lane.
export function badge(v) {
  const c = v && v.class;
  if (c && c.id) {
    return {
      id: c.id, label: label(c), name: c.name || c.id, icon: c.icon || '', glyph: glyph(c), mixed: !!c.mixed, nativeIcon: nativeIcon(c),
      title: `${c.name || c.id}${c.description ? ': ' + c.description : ''} — fixed for this conversation`,
      warn: c.mixed ? MIXED : '', warnTitle: c.mixed ? MIXED_WHY : '',
    };
  }
  const web = (v && v.config && v.config.toolset) === 'web';
  return { id: web ? 'web' : 'internal', label: web ? 'web' : 'internal', name: web ? 'web' : 'internal', icon: '', glyph: web ? 'globe' : 'lock',
    mixed: false, nativeIcon: web ? 'globe' : 'lock', title: 'tool mode — fixed for this conversation', warn: '', warnTitle: '' };
}

// --- the editor (managers) ---------------------------------------------------------

export const ID_RE = /^[a-z][a-z0-9-]{0,31}$/;
const bytes = (s) => new TextEncoder().encode(s).length;
export const splitNames = (s) => [...new Set(String(s || '').split(/[\s,]+/).filter(Boolean))];

// ifaceNames: the names a class lists for a bound slot's providers — as the
// backend names them (<provider>[#<instance>]).
export const ifaceNames = (iface) => ((iface && iface.endpoints) || [])
  .map((e) => (e.provider || '') + (e.instance ? '#' + e.instance : '')).filter(Boolean);

// names: what a "only these" list offers — the bound ones and those it has.
export const names = (text, bound) => [...new Set([...(bound || []), ...splitNames(text)])]
  .map((name) => ({ name, on: splitNames(text).includes(name) }));
export function toggleName(text, name) {
  const l = splitNames(text);
  return (l.includes(name) ? l.filter((n) => n !== name) : [...l, name]).join(', ');
}
export const toggle = (list, x, on) => (on ? [...new Set([...list, x])] : list.filter((y) => y !== x));

// formOf: a class as the editor's form (strings and lists); blankForm: a new one.
export function formOf(c) {
  const ts = [...(c.toolsets || [])];
  const set = (v, relevant) => (v === 'all' || !relevant ? { mode: 'all', names: '' } : { mode: 'only', names: (v || []).join(', ') });
  const mcp = set(c.mcp, ts.includes('internal'));
  const mgr = set(c.managers, ts.includes('sandbox'));
  const hs = set(c.harnesses, ts.includes('harness'));
  return {
    orig: c.id, id: c.id, name: c.name || '', icon: c.icon || '', description: c.description || '', toolsets: ts,
    mcpMode: mcp.mode, mcp: mcp.names, managersMode: mgr.mode, managers: mgr.names, harnessesMode: hs.mode, harnesses: hs.names,
    egress: (c.sandboxEgress || []).length ? [...c.sandboxEgress] : ['none'],
    model: c.model || '', system: c.system || '', who: c.who === 'managers' ? 'managers' : 'everyone', builtin: !!c.builtin,
  };
}
export const blankForm = () => ({
  orig: '', id: '', name: '', icon: '', description: '', toolsets: ['files', 'repl', 'subagents', 'skills'],
  mcpMode: 'all', mcp: '', managersMode: 'all', managers: '', harnessesMode: 'all', harnesses: '', egress: ['none'], model: '', system: '', who: 'everyone', builtin: false,
});

// classOf: the form as PUT /classes takes a class. mcp only with internal,
// managers and sandboxEgress only with a sandbox, harnesses only with the
// harness toolset (the backend's defaults otherwise); toolsets a newer
// backend knows and this list does not stay.
export function classOf(f) {
  const known = TOOLSETS.map((t) => t.id);
  const toolsets = [...known.filter((t) => f.toolsets.includes(t)), ...f.toolsets.filter((t) => !known.includes(t))];
  const c = { id: f.id.trim(), name: f.name.trim(), icon: f.icon.trim(), description: f.description.trim(), toolsets,
    model: f.model || '', system: f.system || '', who: f.who === 'managers' ? 'managers' : 'everyone' };
  if (toolsets.includes('internal')) c.mcp = f.mcpMode === 'all' ? 'all' : splitNames(f.mcp);
  if (toolsets.includes('sandbox')) {
    c.managers = f.managersMode === 'all' ? 'all' : splitNames(f.managers);
    c.sandboxEgress = EGRESS.map((e) => e.id).filter((e) => f.egress.includes(e));
    if (!c.sandboxEgress.length) c.sandboxEgress = ['none'];
  }
  if (toolsets.includes('harness')) c.harnesses = f.harnessesMode === 'only' ? splitNames(f.harnesses) : 'all';
  return c;
}

// harnessNames: the "only these" checklist of coding agents — the catalog's
// (GET /harnesses, model/harness.js catalogOf), then any the class lists
// that it doesn't have — {id, name, on}.
export function harnessNames(text, catalog) {
  const on = splitNames(text);
  const known = ((catalog && catalog.harnesses) || []).map((h) => ({ id: h.id, name: h.name || h.id }));
  return [...known, ...on.filter((id) => !known.some((k) => k.id === id)).map((id) => ({ id, name: id }))].map((h) => ({ ...h, on: on.includes(h.id) }));
}

// HARNESS_NEEDS: what the backend says of a class with the harness toolset
// but no sandbox, or no egress but none (_backend/classes.go normalize);
// harnessWhy says it of a form before it is saved ('' = it holds).
export const HARNESS_NEEDS = 'the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider';
export function harnessWhy(f) {
  if (!f.toolsets.includes('harness')) return '';
  return f.toolsets.includes('sandbox') && f.egress.some((e) => e !== 'none') ? '' : HARNESS_NEEDS;
}

// strip: a listed class (GET's view) back as a class to save.
function strip(c) {
  const f = formOf(c);
  f.toolsets = [...(c.toolsets || [])]; // as it is, in its order
  return classOf(f);
}

// check: what is wrong with the form ('' = nothing). state: GET /classes.
export function check(f, state) {
  const id = f.id.trim();
  if (!ID_RE.test(id)) return 'The id is a–z, 0–9 and -, starting with a letter, up to 32 characters.';
  if (!f.orig && find(state, id)) return `There is a class “${id}” already.`;
  if (f.name.trim().length > 60) return 'The name is up to 60 characters.';
  if (bytes(f.icon.trim()) > 32) return 'The icon is a short symbol — an emoji, say.';
  if (f.description.trim().length > 400) return 'The description is up to 400 characters.';
  if ((f.system || '').length > 16000) return 'The system addendum is up to 16000 characters.';
  const bad = [...splitNames(f.mcp), ...splitNames(f.managers)].find((n) => n.length > 200);
  if (bad) return `“${bad.slice(0, 40)}…” isn't a server or tile name.`;
  return '';
}

// The saved set: what GET says is stored (a backend that does not say: all).
const storedOf = (state) => state.classes.filter((c) => c.stored !== false);
const mixedIds = (list) => list.filter((c) => reach(c).mixed).map((c) => c.id);

// savePlan: saving the form — the body for PUT /classes (the stored classes
// in the list's order, this one replaced or added) and whether THIS class is
// mixed: then the person confirms it and the view sets body.confirmMixed.
// Others that are mixed were confirmed when they were saved (the backend
// asks on every save), so they carry it already.
export function savePlan(state, f) {
  const cls = classOf(f);
  const list = state.classes.filter((c) => c.id === f.orig || c.stored !== false).map((c) => (c.id === f.orig ? cls : strip(c)));
  if (!f.orig) list.push(cls);
  const others = mixedIds(list.filter((c) => c !== cls));
  return { cls, mixed: reach(cls).mixed, others,
    body: { classes: list, default: state.default, ...(others.length ? { confirmMixed: true } : {}) } };
}

// removePlan: deleting a class — a built-in comes back as its default (so
// this is "Reset"); the conversations of a deleted one go on in the built-in
// of their lane. A deleted default hands over to the backend's.
export function removePlan(state, id) {
  const list = storedOf(state).filter((c) => c.id !== id).map(strip);
  const def = state.default === id && !BUILTIN.has(id) ? '' : state.default;
  return { body: { classes: list, default: def, ...(mixedIds(list).length ? { confirmMixed: true } : {}) } };
}

// defaultPlan: making id the class a new conversation gets when none is named.
export function defaultPlan(state, id) {
  const list = storedOf(state).map(strip);
  return { body: { classes: list, default: id, ...(mixedIds(list).length ? { confirmMixed: true } : {}) } };
}

// editorRows: the Classes list for managers.
export function editorRows(state) {
  return ((state && state.classes) || []).map((c) => {
    const lane = laneOf(c) === 'web' ? 'Web' : 'Internal';
    return {
      id: c.id, label: label(c), name: c.name || c.id, icon: c.icon || '', glyph: glyph(c), description: c.description || '', mixed: !!c.mixed,
      nativeIcon: nativeIcon(c), toolsets: (c.toolsets || []).join(' · ') || 'the core tools only',
      tags: [c.id === state.default && 'default', c.builtin && 'built-in', c.who === 'managers' && 'managers only', c.mixed && MIXED].filter(Boolean),
      del: c.builtin
        ? (c.stored !== false ? { label: 'Reset to default', confirm: `Reset the ${c.name || c.id} class to its default?` } : null)
        : { label: 'Delete', confirm: `Delete the ${c.name || c.id} class? Its conversations go on as ${lane}.` },
    };
  });
}

// confirmWords: what the person confirms before a mixed class is saved.
export const confirmWords = (f) => `Save “${f.name.trim() || f.id.trim()}”? It ${MIXED}: ${MIXED_WHY}`;

// --- automations: schedules, watchers, triggers, channels ---------------------------

// laneClass: the built-in a legacy toolset names (as _backend/classes.go).
export const laneClass = (toolset) => (toolset === 'web' ? 'web' : 'internal');

// laneFor: the lane an automation's class runs in — sent beside `class` as
// the legacy toolset. A class the list does not have keeps the toolset it had.
export function laneFor(state, id, toolset = '') {
  const c = find(state, id);
  return c ? laneOf(c) : toolset === 'web' ? 'web' : 'private';
}

// ofAutomation: an automation's class as its card and detail say it, from
// its config (GET /automations: `class`, else the built-in its legacy
// `toolset` names). One you may not use, or one since deleted, is said by
// its id, in the lane its toolset keeps.
export function ofAutomation(state, cfg) {
  const id = (cfg && cfg.class) || laneClass(cfg && cfg.toolset);
  const c = find(state, id);
  const lane = c ? laneOf(c) : (cfg && cfg.toolset) === 'web' ? 'web' : 'private';
  return {
    id, known: !!c, lane, mixed: !!(c && c.mixed), label: c ? label(c) : id, name: c ? c.name || c.id : id,
    icon: c ? c.icon || '' : '', glyph: c ? glyph(c) : '', nativeIcon: c ? nativeIcon(c) : lane === 'web' ? 'globe' : 'lock',
    warn: c && c.mixed ? MIXED : '',
  };
}

// choices: an automation form's class select — the classes you may use
// (lane: only that lane's), each with its lane and warning. The one it has
// stays listed (gone) even when you may not pick it now.
export function choices(state, cur, lane = '') {
  const rows = ((state && state.classes) || []).filter((c) => !lane || laneOf(c) === lane).map((c) => ({
    value: c.id, label: label(c) + (c.mixed ? ` — ${MIXED}` : ''), name: c.name || c.id, description: c.description || '',
    lane: laneOf(c), mixed: !!c.mixed, nativeIcon: nativeIcon(c), on: c.id === cur, gone: false,
  }));
  if (cur && !rows.some((r) => r.on)) {
    rows.push({ value: cur, label: `${cur} (not one you may pick)`, name: cur, description: '', lane: '', mixed: false,
      nativeIcon: 'tag', on: true, gone: true });
  }
  return rows;
}

// channelClasses: a channel's two class selects (D116, its rules) — trusted
// people's (privateClass, when the private lane is open: any class; default
// internal) and everyone else's (webClass: a class that reaches outside and
// has no internal reach, as a reply is an egress; default web). An unset one
// shows its default and stays unset until picked.
export function channelClasses(state, d) {
  const priv = d.privateClass || 'internal';
  const web = d.webClass || 'web';
  return {
    private: { value: priv, rows: choices(state, priv) },
    web: { value: web, rows: choices(state, web, 'web') },
  };
}
