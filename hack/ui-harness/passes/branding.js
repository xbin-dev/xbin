// hack/ui-harness/passes/branding.js — workspace branding (D76): an admin
// sets a title + icon; the shell header, the tab title and the favicon
// follow live (the `branding` event); the sign-in page shows them to a
// logged-out browser; a user may not set them; clearing brings xbin's own
// back. The sign-in page is on the Base Two tokens (D184): it opts in, links
// /vendor/theme.css, follows the system's light or dark — live — and the
// person's hint cookie (xbin_theme) over it; xbin's own logo is the
// wordmark "xbin".
const { URL, login, closeCtx, waitFor, openShell, shot, checker } = require('../lib');

// a 1×1 transparent PNG
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

// The shell's own wordmark is shell-brand.js's: X/BIN until the shell takes
// Base Two's "xbin" (D184).
const SHELL_WORD = /^(X\/BIN|xbin)$/;

// signIn: the sign-in page's logo and theme facts.
const signIn = (p) => p.evaluate(() => {
  const root = document.documentElement;
  return {
    logo: document.querySelector('.logo')?.textContent.trim(), img: !!document.querySelector('.logo img.mark'),
    auto: root.getAttribute('data-bx-theme'), meta: document.querySelector('head > meta[name="xbin-theme"]')?.content || '',
    sheet: [...document.styleSheets].some((s) => (s.href || '').endsWith('/vendor/theme.css')),
    scheme: getComputedStyle(root).getPropertyValue('--bx-scheme').trim(),
  };
});

async function branding(browser) {
  const { check, done } = checker('branding');
  const A = await login(browser, 'admin', 'admin');
  const put = (body) => A.ctx.request.put(`${URL}/api/xbin/branding`, { data: body });
  await put({ title: '', icon: '' }); // a clean start

  // the sign-in page, unbranded: the wordmark; the system's theme, followed
  // live; the hint cookie over it (a value other than light or dark: none)
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light' });
    const p = await ctx.newPage();
    await p.goto(`${URL}/login`);
    let s = await signIn(p);
    check(s.logo === 'xbin' && !s.img && s.auto === 'auto' && s.sheet && s.meta === '' && s.scheme === 'light',
      `sign-in page: the wordmark, opted in, theme.css, a light system's Day (${JSON.stringify(s)})`);
    await shot(p, 'branding-login-light', { fullPage: false });
    await p.emulateMedia({ colorScheme: 'dark' });
    s = await signIn(p);
    check(s.scheme === 'dark', `the system turns dark: the open page follows (${s.scheme})`);
    await shot(p, 'branding-login-dark', { fullPage: false });
    for (const [system, cookie, want] of [['dark', 'light', 'light'], ['light', 'dark', 'dark'], ['dark', 'sepia', 'dark'], ['light', 'sepia', 'light']]) {
      await ctx.addCookies([{ name: 'xbin_theme', value: cookie, url: URL }]);
      await p.emulateMedia({ colorScheme: system });
      await p.reload();
      s = await signIn(p);
      check(s.scheme === want && s.meta === (cookie === 'sepia' ? '' : cookie),
        `sign-in page: a ${system} system, cookie ${cookie} → ${want} (${JSON.stringify(s)})`);
      if (cookie !== 'sepia') await shot(p, `branding-login-${system}-cookie-${cookie}`, { fullPage: false });
    }
    await ctx.close();
  }

  await openShell(A.page);
  const hdr = () => A.page.evaluate(() => {
    const r = document.querySelector('bx-shell').shadowRoot;
    return { logo: r.querySelector('.logo')?.textContent.trim(), img: !!r.querySelector('.logo img.mark'), chip: r.querySelector('.ws-chip')?.textContent,
      title: document.title, icon: document.querySelector('link[rel="icon"]')?.getAttribute('href') };
  });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'the unbranded tab title' });
  let h = await hdr();
  check(SHELL_WORD.test(h.logo) && !h.img && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg', `unbranded: xbin's wordmark, the workspace chip, xbin's favicon (${JSON.stringify(h)})`);

  // set a title + icon over the API; the open shell follows on the branding event
  const r = await put({ title: 'Acme Ops', icon: PNG });
  check(r.status() === 200, `PUT /branding as admin (${r.status()})`);
  await waitFor(A.page, () => /Acme Ops · xbin$/.test(document.title), null, { timeout: 10000, label: 'the tab title follows the brand' });
  h = await hdr();
  check(h.logo === 'Acme Ops' && h.img && !h.chip && (h.icon || '').startsWith('data:image/png'), `branded header: icon + title, no wordmark, data-URI favicon (${JSON.stringify(h)})`);
  await shot(A.page, 'branding-shell', { fullPage: false });

  // the sign-in page, logged out, carries the brand (in both themes)
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light' });
  const p = await ctx.newPage();
  await p.goto(`${URL}/login`);
  const lp = { title: await p.title(), img: await p.locator('.logo img.mark').count(), word: (await p.locator('.logo').textContent()).trim() };
  check(lp.title === 'Acme Ops — sign in' && lp.img === 1 && lp.word === 'Acme Ops', `sign-in page branded (${JSON.stringify(lp)})`);
  await shot(p, 'branding-login', { fullPage: false });
  await p.emulateMedia({ colorScheme: 'dark' });
  await shot(p, 'branding-login-branded-dark', { fullPage: false });
  await ctx.close();

  // a user may not brand; clearing brings xbin's own back everywhere
  const B = await login(browser, 'dev1', 'devpass123');
  const rb = await B.ctx.request.put(`${URL}/api/xbin/branding`, { data: { title: 'nope' } });
  check(rb.status() === 403, `a user may not brand (${rb.status()})`);
  await closeCtx(B.ctx, B.page);
  await put({ title: '', icon: '' });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'cleared: the tab title is back' });
  h = await hdr();
  check(SHELL_WORD.test(h.logo) && !h.img && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg', `cleared: xbin's own again (${JSON.stringify(h)})`);
  await closeCtx(A.ctx, A.page);
  done();
}

module.exports = { branding };
