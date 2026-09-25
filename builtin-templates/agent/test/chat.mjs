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
await serveTile(ctx);
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

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('chat');
