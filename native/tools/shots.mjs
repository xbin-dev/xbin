#!/usr/bin/env node
// native/tools/shots.mjs — screenshots of native trees drawn by the Lit
// reference renderer (web/xb/render.js), the web half of the fixture
// contact sheet (native/AGENTS.md). Each tree is drawn at 390×844 (device
// scale 2) in light and dark, at the default and the large (iOS xxxLarge)
// text size, as <name>-<light|dark>-<default|large>.png.
//
//   PLAYWRIGHT_DIR=~/lcad-wasm node native/tools/shots.mjs [options]
//
// Trees: native/fixtures/*/expected.json by default; when there are none,
// the eight trees printed in the design (plans/native.md §18). Options:
//   --fixtures <dir>    another fixtures directory (<name>/expected.json)
//   --trees <path>…     tree JSON files or directories of them (<name>.json)
//   --design            the §18 trees, as well
//   --only <substr>     only trees whose name contains it (repeatable)
//   --out <dir>         where the PNGs go (default $TMPDIR/xb-shots)
//   --schemes light,dark   --texts default,large   --size 390x844
//   --full              also <name>-…-full.png: the view grown to its content
//   --sheet             also sheet.png: one row per tree (the four variants)
//
// No build step and no server needed: a small static server maps /vendor/
// to web/ (as xbind does) and the page is web/xb/fixture.html.
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { readFileSync, readdirSync, existsSync, statSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, extname, basename, resolve } from 'node:path';
import { tmpdir, homedir } from 'node:os';

const ROOT = resolve(new URL('../..', import.meta.url).pathname);
const WEB = join(ROOT, 'web');

function args(argv) {
  const o = { trees: [], only: [], schemes: ['light', 'dark'], texts: ['default', 'large'], size: [390, 844] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => { if (i + 1 >= argv.length) throw new Error(`${a} needs a value`); return argv[++i]; };
    if (a === '--fixtures') o.fixtures = next();
    else if (a === '--trees') { while (argv[i + 1] && !argv[i + 1].startsWith('--')) o.trees.push(argv[++i]); }
    else if (a === '--design') o.design = true;
    else if (a === '--only') o.only.push(next());
    else if (a === '--out') o.out = next();
    else if (a === '--schemes') o.schemes = next().split(',');
    else if (a === '--texts') o.texts = next().split(',');
    else if (a === '--size') o.size = next().split('x').map(Number);
    else if (a === '--full') o.full = true;
    else if (a === '--sheet') o.sheet = true;
    else if (a === '-h' || a === '--help') { console.log(readFileSync(new URL(import.meta.url), 'utf8').split('\nimport')[0]); process.exit(0); }
    else throw new Error(`unknown option ${a}`);
  }
  return o;
}

// designTrees(): {name: tree} from the §18 examples of plans/native.md.
export function designTrees() {
  const md = readFileSync(join(ROOT, 'plans/native.md'), 'utf8');
  const out = {};
  for (const part of md.split(/^### 18\.\d+ /m).slice(1)) {
    const name = part.split(/\s/)[0];
    const json = part.match(/```json\n([\s\S]*?)```/);
    if (json) out[name] = JSON.parse(json[1]);
  }
  return out;
}

function collect(o) {
  const trees = {};
  const fx = o.fixtures || join(ROOT, 'native/fixtures');
  if (existsSync(fx)) {
    for (const d of readdirSync(fx).sort()) {
      const f = join(fx, d, 'expected.json');
      if (existsSync(f)) trees[d] = JSON.parse(readFileSync(f, 'utf8'));
    }
  }
  for (const t of o.trees) {
    const files = statSync(t).isDirectory() ? readdirSync(t).filter((f) => f.endsWith('.json')).sort().map((f) => join(t, f)) : [t];
    for (const f of files) trees[basename(f, '.json')] = JSON.parse(readFileSync(f, 'utf8'));
  }
  if (o.design || Object.keys(trees).length === 0) Object.assign(trees, designTrees());
  for (const k of Object.keys(trees)) if (o.only.length && !o.only.some((s) => k.includes(s))) delete trees[k];
  return trees;
}

const TYPES = { '.js': 'text/javascript', '.mjs': 'text/javascript', '.html': 'text/html', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png' };
function serve() {
  const srv = createServer((req, res) => {
    const path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
    if (path.startsWith('/vendor/') && !path.includes('..')) {
      const name = path.slice('/vendor/'.length);
      for (const f of [join(WEB, name), join(WEB, 'vendor', name)]) {
        if (existsSync(f) && statSync(f).isFile()) {
          res.writeHead(200, { 'Content-Type': TYPES[extname(f)] || 'application/octet-stream', 'Cache-Control': 'no-cache' });
          res.end(readFileSync(f));
          return;
        }
      }
    }
    res.writeHead(404); res.end('not found');
  });
  return new Promise((r) => srv.listen(0, '127.0.0.1', () => r(srv)));
}

function playwright() {
  const dir = process.env.PLAYWRIGHT_DIR || join(homedir(), 'lcad-wasm');
  try { return createRequire(join(dir, 'package.json'))('playwright'); } catch (e) {
    throw new Error(`playwright not found under ${dir} (set PLAYWRIGHT_DIR): ${e.message}`);
  }
}

async function main() {
  const o = args(process.argv.slice(2));
  const trees = collect(o);
  const names = Object.keys(trees);
  if (!names.length) throw new Error('no trees to draw');
  const out = resolve(o.out || join(tmpdir(), 'xb-shots'));
  mkdirSync(out, { recursive: true });
  const srv = await serve();
  const base = `http://127.0.0.1:${srv.address().port}`;
  const { chromium } = playwright();
  const browser = await chromium.launch();
  const [W, H] = o.size;
  const written = [];
  const problems = [];
  try {
    for (const scheme of o.schemes) {
      for (const text of o.texts) {
        const ctx = await browser.newContext({ viewport: { width: W, height: H }, deviceScaleFactor: 2, colorScheme: scheme,
          timezoneId: 'UTC', locale: 'en-US', reducedMotion: 'reduce' });
        const page = await ctx.newPage();
        page.on('pageerror', (e) => problems.push(`${scheme}/${text}: page error: ${e.message}`));
        page.on('console', (m) => { if (m.type() === 'error') problems.push(`${scheme}/${text}: console: ${m.text()}`); });
        await page.goto(`${base}/vendor/xb/fixture.html?theme=${scheme}&text=${text === 'large' ? 'large' : ''}`);
        await page.waitForFunction(() => window.xbnFixture);
        await page.evaluate(() => window.xbnFixture.ready);
        for (const name of names) {
          await page.setViewportSize({ width: W, height: H });
          await page.evaluate((t) => window.xbnFixture.load(t), trees[name]);
          await page.evaluate(() => document.fonts.ready);
          await page.waitForTimeout(60);
          const file = join(out, `${name}-${scheme}-${text}.png`);
          await page.screenshot({ path: file, animations: 'disabled' });
          written.push(file);
          if (o.full) {
            const extra = await page.evaluate(() => {
              const root = document.querySelector('xb-view').shadowRoot;
              let more = 0;
              for (const el of root.querySelectorAll('.body, .sheet-body, xb-transcript, .loose')) {
                if (el.offsetParent !== null || el.getClientRects().length) more = Math.max(more, el.scrollHeight - el.clientHeight);
              }
              return more;
            });
            if (extra > 0) {
              await page.setViewportSize({ width: W, height: H + extra });
              await page.waitForTimeout(60);
              const ff = join(out, `${name}-${scheme}-${text}-full.png`);
              await page.screenshot({ path: ff, animations: 'disabled' });
              written.push(ff);
            }
          }
        }
        await ctx.close();
      }
    }
    if (o.sheet) {
      const rows = names.map((n) => `<div class="row"><div class="name">${n}</div>${o.schemes.flatMap((s) => o.texts.map((t) => {
        const f = `${n}-${s}-${t}.png`;
        return `<figure><img src="${f}"><figcaption>${s} · ${t}</figcaption></figure>`;
      })).join('')}</div>`).join('');
      const html = `<!doctype html><meta charset="utf-8"><style>body{margin:0;padding:16px;background:#111;color:#ccc;font:13px system-ui}
        .row{display:flex;gap:12px;align-items:flex-start;margin-bottom:20px}.name{width:120px;font-weight:600;padding-top:4px}
        figure{margin:0}img{width:${W}px;height:${H}px;display:block;border-radius:10px}figcaption{text-align:center;padding:4px}</style>${rows}`;
      writeFileSync(join(out, 'sheet.html'), html);
      const ctx = await browser.newContext({ viewport: { width: 160 + (W + 12) * o.schemes.length * o.texts.length, height: 400 } });
      const page = await ctx.newPage();
      await page.goto(`file://${join(out, 'sheet.html')}`);
      await page.waitForLoadState('load');
      await page.screenshot({ path: join(out, 'sheet.png'), fullPage: true });
      written.push(join(out, 'sheet.png'));
      await ctx.close();
    }
  } finally {
    await browser.close();
    srv.close();
  }
  for (const f of written) console.log(f);
  if (problems.length) {
    console.error(problems.join('\n'));
    process.exitCode = 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(new URL(import.meta.url).pathname)) {
  main().catch((e) => { console.error(e.stack || e.message); process.exit(1); });
}
