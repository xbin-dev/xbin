// hack/ui-harness/passes/termtheme.js — the terminal follows the person's
// theme (D184, A2): with no palette picked, the screen is the workspace's
// (the --bx-term-* tokens in force), in the --bx-mono face at the density's
// size; an open terminal repaints when the appearance changes (the person's
// choice through bx-theme.js, then the system's light/dark); a palette the
// person picks stays theirs whatever the theme. The appearance steps need a
// shell that follows the theme (<html data-bx-theme="auto">): without it
// they SKIP, said so.
const { URL, login, closeCtx, settle, fr, waitSel, openShell, openTile, shot, checker } = require('../lib');

const TILE = 'apps/crawler';
const NIGHT_BG = '#0b0c12', DAY_BG = '#ffffff'; // --bx-term-bg, theme.css

async function termTheme(browser) {
  const { check, skip, done } = checker('term-theme');
  const { ctx, page } = await login(browser, 'admin', 'admin', { colorScheme: 'dark' });
  // a fresh start: no palette or size picked in this browser, and none of
  // the crawler sessions earlier passes left (restored as tabs, D73)
  await page.evaluate(() => { localStorage.removeItem('bx-term-theme'); localStorage.removeItem('bx-term-fontsize'); });
  for (const s of await (await ctx.request.get(`${URL}/api/xbin/term/sessions?cwd=apps%2Fcrawler`)).json()) await ctx.request.delete(`${URL}/ws/term?session=${encodeURIComponent(s.id)}`);
  await openShell(page);
  await openTile(page, TILE);
  await page.locator(`.card[data-path="${TILE}"]`).evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await fr(page, TILE, (f) => f.open('term'));
  await fr(page, TILE, (f) => { if (!f.tabs.length) f.newTerm(); });
  const termSel = `bx-frame[src="${TILE}"] bx-terminal`;
  await waitSel(page, `${termSel} textarea`, { timeout: 20000 });
  const term = page.locator(termSel).first();

  // what the screen is painted with (xterm's theme, face and size, and its
  // viewport's background) beside the tokens in force at the element
  const look = () => term.evaluate((el) => {
    const l = el.testApi().look, cs = getComputedStyle(el), tok = (n) => cs.getPropertyValue(n).trim().toLowerCase();
    const rgb = (v) => { const p = document.createElement('i'); p.style.color = v; el.shadowRoot.appendChild(p); const c = getComputedStyle(p).color; p.remove(); return c; };
    const vp = el.shadowRoot.querySelector('.xterm-viewport');
    return l && {
      name: l.name, bg: String(l.theme.background).toLowerCase(), fg: String(l.theme.foreground).toLowerCase(), cursor: String(l.theme.cursor).toLowerCase(),
      blue: String(l.theme.blue).toLowerCase(), fontFamily: l.fontFamily, fontSize: l.fontSize,
      want: { bg: tok('--bx-term-bg'), fg: tok('--bx-term-fg'), cursor: tok('--bx-term-cursor'), blue: tok('--bx-term-blue') },
      painted: vp ? getComputedStyle(vp).backgroundColor === rgb(l.theme.background) : false,
      scheme: tok('--bx-scheme'), mono: cs.getPropertyValue('--bx-mono').trim(), size: parseFloat(tok('--bx-term-size')),
    };
  });
  const isWorkspace = (l) => !!l && l.bg === l.want.bg && l.fg === l.want.fg && l.cursor === l.want.cursor && l.blue === l.want.blue && l.painted;
  // poll until fn(look) holds (a change repaints on the next frame)
  const until = async (fn, label, timeout = 5000) => {
    const t0 = Date.now();
    for (;;) {
      const l = await look();
      if (fn(l)) return l;
      if (Date.now() - t0 > timeout) { check(false, `timed out waiting for ${label} (${JSON.stringify(l)})`); return l; }
      await page.waitForTimeout(50);
    }
  };
  const appearance = (theme) => page.evaluate(async (t) => (await import('/vendor/bx-theme.js')).setAppearance({ theme: t }), theme);

  // 1. nothing picked: the workspace's palette, face and size
  let l = await until((x) => isWorkspace(x), 'the workspace palette');
  check(l.name === 'default' && isWorkspace(l), `with no palette picked, the screen is the workspace's: the --bx-term-* tokens in force (${JSON.stringify(l)})`);
  const firstFace = (l.mono.split(',')[0] || '').replace(/["']/g, '').trim();
  check(!!firstFace && l.fontFamily.includes(firstFace), `the face is the --bx-mono token (${JSON.stringify({ fontFamily: l.fontFamily, mono: l.mono })})`);
  check(l.fontSize === l.size, `the size is the density's --bx-term-size (${l.fontSize} = ${l.size})`);
  const term$ = (fn) => term.evaluate(fn);
  check(await term$((el) => el.shadowRoot.querySelector('.gear svg') !== null && !/[\u{1F300}-\u{1FAFF}⚙]/u.test(el.shadowRoot.querySelector('.gear').textContent)),
    'the settings button is the settings glyph, no emoji');
  await term$((el) => el.shadowRoot.querySelector('.gear').click());
  await settle(page);
  const opts = await term.locator('select.theme option').evaluateAll((os) => os.map((o) => [o.value, o.textContent]));
  check(opts[0]?.[0] === 'default' && /^Workspace \(follows the theme\)$/.test(opts[0]?.[1]) && opts.some(([v, t]) => v === 'concrete-night' && t === 'Concrete Night') && opts.some(([v, t]) => v === 'concrete-day' && t === 'Concrete Day'),
    `the menu offers the workspace's palette first, then Concrete Night and Day (${JSON.stringify(opts.slice(0, 4))})`);
  await page.keyboard.press('Escape');
  await term$((el) => { const g = el.shadowRoot.querySelector('.gear'); if (g.classList.contains('open')) g.click(); });

  const follows = await page.evaluate(() => document.documentElement.getAttribute('data-bx-theme') === 'auto');
  if (!follows) {
    skip('the shell does not follow the person\'s theme (no data-bx-theme="auto" on its document): the appearance steps need it');
  } else {
    // 2. the person's choice, with the terminal open: it repaints in Day, then back
    check(l.scheme === 'dark' && l.bg === NIGHT_BG, `a dark system: Concrete Night (${l.scheme}, ${l.bg})`);
    await appearance('light');
    l = await until((x) => x?.scheme === 'light' && isWorkspace(x), 'the repaint in Day');
    check(l.scheme === 'light' && l.bg === DAY_BG && isWorkspace(l), `choosing light repaints the open terminal in Concrete Day (${l.bg}, painted ${l.painted})`);
    await shot(page, 'term-theme-day', { fullPage: false });
    await appearance('system');
    l = await until((x) => x?.scheme === 'dark' && isWorkspace(x), 'the repaint in Night');
    check(l.bg === NIGHT_BG && isWorkspace(l), `back to the system's (dark): Concrete Night again (${l.bg})`);
    // 3. the system's light/dark, while following it
    await page.emulateMedia({ colorScheme: 'light' });
    l = await until((x) => x?.scheme === 'light' && isWorkspace(x), 'the system turning light');
    check(l.bg === DAY_BG && isWorkspace(l), `the system turning light repaints it (${l.bg})`);
    await page.emulateMedia({ colorScheme: 'dark' });
    l = await until((x) => x?.scheme === 'dark' && isWorkspace(x), 'the system turning dark');
    check(l.bg === NIGHT_BG, `and dark again (${l.bg})`);
    // 4. a palette the person picked stays theirs whatever the theme
    await term$((el) => el.shadowRoot.querySelector('.gear').click());
    await settle(page);
    await term.locator('select.theme').selectOption('concrete-night');
    await term$((el) => el.shadowRoot.querySelector('.gear').click());
    await appearance('light');
    l = await until((x) => x?.scheme === 'light', 'the shell in Day');
    await page.waitForTimeout(200);
    l = await look();
    check(l.name === 'concrete-night' && l.bg === NIGHT_BG && l.painted, `a picked palette stays: Concrete Night in a light shell (${l.name}, ${l.bg})`);
    await shot(page, 'term-theme-picked', { fullPage: false });
    await term$((el) => el.shadowRoot.querySelector('.gear').click());
    await settle(page);
    await term.locator('select.theme').selectOption('default');
    await term$((el) => el.shadowRoot.querySelector('.gear').click());
    l = await until((x) => x?.name === 'default' && isWorkspace(x), 'the workspace palette again');
    check(l.bg === DAY_BG && isWorkspace(l), `picking the workspace's again follows the theme: Concrete Day (${l.bg})`);
    await appearance('system');
  }

  // tidy: the browser's picks and the session
  await page.evaluate(() => { localStorage.removeItem('bx-term-theme'); localStorage.removeItem('bx-term-fontsize'); });
  await fr(page, TILE, (f) => { while (f?.tabs.length) f.closeTab(0); });
  await settle(page);
  await closeCtx(ctx, page);
  done();
}

module.exports = { termTheme };
