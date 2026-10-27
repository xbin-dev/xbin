// hack/ui-harness/passes/pairing.js — adding a phone over the app tunnel
// (D204, docs/app-tunnel.md; the link and the code: native/spec/tunnel.md
// §2, §5, §6) in the shell's devices panel and on xbind's /xbin/devices
// page, in both themes, against the harness xbind's real tunnel (run.sh
// runs it with HARNESS_IROH, relays off) and real phones (../phone.js:
// bin/tunnelpair, devclient over the sidecar's client mode) — the
// confirmation shows the code the phone computed itself. Only what xbind
// can't produce on demand is served by route interception: a pairing that
// expired, one xbind no longer knows (404), and a 409 at the limit the
// device list didn't predict.
//   1. the big QR code: ≥ 288 px drawn at ≥ 4 px per module, decoding to the
//      tunnel link (zbarimg); the address field rewrites u= only; "Older
//      app? Show a classic code" (shot pairing-qr-<theme>);
//   2. a phone pairs: the confirmation shows ITS code large, its name and
//      platform, "Only confirm if YOUR phone shows this code" (shot
//      pairing-confirm-<theme>); They don't → rejected, the phone hears
//      mismatch, "make a new code" (shot pairing-mismatch-<theme>);
//   3. Codes match → enrolled, the phone hears it, the list marks it iroh
//      tunnel (shot pairing-enrolled-<theme>);
//   4. at the limit (1 of 1): the confirmation asks which device the phone
//      replaces, Codes match waits for it; enrolled, the old one named
//      (shots pairing-replace-, pairing-replaced-<theme>);
//   5. the phone leaves before the confirmation: abandoned (shot
//      pairing-abandoned-<theme>); the classic code (no {tunnel}); iroh-only
//      on: rq=1 and no classic link;
//   6. the panel at the limit: tunnels on → "replace a device…", off → no
//      add, "remove one to add another" (shot pairing-atlimit-<theme>);
//   7. a phone-width shell panel on a real confirmation (shot
//      pairing-phone-<theme>); no page error;
//   8. mocked: expired, an unknown pairing, the unpredicted 409 (shot
//      pairing-expired-<theme>);
//   9. /xbin/devices: the list, "N of M devices", add → the QR code → a
//      phone's confirmation → enrolled; a phone leaving; a phone (390 px)
//      without sideways scroll (shots pairing-page-*-<theme>).
// It leaves the tunnel off, the admin without devices or an own limit.
const crypto = require('crypto');
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, OUT, fs, login, closeCtx, settle, waitSel, openShell, shot, shotEl, checker, log } = require('../lib');
const { phone, phoneMissing, tunnelEndpoint } = require('../phone');

const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const enc = encodeURIComponent;
const b64 = (n) => crypto.randomBytes(n).toString('base64url');
const PAGE_CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
  "connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";
const now = () => Math.floor(Date.now() / 1000);
const P = 'bx-devices bx-pair';
const TUNNEL_LINK = /^xbin:\/\/enroll\?u=[^&]+&c=[A-Z2-7]+&tv=1&t=(endpoint|fakeiroh)[^&]+&k=[\w-]{43}&p=[\w-]{43}(&rq=1)?$/;

// ---- the real workspace: the tunnel, the admin's devices and limit ----

async function tunnel(request, body) {
  const r = await request.put(`${URL}/api/xbin/app-tunnel`, { data: body });
  if (!r.ok()) throw new Error(`PUT /app-tunnel ${JSON.stringify(body)}: ${r.status()} ${await r.text()}`);
  if (body.enabled === false) return null;
  for (const end = Date.now() + 30000; ;) {
    const v = await (await request.get(`${URL}/api/xbin/app-tunnel`)).json();
    if (v.state === 'running') return v;
    if (Date.now() > end) throw new Error(`the tunnel never ran: ${JSON.stringify(v)}`);
    await new Promise((res) => setTimeout(res, 100));
  }
}
const limit = (request, n) => request.patch(`${URL}/api/xbin/users/admin`, { data: { maxDevices: n } });
const myDevices = async (request) => (await (await request.get(`${URL}/api/xbin/devices`)).json()).devices ?? [];
async function noDevices(request) {
  for (const d of await myDevices(request)) await request.delete(`${URL}/api/xbin/devices/${d.id}`);
}

// ---- the mocked states ----

// The fake: one pairing at a time; f.confirms records every confirm body.
function fake() {
  const f = { n: 0, confirms: [], dev: { devices: [], limit: 3 }, p: null, on409: false };
  f.k = b64(32);
  f.link = (id) => `xbin://enroll?u=${enc(URL)}&c=${id}&tv=1&t=endpoint${'abcdefghijklmnopqrstuvwxyz234567'.repeat(4).slice(0, 110)}` +
    `&k=${f.k}&p=${b64(32)}`;
  f.set = (o) => { Object.assign(f.p, o); };
  return f;
}
const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function routeFake(ctx, f) {
  await ctx.route((u) => u.pathname === '/api/xbin/devices/enroll-code', (route) => {
    const code = `PAIRCODE${String(++f.n).padStart(2, '0')}QRSTUVWXYZ234567`.slice(0, 26);
    const id = `pair-${f.n}`;
    f.p = { id, state: 'minted', expires: now() + 300 };
    return json(route, 200, { code, origin: URL, expires: f.p.expires, url: f.link(code),
      tunnel: { pairing: id, ticket: 'endpoint…', serverKey: f.k, required: false } });
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
    if (f.on409 && !body.replace) {
      f.on409 = false;
      return json(route, 409, { error: `you have ${f.dev.devices.length} devices; this workspace allows ${f.dev.limit} per person — remove one first (an admin can raise your limit)`,
        limit: f.dev.devices.length, count: f.dev.devices.length });
    }
    const id = `dev-${f.n}`;
    f.dev.devices = [...f.dev.devices.filter((d) => d.id !== body.replace), { id, name: f.p.device.name, platform: 'ios', origin: URL, created: now(), tunnel: true }];
    f.set({ state: 'enrolled', deviceId: id, ...(body.replace ? { replaced: body.replace } : {}) });
    return json(route, 200, { state: 'enrolled', deviceId: id, ...(body.replace ? { replaced: body.replace } : {}) });
  });
  await ctx.route((u) => u.pathname === '/api/xbin/devices', (route) => json(route, 200, { ...f.dev, count: f.dev.devices.length }));
  await ctx.route((u) => u.pathname === '/api/xbin/login/methods', async (route) => {
    const m = await (await route.fetch()).json().catch(() => ({}));
    return json(route, 200, { ...m, tunnel: { enabled: true, required: false } });
  });
}

// ---- helpers ----

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
const flat = (s) => s.replace(/\s+/g, ' ').trim();

// scan: a phone pairs with the link the dialog (scope: its bx-pair) shows,
// and the dialog reaches the confirmation with the phone's own code.
async function scan(page, scope, name, check, label, env) {
  const link = await page.locator(`${scope} input[data-link]`).inputValue();
  const ph = phone(link, name, { endpoint: env.endpoint });
  const code = await ph.code();
  await waitSel(page, `${scope} [data-sas]`, { timeout: 15000 });
  await until(async () => (await page.locator(`${scope} [data-sas]`).textContent()).trim() === code, 'the code on the computer').catch(() => {});
  const shown = (await page.locator(`${scope} [data-sas]`).textContent()).trim();
  check(/^\d{3} \d{3}$/.test(code) && shown === code, `${label}: the computer shows the phone's own code (phone ${code}, computer ${shown})`);
  return ph;
}

async function addDevice(page) {
  await page.locator('bx-shell .top button.chip.settings').click();
  await page.locator('bx-shell .wsmenu [data-add-device]').click();
  await waitSel(page, `${P} [data-qr] svg`);
}

async function shellPanel(page, ctx, check, theme, env) {
  const rq = ctx.request;
  await openShell(page);
  await page.evaluate(() => { try { localStorage.removeItem('xbin-phone-address'); } catch { /* none */ } });
  await addDevice(page);

  // 1. the QR code
  const geo = await page.locator(P).evaluate((el) => el.testApi().qr());
  const box = await page.locator(`${P} [data-qr]`).boundingBox();
  check(geo && geo.px >= 288 && geo.ppm >= 4, `${theme}: the QR code is ≥ 288 px at ≥ 4 px per module (${JSON.stringify(geo)})`);
  check(box && Math.round(box.width) === geo.px, `${theme}: drawn at its full size in the panel (${box?.width}px)`);
  const link = await page.locator(`${P} input[data-link]`).inputValue();
  check(TUNNEL_LINK.test(link) && !link.includes('&rq=1'), `${theme}: the raw link is xbind's tunnel link (${link.slice(0, 90)}…)`);
  check(await page.locator(`${P} [data-classic]`).count() === 1, `${theme}: "Older app? Show a classic code" while the tunnel isn't required`);
  const limitWords = flat(await page.locator('bx-devices [data-limit]').textContent());
  check(limitWords === '0 of 2 devices', `${theme}: the panel says "0 of 2 devices" (${limitWords})`);
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
  await page.locator(`${P} details.more summary`).click();

  // 2. a phone pairs; the confirmation; They don't
  let ph = await scan(page, P, "Alice's iPhone 16", check, theme, env);
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
  const heard = await ph.end();
  check(heard.t === 'rejected' && heard.reason === 'mismatch', `${theme}: They don't → the phone hears mismatch (${JSON.stringify(heard)})`);
  check(/didn't match/.test(await page.locator(`${P} [data-pair="rejected"]`).textContent()), `${theme}: …"Not added: the codes didn't match"`);
  check((await myDevices(rq)).length === 0, `${theme}: …and nothing was paired`);
  check(await page.locator('bx-devices .foot [data-add]').count() === 1, `${theme}: the panel offers add again`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-mismatch-${theme}`);

  // 3. Codes match
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  ph = await scan(page, P, 'Old iPhone', check, `${theme} (match)`, env);
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-pair="enrolled"]`);
  const enrolled = await ph.end();
  check(enrolled.t === 'enrolled' && enrolled.user === 'admin', `${theme}: Codes match → the phone hears enrolled (${JSON.stringify(enrolled)})`);
  const old = enrolled.deviceId;
  await page.waitForFunction((id) => document.querySelector('bx-devices')?.shadowRoot?.querySelector(`li[data-device="${id}"]`), old, { timeout: 5000 }).catch(() => {});
  check(await page.locator(`bx-devices li[data-device="${old}"] [data-tunnel]`).count() === 1, `${theme}: the list has the new phone, marked iroh tunnel`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-enrolled-${theme}`);

  // 4. at the limit: which device the phone replaces
  await limit(rq, 1);
  await page.locator('bx-devices .foot [data-add]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  ph = await scan(page, P, 'New iPhone', check, `${theme} (replace)`, env);
  await waitSel(page, `${P} [data-replace]`);
  check(await page.locator(`${P} [data-match]`).isDisabled(), `${theme}: at the limit, Codes match waits for a device to replace`);
  check(/You have 1 of 1 device/.test(await page.locator(`${P} [data-replace] legend`).textContent()), `${theme}: …saying the limit`);
  await page.locator(`${P} [data-replace-device="${old}"] input`).check();
  await settle(page);
  check(!(await page.locator(`${P} [data-match]`).isDisabled()), `${theme}: picked, Codes match is on`);
  await shotEl(page, 'bx-devices .box', `pairing-replace-${theme}`);
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-pair="enrolled"]`);
  const fresh = await ph.end();
  check(fresh.t === 'enrolled', `${theme}: the new phone hears enrolled (${JSON.stringify(fresh)})`);
  const said = flat(await page.locator(`${P} [data-pair="enrolled"]`).textContent());
  check(/New iPhone was added/.test(said) && /Old iPhone was removed/.test(said), `${theme}: enrolled, the replaced one named (${said})`);
  const left = (await myDevices(rq)).map((d) => d.id);
  check(left.length === 1 && left[0] === fresh.deviceId, `${theme}: xbind replaced the old phone (${JSON.stringify(left)})`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', `pairing-replaced-${theme}`);

  // 5. the phone leaves before the confirmation: abandoned
  await limit(rq, 2);
  await page.locator('bx-devices .foot [data-add]').click();
  await waitSel(page, `${P} [data-qr] svg`);
  ph = await scan(page, P, 'Wandering iPhone', check, `${theme} (abandoned)`, env);
  ph.leave();
  await waitSel(page, `${P} [data-pair="abandoned"]`, { timeout: 30000 });
  check(/stopped before you confirmed/.test(await page.locator(`${P} [data-pair="abandoned"]`).textContent()), `${theme}: the phone left: abandoned says so`);
  await settle(page);
  await shotEl(page, `${P}`, `pairing-abandoned-${theme}`);
  // the classic code: asked for without {tunnel}
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-classic]`);
  const asked = [];
  const onReq = (r) => { if (r.url().endsWith('/api/xbin/devices/enroll-code')) asked.push(r.postData() || ''); };
  page.on('request', onReq);
  await page.locator(`${P} [data-classic]`).click();
  await waitSel(page, 'bx-devices [data-enroll] svg');
  page.off('request', onReq);
  check(asked.length === 1 && !asked[0].includes('tunnel'), `${theme}: the classic code is asked for without {tunnel} (${JSON.stringify(asked)})`);
  check(/^xbin:\/\/enroll\?u=[^&]+&c=[A-Z0-9]+$/.test(await page.locator('bx-devices [data-enroll] input[data-link]').inputValue()), `${theme}: …and drawn as the classic link`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  // iroh-only: rq=1 and no classic link
  await tunnel(rq, { tunnelOnly: true });
  await addDevice(page);
  check(await page.locator(`${P} [data-classic]`).count() === 0, `${theme}: no classic link while the tunnel is required`);
  check(/&rq=1$/.test(await page.locator(`${P} input[data-link]`).inputValue()), `${theme}: …the link says rq=1`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  await tunnel(rq, { tunnelOnly: false });

  // 6. the panel at the limit
  await limit(rq, 1);
  await page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices()));
  await waitSel(page, 'bx-devices [data-limit].at');
  const rep = flat(await page.locator('bx-devices .foot').textContent());
  check(/1 of 1 device — a new phone replaces one/.test(rep) && /replace a device…/.test(rep), `${theme}: at the limit with tunnels on: "replace a device…" (${rep})`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  await tunnel(rq, { enabled: false });
  await page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices({ add: true })));
  await waitSel(page, 'bx-devices [data-limit].at');
  await settle(page);
  const off = flat(await page.locator('bx-devices .foot').textContent());
  check(/remove one to add another/.test(off) && await page.locator('bx-devices .foot [data-add]').count() === 0
    && await page.locator('bx-devices bx-pair, bx-devices [data-enroll]').count() === 0,
  `${theme}: at the limit without tunnels: no add (not even from "add a device"), the rows' remove (${off})`);
  check(await page.locator(`bx-devices li[data-device="${fresh.deviceId}"] button.rm`).count() === 1, `${theme}: …each row's remove is right there`);
  await shotEl(page, 'bx-devices .box', `pairing-atlimit-${theme}`);
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
  await tunnel(rq, { enabled: true });
  await noDevices(rq);
  await limit(rq, 2);
}

// 7. a phone-width shell panel on a real confirmation
async function phonePanel(page, check, theme, env) {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices({ add: true })));
  await waitSel(page, `${P} [data-qr] svg`);
  const ph = await scan(page, P, "Alice's iPhone 16 Pro Max", check, `${theme} (phone width)`, env);
  await settle(page);
  const box = await page.locator('bx-devices .box').boundingBox();
  const inner = await page.locator('bx-devices .box').evaluate((b) => b.scrollWidth - b.clientWidth);
  check(box && box.x >= 0 && box.x + box.width <= 390 && inner <= 0, `${theme}: the confirmation fits a phone-width panel (${JSON.stringify(box)}, ${inner}px over)`);
  await shot(page, `pairing-phone-${theme}`, { fullPage: false });
  await page.locator(`${P} [data-mismatch]`).click();
  await waitSel(page, `${P} [data-pair="rejected"]`);
  await ph.end();
  await page.locator('bx-devices .foot button', { hasText: 'close' }).click();
}

// 8. what xbind can't produce on demand: expired, an unknown pairing (404),
// a 409 at the limit the device list didn't predict
async function mockedStates(browser, check, theme) {
  const f = fake();
  const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
  const errs = [];
  A.page.on('pageerror', (e) => errs.push(e.message));
  await routeFake(A.ctx, f);
  const page = A.page;
  await openShell(page);
  await addDevice(page);
  f.set({ state: 'expired' });
  await waitSel(page, `${P} [data-pair="expired"]`);
  await settle(page);
  await shotEl(page, `${P}`, `pairing-expired-${theme}`);
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ gone: true });
  await waitSel(page, `${P} [data-pair="expired"]`);
  check(true, `${theme}: a pairing xbind no longer knows (404) is expired`);
  // the 409 nobody predicted brings the replace question up
  f.dev = { devices: [{ id: 'dev-x', name: 'Some iPhone', platform: 'ios', origin: URL, created: now() - 86400, tunnel: true }], limit: 3 };
  f.on409 = true;
  await page.locator(`${P} [data-again]`).click();
  await waitSel(page, `${P} [data-qr] svg`);
  f.set({ state: 'confirm', sas: 5, device: { name: 'Third phone', platform: 'ios' }, expires: now() + 110 });
  await waitSel(page, `${P} [data-match]`);
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-replace]`);
  check(await page.locator(`${P} .err`).count() === 1, `${theme}: xbind's 409 at the limit shows its words and the question`);
  await page.locator(`${P} [data-replace-device="dev-x"] input`).check();
  await page.locator(`${P} [data-match]`).click();
  await waitSel(page, `${P} [data-pair="enrolled"]`);
  check(f.confirms.at(-1)?.replace === 'dev-x', `${theme}: …and the retry names it`);
  check(!errs.length, `${theme}: no page error in the mocked shell (${errs.join(' | ')})`);
  await closeCtx(A.ctx, page);
}

// 9. /xbin/devices
async function devicesPage(browser, check, theme, env) {
  const W = await login(browser, 'admin', 'admin', { colorScheme: theme });
  const rq = W.ctx.request;
  const errs = [];
  W.page.on('pageerror', (e) => errs.push(e.message));
  const src = await routePage(W.ctx);
  log(`pairing: /xbin/devices from ${src}`);
  await limit(rq, 3);
  await W.page.goto(`${URL}/xbin/devices`);
  await waitSel(W.page, 'bx-devices-page [data-add]');
  check(flat(await W.page.locator('bx-devices-page [data-limit]').textContent()) === '0 of 3 devices', `${theme}: /xbin/devices says "0 of 3 devices"`);
  await W.page.locator('bx-devices-page [data-add]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-qr] svg');
  check(TUNNEL_LINK.test(await W.page.locator('bx-devices-page bx-pair input[data-link]').inputValue()), `${theme}: the page shows a tunnel code`);
  await settle(W.page);
  await shot(W.page, `pairing-page-qr-${theme}`);
  let ph = await scan(W.page, 'bx-devices-page bx-pair', "Bob's iPhone", check, `${theme} (page)`, env);
  await settle(W.page);
  await shot(W.page, `pairing-page-confirm-${theme}`, { fullPage: false });
  // a phone: the confirmation, no sideways scroll
  await W.page.setViewportSize({ width: 390, height: 844 });
  await settle(W.page);
  check(await noSideways(W.page) <= 0, `${theme}: the confirmation fits a phone (${await noSideways(W.page)}px over)`);
  await shot(W.page, `pairing-page-confirm-phone-${theme}`, { fullPage: false });
  await W.page.locator('bx-devices-page bx-pair [data-match]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-pair="enrolled"]');
  const e = await ph.end();
  check(e.t === 'enrolled', `${theme}: the page's Codes match enrolls the phone (${JSON.stringify(e)})`);
  // another: the QR code at phone width, then the phone leaves
  await W.page.locator('bx-devices-page [data-add]').click();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-qr] svg');
  await settle(W.page);
  check(await noSideways(W.page) <= 0, `${theme}: the QR code fits a phone (${await noSideways(W.page)}px over)`);
  await shot(W.page, `pairing-page-qr-phone-${theme}`);
  ph = await scan(W.page, 'bx-devices-page bx-pair', 'Wandering iPhone', check, `${theme} (page, abandoned)`, env);
  ph.leave();
  await waitSel(W.page, 'bx-devices-page bx-pair [data-pair="abandoned"]', { timeout: 30000 });
  await W.page.setViewportSize({ width: 1400, height: 900 });
  await W.page.goto(`${URL}/xbin/devices`);
  await waitSel(W.page, 'bx-devices-page li[data-device]');
  check(flat(await W.page.locator('bx-devices-page [data-limit]').textContent()) === '1 of 3 devices', `${theme}: /xbin/devices says "1 of 3 devices"`);
  check(await W.page.locator(`bx-devices-page li[data-device="${e.deviceId}"] [data-tunnel]`).count() === 1, `${theme}: …and marks the phone tunnel-paired`);
  await settle(W.page);
  await shot(W.page, `pairing-page-${theme}`, { fullPage: false });
  await W.page.setViewportSize({ width: 390, height: 844 });
  await settle(W.page);
  check(await noSideways(W.page) <= 0, `${theme}: the list fits a phone`);
  await shot(W.page, `pairing-page-phone-${theme}`, { fullPage: false });
  check(!errs.length, `${theme}: no page error on /xbin/devices (${errs.join(' | ')})`);
  await noDevices(rq);
  await W.ctx.close();
}

async function pairing(browser) {
  const { check, skip, done } = checker('pairing');
  const why = phoneMissing();
  for (const theme of ['dark', 'light']) {
    if (!why) {
      const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
      const errs = [];
      A.page.on('pageerror', (e) => errs.push(e.message));
      const rq = A.ctx.request;
      try {
        await tunnel(rq, { enabled: true });
        const env = { endpoint: await tunnelEndpoint(rq) };
        await noDevices(rq);
        await limit(rq, 2);
        await shellPanel(A.page, A.ctx, check, theme, env);
        await phonePanel(A.page, check, theme, env);
        check(!errs.length, `${theme}: no page error in the shell (${errs.join(' | ')})`);
        await devicesPage(browser, check, theme, env);
      } finally {
        await noDevices(rq).catch(() => {});
        await limit(rq, null).catch(() => {});
        await tunnel(rq, { enabled: false }).catch(() => {});
        await closeCtx(A.ctx, A.page);
      }
    } else if (theme === 'dark') skip(`the real pairing: ${why}`);
    await mockedStates(browser, check, theme);
  }
  done();
}

module.exports = { pairing };
