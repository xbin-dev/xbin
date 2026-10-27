// hack/ui-harness/passes/limitscaps.js — covers D203's UI (tile resource
// limits) end to end against the real API (GET/POST/DELETE /api/xbin/limits,
// /limits/dismiss, PUT /orgs/{org}/limits, /xbin/limits). The harness xbind
// has no cgroup delegation, no VM runtime and no tile sandboxes, so the one
// kind it enforces is the data disk quota (diskGiB, the disk monitor's own):
// three tiles that root their scope ask `"limits": {"disk": "80G"}` above
// the 50 GiB default — apps/lim-a (the workspace's; it also asks memory,
// which can't apply: a placement note), apps/lim-org (org devs, whose admin
// is dev1) and apps/lim-mine (dev1's own). Only a cap hit can't be produced
// here (an OOM needs cgroups, a full quota 50 GiB of writes): the banners'
// alerts are served by route interception — their raises still go to the
// real POST /limits.
//   1. the admin: lim-a's window has a key badge and a popover with its
//      ask (shot: limitscaps-popover-<theme>); Approve → the real row's
//      effective is the ask, approved.source "manifest-ask";
//   2. dev1, org devs' admin, with the org's disk ceiling (60 GiB) under
//      lim-org's ask: Approve is greyed with the ceiling's reason, and a POST
//      as dev1 is refused (403) (shot: limitscaps-ceiling-<theme>); the
//      admin raises the ceiling in the admin console's orgs tab
//      (<bx-org-limits>; shot: limitscaps-orgceil-<theme>), then dev1's
//      Approve goes through;
//   3. served cap alerts with an action: an OOM (memory, which the harness
//      can't raise: the server's refusal shows in a toast) and a full disk
//      quota on lim-a — +25 / +50 / +100 %, +50 the primary, +100 greyed
//      with its reason (title; listed when the banner opens); the disk's
//      +50 → the real row's set.source is "raise"; Dismiss POSTs the alert's
//      route; a phone puts the choices on their own line (shots:
//      limitscaps-banner-*);
//   4. the admin console's runtime → limits tab: the alert bar's raise…
//      narrows to the tile; lim-mine's Approve; set… a typed value on lim-a
//      (source admin-set; shot: limitscaps-admin-<theme>), then revert
//      (DELETE): the default again and the ask pending;
//   5. the tile's ⚙ → Limits: dev1 as lim-mine's owner reads the decision,
//      has no set…, and reverts it; as org devs' admin on lim-org, a set…
//      above the ceiling is refused in the editor and one under it holds
//      (shots: limitscaps-gear-owner-<theme>, limitscaps-gear-org-<theme>);
//   6. /xbin/limits: served byte for byte; framed, refused by its headers
//      and, with them stripped, by the element itself; the table and the
//      org ceilings, a tile's own page, a phone (shots: limitscaps-page-*);
//      dev1 reads their tiles and their org's ceilings, read-only.
// The tiles, their decisions, the org's ceiling and the dismissals are
// removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, settle, sh, shot, shotEl, checker, sleep, THEME, gotoTab } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const T = 'apps/lim-a', TORG = 'apps/lim-org', TMINE = 'apps/lim-mine', TILES = [T, TORG, TMINE];
const ASK = 80, CEIL = 60, CEIL2 = 100; // GiB
const enc = encodeURIComponent;
const ago = (min) => new Date(Date.now() - min * 60000);
const clock = (d) => d.toISOString().slice(0, 16).replace('T', ' ');

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

// a tile that roots its own scope (scope.json) and asks for more
function writeTile(t, limits) {
  const dir = path.join(WS, t);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${t}</h1><p>a tile that asks for a bigger disk</p>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: t.split('/')[1], limits }) + '\n');
  fs.writeFileSync(path.join(dir, 'scope.json'), '{}\n');
}

// ---- the served cap alerts (a hit can't be produced here) ----
const WHY = (need, unit) => `needs +${need} ${unit}; 31 GiB free after the 10% reserve and 12 GiB granted to other tiles but unused`;
function capAlerts(diskNow) {
  const d = (pct) => Math.max(Math.ceil((diskNow * (100 + pct)) / 100), diskNow + 1);
  const choice = (pct, value, why) => ({ pct, value, fits: pct !== 100, approvable: true, ...(pct === 50 ? { default: true } : {}), ...(pct === 100 ? { why } : {}) });
  const disk = [25, 50, 100].map((pct) => choice(pct, d(pct), WHY(d(100) - diskNow, 'GiB')));
  const mem = [[25, 2560], [50, 3072], [100, 4096]].map(([pct, value]) => choice(pct, value, WHY(2, 'GiB')));
  const tail = (alias) => ` — raise it: the tile's ⚙ → Limits, /xbin/limits, or \`bx limits raise ${T} ${alias}\``;
  return {
    disk, mem,
    alerts: [
      { level: 'warn', kind: 'oom', tile: T, dismiss: `/limits/dismiss?tile=${enc(T)}&kind=memMiB`,
        message: `${T} was OOM-killed at its 2 GiB memory limit (2 processes), 3× since ${clock(ago(52))} UTC${tail('memory')}`,
        action: { action: 'raise-limit', tile: T, kind: 'memMiB', unit: 'MiB', current: 2048, approvers: ['workspace-admin'], choices: mem } },
      { level: 'crit', kind: 'quota', tile: T, dismiss: `/limits/dismiss?tile=${enc(T)}&kind=diskGiB`,
        message: `${T} met its ${diskNow} GiB disk quota at ${clock(ago(4))} UTC${tail('disk')}`,
        action: { action: 'raise-limit', tile: T, scope: T, kind: 'diskGiB', unit: 'GiB', current: diskNow, approvers: ['workspace-admin'], choices: disk } },
    ],
  };
}
// serve them after the real alerts (every other call passes through)
async function serveAlerts(page, extra) {
  const h = async (route) => {
    const resp = await route.fetch();
    const j = await resp.json().catch(() => ({}));
    await route.fulfill({ response: resp, json: { ...j, alerts: [...(j.alerts ?? []), ...extra] } });
  };
  await page.route('**/api/xbin/alerts', h);
  return () => page.unroute('**/api/xbin/alerts', h);
}

// what a tile's popover shows of its limits
const popover = (page, tile) => page.evaluate((t) => {
  const sh = document.querySelector('bx-shell')?.shadowRoot, cv = sh?.querySelector('bx-canvas')?.shadowRoot;
  const pop = cv?.querySelector(`.pqpop[data-prompts-for="${t}"]`);
  const lim = pop?.querySelector('bx-limits')?.shadowRoot;
  const card = cv?.querySelector(`.card[data-path="${t}"]`);
  return {
    pop: !!pop, head: pop?.querySelector('.pqh span')?.textContent ?? '', badge: Number(card?.querySelector('.pqb')?.dataset.prompts ?? 0),
    plates: [...(lim?.querySelectorAll('.plate') ?? [])].map((p) => {
      const b = p.querySelector('[data-approve]');
      return { key: p.dataset.limit, why: p.dataset.why, text: p.textContent.replace(/\s+/g, ' ').trim(),
        approve: b ? { text: b.textContent.trim(), disabled: b.disabled, title: b.title } : null,
        ask: p.querySelector('.ask')?.textContent.trim() ?? '', whys: p.querySelector('[data-whys]')?.textContent.replace(/\s+/g, ' ').trim() ?? '' };
    }),
  };
}, tile);

async function limitsCaps(browser) {
  const { check, skip, done } = checker(`limitscaps-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  const A = await login(browser, 'admin', 'admin');
  const D = await login(browser, 'dev1', 'devpass123');
  const errors = [];
  for (const [who, p] of [['admin', A.page], ['dev1', D.page]]) p.on('pageerror', (e) => errors.push(`${who}: ${e.message}`));
  const call = (ctx) => (method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json' }, ...(data === undefined ? {} : { data: JSON.stringify(data) }) });
  const api = call(A.ctx), dev1 = call(D.ctx);
  const row = async (t, as = api) => (await (await as('GET', `/limits?tile=${enc(t)}`)).json()).tile;
  const disk = async (t, as) => (await row(t, as))?.limits?.diskGiB;
  const orgCeil = async () => ((await (await api('GET', '/orgs')).json()).orgs ?? []).find((o) => o.id === 'devs')?.limits?.diskGiB ?? 0;
  let unserve = null;
  try {
    // ---- the tiles, from a clean slate (a --shots rerun meets the last run's decisions) ----
    writeTile(T, { disk: `${ASK}G`, memory: '3G' });
    writeTile(TORG, { disk: `${ASK}G` });
    writeTile(TMINE, { disk: `${ASK}G` });
    await until(async () => (await (await api('GET', '/components')).json()).filter((c) => TILES.includes(c.path)).length === 3, 'the tiles registered');
    for (const [t, to] of [[TORG, 'org:devs'], [TMINE, 'user:dev1']]) {
      const r = await api('POST', '/owner', { tile: t, to });
      check(r.status() === 200, `${t} is ${to}'s (${r.status()})`);
    }
    for (const t of TILES) await api('DELETE', `/limits?tile=${enc(t)}&kind=diskGiB`);
    for (const as of [api, dev1]) await as('DELETE', '/prefs/dismissed');
    const put = await api('PUT', '/orgs/devs/limits', { diskGiB: CEIL });
    check(put.status() === 200, `org devs' disk ceiling is ${CEIL} GiB (${put.status()})`);
    const all = await (await api('GET', '/limits')).json();
    const def = all.defaults?.diskGiB;
    check(all.enforced?.diskGiB === true && def > 0 && def < CEIL, `the harness enforces the disk quota, default ${def} GiB (below the ceiling and the ask)`);
    for (const t of TILES) {
      const l = await until(async () => { const x = await disk(t); return x?.pending ? x : null; }, `${t}'s pending disk ask`);
      check(l.ask === ASK && l.effective === def && !l.set && !l.approved, `${t} asks ${ASK} GiB, ${def} GiB in force (${JSON.stringify({ ask: l.ask, effective: l.effective })})`);
    }
    const mem = (await row(T)).limits?.memMiB;
    check(mem && mem.enforced === false && /ignored: the tile runs no backend/.test(mem.note ?? ''), `an ask that can't apply carries a placement note (${mem?.note})`);

    // ---- 1. the admin approves lim-a's ask on its window ----
    await openShell(A.page);
    await usePersonalScreen(A.page);
    await openTile(A.page, T);
    await sh(A.page, (t, x) => t.setGeom((tiles) => tiles.map((o) => (o.path === x ? { ...o, w: 640, h: 512 } : o))), T);
    let p = await until(async () => { const v = await popover(A.page, T); return v.plates.length === 1 ? v : null; }, 'lim-a\'s popover');
    const pl = p.plates[0];
    check(pl.key === `${T}|diskGiB|${ASK}` && pl.why === 'ask', `one limit request: lim-a's disk (${pl.key})`);
    check(/1 limit request/.test(p.head) && p.badge === 1, `the key badge and the popover's words count it (${p.badge}; ${p.head})`);
    check(/asks\s*80 GiB \(the manifest’s limits\.disk\)/.test(pl.text) && new RegExp(`now\\s*${def} GiB`).test(pl.text), `the plate says what it asks and what is in force (${pl.text})`);
    check(pl.approve?.text === `Approve ${ASK} GiB` && !pl.approve.disabled, `Approve ${ASK} GiB is live (${JSON.stringify(pl.approve)})`);
    await shotEl(A.page, `bx-canvas .pqpop[data-prompts-for="${T}"]`, `limitscaps-popover-${THEME}`);
    await A.page.locator(`bx-canvas .pqpop[data-prompts-for="${T}"] bx-limits [data-approve]`).click();
    let l = await until(async () => { const x = await disk(T); return x?.approved ? x : null; }, 'the approval recorded');
    check(l.effective === ASK && l.approved.source === 'manifest-ask' && l.approved.value === ASK && l.approved.by === 'user:admin' && !l.pending,
      `the real row: ${ASK} GiB in force, approved.source manifest-ask by the admin (${JSON.stringify(l.approved)})`);
    await until(async () => !(await popover(A.page, T)).pop, 'the popover goes with nothing left to decide');
    check(true, 'the popover and the badge go once it is decided');

    // ---- 2. dev1, org devs' admin, under the org's ceiling ----
    await openShell(D.page);
    await usePersonalScreen(D.page);
    await openTile(D.page, TORG);
    await sh(D.page, (t, x) => t.setGeom((tiles) => tiles.map((o) => (o.path === x ? { ...o, w: 640, h: 512 } : o))), TORG);
    p = await until(async () => { const v = await popover(D.page, TORG); return v.plates.length === 1 ? v : null; }, 'lim-org\'s popover for dev1');
    const ap = p.plates[0].approve;
    check(ap && ap.disabled && /ceiling of 60 GiB/.test(ap.title), `dev1's Approve is greyed with the ceiling's reason (${JSON.stringify(ap)} ${p.plates[0].ask})`);
    check(/ceiling of 60 GiB/.test(p.plates[0].whys), `…and the reason shows under it (${p.plates[0].whys})`);
    await shotEl(D.page, `bx-canvas .pqpop[data-prompts-for="${TORG}"]`, `limitscaps-ceiling-${THEME}`);
    // …and in its ⚙ → Limits, where dev1 still decides the kind (set…, up to the ceiling)
    await sh(D.page, (t, x) => t.openAdminWindow(x, 'limits'), TORG);
    const gk = D.page.locator('bx-tile-admin bx-limits[view=section] .kind[data-kind=diskGiB]');
    await until(async () => (await gk.locator('[data-approve]').count()) === 1, 'lim-org\'s section for dev1');
    const ga = gk.locator('[data-approve]');
    check(await ga.isDisabled() && /ceiling of 60 GiB/.test(await ga.getAttribute('title')) && /ceiling of 60 GiB/.test(await gk.locator('[data-blocked]').innerText()),
      `the ⚙ section greys the ask's Approve with the reason (${await ga.getAttribute('title')})`);
    check(await gk.locator('[data-edit]').count() === 1, 'and still offers set… (a value up to the ceiling)');
    await sh(D.page, (t) => t.closeAdminWindow());
    const refused = await dev1('POST', '/limits', { tile: TORG, kind: 'diskGiB', value: ASK, source: 'manifest-ask' });
    const rj = await refused.json().catch(() => ({}));
    check(refused.status() === 403 && /ceiling of 60 GiB/.test(rj.error ?? ''), `dev1's POST above the ceiling is refused (${refused.status()} ${rj.error})`);
    // the admin raises the ceiling in the console's orgs tab
    await gotoTab(A.page, 'orgs/devs', 'resource ceilings');
    const ol = A.page.locator('bx-org-limits[org=devs]');
    const field = ol.locator('input[data-kind=diskGiB]');
    check(await field.inputValue() === `${CEIL}G`, `the console shows the ceiling (${await field.inputValue()})`);
    await field.fill(`${CEIL2}G`);
    await ol.scrollIntoViewIfNeeded();
    await shotEl(A.page, 'bx-org-limits[org=devs]', `limitscaps-orgceil-${THEME}`);
    await ol.locator('[data-save]').click();
    await until(async () => (await orgCeil()) === CEIL2, 'the ceiling saved');
    await until(async () => (await ol.locator('.ok').count()) === 1, 'the console says it saved');
    check(true, `saved: org devs' disk ceiling is ${CEIL2} GiB`);
    // dev1's popover follows (the users and limits events) and the approval goes through
    p = await until(async () => { const v = await popover(D.page, TORG); return v.plates[0]?.approve && !v.plates[0].approve.disabled ? v : null; }, 'dev1\'s Approve live');
    check(p.plates[0].approve.text === `Approve ${ASK} GiB`, `under the new ceiling dev1's Approve is live (${JSON.stringify(p.plates[0].approve)})`);
    await D.page.locator(`bx-canvas .pqpop[data-prompts-for="${TORG}"] bx-limits [data-approve]`).click();
    l = await until(async () => { const x = await disk(TORG); return x?.approved ? x : null; }, 'dev1\'s approval recorded');
    check(l.effective === ASK && l.approved.by === 'user:dev1' && l.approved.source === 'manifest-ask', `the real row: dev1 approved ${ASK} GiB (${JSON.stringify(l.approved)})`);

    // ---- 3. the cap banners (served), the raise real ----
    const now = (await disk(T)).effective;
    const C = capAlerts(now);
    unserve = await serveAlerts(A.page, C.alerts);
    await openShell(A.page);
    const banner = (kind) => A.page.evaluate((k) => {
      const a = document.querySelector('bx-shell')?.shadowRoot?.querySelector(`.alerts .alert.act[data-kind="${k}"]`);
      if (!a) return null;
      return { msg: a.querySelector('.msg')?.textContent ?? '', h: a.getBoundingClientRect().height, whys: a.querySelector('.awhys')?.textContent ?? '',
        choices: [...a.querySelectorAll('.achoice')].map((b) => ({ pct: b.dataset.pct, primary: b.classList.contains('primary'), disabled: b.disabled, title: b.title, text: b.textContent.trim() })) };
    }, kind);
    let b = await until(() => banner('quota'), 'the disk banner');
    const bo = await until(() => banner('oom'), 'the OOM banner');
    for (const [name, x, ch] of [['OOM', bo, C.mem], ['disk', b, C.disk]]) {
      check(x.choices.map((c) => c.pct).join() === '25,50,100', `the ${name} banner offers +25 / +50 / +100 % (${x.choices.map((c) => c.text)})`);
      check(x.choices.find((c) => c.pct === '50')?.primary && !x.choices.find((c) => c.pct === '50').disabled, `…+50 % its primary`);
      const c100 = x.choices.find((c) => c.pct === '100');
      check(c100?.disabled && c100.title === ch[2].why, `…+100 % greyed with its reason in the title (${c100?.title})`);
      check(x.h <= 40, `…on one line on a wide screen (${x.h}px)`);
      check(/\/xbin\/limits/.test(x.msg), `…its message names /xbin/limits for shells that can't act on it (${x.msg})`);
    }
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-${THEME}`);
    await A.page.locator('bx-shell .alerts .alert.act[data-kind=quota] button.msg').click();
    b = await until(async () => { const v = await banner('quota'); return v?.whys ? v : null; }, 'the open banner\'s reasons');
    check(b.whys.includes(C.disk[2].why), `an open banner lists why a choice is greyed (${b.whys})`);
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-open-${THEME}`);
    // the OOM's +50: memory can't be raised here — the server's refusal, in a toast
    await A.page.locator('bx-shell .alerts .alert.act[data-kind=oom] .achoice[data-pct="50"]').click();
    let toast = await until(async () => (await sh(A.page, (t) => t.toasts)).find((x) => x.level === 'error' && x.message.startsWith(`${T}:`)), 'the refusal\'s toast');
    check(/runs no backend|aren.t enforced/.test(toast.message), `a refused raise says why in a toast (${toast.message})`);
    // the disk's +50: the real server records a raise
    const raiseReq = A.page.waitForResponse((r) => r.url().endsWith('/api/xbin/limits') && r.request().method() === 'POST');
    await A.page.locator('bx-shell .alerts .alert.act[data-kind=quota] .achoice[data-pct="50"]').click();
    const rr = await raiseReq;
    check(rr.status() === 200 && JSON.stringify(rr.request().postDataJSON()) === JSON.stringify({ tile: T, kind: 'diskGiB', value: C.disk[1].value, source: 'raise' }),
      `+50 % POSTs a raise as the person (${rr.status()} ${rr.request().postData()})`);
    l = await disk(T);
    check(l.set?.source === 'raise' && l.set.value === C.disk[1].value && l.effective === C.disk[1].value && l.set.by === 'user:admin',
      `the real row: set.source raise, ${C.disk[1].value} GiB in force (${JSON.stringify(l.set)})`);
    toast = await until(async () => (await sh(A.page, (t) => t.toasts)).find((x) => x.level === 'ok' && x.message.includes(`raised to ${C.disk[1].value} GiB`)), 'the toast');
    check(!!toast, `a toast says what was raised (${toast.message})`);
    const dis = A.page.waitForResponse((r) => r.url().includes('/api/xbin/limits/dismiss'));
    await A.page.locator('bx-shell .alerts .alert.act[data-kind=quota] .dismiss').click();
    const dr = await dis;
    check(dr.status() === 204 && dr.url().endsWith(C.alerts[1].dismiss), `Dismiss POSTs the alert's route as the person (${dr.status()} ${dr.url()})`);
    // a phone: the choices on their own line
    await A.page.setViewportSize({ width: 390, height: 800 });
    await settle(A.page);
    b = await until(() => banner('quota'), 'the banner on a phone');
    check(b.h > 40, `on a phone the choices wrap below (${b.h}px)`);
    const wideB = await A.page.evaluate(() => document.documentElement.scrollWidth);
    check(wideB <= 390, `no sideways scroll on a phone (${wideB}px)`);
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-phone-${THEME}`);
    await A.page.setViewportSize({ width: 1400, height: 900 });

    // ---- 4. the admin console: runtime → limits ----
    await gotoTab(A.page, 'limits', 'in force');
    const tbl = A.page.locator('bx-limits-table');
    await until(async () => (await tbl.locator('tr.row[data-tile^="apps/lim-"]').count()) >= 4, 'the table\'s rows');
    await A.page.locator('bx-admin .alertbar [data-raise]').first().click();
    await until(async () => (await tbl.locator('tr.row.focus').count()) === 2, 'raise… narrows to the tile');
    check((await tbl.locator('tr.row').count()) === 2 && (await tbl.locator(`tr.row.focus[data-tile="${T}"]`).count()) === 2,
      'the alert bar\'s raise… opens the tab at its tile (its disk and its ignored memory)');
    await unserve(); unserve = null;
    await tbl.locator('input[type=search]').fill('apps/lim-');
    await settle(A.page);
    // lim-mine's ask, approved in the table
    await tbl.locator(`tr.row[data-tile="${TMINE}"][data-kind=diskGiB] [data-approve]`).click();
    l = await until(async () => { const x = await disk(TMINE); return x?.approved ? x : null; }, 'the table\'s approval');
    check(l.effective === ASK && l.approved.source === 'manifest-ask', `the table's Approve: lim-mine at ${ASK} GiB (${JSON.stringify(l.approved)})`);
    // set… a typed value on lim-a
    const ra = tbl.locator(`tr.row[data-tile="${T}"][data-kind=diskGiB]`);
    await ra.locator('[data-edit]').click();
    const ed = tbl.locator('bx-limit-edit');
    await ed.locator('input.v').fill('150G');
    check(/= 150 GiB/.test(await ed.locator('#pv').innerText()), 'the editor previews the parsed value');
    await ed.locator('button.primary').click();
    l = await until(async () => { const x = await disk(T); return x?.set?.source === 'admin-set' ? x : null; }, 'the typed set');
    check(l.effective === 150 && l.set.value === 150, `set… holds: 150 GiB, source admin-set (${JSON.stringify(l.set)})`);
    await until(async () => /set to 150 GiB by admin/.test(await ra.innerText()), 'the row says the decision');
    await shot(A.page, `limitscaps-admin-${THEME}`);
    await ra.locator('[data-revert]').click();
    await tbl.locator('[data-confirm-revert]').click();
    l = await until(async () => { const x = await disk(T); return !x?.set && !x?.approved ? x : null; }, 'the revert');
    check(l.effective === def && l.pending, `revert (DELETE): ${def} GiB again and the ask pending (${JSON.stringify({ effective: l.effective, pending: l.pending })})`);
    await until(async () => /asks 80 GiB/i.test(await ra.innerText()), 'the row says the ask again');
    check(true, 'the row shows the ask pending again');

    // ---- 5. the tile's ⚙ → Limits ----
    // dev1 owns lim-mine: reads the decision, may revert, decides nothing
    await openShell(D.page);
    await sh(D.page, (t, x) => t.openAdminWindow(x, 'limits'), TMINE);
    let sec = D.page.locator('bx-tile-admin bx-limits[view=section]');
    await until(async () => (await sec.locator('.kind[data-kind=diskGiB] [data-decision]').count()) === 1, 'lim-mine\'s section');
    const mine = sec.locator('.kind[data-kind=diskGiB]');
    check(/ask approved up to 80 GiB by admin/.test(await mine.innerText()), `the owner reads the decision (${(await mine.innerText()).replace(/\s+/g, ' ')})`);
    check(await mine.locator('[data-edit]').count() === 0 && await mine.locator('[data-revert]').count() === 1, 'the owner may revert, not set');
    await shotEl(D.page, 'bx-shell .admin-pop', `limitscaps-gear-owner-${THEME}`);
    await mine.locator('[data-revert]').click();
    check(/The manifest’s ask is pending again/.test(await mine.locator('.confirm').innerText()), 'the revert asks first and says the ask pends again');
    await mine.locator('[data-confirm-revert]').click();
    l = await until(async () => { const x = await disk(TMINE, dev1); return !x?.approved ? x : null; }, 'the owner\'s revert');
    check(l.effective === def && l.pending, `the owner's revert holds (${l.effective} GiB, pending)`);
    await until(async () => /waiting for approval/.test(await mine.innerText()), 'the section says the ask waits');
    check(/ask a workspace admin/.test(await mine.innerText()), 'and who to ask');
    await sh(D.page, (t) => t.closeAdminWindow());
    // dev1 admins org devs: lim-org's set… within the ceiling only
    await sh(D.page, (t, x) => t.openAdminWindow(x, 'limits'), TORG);
    sec = D.page.locator('bx-tile-admin bx-limits[view=section]');
    await until(async () => (await sec.locator('.kind[data-kind=diskGiB] [data-edit]').count()) === 1, 'lim-org\'s section with set…');
    const og = sec.locator('.kind[data-kind=diskGiB]');
    await og.locator('[data-edit]').click();
    await og.locator('bx-limit-edit input.v').fill('120G');
    await og.locator('bx-limit-edit button.primary').click();
    const err = await until(async () => { const t = await og.locator('bx-limit-edit .err').innerText(); return t.trim() ? t : null; }, 'the refusal in the editor');
    check(/ceiling of 100 GiB/.test(err), `above the ceiling the editor shows the server's refusal (${err.trim()})`);
    await og.locator('bx-limit-edit input.v').fill('90G');
    await og.locator('bx-limit-edit button.primary').click();
    l = await until(async () => { const x = await disk(TORG, dev1); return x?.set ? x : null; }, 'the org admin\'s set');
    check(l.effective === 90 && l.set.by === 'user:dev1' && l.set.source === 'admin-set', `within it the set holds (${JSON.stringify(l.set)})`);
    await until(async () => /set to 90 GiB by dev1/.test(await og.innerText()), 'the section says it');
    await shotEl(D.page, 'bx-shell .admin-pop', `limitscaps-gear-org-${THEME}`);
    await sh(D.page, (t) => t.closeAdminWindow());

    // ---- 6. /xbin/limits ----
    const raw = await A.ctx.request.get(`${URL}/xbin/limits`, { headers: { Accept: 'text/html' } });
    const h = raw.headers();
    check(raw.status() === 200 && (await raw.text()) === fs.readFileSync(path.join(REPO, 'web', 'limits.html'), 'utf8'),
      'GET /xbin/limits answers web/limits.html byte for byte (no HTML transform)');
    check(h['x-frame-options'] === 'DENY' && /frame-ancestors 'none'/.test(h['content-security-policy'] || ''),
      `it refuses to be framed (${h['x-frame-options']}; ${h['content-security-policy']})`);
    check((await A.ctx.request.get(`${URL}/vendor/limits.html`)).status() === 404, '/vendor/ has no copy of the page');
    await A.page.goto(`${URL}/`);
    await A.page.waitForSelector('bx-shell');
    const framed = await A.page.evaluate(async () => {
      const f = document.createElement('iframe');
      f.src = '/xbin/limits';
      document.body.append(f);
      await new Promise((r) => { f.onload = r; setTimeout(r, 4000); });
      let doc = null;
      try { doc = f.contentDocument; } catch { doc = null; }
      const shown = !!doc?.querySelector('bx-limits-page')?.shadowRoot?.querySelector('main');
      f.remove();
      return { shown };
    });
    check(!framed.shown, 'framed, the browser refuses it: nothing shows');
    const strip = async (route) => {
      if (!route.request().frame().parentFrame()) return route.continue();
      const res = await route.fetch();
      const headers = { ...res.headers() };
      delete headers['x-frame-options'];
      headers['content-security-policy'] = String(headers['content-security-policy'] || '').replace(/;\s*frame-ancestors 'none'/, '');
      return route.fulfill({ response: res, headers });
    };
    await A.page.route('**/xbin/limits?framed=1', strip);
    await A.page.goto(`${URL}/xbin/limits`);
    const pt = A.page.locator('bx-limits-page bx-limits-table');
    await until(async () => (await pt.locator('tr.row').count()) > 0, 'the page\'s table');
    await A.page.evaluate(() => { const f = document.createElement('iframe'); f.src = '/xbin/limits?framed=1'; document.body.append(f); });
    const inner = await until(async () => {
      const fr = A.page.frames().find((f) => f.url().endsWith('/xbin/limits?framed=1'));
      return fr && fr.evaluate(() => {
        const el = document.querySelector('bx-limits-page');
        const alert = el?.shadowRoot?.querySelector('[role=alert]');
        if (!alert) return null;
        return { text: alert.textContent.replace(/\s+/g, ' '), tables: el.shadowRoot.querySelectorAll('bx-limits-table, bx-limits').length, framed: el.testApi().framed(),
          reads: performance.getEntriesByType('resource').filter((e) => e.name.includes('/api/xbin/')).length };
      });
    }, 'the framed element', 10000);
    check(inner.framed && /only when it is opened on its own/.test(inner.text) && inner.tables === 0 && inner.reads === 0,
      `headers stripped, the element in a frame refuses: no table, no read of the API (${JSON.stringify(inner)})`);
    await A.page.unroute('**/xbin/limits?framed=1', strip);
    await A.page.goto(`${URL}/xbin/limits`);
    await until(async () => (await pt.locator('tr.row[data-tile^="apps/lim-"]').count()) >= 4, 'the page lists the tiles');
    check(await pt.locator(`tr.row[data-tile="${TORG}"][data-kind=diskGiB]`).count() === 1, 'the page lists every tile the admin sees');
    await pt.locator('details.org[data-org=devs] summary').click();
    await settle(A.page);
    check(/data disk 100 GiB/.test(await pt.locator('details.org[data-org=devs] summary').innerText()), 'the page lists an org\'s ceilings');
    check(await pt.locator('details.org[data-org=devs] bx-org-limits input[data-kind=diskGiB]').inputValue() === '100G', '…editable for a workspace admin');
    await shot(A.page, `limitscaps-page-${THEME}`);
    await A.page.goto(`${URL}/xbin/limits?tile=${enc(T)}`);
    const one = A.page.locator('bx-limits-page bx-limits[view=section]');
    await until(async () => (await one.locator('.kind').count()) === 2, 'a tile\'s section on the page');
    check(/waiting for approval/.test(await one.locator('.kind[data-kind=diskGiB]').innerText()) && await one.locator('.kind[data-kind=diskGiB] [data-approve]').count() === 1,
      'a tile\'s page: its pending ask with Approve');
    check(/ignored: the tile runs no backend/.test(await one.locator('.kind[data-kind=memMiB]').innerText()), '…and its placement note');
    await shot(A.page, `limitscaps-page-tile-${THEME}`);
    await A.page.setViewportSize({ width: 390, height: 844 });
    await A.page.goto(`${URL}/xbin/limits`);
    await until(async () => (await pt.locator('tr.row').count()) > 0, 'the page on a phone');
    await pt.locator('input[type=search]').fill('apps/lim-');
    await settle(A.page);
    const wide = await A.page.evaluate(() => document.documentElement.scrollWidth);
    check(wide <= 390, `no sideways scroll on a phone (${wide}px)`);
    await shot(A.page, `limitscaps-page-phone-${THEME}`);
    await A.page.setViewportSize({ width: 1400, height: 900 });
    // dev1: their tiles, their org's ceilings read-only
    await D.page.goto(`${URL}/xbin/limits`);
    const dt = D.page.locator('bx-limits-page bx-limits-table');
    await until(async () => (await dt.locator('tr.row').count()) > 0, 'dev1\'s page');
    const dtiles = await dt.locator('tr.row').evaluateAll((rs) => [...new Set(rs.map((r) => r.dataset.tile))]);
    check(dtiles.includes(TORG) && dtiles.includes(TMINE) && !dtiles.includes(T), `dev1 sees the tiles they manage, not the workspace's (${dtiles})`);
    const ro = await until(async () => { const t = await dt.locator('bx-org-limits[org=devs] .meta').innerText(); return /data disk/.test(t) ? t : null; }, 'dev1\'s ceilings');
    check(/data disk 100 GiB/.test(ro) && await dt.locator('bx-org-limits input').count() === 0, `dev1 reads org devs' ceilings, read-only (${ro})`);
    check(errors.length === 0, `no page error (${errors.join(' | ')})`);
    skip('a real cap hit (an OOM needs cgroup delegation, a full quota 50 GiB of writes): the banners\' alerts are served; test/limits drives the real path');
    skip('VM sizes and tile sandbox totals: the harness xbind runs no VM backend and no tile sandboxes (not enforced here)');
  } finally {
    if (unserve) await unserve().catch(() => {});
    for (const t of TILES) await api('DELETE', `/limits?tile=${enc(t)}&kind=diskGiB`).catch(() => {});
    await api('PUT', '/orgs/devs/limits', { diskGiB: null }).catch(() => {});
    for (const as of [api, dev1]) await as('DELETE', '/prefs/dismissed').catch(() => {});
    for (const P of [A, D]) {
      await P.page.setViewportSize({ width: 1400, height: 900 }).catch(() => {});
      await openShell(P.page).then(() => sh(P.page, (t, xs) => xs.forEach((x) => t.closeTile(x)), TILES)).catch(() => {});
      await closeCtx(P.ctx, P.page).catch(() => {});
    }
    for (const t of TILES) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'lim-'))) fs.rmSync(dir, { recursive: true, force: true });
    }
  }
  done();
}

module.exports = { limitsCaps };
