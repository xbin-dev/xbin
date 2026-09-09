#!/usr/bin/env node
// hack/theme-fallbacks.mjs — keep the literal fallbacks in `var(--bx-x, …)`
// equal to web/theme.css. Core elements carry a fallback on every token so a
// bare document (one that never linked theme.css) still renders; those
// literals drifted to an old light palette (var(--bx-panel, #fff) against a
// #23272e theme) and nothing noticed. The theme is never injected into
// documents (docs/compat.md rule 5 — theme.css sets color-scheme, which would
// flip a third-party tile), so the fallbacks ARE the theme for bare documents.
//
//   node hack/theme-fallbacks.mjs          # check: exit 1 on any disagreement (make theme-check)
//   node hack/theme-fallbacks.mjs --fix    # rewrite the fallbacks in place
//
// Font tokens (--bx-font, --bx-mono) are exempt: a fallback may abbreviate
// the stack. Tokens theme.css does not define are left alone.
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, extname } from 'node:path';

const ROOT = new URL('..', import.meta.url).pathname.replace(/\/$/, '');
const TREES = ['web', 'workspace-template', 'builtin-tiles', 'builtin-templates', 'examples'];
const SKIP = new Set(['vendor', 'node_modules', 'deps', 'data', '.git']);
const EXEMPT = new Set(['--bx-font', '--bx-mono']);
const fix = process.argv.includes('--fix');

// theme tokens: the :root block of theme.css
const theme = {};
const css = readFileSync(join(ROOT, 'web', 'theme.css'), 'utf8');
const root = css.match(/:root\s*\{([\s\S]*?)\n\}/);
if (!root) throw new Error('web/theme.css: no :root block');
for (const m of root[1].matchAll(/(--bx-[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
  theme[m[1]] = m[2].replace(/\/\*[\s\S]*?\*\//g, '').replace(/\s+/g, ' ').trim();
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    if (SKIP.has(name) || name.startsWith('.')) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) yield* walk(p);
    else if (['.js', '.mjs', '.html', '.css'].includes(extname(p))) yield p;
  }
}

// Find every var(--bx-x, <fallback>) with balanced parentheses in the fallback.
function* occurrences(src) {
  let i = 0;
  for (;;) {
    const at = src.indexOf('var(--bx-', i);
    if (at < 0) return;
    let k = at + 4, depth = 1;
    while (k < src.length && depth) {
      const c = src[k];
      if (c === '(') depth++;
      else if (c === ')') depth--;
      k++;
    }
    const inner = src.slice(at + 4, k - 1);
    const comma = inner.indexOf(',');
    i = k;
    if (comma < 0) continue; // no fallback
    yield { start: at, end: k, token: inner.slice(0, comma).trim(), fallback: inner.slice(comma + 1).trim() };
  }
}

const norm = (v) => v.replace(/\s+/g, ' ').replace(/,\s*/g, ',').trim().toLowerCase();

let problems = 0, fixed = 0, files = 0;
for (const tree of TREES) {
  let dir;
  try { dir = join(ROOT, tree); statSync(dir); } catch { continue; }
  for (const file of walk(dir)) {
    const src = readFileSync(file, 'utf8');
    let out = '', last = 0, changed = false;
    for (const o of occurrences(src)) {
      const want = theme[o.token];
      if (!want || EXEMPT.has(o.token) || norm(want) === norm(o.fallback)) continue;
      const line = src.slice(0, o.start).split('\n').length;
      if (fix) {
        out += src.slice(last, o.start) + `var(${o.token}, ${want})`;
        last = o.end; changed = true; fixed++;
      } else {
        console.error(`${relative(ROOT, file)}:${line}: var(${o.token}, ${o.fallback}) — theme.css says ${want}`);
        problems++;
      }
    }
    if (changed) { writeFileSync(file, out + src.slice(last)); files++; }
  }
}
if (fix) {
  console.log(`theme-fallbacks: rewrote ${fixed} fallback(s) in ${files} file(s) to match web/theme.css`);
} else if (problems) {
  console.error(`theme-fallbacks: ${problems} fallback(s) disagree with web/theme.css — run: node hack/theme-fallbacks.mjs --fix`);
  process.exit(1);
} else {
  console.log(`theme-fallbacks: every var(--bx-*, …) fallback matches web/theme.css (${Object.keys(theme).length} tokens)`);
}
