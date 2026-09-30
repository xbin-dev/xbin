// chat.mjs — the chat of a run, driven by the live stream.
//
// Runs the real tile (agent.js + chat-*.js) against backend.mjs's fake
// backend and pushes the events the real backend streams. What it pins:
// thinking streams and folds; a tool call is one card headed by the model's
// summary; a card you opened STAYS open while results and tokens arrive; a
// subagent renders inside its parent's session — its own tools and text,
// live — and never in the sidebar; a message sent while the run works is a
// removable queued chip; Stop gives queued text back; a subagent's approval
// can be given from the parent's session.
//
//   node test/chat.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Math.floor(Date.now() / 1000);
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: extra.runId || 1, seq: id, role, content, created: now - 60 + id, ...extra });

const seed = {
  runs: [
    { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1, kind: '', updated: now },
    { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1, kind: '', updated: now },
  ],
  views: {
    1: {
      run: { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1 },
      messages: [
        msg(1, 'user', 'plan it'),
        msg(2, 'assistant', '', {
          reasoning: 'weighing the options', reasoningMs: 2100,
          toolCalls: [
            call('c1', 'xbin_call', { method: 'GET', path: '/api/apps/books/invoices', summary: 'Look up open invoices' }),
            call('c2', 'file_read', { path: 'notes.md' }),
            call('s1', 'subagent_spawn', { task: 'research vendor prices', label: 'research' }),
          ],
        }),
        msg(3, 'tool', 'HTTP 200\n[3 invoices]', { toolCallId: 'c1', name: 'xbin_call' }),
        msg(4, 'tool', '(running…)', { toolCallId: 'c2', name: 'file_read' }),
        msg(5, 'tool', '(waiting for subagent #2…)', { toolCallId: 's1', name: 'subagent_spawn' }),
      ],
      links: [{ id: 1, parentId: 1, childId: 2, toolCallId: 's1', mode: 'fg', state: 'running', label: 'research', phase: 'working',
        child: { id: 2, title: 'research', status: 'running', parentId: 1, llmCalls: 1 } }],
    },
    2: {
      run: { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 },
      chain: [{ id: 1, title: 'plan the quarter' }],
      messages: [
        msg(1, 'system', 'sys', { runId: 2 }), msg(2, 'user', 'research vendor prices', { runId: 2 }),
        msg(3, 'assistant', '', { runId: 2, toolCalls: [call('k1', 'web_fetch', { url: 'https://x', summary: 'Fetch the price list' })] }),
        msg(4, 'tool', 'prices…', { runId: 2, toolCallId: 'k1', name: 'web_fetch' }),
      ],
    },
  },
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx, { realMarked: true }); // the finish line's markdown
await ctx.addInitScript(STUB, seed);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.setViewportSize({ width: 900, height: 900 });
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#runs .run');
const push = (ev) => page.evaluate((e) => window.__push(e), ev);

ok('the sidebar lists the top-level run only', (await page.$$eval('#runs .run', (els) => els.map((e) => e.textContent))).join('|').includes('plan the quarter')
  && (await page.$$('#runs .run')).length === 1);
await page.click('#runs .run');
await page.waitForSelector('.tcard');
await page.waitForFunction(() => window.__streams() > 0);

ok('finished thinking folds to "Thought for 2s"', (await page.textContent('.think .th')).includes('Thought for 2s'));
ok('…and is closed', (await page.$('.think .tb')) === null);
const heads = await page.$$eval('.acard, .tcard', (els) => els.map((e) => e.querySelector('.hl').textContent));
ok('a tool card is headed by the model\'s summary', heads.includes('Look up open invoices'), heads.join(' | '));
ok('…or by a reading of its arguments', heads.includes('Read notes.md'), heads.join(' | '));
ok('a running call shows it', !!(await page.$('.tcard.running .spin')));
ok('a subagent is a card in the session, open while it works', !!(await page.$('.acard.running.on')));
await page.waitForSelector('.acard .acb .tcard .hl', { timeout: 5000 }).catch(() => {}); // its view is read once the card is open
const nested = await page.$$eval('.acard .acb .tcard .hl', (els) => els.map((e) => e.textContent));
ok('…with its own tool calls inside', nested.includes('Fetch the price list'), nested.join(' | '));
ok('…and its task on the card, not as a message', (await page.textContent('.acard .task')).includes('research vendor prices'));

// Thinking streams in live.
await push({ type: 'thinking', run: 1, root: 1, data: { text: 'considering the budget', started: Date.now() } });
await page.waitForSelector('.think.live');
ok('streamed thinking shows live and open', (await page.textContent('.think.live')).includes('considering the budget'));

// Open a card, then let results and tokens arrive: it stays open.
await page.click('.tcard:has-text("Look up open invoices") .tch');
await page.waitForSelector('.tcard.on');
await push({ type: 'message', run: 1, root: 1, data: msg(4, 'tool', '# notes\nq3 plan', { toolCallId: 'c2', name: 'file_read' }) });
await push({ type: 'text', run: 1, root: 1, data: { text: 'Here is the plan' } });
await page.waitForSelector('.msg.assistant.live');
ok('the settled result updates its card', !(await page.$('.tcard.running')));
ok('the card you opened stays open', (await page.$$eval('.tcard.on .hl', (els) => els.map((e) => e.textContent))).includes('Look up open invoices'));
ok('the answer streams as a draft', (await page.textContent('.msg.assistant.live')).includes('Here is the plan'));
ok('thinking folded when the answer started', !(await page.$('.think.live')));

// The subagent's own activity arrives on the parent's stream.
await push({ type: 'message', run: 2, root: 1, data: msg(5, 'assistant', 'CHILD SAYS HI', { runId: 2 }) });
await page.waitForFunction(() => document.querySelector('.acard .acb')?.textContent.includes('CHILD SAYS HI'));
ok('a subagent\'s text shows inside its card', true);
await push({ type: 'run', run: 2, root: 1, data: { id: 2, title: 'research', status: 'running', parentId: 1, rootId: 1 } });
await page.waitForTimeout(100);
ok('a subagent never appears in the sidebar', (await page.$$('#runs .run')).length === 1);

// The pinned task (D133): the current (latest) request on a line under the
// top bar's controls; unfolded, the whole ledger (GET /runs/1/asks) — read-only.
await page.evaluate(() => window.__route('GET', /\/runs\/1\/asks$/, () => window.__json({ asks: [
  { id: 1, seq: 1, source: 'human', who: 'alice', text: 'plan it\nwith the whole team', at: 1700000000, live: false },
  { id: 2, seq: 9, source: 'schedule', who: 'nudge', text: 'check the budget', at: 1700000100, live: true }] })));
await push({ type: 'run', run: 1, root: 1, data: { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1,
  task: { count: 1, first: { seq: 1, source: 'human', who: 'alice', text: 'plan it\nwith the whole team' } } } });
await page.waitForSelector('.taskpin .taskline');
ok('one request: it is the pinned line', (await page.textContent('.taskpin .taskline')) === 'plan it with the whole team'
  && !(await page.textContent('.taskpin .tasktoggle')).includes('+'), await page.textContent('.taskpin'));
await push({ type: 'run', run: 1, root: 1, data: { id: 1, title: 'plan the quarter', status: 'running', parentId: 0, rootId: 1,
  task: { count: 2, first: { seq: 1, source: 'human', who: 'alice', text: 'plan it\nwith the whole team' },
    latest: { seq: 9, source: 'schedule', who: 'nudge', text: 'check the budget' } } } });
await page.waitForFunction(() => document.querySelector('.taskpin .taskline')?.textContent === 'check the budget');
ok('the task is pinned under the top bar, on one line: the latest request, not the first', (await page.textContent('.taskpin .taskline')) === 'check the budget'
  && (await page.textContent('.taskpin .tasktoggle')).includes('+1'), await page.textContent('.taskpin'));
await page.click('.taskpin .tasktoggle');
await page.waitForSelector('.taskpin .taskreq:nth-child(2)');
const asks = await page.$$eval('.taskpin .taskreq', (els) => els.map((e) => e.textContent));
ok('unfolded: every request, verbatim, who sent it, and what was compacted', asks.length === 2 && asks[0].includes('with the whole team')
  && asks[0].includes('compacted') && asks[1].includes('schedule nudge') && !asks[1].includes('compacted'), asks.join(' | '));
ok('…with nothing to edit', !(await page.$('.taskpin input, .taskpin textarea, .taskpin [contenteditable]')));
await page.click('.taskpin .tasktoggle');
await page.waitForSelector('.taskpin .taskline');
ok('…and it folds again', true);

// A message while the run works is queued, removable until delivered.
await page.fill('#msg', 'and also check Q4');
await page.press('#msg', 'Enter');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/1/message')));
const post = await page.evaluate(() => JSON.parse(window.__calls.find((c) => c.url.endsWith('/runs/1/message')).body));
ok('the post carries a client id (retries dedupe)', !!post.clientId, JSON.stringify(post));
await push({ type: 'inbox', run: 1, root: 1, data: { queued: [{ id: 5, text: 'and also check Q4' }] } });
await page.waitForSelector('.qchip');
ok('a queued message shows above the composer', (await page.textContent('#queue')).includes('and also check Q4'));
await page.click('.qchip button');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/runs/1/inbox/5')));
ok('taking it back deletes it', await page.waitForFunction(() => !document.querySelector('.qchip'), null, { timeout: 2000 }).then(() => true, () => false));

// Stop returns what was still queued.
await page.evaluate(() => window.__route('POST', /\/runs\/1\/interrupt$/, () => window.__json({ ok: 'true', returned: [{ text: 'a queued thought' }] })));
ok('Stop shows while the run works', await page.isVisible('#stop'));
await page.click('#stop');
await page.waitForFunction(() => document.getElementById('msg').value.includes('a queued thought'));
ok('Stop puts queued text back in the composer', true);

// A subagent waiting for approval can be approved from the parent.
await push({ type: 'run', run: 2, root: 1, data: { id: 2, status: 'waiting_input', parentId: 1,
  pendingState: { kind: 'approval', toolCalls: [call('z', 'xbin_call', {})] } } });
await page.evaluate(() => window.__views[2].run.status = 'waiting_input');
await page.waitForSelector('.acard .ask.approve');
await page.click('.acard .ask.approve .btn:not(.ghost)');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/2/approve')));
ok('a subagent\'s approval is given from the parent\'s session', true);

// open ↗ shows the subagent's full session, with the way back.
await page.click('.acard .ach .lnk');
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'research');
ok('the breadcrumb leads back to the parent', (await page.textContent('.crumbs')).includes('plan the quarter'));
ok('the sidebar still highlights the root', (await page.textContent('#runs .run.on')).includes('plan the quarter'));

// finish's result is markdown (a model often puts its answer there); a
// render and a live page are buttons that show them again once closed.
await page.click('#top .crumbs a, .crumbs a').catch(() => {});
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'plan the quarter');
const stepEv = (id, kind, detail) => ({ type: 'step', run: 1, root: 1, data: { id, runId: 1, seq: id, kind, detail: JSON.stringify(detail), created: Math.floor(Date.now() / 1000) } });
await push(stepEv(901, 'finish', { result: 'All done — the plan is **ready**:\n\n- one\n- two' }));
await page.waitForSelector('.step.finish .md strong');
ok('the finish line renders its result as markdown', (await page.textContent('.step.finish .md strong')) === 'ready'
  && (await page.$$('.step.finish .md li')).length === 2, await page.innerHTML('.step.finish'));
await page.evaluate(() => window.__route('GET', /\/runs\/1\/file\?path=r\.html$/, () => window.__json({ path: 'r.html', content: '<p id="x">the report</p>', version: 1 })));
await push(stepEv(902, 'render', { path: 'r.html', version: 1, bytes: 26 }));
await page.waitForFunction(() => !document.getElementById('preview').hidden);
ok('a render opens the pane', (await page.textContent('#prev-path')) === 'r.html');
await page.click('#prev-close');
ok('…closed', await page.isHidden('#preview'));
const fileGets = () => page.evaluate(() => window.__calls.filter((c) => c.url.includes('/runs/1/file?path=r.html')).length);
await page.click('.step.render .steplnk');
await page.waitForFunction(() => !document.getElementById('preview').hidden);
ok('the 🖼 line brings the report back', (await page.textContent('#prev-path')) === 'r.html' && (await fileGets()) >= 1);
await page.click('#prev-close');
await page.evaluate(() => window.__route('POST', /\/api\/xbin\/path-tickets$/, () => window.__json({ url: '/api/~t1/', expires: Date.now() + 1e6 })));
await push(stepEv(903, 'live', { sandbox: 'sb-1', name: 'web', port: 8000, path: '/' }));
await page.waitForSelector('#livefr', { state: 'attached' });
await page.waitForFunction(() => document.getElementById('live-strip')?.dataset.tone);
ok('a live page opens with its status strip, which checked its URL', (await page.textContent('#live-strip .lsout')).startsWith('HTTP 404'), await page.textContent('#live-strip'));
ok('…and a page that answers an error is said, not shown blank', await page.evaluate(() => document.getElementById('livefr').hidden
  && !document.querySelector('.lsmsg').hidden && document.querySelector('.lsmsg').textContent.includes('didn\'t load')));
await page.click('#prev-close');
ok('…closed, the strip goes with it', !(await page.$('#live-strip')) && !(await page.$('#livefr')));
await page.click('.step.live .steplnk');
await page.waitForSelector('#livefr', { state: 'attached' });
ok('the 📡 line shows it live again', (await page.textContent('#prev-path')).includes('web:8000/'));
await page.click('#prev-close');

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('chat');
