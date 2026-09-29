// model/harness.js — a coding harness conversation (Claude Code, Codex,
// Gemini CLI, opencode — D-harness §4) in words both views draw:
// the summary a harness run carries (`run.harness`, §4.3.2 — also on run
// events, the `harness` stream event, conversation rows, tree nodes and a
// link's child), its park (`pendingState.harness`, §4.3.4), and the catalog
// of what the caller could start (`GET /harnesses`, §4.3.10). The tool calls
// are harness-heads.js's; the state and calls are harness-store.js's.
// Pure (no DOM): node-tested in hack/agent-template-harness.test.mjs.
import { firewallEgress } from './sandboxes.js';

// The harnesses the sdk's catalog knows: a name before the catalog is read,
// a text monogram (never a vendor's logo), the native icon.
export const HARNESSES = {
  claude: { name: 'Claude Code', mono: 'CC', icon: 'sparkles' },
  codex: { name: 'Codex', mono: 'CX', icon: 'terminal' },
  gemini: { name: 'Gemini CLI', mono: 'GM', icon: 'star' },
  opencode: { name: 'opencode', mono: 'OC', icon: 'code' },
  fake: { name: 'Fake agent (tests)', mono: 'FA', icon: 'wrench' },
};

// isHarness: a run (a summary, a row, a node, a view's run) that a coding
// harness drives — engine "harness", fixed for its life.
export const isHarness = (r) => !!(r && r.engine === 'harness');

// harnessOf: the summary of a view ({run}), a run, a row, a tree node or a
// link ({child}) — null for the built-in agent's.
export function harnessOf(x) {
  const r = x && (x.run || x.child || x);
  return isHarness(r) ? r.harness || { provider: '', state: 'stopped' } : null;
}

export const nameOf = (h) => (h && (h.name || HARNESSES[h.provider]?.name || h.provider || h.id)) || 'the coding agent';
export const monogram = (provider) => HARNESSES[provider]?.mono || String(provider || '?').slice(0, 2).toUpperCase();

// The API's states (§4.3.2) as a word and a tone (ok · run · warn · bad · ''):
// live says an adapter is up or coming up.
export const STATES = {
  stopped: { word: 'stopped', tone: '', live: false },
  starting: { word: 'starting', tone: 'run', live: true },
  ready: { word: 'ready', tone: 'ok', live: true },
  working: { word: 'working', tone: 'run', live: true },
  login: { word: 'needs sign-in', tone: 'warn', live: false },
  lost: { word: 'cut off', tone: 'warn', live: false },
  failed: { word: 'failed', tone: 'bad', live: false },
};

// stateWords: {state, word, tone, live, title} — title says why for lost and failed.
export function stateWords(h) {
  const state = h && STATES[h.state] ? h.state : 'stopped';
  return { state, ...STATES[state], title: (state === 'lost' || state === 'failed') && h.error ? h.error : '' };
}

// pendingOf: what a harness run is parked on — {kind: approval | question |
// login, park, title, harness} from the view's pendingState (the full card
// data, §4.3.4), else the summary's compact `pending` (rows and tree nodes
// carry only that; harness is then null). null: not parked.
export function pendingOf(r) {
  if (!r) return null;
  const ps = r.status === 'waiting_input' && r.pendingState && r.pendingState.harness ? r.pendingState : null;
  const p = r.harness && r.harness.pending;
  if (!ps && !p) return null;
  const hs = ps ? ps.harness : null;
  const title = (p && p.title) || (hs && (hs.tool?.title || hs.message)) || (ps && ps.kind === 'login' ? `Sign in to ${nameOf(r.harness)}` : '');
  return { kind: (ps && ps.kind) || (p && p.kind) || '', park: (ps && ps.park) || (p && p.park) || '', title, harness: hs };
}

// pendingWords: a park in words for a line (a child card, the board, Needs).
export function pendingWords(p, name = 'the coding agent') {
  if (!p) return '';
  if (p.kind === 'approval') return p.harness && p.harness.planApproval ? 'waiting for you to approve its plan' : `waiting for your approval: ${p.title}`;
  if (p.kind === 'question') return `asks: ${p.title}`;
  if (p.kind === 'login') return `needs you to sign in to ${name}`;
  return '';
}

// activityLine: the line under a harness conversation's chat (fold.js
// activity) — null leaves it to the built-in words.
export function activityLine(r) {
  const h = harnessOf(r);
  if (!h) return null;
  const name = nameOf(h);
  switch (h.state) {
    case 'starting': return `Starting ${name}…`;
    case 'login': return `${name} needs you to sign in`;
    case 'lost': return `${name} stopped (${h.error || 'cut off'}) — Retry resumes its session`;
    case 'failed': return `${name} couldn't start${h.error ? ': ' + h.error : ''}`;
  }
  const p = pendingOf(r);
  if (p) return p.kind === 'approval' ? 'Waiting for your approval' : p.kind === 'question' ? 'Waiting for your answer' : `${name} needs you to sign in`;
  if (h.state !== 'working') return null;
  const a = h.activity || {};
  if (a.kind === 'thinking') return 'Thinking…';
  if (a.kind === 'writing') return 'Writing…';
  if (a.kind === 'tool' && a.title) return `Running: ${a.title}`;
  return `Waiting for ${name}…`;
}

const fmtN = (n) => String(Math.round(Number(n) || 0)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');

// countsWords: "12 tool calls · 3 files +40 −7" ('' before any call).
export function countsWords(c) {
  if (!c || !(c.tools || c.files)) return '';
  const parts = [`${c.tools || 0} tool call${c.tools === 1 ? '' : 's'}`];
  if (c.files) parts.push(`${c.files} file${c.files === 1 ? '' : 's'} +${c.add || 0} −${c.del || 0}`);
  return parts.join(' · ');
}

// usageWords: the context in use as a badge — {text: "ctx 26%", title, pct} — or null.
export function usageWords(u) {
  if (!u || !(Number(u.size) > 0)) return null;
  const pct = Math.min(100, Math.round((Number(u.used) || 0) * 100 / Number(u.size)));
  const cost = u.cost && u.cost.amount != null ? ` · ${u.cost.currency === 'USD' || !u.cost.currency ? '$' : u.cost.currency + ' '}${Number(u.cost.amount).toFixed(2)}` : '';
  return { text: `ctx ${pct}%`, pct, title: `${fmtN(u.used)} of ${fmtN(u.size)} tokens of context${cost}` };
}

// planOf: the last ACP plan — {entries, done, total, now (the entry in
// progress), text: "3/7 · now: …"} — or null.
export function planOf(h) {
  const es = h && h.plan && Array.isArray(h.plan.entries) ? h.plan.entries : [];
  if (!es.length) return null;
  const done = es.filter((e) => e.status === 'completed').length;
  const cur = es.find((e) => e.status === 'in_progress');
  return { entries: es, done, total: es.length, now: cur ? cur.content : '', text: `${done}/${es.length}${cur ? ` · now: ${cur.content}` : ''}` };
}

// modeOf: the session's mode — {current, name, explicit, available} (explicit
// from the catalog entry when the summary's list lacks it).
export function modeOf(h, entry = null) {
  const m = (h && h.mode) || {};
  const avail = (m.available && m.available.length ? m.available : entry?.modes || []).map((x) => ({
    ...x, explicit: !!(x.explicit || entry?.modes?.find((y) => y.id === x.id)?.explicit),
  }));
  const cur = avail.find((x) => x.id === m.current);
  return { current: m.current || '', name: cur ? cur.name : m.current || '', explicit: !!(cur && cur.explicit), available: avail };
}

// optionOf: a config option of the session (by id or category), or null.
export const optionOf = (h, key) => ((h && h.options) || []).find((o) => o.id === key || o.category === key) || null;

// --- the catalog (GET /harnesses, §4.3.10) -------------------------------------------

// catalogOf: GET /harnesses' answer with every list present — {harnesses}.
export function catalogOf(resp) {
  const list = resp && Array.isArray(resp.harnesses) ? resp.harnesses : [];
  return {
    harnesses: list.map((h) => ({
      name: HARNESSES[h.id]?.name || h.id, available: false, reason: '', why: '', setting: 'approve',
      ...h, classes: h.classes || [], images: h.images || [], modes: h.modes || [], options: h.options || [], sandboxes: h.sandboxes || {},
    })),
  };
}
export const findHarness = (cat, id) => (cat && cat.harnesses ? cat.harnesses.find((h) => h.id === id) || null : null);

// Why a harness can't be started, when the catalog says only the reason.
export const REASON = {
  'no-image': "no bound sandbox manager's image has it",
  'manager-error': "a sandbox manager that might have it didn't answer",
  'no-class': 'no class you may use allows coding agents',
  'no-egress': 'it needs internet access, and no sandbox manager offers it',
};
export const whyNot = (h) => (!h ? 'unknown coding agent' : h.available ? '' : h.why || REASON[h.reason] || 'not available');

// resolveClass: the class a harness conversation starts in (§4.2.3) — the
// one asked for if it allows the harness, else the caller's first that does
// (h.classes: the caller's default first); '' when none does.
export function resolveClass(h, classId = '') {
  const cs = (h && h.classes) || [];
  return cs.includes(classId) ? classId : cs[0] || '';
}

// providerMode: the provider's mode for a setting — "approve", "auto" (the
// person's, §4.3.12) or "plan" (a spawn's); '' when it has none (no auto mode).
export function providerMode(h, setting) {
  if (!h) return '';
  return setting === 'auto' ? h.autoMode || '' : setting === 'plan' ? h.planMode || '' : h.approveMode || h.defaultMode || '';
}

// sandboxFits: may a harness conversation run in sandbox s (a GET /sandboxes
// row)? {ok, why, signedIn} — its (manager, image) must list the harness, its
// egress (the less restrictive of egress/egressNext) must not be none, and a
// probe must not have found it missing.
export function sandboxFits(h, s) {
  if (!h || !s) return { ok: false, why: 'no sandbox', signedIn: false };
  const name = nameOf(h), sname = s.name || s.id || s.ref;
  const image = (s.image && s.image.id) || s.image || '';
  const seen = h.sandboxes && h.sandboxes[s.ref];
  const out = (why) => ({ ok: !why, why, signedIn: !!(seen && seen.signedIn) });
  if (!(h.images || []).some((i) => i.provider === s.provider && i.image === image)) return out(`${sname}'s image doesn't have ${name}`);
  if (firewallEgress(s) === 'none') return out(`${name} must reach its provider — ${sname}'s egress is none`);
  if (seen && seen.installed === false) return out(`${sname} doesn't have ${name}`);
  return out('');
}

// usageBadge: the top bar's usage badge (the native toolbar's) — {text:
// "ctx 26% · $0.41", title, pct, tone: warn | bad | ''} — the context in
// use, else the tokens, and the cost so far when the adapter says; null
// when it says nothing.
export function usageBadge(u) {
  if (!u) return null;
  const w = usageWords(u);
  const c = u.cost && u.cost.amount != null ? `${u.cost.currency === 'USD' || !u.cost.currency ? '$' : u.cost.currency + ' '}${Number(u.cost.amount).toFixed(2)}` : '';
  const used = Number(u.used) || 0;
  if (!w && !used && !c) return null;
  const head = w ? w.text : used ? `${used >= 1000 ? Math.round(used / 1000) + 'k' : used} tokens` : '';
  return {
    text: [head, c].filter(Boolean).join(' · '), pct: w ? w.pct : null,
    tone: w && w.pct >= 90 ? 'bad' : w && w.pct >= 75 ? 'warn' : '',
    title: w ? w.title : [used ? `${fmtN(used)} tokens of context` : '', c ? `${c} so far` : ''].filter(Boolean).join(' · '),
  };
}

// PLAN_MARK: a plan entry's status as a glyph (○ pending, ◐ in progress, ● done).
export const PLAN_MARK = { pending: '○', in_progress: '◐', completed: '●' };

// planEntries: a plan's entries as the native `plan` takes them — {text,
// status} with status one of pending, in_progress, completed.
export const planEntries = (p) => ((p && p.entries) || []).map((e) => ({
  text: String(e.content ?? ''), status: PLAN_MARK[e.status] ? e.status : 'pending',
}));
