#!/usr/bin/env node
// hack/check-js.mjs — parse-check every shipped JavaScript file, including the
// inline <script type="module"> blocks in component HTML (which node --check
// cannot see on its own, and which hold ~1,000 lines of tile UI). No build
// step, no dependencies: this is `node --check` run over the right inputs,
// with inline blocks reported at their original file:line.
//
//   node hack/check-js.mjs            # make js-check
//   node hack/check-js.mjs path…      # only these files/dirs
//
// Exit 1 on the first-found syntax errors (all are printed).
import { readFileSync, readdirSync, statSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { join, relative, extname, dirname } from 'node:path';
import { tmpdir } from 'node:os';

const ROOT = new URL('..', import.meta.url).pathname.replace(/\/$/, '');
const TREES = ['web', 'workspace-template', 'builtin-tiles', 'builtin-templates', 'examples', 'hack'];
const SKIP_DIRS = new Set(['vendor', 'node_modules', 'deps', 'data', '.git', 'dist', 'out']);

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    if (SKIP_DIRS.has(name) || name.startsWith('.')) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) yield* walk(p);
    else if (st.isFile()) yield p;
  }
}

const args = process.argv.slice(2);
const inputs = args.length ? args.map((a) => join(process.cwd(), a)) : TREES.map((t) => join(ROOT, t));
const files = [];
for (const p of inputs) {
  let st;
  try { st = statSync(p); } catch { continue; }
  if (st.isDirectory()) files.push(...walk(p));
  else files.push(p);
}

const tmp = mkdtempSync(join(tmpdir(), 'xbin-js-check-'));
const problems = [];
let checked = 0;

// nodeCheck runs `node --check` on a file and returns the error text ('' = ok).
function nodeCheck(file) {
  const r = spawnSync(process.execPath, ['--check', file], { encoding: 'utf8' });
  return r.status === 0 ? '' : (r.stderr || r.stdout || 'syntax error');
}

// Inline blocks: <script …>…</script> without src=, not importmap/JSON.
const SCRIPT = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi;
function checkHTML(file) {
  const src = readFileSync(file, 'utf8');
  const rel = relative(ROOT, file);
  let m;
  let n = 0;
  while ((m = SCRIPT.exec(src))) {
    const attrs = m[1];
    if (/\bsrc\s*=/.test(attrs)) continue;
    const type = (attrs.match(/\btype\s*=\s*["']?([^"'\s>]+)/) || [])[1] || '';
    if (type && type !== 'module' && !/javascript/.test(type)) continue; // importmap, JSON, templates
    const startLine = src.slice(0, m.index + m[0].indexOf('>') + 1).split('\n').length;
    const out = join(tmp, `block-${++n}${type === 'module' ? '.mjs' : '.cjs'}`);
    writeFileSync(out, m[2]);
    const err = nodeCheck(out);
    checked++;
    if (err) {
      // node prints "<path>:<line>" — rebase the line onto the HTML file.
      const fixed = err.replace(new RegExp(out.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ':(\\d+)', 'g'),
        (_, line) => `${rel}:${startLine + Number(line) - 1}`);
      problems.push(`${rel}: inline <script> #${n} (line ${startLine}):\n${fixed.trim()}`);
    }
  }
}

// The frontend kit (web/bx-kit.js, docs/frontend-kit.md) is the one home of
// these helpers; a second definition anywhere shipped is the drift this
// check exists to stop. bx-code.js keeps the highlight helpers it exports.
const KIT = { 'web/bx-kit.js': /^(?:export )?(?:const|function|async function) (api|xbinApi|selfApi|jbody|esc|deepActive|pathHas|clampBox|clampWin)\b/,
  'web/bx-code.js': /^(?:export )?(?:const|function) (escHTML|langFor|hl|diffHTML|LANG_BY_EXT)\b/ };
function checkKitDuplicates(file) {
  const rel = relative(ROOT, file);
  if (rel.startsWith('hack/')) return;
  const src = readFileSync(file, 'utf8');
  src.split('\n').forEach((line, i) => {
    for (const [home, re] of Object.entries(KIT)) {
      const m = line.match(re);
      if (m && rel !== home) problems.push(`${rel}:${i + 1}: defines ${m[1]}() — import it from /vendor/${home.slice(4)} instead (docs/frontend-kit.md)`);
    }
  });
}

// Named imports must exist as exports of the module they name — a relative
// import (./x.js, ../x.js) or a kit URL (/vendor/x.js → web/x.js). `node
// --check` parses each file alone, so a renamed or mislocated export only
// failed in the browser (the harness found one that way). Bare specifiers
// (the import map) and vendored bundles are not this check's business.
const IMPORT = /^import\s+(?:[\w$]+\s*,\s*)?\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]/gm;
const EXPORTS = new Map(); // resolved file → Set of names, or null when unknowable
function exportsOf(file) {
  if (EXPORTS.has(file)) return EXPORTS.get(file);
  let names = null;
  try {
    const src = readFileSync(file, 'utf8');
    names = new Set();
    for (const m of src.matchAll(/^export\s+(?:async\s+)?(?:const|let|var|function\*?|class)\s+([\w$]+)/gm)) names.add(m[1]);
    for (const m of src.matchAll(/^export\s*\{([^}]*)\}/gm)) {
      for (const part of m[1].split(',')) { const p = part.trim().split(/\s+as\s+/); if (p[0]) names.add((p[1] || p[0]).trim()); }
    }
    if (/^export\s+default\b/m.test(src)) names.add('default');
    if (/^export\s+\*\s+from/m.test(src)) names = null; // re-exports everything: not enumerable here
    if (names) names.src = src;
  } catch { names = null; }
  EXPORTS.set(file, names);
  return names;
}
function resolveImport(file, spec) {
  if (spec.startsWith('./') || spec.startsWith('../')) return join(dirname(file), spec);
  if (spec.startsWith('/vendor/') && !spec.includes('.min.')) return join(ROOT, 'web', spec.slice('/vendor/'.length));
  return null;
}
function checkImports(file) {
  const rel = relative(ROOT, file);
  const src = readFileSync(file, 'utf8');
  for (const m of src.matchAll(IMPORT)) {
    const target = resolveImport(file, m[2]);
    if (!target) continue;
    const names = exportsOf(target);
    if (!names) continue;
    for (const part of m[1].split(',')) {
      const name = part.trim().split(/\s+as\s+/)[0].trim();
      if (!name || names.has(name)) continue;
      // `export const a = 1, b = 2` and friends: accept a name any export line mentions
      if (new RegExp('^export\\b[^\\n]*\\b' + name.replace(/\$/g, '\\$') + '\\b', 'm').test(names.src)) continue;
      problems.push(`${rel}: imports ${name} from ${m[2]}, which does not export it`);
    }
  }
}

for (const file of files) {
  const ext = extname(file);
  if (ext === '.js' || ext === '.mjs') {
    checked++;
    const err = nodeCheck(file);
    if (err) problems.push(`${relative(ROOT, file)}:\n${err.trim()}`);
    checkKitDuplicates(file);
    checkImports(file);
  } else if (ext === '.html') {
    checkHTML(file);
  }
}
rmSync(tmp, { recursive: true, force: true });

if (problems.length) {
  console.error(problems.join('\n\n'));
  console.error(`\njs-check: ${problems.length} problem(s) (${checked} inputs checked)`);
  process.exit(1);
}
console.log(`js-check: ${checked} scripts parse (files + inline module blocks)`);
