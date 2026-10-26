// hack/ui-harness/passes/tileprompts.js — covers D195: the decisions
// waiting for the person sit on the window of the tile that asked, never in
// a strip at the top. apps/tp-y asks for apps/tp-x's reader role and has an
// unbound net slot. As the admin:
//   1. no strip; tp-y's title bar has a key badge saying 2, and a popover
//      over the top of its window holds its grant request and its slot
//      (only its own); tp-x, which asked nothing, has neither
//      (shot: tileprompts-canvas-<theme>);
//   2. ⌃ folds it to the badge; the badge opens it again;
//   3. the popover is on the window in Fixed width, Document and Tiled,
//      and on a floating window (shots for Document and Tiled);
//   4. the sidebar flags tp-y's row (and not tp-x's), and a closed folder
//      holding it counts 1 (shot: tileprompts-sidebar-<theme>);
//   5. closed on the screen, tp-y shows nothing but its flag;
//   6. on a phone the popover is in the card's flow, above the tile;
//   7. Dismiss on both: the badge, the popover and the flag go.
// The tiles, the folder and the dismissals are removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, settle, sh, shotEl, shot, checker, sleep, THEME } = require('../lib');

const WS = process.env.WS || '';
const X = 'apps/tp-x', Y = 'apps/tp-y';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

function writeTiles() {
  const man = {
    [X]: { title: 'TP x', expose: { roles: { reader: 'read tp-x' } } },
    [Y]: { title: 'TP y', icon: 'mail', uses: [{ target: X, role: 'reader' }], interfaces: { net: { kind: 'net' } } },
  };
  for (const t of [X, Y]) {
    const dir = path.join(WS, t);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><title>${t}</title><h1>${t}</h1>\n`);
    fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify(man[t]) + '\n');
  }
}

// What the shell shows of one tile: its badge, its popover (where, what in
// it), its sidebar row's flag — read through the shadow roots.
const look = (page, tile) => page.evaluate((tile) => {
  const sh = document.querySelector('bx-shell')?.shadowRoot, cv = sh?.querySelector('bx-canvas')?.shadowRoot;
  const card = cv?.querySelector(`.card[data-path="${tile}"]`);
  const pop = cv?.querySelector(`.pqpop[data-prompts-for="${tile}"]`);
  const g = pop?.querySelector('bx-grants')?.shadowRoot, b = pop?.querySelector('bx-bindings')?.shadowRoot;
  const r = (el) => { const x = el?.getBoundingClientRect(); return x ? { x: Math.round(x.x), y: Math.round(x.y), w: Math.round(x.width), h: Math.round(x.height) } : null; };
  const side = sh?.querySelector('bx-side')?.shadowRoot;
  const flag = side?.querySelector(`.item[data-path="${tile}"] .pq`);
  return {
    strip: !!sh?.querySelector('.grants'),
    card: !!card, cardRect: r(card), title: card?.querySelector('.head .nm')?.textContent ?? '', icon: card?.querySelector('.head .tico')?.getAttribute('name') ?? '',
    badge: Number(card?.querySelector('.pqb')?.dataset.prompts ?? 0), expanded: card?.querySelector('.pqb')?.getAttribute('aria-expanded') ?? '',
    pop: !!pop, popRect: r(pop), popInFlow: !!pop && getComputedStyle(pop).position === 'static', popBeforeBody: pop?.parentElement === card,
    requests: [...(g?.querySelectorAll('.plate') ?? [])].map((p) => p.querySelector('.who')?.textContent.replace(/\s+/g, ' ').trim()),
    slots: [...(b?.querySelectorAll('.row') ?? [])].map((x) => x.querySelector('.who')?.textContent.trim() + ' ' + x.querySelector('.slot')?.textContent.trim()),
    flag: Number(flag?.dataset.prompts ?? 0), sideLabel: side?.querySelector(`.item[data-path="${tile}"] .nm`)?.textContent ?? '',
    folderCount: Number(side?.querySelector('.folder .pqn')?.dataset.prompts ?? 0),
  };
}, tile);

async function tilePrompts(browser) {
  const { check, skip, done } = checker(`tile-prompts-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  const A = await login(browser, 'admin', 'admin', { viewport: { width: 1400, height: 900 } });
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }),
  });
  const original = await (await api('GET', '/prefs/layout')).json().catch(() => null);
  await api('DELETE', '/prefs/dismissed');
  writeTiles();
  try {
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [X, Y].includes(c.path)).length === 2, 'the tiles registered');
    await openShell(A.page);
    await usePersonalScreen(A.page);
    const id = await sh(A.page, (t) => t.addScreen());
    await settle(A.page);
    for (const t of [X, Y]) await sh(A.page, (t, p) => t.openTile(p), t);

    // 1. on the tile, no strip
    let y = await until(async () => { const v = await look(A.page, Y); return v.badge && v.requests.length && v.slots.length ? v : null; }, 'tp-y\'s prompts')
      .catch(async () => look(A.page, Y));
    check(!y.strip, 'no decision strip above the screen');
    check(y.badge === 2, `tp-y's title bar has a key badge saying 2 (${y.badge})`);
    check(y.pop && y.requests.length === 1 && y.requests[0].includes(Y) && y.requests[0].includes(X), `its popover holds its grant request (${JSON.stringify(y.requests)})`);
    check(y.slots.length === 1 && y.slots[0] === `${Y} net`, `…and its slot, only its own (${JSON.stringify(y.slots)})`);
    check(y.popRect && y.cardRect && y.popRect.y > y.cardRect.y && y.popRect.y + y.popRect.h <= y.cardRect.y + y.cardRect.h + 1, `the popover sits inside its window (${JSON.stringify([y.cardRect, y.popRect])})`);
    check(y.title === 'TP y' && y.icon === 'mail', `the window says the tile's own title and icon (D194: ${y.title}, ${y.icon})`);
    const x = await look(A.page, X);
    check(x.card && !x.badge && !x.pop, 'tp-x, which asked nothing, has neither');
    await shotEl(A.page, `bx-canvas .card[data-path="${Y}"]`, `tileprompts-canvas-${THEME}`).catch(() => shot(A.page, `tileprompts-canvas-${THEME}`, { fullPage: false }));

    // 2. fold and open again
    await A.page.locator(`bx-canvas .pqpop[data-prompts-for="${Y}"] .pqx`).click();
    y = await look(A.page, Y);
    check(!y.pop && y.badge === 2 && y.expanded === 'false', `⌃ folds it to the badge (${JSON.stringify({ pop: y.pop, badge: y.badge, expanded: y.expanded })})`);
    // a fold is kept in this browser: a reload shows the badge, not the popover
    await sh(A.page, (t) => t.flushSave());
    await A.page.reload();
    await openShell(A.page);
    await sh(A.page, (t, s) => t.setScreen(s), id);
    await settle(A.page);
    y = await until(async () => { const v = await look(A.page, Y); return v.card && v.badge === 2 ? v : null; }, 'tp-y after a reload').catch(async () => look(A.page, Y));
    check(!y.pop && y.badge === 2, `a fold outlasts a reload (${JSON.stringify({ pop: y.pop, badge: y.badge })})`);
    await A.page.locator(`bx-canvas .card[data-path="${Y}"] .pqb`).click();
    y = await look(A.page, Y);
    check(y.pop && y.expanded === 'true', 'the badge opens it again');

    // 3. every mode, and a floating window
    for (const m of ['fixed', 'doc', 'tiled']) {
      await sh(A.page, (t, a) => t.setScreenMode(a[0], a[1]), [id, m]);
      y = await until(async () => { const v = await look(A.page, Y); return v.pop && v.popRect?.h > 20 ? v : null; }, `the popover in ${m}`).catch(async () => look(A.page, Y));
      check(y.pop && y.popRect?.h > 20 && y.requests.length === 1, `${m}: the popover is on tp-y's window (${JSON.stringify(y.popRect)})`);
      if (m === 'doc') check(y.popInFlow && y.popBeforeBody && y.popRect.h > 150, `doc: in the card's flow above the page, its full height (${y.popRect?.h})`);
      if (m === 'doc' || m === 'tiled') await shot(A.page, `tileprompts-${m}-${THEME}`, { fullPage: false });
    }
    await sh(A.page, (t, a) => t.setScreenMode(a, 'canvas'), id);
    await settle(A.page);
    await sh(A.page, (t, p) => t.query('bx-canvas').togglePin(p), Y);
    y = await until(async () => {
      const v = await A.page.evaluate((tile) => !!document.querySelector('bx-shell')?.shadowRoot?.querySelector('bx-canvas')?.shadowRoot
        ?.querySelector(`.float[data-path="${tile}"] .pqpop`), Y);
      return v || null;
    }, 'the popover on the floating window').catch(() => false);
    check(!!y, 'a floating window carries it too');
    await sh(A.page, (t, p) => t.query('bx-canvas').togglePin(p), Y);
    await settle(A.page);

    // 4. the sidebar: tp-y's row flagged, a closed folder counts it
    y = await look(A.page, Y);
    check(y.flag === 2 && (await look(A.page, X)).flag === 0, `the sidebar flags tp-y's row, not tp-x's (${y.flag})`);
    check(y.sideLabel === 'TP y', `the sidebar names it by its title (D194: ${y.sideLabel})`);
    await sh(A.page, (t, p) => t.folderCtx('top').mutate((fs) => [...fs, { id: 'tp-folder', name: 'Prompts', open: false, items: [p] }]), Y);
    const folder = await until(async () => { const v = await look(A.page, Y); return v.folderCount ? v : null; }, 'the folder count').catch(async () => look(A.page, Y));
    check(folder.folderCount === 1, `a closed folder holding it counts 1 (${folder.folderCount})`);
    await shot(A.page, `tileprompts-sidebar-${THEME}`, { fullPage: false, clip: { x: 0, y: 40, width: 260, height: 700 } });
    await sh(A.page, (t) => t.folderCtx('top').mutate((fs) => fs.filter((f) => f.id !== 'tp-folder')));

    // 5. not on the screen: only the flag
    await sh(A.page, (t, p) => t.closeTile(p), Y);
    await settle(A.page);
    y = await look(A.page, Y);
    check(!y.card && !y.pop && y.flag === 2, `closed on the screen: nothing but the sidebar flag (${JSON.stringify({ card: y.card, pop: y.pop, flag: y.flag })})`);
    await sh(A.page, (t, p) => t.openTile(p), Y);

    // 6. a phone: in the card's flow, above the tile
    await A.page.setViewportSize({ width: 400, height: 800 });
    y = await until(async () => { const v = await look(A.page, Y); return v.pop && v.popBeforeBody ? v : null; }, 'the phone popover').catch(async () => look(A.page, Y));
    check(y.pop && y.popInFlow && y.popBeforeBody, `on a phone it sits in the card, above the tile (${JSON.stringify({ inFlow: y.popInFlow, before: y.popBeforeBody })})`);
    await A.page.setViewportSize({ width: 1400, height: 900 });
    await settle(A.page);

    // 7. Dismiss both: everything goes
    await A.page.locator(`bx-canvas .pqpop[data-prompts-for="${Y}"] bx-grants [data-dismiss]`).first().click();
    y = await until(async () => { const v = await look(A.page, Y); return v.badge === 1 ? v : null; }, 'one left').catch(async () => look(A.page, Y));
    check(y.badge === 1 && y.flag === 1, `one dismissed: the badge and the flag say 1 (${y.badge}, ${y.flag})`);
    await A.page.locator(`bx-canvas .pqpop[data-prompts-for="${Y}"] bx-bindings [data-dismiss]`).first().click();
    y = await until(async () => { const v = await look(A.page, Y); return !v.badge && !v.pop && !v.flag ? v : null; }, 'all gone').catch(async () => look(A.page, Y));
    check(!y.badge && !y.pop && !y.flag, `both dismissed: no badge, no popover, no flag (${JSON.stringify({ badge: y.badge, pop: y.pop, flag: y.flag })})`);
    if (!WS) skip('no $WS');
  } finally {
    await api('DELETE', '/prefs/dismissed').catch(() => {});
    if (original) await api('PUT', '/prefs/layout', original).catch(() => {});
    for (const t of [X, Y]) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'tp-'))) fs.rmSync(dir, { recursive: true, force: true });
    }
    await settle(A.page).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { tilePrompts };
