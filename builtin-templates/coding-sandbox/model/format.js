// model/format.js — how both views say things: states, networks, sizes,
// bytes, times, owners, paths. Pure functions: no lit, no DOM, no calls.

// What a sandbox's state is called; the transitional ones end in "…".
export const STATES = {
  creating: 'creating…', stopped: 'stopped', starting: 'starting…', running: 'running', stopping: 'stopping…',
  archiving: 'archiving…', archived: 'archived', thawing: 'thawing…', deleting: 'deleting…', error: 'error',
};

// stateTone: the colour role of a state (the native vocabulary's tones; the
// web maps them to its palette).
export function stateTone(state) {
  if (state === 'running') return 'ok';
  if (state === 'error') return 'danger';
  if (state === 'stopped' || state === 'archived') return 'muted';
  return 'warn';
}

// What a sandbox may reach (docs/sandbox-manager.md), in words.
export const EGRESS = { none: 'no network', internet: 'internet', open: 'open network' };

// egressText: a sandbox's network, and one waiting for its next start.
export function egressText(s) {
  const e = (s && s.egress) || 'none';
  const next = (s && s.egressNext) || '';
  const w = EGRESS[e] || e;
  return next && next !== e ? `${w} → ${EGRESS[next] || next} at the next start` : w;
}

// What keeps a sandbox from its host.
export const ISOLATION = { vm: 'VM', namespace: 'namespace', container: 'container', 'cloud-vm': 'cloud VM', other: 'other' };

// gib: MiB as GiB ("2 GiB", "1.5 GiB").
const gib = (mib) => {
  const g = mib / 1024;
  return `${Number.isInteger(g) ? g : g.toFixed(1)} GiB`;
};

// sizeText: {memMiB, vcpus, diskGiB} in words ("2 GiB · 2 vCPU · 20 GiB disk").
export function sizeText(s) {
  if (!s || !s.memMiB) return '';
  return `${gib(s.memMiB)} · ${s.vcpus} vCPU · ${s.diskGiB} GiB disk`;
}

// bytes: a byte count ("734 MiB", "1.2 GiB", "12 KiB", "80 B").
export function bytes(n) {
  n = Number(n) || 0;
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`;
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MiB`;
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KiB`;
  return `${n} B`;
}

// ago: a time (unix ms) as it is from now ("just now", "5 min ago", "3 h ago", "2 d ago").
export function ago(ms, now = Date.now()) {
  if (!ms) return '';
  const s = Math.max(0, (now - ms) / 1000);
  if (s < 90) return 'just now';
  if (s < 90 * 60) return `${Math.round(s / 60)} min ago`;
  if (s < 36 * 3600) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

// ownerText: a sandbox's owner — the person (asserted when a backend named
// them), or none (the consumer's own).
export function ownerText(o) {
  if (!o || !o.user) return '—';
  return o.asserted ? `${o.user} (asserted)` : o.user;
}

// usersText: a share's people: "*" is everyone the consumer serves.
export const usersText = (u) => (u === '*' ? 'everyone it serves' : (Array.isArray(u) && u.length ? u.join(', ') : 'nobody'));

// whoText: who may use a sandbox, in words: here (its home consumer's people:
// the owner alone — private — or with members, or everyone it serves), then
// each consumer it is shared with and whom there.
export function whoText(s) {
  const members = (s && s.members) || [];
  const here = s && s.visibility === 'team' ? 'everyone its consumer serves'
    : members.length ? `its owner and ${members.join(', ')}` : 'private';
  return [here, ...((s && s.shares) || []).map((x) => `${consumerText(x.consumer, x.partitionId)} (${usersText(x.users)})`)].join(' · ');
}

// consumerText: a consumer tile, or one user partition of a partitioned
// consumer (its opaque id, shortened).
export const consumerText = (consumer, partitionId) => (partitionId ? `${consumer}/${String(partitionId).slice(0, 8)}` : consumer);

// parseUsers: "*" or "" → "*" (everyone); a list of ids, split on commas and spaces.
export function parseUsers(text) {
  const t = String(text ?? '').trim();
  if (!t || t === '*') return '*';
  return [...new Set(t.split(/[\s,]+/).filter(Boolean))];
}

// --- paths inside a sandbox --------------------------------------------------------

// joinPath: dir + name, one slash between.
export const joinPath = (dir, name) => (String(dir).replace(/\/+$/, '') || '') + '/' + String(name).replace(/^\/+/, '');

// parentPath: the directory above p ("/" above itself).
export function parentPath(p) {
  const s = String(p || '/').replace(/\/+$/, '');
  const i = s.lastIndexOf('/');
  return i <= 0 ? '/' : s.slice(0, i);
}

// baseName: the last element of p.
export const baseName = (p) => String(p || '').replace(/\/+$/, '').split('/').pop() || '/';

// crumbs: p as its directories from the root, each with its path.
export function crumbs(p) {
  const parts = String(p || '/').split('/').filter(Boolean);
  const out = [{ name: '/', path: '/' }];
  let at = '';
  for (const x of parts) {
    at += '/' + x;
    out.push({ name: x, path: at });
  }
  return out;
}

// cleanPath: an absolute path as typed ("" → fallback).
export function cleanPath(p, fallback = '/') {
  const t = String(p ?? '').trim();
  if (!t) return fallback;
  const out = [];
  for (const x of (t.startsWith('/') ? t : '/' + t).split('/')) {
    if (!x || x === '.') continue;
    if (x === '..') out.pop();
    else out.push(x);
  }
  return '/' + out.join('/');
}

// looksBinary: bytes a text view shouldn't show (a NUL, or mostly control characters).
export function looksBinary(text) {
  const s = String(text ?? '').slice(0, 4096);
  if (s.includes('\0')) return true;
  let ctl = 0;
  for (const ch of s) {
    const c = ch.charCodeAt(0);
    if (c < 32 && c !== 9 && c !== 10 && c !== 13) ctl++;
  }
  return s.length > 0 && ctl / s.length > 0.1;
}

// entryIcon: a directory entry's icon (the native vocabulary's names).
export const entryIcon = (e) => (e.type === 'dir' ? 'folder' : e.type === 'symlink' ? 'link' : 'file');

// sortEntries: directories first, then by name.
export function sortEntries(entries) {
  return [...(entries || [])].sort((a, b) => (a.type === 'dir') !== (b.type === 'dir') ? (a.type === 'dir' ? -1 : 1) : a.name.localeCompare(b.name));
}
