// sandbox.mjs — coding sandboxes (D115) on the web: the composer's picker
// (#ssel) shows only where the class has the sandbox toolset and binds what
// you pick (PATCH /runs {sandbox}; at home the new chat is asked with it);
// the top bar's ▣ badge says the sandbox and its working directory, or why
// the binding no longer resolves, and its popover sets the cwd, switches
// among the attached ones and detaches; the Sandboxes dialog lists them with
// their lifecycle actions and creates one for the conversation; the sandbox
// tool cards read their command and footer; a grant for `sandboxes` is the
// generic grant card. And what the phase-1 review found: a stale new-chat
// pick (said in the picker, dropped when the ask is refused, the message
// kept), a re-pick keeping its cwd, the popover's field following a switch,
// the dialog's order held while open, a viewer offered no New, a private
// sandbox into a team conversation confirmed, a class edit reaching the
// open conversation.
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
    // shared with you to read: its sandbox is for those who may talk in it
    5: { access: 'viewer', class: coding, config: { sandbox: bind('web'), attached: [bind('web')] },
      run: { id: 5, title: 'read along', status: 'idle', parentId: 0, rootId: 5, owner: 'carol' } },
    // a team conversation: a private sandbox goes in only once you confirm it
    6: { access: 'owner', class: coding, config: {}, acl: { owner: 'admin', visibility: 'team', teamRole: 'participant', members: [] },
      run: { id: 6, title: 'team build', status: 'idle', parentId: 0, rootId: 6, visibility: 'team' } },
  },
  sandboxes: [sb('api', { boundTo: [1] }), sb('web', { state: 'stopped', lastActive: Date.now() - 3600e3 }),
    sb('team-box', { mine: false, owner: { user: 'carol' }, visibility: 'team', canManage: false, canEdit: false }),
    sb('old-box', { lastActive: Date.now() - 7200e3 })],
};

const browser = await launch();
const ctx = await browser.newContext();
await serveTile(ctx);
await ctx.addInitScript(STUB, seed);
// a new chat opens: POST /ask answers its run — or, for a sandbox its
// manager no longer has, refuses it as _backend does (404, refusal not-found)
await ctx.addInitScript(() => window.__route('POST', /\/ask$/, (m, o) => {
  const b = JSON.parse(o.body);
  if (b.sandbox && !window.__sbx.sandboxes.some((s) => s.ref === b.sandbox.ref)) {
    return window.__json({ error: `sandbox ${b.sandbox.ref}: no such sandbox`, refusal: 'not-found' }, 404);
  }
  const run = { id: 9, title: 'build it', status: 'running', parentId: 0, rootId: 9 };
  window.__runs.push(run);
  return window.__json(run);
}));
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
// alerts and confirms are recorded and accepted — a confirm dismissed once when asked to
const dialogs = [];
let dismissNext = false;
page.on('dialog', (d) => {
  dialogs.push(d.message());
  if (dismissNext && d.type() === 'confirm') { dismissNext = false; return d.dismiss(); }
  return d.accept();
});
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

// a stale pick (review): its manager no longer has it — once the list is
// read again the picker says why; the ask is refused, the typed message
// stays, the pick is dropped and says why
await page.selectOption('#ssel', `${MGR}|old-box`);
await page.evaluate((ref) => { window.__sbx.sandboxes = window.__sbx.sandboxes.filter((s) => s.ref !== ref); }, `${MGR}|old-box`);
await page.selectOption('#ssel', '+manage');
await page.waitForFunction(() => document.querySelector('#sbxdlg[open] .sbxrow') && !document.querySelector('#sbxdlg .sbxrow[data-ref$="|old-box"]'));
await page.click('#sbx-close');
const stale = await page.$eval('#ssel', (el) => ({ value: el.value, text: el.selectedOptions[0].textContent.trim(), why: el.selectedOptions[0].title, title: el.title }));
ok('a pick the list no longer has says why in the picker', stale.value === `${MGR}|old-box` && stale.text.endsWith('old-box · unavailable')
  && /^gone/.test(stale.why) && stale.title.includes('old-box: gone'), JSON.stringify(stale));
await page.fill('#msg', 'a long first message');
await page.click('#send');
await page.waitForFunction(() => (document.getElementById('ssel') || {}).value === '');
ok('a refused pick keeps the typed message', (await page.$eval('#msg', (e) => e.value)) === 'a long first message', await page.$eval('#msg', (e) => e.value));
ok('…drops the pick and says why', /The sandbox old-box can't be used: .*no such sandbox\. Your next new chat starts without one/.test(dialogs.at(-1) || ''), dialogs.at(-1));
ok('…and stays home', !(await page.evaluate(() => location.hash)).startsWith('#c='));
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
await page.fill('#sbx-cwd', '/work/web');
await page.click('#sbx-cwd-set');
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('web · /work/web'));
await page.click(`#sbxpop .sbxatt[data-ref="${MGR}|api"]`);
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('api'));
ok('switching makes it active again', (await lastBody('PATCH', /\/runs\/1$/)).sandbox.ref === `${MGR}|api`);
// the field follows the switch (review: it kept web's path, and Set bound api there)
ok('…and the field is its directory', (await page.$eval('#sbx-cwd', (e) => e.value)) === '/work/api', await page.$eval('#sbx-cwd', (e) => e.value));
const patches = await page.evaluate(() => window.__calls.filter((c) => c.method === 'PATCH').length);
await page.click('#sbx-cwd-set');
await page.waitForFunction((n) => window.__calls.filter((c) => c.method === 'PATCH').length > n, patches);
ok('…Set sends its own', JSON.stringify(await lastBody('PATCH', /\/runs\/1$/)) === JSON.stringify({ sandbox: { ref: `${MGR}|api`, cwd: '/work/api' } }),
  JSON.stringify(await lastBody('PATCH', /\/runs\/1$/)));
// picked again in the composer, an attached one keeps its directory (review: reset to the workdir)
await page.keyboard.press('Escape');
await page.waitForFunction(() => !document.getElementById('sbxpop'));
await page.selectOption('#ssel', `${MGR}|web`);
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('web'));
ok('a re-pick sends its stored cwd', JSON.stringify(await lastBody('PATCH', /\/runs\/1$/)) === JSON.stringify({ sandbox: { ref: `${MGR}|web`, cwd: '/work/web' } }),
  JSON.stringify(await lastBody('PATCH', /\/runs\/1$/)));
ok('…the badge says it', (await page.textContent('#sbxbadge')).trim() === '▣ web · /work/web', await page.textContent('#sbxbadge'));
await page.click('#sbxbadge');
await page.waitForSelector('#sbxpop .sbxatt');
await page.click(`#sbxpop .sbxatt[data-ref="${MGR}|api"]`);
await page.waitForFunction(() => document.getElementById('sbxbadge').textContent.includes('api'));
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
const order = () => page.$$eval('#sbxdlg .sbxrow', (els) => els.map((e) => e.dataset.ref.split('|').pop()).join(','));
const shown = await order();
await page.click(`#sbxdlg .sbxrow[data-ref="${MGR}|web"] [data-act="start"]`);
await page.waitForFunction((r) => document.querySelector(`#sbxdlg .sbxrow[data-ref="${r}"] .sbxst`).textContent === 'running', `${MGR}|web`);
const life = (await calls('POST', /\/sandboxes\/.+\/start\?/)).pop();
ok('Start: POST …/start, the ref encoded', life.url === '/api/apps/agent/sandboxes/apps/coding-sandbox%7Cweb/start?wait=20', life.url);
// it was active just now — but no row moves under the cursor (review: the rows re-sorted)
ok('the rows keep their order while it is open', (await order()) === shown && shown === 'api,web,team-box', `${shown} → ${await order()}`);
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
ok('…its row comes last (new rows append)', (await order()) === 'web,team-box,sb-fresh', await order());
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

// --- review fixes: a viewer makes none; a private one into a team conversation; a class edit ------------

// a conversation shared with you to read: New sandbox (for it) is not offered
await page.goto(`${ORIGIN}/#c=5`);
await page.waitForSelector('#sbxbadge');
await page.click('#sbxbadge');
await page.click('#sbx-manage');
await page.waitForSelector('#sbxdlg[open] #sbx-new');
ok('a viewer: New sandbox is disabled', await page.$eval('#sbx-new', (e) => e.disabled));
ok('…and says why', (await page.textContent('#sbx-new-why')).startsWith('You may only read this conversation'), await page.textContent('#sbx-new-why'));
await page.click('#sbx-close');

// a team conversation: a private sandbox goes in only once you confirm it
await page.goto(`${ORIGIN}/#c=6`);
await page.waitForFunction(() => !document.getElementById('ssel').hidden && [...document.querySelectorAll('#ssel option')].some((o) => o.value.endsWith('|web')));
const toSix = () => calls('PATCH', /\/runs\/6$/);
dismissNext = true;
await page.selectOption('#ssel', `${MGR}|web`);
await page.waitForFunction(() => document.getElementById('ssel').value === '');
ok('a private one in a team conversation asks first', /^“web” is private — people in this conversation will be able to work in it/.test(dialogs.at(-1)), dialogs.at(-1));
ok('…declined: nothing is bound', (await toSix()).length === 0);
await page.selectOption('#ssel', `${MGR}|web`);
await page.waitForSelector('#sbxbadge');
ok('…confirmed: it is bound', JSON.stringify(JSON.parse((await toSix()).pop().body)) === JSON.stringify({ sandbox: { ref: `${MGR}|web` } }));
await page.selectOption('#ssel', '+manage');
await page.waitForSelector(`#sbxdlg[open] .sbxrow[data-ref="${MGR}|sb-fresh"] [data-act="use"]`);
await page.click(`#sbxdlg .sbxrow[data-ref="${MGR}|sb-fresh"] [data-act="use"]`);
await page.waitForSelector('#sbx-msg');
ok('"Use here" asks too', /^“fresh” is private — people in this conversation/.test(dialogs.at(-1)), dialogs.at(-1));
ok('…then binds it', JSON.parse((await toSix()).pop().body).sandbox.ref === `${MGR}|sb-fresh`);
await page.click('#sbx-close');

// a class edit reaches the open conversation (review: its badge stayed as loaded)
await page.goto(`${ORIGIN}/#c=1`);
await page.waitForSelector('#top .clsbadge');
ok('the coding conversation: no mixed warning', !(await page.$('#top .clswarn')));
await page.click('#gear');
await page.click('#tabs .tab[data-tab="classes"]');
await page.click('[data-edit="coding"]');
await page.check('[data-ts="internal"]');
await page.click('#clf-save');
ok('saved with internal reach, its conversation warns at once', await page.waitForSelector('#top .clswarn', { timeout: 5000 }).then(() => true, () => false));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('sandbox');
