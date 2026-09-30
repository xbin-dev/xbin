// hack/ui-harness/passes/agentharness.js — coding agents in the agent
// template (D-harness, plans/agtt-harness.md §7.2, §7.3, §8 U9), end to end
// on a real xbind: the seeded apps/agent with its `sandboxes` slot bound to
// apps/fakesbx (seed.sh), whose image advertises the scripted ACP agent
// "fake" (run.sh: FSB_HARNESS_FAKE = bin/fakeacp --steer --auto-mode
// --require-login --persist, a host process in a host-directory sandbox),
// and hack/fakeopenai scripting the AgTT agent ("harness fan out", "harness
// steer"). It drives the real UI, web and native (the reference renderer
// playing the app: /c/apps/agent/?native=1&preview=1), and asserts:
//   1. home → "Who answers" → Fake agent → a sandbox (the setup card's
//      Create) → "cards": a card per ACP kind; "todo": the 📋 plan pin; usage;
//   2. "perm": the permission card with the agent's own options → Allow
//      once → the turn completes; a rejection with feedback (the next
//      prompt); "plan": the plan approval;
//   3. "ask": the question form → answered;
//   4. a sandbox with no credentials: the sign-in card → the login terminal
//      (the terminal dock; `fake-code`) → "Signed in? Retry" → the held
//      prompt answers; the API-key method, the key never echoed;
//   5. the AgTT agent: "harness fan out" → three coding agents → the
//      parent's child cards → the Coding agents board (chip, "needs you") →
//      a child's park answered from the board → "harness steer"; a person's
//      direct message to a child → the parent's notice;
//   6. #hctl: a bypass mode (⚠, confirmed, the owner's), Auto / Always
//      approve; Enter steers a running turn, ⌘⏎ interrupts it.
// Screenshots at 1280 and 480 px (and the native view at 390 × 844) go to
// out/agent-harness-*.png. The sandboxes not used for the sign-in shots are
// signed in by writing ~/.fakeacp/credentials in their HOME (the manager's
// files API, as the person).
const { login, log, shot, checker, noGocryptfs, settle } = require('../lib');

const URL = process.env.URL || 'http://127.0.0.1:8697';

const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 50 });
const text = (page, sel) => page.$$eval(sel, (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
const call = (page, p, opt) => page.evaluate(async ([p, opt]) => {
  const r = await xbin.fetch(p, opt || {});
  const t = await r.text();
  let body = t;
  try { body = JSON.parse(t); } catch { /* text */ }
  return { status: r.status, body };
}, [p, opt]);
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const convId = (page) => page.evaluate(() => +(/c=(\d+)/.exec(location.hash)?.[1] || 0));
const say = async (a, t) => { await a.fill('#msg', t); await a.press('#msg', 'Enter'); };
// answered: an assistant row (not the live draft) holding t
const answered = (a, t, timeout = 30000) => until(a, (t) => [...document.querySelectorAll('#timeline .msg.assistant:not(.live)')]
  .some((e) => e.textContent.includes(t)), t, timeout);
// resting: the open conversation's turn is over (Stop hidden)
const resting = (a, timeout = 20000) => until(a, () => document.getElementById('stop').hidden, null, timeout);
const viewOf = async (a, id) => (await call(a, `/api/apps/agent/runs/${id}/view`)).body;
const runOf = async (a, id) => (await viewOf(a, id)).run || {};

async function wide(a, w) {
  await a.setViewportSize({ width: w, height: 900 });
  await settle(a);
}
// both widths of the state on screen
async function shots(a, name) {
  await shot(a, `agent-harness-${name}`, { fullPage: false });
  await wide(a, 480);
  await a.waitForTimeout(150);
  await shot(a, `agent-harness-${name}-480`, { fullPage: false });
  await wide(a, 1280);
}

// home: "Who answers" → id ('agent': the built-in)
async function whoAnswers(a, id) {
  await a.click('#home');
  await a.waitForSelector('#abtn');
  if (!(await a.$('.clsmenu'))) await a.click('#abtn');
  await a.waitForSelector(`.clsmenu .mi[data-agent="${id}"]`);
  await a.click(`.clsmenu .mi[data-agent="${id}"]`);
  await until(a, () => !document.querySelector('.clsmenu'));
}

// newSandbox: the create form (the home's setup card's Create when it offers
// one, else the picker's ＋ New sandbox) → a sandbox with internet access,
// the next new chat's; signed: its HOME gets the fake agent's credentials.
async function newSandbox(a, name, { signed, viaSetup = false }) {
  if (viaSetup) await a.click('#hsetup-create');
  else {
    await whoAnswers(a, 'fake'); // at home, the picker of the sandboxes it fits
    await a.selectOption('#ssel', '+new');
  }
  await a.waitForSelector('#sbx-form');
  const form = { egress: await a.$eval('#sbxf-egress', (s) => s.value), name: await a.$eval('#sbxf-name', (s) => s.value) };
  await a.fill('#sbxf-name', name);
  await a.selectOption('#sbxf-egress', 'internet');
  await a.click('#sbxf-create');
  await until(a, (n) => [...document.querySelectorAll('#sbxdlg .sbxrow .nm')].some((e) => e.textContent.trim() === n) || !!document.querySelector('#sbxf-err'), name, 30000);
  const err = await a.$('#sbxf-err');
  if (err) throw new Error(`create ${name}: ${await err.textContent()}`);
  const ref = await a.$$eval('#sbxdlg .sbxrow', (els, n) => els.find((e) => e.querySelector('.nm').textContent.trim() === n).dataset.ref, name);
  await a.click('#sbx-close');
  const id = ref.split('|').pop();
  const box = (await call(a, `/api/apps/fakesbx/sbx/sandboxes/${id}`)).body;
  if (signed) {
    const put = await call(a, `/api/apps/fakesbx/sbx/sandboxes/${id}/files/content?path=${encodeURIComponent(box.home + '/.fakeacp/credentials')}&mkdirs=1`,
      { method: 'PUT', body: 'signed in by the UI harness\n' });
    if (put.status !== 200) throw new Error(`sign ${name} in: ${put.status} ${JSON.stringify(put.body)}`);
  }
  return { ref, id, name, home: box.home, form };
}

// start: at home, with the fake agent answering, in sandbox ref: say t → the
// new conversation's id
async function start(a, ref, t) {
  await whoAnswers(a, 'fake');
  if ((await a.$eval('#ssel', (s) => s.value)) !== ref) await a.selectOption('#ssel', ref);
  await until(a, (r) => document.getElementById('ssel').value === r, ref);
  const before = await convId(a);
  await say(a, t);
  await until(a, (b) => { const m = /c=(\d+)/.exec(location.hash); return m && +m[1] !== b; }, before, 20000);
  return convId(a);
}

// the cards of the open conversation, by ACP kind
const cards = (a) => a.$$eval('#timeline .tcard.hcard', (els) => els.map((e) => ({ kind: e.dataset.kind, status: e.dataset.status, k: e.dataset.k,
  hl: e.querySelector('.hl')?.textContent.replace(/\s+/g, ' ').trim() || '', oc: e.querySelector('.oc')?.textContent.trim() || '',
  st: e.querySelector('.st')?.textContent.trim() || '' })));

// ---- 1. start, the transcript ----
async function transcript(a, check, box) {
  const id = await start(a, box.ref, 'cards');
  await answered(a, 'cards done');
  await resting(a);
  const cs = await cards(a);
  const kinds = cs.map((c) => c.kind);
  check(['read', 'edit', 'delete', 'move', 'search', 'execute', 'fetch', 'think', 'other'].every((k) => kinds.includes(k)) && cs.length === 9,
    `"cards": a card per ACP kind (${kinds.join(', ')})`);
  check(cs.every((c) => c.status === 'completed'), `every card completed (${cs.map((c) => c.status).join(', ')})`);
  const ex = cs.find((c) => c.kind === 'execute'), ed = cs.find((c) => c.kind === 'edit'), rd = cs.find((c) => c.kind === 'read');
  check(ex && ex.hl.includes('go test') && ex.oc === 'exit 0', `the execute card: its command and exit 0 (${ex && `${ex.hl} · ${ex.oc}`})`);
  check(ed && ed.hl === 'Edit hello.txt' && ed.oc === '+1 −1', `the edit card: its file and +1 −1 (${ed && `${ed.hl} · ${ed.oc}`})`);
  check(rd && rd.hl === 'Read hello.txt', `the read card: its file (${rd && rd.hl})`);
  const chip = await a.$eval('#hchip', (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
  check(/^FA\s*Fake agent \(tests\) · ready/.test(chip), `the top bar's chip: who answers and its state (${chip})`);
  // open the edit's patch and the command's output
  await a.click('#timeline .tcard[data-kind="edit"] .tch');
  await a.click('#timeline .tcard[data-kind="edit"] .hfh');
  await a.click('#timeline .tcard[data-kind="execute"] .tch');
  await a.waitForSelector('#timeline .tcard[data-kind="edit"] .hdiff');
  const patch = await a.textContent('#timeline .tcard[data-kind="edit"] .hdiff');
  check(/@@ -1 \+1 @@\s*-hello\s*\+hello, world/.test(patch), `the edit's patch, from the backend's diff (${patch.replace(/\s+/g, ' ').slice(-120)})`);
  const out = await a.textContent('#timeline .tcard[data-kind="execute"] .tcb');
  check(/ok\s+example/.test(out) && /exit 0/.test(out), `the execute card's output and exit (${out.replace(/\s+/g, ' ').slice(0, 120)})`);
  await a.$eval('#timeline .tcard[data-kind="edit"]', (e) => e.scrollIntoView({ block: 'start' }));
  await shots(a, 'cards');
  await a.click('#timeline .tcard[data-kind="edit"] .tch');
  await a.click('#timeline .tcard[data-kind="execute"] .tch');

  // the plan: live while the turn runs, then 3/3
  await say(a, 'todo');
  await until(a, () => !!document.querySelector('.planpin'), null, 15000);
  const live = await a.textContent('.planpin .tasktoggle');
  await answered(a, 'todo done');
  await resting(a);
  await until(a, () => /3\/3/.test(document.querySelector('.planpin .tasktoggle')?.textContent || ''), null, 10000);
  check(/📋 Plan · [0-3]\/3/.test(live), `"todo": the 📋 plan pin shows while the turn runs (${live.trim()})`);
  await a.click('.planpin .tasktoggle');
  const entries = await text(a, '.planpin .pe');
  check(entries.length === 3 && (await a.$$('.planpin .pe.completed')).length === 3, `…and unfolds to its 3 entries, all done (${entries.join(' | ')})`);
  const usage = await a.$eval('.husage', (e) => e.textContent.trim()).catch(() => '');
  check(/^ctx \d+%/.test(usage), `the usage badge: the context in use (${usage})`);
  const counts = await a.$eval('.hcounts', (e) => e.textContent.trim()).catch(() => '');
  check(/9 tool calls · 1 file \+1 −1/.test(counts), `what the conversation did (${counts})`);
  await shots(a, 'plan-pin');
  await a.click('.planpin .tasktoggle');
  // no "steered" note for messages sent while no turn ran
  check(!(await a.$('#hsteer .hsn')), 'messages sent between turns are not said to be steered');
  return id;
}

// ---- 2. permissions ----
async function permissions(a, check) {
  await say(a, 'perm');
  await a.waitForSelector('.hask:not(.hq)', { timeout: 20000 });
  const opts = await a.$$eval('.hask .hopts button', (els) => els.map((e) => `${e.dataset.opt}:${e.dataset.kind}:${e.textContent.trim()}`));
  check(opts.join(' ') === 'once:allow_once:Allow once always:allow_always:Allow always no:reject_once:Reject', `"perm": the card offers the agent's own options (${opts.join(' ')})`);
  const lead = await a.textContent('.hask .hlead');
  check(/Fake agent \(tests\) asks to run a command · run ls/.test(lead.replace(/\s+/g, ' ')), `…saying what it asks (${lead.replace(/\s+/g, ' ').trim()})`);
  const card = (await cards(a)).pop();
  check(card && card.kind === 'execute' && card.st === 'needs approval', `its call's card waits for the approval (${card && `${card.hl} · ${card.st}`})`);
  const status = (await runOf(a, await convId(a))).status;
  check(status === 'waiting_input', `the run waits for a person (${status})`);
  await shots(a, 'permission');
  await a.click('.hask .hopts button[data-opt="once"]');
  await answered(a, 'listed');
  await resting(a);
  const done = (await cards(a)).pop();
  check(done && done.status === 'completed' && !(await a.$('.hask')), `Allow once: the call completes, the turn ends, the card goes (${done && done.status})`);
  // a rejection with a word: the next prompt
  await say(a, 'perm again');
  await a.waitForSelector('.hask:not(.hq)', { timeout: 20000 });
  const n = (await cards(a)).length;
  await a.fill('.hask .hfb', 'use ls -la instead');
  await a.click('.hask .hopts button[data-opt="no"]');
  await answered(a, 'echo: use ls -la instead');
  await resting(a);
  const users = await text(a, '#timeline .msg.user');
  check(users.at(-1) === 'use ls -la instead', `Reject with a word: it is the next message (${users.slice(-2).join(' | ')})`);
  const all = await cards(a);
  check(all.length === n && all.at(-1).status === 'failed' && all.at(-2).status === 'completed',
    `the rejected call is a card of its own, the approved one still completed (${all.slice(-2).map((c) => c.status).join(', ')})`);
  await shots(a, 'rejected');
  // plan approval: the plan, its options, "keep planning"
  await say(a, 'plan');
  await a.waitForSelector('.hask.hplan', { timeout: 20000 });
  const plan = await a.$eval('.hask.hplan', (e) => ({ md: e.querySelector('.hplanmd')?.textContent || '', opts: [...e.querySelectorAll('.hopts button')].map((b) => b.textContent.trim()) }));
  check(/Fake plan/.test(plan.md) && plan.opts.length >= 2, `"plan": the plan approval — the plan and its options (${plan.opts.join(' | ')})`);
  await shots(a, 'plan-approval');
  await a.click('.hask.hplan .hopts button:not(.ghost):not(.hwarn)');
  await answered(a, 'plan approved');
  await resting(a);
  check(true, 'approving the plan ends the turn');
}

// ---- 3. a question ----
async function questionPark(a, check) {
  await say(a, 'ask');
  await a.waitForSelector('.hask.hq', { timeout: 20000 });
  const fields = await a.$$eval('.hask.hq .hfield', (els) => els.map((e) => e.dataset.field));
  check(fields.join(',') === 'question_0,question_1', `"ask": the question form's fields (${fields.join(', ')})`);
  await a.click('.hask.hq .hfield[data-field="question_0"] label.hopt:has-text("SQLite") input');
  await a.click('.hask.hq .hfield[data-field="question_1"] label.hopt:has-text("Tracing") input');
  await shots(a, 'question');
  await a.click('.hask.hq .hopts button:has-text("Submit")');
  await answered(a, 'answers:');
  await resting(a);
  const ans = (await text(a, '#timeline .msg.assistant:not(.live)')).filter((t) => t.startsWith('answers:')).pop() || '';
  check(/"question_0":"SQLite"/.test(ans) && /"question_1":\["Tracing"\]/.test(ans), `Submit: the agent got the answers (${ans})`);
}

// ---- 4. sign-in ----
async function signIn(a, check, stamp) {
  // (a) the terminal method
  const box = await newSandbox(a, `harness-login-${stamp}`, { signed: false });
  const id = await start(a, box.ref, 'hello after sign-in');
  await a.waitForSelector('#hlogin', { timeout: 30000 });
  const methods = await a.$$eval('#hlogin .hlm [data-method]', (els) => els.map((e) => `${e.dataset.method}:${e.dataset.kind}`));
  check(['fake-login:terminal', 'fake-api-key:api-key', 'fake-device:device-code'].every((m) => methods.includes(m)), `a signed-out agent: the sign-in card offers its methods (${methods.join(', ')})`);
  check(/credentials land in .*'s home/.test(await a.textContent('#hl-warn')), `…warning where the credentials go (${(await a.textContent('#hl-warn')).trim().slice(0, 100)})`);
  const r0 = await runOf(a, id);
  check(r0.status === 'waiting_input' && r0.pendingState?.kind === 'login' && r0.harness?.state === 'login', `the run waits on a sign-in (${r0.status} · ${r0.pendingState?.kind} · ${r0.harness?.state})`);
  await shots(a, 'signin');
  // at home it Needs you, in the sign-in's words; the row opens it again
  await a.click('#home');
  await a.waitForSelector(`.need[data-r="${id}"]`, { timeout: 15000 });
  const need = (await a.textContent(`.need[data-r="${id}"]`)).replace(/\s+/g, ' ').trim();
  check(/🔑 .*needs you to sign in to Fake agent \(tests\)/.test(need), `Needs you at home: its sign-in (${need})`);
  await a.click(`.need[data-r="${id}"]`);
  await a.waitForSelector('#hlogin', { timeout: 15000 });
  await a.click('#hlogin button[data-method="fake-login"]');
  await a.waitForSelector('#sbxterm-pane bx-terminal');
  const term = () => a.$eval('#sbxterm-pane bx-terminal', (t) => t.testApi().text()).catch(() => '');
  await until(a, () => /paste the code/.test(document.querySelector('#sbxterm-pane bx-terminal')?.testApi().text() || ''), null, 20000);
  check(true, 'the login terminal runs the agent\'s sign-in in its sandbox');
  await a.click('#sbxterm-pane bx-terminal');
  await a.keyboard.type('fake-code');
  await a.keyboard.press('Enter');
  await until(a, () => /Signed in\./.test(document.querySelector('#sbxterm-pane bx-terminal')?.testApi().text() || ''), null, 15000);
  check(/Signed in\./.test(await term()), 'fake-code: "Signed in."');
  await shots(a, 'login-terminal');
  await a.click('#sbxterm-retry');
  await answered(a, 'echo: hello after sign-in', 30000);
  await resting(a);
  check(!(await a.$('#hsteer .hsn')), 'a new conversation\'s first message is not said to be steered');
  const r1 = await runOf(a, id);
  check(r1.status === 'idle' && r1.harness?.state === 'ready', `Signed in? Retry: the held prompt answers (${r1.status} · ${r1.harness?.state})`);
  check(!(await a.$('#sbxterm-pane')), 'the login tab goes with the retry');
  await shots(a, 'signed-in');

  // (b) the API-key method: the key is sent once and never shown
  const kbox = await newSandbox(a, `harness-key-${stamp}`, { signed: false });
  const kid = await start(a, kbox.ref, 'hello with a key');
  await a.waitForSelector('#hlogin form[data-method="fake-api-key"]', { timeout: 30000 });
  const KEY = `sk-harness-${stamp}-secret`;
  const sent = [];
  a.on('request', (r) => { if (r.url().includes('/harness/authenticate')) sent.push(r.postData() || ''); });
  await a.fill('#hlogin form[data-method="fake-api-key"] input', KEY);
  await a.press('#hlogin form[data-method="fake-api-key"] input', 'Enter');
  await answered(a, 'echo: hello with a key', 30000);
  await resting(a);
  check(sent.length === 1 && sent[0].includes(KEY), `the key went once, to authenticate (${sent.length} request(s))`);
  const page = await a.evaluate(() => document.documentElement.outerHTML + [...document.querySelectorAll('input')].map((i) => i.value).join('\n'));
  const view = JSON.stringify(await viewOf(a, kid));
  const logTail = (await call(a, `/api/apps/agent/runs/${kid}/harness/log`)).body;
  check(!page.includes(KEY) && !view.includes(KEY) && !String(logTail).includes(KEY), 'the key is nowhere: not in the page, the conversation or the agent\'s log');
  await shots(a, 'api-key-signed-in');
  return { box, id };
}

// ---- 5. the AgTT agent's coding agents ----
async function children(a, check, box, stamp) {
  await whoAnswers(a, 'agent');
  if (!(await a.$('.clsmenu'))) await a.click('#tset');
  await a.click('.clsmenu .mi[data-class="coding"]');
  await until(a, () => !document.querySelector('.clsmenu'));
  await a.selectOption('#ssel', box.ref);
  await until(a, (r) => document.getElementById('ssel').value === r, box.ref);
  await say(a, `harness fan out ${stamp}`);
  await answered(a, 'Started three coding agents.', 60000);
  const parent = await convId(a);
  await until(a, () => document.querySelectorAll('#timeline .hkid').length >= 3, null, 30000);
  const kids = await a.$$eval('#timeline .hkid', (els) => els.map((e) => ({ id: +e.dataset.child, state: e.dataset.state, t: e.querySelector('.hl')?.textContent.replace(/\s+/g, ' ').trim() })));
  check(kids.length === 3 && kids.every((k) => k.id), `"harness fan out": three coding agent cards in the parent's chat (${kids.map((k) => `#${k.id} ${k.t} ${k.state}`).join(' | ')})`);
  // the perm child parks on a person
  await until(a, () => [...document.querySelectorAll('#timeline .hkid')].some((e) => e.dataset.state === 'approval'), null, 30000);
  const parked = await a.$eval('#timeline .hkid[data-state="approval"]', (e) => ({ id: +e.dataset.child, btns: [...e.querySelectorAll('.hask .hopts button')].map((b) => b.dataset.opt) }));
  check(parked.btns.includes('once'), `a child's permission is on its card, answerable there (#${parked.id}: ${parked.btns.join(', ')})`);
  await shots(a, 'children');
  // the board: chip, the needs-you filter, the park answered from it
  await a.waitForSelector('#hbchip', { timeout: 15000 });
  const chip = (await a.textContent('#hbchip')).trim();
  check(/3|need/.test(chip), `the top bar's Coding agents chip (${chip})`);
  await a.click('#hbchip');
  await a.waitForSelector('#hboard .hbrow', { timeout: 15000 });
  const rows = await a.$$eval('#hboard .hbrow', (els) => els.map((e) => `${e.dataset.row}:${e.dataset.section}`));
  check(rows.length === 3, `the board lists the three (${rows.join(', ')})`);
  await shots(a, 'board');
  const f = await a.$('#hboard .hbfilter');
  check(!!f && /need/.test(await f.textContent()), `the board's "needs you" filter (${f ? (await f.textContent()).trim() : 'none'})`);
  if (f) {
    await f.click();
    await until(a, () => document.querySelectorAll('#hboard .hbrow').length === 1, null, 5000).catch(() => {});
    const only = await a.$$eval('#hboard .hbrow', (els) => els.map((e) => +e.dataset.row));
    check(only.length === 1 && only[0] === parked.id, `…shows only the one waiting for you (${only.join(', ')})`);
    await shots(a, 'board-needs');
    await a.click(`#hboard .hbrow[data-row="${parked.id}"] .hask .hopts button[data-opt="once"]`);
    await until(a, (id) => ![...document.querySelectorAll('#hboard .hbrow')].some((e) => +e.dataset.row === id && e.dataset.section === 'needs'), parked.id, 20000).catch(() => {});
    await a.click('#hboard .hbfilter').catch(() => {});
  }
  await until(a, (id) => document.querySelector(`#timeline .hkid[data-child="${id}"]`)?.dataset.state !== 'approval', parked.id, 20000).catch(() => {});
  const cr = await runOf(a, parked.id);
  const cs = await a.$eval(`#timeline .hkid[data-child="${parked.id}"]`, (e) => e.dataset.state).catch(() => '');
  check(cs !== 'approval' && cr.status !== 'waiting_input', `answered from the board: the child goes on (${cs}, ${cr.status})`);
  await a.click('#hboard [data-act="close"]').catch(() => {});

  // "harness steer": the agent messages its newest coding agent
  await say(a, 'harness steer');
  await answered(a, 'Steered it.', 60000);
  const steerReply = await a.evaluate(() => [...document.querySelectorAll('#timeline .tcard[data-tool="subagent_message"]')].map((e) => e.textContent.replace(/\s+/g, ' ')).pop() || '');
  check(!!steerReply, `"harness steer": the agent messaged its coding agent (${steerReply.slice(0, 160)})`);

  // a person's direct message to a child: the parent's notice
  const target = kids.find((k) => k.id !== parked.id).id;
  await a.click(`#timeline .hkid[data-child="${target}"] .hkact [data-act="message"]`);
  await a.fill(`#timeline .hkid[data-child="${target}"] .hkin`, 'from a person: use tabs');
  await a.click(`#timeline .hkid[data-child="${target}"] .hkmsg [data-act="send"]`);
  await until(a, (id) => !!document.querySelector(`#timeline .hkid[data-child="${id}"] .hknote`), target, 15000).catch(() => {});
  const note = await a.$eval(`#timeline .hkid[data-child="${target}"] .hknote`, (e) => e.textContent.trim()).catch(() => '');
  check(!!note, `a person's message to a child from its card (${note})`);
  // the parent reads the notice at its next step: a turn of the parent shows it
  await say(a, 'quick, what now?');
  await until(a, (id) => [...document.querySelectorAll('#timeline .notice')].some((e) => e.textContent.includes(`direct message to #${id}`)), target, 60000).catch(() => {});
  const notice = await a.$$eval('#timeline .notice', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()).filter((t) => t.includes('direct message to #'))).catch(() => []);
  check(notice.length > 0 && notice.some((t) => t.includes(`#${target}`) && /admin/.test(t)), `…and the parent is told, as a notice (${notice.join(' | ').slice(0, 160)})`);
  await shots(a, 'parent-notice');
  return { parent, kids };
}

// ---- 6. controls ----
async function controls(a, check, conv, dialogs) {
  await a.goto(`${URL}/c/apps/agent/#c=${conv}`);
  await a.waitForSelector('#hctl button', { timeout: 30000 });
  await a.click('#hctl button');
  await a.waitForSelector('#hctl-pop .hmode');
  const modes = await a.$$eval('#hctl-pop .hmode', (els) => els.map((e) => `${e.dataset.mode}${e.classList.contains('hwarn') ? '⚠' : ''}${e.classList.contains('off') ? '(off)' : ''}`));
  check(modes.join(' ') === 'ask auto yolo⚠', `#hctl: the agent's modes, its bypass one marked ⚠ and open to the owner (${modes.join(' ')})`);
  await shots(a, 'hctl');
  const n0 = dialogs.length;
  await a.click('#hctl-pop .hmode[data-mode="yolo"] input');
  await until(a, () => /Yolo/.test(document.querySelector('#hctl button')?.textContent || ''), null, 15000).catch(() => {});
  check(dialogs.length === n0 + 1 && /bypass|without asking|Yolo/i.test(dialogs.at(-1) || ''), `choosing it asks to confirm (${(dialogs.at(-1) || 'no dialog').slice(0, 140)})`);
  const h = (await runOf(a, conv)).harness || {};
  check(h.mode?.current === 'yolo', `…then the conversation runs in it (${h.mode?.current})`);
  // back to ask
  if (!(await a.$('#hctl-pop .hmode'))) await a.click('#hctl button');
  await a.click('#hctl-pop .hmode[data-mode="ask"] input');
  await until(a, () => /Ask/.test(document.querySelector('#hctl button')?.textContent || ''), null, 15000).catch(() => {});
  // the setting: Auto / Always approve
  if (!(await a.$('#hctl-pop [data-setting]'))) await a.click('#hctl button');
  const set = await a.$$eval('#hctl-pop [data-setting]', (els) => els.map((e) => `${e.dataset.setting}:${e.getAttribute('aria-pressed')}`));
  check(set.includes('auto:false') && set.includes('approve:true'), `your setting: Always approve now, Auto offered (${set.join(' ')})`);
  await a.click('#hctl-pop [data-setting="auto"]');
  await until(a, () => document.querySelector('#hctl-pop [data-setting="auto"]')?.getAttribute('aria-pressed') === 'true', null, 10000).catch(() => {});
  const pref = (await call(a, '/api/apps/agent/prefs/harness-mode')).body;
  check(pref?.modes?.fake === 'auto', `Auto: stored as your setting for the fake agent (${JSON.stringify(pref)})`);
  await shots(a, 'setting');
  await a.click('#hctl-pop [data-setting="approve"]');
  await until(a, () => document.querySelector('#hctl-pop [data-setting="approve"]')?.getAttribute('aria-pressed') === 'true', null, 10000).catch(() => {});
  await a.keyboard.press('Escape');
  await a.click('#timeline');

  // steering: Enter sends into the running turn
  await say(a, 'steer me');
  await until(a, () => !document.getElementById('stop').hidden, null, 10000);
  const ph = await a.$eval('#msg', (m) => m.placeholder);
  check(/^steer Fake agent \(tests\) — sent into its running turn/.test(ph), `a running turn: the composer steers (${ph})`);
  await say(a, 'use tabs');
  await until(a, () => !!document.querySelector('#hsteer .hsn'), null, 10000).catch(() => {});
  const sn = await a.$eval('#hsteer .hsn', (e) => e.textContent.trim()).catch(() => '');
  await shots(a, 'steered');
  await answered(a, 'steers: use tabs', 30000);
  await resting(a);
  check(/steered into Fake agent \(tests\)'s running turn: use tabs/.test(sn), `Enter: steered into the running turn, and said so (${sn})`);
  // ⌘⏎ interrupts, then the message is the next prompt
  await say(a, 'slow');
  await until(a, () => [...document.querySelectorAll('#timeline .msg.assistant.live')].some((e) => /tick/.test(e.textContent)), null, 10000).catch(() => {});
  await a.fill('#msg', 'after the interrupt');
  await a.press('#msg', 'Control+Enter');
  await answered(a, 'echo: after the interrupt', 30000);
  await resting(a);
  const tail = (await text(a, '#timeline .msg')).slice(-4).join(' | ');
  check(!/tick 9/.test(tail), `⌘⏎: the slow turn was cut short, the message answered next (${tail.slice(0, 200)})`);
}

// ---- native: the reference renderer playing the app ----
// The app's view on the real backend: /c/apps/agent/?native=1&preview=1 (web/xb
// preview-host.js) draws native.js's trees and turns clicks into the app's
// taps. Its terminal is a placeholder (previews never connect), so the login
// terminal is the tile's relay dialled from the page, as the app's would.
async function native(browser, check, box, parent, stamp) {
  const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 390, height: 844 } });
  const errs = [];
  await page.goto(`${URL}/c/apps/agent/`);
  await page.waitForSelector('#msg', { timeout: 30000 });
  const ask = async (text, ref) => (await call(page, '/api/apps/agent/ask', json('POST', { text, class: 'coding', harness: { provider: 'fake' }, sandbox: { ref } }))).body.id;
  const tree = () => page.evaluate(() => JSON.stringify(window.xbnPreview?.tree?.() || null));
  const has = async (want, timeout = 20000, quiet = false) => {
    let t = '';
    for (const end = Date.now() + timeout; Date.now() < end;) { t = await tree(); if (want.every((w) => t.includes(w))) return true; await page.waitForTimeout(200); }
    if (!quiet) log(`agentHarness native: missing ${want.filter((w) => !t.includes(w)).join(', ')}`);
    return false;
  };
  const open = async (id) => {
    await page.goto(`${URL}/c/apps/agent/?native=1&preview=1#c=${id}`);
    await page.waitForFunction(() => window.xbnPreview && window.xbnPreview.ready, null, { timeout: 30000 });
    await page.evaluate(() => window.xbnPreview.ready);
  };
  const tap = (name) => page.getByRole('button', { name, exact: true }).first().click();
  // the screens on the app's navigation stack, bottom first (their titles)
  const stack = () => page.evaluate(() => {
    const find = (n) => (!n ? null : n.t === 'nav' ? n : (n.c || []).map(find).find(Boolean) || null);
    const t = window.xbnPreview.tree();
    const nav = find(t && (t.root || t));
    return nav ? nav.c.map((x) => (x.p && x.p.title) || '') : [];
  });
  const done = () => page.evaluate(() => (window.xbnPreview.errors || []).map((e) => String(e.message || e))).then((e) => errs.push(...e));

  // a transcript of cards
  const cardsId = await ask('cards', box.ref);
  await open(cardsId);
  check(await has(['"toolcard"', 'Edit hello.txt', 'go test ./...', 'cards done']), 'native: "cards" — a tool card per call, and the answer');
  await shot(page, 'agent-harness-native-cards', { fullPage: false });
  await done();
  // a permission, answered in the app
  const permId = await ask('perm', box.ref);
  await open(permId);
  check(await has(['"approval"', 'Allow once', 'Allow always', 'Reject']), 'native: "perm" — the approval with the agent\'s own options');
  await shot(page, 'agent-harness-native-permission', { fullPage: false });
  await tap('Allow once');
  check(await has(['listed']), 'native: Allow once — the call runs, the turn completes');
  await done();
  // a question
  const askId = await ask('ask', box.ref);
  await open(askId);
  check(await has(['"question"', 'Database', 'Extras: Metrics', 'Extras: Tracing']), 'native: "ask" — the question\'s fields, each choice named for its question');
  await shot(page, 'agent-harness-native-question', { fullPage: false });
  await tap('Skip').catch(async () => tap('Decline'));
  check(await has(['skipped']), 'native: skipping the question answers decline');
  await done();
  // the sign-in: the notice, the Sign in screen, the relay's login terminal, Retry
  const nb = await call(page, '/api/apps/agent/sandboxes', json('POST', { name: `native-login-${stamp}`, provider: 'apps/fakesbx', egress: 'internet' }));
  const loginId = await ask('native hello', nb.body.ref);
  await open(loginId);
  check(await has(['Sign in to Fake agent (tests)']), 'native: a signed-out agent — the sign-in notice');
  await shot(page, 'agent-harness-native-signin', { fullPage: false });
  await tap('Sign in');
  check(await has(['Open a login terminal', 'Signed in? Retry']), 'native: the Sign in screen — the login terminal, an API key, a device code, Retry');
  await shot(page, 'agent-harness-native-signin-screen', { fullPage: false });
  const relay = await page.evaluate(async (id) => new Promise((res) => {
    const w = xbin.ws(`/api/apps/agent/runs/${id}/harness/terminal?login=1&rows=24&cols=80`);
    w.binaryType = 'arraybuffer';
    let text = '', sent = false;
    const frames = [];
    w.onmessage = (e) => {
      if (typeof e.data === 'string') { frames.push(e.data); return; }
      text += new TextDecoder().decode(e.data);
      if (!sent && /paste the code/.test(text)) { sent = true; w.send(new TextEncoder().encode('fake-code\r')); }
    };
    w.onclose = () => res({ text, frames });
    setTimeout(() => { try { w.close(); } catch { /* gone */ } res({ text, frames, timeout: true }); }, 15000);
  }), loginId);
  check(/Signed in\./.test(relay.text) && relay.frames.some((f) => f.includes('"op":"session"')) && relay.frames.some((f) => f.includes('"op":"exit"')),
    `native's login terminal (the run's relay, as the person): the session frame, the sign-in, its exit (${JSON.stringify(relay).slice(0, 240)})`);
  await tap('Signed in? Retry');
  check(await has(['echo: native hello']), 'native: Signed in? Retry — the held prompt answers');
  const after = await stack();
  check(!after.some((t) => /^Sign in/.test(t)), `native: …and the Sign in screen leaves (${after.join(' › ')})`);
  await shot(page, 'agent-harness-native-signed-in', { fullPage: false });
  await done();
  // the parent with its coding agents, and the board
  await open(parent);
  check(await has(['coder 1', 'coder 2', 'coder 3', 'Started three coding agents.']), 'native: the parent — a card per coding agent');
  await shot(page, 'agent-harness-native-parent', { fullPage: false });
  // (a phone's bar in a built-in coding conversation — its model and sandbox
  // pickers — pushes More past the edge in the reference renderer: an open
  // issue of the native toolbar, not this pass's; the tap goes to it anyway)
  await page.getByRole('button', { name: 'More', exact: true }).first().dispatchEvent('click');
  await page.getByRole('button', { name: /^Coding agents/ }).first().dispatchEvent('click');
  await page.waitForFunction(() => JSON.stringify(window.xbnPreview.tree()).includes('"title":"Coding agents"'), null, { timeout: 10000 }).catch(() => {});
  const board = await stack();
  check(board.at(-1) === 'Coding agents' && await has(['coder 1', 'coder 2', 'coder 3']), `native: ⋯ → Coding agents — the board (${board.join(' › ')})`);
  await shot(page, 'agent-harness-native-board', { fullPage: false });
  await done();
  check(!errs.length, `native: no errors (${errs.join(' | ').slice(0, 300)})`);
  await ctx.close();
}

async function agentHarness(browser) {
  const { check, skip, done } = checker('agent-harness');
  if (noGocryptfs()) { skip(`apps/agent is held: ${noGocryptfs()}`); return done(); }
  if (process.env.HARNESS_ISOLATE) { skip('the fake coding agent is a host path (run.sh FSB_HARNESS_FAKE): not under HARNESS_ISOLATE'); return done(); }
  const stamp = Date.now().toString(36);
  const { ctx, page: a } = await login(browser, 'admin', 'admin', { viewport: { width: 1280, height: 900 } });
  const errors = [], dialogs = [];
  a.on('pageerror', (e) => errors.push(e.message));
  a.on('dialog', (d) => { dialogs.push(d.message()); d.accept().catch(() => {}); });
  a.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/apps/agent/')) log(`agentHarness: ${r.status()} ${r.request().method()} ${r.url()}`); });
  await a.goto(`${URL}/c/apps/agent/`);
  await a.waitForSelector('#msg', { timeout: 30000 });
  // what the other passes expect back: the built-in agent answering, the class
  // new chats start in (both remembered per person)
  await whoAnswers(a, 'agent');
  await a.click('#tset');
  await a.waitForSelector('.clsmenu .mi');
  const classBefore = await a.$eval('.clsmenu .mi.on', (e) => e.dataset.class).catch(() => '');
  await a.keyboard.press('Escape');
  await until(a, () => !document.querySelector('.clsmenu'), null, 5000).catch(() => a.click('#tset'));
  const out = {};
  try {
    // ---- 1. "Who answers" ----
    await a.click('#abtn');
    await a.waitForSelector('.clsmenu .mi[data-agent="fake"]');
    const rows = await a.$$eval('.clsmenu .mi', (els) => els.map((e) => ({ id: e.dataset.agent, off: e.classList.contains('off'), t: e.textContent.replace(/\s+/g, ' ').trim() })));
    const fake = rows.find((r) => r.id === 'fake'), cc = rows.find((r) => r.id === 'claude');
    check(!!fake && !fake.off && /in ▣ Coding/.test(fake.t), `"Who answers" offers the fake agent, in the coding class (${fake && fake.t})`);
    check(!!cc && cc.off && /no bound sandbox manager's image has it/.test(cc.t), `…and says why Claude Code isn't there (${cc && cc.t})`);
    await shots(a, 'picker');
    await a.click('.clsmenu .mi[data-agent="fake"]');
    await until(a, () => !document.querySelector('.clsmenu'));
    await until(a, () => ![...document.getElementById('ssel').options].some((o) => o.value === '+loading'), null, 15000);
    const setup = await a.$('#hsetup-create');
    if (setup) check(/Create fake-dev/.test(await setup.textContent()), `no sandbox fits yet: the setup card offers to create one (${(await setup.textContent()).trim()})`);
    else skip('a sandbox already fits (a rerun on the same workspace): no setup card');
    await shots(a, 'setup');
    const box = out.box = await newSandbox(a, `harness-fake-${stamp}`, { signed: true, viaSetup: !!setup });
    if (setup) check(box.form.egress === 'internet' && box.form.name === 'fake-dev', `the create form comes filled in for it (${JSON.stringify(box.form)})`);
    out.cards = await transcript(a, check, box);
    await permissions(a, check);
    await questionPark(a, check);
    out.signin = await signIn(a, check, stamp);
    out.kids = await children(a, check, box, stamp);
    await controls(a, check, out.cards, dialogs);
  } finally {
    await whoAnswers(a, 'agent').catch((e) => log(`agentHarness: putting "Who answers" back: ${e.message}`));
    if (classBefore) {
      await (async () => {
        if (!(await a.$('.clsmenu'))) await a.click('#tset');
        await a.click(`.clsmenu .mi[data-class="${classBefore}"]`);
        await until(a, () => !document.querySelector('.clsmenu'), null, 5000);
      })().catch((e) => log(`agentHarness: putting the class back: ${e.message}`));
    }
  }
  // a fresh page (the built-in agent answering, no sandbox list read yet):
  // the picker names the sandbox the fake agent last started in, not its id
  await a.reload();
  await a.waitForSelector('#abtn', { timeout: 30000 });
  await a.click('#abtn');
  await until(a, () => / signed in on (?!sb-\d)/.test(document.querySelector('.clsmenu .mi[data-agent="fake"]')?.textContent || ''), null, 5000).catch(() => {});
  const seen = (await a.textContent('.clsmenu .mi[data-agent="fake"]')).replace(/\s+/g, ' ').trim();
  check(/(not )?signed in on /.test(seen) && !/ on sb-\d+\b/.test(seen), `"Who answers" says where it is signed in, by the sandbox's name (${seen})`);
  await a.keyboard.press('Escape');
  check(errors.length === 0, `no page errors (${errors.join(' | ')})`);
  await ctx.close();
  await native(browser, check, out.box, out.kids.parent, stamp);
  done();
}

module.exports = { agentHarness };
