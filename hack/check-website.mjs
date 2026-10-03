// hack/check-website.mjs — the xbin.dev site's guard, run by hack/check-website.sh
// (`make website-check`, part of `make guards`; `make website` runs it with --dist).
// website/README.md → "Checks" lists the rules; each one is a function below.
//
//   node hack/check-website.mjs            the guard
//   node hack/check-website.mjs --dist     also website/media/ against website/media.lock
//   node hack/check-website.mjs --privacy-digest FILE
//                                          the digest the privacy check pins, for FILE
//
// Sizes are in KB of 1,000 bytes, uncompressed.

import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
// WEBSITE_DIR points the check at another copy of the site (its tests do).
const SITE = resolve(process.env.WEBSITE_DIR || join(ROOT, 'website'));

// The preserved files, as master had them when the site moved to Base Two (D183):
// install.sh byte for byte (what `curl -fsSL https://xbin.dev/install.sh | sh` runs),
// and the privacy page's words (its title, description and text, normalized by
// privacyText). Change a digest only together with a deliberate change to the file;
// `--privacy-digest website/privacy.html` prints the new one.
const INSTALL_SH_SHA256 = 'ce2c21c4545d3df1d1160a4538462253d61a57326f3cb9f818dd57ad007c5b89';
const PRIVACY_TEXT_SHA256 = 'f5e163864917f86d538e155115a48b28493c6c880c77381e6315d7eddc3cd51a';

// Per page: HTML and its CSS, its JS, the fonts its CSS declares (all of them, as if
// every face loaded), and its eager images (every <img> without loading="lazy" counts
// as above the fold; for a <picture>, its largest candidate).
const BUDGET = { htmlCss: 60, js: 80, fonts: 200, images: 250 };

// External links the pages may carry (site/shared.md): the source, its docs and
// issues, and the hosted trial. Anything else is a new URL someone invented.
const LINKS = [/^https:\/\/github\.com\/xbin-dev\/xbin(\/|$)/, /^https:\/\/vcpu\.sh\/xbin$/];

const failures = [];
const notes = [];
const fail = (msg) => failures.push(msg);
const rel = (p) => relative(ROOT, p);
const kb = (n) => (n / 1000).toFixed(1);

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (['dist', 'static-helpers', 'media'].includes(name) && dir === SITE) continue;
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const decode = (s) => s
  .replace(/&nbsp;/g, ' ').replace(/&quot;/g, '"').replace(/&#39;|&apos;/g, "'")
  .replace(/&lt;/g, '<').replace(/&gt;/g, '>')
  .replace(/&#(\d+);/g, (_, n) => String.fromCodePoint(+n))
  .replace(/&#x([0-9a-f]+);/gi, (_, n) => String.fromCodePoint(parseInt(n, 16)))
  .replace(/&amp;/g, '&');

const stripComments = (html) => html.replace(/<!--[\s\S]*?-->/g, '');

// Every tag of one kind with its attributes (values decoded; a bare attribute is '').
function tags(html, name) {
  const out = [];
  const re = new RegExp(`<${name}\\b([^>]*)>`, 'gi');
  for (const m of html.matchAll(re)) {
    const attrs = {};
    for (const a of m[1].matchAll(/([^\s=/]+)(?:\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
      attrs[a[1].toLowerCase()] = decode(a[3] ?? a[4] ?? a[5] ?? '');
    }
    out.push({ attrs, index: m.index, raw: m[0] });
  }
  return out;
}

const isExternal = (u) => /^(https?:)?\/\//i.test(u.trim());
const isData = (u) => /^data:/i.test(u.trim());

// A site URL as a file: root-relative from website/, otherwise relative to `from`.
function fileFor(url, from) {
  const u = url.trim().replace(/[?#].*$/, '');
  if (!u || isExternal(u) || isData(u)) return null;
  return u.startsWith('/') ? join(SITE, u) : join(dirname(from), u);
}

const size = (p) => (p && existsSync(p) ? statSync(p).size : 0);
const srcsetURLs = (v) => (v || '').split(',').map((c) => c.trim().split(/\s+/)[0]).filter(Boolean);

// ---------------------------------------------------------------- the privacy text
export function privacyText(html) {
  const title = (html.match(/<title>([\s\S]*?)<\/title>/i) || [])[1] || '';
  const desc = (html.match(/<meta\s+name="description"\s+content="([^"]*)"/i) || [])[1] || '';
  const i = html.search(/<h1\b/i);
  const j = html.indexOf('</main>');
  if (i < 0 || j < i) return null;
  const body = stripComments(html.slice(i, j))
    .replace(/<\/?(?:p|li|ul|ol|h[1-6]|div|section|article|main|br|dl|dt|dd|table|tr|td|th|figure|figcaption|blockquote)\b[^>]*>/gi, ' ')
    .replace(/<[^>]+>/g, '');
  const norm = (s) => decode(s).replace(/\s+/g, ' ').trim();
  return `${norm(title)}\n${norm(desc)}\n${norm(body)}`;
}
const sha256 = (s) => createHash('sha256').update(s).digest('hex');

if (process.argv[2] === '--privacy-digest') {
  const t = privacyText(readFileSync(process.argv[3], 'utf8'));
  if (!t) { console.error('no <h1> … </main> in', process.argv[3]); process.exit(1); }
  console.log(sha256(t));
  process.exit(0);
}
const DIST = process.argv.includes('--dist');

// ---------------------------------------------------------------- the files
const files = walk(SITE);
const htmlFiles = files.filter((f) => f.endsWith('.html'));
// og.html is the share card's artwork, rendered to og.png, not a page.
const pages = htmlFiles.filter((f) => !f.endsWith('/og.html')).sort();
const cssFiles = files.filter((f) => f.endsWith('.css'));
const jsFiles = files.filter((f) => /\.m?js$/.test(f));
const read = (f) => readFileSync(f, 'utf8');

// ---------------------------------------------------------------- 1. preserved files
function preserved() {
  const inst = join(SITE, 'install.sh');
  if (!existsSync(inst)) fail('website/install.sh is missing (https://xbin.dev/install.sh serves it)');
  else if (sha256(readFileSync(inst)) !== INSTALL_SH_SHA256) {
    fail('website/install.sh is not byte-identical to the preserved bootstrap (compare: git diff master -- website/install.sh); if the change is deliberate, update INSTALL_SH_SHA256 in hack/check-website.mjs with it');
  }
  const ios = join(SITE, 'app/ios.json');
  try {
    const j = JSON.parse(readFileSync(ios, 'utf8'));
    const n = j && j.nativeRuntime;
    if (!n || typeof n.disabled !== 'boolean' || !Array.isArray(n.disabledBuilds)) {
      fail('website/app/ios.json: the kill switch needs {"nativeRuntime": {"disabled": <bool>, "disabledBuilds": [...]}} (website/README.md)');
    }
  } catch (e) {
    fail(`website/app/ios.json does not parse: ${e.message}`);
  }
  const priv = join(SITE, 'privacy.html');
  const t = existsSync(priv) ? privacyText(read(priv)) : null;
  if (!t) fail('website/privacy.html is missing its <h1> … </main> text');
  else if (sha256(t) !== PRIVACY_TEXT_SHA256) {
    fail('website/privacy.html: its words changed (title, description or text from the <h1> to </main>; compare with git show master:website/privacy.html). The restyle keeps them as they are; a deliberate change updates PRIVACY_TEXT_SHA256 (--privacy-digest website/privacy.html)');
  }
}

// ---------------------------------------------------------------- 2. no third-party loads
const RESOURCE_RELS = /\b(stylesheet|icon|apple-touch-icon|mask-icon|preload|modulepreload|prefetch|preconnect|dns-prefetch|manifest)\b/i;

function cssURLs(css) {
  const out = [];
  for (const m of css.matchAll(/url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)/gi)) out.push(m[1] ?? m[2] ?? m[3] ?? '');
  for (const m of css.matchAll(/@import\s+(?:url\()?\s*["']?([^"')\s;]+)/gi)) out.push(m[1]);
  return out;
}
const jsImports = (js) => [
  ...[...js.matchAll(/\bimport\s+(?:[\w*{}\s,]+\s+from\s+)?["']([^"']+)["']/g)].map((m) => m[1]),
  ...[...js.matchAll(/\bimport\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]),
];

function thirdParty() {
  for (const f of htmlFiles) {
    const html = stripComments(read(f));
    const where = rel(f);
    for (const { attrs } of tags(html, 'link')) {
      if (RESOURCE_RELS.test(attrs.rel || '') && attrs.href && isExternal(attrs.href)) {
        fail(`${where}: <link rel="${attrs.rel}"> loads ${attrs.href} from another site (self-host it)`);
      }
    }
    for (const name of ['img', 'script', 'source', 'video', 'audio', 'iframe', 'embed', 'track', 'input', 'object', 'image', 'use']) {
      for (const { attrs } of tags(html, name)) {
        const hrefs = name === 'use' || name === 'image' ? [attrs.href, attrs['xlink:href']] : [];
        const urls = [attrs.src, attrs.poster, attrs.data, ...hrefs, ...srcsetURLs(attrs.srcset)].filter(Boolean);
        for (const u of urls) if (isExternal(u)) fail(`${where}: <${name}> loads ${u} from another site`);
      }
    }
    for (const { attrs } of tags(html, '[a-z][\\w-]*')) {
      if (attrs.style) for (const u of cssURLs(attrs.style)) if (isExternal(u)) fail(`${where}: a style attribute loads ${u}`);
    }
    for (const m of html.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/gi)) {
      for (const u of cssURLs(m[1])) if (isExternal(u)) fail(`${where}: an inline <style> loads ${u}`);
    }
    for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
      if (/type\s*=\s*["']?importmap/i.test(m[1])) {
        for (const u of m[2].match(/["'](?:https?:)?\/\/[^"']+["']/g) || []) fail(`${where}: the import map points at ${u}`);
      }
      for (const u of jsImports(m[2])) if (isExternal(u)) fail(`${where}: an inline script imports ${u}`);
    }
  }
  for (const f of cssFiles) for (const u of cssURLs(read(f))) if (isExternal(u)) fail(`${rel(f)}: url()/@import loads ${u} from another site`);
  for (const f of jsFiles) for (const u of jsImports(read(f))) if (isExternal(u)) fail(`${rel(f)}: imports ${u} from another site`);
}

// ---------------------------------------------------------------- 3. nothing stored
const STORAGE = /\b(document\s*\.\s*cookie|localStorage|sessionStorage|indexedDB)\b/;
function noStorage() {
  for (const f of jsFiles) {
    read(f).split('\n').forEach((line, i) => {
      if (STORAGE.test(line)) fail(`${rel(f)}:${i + 1}: ${line.match(STORAGE)[1]} (the site stores nothing and sets no cookies)`);
    });
  }
  for (const f of htmlFiles) {
    for (const m of stripComments(read(f)).matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)) {
      const hit = m[1].match(STORAGE);
      if (hit) fail(`${rel(f)}: an inline script uses ${hit[1]} (the site stores nothing and sets no cookies)`);
    }
  }
}

// ---------------------------------------------------------------- 4. budgets
function stylesOf(page, html) {
  const seen = new Set();
  const visit = (file) => {
    if (!file || seen.has(file) || !existsSync(file)) return;
    seen.add(file);
    for (const m of read(file).matchAll(/@import\s+(?:url\()?\s*["']?([^"')\s;]+)/gi)) visit(fileFor(m[1], file));
  };
  for (const { attrs } of tags(html, 'link')) if (/\bstylesheet\b/i.test(attrs.rel || '')) visit(fileFor(attrs.href || '', page));
  return [...seen];
}

function scriptsOf(page, html) {
  const seen = new Set();
  const visit = (file) => {
    if (!file || seen.has(file) || !existsSync(file)) return;
    seen.add(file);
    for (const u of jsImports(read(file))) visit(fileFor(u, file));
  };
  for (const { attrs } of tags(html, 'script')) if (attrs.src) visit(fileFor(attrs.src, page));
  let inline = 0;
  for (const m of html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)) {
    inline += Buffer.byteLength(m[1]);
    for (const u of jsImports(m[1])) visit(fileFor(u, page));
  }
  return { files: [...seen], inline };
}

function fontsOf(cssList, inlineCSS) {
  const fonts = new Set();
  const scan = (css, from) => {
    for (const face of css.match(/@font-face\s*{[^}]*}/gi) || []) {
      for (const u of cssURLs(face)) {
        const f = fileFor(u, from);
        if (f) fonts.add(f);
      }
    }
  };
  for (const f of cssList) scan(read(f), f);
  for (const [css, from] of inlineCSS) scan(css, from);
  return [...fonts];
}

function imagesOf(page, html) {
  let total = 0;
  const pictures = [...html.matchAll(/<picture\b[\s\S]*?<\/picture>/gi)];
  const inPicture = (i) => pictures.some((m) => i > m.index && i < m.index + m[0].length);
  const largest = (urls) => Math.max(0, ...urls.map((u) => size(fileFor(u, page))));
  for (const m of pictures) {
    const img = tags(m[0], 'img')[0];
    if (!img || img.attrs.loading === 'lazy') continue;
    const urls = [img.attrs.src, ...srcsetURLs(img.attrs.srcset)];
    for (const s of tags(m[0], 'source')) urls.push(...srcsetURLs(s.attrs.srcset));
    total += largest(urls.filter(Boolean));
  }
  for (const img of tags(html, 'img')) {
    if (inPicture(img.index) || img.attrs.loading === 'lazy') continue;
    total += largest([img.attrs.src, ...srcsetURLs(img.attrs.srcset)].filter(Boolean));
  }
  return total;
}

const budgetRows = [];
function budgets() {
  for (const page of pages) {
    const raw = read(page);
    const html = stripComments(raw);
    const css = stylesOf(page, html);
    const inlineCSS = [...html.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/gi)].map((m) => [m[1], page]);
    const htmlCss = Buffer.byteLength(raw) + css.reduce((n, f) => n + size(f), 0);
    const { files: js, inline } = scriptsOf(page, html);
    const jsBytes = js.reduce((n, f) => n + size(f), 0) + inline;
    const fonts = fontsOf(css, inlineCSS);
    const fontBytes = fonts.reduce((n, f) => n + size(f), 0);
    for (const f of fonts) if (!existsSync(f)) fail(`${rel(page)}: a declared font is missing: ${rel(f)}`);
    const img = imagesOf(page, html);
    const row = { page: relative(SITE, page), htmlCss, js: jsBytes, fonts: fontBytes, images: img };
    budgetRows.push(row);
    const label = { htmlCss: 'HTML and CSS', js: 'JS', fonts: 'fonts', images: 'first-screen images' };
    for (const [key, limit] of Object.entries(BUDGET)) {
      if (row[key] > limit * 1000) fail(`${row.page}: ${label[key]} ${kb(row[key])} KB, over the ${limit} KB budget`);
    }
  }
}

// ---------------------------------------------------------------- 5. one header, one footer
function chrome() {
  const block = (html, name) => {
    const m = html.match(new RegExp(`<!-- shared ${name}\\b[\\s\\S]*?-->([\\s\\S]*?)<!-- /shared ${name} -->`));
    return m ? m[1] : null;
  };
  // aria-current marks the page itself, and the Try link's target is #try on the home
  // page only; everything else is the same on every page.
  const normalize = (s) => s.replace(/\s+aria-current="page"/g, '').replace(/(\bdata-try\s+href=)"[^"]*"/g, '$1""');
  const ref = {};
  for (const page of pages) {
    const html = read(page);
    const name = relative(SITE, page);
    for (const part of ['header', 'footer']) {
      const b = block(html, part);
      if (b === null) { fail(`${name}: no <!-- shared ${part} --> … <!-- /shared ${part} --> block`); continue; }
      const n = normalize(b);
      if (!ref[part]) ref[part] = { n, name };
      else if (ref[part].n !== n) {
        const a = ref[part].n.split('\n'), c = n.split('\n');
        const k = a.findIndex((l, i) => l !== c[i]);
        fail(`${name}: the shared ${part} differs from ${ref[part].name}'s at its line ${k + 1}:\n    ${ref[part].name}: ${(a[k] ?? '').trim()}\n    ${name}: ${(c[k] ?? '').trim()}`);
      }
      if (part === 'header') {
        const want = name === 'index.html' ? '#try' : '/install.html#trial';
        const got = (b.match(/\bdata-try\s+href="([^"]*)"/) || [])[1];
        if (got !== want) fail(`${name}: the header's Try link goes to ${got ?? 'nowhere'}, not ${want}`);
        for (const m of b.matchAll(/<a href="([^"]*)" aria-current="page">/g)) {
          if (m[1] !== `/${name}`) fail(`${name}: the header marks ${m[1]} as the current page`);
        }
      }
    }
  }
}

// ---------------------------------------------------------------- 6. images, links, colours
function markup() {
  for (const f of htmlFiles) {
    const html = stripComments(read(f));
    const where = rel(f);
    for (const { attrs, raw } of tags(html, 'img')) {
      const missing = ['alt', 'width', 'height'].filter((a) => !(a in attrs));
      if (missing.length) fail(`${where}: <img> without ${missing.join(', ')}: ${raw.slice(0, 100)}`);
    }
    for (const { attrs } of tags(html, 'a')) {
      const h = attrs.href || '';
      if (isExternal(h) && !LINKS.some((re) => re.test(h))) fail(`${where}: links to ${h}, which is not one of the site's allowed links (site/shared.md)`);
    }
    // the light-theme audit: colours come from the tokens, never from markup
    for (const { attrs, raw } of tags(html, '[a-z][\\w-]*')) {
      if (attrs.style && /#[0-9a-f]{3,8}\b/i.test(attrs.style)) fail(`${where}: a hex colour in a style attribute: ${raw.slice(0, 100)}`);
      for (const a of ['fill', 'stroke']) if (attrs[a] && /^#|^rgb/i.test(attrs[a])) fail(`${where}: an inline SVG ${a}="${attrs[a]}" (use the tokens)`);
    }
  }
  for (const f of cssFiles) {
    if (f.endsWith('/tokens.css')) continue;
    read(f).split('\n').forEach((line, i) => {
      const code = line.replace(/\/\*.*?\*\//g, '').replace(/url\("data:[^"]*"\)/g, '');
      if (/:[^;{}]*#[0-9a-f]{3,8}\b/i.test(code)) fail(`${rel(f)}:${i + 1}: a hex colour outside tokens.css (colours come from the tokens)`);
    });
  }
}

// ---------------------------------------------------------------- 7. the mark's files
// The site never redraws the mark: its copies are the masters, byte for byte.
function marks() {
  const masters = join(ROOT, 'plans/brand/marks');
  const same = (a, b) => existsSync(a) && existsSync(b) && readFileSync(a).equals(readFileSync(b));
  for (const [site, master] of [['img/mark.svg', 'mark.svg'], ['favicon.svg', 'favicon.svg']]) {
    if (!same(join(SITE, site), join(masters, master))) fail(`website/${site} is not plans/brand/marks/${master} (copy the master; never redraw the mark)`);
  }
  if (!['wordmark-a.svg', 'wordmark-b.svg'].some((m) => same(join(SITE, 'img/wordmark.svg'), join(masters, m)))) {
    fail('website/img/wordmark.svg is neither plans/brand/marks/wordmark-a.svg nor wordmark-b.svg');
  }
}

// ---------------------------------------------------------------- 8. media over 1 MiB
function media() {
  const lock = join(SITE, 'media.lock');
  if (!existsSync(lock)) { fail('website/media.lock is missing'); return; }
  read(lock).split('\n').forEach((line, i) => {
    if (!line.trim() || line.startsWith('#')) return;
    const m = line.match(/^([0-9a-f]{64})\s+(\S+)\s+(\S.*)$/);
    if (!m) { fail(`website/media.lock:${i + 1}: want "<sha256> <path> <source>"`); return; }
    const [, sum, path] = m;
    if (path.startsWith('/') || path.split('/').includes('..')) { fail(`website/media.lock:${i + 1}: ${path} is not a path under website/media/`); return; }
    if (!DIST) return;
    const f = join(SITE, 'media', path);
    if (!existsSync(f)) fail(`website/media/${path} is missing (website/media.lock:${i + 1} names it; get it from its source)`);
    else if (sha256(readFileSync(f)) !== sum) fail(`website/media/${path}: its sha256 is not the one website/media.lock:${i + 1} pins`);
  });
}

// ---------------------------------------------------------------- run
preserved();
thirdParty();
noStorage();
budgets();
chrome();
markup();
marks();
media();

const todos = { data: 0, copy: 0, shot: 0, page: [] };
for (const f of htmlFiles) {
  const html = read(f);
  // slots are marks on the page; a TODO-COPY gap is a comment by design
  const shown = stripComments(html);
  todos.data += (shown.match(/<mark data-todo="data">/g) || []).length;
  todos.copy += (html.match(/TODO-COPY/g) || []).length;
  todos.shot += (shown.match(/<mark data-todo="shot">/g) || []).length;
  if (/<main\b[^>]*data-todo="page"/.test(html)) todos.page.push(relative(SITE, f));
}
const pad = (s, n) => String(s).padEnd(n);
console.log(`website: ${pages.length} pages; budgets in KB (HTML+CSS ≤ ${BUDGET.htmlCss}, JS ≤ ${BUDGET.js}, fonts ≤ ${BUDGET.fonts}, first-screen images ≤ ${BUDGET.images})`);
for (const r of budgetRows) console.log(`  ${pad(r.page, 15)} html+css ${pad(kb(r.htmlCss), 6)} js ${pad(kb(r.js), 6)} fonts ${pad(kb(r.fonts), 7)} images ${kb(r.images)}`);
if (todos.data || todos.copy || todos.shot || todos.page.length) {
  notes.push(`open: ${todos.data} data slot(s) waiting for the research pass, ${todos.copy} TODO-COPY gap(s), ${todos.shot} shot(s) waiting for their capture (shots.todo.md), pages still stubs: ${todos.page.join(', ') || 'none'}`);
}
for (const n of notes) console.log(`  ${n}`);
if (failures.length) {
  console.error(`website-check: ${failures.length} problem(s)`);
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log('website-check: ok');
