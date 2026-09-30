// model/terminals.js — terminals onto coding sandboxes and a coding agent's
// sign-in, in words both views draw (D147 §2.1, §4.2.6, §4.2.8, §4.3.4):
//
// - the terminal dock's tabs (termsOf(app): page-level — terminals belong to
//   sandboxes, so they outlive the conversation they were opened from): a
//   shell in a sandbox, or a login tab running a coding agent's sign-in
//   command; each tab is what <bx-terminal src> dials (the web: the
//   manager's tty route, as the person) — the native view opens one at a
//   time through the tile's relay (runTerminalSrc, model/sandboxes.js
//   relaySrc);
// - the sign-in card (signIn): a harness run parked on `login` — its
//   methods (a terminal login, an API key, a device code), the sandbox whose
//   HOME the credentials land in, whether that sandbox is shared (a confirm
//   first), who to ask when the person may not use it, and — when the
//   sandbox is gone or its manager down — that there is nothing to sign in
//   to there; read-only, saying why, where the credentials wouldn't stay
//   the person's (a partitioned agent: model/harness-homes.js).
//
// Pure (no DOM, no lit): node-tested in hack/agent-template-harness-term.test.mjs.
import { ICON, bindingOf, sharesOf, brokenWhy, splitRef } from './sandboxes.js';
import { harnessOf, nameOf } from './harness.js';
import { access } from './rules.js';
import { homeOf } from './homes.js';
import { signInAway } from './harness-homes.js';

// --- the dock ---------------------------------------------------------------------

// A tab: {key, gen (a New shell in place bumps it: a fresh element), purpose
// ('shell' | 'login'), ref, id, name, cwd, cmd, manager, src, base (the
// manager's url — '' through the relay: nothing to end the shell with),
// run (a login tab's harness run), harness (its name), session (the exec
// the session frame named), ended ('' | why)}.
const TAB = ['ref', 'id', 'name', 'cwd', 'cmd', 'manager', 'src', 'base', 'purpose', 'run', 'harness'];

// createTerms: the dock — its tabs, the one shown, hidden (▾ Hide keeps the
// shells running) and max. changed() is called after every change.
export function createTerms(changed = () => {}) {
  let seq = 0;
  const T = {
    tabs: [],
    active: 0,      // the key of the tab shown
    hidden: false,  // the dock is folded away; its shells run on
    max: false,     // the dock fills the window
    get current() { return T.tabs.find((t) => t.key === T.active) || null; },
    get(key) { return T.tabs.find((t) => t.key === key) || null; },
    // open adds a tab for spec (app.sbx.terminal()'s answer, plus purpose,
    // run and harness for a login tab) and shows it; null when spec has no src.
    open(spec) {
      if (!spec || !spec.src) return null;
      const t = { purpose: 'shell', cmd: '', run: 0, harness: '', base: '', manager: '', cwd: '' };
      for (const k of TAB) if (spec[k] != null && spec[k] !== '') t[k] = spec[k];
      Object.assign(t, { key: ++seq, gen: 1, session: '', ended: '' });
      T.tabs.push(t);
      T.active = t.key;
      T.hidden = false;
      changed();
      return t;
    },
    select(key) {
      if (!T.get(key)) return;
      T.active = key;
      T.hidden = false;
      changed();
    },
    // close takes a tab away and answers it (the view ends its shell when it
    // has a session that did not end); the neighbour is shown.
    close(key) {
      const i = T.tabs.findIndex((t) => t.key === key);
      if (i < 0) return null;
      const [t] = T.tabs.splice(i, 1);
      if (T.active === key) T.active = (T.tabs[i] || T.tabs[i - 1] || {}).key || 0;
      if (!T.tabs.length) { T.hidden = false; T.max = false; }
      changed();
      return t;
    },
    // session: the exec a tab's session frame named (what closing it ends).
    session(key, id) { const t = T.get(key); if (t) t.session = id || ''; },
    ended(key, why = 'ended') {
      const t = T.get(key);
      if (!t || t.ended) return;
      t.ended = why;
      changed();
    },
    // again: a New shell in the same tab (spec: a fresh terminal() — a login
    // tab becomes a shell); a new element dials it.
    again(key, spec) {
      const t = T.get(key);
      if (!t || !spec || !spec.src) return;
      Object.assign(t, { src: spec.src, base: spec.base || '', cmd: spec.cmd || '', purpose: spec.purpose || 'shell', gen: t.gen + 1, session: '', ended: '' });
      if (t.purpose !== 'login') { t.run = 0; t.harness = ''; }
      changed();
    },
    hide() { if (T.tabs.length && !T.hidden) { T.hidden = true; changed(); } },
    show() { if (T.hidden) { T.hidden = false; changed(); } },
    toggleMax() { T.max = !T.max; changed(); },
    // pill: the hidden dock in words ('' while shown or empty).
    pill() { return T.hidden && T.tabs.length ? `${T.tabs.length} terminal${T.tabs.length === 1 ? '' : 's'}` : ''; },
  };
  return T;
}

// termsOf: the app's dock (one per page), made the first time a view asks.
export function termsOf(app) {
  if (!app.terms) app.terms = createTerms(() => app.emit && app.emit('terms'));
  return app.terms;
}

// tabLabel: a tab's name in the strip — the sandbox, or what it signs in.
export const tabLabel = (t) => (t.purpose === 'login' ? `Sign in · ${t.harness || 'coding agent'}` : `${ICON} ${t.name}`);

// tabHead: the shown tab's header — {title, where, manager, hint}.
export function tabHead(t) {
  if (!t) return null;
  const login = t.purpose === 'login';
  return {
    title: login ? `Sign in · ${t.harness || 'coding agent'} · ${ICON} ${t.name}` : `${ICON} ${t.name}`,
    where: t.cwd || 'its workdir',
    manager: t.manager || '',
    // a sign-in prints a link: opening it needs the tile's open-links grant
    hint: login ? 'a link in the terminal opens in a new tab — if it doesn\'t, copy it from the terminal' : '',
    // a finished login: "Signed in? Retry ‹name›" (POST /resume) or a New shell
    retry: login && t.run ? `Retry ${t.harness || 'the coding agent'}` : '',
    done: login && t.ended ? 'Finished. Signed in?' : '',
  };
}

// runTerminalSrc: a harness run's relay (tile-relative) — a shell at its cwd,
// or (login) its sign-in command: GET /runs/{id}/harness/terminal[?login=1].
// A run at the global instance while this page is a person's partition (a
// shared conversation's, model/homes.js) is relayed there: the app's
// terminal dials a path, so the path asks xbind for it
// (?xbin-partition=global), as model/app.js uploadTarget does. While its
// screen is up the relay's socket keeps the partition it reaches running.
export function runTerminalSrc(runId, { login = false } = {}) {
  const q = [login ? 'login=1' : '', homeOf(runId) === 'global' ? 'xbin-partition=global' : ''].filter(Boolean).join('&');
  return `runs/${runId}/harness/terminal${q ? '?' + q : ''}`;
}

// --- the sign-in card ---------------------------------------------------------------

// A method's kind as the card draws it: terminal, api-key or device-code (§4.3.2).
const KINDS = ['terminal', 'api-key', 'device-code'];

// sharedOf: others may use sandbox row s (team visibility, members, shares).
const sharedOf = (s) => !!(s && (s.visibility === 'team' || (Array.isArray(s.members) && s.members.length) || sharesOf(s).length || s.shared));

// signIn: the sign-in card for view v — null unless its run is a harness run
// parked on `login` (pendingState.kind "login", §4.3.4). opts: list (GET
// /sandboxes, model/sandboxes.js listOf), entry (the harness's catalog
// entry: its login command when the park has none), me (GET /me, or its
// user: who reads the card). Answers
//   {run, park, name, sandbox: {ref, name, cwd}, command,
//    methods: [{id, name, kind}] (terminal · api-key · device-code),
//    shared (a confirm first), canUse (true · false · null: not known — not
//    read yet, or not listed),
//    ask ('' | what to do when you may not use it: whom to ask),
//    gone ('' | why the sandbox can't be reached: the list read has no row
//    for it — gone from its manager, its manager unbound or down — as
//    GET /sandboxes lists every sandbox bound to a conversation you can
//    see), goneText (the card's words for it, in place of the methods),
//    device ({url, message} while a device code waits),
//    title, warn, confirmLabel,
//    talk (false: a view-only reader, or a sign-in this page doesn't offer
//    — the card says why in view and offers nothing), view,
//    away ('' | why this page offers no sign-in: model/harness-homes.js
//    signInAway — the global instance's page, or a shared conversation's
//    run in a person's partition, where the credentials wouldn't stay theirs)}
export function signIn(v, { list = null, entry = null, me = null } = {}) {
  const r = v && v.run;
  const ps = r && r.pendingState;
  if (!r || r.status !== 'waiting_input' || !ps || ps.kind !== 'login' || !ps.harness) return null;
  const h = harnessOf(v) || {};
  const name = nameOf(h);
  const login = { ...(h.login || {}), ...(ps.harness.login || {}) };
  const bound = bindingOf(v) || {};
  const hs = h.sandbox || {};
  const ref = hs.ref || bound.ref || (v.config && v.config.harness && v.config.harness.ref) || '';
  const row = ((list && list.sandboxes) || []).find((s) => s.ref === ref) || null;
  const sname = hs.name || (row && row.name) || bound.name || ref.split('|').pop() || 'the sandbox';
  const cwd = hs.cwd || (v.config && v.config.harness && v.config.harness.cwd) || bound.cwd || '';
  const command = login.command || (entry && entry.login && entry.login.command) || '';
  let methods = (login.methods || []).filter((m) => m && KINDS.includes(m.kind));
  // no methods said: the login command in a terminal
  if (!methods.length && command) methods = [{ id: '', name: `Sign in to ${name}`, kind: 'terminal' }];
  // not in the list read: not "someone else's" (the list has every sandbox a
  // conversation you see is bound to) — gone, or its manager unbound or down
  const listed = !!(list && list.loaded);
  const gone = !row && listed && ref ? brokenWhy({ ref, manager: hs.manager || bound.manager || '' }, null, list) || 'gone — its manager no longer has it' : '';
  const mgr = gone ? (list.managers || []).find((m) => m.provider === splitRef(ref).provider) : null;
  const canUse = row ? !!row.canUse : null;
  const who = typeof me === 'string' ? me : (me && me.user) || '';
  const owner = (row && row.owner && row.owner.user) || '';
  const binder = bound.by || owner;
  const newChat = `start a new chat with ${name} in another sandbox`;
  const shared = !!hs.shared || sharedOf(row);
  const away = signInAway(r.id);
  const talk = access(v).talk && !away;
  return {
    run: r.id, park: ps.park || '', name, command, methods, shared, canUse,
    sandbox: { ref, name: sname, cwd },
    // someone else's: ask them; yours no longer (a share taken back): its owner, or another sandbox
    ask: canUse !== false ? '' : binder && binder !== who ? `Ask ${binder} to sign in — the sandbox is theirs.`
      : `You may no longer use ${sname} — ${owner && owner !== who ? `ask ${owner} to share it with you again, or ` : ''}${newChat}.`,
    gone,
    goneText: gone ? `${ICON} ${sname}: ${gone}. ${name} can't sign in there — ${mgr && mgr.ok === false ? 'Retry once it is back, or ' : ''}${newChat}.` : '',
    device: login.device && login.device.url ? { url: login.device.url, message: login.device.message || '' } : null,
    title: talk ? `${name} needs you to sign in (in ${ICON} ${sname}).` : `${name} is waiting for a sign-in (in ${ICON} ${sname}).`,
    talk, away, view: away || (talk ? '' : 'You may only read this conversation: someone who may write in it signs it in.'),
    warn: `The credentials land in ${sname}'s home: anyone who may use it acts as you with ${name} there, and its clones and snapshots keep them.`,
    confirmLabel: `${sname} is shared — sign in anyway`,
  };
}

// methodLabel: a method's button.
export function methodLabel(m) {
  if (m.kind === 'terminal') return `${m.name} — in a terminal`;
  if (m.kind === 'device-code') return m.name || 'Sign in with a device code';
  return m.name || 'API key';
}

// isHttps: a device page the views may open (never anything but https:).
export const isHttps = (u) => /^https:\/\/[^\s]+$/i.test(String(u || ''));
