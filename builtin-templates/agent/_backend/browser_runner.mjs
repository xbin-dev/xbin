// browser_runner.mjs — browser_check's runner (browser_check.go embeds it and
// writes it into the sandbox): load one page in a headless Chromium with
// Playwright and print what happened as ONE line of JSON after a marker.
// Raw facts only — console, errors, failed requests, an accessibility
// snapshot, screenshots at the moments asked for, a script's return value.
// Nothing here interprets the page; the agent's backend adds the words.
//
//   node browser_runner.mjs '<json>'
//     {url, wait_ms, screenshots_ms: [ms…], out_dir, viewport: {width, height},
//      script, budget_ms, nav_timeout_ms}
//
// Plain ES module, no dependencies but Playwright, found at run time: the
// project's own (node_modules up from the working directory), then the
// global installs (the xbin rootfs: npm install -g playwright under
// /usr/local/node). Browsers: $PLAYWRIGHT_BROWSERS_PATH, else
// /usr/local/ms-playwright when it exists (the rootfs puts them there).
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';

const MARK = '@@XBIN_BROWSER_CHECK@@';
const CAP = { console: 60, text: 600, errors: 20, requests: 60, snapshot: 9000, script: 16000 };

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
  emit({ ok: false, error: 'bad-request', detail: 'the runner could not read its arguments: ' + e.message }, 2);
  process.exit();
}

if (!process.env.PLAYWRIGHT_BROWSERS_PATH && fs.existsSync('/usr/local/ms-playwright')) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = '/usr/local/ms-playwright';
}

// findPlaywright: the project's, then the global installs.
function findPlaywright() {
  const tried = [];
  const bases = [];
  if (process.env.XBIN_PLAYWRIGHT) bases.push(process.env.XBIN_PLAYWRIGHT);
  bases.push(path.join(process.cwd(), 'package.json'));
  const prefix = path.dirname(path.dirname(process.execPath));
  for (const d of [path.join(prefix, 'lib', 'node_modules'), '/usr/local/node/lib/node_modules',
    '/usr/local/lib/node_modules', '/usr/lib/node_modules', ...(process.env.NODE_PATH || '').split(':')]) {
    if (d) bases.push(path.join(d, 'package.json'));
  }
  for (const b of bases) {
    for (const name of ['playwright', 'playwright-core']) {
      try {
        const r = createRequire(b.endsWith('.json') ? b : path.join(b, 'package.json'));
        const p = r.resolve(name);
        return { mod: r(name), where: p };
      } catch { tried.push(path.dirname(b)); }
    }
  }
  return { tried: [...new Set(tried)] };
}

const found = findPlaywright();
if (!found.mod) {
  emit({ ok: false, error: 'playwright-missing', node: process.version,
    detail: 'no playwright module in the project or the global installs (' + found.tried.join(', ') + ')' }, 3);
  process.exit();
}
const { chromium } = found.mod;

const t0 = Date.now();
const since = () => Date.now() - t0;
const out = {
  ok: true, node: process.version, playwright: found.where,
  url: '', title: '', status: null, load: {},
  console: [], console_dropped: 0,
  page_errors: [], requests_failed: [], requests_dropped: 0,
  snapshot: '', screenshots: [],
};
let browser;

// The budget is the backend's: past it, report what there is and stop.
const budget = Math.max(5000, Math.min(Number(req.budget_ms) || 60000, 110000));
const watchdog = setTimeout(async () => {
  out.timed_out = true;
  out.note = `the check hit its time budget (${budget} ms); what it saw until then is reported`;
  emit(out, 0);
  try { await browser?.close(); } catch { /* going anyway */ }
  process.exit();
}, budget);

try {
  try {
    browser = await chromium.launch({ headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  } catch (e) {
    clearTimeout(watchdog);
    emit({ ok: false, error: 'browser-missing', node: process.version, playwright: found.where,
      detail: clip(String(e.message || e).split('\n').filter(Boolean).slice(0, 6).join('\n'), 1200) }, 4);
    process.exit();
  }
  const vp = req.viewport || {};
  const context = await browser.newContext({
    viewport: { width: vp.width || 1280, height: vp.height || 800 },
    ignoreHTTPSErrors: true,
  });
  const page = await context.newPage();

  page.on('console', (m) => {
    if (out.console.length >= CAP.console) { out.console_dropped++; return; }
    const loc = m.location() || {};
    out.console.push({
      type: m.type(), text: clip(m.text(), CAP.text),
      location: loc.url ? `${loc.url}:${(loc.lineNumber ?? 0) + 1}:${(loc.columnNumber ?? 0) + 1}` : '',
      at_ms: since(),
    });
  });
  page.on('pageerror', (e) => {
    if (out.page_errors.length >= CAP.errors) return;
    out.page_errors.push({
      message: clip(e.message || String(e), CAP.text),
      stack: clip(String(e.stack || '').split('\n').slice(1, 5).map((l) => l.trim()).join('\n'), 800),
      at_ms: since(),
    });
  });
  const failed = (r) => {
    if (out.requests_failed.length >= CAP.requests) { out.requests_dropped++; return false; }
    out.requests_failed.push(r);
    return true;
  };
  page.on('requestfailed', (r) => {
    failed({ url: clip(r.url(), 400), method: r.method(), resource: r.resourceType(),
      error: (r.failure() || {}).errorText || 'failed', at_ms: since() });
  });
  page.on('response', (r) => {
    if (r.status() >= 400) {
      failed({ url: clip(r.url(), 400), method: r.request().method(), resource: r.request().resourceType(),
        status: r.status(), at_ms: since() });
    }
  });

  const navTimeout = Math.max(1000, Math.min(Number(req.nav_timeout_ms) || 20000, 60000));
  let resp = null;
  const navStart = since();
  try {
    resp = await page.goto(req.url, { waitUntil: 'load', timeout: navTimeout });
    out.load = { state: 'load', ms: since() - navStart };
  } catch (e) {
    out.load = { state: 'error', ms: since() - navStart, error: clip(String(e.message || e).split('\n')[0], 400) };
  }
  out.status = resp ? resp.status() : null;

  // Screenshots at their moments after the load (or after its failure), then
  // the rest of the wait.
  const loaded = Date.now();
  const wait = Math.max(0, Math.min(Number(req.wait_ms) || 0, 30000));
  const moments = [...new Set((req.screenshots_ms || []).map((n) => Math.max(0, Math.min(Number(n) || 0, 30000))))]
    .sort((a, b) => a - b).slice(0, 6);
  if (req.out_dir) fs.mkdirSync(req.out_dir, { recursive: true });
  for (const [i, ms] of moments.entries()) {
    const d = loaded + ms - Date.now();
    if (d > 0) await page.waitForTimeout(d);
    const file = path.join(req.out_dir || '.', `shot-${i + 1}-${ms}ms.png`);
    try {
      await page.screenshot({ path: file, timeout: 10000 });
      out.screenshots.push({ at_ms: ms, path: file, bytes: fs.statSync(file).size });
    } catch (e) {
      out.screenshots.push({ at_ms: ms, error: clip(String(e.message || e).split('\n')[0], 300) });
    }
  }
  const rest = loaded + wait - Date.now();
  if (rest > 0) await page.waitForTimeout(rest);

  if (typeof req.script === 'string' && req.script.trim()) {
    const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
    try {
      const fn = new AsyncFunction('page', req.script);
      let timer;
      const v = await Promise.race([fn(page), new Promise((_, rej) => {
        timer = setTimeout(() => rej(new Error('the script took over 15 s')), 15000);
      })]).finally(() => clearTimeout(timer));
      let s = JSON.stringify(v === undefined ? null : v);
      if (s === undefined) s = 'null';
      out.script = s.length > CAP.script ? { error: `its return value is ${s.length} bytes of JSON, over ${CAP.script}` } : { value: JSON.parse(s) };
    } catch (e) {
      out.script = { error: clip(String(e.message || e), CAP.text), stack: clip(String(e.stack || '').split('\n').slice(1, 4).map((l) => l.trim()).join('\n'), 600) };
    }
  }

  out.url = page.url();
  try { out.title = clip(await page.title(), 300); } catch { /* navigated away */ }
  try {
    const snap = await page.locator('body').ariaSnapshot({ timeout: 5000 });
    out.snapshot = pruneSnapshot(snap);
  } catch (e) {
    try {
      if (page.accessibility) out.snapshot = pruneSnapshot(axText(await page.accessibility.snapshot({ interestingOnly: true })));
      else out.snapshot_error = clip(String(e.message || e).split('\n')[0], 300);
    } catch (e2) {
      out.snapshot_error = clip(String(e2.message || e2).split('\n')[0], 300);
    }
  }
  clearTimeout(watchdog);
  emit(out, 0);
} catch (e) {
  clearTimeout(watchdog);
  out.ok = false;
  out.error = 'runner-failed';
  out.detail = clip(String(e.stack || e.message || e), 1500);
  emit(out, 5);
} finally {
  try { await browser?.close(); } catch { /* done anyway */ }
  process.exit(); // whatever the page left pending, the answer is out
}

// pruneSnapshot keeps the tree readable within the cap: long text lines are
// clipped, and past the cap the rest is counted, not sent.
function pruneSnapshot(s) {
  const lines = String(s || '').split('\n').map((l) => clip(l, 240));
  let n = 0;
  const kept = [];
  for (const l of lines) {
    if (n + l.length + 1 > CAP.snapshot) {
      kept.push(`… (${lines.length - kept.length} more lines)`);
      break;
    }
    kept.push(l);
    n += l.length + 1;
  }
  return kept.join('\n');
}

// axText renders the older accessibility.snapshot() tree like an aria snapshot.
function axText(node, depth = 0, acc = []) {
  if (!node) return acc.join('\n');
  const name = node.name ? ` "${clip(node.name, 120)}"` : '';
  acc.push(`${'  '.repeat(depth)}- ${node.role}${name}${node.value ? ': ' + clip(node.value, 80) : ''}`);
  for (const c of node.children || []) axText(c, depth + 1, acc);
  return acc.join('\n');
}
