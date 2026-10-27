// hack/limits.test.mjs — tile resource limits in the browser (D203), run by
// `make js-test`: web/limits-kit.js (the kinds as internal/limits has them,
// fmt as limits.Format writes, the sizes an approver types, the choices'
// states, who to ask, a tile's prompt items, the table's filters, the API
// calls' bodies), the shell's prompts.js with limit items, and
// web/bx-dismiss.js's limit key, kind and the organisations badge — with the
// four-argument call an older shell (importing /vendor/bx-dismiss.js) makes.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  KINDS, META, meta, fmt, parseValue, sizeText, step, isCPU, isVM, choiceLabel, choiceState, primaryChoice, askWho, ago, who,
  decisionText, hitText, promptItems, promptCounts, kindsOf, flags, appliesText, tableEntries, counts, target, targetQuery,
  raiseBody, errorText, limitsCall, ceilingsBody, limitKey,
} from '../web/limits-kit.js';
import { KINDS as DKINDS, limitKey as dLimitKey, normDismissed, dismiss, isDismissed, pendingCount } from '../web/bx-dismiss.js';
import { promptsByTile, promptWords } from '../workspace-template/shell/prompts.js';

const NOW = Date.parse('2026-10-27T14:30:00Z');
const choice = (pct, value, fits = true, approvable = true, why = '') => ({ pct, value, fits, approvable, ...(why ? { why } : {}), ...(pct === 50 ? { default: true } : {}) });

test('the kinds and their metadata are internal/limits\'s', () => {
  const src = readFileSync(new URL('../internal/limits/limits.go', import.meta.url), 'utf8');
  const order = /var Kinds = \[\]Kind\{([^}]*)\}/.exec(src)[1].split(',').map((s) => s.trim());
  const consts = Object.fromEntries([...src.matchAll(/^\t(\w+)\s+Kind = "(\w+)"/gm)].map((m) => [m[1], m[2]]));
  assert.deepEqual([...KINDS], order.map((c) => consts[c]), 'the same kinds, in the same order');
  for (const m of src.matchAll(/^\t(\w+):\s+\{"([^"]*)", "([^"]*)", (\d+), "([^"]*)", "([^"]*)", (\d+)\},/gm)) {
    const k = consts[m[1]];
    assert.deepEqual(META[k], { label: m[2], unit: m[3], step: +m[4], alias: m[5], manifest: m[6], min: +m[7] }, k);
  }
  assert.equal(Object.keys(META).length, KINDS.length);
  assert.equal(meta('cpuShares').label, 'cpuShares', 'a newer xbind\'s kind reads by its name');
  assert.ok(isCPU('vmVCPUs') && isCPU('sbxVCPUs') && !isCPU('memMiB'));
  assert.ok(isVM('vmMemMiB') && isVM('vmVCPUs') && !isVM('sbxMemMiB'));
});

test('fmt() writes what limits.Format writes', () => {
  const cases = [['memMiB', 2048, '2 GiB'], ['memMiB', 1536, '1.5 GiB'], ['memMiB', 512, '512 MiB'], ['memMiB', 1280, '1.2 GiB'],
    ['memMiB', 3200, '3.1 GiB'], ['memMiB', 1024, '1 GiB'], ['diskGiB', 75, '75 GiB'], ['diskGiB', 1536, '1.5 TiB'],
    ['vmVCPUs', 1, '1 vCPU'], ['sbxVCPUs', 4, '4 vCPUs'], ['sbxDiskGiB', 200, '200 GiB']];
  for (const [k, v, want] of cases) assert.equal(fmt(k, v), want, `${k} ${v}`);
  assert.equal(fmt('memMiB', null), '—');
  assert.equal(fmt('cpuShares', 7), '7');
});

test('parseValue(): sizes as manifests write them, a bare number in the kind\'s unit, vCPUs as counts', () => {
  assert.deepEqual(parseValue('memMiB', '4G'), { value: 4096 });
  assert.deepEqual(parseValue('memMiB', '512MiB'), { value: 512 });
  assert.deepEqual(parseValue('memMiB', '1.5gb'), { value: 1536 });
  assert.deepEqual(parseValue('memMiB', '3072'), { value: 3072 });
  assert.deepEqual(parseValue('memMiB', '100k'), { value: 1 }, 'rounded up to the unit');
  assert.deepEqual(parseValue('diskGiB', '100G'), { value: 100 });
  assert.deepEqual(parseValue('diskGiB', '1.5G'), { value: 2 }, 'a 1.5G disk is 2 GiB, as limits.FromBytes rounds');
  assert.deepEqual(parseValue('diskGiB', '1T'), { value: 1024 });
  assert.deepEqual(parseValue('vmVCPUs', '4'), { value: 4 });
  for (const [k, s] of [['memMiB', ''], ['memMiB', 'lots'], ['memMiB', '-1G'], ['memMiB', '4i'], ['vmVCPUs', '0'], ['vmVCPUs', '2.5'], ['sbxVCPUs', '300']]) {
    assert.ok(parseValue(k, s).error, `${k} ${JSON.stringify(s)} is refused`);
  }
});

test('sizeText(): the exact value as an approver types it, and it parses back', () => {
  for (const [k, v, want] of [['memMiB', 4096, '4G'], ['memMiB', 1536, '1536M'], ['memMiB', 2150, '2150M'], ['diskGiB', 100, '100G'], ['vmVCPUs', 8, '8'], ['sbxMemMiB', null, '']]) {
    assert.equal(sizeText(k, v), want, `${k} ${v}`);
    if (v != null) assert.equal(parseValue(k, want).value, v, `${want} parses back`);
  }
});

test('step() rounds as limits.Step: up to the kind\'s step, at least one step', () => {
  assert.equal(step('memMiB', 2048, 50), 3072);
  assert.equal(step('memMiB', 2048, 25), 2560);
  assert.equal(step('memMiB', 2048, 100), 4096);
  assert.equal(step('memMiB', 1000, 25), 1280, 'rounded up to 64 MiB');
  assert.equal(step('memMiB', 100, 25), 164, 'at least one step above');
  assert.equal(step('diskGiB', 50, 25), 63);
  assert.equal(step('vmVCPUs', 2, 25), 3, 'at least one more');
  assert.equal(step('vmVCPUs', 1, 50), 2);
});

test('choices: live when they fit and this person may approve them; the primary is +50 % when live', () => {
  const cs = [choice(25, 2560), choice(50, 3072), choice(100, 4096, false, true, 'needs +2 GiB; 0.6 GiB free after the 10% reserve')];
  assert.equal(choiceLabel(cs[1], 'memMiB'), '+50% · 3 GiB');
  assert.deepEqual(choiceState(cs[0]), { live: true, why: '' });
  assert.deepEqual(choiceState(cs[2]), { live: false, why: 'needs +2 GiB; 0.6 GiB free after the 10% reserve' });
  assert.deepEqual(choiceState(choice(25, 1, true, false)), { live: false, why: 'you can’t approve this' });
  assert.deepEqual(choiceState(choice(25, 1, false, true)), { live: false, why: 'doesn’t fit the workspace' });
  assert.deepEqual(choiceState(choice(25, 1, true, false, 'above your org’s ceiling')), { live: false, why: 'above your org’s ceiling' });
  assert.equal(primaryChoice(cs), cs[1]);
  const noDefault = [choice(25, 2560), choice(50, 3072, false), choice(100, 4096, false)];
  assert.equal(primaryChoice(noDefault), noDefault[0], 'the default greyed: the first live one');
  assert.equal(primaryChoice([choice(50, 1, false)]), null);
  assert.equal(primaryChoice(undefined), null);
});

test('askWho(), who(), ago(), decisionText(), hitText()', () => {
  assert.equal(askWho(['workspace-admin', 'org:acme']), 'a workspace admin or an admin of org acme');
  assert.equal(askWho([]), 'a workspace admin');
  assert.equal(askWho(undefined), 'a workspace admin');
  assert.equal(who('user:bob'), 'bob');
  assert.equal(who('root'), 'root');
  assert.equal(ago('2026-10-27T14:29:30Z', NOW), 'just now');
  assert.equal(ago('2026-10-27T14:00:00Z', NOW), '30 min ago');
  assert.equal(ago('2026-10-27T11:30:00Z', NOW), '3 h ago');
  assert.equal(ago('2026-10-24T14:30:00Z', NOW), '3 d ago');
  assert.equal(ago('2026-08-01T00:00:00Z', NOW), '2026-08-01');
  assert.equal(ago('0001-01-01T00:00:00Z', NOW), '', 'Go\'s zero time is no time');
  assert.equal(decisionText({ value: 3072, by: 'user:bob', at: '2026-10-27T12:30:00Z', source: 'raise' }, 'memMiB', NOW), 'raised to 3 GiB by bob, 2 h ago');
  assert.equal(decisionText({ value: 100, by: 'root', at: '2026-10-27T14:30:00Z', source: 'admin-set' }, 'diskGiB', NOW), 'set to 100 GiB by root, just now');
  assert.equal(decisionText({ value: 4096, by: 'user:al', source: 'manifest-ask' }, 'memMiB', NOW), 'ask approved up to 4 GiB by al');
  assert.equal(decisionText(null, 'memMiB'), '');
  const hit = { at: '2026-10-27T14:20:00Z', since: '2026-10-27T13:02:00Z', count: 3, value: 2048, detail: 'OOM-killed (2 processes)' };
  assert.match(hitText(hit, 'memMiB', NOW), /^OOM-killed \(2 processes\) at 2 GiB, 3× since \d\d:\d\d$/);
  assert.equal(hitText({ at: '2026-10-27T14:00:00Z', since: '0001-01-01T00:00:00Z', count: 1, value: 50 }, 'diskGiB', NOW), 'met its limit at 50 GiB, 30 min ago');
  assert.match(hitText({ ...hit, deployment: 'beta', partition: true }, 'memMiB', NOW), /\(deployment beta\) \(a person’s partition\)$/);
});

// GET /limits?pending=1 as §W shapes it
const PENDING = { pending: [
  { tile: 'apps/foo', kind: 'memMiB', unit: 'MiB', why: 'ask', ask: 4096, effective: 2048, approvable: true, mine: false, approvers: ['workspace-admin'] },
  { tile: 'apps/foo', kind: 'diskGiB', unit: 'GiB', why: 'cap', hit: { at: '2026-10-27T14:20:00Z', since: '2026-10-27T14:00:00Z', count: 1, value: 50, detail: 'at its 50 GiB quota' }, effective: 50, approvable: true, mine: false, approvers: ['workspace-admin'] },
  { tile: 'o/acme/app', kind: 'memMiB', unit: 'MiB', why: 'cap', hit: { at: '2026-10-27T14:00:00Z', since: '2026-10-27T13:00:00Z', count: 2, value: 1024 }, effective: 1024, approvable: false, mine: true, approvers: ['workspace-admin', 'org:acme'] },
  { tile: 'apps/other', kind: 'memMiB', unit: 'MiB', why: 'ask', ask: 3072, effective: 2048, approvable: false, mine: false, approvers: ['workspace-admin'] },
] };

test('limitKey(): an ask by its value, a hit by when it began; the kit re-exports bx-dismiss\'s', () => {
  assert.equal(limitKey, dLimitKey);
  assert.equal(limitKey(PENDING.pending[0]), 'apps/foo|memMiB|4096');
  assert.equal(limitKey(PENDING.pending[1]), 'apps/foo|diskGiB|cap:2026-10-27T14:00:00Z');
  assert.equal(limitKey({ tile: 't', kind: 'memMiB', why: 'cap', hit: { at: 'A', since: '0001-01-01T00:00:00Z' } }), 't|memMiB|cap:A', 'no since: at');
  assert.equal(limitKey({ tile: 't', kind: 'memMiB', why: 'cap', hit: { at: 'A' } }), 't|memMiB|cap:A');
  assert.notEqual(limitKey({ ...PENDING.pending[0], ask: 6144 }), limitKey(PENDING.pending[0]), 'a bigger ask shows again');
  assert.notEqual(limitKey({ ...PENDING.pending[1], hit: { since: '2026-10-28T00:00:00Z' } }), limitKey(PENDING.pending[1]), 'a new hit shows again');
  assert.equal(limitKey({ scope: 'apps', kind: 'diskGiB', why: 'cap', hit: { since: 'S' } }), 'apps|diskGiB|cap:S', 'a scope when no tile');
  assert.equal(limitKey(null), '||');
});

test('promptItems(): the tile\'s items this person decides or owns, less the dismissed, with their words', () => {
  const all = promptItems(PENDING, { now: NOW });
  assert.deepEqual(all.map((p) => p.key), ['apps/foo|memMiB|4096', 'apps/foo|diskGiB|cap:2026-10-27T14:00:00Z', 'o/acme/app|memMiB|cap:2026-10-27T13:00:00Z'],
    'apps/other: neither approvable nor mine');
  const foo = promptItems(PENDING, { tile: 'apps/foo', now: NOW });
  assert.equal(foo.length, 2);
  assert.equal(foo[0].head, 'Limit request');
  assert.equal(foo[0].what, '4 GiB (the manifest’s limits.memory)');
  assert.equal(foo[0].current, '2 GiB');
  assert.equal(foo[1].head, 'At its limit');
  assert.ok(foo[1].cap);
  assert.equal(foo[1].what, 'at its 50 GiB quota, 10 min ago', 'the limit once: the detail names it');
  const mine = promptItems(PENDING, { tile: 'o/acme/app', now: NOW })[0];
  assert.equal(mine.askWho, 'a workspace admin or an admin of org acme');
  const d = dismiss(null, 'limits', 'apps/foo|memMiB|4096', 't');
  assert.deepEqual(promptItems(PENDING, { tile: 'apps/foo', dismissed: d }).map((p) => p.kind), ['diskGiB']);
  assert.deepEqual(promptCounts(foo), { asks: 1, caps: 1 });
  assert.deepEqual(promptItems(null), []);
  assert.deepEqual(promptItems({ pending: [null, { kind: 'memMiB', approvable: true }] }), [], 'junk and items without a tile');
});

// GET /limits rows as §W shapes them
const ROWS = [
  { tile: 'apps/foo', scope: 'apps', scopeKey: 'apps', rootsScope: true, limits: {
    memMiB: { effective: 3072, default: 2048, max: 65536, enforced: true, set: { value: 3072, by: 'user:admin', at: '2026-10-27T12:00:00Z', source: 'raise' }, canDecide: true, canRevert: true, usage: 1800 },
    diskGiB: { effective: 50, default: 50, max: 900, enforced: true, hit: { at: 'x', count: 1, value: 50 }, canDecide: true, canRevert: true },
  } },
  { tile: 'o/acme/app', org: 'acme', limits: {
    memMiB: { effective: 1024, default: 1024, enforced: true, ask: 2048, pending: true },
    vmMemMiB: { effective: 4096, default: 2048, enforced: true, applied: 2048, appliesAt: 'next-start', approved: { value: 4096, by: 'user:a', source: 'manifest-ask' } },
    zzNew: { effective: 1, default: 1 },
  } },
];

test('rows: kinds in display order, the states, when a value applies', () => {
  assert.deepEqual(kindsOf(ROWS[1]).map(([k]) => k), ['memMiB', 'vmMemMiB', 'zzNew'], 'a newer xbind\'s kind last');
  assert.deepEqual(flags(ROWS[0].limits.memMiB), { raised: true, pending: false, cap: false, later: false });
  assert.deepEqual(flags(ROWS[0].limits.diskGiB), { raised: false, pending: false, cap: true, later: false });
  assert.deepEqual(flags(ROWS[1].limits.vmMemMiB), { raised: true, pending: false, cap: false, later: true });
  assert.equal(appliesText(ROWS[1].limits.vmMemMiB, 'vmMemMiB'), 'runs at 2 GiB now — 4 GiB applies at the backend’s next start');
  assert.equal(appliesText({ effective: 4, appliesAt: 'next-start' }, 'vmVCPUs'), 'applies at the backend’s next start');
  assert.equal(appliesText(ROWS[0].limits.memMiB, 'memMiB'), '');
});

test('tableEntries(): the chips are either-or, the org and the query narrow; counts()', () => {
  const keys = (es) => es.map((e) => `${e.row.tile}:${e.kind}${e.first ? '*' : ''}`);
  assert.equal(tableEntries(ROWS).length, 5);
  assert.deepEqual(keys(tableEntries(ROWS, { raised: true })), ['apps/foo:memMiB*', 'o/acme/app:vmMemMiB*']);
  assert.deepEqual(keys(tableEntries(ROWS, { pending: true, cap: true })), ['apps/foo:diskGiB*', 'o/acme/app:memMiB*']);
  assert.deepEqual(keys(tableEntries(ROWS, { org: 'acme', pending: true })), ['o/acme/app:memMiB*']);
  assert.deepEqual(keys(tableEntries(ROWS, { q: 'FOO' })), ['apps/foo:memMiB*', 'apps/foo:diskGiB']);
  assert.deepEqual(keys(tableEntries(ROWS, { q: 'acme' })).length, 3, 'the query matches the org too');
  assert.deepEqual(counts(ROWS), { raised: 2, pending: 1, cap: 1 });
  assert.deepEqual(tableEntries(null), []);
});

test('the API: bodies, queries, error words, one call that never throws', async () => {
  assert.deepEqual(target({ tile: 'apps/foo', scope: 'apps' }), { tile: 'apps/foo' });
  assert.deepEqual(target({ scope: 'apps~foo' }), { scope: 'apps~foo' });
  assert.equal(targetQuery({ tile: 'apps/foo' }), 'tile=apps%2Ffoo');
  assert.equal(targetQuery({ scope: 'workspace' }), 'scope=workspace');
  assert.deepEqual(raiseBody({ tile: 'apps/foo', kind: 'x' }, 'memMiB', 3072), { tile: 'apps/foo', kind: 'memMiB', value: 3072, source: 'raise' });
  assert.deepEqual(raiseBody({ tile: 'a' }, 'vmMemMiB', 4096, 'admin-set', { restart: true, force: true }),
    { tile: 'a', kind: 'vmMemMiB', value: 4096, source: 'admin-set', restart: true, force: true });
  assert.equal(errorText(409, { error: 'needs +1 GiB' }), 'needs +1 GiB');
  assert.match(errorText(404, null), /predates/);
  assert.match(errorText(0, null), /reached/);
  const calls = [];
  const fake = (status, body) => async (url, init) => { calls.push([url, init]); return { ok: status < 300, status, text: async () => (body == null ? '' : JSON.stringify(body)) }; };
  let r = await limitsCall(fake(200, { tile: { tile: 'apps/foo' } }), 'POST', '/limits', { kind: 'memMiB' });
  assert.deepEqual(r, { ok: true, status: 200, data: { tile: { tile: 'apps/foo' } }, error: '' });
  assert.deepEqual(calls[0], ['/api/xbin/limits', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"kind":"memMiB"}' }]);
  r = await limitsCall(fake(409, { error: 'doesn’t fit' }), 'POST', '/limits', {});
  assert.equal(r.ok, false); assert.equal(r.status, 409); assert.equal(r.error, 'doesn’t fit');
  r = await limitsCall(fake(204, null), 'POST', '/limits/dismiss?tile=a&kind=memMiB');
  assert.equal(r.ok, true); assert.equal(r.data, null);
  assert.deepEqual(calls.at(-1), ['/api/xbin/limits/dismiss?tile=a&kind=memMiB', { method: 'POST' }]);
  r = await limitsCall(async () => { throw new Error('offline'); }, 'GET', '/limits');
  assert.equal(r.ok, false); assert.equal(r.status, 0); assert.match(r.error, /offline/);
});

test('ceilingsBody(): what the draft changed — a value, null to remove, an error for junk', () => {
  assert.deepEqual(ceilingsBody({ memMiB: '4G', diskGiB: '' }, { diskGiB: 100 }), { body: { memMiB: 4096, diskGiB: null } });
  assert.deepEqual(ceilingsBody({ memMiB: '4G' }, { memMiB: 4096 }), { body: {} }, 'unchanged: nothing');
  assert.deepEqual(ceilingsBody({ diskGiB: '' }, {}), { body: {} }, 'empty where there was none: nothing');
  assert.deepEqual(ceilingsBody({ vmVCPUs: '8' }), { body: { vmVCPUs: 8 } });
  assert.match(ceilingsBody({ memMiB: 'lots' }).error, /^memory: /);
});

test('prompts.js: limit items land on their tile, counted and worded; dismissed and unapprovable not', () => {
  const hidden = (kind, p) => kind === 'limits' && p.kind === 'memMiB' && p.tile === 'apps/foo';
  const m = promptsByTile({ limits: PENDING, grants: { pending: [{ from: 'apps/foo' }] } }, hidden);
  assert.deepEqual(m.get('apps/foo'), { grants: 1, bindings: 0, consents: 0, n: 2, limits: 1, caps: 1 });
  assert.deepEqual(m.get('o/acme/app'), { grants: 0, bindings: 0, consents: 0, n: 1, limits: 1, caps: 1 }, 'mine, not approvable: shown');
  assert.equal(m.has('apps/other'), false, 'neither approvable nor mine');
  assert.equal(promptWords(m.get('apps/foo')), '1 grant request, 1 cap hit waiting for your decision');
  const all = promptsByTile({ limits: PENDING });
  assert.equal(promptWords(all.get('apps/foo')), '1 limit request, 1 cap hit waiting for your decision');
  assert.equal(promptWords({ n: 2, limits: 2 }), '2 limit requests waiting for your decision');
  assert.equal(promptWords({ n: 3, limits: 3, caps: 3 }), '3 cap hits waiting for your decision');
  assert.equal(promptsByTile({ limits: null }).size, 0);
  assert.equal(promptsByTile({ limits: { pending: [{ kind: 'memMiB', approvable: true }] } }).size, 0, 'an item without a tile: nowhere');
});

test('bx-dismiss: the limits kind, the 5-argument badge and the 4-argument one an older shell calls', () => {
  assert.ok(DKINDS.includes('limits'));
  assert.deepEqual(normDismissed({ limits: { 'a|memMiB|4096': 't', bad: 5 } }).limits, { 'a|memMiB|4096': 't' });
  const g = { pending: [{ from: 'a', target: 'b', role: 'r', approvable: true }] }, b = { pending: [{ component: 'c', slot: 's', kind: 'net' }] }, q = { requests: [{ manage: true }] };
  assert.equal(pendingCount(g, b, q, null), 3, 'four arguments: today\'s count');
  assert.equal(pendingCount(g, b, q, null, undefined), 3);
  assert.equal(pendingCount(g, b, q, null, PENDING), 3 + 3, 'approvable or mine: three of the four');
  const d = dismiss(dismiss(null, 'limits', 'apps/foo|memMiB|4096', 't'), 'later-kind', 'x', 't');
  assert.equal(pendingCount(g, b, q, d, PENDING), 3 + 2, 'a dismissed one leaves the badge; an unknown kind changes nothing');
  assert.equal(pendingCount(g, b, q, d), 3, 'an older shell, with limit dismissals stored: its count as before');
  assert.equal(isDismissed(d, 'limits', 'apps/foo|memMiB|4096'), true);
  assert.equal(pendingCount(null, null, null, null, null), 0);
});
