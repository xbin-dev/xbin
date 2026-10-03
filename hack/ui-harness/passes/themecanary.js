// hack/ui-harness/passes/themecanary.js — the runtime half of the theme guard
// (D184; docs/maintenance.md → "the theme guard"). hack/theme-lint.mjs reads
// the source; this reads what the browser draws. Every document gets an
// override that sets each colour token theme.css defines to a fingerprint,
// rgb(1, 254, n), each font token to a family nobody has (CanarySans,
// CanaryMono, CanaryDisplay) and the radius to an odd 1.75px, in both
// colour schemes; then it walks every visible element, shadow roots and
// ::before/::after/::placeholder included, and reports any colour, font or
// corner that didn't come from a token — what a static scan can't see: UA
// defaults (the placeholder grey, accent-color: auto, Arial in a button),
// colours built at runtime, an xterm theme that ignores the tokens.
//
// Screens, as the admin (the sign-in page signed out), in a light and a dark
// system: the sign-in page, the shell, a terminal window, the code window,
// the admin console, the partitions page, and the agent template's chat
// (when the seeded agent tile can run: it needs gocryptfs). Test fixtures
// (the seed's apps/*) aren't shipped UI and aren't audited; their frames are
// skipped. Each finding is listed in $OUT/theme-canary.json; the pass fails
// on any, unless CANARY_REPORT=1 (a burn-down run: counts only).
//
// Exceptions, with their reasons, are EXEMPT below (the runtime `theme-ok:`):
// keep them as few as the allowlist. Content a person wrote (a folder's
// emoji, an agent class's icon) is theirs: an element marked
// data-bx-content, and what it holds, is never counted for emoji.
const path = require('path');
const { URL, OUT, fs, sleep, log, login, closeCtx, openShell, usePersonalScreen, openTile, closeTile, tileFrame, fr, checker, noGocryptfs } = require('../lib');

const REPO = process.env.REPO || path.join(__dirname, '..', '..', '..');
const REPORT = !!process.env.CANARY_REPORT;
const RADIUS = '1.75px';

// what the browser may draw that no token can say, and why
const EXEMPT = [
  { el: /^textarea\.xterm-helper-textarea/, why: 'xterm\'s input proxy, styled by the vendored xterm.css and never visible (audit A22)' },
  { el: /\.qr\b/, why: 'a QR code stays dark on light for scanners' },
  { el: /^span\.ficon\b/, prop: 'emoji', why: 'a folder\'s icon is what the person typed (the sidebar)' },
];

// The canary sheet: theme.css's tokens, colour by colour, as fingerprints.
async function canaryCss(scheme) {
  const { readTheme, dayBlocks } = await import(path.join(REPO, 'hack', 'theme-fallbacks.mjs'));
  const css = fs.readFileSync(path.join(REPO, 'web', 'theme.css'), 'utf8');
  const { tokens, raw } = readTheme(css);
  const inDay = new Set((dayBlocks(css)[0] || []).map((d) => d.split(':')[0].trim()));
  const COLOUR = /#[0-9a-fA-F]{3,8}\b|rgba?\([^)]*\)/g;
  const decl = [];
  let n = 1;
  for (const [name, value] of Object.entries(tokens)) {
    // an alias follows what it names, in both themes; a composite of other
    // tokens (the focus ring, the type shorthands) too. A role Night
    // computes from other tokens (color-mix(): the hover, the tints, the
    // accent's hover), or one Day sets to a value of its own (the accent's
    // ink, the title bars), is a token of its own: a fingerprint of its own.
    if (/var\(/.test(raw[name]) && !/color-mix\(/.test(raw[name]) && !inDay.has(name)) continue;
    if (/^--bx-(sans|mono|display)$/.test(name)) {
      decl.push(`${name}: "Canary${{ sans: 'Sans', mono: 'Mono', display: 'Display' }[name.slice(5)]}"`);
    } else if (name === '--bx-radius') {
      decl.push(`${name}: ${RADIUS}`);
    } else if (COLOUR.test(value)) {
      COLOUR.lastIndex = 0;
      const k = n++;
      decl.push(`${name}: ${value.replace(COLOUR, (c) => (/rgba\(.*,\s*0?\.\d+\s*\)/.test(c) ? `rgba(1, 254, ${k}, 0.5)` : `rgb(1, 254, ${k})`))}`);
    }
    COLOUR.lastIndex = 0;
  }
  return `:root:root:root {\n${decl.map((d) => `  ${d} !important;`).join('\n')}\n  color-scheme: ${scheme} !important;\n}\n`;
}

// in every document: the canary sheet, kept last in <head>
function injectCanary({ css }) {
  const put = () => {
    let s = document.getElementById('bx-canary');
    if (!s) { s = document.createElement('style'); s.id = 'bx-canary'; s.textContent = css; }
    const parent = document.head || document.documentElement;
    if (parent && s.parentNode !== parent) parent.appendChild(s);
    return !!parent;
  };
  if (!put()) new MutationObserver((_, o) => { if (put()) o.disconnect(); }).observe(document, { childList: true, subtree: true });
  document.addEventListener('DOMContentLoaded', () => { const s = document.getElementById('bx-canary'); if (s && document.head) document.head.appendChild(s); });
}

// in a document: every visible element's colours, fonts and corners that
// aren't the fingerprint (ported from the theme preview's audit)
function auditDoc(RADIUS) {
  const out = [];
  const parse = (s) => {
    const res = [];
    const re = /rgba?\(([^)]+)\)|color\((srgb|srgb-linear|display-p3) ([^)]+)\)|(oklab|oklch|lab|lch|hsl|hwb)\(([^)]+)\)/g;
    let m;
    while ((m = re.exec(s))) {
      if (m[1]) { const p = m[1].split(/[\s,/]+/).filter(Boolean).map(Number); res.push({ r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1, raw: m[0] }); }
      else if (m[2] === 'srgb') { const p = m[3].split(/[\s/]+/).filter(Boolean).map(Number); res.push({ r: p[0] * 255, g: p[1] * 255, b: p[2] * 255, a: p.length > 3 ? p[3] : 1, raw: m[0] }); }
      else res.push({ r: NaN, g: NaN, b: NaN, a: 1, raw: m[0] });
    }
    return res;
  };
  const near = (x, y) => Math.abs(x - y) <= 2;
  const canary = (c) => near(c.r, 1) && near(c.g, 254);
  const short = (c) => (isNaN(c.r) ? c.raw : `rgb(${Math.round(c.r)},${Math.round(c.g)},${Math.round(c.b)}${c.a < 1 ? ` / ${(+c.a).toFixed(2)}` : ''})`);
  const desc = (el) => {
    let d = el.localName;
    if (el.id) d += `#${el.id}`;
    const cls = typeof el.className === 'string' ? el.className : el.getAttribute?.('class') || '';
    if (cls.trim()) d += `.${cls.trim().split(/\s+/).slice(0, 4).join('.')}`;
    return d;
  };
  const ownText = (el) => [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join('').replace(/\s+/g, ' ').trim();
  const W = innerWidth, H = innerHeight;
  const FONT_OK = /^"?Canary(Sans|Mono|Display)"?$/;
  const visit = (el, hosts, pseudo) => {
    const cs = getComputedStyle(el, pseudo || null);
    const where = { hosts: hosts.join(' > '), el: desc(el) + (pseudo || '') };
    const flag = (prop, value, extra = {}) => out.push({ ...where, prop, value, ...extra });
    const txt = pseudo === '::placeholder' ? (el.placeholder || '')
      : pseudo ? (cs.content && cs.content !== 'none' && cs.content !== 'normal' ? cs.content.slice(0, 30) : '') : ownText(el);
    const field = !pseudo && /^(input|textarea|select|button)$/.test(el.localName);
    const tag = { text: (txt || (field ? (el.value || el.placeholder || '') : '')).slice(0, 40) };
    const content = !!el.closest?.('[data-bx-content]');
    if (txt && !content && /\p{Extended_Pictographic}/u.test(txt) && !/^[←-⇿⌀-⏿■-◿☀-⛿✀-➿\s]+$/u.test(txt.replace(/[^\p{Extended_Pictographic}\s]/gu, ''))) flag('emoji', txt.match(/\p{Extended_Pictographic}/gu).join(''), tag);
    if (txt || field) {
      for (const c of parse(cs.color)) if (c.a > 0 && !canary(c)) flag('color', short(c), tag);
      const fam = cs.fontFamily.split(',')[0].trim();
      if (!FONT_OK.test(fam)) flag('font-family', cs.fontFamily.slice(0, 60), tag);
    }
    for (const c of parse(cs.backgroundColor)) if (c.a > 0 && !canary(c)) flag('background-color', short(c), tag);
    for (const side of ['Top', 'Right', 'Bottom', 'Left']) {
      if (parseFloat(cs[`border${side}Width`]) > 0 && !/none|hidden/.test(cs[`border${side}Style`])) {
        for (const c of parse(cs[`border${side}Color`])) if (c.a > 0 && !canary(c)) { flag('border-color', short(c), tag); break; }
      }
    }
    if (cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0) for (const c of parse(cs.outlineColor)) if (c.a > 0 && !canary(c)) flag('outline-color', short(c), tag);
    if (cs.boxShadow && cs.boxShadow !== 'none') for (const c of parse(cs.boxShadow)) if (c.a > 0 && !canary(c)) { flag('box-shadow', cs.boxShadow.slice(0, 120), tag); break; }
    if (cs.textShadow && cs.textShadow !== 'none') for (const c of parse(cs.textShadow)) if (c.a > 0 && !canary(c)) { flag('text-shadow', cs.textShadow.slice(0, 80), tag); break; }
    if (cs.backgroundImage && cs.backgroundImage !== 'none') {
      for (const c of parse(cs.backgroundImage)) if (c.a > 0 && !canary(c)) { flag('background-image', cs.backgroundImage.slice(0, 140), tag); break; }
    }
    if (!pseudo) {
      for (const corner of ['TopLeft', 'TopRight', 'BottomRight', 'BottomLeft']) {
        const r = cs[`border${corner}Radius`];
        if (r && r.split(' ').some((x) => x !== '0px' && x !== RADIUS)) { flag('border-radius', r, tag); break; }
      }
    }
    if (!pseudo && el instanceof SVGElement && !['svg', 'g', 'defs', 'symbol', 'clipPath', 'mask', 'linearGradient', 'stop'].includes(el.localName)) {
      for (const prop of ['fill', 'stroke']) {
        const v = cs[prop];
        if (v && v !== 'none' && !/url\(/.test(v)) for (const c of parse(v)) if (c.a > 0 && !canary(c)) flag(prop, short(c), tag);
      }
    }
    if (!pseudo && el.localName === 'stop') for (const c of parse(cs.stopColor)) if (!canary(c)) flag('stop-color', short(c), tag);
    if (!pseudo && el.localName === 'input' && /checkbox|radio|range/.test(el.type) && cs.accentColor === 'auto') flag('accent-color', 'auto (UA default)', tag);
  };
  const walk = (root, hosts) => {
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, [...hosts, desc(el)]);
      if (/^(script|style|link|meta|head|title|template|noscript|img|canvas|video|iframe|picture|source)$/.test(el.localName)) continue;
      if (el.checkVisibility && !el.checkVisibility({ checkVisibilityCSS: true })) continue;
      const r = el.getBoundingClientRect();
      if (r.width < 1 || r.height < 1 || r.right <= 0 || r.bottom <= 0 || r.left >= W || r.top >= H) continue;
      visit(el, hosts);
      for (const ps of ['::before', '::after', '::placeholder']) {
        if (ps === '::placeholder' && !((el.localName === 'input' || el.localName === 'textarea') && el.placeholder)) continue;
        const pc = getComputedStyle(el, ps);
        if (ps !== '::placeholder' && (!pc.content || pc.content === 'none' || pc.content === 'normal')) continue;
        visit(el, hosts, ps);
      }
    }
  };
  walk(document, []);
  const rs = getComputedStyle(document.documentElement);
  for (const c of parse(rs.backgroundColor)) if (c.a > 0 && !canary(c)) out.push({ hosts: '', el: 'html', prop: 'background-color', value: short(c) });
  return { url: location.pathname + location.hash, colorScheme: rs.colorScheme, findings: out };
}

const within = (p, ms) => Promise.race([p, new Promise((_, rej) => setTimeout(() => rej(new Error('frame timeout')), ms))]);
// a frame is shipped UI unless it's a test fixture of the seed (apps/*, but
// the agent template's instance)
const shipped = (u) => !/\/c\/apps\//.test(u) || /\/c\/apps\/agent\//.test(u);

async function auditPage(page) {
  const docs = [];
  for (const f of page.frames()) {
    if (f !== page.mainFrame() && !/^https?:/.test(f.url())) continue;
    if (!shipped(f.url())) continue;
    if (f !== page.mainFrame() && !(await within(f.frameElement().then((h) => h.isVisible()), 3000).catch(() => false))) continue;
    try {
      await within(f.evaluate(() => document.fonts?.ready.then(() => true)), 5000).catch(() => {});
      docs.push(await within(f.evaluate(auditDoc, RADIUS), 30000));
    } catch (e) {
      docs.push({ url: f.url(), error: String(e.message).slice(0, 200), findings: [] });
    }
  }
  for (const d of docs) d.findings = d.findings.filter((x) => !EXEMPT.some((e) => e.el.test(x.el) && (!e.prop || e.prop === x.prop)));
  return docs;
}

async function themeCanary(browser) {
  const { check, skip, done } = checker('theme-canary');
  const report = {};
  for (const scheme of ['dark', 'light']) {
    const css = await canaryCss(scheme);
    const screens = {};
    const record = async (name, page) => {
      await sleep(400);
      screens[name] = await auditPage(page);
      await page.screenshot({ path: `${OUT}/theme-canary-${scheme}-${name}.png` });
    };
    // signed out: the sign-in page
    {
      const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: scheme });
      await ctx.addInitScript(injectCanary, { css });
      const page = await ctx.newPage();
      await page.goto(`${URL}/login`);
      await record('sign-in', page);
      await ctx.close();
    }
    const { ctx, page } = await login(browser, 'admin', 'admin', { viewport: { width: 1440, height: 900 }, colorScheme: scheme });
    await ctx.addInitScript(injectCanary, { css });
    try {
      await openShell(page);
      await usePersonalScreen(page);
      await record('shell', page);
      // a terminal window and the code window, on a seeded tile
      const host = 'apps/focusy';
      await openTile(page, host);
      await fr(page, host, (f) => { f.open('term'); if (!f.tabs.length) f.newTerm(); });
      await page.locator(`bx-frame[src="${host}"] bx-terminal .xterm-rows`).first().waitFor({ timeout: 20000 }).catch(() => {});
      await sleep(800);
      await record('terminal', page);
      await fr(page, host, (f) => { for (let i = f.tabs.length - 1; i >= 0; i--) f.closeTab(i); f.closeTerminal(); }).catch(() => {});
      await fr(page, host, (f) => f.open('code')).catch(() => {});
      await page.locator(`bx-frame[src="${host}"] bx-code`).first().waitFor({ timeout: 15000 }).catch(() => {});
      await sleep(800);
      await record('code', page);
      await fr(page, host, (f) => f.closeTerminal()).catch(() => {});
      await closeTile(page, host);
      // the agent template's chat
      if (noGocryptfs()) {
        skip(`${scheme}: the agent chat — ${noGocryptfs()}`);
      } else {
        await openTile(page, 'apps/agent');
        const f = await tileFrame(page, 'apps/agent').catch(() => null);
        if (f) await f.waitForSelector('textarea, [contenteditable]', { timeout: 20000 }).catch(() => {});
        await record('agent-chat', page);
        await closeTile(page, 'apps/agent');
      }
      // the admin console and the partitions page, full page
      await page.goto(`${URL}/c/tiles/admin/#components`);
      await page.waitForFunction(() => (document.querySelector('bx-admin')?.renderRoot?.textContent || '').length > 40, null, { timeout: 15000 }).catch(() => {});
      await record('admin', page);
      await page.goto(`${URL}/xbin/partitions`);
      await page.waitForSelector('bx-partitions-page', { timeout: 15000 }).catch(() => {});
      await sleep(800);
      await record('partitions', page);
    } finally {
      await closeCtx(ctx, page);
    }
    report[scheme] = screens;
    for (const [name, docs] of Object.entries(screens)) {
      const all = docs.flatMap((d) => d.findings.map((x) => ({ ...x, doc: d.url })));
      const errors = docs.filter((d) => d.error);
      const props = {};
      for (const x of all) props[x.prop] = (props[x.prop] || 0) + 1;
      const what = Object.entries(props).sort((a, b) => b[1] - a[1]).map(([p, k]) => `${p} ${k}`).join(', ');
      const line = `${scheme} ${name}: ${all.length} finding(s) in ${docs.length} document(s)${what ? ` (${what})` : ''}${errors.length ? `; ${errors.length} unreadable` : ''}`;
      if (REPORT) log(`theme-canary: ${line}`);
      else check(all.length === 0 && errors.length === 0, line);
    }
  }
  fs.writeFileSync(`${OUT}/theme-canary.json`, JSON.stringify(report, null, 1));
  log(`theme-canary: findings in ${OUT}/theme-canary.json${REPORT ? ' (CANARY_REPORT: not failing)' : ''}`);
  done();
}

module.exports = { themeCanary, auditDoc, canaryCss };
