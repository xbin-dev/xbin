// hack/ui-harness/passes/agenttemplate.js — the builtin agent template, end
// to end: a real instance (apps/agent, seeded by seed.sh) talking through
// llm-gw to hack/fakeopenai, whose scripted model answers by keyword. It pins
// what the engine rewrite (D81) is for:
//   - thinking streams and folds, on BOTH wires (Chat Completions and the
//     Responses API that gpt-5/o-series models need);
//   - a tool call is a card headed by the model's summary;
//   - a subagent renders inside its parent's chat and never in the sidebar;
//   - a message sent mid-turn is queued, then delivered at the next step;
//   - a new chat answers at once while background subagents hold the model;
//   - a backend swap mid-call hands over at once — the successor re-issues
//     the call within ~1 s of the old process letting go (not the 30–90 s of
//     orphaned leases).
// The tile is driven as its own document (/c/apps/agent/), like the admin
// tile's passes. fakeopenai's GET /debug/requests is the model-side record.
const path = require('path');
const { login, fs, sleep, log, shot, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const FAKE = `http://${process.env.FAKEOPENAI_ADDR || '127.0.0.1:18977'}`;
const WS = process.env.WS;

let since = 0; // this run's requests only (a --shots rerun reuses fakeopenai)
const requests = async () => (await (await fetch(`${FAKE}/debug/requests`)).json()).filter((r) => r.start >= since);
const text = (page, sel) => page.$$eval(sel, (els) => els.map((e) => e.textContent.trim()));
const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
// in the page (addInitScript): does any element matching sel contain t?
const HAS = () => { window.__has = (sel, t) => [...document.querySelectorAll(sel)].some((e) => e.textContent.includes(t)); };

// ask: from home, a quick ask; returns once the chat shows it.
async function ask(page, q) {
  await page.click('#home');
  await page.fill('#msg', q);
  await page.press('#msg', 'Enter');
  await until(page, (q) => document.querySelector('#top .title')?.textContent.includes(q), q);
}
// newRun: a conversation through "New chat with options" (a title).
async function newRun(page, title, goal) {
  await page.click('#newopts');
  await page.fill('#n-title', title);
  await page.fill('#n-goal', goal);
  await page.click('#n-create');
  await until(page, (t) => document.querySelector('#top .title')?.textContent === t, title);
}
async function say(page, t) {
  await page.fill('#msg', t);
  await page.press('#msg', 'Enter');
}
const answered = (page, t, timeout) => until(page, ([t]) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), [t], timeout);
// setModel: the agent's default model, through its own config API (the
// frame's owner is admin of it).
const setModel = (page, model) => page.evaluate(async (model) => {
  const base = `/api/${location.pathname.split('/').slice(2, -1).join('/')}`;
  const c = await (await xbin.fetch(`${base}/config`)).json();
  c.model = model;
  const r = await xbin.fetch(`${base}/config`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(c) });
  if (!r.ok) throw new Error(`PUT config: ${r.status}`);
}, model);

async function agentTemplate(browser) {
  const { check, skip, done } = checker('agent-template');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/apps/')) log(`agentTemplate: ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.addInitScript(HAS);
  since = Date.now();
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  await setModel(page, 'fake/fake-chat');

  // 1. thinking streams, then folds; the answer is markdown.
  await ask(page, 'hello there');
  await until(page, () => window.__has('.think .th', 'Thinking…') || window.__has('.think .th', 'Thought'), null, 60000);
  await answered(page, 'Hello from the fake model.', 30000);
  const think = await text(page, '.think .th');
  check(think.some((t) => /^▸ Thought/.test(t)), `thinking folded to "Thought…" once the answer came (${JSON.stringify(think)})`);
  await page.click('.think .th');
  check((await page.textContent('.think .tb')).includes('Considering the greeting'), 'the folded thinking opens to its text');
  const named = await until(page, () => document.getElementById('runs').textContent.includes('Titled hello there'), null, 15000)
    .then(() => true, () => false);
  check(named, 'after its first answer the chat is named by the model (the sidebar shows it)');

  // 2. a tool call is a card headed by its summary
  await say(page, 'now use a tool');
  await answered(page, 'Noted it.');
  const heads = await text(page, '.tcard .hl');
  check(heads.includes('Jot down a quick note'), `the tool card is headed by the model's summary (${JSON.stringify(heads)})`);
  await page.click('.tcard .tch');
  await until(page, () => document.querySelector('.tcard.on .args'));
  const args = await text(page, '.tcard.on .args .k');
  check(!args.includes('summary') && args.includes('text'), `the summary is the headline, not an argument row (${JSON.stringify(args)})`);
  await shot(page, 'agent-template-chat', { fullPage: false });

  // 3. a subagent, inline and never in the sidebar
  await newRun(page, 'delegation', 'please delegate this');
  await answered(page, 'The helper counted.', 30000);
  await until(page, () => document.querySelector('.acard'));
  const card = await page.textContent('.acard');
  check(card.includes('counter'), `the subagent is a card in the parent's chat, titled by its label (${card.slice(0, 120)})`);
  if (!(await page.$('.acard.on'))) await page.click('.acard .ach');
  await until(page, () => window.__has('.acard .acb', 'one, two, three'));
  check(true, "the card opens to the subagent's own work and answer");
  const side = await text(page, '#runs .run .t');
  check(side.includes('delegation') && !side.some((t) => /counter|count to three/.test(t)), `the sidebar lists the run, never its subagent (${JSON.stringify(side)})`);
  await shot(page, 'agent-template-subagent', { fullPage: false });

  // 4. a message mid-turn is queued, then answered at the next step
  await say(page, 'a slow tool please');
  await until(page, () => document.querySelector('#stop') && !document.querySelector('#stop').hidden);
  await sleep(300);
  await say(page, 'steer: mention the weather');
  await until(page, () => window.__has('#queue .qchip', 'mention the weather'), null, 5000);
  check(true, 'a message sent while the agent works shows as a queued chip');
  await answered(page, 'Got your steer', 30000);
  check(!(await page.$('#queue .qchip')), 'the chip leaves once delivered');
  const order = await page.$$eval('#timeline > *', (els) => els.map((e) => e.classList.contains('tcard') ? 'tool:' + e.querySelector('.hl').textContent
    : e.classList.contains('user') ? 'user:' + e.textContent.trim() : '').filter(Boolean));
  const iNote = order.indexOf('tool:Take a slow note'), iSteer = order.findIndex((o) => o.startsWith('user:steer'));
  check(iNote >= 0 && iSteer > iNote, `the steer landed after the in-flight step, before the next model call (${JSON.stringify(order.slice(-4))})`);

  // 5. a new chat answers at once while background subagents hold the model
  await newRun(page, 'fan out', 'fan out three helpers');
  await answered(page, 'Started three helpers.', 30000);
  await until(page, () => document.querySelectorAll('.acard.running').length === 3 || document.querySelectorAll('.acard').length === 3);
  const t0 = Date.now();
  await ask(page, 'a quick one');
  await answered(page, 'Quick answer.', 15000);
  const quickMs = Date.now() - t0;
  const busyKids = (await requests()).filter((r) => r.sub && /slow job/.test(r.last) && (!r.end || r.end > t0)).length;
  check(quickMs < 4000 && busyKids === 3, `a new chat answered in ${quickMs} ms while ${busyKids} subagents held the model`);
  await page.click('#runs .run:has-text("fan out")');
  await until(page, () => document.querySelector('#top .title')?.textContent === 'fan out');
  await until(page, () => document.querySelector('.notice'), null, 20000);
  check((await page.$$('.notice')).length === 1, 'background answers arrive in the chat as one notice');
  await shot(page, 'agent-template-fanout', { fullPage: false });
  const side2 = await text(page, '#runs .run .t');
  check(!side2.some((t) => /helper|slow job/.test(t)), `background subagents stay out of the sidebar too (${JSON.stringify(side2)})`);

  // 6. the Responses wire: reasoning summaries stream as thinking
  await setModel(page, 'fake/gpt-5-fake');
  await ask(page, 'hello responses');
  await answered(page, 'Hello from the fake model.', 30000);
  check((await text(page, '.think .th')).some((t) => /Thought/.test(t)), 'the Responses wire streams its reasoning summary as thinking');
  const resp = (await requests()).filter((r) => r.wire === 'responses');
  check(resp.length > 0 && resp.every((r) => r.model === 'gpt-5-fake'), `gpt-5* models go over /v1/responses (${resp.length} request(s))`);
  await setModel(page, 'fake/fake-chat');

  // 7. a save mid-call: the successor re-issues the call at once
  if (WS) {
    // unique text: fakeopenai hangs only the FIRST request it sees for it
    const goal = `restart me please (${Date.now()})`;
    await newRun(page, 'restart', goal);
    const mine = (r) => r.last === goal;
    const inflight = () => requests().then((rs) => rs.find((r) => mine(r) && !r.end));
    for (let i = 0; i < 100 && !(await inflight()); i++) await sleep(100);
    check(!!(await inflight()), 'the slow model call is in flight');
    const bump = path.join(WS, 'apps/agent/_backend/zz_harness.go');
    fs.writeFileSync(bump, `package main\n\n// harness: a save that swaps the backend (${Date.now()})\n`);
    try {
      await answered(page, 'Noted it.', 180000);
      const rs = (await requests()).filter(mine);
      const cut = rs.find((r) => r.canceled), again = rs.find((r) => cut && r.start >= cut.end && !r.canceled);
      const gap = cut && again ? again.start - cut.end : -1;
      log(`restart: ${rs.length} request(s), gap ${gap} ms`);
      check(gap >= 0 && gap < 1500, `a swap mid-call: the new backend re-issued the call ${gap} ms after the old one let go`);
      const notes = (await text(page, '.tcard .hl')).filter((h) => h === 'Survive a restart');
      check(notes.length === 1, `the call ran once across the swap (${notes.length} card(s))`);
    } finally {
      fs.rmSync(bump, { force: true });
    }
  }

  check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  done();
}

module.exports = { agentTemplate };
