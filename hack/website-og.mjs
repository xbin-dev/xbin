// hack/website-og.mjs — render the share card: website/og.html to website/og.png at
// 1200 × 630, light, over file:// (`make website-og`). Playwright comes from
// $PLAYWRIGHT_DIR (hack/dev-setup.sh writes it into .dev.mk). pngquant and oxipng,
// when installed, then shrink the PNG.
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dir = process.env.PLAYWRIGHT_DIR;
if (!dir) {
  console.error('website-og: set PLAYWRIGHT_DIR to a directory with node_modules/playwright (hack/dev-setup.sh)');
  process.exit(1);
}
const { chromium } = createRequire(join(dir, 'node_modules', '/'))('playwright');
const out = join(root, 'website/og.png');

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1, colorScheme: 'light' });
  const failed = [];
  page.on('requestfailed', (r) => failed.push(r.url()));
  await page.goto(`file://${join(root, 'website/og.html')}`, { waitUntil: 'load' });
  await page.evaluate(() => document.fonts.ready);
  if (failed.length) throw new Error(`og.html: failed to load ${failed.join(', ')}`);
  await page.screenshot({ path: out });
} finally {
  await browser.close();
}

const run = (cmd, args) => spawnSync(cmd, args, { stdio: 'inherit' }).status === 0;
if (!run('pngquant', ['--quality', '85-98', '--speed', '1', '--strip', '--force', '--output', out, out])) console.error('website-og: pngquant missing or failed; og.png left unquantized');
if (!run('oxipng', ['-q', '-o', '4', '--strip', 'safe', out])) console.error('website-og: oxipng missing or failed');
console.log('>> website/og.png');
