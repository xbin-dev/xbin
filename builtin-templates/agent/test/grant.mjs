// grant.mjs — a grant (D111): the agent asks to read the owner's other
// conversations. Its owner allows it once or here for an hour; anyone else
// may only deny. A grant in force is a chip in the top bar — until it
// expires or the owner revokes it.
//
//   node test/grant.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const asking = { id: 1, title: 'catch me up', status: 'waiting_input', parentId: 0, rootId: 1, owner: 'alice',
  pendingState: { kind: 'approval', grant: 'threads', toolCalls: [call('c1', 'threads_list', { scope: 'all' })] } };
const seedAs = (user) => ({
  runs: [{ ...asking, activityMs: Date.now() }],
  views: { 1: { access: user === 'alice' ? 'owner' : 'participant', run: { ...asking } } },
  me: { kind: 'user', user, level: 'read', manager: false, epochMs: 0 },
});

const browser = await launch();
const errors = [];
async function open(user) {
  const ctx = await browser.newContext();
  await serveTile(ctx);
  await ctx.addInitScript(STUB, seedAs(user));
  const page = await ctx.newPage();
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto(`${ORIGIN}/#c=1`);
  await page.waitForSelector('.ask.approve.grant');
  return page;
}

// the owner: allow once, or here for an hour
let page = await open('alice');
const card = await page.textContent('.ask.approve.grant');
ok('the card says what is asked', card.includes('The agent asks to read your other conversations and automations'), card);
ok('…and which calls', card.includes('threads_list'));
const buttons = await page.$$eval('.ask.approve.grant .btn', (els) => els.map((e) => e.textContent.trim()));
ok('its owner gets Allow once · Allow here for 1 hour · Deny', buttons.join('|') === 'Allow once|Allow here for 1 hour|Deny', buttons.join('|'));
await page.click('.ask.approve.grant .btn:has-text("Allow here for 1 hour")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/1/approve')));
const body = await page.evaluate(() => window.__calls.find((c) => c.url.endsWith('/runs/1/approve')).body);
ok('an hour is sent as grant:"hour"', body === JSON.stringify({ approve: true, grant: 'hour' }), body);

// the grant in force: a chip with revoke; the run event that follows the
// revoke clears it
const push = (ev) => page.evaluate((e) => window.__push(e), ev);
await page.waitForFunction(() => window.__streams() > 0);
const until = Date.now() + 45 * 60e3;
await push({ type: 'run', run: 1, root: 1, data: { ...asking, status: 'running', pendingState: {},
  grants: [{ cap: 'threads', grantedBy: 'alice', expiresMs: until }] } });
await page.waitForSelector('#top .grantchip');
const chip = await page.textContent('#top .grantchip');
ok('the top bar shows the grant and until when', /reads your threads · until \d\d:\d\d/.test(chip), chip);
ok('the owner can revoke it', chip.includes('revoke'));
await page.click('#top .grantchip .linkbtn');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/runs/1/grants/threads')));
ok('revoke deletes the grant', true);
await push({ type: 'run', run: 1, root: 1, data: { ...asking, status: 'idle', pendingState: {}, grants: [] } });
ok('…and the chip goes', await page.waitForFunction(() => !document.querySelector('#top .grantchip'), null, { timeout: 2000 }).then(() => true, () => false));
// an expired one is not shown (read at render time: nothing ticks)
await push({ type: 'run', run: 1, root: 1, data: { ...asking, status: 'idle', pendingState: {},
  grants: [{ cap: 'threads', grantedBy: 'alice', expiresMs: Date.now() - 1000 }] } });
await page.waitForTimeout(200);
ok('an expired grant shows nothing', !(await page.$('#top .grantchip')));

// someone else in the conversation: only Deny
page = await open('carol');
const theirs = await page.$$eval('.ask.approve.grant .btn', (els) => els.map((e) => e.textContent.trim()));
ok('someone else may only deny', theirs.join('|') === 'Deny', theirs.join('|'));
ok('…and is told whose it is', (await page.textContent('.ask.approve.grant')).includes('Only alice can allow this'));
await page.click('.ask.approve.grant .btn:has-text("Deny")');
await page.waitForFunction(() => window.__calls.some((c) => c.method === 'POST' && c.url.endsWith('/runs/1/approve')));
ok('deny is a plain verdict', await page.evaluate(() => window.__calls.find((c) => c.url.endsWith('/runs/1/approve')).body) === JSON.stringify({ approve: false }));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('grant');
