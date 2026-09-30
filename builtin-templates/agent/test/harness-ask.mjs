// harness-ask.mjs — a coding harness asking and driven, on the web (D-harness
// §8 U4: harness-ask.js, harness-controls.js) over the fixtures
// (test/harness-fixtures.mjs) and the STUB's §4 routes: a permission card
// with the harness's own options (reject first when it defaults to no, the
// rule note, a diff preview, feedback with a rejection), a plan approval
// (the plan, a bypass option marked and confirmed, "keep planning" sent as
// feedback), a question (a form: required, Submit, Skip; url mode: the page,
// Done), #hctl (the mode — a bypass one confirmed — and options PATCHed,
// your Auto / Always approve PUT, at home too), the slash menu, and steering
// (the placeholder, the queued chip, ⌘/Ctrl+Enter's {interrupt: true}, a
// steered message said).
//
//   node test/harness-ask.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, harnessSeed());
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
let dialogs = 'accept';
page.on('dialog', (d) => (dialogs === 'accept' ? d.accept() : d.dismiss()));
const go = async (id, sel) => { await page.evaluate((h) => { location.hash = h; }, '#c=' + id); await page.waitForSelector(sel); };
const calls = (method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url))
  .map((c) => ({ url: c.url, body: c.body ? JSON.parse(c.body) : null })), [method, re]);
const last = async (method, re) => (await calls(method, re)).pop() || null;
const waitCall = (method, re, n) => page.waitForFunction(([m, s, k]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)).length >= k,
  [method, re, n], { timeout: 5000 });
// park: re-park a run as the backend would (the stub's run and its event)
const park = (id, pendingState, extra = {}) => page.evaluate(([id, ps, x]) => {
  const r = window.__views[id].run;
  Object.assign(r, { status: 'waiting_input', pendingState: ps, ...x });
  window.__push({ type: 'run', run: id, root: r.rootId || id, data: { id, status: 'waiting_input', pendingState: ps, harness: r.harness, ...x } });
}, [id, pendingState, extra]);
const opts = () => page.$$eval('.hask .hopts button', (els) => els.map((e) => e.dataset.opt));

await page.goto(`${ORIGIN}/#c=22`);
await page.waitForSelector('.hask');

// --- a permission request -----------------------------------------------------------------
ok('the harness\'s own options, in its order', JSON.stringify(await opts()) === '["allow","allow_always","reject"]', JSON.stringify(await opts()));
const card22 = await page.textContent('.hask');
ok('the call: who asks, its title and label', card22.includes('Claude Code asks to run a command') && card22.includes('go test ./...') && card22.includes('Run the tests'), card22);
ok('"always" says what it remembers', (await page.textContent('.hask .hrule')).includes('later execute calls titled ‘go test ./...’'));
ok('the built-in approval card is not drawn for it', !(await page.$('.ask.approve')));
await page.fill('.hask .hfb', 'run only the unit tests');
await page.click('.hask [data-opt="reject"]');
await waitCall('POST', '/runs/22/approve$', 1);
let b = (await last('POST', '/runs/22/approve$')).body;
ok('reject + feedback: {park, option, feedback}', b.park === 'Xq3perm' && b.option === 'reject' && b.feedback === 'run only the unit tests' && !('approve' in b), JSON.stringify(b));
await page.waitForFunction(() => !document.querySelector('.hask'));

// defaultToNo: reject first; an edit's diff previewed
const ps2 = await page.evaluate(() => window.__views[22].run);
await park(22, {
  kind: 'approval', park: 'Xq3perm2', toolCalls: [],
  harness: { callId: 'h2:toolu_03', defaultToNo: true, description: 'This edit touches a generated file.',
    options: [{ optionId: 'allow', name: 'Allow', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }],
    tool: { title: 'Edit client.go', kind: 'edit', content: [{ type: 'diff', path: '/work/api/client.go', oldText: 'a\nb\nc\nd', newText: 'a\nb\nC\nd' }] } } });
await page.waitForSelector('.hask[data-park="Xq3perm2"]');
ok('defaultToNo: reject first', JSON.stringify(await opts()) === '["reject","allow"]', JSON.stringify(await opts()));
const diff = await page.textContent('.hask .hdiff');
ok('the edit\'s diff previewed', diff.includes('/work/api/client.go') && diff.includes('-c') && diff.includes('+C') && diff.includes('+1') && diff.includes('−1'), diff);
ok('the adapter\'s description', (await page.textContent('.hask')).includes('touches a generated file'));
await page.click('.hask [data-opt="allow"]');
await waitCall('POST', '/runs/22/approve$', 2);
b = (await last('POST', '/runs/22/approve$')).body;
ok('allow: the option, no feedback', b.park === 'Xq3perm2' && b.option === 'allow' && !('feedback' in b), JSON.stringify(b));
ok('(the stub still holds the run)', !!ps2);

// --- a plan approval (a child's run, its owner) --------------------------------------------
await go(27, '.hask.hplan');
const plan = await page.textContent('.hask.hplan');
ok('the plan, its options', plan.includes('Backfill in batches of 1000') && plan.includes('Yes, and auto-accept edits') && plan.includes('No, keep planning'), plan);
ok('the bypass option is marked', (await page.textContent('.hask [data-opt="bypassPermissions"]')).startsWith('⚠') &&
  await page.$eval('.hask [data-opt="bypassPermissions"]', (e) => e.classList.contains('hwarn')));
dialogs = 'dismiss';
await page.click('.hask [data-opt="bypassPermissions"]');
await page.waitForTimeout(100);
ok('the bypass option asks first — dismissed, nothing is sent', (await calls('POST', '/runs/27/approve$')).length === 0);
dialogs = 'accept';
await page.fill('.hask textarea.hfb', 'use a transaction per batch');
await page.click('.hask [data-opt="plan"]');
await waitCall('POST', '/runs/27/approve$', 1);
b = (await last('POST', '/runs/27/approve$')).body;
ok('keep planning: the rejection with the feedback', b.park === 'Xq3plan' && b.option === 'plan' && b.feedback === 'use a transaction per batch', JSON.stringify(b));

// --- a question ----------------------------------------------------------------------------
await go(23, '.hask.hq');
ok('the question and its choices (enumNames)', (await page.textContent('.hask.hq')).includes('Which JSON library should I use?') &&
  (await page.textContent('.hask.hq [data-field="library"]')).includes('encoding/json (stdlib)'));
await page.click('.hask.hq .hopts .btn:not(.ghost)');
ok('a required field unanswered is said, nothing sent', (await page.textContent('.hask.hq .err')).includes('Library') && (await calls('POST', '/harness/answer$')).length === 0);
await page.check('.hask.hq [data-field="library"] input[data-i="1"]');
await page.click('.hask.hq .hopts .btn:not(.ghost)');
await waitCall('POST', '/runs/23/harness/answer$', 1);
b = (await last('POST', '/runs/23/harness/answer$')).body;
ok('submit: accept with the form\'s content', b.park === 'Xq3ask' && b.action === 'accept' && b.content.library === 'jsoniter', JSON.stringify(b));
await park(23, { kind: 'question', park: 'Xq3ask2', harness: { eid: 'e2', message: 'Anything else?', schema: { type: 'object', properties: { note: { type: 'string', title: 'Note' } } } } });
await page.waitForSelector('.hask[data-park="Xq3ask2"]');
await page.click('.hask.hq .hopts .btn.ghost');
await waitCall('POST', '/runs/23/harness/answer$', 2);
b = (await last('POST', '/runs/23/harness/answer$')).body;
ok('skip: decline', b.park === 'Xq3ask2' && b.action === 'decline' && !('content' in b), JSON.stringify(b));
await park(23, { kind: 'question', park: 'Xq3url', harness: { eid: 'e3', mode: 'url', url: 'https://example.invalid/verify', message: 'Verify your account' } });
await page.waitForSelector('.hask[data-park="Xq3url"]');
ok('url mode: the page as a link', (await page.getAttribute('.hask a.hurl', 'href')) === 'https://example.invalid/verify' &&
  (await page.textContent('.hask')).includes('Verify your account'));
await page.click('.hask.hq .hopts .btn:not(.ghost)');
await waitCall('POST', '/runs/23/harness/answer$', 3);
b = (await last('POST', '/runs/23/harness/answer$')).body;
ok('url mode: Done accepts', b.park === 'Xq3url' && b.action === 'accept' && JSON.stringify(b.content) === '{}', JSON.stringify(b));

// --- #hctl: mode, options, your setting ------------------------------------------------------
await go(21, '#hctl:not([hidden]) .hctlb');
ok('#hctl says the mode and options', (await page.textContent('#hctl .hctlb')).includes('Accept edits · Default (Opus) · Medium'), await page.textContent('#hctl .hctlb'));
ok('the built-in model picker hides (the model is an option here)', await page.$eval('#msel', (e) => e.hidden));
await page.click('#hctl .hctlb');
await page.waitForSelector('#hctl-pop:not([hidden]) [data-mode="plan"]');
ok('its selects show the current values', JSON.stringify(await page.$$eval('#hctl-pop select', (els) => els.map((e) => e.value))) === '["default","medium"]',
  JSON.stringify(await page.$$eval('#hctl-pop select', (els) => els.map((e) => e.value))));
await page.click('#hctl-pop [data-mode="plan"] input');
await waitCall('PATCH', '/runs/21/harness$', 1);
ok('a mode: PATCH {mode}', JSON.stringify((await last('PATCH', '/runs/21/harness$')).body) === '{"mode":"plan"}');
await page.waitForFunction(() => document.querySelector('#hctl .hctlb').textContent.startsWith('Plan'));
ok('the new mode comes back on the stream', true);
dialogs = 'dismiss';
await page.click('#hctl-pop [data-mode="bypassPermissions"] input');
await page.waitForTimeout(100);
ok('a bypass mode is marked and asks first — dismissed, nothing sent', (await calls('PATCH', '/runs/21/harness$')).length === 1 &&
  (await page.textContent('#hctl-pop [data-mode="bypassPermissions"]')).includes('⚠') &&
  await page.$eval('#hctl-pop [data-mode="bypassPermissions"] input', (e) => !e.checked));
dialogs = 'accept';
await page.click('#hctl-pop [data-mode="bypassPermissions"] input');
await waitCall('PATCH', '/runs/21/harness$', 2);
ok('…confirmed: PATCH {mode: bypassPermissions}', (await last('PATCH', '/runs/21/harness$')).body.mode === 'bypassPermissions');
await page.selectOption('#hctl-pop select[data-opt="model"]', 'sonnet');
await waitCall('PATCH', '/runs/21/harness$', 3);
ok('an option: PATCH {option: {id, value}}', JSON.stringify((await last('PATCH', '/runs/21/harness$')).body) === '{"option":{"id":"model","value":"sonnet"}}');
ok('your setting: Auto, as set (checked, whatever the theme draws a button like)', await page.$eval('#hctl-pop [data-setting="auto"]',
  (e) => e.classList.contains('on') && e.textContent.trim().startsWith('✓') && e.getAttribute('aria-pressed') === 'true'));
await page.click('#hctl-pop [data-setting="approve"]');
await waitCall('PUT', '/prefs/harness-mode/claude$', 1);
ok('Always approve: PUT /prefs/harness-mode/claude', (await last('PUT', '/prefs/harness-mode/claude$')).body.mode === 'approve');
await page.waitForFunction(() => document.querySelector('#hctl-pop [data-setting="approve"]').classList.contains('on'));
await page.keyboard.press('Escape');
ok('Esc closes the popover', await page.$eval('#hctl-pop', (e) => e.hidden));

// --- the slash menu --------------------------------------------------------------------------
await page.focus('#msg');
await page.keyboard.type('/');
await page.waitForSelector('#slash:not([hidden]) .hsl');
ok('"/" offers the advertised commands', JSON.stringify(await page.$$eval('#slash .hsl', (els) => els.map((e) => e.dataset.cmd))) === '["review","compact"]');
await page.keyboard.type('co');
ok('typing narrows it', JSON.stringify(await page.$$eval('#slash .hsl', (els) => els.map((e) => e.dataset.cmd))) === '["compact"]');
await page.keyboard.press('Backspace'); await page.keyboard.press('Backspace');
await page.keyboard.press('ArrowDown'); await page.keyboard.press('ArrowUp'); await page.keyboard.press('ArrowDown');
await page.keyboard.press('Enter');
ok('Enter picks (not sends): "/compact "', (await page.inputValue('#msg')) === '/compact ' && await page.$eval('#slash', (e) => e.hidden) &&
  (await calls('POST', '/runs/21/message$')).length === 0);
await page.fill('#msg', '');
await page.keyboard.type('/r');
await page.waitForSelector('#slash:not([hidden])');
await page.keyboard.press('Escape');
ok('Esc dismisses it', await page.$eval('#slash', (e) => e.hidden) && (await page.inputValue('#msg')) === '/r');
await page.fill('#msg', '');

// --- steering ---------------------------------------------------------------------------------
await page.evaluate(() => {
  const r = window.__views[21].run;
  r.status = 'running';
  window.__push({ type: 'run', run: 21, root: 21, data: { id: 21, status: 'running', harness: { ...r.harness, state: 'working' } } });
});
await page.waitForFunction(() => document.getElementById('msg').placeholder.startsWith('steer'));
ok('while its turn runs: steer, and what interrupts', (await page.getAttribute('#msg', 'placeholder')).includes('⌘/Ctrl+Enter interrupts'));
ok('Stop says it interrupts the harness', (await page.getAttribute('#stop', 'title')).includes('interrupts Claude Code'));
await page.setViewportSize({ width: 700, height: 720 }); // the tile's narrowest column (480px beside the sidebar), Stop showing
await page.waitForTimeout(100);
const fit = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, msg: document.getElementById('msg').getBoundingClientRect().width,
  send: document.getElementById('send').getBoundingClientRect().right }));
ok('…the composer fits the narrowest column, its text box usable (#hctl gives way)', fit.sw <= 700 && fit.msg >= 60 && fit.send <= 700, JSON.stringify(fit));
await page.setViewportSize({ width: 1280, height: 720 });
await page.fill('#msg', 'stop and use tabs');
await page.keyboard.press('Control+Enter');
await waitCall('POST', '/runs/21/message$', 1);
b = (await last('POST', '/runs/21/message$')).body;
ok('⌘/Ctrl+Enter: {text, interrupt: true}', b.text === 'stop and use tabs' && b.interrupt === true, JSON.stringify(b));
await page.waitForFunction(() => document.getElementById('msg').value === '');
ok('…and the box empties', true);
await page.fill('#msg', 'also the tests');
await page.keyboard.press('Enter');
await waitCall('POST', '/runs/21/message$', 2);
b = (await last('POST', '/runs/21/message$')).body;
ok('Enter: the message as usual (no interrupt)', b.text === 'also the tests' && !('interrupt' in b), JSON.stringify(b));
await page.evaluate(() => window.__push({ type: 'inbox', run: 21, root: 21, data: { queued: [{ id: 7, text: 'also the tests' }] } }));
await page.waitForSelector('#queue .qchip');
ok('the queued chip: steering (an adapter that steers)', (await page.textContent('#queue .qchip .ql')) === 'steering');
await page.evaluate(() => {
  window.__push({ type: 'inbox', run: 21, root: 21, data: { queued: [] } });
  window.__push({ type: 'message', run: 21, root: 21, data: { id: 900, runId: 21, seq: 900, role: 'user', content: 'also the tests', created: Math.floor(Date.now() / 1000) } });
});
await page.waitForSelector('#hsteer:not([hidden]) .hsn');
ok('a steered message says so', (await page.textContent('#hsteer')).includes('steered into Claude Code\'s running turn') &&
  (await page.textContent('#hsteer')).includes('also the tests'));
await page.waitForFunction(() => document.getElementById('hsteer').hidden, null, { timeout: 9000 });
ok('…for a moment', true);

// --- at home: your setting for the harness that answers new chats --------------------------------
await page.click('#home');
await page.waitForFunction(() => document.getElementById('hctl').hidden);
ok('home, the built-in agent answering: no #hctl', true);
await page.evaluate(async () => { const { ctx } = await import('/web-ext.js'); ctx.app.harness.choose('codex'); });
await page.waitForSelector('#hctl:not([hidden]) .hctlb');
ok('home, Codex answering: its setting', (await page.textContent('#hctl .hctlb')).includes('Codex: Always approve'), await page.textContent('#hctl .hctlb'));
await page.click('#hctl .hctlb');
ok('…a note without a conversation\'s mode above it', !(await page.textContent('#hctl-pop')).includes('switched above'));
await page.click('#hctl-pop [data-setting="auto"]');
await waitCall('PUT', '/prefs/harness-mode/codex$', 1);
ok('Auto: PUT /prefs/harness-mode/codex', (await last('PUT', '/prefs/harness-mode/codex$')).body.mode === 'auto');
await page.waitForFunction(() => document.querySelector('#hctl .hctlb').textContent.includes('Codex: Auto'));

// a built-in conversation: none of it
await go(25, '.acard');
ok('a built-in conversation: no #hctl, its own words', await page.$eval('#hctl', (e) => e.hidden) && !(await page.$('.hask')) &&
  !(await page.getAttribute('#msg', 'placeholder')).includes('Claude Code'));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness-ask');
