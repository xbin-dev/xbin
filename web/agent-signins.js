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
  constructor(status, message, data = null) { super(message); this.status = status; this.data = data; } // data: the refusal's body
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
  if (!res.ok) throw new SigninsError(res.status, (data && data.error) || `the request failed (${res.status})`, data);
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
    setTrustPolicy: (trust) => c('PUT', '/policy', { trust }),
    trust: (tile, basis) => c('POST', '/trust', { tile, basis }), // basis: the question's own (here.trust.basis) — a tile that changed since is 409 {changed, trust}
    untrust: (tid) => c('DELETE', `/trust/${id(tid)}`),
    forgetHome: (hid) => c('DELETE', `/homes/${id(hid)}`), // "forget what this tile wrote to my home" (D245) → {ok, tile, ended}
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
  if (sn.trust && sn.source !== 'saved') { // it waits for "I trust this tile's source": said as that, not as a fault
    return { text: 'saved sign-in waits for your trust', title: left.trim(), tone: 'warn' };
  }
  switch (sn.source) {
    case 'saved':
      // (a session the workspace agent started names none: it was ticked, and named, on that start's approval)
      return sn.name ? { text: `your saved sign-in: ${sn.name}`, title: `${provider} runs on the sign-in you saved with xbind (${sn.name}).`, tone: '' }
        : { text: 'your saved sign-in', title: `${provider} runs on the saved sign-in you ticked for this start.`, tone: '' };
    case 'home':
      return { text: "this tile's own login", title: `${provider} runs on the login it has in your home on this workspace.${left}`, tone: sn.why ? 'warn' : '' };
    case 'connector':
      return sn.why ? { text: 'saved sign-in not used', title: left.trim(), tone: 'warn' } : null; // the connector's own chip says the rest
    default:
      return sn.why ? { text: 'saved sign-in not used', title: left.trim(), tone: 'warn' } : null;
  }
}

/** namesWords(names, more): "bob", "bob and carol", "bob, carol and dave", "bob, carol, dave and 2 more". */
export function namesWords(names = [], more = 0) {
  const all = more > 0 ? [...names, `${more} more`] : [...names];
  if (all.length <= 1) return all[0] || '';
  return `${all.slice(0, -1).join(', ')} and ${all[all.length - 1]}`;
}

/**
 * trustWords(trust, harness, without, all): the words of "I trust this
 * tile's source" (D230) — xbind's question (a row's `trust`: {tile, name?,
 * basis, ownerKind, owner?, ownerName?, admin?, editors?, names?, past?,
 * pastUntil?, means, allowed, forbidden?}), for the tile's card and for the trust line of a start's
 * approval. People are named by their ACCOUNT ID — what xbind enforces —
 * with the display name beside it, never instead: a display name is its
 * bearer's to set.
 *
 *   title    "Use your Claude Code sign-in on this tile?"
 *   whose    whose tile it is, who can change it, and who could before
 *            and can't now (`past`, until `pastUntil`: what they left in
 *            it is still there — D245): three of each and "N more" —
 *            everyone with `all` (the person must be able to see everyone
 *            they trust: more > 0 means "show all" first)
 *   more     how many are not shown
 *   risk     what trusting means, in two sentences — two things: the
 *            sign-in is used here, and the tile's code can change the
 *            person's home (D245)
 *   trust, without   the two choices — of equal weight
 *   then     what the agent runs on without the sign-in ('own' |
 *            'connector' | anything else: signed out)
 *   line     the approval's trust line; unticked what leaving it means
 *   forbidden  why it can't be trusted here ('' when it can)
 */
export function trustWords(trust, harness = 'coding agent', without = '', all = false) {
  const t = trust || {};
  const beside = (id, name) => (name && name !== id ? `${id} (${name})` : `${id}`);
  const owner = beside(t.owner || '', t.ownerName);
  const whose = t.ownerKind === 'you' ? 'This tile is yours.' : t.ownerKind === 'person' ? `This tile belongs to ${owner}.`
    : t.ownerKind === 'org' ? `This tile belongs to the org ${owner}.` : t.admin ? 'This tile belongs to the workspace, which you administer.' : 'This tile belongs to the workspace.';
  const everyone = (t.editors || []).map((id) => beside(id, (t.names || {})[id]));
  const shown = all ? everyone : everyone.slice(0, 3);
  const eds = namesWords(shown, everyone.length - shown.length);
  const who = eds ? `${eds} can ${t.ownerKind === 'you' || t.admin ? '' : 'also '}change it.` : '';
  const before = (t.past || []).map((id) => beside(id, (t.names || {})[id]));
  const beforeShown = all ? before : before.slice(0, 3);
  const until = t.pastUntil ? ` until ${new Date(t.pastUntil).toISOString().slice(0, 10)}` : ' before';
  const was = before.length ? `${namesWords(beforeShown, before.length - beforeShown.length)} could change this tile's code${until}; trusting it means you trust what is in it.` : '';
  return {
    title: `Use your ${harness} sign-in on this tile?`,
    whose: [whose, who, was].filter(Boolean).join(' '),
    more: everyone.length - shown.length + before.length - beforeShown.length,
    showAll: `Show all ${everyone.length + before.length}`,
    risk: `Trusting means your ${harness} sign-in is used here and this tile's code can change your home. People who can change this tile could read it and use your ${harness} account.`,
    trust: 'Trust this tile — use my sign-in',
    without: 'Start without my sign-in',
    then: without === 'own' ? "Without it, the agent uses this tile's own login." : without === 'connector' ? "Without it, the agent runs on the workspace's model."
      : 'Without it, the agent starts signed out — you can sign in on this tile.',
    line: "I trust this tile's source",
    unticked: 'Tick both, or the session starts without your sign-in.',
    forbidden: t.allowed === false ? `${t.forbidden || "Trusting a tile's source is switched off for this workspace"}.` : '',
  };
}

/** CONFINED: what an admin is told about a session on a tile's copy of their home (D245): it is a member's. */
export const CONFINED = "This session is confined like a member's until you trust this tile: it sees the workspace read-only through the tiles you may read, and can't mount or open new namespaces.";

/** HOME_DOCS: where the home rule is explained (D245). */
export const HOME_DOCS = '/docs/agent-signins.md#your-home-on-a-tile-that-isnt-only-yours';

/** fmtBytes(n): "12 KB", "3.4 MB" — '' for an unknown size. */
export function fmtBytes(n) {
  if (!(n >= 0)) return '';
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v >= 10 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
}

/**
 * homeWords(home): the words for which home a session mounts (D245) — a
 * session row's `home`, or GET /agent/signins?tile='s ({mode: own | copy,
 * why?, kept?, confined?, unsandboxed?, trust?, docs, copy?}). On a tile whose source
 * isn't only the person's and that they have not trusted, their sessions
 * read their real home and write to a copy kept for that tile.
 *
 *   text     "home: yours" | "home: this tile's copy"
 *   long     …with "(changes stay on this tile)"
 *   title    the tooltip: why, in a sentence or two
 *   tone     '' | 'warn' (the copy; a workspace without sandboxes)
 *   docs     the link to the explanation
 *   about    the dialog's paragraphs, for the copy: what it is, why, what
 *            trusting means (two things), and that nothing is copied over
 *   trust    the button that trusts the tile ('' when the person can't)
 *   forget   "Forget what this tile wrote to my home"
 *   trusted  what to say once they trusted it
 */
export function homeWords(home) {
  const h = home || {};
  const docs = h.docs || HOME_DOCS;
  if (h.mode !== 'copy') {
    return {
      text: 'home: yours', long: 'home: yours', tone: h.unsandboxed ? 'warn' : '', docs,
      title: h.unsandboxed ? 'This workspace runs without sandboxes: every session uses your real home, and nothing protects it from a tile\'s code.'
        : 'This session has your real home: what you write under ~ is in your home on every tile.',
      about: [], trust: '', forget: '', trusted: '',
    };
  }
  const t = h.trust;
  const whose = t ? trustWords(t, '', '', true).whose : '';
  const about = [
    'Your home on this tile is a copy: this session reads everything in your real home, and what it writes under ~ stays on this tile. Your real home is not changed.',
    `Why: ${h.kept ? 'this session started on this tile\'s copy of your home and stays on it — a new session is judged anew' : (h.why || 'this tile isn\'t only yours, and you haven\'t said you trust its source')}. Code in a tile that isn't only yours could otherwise leave a file in your home — a shell profile, a git setting, a coding agent's hook — that runs later on your own tiles.`,
  ];
  if (whose) about.push(whose);
  // an admin's (the root token's) session on the copy: it has a member's sandbox, not an admin's
  if (h.confined) about.push(CONFINED);
  if (t && t.allowed) {
    about.push("Trusting this tile's source means two things: xbin uses your saved coding-agent sign-in here, and this tile's code can change your home. New sessions on this tile then have your real home; this one stays on the copy. Nothing this tile wrote to its copy is copied into your home.");
  } else if (t) {
    about.push(`${t.forbidden || "Trusting a tile's source is switched off for this workspace"}.`);
  }
  return {
    text: "home: this tile's copy", long: "home: this tile's copy (changes stay on this tile)", tone: 'warn', docs,
    title: `What you write under ~ here stays on this tile; your real home is read, not changed. ${h.why ? `${h.why}.` : ''}${h.confined ? ` ${CONFINED}` : ''}`.trim(),
    about,
    trust: t && t.allowed ? "I trust this tile's source" : '',
    forget: 'Forget what this tile wrote to my home',
    trusted: 'Trusted. New sessions on this tile have your real home; the ones already open stay on the copy until you start them again.',
  };
}

/**
 * homeCopyWords(copy): one row of the list of copies (GET /agent/signins →
 * homes: {id, tile, name?, created, bytes, sessions, gone?, unused}).
 * `gone`: the tile it was kept for is not there any more — deleted (xbind
 * removes the copy at its next sweep) or made again at its path, as after
 * a restore from a backup (kept until the person forgets it: xbind never
 * deletes on a doubt).
 */
export function homeCopyWords(copy) {
  const c = copy || {};
  const size = fmtBytes(c.bytes);
  const state = c.gone ? 'its tile was deleted or made again — this copy is not used any more'
    : c.unused ? 'kept, not used while you trust this tile'
      : c.sessions > 0 ? `in use by ${c.sessions} session${c.sessions === 1 ? '' : 's'}` : 'used by your sessions on this tile';
  return {
    what: `What ${c.tile || 'this tile'} wrote to its copy of your home${size ? ` — ${size}` : ''}`,
    state,
    forget: 'Forget what this tile wrote to my home',
    confirm: `Delete what ${c.tile || 'this tile'} wrote to its copy of your home${size ? ` (${size})` : ''}? Your real home is not touched.${c.sessions > 0 ? ` Your ${c.sessions} session${c.sessions === 1 ? '' : 's'} on it will be ended.` : ''}`,
  };
}

/**
 * offerWords(signin): a start's "use my sign-in" line on xbin's approval
 * (sessionRows' signin: {offer, name, without}) — a session the workspace
 * agent starts never has the person's saved sign-in unless they tick this,
 * each time:
 *
 *   runs    what the session runs on as it stands (without the sign-in)
 *   line    the line to tick (the sign-in's label is drawn after it)
 *   warn    why it is a warned line, in one sentence
 */
export function offerWords(signin) {
  const name = (signin && signin.name) || 'coding agent';
  const w = signin && signin.without;
  return {
    runs: w === 'connector' ? "Runs on the workspace's model." : w === 'own' ? `Runs on this tile's own ${name} login.`
      : w === 'needed' ? `Starts signed out: ${name} isn't served by the workspace's model here.` : "Runs without your saved sign-in.",
    line: `Use my ${name} sign-in for this session`,
    warn: 'Your workspace agent directs this session: what it tells the coding agent could expose your sign-in.',
  };
}

/**
 * savedHint(prov): the launcher's words for a provider row's `saved` (GET
 * /agent/providers): what a session started now would sign in with — ''
 * when the person has no saved sign-in for it.
 */
export function savedHint(prov) {
  const s = prov?.saved;
  if (!s) return '';
  if (s.inject) return `your saved sign-in: ${s.name}`;
  return s.trust ? `saved sign-in ${s.name} — xbin asks first whether you trust this tile's source` : `saved sign-in ${s.name} not used here — ${s.why}`;
}

/**
 * startSignInWords(h): a brief's or a plan line's words for one harness of
 * GET /delegate/start-defaults ({name, signIn}) — {text, needs}
 * where needs is true when the person has to sign in first.
 */
export function startSignInWords(h) {
  const name = h?.name || 'the coding agent';
  switch (h?.signIn) {
    case 'saved': return { text: h.needsTicket === false ? `your saved ${name} sign-in` : `your saved ${name} sign-in, once you approve the start`, needs: false };
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
