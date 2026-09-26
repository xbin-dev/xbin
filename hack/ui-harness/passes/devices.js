// hack/ui-harness/passes/devices.js — device login (docs/auth.md §Device
// login): the shell's account menu → devices… panel mints an enrollment
// code and draws it as a QR code; a scripted "app" (node crypto standing in
// for the Secure Enclave) enrolls with it and signs in; the panel notices
// the new device; the admin console's Users tab lists the user's devices
// and revokes one; removing the other from the panel ends its session.
// Each device row shows its push registration (GET /devices/push) with its
// own remove; registrations no enrolled device owns are listed apart.
// Step-up: a stale sign-in is asked for the password (or to sign in again)
// before a code is minted — the harness's logins are always fresh, so the
// server's answer is stubbed for those checks (the gate itself is
// TestEnrollCodeStepUp). Sign out everywhere asks the admin about devices.
const crypto = require('crypto');
const { execFileSync } = require('child_process');
const { URL, OUT, login, closeCtx, settle, waitSel, openShell, gotoTab, shot, shotEl, checker, log } = require('../lib');

// A stand-in for the app: a P-256 key, enrollment, challenge login.
function appKey() {
  const { privateKey, publicKey } = crypto.generateKeyPairSync('ec', { namedCurve: 'P-256' });
  return { privateKey, spki: publicKey.export({ type: 'spki', format: 'der' }).toString('base64url') };
}
async function post(path, body, bearer) {
  const r = await fetch(`${URL}${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json',
    ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}) }, body: JSON.stringify(body ?? {}) });
  return { status: r.status, body: await r.json().catch(() => ({})) };
}
async function enroll(code, name, key) {
  return post('/api/xbin/devices/enroll', { code, name, platform: 'ios', publicKey: key.spki });
}
async function deviceLogin(dev, key) {
  const ch = await post('/login/device/challenge', { deviceId: dev.deviceId });
  const msg = `xbin-device-login-v1\n${dev.origin}\n${dev.deviceId}\n${ch.body.nonce}`;
  const sig = crypto.sign('sha256', Buffer.from(msg), { key: key.privateKey, dsaEncoding: 'der' }).toString('base64url');
  return post('/login/device', { deviceId: dev.deviceId, nonce: ch.body.nonce, signature: sig });
}
// A push registration as the app makes one: the relay handle and the
// device's X25519 key (raw, base64url).
async function registerPush(deviceId, kinds, { bearer, request } = {}) {
  const { publicKey } = crypto.generateKeyPairSync('x25519');
  const body = { deviceId, handle: crypto.randomBytes(18).toString('base64url'), publicKey: publicKey.export({ format: 'jwk' }).x, kinds };
  if (request) {
    const r = await request.post(`${URL}/api/xbin/devices/push`, { data: body });
    return { status: r.status(), body: await r.json().catch(() => ({})) };
  }
  return post('/api/xbin/devices/push', body, bearer);
}
async function whoamiStatus(bearer) {
  return (await fetch(`${URL}/api/xbin/whoami`, { headers: { Authorization: `Bearer ${bearer}` } })).status;
}

async function devices(browser) {
  const { check, done } = checker('devices');
  const { ctx, page } = await login(browser, 'admin', 'admin');
  page.on('dialog', (d) => d.accept()); // the admin console's revoke confirm
  // Start clean: a rerun on a kept workspace may find devices from before.
  const before = (await (await ctx.request.get(`${URL}/api/xbin/devices`)).json()).devices ?? [];
  for (const d of before) await ctx.request.delete(`${URL}/api/xbin/devices/${d.id}`);

  // ---- the shell: 🔧 → my account → devices… ----
  await openShell(page);
  await page.locator('button[title="workspace settings (per user)"]').click();
  await waitSel(page, '.wsmenu');
  await shotEl(page, '.wsmenu', 'devices-menu');
  check(await page.locator('.wsmenu form input[name=rmdev]').count() === 1, 'the password form offers removing app devices');
  await page.locator('.wsmenu button', { hasText: 'devices…' }).click();
  await waitSel(page, 'bx-devices .box');
  await waitSel(page, 'bx-devices .empty');
  check((await page.locator('bx-devices .empty').textContent()).includes('No devices'), 'the panel opens on an empty list');

  // ---- step-up: "sign in again" (an account without password sign-in) ----
  const ENROLL = '**/api/xbin/devices/enroll-code';
  await page.route(ENROLL, (route) => route.fulfill({ status: 403, contentType: 'application/json',
    body: JSON.stringify({ error: 'adding a device needs a recent sign-in — sign in again, then add it within 10 minutes', stepUp: 'signin' }) }));
  await page.locator('bx-devices button', { hasText: 'add a device' }).click();
  await waitSel(page, 'bx-devices [data-stepup="signin"]');
  check(await page.locator('bx-devices [data-stepup="signin"] button', { hasText: 'sign in again' }).count() === 1
    && await page.locator('bx-devices .foot button', { hasText: 'add a device' }).count() === 0, 'a stale SSO sign-in is asked to sign in again');
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-stepup-signin');
  await page.locator('bx-devices [data-stepup="signin"] button', { hasText: 'not now' }).click();
  await page.unroute(ENROLL);

  // ---- step-up: the password, then the code ----
  let served = 0, sentPassword = null;
  await page.route(ENROLL, (route) => {
    if (served++ === 0) {
      return route.fulfill({ status: 403, contentType: 'application/json',
        body: JSON.stringify({ error: 'confirm your password to add a device', stepUp: 'password' }) });
    }
    try { sentPassword = JSON.parse(route.request().postData() || '{}').password ?? null; } catch { /* no body */ }
    return route.continue();
  });
  await page.locator('bx-devices button', { hasText: 'add a device' }).click();
  await waitSel(page, 'bx-devices [data-stepup="password"] input[name=pw]');
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-stepup');
  await page.locator('bx-devices [data-stepup="password"] input[name=pw]').fill('admin');
  await page.locator('bx-devices [data-stepup="password"] button', { hasText: 'continue' }).click();
  await waitSel(page, 'bx-devices [data-enroll] svg');
  await page.unroute(ENROLL);
  check(sentPassword === 'admin' && !(await page.locator('bx-devices [data-stepup]').count()),
    `the step-up retry carries the password and gives way to the code (${sentPassword})`);
  const link = await page.locator('bx-devices [data-enroll] input').inputValue();
  const u = new globalThis.URL(link);
  const code = u.searchParams.get('c'), origin = u.searchParams.get('u');
  check(u.protocol === 'xbin:' && u.host === 'enroll' && origin === URL && /^[A-Z2-7]{26}$/.test(code),
    `the raw link is xbin://enroll?u=<origin>&c=<code> (${link})`);
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-enroll');
  // The drawn QR code decodes to that same link (zbarimg, when installed).
  await shotEl(page, 'bx-devices .qr', 'devices-qr');
  try {
    const got = execFileSync('zbarimg', ['-q', '--raw', `${OUT}/devices-qr.png`], { encoding: 'utf8' }).trim();
    check(got === link, `the QR code decodes to the link (${got})`);
  } catch (e) { log('devices: zbarimg unavailable or failed — QR decode not checked:', e.message.split('\n')[0]); }

  // ---- the "app" enrolls with the code; the panel notices ----
  const keyA = appKey();
  const devA = await enroll(code, "Admin's iPhone", keyA);
  check(devA.status === 200 && devA.body.origin === URL && devA.body.user === 'admin', `enroll → ${devA.status} ${JSON.stringify(devA.body)}`);
  const again = await enroll(code, 'replay', appKey());
  check(again.status === 401, `the code works once (${again.status})`);
  await waitSel(page, 'bx-devices .ok', { timeout: 10000 });
  check((await page.locator('bx-devices .ok').textContent()).includes("Admin's iPhone"), 'the panel reports the new device');
  const loginA = await deviceLogin(devA.body, keyA);
  check(loginA.status === 200 && loginA.body.user?.id === 'admin' && loginA.body.tokenType === 'Bearer', `device login → ${loginA.status}`);
  const tokA = loginA.body.token;
  check(await whoamiStatus(tokA) === 200, 'the device session works as a bearer');

  // a second device, enrolled straight through the API (for the admin tab)
  const minted = await (await ctx.request.post(`${URL}/api/xbin/devices/enroll-code`)).json();
  const keyB = appKey();
  const devB = await enroll(minted.code, 'Admin iPad', keyB);
  const tokB = (await deviceLogin(devB.body, keyB)).body.token;

  // Push: the phone registers (as the app does after its device login);
  // the browser registers one too — a sign-in without a device key.
  const regA = await registerPush(devA.body.deviceId, ['agent', 'tile'], { bearer: tokA });
  check(regA.status === 200 && regA.body.device?.deviceId === devA.body.deviceId, `the phone registers push (${regA.status} ${JSON.stringify(regA.body)})`);
  for (const r of (await (await ctx.request.get(`${URL}/api/xbin/devices/push`)).json()).devices ?? []) {
    if (r.deviceId === 'harness-browser') await ctx.request.delete(`${URL}/api/xbin/devices/push/${r.deviceId}`);
  }
  const regW = await registerPush('harness-browser', [], { request: ctx.request });
  check(regW.status === 200, `a browser session registers push (${regW.status} ${JSON.stringify(regW.body)})`);

  // Reopen the panel: both devices, last sign-in stamped.
  await page.locator('bx-devices button', { hasText: 'close' }).last().click();
  await page.locator('button[title="workspace settings (per user)"]').click();
  await page.locator('.wsmenu button', { hasText: 'devices…' }).click();
  await waitSel(page, 'bx-devices li[data-device]');
  await page.waitForFunction(() => document.querySelector('bx-devices')?.shadowRoot?.querySelectorAll('li[data-device]').length === 2, null, { timeout: 10000 });
  const subs = await page.locator('bx-devices li[data-device] .sub').allTextContents();
  check(subs.length === 2 && subs.every((s) => /last sign-in just now/.test(s)), `both devices listed with a fresh sign-in (${JSON.stringify(subs)})`);
  // …each with its push registration, or none
  await waitSel(page, `bx-devices li[data-device="${devA.body.deviceId}"] [data-push]`);
  const pushA = await page.locator(`bx-devices li[data-device="${devA.body.deviceId}"] .push`).textContent();
  const pushB = await page.locator(`bx-devices li[data-device="${devB.body.deviceId}"] .push`).textContent();
  check(/notifications: agent, tile/.test(pushA) && /none sent yet/.test(pushA), `the phone's row shows its push registration (${pushA.trim()})`);
  check(/no push notifications/.test(pushB), `the iPad's row shows none (${pushB.trim()})`);
  check(await page.locator('bx-devices li[data-push-other="harness-browser"] [data-push]').count() === 1,
    'the browser\'s registration is listed apart');
  check(await page.locator('bx-devices [data-push-off]').count() === 1, 'the panel says push is off on this workspace (no relay)');
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-list');
  // remove the phone's registration from its row — the device stays
  await page.locator(`bx-devices li[data-device="${devA.body.deviceId}"] [data-push] button`, { hasText: 'remove' }).click();
  await page.waitForFunction((id) => /no push notifications/.test(document.querySelector('bx-devices')?.shadowRoot
    ?.querySelector(`li[data-device="${id}"] .push`)?.textContent ?? ''), devA.body.deviceId, { timeout: 10000 });
  const left = (await (await ctx.request.get(`${URL}/api/xbin/devices/push`)).json()).devices ?? [];
  check(!left.some((r) => r.deviceId === devA.body.deviceId) && left.some((r) => r.deviceId === 'harness-browser'),
    `removing the phone's push registration leaves the rest (${JSON.stringify(left.map((r) => r.deviceId))})`);
  check(await whoamiStatus(tokA) === 200, 'removing a push registration keeps the device signed in');
  await page.locator('bx-devices li[data-push-other="harness-browser"] [data-push] button', { hasText: 'remove' }).click();
  await page.locator('bx-devices li[data-push-other]').waitFor({ state: 'detached', timeout: 10000 });
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-push-removed');

  // ---- the admin console: Users → admin → devices (2)… → revoke the iPad ----
  const admin = await login(browser, 'admin', 'admin');
  const dialogs = [];
  admin.page.on('dialog', (d) => { dialogs.push(d.message()); d.accept(); });
  await gotoTab(admin.page, 'users', 'add user');
  const row = admin.page.locator('tr', { has: admin.page.locator('td.user .mono', { hasText: /^admin$/ }) }).first();
  await row.locator('button', { hasText: 'devices (2)' }).click();
  await waitSel(admin.page, '[data-devices="admin"] [data-device]');
  await settle(admin.page);
  await shot(admin.page, 'devices-admin-users');
  await admin.page.locator(`[data-devices="admin"] [data-device="${devB.body.deviceId}"] button`, { hasText: 'revoke' }).click();
  await admin.page.waitForSelector(`[data-devices="admin"] [data-device="${devB.body.deviceId}"]`, { state: 'detached', timeout: 10000 });
  check(await whoamiStatus(tokB) === 401, 'revoking in the admin console ends that device\'s session');
  check(await whoamiStatus(tokA) === 200, 'the other device is untouched');
  // sessions tab marks app sessions
  const sess = (await (await admin.ctx.request.get(`${URL}/api/xbin/sessions`)).json()).sessions ?? [];
  check(sess.some((s) => s.user === 'admin' && s.via === 'device' && s.device === devA.body.deviceId), 'sessions list the device session (via device)');

  // ---- sign out everywhere asks about devices (a throwaway user) ----
  const TU = 'devtest';
  await admin.ctx.request.delete(`${URL}/api/xbin/users/${TU}`);
  await admin.ctx.request.post(`${URL}/api/xbin/users`, { data: { id: TU, password: 'devtest-pass-1' } });
  const tu = await login(browser, TU, 'devtest-pass-1');
  const tuCode = await (await tu.ctx.request.post(`${URL}/api/xbin/devices/enroll-code`)).json();
  const keyC = appKey();
  const devC = await enroll(tuCode.code, 'Test phone', keyC);
  const tokC = (await deviceLogin(devC.body, keyC)).body.token;
  await closeCtx(tu.ctx, tu.page);
  await gotoTab(admin.page, 'users', 'add user');
  const trow = admin.page.locator('tr', { has: admin.page.locator('td.user .mono', { hasText: new RegExp(`^${TU}$`) }) }).first();
  await trow.locator('summary', { hasText: 'more' }).click();
  dialogs.length = 0;
  await trow.locator('button', { hasText: 'sign out everywhere' }).click();
  await admin.page.waitForSelector(`text=signed out ${TU}`, { timeout: 10000 });
  const gone = await trow.locator('button', { hasText: 'devices (' }).waitFor({ state: 'detached', timeout: 10000 }).then(() => true, () => false);
  check(gone, 'the users table refreshes: no devices left on the row');
  await settle(admin.page);
  await shot(admin.page, 'devices-admin-signout');
  check(dialogs.length === 2 && /1 xbin app device enrolled/.test(dialogs[1]), `sign out everywhere asks about the device (${JSON.stringify(dialogs)})`);
  check(await whoamiStatus(tokC) === 401, 'the device session ended');
  const chC = await post('/login/device/challenge', { deviceId: devC.body.deviceId });
  check(chC.status === 404, `accepting removed the device (${chC.status})`);
  await admin.ctx.request.delete(`${URL}/api/xbin/users/${TU}`);
  await closeCtx(admin.ctx, admin.page);

  // ---- remove the phone from the shell panel ----
  await page.locator('bx-devices button', { hasText: 'close' }).last().click();
  await page.locator('button[title="workspace settings (per user)"]').click();
  await page.locator('.wsmenu button', { hasText: 'devices…' }).click();
  await waitSel(page, `bx-devices li[data-device="${devA.body.deviceId}"]`);
  await page.locator(`bx-devices li[data-device="${devA.body.deviceId}"] button`, { hasText: 'remove…' }).click();
  await settle(page);
  await shotEl(page, 'bx-devices .box', 'devices-remove-confirm');
  await page.locator(`bx-devices li[data-device="${devA.body.deviceId}"] button.rm`, { hasText: /^remove$/ }).click();
  await waitSel(page, 'bx-devices .empty');
  check(await whoamiStatus(tokA) === 401, 'removing from the panel signs the device out');
  const ch = await post('/login/device/challenge', { deviceId: devA.body.deviceId });
  check(ch.status === 404, `a removed device can't ask for a challenge (${ch.status})`);
  await closeCtx(ctx, page);

  // ---- phone width: the panel with a live code fits a 390px screen ----
  const phone = await login(browser, 'admin', 'admin', { viewport: { width: 390, height: 844 } });
  await openShell(phone.page);
  await phone.page.evaluate(() => import('/c/shell/bx-devices.js').then((m) => m.openDevices()));
  await waitSel(phone.page, 'bx-devices .box');
  await phone.page.locator('bx-devices button', { hasText: 'add a device' }).click();
  await waitSel(phone.page, 'bx-devices [data-enroll] svg');
  const box = await phone.page.locator('bx-devices .box').boundingBox();
  check(box && box.x >= 0 && box.x + box.width <= 390, `the panel fits the phone width (${JSON.stringify(box)})`);
  await shot(phone.page, 'devices-phone', { fullPage: false });
  await closeCtx(phone.ctx, phone.page);
  done();
}

module.exports = { devices };
