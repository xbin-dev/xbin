// hack/ui-harness/passes/apptunnel.js — the admin console's iroh tunneling
// (D204, docs/app-tunnel.md): workspace → xbin app, under the native
// runtime card, and users → devices…'s own limit, in both themes. xbind's
// routes (GET/PUT /app-tunnel, /app-tunnel/relay-tokens, /app-tunnel/test,
// /devices/limits, /users/{id}/devices {limit, override}, PATCH /users/{id}
// {maxDevices}) are served by a fake in this file, shaped like the spec,
// until a harness xbind has them.
//   1. off: the switch and what it does (shot apptunnel-off-<theme>);
//   2. on (n0): the relay note "hobby-scale; publishes this server's
//      endpoint to n0 DNS", the status (running, sandboxed, endpoint id,
//      home relay, devices), Test (shot apptunnel-n0-<theme>);
//   3. a custom relay: its URL, the token slots — generate next (shown
//      once), set next, promote, drop next — only hints otherwise (shot
//      apptunnel-custom-<theme>);
//   4. refuse app connections outside the tunnel: "3 devices aren't
//      tunnel-paired", a confirm (cancel leaves it off), then on (shot
//      apptunnel-only-<theme>);
//   5. failed, unsandboxed (shot apptunnel-failed-<theme>); the default
//      device limit; an xbind without tunnels (its 404 and 405): nothing of
//      them;
//   6. users → dev1 → devices… (none yet, shown anyway): their own limit
//      set and cleared (shot apptunnel-userlimit-<theme>);
//   7. a phone (390 px): no sideways scroll (shot apptunnel-phone-<theme>).
const { URL, login, closeCtx, settle, waitSel, gotoTab, shot, checker } = require('../lib');

const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
const ENDPOINT = 'a6c9f8e2d41b3f7c0e95a8d27b6f4c13e8a0d5b9c2f6e1a74d3b8c0f9e2a5d61';

function fake() {
  const f = {
    v: { enabled: false, tunnelOnly: false, relay: { mode: 'n0' }, state: 'disabled', confined: true, restarts: 0,
      endpointId: '', homeRelay: '', devices: { total: 3, tunnelPaired: 0, classic: 3, connected: 0, synced: 0 } },
    lim: { default: 1, max: 32, atLimit: 2, overrides: [{ user: 'dev1', maxDevices: 3 }] },
    user: { limit: 1, override: null },
    puts: [], tokens: [], limits: [], patches: [], absent: false,
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
    const r = route.request();
    if (r.method() === 'PUT') { const b = JSON.parse(r.postData() || '{}'); f.limits.push(b); f.lim.default = b.default; }
    return json(route, 200, f.lim);
  });
  // users: rows carry deviceLimit (D204); dev1's devices… carries limit/override
  await ctx.route((u) => u.pathname === '/api/xbin/users', async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const r = await route.fetch();
    const body = await r.json();
    for (const u of body.users ?? []) { u.deviceLimit = u.id === 'dev1' ? f.user.limit : 1; u.maxDevices = u.id === 'dev1' ? f.user.override : null; }
    return json(route, 200, body);
  });
  await ctx.route((u) => u.pathname === '/api/xbin/users/dev1/devices', (route) => json(route, 200, { devices: [], ...f.user }));
  await ctx.route((u) => u.pathname === '/api/xbin/users/dev1', (route) => {
    const r = route.request();
    const b = r.method() === 'PATCH' ? JSON.parse(r.postData() || '{}') : {};
    if (!('maxDevices' in b)) return route.continue();
    f.patches.push(b);
    f.user = b.maxDevices === null ? { limit: f.lim.default, override: null } : { limit: b.maxDevices, override: b.maxDevices };
    return json(route, 200, { ok: true });
  });
}

const S = 'bx-admin-apptunnel';
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

async function apptunnel(browser) {
  const { check, done } = checker('apptunnel');
  for (const theme of ['dark', 'light']) {
    const f = fake();
    const A = await login(browser, 'admin', 'admin', { colorScheme: theme });
    const errs = [];
    const dialogs = [];
    let answer = false;
    A.page.on('pageerror', (e) => errs.push(e.message));
    A.page.on('dialog', (d) => { dialogs.push(d.message()); return answer ? d.accept() : d.dismiss(); });
    await routeFake(A.ctx, f);
    const page = A.page;

    // 1. off
    await gotoTab(page, 'nativeapp', 'Iroh tunneling');
    await waitSel(page, `${S} [data-apptunnel="off"]`);
    check(await page.locator('bx-admin-nativeapp [data-native-runtime]').count() === 1, `${theme}: the native runtime card is still there, above`);
    check(await page.locator(`${S} [data-relay]`).count() === 0, `${theme}: off, no relay or status`);
    check(await page.locator(`${S} a[href="/docs/app-tunnel.md"]`).count() >= 1, `${theme}: a link to /docs/app-tunnel.md`);
    await snap(page, `apptunnel-off-${theme}`, { fullPage: false });

    // 2. on, n0
    await page.locator(`${S} [data-enable]`).check();
    await waitSel(page, `${S} [data-state="running"]`);
    check(JSON.stringify(f.puts.at(-1)) === '{"enabled":true}', `${theme}: the switch PUTs {enabled:true}`);
    const n0 = flat(await page.locator(`${S} label`, { has: page.locator('[data-relay="n0"]') }).textContent());
    check(/hobby-scale; publishes this server's endpoint to n0 DNS/.test(n0), `${theme}: n0's note (${n0})`);
    check((await page.locator(`${S} [data-endpoint]`).textContent()).trim() === ENDPOINT, `${theme}: the endpoint id`);
    check(/^yes/.test((await page.locator(`${S} [data-confined]`).textContent()).trim()), `${theme}: sandboxed: yes`);
    check(/1 connected now · 2 tunnel-paired · 1 classic · 2 of 2 synced/.test(flat(await page.locator(`${S} [data-devices]`).textContent())), `${theme}: connected and synced devices`);
    await page.locator(`${S} [data-test]`).click();
    await waitSel(page, `${S} [data-test-result="ok"]`);
    check(/reachable via relay · 42 ms/.test(await page.locator(`${S} [data-test-result]`).textContent()), `${theme}: Test → reachable via relay · 42 ms`);
    await snap(page, `apptunnel-n0-${theme}`);

    // 3. a custom relay and its tokens
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

    // 4. refuse outside the tunnel
    const lock = flat(await page.locator(`${S} [data-lockout]`).textContent());
    check(/1 device isn't tunnel-paired/.test(lock), `${theme}: the warning counts classic devices (${lock})`);
    answer = false;
    await page.locator(`${S} [data-tunnel-only]`).click();
    await settle(page);
    check(dialogs.length === 1 && !(await page.locator(`${S} [data-tunnel-only]`).isChecked()) && !f.puts.some((p) => 'tunnelOnly' in p),
      `${theme}: cancelling the confirm leaves it off, nothing saved`);
    answer = true;
    await page.locator(`${S} [data-tunnel-only]`).click();
    await waitSel(page, `${S} [data-tunnel-only]:checked`);
    check(JSON.stringify(f.puts.at(-1)) === '{"tunnelOnly":true}', `${theme}: confirmed → PUT {tunnelOnly:true}`);
    check(/locked out until paired again/.test(await page.locator(`${S} [data-lockout]`).textContent()), `${theme}: …the warning says they are locked out`);
    await snap(page, `apptunnel-only-${theme}`);

    // 5. failed, unsandboxed; the default limit; no tunnels at all
    Object.assign(f.v, { state: 'failed', confined: false, restarts: 4, error: 'xbin-iroh exited: relay https://relay.example.com refused the token' });
    await gotoTab(page, 'nativeapp', 'Iroh tunneling');
    await waitSel(page, `${S} [data-state="failed"]`);
    check(/refused the token/.test(await page.locator(`${S} [data-state-error]`).textContent()), `${theme}: failed, with xbind's error`);
    check(/^no/.test((await page.locator(`${S} [data-confined]`).textContent()).trim()), `${theme}: sandboxed: no`);
    await page.locator(`${S} [data-limit-default]`).fill('3');
    await page.locator(`${S} [data-device-limit] button[type=submit]`).click();
    await until(() => f.limits.length > 0, 'the default limit saved');
    check(JSON.stringify(f.limits.at(-1)) === '{"default":3}', `${theme}: the default device limit → PUT /devices/limits {default:3} (${JSON.stringify(f.limits.at(-1))})`);
    await snap(page, `apptunnel-failed-${theme}`);
    f.absent = true;
    await gotoTab(page, 'nativeapp', 'Native tile UIs');
    await until(async () => f.absentSeen >= 2, 'the section asked and heard 404');
    await settle(page);
    check(await page.locator(`${S} .card`).count() === 0 && await page.locator('bx-admin-nativeapp [data-native-runtime]').count() === 1,
      `${theme}: an xbind without app tunnels (404, 405): nothing of them, the native card as before`);
    f.absent = false;

    // 6. users → dev1 → devices… → their own limit
    await gotoTab(page, 'users', 'add user');
    const row = page.locator('tr', { has: page.locator('td.user .mono', { hasText: /^dev1$/ }) }).first();
    await row.locator('button', { hasText: /^devices…$/ }).click();
    await waitSel(page, '[data-device-limit="dev1"]');
    check(/0 of 1 — the workspace default/.test(flat(await page.locator('[data-device-limit="dev1"]').textContent())), `${theme}: dev1 has no devices, the default limit, the button shown anyway`);
    await page.locator('[data-device-limit="dev1"] input[name=n]').fill('2');
    await page.locator('[data-device-limit="dev1"] button[type=submit]').click();
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).waitFor();
    check(JSON.stringify(f.patches.at(-1)) === '{"maxDevices":2}', `${theme}: set → PATCH /users/dev1 {maxDevices:2}`);
    check(/0 of 2 — their own/.test(flat(await page.locator('[data-device-limit="dev1"]').textContent())), `${theme}: …their own limit now`);
    await snap(page, `apptunnel-userlimit-${theme}`, { fullPage: false });
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).click();
    await page.locator('[data-device-limit="dev1"] button', { hasText: 'use the default' }).waitFor({ state: 'detached' });
    check(JSON.stringify(f.patches.at(-1)) === '{"maxDevices":null}', `${theme}: use the default → {maxDevices:null}`);

    // 7. a phone
    Object.assign(f.v, { state: 'running', confined: true, restarts: 0, error: '' });
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoTab(page, 'nativeapp', 'Iroh tunneling');
    await waitSel(page, `${S} [data-state="running"]`);
    const over = await page.evaluate(() => document.scrollingElement.scrollWidth - document.scrollingElement.clientWidth);
    check(over <= 0, `${theme}: no sideways scroll on a phone (${over}px over)`);
    await snap(page, `apptunnel-phone-${theme}`);
    check(!errs.length, `${theme}: no page error (${errs.join(' | ')})`);
    await closeCtx(A.ctx, page);
  }
  done();
}

module.exports = { apptunnel };
