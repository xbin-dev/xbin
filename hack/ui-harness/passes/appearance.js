// hack/ui-harness/passes/appearance.js — the theme mechanism in a real
// browser (D184; docs/frontend-kit.md §Theme). Three probe tiles: one that
// opts in (<html data-bx-theme="auto"> + theme.css), sandboxed; one that
// links theme.css without opting in (every third-party tile today); one that
// opts in as approved chrome. As the admin:
//   - no flash: an init script records --bx-scheme (and --bx-row) at the
//     first animation frame of every document; for the person's choice
//     system / light / dark (the shell bucket's `theme` key) × a light and a
//     dark system, a sandboxed frame, a chrome frame and a top-level tab of
//     the opted-in probe are right from that frame, the shell too once it
//     opts in, and the probe that didn't opt in stays Night;
//   - density: a person's comfortable is there at the first frame (32 px
//     rows, 14 px text), compact (28 px) otherwise;
//   - fonts: the sandboxed frame draws in Instrument Sans and JetBrains
//     Mono, fetched cross-origin from /vendor/fonts/ (whatever
//     TILE_ASSETS mode the harness runs);
//   - the hint cookie: the shell leaves xbin_theme equal to the choice
//     (none for system);
//   - the live relay: an xbin:appearance from the frame's parent restyles
//     it at once; one from a sibling frame doesn't; once the shell opts in
//     (and <bx-frame> relays), its own setAppearance() reaches every frame;
//   - a second tab follows a change made in the first, through the prefs
//     event (once the shell follows);
//   - terminals follow the shell's theme, live (once bx-terminal does).
// The parts the branch can't exercise yet SKIP, saying which change brings
// them. The admin's theme and density are restored at the end.
const path = require('path');
const { URL, OUT, fs, sleep, log, login, closeCtx, fr, openShell, usePersonalScreen, openTile, closeTile, checker } = require('../lib');

const WS = process.env.WS;
const PROBE = 'apps/themeprobe', OLD = 'apps/themeprobe-old', CHROME = 'apps/themechrome';
const page = (auto, title) => `<!doctype html><html lang="en"${auto ? ' data-bx-theme="auto"' : ''}><head><meta charset="utf-8">
<title>${title}</title>
<link rel="stylesheet" href="/vendor/theme.css">
</head><body class="bx">
<h1 id="h" style="font: var(--bx-font-heading)">${title}</h1>
<p id="t">The person's theme, at the first frame.</p>
<code id="m" style="font: var(--bx-font-code)">--bx-scheme</code>
<button id="b" class="primary">Primary</button>
</body></html>
`;
const FILES = {
  [`${PROBE}/xbin.json`]: '{"title":"theme probe"}\n',
  [`${PROBE}/index.html`]: page(true, 'theme probe'),
  [`${OLD}/xbin.json`]: '{"title":"theme probe, not opted in"}\n',
  [`${OLD}/index.html`]: page(false, 'not opted in'),
  [`${CHROME}/xbin.json`]: '{"title":"theme probe, chrome","chrome":true}\n',
  [`${CHROME}/index.html`]: page(true, 'chrome probe'),
};

// In every document, at its first animation frame (a render-blocking
// stylesheet holds that frame until it has loaded): what it would paint.
// Kept on the document, not the window: a same-origin frame's first
// document (about:blank) hands its window on to the page that replaces it.
function firstFrame() {
  const doc = document;
  requestAnimationFrame(() => {
    if (doc !== document || !doc.documentElement) return; // replaced since
    const cs = getComputedStyle(doc.documentElement);
    doc.bxFirst = {
      scheme: cs.getPropertyValue('--bx-scheme').trim() || 'none',
      row: cs.getPropertyValue('--bx-row').trim() || 'none',
      bg: doc.body ? getComputedStyle(doc.body).backgroundColor : '',
    };
  });
}
const now = (f) => f.evaluate(() => {
  const cs = getComputedStyle(document.documentElement);
  return { scheme: cs.getPropertyValue('--bx-scheme').trim() || 'none', row: cs.getPropertyValue('--bx-row').trim(), bg: getComputedStyle(document.body).backgroundColor };
});
// first(frameOf, label): the record of the frame's current document. A
// function, not a frame: the shell may replace a card's iframe while it
// settles (a re-render), so the frame is found again on every try.
async function first(frameOf, label) {
  const deadline = Date.now() + 15000;
  for (;;) {
    const f = await frameOf().catch(() => null);
    const v = f && await f.evaluate(() => (location.href !== 'about:blank' && document.bxFirst) || null).catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) {
      const box = f && await f.frameElement().then((h) => h.boundingBox()).catch(() => null);
      throw new Error(`no first-frame record in ${label} (${f?.url()}; frame box ${JSON.stringify(box)})`);
    }
    await sleep(50);
  }
}
// shown(page, tile): the tile's frame on screen — a card the layout keeps on
// another screen tab is in the DOM too, never rendered
async function shown(page, tile) {
  for (const f of page.frames()) {
    if (!f.url().includes(`/c/${tile}/`)) continue;
    if (await f.frameElement().then((h) => h.isVisible()).catch(() => false)) return f;
  }
  return null;
}
async function until(fn, label, timeout = 10000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > deadline) throw new Error(`timed out: ${label}`);
    await sleep(100);
  }
}

function seed() {
  for (const [rel, body] of Object.entries(FILES)) {
    const p = path.join(WS, rel);
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, body);
  }
}

async function appearance(browser) {
  const { check, skip, done } = checker('appearance');
  check(!!WS, 'WS is set (run.sh exports it)');
  seed();
  const A = await login(browser, 'admin', 'admin');
  const api = (method, p, data, headers = {}) => A.ctx.request.fetch(`${URL}/api/xbin${p}`, {
    method, headers: { 'Content-Type': 'application/json', ...headers }, ...(data === undefined ? {} : { data: JSON.stringify(data) }),
  });
  const choose = async (theme, density) => {
    await (theme === 'system' ? api('DELETE', '/prefs/theme') : api('PUT', '/prefs/theme', theme));
    await (density === 'compact' ? api('DELETE', '/prefs/density') : api('PUT', '/prefs/density', density));
  };
  await until(async () => (await (await api('GET', '/components')).json()).filter((c) => [PROBE, OLD, CHROME].includes(c.path)).length === 3, 'the probes registered', 20000);
  const ch = await api('PUT', '/chrome', { path: CHROME, approved: true });
  check(ch.status() === 200, `${CHROME} approved as chrome (${ch.status()})`);
  const bxTerm = await (await A.ctx.request.get(`${URL}/vendor/bx-terminal.js`)).text();
  const termFollows = bxTerm.includes('bx-theme.js');
  let shellFollows = false;

  try {
    // ---- first paint: choice × system, every kind of document ----
    for (const [theme, density] of [['system', 'compact'], ['light', 'comfortable'], ['dark', 'compact']]) {
      await choose(theme, density);
      for (const sys of ['light', 'dark']) {
        const want = theme === 'system' ? sys : theme;
        const row = density === 'comfortable' ? '32px' : '28px';
        const tag = `${theme} on a ${sys} system`;
        const P = await login(browser, 'admin', 'admin', { colorScheme: sys, viewport: { width: 1440, height: 900 } });
        await P.ctx.addInitScript(firstFrame);
        try {
          // a top-level tab of the opted-in probe, and of the one that didn't
          await P.page.goto(`${URL}/c/${PROBE}/`);
          let f0 = await first(async () => P.page.mainFrame(), `${PROBE} (tab)`);
          check(f0.scheme === want && f0.row === row, `${tag}: a tab of the opted-in tile is ${want}, rows ${row}, from its first frame (${JSON.stringify(f0)})`);
          check((await now(P.page.mainFrame())).bg === f0.bg, `${tag}: …and its background never changes after it (${f0.bg})`);
          await P.page.goto(`${URL}/c/${OLD}/`);
          f0 = await first(async () => P.page.mainFrame(), `${OLD} (tab)`);
          check(f0.scheme === 'dark' && f0.row === '28px', `${tag}: a tile that links theme.css without opting in stays Night, compact (${JSON.stringify(f0)})`);

          // the shell, and the probes framed in it
          await openShell(P.page);
          shellFollows = await P.page.evaluate(() => document.documentElement.getAttribute('data-bx-theme') === 'auto');
          const s0 = await first(async () => P.page.mainFrame(), 'the shell');
          if (shellFollows) check(s0.scheme === want, `${tag}: the shell is ${want} from its first frame (${s0.scheme})`);
          else check(s0.scheme === 'dark', `${tag}: the shell that hasn't opted in stays Night (${s0.scheme})`);
          const hint = async () => (await P.ctx.cookies(URL)).find((c) => c.name === 'xbin_theme');
          await until(async () => (theme === 'system' ? !(await hint()) : (await hint())?.value === theme), 'the hint cookie', 5000).catch(() => {});
          const cookie = await hint();
          check(theme === 'system' ? !cookie : cookie?.value === theme && cookie.sameSite === 'Lax' && !cookie.httpOnly,
            `${tag}: the shell left the hint cookie ${theme === 'system' ? 'unset' : `xbin_theme=${theme}`} (${cookie ? `${cookie.value}, ${cookie.sameSite}${cookie.httpOnly ? ', HttpOnly' : ''}` : 'none'})`);
          await usePersonalScreen(P.page);
          for (const [tile, wantHere, what] of [[PROBE, want, 'a sandboxed frame'], [CHROME, want, 'a chrome frame'], [OLD, 'dark', 'a frame that didn\'t opt in']]) {
            await openTile(P.page, tile);
            // in view: Chromium doesn't render an off-screen cross-origin frame, so
            // its first frame is the one painted when it comes into view
            await P.page.locator(`.card[data-path="${tile}"]`).first().scrollIntoViewIfNeeded();
            const v = await first(() => shown(P.page, tile), tile);
            const wantRow = tile === OLD ? '28px' : row;
            check(v.scheme === wantHere && v.row === wantRow, `${tag}: ${what} (${tile}) is ${wantHere}, rows ${wantRow}, from its first frame (${JSON.stringify(v)})`);
          }
          if (theme === 'system') {
            // fonts: the sandboxed frame fetched them cross-origin and draws in them
            const f = await shown(P.page, PROBE);
            const fonts = await f.evaluate(async () => {
              await document.fonts.ready;
              const loaded = [...document.fonts].filter((x) => x.status === 'loaded').map((x) => `${x.family.replace(/"/g, '')} ${x.weight}`);
              return { loaded, t: getComputedStyle(document.getElementById('t')).fontFamily, m: getComputedStyle(document.getElementById('m')).fontFamily, origin: location.origin };
            });
            check(fonts.loaded.includes('Instrument Sans 400') && fonts.loaded.some((x) => x.startsWith('JetBrains Mono')) && fonts.loaded.some((x) => x.startsWith('Bricolage Grotesque')),
              `${tag}: the sandboxed frame (origin ${fonts.origin}) loaded the fonts (${fonts.loaded.join(', ')})`);
            check(/^"?Instrument Sans"?,/.test(fonts.t) && /^"?JetBrains Mono"?,/.test(fonts.m), `${tag}: …and draws in them (${fonts.t.split(',')[0]}; ${fonts.m.split(',')[0]})`);
            await P.page.screenshot({ path: `${OUT}/appearance-${sys}.png` });
          }
          for (const tile of [PROBE, CHROME, OLD]) await closeTile(P.page, tile);
        } finally {
          await closeCtx(P.ctx, P.page);
        }
      }
    }

    // ---- live: the relay into frames ----
    await choose('system', 'compact');
    const L = await login(browser, 'admin', 'admin', { colorScheme: 'dark', viewport: { width: 1440, height: 900 } });
    try {
      await openShell(L.page);
      await usePersonalScreen(L.page);
      for (const tile of [PROBE, OLD]) await openTile(L.page, tile);
      const probe = await until(() => shown(L.page, PROBE), 'the probe on screen'), old = await until(() => shown(L.page, OLD), 'the old probe on screen');
      await until(async () => (await now(probe)).scheme === 'dark', 'the probe is dark on a dark system');
      // the parent's message (what <bx-frame> posts on every load and change)
      const post = (tile, data) => L.page.locator(`.card[data-path="${tile}"] bx-frame iframe`).first()
        .evaluate((iframe, d) => iframe.contentWindow.postMessage(d, '*'), data);
      await post(PROBE, { type: 'xbin:appearance', theme: 'light', density: 'comfortable' });
      const v = await until(async () => { const x = await now(probe); return x.scheme === 'light' && x.row === '32px' && x; }, 'the probe follows its parent');
      check(v.scheme === 'light' && v.row === '32px', `live: the frame applies xbin:appearance from its parent at once (${JSON.stringify(v)})`);
      await post(OLD, { type: 'xbin:appearance', theme: 'light', density: 'comfortable' });
      await sleep(300); // a negative: nothing may change
      check((await now(old)).scheme === 'dark', 'live: a frame that didn\'t opt in hears it and stays Night');
      // anything but the parent posting to the probe — the frame itself, a
      // sibling (another tile's frame): ignored
      await probe.evaluate(() => window.postMessage({ type: 'xbin:appearance', theme: 'dark', density: 'compact' }, '*'));
      const siblings = await old.evaluate(() => {
        let n = 0;
        for (let i = 0; i < parent.frames.length; i++) if (parent.frames[i] !== window) { parent.frames[i].postMessage({ type: 'xbin:appearance', theme: 'dark', density: 'compact' }, '*'); n++; }
        return n;
      });
      await sleep(300); // a negative: nothing may change
      check((await now(probe)).scheme === 'light', `live: an xbin:appearance from the frame itself is ignored${siblings ? `, and from ${siblings} sibling frame(s)` : ' (no sibling could post: frames in shadow roots aren\'t in window.frames)'}`);
      await L.page.screenshot({ path: `${OUT}/appearance-live.png` });
      await post(PROBE, { type: 'xbin:appearance', theme: 'system', density: 'compact' });
      await until(async () => (await now(probe)).scheme === 'dark', 'the probe back to the system');

      if (shellFollows) {
        // the shell's own change reaches every frame (<bx-frame> relays)
        await L.page.evaluate(() => import('/vendor/bx-theme.js').then((m) => m.setAppearance({ theme: 'light', density: 'comfortable' })));
        const r = await until(async () => { const x = await now(probe); return x.scheme === 'light' && x.row === '32px' && x; }, 'the relay reached the probe');
        check(!!r, `live: the shell's setAppearance() reaches its frames (${JSON.stringify(r)})`);
        check((await now(L.page.mainFrame())).scheme === 'light', 'live: …and the shell itself');
        await L.page.evaluate(() => import('/vendor/bx-theme.js').then((m) => m.setAppearance({ theme: 'system', density: 'compact' })));
      } else {
        skip('live: the shell\'s own change reaching its frames — the shell opts in and <bx-frame> relays with P2');
      }

      // terminals follow the theme
      if (termFollows && shellFollows) {
        await fr(L.page, PROBE, (f) => { f.open('term'); if (!f.tabs.length) f.newTerm(); });
        const termBg = () => L.page.locator(`bx-frame[src="${PROBE}"] bx-terminal .xterm-viewport`).first()
          .evaluate((vp) => getComputedStyle(vp).backgroundColor);
        await until(async () => (await termBg()) === 'rgb(11, 12, 18)', 'a dark terminal');
        await L.page.evaluate(() => import('/vendor/bx-theme.js').then((m) => m.setAppearance({ theme: 'light' })));
        const bg = await until(async () => { const x = await termBg(); return x === 'rgb(255, 255, 255)' && x; }, 'the terminal follows to light', 10000).catch(() => '');
        check(bg === 'rgb(255, 255, 255)', `live: an open terminal follows the theme (${bg || await termBg()})`);
        await L.page.evaluate(() => import('/vendor/bx-theme.js').then((m) => m.setAppearance({ theme: 'system' })));
        await fr(L.page, PROBE, (f) => { for (let i = f.tabs.length - 1; i >= 0; i--) f.closeTab(i); f.closeTerminal(); }).catch(() => {});
      } else {
        skip(`terminals following the theme — ${termFollows ? 'needs the shell to follow (P2)' : 'bx-terminal builds its theme from the tokens with P3'}`);
      }
      for (const tile of [PROBE, OLD]) await closeTile(L.page, tile);
    } finally {
      await closeCtx(L.ctx, L.page);
    }

    // ---- a second tab follows a change made in the first ----
    if (shellFollows) {
      const B = await login(browser, 'admin', 'admin', { colorScheme: 'dark' });
      try {
        await openShell(B.page);
        await api('PUT', '/prefs/theme', 'light', { 'X-Prefs-Writer': 'harness-other-tab' });
        const m = await until(() => B.page.evaluate(() => document.querySelector('head > meta[name="xbin-theme"]')?.content === 'light' && getComputedStyle(document.documentElement).getPropertyValue('--bx-scheme').trim()), 'the second tab follows', 15000).catch(() => '');
        check(m === 'light', `a second tab follows the person's change through the prefs event (${m || 'no'})`);
      } finally {
        await closeCtx(B.ctx, B.page);
      }
    } else {
      skip('a second tab following through the prefs event — the shell listens for it with P2');
    }
  } finally {
    await choose('system', 'compact');
    await closeCtx(A.ctx, A.page);
  }
  log(`appearance: shell follows: ${shellFollows}; terminals follow: ${termFollows}`);
  done();
}

module.exports = { appearance };
