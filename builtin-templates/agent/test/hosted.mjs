// hosted.mjs — non-secure (hosted) conversations in a person's page (API.md
// "Non-secure conversations"; model/hosted.js, hosted-ui.js), driving the
// real tile against backend.mjs as alice's own partition ("user:alice"):
//
//   - a hosted conversation (an id from 2^39, at the global instance) has
//     the ⚠ "not private" chip on its row and its header;
//   - opening it shows the warning by itself (who can read it); "Open
//     without sending" leaves the composer locked, the strip above it offers
//     the warning again, and "Start anyway" unlocks it;
//   - one she hosts, paused for a wider audience, asks her above the
//     composer, and Confirm answers her own partition with what she saw;
//   - a shared conversation's share dialog: "Use my private resources…" —
//     the warning, then POST /hosting at home with the audience she saw —
//     and "Add a copy of my files…": POST /copyin at home.
//
//   node test/hosted.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const MINE = 2 ** 40 + 1;
const HOSTED = 2 ** 39 + 1; // bob hosts it; alice is in it
const PAUSED = 2 ** 39 + 2; // alice hosts it; carol was added
const MOVED = 2 ** 39 + 3;
const seed = { runs: [], me: { kind: 'user', user: 'alice', level: 'read', manager: false, halted: false, partition: 'user:alice' } };

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(([MINE, HOSTED, PAUSED, MOVED]) => {
  window.xbin.partition = 'user:alice';
  const j = window.__json;
  const row = (id, title, extra) => ({ id, title, status: 'idle', access: 'participant', mine: true, origin: 'chat', visibility: 'private',
    activityMs: 1000 + (id % 1000), parentId: 0, rootId: id, ...extra });
  const hostedBy = (host, state, extra = {}) => ({ host, state, reason: '', resources: ['sandboxes', 'tiles', 'vault'], since: 1, ...extra });
  const pendingKey = JSON.stringify({ owner: 'alice', visibility: 'private', teamRole: 'viewer', members: { bob: 'participant', carol: 'viewer' } });
  const own = [row(MINE, 'my notes', { owner: 'alice', access: 'owner' })];
  const shared = [
    row(2, 'with bob', { owner: 'alice', access: 'owner', members: 1 }),
    row(HOSTED, 'bob hosts this', { owner: 'bob', members: 1, hosted: hostedBy('bob', 'active') }),
    row(PAUSED, 'alice hosts this', { owner: 'alice', access: 'owner', members: 2,
      hosted: hostedBy('alice', 'paused', { reason: 'confirm', pending: ['carol'], pendingKey, dropsAt: 2e9 }) }),
  ];
  window.__homeRows = { '': own, global: shared };
  window.__route('GET', /\/conversations\?(.*)$/, (m, o) => j({ pinned: [], items: window.__homeRows[o.partition || ''], next: '' }));
  const acl = (r) => ({ owner: r.owner, visibility: 'private', teamRole: 'viewer',
    members: r.id === 2 ? [{ user: 'bob', role: 'participant' }] : [{ user: 'alice', role: 'participant' }] });
  window.__route('GET', /\/runs\/(\d+)\/view/, (m, o) => {
    const id = +m[1];
    const r = [...window.__homeRows[''], ...window.__homeRows.global].find((x) => x.id === id) || { id, title: 'run ' + id };
    return j({ cursor: 'g.1', run: { pendingState: {}, ...r }, access: r.access, messages: [], steps: [], links: [], queued: [], drafts: [],
      chain: [], files: [], memory: {}, config: {}, messageFiles: {}, acl: acl(r), ...(r.hosted ? { hosted: r.hosted } : {}) });
  });
  window.__route('GET', /\/runs\/(\d+)\/members$/, (m) => j({ owner: 'alice', visibility: 'private', teamRole: 'viewer',
    members: [{ user: 'bob', role: 'participant' }], links: [] }));
  window.__route('POST', /\/hosting$/, () => {
    window.__homeRows.global.push(row(MOVED, 'with bob', { owner: 'alice', access: 'owner', members: 1, hosted: hostedBy('alice', 'active') }));
    return j({ conversation: MOVED, state: 'active' });
  });
  window.__route('POST', /\/hosting\/(\d+)\/confirm$/, () => j({ conversation: PAUSED, state: 'active' }));
  window.__route('GET', /\/runs\/(\d+)\/files$/, () => j([{ path: 'plan.md', bytes: 9 }]));
  window.__route('POST', /\/copyin$/, () => j({ conversation: 2, files: ['from-alice/plan.md'] }));
}, [MINE, HOSTED, PAUSED, MOVED]);
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('dialog', (d) => d.accept());
await page.goto(`${ORIGIN}/`);
await page.waitForSelector(`#runs .run[data-id="${HOSTED}"]`);
const calls = (re) => page.evaluate((s) => window.__calls.filter((c) => new RegExp(s).test(c.url)), re.source);
const composer = () => page.evaluate(() => ({ disabled: document.getElementById('msg').disabled, placeholder: document.getElementById('msg').placeholder,
  bar: !document.getElementById('hostbar')?.hidden }));

ok('a hosted conversation\'s row has the ⚠ chip', await page.$eval(`#runs .run[data-id="${HOSTED}"]`, (e) => !!e.querySelector('.chip.notprivate')));
ok('…a plain shared one\'s hasn\'t', await page.$eval('#runs .run[data-id="2"]', (e) => !e.querySelector('.chip.notprivate')));

// opening it: the warning by itself; without sending, the composer is locked
await page.evaluate((id) => { location.hash = 'c=' + id; }, HOSTED);
await page.waitForSelector('#hostdlg[open] #host-anyway');
const readers = await page.$$eval('#hostdlg #host-readers li', (els) => els.map((e) => e.textContent.trim()));
ok('the warning lists who can read it', readers[0].includes('bob') && readers[0].includes('alice') && readers.some((r) => /managers/.test(r))
  && readers.some((r) => /admins/.test(r)) && readers.some((r) => /change this agent's code/.test(r)), JSON.stringify(readers));
ok('…and whose resources it uses', /bob's private sandboxes/.test(await page.$eval('#hostdlg #host-exposed', (e) => e.textContent)));
ok('no "don\'t show again"', !/show again/i.test(await page.$eval('#hostdlg', (e) => e.textContent)));
ok('the header\'s ⚠ chip', /not private/.test(await page.$eval('#top #hosted-chip', (e) => e.textContent)));
await page.click('#hostdlg #host-nosend');
let c = await composer();
ok('"Open without sending": the composer is locked', c.disabled && /without sending/.test(c.placeholder) && c.bar, JSON.stringify(c));
await page.click('#hostbar #host-start');
await page.waitForSelector('#hostdlg[open] #host-anyway');
await page.click('#hostdlg #host-anyway');
await page.waitForFunction(() => !document.getElementById('msg').disabled);
c = await composer();
ok('"Start anyway": it opens for writing', !c.disabled && !c.bar, JSON.stringify(c));
// back to it in the same page session: started, no warning
await page.evaluate(() => { location.hash = 'c=2'; });
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent.includes('with bob'));
await page.evaluate((id) => { location.hash = 'c=' + id; }, HOSTED);
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent.includes('bob hosts'));
ok('back to it in the same page session: no warning, not locked', !(await page.evaluate(() => document.getElementById('hostdlg')?.open)) && !(await composer()).disabled);

// the one she hosts, paused: she is asked above the composer
await page.evaluate((id) => { location.hash = 'c=' + id; }, PAUSED);
await page.waitForSelector('#hostbar #host-confirm');
c = await composer();
ok('paused: locked, waiting for its host', c.disabled && /waiting for alice/.test(c.placeholder), JSON.stringify(c));
ok('…and its host is asked about carol', /carol/.test(await page.$eval('#hostbar', (e) => e.textContent)));
await page.click('#hostbar #host-confirm');
await page.waitForFunction(() => window.__calls.some((x) => /\/hosting\/\d+\/confirm$/.test(x.url)));
const conf = (await calls(/\/hosting\/\d+\/confirm$/))[0];
ok('Confirm goes to her own partition with what she saw', conf.home === '' && JSON.parse(conf.body).seen.includes('carol'), JSON.stringify(conf));

// a plain shared conversation's share dialog: use my private resources
await page.evaluate(() => { location.hash = 'c=2'; });
await page.waitForSelector('#top .sharepill');
await page.click('#top .sharepill');
await page.waitForSelector('#sharedlg #host-use');
await page.click('#sharedlg #host-use');
await page.waitForSelector('#hostdlg[open] #host-go');
ok('the warning before hosting lists who can read it', /bob/.test(await page.$eval('#hostdlg #host-readers', (e) => e.textContent)));
await page.click('#hostdlg #host-go');
await page.waitForFunction((id) => location.hash === '#c=' + id, MOVED);
const host = (await calls(/\/hosting$/))[0];
const hb = JSON.parse(host.body);
ok('hosting is asked of her own partition, with the audience she saw', host.home === '' && hb.conversation === 2 && hb.seen.owner === 'alice'
  && hb.seen.members.bob === 'participant', host.body);
ok('the moved one opens started (she just read the warning)', !(await page.evaluate(() => document.getElementById('hostdlg')?.open)));

// add a copy of my files
await page.evaluate(() => { location.hash = 'c=2'; });
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent.includes('with bob'));
await page.click('#top .sharepill');
await page.waitForSelector('#sharedlg #copy-mine');
await page.click('#sharedlg #copy-mine');
await page.waitForFunction(() => document.querySelectorAll('#hostdlg[open] #copy-conv option').length > 1);
await page.selectOption('#hostdlg #copy-conv', String(MINE));
await page.waitForSelector('#hostdlg input[data-path="plan.md"]');
ok('the copy dialog says the originals stay private', /originals stay private/.test(await page.$eval('#hostdlg', (e) => e.textContent)));
await page.check('#hostdlg input[data-path="plan.md"]');
await page.click('#hostdlg #copy-go');
await page.waitForFunction(() => window.__calls.some((x) => /\/copyin$/.test(x.url)));
const cp = (await calls(/\/copyin$/))[0];
const cb = JSON.parse(cp.body);
ok('Add a copy: sent from her own partition', cp.home === '' && cb.conversation === 2 && cb.files[0].run === MINE && cb.files[0].path === 'plan.md', cp.body);

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('hosted');
