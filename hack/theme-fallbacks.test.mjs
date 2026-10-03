// hack/theme-fallbacks.test.mjs — hack/theme-fallbacks.mjs (D184), run by
// `make js-test`: theme.css's Night block is read with comments stripped and
// its old names and composites resolved to literals; font tokens are exempt;
// the two Day blocks must agree; paths limit a run; --fix writes Night's
// literals and leaves what's right alone.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { readTheme, checkDayBlocks, dayBlocks, scan, isFontToken } from './theme-fallbacks.mjs';

const css = readFileSync(new URL('../web/theme.css', import.meta.url), 'utf8');

test('the real theme: aliases and composites resolve to Night literals', () => {
  const { tokens, raw } = readTheme(css);
  assert.equal(raw['--bx-red'], 'var(--bx-danger)');
  assert.equal(tokens['--bx-red'], '#FF7A7A');
  assert.equal(tokens['--bx-green'], tokens['--bx-ok']);
  assert.equal(tokens['--bx-amber'], tokens['--bx-warn']);
  assert.equal(tokens['--bx-focus-outline'], '3px solid #3DD6F5');
  assert.equal(tokens['--bx-focus-halo'], '0 0 0 2px #0B0C12');
  assert.equal(tokens['--bx-panel'], '#1F2028', 'Night, not Day');
  assert.equal(tokens['--bx-radius'], '2px');
  assert.ok(!Object.values(tokens).some((v) => /var\(--bx-/.test(v)), 'nothing left unresolved');
  assert.deepEqual(checkDayBlocks(css, tokens), [], 'the two Day blocks agree');
  assert.equal(dayBlocks(css).length, 2);
});

test('comments in :root are not tokens', () => {
  const { tokens } = readTheme(':root {\n  /* --bx-ghost: #fff; is not a token */\n  --bx-bg: #000; /* --bx-other: 1px; */\n}\n');
  assert.deepEqual(Object.keys(tokens), ['--bx-bg']);
});

test('font tokens are exempt; others are not', () => {
  for (const t of ['--bx-font', '--bx-font-ui', '--bx-font-micro', '--bx-sans', '--bx-mono', '--bx-display']) assert.ok(isFontToken(t), t);
  for (const t of ['--bx-fontish', '--bx-text', '--bx-text-size', '--bx-monochrome']) assert.ok(!isFontToken(t), t);
});

test('the Day blocks must stay identical', () => {
  const broken = css.replace(/(@media \(prefers-color-scheme: light\)[\s\S]*?--bx-subtle: )#626576/, '$1#777777');
  assert.notEqual(broken, css);
  const p = checkDayBlocks(broken);
  assert.equal(p.length, 1);
  assert.match(p[0], /the two Day blocks differ at declaration \d+: "--bx-subtle: #626576" vs "--bx-subtle: #777777"/);
  const one = css.replace(/\/\* bx-day:start \*\/[\s\S]*?\/\* bx-day:end \*\//, '');
  assert.match(checkDayBlocks(one)[0], /1 bx-day blocks, want 2/);
  const stray = css.replace(/(\/\* bx-day:start \*\/)/g, '$1\n  --bx-not-in-night: #fff;');
  assert.match(checkDayBlocks(stray).join('\n'), /Day sets --bx-not-in-night, which Night's :root doesn't define/);
});

test('a scan: wrong fallbacks found, right ones, fonts and unknown tokens left alone; --fix and paths', () => {
  const root = mkdtempSync(join(tmpdir(), 'theme-fallbacks-'));
  try {
    const { tokens } = readTheme(css);
    mkdirSync(join(root, 'web'), { recursive: true });
    mkdirSync(join(root, 'workspace-template', 'shell'), { recursive: true });
    const a = join(root, 'web', 'a.js');
    const b = join(root, 'workspace-template', 'shell', 'b.css');
    writeFileSync(a, [
      'const css = `',
      '  .x { color: var(--bx-red, #ef5350); background: var(--bx-panel, #1F2028); }',
      '  .y { font: var(--bx-font, 12px sans-serif); border: 1px solid var(--bx-border,#2c2e38); }',
      '  .z { color: var(--bx-yellow, #f2a71b); outline: var(--bx-focus-outline, 2px solid red); }',
      '  .w { color: var(--bx-link, var(--bx-accent, #f5a623)); }',
      '`;',
    ].join('\n'));
    writeFileSync(b, '.b { color: var(--bx-muted, #8794a1) }\n');
    const found = scan({ root, tokens }).problems;
    assert.deepEqual(found, [
      'web/a.js:2: var(--bx-red, #ef5350) — theme.css says #FF7A7A',
      'web/a.js:3: var(--bx-border, #2c2e38) — theme.css says #33353F',
      'web/a.js:4: var(--bx-focus-outline, 2px solid red) — theme.css says 3px solid #3DD6F5',
      'web/a.js:5: var(--bx-accent, #f5a623) — theme.css says #8C9BFF',
      'workspace-template/shell/b.css:1: var(--bx-muted, #8794a1) — theme.css says #A3A6B6',
    ]);
    // a path limits the run
    assert.deepEqual(scan({ root, tokens, paths: [b] }).problems.map((p) => p.split(':')[0]), ['workspace-template/shell/b.css']);
    // --fix on one file, then that file is clean and the other untouched
    const r = scan({ root, tokens, paths: [a], fix: true });
    assert.equal(r.fixed, 4);
    assert.match(readFileSync(a, 'utf8'), /var\(--bx-red, #FF7A7A\).*var\(--bx-panel, #1F2028\)/);
    assert.match(readFileSync(a, 'utf8'), /var\(--bx-link, var\(--bx-accent, #8C9BFF\)\)/);
    assert.match(readFileSync(a, 'utf8'), /var\(--bx-yellow, #f2a71b\)/, 'an undefined token is left to the lint');
    assert.match(readFileSync(a, 'utf8'), /var\(--bx-font, 12px sans-serif\)/, 'a font fallback may abbreviate');
    assert.deepEqual(scan({ root, tokens, paths: [a] }).problems, []);
    assert.equal(readFileSync(b, 'utf8'), '.b { color: var(--bx-muted, #8794a1) }\n');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
