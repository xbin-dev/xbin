// projects.mjs — Projects on the web (API.md §Projects in the UI) over
// projSeed() and PROJ_STUB (test/projects-stub.mjs): the sidebar entry with
// tasks that need you; #proj and the list (yours, archived); a project's
// board — its columns, a card's words, PR chips with checks only while the
// task has no CI, ext.card's chips; New task and From issues… (issue text
// plain and untrusted, the refusals said); the Settings tab — status,
// repos, the policy (unknown keys kept, a stale version read again),
// members (what is typed stays its project's), archive, delete; the new-project form; a task's conversation —
// the crumb back, the branch and PR chips, Open PR once the route exists,
// the prep card with Retry, the sign-in card (polled, then the task looked
// at again — a person's partition's); a `project` event; #proj=<id> on
// load; signing in to the provider offered only in a person's partition
// (from the settings, then Forget), never at an unpartitioned agent or the
// shared space; a phone's width; a person's partition (two homes, a team
// definition's page with no tasks); and the shared space's team definitions
// (no seed at first; a seed of yours only when shared with the team).
//
//   node test/projects.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { PROJ_STUB, projSeed, partitionSeed } from './projects-stub.mjs';

const { ok, done } = checker();
const browser = await launch();

async function open(seed, { width = 1280, hash = '', init = null } = {}) {
  const ctx = await browser.newContext({ viewport: { width, height: 900 } });
  await serveTile(ctx);
  await ctx.addInitScript(STUB, seed);
  await ctx.addInitScript(PROJ_STUB, seed);
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

// === an unpartitioned agent ============================================================================
const seed = projSeed();
const { page, errors } = await open(seed);
await page.waitForSelector('#projentry');

// --- the entry and the list ---------------------------------------------------------------------------
ok('the sidebar entry: Projects, with the tasks that need you', await page.waitForFunction(() => document.querySelector('#projentry').textContent.replace(/\s+/g, '') === 'Projects3',
  null, { timeout: 5000 }).then(() => true, () => false), await page.textContent('#projentry'));
ok('task conversations are not chats', !(await page.textContent('#runs')).includes('Fix the login loop'));
await page.click('#projentry');
await page.waitForSelector('.pcard[data-pid="7"]');
ok('#proj is in the address', await page.evaluate(() => location.hash === '#proj'));
ok('the entry is on', await page.$eval('#projentry', (e) => e.classList.contains('on')));
const groups = await page.$$eval('.projs-page h5', (els) => els.map((e) => e.textContent));
ok('yours, then the archived', JSON.stringify(groups) === '["Projects","Archived"]', groups.join(' | '));
const card = await page.textContent('.pcard[data-pid="7"]');
ok('a card: name, repos, slots and counts', card.includes('Web') && card.includes('acme/web, acme/api') && card.includes('1/3 at work') && card.includes('3 need you'), card);
ok('the top bar says Projects', (await page.textContent('#top .title')) === 'Projects');

// --- a project's board -------------------------------------------------------------------------------------
await page.evaluate(async () => {
  const { ext } = await import('/web-ext.js');
  ext.register({ card: (t) => (t.ci ? `CI ${t.ci.state}` : null) }); // a chip of another module's (CI's, V)
});
ok('a project card is a button the keyboard reaches', (await page.getAttribute('.pcard[data-pid="7"]', 'role')) === 'button' && (await page.getAttribute('.pcard[data-pid="7"]', 'tabindex')) === '0');
await page.focus('.pcard[data-pid="7"]');
await page.keyboard.press('Enter'); // opens it, as a click does
await page.waitForSelector('.pboard .ptask[data-n="1"]');
ok('…the board\'s search has a name', (await page.getAttribute('.pq', 'aria-label')) === 'Find a task');
ok('#proj=7 is in the address', await page.evaluate(() => location.hash === '#proj=7'));
const cols = await page.$$eval('.pboard .pcol', (els) => els.map((e) => `${e.dataset.col}:${[...e.querySelectorAll('.ptask')].map((t) => t.dataset.n).join(',')}`));
ok('the columns, each with its tasks (newest first)', JSON.stringify(cols) === '["queued:4","working:2","needs-you:8,6,3","pr:1","done:5"]', JSON.stringify(cols));
const t1 = await page.textContent('.ptask[data-n="1"]');
ok('a card: #n, title, state, branch, its PR with its checks', t1.includes('#1') && t1.includes('Fix the login loop') && t1.includes('awaiting review')
  && t1.includes('xbin/k3x9qa/1-task-1') && t1.includes('PR #42 open ✗'), t1);
ok('…the PR a link to the platform', (await page.getAttribute('.ptask[data-n="1"] a.pchip', 'href')) === 'https://github.com/acme/web/pull/42');
const t5 = await page.textContent('.ptask[data-n="5"]');
ok('with a CI summary, the PR chip leaves the checks to the CI chip (ext.card)', t5.includes('PR #40 merged') && !t5.includes('✓ ↗') && t5.includes('CI success'), t5);
ok('the board has no horizontal scroll', await noHScroll(page));

// --- a new task ------------------------------------------------------------------------------------------------
await page.click('#ptask-new');
await page.waitForSelector('#ptask-form');
const agentOpts = await page.$$eval('#ptask-form select[data-f="agent"] option', (els) => els.map((e) => [e.value, e.textContent.trim()]));
ok('who works on it: the project\'s default said as what it does, never a "builtin" a task can\'t ask for',
  agentOpts.length > 0 && agentOpts[0][0] === '' && /default|built-in agent/.test(agentOpts[0][1]) && !agentOpts.some(([v]) => v === 'builtin'), JSON.stringify(agentOpts));
await page.click('#ptask-create');
ok('a task needs what to do', await waitText(page, '#ptask-form .err', 'Say what to do'));
await page.fill('#ptask-form textarea', 'Make the header sticky');
await page.selectOption('#ptask-form select >> nth=0', 'big');
await page.uncheck('#ptask-form input[data-repo="api"]');
await page.click('#ptask-create');
ok('Create task posts it', await waitCall(page, 'POST', '/projects/7/tasks$'));
const tb = (await calls(page, 'POST', '/projects/7/tasks$'))[0].body;
ok('…its spec: text, size, the repos left in', tb.text === 'Make the header sticky' && tb.size === 'big' && JSON.stringify(tb.repos) === '["web"]' && !tb.agent && !tb.title, JSON.stringify(tb));
ok('…and the board shows it', await page.waitForSelector('.pcol[data-col="queued"] .ptask[data-n="9"]', { timeout: 5000 }).then(() => true, () => false));
ok('…and says so', await waitText(page, '.pflash', 'Task #9 made'));

// --- tasks from issues -------------------------------------------------------------------------------------------
await page.click('#ptask-issues');
await page.waitForSelector('#ppicker .pissue[data-issue="11"]');
ok('issue text is plain text, never HTML', await page.evaluate(() => !document.querySelector('#ppicker .pissue img') && !window.__pwned
  && document.querySelector('#ppicker .pissue[data-issue="11"] .untrusted').textContent.includes('<img src=x')));
ok('…and marked as untrusted', (await page.textContent('#ppicker')).includes('anyone may have written it'));
await page.check('#ppicker .pissue[data-issue="11"] input');
await page.check('#ppicker .pissue[data-issue="13"] input');
ok('the button counts the picks', (await page.textContent('#ppick-make')) === 'Make 2 tasks');
await page.click('#ppick-make');
ok('From issues posts a batch', await waitCall(page, 'POST', '/projects/7/tasks/batch$'));
const bb = (await calls(page, 'POST', '/projects/7/tasks/batch$'))[0].body;
ok('…of the picked issues', JSON.stringify(bb.issues) === '[{"repo":"acme/web","number":11},{"repo":"acme/web","number":13}]', JSON.stringify(bb));
ok('…a refused one is said', await waitText(page, '#ppicker .err', 'acme/web#13: that issue is locked'));
ok('…and the made one counted', await waitText(page, '.pflash', '1 task made, 1 refused'));

// --- a `project` event: the board is read again ---------------------------------------------------------------------
const before = (await calls(page, 'GET', '/projects/7/tasks')).length;
await page.evaluate(() => {
  const t = window.__proj.tasks[7].find((x) => x.n === 2);
  Object.assign(t, { column: 'needs-you', state: 'needs-you', runStatus: 'waiting_input' });
  window.__push({ type: 'project', run: 0, root: 0, data: { id: 7, change: 'task', n: 2 } });
  window.__push({ type: 'project', run: 0, root: 0, data: { id: 7, change: 'task', n: 2 } });
});
ok('a project event moves the card', await page.waitForSelector('.pcol[data-col="needs-you"] .ptask[data-n="2"]', { timeout: 5000 }).then(() => true, () => false));
await page.waitForTimeout(400);
ok('…two events, one read (coalesced)', (await calls(page, 'GET', '/projects/7/tasks')).length === before + 1, String((await calls(page, 'GET', '/projects/7/tasks')).length - before));

// --- settings -------------------------------------------------------------------------------------------------------
await page.click('[data-tab="settings"]');
await page.waitForSelector('#pstatus-repos tr[data-repo="api"]');
const st = await page.textContent('#pset-status');
ok('status: sandbox, repos, credentials, jobs, a warning', st.includes('running') && st.includes('not protected') && st.includes('alice-gh') && st.includes('fetch · web')
  && st.includes('no protection'), st.replace(/\s+/g, ' ').slice(0, 300));
ok('…never a token', !/ghs_|gho_|ghu_/.test(st));
await page.waitForTimeout(300);
ok('your sign-in: not offered at an unpartitioned agent (its routes answer 409 there)', !(await page.$('#pset-status #psignin-start'))
  && !st.includes('Your sign-in') && (await calls(page, 'GET', '/projects/scm/signin')).length === 0);
await page.click('#pset-warm');
ok('Warm', await waitCall(page, 'POST', '/projects/7/warm$'));

// repos
await page.fill('.prepo[data-repo="web"] textarea', 'npm ci');
await page.click('.prepo[data-repo="web"] [data-act="setup"]');
ok('a setup script is saved', await waitCall(page, 'PATCH', '/projects/7/repos/web$'));
ok('…as its setup', (await calls(page, 'PATCH', '/projects/7/repos/web$'))[0].body.setup === 'npm ci');
await page.click('.prepo[data-repo="api"] [data-act="remove"]');
ok('removing a repo open tasks use asks again, then forces', await waitCall(page, 'DELETE', '/projects/7/repos/api\\?force=1$'));
ok('…and it is gone', await page.waitForFunction(() => !document.querySelector('.prepo[data-repo="api"]'), null, { timeout: 5000 }).then(() => true, () => false));
await page.fill('#pset-addrepo', 'acme/docs');
await page.click('#pset-addrepo-go');
ok('a repo is added', await waitCall(page, 'POST', '/projects/7/repos$'));
ok('…by owner/name', (await calls(page, 'POST', '/projects/7/repos$'))[0].body.repo === 'acme/docs');

// policy: every group, an edit saved with the version, unknown keys kept; a stale version read again
const groupsP = await page.$$eval('#pset-policy .pgroup', (els) => els.map((e) => e.dataset.group));
ok('the policy in its groups', groupsP.join() === 'tasks,prs,ci,setup,big,cleanup,coord', groupsP.join());
const keys = await page.$$eval('#pset-policy :is(input, select, textarea)[id^="pol-"]', (els) => els.length);
ok('every key is there', keys === 36, String(keys));
await page.fill('#pol-maxTasks', '5');
await page.dispatchEvent('#pol-maxTasks', 'change');
await page.evaluate(() => { document.querySelector('.pgroup[data-group="ci"]').open = true; });
await page.uncheck('#pol-ci-autoFix');
await page.click('#pol-save');
ok('Save policy patches it', await waitCall(page, 'PATCH', '/projects/7$'));
const pb = (await calls(page, 'PATCH', '/projects/7$'))[0].body;
ok('…with the version read, the edits, unknown keys kept', pb.version >= 3 && pb.policy.maxTasks === 5 && pb.policy.ci.autoFix === false && pb.policy.ci.maxPerDay === 5
  && pb.policy.futureKey && pb.policy.futureKey.kept === true, JSON.stringify(pb));
ok('…saved', await page.waitForSelector('#pol-saved', { timeout: 5000 }).then(() => true, () => false));
await page.evaluate(() => { window.__proj.projects.find((p) => p.id === 7).version += 5; });
await page.fill('#pol-maxTasks', '6');
await page.dispatchEvent('#pol-maxTasks', 'change');
await page.click('#pol-save');
ok('a stale version: said, the project read again, the edit kept', await waitText(page, '#pol-err', 'Someone changed this policy meanwhile')
  && (await page.inputValue('#pol-maxTasks')) === '6');
await page.click('#pol-save');
ok('…saved again at the version read', await page.waitForSelector('#pol-saved', { timeout: 5000 }).then(() => true, () => false)
  && (await calls(page, 'PATCH', '/projects/7$')).pop().body.policy.maxTasks === 6);

// members
ok('members: the owner and bob', (await page.textContent('#pset-members')).includes('alice') && !!(await page.$('#pset-members .pmember[data-user="bob"]')));
await page.fill('#pset-member', 'carol');
await page.click('#pset-member-add');
ok('a member is added', await waitCall(page, 'POST', '/projects/7/members$'));
ok('…as a participant', JSON.stringify((await calls(page, 'POST', '/projects/7/members$'))[0].body) === '{"user":"carol","role":"participant"}');
await page.selectOption('#pset-vis', 'viewer');
ok('team visibility', await waitCall(page, 'PATCH', '/projects/7$', 3));
const vb = (await calls(page, 'PATCH', '/projects/7$')).pop().body;
ok('…the team reads it', vb.visibility === 'team' && vb.teamRole === 'viewer', JSON.stringify(vb));
{
  const asked = [];
  const note = (d) => asked.push(d.message());
  page.on('dialog', note);
  await page.click('#pset-members .pmember[data-user="carol"] button');
  ok('Remove asks first', await waitCall(page, 'DELETE', '/projects/7/members/carol$') && asked.join() === 'Remove carol from Web? They lose their access; you can add them back.', asked.join(' | '));
  page.off('dialog', note);
}
// typed, never submitted: it stays this project's (checked on the next project's Settings below)
await page.fill('#pset-addrepo', 'acme/leftover');
await page.fill('#pset-member', 'mallory');
await page.selectOption('#pset-member-role', 'viewer');

// --- a task's conversation ------------------------------------------------------------------------------------------------
await page.evaluate(() => { location.hash = '#c=101'; });
await page.waitForSelector('#ptchips');
ok('the crumb: back to its project', (await page.textContent('#projcrumb')) === 'Web ›');
const chips = await page.$$eval('#ptchips .pchip', (els) => els.map((e) => `${e.dataset.kind}:${e.textContent.trim()}`));
ok('the branch, the PR with its checks, the setup', JSON.stringify(chips) === '["branch:⎇ xbin/k3x9qa/1-task-1 ↗","pr:PR #42 open ✗ ↗","setup:setup ✓"]', JSON.stringify(chips));
ok('…the branch a link to it', (await page.getAttribute('#ptchips a[data-kind="branch"]', 'href')) === 'https://github.com/acme/web/tree/xbin/k3x9qa/1-task-1');
ok('Open PR: hidden on a task with an open PR', !(await page.$('#ptask-pr')));
await page.evaluate(() => { location.hash = '#c=102'; });
await page.waitForFunction(() => document.querySelector('#top .title')?.textContent === 'Add dark mode');
await page.waitForTimeout(300);
ok('…and while the backend has no such route (probed, nothing opened)', !(await page.$('#ptask-pr')) && (await calls(page, 'GET', '/runs/\\d+/task/pr$')).length === 1
  && (await calls(page, 'POST', '/task/pr$')).length === 0);
ok('the pinned task\'s project section', await page.click('.tasktoggle').then(() => waitText(page, '#ptasksec', 'task #2')));
await page.click('#projcrumb');
ok('the crumb opens the project', await page.waitForFunction(() => location.hash === '#proj=7' && document.querySelector('.pboard'), null, { timeout: 5000 }).then(() => true, () => false));

// a failed workspace: the prep card with Retry
await page.evaluate(() => { location.hash = '#c=108'; });
await page.waitForSelector('#pprep');
const prep = await page.textContent('#pprep');
ok('the prep card: failed, the step, the error', prep.includes('preparing the workspace failed') && prep.includes('clone failed') && prep.includes('repository not found'), prep);
await page.click('#pprep-retry');
ok('Retry', await waitCall(page, 'POST', '/runs/108/task/retry$'));
ok('…and the task is read again', await waitCall(page, 'GET', '/runs/108/task$'));
// preparing: the steps
await page.evaluate(() => { location.hash = '#c=104'; });
await page.waitForSelector('#pprep[data-ws="preparing"]');
const steps = await page.$$eval('#pprep .pstep', (els) => els.map((e) => `${e.dataset.repo}:${e.dataset.tone}`));
ok('preparing: a step per repo', JSON.stringify(steps) === '["web:ok","api:idle"]' && (await page.textContent('#pprep')).includes('cloning acme/api'), JSON.stringify(steps));
// --- a new project --------------------------------------------------------------------------------------------------
await page.click('#projentry');
await page.waitForSelector('#proj-new');
await page.click('#proj-new');
await page.waitForSelector('#proj-form .pnres[data-repo="acme/web"]');
ok('the provider and what you can reach', (await page.inputValue('#pn-scm')) === 'apps/scm-github' && (await page.$$('#proj-form .pnres')).length === 3);
ok('an archived repo can\'t be added', await page.isDisabled('#proj-form .pnres[data-repo="acme/attic"] button'));
ok('the provider: projects work as its bot, no sign-in offered', await waitText(page, '.pnyou', "work as the provider's bot") && !(await page.$('#proj-form #psignin-start')));
await page.click('#proj-form .pnres[data-repo="acme/web"] button');
ok('adding a repo names the project after it', (await page.inputValue('#pn-name')) === 'web');
await page.fill('#proj-form .pnrepo[data-repo="acme/web"] textarea', 'make deps');
await page.fill('#pn-name', 'Web 2');
await page.click('#pn-create');
ok('Create posts the project', await waitCall(page, 'POST', '/projects$'));
const nb = (await calls(page, 'POST', '/projects$'))[0].body;
ok('…its provider, repos with setup, a new sandbox, the policy basics', nb.name === 'Web 2' && nb.scm === 'apps/scm-github'
  && JSON.stringify(nb.repos) === '[{"repo":"acme/web","setup":"make deps"}]' && nb.sandbox.new && nb.sandbox.new.provider === 'apps/coding-sandbox'
  && nb.sandbox.new.egress === 'internet' && nb.policy.maxTasks === 3 && nb.policy.autoPR === 'off' && !nb.kind && !nb.share, JSON.stringify(nb));
ok('…then opens it', await page.waitForFunction(() => location.hash === '#proj=50', null, { timeout: 5000 }).then(() => true, () => false));
await page.waitForSelector('.pboard');
await page.click('[data-tab="settings"]');
await page.waitForSelector('#pset-project');
ok('another project\'s Settings: the repo and the person typed on Web\'s are not carried over', (await page.inputValue('#pset-addrepo')) === ''
  && (await page.inputValue('#pset-member')) === '' && (await page.inputValue('#pset-member-role')) === 'participant'
  && !(await calls(page, 'POST', '/repos$|/members$')).some((c) => /leftover|mallory/.test(JSON.stringify(c.body))));
// archive, then delete (its sandbox too)
await page.click('#pset-archive');
ok('Archive', await page.waitForFunction(() => window.__calls.some((c) => c.method === 'PATCH' && /projects\/50$/.test(c.url) && JSON.parse(c.body).state === 'archived'), null, { timeout: 5000 }).then(() => true, () => false));
await page.selectOption('#pset-del-sbx', 'delete');
await page.click('#pset-delete');
ok('Delete, confirmed, with its sandbox', await waitCall(page, 'DELETE', '/projects/50\\?sandbox=delete$'));
ok('…back to the list', await page.waitForFunction(() => location.hash === '#proj' && !document.querySelector('.pcard[data-pid="50"]'), null, { timeout: 5000 }).then(() => true, () => false));


// Open PR, once the backend has the route
await page.evaluate(() => { window.__proj.prRoute = true; });
const { page: p2, errors: e2 } = await open({ ...projSeed(), prRoute: true }, { hash: '#c=102' });
await p2.waitForSelector('#ptask-pr');
ok('Open PR, where the route exists', true);
await p2.click('#ptask-pr');
ok('…confirmed, posted', await waitCall(p2, 'POST', '/runs/102/task/pr$'));
ok('#c=102 on load opens the task', (await p2.textContent('#projcrumb')) === 'Web ›');
ok('no page errors (Open PR)', e2.length === 0, e2.join(' | '));

// #proj=7 on load
const { page: p3, errors: e3 } = await open(projSeed(), { hash: '#proj=7' });
ok('#proj=7 on load opens the project', await p3.waitForSelector('.pboard .ptask[data-n="1"]', { timeout: 5000 }).then(() => true, () => false));
ok('no page errors (#proj=7)', e3.length === 0, e3.join(' | '));

// a phone
const { page: p4, errors: e4 } = await open(projSeed(), { hash: '#proj=7', width: 390 });
await p4.waitForSelector('.pboard .ptask[data-n="1"]');
ok('a phone: the board in one column, no horizontal scroll', await noHScroll(p4)
  && await p4.$$eval('.pboard .pcol', (els) => new Set(els.map((e) => Math.round(e.getBoundingClientRect().left))).size === 1));
ok('no page errors (phone)', e4.length === 0, e4.join(' | '));

ok('no page errors', errors.length === 0, errors.join(' | '));

// === a person's partition: two homes ===================================================================
const B = 2 ** 40;
const pseed = partitionSeed();
const { page: q, errors: qe } = await open(pseed, { init: () => { window.xbin.partition = 'user:alice'; } });
await q.click('#projentry');
await q.waitForSelector(`.pcard[data-pid="${B + 7}"]`);
const qg = await q.$$eval('.projs-page h5', (els) => els.map((e) => e.textContent));
ok('a partition: yours and team projects', JSON.stringify(qg) === '["Your projects","Team projects"]', qg.join(' | '));
const homes = [...new Set((await calls(q, 'GET', '/projects(\\?|$)')).map((c) => c.home))].sort();
ok('…read from both homes', JSON.stringify(homes) === '["","global"]', JSON.stringify(homes));
ok('…a team one says whose', (await q.textContent('.pcard[data-pid="9"]')).includes("carol's"));
await q.click(`.pcard[data-pid="${B + 7}"]`);
await q.waitForSelector('.pboard .ptask');
ok('your own project is read at your own partition', (await calls(q, 'GET', `/projects/${B + 7}$`)).every((c) => c.home === ''));
await q.click('[data-tab="settings"]');
await q.waitForSelector('#pset-members');
ok('…and has no members: it is yours alone', (await q.textContent('#pset-members')).includes('yours alone') && (await calls(q, 'GET', '/members$')).length === 0);
await q.click('#top .crumb');
await q.click('.pcard[data-pid="9"]');
ok('a team project\'s definition: a line saying its tasks run in each member\'s space', await waitText(q, '#pdef-note', "each member's own space"));
ok('…read at the shared space', (await calls(q, 'GET', '/projects/9$')).every((c) => c.home === 'global') && (await calls(q, 'GET', '/projects/9$')).length > 0);
ok('…no tasks read, no task actions', (await calls(q, 'GET', '/projects/9/tasks')).length === 0
  && !(await q.$('#ptask-new')) && !(await q.$('#ptask-issues')) && !(await q.$('.pboard')));
// the sign-in card of a task of yours: the code, to the person who must sign in
await q.evaluate((id) => { location.hash = `#c=${id}`; }, B + 106);
await q.waitForSelector('#ptask-signin');
ok('the sign-in card: the device code and page', (await q.textContent('#ptask-signin-code')) === 'ABCD-1234'
  && (await q.getAttribute('#ptask-signin-link', 'href')) === 'https://github.com/login/device');
ok('…polled; once signed in, the task is looked at again', await waitCall(q, 'POST', `/runs/${B + 106}/task/refresh$`) && await waitText(q, '#ptask-signin', 'Signed in'));
ok('no page errors (partition)', qe.length === 0, qe.join(' | '));

// signing in from the settings, in your partition: the code to you, polled until done, then Forget
const { page: p5, errors: e5 } = await open(partitionSeed(), { hash: `#proj=${B + 7}`, init: () => { window.xbin.partition = 'user:alice'; } });
await p5.waitForSelector('.pboard .ptask');
await p5.click('[data-tab="settings"]');
ok('your sign-in: offered in your partition', await p5.waitForSelector('#pset-status #psignin-start', { timeout: 5000 }).then(() => true, () => false));
await p5.click('#psignin-start');
ok('sign-in: the device code, to you', await waitText(p5, '#psignin-code', 'WDJB-MJHT'));
ok('…and its page, a link', (await p5.getAttribute('#psignin-link', 'href')) === 'https://github.com/login/device');
ok('…polled until done', await waitText(p5, '#pset-status .psignin[data-state="done"]', 'Signed in to GitHub as alice-gh'));
await p5.click('#psignin-forget');
ok('…and Forget', await waitCall(p5, 'DELETE', '/projects/scm/signin\\?scm=apps%2Fscm-github$'));
ok('no page errors (signing in)', e5.length === 0, e5.join(' | '));

// === a partitioned agent's shared space: a team project's definition ======================================
const box = (name, visibility, mine = true) => ({ ref: `apps/coding-sandbox|sb-${name}`, provider: 'apps/coding-sandbox', manager: 'Coding sandboxes', id: `sb-${name}`, name,
  state: 'running', egress: 'internet', visibility, image: { id: 'base' }, owner: { user: mine ? 'alice' : 'bob' }, mine, canUse: true, workdir: '/work' });
const { page: g, errors: ge } = await open(projSeed({ sandboxes: [box('mine-private', 'private'), box('mine-team', 'team'), box('bobs-team', 'team', false)] }),
  { init: () => { window.xbin.partition = 'global'; } });
await g.click('#projentry');
await g.waitForSelector('#proj-new');
await g.click('#proj-new');
await g.waitForSelector('#proj-form .pnres[data-repo="acme/web"]');
ok('the shared space: a seed sandbox, none at first', (await g.textContent('#proj-form')).includes('Its seed sandbox') && await g.isChecked('#proj-form input[name="pn-sbx"][value="none"]'));
ok('…shared with the team at first', (await g.inputValue('#pn-vis')) === 'team');
ok('…no sign-in offered: each member signs in from their own space', await waitText(g, '.pnyou', 'each member signs in') && !(await g.$('#proj-form #psignin-start')));
await g.click('#proj-form .pnres[data-repo="acme/web"] button');
await g.click('#pn-create');
ok('…posted as a team definition, shared, without a seed', await waitCall(g, 'POST', '/projects$'));
const gb = (await calls(g, 'POST', '/projects$'))[0].body;
ok('…its body', gb.kind === 'team' && gb.share && gb.share.visibility === 'team' && Array.isArray(gb.share.members) && !('sandbox' in gb), JSON.stringify(gb));
// a seed of your own: only one you have shared with the team (members fork from it only when they can see it)
await g.click('#top .crumb');
await g.waitForSelector('#proj-new');
await g.click('#proj-new');
await g.waitForSelector('#proj-form .pnres[data-repo="acme/web"]');
await g.check('#proj-form input[name="pn-sbx"][value="pick"]');
await g.waitForSelector('#pn-sbx-ref');
const seeds = await g.$$eval('#pn-sbx-ref option', (els) => els.map((e) => e.value).filter(Boolean));
ok('a seed of yours: only your sandbox shared with the team is offered', JSON.stringify(seeds) === '["apps/coding-sandbox|sb-mine-team"]', JSON.stringify(seeds));
if (seeds.includes('apps/coding-sandbox|sb-mine-team')) await g.selectOption('#pn-sbx-ref', 'apps/coding-sandbox|sb-mine-team');
await g.fill('#pn-name', 'Seeded');
await g.click('#proj-form .pnres[data-repo="acme/web"] button');
await g.click('#pn-create');
ok('…posted as the seed, by its ref', await waitCall(g, 'POST', '/projects$', 2));
const gs = ((await calls(g, 'POST', '/projects$'))[1] || {}).body || {};
ok('…its body', gs.kind === 'team' && JSON.stringify(gs.sandbox) === '{"ref":"apps/coding-sandbox|sb-mine-team"}', JSON.stringify(gs));
ok('no page errors (shared space)', ge.length === 0, ge.join(' | '));

await browser.close();
done('projects');
