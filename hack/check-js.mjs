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
import { join, relative, extname } from 'node:path';
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

for (const file of files) {
  const ext = extname(file);
  if (ext === '.js' || ext === '.mjs') {
    checked++;
    const err = nodeCheck(file);
    if (err) problems.push(`${relative(ROOT, file)}:\n${err.trim()}`);
  } else if (ext === '.html') {
    checkHTML(file);
  }
}
rmSync(tmp, { recursive: true, force: true });

if (problems.length) {
  console.error(problems.join('\n\n'));
  console.error(`\njs-check: ${problems.length} file(s) with syntax errors (${checked} inputs checked)`);
  process.exit(1);
}
console.log(`js-check: ${checked} scripts parse (files + inline module blocks)`);
