// hack/agent-chrome.test.mjs — the words and rules of xbin's chrome for the
// workspace agent (web/agent-chrome-words.js, D232), run by `make js-test`:
// what a request, a change set, the audit log and the dock's own states say,
// which choices allow what, and what the dock takes from the agent's page.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as W from '../web/agent-chrome-words.js';

const NOW = Date.parse('2026-11-16T10:16:00Z');
const call = (x = {}) => ({ id: 'r1', kind: 'call', method: 'POST', target: '/api/apps/invoices/links/rebuild', tile: 'apps/invoices', body: '{"all":true}', bodySize: 12, bodyShown: 12,
  summary: 'Rebuild the links — it is safe', state: 'pending', created: '2026-11-16T10:14:00Z', expires: '2026-11-17T10:14:00Z',
  preview: { class: 'write', tile: 'apps/invoices', level: 'write', standing: { call: 'POST to /links/rebuild on Invoices, whatever it sends, without asking again.', tile: 'Any write to Invoices.' } }, ...x });

test('a tile call: its heading, what it sends, the three choices and what each approves', () => {
  const r = call();
  assert.deepEqual(W.heading(r), { before: 'Your agent wants to change something in ', strong: 'Invoices', after: '' });
  assert.equal(W.needTitle(r), 'Approve a change to Invoices');
  assert.deepEqual(W.sends(r), { text: '{"all":true}', binary: false, clipped: false, note: '12 bytes · shown in full' });
  assert.equal(W.sends(call({ bodySize: 14336, bodyShown: 2048 })).note, 'showing the first 2 KiB of 14 KiB');
  assert.equal(W.levelWords('write'), 'you can write here');
  assert.equal(W.undoWords(r), "xbin can't undo this. Whether Invoices can is up to Invoices.");
  assert.equal(W.asked(r, NOW), `asked ${W.clock(Date.parse(r.created))} · expires in 23 h 58 min`);
  const ch = W.grantChoices(r);
  assert.deepEqual(ch.map((c) => [c.id, c.wider]), [['once', false], ['call', false], ['tile', true]]);
  assert.equal(ch[1].text, r.preview.standing.call, "xbind's own sentence for the scope");
  assert.equal(ch[2].label, 'Always, every change to Invoices');
  assert.deepEqual([W.allowLabel(r, 'once'), W.allowLabel(r, 'call'), W.allowLabel(r, 'tile')], ['Allow once', 'Allow always', 'Allow every change to Invoices']);
  assert.equal(W.allowSpoken(r, 'call'), 'Allow always, this exact action');
  assert.deepEqual(W.approveBody(r, 'once'), { grant: 'once' });
  assert.deepEqual(W.approveBody(r, 'call'), { grant: 'always', scope: 'call' }, 'the exact action: until taken back by default');
  assert.deepEqual(W.approveBody(r, 'tile'), { grant: 'hour', scope: 'tile' }, 'the whole tile: an hour by default');
  assert.deepEqual(W.approveBody(r, 'tile', 'always'), { grant: 'always', scope: 'tile' });
  assert.equal(W.widerWords(r).heading, 'Allow every change to Invoices?');
  assert.equal(W.widerWords(r).back, 'Go back');
});

test('a body nobody can read says so, and is approved only as unseen', () => {
  const r = call({ body: undefined, bodyBinary: true, bodySize: 3355443, bodySha256: 'ab12', preview: { tile: 'apps/invoices', body: { binary: true, size: 3355443, sha256: 'ab12' } } });
  assert.deepEqual(W.sends(r), { text: '', binary: true, clipped: false, note: 'not text — 3.2 MiB, sha256 ab12' });
  assert.deepEqual(W.approveBody(r, 'once'), { grant: 'once', unseen: true });
});

test('a tile agent start: the mode leads, and one that never asks is the danger wording', () => {
  const s = (session) => ({ id: 's1', kind: 'session', method: 'POST', target: '/api/xbin/term/sessions', body: JSON.stringify({ prompt: 'Fix the export' }), state: 'pending',
    preview: { session: { tile: 'apps/invoices', providerName: 'Claude Code', model: 'sonnet', mode: 'default', asks: true, net: 'none', ...session } } });
  const asks = W.sessionRows(s({}));
  assert.deepEqual([asks.mode.tag, asks.mode.tone, asks.danger], ['asks before acting', 'ok', '']);
  assert.equal(asks.agent, "Claude Code · model sonnet, through the workspace's connector");
  assert.equal(asks.network, 'Its model only. No internet.');
  assert.equal(asks.brief, 'Fix the export');
  assert.deepEqual(W.heading(s({})), { before: 'Start a tile agent in ', strong: 'Invoices', after: '?' });
  assert.equal(W.heading(s({ resume: 'abc' })).before, 'Continue the tile agent in ');
  assert.equal(W.allowLabel(s({})), 'Start');
  const never = s({ asks: false, mode: 'bypassPermissions' });
  assert.equal(W.sessionRows(never).mode.tag, 'acts without asking');
  assert.match(W.sessionRows(never).danger, /a mode that never asks/);
  assert.equal(W.allowLabel(never), 'Start without asking');
  assert.deepEqual(W.grantChoices(never), [], 'asked every time: no standing choice');
  assert.deepEqual(W.approveBody(never, 'tile', 'always'), { grant: 'once' });
});

const set = (items, x = {}) => ({ id: 'c1', title: 'Wire up the export', state: 'proposed', created: '2026-11-16T10:00:00Z', expires: '2026-11-23T10:00:00Z', items: items.map((it, i) => ({ n: i + 1, ...it })), ...x });

test('a change set: a warned item starts unticked, a blocked one cannot be ticked, the button counts', () => {
  const cs = set([{ kind: 'grant.add', line: 'Give Invoices read access to Orders' }, { kind: 'binding.set', line: 'Publish the export', warnings: ['PUBLIC: anyone on the internet can reach it'] },
    { kind: 'limits.set', line: 'Raise the memory', blocked: "It is above Finance's ceiling." }]);
  assert.deepEqual(W.initialTicks(cs), [1]);
  assert.equal(W.tickable(cs.items[2]), false);
  assert.deepEqual(W.warning(cs.items[1].warnings[0]), { tag: 'PUBLIC', icon: 'globe', text: 'anyone on the internet can reach it' });
  assert.equal(W.warning('OPEN PORT: 0.0.0.0:8080').tag, 'OPEN PORT');
  assert.equal(W.kindTag('org.share'), 'org share');
  const w = W.reviewWords(cs, [1]);
  assert.equal(w.heading, 'Your agent proposes 3 admin changes');
  assert.equal(w.apply, 'Apply 1 change…');
  assert.equal(W.reviewWords(cs, []).apply, 'Apply none');
  assert.match(w.footer, /^1 of 3 ticked · applied in order as you/);
  assert.deepEqual(W.needChangeTitle(cs), { title: '3 admin changes proposed', note: "1 has a warning · 1 can't be applied" });
});

test('results: all, some, none — and what can be undone', () => {
  const partial = set([{ kind: 'grant.add', result: 'applied', inverse: {} }, { kind: 'grant.add', result: 'applied', inverse: {} }, { kind: 'binding.set', result: 'failed', error: 'no such slot' },
    { kind: 'limits.set', result: 'skipped' }, { kind: 'org.share', result: 'rejected' }], { state: 'partial' });
  assert.deepEqual(W.resultsWords(partial), { title: '2 of 4 changes applied', banner: 'One change failed, so the one after it was skipped. The two that were applied stay.', undo: 'Undo the 2 applied…' });
  assert.equal(W.resultOf(partial.items[2]).text, 'xbin said: “no such slot”');
  assert.equal(W.resultOf({ result: 'stale' }).word, 'out of date');
  assert.equal(W.resultOf({ result: 'rejected' }).word, 'left out by you');
  assert.equal(W.resultsWords(set([{ result: 'applied' }, { result: 'applied' }, { result: 'applied' }])).title, 'All 3 changes applied');
  assert.deepEqual(W.resultsWords(set([{ result: 'stale' }])), { title: 'Nothing was applied', banner: '', undo: '' });
  assert.equal(W.stepUp('password').heading, "Confirm it's you");
  assert.equal(W.stepUp('signin').action, 'Sign in again');
});

test('needs you: oldest first, only what waits', () => {
  const rows = W.needs({
    requests: [call({ id: 'r2', created: '2026-11-16T10:15:00Z' }), call({ id: 'r1' }), call({ id: 'r0', state: 'approved' })],
    changesets: [set([{ kind: 'grant.add' }], { created: '2026-11-16T10:14:30Z' }), set([{}], { id: 'c0', state: 'applied' })],
    sessions: [{ id: 't1', cwd: 'apps/orders', providerName: 'Claude Code', pending: 1, origin: 'agent', created: '2026-11-16T10:10:00Z' }, { id: 't2', cwd: 'apps/x', pending: 0, questions: 0, status: 'idle' }],
  }, NOW);
  assert.deepEqual(rows.map((r) => `${r.kind}:${r.id}`), ['session:t1', 'request:r1', 'changeset:c1', 'request:r2']);
  assert.equal(rows[0].title, 'Orders · Claude Code asks permission');
  assert.equal(rows[0].note, 'started by your agent');
  assert.equal(rows[2].title, '1 admin change proposed');
  assert.equal(W.needCount({ pendingRequests: 2, openChangeSets: 1 }), 3);
});

test('what my agent did: a sentence a row, three filters', () => {
  const rows = [
    { ts: '2026-11-16T10:16:00Z', method: 'POST', path: '/api/apps/invoices/links/rebuild', tile: 'apps/invoices', class: 'write', status: 200, ticket: 'r1' },
    { ts: '2026-11-16T10:15:00Z', class: 'decision:approve', tile: 'apps/invoices', effect: 'once' },
    { ts: '2026-11-16T10:12:00Z', method: 'POST', path: '/api/apps/ledger/x', tile: 'apps/ledger', class: 'write', status: 403, refused: 'Finance allows private models only' },
    { ts: '2026-11-16T10:11:00Z', method: 'GET', path: '/api/apps/orders/list', tile: 'apps/orders', class: 'read', status: 200 },
    { ts: '2026-11-16T10:10:00Z', method: 'POST', path: '/api/apps/orders/x', tile: 'apps/orders', class: 'write', status: 200, under: 'call' },
    { ts: '2026-11-16T10:09:00Z', class: 'changeset:grant.add', status: 200 },
  ];
  const all = rows.map(W.did);
  assert.deepEqual(all.map((d) => d.text + d.strong), ['Changed Invoices', 'You allowed a request', 'Was refused: Ledger', 'Read Orders', 'Changed Orders', 'Applied an admin change as you']);
  assert.equal(all[0].detail, 'POST /api/apps/invoices/links/rebuild · you allowed it once');
  assert.equal(all[4].detail.endsWith('under “always, this action”'), true);
  const count = (f) => W.didByDay(rows, f).reduce((n, d) => n + d.rows.length, 0);
  assert.deepEqual([count('change'), count('refused'), count('all')], [4, 1, 6]);
});

test('the dock\'s states and the chip', () => {
  const admin = { admin: true }, on = { workspace: true, pendingRequests: 2 };
  assert.equal(W.dockState({ state: { workspace: false }, me: {} }), 'hidden', 'a non-admin before setup sees nothing');
  assert.equal(W.dockState({ state: { workspace: false }, me: admin }), 'setup');
  assert.equal(W.dockState({ state: { workspace: false, canAdminChanges: true }, me: {} }), 'setup');
  assert.equal(W.dockState({ state: on, llm: { configured: false } }), 'unavailable');
  assert.equal(W.dockState({ state: { ...on, paused: true }, llm: { configured: true } }), 'paused');
  assert.equal(W.dockState({ state: { ...on, held: 'locked', agent: 'system/agent' }, llm: { configured: true } }), 'held');
  assert.equal(W.dockState({ state: { ...on, agent: 'system/agent' }, llm: { configured: true } }), 'page');
  assert.equal(W.dockState({ state: on, llm: { configured: true } }), 'soon', 'on, and this xbin ships no agent yet');
  assert.equal(W.dockState({ state: null }), 'hidden', 'no delegation on this xbind');
  // nobody has an agent until it is given to them: nothing of it shows, on or off, once set up or not
  for (const st of [{ workspace: false, access: false }, { ...on, access: false }, { workspace: false, workspaceAt: 't', access: false }, { ...on, access: false, canAdminChanges: true }]) {
    assert.equal(W.dockState({ state: st, me: {}, llm: { configured: true } }), 'hidden', JSON.stringify(st));
    assert.equal(W.chip({ state: st, me: {}, llm: { configured: true } }).shown, false);
  }
  // an admin: the setup stays until someone was given one (the third step), then the page
  const adminOn = { ...on, access: true, agent: 'xbin/wsagent' };
  assert.equal(W.dockState({ state: { ...adminOn, who: { everyone: false, entries: 0, chosen: false } }, me: admin, llm: { configured: true } }), 'setup');
  assert.equal(W.dockState({ state: { ...adminOn, who: { everyone: true, entries: 0, chosen: true } }, me: admin, llm: { configured: true } }), 'page');
  assert.equal(W.dockState({ state: { ...on, access: true, agent: 'xbin/wsagent' }, me: {}, llm: { configured: true } }), 'page', 'a person who was given one');
  // an admin who gave other people an agent and not themselves has none: no chip, no page
  assert.equal(W.dockState({ state: { ...on, access: false, who: { everyone: false, entries: 2, chosen: true } }, me: admin, llm: { configured: true } }), 'hidden', 'administering it is not having one');
  assert.equal(W.dockState({ state: { ...on, access: false, who: { everyone: false, entries: 0, chosen: false } }, me: admin, llm: { configured: true } }), 'setup', '…and the setup is still theirs to finish');
  const third = (who) => W.setupSteps({ llm: { configured: true }, state: { workspace: true, who } })[2];
  assert.deepEqual(third({ everyone: false, entries: 0, chosen: false }).choices.map((c) => c.who), ['everyone', 'me']);
  assert.equal(third({ everyone: false, entries: 0, chosen: false }).done, false);
  assert.equal(third({ everyone: true, entries: 0, chosen: true }).done, true);
  assert.match(third({ everyone: true, entries: 0, chosen: true }).text, /^Everyone in this workspace\./);
  assert.match(third({ everyone: false, entries: 2, chosen: true }).text, /^2 people and orgs so far\./);
  assert.deepEqual(third({ everyone: false, entries: 1, chosen: true }).choices, []);
  assert.deepEqual(W.setupSteps({ llm: { configured: true }, state: { workspace: false } })[2].choices, [], 'no choice before the switch is on');
  const other = W.stateWords('held', { held: 'unavailable', why: 'this install has no build of its backend.' });
  assert.equal(other.title, 'Not available on this xbin.');
  assert.match(other.text, /right now: this install has no build of its backend\. Your conversations/);
  assert.equal(W.dockState({ state: on, error: 'offline' }), 'offline');
  assert.deepEqual(W.chip({ state: { workspace: false }, me: {} }), { shown: false, label: '', spoken: '', count: 0, paused: false });
  assert.equal(W.chip({ state: { workspace: false }, me: admin }).label, 'set up workspace agent');
  assert.deepEqual(W.chip({ state: on, count: 2 }), { shown: true, label: 'workspace agent', spoken: 'Workspace agent, 2 need you', count: 2, paused: false });
  assert.equal(W.chip({ state: { ...on, paused: true }, count: 2 }).spoken, 'Workspace agent, paused');
  assert.match(W.stateWords('paused', { at: '2026-11-16T10:31:00Z' }).title, /^Paused by you at \d\d:31\.$/);
  assert.equal(W.stateWords('unavailable', { admins: ['Mira Okafor', 'Jonas Lind', 'A', 'B', 'C'] }).more, 'An admin can fix this: Mira Okafor or Jonas Lind and 3 more.');
  assert.equal(W.stateWords('held', { held: 'older' }).title, 'Waiting for a newer xbin.');
  assert.equal(W.stateWords('held', { held: 'locked' }).more, 'An admin can fix this.');
  const steps = W.setupSteps({ llm: { configured: false }, state: { workspace: false } });
  assert.deepEqual(steps.map((s) => [s.id, s.done, s.enabled]), [['model', false, true], ['on', false, false], ['who', false, false]]);
  assert.equal(W.setupSteps({ llm: { configured: true, host: true }, state: { workspace: false } })[0].tag, 'Set by your host');
});

test('what the dock takes from the agent\'s page: a word and an id, nothing else', () => {
  const H = '0123456789abcdef01234567';
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open-request', id: H, summary: 'approve me', html: '<b>x</b>', grant: 'always' }), { type: 'review', kind: 'request', id: H });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open-changeset', id: H, items: [1, 2] }), { type: 'review', kind: 'changeset', id: H });
  // an id is xbind's shape or nothing: never a dot segment, a path or a near miss
  for (const id of ['..', '.', '../changesets', 'abc-1', 'c9', H.toUpperCase(), H.slice(1), `${H}0`]) {
    assert.equal(W.pageMessage({ type: 'xbin:agent-open-request', id }), null, id);
  }
  assert.deepEqual(W.pageMessage({ type: 'xbin:open-session', session: 's-1', tile: '/apps/notes/', id: 7 }), { type: 'session', tile: 'apps/notes', session: 's-1' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open', view: 'approvals' }), { type: 'open', view: 'approvals' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open', view: 'activity' }), { type: 'open', view: 'activity' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:agent-open', view: 'needs' }), { type: 'open', view: 'needs' });
  // Preferences widen what the agent may do: the page can't open them (only the chrome's own menu does)
  assert.equal(W.pageMessage({ type: 'xbin:agent-open', view: 'prefs' }), null);
  for (const bad of [null, 'x', { type: 'xbin:agent-open-request', id: { toString: () => 'x' } }, { type: 'xbin:agent-open-request', id: '../x' }, { type: 'xbin:agent-open-request', id: 'a'.repeat(81) },
    { type: 'xbin:agent-open-changeset' }, { type: 'xbin:open-session', session: 's1', tile: '../etc' }, { type: 'xbin:open-session', session: 'a b', tile: 'apps/x' },
    { type: 'xbin:agent-open', view: 'allow' }, { type: 'xbin:agent-approve', id: 'r1' }, { type: 'xbin:agent-decide', id: 'r1', verdict: 'allow' }, { type: 'xbin:agent-review', kind: 'request', id: 'r1' }]) {
    assert.equal(W.pageMessage(bad), null, JSON.stringify(bad));
  }
  assert.deepEqual(W.contextMessage({ tile: 'apps/notes', screen: 'Home', note: 'x' }), { type: 'xbin:agent-context', tile: 'apps/notes' });
  assert.deepEqual(W.contextMessage({ screen: 'Home' }), { type: 'xbin:agent-context', screen: 'Home' });
  assert.equal(W.contextMessage({ tile: '../x' }), null);
  assert.equal(W.clampDock(100, 1400), 360);
  // a new session of the tile's own agent: a draft the page suggests — plain text, capped, never a command,
  // and labelled as the page's, never as the person's own words
  assert.match(W.DRAFT_FROM.label, /^Suggested by your agent’s page — not your words$/);
  assert.doesNotMatch(`${W.DRAFT_FROM.text} ${W.DRAFT_FROM.copy} ${W.DRAFT_FROM.paste}`, /your (own )?words/i);
  assert.match(W.DRAFT_FROM.text, /nothing is sent until you press Send/);
  assert.deepEqual(W.pageMessage({ type: 'xbin:open-session', session: 'new', tile: 'apps/notes/', draft: '  change line 40\r\n<b>x</b>\u202e\u0007 ', send: true, grant: 'always' }),
    { type: 'new-session', tile: 'apps/notes', draft: 'change line 40\n<b>x</b>' });
  assert.deepEqual(W.pageMessage({ type: 'xbin:open-session', session: 'new', tile: 'apps/notes' }), { type: 'new-session', tile: 'apps/notes', draft: '' });
  assert.equal(W.pageMessage({ type: 'xbin:open-session', session: 'new', tile: '../x', draft: 'x' }), null);
  assert.equal(W.pageMessage({ type: 'xbin:open-session', session: 'new', tile: 'apps/notes', draft: 'y'.repeat(9000) }).draft.length, W.DRAFT_MAX);
  assert.equal(W.plainDraft({ toString: () => 'x' }), '');
  assert.equal(W.plainDraft('a\tb\nc'), 'a\tb\nc', 'tabs and newlines are kept');
  assert.equal(W.handoffLink('/apps/my notes/'), '/?tile=apps/my%20notes');
  assert.equal(W.clampDock(900, 1400), 720);
  assert.equal(W.clampDock(500, 600), 600, 'a narrow window: the whole page');
});

test('preferences: the modes a started tile agent may get — only ones that ask; the rest named, with why', () => {
  const provs = [{ id: 'claude', name: 'Claude Code', modes: [{ id: 'default', name: 'Ask before acting' }, { id: 'plan', name: 'Plan' }, { id: 'bypassPermissions', name: 'Bypass permissions', explicit: true }] },
    { id: 'bare', name: 'No table', modes: [] }, { id: 'codex', name: 'Codex', modes: [{ id: 'read-only', name: 'Read only' }] }, null];
  const rows = W.sessionModes(provs, { claude: 'plan', codex: 'full-access' });
  assert.deepEqual(rows.map((r) => [r.id, r.value, r.options.map((o) => o.id).join(), r.never.join()]), [['claude', 'plan', 'default,plan', 'Bypass permissions'], ['codex', '', 'read-only', '']],
    'a coding agent with no modes has no row; a stored mode that is not offered reads as the asking mode');
  assert.equal(W.sessionModes(provs, { claude: 'bypassPermissions' })[0].value, '', 'a mode that never asks is never the picked one');
  assert.deepEqual(W.sessionModes(null, null), []);
  assert.match(W.neverAsks(['Bypass permissions']), /^Not offered: Bypass permissions — it acts without asking you first/);
});

test('while the agent is stopped nothing can be allowed: the reason in the place of Allow, Deny and Reject stay', () => {
  const on = { workspace: true, access: true, agent: 'xbin/wsagent', paused: false };
  assert.equal(W.stopRefusal(on), '');
  assert.equal(W.stopRefusal(null), '');
  assert.equal(W.stopRefusal({ ...on, workspace: false }), 'workspace-off');
  assert.equal(W.stopRefusal({ ...on, access: false, agent: '' }), 'no-agent');
  assert.equal(W.stopRefusal({ ...on, paused: true }), 'paused');
  assert.equal(W.stopRefusal({ ...on, held: 'locked' }), 'held');
  // the switch outranks the person's own pause, as xbind's refusal does
  assert.equal(W.stopRefusal({ ...on, workspace: false, paused: true }), 'workspace-off');
  assert.equal(W.stopWords(''), null);
  const p = W.stopWords('paused');
  assert.match(p.text, /Resume it first/);
  assert.equal(p.still, 'You can still deny it.');
  assert.equal(W.stopWords('workspace-off', 'changeset').still, 'You can still reject it.');
  assert.equal(W.stopWords('workspace-off', 'changeset').title, "Can't be applied now");
  // a refusal this page doesn't know is still a refusal: said in general words, never taken for "allowed"
  assert.match(W.stopWords('no-sandbox').text, /can't act on this xbin/);
  // withdrawn by xbin, not by the agent
  assert.equal(W.settled({ state: 'withdrawn', withdrawnBy: 'xbin' }), 'Withdrawn by xbin when your agent was stopped');
  assert.equal(W.endedWords({ state: 'withdrawn', withdrawnBy: 'xbin' }), 'Withdrawn by xbin when your agent was stopped. Nothing was applied.');
  assert.equal(W.endedWords({ state: 'applied' }), '');
});

test('the agent\'s text in a sheet: every hidden character a visible escape, nothing interpreted, newline and tab kept', () => {
  assert.deepEqual(W.reveal('pay \u202Eevil\u202C now'), { parts: [{ text: 'pay ' }, { hidden: 'U+202E' }, { text: 'evil' }, { hidden: 'U+202C' }, { text: ' now' }], hidden: 2 });
  for (const c of ['\u202A', '\u202D', '\u2066', '\u2069', '\u200B', '\u200F', '\u2060', '\uFEFF', '\u0000', '\u001B', '\r', '\u00AD']) {
    assert.equal(W.reveal(`a${c}b`).hidden, 1, `U+${c.codePointAt(0).toString(16)}`);
  }
  assert.equal(W.reveal('a\nb\tc é 中 😀').hidden, 0);
  assert.equal(W.revealed('x\u202Ey'), 'x⟨U+202E⟩y');
  assert.equal(W.HIDDEN_NOTE, 'contains hidden characters — shown as ⟨U+…⟩');
});

test('a long body: the note says how much is shown — "shown in full" only when it is; one clip length, in characters', () => {
  assert.equal(W.CLIP, 4000);
  const body = 'é😀ab'.repeat(1100); // 4400 characters, 4 of them an emoji's two UTF-16 units each
  const req = { kind: 'call', body, bodySize: Buffer.byteLength(body), bodyShown: true };
  const s = W.sends(req);
  assert.equal(s.clipped, true);
  assert.equal([...s.text].length, 4000, 'the first 4000 characters, never half an emoji');
  assert.match(s.note, /· first 4000 of 4400 characters$/);
  const all = W.sends(req, true);
  assert.equal(all.text, body);
  assert.match(all.note, /· shown in full$/);
  assert.match(W.sends({ kind: 'call', body: 'x'.repeat(4000), bodySize: 4000, bodyShown: true }).note, /shown in full$/, 'exactly CLIP is whole');
  assert.equal(W.clipNote(W.clipText('x'.repeat(4001))), 'first 4000 of 4001 characters');
});

test('a state the chrome doesn\'t know is never one that can be decided', () => {
  const r = { id: 'x', kind: 'call', state: 'deferred', created: '2026-11-16T10:00:00Z', expires: '2026-11-17T10:00:00Z' };
  assert.equal(W.settled(r), W.UNKNOWN_STATE.page, 'not "Waiting for your approval"');
  assert.deepEqual(W.needs({ requests: [r], changesets: [{ id: 'c', state: 'scheduled', items: [] }] }), [], 'nothing of it waits in Needs you');
  assert.equal(W.endedWords({ state: 'scheduled' }), W.UNKNOWN_STATE.page);
  assert.equal(W.knownState({ state: 'pending' }), true);
  assert.equal(W.knownState({ state: 'scheduled' }, 'changeset'), false);
  assert.equal(W.settled({ state: 'pending' }), 'Waiting for your approval');
});

test('a draft cut at its cap never keeps half an emoji', () => {
  const d = W.plainDraft(`${'a'.repeat(7999)}😀😀`);
  assert.equal(d, 'a'.repeat(7999), 'the pair that does not fit goes whole');
  assert.equal(W.plainDraft(`${'a'.repeat(7998)}😀😀`), `${'a'.repeat(7998)}😀`);
});

test('a change\'s before and after are drawn in words, never as an object (G11)', () => {
  const it = { n: 1, kind: 'binding.set', before: { binding: [] }, after: { binding: [{ provider: 'apps/dice' }] }, beforeText: 'not bound', afterText: 'apps/dice' };
  assert.equal(W.stateText(it, 'before'), 'not bound');
  assert.equal(W.stateText(it, 'after'), 'apps/dice');
  // an xbind that sent no words: the data's fields, plainly — and never "[object Object]"
  const old = { n: 1, before: { binding: [] }, after: { binding: [{ provider: 'apps/dice' }], host: 'a' } };
  assert.equal(W.stateText(old, 'before'), 'binding: []');
  assert.equal(W.stateText(old, 'after'), 'binding: [{"provider":"apps/dice"}], host: "a"');
  assert.equal(W.stateText({ before: 'no access' }, 'before'), 'no access');
  assert.equal(W.stateText({ before: null }, 'before'), null);
  assert.equal(W.stateText({ after: {} }, 'after'), '(nothing)');
  assert.equal(W.stateText({ after: 3 }, 'after'), '3');
  for (const v of [{ a: 1 }, [1], { x: { y: 1 } }]) assert.ok(!String(W.stateText({ before: v }, 'before')).includes('[object'));
});

test('whose a created tile would be is xbind\'s word, not a guess (G1)', () => {
  const req = (owner) => ({ user: 'ada', kind: 'template', preview: owner === undefined ? {} : { create: { owner } } });
  assert.equal(W.ownerWords(req('user:ada')), 'you');
  assert.equal(W.ownerWords(req('org:sales')), 'the org sales');
  assert.equal(W.ownerWords(req('')), 'the workspace');
  assert.equal(W.ownerWords(req('user:bob')), 'bob (another person)');
  assert.equal(W.ownerWords(req(undefined)), 'xbin decides when it is created');
});
