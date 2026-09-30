// hack/ui-harness/passes/partitionmark.js — covers PD-53 (design A) and
// 06 §12.3 — the scaffold shell on partitioned tiles:
//   1. apps/ppart (user + global; holds nothing, so its mode is recorded at
//      once) carries the marker — a teal half-split disc — on its sidebar
//      row (right before ⋯) and on its window head (where the runtime dot
//      is): role img, the tooltip naming the global instance, cursor
//      default, no hover state; an unpartitioned tile keeps its dot;
//   2. apps/pkeep (org devs; holds a vault key; its code starts asking for
//      user partitions): pending — its card greys out under the alert's
//      words; preader (reads it, manages nothing) sees no buttons; dev1 (an
//      admin of devs) presses Keep the current mode → the overlay goes, the
//      frame shows the tile, the vault key is still there;
//   3. apps/pswitch (user:dev1; recorded user, holds a vault key; its code
//      drops partition): pending, and the marker stays; Switch… shows the
//      dry run's counts and keep list in a typed confirmation; a wrong path
//      is refused there, the tile's path switches: the tile runs
//      unpartitioned, the vault key is gone, the marker and overlay go.
// pkeep's manifest goes back to unpartitioned (withdrawn) and preader is
// removed at the end; the tiles stay (a rerun starts them over).
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, tileFrame, settle, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const PART = 'apps/ppart', KEEP = 'apps/pkeep', SWITCH = 'apps/pswitch';
const TEAL = 'rgb(63, 181, 163)';

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

// write a static tile: its index.html names it, its xbin.json asks for partition (or not)
function writeTile(tile, partition) {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  const id = tile.slice(tile.lastIndexOf('/') + 1) + '-tile';
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1 id="${id}">${tile} itself</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: tile, ...(partition ? { partition } : {}) }));
}

const card = (tile) => `bx-canvas .card[data-path="${tile}"]`;
const row = (tile) => `bx-side .item[data-path="${tile}"]`;
// the marker's facts, wherever sel finds it
const markFacts = (page, sel) => page.locator(sel).first().evaluate((el) => {
  const cs = getComputedStyle(el);
  return { role: el.getAttribute('role'), title: el.title, label: el.getAttribute('aria-label'), cursor: cs.cursor, color: cs.color,
    border: cs.borderTopStyle, next: el.nextElementSibling?.className || '', w: el.getBoundingClientRect().width };
}).catch(() => null);

async function partitionMark(browser) {
  const { check, done } = checker('partition-mark');
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (tile) => ((await (await api('GET', `/components/${tile}`)).json().catch(() => ({}))).component || {});
  const vaultHas = async (tile) => JSON.stringify(await (await api('GET', `/vault/${tile}`)).json().catch(() => ({}))).includes('token');
  let B, R;
  try {
    // ---- the tiles ----
    await api('POST', '/users', { id: 'preader', name: 'P Reader', role: 'user', password: 'preaderpass123', orgs: [{ org: 'devs', level: 'read' }] });
    writeTile(PART, ['user', 'global']);
    writeTile(KEEP, null);
    writeTile(SWITCH, ['user']);
    for (const t of [PART, KEEP, SWITCH]) await until(async () => (await api('GET', `/components/${t}`)).status() === 200, `${t} registered`);
    const own = [await api('POST', '/owner', { tile: PART, to: 'user:dev1' }), await api('POST', '/owner', { tile: KEEP, to: 'org:devs' }),
      await api('POST', '/owner', { tile: SWITCH, to: 'user:dev1' })];
    check(own.every((r) => r.status() === 200), `owners set: ${PART}, ${SWITCH} dev1's, ${KEEP} org devs' (${own.map((r) => r.status())})`);
    const p = await until(async () => { const c = await comp(PART); return c.partition?.state === 'partitioned' && c; }, `${PART} partitioned`);
    check(p.partition.user && p.partition.global && !p.partition.request, `${PART} holds nothing: user + global recorded at once (${JSON.stringify(p.partition)})`);
    await until(async () => (await comp(SWITCH)).partition?.state === 'partitioned', `${SWITCH} partitioned`);
    const v = [await api('PUT', `/vault/${KEEP}/token`, { value: 'kept' }), await api('PUT', `/vault/${SWITCH}/token`, { value: 'doomed' })];
    check(v.every((r) => r.status() === 200), `${KEEP} and ${SWITCH} hold data: a vault key each (${v.map((r) => r.status())})`);
    writeTile(KEEP, ['user']);
    writeTile(SWITCH, null);
    const k = await until(async () => { const c = await comp(KEEP); return c.partition?.state === 'pending' && c; }, `${KEEP} pending`);
    const s = await until(async () => { const c = await comp(SWITCH); return c.partition?.state === 'pending' && c; }, `${SWITCH} pending`);
    check(!k.partition.user && k.partition.request?.user, `${KEEP}: unpartitioned → user, pending (${JSON.stringify(k.partition)})`);
    check(s.partition.user && !s.partition.request?.user, `${SWITCH}: user → unpartitioned, pending (${JSON.stringify(s.partition)})`);

    // ---- 1. the marker ----
    B = await login(browser, 'dev1', 'devpass123');
    await openShell(B.page);
    await usePersonalScreen(B.page);
    for (const t of [PART, KEEP, SWITCH]) await openTile(B.page, t);
    await B.page.locator(`${row(PART)} .pm`).waitFor({ timeout: 10000 });
    const want = 'Partitioned: each person here has their own data';
    const mr = await markFacts(B.page, `${row(PART)} .pm`);
    check(mr && mr.role === 'img' && mr.title.startsWith(want) && mr.label === mr.title && /global instance/.test(mr.title),
      `the sidebar row carries the marker, role img, its tooltip naming the global instance (${JSON.stringify(mr)})`);
    check(mr && mr.next.includes('more'), `the row's marker sits right before ⋯ (${mr?.next})`);
    check(mr && mr.cursor === 'default' && mr.border === 'none' && mr.color === TEAL && mr.w === 8, `status, not a button: cursor default, no border, teal, 8px (${JSON.stringify(mr)})`);
    await B.page.locator(`${row(PART)} .pm`).hover();
    const mh = await markFacts(B.page, `${row(PART)} .pm`);
    check(mh && mh.color === mr.color && mh.cursor === 'default', 'no hover state');
    const hd = await markFacts(B.page, `${card(PART)} .head .pm`);
    check(hd && hd.role === 'img' && hd.title === mr.title && hd.cursor === 'default', `the window head carries it too (${JSON.stringify(hd)})`);
    check(await B.page.locator(`${card(PART)} .head .c`).count() === 0, 'in the runtime dot\'s place: the head has no dot');
    check(await B.page.locator(`${card(KEEP)} .head .c`).count() === 1 && await B.page.locator(`${card(KEEP)} .head .pm`).count() === 0
      && await B.page.locator(`${row(KEEP)} .pm`).count() === 0, `an unpartitioned tile (${KEEP}, even pending into partitions) keeps its dot and has no marker`);
    check(await B.page.locator(`${card(SWITCH)} .head .pm`).count() === 1, `a pending switch doesn't change the marker (${SWITCH} still runs user)`);
    await shotEl(B.page, row(PART), 'partition-mark-row');
    await shotEl(B.page, `${card(PART)} .head`, 'partition-mark-head');

    // ---- 2. pending: a reader sees the words, a manager keeps ----
    R = await login(browser, 'preader', 'preaderpass123');
    await openShell(R.page);
    await usePersonalScreen(R.page);
    await openTile(R.page, KEEP);
    await R.page.locator(`${card(KEEP)} .pover`).waitFor({ timeout: 15000 });
    const rtext = await R.page.locator(`${card(KEEP)} .pover`).innerText();
    check(rtext.includes(`A partition mode switch is requested for ${KEEP} (unpartitioned → user)`) && /deletes all data in this tile/.test(rtext),
      `a reader's card greys out under the request's words (${rtext.slice(0, 160)})`);
    check(await R.page.locator(`${card(KEEP)} .pover button`).count() === 0 && rtext.includes('A manager of'), 'a reader gets no Keep/Switch, only who decides');
    await shotEl(R.page, card(KEEP), 'partition-pending-reader');
    await closeTile(R.page, KEEP);
    await closeCtx(R.ctx, R.page);
    R = null;

    const ov = B.page.locator(`${card(KEEP)} .pover`);
    await ov.waitFor({ timeout: 15000 });
    check(await ov.locator('button.pkeep').count() === 1 && (await ov.locator('button.pswitch').innerText()).includes('Switch and delete all data…'),
      'a manager gets Keep the current mode and Switch and delete all data…');
    const gray = await ov.evaluate((el) => getComputedStyle(el).backdropFilter || getComputedStyle(el).webkitBackdropFilter);
    check(/grayscale/.test(gray), `the card greys out (${gray})`);
    await shotEl(B.page, card(KEEP), 'partition-pending-manager');
    await ov.locator('button.pkeep').click();
    await B.page.locator(`${card(KEEP)} .pover`).waitFor({ state: 'detached', timeout: 15000 });
    const kept = await comp(KEEP);
    check(kept.partition?.state === 'unpartitioned' && kept.partition?.request?.declined === true, `Keep: ${KEEP} runs unpartitioned, user declined (${JSON.stringify(kept.partition)})`);
    const f = await until(async () => (await tileFrame(B.page, KEEP)).evaluate(() => !!document.getElementById('pkeep-tile')).catch(() => false), 'the kept tile in its frame');
    check(f, 'the frame shows the tile itself again');
    check(await vaultHas(KEEP), 'keep deleted nothing: the vault key is still there');

    // ---- 3. pending: Switch… and the typed confirmation ----
    const sw = B.page.locator(`${card(SWITCH)} .pover`);
    await sw.waitFor({ timeout: 15000 });
    await sw.locator('button.pswitch').click();
    const dlg = B.page.locator('bx-canvas bx-dialog .box');
    await dlg.waitFor({ timeout: 15000 });
    const dtext = await dlg.innerText();
    check(dtext.includes(`Switching ${SWITCH} from user to unpartitioned deletes all data in this tile`) && /1 vault key/.test(dtext) && dtext.includes('It keeps:'),
      `the dry run's counts and keep list (${dtext.slice(0, 220).replace(/\n/g, ' ⏎ ')})`);
    check(await vaultHas(SWITCH), 'the dry run deleted nothing');
    await shot(B.page, 'partition-switch-confirm');
    await dlg.locator('input[name=confirm]').fill('apps/nope');
    await dlg.locator('button.danger').click();
    await dlg.locator('.err').waitFor({ timeout: 5000 });
    check((await dlg.locator('.err').innerText()).includes(`Type the tile's path exactly (${SWITCH})`), 'a wrong path is refused in the dialog');
    check(await vaultHas(SWITCH), '… and deletes nothing');
    await dlg.locator('input[name=confirm]').fill(SWITCH);
    await dlg.locator('button.danger').click();
    await dlg.waitFor({ state: 'detached', timeout: 15000 });
    await B.page.locator(`${card(SWITCH)} .pover`).waitFor({ state: 'detached', timeout: 20000 });
    // unpartitioned, and its code asks nothing else: the row carries no partition (or says so)
    const after = await until(async () => { const c = await comp(SWITCH); return c.path && (!c.partition || c.partition.state === 'unpartitioned' && !c.partition.request) && c; }, `${SWITCH} switched`);
    check(!!after, `Switch: ${SWITCH} runs unpartitioned, no request left (${JSON.stringify(after.partition ?? null)})`);
    check(!(await vaultHas(SWITCH)), 'the switch deleted the tile\'s data: its vault key is gone');
    await settle(B.page);
    check(await B.page.locator(`${card(SWITCH)} .head .pm`).count() === 0 && await B.page.locator(`${card(SWITCH)} .head .c`).count() === 1
      && await B.page.locator(`${row(SWITCH)} .pm`).count() === 0, 'the marker goes with the partitions: the dot is back');
    await shot(B.page, 'partition-after-switch');
    for (const t of [PART, KEEP, SWITCH]) await closeTile(B.page, t);
  } finally {
    if (R) await closeCtx(R.ctx, R.page);
    if (B) await closeCtx(B.ctx, B.page);
    writeTile(KEEP, null); // withdrawn
    await api('DELETE', `/vault/${KEEP}/token`).catch(() => {});
    await api('DELETE', '/users/preader').catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { partitionMark };
