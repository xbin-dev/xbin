// hack/ui-harness/passes/limitscaps.js — covers D203's UI (tile resource
// limits) against §W-shaped answers served by route interception: the API
// (GET/POST/DELETE /api/xbin/limits, /limits/dismiss, PUT /orgs/{org}/limits)
// and the /xbin/limits route are W1's, built in parallel, so this pass
// serves them itself and checks what the UI sends. Phase 2 turns it into
// the end-to-end pass on the real API (plans' §10 steps 1–7). As the admin:
//   1. apps/lim-a's window: the popover holds its pending memory ask
//      (Approve 3 GiB → POST manifest-ask), its cap hit (+25 / +50 / +100 %,
//      +50 the primary, +100 greyed with the reason in its title and
//      listed; +50 → POST raise 3072; Dismiss → POST /limits/dismiss) and a
//      sandbox-disk ask the person may not approve ("ask a workspace
//      admin"); the key badge counts them (shot: limitscaps-popover-<theme>);
//   2. a served OOM alert with an action: the banner's three choices, +100
//      greyed with its reason (title; listed when the banner opens); +50
//      POSTs a raise as the person; Dismiss POSTs the alert's route; a
//      phone puts the choices on their own line (shots: limitscaps-banner-*);
//   3. the tile's settings window, limits section: every kind's state, a
//      typed set… (3G → POST admin-set 3072), a revert (confirm → DELETE)
//      (shot: limitscaps-gear-<theme>);
//   4. the admin console's runtime → limits tab: the table, a chip filter,
//      the alert bar's raise… link narrowing it to the tile (shot:
//      limitscaps-admin-<theme>); an org's resource ceilings (6G → PUT
//      /orgs/devs/limits {memMiB: 6144}) (shot: limitscaps-orgceil-<theme>);
//   5. /xbin/limits (served here from web/limits.html: the route is W1's)
//      — the table and the ceilings, a tile's section with a VM's "restart
//      now", a phone (shots: limitscaps-page-*). Framed-refusal and the
//      page's headers are W1's (server tests), not checked here.
// The tile is removed at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, settle, sh, shot, shotEl, checker, sleep, THEME, gotoTab } = require('../lib');

const WS = process.env.WS || '';
const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const T = 'apps/lim-a', TVM = 'apps/lim-vm', TORG = 'o/devs/lim-org';
const ago = (min) => new Date(Date.now() - min * 60000).toISOString();

// ---- §W fixtures ----
const HIT = { at: ago(5), since: ago(52), count: 3, value: 2048, detail: 'OOM-killed (2 processes)' };
const WHY100 = 'needs +2 GiB; 1.4 GiB free after the 10% reserve and 4 GiB granted to other tiles but unused';
const MEM_CHOICES = [
  { pct: 25, value: 2560, fits: true, approvable: true },
  { pct: 50, value: 3072, fits: true, approvable: true, default: true },
  { pct: 100, value: 4096, fits: false, approvable: true, why: WHY100 },
];
const ROWS = {
  [T]: { tile: T, scope: 'apps', scopeKey: 'apps', rootsScope: false, limits: {
    memMiB: { effective: 2048, default: 2048, max: 31744, enforced: true, ask: 3072, pending: true, usage: 2010, hit: HIT,
      approvers: ['workspace-admin'], canDecide: true, canRevert: true, choices: MEM_CHOICES },
    diskGiB: { effective: 50, default: 50, max: 900, enforced: true, usage: 12, canDecide: false, canRevert: false,
      note: 'ignored: the quota is its scope’s (apps); ask from the tile that roots it' },
    sbxMemMiB: { effective: 12288, default: 8192, max: 24576, enforced: true, usage: 6144, canDecide: true, canRevert: true,
      set: { value: 12288, by: 'user:admin', at: ago(130), source: 'raise' }, approvers: ['workspace-admin'] },
    sbxDiskGiB: { effective: 100, default: 100, max: 800, enforced: true, ask: 200, pending: true, canDecide: false, canRevert: false, approvers: ['workspace-admin'] },
  } },
  [TVM]: { tile: TVM, scope: 'apps', scopeKey: 'apps', rootsScope: false, limits: {
    vmMemMiB: { effective: 4096, default: 2048, max: 16384, enforced: true, applied: 2048, appliesAt: 'next-start', canDecide: true, canRevert: true,
      approved: { value: 4096, by: 'user:admin', at: ago(60 * 26), source: 'manifest-ask' }, ask: 4096, approvers: ['workspace-admin'] },
    vmVCPUs: { effective: 2, default: 2, max: 16, enforced: true, canDecide: true, canRevert: true, approvers: ['workspace-admin'] },
  } },
  [TORG]: { tile: TORG, org: 'devs', scope: 'o/devs', scopeKey: 'o~devs', rootsScope: false, limits: {
    memMiB: { effective: 2048, default: 2048, max: 31744, enforced: true, ask: 6144, pending: true, canDecide: true, canRevert: true, approvers: ['workspace-admin'] },
  } },
};
const DEFAULTS = { memMiB: 2048, diskGiB: 50, vmMemMiB: 2048, vmVCPUs: 2, sbxMemMiB: 8192, sbxVCPUs: 4, sbxDiskGiB: 100 };
const TABLE = { tiles: Object.values(ROWS), defaults: DEFAULTS, enforced: Object.fromEntries(Object.keys(DEFAULTS).map((k) => [k, true])),
  orgCeilings: { devs: { memMiB: 4096, diskGiB: 100 } }, canSetCeilings: true };
const CAPACITY = { memMiB: { kind: 'memMiB', max: 31744, total: 32768, free: 9830, reserve: 3276, promised: 4096 } };
const PENDING = { pending: [
  { tile: T, kind: 'memMiB', unit: 'MiB', why: 'ask', ask: 3072, effective: 2048, approvable: true, mine: false, approvers: ['workspace-admin'] },
  { tile: T, kind: 'memMiB', unit: 'MiB', why: 'cap', hit: HIT, effective: 2048, approvable: true, mine: false, approvers: ['workspace-admin'] },
  { tile: T, kind: 'sbxDiskGiB', unit: 'GiB', why: 'ask', ask: 200, effective: 100, approvable: false, mine: true, approvers: ['workspace-admin'] },
] };
const DISMISS = `/limits/dismiss?tile=${encodeURIComponent(T)}&kind=memMiB`;
const ALERT = { level: 'warn', kind: 'oom', tile: T, system: false, dismiss: DISMISS,
  message: `${T} was OOM-killed at its 2 GiB memory limit (3× since ${new Date(HIT.since).toTimeString().slice(0, 5)}) — raise it: the tile's settings → Limits, /xbin/limits, or \`bx limits raise ${T} memory\``,
  action: { action: 'raise-limit', tile: T, kind: 'memMiB', unit: 'MiB', current: 2048, approvers: ['workspace-admin'], choices: MEM_CHOICES } };

// serve the limits API and record what the UI sends
function api(page) {
  const sent = [];
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const handler = async (route) => {
    const req = route.request(), u = new globalThis.URL(req.url()), m = req.method();
    let body = null;
    try { body = req.postDataJSON(); } catch { body = null; }
    if (u.pathname === '/api/xbin/limits' && m === 'GET') {
      if (u.searchParams.get('pending')) return json(route, PENDING);
      const t = u.searchParams.get('tile');
      if (t) return ROWS[t] ? json(route, { tile: ROWS[t], capacity: CAPACITY }) : json(route, { error: `${t}: no such tile` }, 404);
      return json(route, TABLE);
    }
    sent.push({ m, url: u.pathname + u.search, body });
    if (u.pathname === '/api/xbin/limits') return json(route, { tile: ROWS[body?.tile ?? u.searchParams.get('tile')] ?? ROWS[T] });
    if (u.pathname === '/api/xbin/limits/dismiss') return route.fulfill({ status: 204 });
    if (/^\/api\/xbin\/orgs\/[^/]+\/limits$/.test(u.pathname)) return json(route, { limits: { ...TABLE.orgCeilings.devs, ...body } });
    return route.continue();
  };
  const re = /\/api\/xbin\/(limits|orgs\/[^/?]+\/limits)(\/dismiss)?(\?|$)/;
  return { sent, on: () => page.route(re, handler), off: () => page.unroute(re, handler) };
}

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(150);
  }
}

function writeTile() {
  const dir = path.join(WS, T);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${T}</h1><p>a tile at its memory limit</p>\n`);
  fs.writeFileSync(path.join(dir, 'xbin.json'), JSON.stringify({ title: 'lim a', limits: { memory: '3G', sandboxes: { disk: '200G' } } }) + '\n');
}

// what apps/lim-a's popover shows
const popover = (page) => page.evaluate((t) => {
  const sh = document.querySelector('bx-shell')?.shadowRoot, cv = sh?.querySelector('bx-canvas')?.shadowRoot;
  const pop = cv?.querySelector(`.pqpop[data-prompts-for="${t}"]`);
  const lim = pop?.querySelector('bx-limits')?.shadowRoot;
  const plates = [...(lim?.querySelectorAll('.plate') ?? [])];
  const cap = plates.find((p) => p.dataset.why === 'cap');
  return {
    pop: !!pop, head: pop?.querySelector('.pqh span')?.textContent ?? '',
    plates: plates.map((p) => p.dataset.why),
    approve: !!plates.find((p) => p.dataset.why === 'ask')?.querySelector('[data-approve]'),
    ask: plates.map((p) => p.querySelector('.ask')?.textContent.trim() ?? '').filter(Boolean),
    choices: [...(cap?.querySelectorAll('.choices button') ?? [])].map((b) => ({ pct: b.dataset.pct, primary: b.classList.contains('primary'), disabled: b.disabled, title: b.title })),
    whys: cap?.querySelector('[data-whys]')?.textContent.replace(/\s+/g, ' ').trim() ?? '',
  };
}, T);

async function limitsCaps(browser) {
  const { check, skip, done } = checker(`limitscaps-${THEME}`);
  check(!!WS, 'WS is set (run.sh exports it)');
  const A = await login(browser, 'admin', 'admin');
  const req = (method, p) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, { method });
  const L = api(A.page);
  await L.on();
  writeTile();
  try {
    await until(async () => (await (await req('GET', '/components')).json()).some((c) => c.path === T), 'the tile registered');

    // 1. the popover on the tile's window
    await openShell(A.page);
    await usePersonalScreen(A.page);
    await openTile(A.page, T);
    // a window tall enough that the popover shows all three items
    await sh(A.page, (t, x) => t.setGeom((tiles) => tiles.map((o) => (o.path === x ? { ...o, w: 640, h: 768 } : o))), T);
    await settle(A.page);
    let p = await until(async () => { const v = await popover(A.page); return v.plates.length === 3 && v.choices.length === 3 ? v : null; }, 'the popover\'s three items')
      .catch(async (e) => { check(false, `${e.message}: ${JSON.stringify(await popover(A.page))}`); return popover(A.page); });
    check(p.plates.join() === 'ask,cap,ask', `an ask, a hit, an ask (${p.plates})`);
    check(/1 limit request|2 limit requests/.test(p.head) && /1 cap hit/.test(p.head), `the popover's words count them (${p.head})`);
    const counted = await sh(A.page, (t, x) => t.prompts[x], T);
    check(counted?.limits === 3 && counted?.caps === 1, `the shell counts 3 limit items, 1 a cap hit (${JSON.stringify(counted)})`);
    check(p.approve, 'the approvable ask has Approve');
    check(p.ask.some((a) => /ask a workspace admin/.test(a)), `an ask the person can't approve says who to ask (${JSON.stringify(p.ask)})`);
    const c50 = p.choices.find((c) => c.pct === '50'), c100 = p.choices.find((c) => c.pct === '100');
    check(c50?.primary && !c50.disabled, '+50 % is the primary choice');
    check(c100?.disabled && c100.title === WHY100, `+100 % is greyed with the reason in its title (${c100?.title})`);
    check(p.whys.includes(WHY100), `…and the reason shows under the buttons (${p.whys})`);
    await shotEl(A.page, `bx-canvas .pqpop[data-prompts-for="${T}"]`, `limitscaps-popover-${THEME}`);
    const limEl = A.page.locator(`bx-canvas .pqpop[data-prompts-for="${T}"] bx-limits`);
    await limEl.locator('.plate[data-why=ask] [data-approve]').first().click();
    await limEl.locator('.plate[data-why=cap] .choices button[data-pct="50"]').click();
    await limEl.locator('.plate[data-why=cap] [data-dismiss]').click();
    await until(async () => L.sent.length >= 3, 'three calls');
    const [a1, a2, a3] = L.sent.splice(0);
    check(a1?.m === 'POST' && JSON.stringify(a1.body) === JSON.stringify({ tile: T, kind: 'memMiB', value: 3072, source: 'manifest-ask' }), `Approve POSTs the ask (${JSON.stringify(a1)})`);
    check(a2?.m === 'POST' && JSON.stringify(a2.body) === JSON.stringify({ tile: T, kind: 'memMiB', value: 3072, source: 'raise' }), `+50 % POSTs a raise to 3 GiB (${JSON.stringify(a2)})`);
    check(a3?.m === 'POST' && a3.url === `/api/xbin${DISMISS}`, `an approver's Dismiss on a hit clears it for the workspace (${JSON.stringify(a3)})`);

    // 2. the banner
    await A.page.route('**/api/xbin/alerts', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ alerts: [ALERT] }) }));
    await A.page.reload();
    await A.page.waitForSelector('bx-shell');
    const banner = () => A.page.evaluate(() => {
      const a = document.querySelector('bx-shell')?.shadowRoot?.querySelector('.alerts .alert.act');
      if (!a) return null;
      return { choices: [...a.querySelectorAll('.achoice')].map((b) => ({ pct: b.dataset.pct, primary: b.classList.contains('primary'), disabled: b.disabled, title: b.title })),
        whys: a.querySelector('.awhys')?.textContent ?? '', h: a.getBoundingClientRect().height };
    });
    let b = await until(banner, 'the cap banner');
    check(b.choices.length === 3, `the banner has three choices (${b.choices.length})`);
    check(b.choices.find((c) => c.pct === '50')?.primary, '+50 % is its primary');
    check(b.choices.find((c) => c.pct === '100')?.disabled && b.choices.find((c) => c.pct === '100')?.title === WHY100, 'the greyed one carries its reason');
    check(b.h <= 40, `one line on a wide screen (${b.h}px)`);
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-${THEME}`);
    await A.page.locator('bx-shell .alerts .alert.act button.msg').click();
    b = await until(async () => { const v = await banner(); return v?.whys ? v : null; }, 'the open banner\'s reasons');
    check(b.whys.includes(WHY100), 'an open banner lists why a choice is greyed');
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-open-${THEME}`);
    await A.page.locator('bx-shell .alerts .achoice[data-pct="50"]').click();
    await until(async () => L.sent.length >= 1, 'the banner\'s raise');
    const r1 = L.sent.splice(0)[0];
    check(r1?.m === 'POST' && JSON.stringify(r1.body) === JSON.stringify({ tile: T, kind: 'memMiB', value: 3072, source: 'raise' }), `the banner's +50 % POSTs a raise (${JSON.stringify(r1)})`);
    const toast = await until(async () => (await sh(A.page, (t) => t.toasts)).find((x) => /raised to 3 GiB/.test(x.message)), 'the toast').catch(() => null);
    check(!!toast, 'a toast says what was raised');
    await A.page.locator('bx-shell .alerts .alert.act .dismiss').click();
    await until(async () => L.sent.length >= 1, 'the banner\'s dismiss');
    const d1 = L.sent.splice(0)[0];
    check(d1?.m === 'POST' && d1.url === `/api/xbin${DISMISS}`, `Dismiss POSTs the alert's route (${JSON.stringify(d1)})`);
    // a phone: the choices on their own line
    await A.page.setViewportSize({ width: 390, height: 800 });
    await settle(A.page);
    b = await until(banner, 'the banner on a phone');
    check(b.h > 40, `on a phone the choices wrap below (${b.h}px)`);
    await shotEl(A.page, 'bx-shell .alerts', `limitscaps-banner-phone-${THEME}`);
    await A.page.setViewportSize({ width: 1400, height: 900 });
    await A.page.unroute('**/api/xbin/alerts');
    await A.page.reload();
    await A.page.waitForSelector('bx-shell');

    // 3. the tile's settings window, limits section
    await openShell(A.page);
    await sh(A.page, (t, x) => t.openAdminWindow(x, 'limits'), T);
    const sec = A.page.locator('bx-tile-admin bx-limits[view=section]');
    await until(async () => (await sec.locator('.kind').count()) === 4, 'the section\'s four kinds');
    await settle(A.page);
    check((await sec.locator('.kind[data-kind=memMiB] [data-hit] .choices button').count()) === 3, 'the hit\'s choices in the section');
    check(/raised to 12 GiB by admin/.test(await sec.locator('.kind[data-kind=sbxMemMiB] [data-decision=set]').innerText()), 'a decision: who, what, when');
    check(/ignored: the quota is its scope/.test(await sec.locator('.kind[data-kind=diskGiB]').innerText()), 'a placement note shows');
    check((await sec.locator('.kind[data-kind=diskGiB] [data-edit]').count()) === 0, 'no set… where the viewer can\'t decide');
    await shotEl(A.page, 'bx-shell .admin-pop', `limitscaps-gear-${THEME}`);
    await sec.locator('.kind[data-kind=memMiB] [data-edit]').click();
    const ed = sec.locator('bx-limit-edit');
    await ed.locator('input.v').fill('3G');
    check(/= 3 GiB/.test(await ed.locator('#pv').innerText()), 'the editor previews the parsed value');
    await shotEl(A.page, 'bx-tile-admin bx-limits .kind[data-kind=memMiB]', `limitscaps-gear-edit-${THEME}`);
    await ed.locator('button.primary').click();
    await until(async () => L.sent.length >= 1, 'the typed set');
    const s1 = L.sent.splice(0)[0];
    check(s1?.m === 'POST' && JSON.stringify(s1.body) === JSON.stringify({ tile: T, kind: 'memMiB', value: 3072, source: 'admin-set' }), `set… POSTs admin-set (${JSON.stringify(s1)})`);
    await sec.locator('.kind[data-kind=sbxMemMiB] [data-revert]').click();
    await sec.locator('.kind[data-kind=sbxMemMiB] [data-confirm-revert]').click();
    await until(async () => L.sent.length >= 1, 'the revert');
    const v1 = L.sent.splice(0)[0];
    check(v1?.m === 'DELETE' && v1.url === `/api/xbin/limits?tile=${encodeURIComponent(T)}&kind=sbxMemMiB`, `revert DELETEs the kind (${JSON.stringify(v1)})`);
    await sh(A.page, (t) => t.closeAdminWindow());

    // 4. the admin console: runtime → limits, an org's ceilings
    await A.page.route('**/api/xbin/alerts', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ alerts: [ALERT] }) }));
    await gotoTab(A.page, 'limits', 'in force');
    const tbl = A.page.locator('bx-limits-table');
    await until(async () => (await tbl.locator('tr.row').count()) === 7, 'the table\'s seven tile-kinds');
    check((await tbl.locator('tr.row[data-tile="o/devs/lim-org"] [data-approve]').count()) === 1, 'a pending ask has Approve in the table');
    await shot(A.page, `limitscaps-admin-${THEME}`);
    await tbl.locator('[data-chip=pending]').click();
    check((await tbl.locator('tr.row').count()) === 3, `the pending chip keeps the pending kinds (${await tbl.locator('tr.row').count()})`);
    await tbl.locator('[data-chip=pending]').click();
    await A.page.locator('bx-admin .alertbar [data-raise]').click();
    await until(async () => (await tbl.locator('tr.row').count()) === 4, 'raise… narrows to the tile');
    check((await tbl.locator('tr.row.focus').count()) === 4, 'the alert bar\'s raise… opens the tab at its tile');
    await A.page.unroute('**/api/xbin/alerts');
    await A.page.route('**/api/xbin/orgs', async (r) => {
      const resp = await r.fetch();
      const j = await resp.json();
      for (const o of j.orgs ?? []) if (o.id === 'devs') o.limits = TABLE.orgCeilings.devs;
      await r.fulfill({ response: resp, json: j });
    });
    await gotoTab(A.page, 'orgs/devs', 'resource ceilings');
    const ol = A.page.locator('bx-org-limits[org=devs]');
    await ol.locator('input[data-kind=memMiB]').fill('6G');
    await ol.scrollIntoViewIfNeeded();
    await shotEl(A.page, 'bx-org-limits[org=devs]', `limitscaps-orgceil-${THEME}`);
    await ol.locator('[data-save]').click();
    await until(async () => L.sent.length >= 1, 'the ceilings PUT');
    const o1 = L.sent.splice(0)[0];
    check(o1?.m === 'PUT' && o1.url === '/api/xbin/orgs/devs/limits' && JSON.stringify(o1.body) === '{"memMiB":6144}', `the ceilings PUT the changed kind (${JSON.stringify(o1)})`);
    await A.page.unroute('**/api/xbin/orgs');

    // 5. /xbin/limits — the page's files (W1 serves the route)
    const htmlFile = fs.readFileSync(path.join(REPO, 'web', 'limits.html'), 'utf8');
    await A.page.route(/^https?:\/\/[^/]+\/xbin\/limits(\?|$)/, (r) => r.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: htmlFile }));
    await A.page.goto(`${URL}/xbin/limits`);
    const pt = A.page.locator('bx-limits-page bx-limits-table');
    await until(async () => (await pt.locator('tr.row').count()) === 7, 'the page\'s table');
    await until(async () => (await pt.locator('details.org').count()) > 0, 'the ceilings');
    await A.page.locator('bx-limits-page details.org[data-org=devs] summary').click();
    await settle(A.page);
    check(/memory 4 GiB/.test(await pt.locator('details.org[data-org=devs] summary').innerText()), 'the page lists an org\'s ceilings');
    await shot(A.page, `limitscaps-page-${THEME}`);
    await A.page.goto(`${URL}/xbin/limits?tile=${encodeURIComponent(TVM)}`);
    const one = A.page.locator('bx-limits-page bx-limits[view=section]');
    await until(async () => (await one.locator('.kind').count()) === 2, 'a tile\'s section on the page');
    check(/next start/.test(await one.locator('.kind[data-kind=vmMemMiB] [data-applies]').innerText()), 'a VM\'s size says it applies at the next start');
    check((await one.locator('.kind[data-kind=vmMemMiB] [data-applies] button').count()) === 1, '…with restart now');
    await shot(A.page, `limitscaps-page-tile-${THEME}`);
    await one.locator('.kind[data-kind=vmMemMiB] [data-applies] button').click();
    await until(async () => L.sent.length >= 1, 'restart now');
    const rs = L.sent.splice(0)[0];
    check(JSON.stringify(rs?.body) === JSON.stringify({ tile: TVM, kind: 'vmMemMiB', value: 4096, source: 'manifest-ask', restart: true }), `restart now re-POSTs the decision with restart (${JSON.stringify(rs)})`);
    await A.page.setViewportSize({ width: 390, height: 844 });
    await A.page.goto(`${URL}/xbin/limits`);
    await until(async () => (await pt.locator('tr.row').count()) === 7, 'the page on a phone');
    const wide = await A.page.evaluate(() => document.documentElement.scrollWidth);
    check(wide <= 390, `no sideways scroll on a phone (${wide}px)`);
    await shot(A.page, `limitscaps-page-phone-${THEME}`);
    skip('the framed refusal and the page\'s headers: W1\'s route (server tests; Phase 2\'s end-to-end pass)');
  } finally {
    await L.off().catch(() => {});
    await openShell(A.page).then(() => sh(A.page, (t, x) => t.closeTile(x), T)).catch(() => {});
    const dir = path.join(WS, T);
    if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'lim-'))) fs.rmSync(dir, { recursive: true, force: true });
    await A.page.goto(`${URL}/`).catch(() => {});
    await A.page.waitForSelector('bx-shell', { timeout: 15000 }).catch(() => {});
    await settle(A.page).catch(() => {});
    await closeCtx(A.ctx, A.page);
  }
  done();
}

module.exports = { limitsCaps };
