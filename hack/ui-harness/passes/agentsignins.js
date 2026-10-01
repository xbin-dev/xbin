// hack/ui-harness/passes/agentsignins.js — a person's saved sign-ins for
// coding agents in the agent template (D179; its API.md "Saved sign-ins"),
// through ⚙ → Coding agents → Coding-agent sign-ins, on the real apps/agent:
//   - unpartitioned (the default seed): none, and the section says why
//     (each sandbox signs in on its own there);
//   - partitioned (HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1: the admin's
//     page is their own partition): an Anthropic API key pasted as "Work"
//     (the first: Claude Code's default), a setup-token pasted as
//     "Personal" — each said for what it is, its secret in no page, no
//     input and no answer — Make default, Rename (a prompt), Forget (a
//     confirm); the global instance refuses the route (409: the shared
//     space holds no one's credentials). It forgets what it saved.
// The guided sign-in, the switch and the gate run on the fake coding agent,
// a host process: the agentHarness pass (unisolated) and the agent
// backend's tests (harness_signin_test.go); test/isolated's partition suite
// checks the vault and the injection in a real partition.
// Screenshots: out/agent-signins-*.png.
const { login, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
// call: this page's own backend through its frame's xbin.fetch — at the global instance with global
const call = (page, p, { method = 'GET', body, global } = {}) => page.evaluate(async ([p, method, body, global]) => {
  const r = await xbin.fetch(`/api/${xbin.self}${p}`, { method, ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
    ...(global ? { partition: 'global' } : {}) });
  return { status: r.status, body: await r.text() };
}, [p, method, body ?? null, !!global]);

const CLAUDE = '#hsignins .hsgroup[data-harness="claude"]';
const rowsOf = (page) => page.$$eval(`${CLAUDE} .hsrow`, (els) => els.map((e) => ({
  id: e.dataset.id, name: e.querySelector('.nm').textContent.trim(), def: !!e.querySelector('.badge'),
  what: e.querySelector('.muted')?.textContent.trim() || '', st: e.querySelector('.st')?.textContent.trim() || '' })));

async function agentSignins(browser) {
  const { check, skip, done } = checker('agent-signins');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const stamp = Date.now().toString(36);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  let promptAnswer = '';
  page.on('dialog', (d) => { (d.type() === 'prompt' ? d.accept(promptAnswer) : d.accept()).catch(() => {}); });
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 60000 });
  const part = await page.evaluate(() => xbin.partition || '');
  await page.waitForSelector('#gear:not([hidden])', { timeout: 30000 });
  await page.click('#gear');
  await page.click('#tabs .tab[data-tab="harnesses"]');
  await page.waitForSelector('#hsignins h4', { timeout: 30000 });
  await until(page, () => !/loading…/.test(document.getElementById('hsignins')?.textContent || ''), null, 30000);

  if (!process.env.HARNESS_AGENT_PARTITION) {
    const why = (await page.textContent('#hsignins')).replace(/\s+/g, ' ').trim();
    check(!part && /need a partitioned agent/.test(why) && /sign each sandbox in on its own/.test(why),
      `unpartitioned: no saved sign-ins, and the section says why (${why.slice(0, 160)})`);
    const r = await call(page, '/prefs/harness-signins', { method: 'POST', body: { harness: 'claude', secret: 'sk-ant-api03-x' } });
    check(r.status === 409, `…and a paste is refused (${r.status} ${r.body.slice(0, 120)})`);
    await shot(page, 'agent-signins-unpartitioned', { fullPage: false });
    check(!errors.length, `no page errors (${errors.join(' | ')})`);
    await ctx.close();
    return done();
  }
  check(part.startsWith('user:'), `the admin's page is their own partition (xbin.partition ${JSON.stringify(part)})`);
  if (!part.startsWith('user:')) { await ctx.close(); return done(); }
  await page.waitForSelector(CLAUDE, { timeout: 20000 });
  const KEY = `sk-ant-api03-harness-${stamp}-keysecret`;
  const TOK = `sk-ant-oat01-harness-${stamp}-tokensecret`;
  const paste = async (name, secret) => {
    await page.fill(`${CLAUDE} .hsadd input[name=name]`, name);
    await page.fill(`${CLAUDE} .hsadd input[type=password]`, secret);
    await page.press(`${CLAUDE} .hsadd input[type=password]`, 'Enter');
    await until(page, ([sel, n]) => [...document.querySelectorAll(`${sel} .hsrow .nm`)].some((e) => e.textContent.trim() === n), [CLAUDE, name]);
  };
  await paste('Work', KEY);
  check(await page.$eval(`${CLAUDE} .hsadd input[type=password]`, (i) => i.value === ''), 'the secret\'s field is emptied as it is sent');
  await paste('Personal', TOK);
  let rows = await rowsOf(page);
  const work = rows.find((r) => r.name === 'Work'), personal = rows.find((r) => r.name === 'Personal');
  check(!!work && work.def && work.what === 'Anthropic API key' && !!personal && !personal.def && /subscription token/.test(personal.what),
    `Work (an API key) is Claude Code's default as its first, Personal (a setup-token) not (${JSON.stringify(rows)})`);
  await shot(page, 'agent-signins-two', { fullPage: false });
  // the secrets: in no page, no input, no answer
  const html = await page.evaluate(() => document.documentElement.outerHTML + [...document.querySelectorAll('input')].map((i) => i.value).join('\n'));
  const listed = await call(page, '/prefs/harness-signins');
  check(listed.status === 200 && !listed.body.includes(KEY) && !listed.body.includes(TOK) && !html.includes(KEY) && !html.includes(TOK),
    'the secrets are nowhere: not in the page, its inputs or GET /prefs/harness-signins');
  // Make default, Rename, Forget
  await page.click(`${CLAUDE} .hsrow[data-id="${personal.id}"] [data-act="default"]`);
  await until(page, ([sel, id]) => !!document.querySelector(`${sel} .hsrow[data-id="${id}"] .badge`), [CLAUDE, personal.id]);
  rows = await rowsOf(page);
  check(rows.find((r) => r.id === personal.id)?.def && !rows.find((r) => r.id === work.id)?.def, `Make default moves the default to Personal (${JSON.stringify(rows.map((r) => [r.name, r.def]))})`);
  promptAnswer = 'Company';
  await page.click(`${CLAUDE} .hsrow[data-id="${work.id}"] [data-act="rename"]`);
  await until(page, ([sel, id]) => document.querySelector(`${sel} .hsrow[data-id="${id}"] .nm`)?.textContent.trim() === 'Company', [CLAUDE, work.id]);
  check(true, 'Rename: Work is Company now');
  await page.click(`${CLAUDE} .hsrow[data-id="${work.id}"] [data-act="forget"]`);
  await until(page, ([sel, id]) => !document.querySelector(`${sel} .hsrow[data-id="${id}"]`), [CLAUDE, work.id]);
  check(/Forgot Company/.test(await page.textContent('#hs-msg')), `Forget: gone, and said (${(await page.textContent('#hs-msg')).trim()})`);
  await shot(page, 'agent-signins-after', { fullPage: false });
  // the global instance has none
  const g = await call(page, '/prefs/harness-signins', { global: true });
  check(g.status === 409 && /shared space holds no one's credentials/.test(g.body), `the global instance refuses them (${g.status} ${g.body.slice(0, 120)})`);
  // put things back: forget what this pass saved
  for (const r of await rowsOf(page)) {
    if (r.name === 'Personal') {
      const f = await call(page, `/prefs/harness-signins/${r.id}`, { method: 'DELETE' });
      check(f.status === 200, `cleaned up ${r.name} (${f.status})`);
    }
  }
  check(!errors.length, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  done();
}

module.exports = { agentSignins };
