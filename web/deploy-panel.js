// web/deploy-panel.js — the Deployments panel's view model: pure functions from
// the deployments state (`GET /api/xbin/deployments?tile=`, docs/protocol.md)
// and the viewer's permissions to what web/bx-deploy.js draws: the side list,
// the header, a deployment's overview and actions, the tile-wide page, the
// edges table, registrations, the deploy log, the diff line and the add form.
//
// Split out of web/deploy-state.js, whose rules it follows: the server
// decides and this renders; a control the viewer may not use is disabled
// with the server's reason, never hidden; a fact the state doesn't carry is
// left out. The panel's confirmations and results stay in deploy-state.js,
// beside the live reload view's. Imports only deploy-state.js and touches no
// DOM, so hack/deploy-panel.test.mjs runs it under node (`make js-test`).

import { who, ago, LABEL, control, chipItems, REASON, shared } from './deploy-state.js';

const { facts, zeroSentence, cp, pausedSentence, serving, dep, CODE_MOVES, howPhrase, files, MINUS, others, plural, dataText, edgeLabel } = shared;

// the deploy log's `how`, in words
const HOW = { deploy: 'deploy', promote: 'promote', rollback: 'roll back', 'reload-now': 'reload now', resume: 'resume live reload', pause: 'live reload paused', attach: 'live reload attached', add: 'added', protect: 'protected', reassign: 'primary reassigned', restart: 'restart' };
const same = (a, b) => !!a && !!b && (a.startsWith(b) || b.startsWith(a)); // two prefixes of one checkpoint id
const pointer = (d) => (d?.checkpoint?.id ? `📌 ${d.checkpoint.id}` : '● work tree');
const deliveriesText = (d) => (typeof d.deliveries !== 'boolean' ? '' : d.primary ? 'deliveries: active' : `deliveries: ${d.deliveries ? 'on' : 'off'}`);

function statusText(d) {
  const st = d?.status || {};
  const moving = st.deploying ? `deploying${st.deploying.checkpoint ? ` ${st.deploying.checkpoint}` : ''}…` : st.queued?.length ? 'queued' : '';
  return [st.state === 'crash-looping' ? 'failed' : st.state, moving].filter(Boolean).join(' · ');
}

// panelRows(state, opts) → the side list, primary first; opts.target: the active tab's target.
export function panelRows(s, opts = {}) {
  const P = s?.primary || 'main', t = opts.target === 'primary' ? P : opts.target;
  return [...(s?.deployments || [])].sort((a, b) => (b.name === P) - (a.name === P) || a.name.localeCompare(b.name)).map((d) => ({
    name: d.name, primary: d.name === P, protected: d.name === P && !!s.protectedPrimary, code: pointer(d), status: statusText(d),
    data: dataText(d, opts), deliveries: deliveriesText(d), target: !!t && t === d.name, lastDeployFailed: d.lastDeploy?.result === 'failed',
  }));
}

// panelHeader(state, opts) → {text, actions: [{id, label, enabled, why, items?}]}; opts.undo (the last
// target's newest deploy-log entry) adds Undo after a code move onto it, back to its `previous`.
export function panelHeader(s, opts = {}) {
  if (!s) return null;
  const f = facts(s), P = f.primary;
  let text = f.zero ? zeroSentence(s) : f.reader && s.liveReload !== P ? `${P} is pinned to ${cp(s, P)}.` : f.paused ? pausedSentence(s, f, { ...opts, header: true })
    : f.attached === P ? `Live reload: ${P} (primary) — every save reaches everyone using ${s.tile}.`
      : `Live reload: ${f.attached} — saves reach ${s.tile}+${f.attached}. The primary, ${P}, is pinned to ${cp(s, P)}.`;
  if (f.failed && !f.zero) text = `${text.replace(/\.$/, '')} — the last deploy to ${f.failed} failed; ${f.failed} keeps running ${serving(s, f.failed)}.`;
  const actions = chipItems(s, { ...opts, panel: false }).filter((it) => !it.kind && (it.op || it.items)).map((it) => {
    const id = it.op || (it.label === LABEL.resume ? 'resume' : 'attach');
    const items = it.items?.map((x) => ({ id: `${id}/${x.deployment}`, label: x.label, enabled: x.enabled, why: x.hint || '', title: x.title }));
    return { id, label: it.label, enabled: it.enabled, why: it.hint || '', title: it.title, ...(items ? { items } : {}) };
  });
  const u = opts.undo;
  if (f.paused && ['deploy', 'promote', 'rollback'].includes(f.cause) && u?.previous && u.deployment === f.last && u.result === 'ok') {
    const c = control(s, 'rollback', f.last, opts);
    actions.push({ id: 'undo', label: `Undo: roll ${f.last} back to ${u.previous}`, enabled: c.enabled, why: c.why, title: `Put back the code ${f.last} ran before the last move.` });
  }
  return { text, actions };
}

// overview(state, name, opts) → {heading, lines: [[label, text]], url, gitLine, gitNote}; opts.entry:
// the newest deploy-log entry, which says how the code got there.
export function overview(s, name, opts = {}) {
  const d = dep(s, name), e = opts.entry?.deployment === name ? opts.entry : d?.lastDeploy, lines = [];
  if (!d) return null;
  const how = e?.how ? (CODE_MOVES.has(e.how) ? howPhrase(e) : HOW[e.how] || e.how) : '', at = e?.finishedAt || e?.at || e?.requestedAt;
  if (d.primary) lines.push(['primary', `primary — everything from outside reaches it${s.protectedPrimary ? ' · 🛡 protected' : ''}`]);
  lines.push(['code', `${pointer(d)}${how ? ` · ${how}${e.by ? ` by ${who(e.by)}` : ''}${at ? `, ${ago(at, opts.now)}` : ''}` : ''}`],
    ['status', `${statusText(d)}${d.status?.error ? ` — ${d.status.error}` : ''}`]);
  if (d.lastDeploy?.result === 'failed') lines.push(['last deploy', 'last deploy failed — the deploy log has its error']);
  const l = d.limits, ov = new Set(l?.overrides || []), v = d.vault;
  const lim = (k, word, unit) => `${word}: ${ov.has(k) ? `${l[k]} ${unit} (set by a tile manager)` : `the tile's default (${l[k]} ${unit})`}`;
  if (dataText(d, opts)) lines.push(['data', dataText(d, opts)]);
  if (l) lines.push(['limits', `${lim('memMiB', 'memory', 'MiB')} · ${lim('diskGiB', 'disk', 'GiB')}`]);
  if (v) lines.push(['vault', `${plural(v.keys | 0, 'secret', 'secrets')}${v.placeholders ? ` · ${v.placeholders === 1 ? '1 is a placeholder' : `${v.placeholders} are placeholders`}` : ''}`]);
  if (deliveriesText(d)) lines.push(['deliveries', deliveriesText(d)]);
  if (!d.primary && d.alwaysOnDeclared) lines.push(['alwaysOn', `alwaysOn: ${d.alwaysOn ? 'on' : 'off'}`]);
  const git = !!d.checkpoint && s.view !== 'reader';
  return { heading: name, lines, url: d.url || `/c/${s.tile}${d.primary ? '' : `+${name}`}/`, gitLine: git ? `git: deploy/${name} — git fetch xbin-deploy` : null, gitNote: git ? REASON.gitNote : null };
}

// panelActions(state, name, opts) → [{id, label, enabled, why, title, on? (a switch), to?}] of a
// deployment, or the tile-wide page (name ''); what the viewer may not use is disabled with its reason.
export function panelActions(s, name, opts = {}) {
  const P = s?.primary || 'main', out = [], d = dep(s, name), c = (op, y = name) => control(s, op, y, opts);
  const add = (id, label, k, title, extra) => out.push({ id, label, enabled: k.enabled, why: k.why, title, ...extra });
  if (!s?.record || (name && !d)) return out;
  if (!name) {
    const ys = others(s).map((y) => dep(s, y)), ok = ys.filter((y) => y.can?.primary?.ok && y.status?.state === 'healthy');
    const k = ys.length ? c('primary', (ok[0] || ys[0]).name) : null;
    if (k?.enabled && !ok.length) Object.assign(k, { enabled: false, why: REASON.unhealthy(ys[0].name) });
    if (k) add('reassign', 'Reassign the primary…', k, 'Send everything from outside to another deployment; data doesn\'t move.');
    const pr = !!s.protectedPrimary;
    add(pr ? 'unprotect' : 'protect', `${pr ? 'Unprotect' : 'Protect'} the primary`, c('protect', null), pr ? `Terminal users and agents may deploy to ${P} again.` : `Only tile managers change ${P}'s code.`);
    if ((s.edges || []).some((e) => (e.effective || e.policy) !== 'block')) add('blockEdges', 'Block every edge', c('edges', null), 'Set every edge\'s non-primary access to block.');
    return out;
  }
  const B = d.primary ? others(s).sort()[0] : P;
  add('deploy', `Deploy to ${name}`, c('deploy'), `Put a fresh checkpoint of the work tree on ${name}.`);
  if (B) add('promote', `Promote ${name} → ${B}…`, c('promoteTo', B), `${B} gets exactly ${name}'s code; its data stays.`, { to: B });
  add('remove', 'Remove deployment…', c('remove'), `Delete ${name} and its data.`);
  if (!d.primary) {
    add('seed', `Seed from ${P}…`, c('seed'), `Copy ${P}'s data into ${name} (it may contain personal data).`);
    add('reset', 'Reset data…', c('reset'), `Delete everything ${name} stored.`);
    add('vaultCopy', 'Copy vault values…', c('vaultCopy'), `Copy chosen secrets from ${P} into ${name}.`);
    add('deliveries', `Deliveries: ${d.deliveries ? 'on' : 'off'}`, c('deliveries'), d.deliveries ? `${name}'s cron jobs and bus subscriptions fire for ${name}, with its own data. Switch off to silence them.`
      : `${name}'s cron jobs and bus subscriptions are silenced. Switch on to let them fire for ${name} again.`, { on: !!d.deliveries });
    if (d.alwaysOnDeclared) add('alwaysOn', `alwaysOn: ${d.alwaysOn ? 'on' : 'off'}`, c('alwaysOn'), `Keep ${name} running, never idle-stopped.`, { on: !!d.alwaysOn });
  }
  add('limits', 'Set limits…', c('limits'), `${name}'s memory and disk share, never above ${s.tile}'s.`);
  add('open', 'open ↗', d.primary && !d.can ? { enabled: true, why: '' } : c('open'), `Open ${d.primary ? s.tile : `${s.tile}+${name}`} in a new tab.`);
  return out;
}

// zeroPanel(state, opts) → the zero state's entry point: [{id?, lead, text, enabled?, why?}].
export const zeroPanel = (s, opts = {}) => [{ lead: `Live reload: ${s.primary || 'main'}`, text: `every save reaches everyone using ${s.tile}.` },
  { id: 'pause', lead: LABEL.pause, text: `keep ${s.tile} on its current code while you work; Reload now ships your changes when you're ready.`, ...control(s, 'pause', null, opts) },
  { id: 'add', lead: 'Add deployment…', text: `a second runtime of ${s.tile}, for example dev, with its own data, at /c/${s.tile}+dev/. Saves can go there while ${s.primary || 'main'} stays put.`, ...control(s, 'add', null, opts) }];

// edgeRows(state, opts) → the edges table: [{id, label, primary, value, values (none: text only), text,
// refused, enabled, why}].
export function edgeRows(s, opts = {}) {
  const m = control(s, 'edges', null, opts);
  return (s?.edges || []).map((e) => {
    const vl = (v) => (v === 'read' ? `read — its primary, as reader${/^(writer|admin)$/.test(e.role || '') ? ` (clamped from ${e.role})` : ''}` : v === 'inherit' ? `inherit — as ${s.tile}` : v);
    const fixed = (e.values || []).length < 2 || (e.effective === 'block' && !!e.why);
    return { id: e.id, label: edgeLabel(e), primary: e.role || 'as today', value: e.policy, refused: `refused ${e.refused | 0} · clamped ${e.clamped | 0}`,
      values: fixed ? [] : [...e.values.map((v) => ({ value: v, label: vl(v) })), ...(e.set ? [{ value: 'default', label: `default (${e.default})` }] : [])],
      text: fixed ? `blocked — ${e.why || 'this edge can\'t be limited to reading'}` : vl(e.effective || e.policy), enabled: m.enabled && !fixed, why: m.enabled ? '' : (m.why || REASON.manager) };
  });
}

// widens(state, edge, value): block → read or inherit, the one direction that confirms.
export function widens(s, id, v) {
  const e = (s?.edges || []).find((g) => g.id === id);
  return !!e && (e.effective || e.policy) === 'block' && (v === 'default' ? e.default : v) !== 'block';
}

// registrationRows(state, name, opts) → registrations with their pill, and Run now for cron jobs. A
// non-primary's cron jobs and bus subscriptions are active, for it, unless its deliveries are off;
// its interface instances and ingress hosts are dormant: routes reach the primary only (P13).
export const registrationRows = (s, name, opts = {}) => (dep(s, name)?.registrations || []).map((r) => {
  const d = dep(s, name), routes = r.kind === 'iface-instance' || r.kind === 'ingress-host';
  return { kind: r.kind, name: r.name, pill: d.primary ? 'active' : routes ? 'dormant — routes reach the primary only' : r.dormant ? 'dormant — deliveries off' : 'active',
    label: r.kind === 'cron' ? `${r.name} · ${r.schedule || ''}` : r.kind === 'bus' ? `${r.name} · ${r.resource || ''}${r.prefix ? ` ${r.prefix}` : ''}` : r.kind === 'iface-instance' ? `#${r.name}` : r.name,
    runNow: r.kind === 'cron' && !d.primary ? control(s, 'runNow', name, opts) : null };
});

// asksToRun(state, name): Run now confirms unless the data is empty and the network blocked.
export function asksToRun(s, name) {
  const net = (s?.edges || []).find((e) => e.id === 'slot:net');
  return !(dep(s, name)?.data?.state === 'empty' && net && (net.effective || net.policy) === 'block');
}

// registrationsNote(state, name) → the registrations tab's sentence for a non-primary deployment; '' for the primary.
export function registrationsNote(s, name) {
  const d = dep(s, name);
  if (!d || d.primary) return '';
  const fires = d.deliveries === false ? `${name}'s cron jobs and bus subscriptions are silenced: a tile manager switched its deliveries off.`
    : `${name}'s cron jobs and bus subscriptions fire for ${name}, with its own data.`;
  return `${fires} Its interface instances and ingress hosts stay dormant: routes reach the primary, ${s.primary || 'main'}, only.`;
}

export const wouldNotifyRows = (s, name, opts = {}) => (dep(s, name)?.wouldNotify || []).map((w) => `would notify ${who(w.to)} · "${w.title}" · ${ago(w.at, opts.now)}`);

// logRows(state, name, entries, opts) → the deploy log: the entry that runs now, Roll back on older ok ones.
export function logRows(s, name, entries, opts = {}) {
  const d = dep(s, name), cur = d?.checkpoint?.id;
  let found = false;
  return (entries || []).map((e) => {
    const running = !found && e.result === 'ok' && (e.followsWorkTree ? !!d?.liveReload : same(e.checkpoint, cur)), back = e.result === 'ok' && !e.followsWorkTree && !!e.checkpoint && !same(e.checkpoint, cur);
    found ||= running;
    return { id: e.id, checkpoint: e.checkpoint || '', code: e.followsWorkTree || !e.checkpoint ? '● work tree' : `📌 ${e.checkpoint}`, how: `${HOW[e.how] || e.how}${e.how === 'promote' && e.from ? ` (from ${e.from})` : ''}`,
      result: e.result === 'failed' ? `failed${e.error ? ` — ${e.error}` : ''}` : e.result === 'running' ? `running${e.phase ? ` · ${e.phase}` : ''}` : e.result,
      who: `${who(e.by)}${e.agent ? ' (agent)' : ''} · ${ago(e.finishedAt || e.requestedAt, opts.now)}`, feed: !e.feed || e.feed === 'work-tree' ? 'work tree' : e.feed,
      state: running ? 'running' : '', rollback: back ? { label: `Roll back to ${e.checkpoint}`, ...control(s, 'rollback', name, opts) } : null };
  });
}

// diffLine(from, to, stats) → "c:3f2a1c9 → c:7b19e02 · 3 files, +40 −12".
export const diffLine = (from, to, st) => `${from || '?'} → ${to || '?'} · ${files(st?.files | 0)}, +${st?.add | 0} ${MINUS}${st?.del | 0}`;

// addDialog(state, error, recent) → the Add deployment form; recent: checkpoint ids to offer.
export function addDialog(s, error, recent = []) {
  const P = s.primary || 'main', pin = cp(s, P), man = !!s.caller?.manager, opt = (value, label) => ({ value, label });
  return { title: `Add deployment to ${s.tile}`, ...(error ? { error } : {}), message: man ? '' : 'Seeding needs a tile manager.',
    fields: [{ name: 'name', label: 'Name', placeholder: 'dev' },
      { name: 'from', label: 'Code', type: 'select', value: 'work-tree', options: [opt('work-tree', 'the work tree now (a fresh checkpoint)'), opt('primary', `${P}'s code${pin ? ` (${pin})` : ''}`), ...recent.filter((id) => id !== pin).map((id) => opt(id, id))] },
      { name: 'data', label: 'Data', type: 'select', value: 'empty', options: [opt('empty', 'start empty'), ...(man ? [opt('seed', `seed from ${P} (copies its data, which may be personal)`)] : [])] },
      { name: 'attach', type: 'checkbox', label: `Attach live reload to it — ${P} is pinned to its current code` }],
    buttons: [{ label: 'Cancel', value: null }, { label: 'Add deployment', value: 'ok', primary: true }] };
}
