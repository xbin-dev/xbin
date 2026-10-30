// hack/ui-harness/passes/tiled.js — the Tiled mode (D193): a screen
// switched to Tiled shows its one tile filling the pane (no page scroll, the
// top bar out of the way); a second tile splits it along the longer side, a
// third splits the focused tile; dragging a divider shares the space anew
// and is kept with the screen; a card dragged by its title bar onto another
// swaps them at its centre, takes half of its space at an edge (D205: the
// preview shows where it lands and where the other goes), takes half of the
// screen at the screen's edge, and Escape cancels; a new screen starts
// Tiled; the tile menu's Rotate split turns a split; closing a tile
// gives its space to its sibling; a reload keeps the tree; a phone shows one
// tile with a switcher; switching back to Canvas shows every tile where it
// was. An org screen in Tiled shows no dividers in view mode, and a draft's
// divider drag is published with the screen at a new revision. Run it in
// both themes (HARNESS_THEME=light).
const { URL, sleep, login, settle, sh, waitFor, openShell, usePersonalScreen, shot, checker, THEME } = require('../lib');

const [A, B, C] = ['apps/pinned', 'apps/offline', 'apps/leads'];
const rectIn = (page, sel) => sh(page, (t, sel) => { const r = t.query(sel)?.getBoundingClientRect(); return r ? { x: r.x, y: r.y, w: r.width, h: r.height } : null; }, sel);
const tileRect = (page, p) => rectIn(page, `.ttile[data-path="${p}"]`);
const near = (a, b, d = 2) => Math.abs(a - b) <= d;
const R = (r) => r && [Math.round(r.x), Math.round(r.y), Math.round(r.w), Math.round(r.h)];
const tree = (page) => sh(page, (t) => t.tiling());
const leafPath = (t, p, np = '') => (!t ? null : t.leaf !== undefined ? (t.leaf === p ? np : null) : leafPath(t.a, p, np + 'a') ?? leafPath(t.b, p, np + 'b'));
const geom = (page) => sh(page, (t) => t.openTiles.map(({ path, x, y, w, h, float }) => ({ path, x, y, w, h, float: float ?? null })).sort((p, q) => (p.path < q.path ? -1 : 1)));

// a title-bar click (not a drag): the tile is the active window
async function focusTile(page, p) {
  const h = await rectIn(page, `.ttile[data-path="${p}"] .card .head`);
  if (h) { await page.mouse.click(h.x + 40, h.y + h.h / 2); await settle(page); }
}
// a drag from (x, y) by (dx, dy), in steps
async function drag(page, x, y, dx, dy, during) {
  await page.mouse.move(x, y); await page.mouse.down();
  await page.mouse.move(x + dx / 4, y + dy / 4, { steps: 3 });
  await page.mouse.move(x + dx, y + dy, { steps: 8 });
  const seen = during ? await during() : null;
  await page.mouse.up();
  await settle(page);
  return seen;
}

async function tiled(browser) {
  const { check, skip, done } = checker(`tiled-${THEME}`);
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const prefs = `${URL}/api/xbin/prefs/layout`;
  const original = await (await ctx.request.get(prefs)).json();
  await openShell(page);
  await usePersonalScreen(page);
  const id = await sh(page, (t) => t.addScreen());
  await settle(page);
  check(await sh(page, (t) => t.screenMode) === 'tiled', 'a new screen starts Tiled');
  await sh(page, (t, p) => t.openTile(p), A);
  await waitFor(page, (t) => t.openTiles.length === 1, null, { label: 'a tile open' });

  // Tiled: the one tile fills the pane, nothing scrolls, the bar is away
  await sh(page, (t, id) => t.setScreenMode(id, 'tiled'), id);
  await page.waitForSelector('.tiled .ttile', { timeout: 10000, state: 'attached' });
  await settle(page);
  check(await sh(page, (t) => t.screenMode) === 'tiled', 'the screen says Tiled');
  // no strip of decisions above the tiles (they sit on the tile that asked, D195)
  check(!(await sh(page, (t) => !!t.query('.grants'))), 'no decision strip above the tiles');
  const pane = await rectIn(page, '.tiled');
  const one = await tileRect(page, A);
  const scroll = await sh(page, (t) => { const m = t.query('main'); return { sh: m.scrollHeight, ch: m.clientHeight, sw: m.scrollWidth, cw: m.clientWidth }; });
  check(one && near(one.x, pane.x) && near(one.y, pane.y) && near(one.w, pane.w) && near(one.h, pane.h) && pane.h > 700,
    `one tile fills the pane (${JSON.stringify(R(one))} in ${JSON.stringify(R(pane))})`);
  check(scroll.sh <= scroll.ch + 1 && scroll.sw <= scroll.cw + 1, `the page doesn't scroll (${JSON.stringify(scroll)})`);
  const bar = await sh(page, (t) => t.topBar);
  check(bar.on && bar.hidden, `the top bar is out of the way (${JSON.stringify(bar)})`);
  await page.mouse.move(700, 3); await sleep(300);
  check(!(await sh(page, (t) => t.topBar)).hidden, 'the pointer at the top edge brings the bar back');
  await page.mouse.move(700, 20); await page.mouse.move(700, 500, { steps: 12 }); await sleep(300);
  check((await sh(page, (t) => t.topBar)).hidden, 'moving down onto the tiles sends it away again');

  // a second tile splits the first along its longer side (a wide pane: side by side)
  await focusTile(page, A);
  await sh(page, (t, p) => t.openTile(p), B);
  await waitFor(page, (t) => t.openTiles.length === 2, null, { label: 'two tiles' });
  await settle(page);
  let tr = await tree(page);
  const [ra, rb] = [await tileRect(page, A), await tileRect(page, B)];
  check(tr?.split === 'row' && tr.a.leaf === A && tr.b.leaf === B && near(ra.h, pane.h) && near(rb.h, pane.h) && rb.x > ra.x + ra.w && near(ra.w, rb.w, 3),
    `a second tile splits the first side by side, half each (${JSON.stringify(R(ra))} | ${JSON.stringify(R(rb))})`);

  // a third splits the focused tile — B, taller than wide: one above the other
  await focusTile(page, B);
  await sh(page, (t, p) => t.openTile(p), C);
  await waitFor(page, (t) => t.openTiles.length === 3, null, { label: 'three tiles' });
  await settle(page);
  tr = await tree(page);
  const [rb2, rc] = [await tileRect(page, B), await tileRect(page, C)];
  check(leafPath(tr, C) === 'bb' && tr.b.split === 'col' && near(rc.x, rb2.x) && rc.y > rb2.y + rb2.h && near(rc.w, rb2.w),
    `a third splits the focused tile, below it (${leafPath(tr, C)}; ${JSON.stringify(R(rb2))} / ${JSON.stringify(R(rc))})`);
  check(!(await sh(page, (t) => t.anchor))?.held, 'the focused tile holds no place: nothing scrolls');
  const canvas = await geom(page);
  await shot(page, `tiled-three-${THEME}`, { fullPage: false });

  // a divider: drag the root one 200 px left — the left part narrows, kept with the screen
  const dv = await rectIn(page, '.tdiv[data-node=""]');
  check(!!dv, 'a divider sits between the two halves');
  if (dv) {
    const w0 = (await tileRect(page, A)).w;
    await drag(page, dv.x + dv.w / 2, dv.y + dv.h / 2, -200, 0);
    const w1 = (await tileRect(page, A)).w;
    const ratio = await sh(page, (t) => t.arrange?.tiling?.ratio ?? null);
    check(near(w1, w0 - 200, 3) && ratio != null && ratio < 0.45, `dragging the divider 200 px left narrows the left part (${Math.round(w0)} → ${Math.round(w1)}; ratio ${ratio?.toFixed(3)})`);
    await sh(page, (t) => t.flushSave());
    const saved = (await (await ctx.request.get(prefs)).json()).screens?.find((x) => x.id === id)?.arrange?.tiling;
    check(saved?.ratio === ratio && saved?.b?.b?.leaf === C, `the tree is saved with the screen (ratio ${saved?.ratio})`);
  }

  // a swap: A's title bar dragged onto C
  const ha = await rectIn(page, `.ttile[data-path="${A}"] .card .head`), tc = await tileRect(page, C);
  if (ha && tc) {
    const drop = await drag(page, ha.x + 40, ha.y + ha.h / 2, tc.x + tc.w / 2 - (ha.x + 40), tc.y + tc.h / 2 - (ha.y + ha.h / 2),
      () => sh(page, (t, p) => !!t.query(`.ttile.drop[data-path="${p}"]`), C));
    check(drop, 'the tile it would land on is marked while dragging');
    await shot(page, `tiled-swapped-${THEME}`, { fullPage: false });
  }
  tr = await tree(page);
  check(leafPath(tr, C) === 'a' && leafPath(tr, A) === 'bb', `dragging A's title bar onto C swaps them (C ${leafPath(tr, C)}, A ${leafPath(tr, A)})`);

  // D205 — C | (B / A). A onto the right edge of C: A takes C's right half,
  // B the space A left; the preview is the outcome, before the drop
  const headOf = (p) => rectIn(page, `.ttile[data-path="${p}"] .card .head`);
  const dragTo = async (p, x, y, during) => { const h = await headOf(p); return h ? drag(page, h.x + 40, h.y + h.h / 2, x - (h.x + 40), y - (h.y + h.h / 2), during) : null; };
  const ghosts = () => sh(page, (t) => [...(t.query('.tiled')?.querySelectorAll('.tghost') ?? [])].map((g) => {
    const r = g.getBoundingClientRect(); return { cls: g.classList.contains('other') ? 'other' : 'land', text: g.textContent.trim(), r: [r.x, r.y, r.width, r.height] };
  }));
  const at = (r, u, v) => [r.x + u * r.w, r.y + v * r.h];
  const sameBox = (g, r, d = 3) => !!g && !!r && near(g[0], r.x, d) && near(g[1], r.y, d) && near(g[2], r.w, d) && near(g[3], r.h, d);
  let rc0 = await tileRect(page, C);
  const half = await dragTo(A, ...at(rc0, 0.92, 0.5), async () => {
    const g = await ghosts(); await shot(page, `tiled-drop-half-${THEME}`, { fullPage: false }); return g;
  });
  tr = await tree(page);
  const [ra3, rc3] = [await tileRect(page, A), await tileRect(page, C)];
  check(leafPath(tr, C) === 'aa' && leafPath(tr, A) === 'ab' && leafPath(tr, B) === 'b' && tr.a.split === 'row',
    `A dropped on C's right edge takes C's right half, B takes A's space (C ${leafPath(tr, C)}, A ${leafPath(tr, A)}, B ${leafPath(tr, B)})`);
  const land = half?.find((g) => g.cls === 'land'), oth = half?.find((g) => g.cls === 'other');
  check(half?.length === 2 && /lands here$/.test(land?.text) && /squeezes here$/.test(oth?.text),
    `while dragging: where A lands and where C squeezes, no zones (${JSON.stringify(half?.map((g) => g.text))})`);
  check(sameBox(land?.r, ra3) && sameBox(oth?.r, rc3), `the preview was the outcome (A ${JSON.stringify(land?.r.map(Math.round))} → ${JSON.stringify(R(ra3))}; C ${JSON.stringify(oth?.r.map(Math.round))} → ${JSON.stringify(R(rc3))})`);

  // B to the screen's bottom edge: B takes the bottom half of the screen
  const edge = await dragTo(B, pane.x + pane.w / 2, pane.y + pane.h - 8, ghosts);
  tr = await tree(page);
  const rb3 = await tileRect(page, B);
  check(tr?.split === 'col' && tr.b?.leaf === B && near(rb3.y + rb3.h, pane.y + pane.h) && near(rb3.w, pane.w) && near(rb3.h, pane.h / 2, 6),
    `B dropped on the screen's bottom edge takes the bottom half (${JSON.stringify(R(rb3))})`);
  check(edge?.length === 1 && /bottom half of the screen$/.test(edge[0].text) && sameBox(edge[0].r, rb3),
    `while dragging: only where B lands (${JSON.stringify(edge?.map((g) => g.text))})`);

  // Escape mid-drag: nothing moves, nothing left drawn
  const t0 = JSON.stringify(tr), ra4 = await tileRect(page, A);
  const esc = await dragTo(C, ...at(ra4, 0.5, 0.5), async () => {
    const g = (await ghosts()).length; await page.keyboard.press('Escape'); await sleep(150); return { g, after: (await ghosts()).length };
  });
  check(esc?.g === 2 && esc.after === 0 && JSON.stringify(await tree(page)) === t0, `Escape cancels a drag (${JSON.stringify(esc)})`);

  // back to C | (B / A) for what follows: C to the left edge, A onto B's bottom (they share a split: B keeps its size)
  await dragTo(C, pane.x + 6, pane.y + pane.h / 2);
  const back = await dragTo(A, ...at(await tileRect(page, B), 0.5, 0.85), ghosts);
  tr = await tree(page);
  check(leafPath(tr, C) === 'a' && leafPath(tr, B) === 'ba' && leafPath(tr, A) === 'bb' && tr.split === 'row',
    `C to the left edge, A onto B's bottom: C | (B / A) again (${JSON.stringify(tr)})`);
  check(/moves here$/.test(back?.find((g) => g.cls === 'other')?.text ?? ''),
    `a tile dropped on its sibling: the sibling just moves (${JSON.stringify(back?.map((g) => g.text))})`);

  // the tile menu: Rotate split turns A's split (B above A → B beside A)
  const lines = await sh(page, (t, p) => t.tileMenuItems(p).filter((x) => ['Rotate split', 'Swap with sibling'].includes(x.label)).map((x) => [x.label, !!x.disabled]), A);
  check(lines.length === 2 && lines.every((l) => !l[1]), `the tile menu has Rotate split and Swap with sibling (${JSON.stringify(lines)})`);
  await sh(page, (t, p) => t.tileMenuItems(p).find((x) => x.label === 'Rotate split').action(), A);
  await settle(page);
  check((await tree(page))?.b?.split === 'row', 'Rotate split turns the split side by side');
  await sh(page, (t, p) => t.tileMenuItems(p).find((x) => x.label === 'Rotate split').action(), A);
  await settle(page);

  // closing B gives its space to A (its sibling): A spans the right part's height
  await sh(page, (t, p) => t.closeTile(p), B);
  await waitFor(page, (t) => t.openTiles.length === 2, null, { label: 'B closed' });
  await settle(page);
  const ra2 = await tileRect(page, A);
  tr = await tree(page);
  check(near(ra2.h, pane.h) && leafPath(tr, A) === 'b' && leafPath(tr, B) === null, `closing a tile gives its space to its sibling (${JSON.stringify(R(ra2))})`);
  await sh(page, (t, p) => t.openTile(p), B); // back for what follows: it splits the focused tile
  await waitFor(page, (t) => t.openTiles.length === 3, null, { label: 'B again' });

  // a reload keeps the tree
  await sh(page, (t) => t.flushSave());
  const before = JSON.stringify(await tree(page));
  await openShell(page);
  await page.waitForSelector('.tiled .ttile', { timeout: 10000, state: 'attached' });
  await sleep(300); await settle(page);
  const after = JSON.stringify(await tree(page));
  check(after === before && (await sh(page, (t) => t.activeScreen)) === id, `a reload keeps the tree (${after})`);

  // a phone: one tile, a switcher of the tiles' names
  await page.setViewportSize({ width: 400, height: 800 });
  await sleep(500);
  const ph = await sh(page, (t) => ({ shown: [...t.query('.tiled').querySelectorAll('.ttile:not(.off)')].map((e) => e.dataset.path),
    tabs: [...t.query('.tiled').querySelectorAll('.tswitch button')].map((b) => b.textContent.trim()) }));
  check(ph.shown.length === 1 && ph.tabs.length === 3, `a phone shows one tile and a switcher (${JSON.stringify(ph)})`);
  const other = await sh(page, (t, p) => { const b = [...t.query('.tiled').querySelectorAll('.tswitch button')].find((x) => x.title !== p); b?.click(); return b?.title ?? null; }, ph.shown[0]);
  await settle(page);
  const ph2 = await sh(page, (t) => [...t.query('.tiled').querySelectorAll('.ttile:not(.off)')].map((e) => e.dataset.path));
  check(other && ph2.length === 1 && ph2[0] === other, `the switcher shows another tile (${ph2})`);
  const phr = await tileRect(page, ph2[0]);
  // the pane under the switcher: from the switcher's bottom to the tiled
  // area's (warning banners above main, D188, may take some of the height)
  const pb = await sh(page, (t) => { const r = (e) => e && e.getBoundingClientRect(); const sw = r(t.query('.tiled .tswitch')), a = r(t.query('.tiled'));
    return sw && a ? { top: sw.bottom, bottom: a.bottom } : null; });
  check(phr && pb && phr.w > 350 && phr.h > 250 && phr.y - pb.top >= 0 && phr.y - pb.top <= 16 && Math.abs(phr.y + phr.h - pb.bottom) <= 16,
    `it fills the pane under the switcher (${JSON.stringify(R(phr))} in ${JSON.stringify(pb)})`);
  await shot(page, `tiled-phone-${THEME}`, { fullPage: false });
  await page.setViewportSize({ width: 1400, height: 900 });
  await sleep(400);

  // back to Canvas: every tile exactly where it was
  await sh(page, (t, id) => t.setScreenMode(id, 'canvas'), id);
  await settle(page);
  check(JSON.stringify(await geom(page)) === JSON.stringify(canvas), 'Canvas shows every tile at its old place and size');
  check(!!(await rectIn(page, '.canvas')) && !(await sh(page, (t) => t.topBar)).on, 'the canvas is back, the bar in its place');

  // an org screen in Tiled: view mode has no dividers; a draft's divider drag is published with it
  const srv = async (sid) => ((await (await ctx.request.get(`${URL}/api/xbin/screens`)).json()).org ?? []).find((x) => x.id === sid);
  const mk = await ctx.request.put(`${URL}/api/xbin/screens/org`, { data: { org: 'devs', name: 'Tiled HQ', edit: 'admins', mode: 'tiled',
    tiles: [{ path: A, x: 0, y: 0, w: 576, h: 384 }, { path: B, x: 576, y: 0, w: 576, h: 384 }] } });
  const oid = (await mk.json()).id;
  check(mk.ok() && !!oid, `an org screen in Tiled to try it on (${mk.status()})`);
  if (oid) {
    await waitFor(page, (t, id) => t.orgScreens.some((s) => s.id === id), oid, { label: 'the org screen arrives' });
    await sh(page, (t, id) => t.openOrgScreen(id), oid);
    await page.waitForSelector('.tiled .ttile', { timeout: 10000, state: 'attached' });
    await settle(page);
    const view = await sh(page, (t) => ({ tiles: t.query('.tiled').querySelectorAll('.ttile:not(.off)').length, divs: t.query('.tiled').querySelectorAll('.tdiv').length }));
    check(view.tiles === 2 && view.divs === 0, `view mode: the tiles side by side, no dividers (${JSON.stringify(view)})`);
    await shot(page, `tiled-org-view-${THEME}`, { fullPage: false });
    await page.locator('bx-shell .orgbar button', { hasText: 'edit layout' }).click();
    await waitFor(page, (t, id) => !!t.orgDraft(id), oid, { label: 'a draft' });
    await page.mouse.move(700, 500); await sleep(300);
    await settle(page);
    const odv = await rectIn(page, '.tdiv[data-node=""]');
    if (odv) await drag(page, odv.x + odv.w / 2, odv.y + odv.h / 2, 150, 0);
    const d = await sh(page, (t, id) => t.orgDraft(id), oid);
    const rev0 = (await srv(oid)).rev;
    check(d?.dirty && d.arrange?.tiling?.ratio > 0.55 && !(await srv(oid)).arrange, `the divider moves in the draft, not on the server yet (ratio ${d?.arrange?.tiling?.ratio?.toFixed(3)})`);
    await page.locator('bx-shell .orgbar button', { hasText: 'Save and update' }).click();
    await waitFor(page, (t, id) => !t.orgDraft(id), oid, { label: 'published' });
    const pub = await srv(oid);
    check(pub.rev === rev0 + 1 && pub.mode === 'tiled' && pub.arrange?.tiling?.ratio === d.arrange.tiling.ratio,
      `published: the tree with the screen at a new revision (rev ${rev0} → ${pub.rev}, ratio ${pub.arrange?.tiling?.ratio})`);
    const del = await ctx.request.delete(`${URL}/api/xbin/screens/org`, { data: { id: oid, org: 'devs' } });
    check(del.ok(), `tidy: the org screen deleted (${del.status()})`);
  } else {
    skip('no org screen');
  }

  // tidy: the strip back, the layout as it was
  await sh(page, (t) => t.flushSave());
  const put = await ctx.request.put(prefs, { data: original, headers: { 'X-Prefs-Writer': 'harness-tiled' } });
  check(put.ok(), `tidy: the original layout back (${put.status()})`);
  await sleep(500);
  await ctx.close();
  done();
}

module.exports = { tiled };
