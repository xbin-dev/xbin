// model/harness-start.js — starting a conversation with a coding agent
// (D-harness §2.2 "Conversation start", §4.2.3, §4.3.8, §4.3.12) as both
// views draw it: who answers new chats — the built-in agent or a coding
// agent of the catalog (GET /harnesses) — with the class a harness resolves
// to (a class you may use that allows it) and the sandbox it starts in (one
// whose image has it, egress not none; the one you last used with it,
// prefs/harness-sandbox), the setup card when no sandbox fits or it isn't
// signed in there, the new-chat dialog's field, a conversation row's kind
// chip and the top bar's chip. The functions of a catalog, a sandbox list
// and a row are pure; the last section keeps the app's pick of a sandbox in
// step with the harness picked (wired once by model/app.js). No lit, no
// DOM — node-tested in hack/agent-template-harness-start.test.mjs.
import { HARNESSES, findHarness, nameOf, monogram, whyNot, resolveClass, sandboxFits, harnessOf, stateWords } from './harness.js';
import { HOME } from './home.js';
import { splitRef, classAllows, STATES as SBX_STATES, ICON as SBX } from './sandboxes.js';
import * as classes from './classes.js';
import { AGENT } from './harness-store.js'; // "Who answers": the built-in agent (prefs/agent)

export { AGENT };

const sbxName = (s) => (s && (s.name || splitRef(s.ref).id)) || '';
const clsLabel = (state, id) => { const c = classes.find(state, id); return c ? classes.label(c) : id; };
// the sandboxes a new chat could start in: yours to use
const usable = (list) => ((list && list.sandboxes) || []).filter((s) => s.canUse !== false);

// --- who answers --------------------------------------------------------------------

/**
 * agentPicker: "Who answers" for new chats — the composer's picker at home
 * (web #apick, the native toolbar's) and the new-chat field (#n-agent).
 * @param cat   the catalog (model/harness.js catalogOf)
 * @param pick  'agent' or a harness id (prefs/agent)
 * @param o     {classes: GET /classes (model/classes.js listOf), classId: your
 *              class for new chats, remembered: {provider: ref}, list: GET
 *              /sandboxes (model/sandboxes.js listOf), manager: you manage the tile}
 * → {shown, value, harness, name, mono, label, title, rows, empty, header}:
 * value is who answers ('agent' while the pick isn't available); rows are the
 * built-in agent, then each coding agent (an unavailable one disabled, why);
 * empty is what to do when none is available ('' when one is); shown: there
 * is a choice to make (a coding agent is available, or picked).
 */
export function agentPicker(cat, pick, o = {}) {
  const hs = (cat && cat.harnesses) || [];
  const cur = pick && pick !== AGENT ? findHarness(cat, pick) : null;
  const h = cur && cur.available ? cur : null;
  const sandboxes = (o.list && o.list.sandboxes) || [];
  const seenOn = (x) => {
    const ref = (o.remembered || {})[x.id];
    const seen = ref && x.sandboxes && x.sandboxes[ref];
    if (!seen || seen.signedIn == null) return '';
    const s = sandboxes.find((y) => y.ref === ref);
    return `${seen.signedIn ? 'signed in' : 'not signed in'} on ${sbxName(s || { ref })}`;
  };
  const rows = [
    { value: AGENT, name: `${HOME.title} (built in)`, mono: '✦', icon: 'sparkles', on: !h, disabled: false, why: '',
      detail: 'this tile\'s agent: your classes\' tools, subagents, schedules' },
    ...hs.map((x) => {
      const cls = x.available ? resolveClass(x, o.classId || '') : '';
      return { value: x.id, name: nameOf(x), mono: monogram(x.id), icon: HARNESSES[x.id]?.icon || 'terminal', on: !!h && h.id === x.id,
        disabled: !x.available, why: whyNot(x), signedIn: seenOn(x),
        detail: x.available ? [cls ? `in ${clsLabel(o.classes, cls)}` : '', seenOn(x)].filter(Boolean).join(' · ') : whyNot(x) };
    }),
  ];
  const any = hs.some((x) => x.available);
  const name = h ? nameOf(h) : `${HOME.title} (built in)`;
  return {
    shown: any || !!cur,
    value: h ? h.id : AGENT,
    harness: h,
    name,
    mono: h ? monogram(h.id) : '✦',
    label: h ? nameOf(h) : HOME.title,
    title: h ? `${name} answers your next new chat — in ${clsLabel(o.classes, resolveClass(h, o.classId || ''))}, in a coding sandbox; fixed once the chat starts`
      : 'Who answers your next new chat — fixed once it starts',
    header: 'Who answers — fixed once a chat starts',
    section: 'Coding agents — run in a coding sandbox',
    rows,
    empty: any ? '' : o.manager
      ? 'Bind a sandbox manager whose image has Claude Code, Codex, Gemini CLI or opencode, and allow it in ⚙ Classes.'
      : 'Ask a manager of this agent to bind a sandbox manager with coding agents and allow them in a class.',
  };
}

// --- the sandbox it starts in ---------------------------------------------------------

// fitsWhy: why a sandbox can't hold a conversation with harness h ('' = it
// can) — the sandbox picker's rows (model/sandboxes.js sandboxPicker fits).
export const fitsWhy = (h) => (s) => sandboxFits(h, s).why;

// sandboxOptions: every sandbox you may use for a conversation with h —
// those it fits first (signed in, then running), each {value, name, label,
// state, disabled, why, signedIn}.
export function sandboxOptions(h, list) {
  const rank = (r) => (r.disabled ? 2 : 0) + (r.signedIn ? 0 : 1) * 0.5 + (r.state === 'running' ? 0 : 0.25);
  return usable(list).map((s) => {
    const f = sandboxFits(h, s);
    const st = SBX_STATES[s.state] || s.state || '';
    return { value: s.ref, name: sbxName(s), label: `${sbxName(s)}${st ? ' · ' + st : ''}${f.signedIn ? ' · signed in' : ''}`,
      state: s.state || '', disabled: !f.ok, why: f.why, signedIn: f.signedIn };
  }).sort((a, b) => rank(a) - rank(b));
}

// preferredSandbox: the sandbox a new chat with h starts in — the one you
// last used with it (remembered), else the one picked now, else the best
// that fits (signed in, running); null when none fits.
export function preferredSandbox(h, list, remembered, pick) {
  if (!h) return null;
  const fits = sandboxOptions(h, list).filter((r) => !r.disabled);
  const by = (ref) => (ref ? fits.find((r) => r.value === ref) : null);
  return by(remembered) || by(pick) || fits[0] || null;
}

// createPrefill: the create form's values for a sandbox h runs in — a
// manager and image that have it, internet (else another egress the class
// allows but none), a free name (claude-dev, claude-dev-2, …); null when no
// bound manager can make one. cls: the class it would start in.
export function createPrefill(h, list, cls) {
  if (!h) return null;
  const managers = ((list && list.managers) || []).filter((m) => m.ok !== false);
  for (const i of h.images || []) {
    const m = managers.find((x) => x.provider === i.provider);
    if (!m || (cls && classAllows(cls, i.provider, '', m.title))) continue;
    const offered = (i.egress || []).filter((e) => e !== 'none' && (!cls || !classAllows(cls, '', e)));
    if (!offered.length) continue;
    const taken = new Set(((list && list.sandboxes) || []).map((s) => s.name));
    let name = `${h.id}-dev`;
    for (let n = 2; taken.has(name); n++) name = `${h.id}-dev-${n}`;
    return { provider: i.provider, image: i.image, egress: offered.includes('internet') ? 'internet' : offered[0], name };
  }
  return null;
}

/**
 * setupOf: the home's setup card for a new chat with h — or null when there
 * is nothing to set up (or the sandbox list isn't read yet).
 *   {kind: 'create', title, text, create: {label, form} | null, why}
 *     no sandbox you may use fits: Create opens the create form prefilled
 *     (createPrefill; it becomes the next chat's pick); why: when none can be made
 *   {kind: 'signin', title, text, use: {label, ref} | null}
 *     the picked sandbox fits but the catalog says h isn't signed in there:
 *     the first message asks to sign in; use: another one where it is
 */
export function setupOf(h, list, pick, cls) {
  if (!h || !list || !list.loaded) return null;
  const name = nameOf(h);
  const opts = sandboxOptions(h, list);
  const fits = opts.filter((r) => !r.disabled);
  if (!fits.length) {
    const form = createPrefill(h, list, cls);
    return { kind: 'create', title: `${name} needs a coding sandbox`,
      text: `${name} needs a coding sandbox with internet access. Its sign-in is kept in that sandbox — reuse one to stay signed in.`,
      create: form ? { label: `Create ${form.name}`, form } : null,
      why: form ? '' : `no bound sandbox manager can make one for ${name} — ask a manager of this agent` };
  }
  const cur = pick && fits.find((r) => r.value === pick);
  if (!cur) return null;
  const seen = h.sandboxes && h.sandboxes[cur.value];
  if (!seen || seen.signedIn !== false) return null;
  const s = usable(list).find((x) => x.ref === cur.value);
  const shared = !!(s && (s.visibility === 'team' || (s.shares || []).length));
  const other = fits.find((r) => r.signedIn && r.value !== cur.value);
  return { kind: 'signin', title: `${name} isn't signed in on ${cur.name}`,
    text: `Your first message asks you to sign in to ${name} there. The sign-in stays in ${cur.name}${shared ? ' — everyone who may use it acts as you with ' + name : ''}.`,
    use: other ? { label: `Use ${other.name} (signed in)`, ref: other.value } : null };
}

// --- a conversation's kind ------------------------------------------------------------

// kindOf: a conversation row (GET /conversations, §4.3.8), a run or a view
// that a coding agent answers — {provider, mono, name, title} — else null.
export function kindOf(r) {
  const h = harnessOf(r);
  if (!h) return null;
  const name = nameOf(h);
  const where = h.sandbox && (h.sandbox.name || splitRef(h.sandbox.ref).id);
  return { provider: h.provider || '', mono: monogram(h.provider), name, title: `${name} answers here${where ? ` — in ${SBX} ${where}` : ''}` };
}

// topChip: the open conversation's coding agent for its top bar —
// {mono, name, state, word, tone, label, title, shared} — else null. shared says
// who can read what it does (§2.2 Privacy: its sandbox's co-users).
export function topChip(v) {
  const h = harnessOf(v);
  if (!h) return null;
  const name = nameOf(h);
  const st = stateWords(h);
  const sb = h.sandbox || {};
  const where = sb.name || (sb.ref ? splitRef(sb.ref).id : '');
  const shared = sb.shared ? `${where} is shared — the people who may use it can read what ${name} does here` : '';
  return { mono: monogram(h.provider), name, state: st.state, word: st.word, tone: st.tone, label: `${name} · ${st.word}`,
    title: [`${name} answers this conversation${where ? ` in ${SBX} ${where}${sb.cwd ? ' at ' + sb.cwd : ''}` : ''} — fixed for its life`,
      st.title, shared].filter(Boolean).join('\n'), shared };
}

// --- with the app ---------------------------------------------------------------------

/**
 * startOf(app): what the home composer and a new chat need, for where the
 * app is — {picker, harness, cls (the class a new chat starts in), setup,
 * placeholder}.
 */
export function startOf(app) {
  const hs = app.harness;
  const h = hs.picked();
  const clsId = app.newClassId ? app.newClassId() : app.classId;
  const picker = agentPicker(hs.catalog, hs.pick, { classes: app.classes, classId: app.classId, remembered: hs.sandboxes, list: app.sbx.list, manager: !!(app.me && app.me.manager) });
  const cls = classes.find(app.classes, clsId);
  const pick = app.sbx.pick && app.sbx.pick.ref;
  const s = h && pick ? usable(app.sbx.list).find((x) => x.ref === pick) : null;
  return {
    picker, harness: h, cls,
    setup: h ? setupOf(h, app.sbx.list, pick, cls) : null,
    placeholder: h ? `ask ${nameOf(h)}${s && sandboxFits(h, s).ok ? ` — it works in ${sbxName(s)}` : ''}…` : '',
  };
}

// chooseAgent: who answers new chats (prefs/agent) — a coding agent takes
// the sandbox it starts in along (keepSandbox).
export function chooseAgent(app, id) {
  app.harness.choose(id);
  keepSandbox(app);
}

// newChatPick: the new-chat dialog's choice as its ask's part — {harness,
// class, sandbox} for a coding agent (system and the built-in model don't
// go: a coding agent keeps its own instructions, its model is an option),
// else the built-in agent's (no harness, the model you picked).
export function newChatPick(app, agent, ref, cwd = '') {
  const h = agent && agent !== AGENT ? app.harness.find(agent) : null;
  if (!h || !h.available) return { harness: undefined, ...(app.model ? { model: app.model } : {}) };
  const options = app.harness.options[h.id];
  return {
    harness: { provider: h.id, ...(options && Object.keys(options).length ? { options } : {}) },
    class: resolveClass(h, app.classId) || undefined,
    ...(ref ? { sandbox: { ref, ...(cwd ? { cwd } : {}) } } : { sandbox: undefined }),
    system: undefined, model: undefined,
  };
}

// keepSandbox: at home, while a coding agent answers new chats, the next
// chat's sandbox is one it fits — the one you last used with it, else the
// best (preferredSandbox) — and the one in use is remembered for it
// (prefs/harness-sandbox); a running one it hasn't been looked for in yet
// is probed (GET /harnesses?probe=, once per sandbox). Called on the app's
// 'harness' and 'sandboxes' events (wireStart) and when a pick changes.
const probed = new WeakMap(); // app → Set of refs asked
let busy = false;
export function keepSandbox(app) {
  if (busy || app.sel != null) return;
  const h = app.harness.picked();
  if (!h) return;
  busy = true;
  try {
    app.sbx.ensure();
    const list = app.sbx.list;
    if (!list.loaded) return;
    let pick = app.sbx.pick && app.sbx.pick.ref;
    const find = (ref) => (ref ? usable(list).find((x) => x.ref === ref) : null);
    let s = find(pick);
    if (!(s && sandboxFits(h, s).ok)) {
      const to = preferredSandbox(h, list, app.harness.sandboxes[h.id], pick);
      if (!to) return; // the setup card says what to do
      if (to.value !== pick) app.sbx.choose(to.value).catch(() => {});
      pick = to.value;
      s = find(pick);
    }
    if (app.harness.sandboxes[h.id] !== pick) app.harness.rememberSandbox(h.id, pick);
    const asked = probed.get(app) || new Set();
    probed.set(app, asked);
    if (s && s.state === 'running' && !(h.sandboxes && h.sandboxes[pick]) && !asked.has(pick)) {
      asked.add(pick);
      app.harness.load(pick).catch(() => {});
    }
  } finally { busy = false; }
}

// wireStart: the app keeps a coding agent's sandbox in step (model/app.js calls it once).
export function wireStart(app) {
  const keep = () => keepSandbox(app);
  app.on('harness', keep);
  app.on('sandboxes', keep);
  app.on('home', keep);
}
