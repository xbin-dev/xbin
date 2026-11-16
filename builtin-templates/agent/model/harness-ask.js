// model/harness-ask.js — what a coding harness asks and how its controls read
// (D147 §4.2.4, §4.2.5, §4.2.9, §4.2.10, §4.3.4, §4.3.12), in words both
// views draw:
//   permission(ps)  a permission park as the harness's own options (reject
//                   first when it defaults to no; an explicit one — it raises
//                   the session to a bypass mode — only for the owner, marked),
//                   the call, a preview of its diff, what "always" remembers;
//                   a plan approval (a plan-mode exit) with its plan
//   question(ps)    a question park: a form from its schema (formFields), or
//                   a page to open (url mode)
//   controls(h)     the live mode and config options (#hctl, the native toolbar)
//   settingOf(e)    the person's Auto / Always approve for a harness
//   slashMatches    the slash-command menu while "/" is typed
//   steerWords(v)   what the composer says while a turn runs; steerTrack
//                   notices a message steered into it (from the stream)
// The cards' words, the form, the slash menu's rule and the composer's words
// are xbind's chat kit's (/vendor/chat/ask.js, D224 — the Agent tab asks the
// same way). This module is the adapter from the template's park
// (pendingState.harness) and run view to the kit's perm and ask blocks
// (permBlock, askBlock), and keeps every export it had.
// Pure (no DOM): node-tested in hack/agent-template-harness-ask.test.mjs.
import { isHarness, nameOf, modeOf } from './harness.js';
import { busy, ownApproval } from './fold.js';
import { barredWhy, BARRED_WORDS } from './harness-homes.js';
import { callIdOf } from './harness-heads.js';
import { permission as permCard, question as askCard, slashCommands as commandsOf, slashMenu, composerWords, steerTracker } from '/vendor/chat/ask.js';

// The kit's, under the names they had here: an option's kind; the form of a
// question (formFields: a single choice → radio — oneOf, anyOf or enum, with
// enumNames — a multiple one → check, boolean, number, else text; a field
// marked as another's custom answer rides on it as `other`; each property's
// `default` as dflt), an accept's content and what it leaves out, the native
// `question` primitive's flat schema and its answers folded back; the words
// that confirm an explicit (bypass) mode or option; a command picked from
// the slash menu.
export { isAllow, isReject, formFields, formContent, missingRequired, nativeSchema, nativeContent, modeConfirm, optionConfirm, slashText } from '/vendor/chat/ask.js';
// …and a request's diff preview: ACP tool content as [{path, lines: [{t:
// '+'|'-'|' '|'@', text}], add, del, more}] per diff or [{text}] per text
// (contentPreview), a tool row's backend-made patches the same way
// (diffsPreview), old → new as its changed lines (lineDiff), a preview's
// diff as patch text for the native `diff` primitive (patchOf).
export { contentPreview, diffsPreview, lineDiff, patchOf } from '/vendor/chat/tools.js';

const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };

// owner: the conversation's owner, a person (explicit options are theirs);
// talk: may answer at all (a participant).
export const ownerOf = (v, me) => !!(v && (!v.access || v.access === 'owner' || v.access === 'system') && me && me.kind === 'user' && !me.viewedBy);

// permBlock: a harness's permission park (pendingState.kind approval with
// harness) as the kit's perm block — null for anything else.
export function permBlock(ps) {
  const hs = ps && ps.kind === 'approval' && ps.harness;
  if (!hs) return null;
  const t = hs.tool || {};
  return {
    k: 'park:' + (ps.park || ''), v: 0, kind: 'perm', pid: hs.pid || '', park: ps.park || '', callId: callIdOf(hs.callId),
    tool: { title: t.title || '', kind: t.kind || '', name: t.name || '', label: t.label || '', command: t.command || '', rawInput: t.rawInput ?? null, content: t.content ?? null },
    options: (hs.options || []).filter(Boolean).map((o) => ({ optionId: o.optionId, name: o.name, kind: o.kind, explicit: !!o.explicit })),
    rule: hs.rule ? { kind: hs.rule.kind || '', title: hs.rule.title || '' } : null,
    title: '', description: hs.description || '', defaultToNo: !!hs.defaultToNo,
    plan: hs.planApproval ? { text: hs.plan || '', path: t.rawInput && typeof t.rawInput.planFilePath === 'string' ? t.rawInput.planFilePath : '' } : null,
    by: null, optionId: null,
  };
}

// permission: a harness's permission park as a card — null for anything
// else. acp: the call's tool row's acp (§4.3.5), whose backend-made patches
// preview an edit when the park's own content has no diff.
export function permission(ps, { owner = false, talk = true, name = 'the coding agent', acp = null } = {}) {
  const perm = permBlock(ps);
  if (!perm) return null;
  const first = ps.toolCalls && ps.toolCalls[0] && ps.toolCalls[0].function && ps.toolCalls[0].function.name;
  if (!perm.tool.title && first) perm.tool.title = first;
  return { park: perm.park, ...permCard(perm, { name, can: { answer: talk, raise: owner }, diffs: acp && acp.diffs }), talk };
}

// --- a question (elicitation) ---------------------------------------------------------

// askBlock: a harness's question park as the kit's ask block — null for
// anything else.
export function askBlock(ps) {
  const hs = ps && ps.kind === 'question' && ps.harness;
  if (!hs) return null;
  const url = hs.url || '';
  return { k: 'park:' + (ps.park || ''), v: 0, kind: 'ask', eid: hs.eid || '', park: ps.park || '', callId: callIdOf(hs.callId), message: hs.message || '',
    mode: url || hs.mode === 'url' ? 'url' : 'form', schema: hs.schema ?? null, url, action: null, by: null, content: null };
}

// parkBlock: what a run is parked on, as the kit's block that asks — a
// coding agent's request (pendingState.harness: its permission, its
// question, its sign-in), else the template's own approval (the calls its
// engine wants to run) or question (the run's result while it waits for an
// answer). null: it waits on nobody.
export function parkBlock(run) {
  const ps = run && run.status === 'waiting_input' ? run.pendingState : null;
  if (!ps) return null;
  const k = 'park:' + (ps.park || '');
  const hs = ps.harness;
  if (!hs) {
    if (ps.kind === 'approval') return ownApproval(k, ps.park || '', ps.toolCalls);
    return run.result ? { k, v: 0, kind: 'ask', eid: '', park: ps.park || '', callId: '', message: run.result, mode: 'text', schema: null, action: null, by: null, content: null } : null;
  }
  if (ps.kind === 'approval') return permBlock(ps);
  if (ps.kind === 'question') return askBlock(ps);
  if (ps.kind === 'login') {
    const l = hs.login || {};
    return { k, v: 0, kind: 'signin', park: ps.park || '', provider: nameOf(run.harness), command: l.command || '', methods: l.methods || [], device: l.device || null };
  }
  return null;
}

// question: a harness's question park — {park, message, mode: 'form', fields}
// or {mode: 'url', url} (a page to open, then Done) — null for anything else.
export function question(ps) {
  const ask = askBlock(ps);
  return ask ? { park: ask.park, ...askCard(ask) } : null;
}

// --- the mode and config options (§4.2.4) ---------------------------------------------

// controls: the live session's mode and options as the #hctl popover and the
// native toolbar draw them — null for a run that isn't a harness's. A mode
// that raises the session to a bypass mode (explicit) is the owner's only.
export function controls(h, entry = null, { owner = false, talk = true } = {}) {
  if (!h) return null;
  const m = modeOf(h, entry);
  const modes = m.available.map((x) => ({
    id: x.id, name: x.name || x.id, description: x.description || '', explicit: !!x.explicit, current: x.id === m.current,
    allowed: talk && (!x.explicit || owner),
  }));
  const options = (h.options || []).filter((o) => o && o.id && o.category !== 'mode').map((o) => {
    const choices = (o.options || []).map((c) => ({ value: c.value, name: c.name || String(c.value), description: c.description || '' }));
    const cur = choices.find((c) => c.value === o.currentValue);
    return { id: o.id, name: o.name || o.id, category: o.category || '', description: o.description || '', value: o.currentValue ?? '', valueName: cur ? cur.name : String(o.currentValue ?? ''), choices };
  });
  // the words alone (D184): a view marks an explicit (bypass) mode with its warning glyph (mode.explicit)
  const label = [m.name, ...options.map((o) => o.valueName)].filter(Boolean).join(' · ') || 'Mode';
  return { name: nameOf(h), mode: { current: m.current, name: m.name, explicit: m.explicit }, modes, options, label: clip(label, 48), talk, owner };
}

// --- Auto / Always approve (§4.3.12) ------------------------------------------------------

// settingOf: the person's setting for a harness (a catalog entry) — its value
// and the two choices; Auto is off for a harness without an auto mode.
export function settingOf(entry, setting = 'approve') {
  const name = nameOf(entry);
  const canAuto = !!(entry && entry.autoMode);
  const mode = (id) => ((entry && entry.modes) || []).find((x) => x.id === id)?.name || id || '';
  const ask = mode(entry && (entry.approveMode || entry.defaultMode));
  return {
    provider: (entry && entry.id) || '', name, canAuto,
    value: setting === 'auto' && canAuto ? 'auto' : 'approve',
    choices: [
      { value: 'approve', label: 'Always approve', title: `${name} asks you before it edits or runs anything${ask ? ` (${ask})` : ''}` },
      { value: 'auto', label: 'Auto', disabled: !canAuto,
        title: canAuto ? `${name} goes ahead on its own (${mode(entry.autoMode)}) — it may still ask now and then` : `${name} has no auto mode — it asks as its own settings say` },
    ],
    note: `How ${name} starts for you — your new conversations and the coding agents started for you (a project's tasks start in a mode of their own: the project's settings say what a request of theirs does).`,
  };
}

// --- slash commands -------------------------------------------------------------------

// slashCommands: a harness's advertised commands ({name, description, hint}).
export const slashCommands = (h) => commandsOf(h && h.commands);

// slashMatches: the commands the draft names so far — null while the draft
// isn't a command being typed ("/" and a name, no space yet); names that
// start with it first, then names or descriptions containing it (the kit's
// one rule, at most eight).
export const slashMatches = (cmds, draft) => slashMenu(cmds, draft);

// --- steering (§3.5, §4.2.10) -------------------------------------------------------------

// steerWords: what the composer says on a harness conversation (null: the
// built-in words) — the placeholder, and how a queued chip is labelled: while
// a turn runs a message steers it (an adapter that steers) or waits for it
// to end; ⌘/Ctrl+Enter (native: Send now) interrupts it.
export function steerWords(v, { native = false } = {}) {
  const r = v && v.run;
  if (!isHarness(r) || v.access === 'viewer') return null;
  const h = r.harness || {};
  const ps = r.status === 'waiting_input' ? r.pendingState || {} : {};
  return composerWords({
    name: nameOf(h), native, busy: busy(r.status), steering: !!h.steering,
    // one in the shared space never runs again (model/harness-homes.js barredWhy): the composer is off, saying why
    barred: barredWhy(r) ? BARRED_WORDS : '',
    waits: ps.kind === 'login' || h.state === 'login' ? 'login' : ps.kind === 'approval' || ps.kind === 'question' ? ps.kind : '',
  });
}

// steerTrack: a tracker that notices, from the stream, a message of the
// composer's steered into a running turn: a queued message of an adapter
// that steers, queued while a turn ran, left the queue while it still ran
// and then showed up in the transcript. A message sent while no turn runs
// is queued too (the real backend's view lists every waiting `hprompt`)
// until it becomes the next prompt — that is no steer; nor is a new
// conversation's first message (its run is running while the agent starts,
// and the summary says `working` once the session is up, before the prompt
// is sent: there is no earlier turn to steer). track(v, blocks, now) → the
// notes to show ([{text, until}]), each for `ms`. A taken-back message never
// shows up, so never counts.
export function steerTrack(ms = 6000) {
  const track = steerTracker(ms);
  return (v, blocks, now) => {
    const r = v && v.run;
    if (!isHarness(r)) return track(null, now);
    const users = (blocks || []).filter((b) => b.k === 'user');
    return track({
      run: r.id, steering: !!(r.harness && r.harness.steering), queued: v.queued || [],
      turn: busy(r.status) && !!(r.harness && r.harness.state === 'working') && users.length > 0,
      count: (text) => users.reduce((n, b) => n + (b.text === text ? 1 : 0), 0),
    }, now);
  };
}
