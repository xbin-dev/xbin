// model/harness-signins.js — a coding agent's sign-ins in words both views
// draw (D179; API.md "Coding agents" → "Signing in", "Saved sign-ins"):
//
//   - the guided sign-in on a sign-in card (guidedOf, guidedWords): the
//     provider's own CLI runs in the sandbox (Claude Code's `claude auth
//     login`), the page offers its link — Open sign-in page ↗, Copy link —
//     and takes the code it shows (Finish); "Remember for my other
//     sandboxes" runs `claude setup-token` instead and keeps the token as a
//     saved sign-in the backend holds — it never reaches the page;
//   - saved sign-ins (signinsOf, signinGroups, statusOf, keyFor): a
//     person's own, named per coding agent ("Personal", "Work"), one the
//     default; a key or token pasted, renamed, made the default, forgotten
//     — only in a person's own partition (an unpartitioned agent says why;
//     the shared space has none);
//   - a conversation's account (accountOf): the harness chip's "using
//     Work" and the switch menu — the default, another saved sign-in, or
//     the sandbox's own.
//
// Pure (no DOM, no lit): node-tested in hack/agent-template-harness-signins.test.mjs.
import { partitionState } from './partition.js';
import { nameOf, HARNESSES } from './harness.js';

const DAY = 86400000;
export const WARN_DAYS = 14;

// signinsOf: GET /prefs/harness-signins (null: not read yet) →
// {loaded, available, why, list, harnesses, warnDays}.
export function signinsOf(r) {
  if (!r) return { loaded: false, available: false, why: '', list: [], harnesses: {}, warnDays: WARN_DAYS };
  return {
    loaded: true, available: !!r.available, why: String(r.why || ''),
    list: Array.isArray(r.signins) ? r.signins : [], harnesses: r.harnesses && typeof r.harnesses === 'object' ? r.harnesses : {},
    warnDays: Number(r.warnDays) || WARN_DAYS,
  };
}

const fmtDate = (ms) => new Date(ms).toISOString().slice(0, 10);

// statusOf: a saved sign-in's state — {state: ok | expiring | expired |
// refused, tone: ok | warn | bad, text} (now: ms).
export function statusOf(s, now = Date.now(), warnDays = WARN_DAYS) {
  if (!s) return { state: 'ok', tone: 'ok', text: '' };
  if (s.refusedAt) return { state: 'refused', tone: 'bad', text: 'refused — sign in again' };
  if (s.expiresAt && now >= s.expiresAt) return { state: 'expired', tone: 'bad', text: 'expired — sign in again' };
  if (s.expiresAt && now + warnDays * DAY >= s.expiresAt) {
    const d = Math.max(1, Math.ceil((s.expiresAt - now) / DAY));
    return { state: 'expiring', tone: 'warn', text: `expires in ${d} day${d === 1 ? '' : 's'} — sign in again soon` };
  }
  return { state: 'ok', tone: 'ok', text: s.expiresAt ? `valid until ${fmtDate(s.expiresAt)}` : 'saved' };
}

// usable: a saved sign-in a conversation can start with (not refused or expired).
export const usable = (s, now = Date.now()) => !!s && !s.refusedAt && !(s.expiresAt && now >= s.expiresAt);

// keyLabel: what a saved sign-in is — "Claude subscription token (claude
// setup-token)", "OpenAI API key" — from its harness's keys.
export function keyLabel(st, s) {
  const keys = (st.harnesses[s.harness] || {}).keys || [];
  const k = keys.find((x) => x.env === s.env);
  return (k && k.label) || (s.kind === 'setup-token' ? 'subscription token' : 'API key');
}

const harnessName = (st, id) => (st.harnesses[id] || {}).name || HARNESSES[id]?.name || id;

// signinGroups: the Coding-agent sign-ins section — one group per coding
// agent that takes saved sign-ins: {harness, name, mint, keys, rows: [{id,
// name, what, isDefault, status}]}. Coding agents with none saved come
// after the ones with some.
export function signinGroups(st, now = Date.now()) {
  const ids = Object.keys(st.harnesses);
  const groups = ids.map((id) => ({
    harness: id, name: harnessName(st, id), mint: !!st.harnesses[id].mint, keys: st.harnesses[id].keys || [],
    rows: st.list.filter((s) => s.harness === id).map((s) => ({
      id: s.id, name: s.name, what: keyLabel(st, s), isDefault: !!s.isDefault, status: statusOf(s, now, st.warnDays),
      env: s.env, mintedAt: s.mintedAt || 0,
    })),
  }));
  return [...groups.filter((g) => g.rows.length), ...groups.filter((g) => !g.rows.length)];
}

// keyFor: the key of harness's keys a pasted value goes to (the backend's
// acp.Provider.KeyFor): the first whose prefix it starts with, else one
// with none — null when the person must say which (opencode's keys).
export function keyFor(st, harness, value) {
  const keys = (st.harnesses[harness] || {}).keys || [];
  const v = String(value || '').trim();
  return keys.find((k) => k.prefix && v.startsWith(k.prefix)) || keys.find((k) => !k.prefix) || null;
}

// expiringCount: saved sign-ins that want attention (expiring, expired, refused).
export const attention = (st, now = Date.now()) => st.list.filter((s) => statusOf(s, now, st.warnDays).state !== 'ok').length;

// --- a conversation's account ---------------------------------------------------------

// accountOf: the harness chip's account for a coding agent's summary h —
// {shown, label, warn, choices: [{value, label, current, disabled, why}]};
// shown only in a person's own partition, where h carries `signin` and
// saved sign-ins exist (st: signinsOf). value is what PUT
// /runs/{id}/harness/signin takes: "default", "sandbox" or an id.
export function accountOf(h, st, now = Date.now()) {
  const sg = h && h.signin;
  if (!sg || !st || !st.available) return { shown: false, label: '', warn: '', choices: [] };
  const mine = st.list.filter((s) => s.harness === h.provider);
  const def = mine.find((s) => s.isDefault) || null;
  const using = sg.using || null;
  const pick = sg.pick || 'default';
  const live = h.state === 'ready' || h.state === 'working' || h.state === 'starting' || h.state === 'login';
  let label = '';
  if (using && live) label = using.name ? `using ${using.name}` : 'using a forgotten sign-in';
  else if (live) label = 'this sandbox\'s sign-in';
  else if (pick === 'sandbox') label = 'this sandbox\'s sign-in';
  else {
    const s = pick === 'default' ? def : mine.find((x) => x.id === pick) || def;
    label = s ? s.name : (mine.length ? 'this sandbox\'s sign-in' : '');
  }
  const warnOf = (s) => (s ? statusOf(s, now, st.warnDays) : null);
  const cur = using && mine.find((s) => s.id === using.id);
  const w = warnOf(cur);
  const choices = [
    { value: 'default', label: def ? `Default (${def.name})` : 'Default (none saved: this sandbox\'s)', current: pick === 'default', disabled: false, why: '' },
    ...mine.map((s) => {
      const ss = statusOf(s, now, st.warnDays);
      const ok = usable(s, now);
      return { value: s.id, label: s.name, current: pick === s.id, disabled: !ok, why: ok ? (ss.state === 'expiring' ? ss.text : '') : ss.text };
    }),
    { value: 'sandbox', label: 'This sandbox\'s own sign-in', current: pick === 'sandbox', disabled: false, why: '' },
  ];
  return { shown: true, label, warn: w && w.state !== 'ok' ? `${cur.name}: ${w.text}` : '', choices };
}

// switchWords: what a switch says while it goes, and once the backend took it.
export const switchWords = (name, choice) => `Switched to ${choice ? choice.label : 'it'} — ${name} resumes this conversation with it at your next message.`;

// --- the guided sign-in ------------------------------------------------------------------

// rememberOf: may this card's guided sign-in mint a saved sign-in
// (Remember)? {offered, why}: only in a person's own partition, for a coding
// agent whose CLI can (entry.login.mint: claude's setup-token), in a
// sandbox of theirs no one else uses (the backend's gate says so too).
export function rememberOf(c, { entry = null, row = null, me = '', state = partitionState() } = {}) {
  const who = typeof me === 'string' ? me : (me && me.user) || '';
  if (state !== 'user') return { offered: false, why: '' };
  if (!entry || !entry.login || !entry.login.mint) return { offered: false, why: '' };
  if (c.shared) return { offered: false, why: `Remember works only in a sandbox of your own that no one else uses — ${c.sandbox.name} is shared.` };
  const owner = (row && row.owner && row.owner.user) || '';
  if (row && owner && who && owner !== who) return { offered: false, why: `Remember works only in a sandbox of your own — ${c.sandbox.name} is ${owner}'s.` };
  return { offered: true, why: '' };
}

// guidedOf: whether the card offers the guided sign-in — the provider has
// one (GET /harnesses' login.guided) and the card talks.
export const guidedOf = (c, entry) => !!(c && c.talk && !c.gone && !c.ask && entry && entry.login && entry.login.guided);

// A guided sign-in's state, per park (the views keep it; never a code or a
// token): {phase: idle | starting | waiting | finishing | done, url, paste,
// remember, name, err, msg}.
export const newGuided = () => ({ phase: 'idle', url: '', paste: false, remember: false, name: '', err: '', msg: '' });

// guidedStarted / guidedFailed / guidedFinished: the transitions, from the
// backend's answers (POST …/authenticate {method: "guided"}).
export function guidedStarted(g, r) {
  const s = (r && r.signin) || {};
  return { ...g, phase: s.url ? 'waiting' : 'idle', url: isHttps(s.url) ? s.url : '', paste: !!s.paste, err: s.url ? '' : 'No sign-in link came.', msg: '' };
}
export function guidedFailed(g, e) {
  const status = e && e.status;
  // a malformed code: the CLI asks again — the link stands
  if (status === 409 && g.phase === 'finishing') return { ...g, phase: 'waiting', err: e.message, msg: '' };
  return { ...g, phase: 'idle', url: '', paste: false, err: (e && e.message) || String(e), msg: '' };
}
export function guidedFinished(g, r, name) {
  const saved = r && r.saved;
  return { ...g, phase: 'done', err: '', url: '',
    msg: saved ? `Signed in to ${name} — saved as ${saved.name} for your other sandboxes. Sending your message again…` : `Signed in to ${name}. Sending your message again…` };
}

// guidedWords: what the card says in a phase — {status, start, finish, busy}.
export function guidedWords(g, c) {
  const name = c.name;
  switch (g.phase) {
    case 'starting': return { status: `Starting ${name}'s sign-in in ${c.sandbox.name}…`, busy: true };
    case 'waiting': return { status: g.err || (g.paste ? 'Open the sign-in page, sign in, then paste the code it shows here.' : 'Open the sign-in page and sign in.'), busy: false };
    case 'finishing': return { status: `Checking the code with ${name}…`, busy: true };
    case 'done': return { status: g.msg, busy: false };
  }
  return { status: g.err || '', busy: false, start: g.remember ? `Sign in and remember` : `Sign in to ${name}` };
}

// shownMethods: a sign-in card's methods as the views draw them — beside the
// guided sign-in one terminal ("Use a terminal instead": they all run the
// session's one sign-in command), else every method.
export const shownMethods = (c) => (c && c.guided
  ? c.methods.filter((m, i) => m.kind !== 'terminal' || c.methods.findIndex((x) => x.kind === 'terminal') === i)
  : (c && c.methods) || []);

// cleanCode: a pasted code as the CLI reads it — one line, nothing but what
// a code is made of.
export const cleanCode = (s) => String(s || '').replace(/[\s\x00-\x1f\x7f]/g, '');

// isHttps: a page the views may open.
export const isHttps = (u) => /^https:\/\/[^\s]+$/i.test(String(u || ''));

// nameFor: the name a Remember starts with (the field's placeholder): the
// harness's first is Personal.
export const nameFor = (st, harness) => (st.list.some((s) => s.harness === harness) ? '' : 'Personal');

export { nameOf };
