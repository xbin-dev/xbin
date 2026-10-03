// The site's guard (hack/check-website.mjs), run against copies of website/ with one
// rule broken in each: the check must pass the site as it is and name every break.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const CHECK = join(ROOT, 'hack/check-website.mjs');

function site(t) {
  const top = mkdtempSync(join(tmpdir(), 'xbin-site-'));
  const dir = join(top, 'website');
  const skip = /^\/website\/(dist|static-helpers|media)(\/|$)/;
  cpSync(join(ROOT, 'website'), dir, { recursive: true, filter: (src) => !skip.test(src.slice(ROOT.length)) });
  t.after(() => rmSync(top, { recursive: true, force: true }));
  return dir;
}

// --dist also checks the prebuilt helpers hack/helpers.sha256 lists, which a copy of the
// site does not have: unless a test says otherwise, it gets a manifest with none.
function check(dir, ...args) {
  const manifest = join(dir, '..', `${dir.split('/').pop()}.helpers.sha256`);
  if (!existsSync(manifest)) writeFileSync(manifest, '# no prebuilt helpers\n');
  const env = { ...process.env, WEBSITE_DIR: dir, HELPERS_MANIFEST: manifest };
  const r = spawnSync(process.execPath, [CHECK, ...args], { env, encoding: 'utf8' });
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
  // a script that reaches another host by any API names its URL
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nnavigator.sendBeacon('https://plausible.io/api/event', '{}');\n"), /copy\.js:\d+: names https:\/\/plausible\.io\/api\/event/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), '\nfetch("//stats.example.com/hit");\n'), /names \/\/stats\.example\.com\/hit/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), '\nnew Image().src = `https://px.example.com/p.gif`;\n'), /names https:\/\/px\.example\.com\/p\.gif/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nnew WebSocket('wss://live.example.com/');\n"), /names wss:\/\/live\.example\.com/);
  breaks(t, (d) => edit(d, 'index.html', (s) => s.replace('</body>', '<script>fetch("https://stats.example.com/e")</script>\n</body>')), /an inline script names https:\/\/stats\.example\.com\/e/);
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('<li><a href="/">Home</a></li>', '<li><a href="/" ping="https://stats.example.com/ping">Home</a></li>')), /a ping attribute \(https:\/\/stats\.example\.com\/ping\) reports clicks/);
  breaks(t, (d) => edit(d, '404.html', (s) => s.replace('</head>', '<link rel="prerender" href="https://example.com/">\n</head>')), /<link rel="prerender"> loads https:\/\/example\.com\//);
});

test('the site stores nothing', (t) => {
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nlocalStorage.setItem('seen', '1');\n"), /localStorage \(the site stores nothing/);
  breaks(t, (d) => edit(d, 'index.html', (s) => s.replace('</body>', '<script>document.cookie = "a=1";</script>\n</body>')), /inline script uses document\.cookie/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\ncookieStore.set('seen', '1');\n"), /cookieStore \(the site stores nothing/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\ncaches.open('v1');\n"), /caches \(the site stores nothing/);
  breaks(t, (d) => appendFileSync(join(d, 'js/copy.js'), "\nnavigator.serviceWorker.register('/sw.js');\n"), /serviceWorker \(the site stores nothing/);
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
  // wordmark B is wider than A: the file alone squeezes it into A's sizes
  const wordmarkB = (d) => writeFileSync(join(d, 'img/wordmark.svg'), readFileSync(join(ROOT, 'plans/brand/marks/wordmark-b.svg')));
  breaks(t, wordmarkB, /404\.html: the wordmark <img> is 51 × 20 \(2\.55:1\), but img\/wordmark\.svg is 3\.49:1/);
  // with B's sizes (website/README.md → "Assets") on every page and the share card, it passes
  const dir = site(t);
  wordmarkB(dir);
  for (const f of ['404.html', 'index.html', 'install.html', 'ios.html', 'privacy.html', 'product.html', 'security.html', 'og.html']) {
    edit(dir, f, (s) => s.replaceAll('alt="xbin" width="51" height="20"', 'alt="xbin" width="73" height="21"')
      .replaceAll('alt="xbin" width="61" height="24"', 'alt="xbin" width="105" height="30"')
      .replaceAll('alt="xbin" width="112" height="44"', 'alt="xbin" width="154" height="44"'));
  }
  const r = check(dir);
  assert.equal(r.code, 0, r.out);
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

test('what make website deploys carries no slot, waiting shot or stub', (t) => {
  // the site as it is deploys
  const ok = check(site(t), '--dist');
  assert.equal(ok.code, 0, ok.out);
  const slot = (d) => edit(d, 'index.html', (s) => s.replace('<span class="fig-num">121M</span>', '<span class="fig-num"><mark data-todo="data">{{DATA: growth since the inflection}}</mark></span>'));
  const shot = (d) => edit(d, 'product.html', (s) => s.replace('<!-- S-2 (workspace canvas, cobalt mat) waits for the product theme -->', '<figure class="shot"><div class="mat mat-shell"><div class="ph" aria-hidden="true"><mark data-todo="shot">S-2</mark></div></div></figure>'));
  const stub = (d) => edit(d, 'ios.html', (s) => s.replace('<main id="content">', '<main id="content" data-todo="page">'));
  // the guard (make guards) counts them and passes
  for (const open of [slot, shot, stub]) {
    const dir = site(t);
    open(dir);
    const r = check(dir);
    assert.equal(r.code, 0, r.out);
    assert.match(r.out, /open: /);
  }
  // make website refuses each one
  breaks(t, slot, /index\.html: 1 \{\{DATA\}\} slot\(s\) left/, '--dist');
  breaks(t, shot, /product\.html: 1 shot\(s\) waiting for their capture \(S-2\)/, '--dist');
  breaks(t, stub, /ios\.html: still a stub page/, '--dist');
});

test('make website ships every prebuilt helper set the manifest lists', (t) => {
  const tarball = Buffer.from('a staged helper set');
  const sum = createHash('sha256').update(tarball).digest('hex');
  const withManifest = (d) => writeFileSync(join(d, '..', 'website.helpers.sha256'), [
    '# <group> <key> <arch> <file> <sha256>',
    `vm 8dabfd34a9fb amd64 amd64.tar.zst ${sum}`,
    `vm 8dabfd34a9fb amd64 vmlinux ${'b'.repeat(64)}`,
    '',
  ].join('\n'));
  const stage = (d, bytes) => {
    mkdirSync(join(d, 'static-helpers/vm/8dabfd34a9fb'), { recursive: true });
    writeFileSync(join(d, 'static-helpers/vm/8dabfd34a9fb/amd64.tar.zst'), bytes);
  };
  // the guard does not look; make website does, by each tarball's sha256 (a file listed
  // inside a tarball is not staged on its own)
  let dir = site(t);
  withManifest(dir);
  assert.equal(check(dir).code, 0);
  breaks(t, withManifest, /static-helpers\/vm\/8dabfd34a9fb\/amd64\.tar\.zst is missing: .*helpers\.sha256:2 lists it/, '--dist');
  breaks(t, (d) => { withManifest(d); stage(d, Buffer.from('another set')); }, /amd64\.tar\.zst: its sha256 is not the one .*helpers\.sha256:2 pins/, '--dist');
  dir = site(t);
  withManifest(dir);
  stage(dir, tarball);
  const r = check(dir, '--dist');
  assert.equal(r.code, 0, r.out);
});
