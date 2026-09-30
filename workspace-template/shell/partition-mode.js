// shell/partition-mode.js — partitioned tiles as the shell shows them
// (docs/partitions.md; the marker is PD-53, design A):
// what a /components row's `partition` says, the marker's words, the text
// of a pending tile's card, and a tile manager's decision through POST
// /api/xbin/partitions/mode — keep, or switch after a dry run and a typed
// confirmation. Pure and lit-free, so hack/partition-mode.test.mjs runs it
// in node; shell-kit.js draws the marker, bx-canvas.js the card overlay.
//
// Optional (docs/compat.md rule 3): a row without `partition` — every tile
// of an xbind older than partitioned tiles, and every tile that never
// asked for a mode — reads as null here, and the shell draws what it always
// drew.
//
// TODO(consent prompts): shown only while the partitionConsent policy is
// on, they need the partition consents API, which xbind doesn't serve yet;
// they belong beside the card overlay once it does.

// The marker's words.
export const MARK_TITLE = 'Partitioned: each person here has their own data';
export const MARK_GLOBAL = 'one shared global instance also runs, for what isn\'t a person\'s';
// What a switch between user partitions and unpartitioned deletes, in
// xbind's words (registry.SwitchDeletesAll).
export const DELETES_ALL = 'all data in this tile';

const spec = (x) => ({ user: !!x?.user, global: !!x?.global });

// partitionView(c) → null, or what c's row says: its state, its recorded
// mode (from: user, global), the mode its code asks for (to, null without a
// request), and whether that request waits for a manager (pending) or was
// declined.
export function partitionView(c) {
  const p = c?.partition;
  if (!p || typeof p !== 'object') return null;
  const q = p.request && typeof p.request === 'object' ? p.request : null;
  const from = spec(p), to = q ? spec(q) : null;
  const state = typeof p.state === 'string' ? p.state : '';
  return { state, user: from.user, global: from.global, from, to, pending: state === 'pending' && !!to, declined: !!q?.declined };
}

// requestKey(view) → a request's identity (R → Q): what a card's decision
// state is kept under, so a new request never shows an old one's answer.
export const requestKey = (v) => (v ? `${modeName(v.from)}→${modeName(v.to)}` : '');

// markTitle(view) → the marker's tooltip, '' when the tile has no user
// partitions (no marker). The marker follows the recorded mode: a pending
// switch doesn't change it (the overlay and the banner carry that).
export function markTitle(v) {
  if (!v?.user) return '';
  return v.global ? `${MARK_TITLE}; ${MARK_GLOBAL}` : MARK_TITLE;
}

// modeName(spec) → the mode as xbind names it (registry.PartitionSpec.String).
export function modeName(s) {
  if (s?.user && s?.global) return 'user + global';
  if (s?.user) return 'user';
  if (s?.global) return 'global';
  return 'unpartitioned';
}

// switchDeletes(from, to) → what the switch deletes (registry.SwitchDeletes,
// owner ruling H1): everything between user partitions and unpartitioned,
// the global instance's data when "global" goes, nothing when it comes.
export function switchDeletes(from, to) {
  if (!!from?.user !== !!to?.user) return DELETES_ALL;
  if (from?.global && !to?.global) return 'the global instance\'s data and the tile\'s shared resources (people\'s partitions stay)';
  return 'nothing (the global instance starts empty)';
}

// switchLabel(from, to) → the Switch button's words.
export const switchLabel = (from, to) => (switchDeletes(from, to) === DELETES_ALL ? 'Switch and delete all data' : 'Switch');

// pendingText(path, view, alerts) → the card's words for a pending tile: the
// /alerts row's message (xbind's words, as the banner shows them) when there
// is one, else the same facts from the row.
export function pendingText(path, v, alerts) {
  const a = (Array.isArray(alerts) ? alerts : []).find((x) => x?.kind === 'partition-switch' && x?.tile === path);
  if (a?.message) return String(a.message);
  return `A partition mode switch is requested for ${path} (${modeName(v?.from)} → ${modeName(v?.to)}): switching deletes `
    + `${switchDeletes(v?.from, v?.to)}. Until a manager of ${path} switches or keeps the current mode, it doesn't run.`;
}

// Who decides (docs/partitions.md §The mode), for people who can't.
export const DECIDERS = 'the tile\'s owner, an admin of its owning org, or a workspace admin';

// modeBody(path, act, view, extra) → POST /partitions/mode's body: the
// request as the row showed it (xbind refuses a decision on another one).
export function modeBody(path, act, v, extra = {}) {
  const z = (s) => (s && (s.user || s.global) ? { user: !!s.user, global: !!s.global } : null);
  return { tile: path, act, from: z(v?.from), to: z(v?.to), ...extra };
}

// postMode(fetchFn, body) → {ok, status, body}: the call as the signed-in
// person (a raw fetch: xbin.fetch would act as the shell tile, which never
// decides). A network failure is status 0.
export async function postMode(fetchFn, body) {
  let r;
  try {
    r = await fetchFn('/api/xbin/partitions/mode', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  } catch (e) {
    return { ok: false, status: 0, body: { error: `xbind can't be reached (${e?.message || e}): try again` } };
  }
  const b = await r.json().catch(() => ({}));
  return { ok: r.ok, status: r.status, body: b && typeof b === 'object' ? b : {} };
}

// errorText(answer) → a refusal's words.
export const errorText = (res) => String(res?.body?.error || `failed (${res?.status ?? '?'})`);

const plural = (n, one, many = one + 's') => `${n} ${n === 1 ? one : many}`;
export function bytesText(n) {
  const b = Number(n) || 0;
  if (b < 1024) return plural(b, 'byte');
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = b / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

// wipedText(wiped) → the dry run's counts in words.
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

// switchSpec(path, view, dry, {typed, error, yes}) → the <bx-dialog> spec of
// the typed confirmation, from the dry run's answer (dry: what it deletes,
// the counts, what it keeps, the sandbox managers that don't keep people
// apart). The confirm field must hold the tile's path.
export function switchSpec(path, v, dry = {}, { typed = '', error = '', yes = false } = {}) {
  const from = v?.from, to = v?.to;
  const lines = [
    `Switching ${path} from ${modeName(from)} to ${modeName(to)} deletes ${dry.deletes || switchDeletes(from, to)}.`,
    '',
    `It deletes: ${wipedText(dry.wiped)}.`,
  ];
  if (dry.people) lines.push(`${plural(dry.people, 'person', 'people')} whose partition is deleted will be told.`);
  const keeps = Array.isArray(dry.keeps) ? dry.keeps : [];
  if (keeps.length) lines.push('', 'It keeps:', ...keeps.map((k) => `• ${k}`));
  const managers = Array.isArray(dry.managers) ? dry.managers : [];
  if (managers.length) {
    lines.push('', `These sandbox managers don't keep people apart (their hello lacks "partitions"): ${managers.join(', ')} — `
      + 'each person\'s partition would see every person\'s sandboxes there. Update them, or switch anyway.');
  }
  lines.push('', 'This can\'t be undone.');
  const fields = [{ name: 'confirm', label: `Type ${path} to confirm`, placeholder: path, value: typed }];
  if (managers.length) fields.push({ name: 'yes', type: 'checkbox', label: 'Switch anyway', value: yes });
  return {
    title: `Switch ${path}'s partition mode?`,
    message: lines.join('\n'),
    error: error || undefined,
    fields,
    buttons: [{ label: 'Cancel', value: null }, { label: switchLabel(from, to), value: 'switch', danger: true }],
  };
}

// switchResolve(path, detail, dry) → what the dialog's answer asks for:
// {cancel} (dismissed), {again: error} (the path typed wrong, or managers
// not acknowledged), or {extra} for the switch's body.
export function switchResolve(path, detail, dry = {}) {
  if (detail?.button !== 'switch') return { cancel: true };
  const typed = String(detail?.values?.confirm ?? '').trim();
  const yes = !!detail?.values?.yes;
  if (typed !== path) return { again: `Type the tile's path exactly (${path}) to switch.`, typed, yes };
  if ((dry.managers?.length ?? 0) > 0 && !yes) return { again: 'Tick "Switch anyway" to switch with those sandbox managers bound.', typed, yes };
  return { extra: yes ? { confirm: path, yes: true } : { confirm: path }, typed, yes };
}
