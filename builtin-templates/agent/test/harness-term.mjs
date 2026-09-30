// harness-term.mjs — terminals and a coding agent's sign-in on the web
// (D-harness §8 U5) over the harness fixtures (test/harness-fixtures.mjs,
// the STUB's §4 routes) and a fake manager terminal (test/fake-tty.mjs —
// the STUB can't upgrade a WebSocket): the sign-in card is drawn for a
// login park only (the `end` seam's rule), with the shared-HOME warning and
// its confirm; a terminal login opens a login tab in the dock whose src
// carries the agent's sign-in command, and "Retry" posts /resume; the dock
// holds several tabs (＋, switch, ✕ ends that shell, ▾ Hide keeps them
// running behind a pill, across conversations); the top bar's >_ Terminal
// opens a shell at the agent's cwd; an API key is sent once and never
// echoed; a device code shows its page and code; a sandbox the person may
// not use says whom to ask.
//
//   node test/harness-term.mjs        (needs playwright + a chromium build)
import { ORIGIN, STUB, serveTile, launch, checker } from './backend.mjs';
import { harnessSeed, SBX, API_DEV } from './harness-fixtures.mjs';
import { FAKE_TTY, serveTerminal } from './fake-tty.mjs';

const { ok, done } = checker();
const seed = harnessSeed();
seed.sbxManagers = [{ provider: SBX, title: 'Coding sandboxes', ok: true, caps: ['exec', 'files', 'tar', 'tty'], egress: ['none', 'internet'],
  images: [{ id: 'base', title: 'Debian', default: true }], sizes: [], limits: {} }];
// 30: Codex waiting for a sign-in in a private sandbox of yours; 31: in bob's, which you may not use
const box = (id, name, extra) => ({ ref: `${SBX}|${id}`, provider: SBX, manager: 'Coding sandboxes', id, name, state: 'running', egress: 'internet',
  visibility: 'private', image: { id: 'base' }, owner: { user: 'admin' }, mine: true, canUse: true, canManage: true, canEdit: true, workdir: '/work', ...extra });
seed.sandboxes.push(box('sb-solo', 'solo', { boundTo: [30] }),
  box('sb-bob', 'bobs-box', { owner: { user: 'bob' }, mine: false, canUse: false, canManage: false, canEdit: false, boundTo: [31] }));
const r24 = seed.runs.find((r) => r.id === 24);
for (const [id, sb, by] of [[30, 'sb-solo', 'admin'], [31, 'sb-bob', 'bob']]) {
  const s = seed.sandboxes.find((x) => x.id === sb);
  const sandbox = { ref: s.ref, name: s.name, cwd: '/work', shared: false };
  const run = { ...r24, id, rootId: id, title: `sign in ${id}`, harness: { ...r24.harness, sandbox }, pendingState: { ...r24.pendingState, park: `park${id}` } };
  seed.runs.push(run);
  seed.views[id] = { ...seed.views[24], run, config: { sandbox: { ref: s.ref, name: s.name, cwd: '/work', manager: 'Coding sandboxes', by },
    engine: 'harness', harness: { provider: 'codex', mode: 'agent', ref: s.ref, cwd: '/work', by } } };
}

// 32: run 24's park, for someone it is shared with to read
{
  const run = { ...r24, id: 32, rootId: 32, title: 'read only', pendingState: { ...r24.pendingState, park: 'park32' } };
  seed.runs.push(run);
  seed.views[32] = { ...seed.views[24], run, access: 'viewer' };
}

const browser = await launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
await serveTile(ctx);
await serveTerminal(ctx);
await ctx.addInitScript(STUB, seed);
await ctx.addInitScript(FAKE_TTY);
await ctx.addInitScript(() => {
  window.__route('DELETE', /\/api\/apps\/coding-sandbox\/sbx\/sandboxes\/[^/]+\/execs\/[^/]+$/, () => new Response(null, { status: 204 }));
  // run 30's backend knows its sandbox is shared although the view didn't say: confirm first
  window.__route('POST', /\/runs\/30\/harness\/authenticate$/, (m, o) => (JSON.parse(o.body).confirm ? window.__json({ ok: 'true', state: 'ready' })
    : window.__json({ error: 'anyone who may use solo acts as you with Codex there — confirm to sign in', confirm: true }, 409)));
  // the first GET /sandboxes waits until the test lets it go (window.__sbxGo)
  const gate = new Promise((go) => { window.__sbxGo = go; });
  let held = false;
  window.__route('GET', /\/sandboxes(\?fresh=1)?$/, async () => { if (!held) { held = true; await gate; } return window.__json(window.__sbx); });
});
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
// (the test's own init scripts are refused in the render preview's sandbox="" frame: not the page's error)
page.on('console', (m) => { if (m.type() === 'error' && !/^Blocked script execution in 'about:blank'/.test(m.text())) errors.push(m.text()); });
page.on('dialog', (d) => d.accept());
const until = (fn, arg, timeout = 8000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 30 });
const dials = () => page.evaluate(() => window.__tty.dials);
const calls = (method, re) => page.evaluate(([m, s]) => window.__calls.filter((c) => c.method === m && new RegExp(s).test(c.url)).map((c) => ({ url: c.url, body: c.body })), [method, re.source]);
const go = async (id, sel) => { await page.evaluate((h) => { location.hash = h; }, '#c=' + id); await page.waitForSelector(sel); };
const text = (sel) => page.textContent(sel).then((t) => t.replace(/\s+/g, ' ').trim());
const shown = (key) => page.$eval(`#sbxterm-pane bx-terminal[data-tab="${key}"]`, (t) => t.style.display !== 'none');
const screenOf = (key) => page.$eval(`#sbxterm-pane bx-terminal[data-tab="${key}"]`, (t) => t.testApi().text());
const LOGIN_SRC = `/api/apps/coding-sandbox/sbx/sandboxes/sb-7f3a/tty?cwd=%2Fwork%2Fapi&cmd=codex%20login`;
const SHELL_SRC = '/api/apps/coding-sandbox/sbx/sandboxes/sb-7f3a/tty?cwd=%2Fwork%2Fapi';

// --- the card: only for a login park ------------------------------------------------------------
await page.goto(`${ORIGIN}/#c=24`);
await page.waitForSelector('#hlogin');
ok('while the sandboxes are still being read: the login terminal waits, no "no terminal" note', !(await page.$('#hl-noterm'))
  && await page.$eval('#hlogin [data-kind="terminal"]', (b) => b.disabled && !/No terminal/.test(b.title)));
await page.evaluate(() => window.__sbxGo());
ok('a login park: the sign-in card, saying where', (await text('#hlogin b')) === 'Codex needs you to sign in (in ▣ api-dev).', await text('#hlogin b'));
ok('…the shared home\'s warning', /credentials land in api-dev's home: anyone who may use it acts as you with Codex there/.test(await text('#hl-warn')), await text('#hl-warn'));
ok('…its three methods', JSON.stringify(await page.$$eval('#hlogin [data-kind]', (els) => els.map((e) => [e.dataset.kind, e.dataset.method])))
  === JSON.stringify([['terminal', 'chatgpt'], ['api-key', 'openai-api-key'], ['device-code', 'device-code']]));
ok('no built-in card beside it', !(await page.$('.ask.approve')) && (await page.$$('#timeline div.ask')).length === 1);
ok('the composer says sign in (not "answer the question")', (await page.getAttribute('#msg', 'placeholder')) === 'sign in to Codex first — then message it…',
  await page.getAttribute('#msg', 'placeholder'));
ok('…the activity line too, with no spinner: it waits on you', (await text('.activity')) === 'Codex needs you to sign in' && !(await page.$('.activity .spin')));
ok('…and the top bar has no Memory, Learn skill or Compact (Codex has no /compact)',
  !(await page.$$eval('#top button', (els) => els.some((e) => /^(Memory|Learn skill|Compact)/.test(e.textContent.trim())))));
ok('a shared sandbox: every method waits for the confirm', await page.$$eval('#hlogin [data-kind="terminal"], #hlogin [data-kind="device-code"], #hlogin [data-kind="api-key"] button',
  (els) => els.every((e) => e.disabled)) && (await text('#hl-shared')).includes('api-dev is shared — sign in anyway'));
await page.check('#hl-confirm');
await until(() => !document.querySelector('#hlogin [data-kind="terminal"]').disabled);
ok('…and are offered once confirmed', true);

// --- a terminal login: a login tab running the sign-in command ---------------------------------------
await page.click('#hlogin [data-kind="terminal"]');
await page.waitForSelector('#sbxterm-pane bx-terminal');
await until((s) => window.__tty.dials.includes(s), LOGIN_SRC);
ok('the login tab dials the manager\'s tty with the agent\'s sign-in command, at its cwd', (await dials()).at(-1) === LOGIN_SRC, (await dials()).at(-1));
ok('…its header says what it signs in, where', (await text('#sbxterm-pane .sbxthd b')) === 'Sign in · Codex · ▣ api-dev' && (await text('.sbxthint')).includes('copy it'));
await until(() => document.querySelector('#sbxterm-pane bx-terminal').testApi().text().includes('running: codex login'));
ok('…and runs it', true);
ok('…with Signed in? Retry at hand', (await text('#sbxterm-retry')) === 'Signed in? Retry Codex');
ok('the card says what next', (await text('#hl-msg')).includes('then Retry'));
const loginKey = await page.$eval('#sbxterm-pane bx-terminal', (t) => t.dataset.tab);

// ＋: another tab, a shell in the same sandbox
await page.click('#sbxterm-new');
await until(() => document.querySelectorAll('#sbxterm-pane .sbxtab').length === 2);
await until((s) => window.__tty.dials.filter((d) => d === s).length === 1, SHELL_SRC);
const tabs = await page.$$eval('#sbxterm-pane .sbxtab', (els) => els.map((e) => ({ key: e.dataset.tab, label: e.querySelector('.lb').textContent.trim(), on: e.classList.contains('on') })));
ok('＋ opens another tab: a shell at the same cwd, shown', tabs.length === 2 && tabs[0].label === 'Sign in · Codex' && tabs[1].label === '▣ api-dev' && tabs[1].on, JSON.stringify(tabs));
const shellKey = tabs[1].key;
ok('…the other tab\'s terminal stays, out of sight', !(await shown(loginKey)) && (await shown(shellKey)));
await until((k) => document.querySelector(`bx-terminal[data-tab="${k}"]`).testApi().text().includes('sandbox$'), shellKey);
await page.click(`#sbxterm-pane .sbxtab[data-tab="${loginKey}"]`);
await until((k) => document.querySelector(`bx-terminal[data-tab="${k}"]`).style.display !== 'none', loginKey);
ok('a tab shows its own terminal and header', (await text('#sbxterm-pane .sbxthd b')).startsWith('Sign in') && (await screenOf(loginKey)).includes('running: codex login'));

// ▾ Hide: the shells keep running; the pill brings them back, in any conversation
await page.click('#sbxterm-hide');
await page.waitForSelector('#sbxterm-pill');
ok('Hide: the dock goes, a pill says how many', await page.$eval('#sbxterm-pane', (p) => p.style.display === 'none') && (await text('#sbxterm-pill')) === '>_ 2 terminals');
ok('…their sockets stay open', await page.evaluate(() => window.__tty.sockets.filter((s) => s.readyState === 1).length === 2));
const nDials = (await dials()).length;
await go(21, '[data-k="ch1:toolu_10"]');
ok('another conversation: the pill stays', !!(await page.$('#sbxterm-pill')));
await page.click('#sbxterm-pill');
await until(() => document.getElementById('sbxterm-pane').style.display !== 'none');
ok('…and brings the same terminals back (nothing dialled again)', (await page.$$('#sbxterm-pane .sbxtab')).length === 2 && (await dials()).length === nDials && !(await page.$('#sbxterm-pill')));

// ✕ on a tab ends that shell only
await page.click(`#sbxterm-pane .sbxtab[data-tab="${shellKey}"] [data-close]`);
await until(() => document.querySelectorAll('#sbxterm-pane bx-terminal').length === 1);
await until(() => window.__calls.some((c) => c.method === 'DELETE'));
ok('✕ on a tab ends its shell at the manager', JSON.stringify((await calls('DELETE', /execs/)).map((c) => c.url)) === JSON.stringify(['/api/apps/coding-sandbox/sbx/sandboxes/sb-7f3a/execs/e2']),
  JSON.stringify(await calls('DELETE', /execs/)));
ok('…one tab left: no strip, the login tab shown', !(await page.$('#sbxterm-pane .sbxttabs')) && (await text('#sbxterm-pane .sbxthd b')).startsWith('Sign in'));

// the sign-in command ends: Finished. Signed in? Retry Codex — /resume, and the tab goes
await page.click('#sbxterm-pane bx-terminal');
await page.keyboard.type('exit');
await page.keyboard.press('Enter');
await page.waitForSelector('#sbxterm-done');
ok('the command ended: "Finished. Signed in?" and Retry Codex, or a New shell', (await text('#sbxterm-done')) === 'Finished. Signed in?'
  && (await text('#sbxterm-retry')) === 'Retry Codex' && !!(await page.$('#sbxterm-again')));
const fit = await page.evaluate(() => {
  const done = document.getElementById('sbxterm-done');
  const pane = document.getElementById('sbxterm-pane').getBoundingClientRect();
  return { whole: done.scrollWidth <= done.clientWidth, reach: document.getElementById('sbxterm-close').getBoundingClientRect().right <= pane.right };
});
ok('…the crowded header keeps "Finished. Signed in?" whole and ✕ in reach (the title gives way)', fit.whole && fit.reach, JSON.stringify(fit));
await page.click('#sbxterm-retry');
await until(() => !document.getElementById('sbxterm-pane'));
ok('Retry posts /resume for its run; the tab closes', JSON.stringify((await calls('POST', /\/resume$/)).map((c) => c.url)) === '["/api/apps/agent/runs/24/resume"]');
ok('…an ended shell is not ended again', (await calls('DELETE', /execs/)).length === 1);

// the top bar's >_ Terminal: a shell at the coding agent's cwd
await page.click('#hterm');
await page.waitForSelector('#sbxterm-pane bx-terminal');
await until((n) => window.__tty.dials.length > n, nDials + 0);
ok('>_ Terminal: a shell in its sandbox at its cwd', (await dials()).at(-1) === SHELL_SRC && (await text('#sbxterm-pane .sbxthd b')) === '▣ api-dev', (await dials()).at(-1));
await page.click('#sbxterm-close');
await until(() => !document.getElementById('sbxterm-pane'));

// --- the card's own flows ------------------------------------------------------------------------------
await go(24, '#hlogin');
await page.check('#hl-confirm');
await page.click('#hl-retry');
await until(() => window.__calls.filter((c) => c.url.endsWith('/runs/24/resume')).length === 2);
ok('Signed in? Retry on the card posts /resume', (await text('#hl-msg')) === 'Retrying…');

// a device code: the page (a link out) and the code
await page.click('#hlogin [data-kind="device-code"]');
await page.waitForSelector('#hl-device-link');
const link = await page.$eval('#hl-device-link', (a) => ({ href: a.href, target: a.target, rel: a.rel }));
ok('a device code: its page as a link out', link.href === 'https://example.invalid/device' && link.target === '_blank' && /noopener/.test(link.rel), JSON.stringify(link));
ok('…and the code to enter there', (await text('#hl-device-msg')).includes('FAKE-1234'));
const dev = (await calls('POST', /\/harness\/authenticate$/)).map((c) => JSON.parse(c.body));
ok('…asked with the confirm, no key', JSON.stringify(dev) === JSON.stringify([{ method: 'device-code', confirm: true }]), JSON.stringify(dev));

// an API key: sent once, never shown or kept
const KEY = 'sk-live-0123456789abcdef';
const keyForm = '#hlogin form[data-kind="api-key"]';
await page.fill(`${keyForm} input`, 'bad');
await page.click(`${keyForm} button`);
await page.waitForSelector('#hl-err');
ok('a refused key: the adapter\'s words; the field is empty', (await text('#hl-err')) === 'invalid API key' && (await page.$eval(`${keyForm} input`, (i) => i.value)) === '');
await page.fill(`${keyForm} input`, KEY);
ok('the field hides what is typed', (await page.$eval(`${keyForm} input`, (i) => i.type)) === 'password');
await page.click(`${keyForm} button`);
await until(() => !document.getElementById('hlogin'));
const sent = await page.evaluate((k) => window.__calls.filter((c) => (c.body || '').includes(k)), KEY);
ok('the key goes once, to authenticate, with the confirm', sent.length === 1 && sent[0].url.endsWith('/runs/24/harness/authenticate')
  && JSON.stringify(JSON.parse(sent[0].body)) === JSON.stringify({ method: 'openai-api-key', apiKey: KEY, confirm: true }), JSON.stringify(sent));
const echoed = await page.evaluate((k) => {
  const inputs = [...document.querySelectorAll('input, textarea')].some((i) => (i.value || '').includes(k));
  let stored = false;
  try { stored = JSON.stringify({ ...localStorage }).includes(k) || JSON.stringify({ ...sessionStorage }).includes(k); } catch { /* none */ }
  return { html: document.documentElement.outerHTML.includes(k), inputs, stored };
}, KEY);
ok('…and is never echoed: not in the page, a field or storage', !echoed.html && !echoed.inputs && !echoed.stored, JSON.stringify(echoed));
ok('signed in: the card goes with the park', !(await page.$('#hlogin')));

// a private sandbox: no confirm asked — until the backend asks for one
await go(30, '#hlogin');
ok('a private sandbox: no confirm; the methods are offered', !(await page.$('#hl-confirm')) && !(await page.$eval('#hlogin [data-kind="terminal"]', (b) => b.disabled)));
await page.fill(`${keyForm} input`, KEY);
await page.click(`${keyForm} button`);
await page.waitForSelector('#hl-confirm');
ok('the backend asks for a confirm: the card asks, and says why', (await text('#hl-err')).includes('confirm to sign in') && (await page.$eval(`${keyForm} input`, (i) => i.value)) === '');
await page.check('#hl-confirm');
await page.fill(`${keyForm} input`, KEY);
await page.click(`${keyForm} button`);
await page.waitForSelector('#hl-msg');
const b30 = (await calls('POST', /\/runs\/30\/harness\/authenticate$/)).map((c) => JSON.parse(c.body).confirm ?? false);
ok('…confirmed, it is sent again with confirm: true', JSON.stringify(b30) === '[false,true]' && (await text('#hl-msg')) === 'Signed in to Codex.', JSON.stringify(b30));

// someone else's sandbox: whom to ask
await go(31, '#hlogin');
await page.waitForSelector('#hl-ask');
ok('a sandbox you may not use: whom to ask, no methods', (await text('#hl-ask')) === 'Ask bob to sign in — the sandbox is theirs.' && !(await page.$('#hlogin [data-kind]')) && !!(await page.$('#hl-retry')));

// a view-only reader: what it waits for, no actions
await go(32, '#hlogin');
ok('a view-only reader: the card says what it waits for and offers nothing', (await text('#hlogin b')) === 'Codex is waiting for a sign-in (in ▣ api-dev).'
  && (await text('#hl-view')).includes('You may only read this conversation') && !(await page.$('#hlogin [data-kind], #hl-retry, #hl-warn, #hl-confirm')), await text('#hlogin'));

// the end seam's rule: another park is not this card's (an approval or a question is harness-ask.js's, U4)
await go(22, '.hask');
ok('an approval park: its own card, no sign-in card', !(await page.$('#hlogin')) && !(await page.$('.hq')));
await go(23, '.hask.hq');
ok('a question park: its own card, no sign-in card', !(await page.$('#hlogin')));

ok('no page errors', errors.length === 0, errors.join(' | '));
await browser.close();
done('harness-term');
void API_DEV;
