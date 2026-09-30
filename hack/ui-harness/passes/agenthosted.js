// hack/ui-harness/passes/agenthosted.js — a partitioned agent's non-secure
// (hosted) conversations (work pack B2d; the template's API.md "Non-secure
// conversations"), against the real apps/agent seeded partitioned
// (HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1) and hack/fakeopenai:
//   - the admin shares a conversation with dev1 (at the global instance),
//     then lets it use their private resources from its share dialog: the
//     warning names who can read it; the conversation moves (an id from
//     2^39) and carries the ⚠ "not private" chip;
//   - dev1 opens it: the warning opens by itself, "Open without sending"
//     leaves the composer locked; a reload is a new page session — the
//     warning again; "Start anyway" unlocks it; dev1's list row has the chip;
//   - dev1 writes: the admin's partition runs it and both pages watch it
//     stream at once (the host's run through the global instance, 90 §I4);
//   - the admin adds sales1: the conversation pauses — dev1's composer is
//     locked ("waiting for admin to confirm"), the admin is asked above the
//     composer; the admin confirms and dev1 may write again.
// Otherwise (the default unpartitioned seed) it says SKIP.
const { login, sleep, log, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const T39 = 2 ** 39, B = 2 ** 40;
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const call = (page, p, { method = 'GET', body, global } = {}) => page.evaluate(async ([p, method, body, global]) => {
  const r = await xbin.fetch(`/api/${xbin.self}${p}`, { method, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
    ...(global ? { partition: 'global' } : {}) });
  return { status: r.status, body: await r.json().catch(() => null) };
}, [p, method, body ?? null, !!global]);
const answered = (page, t, timeout = 60000) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);
const dialogOpen = (page) => page.evaluate(() => !!document.querySelector('#hostdlg')?.open);
const composer = (page) => page.evaluate(() => ({ disabled: document.getElementById('msg').disabled, placeholder: document.getElementById('msg').placeholder,
  bar: !document.getElementById('hostbar')?.hidden && (document.getElementById('hostbar')?.textContent || '').replace(/\s+/g, ' ').trim() }));

async function open(browser, user, pass) {
  const { ctx, page } = await login(browser, user, pass, { viewport: { width: 1300, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('response', (r) => { if (r.status() >= 500 && r.url().includes('/api/apps/agent')) log(`agentHosted(${user}): ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 60000 });
  return { ctx, page, errors };
}

const go = (x, id) => x.page.evaluate((id) => { location.hash = 'c=' + id; }, id);

async function agentHosted(browser) {
  const { check, skip, done } = checker('agent-hosted');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  if (!process.env.HARNESS_AGENT_PARTITION) { skip('apps/agent is seeded unpartitioned — run with HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1'); return done(); }
  const A = await open(browser, 'admin', 'admin');
  const part = await A.page.evaluate(() => xbin.partition || '');
  check(part.startsWith('user:'), `the admin's page is their own partition (xbin.partition ${JSON.stringify(part)})`);
  if (!part.startsWith('user:')) return done();

  // a conversation the admin shares with dev1 (at the global instance)
  const tok = `hplan${Date.now().toString(36)}`;
  const made = await call(A.page, '/ask', { method: 'POST', global: true,
    body: { text: `${tok} says hello`, share: { members: [{ user: 'dev1', role: 'participant' }] } } });
  check(made.status === 200 && made.body.id < T39, `a shared conversation at the global instance (${made.status}, #${made.body?.id})`);
  const shared = made.body.id;
  await go(A, shared);
  await answered(A.page, 'Hello from the fake model.');

  // the admin lets it use their private resources: the warning, then hosting
  await A.page.waitForSelector('#top .sharepill');
  await A.page.click('#top .sharepill');
  await A.page.waitForSelector('#sharedlg #host-use');
  await shot(A.page, 'agent-hosted-share-dialog');
  await A.page.click('#sharedlg #host-use');
  await A.page.waitForSelector('#hostdlg[open] #host-go');
  const readers = await A.page.$$eval('#hostdlg #host-readers li', (els) => els.map((e) => e.textContent.trim()));
  check(readers.some((r) => r.includes('admin') && r.includes('dev1')) && readers.some((r) => r.includes('managers'))
    && readers.some((r) => r.includes('admins')) && readers.some((r) => r.includes("change this agent's code")),
  `the warning before hosting lists who can read it (${JSON.stringify(readers)})`);
  await shot(A.page, 'agent-hosted-host-warning');
  await A.page.click('#hostdlg #host-go');
  const hid = await until(A.page, ([lo, hi]) => { const m = /#c=(\d+)/.exec(location.hash); return m && +m[1] >= lo && +m[1] < hi && +m[1]; }, [T39, B], 30000)
    .then((h) => h.jsonValue(), () => 0);
  check(hid >= T39 && hid < B, `hosting moved it into the non-secure space (#${shared} → #${hid})`);
  await until(A.page, () => !!document.querySelector('#top #hosted-chip'), null, 20000).catch(() => {});
  check(await A.page.evaluate(() => (document.querySelector('#top #hosted-chip')?.textContent || '').includes('not private')), 'the ⚠ "not private" chip on its header');
  check(!(await composer(A.page)).disabled && !(await dialogOpen(A.page)), 'its host, who just read the warning, may write at once');
  await shot(A.page, 'agent-hosted-admin');

  // dev1 opens it: the warning by itself; without sending, the composer stays locked
  const D = await open(browser, 'dev1', 'devpass123');
  await go(D, hid);
  const warned = await until(D.page, () => !!document.querySelector('#hostdlg')?.open && !!document.querySelector('#hostdlg #host-anyway'), null, 30000).then(() => true, () => false);
  check(warned, 'dev1 opening it: the warning opens by itself');
  await shot(D.page, 'agent-hosted-start-warning');
  await D.page.click('#hostdlg #host-nosend');
  let c = await composer(D.page);
  check(c.disabled && /without sending/.test(c.placeholder) && !!c.bar, `"Open without sending": the composer is locked (${JSON.stringify(c)})`);
  await shot(D.page, 'agent-hosted-locked');
  // a new page session: the warning again; Start anyway unlocks
  await D.page.reload();
  await D.page.waitForSelector('#msg', { timeout: 60000 });
  await go(D, hid);
  const again = await until(D.page, () => !!document.querySelector('#hostdlg')?.open, null, 30000).then(() => true, () => false);
  check(again, 'after a reload (a new page session) the warning opens again — no "don\'t show again"');
  await D.page.click('#hostdlg #host-anyway');
  await until(D.page, () => !document.getElementById('msg').disabled, null, 10000).catch(() => {});
  c = await composer(D.page);
  check(!c.disabled && !c.bar, `"Start anyway": dev1 may write (${JSON.stringify(c)})`);
  await until(D.page, (id) => !!document.querySelector(`#runs .run[data-id="${id}"] .chip.notprivate`), hid, 20000).catch(() => {});
  check(await D.page.evaluate((id) => !!document.querySelector(`#runs .run[data-id="${id}"] .chip.notprivate`), hid), 'dev1\'s list row has the ⚠ chip');

  // dev1 writes; the admin's partition runs it; both watch it stream
  await D.page.fill('#msg', 'paras 8');
  await D.page.press('#msg', 'Enter');
  const liveBoth = await Promise.all([A, D].map((x) => until(x.page, () => !!document.querySelector('#timeline .msg.assistant.live'), null, 60000).then(() => true, () => false)));
  check(liveBoth.every(Boolean), `both pages watch the host's run stream (${liveBoth})`);
  await shot(D.page, 'agent-hosted-live-dev1');
  for (const x of [A, D]) await answered(x.page, 'Paragraph 8').catch(() => {});
  check(await D.page.evaluate(() => [...document.querySelectorAll('#timeline .msg.assistant')].some((e) => e.textContent.includes('Paragraph 8'))), 'the answer, written by the admin\'s partition');

  // a wider audience: paused for everyone, the host asked
  const added = await call(A.page, `/runs/${hid}/members`, { method: 'POST', global: true, body: { user: 'sales1', role: 'viewer' } });
  check(added.status === 200, `the admin adds sales1 (${added.status})`);
  const paused = await until(D.page, () => document.getElementById('msg').disabled && /waiting for admin/.test(document.getElementById('msg').placeholder), null, 30000)
    .then(() => true, () => false);
  c = await composer(D.page);
  check(paused, `widening pauses it: dev1's composer is locked (${JSON.stringify(c)})`);
  const asked = await until(A.page, () => !!document.querySelector('#hostbar #host-confirm') && document.getElementById('hostbar').textContent.includes('sales1'), null, 30000)
    .then(() => true, () => false);
  check(asked, 'the admin is asked to confirm sales1 above the composer');
  await shot(D.page, 'agent-hosted-paused-dev1');
  await shot(A.page, 'agent-hosted-paused-admin');
  await A.page.click('#hostbar #host-confirm');
  const resumed = await until(D.page, () => !document.getElementById('msg').disabled, null, 30000).then(() => true, () => false);
  check(resumed, 'the admin confirms: dev1 may write again');

  const errs = [...A.errors, ...D.errors];
  check(errs.length === 0, `no page errors (${errs.slice(0, 3).join(' | ')})`);
  await sleep(100);
  await A.ctx.close();
  await D.ctx.close();
  return done();
}

module.exports = { agentHosted };
