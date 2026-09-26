// hack/ui-harness/passes/channels.js — chat channels, identity links, files
// and triggers end to end (D84–D87): a copy of the agent-messaging-bridge
// template whose built-in console plays the chat platform, the agent, the
// webhooks tile, hack/fakeopenai.
//   - the bridge (alwaysOn) connects by itself; its page says it has no
//     platform yet and how to add one; the agent offers the account to claim;
//   - a stranger's DM gets a link code; dev1, signed in, pastes it on the
//     bridge's page — from then on that chat account is dev1: their DM is
//     their own conversation (in dev1's sidebar, not admin's);
//   - an attachment reaches the conversation; a reply carries a file back;
//     a mention in a channel is answered in a thread under it;
//   - a webhook through the ingress listener runs a trigger that announces
//     into the DM — once, however often the same delivery comes;
//   - a bus event runs a bus trigger;
//   - the bridge backend killed: alwaysOn brings it back, and it reconnects.
const http = require('http');
const { login, sleep, shot, checker } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const INGRESS = process.env.INGRESS_ADDR || '127.0.0.1:8698';

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
  // the agent, from a page of it (attributed to whoever is signed in there)
  const agentAs = (p) => (path, opt) => p.evaluate(async ([path, opt]) => {
    const r = await xbin.fetch(`/api/apps/agent${path}`, opt ? { method: opt.method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(opt.body) } : {});
    let body = null;
    try { body = await r.json(); } catch { /* none */ }
    return { status: r.status, body };
  }, [path, opt]);
  const agent = agentAs(page);
  const backend = async (p) => (await ws('GET', `xbin/tile-status?component=${p}`)).body?.backend || {};
  const transcript = async () => (await ws('GET', 'apps/bridge/console/transcript')).body?.lines || [];
  const say = (conv, text, extra = {}) => ws('POST', 'apps/bridge/console/send', { as: { id: 'u1', name: 'Uma' }, conversation: conv, text, ...extra });
  const DM = { id: 'D1', type: 'dm' };
  const GENERAL = { id: 'C1', type: 'channel', name: 'general' };

  // the bridge runs by itself and tells you what it is
  const connected = await eventually('the bridge connected', async () => (await ws('GET', 'apps/bridge/status')).body?.state?.phase === 'connected', 120000)
    .catch(() => false);
  check(connected, 'the bridge template connected by itself (alwaysOn, its console)');
  await page.goto(`${URL}/c/apps/bridge/`);
  await page.waitForSelector('[data-customise]', { timeout: 20000 });
  check((await page.textContent('[data-customise]')).includes('AGENTS.md'), 'its page says it has no platform yet and how a coding agent adds one');
  await shot(page, 'channels-bridge-template');

  // claim the console account on the agent; only linked people may DM
  await page.goto(`${URL}/c/apps/agent/#auto`);
  await page.waitForSelector('.autos-page', { timeout: 30000 });
  const item = await eventually('the channel', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel'));
  check(item.access === 'claim', `the agent offers the account to claim (${item.name})`);
  await page.reload();
  await page.click(`.acard2[data-auto="channel:${item.id}"]`);
  await page.waitForSelector('.autos-page button:has-text("Claim")');
  await page.click('.autos-page button:has-text("Claim")');
  await eventually('claimed', async () => ((await agent('/automations')).body?.items || []).find((i) => i.kind === 'channel' && i.access === 'owner'));
  await agent(`/channels/${item.id}`, { method: 'PUT', body: { policy: { dm: { policy: 'linked' }, groups: { allow: ['C1'] } } } });
  check(true, 'claimed from the page; DMs only for linked people');
  const told = await eventually('the bridge told', async () => (await ws('GET', 'apps/bridge/status')).body?.state?.accounts?.[0]?.claimed === 'active', 15000)
    .catch(() => false);
  check(!!told, 'the agent told the bridge it was claimed (before any message)');

  // a stranger gets a link code; dev1 pastes it on the bridge's page
  await say(DM, 'hello');
  const codeLine = await eventually('the link code', async () => (await transcript()).find((l) => l.dir === 'out' && /only talk with people who linked/.test(l.text)));
  const code = /([A-Z2-9]{8})/.exec(codeLine.text)?.[1];
  check(!!code, `an unlinked DM gets a link code (${code})`);
  const dev = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1200, height: 900 } });
  await dev.page.goto(`${URL}/c/apps/bridge/`);
  await dev.page.waitForSelector('input[placeholder="code"]', { timeout: 20000 });
  await dev.page.fill('input[placeholder="code"]', code);
  await dev.page.click('button:has-text("Link")');
  await dev.page.waitForSelector('[data-linked]', { timeout: 10000 });
  check((await dev.page.textContent('[data-linked]')).includes('Linked Uma'), 'dev1 linked the chat account on the bridge page');
  await shot(dev.page, 'channels-link');
  await eventually('told they are linked', async () => (await transcript()).some((l) => l.dir === 'out' && l.text.includes('@dev1')));

  // now the chat account is dev1
  await say(DM, 'hello');
  const hi = await eventually('the DM answer', async () => (await transcript()).find((l) => l.dir === 'out' && l.text.includes('Hello from the fake model')), 60000)
    .catch(() => null);
  check(!!hi, 'the linked DM is answered');
  await dev.page.goto(`${URL}/c/apps/agent/`);
  await dev.page.waitForSelector('#runs', { timeout: 30000 });
  const devRun = await eventually('dev1 has the DM', async () => ((await agentAs(dev.page)('/conversations')).body?.items || [])
    .find((r) => r.origin === 'channel'), 20000).catch(() => null);
  check(!!devRun, `the DM is dev1's own conversation (${devRun && devRun.title})`);
  if (devRun) {
    check((await agent(`/runs/${devRun.id}/view`)).status === 404, 'admin, the channel\'s owner, can\'t read dev1\'s DM');
  }

  // files both ways
  await say(DM, 'what is in this file', { files: [{ name: 'notes.txt', mime: 'text/plain', data: Buffer.from('line one').toString('base64') }] });
  const attached = devRun && await eventually('the attachment in the conversation', async () => {
    const v = (await agentAs(dev.page)(`/runs/${devRun.id}/view`)).body;
    return (v?.messages || []).some((m) => (m.content || '').includes('[attached: notes.txt'));
  }, 30000).catch(() => false);
  check(!!attached, 'an attachment reaches the conversation as a session file');
  await say(DM, 'make a file');
  const withFile = await eventually('a reply with a file', async () => (await transcript()).find((l) => l.dir === 'out' && (l.files || []).some((f) => f.name === 'note.txt')), 60000)
    .catch(() => null);
  check(!!withFile && withFile.text.includes('Here is the file'), 'a reply carries the file the model attached');
  if (withFile) {
    const f = withFile.files.find((x) => x.name === 'note.txt');
    const r = await admin.ctx.request.get(`${URL}/api/apps/bridge/console/files/${f.id}`);
    check((await r.text()) === 'made by the fake model', 'the file arrives intact');
  }

  // a mention in a channel is answered in a thread under it
  const m = (await say(GENERAL, 'quick question', { mentioned: true })).body;
  const threaded = await eventually('the thread answer', async () => (await transcript()).find((l) => l.dir === 'out' && l.conversation.id === 'C1' && l.thread === m.messageId), 60000)
    .catch(() => null);
  check(!!threaded && threaded.text.includes('Quick answer'), `a mention is answered in a thread under it (${threaded && threaded.text})`);
  await page.goto(`${URL}/c/apps/bridge/`);
  await page.waitForSelector('[data-transcript]');
  check((await page.textContent('#app')).includes('claimed on the agent'), 'the bridge page shows the claim');
  await shot(page, 'channels-console');

  // a webhook runs a trigger that announces into the DM
  const dmKey = `chan:${item.id}:dm:u1`;
  await page.goto(`${URL}/c/apps/agent/#auto`);
  await page.waitForSelector('.autos-page');
  const tr = await agent('/triggers', { method: 'POST', body: { name: 'deploy-hook', source: 'push', sourceRef: 'apps/webhooks', match: 'deploy',
    goal: 'quick check of {{topic}}', toolset: 'web', dataClass: 'public', deliver: dmKey } });
  check(tr.status === 200, `a push trigger announcing into the DM (${tr.status})`);
  const h = await ws('POST', 'apps/webhooks/hooks', { name: 'deploy', auth: 'token' });
  const before = (await transcript()).length;
  let status = 0;
  await eventually('the hook taken', async () => (status = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { ref: 'main' })) === 202, 20000)
    .catch(() => {});
  check(status === 202, `the webhook through the ingress listener is taken (${status})`);
  const announced = await eventually('the announcement', async () => (await transcript()).slice(before)
    .find((l) => l.dir === 'out' && l.kind === 'announce' && l.text.includes('[trigger deploy-hook]')), 60000).catch(() => null);
  check(!!announced, 'the trigger announced its answer into the DM');
  const again = await hook(`/hook/${h.body.hook.id}?token=${h.body.secret}`, { ref: 'main' });
  await sleep(1500);
  const evs = (await agent(`/triggers/${tr.body.id}/events`)).body?.events || [];
  check(again === 202 && evs.filter((e) => e.accepted).length === 1, `the same delivery again runs nothing more (${again}, ${evs.length} events)`);

  // a bus event runs a bus trigger
  const bt = await agent('/triggers', { method: 'POST', body: { name: 'harness-bus', source: 'bus', sourceRef: 'res:apps/agent/events',
    match: 'harness/', goal: 'quick note on {{topic}}' } });
  check(bt.status === 200 && bt.body.status === 'ok', `a bus trigger subscribes (${bt.status} ${bt.body && bt.body.status})`);
  await ws('POST', 'xbin/bus/publish', { resource: 'res:apps/agent/events', topic: 'harness/ping', data: { n: 1 } });
  const busRun = await eventually('the bus run', async () => ((await agent(`/automations/trigger/${bt.body.id}/runs`)).body?.items || [])[0], 30000)
    .catch(() => null);
  check(!!busRun, 'a bus event ran it');

  // alwaysOn: kill the bridge backend; it comes back and reconnects
  const b0 = await backend('apps/bridge');
  if (b0.pid) process.kill(b0.pid, 'SIGKILL');
  const back = await eventually('the bridge back', async () => {
    const b = await backend('apps/bridge');
    return b.gen > b0.gen && b.state === 'healthy' && (await ws('GET', 'apps/bridge/status')).body?.state?.phase === 'connected' && b;
  }, 60000).catch(() => null);
  check(!!b0.pid && !!back, `killed, the bridge came back and reconnected (gen ${b0.gen} → ${back?.gen})`);

  check(errors.length === 0, `no page errors ${errors.join(' | ')}`);
  await dev.ctx.close();
  await admin.ctx.close();
  return done();
}

module.exports = { channels };
