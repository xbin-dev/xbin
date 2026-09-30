// harness-board.mjs — the Coding agents board on the web (D-harness §8 U7:
// harness-board.js) over kidsSeed() (test/harness-fixtures.mjs) and the
// STUB's §4 routes: #25's three coding agents — 26 working, 27 parked on a
// permission, 28 signed out. The top bar's chip and its counts; the dock (a
// third column, over the chat when narrow); rows in the order they started,
// kept as they change and as a new one arrives; the "needs you" filter; a
// park answered from the board, on the child; the pinned task's Delegated
// section; at home, yours that run or need you, and Needs you's sign-in.
//
//   node test/harness-board.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { kidsSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await serveTile(ctx);
const seed = kidsSeed();
await ctx.addInitScript(STUB, seed);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
page.on('dialog', (d) => d.accept());
const calls = (method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url))
  .map((c) => ({ url: c.url, body: c.body ? JSON.parse(c.body) : null })), [method, re]);
const waitCall = (method, re, n) => page.waitForFunction(([m, s, k]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)).length >= k,
  [method, re, n], { timeout: 5000 });
const text = (sel) => page.textContent(sel);
const push = (ev) => page.evaluate((e) => window.__push(e), ev);
const row = (id) => `#hboard .hbrow[data-row="${id}"]`;
const order = () => page.$$eval('#hboard .hbrow', (els) => els.map((e) => +e.dataset.row));
const waitOrder = (want) => page.waitForFunction((w) => JSON.stringify([...document.querySelectorAll('#hboard .hbrow')].map((e) => +e.dataset.row)) === w,
  JSON.stringify(want), { timeout: 5000 }).then(() => true, () => false);
const chipIs = (t) => page.waitForFunction((x) => document.querySelector('#hbchip')?.textContent === x, t, { timeout: 5000 }).then(() => true, () => false);
const noHScroll = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth && document.body.scrollWidth <= window.innerWidth);

await page.goto(`${ORIGIN}/#c=25`);
await page.waitForSelector('#hbchip');

// --- the chip ---------------------------------------------------------------------------------
ok('the chip: its coding agents, and how many need you', (await text('#hbchip')) === '⌨ 3 coding agents · 2 need you', await text('#hbchip'));
ok('…its title says each count', (await page.getAttribute('#hbchip', 'title')).startsWith('2 waiting for you · 1 running'));
ok('the tree is read once for it', (await calls('GET', '/runs/25/tree$')).length === 1);
ok('the board starts closed', !(await page.$('#hboard:not([hidden])')));

// --- the dock: rows in the order they started, each a child card -------------------------------
await page.click('#hbchip');
await page.waitForSelector(`${row(28)} .hkid`);
ok('open: a dock beside the chat (a third column at 1280 px)', await page.evaluate(() => {
  const d = document.getElementById('hboard');
  const r = d.getBoundingClientRect();
  return document.querySelector('.wrap').classList.contains('dockon') && getComputedStyle(d).position !== 'fixed'
    && Math.round(r.width) === 340 && Math.round(r.right) === window.innerWidth
    && document.getElementById('main').getBoundingClientRect().right <= r.left + 1;
}));
ok('no horizontal scroll', await noHScroll());
ok('its scope: this conversation', (await text('#hboard .hbscope')) === 'in Refactor the API');
ok('rows in the order they started', JSON.stringify(await order()) === '[26,27,28]', JSON.stringify(await order()));
const states = await page.$$eval('#hboard .hkid', (els) => els.map((e) => `${e.dataset.child}:${e.dataset.state}`));
ok('each a child card with its state', JSON.stringify(states) === '["26:working","27:approval","28:login"]', JSON.stringify(states));
ok('…what it does now', (await text(`${row(26)} .hkline`)).trim() === 'Running: Run go vet ./...');
ok('…its park drawn in place', !!(await page.$(`${row(27)} .hask [data-opt="approved"]`)) && !!(await page.$(`${row(28)} .hlogin`)));
ok('…and its actions', !!(await page.$(`${row(26)} [data-act="stop"]`)) && !!(await page.$(`${row(26)} [data-act="message"]`)));

// --- the order holds as they change; a new one comes last --------------------------------------
const h26 = seed.runs.find((r) => r.id === 26).harness;
await push({ type: 'run', run: 26, root: 25, data: { id: 26, status: 'waiting_input', parentId: 25, rootId: 25,
  pendingState: { kind: 'question', park: 'Q26', harness: { eid: 'e9', message: 'Keep the old routes as aliases?',
    schema: { type: 'object', properties: { keep: { type: 'boolean', title: 'Keep them' } } } } },
  harness: { ...h26, activity: { kind: 'waiting', at: 1 }, pending: { park: 'Q26', kind: 'question', title: 'Keep the old routes as aliases?' } } } });
await page.waitForFunction(() => document.querySelector('#hboard .hkid[data-child="26"]')?.dataset.state === 'question');
ok('26 now asks: its row stays first', JSON.stringify(await order()) === '[26,27,28]');
ok('the chip counts it', await chipIs('⌨ 3 coding agents · 3 need you'));
const n29 = { id: 29, parentId: 25, depth: 1, created: 99, title: 'Write the tests', status: 'running', rawStatus: 'running', engine: 'harness',
  harness: { provider: 'codex', name: 'Codex', state: 'starting', mode: { current: 'agent' }, counts: { tools: 0, files: 0, add: 0, del: 0 } } };
await page.evaluate(({ nodes, n }) => {
  const tree = { root: 25, nodes: [...nodes, n], totals: {} };
  window.__route('GET', /\/runs\/25\/tree$/, () => window.__json(tree));
}, { nodes: seed.trees[25].nodes, n: n29 });
const reads = (await calls('GET', '/runs/25/tree$')).length;
await push({ type: 'link', run: 25, root: 25, data: { id: 4, parentId: 25, childId: 29, toolCallId: 's4', mode: 'bg', state: 'running', label: 'Write the tests',
  created: 1790000000, child: { id: 29, title: 'Write the tests', status: 'running', parentId: 25, rootId: 25, engine: 'harness', harness: n29.harness } } });
ok('a new coding agent: the tree is read again, the new row comes last', await waitOrder([26, 27, 28, 29]), JSON.stringify(await order()));
ok('…one read more', (await calls('GET', '/runs/25/tree$')).length === reads + 1);
ok('…the chip counts it', await chipIs('⌨ 4 coding agents · 3 need you'));

// --- the "needs you" filter -------------------------------------------------------------------------
ok('the filter chip says how many need you', (await text('#hboard .hbfilter')) === '3 need you');
await page.click('#hboard .hbfilter');
ok('filtered: only those that need you', await waitOrder([26, 27, 28]));
ok('…pressed', (await page.getAttribute('#hboard .hbfilter', 'aria-pressed')) === 'true');
await page.click('#hboard .hbfilter');
ok('…and every one again', await waitOrder([26, 27, 28, 29]));

// --- a park answered from the board, on the child ------------------------------------------------------
await page.click(`${row(27)} .hask [data-opt="approved"]`);
await waitCall('POST', '/runs/27/approve$', 1);
const ap = (await calls('POST', '/runs/27/approve$')).pop().body;
ok('27\'s permission answered from the board: on 27, {park, option}', ap.park === 'Xq3kid' && ap.option === 'approved', JSON.stringify(ap));
ok('…never on the parent', (await calls('POST', '/runs/25/approve')).length === 0);
await page.waitForFunction(() => document.querySelector('#hboard .hkid[data-child="27"]')?.dataset.state === 'working');
ok('it works again, and keeps its place', JSON.stringify(await order()) === '[26,27,28,29]');
ok('the chip follows', await chipIs('⌨ 4 coding agents · 2 need you'));

// --- the pinned task: Delegated -------------------------------------------------------------------------
await page.click('.taskpin .tasktoggle');
await page.waitForSelector('.taskpin .taskdel .deleg');
const dels = await page.$$eval('.taskpin .taskdel .deleg', (els) => els.map((e) => +e.dataset.child));
ok('the unfolded task lists what it delegated', JSON.stringify(dels) === '[26,27,28,29]', JSON.stringify(dels));
const d26 = await text('.taskpin .deleg[data-child="26"]');
ok('…each coding agent\'s task, state and a link', d26.includes('#26 Split the router') && d26.includes('asks you')
  && d26.includes('Split the router into one file per resource'), d26);
ok('…read-only', !(await page.$('.taskpin input, .taskpin textarea, .taskpin button:not(.tasktoggle)')));
await page.click('.taskpin .deleg[data-child="28"] .lnk');
await page.waitForFunction(() => location.hash === '#c=28');
ok('its link opens the child\'s chat — the board stays, on the same tree', !!(await page.$('#hboard:not([hidden]) .hbrow[data-row="26"]'))
  && (await text('#hboard .hbscope')) === 'in Refactor the API');

// --- narrower: over the chat, never a horizontal scroll ----------------------------------------------------
for (const w of [900, 480]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.waitForTimeout(50);
  ok(`${w} px: the dock lies over the chat`, await page.evaluate(() => getComputedStyle(document.getElementById('hboard')).position === 'fixed'));
  ok(`${w} px: no horizontal scroll`, await noHScroll());
}
await page.setViewportSize({ width: 1280, height: 900 });

// --- home: yours that run or need you; Needs you's sign-in --------------------------------------------------
await page.click('#home');
await page.waitForSelector('.home .need');
const login = await page.textContent('.home .need[data-r="24"]');
ok('Needs you: the sign-in, naming the coding agent', login.includes('needs you to sign in to Codex') && login.includes('🔑'), login);
ok('home\'s chip: every one of yours at work', await chipIs('⌨ 7 coding agents · 5 need you'), await text('#hbchip'));
ok('the board follows home', (await text('#hboard .hbscope')).startsWith('yours'));
ok('…coding-agent conversations, then the ones below yours, in the order they started', await waitOrder([22, 23, 24, 26, 27, 28, 29]), JSON.stringify(await order()));
ok('…a child says which conversation it is in', (await text(`${row(26)} .hbin`)) === 'in Refactor the API' && !(await page.$(`${row(22)} .hbin`)));
ok('…nothing idle or done (21 is idle)', !(await page.$(row(21))));
await page.click(`${row(22)} .hask [data-opt="allow"]`);
await waitCall('POST', '/runs/22/approve$', 1);
ok('a park answered at home, on its run', (await calls('POST', '/runs/22/approve$')).pop().body.option === 'allow');

// --- closing it ---------------------------------------------------------------------------------------------
await page.click('#hboard [data-act="close"]');
await page.waitForFunction(() => document.getElementById('hboard').hidden && !document.querySelector('.wrap').classList.contains('dockon'));
ok('✕ closes the dock', true);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness-board');
