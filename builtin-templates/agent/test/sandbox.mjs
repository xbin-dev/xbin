// sandbox.mjs — coding sandboxes (D115) on the web: the composer's picker
// (#ssel) shows only where the class has the sandbox toolset and binds what
// you pick (PATCH /runs {sandbox}; at home the new chat is asked with it);
// the top bar's ▣ badge says the sandbox and its working directory, or why
// the binding no longer resolves, and its popover sets the cwd, switches
// among the attached ones and detaches; the Sandboxes dialog lists them with
// their lifecycle actions and creates one for the conversation; the sandbox
// tool cards read their command and footer; a grant for `sandboxes` is the
// generic grant card.
//
//   node test/sandbox.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';

const { ok, done } = checker();
const now = Math.floor(Date.now() / 1000);
const MGR = 'apps/coding-sandbox';
const call = (id, name, args) => ({ id, type: 'function', function: { name, arguments: JSON.stringify(args) } });
const msg = (id, role, content, extra = {}) => ({ id, runId: 1, seq: id, role, content, created: now - 60 + id, ...extra });
const coding = { id: 'coding', name: 'Coding', icon: '▣', toolsets: ['sandbox', 'web', 'files'], managers: 'all', sandboxEgress: ['none', 'internet'] };
const internal = { id: 'internal', name: 'Internal', icon: '🔒', toolsets: ['internal', 'files'], managers: [], sandboxEgress: [] };
const sb = (id, extra = {}) => ({ ref: `${MGR}|${id}`, provider: MGR, manager: 'Coding sandboxes', id, name: id, state: 'running', egress: 'none',
  visibility: 'private', owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work',
  caps: ['exec', 'files', 'tar', 'archive'], image: { id: 'base', title: 'Debian' }, lastActive: Date.now() - 60e3, ...extra });
const bind = (id, extra = {}) => ({ ref: `${MGR}|${id}`, name: id, cwd: '/work', manager: 'Coding sandboxes', egress: 'none', by: 'admin', ...extra });

const seed = {
  runs: [
    { id: 1, title: 'fix the build', status: 'idle', parentId: 0, rootId: 1, updated: now },
    { id: 2, title: 'internal things', status: 'idle', parentId: 0, rootId: 2, updated: now },
    { id: 3, title: 'lost box', status: 'idle', parentId: 0, rootId: 3, updated: now },
    { id: 4, title: 'may I', status: 'waiting_input', parentId: 0, rootId: 4, updated: now, owner: 'admin',
      pendingState: { kind: 'approval', grant: 'sandboxes', grantAsk: 'create a coding sandbox “scratch” for this conversation',
        toolCalls: [call('g1', 'sandbox_create', { name: 'scratch' })] } },
  ],
  views: {
    1: {
      access: 'owner', class: coding, config: { sandbox: bind('api'), attached: [bind('api')] },
      run: { id: 1, title: 'fix the build', status: 'idle', parentId: 0, rootId: 1 },
      messages: [
        msg(1, 'user', 'make the tests pass'),
        msg(2, 'assistant', '', { toolCalls: [
          call('b1', 'bash', { command: 'go test ./...', summary: 'Run the tests' }),
          call('b2', 'grep', { pattern: 'TODO', path: 'src' }),
          call('b3', 'edit', { path: '/work/api/main.go', old_string: 'return nil', new_string: 'return err' }),
          call('b4', 'bash', { command: 'make serve', background: true }),
        ] }),
        msg(3, 'tool', '--- FAIL: TestX\nFAIL\n[exit 1 · 14s · job 3]', { toolCallId: 'b1', name: 'bash' }),
        msg(4, 'tool', 'src/a.go:3: // TODO\nsrc/b.go:9: // TODO', { toolCallId: 'b2', name: 'grep' }),
        msg(5, 'tool', 'edited /work/api/main.go (1 replacement)\n  12\treturn err', { toolCallId: 'b3', name: 'edit' }),
        msg(6, 'tool', 'serving\n[still running after 2m00s · job 4 — bash_output {"job": 4} follows it, bash_kill {"job": 4} stops it]',
          { toolCallId: 'b4', name: 'bash' }),
      ],
    },
    2: { access: 'owner', class: internal, config: {}, run: { id: 2, title: 'internal things', status: 'idle', parentId: 0, rootId: 2 } },
    3: { access: 'owner', class: coding, config: { sandbox: bind('vanished', { cwd: '/srv' }) },
      run: { id: 3, title: 'lost box', status: 'idle', parentId: 0, rootId: 3 } },
    4: { access: 'owner', class: coding, config: {} }, // its run: the list's row (seed.runs)
  },
  sandboxes: [sb('api', { boundTo: [1] }), sb('web', { state: 'stopped', lastActive: Date.now() - 3600e3 }),
    sb('team-box', { mine: false, owner: { user: 'carol' }, visibility: 'team', canManage: false, canEdit: false }),
    // an internal-reach conversation has worked in it; its network opens at the next start
    sb('vault', { labels: { 'xbin.agent/internal': '1' }, lastActive: Date.now() - 7200e3 }),
    sb('pending', { egressNext: 'open', lastActive: Date.now() - 7300e3 })],
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
// a new chat opens: POST /ask answers its run
await ctx.addInitScript(() => window.__route('POST', /\/ask$/, () => {
  const run = { id: 9, title: 'build it', status: 'running', parentId: 0, rootId: 9 };
  window.__runs.push(run);
  return window.__json(run);
}));
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('dialog', (d) => d.accept());
const calls = (method, re) => page.evaluate(([m, r]) => window.__calls.filter((c) => c.method === m && new RegExp(r).test(c.url)), [method, re.source]);
const lastBody = async (method, re) => JSON.parse((await calls(method, re)).pop().body);
const hidden = (sel) => page.$eval(sel, (el) => el.hidden);

// --- home: the picker follows the class for new chats ------------------------------------
await page.goto(`${ORIGIN}/`);
await page.waitForSelector('#tset');
ok('home, the internal class: no sandbox picker', await hidden('#ssel'));
await page.click('#tset');
await page.click('.clsmenu .mi[data-class="coding"]');
await page.waitForFunction(() => !document.getElementById('ssel').hidden);
ok('the coding class: the picker shows', true);
await page.waitForFunction(() => [...document.querySelectorAll('#ssel option')].some((o) => o.value.endsWith('|web')));
const groups = await page.$$eval('#ssel optgroup', (els) => els.map((e) => e.label));
ok('grouped: Yours · Team, then the actions', groups.join('|') === 'Yours|Team|Sandboxes', groups.join('|'));
const opt = (id) => page.$eval(`#ssel option[value="${MGR}|${id}"]`, (o) => ({ disabled: o.disabled, title: o.title }));
const vault = await opt('vault');
ok('one that held internal data: disabled for a class that reaches outside', vault.disabled && vault.title.includes('internal-reach'), JSON.stringify(vault));
const pend = await opt('pending');
ok('one whose network opens at its next start: counted as open', pend.disabled && pend.title.includes('open network'), JSON.stringify(pend));
await page.selectOption('#ssel', `${MGR}|web`);
await page.fill('#msg', 'build it');
await page.click('#send');
await page.waitForFunction(() => location.hash === '#c=9');
const ask = await lastBody('POST', /\/ask$/);
ok('a new chat is asked with the picked sandbox', ask.sandbox && ask.sandbox.ref === `${MGR}|web` && ask.class === 'coding', JSON.stringify(ask));

// --- a conversation: the badge, the popover ------------------------------------------------
await page.goto(`${ORIGIN}/#c=1`);
await page.waitForSelector('#sbxbadge');
ok('the badge: ▣ name · cwd', (await page.textContent('#sbxbadge')).trim() === '▣ api · /work', await page.textContent('#sbxbadge'));
ok('the picker shows its sandbox', (await page.$eval('#ssel', (el) => el.value)) === `${MGR}|api`);
const here = await page.$$eval('#ssel optgroup', (els) => els.map((e) => e.label));
ok('…with This conversation first', here[0] === 'This conversation', here.join('|'));
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop');
await page.fill('#sbx-cwd', 'relative');
await page.click('#sbx-cwd-set');
await page.waitForSelector('#sbx-err');
ok('a relative cwd is refused before it is sent', (await page.textContent('#sbx-err')).includes('absolute path'));
await page.fill('#sbx-cwd', '/work/api');
await page.click('#sbx-cwd-set');
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('/work/api'));
const cwdBody = await lastBody('PATCH', /\/runs\/1$/);
ok('Set sends the cwd for the active sandbox', cwdBody.sandbox.ref === `${MGR}|api` && cwdBody.sandbox.cwd === '/work/api', JSON.stringify(cwdBody));
ok('…and the view is read again', (await calls('GET', /\/runs\/1\/view\?limit=1$/)).length > 0);

// the picker binds another: both attached, switch back from the popover
await page.keyboard.press('Escape');
await page.waitForFunction(() => !document.getElementById('sbxpop'));
await page.selectOption('#ssel', `${MGR}|web`);
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('web'));
const bindBody = await lastBody('PATCH', /\/runs\/1$/);
ok('a pick binds it (PATCH /runs {sandbox})', JSON.stringify(bindBody) === JSON.stringify({ sandbox: { ref: `${MGR}|web` } }), JSON.stringify(bindBody));
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop .sbxatt');
const att = await page.$$eval('#sbxpop .sbxatt', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
ok('the popover lists the attached ones', att.length === 2 && att.some((t) => t.startsWith('● web')) && att.some((t) => t.startsWith('○ api')), att.join(' | '));
await page.click(`#sbxpop .sbxatt[data-ref="${MGR}|api"]`);
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('api'));
ok('switching makes it active again', (await lastBody('PATCH', /\/runs\/1$/)).sandbox.ref === `${MGR}|api`);
await page.click('#sbx-detach');
await page.waitForFunction(() => !document.getElementById('sbxbadge') || !document.getElementById('sbxbadge').textContent.includes('api'));
ok('Detach takes it off', (await lastBody('PATCH', /\/runs\/1$/)).detach === `${MGR}|api`);
ok('…the other stays attached (not active): no badge', !(await page.$('#sbxbadge')));

// a run event that carries the binding: the badge follows at once
await page.waitForFunction(() => window.__streams() > 0);
await page.evaluate((b) => window.__push({ type: 'run', run: 1, root: 1, data: { id: 1, status: 'idle', sandbox: b, attached: 1 } }),
  { ref: `${MGR}|web`, name: 'web', cwd: '/srv/web', egress: 'none', manager: 'Coding sandboxes' });
await page.waitForSelector('#sbxbadge');
ok('a run event with {sandbox} updates the badge', (await page.textContent('#sbxbadge')).includes('web · /srv/web'));

// --- the tool cards -----------------------------------------------------------------------
const cards = await page.$$eval('.tcard[data-fam="box"] .tch', (els) => els.map((e) => ({
  hl: e.querySelector('.hl').textContent.replace(/\s+/g, ' ').trim(),
  oc: e.querySelector('.oc') ? e.querySelector('.oc').textContent : '', tone: e.querySelector('.oc') ? e.querySelector('.oc').className : '' })));
ok('four sandbox cards, the ▣ family', cards.length === 4, JSON.stringify(cards));
ok('bash: the summary, the command under it', cards[0].hl === 'Run the tests$ go test ./...', cards[0].hl);
ok('…and the footer: exit 1 · 14s · job 3, marked bad', cards[0].oc === 'exit 1 · 14s · job 3' && /\bbad\b/.test(cards[0].tone), JSON.stringify(cards[0]));
ok('grep: pattern and count', cards[1].hl === 'Search /TODO/ under src' && cards[1].oc === '2 matches', JSON.stringify(cards[1]));
ok('edit: old → new', cards[2].hl === 'Edit /work/api/main.go: return nil → return err' && cards[2].oc === '1 replacement', JSON.stringify(cards[2]));
ok('a command that went on as a job', cards[3].hl === '$ make serve &' && cards[3].oc === 'still running · 2m00s · job 4' && /\brun\b/.test(cards[3].tone), JSON.stringify(cards[3]));

// --- the Sandboxes dialog: lifecycle, sharing, delete, create --------------------------------
await page.selectOption('#ssel', '+manage');
await page.waitForSelector('#sbxdlg[open] .sbxrow');
ok('Manage… opens the dialog, the picker keeps its value', (await page.$eval('#ssel', (el) => el.value)) === `${MGR}|web`);
const rowActs = (ref) => page.$$eval(`#sbxdlg .sbxrow[data-ref="${ref}"] [data-act]`, (els) => els.map((e) => e.dataset.act));
ok('a stopped one of yours (active here: no "use"): start, archive, share, delete', (await rowActs(`${MGR}|web`)).join(',') === 'start,archive,team,delete',
  (await rowActs(`${MGR}|web`)).join(','));
ok('the team one: use it, stop it — no delete', (await rowActs(`${MGR}|team-box`)).join(',') === 'use,stop', (await rowActs(`${MGR}|team-box`)).join(','));
await page.click(`#sbxdlg .sbxrow[data-ref="${MGR}|web"] [data-act="start"]`);
await page.waitForFunction((r) => document.querySelector(`#sbxdlg .sbxrow[data-ref="${r}"] .sbxst`).textContent === 'running', `${MGR}|web`);
const life = (await calls('POST', /\/sandboxes\/.+\/start\?/)).pop();
ok('Start: POST …/start, the ref encoded', life.url === '/api/apps/agent/sandboxes/apps/coding-sandbox%7Cweb/start?wait=20', life.url);
await page.click(`#sbxdlg .sbxrow[data-ref="${MGR}|api"] [data-act="team"]`);
await page.waitForFunction((r) => [...document.querySelectorAll(`#sbxdlg .sbxrow[data-ref="${r}"] [data-act]`)].some((b) => b.dataset.act === 'private'), `${MGR}|api`);
ok('Share with the team: PATCH {visibility}', (await lastBody('PATCH', /\/sandboxes\//)).visibility === 'team');
await page.click(`#sbxdlg .sbxrow[data-ref="${MGR}|api"] [data-act="delete"]`);
await page.waitForFunction((r) => !document.querySelector(`#sbxdlg .sbxrow[data-ref="${r}"]`), `${MGR}|api`);
ok('Delete (confirmed): DELETE, the row goes', (await calls('DELETE', /\/sandboxes\//)).length === 1);
// create for this conversation
await page.click('#sbx-new');
await page.waitForSelector('#sbx-form');
const egress = await page.$$eval('#sbxf-egress option', (els) => els.map((e) => `${e.value}${e.disabled ? '(off)' : ''}`));
ok('the network the class allows; the rest disabled', egress.join(',') === 'none,internet,open(off)', egress.join(','));
await page.click('#sbxf-create');
await page.waitForSelector('#sbxf-err');
ok('no name: the form says so', (await page.textContent('#sbxf-err')).includes('Name the sandbox'));
await page.fill('#sbxf-name', 'fresh');
await page.selectOption('#sbxf-egress', 'internet');
await page.fill('#sbxf-cwd', '/work/fresh');
await page.click('#sbxf-create');
await page.waitForSelector('#sbx-msg');
const made = await lastBody('POST', /\/sandboxes$/);
ok('Create: POST /sandboxes for this conversation', made.name === 'fresh' && made.conversation === 1 && made.egress === 'internet'
  && made.cwd === '/work/fresh' && made.image === 'base' && made.size === 'small' && !!made.clientId, JSON.stringify(made));
await page.waitForFunction(() => (document.getElementById('sbxbadge') || {}).textContent?.includes('fresh'));
ok('…bound there: the badge says it', (await page.textContent('#sbxbadge')).includes('fresh · /work/fresh'));
await page.click('#sbx-close');
ok('the dialog closes', !(await page.$('#sbxdlg[open]')));

// --- a class without the sandbox toolset; a binding that no longer resolves ---------------------
await page.goto(`${ORIGIN}/#c=2`);
await page.waitForSelector('#top .clsbadge');
ok('an internal conversation: no picker, no badge', (await hidden('#ssel')) && !(await page.$('#sbxbadge')));
await page.goto(`${ORIGIN}/#c=3`);
await page.waitForSelector('#sbxbadge.broken');
ok('a sandbox its manager no longer has: the badge says so', (await page.textContent('#sbxbadge')).includes('⚠'));
await page.click('#sbxbadge');
await page.waitForSelector('#sbx-broken');
ok('…and why, in the popover', (await page.textContent('#sbx-broken')).includes('gone'), await page.textContent('#sbx-broken'));

// --- sandbox_create asks the owner: the generic grant card ------------------------------------
await page.goto(`${ORIGIN}/#c=4`);
await page.waitForSelector('.ask.approve.grant');
const g = await page.textContent('.ask.approve.grant');
ok('the grant card says what the backend asks', g.includes('The agent asks to create a coding sandbox “scratch” for this conversation'), g);
ok('…with Allow once · for an hour · Deny', (await page.$$eval('.ask.approve.grant .btn', (els) => els.map((e) => e.textContent.trim()))).join('|')
  === 'Allow once|Allow here for 1 hour|Deny');

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('sandbox');
