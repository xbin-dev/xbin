#!/usr/bin/env node
// native/tools/fixture.mjs — run the native fixtures (native/fixtures/<name>/)
// and compare what they render with their expected.json: the contract every
// renderer (the SwiftUI app, the Lit reference renderer) is tested against.
//
//   node native/tools/fixture.mjs [--check] [name…]   compare (the default); exit 1 on any difference
//   node native/tools/fixture.mjs --update [name…]    write expected.json, printing what changed
//   node native/tools/fixture.mjs --print name        print the rendered tree
//   node native/tools/fixture.mjs --messages name     print every runtime → app message
//   node native/tools/fixture.mjs --coverage [name…]  what the fixtures exercise of the vocabulary
//   node native/tools/fixture.mjs --list              the fixtures, with their `about`
//
// Each fixture's native.js runs in node (hack/xbn/node.mjs, xb-native's JSON
// target) against a scripted xbin built from its data.json, on a virtual
// clock pinned to `now`, in the pinned time zone and locale; its
// `interactions` then play in order and the run settles. A run fails on a
// runtime error, a warn/error diagnostic (unless its code is listed in
// `allowDiagnostics`), or a request no route answers. The format is
// native/fixtures/README.md.
//
// A full --check (no names) also fails when the fixtures together leave any
// part of the vocabulary unexercised (native/tools/coverage.mjs), or when
// native/fixtures/README.md does not list a fixture.
import { writeFileSync, readFileSync } from 'node:fs';
import { join, relative, resolve as resolvePath } from 'node:path';
import { fileURLToPath } from 'node:url';
import { availableParallelism } from 'node:os';
import { runNative } from '../../hack/xbn/node.mjs';
import { VOCAB } from '../../web/xb/vocab.js';
import { ROOT, FIXTURES, listFixtures, loadFixture, format, treeDiff, countNodes } from './fixture-lib.mjs';
import { report } from './coverage.mjs';

const MAX_DIFF_LINES = 40;

function parseArgs(argv) {
  const o = { mode: 'check', names: [], coverage: false };
  for (const a of argv) {
    if (a === '--check' || a === '--update' || a === '--print' || a === '--messages' || a === '--list') o.mode = a.slice(2);
    else if (a === '--coverage') o.coverage = true;
    else if (a === '-h' || a === '--help') o.mode = 'help';
    else if (a.startsWith('-')) throw new Error(`unknown flag ${a}`);
    else o.names.push(a.replace(/\/+$/, '').split('/').pop());
  }
  return o;
}

// runFixture(fx) → {tree, problems: [..], notes: [..], result}
export async function runFixture(fx) {
  const problems = [];
  const notes = [];
  let r;
  try {
    r = await runNative({ ...fx.run, timeout: 60000 });
  } catch (e) {
    const msg = String(e?.message ?? e).split('\n').slice(0, 6).join('\n    ');
    problems.push(`the run failed: ${msg}`);
    return { tree: e?.result?.tree ?? null, problems, notes, result: e?.result ?? null };
  }
  const allow = new Set(fx.data.allowDiagnostics || []);
  for (const m of r.errors) problems.push(`runtime error (${m.kind}): ${m.message}${m.where ? ` (${m.where})` : ''}`);
  for (const d of r.diagnostics) {
    if (d.level === 'info' || allow.has(d.code)) continue;
    problems.push(`diagnostic ${d.level} ${d.code}: ${d.message}${d.where ? ` (${d.where})` : ''}`);
  }
  for (const u of r.unmatched) problems.push(`no route answers ${u} (add it to data.json "routes")`);
  const used = new Set(r.requests.map((q) => q.url));
  for (const key of Object.keys(fx.data.routes || {})) {
    const path = key.replace(/^[A-Z]+ /, '');
    if (![...used].some((u) => u === path || u.split('?')[0] === path)) notes.push(`route ${key} is never requested`);
  }
  return { tree: r.tree, problems, notes, result: r };
}

async function pool(items, limit, fn) {
  const out = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) { const i = next++; out[i] = await fn(items[i], i); }
  }));
  return out;
}

function printLines(lines, indent = '    ', max = MAX_DIFF_LINES) {
  for (const l of lines.slice(0, max)) console.log(`${indent}${l}`);
  if (lines.length > max) console.log(`${indent}… ${lines.length - max} more`);
}

async function main() {
  const o = parseArgs(process.argv.slice(2));
  if (o.mode === 'help') {
    const lines = readFileSync(new URL(import.meta.url), 'utf8').split('\n').slice(1);
    console.log(lines.slice(0, lines.findIndex((l) => !l.startsWith('//'))).map((l) => l.replace(/^\/\/ ?/, '')).join('\n'));
    return 0;
  }
  const all = listFixtures();
  const unknown = o.names.filter((n) => !all.includes(n));
  if (unknown.length) { console.error(`no fixture ${unknown.join(', ')} in ${relative(process.cwd(), FIXTURES) || FIXTURES}`); return 2; }
  const names = o.names.length ? o.names : all;
  if (o.mode === 'list') {
    for (const n of names) {
      let about = '';
      try { about = JSON.parse(readFileSync(join(FIXTURES, n, 'data.json'), 'utf8')).about ?? ''; } catch { /* no data.json */ }
      console.log(`${n.padEnd(24)} ${about}`);
    }
    return 0;
  }
  if (!names.length) { console.error('no fixtures'); return 2; }

  const fixtures = [];
  let failed = 0;
  for (const n of names) {
    try { fixtures.push(loadFixture(n)); } catch (e) { console.log(`FAIL ${n}\n    ${e.message}`); failed++; }
  }
  const runs = await pool(fixtures, Math.max(2, Math.min(8, availableParallelism())), (fx) => runFixture(fx));
  const trees = {};

  for (let i = 0; i < fixtures.length; i++) {
    const fx = fixtures[i];
    const { tree, problems, notes, result } = runs[i];
    if (tree) trees[fx.name] = tree;
    if (o.mode === 'print') { process.stdout.write(tree ? format(tree) : 'null\n'); if (problems.length) { printLines(problems, '# '); failed++; } continue; }
    if (o.mode === 'messages') {
      for (const m of result?.messages ?? []) console.log(JSON.stringify(m));
      if (problems.length) { printLines(problems, '# '); failed++; }
      continue;
    }
    const lines = [...problems];
    const file = join(fx.dir, 'expected.json');
    const size = tree ? `${countNodes(tree.root)} nodes, ${fx.run.steps.length} steps` : 'no tree';
    if (o.mode === 'update') {
      if (!tree || problems.length) { console.log(`FAIL ${fx.name} (not updated)`); printLines(lines); failed++; continue; }
      const text = format(tree);
      if (text === fx.expectedText) { console.log(`ok   ${fx.name.padEnd(24)} unchanged (${size})`); continue; }
      writeFileSync(file, text);
      if (!fx.expected) console.log(`new  ${fx.name.padEnd(24)} wrote ${relative(ROOT, file)} (${size}) — read it before committing`);
      else { console.log(`upd  ${fx.name.padEnd(24)} rewrote ${relative(ROOT, file)} (${size}); review the change:`); printLines(treeDiff(fx.expected, tree)); }
      for (const n of notes) console.log(`    note: ${n}`);
      continue;
    }
    // check
    if (!fx.expected) lines.push(`no expected.json (run: node native/tools/fixture.mjs --update ${fx.name}, then read it)`);
    else if (tree) {
      const d = treeDiff(fx.expected, tree);
      lines.push(...d);
      if (!d.length && format(tree) !== fx.expectedText) lines.push('expected.json matches but is not in the canonical layout (--update rewrites it)');
    }
    if (lines.length) { console.log(`FAIL ${fx.name}`); printLines(lines); failed++; } else console.log(`ok   ${fx.name.padEnd(24)} ${size}`);
    for (const n of notes) console.log(`    note: ${n}`);
  }

  if (o.mode === 'check' || o.mode === 'update' || o.coverage) {
    const full = !o.names.length;
    // coverage over what is committed (expected.json), or the fresh trees under --update
    const basis = {};
    for (const fx of fixtures) basis[fx.name] = o.mode === 'update' ? trees[fx.name] ?? fx.expected : fx.expected ?? trees[fx.name];
    for (const [n, t] of Object.entries(basis)) if (!t) delete basis[n];
    const cov = report(VOCAB, basis);
    if (o.coverage) {
      const items = Object.keys(cov.by).sort();
      for (const it of items) console.log(`${it.padEnd(48)} ${cov.by[it].join(' ')}`);
    }
    if (full && o.mode === 'check') {
      // the README's index names every fixture (docs stay true)
      let readme = '';
      try { readme = readFileSync(join(FIXTURES, 'README.md'), 'utf8'); } catch { /* reported below */ }
      const unlisted = names.filter((n) => !readme.includes(`\`${n}\``));
      if (unlisted.length) { console.log(`FAIL native/fixtures/README.md does not list ${unlisted.map((n) => `\`${n}\``).join(', ')}`); failed++; }
    }
    const line = `coverage: ${cov.covered}/${cov.total} of the vocabulary exercised${full ? '' : ` by ${names.join(', ')}`}`;
    if (cov.missing.length && !full && !o.coverage) console.log(`${line} (--coverage lists what is missing)`);
    else if (cov.missing.length) {
      console.log(`${line}; missing:`);
      printLines(cov.missing, '    ', 200);
      if (full && o.mode === 'check') failed++;
    } else console.log(line);
  }
  if (failed) console.log(`${failed} failed`);
  return failed ? 1 : 0;
}

if (process.argv[1] && resolvePath(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then((code) => process.exit(code), (e) => { console.error(String(e?.stack ?? e)); process.exit(2); });
}
