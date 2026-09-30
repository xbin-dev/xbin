// hack/partitions-page.test.mjs — unit tests for the partitions page's
// words and data (web/partitions-kit.js; /xbin/partitions), run by `make
// js-test`: one round of reads assembled into the page's model (who the
// caller is, which tiles they manage, their own rows only), the switch
// decisions a manager sees (pending first, then declined), the mode bodies,
// the switch's words (in step with xbind's registry.SwitchDeletes), what a
// partition, an inbox and a held credential say, the typed confirmations,
// and the ledger in both directions. Every call is the person's own:
// /api/xbin/…, same-origin credentials.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  call, errorText, loadPage, assemble, mayManage, modeName, switchDeletes, deletesNothing, switchLabel, DELETES_ALL,
  decisions, modeBody, whoDecides, wipedText, switchedText, bytesText, resetConfirm, rowState, usageText, mailText,
  credentialText, credentialOutcome, credentialAsk, credentialNoun, credentialWhat, partitionedTiles, ledgerTotals, edgesInto, ledgerKind,
  durationText, timeText, zoneName,
} from '../web/partitions-kit.js';

const ok = (body, status = 200) => ({ ok: status < 300, status, body });

// a fake xbind: path → answer; records every call
function fakeFetch(routes) {
  const calls = [];
  const f = async (url, init) => {
    calls.push({ url, method: init?.method, body: init?.body ? JSON.parse(init.body) : undefined, credentials: init?.credentials });
    const r = routes[`${init?.method || 'GET'} ${url}`];
    if (!r) return { ok: false, status: 404, json: async () => ({ error: `no route ${url}` }) };
    return { ok: r.status < 300, status: r.status, json: async () => r.body };
  };
  f.calls = calls;
  return f;
}

const who = { kind: 'user', id: 'dev1', name: 'Dev One', admin: false, owned: ['apps/mine', 'apps/sw'], orgs: [{ id: 'devs', admin: true }, { id: 'ops', admin: false }] };

test('call: /api/xbin, the person\'s own session, JSON both ways; a network failure says so', async () => {
  const f = fakeFetch({ 'POST /api/xbin/partitions/stop': { status: 200, body: { ok: true } } });
  const r = await call(f, 'POST', '/partitions/stop', { tile: 'apps/a', partition: 'user:dev1' });
  assert.deepEqual(r, { ok: true, status: 200, body: { ok: true } });
  assert.deepEqual(f.calls[0], { url: '/api/xbin/partitions/stop', method: 'POST', body: { tile: 'apps/a', partition: 'user:dev1' }, credentials: 'same-origin' });
  const down = await call(async () => { throw new Error('offline'); }, 'GET', '/partitions');
  assert.equal(down.status, 0);
  assert.match(errorText(down), /can't be reached \(offline\)/);
  assert.equal(errorText({ status: 409, body: {} }), 'failed (409)');
  assert.equal(errorText({ status: 403, body: { error: 'nope' } }), 'nope');
});

test('loadPage: one round of reads, a detail per listed tile, the caller\'s own rows only', async () => {
  const f = fakeFetch({
    'GET /api/xbin/whoami': ok(who),
    'GET /api/xbin/partitions': ok({
      features: ['partitions/1', 'partitions-page/1'], policies: { partitionConsent: true, credentialResetConfirm: false },
      tiles: [{ tile: 'apps/notes', state: 'partitioned', spec: { user: true, global: true }, request: null, mine: { partition: 'user:dev1' } },
        { tile: 'apps/sw', state: 'pending', spec: { user: true, global: false }, request: { spec: null, since: '2026-09-30T10:00:00Z', declined: false } }],
      credentials: [{ id: 'h1', kind: 'invite', by: 'admin', at: '2026-09-30T09:00:00Z', until: '2026-10-01T09:00:00Z' }],
      notices: [{ id: 'n1', kind: 'credential', text: 'A sign-in link…', hold: 'h1' }],
    }),
    'GET /api/xbin/components': ok([{ path: 'apps/notes', owner: 'org:ops' }, { path: 'apps/sw', owner: 'user:dev1', partition: { state: 'pending', note: ' mine ' } }]),
    'GET /api/xbin/partitions/consents': ok({ policy: { partitionConsent: true }, consents: [{ from: 'apps/z', to: 'apps/notes' }], asked: [] }),
    'GET /api/xbin/partitions/binds': ok({ binds: [{ id: 'b1', user: 'dev1' }, { id: 'b2', user: 'dev2' }] }),
    'GET /api/xbin/partitions/ledger?days=30': ok({ days: 30, rows: [{ tile: 'apps/z', day: '2026-09-29', kind: 'edge', target: 'apps/notes', count: 3 }] }),
    'GET /api/xbin/partitions?tile=apps%2Fnotes': ok({ tile: 'apps/notes', partitions: [{ user: 'dev2', bytes: 9 }, { user: 'dev1', bytes: 5, running: true }],
      trust: { writers: ['dev3'], providers: [] }, binds: [{ id: 'b1', user: 'dev1', slot: 'mcp', provider: 'apps/mine' }, { id: 'b2', user: 'dev2', slot: 'mcp', provider: 'users/dev2/mcp' }] }),
    'GET /api/xbin/partitions?tile=apps%2Fsw': ok({ tile: 'apps/sw', partitions: [] }),
  });
  const m = await loadPage(f);
  assert.equal(f.calls.length, 8);
  assert.ok(f.calls.every((c) => c.method === 'GET' && c.credentials === 'same-origin'));
  assert.equal(m.me.id, 'dev1');
  assert.ok(m.people && m.features.has('partitions-page/1') && m.policies.partitionConsent && !m.policies.credentialResetConfirm);
  assert.equal(m.tiles.length, 2);
  const [notes, sw] = m.tiles;
  assert.deepEqual(notes.rows.map((r) => r.user), ['dev1'], 'another person\'s row (an admin\'s view) is never the caller\'s');
  assert.equal(notes.manage, false, 'org ops owns it and dev1 isn\'t its admin');
  assert.equal(sw.manage, true, 'dev1 owns it');
  assert.equal(sw.note, 'mine');
  assert.deepEqual(sw.from, { user: true, global: false });
  assert.deepEqual(sw.to, { user: false, global: false });
  assert.deepEqual(m.binds.map((b) => b.id), ['b1'], 'only the caller\'s personal binds');
  assert.deepEqual(notes.binds.map((b) => b.id), ['b1'], 'a tile\'s personal binds: the caller\'s only');
  assert.equal(m.credentials[0].id, 'h1');
  assert.equal(m.consents.policy, true);
  assert.deepEqual(m.errors, []);
});

test('assemble: an older xbind (404), the root token, view-as, a PersonOnly refusal', () => {
  const m = assemble({ who: ok({ kind: 'root', id: 'root', admin: true }), ov: { ok: false, status: 404, body: {} },
    comps: ok([]), consents: { ok: false, status: 403, body: { error: 'a person\'s own act' } }, binds: { ok: false, status: 403, body: { error: 'x' } }, ledger: ok({}) });
  assert.match(m.errors[0], /no partitioned tiles/);
  assert.equal(m.people, false);
  assert.equal(m.consents.error, 'a person\'s own act');
  assert.deepEqual(m.tiles, []);
  const v = assemble({ who: ok({ kind: 'user', id: 'dev1', impersonatedBy: 'admin', readOnly: true }), ov: ok({ tiles: [] }), comps: ok([]),
    consents: ok({}), binds: ok({}), ledger: ok({}) });
  assert.equal(v.people, false);
  assert.ok(v.me.readOnly);
  assert.equal(mayManage(v.me, 'apps/x', 'user:dev1'), false, 'view-as decides nothing');
});

test('an admin\'s answers carry every person\'s rows and binds: the admin\'s page shows none of them as theirs', () => {
  const detail = ok({ tile: 'apps/notes', partitions: [{ user: 'alice', partition: 'user:alice', bytes: 9 }],
    trust: { writers: [], providers: [] }, binds: [{ id: 'b9', user: 'alice', requester: 'apps/notes', slot: 'mcp', provider: 'users/alice/mcp' }] });
  const m = assemble({ who: ok({ kind: 'user', id: 'admin', admin: true }), comps: ok([{ path: 'apps/notes', owner: '' }]),
    ov: ok({ tiles: [{ tile: 'apps/notes', state: 'partitioned', spec: { user: true } }] }),
    consents: ok({}), binds: ok({ binds: [{ id: 'b9', user: 'alice' }] }), ledger: ok({}), details: [detail] });
  const [notes] = m.tiles;
  assert.deepEqual(notes.rows, [], 'alice\'s partition is not the admin\'s');
  assert.deepEqual(notes.binds, [], 'nor is her personal bind');
  assert.deepEqual(m.binds, []);
  assert.equal(notes.manage, true);
  const anon = assemble({ who: ok({}), ov: ok({ tiles: [{ tile: 'apps/notes', state: 'partitioned', spec: { user: true } }] }), comps: ok([]),
    consents: ok({}), binds: ok({ binds: [{ id: 'b0' }] }), ledger: ok({}), details: [ok({ partitions: [{ bytes: 1 }], binds: [{ id: 'b0' }] })] });
  assert.deepEqual([anon.binds, anon.tiles[0].rows, anon.tiles[0].binds], [[], [], []], 'no id: nothing is anyone\'s');
});

test('mayManage: an admin, the owner, an admin of the owning org — no one else', () => {
  const me = { id: 'dev1', owned: new Set(), adminOrgs: new Set(['devs']) };
  assert.ok(mayManage({ ...me, admin: true }, 'apps/x', ''));
  assert.ok(mayManage(me, 'apps/x', 'user:dev1'));
  assert.ok(mayManage(me, 'apps/x', 'org:devs'));
  assert.ok(!mayManage(me, 'apps/x', 'org:ops'));
  assert.ok(!mayManage(me, 'apps/x', 'user:dev2'));
  assert.ok(!mayManage(me, 'apps/x', ''), 'an unowned tile: workspace admins only');
  assert.ok(!mayManage({ owned: new Set() }, 'apps/x', 'user:'), 'no id: no one');
});

test('decisions: a manager\'s pending requests first, then declined ones; nobody else\'s', () => {
  const t = (tile, state, declined, manage = true) => ({ tile, state, declined, manage, to: { user: true, global: false } });
  const m = { tiles: [t('apps/d', 'partitioned', true), t('apps/b', 'pending', false), t('apps/c', 'pending', false, false),
    t('apps/a', 'pending', false), { tile: 'apps/e', state: 'partitioned', to: null, manage: true }] };
  assert.deepEqual(decisions(m).map((x) => x.tile), ['apps/a', 'apps/b', 'apps/d']);
});

test('the switch\'s words are xbind\'s (registry.SwitchDeletes, SwitchDeletesAll)', () => {
  const U = { user: true }, UG = { user: true, global: true }, N = {};
  assert.equal(DELETES_ALL, 'all data in this tile');
  assert.equal(switchDeletes(N, U), DELETES_ALL);
  assert.equal(switchDeletes(UG, N), DELETES_ALL);
  assert.equal(switchDeletes(UG, U), 'the global instance\'s data and the tile\'s shared resources (people\'s partitions stay)');
  assert.equal(switchDeletes(U, UG), 'nothing (the global instance starts empty)');
  assert.ok(deletesNothing(U, UG) && !deletesNothing(N, U));
  assert.equal(switchLabel(N, U), 'Switch and delete all data');
  assert.equal(switchLabel(UG, U), 'Switch');
  assert.deepEqual([N, U, { global: true }, UG].map(modeName), ['unpartitioned', 'user', 'global', 'user + global']);
  assert.equal(whoDecides('org:devs'), 'an admin of org:devs, which owns it, or a workspace admin');
  assert.equal(whoDecides('user:dev1'), 'its owner, user:dev1, or a workspace admin');
  assert.equal(whoDecides(''), 'a workspace admin');
});

test('modeBody: the request as shown; unpartitioned is null; extras ride along', () => {
  assert.deepEqual(modeBody('apps/x', 'keep', { user: true, global: false }, { user: false, global: false }),
    { tile: 'apps/x', act: 'keep', from: { user: true, global: false }, to: null });
  assert.deepEqual(modeBody('apps/x', 'switch', {}, { user: true }, { confirm: 'apps/x', yes: true }),
    { tile: 'apps/x', act: 'switch', from: null, to: { user: true, global: false }, confirm: 'apps/x', yes: true });
});

test('a dry run\'s counts and a switch\'s answer in words', () => {
  assert.equal(wipedText({ namespaces: 2, partitions: 1, vaultKeys: 1, registrations: 0, bytes: 2048, subkeys: 3 }),
    '2 data namespaces, 1 person\'s partition, 1 vault key, 0 registrations (cron jobs, bus subscriptions, interface instances, ingress hosts), 2.0 KB; 3 backup keys erased, so that data\'s backups can\'t be read any more');
  assert.equal(bytesText(5), '5 bytes');
  assert.equal(bytesText(1), '1 byte');
  assert.equal(bytesText(3 * 1024 * 1024), '3.0 MB');
  assert.equal(switchedText('apps/x', {}, { deletes: 'all data in this tile' }), 'Switched apps/x to unpartitioned: all data in this tile deleted.');
  assert.equal(switchedText('apps/x', { user: true, global: true }, { deletes: 'nothing (the global instance starts empty)', archiver: 'offline' }),
    'Switched apps/x to user + global: nothing was deleted.\nArchiver: offline');
});

test('a person\'s partition: the typed confirmation, its state, what it holds, its inbox', () => {
  assert.equal(resetConfirm('apps/x', 'dev1'), 'apps/x user:dev1');
  assert.match(rowState(null), /no partition yet/);
  assert.equal(rowState({ state: 'dormant', why: 'dev1 can\'t read apps/x' }), 'dormant: dev1 can\'t read apps/x');
  assert.equal(rowState({ state: 'active', running: true, instance: { uptimeSec: 7200 } }), 'running for 2 hours');
  assert.match(rowState({ state: 'active', running: false }), /^stopped/);
  assert.equal(usageText({ bytes: 0, registrations: { vaultKeys: 1, cronJobs: 2, busSubscriptions: 0, missedTicks: 1 } }), '0 bytes · 1 vault key · 2 cron jobs · 1 missed tick');
  assert.equal(mailText(null), '');
  assert.equal(mailText({ pending: 2, bytes: 10, expired: 1 }), '2 items waiting (10 bytes), 1 expired');
  assert.equal(durationText(30), '30 seconds');
  assert.equal(durationText(3 * 86400), '3 days');
});

test('a held credential: what, who, when it takes effect; the decision\'s answer', () => {
  const now = Date.parse('2026-09-30T10:00:00Z');
  const h = { id: 'h1', kind: 'invite', by: 'admin', at: '2026-09-30T09:00:00Z', until: '2026-10-01T09:00:00Z' };
  const txt = credentialText(h, now);
  assert.match(txt, /^A sign-in link for your account, made by admin at /);
  assert.match(txt, /takes effect .* \(in 23 hours\) unless you refuse it\.$/);
  assert.match(credentialText({ ...h, until: '2026-09-30T09:30:00Z' }, now), /24 hours have passed/);
  assert.equal(credentialWhat({ kind: 'email', email: 'a@b.c' }), 'The single sign-on email a@b.c bound to your account');
  assert.equal(credentialWhat({ kind: 'email' }), 'A single sign-on email bound to your account');
  assert.equal(credentialWhat({ kind: 'sso-provider', issuer: 'https://id.example' }), 'A new single sign-on provider (https://id.example) for your account');
  assert.equal(credentialNoun(h), 'the sign-in link');
  assert.equal(credentialNoun({ kind: 'email', email: 'a@b.c' }), 'the single sign-on email a@b.c');
  // allowing is what asks first: who made it, when, and what it lets them do
  const ask = credentialAsk(h);
  assert.match(ask, /^Allow the sign-in link admin made at 2026-09-30 \d\d:\d\d/);
  assert.match(ask, /Whoever opens it signs in as you: allow it only if you asked for it\.$/);
  assert.match(credentialAsk({ kind: 'password' }), /^Allow the new password an admin made\? Whoever knows it signs in as you/);
  // the answers, kept on the page: a decision; xbind's own words when it was no longer waiting
  assert.deepEqual(credentialOutcome(h, ok({ decision: 'allowed' })), { text: 'Allowed: a sign-in link for your account works now.', warn: false });
  assert.deepEqual(credentialOutcome(h, ok({ decision: 'refused' })), { text: 'Refused: a sign-in link for your account was revoked.', warn: false });
  const late = 'The sign-in link admin made for your account was no longer waiting when you answered: it was used, replaced or expired. If you didn\'t use it, change your password and sign out everywhere.';
  assert.deepEqual(credentialOutcome(h, { ok: false, status: 409, body: { decision: 'already-effective', error: late } }), { text: late, warn: true });
  const gone = credentialOutcome(h, { ok: false, status: 404, body: {} });
  assert.ok(gone.warn && /change your password and sign out everywhere/.test(gone.text));
  assert.equal(credentialOutcome(h, { ok: false, status: 0, body: { error: 'offline' } }), null, 'not decided: the card stays, with the error');
  assert.equal(credentialOutcome(h, { ok: false, status: 403, body: { error: 'a person\'s own act' } }), null);
});

test('the ledger: totals per tile, kind and target; which tiles used your data in one', () => {
  const rows = [
    { tile: 'apps/z', day: 'd1', kind: 'edge', target: 'apps/x', count: 2 },
    { tile: 'apps/z', day: 'd2', kind: 'edge', target: 'apps/x', count: 3 },
    { tile: 'apps/y', day: 'd1', kind: 'edge', target: 'apps/x', count: 1 },
    { tile: 'apps/x', day: 'd1', kind: 'provider', target: 'apps/llm', count: 9 },
  ];
  assert.deepEqual(ledgerTotals(rows).map((r) => `${r.tile} ${r.kind} ${r.target} ${r.count}`),
    ['apps/x provider apps/llm 9', 'apps/z edge apps/x 5', 'apps/y edge apps/x 1']);
  assert.deepEqual(ledgerTotals(rows, 'apps/x').map((r) => r.target), ['apps/llm']);
  assert.deepEqual(edgesInto(rows, 'apps/x'), [{ from: 'apps/z', count: 5 }, { from: 'apps/y', count: 1 }]);
  assert.equal(ledgerKind('edge'), 'used your data in');
});

test('times: local, with the zone named (xbind\'s notices say UTC)', () => {
  assert.equal(timeText('2026-09-30T14:08:00Z', () => 'XYZ').slice(-4), ' XYZ');
  assert.match(timeText('2026-09-30T14:08:00Z', () => ''), /^2026-09-30 \d\d:08$/);
  assert.equal(timeText('nope'), '');
  assert.equal(typeof zoneName(new Date()), 'string');
  assert.match(timeText('2026-09-30T14:08:00Z'), /^2026-09-30 \d\d:08 \S+/, 'the browser\'s zone, named');
});

test('partitionedTiles: only tiles running people\'s partitions', () => {
  const m = { tiles: [{ tile: 'apps/a', state: 'partitioned', from: { user: true } }, { tile: 'apps/b', state: 'pending', from: { user: true } },
    { tile: 'apps/g', state: 'partitioned', from: { user: false, global: true } }] };
  assert.deepEqual(partitionedTiles(m), ['apps/a']);
});
