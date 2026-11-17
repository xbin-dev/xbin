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
const P = await import(new URL('../../web/agent-plan-words.js', import.meta.url).href); // a plan's review (DelegationPlanWords.swift)
const { trustWords } = await import(new URL('../../web/agent-signins.js', import.meta.url).href); // whose tile it is, for a start's trust need (D230)

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

const web = (o = {}) => ({ id: 'rq_web', user: 'ana', agent: 'xbin/wsagent', kind: 'web', method: 'POST', target: 'xbin/delegate/web/fetch', tile: '',
  contentType: 'application/json', body: '{"url":"https://status.example.com/incidents?since=today"}', bodyShown: true, bodySize: 57, bodySha256: 'ef56',
  summary: 'To see whether the payment provider reports an outage.', state: 'pending', created: t('10:10:00'), expires: '2026-11-17T10:10:00Z',
  preview: { class: 'web', url: 'https://status.example.com/incidents?since=today', host: 'status.example.com', orgs: ['sales'], readWeb: true,
    why: 'This conversation holds data of Sales: an address can carry it out, so each page it reads is one you approve' }, ...o });
const mcp = (o = {}) => ({ id: 'rq_mcp', user: 'ana', agent: 'xbin/wsagent', kind: 'mcp', method: 'POST', target: 'xbin/delegate/mcp/call', tile: 'apps/dice',
  contentType: 'application/json', body: '{"server":"apps/dice","tool":"wipe","arguments":{"table":"main"}}', bodyShown: true, bodySize: 62, bodySha256: '9a8b',
  summary: 'You asked to clear the table.', state: 'pending', created: t('10:12:00'), expires: '2026-11-17T10:12:00Z',
  preview: { class: 'mcp', tile: 'apps/dice', server: 'apps/dice', serverName: 'Dice', tool: 'wipe', title: 'Wipe the table', description: 'Clears every die from the table.',
    readOnly: false, arguments: '{\n  "table": "main"\n}', standing: { tool: 'wipe on Dice — this tool again, with any arguments, without asking' } }, ...o });

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
  // a state this chrome doesn't know (a newer xbind's): never one that can be decided
  call({ id: 'rq_future', state: 'deferred' }),
  call({ id: 'rq_by_xbin', state: 'withdrawn', withdrawnBy: 'xbin', decided: t('10:15:00') }),
  // a body longer than a sheet shows at once: its first CLIP characters (code points, an emoji one), and a note that says so
  call({ id: 'rq_long', body: 'é😀ab'.repeat(1100), bodySize: Buffer.byteLength('é😀ab'.repeat(1100)) }),
  call({ id: 'rq_soon', expires: t('10:16:20'), created: '0001-01-01T00:00:00Z' }),
  session(),
  session({ asks: false, mode: 'bypassPermissions', net: 'internet', api: false, model: '', credentials: 'own' }, { id: 'rq_never' }),
  session({ resume: 'ses_1', providerName: '', model: '' }, { id: 'rq_resume' }),
  // a saved sign-in (D230): named by xbind's id, with who will hold it — a label in the request is never shown; one that would start signed out
  session({ model: '', credentials: 'own', signIn: 'saved', signInId: 'si_1a2b', savedSignIn: 'a label the agent wrote' }, { id: 'rq_saved_signin' }),
  session({ model: '', signIn: 'saved' }, { id: 'rq_saved_no_id' }),
  // …on a tile whose source isn't only the person's (D230, 2026-11-17): the trust need, in xbind's words — the app says "decide on the web"
  session({ model: '', signIn: 'saved', signInId: 'si_1a2b', signInTrust: { tile: 'apps/orders', ownerKind: 'person', owner: 'bob', editors: ['carol', 'dave', 'erin'], more: 2, allowed: true } }, { id: 'rq_trust_person' }),
  session({ model: '', signIn: 'saved', signInId: 'si_1a2b', signInTrust: { tile: 'apps/orders', ownerKind: 'you', editors: ['carol'], allowed: true } }, { id: 'rq_trust_own' }),
  session({ model: '', signIn: 'saved', signInId: 'si_1a2b', signInTrust: { tile: 'apps/orders', ownerKind: 'org', owner: 'Sales', allowed: true } }, { id: 'rq_trust_org' }),
  session({ model: '', signIn: 'saved', signInId: 'si_1a2b', signInTrust: { tile: 'apps/orders', ownerKind: 'workspace', editors: ['carol', 'dave'], allowed: true } }, { id: 'rq_trust_ws' }),
  session({ model: '', signIn: 'needed' }, { id: 'rq_needs_signin' }),
  // a mode xbind doesn't vouch for (no `asks`): never the green tag
  // …whose words hold characters that reorder or hide text: the sheets show each as its escape
  session({ asks: undefined, mode: 'acceptEdits' }, { id: 'rq_unknown_mode', summary: 'Start it \u2066in a mode\u2069 xbin doesn’t vouch for.\u200B',
    body: JSON.stringify({ cwd: 'apps/orders', kind: 'agent', prompt: 'Goal: export the \u202Einvoices\u202C.\nDone when: the test passes.' }) }),
  session({ asks: 'yes', mode: 'odd' }, { id: 'rq_odd_asks' }),
  session({}, { id: 'rq_tmpl', kind: 'template', tile: '', target: 'xbin/templates/new', preview: { class: 'act' } }),
  // the web and MCP tools (D239): a page is asked every time, with its whole address; a tool once or always
  web(), web({ id: 'rq_web_used', state: 'used', grant: 'once', decided: t('10:15:00') }),
  web({ id: 'rq_web_bare', body: '{"url":"HTTPS://Docs.Example.com:8443/a?b=1"}', bodySize: 44, preview: { class: 'web', url: 'HTTPS://Docs.Example.com:8443/a?b=1' } }),
  mcp(), mcp({ id: 'rq_mcp_ro', preview: { class: 'mcp', tile: 'apps/dice', server: 'apps/dice', tool: 'roll_dice', readOnly: true, arguments: '{}' } }),
  mcp({ id: 'rq_mcp_used', state: 'approved', grant: 'always', decided: t('10:16:00') }),
  // a tool that changed since it was allowed or first met: what changed, said
  mcp({ id: 'rq_mcp_changed', preview: { class: 'mcp', tile: 'apps/dice', server: 'apps/dice', serverName: 'Dice', tool: 'wipe', readOnly: true, arguments: '{}', changed: ['description', 'annotations', 'somethingNew'] } }),
  // an outside server's tool (D240): outside this workspace and its host, said; a read-only mark it isn't trusted for; once only while each call asks
  ...[['rq_mcp_out', {}], ['rq_mcp_out_ro', { tool: 'lookup', readOnly: true }], ['rq_mcp_out_trusted', { tool: 'lookup', readOnly: true, readOnlyTrusted: true }],
    ['rq_mcp_each', { each: true, orgs: ['sales'], why: 'This conversation holds data of Sales: what is sent to Tracker (mcp.tracker.example) leaves the workspace, so each call is one you approve', standing: undefined }],
    ['rq_mcp_each_bare', { each: true, standing: undefined, host: '' }]].map(([id, o]) => mcp({ id, tile: 'ext:tracker@1a2b3c4d', body: '{"server":"ext:tracker","tool":"post","arguments":{"id":"T-7"}}', bodySize: 60,
    preview: { class: 'mcp', server: 'ext:tracker', serverName: 'Tracker', host: 'mcp.tracker.example', outside: true, tool: 'post', title: '', description: 'Post a comment on a ticket.',
      readOnly: false, arguments: '{\n  "id": "T-7"\n}', standing: { tool: 'post on Tracker (outside this workspace: mcp.tracker.example) — this tool again, as it is described now, with any arguments, without asking' }, ...o } })),
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
  { id: 'cs_future', title: 't', state: 'scheduled', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1)] },
  { id: 'cs_by_xbin', title: 't', state: 'withdrawn', withdrawnBy: 'xbin', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1)] },
  { id: 'cs_ff', title: 't', state: 'partial', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [item(1, { result: 'applied' }), item(2, { result: 'failed', error: 'x' }), item(3, { result: 'stale' })] },
];

export const approvals = [
  { id: 'ap_1', user: 'ana', tile: 'apps/invoices', scope: 'call', method: 'POST', path: 'apps/invoices/links/rebuild', created: t('09:00:00') },
  { id: 'ap_2', user: 'ana', tile: 'apps/orders', scope: 'tile', until: t('10:40:00'), created: t('09:40:00') },
  { id: 'ap_3', user: 'ana', tile: 'apps/orders', scope: 'tile', until: t('10:00:00'), created: t('09:00:00') },
  { id: 'ap_4', user: 'ana', tile: 'apps/dice', scope: 'tool', method: 'MCP', path: 'wipe', created: t('09:30:00') },
  { id: 'ap_5', user: 'ana', tile: 'ext:tracker@1a2b3c4d', scope: 'tool', method: 'MCP', path: 'post', created: t('09:35:00'),
    server: { server: 'ext:tracker', name: 'Tracker', host: 'mcp.tracker.example' } }, // an outside server's tool (D240)
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
  { ts: t('09:50:00'), method: 'GET', path: 'https://status.example.com/incidents', class: 'web', status: 200, bytes: 14336, ticket: 'rq_web' },
  { ts: t('09:49:00'), method: 'GET', path: 'https://docs.example.com/', class: 'web', status: 200 },
  { ts: t('09:48:00'), method: 'GET', path: 'https://intranet.example.com/', class: 'web', status: 403, refused: 'intranet.example.com is on the workspace\'s deny list' },
  { ts: t('09:47:00'), method: 'POST', path: '/api/apps/dice/mcp', tile: 'apps/dice', class: 'mcp', effect: true, status: 200, ticket: 'rq_mcp' },
  { ts: t('09:46:00'), method: 'POST', path: '/api/apps/dice/mcp', tile: 'apps/dice', class: 'mcp', effect: true, status: 200, under: 'preference' },
  { ts: t('09:45:00'), method: 'POST', path: '/api/apps/dice/mcp', tile: 'apps/dice', class: 'mcp', effect: true, status: 502, under: 'approval:ap_4' },
  { ts: t('09:44:00'), method: 'POST', path: '/api/apps/dice/mcp', tile: 'apps/dice', class: 'mcp-read', status: 200 },
  { ts: t('09:43:00'), method: 'POST', path: '/api/apps/dice/mcp', tile: 'apps/dice', class: 'mcp', status: 403, refused: 'Using MCP tools is off for this workspace' },
  // outside servers (D240): the server and the tool, the URL's scheme and host as path
  { ts: t('09:42:00'), method: 'POST', path: 'https://mcp.tracker.example', class: 'mcp', server: 'ext:tracker', tool: 'post', effect: true, status: 200, ticket: 'rq_mcp_out', bytes: 2048 },
  { ts: t('09:41:00'), method: 'POST', path: 'https://mcp.tracker.example', class: 'mcp', server: 'ext:tracker', tool: 'lookup', effect: true, status: 200, under: 'preference' },
  { ts: t('09:40:00'), method: 'POST', path: 'https://mcp.tracker.example', class: 'mcp-read', server: 'ext:tracker', status: 200 },
  { ts: t('09:39:00'), method: 'POST', path: 'https://mcp.tracker.example', class: 'mcp', server: 'ext:tracker', tool: 'post', status: 502, refused: 'the server refused xbin\'s credentials (401)' },
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

// ---- plans (docs/protocol/delegation.md §Plans): sets as xbind answers them, each under three people's states ----
const start = (o = {}) => ({ tile: 'apps/dice', provider: 'codex', providerName: 'Codex', mode: 'default', asks: true, net: 'none', api: true, prompt: 'Goal: roll_dice.\nDone when: tools/list names it.', ...o });
const dice = (o = {}, edit = (items) => items) => ({ id: 'pl_dice', user: 'ana', agent: 'xbin/wsagent', plan: true, session: 'h1', title: 'A dice tool and an assistant', state: 'proposed', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z',
  items: edit([
    { n: 1, kind: 'tile.create', args: {}, line: 'Create the tile apps/dice from the template Starter (starter) — yours', before: { tile: null }, after: { owner: 'user:ana', tile: 'apps/dice' }, beforeText: 'nothing at apps/dice', afterText: 'a new tile, yours',
      undoByHand: "xbin doesn't delete tiles: deleting apps/dice is yours to do, from the tile's own menu" },
    { n: 2, kind: 'tile.create', args: {}, line: 'Create the tile apps/assistant from the template AI Agent (agent) — yours', before: {}, after: { tile: 'apps/assistant' } },
    { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, deferred: true, needs: [2], before: { binding: [] }, after: { binding: [{ provider: 'xbin:llm' }] }, beforeText: 'not bound',
      afterText: "the workspace's LLM connector (xbin:llm)", warnings: ["SPEND: its calls spend the workspace's LLM quota"] },
    { n: 4, kind: 'binding.set', args: {}, line: "Bind apps/assistant's mcp to apps/dice", deferred: true, needs: [1, 2], before: { binding: [] }, after: { binding: [{ provider: 'apps/dice' }] }, afterText: 'apps/dice' },
    { n: 5, kind: 'agent.start', args: {}, line: 'Start a tile agent in apps/dice — Codex, in its mode "default"', deferred: true, needs: [1], start: start() },
  ]), ...o });
export const plans = [
  dice(),
  // a person who is no admin: the admin's line carries xbind's sentence and nothing else
  dice({ id: 'pl_user' }, (it) => { it[2] = { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, needsAdmin: true, needs: [2] }; return it; }),
  // …after they applied the rest, and after they asked an admin
  dice({ id: 'pl_applied', state: 'applied' }, (it) => { it[2] = { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, needsAdmin: true, needs: [2] }; return it.map((x) => (x.n === 3 ? x : { ...x, result: 'applied' })); }),
  dice({ id: 'pl_asked', state: 'applied' }, (it) => { it[2] = { n: 3, kind: 'binding.set', args: {}, line: "Bind apps/assistant's llm to xbin:llm", admin: true, needsAdmin: true, needs: [2], result: 'asked' }; return it.map((x) => (x.n === 3 ? x : { ...x, result: 'applied' })); }),
  // a warned creation, a blocked one, a kind this client doesn't know, a start in a mode xbind doesn't vouch for
  dice({ id: 'pl_odd' }, (it) => { it[0] = { ...it[0], warnings: ['PUBLIC: x'] }; it[1] = { ...it[1], blocked: 'a tile is there already' }; it[4] = { ...it[4], needs: [], start: start({ asks: false, mode: 'full-auto', prompt: 'Goal: pay \u202Eevil\u202C now' }),
    warnings: ['ACTS WITHOUT ASKING: in the mode "full-auto" Codex edits files and runs commands in apps/dice without asking you.'] }; return [...it, { n: 6, kind: 'tile.delete', args: {}, line: 'Delete the tile apps/old' }]; }),
  // decided, with a line that waits for its tile to provide what was approved (D243) — and one held because the tile declared something else
  dice({ id: 'pl_waits', state: 'applied' }, (it) => it.map((x) => (x.n === 4 ? { ...x, result: 'waiting', wait: { component: 'apps/assistant', slot: 'mcp', tile: 'apps/dice', kind: 'http', service: 'mcp', role: 'reader', until: '2026-11-17T09:00:00Z' } } : { ...x, result: 'applied' }))),
  dice({ id: 'pl_held', state: 'applied' }, (it) => it.map((x) => (x.n === 4 ? { ...x, result: 'waiting', wait: { component: 'apps/assistant', slot: 'mcp', tile: 'apps/dice', kind: 'http', service: 'mcp', role: 'reader', until: '2026-11-17T09:00:00Z', held: 'apps/dice declares mcp for the role "writer": what you approved is the role reader' } }
    : x.n === 5 ? { ...x, result: 'lapsed', error: 'apps/dice did not provide mcp within 24 hours of the plan being applied: nothing was bound' } : { ...x, result: 'applied' }))),
  // a chain: 3 needs 2 needs 1
  { id: 'pl_chain', plan: true, title: 't', state: 'proposed', created: t('09:00:00'), expires: '2026-11-23T09:00:00Z', items: [{ n: 1, kind: 'tile.create', line: 'a' }, { n: 2, kind: 'binding.set', line: 'b', needs: [1] }, { n: 3, kind: 'agent.start', line: 'c', needs: [2], start: start({ providerName: '', asks: 'yes' }) }] },
  // an admin's copy of another person's request, and an undo set that says what it can't undo
  { id: 'pl_from', user: 'ana', agent: 'xbin/wsagent', plan: true, from: 'bob', askOf: 'x', askId: 'a1', title: 'A dice tool', state: 'proposed', created: t('09:10:00'), expires: '2026-11-23T09:00:00Z', items: [
    { n: 1, of: 3, kind: 'binding.set', args: {}, line: "Bind apps/helper's llm to xbin:llm", admin: true, before: { binding: [] }, after: { binding: [{ provider: 'xbin:llm' }] }, beforeText: 'not bound', warnings: ['SPEND: it spends the quota'] },
    { n: 2, of: 4, kind: 'grant.add', args: {}, line: 'Let apps/helper use apps/x as reader', admin: true, before: { roles: [] }, after: { roles: ['reader'] } }] },
  { id: 'pl_undo', plan: true, undoOf: 'pl_dice', title: 'Undo: A dice tool', state: 'proposed', created: t('09:20:00'), expires: '2026-11-23T09:00:00Z', byHand: ["xbin doesn't delete tiles: deleting apps/dice is yours to do, from the tile's own menu"],
    items: [{ n: 1, kind: 'binding.remove', args: {}, line: "Unbind apps/assistant's mcp", before: { binding: [{ provider: 'apps/dice' }] }, after: { binding: [] }, beforeText: 'apps/dice', afterText: 'not bound' }] },
];
// a plan decided: what was chosen applied (or line failAt failed and the rest was skipped), the rest left out
const decided = (c, chosen, failAt = 0) => ({ ...c, state: failAt ? 'partial' : 'applied', items: c.items.map((it) => (it.needsAdmin || it.result ? it
  : { ...it, result: !chosen.includes(it.n) ? 'rejected' : failAt && it.n === failAt ? 'failed' : failAt && it.n > failAt ? 'skipped' : 'applied' })) });

const out = { now: new Date(NOW).toISOString(), requests: [], changesets: [], approvals: [], audit: [], states: [], small: {}, plans: [] };
for (const c of plans) {
  const ticks = P.planTicks(c);
  const every = c.items.filter((it) => P.canTick(c, c.items.map((x) => x.n), it)).map((it) => it.n);
  out.plans.push({ json: c, need: W.needChangeTitle(c), askedWords: P.askedWords(c), waitingWords: P.waitingWords(c),
    items: c.items.map((it) => ({ tag: W.kindTag(it.kind), known: P.knownKind(it.kind), result: W.resultOf(it), before: W.stateText(it, 'before'), after: W.stateText(it, 'after'), neededBy: P.neededBy(c, it.n),
      start: it.kind === 'agent.start' ? P.startRows(it) : null })),
    ticks, words: P.planWords(c, ticks), none: P.planWords(c, []).apply,
    lines: c.items.map((it) => ({ state: P.lineState(it), can: P.canTick(c, ticks, it), alone: P.canTick(c, [], it), notes: P.lineNotes(c, it),
      off: P.setTick(c, ticks, it.n, false), on: P.setTick(c, ticks, it.n, true) })),
    kept: [c.items.map((it) => it.n), [3, 4, 5], [1, 2, 3], [6]].map((k) => P.keepTicks(c, k)),
    // after Apply: does the review close itself? — what was ticked, every line that can be, and a failure
    closes: [[ticks, 0], [every, 0], [every, every[0] || 0], [[], 0]].map(([chosen, failAt]) => { const json = decided(c, chosen, failAt); return { json, chosen, want: P.closesItself(json, chosen) }; }) });
}
for (const r of requests) {
  const tl = W.tile(r);
  out.requests.push({ json: r, tile: tl, level: W.levelWords(r.preview && r.preview.level), needTitle: W.needTitle(r), heading: W.heading(r), sends: W.sends(r), sendsAll: W.sends(r, true), undo: W.undoWords(r),
    choices: W.grantChoices(r).map((c) => ({ id: c.id, label: c.label, text: c.text, wider: c.wider })),
    allow: ['once', 'call', 'tile'].map((c) => [W.allowLabel(r, c), W.allowSpoken(r, c)]),
    bodies: [W.approveBody(r, 'once'), W.approveBody(r, 'call'), W.approveBody(r, 'tile'), W.approveBody(r, 'call', 'hour'), W.approveBody(r, 'tile', 'always')],
    tool: [W.allowLabel(r, 'tool'), W.allowSpoken(r, 'tool'), W.approveBody(r, 'tool'), W.approveBody(r, 'tool', 'hour')],
    net: r.kind === 'web' ? W.webRows(r) : r.kind === 'mcp' ? W.mcpRows(r) : null,
    wider: W.widerWords(r), session: r.kind === 'session' ? { ...W.sessionRows(r), trustWhose: r.preview?.session?.signInTrust ? trustWords(r.preview.session.signInTrust).whose : '' } : null, settled: W.settled(r), asked: W.asked(r, NOW), card: W.requestCard(r, NOW) });
}
for (const c of changesets) {
  const ticks = W.initialTicks(c);
  out.changesets.push({ json: c, ticks, review: W.reviewWords(c, ticks), need: W.needChangeTitle(c), results: W.resultsWords(c), ended: W.endedWords(c), card: W.changeSetCard(c, NOW),
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
// what the agent's view may ask the chrome: messages, and what the web's pageMessage makes of each (null: nothing)
const H = '0123456789abcdef01234567';
const bridgeCorpus = [
  { type: 'xbin:agent-open-request', id: H }, { type: 'xbin:agent-open-changeset', id: '89abcdef0123456789abcdef', items: [1, 2] },
  { type: 'xbin:agent-open-request', id: H, approve: true, grant: 'always', summary: 'approve me' },
  ...['', '.', '..', '../changesets', 'r-1.a:b_C', 'cs42', H.toUpperCase(), H.slice(1), `${H}0`, 'a b', 'a/b', 'é', 'g'.repeat(24), 7, null, ['x']]
    .flatMap((id) => [{ type: 'xbin:agent-open-request', id }, { type: 'xbin:agent-open-changeset', id }]),
  { type: 'xbin:agent-open-request' },
  { type: 'xbin:open-session', session: 's_1-A', tile: 'apps/orders' }, { type: 'xbin:open-session', session: '8f00b204e9800998', tile: '/apps/orders//' },
  { type: 'xbin:open-session', session: 's1', tile: 'apps/orders', draft: 'run rm -rf' },
  ...['', '/', 'apps//orders', 'apps/./orders', 'apps/../admin', '..', 3, null, 'a'.repeat(513)].map((tile) => ({ type: 'xbin:open-session', session: 's1', tile })),
  ...['', 'a.b', 'a:b', 'a b', 'a/b', 's'.repeat(65), 1, null].map((session) => ({ type: 'xbin:open-session', session, tile: 'apps/orders' })),
  { type: 'xbin:open-session', tile: 'apps/orders' }, { type: 'xbin:open-session', session: 's1' },
  ...['change line 40 in export.go', 'a\r\nb\rc\nd\te', 'a\r\n\r\nb\r\r\nc', 'a\u0000b\u0007c\u001B[31md\u007Fe\u009Ff', 'safe\u202Eevil\u2066x\u2069\u200B\u200F\uFEFFy',
    '\u000B\u000Cx', '  \n\t words \u00A0\n', 'y'.repeat(9000), `${'a'.repeat(7999)}😀😀`, '\u200B\u200B', '<b>x</b>', 42, ['a'], null, undefined]
    .map((draft) => ({ type: 'xbin:open-session', session: 'new', tile: 'apps/orders', ...(draft === undefined ? {} : { draft }) })),
  ...['needs', 'approvals', 'activity', 'prefs', 'setup', 'Approvals', '', 1, null].map((view) => ({ type: 'xbin:agent-open', view })),
  { type: 'xbin:agent-open' },
  ...['xbin:agent-approve', 'xbin:agent-deny', 'xbin:agent-resume', 'xbin:agent-context', 'xbin:agent-decide', 'approve', ''].map((type) => ({ type, id: H })),
  { id: H }, { type: 3, id: H },
];

out.bridge = bridgeCorpus.map((msg) => ({ msg, out: W.pageMessage(msg) }));
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
  unknown: { ...W.UNKNOWN_STATE },
  // the cards the app draws for `session` nodes, and Home's status line
  sessionCards: [
    { id: 's1', cwd: 'apps/orders', provider: 'claude', status: 'waiting_permission', pending: 1 },
    { id: 's2', status: 'running', pending: 0, questions: 2 },
    { id: 's3', status: 'idle', questions: 1, pending: 1 },
    { id: 's4', status: 'running', last: 'Writing export_test.go' },
    { id: 's5', status: 'starting' }, { id: 's6', status: 'cancelling' }, { id: 's7', status: 'idle', last: 'done' },
    { id: 's8', status: 'error' }, { id: 's9', status: 'ended', endedBy: 'restart' }, { id: 's10', status: 'ended', pending: 3 },
    { id: 's11', status: 'exited', questions: 1 }, { id: 's12', status: 'exited' }, { id: 's13', status: 'something-new', provider: 'codex' },
  ].flatMap((row, i) => [['Orders', 'Claude Code'], ['', ''], ['Orders', '']].slice(0, i < 2 ? 3 : 1).map(([tileName, harnessName]) => ({ row, tileName, harnessName, card: W.sessionCard(row, tileName, harnessName) }))),
  homeStatus: ['page', 'paused', 'setup', 'off', 'unavailable', 'held', 'soon', 'offline', 'hidden'].flatMap((dock) => [[0, 0, 0], [2, 1, 2], [1, 0, 1], [3, 3, 0]]
    .map(([tileAgents, working, count]) => ({ dock, tileAgents, working, count, out: W.homeStatus(dock, { tileAgents, working, count }) }))),
  clip: W.CLIP,
  clips: ['', 'short', 'x'.repeat(4000), 'x'.repeat(4001), '😀'.repeat(4002), `${'a'.repeat(3999)}😀b`].map((input) => ({ input, ...W.clipText(input), note: W.clipNote(W.clipText(input)) })),
  // the Preferences of the web and MCP tools (D239), from GET /delegate/state: what the person set, what the policy lets them
  readWeb: W.READ_WEB,
  netPrefs: [
    {}, { web: true, mcp: true, mcpReadOnly: true },
    { web: true, net: { web: { policy: 'read' }, mcp: { policy: 'off', why: 'Using MCP tools is off for this workspace — an admin can change it (the workspace agent’s settings)' } } },
    { web: false, mcp: true, mcpReadOnly: false, net: { web: { policy: 'read', only: ['docs.example.com', '*.wiki.test'] }, mcp: { policy: 'tiles' } } },
    { web: true, mcp: true, mcpReadOnly: true, net: { web: { policy: 'off' }, mcp: { policy: 'tiles' } } },
    { mcp: false, mcpReadOnly: true, net: { web: { policy: 'read' }, mcp: { policy: 'tiles' } } },
    // outside servers (D240): listed under the MCP toggle, by name and host
    { mcp: true, mcpReadOnly: true, net: { web: { policy: 'off' }, mcp: { policy: 'listed', servers: [{ server: 'ext:tracker', name: 'Tracker', host: 'mcp.tracker.example' }, { server: 'ext:wiki', host: 'wiki.example.org' }] } } },
    { mcp: true, net: { mcp: { policy: 'listed', servers: [] } } },
  ].map((state) => ({ state, prefs: W.netPrefs(state) })),
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
  // the demo `net` (D239): a web page and an MCP tool to approve, the web on by the workspace's policy and MCP tiles too
  netRequests: ['rq_web', 'rq_mcp', 'rq_mcp_each'].map((id) => byId(requests, id)),
  // …and an outside server listed (D240): its tool asked once only, the server in Preferences
  net: { web: { policy: 'read' }, mcp: { policy: 'listed', servers: [{ server: 'ext:tracker', name: 'Tracker', host: 'mcp.tracker.example' }] } },
  changeset: byId(changesets, 'cs_1'),
  approvals,
  audit,
  states: Object.fromEntries(states.filter((x) => x.state).map((x) => [x.name, { state: x.state, llm: x.llm }])),
  // -XbinAgentDemo plan | plan-user | plan-from: a plan as an admin, as a person who is none, and a request from another person
  plans: { plan: byId(plans, 'pl_dice'), 'plan-user': byId(plans, 'pl_user'), 'plan-from': byId(plans, 'pl_from') },
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
