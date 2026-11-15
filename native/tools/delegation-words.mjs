// native/tools/delegation-words.mjs — the web chrome's own words
// (web/agent-chrome-words.js, D232) over a corpus of xbind's answers, for the
// Swift port's parity test (XbinCore: DelegationWords.swift,
// DelegationWordsTests; D233). Run after the words or the corpus change:
//
//   node native/tools/delegation-words.mjs > native/ios/Packages/XbinCore/Tests/XbinCoreTests/Resources/delegation-words.json
//
// hack/delegation-words.test.mjs fails while the checked-in file differs.
// Times are said in UTC here and in the Swift test.
process.env.TZ = 'UTC';
const W = await import(new URL('../../web/agent-chrome-words.js', import.meta.url).href);

export const NOW = Date.parse('2026-11-16T10:16:00Z');
const t = (s) => `2026-11-16T${s}Z`;

const call = (o = {}) => ({ id: 'rq_call', user: 'ana', agent: 'xbin/wsagent', kind: 'call', method: 'POST', target: 'apps/invoices/links/rebuild?dry=0',
  tile: 'apps/invoices', contentType: 'application/json', body: '{"customer":"acme","notify":true}', bodyShown: true, bodySize: 34,
  bodySha256: 'ab12', summary: 'Rebuild the customer links so the overdue ones show.', state: 'pending', created: t('10:14:00'), expires: '2026-11-17T10:14:00Z',
  preview: { class: 'tile-write', tile: 'apps/invoices', level: 'write', role: 'writer',
    standing: { call: 'POST to apps/invoices/links/rebuild on Invoices, whatever it sends, without asking again.', tile: 'Any write to Invoices that you could make yourself, without asking again.' } }, ...o });
const session = (s = {}, o = {}) => ({ id: 'rq_sess', user: 'ana', agent: 'xbin/wsagent', kind: 'session', method: 'POST', target: 'xbin/term/sessions', tile: 'apps/orders',
  body: JSON.stringify({ cwd: 'apps/orders', kind: 'agent', prompt: 'Goal: add CSV export.\nDone when: the export test passes.' }), bodyShown: true, bodySize: 96, bodySha256: 'cd34',
  summary: 'Start Claude Code on the export.', state: 'pending', created: t('10:02:00'), expires: '2026-11-17T10:02:00Z',
  preview: { class: 'act', session: { tile: 'apps/orders', provider: 'claude', providerName: 'Claude Code', mode: 'default', asks: true, net: 'none', api: true, model: 'sonnet', ...s } }, ...o });

export const requests = [
  call(),
  call({ id: 'rq_nobody', body: '', bodySize: 0, preview: { class: 'tile-write', tile: 'apps/ledger', level: 'owner' }, tile: 'apps/ledger', method: 'DELETE', target: 'apps/ledger/rows/7' }),
  call({ id: 'rq_bin', body: '', bodyShown: false, bodyBinary: true, bodySize: 3355443, bodySha256: 'ffee', preview: { class: 'tile-write', tile: 'apps/files', level: 'terminal', body: { binary: true, size: 3355443, sha256: 'ffee' } }, tile: 'apps/files' }),
  call({ id: 'rq_named', preview: { class: 'tile-write', tile: { name: 'Team calendar', path: 'apps/cal', org: 'Sales' }, level: 'read' }, tile: 'apps/cal', bodySize: 14336, body: 'x'.repeat(40) }),
  call({ id: 'rq_approved', state: 'approved', grant: 'once', decided: t('10:16:00') }),
  call({ id: 'rq_always', state: 'approved', grant: 'always', decided: t('10:16:00') }),
  call({ id: 'rq_used', state: 'used', grant: 'once', decided: t('10:16:00') }),
  call({ id: 'rq_denied', state: 'denied', decided: t('10:15:00') }),
  call({ id: 'rq_expired', state: 'expired', expires: t('09:00:00') }),
  call({ id: 'rq_withdrawn', state: 'withdrawn' }),
  call({ id: 'rq_by_xbin', state: 'withdrawn', withdrawnBy: 'xbin', decided: t('10:15:00') }),
  // a body longer than a sheet shows at once: its first CLIP characters (code points, an emoji one), and a note that says so
  call({ id: 'rq_long', body: 'é😀ab'.repeat(1100), bodySize: Buffer.byteLength('é😀ab'.repeat(1100)) }),
  call({ id: 'rq_soon', expires: t('10:16:20'), created: '0001-01-01T00:00:00Z' }),
  session(),
  session({ asks: false, mode: 'bypassPermissions', net: 'internet', api: false, model: '', credentials: 'own' }, { id: 'rq_never' }),
  session({ resume: 'ses_1', providerName: '', model: '' }, { id: 'rq_resume' }),
  // a mode xbind doesn't vouch for (no `asks`): never the green tag
  // …whose words hold characters that reorder or hide text: the sheets show each as its escape
  session({ asks: undefined, mode: 'acceptEdits' }, { id: 'rq_unknown_mode', summary: 'Start it \u2066in a mode\u2069 xbin doesn’t vouch for.\u200B',
    body: JSON.stringify({ cwd: 'apps/orders', kind: 'agent', prompt: 'Goal: export the \u202Einvoices\u202C.\nDone when: the test passes.' }) }),
  session({ asks: 'yes', mode: 'odd' }, { id: 'rq_odd_asks' }),
  session({}, { id: 'rq_tmpl', kind: 'template', tile: '', target: 'xbin/templates/new', preview: { class: 'act' } }),
];

const item = (n, o = {}) => ({ n, kind: 'grant.add', args: {}, line: `Give Invoices read access to Orders (${n})`, before: 'no access', after: 'read', ...o });
export const changesets = [
  { id: 'cs_1', user: 'ana', agent: 'xbin/wsagent', title: 'Wire up the export', state: 'proposed', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [
    item(1, { why: 'The export needs it.', effects: ['Invoices restarts (about 3 s).'] }),
    item(2, { kind: 'binding.set', warnings: ['PUBLIC: anyone on the internet can reach /export.', 'OPEN PORT: listens on 0.0.0.0:8443'], before: null, after: { host: 'export.example' } }),
    item(3, { kind: 'limits.set', blocked: 'It is above Finance\'s ceiling for one tile (1024 MiB).' }),
    item(4, { kind: 'org.share', warnings: ['EGRESS: reaches the internet'] }),
    item(5, { kind: 'other.thing', warnings: ['no tag here'] }),
  ] },
  { id: 'cs_partial', title: 't', state: 'partial', created: t('09:00:00'), decided: t('09:30:00'), expires: '2026-11-23T09:00:00Z', items: [
    item(1, { result: 'applied', inverse: { method: 'DELETE', path: '/grants' } }), item(2, { result: 'applied', inverse: { method: 'DELETE', path: '/grants' } }),
    item(3, { result: 'failed', error: 'no such tile' }), item(4, { result: 'skipped' }), item(5, { result: 'rejected' }), item(6, { result: 'blocked', blocked: 'above the ceiling' }), item(7, { result: 'stale' }) ] },
  { id: 'cs_all', title: 't', state: 'applied', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'applied' }), item(2, { result: 'applied' }), item(3, { result: 'applied' })] },
  { id: 'cs_one', title: 't', state: 'applied', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'applied', inverse: {} })] },
  { id: 'cs_none', title: 't', state: 'partial', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'failed' }), item(2, { result: 'skipped' }), item(3, { result: 'skipped' })] },
  { id: 'cs_rejected', title: 't', state: 'rejected', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'rejected' })] },
  { id: 'cs_expired', title: 't', state: 'expired', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1)] },
  { id: 'cs_withdrawn', title: 't', state: 'withdrawn', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1)] },
  { id: 'cs_by_xbin', title: 't', state: 'withdrawn', withdrawnBy: 'xbin', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1)] },
  { id: 'cs_ff', title: 't', state: 'partial', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'applied' }), item(2, { result: 'failed', error: 'x' }), item(3, { result: 'stale' })] },
];

export const approvals = [
  { id: 'ap_1', user: 'ana', tile: 'apps/invoices', scope: 'call', method: 'POST', path: 'apps/invoices/links/rebuild', created: t('09:00:00') },
  { id: 'ap_2', user: 'ana', tile: 'apps/orders', scope: 'tile', until: t('10:40:00'), created: t('09:40:00') },
  { id: 'ap_3', user: 'ana', tile: 'apps/orders', scope: 'tile', until: t('10:00:00'), created: t('09:00:00') },
];

export const audit = [
  { ts: t('10:16:00'), method: 'POST', path: '/api/apps/invoices/links/rebuild', tile: 'apps/invoices', class: 'tile-write', effect: true, status: 200, ticket: 'rq_call' },
  { ts: t('10:15:00'), method: 'POST', path: '/api/xbin/delegate/requests/rq_call/approve', tile: 'apps/invoices', class: 'decision:approve', status: 200, under: 'once' },
  { ts: t('10:14:30'), method: 'POST', path: '/api/xbin/delegate/requests/rq_x/approve', tile: 'apps/orders', class: 'decision:approve', status: 200, under: 'hour tile' },
  { ts: t('10:14:20'), method: 'POST', path: '/api/xbin/delegate/requests/rq_y/deny', tile: 'apps/orders', class: 'decision:deny', status: 200 },
  { ts: t('10:14:10'), method: 'DELETE', path: '/api/xbin/delegate/approvals/ap_1', tile: 'apps/orders', class: 'decision:revoke', status: 204 },
  { ts: t('10:03:00'), method: 'POST', path: '/api/xbin/term/sessions', tile: 'apps/orders', class: 'act', effect: true, status: 200, under: 'preference' },
  { ts: t('10:02:00'), method: 'GET', path: '/api/apps/ledger/rows', tile: 'apps/ledger', class: 'tile-read', status: 403, refused: 'Finance allows private models only' },
  { ts: t('10:01:00'), method: 'POST', path: '/api/xbin/grants', class: 'changeset:grant.add', effect: true, status: 200 },
  { ts: t('10:00:30'), method: 'POST', path: '/api/xbin/bindings', class: 'changeset:binding.set', effect: true, status: 409 },
  { ts: t('10:00:00'), method: 'PUT', path: '/api/apps/orders/rows/3', tile: 'apps/orders', class: 'tile-write', effect: true, status: 200, under: 'approval:ap_2' },
  { ts: t('09:59:00'), method: 'GET', path: '/api/apps/orders/rows', tile: 'apps/orders', class: 'tile-read', status: 200, n: 12 },
  { ts: t('09:58:00'), method: 'POST', path: '/api/apps/orders/rows', tile: 'apps/orders', class: 'tile-write', dryRun: true, status: 200 },
  { ts: t('09:57:00'), method: 'POST', path: '/api/xbin/delegate/requests', class: 'act', status: 200 },
  { ts: t('09:56:00'), method: 'DELETE', path: '/api/xbin/delegate/requests/rq_1', class: 'act', status: 200 },
  { ts: t('09:55:00'), method: 'POST', path: '/api/xbin/delegate/changesets', class: 'act', status: 200 },
  { ts: t('09:54:00'), method: 'POST', path: '/api/xbin/delegate/sessions', class: 'act', status: 200 },
  { ts: t('09:53:00'), method: 'PATCH', path: '/api/xbin/delegate/sessions/h1', class: 'act', status: 200 },
  { ts: t('09:52:00'), method: 'DELETE', path: '/api/xbin/delegate/sessions/h1', class: 'act', status: 204 },
  { ts: t('09:51:00'), method: 'GET', path: '/api/xbin/whoami', class: 'read', status: 200 },
];

const S = (o = {}) => ({ user: 'ana', workspace: true, paused: false, adminChanges: false, canAdminChanges: false, pendingRequests: 2, standingApprovals: 1, openChangeSets: 1, access: true, agent: 'xbin/wsagent', workspaceAt: t('09:12:00'), ...o });
export const states = [
  { name: 'page', state: S(), llm: { configured: true }, admin: false },
  { name: 'no-state', state: null, llm: null, admin: false },
  { name: 'no-access', state: S({ access: false, agent: '' }), llm: { configured: true }, admin: false },
  { name: 'never-on', state: S({ workspace: false, workspaceAt: undefined }), llm: { configured: true }, admin: false },
  { name: 'never-on-admin', state: S({ workspace: false, workspaceAt: undefined, who: { everyone: false, entries: 0, chosen: false } }), llm: { configured: false }, admin: true },
  { name: 'never-on-orgadmin', state: S({ workspace: false, workspaceAt: undefined, canAdminChanges: true }), llm: { configured: true }, admin: false },
  { name: 'off', state: S({ workspace: false }), llm: { configured: true }, admin: false },
  { name: 'nobody-chosen', state: S({ who: { everyone: false, entries: 0, chosen: false } }), llm: { configured: true }, admin: true },
  { name: 'chosen-some', state: S({ who: { everyone: false, entries: 3, chosen: true } }), llm: { configured: true, host: true }, admin: true },
  { name: 'no-model', state: S(), llm: { configured: false }, admin: false },
  { name: 'paused', state: S({ paused: true, pausedAt: t('10:31:00') }), llm: { configured: true }, admin: false },
  { name: 'locked', state: S({ held: 'locked' }), llm: { configured: true }, admin: false },
  { name: 'older', state: S({ held: 'older' }), llm: { configured: true }, admin: false },
  { name: 'held-other', state: S({ held: 'unavailable', heldWhy: 'tiles run without sandboxes here.' }), llm: { configured: true }, admin: true },
  { name: 'soon', state: S({ agent: '' }), llm: null, admin: false },
  // an admin who gave others an agent and not themselves: administering it isn't having one
  { name: 'admin-without-one', state: S({ access: false, agent: '', who: { everyone: false, entries: 2, chosen: true } }), llm: { configured: true }, admin: true },
  { name: 'admin-with-one', state: S({ who: { everyone: true, entries: 0, chosen: true } }), llm: { configured: true }, admin: true },
  { name: 'admin-held-without-one', state: S({ access: false, agent: '', held: 'locked', who: { everyone: false, entries: 1, chosen: true } }), llm: { configured: true }, admin: true },
];

const out = { now: new Date(NOW).toISOString(), requests: [], changesets: [], approvals: [], audit: [], states: [], small: {} };
for (const r of requests) {
  const tl = W.tile(r);
  out.requests.push({ json: r, tile: tl, level: W.levelWords(r.preview && r.preview.level), needTitle: W.needTitle(r), heading: W.heading(r), sends: W.sends(r), sendsAll: W.sends(r, true), undo: W.undoWords(r),
    choices: W.grantChoices(r).map((c) => ({ id: c.id, label: c.label, text: c.text, wider: c.wider })),
    allow: ['once', 'call', 'tile'].map((c) => [W.allowLabel(r, c), W.allowSpoken(r, c)]),
    bodies: [W.approveBody(r, 'once'), W.approveBody(r, 'call'), W.approveBody(r, 'tile'), W.approveBody(r, 'call', 'hour'), W.approveBody(r, 'tile', 'always')],
    wider: W.widerWords(r), session: r.kind === 'session' ? W.sessionRows(r) : null, settled: W.settled(r), asked: W.asked(r, NOW) });
}
for (const c of changesets) {
  const ticks = W.initialTicks(c);
  out.changesets.push({ json: c, ticks, review: W.reviewWords(c, ticks), need: W.needChangeTitle(c), results: W.resultsWords(c), ended: W.endedWords(c),
    items: c.items.map((it) => ({ tag: W.kindTag(it.kind), tickable: W.tickable(it), warnings: (it.warnings || []).map(W.warning), result: W.resultOf(it) })) });
}
for (const a of approvals) { const r = W.standingRow(a, NOW); out.approvals.push({ json: a, tile: r.tile, text: r.text, wider: r.wider, until: r.until }); }
for (const row of audit) { const d = W.did(row); out.audit.push({ json: row, text: d.text, strong: d.strong, detail: d.detail, icon: d.icon, group: d.group }); }
for (const s of states) {
  const x = { state: s.state, llm: s.llm, me: { admin: s.admin } };
  const dock = W.dockState(x);
  const count = W.needCount(s.state);
  out.states.push({ name: s.name, state: s.state, llm: s.llm, admin: s.admin, dock, chip: W.chip({ ...x, count }), stop: W.stopRefusal(s.state),
    words: W.stateWords(dock, { at: s.state && (dock === 'paused' ? s.state.pausedAt : s.state.workspaceAt), held: s.state && s.state.held, why: s.state && s.state.heldWhy, admins: [] }),
    named: W.stateWords(dock, { held: s.state && s.state.held, admins: ['Mira Okafor', 'Jonas Lind', 'Kit'] }) });
}
out.small = {
  left: [0, 59e3, 60e3, 59 * 60e3, 3600e3, 3660e3, 47 * 3600e3 + 59 * 60e3, 48 * 3600e3, 7 * 86400e3].map((ms) => [ms, W.left(ms)]),
  bytes: [0, 1, 48, 1023, 1024, 1536, 10239, 10240, 14336, 1048575, 1048576, 3355443].map((n) => [n, W.bytes(n)]),
  admins: [[], ['A'], ['A', 'B'], ['A', 'B', 'C', 'D']].map((n) => [n, W.admins(n)]),
  stepUp: [W.stepUp('password'), W.stepUp('signin')],
  needs: W.needs({ requests, changesets }, NOW).map((n) => ({ kind: n.kind, id: n.id, title: n.title, note: n.note })),
  pageViews: [...W.PAGE_VIEWS], pageMessages: [['needs'], ['approvals'], ['activity'], ['prefs'], ['setup'], ['']].map(([view]) => [view, W.pageMessage({ type: 'xbin:agent-open', view })]),
  draftFrom: { ...W.DRAFT_FROM },
  // what a sheet says in place of Allow / Apply while the agent is stopped, for each refusal xbind answers (and one it doesn't know)
  stops: ['workspace-off', 'paused', 'held', 'no-agent', 'no-sandbox', ''].flatMap((r) => ['request', 'changeset'].map((k) => [r, k, W.stopWords(r, k)])),
  // the agent's text in a sheet: each hidden character (bidi, zero-width, control but \n and \t, any Cf) its own escape
  reveal: ['plain words', 'pay \u202Eevil\u202C now', '\u2066iso\u2069 \u2067x\u2068', 'zero\u200Bwidth\u200C\u200D\u200E\u200F', 'joiner\u2060bom\uFEFF',
    'lines\nand\ttabs stay', 'cr\r\nlf', 'nul\u0000bel\u0007esc\u001B[31m del\u007F c1\u0085', 'soft\u00ADhyphen', 'tag\u{E0041}s', 'emoji 😀 é 中文', '', '\u202A\u202B\u202D']
    .map((text) => ({ text, ...W.reveal(text), revealed: W.revealed(text) })),
  hiddenNote: W.HIDDEN_NOTE,
  clip: W.CLIP,
  clips: ['', 'short', 'x'.repeat(4000), 'x'.repeat(4001), '😀'.repeat(4002), `${'a'.repeat(3999)}😀b`].map((input) => ({ input, ...W.clipText(input), note: W.clipNote(W.clipText(input)) })),
  standingNote: W.STANDING_NOTE, didNote: W.DID_NOTE, arm: W.CHOICES.armMs, duration: W.CHOICES.duration,
};
// ---- the app's Debug demo (-XbinAgentDemo <state>, native/AGENTS.md): the same corpus, as xbind would answer it ----
//
//   node native/tools/delegation-words.mjs --demo-swift > native/ios/App/Model/AgentDemoData.swift
//
// What the app's chrome shows for states a test xbind can't be put in (a
// held agent, the recent-sign-in step, an admin before setup, a start in a
// mode that never asks) is drawn from these answers, so the demo shows what
// the vectors pin. hack/delegation-words.test.mjs fails while the file is stale.
const byId = (list, id) => list.find((x) => x.id === id);
export const demo = {
  requests: ['rq_call', 'rq_never', 'rq_unknown_mode', 'rq_sess'].map((id) => byId(requests, id)),
  changeset: byId(changesets, 'cs_1'),
  approvals,
  audit,
  states: Object.fromEntries(states.filter((x) => x.state).map((x) => [x.name, { state: x.state, llm: x.llm }])),
};
const Q3 = '"'.repeat(3);
export function demoSwift() {
  const json = JSON.stringify(demo, null, 1);
  if (json.includes(`${Q3}#`)) throw new Error('the demo JSON would end the Swift raw string');
  return [
    '// Generated by `node native/tools/delegation-words.mjs --demo-swift` from the',
    "// corpus the delegation words' vectors are made of — do not edit: change the",
    '// corpus and regenerate (hack/delegation-words.test.mjs fails while stale).',
    '//',
    "// What xbind would answer, for a Debug build's `-XbinAgentDemo <state>`",
    '// (AgentDemo.swift). Not in a Release build.',
    '#if DEBUG',
    'enum AgentDemoData {',
    `    static let json = #${Q3}`,
    json,
    `${Q3}#`,
    '}',
    '#endif',
    '',
  ].join('\n');
}

if (import.meta.url === `file://${process.argv[1]}`) {
  process.stdout.write(process.argv.includes('--demo-swift') ? demoSwift() : `${JSON.stringify(out, null, 1)}\n`);
}
export default out;
