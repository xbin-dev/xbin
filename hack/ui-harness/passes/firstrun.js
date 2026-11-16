// hack/ui-harness/passes/firstrun.js — the first-run sheet (D236; plans/
// wsagent/journey.md G3, G5, G7), asserted on xbinds of its own booted as an
// install boots one (journey.js boot: no --dev, --isolate, the owner-token
// login), in the states the sheet has:
//
//   A  the installer's default — manual vault, the host's model: two vault
//      fields and "Create my account and turn on my workspace agent".
//      Keyboard-first (focus in the name, Enter moves on, the field that
//      isn't right says why in place: a weak password, a repeat that
//      doesn't match — and nothing is made), "Not now" (gone, the shell as
//      before, remembered in this browser only: another browser sees the
//      sheet), then the whole sheet: signed in as the account, the vault
//      unsealed, the agent on for them alone, the dock open with the
//      agent's composer focused; a second browser afterwards gets no sheet,
//      and the swap route refuses everyone outside the bootstrap state
//   B  the host unseals and there is no model: no vault field, the agent
//      needs a model (/xbin/llm), "Create my account" — and the dock stays
//      closed
//   C  the host's preset {"workspaceAgent": {"enabled": true, "who":
//      "admins"}}: on for the new admin with no click, the host's locks on
//      the API; the vault's setup fails (the route answered 500 by the
//      pass): the account is made, the person signed in, the sheet says
//      what is left
//   D  the host's "workspaceAgent": false: the sheet says the host turned
//      agents off; and a vault that is set up and sealed (its status and
//      the unseal answered by the pass): one field to unlock it, tried
//      first, a wrong passphrase said in place with nothing made
//
// A screenshot of each state at 1280 and 390 wide, dark and light, and at
// 200 % zoom (FIRSTRUN_SHOTS, default out/firstrun-shots). Passwords and
// passphrases are made per run and never logged; the login URL's token is
// redacted from every error. Needs HARNESS_ISOLATE=1 and `make sealed`
// (the dock's agent page), like the journey passes; it uses nothing of the
// seeded workspace, and boots on the journey's port ($PORT+3).
'use strict';
const path = require('path');
const crypto = require('crypto');
const { fs, OUT, log, checker } = require('../lib');
const J0 = require('./journey');

const SHOTS = process.env.FIRSTRUN_SHOTS || path.join(OUT, 'firstrun-shots');
const S = 'bx-first-run';
const secret = () => crypto.randomBytes(12).toString('base64url');
const redact = (e) => String(e && e.message || e).replace(/token=[0-9a-f]+/g, 'token=…');

async function newPage(browser, theme = 'dark') {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1, colorScheme: theme });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log('firstRun: PAGE ERROR', redact(e)));
  return { ctx, page };
}
async function open(page, X) {
  try { await page.goto(`${X.url}/login?token=${X.token}`); } catch (e) { throw new Error(`open the login URL: ${redact(e)}`); }
  await J0.shellUp(page);
}
const api = (page, p, method = 'GET', body) => page.evaluate(async ([p2, m, b]) => {
  const r = await fetch('/api/xbin' + p2, { method: m, headers: b ? { 'Content-Type': 'application/json' } : {}, body: b ? JSON.stringify(b) : undefined });
  return { status: r.status, body: await r.json().catch(() => null) };
}, [p, method, body]);
const bearer = async (X, p, method = 'GET', body) => {
  const r = await fetch(`${X.url}/api/xbin${p}`, { method, headers: { Authorization: `Bearer ${X.token}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  return { status: r.status, body: await r.json().catch(() => null) };
};
const sheetUp = (page) => page.waitForSelector(`${S} [data-state="form"] #bxfr-name`, { timeout: 30000 });
const activeId = (page) => page.evaluate(() => (document.activeElement && document.activeElement.id) || '');
const sheetApi = (page) => page.evaluate((s) => { const el = document.querySelector(s); return el && el.testApi ? el.testApi() : null; }, S);
// the deepest focused element's tag, through shadow roots
const deepFocus = (frameOrPage) => frameOrPage.evaluate(() => { let a = document.activeElement; while (a && a.shadowRoot && a.shadowRoot.activeElement) a = a.shadowRoot.activeElement; return { tag: a ? a.localName : '', has: document.hasFocus() }; });

/** shots(page, name): the state at 1280 and 390, dark and light, and at 200 % (a 640-wide viewport) — and whether any of them scrolls sideways. */
async function shots(page, name) {
  fs.mkdirSync(SHOTS, { recursive: true });
  const sideways = [];
  for (const [w, h, tag] of [[1280, 900, 'desktop'], [390, 844, 'phone'], [640, 450, 'zoom200']]) {
    await page.setViewportSize({ width: w, height: h });
    for (const theme of tag === 'zoom200' ? ['dark'] : ['dark', 'light']) {
      await page.emulateMedia({ colorScheme: theme });
      await page.waitForTimeout(150);
      await page.screenshot({ path: path.join(SHOTS, `${name}-${tag}-${theme}.png`) });
      const over = await page.evaluate((s) => { const el = document.querySelector(s); return !!el && el.scrollWidth > el.clientWidth + 1; }, S);
      if (over) sideways.push(`${tag}-${theme}`);
    }
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.emulateMedia({ colorScheme: 'dark' });
  return sideways;
}

async function stateA(browser, check) {
  const X = await J0.boot('firstrun-a');
  const P = { name: 'Ada Admin', password: secret(), vault: secret() };
  const ctxs = [];
  try {
    // ---- the sheet, as the login URL opens it ----
    let { ctx, page } = await newPage(browser);
    ctxs.push(ctx);
    await open(page, X);
    await sheetUp(page);
    const boot = await api(page, '/first-run');
    check(boot.status === 200 && boot.body.bootstrap === true, 'A: GET /first-run — the owner token\'s browser session, no accounts: bootstrap');
    check((await bearer(X, '/first-run')).body.bootstrap === false, 'A: bx\'s bearer is no browser: not the bootstrap state');
    const t = await sheetApi(page);
    check(t && t.plan.vault === 'new' && t.plan.agent.kind === 'offer', `A: the plan — two vault fields, the agent offered (${JSON.stringify(t && t.plan)})`);
    check(await page.locator(`${S} [data-primary]`).innerText() === 'Create my account and turn on my workspace agent', 'A: the primary button turns the agent on');
    check(await page.locator(`${S} [data-quiet]`).count() === 1, 'A: a quiet button makes the account without it');
    check(/asks you before it changes anything/.test(await page.locator(`${S} [data-agent]`).innerText()), 'A: one sentence says what the agent is and that it asks first');
    check(/cannot be recovered/.test(await page.locator(`${S} #bxfr-vault-hint`).innerText()) && /bxfr-vault-hint/.test(await page.getAttribute(`${S} #bxfr-vault`, 'aria-describedby')),
      'A: the vault\'s "cannot be recovered" line, over its two fields (and the passphrase\'s description)');
    check(/Admin → Users → sign-in security/.test(await page.locator(`${S} [data-token]`).innerText()), 'A: the sign-in link keeps working until it is turned off — said, with where');
    check(await activeId(page) === 'bxfr-name', 'A: focus starts in the name');
    check(await page.evaluate(() => document.querySelector('bx-shell').inert === true), 'A: the shell behind the sheet is inert');
    check(await page.evaluate(() => !document.querySelector('bx-shell').shadowRoot.querySelector('.alerts')), 'A: no first-run hint under the sheet (no setup card, no alert)');
    const ac = await page.evaluate((s) => [...document.querySelectorAll(`${s} input`)].map((i) => `${i.name}:${i.autocomplete}`).join(' '), S);
    check(/username:username/.test(ac) && /password:new-password/.test(ac) && /name:name/.test(ac) && /vault-passphrase:off/.test(ac), `A: a password manager is offered the new credential, not the vault's (${ac})`);
    check(await page.evaluate((s) => document.querySelector(`${s} #bxfr-vault`).form.id !== document.querySelector(`${s} #bxfr-pw`).form.id, S), 'A: the vault\'s fields are not the sign-up form\'s');
    const wide = await shots(page, 'a-sheet');
    check(wide.length === 0, `A: the sheet never scrolls sideways — 1280, 390, 200 % (${wide.join(' ') || 'none'})`);

    // ---- keyboard-first, and errors in place ----
    await page.keyboard.type(P.name);
    check((await sheetApi(page)).fields.id === 'ada', 'A: the name gives the sign-in name (ada)');
    await page.keyboard.press('Enter');
    check(await activeId(page) === 'bxfr-pw', 'A: Enter in the name moves on to the password');
    await page.keyboard.type('short');
    await page.keyboard.press('Enter');
    check(await page.locator(`${S} [data-err="password"]`).count() === 1 && await activeId(page) === 'bxfr-pw', 'A: a weak password — said under it, and the cursor stays');
    check(await page.getAttribute(`${S} #bxfr-pw`, 'aria-invalid') === 'true' && /bxfr-pw-err/.test(await page.getAttribute(`${S} #bxfr-pw`, 'aria-describedby')), 'A: the error is the field\'s (aria-invalid, aria-describedby)');
    await shots(page, 'a-weak-password');
    await page.fill(`${S} #bxfr-pw`, P.password);
    await page.press(`${S} #bxfr-pw`, 'Enter');
    check(await activeId(page) === 'bxfr-vault', 'A: Enter moves on to the vault\'s passphrase');
    await page.keyboard.type(P.vault);
    await page.keyboard.press('Enter');
    check(await activeId(page) === 'bxfr-repeat', 'A: …and to its repeat');
    await page.keyboard.type(`${P.vault}x`);
    await page.keyboard.press('Enter'); // the last field: the primary button
    await page.waitForSelector(`${S} [data-err="repeat"]`, { timeout: 5000 });
    check(/don't match/.test(await page.locator(`${S} [data-err="repeat"]`).innerText()), 'A: a repeat that doesn\'t match — said under it');
    check(((await bearer(X, '/users')).body.users || []).length === 0, 'A: …and nothing was made');
    await shots(page, 'a-repeat-mismatch');

    // ---- Not now ----
    await page.click(`${S} [data-skip]`);
    await page.waitForSelector(S, { state: 'detached', timeout: 5000 });
    check(await page.evaluate(() => document.querySelector('bx-shell').inert === false), 'A: "Not now" — the sheet is gone and the shell is the shell again');
    await page.waitForFunction(() => !!document.querySelector('bx-shell').shadowRoot.querySelector('.alerts .alert'), null, { timeout: 10000 }).catch(() => {});
    check(/Secure this workspace/.test(await page.evaluate(() => document.querySelector('bx-shell').shadowRoot.querySelector('.alerts')?.innerText || '')), 'A: …and everything is as before: the setup card');
    await page.reload();
    await J0.shellUp(page);
    await page.waitForTimeout(1500);
    check(await page.locator(S).count() === 0, 'A: skipping is remembered in this browser');
    await page.screenshot({ path: path.join(SHOTS, 'a-skipped-desktop-dark.png') });

    // ---- another browser: the sheet again, and the whole of it ----
    ({ ctx, page } = await newPage(browser));
    ctxs.push(ctx);
    await open(page, X);
    await sheetUp(page);
    check(true, 'A: another browser sees the sheet ("Not now" is that browser\'s)');
    await page.fill(`${S} #bxfr-name`, P.name);
    await page.fill(`${S} #bxfr-pw`, P.password);
    await page.fill(`${S} #bxfr-vault`, P.vault);
    await page.fill(`${S} #bxfr-repeat`, P.vault);
    const t0 = Date.now();
    await Promise.all([page.waitForNavigation({ timeout: 60000 }), page.click(`${S} [data-primary]`)]);
    await J0.shellUp(page);
    const who = await api(page, '/whoami');
    check(who.body.kind === 'user' && who.body.id === 'ada' && who.body.role === 'admin', `A: this browser is signed in as the new admin account (${who.body.kind} ${who.body.id})`);
    check((await bearer(X, '/vault-status')).body.mode === 'unsealed', 'A: the vault is set up and unsealed');
    const ws = await api(page, '/delegate/workspace');
    check(ws.body.enabled === true && ws.body.by === 'user:ada' && ws.body.who && ws.body.who.everyone === false && ws.body.who.entries === 1, `A: workspace agents on, by ada, for ada alone — never "everyone" (${JSON.stringify(ws.body.who)})`);
    await page.waitForSelector('bx-agent-dock[open]', { timeout: 30000 });
    const el = await page.waitForSelector('bx-agent-dock iframe', { state: 'attached', timeout: 60000 });
    const f = await el.contentFrame();
    await f.waitForSelector('.wsp-main[aria-busy="false"]', { timeout: 120000 });
    let focus = { tag: '' };
    for (let i = 0; i < 40 && focus.tag !== 'textarea'; i++) { focus = await deepFocus(f); if (focus.tag !== 'textarea') await page.waitForTimeout(250); }
    const top = await deepFocus(page);
    check(focus.tag === 'textarea' && focus.has && top.tag === 'iframe', `A: the dock opened by itself, the agent's composer focused (${focus.tag}, top ${top.tag}; ${Date.now() - t0} ms)`);
    check(await page.evaluate(() => !document.querySelector('bx-shell').shadowRoot.querySelector('.alerts .alert.warn')), 'A: after the sheet, no first-run hint stacks on top (no setup card, no vault alert)');
    await page.screenshot({ path: path.join(SHOTS, 'a-done-desktop-dark.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(SHOTS, 'a-done-phone-dark.png') });
    await page.setViewportSize({ width: 1280, height: 900 });

    // ---- afterwards: no sheet, and the swap refused outside the bootstrap state ----
    const late = await newPage(browser);
    ctxs.push(late.ctx);
    await open(late.page, X);
    await late.page.waitForTimeout(2500);
    check(await late.page.locator(S).count() === 0 && (await api(late.page, '/first-run')).body.bootstrap === false, 'A: a second browser, once an account exists: no sheet');
    const swap = { id: 'ada', password: P.password };
    check((await api(late.page, '/first-run/session', 'POST', swap)).status === 409, 'A: the swap, from the token\'s browser session once the account signed in: 409');
    check((await api(page, '/first-run/session', 'POST', swap)).status === 403, 'A: …from the account\'s own session: 403');
    check((await bearer(X, '/first-run/session', 'POST', swap)).status === 403, 'A: …with bx\'s bearer token: 403');
    check((await bearer(X, '/whoami')).body.kind === 'root', 'A: the owner token stays the bootstrap credential');
  } finally {
    for (const c of ctxs) await c.close().catch(() => {});
    await X.stop();
  }
}

async function stateB(browser, check) {
  const X = await J0.boot('firstrun-b', { llm: false, vault: secret() });
  const { ctx, page } = await newPage(browser);
  try {
    await open(page, X);
    await sheetUp(page);
    const t = await sheetApi(page);
    check(t.plan.vault === '' && await page.locator(`${S} #bxfr-vault`).count() === 0, 'B: the host unseals: no vault field');
    check(t.plan.agent.kind === 'no-model' && /needs a language model/.test(await page.locator(`${S} [data-agent]`).innerText()) && await page.getAttribute(`${S} [data-agent] a[data-link]`, 'href') === '/xbin/llm',
      'B: no model: the agent needs one, and the sheet links /xbin/llm');
    check(await page.locator(`${S} [data-primary]`).innerText() === 'Create my account' && await page.locator(`${S} [data-quiet]`).count() === 0, 'B: …and the button is just "Create my account"');
    const wide = await shots(page, 'b-host-vault-no-model');
    check(wide.length === 0, `B: never sideways (${wide.join(' ') || 'none'})`);
    await page.fill(`${S} #bxfr-name`, 'Bo Builder');
    await page.fill(`${S} #bxfr-pw`, secret());
    await Promise.all([page.waitForNavigation({ timeout: 60000 }), page.press(`${S} #bxfr-pw`, 'Enter')]);
    await J0.shellUp(page);
    const who = await api(page, '/whoami');
    check(who.body.id === 'bo' && who.body.role === 'admin', `B: Enter in the last field is the primary button: signed in as bo (${who.body.id})`);
    await page.waitForTimeout(2000);
    check(await page.locator('bx-agent-dock[open]').count() === 0 && (await api(page, '/delegate/workspace')).body.enabled === false, 'B: the agent was not turned on, and the dock stays closed');
  } finally {
    await ctx.close().catch(() => {});
    await X.stop();
  }
}

async function stateC(browser, check) {
  const X = await J0.boot('firstrun-c', { host: { workspaceAgent: { enabled: true, who: 'admins' } } });
  const { ctx, page } = await newPage(browser);
  try {
    await open(page, X);
    await sheetUp(page);
    const t = await sheetApi(page);
    check(t.plan.agent.kind === 'host-on' && /Your host has turned it on/.test(await page.locator(`${S} [data-agent]`).innerText()), 'C: the host\'s preset: the sheet says the agent is on for them');
    check(await page.locator(`${S} [data-primary]`).innerText() === 'Create my account', 'C: …and the button needs to turn nothing on');
    const ws0 = await bearer(X, '/delegate/workspace');
    check(ws0.body.enabled === true && ws0.body.by === 'host' && ws0.body.locks && ws0.body.locks.enabled.locked && ws0.body.locks.who.locked, `C: on by the host, enabled and who locked (${JSON.stringify(ws0.body.locks)})`);
    check((await bearer(X, '/delegate/workspace', 'PUT', { enabled: false })).status === 409, 'C: the host\'s switch can\'t be turned off through the API (409)');
    await shots(page, 'c-host-on');
    // the vault's setup fails (answered by the pass): the account is made anyway, and what is left is said
    await page.route('**/api/xbin/vault-unseal', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"the disk is full"}' }));
    await page.fill(`${S} #bxfr-name`, 'Cy Admin');
    const pw = secret();
    await page.fill(`${S} #bxfr-pw`, pw);
    await page.fill(`${S} #bxfr-vault`, 'v');
    await page.fill(`${S} #bxfr-repeat`, 'v');
    await page.click(`${S} [data-primary]`);
    await page.waitForSelector(`${S} [data-state="left"]`, { timeout: 30000 });
    check(/The vault isn't set up \(the disk is full\)/.test(await page.locator(`${S} [data-left]`).innerText()), 'C: the vault failed: the sheet says what is left');
    check((await api(page, '/whoami')).body.id === 'cy', 'C: …and the person is signed in as their account all the same, not locked out');
    await shots(page, 'c-vault-left');
    await page.unroute('**/api/xbin/vault-unseal');
    await Promise.all([page.waitForNavigation({ timeout: 60000 }), page.click(`${S} [data-continue]`)]);
    await J0.shellUp(page);
    const st = await api(page, '/delegate/state');
    check(st.body.access === true && st.body.locks && st.body.locks.who, 'C: the host gave the new admin an agent of their own, with no click');
    check((await bearer(X, '/vault-status')).body.mode === 'unconfigured', 'C: the vault is left for the admin console, as the sheet said');
    await page.goto(`${X.url}/xbin/system`);
    await page.waitForSelector('[data-host]', { timeout: 20000 }).catch(() => {});
    check(/Set by your host/.test(await page.locator('[data-host]').first().innerText().catch(() => ''))
      && await page.locator('button', { hasText: /^Remove$/ }).count() === 0 && await page.locator('button', { hasText: 'Give access' }).count() === 0,
      'C: /xbin/system shows who has an agent as the host\'s — read-only, no Remove, no Give access');
    await page.screenshot({ path: path.join(SHOTS, 'c-system-page-desktop-dark.png') });
  } finally {
    await ctx.close().catch(() => {});
    await X.stop();
  }
}

async function stateD(browser, check) {
  const X = await J0.boot('firstrun-d', { host: { workspaceAgent: false }, vault: secret() });
  const { ctx, page } = await newPage(browser);
  try {
    await open(page, X);
    await sheetUp(page);
    const t = await sheetApi(page);
    check(t.plan.agent.kind === 'host-off' && /turned workspace agents off/.test(await page.locator(`${S} [data-agent]`).innerText()), 'D: the host locked agents off: the sheet says so');
    check(await page.locator(`${S} [data-primary]`).innerText() === 'Create my account' && await page.locator(`${S} [data-quiet]`).count() === 0, 'D: …and offers no agent');
    await shots(page, 'd-host-off');
    // a vault set up and sealed (after a restart without the host's passphrase): answered by the pass —
    // one field, to unlock it, tried before anything is made: a wrong passphrase is said in place
    await page.route('**/api/xbin/vault-status', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"initialized":true,"sealed":true,"mode":"sealed","insecure":false}' }));
    await page.route('**/api/xbin/vault-unseal', (r) => r.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"bad passphrase"}' }));
    await page.evaluate(() => { try { localStorage.clear(); } catch { /* none */ } });
    await page.reload();
    await J0.shellUp(page);
    await sheetUp(page);
    check((await sheetApi(page)).plan.vault === 'unlock' && await page.locator(`${S} #bxfr-repeat`).count() === 0, 'D: a sealed vault: one field, to unlock it');
    await page.fill(`${S} #bxfr-name`, 'Di Admin');
    await page.fill(`${S} #bxfr-pw`, secret());
    await page.fill(`${S} #bxfr-vault`, 'not-the-passphrase');
    await page.press(`${S} #bxfr-vault`, 'Enter');
    await page.waitForSelector(`${S} [data-err="vault"]`, { timeout: 10000 });
    check(/isn't the vault's passphrase/.test(await page.locator(`${S} [data-err="vault"]`).innerText()) && ((await bearer(X, '/users')).body.users || []).length === 0,
      'D: …a wrong passphrase is said under it, and nothing was made');
    await shots(page, 'd-unlock-wrong');
  } finally {
    await ctx.close().catch(() => {});
    await X.stop();
  }
}

async function firstRun(browser) {
  const { check, skip, done } = checker('first-run');
  const why = J0.noJourney();
  if (why) { skip(`the first-run sheet: ${why}`); done(); return; }
  for (const [name, fn] of [['A', stateA], ['B', stateB], ['C', stateC], ['D', stateD]]) {
    const t = Date.now();
    try { await fn(browser, check); } catch (e) { check(false, `${name}: ${redact(e).split('\n').slice(0, 3).join(' ')}`); }
    log(`firstRun: state ${name} in ${((Date.now() - t) / 1000).toFixed(1)} s`);
  }
  log(`firstRun: screenshots in ${SHOTS}`);
  done();
}

module.exports = { firstRun };
