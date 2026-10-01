// hack/ui-harness/passes/tilepages.js — builtin tiles whose page logic moved
// into a module the page shares with the tile's native view (native.js,
// plans/native.md §18): chat (chat-core.js) and prometheus-viewer (prom.js),
// on the real xbind — the page's module graph loads in its sandboxed frame
// document and the page works end to end (chat: a turn through llm-gw to
// hack/fakeopenai, thinking folded; the viewer: llm-gw's own /metrics). Then
// each installed tile's native.js boots in that same document through the
// xb-native runtime (a preview host collects the messages): it must mount a
// tree and report no error. Tiles are imported here, not in seed.sh.
const { URL, login, sleep, shot, checker } = require('../lib');

const until = (page, fn, arg, timeout = 30000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 100 });

// nativeBoot: boot the tile's native.js in its own page document; returns the
// runtime's messages once a mount arrived (or the wait ran out).
async function nativeBoot(page) {
  await page.evaluate(async () => {
    const xb = await import('/vendor/xb-native.js');
    await xb.boot('./native.js');
  });
  await page.waitForFunction(() => (window.__xbn || []).some((m) => m.op === 'mount' || m.op === 'error'), null, { timeout: 15000, polling: 100 }).catch(() => {});
  await sleep(1500); // the first data-driven renders
  return page.evaluate(() => (window.__xbn || []).map((m) => JSON.parse(JSON.stringify(m))));
}

async function tilePages(browser) {
  const { check, done } = checker('tile-pages');
  const { ctx } = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  for (const name of ['chat', 'prometheus-viewer']) {
    const r = await api('POST', '/builtins/import', { name });
    check(r.ok() || r.status() === 409, `import builtin ${name} (${r.status()})`);
  }
  // Bind only what is missing: a (re)binding restarts the provider (llm-gw,
  // twice), which cuts a turn streaming through it — so settle after one.
  const bound = (await (await api('GET', '/bindings')).json()).bindings || {};
  let changed = false;
  for (const [component, slot] of [['apps/chat', 'llm'], ['apps/prometheus-viewer', 'sources']]) {
    if (JSON.stringify(bound[component]?.[slot] ?? null).includes('apps/llm-gw')) continue;
    const r = await api('POST', '/bindings', { component, slot, providers: ['apps/llm-gw'] });
    check(r.ok(), `bind ${component} ${slot} → apps/llm-gw (${r.status()} ${r.ok() ? '' : await r.text()})`);
    changed = true;
  }
  if (changed) await sleep(4000);

  const openPage = async (tile) => {
    const page = await ctx.newPage();
    const errors = [], modules = [];
    page.on('pageerror', (e) => errors.push(e.message));
    // by path: under TILE_ASSETS=origins the page is on the tile's own origin,
    // under tokens a relative module loads from /c/~<asset-token>/…
    page.on('response', (r) => {
      const path = new globalThis.URL(r.url()).pathname.replace(/^\/c\/~[^/]+\//, '/c/');
      if (/^\/c\/apps\/[^/]+\/[\w-]+\.js$/.test(path)) modules.push(`${r.status()} ${path}`);
    });
    await page.addInitScript(() => { window.__xbn = []; window.xbnHost = { post: (m) => window.__xbn.push(m) }; });
    await page.goto(`${URL}/c/${tile}/`);
    return { page, errors, modules };
  };

  // ---- chat: models from llm-gw, a turn with streamed thinking ----
  {
    const { page, errors, modules } = await openPage('apps/chat');
    const models = await until(page, () => {
      const m = [...document.querySelectorAll('#model option')].map((o) => o.textContent).filter((t) => t.startsWith('fake/'));
      return m.length ? m : null;
    }).then((h) => h.jsonValue()).catch(() => []);
    check(models.includes('fake/fake-chat'), `chat: the model picker lists llm-gw's models (${models.join(', ')})`);
    await page.selectOption('#model', { label: 'fake/fake-chat' });
    await page.fill('#input', 'hello there');
    await page.press('#input', 'Enter');
    const answered = await until(page, () => [...document.querySelectorAll('.msg.assistant .md')].some((e) => e.textContent.includes('Hello from the fake model.'))
      && document.getElementById('send').textContent === 'Send').then(() => true).catch(() => false);
    check(answered, 'chat: the turn streams the fake model\'s answer and ends (Send is back)');
    const think = await page.evaluate(() => {
      const d = [...document.querySelectorAll('.msg.assistant details.think')].find((x) => !x.hidden);
      return d ? { open: d.open, text: d.querySelector('pre').textContent, summary: d.querySelector('summary').textContent } : null;
    });
    check(!!think && !think.open && think.text.includes('Considering') && think.summary === 'Thinking', `chat: thinking shown, then folded once the answer arrived (${JSON.stringify(think)})`);
    check(modules.some((m) => m.startsWith('200 ') && m.endsWith('/c/apps/chat/chat-core.js')), `chat: the page loaded ./chat-core.js (${modules.join(', ')})`);
    await shot(page, 'tile-chat-page');
    const msgs = await nativeBoot(page);
    const mount = msgs.find((m) => m.op === 'mount');
    const errs = msgs.filter((m) => m.op === 'error' || (m.op === 'diag' && m.level !== 'info'));
    check(mount?.root?.t === 'screen' && mount.root.p?.title === 'Chat', `chat: native.js mounts its screen (${mount ? mount.root.t : 'no mount'})`);
    check(errs.length === 0, `chat: native.js reports no error (${JSON.stringify(errs).slice(0, 300)})`);
    check(errors.length === 0, `chat: no page errors (${errors.join(' | ')})`);
    await page.close();
  }

  // ---- prometheus-viewer: llm-gw's /metrics, parsed by prom.js ----
  {
    const { page, errors, modules } = await openPage('apps/prometheus-viewer');
    const seen = await until(page, () => {
      const r = document.querySelector('bx-prometheus-viewer')?.shadowRoot;
      return r && r.textContent.includes('llmgw_requests_total') && r.textContent.includes('{backend="fake"}');
    }).then(() => true).catch(() => false);
    check(seen, 'prometheus-viewer: llm-gw\'s series are scraped and listed');
    check(modules.some((m) => m.startsWith('200 ') && m.endsWith('/c/apps/prometheus-viewer/prom.js')), `prometheus-viewer: the page loaded ./prom.js (${modules.join(', ')})`);
    await shot(page, 'tile-prometheus-page');
    const msgs = await nativeBoot(page);
    const trees = msgs.filter((m) => m.op === 'mount' || m.op === 'patch');
    const text = JSON.stringify(trees);
    check(trees[0]?.op === 'mount' && trees[0].root.p?.title === 'Prometheus', 'prometheus-viewer: native.js mounts its screen');
    check(text.includes('llmgw_requests_total') && text.includes('"kind":"spark"'), 'prometheus-viewer: native.js renders the scraped series with sparklines');
    check(!msgs.some((m) => m.op === 'error' || (m.op === 'diag' && m.level !== 'info')), 'prometheus-viewer: native.js reports no error');
    check(errors.length === 0, `prometheus-viewer: no page errors (${errors.join(' | ')})`);
    await page.close();
  }

  // ---- webhooks (seed.sh): its native.js on the real backend ----
  {
    const { page, errors } = await openPage('apps/webhooks');
    await until(page, () => document.querySelector('#app h4')).catch(() => {});
    const msgs = await nativeBoot(page);
    const last = msgs.filter((m) => m.op === 'mount' || m.op === 'patch');
    check(last.length > 0 && JSON.stringify(last).includes('"title":"Wiring"'), 'webhooks: native.js renders the hooks screen from GET /hooks');
    check(!msgs.some((m) => m.op === 'error' || (m.op === 'diag' && m.level !== 'info')), 'webhooks: native.js reports no error');
    check(errors.length === 0, `webhooks: no page errors (${errors.join(' | ')})`);
    await page.close();
  }

  // ---- llm-gw: usage by partition (B3) ----
  // The harness runs no partitioned caller, so the rows are seeded into
  // llm-gw's kv and its backend restarted to read them. Signed in, the admin
  // reads as a manager of the tile (a tile can't tell an admin): the
  // people's partitions together and the global instance, never a person's
  // row, and the fairness limit's form, which saves. dev1, given read on
  // it for this block, sees their own row only, and no form.
  {
    const now = Date.now();
    const rows = [
      { from: 'apps/agent', partition: 'global', reqs: 12, tokIn: 48210, tokOut: 3120, cost: 0.41, last: now - 90e3 },
      { from: 'apps/agent', partition: 'user:dev1', partitionId: 'u-0123456789abcdef0123456789abcdef', reqs: 7, tokIn: 20488, tokOut: 1811, cost: 0, last: now - 3 * 3600e3 },
      { from: 'apps/agent', partition: 'user:sales1', partitionId: 'u-fedcba9876543210fedcba9876543210', reqs: 5, tokIn: 10000, tokOut: 900, cost: 0.2, last: now - 600e3 },
    ];
    const kv = (method, data) => ctx.request.fetch(`${URL}/api/xbin/kv/res:apps/llm-gw/state/callers`, { method, data });
    const restart = async () => {
      const b = (await (await api('GET', '/tile-status?component=apps/llm-gw')).json().catch(() => ({}))).backend || {};
      if (b.pid) { try { process.kill(b.pid, 'SIGKILL'); } catch { /* gone already */ } }
      await sleep(500);
    };
    const put = await kv('PUT', JSON.stringify(rows));
    check(put.ok(), `llm-gw: seed its callers (${put.status()})`);
    await restart();
    // section: the text of the section's two tables and whether the form shows
    const section = () => {
      const r = document.querySelector('bx-llm-gw')?.shadowRoot;
      const t = (sel) => (r?.querySelector(sel)?.textContent || '').replace(/\s+/g, ' ');
      return { rows: t('table[data-callers]'), totals: t('table[data-caller-totals]'), form: !!r?.querySelector('form input[name="limit"]') };
    };
    const { page, errors } = await openPage('apps/llm-gw');
    const seen = await until(page, () => {
      const r = document.querySelector('bx-llm-gw')?.shadowRoot;
      const tot = r?.querySelector('table[data-caller-totals]')?.textContent || '';
      return tot.includes('together') && (r?.querySelector('table[data-callers]')?.textContent || '').includes('global instance');
    }, null, 60000).then(() => page.evaluate(section)).catch(() => ({ rows: '', totals: '', form: false }));
    check(seen.totals.includes('2 together') && seen.totals.includes('30,488') && seen.rows.includes('48,210'),
      `llm-gw: a manager sees the people's partitions together and the global instance (${seen.totals.slice(0, 160)} | ${seen.rows.slice(0, 160)})`);
    check(!seen.rows.includes('dev1') && !seen.rows.includes('sales1') && !seen.totals.includes('dev1'),
      `llm-gw: a manager sees no person's row (${seen.rows.slice(0, 200)})`);
    check(seen.form, 'llm-gw: a manager is offered the fairness limit');
    await shot(page, 'tile-llmgw-partitions');
    const input = page.locator('bx-llm-gw form:has(input[name="limit"]) input[name="limit"]');
    await input.fill('2');
    await page.locator('bx-llm-gw form:has(input[name="limit"]) button').click();
    let limit = 0;
    for (let i = 0; i < 40 && limit !== 2; i++) {
      await sleep(250);
      limit = await page.evaluate(async () => (await (await xbin.fetch('/api/apps/llm-gw/config')).json()).partitionLimit || 0).catch(() => 0);
    }
    check(limit === 2, `llm-gw: the fairness limit saves from the page (${limit})`);
    await page.evaluate(() => xbin.fetch('/api/apps/llm-gw/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ partitionLimit: 0 }) }));
    check(errors.length === 0, `llm-gw: no page errors (${errors.join(' | ')})`);
    await page.close();

    // a person who reads the tile: their own row, nothing else
    const grant = (level) => api('PUT', '/access', { tile: 'apps/llm-gw', kind: 'user', id: 'dev1', level });
    check((await grant('read')).ok(), 'llm-gw: dev1 may read it');
    const dev = await login(browser, 'dev1', 'devpass123');
    const dpage = await dev.ctx.newPage();
    const derrors = [];
    dpage.on('pageerror', (e) => derrors.push(e.message));
    await dpage.goto(`${URL}/c/apps/llm-gw/`);
    const mine = await until(dpage, () => (document.querySelector('bx-llm-gw')?.shadowRoot?.querySelector('table[data-callers]')?.textContent || '').includes('dev1'),
      null, 60000).then(() => dpage.evaluate(section)).catch(() => ({ rows: '', totals: '', form: true }));
    check(mine.rows.includes("dev1's partition") && mine.rows.includes('20,488') && !mine.rows.includes('sales1') && !mine.rows.includes('global instance'),
      `llm-gw: dev1 sees their own row only (${mine.rows.slice(0, 200)})`);
    check(!mine.totals && !mine.form, `llm-gw: dev1 gets no totals and no limit form (${mine.totals.slice(0, 80)} form=${mine.form})`);
    await shot(dpage, 'tile-llmgw-partitions-reader');
    check(derrors.length === 0, `llm-gw: no page errors for dev1 (${derrors.join(' | ')})`);
    await dev.ctx.close();
    await grant('');

    await kv('DELETE');
    await restart(); // back to no rows: the page as in a workspace without partitioned tiles
  }

  await ctx.close();
  done();
}

module.exports = { tilePages };
