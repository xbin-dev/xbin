// hack/demo/stills.js — the demo film set's stills: the harness pass
// demoStills (hack/ui-harness/run.sh with HARNESS_SEED=demo runs it). It signs
// in as a regular person and as the admin persona (company.json personas),
// walks their seeded screens and saves 1440×900 frames at device scale 2 to
// $DEMO_STILLS (default $OUT/stills). Each still waits for the content it
// shows, so a frame is never of a loading tile.
const path = require('path');
const { login, closeCtx, sh, fr, waitFor, openShell, settle, sleep, log, fs, OUT, URL } = require('../ui-harness/lib');

const STILLS = process.env.DEMO_STILLS || path.join(OUT, 'stills');
const VIEW = { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 };
const company = JSON.parse(fs.readFileSync(path.join(__dirname, 'company.json'), 'utf8'));
// the set's password: random per set (hack/demo/lib.sh demo_password; run.sh
// exports it), else the file beside the harness's workspace
const PASS = process.env.DEMO_PASSWORD || (() => {
  try { return fs.readFileSync(`${process.env.WS}.password`, 'utf8').trim(); } catch { return ''; }
})();
const AGENT = company.agent.tile;

async function still(page, name) {
  fs.mkdirSync(STILLS, { recursive: true });
  await page.mouse.move(1430, 890); // no hover state in the frame
  await sleep(300);
  await page.screenshot({ path: path.join(STILLS, `${name}.png`) });
  log('still', name + '.png');
}

// tile: the Playwright Frame of a tile's document on the current screen
async function tile(page, p, timeout = 30000) {
  const end = Date.now() + timeout;
  for (;;) {
    const f = page.frames().find((x) => x.url().includes(`/c/${p}/`));
    if (f) return f;
    if (Date.now() > end) throw new Error(`no frame for ${p}`);
    await sleep(100);
  }
}
async function screen(page, id) {
  await sh(page, (t, id) => t.setScreen(id), id);
  await settle(page);
  await sleep(400);
}
const shown = (f, sel, timeout = 30000) => f.waitForSelector(sel, { timeout });
// openConversation: Merrow's conversation of that title, by its link (#c=<id>)
async function openConversation(agent, title) {
  await shown(agent, `text=${title}`);
  const id = await agent.evaluate(async (title) => {
    const d = await (await xbin.fetch(`/api/${xbin.self}/conversations`)).json();
    return [...(d.pinned || []), ...(d.items || [])].find((r) => r.title === title)?.id;
  }, title);
  if (!id) throw new Error(`no conversation "${title}"`);
  await agent.evaluate((id) => { location.hash = `c=${id}`; }, id);
}

const layouts = JSON.parse(fs.readFileSync(path.join(__dirname, 'data/layouts.json'), 'utf8'));
async function person(browser, who) {
  const { ctx, page } = await login(browser, who, PASS, VIEW);
  // their font as seeded (another pass, a phone still, may have left another)
  await page.context().request.put(`${URL}/api/xbin/prefs/settings`, { data: { fontSize: layouts.people[who]?.fontSize ?? 17 } });
  await openShell(page);
  await waitFor(page, (t) => t.screens.length > 1, null, { timeout: 15000, label: `${who}'s seeded screens` });
  return { ctx, page };
}

async function demoStills(browser) {
  if (process.env.HARNESS_SEED !== 'demo') { log('SKIP demoStills: the demo seed only (HARNESS_SEED=demo ./run.sh)'); return; }
  const P = company.personas.person, A = company.personas.admin;

  // ---- a regular person: Priya, customer success ----
  {
    const { ctx, page } = await person(browser, P);
    // Today: Merrow with the renewal-call prep, and the day's calendar
    await screen(page, 's-today');
    const agent = await tile(page, AGENT);
    await openConversation(agent, 'Brightwell renewal call prep');
    await shown(agent, 'text=Worth raising');
    // from the top of the answer (the question is pinned above it): its
    // thinking, the CRM and ops-report calls it made, then what it found —
    // scrolling the chat only, never the shell around it
    await agent.evaluate(() => {
      const card = document.querySelector('#timeline .tcard');
      const top = card?.previousElementSibling?.classList.contains('think') ? card.previousElementSibling : card;
      let box = card?.parentElement;
      while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
      if (top && box) box.scrollTop += top.getBoundingClientRect().top - box.getBoundingClientRect().top - 4;
    });
    await shown(await tile(page, 'apps/calendar'), '.ev');
    await sleep(800);
    await still(page, '01-person-today-agent');

    // Customers: the pipeline board, then an account
    await screen(page, 's-customers');
    const crm = await tile(page, 'apps/crm');
    await shown(crm, '.card');
    await sleep(500);
    await still(page, '02-person-crm-pipeline');
    await crm.locator('.card:has-text("Brightwell")').dispatchEvent('click');
    await shown(crm, 'aside .contact');
    await sleep(500);
    await still(page, '03-person-crm-account');

    // Onboarding: the tracker Merrow built, one checklist open
    await screen(page, 's-onboarding');
    const ob = await tile(page, 'apps/onboarding');
    await shown(ob, '.card');
    await ob.locator('.card:has-text("Riverbend") .toggle').dispatchEvent('click');
    await shown(ob, '.step');
    await sleep(400);
    await still(page, '04-person-onboarding');

    // Expenses: her own book (a partition of her own under --isolate)
    await screen(page, 's-expenses');
    await shown(await tile(page, 'apps/expenses'), '.it');
    await sleep(400);
    await still(page, '05-person-expenses');

    // Inbox: the threads, and the day's meetings beside them (read through
    // the inbox's backend: on a fresh set the first read waits for its start)
    await screen(page, 's-inbox');
    const mail = await tile(page, 'apps/email');
    await shown(mail, '.msg');
    await shown(mail, '.today b', 90000);
    await sleep(400);
    await still(page, '06-person-inbox');
    await sh(page, (t) => t.setScreen('s-today'));
    await closeCtx(ctx, page);
  }

  // ---- the admin persona: Tomás, CTO ----
  {
    const { ctx, page } = await person(browser, A);
    // Ops: the nightly report beside live metrics (sparklines need a few scrapes)
    await screen(page, 's-ops');
    await shown(await tile(page, 'apps/ops-report'), '.k');
    await shown(await tile(page, 'apps/metrics'), '.srow');
    await sleep(Number(process.env.DEMO_METRICS_WAIT || 20000));
    await still(page, '07-admin-ops');

    // Build: the tile Merrow built, with its history (Merrow's commits) open
    await screen(page, 's-build');
    await shown(await tile(page, 'apps/onboarding'), '.card');
    await fr(page, 'apps/onboarding', (f) => f.open('code'));
    await fr(page, 'apps/onboarding', (f) => f.setPop({ x: 360, y: 150, w: 720, h: 470 }));
    const code = page.locator('bx-frame[src="apps/onboarding"] bx-code');
    await code.locator('button:has-text("Changes")').dispatchEvent('click');
    // (the checklist's commit: its diff is the tile's own code, no platform
    // call in it)
    const commit = code.locator('.commit:has-text("Checklist per customer")');
    await commit.waitFor({ timeout: 20000 });
    // the code window's first load, still in flight when the commit is
    // picked this early, can put it back on the working tree (bx-code's
    // _load: !this._diff → _loadDiff('')): pick it again until its diff shows
    for (let i = 0; ; i++) {
      await commit.dispatchEvent('click');
      try { await code.locator('.diff:has-text("was due")').waitFor({ timeout: 4000 }); break; } catch (e) { if (i >= 4) throw e; }
    }
    await sleep(800);
    await still(page, '08-admin-agent-built-app');
    await fr(page, 'apps/onboarding', (f) => f.closeTerminal());

    // Merrow's automations: the schedules and the team chat channel
    await screen(page, 's-today');
    const agent = await tile(page, AGENT);
    await agent.evaluate(() => { location.hash = '#auto'; });
    await shown(agent, '.autos-page');
    await sleep(800);
    await still(page, '09-admin-agent-automations');
    await agent.evaluate(() => { location.hash = ''; });
    await closeCtx(ctx, page);
  }

  // ---- the sign-in page, branded ----
  {
    const ctx = await browser.newContext(VIEW);
    const page = await ctx.newPage();
    await page.goto(`${URL}/login`);
    await page.waitForSelector('.logo img.mark');
    await still(page, '10-sign-in');
    await ctx.close();
  }
}

module.exports = { demoStills, still, STILLS, VIEW, PASS, company };
