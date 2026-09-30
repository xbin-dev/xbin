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
// on, they need the partition consents API, which this xbind doesn't serve
// yet; they belong beside the card overlay once it does.

// The marker's words.
export const MARK_TITLE = 'Partitioned: each person here has their own data';
export const MARK_GLOBAL = 'one shared global instance also runs, for what isn\'t a person\'s';
// What a switch between user partitions and unpartitioned deletes, in
// xbind's words (registry.SwitchDeletesAll).
export const DELETES_ALL = 'all data in this tile';

const spec = (x) => ({ user: !!x?.user, global: !!x?.global });

// partitionView(c) → null, or what c's row says: its state, its recorded
// mode (from: user, global), the mode its code asks for (to, null without a
// request), whether that request waits for a manager (pending) or was
// declined, and the tile's partitionNote while it waits (note: the tile's
// own words — sandbox-writable, so shown as text and attributed to it).
export function partitionView(c) {
  const p = c?.partition;
  if (!p || typeof p !== 'object') return null;
  const q = p.request && typeof p.request === 'object' ? p.request : null;
  const from = spec(p), to = q ? spec(q) : null;
  const state = typeof p.state === 'string' ? p.state : '';
  const note = typeof p.note === 'string' ? p.note.trim() : '';
  return { state, user: from.user, global: from.global, from, to, pending: state === 'pending' && !!to, declined: !!q?.declined, note };
}

// requestKey(view) → a request's R → Q: what a card's decision state is kept
// under while the request stays pending. pruneDecisions drops it once the
// row stops showing that request pending, so a request that closes and
// reopens with the same R → Q starts over.
export const requestKey = (v) => (v ? `${modeName(v.from)}→${modeName(v.to)}` : '');

// pruneDecisions(part, rowOf) → the cards' decision states (path → {key, …})
// without those whose row no longer shows that request pending — decided,
// withdrawn, changed or gone. The same object when nothing goes.
export function pruneDecisions(part, rowOf) {
  let out = part;
  for (const [path, st] of Object.entries(part ?? {})) {
    const v = partitionView(rowOf(path));
    if (v?.pending && st?.key === requestKey(v)) continue;
    if (out === part) out = { ...part };
    delete out[path];
  }
  return out;
}

// A window showing a non-primary deployment of a partitioned tile carries
// no marker: a deployment never runs people's partitions — its one instance
// is shared by the tile's writers (01 §2.8). Its head shows a "shared" chip
// instead (depShared(view): the tile's recorded mode has user partitions),
// with DEP_SHARED as the tooltip.
export const DEP_SHARED = 'Not partitioned: a deployment runs one instance that every writer of the tile shares '
  + '(its global instance, when its code asks for partitions), not each person\'s own partition';
export const depShared = (v) => !!v?.user;

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
// is one for this request, else the same facts from the row. The message's
// `bx partition switch|keep` hint is left out: the card has the buttons, and
// says who decides.
export function pendingText(path, v, alerts) {
  const rq = `(${modeName(v?.from)} → ${modeName(v?.to)})`;
  const a = (Array.isArray(alerts) ? alerts : []).find((x) => x?.kind === 'partition-switch' && x?.tile === path
    && typeof x?.message === 'string' && x.message.includes(rq));
  if (a) return a.message.replace(` (bx partition switch|keep ${path})`, '');
  return `A partition mode switch is requested for ${path} ${rq}: switching deletes `
    + `${switchDeletes(v?.from, v?.to)}. Until a manager of ${path} switches or keeps the current mode, it doesn't run.`;
}

// whoDecides(owner) → who decides this tile, from its row's owner
// ("user:<id>" | "org:<id>" | ""), as xbind's in-frame page names them.
export function whoDecides(owner) {
  const o = typeof owner === 'string' ? owner : '';
  if (o.startsWith('org:')) return `an admin of ${o}, which owns it, or a workspace admin`;
  if (o.startsWith('user:')) return `its owner, ${o}, or a workspace admin`;
  return 'a workspace admin';
}

// noteText(path, view) → the tile's partitionNote attributed to it ('' when
// it has none), as xbind's in-frame page says it.
export const noteText = (path, v) => (v?.note ? `${path} says: ${v.note}` : '');

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

// staleRefusal(answer) → whether a refused switch is one the typed
// confirmation can't fix: a 409 other than the sandbox managers' (C12) —
// the request changed or was decided meanwhile, a switch already runs, or
// the data went but the mode wasn't recorded. The card closes the dialog,
// shows why and looks again; the managers' 409 and a part-way failure (500)
// re-open the dialog.
export const staleRefusal = (res) => res?.status === 409 && !Array.isArray(res?.body?.managers);

// deletesNothing(from, to) → whether the switch deletes nothing ("global"
// comes, H1).
export const deletesNothing = (from, to) => switchDeletes(from, to).startsWith('nothing');

// keptText(path, view) → a Keep's answer on the card.
export const keptText = (path, v) => `Kept ${modeName(v?.from)}: ${path} runs again, and nothing was deleted.`;

// switchedText(path, view, body) → a switch's answer on the card, from
// xbind's: what it deleted, an erase that left key files behind, and the
// archiver's note (what bx prints).
export function switchedText(path, v, body = {}) {
  const deletes = String(body?.deletes || switchDeletes(v?.from, v?.to));
  const lines = [deletes.startsWith('nothing')
    ? `Switched ${path} to ${modeName(v?.to)}: nothing was deleted (the global instance starts empty).`
    : `Switched ${path} to ${modeName(v?.to)}: ${deletes} deleted.`];
  if (body?.eraseError) lines.push(`The backup keys are erased, but not every key file is removed yet: ${body.eraseError}`);
  if (body?.archiver) lines.push(`Archiver: ${body.archiver}`);
  return lines.join('\n');
}

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
// apart), with the tile's partitionNote attributed to it. The confirm field
// must hold the tile's path. A switch that deletes nothing ("global" comes)
// shows no counts, says so, and its button isn't a danger one.
export function switchSpec(path, v, dry = {}, { typed = '', error = '', yes = false } = {}) {
  const from = v?.from, to = v?.to, none = deletesNothing(from, to);
  const lines = [`Switching ${path} from ${modeName(from)} to ${modeName(to)} deletes ${dry.deletes || switchDeletes(from, to)}.`];
  if (v?.note) lines.push('', noteText(path, v));
  if (!none) lines.push('', `It deletes: ${wipedText(dry.wiped)}.`);
  if (dry.people) lines.push(`${plural(dry.people, 'person', 'people')} whose partition is deleted will be told.`);
  const keeps = Array.isArray(dry.keeps) ? dry.keeps : [];
  if (keeps.length) lines.push('', 'It keeps:', ...keeps.map((k) => `• ${k}`));
  const managers = Array.isArray(dry.managers) ? dry.managers : [];
  if (managers.length) {
    lines.push('', `These sandbox managers don't keep people apart (their hello lacks "partitions"): ${managers.join(', ')} — `
      + 'each person\'s partition would see every person\'s sandboxes there. Update them, or switch anyway.');
  }
  lines.push('', none ? 'Nothing is deleted: the global instance starts empty.' :'This can\'t be undone.');
  const fields = [{ name: 'confirm', label: `Type ${path} to confirm`, placeholder: path, value: typed }];
  if (managers.length) fields.push({ name: 'yes', type: 'checkbox', label: 'Switch anyway', value: yes });
  return {
    title: `Switch ${path}'s partition mode?`,
    message: lines.join('\n'),
    error: error || undefined,
    fields,
    buttons: [{ label: 'Cancel', value: null }, { label: switchLabel(from, to), value: 'switch', danger: !none }],
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
