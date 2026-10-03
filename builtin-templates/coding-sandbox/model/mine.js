// model/mine.js — what "Your sandboxes" shows: the page as a consumer of its
// own (API.md "Who is asking": its partition is this tile's path), from
// GET /sbx/hello and GET /sbx/sandboxes — the sandboxes you may use, the
// create form, a sandbox's lifecycle, sharing, files and terminal. Pure
// functions: no lit, no DOM, no calls — model/app.js makes those.
import * as F from './format.js';

// lookOnly: you have read access to this tile (GET /me's write is false):
// you may look at the sandboxes you may use, never change them — the
// manager refuses every change from its page without write access (D29).
export const lookOnly = (me) => !!(me && me.write === false);

// READ_ONLY: what the page says to someone who may only look.
export const READ_ONLY = 'You have read access to this tile: you may look at the sandboxes you may use and their files, '
  + 'not change them. Making, starting, stopping and sharing sandboxes, terminals and changing files need write access.';

// myRows: the sandboxes you may use, most recently active first, each with
// the actions you may take (the manager refuses what you may not anyway),
// and — for what you may not — why (filesWhy, termWhy, shareWhy).
export function myRows(list, me, now = Date.now()) {
  const user = (me && me.user) || '';
  const ro = lookOnly(me);
  return (list || []).map((s) => {
    const mine = !s.owner || !s.owner.user || s.owner.user === user;
    const busy = ['creating', 'deleting', 'starting', 'stopping'].includes(s.state);
    const caps = s.caps || [];
    const label = F.STATES[s.state] || s.state;
    const actions = [];
    if (!ro && s.state === 'stopped') actions.push({ id: 'start', label: 'Start' });
    if (!ro && (s.state === 'running' || s.state === 'starting')) actions.push({ id: 'stop', label: 'Stop' });
    if (!ro && mine && s.state !== 'deleting') {
      actions.push({ id: 'delete', label: 'Delete', danger: true, confirm: `Delete ${s.name}? Its files and snapshots go with it.` });
    }
    const filesWhy = !caps.includes('files') ? 'the substrate serves none yet'
      : busy || s.state === 'error' ? `the sandbox is ${label}`
      : ro && s.state !== 'running' ? `it is ${label}, and starting it needs write access to this tile` : '';
    const termWhy = ro ? 'a terminal needs write access to this tile'
      : s.state === 'error' ? 'the sandbox is in error'
      : busy ? `the sandbox is ${label}`
      : !caps.includes('tty') ? 'the substrate offers none (tty)' : '';
    const shareWhy = ro ? 'changing who may use it needs write access to this tile' : !mine ? 'only its owner changes who may use it' : '';
    return {
      id: s.id, name: s.name, state: s.state, stateLabel: F.STATES[s.state] || s.state, tone: F.stateTone(s.state), icon: F.stateIcon(s.state),
      stateDetail: s.stateDetail || '', image: (s.image && (s.image.title || s.image.id)) || '', size: (s.size && s.size.id) || '',
      sizeText: F.sizeText(s.size), egress: s.egress || 'none', egressText: F.egressText(s),
      isolation: F.ISOLATION[s.isolation] || s.isolation || '', owner: F.ownerText(s.owner), mine,
      visibility: s.visibility || 'private', shares: s.shares || [], workdir: s.workdir || '/', home: s.home || '',
      user: s.user || '', lastActive: s.lastActive || 0, lastText: F.ago(s.lastActive, now), caps,
      canFiles: !filesWhy, filesWhy, canTerminal: !termWhy, termWhy, canShare: !shareWhy, shareWhy,
      canChange: !ro, readOnly: ro, version: s.version, actions,
    };
  }).sort((a, b) => b.lastActive - a.lastActive || a.id.localeCompare(b.id));
}

// createForm: the create form as a view shows it — hello's images, sizes and
// networks as options, what is picked (the defaults unless changed), why it
// can't be sent yet (error) and why none can be made at all (cant: the
// substrate, or you — me — may only look).
export function createForm(hello, form = {}, me = null) {
  const h = hello || {};
  const images = (h.images || []).map((im) => ({ value: im.id, label: im.title || im.id, detail: (im.tools || []).join(', ') }));
  const sizes = (h.sizes || []).map((s) => ({ value: s.id, label: `${s.title || s.id} — ${F.sizeText(s)}` }));
  const egress = (h.egress || ['none']).map((e) => ({ value: e, label: F.EGRESS[e] || e }));
  const def = (xs, list) => ((list || []).find((x) => x.default) || (list || [])[0] || {}).id || (xs[0] && xs[0].value) || '';
  const f = {
    name: form.name ?? '', image: form.image || def(images, h.images), size: form.size || def(sizes, h.sizes),
    egress: form.egress || 'none', visibility: form.visibility || 'private',
  };
  let error = '';
  const name = String(f.name).trim();
  if (!name) error = 'name it';
  else if ([...name].length > 64) error = 'a name is 64 characters at most';
  const caps = h.caps || [];
  const cant = lookOnly(me) ? 'making a sandbox needs write access to this tile'
    : !hello ? 'the manager hasn\'t answered yet' : !caps.includes('exec') ? 'the substrate runs no commands yet' : '';
  return { f, images, sizes, egress, error, cant, notes: h.notes || [] };
}

// createBody: the create form as POST /sbx/sandboxes wants it.
export const createBody = (f) => ({
  name: String(f.name).trim(), image: f.image || undefined, size: f.size || undefined,
  egress: f.egress || undefined, visibility: f.visibility || undefined,
});

// terminalSrc: what <bx-terminal src> (web) dials for a shell in sandbox id
// at cwd — this tile's own route, with the page's frame token (the person
// verified): /api/<self>/sbx/sandboxes/{id}/tty?cwd=…
export const terminalSrc = (self, id, cwd = '') =>
  `/api/${self}/sbx/sandboxes/${encodeURIComponent(id)}/tty${cwd ? `?cwd=${encodeURIComponent(cwd)}` : ''}`;

// attachSrc: a terminal onto tty exec eid (the native view's: tile-relative,
// so a reconnect reattaches to the same shell).
export const attachSrc = (id, eid) => `sbx/sandboxes/${encodeURIComponent(id)}/execs/${encodeURIComponent(eid)}/tty`;

// fileRows: a directory listing as the file browser shows it (directories
// first), with what a tap does.
export function fileRows(listing, now = Date.now()) {
  if (!listing) return [];
  return F.sortEntries(listing.entries).map((e) => ({
    name: e.name, path: F.joinPath(listing.path, e.name), type: e.type, icon: F.entryIcon(e),
    dir: e.type === 'dir', size: e.type === 'dir' ? '' : F.bytes(e.size), when: F.ago(e.mtimeMs, now),
    detail: e.type === 'symlink' ? `→ ${e.target || '?'}` : e.type === 'dir' ? '' : F.bytes(e.size),
  }));
}

// VIEW_MAX: the most of a file the viewer reads (a ranged read; download for the rest).
export const VIEW_MAX = 256 << 10;
