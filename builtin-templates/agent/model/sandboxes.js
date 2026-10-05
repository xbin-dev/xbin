// model/sandboxes.js — coding sandboxes (D115) as both views show them: the
// composer's sandbox picker (the open conversation's, or the next new
// chat's — only where the class has the sandbox toolset), the sandbox badge (the
// active sandbox and its working directory, and why a binding no longer
// resolves), the Sandboxes dialog's rows (state, manager, egress, owner, the
// actions your rights allow), a terminal onto one (its manager's `tty`, the
// web view only) and the create form (a manager's images, sizes and egress,
// as the class allows them), and sharing one with a terminal tile (the
// builtin sandbox-terminal, D121). Pure functions of GET /sandboxes, a
// class (GET /classes, or a run view's `class`) and a run's view: no lit, no
// DOM, no calls — model/sandbox-store.js keeps the state and makes the calls
// (API.md "Coding sandboxes").

// A sandbox's glyph (bx-icons `box`, the app's `box`): the web view draws
// it before a sandbox's name where a string is rich (the badge, a terminal
// tab, a coding agent's card); the strings carry the words alone (D184
// §1.6), so an <option>, a title or the app's subtitle reads without it.
export const GLYPH = 'box';
// The text glyph the labels began with before D184, kept for an instance's
// own code that imports it; the template's views never draw it.
// theme-ok: an export kept for compatibility, never drawn (D184)
export const ICON = '▣';

// What a sandbox's state is called; the transitional ones end in "…".
export const STATES = {
  creating: 'creating…', stopped: 'stopped', starting: 'starting…', running: 'running', stopping: 'stopping…',
  archiving: 'archiving…', archived: 'archived', thawing: 'thawing…', deleting: 'deleting…', error: 'error',
};

// What a sandbox may reach (docs/sandbox-manager.md), in words.
export const EGRESS = { none: 'no network', internet: 'internet', open: 'open network' };

const one = (s, n = 60) => {
  const t = String(s ?? '').replace(/\s+/g, ' ').trim();
  return t.length > n ? t.slice(0, n - 1) + '…' : t;
};

// listOf: GET /sandboxes as the views keep it ({sandboxes, managers}); loaded
// is false until it has been read.
export function listOf(resp) {
  return {
    sandboxes: resp && Array.isArray(resp.sandboxes) ? resp.sandboxes : [],
    managers: resp && Array.isArray(resp.managers) ? resp.managers : [],
    loaded: !!resp,
  };
}

// A reference is <provider>[#inst]|<id>: the last | splits it.
export function splitRef(ref) {
  const s = String(ref || '');
  const i = s.lastIndexOf('|');
  return i > 0 ? { provider: s.slice(0, i), id: s.slice(i + 1) } : { provider: '', id: s };
}

export const hasSandbox = (cls) => !!(cls && (cls.toolsets || []).includes('sandbox'));
const clsName = (cls) => (cls && (cls.name || cls.id)) || 'this';
const tileOf = (p) => String(p || '').split('#')[0];
const managerAllowed = (set, provider) => set == null || set === 'all'
  || (Array.isArray(set) && (set.includes(provider) || set.includes(tileOf(provider))));
const egressOf = (cls) => (cls && Array.isArray(cls.sandboxEgress) && cls.sandboxEgress.length ? cls.sandboxEgress : ['none']);

// firewallEgress: a sandbox's network as the class check counts it
// (_backend effectiveEgress): the less restrictive of what it has now
// (egress) and what it takes at its next start (egressNext) — a missing or
// unknown one is open.
const rank = (e) => (e === 'none' ? 0 : e === 'internet' ? 1 : 2);
export function firewallEgress(s) {
  let e = (s && s.egress) || '';
  const next = (s && s.egressNext) || '';
  if (next && rank(next) > rank(e)) e = next;
  return rank(e) === 2 ? 'open' : e;
}

// egressWords: a sandbox's network in words, with one waiting for its next start.
export function egressWords(s) {
  const e = (s && s.egress) || '';
  const next = (s && s.egressNext) || '';
  const w = EGRESS[e] || e;
  return next && next !== e ? `${w || '?'} → ${EGRESS[next] || next} at the next start` : w;
}

// projectSandbox(sb, managers): a project's new sandbox as both views make
// it (the new-project form, Work on this): the manager picked, else the
// first usable one; what the form names, else the manager's default image
// and size; and internet when the manager offers it — a project clones and
// fetches, and a sandbox created without an egress has no network
// (docs/sandbox-manager.md). `new` is the body's sandbox.new; m, usable and
// egressOpts are for a form to draw.
export function projectSandbox(sb, managers) {
  const usable = (managers || []).filter((m) => m.ok !== false);
  const m = usable.find((x) => x.provider === (sb && sb.provider)) || usable[0] || null;
  const egressOpts = m && m.egress && m.egress.length ? m.egress : ['none'];
  const out = {
    ...sb,
    provider: m ? m.provider : '',
    image: (sb && sb.image) || ((m && m.images) || []).find((i) => i.default)?.id || '',
    size: (sb && sb.size) || ((m && m.sizes) || []).find((z) => z.default)?.id || '',
    egress: sb && sb.egress && egressOpts.includes(sb.egress) ? sb.egress : egressOpts.includes('internet') ? 'internet' : egressOpts[0],
    m, usable, egressOpts,
  };
  out.new = out.provider ? { provider: out.provider, ...(out.image ? { image: out.image } : {}), ...(out.size ? { size: out.size } : {}), egress: out.egress } : null;
  return out;
}

// A sandbox a conversation with internal reach has worked in carries this
// label; a class that reaches outside with no internal reach may not use it
// (_backend taintRefusal).
export const INTERNAL_LABEL = 'xbin.agent/internal';
const reachesOut = (cls) => !!cls && ((cls.toolsets || []).includes('web') || (hasSandbox(cls) && egressOf(cls).some((e) => e !== 'none')));
export function taintWhy(cls, s) {
  if (!s || !s.labels || !s.labels[INTERNAL_LABEL] || !reachesOut(cls) || (cls.toolsets || []).includes('internal')) return '';
  return `it has held data from an internal-reach conversation — the ${clsName(cls)} class reaches outside`;
}

// classAllows: '' when a conversation of cls may bind a sandbox of provider
// with egress, else why not (as _backend/sandbox_bind.go sandboxClassAllows
// says it). An empty provider or egress skips that check.
export function classAllows(cls, provider, egress, manager = '') {
  if (!hasSandbox(cls)) return `the ${clsName(cls)} class has no coding sandbox`;
  if (provider && !managerAllowed(cls.managers, provider)) return `the ${clsName(cls)} class doesn't allow sandboxes from ${manager || provider}`;
  if (egress && !egressOf(cls).includes(egress)) return `the ${clsName(cls)} class doesn't allow a sandbox with ${EGRESS[egress] || egress}`;
  return '';
}

// bindingOf: the conversation's active sandbox (its config's binding), or null.
export const bindingOf = (v) => (v && v.config && v.config.sandbox && v.config.sandbox.ref ? v.config.sandbox : null);

// attachedOf: every sandbox the conversation has attached, the active one
// among them (as its binding says it: the newest cwd).
export function attachedOf(v) {
  const b = bindingOf(v);
  const list = ((v && v.config && v.config.attached) || []).filter((a) => a && a.ref).map((a) => (b && a.ref === b.ref ? b : a));
  return b && !list.some((a) => a.ref === b.ref) ? [b, ...list] : list;
}

const rootOf = (v) => (v && v.run ? v.run.rootId || v.run.id : null);
const talks = (v) => !v || v.access !== 'viewer';
// the open conversation holds ref: attached, or bound there as the list says
const heldHere = (v, s) => !!v && (attachedOf(v).some((a) => a.ref === s.ref) || (s.boundTo || []).includes(rootOf(v)));

// sharedConv: the conversation has other people in it — shared with the
// team, or with people (its view's acl; a run that says its visibility).
export function sharedConv(v) {
  if (!v) return false;
  const acl = v.acl || {};
  return (acl.visibility || (v.run && v.run.visibility)) === 'team' || (acl.members || []).length > 0;
}

// VIEW_ONLY: why someone who may only read a conversation can't make or bind
// a sandbox for it.
export const VIEW_ONLY = 'you may only read this conversation — someone who may talk in it can make it a sandbox';
// sentence: a reason ("why …" above) said on its own.
export const sentence = (why) => (why ? why[0].toUpperCase() + why.slice(1) + (/[.!?]$/.test(why) ? '' : '.') : '');

// createWhy: why New sandbox can't be offered here ('' = it can): the open
// conversation is view-only for you (a sandbox made while it is open is made
// for it), or no manager is available (list: GET /sandboxes; null skips it).
export function createWhy(list, conv) {
  if (conv && !talks(conv)) return VIEW_ONLY;
  if (!list) return '';
  const ms = list.managers || [];
  if (ms.some((m) => m.ok !== false)) return '';
  return !list.loaded ? 'the sandboxes are still being read' : ms.length ? 'no sandbox manager is available right now' : 'no sandbox manager is bound';
}

// bindConfirm: what to confirm before ref becomes the open conversation's
// sandbox ('' = nothing): a private one that isn't there yet, in a
// conversation other people are in — they will be able to work in it.
export function bindConfirm(list, conv, ref) {
  if (!conv || !ref || !sharedConv(conv)) return '';
  const s = find(list, ref);
  if (!s || s.visibility === 'team' || heldHere(conv, s)) return '';
  return `“${nameOf(s)}” is private — people in this conversation will be able to work in it. Use it here?`;
}

// askRefusal: why POST /ask refused the sandbox it named ('' = the refusal
// was not about the sandbox): a manager's refusal (e.refusal: the sandbox is
// gone, not allowed, or its manager unbound or down), or the backend's own
// (the class doesn't allow it, you may not use it, a bad cwd).
const BIND_REFUSED = /^sandbox\.(ref|cwd):|has no sandbox toolset|doesn't allow (sandboxes from|a sandbox with)|may not use this sandbox|this sandbox offers no|has held data from an internal-reach|must be marked as holding internal data/;
export function askRefusal(e) {
  if (!e || !e.status) return '';
  if (e.refusal) return [403, 404, 410].includes(e.status) || e.status >= 500 ? e.message : '';
  return [400, 403, 409].includes(e.status) && BIND_REFUSED.test(e.message || '') ? e.message : '';
}
const find = (list, ref) => ((list && list.sandboxes) || []).find((s) => s.ref === ref) || null;
const nameOf = (s) => (s && (s.name || splitRef(s.ref).id)) || '';
const recent = (a, b) => (b.lastActive || 0) - (a.lastActive || 0) || nameOf(a).localeCompare(nameOf(b));

// brokenWhy: why a binding no longer resolves ('' = it does, as far as the
// list says): its class no longer allows it, its manager is gone or down, or
// its manager no longer has it.
export function brokenWhy(b, cls, list) {
  if (cls) {
    const s = find(list, b.ref);
    const provider = splitRef(b.ref).provider;
    const why = classAllows(cls, provider, b.egress, b.manager) || (s && classAllows(cls, provider, firewallEgress(s), b.manager))
      || taintWhy(cls, s);
    if (why) return `not allowed: ${why}`;
  }
  if (!list || !list.loaded) return '';
  const provider = splitRef(b.ref).provider;
  const m = list.managers.find((x) => x.provider === provider);
  if (!m) return `its manager (${b.manager || provider}) is no longer bound`;
  if (m.ok === false) return `its manager (${m.title || provider}) is unavailable${m.error ? ': ' + one(m.error, 120) : ''}`;
  if (!find(list, b.ref)) return 'gone — its manager no longer has it';
  return '';
}

const detailOf = (s) => [s.manager, STATES[s.state] || s.state, egressWords(s)].filter(Boolean).join(' · ');

// sandboxPicker: the composer's sandbox — the open conversation's (a pick
// binds it from its next turn) or, at home, the next new chat's
// (opts.pick {ref, cwd}; opts.cls the class for new chats). Shown only where
// the class has the sandbox toolset. Rows grouped This conversation (what it
// has attached) · Yours · Shared · Team; one you may not use, or the class
// does not allow, is there but disabled with the reason — as is one
// opts.fits(s) says why not (at home, a coding agent's: D147). `none`
// leaves the conversation without an active sandbox.
// notHomed: why a conversation of a person's partition can't work in row s
// — the backend's word (GET /sandboxes: `homed` false, `why`), sent only in
// a person's partition's list (the team's sandboxes there, API.md
// "Partitioned instances"); '' for any other row.
const notHomed = (s) => (s.homed === false ? String(s.why || 'not a sandbox of your own space') : '');

export function sandboxPicker(list, conv, me, opts = {}) {
  const cls = conv ? conv.class : opts.cls;
  const shown = hasSandbox(cls);
  const active = conv ? bindingOf(conv) : opts.pick && opts.pick.ref ? opts.pick : null;
  const value = active ? active.ref : '';
  const root = rootOf(conv);
  // making one active takes the right to use it — an attached one someone
  // else bound works on for everyone, but only they can make it active again
  const row = (s) => {
    const why = !s.canUse && s.ref !== value ? 'someone else bound it — you may not use it yourself'
      : classAllows(cls, s.provider || splitRef(s.ref).provider, firewallEgress(s), s.manager) || taintWhy(cls, s) || notHomed(s) || (opts.fits ? opts.fits(s) : '');
    return { value: s.ref, name: nameOf(s), label: `${nameOf(s)} · ${STATES[s.state] || s.state || '?'}`, detail: detailOf(s),
      state: s.state || '', egress: s.egress || '', on: s.ref === value, disabled: !!why, why };
  };
  const all = [...((list && list.sandboxes) || [])].sort(recent);
  const hereRefs = new Set(conv ? attachedOf(conv).map((b) => b.ref) : []);
  for (const s of all) if (root != null && (s.boundTo || []).includes(root)) hereRefs.add(s.ref);
  const here = [...hereRefs].map((ref) => {
    const s = find(list, ref);
    if (s) return row(s);
    const b = attachedOf(conv).find((x) => x.ref === ref) || { ref };
    const why = list && list.loaded ? brokenWhy(b, cls, list) || 'unavailable' : '';
    return { value: ref, name: b.name || splitRef(ref).id, label: `${b.name || splitRef(ref).id}${why ? ' · unavailable' : ''}`,
      detail: [b.manager, EGRESS[b.egress] || b.egress].filter(Boolean).join(' · '), state: '', egress: b.egress || '',
      on: ref === value, disabled: !!why && ref !== value, why };
  });
  const rest = all.filter((s) => !hereRefs.has(s.ref) && s.canUse);
  const groups = [
    { id: 'here', label: 'This conversation', rows: here },
    { id: 'mine', label: 'Yours', rows: rest.filter((s) => s.mine).map((s) => row(s)) },
    { id: 'shared', label: 'Shared', rows: rest.filter((s) => !s.mine && s.visibility !== 'team').map((s) => row(s)) },
    { id: 'team', label: 'Team', rows: rest.filter((s) => !s.mine && s.visibility === 'team').map((s) => row(s)) },
  ].filter((g) => g.rows.length);
  // a pick that is not listed still says itself — and, once the list is
  // read, why it can't be used (gone, its manager unbound or down, or listed
  // but no longer yours to use)
  let stale = '';
  if (value && !groups.some((g) => g.rows.some((r) => r.value === value))) {
    const b = active;
    const name = b.name || splitRef(value).id;
    stale = list && list.loaded ? brokenWhy(b, cls, list) || (find(list, value) ? 'you may no longer use it' : 'gone — its manager no longer has it') : '';
    groups.unshift({ id: 'picked', label: conv ? 'This conversation' : 'Picked', rows: [{ value, name,
      label: `${name}${stale ? ' · unavailable' : ''}`, detail: '', state: '', egress: b.egress || '', on: true, disabled: false, why: stale }] });
  }
  const managers = ((list && list.managers) || []).filter((m) => m.ok !== false && !classAllows(cls, m.provider, '', m.title));
  const talk = talks(conv);
  const newWhy = !talk ? VIEW_ONLY : !managers.length ? (list && list.loaded ? 'no sandbox manager this class allows is available' : '') : '';
  const notes = ((list && list.managers) || []).filter((m) => m.ok === false).map((m) => `${m.title || m.provider}: ${m.error || 'unavailable'}`);
  if (stale) notes.unshift(`${active.name || splitRef(value).id}: ${stale} — pick another`);
  return {
    shown, value, cls: cls || null,
    loading: !!list && !list.loaded,
    label: active ? active.name || splitRef(value).id : 'No sandbox',
    icon: GLYPH,
    title: conv ? 'the coding sandbox this conversation works in, from its next turn' : 'the coding sandbox your next new chat starts in',
    disabled: !talk,
    none: { value: '', label: 'No sandbox', on: !value },
    groups,
    actions: [
      { id: 'new', label: 'New sandbox…', disabled: !talk || !managers.length, why: newWhy },
      { id: 'manage', label: 'Manage sandboxes…', disabled: false, why: '' },
    ],
    stale, // why the pick can't be used ('' = it can, or the list isn't read yet)
    notes,
  };
}

// sandboxBadge: the open conversation's sandbox badge — its active sandbox and working
// directory, and why the binding no longer resolves (gone, its manager
// unbound or down, its class no longer allows it) with what to do (advice);
// every attached one for the popover (switch, detach). null when it has none.
// opts.fixed: the conversation keeps its sandbox — a coding agent's, named
// (D147 §2.2: the backend refuses a rebind, a detach and a new cwd) —
// so nothing changes it here (canChange false) and a broken one's advice
// is a new chat. talk: a participant, fixed or not (ports.js).
export function sandboxBadge(conv, list, now = Date.now(), { fixed = '' } = {}) {
  const b = bindingOf(conv);
  if (!b) return null;
  const cls = conv.class || null;
  const s = find(list, b.ref);
  const name = b.name || nameOf(s) || splitRef(b.ref).id;
  const broken = brokenWhy(b, cls, list);
  const state = s ? s.state || '' : '';
  const egress = (s && s.egress) || b.egress || '';
  const manager = (s && s.manager) || b.manager || splitRef(b.ref).provider;
  return {
    ref: b.ref, name, cwd: b.cwd || '', manager, egress, state,
    label: `${name}${b.cwd ? ' · ' + b.cwd : ''}`,
    icon: GLYPH, // drawn before the label (the web's badge)
    broken,
    detail: [manager, EGRESS[egress] || egress, STATES[state] || state].filter(Boolean).join(' · '),
    title: broken ? `${name}: ${broken}` : `works in ${name}${b.cwd ? ' at ' + b.cwd : ''} (${[manager, EGRESS[egress] || egress].filter(Boolean).join(', ')}) — ${fixed ? 'fixed for this conversation' : 'change it here'}`,
    canChange: !fixed && talks(conv),
    talk: talks(conv), // a participant (not a viewer): the popover's Ports (D135) — a coding agent's too
    fixed: !!fixed,
    advice: fixed ? `start a new chat with ${fixed} in another sandbox` : 'pick another, or detach it',
    attached: attachedOf(conv).map((a) => ({ ref: a.ref, name: a.name || nameOf(find(list, a.ref)) || splitRef(a.ref).id, cwd: a.cwd || '',
      on: a.ref === b.ref, broken: brokenWhy(a, cls, list) })),
    since: b.at ? ago(b.at, now) : '',
    by: b.by || '',
  };
}

// cwdCheck: what is wrong with a working directory ('' = nothing; '' = the
// sandbox's workdir).
export function cwdCheck(cwd) {
  const p = String(cwd || '').trim();
  if (!p) return '';
  if (!p.startsWith('/')) return 'The working directory is an absolute path (/work/…).';
  if (p.length > 4096) return 'That path is too long.';
  return '';
}

// ago: how long ago, in a few characters.
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

const sizeWords = (z) => (!z ? '' : [z.id, z.memMiB && `${+(z.memMiB / 1024).toFixed(1)} GiB`, z.vcpus && `${z.vcpus} vCPU`, z.diskGiB && `${z.diskGiB} GiB disk`]
  .filter(Boolean).join(' · '));
const capsOf = (s, list) => s.caps || ((list.managers.find((m) => m.provider === s.provider) || {}).caps) || [];

// viaConv: the conversation start/stop/thaw goes through for s (its root;
// undefined = as yourself): one the open conversation holds that you may
// neither use nor manage, while you may talk in it — the backend acts as
// the one who bound it there (?conversation=).
export function viaConv(s, conv) {
  return s && !(s.canUse || s.canManage) && talks(conv) && heldHere(conv, s) ? rootOf(conv) : undefined;
}

// --- terminals: a manager's `tty` (docs/sandbox-manager.md) --------------------------

// endpointOf: the page's endpoint for a manager (provider: <tile>[#inst]),
// from its `sandboxes` slot (xbin.iface('sandboxes').endpoints: {provider,
// instance?, url}), or null.
export function endpointOf(eps, provider) {
  return (eps || []).find((e) => e && e.url && (e.instance ? `${e.provider}#${e.instance}` : e.provider) === provider) || null;
}

// A terminal opens in a sandbox that runs or can start (a stopped one starts
// on it; an archived one is thawed first).
const TTY_STATES = new Set(['running', 'stopped', 'starting']);

// ttyQuery: ?cwd=&cmd= — each only when given.
const ttyQuery = (cwd, cmd) => {
  const q = [['cwd', String(cwd || '').trim()], ['cmd', String(cmd || '').trim()]].filter(([, v]) => v);
  return q.length ? '?' + q.map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join('&') : '';
};
// terminalSrc: the manager route that starts a terminal in sandbox id at cwd
// ('' = its workdir) running cmd ('' = the login shell) — what
// <bx-terminal src> dials.
export function terminalSrc(ep, id, cwd = '', cmd = '') {
  return `${String(ep.url).replace(/\/+$/, '')}/sbx/sandboxes/${encodeURIComponent(id)}/tty${ttyQuery(cwd, cmd)}`;
}
// RELAY: what a view passes to terminal() as its endpoints when it reaches a
// sandbox's terminal through this tile's own relay (D147 §4.2.8) — the
// native view: the app's terminal dials only the tile's own routes.
export const RELAY = 'relay';
// relaySrc: that relay for sandbox ref — GET /sandboxes/{ref}/terminal?cwd=&cmd=,
// tile-relative (under /api/<self>/).
export const relaySrc = (ref, cwd = '', cmd = '') =>
  `sandboxes/${String(ref).split('/').map(encodeURIComponent).join('/')}/terminal${ttyQuery(cwd, cmd)}`;
// execSrc: that manager's route for one exec of the sandbox (DELETE ends it).
export const execSrc = (ep, id, eid) => `${String(ep.url).replace(/\/+$/, '')}/sbx/sandboxes/${encodeURIComponent(id)}/execs/${encodeURIComponent(eid)}`;

// terminal: "Open terminal" for the sandbox ref at cwd, running cmd ('' =
// the login shell) — {shown, why, src, base (the manager's url; '' through
// the relay), relay, id, name, cwd, cmd, manager, label}. eps: the page's
// endpoints for its `sandboxes` slot, or RELAY (the tile's own relay); a view
// that draws no terminal passes none (null: not shown). Shown where its
// manager's hello offers `tty` (and the sandbox does not leave it out);
// offered ('' why) when the page is bound to that manager (or relays), you
// may use the sandbox yourself — the page dials the manager as you, so the
// manager applies its per-person rules (the relay checks the same), and
// acting through a conversation doesn't reach it — and it runs or can start.
export function terminal(list, ref, eps, cwd = '', cmd = '') {
  const L = list || listOf(null);
  const { provider, id } = splitRef(ref);
  const s = find(L, ref);
  const m = L.managers.find((x) => x.provider === ((s && s.provider) || provider));
  const tty = !!m && (m.caps || []).includes('tty') && !(s && Array.isArray(s.caps) && !s.caps.includes('tty'));
  const name = nameOf(s) || id;
  const relay = eps === RELAY;
  const out = { shown: !!eps && tty, why: '', src: '', base: '', relay, ref, id: (s && s.id) || id, name, cwd: String(cwd || '').trim(),
    cmd: String(cmd || '').trim(), manager: (s && s.manager) || (m && m.title) || provider, label: 'Open terminal' };
  if (!out.shown) return { ...out, why: !eps ? 'this view opens no terminals' : `its manager (${out.manager}) offers no terminals` };
  const ep = relay ? null : endpointOf(eps, (s && s.provider) || provider);
  const why = !s ? 'gone — its manager no longer has it'
    : m.ok === false ? `its manager (${out.manager}) is unavailable`
    : !ep && !relay ? 'this page is not bound to its manager — reload it'
    : !s.canUse ? 'you may not use it yourself'
    : !TTY_STATES.has(s.state || '') ? `it is ${STATES[s.state] || s.state || 'not ready'}${s.state === 'archived' ? ' — thaw it first' : ''}`
    : '';
  if (why) return { ...out, why };
  if (relay) return { ...out, src: relaySrc(out.ref, out.cwd, out.cmd) };
  return { ...out, src: terminalSrc(ep, out.id, out.cwd, out.cmd), base: ep.url };
}

// sandboxRows: the Sandboxes dialog — every sandbox you may see, yours
// first, then by when it was last active; each with the actions your rights
// allow: start/stop/thaw (who may use or manage it — or, for one the open
// conversation holds, anyone who may talk in it: through the conversation),
// archive (who may manage it, where its manager archives), share with the
// team or make private (its owner), share with a terminal tile (its owner,
// when this agent is its home: shareForm), delete (who may manage it —
// confirmed).
// opts.conv: the open conversation — "Use here" binds one (confirmed when a
// private one goes into a conversation other people are in); opts.cls at
// home: "Use for a new chat". opts.order: the refs in the order a view shows
// them — kept while it is open (a row never moves under the cursor), the
// ones it hasn't shown yet after them. opts.tty: the page's endpoints for
// its `sandboxes` slot — "Terminal" where terminal() offers one (at the
// working directory the open conversation has it at; else its workdir); a
// view without terminals passes none.
export function sandboxRows(list, me, opts = {}) {
  const conv = opts.conv || null;
  const cls = conv ? conv.class : opts.cls;
  const active = conv ? bindingOf(conv) : opts.pick || null;
  const root = rootOf(conv);
  const user = (me && me.user) || '';
  const L = list || listOf(null);
  const at = new Map((opts.order || []).map((ref, i) => [ref, i]));
  const pos = (s) => (at.has(s.ref) ? at.get(s.ref) : Infinity);
  return [...L.sandboxes].sort((a, b) => (pos(a) - pos(b)) || (!!b.mine - !!a.mine) || recent(a, b)).map((s) => {
    const st = s.state || '';
    const via = viaConv(s, conv);
    const use = !!(s.canUse || s.canManage) || via != null;
    const bound = (s.boundTo || []).length;
    const name = nameOf(s);
    const acts = [];
    const on = !!(active && active.ref === s.ref);
    if (hasSandbox(cls) && s.canUse && !on && talks(conv) && !classAllows(cls, s.provider || splitRef(s.ref).provider, firewallEgress(s))
      && !taintWhy(cls, s) && !notHomed(s)) {
      const ask = bindConfirm(L, conv, s.ref);
      acts.push({ id: 'use', label: conv ? 'Use here' : 'Use for a new chat', ...(ask ? { confirm: ask } : {}) });
    }
    const through = via != null ? { via: true } : {};
    if (use && st === 'stopped') acts.push({ id: 'start', label: 'Start', ...through });
    if (use && st === 'running') acts.push({ id: 'stop', label: 'Stop', ...through });
    if (use && st === 'archived') acts.push({ id: 'thaw', label: 'Thaw', ...through });
    if (s.canManage && (st === 'running' || st === 'stopped') && capsOf(s, L).includes('archive')) {
      acts.push({ id: 'archive', label: 'Archive', confirm: `Archive the sandbox “${name}”? It stops, and thawing it takes a while.` });
    }
    if (opts.tty) {
      const cwd = (conv && attachedOf(conv).find((a) => a.ref === s.ref) || {}).cwd || '';
      const t = terminal(L, s.ref, opts.tty, cwd);
      if (t.shown && !t.why) acts.push({ id: 'terminal', label: 'Terminal', cwd: t.cwd });
    }
    if (s.canEdit) acts.push(s.visibility === 'team' ? { id: 'private', label: 'Make private' } : { id: 'team', label: 'Share with the team' });
    if (canShareOut(s)) acts.push({ id: 'shareTerm', label: 'Share with a terminal tile…' });
    if (s.canManage) {
      acts.push({ id: 'delete', label: 'Delete', danger: true, confirm: `Delete the sandbox “${name}”? Everything in it is gone for good${bound
        ? ` — ${bound === 1 ? 'a conversation' : bound + ' conversations'} you see ${bound === 1 ? 'loses' : 'lose'} it` : ''}.` });
    }
    const owner = s.owner && s.owner.user;
    return {
      ref: s.ref, name, id: s.id || splitRef(s.ref).id, state: st, stateLabel: STATES[st] || st || '?',
      stateDetail: s.stateDetail || '', busy: /…$/.test(STATES[st] || ''),
      manager: s.manager || s.provider || '', egress: s.egress || '', egressLabel: egressWords(s),
      image: (s.image && (s.image.title || s.image.id)) || '', size: sizeWords(s.size),
      owner: s.mine || (owner && owner === user) ? 'you' : owner || 'the agent', mine: !!s.mine,
      visibility: s.visibility === 'team' ? 'team' : 'private', visLabel: s.visibility === 'team' ? 'team' : 'private',
      lastActive: s.lastActive || 0, lastLabel: ago(s.lastActive, opts.now),
      // the other consumers it is shared with (a terminal tile), by path
      sharedWith: sharesOf(s).map((x) => x.consumer),
      bound, here: root != null && (s.boundTo || []).includes(root), active: on,
      // where it is used, in words: the open conversation's active or attached
      // one — at home, the pick is the next new chat's
      where: on ? (conv ? 'active here' : 'next new chat') : root != null && (s.boundTo || []).includes(root) ? 'attached here' : '',
      actions: acts,
    };
  });
}

// --- sharing with a terminal tile (D121) ---------------------------------------------

// TERMINAL_TILE: where the builtin sandbox-terminal tile is imported by default.
export const TERMINAL_TILE = 'apps/sandbox-terminal';
const TILE_PATH = /^[A-Za-z0-9][A-Za-z0-9._-]*(\/[A-Za-z0-9][A-Za-z0-9._-]*)*$/;

// sharesOf: the consumers a sandbox is shared with, as its manager keeps
// them ([{consumer, users: "*" | [user…]}]).
export const sharesOf = (s) => (s && Array.isArray(s.shares) ? s.shares.filter((x) => x && x.consumer) : []);

// canShareOut: s is yours and this agent is its home — the contract lets
// only the home consumer change a sandbox's shares, so one another consumer
// shared with this agent is not this agent's to pass on.
export const canShareOut = (s) => !!(s && s.mine && s.canEdit && !s.shared);

const usersWords = (u, me) => (u === '*' ? 'everyone who may use it'
  : Array.isArray(u) ? u.map((x) => (x === me ? 'you' : x)).join(', ') || 'nobody' : '');

// shareForm: "Share with a terminal tile…" for sandbox s — the tile's path
// (f.tile; the builtin's default path), who the share is for (a team
// sandbox: "*", everyone who may use it; a private one: you, with whoever
// that tile's share already named), the shares it has now, what is wrong
// (error; '' = it can be shared), and the PATCH /sandboxes/{ref} body: its
// shares with that tile's replaced, and the version they were read at
// (withVersion). The terminal tile applies the person rules too (owner,
// members, team), so a share never widens who may use the sandbox. self:
// this agent's path (a share with itself is refused).
export function shareForm(s, me, f = {}, self = '') {
  const user = (me && me.user) || '';
  const tile = String(f.tile ?? TERMINAL_TILE).trim().replace(/^\/+|\/+$/g, '');
  const team = !!s && s.visibility === 'team';
  const current = sharesOf(s);
  const prior = current.find((x) => x.consumer === tile);
  const users = team || (prior && prior.users === '*') ? '*'
    : [...new Set([...(prior && Array.isArray(prior.users) ? prior.users : []), user])].filter(Boolean);
  let error = '';
  if (!s) error = 'gone — its manager no longer has it';
  else if (!canShareOut(s)) error = s.shared ? 'it was shared with this agent: only its home can share it on' : 'only its owner shares it';
  else if (!tile) error = 'Name the terminal tile: its path, like apps/sandbox-terminal.';
  else if (!TILE_PATH.test(tile) || tile.split('/').some((x) => x === '..' || x === '.')) error = 'A tile\'s path is like apps/sandbox-terminal.';
  else if (self && tile === self) error = 'That is this agent — name the terminal tile.';
  else if (!team && !user) error = 'Who you are isn\'t known yet — try again in a moment.';
  return {
    tile, users,
    usersLabel: team ? 'everyone who may use it (a team sandbox)' : usersWords(users, user),
    current: current.map((x) => ({ consumer: x.consumer, users: x.users, usersLabel: usersWords(x.users, user) })),
    error, ok: !error,
    body: withVersion(s, { shares: [...current.filter((x) => x.consumer !== tile), { consumer: tile, users }] }),
  };
}

// unshareBody: the PATCH /sandboxes/{ref} body that takes consumer's share
// away (with the version it was read at).
export const unshareBody = (s, consumer) => withVersion(s, { shares: sharesOf(s).filter((x) => x.consumer !== consumer) });

// withVersion: a PATCH body that replaces a whole list (the shares) goes
// with the sandbox's version as it was read, so a change made since — a
// share someone else added — is refused (412) rather than lost; the store
// reads it again and computes the body afresh (sandbox-store.js).
const withVersion = (s, body) => (s && Number.isInteger(s.version) ? { ...body, version: s.version } : body);

// --- the create form ---------------------------------------------------------------

const imgLabel = (i) => (i.title && i.title !== i.id ? `${i.id} — ${i.title}` : i.id);
const pickOf = (opts, want, dflt) => {
  const ok = opts.filter((o) => !o.disabled);
  if (ok.some((o) => o.value === want)) return want;
  if (dflt && ok.some((o) => o.value === dflt)) return dflt;
  return ok.length ? ok[0].value : '';
};

// createForm: the create form as a view draws it, from the managers (GET
// /sandboxes' `managers`), the class it is made for (the open conversation's,
// or the next new chat's; null: any) and what was entered so far (f). Each
// field's options — a manager's images, sizes and egress, the ones the class
// does not allow disabled — its values with the defaults filled in, and
// what is wrong (error; '' = it can be created). Call it again on every change.
export function createForm(managers, cls, f = {}, opts = {}) {
  const usable = (managers || []).filter((m) => m.ok !== false);
  const limit = cls && hasSandbox(cls) ? cls : null;
  const mopts = usable.map((m) => {
    const why = limit ? classAllows(limit, m.provider, '', m.title) : '';
    return { value: m.provider, label: m.title || m.provider, disabled: !!why, why };
  });
  const provider = pickOf(mopts, f.provider);
  const m = usable.find((x) => x.provider === provider) || null;
  const images = ((m && m.images) || []).map((i) => ({ value: i.id, label: imgLabel(i), disabled: false }));
  const sizes = ((m && m.sizes) || []).map((z) => ({ value: z.id, label: sizeWords(z), disabled: false }));
  const egress = ((m && m.egress && m.egress.length ? m.egress : ['none'])).map((e) => {
    const why = limit ? classAllows(limit, '', e) : '';
    return { value: e, label: EGRESS[e] || e, disabled: !!why, why };
  });
  const v = {
    provider,
    name: f.name ?? '',
    image: pickOf(images, f.image, ((m && m.images) || []).find((i) => i.default)?.id),
    size: pickOf(sizes, f.size, ((m && m.sizes) || []).find((z) => z.default)?.id),
    egress: pickOf(egress, f.egress, 'none'),
    visibility: f.visibility === 'team' || f.visibility === 'private' ? f.visibility : opts.team ? 'team' : 'private',
    cwd: f.cwd ?? '',
  };
  let error = '';
  if (!usable.length) error = (managers || []).length ? 'No sandbox manager is available right now.' : 'No sandbox manager is bound — bind one to this agent\'s sandboxes slot.';
  else if (!provider) error = `No manager the ${clsName(limit)} class allows is available.`;
  else if (!v.egress) error = `This manager offers no egress the ${clsName(limit)} class allows.`;
  else if (!String(v.name).trim()) error = 'Name the sandbox.';
  else if (String(v.name).trim().length > 64) error = 'The name is up to 64 characters.';
  else error = cwdCheck(v.cwd);
  return { f: v, managers: mopts, images, sizes, egress, error, ok: !error, many: usable.length > 1 };
}

// createBody: the form's values as POST /sandboxes takes them. conversation:
// made for it (and bound there unless bind is false); clientId: a retry
// returns the same sandbox.
export function createBody(f, { conversation, bind, clientId } = {}) {
  const b = { name: String(f.name || '').trim(), provider: f.provider, egress: f.egress, visibility: f.visibility };
  if (f.image) b.image = f.image;
  if (f.size) b.size = f.size;
  const cwd = String(f.cwd || '').trim();
  if (cwd) b.cwd = cwd;
  if (conversation != null) { b.conversation = conversation; if (bind === false) b.bind = false; }
  if (clientId) b.clientId = clientId;
  return b;
}
