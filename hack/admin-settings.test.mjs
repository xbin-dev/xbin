// hack/admin-settings.test.mjs — unit tests for the admin console's
// workspace → settings tab (workspace-template/tiles/admin/tabs/settings-view.js;
// D180), run by `make js-test`: how it reads every setting from an xbind
// since D180, and from the older ones it may meet — v0.3.66 (the
// partitioned tiles' switches at /workspace-policies), v0.3.65 (base
// auto-update alone), one before both — where each save goes, and which
// events make it read again.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { SETTINGS, GROUPS, settingsOf, loadSettings, saveRequest, reloadOn, missingRoute } from '../workspace-template/tiles/admin/tabs/settings-view.js';

// xbind(routes): a call() answering each path from routes; a path it lacks
// is Go's mux 404 (no error body); calls are recorded.
function xbind(routes) {
  const calls = [];
  const call = async (path) => {
    calls.push(path);
    const r = routes[path];
    if (!r) return { status: 404, ok: false, body: '404 page not found\n' };
    return { status: r.status ?? 200, ok: (r.status ?? 200) < 400, body: r.body };
  };
  return { call, calls };
}

test('every setting has a group, words and a notice', () => {
  assert.deepEqual(GROUPS.map((g) => g.id), ['terminals', 'partitions']);
  assert.deepEqual(settingsOf('terminals').map((s) => s.key), ['baseAutoUpdate']);
  assert.deepEqual(settingsOf('partitions').map((s) => s.key), ['partitionConsent', 'credentialResetConfirm']);
  for (const s of SETTINGS) {
    assert.ok(s.label && s.on && s.off && s.unreadable, s.key);
    assert.notEqual(s.notice(true), s.notice(false), s.key);
  }
  assert.ok(SETTINGS.find((s) => s.key === 'partitionConsent').confirm, 'turning consent on asks first');
  assert.equal(SETTINGS.find((s) => s.key === 'credentialResetConfirm').confirm, undefined, 'credential resets save at once');
});

test('an xbind since D180: one GET, every setting, its errors', async () => {
  const x = xbind({ '/workspace-settings': { body: { baseAutoUpdate: false, partitionConsent: true, credentialResetConfirm: false,
    errors: { credentialResetConfirm: 'data/workspace-settings.json: credentialResetConfirm is "x", not true or false' } } } });
  const st = await loadSettings(x.call);
  assert.deepEqual(x.calls, ['/workspace-settings'], 'no second request');
  assert.deepEqual(st.values, { baseAutoUpdate: false, partitionConsent: true, credentialResetConfirm: false });
  assert.deepEqual(st.groups, { terminals: { state: 'ok' }, partitions: { state: 'ok' } });
  assert.equal(st.legacy, false);
  assert.match(st.errors.credentialResetConfirm, /not true or false/);
  assert.deepEqual(saveRequest(st, 'partitionConsent', false), { path: '/workspace-settings', body: { partitionConsent: false } });
  assert.deepEqual(saveRequest(st, 'baseAutoUpdate', true), { path: '/workspace-settings', body: { baseAutoUpdate: true } });
  assert.equal(reloadOn(st, { type: 'workspace-settings', data: { baseAutoUpdate: true, changed: ['partitionConsent'] } }), true);
  assert.equal(reloadOn(st, { type: 'policies' }), false, 'its `policies` echo is for older clients: not read twice');
  assert.equal(reloadOn(st, { type: 'grants' }), false);
});

test('v0.3.66: the partitioned tiles\' switches at /workspace-policies, saved there', async () => {
  const x = xbind({
    '/workspace-settings': { body: { baseAutoUpdate: true, error: 'data/workspace-settings.json isn\'t a JSON object (fix or remove it)' } },
    '/workspace-policies': { body: { schema: 1, partitionConsent: false, credentialResetConfirm: true } },
  });
  const st = await loadSettings(x.call);
  assert.deepEqual(x.calls, ['/workspace-settings', '/workspace-policies']);
  assert.deepEqual(st.values, { baseAutoUpdate: true, partitionConsent: false, credentialResetConfirm: true });
  assert.equal(st.legacy, true);
  assert.match(st.errors.baseAutoUpdate, /isn't a JSON object/, 'D175\'s error is base auto-update\'s');
  assert.deepEqual(saveRequest(st, 'credentialResetConfirm', false), { path: '/workspace-policies', body: { credentialResetConfirm: false } });
  assert.deepEqual(saveRequest(st, 'baseAutoUpdate', false), { path: '/workspace-settings', body: { baseAutoUpdate: false } });
  assert.equal(reloadOn(st, { type: 'policies' }), true, 'v0.3.66 says `policies` for its switches');
  assert.equal(reloadOn(st, { type: 'workspace-settings' }), true);
});

test('v0.3.66, a switch it can\'t read: that group says so, the other reads', async () => {
  const x = xbind({
    '/workspace-settings': { body: { baseAutoUpdate: true } },
    '/workspace-policies': { status: 500, body: { error: 'data/workspace-policies.json: not a JSON object — fix or remove the file by hand' } },
  });
  const st = await loadSettings(x.call);
  assert.deepEqual(st.groups.terminals, { state: 'ok' });
  assert.equal(st.groups.partitions.state, 'error');
  assert.match(st.groups.partitions.why, /fix or remove/);
});

test('v0.3.65: base auto-update alone; no partitioned tiles', async () => {
  const st = await loadSettings(xbind({ '/workspace-settings': { body: { baseAutoUpdate: true } } }).call);
  assert.deepEqual(st.values, { baseAutoUpdate: true });
  assert.equal(st.groups.partitions.state, 'absent');
  assert.match(st.groups.partitions.why, /no partitioned tiles/);
});

test('before D175: no settings at all, each group says so', async () => {
  const st = await loadSettings(xbind({}).call);
  assert.deepEqual(st.values, {});
  assert.equal(st.groups.terminals.state, 'absent');
  assert.equal(st.groups.partitions.state, 'absent');
});

test('a route\'s own 404 is not a missing route', () => {
  assert.equal(missingRoute({ status: 404, body: '404 page not found\n' }), true);
  assert.equal(missingRoute({ status: 405, body: 'Method Not Allowed\n' }), true);
  assert.equal(missingRoute({ status: 404, body: { error: 'no such tile' } }), false);
  assert.equal(missingRoute({ status: 403, body: { error: 'admin only' } }), false);
  assert.throws(() => saveRequest({}, 'nope', true));
});
