// hack/ui-harness/passes/agentsignin.js — the Agent tab's guided sign-in and
// whole links in a terminal (D178). The scripted "fake" agent signs in the
// way Claude Code does (internal/agent gives it claude's Signin), and run.sh
// puts hack/ui-harness/fakebin first on the shells' PATH: its `claude auth
// login` draws the URL as Ink does (broken over 60-column rows, each an OSC
// 8 link to the whole URL) and takes a code. What this checks: (a) a
// signed-out agent shows the strip, and Sign in runs the CLI in a terminal
// session that is no tab (named xbin:sign-in in the listing); (b) the strip
// offers the whole URL — the link and the field — rejoined from the rows;
// (c) a code without '#' is refused and asked again; Finish sends the right
// one, the strip reports signed in and goes, and its session ends with the
// CLI; (d) in a plain shell, a URL a program broke over rows at the
// terminal's width is one link from each row; (e) a click on a later row of
// an OSC 8 link opens the whole URL in a new tab, with no confirm(); (f) an
// OSC 8 link that shows one URL and points at another asks first, naming its
// real target (the security review of D178).
const { URL, login, closeCtx, settle, fr, waitFor, waitSel, openShell, usePersonalScreen, openTile, shotEl, checker } = require('../lib');

const TILE = 'apps/crawler';
const VIEW = { viewport: { width: 1400, height: 1300 } };
// fakebin/claude's URL
const FAKE_URL = 'https://claude.com/cai/oauth/authorize?code=true&client_id=harness-client-0123456789abcdef&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=harnessChallenge0123456789&code_challenge_method=S256&state=harness-state';
const TERM = `bx-frame[src="${TILE}"] bx-terminal`;
// (f): an OSC 8 link whose text is a trusted URL and whose target isn't
const DECEPTIVE = { shows: 'https://login.xbin.dev/ok', to: 'https://evil.example/steal' };
const DECEPTIVE_TO_LINE = (to) => `It opens: ${to}`;

async function agentSignin(browser) {
  const { check, done } = checker('agent-signin');
  const { ctx, page } = await login(browser, 'admin', 'admin', VIEW);
  const listed = async () => (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json();
  for (const s of await listed()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));

  // ---- (a) a fake agent whose turn fails signed out ----
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t) => { const api = t.frameFor('apps/crawler')?.testApi(); const ag = api && api.agent(api.tabs.length - 1); return !!ag && !!ag.sessionId; }, null, { timeout: 15000, label: 'the fake agent starts' });
  const idx = await fr(page, TILE, (f) => f.tabs.length - 1);
  await fr(page, TILE, (f, t, i) => f.agent(i).send('please fail'), idx);
  await waitFor(page, (t, i) => !!t.frameFor('apps/crawler')?.testApi().agent(i)?.signin, idx, { timeout: 15000, label: 'the signed-out agent shows the guided sign-in' });
  const strip = () => fr(page, TILE, (f, t, i) => { const s = f.agent(i).signin; return s && { phase: s.phase, url: s.url, code: s.code, status: s.status, session: s.session }; }, idx);
  let s = await strip();
  check(s.phase === 'idle' && /Not signed in to Fake agent/.test(s.status), `a signed-out agent shows the sign-in strip, not a terminal (${JSON.stringify(s)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-signin-idle');
  const nTabs = await fr(page, TILE, (f) => f.tabs.length);
  await fr(page, TILE, (f, t, i) => f.agent(i).signin.start(), idx);
  await waitFor(page, (t, i) => { const g = t.frameFor('apps/crawler')?.testApi().agent(i)?.signin; return !!g && g.phase === 'waiting' && g.code; }, idx, { timeout: 15000, label: 'the strip offers the link and asks for the code' });
  s = await strip();

  // ---- (b) the whole URL, rejoined from the rows ----
  check(s.url === FAKE_URL, `the strip offers the whole URL, joined from its 60-column rows (${s.url})`);
  const href = await page.locator(`bx-frame[src="${TILE}"] bx-agent-signin a.open`).getAttribute('href');
  const field = await page.locator(`bx-frame[src="${TILE}"] bx-agent-signin input.url`).inputValue();
  check(href === FAKE_URL && field === FAKE_URL, `"Open sign-in page" and the link field carry it (${href === FAKE_URL}, ${field === FAKE_URL})`);
  const rows = await listed();
  const row = rows.find((r) => r.id === s.session);
  check(!!row && row.name === 'xbin:sign-in', `the CLI runs in a terminal session named for the sign-in (${JSON.stringify(row)})`);
  const tabs = await fr(page, TILE, (f) => f.tabs);
  check(tabs.length === nTabs && !tabs.some((x) => x.id === s.session), `…which is no tab (${tabs.length} tabs, as before)`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-signin-waiting');

  // ---- (c) a malformed code, then the right one ----
  await fr(page, TILE, (f, t, i) => f.agent(i).signin.finish('garbage'), idx);
  await waitFor(page, (t, i) => { const g = t.frameFor('apps/crawler')?.testApi().agent(i)?.signin; return !!g && g.phase === 'waiting' && /not the whole code/.test(g.status); }, idx, { timeout: 15000, label: 'a code without # is refused and asked again' });
  check(true, 'a malformed code is refused, and the strip asks for it again');
  await fr(page, TILE, (f, t, i) => f.agent(i).signin.finish('harness-code#harness-state'), idx);
  await waitFor(page, (t, i) => { const a = t.frameFor('apps/crawler')?.testApi().agent(i); return !!a && !a.signin && /^Signed in to Fake agent/.test(a.signedNote); }, idx, { timeout: 15000, label: 'Finish signs in; the strip goes' });
  const after = await fr(page, TILE, (f, t, i) => ({ login: f.agent(i).login, note: f.agent(i).signedNote }), idx);
  check(after.login === null && /Send your message again/.test(after.note), `signed in: the prompt is gone, the tab says to send again (${JSON.stringify(after)})`);
  let gone = false;
  for (let k = 0; k < 200 && !gone; k++) {
    gone = !(await listed()).some((r) => r.id === s.session);
    if (!gone) await page.waitForTimeout(50);
  }
  check(gone, 'the sign-in\'s session ended with the CLI');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'agent-signin-done');

  // ---- (d) a plain shell: a URL broken at the terminal's width is one link ----
  await fr(page, TILE, (f) => f.startKind('shell', null, { run: 'FAKE_CLAUDE_PLAIN=1 claude auth login --claudeai' }));
  await waitSel(page, `${TERM} textarea`, { timeout: 20000 });
  // read once the CLI asks for the code: every row of the URL is out by then
  const plain = await page.locator(TERM).last().evaluate(async (el) => {
    const t = el.testApi();
    for (let n = 0; n < 400; n++) {
      const size = t.size, lines = [];
      for (let row = 0; size && row < size.rows; row++) lines.push(t.screenLine(row));
      const row = lines.findIndex((l) => l.startsWith('https://claude.com/cai/oauth/authorize') && l.length === size.cols);
      if (row >= 0 && lines.some((l) => l.includes('Paste code here if prompted'))) {
        const first = t.links(row), next = t.links(row + 1);
        return { cols: size.cols, first: first.map((l) => l.text), next: next.map((l) => l.text), handler: t.linkHandler, clip: typeof window.ClipboardAddon };
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    return null;
  });
  check(!!plain && plain.first.length === 1 && plain.first[0] === FAKE_URL && plain.next[0] === FAKE_URL,
    `a URL broken over ${plain?.cols}-column rows is one link from its first row and the next (${JSON.stringify(plain)})`);
  check(!!plain && plain.handler && plain.clip === 'undefined', `the terminal has its OSC 8 handler, and OSC 52 is its own (no clipboard addon that answers reads) (${plain?.handler}, ${plain?.clip})`);
  await fr(page, TILE, (f) => f.closeTab(f.tabs.length - 1));
  await settle(page);

  // ---- (e) a click on a later row of an OSC 8 link opens the whole URL ----
  await page.evaluate(() => { window.__opened = []; window.__confirmed = 0; window.open = (u) => { window.__opened.push(String(u)); return null; }; window.confirm = () => { window.__confirmed++; return false; }; });
  await fr(page, TILE, (f) => f.startKind('shell', null, { run: 'claude auth login --claudeai' }));
  await waitSel(page, `${TERM} textarea`, { timeout: 20000 });
  const at = await page.locator(TERM).last().evaluate(async (el, frag) => {
    const t = el.testApi();
    for (let n = 0; n < 400; n++) {
      const lines = [];
      for (let row = 0; t.size && row < t.size.rows; row++) lines.push(t.screenLine(row));
      // once the CLI asks for the code: nothing scrolls the rows after that
      if (lines.some((l) => l.includes('Paste code here if prompted')) && lines.includes(frag)) return t.cellPoint(lines.indexOf(frag), 5);
      await new Promise((r) => setTimeout(r, 50));
    }
    return null;
  }, FAKE_URL.slice(60, 120));
  check(!!at, `the OSC 8 link's second row is on screen (${JSON.stringify(at)})`);
  if (at) {
    await page.mouse.move(at.x, at.y);
    const hovered = await page.locator(TERM).last().evaluate(async (el) => {
      for (let n = 0; n < 200; n++) {
        if (el.shadowRoot.querySelector('.xterm-cursor-pointer')) return true;
        await new Promise((r) => setTimeout(r, 25));
      }
      return false;
    });
    check(hovered, 'hovering the row shows it as a link');
    await page.mouse.down();
    await page.mouse.up();
    let opened = [];
    for (let k = 0; k < 100 && !opened.length; k++) {
      opened = await page.evaluate(() => window.__opened);
      if (!opened.length) await page.waitForTimeout(25);
    }
    const confirmed = await page.evaluate(() => window.__confirmed);
    check(opened.length === 1 && opened[0] === FAKE_URL && confirmed === 0, `a click on its second row opens the whole URL, no confirm (${JSON.stringify(opened)}, confirm ×${confirmed})`);
  }
  await fr(page, TILE, (f) => { while (f.tabs.length) f.closeTab(0); });
  await settle(page);

  // ---- (f) an OSC 8 link that shows one URL and goes to another asks first ----
  await page.evaluate(() => { window.__opened = []; window.__asked = []; window.confirm = (m) => { window.__asked.push(String(m)); return false; }; });
  const printLink = `printf '\\033]8;;${DECEPTIVE.to}\\007${DECEPTIVE.shows}\\033]8;;\\007\\n'`;
  await fr(page, TILE, (f, t, run) => f.startKind('shell', null, { run }), printLink);
  await waitSel(page, `${TERM} textarea`, { timeout: 20000 });
  const dat = await page.locator(TERM).last().evaluate(async (el, shows) => {
    const t = el.testApi();
    for (let n = 0; n < 400; n++) {
      const lines = [];
      for (let row = 0; t.size && row < t.size.rows; row++) lines.push(t.screenLine(row));
      const row = lines.findIndex((l) => l.trim() === shows); // the link's own row (not the command echoed)
      if (row >= 0) return t.cellPoint(row, 3);
      await new Promise((r) => setTimeout(r, 50));
    }
    return null;
  }, DECEPTIVE.shows);
  check(!!dat, `the deceptive link is on screen (${JSON.stringify(dat)})`);
  if (dat) {
    await page.mouse.move(dat.x, dat.y);
    await page.locator(TERM).last().evaluate(async (el) => {
      for (let n = 0; n < 200 && !el.shadowRoot.querySelector('.xterm-cursor-pointer'); n++) await new Promise((r) => setTimeout(r, 25));
    });
    await page.mouse.down();
    await page.mouse.up();
    let asked = [];
    for (let k = 0; k < 100 && !asked.length; k++) {
      asked = await page.evaluate(() => window.__asked);
      if (!asked.length) await page.waitForTimeout(25);
    }
    const opened = await page.evaluate(() => window.__opened);
    check(asked.length === 1 && asked[0].includes('goes to evil.example') && asked[0].includes(DECEPTIVE_TO_LINE(DECEPTIVE.to)) && !opened.length,
      `a link that shows ${DECEPTIVE.shows} but goes to ${DECEPTIVE.to} names its real target first, and a No opens nothing (${JSON.stringify(asked)}, opened ${JSON.stringify(opened)})`);
  }
  await fr(page, TILE, (f) => { while (f.tabs.length) f.closeTab(0); });
  await settle(page);
  for (const r of await listed()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(r.id)}`);
  await closeCtx(ctx, page);
  done();
}

module.exports = { agentSignin };
