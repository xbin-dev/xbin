// hack/ui-harness/passes/journey.js — what the two journey passes share
// (journeymanual.js, journeyagent.js; plans/wsagent/journey.md): the owner's
// UX benchmark, measured. "Starting with an installed workspace that has a
// pre-provided llm, count the steps (every click) from opening the UI for
// the first time … to creating an MCP tile ('roll dice') and having it
// hooked up into an agent instance."
//
// Each pass boots an xbind of its own, the way an install runs one — no
// --dev, --isolate, owner-token login, the vault unconfigured (the
// installer's default, manual unseal), the LLM connector set by a host file
// (XBIN_LLM_CONFIG) whose one upstream is hack/fakeopenai — on a workspace
// `xbind init` just made, on its own port beside the harness's, and stops it
// when it is done. Nothing of the harness's seeded workspace is used.
//
// Every interaction goes through one counter (journey()): a navigation, a
// click, a field (one filled or one choice picked), a key. A step is counted
// once however it is driven — a DOM click where the markup allows, the
// element's testApi() otherwise (the row says which) — and a screenshot is
// kept of each. Waits are timed, not counted; what a person has to know that
// no screen says is listed beside the count ("know"); what a real coding
// agent would add and the scripted one doesn't is counted by hand ("hand").
//
// The MCP server is BUILT (D243: there is no template to stamp one from),
// by the tile's own coding agent — here the scripted ACP agent (hack/fakeacp;
// XBIN_AGENT_FAKE names a path inside the dice tile's directory, which the
// tile's sandbox sees, planted before the tile exists). Its script (BUILD,
// acptest's "fakeacp-steps:") does what the brief tells a real one to do:
// it reads /docs/mcp.md from inside its sandbox, writes backend/main.go out
// of that page, go.mod and the manifest that provides mcp, and checks
// tools/list — asking its person before each command, as a coding agent in
// an asking mode does. Every answer the person gives is counted.
'use strict';
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const { fs, OUT, log, sleep } = require('../lib');

const REPO = process.env.REPO || path.resolve(__dirname, '../../..');
const PORT = Number(process.env.JOURNEY_PORT || Number(process.env.PORT || 8697) + 3);
const FAKE = process.env.JOURNEY_FAKE || `127.0.0.1:${PORT + 1}`;
const URL = `http://127.0.0.1:${PORT}`;
const SHOTS = process.env.JOURNEY_SHOTS || path.join(OUT, 'journey-shots');
const DICE = 'apps/dice';

/** why the journey can't be driven here ('' when it can). */
function noJourney() {
  if (!process.env.HARNESS_ISOLATE) return 'no sandboxes (HARNESS_ISOLATE=1): the journey runs on an xbind as an install runs one, --isolate';
  if (!process.env.HARNESS_DIR) return 'no HARNESS_DIR: where the journey\'s own workspace is made';
  for (const f of ['bin/xbind', 'bin/fakeopenai', 'bin/fakeacp']) if (!fs.existsSync(path.join(REPO, f))) return `no ${f} (run.sh builds it; a --shots run needs it built)`;
  if (!fs.existsSync(path.join(REPO, 'bin/sealed/sealed.json'))) return 'no bin/sealed (make sealed): an xbind without --dev runs the release\'s build of the workspace agent and never compiles one';
  const rootfs = process.env.ROOTFS || process.env.XBIN_TEST_ROOTFS || path.join(REPO, '.rootfs');
  if (!fs.existsSync(path.join(rootfs, 'bin/sh'))) return `no rootfs at ${rootfs} (make rootfs, or XBIN_TEST_ROOTFS)`;
  return '';
}

const mounts = (dir) => fs.readFileSync('/proc/self/mounts', 'utf8').split('\n').map((l) => l.split(' ')[1] || '').filter((m) => m.startsWith(dir + '/'));

/**
 * boot(name, {llm, host, vault}): a fresh workspace under
 * $HARNESS_DIR/journey-<name>, the scripted model and a production-mode
 * xbind on it. → {url, ws, token, stop()}. The login URL is the one xbind
 * prints (…/login?token=…). The default is the journey's install; the
 * first-run pass (firstrun.js) varies it: llm false — no host file, no
 * model; host — more keys of the host's file (its workspaceAgent); vault —
 * the host unseals with this passphrase (XBIN_VAULT_PASSPHRASE).
 */
async function boot(name, o = {}) {
  const dir = path.join(path.resolve(process.env.HARNESS_DIR), `journey-${name}`);
  const ws = path.join(dir, 'ws');
  for (const m of mounts(ws)) spawnSync('fusermount3', ['-uz', m]); // an earlier run that was killed
  fs.rmSync(ws, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  const init = spawnSync(path.join(REPO, 'bin/xbind'), ['init', ws], { encoding: 'utf8' });
  if (init.status !== 0) throw new Error(`xbind init: ${init.stderr || init.stdout}`);
  // the host's LLM configuration: one upstream, the scripted model; a key that is none
  fs.writeFileSync(path.join(dir, 'llm.key'), 'sk-fake-JOURNEY-0000\n');
  fs.writeFileSync(path.join(dir, 'llm.jsonc'), JSON.stringify({
    upstreams: { fake: { baseURL: `http://${FAKE}/v1`, keyFile: 'llm.key', models: ['fake-chat', 'gpt-5-fake'] } },
    defaults: { model: 'fake/fake-chat' }, ...(o.host || {}),
  }, null, 1) + '\n');
  const out = (f) => fs.openSync(path.join(dir, f), 'w');
  const fake = spawn(path.join(REPO, 'bin/fakeopenai'), ['-addr', FAKE], { cwd: REPO, stdio: ['ignore', out('fakeopenai.log'), out('fakeopenai.err')] });
  if (o.plant) plant(ws); // the scripted coding agent's binary, where the dice tile will be made (a real one comes from the rootfs)
  const env = { ...process.env, XBIN_SDK_PATH: path.join(REPO, 'sdk'), XBIN_BIN: path.join(REPO, 'bin'), XBIN_LLM_CONFIG: path.join(dir, 'llm.jsonc'),
    XBIN_AGENT_FAKE: path.join(ws, DICE, '.bin', 'fakeacp') };
  if (o.llm === false) delete env.XBIN_LLM_CONFIG;
  delete env.XBIN_VAULT_PASSPHRASE; // manual unseal, the installer's default: the vault boots unconfigured
  if (o.vault) env.XBIN_VAULT_PASSPHRASE = o.vault; // …or the host unseals it
  const rootfs = process.env.ROOTFS || process.env.XBIN_TEST_ROOTFS || path.join(REPO, '.rootfs');
  const xbind = spawn(path.join(REPO, 'bin/xbind'), ['--isolate', '--rootfs', rootfs, '--workspace', ws, '--listen', `127.0.0.1:${PORT}`, '--external-url', URL],
    { cwd: REPO, env, stdio: ['ignore', out('xbind.log'), out('xbind.err')] });
  const gone = (p) => p.exitCode !== null || p.signalCode !== null;
  const stop = async () => {
    xbind.kill('SIGTERM');
    for (let i = 0; i < 80 && !gone(xbind); i++) await sleep(250); // it unmounts people's encrypted volumes on its way out
    if (!gone(xbind)) xbind.kill('SIGKILL');
    fake.kill('SIGTERM');
    for (const m of mounts(ws)) spawnSync('fusermount3', ['-uz', m]);
  };
  try {
    for (let i = 0; ; i++) {
      if (gone(xbind)) throw new Error(`the journey's xbind exited (${path.join(dir, 'xbind.err')}; is something else on ${URL}?)`);
      const r = await fetch(`${URL}/login`).catch(() => null);
      if (r && r.ok) break;
      if (i > 120) throw new Error(`the journey's xbind did not come up on ${URL}`);
      await sleep(250);
    }
  } catch (e) { await stop(); throw e; }
  log(`journey ${name}: xbind (no --dev, --isolate) on ${URL}, workspace ${ws}`);
  return { url: URL, dir, ws, token: fs.readFileSync(path.join(ws, '.xbin', 'token'), 'utf8').trim(), stop };
}

/** plant(ws): the scripted coding agent's binary inside the dice tile's directory — which need not be a tile yet (its sandbox sees its own directory). */
function plant(ws) {
  const tile = path.join(ws, DICE);
  fs.mkdirSync(path.join(tile, '.bin'), { recursive: true });
  fs.copyFileSync(path.join(REPO, 'bin/fakeacp'), path.join(tile, '.bin', 'fakeacp'));
  fs.chmodSync(path.join(tile, '.bin', 'fakeacp'), 0o755);
}
/**
 * STEPS: the scripted coding agent's work, a command a step (acptest's
 * "fakeacp-steps:"): the server out of /docs/mcp.md's marked go block —
 * read from inside the sandbox, as the brief says —, the module file, the
 * manifest that provides mcp (last: the first build has everything), and
 * the brief's own check. It asks before each (four requests).
 */
const STEPS = [
  'mkdir -p backend && curl -fsS -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/docs/mcp.md?raw=1" | awk \'/^<!-- examples\\/mcp-server\\/backend\\/main.go/{f=1;next} f&&/^```go$/{g=1;next} g&&/^```$/{exit} g\' > backend/main.go && grep -c roll_dice backend/main.go',
  'printf \'module dice\\n\\ngo 1.24\\n\\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\\n\' > go.mod && echo go.mod',
  'printf \'%s\\n\' \'{"title":"Dice","runtime":"go","expose":{"roles":{"reader":"MCP at POST /mcp"}},"provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}\' > xbin.json && echo xbin.json',
  'for i in $(seq 1 150); do curl -s -H "Authorization: Bearer $XBIN_TOKEN" -H \'Content-Type: application/json\' -d \'{"jsonrpc":"2.0","id":1,"method":"tools/list"}\' "$XBIN_URL/api/$XBIN_COMPONENT/mcp" | grep -q roll_dice && echo "tools/list names roll_dice" && exit 0; sleep 2; done; echo "no answer from /mcp"; exit 1',
];
/** BUILD: the scripted coding agent's script, for a brief or a prompt to carry on a line of its own. */
const BUILD = `fakeacp-steps: ${STEPS.join(' ||| ')}`;
/** SAY_BUILD: what a person types to the tile's coding agent by hand (the scripted one plays BUILD). */
const SAY_BUILD = `Make this tile an MCP server with one tool, roll_dice (sides, count): /docs/mcp.md has the interface and a server to start from.\n${BUILD}`;

// The agent template names a bound MCP server's tool mcp:<provider>:<tool>
// and sends it to the model as wireToolName does (its context.go): what a
// provider won't take replaced by _, and an FNV-1a hash of the name appended.
function mcpWireName(provider, tool) {
  const name = `mcp:${provider}:${tool}`;
  let h = 0x811c9dc5;
  for (const c of Buffer.from(name)) { h ^= c; h = Math.imul(h, 0x01000193) >>> 0; }
  return `${name.replace(/[^A-Za-z0-9_-]+/g, '_')}_${h.toString(16).padStart(8, '0')}`;
}
/** ASK: what the person asks the new assistant — and, for the scripted model, the call a model would make of it. */
const ASK = `Roll a d20 for me.\nws: ${mcpWireName(DICE, 'roll_dice')} {"sides":20}`;
const ROLLED = /rolled 1d20: \d+/;

/**
 * journey(name, page): the counter. nav/click/field/key(what, fn, note) run
 * fn and count one interaction; wait(what, fn) times fn; know(what) and
 * hand(n, what) add rows nothing drives. `api: true` in opts marks a step
 * driven through an element's testApi() where the DOM gave no stable hook.
 */
function journey(name, getPage) {
  fs.mkdirSync(SHOTS, { recursive: true });
  const rows = [];
  let n = 0, section = '';
  const add = (kind, what, extra = {}) => { const row = { n: ['wait', 'know', 'hand', 'dead-end'].includes(kind) ? '' : ++n, section, kind, what, ...extra }; rows.push(row); return row; };
  const counted = (kind) => async (what, fn, opts = {}) => {
    try { await fn(); } catch (e) { throw new Error(`journey ${name}, at "${what}": ${String(e.message || e).replace(/token=[0-9a-f]+/g, 'token=…').split('\n').slice(0, 4).join(' ')}`); } // (the login URL carries the owner token: never into a log)
    const row = add(kind, what, { note: opts.note || '', driven: opts.api ? 'testApi' : 'DOM' });
    const file = `${name}-${String(row.n).padStart(2, '0')}-${kind}.png`;
    await getPage().waitForTimeout(250);
    await getPage().screenshot({ path: path.join(SHOTS, file) }).catch(() => {});
    row.shot = file;
    log(`journey ${name}: ${row.n}. [${kind}] ${what}`);
  };
  const J = {
    section: (s) => { section = s; },
    nav: counted('nav'), click: counted('click'), field: counted('field'), key: counted('key'),
    wait: async (what, fn, note = '') => { const t0 = Date.now(); const v = await fn(); add('wait', what, { ms: Date.now() - t0, note }); log(`journey ${name}: waited ${Date.now() - t0} ms — ${what}`); return v; },
    know: (what) => add('know', what),
    hand: (count, what) => add('hand', what, { count }),
    deadEnd: (what) => add('dead-end', what),
    rows,
    totals: () => {
      const by = (k) => rows.filter((r) => r.kind === k).length;
      return { interactions: n, nav: by('nav'), click: by('click'), field: by('field'), key: by('key'), know: by('know'), deadEnds: by('dead-end'),
        hand: rows.filter((r) => r.kind === 'hand').reduce((a, r) => a + r.count, 0), waitMs: rows.filter((r) => r.kind === 'wait').reduce((a, r) => a + r.ms, 0) };
    },
    /** write(): out/journey-<name>.md (the step table) and .json. */
    write: () => {
      const t = J.totals();
      const cell = (s) => String(s == null ? '' : s).replace(/\|/g, '\\|').replace(/\n/g, ' ');
      const lines = [`# journey ${name}: ${t.interactions} interactions (${t.nav} navigation, ${t.click} clicks, ${t.field} fields, ${t.key} keys)`,
        `${t.hand} more counted by hand, ${t.know} things to know that no screen says, ${t.deadEnds} dead ends, ${(t.waitMs / 1000).toFixed(1)} s of measured waits (scripted model and coding agent).`, '',
        '| # | Stretch | Kind | What | Note |', '|---|---|---|---|---|'];
      for (const r of rows) {
        const kind = r.kind === 'wait' ? `wait ${(r.ms / 1000).toFixed(1)} s` : r.kind === 'hand' ? `by hand +${r.count}` : r.kind;
        lines.push(`| ${r.n} | ${cell(r.section)} | ${kind} | ${cell(r.what)} | ${cell([r.note, r.driven === 'testApi' ? 'driven through testApi()' : ''].filter(Boolean).join('; '))} |`);
      }
      fs.writeFileSync(path.join(OUT, `journey-${name}.md`), lines.join('\n') + '\n');
      fs.writeFileSync(path.join(OUT, `journey-${name}.json`), JSON.stringify({ totals: t, rows }, null, 1) + '\n');
      log(`journey ${name}: ${t.interactions} interactions (+${t.hand} by hand), ${t.know} to know, ${t.deadEnds} dead ends — out/journey-${name}.md`);
      return t;
    },
  };
  return J;
}

// ---- the first-run steps both journeys share ------------------------------------------------

/** openPage(browser): a 1400×1000 page; console errors go to the log. */
async function openPage(browser, who) {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log(`${who}: PAGE ERROR`, e.message));
  return { ctx, page };
}
/** tileFrame(page, tile): the Frame of a tile's own document. */
async function tileFrame(page, tile, timeout = 20000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const f = page.frames().find((x) => x.url().includes(`/c/${tile}/`));
    if (f) return f;
    if (Date.now() > deadline) throw new Error(`tile frame ${tile} never appeared`);
    await sleep(100);
  }
}
const shellUp = async (page) => { await page.waitForSelector('bx-shell', { timeout: 20000 }); await page.waitForFunction(() => { const t = document.querySelector('bx-shell').testApi?.(); return !!t && t.screens.length > 0; }, null, { timeout: 20000 }); };

/** firstLogin(J, page, X): the owner opens the login URL for the first time. */
async function firstLogin(J, page, X) {
  J.section('first run');
  J.know('the login URL: it is in the installer\'s output and the server log (journalctl -u xbin | grep login), nowhere in a browser');
  await J.nav('open the one-time login URL', async () => { await page.goto(`${X.url}/login?token=${X.token}`); await shellUp(page); });
}
/**
 * firstRunSheet(J, page, who, {agent}): the first-run sheet the login URL
 * lands on (D237) — the name, a password, the vault's passphrase twice
 * (it asks: the vault boots unconfigured here), and the primary button
 * (agent: "Create my account and turn on my workspace agent") or the quiet
 * one; the page reloads as the new account. → whether focus started in
 * the name.
 */
async function firstRunSheet(J, page, who, { agent = true } = {}) {
  const S = 'bx-first-run';
  await page.waitForSelector(`${S} [data-state="form"] #bxfr-name`, { timeout: 30000 });
  const focused = await page.evaluate(() => (document.activeElement && document.activeElement.id) === 'bxfr-name');
  await J.field('first-run sheet: your name', () => page.fill(`${S} #bxfr-name`, who.name), { note: `the sign-in name follows from it (${who.id})` });
  await J.field('first-run sheet: a password', () => page.fill(`${S} #bxfr-pw`, who.password));
  const vault = require('crypto').randomBytes(12).toString('base64url'); // (made per run, never logged)
  await J.field('first-run sheet: the vault\'s passphrase', () => page.fill(`${S} #bxfr-vault`, vault), { note: 'absent where the host unseals (XBIN_VAULT_PASSPHRASE)' });
  await J.field('first-run sheet: the passphrase again', () => page.fill(`${S} #bxfr-repeat`, vault));
  const button = agent ? 'first-run sheet: "Create my account and turn on my workspace agent"' : 'first-run sheet: "Create my account without it"';
  await J.click(button, async () => {
    await Promise.all([page.waitForNavigation({ timeout: 60000 }), page.click(`${S} ${agent ? '[data-primary]' : '[data-quiet]'}`)]);
    await shellUp(page);
  }, { note: 'makes the admin account, signs this browser in as it, sets up the vault' + (agent ? ', turns the workspace agent on for this person ("Just me")' : '') });
  return focused;
}
/** makeTile(J, page, name): the Tile Manager's create tab (the one it opens on) → a static tile. */
async function makeTile(J, page, name) {
  const m = await tileFrame(page, 'tiles/manager');
  await J.field(`Tile Manager: the new tile's name (${name})`, () => m.locator('input#pretty').fill(name));
  await J.click('Tile Manager: "Create"', () => m.locator('button#create').click());
  await J.wait('the tile is created', () => m.waitForSelector(`text=created apps/${name.toLowerCase()}`, { timeout: 20000 }));
}
/** makeFromTemplate(J, page, title, tile, {tabOpen}): the Tile Manager's templates tab → an instance at `tile` ('' = the template's default path); tabOpen: the tab is open already (no click). */
async function makeFromTemplate(J, page, title, tile, { tabOpen = false } = {}) {
  const m = await tileFrame(page, 'tiles/manager');
  if (!tabOpen) await J.click('Tile Manager: the "new from template" tab', () => m.locator('.tabs button[data-tab="template"]').click());
  const card = m.locator('#templates .tile', { has: m.locator('.title', { hasText: new RegExp(`^${title}$`) }) });
  await card.waitFor({ timeout: 20000 });
  if (tile) await J.field(`the ${title} template: where to create it (${tile})`, () => card.locator('input.path').fill(tile));
  await J.click(`the ${title} template: "create"`, () => card.locator('button.make').click());
  await J.wait('the instance is created', () => m.waitForSelector(`text=created ${tile || 'apps/agent'}`, { timeout: 120000 }));
}
/** openFromSidebar(J, page, tile): a click on the tile's sidebar row opens its window on the screen. */
async function openFromSidebar(J, page, tile) {
  await J.click(`sidebar: ${tile} (opens the tile)`, () => page.locator(`.item[data-path="${tile}"]`).click());
  await page.waitForSelector(`.card[data-path="${tile}"] bx-frame`, { state: 'attached', timeout: 20000 });
  await page.locator(`.card[data-path="${tile}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
}
/** askAssistant(J, page, tile): the person asks the assistant to roll; → the answer's text once the roll shows. */
async function askAssistant(J, page, tile) {
  const f = await tileFrame(page, tile);
  await J.wait('the assistant\'s page is up (its backend compiled in its sandbox)', () => f.waitForSelector('textarea#msg', { timeout: 600000 }));
  await J.field('the assistant: the message ("Roll a d20 for me.")', () => f.locator('textarea#msg').fill(ASK));
  await J.click('the assistant: "Send"', () => f.locator('button#send').click());
  return J.wait('the answer shows the roll', async () => {
    const h = await f.waitForFunction((src) => { const m = new RegExp(src).exec(document.body.innerText); return m ? m[0] : null; }, ROLLED.source, { timeout: 120000, polling: 250 });
    return h.jsonValue();
  });
}

/** servesDice(ctx, X): the dice tile answers MCP's tools/list with roll_dice (polled: xbind restarts its backend when its files change). → the last status. */
async function servesDice(ctx, X) {
  let status = 0;
  for (let i = 0; i < 120; i++) {
    const r = await ctx.request.post(`${X.url}/api/${DICE}/mcp`, { data: { jsonrpc: '2.0', id: 1, method: 'tools/list' }, failOnStatusCode: false });
    status = r.status();
    if (r.ok() && /roll_dice/.test(await r.text())) return { ok: true, status };
    await sleep(250);
  }
  return { ok: false, status };
}

module.exports = { REPO, URL, DICE, SHOTS, servesDice, BUILD, SAY_BUILD, STEPS, ASK, ROLLED, noJourney, boot, plant, journey, openPage, tileFrame, shellUp, firstLogin, firstRunSheet, makeTile, makeFromTemplate, openFromSidebar, askAssistant };
