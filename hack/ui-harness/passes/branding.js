// hack/ui-harness/passes/branding.js — workspace branding (D76): an admin
// sets a title + icon; the shell header, the tab title and the favicon
// follow live (the `branding` event); the sign-in page shows them to a
// logged-out browser; a user may not set them; clearing brings xbin's own
// back. The sign-in page is on the Base Two tokens (D184): it opts in, links
// /vendor/theme.css, follows the system's light or dark — live — and the
// person's hint cookie (xbin_theme) over it. xbin's own logo, there and in
// the shell's header, is its mark and wordmark (D183): the lockup drawn
// inline, the tile in its own colours in both themes and the word in the
// text colour; an icon set without a title stands beside the word alone.
const { URL, login, closeCtx, waitFor, openShell, shot, checker } = require('../lib');

// a 1×1 transparent PNG
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';
const COBALT = 'rgb(31, 61, 255)'; // the tile, #1F3DFF, the same in both themes

// lockupFacts(root), evaluated in the page (root: the document or a shadow
// root): the logo block's lockup — its name, its drawn size, the tile's
// fill, and whether the word is drawn in the logo's text colour — or null.
const lockupFacts = (root) => {
  const svg = root.querySelector('.logo svg.lockup');
  if (!svg) return null;
  const box = svg.getBoundingClientRect(), word = svg.querySelector('.m-w'), tile = svg.querySelector('.m-tile');
  return {
    label: svg.getAttribute('aria-label'), w: Math.round(box.width), h: Math.round(box.height),
    tile: tile ? getComputedStyle(tile).fill : '',
    wordInk: !!word && getComputedStyle(word).fill === getComputedStyle(svg.closest('.logo')).color,
  };
};
const lockupIn = (p, rootExpr) => p.evaluate(`(${lockupFacts})(${rootExpr})`);
// the lockup as drawn: named xbin, the tile's height, its width from the
// viewBox's 3174:1024, the tile cobalt, the word in the text colour
const lockupOK = (l, h) => !!l && l.label === 'xbin' && l.h === h && Math.abs(l.w - h * 3174 / 1024) <= 1 && l.tile === COBALT && l.wordInk;

// signIn: the sign-in page's logo and theme facts.
const signIn = async (p) => ({
  ...(await p.evaluate(() => {
    const root = document.documentElement;
    return {
      logo: document.querySelector('.logo')?.textContent.trim(), img: !!document.querySelector('.logo img.mark'),
      word: !!document.querySelector('.logo svg.wordmark'),
      auto: root.getAttribute('data-bx-theme'), meta: document.querySelector('head > meta[name="xbin-theme"]')?.content || '',
      sheet: [...document.styleSheets].some((s) => (s.href || '').endsWith('/vendor/theme.css')),
      scheme: getComputedStyle(root).getPropertyValue('--bx-scheme').trim(),
    };
  })),
  lockup: await lockupIn(p, 'document'),
});

async function branding(browser) {
  const { check, done } = checker('branding');
  const A = await login(browser, 'admin', 'admin');
  const put = (body) => A.ctx.request.put(`${URL}/api/xbin/branding`, { data: body });
  await put({ title: '', icon: '' }); // a clean start

  // the sign-in page, unbranded: the mark and wordmark; the system's theme,
  // followed live; the hint cookie over it (a value other than light or
  // dark: none)
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light' });
    const p = await ctx.newPage();
    await p.goto(`${URL}/login`);
    let s = await signIn(p);
    check(s.logo === 'xbin' && lockupOK(s.lockup, 32) && !s.img && s.auto === 'auto' && s.sheet && s.meta === '' && s.scheme === 'light',
      `sign-in page: the mark and wordmark, opted in, theme.css, a light system's Day (${JSON.stringify(s)})`);
    await shot(p, 'branding-login-light', { fullPage: false });
    await p.emulateMedia({ colorScheme: 'dark' });
    s = await signIn(p);
    check(s.scheme === 'dark' && lockupOK(s.lockup, 32), `the system turns dark: the open page follows, the tile stays cobalt and the word follows the text (${JSON.stringify(s)})`);
    await shot(p, 'branding-login-dark', { fullPage: false });
    for (const [system, cookie, want] of [['dark', 'light', 'light'], ['light', 'dark', 'dark'], ['dark', 'sepia', 'dark'], ['light', 'sepia', 'light']]) {
      await ctx.addCookies([{ name: 'xbin_theme', value: cookie, url: URL }]);
      await p.emulateMedia({ colorScheme: system });
      await p.reload();
      s = await signIn(p);
      check(s.scheme === want && s.meta === (cookie === 'sepia' ? '' : cookie) && lockupOK(s.lockup, 32),
        `sign-in page: a ${system} system, cookie ${cookie} → ${want} (${JSON.stringify(s)})`);
      if (cookie !== 'sepia') await shot(p, `branding-login-${system}-cookie-${cookie}`, { fullPage: false });
    }
    await ctx.close();
  }

  await openShell(A.page);
  const shellRoot = `document.querySelector('bx-shell').shadowRoot`;
  const hdr = async () => ({
    ...(await A.page.evaluate(() => {
      const r = document.querySelector('bx-shell').shadowRoot;
      return { logo: r.querySelector('.logo')?.textContent.trim(), img: !!r.querySelector('.logo img.mark'), word: !!r.querySelector('.logo svg.word'),
        chip: r.querySelector('.ws-chip')?.textContent, title: document.title, icon: document.querySelector('link[rel="icon"]')?.getAttribute('href') };
    })),
    lockup: await lockupIn(A.page, shellRoot),
  });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'the unbranded tab title' });
  let h = await hdr();
  check(h.logo === 'xbin' && lockupOK(h.lockup, 24) && !h.img && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg',
    `unbranded: xbin's mark and wordmark, the workspace chip, xbin's favicon (${JSON.stringify(h)})`);
  await shot(A.page, 'branding-shell-xbin', { fullPage: false });

  // set a title + icon over the API; the open shell follows on the branding event
  const r = await put({ title: 'Acme Ops', icon: PNG });
  check(r.status() === 200, `PUT /branding as admin (${r.status()})`);
  await waitFor(A.page, () => /Acme Ops · xbin$/.test(document.title), null, { timeout: 10000, label: 'the tab title follows the brand' });
  h = await hdr();
  check(h.logo === 'Acme Ops' && h.img && !h.lockup && !h.chip && (h.icon || '').startsWith('data:image/png'), `branded header: icon + title, no lockup, data-URI favicon (${JSON.stringify(h)})`);
  await shot(A.page, 'branding-shell', { fullPage: false });

  // the sign-in page, logged out, carries the brand (in both themes)
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: 'light' });
  const p = await ctx.newPage();
  await p.goto(`${URL}/login`);
  let lp = { title: await p.title(), ...(await signIn(p)) };
  check(lp.title === 'Acme Ops — sign in' && lp.img && lp.logo === 'Acme Ops' && !lp.lockup, `sign-in page branded (${JSON.stringify(lp)})`);
  await shot(p, 'branding-login', { fullPage: false });
  await p.emulateMedia({ colorScheme: 'dark' });
  await shot(p, 'branding-login-branded-dark', { fullPage: false });

  // an icon without a title: the icon beside xbin's word, never its tile
  await put({ title: '' });
  await waitFor(A.page, () => /workspace · xbin$/.test(document.title), null, { timeout: 10000, label: 'the title cleared, the icon kept' });
  h = await hdr();
  check(h.img && h.word && h.logo === 'xbin' && !h.lockup && h.chip === 'workspace' && (h.icon || '').startsWith('data:image/png'),
    `an icon alone: the icon, xbin's word, the chip (${JSON.stringify(h)})`);
  await p.reload();
  lp = { title: await p.title(), ...(await signIn(p)) };
  check(lp.title === 'xbin — sign in' && lp.img && lp.word && !lp.lockup, `sign-in page, an icon alone: the icon and the word (${JSON.stringify(lp)})`);
  await ctx.close();

  // a user may not brand; clearing brings xbin's own back everywhere
  const B = await login(browser, 'dev1', 'devpass123');
  const rb = await B.ctx.request.put(`${URL}/api/xbin/branding`, { data: { title: 'nope' } });
  check(rb.status() === 403, `a user may not brand (${rb.status()})`);
  await closeCtx(B.ctx, B.page);
  await put({ title: '', icon: '' });
  await waitFor(A.page, () => document.querySelector('link[rel="icon"]')?.getAttribute('href') === '/vendor/favicon.svg', null, { timeout: 10000, label: 'cleared: the favicon is back' });
  h = await hdr();
  check(h.logo === 'xbin' && lockupOK(h.lockup, 24) && !h.img && !h.word && h.chip === 'workspace' && h.icon === '/vendor/favicon.svg', `cleared: xbin's own again (${JSON.stringify(h)})`);
  await closeCtx(A.ctx, A.page);
  done();
}

module.exports = { branding };
