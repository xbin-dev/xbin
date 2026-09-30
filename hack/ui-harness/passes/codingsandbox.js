// hack/ui-harness/passes/codingsandbox.js — the builtin sandbox manager
// (D122: builtin-templates/coding-sandbox) end to end on its test backend:
// the seeded apps/coding-sandbox copy carries the template's `fake` backend
// (seed.sh: every sandbox a host directory under its `boxes` resource). It
// pins:
//   - on the default `xbin` backend a harness xbind (no --isolate) says why
//     no sandbox runs, on the operators' page (the substrate's error);
//   - an operator switches it to the fake (PUT /ops/config), and the page
//     shows the offer (exec files tar tty snapshots clone; VMs);
//   - bound to the agent's `sandboxes` slot beside apps/fakesbx, the agent
//     makes a sandbox there for admin (consumer apps/agent, asserted);
//   - "Yours": admin makes one on the page itself, browses its workdir,
//     opens a terminal (<bx-terminal src> on the tile's own tty route) whose
//     shell answers;
//   - the operators' table lists both consumers' sandboxes; a snapshot is
//     taken; Images and Settings draw;
//   - dev1 (read access to the tile) gets the read-only view: the page says
//     why, New sandbox is disabled, and a create from the page is refused
//     (403 not-allowed: it needs write access) — xbind's own D29 headers;
//   - the sandboxes are deleted and the agent's binding restored.
// Screenshots: coding-sandbox-{xbin,ops,yours,images,settings,reader}.png.
const { URL, login, log, shot, checker } = require('../lib');

const SELF = 'apps/coding-sandbox';
const until = (page, fn, arg, timeout = 30000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 100 });
const tileApi = (page, tile, p, opt) => page.evaluate(async ([tile, p, opt]) => {
  const r = await xbin.fetch(`/api/${tile}${p}`, opt || {});
  let body = null;
  try { body = await r.json(); } catch { /* none */ }
  return { status: r.status, body };
}, [tile, p, opt]);
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

async function openPage(page) {
  await page.goto(`${URL}/c/${SELF}/`);
  // the backend may still be building: the page says it can't reach it until it answers
  for (let i = 0; i < 90; i++) {
    const ok = await page.waitForSelector('#tab-ops', { timeout: 2000 }).then(() => true, () => false);
    if (ok) return;
    await page.reload();
  }
  throw new Error('the coding-sandbox page never showed the operators\' tabs');
}

async function codingSandbox(browser) {
  const { check, done } = checker('coding-sandbox');
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1300, height: 950 } });
  const admin = (method, path, body) => ctx.request.fetch(`${URL}/api/xbin${path}`, { method, data: body });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await openPage(page);

  // the default backend: xbind's runtime, which a harness xbind can't give (no --isolate)
  let st = (await tileApi(page, SELF, '/ops/state')).body;
  if (st.backend.name === 'xbin') {
    await page.waitForSelector('.substrate-err', { timeout: 15000 }).catch(() => {});
    const why = await page.$$eval('.substrate-err', (e) => e.map((x) => x.textContent).join(' '));
    check(/isolation|cap:sandboxes|not-allowed|unavailable/.test(why), `the xbin backend says why nothing runs here (${why.slice(0, 160)})`);
    await shot(page, 'coding-sandbox-xbin');
    const put = await tileApi(page, SELF, '/ops/config', json('PUT', { backend: 'fake', backendConfig: { root: 'res:boxes' } }));
    check(put.status === 200 && put.body.backend.name === 'fake' && !put.body.backend.error, `switched to the fake backend (${put.status} ${JSON.stringify(put.body.backend || put.body)})`);
    st = put.body;
  }
  check((st.offer?.caps || []).join(' ') === 'exec files tar tty snapshots clone stdio', `the fake's offer (with A4's stdio) (${(st.offer?.caps || []).join(' ')})`);

  // the agent binds it beside apps/fakesbx, and makes a sandbox there
  let r = await admin('POST', '/bindings', { component: 'apps/agent', slot: 'sandboxes', providers: ['apps/fakesbx', SELF] });
  check(r.ok(), `bound the agent to both managers (${r.status()})`);
  const agent = await ctx.newPage();
  await agent.goto(`${URL}/c/apps/agent/`);
  await agent.waitForSelector('#msg', { timeout: 60000 });
  let made = null;
  for (let i = 0; i < 60 && !made; i++) { // the agent restarts for the binding
    const a = await tileApi(agent, 'apps/agent', '/sandboxes', json('POST', { name: 'agent-box', provider: SELF }));
    if (a.status === 201) made = a.body; else await agent.waitForTimeout(1000);
  }
  check(made && made.provider === SELF && made.state === 'running', `the agent made agent-box at coding-sandbox (${JSON.stringify(made && { provider: made.provider, state: made.state })})`);
  await agent.close();

  // Yours: a sandbox of the page's own, its files, a terminal
  await page.reload();
  await page.waitForSelector('#tab-mine');
  await page.click('#tab-mine');
  await page.click('#new');
  await page.fill('#cf-name', 'my-box');
  await page.click('#cf-create');
  await page.waitForSelector('#detail #entries, #detail .muted', { timeout: 60000 });
  await until(page, () => !!document.querySelector('#files'));
  check(!!(await page.$('#crumbs')), 'the file browser opened on its workdir');
  await page.click('#sub-term');
  await page.waitForSelector('#term bx-terminal');
  const src = await page.$eval('#term bx-terminal', (e) => e.getAttribute('src'));
  check(/^\/api\/apps\/coding-sandbox\/sbx\/sandboxes\/sb-[0-9a-f]+\/tty\?cwd=/.test(src), `the terminal dials the tile's own route (${src})`);
  const open = await until(page, () => document.querySelector('#term bx-terminal')?.testApi?.().open, null, 20000).then(() => true, () => false);
  check(open, 'the terminal is open (the manager started a shell and relays it)');
  const text = () => page.$eval('#term bx-terminal', (t) => t.testApi().text()).catch(() => '');
  await until(page, () => /[$#] ?$/m.test(document.querySelector('#term bx-terminal')?.testApi?.().text() || ''), null, 15000).catch(() => {});
  await page.click('#term bx-terminal');
  await page.keyboard.type('echo harness-$((6*7))');
  await page.keyboard.press('Enter');
  const answered = await until(page, () => /^harness-42\s*$/m.test(document.querySelector('#term bx-terminal')?.testApi?.().text() || ''), null, 15000)
    .then(() => true, () => false);
  check(answered, `the shell answered (${(await text()).slice(-120).replace(/\s+/g, ' ')})`);
  await shot(page, 'coding-sandbox-yours');
  await page.click('#term-end');

  // the operators' table: both consumers
  await page.click('#tab-ops');
  await page.waitForSelector('#ops-table');
  const rows = await page.$$eval('#ops-table tr.sb', (t) => t.map((x) => x.textContent.replace(/\s+/g, ' ')));
  check(rows.some((x) => x.includes('agent-box') && x.includes('apps/agent') && x.includes('admin (asserted)')), 'the agent\'s sandbox, its consumer and asserted owner');
  check(rows.some((x) => x.includes('my-box') && x.includes(SELF)), 'the page\'s own sandbox');
  const agentRow = page.locator('#ops-table tr.sb', { hasText: 'agent-box' });
  await agentRow.locator('button[data-act="snapshots"]').click();
  await page.waitForSelector('#snaps');
  await page.fill('#snap-name', 'harness');
  await page.click('#snap-take');
  const snapped = await page.waitForSelector('#snaps tr[data-snap]', { timeout: 20000 }).then(() => true, () => false);
  check(snapped, 'an operator took a snapshot of the agent\'s sandbox');
  await shot(page, 'coding-sandbox-ops');
  await page.click('#tab-images');
  await page.waitForSelector('#images');
  await shot(page, 'coding-sandbox-images');
  await page.click('#tab-settings');
  await page.waitForSelector('#mode-now');
  check((await page.textContent('#mode-now')).includes('VMs'), 'automatic mode: VMs where the substrate offers them');
  await shot(page, 'coding-sandbox-settings');

  // a person with read access looks, and changes nothing (D122 addendum)
  {
    const { ctx: rctx, page: rpage } = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1300, height: 950 } });
    await rpage.goto(`${URL}/c/${SELF}/`);
    const shown = await rpage.waitForSelector('#readonly-note', { timeout: 30000 }).then(() => true, () => false);
    check(shown, 'a reader gets the read-only view, saying why');
    const me = (await tileApi(rpage, SELF, '/me')).body || {};
    check(me.write === false && me.level === 'read' && me.operator === false, `/me for a reader (${JSON.stringify(me)})`);
    check(await rpage.$eval('#new', (b) => b.disabled).catch(() => false), 'New sandbox is disabled for a reader');
    const made = await tileApi(rpage, SELF, '/sbx/sandboxes', json('POST', { name: 'nope' }));
    check(made.status === 403 && made.body?.refusal === 'not-allowed' && /write access/.test(made.body?.error || ''),
      `a reader's create from the page is refused (${made.status} ${JSON.stringify(made.body)})`);
    const list = await tileApi(rpage, SELF, '/sbx/sandboxes');
    check(list.status === 200, `a reader lists (${list.status})`);
    await shot(rpage, 'coding-sandbox-reader');
    await rctx.close();
  }

  // clean up: the sandboxes, then the agent's binding as the seed left it
  st = (await tileApi(page, SELF, '/ops/state')).body;
  for (const s of st.sandboxes || []) {
    const d = await tileApi(page, SELF, `/ops/sandboxes/${s.id}`, { method: 'DELETE' });
    check(d.status === 204, `deleted ${s.name} (${d.status})`);
  }
  r = await admin('POST', '/bindings', { component: 'apps/agent', slot: 'sandboxes', providers: ['apps/fakesbx'] });
  check(r.ok(), `the agent's binding restored (${r.status()})`);
  check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  if (errors.length) log('coding-sandbox page errors:', errors);
  await ctx.close();
  done();
}

module.exports = { codingSandbox };
