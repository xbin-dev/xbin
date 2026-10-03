// terminal.mjs — a terminal in a coding sandbox (D115; a manager's `tty`,
// docs/sandbox-manager.md) on the web: the sandbox badge's popover offers "Open terminal"
// only where the sandbox's manager says `tty` in its hello (disabled, with
// why, for an archived one), and a Sandboxes row "Terminal"; the pane holds
// the real <bx-terminal src> dialling the manager's …/tty route through the
// page's own xbin.ws (so the manager sees the verified person) at the
// binding's working directory. It sends its size first, takes typed input
// and prints what comes back, resizes when the pane grows, keeps Escape for
// the shell, reattaches to the same exec after a drop, says when the shell
// ended (New shell starts another), and Close ends the shell with a DELETE
// of its exec at the manager. The manager is a fake WebSocket speaking the
// terminal wire (window.__tty).
//
//   node test/terminal.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { FAKE_TTY, serveTerminal } from './fake-tty.mjs';

const { ok, done } = checker();
const now = Math.floor(Date.now() / 1000);
const MGR = 'apps/coding-sandbox';
const coding = { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web', 'files'], managers: 'all', sandboxEgress: ['none', 'internet'] };
const sb = (id, extra = {}) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
  image: { id: 'base', title: 'Debian' }, lastActive: Date.now() - 60e3, ...extra });
const bind = (id, extra = {}) => ({ ref: `${MGR}|${id}`, name: id, cwd: '/work/api', manager: 'Coding sandboxes', egress: 'none', by: 'admin', ...extra });
const MANAGER = { provider: MGR, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none', 'internet'],
  images: [{ id: 'base', title: 'Debian', default: true }], sizes: [], limits: {} };

const seed = {
  runs: [
    { id: 1, title: 'fix the build', status: 'idle', parentId: 0, rootId: 1, updated: now },
    { id: 2, title: 'frozen', status: 'idle', parentId: 0, rootId: 2, updated: now },
    { id: 3, title: 'elsewhere', status: 'idle', parentId: 0, rootId: 3, updated: now },
  ],
  views: {
    1: { access: 'owner', class: coding, config: { sandbox: bind('api'), attached: [bind('api')] },
      run: { id: 1, title: 'fix the build', status: 'idle', parentId: 0, rootId: 1 } },
    2: { access: 'owner', class: coding, config: { sandbox: bind('cold', { cwd: '' }) }, run: { id: 2, title: 'frozen', status: 'idle', parentId: 0, rootId: 2 } },
    3: { access: 'owner', class: coding, config: { sandbox: bind('plain', { provider: 'apps/plain', ref: 'apps/plain|plain' }) },
      run: { id: 3, title: 'elsewhere', status: 'idle', parentId: 0, rootId: 3 } },
  },
  sandboxes: [sb('api', { boundTo: [1] }), sb('cold', { state: 'archived', boundTo: [2] }), sb('web', { state: 'stopped' }),
    sb('plain', { ref: 'apps/plain|plain', provider: 'apps/plain', manager: 'Plain', boundTo: [3] })],
  sbxManagers: [MANAGER, { ...MANAGER, provider: 'apps/plain', title: 'Plain', caps: ['exec', 'files'] }],
};

const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await serveTile(ctx);
await serveTerminal(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(FAKE_TTY);
// DELETE of an exec at the manager: straight from the page (xbin.fetch)
await ctx.addInitScript(() => window.__route('DELETE', /\/api\/apps\/coding-sandbox\/sbx\/sandboxes\/[^/]+\/execs\/[^/]+$/, () => new Response(null, { status: 204 })));
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('dialog', (d) => d.accept());
const tty = () => page.evaluate(() => ({ dials: window.__tty.dials, resizes: window.__tty.resizes, keys: window.__tty.keys }));
const screen = () => page.$eval('#sbxterm-pane bx-terminal', (t) => t.testApi().text());
const until = (fn, arg, timeout = 8000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 30 });
const deletes = () => page.evaluate(() => window.__calls.filter((c) => c.method === 'DELETE').map((c) => c.url));

// --- the popover: Open terminal where the manager has tty ------------------------------------
await page.goto(`${ORIGIN}/#c=1`);
await page.waitForSelector('#sbxbadge');
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop');
await until(() => !!document.getElementById('sbx-term'));
ok('the popover offers Open terminal (its manager says tty)', !(await page.$eval('#sbx-term', (b) => b.disabled)));
await page.click('#sbx-term');
await page.waitForSelector('#sbxterm-pane bx-terminal');
ok('the popover closes; the pane opens', !(await page.$('#sbxpop')));
await until(() => window.__tty.dials.length > 0 && window.__tty.resizes.length > 0);
let t = await tty();
ok('it dials the manager\'s tty through the page\'s xbin.ws, at the binding\'s working directory',
  t.dials[0] === '/api/apps/coding-sandbox/sbx/sandboxes/api/tty?cwd=%2Fwork%2Fapi', t.dials[0]);
const first = t.resizes[0];
ok('…and sends its size first', first && first[0] > 20 && first[1] > 5, JSON.stringify(t.resizes));
const head = await page.textContent('#sbxterm-pane .sbxthd');
ok('the pane says which sandbox and where', head.includes('api') && head.includes('/work/api') && head.includes('Coding sandboxes'), head.replace(/\s+/g, ' '));
await until(() => document.querySelector('#sbxterm-pane bx-terminal').testApi().text().includes('sandbox$'));
await page.click('#sbxterm-pane bx-terminal');
await page.keyboard.type('echo hi-from-tty');
await page.keyboard.press('Enter');
await until(() => /\nhi-from-tty\n/.test(document.querySelector('#sbxterm-pane bx-terminal').testApi().text()));
ok('typed input goes to the manager, and what comes back is printed', (await tty()).keys.startsWith('echo hi-from-tty\r'), JSON.stringify((await tty()).keys));

// Escape belongs to the shell: the pane stays, the key went through
await page.keyboard.press('Escape');
await until(() => window.__tty.keys.includes('\x1b'));
ok('Escape reaches the shell; the pane stays open', !!(await page.$('#sbxterm-pane')));

// bigger: the terminal resizes the far end
const n0 = (await tty()).resizes.length;
await page.click('#sbxterm-max');
await until((n) => window.__tty.resizes.length > n, n0);
t = await tty();
const grown = t.resizes.at(-1);
ok('Larger: the terminal grows and says so to the manager', grown[0] > first[0] && grown[1] > first[1], `${JSON.stringify(first)} → ${JSON.stringify(grown)}`);
const size = await page.$eval('#sbxterm-pane bx-terminal', (el) => el.testApi().size);
ok('…to the size it draws', size.cols === grown[0] && size.rows === grown[1], JSON.stringify(size));

// a drop: it reattaches to the same exec, not a new shell
await page.evaluate(() => window.__tty.drop());
await until(() => window.__tty.dials.length > 1, null, 10000);
t = await tty();
ok('after a drop it reattaches to the same exec', t.dials[1] === '/api/apps/coding-sandbox/sbx/sandboxes/api/execs/e1/tty', JSON.stringify(t.dials));
await until(() => document.querySelector('#sbxterm-pane bx-terminal').testApi().text().includes('(reattached)'));
ok('…and keeps its screen', (await screen()).includes('hi-from-tty'));

// Close: the pane goes, the shell is ended at the manager
await page.click('#sbxterm-close');
await until(() => !document.getElementById('sbxterm-pane'));
await until(() => window.__calls.some((c) => c.method === 'DELETE'));
ok('Close ends the shell: DELETE its exec at the manager, from the page',
  (await deletes()).join() === '/api/apps/coding-sandbox/sbx/sandboxes/api/execs/e1', JSON.stringify(await deletes()));
ok('…its socket is closed', await page.evaluate(() => window.__tty.sockets.every((s) => s.readyState === 3)));

// --- the dialog's row: Terminal; exit and New shell ---------------------------------------------------
await page.click('#sbxbadge');
await page.click('#sbx-manage');
await page.waitForSelector('#sbxdlg[open] .sbxrow');
await until(() => !!document.querySelector('#sbxdlg .sbxrow[data-ref$="|web"] [data-act="terminal"]'));
const rowActs = await page.$$eval('#sbxdlg .sbxrow', (els) => Object.fromEntries(els.map((e) => [e.querySelector('.nm').textContent.trim(),
  [...e.querySelectorAll('[data-act]')].map((b) => b.dataset.act)])));
ok('rows offer Terminal where the manager has tty and it can run', rowActs.api.includes('terminal') && rowActs.web.includes('terminal')
  && !rowActs.cold.includes('terminal') && !rowActs.plain.includes('terminal'), JSON.stringify(rowActs));
await page.click('#sbxdlg .sbxrow[data-ref$="|web"] [data-act="terminal"]');
await page.waitForSelector('#sbxterm-pane bx-terminal');
ok('Terminal closes the dialog, opens the pane', !(await page.$('#sbxdlg[open]')));
await until(() => window.__tty.dials.length > 2);
ok('a row not in this conversation: its workdir (no cwd)', (await tty()).dials.at(-1) === '/api/apps/coding-sandbox/sbx/sandboxes/web/tty', (await tty()).dials.at(-1));
await until(() => document.querySelector('#sbxterm-pane bx-terminal').testApi().text().includes('sandbox$'));
await page.click('#sbxterm-pane bx-terminal');
await page.keyboard.type('exit');
await page.keyboard.press('Enter');
await page.waitForSelector('#sbxterm-ended');
await until(() => document.querySelector('#sbxterm-pane bx-terminal').testApi().text().includes('[exited]')).catch(() => {});
ok('the shell exits: the pane says it ended and offers New shell', !!(await page.$('#sbxterm-again')) && (await screen()).includes('[exited]'), await screen());
const dials = (await tty()).dials.length;
await page.click('#sbxterm-again');
await until((n) => window.__tty.dials.length > n, dials);
ok('New shell dials a fresh one', (await tty()).dials.at(-1) === '/api/apps/coding-sandbox/sbx/sandboxes/web/tty' && !(await page.$('#sbxterm-ended')));
await page.click('#sbxterm-close');
await until(() => !document.getElementById('sbxterm-pane'));
await until(() => window.__calls.filter((c) => c.method === 'DELETE').length >= 2);
ok('an ended shell is not ended again; the fresh one is', (await deletes()).at(-1) === '/api/apps/coding-sandbox/sbx/sandboxes/web/execs/e3'
  && (await deletes()).length === 2, JSON.stringify(await deletes()));

// --- where there is no terminal ----------------------------------------------------------------------
await page.goto(`${ORIGIN}/#c=2`);
await page.waitForSelector('#sbxbadge');
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop');
await until(() => !!document.getElementById('sbx-term'));
ok('an archived sandbox: Open terminal disabled, and why', await page.$eval('#sbx-term', (b) => b.disabled)
  && /archived — thaw it first/.test(await page.textContent('#sbx-term-why')), await page.textContent('#sbx-term-why'));
await page.keyboard.press('Escape');
await page.goto(`${ORIGIN}/#c=3`);
await page.waitForSelector('#sbxbadge');
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop');
await page.waitForTimeout(200);
ok('a manager without tty: no Open terminal', !(await page.$('#sbx-term')) && !(await page.$('#sbx-term-why')));

// --- the element on its own: a refused src gives up after a retry; another src gets its own ------------
await page.keyboard.press('Escape');
const refusedDials = (id) => page.evaluate((id) => window.__tty.dials.filter((d) => d.includes(`/sandboxes/${id}/`)).length, id);
await page.evaluate(async () => {
  await import('/vendor/bx-terminal.js');
  const t = document.createElement('bx-terminal');
  t.id = 'lone';
  t.style.cssText = 'position:fixed;left:0;top:0;width:400px;height:200px';
  t.src = '/api/apps/coding-sandbox/sbx/sandboxes/refuse-a/tty';
  document.body.append(t);
});
const gaveUp = (n) => until((n) => (document.getElementById('lone').testApi().text().match(/could not open the terminal/g) || []).length >= n, n);
await gaveUp(1);
ok('a refused handshake is tried twice, then the terminal says it could not open', (await refusedDials('refuse-a')) === 2, String(await refusedDials('refuse-a')));
await page.evaluate(() => { document.getElementById('lone').src = '/api/apps/coding-sandbox/sbx/sandboxes/refuse-b/tty'; });
await gaveUp(2);
ok('…and another src starts over: it is tried twice too', (await refusedDials('refuse-b')) === 2, String(await refusedDials('refuse-b')));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('terminal');
