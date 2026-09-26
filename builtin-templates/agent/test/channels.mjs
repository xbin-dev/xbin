// channels.mjs — chat channels on the Automations page (D86): an announced
// channel to claim (with its rules), the pairing queue (approve by code), the
// people it knows, its sessions, the replies it could not deliver, and the
// rules form; the page refreshes on the stream's `automation` events.
//
//   node test/channels.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Date.now();
const seed = {
  runs: [{ id: 1, title: 'a chat', status: 'idle', parentId: 0, rootId: 1, activityMs: now }],
  automations: [
    { kind: 'channel', id: 7, name: 'Slack · Acme', access: 'claim', attention: 1, enabled: false, visibility: 'private',
      summary: 'Slack · Acme via apps/slack', config: { adapter: 'apps/slack', platform: 'slack', state: 'unclaimed', lastSeen: now / 1000 - 30 } },
    { kind: 'channel', id: 8, name: 'Support bot', owner: 'admin', access: 'owner', attention: 2, enabled: true, visibility: 'private', runs: 3,
      summary: 'Slack · Beta via apps/slack2', config: { adapter: 'apps/slack2', platform: 'slack', state: 'active', botName: 'helper',
        policy: { dm: { policy: 'pairing' }, groups: { policy: 'allowlist', allow: ['C1'] } }, pendingPeers: 1, failedDeliveries: 1 } },
  ],
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript((t) => {
  const peers = [
    { peerId: 'U9', name: 'Uma', state: 'pending', trusted: false, codeExpires: t / 1000 + 1800 },
    { peerId: 'U2', name: 'Bo', state: 'allowed', trusted: false },
    { peerId: 'U3', name: 'Cy', state: 'allowed', trusted: false, xbinUser: 'cy', linkedAt: t / 1000 - 600 },
  ];
  window.__route('GET', /\/channels\/8\/peers$/, () => window.__json({ peers }));
  window.__route('POST', /\/channels\/8\/pair$/, (m, o) => (JSON.parse(o.body).code === 'ABCD2345'
    ? window.__json({ ok: 'true', peerId: 'U9', name: 'Uma' }) : window.__json({ error: 'no pending pairing with that code' }, 404)));
  window.__route('PUT', /\/channels\/8\/peers\/(\w+)$/, (m) => window.__json({ peerId: m[1] }));
  window.__route('GET', /\/channels\/8\/sessions$/, () => window.__json({ sessions: [
    { key: 'chan:8:dm:U2', runId: 31, resets: 0, lastIn: t / 1000 - 90 }] }));
  window.__route('GET', /\/channels\/8\/outbox/, () => window.__json({ items: [
    { id: 55, kind: 'answer', body: { text: 'the deploy is done' }, error: 'channel_not_found', state: 'failed' }] }));
  window.__route('POST', /\/channels\/8\/outbox\/55\/retry$/, () => window.__json({ ok: 'true' }));
  window.__route('PUT', /\/channels\/8$/, () => window.__json({ id: 8 }));
  window.__route('POST', /\/channels\/7\/claim$/, () => window.__json({ id: 7 }));
}, now);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
const called = (method, tail) => page.evaluate(([m, t]) => window.__calls.filter((c) => c.method === m && c.url.split('?')[0].endsWith(t)).map((c) => c.body), [method, tail]);
await page.goto(`${ORIGIN}/`);
await page.waitForFunction(() => document.querySelector('#autos .badge.unread')?.textContent === '3');
ok('what waits on you counts in the sidebar entry', true);

await page.click('#autos .autos-entry');
await page.waitForSelector('.acard2[data-auto="channel:7"]');
const groups = await page.$$eval('.autos-page h5', (els) => els.map((e) => e.textContent));
ok('channels come first', groups[0] === 'Channels', groups.join(' | '));
ok('an announced channel asks to be claimed', (await page.textContent('.acard2[data-auto="channel:7"]')).includes('claim it'));
const c8 = await page.textContent('.acard2[data-auto="channel:8"]');
ok('a channel says who waits and what failed', c8.includes('1 waiting to pair') && c8.includes('1 not delivered'), c8);

// claim, with rules
await page.click('.acard2[data-auto="channel:7"]');
await page.waitForSelector('.autos-page button:has-text("Claim")');
ok('the claim explains what claiming does', (await page.textContent('.autos-page')).includes('ignores every'));
await page.selectOption('.autos-page select >> nth=1', 'allowlist');
await page.click('.autos-page button:has-text("Claim")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/channels/7/claim')));
const cl = JSON.parse((await called('POST', '/channels/7/claim'))[0]);
ok('the claim carries its rules', cl.policy.dm.policy === 'allowlist' && cl.policy.groups.policy === 'allowlist' && cl.name === 'Slack · Acme',
  JSON.stringify(cl));

// the owned one
await page.click('.autos-page .crumb');
await page.click('.acard2[data-auto="channel:8"]');
await page.waitForSelector('.chrow[data-peer="U9"]');
ok('the pairing queue lists who waits', (await page.textContent('.chrow[data-peer="U9"]')).includes('Uma'));
await page.fill('.chadd input >> nth=0', 'WRONG');
await page.click('.chadd button:has-text("Approve")');
await page.waitForSelector('.autos-page .err');
ok('a wrong code says so', (await page.textContent('.autos-page .err')).includes('no pending pairing'));
await page.fill('.chadd input >> nth=0', 'ABCD2345');
await page.click('.chadd button:has-text("Approve")');
await page.waitForSelector('.autos-page .note');
ok('the right code pairs', (await page.textContent('.autos-page .note')).includes('Paired with Uma'));
ok('its sessions are listed', (await page.textContent('.autos-page')).includes('chan:8:dm:U2'));
ok('a linked person shows whose account they are', (await page.textContent('.chrow[data-peer="U3"]')).includes('@cy'));
await page.click('.chrow[data-peer="U3"] button:has-text("Unlink")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PUT' && c.url.endsWith('/peers/U3')));
ok('the owner can unlink them', JSON.parse((await called('PUT', '/peers/U3'))[0]).unlink === true);
await page.click('.chrow:has-text("the deploy is done") button:has-text("Retry")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/outbox/55/retry')));
ok('a failed reply can be retried', true);

// rules: open the private lane; people become trustable
ok('no trust without the private lane', !(await page.$('.chrow[data-peer="U2"] .chk')));
await page.check('.autos-page label:has-text("private lane") input');
await page.waitForSelector('.chrow[data-peer="U2"] .chk');
ok('with it, people can be trusted', true);
await page.click('.autos-page button:has-text("Save rules")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PUT' && c.url.endsWith('/channels/8')));
const put = JSON.parse((await called('PUT', '/channels/8'))[0]);
ok('saving sends the whole policy', put.policy.privateLane === true && put.policy.groups.allow[0] === 'C1' && put.policy.dm.policy === 'pairing',
  JSON.stringify(put));

// a channel change on the stream refreshes the page
const before = await page.evaluate(() => window.__calls.filter((c) => c.url.endsWith('/automations')).length);
await page.evaluate(() => window.__push({ type: 'automation', data: { kind: 'channel', id: 8 } }));
await page.waitForFunction((n) => window.__calls.filter((c) => c.url.endsWith('/automations')).length > n, before);
ok('an automation event reloads the page', true);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('channels');
