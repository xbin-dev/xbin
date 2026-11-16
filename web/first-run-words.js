/**
 * first-run-words.js — the first-run sheet's words and rules
 * (/vendor/first-run-words.js, D236): pure, for /vendor/bx-first-run.js,
 * the shell's hook (shell-first-run.js) and hack/first-run.test.mjs.
 *
 *   signInName(name)                 the account id a name gives
 *   plan({vault, llm, ws})           what the sheet asks and what its button does
 *   check(fields, plan, only?)       the fields' errors, by field, before anything is sent
 *   fieldOf(step, status, message)   which field a refusal is shown next to
 *   SKIP_KEY, DONE_KEY               this browser's "Not now", and the reload's "open the dock"
 */

/** localStorage: "Not now" in this browser (any value). */
export const SKIP_KEY = 'xbin-first-run-skipped';
/** sessionStorage: the sheet finished; "agent" when the dock is to open. */
export const DONE_KEY = 'xbin-first-run';

export const MIN_PASSWORD = 8; // POST /users's rule (docs/protocol/users.md)
const ID = /^[a-z0-9][a-z0-9._-]{0,31}$/;
const RESERVED = new Set(['owner']);
// letters NFKD doesn't take apart
const FOLD = { 'ł': 'l', 'đ': 'd', 'ø': 'o', 'æ': 'ae', 'œ': 'oe', 'ß': 'ss', 'þ': 'th', 'ð': 'd', 'ı': 'i', 'ħ': 'h', 'ŧ': 't' };

/**
 * signInName(name): the sign-in name a person's name gives — its first
 * word, lowercase, accents taken off ("Łukasz Nowak" → "lukasz"); '' when
 * nothing of it is a letter or digit a sign-in name may hold.
 */
export function signInName(name) {
  const s = String(name || '').trim().toLowerCase().normalize('NFKD').replace(/[̀-ͯ]/g, '')
    .replace(/[łđøæœßþðıħŧ]/g, (c) => FOLD[c]);
  const words = s.split(/[^a-z0-9]+/).filter(Boolean);
  if (!words.length) return '';
  let id = words[0].slice(0, 32);
  if (RESERVED.has(id)) id = (words[1] ? `${id}.${words[1]}` : `${id}1`).slice(0, 32);
  return id;
}

/** The sheet's words. */
export const WORDS = Object.freeze({
  bar: 'xbin · first run',
  title: 'Welcome to your workspace',
  lead: 'Make your account: this browser signs in with it now, and you use it from here on.',
  name: 'Your name',
  nameHint: 'Your sign-in name is made from it.',
  signsInAs: 'You sign in as',
  noName: 'Choose the name you sign in with.',
  change: 'Change',
  username: 'Sign-in name',
  usernameHint: 'Letters a–z, digits, dot, dash or underscore; up to 32.',
  password: 'A password',
  passwordHint: `At least ${MIN_PASSWORD} characters.`,
  vault: 'The vault',
  vaultNew: 'A passphrase for the vault',
  vaultRepeat: 'The passphrase again',
  vaultLine: "It encrypts this workspace's secrets and data, and you type it again after each restart. The passphrase cannot be recovered — losing it loses the data.",
  vaultUnlock: "The vault's passphrase",
  vaultUnlockLine: 'The vault is locked since xbin last started: its passphrase opens it again.',
  agentLine: 'Your workspace agent is an AI assistant that plans and does work across your tiles for you, and asks you before it changes anything.',
  agentNoModel: 'It needs a language model, and none is connected yet: connect one on the LLM page afterwards (/xbin/llm), then turn it on.',
  agentHostOn: 'Your host has turned it on for you: it opens once you are in.',
  agentHostOff: 'Your host has turned workspace agents off in this workspace.',
  agentSet: 'An admin has set workspace agents up already: they are in the top bar once you are in.',
  primaryAgent: 'Create my account and turn on my workspace agent',
  primary: 'Create my account',
  quiet: 'Create my account without it',
  skip: 'Not now — set up by hand',
  token: 'The sign-in link with the token keeps working until you turn it off: Admin → Users → sign-in security.',
  busy: { account: 'Making your account…', signin: 'Signing this browser in…', vault: 'Setting up the vault…', agent: 'Turning on your workspace agent…', done: 'Opening your workspace…' },
  leftTitle: 'Your account is ready',
  leftText: (id) => `This browser is signed in as ${id}. Left to do:`,
  vaultLeft: (why) => `The vault isn't set up (${why}). Set it up in the admin console: Runtime → vault.`,
  agentLeft: (why) => `Your workspace agent isn't on (${why}). Use “set up workspace agent” in the top bar.`,
  continue: 'Continue',
  madeTitle: 'Your account is made',
  madeText: (id, why) => `This browser couldn't switch to it (${why}). Sign in as ${id} with the password you chose.`,
  signIn: 'Sign in',
  errors: {
    name: 'Your name, please.',
    id: "That can't be a sign-in name: letters a–z, digits, dot, dash or underscore, starting with a letter or digit; up to 32.",
    reserved: 'That sign-in name is taken by xbin itself: choose another.',
    password: `At least ${MIN_PASSWORD} characters, please.`,
    vault: 'A passphrase, please.',
    repeat: "The two passphrases don't match.",
    unlock: "The vault's passphrase, please.",
    wrongUnlock: "That isn't the vault's passphrase.",
    offline: "xbin can't be reached: nothing was made. Try again.",
  },
});

const zeroTime = (t) => !t || /^0001-01-01/.test(String(t));

/**
 * plan({vault, llm, ws}) — from GET /vault-status, GET /llm and GET
 * /delegate/workspace (null where unread) — what the sheet asks:
 *   vault  'new' (a passphrase and its repeat) | 'unlock' (one) | ''
 *   agent  {kind, body?}: offer (the button sends PUT /delegate/workspace
 *          body), host-on, host-off, no-model, set, none
 *   primary / quiet  the buttons' words (quiet '' when there is none)
 *   line   the sentence under the agent's own ('' none)
 */
export function plan({ vault = null, llm = null, ws = null } = {}) {
  const mode = vault && vault.mode;
  const v = mode === 'unconfigured' ? 'new' : mode === 'sealed' ? 'unlock' : '';
  const agent = agentPlan(llm, ws);
  const offer = agent.kind === 'offer';
  const line = { 'host-on': WORDS.agentHostOn, 'host-off': WORDS.agentHostOff, 'no-model': WORDS.agentNoModel, set: WORDS.agentSet }[agent.kind] || '';
  return { vault: v, agent, primary: offer ? WORDS.primaryAgent : WORDS.primary, quiet: offer ? WORDS.quiet : '', line, says: agent.kind !== 'none' };
}

function agentPlan(llm, ws) {
  if (!ws || !ws.agent) return { kind: 'none' }; // this xbind ships no workspace agent (or it couldn't be read)
  const locks = ws.locks || {};
  if (locks.enabled && !ws.enabled) return { kind: 'host-off' };
  if (!(llm && llm.configured)) return { kind: 'no-model' };
  const who = ws.who || {};
  if (locks.enabled) return locks.who ? { kind: 'host-on' } : { kind: 'offer', body: { who: 'me' } }; // on by the host; who is ours to say
  if (zeroTime(ws.at)) return { kind: 'offer', body: { enabled: true, who: 'me' } }; // never switched: the one state this turns it on in
  if (ws.enabled && !who.chosen) return { kind: 'offer', body: { who: 'me' } };
  return { kind: 'set' }; // an admin switched it already: left as they set it
}

/**
 * check(fields, plan, only): {field: message} for what can be told before
 * anything is sent — fields {name, id, password, vault, repeat}; only, a
 * field name, checks that one alone (Enter in it).
 */
export function check(f, p, only = '') {
  const e = {};
  const want = (k) => !only || only === k;
  const named = !!String(f.name || '').trim(), id = String(f.id || '');
  if (want('name') && !named) e.name = WORDS.errors.name;
  if ((!only && named) || only === 'id') { // (no name yet: that is the one thing to say)
    if (RESERVED.has(id)) e.id = WORDS.errors.reserved;
    else if (!ID.test(id)) e.id = WORDS.errors.id;
  }
  if (want('password') && [...String(f.password || '')].length < MIN_PASSWORD) e.password = WORDS.errors.password;
  if (p && p.vault === 'new') {
    if (want('vault') && !f.vault) e.vault = WORDS.errors.vault;
    if (want('repeat') && f.vault && f.repeat !== f.vault) e.repeat = WORDS.errors.repeat;
  }
  if (p && p.vault === 'unlock' && want('vault') && !f.vault) e.vault = WORDS.errors.unlock;
  return e;
}

/**
 * fieldOf(step, status, message): where a refusal goes — 'name', 'id',
 * 'password', 'vault' or 'form'. step: account (POST /users), vault.
 */
export function fieldOf(step, status, message) {
  const m = String(message || '').toLowerCase();
  if (step === 'vault') return 'vault';
  if (step !== 'account') return 'form';
  if (status === 409 || /already exists|reserved|invalid id|\bid\b/.test(m)) return 'id';
  if (/password/.test(m)) return 'password';
  if (/name/.test(m)) return 'name';
  return 'form';
}
