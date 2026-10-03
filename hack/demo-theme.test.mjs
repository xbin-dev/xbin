// hack/demo-theme.test.mjs — the demo film set (hack/demo) in both themes
// (D184): every tile page follows the person's light or dark theme, the
// tiles style themselves from the theme's tokens alone (no colour, font
// stack, radius or small type of their own, no gradient, no emoji), and the
// stills and the camera name their themes the same way (themes.js).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = fileURLToPath(new URL('.', import.meta.url));
const TILES = join(HERE, 'demo', 'tiles');
const require = createRequire(import.meta.url);
const { themes, suffix } = require('./demo/themes.js');

function* files(dir) {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (n === 'backend') continue; // node servers: no styling
    if (statSync(p).isDirectory()) yield* files(p);
    else if (/\.(js|html)$/.test(n)) yield p;
  }
}

// the source without its comments (CSS and JS block comments, line comments
// that start a line or follow a space, HTML comments)
const uncomment = (s) => s.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|\s)\/\/.*$/gm, '$1');

test('themes: dark by default, light, or both in that order; a light file says so', () => {
  assert.deepEqual(themes(), ['dark']);
  assert.deepEqual(themes('dark'), ['dark']);
  assert.deepEqual(themes('light'), ['light']);
  assert.deepEqual(themes('both'), ['dark', 'light']);
  assert.throws(() => themes('sepia'), /dark, light or both/);
  assert.equal(suffix('dark'), '');
  assert.equal(suffix(undefined), '');
  assert.equal(suffix('light'), '-light');
});

test('every tile page opts in to the theme', () => {
  const pages = readdirSync(TILES).map((t) => join(TILES, t, 'index.html')).filter((p) => { try { return statSync(p).isFile(); } catch { return false; } });
  assert.ok(pages.length >= 8, `the tile pages: ${pages.length}`);
  for (const p of pages) {
    const s = readFileSync(p, 'utf8');
    assert.match(s, /<html lang="en" data-bx-theme="auto">/, `${relative(HERE, p)} follows the person's theme`);
    assert.match(s, /<link rel="stylesheet" href="\/vendor\/theme\.css">/, `${relative(HERE, p)} links the theme`);
  }
});

test('the tiles style themselves from the theme tokens alone', () => {
  const EMOJI = /[\u{1F300}-\u{1FAFF}☔☕⚠⚡⛔✅❌⭐⏳⌛]/u;
  const checks = [
    ['colour', /#[0-9a-fA-F]{3}(?:[0-9a-fA-F]{1,5})?\b(?![-\w])|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\(/],
    ['named colour', /(?:color|background|border|fill|stroke|shadow|outline)[\w-]*\s*:\s*[^;`]*\b(?:white|black)\b/],
    ['gradient', /\b(?:linear|radial|conic)-gradient\(/],
    ['emoji', EMOJI],
  ];
  const hits = [];
  for (const f of files(TILES)) {
    const src = uncomment(readFileSync(f, 'utf8'));
    src.split('\n').forEach((l, i) => {
      const at = `${relative(HERE, f)}:${i + 1}`;
      for (const [what, rx] of checks) if (rx.test(l)) hits.push(`${at}: ${what}: ${l.trim()}`);
      for (const m of l.matchAll(/border(?:-[a-z]+)*-radius\s*:\s*([^;`}]+)/g)) {
        if (!/^(?:0|var\(--bx-radius\b.*)\s*$/.test(m[1].trim())) hits.push(`${at}: radius: ${m[1].trim()}`);
      }
      for (const m of l.matchAll(/font-family\s*:\s*([^;`}]+)/g)) {
        if (!/^(?:var\(--bx-[\w-]+\)|inherit)$/.test(m[1].trim())) hits.push(`${at}: font: ${m[1].trim()}`);
      }
      for (const m of l.matchAll(/\bfont(?:-size)?\s*:\s*([^;`}]+)/g)) {
        for (const px of m[1].replace(/var\([^)]*\)/g, '').matchAll(/(\d+(?:\.\d+)?)px/g)) {
          if (Number(px[1]) < 13) hits.push(`${at}: small type: ${m[1].trim()}`);
        }
      }
    });
  }
  assert.deepEqual(hits, [], `the film set's tiles off the theme's tokens:\n${hits.join('\n')}`);
});

test("the change the 'live' shot films is the tracker's board plus three lines (tiles/onboarding/next/)", () => {
  const lines = (p) => readFileSync(join(TILES, 'onboarding', p), 'utf8').split('\n');
  const board = lines('board.js'), next = lines('next/board.js');
  const added = next.filter((l) => !board.includes(l)), removed = board.filter((l) => !next.includes(l));
  assert.deepEqual(added, ["import { timeline, timelineCss } from './timeline.js';", '  static styles = [styles, timelineCss];', '      ${timeline(this._cs)}']);
  assert.deepEqual(removed, ['  static styles = styles;']);
});
