/**
 * partitions-view.js — the words and request bodies of the admin console's
 * runtime → partitions view (partitions.js, partition-tile.js;
 * docs/partitions.md §Operating people's partitions). Pure and lit-free, so hack/admin-partitions.test.mjs runs it
 * in node.
 *
 * Everything here reads GET /api/xbin/partitions as an admin sees it:
 * metadata — who has a partition, its state, size and counts — never what a
 * partition holds. A field an older xbind doesn't send reads as absent
 * (docs/compat.md rule 3): its line is left out, never shown as zero.
 */

const plural = (n, one, many = one + 's') => `${n} ${n === 1 ? one : many}`;

// modeName(spec) → the mode as xbind names it (registry.PartitionSpec.String).
export function modeName(s) {
  if (s?.user && s?.global) return 'user + global';
  if (s?.user) return 'user';
  if (s?.global) return 'global';
  return 'unpartitioned';
}

// specOf(x) → {user, global}, or null for unpartitioned (the mode act's
// from/to).
export const specOf = (x) => (x && (x.user || x.global) ? { user: !!x.user, global: !!x.global } : null);

// STATE_WORDS: a tile's state as the view says it.
export const STATE_WORDS = {
  partitioned: 'partitioned',
  unpartitioned: 'unpartitioned',
  pending: 'waiting for a manager',
  invalid: 'invalid request',
};
export const stateWords = (st) => STATE_WORDS[st] || String(st || '—');

// requestText(tile, x) → the open or declined request of a listing (or an
// overview row) in words, '' without one.
export function requestText(tile, x) {
  const q = x?.request;
  if (!q || typeof q !== 'object') return '';
  const move = `${modeName(x.spec)} → ${modeName(q.spec)}`;
  const since = q.since ? ` since ${day(q.since)}` : '';
  return q.declined ? `The request ${move} was declined: ${tile} runs ${modeName(x.spec)}, nothing was deleted.`
    : `${tile}'s code asks for ${move}${since}; it runs nothing until a manager switches or keeps the current mode.`;
}

// switchDeletes(from, to) → what a switch deletes (registry.SwitchDeletes,
// owner ruling H1).
export function switchDeletes(from, to) {
  if (!!from?.user !== !!to?.user) return 'all data in this tile';
  if (from?.global && !to?.global) return 'the global instance\'s data and the tile\'s shared resources (people\'s partitions stay)';
  return 'nothing (the global instance starts empty)';
}
export const deletesNothing = (from, to) => switchDeletes(from, to).startsWith('nothing');

// modeBody(tile, x, act, extra) → POST /partitions/mode's body for the
// request listing x shows (xbind refuses a decision on another one).
export const modeBody = (tile, x, act, extra = {}) => ({ tile, act, from: specOf(x?.spec), to: specOf(x?.request?.spec), ...extra });

export function bytesText(n) {
  const b = Number(n) || 0;
  if (b < 1024) return plural(b, 'byte');
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = b / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

// wipedText(w) → a dry run's (or a switch's) counts in words.
export function wipedText(w = {}) {
  const parts = [
    plural(w.namespaces || 0, 'data namespace'),
    plural(w.partitions || 0, 'person\'s partition', 'people\'s partitions'),
    plural(w.vaultKeys || 0, 'vault key'),
    plural(w.registrations || 0, 'registration'),
    bytesText(w.bytes || 0),
  ];
  return parts.join(', ') + (w.subkeys ? `; ${plural(w.subkeys, 'backup key')} erased` : '');
}

// switchLines(tile, x, dry) → the typed confirmation's text, from the dry
// run's answer: what it deletes, the counts, what it keeps, the sandbox
// managers that don't keep people apart.
export function switchLines(tile, x, dry = {}) {
  const from = x?.spec, to = x?.request?.spec, none = deletesNothing(from, to);
  const lines = [`Switching ${tile} from ${modeName(from)} to ${modeName(to)} deletes ${dry.deletes || switchDeletes(from, to)}.`];
  if (!none) lines.push(`It deletes: ${wipedText(dry.wiped)}.`);
  if (dry.people) lines.push(`${plural(dry.people, 'person', 'people')} whose partition is deleted will be told.`);
  for (const k of Array.isArray(dry.keeps) ? dry.keeps : []) lines.push(`Keeps: ${k}`);
  if (Array.isArray(dry.managers) && dry.managers.length) {
    lines.push(`These sandbox managers don't keep people apart (their hello lacks "partitions"): ${dry.managers.join(', ')}.`);
  }
  lines.push(none ? 'Nothing is deleted.' : 'This can\'t be undone.');
  return lines;
}

// resetConfirm(tile, user) → the text a reset needs typed (POST
// /partitions/reset's confirm), as a restore does too.
export const resetConfirm = (tile, user) => `${tile} user:${user}`;

// partitionOp(tile, row, extra) → a stop's / reset's body.
export const partitionOp = (tile, row, extra = {}) => ({ tile, partition: row.partition || `user:${row.user}`, ...extra });

const day = (t) => {
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? String(t || '') : d.toISOString().slice(0, 16).replace('T', ' ');
};
export const when = day;

// regsText(r) → a row's registrations in words ('' for none).
export function regsText(r) {
  if (!r || typeof r !== 'object') return '';
  const parts = [];
  if (r.cronJobs) parts.push(plural(r.cronJobs, 'cron job'));
  if (r.busSubscriptions) parts.push(plural(r.busSubscriptions, 'bus subscription'));
  if (r.ifaceInstances) parts.push(plural(r.ifaceInstances, 'interface instance'));
  if (r.ingressHosts) parts.push(plural(r.ingressHosts, 'ingress host'));
  if (r.vaultKeys) parts.push(plural(r.vaultKeys, 'vault key'));
  if (r.missedTicks) parts.push(`${plural(r.missedTicks, 'missed tick')}`);
  if (r.dormantDrops) parts.push(`${plural(r.dormantDrops, 'dropped delivery', 'dropped deliveries')}`);
  return parts.join(' · ');
}

// mailText(m) → an inbox's counts in words ('' for none): never an item.
export function mailText(m) {
  if (!m || typeof m !== 'object') return '';
  const parts = [`${m.pending || 0} waiting${m.pending ? ` (${bytesText(m.bytes)})` : ''}`];
  if (m.expired) parts.push(`${m.expired} expired`);
  if (m.undeliverable) parts.push(`${m.undeliverable} undeliverable`);
  return parts.join(' · ');
}

// runningText(row) → whether and how a person's instance runs.
export function runningText(row) {
  const i = row?.instance;
  if (!row?.running || !i) return row?.crashLoop ? 'stopped (crash loop)' : 'stopped';
  const bits = [i.state || 'running'];
  if (i.uptimeSec) bits.push(durText(i.uptimeSec));
  if (i.rssKb) bits.push(bytesText(i.rssKb * 1024));
  if (i.restarts) bits.push(plural(i.restarts, 'restart'));
  if (i.errorClass) bits.push(`error: ${i.errorClass}`);
  return bits.join(' · ');
}

export function durText(s) {
  s = Math.max(0, Math.floor(Number(s) || 0));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d${Math.floor((s % 86400) / 3600)}h`;
}

// shortId(pkey) → a partition id shortened for a table cell (the whole id
// in the title).
export const shortId = (id) => (typeof id === 'string' && id.length > 12 ? `${id.slice(0, 10)}…` : String(id || ''));

// historyWhat(h) → what a mode history entry did, in words.
function historyWhat(h) {
  const from = modeName(h.from), to = modeName(h.to);
  switch (h.op) {
    case 'auto': return `recorded ${to} (the tile held no data)`;
    case 'request': return `asked for ${from} → ${to}`;
    case 'switch': return `switched ${from} → ${to}`;
    case 'keep': return `kept ${from}, declining ${to}`;
    case 'withdrawn': return `request ${from} → ${to} withdrawn`;
    case 'backup-erase': return 'backup keys erased';
    case 'partition-restore': return 'a person\'s partition restored';
    default: return String(h.op || '');
  }
}

// historyText(h) → one mode history entry in words.
export function historyText(h) {
  if (!h || typeof h !== 'object') return '';
  const bits = [historyWhat(h)];
  if (h.by) bits.push(`by ${h.by}`);
  const w = h.wiped && typeof h.wiped === 'object' ? h.wiped : null;
  if (w && h.op === 'switch') bits.push(`deleted ${wipedText(w)}`);
  else if (w?.subkeys) bits.push(plural(w.subkeys, 'key'));
  if (h.partition) bits.push(`partition ${shortId(h.partition)}`);
  if (h.reason) bits.push(h.reason);
  return bits.join(' · ');
}

// limitsBody(tile, draft) → POST /partitions/limits' body from the inputs
// ('' leaves a value unchanged; 0 clears it back to the default).
export function limitsBody(tile, draft = {}) {
  const b = { tile };
  const num = (v) => (v === '' || v === undefined || v === null ? undefined : Number(v));
  const mr = num(draft.maxRunning), pb = num(draft.partitionMiB);
  if (mr !== undefined && Number.isFinite(mr)) b.maxRunning = Math.max(0, Math.floor(mr));
  if (pb !== undefined && Number.isFinite(pb)) b.partitionBytes = Math.max(0, Math.floor(pb * 1024 * 1024));
  return b;
}

// tileNotes(row) → an overview row's flags in words: reviewed code only,
// trust warnings, caps hit, global binds and requesters without global.
export function tileNotes(row) {
  const out = [];
  if (row?.reviewedOnly?.on) out.push({ kind: 'ok', text: 'reviewed code only' });
  for (const w of Array.isArray(row?.trust) ? row.trust : []) out.push({ kind: 'warn', text: String(w) });
  if (row?.capsHit) out.push({ kind: 'warn', text: `caps hit: ${row.capsHit.kind} ×${row.capsHit.count} (last ${day(row.capsHit.at)})` });
  const gb = row?.globalBinds && typeof row.globalBinds === 'object' ? Object.entries(row.globalBinds) : []; // slot → providers
  if (gb.length) out.push({ kind: 'info', text: `global binds: ${gb.map(([slot, refs]) => `${slot} → ${[].concat(refs).join(', ')}`).join('; ')}` });
  if (Array.isArray(row?.boundWithoutGlobal) && row.boundWithoutGlobal.length) out.push({ kind: 'warn', text: `bound by ${row.boundWithoutGlobal.join(', ')}, which reach nothing (no global instance)` });
  if (Array.isArray(row?.managersLacking) && row.managersLacking.length) out.push({ kind: 'warn', text: `sandbox managers that don't keep people apart: ${row.managersLacking.join(', ')}` });
  if (row?.untrackedCount) out.push({ kind: 'warn', text: `${plural(row.untrackedCount, 'untracked file')} in the shared directory` });
  if (row?.untrackedError) out.push({ kind: 'warn', text: `untracked files: ${row.untrackedError}` });
  return out;
}
