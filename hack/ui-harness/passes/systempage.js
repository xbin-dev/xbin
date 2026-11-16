// hack/ui-harness/passes/systempage.js — covers D227: /xbin/system, the
// xbind-served page of the system (sealed) components, against the real
// API (GET /api/xbin/sealed, PUT /access, POST /lifecycle).
//
// A default xbind has no sealed component, so the page's lists are driven
// only on a harness built with the fixture table:
//   HARNESS_GO_TAGS=sealedfixture hack/ui-harness/run.sh --keep systemPage
// (xbin/probe: one instance; xbin/people: partitioned — one shared
// instance on the harness, a development xbind without sandboxes). On any
// other harness the pass checks the page's empty and refused states and
// SKIPs the rest, said so.
//   1. the page is served as it is and only top-level: framed, it renders
//      nothing (its headers refuse the frame; with them stripped, the
//      element does);
//   2. an admin sees each component with its state, version and the
//      "System component · managed by xbin" line — never the word
//      "sealed"; nobody but admins has access at first — the pass makes it
//      so (the component's own access entries removed and, for the
//      workspace agent, the everyone entry), and puts back what it found
//      at the end, however it ends: it doesn't depend on what ran before;
//   3. Give access to a person: the entry shows, GET /access has it at
//      read; Remove takes it away; given to everyone (the workspace agent's
//      who: everyone, a workspace default) the card says "Everyone in this
//      workspace" and where it was set, never "nobody"; taken back, nobody;
//   4. Turn off: the badge says Off and POST /lifecycle recorded disabled;
//      Turn on: Ready again;
//   5. a person who isn't an admin gets "Only workspace admins manage
//      system components";
//   6. the tile listing the shell reads (GET /components) has no xbin/ row,
//      and the shell shows none (shots: systempage-<theme>,
//      systempage-phone-<theme>).
const { URL, login, closeCtx, settle, shot, shotEl, checker, THEME } = require('../lib');
const { snapshot, putBack, putBackChecked, caller } = require('./wsagentpage'); // the component's access, read and put back

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await new Promise((r) => setTimeout(r, 150));
  }
}

const api = (page) => (fn) => page.evaluate(fn);
const comps = (page) => page.evaluate(() => document.querySelector('bx-system-page')?.testApi().components() ?? null);
const text = (page) => page.evaluate(() => document.querySelector('bx-system-page')?.shadowRoot?.textContent ?? '');

async function systemPage(browser) {
  const { check, skip, done } = checker('system-page');
  const errors = [];
  const A = await login(browser, 'admin', 'admin');
  A.page.on('pageerror', (e) => errors.push(e.message));
  let D;
  let found = null;
  try {
    const sealed = await (await A.ctx.request.get(`${URL}/api/xbin/sealed`)).json();
    const paths = (sealed.components ?? []).map((c) => c.path);
    if (paths.length) {
      // the start state the checks assume: nobody given the component (the workspace agent: nor everyone, by the defaults)
      found = await snapshot(caller(A.ctx), paths[0]);
      const bad = await putBack(caller(A.ctx), { ...found, entries: [], ...(found.agent ? { everyone: false } : {}) });
      if (bad.length) throw new Error(`${paths[0]}: taking away the access entries found (${JSON.stringify(found.entries)}): ${bad.join(' | ')}`);
    }

    // 1. served as it is, top-level only
    const resp = await A.page.goto(`${URL}/xbin/system`);
    const h = resp.headers();
    check(resp.status() === 200 && h['x-frame-options'] === 'DENY' && /frame-ancestors 'none'/.test(h['content-security-policy'] ?? ''),
      `/xbin/system is served with its framing headers (${resp.status()}, ${h['x-frame-options']})`);
    check((await (await A.ctx.request.get(`${URL}/vendor/system.html`)).status()) === 404, 'it has no copy under /vendor/');
    await until(async () => (await comps(A.page)) !== null && !(await text(A.page)).includes('Loading'), 'the page loaded');

    if (paths.length === 0) {
      check((await text(A.page)).includes('This xbin release has no system components'), 'an xbind without system components says so');
      skip('this xbind has no sealed component (a default build): the lists need HARNESS_GO_TAGS=sealedfixture');
    } else {
      // 2. the list
      const list = await comps(A.page);
      check(JSON.stringify(list.map((c) => c.path).sort()) === JSON.stringify([...paths].sort()), `the page lists ${paths.join(', ')} (${JSON.stringify(list)})`);
      const body = await text(A.page);
      check(body.includes('System component · managed by xbin') && !/sealed/i.test(body), 'each is called a system component; the page never says "sealed"');
      check(body.includes(`xbin ${sealed.release}`) && body.includes(sealed.components[0].digest), 'it shows the release and the code digest');
      // one instance: runnable. One per person: only with sandboxes — the
      // harness has users, so without HARNESS_ISOLATE it is held, and the
      // page says why (one shared instance is --no-auth's alone)
      const runnable = (c) => ['Ready', 'Running', 'Starting'].includes(c.state);
      const held = list.filter((c) => !runnable(c));
      // the release's own table (the workspace agent, one per person) has no
      // one-instance component: on a harness without sandboxes its first is
      // held too, and the parts that call it SKIP
      const first = list.find((c) => c.path === paths[0]);
      const firstRuns = !!first && runnable(first);
      check(held.every((c) => c.hold === 'needs-sandboxes') && (firstRuns || first?.hold === 'needs-sandboxes') && (held.length === 0 || body.includes('--isolate')),
        `each is runnable, or held for needing sandboxes with the reason shown (${JSON.stringify(list)})`);
      const P = paths[0];
      const card = A.page.locator(`bx-system-page section[data-path="${P}"]`);
      check((await card.innerText()).includes('Nobody but workspace admins'), `${P}: nobody but admins has access at first`);
      await shot(A.page, `systempage-${THEME}`);

      // 3. access: read, given and removed
      await card.locator('select[name=who]').selectOption('user:dev1');
      await card.locator('button[type=submit]', { hasText: 'Give access' }).click();
      await until(async () => (await card.innerText()).includes('dev1') && !(await card.innerText()).includes('Nobody but workspace admins'), 'dev1 shows in the access list');
      let acc = await (await A.ctx.request.get(`${URL}/api/xbin/access?tile=${encodeURIComponent(P)}`)).json();
      check(acc.entries.some((e) => e.kind === 'user' && e.id === 'dev1' && e.level === 'read'), `GET /access has dev1 at read (${JSON.stringify(acc.entries)})`);
      const more = await A.ctx.request.put(`${URL}/api/xbin/access`, { data: { tile: P, kind: 'user', id: 'dev1', level: 'terminal' } });
      check(more.status() === 400 && /system component/.test(await more.text()), `a level above read is refused in the page's words (${more.status()})`);

      // 5. someone who isn't an admin
      D = await login(browser, 'dev1', 'devpass123');
      D.page.on('pageerror', (e) => errors.push(`dev1: ${e.message}`));
      await D.page.goto(`${URL}/xbin/system`);
      await until(async () => (await text(D.page)).includes('Only workspace admins manage system components'), 'dev1 is told the page is the admins\'');
      check((await comps(D.page)).length === 0, 'and sees no component');
      // …but, with read access, opens its page like a tile's
      const own = await D.ctx.request.get(`${URL}/c/${P}/`);
      check(own.status() === 200, `dev1, given access, loads its page (${own.status()})`);

      await card.locator('li', { hasText: 'dev1' }).locator('button.rm', { hasText: 'Remove' }).click();
      await until(async () => (await card.innerText()).includes('Nobody but workspace admins'), 'the entry is removed');
      acc = await (await A.ctx.request.get(`${URL}/api/xbin/access?tile=${encodeURIComponent(P)}`)).json();
      check(!acc.entries.some((e) => e.id === 'dev1'), 'GET /access no longer has dev1');
      if (found.agent) {
        // everyone given it by the workspace defaults (the agent's who: everyone): the card says so, never "nobody"
        const who = async (w) => (await A.ctx.request.put(`${URL}/api/xbin/delegate/workspace`, { data: { who: w } })).status();
        check((await who('everyone')) === 200, `${P}: PUT /delegate/workspace {who: "everyone"}`);
        acc = await (await A.ctx.request.get(`${URL}/api/xbin/access?tile=${encodeURIComponent(P)}`)).json();
        check(JSON.stringify(acc.defaults) === '[{"level":"read","source":"exact"}]', `GET /access names the workspace default that gives it everyone (${JSON.stringify(acc.defaults)})`);
        await A.page.evaluate(() => document.querySelector('bx-system-page').testApi().reload());
        await until(async () => (await card.innerText()).includes('Everyone in this workspace'), 'the card says everyone');
        const said = await card.innerText();
        check(!said.includes('Nobody but workspace admins') && said.includes('Workspace agents: who gets one'), `${P}: given to everyone, the card says so and where it was set, never "nobody" (${said.replace(/\s+/g, ' ').slice(0, 160)})`);
        check((await who('chosen')) === 200, `${P}: PUT /delegate/workspace {who: "chosen"} takes it back`);
        await A.page.evaluate(() => document.querySelector('bx-system-page').testApi().reload());
        await until(async () => !(await card.innerText()).includes('Everyone in this workspace'), 'the everyone line goes');
        check((await card.innerText()).includes('Nobody but workspace admins'), `${P}: taken back, nobody but admins again`);
        // the web and MCP tools (D236): off until an admin sets them here; the modes not built yet can't be picked
        const pol = card.locator('bx-wsagent-policy');
        await until(async () => (await pol.locator('[data-save]').count()) === 1, 'the web and MCP settings');
        const offFirst = await pol.locator('input[name=web][value=off]').isChecked() && await pol.locator('input[name=mcp][value=off]').isChecked();
        const notYet = await pol.locator('input[name=web][value=full]').isDisabled() && await pol.locator('input[name=mcp][value=listed]').isDisabled();
        check(offFirst && notYet, `${P}: the web and MCP read off; "send to the web" and listed servers can't be picked yet`);
        await pol.locator('input[name=web][value=read]').check();
        await pol.locator('textarea[data-field=deny]').fill('intranet.example.com\nbank.test');
        await pol.locator('input[name=mcp][value=tiles]').check();
        const [put] = await Promise.all([A.page.waitForRequest((r) => r.method() === 'PUT' && r.url().endsWith('/api/xbin/delegate/workspace')), pol.locator('[data-save]').click()]);
        await until(async () => (await pol.locator('[role=status]').count()) === 1, 'saved');
        const ws = await (await A.ctx.request.get(`${URL}/api/xbin/delegate/workspace`)).json();
        check(JSON.parse(put.postData()).web.mode === 'read' && ws.web?.mode === 'read' && JSON.stringify(ws.web?.deny) === '["bank.test","intranet.example.com"]' && ws.mcp?.mode === 'tiles',
          `${P}: Save sets the policy (${put.postData()} → ${JSON.stringify({ web: ws.web, mcp: ws.mcp })})`);
        await shotEl(A.page, 'bx-system-page bx-wsagent-policy', `systempage-netpolicy-${THEME}`);
        const back = await A.ctx.request.put(`${URL}/api/xbin/delegate/workspace`, { data: { web: { mode: 'off', allow: [], deny: [] }, mcp: { mode: 'off' } } });
        check(back.status() === 200, `${P}: put back off (${back.status()})`);
      } else skip(`${P} isn't the workspace agent here: the everyone line (its who: everyone) is the release table's to drive`);

      // 4. off and on
      await card.locator('button', { hasText: 'Turn off' }).click();
      await until(async () => (await comps(A.page)).find((c) => c.path === P)?.state === 'Off', `${P} reads Off`);
      const off = await (await A.ctx.request.get(`${URL}/api/xbin/sealed`)).json();
      check(off.components.find((c) => c.path === P).state === 'disabled', 'POST /lifecycle recorded disabled');
      await card.locator('button', { hasText: 'Turn on' }).click();
      await until(async () => (await comps(A.page)).find((c) => c.path === P)?.state !== 'Off', `${P} is on again`);

      await A.page.setViewportSize({ width: 390, height: 780 });
      await settle(A.page);
      const wide = await A.page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
      check(!wide, 'on a phone the page doesn\'t scroll sideways');
      await shot(A.page, `systempage-phone-${THEME}`);
      await A.page.setViewportSize({ width: 1400, height: 900 });

      // 6. the tile listing and the shell never show one
      const listed = await (await A.ctx.request.get(`${URL}/api/xbin/components`)).json();
      check(!listed.some((c) => c.path.startsWith('xbin/')), 'GET /components (what a shell reads) has no xbin/ row');
      const asked = await (await A.ctx.request.get(`${URL}/api/xbin/components?sealed=1`)).json();
      check(paths.every((p) => asked.some((c) => c.path === p && c.sealed === true)), '…and ?sealed=1 has them, marked sealed');
      await A.page.goto(`${URL}/`);
      await A.page.waitForSelector('bx-shell');
      await settle(A.page);
      const inShell = await A.page.evaluate((ps) => {
        const seen = (root) => [...root.querySelectorAll('*')].some((el) => (el.shadowRoot && seen(el.shadowRoot)) ||
          [...el.childNodes].some((n) => n.nodeType === 3 && ps.some((p) => n.textContent.includes(p))));
        return seen(document);
      }, paths);
      check(!inShell, 'the shell shows no system component among its tiles');

      // 7. a page left open across a release: its calls name its code, and
      // the answer a page of other code gets (409 + X-XBin-Reload, as after
      // an xbind restart onto another release) reloads it
      await A.page.goto(`${URL}/c/${P}/`);
      await A.page.waitForFunction(() => !!window.xbin);
      const code = await A.page.evaluate(() => document.querySelector('meta[name="xbin-code"]')?.content ?? '');
      check(/^[0-9a-f]{24}$/.test(code), `its document names its code (${code})`);
      let sent = null;
      // a route of the component's own: the fixtures answer /who; the workspace agent (the release's table) has a
      // closed route list, and /ws/state is on it (its first call compiles and starts the admin's backend: no timeout)
      const ownRoute = `/api/${P}/${P === 'xbin/wsagent' ? 'ws/state' : 'who'}`;
      if (!firstRuns) {
        skip(`${P} is held here (it needs sandboxes: HARNESS_ISOLATE=1 runs it): its page's calls and the reload across a release`);
      } else {
      const who = await A.page.evaluate(async (url) => {
        const r = await window.xbin.fetch(url);
        return r.status;
      }, ownRoute);
      check(who === 200, `a call from its own page passes (${who})`);
      await A.page.route(`**${ownRoute}`, (route) => {
        if (sent !== null) return route.continue(); // once: the reloaded page may ask this route itself (the agent's does)
        sent = route.request().headers()['x-xbin-code'] ?? '';
        return route.fulfill({ status: 409, headers: { 'X-XBin-Reload': 'f'.repeat(24), 'Access-Control-Expose-Headers': 'X-XBin-Reload' }, body: '{}' });
      });
      await A.page.evaluate(() => { window.__before = true; });
      const nav = A.page.waitForNavigation({ timeout: 10000 }).then(() => true, () => false);
      await A.page.evaluate((url) => { window.xbin.fetch(url); }, ownRoute);
      const reloaded = await nav;
      check(sent === code, `xbin.fetch names the page's code (${sent})`);
      check(reloaded && !(await A.page.evaluate(() => window.__before === true)), 'told its code is another release\'s, the page reloads');
      await A.page.unroute(`**${ownRoute}`);
      }
    }
    check(errors.length === 0, `no page error (${errors.join(' | ')})`);
  } finally {
    if (found) await putBackChecked(browser, found, check);
    if (D) await closeCtx(D.ctx, D.page).catch(() => {});
    await closeCtx(A.ctx, A.page).catch(() => {});
  }
  done();
}

module.exports = { systemPage };
