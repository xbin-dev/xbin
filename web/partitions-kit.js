// web/partitions-kit.js — the partitions page's words and data
// (/xbin/partitions; docs/partitions.md §Your partitions page; PD-47).
// Pure and lit-free: it reads the partitions API as the signed-in person
// and turns the answers into what the page shows —
// hack/partitions-page.test.mjs runs it in node, and
// web/partitions-page.js draws it. Every call is a plain fetch with the
// person's own session (never xbin.fetch: the page is xbind's, not a tile's).

// ---- the API ----

// call(fetchFn, method, path, body?) → {ok, status, body}: one call of
// /api/xbin<path>. A network failure is status 0 with an error body; a
// non-JSON answer is an empty body.
export async function call(fetchFn, method, path, body) {
  const init = { method, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  let r;
  try {
    r = await fetchFn(`/api/xbin${path}`, init);
  } catch (e) {
    return { ok: false, status: 0, body: { error: `xbind can't be reached (${e?.message || e}): try again` } };
  }
  const b = await r.json().catch(() => ({}));
  return { ok: r.ok, status: r.status, body: b && typeof b === 'object' ? b : {} };
}

// errorText(answer) → a refusal's words.
export const errorText = (res) => String(res?.body?.error || `failed (${res?.status ?? '?'})`);

const q = encodeURIComponent;
const arr = (x) => (Array.isArray(x) ? x : []);
const obj = (x) => (x && typeof x === 'object' && !Array.isArray(x) ? x : {});

// loadPage(fetchFn) → the page's model (assemble's), from one round of reads:
// who the caller is, the partitions overview, the component rows (owners,
// the notes of paused tiles), their consents, personal binds and ledger, and
// each listed tile's detail.
export async function loadPage(fetchFn) {
  const [who, ov, comps, consents, binds, ledger] = await Promise.all([
    call(fetchFn, 'GET', '/whoami'),
    call(fetchFn, 'GET', '/partitions'),
    call(fetchFn, 'GET', '/components'),
    call(fetchFn, 'GET', '/partitions/consents'),
    call(fetchFn, 'GET', '/partitions/binds'),
    call(fetchFn, 'GET', '/partitions/ledger?days=30'),
  ]);
  const tiles = ov.ok ? arr(ov.body.tiles).filter((t) => typeof t?.tile === 'string') : [];
  const details = await Promise.all(tiles.map((t) => call(fetchFn, 'GET', `/partitions?tile=${q(t.tile)}`)));
  return assemble({ who, ov, comps, consents, binds, ledger, details });
}

// assemble(answers) → the model: {me, features, policies, credentials,
// notices, tiles, consents, binds, ledger, errors}. Pure: the tests feed it
// answers.
export function assemble({ who, ov, comps, consents, binds, ledger, details = [] }) {
  const errors = [];
  const w = obj(who?.body);
  const me = {
    id: typeof w.id === 'string' ? w.id : '', kind: w.kind || '', name: w.name || '', admin: !!w.admin,
    readOnly: !!w.readOnly, impersonatedBy: w.impersonatedBy || '',
    owned: new Set(arr(w.owned)),
    adminOrgs: new Set(arr(w.orgs).filter((o) => o?.admin).map((o) => o.id)),
  };
  if (!who?.ok) errors.push(`who you are: ${errorText(who)}`);
  if (!ov?.ok) errors.push(ov?.status === 404 ? 'this xbind has no partitioned tiles (GET /api/xbin/partitions: 404)' : `partitions: ${errorText(ov)}`);
  const o = obj(ov?.body);
  const rows = new Map(arr(comps?.body).map((c) => [c.path, c]));
  const tiles = arr(o.tiles).map((t, i) => tileModel(t, obj(details[i]?.ok ? details[i].body : null), rows.get(t.tile), me));
  const people = me.kind === 'user' && !me.impersonatedBy;
  return {
    me,
    people,
    features: new Set(arr(o.features)),
    policies: { partitionConsent: !!o.policies?.partitionConsent, credentialResetConfirm: !!o.policies?.credentialResetConfirm },
    credentials: arr(o.credentials),
    notices: arr(o.notices),
    tiles,
    consents: consents?.ok ? { policy: !!consents.body.policy?.partitionConsent, consents: arr(consents.body.consents), asked: arr(consents.body.asked) }
      : { error: errorText(consents), status: consents?.status ?? 0 },
    binds: binds?.ok ? arr(binds.body.binds).filter((b) => !!me.id && b?.user === me.id) : [],
    bindsError: binds?.ok ? '' : errorText(binds),
    ledger: ledger?.ok ? arr(ledger.body.rows) : [],
    errors,
  };
}

// tileModel(overview row, detail answer, components row, me) → one tile as
// the page shows it.
function tileModel(t, d, c, me) {
  const request = t.request && typeof t.request === 'object' ? t.request : null;
  const from = spec(t.spec), to = request ? spec(request.spec) : null;
  // the caller's own rows and binds only: an admin's answer carries every
  // person's (06 §12.1 — this is a person's page, not the admin section)
  const mineOnly = (r) => !!me.id && r?.user === me.id;
  const own = arr(d.partitions).filter(mineOnly);
  const owner = typeof c?.owner === 'string' ? c.owner : '';
  const note = typeof c?.partition?.note === 'string' ? c.partition.note.trim() : '';
  return {
    tile: t.tile, state: t.state || '', from, to, request, declined: !!request?.declined, since: request?.since || '',
    error: t.error || d.error || '', owner, note,
    manage: mayManage(me, t.tile, owner),
    mine: t.mine || null, rows: own, trust: d.trust || null, binds: arr(d.binds).filter(mineOnly), notices: arr(d.notices),
    consents: arr(d.consents), limits: d.limits || null, totals: d.totals || null, reviewedOnly: d.reviewedOnly || null,
  };
}

// mayManage(me, tile, owner) → whether the caller is one of the tile's
// managers, who decide its partition mode (docs/partitions.md §The mode):
// a workspace admin, the tile's owner, an admin of the org that owns it.
// The page offers the decision to them; xbind judges it again.
export function mayManage(me, tile, owner) {
  if (me?.impersonatedBy) return false; // view-as decides nothing
  if (me?.admin) return true;
  if (!me?.id) return false;
  if (owner === `user:${me.id}` || me.owned?.has?.(tile)) return true;
  return typeof owner === 'string' && owner.startsWith('org:') && !!me.adminOrgs?.has?.(owner.slice(4));
}

// ---- modes and the switch (xbind's words: registry.PartitionSpec,
// registry.SwitchDeletes) ----

export const spec = (x) => ({ user: !!x?.user, global: !!x?.global });

export function modeName(s) {
  if (s?.user && s?.global) return 'user + global';
  if (s?.user) return 'user';
  if (s?.global) return 'global';
  return 'unpartitioned';
}

export const DELETES_ALL = 'all data in this tile';

export function switchDeletes(from, to) {
  if (!!from?.user !== !!to?.user) return DELETES_ALL;
  if (from?.global && !to?.global) return 'the global instance\'s data and the tile\'s shared resources (people\'s partitions stay)';
  return 'nothing (the global instance starts empty)';
}

export const deletesNothing = (from, to) => switchDeletes(from, to).startsWith('nothing');
export const switchLabel = (from, to) => (switchDeletes(from, to) === DELETES_ALL ? 'Switch and delete all data' : 'Switch');

// decisions(model) → the tiles whose partition mode waits for (or was
// kept by) a manager the caller is: pending first, then declined requests
// (a manager may still switch those).
export function decisions(m) {
  const d = arr(m?.tiles).filter((t) => t.to && t.manage && (t.state === 'pending' || t.declined));
  return d.sort((a, b) => Number(a.declined) - Number(b.declined) || a.tile.localeCompare(b.tile));
}

// modeBody(tile, act, from, to, extra) → POST /partitions/mode's body: the
// request as the page showed it (xbind refuses a decision on another one).
export function modeBody(tile, act, from, to, extra = {}) {
  const z = (s) => (s && (s.user || s.global) ? { user: !!s.user, global: !!s.global } : null);
  return { tile, act, from: z(from), to: z(to), ...extra };
}

// whoDecides(owner) → who decides a tile, as xbind's in-frame page says it.
export function whoDecides(owner) {
  const o = typeof owner === 'string' ? owner : '';
  if (o.startsWith('org:')) return `an admin of ${o}, which owns it, or a workspace admin`;
  if (o.startsWith('user:')) return `its owner, ${o}, or a workspace admin`;
  return 'a workspace admin';
}

export const plural = (n, one, many = one + 's') => `${n} ${n === 1 ? one : many}`;

export function bytesText(n) {
  const b = Number(n) || 0;
  if (b < 1024) return plural(b, 'byte');
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = b / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

// wipedText(wiped) → a switch dry run's counts in words.
export function wipedText(w = {}) {
  const parts = [
    plural(w.namespaces || 0, 'data namespace'),
    plural(w.partitions || 0, 'person\'s partition', 'people\'s partitions'),
    plural(w.vaultKeys || 0, 'vault key'),
    plural(w.registrations || 0, 'registration') + ' (cron jobs, bus subscriptions, interface instances, ingress hosts)',
    bytesText(w.bytes || 0),
  ];
  const keys = w.subkeys ? `; ${plural(w.subkeys, 'backup key')} erased, so that data's backups can't be read any more` : '';
  return parts.join(', ') + keys;
}

// switchedText(tile, to, answer) → a switch's answer, from xbind's.
export function switchedText(tile, to, body = {}) {
  const deletes = String(body?.deletes || '');
  const lines = [deletes.startsWith('nothing') || !deletes
    ? `Switched ${tile} to ${modeName(to)}: nothing was deleted.`
    : `Switched ${tile} to ${modeName(to)}: ${deletes} deleted.`];
  if (body?.eraseError) lines.push(`The backup keys are erased, but not every key file is removed yet: ${body.eraseError}`);
  if (body?.archiver) lines.push(`Archiver: ${body.archiver}`);
  return lines.join('\n');
}

// ---- a person's own partition ----

// resetConfirm(tile, user) → what a reset or a restore asks the person to type.
export const resetConfirm = (tile, user) => `${tile} user:${user}`;

// rowState(row) → a person's partition's state in words.
export function rowState(r) {
  if (!r) return 'no partition yet — it starts on first use';
  if (r.state === 'dormant') return `dormant: ${r.why || 'nothing starts it'}`;
  if (r.state === 'orphaned') return `orphaned${r.why ? `: ${r.why}` : ''}`;
  if (r.running) {
    const up = Number(r.instance?.uptimeSec) || 0;
    return up ? `running for ${durationText(up)}` : 'running';
  }
  return 'stopped — the next request starts it';
}

export function durationText(sec) {
  const s = Math.max(0, Math.round(Number(sec) || 0));
  if (s < 90) return plural(s, 'second');
  if (s < 5400) return plural(Math.round(s / 60), 'minute');
  if (s < 129600) return plural(Math.round(s / 3600), 'hour');
  return plural(Math.round(s / 86400), 'day');
}

// usageText(row) → what a partition holds and keeps, in words (counts only).
export function usageText(r) {
  if (!r) return '';
  const g = obj(r.registrations);
  const parts = [bytesText(r.bytes || 0)];
  if (g.vaultKeys) parts.push(plural(g.vaultKeys, 'vault key'));
  if (g.cronJobs) parts.push(plural(g.cronJobs, 'cron job'));
  if (g.busSubscriptions) parts.push(plural(g.busSubscriptions, 'bus subscription'));
  if (g.ifaceInstances) parts.push(plural(g.ifaceInstances, 'interface instance'));
  if (g.ingressHosts) parts.push(plural(g.ingressHosts, 'ingress host'));
  if (g.missedTicks) parts.push(`${plural(g.missedTicks, 'missed tick')}`);
  if (g.dormantDrops) parts.push(`${plural(g.dormantDrops, 'dropped delivery', 'dropped deliveries')}`);
  return parts.join(' · ');
}

// mailText(mail) → an inbox's counts in words ('' for none).
export function mailText(m) {
  if (!m) return '';
  const parts = [];
  if (m.pending) parts.push(`${plural(m.pending, 'item')} waiting (${bytesText(m.bytes || 0)})`);
  if (m.expired) parts.push(`${m.expired} expired`);
  if (m.undeliverable) parts.push(`${m.undeliverable} undeliverable`);
  return parts.join(', ');
}

// ---- credentials an admin made (held) ----

export function credentialWhat(h) {
  switch (h?.kind) {
    case 'invite': return 'A sign-in link for your account';
    case 'password': return 'A new password for your account';
    case 'email': return h.email ? `The single sign-on email ${h.email} bound to your account` : 'A single sign-on email bound to your account';
    case 'sso-provider': return `A new single sign-on provider${h.issuer ? ` (${h.issuer})` : ''} for your account`;
    default: return `A credential (${h?.kind || 'unknown'}) for your account`;
  }
}

// credentialText(h, now) → who made it, when, and when it takes effect.
export function credentialText(h, now = Date.now()) {
  const by = h?.by ? `made by ${h.by}` : 'made by an admin';
  const at = h?.at ? ` at ${timeText(h.at)}` : '';
  const until = Date.parse(h?.until || '');
  const left = Number.isFinite(until) ? until - now : NaN;
  const when = !Number.isFinite(left) ? '' : left > 0
    ? ` It takes effect ${timeText(h.until)} (in ${durationText(left / 1000)}) unless you refuse it.`
    : ' Its 24 hours have passed: it takes effect now unless you refuse it.';
  return `${credentialWhat(h)}, ${by}${at}.${when}`;
}

// credentialNoun(h) → the credential as the Allow question names it.
export function credentialNoun(h) {
  switch (h?.kind) {
    case 'invite': return 'the sign-in link';
    case 'password': return 'the new password';
    case 'email': return h.email ? `the single sign-on email ${h.email}` : 'the single sign-on email';
    case 'sso-provider': return `the new single sign-on provider${h.issuer ? ` (${h.issuer})` : ''}`;
    default: return `the credential (${h?.kind || 'unknown'})`;
  }
}

// credentialAsk(h) → what Allow asks before it acts: allowing a credential
// someone else made is the act that could hand the account over, so it is
// the one confirmed (refusing costs at most a fresh one).
export function credentialAsk(h) {
  const by = h?.by || 'an admin', at = h?.at ? ` at ${timeText(h.at)}` : '';
  const effect = h?.kind === 'invite' ? 'Whoever opens it signs in as you'
    : h?.kind === 'password' ? 'Whoever knows it signs in as you'
      : h?.kind === 'email' ? 'Whoever signs in with it at the single sign-on provider signs in as you'
        : h?.kind === 'sso-provider' ? 'Your single sign-on moves to it' : 'It signs in as you';
  return `Allow ${credentialNoun(h)} ${by} made${at}? ${effect}: allow it only if you asked for it.`;
}

// credentialOutcome(h, answer) → a decision's answer, kept on the page after
// the credential leaves the list: {text, warn}. warn: the credential wasn't
// waiting any more (409 already-effective: used, replaced or expired; 404:
// it took effect unanswered) — xbind's own words, which say what to do.
// null: nothing was decided (a refusal of the act, xbind unreachable) — the
// card stays, with the error.
export function credentialOutcome(h, res) {
  const what = credentialWhat(h);
  if (res?.ok) {
    switch (res.body?.decision) {
      case 'allowed': return { text: `Allowed: ${lower(what)} works now.`, warn: false };
      case 'refused': return { text: `Refused: ${lower(what)} was revoked.`, warn: false };
      default: return { text: `Done: ${lower(what)}.`, warn: false };
    }
  }
  if (res?.status === 409 || res?.status === 404) {
    return { text: res.body?.error ? String(res.body.error)
      : `${what} was no longer waiting: if you didn't use it, change your password and sign out everywhere.`, warn: true };
  }
  return null;
}

const lower = (s) => s.charAt(0).toLowerCase() + s.slice(1);

// ---- consents and the ledger ----

// partitionedTiles(model) → the tiles with people's partitions the caller
// can read (consent and personal-bind pickers).
export const partitionedTiles = (m) => arr(m?.tiles).filter((t) => t.state === 'partitioned' && t.from.user).map((t) => t.tile);

// ledgerTotals(rows, tile?) → per (tile, kind, target) totals over the
// window, largest first.
export function ledgerTotals(rows, tile = '') {
  const sum = new Map();
  for (const r of arr(rows)) {
    if (tile && r.tile !== tile) continue;
    const k = `${r.tile}\u0000${r.kind}\u0000${r.target}`;
    const cur = sum.get(k) || { tile: r.tile, kind: r.kind, target: r.target, count: 0 };
    cur.count += Number(r.count) || 0;
    sum.set(k, cur);
  }
  return [...sum.values()].sort((a, b) => b.count - a.count || a.tile.localeCompare(b.tile) || a.target.localeCompare(b.target));
}

// edgesInto(rows, tile) → which tiles used the caller's data in tile (their
// partitions' calls into it), with counts.
export const edgesInto = (rows, tile) => ledgerTotals(arr(rows).filter((r) => r.kind === 'edge' && r.target === tile))
  .map((r) => ({ from: r.tile, count: r.count }));

export function ledgerKind(kind) {
  switch (kind) {
    case 'edge': return 'used your data in';
    case 'provider': return 'called';
    case 'bus': return 'bus events from';
    case 'trigger': return 'private trigger';
    default: return kind || '?';
  }
}

// ---- time ----

// timeText(iso) → a local time with its zone ("2026-09-30 16:08 CEST"):
// xbind's own texts (notices) say UTC, so the page names its zone too.
export function timeText(iso, zone = zoneName) {
  const t = Date.parse(iso || '');
  if (!Number.isFinite(t)) return '';
  const d = new Date(t);
  const p = (n) => String(n).padStart(2, '0');
  const z = zone(d);
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}${z ? ` ${z}` : ''}`;
}

// zoneName(date) → the browser's short zone name at date ("CEST", "UTC",
// "GMT+2"), or '' where Intl can't say.
export function zoneName(d) {
  try {
    return new Intl.DateTimeFormat(undefined, { timeZoneName: 'short' }).formatToParts(d).find((x) => x.type === 'timeZoneName')?.value || '';
  } catch {
    return '';
  }
}
