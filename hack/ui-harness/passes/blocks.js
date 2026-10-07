// hack/ui-harness/passes/blocks.js — screen blocks (D192): a heading and a
// text block put on a personal screen from the canvas menu with no tile,
// typed in place, edited again by a double-click, the heading's level from
// its menu, moved by the grip (pushing a tile out of the way) and resized
// by its corner; the Markdown rendered safely (raw HTML as text, links in a
// new tab with no opener); a reload keeps them; Document mode shows them
// as rows; a block deleted from its menu goes; and an org screen's draft
// takes a heading, publishes it with the tiles (a new revision), keeps it
// through a tiles-only save (an older shell) and shows it read-only in
// view mode. Run it in both themes (HARNESS_THEME=light); screenshots go to
// $OUT and, with BLOCK_SHOTS=dir, to that directory too.
const path = require('path');
const { URL, OUT, fs, sleep, settle, sh, waitFor, openShell, usePersonalScreen, login, shot, checker, THEME } = require('../lib');

const TILES = ['apps/pinned', 'apps/offline'];
const copyOut = (name) => {
  const dir = process.env.BLOCK_SHOTS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  fs.copyFileSync(`${OUT}/${name}.png`, path.join(dir, `${name}.png`));
};
const snap = async (page, name) => { await shot(page, name, { fullPage: false }); copyOut(name); };
const rectOf = (page, sel) => sh(page, (t, sel) => { const r = t.query(sel)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
const blocks = (page) => sh(page, (t) => t.blocks());
const blockSel = (id) => `[data-block="${id}"]`;
const overlap = (a, b) => a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y;

// the canvas menu at a point of the empty canvas, then one of its lines
async function canvasMenu(page, x, y, label) {
  await page.mouse.click(x, y, { button: 'right' });
  await page.locator('bx-menu .it', { hasText: label }).first().click();
  await settle(page);
}
// a block's ⋯ menu, then a line (and a submenu line)
async function blockMenu(page, id, label, sub) {
  const r = await rectOf(page, blockSel(id));
  await page.mouse.move(r.x + r.w / 2, r.y + r.h / 2);
  await page.locator(`${blockSel(id)} .btools button`).click();
  await page.locator('bx-menu .it', { hasText: label }).first().click();
  if (sub) await page.locator('bx-menu .it', { hasText: sub }).first().click();
  await settle(page);
}
// drag from (x, y) by (dx, dy) in steps
async function drag(page, x, y, dx, dy) {
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx / 2, y + dy / 2, { steps: 4 });
  await page.mouse.move(x + dx, y + dy, { steps: 6 });
  await page.mouse.up();
  await settle(page);
}

async function blocksPass(browser) {
  const { check, skip, done } = checker(`blocks-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await openShell(page);
  await usePersonalScreen(page);
  const id = await sh(page, (t) => t.addScreen());
  await settle(page);
  for (const p of TILES) await sh(page, (t, p) => t.openTile(p), p);
  await waitFor(page, (t, n) => t.openTiles.length === n, TILES.length, { label: 'tiles open' });
  // the two tiles side by side at the top (a known layout to push against)
  await sh(page, (t) => t.setGeom((tiles) => tiles.map((o, i) => ({ ...o, x: i * 576, y: 192, w: 576, h: 384 }))));
  await settle(page);

  // ---- a heading from the canvas menu, typed in place ----
  // the grant strip sits above the canvas: scroll the canvas's top into view
  const canvasTop = async () => { await sh(page, (t) => t.query('.canvas')?.scrollIntoView({ block: 'start' })); await settle(page); return rectOf(page, '.canvas'); };
  let cv = await canvasTop();
  await canvasMenu(page, cv.x + 30, cv.y + 20, 'Add heading');
  let bl = await blocks(page);
  const hid = bl[0]?.id;
  check(bl.length === 1 && bl[0].kind === 'heading' && bl[0].x === 0 && bl[0].y === 0, `Add heading puts a heading where the menu was opened (${JSON.stringify(bl)})`);
  check(await sh(page, (t) => t.openTiles.length) === TILES.length && !(await sh(page, (t) => t.openTiles.some((o) => !o.path))), 'no tile was made for it');
  await waitFor(page, (t, id) => !!t.query(`[data-block="${id}"] input.bedit`), hid, { label: 'the heading editor' });
  await page.keyboard.type('Quarterly review');
  await page.keyboard.press('Enter');
  await settle(page);
  bl = await blocks(page);
  check(bl[0]?.text === 'Quarterly review', `Enter saves the heading (${bl[0]?.text})`);
  check(await sh(page, (t, id) => t.query(`[data-block="${id}"] .bh`)?.textContent?.trim(), hid) === 'Quarterly review', 'the heading draws its text');

  // ---- a text block: Markdown, safely ----
  await canvasMenu(page, cv.x + 30, cv.y + 70, 'Add text');
  bl = await blocks(page);
  const tid = bl.find((b) => b.kind === 'text')?.id;
  check(!!tid && !overlap(bl.find((b) => b.id === tid), bl.find((b) => b.id === hid)), 'Add text lands clear of the heading');
  await waitFor(page, (t, id) => !!t.query(`[data-block="${id}"] textarea.bedit`), tid, { label: 'the text editor' });
  await page.keyboard.type('Numbers are **up**; see [the plan](https://example.com/plan).');
  await page.keyboard.press('Enter'); await page.keyboard.press('Enter');
  await page.keyboard.type('- one\n- two <img src=x onerror="window.pwned=1"> <b>raw</b>');
  await snap(page, `blocks-editing-${THEME}`);
  await page.keyboard.press('Escape');
  await settle(page);
  const md = await sh(page, (t, id) => {
    const el = t.query(`[data-block="${id}"] .bt`);
    const a = el?.querySelector('a');
    return { strong: el?.querySelector('strong')?.textContent, li: el?.querySelectorAll('li').length, img: !!el?.querySelector('img'),
      b: !!el?.querySelector('b'), target: a?.target, rel: a?.rel, href: a?.href, pwned: !!window.pwned };
  }, tid);
  check(md.strong === 'up' && md.li === 2, `the Markdown renders (${JSON.stringify(md)})`);
  check(!md.img && !md.b && !md.pwned, 'raw HTML stays text: no <img>, no <b>, nothing ran');
  check(md.target === '_blank' && /noopener/.test(md.rel) && md.href === 'https://example.com/plan', 'a link opens in a new tab with no opener');
  await snap(page, `blocks-canvas-${THEME}`);

  // ---- edit again: a double-click, leaving the field saves ----
  const hr = await rectOf(page, blockSel(hid));
  await page.mouse.dblclick(hr.x + 40, hr.y + hr.h / 2);
  await waitFor(page, (t, id) => !!t.query(`[data-block="${id}"] input.bedit`), hid, { label: 'editing on double-click' });
  await page.keyboard.press('Control+A');
  await page.keyboard.type('Overview');
  await page.mouse.click(cv.x + 1000, cv.y + 700); // the empty canvas: the field blurs
  await settle(page);
  check((await blocks(page)).find((b) => b.id === hid)?.text === 'Overview', 'leaving the field saves the edit');

  // ---- the heading's level from its menu ----
  await blockMenu(page, hid, 'Heading level', 'Heading 1');
  check((await blocks(page)).find((b) => b.id === hid)?.level === 1, 'Heading level ▸ Heading 1');
  const font = await sh(page, (t, id) => { const el = t.query(`[data-block="${id}"] .bh`); return el ? getComputedStyle(el).fontSize : ''; }, hid);
  check(font === '32px', `an H1 is the hero size (${font})`);

  // ---- move: the grip drags the text block onto apps/pinned, which is pushed ----
  const before = await sh(page, (t) => t.openTiles.map(({ path, x, y }) => ({ path, x, y })));
  const tr = await rectOf(page, blockSel(tid));
  await page.mouse.move(tr.x + tr.w / 2, tr.y + tr.h / 2);
  const grip = await rectOf(page, `${blockSel(tid)} .bgrip`);
  check(!!grip, 'the grip shows under the pointer');
  if (grip) await drag(page, grip.x + grip.w / 2, grip.y + grip.h / 2, 96, 192);
  const tb = (await blocks(page)).find((b) => b.id === tid);
  const after = await sh(page, (t) => t.openTiles.map(({ path, x, y, w, h }) => ({ path, x, y, w, h })));
  const moved = after.filter((o) => { const b = before.find((x) => x.path === o.path); return b && (b.x !== o.x || b.y !== o.y); });
  check(tb && tb.x === 96 && tb.y >= 192, `the grip moves the block by whole cells (${JSON.stringify(tb && { x: tb.x, y: tb.y })})`);
  check(moved.length >= 1 && after.every((o) => !overlap(o, tb)), `the tile under it is pushed out of the way (${JSON.stringify(moved)})`);

  // ---- resize by the corner ----
  const tr2 = await rectOf(page, blockSel(tid));
  await page.mouse.move(tr2.x + tr2.w / 2, tr2.y + tr2.h / 2);
  const rz = await rectOf(page, `${blockSel(tid)} .rz`);
  if (rz) await drag(page, rz.x + 6, rz.y + 6, 96, 48);
  const tb2 = (await blocks(page)).find((b) => b.id === tid);
  check(tb2 && tb2.w === tb.w + 96 && tb2.h === tb.h + 48, `the corner resizes it (${tb.w}×${tb.h} → ${tb2?.w}×${tb2?.h})`);
  const tr3 = await rectOf(page, blockSel(tid));
  await page.mouse.move(tr3.x + tr3.w / 2, tr3.y + tr3.h / 2);
  await snap(page, `blocks-hover-${THEME}`);
  await page.mouse.move(cv.x + 1000, cv.y + 700);
  await snap(page, `blocks-moved-${THEME}`);

  // ---- a reload keeps them ----
  await sh(page, (t) => t.flushSave());
  const kept = await blocks(page);
  await page.reload();
  await openShell(page);
  await sh(page, (t, id) => t.setScreen(id), id);
  await settle(page);
  await waitFor(page, (t, id) => !!t.query(`[data-block="${id}"]`), hid, { label: 'blocks after a reload' });
  check(JSON.stringify(await blocks(page)) === JSON.stringify(kept), 'a reload shows the same blocks');

  // ---- a phone stacks the blocks between the cards, nothing absolutely placed ----
  await page.setViewportSize({ width: 400, height: 800 });
  await sleep(400);
  const ys = await sh(page, (t, a) => a.map((sel) => t.query(sel)?.getBoundingClientRect().y ?? null),
    [blockSel(hid), blockSel(tid), '.gtile[data-path="apps/pinned"]', '.gtile[data-path="apps/offline"]']);
  check(ys.every((y, i) => y != null && (i === 0 || y > ys[i - 1])), `on a phone: heading, text, then the cards, stacked (${ys.map((y) => y && Math.round(y)).join(' < ')})`);
  await sh(page, (t, sel) => t.query(sel)?.scrollIntoView({ block: 'start' }), blockSel(hid));
  await settle(page);
  await snap(page, `blocks-phone-${THEME}`);
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(300);
  cv = await canvasTop();

  // ---- Document mode: each block a row, in reading order ----
  await sh(page, (t, id) => t.setScreenMode(id, 'doc'), id);
  await page.waitForSelector('.doc .dblock', { timeout: 10000, state: 'attached' });
  await settle(page);
  const rs = await sh(page, (t) => t.docRows());
  const flat = rs.flatMap((r) => r.paths);
  check(rs[0]?.paths.join() === `block:${hid}` && rs.every((r) => r.cols === 1 && r.paths.length === 1) && flat.includes(`block:${tid}`),
    `Document mode: the heading first, every block and tile a row of its own (${rs.map((r) => r.paths.join(',')).join(' | ')})`);
  check(!(await sh(page, (t, id) => !!t.query(`.dblock[data-block="${id}"] bx-frame, .dblock[data-block="${id}"] .card`), hid)), 'a block has no frame and no card');
  await page.mouse.move(700, 600);
  await snap(page, `blocks-doc-${THEME}`);
  // the heading's Row ▸ Move down from its menu (the keyboard's way)
  await sh(page, (t, id) => t.blockMenuItems(id).find((x) => x.label === 'Row').items.find((x) => x.label === 'Move down').action(), hid);
  await settle(page);
  const rs2 = await sh(page, (t) => t.docRows());
  check(rs2[1]?.paths.join() === `block:${hid}`, `Row ▸ Move down moves the heading a row (${rs2.map((r) => r.paths.join(',')).join(' | ')})`);
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await settle(page);
  const cb = (await blocks(page)).map(({ id, x, y, w, h }) => ({ id, x, y, w, h }));
  check(JSON.stringify(cb) === JSON.stringify(kept.map(({ id, x, y, w, h }) => ({ id, x, y, w, h }))), 'back on the canvas the blocks are where they were');

  // ---- delete from the menu ----
  cv = await canvasTop();
  await canvasMenu(page, cv.x + 1100, cv.y + 20, 'Add text');
  await page.keyboard.type('temporary');
  await page.keyboard.press('Escape');
  await settle(page);
  const tmp = (await blocks(page)).find((b) => b.text === 'temporary')?.id;
  if (tmp) await blockMenu(page, tmp, 'Delete text');
  check(!!tmp && !(await blocks(page)).some((b) => b.id === tmp), 'Delete text removes the block');
  // a block saved empty goes too
  await canvasMenu(page, cv.x + 1100, cv.y + 20, 'Add heading');
  await page.keyboard.press('Escape');
  await settle(page);
  check((await blocks(page)).length === 2, 'a new block left empty is not kept');

  // ---- an org screen: a draft takes a heading, publish keeps it with the tiles ----
  const srv = async (sid) => ((await (await ctx.request.get(`${URL}/api/xbin/screens`)).json()).org ?? []).find((x) => x.id === sid);
  const mk = await ctx.request.put(`${URL}/api/xbin/screens/org`, { data: { org: 'devs', name: 'Blocks HQ', edit: 'admins',
    tiles: [{ path: 'apps/pinned', x: 0, y: 96, w: 576, h: 384 }] } });
  const oid = (await mk.json()).id;
  check(mk.ok() && !!oid, `an org screen to try it on (${mk.status()})`);
  if (oid) {
    await waitFor(page, (t, id) => t.orgScreens.some((s) => s.id === id), oid, { label: 'the org screen arrives' });
    await sh(page, (t, id) => t.openOrgScreen(id), oid);
    await settle(page);
    const lines = await sh(page, (t) => t.canvasMenuItems().filter((x) => /^Add /.test(x.label ?? '')).map((x) => [x.label, x.hint, !!x.disabled]));
    check(lines.length === 2 && lines.every((l) => l[1] === 'starts a draft' && !l[2]), `on an org screen in view mode the Add lines start a draft (${JSON.stringify(lines)})`);
    await sh(page, (t) => t.addBlock('heading', { x: 0, y: 0 }));
    await waitFor(page, (t, id) => !!t.orgDraft(id), oid, { label: 'a draft' });
    await waitFor(page, () => !!document.querySelector('bx-shell')?.renderRoot?.querySelector('bx-canvas')?.renderRoot?.querySelector('input.bedit'), null, { label: 'the org heading editor' });
    await page.keyboard.type('Team board');
    await page.keyboard.press('Enter');
    await settle(page);
    const d = await sh(page, (t, id) => t.orgDraft(id), oid);
    check(d?.dirty && d.blocks?.[0]?.text === 'Team board' && !(await srv(oid)).blocks, 'the heading lives in the draft until it is published');
    await snap(page, `blocks-org-draft-${THEME}`);
    const rev0 = (await srv(oid)).rev;
    await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
    await waitFor(page, (t, id) => !t.orgDraft(id), oid, { label: 'published' });
    const pub = await srv(oid);
    check(pub.rev === rev0 + 1 && pub.blocks?.[0]?.text === 'Team board' && pub.tiles?.length === 1, `published with the tiles, a new revision (rev ${rev0} → ${pub.rev})`);
    // an older shell publishes tiles only: the heading stays
    const old = await ctx.request.put(`${URL}/api/xbin/screens/org`, { data: { id: oid, org: 'devs', tiles: [...pub.tiles, { path: 'apps/offline', x: 576, y: 96, w: 576, h: 384 }], rev: pub.rev } });
    const after2 = await srv(oid);
    check(old.ok() && after2.blocks?.[0]?.text === 'Team board' && after2.tiles.length === 2, 'a tiles-only save (an older shell) keeps the blocks');
    await waitFor(page, (t, a) => (t.orgScreens.find((s) => s.id === a.id)?.rev ?? 0) === a.rev, { id: oid, rev: after2.rev }, { label: 'the newer revision' });
    await settle(page);
    const ro = await sh(page, (t, id) => ({ text: t.query(`[data-block]`)?.textContent?.trim(), tools: !!t.query(`[data-block] .btools`), menu: t.blockMenuItems(id).length }), pub.blocks[0].id);
    check(ro.text === 'Team board' && !ro.tools && ro.menu === 0, `view mode shows it read-only: no tools, no menu (${JSON.stringify(ro)})`);
    await snap(page, `blocks-org-view-${THEME}`);
    const del = await ctx.request.delete(`${URL}/api/xbin/screens/org`, { data: { id: oid, org: 'devs' } });
    check(del.ok(), `tidy: the org screen deleted (${del.status()})`);
  } else {
    skip('no org screen');
  }

  // tidy: the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-blocks' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { blocks: blocksPass };
