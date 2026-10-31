// hack/ui-harness/passes/dragshield.js — #54 / D217: a drag never falls off
// its window onto a tile's <iframe>. Every drag is fast here — one or two
// pointer moves, each landing over ANOTHER tile's page — and must keep
// following to the end:
//   - a tile window's (bx-frame's terminal/IDE window) bottom-right corner
//     across a tile's page: the size follows the pointer, is kept after the
//     release, and the page under it takes a click again (no shield left);
//   - Escape mid-resize: the size goes back, the shield is gone, the moves
//     after it change nothing; the window losing focus mid-resize (a release
//     the page never saw): the same;
//   - the window's title bar jumped onto another tile's page;
//   - a floating tile's corner and a pop-out window's corner;
//   - a Tiled screen's divider jumped onto the next tile's page.
// Runs in a Chromium of its own whose tile pages are out of process, as a
// desktop Chrome's are (site isolation, sandboxed frames isolated): only
// there does a page under the cursor take the pointer from the shell —
// in the harness's shared browser the CSS resize corner of before D217
// kept following, here it stalls at once (checked against 2b9f9d40).
// Restores the layout and the window's pref it found.
const { pw, themed, URL, sleep, login, settle, sh, fr, waitFor, waitSel, openShell, usePersonalScreen, tileFrame, shot, checker } = require('../lib');

const [A, B, C] = ['apps/crawler', 'apps/pinned', 'apps/offline'];
const R = (r) => r && [Math.round(r.x), Math.round(r.y), Math.round(r.w), Math.round(r.h)];
const near = (a, b, d = 2) => Math.abs(a - b) <= d;
const rectOf = (page, sel) => sh(page, (t, s) => { const r = t.query(s)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
// a tile's page (its <iframe>) on screen
const pageRect = (page, p) => sh(page, (t, p) => {
  const r = t.frameFor(p)?.shadowRoot?.querySelector('iframe')?.getBoundingClientRect();
  return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null;
}, p);
const popBox = (page) => sh(page, (t, p) => t.frameFor(p)?.popBox() ?? null, A);
const setPop = async (page, box) => { await fr(page, A, (f, t, b) => f.setPop(b), box); await settle(page); await settle(page); };
const popRect = (page) => sh(page, (t, p) => { const r = t.frameFor(p)?.testApi().popElement()?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, A);
// the element under a viewport point, through the shadow roots
const deepAt = (page, x, y) => page.evaluate(([x, y]) => {
  let el = document.elementFromPoint(x, y);
  for (;;) { const n = el?.shadowRoot?.elementFromPoint(x, y); if (!n || n === el) break; el = n; }
  return el ? `${el.tagName.toLowerCase()}${el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).join('.') : ''}` : null;
}, [x, y]);
const shielded = (page) => page.evaluate(() => !!document.querySelector('[data-drag-shield]'));
// does a click at (x, y) reach tile p's page? (its document counts pointerdowns)
async function clickReaches(page, p, x, y) {
  const f = await tileFrame(page, p);
  await f.evaluate(() => { if (window.dsHits === undefined) { window.dsHits = 0; addEventListener('pointerdown', () => { window.dsHits++; }, true); } });
  const n0 = await f.evaluate(() => window.dsHits);
  await page.mouse.click(x, y);
  await sleep(100);
  return (await f.evaluate(() => window.dsHits)) > n0;
}
// press at (x, y), then each [dx, dy] stop in `steps` moves — a fast drag
async function press(page, x, y, stops, steps = 1) {
  await page.mouse.move(x, y);
  await page.mouse.down();
  for (const [dx, dy] of stops) await page.mouse.move(x + dx, y + dy, { steps });
}

async function dragShield() {
  const args = ['--site-per-process', '--enable-features=IsolateSandboxedIframes'];
  if (process.env.TILE_ASSETS === 'origins') args.push('--host-resolver-rules=MAP *.localhost 127.0.0.1');
  const browser = themed(await pw.chromium.launch({ args }));
  try { await drags(browser); } finally { await browser.close(); }
}

async function drags(browser) {
  const { check, done } = checker('dragshield');
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`, termPref = `${URL}/api/xbin/prefs/term%3Aapps%3Acrawler`;
  const original = await (await ctx.request.get(prefs)).json();
  await openShell(page);
  await usePersonalScreen(page);
  await sh(page, (t) => t.addScreen('canvas'));
  await settle(page);
  // A small at the origin, B big beside it: the window of A reaches over B's page
  await sh(page, (t, [a, b]) => t.setGeom(() => [{ path: a, x: 0, y: 0, w: 480, h: 384 }, { path: b, x: 528, y: 0, w: 624, h: 672 }]), [A, B]);
  await waitSel(page, `.card[data-path="${B}"] bx-frame`, { state: 'attached' });
  await page.locator(`.card[data-path="${A}"]`).first().evaluate((e) => e.scrollIntoView({ block: 'start' }));
  await tileFrame(page, A); await tileFrame(page, B);
  await settle(page);
  const pa = await pageRect(page, A), pb = await pageRect(page, B);
  check(!!pa && !!pb && pb.h > 560, `two tiles' pages on screen (${JSON.stringify(R(pa))}, ${JSON.stringify(R(pb))})`);
  if (!pa || !pb) { await ctx.close(); done(); return; }
  const { targetInfos } = await (await ctx.newCDPSession(page)).send('Target.getTargets');
  const oopif = targetInfos.filter((x) => x.type === 'iframe').length;
  check(oopif >= 2, `the tiles' pages run out of process, as in a desktop Chrome (${oopif} iframe targets)`);

  // ---- the tile window's corner, across B's page ----
  await fr(page, A, (f) => f.open());
  await waitSel(page, `bx-frame[src="${A}"] .pop`);
  const P0 = { x: Math.round(pb.x - 300), y: Math.round(pb.y + 40), w: 420, h: 240 }; // over the window's min-size (380 × 220)
  await setPop(page, P0);
  let r = await popRect(page);
  const cx = r.x + r.w - 4, cy = r.y + r.h - 4; // inside the corner's grip
  check(/^div\.rsz/.test(await deepAt(page, cx, cy) ?? ''), `the window's corner is its resize grip (${await deepAt(page, cx, cy)})`);
  check(/^iframe/.test(await deepAt(page, cx + 200, cy + 150) ?? ''), `past the corner lies B's page (${await deepAt(page, cx + 200, cy + 150)})`);
  await press(page, cx, cy, [[400, 300], [410, 310]], 2);
  const during = await popRect(page), shieldOn = await shielded(page);
  await page.mouse.up();
  await settle(page);
  r = await popRect(page);
  const kept = await popBox(page);
  check(shieldOn && near(during.w, P0.w + 410) && near(during.h, P0.h + 310), `the corner kept following across B's page (${P0.w}×${P0.h} → ${Math.round(during.w)}×${Math.round(during.h)}, want ${P0.w + 410}×${P0.h + 310}; shield up ${shieldOn})`);
  check(near(r.w, P0.w + 410) && near(r.h, P0.h + 310) && near(kept?.w, P0.w + 410) && near(kept?.h, P0.h + 310), `the size is kept after the release (${JSON.stringify(R(r))}; pref ${kept?.w}×${kept?.h})`);
  check(!(await shielded(page)), 'no shield after the release');
  await shot(page, 'dragshield-resized', { fullPage: false });
  check(await clickReaches(page, B, pb.x + 40, pb.y + pb.h - 40), "B's page takes a click again");

  // ---- Escape mid-resize (the keyboard was in B's page, from that click) ----
  await setPop(page, P0);
  r = await popRect(page);
  await press(page, r.x + r.w - 4, r.y + r.h - 4, [[200, 150]]);
  const grown = await popRect(page);
  await page.keyboard.press('Escape');
  await sleep(50); await settle(page);
  const back = await popRect(page), escShield = await shielded(page);
  await page.mouse.move(r.x + r.w + 300, r.y + r.h + 250, { steps: 2 }); // the button still down: nothing follows
  await page.mouse.up();
  await settle(page);
  const after = await popRect(page);
  check(near(grown.w, P0.w + 200) && near(back.w, P0.w) && near(back.h, P0.h) && !escShield && near(after.w, P0.w) && near(after.h, P0.h),
    `Escape mid-resize: the size goes back, the shield goes, later moves change nothing (${Math.round(grown.w)} → ${Math.round(back.w)}×${Math.round(back.h)} → ${Math.round(after.w)}×${Math.round(after.h)}; shield ${escShield})`);
  check(near((await popBox(page))?.w, P0.w), `and the kept size is the old one (${(await popBox(page))?.w})`);

  // ---- the window losing focus mid-resize (a release outside it, never seen) ----
  await setPop(page, P0);
  r = await popRect(page);
  await press(page, r.x + r.w - 4, r.y + r.h - 4, [[200, 150]]);
  await page.evaluate(() => window.dispatchEvent(new Event('blur')));
  await settle(page);
  const blurShield = await shielded(page), blurred = await popRect(page);
  await page.mouse.up();
  check(!blurShield && near(blurred.w, P0.w) && near(blurred.h, P0.h), `the window losing focus mid-resize: no shield, the size back (${Math.round(blurred.w)}×${Math.round(blurred.h)}; shield ${blurShield})`);
  check(await clickReaches(page, B, pb.x + 40, pb.y + pb.h - 40), "B's page takes a click after it");

  // ---- the title bar jumped onto A's page ----
  await setPop(page, P0);
  r = await popRect(page);
  const tx = r.x + 100, ty = r.y + 16;
  check(/^iframe/.test(await deepAt(page, tx - 160, ty + 260) ?? ''), `the jump lands on A's page (${await deepAt(page, tx - 160, ty + 260)})`);
  await press(page, tx, ty, [[-150, 250], [-160, 260]]);
  const moving = await popRect(page);
  await page.mouse.up();
  await settle(page);
  const moved = await popBox(page);
  check(near(moving.x, r.x - 160) && near(moving.y, r.y + 260) && near(moved?.x, r.x - 160) && near(moved?.y, r.y + 260),
    `the window followed its title bar onto A's page and stays there (${JSON.stringify(R(r))} → ${JSON.stringify(R(moving))}; kept ${moved?.x},${moved?.y})`);
  check(!(await shielded(page)), 'no shield after the move');
  check(await clickReaches(page, A, pa.x + 40, pa.y + pa.h - 40), "A's page takes a click again");
  await fr(page, A, (f) => f.closeTerminal());
  await waitSel(page, `bx-frame[src="${A}"] .pop`, { state: 'hidden' });

  // ---- a floating tile's corner across B's page ----
  const F0 = { x: Math.round(pb.x + 40), y: Math.round(pb.y + 80), w: 300, h: 200 };
  await sh(page, (t, [c, f]) => t.setGeom((tiles) => [...tiles, { path: c, x: 0, y: 432, w: 480, h: 240, float: { ...f, z: 120 } }]), [C, F0]);
  await waitSel(page, `.float[data-path="${C}"]`, { state: 'attached' });
  await settle(page);
  r = await rectOf(page, `.float[data-path="${C}"]`);
  await press(page, r.x + r.w - 4, r.y + r.h - 4, [[250, 200], [260, 210]], 2);
  await page.mouse.up();
  await settle(page);
  const fl = await sh(page, (t, c) => t.floatOf(c), C);
  check(near(fl?.w, F0.w + 260) && near(fl?.h, F0.h + 210) && !(await shielded(page)), `a floating tile's corner followed across B's page and kept the size (${JSON.stringify(fl)})`);
  await sh(page, (t, c) => t.closeTile(c), C);
  await settle(page);

  // ---- a pop-out window's corner across B's page ----
  await sh(page, (t, [a, c, f]) => t.setSpawnWindows([{ id: 'ds', from: a, src: c, reply() {}, title: 'pop-out', ...f, z: 3000 }]), [A, C, F0]);
  await waitSel(page, '.spawn', { state: 'attached' });
  r = await rectOf(page, '.spawn');
  await press(page, r.x + r.w - 4, r.y + r.h - 4, [[250, 200], [260, 210]], 2);
  await page.mouse.up();
  await settle(page);
  const sw = await sh(page, (t) => { const w = t.spawnWindows[0]; const r = t.query('.spawn')?.getBoundingClientRect(); return w && { w: w.w, h: w.h, dw: Math.round(r?.width ?? 0), dh: Math.round(r?.height ?? 0) }; });
  check(near(sw?.w, F0.w + 260) && near(sw?.h, F0.h + 210) && near(sw?.dw, F0.w + 260) && !(await shielded(page)), `a pop-out window's corner followed across B's page (${JSON.stringify(sw)})`);
  await sh(page, (t) => t.setSpawnWindows([]));
  await settle(page);

  // ---- a Tiled screen's divider jumped onto A's page ----
  await sh(page, (t) => t.addScreen('tiled'));
  await settle(page);
  await sh(page, (t, p) => t.openTile(p), A);
  await waitFor(page, (t) => t.openTiles.length === 1, null, { label: 'a tile open' });
  await sh(page, (t, p) => t.openTile(p), B);
  await waitFor(page, (t) => t.openTiles.length === 2, null, { label: 'two tiles' });
  await page.waitForSelector('.tiled .ttile', { state: 'attached' }).catch(() => {});
  await settle(page);
  const dv = await rectOf(page, '.tdiv[data-node=""]'), ta0 = await rectOf(page, `.ttile[data-path="${A}"]`);
  check(!!dv && !!ta0, 'a divider between the two tiles');
  if (dv && ta0) {
    const x = dv.x + dv.w / 2, y = dv.y + dv.h / 2;
    check(/^iframe/.test(await deepAt(page, x - 200, y) ?? ''), `the jump lands on A's page (${await deepAt(page, x - 200, y)})`);
    await press(page, x, y, [[-200, 0]]);
    await page.mouse.up();
    await settle(page);
    const ta1 = await rectOf(page, `.ttile[data-path="${A}"]`);
    check(near(ta1.w, ta0.w - 200, 3) && !(await shielded(page)), `the divider followed onto A's page (${Math.round(ta0.w)} → ${Math.round(ta1.w)})`);
    const pa2 = await pageRect(page, A);
    check(await clickReaches(page, A, pa2.x + 40, pa2.y + pa2.h / 2), "A's page takes a click again");
  }

  // tidy: the layout and the window's pref as they were
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-dragshield' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await ctx.request.delete(termPref);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { dragShield };
