// model/sandboxes.js — coding sandboxes (D115) as both views show them: the
// composer's sandbox picker (the open conversation's, or the next new
// chat's — only where the class has the sandbox toolset), the ▣ badge (the
// active sandbox and its working directory, and why a binding no longer
// resolves), the Sandboxes dialog's rows (state, manager, egress, owner, the
// actions your rights allow) and the create form (a manager's images, sizes
// and egress, as the class allows them). Pure functions of GET /sandboxes, a
// class (GET /classes, or a run view's `class`) and a run's view: no lit, no
// DOM, no calls — model/sandbox-store.js keeps the state and makes the calls
// (API.md "Coding sandboxes").

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
const BIND_REFUSED = /^sandbox\.(ref|cwd):|has no sandbox toolset|doesn't allow (sandboxes from|a sandbox with)|may not use this sandbox|this sandbox offers no/;
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
    const why = classAllows(cls, splitRef(b.ref).provider, b.egress, b.manager);
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

const detailOf = (s) => [s.manager, STATES[s.state] || s.state, EGRESS[s.egress] || s.egress].filter(Boolean).join(' · ');

// sandboxPicker: the composer's sandbox — the open conversation's (a pick
// binds it from its next turn) or, at home, the next new chat's
// (opts.pick {ref, cwd}; opts.cls the class for new chats). Shown only where
// the class has the sandbox toolset. Rows grouped This conversation (what it
// has attached) · Yours · Shared · Team; one you may not use, or the class
// does not allow, is there but disabled with the reason. `none` leaves the
// conversation without an active sandbox.
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
      : classAllows(cls, s.provider || splitRef(s.ref).provider, s.egress, s.manager);
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
    label: `${ICON} ${active ? active.name || splitRef(value).id : 'No sandbox'}`,
    title: conv ? 'the coding sandbox this conversation works in, from its next turn' : 'the coding sandbox your next new chat starts in',
    disabled: !talk,
    none: { value: '', label: 'No sandbox', on: !value },
    groups,
    actions: [
      { id: 'new', label: '＋ New sandbox…', disabled: !talk || !managers.length, why: newWhy },
      { id: 'manage', label: 'Manage sandboxes…', disabled: false, why: '' },
    ],
    stale, // why the pick can't be used ('' = it can, or the list isn't read yet)
    notes,
  };
}

// sandboxBadge: the open conversation's ▣ — its active sandbox and working
// directory, and why the binding no longer resolves (gone, its manager
// unbound or down, its class no longer allows it); every attached one for
// the popover (switch, detach). null when it has none.
export function sandboxBadge(conv, list, now = Date.now()) {
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
    label: `${ICON} ${name}${b.cwd ? ' · ' + b.cwd : ''}`,
    broken,
    detail: [manager, EGRESS[egress] || egress, STATES[state] || state].filter(Boolean).join(' · '),
    title: broken ? `${name}: ${broken}` : `works in ${name}${b.cwd ? ' at ' + b.cwd : ''} (${[manager, EGRESS[egress] || egress].filter(Boolean).join(', ')}) — change it here`,
    canChange: talks(conv),
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

// sandboxRows: the Sandboxes dialog — every sandbox you may see, yours
// first, then by when it was last active; each with the actions your rights
// allow: start/stop/thaw (who may use or manage it — or, for one the open
// conversation holds, anyone who may talk in it: through the conversation),
// archive (who may manage it, where its manager archives), share with the
// team or make private (its owner), delete (who may manage it — confirmed).
// opts.conv: the open conversation — "Use here" binds one (confirmed when a
// private one goes into a conversation other people are in); opts.cls at
// home: "Use for a new chat". opts.order: the refs in the order a view shows
// them — kept while it is open (a row never moves under the cursor), the
// ones it hasn't shown yet after them. "Open terminal" waits for phase 3 (a
// bx-terminal src): not offered.
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
    if (hasSandbox(cls) && s.canUse && !on && talks(conv) && !classAllows(cls, s.provider || splitRef(s.ref).provider, s.egress)) {
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
    if (s.canEdit) acts.push(s.visibility === 'team' ? { id: 'private', label: 'Make private' } : { id: 'team', label: 'Share with the team' });
    if (s.canManage) {
      acts.push({ id: 'delete', label: 'Delete', danger: true, confirm: `Delete the sandbox “${name}”? Everything in it is gone for good${bound
        ? ` — ${bound === 1 ? 'a conversation' : bound + ' conversations'} you see ${bound === 1 ? 'loses' : 'lose'} it` : ''}.` });
    }
    const owner = s.owner && s.owner.user;
    return {
      ref: s.ref, name, id: s.id || splitRef(s.ref).id, state: st, stateLabel: STATES[st] || st || '?',
      stateDetail: s.stateDetail || '', busy: /…$/.test(STATES[st] || ''),
      manager: s.manager || s.provider || '', egress: s.egress || '', egressLabel: EGRESS[s.egress] || s.egress || '',
      image: (s.image && (s.image.title || s.image.id)) || '', size: sizeWords(s.size),
      owner: s.mine || (owner && owner === user) ? 'you' : owner || 'the agent', mine: !!s.mine,
      visibility: s.visibility === 'team' ? 'team' : 'private', visLabel: s.visibility === 'team' ? 'team' : 'private',
      lastActive: s.lastActive || 0, lastLabel: ago(s.lastActive, opts.now),
      bound, here: root != null && (s.boundTo || []).includes(root), active: on,
      // where it is used, in words: the open conversation's active or attached
      // one — at home, the pick is the next new chat's
      where: on ? (conv ? 'active here' : 'next new chat') : root != null && (s.boundTo || []).includes(root) ? 'attached here' : '',
      actions: acts,
    };
  });
}

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
