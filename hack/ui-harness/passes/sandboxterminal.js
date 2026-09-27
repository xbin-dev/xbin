// hack/ui-harness/passes/sandboxterminal.js — the sandbox-terminal builtin
// tile (D121), end to end on the real xbind: seed.sh imports it, binds its
// `sandboxes` slot to apps/fakesbx (hack/fakesandbox as a tile) and publishes
// its SSH on $SBXTERM_SSH_ADDR (the address people type set by its owner).
// It pins:
//   - the agent's Sandboxes dialog shares a sandbox of admin's with
//     apps/sandbox-terminal ("Share with a terminal tile…": PATCH {shares,
//     version}, for admin); one it doesn't share stays out of the terminal tile;
//   - the terminal tile lists the shared one under its manager, with its ssh
//     command; Open terminal dials the manager's tty with the page's frame
//     token and `echo hi-term` answers;
//   - after a reload the running terminal is listed (the manager's execs) and
//     Attach replays its screen; End ends it at the manager;
//   - a key generated with ssh-keygen (into $HARNESS_DIR) registered through
//     the page, then OpenSSH through xbind's port relay: `ssh -i key -p <port>
//     <login>@127.0.0.1 echo hi-ssh` prints hi-ssh;
//   - the tile's native.js boots in its page document and draws the sandbox;
//   - dev1 (read access to the tile, nothing shared with them) gets the
//     empty state that says how sandboxes get here, and no manager controls;
//   - dev1 taken off the tile (PUT /access none): their key registered at the
//     start logs nobody in — OpenSSH hears "access revoked" — and it is kept,
//     marked inactive, in everyone's keys (the tile asks xbind's GET
//     /api/xbin/access/<user> at every login; it keeps an answer 30 s — the
//     page's own requests too — so the pass waits that out after the PUT).
const path = require('path');
const { execFileSync } = require('child_process');
const { URL, fs, log, settle, shot, checker, noGocryptfs, login, sleep } = require('../lib');
const { openAgent, classRows, pickClass, openDialog, closeDialog, createInDialog } = require('./agentsandbox');

const until = (page, fn, arg, timeout = 20000) => page.waitForFunction(fn, arg ?? null, { timeout, polling: 100 });
const TILE = 'apps/sandbox-terminal';
const SSH_ADDR = process.env.SBXTERM_SSH_ADDR || '127.0.0.1:8699';
const HDIR = process.env.HARNESS_DIR || path.join(__dirname, '..', 'out');

// the open tab's terminal: its text; wait for a line in it
const termText = (page) => page.$eval('#terms bx-terminal.on', (t) => t.testApi().text()).catch(() => '');
const termLine = (page, re, timeout = 15000) => until(page, (src) => new RegExp(src, 'm')
  .test(document.querySelector('#terms bx-terminal.on')?.testApi().text() || ''), re.source, timeout);
// a manager call from the terminal tile's page, as its person (the frame token)
const mgr = (page, p, opt) => page.evaluate(async ([p, opt]) => {
  const r = await xbin.fetch(`/api/apps/fakesbx/sbx${p}`, opt || {});
  let body = null;
  try { body = await r.json(); } catch { /* none */ }
  return { status: r.status, body };
}, [p, opt]);

async function openTerminalTile(ctx) {
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('dialog', (d) => d.accept());
  page.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/apps/')) log(`sandboxTerminal: ${r.status()} ${r.request().method()} ${r.url()}`); });
  await page.goto(`${URL}/c/${TILE}/`);
  await page.waitForSelector('#ssh-state', { timeout: 60000 });
  return { page, errors };
}

// sshAs runs `ssh -i key login@host cmd` (OpenSSH, no agent, a connection
// of its own — never a ControlMaster the user's ssh config keeps, which
// would skip the login): what it printed on stdout and stderr, and its status.
function sshAs(keyFile, login_, host, port, cmd) {
  try {
    const out = execFileSync('ssh', ['-i', keyFile, '-p', port, '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null',
      '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'LogLevel=ERROR', '-o', 'ConnectTimeout=15',
      '-o', 'ControlMaster=no', '-o', 'ControlPath=none',
      `${login_}@${host}`, cmd], { encoding: 'utf8', timeout: 45000, env: { ...process.env, SSH_AUTH_SOCK: '' }, stdio: ['ignore', 'pipe', 'pipe'] });
    return { out, status: 0 };
  } catch (e) { return { out: `${e.stdout || ''}${e.stderr || ''}`, status: e.status }; }
}

// newKeyFile makes an ed25519 key pair under $HARNESS_DIR: its path and .pub line.
function newKeyFile(name) {
  const keyFile = path.join(HDIR, name);
  fs.rmSync(keyFile, { force: true });
  fs.rmSync(`${keyFile}.pub`, { force: true });
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', `harness@${name}`, '-f', keyFile]);
  return { keyFile, pub: fs.readFileSync(`${keyFile}.pub`, 'utf8').trim() };
}

async function sandboxTerminal(browser) {
  const { check, skip, done } = checker('sandbox-terminal');
  if (noGocryptfs()) { skip(`apps/agent and apps/fakesbx are held: ${noGocryptfs()}`); return done(); }
  const stamp = Date.now().toString(36);
  const BOX = `term-box-${stamp}`, OTHER = `unshared-${stamp}`;
  const [host, port] = SSH_ADDR.split(':');

  // ---- 0. dev1 (read access to the tile) registers a key: step 7 takes
  // them off the tile and tries it ----
  const dev1Key = newKeyFile('sbxterm-dev1-key');
  let dev1KeyId = '';
  {
    const { ctx: dctx } = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1200, height: 800 } });
    const D = await openTerminalTile(dctx);
    await D.page.fill('#key-text', dev1Key.pub);
    await D.page.click('#key-add');
    await D.page.waitForSelector('#keys .key[data-key-id]', { timeout: 15000 }).catch(() => {});
    dev1KeyId = await D.page.$eval('#keys .key[data-key-id]', (e) => e.dataset.keyId).catch(() => '');
    check(!!dev1KeyId, 'dev1 (read access) registers a key on the page');
    await dctx.close();
  }

  // ---- 1. the agent shares a sandbox with the terminal tile ----
  const A = await openAgent(browser, 'admin', 'admin');
  const a = A.page;
  const before = (await classRows(a)).find((r) => r.on)?.id || 'internal';
  let boxRef = '';
  try {
    await pickClass(a, 'coding');
    await openDialog(a);
    boxRef = await createInDialog(a, BOX, { bind: false });
    const otherRef = await createInDialog(a, OTHER, { bind: false });
    check(boxRef.startsWith('apps/fakesbx|') && otherRef.startsWith('apps/fakesbx|'), `created ${BOX} and ${OTHER} in the agent's dialog`);
    const patched = a.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().includes('/api/apps/agent/sandboxes/'), { timeout: 15000 });
    await a.click(`#sbxdlg .sbxrow[data-ref="${boxRef}"] [data-act="shareTerm"]`);
    await a.waitForSelector('#sbx-share');
    const tile = await a.$eval('#sbxs-tile', (e) => e.value);
    const who = (await a.textContent('#sbxs-who')).trim();
    check(tile === TILE && who === 'you', `"Share with a terminal tile…": the builtin's path, for you (${tile} · ${who})`);
    await shot(a, 'sandbox-terminal-agent-share', { fullPage: false });
    await a.click('#sbxs-share');
    const resp = await patched;
    const sent = JSON.parse(resp.request().postData() || '{}');
    check(resp.status() === 200 && JSON.stringify(sent.shares) === JSON.stringify([{ consumer: TILE, users: ['admin'] }]) && Number.isInteger(sent.version),
      `Share: PATCH {shares: [{consumer: ${TILE}, users: [admin]}], version} through the agent to the manager (${resp.status()} ${JSON.stringify(sent)})`);
    const kept = (await resp.json().catch(() => ({}))).shares || [];
    check(kept.some((s) => s.consumer === TILE), `the manager keeps the share (${JSON.stringify(kept)})`);
    await a.waitForSelector('#sbx-msg');
    check(/is shared with apps\/sandbox-terminal/.test(await a.textContent('#sbx-msg')), `…and the dialog says so (${(await a.textContent('#sbx-msg')).trim()})`);
    await closeDialog(a);
  } finally {
    await pickClass(a, before).catch((e) => log(`sandboxTerminal: restoring admin's class: ${e.message}`));
  }
  check(A.errors.length === 0, `no page errors in the agent (${A.errors.join(' | ')})`);
  await A.ctx.close();
  if (!boxRef) return done();
  const boxId = boxRef.split('|').pop();
  const key = `apps/fakesbx|${boxId}`;

  // ---- 2. the terminal tile lists it; a terminal onto it ----
  const { ctx } = await login(browser, 'admin', 'admin', { viewport: { width: 1200, height: 900 } });
  const T = await openTerminalTile(ctx);
  const p = T.page;
  await p.waitForSelector(`.sbx[data-key="${key}"]`, { timeout: 30000 }).catch(() => {});
  const listed = await p.$$eval('.sbx', (els) => els.map((e) => e.querySelector('.nm').textContent.trim()));
  check(listed.includes(BOX), `the terminal tile lists the shared sandbox (${listed.join(', ')})`);
  check(!listed.includes(OTHER), `…and not the one the agent didn't share (${OTHER})`);
  const mgrTitle = await p.$eval(`.mgr[data-provider="apps/fakesbx"]`, (e) => e.textContent.replace(/\s+/g, ' ').trim()).catch(() => '');
  check(mgrTitle.includes('apps/fakesbx'), `grouped under its manager (${mgrTitle})`);
  const login_ = await p.$eval(`.sbx[data-key="${key}"]`, (e) => e.dataset.login).catch(() => '');
  const sshCmd = await p.$eval(`.sbx[data-key="${key}"] [data-ssh]`, (e) => e.textContent.trim()).catch(() => '');
  check(sshCmd === `ssh ${login_}@${host} -p ${port}`, `its ssh command, with the address the owner set (${sshCmd})`);
  check((await p.$eval('#ssh-state', (e) => e.dataset.ready)) === '1', 'the SSH panel says SSH is ready');
  await shot(p, 'sandbox-terminal-list');

  const dialled = [];
  p.on('websocket', (w) => { if (w.url().includes('/sbx/')) dialled.push(w.url()); });
  await p.click(`.sbx[data-key="${key}"] [data-act="open"]`);
  await p.waitForSelector('#terms bx-terminal.on');
  await until(p, () => document.querySelector('#terms bx-terminal.on').testApi().open, null, 20000);
  const url = dialled.at(-1) || '';
  check(url.includes(`/api/apps/fakesbx/sbx/sandboxes/${boxId}/tty?frame=`) || url.includes(`/api/apps/fakesbx/sbx/sandboxes/${boxId}/tty&frame=`),
    `Open terminal dials the manager's tty through xbind with the page's frame token (${url.replace(/frame=[^&]+/, 'frame=…')})`);
  await termLine(p, /[$#] ?$/);
  await p.click('#terms bx-terminal.on');
  await p.keyboard.type('echo hi-term');
  await p.keyboard.press('Enter');
  await termLine(p, /^hi-term\s*$/).then(() => check(true, 'typed `echo hi-term`: the shell answers hi-term'),
    async () => check(false, `typed echo hi-term, never saw it (${(await termText(p)).slice(-200)})`));
  await until(p, (k) => !!document.querySelector(`.sbx[data-key="${k}"] .run[data-exec]`), key, 10000).catch(() => {});
  const runs = await p.$$eval(`.sbx[data-key="${key}"] .run`, (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  check(runs.length === 1 && /open here/.test(runs[0]), `the sandbox lists its running terminal, open here (${JSON.stringify(runs)})`);
  await shot(p, 'sandbox-terminal-terminal', { fullPage: false });

  // ---- 3. a reload: the running terminal is attached again; End ends it ----
  await p.reload();
  await p.waitForSelector(`.sbx[data-key="${key}"] .run[data-exec] [data-act="attach"]`, { timeout: 20000 }).catch(() => {});
  const attach = await p.$(`.sbx[data-key="${key}"] .run[data-exec] [data-act="attach"]`);
  check(!!attach, 'after a reload the running terminal is listed, to attach');
  if (attach) {
    const eid = await p.$eval(`.sbx[data-key="${key}"] .run[data-exec]`, (e) => e.dataset.exec);
    await attach.click();
    await p.waitForSelector('#terms bx-terminal.on');
    await termLine(p, /^hi-term\s*$/).then(() => check(true, `Attach reattaches to exec ${eid}: its screen replays (hi-term)`),
      async () => check(false, `Attach: no replay (${(await termText(p)).slice(-200)})`));
    await p.click('#terms bx-terminal.on');
    await p.keyboard.type('echo again-$((20+1))');
    await p.keyboard.press('Enter');
    await termLine(p, /^again-21\s*$/).then(() => check(true, '…and it goes on: the same shell answers'), () => check(false, 'the attached shell did not answer'));
    await shot(p, 'sandbox-terminal-attached', { fullPage: false });
    await p.click('#term-end');
    let left = [];
    for (let i = 0; i < 40; i++) {
      left = ((await mgr(p, `/sandboxes/${boxId}/execs`)).body?.execs || []).filter((e) => e.tty && e.state === 'running');
      if (!left.length) break;
      await p.waitForTimeout(250);
    }
    check(left.length === 0, `End ends the shell at the manager (${left.length} tty exec(s) still running)`);
    check(!(await p.$('#terms')), 'and closes its tab');
  }

  // ---- 4. an SSH key through the page; OpenSSH through the relay ----
  const keyFile = path.join(HDIR, 'sbxterm-key');
  fs.rmSync(keyFile, { force: true });
  fs.rmSync(`${keyFile}.pub`, { force: true });
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', 'harness@sbxterm', '-f', keyFile]);
  const pub = fs.readFileSync(`${keyFile}.pub`, 'utf8').trim();
  const fp = execFileSync('ssh-keygen', ['-lf', `${keyFile}.pub`], { encoding: 'utf8' }).split(' ')[1];
  await p.fill('#key-text', 'not a key');
  await p.click('#key-add');
  check(/doesn't look like an OpenSSH public key/.test(await p.textContent('#key-err').catch(() => '')), 'a bad paste is refused on the page');
  await p.fill('#key-text', pub);
  await p.fill('#key-name', 'harness laptop');
  await p.click('#key-add');
  await until(p, (f) => [...document.querySelectorAll('#keys .key')].some((e) => e.textContent.includes(f)), fp, 15000).catch(() => {});
  const keyRow = await p.$$eval('#keys .key', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()));
  check(keyRow.some((t) => t.includes(fp) && t.includes('harness laptop')), `the key is registered through the page (${JSON.stringify(keyRow)})`);
  await settle(p);
  await shot(p, 'sandbox-terminal-keys');

  let out = '';
  try {
    out = execFileSync('ssh', ['-i', keyFile, '-p', port, '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null',
      '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'LogLevel=ERROR', '-o', 'ConnectTimeout=15',
      `${login_}@${host}`, 'echo hi-ssh'], { encoding: 'utf8', timeout: 45000, env: { ...process.env, SSH_AUTH_SOCK: '' } });
  } catch (e) { out = `ERR ${e.status}: ${e.stderr || e.message}`; }
  log(`sandboxTerminal: ssh said ${JSON.stringify(out)}`);
  check(out.split(/\r?\n/).includes('hi-ssh'), `ssh -i key -p ${port} ${login_}@${host} echo hi-ssh → hi-ssh (${JSON.stringify(out.slice(0, 300))})`);
  let refused = '';
  try {
    execFileSync('ssh', ['-i', keyFile, '-p', port, '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null',
      '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'LogLevel=ERROR', '-o', 'ConnectTimeout=15',
      `nope-${stamp}@${host}`, 'true'], { encoding: 'utf8', timeout: 45000, env: { ...process.env, SSH_AUTH_SOCK: '' } });
  } catch (e) { refused = `${e.stdout || ''}${e.stderr || ''}`; }
  check(/no sandbox "nope-/.test(refused) && refused.includes(login_), `an unknown sandbox name lists the ones you may use (${JSON.stringify(refused.slice(0, 200))})`);

  // ---- 5. the native view boots in the tile's document ----
  {
    const np = await ctx.newPage();
    const nerr = [];
    np.on('pageerror', (e) => nerr.push(e.message));
    await np.addInitScript(() => { window.__xbn = []; window.xbnHost = { post: (m) => window.__xbn.push(m) }; });
    await np.goto(`${URL}/c/${TILE}/`);
    await np.waitForSelector('#ssh-state', { timeout: 30000 }).catch(() => {});
    await np.evaluate(async () => { const xb = await import('/vendor/xb-native.js'); await xb.boot('./native.js'); });
    await until(np, (n) => JSON.stringify(window.__xbn || []).includes(n), BOX, 20000).catch(() => {});
    const msgs = await np.evaluate(() => (window.__xbn || []).map((m) => JSON.parse(JSON.stringify(m))));
    const bad = msgs.filter((m) => m.op === 'error');
    check(msgs.some((m) => m.op === 'mount') && !bad.length, `native.js mounts a tree with no error (${bad.map((m) => m.message || JSON.stringify(m)).join(' | ')})`);
    check(JSON.stringify(msgs).includes(BOX) && JSON.stringify(msgs).includes('Terminals open in the browser'),
      'the native view lists the sandbox and says terminals open in the browser');
    check(nerr.length === 0, `no page errors booting native.js (${nerr.join(' | ')})`);
    await np.close();
  }

  check(T.errors.length === 0, `no page errors in the terminal tile (${T.errors.join(' | ')})`);
  await ctx.close();

  // ---- 6. dev1: nothing shared with them ----
  {
    const { ctx: dctx } = await login(browser, 'dev1', 'devpass123', { viewport: { width: 1200, height: 800 } });
    const D = await openTerminalTile(dctx);
    const d = D.page;
    await d.waitForSelector('#empty', { timeout: 15000 }).catch(() => {});
    const empty = await d.textContent('#empty').catch(() => '');
    check(/No sandboxes here for you yet/.test(empty) && /Share with a terminal tile/.test(empty),
      `dev1: the empty state says how a sandbox gets here (${empty.replace(/\s+/g, ' ').trim().slice(0, 160)})`);
    check(!(await d.$(`.sbx[data-key="${key}"]`)), `dev1 doesn't see admin's sandbox (shared for admin only)`);
    check(!(await d.$('#ssh-addr')) && !(await d.$('#all-keys')), 'dev1 (read access) gets no manager controls');
    await shot(d, 'sandbox-terminal-empty');
    check(D.errors.length === 0, `no page errors for dev1 (${D.errors.join(' | ')})`);
    await dctx.close();
  }

  // ---- 7. dev1 taken off the tile: their key logs nobody in ----
  if (dev1KeyId) {
    const { ctx: actx } = await login(browser, 'admin', 'admin', { viewport: { width: 1200, height: 800 } });
    const access = (level) => actx.request.put(`${URL}/api/xbin/access`, { data: { tile: TILE, kind: 'user', id: 'dev1', level } });
    try {
      const put = await access('none');
      check(put.ok(), `admin takes dev1 off ${TILE} (PUT /access none: ${put.status()})`);
      // past every answer the tile kept before the PUT: its own asks, and
      // dev1's page requests (step 6 — a request's level is xbind's answer
      // too, kept the same 30 s)
      log('sandboxTerminal: waiting 31 s for the tile\'s cached answer to lapse');
      await sleep(31000);
      const r = sshAs(dev1Key.keyFile, 'anything', host, port, 'echo in');
      log(`sandboxTerminal: dev1's ssh said ${JSON.stringify(r)}`);
      check(r.status === 1 && /access revoked: you no longer have access/.test(r.out) && !/^in$/m.test(r.out),
        `dev1's key after their access went: "access revoked", exit 1 (${JSON.stringify(r.out.slice(0, 200))} · ${r.status})`);
      const T2 = await openTerminalTile(actx);
      const all = await T2.page.evaluate(async () => (await (await xbin.fetch('/api/apps/sandbox-terminal/keys?all=1')).json()).keys || []);
      const k = all.find((x) => x.id === dev1KeyId);
      check(!!k && k.inactive > 0, `…their key is kept, marked inactive, in everyone's keys (${JSON.stringify(k || null)})`);
      await T2.page.evaluate(async (id) => { await xbin.fetch(`/api/apps/sandbox-terminal/keys/${encodeURIComponent(id)}`, { method: 'DELETE' }); }, dev1KeyId);
    } finally {
      const back = await access('read');
      if (!back.ok()) log(`sandboxTerminal: restoring dev1's read access: ${back.status()}`);
      await actx.close();
    }
  }
  done();
}

module.exports = { sandboxTerminal };
