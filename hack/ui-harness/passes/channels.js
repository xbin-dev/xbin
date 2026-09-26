// hack/ui-harness/passes/channels.js — chat channels and triggers end to end
// (D84–D87), with the real tiles against hack/fakeslack and hack/fakeopenai:
//   - the slack tile (alwaysOn) connects by itself and says hello to the
//     agent; the Automations page offers the account to claim;
//   - a stranger's DM gets a pairing code, approved on the page; the next DM
//     is answered in Slack; a mention in an allowed channel is answered in a
//     thread under it; both sessions are listed and open as conversations;
//   - a webhook through the ingress listener runs a trigger that announces
//     into the DM — once, however often the same delivery comes;
//   - a bus event runs a bus trigger (a push subscription);
//   - the slack backend killed: alwaysOn brings it back, and it reconnects.
const http = require('http');
const { login, sleep, shot, checker } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const SLACK = `http://${process.env.FAKESLACK_ADDR || '127.0.0.1:18978'}`;
const INGRESS = process.env.INGRESS_ADDR || '127.0.0.1:8698';

const fake = async (path, body) => {
  const r = await fetch(SLACK + path, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {});
  return r.json();
};
const posts = () => fake('/debug/posts');
async function eventually(what, fn, timeout = 30000) {
  const end = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await sleep(250);
  }
}
// a webhook through the ingress listener (Host: the published name)
const hook = (path, body) => new Promise((resolve, reject) => {
  const [host, port] = INGRESS.split(':');
  const req = http.request({ host, port, path, method: 'POST', headers: { Host: 'hooks.test', 'Content-Type': 'application/json' } }, (res) => {
    res.resume();
    res.on('end', () => resolve(res.statusCode));
  });
  req.on('error', reject);
  req.end(JSON.stringify(body));
});

async function channels(browser) {
  const { check, done } = checker('channels');
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1200, height: 850 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const ws = async (method, path, body) => {
    const r = await ctx.request.fetch(`${URL}/api/${path}`, { method, data: body });
    let j = null;
    try { j = await r.json(); } catch { /* none */ }
    return { status: r.status(), body: j };
  };
  const agent = (path, opt) => page.evaluate(async ([path, opt]) => {
    const r = await xbin.fetch(`/api/apps/agent${path}`, opt ? { method: opt.method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(opt.body) } : {});
    let body = null;
    try { body = await r.json(); } catch { /* none */ }
    return { status: r.status, body };
  }, [path, opt]);
  // the runner's view of a backend (tile-status has the pid; /backends doesn't)
  const backend = async (p) => (await ws('GET', `xbin/tile-status?component=${p}`)).body?.backend || {};

  // the slack tile runs by itself (alwaysOn) and connects
  await page.goto(`${URL}/c/apps/agent/#auto`);
  await page.waitForSelector('.autos-page', { timeout: 30000 });
  let phase = '';
  try {
    await eventually('slack connected', async () => (phase = (await ws('GET', 'apps/slack/status')).body?.state?.phase) === 'connected', 60000);
  } catch {
    // started before its bindings existed: a restart picks them up (alwaysOn restarts it)
    const b = await backend('apps/slack');
    if (b.pid) process.kill(b.pid, 'SIGKILL');
    await eventually('slack connected after a restart', async () => (phase = (await ws('GET', 'apps/slack/status')).body?.state?.phase) === 'connected', 120000)
      .catch(() => {});
  }
  check(phase === 'connected', `the slack tile connected by itself (alwaysOn): ${phase}`);
  const list = await eventually('the channel', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel'));
  const ch = list.id;
  check(list.access === 'claim', `the agent offers the Slack account to claim (${list.name})`);

  // claim it on the page
  await page.reload();
  await page.waitForSelector(`.acard2[data-auto="channel:${ch}"]`);
  await shot(page, 'channels-list');
  await page.click(`.acard2[data-auto="channel:${ch}"]`);
  await page.waitForSelector('.autos-page button:has-text("Claim")');
  await shot(page, 'channels-claim');
  await page.click('.autos-page button:has-text("Claim")');
  await eventually('claimed', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel' && i.access === 'owner'));
  check(true, 'claimed from the page');

  // a stranger DMs: a pairing code; approved on the page
  await fake('/debug/inject', { kind: 'dm', user: 'U1', text: 'hello' });
  const pairing = await eventually('the pairing code', async () => (await posts()).find((p) => p.channel === 'D1' && /pairing code/.test(p.text)));
  const code = /code: ([A-Z2-9]{8})/.exec(pairing.text)?.[1];
  check(!!code, `a stranger's DM gets a pairing code (${code})`);
  await page.reload();
  await page.waitForSelector('.chadd input');
  await page.fill('.chadd input >> nth=0', code);
  await page.click('.chadd button:has-text("Approve")');
  await page.waitForSelector('.autos-page .note');
  check((await page.textContent('.autos-page .note')).includes('Paired with'), 'approving the code on the page pairs them');

  // the conversation
  await fake('/debug/inject', { kind: 'dm', user: 'U1', text: 'hello' });
  const dmAnswer = await eventually('the DM answer', async () => (await posts()).find((p) => p.channel === 'D1' && p.text.includes('Hello from the fake model')), 60000)
    .catch(() => null);
  check(!!dmAnswer, 'a DM is answered in Slack');
  await agent(`/channels/${ch}`, { method: 'PUT', body: { policy: { groups: { allow: ['C1'] } } } });
  const m = await fake('/debug/inject', { kind: 'mention', user: 'U2', channel: 'C1', text: 'quick question' });
  const threaded = await eventually('the thread answer', async () => (await posts()).find((p) => p.channel === 'C1' && p.thread_ts === m.ts), 60000)
    .catch(() => null);
  check(!!threaded && threaded.text.includes('Quick answer'), `a mention is answered in a thread under it (${threaded && threaded.text})`);

  // its sessions, open as conversations
  await page.reload();
  await page.waitForSelector('.autos-page h5:has-text("Sessions")');
  const sessions = await page.$$eval('.chrow a.mono', (els) => els.map((e) => e.textContent));
  check(sessions.length === 2 && sessions.some((k) => k.includes(':dm:U1')) && sessions.some((k) => k.includes(':group:C1:thread:')),
    `the DM and the thread are its sessions (${sessions.join(', ')})`);
  await shot(page, 'channels-detail');
  await page.click('.chrow a.mono >> text=/:dm:U1/');
  await page.waitForFunction(() => (document.querySelector('#top .title')?.textContent || '').includes('Slack'), null, { timeout: 15000 });
  check(true, 'a session opens as its conversation');
  await shot(page, 'channels-conversation');

  // a webhook runs a trigger that announces into the DM
  const dmKey = `chan:${ch}:dm:U1`;
  const tr = await agent('/triggers', { method: 'POST', body: { name: 'deploy-hook', source: 'push', sourceRef: 'apps/webhooks', match: 'deploy',
    goal: 'quick check of {{topic}}', toolset: 'web', dataClass: 'public', deliver: dmKey } });
  check(tr.status === 200, `a push trigger announcing into the DM (${tr.status})`);
  const h = await ws('POST', 'apps/webhooks/hooks', { name: 'deploy', auth: 'token' });
  check(!!h.body?.secret, 'a webhook with a token');
  const before = (await posts()).filter((p) => p.channel === 'D1').length;
  // retried until taken (the route may still be settling); the same body is
  // the same event, so a retry can't run the trigger twice
  let status = 0;
  await eventually('the hook taken', async () => (status = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { ref: 'main' })) === 202, 20000)
    .catch(() => {});
  check(status === 202, `the webhook through the ingress listener is taken (${status})`);
  // (the fake model answers whatever the prompt says; the announcement is the
  // new DM post that answers the trigger's prompt)
  const announced = await eventually('the announcement', async () => (await posts()).filter((p) => p.channel === 'D1').slice(before)
    .find((p) => p.text.includes('[trigger deploy-hook]')), 60000).catch(() => null);
  check(!!announced, `the trigger announced its answer into the DM (${announced && announced.text.slice(0, 60)})`);
  const again = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { ref: 'main' });
  await sleep(1500);
  const evs = (await agent(`/triggers/${tr.body.id}/events`)).body?.events || [];
  check(again === 202 && evs.filter((e) => e.accepted).length === 1, `the same delivery again runs nothing more (${again}, ${evs.length} events)`);
  if (tr.body?.id) {
    await page.goto(`${URL}/c/apps/agent/#auto=trigger:${tr.body.id}`);
    await page.waitForSelector('.autos-page .agoal');
    await shot(page, 'channels-trigger');
  }

  // a bus event runs a bus trigger
  const bt = await agent('/triggers', { method: 'POST', body: { name: 'harness-bus', source: 'bus', sourceRef: 'res:apps/agent/events',
    match: 'harness/', goal: 'quick note on {{topic}}' } });
  check(bt.status === 200 && bt.body.status === 'ok', `a bus trigger subscribes (${bt.status} ${bt.body && bt.body.status})`);
  await ws('POST', 'xbin/bus/publish', { resource: 'res:apps/agent/events', topic: 'harness/ping', data: { n: 1 } });
  const busRun = await eventually('the bus run', async () => ((await agent(`/automations/trigger/${bt.body.id}/runs`)).body?.items || [])[0], 30000)
    .catch(() => null);
  check(!!busRun, 'a bus event ran it');

  // alwaysOn: kill the slack backend; it comes back and reconnects
  const b0 = await backend('apps/slack');
  const opens0 = (await fake('/debug/state')).opens;
  if (b0.pid) process.kill(b0.pid, 'SIGKILL');
  const back = await eventually('slack back', async () => {
    const b = await backend('apps/slack');
    const s = await fake('/debug/state');
    return b.gen > b0.gen && b.state === 'healthy' && s.connected && s.opens > opens0;
  }, 60000).catch(() => false);
  check(!!b0.pid && back, `killed, the slack backend came back and reconnected (gen ${b0.gen} →)`);

  // the tiles' own pages
  await page.goto(`${URL}/c/apps/slack/`);
  await page.waitForSelector('[data-phase]', { timeout: 15000 });
  await shot(page, 'channels-slack-tile');
  await page.goto(`${URL}/c/apps/webhooks/`);
  await page.waitForSelector('[data-hook]', { timeout: 15000 });
  await shot(page, 'channels-webhooks-tile');

  check(errors.length === 0, `no page errors ${errors.join(' | ')}`);
  await ctx.close();
  return done();
}

module.exports = { channels };
