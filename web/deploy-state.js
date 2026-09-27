// web/deploy-state.js — the terminal window's view of a tile's live reload
// state and deployments, and every string it shows. Pure functions from the
// deployments state (`GET /api/xbin/deployments?tile=`, docs/protocol.md)
// and the viewer's permissions (the state's `caller`, plus who a view-as
// session views) to what the window draws: the title-bar chip, the Reload
// now offer, the chip's menu, the launcher's banner, the frame chip, the tile
// API select's entries, the Deployments panel (header, rows, overview,
// actions, edges, registrations, deploy log); and the words of
// confirmations, results, refusals and the grey terminal lines.
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
// under node (`make js-test`); web/frame-deploy.js and web/bx-deploy.js do
// the fetching, the events and the dialogs.

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
  if (!can || can.ok !== true) return { enabled: false, why: can?.why || (s?.view === 'reader' ? REASON.needsWrite(s.tile) : ''), kind: can?.kind || '' };
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
  if (f.changed > 0) return `${head} — ${files(f.changed)} changed since ${f.since}, ${opts.header ? `the checkpoint ${L} runs.` : `which ${L} runs. Reload now ships them to ${L} once.`}`;
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
  return { text: failed ? `${base} · deploy failed` : base, compact: failed ? `${baseCompact}!` : baseCompact, base, baseCompact,
    title: failed ? `${title} ${failedSentence(s, f)}` : title, failed, count };
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

// confirmation(op, {state, impact, deployment, …}, opts) → the dialog for one
// operation, rendered from its dry run: {title, message, ok, expect, spec,
// required, send}. spec is the <bx-dialog> spec (Cancel first, the verb as
// OK, danger for data loss); expect is the checkpoint Reload now then sends,
// so what the dialog showed is what ships; required names the checkboxes
// that must be ticked; send(values) → the request's extra fields (a confirm
// token, the reviewed checkpoint; null: send nothing). op: pause | resume |
// reloadNow | attach, or one of the panel's (PANEL_OPS).
export function confirmation(op, { state: s, impact: im, deployment, ...x } = {}, opts = {}) {
  if (!Object.hasOwn(ROWS, op)) throw new Error(`deploy-state: no confirmation for ${op}`);
  const r = ROWS[op](s, im, deployment, x, facts(s), opts), message = r.lines.filter(Boolean).join('\n');
  const okButton = { label: r.ok, value: 'ok', ...(r.danger ? { danger: true } : { primary: true }) };
  const spec = { title: r.title, message, ...(r.fields ? { fields: r.fields } : {}), buttons: [{ label: 'Cancel', value: null }, okButton] };
  return { title: r.title, message, ok: r.ok, expect: r.expect, danger: !!r.danger, required: r.required || [], send: r.send || sendAs('expect', r.expect), spec };
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
  if (s && Object.hasOwn(RESULT, op)) return RESULT[op](s, answer, opts.deployment || '');
  if (e && e.result !== 'running' && e.result !== 'ok') return deployText(e, s);
  if (!s) return null;
  const P = s.primary || 'main';
  if (['deploy', 'promote', 'rollback', 'undo'].includes(op)) return answer.unchanged ? REASON.emptyDiff(opts.deployment || P) : e?.result === 'ok' ? deployText(e, s) : null;
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
    const what = ev.what || [], P = next.primary || 'main', t = opts.target;
    if (what.includes('primary') && prev?.primary && prev.primary !== P) return `${P} is now the primary of ${next.tile}${by ? ` (by ${by})` : ''} — it serves ${P}'s data`;
    if (what.includes('protectedPrimary') && next.protectedPrimary && !prev?.protectedPrimary) return `${P} is protected${byPart} — only tile managers change its code`;
    if (what.includes('deployments') && t && t !== 'primary' && t !== 'off' && dep(prev, t) && !dep(next, t)) {
      return `deployment ${t} was removed${byPart} — this terminal's API calls fail until you switch it in the tile API select`;
    }
    if (!what.includes('liveReload')) return null;
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
      const calls = !t || t === 'primary' ? P : t;
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
  const f = facts(s), c = chip(s, opts), o = offer(s, opts), n = s?.record ? (s.deployments || []).length : 0;
  return {
    feature: !!s, zero: f.zero, reader: f.reader, primary: f.primary, attached: f.attached, paused: f.paused, last: f.last, changed: f.changed,
    entry: entry(s), chip: c, offer: o, items: chipItems(s, opts), launcher: launcher(s, opts), api: apiOptions(s, opts.session),
    barKey: [f.zero ? '' : f.attached, f.paused ? 1 : 0, f.changed ?? '', n, o ? 1 : 0, c?.failed ? 1 : 0].join('|'),
  };
}

// ---- the Deployments panel's shared words (M2); its view is web/deploy-panel.js ----

// The panel's words that belong to no operation: reasons that aren't refusals, and notes.
export const REASON = Object.freeze({
  needsWrite: (t) => `Needs write access to ${t}.`, logs: (t) => `Logs need terminal access to ${t} (they can carry secrets).`,
  emptyDiff: (b) => `${b} already runs this code.`, unhealthy: (y) => `${y} isn't healthy — deploy working code to it first.`,
  manager: 'Only tile managers change this: the tile\'s owner, its org\'s admins, or a workspace admin',
  stale: 'The work tree changed since this diff was taken.', reviewAgain: 'The code changed since you reviewed it — review the new diff.',
  tick: 'Tick the box to confirm.', wouldNotify: 'Would notify (not sent — only the primary notifies people)',
  gitNote: 'terminals opened before this tile had deployments lack the remote: open a new one',
  caps: (c, t) => `non-primary deployments: ${c.tileUsed} of ${c.tile} on ${t}, ${c.workspaceUsed} of ${c.workspace} in the workspace`,
  edgeRule: (t) => `The primary uses every edge as today. Non-primary deployments reach other tiles' primaries as reader (writer and admin roles are clamped to reader) and never write to them. Edges that can't be limited to reading are blocked for them, with no override. They use ${t}'s network, but never host networking.`,
});

const others = (s) => (s?.deployments || []).filter((d) => !d.primary).map((d) => d.name);
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

function dataText(d, opts = {}) {
  const x = d?.data, when = x?.at ? ago(x.at, opts.now) : '';
  if (!x) return '';
  if (x.busy) return `${x.busy}…`;
  if (x.state === 'empty') return x.reset ? `started empty · reset${when ? ` ${when}` : ''}` : 'started empty';
  if (x.state === 'seeded' || x.state === 'restored') return `${x.state === 'seeded' ? `seeded from ${x.from || 'main'}` : 'restored'}${when ? ` · ${when}` : ''}`;
  return x.state === 'partial' ? 'partial — the last seed or restore failed' : x.state || '';
}

const edgeLabel = (e) => (e.id.startsWith('slot:') ? `${e.id.slice(5)}${e.to ? ` → ${e.to}` : ''}` : e.id.replace(/^grant:/, ''));

// ---- the panel's confirmations (§5.2 rows 5–20) and results ----

const box = (name, label) => ({ name, type: 'checkbox', label });
const hm = (at) => { const t = new Date(at); return Number.isFinite(t.getTime()) ? `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}` : ''; };
const against = (c, whose = '') => (c?.from && c.from !== 'work-tree' ? `${files(c.files | 0)}, ${stat(c)} against ${whose}${c.from}` : '');
const pausesLine = (im, X, more = '') => (im?.pausesLiveReload ? `Pauses: live reload — later saves won't reach ${X} until you resume.${more}` : '');
const reach = (s, im, X) => (im?.affects === 'everyone' ? `Affects: everyone using ${s.tile}: frames reload once${dep(s, X)?.api ? '; WebSocket and SSE connections drop at the 30 s drain' : ''}.`
  : im?.affects === 'deployment' ? `Affects: only people using ${s.tile}+${X}.` : affects(s, im, X, ''));
const stopping = (Y, im) => [Y, ...(im?.stops || []).filter((n) => n !== Y)];
const sendAs = (key, k) => () => (k ? { [key]: k } : {});

// Each row: (state, impact, deployment, extra, facts, opts) → {title, ok, lines, expect?, danger?, fields?, required?, send?}.
const ROWS = {
  pause(s, im, D, x, f) {
    const X = f.attached || f.primary, c = im?.code;
    return { title: `Pause live reload on ${s.tile}?`, ok: 'Pause live reload', lines: [
      `Code: ${X} keeps running the code it runs now, pinned to ${c?.to || 'a checkpoint of the work tree taken when you confirm'}.${c?.files > 0 ? ` The work tree changed since ${X}'s last build (${files(c.files)}): pausing live reload ships those changes to ${X} once, now.` : ''}`,
      'Data: nothing moves.', `Pauses: live reload — saves stop reaching ${X} until Reload now or Resume live reload.`, affects(s, im, X, 'frames reload once')] };
  },
  resume(s, im, D, x, f, o) {
    const Y = D || f.last, c = im?.code, last = dep(s, Y)?.lastDeploy;
    const code = c?.files > 0 ? `${Y} switches to the work tree now: ${files(c.files)} (${stat(c)}) changed since ${Y}'s ${c.from || cp(s, Y)} ${ships(c.files)} at once, then every save reaches ${Y}.`
      : `${Y} switches to the work tree now, then every save reaches ${Y}.`;
    return { title: `Resume live reload on ${Y}?`, ok: 'Resume live reload',
      lines: [`Code: ${code}${last?.how === 'rollback' ? ` This includes the change rolled back ${ago(last.at, o.now)}.` : ''}`, 'Data: nothing moves.', affects(s, im, Y, 'frames reload')] };
  },
  reloadNow(s, im, D, x, f, o) {
    const X = f.last, c = im?.code, d = dep(s, X), as = c?.to ? `, as ${c.to}${c.from ? ` (${files(c.files | 0)}, ${stat(c)} against ${c.from})` : ''}` : '';
    return { title: `Reload ${X} now?`, ok: 'Reload now', expect: c?.to || undefined,
      lines: [`Code: ships the work tree to ${X} once${as}${d?.status?.state === 'static' ? '' : ': build, health check, swap'}. ${X} stays pinned; later saves wait for the next Reload now.${o.panel ? ' The Deployments panel shows the diff.' : ''}`,
        'Data: nothing moves.', affects(s, im, X, `frames reload once${d?.api ? '; open WebSocket and SSE connections drop at the 30 s drain' : ''}`)] };
  },
  attach(s, im, Y, x, f) {
    const A = f.attached, c = im?.code, pin = c?.to ? `${A} is pinned to a fresh checkpoint of the work tree, ${c.to}, and stops following saves. ` : '';
    const moves = c?.files > 0 ? `${Y} switches to the work tree now: ${files(c.files)} (${stat(c)}) against ${Y}'s ${c.from || cp(s, Y)} ${ships(c.files)} at once, and ${Y} follows every save.`
      : `${Y} switches to the work tree now and follows every save.`;
    let a = affects(s, im, Y, 'frames reload');
    if (a && Array.isArray(im?.reloads) && !im.reloads.includes(A)) a += ` Nobody using ${A} sees a change.`;
    return { title: `Attach live reload to ${Y}?`, ok: `Attach to ${Y}`, lines: [`Code: ${pin}${moves}`, 'Data: nothing moves.', a] };
  },
  add(s, im, X, x, f, o) {
    const P = f.primary, c = im?.code, v = x.add || {}, j = im?.joins, seed = v.data === 'seed' ? ROWS.seed(s, im, X, x, f) : null;
    const code = v.from === 'primary' ? `${P}'s code${c?.to ? `, ${c.to}` : ''}` : v.from && v.from !== 'work-tree' ? v.from : `a fresh checkpoint of the work tree${c?.to ? `, ${c.to}` : ''}`;
    const lr = v.attach ? ` Live reload moves to ${X}; ${f.attached || P} is pinned to ${c?.to || 'a checkpoint of the work tree taken when you confirm'}.` : '';
    const data = seed ? `seeded from ${P}: its data, which may be personal — see below` : j ? `joins ${j.scope}'s "${X}" data (${j.state}${j.by ? ` by ${who(j.by)}` : ''}${j.at ? ` ${ago(j.at, o.now)}` : ''})` : 'starts empty';
    return { title: `Add deployment ${X} to ${s.tile}?`, ok: 'Add deployment', fields: seed?.fields.filter((b) => b.name !== 'stop'), required: seed?.required, send: sendAs('confirm', seed && 'copy-data'),
      lines: [`Code: ${X} runs ${code}.${lr}`, `Data: ${data}; secrets start as names only.`, `Edges: it uses ${s.tile}'s grants and bindings, reading other tiles' primaries as reader.`,
        `Affects: nobody now. It is reachable at /c/${s.tile}+${X}/ by people with write on ${s.tile} and by this tile's terminals. Its cron jobs, bus deliveries and alwaysOn stay off.`, ...(seed?.lines || [])] };
  },
  deploy(s, im, X, x) {
    const c = im?.code, named = x.checkpoint, st = against(c);
    const what = named ? `${named}${st ? ` (${st})` : ''}` : `a fresh checkpoint of the work tree${c?.to ? `, ${c.to}${st ? ` (${st})` : ''}` : ''}`;
    return { title: named ? `Deploy ${named} to ${X}?` : `Deploy the work tree to ${X}?`, ok: `Deploy to ${X}`, send: sendAs('checkpoint', named || x.reviewed || c?.to),
      lines: [`Code: ${X} runs ${what}. If it fails, ${X} keeps its current code.`, `Data: ${X} keeps its data.`, pausesLine(im, X), reach(s, im, X)] };
  },
  promote(s, im, X, x) {
    const { from: A, to: B } = x, c = im?.code, st = against(c, `${B}'s `);
    const code = c?.to ? `, ${c.to}${c.workTreeAt ? `, the work tree at ${hm(c.workTreeAt)}` : ''}${st ? ` (${st})` : ''}` : '';
    return { title: `Promote ${A} → ${B}?`, ok: 'Promote', send: sendAs('expect', x.reviewed || c?.to),
      lines: [`Code: ${B} runs ${A}'s code${code}.`, `Data: only code moves — ${B} keeps its data, secrets, cron jobs and routing.`, pausesLine(im, B), reach(s, im, B)] };
  },
  rollback(s, im, X, x, f, o) {
    const C = x.checkpoint, e = x.entry, when = e && (e.finishedAt || e.requestedAt);
    const was = [when ? `deployed ${ago(when, o.now)}${e.by ? ` by ${who(e.by)}` : ''}` : '', against(im?.code)].filter(Boolean).join('; ');
    return { title: `Roll back ${X} to ${C}?`, ok: 'Roll back', send: sendAs('checkpoint', C),
      lines: [`Code: ${X} runs ${C} again${was ? ` (${was})` : ''}.`, 'Data: stays as it is — a roll back moves code, not state; resources the newer code created are kept.',
        pausesLine(im, X, ' The work tree still holds the code you roll back from.'), reach(s, im, X)] };
  },
  remove(s, im, Y, x, f) {
    const P = f.primary, onY = s.liveReload === Y || (!s.liveReload && s.lastLiveReload === Y);
    return { title: `Remove deployment ${Y}?`, ok: `Remove ${Y}`, danger: true, fields: [box('ok', `I understand ${Y}'s data is deleted`)], required: ['ok'], send: sendAs('confirm', 'erase'),
      lines: [`${Y} stops. Its data, secrets, logs, cron jobs and subscriptions are deleted, and this can't be undone. Its checkpoints stay until cleanup.`,
        onY ? `Pauses: live reload was on ${Y}; Reload now and Resume live reload then go to ${P}, which keeps running ${serving(s, P)}.` : '', `Affects: people using /c/${s.tile}+${Y}/ lose it.`] };
  },
  primary(s, im, Y, x, f, o) {
    const P = f.primary, data = dataText(dep(s, Y), o), miss = im?.placeholders || [];
    return { title: `Make ${Y} the primary of ${s.tile}?`, ok: 'Reassign the primary', danger: true, required: miss.length ? ['ok', 'secrets'] : ['ok'],
      fields: [box('ok', `I understand ${P}'s data does not move to ${Y}`), ...(miss.length ? [box('secrets', `I understand ${Y} has no value for ${miss.join(', ')}`)] : [])],
      send: () => ({ confirm: 'data-stays', ...(s.protectedPrimary && im?.code?.to ? { expect: im.code.to } : {}) }),
      lines: [`Everything that reaches ${s.tile} moves to ${Y} at once: its URL, other tiles' bindings and grants, ingress, cron jobs, bus deliveries, notifications and the app.`,
        `Data: the primary will serve ${Y}'s data${data ? ` (${data})` : ''}. ${P}'s data does not move: ${P} keeps it, keeps running ${serving(s, P)}, and its cron jobs and subscriptions become dormant. Per-user settings saved while ${P} was primary stay with ${P}.`,
        miss.length ? `Secrets: ${Y} has no value for ${plural(miss.length, 'secret', 'secrets')} ${P} uses (${miss.slice(0, 3).join(', ')}${miss.length > 3 ? ', …' : ''}): copy or set them first.` : '',
        s.liveReload === Y && !s.protectedPrimary ? `Live reload is attached to ${Y}: from now on every save reaches everyone using ${s.tile}. Pause live reload first to keep ${Y} pinned.` : '',
        s.protectedPrimary ? `The primary is protected: ${Y} becomes protected, live reload leaves it, and ${Y} is pinned to ${im?.code?.to || cp(s, Y) || 'its current code'}.` : '',
        `Pauses: ${P}'s and ${Y}'s backends restart now; WebSocket and SSE connections drop.${dep(s, P)?.alwaysOn ? ` ${P} is no longer kept running (alwaysOn).` : ''}`,
        `Terminals: sessions that follow the primary now call ${Y} and ${Y}'s data; sessions that name a deployment keep calling it.`] };
  },
  protect(s, im, X, x, f) {
    const P = f.primary, A = s.liveReload && s.liveReload !== P ? s.liveReload : '';
    return { title: `Protect ${P}?`, ok: `Protect ${P}`, send: sendAs('expect', im?.code?.to),
      lines: [`Only tile managers (the tile's owner, its org's admins, or a workspace admin) can change ${P}'s code: deploy, promote, roll back, reload now — from their own browser session, naming the checkpoint they reviewed, never from a terminal or an agent.`,
        f.attached === P && !f.paused ? `Pauses: live reload leaves ${P}, which is pinned to ${im?.code?.to || 'a checkpoint of the work tree taken when you confirm'}.` : '',
        `Terminals: terminal and agent sessions that call ${P} restart now, ${A ? `calling ${A}` : 'with the tile API off: nothing else can be offered'}; their running commands end and shell scrollback is lost (agents resume their conversation).`,
        'Other deployments work as before.'] };
  },
  unprotect: (s, im, X, x, f) => ({ title: `Unprotect ${f.primary}?`, ok: 'Unprotect',
    lines: [`Terminal users and their agents can deploy to ${f.primary} again, as saving did before protection. New sessions call ${f.primary} by default again; running ones keep their target.`] }),
  seed(s, im, Y, x, f) {
    const P = f.primary, st = stopping(Y, im), full = dep(s, Y)?.data?.state;
    return { title: `Seed ${Y} with ${P}'s data?`, ok: `Seed ${Y}`, danger: !!full && full !== 'empty', required: ['ok'], send: (v) => ({ confirm: 'copy-data', ...(v.stop ? { stop: true } : {}) }),
      fields: [box('stop', `Stop ${P} for a point-in-time copy`), box('ok', `I understand ${P}'s data is copied into ${Y}, replacing ${Y}'s`)],
      lines: [`Data: copies ${P}'s data as of now (kv, sqlite, files, blobs) into ${Y}, replacing ${Y}'s data. ${st.join(' and ')} ${st.length > 1 ? 'stop during the copy and restart' : 'stops during the copy and restarts'}.`,
        `The copy may contain personal data. Everyone with write on ${s.tile}, and their agents, can open ${Y} and run any code there; protecting the primary doesn't cover this copy. It stays until reset or removal, and opt-in backups of ${Y} send it to the archiver.`] };
  },
  reset(s, im, Y, x, f) {
    const P = f.primary, st = stopping(Y, im), main = Y === 'main' && P !== 'main' ? ` ${Y} is main and not the primary: this deletes main's data — what ${s.tile} served until ${P} became the primary.` : '';
    return { title: `Reset ${Y}'s data?`, ok: `Reset ${Y}`, danger: true, required: ['ok'], send: (v) => ({ confirm: 'erase-data', ...(v.vault ? { vault: true } : {}) }),
      fields: [box('vault', `Also clear ${Y}'s secrets`), box('ok', `I understand ${Y}'s data is deleted`)],
      lines: [`Data: deletes everything in ${Y}'s data (kv, sqlite, files, blobs). ${st.join(' and ')} ${st.length > 1 ? 'restart' : 'restarts'} empty. ${P}'s data is not touched. This can't be undone.${main}`] };
  },
  vaultCopy(s, im, Y, x, f) {
    const keys = x.keys || [];
    return { title: `Copy secrets to ${Y}?`, ok: 'Copy values', fields: keys.length ? keys.map((k) => box(`key:${k}`, k)) : [box('all', `all of ${f.primary}'s secrets`)],
      send: (v) => { const k = keys.filter((n) => v[`key:${n}`]); return k.length ? { keys: k } : v.all ? { all: true } : null; },
      lines: [`Copies the ticked secrets' values from ${f.primary} into ${Y}'s vault. Any code running on ${Y} can read them, and any terminal user can deploy code to ${Y}, even while ${f.primary} is protected.`] };
  },
  deliveries(s, im, Y, x, f) {
    const r = dep(s, Y)?.registrations || [], n = (k) => r.filter((g) => g.kind === k).length;
    return { title: `Turn on deliveries for ${Y}?`, ok: 'Turn on deliveries',
      lines: [`${Y}'s cron jobs (${n('cron')}) and bus subscriptions (${n('bus')}) start firing for ${Y}, alongside ${f.primary}'s. Their side effects are real: they run with ${Y}'s data, ${s.tile}'s grants (reading other tiles' primaries) and ${s.tile}'s network, so anything they send (email, webhooks) is real.`] };
  },
  alwaysOn: (s, im, Y, x, f) => ({ title: `Keep ${Y} running?`, ok: 'Turn on alwaysOn', lines: [`${Y} starts now, is never idle-stopped and restarts after exits, like ${f.primary}. It uses memory while it runs.`] }),
  edge(s, im, X, x) {
    const e = (s.edges || []).find((g) => g.id === x.edge) || { id: x.edge }, to = e.to || edgeLabel(e), net = e.kind === 'net' || e.id === 'slot:net';
    const v = x.policy === 'default' ? e.default : x.policy, them = `${s.tile}'s non-primary deployments${others(s).length ? ` (${others(s).join(', ')})` : ''}`;
    return { title: v === 'read' ? `Let non-primary deployments read ${to}?` : `Let non-primary deployments use ${net ? `${s.tile}'s network` : to}?`, ok: 'Allow',
      lines: [v === 'read' ? `${them} may call ${to}'s primary, as reader. They never write to it.` : net ? `${them} get ${s.tile}'s relay policy, never host networking; anything they send is real.` : `${them} may use ${to} as ${s.tile} does.`] };
  },
  runNow(s, im, Y, x) {
    const d = dep(s, Y)?.data;
    return { title: `Run ${x.job} on ${Y} once?`, ok: 'Run now',
      lines: [`Runs ${x.job} once on ${Y}, with ${Y}'s data${d?.state === 'seeded' ? ` seeded from ${d.from || 'main'}: real people's data` : ''} and ${s.tile}'s network: anything it sends (email, webhooks) is real.`] };
  },
  limits(s, im, Y, x, f) {
    const l = dep(s, Y)?.limits || {}, ov = new Set(l.overrides || []), pl = dep(s, f.primary)?.limits;
    const fld = (k, word, unit) => ({ name: k, type: 'number', label: `${word} (${unit})`, value: ov.has(k) ? String(l[k]) : '', placeholder: !ov.has(k) && l[k] ? `the tile's default (${l[k]} ${unit})` : 'the tile\'s default' });
    return { title: `Set ${Y}'s limits?`, ok: 'Set limits', fields: [fld('memMiB', 'memory', 'MiB'), fld('diskGiB', 'disk-quota share', 'GiB')],
      send: (v) => ({ limits: Object.fromEntries(['memMiB', 'diskGiB'].map((k) => [k, String(v[k] ?? '').trim()]).filter(([k, t]) => t || ov.has(k)).map(([k, t]) => [k, !t ? null : /^\d+$/.test(t) ? Number(t) : t])) }),
      lines: [`${s.tile}'s own limits${pl && !pl.overrides?.length ? ` (${pl.memMiB} MiB, ${pl.diskGiB} GiB)` : ''} are the ceiling, and the primary keeps first call on them.`] };
  },
  target: (s, im, Y) => ({ title: Y === 'off' ? 'Restart this terminal without API access?' : `Restart this terminal calling ${Y === 'primary' ? s.primary || 'main' : Y}?`, ok: 'Restart',
    lines: [`Its shell and anything running in it end, and the scrollback is lost.${Y === 'off' ? '' : ` Its API calls and bx commands then reach ${Y === 'primary' ? 'the primary' : `${s.tile}+${Y}`}.`}`] }),
};
ROWS.undo = ROWS.rollback;

// The operations the panel confirms beyond M1's four (undo: a roll back to what the last move replaced).
export const PANEL_OPS = Object.freeze(Object.keys(ROWS).filter((op) => !OP_NAME[op]));

const RESULT = {
  add: (s, a, X) => `Added ${X} at /c/${s.tile}+${X}/.`, remove: (s, a, X) => `Removed ${X}.`,
  primary: (s) => `${s.primary} is now the primary — it serves ${s.primary}'s data.`,
  protect: (s) => `${s.primary} is protected.`, unprotect: (s) => `${s.primary} is no longer protected.`,
  seed: (s, a, X) => `${X} seeded from ${s.primary}.`, reset: (s, a, X) => `${X}'s data was reset.`,
  vaultCopy: (s, a, X) => `Copied ${plural(a.copied?.length | 0, 'secret', 'secrets')} to ${X}.`,
  deliveries: (s, a, X) => `Deliveries ${dep(s, X)?.deliveries ? 'on' : 'off'} for ${X}.`, alwaysOn: (s, a, X) => `alwaysOn ${dep(s, X)?.alwaysOn ? 'on' : 'off'} for ${X}.`,
  limits: (s, a, X) => { const l = dep(s, X)?.limits; return l ? `${X}'s limits set: ${l.memMiB} MiB, ${l.diskGiB} GiB.` : `${X}'s limits set.`; },
  runNow: (s, a) => (a.delivery ? `delivered · ${a.delivery.status} · ${a.delivery.ms} ms` : null),
};

// What web/deploy-panel.js, the Deployments panel's half of this view, reads
// of this module's private helpers.
export const shared = Object.freeze({ facts, zeroSentence, cp, pausedSentence, serving, dep, CODE_MOVES, howPhrase, files, MINUS, others, plural, dataText, edgeLabel });
