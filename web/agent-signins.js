/**
 * agent-signins.js — the saved coding-agent sign-ins as a client uses them
 * (D230; docs/agent-signins.md, docs/protocol/signins.md): the calls to
 * /api/xbin/agent/signins and the words for what they answer. No lit, no
 * DOM: <bx-agent-signin> draws with it, and node tests it
 * (hack/agent-signins.test.mjs).
 *
 * Nothing here ever holds a saved secret: a pasted key goes from the field
 * into one request body and is dropped; the guided sign-in's token never
 * leaves xbind.
 */

const BASE = '/api/xbin/agent/signins';

/** An API refusal: `status`, and `message` — the server's own words. */
export class SigninsError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}

async function call(fetchFn, method, path, body) {
  let res;
  try {
    res = await fetchFn(BASE + path, { method, headers: body ? { 'Content-Type': 'application/json' } : undefined, body: body ? JSON.stringify(body) : undefined });
  } catch (e) {
    throw new SigninsError(0, 'xbind could not be reached — check the connection and try again.');
  }
  let data = null;
  try { data = await res.json(); } catch { /* an empty body */ }
  if (!res.ok) throw new SigninsError(res.status, (data && data.error) || `the request failed (${res.status})`);
  return data || {};
}

/**
 * signinsApi(fetchFn?): the calls. Each resolves to the route's answer or
 * rejects with a SigninsError.
 */
export function signinsApi(fetchFn = (...a) => fetch(...a)) {
  const c = (m, p, b) => call(fetchFn, m, p, b);
  const id = (s) => encodeURIComponent(s);
  return {
    list: (tile) => c('GET', tile ? `?tile=${id(tile)}` : ''),
    paste: (harness, secret, name, makeDefault) => c('POST', '', { harness, secret, ...(name ? { name } : {}), ...(makeDefault ? { default: true } : {}) }),
    rename: (sid, name) => c('PUT', `/${id(sid)}`, { name }),
    makeDefault: (sid) => c('PUT', `/${id(sid)}`, { default: true }),
    remove: (sid) => c('DELETE', `/${id(sid)}`),
    dismissNotice: () => c('DELETE', '/notice'),
    uses: () => c('GET', '/uses'),
    guided: (harness, name, makeDefault) => c('POST', '/guided', { harness, ...(name ? { name } : {}), ...(makeDefault ? { default: true } : {}) }),
    code: (flow, code) => c('POST', `/guided/${id(flow)}`, { code }),
    cancel: (flow) => c('DELETE', `/guided/${id(flow)}`),
    people: () => c('GET', '/people'),
    revoke: (user, sid) => c('DELETE', `/people/${id(user)}/${id(sid)}`),
    setPolicy: (enabled) => c('PUT', '/policy', { enabled }),
  };
}

/** harnessOf(view, id): one coding agent's row of GET /agent/signins. */
export const harnessOf = (view, id) => (view?.harnesses || []).find((h) => h.id === id) || null;

/**
 * whyNot(view): why nothing can be saved right now, as a sentence that says
 * what to do — or '' when it can.
 */
export function whyNot(view) {
  if (!view) return '';
  if (!view.available) return view.why || 'This workspace keeps no saved sign-ins.';
  if (view.policy && !view.policy.enabled) {
    return view.policy.locked ? 'Saving sign-ins is switched off for this workspace by its host.' : 'Saving sign-ins is switched off for this workspace — a workspace admin can switch it on.';
  }
  if (view.vault === 'sealed') return 'The workspace vault is sealed: saved sign-ins can be used and changed again once an admin unseals it.';
  if (view.vault === 'locked') return 'The workspace vault is not set up yet, and a sign-in is never kept unsealed — an admin sets the vault up first.';
  return '';
}

/** kindWords(kind): a sign-in's kind for people. */
export const kindWords = (kind) => (kind === 'setup-token' ? 'subscription token' : 'API key');

const DAY = 86400000;

/** dayWords(ms, now): "today", "yesterday", "12 days ago", or the date. */
export function dayWords(ms, now = Date.now()) {
  if (!ms) return 'never';
  const days = Math.floor((startOfDay(now) - startOfDay(ms)) / DAY);
  if (days <= 0) return 'today';
  if (days === 1) return 'yesterday';
  if (days < 60) return `${days} days ago`;
  return new Date(ms).toISOString().slice(0, 10);
}

function startOfDay(ms) { const d = new Date(ms); d.setHours(0, 0, 0, 0); return d.getTime(); }

/**
 * expiryWords(sg, now): what to say of a sign-in's expiry — '' while it is
 * far off or unknown, "expires in 12 days" from 30 days before, "expired".
 */
export function expiryWords(sg, now = Date.now()) {
  if (!sg?.expiresAt) return '';
  const left = sg.expiresAt - now;
  if (left <= 0) return 'expired — sign in again';
  const days = Math.ceil(left / DAY);
  if (days > 30) return '';
  return days === 1 ? 'expires tomorrow' : `expires in ${days} days`;
}

/**
 * signinChip(sn, provider): the header's word on what a session signs in
 * with, from its row's or its notice's `signin` ({source, name?, why?}) —
 * {text, title, tone} or null when there is nothing to say. tone: '' |
 * 'warn' (a saved sign-in that was left out).
 */
export function signinChip(sn, provider = 'the agent') {
  if (!sn || !sn.source) return null;
  const left = sn.why ? ` Your saved sign-in ${sn.name || ''} isn't used here: ${sn.why}.`.replace('  ', ' ') : '';
  switch (sn.source) {
    case 'saved':
      return { text: `your saved sign-in: ${sn.name}`, title: `${provider} runs on the sign-in you saved with xbind (${sn.name}).`, tone: '' };
    case 'home':
      return { text: "this tile's own login", title: `${provider} runs on the login it has in your home on this workspace.${left}`, tone: sn.why ? 'warn' : '' };
    case 'connector':
      return sn.why ? { text: 'saved sign-in not used', title: left.trim(), tone: 'warn' } : null; // the connector's own chip says the rest
    default:
      return sn.why ? { text: 'saved sign-in not used', title: left.trim(), tone: 'warn' } : null;
  }
}

/**
 * savedHint(prov): the launcher's words for a provider row's `saved` (GET
 * /agent/providers): what a session started now would sign in with — ''
 * when the person has no saved sign-in for it.
 */
export function savedHint(prov) {
  const s = prov?.saved;
  if (!s) return '';
  return s.inject ? `your saved sign-in: ${s.name}` : `saved sign-in ${s.name} not used here — ${s.why}`;
}

/**
 * startSignInWords(h): a brief's or a plan line's words for one harness of
 * GET /delegate/start-defaults ({name, signIn}) — {text, needs}
 * where needs is true when the person has to sign in first.
 */
export function startSignInWords(h) {
  const name = h?.name || 'the coding agent';
  switch (h?.signIn) {
    case 'saved': return { text: `your saved ${name} sign-in, once you approve the start`, needs: false };
    case 'connector': return { text: "the workspace's model — no sign-in needed", needs: false };
    case 'own': return { text: `the ${name} login on this workspace`, needs: false };
    case 'needed': return { text: `needs your ${name} sign-in`, needs: true };
    default: return { text: '', needs: false };
  }
}

/**
 * nextStep(err): what to do about a refusal of the guided sign-in or a
 * paste, after the server's own words — '' when those say it all.
 */
export function nextStep(err) {
  switch (err?.status) {
    case 0: return '';
    case 409: return '';
    case 429: return 'Wait a minute, then start again.';
    case 502: return 'Start again; if it keeps failing, paste a key instead or sign in on the tile in a terminal.';
    case 503: return 'Ask a workspace admin to unseal the vault.';
    case 504: return 'Start again.';
    default: return '';
  }
}
