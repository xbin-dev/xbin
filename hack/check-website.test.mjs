// The site's guard (hack/check-website.mjs), run against copies of website/ with one
// rule broken in each: the check must pass the site as it is and name every break.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { appendFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const CHECK = join(ROOT, 'hack/check-website.mjs');

function site(t) {
  const dir = mkdtempSync(join(tmpdir(), 'xbin-site-'));
  const skip = /^\/website\/(dist|static-helpers|media)(\/|$)/;
  cpSync(join(ROOT, 'website'), dir, { recursive: true, filter: (src) => !skip.test(src.slice(ROOT.length)) });
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function check(dir, ...args) {
  const r = spawnSync(process.execPath, [CHECK, ...args], { env: { ...process.env, WEBSITE_DIR: dir }, encoding: 'utf8' });
  return { code: r.status, out: `${r.stdout}${r.stderr}` };
}

function edit(dir, file, fn) {
  const before = readFileSync(join(dir, file), 'utf8');
  const after = fn(before);
  assert.notEqual(after, before, `${file}: the edit found nothing to change (has the page moved on?)`);
  writeFileSync(join(dir, file), after);
}

function breaks(t, mutate, want, ...args) {
  const dir = site(t);
  mutate(dir);
  const r = check(dir, ...args);
  assert.equal(r.code, 1, `the check passed a broken site:\n${r.out}`);
  assert.match(r.out, want);
}

test('the site as it is passes', (t) => {
  const r = check(site(t));
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /website-guard: ok/);
});

test('install.sh must stay byte-identical', (t) => {
  breaks(t, (d) => appendFileSync(join(d, 'install.sh'), '\n'), /install\.sh is not byte-identical/);
});

test('app/ios.json must parse as the kill switch', (t) => {
  breaks(t, (d) => writeFileSync(join(d, 'app/ios.json'), '{"nativeRuntime": {'), /ios\.json does not parse/);
  breaks(t, (d) => writeFileSync(join(d, 'app/ios.json'), '{"nativeRuntime": {"disabled": "no"}}'), /kill switch needs/);
});

test('the privacy page keeps its words, its markup may change', (t) => {
  breaks(t, (d) => edit(d, 'privacy.html', (s) => s.replace('It sends no telemetry', 'It sends little telemetry')), /privacy\.html: its words changed/);
  const dir = site(t);
  edit(dir, 'privacy.html', (s) => s.replace('<h2>This website</h2>', '<h2 class="h3">This\n        website</h2>'));
  assert.equal(check(dir).code, 0);
});

test('nothing loads from another site', (t) => {
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('</head>', '<link rel="stylesheet" href="https://fonts.example.com/inter.css">\n</head>')), /loads https:\/\/fonts\.example\.com/);
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('</head>', '<link rel="preload" href="//cdn.example.com/a.woff2" as="font">\n</head>')), /loads \/\/cdn\.example\.com/);
  breaks(t, (d) => appendFileSync(join(d, 'css/site.css'), '\n.x{background:url("https://cdn.example.com/x.png")}\n'), /url\(\)\/@import loads https:\/\/cdn/);
  breaks(t, (d) => edit(d, 'index.html', (s) => s.replace('</body>', '<script src="https://stats.example.com/s.js"></script>\n</body>')), /<script> loads https:\/\/stats/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nimport x from 'https://cdn.example.com/x.js';\n"), /imports https:\/\/cdn/);
});

test('the site stores nothing', (t) => {
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nlocalStorage.setItem('seen', '1');\n"), /localStorage \(the site stores nothing/);
  breaks(t, (d) => edit(d, 'index.html', (s) => s.replace('</body>', '<script>document.cookie = "a=1";</script>\n</body>')), /inline script uses document\.cookie/);
});

test('the budgets hold per page', (t) => {
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('</body>', `<p>${'x'.repeat(30000)}</p>\n</body>`)), /404\.html: HTML and CSS \d+\.\d KB, over the 60 KB budget/);
  breaks(t, (d) => {
    mkdirSync(join(d, 'art'), { recursive: true });
    writeFileSync(join(d, 'art/big.webp'), Buffer.alloc(260000));
    edit(d, '404.html', (s) => s.replace('<main id="content">', '<main id="content"><img src="/art/big.webp" alt="" width="10" height="10">'));
  }, /404\.html: first-screen images 26\d\.\d KB, over the 250 KB budget/);
  // the same image, lazy, is not in the first screen
  const dir = site(t);
  writeFileSync(join(dir, 'art/big.webp'), Buffer.alloc(260000));
  edit(dir, '404.html', (s) => s.replace('<main id="content">', '<main id="content"><img src="/art/big.webp" alt="" width="10" height="10" loading="lazy">'));
  assert.equal(check(dir).code, 0);
});

test('every page carries the same header and footer', (t) => {
  breaks(t, (d) => edit(d, 'product.html', (s) => s.replace('<li><a href="/ios.html">iOS</a></li>', '<li><a href="/ios.html">iPhone</a></li>')), /product\.html: the shared header differs/);
  breaks(t, (d) => edit(d, 'security.html', (s) => s.replace('<h2 class="foot-h">Try</h2>', '<h2 class="foot-h">Trial</h2>')), /security\.html: the shared footer differs/);
  breaks(t, (d) => edit(d, 'index.html', (s) => s.replace('data-try href="#try"', 'data-try href="/install.html#trial"')), /index\.html: the header's Try link goes to/);
  breaks(t, (d) => edit(d, 'ios.html', (s) => s.replace('<li><a href="/install.html">Install</a></li>', '<li><a href="/install.html" aria-current="page">Install</a></li>')), /ios\.html: the header marks \/install\.html as the current page/);
});

test('images, links and colours', (t) => {
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('alt="xbin" width="51"', 'width="51"')), /<img> without alt/);
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('<li><a href="/">Home</a></li>', '<li><a href="https://example.com/">Home</a></li>')), /links to https:\/\/example\.com\/, which is not one of the site's allowed links/);
  breaks(t, (d) => appendFileSync(join(d, 'css/site.css'), '\n.x{color:#ff0000}\n'), /a hex colour outside tokens\.css/);
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('<main id="content">', '<main id="content" style="background:#fff">')), /hex colour in a style attribute/);
});

test("the site's marks are the brand's masters", (t) => {
  breaks(t, (d) => edit(d, 'img/mark.svg', (s) => s.replace('#FFD000', '#FFE000')), /img\/mark\.svg is not plans\/brand\/marks\/mark\.svg/);
  breaks(t, (d) => edit(d, 'img/wordmark.svg', (s) => s.replace('<title id="title">xbin</title>', '<title id="title">XBIN</title>')), /wordmark\.svg is neither/);
  // swapping in wordmark B is one file
  const dir = site(t);
  writeFileSync(join(dir, 'img/wordmark.svg'), readFileSync(join(ROOT, 'plans/brand/marks/wordmark-b.svg')));
  assert.equal(check(dir).code, 0);
});

test('media over 1 MiB is locked by sha256', (t) => {
  const line = `${'a'.repeat(64)}  film/F-1-day.mp4  hack/demo set/09, Concrete Day\n`;
  breaks(t, (d) => appendFileSync(join(d, 'media.lock'), 'not a lock line\n'), /media\.lock:\d+: want/);
  // without --dist (the guard) a locked file need not be here; with it, it must, and match
  const dir = site(t);
  appendFileSync(join(dir, 'media.lock'), line);
  assert.equal(check(dir).code, 0);
  let r = check(dir, '--dist');
  assert.equal(r.code, 1);
  assert.match(r.out, /media\/film\/F-1-day\.mp4 is missing/);
  mkdirSync(join(dir, 'media/film'), { recursive: true });
  writeFileSync(join(dir, 'media/film/F-1-day.mp4'), 'not the film');
  r = check(dir, '--dist');
  assert.match(r.out, /F-1-day\.mp4: its sha256 is not the one/);
});
