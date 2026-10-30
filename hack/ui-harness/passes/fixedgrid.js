// hack/ui-harness/passes/fixedgrid.js — Fixed width (D193): a screen whose
// canvas runs past column 48 switched to Fixed width keeps the tiles that
// fit where they were and packs the rest in below; the 48 columns span the
// pane (no sideways scroll, nothing past the right edge) and stretch with
// the window; a drag stops at the right edge and a push there goes down; a
// tile opened there lands inside the columns and every tile gets its place
// (`fixed`); a reload keeps it; a phone stacks the cards; and switching
// back to Canvas shows every tile at its old place and size. Run it in both
// themes (HARNESS_THEME=light); screenshots go to $OUT.
const { URL, sleep, login, settle, sh, waitFor, openShell, usePersonalScreen, shot, checker, THEME } = require('../lib');

const FIXED_W = 2304;
// canvas places: two tiles past column 48 (x 2400 and 2880), three inside
const PLACES = {
  'apps/pinned': [0, 0, 576, 384], 'apps/offline': [576, 0, 576, 384], 'apps/leads': [0, 384, 1152, 384],
  'apps/reloady': [2400, 0, 576, 384], 'apps/deployy': [2880, 384, 576, 384],
};
const TILES = Object.keys(PLACES);

const rectIn = (page, sel) => sh(page, (t, sel) => { const r = t.query(sel)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
const geom = (page) => sh(page, (t) => t.openTiles.map(({ path, x, y, w, h }) => ({ path, x, y, w, h })).sort((a, b) => (a.path < b.path ? -1 : 1)));
const fixedOf = (page) => sh(page, (t) => Object.fromEntries(t.openTiles.map((o) => [o.path, o.fixed ?? null])));
const card = (page, p) => rectIn(page, `.gtile[data-path="${p}"]`);
// the seed's decision strip sits above the canvas: bring the canvas (or a card) into view first
const into = (page, sel, block = 'start') => sh(page, (t, [sel, block]) => { t.query(sel)?.scrollIntoView({ block }); }, [sel, block]);
const snap = async (page, name) => { await into(page, '.canvas'); await settle(page); await shot(page, name, { fullPage: false }); };

// the cards' right edges and the pane: nothing past the canvas, no sideways scroll
async function inside(page) {
  const cv = await rectIn(page, '.canvas');
  const rights = [];
  for (const p of TILES) { const r = await card(page, p); if (r) rights.push(r.x + r.w); }
  const scroll = await sh(page, (t) => { const m = t.query('main'); return { sw: m.scrollWidth, cw: m.clientWidth }; });
  return { ok: !!cv && rights.length === TILES.length && rights.every((x) => x <= cv.x + cv.w + 0.5) && scroll.sw <= scroll.cw,
    note: `canvas ${cv && Math.round(cv.x + cv.w)}, rights ${rights.map(Math.round).join(',')}, main ${scroll.sw}/${scroll.cw}` };
}

// a tile's share of the 48 columns: (its width + the gutter) / the canvas width
async function share(page, p) {
  const cv = await rectIn(page, '.canvas'), r = await card(page, p);
  return cv && r ? (r.w + 8) / cv.w : 0;
}

// drag a card's title bar so its left edge lands at logical x (Fixed width's px), same row
async function dragTo(page, p, lx) {
  await into(page, `.gtile[data-path="${p}"]`, 'center');
  await settle(page);
  const head = await rectIn(page, `.gtile[data-path="${p}"] .head`), cv = await rectIn(page, '.canvas'), r = await card(page, p);
  if (!head || !cv || !r) return;
  const kx = cv.w / FIXED_W, off = 30, y = head.y + head.h / 2;
  await page.mouse.move(r.x + off, y);
  await page.mouse.down();
  await page.mouse.move(r.x + off + 20, y, { steps: 2 });
  await page.mouse.move(cv.x + lx * kx + off, y, { steps: 8 });
  await page.mouse.up();
  await settle(page);
}

async function fixedGrid(browser) {
  const { check, done } = checker(`fixed-grid-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await openShell(page);
  await usePersonalScreen(page);
  const id = await sh(page, (t) => t.addScreen('canvas'));
  await settle(page);
  for (const p of TILES) await sh(page, (t, p) => t.openTile(p), p);
  await waitFor(page, (t, n) => t.openTiles.length === n, TILES.length, { label: 'tiles open' });
  await sh(page, (t, P) => t.setGeom((tiles) => tiles.map((o) => (P[o.path] ? { ...o, x: P[o.path][0], y: P[o.path][1], w: P[o.path][2], h: P[o.path][3] } : o))), PLACES);
  await sh(page, (t) => t.flushSave());
  await settle(page);
  const canvas = await geom(page);

  // Fixed width: the tiles that fit stay, the two past column 48 go below
  await sh(page, (t, id) => t.setScreenMode(id, 'fixed'), id);
  await waitFor(page, (t) => !!t.query('.canvas.fixed'), null, { label: 'the fixed canvas' });
  await settle(page);
  check(await sh(page, (t) => t.screenMode) === 'fixed', 'the screen says Fixed width');
  check(!!(await rectIn(page, '.canvas.fixed .fdots')), 'the dot field is drawn on the stretched columns');
  let at = await inside(page);
  check(at.ok, `nothing past the right edge, no sideways scroll (${at.note})`);
  const cv = await rectIn(page, '.canvas'), main = await rectIn(page, 'main');
  check(cv && main && Math.abs(cv.w - (main.w - 28)) <= 2, `the 48 columns span the pane (${cv && Math.round(cv.w)} of ${main && Math.round(main.w)})`);
  const pin = await card(page, 'apps/pinned'), off = await card(page, 'apps/offline'), rel = await card(page, 'apps/reloady'), dep = await card(page, 'apps/deployy'), leads = await card(page, 'apps/leads');
  check(pin && off && Math.abs(pin.y - off.y) < 1 && off.x > pin.x + pin.w && Math.abs((await share(page, 'apps/pinned')) - 0.25) < 0.01,
    `a 12-column tile is a quarter of the pane, its neighbour beside it (${pin && Math.round(pin.w)} px, share ${(await share(page, 'apps/pinned')).toFixed(3)})`);
  check(rel && dep && leads && rel.y >= leads.y + leads.h && Math.abs(rel.y - dep.y) < 1 && dep.x > rel.x,
    `the tiles past column 48 are packed in below, side by side (reloady ${rel && [Math.round(rel.x), Math.round(rel.y)]}, deployy ${dep && [Math.round(dep.x), Math.round(dep.y)]})`);
  check(JSON.stringify(await geom(page)) === JSON.stringify(canvas), 'switching writes nothing: the canvas places are as they were');
  await snap(page, `fixedgrid-wide-${THEME}`);

  // the window narrower: the columns shrink with it, rows keep their height
  await page.setViewportSize({ width: 1000, height: 900 });
  await sleep(400);
  at = await inside(page);
  const s2 = await share(page, 'apps/pinned'), pin2 = await card(page, 'apps/pinned');
  check(at.ok && Math.abs(s2 - 0.25) < 0.01 && pin2 && pin && pin2.w < pin.w - 50 && Math.abs(pin2.h - pin.h) < 1,
    `a narrower window stretches the columns, not the rows (${Math.round(pin.w)} → ${pin2 && Math.round(pin2.w)} px wide, ${pin2 && Math.round(pin2.h)} tall; share ${s2.toFixed(3)}; ${at.note})`);
  await snap(page, `fixedgrid-narrow-${THEME}`);
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(400);

  // a drag past the right edge (two columns past where the tile still fits)
  // stops at it; the first change writes every tile's place
  await dragTo(page, 'apps/offline', FIXED_W - 576 + 96);
  let fx = await fixedOf(page);
  check(fx['apps/offline'] && fx['apps/offline'].x + fx['apps/offline'].w === FIXED_W && fx['apps/offline'].y === 0,
    `a drag past the right edge stops at it (${JSON.stringify(fx['apps/offline'])})`);
  check(TILES.every((p) => fx[p]), `the first change writes every tile's place (${TILES.filter((p) => !fx[p]).join(', ') || 'all'})`);
  check(JSON.stringify(await geom(page)) === JSON.stringify(canvas), 'the drag leaves the canvas places alone');
  // a push to the right at the edge goes down instead
  await dragTo(page, 'apps/pinned', 1440);
  fx = await fixedOf(page);
  const o = fx['apps/offline'];
  check(fx['apps/pinned']?.x === 1440 && o && o.y >= 384 && o.x + o.w <= FIXED_W,
    `a push right at the edge wraps down (pinned ${JSON.stringify(fx['apps/pinned'])}, offline ${JSON.stringify(o)})`);
  at = await inside(page);
  check(at.ok, `still nothing past the right edge (${at.note})`);
  await snap(page, `fixedgrid-pushed-${THEME}`);

  // a tile opened here lands inside the columns, clear of the others
  await sh(page, (t) => t.openTile('apps/linky'));
  await waitFor(page, (t) => t.openTiles.some((o) => o.path === 'apps/linky'), null, { label: 'linky open' });
  fx = await fixedOf(page);
  const n = fx['apps/linky'];
  const clear = n && Object.entries(fx).every(([p, r]) => p === 'apps/linky' || !r || n.x >= r.x + r.w || r.x >= n.x + n.w || n.y >= r.y + r.h || r.y >= n.y + n.h);
  check(n && n.x + n.w <= FIXED_W && clear, `a tile opened here lands inside the columns, clear of the others (${JSON.stringify(n)})`);
  await sh(page, (t) => t.closeTile('apps/linky'));
  await settle(page);

  // a reload keeps the places
  await sh(page, (t) => t.flushSave());
  const before = await fixedOf(page);
  await page.reload();
  await openShell(page);
  await sh(page, (t, id) => t.setScreen(id), id);
  await waitFor(page, (t) => !!t.query('.canvas.fixed'), null, { label: 'the fixed canvas after a reload' });
  check(JSON.stringify(await fixedOf(page)) === JSON.stringify(before) && await sh(page, (t) => t.screenMode) === 'fixed', 'a reload keeps the mode and every place');

  // a phone stacks the cards
  await page.setViewportSize({ width: 400, height: 800 });
  await sleep(400);
  const ph = [];
  for (const p of TILES) ph.push(await card(page, p));
  const ys = ph.map((r) => r && Math.round(r.y)).sort((a, b) => a - b);
  check(ph.every((r) => r && Math.abs(r.x - ph[0].x) < 1) && new Set(ys).size === ys.length, `a phone stacks the cards (${ys.join(' < ')})`);
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(300);

  // back to Canvas: every tile exactly where it was
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await settle(page);
  check(JSON.stringify(await geom(page)) === JSON.stringify(canvas), 'Canvas shows every tile at its old place and size');
  check(!(await rectIn(page, '.canvas.fixed')), 'the canvas is back');

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-fixedgrid' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { fixedGrid };
