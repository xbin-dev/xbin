// runner.mjs — the preview browser's driver (preview.go embeds it and runs it
// with node in a throwaway confine sandbox whose network namespace is empty).
//
//   node runner.mjs '<json>'
//     {url, sock, port, viewport:{width,height}, wait_ms, budget_ms,
//      full_page, map_all, spki, probe}
//
// The sandbox has no network but its own loopback. The one way out is the
// unix socket `sock` (bound in read-only), which xbind serves for this
// render alone: this script listens on 127.0.0.1:<port> inside the sandbox
// and pipes every connection to it, bytes untouched. Chromium then opens the
// workspace's own URL: on a loopback name (127.0.0.1, *.localhost — a dev
// workspace) as it is; any other name through `map_all`, a resolver rule
// sending every host to the forwarder, with xbind ending TLS on the socket
// under a throwaway key pinned by `spki`.
//
// Output: ONE line of JSON after the marker on stdout — the picture as
// base64 PNG, a text snapshot of the page and of each frame in it, console
// errors, failed requests, the final URL. Nothing is written to disk, and
// the script holds no credential: xbind sets the session on the socket's
// requests itself.
import { createRequire } from 'node:module';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

const MARK = '@@XBIN_PREVIEW@@';
const CAP = { console: 40, text: 500, errors: 20, requests: 40, snapshot: 12000, frames: 24 };

let emitted = false;
function emit(obj, code) {
  if (!emitted) {
    emitted = true;
    process.stdout.write('\n' + MARK + JSON.stringify(obj) + '\n');
  }
  process.exitCode = code;
}
const clip = (s, n) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };

let req;
try {
  req = JSON.parse(process.argv[2] || '{}');
} catch (e) {
  emit({ ok: false, error: 'bad-request', detail: e.message }, 2);
  process.exit();
}

// probe: what the sandbox can reach, for the test that it reaches nothing
// (no browser). Each target is dialled once with a short timeout.
if (req.probe) {
  const dial = (host, port) => new Promise((res) => {
    const c = net.connect({ host, port, timeout: 1500 });
    c.on('connect', () => { c.destroy(); res('connected'); });
    c.on('timeout', () => { c.destroy(); res('timeout'); });
    c.on('error', (e) => res(e.code || 'error'));
  });
  const out = { ok: true, interfaces: Object.keys(os.networkInterfaces()), dials: {} };
  for (const t of req.probe) out.dials[t] = await dial(...((i) => [t.slice(0, i), Number(t.slice(i + 1))])(t.lastIndexOf(':')));
  emit({ ok: false, error: 'probe', detail: JSON.stringify(out) }, 0);
  process.exit();
}

if (!process.env.PLAYWRIGHT_BROWSERS_PATH && fs.existsSync('/usr/local/ms-playwright')) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = '/usr/local/ms-playwright';
}
function findPlaywright() {
  const prefix = path.dirname(path.dirname(process.execPath));
  for (const d of [path.join(prefix, 'lib', 'node_modules'), '/usr/local/node/lib/node_modules', '/usr/local/lib/node_modules', '/usr/lib/node_modules']) {
    for (const name of ['playwright', 'playwright-core']) {
      try { return createRequire(path.join(d, 'package.json'))(name); } catch { /* next */ }
    }
  }
  return null;
}
const pw = findPlaywright();
if (!pw) {
  emit({ ok: false, error: 'playwright-missing', detail: 'no playwright module in the rootfs' }, 3);
  process.exit();
}

// The forwarder: loopback in here → xbind's socket for this render.
function forward(host) {
  return new Promise((res) => {
    const s = net.createServer((c) => {
      const u = net.connect(req.sock);
      c.on('error', () => u.destroy());
      u.on('error', () => c.destroy());
      c.pipe(u);
      u.pipe(c);
    });
    s.on('error', () => res(false));
    s.listen(req.port, host, () => res(true));
  });
}
if (!(await forward('127.0.0.1'))) {
  emit({ ok: false, error: 'forwarder', detail: 'could not listen on the sandbox loopback' }, 4);
  process.exit();
}
await forward('::1');

const t0 = Date.now();
const since = () => Date.now() - t0;
const out = {
  ok: true, url: '', title: '', status: null, console: [], console_dropped: 0, page_errors: [],
  requests_failed: [], requests_dropped: 0, snapshot: '', frames: [], png: '', timing: {},
};
let browser;
const budget = Math.max(5000, Math.min(Number(req.budget_ms) || 30000, 110000));
const watchdog = setTimeout(async () => {
  out.timed_out = true;
  emit(out, 0);
  try { await browser?.close(); } catch { /* going anyway */ }
  process.exit();
}, budget);

try {
  const args = [
    '--no-sandbox', // Chromium's own sandbox is off (whether it can nest in here is untested): the confine sandbox is the boundary
    '--disable-dev-shm-usage', '--disable-gpu', '--no-first-run', '--mute-audio',
    '--disable-background-networking', '--disable-component-update', '--disable-sync',
    '--disable-default-apps', '--disable-extensions', '--no-default-browser-check',
    '--disable-features=Translate,MediaRouter,OptimizationHints,AutofillServerCommunication',
  ];
  if (req.map_all) args.push(`--host-resolver-rules=MAP * 127.0.0.1:${req.port}`);
  if (req.spki) args.push(`--ignore-certificate-errors-spki-list=${req.spki}`);
  try {
    browser = await pw.chromium.launch({ headless: true, args });
  } catch (e) {
    clearTimeout(watchdog);
    emit({ ok: false, error: 'browser-missing', detail: clip(String(e.message || e).split('\n').filter(Boolean).slice(0, 6).join('\n'), 1200) }, 4);
    process.exit();
  }
  out.timing.launch_ms = since();
  const vp = req.viewport || {};
  const context = await browser.newContext({
    viewport: { width: vp.width || 1280, height: vp.height || 800 },
    deviceScaleFactor: 1,
    serviceWorkers: 'block',
  });
  const page = await context.newPage();

  page.on('console', (m) => {
    if (m.type() !== 'error' && m.type() !== 'warning') return;
    if (out.console.length >= CAP.console) { out.console_dropped++; return; }
    out.console.push({ type: m.type(), text: clip(m.text(), CAP.text), at_ms: since() });
  });
  page.on('pageerror', (e) => {
    if (out.page_errors.length < CAP.errors) out.page_errors.push({ message: clip(e.message || String(e), CAP.text), at_ms: since() });
  });
  const failed = (r) => {
    if (out.requests_failed.length >= CAP.requests) { out.requests_dropped++; return; }
    out.requests_failed.push(r);
  };
  page.on('requestfailed', (r) => failed({ url: clip(r.url(), 300), method: r.method(), error: (r.failure() || {}).errorText || 'failed', at_ms: since() }));
  page.on('response', (r) => {
    if (r.status() >= 400) failed({ url: clip(r.url(), 300), method: r.request().method(), status: r.status(), at_ms: since() });
  });

  const navStart = since();
  let resp = null;
  try {
    resp = await page.goto(req.url, { waitUntil: 'load', timeout: Math.min(20000, budget - 3000) });
  } catch (e) {
    out.nav_error = clip(String(e.message || e).split('\n')[0], 300);
  }
  out.status = resp ? resp.status() : null;
  out.timing.load_ms = since() - navStart;

  // Settling. Nothing in the shell or a tile says "ready" today, so: the
  // network quiet for 500 ms, then every frame loaded and the set of frames
  // unchanged across a quiet period, then the caller's wait.
  const left = () => budget - since() - 2500;
  const quiet = async (ms) => { try { await page.waitForLoadState('networkidle', { timeout: Math.max(1, Math.min(ms, left())) }); } catch { /* still busy: go on */ } };
  await quiet(6000);
  for (let i = 0, last = -1; i < 6 && left() > 0; i++) {
    const fr = page.frames();
    await Promise.all(fr.map((f) => f.waitForLoadState('load', { timeout: Math.max(1, Math.min(4000, left())) }).catch(() => {})));
    if (fr.length === last) break;
    last = fr.length;
    await page.waitForTimeout(250);
    await quiet(2500);
  }
  const wait = Math.max(0, Math.min(Number(req.wait_ms) || 0, 15000, left()));
  if (wait > 0) await page.waitForTimeout(wait);
  out.timing.settle_ms = since() - navStart - out.timing.load_ms;

  out.url = page.url();
  try { out.title = clip(await page.title(), 200); } catch { /* navigated away */ }
  const snap = async (f) => {
    try { return prune(await f.locator('body').ariaSnapshot({ timeout: 4000 })); } catch (e) { return '(no snapshot: ' + clip(String(e.message || e).split('\n')[0], 120) + ')'; }
  };
  out.snapshot = await snap(page.mainFrame());
  let room = CAP.snapshot - out.snapshot.length;
  for (const f of page.frames().slice(0, CAP.frames + 1)) {
    if (f === page.mainFrame()) continue;
    let text = room > 200 ? await snap(f) : '(left out: the snapshot is at its size cap)';
    if (text.length > room && room > 200) text = text.slice(0, room) + '\n… (cut)';
    room -= text.length;
    out.frames.push({ url: clip(f.url(), 300), snapshot: text });
  }
  const shotStart = since();
  try {
    const buf = await page.screenshot({ type: 'png', fullPage: !!req.full_page, timeout: 10000 });
    out.png = buf.toString('base64');
  } catch (e) {
    out.png_error = clip(String(e.message || e).split('\n')[0], 300);
  }
  out.timing.shot_ms = since() - shotStart;
  out.timing.total_ms = since();
  try { // a measurement only: resident and proportional memory of everything in the sandbox
    const sum = (file, re) => fs.readdirSync('/proc').filter((d) => /^\d+$/.test(d)).reduce((n, d) => {
      try { return n + Number((re.exec(fs.readFileSync(`/proc/${d}/${file}`, 'utf8')) || [0, 0])[1]); } catch { return n; }
    }, 0);
    out.rss_kb = sum('status', /VmRSS:\s+(\d+)/);
    out.pss_kb = sum('smaps_rollup', /Pss:\s+(\d+)/);
    out.procs = fs.readdirSync('/proc').filter((d) => /^\d+$/.test(d)).length;
  } catch { /* none */ }
  clearTimeout(watchdog);
  emit(out, 0);
} catch (e) {
  clearTimeout(watchdog);
  out.ok = false;
  out.error = 'runner-failed';
  out.detail = clip(String(e.stack || e.message || e), 1200);
  emit(out, 5);
} finally {
  try { await browser?.close(); } catch { /* done anyway */ }
  process.exit();
}

function prune(s) {
  const lines = String(s || '').split('\n').map((l) => clip(l, 240));
  let n = 0;
  const kept = [];
  for (const l of lines) {
    if (n + l.length + 1 > CAP.snapshot) { kept.push(`… (${lines.length - kept.length} more lines)`); break; }
    kept.push(l);
    n += l.length + 1;
  }
  return kept.join('\n');
}
