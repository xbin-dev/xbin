// homes.mjs — a person's two homes (API.md "Partitioned instances" → "Shared
// conversations"; model/homes.js, homes-ui.js), driving the real tile against
// backend.mjs as alice's own partition ("user:alice"): her own conversations
// (ids from 2^40) are her partition's, shared ones (below) the global
// instance's, which the page reaches with xbin.fetch's {partition: 'global'}.
//
//   - Mine lists both homes; the Shared view the shared space's alone;
//   - #c=<a shared id> opens it at global, and the stream following it is
//     global's; her own opens at home;
//   - Share on a shared conversation is its share dialog (at global), with
//     "Copy to my own space"; on her own it is "Share a copy": the copy is
//     published from her partition and opens;
//   - New chat with options asks who can see it; a shared one is made at
//     global.
//
//   node test/homes.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const MINE = 2 ** 40 + 1;
const COPY = 2 ** 40 + 9;
const seed = { runs: [] };

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(([MINE, COPY]) => {
  window.xbin.partition = 'user:alice';
  const j = window.__json;
  const row = (id, title, extra) => ({ id, title, status: 'idle', access: 'owner', mine: true, origin: 'chat', visibility: 'private',
    activityMs: 1000 + (id % 1000), parentId: 0, rootId: id, ...extra });
  const own = [row(MINE, 'my notes')];
  const shared = [row(1, 'team plan', { visibility: 'team', teamRole: 'participant', owner: 'bob', access: 'participant', mine: false, members: 0 }),
    row(2, 'with carol', { owner: 'alice', members: 1 })];
  window.__homeRows = { '': own, global: shared };
  window.__route('GET', /\/conversations\?(.*)$/, (m, o) => {
    const q = new URLSearchParams(m[1]);
    let rows = window.__homeRows[o.partition || ''];
    if (q.get('scope') === 'shared') rows = rows.filter((r) => r.visibility === 'team' || r.members);
    else if (q.get('scope') === 'mine') rows = rows.filter((r) => r.mine);
    return j({ pinned: [], items: rows, next: '' });
  });
  window.__route('GET', /\/runs\/(\d+)\/view/, (m, o) => {
    const id = +m[1];
    const r = [...window.__homeRows[''], ...window.__homeRows.global].find((x) => x.id === id) || { id, title: 'run ' + id };
    return j({ cursor: 'g.1', run: { pendingState: {}, ...r }, access: r.access, messages: [], steps: [], links: [], queued: [], drafts: [],
      chain: [], files: [], memory: {}, config: {}, messageFiles: {}, home: o.partition || '' });
  });
  window.__route('GET', /\/runs\/(\d+)\/members$/, (m) => j({ owner: m[1] === '1' ? 'bob' : 'alice', visibility: m[1] === '1' ? 'team' : 'private',
    teamRole: 'participant', members: m[1] === '2' ? [{ user: 'carol', role: 'participant' }] : [], links: [] }));
  window.__route('POST', /\/runs\/(\d+)\/publish$/, () => {
    window.__homeRows.global.push(row(7, 'my notes (shared)', { visibility: 'team', teamRole: 'participant', owner: 'alice' }));
    return j({ run: { id: 7, title: 'my notes (shared)', owner: 'alice' }, deleted: false, left: [] });
  });
  window.__route('POST', /\/copy$/, () => { window.__homeRows[''].push(row(COPY, 'a private copy')); return j({ id: COPY, title: 'a private copy' }); });
  window.__route('POST', /\/ask$/, (m, o) => j({ id: o.partition ? 8 : 2 ** 40 + 8, title: 'new', status: 'running', rootId: o.partition ? 8 : 2 ** 40 + 8 }));
}, [MINE, COPY]);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('dialog', (d) => d.accept());
await page.goto(`${ORIGIN}/`);
await page.waitForSelector(`#runs .run[data-id="${MINE}"]`);
await page.waitForSelector('#runs .run[data-id="2"]');
const calls = (re) => page.evaluate((s) => window.__calls.filter((c) => new RegExp(s).test(c.url)), re.source);
const rows = () => page.$$eval('#runs .run', (els) => els.map((e) => +e.dataset.id));
const segs = () => page.$$eval('#views .seg', (els) => els.map((e) => e.textContent.trim()));
const menu = async (id) => {
  await page.click(`#runs .run[data-id="${id}"]`, { button: 'right' });
  const items = await page.$$eval('.rowmenu .mi', (els) => els.map((e) => e.textContent.trim()));
  await page.click('.mback');
  return items;
};

// the list: both homes in Mine, the shared space alone in Shared
const lists = await calls(/\/conversations\?/);
ok('Mine reads both homes', ['', 'global'].every((h) => lists.some((c) => c.home === h)), JSON.stringify(lists.map((c) => c.home)));
ok('Mine: her own and the shared one she started', JSON.stringify((await rows()).sort()) === JSON.stringify([2, MINE].sort()), (await rows()).join());
ok('the Shared view is offered', (await segs()).includes('Shared'));
await page.click('#views .seg:has-text("Shared")');
await page.waitForSelector('#runs .run[data-id="1"]');
ok('Shared: the shared space\'s', JSON.stringify((await rows()).sort()) === JSON.stringify([1, 2]), (await rows()).join());
ok('…read at global only', (await calls(/scope=shared/)).every((c) => c.home === 'global'));

// the router: #c=1 opens at global, followed by global's stream
await page.evaluate(() => { location.hash = 'c=1'; });
await page.waitForSelector('#top .title');
ok('#c=1 is read at global', (await calls(/\/runs\/1\/view/)).length > 0 && (await calls(/\/runs\/1\/view/)).every((c) => c.home === 'global'));
await page.waitForFunction(() => window.__calls.some((c) => /\/stream\?/.test(c.url) && /run=1\b/.test(c.url) && c.home === 'global'));
ok('the stream following it is global\'s', true);
ok('her own stream follows the list meanwhile', (await calls(/\/stream\?/)).some((c) => c.home === '' && !/run=/.test(c.url)));
ok('a shared conversation: Share…', (await menu(1)).includes('Share…') || (await menu(2)).includes('Share…'));
await page.click('#top .sharepill');
await page.waitForSelector('#sharedlg #sh-copy');
ok('its share dialog reads its members at global', (await calls(/\/runs\/1\/members/)).every((c) => c.home === 'global'));
await page.click('#sharedlg #sh-copy');
await page.waitForFunction((id) => location.hash === '#c=' + id, COPY);
ok('Copy to my own space: made in her partition, and it opens', (await calls(/\/copy$/)).length === 1 && (await calls(/\/copy$/))[0].home === '');

// her own: Share a copy
await page.click('#views .seg:has-text("Mine")');
await page.waitForSelector(`#runs .run[data-id="${MINE}"]`);
const mine = await menu(MINE);
ok('her own: "Share a copy…", not "Share…"', mine.includes('Share a copy…') && !mine.includes('Share…'), mine.join());
await page.evaluate((id) => { location.hash = 'c=' + id; }, MINE);
await page.waitForFunction((id) => window.__calls.some((c) => c.url.includes(`/runs/${id}/view`)), MINE);
ok('her own is read at home', (await calls(new RegExp(`/runs/${MINE}/view`))).every((c) => c.home === ''));
await page.waitForSelector('#top .sharepill');
await page.click('#top .sharepill');
await page.waitForSelector('#pubdlg #pub-go');
const note = (await page.$eval('#pubnote', (e) => e.textContent)).replace(/\s+/g, ' ');
ok('the copy\'s dialog says what goes where — the whole transcript, what its tools returned too', /everything its tools returned/.test(note) &&
  /session files go too only if you add them/.test(note), note);
await page.click('#pubdlg #pub-go');
await page.waitForFunction(() => location.hash === '#c=7');
const pub = await calls(/\/publish$/);
ok('Share a copy: published from her partition', pub.length === 1 && pub[0].home === '' && pub[0].url.includes(`/runs/${MINE}/publish`));
const body = JSON.parse(pub[0].body);
ok('…shared with the team to write, the original kept, no files', body.share.visibility === 'team' && body.share.teamRole === 'participant' && body.keep && !body.files, pub[0].body);
ok('…and the copy opens at global', (await calls(/\/runs\/7\/view/)).every((c) => c.home === 'global') && (await calls(/\/runs\/7\/view/)).length > 0);

// new chat with options: who can see it
await page.click('#newopts');
await page.waitForSelector('#newdlg #n-share');
await page.fill('#n-goal', 'for the team');
await page.selectOption('#n-share', 'team-participant');
await page.click('#n-create');
await page.waitForFunction(() => window.__calls.some((c) => /\/ask$/.test(c.url)));
const ask = (await calls(/\/ask$/))[0];
ok('a shared new chat is made at global, shared', ask.home === 'global' && JSON.parse(ask.body).share.visibility === 'team', JSON.stringify(ask));
// …or shared with people: none named, the dialog says so and stays open
await page.click('#newopts');
await page.waitForSelector('#newdlg[open] #n-share');
await page.fill('#n-goal', 'for carol and dave');
await page.selectOption('#n-share', 'people');
await page.click('#n-create');
await page.waitForSelector('#newdlg[open] #n-share-err');
ok('people chosen, none named: the dialog says so and stays open', (await calls(/\/ask$/)).length === 1);
await page.fill('#n-share-people', 'carol, dave');
await page.click('#n-create');
await page.waitForFunction(() => window.__calls.filter((c) => /\/ask$/.test(c.url)).length === 2);
const ask2 = (await calls(/\/ask$/))[1];
ok('a chat shared with people is made at global, with them', ask2.home === 'global' &&
  JSON.stringify(JSON.parse(ask2.body).share) === '{"members":[{"user":"carol","role":"participant"},{"user":"dave","role":"participant"}]}', ask2.body);
ok('no page errors', !errors.length, errors.join(' | '));
await browser.close();
done('homes');
