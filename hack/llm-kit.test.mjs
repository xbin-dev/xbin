// hack/llm-kit.test.mjs — the LLM connector in the browser (D226), run by
// `make js-test`: web/llm-kit.js (the limits as internal/llm names them,
// numbers as an admin types and reads them, the host's locks, the status
// line, an upstream's form → its PUT body, a quota's form → its body, the
// sentence a person reads at a limit, usage summed per person and per
// component, sorted) and web/llm-client.js (each call's method, path and
// body; a refusal's words and its locked paths; one subject's quota).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  LIMITS, LIMIT_META, UNLIMITED, PEOPLE, fmtTokens, fmtCost, limitText, fmtLimit, parseLimit, lockOf, isLocked, fullyLocked, lockLabel, lockedPaths,
  statusOf, vaultWords, vaultShut, keyWords, modelsWords, testWords, validName, validBaseURL, parseModels, priceRows, pricesBody, upstreamBody,
  upstreamDraft, addressChanged, modelChoices, modelsDraft, modelsChanges, aliasShadows, concurrencyChange, ceilingOf, ceilingProblem, quotaDraft, quotaBody, quotaSummary, sourceWords, limitPct, usedWords, resetWords, quotaMessage, today,
  personLabel, componentLabel, modelLabel, requestMarks, tokensWords, aggregate, totalsOf, sortGroups, USAGE_COLS, costWords, tokensTitle, setupAlert, isLLMAlert,
} from '../web/llm-kit.js';
import { llmClient, errorText, quotaRules, quotaOf, quotaLockPath } from '../web/llm-client.js';

const NOW = Date.parse('2026-11-14T12:00:00Z');
const read = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');

test('the limits, the people switch and the lock reason are internal/llm\'s', () => {
  const quota = read('../internal/llm/quota.go');
  const names = /var limitNames = \[\]string\{([^}]*)\}/.exec(quota)[1].split(',').map((s) => s.trim().replace(/"/g, ''));
  assert.deepEqual([...LIMITS], names, 'the same limits, in the same order');
  assert.deepEqual(Object.keys(LIMIT_META), names);
  assert.equal(UNLIMITED, +/const Unlimited = (-?\d+)/.exec(quota)[1]);
  const config = read('../internal/llm/config.go');
  for (const p of PEOPLE) assert.ok(config.includes(`"${p.id}"`), `people: ${p.id}`);
  assert.equal(lockLabel(/const LockReason = "([^"]*)"/.exec(config)[1]), 'Set by your host');
  const alerts = read('../internal/llm/alerts.go');
  const admin = read('../internal/llm/adminalerts.go');
  assert.ok(admin.includes('AlertSetup = "llm-setup"') && admin.includes('AlertQuota = "llm-quota"') && alerts.includes('const AlertKind = "llm"'));
  assert.ok(read('../internal/llm/models.go').includes('const unknownModel = "(unknown)"'), 'the name unknown models are metered under');
});

test('numbers: tokens and cost to read', () => {
  for (const [n, want] of [[0, '0'], [950, '950'], [1500, '1.5k'], [12345, '12.3k'], [200000, '200k'], [2e6, '2M'], [1250000, '1.25M'], [3.4e9, '3.4B'], [null, '0']]) {
    assert.equal(fmtTokens(n), want, String(n));
  }
  assert.equal(fmtTokens(undefined), '—');
  for (const [n, want] of [[0, '$0'], [0.004, '$0.004'], [1.5, '$1.50'], [12.345, '$12.35'], [1250, '$1,250'], [NaN, '—']]) assert.equal(fmtCost(n), want, String(n));
});

test('a limit as typed: parseLimit and limitText go round', () => {
  for (const [name, text, value] of [['tokensPerDay', '2M', 2e6], ['tokensPerDay', '200k', 200000], ['tokensPerMonth', '1500', 1500],
    ['tokensPerDay', 'unlimited', -1], ['costPerMonth', '50', 50], ['costPerDay', '12.5', 12.5], ['costPerDay', 'unlimited', -1], ['tokensPerDay', '', 0]]) {
    assert.deepEqual(parseLimit(name, text), { value }, `${name} ${text}`);
    assert.equal(limitText(name, value), text, `${name} ${value}`);
  }
  assert.deepEqual(parseLimit('tokensPerDay', ' 1.5m '), { value: 1500000 });
  assert.deepEqual(parseLimit('tokensPerDay', '2,000,000'), { value: 2000000 });
  assert.deepEqual(parseLimit('costPerMonth', '$500'), { value: 500 });
  assert.deepEqual(parseLimit('costPerMonth', 'none'), { value: -1 });
  for (const [name, text] of [['tokensPerDay', 'lots'], ['tokensPerDay', '-5'], ['tokensPerDay', '0'], ['costPerDay', '2M'], ['costPerDay', '0'], ['tokensPerDay', '$5']]) {
    assert.ok(parseLimit(name, text).error, `${name} ${JSON.stringify(text)} is refused`);
  }
  assert.equal(fmtLimit('tokensPerDay', 2e6), '2M tokens');
  assert.equal(fmtLimit('costPerMonth', 50), '$50.00');
  assert.equal(fmtLimit('costPerMonth', -1), 'unlimited');
  assert.equal(fmtLimit('tokensPerDay', 0), 'not set');
});

test('locks: a setting is the host\'s by its own path or by "*"', () => {
  const locks = { 'defaults.model': { locked: true, reason: 'set by your host' }, 'quotas.users': { locked: true, reason: 'set by your host' } };
  assert.deepEqual(lockOf(locks, 'defaults.model'), { locked: true, reason: 'set by your host' });
  assert.deepEqual(lockOf(locks, 'people'), { locked: false, reason: '' });
  assert.ok(isLocked(locks, 'quotas.users') && !isLocked(locks, 'quotas.orgs') && !isLocked(undefined, 'people'));
  const all = { '*': { locked: true, reason: 'set by your host' } };
  assert.ok(isLocked(all, 'people') && isLocked(all, 'upstreams.anything'));
  assert.ok(fullyLocked({ locks: all }) && fullyLocked({ host: { lockAll: true } }) && !fullyLocked({ locks, host: { active: true } }));
  assert.deepEqual(lockedPaths({ locks: { ...locks, ...all } }), ['defaults.model', 'quotas.users']);
  assert.equal(lockLabel(''), 'Set by your host');
});

test('the status line', () => {
  const up = (name, source = 'vault') => ({ name, key: { set: true, source } });
  assert.equal(statusOf({ configured: false, upstreams: [] }).state, 'unconfigured');
  assert.match(statusOf({ configured: false, upstreams: [], host: { lockAll: true } }).words, /your host configures it/);
  assert.equal(statusOf(null).state, 'unconfigured');
  assert.deepEqual(statusOf({ configured: true, enabled: true, upstreams: [up('a'), up('b')], vault: 'open', problems: [] }),
    { state: 'ok', icon: 'ok', cls: 'on', words: 'On — 2 providers' });
  assert.match(statusOf({ configured: true, enabled: false, upstreams: [up('a')] }).words, /^Off — every call is refused \(switched off\)$/);
  assert.match(statusOf({ configured: true, enabled: false, upstreams: [up('a')], locks: { enabled: { locked: true } } }).words, /switched off by your host/);
  const sealed = statusOf({ configured: true, enabled: true, upstreams: [up('a'), up('house', 'host')], vault: 'sealed', problems: [] });
  assert.equal(sealed.state, 'sealed');
  assert.match(sealed.words, /1 of 2 providers can’t be reached/);
  assert.equal(statusOf({ configured: true, enabled: true, upstreams: [up('house', 'host')], vault: 'sealed', problems: [] }).state, 'ok', 'a host\'s key is a file');
  assert.equal(statusOf({ configured: true, enabled: true, upstreams: [up('a')], vault: 'open', problems: ['the default model: …'] }).words, 'On, with 1 problem');
  assert.ok(vaultShut({ vault: 'locked' }) && !vaultShut({ vault: 'plaintext' }));
  assert.match(vaultWords('sealed'), /sealed/);
  assert.equal(vaultWords('open'), '');
});

test('a key is never shown: a hint, the host\'s, or none', () => {
  assert.equal(keyWords({ set: true, source: 'vault', hint: '…cdef' }), 'ends in cdef');
  assert.equal(keyWords({ set: true, source: 'vault' }), 'set (in the vault)');
  assert.equal(keyWords({ set: true, source: 'host' }), 'set by your host');
  assert.equal(keyWords({ set: false }), 'no key');
  assert.equal(modelsWords([]), 'every model the provider lists');
  assert.equal(modelsWords(['a', 'b']), 'a, b');
});

test('a test\'s answer in words', () => {
  assert.deepEqual(testWords({ ok: true, model: 'gpt-5', ms: 120, tokens: 12 }), { ok: true, text: 'gpt-5 answered in 120 ms (12 tokens)' });
  assert.deepEqual(testWords({ ok: true, models: 3, ms: 40 }), { ok: true, text: 'reachable in 40 ms — it lists 3 models' });
  assert.deepEqual(testWords({ ok: false, status: 401, ms: 9, error: 'the upstream refused the key' }), { ok: false, text: 'the upstream refused the key' });
  assert.deepEqual(testWords({ ok: false, status: 500, ms: 9 }), { ok: false, text: 'failed (500)' });
  assert.equal(testWords({ running: true }).text, 'testing…');
  assert.equal(testWords(null).text, '');
});

test('an upstream\'s form: names, URLs, models, prices', () => {
  for (const ok of ['openai', 'local-2', 'a.b_c']) assert.equal(validName(ok), '', ok);
  for (const bad of ['', 'xbin', 'Open', '-x', 'a b', 'a/b']) assert.ok(validName(bad), JSON.stringify(bad));
  for (const ok of ['https://api.openai.com/v1', 'http://127.0.0.1:8080']) assert.equal(validBaseURL(ok), '', ok);
  for (const bad of ['', 'api.openai.com', 'ftp://x', 'https://u:p@x/v1', 'https://x/v1?k=1']) assert.ok(validBaseURL(bad), JSON.stringify(bad));
  assert.deepEqual(parseModels('gpt-5, gpt-5-mini\n  gpt-5 other'), ['gpt-5', 'gpt-5-mini', 'other']);
  assert.deepEqual(parseModels(''), []);
  const prices = { 'gpt-5': { in: 1.25, out: 10, cachedIn: 0.125 }, 'a*': { in: 1, out: 2 } };
  const rows = priceRows(prices);
  assert.deepEqual(rows, [{ model: 'a*', in: '1', out: '2', cachedIn: '' }, { model: 'gpt-5', in: '1.25', out: '10', cachedIn: '0.125' }]);
  assert.deepEqual(pricesBody(rows), { prices }, 'rows go back to the table');
  assert.deepEqual(pricesBody([...rows, { model: '', in: '', out: '' }]), { prices }, 'an empty row is dropped');
  assert.match(pricesBody([{ model: 'x', in: '1', out: '' }]).error, /both an input and an output/);
  assert.match(pricesBody([{ model: 'x', in: 'cheap', out: '1' }]).error, /USD per million/);
  assert.match(pricesBody([{ model: 'x', in: '1', out: '1' }, { model: 'x', in: '2', out: '2' }]).error, /twice/);
});

test('upstreamBody(): a new upstream names everything, a change only what changed, a key only when typed', () => {
  const draft = { ...upstreamDraft(null), name: 'openai', baseURL: 'https://api.openai.com/v1/', key: ' sk-abc ', models: 'gpt-5', private: true };
  assert.deepEqual(upstreamBody(draft, null), { name: 'openai', body: { baseURL: 'https://api.openai.com/v1', key: 'sk-abc', models: ['gpt-5'], private: true } });
  const cur = { name: 'openai', baseURL: 'https://api.openai.com/v1', key: { set: true, source: 'vault', hint: '…cabc' }, models: ['gpt-5'], private: true, prices: { 'gpt-5': { in: 1, out: 2 } } };
  const same = upstreamDraft(cur);
  assert.equal(same.key, '', 'the form never starts with a key');
  assert.deepEqual(upstreamBody(same, cur), { name: 'openai', body: {} }, 'nothing changed: an empty body, the key untouched');
  assert.deepEqual(upstreamBody({ ...same, models: '', private: false }, cur).body, { models: [], private: false });
  assert.deepEqual(upstreamBody({ ...same, key: 'sk-new' }, cur).body, { key: 'sk-new' });
  assert.deepEqual(upstreamBody({ ...same, removeKey: true }, cur).body, { key: '' });
  assert.deepEqual(upstreamBody({ ...same, prices: [] }, cur).body, { prices: {} });
  assert.match(upstreamBody({ ...draft, name: 'xbin' }, null).error, /^name:/);
  assert.match(upstreamBody({ ...draft, baseURL: 'nope' }, null).error, /^base URL:/);
  assert.match(upstreamBody({ ...draft, key: 'two words' }, null).error, /^key:/);
});

test('a changed address needs the key again: xbind drops the stored one', () => {
  assert.ok(addressChanged('https://api.openai.com/v1', 'https://evil.example/v1') && addressChanged('https://a.example/v1', 'http://a.example/v1')
    && addressChanged('http://127.0.0.1:8080/v1', 'http://127.0.0.1:8081/v1'));
  assert.ok(!addressChanged('https://a.example/v1', 'https://a.example/v2/') && !addressChanged('https://a.example', 'not a url'));
  const cur = { name: 'openai', baseURL: 'https://api.openai.com/v1', key: { set: true, source: 'vault' }, models: [], private: false };
  const moved = { ...upstreamDraft(cur), baseURL: 'https://proxy.example/v1' };
  assert.match(upstreamBody(moved, cur).error, /^key: the address changed/);
  assert.deepEqual(upstreamBody({ ...moved, key: 'sk-again' }, cur).body, { baseURL: 'https://proxy.example/v1', key: 'sk-again' });
  assert.deepEqual(upstreamBody({ ...moved, removeKey: true }, cur).body, { baseURL: 'https://proxy.example/v1', key: '' });
  assert.deepEqual(upstreamBody({ ...upstreamDraft(cur), baseURL: 'https://api.openai.com/v2' }, cur).body, { baseURL: 'https://api.openai.com/v2' }, 'a path alone keeps the key');
  assert.deepEqual(upstreamBody({ ...moved }, { ...cur, key: { set: false } }).body, { baseURL: 'https://proxy.example/v1' }, 'no key stored: nothing to ask');
});

test('the models a default may take', () => {
  const list = { data: [{ id: 'openai/gpt-5', owned_by: 'openai' }, { id: 'fast', owned_by: 'alias' }, { id: 'xbin/default', owned_by: 'xbin' }] };
  assert.deepEqual(modelChoices(list, { upstreams: [{ name: 'local', models: ['m1'] }] }), ['local/m1', 'openai/gpt-5']);
  assert.deepEqual(modelChoices(null, null), []);
});

test('default models and aliases: the form\'s rows, and only what differs is sent', () => {
  const cfg = { defaults: { model: 'main/gpt-5', roles: { harness: 'main/mini', review: 'main/gpt-5' } }, aliases: { '*': 'main/gpt-5', fast: 'main/mini' },
    locks: { 'defaults.roles.harness': { locked: true }, 'aliases.*': { locked: true } } };
  const d = modelsDraft(cfg);
  assert.deepEqual(d, { model: 'main/gpt-5', roles: [['agent', ''], ['harness', 'main/mini'], ['review', 'main/gpt-5']], aliases: [['*', 'main/gpt-5'], ['fast', 'main/mini']] });
  assert.deepEqual(modelsChanges(d, cfg), { changes: null }, 'nothing differs');
  assert.deepEqual(modelsChanges({ ...d, model: '' }, cfg), { changes: { defaults: { model: '' } } }, '"" removes the default');
  assert.deepEqual(modelsChanges({ ...d, roles: [['agent', ' main/gpt-5 '], ['harness', 'x/y'], ['review', ''], ['plan', 'main/mini']] }, cfg),
    { changes: { defaults: { roles: { agent: 'main/gpt-5', review: '', plan: 'main/mini' } } } }, 'a locked role is never named');
  assert.deepEqual(modelsChanges({ ...d, aliases: [['*', 'other/x'], ['gpt-6', 'main/gpt-5']] }, cfg),
    { changes: { aliases: { 'gpt-6': 'main/gpt-5', fast: '' } } }, 'a dropped alias is removed; a locked one stays');
  assert.match(modelsChanges({ ...d, roles: [['', 'main/x']] }, cfg).error, /role needs a name/);
  assert.match(modelsChanges({ ...d, roles: [['Default', 'main/x']] }, cfg).error, /lowercase/);
  assert.match(modelsChanges({ ...d, aliases: [['a', 'x/y'], ['a', 'x/z']] }, cfg).error, /twice/);
  assert.deepEqual(modelsDraft(null), { model: '', roles: [['agent', ''], ['harness', '']], aliases: [] });
});

test('an alias never takes over a name the configuration has; calls in flight', () => {
  const cfg = { upstreams: [{ name: 'main', models: ['gpt-5'] }, { name: 'open', models: [] }], aliases: { 'main/old': 'main/gpt-5' } };
  assert.match(aliasShadows('main/gpt-9', cfg), /names under main\//);
  assert.match(aliasShadows('gpt-5', cfg), /main lists a model of that name/);
  assert.equal(aliasShadows('gpt-6-astra', cfg), '');
  assert.equal(aliasShadows('other/x', cfg), '');
  const d = modelsDraft(cfg);
  assert.match(modelsChanges({ ...d, aliases: [...d.aliases, ['open/x', 'main/gpt-5']] }, cfg).error, /^alias open\/x: names under open\//);
  assert.deepEqual(modelsChanges(d, cfg), { changes: null }, 'one that is already there is left alone');
  assert.deepEqual(modelsChanges({ ...d, aliases: [] }, cfg), { changes: { aliases: { 'main/old': '' } } }, '…and can be removed');
  assert.deepEqual(concurrencyChange('32'), { value: 32 });
  assert.deepEqual(concurrencyChange(' '), { value: 0 }, 'empty: the default');
  for (const bad of ['0', '1025', '1.5', 'many']) assert.ok(concurrencyChange(bad).error, bad);
});

test('the host\'s default is a ceiling over the admin\'s per-person and per-org entries', () => {
  const rules = { default: { tokensPerDay: 2e6, costPerMonth: 50 }, users: {} };
  const host = { 'quotas.default': { locked: true, reason: 'set by your host' } };
  assert.deepEqual(ceilingOf(rules, host, 'user:bob'), rules.default);
  assert.deepEqual(ceilingOf(rules, host, 'org:eng'), rules.default);
  assert.equal(ceilingOf(rules, host, 'default'), null);
  assert.equal(ceilingOf(rules, host, 'workspace'), null);
  assert.equal(ceilingOf(rules, {}, 'user:bob'), null, 'the admin\'s own default is no ceiling');
  assert.equal(ceilingOf(rules, { ...host, 'quotas.users': { locked: true } }, 'user:bob'), null, 'the host\'s own entries are the host\'s to give');
  assert.equal(ceilingOf({ default: { action: 'warn' } }, host, 'user:bob'), null);
  assert.equal(ceilingProblem({ tokensPerDay: 1e6, costPerMonth: 50 }, rules.default), '', 'lower, or the same: it narrows');
  assert.equal(ceilingProblem({ tokensPerMonth: -1, costPerDay: 500 }, rules.default), '', 'a limit the host doesn\'t set is free');
  assert.match(ceilingProblem({ tokensPerDay: 3e6 }, rules.default), /^tokens per day: at most 2M tokens — your host’s default is a ceiling; a higher limit here would give nothing$/);
  assert.match(ceilingProblem({ costPerMonth: -1 }, rules.default), /^cost per month: at most \$50\.00 .* unlimited here would give nothing$/);
  assert.equal(ceilingProblem({ tokensPerDay: 3e6 }, null), '');
});

test('a quota\'s form goes round; an empty one removes the quota', () => {
  const q = { tokensPerDay: 2e6, costPerMonth: 50, action: 'warn' };
  const d = quotaDraft(q);
  assert.deepEqual(d, { action: 'warn', tokensPerDay: '2M', tokensPerMonth: '', costPerDay: '', costPerMonth: '50' });
  assert.deepEqual(quotaBody(d), { quota: q });
  assert.deepEqual(quotaBody(quotaDraft({ tokensPerDay: -1 })), { quota: { tokensPerDay: -1 } }, 'stop is the default: not written');
  assert.deepEqual(quotaBody(quotaDraft(null)), { quota: null });
  assert.deepEqual(quotaBody({ action: 'warn' }), { quota: null }, 'an action alone is no quota');
  assert.match(quotaBody({ tokensPerDay: 'many' }).error, /tokens per day/);
  assert.equal(quotaSummary(q), '2M tokens/day · $50.00/month · warns');
  assert.equal(quotaSummary({ tokensPerDay: -1, tokensPerMonth: 5000 }), 'unlimited tokens/day · 5k tokens/month · stops');
  assert.equal(quotaSummary(null), 'no limits');
  assert.equal(sourceWords('org:interns'), 'org interns');
  assert.equal(sourceWords('default'), 'the default');
  assert.equal(sourceWords('user'), 'your own quota');
  assert.equal(sourceWords('user', 'their'), 'their own quota');
});

test('a limit\'s progress and when it resets', () => {
  const l = { name: 'tokensPerDay', value: 2e6, used: 1.2e6, resets: '2026-11-15T00:00:00Z', state: 'ok', source: 'default' };
  assert.equal(limitPct(l), 60);
  assert.equal(limitPct({ ...l, used: 3e6 }), 100);
  assert.equal(limitPct({ value: 0, used: 5 }), 0);
  assert.equal(usedWords(l), '1.2M of 2M tokens');
  assert.equal(usedWords({ name: 'costPerMonth', value: 50, used: 12.4 }), '$12.40 of $50.00');
  assert.equal(resetWords(l.resets, NOW), 'in 12 h');
  assert.equal(resetWords('2026-12-01T00:00:00Z', NOW), 'in 17 d');
  assert.equal(resetWords('2026-11-14T12:30:00Z', NOW), 'in 30 min');
  assert.equal(resetWords('', NOW), '');
});

test('quotaMessage(): what a person reads at a limit — nothing below it', () => {
  const lim = (over) => ({ name: 'tokensPerDay', value: 200, used: 240, resets: '2026-11-15T00:00:00Z', state: 'stop', source: 'default', action: 'stop', ...over });
  assert.equal(quotaMessage({ limits: [lim({ state: 'ok', used: 10 })], workspace: [] }, NOW), null);
  assert.equal(quotaMessage(null, NOW), null);
  const stop = quotaMessage({ limits: [lim()], workspace: [] }, NOW);
  assert.equal(stop.level, 'stop');
  assert.equal(stop.text, 'You have used all of today’s LLM tokens (240 of 200 tokens). Calls to the workspace’s models are refused until it resets; a workspace admin can raise the limit. It resets in 12 h (midnight, UTC).');
  const warn = quotaMessage({ limits: [lim({ state: 'warn', name: 'costPerMonth', value: 50, used: 51, resets: '2026-12-01T00:00:00Z' })], workspace: [] }, NOW);
  assert.equal(warn.level, 'warn');
  assert.match(warn.text, /^You have passed this month’s LLM budget \(\$51\.00 of \$50\.00\)\. Calls still go through/);
  assert.match(warn.text, /the first of the month, UTC/);
  const ws = quotaMessage({ limits: [lim({ state: 'warn' })], workspace: [lim({ source: 'workspace' })] }, NOW);
  assert.equal(ws.level, 'stop', 'a stop beats a warn');
  assert.match(ws.text, /^The workspace has used all of today’s LLM tokens/);
  for (const m of [stop, warn, ws]) assert.ok(!/tokensPer|costPer|quota\b|429|xbin_/.test(m.text), `plain words: ${m.text}`);
});

test('usage: rows summed per person and per component, today and this month, sorted', () => {
  const rows = [
    { day: '2026-11-14', user: 'bob', component: 'apps/agent', model: 'main/gpt-5', requests: 2, inputTokens: 200, outputTokens: 40, cost: 0.5 },
    { day: '2026-11-13', user: 'bob', component: '', model: 'main/gpt-5', requests: 1, failed: 1, inputTokens: 100, outputTokens: 20, cost: 0.25 },
    { day: '2026-11-14', user: '', component: 'apps/agent', model: 'main/mini', requests: 3, inputTokens: 30, outputTokens: 6, cost: 0, unpriced: 3 },
    { day: '2026-11-14', user: 'owner', component: '', model: 'main/gpt-5', requests: 1, inputTokens: 1000, outputTokens: 0, cost: 2 },
  ];
  assert.equal(today(NOW), '2026-11-14');
  const people = aggregate(rows, 'user', today(NOW));
  const bob = people.find((g) => g.key === 'bob');
  assert.deepEqual(bob.today, { requests: 2, failed: 0, estimated: 0, tokens: 240, inputTokens: 200, outputTokens: 40, cost: 0.5, unpriced: 0 });
  assert.deepEqual(bob.month, { requests: 3, failed: 1, estimated: 0, tokens: 360, inputTokens: 300, outputTokens: 60, cost: 0.75, unpriced: 0 });
  assert.deepEqual(people.map((g) => g.label).sort(), ['bob', 'no person (a tile’s own calls)', 'owner token']);
  assert.equal(personLabel('ana'), 'ana');
  const comps = aggregate(rows, 'component', today(NOW));
  assert.deepEqual(comps.map((g) => [g.label, g.month.tokens]).sort(), [['apps/agent', 276], ['direct calls', 1120]]);
  assert.equal(componentLabel(''), 'direct calls');
  assert.deepEqual(aggregate(rows, 'model', today(NOW)).map((g) => g.key).sort(), ['main/gpt-5', 'main/mini']);
  const t = totalsOf(people);
  assert.equal(t.month.tokens, 1396);
  assert.equal(t.today.tokens, 1276);
  assert.equal(t.month.requests, 7);
  assert.deepEqual(sortGroups(people).map((g) => g.key), ['owner', 'bob', ''], 'tokens this month, most first');
  assert.deepEqual(sortGroups(people, 'label').map((g) => g.key), ['bob', '', 'owner'], 'names, a to z');
  assert.deepEqual(sortGroups(people, 'todayCost', 'asc').map((g) => g.key), ['', 'bob', 'owner']);
  assert.deepEqual(sortGroups(people, 'requests').map((g) => g.key), ['bob', '', 'owner']);
  assert.deepEqual(USAGE_COLS.map((c) => c[0]), ['label', 'todayTokens', 'todayCost', 'monthTokens', 'monthCost', 'requests']);
  assert.equal(costWords(people.find((g) => g.key === '').month), '$0 + 3 unpriced');
  assert.equal(costWords(bob.month), '$0.75');
  assert.equal(tokensTitle(bob.month), '300 in · 60 out · 3 requests (1 failed)');
  assert.deepEqual(aggregate(null), []);
});

test('usage: estimated requests are marked, and unknown models are one row per provider', () => {
  const rows = [
    { day: '2026-11-14', user: 'bob', component: '', model: 'main/gpt-5', requests: 3, estimated: 2, inputTokens: 900, outputTokens: 100, cost: 0 },
    { day: '2026-11-14', user: 'bob', component: '', model: 'main/(unknown)', requests: 4, failed: 4, inputTokens: 0, outputTokens: 0, cost: 0 },
  ];
  const [bob] = aggregate(rows, 'user', '2026-11-14');
  assert.equal(bob.month.estimated, 2);
  assert.equal(tokensWords(bob.month), '~1k', 'a sum with an estimate in it says so');
  assert.equal(tokensWords({ tokens: 1000 }), '1k');
  assert.equal(requestMarks(bob.month), '4 failed · 2 estimated');
  assert.equal(requestMarks({ requests: 3 }), '');
  assert.equal(tokensTitle(bob.month), '900 in · 100 out · 7 requests (4 failed · 2 estimated: their provider reported no usage)');
  const models = aggregate(rows, 'model', '2026-11-14');
  assert.deepEqual(models.map((g) => [g.key, g.label]), [['main/gpt-5', 'main/gpt-5'], ['main/(unknown)', 'main: unknown models']]);
  assert.equal(modelLabel('a/b/(unknown)'), 'a/b: unknown models');
  assert.deepEqual(testWords({ ok: false, error: 'this workspace’s LLM connector is turned off' }), { ok: false, text: 'this workspace’s LLM connector is turned off' });
});

test('the connector\'s alerts', () => {
  const alerts = [{ kind: 'oom' }, { kind: 'llm-quota', message: 'q' }, { kind: 'llm-setup', dismiss: '/llm/setup/dismiss' }, { kind: 'llm', message: 'unpriced' }];
  assert.equal(setupAlert(alerts).dismiss, '/llm/setup/dismiss');
  assert.equal(setupAlert([alerts[0]]), null);
  assert.deepEqual(alerts.map(isLLMAlert), [false, true, true, true]);
});

// ---- the client ----

function fake(answers) {
  const calls = [];
  const fetcher = async (url, init) => {
    calls.push([url, init]);
    const a = answers.shift() ?? { status: 200, body: {} };
    if (a.throw) throw new Error(a.throw);
    return { ok: a.status >= 200 && a.status < 300, status: a.status, text: async () => (typeof a.body === 'string' ? a.body : JSON.stringify(a.body)) };
  };
  return { calls, fetcher };
}
const JSONH = { Accept: 'application/json', 'Content-Type': 'application/json' };

test('llmClient: each call\'s method, path and body', async () => {
  const { calls, fetcher } = fake([]);
  const c = llmClient(fetcher);
  await c.status(); await c.config(); await c.putConfig({ people: 'everyone' });
  await c.putUpstream('open ai', { baseURL: 'https://x/v1' }); await c.deleteUpstream('main'); await c.testUpstream('main', 'gpt-5'); await c.testUpstream('main');
  await c.usage({ period: 'day', user: 'bob', component: '' }); await c.usage(); await c.quotas('bob'); await c.quotas();
  await c.putQuotas({ default: { tokensPerDay: 5 } }); await c.putQuota('user:bob', null); await c.models(); await c.dismissSetup();
  assert.equal(await c.setupPending(), false, 'no llm-setup alert in the answer');
  const get = { method: 'GET', headers: { Accept: 'application/json' } };
  assert.deepEqual(calls, [
    ['/api/xbin/llm', get],
    ['/api/xbin/llm/config', get],
    ['/api/xbin/llm/config', { method: 'PUT', headers: JSONH, body: '{"people":"everyone"}' }],
    ['/api/xbin/llm/upstreams/open%20ai', { method: 'PUT', headers: JSONH, body: '{"baseURL":"https://x/v1"}' }],
    ['/api/xbin/llm/upstreams/main', { method: 'DELETE', headers: { Accept: 'application/json' } }],
    ['/api/xbin/llm/upstreams/main/test', { method: 'POST', headers: JSONH, body: '{"model":"gpt-5"}' }],
    ['/api/xbin/llm/upstreams/main/test', { method: 'POST', headers: JSONH, body: '{}' }],
    ['/api/xbin/llm/usage?period=day&user=bob', get],
    ['/api/xbin/llm/usage', get],
    ['/api/xbin/llm/quotas?user=bob', get],
    ['/api/xbin/llm/quotas', get],
    ['/api/xbin/llm/quotas', { method: 'PUT', headers: JSONH, body: '{"default":{"tokensPerDay":5}}' }],
    ['/api/xbin/llm/quotas', { method: 'PUT', headers: JSONH, body: '{"users":{"bob":null}}' }],
    ['/api/xbin/llm/v1/models', get],
    ['/api/xbin/llm/setup/dismiss', { method: 'POST', headers: { Accept: 'application/json' } }],
    ['/api/xbin/alerts', get],
  ]);
  const up = llmClient(fake([{ status: 200, body: { alerts: [{ kind: 'oom' }, { kind: 'llm-setup' }] } }, { status: 403, body: {} }, { throw: 'offline' }]).fetcher);
  assert.deepEqual([await up.setupPending(), await up.setupPending(), await up.setupPending()], [true, false, false]);
});

test('llmClient: answers and refusals never throw', async () => {
  const { fetcher } = fake([
    { status: 200, body: { configured: true } },
    { status: 409, body: { error: 'defaults.model: set by your host — it can’t be changed here', locked: ['defaults.model'], reason: 'set by your host' } },
    { status: 429, body: { error: { message: 'LLM quota reached', code: 'xbin_llm_quota_exceeded' } } },
    { status: 404, body: '404 page not found' },
    { status: 403, body: '' },
    { throw: 'offline' },
  ]);
  const c = llmClient(fetcher);
  assert.deepEqual(await c.config(), { ok: true, status: 200, data: { configured: true }, error: '', locked: [] });
  const locked = await c.putConfig({ defaults: { model: 'x/y' } });
  assert.equal(locked.ok, false);
  assert.deepEqual(locked.locked, ['defaults.model']);
  assert.match(locked.error, /set by your host/);
  assert.equal((await c.models()).error, 'LLM quota reached', 'an OpenAI-shaped error\'s message');
  assert.equal((await c.status()).error, 'this xbind has no LLM connector (it predates it)');
  assert.equal((await c.config()).error, 'the LLM connector’s settings are a workspace admin’s');
  const off = await c.config();
  assert.equal(off.status, 0);
  assert.match(off.error, /can’t be reached \(offline\)/);
  assert.equal(errorText(500, null), 'error 500');
});

test('one subject\'s quota: its body, its place in the rules, its lock', () => {
  const q = { tokensPerDay: 5 };
  assert.deepEqual(quotaRules('workspace', q), { workspace: q });
  assert.deepEqual(quotaRules('default', null), { default: null });
  assert.deepEqual(quotaRules('user:bob', q), { users: { bob: q } });
  assert.deepEqual(quotaRules('org:eng', null), { orgs: { eng: null } });
  assert.deepEqual(quotaRules('nonsense', q), {});
  const rules = { default: q, users: { bob: { costPerDay: 1 } }, orgs: {} };
  assert.deepEqual(quotaOf(rules, 'default'), q);
  assert.deepEqual(quotaOf(rules, 'user:bob'), { costPerDay: 1 });
  assert.equal(quotaOf(rules, 'org:eng'), null);
  assert.equal(quotaOf(rules, 'workspace'), null);
  assert.deepEqual(['workspace', 'default', 'user:bob', 'org:eng'].map(quotaLockPath), ['quotas.workspace', 'quotas.default', 'quotas.users', 'quotas.orgs']);
});
