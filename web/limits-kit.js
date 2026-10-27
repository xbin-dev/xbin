/**
 * limits-kit.js — the words and arithmetic of a tile's resource limits
 * (D203, docs/isolation.md §Resource limits) for every surface that shows
 * them: the shell's prompt on a tile (<bx-limits view="prompt">), its
 * banners' raise buttons, the tile's ⚙ section, the admin console's limits
 * tab and org ceilings, and xbind's /xbin/limits page.
 *
 * Mirrors internal/limits: the kinds (named after their unit — every value
 * on the wire is an integer in it), their labels and steps, Format (fmt
 * here) and ParseValue. The server decides everything: what fits, who may
 * approve (a choice's `fits`/`approvable`/`why`, a row's `canDecide`/
 * `canRevert`) — this file only says it.
 *
 * Pure (one import, the dismissal key's home): hack/limits.test.mjs runs it
 * under node.
 */
import { limitKey, isDismissed } from './bx-dismiss.js';

export { limitKey };

// The kinds, in display order (limits.Kinds).
export const KINDS = Object.freeze(['memMiB', 'diskGiB', 'vmMemMiB', 'vmVCPUs', 'sbxMemMiB', 'sbxVCPUs', 'sbxDiskGiB']);

// limits.Meta: label, unit, a raise's step, the bx alias, where a manifest asks.
export const META = Object.freeze({
  memMiB: { label: 'memory', unit: 'MiB', step: 64, alias: 'memory', manifest: 'limits.memory', min: 128 },
  diskGiB: { label: 'data disk', unit: 'GiB', step: 1, alias: 'disk', manifest: 'limits.disk', min: 1 },
  vmMemMiB: { label: 'VM memory', unit: 'MiB', step: 64, alias: 'vm-memory', manifest: 'vm.memory', min: 128 },
  vmVCPUs: { label: 'VM vCPUs', unit: 'vCPUs', step: 1, alias: 'vm-vcpus', manifest: 'vm.vcpus', min: 1 },
  sbxMemMiB: { label: 'sandbox memory', unit: 'MiB', step: 64, alias: 'sandbox-memory', manifest: 'limits.sandboxes.memory', min: 128 },
  sbxVCPUs: { label: 'sandbox vCPUs', unit: 'vCPUs', step: 1, alias: 'sandbox-vcpus', manifest: 'limits.sandboxes.vcpus', min: 1 },
  sbxDiskGiB: { label: 'sandbox disk', unit: 'GiB', step: 1, alias: 'sandbox-disk', manifest: 'limits.sandboxes.disk', min: 1 },
});

// meta(kind): a kind's metadata; an unknown kind (a newer xbind's) by its name.
export const meta = (k) => META[k] ?? { label: String(k ?? ''), unit: '', step: 1, alias: String(k ?? ''), manifest: '', min: 1 };
export const isCPU = (k) => meta(k).unit === 'vCPUs';
// a VM's size applies at the backend's next start (a restart is downtime)
export const isVM = (k) => k === 'vmMemMiB' || k === 'vmVCPUs';

// One decimal, the way Go's strconv.FormatFloat(f, 'f', 1) rounds (half to
// even: 1280 MiB is "1.2 GiB" on both ends), ".0" trimmed.
function oneDecimal(n, d) {
  const t = (n * 10) / d;
  let r = Math.floor(t);
  const frac = t - r;
  if (frac > 0.5 || (frac === 0.5 && r % 2 === 1)) r += 1;
  return r % 10 === 0 ? String(r / 10) : `${Math.floor(r / 10)}.${r % 10}`;
}

// fmt(kind, v) → "2 GiB", "1.5 GiB", "512 MiB", "75 GiB", "4 vCPUs" (limits.Format).
export function fmt(k, v) {
  if (v == null || !Number.isFinite(Number(v))) return '—';
  v = Number(v);
  switch (meta(k).unit) {
    case 'MiB': return v >= 1024 ? `${oneDecimal(v, 1024)} GiB` : `${v} MiB`;
    case 'GiB': return v >= 1024 ? `${oneDecimal(v, 1024)} TiB` : `${v} GiB`;
    case 'vCPUs': return v === 1 ? '1 vCPU' : `${v} vCPUs`;
  }
  return String(v);
}

// parseValue(kind, text) → {value} or {error}: what an approver types. A size
// for a byte kind ("6G", "1536M", "1.5 GiB" — binary units, as manifests
// write them; a bare number is in the kind's unit), a count for vCPUs.
export function parseValue(k, s) {
  const t = String(s ?? '').trim();
  const m = meta(k);
  if (!t) return { error: 'a value' };
  if (isCPU(k)) {
    if (!/^\d+$/.test(t) || +t < 1 || +t > 256) return { error: `${t}: a number of vCPUs, 1–256` };
    return { value: +t };
  }
  const x = /^(\d+(?:\.\d+)?)\s*([kmgt]?)(i?b?)$/i.exec(t);
  if (!x || !(+x[1] > 0) || (x[3] && !x[2])) return { error: `${t}: a size like "512M" or "4G"` };
  const shift = { k: 10, m: 20, g: 30, t: 40 }[x[2].toLowerCase()];
  const per = m.unit === 'GiB' ? 2 ** 30 : 2 ** 20;
  if (shift === undefined) return { value: Math.ceil(+x[1]) }; // a bare number: the kind's own unit
  return { value: Math.ceil((+x[1] * 2 ** shift) / per) };
}

// sizeText(kind, v) → v as an approver would type it, exactly: "4G",
// "1536M", "100G", "8" — what an editor's field starts from (fmt rounds).
export function sizeText(k, v) {
  if (v == null || !Number.isFinite(Number(v))) return '';
  v = Number(v);
  switch (meta(k).unit) {
    case 'MiB': return v % 1024 === 0 ? `${v / 1024}G` : `${v}M`;
    case 'GiB': return `${v}G`;
  }
  return String(v);
}

// step(kind, current, pct): current raised by pct, rounded up to the kind's
// step, at least one step up (limits.Step) — the server's choices say the
// same; this is for a preview before they are read.
export function step(k, current, pct) {
  const s = Math.max(meta(k).step, 1);
  let v = Math.ceil((current * (100 + pct)) / 100);
  v = Math.ceil(v / s) * s;
  return Math.max(v, current + s);
}

// ---- choices (limits.Choice: {pct, value, fits, why?, approvable, default?}) ----

export const choiceLabel = (c, k) => `+${c?.pct ?? 0}% · ${fmt(k, c?.value)}`;

// choiceState(c) → {live, why}: a choice is live when it fits and this
// viewer may approve it; otherwise its reason (the server's words first).
export function choiceState(c) {
  const live = !!c?.fits && !!c?.approvable;
  if (live) return { live, why: '' };
  return { live, why: c?.why || (!c?.fits ? 'doesn’t fit the workspace' : 'you can’t approve this') };
}

// primaryChoice(choices) → the one to show as the primary button: the
// default (+50 %) when it is live, else the first live one; null for none.
export function primaryChoice(choices) {
  const live = (choices ?? []).filter((c) => choiceState(c).live);
  return live.find((c) => c.default) ?? live[0] ?? null;
}

// askWho(approvers) → "a workspace admin or an admin of org acme" — who
// to ask, from the server's hints ("workspace-admin", "org:<id>").
export function askWho(approvers) {
  const who = (approvers ?? []).map((a) => (a === 'workspace-admin' ? 'a workspace admin'
    : String(a).startsWith('org:') ? `an admin of org ${String(a).slice(4)}` : String(a))).filter(Boolean);
  return who.length ? who.join(' or ') : 'a workspace admin';
}

// ---- times and decisions ----

const zeroTime = (t) => !t || typeof t !== 'string' || t.startsWith('0001-');

// ago(iso, now) → "just now", "5 min ago", "3 h ago", "2 d ago", "2026-09-30".
export function ago(iso, now = Date.now()) {
  if (zeroTime(iso)) return '';
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '';
  const s = Math.max(0, (now - t) / 1000);
  if (s < 90) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400 * 2) return `${Math.round(s / 3600)} h ago`;
  if (s < 86400 * 30) return `${Math.round(s / 86400)} d ago`;
  return new Date(t).toISOString().slice(0, 10);
}

// who(by) → "bob" for "user:bob"; "root" stays.
export const who = (by) => (String(by ?? '').startsWith('user:') ? String(by).slice(5) : String(by ?? '') || 'someone');

const VERB = { raise: 'raised to', 'admin-set': 'set to', 'manifest-ask': 'ask approved up to' };

// decisionText(d, kind, now) → "raised to 3 GiB by bob, 2 h ago".
export function decisionText(d, k, now = Date.now()) {
  if (!d) return '';
  const when = ago(d.at, now);
  return `${VERB[d.source] ?? 'set to'} ${fmt(k, d.value)} by ${who(d.by)}${when ? `, ${when}` : ''}`;
}

// hitText(hit, kind, now) → "OOM-killed (2 processes) at 2 GiB, 3× since
// 14:02" — what met the cap, the limit it met, how often.
export function hitText(h, k, now = Date.now()) {
  if (!h) return '';
  const since = zeroTime(h.since) ? h.at : h.since;
  // the limit it met, unless the detail already names it ("at its 50 GiB quota")
  const at = h.value && !String(h.detail ?? '').includes(fmt(k, h.value)) ? ` at ${fmt(k, h.value)}` : '';
  const parts = [`${h.detail || 'met its limit'}${at}`];
  if ((h.count ?? 0) > 1) parts.push(`${h.count}× since ${clock(since, now)}`);
  else if (!zeroTime(h.at)) parts.push(ago(h.at, now));
  let s = parts.filter(Boolean).join(', ');
  if (h.deployment) s += ` (deployment ${h.deployment})`;
  if (h.partition) s += ' (a person’s partition)';
  return s;
}

// clock(iso, now): "14:02" today, else the date.
function clock(iso, now = Date.now()) {
  if (zeroTime(iso)) return '';
  const t = new Date(iso);
  if (!Number.isFinite(t.getTime())) return '';
  return new Date(now).toDateString() === t.toDateString()
    ? `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}` : t.toISOString().slice(0, 10);
}

// ---- prompts (GET /limits?pending=1) ----

// promptItems(answer, {tile, dismissed, now}) → what a tile's prompt shows:
// the pending items this person may decide (approvable) or whose tile is
// theirs (mine), less what they dismissed — each with its key and words.
export function promptItems(answer, { tile = '', dismissed = null, now = Date.now() } = {}) {
  const out = [];
  for (const p of answer?.pending ?? []) {
    if (!p || typeof p.tile !== 'string' || !p.tile || (tile && p.tile !== tile)) continue;
    if (!(p.approvable || p.mine)) continue;
    const key = limitKey(p);
    if (isDismissed(dismissed, 'limits', key)) continue;
    const cap = p.why === 'cap', m = meta(p.kind);
    out.push({
      ...p, key, cap, label: m.label,
      head: cap ? 'At its limit' : 'Limit request',
      what: cap ? hitText(p.hit, p.kind, now) : `${fmt(p.kind, p.ask)}${m.manifest ? ` (the manifest’s ${m.manifest})` : ''}`,
      current: fmt(p.kind, p.effective),
      askWho: askWho(p.approvers),
    });
  }
  return out;
}

// promptCounts(items) → {asks, caps}: the words' numbers.
export const promptCounts = (items) => ({ asks: (items ?? []).filter((p) => p.why !== 'cap').length, caps: (items ?? []).filter((p) => p.why === 'cap').length });

// ---- rows (GET /limits, GET /limits?tile=) ----

// kindsOf(row) → [[kind, limit], …] in display order (unknown kinds last).
export function kindsOf(row) {
  const l = row?.limits ?? {};
  const known = KINDS.filter((k) => l[k]).map((k) => [k, l[k]]);
  return [...known, ...Object.keys(l).filter((k) => !KINDS.includes(k)).map((k) => [k, l[k]])];
}

// flags(l) → what a kind's state is, for filters and badges.
export function flags(l) {
  return {
    raised: !!l && Number(l.effective) > Number(l.default),
    pending: !!l?.pending,
    cap: !!l?.hit,
    later: !!l && (l.appliesAt === 'next-start' || (l.applied != null && l.applied !== l.effective)),
  };
}

// appliesText(l, kind) → when the value takes effect, or ''.
export function appliesText(l, k) {
  if (!l) return '';
  if (l.applied != null && l.applied !== l.effective) {
    return `runs at ${fmt(k, l.applied)} now — ${fmt(k, l.effective)} applies at the backend’s next start`;
  }
  if (l.appliesAt === 'next-start') return 'applies at the backend’s next start';
  return '';
}

// tableEntries(rows, {q, raised, pending, cap, org}) → [{row, kind, l,
// first}]: one entry per tile and kind the filters keep. The chips are
// either-or (none on: everything); the org and the query narrow.
export function tableEntries(rows, f = {}) {
  const q = String(f.q ?? '').trim().toLowerCase();
  const chips = ['raised', 'pending', 'cap'].filter((c) => f[c]);
  const out = [];
  for (const row of rows ?? []) {
    if (f.org && (row.org ?? '') !== f.org) continue;
    if (q && !`${row.tile} ${row.org ?? ''} ${row.scope ?? ''}`.toLowerCase().includes(q)) continue;
    let first = true;
    for (const [kind, l] of kindsOf(row)) {
      const fl = flags(l);
      if (chips.length && !chips.some((c) => fl[c])) continue;
      out.push({ row, kind, l, first });
      first = false;
    }
  }
  return out;
}

// counts(rows) → {raised, pending, cap}: tile-kinds in each state.
export function counts(rows) {
  const n = { raised: 0, pending: 0, cap: 0 };
  for (const row of rows ?? []) for (const [, l] of kindsOf(row)) { const fl = flags(l); for (const k of Object.keys(n)) if (fl[k]) n[k] += 1; }
  return n;
}

// ---- the API ----

// target(x) → the body's {tile} or {scope}: a row, a pending item or an
// alert's action names the tile; a scope only when no tile does (disk).
export const target = (x) => (x?.tile ? { tile: x.tile } : x?.scope ? { scope: x.scope } : {});
export const targetQuery = (x) => (x?.tile ? `tile=${encodeURIComponent(x.tile)}` : `scope=${encodeURIComponent(x?.scope ?? '')}`);

// raiseBody(x, kind, value, source, {restart, force}) → POST /limits's body.
export function raiseBody(x, kind, value, source = 'raise', { restart = false, force = false } = {}) {
  const b = { ...target(x), kind, value, source };
  if (restart) b.restart = true;
  if (force) b.force = true;
  return b;
}

// errorText(status, data): the server's words, or what a status means here.
export function errorText(status, data) {
  if (data && typeof data === 'object' && data.error) return String(data.error);
  if (status === 404) return 'this xbind has no tile limits (it predates them)';
  if (status === 403) return 'you can’t change this limit';
  return status ? `error ${status}` : 'xbind can’t be reached';
}

// limitsCall(fetcher, method, path, body) → {ok, status, data, error}: one
// call under /api/xbin, never a throw. fetcher is the page's (plain fetch:
// the person's session; the admin console's xbin.fetch).
export async function limitsCall(fetcher, method, path, body) {
  const f = fetcher ?? globalThis.fetch;
  try {
    const r = await f(`/api/xbin${path}`, body === undefined ? { method }
      : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const text = await r.text().catch(() => '');
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = null; }
    return { ok: r.ok, status: r.status, data, error: r.ok ? '' : errorText(r.status, data) };
  } catch (e) {
    return { ok: false, status: 0, data: null, error: errorText(0, null) + (e?.message ? ` (${e.message})` : '') };
  }
}

// ceilingsBody(draft, current) → PUT /orgs/{org}/limits's body: every kind
// the draft changed — a value, or null to remove one ("" in the draft).
// {error} when a value doesn't parse.
export function ceilingsBody(draft, current = {}) {
  const body = {};
  for (const k of KINDS) {
    if (!(k in (draft ?? {}))) continue;
    const t = String(draft[k] ?? '').trim();
    if (!t) { if (current?.[k]) body[k] = null; continue; }
    const v = parseValue(k, t);
    if (v.error) return { error: `${meta(k).label}: ${v.error}` };
    if (v.value !== current?.[k]) body[k] = v.value;
  }
  return { body };
}
