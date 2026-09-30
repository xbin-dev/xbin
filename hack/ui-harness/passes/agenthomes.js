// hack/ui-harness/passes/agenthomes.js — a partitioned agent's two homes
// (work pack B2b; the template's API.md "Partitioned instances" → "Shared
// conversations"), against the real apps/agent seeded partitioned
// (HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1) and hack/fakeopenai:
//   - the seed: the admin and dev1 each chat privately in their own
//     partition, and the admin starts one shared with the team at the global
//     instance (xbin.fetch's {partition: 'global'});
//   - the admin's Mine lists their own and the shared one; dev1's Mine never
//     the admin's own, dev1's Shared view the shared one;
//   - #c=<the shared id> opens it at global in dev1's page (the router by
//     id), and both people see its run streaming at the same time (90 §I4);
//   - Share a copy: the admin publishes their own conversation; the copy
//     opens at global and dev1 lists it;
//   - Copy to my own space: dev1 copies the shared one into their partition;
//   - the global-viewer state: the owner token's page is the global
//     instance's single list, with the note to sign in as a person.
// Otherwise (the default unpartitioned seed) it says SKIP.
const path = require('path');
const { login, fs, sleep, log, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const B = 2 ** 40;
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
// call: this page's own backend (the tile's API) through its frame's xbin.fetch — at the global instance with global.
const call = (page, p, { method = 'GET', body, global } = {}) => page.evaluate(async ([p, method, body, global]) => {
  const r = await xbin.fetch(`/api/${xbin.self}${p}`, { method, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
    ...(global ? { partition: 'global' } : {}) });
  return { status: r.status, body: await r.json().catch(() => null) };
}, [p, method, body ?? null, !!global]);
const rows = (page) => page.$$eval('#runs .run', (els) => els.map((e) => ({ id: +e.dataset.id, t: e.querySelector('.t')?.textContent || '' })));
const answered = (page, t, timeout = 60000) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);

async function open(browser, user, pass) {
  const { ctx, page } = await login(browser, user, pass, { viewport: { width: 1300, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('response', (r) => { if (r.status() >= 500 && r.url().includes('/api/apps/agent')) log(`agentHomes(${user}): ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 60000 });
  return { ctx, page, errors };
}

async function agentHomes(browser) {
  const { check, skip, done } = checker('agent-homes');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  if (!process.env.HARNESS_AGENT_PARTITION) { skip('apps/agent is seeded unpartitioned — run with HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1'); return done(); }
  const A = await open(browser, 'admin', 'admin');
  const D = await open(browser, 'dev1', 'devpass123');
  const part = await A.page.evaluate(() => xbin.partition || '');
  // HARNESS_AGENT_PARTITION says it is partitioned: a page that isn't is a failure, not a skip
  check(part.startsWith('user:'), `the admin's page is their own partition (xbin.partition ${JSON.stringify(part)})`);
  if (!part.startsWith('user:')) return done();

  // the seed: one private conversation each, one shared with the team (at global)
  const n = Date.now().toString(36);
  const ask = async (who, text, global) => {
    const r = await call(who.page, '/ask', { method: 'POST', body: { text, ...(global ? { share: { visibility: 'team', teamRole: 'participant' } } : {}) }, global });
    if (r.status !== 200) throw new Error(`seed ask ${text}: ${r.status} ${JSON.stringify(r.body)}`);
    return r.body.id;
  };
  // (a title's first words carry the run's token: the model renames a chat after its first answer)
  const tok = { mine: `aplan${n}`, devs: `dnote${n}`, team: `troad${n}` };
  const mine = await ask(A, `${tok.mine} says hello`);
  const devs = await ask(D, `${tok.devs} says hello`);
  const team = await ask(A, `${tok.team} says hello`, true);
  check(mine >= B && devs >= B && team > 0 && team < B, `ids say the home: the admin's ${mine}, dev1's ${devs} (from 2^40), the shared ${team} (below)`);
  const plain = await call(A.page, '/ask', { method: 'POST', body: { text: 'no share' }, global: true });
  check(plain.status === 409, `a person's unshared ask at the global instance is refused (${plain.status})`);

  // the lists: Mine merges both homes; dev1 never sees the admin's own
  for (const x of [A, D]) { await x.page.reload(); await x.page.waitForSelector('#runs .run', { timeout: 30000 }); }
  await until(A.page, ([a, b]) => document.querySelector(`#runs .run[data-id="${a}"]`) && document.querySelector(`#runs .run[data-id="${b}"]`), [mine, team], 30000)
    .catch(() => {});
  const aRows = await rows(A.page);
  check(aRows.some((r) => r.id === mine) && aRows.some((r) => r.id === team), `the admin's Mine: their own and the shared one (${JSON.stringify(aRows.slice(0, 6))})`);
  await shot(A.page, 'agent-homes-mine');
  await until(D.page, (t) => document.getElementById('runs').textContent.includes(t), tok.devs, 20000).catch(() => {});
  const dRows = await rows(D.page);
  check(!dRows.some((r) => r.t.includes(tok.mine)) && dRows.some((r) => r.t.includes(tok.devs)), `dev1's Mine: their own, never the admin's (${JSON.stringify(dRows.slice(0, 6))})`);
  await D.page.click('#views .seg:has-text("Shared")');
  await until(D.page, (id) => document.querySelector(`#runs .run[data-id="${id}"]`), team, 20000).catch(() => {});
  check((await rows(D.page)).some((r) => r.id === team), 'dev1\'s Shared view: the shared one (the global instance\'s)');
  await shot(D.page, 'agent-homes-shared-view');

  // the router by id: #c=<shared> opens it at global; both watch it stream
  await D.page.evaluate((id) => { location.hash = 'c=' + id; }, team);
  await A.page.evaluate((id) => { location.hash = 'c=' + id; }, team);
  for (const x of [A, D]) await until(x.page, (t) => document.querySelector('#top .title')?.textContent.includes(t), tok.team, 20000);
  check(true, '#c=<a shared id> opens the shared conversation in both pages');
  await answered(D.page, 'Hello from the fake model.');
  await D.page.fill('#msg', 'paras 10');
  await D.page.press('#msg', 'Enter');
  const liveBoth = await Promise.all([A, D].map((x) => until(x.page, () => !!document.querySelector('#timeline .msg.assistant.live'), null, 60000).then(() => true, () => false)));
  const doneYet = await Promise.all([A, D].map((x) => x.page.evaluate(() => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Paragraph 10')))));
  check(liveBoth.every(Boolean) && !doneYet.some(Boolean), `both people see the shared run streaming at once (live ${liveBoth}, finished ${doneYet})`);
  await shot(A.page, 'agent-homes-live-admin');
  await shot(D.page, 'agent-homes-live-dev1');
  for (const x of [A, D]) await answered(x.page, 'Paragraph 10');
  check(true, 'both see the answer when it is done');

  // Share a copy: the admin's own conversation, published
  await A.page.evaluate((id) => { location.hash = 'c=' + id; }, mine);
  await until(A.page, (t) => document.querySelector('#top .title')?.textContent.includes(t), tok.mine, 20000);
  await A.page.waitForSelector('#top .sharepill');
  await A.page.click('#top .sharepill');
  await A.page.waitForSelector('#pubdlg #pub-go');
  await shot(A.page, 'agent-homes-publish');
  await A.page.click('#pubdlg input[name=pubvis] >> nth=0'); // the team, to read
  await A.page.click('#pubdlg #pub-go');
  const copyId = await until(A.page, (b) => { const m = /#c=(\d+)/.exec(location.hash); return m && +m[1] < b && +m[1]; }, B, 30000).then((h) => h.jsonValue(), () => 0);
  check(copyId > 0, `Share a copy: the copy (${copyId}) opens at the global instance`);
  const stillMine = await call(A.page, `/runs/${mine}`);
  check(stillMine.status === 200, 'the private original stays (kept by default)');
  await D.page.click('#views .seg:has-text("Shared")');
  await D.page.reload();
  await D.page.waitForSelector('#runs .run', { timeout: 30000 });
  await D.page.click('#views .seg:has-text("Shared")');
  await until(D.page, (id) => document.querySelector(`#runs .run[data-id="${id}"]`), copyId, 20000).catch(() => {});
  check((await rows(D.page)).some((r) => r.id === copyId), 'dev1 lists the published copy in Shared');

  // Copy to my own space: dev1 takes a private copy of the shared chat
  await D.page.evaluate((id) => { location.hash = 'c=' + id; }, team);
  await D.page.waitForSelector('#top .sharepill');
  await D.page.click('#top .sharepill');
  await D.page.waitForSelector('#sharedlg #sh-copy');
  await D.page.click('#sharedlg #sh-copy');
  const own = await until(D.page, (b) => { const m = /#c=(\d+)/.exec(location.hash); return m && +m[1] >= b && +m[1]; }, B, 30000).then((h) => h.jsonValue(), () => 0);
  check(own >= B, `Copy to my own space: a private copy (${own}) opens in dev1's partition`);
  await until(D.page, () => [...document.querySelectorAll('#timeline .msg')].some((e) => e.textContent.includes('Paragraph 10')), null, 20000).catch(() => {});
  check(await D.page.evaluate(() => [...document.querySelectorAll('#timeline .msg')].some((e) => e.textContent.includes('Paragraph 10'))), 'the copy carries the shared transcript');

  // a copy names its writers only for who made it: dev1 shares their copy
  // back — the admin's message in it is "copied · admin", dev1's is dev1's
  const back = await call(D.page, `/runs/${own}/publish`, { method: 'POST', body: { share: { visibility: 'team', teamRole: 'viewer' }, keep: true } });
  const backId = back.body?.run?.id || 0;
  await A.page.evaluate((id) => { location.hash = 'c=' + id; }, backId);
  const whos = await until(A.page, () => { const w = [...document.querySelectorAll('#timeline .msg.user .who')].map((e) => e.textContent.trim()); return w.length >= 2 && w; }, null, 20000)
    .then((h) => h.jsonValue(), () => []);
  check(back.status === 200 && whos.includes('copied · admin') && whos.includes('dev1') && !whos.includes('admin'),
    `a copy dev1 shares back: the admin's message reads "copied · admin", dev1's dev1 (${back.status}, ${JSON.stringify(whos)})`);
  await shot(A.page, 'agent-homes-copied');

  // the global-viewer state: the owner token's page is the global instance's
  // (an acceptance item: no owner token, or a page not opening as global, fails)
  let token = '';
  try { token = fs.readFileSync(path.join(process.env.WS || '', '.xbin', 'token'), 'utf8').trim(); } catch { /* no workspace here */ }
  check(!!token, 'the global-viewer state: the owner token ($WS/.xbin/token) is there');
  if (token) {
    const ctx = await browser.newContext({ viewport: { width: 1300, height: 900 }, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
    const page = await ctx.newPage();
    let viaGlobal = 0; // the global instance's own page never addresses its global instance
    page.on('request', (r) => { if (r.url().includes('xbin-partition=')) viaGlobal++; });
    await page.goto(`${URL}/c/apps/agent/`);
    const ok = await page.waitForSelector('#partnote .pn.global', { timeout: 30000 }).then(() => true, () => false);
    check(ok, `the global-viewer state: the owner token's page opens as the global instance (partition ${await page.evaluate(() => globalThis.xbin?.partition || '').catch(() => '?')})`);
    if (ok) {
      await until(page, (id) => document.querySelector(`#runs .run[data-id="${id}"]`), team, 20000).catch(() => {});
      const gRows = await rows(page);
      check(gRows.some((r) => r.id === team) && !gRows.some((r) => r.id >= B), `the global-viewer: the global instance's list only (${JSON.stringify(gRows.slice(0, 5))})`);
      check(viaGlobal === 0, `the global-viewer: one home — no call asks for a partition (${viaGlobal})`);
      await shot(page, 'agent-homes-global-viewer');
    }
    await ctx.close();
  }
  check(!A.errors.length && !D.errors.length, `no page errors (${[...A.errors, ...D.errors].join(' | ')})`);
  await A.ctx.close();
  await D.ctx.close();
  await sleep(10);
  done();
}

module.exports = { agentHomes };
