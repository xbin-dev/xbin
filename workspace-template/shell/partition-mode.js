// shell/partition-mode.js — partitioned tiles as the shell shows them
// (docs/partitions.md; the marker is PD-53, design A):
// what a /components row's `partition` says, the marker's words, the
// partition chip a window's head carries (whose partition it shows), the
// text of a pending tile's card, a tile manager's decision through POST
// /api/xbin/partitions/mode — keep, or switch after a dry run and a typed
// confirmation — the consent prompts' words and calls, and when the
// settings menu links the partitions page (the end of this file). Pure and
// lit-free, so hack/partition-mode.test.mjs runs it in node; shell-kit.js
// draws the marker, bx-canvas.js the chip and the card overlay,
// bx-part-consent.js the prompts, shell-account.js the menu entry.
//
// Optional (docs/compat.md rule 3): a row without `partition` — every tile
// of an xbind older than partitioned tiles, and every tile that never
// asked for a mode — reads as null here, and the shell draws what it always
// drew.

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
// is shared by the tile's writers (01 §2.8). Its head's partition chip
// (partitionChip below) says `shared`, with DEP_SHARED as the tooltip.
export const DEP_SHARED = 'Not partitioned: a deployment runs one instance that every writer of the tile shares '
  + '(its global instance, when its code asks for partitions), not each person\'s own partition';

// The partition chip (owner ruling I3): every window of a tile whose
// recorded mode has user partitions says in its head, after the path,
// whose partition it shows — `yours`: the viewer's own (a person on the
// primary); `shared`: a non-primary deployment's one instance (DEP_SHARED);
// `global`: the tile's global instance, which a window with no person
// behind it reaches (the workspace token, --no-auth). A window that reaches
// no partition says so: view-as never opens a person's partition, and the
// workspace token has none of its own on a tile without a global instance
// (docs/partitions.md §Who reaches which partition).
export const CHIP_YOURS = 'Your partition: this window shows your own data in this tile — everyone who uses it has their own, '
  + 'and nobody else\'s shows here';
export const CHIP_GLOBAL = 'The global instance: this window shows the tile\'s one instance for what isn\'t a person\'s, '
  + 'not anyone\'s partition (the workspace token has none of its own)';
export const CHIP_NONE_ROOT = 'No partition: the workspace token has none of its own, and this tile has no global instance, '
  + 'so it refuses this window\'s calls — sign in as a person to use it';
export const chipNoneViewAs = (id) => `No partition: viewing the workspace as ${id || 'someone'} never opens their partition, `
  + 'so this tile refuses this window\'s calls';

// partitionChip(view, {shown, who}) → null, or {kind, text, title}: the
// chip of a window on the tile view reads (partitionView) that shows
// deployment `shown` ('' the primary), for the viewer `who` (/whoami: kind,
// id, impersonatedBy). Nothing on a tile without user partitions, nor on
// the primary while who is unknown — never a guess.
export function partitionChip(v, { shown = '', who = null } = {}) {
  if (!v?.user) return null;
  if (who?.impersonatedBy) return { kind: 'none', text: 'no partition', title: chipNoneViewAs(who.id) };
  if (shown) return { kind: 'shared', text: 'shared', title: DEP_SHARED };
  if (who?.kind === 'user') return { kind: 'yours', text: 'yours', title: CHIP_YOURS };
  if (who?.kind !== 'root') return null;
  return v.global ? { kind: 'global', text: 'global', title: CHIP_GLOBAL } : { kind: 'none', text: 'no partition', title: CHIP_NONE_ROOT };
}

// framedTile(src, components) → {row, shown}: what a window framing src
// (a tile's pop-out, xbin.window: a sub-path of the calling tile, or
// spec.src) shows — the listed tile src is, or lies under (a sub-path is
// its tile's own page, in the same partition as its card; the longest
// listed path wins), and the deployment a `<tile>+<name>` src names ('' the
// primary). row null for a src under no listed tile: no marker, no chip.
export function framedTile(src, components) {
  const s = typeof src === 'string' ? src : '';
  let row = null;
  for (const c of Array.isArray(components) ? components : []) {
    const p = c?.path;
    if (typeof p !== 'string' || !p || !s.startsWith(p) || (row && row.path.length >= p.length)) continue;
    if (s.length === p.length || s[p.length] === '/' || s[p.length] === '+') row = c;
  }
  const rest = row ? s.slice(row.path.length) : '';
  return { row, shown: rest.startsWith('+') ? rest.slice(1).split('/')[0] : '' };
}

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
// hint — the parenthesis from `(bx partition switch|keep <tile>` to its
// close, whatever else it names (the partitions page) — is left out: the
// card has the buttons, and says who decides.
export function pendingText(path, v, alerts) {
  const rq = `(${modeName(v?.from)} → ${modeName(v?.to)})`;
  const a = (Array.isArray(alerts) ? alerts : []).find((x) => x?.kind === 'partition-switch' && x?.tile === path
    && typeof x?.message === 'string' && x.message.includes(rq));
  if (a) return stripHint(a.message, ` (bx partition switch|keep ${path}`);
  return `A partition mode switch is requested for ${path} ${rq}: switching deletes `
    + `${switchDeletes(v?.from, v?.to)}. Until a manager of ${path} switches or keeps the current mode, it doesn't run.`;
}
// stripHint(msg, head) → msg without the parenthesis that starts with head
// (through its closing ')'); msg itself when it has none.
function stripHint(msg, head) {
  const i = msg.indexOf(head), j = i < 0 ? -1 : msg.indexOf(')', i + head.length);
  return j < 0 ? msg : msg.slice(0, i) + msg.slice(j + 1);
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
export const postMode = (fetchFn, body) => personCall(fetchFn, '/api/xbin/partitions/mode', 'POST', body);

// personCall(fetchFn, url, method, body) → {ok, status, body}: one JSON
// call of the partitions API as the signed-in person (no body for GET).
async function personCall(fetchFn, url, method, body) {
  let r;
  try {
    r = await fetchFn(url, body === undefined ? { method }
      : { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
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

// ---- consent prompts (docs/partitions.md §Calls between partitioned tiles) ----
// With the workspace policy partitionConsent on, a partitioned tile's call
// into the same person's partition of another partitioned tile needs that
// person's consent: xbind refuses it ("<id> hasn't let <from> use their
// <to> data") and asks them — a `partitions` event, op consent-needed
// {from, to}, to their own sockets only (the page's /ws/events socket,
// /vendor/events-socket.js, is the signed-in person's), at most once a day
// per edge, and only for an edge an admin approved. The shell shows the
// asks GET /partitions/consents lists (`asked`) and answers as the person:
// a raw fetch, since xbin.fetch would act as the shell tile, and xbind
// takes consents from a person's own sign-in only (PersonOnly). Allow
// POSTs {from, to}; Don't allow stores nothing — the calls stay refused,
// and this browser stops showing that ask (xbind asks again, at most once
// a day, when the tile tries again).
export const CONSENTS_API = '/api/xbin/partitions/consents';
// PARTITIONS_PAGE: the person's partitions page (xbind's own, top-level:
// it refuses frames), where a consent is taken back — the Allow answer
// names and links it; '' would name the bx command instead.
export const PARTITIONS_PAGE = '/xbin/partitions';
export const CONSENT_HEAD = 'Partitioned tiles ask for your data';
export const CONSENT_REGION = 'Partitioned tiles\' consent prompts';
export const CONSENT_DENY_TITLE = 'Nothing is stored: its calls stay refused, and xbind asks again, at most once a day, when it tries again';

export const consentKey = (e) => `${e?.from ?? ''}→${e?.to ?? ''}`;
export const consentAsk = (from, to) => `${from} asks to use your data in ${to}`;
export const consentWhy = (from, to) => `Your workspace asks you first. Allowing lets ${from}'s code — and everyone who can `
  + `change it — reach your ${to} data whenever it acts for you. You can take it back at any time.`;
export const consentAllowTitle = (from, to) => `let ${from} use your ${to} data from its next call`;
const revokeHint = (from, to) => (PARTITIONS_PAGE ? `on your partitions page (${PARTITIONS_PAGE})` : `with bx partition consent ${from} ${to} --revoke`);
export const allowedText = (from, to) => `Allowed: ${from} can use your ${to} data from its next call. Take it back ${revokeHint(from, to)}.`;
export const declinedText = (from, to) => `Not allowed: ${from}'s calls into your ${to} data stay refused. `
  + 'xbind asks again, at most once a day, when it tries again.';

// consentPerson(who) → whether /whoami is a signed-in person — not view-as,
// not the workspace token: the only viewer xbind has consents for
// (PersonOnly), so the only one the shell ever asks about them.
export const consentPerson = (who) => who?.kind === 'user' && !who.impersonatedBy && !!who.id;

// consentWatch(components, who) → whether the shell reads the person's
// consents at all: a signed-in person who sees a partitioned tile. A
// consent event makes it look anyway (for a person).
export function consentWatch(components, who) {
  return consentPerson(who) && anyPartitioned(components);
}

// anyPartitioned(components) → whether a /components listing holds a
// partitioned tile: one whose recorded mode has user partitions (the
// marker's rule — a pending switch into partitions isn't one yet).
export const anyPartitioned = (components) => (Array.isArray(components) ? components : []).some((c) => partitionView(c)?.user);

// consentStoreKey(who) → the localStorage key this browser keeps the
// person's dismissed asks under: one per person, so on a shared browser
// nobody's dismissals show, hide or prune anybody else's; '' for anyone
// but a signed-in person (nothing kept).
export const consentStoreKey = (who) => (consentPerson(who) ? `xbin-partition-consent-dismissed:${who.id}` : '');

// coverPoints(rect, step) → the points an ask is hit-tested at before its
// Allow counts (bx-part-consent: nothing may be drawn over any of them — a
// tile's pop-out window could hide the question and leave Allow in view):
// a grid every `step` px across rect, 1px inside, its far edges included.
export function coverPoints(r, step = 24) {
  const axis = (a, b) => {
    const out = [];
    for (let v = a + 1; v < b - 1; v += step) out.push(v);
    out.push(Math.max(a + 1, b - 1));
    return out;
  };
  const xs = axis(r.left, r.right), ys = axis(r.top, r.bottom);
  return xs.flatMap((x) => ys.map((y) => [x, y]));
}
export const CONSENT_COVERED = 'This question was just shown, covered or partly out of view: '
  + 'make sure you can read all of it, then answer again.';

// consentPrompts(view, {dismissed, paths}) → the asks to show, from GET
// /partitions/consents' answer: none unless the policy is on; each asked
// edge once — unless this browser dismissed that very ask (dismissed[key]
// is its `at`) — and, given paths (the tiles the shell knows), only while
// both tiles are still there (xbind drops an ask naming a tile that went;
// the shell's list may be a moment behind or ahead of xbind's).
export function consentPrompts(view, { dismissed = {}, paths = null } = {}) {
  if (!view?.policy?.partitionConsent) return [];
  const seen = new Set(), out = [];
  for (const a of Array.isArray(view.asked) ? view.asked : []) {
    if (typeof a?.from !== 'string' || typeof a?.to !== 'string' || !a.from || !a.to) continue;
    const key = consentKey(a), at = String(a.at ?? '');
    if (seen.has(key) || dismissed?.[key] === at || (paths && !(paths.has(a.from) && paths.has(a.to)))) continue;
    seen.add(key);
    out.push({ key, from: a.from, to: a.to, at });
  }
  return out;
}

// keepDismissed(dismissed, view) → the dismissals that still name an ask
// the view lists (the others expired or were answered): the same object
// when none goes.
export function keepDismissed(dismissed, view) {
  const live = new Set((Array.isArray(view?.asked) ? view.asked : []).map((a) => `${consentKey(a)}\n${a?.at ?? ''}`));
  let out = dismissed;
  for (const [k, at] of Object.entries(dismissed ?? {})) {
    if (live.has(`${k}\n${at}`)) continue;
    if (out === dismissed) out = { ...dismissed };
    delete out[k];
  }
  return out;
}

// consentEventOp(e) → a /ws/events frame's consent op ('consent-needed',
// 'consent'), else ''.
export const consentEventOp = (e) => (e?.type === 'partitions' && ['consent-needed', 'consent'].includes(e?.data?.op) ? e.data.op : '');

// consentCall(fetchFn, method, edge) → {ok, status, body}: GET the
// person's consents view, POST an edge (allow) or DELETE it (take back).
export const consentCall = (fetchFn, method = 'GET', edge) => personCall(fetchFn, CONSENTS_API, method,
  method === 'GET' ? undefined : { from: edge?.from, to: edge?.to });

// ---- the settings menu's "your partitions" entry (owner ruling I13) ----
// pageEntry(components, who) → whether the account block of the shell's
// settings menu links the person's partitions page (PARTITIONS_PAGE): a
// signed-in person who sees a partitioned tile. Not the workspace token (no
// partitions of its own; a paused card's "details…" still links the page
// for a switch it decides) nor an admin viewing as someone (the page shows
// view-as none of that person's partitions). Nothing on a workspace without
// a partitioned tile, and nothing on an xbind that doesn't partition.
export const pageEntry = (components, who) => consentPerson(who) && anyPartitioned(components);
export const PAGE_ENTRY = 'your partitions';
export const PAGE_ENTRY_TITLE = 'Your partitions page: your own data in each partitioned tile, the consents and personal binds '
  + 'you gave, and the switches you decide — xbind\'s page, in a new tab';
