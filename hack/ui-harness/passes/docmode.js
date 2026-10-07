// hack/ui-harness/passes/docmode.js — Document mode (D187): a screen
// switched to Document reads as rows (one tile each, then a 2-wide row made
// by dragging a card beside another and a 4-wide one from the tile menu);
// a tall tile grows to its content with no scrollbar inside it, a tile
// pinned to its viewport settles at 480 px and takes a fixed height from
// its handle; the top bar hides on a scroll down and comes back on a scroll
// up and at the top edge; a phone stacks the rows in reading order; and
// switching back to Canvas shows every tile exactly where it was. The
// screen tabs sit in the top bar on a wide screen and in a row of their own
// on a phone. Run it in both themes (HARNESS_THEME=light); screenshots go
// to $OUT and, with DOC_SHOTS=dir, to that directory too.
const path = require('path');
const { URL, OUT, fs, sleep, login, settle, sh, waitFor, openShell, usePersonalScreen, tileFrame, shot, shotEl, checker, THEME } = require('../lib');

const WS = process.env.WS || '';
const TALL = 'apps/doc-tall', VH = 'apps/doc-vh';
const TILES = [TALL, VH, 'apps/pinned', 'apps/offline', 'apps/reloady', 'apps/deployy', 'apps/leads', 'apps/linky'];
const PAGES = {
  [TALL]: `<!doctype html><meta charset="utf-8"><title>doc-tall</title>
<style>body { margin: 0; padding: 16px 20px; font: 14px/1.5 system-ui, sans-serif; }</style>
<h1>A tall document</h1>
${Array.from({ length: 40 }, (_, i) => `<p>Paragraph ${i + 1}: the tile grows to its content in Document mode, so the page scrolls, not the tile.</p>`).join('\n')}
<p id="end">the end</p>`,
  [VH]: `<!doctype html><meta charset="utf-8"><title>doc-vh</title>
<style>html, body { height: 100%; margin: 0; } body { display: flex; flex-direction: column; font: 14px/1.5 system-ui, sans-serif; }
main { flex: 1; overflow: auto; padding: 12px; }</style>
<main>${Array.from({ length: 60 }, (_, i) => `<div>line ${i + 1} of a document pinned to its viewport (a terminal, a chat)</div>`).join('')}</main>`,
};

async function ensureTiles(ctx, check) {
  for (const p of [TALL, VH]) {
    const r = await ctx.request.post(`${URL}/api/xbin/create`, { data: { path: p, title: p } });
    check(r.ok() || r.status() === 409, `${p} exists (${r.status()})`);
    if (WS) fs.writeFileSync(path.join(WS, p, 'index.html'), PAGES[p]);
  }
}

const copyOut = (name) => {
  const dir = process.env.DOC_SHOTS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  fs.copyFileSync(`${OUT}/${name}.png`, path.join(dir, `${name}.png`));
};
const snap = async (page, name) => { await shot(page, name, { fullPage: false }); copyOut(name); };

const rows = (page) => sh(page, (t) => t.docRows());
const shape = (rs) => rs.map((r) => `${r.cols}:${r.paths.join(',')}`).join(' | ');
const rectIn = (page, sel) => sh(page, (t, sel) => { const r = t.query(sel)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
const mainScroll = (page, y) => sh(page, (t, y) => { t.query('main').scrollTop = y; }, y);

async function docMode(browser) {
  const { check, skip, done } = checker(`doc-mode-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await ensureTiles(ctx, check);
  if (!WS) skip('no $WS: the tall and viewport-pinned tiles keep their scaffold pages');
  await openShell(page);
  await usePersonalScreen(page);

  // the tabs fold into the top bar on a wide screen
  check(!!(await rectIn(page, '.top .tabs')), 'the screen tabs sit in the top bar');
  const id = await sh(page, (t) => t.addScreen());
  await settle(page);
  for (const p of TILES) await sh(page, (t, p) => t.openTile(p), p);
  await waitFor(page, (t, n) => t.openTiles.length === n, TILES.length, { label: 'tiles open' });
  await sh(page, (t) => t.flushSave());
  const canvas = await sh(page, (t) => t.openTiles.map(({ path, x, y, w, h, float }) => ({ path, x, y, w, h, float: float ?? null })));
  await snap(page, `docmode-tabs-folded-${THEME}`);
  await shotEl(page, '.top', `docmode-topbar-${THEME}`); copyOut(`docmode-topbar-${THEME}`);

  // Document mode: one tile per row, in reading order
  await sh(page, (t, id) => t.setScreenMode(id, 'doc'), id);
  await waitSel(page);
  let rs = await rows(page);
  check(rs.length === TILES.length && rs.every((r) => r.cols === 1 && r.paths.length === 1), `one tile per row (${shape(rs)})`);
  check(await sh(page, (t) => t.screenMode) === 'doc', 'the screen says Document');
  check((await sh(page, (t) => t.topBar)).hidden, 'the top bar starts out of the way');
  check((await sh(page, (t) => t.query('.top')?.className ?? '')).includes('away'), 'the bar is drawn away');

  // a 2-wide row: drag offline's title bar onto pinned's right edge
  const order = rs.map((r) => r.paths[0]);
  const [a, b] = [order[2], order[3]];
  const head = await rectIn(page, `.dcell[data-path="${b}"] .head`);
  await sh(page, (t, a) => t.query(`.dcell[data-path="${a}"]`).scrollIntoView({ block: 'start' }), a);
  await settle(page);
  const head2 = await rectIn(page, `.dcell[data-path="${b}"] .head`);
  const tgt = await rectIn(page, `.dcell[data-path="${a}"] .card`);
  if (head2 && tgt) {
    await page.mouse.move(head2.x + 60, head2.y + head2.h / 2);
    await page.mouse.down();
    await page.mouse.move(head2.x + 80, head2.y + 10, { steps: 3 });
    await page.mouse.move(tgt.x + tgt.w * 0.9, tgt.y + Math.min(tgt.h / 2, 100), { steps: 8 });
    const mark = await sh(page, (t) => !!t.query('.dmark.v'));
    check(mark, 'a drop bar shows beside the card while dragging');
    await page.mouse.up();
    await settle(page);
  }
  rs = await rows(page);
  check(rs.some((r) => r.cols === 2 && r.paths.join() === `${a},${b}`), `dragging ${b} beside ${a} makes a 2-wide row (${shape(rs)}; head ${JSON.stringify(head)})`);

  // a 4-wide row from the tile menu: the next tiles are pulled up into it
  const four = rs[rs.findIndex((r) => r.cols === 2) + 1]?.paths[0];
  await sh(page, (t, p) => { const row = t.tileMenuItems(p).find((x) => x.label === 'Row'); row.items.find((x) => x.label === '4 columns').action(); }, four);
  await settle(page);
  rs = await rows(page);
  check(rs.some((r) => r.cols === 4 && r.paths.length === 4), `Row ▸ 4 columns pulls three tiles up (${shape(rs)})`);
  // its cards sit side by side on one line, a quarter of the page each
  const r4 = rs.find((r) => r.cols === 4);
  const rects = [];
  for (const p of r4?.paths ?? []) rects.push(await rectIn(page, `.dcell[data-path="${p}"]`));
  check(rects.length === 4 && rects.every((r) => Math.abs(r.y - rects[0].y) < 1) && rects[1].x > rects[0].x + 100, `the 4-wide row is one line (${JSON.stringify(rects.map((r) => r && [Math.round(r.x), Math.round(r.y), Math.round(r.w)]))})`);

  // a tall tile grows: no scrollbar inside it
  await sh(page, (t, p) => t.query(`.dcell[data-path="${p}"]`).scrollIntoView({ block: 'start' }), TALL);
  let grown = null;
  try {
    await waitFor(page, (t, p) => (t.frameFor(p)?.getBoundingClientRect().height ?? 0) > 1000, TALL, { timeout: 15000, label: 'the tall tile grows' });
  } catch { /* checked below */ }
  const tf = await tileFrame(page, TALL).catch(() => null);
  if (tf) grown = await tf.evaluate(() => ({ sh: document.scrollingElement.scrollHeight, ch: document.scrollingElement.clientHeight })).catch(() => null);
  const th = await sh(page, (t, p) => Math.round(t.frameFor(p)?.getBoundingClientRect().height ?? 0), TALL);
  check(th > 1000 && grown && grown.sh <= grown.ch + 1, `the tall tile is as tall as its document (frame ${th} px; inside ${JSON.stringify(grown)})`);
  await snap(page, `docmode-grown-${THEME}`);

  // a viewport-pinned tile settles at 480 px; its handle fixes a height; a double-click fits again
  await sh(page, (t, p) => t.query(`.dcell[data-path="${p}"]`).scrollIntoView({ block: 'center' }), VH);
  await sleep(800);
  const vh0 = await sh(page, (t, p) => Math.round(t.frameFor(p)?.getBoundingClientRect().height ?? 0), VH);
  check(Math.abs(vh0 - 480) <= 2, `the viewport-pinned tile settles at 480 px (${vh0})`);
  const dh = await rectIn(page, `.dcell[data-path="${VH}"] .dh`);
  if (dh) {
    await page.mouse.move(dh.x + dh.w / 2, dh.y + dh.h / 2);
    await page.mouse.down();
    await page.mouse.move(dh.x + dh.w / 2, dh.y + dh.h / 2 + 100, { steps: 5 });
    await page.mouse.up();
    await settle(page);
  }
  const fixed = await sh(page, (t, p) => t.openTiles.find((o) => o.path === p)?.doc?.h ?? null, VH);
  const vh1 = await sh(page, (t, p) => Math.round(t.frameFor(p)?.getBoundingClientRect().height ?? 0), VH);
  check(fixed && Math.abs(fixed - 580) <= 3 && Math.abs(vh1 - fixed) <= 2, `the handle sets a fixed height (doc.h ${fixed}, frame ${vh1})`);
  const dh2 = await rectIn(page, `.dcell[data-path="${VH}"] .dh`);
  if (dh2) await page.mouse.dblclick(dh2.x + dh2.w / 2, dh2.y + dh2.h / 2);
  await sleep(300);
  check(!(await sh(page, (t, p) => t.openTiles.find((o) => o.path === p)?.doc?.h ?? null, VH)), 'a double-click on the handle fits the content again');

  // the top bar: hides on a scroll down, back on a scroll up and at the top edge
  await page.mouse.move(700, 500);
  await mainScroll(page, 0); await sleep(150);
  await mainScroll(page, 700); await sleep(250);
  check((await sh(page, (t) => t.topBar)).hidden, 'a scroll down keeps the bar away');
  await snap(page, `docmode-top-hidden-${THEME}`);
  await mainScroll(page, 400); await sleep(250);
  check(!(await sh(page, (t) => t.topBar)).hidden, 'a scroll up brings the bar back');
  await mainScroll(page, 1200); await sleep(250);
  check((await sh(page, (t) => t.topBar)).hidden, 'scrolling down again sends it away');
  await page.mouse.move(700, 3); await sleep(300);
  check(!(await sh(page, (t) => t.topBar)).hidden, 'the pointer at the top edge brings it back');
  await snap(page, `docmode-top-revealed-${THEME}`);
  // the rows: a 2-wide and the 4-wide in one view
  const row2 = rs.find((r) => r.cols === 2);
  if (row2) await sh(page, (t, p) => t.query(`.dcell[data-path="${p}"]`).scrollIntoView({ block: 'start' }), row2.paths[0]);
  await page.mouse.move(700, 500); await sleep(300);
  await snap(page, `docmode-rows-${THEME}`);

  // a phone stacks every row, in reading order
  await page.setViewportSize({ width: 400, height: 800 });
  await sleep(400);
  const flat = (await rows(page)).flatMap((r) => r.paths);
  const ph = [];
  for (const p of flat) ph.push(await rectIn(page, `.dcell[data-path="${p}"]`));
  check(ph.every((r, i) => r && Math.abs(r.x - ph[0].x) < 1 && (i === 0 || r.y > ph[i - 1].y)), `a phone stacks the rows in reading order (${ph.map((r) => r && Math.round(r.y)).join(' < ')})`);
  check(!(await rectIn(page, '.top .tabs')) && !!(await rectIn(page, '.tabs')), 'on a phone the tabs are a row of their own');
  await snap(page, `docmode-phone-${THEME}`);
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(300);

  // back to Canvas: every tile exactly where it was
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await settle(page);
  const back = await sh(page, (t) => t.openTiles.map(({ path, x, y, w, h, float }) => ({ path, x, y, w, h, float: float ?? null })));
  const key = (l) => JSON.stringify([...l].sort((p, q) => (p.path < q.path ? -1 : 1)));
  check(key(back) === key(canvas), 'Canvas shows every tile at its old place and size');
  check(!!(await rectIn(page, '.canvas')) && !(await sh(page, (t) => t.topBar)).on, 'the canvas is back, the bar in its place');

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-docmode' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

// the doc view rendered (a waitSel on the shadow DOM)
async function waitSel(page) {
  await page.waitForSelector('.doc .dcell', { timeout: 10000, state: 'attached' });
  await settle(page);
}

module.exports = { docMode };
