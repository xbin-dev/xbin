// harness-homes.mjs — coding agents in a partitioned agent, as the web page
// shows them (model/harness-homes.js; API.md "Coding agents" → "In a
// partitioned instance (the UI)"), over the harness fixtures
// (test/harness-fixtures.mjs: runs 21–28, all below 2^40 — the shared space's):
//
//   - the global instance's own page (the owner token): "Who answers" isn't
//     shown, the new-chat dialog has no #n-agent, no setup card; a coding
//     agent's run there is read, not driven (the composer off, saying why;
//     no Retry; its mode and options shown, not switched), and its sign-in
//     card is read-only, saying why;
//   - a person's partition ("user:alice"): a shared conversation's coding
//     agent (at the global instance) the same — its sign-in card read-only
//     (no methods, no Retry), the composer off, no Retry in the top bar.
//
// test/homes.mjs drives a coding agent's rows, share dialog and the
// new-chat dialog in a person's partition.
//
//   node test/harness-homes.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const SIGNIN_GLOBAL = /this shared instance holds no one's credentials/;
const SIGNIN_SHARED = /This conversation is in the shared space: a coding agent signs in only in your own conversations/;
const BARRED = 'a coding agent doesn\'t run in the shared space — start one in your own conversations';

async function open(partition) {
  const seed = harnessSeed();
  // 21 cut off (its adapter lost): Retry would resume it, where it may still run
  Object.assign(seed.views[21].run, { status: 'error', harness: { ...seed.views[21].run.harness, state: 'lost', error: 'cut off' } });
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 760 } });
  await serveTile(ctx);
  await ctx.addInitScript(STUB, seed);
  await ctx.addInitScript((p) => { window.xbin.partition = p; }, partition);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('dialog', (d) => d.accept());
  await page.goto(`${ORIGIN}/`);
  await page.waitForSelector('#runs .run[data-id="21"]');
  return { ctx, page, errors };
}
const go = async (page, id, sel) => { await page.evaluate((h) => { location.hash = h; }, '#c=' + id); await page.waitForSelector(sel); };
const text = (page, sel) => page.textContent(sel).then((t) => (t || '').replace(/\s+/g, ' ').trim());
const retryShown = (page) => page.$('#top button[title="Drive the run again"]').then((b) => !!b);

// read, not driven: the sign-in card of 24 (a Codex login), and 21 (cut off) — as either page shows them
async function readOnly(page, label, signin) {
  await go(page, 24, '#hlogin');
  ok(`${label}: the sign-in card is read-only, saying why`, signin.test(await text(page, '#hl-view')), await text(page, '#hlogin'));
  ok(`${label}: …no methods, no Retry`, !(await page.$('#hlogin [data-method]')) && !(await page.$('#hl-retry')));
  ok(`${label}: …the composer off, saying why`, (await page.$eval('#msg', (e) => e.disabled)) && (await page.getAttribute('#msg', 'placeholder')) === BARRED,
    await page.getAttribute('#msg', 'placeholder'));
  await go(page, 21, '#hctl:not([hidden]) .hctlb');
  ok(`${label}: a cut-off coding agent there offers no Retry`, !(await retryShown(page)));
  ok(`${label}: …its composer is off`, await page.$eval('#msg', (e) => e.disabled));
  await page.click('#hctl .hctlb');
  await page.waitForSelector('#hctl-pop:not([hidden]) [data-mode]');
  const modes = await page.$$eval('#hctl-pop [data-mode] input', (els) => els.map((e) => e.disabled));
  const sels = await page.$$eval('#hctl-pop select.hsel', (els) => els.map((e) => e.disabled));
  ok(`${label}: …its mode and options are shown, not switched`, modes.length && modes.every(Boolean) && sels.length && sels.every(Boolean), JSON.stringify({ modes, sels }));
  await page.click('#hctl .hctlb');
}

// --- the global instance's own page --------------------------------------------------------------
{
  const { ctx, page, errors } = await open('global');
  await page.waitForSelector('#msg');
  ok('global: no "Who answers" at home', await page.$eval('#apick', (e) => e.hidden));
  ok('global: no setup card', !(await page.$('#hsetup')));
  await page.click('#newopts');
  await page.waitForSelector('#newdlg[open] #n-goal');
  ok('global: the new-chat dialog has no "Who answers" and no sandbox of a coding agent', !(await page.$('#newdlg #n-agent')) && !(await page.$('#newdlg #n-sandbox')));
  await page.click('#newdlg .dlg-ft button[value="cancel"]');
  await readOnly(page, 'global', SIGNIN_GLOBAL);
  ok('global: no page errors', !errors.length, errors.join(' | '));
  await ctx.close();
}

// --- a person's partition: a shared conversation's coding agent -----------------------------------
{
  const { ctx, page, errors } = await open('user:alice');
  await readOnly(page, 'her partition, a shared one', SIGNIN_SHARED);
  const calls = await page.evaluate(() => window.__calls.filter((c) => /\/runs\/(21|24)\//.test(c.url)).map((c) => c.home));
  ok('her partition: its calls go to global', calls.length > 0 && calls.every((h) => h === 'global'), JSON.stringify(calls));
  ok('her partition: no page errors', !errors.length, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('harness-homes');
