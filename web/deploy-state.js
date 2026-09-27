// web/deploy-state.js — the terminal window's view of a tile's live reload
// state, and every string it shows. Pure functions from the deployments
// state (`GET /api/xbin/deployments?tile=`, docs/protocol.md) and the
// viewer's permissions (the state's `caller`, plus who a view-as session
// views) to what the window draws: the title-bar chip, the Reload now offer,
// the chip's menu, the launcher's banner, the frame chip, the tile API
// select's entries; and the words of confirmations, results, refusals and
// the grey terminal lines.
//
// The server decides; this module renders. Who may do what is the state's
// `can` and `why` (a control the viewer may not use is disabled with that
// reason, never hidden), and what an operation will do is a dry run's
// `impact`: a fact the impact doesn't carry is left out, never guessed.
//
// Two inputs mean "draw nothing new": a state of null (this xbind has no
// tile deployments: the route answered a plain 404 or 405), and the zero
// state (`record: false`), which gets exactly one unobtrusive entry point
// (`entry`) and today's two tile API options, byte for byte.
//
// Imports nothing and touches no DOM, so hack/deploy-state.test.mjs runs it
// under node (`make js-test`); web/frame-deploy.js does the fetching, the
// events and the dialogs.

export const GLYPH = Object.freeze({ attached: '●', pinned: '📌', reloadNow: '⇡', layout: '⇈' });

// The labels a view model's menu items carry: the frame's test names find an
// item by them (a submenu item as "<parent>/<name>").
export const LABEL = Object.freeze({
  header: 'Live reload',
  pause: 'Pause live reload',
  resume: 'Resume live reload on ▸',
  attach: 'Attach live reload to ▸',
  reloadNow: '⇡ Reload now',
  deployments: '⇈ Deployments…',
});

// What a refusal dialog calls each operation ("Reload now was refused").
const OP_NAME = { pause: 'Pause live reload', resume: 'Resume live reload', reloadNow: 'Reload now', attach: 'Attach live reload' };

// The deploy log's `how` values that move code: a terminal says where the
// code now is. Pause, resume and attach are told by the record's line.
const CODE_MOVES = new Set(['deploy', 'promote', 'rollback', 'reload-now']);

const MINUS = '−';
const LAYOUT_TIP = 'deployments — live reload, deploy, promote, roll back';

// ---- small facts ----

// who(by) → how a deploy entry's `by` reads: "user:ana" → "ana".
export function who(by) {
  if (!by) return '';
  if (by === 'owner') return 'the owner';
  return String(by).replace(/^user:/, '');
}

// ago(at, now) → "12m ago", from an RFC 3339 time ('' when unreadable).
export function ago(at, now = Date.now()) {
  const t = Date.parse(at);
  if (!Number.isFinite(t)) return '';
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

const files = (n) => (n === 1 ? '1 file' : `${n} files`);
const ships = (n) => (n === 1 ? 'ships' : 'ship');
const stat = (c) => `+${c.added | 0} ${MINUS}${c.removed | 0}`;
// a name on the degraded bar: at most 10 characters, the tooltip has the rest
const cut = (name) => (name.length > 10 ? name.slice(0, 9) + '…' : name);
const dep = (s, name) => s?.deployments?.find((d) => d.name === name) || null;
// the checkpoint a deployment is pinned to ('' while it follows the work tree)
const cp = (s, name) => dep(s, name)?.checkpoint?.id || '';
// what a deployment serves now: its generation's checkpoint, else its record's
function serving(s, name) {
  const v = dep(s, name)?.status?.serving;
  if (v && v !== 'work-tree') return v;
  if (v === 'work-tree') return 'its current code';
  return cp(s, name) || 'its current code';
}

// facts(state) → the few things every surface derives from the state.
function facts(s) {
  const zero = !s || !s.record;
  const reader = !zero && s.view === 'reader';
  const primary = s?.primary || 'main';
  // the live reload target; '' = paused (a reader's '' may also mean saves
  // reach another deployment: a reader can't tell, and is shown neither)
  const attached = zero ? primary : (s.liveReload || '');
  const paused = !zero && !reader && attached === '';
  const last = zero ? primary : (s.lastLiveReload || attached || primary);
  const wt = s?.workTree;
  const changed = paused && wt && Number.isFinite(wt.changed) ? wt.changed : null;
  const since = (paused && wt?.since) || cp(s, last);
  // a deployment whose last deploy failed: the last target first, then the primary
  let failed = '';
  if (!zero) {
    const bad = (s.deployments || []).filter((d) => d.lastDeploy?.result === 'failed').map((d) => d.name);
    failed = bad.includes(last) ? last : bad.includes(primary) ? primary : (bad[0] || '');
  }
  // why live reload is paused: the last target's last deploy says
  const how = paused ? dep(s, last)?.lastDeploy?.how || '' : '';
  const cause = how === 'protect' ? 'protect' : ['deploy', 'promote', 'rollback'].includes(how) ? how : 'pause';
  return { zero, reader, primary, attached, paused, last, changed, since, failed, cause };
}

// ---- permissions ----

const viewAsWhy = (name) => `${name || 'this user'} may do this — you are viewing as ${name || 'another user'} (read-only).`;

// control(state, op, deployment, opts) → {enabled, why, kind} for one
// operation: the tile-level `caller.can[op]`, or `deployments[i].can[op]`
// when a deployment is named. What the tile itself may not do (`allowed`:
// isolation, chrome, a cap) wins, since nobody may; a view-as session gets
// every action disabled, with "<user> may do this" where the viewed user
// may (opts.viewing names them).
export function control(s, op, name, opts = {}) {
  const allowed = op === 'pause' ? s?.allowed?.pause : op === 'add' ? s?.allowed?.deployments : null;
  if (allowed && allowed.ok === false) return { enabled: false, why: allowed.why || '', kind: allowed.kind || 'policy' };
  const can = name ? dep(s, name)?.can?.[op] : s?.caller?.can?.[op];
  if (!can || can.ok !== true) return { enabled: false, why: can?.why || '', kind: can?.kind || '' };
  if (s?.caller?.readOnly) return { enabled: false, why: viewAsWhy(opts.viewing), kind: 'authority' };
  return { enabled: true, why: '', kind: '' };
}

// Reload now adds the one reason that isn't a refusal: nothing changed since
// the last target's checkpoint (unless its last deploy failed: then it retries).
function reloadNowControl(s, f, opts) {
  const c = control(s, 'reloadNow', null, opts);
  if (c.enabled && f.changed === 0 && f.failed !== f.last) return { enabled: false, why: `No changes since ${f.since}.`, kind: 'state' };
  return c;
}

// ---- the chip ----

function pausedSentence(s, f, opts) {
  const L = f.last;
  const pin = cp(s, L);
  const entry = dep(s, L)?.lastDeploy;
  if (f.cause === 'protect') {
    const by = who(entry?.by || s.liveReloadSince?.by);
    return `Live reload paused when ${by || 'a tile manager'} protected ${L} — ${L} is pinned to ${pin}.`;
  }
  if (f.cause === 'rollback') {
    return `Live reload paused — ${L} was rolled back to ${pin}. The work tree still holds the code you rolled back from: resuming ships it again.`;
  }
  if (f.cause === 'promote' || f.cause === 'deploy') {
    const from = f.cause === 'promote' && entry?.from ? ` from ${entry.from}` : '';
    return `Live reload paused — ${L} received ${pin}${from}. Resuming ships the work tree to ${L}.`;
  }
  const since = s.liveReloadSince;
  const by = since ? who(since.by) : '';
  const when = since ? ago(since.at, opts.now) : '';
  const head = `Live reload paused${by ? ` by ${by}${since.agent ? ' (agent)' : ''}` : ''}${when ? ` ${when}` : ''}`;
  if (f.changed > 0) return `${head} — ${files(f.changed)} changed since ${f.since}, which ${L} runs. Reload now ships them to ${L} once.`;
  return `${head} — no changes since ${f.since}.`;
}

function failedSentence(s, f) {
  const D = f.failed;
  const retry = f.paused && D === f.last ? ' Reload now retries.' : '';
  return `The last deploy to ${D} failed; ${D} keeps running ${serving(s, D)}.${retry}`;
}

// chip(state, opts) → null in the zero state, else
// {text, compact, title, failed, count}: the full bar's text, the degraded
// bar's, and the sentence that is both its tooltip and its aria-label.
// `text` and `compact` already end in the failure mark when `failed`
// (base/baseCompact are without it, for a red span).
export function chip(s, opts = {}) {
  const f = facts(s);
  if (f.zero) return null;
  const P = f.primary;
  let base, baseCompact, title, count = null;
  if (f.reader && s.liveReload !== P) {
    const pin = cp(s, P);
    base = `📌 ${P} pinned to ${pin}`;
    baseCompact = `📌 ${cut(P)}`;
    title = `${P} is pinned to ${pin}: saves in the work tree don't reach it.`;
  } else if (f.paused) {
    count = f.cause === 'protect' ? null : f.changed;
    base = count > 0 ? `📌 Live reload paused · ${count}` : '📌 Live reload paused';
    baseCompact = count > 0 ? `📌 ${count}` : '📌';
    title = pausedSentence(s, f, opts);
  } else if (f.attached === P) {
    base = `● Live reload: ${P}`;
    baseCompact = `● ${cut(P)}`;
    title = `Live reload: ${P} — every save reaches everyone using ${s.tile}.`;
  } else {
    const A = f.attached;
    base = `● Live reload: ${A}`;
    baseCompact = `● ${cut(A)}`;
    title = `Live reload: ${A} — saves reach ${s.tile}+${A}. The primary, ${P}, is pinned to ${cp(s, P)}.`;
  }
  const failed = !!f.failed;
  return {
    text: failed ? `${base} · deploy failed` : base,
    compact: failed ? `${baseCompact}!` : baseCompact,
    base, baseCompact,
    title: failed ? `${title} ${failedSentence(s, f)}` : title,
    failed, count,
  };
}

// offer(state, opts) → the full bar's Reload now offer, or null: only while
// live reload is paused, with files changed, and the viewer may use it.
export function offer(s, opts = {}) {
  const f = facts(s);
  if (!f.paused || !(f.changed > 0)) return null;
  if (!reloadNowControl(s, f, opts).enabled) return null;
  return { label: `⇡ Reload now · ${f.changed}`, title: reloadNowTip(f) };
}

const reloadNowTip = (f) => (f.changed > 0
  ? `Ship the work tree to ${f.last} once (${files(f.changed)}); ${f.last} stays pinned.`
  : `Ship the work tree to ${f.last} once; ${f.last} stays pinned.`);
const pauseTip = (A) => `Keep ${A} on the code it runs now; saves stop reaching it until Reload now or Resume live reload.`;
function resumeTip(f, Y) {
  if (Y !== f.last || !(f.changed > 0)) return `${Y} follows the work tree again.`;
  return `${Y} follows the work tree again; the ${files(f.changed).replace(/^(\d+) /, '$1 changed ')} ${ships(f.changed)} now.`;
}
const attachTip = (Y, A) => `${Y} follows every save; ${A} is pinned to its current code.`;

const zeroSentence = (s) => `Live reload: ${s.primary || 'main'} — every save reaches everyone using ${s.tile}.`;

// chipItems(state, opts) → the chip's menu, as data:
//   [{kind:'header', label} | {kind:'sep'} |
//    {label, enabled, hint, op?, deployment?, title?, items?}]
// op is pause | resume | reloadNow | attach | deployments; a submenu's items
// carry the deployment they act on. The tooltip sentence is a disabled item
// (no op). A reader gets the header and the sentence only. opts.panel: the
// Deployments layout exists, so the menu ends with a line that opens it.
export function chipItems(s, opts = {}) {
  if (!s) return [];
  const f = facts(s);
  const items = [{ kind: 'header', label: LABEL.header }];
  items.push({ label: f.zero ? zeroSentence(s) : chip(s, opts).title, enabled: false, hint: '' });
  if (!f.reader) {
    if (f.paused) {
      const rc = reloadNowControl(s, f, opts);
      items.push({ label: f.changed > 0 ? `${LABEL.reloadNow} · ${f.changed}` : LABEL.reloadNow, enabled: rc.enabled, hint: rc.why, op: 'reloadNow', title: reloadNowTip(f) });
      const rs = control(s, 'resume', null, opts);
      const sub = resumeCandidates(s, f).map((Y) => ({ label: Y, enabled: rs.enabled, hint: rs.why, op: 'resume', deployment: Y, title: resumeTip(f, Y) }));
      items.push({ label: LABEL.resume, enabled: sub.some((it) => it.enabled), hint: sub.length ? rs.why : (rs.why || `${f.primary} is protected: live reload can't attach to it.`), items: sub });
    } else {
      const pc = control(s, 'pause', null, opts);
      items.push({ label: LABEL.pause, enabled: pc.enabled, hint: pc.why, op: 'pause', title: pauseTip(f.attached) });
      const others = f.zero ? [] : attachCandidates(s, f);
      if (others.length) {
        const sub = others.map((Y) => {
          const c = control(s, 'attach', Y, opts);
          return { label: Y, enabled: c.enabled, hint: c.why, op: 'attach', deployment: Y, title: attachTip(Y, f.attached) };
        });
        const any = sub.some((it) => it.enabled);
        items.push({ label: LABEL.attach, enabled: any, hint: any ? '' : sub[0].hint, items: sub });
      }
    }
  }
  if (opts.panel) items.push({ kind: 'sep' }, { label: LABEL.deployments, enabled: true, hint: '', op: 'deployments' });
  return items;
}

// resume onto: the last target first, then every other deployment, never a
// protected primary
function resumeCandidates(s, f) {
  const names = (s.deployments || []).map((d) => d.name).filter((n) => !(s.protectedPrimary && n === f.primary));
  return [...names.filter((n) => n === f.last), ...names.filter((n) => n !== f.last)];
}
// attach to: every deployment but the target, never a protected primary
const attachCandidates = (s, f) => (s.deployments || []).map((d) => d.name)
  .filter((n) => n !== f.attached && !(s.protectedPrimary && n === f.primary));

// toMenu(items, run) → <bx-menu> items: run(op, deployment) is called for a
// chosen item the viewer may use; disabled items keep their reason as hint.
export function toMenu(items, run) {
  return items.map((it) => {
    if (it.kind) return { ...it };
    const m = { label: it.label, disabled: !it.enabled };
    if (it.hint) m.hint = it.hint;
    if (it.items) m.items = toMenu(it.items, run);
    else if (it.enabled && it.op) m.action = () => run(it.op, it.deployment);
    return m;
  });
}

// entry(state) → the one control a tile shows for its deployments, on every
// tile of an xbind that has them (null when it doesn't): the ⇈ button, with
// a count once the viewer's state lists two or more deployments.
export function entry(s) {
  if (!s) return null;
  const n = s.record ? (s.deployments || []).length : 0;
  return { text: n >= 2 ? `${GLYPH.layout} ${n}` : GLYPH.layout, title: LAYOUT_TIP, count: n >= 2 ? n : 0 };
}

// ---- the tile API select (a session's target) ----

// defaultTarget(state) → what a new session calls: 'primary'; the live
// reload target while the primary is protected; 'off' when that is paused too.
export function defaultTarget(s) {
  if (!s || !s.record || !s.protectedPrimary) return 'primary';
  const A = s.liveReload || '';
  return A && A !== (s.primary || 'main') ? A : 'off';
}

// apiOptions(state, session) → {options: [{value, label}], value, def, notes}
// for the tile API select. A tile with no record, or whose only deployment
// is an unprotected primary, keeps today's two options ('on' / 'off').
// Otherwise one entry per deployment the viewer may reach ('primary' or a
// name), never a protected primary, then 'off'. value is the session's echo
// ({api, deployment}), never a guess; notes are lines for the select's title.
export function apiOptions(s, session = {}) {
  const off = { value: 'off', label: '⛔ no API' };
  const deps = s?.record ? (s.deployments || []) : [];
  if (!s || !s.record || (deps.length <= 1 && !s.protectedPrimary)) {
    return { options: [{ value: 'on', label: '🔌 tile API' }, off], value: session?.api === false ? 'off' : 'on', def: 'on', notes: [] };
  }
  const P = s.primary || 'main';
  const options = [];
  if (!s.protectedPrimary) options.push({ value: 'primary', label: `🔌 target: ${P} (primary)` });
  deps.filter((d) => d.name !== P && d.can?.open?.ok === true).map((d) => d.name).sort()
    .forEach((n) => options.push({ value: n, label: `🔌 target: ${n}` }));
  options.push(off);
  const value = session?.api === false ? 'off' : (session?.deployment || 'primary');
  const notes = [];
  if (s.protectedPrimary) notes.push(`${P} is protected: terminals and agents can't call it.`);
  const A = s.liveReload || '';
  const calls = value === 'primary' ? P : value;
  if (A && value !== 'off' && calls !== A) notes.push(`Saves reach ${A}; this terminal calls ${calls}.`);
  return { options, value, def: defaultTarget(s), notes };
}

// ---- the launcher (the empty window) ----

// launcher(state, opts) → null in the zero state and for readers, else
// {banner, subtitle, note}: banner is null or {text, tone ('paused' amber |
// 'plain'), reloadNow (show the ⇡ Reload now button)}; subtitle ends each
// session card's line (what a new session calls); note is a line under the
// cards when saves and new sessions go to different places.
export function launcher(s, opts = {}) {
  const f = facts(s);
  if (f.zero || f.reader) return null;
  const P = f.primary;
  let banner = null;
  if (f.paused) {
    let text;
    if (f.cause === 'protect') text = `Live reload is paused: ${f.last} is protected.`;
    else if (f.changed > 0) text = `Live reload is paused: ${files(f.changed)} changed since ${f.since}, the checkpoint ${f.last} runs.`;
    else text = `Live reload is paused: no changes since ${f.since}.`;
    banner = { text, tone: 'paused', reloadNow: reloadNowControl(s, f, opts).enabled };
  } else if (f.attached !== P) {
    banner = { text: `Live reload: ${f.attached} — saves reach ${s.tile}+${f.attached}; ${P} is pinned to ${cp(s, P)}.`, tone: 'plain', reloadNow: false };
  }
  if (banner && f.failed) banner.text = banner.text.replace(/\.$/, `, and the last deploy to ${f.failed} failed.`);
  const def = defaultTarget(s);
  const subtitle = def === 'primary' ? `· target: ${P}`
    : def === 'off' ? `· tile API off (${P} is protected and live reload is paused)`
      : `· target: ${def} (${P} is protected)`;
  const calls = def === 'primary' ? P : def;
  const note = f.attached && def !== 'off' && calls !== f.attached
    ? `Saves reach ${f.attached}; new sessions call ${calls}. Switch in the tile API select after starting.` : null;
  return { banner, subtitle, note };
}

// ---- the frame chip (over the tile, for people whose saves it concerns) ----

// frameChip(summary, state) → null or {text, title}. summary is the tile's
// /components `deployments` block ({primary, pinned, protected}); the chip
// shows only while the primary is pinned, to viewers with terminal level.
export function frameChip(summary, s) {
  if (!summary?.pinned || !s?.record) return null;
  const level = s.caller?.level;
  if (level !== 'terminal' && level !== 'admin') return null;
  const P = summary.primary || s.primary || 'main';
  if (s.liveReload === P) return null;
  const why = dep(s, P)?.lastDeploy?.result === 'failed' ? 'deploy failed'
    : s.liveReload ? `saves reach ${s.liveReload}` : 'live reload paused';
  return { text: '📌 pinned', title: `${P} pinned to ${cp(s, P)} · ${why}` };
}

// ---- confirmations ----

function affects(s, im, name, tail) {
  if (im?.affects === 'nobody') return 'Affects: nobody now.';
  if (im?.affects === 'everyone') return `Affects: everyone using ${s.tile}: ${tail}.`;
  if (im?.affects === 'deployment') return `Affects: people using ${s.tile}+${name}: ${tail}.`;
  return '';
}

// confirmation(op, {state, impact, deployment}, opts) → the dialog for one
// operation, rendered from its dry run: {title, message, ok, expect, spec}.
// spec is the <bx-dialog> spec (Cancel first, the verb as OK); expect is the
// checkpoint the request then sends as `expect` (Reload now), so what the
// dialog showed is what ships. op: pause | resume | reloadNow | attach.
export function confirmation(op, { state: s, impact: im, deployment } = {}, opts = {}) {
  const f = facts(s);
  const code = im?.code || null;
  const lines = [];
  let title, ok, expect;
  if (op === 'pause') {
    const X = f.attached || f.primary;
    title = `Pause live reload on ${s.tile}?`;
    ok = 'Pause live reload';
    let c = code?.to ? `Code: ${X} keeps running the code it runs now, pinned to ${code.to}.`
      : `Code: ${X} keeps running the code it runs now, pinned to a checkpoint of the work tree taken when you confirm.`;
    if (code?.files > 0) c += ` The work tree changed since ${X}'s last build (${files(code.files)}): pausing live reload ships those changes to ${X} once, now.`;
    lines.push(c, 'Data: nothing moves.', `Pauses: live reload — saves stop reaching ${X} until Reload now or Resume live reload.`, affects(s, im, X, 'frames reload once'));
  } else if (op === 'resume') {
    const Y = deployment || f.last;
    title = `Resume live reload on ${Y}?`;
    ok = 'Resume live reload';
    const from = code?.from || cp(s, Y);
    let c = code?.files > 0
      ? `Code: ${Y} switches to the work tree now: ${files(code.files)} (${stat(code)}) changed since ${Y}'s ${from} ${ships(code.files)} at once, then every save reaches ${Y}.`
      : `Code: ${Y} switches to the work tree now, then every save reaches ${Y}.`;
    const last = dep(s, Y)?.lastDeploy;
    if (last?.how === 'rollback') c += ` This includes the change rolled back ${ago(last.at, opts.now)}.`;
    lines.push(c, 'Data: nothing moves.', affects(s, im, Y, 'frames reload'));
  } else if (op === 'reloadNow') {
    const X = f.last;
    title = `Reload ${X} now?`;
    ok = 'Reload now';
    expect = code?.to || undefined;
    const d = dep(s, X);
    const staticTile = d?.status?.state === 'static';
    const as = code?.to ? `, as ${code.to}${code.from ? ` (${files(code.files | 0)}, ${stat(code)} against ${code.from})` : ''}` : '';
    let c = `Code: ships the work tree to ${X} once${as}${staticTile ? '' : ': build, health check, swap'}. ${X} stays pinned; later saves wait for the next Reload now.`;
    if (opts.panel) c += ' The Deployments panel shows the diff.';
    const drain = d?.api ? '; open WebSocket and SSE connections drop at the 30 s drain' : '';
    lines.push(c, 'Data: nothing moves.', affects(s, im, X, `frames reload once${drain}`));
  } else if (op === 'attach') {
    const Y = deployment;
    const A = f.attached;
    title = `Attach live reload to ${Y}?`;
    ok = `Attach to ${Y}`;
    const pin = code?.to ? `${A} is pinned to a fresh checkpoint of the work tree, ${code.to}, and stops following saves. ` : '';
    const from = code?.from || cp(s, Y);
    const moves = code?.files > 0
      ? `${Y} switches to the work tree now: ${files(code.files)} (${stat(code)}) against ${Y}'s ${from} ${ships(code.files)} at once, and ${Y} follows every save.`
      : `${Y} switches to the work tree now and follows every save.`;
    let a = affects(s, im, Y, 'frames reload');
    if (a && Array.isArray(im?.reloads) && !im.reloads.includes(A)) a += ` Nobody using ${A} sees a change.`;
    lines.push(`Code: ${pin}${moves}`, 'Data: nothing moves.', a);
  } else {
    throw new Error(`deploy-state: no confirmation for ${op}`);
  }
  const message = lines.filter(Boolean).join('\n');
  const spec = { title, message, buttons: [{ label: 'Cancel', value: null }, { label: ok, value: 'ok', primary: true }] };
  return { title, message, ok, expect, spec };
}

// refusal(op, error) → the dialog for a refused bar action: the server's
// error text, verbatim ({title, message, ok}).
export function refusal(op, error) {
  return { title: `${OP_NAME[op] || 'The operation'} was refused`, message: String(error || ''), ok: 'OK' };
}

// conflict(status, error) → 'seq' when the record moved (refetch the state,
// run the dry run again, re-open the confirmation), 'expect' when the
// reviewed code moved, null for any other answer.
export function conflict(status, error) {
  if (status !== 409) return null;
  const e = String(error || '');
  if (/^the deployments of .+ changed \(seq \d+\)/.test(e)) return 'seq';
  if (e.startsWith('the code changed since you reviewed')) return 'expect';
  return null;
}

// ---- results and terminal lines ----

function howPhrase(e) {
  if (e.how === 'promote') return e.from ? `promoted from ${e.from}` : 'promoted';
  if (e.how === 'rollback') return 'rolled back';
  if (e.how === 'reload-now') return 'Reload now';
  return 'deployed';
}

// deployText(entry, state) → the result line of a deploy entry: a final
// one (ok, failed, cancelled) or a queued one; null while it runs.
export function deployText(e, s) {
  if (!e) return null;
  const D = e.deployment || s?.primary || 'main';
  if (e.result === 'ok') return CODE_MOVES.has(e.how) ? `${D} now runs ${e.checkpoint} (${howPhrase(e)}).` : null;
  if (e.result === 'failed') {
    const keeps = e.previous || (s ? serving(s, D) : 'its current code');
    return `Deploy to ${D} failed — ${D} keeps running ${keeps}.${e.error ? ` ${e.error}` : ''}`;
  }
  if (e.result === 'queued') return `Waiting for the deploy in progress on ${D}…`;
  if (e.result === 'cancelled') return `Cancelled: ${D} was removed or ${s?.tile || 'the tile'} was disabled.`;
  return null;
}

// result(op, answer, opts) → the line an operation's answer
// ({state, deploy?, unchanged?}) reads as. opts.prev is the state the
// request was made on (attach names the deployment it pinned).
export function result(op, answer, opts = {}) {
  const s = answer?.state;
  const e = answer?.deploy;
  if (e && e.result !== 'running' && e.result !== 'ok') return deployText(e, s);
  if (!s) return null;
  const P = s.primary || 'main';
  if (op === 'pause') {
    const L = s.lastLiveReload || P;
    return `Live reload paused — ${L} is pinned to ${cp(s, L)}.`;
  }
  if (op === 'resume') {
    const A = (s.record && s.liveReload) || P;
    return `Live reload: ${A} — saves reach ${A} again.`;
  }
  if (op === 'attach') {
    const X = opts.prev?.liveReload || '';
    return X ? `Live reload: ${s.liveReload} — ${X} is pinned to ${cp(s, X)}.` : `Live reload: ${s.liveReload}.`;
  }
  if (op === 'reloadNow') {
    const L = s.lastLiveReload || P;
    if (answer.unchanged) return `No changes since ${cp(s, L)}.`;
    return e?.result === 'ok' ? deployText(e, s) : null;
  }
  return null;
}

// notice(prev, next, event, opts) → the grey line a tile's open terminals
// print for a `deployments` event (without the [ ] framing bx-terminal
// adds), or null. prev/next are the states before and after the event;
// opts.target is the tab's target ('primary', a name, 'off'); opts.panel
// says the Deployments panel exists. The tab whose own session acted
// (event.session) is skipped by the caller, not here.
export function notice(prev, next, ev, opts = {}) {
  if (!ev || !next) return null;
  const by = who(ev.by || (ev.op === 'record' ? next.liveReloadSince?.by : ''));
  const byPart = by ? ` by ${by}` : '';
  if (ev.op === 'record') {
    if (!(ev.what || []).includes('liveReload')) return null;
    const P = next.primary || 'main';
    const was = prev?.record ? (prev.liveReload || '') : (prev?.primary || P);
    const now = next.record ? (next.liveReload || '') : P;
    let line;
    if (now === '' && was !== '') {
      const L = next.lastLiveReload || was;
      line = `live reload paused${byPart} — ${L} is pinned to ${cp(next, L)}; saves no longer reach it`;
    } else if (was === '' && now) {
      line = `live reload resumed on ${now}${byPart} — saves reach ${now} again`;
    } else if (was && now && was !== now) {
      line = `live reload attached to ${now}${byPart} — saves reach ${next.tile}+${now}; ${was} is pinned to ${cp(next, was)}`;
      const t = opts.target || 'primary';
      const calls = t === 'primary' ? P : t;
      if (t !== 'off' && calls !== now) line += ` — this terminal still calls ${calls}`;
    } else return null;
    if (prev && !prev.record && next.record) line += ' — new terminals get the xbin-deploy remote';
    return line;
  }
  if (ev.op === 'deploy') {
    const D = ev.deployment || next.primary || 'main';
    if (ev.result === 'ok' && CODE_MOVES.has(ev.how)) return `${D} now runs ${ev.checkpoint} — ${howPhrase(ev)}${byPart}`;
    if (ev.result === 'failed') {
      const where = opts.panel ? 'the Deployments panel has the output' : 'bx logs has the output';
      return `deploy to ${D} failed — ${D} keeps running ${serving(next, D)}; ${where}`;
    }
  }
  return null;
}

// applyEvent(state, data) → {state, refetch} for a `deployments` event's
// data: op work-tree moves the pending count in place (no request per
// save); record, deploy and data changes refetch the state; the rest leave
// it as it is.
export function applyEvent(s, d) {
  if (!s || !d) return { state: s, refetch: false };
  if (d.op === 'work-tree') {
    if (!s.record || s.liveReload !== '' || s.view === 'reader') return { state: s, refetch: false };
    return { state: { ...s, workTree: { ...(s.workTree || {}), changed: d.changed | 0 } }, refetch: false };
  }
  return { state: s, refetch: d.op === 'record' || d.op === 'deploy' || d.op === 'data' };
}

// ---- everything the title bar needs, at once ----

// viewModel(state, opts) → what the terminal window draws for this state:
//   {feature, zero, reader, primary, attached, paused, last, changed,
//    entry, chip, offer, items, launcher, api, barKey}
// opts: {now, viewing (view-as: the viewed user's name), panel, session
// (the active tab's echo, for the API select)}. barKey changes whenever the
// bar's width may (D107's fitBar measures again).
export function viewModel(s, opts = {}) {
  const f = facts(s);
  const c = chip(s, opts);
  const o = offer(s, opts);
  const n = s?.record ? (s.deployments || []).length : 0;
  return {
    feature: !!s,
    zero: f.zero,
    reader: f.reader,
    primary: f.primary,
    attached: f.attached,
    paused: f.paused,
    last: f.last,
    changed: f.changed,
    entry: entry(s),
    chip: c,
    offer: o,
    items: chipItems(s, opts),
    launcher: launcher(s, opts),
    api: apiOptions(s, opts.session),
    barKey: [f.zero ? '' : f.attached, f.paused ? 1 : 0, f.changed ?? '', n, o ? 1 : 0, c?.failed ? 1 : 0].join('|'),
  };
}
