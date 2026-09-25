// share.mjs — the share dialog (D83): who can see a conversation, the people
// it is shared with, invite links — and a link must never carry the frame's
// query string (a credentialless frame's URL holds its ?frame= token).
//
//   node test/share.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const seed = {
  runs: [{ id: 1, title: 'plan the quarter', status: 'idle', parentId: 0, rootId: 1, activityMs: Date.now() }],
  me: { kind: 'user', user: 'alice', level: 'read', manager: false, epochMs: 0 },
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(() => {
  const members = { owner: 'alice', visibility: 'private', teamRole: 'viewer', members: [{ user: 'bob', role: 'participant' }], links: [] };
  window.__members = members;
  window.__route('GET', /\/runs\/1\/members$/, () => window.__json(members));
  window.__route('POST', /\/runs\/1\/members$/, (m, o) => {
    const b = JSON.parse(o.body);
    const cur = members.members.find((x) => x.user === b.user);
    if (cur) cur.role = b.role; else members.members.push({ user: b.user, role: b.role });
    return window.__json({ ok: 'true' });
  });
  window.__route('POST', /\/runs\/1\/links$/, () => {
    members.links.push({ id: 7, role: 'participant', uses: 0, maxUses: 0, expires: 0 });
    return window.__json({ id: 7, token: 'TOKEN123', hash: '#join=TOKEN123' });
  });
  window.__route('PATCH', /\/runs\/1$/, (m, o) => {
    const b = JSON.parse(o.body);
    Object.assign(members, b);
    return window.__json({ id: 1, title: 'plan the quarter', access: 'owner', mine: true, ...b });
  });
});
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
await page.goto(`${ORIGIN}/?frame=SECRET#c=1`);
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'plan the quarter');
ok('#c=<id> opens the conversation', true);
ok('a non-manager sees no settings gear', await page.$eval('#gear', (e) => e.hidden));

await page.click('#top button:has-text("Share")');
await page.waitForSelector('#sharedlg .prow');
ok('the people it is shared with are listed', (await page.textContent('#sharedlg')).includes('bob'));
await page.fill('#sh-user', 'Carol');
await page.click('#sharedlg button:has-text("Add")');
await page.waitForFunction(() => window.__members.members.some((m) => m.user === 'carol'));
ok('adding someone sends their (lowercased) id', true);
await page.click('#sharedlg input[type=radio] >> nth=1');
await page.waitForFunction(() => window.__members.visibility === 'team');
ok('sharing with the team as readers', (await page.evaluate(() => window.__members.teamRole)) === 'viewer');
await page.click('#sharedlg button:has-text("Create link")');
await page.waitForSelector('#sharedlg .linkout');
const link = await page.$eval('#sharedlg .linkout', (e) => e.value);
ok('the invite link carries the token', link.endsWith('#join=TOKEN123'), link);
ok('…and never the frame token in the query string', !link.includes('SECRET') && !link.includes('?'), link);
await page.click('#sharedlg button:has-text("Done")');

// Opening an invite joins and opens the conversation.
await page.evaluate(() => window.__route('POST', /\/join$/, () => window.__json({ runId: 1, role: 'participant', title: 'plan the quarter' })));
await page.click('#home');
await page.fill('#csearch', 'http://x/c/apps/agent/#join=ABC');
await page.waitForFunction(() => window.__calls.some((c) => c.url.endsWith('/join') && c.body.includes('ABC')));
ok('a pasted invite link joins', true);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('share');
