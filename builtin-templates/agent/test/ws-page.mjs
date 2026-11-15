// ws-page.mjs — the workspace agent's own page (ws/page.js) against a
// stubbed backend in workspace mode (test/ws-stub.mjs). What it pins:
//
//   - boot.js loads the workspace page when GET /ws/state says the mode,
//     and nothing of the normal app;
//   - the empty state: what the agent can and can't do, starters from the
//     person's tiles; a starter fills the composer (the cursor in its
//     blank) and sends nothing;
//   - a conversation: the plan with what each step does, a brief, a request
//     that waits, a refusal of xbin's, the agent's question as choices,
//     two tile agents' cards (working, waiting for the person);
//   - every way out of the page is a message to the chrome with ids only —
//     Review… → xbin:agent-open-request {id}, Open/Answer → xbin:open-session
//     {session, tile} — and the page draws no grant: no Allow, Approve or
//     Deny anywhere, whatever waits;
//   - a request's card from waiting to settled; the brief edited and
//     started; a question answered; the context chip from the chrome sent
//     with the next message and shown above it;
//   - the session switcher: Main always there, new, rename, archive.
//
//   node test/ws-page.mjs        (needs playwright + a chromium build)
import { launch, checker } from './backend.mjs';
import { openWs, fresh, SESSIONS, FULL, EMPTY, CONVERSATION, CARDS, now } from './ws-stub.mjs';

const { ok, done } = checker();
const browser = await launch();
const calls = (page, method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)), [method, re.source]);
const posted = (page) => page.evaluate(() => window.__posted);
const text = (page, sel) => page.$eval(sel, (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
const until = async (f, ms = 4000) => { for (const t0 = Date.now(); Date.now() - t0 < ms;) { if (await f()) return true; await new Promise((r) => setTimeout(r, 40)); } return false; };
// no grant on the page: a button that would allow, approve or deny anything
const grants = (page) => page.evaluate(() => [...document.querySelectorAll('button')].map((b) => b.innerText.trim()).filter((t) => /^(allow|approve|deny|grant|resume)/i.test(t)));

// ---- workspace mode loads this page, and only it ---------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, { sessions: fresh(SESSIONS).slice(0, 1).map((s) => ({ ...s, status: 'idle', agents: 0, requests: 0 })), full: FULL, empty: EMPTY });
  ok('workspace mode: the workspace page, titled', !!(await page.$('#ws-page')) && (await page.title()) === 'Workspace agent');
  ok('…and nothing of the normal app', !(await page.$('.wrap')) && !(await page.$('#msg')));
  const asked = await page.evaluate(() => window.__calls.map((c) => c.url.replace(/^\/api\/xbin\/wsagent/, '')));
  ok('…it never calls a route this mode doesn\'t serve', asked.every((u) => /^\/(ws\/|runs\/\d+\/(view|message|interrupt|inbox)|stream)|^\/api\/xbin\/agents\/connection/.test(u)), asked.join(' '));
  // the empty state
  const empty = await text(page, '.wsp-empty');
  ok('empty state: the heading and the line', empty.includes('Ask for an outcome') && empty.includes('asks you before anything changes'), empty);
  ok('…what it can and can\'t do, in this session\'s numbers', empty.includes('Look across your 3 connected tiles') && empty.includes('See the 3 tiles that aren’t connected'), empty);
  ok('…starters from the person\'s tiles, each with its kind', (await page.$$eval('.wsp-starter', (b) => b.map((x) => x.dataset.kind))).join() === 'Fix,Ask,Change,New');
  ok('…the hint under the composer before the first message', (await text(page, '.wsp-hint')).includes('how you’ll know it’s done'));
  await page.click('.wsp-starter[data-kind="Change"]');
  await page.waitForTimeout(150);
  const ta = await page.$eval('bx-chat-composer textarea', (t) => ({ v: t.value, s: t.selectionStart, e: t.selectionEnd, focus: document.activeElement === t }));
  ok('a starter fills the composer, the cursor on its blank', ta.v === 'Add ___ to Invoices' && ta.s === 4 && ta.e === 7 && ta.focus, JSON.stringify(ta));
  ok('…and sends nothing', (await calls(page, 'POST', /\/message$/)).length === 0);
  ok('…the composer has a name for screen readers', (await page.$eval('bx-chat-composer textarea', (t) => t.getAttribute('aria-label'))) === 'Message to your workspace agent');
  ok('no page errors (empty state)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- a conversation with a plan, a brief, a request, a refusal, a question and tile agents --------------
{
  const seed = { sessions: fresh(SESSIONS), full: FULL, empty: EMPTY, views: { 1: { ...fresh(CONVERSATION), run: { status: 'running' } } }, cards: { 1: fresh(CARDS) } };
  const { ctx, page, errors } = await openWs(browser, seed);
  const plan = await page.$eval('.bxc-plan', (e) => ({ title: e.querySelector('.bxc-h span').textContent, tags: [...e.querySelectorAll('.bxc-ptag')].map((t) => t.textContent.trim()), meta: e.querySelector('.bxc-pmeta')?.textContent || '', numbered: !!e.querySelector('ol') })).catch(() => null);
  ok('the plan: numbered, each step with what it does or where it stands', plan && plan.numbered && plan.tags.join('|') === 'done|working|needs your OK|reads only', JSON.stringify(plan));
  ok('…and when the person said go ahead', plan && /^you said go ahead · /.test(plan.meta), JSON.stringify(plan));
  ok('…drawn once, in place of the plan call', (await page.$$('.bxc-plan')).length === 1);
  const brief = await text(page, '.wsc-brief');
  ok('the brief: the four fields in its card', ['GOAL', 'DONE WHEN', 'LEAVE ALONE', 'CHECK BY'].every((w) => brief.toUpperCase().includes(w)) && brief.includes('Export open orders'), brief);
  const req = await page.$eval('.wsc-req[data-req="12"]', (e) => ({ t: e.innerText, btns: [...e.querySelectorAll('button')].map((b) => b.innerText.trim()) })).catch(() => null);
  ok('a request that waits: what, for which tile, how long it has, one Review…', req && req.t.includes('Waiting for your approval') && req.t.includes('Invoices') && /expires in/.test(req.t) && req.btns.join() === 'Review…', JSON.stringify(req));
  ok('…in place of the call that filed it', (await page.$$eval('.bxc-row', (rows) => rows.filter((r) => r.querySelector('.wsc-req[data-req="12"]')).length)) === 1);
  const ref = await text(page, '[data-refusal="policy"]');
  ok('xbin\'s refusal: the org\'s sentence, the rule, two ways forward', ref.includes('Finance allows private models only') && ref.includes('not something the agent can work around') && ref.includes('See what a private model connects') && ref.includes('Open scope'), ref);
  const agents = await page.$$eval('.wsc-agent', (cs) => cs.map((c) => ({ tile: c.dataset.tile, t: c.innerText, btn: c.querySelector('button').innerText.trim() })));
  ok('two tile agents: tile, coding agent, status as glyph and word, last line', agents.length === 2 && /Orders/.test(agents[0].t) && /Claude Code/.test(agents[0].t) && /working \d+:\d\d/.test(agents[0].t) && /3 of 5 cases pass/.test(agents[0].t), JSON.stringify(agents));
  ok('…the one that waits says so, and only the person can answer: Answer', agents[1] && /waiting for you/.test(agents[1].t) && /Only you can answer/.test(agents[1].t) && agents[1].btn === 'Answer', JSON.stringify(agents[1]));
  ok('…the other: Open', agents[0] && agents[0].btn === 'Open');
  const ask = await page.$eval('.bxc-ask', (e) => ({ opts: [...e.querySelectorAll('.bxc-opt')].map((o) => o.innerText.trim()), other: !!e.querySelector('.bxc-other'), btns: [...e.querySelectorAll('button')].map((b) => b.innerText.trim()) })).catch(() => null);
  ok('the agent\'s question: its choices as rows, "something else", Answer and Skip', ask && ask.opts.length === 2 && ask.other && ask.btns.join('|') === 'Answer|Skip — let the agent look around first', JSON.stringify(ask));
  ok('…the journal\'s "asked: …" line is not drawn beside it', !(await page.$$eval('.bxc-notice', (n) => n.some((x) => /^asked:/.test(x.innerText.trim())))));
  const about = await page.$eval('.bxc-user .wsp-about', (e) => e.innerText).catch(() => '');
  ok('a message sent about a tile shows its chip above the words', about.includes('About') && about.includes('Orders'), about);
  ok('the composer says a question waits', (await page.$eval('bx-chat-composer textarea', (t) => t.placeholder)) === 'Or type your answer…');
  ok('NO GRANT on the page', (await grants(page)).length === 0, (await grants(page)).join(', '));

  // the ways out are messages with ids
  await page.click('.wsc-req[data-req="12"] button');
  await page.click('.wsc-agent[data-agent="8"] button');
  await page.click('.wsc-agent[data-agent="7"] button');
  await page.waitForTimeout(100);
  const out = await posted(page);
  ok('Review… asks the chrome to open the request by its id, nothing else', JSON.stringify(out[0]) === JSON.stringify({ type: 'xbin:agent-open-request', id: 'rq-12' }), JSON.stringify(out));
  ok('Answer and Open ask to show the tile agent\'s session', JSON.stringify(out.slice(1)) === JSON.stringify([{ type: 'xbin:open-session', session: 'ts-8', tile: 'sales/invoices' }, { type: 'xbin:open-session', session: 'ts-7', tile: 'sales/orders' }]), JSON.stringify(out));

  // the question answered: a choice, then something else
  await page.click('.bxc-ask .bxc-opt >> nth=0');
  await page.click('.bxc-ask button[data-answer="accept"]');
  await until(async () => (await calls(page, 'POST', /\/runs\/1\/message$/)).length === 1);
  const sent = (await calls(page, 'POST', /\/runs\/1\/message$/)).map((c) => JSON.parse(c.body).text);
  ok('a choice answers with its words, as the person\'s message', sent[0] === 'The three with overdue invoices', JSON.stringify(sent));

  // the request settles: a run event says the cards changed
  await page.evaluate(() => {
    const q = window.__seed.cards[1].requests.find((x) => x.id === 12);
    Object.assign(q, { state: 'used', decidedMs: Date.now(), outcome: 'approved by your person — it ran: 200 OK · 38 links rebuilt' });
    window.__push({ type: 'run', run: 1, data: { id: 1, status: 'running' } });
  });
  await until(async () => (await text(page, '.wsc-req[data-req="12"]')).includes('Allowed once by you'));
  const settled = await text(page, '.wsc-req[data-req="12"]');
  ok('settled: who allowed it, what ran, the result — and no button', settled.includes('Allowed once by you') && settled.includes('POST /links/rebuild') && settled.includes('38 links rebuilt')
    && (await page.$$('.wsc-req[data-req="12"] button')).length === 0, settled);
  ok('…said once to a screen reader', /Rebuild the order links.*Allowed once by you/.test(await page.evaluate(() => window.wsAgentPage.testApi().said)), await page.evaluate(() => window.wsAgentPage.testApi().said));
  // denied, expired, withdrawn
  for (const [state, words] of [['denied', 'Denied by you'], ['expired', 'Expired — not approved within 24 hours.'], ['withdrawn', 'Withdrawn by the agent']]) {
    await page.evaluate((st) => { window.__seed.cards[1].requests.find((x) => x.id === 12).state = st; window.__push({ type: 'run', run: 1, data: { id: 1 } }); }, state);
    ok(`settled ${state}: "${words}"`, await until(async () => (await text(page, '.wsc-req[data-req="12"]')).includes(words)));
  }
  ok('NO GRANT on the page, after it all', (await grants(page)).length === 0);
  ok('no page errors (conversation)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- the brief: edit, the coding agent, start ------------------------------------------------------
{
  const cards = fresh(CARDS);
  cards.briefs[0].state = 'draft';
  cards.requests = cards.requests.filter((q) => q.id !== 11);
  cards.agents = [];
  const sd = { harness: 'claude', harnessName: 'Claude Code', asks: true, ticket: true, line: 'Claude Code · asks before acting',
    defaults: { harness: 'claude', harnesses: [{ id: 'claude', name: 'Claude Code', asks: true, needsTicket: true }, { id: 'codex', name: 'Codex', asks: true, needsTicket: true }] } };
  const { ctx, page, errors } = await openWs(browser, { sessions: fresh(SESSIONS), full: FULL, views: { 1: fresh(CONVERSATION) }, cards: { 1: cards }, startDefaults: sd });
  await until(async () => (await text(page, '.wsc-brief')).includes('Claude Code · asks before acting'));
  const b = await text(page, '.wsc-brief');
  ok('the brief says what it will run on, from xbind', b.includes('Claude Code · asks before acting'), b);
  ok('…that it is what the tile agent is told first, and later messages aren\'t approved one by one', b.includes('told first') && b.includes('isn’t approved message by message'), b);
  ok('…one button: Review and start…', (await page.$eval('.wsc-start', (x) => x.innerText.trim())) === 'Review and start…');
  ok('…the allowed coding agents to pick from', (await page.$$eval('.wsc-brief select option', (o) => o.map((x) => x.textContent))).join() === 'Claude Code,Codex');
  await page.selectOption('.wsc-brief select', 'codex');
  await until(async () => (await calls(page, 'PUT', /\/briefs\/4$/)).length === 1);
  ok('picking one is the brief\'s harness', JSON.parse((await calls(page, 'PUT', /\/briefs\/4$/))[0].body).harness === 'codex');
  await page.click('.wsc-edit');
  await page.fill('.wsc-field[data-field="goal"] textarea', 'Add a CSV export of open orders.');
  await page.click('.wsc-brief button.primary');
  await until(async () => (await calls(page, 'PUT', /\/briefs\/4$/)).length === 2);
  const put = JSON.parse((await calls(page, 'PUT', /\/briefs\/4$/))[1].body);
  ok('Edit turns the fields into text in place; Save sends them', put.goal === 'Add a CSV export of open orders.' && put.doneWhen.startsWith('A CSV'), JSON.stringify(put));
  await until(async () => (await text(page, '.wsc-brief')).includes('Add a CSV export of open orders.'));
  await page.click('.wsc-start');
  await until(async () => (await posted(page)).length > 0);
  ok('Review and start… files the start and asks the chrome to open it', JSON.stringify((await posted(page))[0]) === JSON.stringify({ type: 'xbin:agent-open-request', id: 'rq-brief-4' }), JSON.stringify(await posted(page)));
  ok('NO GRANT on the page (brief)', (await grants(page)).length === 0);
  ok('no page errors (brief)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}
{
  // where the person's preferences let tile agents start without asking: Start, and the click is the confirmation
  const cards = fresh(CARDS);
  cards.briefs[0].state = 'draft';
  cards.requests = [];
  cards.agents = [];
  const sd = { harness: 'claude', harnessName: 'Claude Code', asks: true, ticket: false, line: 'Claude Code · asks before acting', defaults: { harness: 'claude', harnesses: [{ id: 'claude', name: 'Claude Code', asks: true, needsTicket: false }] } };
  const { ctx, page } = await openWs(browser, { sessions: fresh(SESSIONS), full: FULL, views: { 1: fresh(CONVERSATION) }, cards: { 1: cards }, startDefaults: sd, startAnswer: { started: true, id: 5, outcome: '' } });
  await until(async () => (await page.$eval('.wsc-start', (x) => x.innerText.trim()).catch(() => '')) === 'Start');
  ok('start without asking: the button reads Start', (await page.$eval('.wsc-start', (x) => x.innerText.trim())) === 'Start');
  await page.click('.wsc-start');
  await until(async () => (await calls(page, 'POST', /\/briefs\/4\/start$/)).length === 1);
  ok('…the click starts it, and nothing is sent to the chrome', (await calls(page, 'POST', /\/briefs\/4\/start$/)).length === 1 && (await posted(page)).length === 0);
  await ctx.close();
}

// ---- the context chip from the chrome --------------------------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, { sessions: fresh(SESSIONS).slice(0, 1).map((s) => ({ ...s, status: 'idle' })), full: FULL, empty: EMPTY });
  await page.evaluate(() => window.postMessage({ type: 'xbin:agent-context', tile: 'sales/orders' }, '*'));
  await until(async () => !!(await page.$('.wsp-ctx')));
  ok('xbin:agent-context {tile}: the chip "About Orders", by the tile\'s name', (await text(page, '.wsp-ctx')).includes('About Orders'));
  await page.evaluate(() => window.postMessage({ type: 'xbin:agent-context', tile: 'finance/ledger' }, '*'));
  await until(async () => (await text(page, '.wsp-ctx')).includes('Ledger'));
  ok('a tile that isn\'t connected: the chip says so, and why', /About Ledger — not connected \(Finance allows private models only\)/.test(await text(page, '.wsp-ctx')), await text(page, '.wsp-ctx'));
  await page.click('.wsp-ctx button.wsp-about');
  ok('…a click on it opens the scope panel', await until(async () => !!(await page.$('.wsp-scope'))));
  await page.keyboard.press('Escape');
  await page.evaluate(() => window.postMessage({ type: 'xbin:agent-context', tile: 'sales/orders' }, '*'));
  await until(async () => (await text(page, '.wsp-ctx')).includes('Orders'));
  await page.fill('bx-chat-composer textarea', 'Add a CSV export');
  await page.keyboard.press('Enter');
  await until(async () => (await calls(page, 'POST', /\/message$/)).length === 1);
  const body = JSON.parse((await calls(page, 'POST', /\/message$/))[0].body);
  ok('the chip goes with the next message: the tile\'s name and path, then the words', body.text === 'About tile "Orders" (sales/orders):\nAdd a CSV export', JSON.stringify(body));
  await page.waitForTimeout(100);
  ok('…and only that one: it is gone after', !(await page.$('.wsp-ctx')));
  await page.evaluate(() => window.postMessage({ type: 'xbin:agent-context', screen: 'Sales desk' }, '*'));
  await until(async () => !!(await page.$('.wsp-ctx')));
  ok('xbin:agent-context {screen}: "About the Sales desk screen"', (await text(page, '.wsp-ctx')).replace(/\s+/g, ' ').includes('About the Sales desk screen'), await text(page, '.wsp-ctx'));
  await page.click('.wsp-ctx .x');
  ok('…removable', !(await page.$('.wsp-ctx')));
  // a message from anyone but the framing window is ignored (at the top level the page is its own parent:
  // a message whose source is another window — an iframe's — is not the chrome's)
  await page.evaluate(() => new Promise((r) => { const f = document.createElement('iframe'); f.srcdoc = '<script>parent.postMessage({type:"xbin:agent-context",tile:"x/evil"},"*")<\/script>'; f.onload = () => setTimeout(r, 100); document.body.append(f); }));
  ok('a context message from another window is ignored', (await page.evaluate(() => window.wsAgentPage.testApi().context)) === null, await page.evaluate(() => JSON.stringify(window.wsAgentPage.testApi().context)));
  ok('no page errors (context)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

// ---- sessions ----------------------------------------------------------------------------------
{
  const { ctx, page, errors } = await openWs(browser, { sessions: fresh(SESSIONS), full: FULL, empty: EMPTY, views: { 1: fresh(CONVERSATION) }, cards: { 1: fresh(CARDS) } });
  ok('the strip names the session in view', (await text(page, '.wsp-sess')).trim() === 'Main');
  await page.click('.wsp-sess');
  const rows = await page.$$eval('.wsp-sessions > .wsp-slist > .wsp-srow', (r) => r.map((x) => x.innerText.replace(/\s+/g, ' ').trim()));
  ok('the switcher: Main first and always here, then the others with scope, model and status', rows.length === 2 && /^Main/.test(rows[0]) && rows[0].includes('always here') && rows[0].includes('3 tiles · 2 orgs · big') && rows[0].includes('waiting for you')
    && rows[1].includes('Month-end') && rows[1].includes('waiting for you') && rows[1].includes('5 tiles · 3 orgs · llama'), JSON.stringify(rows));
  ok('…archived ones fold under Older', (await text(page, '.wsp-older summary')).includes('Older (1)'));
  ok('…Main can\'t be archived', await page.$eval('.wsp-sacts button:nth-child(3)', (b) => b.disabled));
  ok('…the foot says what a new session is', (await text(page, '.wsp-sessions .wsp-foot')).includes('starts empty, with its own scope and model'));
  await page.click('.wsp-sacts button:nth-child(2)');
  await page.fill('.wsp-rename', 'Main desk');
  await page.keyboard.press('Enter');
  await until(async () => (await calls(page, 'PATCH', /\/ws\/sessions\/1$/)).length === 1);
  ok('Rename in place', JSON.parse((await calls(page, 'PATCH', /\/ws\/sessions\/1$/))[0].body).name === 'Main desk');
  await page.click('.wsp-srow[data-session="2"] button');
  await until(async () => (await page.evaluate(() => window.wsAgentPage.testApi().sel)) === 2);
  ok('picking a session shows it', (await page.evaluate(() => window.wsAgentPage.testApi().sel)) === 2 && (await calls(page, 'GET', /\/runs\/2\/view/)).length > 0);
  await page.click('.wsp-sess');
  await page.click('.wsp-sacts button:nth-child(3)');
  await until(async () => (await calls(page, 'PATCH', /\/ws\/sessions\/2$/)).length === 1);
  ok('Archive the one in view: back on Main', JSON.parse((await calls(page, 'PATCH', /\/ws\/sessions\/2$/))[0].body).archived === true
    && await until(async () => (await page.evaluate(() => window.wsAgentPage.testApi().sel)) === 1));
  await page.click('.wsp-sess');
  await page.click('.wsp-sacts button:nth-child(1)');
  await until(async () => (await calls(page, 'POST', /\/ws\/sessions$/)).length >= 1);
  const made = (await calls(page, 'POST', /\/ws\/sessions$/)).map((c) => JSON.parse(c.body)).pop();
  ok('New session… asks nothing up front: it copies the scope and model of the one in view', made.from === 1 && !made.name, JSON.stringify(made));
  ok('…and opens on its empty state', await until(async () => !!(await page.$('.wsp-empty'))));
  ok('no page errors (sessions)', errors.length === 0, errors.join(' | '));
  await ctx.close();
}

await browser.close();
done('ws-page');
