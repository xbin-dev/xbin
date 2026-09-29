// live-policy.mjs — real-browser verification of the live preview's frame
// (preview_port, D135): a HOSTILE page served from a sandbox, with scripts
// running, must get nothing of the workspace.
//
// A local server plays the parts: the shell (the workspace origin, a
// session cookie), the agent tile's page (in a frame sandboxed as xbind
// frames tiles), and the live route under a path ticket — answering with
// the headers the agent backend sets (liveCSP, read from
// _backend/sandbox_ports.go) — plus the credentialed APIs a hostile page
// would reach for (/api/xbin/whoami, the tile's API), which answer only a
// session cookie or a frame token, as xbind does. The live frame is built
// by live.js's own liveFrame(). The page then tries: document.cookie,
// localStorage, fetch whoami and the tile API with credentials, navigate
// top, open a window, and postMessage its parent; it reports what it saw
// on its own prefix. Opened top-level, the CSP header alone must confine
// it the same way.
//
//   node test/live-policy.mjs      (needs playwright + chromium; PLAYWRIGHT_DIR)
//
// Skips cleanly when playwright is unavailable.
import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));

let chromium;
for (const p of ['/usr/local/node/lib/node_modules/playwright/index.mjs', 'playwright',
  process.env.PLAYWRIGHT_DIR ? join(process.env.PLAYWRIGHT_DIR, 'node_modules/playwright/index.mjs') : '']) {
  if (!p) continue;
  try { ({ chromium } = await import(p)); break; } catch { /* next */ }
}
if (!chromium) {
  console.log('SKIP: playwright not installed (npm i -g playwright && playwright install chromium; or PLAYWRIGHT_DIR)');
  process.exit(0);
}

const goSrc = readFileSync(join(here, '..', '_backend', 'sandbox_ports.go'), 'utf8');
const cspM = goSrc.match(/const liveCSP = "([^"]+)"/);
if (!cspM) {
  console.error('FAIL: liveCSP not found in _backend/sandbox_ports.go — update this test');
  process.exit(1);
}
const LIVE_CSP = cspM[1];
const liveJS = readFileSync(join(here, '..', 'live.js'), 'utf8');

let failures = 0;
const ok = (name, cond, extra = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'}  ${name}${cond ? '' : '  ← ' + extra}`);
  if (!cond) failures++;
};

const TICKET = '/api/~p1.test-ticket/';
const SESSION = 'xbin_session=SECRET-SESSION';
const reports = [];
const liveCookies = [];
const escaped = [];

// The hostile page: every reach it has, reported on its own prefix.
const HOSTILE = `<!doctype html><html><head><title>hostile</title></head><body>
<h1 id="h">live page</h1>
<script>
(async () => {
  const r = { ran: true, origin: String(self.origin) };
  try { r.cookie = document.cookie; } catch (e) { r.cookie = 'threw:' + e.name; }
  try { document.cookie = 'planted=1'; r.cookieAfter = document.cookie; } catch (e) { r.cookieAfter = 'threw:' + e.name; }
  try { localStorage.setItem('x', '1'); r.storage = 'usable'; } catch (e) { r.storage = 'threw:' + e.name; }
  try { sessionStorage.setItem('x', '1'); r.session = 'usable'; } catch (e) { r.session = 'threw:' + e.name; }
  for (const [k, u] of [['whoami', '/api/xbin/whoami'], ['tileApi', '/api/apps/agent/runs/5/view']]) {
    try {
      const res = await fetch(u, { credentials: 'include' });
      r[k] = res.status + ':' + (await res.text()).slice(0, 80);
    } catch (e) { r[k] = 'threw:' + e.name; }
  }
  try { r.parentDoc = String(parent.document.title); } catch (e) { r.parentDoc = 'threw:' + e.name; }
  try { r.topDoc = String(top.document.title); } catch (e) { r.topDoc = 'threw:' + e.name; }
  try { top.location.href = 'https://evil.example/top'; r.topNav = 'no throw'; } catch (e) { r.topNav = 'threw:' + e.name; }
  try { r.popup = String(window.open('https://evil.example/pop') === null ? 'null' : 'opened'); } catch (e) { r.popup = 'threw:' + e.name; }
  try { parent.postMessage({ op: 'close' }, '*'); top.postMessage({ op: 'navigate', to: 'x' }, '*'); r.posted = true; } catch (e) { r.posted = 'threw:' + e.name; }
  document.getElementById('h').textContent = 'scripts ran';
  await fetch('report?d=' + encodeURIComponent(JSON.stringify(r)));
})();
</script></body></html>`;

const TILE = `<!doctype html><html><head><title>agent tile</title></head><body>
<div id="pane"></div>
<script type="module">
${liveJS.replace(/^import .*$/gm, '').replace(/^export \{[^}]*\};$/gm, '').replace(/^export /gm, '')}
window.__msgs = 0;
window.addEventListener('message', () => { window.__msgs++; });
document.getElementById('pane').appendChild(liveFrame(${JSON.stringify(TICKET + 'index.html')}));
</script></body></html>`;

const SHELL = `<!doctype html><html><head><title>shell</title></head><body>
<iframe id="tile" sandbox="allow-scripts allow-forms allow-modals allow-downloads" style="width:800px;height:600px"
  src="/c/apps/agent/index.html"></iframe></body></html>`;

const server = createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  const cookie = req.headers.cookie || '';
  const authed = cookie.includes(SESSION) || !!req.headers['x-xbin-frame-token'];
  if (req.headers.origin === 'null') res.setHeader('Access-Control-Allow-Origin', 'null'); // xbind's nullOriginCORS
  if (u.pathname === '/') {
    res.setHeader('Set-Cookie', SESSION + '; Path=/; HttpOnly; SameSite=Lax');
    res.setHeader('Content-Type', 'text/html');
    return res.end(SHELL);
  }
  if (u.pathname === '/c/apps/agent/index.html') {
    res.setHeader('Content-Security-Policy', "sandbox allow-scripts allow-forms allow-modals allow-downloads; frame-ancestors 'self'");
    res.setHeader('Content-Type', 'text/html');
    return res.end(TILE);
  }
  if (u.pathname.startsWith(TICKET)) {
    liveCookies.push(cookie);
    res.setHeader('Content-Security-Policy', LIVE_CSP); // the agent backend's headers (liveHeaders)
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    if (u.pathname.endsWith('/report')) {
      reports.push(JSON.parse(u.searchParams.get('d')));
      return res.end('ok');
    }
    res.setHeader('Content-Type', 'text/html');
    return res.end(HOSTILE);
  }
  if (u.pathname === '/api/xbin/whoami' || u.pathname.startsWith('/api/apps/agent/')) {
    res.statusCode = authed ? 200 : 401;
    return res.end(authed ? '{"user":"alice"}' : 'unauthorized');
  }
  res.statusCode = 404;
  res.end('nope');
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${server.address().port}`;

const browser = await chromium.launch();
const ctx = await browser.newContext();
await ctx.route('**/*', (route) => {
  const url = route.request().url();
  if (url.includes('evil.example')) { escaped.push(url); return route.abort(); }
  return route.continue();
});
const page = await ctx.newPage();
page.on('console', (m) => { if (process.env.LIVE_DEBUG) console.log('console:', m.text()); });
const popups = [];
ctx.on('page', (p) => popups.push(p.url()));

await page.goto(origin + '/');
const deadline = Date.now() + 10000;
while (!reports.length && Date.now() < deadline) await page.waitForTimeout(100);
const r = reports[0] || {};
ok('the live page loaded and its scripts ran', r.ran === true, JSON.stringify(r));
ok('it is an opaque origin', r.origin === 'null', r.origin);
ok('document.cookie is empty', r.cookie === '' || String(r.cookie).startsWith('threw:'), r.cookie);
ok('a cookie can\'t be set', r.cookieAfter === '' || String(r.cookieAfter).startsWith('threw:'), r.cookieAfter);
ok('localStorage is unavailable', String(r.storage).startsWith('threw:'), r.storage);
ok('sessionStorage is unavailable', String(r.session).startsWith('threw:'), r.session);
ok('fetch /api/xbin/whoami gets no identity', !String(r.whoami).includes('alice'), r.whoami);
ok('fetch of the tile API gets no identity', !String(r.tileApi).includes('alice'), r.tileApi);
ok('the parent\'s document is out of reach', String(r.parentDoc).startsWith('threw:'), r.parentDoc);
ok('the top document is out of reach', String(r.topDoc).startsWith('threw:'), r.topDoc);
await page.waitForTimeout(500);
ok('top navigation is blocked', page.url() === origin + '/' && !escaped.some((u) => u.includes('/top')), page.url() + ' ' + JSON.stringify(escaped));
ok('no pop-up opened', r.popup === 'null' && popups.length === 0, r.popup + ' ' + JSON.stringify(popups));
ok('the viewer\'s session cookie never reached the live route', liveCookies.length > 0 && liveCookies.every((c) => !c.includes('SECRET')), JSON.stringify(liveCookies));
const tile = page.frames().find((f) => f.url().includes('/c/apps/agent/'));
const shellTitle = await page.title();
ok('a postMessage to its parent changes nothing on the page', shellTitle === 'shell' && !!tile, shellTitle);
ok('the pane keeps the live frame (a message closes nothing)', tile ? await tile.evaluate(() => !!document.getElementById('livefr')) : false);
const fr = tile ? await tile.evaluate(() => { const f = document.getElementById('livefr'); return { sandbox: f.getAttribute('sandbox'), ref: f.getAttribute('referrerpolicy') }; }) : {};
ok('the live frame is sandboxed allow-scripts allow-forms (never allow-same-origin)', fr.sandbox === 'allow-scripts allow-forms', JSON.stringify(fr));
ok('the live frame sends no referrer', fr.ref === 'no-referrer', JSON.stringify(fr));

// Opened top-level (a copied link): the CSP header alone confines it.
reports.length = 0;
const direct = await ctx.newPage();
await direct.goto(origin + TICKET + 'index.html');
const d2 = Date.now() + 10000;
while (!reports.length && Date.now() < d2) await direct.waitForTimeout(100);
const t = reports[0] || {};
ok('opened directly: still an opaque origin', t.origin === 'null', JSON.stringify(t));
ok('opened directly: no cookie, no storage, no identity', (t.cookie === '' || String(t.cookie).startsWith('threw:')) &&
  String(t.storage).startsWith('threw:') && !String(t.whoami).includes('alice') && !String(t.tileApi).includes('alice'), JSON.stringify(t));

await browser.close();
server.close();
console.log(failures ? `\n${failures} FAILURE(S)` : '\nall live-policy checks passed');
process.exit(failures ? 1 : 0);
