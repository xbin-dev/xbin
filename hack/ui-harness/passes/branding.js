// hack/ui-harness/passes/branding.js — workspace branding (D76): an admin
// sets a title + icon; the shell header, the tab title and the favicon
// follow live (the `branding` event); the sign-in page shows them to a
// logged-out browser; a user may not set them; clearing brings xbin's own
// back.
const { URL, login, closeCtx, waitFor, openShell, shot, checker } = require('../lib');

// a 1×1 transparent PNG
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

async function branding(browser) {
  const { check, done } = checker('branding');
  const A = await login(browser, 'admin', 'admin');
  const put = (body) => A.ctx.request.put(`${URL}/api/xbin/branding`, { data: body });
  await put({ title: '', icon: '' }); // a clean start
  await openShell(A.page);
  const hdr = () => A.page.evaluate(() => {
    const r = document.querySelector('bx-shell').shadowRoot;
    return { logo: r.querySelector('.logo')?.textContent.trim(), img: !!r.querySelector('.logo img.mark'), chip: r.querySelector('.ws-chip')?.textContent,
      title: document.title, icon: document.querySelector('link[rel="icon"]')?.getAttribute('href') };
  });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'the unbranded tab title' });
  let h = await hdr();
  check(h.logo === 'X/BIN' && !h.img && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg', `unbranded: X/BIN, the workspace chip, xbin's favicon (${JSON.stringify(h)})`);

  // set a title + icon over the API; the open shell follows on the branding event
  const r = await put({ title: 'Acme Ops', icon: PNG });
  check(r.status() === 200, `PUT /branding as admin (${r.status()})`);
  await waitFor(A.page, () => /Acme Ops · xbin$/.test(document.title), null, { timeout: 10000, label: 'the tab title follows the brand' });
  h = await hdr();
  check(h.logo === 'Acme Ops' && h.img && !h.chip && (h.icon || '').startsWith('data:image/png'), `branded header: icon + title, no wordmark, data-URI favicon (${JSON.stringify(h)})`);
  await shot(A.page, 'branding-shell', { fullPage: false });

  // the sign-in page, logged out, carries the brand
  const ctx = await browser.newContext(); const p = await ctx.newPage();
  await p.goto(`${URL}/login`);
  const lp = { title: await p.title(), img: await p.locator('.logo img.mark').count(), word: (await p.locator('.logo').textContent()).trim() };
  check(lp.title === 'Acme Ops — sign in' && lp.img === 1 && lp.word === 'Acme Ops', `sign-in page branded (${JSON.stringify(lp)})`);
  await shot(p, 'branding-login', { fullPage: false });
  await ctx.close();

  // a user may not brand; clearing brings xbin's own back everywhere
  const B = await login(browser, 'dev1', 'devpass123');
  const rb = await B.ctx.request.put(`${URL}/api/xbin/branding`, { data: { title: 'nope' } });
  check(rb.status() === 403, `a user may not brand (${rb.status()})`);
  await closeCtx(B.ctx, B.page);
  await put({ title: '', icon: '' });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'cleared: the tab title is back' });
  h = await hdr();
  check(h.logo === 'X/BIN' && !h.img && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg', `cleared: xbin's own again (${JSON.stringify(h)})`);
  await closeCtx(A.ctx, A.page);
  done();
}

module.exports = { branding };
