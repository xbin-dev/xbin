#!/usr/bin/env node
// hack/theme-fallbacks.mjs — keep the literal fallbacks in `var(--bx-x, …)`
// equal to web/theme.css's Concrete Night (D184). Core elements carry a
// fallback on every token so a bare document (one that never linked
// theme.css) still renders; those literals once drifted to an old light
// palette (var(--bx-panel, #fff) against a #23272e theme) and nothing
// noticed. The theme is never injected into documents (docs/compat.md
// rule 5: a document turns light only when it opts in), so the fallbacks
// ARE the theme for bare documents, and they are Night's values.
//
//   node hack/theme-fallbacks.mjs [path…]        # check: exit 1 on any disagreement (make theme-check)
//   node hack/theme-fallbacks.mjs --fix [path…]  # rewrite the fallbacks in place
//
// Paths (files or directories) limit the scan; none = every tree below.
//
// The tokens are theme.css's first `:root { … }` block, comments stripped.
// Old names and composites there are var() references (--bx-red:
// var(--bx-danger), the focus ring's outline and halo, the type shorthands);
// they are resolved to Night literals, so a fallback reads
// var(--bx-red, #FF7A7A), never var(--bx-red, var(--bx-danger)). Font tokens
// (--bx-font, --bx-font-*, --bx-sans, --bx-mono, --bx-display) are exempt: a
// fallback may abbreviate the stack. Tokens theme.css doesn't define are
// left alone here; hack/theme-lint.mjs counts their fallbacks as hard-coded
// colours.
//
// It also checks that the Day block, which theme.css carries twice (the
// person's light, and the system's light without a dark override), is the
// same text in both places: the declarations between each
// `/* bx-day:start */` and `/* bx-day:end */` marker pair.
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, extname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const ROOT = new URL('..', import.meta.url).pathname.replace(/\/$/, '');
export const TREES = ['web', 'workspace-template', 'builtin-tiles', 'builtin-templates', 'examples', 'hack/demo/tiles'];
const SKIP = new Set(['vendor', 'node_modules', 'deps', 'data', '.git']);
const EXTS = new Set(['.js', '.mjs', '.html', '.css']);

export const isFontToken = (name) => /^--bx-(font(-[a-z0-9-]+)?|sans|mono|display)$/.test(name);

// CSS comments out, newlines kept (line numbers stay true).
export const stripCSSComments = (css) => css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ' '));

// The balanced end of a parenthesised group opening at src[open] === '('.
function closeOf(src, open) {
  let depth = 0;
  for (let k = open; k < src.length; k++) {
    if (src[k] === '(') depth++;
    else if (src[k] === ')' && --depth === 0) return k;
  }
  return -1;
}

// readTheme(css) → {tokens, raw}: the first :root block's custom properties,
// var() references resolved against the block itself (to Night literals).
export function readTheme(css) {
  const text = stripCSSComments(css);
  const m = /(^|[\s}]):root\s*\{/.exec(text);
  if (!m) throw new Error('theme.css: no :root block');
  const open = m.index + m[0].length - 1;
  let depth = 0, end = -1;
  for (let k = open; k < text.length; k++) {
    if (text[k] === '{') depth++;
    else if (text[k] === '}' && --depth === 0) { end = k; break; }
  }
  if (end < 0) throw new Error('theme.css: the :root block never closes');
  const raw = {};
  for (const d of text.slice(open + 1, end).matchAll(/(--bx-[a-z0-9-]+)\s*:\s*([^;]+);/g)) raw[d[1]] = d[2].replace(/\s+/g, ' ').trim();
  const tokens = {};
  const resolving = new Set();
  const resolveValue = (v) => {
    let out = '';
    for (let i = 0; ;) {
      const at = v.indexOf('var(', i);
      if (at < 0) return out + v.slice(i);
      const close = closeOf(v, at + 3);
      if (close < 0) return out + v.slice(i);
      const inner = v.slice(at + 4, close);
      const comma = inner.indexOf(',');
      const name = (comma < 0 ? inner : inner.slice(0, comma)).trim();
      const fb = comma < 0 ? null : inner.slice(comma + 1).trim();
      out += v.slice(i, at) + (name in raw ? get(name) : fb !== null ? resolveValue(fb) : v.slice(at, close + 1));
      i = close + 1;
    }
  };
  const get = (name) => {
    if (name in tokens) return tokens[name];
    if (resolving.has(name)) throw new Error(`theme.css: ${name} refers to itself`);
    resolving.add(name);
    tokens[name] = resolveValue(raw[name]);
    resolving.delete(name);
    return tokens[name];
  };
  for (const name of Object.keys(raw)) get(name);
  return { tokens, raw };
}

// dayBlocks(css) → the declaration lists between the bx-day markers.
export function dayBlocks(css) {
  const out = [];
  for (const b of css.matchAll(/\/\*\s*bx-day:start\s*\*\/([\s\S]*?)\/\*\s*bx-day:end\s*\*\//g)) {
    out.push(stripCSSComments(b[1]).split(';').map((d) => d.replace(/\s+/g, ' ').trim()).filter(Boolean));
  }
  return out;
}

// checkDayBlocks(css) → problems: two blocks, identical, every token in them
// defined in Night (or the fallback rule would have nothing to say).
export function checkDayBlocks(css, tokens = readTheme(css).tokens) {
  const blocks = dayBlocks(css);
  const problems = [];
  if (blocks.length !== 2) {
    problems.push(`web/theme.css: ${blocks.length} bx-day blocks, want 2 (the person's light; the system's light)`);
    return problems;
  }
  const [a, b] = blocks;
  const n = Math.max(a.length, b.length);
  for (let i = 0; i < n; i++) {
    if (a[i] !== b[i]) {
      problems.push(`web/theme.css: the two Day blocks differ at declaration ${i + 1}: "${a[i] ?? '(none)'}" vs "${b[i] ?? '(none)'}" — keep the bx-day blocks identical`);
      break;
    }
  }
  for (const d of a) {
    const name = d.split(':')[0].trim();
    if (name.startsWith('--') && !(name in tokens)) problems.push(`web/theme.css: Day sets ${name}, which Night's :root doesn't define (no fallback can follow it)`);
  }
  return problems;
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    if (SKIP.has(name) || name.startsWith('.')) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) yield* walk(p);
    else if (EXTS.has(extname(p))) yield p;
  }
}

// files(root, paths): the files a run covers.
export function* files(root, paths = []) {
  const inputs = paths.length ? paths.map((p) => resolve(p)) : TREES.map((t) => join(root, t));
  for (const p of inputs) {
    let st;
    try { st = statSync(p); } catch { continue; }
    if (st.isDirectory()) yield* walk(p);
    else if (EXTS.has(extname(p))) yield p;
  }
}

// Every var(--bx-x, <fallback>) with balanced parentheses in the fallback.
export function* occurrences(src) {
  let i = 0;
  for (;;) {
    const at = src.indexOf('var(--bx-', i);
    if (at < 0) return;
    const close = closeOf(src, at + 3);
    if (close < 0) return;
    const inner = src.slice(at + 4, close);
    const comma = inner.indexOf(',');
    i = at + 4; // nested var()s in a fallback are occurrences of their own
    if (comma < 0) continue;
    yield { start: at, end: close + 1, token: inner.slice(0, comma).trim(), fallback: inner.slice(comma + 1).trim() };
  }
}

const norm = (v) => v.replace(/\s+/g, ' ').replace(/\s*,\s*/g, ',').replace(/\(\s+/g, '(').replace(/\s+\)/g, ')').trim().toLowerCase();

// scan({root, paths, tokens, fix}) → {problems, fixed, files}: problems are
// "file:line: …" strings (check mode); fix rewrites in place.
export function scan({ root = ROOT, paths = [], tokens, fix = false }) {
  const problems = [];
  let fixed = 0, changedFiles = 0;
  for (const file of files(root, paths)) {
    const src = readFileSync(file, 'utf8');
    let out = '', last = 0;
    const edits = [];
    for (const o of occurrences(src)) {
      const want = tokens[o.token];
      if (want === undefined || isFontToken(o.token) || norm(want) === norm(o.fallback)) continue;
      if (o.fallback.includes('var(')) {
        // a fallback holding another var(): its own occurrence is checked;
        // the outer one is right when that var() names the same value
        const inner = o.fallback.match(/^var\((--bx-[a-z0-9-]+)/);
        if (inner && norm(tokens[inner[1]] ?? '') === norm(want)) continue;
      }
      if (edits.length && o.start < edits.at(-1).end) continue; // inside an outer edit
      edits.push(o);
    }
    for (const o of edits) {
      const want = tokens[o.token];
      const line = src.slice(0, o.start).split('\n').length;
      if (fix) {
        out += src.slice(last, o.start) + `var(${o.token}, ${want})`;
        last = o.end;
        fixed++;
      } else {
        problems.push(`${relative(root, file)}:${line}: var(${o.token}, ${o.fallback}) — theme.css says ${want}`);
      }
    }
    if (fix && edits.length) { writeFileSync(file, out + src.slice(last)); changedFiles++; }
  }
  return { problems, fixed, files: changedFiles };
}

function main(argv) {
  const fix = argv.includes('--fix');
  const paths = argv.filter((a) => !a.startsWith('--'));
  const css = readFileSync(join(ROOT, 'web', 'theme.css'), 'utf8');
  const { tokens } = readTheme(css);
  const day = checkDayBlocks(css, tokens);
  const { problems, fixed, files: n } = scan({ paths, tokens, fix });
  for (const p of day) console.error(p);
  if (fix) {
    console.log(`theme-fallbacks: rewrote ${fixed} fallback(s) in ${n} file(s) to match web/theme.css`);
  } else {
    for (const p of problems) console.error(p);
    if (problems.length) console.error(`theme-fallbacks: ${problems.length} fallback(s) disagree with web/theme.css — run: node hack/theme-fallbacks.mjs --fix`);
  }
  if (day.length || (!fix && problems.length)) process.exit(1);
  if (!fix) console.log(`theme-fallbacks: every var(--bx-*, …) fallback matches web/theme.css (${Object.keys(tokens).length} tokens; the Day blocks agree)`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) main(process.argv.slice(2));
