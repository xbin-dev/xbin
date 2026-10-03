// hack/website-check.mjs — the xbin.dev site in a real browser. `make website-check`
// runs it after the site's guard (hack/check-website.sh, `make website-guard`). Every
// page at 360, 390, 768, 1024, 1440 and 1920 px wide, in light, dark and reduced
// motion, and at 320 px (WCAG's reflow width) for overflow, served the way the site is
// (root-relative URLs) by
// `python3 -m http.server 9424 --bind 127.0.0.1`, which this script starts and stops.
// A page fails on:
//
//   - a console error or an uncaught exception;
//   - a request that leaves 127.0.0.1:9424 (it is blocked, and named);
//   - horizontal overflow: the page scrolls sideways, or something runs past the
//     viewport's edge with nothing of the page's own to scroll or clip it, or a command
//     does not fit its line (checked again with every <details> open);
//   - layout shift over 0.05: the largest session window of layout-shift entries, as
//     Chrome counts CLS, while the page loads and is scrolled to its end. The fonts are
//     held until the first paint and then let in one at a time, as on a first visit
//     over a real network, so every font swap that moves the page counts, every run,
//     rather than when a race goes that way;
//   - a focusable element without a visible focus ring: every element Tab reaches
//     (and, each <details> opened alone, what it reveals) must match :focus-visible,
//     be shown and not covered, and draw an outline (or a box-shadow) at 3:1 or more
//     against the ground around it, at least 60 % of it inside the viewport and the
//     boxes that clip it;
//   - a missing image: an <img> that did not load, a request that failed or answered
//     4xx/5xx, or an <img>/<source> candidate (src, srcset) the server does not have;
//   - motion under prefers-reduced-motion: an animation that runs anyway.
//
// Playwright comes from $PLAYWRIGHT_DIR, as for `make website-og` (hack/dev-setup.sh
// writes it into .dev.mk).
//
//   node hack/website-check.mjs                    website/ as it is
//   node hack/website-check.mjs --dist             website/dist, as `make website` left it
//   node hack/website-check.mjs --page ios.html    one page (repeat the flag for more)
//   node hack/website-check.mjs --shots DIR        then full-page screenshots into DIR:
//                                                  every page at 1440 × 900 and 390 × 844,
//                                                  DPR 2, light and dark
//
// WEBSITE_DIR points it at another copy of the site, as for the guard.

import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { connect } from 'node:net';
import { availableParallelism } from 'node:os';
import { dirname, join, relative, resolve } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const HOST = '127.0.0.1';
const PORT = 9424;
const ORIGIN = `http://${HOST}:${PORT}`;

// [width, height, device pixel ratio]: two phones, a tablet either way up, a laptop, a desktop
const SIZES = [[360, 740, 3], [390, 844, 3], [768, 1024, 2], [1024, 768, 2], [1440, 900, 2], [1920, 1080, 1]];
// WCAG's reflow width (a 1280 px desktop at 400 %, the narrowest phones): every page is
// checked there for overflow only. Layout shift there waits on fallback fonts sized to
// the web fonts' metrics (Arial Black wraps a hero one line longer than Bricolage).
const REFLOW = [320, 568, 2];
const MODES = {
  light: { colorScheme: 'light', reducedMotion: 'no-preference' },
  dark: { colorScheme: 'dark', reducedMotion: 'no-preference' },
  'reduced motion': { colorScheme: 'light', reducedMotion: 'reduce' },
};
const SHOT_SIZES = [[1440, 900], [390, 844]];
const LIMITS = { cls: 0.05, ringContrast: 3, ringShown: 0.6 };

const fail = (msg) => { console.error(`website-check: ${msg}`); process.exit(2); };
const argv = process.argv.slice(2);
const opts = { dist: false, pages: [], shots: null };
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (a === '--dist') opts.dist = true;
  else if (a === '--page' && argv[i + 1]) opts.pages.push(argv[++i]);
  else if (a === '--shots' && argv[i + 1]) opts.shots = resolve(argv[++i]);
  else fail(`unknown argument ${a} (--dist, --page NAME, --shots DIR)`);
}

const SITE = resolve(process.env.WEBSITE_DIR || join(ROOT, opts.dist ? 'website/dist' : 'website'));
if (!existsSync(join(SITE, 'index.html'))) fail(`no ${relative(ROOT, SITE)}/index.html${opts.dist ? ' (make website builds it)' : ''}`);
// og.html is the share card's artwork, not a page
let pages = readdirSync(SITE).filter((f) => f.endsWith('.html') && f !== 'og.html')
  .sort((a, b) => (a === 'index.html' ? -1 : b === 'index.html' ? 1 : a.localeCompare(b)));
if (opts.pages.length) {
  for (const p of opts.pages) if (!pages.includes(p)) fail(`no page ${p} in ${relative(ROOT, SITE)} (${pages.join(', ')})`);
  pages = pages.filter((p) => opts.pages.includes(p));
}

const pwDir = process.env.PLAYWRIGHT_DIR;
if (!pwDir || !existsSync(join(pwDir, 'node_modules/playwright'))) {
  fail('set PLAYWRIGHT_DIR to a directory with node_modules/playwright (hack/dev-setup.sh writes it into .dev.mk)');
}
const { chromium } = createRequire(join(pwDir, 'node_modules', '/'))('playwright');

// ---------------------------------------------------------------- the server
// Its own python3 -m http.server, stopped on the way out; a port someone else holds
// is theirs, so the check stops instead of using or killing it.
const taken = await new Promise((done) => {
  const s = connect(PORT, HOST);
  s.once('connect', () => { s.destroy(); done(true); });
  s.once('error', () => done(false));
});
if (taken) fail(`${HOST}:${PORT} is already in use; this check serves the site there itself (python3 -m http.server), so free the port first`);
const server = spawn('python3', ['-m', 'http.server', String(PORT), '--bind', HOST, '--directory', SITE], { stdio: ['ignore', 'ignore', 'pipe'] });
let serverLog = '';
server.stderr.on('data', (d) => { serverLog = (serverLog + d).slice(-4000); });
const stopServer = () => { if (server.exitCode === null && server.signalCode === null) server.kill('SIGTERM'); };
process.on('exit', stopServer);
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(sig, () => { stopServer(); process.exit(130); });
for (let t = Date.now(); ; await sleep(50)) {
  if (server.exitCode !== null) fail(`python3 -m http.server exited (${server.exitCode}):\n${serverLog}`);
  try { if ((await fetch(`${ORIGIN}/index.html`, { method: 'HEAD' })).ok) break; } catch { /* not up yet */ }
  if (Date.now() - t > 10000) fail(`python3 -m http.server did not answer on ${ORIGIN} within 10 s:\n${serverLog}`);
}

// ---------------------------------------------------------------- in the page
// Installed before any of the page's own code: the layout-shift record and the
// measuring functions the checks call (window.__wc).
function pageKit() {
  // Every shift counts, hadRecentInput or not: the check types and clicks nothing before
  // it measures, and Chromium's mobile emulation marks shifts as following input anyway.
  const shifts = [];
  const record = (entries) => {
    for (const e of entries) shifts.push({ t: e.startTime, v: e.value, nodes: (e.sources || []).map((s) => s.node).filter(Boolean) });
  };
  let observer = null;
  try {
    observer = new PerformanceObserver((list) => record(list.getEntries()));
    observer.observe({ type: 'layout-shift', buffered: true });
  } catch { /* no layout-shift entries in this engine: shifts stay empty */ }
  // the first paint, for the check that holds the fonts until it (window.__wcPainted)
  try {
    new PerformanceObserver((list) => {
      if (list.getEntries().some((e) => e.name === 'first-contentful-paint') && window.__wcPainted) window.__wcPainted();
    }).observe({ type: 'paint', buffered: true });
  } catch { /* no paint timing: the fonts wait for the timeout */ }

  const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  const timeout = (ms) => new Promise((r) => setTimeout(r, ms));
  const text = (el) => (el.getAttribute('aria-label') || el.getAttribute('alt') || el.textContent || '').trim().replace(/\s+/g, ' ');
  const describe = (el) => {
    if (!el || el.nodeType !== 1) return el ? el.nodeName.toLowerCase() : 'nothing';
    const part = (e) => e.tagName.toLowerCase() + (e.id ? `#${e.id}` : '') + [...e.classList].slice(0, 2).map((c) => `.${c}`).join('');
    const ctx = el.parentElement && el.parentElement.closest('[id]');
    const t = text(el).slice(0, 48);
    return `${ctx && ctx !== document.body ? `#${ctx.id} ` : ''}${part(el)}${t ? ` "${t}"` : ''}`;
  };
  const parse = (c) => {
    const m = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/.exec(c || '');
    if (!m) return null;
    const a = m[4] === undefined ? 1 : m[4].endsWith('%') ? parseFloat(m[4]) / 100 : +m[4];
    return { r: +m[1], g: +m[2], b: +m[3], a };
  };
  const over = (top, under) => ({ r: top.r * top.a + under.r * (1 - top.a), g: top.g * top.a + under.g * (1 - top.a), b: top.b * top.a + under.b * (1 - top.a), a: 1 });
  const lum = ({ r, g, b }) => {
    const f = (c) => { c /= 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const contrast = (x, y) => { const a = lum(x), b = lum(y); return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); };
  const hex = ({ r, g, b }) => `#${[r, g, b].map((v) => Math.round(v).toString(16).padStart(2, '0')).join('')}`;
  // the colour a box's ancestors paint behind it: background colours composited down to
  // the first opaque one (the canvas is the page colour, the root's or the body's)
  const ground = (el) => {
    const layers = [];
    for (let a = el; a; a = a.parentElement) {
      const c = parse(getComputedStyle(a).backgroundColor);
      if (c && c.a > 0) { layers.push(c); if (c.a >= 1) break; }
    }
    let g = layers.length && layers[layers.length - 1].a >= 1 ? layers.pop() : parse(getComputedStyle(document.body).backgroundColor) || { r: 255, g: 255, b: 255, a: 1 };
    if (g.a < 1) g = over(g, { r: 255, g: 255, b: 255, a: 1 });
    while (layers.length) g = over(layers.pop(), g);
    return g;
  };
  const box = (r) => ({ l: r.left, t: r.top, r: r.right, b: r.bottom });
  const isect = (a, b) => ({ l: Math.max(a.l, b.l), t: Math.max(a.t, b.t), r: Math.min(a.r, b.r), b: Math.min(a.b, b.b) });
  const area = (a) => Math.max(0, a.r - a.l) * Math.max(0, a.b - a.t);
  const grow = (a, k) => ({ l: a.l - k, t: a.t - k, r: a.r + k, b: a.b + k });
  // what can be seen of an element: the viewport, cut by every ancestor that clips or
  // scrolls (the root's and the body's overflow belong to the viewport)
  const clipOf = (el) => {
    let clip = { l: 0, t: 0, r: document.documentElement.clientWidth, b: document.documentElement.clientHeight };
    let by = null;
    for (let a = el.parentElement; a && a !== document.body && a !== document.documentElement; a = a.parentElement) {
      const s = getComputedStyle(a);
      const cx = s.overflowX !== 'visible', cy = s.overflowY !== 'visible';
      if (!cx && !cy) continue;
      const r = a.getBoundingClientRect();
      const pad = { l: r.left + a.clientLeft, t: r.top + a.clientTop, r: r.left + a.clientLeft + a.clientWidth, b: r.top + a.clientTop + a.clientHeight };
      const next = { l: cx ? Math.max(clip.l, pad.l) : clip.l, t: cy ? Math.max(clip.t, pad.t) : clip.t, r: cx ? Math.min(clip.r, pad.r) : clip.r, b: cy ? Math.min(clip.b, pad.b) : clip.b };
      if (area(next) < area(clip)) by = a;
      clip = next;
    }
    return { clip, by };
  };

  window.__wc = {
    // finite animations played out (the era stripe, the stair), at most 4 s
    async settle() {
      const finite = document.getAnimations().filter((a) => a.effect && a.effect.getTiming().iterations !== Infinity);
      await Promise.race([Promise.all(finite.map((a) => a.finished.catch(() => {}))), timeout(4000)]);
      await frame();
    },
    running: () => document.getAnimations().filter((a) => a.playState === 'running').map((a) => `${describe(a.effect && a.effect.target)} (${a.animationName || a.transitionProperty || 'animation'})`),
    // the page scrolled to its end in steps, each step's lazy images loaded
    async scrollThrough() {
      const se = document.scrollingElement;
      for (let y = 0; ; y += Math.max(200, Math.round(innerHeight * 0.75))) {
        window.scrollTo(0, y);
        await frame();
        const near = [...document.images].filter((i) => {
          if (i.complete) return false;
          const r = i.getBoundingClientRect();
          return r.width > 0 && r.bottom > -innerHeight && r.top < 2 * innerHeight;
        });
        await Promise.race([Promise.all(near.map((i) => i.decode().catch(() => {}))), timeout(3000)]);
        if (y + innerHeight >= se.scrollHeight) break;
      }
      await frame();
    },
    // CLS as Chrome counts it: shifts in session windows (gaps under 1 s, at most 5 s
    // long), the largest window's sum, with the boxes that moved most in it
    cls() {
      if (observer) record(observer.takeRecords()); // entries not yet delivered to the callback
      let best = { v: 0, nodes: new Map() }, cur = null;
      for (const e of shifts) {
        if (!cur || e.t - cur.last >= 1000 || e.t - cur.start >= 5000) cur = { start: e.t, last: e.t, v: 0, nodes: new Map() };
        cur.last = e.t;
        cur.v += e.v;
        for (const n of e.nodes) cur.nodes.set(n, (cur.nodes.get(n) || 0) + e.v);
        if (cur.v > best.v) best = cur;
      }
      const moved = [...best.nodes.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3).map(([n]) => describe(n));
      return { value: best.v, moved };
    },
    // every rendered <img> loaded and decoded
    broken: () => [...document.images].filter((i) => i.getClientRects().length && (!i.complete || i.naturalWidth === 0)).map((i) => `${describe(i)} (${i.currentSrc || i.src})`),
    // every image candidate the markup names, loaded or not (a 2× file, a dark source)
    candidates() {
      const urls = new Set();
      for (const el of document.querySelectorAll('img, source, video[poster], link[rel~="icon"], link[rel="apple-touch-icon"], link[rel="preload"][as="image"]')) {
        for (const attr of ['src', 'poster', 'href']) if (el.getAttribute(attr)) urls.add(new URL(el.getAttribute(attr), location.href).href);
        for (const c of (el.getAttribute('srcset') || '').split(',')) {
          const u = c.trim().split(/\s+/)[0];
          if (u) urls.add(new URL(u, location.href).href);
        }
      }
      return [...urls];
    },
    // horizontal overflow: the page scrolling sideways, or a box past the viewport's
    // edge that no box of the page's own scrolls or clips (the outermost one is named)
    overflow() {
      const se = document.scrollingElement, vw = document.documentElement.clientWidth;
      const out = [];
      if (se.scrollWidth > se.clientWidth + 1) out.push(`the page scrolls sideways: ${se.scrollWidth} px of content in ${se.clientWidth} px`);
      const held = (el) => {
        for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) if (getComputedStyle(a).overflowX !== 'visible') return true;
        return false;
      };
      const past = new Set();
      for (const el of document.body.querySelectorAll('*')) {
        const r = el.getBoundingClientRect();
        if (r.width < 1 || r.height < 1 || (r.left >= -1 && r.right <= vw + 1)) continue;
        if (getComputedStyle(el).visibility === 'hidden' || held(el)) continue;
        past.add(el);
      }
      for (const el of past) {
        let a = el.parentElement;
        while (a && !past.has(a)) a = a.parentElement;
        if (a) continue;
        const r = el.getBoundingClientRect();
        out.push(`${describe(el)} runs ${r.left < -1 ? `${Math.round(-r.left)} px past the left edge` : `${Math.round(r.right - vw)} px past the right edge`} of ${vw} px`);
      }
      // a command is one line that scrolls inside its block with no scrollbar shown, so
      // one that does not fit hides its end from anyone who reads or types it: it must fit
      for (const c of document.querySelectorAll('.cmd-code')) {
        if (c.getClientRects().length && c.scrollWidth > c.clientWidth + 1) out.push(`${describe(c)} does not fit its line: ${c.scrollWidth} px in ${c.clientWidth} px, its end hidden`);
      }
      return out;
    },
    details(open) {
      for (const d of document.querySelectorAll('details')) {
        if (open) { d.__wcWasOpen = d.open; d.open = true; } else d.open = !!d.__wcWasOpen;
      }
    },
    // the closed <details> a visitor can open here, by index
    closedDetails: () => [...document.querySelectorAll('details')].map((d, i) => (!d.open && d.getClientRects().length ? i : -1)).filter((i) => i >= 0),
    openAlone(i, open) {
      const d = document.querySelectorAll('details')[i];
      d.open = open;
      if (open) d.querySelector('summary').focus();
    },
    // the focused element against the ring's rules; null once focus has left the page
    // (or the <details> being walked)
    focused(scope) {
      const el = document.activeElement;
      if (!el || el === document.body || el === document.documentElement) return null;
      if (scope !== undefined && !document.querySelectorAll('details')[scope].contains(el)) return null;
      if (!el.__wcKey) el.__wcKey = (window.__wcKeys = (window.__wcKeys || 0) + 1);
      const problems = [];
      const s = getComputedStyle(el);
      const rects = [...el.getClientRects()].filter((r) => r.width > 0 || r.height > 0);
      if (!rects.length || s.visibility !== 'visible' || +s.opacity === 0) {
        problems.push('focus lands on something that is not shown');
        return { key: el.__wcKey, what: describe(el), problems };
      }
      if (!el.matches(':focus-visible')) problems.push('keyboard focus does not match :focus-visible');
      const b = box(el.getBoundingClientRect());
      const { clip, by } = clipOf(el);
      const seen = isect(b, clip);
      if (area(seen) <= 0) problems.push('it is out of sight when focused');
      else {
        const hit = document.elementFromPoint((seen.l + seen.r) / 2, (seen.t + seen.b) / 2);
        if (hit && hit !== el && !el.contains(hit) && !hit.contains(el)) problems.push(`it is covered by ${describe(hit)}`);
      }
      const width = s.outlineStyle === 'none' ? 0 : parseFloat(s.outlineWidth) || 0;
      const color = parse(s.outlineColor);
      if (width >= 1 && color && color.a > 0) {
        const off = parseFloat(s.outlineOffset) || 0;
        const outer = grow(b, off + width), inner = grow(b, off);
        const total = area(outer) - area(inner);
        const shown = area(isect(outer, clip)) - area(isect(inner, clip));
        if (total > 0 && shown / total < LIMITS.ringShown) {
          problems.push(`its focus ring is ${Math.round(100 - (100 * shown) / total)} % hidden by ${by ? describe(by) : 'the viewport'}`);
        }
        const g = ground(off < 0 ? el : el.parentElement || el);
        const ring = color.a < 1 ? over(color, g) : color;
        const c = contrast(ring, g);
        if (c < LIMITS.ringContrast) problems.push(`its focus ring (${hex(ring)}) is ${c.toFixed(2)}:1 against ${hex(g)}, under ${LIMITS.ringContrast}:1`);
      } else if (!s.boxShadow || s.boxShadow === 'none') {
        problems.push('it draws no outline or box-shadow when focused');
      }
      return { key: el.__wcKey, what: describe(el), problems };
    },
  };
}
// the kit is a function in this file; the page gets its source with the limits inlined
const KIT = `(() => { const LIMITS = ${JSON.stringify(LIMITS)}; (${pageKit.toString()})(); })()`;

// ---------------------------------------------------------------- one page, one size, one mode
const problems = new Map(); // "page\tkind\tmessage" → Set of "width mode"
const stats = new Map(); // page → { cls, stops }
const note = (page, where, kind, msg) => {
  const k = `${page}\t${kind}\t${msg}`;
  if (!problems.has(k)) problems.set(k, new Set());
  problems.get(k).add(where);
};

async function contextFor(browser, [width, height, dpr], mode) {
  const ctx = await browser.newContext({
    viewport: { width, height }, deviceScaleFactor: dpr, isMobile: width <= 768, hasTouch: width <= 768, ...MODES[mode],
  });
  await ctx.addInitScript(KIT);
  return ctx;
}

async function checkPage(browser, name, size, mode) {
  const where = `${size[0]} ${mode}`;
  const add = (kind, msg) => note(name, where, kind, msg);
  const ctx = await contextFor(browser, size, mode);
  try {
    // the fonts wait for the first paint, then come in one at a time, in a fixed order,
    // each given time to be laid out: the swaps are the same shifts on every run
    const fonts = [];
    let painted = false;
    const letFontsIn = async () => {
      if (painted) return;
      painted = true;
      await sleep(50);
      while (fonts.length) {
        fonts.sort((a, b) => a.request().url().localeCompare(b.request().url()));
        await fonts.shift().continue().catch(() => {});
        await sleep(120);
      }
      painted = 'done';
    };
    await ctx.exposeBinding('__wcPainted', () => { letFontsIn(); });
    await ctx.route('**/*', async (route) => {
      const url = route.request().url();
      if (!url.startsWith(`${ORIGIN}/`)) {
        add('offsite', `requests ${url} (blocked: the site loads nothing from other hosts)`);
        return route.abort('blockedbyclient');
      }
      if (route.request().resourceType() !== 'font' || painted === 'done') return route.continue();
      fonts.push(route);
      setTimeout(letFontsIn, 5000); // a page with no contentful paint
      return undefined;
    });
    const page = await ctx.newPage();
    const kindOf = (r) => (r.resourceType() === 'image' ? 'image' : 'request');
    const short = (u) => u.replace(ORIGIN, '');
    page.on('console', (m) => {
      if (m.type() !== 'error') return;
      // failed loads are reported below, with their URL and status
      if (/^Failed to load resource|ERR_BLOCKED_BY_CLIENT/.test(m.text())) return;
      const at = m.location() && m.location().url ? ` (${short(m.location().url)}:${m.location().lineNumber})` : '';
      add('console', `console error: ${m.text()}${at}`);
    });
    page.on('pageerror', (e) => add('console', `uncaught ${e.name}: ${e.message}`));
    page.on('requestfailed', (r) => {
      if (r.url().startsWith(`${ORIGIN}/`)) add(kindOf(r), `${short(r.url())} failed: ${r.failure() ? r.failure().errorText : 'no answer'}`);
    });
    page.on('response', (r) => { if (r.status() >= 400) add(kindOf(r.request()), `${short(r.url())} answered HTTP ${r.status()}`); });

    await page.goto(`${ORIGIN}/${name}`, { waitUntil: 'load' });
    await page.evaluate(() => document.fonts.ready);
    if (MODES[mode].reducedMotion === 'reduce') {
      for (const a of await page.evaluate(() => window.__wc.running())) add('motion', `${a} animates under prefers-reduced-motion`);
    }
    await page.evaluate(() => window.__wc.settle());
    // what comes late (a font, an image, a script) has come, and moved what it moved,
    // while the page sits at its top, as a visitor's would: no request for 500 ms. Only
    // then the scroll, so scroll anchoring cannot hide a shift above the fold.
    await page.waitForLoadState('networkidle');

    // every candidate the markup names, once per page
    if (size === SIZES[0] && mode === 'light') {
      for (const url of await page.evaluate(() => window.__wc.candidates())) {
        if (!url.startsWith(`${ORIGIN}/`)) continue;
        const r = await fetch(url, { method: 'HEAD' });
        if (!r.ok) add('image', `${short(url)}, named by the markup, answers HTTP ${r.status}`);
      }
    }

    await page.evaluate(() => window.__wc.scrollThrough());
    const cls = await page.evaluate(() => window.__wc.cls());
    if (size !== REFLOW && cls.value > LIMITS.cls) add('layout shift', `layout shift ${cls.value.toFixed(3)}, over ${LIMITS.cls}: ${cls.moved.join(', ') || 'no source named'}`);
    for (const b of await page.evaluate(() => window.__wc.broken())) add('image', `${b} did not load`);

    await page.evaluate(() => window.scrollTo(0, 0));
    for (const o of await page.evaluate(() => window.__wc.overflow())) add('overflow', o);
    await page.evaluate(() => window.__wc.details(true));
    for (const o of await page.evaluate(() => window.__wc.overflow())) add('overflow', `with every <details> open: ${o}`);
    await page.evaluate(() => window.__wc.details(false));
    if (size === REFLOW) return; // the reflow width: overflow only

    // the keyboard's way through the page, then through each <details>, opened alone
    let stops = 0;
    const walk = async (scope) => {
      const keys = new Set();
      for (let i = 0; i < 500; i++) {
        await page.keyboard.press('Tab');
        const f = await page.evaluate((s) => window.__wc.focused(s === null ? undefined : s), scope);
        if (!f || keys.has(f.key)) break;
        keys.add(f.key);
        stops++;
        for (const p of f.problems) add('focus', `${f.what}: ${p}`);
      }
    };
    await page.evaluate(() => { window.scrollTo(0, 0); if (document.activeElement) document.activeElement.blur(); });
    await walk(null);
    for (const i of await page.evaluate(() => window.__wc.closedDetails())) {
      await page.evaluate((d) => window.__wc.openAlone(d, true), i);
      await walk(i);
      await page.evaluate((d) => window.__wc.openAlone(d, false), i);
    }
    const st = stats.get(name) || { cls: 0, stops: 0 };
    stats.set(name, { cls: Math.max(st.cls, cls.value), stops: Math.max(st.stops, stops) });
  } catch (e) {
    add('error', `the check itself failed: ${e.message.split('\n')[0]}`);
  } finally {
    await ctx.close();
  }
}

async function shoot(browser, name, [width, height], theme) {
  const ctx = await contextFor(browser, [width, height, 2], theme);
  try {
    const page = await ctx.newPage();
    await page.goto(`${ORIGIN}/${name}`, { waitUntil: 'load' });
    await page.evaluate(() => document.fonts.ready);
    await page.evaluate(() => window.__wc.settle());
    await page.evaluate(() => window.__wc.scrollThrough());
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    const file = join(opts.shots, `${name.replace(/\.html$/, '')}-${width}x${height}-${theme}.png`);
    await page.screenshot({ path: file, fullPage: true });
    return file;
  } finally {
    await ctx.close();
  }
}

async function pool(jobs, n) {
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(n, jobs.length) }, async () => {
    while (next < jobs.length) await jobs[next++]();
  }));
}

// ---------------------------------------------------------------- run
const started = Date.now();
const browser = await chromium.launch();
let code = 0;
try {
  const jobs = [];
  for (const name of pages) for (const size of SIZES) for (const mode of Object.keys(MODES)) jobs.push(() => checkPage(browser, name, size, mode));
  for (const name of pages) jobs.push(() => checkPage(browser, name, REFLOW, 'light'));
  await pool(jobs, Math.max(2, Math.min(8, Math.floor(availableParallelism() / 2))));

  const secs = ((Date.now() - started) / 1000).toFixed(0);
  console.log(`website-check: ${pages.length} page(s) × ${SIZES.length} widths × ${Object.keys(MODES).length} modes, and at ${REFLOW[0]} px for overflow, in Chromium ${browser.version()}, from ${relative(ROOT, SITE)}/ (${secs} s)`);
  for (const name of pages) {
    const st = stats.get(name) || { cls: 0, stops: 0 };
    const n = [...problems.keys()].filter((k) => k.startsWith(`${name}\t`)).length;
    console.log(`  ${name.padEnd(15)} ${n ? `${n} problem(s)` : 'ok'.padEnd(12)}  largest layout shift ${st.cls.toFixed(3)}, ${st.stops} focus stops`);
  }
  if (problems.size) {
    const all = SIZES.length * Object.keys(MODES).length;
    console.error(`website-check: ${problems.size} problem(s)`);
    for (const [k, where] of problems) {
      const [page, kind, msg] = k.split('\t');
      console.error(`  - ${page} [${kind}] ${msg}\n      at ${where.size === all ? 'every width and mode' : [...where].join(', ')}`);
    }
    code = 1;
  } else console.log('website-check: ok');

  if (opts.shots) {
    mkdirSync(opts.shots, { recursive: true });
    const shots = [];
    for (const name of pages) for (const size of SHOT_SIZES) for (const theme of ['light', 'dark']) shots.push(async () => { await shoot(browser, name, size, theme); });
    await pool(shots, 4);
    console.log(`website-check: ${shots.length} screenshots in ${opts.shots}`);
  }
} finally {
  await browser.close();
  stopServer();
}
process.exit(code);
