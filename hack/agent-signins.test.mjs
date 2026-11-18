// hack/agent-signins.test.mjs — web/agent-signins.js: the saved sign-ins'
// calls and words (D230). node --test hack/agent-signins.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import { signinsApi, SigninsError, harnessOf, whyNot, kindWords, dayWords, expiryWords, signinChip, savedHint, startSignInWords, nextStep, trustWords, namesWords, offerWords, homeWords, homeCopyWords, fmtBytes, HOME_DOCS } from '../web/agent-signins.js';

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

test('"I trust this tile\'s source": the card\'s words — whose tile, who can change it (by account id), the risk, two choices, what happens without', () => {
  assert.equal(namesWords([]), '');
  assert.equal(namesWords(['bob']), 'bob');
  assert.equal(namesWords(['bob', 'carol']), 'bob and carol');
  assert.equal(namesWords(['bob', 'carol', 'dave']), 'bob, carol and dave');
  assert.equal(namesWords(['bob', 'carol', 'dave'], 2), 'bob, carol, dave and 2 more');
  const w = trustWords({ tile: 'apps/crm', ownerKind: 'person', owner: 'bob', editors: ['carol', 'dave'], allowed: true }, 'Claude Code', 'own');
  assert.equal(w.title, 'Use your Claude Code sign-in on this tile?');
  assert.equal(w.whose, 'This tile belongs to bob. carol and dave can also change it.');
  assert.equal(w.risk, "Trusting means your Claude Code sign-in is used here and this tile's code can change your home. People who can change this tile could read it and use your Claude Code account.");
  assert.equal(w.trust, 'Trust this tile — use my sign-in');
  assert.equal(w.without, 'Start without my sign-in');
  assert.equal(w.then, "Without it, the agent uses this tile's own login.");
  assert.equal(w.forbidden, '');
  assert.equal(w.more, 0);
  assert.ok(w.risk.split(/(?<=\.) /).length === 2 && w.risk.length < 180, 'the risk is two short sentences');
  // account ids are what xbind enforces: a display name — its bearer's to set, to "alice" too — only rides beside the id
  const spoof = trustWords({ ownerKind: 'person', owner: 'mallory', ownerName: 'alice', editors: ['eve', 'bob'], names: { eve: 'alice (you)', bob: 'bob' }, allowed: true });
  assert.equal(spoof.whose, 'This tile belongs to mallory (alice). eve (alice (you)) and bob can also change it.');
  assert.equal(trustWords({ ownerKind: 'you', owner: 'alice', editors: ['carol'], allowed: true }).whose, 'This tile is yours. carol can change it.');
  assert.equal(trustWords({ ownerKind: 'org', owner: 'sales', ownerName: 'Sales', allowed: true }).whose, 'This tile belongs to the org sales (Sales).');
  assert.equal(trustWords({ ownerKind: 'workspace', editors: ['carol'], allowed: true }).whose, 'This tile belongs to the workspace. carol can also change it.');
  assert.equal(trustWords({ ownerKind: 'workspace', admin: true, editors: ['carol'], allowed: true }).whose, 'This tile belongs to the workspace, which you administer. carol can change it.');
  // more than three: three and a count — and "show all" before it can be trusted (the person sees everyone they trust)
  const many = { ownerKind: 'workspace', editors: ['a', 'b', 'c', 'd', 'e', 'f', 'g'], allowed: true };
  const first = trustWords(many);
  assert.equal(first.whose, 'This tile belongs to the workspace. a, b, c and 4 more can also change it.');
  assert.equal(first.more, 4);
  assert.equal(first.showAll, 'Show all 7');
  const every = trustWords(many, 'Claude Code', '', true);
  assert.equal(every.whose, 'This tile belongs to the workspace. a, b, c, d, e, f and g can also change it.');
  assert.equal(every.more, 0);
  assert.equal(trustWords({ ownerKind: 'you', allowed: true }, 'Codex', 'connector').then, "Without it, the agent runs on the workspace's model.");
  assert.equal(trustWords({ ownerKind: 'you', allowed: true }, 'Codex', 'none').then, 'Without it, the agent starts signed out — you can sign in on this tile.');
  assert.equal(trustWords({ ownerKind: 'person', owner: 'bob', allowed: false, forbidden: "Trusting a tile's source is switched off for this workspace — a workspace admin can change it" }).forbidden,
    "Trusting a tile's source is switched off for this workspace — a workspace admin can change it.");
  // a start's "use my sign-in" line, on the approval: what the session runs on as it stands, the line, why it is warned
  const o = offerWords({ offer: true, name: 'Claude Code', without: 'connector' });
  assert.equal(o.runs, "Runs on the workspace's model.");
  assert.equal(o.line, 'Use my Claude Code sign-in for this session');
  assert.equal(o.warn, 'Your workspace agent directs this session: what it tells the coding agent could expose your sign-in.');
  assert.equal(offerWords({ name: 'Codex', without: 'own' }).runs, "Runs on this tile's own Codex login.");
  assert.equal(offerWords({ name: 'Codex', without: 'needed' }).runs, "Starts signed out: Codex isn't served by the workspace's model here.");
  // a launcher's hint and a session's chip say it waits for trust — not that something is wrong
  const t = { tile: 'apps/crm', ownerKind: 'person', owner: 'bob', allowed: true };
  assert.match(savedHint({ saved: { name: 'Personal', inject: false, why: 'x', trust: t } }), /xbin asks first whether you trust this tile's source/);
  assert.deepEqual(signinChip({ source: 'home', name: 'Personal', why: "apps/crm isn't only yours", trust: t }).text, 'saved sign-in waits for your trust');
  assert.equal(signinChip({ source: 'saved', name: 'Personal' }).text, 'your saved sign-in: Personal');
  assert.equal(signinChip({ source: 'saved' }).text, 'your saved sign-in', 'a session the workspace agent started names none');
  // the calls: an answer carries the question's basis; a refusal's body rides the error (409 {changed, trust})
  const sent = [];
  const api = signinsApi(async (url, init) => { sent.push([init.method, url, init.body || '']); return { ok: true, json: async () => ({}) }; });
  const refusing = signinsApi(async () => ({ ok: false, status: 409, json: async () => ({ error: 'look again', changed: true, trust: { basis: 'tb_new' } }) }));
  return Promise.all([api.trust('apps/crm', 'tb_1'), api.untrust('tr_1/x'), api.setTrustPolicy(false), refusing.trust('apps/crm', 'tb_old').catch((e) => e)]).then(([, , , err]) => {
    assert.deepEqual(sent, [['POST', '/api/xbin/agent/signins/trust', '{"tile":"apps/crm","basis":"tb_1"}'], ['DELETE', '/api/xbin/agent/signins/trust/tr_1%2Fx', ''], ['PUT', '/api/xbin/agent/signins/policy', '{"trust":false}']]);
    assert.ok(err instanceof SigninsError && err.status === 409 && err.data.changed === true && err.data.trust.basis === 'tb_new');
  });
});

// A button armed for one question is not armed for the next: lit reuses the element when a card redraws, so the wait starts again whenever what
// the button stands for (its data-armed key: the question's basis, who is listed) changes — review finding 2
test('ArmedButtons: armed a moment after it appears — and again whenever what it stands for changes', async () => {
  globalThis.document ??= { hasFocus: () => true, visibilityState: 'visible' };
  const { ArmedButtons, ARM_MS } = await import('../web/agent-armed.js');
  const attrs = { 'data-armed': 'tb_1|' };
  const el = { isConnected: true, getAttribute: (k) => (k in attrs ? attrs[k] : null), setAttribute: (k, v) => { attrs[k] = v; } };
  const host = { renderRoot: { querySelectorAll: () => [el] }, addController() {} };
  const armed = new ArmedButtons(host);
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  armed.hostUpdated();
  assert.equal(attrs['aria-disabled'], 'true');
  assert.equal(armed.armed(el), false, 'not as it appears');
  await wait(ARM_MS + 60);
  assert.equal(armed.armed(el), true);
  assert.equal(attrs['aria-disabled'], 'false');
  armed.hostUpdated(); // a redraw that changes nothing: still armed
  assert.equal(armed.armed(el), true);
  attrs['data-armed'] = 'tb_2|'; // the question changed under the person (the same element, reused)
  assert.equal(armed.armed(el), false, 'the key moved: not armed even before the host redraws');
  armed.hostUpdated();
  assert.equal(attrs['aria-disabled'], 'true', 'it waits again');
  await wait(ARM_MS / 2);
  assert.equal(armed.armed(el), false);
  attrs['data-armed'] = 'tb_2|all'; // …and again when more names are shown
  armed.hostUpdated();
  await wait(ARM_MS / 2 + 60);
  assert.equal(armed.armed(el), false, 'the earlier timer does not arm it for the newer content');
  assert.equal(attrs['aria-disabled'], 'true');
  await wait(ARM_MS / 2 + 60);
  assert.equal(armed.armed(el), true);
  assert.equal(attrs['aria-disabled'], 'false');
});

test('which home a session mounts (D245): the words for "yours" and for this tile\'s copy, what trusting means, forget — and nothing that copies a copy over', () => {
  const own = homeWords({ mode: 'own', docs: HOME_DOCS });
  assert.equal(own.text, 'home: yours');
  assert.equal(own.long, 'home: yours');
  assert.equal(own.tone, '');
  assert.equal(own.docs, '/docs/agent-signins.md#your-home-on-a-tile-that-isnt-only-yours');
  assert.equal(own.trust, '');
  // a workspace without sandboxes protects nothing, and says so
  const bare = homeWords({ mode: 'own', unsandboxed: true });
  assert.equal(bare.tone, 'warn');
  assert.match(bare.title, /without sandboxes/);
  assert.match(bare.title, /nothing protects/);

  const trust = { tile: 'apps/crm', basis: 'tb_1', ownerKind: 'person', owner: 'bob', editors: ['carol', 'dave', 'erin', 'frank'], allowed: true };
  const copy = homeWords({ mode: 'copy', why: "apps/crm isn't only yours — it belongs to bob — and you haven't said you trust its source", trust });
  assert.equal(copy.text, "home: this tile's copy");
  assert.equal(copy.long, "home: this tile's copy (changes stay on this tile)");
  assert.equal(copy.tone, 'warn');
  assert.equal(copy.trust, "I trust this tile's source");
  assert.equal(copy.forget, 'Forget what this tile wrote to my home');
  const all = copy.about.join(' ');
  assert.match(all, /reads everything in your real home/);
  assert.match(all, /Your real home is not changed/);
  assert.match(all, /it belongs to bob/);
  // everyone who can change the tile is named before it can be trusted — never "and 2 more"
  assert.match(all, /carol, dave, erin and frank can also change it/);
  // what trusting means: two things — and nothing is merged
  assert.match(all, /means two things: xbin uses your saved coding-agent sign-in here, and this tile's code can change your home/);
  assert.match(all, /Nothing this tile wrote to its copy is copied into your home/);
  for (const w of [copy.trust, copy.forget, ...copy.about]) assert.doesNotMatch(w, /copy (it|them) over|merge|bring .* along/i);
  // trusting switched off: no button, and who can change that
  const off = homeWords({ mode: 'copy', why: 'x', trust: { ...trust, allowed: false, forbidden: "Trusting a tile's source is switched off for this workspace — a workspace admin can change it" } });
  assert.equal(off.trust, '');
  assert.match(off.about.join(' '), /a workspace admin can change it/);
  // nobody to ask (the root token, a session the workspace agent started): no button either
  assert.equal(homeWords({ mode: 'copy', why: 'x' }).trust, '');
  // a session that started on the copy and stays on it
  assert.match(homeWords({ mode: 'copy', kept: true, trust }).about.join(' '), /started on this tile's copy of your home and stays on it/);

  assert.equal(fmtBytes(-1), '');
  assert.equal(fmtBytes(512), '512 B');
  assert.equal(fmtBytes(123456), '121 KB');
  assert.equal(fmtBytes(5 * 1024 * 1024), '5 MB');
  const kept = homeCopyWords({ id: 'hc_1', tile: 'apps/crm', bytes: 123456, sessions: 0, unused: true });
  assert.equal(kept.state, 'kept, not used while you trust this tile');
  assert.equal(kept.forget, 'Forget what this tile wrote to my home');
  assert.match(kept.confirm, /Your real home is not touched/);
  assert.equal(homeCopyWords({ tile: 'apps/crm', bytes: 1, sessions: 2 }).state, 'in use by 2 sessions');
  assert.match(homeCopyWords({ tile: 'apps/crm', bytes: 1, sessions: 2 }).confirm, /Your 2 sessions on it will be ended/);
  assert.match(homeCopyWords({ tile: 'apps/gone', gone: true }).state, /its tile is gone/);
});

test('forgetting a copy is one call: DELETE /agent/signins/homes/<id>', async () => {
  const calls = [];
  const api = signinsApi(async (url, init) => { calls.push([init.method, url]); return new Response(JSON.stringify({ ok: true, tile: 'apps/crm', ended: 1 }), { status: 200, headers: { 'content-type': 'application/json' } }); });
  assert.deepEqual(await api.forgetHome('hc_a/b'), { ok: true, tile: 'apps/crm', ended: 1 });
  assert.deepEqual(calls, [['DELETE', '/api/xbin/agent/signins/homes/hc_a%2Fb']]);
});
