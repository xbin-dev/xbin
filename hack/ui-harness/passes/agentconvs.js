// hack/ui-harness/passes/agentconvs.js — per-user conversations in the agent
// template (D83), with two real users against the seeded apps/agent:
//   - admin's new chat is private: dev1 neither lists it nor can open it;
//   - shared with the team to read, dev1 finds it under "Shared with team"
//     with a read-only composer;
//   - an invite link makes dev1 a participant: dev1 writes, admin sees who,
//     and the model is told who spoke ([dev1] …);
//   - removed and made private again, dev1's open chat goes away.
const { login, sleep, shot, checker } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const FAKE = `http://${process.env.FAKEOPENAI_ADDR || '127.0.0.1:18977'}`;
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const api = (page, path, opt) => page.evaluate(async ([path, opt]) => {
  const r = await xbin.fetch(`/api/apps/agent${path}`, opt || {});
  let body = null;
  try { body = await r.json(); } catch { /* none */ }
  return { status: r.status, body };
}, [path, opt]);
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

async function openAgent(browser, user, pass) {
  const { ctx, page } = await login(browser, user, pass, { viewport: { width: 1200, height: 850 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  return { ctx, page, errors };
}

async function agentConvs(browser) {
  const { check, done } = checker('agent-convs');
  const admin = await openAgent(browser, 'admin', 'admin');
  const dev = await openAgent(browser, 'dev1', 'devpass123');

  // admin's new chat
  const title = `private plans ${Date.now()}`;
  await admin.page.click('#newopts');
  await admin.page.fill('#n-title', title);
  await admin.page.fill('#n-goal', 'quick question about the budget');
  await admin.page.click('#n-create');
  await until(admin.page, (t) => document.querySelector('#top .title')?.textContent === t, title);
  await until(admin.page, () => [...document.querySelectorAll('#timeline .msg.assistant')].some((e) => e.textContent.includes('Quick answer')));
  const id = +(await admin.page.evaluate(() => /c=(\d+)/.exec(location.hash)?.[1]));
  check(id > 0, `the chat's link is #c=${id}`);
  check((await admin.page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent))).includes(title), 'it is in admin\'s list, under Today');

  // dev1 can't see it
  await sleep(500);
  const devRows = () => dev.page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent));
  check(!(await devRows()).includes(title), 'dev1 does not list admin\'s private chat');
  check((await api(dev.page, `/runs/${id}/view`)).status === 404, 'dev1 opening it by id gets 404');
  const found = await api(dev.page, `/conversations?q=${encodeURIComponent('budget')}`);
  check(!(found.body.items || []).some((r) => r.id === id), 'dev1 search finds nothing of it');

  // shared with the team, to read
  await admin.page.click('#top button:has-text("Share")');
  await admin.page.waitForSelector('#sharedlg .prow');
  await admin.page.click('#sharedlg input[type=radio] >> nth=1');
  await until(admin.page, () => document.querySelector('#sharedlg input[type=radio]:nth-of-type(1)') !== null);
  await sleep(300);
  await admin.page.click('#sharedlg button:has-text("Done")');
  await dev.page.click('#sfoot a:has-text("Shared with team")');
  await until(dev.page, (t) => document.getElementById('runs').textContent.includes(t), title);
  check(true, 'dev1 finds it under "Shared with team"');
  await dev.page.click(`#runs .run:has-text("${title}")`);
  await until(dev.page, (t) => document.querySelector('#top .title')?.textContent === t, title);
  check(await dev.page.$eval('#msg', (e) => e.disabled), 'shared to read: dev1\'s composer is read-only');
  check((await dev.page.textContent('#top')).includes('view only'), '…and says so');
  await shot(dev.page, 'agent-convs-view-only', { fullPage: false });

  // an invite link makes dev1 a participant
  const link = await api(admin.page, `/runs/${id}/links`, json('POST', { role: 'participant', expiresIn: 3600 }));
  check(link.status === 200 && !!link.body.token, 'admin creates an invite link');
  await dev.page.goto(`${URL}/c/apps/agent/#join=${link.body.token}`);
  // Joined = the conversation is selected AND writable (the composer of the
  // page's own empty chat is enabled before the join lands).
  const joined = await until(dev.page, (t) => !document.getElementById('msg').disabled && document.querySelector('#top .title')?.textContent === t, title, 10000).then(() => true, () => false);
  check(joined, 'the link made dev1 a participant');
  await dev.page.fill('#msg', 'hello from dev1');
  await dev.page.press('#msg', 'Enter');
  await until(admin.page, () => [...document.querySelectorAll('#timeline .msg.user.other .who')].some((e) => e.textContent === 'dev1'));
  check(true, 'admin sees dev1\'s message, labelled');
  let told = false;
  for (let i = 0; i < 100 && !told; i++) {
    const reqs = await (await fetch(`${FAKE}/debug/requests`)).json();
    told = reqs.some((r) => /\[dev1\] hello from dev1/.test(r.last));
    if (!told) await sleep(100);
  }
  check(told, 'the model is told who spoke: "[dev1] hello from dev1"');
  await shot(admin.page, 'agent-convs-shared', { fullPage: false });

  // removed and private again: dev1's open chat goes away
  await api(admin.page, `/runs/${id}/members/dev1`, { method: 'DELETE' });
  await api(admin.page, `/runs/${id}`, json('PATCH', { visibility: 'private' }));
  await until(dev.page, () => !document.querySelector('#top .title') || document.querySelector('.home'), null, 10000);
  check(true, 'once removed, dev1\'s view of it closes');
  check((await api(dev.page, `/runs/${id}/view`)).status === 404, '…and it is 404 again');

  // an automation: created on the Automations page, run now; its run is listed
  // there (with what is new), not among admin's conversations; dev1 sees none
  // of it
  await admin.page.click('#autos .autos-entry');
  await admin.page.click('.autos-page button:has-text("New schedule")');
  await admin.page.fill('.autos-page input[placeholder="Morning digest"]', 'harness digest');
  await admin.page.fill('.autos-page textarea', 'a quick digest');
  await admin.page.click('.autos-page button:has-text("Create")');
  await admin.page.waitForSelector('.autos-page .agoal');
  await admin.page.click('.autos-page button:has-text("Run now")');
  await until(admin.page, () => document.querySelectorAll('.autos-page .run').length > 0, null, 30000);
  check(true, 'an automation created on the page runs, and its run is listed under it');
  check(!(await admin.page.$$eval('#runs .run .t', (els) => els.map((e) => e.textContent))).some((t) => t.includes('harness digest')),
    'its run is not among admin\'s conversations');
  const devAutos = await api(dev.page, '/automations');
  check(!(devAutos.body.items || []).some((i) => i.name === 'harness digest'), 'dev1 does not see admin\'s automation');
  await shot(admin.page, 'agent-convs-automation', { fullPage: false });

  check(admin.errors.length === 0 && dev.errors.length === 0, `no page errors (${[...admin.errors, ...dev.errors].join(' | ')})`);
  await admin.ctx.close();
  await dev.ctx.close();
  done();
}

module.exports = { agentConvs };
