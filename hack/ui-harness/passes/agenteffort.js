// hack/ui-harness/passes/agenteffort.js — D249 end to end: an agent
// template instance of its own (apps/effort) on xbind's LLM connector, whose
// one upstream is hack/fakeopenai. Its "fake-think" is a thinking model as
// DeepSeek's API runs one: its own model list describes its effort (low ·
// high · max, default high), every answer reasons, each says the effort it
// was asked for ("[effort max] …"), and a request that offers tools while an
// earlier answer lacks its reasoning_content is refused (400) — so a turn
// with a tool call finishes only when the agent sends its reasoning back.
// "fake-chat" an admin describes (OpenAI-style: low · medium · high);
// "gpt-5-fake" nobody does.
//   0. /xbin/llm: the provider's "reasoning…" — the line per model, and
//      "describe…" ticking the levels gpt-5-fake takes (then forgotten);
//   1. the composer's chip beside the model: "Default · High" for fake-think;
//   2. a level picked at home (Max) goes with the new chat — and only it:
//      the next new chat is at Default again — and arrives as asked; a tool
//      call's turn finishes (its reasoning went back); the thinking fold
//      says its reasoning tokens;
//   3. the keyboard in the popover: ← moves a notch (PATCH /runs/{id}
//      {effort}), 0 is Default;
//   4. a model switch keeps the level: on fake-chat Max becomes High — the
//      chip says so, the transcript has the line, the model got high;
//   5. an undescribed model: no chip;
//   6. a phone's width: the chip and its popover inside 390 px.
// The upstream and the instance are removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, sleep, shot, checker, THEME, noGocryptfs } = require('../lib');

const WS = process.env.WS || '';
const FAKE = `http://${process.env.FAKEOPENAI_ADDR || '127.0.0.1:18977'}`;
const UP = 'think', TILE = 'apps/effort';
const THINK = `${UP}/fake-think`, CHAT = `${UP}/fake-chat`, UNDESCRIBED = `${UP}/gpt-5-fake`;

const until = (page, fn, arg, timeout = 30000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 100 });
const chipText = (page) => page.evaluate(() => { const b = document.querySelector('#echip:not([hidden]) .echipt'); return b ? b.textContent.trim() : null; });
const answered = (page, t, timeout = 60000) => until(page, (t) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes(t)), t, timeout);

async function agentEffort(browser) {
  const { check, skip, done } = checker(`agent-effort-${THEME}`);
  if (noGocryptfs()) { skip(`a new agent instance is held: ${noGocryptfs()}`); return done(); }
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const api = (method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }), failOnStatusCode: false,
  });
  const tile = (method, p, data) => ctx.request.fetch(`${URL}/api/${TILE}${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }), failOnStatusCode: false,
  });
  try {
    // the connector: one upstream on the fake, fake-chat described by an admin
    let r = await api('PUT', `/llm/upstreams/${UP}`, { baseURL: `${FAKE}/v1`, key: '', reasoning: { 'fake-chat': { preset: 'openai' } } });
    check(r.ok(), `the upstream (${r.status()} ${(await r.text()).slice(0, 160)})`);
    const models = await (await api('GET', '/llm/v1/models')).json();
    const effortOf = (id) => (models.data || []).find((m) => m.id === id)?.effort;
    check(effortOf(THINK)?.source === 'upstream' && effortOf(CHAT)?.source === 'admin' && !effortOf(UNDESCRIBED),
      `the model list: fake-think from its upstream, fake-chat from the admin, gpt-5-fake none (${JSON.stringify([effortOf(THINK), effortOf(CHAT), effortOf(UNDESCRIBED)])})`);

    // 0. /xbin/llm: the provider's reasoning… — a line per model, and describe… (tick the levels a model takes)
    await page.goto(`${URL}/xbin/llm`);
    const row = `bx-llm-upstreams tr[data-upstream="${UP}"]`;
    await page.waitForSelector(`${row} button[data-reasoning]`, { timeout: 30000 });
    await page.click(`${row} button[data-reasoning]`);
    await page.waitForSelector(`bx-llm-upstreams [data-reason="${UP}"] [data-reason-line="fake-think"]`, { timeout: 20000 });
    const lineOf = (m) => page.textContent(`bx-llm-upstreams [data-reason-line="${m}"]`).then((t) => t.trim());
    check(await lineOf('fake-think') === 'Reasoning: Low · High · Max (default High) · from the upstream'
      && await lineOf('gpt-5-fake') === 'Reasoning: not described — no control is offered'
      && /^Reasoning: Low · Medium · High \(default Medium; always thinks\) · from the admin$/.test(await lineOf('fake-chat')),
    `the lines per model (${await lineOf('fake-think')} | ${await lineOf('gpt-5-fake')} | ${await lineOf('fake-chat')})`);
    await page.click('bx-llm-upstreams button[data-describe-open="gpt-5-fake"]');
    for (const w of ['medium', 'high', 'xhigh']) await page.check(`bx-llm-upstreams [data-describe="gpt-5-fake"] input[data-level="${w}"]`);
    await page.check('bx-llm-upstreams [data-describe="gpt-5-fake"] input[data-off]');
    check((await page.textContent('bx-llm-upstreams [data-describe-words]')).trim() === 'Reasoning: Medium · High · Extra high (can be off) · from the admin',
      'the form says what it will save');
    await shot(page, `agent-effort-describe-${THEME}`, { fullPage: false });
    await page.click('bx-llm-upstreams button[data-describe-save]');
    // (the element is inside the page's own shadow root: Playwright's selectors pierce it, document.querySelector doesn't)
    const lineIs = async (m, re) => { for (const t0 = Date.now(); Date.now() - t0 < 15000; await sleep(100)) if (re.test(await lineOf(m).catch(() => ''))) return true; return false; };
    check(await lineIs('gpt-5-fake', /Medium · High · Extra high \(can be off\) · from the admin/), `saved: the line follows (${await lineOf('gpt-5-fake')})`);
    const described = (await (await api('GET', '/llm/v1/models')).json()).data.find((m) => m.id === UNDESCRIBED)?.effort;
    check(JSON.stringify(described?.supported_levels) === '["medium","high","xhigh"]' && described?.can_disable === true && described?.source === 'admin',
      `described: on the model list (${JSON.stringify(described)})`);
    await page.click('bx-llm-upstreams button[data-forget="gpt-5-fake"]');
    check(await lineIs('gpt-5-fake', /not described/), 'forget: not described again');

    // an agent instance of its own, on the connector
    r = await api('POST', '/templates/new', { source: 'agent', path: TILE, partition: false });
    check(r.ok(), `${TILE} made (${r.status()})`);
    r = await api('POST', '/bindings', { component: TILE, slot: 'llm', providers: ['xbin:llm'] });
    check(r.ok(), `its llm slot bound to xbin:llm (${r.status()})`);
    let cfg = null;
    for (const t0 = Date.now(); Date.now() - t0 < 360000; await sleep(1000)) {
      const c = await tile('GET', '/config').catch(() => null);
      if (c && c.ok()) { cfg = await c.json().catch(() => null); if (cfg && 'system' in cfg) break; }
    }
    check(!!cfg, `${TILE}'s backend answers`);
    if (!cfg) return;
    r = await tile('PUT', '/config', { ...cfg, model: THINK, subagents: false });
    check(r.ok(), `its default model is ${THINK} (${r.status()})`);

    await page.goto(`${URL}/c/${TILE}/`);
    await page.waitForSelector('#msg', { timeout: 60000 });

    // 1. the chip
    await until(page, () => !!document.querySelector('#echip:not([hidden])'), null, 60000);
    check((await chipText(page)) === 'Default · High', `the chip beside the model: "${await chipText(page)}"`);
    await shot(page, `agent-effort-chip-${THEME}`, { fullPage: false });

    // 2. a level for the new chat; a tool call's turn finishes; the fold's tokens
    await page.click('#echip button');
    await until(page, () => !!document.querySelector('#epop:not([hidden]) .enotch'));
    const notches = await page.$$eval('#epop .enotch', (els) => els.map((e) => e.textContent.trim()));
    check(JSON.stringify(notches) === JSON.stringify(['Low', 'High', 'Max']) && (await page.textContent('#epop .edef')).trim() === 'Default · High',
      `the track: the model's own levels, Default named after its own (${JSON.stringify(notches)})`);
    await shot(page, `agent-effort-popover-${THEME}`, { fullPage: false });
    await page.click('#epop .enotch[data-notch="max"]');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'Max');
    await page.keyboard.press('Escape');
    await page.fill('#msg', 'use a tool');
    await page.press('#msg', 'Enter');
    await answered(page, 'Noted it.', 90000);
    const said = await page.$$eval('#timeline .msg.assistant:not(.live)', (els) => els.map((e) => e.textContent.trim()));
    check(said.some((t) => t.includes('[effort max] Noted it.')),
      `the tool call's turn finished — its reasoning went back (the fake refuses a follow-up without it) — at Max (${JSON.stringify(said).slice(0, 200)})`);
    const fold = await page.$$eval('.think summary', (els) => els.map((e) => e.textContent.trim()));
    check(fold.some((t) => /^Thought for \d+s · \d+ tokens$/.test(t)), `the thinking fold says its tokens (${JSON.stringify(fold)})`);
    check((await chipText(page)) === 'Max', `the conversation's chip: "${await chipText(page)}"`);
    await page.click('#home');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'Default · High').then(() => true, () => false);
    check((await chipText(page)) === 'Default · High', `a new chat starts at Default again (${await chipText(page)})`);
    await page.click('#runs .run');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'Max');

    // 3. the keyboard: ← a notch down, 0 Default
    await page.click('#echip button');
    await until(page, () => document.activeElement?.classList.contains('enotch'));
    await page.keyboard.press('ArrowLeft');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'High');
    check(true, '← moved a notch down (High)');
    await page.keyboard.press('0');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'Default · High');
    check(true, '0 is Default');
    await page.keyboard.press('ArrowRight'); // from Default: the model's default notch
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'High');
    await page.keyboard.press('End');
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'Max');
    await page.keyboard.press('Enter');
    await until(page, () => document.querySelector('#epop').hidden);
    check(true, '→ from Default lands on the model\'s default, End on its most thorough; Enter closes');

    // 4. a model switch keeps the level: fake-chat has no Max
    await page.selectOption('#msel', CHAT);
    await until(page, () => document.querySelector('#echip .echipt')?.textContent.trim() === 'High (Max isn\'t offered)');
    check(true, 'on fake-chat the chip says Max became High');
    await page.fill('#msg', 'quick one');
    await page.press('#msg', 'Enter');
    await answered(page, 'Quick answer.');
    const line = await until(page, () => [...document.querySelectorAll('#timeline *')].map((e) => e.textContent).find((t) => /model → fake-chat · effort Max → High/.test(t)), null, 20000)
      .then((h) => h.jsonValue(), () => '');
    check(/model → fake-chat · effort Max → High \(its highest at or below\)/.test(line || ''), `the transcript's line (${String(line).slice(0, 120)})`);
    const reqs = await (await fetch(`${FAKE}/debug/requests`)).json();
    check(reqs.some((q) => q.model === 'fake-chat' && q.purpose === 'turn'), 'fake-chat answered the turn');
    await shot(page, `agent-effort-switch-${THEME}`, { fullPage: false });

    // 5. an undescribed model: no chip
    await page.selectOption('#msel', UNDESCRIBED);
    await until(page, () => !!document.querySelector('#echip[hidden]'));
    check(true, 'gpt-5-fake, which nobody describes: no chip');
    await page.selectOption('#msel', THINK);
    await until(page, () => !!document.querySelector('#echip:not([hidden])'));

    // 6. a phone's width
    await page.setViewportSize({ width: 390, height: 844 });
    await sleep(300);
    await page.click('#echip button');
    await until(page, () => !!document.querySelector('#epop:not([hidden]) .enotch'));
    const fit = await page.evaluate(() => {
      const p = document.querySelector('#epop').getBoundingClientRect(), c = document.querySelector('#echip').getBoundingClientRect();
      return { pop: [p.left, p.right], chip: [c.left, c.right], scroll: document.documentElement.scrollWidth, width: innerWidth };
    });
    check(fit.pop[0] >= 0 && fit.pop[1] <= fit.width && fit.chip[1] <= fit.width && fit.scroll <= fit.width + 1,
      `at 390 px the chip and its popover are inside the page (${JSON.stringify(fit)})`);
    await shot(page, `agent-effort-phone-${THEME}`, { fullPage: false });
    await page.keyboard.press('Escape');
    check(errors.length === 0, `no page errors (${errors.slice(0, 3).join(' | ')})`);
  } finally {
    await api('DELETE', `/llm/upstreams/${UP}`);
    if (WS && fs.existsSync(path.join(WS, TILE))) fs.rmSync(path.join(WS, TILE), { recursive: true, force: true });
    await closeCtx(ctx, page);
  }
  done();
}

module.exports = { agentEffort };
