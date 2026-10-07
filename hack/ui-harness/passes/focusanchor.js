// hack/ui-harness/passes/focusanchor.js — the focused tile holds its place
// (D191, shell/shell-anchor.js). Document mode with a tall stack of tiles
// whose documents the pass grows and shrinks: with a middle tile focused,
// tiles above and below change height and its title bar stays at the same
// viewport y (within 1 px) — at every resize the page saw, not only once it
// settled — and the top bar doesn't take the hold for reading; the last
// tile, focused by a click into its frame, grows and shrinks itself and
// keeps its title bar where it was (a bottom spacer makes the room, and goes
// once the page is scrolled up); scrolled fully out of view, the tile lets
// go. Then a phone's stacked canvas: a tile above the focused one grows and
// another closes, and the focused title bar stays; on the desktop canvas a
// focused card moved by a layout edit moves. Screenshots go to $OUT
// and, with ANCHOR_SHOTS=dir, to that directory too.
const path = require('path');
const { URL, OUT, fs, sleep, login, settle, sh, waitFor, openShell, usePersonalScreen, tileFrame, shot, checker } = require('../lib');

const WS = process.env.WS || '';
const NAMES = ['fa-a', 'fa-b', 'fa-c', 'fa-d', 'fa-e', 'fa-f'];
const TILES = NAMES.map((n) => `apps/${n}`);
const [A, B, C, D, E, F] = TILES;
const page0 = (n) => `<!doctype html><meta charset="utf-8"><title>${n}</title>
<style>body { margin: 0; padding: 12px 16px; font: 14px/1.5 system-ui, sans-serif; }
#pad { box-sizing: border-box; border: 1px dashed currentColor; padding: 8px; }</style>
<h1>${n}</h1><div id="pad" style="height: 420px">${n}: the harness sets this block's height</div>`;

async function ensureTiles(ctx, check) {
  for (const p of TILES) {
    const r = await ctx.request.post(`${URL}/api/xbin/create`, { data: { path: p, title: p } });
    check(r.ok() || r.status() === 409, `${p} exists (${r.status()})`);
    if (!WS) continue;
    const f = path.join(WS, p, 'index.html'); // wait for the scaffold's page, then replace it (docmode.js)
    for (let i = 0; i < 100 && !fs.existsSync(f); i++) await sleep(100);
    fs.writeFileSync(f, page0(p.slice(5)));
  }
}

const copyOut = (name) => {
  const dir = process.env.ANCHOR_SHOTS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  fs.copyFileSync(`${OUT}/${name}.png`, path.join(dir, `${name}.png`));
};
const snap = async (page, name) => { await shot(page, name, { fullPage: false }); copyOut(name); };

const headRect = (page, p) => sh(page, (t, p) => { const r = t.query(`.card[data-path="${p}"] .head`)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, p);
// a click on the title bar beside the name (no drag): the card becomes the active window
const clickHead = async (page, p) => { const r = await headRect(page, p); await page.mouse.click(r.x + r.w * 0.45, r.y + r.h / 2); await settle(page); };
const headY = (page, p) => sh(page, (t, p) => t.query(`.card[data-path="${p}"] .head`)?.getBoundingClientRect().top ?? null, p);
const scrollTop = (page) => sh(page, (t) => t.query('main').scrollTop);
const setScroll = (page, y) => sh(page, (t, y) => { t.query('main').scrollTop = y; }, y);
const frameH = (page, p) => sh(page, (t, p) => Math.round(t.frameFor(p)?.getBoundingClientRect().height ?? 0), p);
// the tile's document: its #pad block `h` px tall; then wait for its frame to follow
async function setPad(page, p, h) {
  const before = await frameH(page, p);
  const f = await tileFrame(page, p);
  await f.evaluate((h) => { document.getElementById('pad').style.height = `${h}px`; }, h);
  try { await waitFor(page, (t, a) => Math.abs((t.frameFor(a.p)?.getBoundingClientRect().height ?? 0) - a.before) > 20, { p, before }, { timeout: 5000, label: `${p} resized` }); } catch { /* the check says */ }
  await sleep(250); await settle(page);
}
// a ResizeObserver made after the anchor's: it runs in the same turn, after
// it — what the page paints — and records the head's y at every resize
const sample = (page, p) => sh(page, (t, p) => {
  window.faRO?.disconnect();
  window.faSamples = [];
  window.faRO = new ResizeObserver(() => { const h = t.query(`.card[data-path="${p}"] .head`); if (h) window.faSamples.push(h.getBoundingClientRect().top); });
  window.faRO.observe(t.query('bx-canvas')); window.faRO.observe(t.query('main'));
}, p);
const samples = (page) => page.evaluate(() => window.faSamples ?? []);
const spread = (ys, y0) => ys.reduce((m, y) => Math.max(m, Math.abs(y - y0)), 0);

async function focusAnchor(browser) {
  const { check, skip, done } = checker('focus-anchor');
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await ensureTiles(ctx, check);
  if (!WS) { skip('no $WS: the fixture tiles keep their scaffold pages'); await ctx.close(); done(); return; }
  await openShell(page);
  await usePersonalScreen(page);
  const id = await sh(page, (t) => t.addScreen());
  await settle(page);
  for (const p of TILES) await sh(page, (t, p) => t.openTile(p), p);
  await waitFor(page, (t, n) => t.openTiles.length === n, TILES.length, { label: 'tiles open' });
  await sh(page, (t, id) => t.setScreenMode(id, 'doc'), id);
  await page.waitForSelector('.doc .dcell', { timeout: 10000, state: 'attached' });
  for (const p of TILES) await tileFrame(page, p);
  try { await waitFor(page, (t, ps) => ps.every((p) => Math.abs((t.frameFor(p)?.getBoundingClientRect().height ?? 0) - 480) > 2), TILES, { timeout: 15000, label: 'the tiles fit their documents' }); } catch { /* checked below */ }
  const order = (await sh(page, (t) => t.docRows())).flatMap((r) => r.paths);
  check(order.join() === TILES.join(), `the stack reads ${order.join(', ')}`);
  const hc = await frameH(page, C);
  check(hc > 300 && hc < 600, `the tiles fit their documents (${C}: ${hc} px)`);

  // ---- a middle tile focused; the tiles above and below change ----
  await sh(page, (t, p) => t.query(`.dcell[data-path="${p}"]`).scrollIntoView({ block: 'start' }), C);
  await setScroll(page, (await scrollTop(page)) - 240);
  await page.mouse.move(700, 500); await sleep(300);
  await clickHead(page, C);
  let a = await sh(page, (t) => t.anchor);
  let y0;
  check(a?.path === C && a.held, `clicking ${C}'s title bar focuses it and holds it (${JSON.stringify(a)})`);
  y0 = await headY(page, C);
  const bar0 = (await sh(page, (t) => t.topBar)).hidden;
  await snap(page, 'focusanchor-1-held');
  await sample(page, C);
  const s0 = await scrollTop(page);
  for (const [p, h, what] of [[A, 900, 'the first tile grows 480 px'], [B, 120, 'the second shrinks 300 px'], [A, 300, 'the first shrinks 600 px'], [B, 1000, 'the second grows 880 px'],
    [E, 1400, 'a tile below grows'], [D, 100, 'the tile just below shrinks']]) {
    await setPad(page, p, h);
    const y = await headY(page, C), ys = await samples(page);
    check(Math.abs(y - y0) <= 1 && spread(ys, y0) <= 1, `${what}: ${C}'s title bar stays at ${Math.round(y0)} (now ${y?.toFixed(1)}; ${ys.length} resizes, worst ${spread(ys, y0).toFixed(1)} px off)`);
    if (p === A && h === 900) await snap(page, 'focusanchor-2-above-grew');
  }
  check(await scrollTop(page) !== s0, `the page scrolled to hold it (${s0} → ${await scrollTop(page)})`);
  check((await sh(page, (t) => t.topBar)).hidden === bar0, 'the top bar takes the hold for no scroll of the person\'s');
  // a tile above closes, and opens again (at the end of the page)
  await sh(page, (t, p) => t.closeTile(p), B); await sleep(400); await settle(page);
  let y = await headY(page, C);
  check(Math.abs(y - y0) <= 1, `${B} closing: ${C}'s title bar stays (${y?.toFixed(1)} vs ${y0.toFixed(1)})`);
  await sh(page, (t, p) => t.openTile(p), B); await sleep(600); await settle(page);
  y = await headY(page, C);
  check(Math.abs(y - y0) <= 1, `${B} opening again: ${C}'s title bar stays (${y?.toFixed(1)})`);
  // the person scrolls: the hold moves with the page
  await page.mouse.move(700, 500);
  await page.mouse.wheel(0, 200); await sleep(400);
  y0 = await headY(page, C);
  await setPad(page, A, 700);
  y = await headY(page, C);
  check(Math.abs(y - y0) <= 1, `after the person scrolls, the new place is held (${y?.toFixed(1)} vs ${y0.toFixed(1)})`);

  // ---- the last tile, focused from inside its frame, grows and shrinks ----
  const last = (await sh(page, (t) => t.docRows())).flatMap((r) => r.paths).at(-1);
  await setPad(page, last, 500);
  await setScroll(page, 1e7); await sleep(300); await settle(page);
  const fr = await sh(page, (t, p) => { const r = t.frameFor(p).getBoundingClientRect(); return { x: r.x, y: r.y, h: r.height }; }, last);
  await page.mouse.click(fr.x + 300, fr.y + Math.min(fr.h - 20, 200));
  await sleep(300);
  a = await sh(page, (t) => t.anchor);
  check(a?.path === last && a.held, `a click into ${last}'s document focuses its card (${JSON.stringify(a)})`);
  y0 = await headY(page, last);
  await sample(page, last);
  await setPad(page, last, 1100);
  y = await headY(page, last);
  check(Math.abs(y - y0) <= 1, `the last tile grows: its title bar stays (${y?.toFixed(1)} vs ${y0.toFixed(1)})`);
  await sample(page, last);
  await snap(page, 'focusanchor-3-last-before');
  await setPad(page, last, 150); // shorter than the page below it: the page would end above the title bar's place
  y = await headY(page, last);
  let ys = await samples(page);
  a = await sh(page, (t) => t.anchor);
  check(Math.abs(y - y0) <= 1 && spread(ys, y0) <= 1, `the last tile shrinks 950 px: its title bar stays (${y?.toFixed(1)} vs ${y0.toFixed(1)}; worst ${spread(ys, y0).toFixed(1)} px)`);
  check(a?.spacer > 0, `a bottom spacer makes the room (${a?.spacer} px)`);
  await snap(page, 'focusanchor-4-last-shrunk');
  await page.mouse.move(700, 500);
  for (let i = 0; i < 8; i++) { await page.mouse.wheel(0, -300); await sleep(60); }
  await sleep(400);
  a = await sh(page, (t) => t.anchor);
  check(a?.spacer === 0, `scrolled up past it, the spacer goes (${a?.spacer})`);
  // read near its foot (the title bar above the window), it shrinks: the
  // page doesn't hold the title bar over an empty window — what is left of
  // the tile shows
  await setScroll(page, 1e7); await sleep(300); // a frame out of view doesn't render, so never reports its height: in view first
  await setPad(page, last, 1400);
  await setScroll(page, 1e7); await sleep(300); await settle(page);
  y0 = await headY(page, last);
  await setPad(page, last, 150);
  const cr = await sh(page, (t, p) => { const r = t.query(`.card[data-path="${p}"]`).getBoundingClientRect(), m = t.query('main').getBoundingClientRect(); return { top: r.top, bottom: r.bottom, vt: m.top, vb: m.bottom }; }, last);
  a = await sh(page, (t) => t.anchor);
  check(y0 < 0 && cr.bottom > cr.vt && cr.top < cr.vb && a?.spacer === 0, `a tall tile read near its foot shrinks: it stays in view, no spacer (title bar was at ${y0?.toFixed(0)}; card ${cr.top.toFixed(0)}–${cr.bottom.toFixed(0)} in ${cr.vt.toFixed(0)}–${cr.vb.toFixed(0)}; spacer ${a?.spacer})`);

  // ---- scrolled fully out of view: it lets go ----
  await setScroll(page, 0); await sleep(300);
  a = await sh(page, (t) => t.anchor);
  check(a && !a.held, `scrolled out of view, ${last} is no longer held (${JSON.stringify(a)})`);
  const st = await scrollTop(page);
  await setPad(page, A, 1000);
  check(await scrollTop(page) === st, `a change then doesn't move the page (${st} → ${await scrollTop(page)})`);

  // ---- a phone's canvas: stacked cards ----
  await page.setViewportSize({ width: 400, height: 800 });
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await page.waitForSelector('.canvas .gtile', { timeout: 10000, state: 'attached' });
  await sleep(500); await settle(page);
  const stack = await sh(page, (t) => [...t.query('.canvas').querySelectorAll('.gtile')]
    .map((g) => ({ p: g.dataset.path, y: g.getBoundingClientRect().top })).sort((x, y) => x.y - y.y).map((g) => g.p));
  const [up1, up2, mid] = [stack[0], stack[1], stack[3]];
  await sh(page, (t, p) => t.query(`.gtile[data-path="${p}"]`).scrollIntoView({ block: 'start' }), mid);
  await setScroll(page, (await scrollTop(page)) - 150); await sleep(300);
  await clickHead(page, mid);
  a = await sh(page, (t) => t.anchor);
  check(a?.path === mid && a.held, `on a phone, tapping ${mid}'s title bar holds it (${JSON.stringify(a)})`);
  y0 = await headY(page, mid);
  await sample(page, mid);
  await sh(page, (t, p) => t.setGeom((tiles) => tiles.map((o) => (o.path === p ? { ...o, h: o.h + 192 } : o))), up1);
  await sleep(400); await settle(page);
  y = await headY(page, mid);
  check(Math.abs(y - y0) <= 1 && spread(await samples(page), y0) <= 1, `a card above grows: ${mid}'s title bar stays (${y?.toFixed(1)} vs ${y0.toFixed(1)})`);
  await sh(page, (t, p) => t.closeTile(p), up2);
  await sleep(400); await settle(page);
  y = await headY(page, mid);
  check(Math.abs(y - y0) <= 1, `a card above closes: ${mid}'s title bar stays (${y?.toFixed(1)} vs ${y0.toFixed(1)})`);
  await snap(page, 'focusanchor-5-phone');

  // ---- the desktop canvas: a card sits where its x/y say ----
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(400); await settle(page);
  await sh(page, (t, p) => t.query(`.gtile[data-path="${p}"]`).scrollIntoView({ block: 'center' }), mid);
  await sleep(300);
  await clickHead(page, mid);
  y0 = await headY(page, mid);
  const k = await sh(page, (t) => t.gridScale);
  await sh(page, (t, p) => t.setGeom((tiles) => tiles.map((o) => (o.path === p ? { ...o, y: o.y + 96 } : o))), mid);
  await sleep(400); await settle(page);
  y = await headY(page, mid);
  check(Math.abs(y - y0 - 96 * k) <= 1, `the focused card's own move (96 px down) is a move, not held (${y0.toFixed(1)} → ${y?.toFixed(1)})`);

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-focusanchor' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { focusAnchor };
