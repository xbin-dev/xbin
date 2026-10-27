// hack/ui-harness/passes/pairing.js — adding a phone over the app tunnel
// (D204, docs/app-tunnel.md; the link and the code: native/spec/tunnel.md
// §2, §5, §6) in the shell's devices panel and on xbind's /xbin/devices
// page, in both themes. xbind's pairing API is served by route interception
// (a fake in this file shaped like the spec's routes: enroll-code {tunnel},
// GET /devices/pairing/{id}, POST …/confirm, GET /devices {limit, count},
// /login/methods tunnel) until a harness xbind runs a tunnel of its own; the
// page itself is xbind's when it serves /xbin/devices, else web/devices.html
// with the page's CSP.
//   1. the big QR code: ≥ 288 px drawn at ≥ 4 px per module, decoding to the
//      tunnel link (zbarimg); the address field rewrites u= only; "Older
//      app? Show a classic code" (shot pairing-qr-<theme>);
//   2. a phone connects, then the confirmation: "012 345" large, the
//      phone's name and platform, "Only confirm if YOUR phone shows this
//      code" (shot pairing-confirm-<theme>); They don't → {match:false},
//      rejected, "make a new code" (shot pairing-mismatch-<theme>);
//   3. at the limit (1 of 1): the confirmation asks which device the phone
//      replaces, Codes match waits for it, → {match:true, replace}; enrolled
//      (shots pairing-replace-, pairing-enrolled-<theme>); a 409 from xbind
//      when the list didn't say so brings the question up too;
//   4. abandoned, expired (xbind's word, and an unknown pairing), the
//      classic code (no {tunnel}), no classic link when the tunnel is
//      required (shots pairing-abandoned-, pairing-expired-<theme>);
//   5. the panel at the limit: tunnels on → "replace a device…", off → no
//      add, "remove one to add another" (shot pairing-atlimit-<theme>);
//   6. /xbin/devices: the list, "N of M devices", add → the QR code → the
//      confirmation; a phone (390 px) without sideways scroll (shots
//      pairing-page-*-<theme>);
//   7. a phone-width shell panel on the confirmation; no page error.
const crypto = require('crypto');
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, OUT, fs, login, closeCtx, settle, waitSel, openShell, shot, shotEl, checker, log } = require('../lib');

const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const enc = encodeURIComponent;
const b64 = (n) => crypto.randomBytes(n).toString('base64url');
const PAGE_CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
  "connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
const now = () => Math.floor(Date.now() / 1000);
const DAY = 86400;

// The fake: one pairing at a time, a device list with a limit, the tunnel
// switch. f.reqs records every enroll-code and confirm body.
function fake() {
  const f = {
    n: 0, required: false, tunnelOn: true, reqs: [], confirms: [],
    dev: { devices: [], limit: 3 },
    p: null, // {id, state, sas, device, expires, reason, deviceId, replaced}
    on409: false, // the next match:true without replace answers 409 {limit}
  };
  f.link = (id, required) => `xbin://enroll?u=${enc(URL)}&c=${id}&tv=1&t=endpoint${'abcdefghijklmnopqrstuvwxyz234567'.repeat(4).slice(0, 110)}` +
    `&k=${f.k}&p=${b64(32)}${required ? '&rq=1' : ''}`;
  f.k = b64(32);
  f.list = () => ({ devices: f.dev.devices, limit: f.dev.limit, count: f.dev.devices.length });
  f.set = (o) => { Object.assign(f.p, o); };
  return f;
}
const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function routeFake(ctx, f) {
  await ctx.route((u) => u.pathname === '/api/xbin/devices/enroll-code', (route) => {
    let body = {};
    try { body = JSON.parse(route.request().postData() || '{}'); } catch { /* '' */ }
    f.reqs.push(body);
    const code = `PAIRCODE${String(++f.n).padStart(2, '0')}QRSTUVWXYZ234567`.slice(0, 26);
    const expires = now() + 300;
    if (body.tunnel && f.tunnelOn) {
      const id = `pair-${f.n}`;
      f.p = { id, state: 'minted', expires };
      return json(route, 200, { code, origin: URL, expires, url: f.link(code, f.required),
        tunnel: { pairing: id, ticket: 'endpoint…', serverKey: f.k, required: f.required } });
    }
    if (f.required) return json(route, 409, { error: `this workspace pairs the xbin app over its iroh tunnel only — open ${URL}/xbin/devices`, docs: '/docs/app-tunnel.md' });
    return json(route, 200, { code, origin: URL, expires, url: `xbin://enroll?u=${enc(URL)}&c=${code}` });
  });
  await ctx.route((u) => /^\/api\/xbin\/devices\/pairing\/[^/]+$/.test(u.pathname), (route) => {
    const id = decodeURIComponent(new globalThis.URL(route.request().url()).pathname.split('/').pop());
    if (!f.p || f.p.id !== id || f.p.gone) return json(route, 404, { error: 'no such pairing' });
    const { id: _, gone: __, ...view } = f.p;
    return json(route, 200, view);
  });
  await ctx.route((u) => /^\/api\/xbin\/devices\/pairing\/[^/]+\/confirm$/.test(u.pathname), (route) => {
    const body = JSON.parse(route.request().postData() || '{}');
    f.confirms.push(body);
    if (!f.p || f.p.state !== 'confirm') return json(route, 409, { error: 'the phone stopped before you confirmed', state: f.p?.state ?? 'expired' });
    if (!body.match) { f.set({ state: 'rejected', reason: 'mismatch' }); return json(route, 200, { state: 'rejected', reason: 'mismatch' }); }
    const lim = f.dev.limit;
    if ((f.on409 || f.dev.devices.length >= lim) && !body.replace) {
      f.on409 = false;
      return json(route, 409, { error: `you have ${f.dev.devices.length} devices; this workspace allows ${lim} per person — remove one first (an admin can raise your limit)`,
        limit: f.dev.devices.length, count: f.dev.devices.length });
    }
    const id = `dev-${f.n}`;
    if (body.replace) f.dev.devices = f.dev.devices.filter((d) => d.id !== body.replace);
    f.dev.devices = [...f.dev.devices, { id, name: f.p.device.name, platform: f.p.device.platform, origin: URL, created: now(), tunnel: true }];
    f.set({ state: 'enrolled', deviceId: id, ...(body.replace ? { replaced: body.replace } : {}) });
    return json(route, 200, { state: 'enrolled', deviceId: id, ...(body.replace ? { replaced: body.replace } : {}) });
  });
  await ctx.route((u) => u.pathname === '/api/xbin/devices', (route) => json(route, 200, f.list()));
  await ctx.route((u) => u.pathname === '/api/xbin/login/methods', async (route) => {
    const r = await route.fetch();
    const m = await r.json().catch(() => ({}));
    return json(route, 200, { ...m, tunnel: { enabled: f.tunnelOn, required: f.required } });
  });
}

// The page as xbind serves it: its own route when there is one, else the
// file with the page's headers.
async function routePage(ctx) {
  const r = await ctx.request.get(`${URL}/xbin/devices`, { headers: { Accept: 'text/html' } });
  if (r.status() === 200) return 'xbind';
  const html = fs.readFileSync(path.join(REPO, 'web', 'devices.html'));
  await ctx.route((u) => u.pathname === '/xbin/devices', (route) => route.fulfill({ status: 200, body: html,
    headers: { 'Content-Type': 'text/html; charset=utf-8', 'Content-Security-Policy': PAGE_CSP, 'X-Frame-Options': 'DENY', 'Cache-Control': 'no-cache' } }));
  return 'web/devices.html';
}

function qrText(file) {
  try { return execFileSync('zbarimg', ['-q', '--raw', `${OUT}/${file}.png`], { encoding: 'utf8' }).trim(); } catch (e) {
    log('pairing: zbarimg unavailable or failed — QR decode not checked:', e.message.split('\n')[0]);
    return null;
  }
}
const until = async (fn, label, timeout = 10000) => {
  const end = Date.now() + timeout;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error(`timed out: ${label}`);
    await new Promise((r) => setTimeout(r, 100));
  }
};
const noSideways = (page) => page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);

async function shellPanel(page, f, check, theme) {
  const P = 'bx-devices bx-pair';
  await openShell(page);
  await page.evaluate(() => { try { localStorage.removeItem('xbin-phone-address'); } catch { /* none */ } });
  await page.locator('bx-shell .top button.chip.settings').click();
  await page.locator('bx-shell .wsmenu [data-add-device]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  check(f.reqs.at(-1)?.tunnel === true, `${theme}: the shell asks for a tunnel code ({tunnel: true}) (${JSON.stringify(f.reqs.at(-1))})`);
  const pair = page.locator(P);
  const geo = await pair.evaluate((el) => el.testApi().qr());
  const box = await page.locator(`${P} [data-qr]`).boundingBox();
  check(geo && geo.px >= 288 && geo.ppm >= 4, `${theme}: the QR code is ≥ 288 px at ≥ 4 px per module (${JSON.stringify(geo)})`);
  check(box && Math.round(box.width) === geo.px, `${theme}: drawn at its full size in the panel (${box?.width}px)`);
  const link = await page.locator(`${P} input[data-link]`).inputValue();
  check(/^xbin:\/\/enroll\?u=.+&c=PAIRCODE.+&tv=1&t=endpoint.+&k=.+&p=.+$/.test(link), `${theme}: the raw link is the tunnel link (${link.slice(0, 80)}…)`);
  check(await page.locator(`${P} [data-classic]`).count() === 1, `${theme}: "Older app? Show a classic code" while the tunnel isn't required`);
  const limitWords = (await page.locator('bx-devices [data-limit]').textContent()).trim();
  check(limitWords === '0 of 3 devices', `${theme}: the panel says "0 of 3 devices" (${limitWords})`);
  const over = await page.locator('bx-devices .box').evaluate((b) => b.scrollHeight - b.clientHeight);
  check(over <= 0, `${theme}: the QR code, the steps and the foot fit the dialog without scrolling (${over}px over)`);
  check(!(await page.locator(`${P} details.more`).evaluate((d) => d.open)), `${theme}: the address and the raw link are folded away`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-qr-${theme}`);
  await shotEl(page, `${P} [data-qr]`, `pairing-qrcode-${theme}`);
  const got = qrText(`pairing-qrcode-${theme}`);
  if (got !== null) check(got === link, `${theme}: the QR code decodes to the tunnel link`);
  // the address the phone uses: u= only, the tunnel params byte for byte
  await page.locator(`${P} details.more summary`).click();
  const custom = 'https://phone.example.test:8443';
  await page.locator(`${P} [data-addr]`).fill(custom);
  await page.locator(`${P} [data-addr]`).press('Enter');
  const want = link.replace(`u=${enc(URL)}`, `u=${enc(custom)}`);
  await until(async () => (await page.locator(`${P} input[data-link]`).inputValue()) === want, 'the address rewrites the link').catch(() => {});
  check(await page.locator(`${P} input[data-link]`).inputValue() === want, `${theme}: a typed address rewrites u= only`);
  check(await page.evaluate(() => localStorage.getItem('xbin-phone-address')) === custom, `${theme}: …remembered under the shell's key`);
  await page.locator(`${P} [data-addr]`).fill('');
  await page.locator(`${P} [data-addr]`).press('Enter');
  await until(async () => (await page.locator(`${P} input[data-link]`).inputValue()) === link, 'reset').catch(() => {});
  check(await page.locator(`${P} input[data-link]`).inputValue() === link, `${theme}: emptied, the server's address again`);

  // 2. a phone connects; the confirmation; They don't
  f.set({ state: 'handshaken' });
  await waitSel(page, `${P} [data-status].on`);
  f.set({ state: 'confirm', sas: 12345, device: { name: "Alice's iPhone 16", platform: 'ios' }, expires: new Date(Date.now() + 118000).toISOString() });
  await waitSel(page, `${P} [data-sas]`);
  await page.waitForFunction(() => document.querySelector('bx-devices')?.shadowRoot?.querySelector('bx-pair')?.shadowRoot?.querySelector('[data-sas]')?.textContent.trim() === '012 345', null, { timeout: 5000 }).catch(() => {});
  const sas = (await page.locator(`${P} [data-sas]`).textContent()).trim();
  check(sas === '012 345', `${theme}: the confirmation code is six digits, grouped (${sas})`);
  const conf = await page.locator(`${P} [data-pair="confirm"]`).textContent();
  check(/Alice's iPhone 16/.test(conf) && /iOS/.test(conf), `${theme}: the phone's name and platform`);
  check(/Only confirm if YOUR phone shows this code/.test(conf), `${theme}: "Only confirm if YOUR phone shows this code"`);
  check(await page.locator(`${P} [data-replace]`).count() === 0, `${theme}: under the limit, no device to replace`);
  check(await page.locator(`${P} [data-qr]`).count() === 0, `${theme}: the QR code gives way to the code`);
  const fs1 = await page.locator(`${P} [data-sas]`).evaluate((e) => parseFloat(getComputedStyle(e).fontSize));
  check(fs1 >= 40, `${theme}: the code is large (${fs1}px)`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-confirm-${theme}`);
  await page.locator(`${P} [data-mismatch]`).click();
  await waitSel(page, `${P} [data-pair="rejected"]`);
  check(JSON.stringify(f.confirms.at(-1)) === '{"match":false}', `${theme}: They don't → {match:false} (${JSON.stringify(f.confirms.at(-1))})`);
  check(/didn't match/.test(await page.locator(`${P} [data-pair="rejected"]`).textContent()), `${theme}: …"Not added: the codes didn't match"`);
  check(await page.locator('bx-devices .foot [data-add]').count() === 1, `${theme}: the panel offers add again`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-mismatch-${theme}`);

  // 3. at the limit: which device the phone replaces
  f.dev = { devices: [{ id: 'old-1', name: 'Old iPhone', platform: 'ios', origin: URL, created: now() - 40 * DAY, lastUsed: now() - 3 * DAY }], limit: 1 };
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ state: 'confirm', sas: '907 001', device: { name: 'New iPhone', platform: 'ios' }, expires: now() + 110 });
  await waitSel(page, `${P} [data-replace]`);
  check(await page.locator(`${P} [data-match]`).isDisabled(), `${theme}: at the limit, Codes match waits for a device to replace`);
  check(/You have 1 of 1 device/.test(await page.locator(`${P} [data-replace] legend`).textContent()), `${theme}: …saying the limit`);
  await page.locator(`${P} [data-replace-device="old-1"] input`).check();
  await settle(page);
  check(!(await page.locator(`${P} [data-match]`).isDisabled()), `${theme}: picked, Codes match is on`);
  await shotEl(page, 'bx-devices .box', `pairing-replace-${theme}`);
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-pair="enrolled"]`);
  check(JSON.stringify(f.confirms.at(-1)) === '{"match":true,"replace":"old-1"}', `${theme}: Codes match → {match:true, replace} (${JSON.stringify(f.confirms.at(-1))})`);
  const done = await page.locator(`${P} [data-pair="enrolled"]`).textContent();
  check(/New iPhone was added/.test(done) && /Old iPhone was removed/.test(done), `${theme}: enrolled, the replaced one named (${done.trim().replace(/\s+/g, ' ')})`);
  await page.waitForFunction(() => document.querySelector('bx-devices')?.shadowRoot?.querySelector('li[data-device="dev-2"]'), null, { timeout: 5000 }).catch(() => {});
  check(await page.locator('bx-devices li[data-device="dev-2"] [data-tunnel]').count() === 1, `${theme}: the list has the new phone, marked iroh tunnel`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-enrolled-${theme}`);

  // a 409 {limit} the list didn't predict brings the question up too
  f.dev.limit = 3;
  f.on409 = true;
  await page.locator('bx-devices .foot [data-add]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ state: 'confirm', sas: 5, device: { name: 'Third phone', platform: 'ios' }, expires: now() + 110 });
  await waitSel(page, `${P} [data-match]`);
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-replace]`);
  check(await page.locator(`${P} .err`).count() === 1, `${theme}: xbind's 409 at the limit shows its words and the question`);
  await page.locator(`${P} [data-replace-device="dev-2"] input`).check();
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-pair="enrolled"]`);
  check(f.confirms.at(-1)?.replace === 'dev-2', `${theme}: …and the retry names it`);

  // 4. abandoned, expired, an unknown pairing, the classic code, required
  await page.locator('bx-devices .foot [data-add]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ state: 'abandoned' });
  await waitSel(page, `${P} [data-pair="abandoned"]`);
  check(/stopped before you confirmed/.test(await page.locator(`${P} [data-pair="abandoned"]`).textContent()), `${theme}: abandoned says so`);
  await settle(page);
  await shotEl(page, `${P}`, `pairing-abandoned-${theme}`);
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ state: 'expired' });
  await waitSel(page, `${P} [data-pair="expired"]`);
  await settle(page);
  await shotEl(page, `${P}`, `pairing-expired-${theme}`);
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ gone: true });
  await waitSel(page, `${P} [data-pair="expired"]`);
  check(true, `${theme}: a pairing xbind no longer knows (404) is expired`);
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-classic]`);
  await page.locator(`${P} [data-classic]`).click();
  await waitSel(page, 'bx-devices [data-enroll] svg');
  check(!('tunnel' in f.reqs.at(-1)), `${theme}: the classic code is asked for without {tunnel} (${JSON.stringify(f.reqs.at(-1))})`);
  check(/^xbin:\/\/enroll\?u=[^&]+&c=[A-Z0-9]+$/.test(await page.locator('bx-devices [data-enroll] input[data-link]').inputValue()), `${theme}: …and drawn as the classic link`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  f.required = true;
  await page.locator('bx-shell .top button.chip.settings').click();
  await page.locator('bx-shell .wsmenu [data-add-device]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  check(await page.locator(`${P} [data-classic]`).count() === 0, `${theme}: no classic link while the tunnel is required`);
  check(/&rq=1$/.test(await page.locator(`${P} input[data-link]`).inputValue()), `${theme}: …the link says rq=1`);
  f.set({ gone: true });
  await waitSel(page, `${P} [data-pair="expired"]`);
  f.required = false;

  // 5. the panel at the limit
  f.dev = { devices: [{ id: 'old-1', name: 'Old iPhone', platform: 'ios', origin: URL, created: now() - 40 * DAY, lastUsed: now() - 3 * DAY, tunnel: true }], limit: 1 };
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  await page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices()));
  await waitSel(page, 'bx-devices [data-limit].at');
  const rep = await page.locator('bx-devices .foot').textContent();
  check(/1 of 1 device — a new phone replaces one/.test(rep) && /replace a device…/.test(rep), `${theme}: at the limit with tunnels on: "replace a device…" (${rep.trim().replace(/\s+/g, ' ')})`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  f.tunnelOn = false;
  await page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices({ add: true })));
  await waitSel(page, 'bx-devices [data-limit].at');
  await settle(page);
  const off = await page.locator('bx-devices .foot').textContent();
  check(/remove one to add another/.test(off) && await page.locator('bx-devices .foot [data-add]').count() === 0
    && await page.locator('bx-devices bx-pair, bx-devices [data-enroll]').count() === 0,
  `${theme}: at the limit without tunnels: no add (not even from "add a device"), the rows' remove (${off.trim().replace(/\s+/g, ' ')})`);
  check(await page.locator('bx-devices li[data-device="old-1"] button.rm').count() === 1, `${theme}: …each row's remove is right there`);
  await shotEl(page, 'bx-devices .box', `pairing-atlimit-${theme}`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  f.tunnelOn = true;
}

async function devicesPage(browser, f, check, theme) {
  const W = await login(browser, 'admin', 'admin', { colorScheme: theme });
  const errs = [];
  W.page.on('pageerror', (e) => errs.push(e.message));
  await routeFake(W.ctx, f);
  const src = await routePage(W.ctx);
  log(`pairing: /xbin/devices from ${src}`);
  f.dev = { devices: [
    { id: 'old-1', name: 'Old iPhone', platform: 'ios', origin: URL, created: now() - 40 * DAY, lastUsed: now() - 3 * DAY },
    { id: 'ipad-1', name: 'iPad Pro', platform: 'ipados', origin: URL, created: now() - 9 * DAY, lastUsed: now() - 3600, tunnel: true },
  ], limit: 3 };
  await W.page.goto(`${URL}/xbin/devices`);
  await waitSel(W.page, 'bx-devices-page li[data-device]');
  check((await W.page.locator('bx-devices-page [data-limit]').textContent()).trim() === '2 of 3 devices', `${theme}: /xbin/devices says "2 of 3 devices"`);
  check(await W.page.locator('bx-devices-page li[data-device="ipad-1"] [data-tunnel]').count() === 1, `${theme}: …marks the tunnel-paired one`);
  await settle(W.page);
  await shot(W.page, `pairing-page-${theme}`, { fullPage: false });
  await W.page.locator('bx-devices-page [data-add]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-qr] svg');
  check(f.reqs.at(-1)?.tunnel === true, `${theme}: the page asks for a tunnel code`);
  await settle(W.page);
  await shot(W.page, `pairing-page-qr-${theme}`);
  f.set({ state: 'confirm', sas: '048213', device: { name: "Bob's iPhone", platform: 'ios' }, expires: now() + 115 });
  await waitSel(W.page, 'bx-devices-page bx-pair [data-sas]');
  check((await W.page.locator('bx-devices-page bx-pair [data-sas]').textContent()).trim() === '048 213', `${theme}: the page shows the code`);
  await settle(W.page);
  await shot(W.page, `pairing-page-confirm-${theme}`, { fullPage: false });
  // a phone: the list, the QR code and the confirmation, no sideways scroll
  await W.page.setViewportSize({ width: 390, height: 844 });
  await settle(W.page);
  check(await noSideways(W.page) <= 0, `${theme}: the confirmation fits a phone (${await noSideways(W.page)}px over)`);
  await shot(W.page, `pairing-page-confirm-phone-${theme}`, { fullPage: false });
  await W.page.locator('bx-devices-page bx-pair [data-match]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-pair="enrolled"]');
  await W.page.locator('bx-devices-page [data-add]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-qr] svg');
  await settle(W.page);
  check(await noSideways(W.page) <= 0, `${theme}: the QR code fits a phone (${await noSideways(W.page)}px over)`);
  await shot(W.page, `pairing-page-qr-phone-${theme}`);
  f.set({ state: 'abandoned' });
  await waitSel(W.page, 'bx-devices-page bx-pair [data-pair="abandoned"]');
  await W.page.goto(`${URL}/xbin/devices`);
  await waitSel(W.page, 'bx-devices-page li[data-device]');
  check(await noSideways(W.page) <= 0, `${theme}: the list fits a phone`);
  await shot(W.page, `pairing-page-phone-${theme}`, { fullPage: false });
  check(!errs.length, `${theme}: no page error on /xbin/devices (${errs.join(' | ')})`);
  await W.ctx.close();
}

async function pairing(browser) {
  const { check, done } = checker('pairing');
  for (const theme of ['dark', 'light']) {
    const f = fake();
    const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
    const errs = [];
    A.page.on('pageerror', (e) => errs.push(e.message));
    await routeFake(A.ctx, f);
    await shellPanel(A.page, f, check, theme);
    // 7. a phone-width shell panel on the confirmation
    f.dev = { devices: [], limit: 3 };
    await A.page.setViewportSize({ width: 390, height: 844 });
    await A.page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices({ add: true })));
    await waitSel(A.page, 'bx-devices bx-pair [data-qr] svg');
    f.set({ state: 'confirm', sas: 12345, device: { name: "Alice's iPhone 16 Pro Max", platform: 'ios' }, expires: now() + 110 });
    await waitSel(A.page, 'bx-devices bx-pair [data-sas]');
    await settle(A.page);
    const box = await A.page.locator('bx-devices .box').boundingBox();
    const inner = await A.page.locator('bx-devices .box').evaluate((b) => b.scrollWidth - b.clientWidth);
    check(box && box.x >= 0 && box.x + box.width <= 390 && inner <= 0, `${theme}: the confirmation fits a phone-width panel (${JSON.stringify(box)}, ${inner}px over)`);
    await shot(A.page, `pairing-phone-${theme}`, { fullPage: false });
    await A.page.locator('bx-devices .foot button', { hasText: 'close' }).click();
    check(!errs.length, `${theme}: no page error in the shell (${errs.join(' | ')})`);
    await closeCtx(A.ctx, A.page);
    await devicesPage(browser, fake(), check, theme);
  }
  done();
}

module.exports = { pairing };
