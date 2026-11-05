// mcp-direct.mjs — Settings → MCP's "Connected by URL" (mcp-direct.js,
// model/mcp-direct.js; API.md "Direct MCP servers") in a person's partition:
// someone who isn't a manager gets the ⚙ panel opened on MCP (no managers' tab), sees
// the global servers (read from the shared space) and their own, adds one of
// their own (at their own home), is handed the server's sign-in page as a
// real link with this tile's mcp-callback.html as the return address, sees
// it signed in once the callback finished elsewhere, and removes it; a
// manager may add one for everyone, which goes to the shared space.
//
//   node test/mcp-direct.mjs     (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const browser = await launch();

async function open(me) {
  const ctx = await browser.newContext({ viewport: { width: 760, height: 900 } });
  await serveTile(ctx);
  await ctx.addInitScript(STUB, { runs: [], views: {}, me });
  await ctx.addInitScript(() => {
    window.xbin.partition = 'user:' + (window.__seedUser || 'alice');
    const j = window.__json;
    const S = window.__mcp = {
      global: [{ id: 'mcpg1', name: 'docs', slug: 'docs', url: 'https://docs.example/mcp', scope: 'global', auth: 'none', state: 'ready', tools: ['search'], canEdit: false, signedIn: false }],
      mine: [],
    };
    window.__route('GET', /\/mcp\/servers$/, (m, o) => (o.partition === 'global'
      ? j({ servers: S.global.map((s) => ({ ...s, canEdit: S.manager })), personal: false, global: true, canGlobal: !!S.manager })
      : j({ servers: S.mine, personal: true, global: false, canGlobal: false })));
    window.__route('POST', /\/mcp\/servers$/, (m, o) => {
      const b = JSON.parse(o.body);
      const s = { id: 'mcp' + (S.mine.length + S.global.length + 1), slug: b.name || 'travel', url: b.url, scope: b.scope, auth: 'oauth', state: 'signin', tools: [], canEdit: true, signedIn: false };
      s.name = s.scope === 'personal' ? 'my-' + s.slug : s.slug;
      (o.partition === 'global' ? S.global : S.mine).push(s);
      return j({ server: s }, 201);
    });
    window.__route('POST', /\/mcp\/servers\/([^/]+)\/signin$/, () => j({ url: 'https://auth.example/oauth/authorize?client_id=c&state=p123' }));
    window.__route('DELETE', /\/mcp\/servers\/([^/]+)$/, (m) => { S.mine = S.mine.filter((s) => s.id !== m[1]); return new Response(null, { status: 204 }); });
    window.__confirms = [];
    window.confirm = (msg) => { window.__confirms.push(msg); return true; };
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${ORIGIN}/`);
  return { page, errors, ctx };
}
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url))
  .map((c) => ({ url: c.url, home: c.home, body: c.body ? JSON.parse(c.body) : null })), [method, re]);

// --- a person who isn't a manager ------------------------------------------------------------
{
  const { page, errors, ctx } = await open({ kind: 'user', user: 'alice', level: 'read', manager: false, epochMs: 0 });
  await page.waitForSelector('#gear:not([hidden])', { timeout: 5000 });
  ok('⚙ is offered to someone who isn\'t a manager', true);
  await page.click('#gear');
  await page.waitForSelector('#mcpd #mcpd-list');
  const tabs = await page.$$eval('#tabs .tab[data-tab]', (bs) => bs.filter((b) => !b.hidden).map((b) => b.dataset.tab));
  ok('…opened on MCP, with a conversation\'s Memory and Files beside it, and none of the managers\' tabs', tabs.join() === 'memory,files,mcp'
    && (await page.$eval('#tabs .tab.on', (b) => b.dataset.tab)) === 'mcp', tabs.join());
  ok('no call for the managers\' config', (await calls(page, 'GET', '/config$')).length === 0);
  const g = page.locator('.mcpd-row[data-id="mcpg1"]');
  ok('the global servers, read from the shared space', await g.isVisible() && (await g.textContent()).includes('everyone')
    && (await calls(page, 'GET', '/mcp/servers$')).some((c) => c.home === 'global'));
  ok('…which they may not change', (await g.locator('button[data-act="remove"]').count()) === 0 && (await g.locator('button[data-act="signin"]').count()) === 0);
  ok('they add only their own', (await page.$$eval('#mcpd-scope option', (os) => os.map((o) => o.value))).join() === 'personal');

  await page.fill('#mcpd-url', 'https://api.ottotheagent.com/mcp');
  await page.fill('#mcpd-name', 'otto');
  await page.click('#mcpd-add');
  await page.waitForSelector('#mcpd-open-mcp2', { timeout: 5000 });
  const add = (await calls(page, 'POST', '/mcp/servers$'))[0];
  ok('Add: at their own home, as theirs', add.home === '' && add.body.scope === 'personal' && add.body.url === 'https://api.ottotheagent.com/mcp' && add.body.name === 'otto', JSON.stringify(add));
  const si = (await calls(page, 'POST', '/mcp/servers/mcp2/signin$'))[0];
  ok('…then its sign-in, returning to this tile\'s mcp-callback.html', si && si.home === '' && /^http:\/\/[^/]+\/c\/apps\/agent\/mcp-callback\.html$/.test(si.body.redirectUri), JSON.stringify(si));
  const link = page.locator('#mcpd-open-mcp2');
  ok('…handed over as a real link to a new tab', (await link.getAttribute('href')).startsWith('https://auth.example/oauth/authorize') && (await link.getAttribute('target')) === '_blank'
    && (await link.getAttribute('rel')).includes('noopener'));
  ok('…the row says it needs a sign-in', (await page.textContent('.mcpd-row[data-id="mcp2"] [data-role="state"]')).includes('needs a sign-in'));

  // the callback finished in the other tab: coming back reads the list again
  await page.evaluate(() => { Object.assign(window.__mcp.mine[0], { state: 'ready', signedIn: true, tools: ['search_flights', 'book'] }); window.dispatchEvent(new Event('focus')); });
  await page.waitForFunction(() => document.querySelector('.mcpd-row[data-id="mcp2"]')?.dataset.state === 'ready', null, { timeout: 5000 });
  const row = page.locator('.mcpd-row[data-id="mcp2"]');
  ok('signed in: its tools, no link, Sign out offered', (await row.textContent()).includes('signed in · 2 tools') && (await row.textContent()).includes('search_flights')
    && (await page.$('#mcpd-open-mcp2')) === null && (await row.locator('button[data-act="signout"]').count()) === 1);
  await row.locator('button[data-act="remove"]').click();
  await page.waitForFunction(() => !document.querySelector('.mcpd-row[data-id="mcp2"]'), null, { timeout: 5000 });
  ok('Remove: confirmed, at its home', (await page.evaluate(() => window.__confirms.length)) === 1 && (await calls(page, 'DELETE', '/mcp/servers/mcp2$'))[0].home === '');
  ok('fits 700px: no horizontal scroll', await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  ok('no page errors', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// --- a manager: one for everyone goes to the shared space -----------------------------------
{
  const { page, errors, ctx } = await open({ kind: 'user', user: 'mgr', level: 'write', manager: true, epochMs: 0 });
  await page.evaluate(() => { window.__mcp.manager = true; });
  await page.click('#gear');
  await page.click('#tabs .tab[data-tab="mcp"]');
  await page.waitForSelector('#mcpd-scope');
  ok('a manager has every tab', (await page.$$eval('#tabs .tab[data-tab]', (bs) => bs.filter((b) => !b.hidden).length)) > 1);
  ok('…and may add one for everyone', (await page.$$eval('#mcpd-scope option', (os) => os.map((o) => o.value))).join() === 'personal,global');
  ok('…and change the global ones', (await page.locator('.mcpd-row[data-id="mcpg1"] button[data-act="remove"]').count()) === 1);
  await page.fill('#mcpd-url', 'https://tools.example/mcp');
  await page.selectOption('#mcpd-scope', 'global');
  await page.click('#mcpd-add');
  await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && /\/signin$/.test(c.url)), null, { timeout: 5000 });
  const add = (await calls(page, 'POST', '/mcp/servers$'))[0];
  const si = (await calls(page, 'POST', '/signin$'))[0];
  ok('a global one is added at the shared space, signed in there', add.home === 'global' && add.body.scope === 'global' && si.home === 'global', JSON.stringify([add, si]));
  ok('no page errors (manager)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('mcp-direct');
