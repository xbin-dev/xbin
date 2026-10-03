// hack/website-terminal.mjs — the brand terminal for xbin.dev's terminal shots (S-4, S-5,
// S-11, S-12; site/visuals.md 1.2, plans/brand.md §15; website/README.md → "Assets").
// It replays a session hack/website-terminal-record.py recorded, byte for byte, into
// xterm.js (web/vendor/xterm.js, the emulator the product's own terminals use) set up as
// the spec says: JetBrains Mono 400 and 700 at 13 px on a 20 px line, ligatures off,
// padding 8 × 12 px, the Concrete Day or Concrete Night terminal colours. It photographs
// the content area, no window chrome, as the masters website/art/shots/<ID>-light.webp
// (Day) and <ID>-dark.webp (Night): WebP at quality 90.
//
// The window is 800 × 500 CSS px (97 × 24 cells: JetBrains Mono's 7.8 px advance lands on
// 8 px cells) at 4×, so a master is 3200 × 2000 px. The spec's 1600 × 1000 window would
// set 13 px type at 6 px on a page that shows the shot about 740 px wide; at 800 it shows
// at about 12 px.
//
//   PLAYWRIGHT_DIR=… WEBSITE_TERMINAL_FONTS=<JetBrains Mono 2.304 fonts/webfonts> \
//     node hack/website-terminal.mjs website/art/shots/S-4.session
//
// The session's .json sets "view": "bottom" (the screen as it was at the "end" mark) or
// "top" (scrolled back to the command, for a first screenful). The site's own JetBrains
// Mono is a latin subset without box drawing or ✓ → ·, so the full release's fonts are
// needed (https://github.com/JetBrains/JetBrainsMono/releases/tag/v2.304).
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const W = 800, H = 500, PAD_X = 12, PAD_Y = 8, DPR = 4;
const THEMES = {
  light: { // Concrete Day
    background: '#FFFFFF', foreground: '#1C1D24', cursor: '#1F3DFF', cursorAccent: '#FFFFFF', selectionBackground: '#DDE2FF',
    black: '#1C1D24', brightBlack: '#5A5D6C', red: '#C81E1E', brightRed: '#E0352B', green: '#00794A', brightGreen: '#00965C',
    yellow: '#8A6100', brightYellow: '#A87800', blue: '#1F3DFF', brightBlue: '#4A63FF', magenta: '#B0005C', brightMagenta: '#D4007A',
    cyan: '#0E7490', brightCyan: '#0891B2', white: '#7A7D8C', brightWhite: '#A6A9B8',
  },
  dark: { // Concrete Night
    background: '#0B0C12', foreground: '#E6E7EE', cursor: '#8C9BFF', cursorAccent: '#0B0C12', selectionBackground: '#262C5C',
    black: '#1C1D26', brightBlack: '#5C5F70', red: '#FF6B6B', brightRed: '#FF8F8F', green: '#4CD69B', brightGreen: '#7BE6B6',
    yellow: '#FFD54A', brightYellow: '#FFE27A', blue: '#6F86FF', brightBlue: '#96A6FF', magenta: '#FF5FB0', brightMagenta: '#FF8CC8',
    cyan: '#4FC3DC', brightCyan: '#85DCEC', white: '#C9CBD6', brightWhite: '#FFFFFF',
  },
};

const die = (m) => { console.error(`website-terminal: ${m}`); process.exit(2); };
const session = process.argv[2];
if (!session || !existsSync(session) || !existsSync(`${session}.json`)) die('usage: node hack/website-terminal.mjs website/art/shots/<ID>.session (with its .json)');
const fonts = process.env.WEBSITE_TERMINAL_FONTS;
const font = (f) => {
  const p = fonts && join(fonts, f);
  if (!p || !existsSync(p)) die(`set WEBSITE_TERMINAL_FONTS to JetBrains Mono's fonts/webfonts directory (${f})`);
  return readFileSync(p).toString('base64');
};
const pw = process.env.PLAYWRIGHT_DIR;
if (!pw || !existsSync(join(pw, 'node_modules/playwright'))) die('set PLAYWRIGHT_DIR (hack/dev-setup.sh writes it into .dev.mk)');
const { chromium } = createRequire(join(pw, 'node_modules', '/'))('playwright');

const bin = readFileSync(session);
const meta = JSON.parse(readFileSync(`${session}.json`, 'utf8'));
// from the end of the last screen clear before the command to the end mark
let from = bin.lastIndexOf(Buffer.from('\x1b[3J'), meta.marks.cmd);
if (from >= 0) from += 4;
else from = Math.max(0, bin.lastIndexOf(Buffer.from('\x1b[2J'), meta.marks.cmd) + 4);
const bytes = [...bin.subarray(from, meta.marks.end)];
const regular = font('JetBrainsMono-Regular.woff2');
const bold = font('JetBrainsMono-Bold.woff2');

const browser = await chromium.launch();
try {
  for (const [theme, t] of Object.entries(THEMES)) {
    const page = await browser.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: DPR });
    await page.setContent(`<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:"Brand Mono";src:url(data:font/woff2;base64,${regular}) format("woff2");font-weight:400}
@font-face{font-family:"Brand Mono";src:url(data:font/woff2;base64,${bold}) format("woff2");font-weight:700}
${readFileSync(join(ROOT, 'web/vendor/xterm.css'), 'utf8')}
html,body{margin:0;background:${t.background}}
#win{width:${W}px;height:${H}px;box-sizing:border-box;padding:${PAD_Y}px ${PAD_X}px;background:${t.background};overflow:hidden}
.xterm .xterm-viewport{overflow:hidden!important;background:${t.background}!important}
.xterm,.xterm *{font-variant-ligatures:none!important;font-feature-settings:"liga" 0,"calt" 0!important}
</style><div id="win"><div id="t"></div></div>`);
    await page.addScriptTag({ content: readFileSync(join(ROOT, 'web/vendor/xterm.js'), 'utf8') });
    const got = await page.evaluate(async ({ cols, rows, theme, data, view }) => {
      await document.fonts.load('400 13px "Brand Mono"');
      await document.fonts.load('700 13px "Brand Mono"');
      const term = new window.Terminal({
        cols, rows, theme, fontFamily: '"Brand Mono"', fontSize: 13, fontWeight: 400, fontWeightBold: 700, lineHeight: 1,
        letterSpacing: 0, cursorBlink: false, cursorStyle: 'block', scrollback: 5000, drawBoldTextInBrightColors: true,
      });
      term.open(document.getElementById('t'));
      const cell = () => term._core._renderService.dimensions.css.cell;
      for (let i = 0; i < 4 && Math.abs(cell().height - 20) > 0.01; i++) term.options.lineHeight *= 20 / cell().height; // a 20 px line
      await new Promise((r) => term.write(new Uint8Array(data), r));
      if (view === 'top') term.scrollToTop();
      term.focus();
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      return { w: cell().width, h: cell().height };
    }, { cols: meta.cols, rows: meta.rows, theme: t, data: bytes, view: meta.view || 'bottom' });
    if (Math.abs(got.h - 20) > 0.01 || got.w * meta.cols + 2 * PAD_X > W) die(`cells ${got.w} × ${got.h} do not fit ${meta.cols} × ${meta.rows} in ${W} × ${H}`);
    const png = session.replace(/\.session$/, `-${theme}.png`);
    const webp = session.replace(/\.session$/, `-${theme}.webp`);
    await page.locator('#win').screenshot({ path: png });
    await page.close();
    const py = spawnSync('python3', ['-c', 'import sys; from PIL import Image; Image.open(sys.argv[1]).convert("RGB").save(sys.argv[2], "WEBP", quality=90, method=6)', png, webp], { stdio: 'inherit' });
    rmSync(png);
    if (py.status !== 0) die('python3 with Pillow is needed to write the WebP master');
    console.log(`>> ${webp}: ${W * DPR} × ${H * DPR}, ${meta.cols} × ${meta.rows} cells, view ${meta.view || 'bottom'}`);
  }
} finally {
  await browser.close();
}
