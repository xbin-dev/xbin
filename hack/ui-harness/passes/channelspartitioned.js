// hack/ui-harness/passes/channelspartitioned.js — chat channels and event
// triggers of a PARTITIONED agent (the agent template's API.md
// "Partitioned instances"): run with HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1
// (seed.sh then makes apps/agent partitioned; SKIP otherwise). The bridge's
// console plays the chat platform, as in the channels pass; the bridge and
// the webhooks tile reach the agent's global instance.
//   - admin's page is admin's own partition: its Automations list the global
//     instance's channel, and claiming it there is forwarded to global;
//   - dev1 opens the agent once (their partition has run), links the chat
//     account on the bridge's page; a DM from it is answered by dev1's
//     partition — the conversation is in dev1's list, not admin's;
//   - dev1's private webhook trigger (an empty or an overlapping prefix is
//     refused) runs in dev1's partition and announces into the DM, once.
const http = require('http');
const { login, sleep, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const INGRESS = process.env.INGRESS_ADDR || '127.0.0.1:8698';

async function eventually(what, fn, timeout = 30000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await sleep(300);
  }
}
const hook = (path, body) => new Promise((resolve, reject) => {
  const [host, port] = INGRESS.split(':');
  const req = http.request({ host, port, path, method: 'POST', headers: { Host: 'hooks.test', 'Content-Type': 'application/json' } }, (res) => {
    res.resume();
    res.on('end', () => resolve(res.statusCode));
  });
  req.on('error', reject);
  req.end(JSON.stringify(body));
});

async function channelsPartitioned(browser) {
  const { check, skip, done } = checker('channelsPartitioned');
  if (noGocryptfs()) { skip(`apps/agent and the bridge are held: ${noGocryptfs()}`); return done(); }
  const admin = await login(browser, 'admin', 'admin', { viewport: { width: 1200, height: 900 } });
  const page = admin.page;
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const ws = async (method, path, body) => {
    const r = await admin.ctx.request.fetch(`${URL}/api/${path}`, { method, data: body });
    let j = null;
    try { j = await r.json(); } catch { /* none */ }
    return { status: r.status(), body: j };
  };
  const agentAs = (p) => (path, opt) => p.evaluate(async ([path, opt]) => {
    const r = await xbin.fetch(`/api/apps/agent${path}`, opt ? { method: opt.method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(opt.body) } : {});
    let body = null;
    try { body = await r.json(); } catch { /* none */ }
    return { status: r.status, body };
  }, [path, opt]);
  await page.goto(`${URL}/c/apps/agent/#auto`);
  await page.waitForSelector('.autos-page', { timeout: 60000 });
  const agent = agentAs(page);
  const me = (await agent('/me')).body || {};
  if (!String(me.partition || '').startsWith('user:')) {
    skip(`apps/agent isn't partitioned here (partition ${me.partition || 'none'}): HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1`);
    await admin.ctx.close();
    return done();
  }
  const transcript = async () => (await ws('GET', 'apps/bridge/console/transcript')).body?.lines || [];
  const say = (text) => ws('POST', 'apps/bridge/console/send', { as: { id: 'u7', name: 'Uma' }, conversation: { id: 'D7', type: 'dm' }, text });

  // admin's own partition lists the global instance's channel; the claim is forwarded there
  await eventually('the bridge connected', async () => (await ws('GET', 'apps/bridge/status')).body?.state?.phase === 'connected', 180000);
  const item = await eventually('the channel', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel'), 60000);
  check(item.access === 'claim' || item.access === 'owner', `admin's partition offers the global instance's channel to claim (${item.name}, ${item.access})`);
  await page.reload();
  if (item.access === 'claim') {
    await page.click(`.acard2[data-auto="channel:${item.id}"]`);
    await page.waitForSelector('.autos-page button:has-text("Claim")');
    await shot(page, 'channelsp-admin-claim');
    await page.click('.autos-page button:has-text("Claim")');
  }
  await eventually('claimed', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel' && i.access === 'owner'));
  const put = await agent(`/channels/${item.id}`, { method: 'PUT', body: { visibility: 'team', policy: { dm: { policy: 'linked' } } } });
  check(put.status === 200, `claimed from admin's partition, forwarded to the global instance (${put.status})`);
  await page.goto(`${URL}/c/apps/agent/#auto=channel:${item.id}`);
  await page.waitForSelector('.autos-page', { timeout: 30000 });
  await sleep(1500);
  await shot(page, 'channelsp-admin-channel');

  // dev1 opens the agent once: their partition has run, so mail may start it
  const dev = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1200, height: 900 } });
  dev.page.on('pageerror', (e) => errors.push('dev1: ' + e.message));
  await dev.page.goto(`${URL}/c/apps/agent/`);
  await dev.page.waitForSelector('#runs', { timeout: 60000 });
  const devAgent = agentAs(dev.page);
  check(String((await devAgent('/me')).body?.partition || '') === 'user:dev1', 'dev1 reaches their own partition');

  await say('hello');
  const codeLine = await eventually('the link code', async () => (await transcript()).find((l) => l.dir === 'out' && l.conversation?.id === 'D7' && /[A-Z2-9]{8}/.test(l.text)), 60000);
  const code = /([A-Z2-9]{8})/.exec(codeLine.text)?.[1];
  await dev.page.goto(`${URL}/c/apps/bridge/`);
  await dev.page.waitForSelector('input[placeholder="code"]', { timeout: 20000 });
  await dev.page.fill('input[placeholder="code"]', code);
  await dev.page.click('button:has-text("Link")');
  await dev.page.waitForSelector('[data-linked]', { timeout: 10000 });
  check((await dev.page.textContent('[data-linked]')).includes('Linked Uma'), 'dev1 linked the chat account (at the global instance)');

  await say('hello again');
  const hi = await eventually('the DM answer', async () => (await transcript()).find((l) => l.dir === 'out' && l.conversation?.id === 'D7' && l.text.includes('Hello from the fake model')), 120000)
    .catch(() => null);
  check(!!hi, 'the linked DM is answered — by dev1\'s partition, through the global instance');
  await dev.page.goto(`${URL}/c/apps/agent/`);
  await dev.page.waitForSelector('#runs', { timeout: 60000 });
  const devRun = await eventually('dev1 has the DM', async () => ((await devAgent('/conversations')).body?.items || []).find((r) => r.origin === 'channel'), 20000).catch(() => null);
  check(!!devRun && devRun.id >= 2 ** 40, `the DM is a conversation of dev1's partition (${devRun && devRun.id})`);
  check(!((await agent('/conversations')).body?.items || []).some((r) => r.origin === 'channel'), 'admin\'s partition has no channel conversation');
  if (devRun) {
    await dev.page.goto(`${URL}/c/apps/agent/#c=${devRun.id}`);
    await dev.page.waitForSelector('#runs', { timeout: 30000 });
    await sleep(1500);
    await shot(dev.page, 'channelsp-dev1-dm');
  }

  // dev1's private webhook trigger, announcing into their DM
  const dmKey = `chan:${item.id}:dm:u7`;
  const trig = (who, name, match, deliver = '') => who('/triggers', { method: 'POST', body: { name, source: 'push', sourceRef: 'apps/webhooks', match,
    goal: 'quick check of {{topic}}', toolset: 'web', dataClass: 'public', deliver } });
  const empty = await trig(devAgent, 'dev1-all', '');
  check(empty.status === 400, `a private trigger without a topic prefix is refused (${empty.status})`);
  const tr = await trig(devAgent, 'dev1-deploys', 'pdeploy/', dmKey);
  check(tr.status === 200, `dev1's private trigger, registered at the global instance (${tr.status} ${tr.body?.error || ''})`);
  const over = await trig(agent, 'admin-prod', 'pdeploy/prod');
  check(over.status === 409, `admin's trigger overlapping dev1's is refused (${over.status})`);
  const h = await ws('POST', 'apps/webhooks/hooks', { name: 'pdeploy', auth: 'token', topicFrom: 'json:env', eventIdFrom: 'json:id' });
  const before = (await transcript()).length;
  let status = 0;
  await eventually('the hook taken', async () => (status = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { env: 'prod', id: 'p-1' })) === 202, 30000)
    .catch(() => {});
  check(status === 202, `the webhook through the ingress listener is taken (${status})`);
  const announced = await eventually('the announcement', async () => (await transcript()).slice(before)
    .find((l) => l.dir === 'out' && l.kind === 'announce' && l.conversation?.id === 'D7'), 120000).catch(() => null);
  check(!!announced, 'dev1\'s trigger ran in their partition and announced into their DM');
  const again = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { env: 'prod', id: 'p-1' });
  await sleep(3000);
  const evs = (await devAgent(`/triggers/${tr.body?.id}/events`)).body?.events || [];
  check(again === 202 && evs.filter((e) => e.accepted).length === 1, `the same delivery again runs nothing more (${again}, ${evs.length} events)`);
  await dev.page.goto(`${URL}/c/apps/agent/#auto`);
  await dev.page.waitForSelector('.autos-page', { timeout: 30000 });
  await sleep(1000);
  await shot(dev.page, 'channelsp-dev1-automations');
  const overseen = ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'trigger' && i.owner === 'dev1');
  check(!!overseen && overseen.access === 'oversee' && !overseen.config,
    `admin (a manager) sees dev1's trigger from their own partition — that it exists, not what it does (${overseen && overseen.access})`);
  await page.goto(`${URL}/c/apps/agent/#auto`);
  await page.reload();
  await page.waitForSelector('.autos-page', { timeout: 30000 });
  await sleep(1000);
  await shot(page, 'channelsp-admin-oversight');

  check(errors.length === 0, `no page errors ${errors.join(' | ')}`);
  await dev.ctx.close();
  await admin.ctx.close();
  return done();
}

module.exports = { channelsPartitioned };
