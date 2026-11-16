// hack/agent-signins.test.mjs — web/agent-signins.js: the saved sign-ins'
// calls and words (D230). node --test hack/agent-signins.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import { signinsApi, SigninsError, harnessOf, whyNot, kindWords, dayWords, expiryWords, signinChip, savedHint, startSignInWords, nextStep } from '../web/agent-signins.js';

const DAY = 86400000;

// a recording fetch: answers by [status, body] per call, keeps what was asked
function fakeFetch(answers) {
  const calls = [];
  const fn = async (url, init = {}) => {
    calls.push({ url, method: init.method, body: init.body ? JSON.parse(init.body) : undefined });
    const [status, body] = answers.shift() || [200, {}];
    if (status === 'throw') throw new Error('offline');
    return { ok: status >= 200 && status < 300, status, json: async () => { if (body === null) throw new Error('no body'); return body; } };
  };
  return { fn, calls };
}

test('the calls: paths, methods and bodies — a secret rides one body and nothing else', async () => {
  const f = fakeFetch([]);
  const api = signinsApi(f.fn);
  await api.list();
  await api.list('apps/my tile');
  await api.paste('claude', 'sk-FAKE-not-a-real-key', 'Work', true);
  await api.paste('codex', 'sk-FAKE-not-a-real-key');
  await api.rename('si_1', 'Home');
  await api.makeDefault('si_1');
  await api.remove('si_1');
  await api.guided('claude', '', false);
  await api.code('sf_1', 'abc#def');
  await api.cancel('sf_1');
  await api.uses();
  await api.people();
  await api.revoke('bob smith', 'si_2');
  await api.setPolicy(false);
  await api.dismissNotice();
  const B = '/api/xbin/agent/signins';
  assert.deepEqual(f.calls, [
    { url: B, method: 'GET', body: undefined },
    { url: `${B}?tile=apps%2Fmy%20tile`, method: 'GET', body: undefined },
    { url: B, method: 'POST', body: { harness: 'claude', secret: 'sk-FAKE-not-a-real-key', name: 'Work', default: true } },
    { url: B, method: 'POST', body: { harness: 'codex', secret: 'sk-FAKE-not-a-real-key' } },
    { url: `${B}/si_1`, method: 'PUT', body: { name: 'Home' } },
    { url: `${B}/si_1`, method: 'PUT', body: { default: true } },
    { url: `${B}/si_1`, method: 'DELETE', body: undefined },
    { url: `${B}/guided`, method: 'POST', body: { harness: 'claude' } },
    { url: `${B}/guided/sf_1`, method: 'POST', body: { code: 'abc#def' } },
    { url: `${B}/guided/sf_1`, method: 'DELETE', body: undefined },
    { url: `${B}/uses`, method: 'GET', body: undefined },
    { url: `${B}/people`, method: 'GET', body: undefined },
    { url: `${B}/people/bob%20smith/si_2`, method: 'DELETE', body: undefined },
    { url: `${B}/policy`, method: 'PUT', body: { enabled: false } },
    { url: `${B}/notice`, method: 'DELETE', body: undefined },
  ]);
});

test('a refusal carries the status and the server\'s own words; an unreachable xbind says so', async () => {
  const api = signinsApi(fakeFetch([[409, { error: 'Claude Code says that isn\'t the whole code' }], [502, null], ['throw']]).fn);
  await assert.rejects(api.code('sf_1', 'x'), (e) => e instanceof SigninsError && e.status === 409 && /whole code/.test(e.message));
  await assert.rejects(api.guided('claude'), (e) => e.status === 502 && /502/.test(e.message));
  await assert.rejects(api.list(), (e) => e.status === 0 && /could not be reached/.test(e.message));
});

test('whyNot says what stands in the way, and what to do', () => {
  const ok = { available: true, vault: 'open', policy: { enabled: true } };
  assert.equal(whyNot(ok), '');
  assert.equal(whyNot(null), '');
  assert.match(whyNot({ available: false, why: 'saved sign-ins need sandboxes (xbind --isolate)' }), /need sandboxes/);
  assert.match(whyNot({ ...ok, policy: { enabled: false } }), /workspace admin can switch it on/);
  assert.match(whyNot({ ...ok, policy: { enabled: false, locked: true } }), /by its host/);
  assert.match(whyNot({ ...ok, vault: 'sealed' }), /once an admin unseals/);
  assert.match(whyNot({ ...ok, vault: 'locked' }), /sets the vault up first/);
  assert.equal(whyNot({ ...ok, vault: 'plaintext' }), '');
  assert.equal(harnessOf({ harnesses: [{ id: 'claude' }, { id: 'codex' }] }, 'codex').id, 'codex');
  assert.equal(harnessOf(null, 'codex'), null);
});

test('times and kinds, in words', () => {
  const now = new Date(2026, 10, 16, 15, 0, 0).getTime();
  assert.equal(dayWords(0, now), 'never');
  assert.equal(dayWords(now - 3600000, now), 'today');
  assert.equal(dayWords(now - 20 * 3600000, now), 'yesterday');
  assert.equal(dayWords(now - 12 * DAY, now), '12 days ago');
  assert.match(dayWords(now - 200 * DAY, now), /^2026-\d\d-\d\d$/);
  assert.equal(kindWords('setup-token'), 'subscription token');
  assert.equal(kindWords('api-key'), 'API key');
  assert.equal(expiryWords({}, now), '');
  assert.equal(expiryWords({ expiresAt: now + 200 * DAY }, now), '');
  assert.equal(expiryWords({ expiresAt: now + 12 * DAY }, now), 'expires in 12 days');
  assert.equal(expiryWords({ expiresAt: now + DAY / 2 }, now), 'expires tomorrow');
  assert.match(expiryWords({ expiresAt: now - 1 }, now), /^expired/);
});

test('the header says what a session signs in with — and why a saved sign-in was left out', () => {
  assert.equal(signinChip(null), null);
  assert.equal(signinChip({ source: '' }), null);
  assert.deepEqual(signinChip({ source: 'saved', name: 'Personal' }, 'Claude Code'),
    { text: 'your saved sign-in: Personal', title: 'Claude Code runs on the sign-in you saved with xbind (Personal).', tone: '' });
  const home = signinChip({ source: 'home' }, 'Codex');
  assert.equal(home.text, "this tile's own login");
  assert.equal(home.tone, '');
  const left = signinChip({ source: 'home', name: 'Personal', why: 'bob can also open terminals on apps/crm' }, 'Claude Code');
  assert.equal(left.tone, 'warn');
  assert.match(left.title, /Personal isn't used here: bob can also open terminals on apps\/crm\./);
  assert.equal(signinChip({ source: 'connector' }), null); // the connector's own chip says it
  assert.equal(signinChip({ source: 'connector', name: 'P', why: 'you chose the connector' }).text, 'saved sign-in not used');
  assert.equal(signinChip({ source: 'none' }), null);
  assert.equal(signinChip({ source: 'none', name: 'P', why: 'the workspace vault is sealed' }).tone, 'warn');
});

test('the launcher and a brief say it before anything starts', () => {
  assert.equal(savedHint({}), '');
  assert.equal(savedHint({ saved: { name: 'Personal', inject: true } }), 'your saved sign-in: Personal');
  assert.equal(savedHint({ saved: { name: 'Personal', inject: false, why: 'bob can also open terminals on apps/crm' } }),
    'saved sign-in Personal not used here — bob can also open terminals on apps/crm');
  assert.deepEqual(startSignInWords({ name: 'Claude Code', signIn: 'saved' }), { text: 'your saved Claude Code sign-in, once you approve the start', needs: false });
  assert.deepEqual(startSignInWords({ name: 'Codex', signIn: 'connector' }), { text: "the workspace's model — no sign-in needed", needs: false });
  assert.deepEqual(startSignInWords({ name: 'Claude Code', signIn: 'needed' }), { text: 'needs your Claude Code sign-in', needs: true });
  assert.equal(startSignInWords({ name: 'Claude Code', signIn: 'own' }).needs, false);
  assert.deepEqual(startSignInWords({ name: 'Claude Code' }), { text: '', needs: false });
});

test('an error says what to do next', () => {
  assert.match(nextStep({ status: 503 }), /unseal the vault/);
  assert.match(nextStep({ status: 502 }), /paste a key instead/);
  assert.match(nextStep({ status: 429 }), /Wait a minute/);
  assert.equal(nextStep({ status: 409 }), '');
  assert.equal(nextStep(null), '');
});
