// harness-child.mjs — coding agents the agent started, as their cards in the
// parent's chat on the web (D147 §8 U6: harness-child.js) over
// kidsSeed() (test/harness-fixtures.mjs) and the STUB's §4 routes: #25's
// three children — 26 working through its plan, 27 parked on a permission,
// 28 signed out. Each card's identity, status line, counters and plan; its
// last 3 blocks read only once the card is open (GET /runs/26/view?limit=8)
// and kept current by the stream; the permission and the sign-in drawn on the
// card and answered on the CHILD's run; Stop, Cancel (confirmed), Message
// (Enter queues, ⌘/Ctrl+Enter and Send now interrupt) and the notice the
// parent's chat shows once it is told; Open ↗; the list row's ? and ⧉ N;
// view-only readers get no controls.
//
//   node test/harness-child.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { kidsSeed } from './harness-fixtures.mjs';

const { ok, done } = checker();
const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await serveTile(ctx);
await ctx.addInitScript(STUB, kidsSeed());
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
let dialogs = 'accept';
page.on('dialog', (d) => (dialogs === 'accept' ? d.accept() : d.dismiss()));
const calls = (method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url))
  .map((c) => ({ url: c.url, body: c.body ? JSON.parse(c.body) : null })), [method, re]);
const last = async (method, re) => (await calls(method, re)).pop() || null;
const waitCall = (method, re, n) => page.waitForFunction(([m, s, k]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)).length >= k,
  [method, re, n], { timeout: 5000 });
const card = (id) => `#timeline .hkid[data-child="${id}"]`;
const text = (sel) => page.textContent(sel);
const push = (ev) => page.evaluate((e) => window.__push(e), ev);

await page.goto(`${ORIGIN}/#c=25`);
await page.waitForSelector(card(28));

// --- three cards, where the spawn calls are -----------------------------------------------
ok('three coding agents, each a child card (not the built-in subagent card)',
  (await page.$$('#timeline .acard')).length === 3 && (await page.$$('#timeline .acard.hkid')).length === 3);
const head26 = await text(`${card(26)} .ach`);
ok('identity: monogram, name, the link\'s label, #id, state', (await text(`${card(26)} .ach .kind`)) === 'CC' && head26.includes('Claude Code')
  && head26.includes('Split the router') && head26.includes('#26') && (await text(`${card(26)} .hkst`)) === 'working', head26);
ok('the status line: what it runs now', (await text(`${card(26)} .hkline`)).trim() === 'Running: Run go vet ./...');
const meta26 = await text(`${card(26)} .hkmeta`);
ok('where it works and its counters', meta26.includes('api-dev:/work/api') && meta26.includes('7 tool calls · 2 files +31 −4'), meta26);
ok('Codex\'s: CX, its state says it waits for you', (await text(`${card(27)} .ach .kind`)) === 'CX' && (await text(`${card(27)} .hkst`)) === 'needs approval');
ok('the signed-out one says so', (await text(`${card(28)} .hkst`)) === 'needs sign-in'
  && (await text(`${card(28)} .hkline`)).includes('needs you to sign in to Claude Code'));
ok('collapsed: no body, and no child\'s view read yet', !(await page.$(`${card(26)} .hkbody`)) && (await calls('GET', '/runs/2[678]/view')).length === 0);

// --- open: the task, the plan, the last 3 blocks (read now, kept current) -------------------
await page.click(`${card(26)} .ach`);
await page.waitForSelector(`${card(26)} .hktail`);
const reads = await calls('GET', '/runs/26/view');
ok('its tail is read once the card is open and on screen: the newest page', reads.length === 1 && /\/runs\/26\/view\?limit=8$/.test(reads[0].url), JSON.stringify(reads));
ok('the task and the plan', (await text(`${card(26)} .task`)).includes('one file per resource')
  && (await page.$$(`${card(26)} .hkplan .pe`)).length === 3 && (await text(`${card(26)} .hkplan .pe.in_progress`)).includes('Split the router'));
const tailKeys = () => page.$$eval(`${card(26)} .hktail > [data-k]`, (els) => els.map((e) => e.dataset.k));
ok('the last 3 blocks, drawn as the chat draws them', JSON.stringify(await tailKeys()) === '["ch1:k03","m8","ch1:k04"]', JSON.stringify(await tailKeys()));
ok('…the rest a click away', !!(await page.$(`${card(26)} .hkmore`)));
await push({ type: 'message', run: 26, root: 25, data: { id: 11, runId: 26, seq: 11, role: 'assistant', content: '', created: 1790000000,
  toolCalls: [{ id: 'h1:k05', type: 'function', function: { name: 'acp:read', arguments: JSON.stringify({ file_path: '/work/api/go.mod', summary: 'Read go.mod' }) } }] } });
await page.waitForFunction((sel) => [...document.querySelectorAll(sel)].some((e) => e.dataset.k === 'ch1:k05'), `${card(26)} .hktail > [data-k]`);
ok('the stream keeps it current: a new call, the oldest one gone', JSON.stringify(await tailKeys()) === '["m8","ch1:k04","ch1:k05"]', JSON.stringify(await tailKeys()));
await push({ type: 'harness', run: 26, root: 25, data: { ...(await page.evaluate(() => window.__runs.find((r) => r.id === 26).harness)), activity: { kind: 'thinking', at: 1 } } });
await page.waitForFunction((sel) => document.querySelector(sel).textContent.trim() === 'Thinking…', `${card(26)} .hkline`);
ok('a harness event moves the status line', true);
ok('still one read', (await calls('GET', '/runs/26/view')).length === 1);

// --- 27's permission: on its card, answered on 27 ----------------------------------------
const opts27 = await page.$$eval(`${card(27)} .hask .hopts button`, (els) => els.map((e) => e.dataset.opt));
ok('27\'s permission is on its card, with Codex\'s own options', JSON.stringify(opts27) === '["approved","approved-for-session","abort"]', JSON.stringify(opts27));
ok('…the command it asks to run', (await text(`${card(27)} .hask`)).includes('psql -f migrations/0007_users.sql'));
await page.click(`${card(27)} .hask [data-opt="approved"]`);
await waitCall('POST', '/runs/27/approve$', 1);
const ap = (await last('POST', '/runs/27/approve$')).body;
ok('answered on the child\'s run: {park, option}', ap.park === 'Xq3kid' && ap.option === 'approved', JSON.stringify(ap));
ok('…never on the parent\'s', (await calls('POST', '/runs/25/approve')).length === 0);
await page.waitForFunction((sel) => !document.querySelector(sel + ' .hask') && document.querySelector(sel + ' .hkst').textContent === 'working', card(27));
ok('the card goes on: working, the card gone', true);

// --- 28's sign-in: on its card, for run 28 ----------------------------------------------------
const login = `${card(28)} .hlogin`;
ok('28\'s sign-in is on its card: the warning, the confirm (a shared sandbox), its methods', !!(await page.$(login))
  && (await text(login)).includes('credentials land in api-dev') && !!(await page.$(`${login} #hl-confirm`))
  && JSON.stringify(await page.$$eval(`${login} [data-kind]`, (els) => els.map((e) => e.dataset.kind))) === '["terminal","api-key"]');
await page.check(`${login} #hl-confirm`);
await page.fill(`${login} form[data-kind="api-key"] input`, 'sk-ant-test');
await page.click(`${login} form[data-kind="api-key"] button`);
await waitCall('POST', '/runs/28/harness/authenticate$', 1);
const au = (await last('POST', '/runs/28/harness/authenticate$')).body;
ok('its API key goes to run 28, once, confirmed', au.method === 'anthropic-api-key' && au.apiKey === 'sk-ant-test' && au.confirm === true, JSON.stringify({ ...au, apiKey: au.apiKey ? '…' : '' }));
await page.waitForFunction((sel) => document.querySelector(sel + ' .hkst').textContent === 'working', card(28));
ok('signed in: it goes on working', !(await page.$(login)));

// --- Stop, Message, the notice ----------------------------------------------------------------
await page.click(`${card(26)} [data-act="stop"]`);
await waitCall('POST', '/runs/26/interrupt$', 1);
ok('Stop interrupts its turn', (await text(`${card(26)} .hknote`)).includes('Stopped'));
await page.click(`${card(26)} [data-act="message"]`);
ok('Message: a box saying the agent is told', (await page.getAttribute(`${card(26)} .hkin`, 'placeholder')) === 'Message Claude Code directly — the agent is told');
await page.fill(`${card(26)} .hkin`, 'keep the old routes working');
await page.press(`${card(26)} .hkin`, 'Enter');
await waitCall('POST', '/runs/26/message$', 1);
let mb = (await last('POST', '/runs/26/message$')).body;
ok('Enter sends it to the child, queued (no interrupt)', mb.text === 'keep the old routes working' && !('interrupt' in mb) && !!mb.clientId, JSON.stringify(mb));
ok('…and says the agent is told', (await text(`${card(26)} .hknote`)).includes('#25\'s agent is told'));
await page.waitForFunction(() => [...document.querySelectorAll('#timeline .notice .nh')].filter((e) => e.textContent.includes('direct message to #26 (Claude Code) from admin')).length === 2);
await page.locator('#timeline .notice .nh').last().click();
ok('the parent\'s chat shows the notice once it is told', (await text('#timeline .notice.on .nb')).includes('keep the old routes working'));
await page.fill(`${card(26)} .hkin`, 'stop and run the tests');
await page.press(`${card(26)} .hkin`, process.platform === 'darwin' ? 'Meta+Enter' : 'Control+Enter');
await waitCall('POST', '/runs/26/message$', 2);
mb = (await last('POST', '/runs/26/message$')).body;
ok('⌘/Ctrl+Enter interrupts its turn first', mb.text === 'stop and run the tests' && mb.interrupt === true, JSON.stringify(mb));
await page.fill(`${card(26)} .hkin`, 'now');
await page.click(`${card(26)} [data-act="send-now"]`);
await waitCall('POST', '/runs/26/message$', 3);
ok('Send now: the same', (await last('POST', '/runs/26/message$')).body.interrupt === true);

// --- Cancel (confirmed) ---------------------------------------------------------------------
dialogs = 'dismiss';
await page.click(`${card(26)} [data-act="cancel"]`);
await page.waitForTimeout(100);
ok('Cancel asks first — dismissed, nothing is sent', (await calls('POST', '/runs/26/cancel')).length === 0);
dialogs = 'accept';
await page.click(`${card(26)} [data-act="cancel"]`);
await waitCall('POST', '/runs/26/cancel$', 1);
await page.waitForFunction((sel) => document.querySelector(sel + ' .hkst').textContent === 'canceled', card(26));
ok('canceled: its link settled, no Stop or Cancel left', !(await page.$(`${card(26)} [data-act="stop"]`)) && !(await page.$(`${card(26)} [data-act="cancel"]`))
  && (await text(`${card(26)} .hkline`)).trim() === 'canceled');

// --- the list row: ? and ⧉ N -----------------------------------------------------------------
const row = '#runs .run[data-id="25"]';
await page.waitForSelector(row);
ok('the row: ? while a run below waits, the agents glyph and N coding agents at work', !!(await page.$(`${row} .gl.ask`))
  && (await text(`${row} .kids`)) === '3' && !!(await page.$(`${row} .kids bx-icon[name="agent"]`))
  && (await page.getAttribute(`${row} .kids`, 'title')).includes('3 coding agents at work in this conversation'));

// --- Open ↗ ------------------------------------------------------------------------------------
await page.click(`${card(27)} [data-act="open"]`);
await page.waitForFunction(() => location.hash === '#c=27');
await page.waitForSelector('.crumbs');
ok('Open ↗: the child\'s own chat, under its parent', (await text('.crumbs')).includes('Refactor the API'));

// --- a view-only reader: the cards, no controls -------------------------------------------------
await page.evaluate(() => { window.__views[25].access = 'viewer'; });
await page.evaluate(() => { location.hash = '#c=25'; });
await page.waitForSelector(card(28));
await page.waitForFunction(() => !document.querySelector('.hkid [data-act="message"]'));
ok('view only: Open ↗ but no Stop, Cancel or Message', !!(await page.$(`${card(28)} [data-act="open"]`)) && !(await page.$('.hkid [data-act="stop"]')));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness-child');
