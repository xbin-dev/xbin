// hack/ui-harness/passes/apptunnel.js — the admin console's iroh tunneling
// (D204, docs/app-tunnel.md): workspace → xbin app, under the native
// runtime card, and users → devices…'s own limit, in both themes — against
// the harness xbind's real tunnel (run.sh: HARNESS_IROH, XBIN_IROH_RELAY=
// disabled) with a real phone (../phone.js) connected through it. What
// that xbind can't show is served by route interception: n0's relay, a
// custom relay's tokens (XBIN_IROH_RELAY overrides the relay: a PUT of one
// is 409), a failed unsandboxed sidecar, and an xbind without tunnels.
//   real:
//   1. off: the switch and what it does (shot apptunnel-off-<theme>);
//   2. on: running, sandboxed as xbind says, its endpoint id, the relay
//      XBIN_IROH_RELAY sets (the choice greyed out); a phone pairs and
//      connects: "1 connected now · 1 tunnel-paired · 1 classic · 1 of 1
//      synced"; Test reaches it (shot apptunnel-on-<theme>);
//   3. refuse app connections outside the tunnel: "1 device isn't
//      tunnel-paired", a confirm (cancel leaves it off, nothing saved), then
//      on, xbind's tunnelOnly (shot apptunnel-only-<theme>), and off again;
//   4. the default device limit, saved; users → dev1 → devices…: their own
//      limit set and cleared, as xbind keeps it (shot
//      apptunnel-userlimit-<theme>);
//   5. a phone (390 px): no sideways scroll (shot apptunnel-phone-<theme>);
//   mocked:
//   6. n0: the relay note "hobby-scale; publishes this server's endpoint to
//      n0 DNS", its home relay (shot apptunnel-n0-<theme>);
//   7. a custom relay: its URL, the token slots — generate next (shown
//      once), set next, promote, drop next — only hints otherwise (shot
//      apptunnel-custom-<theme>);
//   8. failed, unsandboxed (shot apptunnel-failed-<theme>); an xbind without
//      tunnels (its 404 and 405): nothing of them.
// It leaves the tunnel off, no devices, the default limit 1, dev1 without
// a limit of their own.
const crypto = require('crypto');
const { URL, login, closeCtx, settle, waitSel, gotoTab, shot, checker } = require('../lib');
const { phone, phoneMissing, tunnelEndpoint } = require('../phone');

const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
const ENDPOINT = 'a6c9f8e2d41b3f7c0e95a8d27b6f4c13e8a0d5b9c2f6e1a74d3b8c0f9e2a5d61';
const S = 'bx-admin-apptunnel';

// ---- the real workspace ----

async function view(rq) { return (await rq.get(`${URL}/api/xbin/app-tunnel`)).json(); }
async function off(rq) {
  await rq.put(`${URL}/api/xbin/app-tunnel`, { data: { enabled: false } });
  for (const d of (await (await rq.get(`${URL}/api/xbin/devices`)).json()).devices ?? []) await rq.delete(`${URL}/api/xbin/devices/${d.id}`);
  await rq.patch(`${URL}/api/xbin/users/admin`, { data: { maxDevices: null } });
  await rq.patch(`${URL}/api/xbin/users/dev1`, { data: { maxDevices: null } });
  await rq.put(`${URL}/api/xbin/devices/limits`, { data: { default: 1 } });
}
// a classic device for the admin (the app before tunnels): a P-256 key and
// a classic code, as passes/devices.js enrolls one
async function classicDevice(rq) {
  const { publicKey } = crypto.generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const minted = await (await rq.post(`${URL}/api/xbin/devices/enroll-code`, { data: {} })).json();
  const r = await rq.post(`${URL}/api/xbin/devices/enroll`, { data: { code: minted.code, name: 'Old classic iPhone', platform: 'ios',
    publicKey: publicKey.export({ type: 'spki', format: 'der' }).toString('base64url') } });
  return r.status();
}
// a phone pairs through the API (enroll-code {tunnel}, the confirmation)
// and holds a tunnel connection, signed in
async function connectedPhone(rq, endpoint) {
  const m = await (await rq.post(`${URL}/api/xbin/devices/enroll-code`, { data: { tunnel: true } })).json();
  const ph = phone(m.url, 'Console iPhone', { endpoint, connect: true });
  await ph.code();
  for (const end = Date.now() + 10000; ;) {
    const st = await (await rq.get(`${URL}/api/xbin/devices/pairing/${m.tunnel.pairing}`)).json();
    if (st.state === 'confirm') break;
    if (Date.now() > end) throw new Error(`the pairing never reached confirm: ${JSON.stringify(st)}`);
    await new Promise((r) => setTimeout(r, 100));
  }
  const c = await rq.post(`${URL}/api/xbin/devices/pairing/${m.tunnel.pairing}/confirm`, { data: { match: true } });
  if (!c.ok()) throw new Error(`confirm: ${c.status()} ${await c.text()}`);
  await ph.signedIn();
  return ph;
}

// ---- the mocked xbind ----

function fake() {
  const f = {
    v: { enabled: false, tunnelOnly: false, relay: { mode: 'n0' }, state: 'disabled', confined: true, restarts: 0,
      endpointId: '', homeRelay: '', devices: { total: 3, tunnelPaired: 0, classic: 3, connected: 0, synced: 0 }, conns: [] },
    lim: { default: 1, max: 32, atLimit: [], overrides: [] },
    puts: [], tokens: [], absent: false,
  };
  f.running = () => Object.assign(f.v, { state: 'running', endpointId: ENDPOINT,
    homeRelay: f.v.relay.mode === 'n0' ? 'https://euc1-1.relay.n0.iroh-canary.iroh.link./' : f.v.relay.url,
    devices: { total: 3, tunnelPaired: 2, classic: 1, connected: 1, synced: 2 } });
  return f;
}

async function routeFake(ctx, f) {
  await ctx.route((u) => u.pathname === '/api/xbin/app-tunnel', (route) => {
    if (f.absent) { f.absentSeen = (f.absentSeen ?? 0) + 1; return json(route, 404, { error: 'not found' }); }
    const r = route.request();
    if (r.method() === 'PUT') {
      const b = JSON.parse(r.postData() || '{}');
      f.puts.push(b);
      if ('enabled' in b) { f.v.enabled = b.enabled; if (b.enabled) f.running(); else Object.assign(f.v, { state: 'disabled', tunnelOnly: false }); }
      if (b.relay) { f.v.relay = b.relay.mode === 'n0' ? { mode: 'n0' } : { mode: 'custom', url: b.relay.url, tokenHint: f.v.relay.tokenHint }; f.running(); }
      if ('tunnelOnly' in b) f.v.tunnelOnly = b.tunnelOnly;
    }
    return json(route, 200, f.v);
  });
  await ctx.route((u) => u.pathname === '/api/xbin/app-tunnel/relay-tokens', (route) => {
    const b = JSON.parse(route.request().postData() || '{}');
    f.tokens.push(b);
    const r = f.v.relay;
    let token;
    switch (b.action) {
      case 'generate-next': token = 'xr_9QmZ3vKp7TnW2yHs8LdC4fJb6RgX1aEo'; r.next = { url: r.url, tokenHint: `…${token.slice(-4)}` }; break;
      case 'set-next': r.next = { url: r.url, tokenHint: `…${b.token.slice(-4)}` }; break;
      case 'promote': r.tokenHint = r.next?.tokenHint; delete r.next; break;
      case 'drop-next': delete r.next; break;
    }
    return json(route, 200, token ? { ...f.v, token } : f.v);
  });
  await ctx.route((u) => u.pathname === '/api/xbin/app-tunnel/test', (route) => json(route, 200, { reachable: true, path: 'relay', rttMs: 42 }));
  await ctx.route((u) => u.pathname === '/api/xbin/devices/limits', (route) => {
    // today's xbind: its DELETE /devices/{id} pattern answers GET /devices/limits 405
    if (f.absent) { f.absentSeen = (f.absentSeen ?? 0) + 1; return route.fulfill({ status: 405, contentType: 'text/plain', body: 'Method Not Allowed\n' }); }
    return json(route, 200, f.lim);
  });
}

// a full-page shot of the console from its top (scrolled, the sticky nav lands mid-image)
async function snap(page, name, opts) {
  await page.evaluate(() => window.scrollTo(0, 0));
  await settle(page);
  await shot(page, name, opts);
}
const until = async (fn, label, timeout = 10000) => {
  const end = Date.now() + timeout;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error(`timed out: ${label}`);
    await new Promise((r) => setTimeout(r, 100));
  }
};
const flat = (s) => s.replace(/\s+/g, ' ').trim();

// ---- the real console ----

async function realConsole(browser, check, theme) {
  const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
  const page = A.page;
  const rq = A.ctx.request;
  const errs = [];
  const dialogs = [];
  let answer = false;
  page.on('pageerror', (e) => errs.push(e.message));
  page.on('dialog', (d) => { dialogs.push(d.message()); return answer ? d.accept() : d.dismiss(); });
  let ph = null;
  try {
    await off(rq);
    check(await classicDevice(rq) === 200, `${theme}: a classic device for the admin`);
    await rq.patch(`${URL}/api/xbin/users/admin`, { data: { maxDevices: 3 } });

    // 1. off
    await gotoTab(page, 'nativeapp', 'Iroh tunneling');
    await waitSel(page, `${S} [data-apptunnel="off"]`);
    check(await page.locator('bx-admin-nativeapp [data-native-runtime]').count() === 1, `${theme}: the native runtime card is still there, above`);
    check(await page.locator(`${S} [data-relay]`).count() === 0, `${theme}: off, no relay or status`);
    check(await page.locator(`${S} a[href="/docs/app-tunnel.md"]`).count() >= 1, `${theme}: a link to /docs/app-tunnel.md`);
    await snap(page, `apptunnel-off-${theme}`, { fullPage: false });

    // 2. on: the real sidecar
    await page.locator(`${S} [data-enable]`).check();
    await waitSel(page, `${S} [data-state="running"]`, { timeout: 30000 });
    let v = await view(rq);
    check(v.enabled && v.state === 'running', `${theme}: the switch turned xbind's tunnel on (${v.state})`);
    check((await page.locator(`${S} [data-endpoint]`).textContent()).trim() === v.endpointId && /^[0-9a-f]{64}$/.test(v.endpointId), `${theme}: the endpoint id, xbind's`);
    const conf = (await page.locator(`${S} [data-confined]`).textContent()).trim();
    check(v.confined ? /^yes/.test(conf) : /^no/.test(conf), `${theme}: sandboxed as xbind says (${v.confined}: ${conf.slice(0, 40)})`);
    const env = flat(await page.locator(`${S} [data-relay-env]`).textContent().catch(() => ''));
    check(/XBIN_IROH_RELAY=disabled/.test(env) && /direct connections only/.test(env), `${theme}: the relay XBIN_IROH_RELAY sets is said (${env})`);
    check(await page.locator(`${S} [data-relay="custom"]`).isDisabled() && await page.locator(`${S} [data-relay="n0"]`).isDisabled(),
      `${theme}: …and the relay choice greyed out under it`);
    ph = await connectedPhone(rq, await tunnelEndpoint(rq));
    await gotoTab(page, 'nativeapp', 'Iroh tunneling'); // connections and syncs publish no event: a fresh look
    await waitSel(page, `${S} [data-state="running"]`);
    const dev = flat(await page.locator(`${S} [data-devices]`).textContent());
    check(dev === '1 connected now · 1 tunnel-paired · 1 classic · 1 of 1 synced', `${theme}: the phone connected and synced, the classic device counted (${dev})`);
    await page.locator(`${S} [data-test]`).click();
    await waitSel(page, `${S} [data-test-result="ok"]`, { timeout: 30000 });
    check(/reachable via direct · \d+ ms/.test(await page.locator(`${S} [data-test-result]`).textContent()), `${theme}: Test → reachable via direct (relays off)`);
    await snap(page, `apptunnel-on-${theme}`);

    // 3. refuse outside the tunnel
    const lock = flat(await page.locator(`${S} [data-lockout]`).textContent());
    check(/1 device isn't tunnel-paired/.test(lock), `${theme}: the warning counts the classic device (${lock})`);
    answer = false;
    await page.locator(`${S} [data-tunnel-only]`).click();
    await settle(page);
    check(dialogs.length === 1 && !(await page.locator(`${S} [data-tunnel-only]`).isChecked()) && !(await view(rq)).tunnelOnly,
      `${theme}: cancelling the confirm leaves it off, nothing saved`);
    answer = true;
    await page.locator(`${S} [data-tunnel-only]`).click();
    await waitSel(page, `${S} [data-tunnel-only]:checked`);
    await until(async () => (await view(rq)).tunnelOnly, 'tunnelOnly saved');
    check((await view(rq)).tunnelOnly, `${theme}: confirmed → xbind refuses the app outside the tunnel`);
    check(/locked out until paired again/.test(await page.locator(`${S} [data-lockout]`).textContent()), `${theme}: …the warning says it is locked out`);
    await snap(page, `apptunnel-only-${theme}`);
    await page.locator(`${S} [data-tunnel-only]`).click();
    await until(async () => !(await view(rq)).tunnelOnly, 'tunnelOnly off');
    check(!(await page.locator(`${S} [data-tunnel-only]`).isChecked()), `${theme}: …and off again`);

    // 4. the default limit; dev1's own
    await page.locator(`${S} [data-limit-default]`).fill('3');
    await page.locator(`${S} [data-device-limit] button[type=submit]`).click();
    await until(async () => (await (await rq.get(`${URL}/api/xbin/devices/limits`)).json()).default === 3, 'the default limit saved');
    check(true, `${theme}: the default device limit → xbind's default is 3`);
    const atLim = flat(await page.locator(`${S} [data-at-limit]`).textContent());
    check(/^0 people at their limit · 1 person with a limit of their own$/.test(atLim), `${theme}: how many people are at their limit, in words (${atLim})`);
    await rq.put(`${URL}/api/xbin/devices/limits`, { data: { default: 1 } });
    await gotoTab(page, 'users', 'add user');
    const row = page.locator('tr', { has: page.locator('td.user .mono', { hasText: /^dev1$/ }) }).first();
    await row.locator('button', { hasText: /^devices…$/ }).click();
    await waitSel(page, '[data-device-limit="dev1"]');
    check(/0 of 1 — the workspace default/.test(flat(await page.locator('[data-device-limit="dev1"]').textContent())), `${theme}: dev1 has no devices, the default limit, the button shown anyway`);
    await page.locator('[data-device-limit="dev1"] input[name=n]').fill('2');
    await page.locator('[data-device-limit="dev1"] button[type=submit]').click();
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).waitFor();
    let own = await (await rq.get(`${URL}/api/xbin/users/dev1/devices`)).json();
    check(own.limit === 2 && own.override === 2, `${theme}: set → xbind keeps dev1's own limit (${JSON.stringify(own)})`);
    check(/0 of 2 — their own/.test(flat(await page.locator('[data-device-limit="dev1"]').textContent())), `${theme}: …their own limit now`);
    await snap(page, `apptunnel-userlimit-${theme}`, { fullPage: false });
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).click();
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).waitFor({ state: 'detached' });
    own = await (await rq.get(`${URL}/api/xbin/users/dev1/devices`)).json();
    check(own.limit === 1 && own.override === null, `${theme}: use the default → cleared (${JSON.stringify(own)})`);

    // 5. a phone
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoTab(page, 'nativeapp', 'Iroh tunneling');
    await waitSel(page, `${S} [data-state="running"]`);
    const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
    check(over <= 0, `${theme}: no sideways scroll on a phone (${over}px over)`);
    await snap(page, `apptunnel-phone-${theme}`);
    check(!errs.length, `${theme}: no page error (${errs.join(' | ')})`);
  } finally {
    ph?.leave();
    await off(rq).catch(() => {});
    await closeCtx(A.ctx, page);
  }
}

// ---- the mocked console ----

async function mockedConsole(browser, check, theme) {
  const f = fake();
  const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
  const errs = [];
  A.page.on('pageerror', (e) => errs.push(e.message));
  await routeFake(A.ctx, f);
  const page = A.page;

  // 6. n0
  await gotoTab(page, 'nativeapp', 'Iroh tunneling');
  await waitSel(page, `${S} [data-apptunnel="off"]`);
  await page.locator(`${S} [data-enable]`).check();
  await waitSel(page, `${S} [data-state="running"]`);
  const n0 = flat(await page.locator(`${S} label`, { has: page.locator('[data-relay="n0"]') }).textContent());
  check(/hobby-scale; publishes this server's endpoint to n0 DNS/.test(n0), `${theme}: n0's note (${n0})`);
  check(await page.locator(`${S} [data-relay-env]`).count() === 0 && !(await page.locator(`${S} [data-relay="custom"]`).isDisabled()),
    `${theme}: without XBIN_IROH_RELAY, no override note and the relay choice open`);
  await page.locator(`${S} [data-test]`).click();
  await waitSel(page, `${S} [data-test-result="ok"]`);
  check(/reachable via relay · 42 ms/.test(await page.locator(`${S} [data-test-result]`).textContent()), `${theme}: Test → reachable via relay · 42 ms`);
  await snap(page, `apptunnel-n0-${theme}`);

  // 7. a custom relay and its tokens
  await page.locator(`${S} [data-relay="custom"]`).check();
  await waitSel(page, `${S} [data-relay-url]`);
  check(f.puts.length === 1, `${theme}: picking "your own relay" saves nothing yet`);
  await page.locator(`${S} [data-relay-url]`).fill('https://relay.example.com');
  await page.locator(`${S} button`, { hasText: 'use this relay' }).click();
  await waitSel(page, `${S} [data-token="current"]`);
  check(JSON.stringify(f.puts.at(-1)) === '{"relay":{"mode":"custom","url":"https://relay.example.com"}}', `${theme}: → PUT {relay:{mode:custom, url}} (${JSON.stringify(f.puts.at(-1))})`);
  await page.locator(`${S} [data-act="generate-next"]`).click();
  await waitSel(page, `${S} [data-generated] code`);
  check((await page.locator(`${S} [data-generated] code`).textContent()).trim() === 'xr_9QmZ3vKp7TnW2yHs8LdC4fJb6RgX1aEo', `${theme}: a generated token is shown once`);
  check((await page.locator(`${S} [data-token="next"] .mono`).textContent()).trim() === '…1aEo', `${theme}: …and the next slot holds its hint`);
  check(await page.locator(`${S} [data-synced]`).count() === 1, `${theme}: how many devices know the next relay settings`);
  await snap(page, `apptunnel-custom-${theme}`);
  await page.locator(`${S} [data-generated] button`, { hasText: "I've copied it" }).click();
  await page.locator(`${S} [data-generated]`).waitFor({ state: 'detached' });
  await page.locator(`${S} [data-act="set-next"]`).click();
  await page.locator(`${S} [data-set-next] input[name=tok]`).fill('a-relay-token-of-my-own-7Hq2');
  await page.locator(`${S} [data-set-next] button[type=submit]`).click();
  await page.locator(`${S} [data-set-next]`).waitFor({ state: 'detached' });
  check(JSON.stringify(f.tokens.at(-1)) === '{"action":"set-next","token":"a-relay-token-of-my-own-7Hq2"}', `${theme}: set next → {action, token}`);
  check(await page.locator(`${S}`).evaluate((el) => !el.shadowRoot.textContent.includes('a-relay-token-of-my-own')), `${theme}: …and only its hint is shown after`);
  await page.locator(`${S} [data-act="promote"]`).click();
  await page.locator(`${S} [data-act="promote"]`).waitFor({ state: 'detached' });
  check((await page.locator(`${S} [data-token="current"] .mono`).textContent()).trim() === '…7Hq2', `${theme}: promote → the current slot holds it`);
  await page.locator(`${S} [data-act="generate-next"]`).click();
  await waitSel(page, `${S} [data-act="drop-next"]`);
  await page.locator(`${S} [data-act="drop-next"]`).click();
  await page.locator(`${S} [data-act="drop-next"]`).waitFor({ state: 'detached' });
  check(f.tokens.map((t) => t.action).join(',') === 'generate-next,set-next,promote,generate-next,drop-next', `${theme}: the token actions (${f.tokens.map((t) => t.action)})`);

  // 8. failed, unsandboxed; no tunnels at all
  Object.assign(f.v, { state: 'failed', confined: false, restarts: 4, error: 'xbin-iroh exited: relay https://relay.example.com refused the token' });
  await gotoTab(page, 'nativeapp', 'Iroh tunneling');
  await waitSel(page, `${S} [data-state="failed"]`);
  check(/refused the token/.test(await page.locator(`${S} [data-state-error]`).textContent()), `${theme}: failed, with xbind's error`);
  check(/^no/.test((await page.locator(`${S} [data-confined]`).textContent()).trim()), `${theme}: sandboxed: no`);
  await snap(page, `apptunnel-failed-${theme}`);
  f.absent = true;
  await gotoTab(page, 'nativeapp', 'Native tile UIs');
  await until(async () => f.absentSeen >= 2, 'the section asked and heard 404');
  await settle(page);
  check(await page.locator(`${S} .card`).count() === 0 && await page.locator('bx-admin-nativeapp [data-native-runtime]').count() === 1,
    `${theme}: an xbind without app tunnels (404, 405): nothing of them, the native card as before`);
  check(!errs.length, `${theme}: no page error, mocked (${errs.join(' | ')})`);
  await closeCtx(A.ctx, page);
}

async function apptunnel(browser) {
  const { check, skip, done } = checker('apptunnel');
  const why = phoneMissing();
  for (const theme of ['dark', 'light']) {
    if (!why) await realConsole(browser, check, theme);
    else if (theme === 'dark') skip(`the real console: ${why}`);
    await mockedConsole(browser, check, theme);
  }
  done();
}

module.exports = { apptunnel };
