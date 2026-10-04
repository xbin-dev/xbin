// projects-more.mjs — what the coordinator, the event feed, team projects
// and "Make this a project…" add to Projects on the web (API.md §Projects in
// the UI) over moreSeed()/teamSeed() and PROJ_MORE_STUB
// (test/projects-more-stub.mjs), beside PROJ_STUB: the coordinator card
// (Send, Open); the Activity tab (newest first, scm text plain, only https
// links, a `project` event reads only what is new); the ▣ popover's "Make
// this a project…" (detect, the picks, https, the body, the crumb after);
// a team definition's board (rows plain, "open (yours)" for your own,
// a member who left greyed, Hide) and its seed sandbox; Work on this (the security part shown,
// then accepted by its hash); reviewing a membership's changes (side by
// side, marked, Accept); a team project's definition made from your own
// space; a phone's width.
//
//   node test/projects-more.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { PROJ_STUB } from './projects-stub.mjs';
import { PROJ_MORE_STUB, moreSeed, teamSeed, ev } from './projects-more-stub.mjs';

const { ok, done } = checker();
const browser = await launch();
const B = 2 ** 40;

async function open(seed, { width = 1280, hash = '', init = null } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } });
  await serveTile(ctx);
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
  .map((c) => ({ url: c.url, home: c.home, body: c.body ? JSON.parse(c.body) : null })), [method, re]);
const waitCall = (page, method, re, n = 1) => page.waitForFunction(([m, s, k]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)).length >= k,
  [method, re, n], { timeout: 5000 }).then(() => true, () => false);
const waitText = (page, sel, want) => page.waitForFunction(([s, w]) => (document.querySelector(s)?.textContent || '').includes(w), [sel, want], { timeout: 5000 })
  .then(() => true, () => false);
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth && document.body.scrollWidth <= window.innerWidth);

// === an unpartitioned agent: the coordinator and the feed ====================================================
const { page, errors } = await open(moreSeed(), { hash: '#proj=7' });
await page.waitForSelector('#pcoord');
ok('the coordinator card, above the board', await page.$eval('#pcoord', (e) => !!e.nextElementSibling && e.closest('.projs-page') !== null));
await page.fill('#pcoord-text', 'make tasks for the open bugs');
await page.click('#pcoord-send');
ok('Send writes to it (made on first use)', await waitCall(page, 'POST', '/projects/7/coordinator$'));
ok('…with the text', JSON.stringify((await calls(page, 'POST', '/projects/7/coordinator$'))[0].body) === '{"text":"make tasks for the open bugs"}');
ok('…and says so', await waitText(page, '#pcoord-note', 'Sent to the coordinator'));
ok('…the line emptied', (await page.inputValue('#pcoord-text')) === '');
await page.click('#pcoord-open');
ok('Open opens its conversation', await page.waitForFunction(() => location.hash === '#c=900', null, { timeout: 5000 }).then(() => true, () => false));
ok('…asked with no text', JSON.stringify((await calls(page, 'POST', '/projects/7/coordinator$'))[1].body) === '{}');

await page.evaluate(() => { location.hash = '#proj=7'; });
await page.waitForSelector('[data-tab="events"]');
await page.click('[data-tab="events"]');
await page.waitForSelector('#pfeed .pev');
const kinds = await page.$$eval('#pfeed .pev', (els) => els.map((e) => e.dataset.kind));
ok('the Activity tab: the events, newest first', JSON.stringify(kinds) === '["note","comment","ci.failed","pr.opened","task.created"]', JSON.stringify(kinds));
const failed = await page.$eval('#pfeed .pev[data-kind="ci.failed"]', (e) => ({ text: e.textContent, imgs: e.querySelectorAll('img, b b').length, links: [...e.querySelectorAll('a[href]')].map((a) => a.getAttribute('href')) }));
ok('scm text is plain (no markup drawn)', failed.text.includes('<img src=x onerror="window.__pwned=1"> **bold**') && failed.imgs === 0, failed.text);
ok('…and never runs', await page.evaluate(() => window.__pwned === undefined));
ok('…a link only when https', failed.links.length === 0, JSON.stringify(failed.links));
ok('…an https link is one', (await page.getAttribute('#pfeed .pev[data-kind="pr.opened"] a[target]', 'href')) === 'https://github.com/acme/web/pull/42');
ok('…direction overrides dropped', !(await page.textContent('#pfeed .pev[data-kind="comment"]')).includes('‮'));
ok('…the coordinator woken for it says so', (await page.textContent('#pfeed .pev[data-kind="ci.failed"]')).includes('coordinator'));
ok('…its task opens its conversation', !!(await page.$('#pfeed .pev[data-kind="ci.failed"] a.lnk')));
const before = (await calls(page, 'GET', '/projects/7/events')).length;
await page.evaluate((e) => { window.__more.events[7].push(e); window.__push({ type: 'project', run: 0, root: 0, data: { id: 7, change: 'event', n: 2 } }); }, ev(6, 7, 'merged', 2, { text: 'acme/web#43 merged' }));
ok('a project event: the new one shows first', await page.waitForFunction(() => document.querySelector('#pfeed .pev')?.dataset.kind === 'merged', null, { timeout: 5000 }).then(() => true, () => false));
const after = (await calls(page, 'GET', '/projects/7/events')).slice(before);
ok('…read since the last one held', after.length >= 1 && after.every((c) => /since=5/.test(c.url)), JSON.stringify(after.map((c) => c.url)));
await page.click('[data-tab="board"]');
await page.click('#proj-forkbase');
ok('Fork base now (confirmed): POST …/fork-base {now: true}', await waitCall(page, 'POST', '/projects/7/fork-base$'));
ok('…its body', JSON.stringify((await calls(page, 'POST', '/projects/7/fork-base$'))[0].body) === '{"now":true}');
ok('…said', await waitText(page, '.pflash', 'fork base is being taken'));
ok('no page errors (coordinator, feed)', errors.length === 0, errors.join(' | '));

// === "Make this a project…" ===========================================================================
const { page: u, errors: ue } = await open(moreSeed(), { hash: '#c=1' });
await u.waitForSelector('#sbxbadge');
await u.click('#sbxbadge');
await u.waitForSelector('#sbxpop #sbx-upgrade');
ok('the ▣ popover offers Make this a project…', (await u.textContent('#sbx-upgrade')) === 'Make this a project…');
await u.click('#sbx-upgrade');
await u.waitForSelector('#pupg .pupgc');
ok('…the popover closes', !(await u.$('#sbxpop')));
ok('…the sandbox is read', (await calls(u, 'GET', '/runs/1/project/detect$')).length === 1);
const cands = await u.$$eval('#pupg .pupgc', (els) => els.map((e) => ({ path: e.dataset.path, on: e.querySelector('input').checked, dis: e.querySelector('input').disabled })));
ok('its repos: those a provider serves picked, the other one not offered', JSON.stringify(cands) === JSON.stringify([
  { path: '/work/api', on: true, dis: false }, { path: '/work/web', on: true, dis: false }, { path: '/work/other', on: false, dis: true }]), JSON.stringify(cands));
ok('…why not', (await u.textContent('#pupg .pupgc[data-path="/work/other"]')).includes('no scm provider serves gitlab.example'));
ok('…an ssh or credentialed remote may switch to https', (await u.$$('#pupg .pupgc input[type=checkbox]')).length === 5);
await u.click('#pupg .pupgc[data-path="/work/web"] label.small input');
await u.fill('#pupg-name', 'API');
await u.selectOption('#pupg-branch', 'new');
await u.click('#pupg-make');
ok('Make the project: POST /runs/1/project', await waitCall(u, 'POST', '/runs/1/project$'));
const ub = (await calls(u, 'POST', '/runs/1/project$'))[0].body;
ok('…its body', JSON.stringify(ub) === JSON.stringify({ name: 'API', scm: 'apps/scm-github', repos: [{ path: '/work/api', repo: 'acme/api' }, { path: '/work/web', repo: 'acme/web' }], branch: 'new', switchHttps: ['/work/api'] }), JSON.stringify(ub));
ok('…done, said', await waitText(u, '#pupg-done', 'task #1 of API'));
await u.click('#pupg .dlg-ft .btn:not(.ghost)');
ok('the conversation is the project\'s task now: its crumb', await waitText(u, '#projcrumb', 'API ›'));
ok('…and no more upgrade', await (async () => { await u.click('#sbxbadge'); await u.waitForSelector('#sbxpop'); return !(await u.$('#sbx-upgrade')); })());
ok('no page errors (upgrade)', ue.length === 0, ue.join(' | '));

// a refusal is said in the dialog
const { page: u2, errors: ue2 } = await open(moreSeed(), { hash: '#c=1' });
await u2.waitForSelector('#sbxbadge');
await u2.click('#sbxbadge');
await u2.click('#sbx-upgrade');
await u2.waitForSelector('#pupg-name');
await u2.fill('#pupg-name', 'internal');
await u2.click('#pupg-make');
ok('a refusal is said there', await waitText(u2, '#pupg-err', 'internal reach'));
ok('no page errors (refused upgrade)', ue2.length === 0, ue2.join(' | '));

// === a person's partition: team projects =========================================================================
const tseed = teamSeed();
const part = () => { window.xbin.partition = 'user:alice'; };
const { page: t, errors: te } = await open(tseed, { hash: '#proj=9', init: part });
await t.waitForSelector('.pteamboard .tbrow');
const rows = await t.$$eval('.pteamboard .tbrow', (els) => els.map((e) => ({ key: e.dataset.key, col: e.closest('.pcol').dataset.col, stale: e.classList.contains('stale'), open: !!e.querySelector('.tbopen'), hide: !!e.querySelector('.tbhide') })));
ok('the team board: each member\'s tasks by column', JSON.stringify(rows.map((r) => `${r.col}:${r.key}`)) === '["working:alice:1","pr:bob:3","done:carl:2"]', JSON.stringify(rows));
ok('…read at the shared space', (await calls(t, 'GET', '/projects/9/board')).every((c) => c.home === 'global'));
ok('…only your own row opens (yours)', JSON.stringify(rows.map((r) => r.open)) === '[true,false,false]');
ok('…a member who left greyed and said', rows[2].stale && (await t.textContent('.tbrow[data-key="carl:2"]')).includes('no longer a member'));
ok('…its text plain', (await t.textContent('.tbrow[data-key="alice:1"] .ptt')) === 'Alice\'s **task** <b>x</b>');
const bobLinks = await t.$$eval('.tbrow[data-key="bob:3"] a[href]', (els) => els.map((a) => a.getAttribute('href')));
ok('…links only when https', JSON.stringify(bobLinks) === '["https://github.com/acme/web/pull/7"]', JSON.stringify(bobLinks));
ok('…waiting said', (await t.textContent('.tbrow[data-key="bob:3"]')).includes('waiting for a review'));
ok('the owner may hide a row', rows.every((r) => r.hide));
await t.click('.tbrow[data-key="carl:2"] .tbhide');
ok('Hide: POST …/board/carl/2/hide', await waitCall(t, 'POST', '/projects/9/board/carl/2/hide$'));
ok('…and it goes', await t.waitForFunction(() => !document.querySelector('.tbrow[data-key="carl:2"]'), null, { timeout: 5000 }).then(() => true, () => false));
await t.evaluate(() => { window.__more.board[9][0].state = 'needs-you'; window.__more.board[9][0].col = 'needs-you'; window.__push({ type: 'project', run: 0, root: 0, data: { id: 9, change: 'board' } }); });
ok('a board event reads it again', await t.waitForFunction(() => document.querySelector('.tbrow[data-key="alice:1"]')?.closest('.pcol').dataset.col === 'needs-you', null, { timeout: 5000 }).then(() => true, () => false));

// the seed sandbox: the definition's owner picks one of theirs the team can see
const seedOpts = await t.$$eval('#pteam-seed-ref option', (els) => els.map((e) => e.value));
ok('the seed: only your sandboxes shared with the team', JSON.stringify(seedOpts) === '["","apps/coding-sandbox|seedbox"]', JSON.stringify(seedOpts));
await t.selectOption('#pteam-seed-ref', 'apps/coding-sandbox|seedbox');
await t.click('#pteam-seed-set');
ok('…set at the shared space', await waitCall(t, 'POST', '/projects/9/seed$'));
const sc = (await calls(t, 'POST', '/projects/9/seed$'))[0];
ok('…as {sandbox: {ref}}', sc.home === 'global' && JSON.stringify(sc.body) === '{"sandbox":{"ref":"apps/coding-sandbox|seedbox"}}', JSON.stringify(sc));
ok('…and shown', await waitText(t, '#pteam-seed', 'apps/coding-sandbox|seedbox'));
ok('…read-only once set (the backend keeps the first seed)', await t.waitForFunction(() => !document.querySelector('#pteam-seed-ref') && !document.querySelector('#pteam-seed-set'), null, { timeout: 5000 }).then(() => true, () => false));

// Work on this: the security part first, accepted by its hash
await t.click('#pteam-work');
await t.waitForSelector('#pteam-form');
ok('…a seed served here: no manager to pick', !(await t.$('#pteam-sbx-mgr')));
await t.click('#pteam-go');
ok('Work on this: asks first', await waitCall(t, 'POST', '/memberships$'));
const m1 = (await calls(t, 'POST', '/memberships$'))[0];
ok('…in your own space, with nothing accepted yet', m1.home === '' && JSON.stringify(m1.body) === '{"team":9,"accept":""}', JSON.stringify(m1));
ok('…then shows what you accept: its setup scripts', await waitText(t, '#psec', 'npm ci && curl https://get.example | sh'));
ok('…and its policy', (await t.textContent('#psec')).includes('be careful') && (await t.textContent('#psec')).includes('go test ./...'));
await t.click('#pteam-go');
ok('Accept and start: the hash shown', await waitCall(t, 'POST', '/memberships$', 2));
ok('…sent', JSON.stringify((await calls(t, 'POST', '/memberships$'))[1].body) === '{"team":9,"accept":"d9"}');
ok('…your half opens', await t.waitForFunction((id) => location.hash === `#proj=${id}`, B + 60, { timeout: 5000 }).then(() => true, () => false));
ok('…it leads to the team board', await waitText(t, '#pteam-def', 'the team\'s board'));
ok('no page errors (team board, work on this)', te.length === 0, te.join(' | '));

// your half with the team's changes waiting
const { page: r, errors: re } = await open(teamSeed(), { hash: `#proj=${B + 20}`, init: part });
await r.waitForSelector('#pteam-review #psec');
const marked = await r.$$eval('#pteam-review tr.chg', (els) => els.map((e) => e.dataset.repo || e.dataset.key));
ok('Review the team project\'s changes: each change marked', JSON.stringify(marked) === '["acme/web","as"]', JSON.stringify(marked));
ok('…side by side', (await r.textContent('#pteam-review tr[data-repo="acme/web"]')).includes('npm ci && ./evil.sh') && (await r.textContent('#pteam-review tr[data-repo="acme/web"]')).includes('npm ci'));
await r.click('#pteam-review-accept');
ok('Accept: the hash shown', await waitCall(r, 'POST', `/memberships/${B + 20}/accept$`));
ok('…sent', JSON.stringify((await calls(r, 'POST', `/memberships/${B + 20}/accept$`))[0].body) === '{"hash":"h2"}');
ok('…and the card goes', await r.waitForFunction(() => !document.querySelector('#pteam-review'), null, { timeout: 5000 }).then(() => true, () => false));
ok('…said', await waitText(r, '.pflash', 'Accepted'));
ok('no page errors (review)', re.length === 0, re.join(' | '));

// Work on this, a definition with no seed: a new sandbox from a manager of yours
const { page: w, errors: we } = await open(teamSeed(), { hash: '#proj=9', init: part });
await w.waitForSelector('#pteam-work');
ok('no seed set: Set the seed offered', !!(await w.$('#pteam-seed-set')));
await w.click('#pteam-work');
await w.waitForSelector('#pteam-sbx-mgr');
ok('no seed: a manager of yours to pick', JSON.stringify(await w.$$eval('#pteam-sbx-mgr option', (els) => els.map((e) => e.value))) === '["apps/coding-sandbox"]');
await w.click('#pteam-go');
await w.waitForSelector('#psec');
await w.click('#pteam-go');
ok('…Accept and start sends {new: {provider}}', await waitCall(w, 'POST', '/memberships$', 2));
const wb = (await calls(w, 'POST', '/memberships$')).map((c) => c.body);
ok('…both times', JSON.stringify(wb) === JSON.stringify([{ team: 9, accept: '', sandbox: { new: { provider: 'apps/coding-sandbox' } } }, { team: 9, accept: 'd9', sandbox: { new: { provider: 'apps/coding-sandbox' } } }]), JSON.stringify(wb));
ok('…your half is made (no 400)', await w.waitForFunction((id) => location.hash === `#proj=${id}`, B + 60, { timeout: 5000 }).then(() => true, () => false));
ok('no page errors (work on this, no seed)', we.length === 0, we.join(' | '));

// an archived half: Work on this again
const aseed = teamSeed();
aseed.projects = [...aseed.projects, { ...aseed.projects[3], id: B + 30, teamRef: 9, name: 'Team site', state: 'archived', defPending: '' }];
aseed.tasks = { ...aseed.tasks, [B + 30]: [] };
const { page: a, errors: ae } = await open(aseed, { hash: '#proj=9', init: part });
await a.waitForSelector('#pteam-work');
ok('an archived half: Work on this again', (await a.textContent('#pteam-work')) === 'Work on this again' && !(await a.$('#pteam-mine')));
await a.click('#pteam-work');
await a.click('#pteam-go');
await a.waitForSelector('#psec');
await a.click('#pteam-go');
ok('…taken up again, and opened', await a.waitForFunction((id) => location.hash === `#proj=${id}`, B + 30, { timeout: 5000 }).then(() => true, () => false));
ok('no page errors (work on this again)', ae.length === 0, ae.join(' | '));

// your half re-reads the definition as its page opens: changes found then are offered
const sseed = teamSeed();
const found = sseed.pending[B + 20];
sseed.projects = sseed.projects.map((x) => (x.id === B + 20 ? { ...x, defPending: '' } : x));
sseed.pending = { [B + 20]: { hash: '', accepted: found.accepted, pending: null } };
sseed.syncOnRead = { [B + 20]: found };
const { page: o, errors: oe } = await open(sseed, { hash: `#proj=${B + 20}`, init: part });
ok('a membership\'s page opening reads its definition', await waitCall(o, 'GET', `/memberships/${B + 20}/pending$`));
ok('…the changes found are offered', await o.waitForSelector('#pteam-review #psec', { timeout: 5000 }).then(() => true, () => false));
ok('…read once', (await calls(o, 'GET', `/memberships/${B + 20}/pending$`)).length === 1);
ok('no page errors (review on open)', oe.length === 0, oe.join(' | '));

// a team project's definition, from your own space
const { page: n, errors: ne } = await open(teamSeed(), { hash: '#proj', init: part });
await n.waitForSelector('#proj-new');
await n.click('#proj-new');
await n.waitForSelector('#pn-kind');
await n.click('#pn-kind input[value="team"]');
ok('a team project: no sandbox of yours', !(await n.$('input[name="pn-sbx"]')) && !!(await n.$('#pn-team-note')));
await n.click('.pnres[data-repo="acme/web"] button');
await n.selectOption('#pn-vis', 'team');
await n.click('#pn-create');
ok('…made at the shared space', await waitCall(n, 'POST', '/projects$'));
const nb = (await calls(n, 'POST', '/projects$'))[0];
ok('…as a team definition, shared, with no sandbox', nb.home === 'global' && nb.body.kind === 'team' && !('sandbox' in nb.body) && nb.body.share.visibility === 'team', JSON.stringify(nb));
ok('no page errors (team definition)', ne.length === 0, ne.join(' | '));

// a phone's width
const { page: ph, errors: pe } = await open(teamSeed(), { width: 390, hash: '#proj=9', init: part });
await ph.waitForSelector('.pteamboard .tbrow');
ok('a phone: the team board fits', await noHScroll(ph));
await ph.evaluate((id) => { location.hash = `#proj=${id}`; }, B + 20);
await ph.waitForSelector('#pteam-review');
ok('…the review card fits', await noHScroll(ph));
ok('no page errors (phone)', pe.length === 0, pe.join(' | '));

await browser.close();
done('projects-more');
