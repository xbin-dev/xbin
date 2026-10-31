// projects-overview.mjs — a project's overview page on the web (D214; API.md
// §Projects in the UI) over moreSeed()'s overview (projects-more-stub.mjs
// overviewSeed): the status strip's cells and words (scm text plain, links
// only https), Day/Week (a read with range=week), the fold and its pref, the
// board's cards with their agent, the drag bar (pointer and keys, its clamps,
// double-click, its pref), the coordinator's section (state, Stop, Open
// chat, the goal line expanding, idle: what wakes it), its chat window
// keeping its height as messages arrive, tooltips (none before the delay,
// shown after it, switching at once, at once on keyboard focus, a tap on a
// phone), a tall tile (the page fits it, the bar at the bottom) against a
// short one (the page scrolls, the input in the section; the draft moves
// between them), leaving the page, an older backend (the page as before), a
// phone's width (no sideways page scroll; the board scrolls sideways).
//
//   node test/projects-overview.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { PROJ_STUB } from './projects-stub.mjs';
import { PROJ_MORE_STUB, moreSeed, overviewSeed, coordMsgs } from './projects-more-stub.mjs';

const { ok, done } = checker();
const browser = await launch();

async function open(seed, { width = 1280, height = 900, hash = '#proj=7', init = null, touch = false } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height }, ...(touch ? { hasTouch: true } : {}) });
  await serveTile(ctx, { realMarked: true });
  await ctx.addInitScript(STUB, seed);
  await ctx.addInitScript(PROJ_STUB, seed);
  await ctx.addInitScript(PROJ_MORE_STUB, seed);
  if (init) await ctx.addInitScript(init);
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('dialog', (d) => d.accept());
  await page.goto(`${ORIGIN}/${hash}`);
  return { page, errors, ctx };
}
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url))
  .map((c) => ({ url: c.url, body: c.body ? JSON.parse(c.body) : null })), [method, re]);
const until = (page, fn, arg) => page.waitForFunction(fn, arg, { timeout: 5000 }).then(() => true, () => false);
const text = async (page, sel) => ((await page.$eval(sel, (e) => e.innerText)) || '').replace(/\s+/g, ' ').trim();
const has = (t, ...words) => words.every((w) => t.includes(w));
const box = (page, sel) => page.$eval(sel, (e) => { const r = e.getBoundingClientRect(); return { top: Math.round(r.top), bottom: Math.round(r.bottom), h: Math.round(r.height) }; });
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth && document.body.scrollWidth <= window.innerWidth);
const tip = (page) => page.evaluate(() => { const b = document.querySelector('.bxtip.on'); return b ? { title: b.querySelector('b')?.textContent || '', text: b.textContent } : null; });

// the coordinator's conversation: enough that its window clips the oldest
const SAID = [['user', 'Triage the open bugs into tasks, keep main green, and tell me what needs me.'],
  ['tool', 'done', { name: 'issue_list', args: { summary: 'acme/web, acme/api · label:bug → 9 open' } }],
  ['tool', 'done', { name: 'task_create', args: { summary: '#9 Login loops on Safari' } }],
  ['assistant', 'Made **3 tasks** from the 9 open bugs; the other 6 are duplicates or need more information (I commented on each).'],
  ['user', 'thanks — prioritise #9'],
  ['assistant', 'Moved #9 to the front of the queue; it starts as soon as a slot frees (2 of 3 in use).'],
  ['tool', 'done', { name: 'task_message', args: { summary: '#1 · the retry path' } }],
  ['assistant', 'CI failed on #1 (test (ubuntu), upload_spec). I sent the failure to its agent; it is fixing it.'],
  ['user', 'what about the flaky upload test?'],
  ['assistant', 'The same test failed twice on main this week, so I am making a task to fix the flaky test itself.'],
  ['tool', '', { name: 'task_create', args: { summary: '#12 Retry flaky upload test' } }]];
const seedWith = (extra = {}) => ({ ...moreSeed(), coordinator: { 7: { status: 'running', messages: coordMsgs(900, SAID), hasOlder: true } }, ...extra });
const idleOverview = () => {
  const o = overviewSeed(7, 'day');
  o.coordinator = { ...o.coordinator, state: 'idle', run: { id: 900, status: 'idle' } };
  return o;
};

// === the page on a tall tile ===============================================================================
{
  const { page, errors } = await open(seedWith());
  await page.waitForSelector('#pov-strip .povc[data-cell="sandbox"]');
  const cells = await page.$$eval('#pov-strip .povc', (els) => els.map((e) => e.dataset.cell));
  ok('the strip: six cells, in order', JSON.stringify(cells) === '["coordinator","workers","subagents","tokens","scm","sandbox"]', JSON.stringify(cells));
  ok('…read once, the day\'s', JSON.stringify(await page.evaluate(() => window.__more.overviewReads)) === '[{"pid":7,"range":"day"}]');
  const c = await text(page, '.povc[data-cell="coordinator"]');
  ok('the coordinator: its state, what it does, what woke it, its counts', has(c, 'Working', 'making a task for the flaky upload test', 'last:', 'CI failed on #1', '6 turns today', '11 tasks / 7 d'), c);
  const w = await text(page, '.povc[data-cell="workers"]');
  ok('worker agents: tasks, slots, by state, by kind', has(w, '7 tasks', '2/3', '2 working', '3 need you', '2 idle', '1 queued', '4 Claude Code', '3 agent'), w);
  const s = await text(page, '.povc[data-cell="subagents"]');
  ok('subagents: running, waiting, finished and failed in 24 h, under which', has(s, '4 running', '1 waiting', '17 finished (24 h)', '2 failed', '#2', '×2', 'coordinator'), s);
  const t = await text(page, '.povc[data-cell="tokens"]');
  ok('tokens: the total, each kind, the coding agents\' cost', has(t, '1.84 M', 'last 24 h', 'work 1.21 M', 'coord 380 k', 'sub 250 k', '$6.40'), t);
  ok('…a stacked chart: three layers', (await page.$$('.povc[data-cell="tokens"] svg.povch path')).length === 3);
  const g = await text(page, '.povc[data-cell="scm"]');
  ok('source control: the provider\'s title, the repos, PRs failing and in review, issues, merged', (await page.textContent('.povc[data-cell="scm"] h4')).includes('GitHub') && has(g, 'acme/web', 'acme/api', '3 PRs', '1 review', '14 issues', '5 merged / 7 d'), g);
  ok('…each repo a link to it', JSON.stringify(await page.$$eval('.povc[data-cell="scm"] .repo a', (els) => els.map((a) => a.getAttribute('href'))))
    === '["https://github.com/acme/web","https://github.com/acme/api"]');
  ok('…a failing default branch marked', (await page.getAttribute('.repo[data-repo="acme/api"] .pdot', 'data-tone')) === 'bad' && (await page.getAttribute('.repo[data-repo="acme/web"] .pdot', 'data-tone')) === 'ok');
  const b = await text(page, '.povc[data-cell="sandbox"]');
  ok('the sandbox: its name, state, commands, size, disk, CPU and memory', has(b, 'sb-web', 'Running', '2 cmds', '2 vCPU', '4.0 GiB', '3.1 GiB', '/ 20 GiB', 'CPU', '63%', 'Mem', '2.6 GiB'), b);
  ok('…two charts of the last hour', (await page.$$('.povc[data-cell="sandbox"] svg.povch')).length === 2);
  ok('the strip is ~125 px tall (a row)', await page.$eval('.povc[data-cell="coordinator"]', (e) => Math.abs(e.offsetHeight - 124) <= 2));

  // the board's cards say their agent
  ok('a card says its agent, what it does, its subagents', (await text(page, '.ptask[data-n="2"] .ptagent')) === 'agent · working · 2 subagents · 120 k tokens', await text(page, '.ptask[data-n="2"] .ptagent'));
  ok('…a coding agent\'s', (await text(page, '.ptask[data-n="1"] .ptagent')).startsWith('Claude Code · working · 1 subagent'));
  ok('…its tooltip instead of a title', (await page.getAttribute('.ptask[data-n="2"]', 'data-tip')).includes('Its agent: agent, working, with 2 subagents running') && !(await page.getAttribute('.ptask[data-n="2"]', 'title')));

  // the top bar: the name and repos, Status, the tabs
  ok('the top bar: Status and the tabs', !!(await page.$('#top #pov-fold')) && (await text(page, '#top [data-tab="board"]')) === 'Overview' && !(await page.$('.projs-page .ahd')));
  ok('…the repos beside the name', (await text(page, '#top .title')).includes('acme/web, acme/api'));

  // a tall tile: the page fits it — nothing of the timeline scrolls, the board scrolls in its share, the bar at the bottom
  ok('a tall tile: the page fits it', await page.evaluate(() => document.getElementById('main').classList.contains('pov-fit')
    && document.getElementById('timeline').scrollHeight <= document.getElementById('timeline').clientHeight + 1));
  ok('…the board scrolls inside its share', await page.$eval('#pov-board', (e) => e.scrollHeight > e.clientHeight && getComputedStyle(e).overflowY === 'auto'));
  ok('…the input is the bar at the bottom', !(await page.$eval('.composer', (e) => e.hidden)) && !(await page.$('#pcoord-msg')) && (await page.textContent('#pjto')).trim() === 'Coordinator');
  ok('…all of it in view', await page.evaluate(() => document.querySelector('.composer').getBoundingClientRect().bottom <= window.innerHeight
    && document.getElementById('pcoord').getBoundingClientRect().bottom <= document.querySelector('.composer').getBoundingClientRect().top + 1));

  // the coordinator's section
  await page.waitForSelector('#pcoord-chat .msg');
  ok('the coordinator: its state, model, tokens today, Stop, Open chat', (await text(page, '#pcoord-state')) === 'working'
    && has(await text(page, '#pcoord .pch'), 'Claude Opus 5.5', '312 k tokens today') && !!(await page.$('#pcoord-stop')) && (await text(page, '#pcoord-open')) === 'Open chat');
  ok('…its goal, one line, and what it does now', (await page.getAttribute('#pcoord-goal', 'data-kind')) === 'goal'
    && has(await text(page, '#pcoord-goal'), 'Triage open bugs into tasks and keep main green; #9 first.', 'Now: making a task for the flaky upload test')
    && await page.$eval('#pcoord-goal', (e) => getComputedStyle(e).whiteSpace === 'nowrap'));
  const g0 = await box(page, '#pcoord-goal');
  await page.click('#pcoord-goal .gt');
  ok('…a click shows all of it', await until(page, () => document.querySelector('#pcoord-goal').classList.contains('open') && document.querySelector('#pcoord-goal').getAttribute('aria-expanded') === 'true'));
  ok('…on more lines', (await box(page, '#pcoord-goal')).h > g0.h, JSON.stringify([g0, await box(page, '#pcoord-goal')]));
  await page.click('#pcoord-goal .gt');
  await until(page, () => !document.querySelector('#pcoord-goal').classList.contains('open'));
  ok('the window: its newest messages and tool steps (one line each)', await page.$$eval('#pcoord-chat .pcstep', (els) => els.length >= 2 && els.every((e) => getComputedStyle(e).whiteSpace === 'nowrap')));
  ok('…a running step says so', (await text(page, '#pcoord-chat .pcstep:last-child')).includes('task_create') && (await text(page, '#pcoord-chat .pcstep:last-child')).includes('running'));
  ok('…markdown in its messages', !!(await page.$('#pcoord-chat .msg.assistant strong')));
  ok('…the oldest clipped at the top, under "earlier messages are in its chat"', await until(page, () => document.getElementById('pcoord-chat').dataset.clip === '1')
    && await page.$eval('#pcoord-chat .pcearlier', (e) => getComputedStyle(e).display !== 'none' && e.textContent.includes('earlier messages are in its chat')));
  ok('…no scrollbar of its own', await page.$eval('#pcoord-chat', (e) => ['hidden', 'clip'].includes(getComputedStyle(e).overflowY)));
  ok('…its height the default, 300 px', (await box(page, '#pcoord-chat')).h === 300);
  ok('…its messages read with room to spare', (await calls(page, 'GET', '/runs/900/view\\?limit=30$')).length >= 1);

  // a message arrives: the window keeps its height, nothing below it moves
  const before = { chat: await box(page, '#pcoord-chat'), sec: await box(page, '#pcoord'), bar: await box(page, '.composer'), board: await box(page, '#pov-board') };
  await page.evaluate(() => {
    const v = window.__views[900];
    const n = v.messages.length;
    v.messages = [...v.messages, ...['#12 is queued; it starts when #2 finishes.', 'CI passed on #1 after the fix; it waits for the second review.', 'New issue acme/web#58 (bug): made task #13 for it.']
      .map((t, i) => ({ id: 800 + i, runId: 900, seq: n + 100 + i, role: 'assistant', content: t, created: 1790000500 + i }))];
    window.__push({ type: 'run', run: 900, root: 900, data: { id: 900, status: 'running', parentId: 0, rootId: 900, origin: 'project', originId: 7 } });
  });
  ok('new messages show at the bottom', await until(page, () => (document.querySelector('#pcoord-chat .msg:last-child')?.textContent || '').includes('made task #13')));
  const after = { chat: await box(page, '#pcoord-chat'), sec: await box(page, '#pcoord'), bar: await box(page, '.composer'), board: await box(page, '#pov-board') };
  ok('…the window keeps its height and place, nothing around it moves', JSON.stringify(before) === JSON.stringify(after), JSON.stringify([before, after]));
  ok('…the newest in view, the oldest pushed out at the top', await page.evaluate(() => {
    const ch = document.getElementById('pcoord-chat').getBoundingClientRect();
    const last = document.querySelector('#pcoord-chat .pcmsgs > :last-child').getBoundingClientRect();
    const first = document.querySelector('#pcoord-chat .pcmsgs > :first-child').getBoundingClientRect();
    return last.bottom <= ch.bottom + 1 && first.top < ch.top;
  }));

  // Day / Week
  await page.click('#pov-range [data-range="week"]');
  ok('Week: read again with range=week', await until(page, () => window.__more.overviewReads.some((r) => r.range === 'week')));
  ok('…its total and span', await until(page, () => /11\.6 M/.test(document.querySelector('.povc[data-cell="tokens"]').textContent) && /last 7 days/.test(document.querySelector('.povc[data-cell="tokens"]').textContent)));
  ok('…pressed', (await page.getAttribute('#pov-range [data-range="week"]', 'aria-pressed')) === 'true' && (await page.getAttribute('#pov-range [data-range="day"]', 'aria-pressed')) === 'false');
  await page.click('#pov-range [data-range="day"]');
  ok('…and back to the day', await until(page, () => /last 24 h/.test(document.querySelector('.povc[data-cell="tokens"]').textContent)));

  // a project event reads the overview again (coalesced)
  const reads = (await page.evaluate(() => window.__more.overviewReads.length));
  await page.evaluate(() => { for (let i = 0; i < 3; i++) window.__push({ type: 'project', run: 0, root: 0, data: { id: 7, change: 'coordinator' } }); });
  ok('its project\'s events read it again, once for a burst', await until(page, (n) => window.__more.overviewReads.length === n + 1, reads));
  await page.waitForTimeout(700);
  ok('…once', (await page.evaluate(() => window.__more.overviewReads.length)) === reads + 1);

  // the fold
  await page.click('#pov-fold');
  ok('Status folds the strip to one line', await until(page, () => !document.getElementById('pov-strip') && !!document.getElementById('pov-line')));
  const line = await text(page, '#pov-line');
  ok('…its words', has(line, 'coordinator working', 'workers 2 working · 3 need you', 'subagents 4', 'tokens 1.84 M/24 h', 'PRs 3 · 1 failing', 'sandbox running · CPU 63%'), line);
  ok('…kept in your prefs', await until(page, () => window.__prefs['project-status-folded'] === true));
  ok('…the button says so', (await page.getAttribute('#pov-fold', 'aria-expanded')) === 'false');
  await page.click('#pov-line');
  ok('…the line unfolds it', await until(page, () => !!document.getElementById('pov-strip') && window.__prefs['project-status-folded'] === false));

  // Stop: interrupts its turn, what it had not read comes back into the input
  await page.evaluate(() => window.__route('POST', /\/runs\/900\/interrupt$/, () => window.__json({ ok: 'true', returned: [{ id: 1, text: 'also look at #4' }] })));
  await page.click('#pcoord-stop');
  ok('Stop interrupts the coordinator\'s turn', await until(page, () => window.__calls.some((c) => c.method === 'POST' && /\/runs\/900\/interrupt$/.test(c.url))));
  ok('…what it had not read comes back into the bar', await until(page, () => document.getElementById('msg').value === 'also look at #4'));
  await page.fill('#msg', '');

  // leaving the page: a conversation opens — the layout and the bar's label go; back, they return
  await page.click('.ptask[data-n="2"]');
  ok('a card opens its conversation: the page\'s layout goes', await until(page, () => location.hash === '#c=102' && !document.getElementById('main').classList.contains('pov-on')
    && !document.getElementById('main').classList.contains('pov-fit') && document.getElementById('pjto').hidden));
  ok('…the timeline scrolls again', await page.$eval('#timeline', (e) => getComputedStyle(e).overflowY === 'auto'));
  await page.evaluate(() => { location.hash = '#proj=7'; });
  ok('…and back', await until(page, () => document.getElementById('main').classList.contains('pov-fit') && !!document.getElementById('pov-strip')));
  ok('no page errors (the page)', errors.length === 0, errors.join(' | '));
}

// === the bar: drag, keys, clamps, double-click, kept =========================================================
{
  const { page, errors } = await open(seedWith(), { width: 1600, height: 1000 });
  await page.waitForSelector('#pov-split');
  await page.waitForSelector('#pcoord-chat .msg');
  const h = () => page.$eval('#pcoord-chat', (e) => e.offsetHeight);
  ok('the bar: a separator', (await page.getAttribute('#pov-split', 'role')) === 'separator' && (await page.getAttribute('#pov-split', 'tabindex')) === '0' && (await h()) === 300);
  const drag = async (dy) => {
    const r = await box(page, '#pov-split');
    const x = 800, y = r.top + 4;
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x, y + dy / 2, { steps: 3 });
    await page.mouse.move(x, y + dy, { steps: 3 });
    await page.mouse.up();
  };
  await drag(-80);
  ok('dragging it up gives the chat more', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 380));
  ok('…kept in your prefs', await until(page, () => window.__prefs['project-chat-height'] === 380));
  ok('…its value said', (await page.getAttribute('#pov-split', 'aria-valuenow')) === '380');
  await page.focus('#pov-split');
  await page.keyboard.press('ArrowUp');
  ok('ArrowUp: 24 px more', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 404));
  await page.keyboard.press('Shift+ArrowUp');
  ok('Shift+ArrowUp: 96 px more', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 500));
  await page.keyboard.press('ArrowDown');
  ok('ArrowDown: 24 px less', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 476 && window.__prefs['project-chat-height'] === 476));
  await drag(-3000);
  ok('…up as far as it goes: the board keeps 120 px', await until(page, () => window.__prefs['project-chat-height'] > 476)
    && await page.$eval('#pov-board', (e) => e.clientHeight >= 119 && e.clientHeight <= 122), String(await page.$eval('#pov-board', (e) => e.clientHeight)));
  await drag(3000);
  ok('…down as far as it goes: the chat keeps 100 px', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 100 && window.__prefs['project-chat-height'] === 100));
  await page.dblclick('#pov-split');
  ok('double-click puts it back', await until(page, () => document.getElementById('pcoord-chat').offsetHeight === 300));
  ok('…and forgets it', (await calls(page, 'DELETE', '/prefs/project-chat-height$')).length === 1);
  ok('no page errors (the bar)', errors.length === 0, errors.join(' | '));

  // kept: a height and a fold set before
  const { page: k, errors: ke } = await open(seedWith({ prefs: { 'project-chat-height': 250, 'project-status-folded': true } }), { width: 1600, height: 1000 });
  await k.waitForSelector('#pcoord-chat');
  ok('your height and fold, from your prefs', await until(k, () => document.getElementById('pcoord-chat').offsetHeight === 250 && !!document.getElementById('pov-line')));
  ok('no page errors (kept)', ke.length === 0, ke.join(' | '));
}

// === tooltips ===================================================================================================
{
  const { page, errors } = await open(seedWith(), { width: 1600, height: 1000 });
  await page.waitForSelector('.povc[data-cell="workers"]');
  await page.mouse.move(1500, 990); // an untipped place (the bar's Send)
  const at = async (sel) => { const r = await page.$eval(sel, (e) => { const b = e.getBoundingClientRect(); return [b.left + b.width / 2, b.top + b.height / 2]; }); await page.mouse.move(r[0], r[1]); };
  await at('.povc[data-cell="workers"] h4 > span');
  await page.waitForTimeout(250);
  ok('hovering: no tooltip before the delay', (await tip(page)) === null);
  ok('…then the cell\'s, its first sentence the title', await until(page, () => document.querySelector('.bxtip.on b')?.textContent === 'Worker agents'));
  ok('…the rest its body', (await tip(page)).text.includes('One per task'));
  await at('.povc[data-cell="tokens"] .big');
  ok('moving to another: it switches at once', (await tip(page))?.title === 'Tokens');
  await page.mouse.move(1500, 990);
  ok('moving off: it goes', await until(page, () => !document.querySelector('.bxtip.on')));
  // passing over things shows nothing
  await page.waitForTimeout(400);
  for (const c of ['coordinator', 'workers', 'subagents', 'scm']) await at(`.povc[data-cell="${c}"] h4`);
  await page.mouse.move(1500, 990);
  await page.waitForTimeout(800);
  ok('passing over cells: none stays', (await tip(page)) === null);
  ok('…kept inside the window', await (async () => {
    await at('.povc[data-cell="sandbox"] h4 > span');
    await until(page, () => !!document.querySelector('.bxtip.on'));
    return page.evaluate(() => { const r = document.querySelector('.bxtip.on').getBoundingClientRect(); return r.right <= innerWidth && r.bottom <= innerHeight && r.left >= 0 && r.top >= 0; });
  })());
  await page.mouse.move(1500, 990);
  await until(page, () => !document.querySelector('.bxtip.on'));
  // keyboard focus: at once, described by it
  await page.focus('#pov-fold');
  await page.keyboard.press('Tab');
  const focused = await page.evaluate(() => document.activeElement.dataset.tab);
  ok('keyboard focus: at once', focused === 'board' && (await tip(page))?.title === 'Overview', `${focused} ${JSON.stringify(await tip(page))}`);
  ok('…the element described by it', (await page.evaluate(() => document.activeElement.getAttribute('aria-describedby'))) === 'bx-tip'
    && (await page.getAttribute('#bx-tip', 'role')) === 'tooltip');
  await page.keyboard.press('Escape');
  ok('…Escape hides it', (await tip(page)) === null && !(await page.evaluate(() => document.activeElement.getAttribute('aria-describedby'))));
  await page.focus('#pov-split');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Shift+Tab');
  ok('the bar has one too', (await tip(page))?.title === 'Drag to resize');
  const untipped = await page.evaluate(() => [...document.querySelectorAll('#pov .povc, #pov .chip, #pov button, #top #pov-fold, #top [role="tab"], #pov-split, #pjto')]
    .filter((e) => e.offsetParent !== null && !e.closest('[data-tip]')).map((e) => e.id || e.className || e.tagName));
  ok('every cell, chip and button of the overview has one', untipped.length === 0, JSON.stringify(untipped));
  ok('no page errors (tooltips)', errors.length === 0, errors.join(' | '));
}

// === idle: what wakes it =========================================================================================
{
  const { page, errors } = await open({ ...seedWith(), overview: { 7: idleOverview() }, coordinator: { 7: { status: 'idle', messages: coordMsgs(900, SAID.slice(0, 6)) } } });
  await page.waitForSelector('#pcoord-goal');
  ok('idle: what wakes it, then the goal', (await page.getAttribute('#pcoord-goal', 'data-kind')) === 'idle'
    && JSON.stringify(await page.$$eval('#pcoord-goal .chip', (els) => els.map((e) => e.textContent.trim()))) === '["CI on #1","review on #42","new issues","your message"]'
    && (await text(page, '#pcoord-goal')).includes('Goal: Triage open bugs'));
  ok('…each with its tooltip', (await page.getAttribute('#pcoord-goal .chip[data-kind="ci"]', 'data-tip')).includes('when the checks on #1 finish'));
  ok('…no Stop', !(await page.$('#pcoord-stop')) && (await text(page, '#pcoord-state')) === 'idle');
  ok('…its cell says so', (await text(page, '.povc[data-cell="coordinator"]')).includes('waiting for: CI on #1'));
  ok('no page errors (idle)', errors.length === 0, errors.join(' | '));
}

// === a short tile: the page scrolls, the input ends the coordinator's section ======================================
{
  const { page, errors } = await open(seedWith(), { height: 900 });
  await page.waitForSelector('#pcoord-chat .msg');
  await page.fill('#msg', 'half a thought');
  await page.setViewportSize({ width: 1280, height: 520 });
  ok('a short tile: the page scrolls as one', await until(page, () => !document.getElementById('main').classList.contains('pov-fit') && document.getElementById('main').classList.contains('pov-on')
    && document.getElementById('timeline').scrollHeight > document.getElementById('timeline').clientHeight));
  ok('…the bar goes, the input ends the coordinator\'s section', await until(page, () => document.querySelector('.composer').hidden && !!document.querySelector('#pcoord > #pcoord-in #pcoord-msg')));
  ok('…with what you were writing', (await page.inputValue('#pcoord-msg')) === 'half a thought');
  ok('…the window 200 px', (await box(page, '#pcoord-chat')).h === 200);
  ok('…the board at its own height', await page.$eval('#pov-board', (e) => e.scrollHeight <= e.clientHeight + 1));
  await page.click('#pcoord-msg');
  await page.keyboard.press('End');
  await page.keyboard.type(', and one more');
  await page.keyboard.press('Enter');
  ok('…Enter sends it to the coordinator', await until(page, () => window.__more.texts.some((t) => t.text === 'half a thought, and one more')));
  ok('…and empties it', await until(page, () => document.getElementById('pcoord-msg').value === ''));
  await page.fill('#pcoord-msg', 'the other way');
  await page.setViewportSize({ width: 1280, height: 900 });
  ok('tall again: the bar, with it', await until(page, () => !document.querySelector('.composer').hidden && document.getElementById('msg').value === 'the other way' && !document.getElementById('pcoord-msg')));
  ok('no page errors (short)', errors.length === 0, errors.join(' | '));

  // 600 px or more, but not enough for the rest with a strip of two rows: it scrolls rather than clip the coordinator
  const { page: n, errors: ne } = await open(seedWith(), { width: 1100, height: 640 });
  await n.waitForSelector('#pcoord-chat .msg');
  ok('a tall tile too short for the rest: the page scrolls, nothing clipped', await until(n, () => !document.getElementById('main').classList.contains('pov-fit')
    && !!document.getElementById('pcoord-msg') && document.querySelector('.composer').hidden && document.getElementById('pcoord-chat').offsetHeight === 200));
  await n.setViewportSize({ width: 1600, height: 640 });
  ok('…wide enough for a strip of one row: it fits', await until(n, () => document.getElementById('main').classList.contains('pov-fit') && !document.getElementById('pcoord-msg')
    && document.getElementById('pcoord-chat').offsetHeight >= 100 && document.getElementById('pov-board').clientHeight >= 119
    && document.getElementById('pcoord').getBoundingClientRect().bottom <= document.querySelector('.composer').getBoundingClientRect().top + 1));
  ok('no page errors (too short)', ne.length === 0, ne.join(' | '));
}

// === an older backend: the page as before ==========================================================================
{
  const { page, errors } = await open(seedWith({ noOverview: true }));
  await page.waitForSelector('#pcoord-prev');
  await page.waitForTimeout(200);
  ok('an older backend: no strip, no Status, the Board tab', !(await page.$('#pov-strip')) && !(await page.$('#pov-fold')) && (await text(page, '[data-tab="board"]')) === 'Board');
  ok('…the coordinator card above the board, its small chat', await page.$eval('#pcoord', (e) => !!(e.compareDocumentPosition(document.querySelector('.pboard')) & Node.DOCUMENT_POSITION_FOLLOWING))
    && (await page.$$('#pcoord-prev .msg')).length === 3);
  ok('…no overview layout, cards as they were', await page.evaluate(() => !document.getElementById('main').classList.contains('pov-on') && !document.querySelector('.ptagent')
    && document.querySelector('.ptask[data-n="2"]').title === 'open its conversation'));
  ok('…the bar writes to the coordinator', !(await page.$eval('.composer', (e) => e.hidden)) && (await page.getAttribute('#msg', 'placeholder')).startsWith('Message the coordinator'));
  ok('…the overview asked once', JSON.stringify(await page.evaluate(() => window.__more.overviewReads)) === '[]'
    && (await calls(page, 'GET', '/projects/7/overview')).length === 1);
  ok('no page errors (older backend)', errors.length === 0, errors.join(' | '));
}

// === the status as it came: scm text plain, a link only when https; a read that failed ======================
{
  const o = overviewSeed(7, 'day');
  o.scm.provider.title = '<img src=x onerror="window.__pwned=1">Hub';
  o.scm.repos[1] = { ...o.scm.repos[1], url: 'javascript:alert(1)', checks: { state: 'failure', url: 'http://evil.example/x' } };
  const { page, errors } = await open(seedWith({ overview: { 7: o } }));
  await page.waitForSelector('.povc[data-cell="scm"]');
  ok('the provider\'s title is text', (await page.textContent('.povc[data-cell="scm"] h4')).includes('<img src=x') && !(await page.$('.povc[data-cell="scm"] img')));
  ok('…a repo\'s link only when https', !(await page.$('.repo[data-repo="acme/api"] a')) && !!(await page.$('.repo[data-repo="acme/web"] a')));
  ok('…nothing ran', await page.evaluate(() => window.__pwned === undefined));
  ok('no page errors (plain)', errors.length === 0, errors.join(' | '));

  // the first read fails (then the stub answers): the strip's place says so, Retry reads it
  const { page: f, errors: fe } = await open(seedWith(), { init: () => { let n = 1; window.__route('GET', /\/projects\/7\/overview/, () => (n-- > 0 ? window.__json({ error: 'the database is busy' }, 500) : null)); } });
  ok('a failed read: said in the strip\'s place', await until(f, () => /Couldn't read the project's status: the database is busy/.test(document.getElementById('pov-strip')?.textContent || '')));
  ok('…the board and the coordinator still there', !!(await f.$('#pov-board .ptask')) && !!(await f.$('#pcoord')));
  await f.click('#pov-retry');
  ok('…Retry reads it', await until(f, () => !!document.querySelector('.povc[data-cell="sandbox"]')));
  ok('no page errors (failed read)', fe.length === 0, fe.join(' | '));
}

// === a phone ======================================================================================================
{
  const { page, errors } = await open(seedWith(), { width: 390, height: 844, touch: true });
  await page.waitForSelector('#pov-strip');
  await page.waitForSelector('#pcoord-chat .msg');
  ok('a phone: no sideways page scroll', await noHScroll(page));
  ok('…the strip two cells a row', await page.$eval('#pov-strip', (e) => getComputedStyle(e).gridTemplateColumns.split(' ').length === 2));
  ok('…the board scrolls sideways', await page.$eval('#pov-board', (e) => e.scrollWidth > e.clientWidth));
  await page.tap('.povc[data-cell="tokens"] .big');
  ok('…a tap shows a tooltip', (await tip(page))?.title === 'Tokens');
  await page.tap('.povc[data-cell="tokens"] .big');
  ok('…the next tap hides it', (await tip(page)) === null);
  ok('…inside the window', await (async () => {
    await page.tap('.povc[data-cell="sandbox"] h4');
    return page.evaluate(() => { const r = document.querySelector('.bxtip.on').getBoundingClientRect(); return r.right <= innerWidth && r.left >= 0; });
  })());
  ok('no page errors (phone)', errors.length === 0, errors.join(' | '));
}

await browser.close();
done('projects-overview');
