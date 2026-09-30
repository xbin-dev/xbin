// hack/ui-harness/passes/partitionmark.js — covers PD-53 (design A) and
// 06 §12.3 — the scaffold shell on partitioned tiles:
//   1. apps/ppart (user + global; holds nothing, so its mode is recorded at
//      once) carries the marker — a teal half-split disc — on its sidebar
//      row (right before ⋯) and on its window head (where the runtime dot
//      is): role img, the tooltip naming the global instance, cursor
//      default, no hover state; its head's partition chip (owner ruling
//      I3) says `yours`, quiet, in the marker's hue, with its tooltip; an
//      unpartitioned tile keeps its dot and has no chip; a pop-out window
//      (xbin.window) framing apps/ppart — a sub-path of its own, or
//      spec.src from another tile — carries the marker and `yours` too, one
//      of an unpartitioned tile neither;
//   1b. a deployment of apps/ppart (01 §2.8: one instance its writers
//      share): the window showing it has no marker — the runtime dot and
//      the chip `shared` — while the row keeps its marker; back on the
//      primary, the marker again and `yours`;
//   1c. the workspace token (no person): apps/ppart's window shows its
//      global instance — the chip `global` (its pop-out too); apps/pswitch
//      (no global instance) says `no partition`; so does apps/ppart's
//      window for an admin viewing the workspace as dev1 (view-as opens no
//      partition);
//   2. apps/pkeep (org devs; holds a vault key; its code starts asking for
//      user partitions, with a partitionNote): pending — its card greys out
//      under the alert's words and the tile's note; preader (reads it,
//      manages nothing) sees no buttons, only who decides (an admin of
//      org:devs); dev1 (an admin of devs) presses Keep the current mode →
//      the overlay goes, the frame shows the tile, the vault key is still
//      there; the code withdraws and asks again (the same request): the
//      card offers Keep and Switch… again, not the last answer;
//   3. apps/pswitch (user:dev1; recorded user, holds a vault key; its code
//      drops partition): pending, and the marker stays; Switch… shows the
//      dry run's counts and keep list in a typed confirmation; a wrong path
//      is refused there, the tile's path switches: the tile runs
//      unpartitioned, the vault key is gone, the marker and overlay go.
// pkeep's manifest goes back to unpartitioned (withdrawn), ppart's
// deployment and preader are removed at the end; the tiles stay (a rerun
// starts them over).
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, tileFrame, settle, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const PART = 'apps/ppart', KEEP = 'apps/pkeep', SWITCH = 'apps/pswitch', DEP = 'pdep';
const NOTE = 'Your notes live here, one list per person.';
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
function writeTile(tile, partition, note = '') {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  const id = tile.slice(tile.lastIndexOf('/') + 1) + '-tile';
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1 id="${id}">${tile} itself</h1>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: tile, ...(partition ? { partition } : {}), ...(note ? { partitionNote: note } : {}) }));
}

const card = (tile) => `bx-canvas .card[data-path="${tile}"]`;
const NOTES = 'apps/dev1-notes'; // the seed's: dev1's, unpartitioned
// pop(page, tile, spec) → the head of the pop-out window tile's frame opens
// (xbin.window(spec)); spec.title names it
async function pop(page, tile, spec) {
  await (await tileFrame(page, tile)).evaluate((sp) => { window.xbin.window(sp); }, spec);
  const head = page.locator('bx-shell .spawn .shead').filter({ has: page.locator('.stitle', { hasText: new RegExp(`^${spec.title}$`) }) });
  await head.waitFor({ timeout: 10000 });
  return head;
}
const popFacts = (head) => head.evaluate((el) => ({ mark: !!el.querySelector('.pm[role=img]'), first: el.firstElementChild?.className || '',
  chip: el.querySelector('.pchip')?.textContent.trim() || '', kind: el.querySelector('.pchip')?.dataset.chip || '',
  title: el.querySelector('.pchip')?.title || '', color: el.querySelector('.pchip') ? getComputedStyle(el.querySelector('.pchip')).color : '',
  prev: el.querySelector('.pchip')?.previousElementSibling?.className || '' }));
const row = (tile) => `bx-side .item[data-path="${tile}"]`;
// the marker's facts, wherever sel finds it
const markFacts = (page, sel) => page.locator(sel).first().evaluate((el) => {
  const cs = getComputedStyle(el);
  return { role: el.getAttribute('role'), title: el.title, label: el.getAttribute('aria-label'), cursor: cs.cursor, color: cs.color,
    border: cs.borderTopStyle, next: el.nextElementSibling?.className || '', w: el.getBoundingClientRect().width };
}).catch(() => null);

// the partition chip's facts (owner ruling I3)
const chipFacts = (page, sel) => page.locator(sel).first().evaluate((el) => {
  const cs = getComputedStyle(el);
  return { text: el.textContent.trim(), kind: el.dataset.chip, title: el.title, cursor: cs.cursor, color: cs.color, bg: cs.backgroundColor,
    border: cs.borderTopStyle, prev: el.previousElementSibling?.className || '' };
}).catch(() => null);

// pick what a window shows from its head's ⇈ menu
async function pickDeployment(page, head, name) {
  const menu = page.locator('bx-canvas bx-menu[open]');
  await head.locator('button.dpb').click();
  await menu.waitFor({ timeout: 10000 });
  await menu.locator('button.it').filter({ has: page.locator('.lb', { hasText: new RegExp(`^${name}$`) }) }).click();
  await menu.waitFor({ state: 'detached', timeout: 10000 });
}

async function partitionMark(browser) {
  const { check, done } = checker('partition-mark');
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const comp = async (tile) => ((await (await api('GET', `/components/${tile}`)).json().catch(() => ({}))).component || {});
  const vaultHas = async (tile) => JSON.stringify(await (await api('GET', `/vault/${tile}`)).json().catch(() => ({}))).includes('token');
  // a deployments op, waiting out the checkpoint rate (429 "… retry in Ns")
  const deployOp = async (op, data) => {
    for (let i = 0; ; i++) {
      const r = await api('POST', `/deployments/${op}`, data);
      const body = await r.json().catch(() => ({}));
      const m = r.status() === 429 && /retry in (\d+)s/.exec(body?.error || '');
      if (!m || i === 20) return { status: r.status(), error: body?.error || '' };
      await sleep(Number(m[1]) * 1000 + 100);
    }
  };
  let B, R, T, V;
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
    writeTile(KEEP, ['user'], NOTE);
    writeTile(SWITCH, null);
    const k = await until(async () => { const c = await comp(KEEP); return c.partition?.state === 'pending' && c; }, `${KEEP} pending`);
    const s = await until(async () => { const c = await comp(SWITCH); return c.partition?.state === 'pending' && c; }, `${SWITCH} pending`);
    check(!k.partition.user && k.partition.request?.user && k.partition.note === NOTE, `${KEEP}: unpartitioned → user, pending, its note on the row (${JSON.stringify(k.partition)})`);
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
    const ch = await chipFacts(B.page, `${card(PART)} .head .pchip`);
    check(ch && ch.text === 'yours' && ch.kind === 'yours' && /^Your partition: this window shows your own data/.test(ch.title),
      `the head's partition chip says whose partition the window shows: yours, with its tooltip (${JSON.stringify(ch)})`);
    check(ch && ch.cursor === 'default' && ch.border === 'none' && ch.color === TEAL && ch.prev === 't',
      `the chip is quiet, in the marker's hue, after the path: no border, cursor default (${JSON.stringify(ch)})`);
    await B.page.locator(`${card(PART)} .head .pchip`).hover();
    const chh = await chipFacts(B.page, `${card(PART)} .head .pchip`);
    check(chh && chh.color === ch?.color && chh.bg === ch?.bg && chh.cursor === 'default', 'the chip has no hover state');
    check(await B.page.locator(`${card(KEEP)} .head .pchip`).count() === 0, `an unpartitioned tile's window has no chip (${KEEP})`);
    await shotEl(B.page, row(PART), 'partition-mark-row');
    await shotEl(B.page, `${card(PART)} .head`, 'partition-mark-head');
    // pop-out windows (xbin.window) are windows of the tile they frame
    await openTile(B.page, NOTES);
    const p1 = await pop(B.page, PART, { path: 'pop', title: 'pop-own', x: 700, y: 480, width: 320, height: 160 });
    const f1 = await popFacts(p1);
    check(f1.mark && f1.first === 'pm' && f1.chip === 'yours' && f1.kind === 'yours' && /^Your partition: /.test(f1.title) && f1.color === TEAL && f1.prev === 'stitle',
      `a pop-out of ${PART}'s own sub-path: the marker first, then the title and the chip yours (${JSON.stringify(f1)})`);
    await shotEl(B.page, 'bx-shell .spawn', 'partition-chip-popout');
    const p2 = await pop(B.page, NOTES, { src: PART, title: 'pop-src', x: 740, y: 520, width: 320, height: 160 });
    const f2 = await popFacts(p2);
    check(f2.mark && f2.chip === 'yours', `a pop-out another tile (${NOTES}) opens on ${PART} (spec.src): the marker and yours (${JSON.stringify(f2)})`);
    const p3 = await pop(B.page, NOTES, { path: 'pop', title: 'pop-plain', x: 780, y: 560, width: 320, height: 160 });
    const f3 = await popFacts(p3);
    check(!f3.mark && !f3.chip, `a pop-out of an unpartitioned tile: no marker, no chip (${JSON.stringify(f3)})`);
    for (const h of [p3, p2, p1]) { await h.locator('button').click(); await h.waitFor({ state: 'detached', timeout: 5000 }); }
    await closeTile(B.page, NOTES);

    // ---- 1b. a window on a deployment: its writers' shared instance, no marker ----
    const added = await deployOp('add', { tile: PART, deployment: DEP });
    check(added.status === 200, `${PART} gains a deployment ${DEP} (${added.status} ${added.error})`);
    if (added.status === 200) {
      const head = B.page.locator(`${card(PART)} .head`);
      await head.locator('button.dpb').waitFor({ timeout: 15000 });
      await pickDeployment(B.page, head, DEP);
      await head.locator('.dtag').waitFor({ timeout: 10000 });
      check(await head.locator('.pm').count() === 0 && await head.locator('.c').count() === 1,
        `a window showing ${DEP} carries no per-person marker: the runtime dot`);
      const chip = await head.locator('.dshare').first().evaluate((el) => ({ text: el.textContent.trim(), title: el.title, kind: el.dataset.chip })).catch(() => null);
      check(chip?.text === 'shared' && chip.kind === 'shared' && /^Not partitioned: .*every writer of the tile shares/.test(chip.title),
        `… and its partition chip says shared (${JSON.stringify(chip)})`);
      check(await B.page.locator(`${row(PART)} .pm`).count() === 1, 'the sidebar row (the tile itself) keeps its marker');
      await shotEl(B.page, `${card(PART)} .head`, 'partition-mark-deployment');
      await pickDeployment(B.page, head, 'main');
      await head.locator('.dtag').waitFor({ state: 'detached', timeout: 10000 });
      check(await head.locator('.pm').count() === 1 && await head.locator('.dshare').count() === 0
        && (await head.locator('.pchip').innerText()) === 'yours', 'back on the primary: the marker again, and the chip says yours');
    }

    // ---- 1c. the workspace token: no person, so the global instance ----
    const token = fs.readFileSync(path.join(WS, '.xbin', 'token'), 'utf8').trim();
    T = await browser.newContext({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1 });
    const tp = await T.newPage();
    await tp.goto(`${URL}/login?token=${encodeURIComponent(token)}`);
    await openShell(tp);
    await usePersonalScreen(tp);
    for (const t of [PART, SWITCH]) await openTile(tp, t);
    await tp.locator(`${card(PART)} .head .pchip`).waitFor({ timeout: 15000 });
    const gc = await chipFacts(tp, `${card(PART)} .head .pchip`);
    check(gc && gc.text === 'global' && /^The global instance: /.test(gc.title) && gc.color === TEAL,
      `the workspace token's window on ${PART} shows its global instance: the chip says global (${JSON.stringify(gc)})`);
    const nc = await chipFacts(tp, `${card(SWITCH)} .head .pchip`);
    check(nc && nc.text === 'no partition' && /^No partition: the workspace token has none of its own/.test(nc.title) && nc.color !== TEAL,
      `on ${SWITCH}, which has no global instance, it says no partition, muted (${JSON.stringify(nc)})`);
    await shotEl(tp, `${card(PART)} .head`, 'partition-chip-global');
    const tpop = await pop(tp, PART, { path: 'pop', title: 'pop-global', x: 700, y: 480, width: 320, height: 160 });
    const tf = await popFacts(tpop);
    check(tf.mark && tf.chip === 'global', `the workspace token's pop-out of ${PART}: global (${JSON.stringify(tf)})`);
    await tpop.locator('button').click();
    await shotEl(tp, `${card(SWITCH)} .head`, 'partition-chip-none');
    for (const t of [PART, SWITCH]) await closeTile(tp, t);
    await closeCtx(T, tp);
    T = null;
    // an admin viewing the workspace as dev1: view-as never opens a person's partition
    V = await login(browser, 'admin', 'admin');
    const tk = await (await V.ctx.request.post(`${URL}/api/xbin/impersonate`, { data: { user: 'dev1' } })).json().catch(() => ({}));
    if (tk.url) {
      await V.page.goto(tk.url.startsWith('http') ? tk.url : `${URL}${tk.url}`);
      await openShell(V.page);
      await V.page.locator(`${card(PART)} .head .pchip`).waitFor({ timeout: 15000 });
      const vc = await chipFacts(V.page, `${card(PART)} .head .pchip`);
      check(vc && vc.text === 'no partition' && /^No partition: viewing the workspace as dev1 never opens their partition/.test(vc.title),
        `viewing as dev1, ${PART}'s window says no partition (${JSON.stringify(vc)})`);
      await shotEl(V.page, `${card(PART)} .head`, 'partition-chip-viewas');
      await V.ctx.request.post(`${URL}/api/xbin/impersonate/stop`).catch(() => {});
    } else {
      check(false, `a view-as ticket for dev1 (${JSON.stringify(tk)})`);
    }
    await V.ctx.close();
    V = null;

    // ---- 2. pending: a reader sees the words, a manager keeps ----
    R = await login(browser, 'preader', 'preaderpass123');
    await openShell(R.page);
    await usePersonalScreen(R.page);
    await openTile(R.page, KEEP);
    await R.page.locator(`${card(KEEP)} .pover`).waitFor({ timeout: 15000 });
    const rtext = await R.page.locator(`${card(KEEP)} .pover`).innerText();
    check(rtext.includes(`A partition mode switch is requested for ${KEEP} (unpartitioned → user)`) && /deletes all data in this tile/.test(rtext)
      && !rtext.includes('switch|keep'), `a reader's card greys out under the request's words, without the CLI hint (${rtext.slice(0, 160)})`);
    check(rtext.includes(`${KEEP} says: ${NOTE}`), 'the card shows the tile\'s partitionNote, attributed to it');
    check(await R.page.locator(`${card(KEEP)} .pover button`).count() === 0 && rtext.includes('Who decides: an admin of org:devs, which owns it, or a workspace admin'),
      'a reader gets no Keep/Switch, only who decides: the owning org\'s admins');
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
    check(kept.partition?.state === 'unpartitioned' && kept.partition?.request?.declined === true && !kept.partition?.note,
      `Keep: ${KEEP} runs unpartitioned, user declined, no note on the row (${JSON.stringify(kept.partition)})`);
    const f = await until(async () => (await tileFrame(B.page, KEEP)).evaluate(() => !!document.getElementById('pkeep-tile')).catch(() => false), 'the kept tile in its frame');
    check(f, 'the frame shows the tile itself again');
    check(await vaultHas(KEEP), 'keep deleted nothing: the vault key is still there');
    // the code withdraws, then asks for the same mode again: a new request
    writeTile(KEEP, null);
    await until(async () => { const c = await comp(KEEP); return c.path && !c.partition?.request && c; }, `${KEEP} withdrawn`);
    writeTile(KEEP, ['user'], NOTE);
    await until(async () => (await comp(KEEP)).partition?.state === 'pending', `${KEEP} pending again`);
    await ov.waitFor({ timeout: 15000 });
    await ov.locator('button.pkeep').waitFor({ timeout: 5000 }).catch(() => {});
    check(await ov.locator('button.pkeep').count() === 1 && await ov.locator('button.pswitch').count() === 1 && await ov.locator('.pdone').count() === 0,
      'the same request again (unpartitioned → user): Keep and Switch… again, not the last answer');
    await ov.locator('button.pkeep').click();
    await B.page.locator(`${card(KEEP)} .pover`).waitFor({ state: 'detached', timeout: 15000 });
    check((await comp(KEEP)).partition?.request?.declined === true, 'and it keeps again');

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
    if (T) await T.close();
    if (V) await V.ctx.close();
    if (B) await closeCtx(B.ctx, B.page);
    writeTile(KEEP, null); // withdrawn
    await api('DELETE', `/vault/${KEEP}/token`).catch(() => {});
    await deployOp('remove', { tile: PART, deployment: DEP, confirm: 'erase' }).catch(() => {});
    await api('DELETE', '/users/preader').catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { partitionMark };
