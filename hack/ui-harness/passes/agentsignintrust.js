// hack/ui-harness/passes/agentsignintrust.js — "I trust this tile's source"
// (D230, the owner's rule of 2026-11-17), through <bx-agent-signin
// view="trust"> in the Agent tab. A person's saved sign-in is used by
// default on a tile that is theirs alone; on a tile whose source isn't only
// theirs — here apps/leads: the Sales org's, which dev1 and sales1 can
// change — it waits for their answer. The scripted "fake" agent signs in
// the way Claude Code does; the sign-in is an obvious fake, pasted through
// the API. No real CLI, no real token.
//
// What this checks: (a) the session starts WITHOUT the sign-in and says
// why; the card asks — whose tile it is, who can change it, two sentences
// of risk, and two choices of the same size and weight, neither focused or
// pre-selected; (b) "Trust this tile" answers only a person's own click:
// not Enter, not a scripted click, not before it is armed; (c) "Start
// without my sign-in" trusts nothing, the card goes and the header offers
// it again; (d) trusting restarts the session on the sign-in and the tile
// is listed under "Tiles you trust"; (e) "Stop trusting" ends the session
// that ran on it; (f) the workspace's switch: off, nothing is asked and
// nothing is used, and the header says who can change that; (g) the card
// alone — desktop and phone width, dark and light — fits and is short
// enough to read in a few seconds.
const { URL, OUT, login, closeCtx, settle, fr, waitFor, openShell, usePersonalScreen, openTile, shotEl, checker } = require('../lib');

const TILE = 'apps/leads'; // the Sales org's; dev1 and sales1 (not admins) can change it
const VIEW = { viewport: { width: 1400, height: 1300 } };
const TOKEN = 'sk-ant-oat01-FAKEFAKE-harness-trust-token-0001'; // an obvious fake
const API = `${URL}/api/xbin/agent/signins`;
const CARD = `bx-frame[src="${TILE}"] bx-agent-signin[view="trust"]`;
const DECLINED = `xbin-signin-declined:${TILE}`;

async function agentSigninTrust(browser) {
  const { check, done } = checker('agent-signin-trust');
  const { ctx, page } = await login(browser, 'admin', 'admin', VIEW);
  const get = async (path = '') => (await ctx.request.get(API + path)).json();
  const sessions = async () => (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`)).json();
  const endAll = async () => { for (const s of await sessions()) await ctx.request.delete(`${URL}/api/xbin/term/sessions/${encodeURIComponent(s.id)}`); };
  const clean = async () => {
    await ctx.request.put(`${API}/policy`, { data: { enabled: true, trust: true } });
    const v = await get();
    for (const t of v.trusted || []) await ctx.request.delete(`${API}/trust/${t.id}`);
    for (const h of v.harnesses || []) for (const s of h.signins) await ctx.request.delete(`${API}/${s.id}`);
    await endAll();
  };
  await clean();
  const saved = await ctx.request.post(API, { data: { harness: 'fake', secret: TOKEN, name: 'Personal' } });
  check(saved.status() === 201, `a sign-in is saved for the fake agent (${saved.status()})`);
  const here = (await get(`?tile=${encodeURIComponent(TILE)}`)).harnesses.find((h) => h.id === 'fake').here;
  check(here.inject === false && here.trust && here.trust.ownerKind === 'org' && here.trust.allowed === true && (here.trust.editors || []).includes('dev1') && ['own', 'connector', 'none'].includes(here.without),
    `xbind's question for ${TILE}: an org's tile that others can change (${JSON.stringify(here)})`);

  await openShell(page);
  await usePersonalScreen(page);
  await page.evaluate((k) => localStorage.removeItem(k), DECLINED);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => f.startKind('agent', 'fake'));
  await waitFor(page, (t, tile) => { const api = t.frameFor(tile)?.testApi(); const ag = api && api.agent(api.tabs.length - 1); return !!ag && !!ag.sessionId && !!ag.signinState && !!ag.trust?.ask; }, TILE,
    { timeout: 30000, label: 'the agent starts without the sign-in and the trust card asks' });
  const idx = await fr(page, TILE, (f) => f.tabs.length - 1);
  const agent = () => fr(page, TILE, (f, t, i) => { const a = f.agent(i); return { id: a.sessionId, state: a.signinState, chip: a.signinChip, asks: !!a.trust?.ask, again: a.trustAgain, note: a.signedNote }; }, idx);

  // ---- (a) the session runs without, and the card asks ----
  let a = await agent();
  const first = a.id;
  check(a.state.source !== 'saved' && !!a.state.trust && /isn't only yours — it belongs to the org/.test(a.state.why) && /waits for your trust/.test(a.chip),
    `the session started without the saved sign-in and says why ("${a.chip}": ${a.state.why})`);
  const words = await page.locator(CARD).evaluate((el) => {
    const q = (s) => el.shadowRoot.querySelector(s), box = (s) => q(s).getBoundingClientRect(), css = (s) => getComputedStyle(q(s));
    const yes = box('.trust-yes'), no = box('.trust-no');
    return { title: q('.msg b').textContent, whose: q('[data-whose]').textContent, risk: q('[data-risk]').textContent, then: q('[data-then]').textContent,
      yes: q('.trust-yes').textContent.trim(), no: q('.trust-no').textContent.trim(),
      sameSize: Math.abs(yes.width - no.width) <= 1 && Math.abs(yes.height - no.height) <= 1,
      sameLook: ['backgroundColor', 'color', 'borderTopColor', 'fontWeight'].every((p) => css('.trust-yes')[p] === css('.trust-no')[p]),
      focused: el.shadowRoot.activeElement ? el.shadowRoot.activeElement.className : '', text: q('.box').textContent.replace(/\s+/g, ' ').trim() };
  });
  check(words.title === 'Use your Fake agent (tests) sign-in on this tile?' && /^This tile belongs to the org .+\. .*dev1.* can also change it\.$/.test(words.whose),
    `the card: whose tile it is and who can change it (${words.title} / ${words.whose})`);
  check(words.risk === 'Code in this tile runs with your sign-in while your coding agent works. People who can change this tile could read it and use your Fake agent (tests) account.',
    `…the risk, in two sentences (${words.risk})`);
  check(words.yes === 'Trust this tile — use my sign-in' && words.no === 'Start without my sign-in' && words.sameSize && words.sameLook && words.focused === '' && /^Without it, the agent /.test(words.then),
    `…and two choices of the same size and weight, neither focused; what happens without (${JSON.stringify({ yes: words.yes, no: words.no, sameSize: words.sameSize, sameLook: words.sameLook, then: words.then })})`);
  check(words.text.split(' ').length <= 85, `the whole card is ${words.text.split(' ').length} words: read in a few seconds (the harness agent's name is three words long, three times)`);
  await page.locator(`${CARD} button.trust-yes[aria-disabled="false"]`).waitFor({ timeout: 10000 }); // as a person meets it: armed a moment after it shows
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'trust-card-agent-tab');

  // ---- (b) only a person's own click on the armed button trusts ----
  const trusted = async () => ((await get()).trusted || []).length;
  await page.locator(`${CARD} button.trust-yes`).evaluate((b) => b.click()); // a script's click
  await page.locator(`${CARD} button.trust-yes`).focus();
  await page.keyboard.press('Enter');
  await page.waitForTimeout(400); // (a negative: nothing may be sent)
  check((await trusted()) === 0 && (await agent()).asks, 'a scripted click and Enter on "Trust this tile" trust nothing');

  // ---- (c) "Start without my sign-in" ----
  await page.locator(`${CARD} button.trust-no`).click();
  await waitFor(page, (t, x) => { const ag = t.frameFor(x.tile)?.testApi().agent(x.i); return !!ag && !ag.trust?.ask && ag.trustAgain; }, { tile: TILE, i: idx }, { timeout: 10000, label: 'turned down: the card goes, the header offers it again' });
  a = await agent();
  check((await trusted()) === 0 && a.id === first && a.state.source !== 'saved', `"Start without my sign-in": nothing is trusted, the session goes on as it was (${a.chip})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'trust-declined');
  // asked again from the header: the button is not armed as it appears
  const early = await page.evaluate(async () => {
    const deep = (sel, root = document) => { for (const n of root.querySelectorAll('*')) { if (n.matches(sel)) return n; if (n.shadowRoot) { const r = deep(sel, n.shadowRoot); if (r) return r; } } return null; };
    deep('button.trust-again').click();
    let yes = null; // read in the frame the button first exists in
    for (let n = 0; n < 600 && !yes; n++) { await new Promise((r) => requestAnimationFrame(r)); yes = deep('bx-agent-signin[view="trust"]')?.shadowRoot.querySelector('.trust-yes'); }
    return yes ? yes.getAttribute('aria-disabled') : 'no card';
  }).catch((e) => `error: ${e.message}`);
  check(early === 'true', `"Use my sign-in here…" asks again, and "Trust this tile" is not armed as it appears (aria-disabled ${early})`);

  // ---- (d) trusting: the session restarts on the sign-in; the tile is listed ----
  await page.locator(`${CARD} button.trust-yes[aria-disabled="false"]`).click({ timeout: 15000 });
  await waitFor(page, (t, x) => { const ag = t.frameFor(x.tile)?.testApi().agent(x.i); return !!ag && !ag.trust?.ask && /your saved sign-in: Personal/.test(ag.signinChip || ''); }, { tile: TILE, i: idx },
    { timeout: 40000, label: 'trusted: the agent restarts and its header names the saved sign-in' });
  a = await agent();
  const list = (await get()).trusted || [];
  check(a.state.source === 'saved' && a.id !== first && list.length === 1 && list[0].tile === TILE && list[0].holds === true && list[0].via === 'card' && !('ident' in list[0]),
    `her click trusted the tile: the session restarted on the sign-in, and the tile is listed (${a.chip}; ${JSON.stringify(list)})`);
  check((await get(`?tile=${encodeURIComponent(TILE)}`)).harnesses.find((h) => h.id === 'fake').here.inject === true && !a.again, 'a new session there is not asked again');
  await shotEl(page, `bx-frame[src="${TILE}"] .pop`, 'trust-trusted');

  // ---- (e) "Tiles you trust", and Stop trusting ----
  await page.locator(`bx-frame[src="${TILE}"] bx-agent-signin.signins button.open-list`).click();
  const DLG = `bx-frame[src="${TILE}"] bx-agent-signin.signins .dialog`;
  await page.locator(`${DLG} .trusted .item`).waitFor({ timeout: 10000 });
  const rowText = (await page.locator(`${DLG} .trusted`).textContent()).replace(/\s+/g, ' ').trim();
  check(/Tiles you trust/.test(rowText) && rowText.includes(TILE) && /trusted today/.test(rowText) && /Stop trusting/.test(rowText), `the list: "Tiles you trust" names it (${rowText.slice(0, 200)})`);
  await shotEl(page, DLG, 'trust-list');
  const running = a.id;
  await page.locator(`${DLG} .trusted button.untrust`).click();
  let gone = false;
  for (let n = 0; n < 100 && !gone; n++) { gone = !(await sessions()).some((s) => s.id === running); if (!gone) await page.waitForTimeout(100); }
  check(gone && (await trusted()) === 0, 'Stop trusting: the tile is no longer listed, and the session that ran on the sign-in is ended');
  await page.keyboard.press('Escape');
  await page.locator(DLG).waitFor({ state: 'detached', timeout: 10000 }).catch(() => {});

  // ---- (f) the workspace's switch: trusting off ----
  const off = await ctx.request.put(`${API}/policy`, { data: { trust: false } });
  const forbidden = (await get(`?tile=${encodeURIComponent(TILE)}`)).harnesses.find((h) => h.id === 'fake').here;
  check(off.ok() && forbidden.inject === false && forbidden.trust.allowed === false && /a workspace admin can change it/.test(forbidden.trust.forbidden),
    `trusting switched off: nothing is used there, and the answer says who can change that (${JSON.stringify(forbidden.trust)})`);
  const refused = await ctx.request.post(`${API}/trust`, { data: { tile: TILE } });
  check(refused.status() === 403, `…and nothing can be trusted (${refused.status()})`);
  await ctx.request.put(`${API}/policy`, { data: { trust: true } });
  await fr(page, TILE, (f) => { while (f.tabs.length) f.closeTab(0); });
  await settle(page);
  await closeCtx(ctx, page);

  // ---- (g) the card alone: desktop and phone width, dark and light ----
  for (const [name, viewport, scale] of [['desktop', { width: 1280, height: 520 }, 1], ['phone', { width: 390, height: 844 }, 2]]) {
    for (const scheme of ['dark', 'light']) {
      const pc = await browser.newContext({ viewport, deviceScaleFactor: scale, colorScheme: scheme });
      const pp = await pc.newPage();
      await pp.goto(`${URL}/login`);
      await pp.fill('input[name=username]', 'admin');
      await pp.fill('input[name=password]', 'admin');
      await Promise.all([pp.waitForNavigation(), pp.click('button')]);
      await pp.goto(`${URL}/xbin/system`); // an xbind-served page: no import map, the person's theme
      const m = await pp.evaluate(async (tile) => {
        await import('/vendor/agent-signin.js');
        const el = document.createElement('bx-agent-signin');
        el.setAttribute('view', 'trust'); el.setAttribute('harness', 'fake'); el.setAttribute('provider', 'Claude Code'); el.setAttribute('component', tile);
        const box = document.createElement('div');
        box.id = 'probe';
        box.style.cssText = 'padding: 12px; background: var(--bx-bg, #0B0C12); min-height: 100vh; box-sizing: border-box; max-width: 640px';
        box.append(el);
        document.body.replaceChildren(box);
        for (let n = 0; n < 200 && !el.testApi().ask; n++) await new Promise((r) => setTimeout(r, 25));
        await el.updateComplete;
        const yes = el.shadowRoot.querySelector('.trust-yes'), no = el.shadowRoot.querySelector('.trust-no');
        return { asks: !!el.testApi().ask, width: document.documentElement.scrollWidth, view: window.innerWidth, height: el.getBoundingClientRect().height,
          yes: yes && yes.getBoundingClientRect().toJSON(), no: no && no.getBoundingClientRect().toJSON() };
      }, TILE);
      check(m.asks && m.width <= m.view && m.yes && m.no && Math.abs(m.yes.height - m.no.height) <= 1 && Math.abs(m.yes.width - m.no.width) <= 1 && m.yes.height >= 36,
        `${name}, ${scheme}: the card fits (${m.width} ≤ ${m.view}, ${Math.round(m.height)} px tall), its two choices the same size (${Math.round(m.yes?.width)}×${Math.round(m.yes?.height)})`);
      await pp.waitForTimeout(700); // armed: as a person meets it a moment after it shows
      await pp.screenshot({ path: `${OUT}/trust-card-${name}-${scheme}.png`, fullPage: false });
      await pc.close();
    }
  }

  // leave nothing behind for the passes that follow
  const { ctx: c2, page: p2 } = await login(browser, 'admin', 'admin', VIEW);
  const v = await (await c2.request.get(API)).json();
  for (const t of v.trusted || []) await c2.request.delete(`${API}/trust/${t.id}`);
  for (const h of v.harnesses || []) for (const s of h.signins) await c2.request.delete(`${API}/${s.id}`);
  await closeCtx(c2, p2);
  return done();
}

module.exports = { agentSigninTrust };
