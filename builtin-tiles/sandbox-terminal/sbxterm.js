// sbxterm.js — what the sandbox-terminal tile's page (index.html) and its
// native view (native.js) show (D121), as pure functions of what the backend
// answers (GET /me, /sandboxes, /keys, /sessions — API.md) and what a manager
// answers the page itself (…/sbx/sandboxes/{id}/execs, docs/sandbox-manager.md):
// no DOM, no lit, no calls. hack/tile-js.test.mjs pins them.
//
// The page dials a manager's terminal itself, with its frame token, so the
// manager sees the verified person: a terminal is a <bx-terminal src> on
// …/sbx/sandboxes/{id}/tty (a new login shell) or …/execs/{eid}/tty (one
// already running — after a reload, from another browser). The native view
// can't: the app's `terminal` primitive dials only the tile's own routes
// (TERMINAL_GAP), so it lists, ends and says where to open them.

// The tile's glyph: a name in /vendor/bx-icons.js (the page draws <bx-icon>),
// the native vocabulary's too (D184).
export const ICON = 'box';

// What a sandbox's state is called (the contract's states); the transitional
// ones end in "…".
export const STATES = {
  creating: 'creating…', stopped: 'stopped', starting: 'starting…', running: 'running', stopping: 'stopping…',
  archiving: 'archiving…', archived: 'archived', thawing: 'thawing…', deleting: 'deleting…', error: 'error',
};
export const EGRESS = { none: 'no network', internet: 'internet', open: 'open network' };

// A terminal opens in a sandbox that runs or can start (a stopped one starts
// on it); an archived one is thawed in its manager first.
const TTY_STATES = new Set(['running', 'stopped', 'starting']);

// TERMINAL_GAP: why the native view opens no terminal (a D96 difference, as
// the agent template's model/features.js DIFFERENCES.native says it).
export const TERMINAL_GAP = 'The app\'s terminal reaches only this tile\'s own routes, and a sandbox\'s terminal is its manager\'s — relaying it through this tile\'s backend would make the person the manager checks an asserted one. Open terminals in a browser: this page there opens them, and attaches to the ones running.';

// keyOf: a sandbox's key across managers.
export const keyOf = (s) => `${s.provider}|${s.id}`;

// ago: how long ago, in a few characters.
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

// shortPath: a path that fits a line — its tail after "…/" when it is longer
// than n characters.
export function shortPath(p, n = 40) {
  const s = String(p || '');
  if (s.length <= n) return s;
  const parts = s.split('/');
  let out = parts.pop();
  while (parts.length && out.length + parts[parts.length - 1].length + 3 <= n) out = `${parts.pop()}/${out}`;
  return `…/${out}`;
}

// --- a manager's routes, as the page dials them -------------------------------------

// endpointOf: the page's endpoint for a manager (provider: <tile>[#inst]) in
// its `sandboxes` slot (xbin.iface('sandboxes').endpoints: {provider,
// instance?, url}), or null.
export function endpointOf(eps, provider) {
  return (eps || []).find((e) => e && e.url && (e.instance ? `${e.provider}#${e.instance}` : e.provider) === provider) || null;
}
const sbxURL = (ep, id, rest = '') => `${String(ep.url).replace(/\/+$/, '')}/sbx/sandboxes/${encodeURIComponent(id)}${rest}`;
// terminalSrc: a new terminal — the sandbox user's login shell, at cwd ('' = its workdir).
export const terminalSrc = (ep, id, cwd = '') => sbxURL(ep, id, '/tty') + (cwd ? `?cwd=${encodeURIComponent(cwd)}` : '');
// attachSrc: a terminal already running (a tty exec): its screen replays, then it goes on.
export const attachSrc = (ep, id, eid) => sbxURL(ep, id, `/execs/${encodeURIComponent(eid)}/tty`);
// execsURL: the sandbox's execs; execURL: one of them (DELETE ends it).
export const execsURL = (ep, id) => sbxURL(ep, id, '/execs');
export const execURL = (ep, id, eid) => sbxURL(ep, id, `/execs/${encodeURIComponent(eid)}`);

// runningTerminals: the running terminals among a sandbox's execs — tty
// execs labelled "terminal" (what the tty route starts: this page's, the
// agent's Open terminal, an SSH login with a terminal), oldest first.
export function runningTerminals(execs) {
  return (Array.isArray(execs) ? execs : [])
    .filter((e) => e && e.id && e.tty && e.state === 'running' && (e.label || 'terminal') === 'terminal')
    .sort((a, b) => (a.started || 0) - (b.started || 0));
}

// --- SSH -----------------------------------------------------------------------------

// sshTarget: where people reach the SSH port — the address a manager of the
// tile set ("host", "host:port", "[v6]:port"): {host, port} (port 22 when
// it names none), or null when none is set.
export function sshTarget(ssh) {
  const a = String((ssh && ssh.address) || '').trim();
  if (!a) return null;
  const m = /^\[([^\]]+)\](?::(\d+))?$/.exec(a) || /^([^:]+)(?::(\d+))?$/.exec(a);
  if (!m) return { host: a, port: 22 };
  return { host: m[1], port: m[2] ? +m[2] : 22 };
}

// sshCommand: the command that logs in to the sandbox whose login this is,
// or '' while SSH isn't ready (sshState).
export function sshCommand(ssh, login) {
  const t = sshState(ssh).ready ? sshTarget(ssh) : null;
  if (!t || !login) return '';
  const host = t.host.includes(':') ? `[${t.host}]` : t.host;
  return `ssh ${login}@${host}${t.port === 22 ? '' : ` -p ${t.port}`}`;
}

// sshState: whether people can log in over SSH, and what to say when they
// can't: the server isn't running, or the port isn't published yet — the
// tile can't see xbind's port binding, so "published" is a manager of the
// tile having set the address people use (PUT /settings). expose: the
// command an admin runs to publish it.
export function sshState(ssh, { self = 'apps/sandbox-terminal', manager = false } = {}) {
  const port = (ssh && ssh.port) || 2222;
  const expose = `bx expose ${self} ssh=runtime --listen :${port}`;
  if (!ssh) return { ready: false, tone: 'muted', title: 'SSH', text: 'loading…', expose };
  if (!ssh.listening) {
    return { ready: false, tone: 'danger', title: 'The SSH server isn\'t running', text: ssh.error || 'it hasn\'t started yet', expose };
  }
  if (!sshTarget(ssh)) {
    return { ready: false, tone: 'info', title: 'SSH isn\'t published yet', expose,
      text: `An admin publishes the port (below), then ${manager ? 'you set' : 'a manager of this tile sets'} the address people use — until then there is no ssh command to show.` };
  }
  const t = sshTarget(ssh);
  return { ready: true, tone: 'ok', title: 'SSH', expose,
    text: `Log in with a key you registered here: ssh <sandbox>@${t.host.includes(':') ? `[${t.host}]` : t.host}${t.port === 22 ? '' : ` -p ${t.port}`}` };
}

// knownHosts: the host key as a known_hosts line for the address ('' = no
// address or no key yet).
export function knownHosts(ssh) {
  const t = sshTarget(ssh);
  const k = ssh && ssh.hostKey && ssh.hostKey.publicKey;
  if (!t || !k) return '';
  return `${t.port === 22 ? t.host : `[${t.host}]:${t.port}`} ${k}`;
}

// keyCheck: what is wrong with a pasted public key before it is sent ('' =
// nothing the page can tell; the backend checks the rest).
export function keyCheck(text) {
  const t = String(text || '').trim();
  if (!t) return 'Paste a public key — the contents of ~/.ssh/id_ed25519.pub, say.';
  if (/PRIVATE KEY/.test(t)) return 'That is a PRIVATE key — never paste it anywhere. Paste the .pub file next to it.';
  if (t.split(/\r?\n/).filter((l) => l.trim()).length > 1) return 'One key at a time: a single line.';
  if (!/^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-\S+|sk-\S+)\s+\S+/.test(t)) return 'That doesn\'t look like an OpenSSH public key (ssh-ed25519 AAAA… comment).';
  return '';
}

// keyRows: registered keys as a list shows them. inactive: when xbind last
// said the key's person may no longer use this tile ('' = active) — the key
// logs nobody in until their access is back (the backend marks it; D121).
export function keyRows(keys, now = Date.now()) {
  return (keys || []).map((k) => ({
    id: k.id, user: k.user || '', name: k.name || k.type || 'key', type: k.type || '', fingerprint: k.fingerprint || '',
    added: ago(k.added, now), lastUsed: k.lastUsed ? ago(k.lastUsed, now) : 'never used',
    inactive: k.inactive ? `inactive — access gone ${ago(k.inactive, now) || 'a while ago'}` : '',
  }));
}

// --- the list --------------------------------------------------------------------------

// openWhy: why a terminal can't open onto s here ('' = it can).
function openWhy(s, ep, me) {
  if (me && me.viewedBy) return 'viewing as someone opens no terminals';
  if (!s.tty) return 'its manager offers no terminals';
  if (!ep) return 'this page is not bound to its manager — reload it';
  if (!TTY_STATES.has(s.state || '')) return `it is ${STATES[s.state] || s.state || 'not ready'}${s.state === 'archived' ? ' — thaw it in its manager' : ''}`;
  return '';
}

// groups: GET /sandboxes as a list shows it — a group per bound manager (in
// the binding's order, with its error when it didn't answer), a row per
// sandbox: what it is, who it is for, its ssh command, whether a terminal
// can open here (and why not), and the terminals running in it (execs:
// {key: [exec…]} as the managers listed them; tabs: the terminals open on
// this page, to mark the ones here).
export function groups(list, { eps = null, me = null, execs = {}, tabs = [], now = Date.now() } = {}) {
  const L = list || {};
  const managers = Array.isArray(L.managers) ? L.managers : [];
  const all = Array.isArray(L.sandboxes) ? L.sandboxes : [];
  const user = (me && me.user) || '';
  const openHere = new Set((tabs || []).filter((t) => t.session && !t.ended).map((t) => `${t.key}|${t.session}`));
  return managers.map((m) => ({
    provider: m.provider, title: m.title || m.provider, error: m.error || '', tty: !!m.tty,
    rows: all.filter((s) => s.provider === m.provider).map((s) => {
      const ep = endpointOf(eps, s.provider);
      const key = keyOf(s);
      const why = openWhy(s, ep, me);
      const owner = s.owner ? (s.owner === user ? 'you' : s.owner) : '';
      const facts = [s.visibility === 'team' ? 'team' : 'private', owner && `owner: ${owner}`, s.via && `from ${s.via}`,
        s.image, EGRESS[s.egress] || s.egress].filter(Boolean).join(' · ');
      return {
        key, provider: s.provider, id: s.id, name: s.name || s.id, login: s.login || s.id, state: s.state || '',
        stateLabel: STATES[s.state] || s.state || '?', busy: /…$/.test(STATES[s.state] || ''),
        tone: s.state === 'running' ? 'ok' : s.state === 'error' ? 'danger' : 'muted',
        facts, workdir: s.workdir || '', visibility: s.visibility === 'team' ? 'team' : 'private', owner,
        ssh: '', // sshCommand, filled by rowsWithSSH
        open: { ok: !why, why },
        running: runningTerminals(execs[key]).map((e, i) => ({
          id: e.id, n: i + 1, cwd: e.cwd || '', started: e.started || 0, here: openHere.has(`${key}|${e.id}`),
          label: [`started ${ago(e.started, now) || 'a while ago'}`, shortPath(e.cwd)].filter(Boolean).join(' · '),
        })),
      };
    }),
  }));
}

// withSSH: the groups with each row's ssh command (while SSH is ready).
export function withSSH(gs, ssh) {
  return gs.map((g) => ({ ...g, rows: g.rows.map((r) => ({ ...r, ssh: sshCommand(ssh, r.login) })) }));
}

// emptyWhy: what an empty list says ({title, text, cmd?}), or null when
// there is something to show. self: this tile's path.
export function emptyWhy(list, self = 'apps/sandbox-terminal') {
  const L = list || {};
  const ms = Array.isArray(L.managers) ? L.managers : [];
  if ((L.sandboxes || []).length) return null;
  if (!ms.length) {
    return { title: 'Not bound to a sandbox manager yet', cmd: `bx bind ${self} sandboxes=<manager>`,
      text: 'Sandboxes come from sandbox managers (the coding-sandbox template, or any tile speaking the sandbox-manager contract). An admin binds this tile to one or more:' };
  }
  if (ms.every((m) => m.error)) return { title: 'No sandbox manager answered', text: 'Try again in a moment; each manager\'s error is shown above.' };
  return { title: 'No sandboxes here for you yet',
    text: `A sandbox shows up here once its owner shares it with this tile (${self}): in the agent, Sandboxes → “Share with a terminal tile…”, or from the sandbox manager's own page. You see the ones the share names you in and that you may use — yours, ones you are a member of, and team ones.` };
}

// tabTitle: a terminal tab's name — its sandbox's, numbered when several
// tabs are on one sandbox.
export function tabTitle(tabs, tab) {
  const same = (tabs || []).filter((t) => t.key === tab.key);
  return same.length > 1 ? `${tab.name} ${same.indexOf(tab) + 1}` : tab.name;
}

// browserURL: where "open in the browser" goes — the workspace the app
// reached (its xbin-ws-origin meta: ws(s)://host), else this document's
// origin when it is a web one; '' when neither says.
export function browserURL(wsOrigin, href) {
  const w = /^(wss?):\/\/([^/]+)/.exec(String(wsOrigin || ''));
  if (w) return `${w[1] === 'wss' ? 'https' : 'http'}://${w[2]}/`;
  try {
    const u = new URL(String(href || ''));
    if (u.protocol === 'https:' || u.protocol === 'http:') return `${u.origin}/`;
  } catch { /* none */ }
  return '';
}
