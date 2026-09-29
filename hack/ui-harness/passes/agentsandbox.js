// hack/ui-harness/passes/agentsandbox.js — the agent in a coding sandbox
// (D115/D116; plans/sandbox-managers.md phase 1, the UI harness), end to
// end: the seeded apps/agent with its `sandboxes` slot bound to apps/fakesbx
// (seed.sh: hack/fakesandbox as a tile — every sandbox a host directory)
// and hack/fakeopenai scripting the coding tools by keyword ("sandbox pwd",
// "sandbox write", …; its header lists them). It pins:
//   - the composer's class picker lists internal / web / coding; the sandbox
//     picker (#ssel) shows only for a class with the sandbox toolset;
//   - a sandbox made in the Sandboxes dialog becomes the next new chat's; the
//     top bar's ▣ badge says it, and its popover sets the working directory
//     (from the next turn);
//   - bash (its exit reading), write, edit, a command that outlives its
//     timeout as a job, and a job that survives the agent's backend swap;
//   - D134: a command with pkill -f isn't run until forced, and then doesn't
//     kill its own job; bash_kill answers with the job's tail and signal;
//   - sandbox_create parks for the conversation owner's grant: the owner may
//     allow it, a participant may only deny;
//   - people (D83): dev1 doesn't see admin's private sandbox; in a
//     team-shared conversation dev1 works in its team sandbox;
//   - a terminal (phase 3: <bx-terminal src> on the manager's tty, the page's
//     frame token): Open terminal in the ▣ popover dials the fake's …/tty at
//     the binding's working directory; typed input is echoed, `pwd` is that
//     directory, Larger resizes the PTY (`stty size`), Close kills the shell
//     at the manager; the manager refuses dev1 admin's private sandbox.
// Driven as the tile's own document (/c/apps/agent/), like agentTemplate.
const path = require('path');
const { login, fs, log, settle, shot, dumpSelects, checker, noGocryptfs } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';
const WS = process.env.WS;

const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const text = (page, sel) => page.$$eval(sel, (els) => els.map((e) => e.textContent.trim()));
const api = (page, p, opt) => page.evaluate(async ([p, opt]) => {
  const r = await xbin.fetch(`/api/apps/agent${p}`, opt || {});
  let body = null;
  try { body = await r.json(); } catch { /* none */ }
  return { status: r.status, body };
}, [p, opt]);
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const convId = (page) => page.evaluate(() => +(/c=(\d+)/.exec(location.hash)?.[1] || 0));
const hidden = (page, sel) => page.$eval(sel, (e) => e.hidden || getComputedStyle(e).display === 'none');

async function openAgent(browser, user, pass) {
  const { ctx, page } = await login(browser, user, pass, { viewport: { width: 1300, height: 950 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(`${user}: ${e.message}`));
  page.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/apps/')) log(`agentSandbox ${user}: ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  return { ctx, page, errors };
}

// the class picker (at home): open it, read the rows, pick one
async function classRows(page) {
  if (!(await page.$('.clsmenu'))) {
    await page.click('#home');
    await page.click('#tset');
  }
  await page.waitForSelector('.clsmenu .mi');
  return page.$$eval('.clsmenu .mi', (els) => els.map((e) => ({ id: e.dataset.class, on: e.classList.contains('on') })));
}
async function pickClass(page, id) {
  await classRows(page);
  await page.click(`.clsmenu .mi[data-class="${id}"]`);
  await until(page, () => !document.querySelector('.clsmenu'));
}

// the picker's options once the list is read
async function pickerOptions(page) {
  await until(page, () => { const s = document.getElementById('ssel'); return s && !s.hidden && ![...s.options].some((o) => o.value === '+loading'); });
  return page.$$eval('#ssel option', (els) => els.map((o) => ({ value: o.value, label: o.textContent.trim(), disabled: o.disabled, group: o.parentElement.label || '' })));
}

// openDialog: Manage sandboxes… — and wait for the list it reads afresh: the
// rows it redraws with may come in another order (by last activity), so a
// click before it lands could hit a different row's button.
async function openDialog(page) {
  const fresh = page.waitForResponse((r) => /\/api\/apps\/agent\/sandboxes(\?fresh=1)?$/.test(r.url()) && r.request().method() === 'GET', { timeout: 15000 });
  await page.selectOption('#ssel', '+manage');
  await fresh;
  await until(page, () => document.getElementById('sbxdlg').open && !!document.getElementById('sbx-list')
    && !document.querySelector('#sbxdlg .muted')?.textContent.includes('loading…'));
  await settle(page);
}
const closeDialog = (page) => page.click('#sbx-close');
const dialogRows = (page) => page.$$eval('#sbxdlg .sbxrow', (els) => els.map((e) => ({ ref: e.dataset.ref, name: e.querySelector('.nm').textContent.trim(),
  acts: [...e.querySelectorAll('[data-act]')].map((b) => b.dataset.act) })));

// createInDialog: ＋ New sandbox → name, visibility → Create; the new row's ref
async function createInDialog(page, name, { team = false, bind = true } = {}) {
  await page.click('#sbx-new');
  await page.waitForSelector('#sbx-form');
  await page.fill('#sbxf-name', name);
  if (team) await page.selectOption('#sbxf-vis', 'team');
  if (!bind) await page.uncheck('#sbxf-bind');
  await page.click('#sbxf-create');
  await until(page, (n) => [...document.querySelectorAll('#sbxdlg .sbxrow .nm')].some((e) => e.textContent.trim() === n)
    || !!document.querySelector('#sbxf-err'), name, 30000);
  const err = await page.$('#sbxf-err');
  if (err) throw new Error(`create ${name}: ${await err.textContent()}`);
  return (await dialogRows(page)).find((r) => r.name === name).ref;
}

// turn: say msg in the open conversation; wait for a NEW answer holding want
// and for the run to settle; that answer's text.
async function turn(page, msg, want, timeout = 30000) {
  const count = (w) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].filter((e) => e.textContent.includes(w)).length;
  const n0 = await page.evaluate(`(${count.toString()})(${JSON.stringify(want)})`);
  await page.fill('#msg', msg);
  await page.press('#msg', 'Enter');
  await until(page, ([src, w, n]) => new Function('w', `return (${src})(w)`)(w) > n, [count.toString(), want, n0], timeout);
  await until(page, () => document.getElementById('stop').hidden, null, 15000);
  return (await text(page, '#timeline .msg.assistant:not(.live)')).filter((t) => t.includes(want)).pop() || '';
}
// the last tool card of a tool: its headline, outcome and state
const lastCard = (page, tool) => page.$$eval(`#timeline .tcard[data-tool="${tool}"]`, (els) => {
  const e = els[els.length - 1];
  if (!e) return null;
  return { fam: e.dataset.fam, hl: e.querySelector('.hl')?.textContent.trim() || '', oc: e.querySelector('.oc')?.textContent.trim() || '',
    tone: e.querySelector('.oc')?.className || '', st: [...e.classList].filter((c) => c !== 'tcard' && c !== 'on').join(' '),
    ic: e.querySelector('.ic')?.textContent.trim() || '' };
});
const badge = (page) => page.$eval('#sbxbadge', (e) => e.textContent.trim()).catch(() => '');

const reQuote = (t) => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
// the open terminal pane's buffer as text; its grid; wait for a line in it
const termText = (page) => page.$eval('#sbxterm-pane bx-terminal', (t) => t.testApi().text()).catch(() => '');
const termSize = (page) => page.$eval('#sbxterm-pane bx-terminal', (t) => t.testApi().size);
const termLine = (page, re, timeout = 15000) => until(page, (src) => new RegExp(src, 'm')
  .test(document.querySelector('#sbxterm-pane bx-terminal')?.testApi().text() || ''), re.source, timeout);
// a manager call from the page as its person (xbin.fetch: the frame token)
const mgr = (page, p, opt) => page.evaluate(async ([p, opt]) => {
  const r = await xbin.fetch(`/api/apps/fakesbx/sbx${p}`, opt || {});
  let body = null;
  try { body = await r.json(); } catch { /* none */ }
  return { status: r.status, body };
}, [p, opt]);

// terminal: the ▣ popover's Open terminal on the bound sandbox (id) at cwd —
// echo, pwd, a resize the PTY sees, Close ending the shell at the manager.
async function terminal(a, check, id, cwd) {
  const dialled = [];
  a.on('websocket', (w) => { if (w.url().includes('/sbx/')) dialled.push(w.url()); });
  await a.click('#sbxbadge');
  await a.waitForSelector('#sbxpop');
  const offer = await a.$('#sbx-term');
  check(!!offer && !(await offer.isDisabled()), `the popover offers Open terminal: the fake's hello says tty (${offer ? 'shown' : 'not shown'}${
    (await a.$('#sbx-term-why')) ? ': ' + (await a.textContent('#sbx-term-why')) : ''})`);
  if (!offer) return;
  await offer.click();
  await a.waitForSelector('#sbxterm-pane bx-terminal');
  await until(a, () => document.querySelector('#sbxterm-pane bx-terminal').testApi().open, null, 15000);
  const url = dialled.at(-1) || '';
  check(url.includes(`/api/apps/fakesbx/sbx/sandboxes/${id}/tty?cwd=${encodeURIComponent(cwd)}&frame=`),
    `it dials the manager's tty through xbind with the page's frame token, at the binding's cwd (${url.replace(/frame=[^&]+/, 'frame=…')})`);
  await termLine(a, /[$#] ?$/);
  await a.click('#sbxterm-pane bx-terminal');
  await a.keyboard.type('echo hi-from-tty');
  await a.keyboard.press('Enter');
  await termLine(a, /^hi-from-tty\s*$/);
  check(/echo hi-from-tty/.test(await termText(a)), 'typed `echo hi-from-tty`: the shell echoes the command and prints hi-from-tty');
  await a.keyboard.type('pwd');
  await a.keyboard.press('Enter');
  await termLine(a, new RegExp(`^${reQuote(cwd)}\\s*$`));
  check(true, `pwd in the terminal is the binding's working directory (${cwd})`);
  await shot(a, 'agent-sandbox-terminal', { fullPage: false });
  const before = await termSize(a);
  await a.click('#sbxterm-max');
  await until(a, (b) => { const s = document.querySelector('#sbxterm-pane bx-terminal').testApi().size; return s.cols > b.cols && s.rows > b.rows; }, before, 10000);
  const after = await termSize(a);
  await a.click('#sbxterm-pane bx-terminal'); // the keys go to the terminal again, not the ⤢ button
  await a.keyboard.type('stty size');
  await a.keyboard.press('Enter');
  await termLine(a, new RegExp(`^${after.rows} ${after.cols}\\s*$`));
  check(true, `Larger: the PTY follows the pane (${before.cols}×${before.rows} → ${after.cols}×${after.rows}, stty size agrees)`);
  await shot(a, 'agent-sandbox-terminal-max', { fullPage: false });
  const running = async () => ((await mgr(a, `/sandboxes/${id}/execs`)).body?.execs || []).filter((e) => e.tty && e.state === 'running');
  check((await running()).length === 1, `the manager lists the terminal's exec, running (${JSON.stringify((await mgr(a, `/sandboxes/${id}/execs`)).body).slice(0, 200)})`);
  await a.click('#sbxterm-close');
  await until(a, () => !document.getElementById('sbxterm-pane'));
  let left = [];
  for (let i = 0; i < 40; i++) { left = await running(); if (!left.length) break; await a.waitForTimeout(250); }
  check(left.length === 0, `Close ends the shell at the manager (${left.length} tty exec(s) still running)`);
}

async function agentSandbox(browser) {
  const { check, skip, done } = checker('agent-sandbox');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  const stamp = Date.now().toString(36);
  const BOX = `harness-box-${stamp}`, TEAM = `harness-team-${stamp}`;
  const A = await openAgent(browser, 'admin', 'admin');
  const a = A.page;
  let dev = null;
  // the class pick is remembered per person: put it back for the passes after
  const before = (await classRows(a)).find((r) => r.on)?.id || 'internal';
  let devBefore = '';
  try {
    // ---- 1. classes: internal has no sandbox picker, coding has ----
    const rows = (await classRows(a)).map((r) => r.id);
    check(['internal', 'web', 'coding'].every((c) => rows.includes(c)), `the class picker lists internal, web and coding (${rows.join(', ')})`);
    await shot(a, 'agent-sandbox-classes', { fullPage: false });
    await a.click('.clsmenu .mi[data-class="internal"]');
    await until(a, () => !document.querySelector('.clsmenu'));
    check(await hidden(a, '#ssel'), 'at home in the internal class: no sandbox picker');
    await a.fill('#msg', 'quick, internal');
    await a.press('#msg', 'Enter');
    await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Quick answer.')), null, 30000);
    check(/Internal/.test(await a.textContent('#top .clsbadge')), `the new chat is in the internal class (${(await a.textContent('#top .clsbadge')).trim()})`);
    check(await hidden(a, '#ssel') && !(await a.$('#sbxbadge')), 'an internal chat: no sandbox picker, no ▣ badge');
    await pickClass(a, 'coding');
    check(!(await hidden(a, '#ssel')), 'at home in the coding class: the sandbox picker shows');

    // ---- 2. the Sandboxes dialog: a private one (the next new chat's), a team one ----
    await openDialog(a);
    check(await a.$('#sbx-nomgr') === null, 'the dialog finds the bound manager (apps/fakesbx)');
    const boxRef = await createInDialog(a, BOX);
    check(boxRef.startsWith('apps/fakesbx|'), `created ${BOX} in the dialog (${boxRef})`);
    check(/your next new chat starts in it/.test(await a.textContent('#sbx-msg')), `…and it is the next new chat's (${(await a.textContent('#sbx-msg')).trim()})`);
    const tag = await a.$eval(`#sbxdlg .sbxrow[data-ref="${boxRef}"] .l1`, (e) => [...e.querySelectorAll('.badge')].map((b) => b.textContent.trim()));
    check(tag.includes('next new chat') && !tag.includes('active here'), `at home its row says it is the next new chat's (${JSON.stringify(tag)})`);
    const teamRef = await createInDialog(a, TEAM, { team: true, bind: false });
    const trow = await a.textContent(`#sbxdlg .sbxrow[data-ref="${teamRef}"]`);
    check(/team/.test(trow), `created ${TEAM} for the team (${trow.replace(/\s+/g, ' ').trim().slice(0, 120)})`);
    await shot(a, 'agent-sandbox-dialog', { fullPage: false });
    await closeDialog(a);
    const opts = await pickerOptions(a);
    const sel = opts.find((o) => o.value === boxRef);
    check(!!sel && (await a.$eval('#ssel', (s) => s.value)) === boxRef, `the composer's picker has ${BOX} picked (${JSON.stringify(sel)})`);
    await dumpSelects(a, 'agent-sandbox-picker-selects', '#ssel');
    await shot(a, 'agent-sandbox-composer', { fullPage: false });

    // ---- 3. a chat in it: bash, the working directory, write, edit, a job ----
    await a.fill('#msg', 'sandbox pwd');
    await a.press('#msg', 'Enter');
    await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Ran: ')), null, 30000);
    await until(a, () => document.getElementById('stop').hidden, null, 15000);
    const privId = await convId(a);
    const ran = (await text(a, '#timeline .msg.assistant:not(.live)')).filter((t) => t.includes('Ran: ')).pop();
    const workdir = (/Ran: (\S+)/.exec(ran) || [])[1] || '';
    check(workdir.endsWith('/work'), `"sandbox pwd": the agent ran pwd in the sandbox's workdir (${ran})`);
    const pwdCard = await lastCard(a, 'bash');
    check(pwdCard && pwdCard.fam === 'box' && pwdCard.ic === '▣', `bash is a ▣ card (${JSON.stringify(pwdCard)})`);
    check(pwdCard && /^exit 0\b/.test(pwdCard.oc) && /\bok\b/.test(pwdCard.tone), `its reading says how it ended: "${pwdCard && pwdCard.oc}"`);
    check(pwdCard && pwdCard.hl.includes('Where am I') && pwdCard.hl.includes('$ pwd'), `headed by the summary, the command under it (${pwdCard && pwdCard.hl})`);
    check((await badge(a)).startsWith(`▣ ${BOX}`), `the top bar's badge names the sandbox (${await badge(a)})`);

    // the popover: the working directory, from the next turn
    const home = workdir.replace(/\/work$/, '/home');
    await a.click('#sbxbadge');
    await a.waitForSelector('#sbxpop');
    await a.fill('#sbx-cwd', home);
    await a.click('#sbx-cwd-set');
    await until(a, (h) => document.getElementById('sbxbadge')?.textContent.includes(h), home, 10000);
    check((await badge(a)) === `▣ ${BOX} · ${home}`, `Set: the badge says ▣ name · cwd (${await badge(a)})`);
    check(!(await a.$('#sbx-err')), 'the popover took the working directory');
    await shot(a, 'agent-sandbox-badge', { fullPage: false });
    await a.keyboard.press('Escape');
    await until(a, () => !document.getElementById('sbxpop'));
    const ran2 = await turn(a, 'sandbox pwd again', 'Ran: ');
    check(ran2 === `Ran: ${home}`, `the next turn works at the new cwd (${ran2})`);

    // a terminal in it, at that working directory
    await terminal(a, check, boxRef.split('|').pop(), home);

    // write, edit, and what the file says now
    check((await turn(a, 'sandbox write', 'Wrote it.')) !== '', '"sandbox write": the agent wrote hello.txt');
    const wcard = await lastCard(a, 'write');
    check(wcard && wcard.fam === 'box' && wcard.hl.includes('Write hello.txt'), `write is a ▣ card (${wcard && wcard.hl})`);
    check((await turn(a, 'sandbox edit', 'Edited it.')) !== '', '"sandbox edit": the agent edited it');
    const ecard = await lastCard(a, 'edit');
    check(ecard && ecard.hl.includes('Edit hello.txt: hi → hello'), `the edit card shows old → new (${ecard && ecard.hl})`);
    await a.click('#timeline .tcard[data-tool="edit"] .tch >> nth=-1');
    await until(a, () => !!document.querySelector('#timeline .tcard.on[data-tool="edit"] .res'));
    const eres = await a.textContent('#timeline .tcard.on[data-tool="edit"] .res');
    check(/hello from the agent/.test(eres), `the edit's result shows the changed line (${eres.replace(/\s+/g, ' ').trim().slice(0, 120)})`);
    const cat = await turn(a, 'sandbox cat', 'Ran: ');
    check(cat === 'Ran: hello from the agent', `the file changed in the sandbox: cat hello.txt → "${cat}"`);
    await shot(a, 'agent-sandbox-cards', { fullPage: false });
    await a.click('#timeline .tcard[data-tool="edit"] .tch >> nth=-1');

    // a command that outlives its timeout goes on as a job
    const job = await turn(a, 'sandbox long', 'Job: ', 45000);
    check(job === 'Job: slow done', `"sandbox long": the job ran to its end (${job})`);
    const lcard = await lastCard(a, 'bash');
    check(lcard && /^still running · .*\bjob \d+$/.test(lcard.oc) && /\brun\b/.test(lcard.tone), `the bash card says it went on as a job: "${lcard && lcard.oc}"`);
    const ocard = await lastCard(a, 'bash_output');
    check(ocard && ocard.fam === 'box' && /^exit 0\b/.test(ocard.oc) && /Output of job \d+/.test(ocard.hl), `bash_output reads the job to its end (${ocard && ocard.hl} → ${ocard && ocard.oc})`);
    await shot(a, 'agent-sandbox-job', { fullPage: false });

    // D134: a kill-by-name command isn't run until forced, and then doesn't
    // kill its own job; bash_kill answers with the tail and the signal
    const pk = await turn(a, 'sandbox pkill', 'Ran: ');
    check(pk === 'Ran: before', `"sandbox pkill": refused, then forced (${pk})`);
    const pkCards = await a.$$eval('#timeline .tcard[data-tool="bash"]', (els) => els.slice(-2).map((e) => e.querySelector('.oc')?.textContent.trim() || ''));
    check(pkCards[0] === 'not run: kills by name', `the refused bash card says so (${pkCards[0]})`);
    check(/^exit 0\b/.test(pkCards[1] || ''), `the forced one outlived its own pkill -f: exit 0 (${pkCards[1]})`);
    const killed = await turn(a, 'sandbox kill', 'Killed: ');
    check(/^Killed: \[job \d+ stopped · killed by TERM\]$/.test(killed), `"sandbox kill": bash_kill says how it ended (${killed})`);
    const kcard = await lastCard(a, 'bash_kill');
    check(kcard && /^job \d+ stopped · killed by TERM$/.test(kcard.oc), `the bash_kill card reads its footer (${kcard && kcard.oc})`);
    await a.click('#timeline .tcard[data-tool="bash_kill"] .tch >> nth=-1');
    await until(a, () => !!document.querySelector('#timeline .tcard.on[data-tool="bash_kill"] .res'));
    const kres = await a.textContent('#timeline .tcard.on[data-tool="bash_kill"] .res');
    check(/line \d+/.test(kres), `its result shows the job's last output (${kres.replace(/\s+/g, ' ').trim().slice(0, 120)})`);
    await a.click('#timeline .tcard[data-tool="bash_kill"] .tch >> nth=-1');

    // ---- 4. a team conversation: dev1 in its team sandbox; the owner's grant ----
    await a.click('#home');
    await a.selectOption('#ssel', '');
    await a.fill('#msg', 'quick, for the team');
    await a.press('#msg', 'Enter');
    await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Quick answer.')), null, 30000);
    await until(a, () => document.getElementById('stop').hidden, null, 15000);
    const teamId = await convId(a);
    check(!(await a.$('#sbxbadge')) && (await a.$eval('#ssel', (s) => s.value)) === '', 'a coding chat started with "No sandbox" has none');
    const shared = await api(a, `/runs/${teamId}`, json('PATCH', { visibility: 'team', teamRole: 'participant' }));
    check(shared.status === 200, `shared with the team to read and write (${shared.status})`);
    await openDialog(a);
    const useBtn = `#sbxdlg .sbxrow[data-ref="${teamRef}"] [data-act="use"]`;
    check(!!(await a.$(useBtn)), `the dialog offers "Use here" on ${TEAM}`);
    await a.click(useBtn);
    await until(a, () => /this conversation's sandbox/.test(document.getElementById('sbx-msg')?.textContent || ''), null, 10000);
    await closeDialog(a);
    await until(a, (n) => document.getElementById('sbxbadge')?.textContent.includes(n), TEAM, 10000);
    check(true, `"Use here" bound ${TEAM}: the badge says it`);

    dev = await openAgent(browser, 'dev1', 'devpass123');
    const d = dev.page;
    devBefore = (await classRows(d)).find((r) => r.on)?.id || 'internal';
    await d.click('.clsmenu .mi[data-class="coding"]');
    await until(d, () => !document.querySelector('.clsmenu'));
    const dopts = await pickerOptions(d);
    check(!dopts.some((o) => o.value === boxRef), `dev1's picker doesn't list admin's private ${BOX}`);
    const tOpt = dopts.find((o) => o.value === teamRef);
    check(!!tOpt && !tOpt.disabled && tOpt.group === 'Team', `dev1's picker lists ${TEAM} under Team (${JSON.stringify(tOpt)})`);
    await dumpSelects(d, 'agent-sandbox-dev1-selects', '#ssel');
    await openDialog(d);
    const drows = (await dialogRows(d)).map((r) => r.ref);
    check(!drows.includes(boxRef) && drows.includes(teamRef), `dev1's Sandboxes dialog: ${TEAM}, not ${BOX}`);
    await shot(d, 'agent-sandbox-dev1-dialog', { fullPage: false });
    await closeDialog(d);
    // the manager itself refuses dev1 admin's private sandbox: a page's call carries its verified person
    const boxId = boxRef.split('|').pop();
    const peek = await mgr(d, `/sandboxes/${boxId}`);
    check([403, 404].includes(peek.status), `the manager refuses dev1's page admin's private ${BOX} (${peek.status} ${JSON.stringify(peek.body).slice(0, 120)})`);
    const sock = await d.evaluate((id) => new Promise((res) => {
      const w = xbin.ws(`/api/apps/fakesbx/sbx/sandboxes/${id}/tty`);
      w.onopen = () => { w.close(); res('opened'); };
      w.onclose = () => res('refused');
      setTimeout(() => res('timeout'), 10000);
    }), boxId);
    check(sock === 'refused', `…and a terminal onto it (${sock})`);

    await d.goto(`${URL}/c/apps/agent/#c=${teamId}`);
    await d.waitForSelector('#msg', { timeout: 30000 });
    await until(d, (n) => document.getElementById('sbxbadge')?.textContent.includes(n) && !document.getElementById('msg').disabled, TEAM, 15000);
    const dran = await turn(d, 'sandbox pwd, dev1 here', 'Ran: ');
    check(/^Ran: \/\S+\/work$/.test(dran) && dran !== ran, `dev1 works in the team sandbox of the team conversation (${dran})`);

    // sandbox_create: dev1 asks, only admin may allow
    await d.fill('#msg', 'new sandbox please');
    await d.press('#msg', 'Enter');
    await until(d, () => !!document.querySelector('.ask.approve.grant'), null, 30000);
    const dcard = await d.textContent('.ask.approve.grant');
    const dbtns = await text(d, '.ask.approve.grant .btn');
    check(dbtns.length === 1 && dbtns[0] === 'Deny', `dev1 (a participant) may only deny (${JSON.stringify(dbtns)})`);
    check(/Only admin can allow this/.test(dcard), `…and is told who can allow it (${dcard.replace(/\s+/g, ' ').trim().slice(0, 140)})`);
    await shot(d, 'agent-sandbox-grant-participant', { fullPage: false });
    await until(a, () => !!document.querySelector('.ask.approve.grant'), null, 15000);
    const acard = await a.textContent('.ask.approve.grant');
    check(/sandbox_create/.test(acard) && (await text(a, '.ask.approve.grant .btn')).includes('Allow once'), `the owner is asked to allow sandbox_create (${acard.replace(/\s+/g, ' ').trim().slice(0, 140)})`);
    await shot(a, 'agent-sandbox-grant', { fullPage: false });
    await a.click('.ask.approve.grant .btn:has-text("Allow once")');
    await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Created: ')), null, 30000);
    const created = (await text(a, '#timeline .msg.assistant:not(.live)')).filter((t) => t.includes('Created: ')).pop();
    check(/^Created: Created the sandbox "scratch"/.test(created), `allowed once, the agent made it (${created.slice(0, 160)})`);
    await until(d, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => e.textContent.includes('Created: ')), null, 15000);
    check(true, 'dev1 sees it made too');
    await until(a, () => document.getElementById('stop').hidden, null, 15000);
    await a.click('#sbxbadge');
    await a.waitForSelector('#sbxpop');
    const att = await text(a, '#sbxpop .sbxatt');
    check(att.length === 2 && att.some((t) => t.includes(TEAM) && t.startsWith('●')) && att.some((t) => t.includes('scratch') && t.startsWith('○'))
      && !att.some((t) => t.includes('⚠')), // the new one isn't "gone": the list is read again for it
      `the popover lists it attached beside the active ${TEAM} (${JSON.stringify(att)})`);
    const spill = await a.$eval('#sbxpop', (p) => {
      const edge = p.getBoundingClientRect().right + 0.5;
      return [...p.querySelectorAll('.sbxatt, #sbx-cwd, #sbx-manage')].filter((e) => e.getBoundingClientRect().right > edge).map((e) => e.id || e.className);
    });
    check(spill.length === 0, `long working directories stay inside the popover, Manage… too (${JSON.stringify(spill)})`);
    await shot(a, 'agent-sandbox-attached', { fullPage: false });
    await a.keyboard.press('Escape');

    // ---- 5. a job survives the agent's backend swap ----
    if (WS) {
      await a.goto(`${URL}/c/apps/agent/#c=${privId}`);
      await a.waitForSelector('#msg', { timeout: 30000 });
      await until(a, (n) => document.getElementById('sbxbadge')?.textContent.includes(n), BOX, 15000);
      const nCards = (await a.$$('#timeline .tcard[data-tool="bash"]')).length;
      await a.fill('#msg', 'sandbox restart');
      await a.press('#msg', 'Enter');
      await until(a, (n) => document.querySelectorAll('#timeline .tcard[data-tool="bash"]').length > n, nCards, 30000);
      const bump = path.join(WS, 'apps/agent/_backend/zz_harness.go');
      const t0 = Date.now();
      fs.writeFileSync(bump, `package main\n\n// harness: a save that swaps the backend (${Date.now()})\n`);
      try {
        await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')].some((e) => /(Job|Ran): survived/.test(e.textContent)), null, 180000);
        const ans = (await text(a, '#timeline .msg.assistant:not(.live)')).filter((t) => /survived/.test(t)).pop();
        log(`agentSandbox restart: answered ${JSON.stringify(ans)} ${Date.now() - t0} ms after the save`);
        check(ans === 'Job: survived', ans === 'Ran: survived'
          ? `the swap came only after the command ended (${Date.now() - t0} ms): nothing was cut off — a slow build? (fakeopenai's "sandbox restart" sleeps 8 s)`
          : `the command went on across the swap as a job, and the successor followed it (${ans})`);
        const cut = await lastCard(a, 'bash');
        check(cut && /^went on as job \d+$/.test(cut.oc) && /\brun\b/.test(cut.tone) && /\bdone\b/.test(cut.st),
          `the cut-off bash card says it went on as a job, not struck through as stopped (${cut && `${cut.oc} · ${cut.st}`})`);
        const follow = await lastCard(a, 'bash_output');
        check(follow && /^exit 0\b/.test(follow.oc), `bash_output picked the job up to its end (${follow && follow.oc})`);
        await a.click('#timeline .tcard[data-tool="bash"] .tch >> nth=-1');
        await shot(a, 'agent-sandbox-restart', { fullPage: false });
      } finally {
        fs.rmSync(bump, { force: true });
      }
    }
  } finally {
    // the remembered class picks back as they were (the other passes ask in them)
    await pickClass(a, before).catch((e) => log(`agentSandbox: restoring admin's class: ${e.message}`));
    if (dev && devBefore) await pickClass(dev.page, devBefore).catch((e) => log(`agentSandbox: restoring dev1's class: ${e.message}`));
  }
  check(A.errors.length === 0 && (!dev || dev.errors.length === 0), `no page errors (${[...A.errors, ...(dev ? dev.errors : [])].join(' | ')})`);
  await A.ctx.close();
  if (dev) await dev.ctx.close();
  done();
}

// the helpers the sandboxTerminal pass drives the same dialog with
module.exports = { agentSandbox, openAgent, classRows, pickClass, openDialog, closeDialog, dialogRows, createInDialog };
