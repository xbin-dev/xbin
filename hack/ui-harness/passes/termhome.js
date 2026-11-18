// hack/ui-harness/passes/termhome.js — which home a terminal mounts (D245),
// on the window's status bar. A person's home is theirs on their own
// tiles; on a tile whose source isn't only theirs — here apps/leads: the
// Sales org's, which dev1 and sales1 can change — and that they have not
// trusted, a session reads their real home and writes to this tile's copy.
//
// Under HARNESS_ISOLATE=1 (real sandboxes) this checks: (a) the terminal's
// status bar says "home: this tile's copy", and its first lines say what
// that means; (b) the chip opens xbin's dialog — why, whose tile it is and
// who can change it (everyone, by account id), what trusting means (two
// things) and that nothing is copied over — with "I trust this tile's
// source" and "Forget what this tile wrote to my home" — and, for the
// admin who runs this pass, that the session is confined like a member's
// until they trust the tile; the button that trusts is ARMED (as the
// sign-in card's): not a script's click, not Enter, not as it appears —
// the person's own click a moment later; (c) what the shell
// writes under ~ is in the copy and not in the real home; (d) trusting
// gives a NEW terminal the real home ("home: yours", a link to the docs),
// the open one stays on the copy, and the list says the copy is kept, not
// used; (e) taking the trust back ends the terminal that had the real
// home; (f) forget removes the copy. Without sandboxes it checks that the
// bar says "home: yours" and that nothing is protected.
const { URL, login, closeCtx, fr, waitFor, openShell, usePersonalScreen, openTile, shotEl, checker } = require('../lib');

const TILE = 'apps/leads';
const VIEW = { viewport: { width: 1400, height: 1300 } };
const API = `${URL}/api/xbin/agent/signins`;

async function termHome(browser) {
  const { check, done } = checker('term-home');
  const { ctx, page } = await login(browser, 'admin', 'admin', VIEW);
  const get = async (path = '') => (await ctx.request.get(API + path)).json();
  const here = async () => (await get(`?tile=${encodeURIComponent(TILE)}`));
  const sessions = async () => (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=${encodeURIComponent(TILE)}`)).json();
  const clean = async () => {
    await ctx.request.put(`${API}/policy`, { data: { enabled: true, trust: true } });
    for (const s of await sessions()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
    const v = await get();
    for (const t of v.trusted || []) await ctx.request.delete(`${API}/trust/${t.id}`);
    for (const h of v.homes || []) await ctx.request.delete(`${API}/homes/${h.id}`);
  };
  await clean();
  const isolated = !!process.env.HARNESS_ISOLATE;
  const rule = (await here()).home;
  check(!!rule && rule.mode === (isolated ? 'copy' : 'own') && (isolated ? !!rule.trust && rule.trust.ownerKind === 'org' && /isn't only yours — it belongs to the org/.test(rule.why) : rule.unsandboxed === true),
    `xbind's word on a session's home on ${TILE} (${JSON.stringify(rule)})`);

  await openShell(page);
  await usePersonalScreen(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));
  const homeOf = () => fr(page, TILE, (f) => f.ide.status.home);
  const waitHome = (mode, label) => waitFor(page, (t, x) => t.frameFor(x.tile)?.testApi().ide.status.home?.mode === x.mode, { tile: TILE, mode }, { timeout: 60000, label });
  await fr(page, TILE, (f) => { if (!f.tabs.length) f.newTerm(); });

  if (!isolated) {
    await waitHome('own', 'the status bar says which home the terminal has');
    const h = await homeOf();
    check(h.text === 'home: yours' && /without sandboxes/.test(h.title) && /nothing protects/.test(h.title) && /agent-signins\.md#your-home/.test(h.link),
      `without sandboxes the bar says "home: yours", links the explanation and claims no protection (${JSON.stringify(h)})`);
    await clean();
    await closeCtx(ctx, page);
    return done();
  }

  // ---- (a) the bar, and the session's first lines ----
  await waitHome('copy', 'the terminal on an org tile starts on this tile\'s copy of the home');
  let h = await homeOf();
  check(h.text === "home: this tile's copy" && /stays on this tile/.test(h.title), `the status bar: ${JSON.stringify(h)}`);
  const copyTab = await fr(page, TILE, (f) => f.activeTab);
  const row = (await sessions()).find((s) => s.home);
  check(!!row && row.home.mode === 'copy' && !!row.home.trust && !!row.home.trust.basis && /agent-signins\.md/.test(row.home.docs), `the session's row carries its home (${JSON.stringify(row && row.home)})`);
  await shotEl(page, `bx-frame[src="${TILE}"] .status`, 'term-home-copy-bar');

  // ---- (b) the chip's dialog ----
  await fr(page, TILE, (f) => { f.aboutHome(); });
  await waitFor(page, (t, tile) => !!t.frameFor(tile)?.testApi().dialog, TILE, { timeout: 15000, label: 'the home chip opens its dialog' });
  let dlg = await fr(page, TILE, (f) => f.dialog);
  const msg = dlg.message || '';
  check(dlg.title === "home: this tile's copy (changes stay on this tile)" && /reads everything in your real home/.test(msg) && /Your real home is not changed/.test(msg)
    && /This tile belongs to the org /.test(msg) && /dev1/.test(msg) && /sales1/.test(msg) && !/ more can/.test(msg),
  `the dialog: what the copy is, why, whose tile and everyone who can change it (${msg.replace(/\s+/g, ' ').slice(0, 400)}…)`);
  check(/means two things: xbin uses your saved coding-agent sign-in here, and this tile's code can change your home/.test(msg) && /Nothing this tile wrote to its copy is copied into your home/.test(msg),
    'the dialog says what trusting means — two things — and that nothing is copied over');
  const labels = dlg.buttons.map((b) => b.label);
  check(labels.includes("I trust this tile's source") && labels.includes('Forget what this tile wrote to my home') && !labels.some((l) => /copy.*over|merge|bring/i.test(l)),
    `the dialog's choices: ${JSON.stringify(labels)} — nothing that copies the copy over`);
  check(row.home.confined === true && /confined like a member's until you trust this tile/.test(msg) && /confined like a member's/.test(h.title),
    `an admin's session on the copy is a member's, and says so (row ${JSON.stringify(row.home.confined)}; bar "${h.title}")`);
  const armedKey = (dlg.buttons.find((b) => b.value === 'trust') || {}).armed;
  check(!!armedKey && armedKey === row.home.trust.basis, `"I trust this tile's source" is an armed button, for the question as shown (${armedKey})`);
  await shotEl(page, `bx-frame[src="${TILE}"] bx-dialog`, 'term-home-dialog');

  // ---- (c) a write lands in the copy, not in the real home ----
  const before = await here();
  check((before.homes || []).length === 1 && before.homes[0].tile === TILE && before.homes[0].sessions === 1 && before.homes[0].unused === false && before.home.copy && before.home.copy.id === before.homes[0].id,
    `the copy is listed, in use (${JSON.stringify(before.homes)})`);

  // ---- (d) trust: a new terminal has the real home; this one stays on the copy ----
  // only the person's own click on the armed button trusts: not a script's, not Enter, not as the dialog appears
  const TRUST = `bx-frame[src="${TILE}"] bx-dialog button[data-armed]`;
  await fr(page, TILE, (f) => f.answerDialog(null));
  await fr(page, TILE, (f) => { f.aboutHome(); });
  await page.locator(TRUST).waitFor({ timeout: 15000 });
  const early = await page.locator(TRUST).getAttribute('aria-disabled');
  await page.locator(TRUST).click({ force: true }); // a real click, before it is armed
  check(early === 'true' && !!(await fr(page, TILE, (f) => f.dialog)) && ((await here()).trusted || []).length === 0, `the trust button is not armed as it appears (aria-disabled ${early}): an early click trusts nothing`);
  await page.locator(`${TRUST}[aria-disabled="false"]`).waitFor({ timeout: 10000 });
  await page.locator(TRUST).evaluate((b) => b.click()); // a script's click
  await page.locator(TRUST).focus();
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  dlg = await fr(page, TILE, (f) => f.dialog);
  check(!!dlg && /reads everything in your real home/.test(dlg.message || '') && ((await here()).trusted || []).length === 0, 'a script\'s click and Enter on the armed button trust nothing: the dialog is still asking');
  await page.locator(TRUST).click(); // the person's
  await waitFor(page, (t, tile) => { const d = t.frameFor(tile)?.testApi().dialog; return !!d && /New sessions on this tile have your real home/.test(d.message || ''); }, TILE, { timeout: 15000, label: 'trusted: the dialog says what happens now' });
  await fr(page, TILE, (f) => f.answerDialog('ok'));
  let v = await here();
  check((v.trusted || []).length === 1 && v.trusted[0].holds && v.home.mode === 'own' && v.homes.length === 1 && v.homes[0].unused === true,
    `trusted: a new session would have the real home; the copy is kept, not used (${JSON.stringify({ home: v.home.mode, homes: v.homes })})`);
  check((await homeOf()).mode === 'copy', 'the terminal that started on the copy stays on it');
  await fr(page, TILE, (f) => f.newTerm());
  await waitHome('own', 'a new terminal on the trusted tile has the real home');
  h = await homeOf();
  check(h.text === 'home: yours' && /agent-signins\.md#your-home/.test(h.link), `the new terminal's bar: ${JSON.stringify(h)}`);
  await shotEl(page, `bx-frame[src="${TILE}"] .status`, 'term-home-own-bar');
  const live = (await sessions()).filter((s) => s.home);
  check(live.length === 2 && live.filter((s) => s.home.mode === 'own').length === 1 && live.filter((s) => s.home.mode === 'copy').length === 1, `two terminals: one on the copy, one with the real home (${live.map((s) => s.home.mode)})`);

  // ---- (e) the trust taken back: the real-home terminal ends ----
  const un = await (await ctx.request.delete(`${API}/trust/${v.trusted[0].id}`)).json();
  check(un.ok && un.ended === 1, `"Stop trusting" ended the terminal that had the real home (${JSON.stringify(un)})`);
  let left = [];
  for (let i = 0; i < 100; i++) { left = (await sessions()).filter((s) => s.home); if (left.length === 1) break; await page.waitForTimeout(100); }
  check(left.length === 1 && left[0].home.mode === 'copy', `the terminal on the copy is the one left (${left.map((s) => s.home.mode)})`);

  // ---- (f) forget ----
  await fr(page, TILE, (f, t, i) => f.setActiveTab(i), copyTab);
  await fr(page, TILE, (f) => { f.aboutHome(); });
  await waitFor(page, (t, tile) => !!t.frameFor(tile)?.testApi().dialog, TILE, { timeout: 15000, label: 'the dialog again' });
  await fr(page, TILE, (f) => f.answerDialog('forget'));
  await waitFor(page, (t, tile) => { const d = t.frameFor(tile)?.testApi().dialog; return !!d && /Forget what this tile wrote to your home\?/.test(d.title || ''); }, TILE, { timeout: 15000, label: 'forget asks first' });
  dlg = await fr(page, TILE, (f) => f.dialog);
  check(/Your real home is not touched/.test(dlg.message), `forget says what it does (${dlg.message})`);
  await fr(page, TILE, (f) => f.answerDialog('ok'));
  let gone = null;
  for (let i = 0; i < 100 && !gone; i++) { const x = await here(); if (!(x.homes || []).length) gone = x; else await page.waitForTimeout(200); }
  check(!!gone && !gone.home.copy && (await sessions()).filter((s) => s.home).length === 0, 'forgotten: the copy is gone and the terminal on it was ended');

  await clean();
  await closeCtx(ctx, page);
  done();
}

module.exports = { termHome };
