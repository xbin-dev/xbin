// hack/ui-harness/passes/apphelp.js — the iOS app's help screenshots ("where
// do I find the QR code?"): the shell's top bar with the settings menu open
// and "add a device" ringed, and the add-device panel with its QR code and
// the address field. Phone-friendly crops (about 1100 px wide at 2×) in the
// shell's dark theme, as dev1 of the seeded workspace; the enrollment answer
// is stubbed to a fixed code at https://xbin.example.com, so no harness
// address shows and the PNGs change only when the shell does.
// hack/ui-harness/app-help-shots.sh runs this pass on a fresh seed and
// copies the two PNGs into native/ios/App/Resources/Help/ (native/AGENTS.md
// → "The help screenshots").
const { execFileSync } = require('child_process');
const { OUT, login, closeCtx, settle, sh, waitSel, openShell, usePersonalScreen, checker, log } = require('../lib');

const ORIGIN = 'https://xbin.example.com';
const CODE = 'XBINHELPSCREENSHOTXBINHELP'; // 26 × [A-Z2-7], like a real code
const LINK = `xbin://enroll?u=${encodeURIComponent(ORIGIN)}&c=${CODE}`;
const W = 1280, H = 800, CROP = 560; // CSS px; the crop is the top bar's right end

// ring: numbered accent rings around page rects (help-screen callouts),
// drawn over the page for the screenshot and removed after.
function ring(page, rects) {
  return page.evaluate((rs) => {
    for (const [i, r] of rs.entries()) {
      const d = document.createElement('div');
      d.className = 'help-ring';
      d.style.cssText = `position:fixed; z-index:99999; pointer-events:none; left:${r.x - 4}px; top:${r.y - 4}px;
        width:${r.width + 8}px; height:${r.height + 8}px; border:2px solid #f5a623; border-radius:9px;
        box-shadow:0 0 0 4px rgba(245,166,35,.28), 0 0 18px rgba(245,166,35,.45); box-sizing:border-box`;
      const n = document.createElement('div');
      n.className = 'help-ring';
      n.textContent = String(i + 1);
      n.style.cssText = `position:fixed; z-index:99999; pointer-events:none; left:${r.x - 34}px; top:${r.y + r.height / 2 - 11}px;
        width:22px; height:22px; border-radius:50%; background:#f5a623; color:#23272e; font:700 13px/22px system-ui, sans-serif;
        text-align:center; box-shadow:0 2px 8px rgba(0,0,0,.5)`;
      document.body.append(d, n);
    }
  }, rects);
}

async function appHelp(browser) {
  const { check, done } = checker('appHelp');
  const { ctx, page } = await login(browser, 'dev1', 'devpass123', { viewport: { width: W, height: H }, deviceScaleFactor: 2, colorScheme: 'dark' });
  await page.evaluate(() => { try { localStorage.removeItem('xbin-phone-address'); } catch { /* none */ } });
  await openShell(page);
  await usePersonalScreen(page);
  // a bare canvas behind the menu and the panel (dev1's layout comes back after)
  const saved = await sh(page, (t) => t.openTiles.map((o) => ({ ...o })));
  await sh(page, (t) => { t.setGeom(() => []); return t.flushSave(); });
  await settle(page);

  // 1. the settings chip → the menu, "add a device" first
  const chip = page.locator('bx-shell .top button.chip.settings');
  check((await chip.textContent()).trim() === 'settings', 'the top bar has the settings chip');
  await chip.click();
  await waitSel(page, 'bx-shell .wsmenu [data-add-device]');
  const rect = (sel) => page.locator(sel).first().boundingBox();
  const [c, a, m] = [await rect('bx-shell .top button.chip.settings'), await rect('bx-shell .wsmenu [data-add-device]'), await rect('bx-shell .wsmenu')];
  await ring(page, [c, a]);
  await settle(page);
  const x = W - CROP;
  check(c.x - 36 >= x && a.x - 36 >= x - 40 && m.y + m.height + 14 <= H, `the callouts fit the crop (${JSON.stringify({ c, a, m })})`);
  await page.screenshot({ path: `${OUT}/app-help-1-settings.png`, clip: { x, y: 0, width: CROP, height: Math.ceil(m.y + m.height + 14) } });
  log('wrote app-help-1-settings.png');
  await page.evaluate(() => document.querySelectorAll('.help-ring').forEach((e) => e.remove()));

  // 2. "add a device" opens the panel on the add flow (stubbed code)
  await page.route('**/api/xbin/devices/enroll-code', (route) => route.fulfill({ status: 200, contentType: 'application/json',
    body: JSON.stringify({ code: CODE, origin: ORIGIN, url: LINK, expires: Math.floor(Date.now() / 1000) + 300 }) }));
  await page.locator('bx-shell .wsmenu [data-add-device]').click();
  await waitSel(page, 'bx-devices [data-enroll] svg');
  check(await page.locator('bx-devices [data-addr]').inputValue() === ORIGIN, 'the address field shows the workspace address');
  const box = await rect('bx-devices .box');
  const pad = 10;
  await page.screenshot({ path: `${OUT}/app-help-2-add-device.png`,
    clip: { x: box.x - pad, y: box.y - pad, width: box.width + 2 * pad, height: box.height + 2 * pad } });
  log('wrote app-help-2-add-device.png');
  try {
    const got = execFileSync('zbarimg', ['-q', '--raw', `${OUT}/app-help-2-add-device.png`], { encoding: 'utf8' }).trim();
    check(got === LINK, `the help QR code decodes to the example link (${got})`);
  } catch (e) { log('appHelp: zbarimg unavailable or failed — QR decode not checked:', e.message.split('\n')[0]); }
  await page.unroute('**/api/xbin/devices/enroll-code');
  await sh(page, (t, tiles) => { t.setGeom(() => tiles); return t.flushSave(); }, saved);
  await closeCtx(ctx, page);
  done();
}

module.exports = { appHelp };
