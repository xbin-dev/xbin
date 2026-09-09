// hack/ui-harness/lib.js — shared plumbing for the harness passes (shots.js):
// Playwright bootstrap, login, the shell's test surface, wait-for helpers,
// screenshots, <select> dumps, and the PASS/FAIL checker.
//
// Passes drive the UI through `testApi()` on bx-shell / bx-frame / bx-admin
// (stable names over private state) plus a few DOM hooks that are part of
// the markup contract (.card[data-path], .item[data-path], .admin-pop,
// .float[data-path], .spawn, details[data-sec], [data-netset], [data-set]).
// `make js-check` refuses a private-member access (dot underscore) here.
const path = require('path');
const fs = require('fs');

const pw = (() => {
  try { return require('playwright'); } catch { /* fall through */ }
  const dir = process.env.PLAYWRIGHT_DIR;
  if (!dir) throw new Error('playwright not found: npm i -g playwright (+ npx playwright install chromium) or set PLAYWRIGHT_DIR');
  return require(path.join(dir, 'node_modules', 'playwright'));
})();
const URL = process.env.URL || 'http://127.0.0.1:8697';
const OUT = process.env.OUT || path.join(__dirname, 'out');
fs.mkdirSync(OUT, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => console.log('[shots]', ...a);

async function login(browser, user, pass, ctxOpts = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1, ...ctxOpts });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log(`${user}: PAGE ERROR`, e.message));
  page.on('console', (m) => { if (m.type() === 'error') log(`${user}: console.error`, m.text().slice(0, 200)); });
  await page.goto(`${URL}/login`);
  await page.fill('input[name=username]', user);
  await page.fill('input[name=password]', pass);
  await Promise.all([page.waitForNavigation(), page.click('button')]);
  return { ctx, page };
}

// settle: two animation frames — lit has rendered whatever the last state
// change queued. Use after a state mutation, before reading the DOM.
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))));

// In-page function plumbing: fn's SOURCE travels to the browser and runs
// with the shell's test surface as `t` (undefined until <bx-shell> upgraded).
const inPage = `(function (src, arg) {
  const el = document.querySelector('bx-shell');
  const t = el && el.testApi ? el.testApi() : undefined;
  return new Function('t', 'arg', 'return (' + src + ')(t, arg)')(t, arg);
})`;

// sh(page, (t, arg) => …, arg): run against the shell's test surface.
const sh = (page, fn, arg) => page.evaluate(`${inPage}(${JSON.stringify(fn.toString())}, ${JSON.stringify(arg ?? null)})`);
// fr(page, path, (f, t, arg) => …, arg): run against a mounted tile frame's test surface.
const fr = (page, src, fn, arg) => sh(page, (t, a) => new Function('f', 't', 'arg', `return (${a.fsrc})(f, t, arg)`)(t.frameFor(a.src)?.testApi(), t, a.arg), { src, fsrc: fn.toString(), arg: arg ?? null });

// waitFor(page, (t, arg) => truthy, arg, {timeout, label}): poll the page,
// then settle. The label names the wait in the timeout error.
async function waitFor(page, fn, arg, { timeout = 10000, label = '' } = {}) {
  try {
    await page.waitForFunction(`${inPage}(${JSON.stringify(fn.toString())}, ${JSON.stringify(arg ?? null)})`, null, { timeout, polling: 50 });
  } catch (e) {
    throw new Error(`waitFor ${label || fn.toString().slice(0, 100)}: ${e.message.split('\n')[0]}`);
  }
  await settle(page);
}

// Playwright selectors pierce open shadow roots, so a plain CSS wait reaches
// into bx-shell / bx-frame / bx-tile-admin.
async function waitSel(page, selector, { timeout = 10000, state = 'visible' } = {}) {
  await page.waitForSelector(selector, { timeout, state });
  await settle(page);
}

// openShell: the workspace root with the shell upgraded and its layout loaded.
async function openShell(page) {
  await page.goto(`${URL}/`);
  await page.waitForSelector('bx-shell', { timeout: 15000 });
  await waitFor(page, (t) => !!t && t.screens.length > 0, null, { timeout: 15000, label: 'shell layout loaded' });
}
// usePersonalScreen: start on a personal screen (a sidebar click on an org
// screen would open a draft).
async function usePersonalScreen(page) {
  await sh(page, (t) => t.usePersonalScreen());
  await settle(page);
}
async function openTile(page, p) {
  await sh(page, (t, p) => t.openTile(p), p);
  await waitSel(page, `.card[data-path="${p}"] bx-frame`, { state: 'attached' });
}
async function closeTile(page, p) {
  await sh(page, (t, p) => t.closeTile(p), p);
  await settle(page);
}
// tileFrame: the Playwright Frame of a mounted tile's document.
async function tileFrame(page, p, { timeout = 15000 } = {}) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const f = page.frames().find((f) => f.url().includes(`/c/${p}/`));
    if (f) return f;
    if (Date.now() > deadline) throw new Error(`tile frame ${p} never appeared`);
    await sleep(100);
  }
}

// The admin tile is its own document (not the shell): navigate by hash and
// wait for the tab's own text. A hash-only change never switches tabs (the
// constructor reads the hash), hence the reload.
async function gotoTab(page, hash, waitText) {
  await page.goto(`${URL}/c/tiles/admin/#${hash}`);
  await page.reload();
  await page.waitForSelector(`text=${waitText}`, { timeout: 15000 });
  await settle(page);
}

async function shot(page, name, opts = {}) {
  await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true, ...opts });
  log('wrote', name + '.png');
}

// Text dump of every <select> under a selector (native dropdowns don't render
// in screenshots) → out/<name>.txt
async function dumpSelects(page, name, selector = 'select') {
  const rows = await page.locator(selector).evaluateAll((sels) => sels.map((s) => ({
    title: s.title, value: s.value,
    options: [...s.options].map((o) => `${o.selected ? '*' : ' '} [${o.value}] ${o.textContent.trim()}${o.title ? '   // ' + o.title.replace(/\n/g, ' ⏎ ') : ''}`),
  })));
  const txt = rows.map((r, i) => `select#${i} value=${JSON.stringify(r.value)}${r.title ? ' title=' + JSON.stringify(r.title) : ''}\n  ${r.options.join('\n  ')}`).join('\n\n');
  fs.writeFileSync(`${OUT}/${name}.txt`, txt + '\n');
  log('wrote', name + '.txt');
}

// closeCtx: flush the shell's debounced layout save (400 ms) before closing
// the context — otherwise the pass's last change (a closed tile, a screen
// switch) never reaches the server and the NEXT pass starts from stale state.
async function closeCtx(ctx, page) {
  await sh(page, (t) => t?.flushSave?.()).catch(() => {});
  await ctx.close();
}

// checker(name): PASS/FAIL lines to out/<name>.txt; done() throws when any failed.
function checker(name) {
  const file = `${OUT}/${name}.txt`;
  fs.writeFileSync(file, '');
  const fails = [];
  const check = (cond, msg) => { fs.appendFileSync(file, `${cond ? 'PASS' : 'FAIL'} ${msg}\n`); if (!cond) fails.push(msg); };
  const done = () => { if (fails.length) throw new Error(`${name}: ${fails.length} check(s) failed:\n  ${fails.join('\n  ')}`); };
  return { check, done };
}

module.exports = { pw, URL, OUT, fs, sleep, log, login, closeCtx, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, closeTile, tileFrame, gotoTab, shot, dumpSelects, checker };
