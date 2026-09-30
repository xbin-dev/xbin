// hack/ui-harness/passes/partitionconsent.js — covers PD-13 and 06 §12.3
// (F14b): the shell's consent prompts for partitioned tiles, over a real
// refused cross-partition reach. <x> (partitioned, user) declares a kv
// resource `notes`; <a> and <b> (partitioned) each hold an approved grant on
// it (res:<x>/notes, reader); all three are dev1's. dev1's frame of <a> or
// <b> reads it through xbin.fetch — dev1's own partition of it (05 §1):
//   1. policy off: the read goes through, and the shell prompts nothing
//      (after it read the consents; the empty strip item takes no room);
//   2. policy on: the read is refused ("dev1 hasn't let <a> use their <x>
//      data") and dev1's shell asks — the tiles, why, Allow / Don't allow —
//      as does a second shell of dev1's opened afterwards (the asks xbind
//      lists); nobody else's shell shows dev1's ask: an admin's (it reads
//      the admin's own, empty), the workspace token's and an admin's view
//      as dev1 (neither even asks xbind);
//   3. the policy off: the prompt goes (the policies event) and the read
//      goes through; on again: the ask shows again;
//   4. Don't allow: the prompt goes, the read stays refused, a reload
//      doesn't bring it back;
//   5. <b>'s read is refused and asks; while a pop-out window of <b>'s
//      covers the question, Allow is disabled (and a click does nothing);
//      the window closed, Allow: the retry goes through, and dev1's other
//      shell drops its prompt (the consent event);
//   6. dev1 takes it back: <b> is refused again, and the ask dev1 answered
//      doesn't come back (xbind asks again another day).
// The tile names are fresh each run (xbind asks at most once a day per
// edge, and a rerun on the same xbind must see new asks). The policy goes
// back off; dev1's consent, the grants and the tiles go at the end.
const path = require('path');
const { URL, fs, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, tileFrame, settle, sleep, shot, shotEl, checker } = require('../lib');

const WS = process.env.WS || '';
const RUN = Date.now().toString(36).slice(-5);
const X = `apps/pc${RUN}x`, A = `apps/pc${RUN}a`, B = `apps/pc${RUN}b`;
const RES = `res:${X}/notes`;
const PANEL = 'bx-shell bx-part-consent .panel';
const ask = (from) => `bx-part-consent [data-consent="${from}→${X}"]`;
const CONSENTS = '/api/xbin/partitions/consents';
const isConsents = (r) => new globalThis.URL(r.url()).pathname === CONSENTS;
// the consents calls a page makes, from now on
const consentCalls = (page) => { const seen = []; page.on('request', (r) => { if (isConsents(r)) seen.push(r.method()); }); return seen; };
// the strip item's box: hidden (no box, no gap) when there is nothing to answer
const hostShown = (page) => page.locator('bx-shell bx-part-consent').evaluate((el) => getComputedStyle(el).display !== 'none');

async function until(fn, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(200);
  }
}

function writeTile(tile, files) {
  const dir = path.join(WS, tile);
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'index.html'), `<!doctype html><h1>${tile}</h1>\n`);
  for (const [n, v] of Object.entries(files)) fs.writeFileSync(path.join(dir, n), JSON.stringify(v));
}

// read(page, tile) → dev1's frame of tile reads <x>'s notes: {status, text}
const read = async (page, tile) => (await tileFrame(page, tile)).evaluate(async (u) => {
  const r = await window.xbin.fetch(u);
  return { status: r.status, text: (await r.text()).slice(0, 300) };
}, `/api/xbin/kv/${RES}/?prefix=`);

async function partitionConsent(browser) {
  const { check, done } = checker('partition-consent');
  const AD = await login(browser, 'admin', 'admin');
  const api = (method, p, data) => AD.ctx.request.fetch(`${URL}/api/xbin${p}`, { method, data });
  const policy = async (on) => (await api('PUT', '/workspace-policies', { partitionConsent: on })).status();
  let D, P2, T, V;
  try {
    // ---- the edge ----
    check(await policy(false) === 200, 'the policy starts off');
    writeTile(X, { 'xbin.json': { title: 'notes, per person', partition: ['user'] }, 'scope.json': { resources: { notes: { type: 'kv' } } } });
    for (const t of [A, B]) writeTile(t, { 'xbin.json': { title: `${t}: reads ${X}`, partition: ['user'], uses: [{ target: RES, role: 'reader' }] } });
    await until(async () => {
      for (const t of [X, A, B]) {
        const r = await api('GET', `/components/${t}`);
        if (r.status() !== 200 || (await r.json().catch(() => ({}))).component?.partition?.state !== 'partitioned') return false;
      }
      return true;
    }, 'the three tiles registered, partitioned');
    const set = [];
    for (const t of [X, A, B]) set.push((await api('POST', '/owner', { tile: t, to: 'user:dev1' })).status());
    for (const t of [A, B]) set.push((await api('POST', '/grants', { from: t, target: RES, role: 'reader' })).status());
    check(set.every((s) => s === 200), `${X}, ${A}, ${B} are dev1's; ${A} and ${B} hold an approved grant on ${RES} (${set.join(' ')})`);

    // ---- 1. policy off: the reach goes through, nothing asks ----
    D = await login(browser, 'dev1', 'devpass123');
    let read1 = D.page.waitForResponse((x) => isConsents(x) && x.request().method() === 'GET', { timeout: 20000 });
    await openShell(D.page);
    const g1 = await read1.then((x) => x.status()).catch(() => 0);
    check(g1 === 200, `dev1's shell reads their consents once it sees a partitioned tile (${g1})`);
    await usePersonalScreen(D.page);
    await openTile(D.page, A);
    await openTile(D.page, B);
    let r = await until(async () => { const x = await read(D.page, A).catch(() => null); return x?.status && x; }, `${A}'s frame reads`);
    check(r.status === 200 && r.text.includes('keys'), `policy off: dev1's ${A} reads their ${X} notes (${r.status} ${r.text})`);
    await settle(D.page);
    check(await D.page.locator(PANEL).count() === 0 && !(await hostShown(D.page)), 'policy off: no prompt, and the strip item takes no room');

    // ---- 2. policy on: refused, and dev1 is asked ----
    check(await policy(true) === 200, 'the admin turns partitionConsent on');
    r = await until(async () => { const x = await read(D.page, A); return x.status === 403 && x; }, `${A}'s read refused`);
    check(r.text.includes(`dev1 hasn't let ${A} use their ${X} data`), `policy on: the read is refused for want of dev1's consent (${r.text})`);
    const q = D.page.locator(ask(A));
    await q.waitFor({ timeout: 15000 });
    const words = (await q.innerText()).replace(/\s+/g, ' ');
    check(words.includes(`${A} asks to use your data in ${X}`) && words.includes('Your workspace asks you first')
      && words.includes('everyone who can change it'), `dev1's shell asks, naming both tiles and why (${words.slice(0, 200)})`);
    const bt = await q.locator('button').evaluateAll((bs) => bs.map((b) => [b.textContent.trim(), b.title]));
    check(bt.length === 2 && bt[0][0] === 'Don\'t allow' && /Nothing is stored/.test(bt[0][1]) && bt[1][0] === 'Allow', `Don't allow and Allow (${JSON.stringify(bt)})`);
    check(await D.page.locator(PANEL).locator('h4').innerText().then((t) => /partitioned tiles ask for your data/i.test(t)), 'under its heading, in the shell\'s decision strip');
    await shot(D.page, 'partition-consent-prompt');
    await shotEl(D.page, PANEL, 'partition-consent-panel');
    P2 = await D.ctx.newPage();
    await openShell(P2);
    await P2.locator(ask(A)).waitFor({ timeout: 15000 });
    check(true, 'a second shell of dev1\'s shows the ask too (xbind lists it)');
    // nobody else's shell shows dev1's ask
    read1 = AD.page.waitForResponse((x) => isConsents(x) && x.request().method() === 'GET', { timeout: 20000 });
    await openShell(AD.page);
    const ga = await read1.then((x) => x.json()).catch(() => null);
    await settle(AD.page);
    check(ga && Array.isArray(ga.asked) && ga.asked.length === 0 && await AD.page.locator(PANEL).count() === 0 && !(await hostShown(AD.page)),
      `an admin's shell reads the admin's own consents — no ask of dev1's — and shows no prompt (${JSON.stringify(ga?.asked)})`);
    const token = fs.readFileSync(path.join(WS, '.xbin', 'token'), 'utf8').trim();
    T = await browser.newContext({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1 });
    const tp = await T.newPage();
    const tcalls = consentCalls(tp);
    await tp.goto(`${URL}/login?token=${encodeURIComponent(token)}`);
    await openShell(tp);
    await sleep(1500);
    check(tcalls.length === 0 && await tp.locator(PANEL).count() === 0, `the workspace token's shell never asks xbind for consents, and shows no prompt (${tcalls})`);
    await T.close();
    T = null;
    V = await login(browser, 'admin', 'admin');
    const tk = await (await V.ctx.request.post(`${URL}/api/xbin/impersonate`, { data: { user: 'dev1' } })).json().catch(() => ({}));
    if (tk.url) {
      await V.page.goto(tk.url.startsWith('http') ? tk.url : `${URL}${tk.url}`);
      await openShell(V.page);
      await V.page.locator('bx-shell .viewas').waitFor({ timeout: 10000 });
      // counted from a fresh view-as document (the admin's own shell, before, may read the admin's)
      const vcalls = consentCalls(V.page);
      await V.page.reload();
      await V.page.locator('bx-shell .viewas').waitFor({ timeout: 15000 });
      await sleep(1500);
      check(vcalls.length === 0 && await V.page.locator(PANEL).count() === 0,
        `an admin viewing the workspace as dev1 never sees dev1's ask, nor asks xbind for consents (${vcalls})`);
      await V.ctx.request.post(`${URL}/api/xbin/impersonate/stop`).catch(() => {});
    } else {
      check(false, `a view-as ticket for dev1 (${JSON.stringify(tk)})`);
    }
    await closeCtx(V.ctx, V.page);
    V = null;

    // ---- 3. policy off: the prompt goes; on again: it's back ----
    check(await policy(false) === 200, 'the admin turns the policy off');
    await q.waitFor({ state: 'detached', timeout: 15000 });
    check(await D.page.locator(PANEL).count() === 0, 'policy off: the prompt goes (the policies event)');
    r = await read(D.page, A);
    check(r.status === 200, `… and the read goes through again (${r.status})`);
    check(await policy(true) === 200, 'on again');
    await q.waitFor({ timeout: 15000 });
    check(true, 'the ask is back while dev1 hasn\'t answered it');

    // ---- 4. Don't allow: stays refused ----
    await q.locator('button.deny').click();
    await q.waitFor({ state: 'detached', timeout: 10000 });
    const said = await D.page.locator(`bx-part-consent [data-consent-done="${A}→${X}"]`).innerText().catch(() => '');
    check(said.includes(`Not allowed: ${A}'s calls into your ${X} data stay refused`), `Don't allow: the prompt goes, saying so (${said})`);
    r = await read(D.page, A);
    check(r.status === 403, `the read stays refused (${r.status})`);
    await D.page.reload();
    await openShell(D.page);
    await settle(D.page);
    await sleep(1500);
    check(await D.page.locator(ask(A)).count() === 0, 'a reload doesn\'t bring the declined ask back');

    // ---- 5. Allow: the retry goes through ----
    r = await until(async () => { const x = await read(D.page, B).catch(() => null); return x?.status === 403 && x; }, `${B}'s read refused`);
    check(r.text.includes(`dev1 hasn't let ${B} use their ${X} data`), `${B}'s read is refused too (${r.status})`);
    const qb = D.page.locator(ask(B));
    await qb.waitFor({ timeout: 15000 });
    await P2.locator(ask(B)).waitFor({ timeout: 15000 });
    check(await D.page.locator(ask(A)).count() === 0, `only ${B}'s ask shows: ${A}'s stays declined`);
    const allow = qb.locator('button.allow');
    await qb.locator('button.allow:not([disabled])').waitFor({ timeout: 5000 });
    // a pop-out window of B's over the question, Allow still in view: Allow is off
    const qbox = await qb.locator('.q').boundingBox();
    await (await tileFrame(D.page, B)).evaluate((sp) => { window.__cover = window.xbin.window(sp); },
      { path: 'cover', title: 'cover', x: Math.round(qbox.x), y: Math.round(qbox.y - 20), width: 260, height: 160 });
    const cover = D.page.locator('bx-shell .spawn').filter({ has: D.page.locator('.stitle', { hasText: /^cover$/ }) });
    await cover.waitFor({ timeout: 10000 });
    const abox = await allow.boundingBox(), wbox = await cover.boundingBox();
    check(abox && wbox && abox.x > wbox.x + wbox.width, `the window covers the question, not Allow (window ${JSON.stringify(wbox)}, Allow ${JSON.stringify(abox)})`);
    await until(async () => allow.isDisabled(), 'Allow disabled while covered', 5000).catch(() => {});
    check(await allow.isDisabled(), 'while a tile\'s window covers the question, Allow is disabled');
    await shot(D.page, 'partition-consent-covered');
    await allow.click({ force: true }).catch(() => {});
    await sleep(500);
    const still = await (await D.ctx.request.get(`${URL}${CONSENTS}`)).json();
    check(!(still.consents || []).some((c) => c.from === B), 'a click on it does nothing: no consent');
    await (await tileFrame(D.page, B)).evaluate(() => window.__cover.close());
    await cover.waitFor({ state: 'detached', timeout: 10000 });
    await qb.locator('button.allow:not([disabled])').waitFor({ timeout: 5000 });
    check(true, 'the window closed, Allow is live again (after a moment)');
    await allow.click();
    const ok = D.page.locator(`bx-part-consent [data-consent-done="${B}→${X}"]`);
    await ok.waitFor({ timeout: 10000 });
    const okText = await ok.innerText();
    check(okText.includes(`Allowed: ${B} can use your ${X} data from its next call`) && okText.includes(`bx partition consent ${B} ${X} --revoke`),
      `Allow: the shell says so, and how to take it back (${okText})`);
    await shotEl(D.page, PANEL, 'partition-consent-allowed');
    r = await read(D.page, B);
    check(r.status === 200, `the retry goes through (${r.status} ${r.text})`);
    const view = await (await D.ctx.request.get(`${URL}/api/xbin/partitions/consents`)).json();
    check((view.consents || []).some((c) => c.from === B && c.to === X) && !(view.asked || []).some((c) => c.from === B),
      `xbind holds dev1's consent ${B} → ${X} (${JSON.stringify(view.consents)})`);
    await P2.locator(ask(B)).waitFor({ state: 'detached', timeout: 15000 });
    check(true, 'dev1\'s other shell drops the prompt (the consent event)');
    r = await read(D.page, A);
    check(r.status === 403, `${A} stays refused (${r.status})`);

    // ---- 6. taken back: the answered ask doesn't come back ----
    const reread = D.page.waitForResponse((x) => isConsents(x) && x.request().method() === 'GET', { timeout: 15000 });
    const rv = await D.ctx.request.fetch(`${URL}${CONSENTS}`, { method: 'DELETE', data: { from: B, to: X } });
    const rb = await rv.json().catch(() => ({}));
    check(rv.status() === 200 && rb.revoked === true && !(rb.asked || []).some((c) => c.from === B), `dev1 takes ${B}'s consent back; xbind lists no ask for it (${JSON.stringify(rb.asked)})`);
    await reread.catch(() => {}); // the consent event: the shell reads again
    await settle(D.page);
    r = await read(D.page, B);
    await sleep(1000);
    check(r.status === 403 && await D.page.locator(ask(B)).count() === 0, `${B} is refused again, and the shell doesn't bring back the ask dev1 answered (${r.status})`);
  } finally {
    if (T) await T.close().catch(() => {});
    if (V) await closeCtx(V.ctx, V.page).catch(() => {});
    await policy(false).catch(() => {});
    if (D) {
      await D.ctx.request.fetch(`${URL}/api/xbin/partitions/consents`, { method: 'DELETE', data: { from: B, to: X } }).catch(() => {});
      for (const t of [A, B]) await closeTile(D.page, t).catch(() => {});
      await closeCtx(D.ctx, D.page);
    }
    for (const t of [A, B]) await api('DELETE', '/grants', { from: t, target: RES, role: 'reader' }).catch(() => {});
    for (const t of [X, A, B]) {
      const dir = path.join(WS, t);
      if (WS && path.isAbsolute(WS) && dir.startsWith(path.join(WS, 'apps', 'pc')) && dir.length > path.join(WS, 'apps', 'pc').length) {
        fs.rmSync(dir, { recursive: true, force: true });
      }
    }
    await closeCtx(AD.ctx, AD.page);
  }
  done();
}

module.exports = { partitionConsent };
