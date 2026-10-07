// hack/ui-harness/passes/devicetheme.js — covers D188: a light/dark
// override per device. Run it under both system schemes (HARNESS_THEME=light
// and the default dark). The admin's Theme is set to the system's own
// scheme S explicitly; this browser picks the other one, D, in the settings
// menu's "This device" row. Then:
//   1. the shell restyles to D at once, the Theme row still shows S (the
//      person's choice) and This device shows D; localStorage holds D and
//      the hint cookie is D; an open tile follows live (xbin:appearance);
//   2. after a reload the shell is D from its first frame (theme-boot.js),
//      a tile's first frame is D (its URL asks for xbin-appearance=D) and a
//      tile opened later too;
//   3. another context of the same person (no override) is S — the shell
//      and a tile's first frame;
//   4. the person's Theme changed elsewhere (to D', the other context's
//      prefs write) leaves this browser on D, the Theme row following;
//   5. "Follow my setting" brings the person's theme back to the shell and
//      the open tile, clears the stored value and sets the cookie to it.
// Shot: devicetheme-row (the settings menu). The admin's theme and this
// browser's override are cleared at the end.
const path = require('path');
const { URL, fs, sleep, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, sh, settle, shotEl, checker, THEME } = require('../lib');

const WS = process.env.WS || '';
const PROBE = 'apps/dtprobe', LATER = 'apps/dtprobe-later';
const page = (title) => `<!doctype html><html lang="en" data-bx-theme="auto"><head><meta charset="utf-8">
<title>${title}</title><link rel="stylesheet" href="/vendor/theme.css"></head>
<body class="bx"><h1>${title}</h1><p>This tile follows the device's theme from its first frame.</p></body></html>
`;

// In every document, at its first animation frame: the scheme it paints.
function firstFrame() {
  const doc = document;
  requestAnimationFrame(() => {
    if (doc !== document || !doc.documentElement) return;
    doc.bxFirst = getComputedStyle(doc.documentElement).getPropertyValue('--bx-scheme').trim() || 'none';
  });
}
const nowScheme = (f) => f.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--bx-scheme').trim());
async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(100);
  }
}
async function shown(pg, tile) {
  for (const f of pg.frames()) {
    if (!f.url().includes(`/c/${tile}/`)) continue;
    if (await f.frameElement().then((h) => h.isVisible()).catch(() => false)) return f;
  }
  return null;
}
const first = (frameOf, label) => until(async () => {
  const f = await frameOf();
  return f && f.evaluate(() => (location.href !== 'about:blank' && document.bxFirst) || null);
}, `the first-frame record of ${label}`);
async function framed(pg, tile) {
  await openTile(pg, tile);
  await pg.locator(`.card[data-path="${tile}"]`).first().scrollIntoViewIfNeeded();
  return until(() => shown(pg, tile), `${tile} on screen`);
}

async function deviceTheme(browser) {
  const { check, done } = checker(`devicetheme-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  for (const t of [PROBE, LATER]) {
    fs.mkdirSync(path.join(WS, t), { recursive: true });
    fs.writeFileSync(path.join(WS, t, 'xbin.json'), JSON.stringify({ title: t }) + '\n');
    fs.writeFileSync(path.join(WS, t, 'index.html'), page(t));
  }
  const S = THEME === 'light' ? 'light' : 'dark', D = S === 'light' ? 'dark' : 'light';
  const C = await login(browser, 'admin', 'admin', { viewport: { width: 1440, height: 900 } });
  await C.ctx.addInitScript(firstFrame);
  const api = (method, p, data, headers = {}) => C.ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json', ...headers }, ...(data === undefined ? {} : { data: JSON.stringify(data) }),
  });
  let O = null;
  try {
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [PROBE, LATER].includes(c.path)).length === 2, 'the probes registered', 20000);
    await api('PUT', '/prefs/theme', S);
    await openShell(C.page);
    await usePersonalScreen(C.page);
    const probe = await framed(C.page, PROBE);
    await until(async () => (await nowScheme(probe)) === S, `the probe is ${S}`);

    // 1. pick This device: D
    await sh(C.page, (t) => t.openSettings());
    await settle(C.page);
    const pressed = () => C.page.evaluate(() => [...document.querySelector('bx-shell').shadowRoot.querySelectorAll('.wsmenu [aria-pressed="true"]')]
      .map((b) => b.dataset.appearance || `device:${b.dataset.deviceTheme}`));
    check(JSON.stringify(await pressed()) === JSON.stringify([`theme:${S}`, 'device:person', 'density:compact']), `the menu: Theme ${S}, This device: Follow my setting (${JSON.stringify(await pressed())})`);
    await C.page.locator(`bx-shell .wsmenu [data-device-theme="${D}"]`).click();
    await settle(C.page);
    check(await nowScheme(C.page.mainFrame()) === D, `This device: ${D} restyles the shell at once`);
    check(JSON.stringify(await pressed()) === JSON.stringify([`theme:${S}`, `device:${D}`, 'density:compact']), `…Theme still shows the person's ${S}, This device ${D} (${JSON.stringify(await pressed())})`);
    await shotEl(C.page, 'bx-shell .wsmenu', `devicetheme-row-${THEME}`);
    check(await C.page.evaluate(() => localStorage.getItem('xbin-theme-device')) === D, `localStorage xbin-theme-device = ${D}`);
    const cookie = async (ctx) => (await ctx.cookies(URL)).find((c) => c.name === 'xbin_theme')?.value ?? '';
    check(await cookie(C.ctx) === D, `the hint cookie is the device's (${await cookie(C.ctx)})`);
    const live = await until(async () => (await nowScheme(probe)) === D, 'the open tile follows').catch(() => false);
    check(!!live, `an open tile follows live (${await nowScheme(probe)})`);
    check((await (await api('GET', '/prefs/theme')).json()) === S, "the person's synced theme is unchanged on the server");
    await C.page.locator('bx-shell .ctx-backdrop').first().dispatchEvent('pointerdown');

    // 2. a reload: the first frames (the layout with the probe saved first)
    await sh(C.page, (t) => t.flushSave());
    await C.page.reload();
    await C.page.waitForSelector('bx-shell');
    const s0 = await first(async () => C.page.mainFrame(), 'the shell');
    check(s0 === D, `after a reload the shell is ${D} from its first frame (${s0})`);
    // in view: Chromium paints an off-screen cross-origin frame only once it comes into view
    await C.page.locator(`.card[data-path="${PROBE}"]`).first().scrollIntoViewIfNeeded();
    const p0 = await first(() => shown(C.page, PROBE), PROBE);
    check(p0 === D, `a tile's first frame is ${D} (${p0})`);
    check((await shown(C.page, PROBE)).url().includes(`xbin-appearance=${D}`), "…its URL asks xbind for the device's theme");
    await framed(C.page, LATER);
    const l0 = await first(() => shown(C.page, LATER), LATER);
    check(l0 === D, `a tile opened later is ${D} from its first frame (${l0})`);

    // 3. another context: the person's theme
    O = await login(browser, 'admin', 'admin', { viewport: { width: 1440, height: 900 } });
    await O.ctx.addInitScript(firstFrame);
    await openShell(O.page);
    const o0 = await first(async () => O.page.mainFrame(), 'the other shell');
    await O.page.locator(`.card[data-path="${PROBE}"]`).first().scrollIntoViewIfNeeded();
    const op = await first(() => shown(O.page, PROBE), `${PROBE} elsewhere`);
    check(o0 === S && op === S, `another browser without the override shows the person's ${S} (shell ${o0}, tile ${op})`);
    check(!(await (await shown(O.page, PROBE)).url().includes('xbin-appearance')), '…and asks for no override');

    // 4. the person's theme changes elsewhere: this browser stays on D
    await O.page.request.put(`${URL}/api/xbin/prefs/theme`, { data: JSON.stringify(D), headers: { 'Content-Type': 'application/json', 'X-Prefs-Writer': 'other-device' } });
    await until(async () => (await nowScheme(O.page.mainFrame())) === D, 'the other browser follows the person');
    await api('PUT', '/prefs/theme', S, { 'X-Prefs-Writer': 'other-device' });
    await until(async () => (await nowScheme(O.page.mainFrame())) === S, 'the other browser follows back');
    await sleep(300);
    check(await nowScheme(C.page.mainFrame()) === D && await nowScheme(await shown(C.page, PROBE)) === D, `the person's theme changing elsewhere leaves this browser on ${D}`);

    // 5. Follow my setting
    await sh(C.page, (t) => t.openSettings());
    await settle(C.page);
    await C.page.locator('bx-shell .wsmenu [data-device-theme="person"]').click();
    await settle(C.page);
    const back = await until(async () => (await nowScheme(await shown(C.page, PROBE))) === S, 'the tile back to the person').catch(() => false);
    check(await nowScheme(C.page.mainFrame()) === S && !!back, `Follow my setting: the shell and the open tile show the person's ${S} again`);
    check(await C.page.evaluate(() => localStorage.getItem('xbin-theme-device')) === null && await cookie(C.ctx) === S, `…the stored value is gone, the cookie is ${S} (${await cookie(C.ctx)})`);
    await C.page.locator('bx-shell .ctx-backdrop').first().dispatchEvent('pointerdown');
    for (const t of [PROBE, LATER]) await closeTile(C.page, t);
  } finally {
    await C.page.evaluate(() => localStorage.removeItem('xbin-theme-device')).catch(() => {});
    await api('DELETE', '/prefs/theme').catch(() => {});
    if (O) await closeCtx(O.ctx, O.page);
    await closeCtx(C.ctx, C.page);
    for (const t of [PROBE, LATER]) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'dtprobe'))) fs.rmSync(dir, { recursive: true, force: true });
    }
  }
  done();
}

module.exports = { deviceTheme };
