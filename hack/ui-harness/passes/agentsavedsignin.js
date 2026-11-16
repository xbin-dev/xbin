// hack/ui-harness/passes/agentsavedsignin.js — coding-agent sign-ins held
// by xbind (D230), through <bx-agent-signin> in the Agent tab. The scripted
// "fake" agent signs in the way Claude Code does (internal/agent gives it
// claude's Signin, Mint and Keys), and run.sh puts hack/ui-harness/fakebin
// first on xbind's PATH: its `claude setup-token` is what xbind's own
// guided sign-in runs here (the harness is unisolated: a development
// workspace, which the list says). No real CLI, no real token.
//
// What this checks: (a) a signed-out agent's card offers xbind's sign-in
// first, and says where a saved sign-in is used; (b) Sign in shows the
// CLI's link — fetched by xbind, no terminal session of the person's — a
// malformed code is asked again, the right one saves the sign-in, and the
// agent restarts on it: the header says "your saved sign-in", the agent's
// own word on its account is the saved token, and what the tab shows of its
// environment is masked; (c) no answer and no row carries the token; (d)
// the list: rename, add a pasted key, make default, remove (which says
// what it does first), where it was used; (e) admins: who has sign-ins —
// no names, no secrets — and the switch, which stops saving; (f) a tile
// someone else can open a terminal on: the card says the saved sign-in
// isn't used there and offers the tile's own sign-in first, and a session
// there says who; (g) the element alone at phone width, light and dark.
const { URL, OUT, login, closeCtx, settle, fr, waitFor, openShell, usePersonalScreen, openTile, shotEl, checker } = require('../lib');

const TILE = 'apps/welcome'; // only admins open a terminal here
const SHARED = 'apps/leads'; // dev1 and sales1 (not admins) may too
const VIEW = { viewport: { width: 1400, height: 1300 } };
const FAKE_URL = 'https://claude.com/cai/oauth/authorize?code=true&client_id=harness-client-0123456789abcdef&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=harnessChallenge0123456789&code_challenge_method=S256&state=harness-state';
const CODE = 'harness-code#harness-state';
const TOKEN_MARK = 'sk-ant-oat01-harness'; // what fakebin's setup-token prints starts with
const PASTED = 'sk-ant-api03-FAKEFAKE-harness-pasted-key-0001'; // an obvious fake
const API = `${URL}/api/xbin/agent/signins`;
const CARD = `bx-frame[src="${TILE}"] bx-agent-signin:not([view])`;
const LISTBTN = `bx-frame[src="${TILE}"] bx-agent-signin.signins`;

async function agentSavedSignin(browser) {
  const { check, done } = checker('agent-saved-signin');
  const { ctx, page } = await login(browser, 'admin', 'admin', VIEW);
  const seen = []; // every answer of the sign-in routes read here
  const get = async (path) => { const r = await ctx.request.get(API + path); const t = await r.text(); seen.push(t); return { status: r.status(), body: t ? JSON.parse(t) : {} }; };
  const mine = async (harness = 'fake') => ((await get('')).body.harnesses || []).find((h) => h.id === harness)?.signins || [];
  const wipe = async () => { for (const h of (await get('')).body.harnesses || []) for (const s of h.signins) await ctx.request.delete(`${API}/${s.id}`); };
  const sessions = async (tile) => (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`)).json();
  const endAll = async (tile) => { for (const s of await sessions(tile)) await ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(s.id)}`); };
  await ctx.request.put(`${API}/policy`, { data: { enabled: true } });
  await wipe();
  await endAll(TILE);
  await endAll(SHARED);

  const first = await get('');
  check(first.status === 200 && first.body.available && first.body.unisolated === true && first.body.policy.enabled && first.body.admin === true,
    `the store answers; the harness runs without sandboxes and says so (${JSON.stringify({ available: first.body.available, unisolated: first.body.unisolated, vault: first.body.vault })})`);
  const here = (await get(`?tile=${encodeURIComponent(TILE)}`)).body.harnesses.find((h) => h.id === 'fake');
  const there = (await get(`?tile=${encodeURIComponent(SHARED)}`)).body.harnesses.find((h) => h.id === 'fake');
  check(here.guided && here.here?.inject === true, `on ${TILE} a saved sign-in would be used (${JSON.stringify(here.here)})`);
  check(there.here?.inject === false && /dev1/.test(there.here.why) && there.here.why.includes(SHARED), `on ${SHARED} it would not, and it says who (${JSON.stringify(there.here)})`);

  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));

  // ---- (a) a signed-out agent: the card offers xbind's sign-in ----
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t, tile) => { const api = t.frameFor(tile)?.testApi(); const ag = api && api.agent(api.tabs.length - 1); return !!ag && !!ag.sessionId; }, TILE, { timeout: 15000, label: 'the fake agent starts' });
  const idx = await fr(page, TILE, (f) => f.tabs.length - 1);
  await fr(page, TILE, (f, t, i) => f.agent(i).send('please fail'), idx);
  await waitFor(page, (t, a) => { const g = t.frameFor(a.tile)?.testApi().agent(a.i)?.signin; return !!g && g.data && g.primary === 'saved'; }, { tile: TILE, i: idx }, { timeout: 15000, label: 'the signed-out agent shows the card, with xbind\'s sign-in first' });
  const card = () => fr(page, TILE, (f, t, i) => { const s = f.agent(i).signin; return s && { phase: s.phase, how: s.how, url: s.url, code: s.code, status: s.status, hint: s.hint, primary: s.primary, session: s.session, saved: s.saved }; }, idx);
  let s = await card();
  check(s.phase === 'idle' && /Not signed in to Fake agent/.test(s.status) && /keeps the sign-in sealed/.test(s.hint) && /every tile only you can open a terminal on/.test(s.hint) && /without sandboxes/.test(s.hint),
    `the card says what Sign in does, where a saved sign-in is used, and that this workspace runs without sandboxes (${JSON.stringify(s)})`);
  const links = await page.locator(`${CARD} .links button`).allTextContents();
  check(['Paste a key instead', 'Sign in on this tile only', 'Use a terminal instead', 'Saved sign-ins…'].every((l) => links.includes(l)), `…and the other ways beside it (${JSON.stringify(links)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'signin-card-idle');

  // ---- (b) Sign in: the link, a malformed code, the right one ----
  const before = (await sessions(TILE)).length;
  await page.locator(`${CARD} button.start`).click();
  await waitFor(page, (t, a) => { const g = t.frameFor(a.tile)?.testApi().agent(a.i)?.signin; return !!g && g.phase === 'waiting' && g.code; }, { tile: TILE, i: idx }, { timeout: 20000, label: 'xbind shows the CLI\'s link and asks for the code' });
  s = await card();
  const href = await page.locator(`${CARD} a.open`).getAttribute('href');
  check(s.how === 'saved' && s.url === FAKE_URL && href === FAKE_URL, `the card offers the whole sign-in link, read by xbind (${s.url === FAKE_URL}, ${href === FAKE_URL})`);
  check((await sessions(TILE)).length === before && !s.session, `…with no terminal session of the person's: xbind runs the CLI itself (${(await sessions(TILE)).length} sessions, as before)`);
  const focused = await page.locator(`${CARD}`).evaluate((el) => el.shadowRoot.activeElement?.className || '');
  check(focused === 'code', `the code field has the focus (${focused})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'signin-card-link');
  await page.locator(`${CARD} input.code`).fill('garbage');
  await page.keyboard.press('Enter');
  await waitFor(page, (t, a) => { const g = t.frameFor(a.tile)?.testApi().agent(a.i)?.signin; return !!g && g.phase === 'waiting' && /not the whole code/.test(g.status); }, { tile: TILE, i: idx }, { timeout: 20000, label: 'a code without # is refused and asked again' });
  check(true, 'a malformed code is refused, and the card asks for it again');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'signin-card-invalid');
  await page.locator(`${CARD} input.code`).fill(`  ${CODE} `);
  await page.keyboard.press('Enter');
  // saved → the agent restarts on it: a new session, whose header says so
  await waitFor(page, (t, a) => { const ag = t.frameFor(a.tile)?.testApi().agent(a.i); return !!ag && !ag.signin && /your saved sign-in: Fake agent/.test(ag.signinChip || ''); }, { tile: TILE, i: idx }, { timeout: 30000, label: 'saved; the agent restarts and its header names the saved sign-in' });
  const after = await fr(page, TILE, (f, t, i) => ({ chip: f.agent(i).signinChip, state: f.agent(i).signinState, id: f.agent(i).sessionId }), idx);
  const firstEvs = await (await ctx.request.get(`${URL}/api/xbin/term/sessions/${encodeURIComponent(after.id)}/events`)).text();
  check(after.state?.source === 'saved' && /Using your saved sign-in Fake agent \(tests\)\./.test(firstEvs), `the tab's header: "${after.chip}"; the restarted session's first lines say it too`);
  const saved = await mine();
  check(saved.length === 1 && saved[0].kind === 'setup-token' && saved[0].default && saved[0].env === 'CLAUDE_CODE_OAUTH_TOKEN' && saved[0].expiresAt > Date.now() + 300 * 86400000 && saved[0].lastUsedAt > 0,
    `xbind keeps it: named, the default, a year's token, used by the restarted session (${JSON.stringify(saved)})`);
  const row = (await sessions(TILE)).find((r) => r.id === after.id);
  check(!!row && row.signin?.source === 'saved' && row.signin.name === saved[0].name, `the session's row says what it signs in with (${JSON.stringify(row?.signin)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'signin-agent-saved');

  // the agent's own word on its account, and its environment as the tab keeps it
  const say = async (text, until) => {
    await fr(page, TILE, (f, t, a) => f.agent(a.i).send(a.text), { i: idx, text });
    let evs = '';
    for (let k = 0; k < 200; k++) {
      evs = await (await ctx.request.get(`${URL}/api/xbin/term/sessions/${encodeURIComponent(after.id)}/events`)).text();
      if (until.test(evs)) break;
      await page.waitForTimeout(100);
    }
    return evs;
  };
  let evs = await say('whoami', /account: token/);
  check(/account: token …harn/.test(evs), 'the agent signs in with the saved token (its own word on its account)');
  evs = await say('printenv CLAUDE_CODE_OAUTH_TOKEN', /printenv: /);
  check(/printenv: \[saved sign-in\]/.test(evs) && !evs.includes(TOKEN_MARK), 'what the tab keeps of the agent\'s environment is masked');

  // ---- (c) nothing answered the token ----
  const uses = (await get('/uses')).body.uses || [];
  check(uses.length >= 1 && uses[0].tile === TILE && uses[0].origin === 'person', `the use is logged for its person (${JSON.stringify(uses[0])})`);

  // ---- (d) the list ----
  await page.locator(LISTBTN).evaluate((el) => el.testApi().openList());
  await page.locator(`${LISTBTN} .dialog section[data-harness="fake"] .item`).first().waitFor({ timeout: 10000 });
  const DLG = `${LISTBTN} .dialog`;
  const names = () => page.locator(`${DLG} section[data-harness="fake"] .item .name`).allTextContents();
  check((await names())[0]?.startsWith('Fake agent (tests)') && (await page.locator(`${DLG} .banner`).first().textContent()).includes('without sandboxes'),
    `the list shows the sign-in, and that this workspace runs without sandboxes (${JSON.stringify(await names())})`);
  const sections = await page.locator(`${DLG} section[data-harness] h4`).allTextContents();
  check(['Claude Code', 'Codex', 'Gemini CLI', 'OpenCode'].every((n) => sections.includes(n)), `every coding agent that takes a saved credential has its section (${JSON.stringify(sections)})`);
  await shotEl(page, DLG, 'signin-list');
  // rename, by keyboard
  await page.locator(`${DLG} section[data-harness="fake"] button.rename-it`).first().click();
  await page.locator(`${DLG} input.rename`).fill('Personal');
  await page.keyboard.press('Enter');
  await page.locator(`${DLG} section[data-harness="fake"] .item .name`, { hasText: 'Personal' }).waitFor({ timeout: 10000 });
  check((await mine())[0].name === 'Personal', 'Rename renames it');
  // add a pasted key
  await page.locator(`${DLG} section[data-harness="fake"] button.add`).click();
  const ADD = `${DLG} section[data-harness="fake"] bx-agent-signin.adding`;
  await page.locator(`${ADD} button.paste-instead`).click();
  await shotEl(page, DLG, 'signin-list-paste');
  await page.locator(`${ADD} input.secret`).fill(PASTED);
  await page.locator(`${ADD} input.name`).fill('Work');
  await page.keyboard.press('Enter');
  await page.locator(`${DLG} section[data-harness="fake"] .item .name`, { hasText: 'Work' }).waitFor({ timeout: 10000 });
  let two = await mine();
  const work = two.find((x) => x.name === 'Work'), personal = two.find((x) => x.name === 'Personal');
  check(two.length === 2 && work?.kind === 'api-key' && !work.default && personal.default, `a pasted key is saved beside it, not the default (${JSON.stringify(two.map((x) => [x.name, x.kind, x.default]))})`);
  // a key that isn't one line of printable characters is refused, with what to do
  await page.locator(`${DLG} section[data-harness="fake"] button.add`).click();
  await page.locator(`${ADD} button.paste-instead`).click();
  await page.locator(`${ADD} input.secret`).fill('two words');
  await page.keyboard.press('Enter');
  await page.locator(`${ADD} .status.bad`).waitFor({ timeout: 10000 });
  const refusal = await page.locator(`${ADD} .status.bad`).textContent();
  check(/one line/.test(refusal) && /paste the key or token alone/.test(refusal), `an unfit key is refused, and the card says what to paste (${refusal.trim()})`);
  await page.keyboard.press('Escape');
  await page.locator(`${DLG} button.link`, { hasText: 'Done adding' }).click();
  // make default
  await page.locator(`${DLG} .item[data-id="${work.id}"] button.make-default`).click();
  await page.locator(`${DLG} .item[data-id="${work.id}"] .badge`).waitFor({ timeout: 10000 });
  two = await mine();
  check(two.find((x) => x.name === 'Work').default && !two.find((x) => x.name === 'Personal').default, 'Make default moves the default');
  // remove: it says what that does first
  await page.locator(`${DLG} .item[data-id="${work.id}"] button.remove-it`).click();
  const warn = await page.locator(`${DLG} .confirm`).textContent();
  check(/Coding agents running on it are ended/.test(warn) && /stays valid/.test(warn) && /revoke it there/.test(warn), `Remove says what it does and doesn't do (${warn.replace(/\s+/g, ' ').trim().slice(0, 160)})`);
  await shotEl(page, DLG, 'signin-list-remove');
  await page.locator(`${DLG} .confirm button.remove-yes`).click();
  await page.locator(`${DLG} .item[data-id="${work.id}"]`).waitFor({ state: 'detached', timeout: 10000 });
  two = await mine();
  check(two.length === 1 && two[0].name === 'Personal' && two[0].default, `removing the default hands it to the one left (${JSON.stringify(two.map((x) => [x.name, x.default]))})`);
  // where they were used
  await page.locator(`${DLG} button.show-uses`).click();
  await page.locator(`${DLG} section.uses table`).waitFor({ timeout: 10000 });
  const usesText = await page.locator(`${DLG} section.uses table`).textContent();
  check(usesText.includes(TILE) && usesText.includes('you'), 'the list shows where a sign-in was used');

  // ---- (e) admins: that, not what; the switch ----
  await page.locator(`${DLG} section.admin table.people tr[data-user="admin"]`).first().waitFor({ timeout: 10000 });
  const people = await page.locator(`${DLG} section.admin table.people`).textContent();
  const peopleApi = await get('/people');
  check(/admin/.test(people) && !people.includes('Personal') && !JSON.stringify(peopleApi.body).includes('Personal'), 'admins see who has sign-ins, of which kind and when — not the person\'s name for one');
  await shotEl(page, DLG, 'signin-list-admin');
  await page.locator(`${DLG} input.policy`).click();
  await page.locator(`${DLG} .banner.why-not`).waitFor({ timeout: 10000 });
  const off = await page.locator(`${DLG} .banner.why-not`).textContent();
  const refused = await ctx.request.post(API, { data: { harness: 'fake', secret: PASTED } });
  check(/switched off/.test(off) && refused.status() === 403 && (await page.locator(`${DLG} button.add`).count()) === 0, `switched off: nothing can be added, and the list says why (${off.trim()}; a paste answers ${refused.status()})`);
  await shotEl(page, DLG, 'signin-list-off');
  await page.locator(`${DLG} input.policy`).click();
  await page.locator(`${DLG} .banner.why-not`).waitFor({ state: 'detached', timeout: 10000 });
  await page.keyboard.press('Escape');
  await page.locator(DLG).waitFor({ state: 'detached', timeout: 10000 }).catch(() => {});
  check((await page.locator(DLG).count()) === 0, 'Escape closes the list');

  // ---- (f) a tile someone else can open a terminal on ----
  await fr(page, TILE, (f) => { while (f.tabs.length) f.closeTab(0); });
  await settle(page);
  await openTile(page, SHARED);
  await page.locator(`.card[data-path="${SHARED}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, SHARED, (f) => f.open('term'));
  await fr(page, SHARED, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t, tile) => { const api = t.frameFor(tile)?.testApi(); const ag = api && api.agent(api.tabs.length - 1); return !!ag && !!ag.sessionId && !!ag.signinState; }, SHARED, { timeout: 20000, label: 'the agent on the shared tile says what it signs in with' });
  const sidx = await fr(page, SHARED, (f) => f.tabs.length - 1);
  const shared = await fr(page, SHARED, (f, t, i) => ({ chip: f.agent(i).signinChip, state: f.agent(i).signinState }), sidx);
  // (what it runs on instead depends on the home: the tile's own login where an earlier pass signed the fake in, else nothing)
  check(shared.state.source !== 'saved' && /dev1.* can also open terminals on apps\/leads/.test(shared.state.why) && /saved sign-in not used|this tile's own login/.test(shared.chip),
    `on ${SHARED} the saved sign-in is left out, and the session says who can also open a terminal there (${JSON.stringify(shared)})`);
  await fr(page, SHARED, (f, t, i) => f.agent(i).send('please fail'), sidx);
  await waitFor(page, (t, a) => { const g = t.frameFor(a.tile)?.testApi().agent(a.i)?.signin; return !!g && g.data && g.primary === 'tile'; }, { tile: SHARED, i: sidx }, { timeout: 15000, label: 'the shared tile\'s card offers the tile\'s own sign-in first' });
  const scard = await fr(page, SHARED, (f, t, i) => { const g = f.agent(i).signin; return { hint: g.hint, primary: g.primary }; }, sidx);
  const sbtn = await page.locator(`bx-frame[src="${SHARED}"] bx-agent-signin:not([view]) button.start`).textContent();
  check(/isn't used on this tile: dev1.* can also open terminals/.test(scard.hint) && sbtn.trim() === 'Sign in on this tile', `…and its card says so and signs in on the tile ("${sbtn.trim()}": ${scard.hint})`);
  await shotEl(page, `bx-frame[src="${SHARED}"] .pop`, 'signin-card-shared-tile');
  await fr(page, SHARED, (f) => { while (f.tabs.length) f.closeTab(0); });
  await settle(page);

  // ---- nothing read here carried the token ----
  const all = seen.join('\n');
  check(!all.includes(TOKEN_MARK) && !all.includes(PASTED), 'no answer of the sign-in routes carried a secret');
  await closeCtx(ctx, page);

  // ---- (g) the element alone, at phone width, light and dark ----
  for (const scheme of ['dark', 'light']) {
    const pc = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, colorScheme: scheme });
    const pp = await pc.newPage();
    await pp.goto(`${URL}/login`);
    await pp.fill('input[name=username]', 'admin');
    await pp.fill('input[name=password]', 'admin');
    await Promise.all([pp.waitForNavigation(), pp.click('button')]);
    await pp.goto(`${URL}/xbin/system`); // an xbind-served page: no import map, the person's theme
    const mount = (attrs) => pp.evaluate(async (a) => {
      await import('/vendor/agent-signin.js');
      const el = document.createElement('bx-agent-signin');
      for (const [k, v] of Object.entries(a)) el.setAttribute(k, v);
      const box = document.createElement('div');
      box.id = 'probe';
      box.style.cssText = 'padding: 12px; background: var(--bx-bg, #0B0C12); min-height: 100vh; box-sizing: border-box';
      box.append(el);
      document.body.replaceChildren(box);
      for (let n = 0; n < 200 && !el.testApi().data; n++) await new Promise((r) => setTimeout(r, 25));
      await el.updateComplete;
      return { width: document.documentElement.scrollWidth, view: window.innerWidth };
    }, attrs);
    let m = await mount({ view: 'manage' });
    check(m.width <= m.view, `${scheme}, 390 px: the list fits the width (${m.width} ≤ ${m.view})`);
    await pp.screenshot({ path: `${OUT}/signin-phone-list-${scheme}.png`, fullPage: true });
    m = await mount({ harness: 'fake', provider: 'Fake agent (tests)', component: TILE });
    check(m.width <= m.view, `${scheme}, 390 px: the card fits the width (${m.width} ≤ ${m.view})`);
    await pp.screenshot({ path: `${OUT}/signin-phone-card-${scheme}.png` });
    await pp.locator('bx-agent-signin button.start').click();
    await pp.locator('bx-agent-signin input.code:not([disabled])').waitFor({ timeout: 20000 });
    m = await pp.evaluate(() => ({ width: document.documentElement.scrollWidth, view: window.innerWidth }));
    check(m.width <= m.view, `${scheme}, 390 px: the link and the code field fit (${m.width} ≤ ${m.view})`);
    await pp.screenshot({ path: `${OUT}/signin-phone-link-${scheme}.png` });
    await pp.keyboard.press('Escape'); // Escape in the code field cancels
    await pp.locator('bx-agent-signin button.start').waitFor({ timeout: 10000 });
    check(true, `${scheme}: Escape cancels the sign-in`);
    await pp.locator('bx-agent-signin button.paste-instead').click();
    await pp.screenshot({ path: `${OUT}/signin-phone-paste-${scheme}.png` });
    await pc.close();
  }
  // desktop, the other theme: the card and the list
  {
    const other = process.env.HARNESS_THEME === 'light' ? 'dark' : 'light';
    const { ctx: c2, page: p2 } = await login(browser, 'admin', 'admin', { ...VIEW, colorScheme: other });
    await p2.goto(`${URL}/xbin/system`);
    await p2.evaluate(async (tile) => {
      await import('/vendor/agent-signin.js');
      const box = document.createElement('div');
      box.style.cssText = 'padding: 24px; max-width: 760px; display: flex; flex-direction: column; gap: 16px';
      for (const a of [{ harness: 'fake', provider: 'Fake agent (tests)', component: tile }, { view: 'manage' }]) {
        const el = document.createElement('bx-agent-signin');
        for (const [k, v] of Object.entries(a)) el.setAttribute(k, v);
        box.append(el);
      }
      document.body.replaceChildren(box);
      for (const el of box.children) { for (let n = 0; n < 200 && !el.testApi().data; n++) await new Promise((r) => setTimeout(r, 25)); await el.updateComplete; }
    }, TILE);
    await p2.screenshot({ path: `${OUT}/signin-desktop-${other}.png`, fullPage: true });
    await closeCtx(c2, p2);
  }

  // leave nothing behind
  const { ctx: c3, page: p3 } = await login(browser, 'admin', 'admin', VIEW);
  for (const h of (await (await c3.request.get(API)).json()).harnesses || []) for (const sg of h.signins) await c3.request.delete(`${API}/${sg.id}`);
  for (const tile of [TILE, SHARED]) for (const r of await (await c3.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(tile)}`)).json()) await c3.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(r.id)}`);
  await closeCtx(c3, p3);
  done();
}

module.exports = { agentSavedSignin };
