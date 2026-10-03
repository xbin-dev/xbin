// hack/theme-lint.test.mjs — hack/theme-lint.mjs (D184), run by `make
// js-test`: each check fires on a fixture; a known token's fallback passes
// and an unknown token's doesn't; comments never count; `theme-ok:` and the
// allowlist work and need a reason; contrast fails on a lowered pair; the
// real theme.css's pairs hold.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, rmSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { tmpdir } from 'node:os';
import { lint, readAllow, contrastFindings, stripJS, stripHTML, stripGo, CHECKS } from './theme-lint.mjs';

const css = readFileSync(new URL('../web/theme.css', import.meta.url), 'utf8');

// run(files, opts): lint a throwaway tree holding files ({path: text}).
function run(files, opts = {}) {
  const root = mkdtempSync(join(tmpdir(), 'theme-lint-'));
  try {
    for (const [p, text] of Object.entries(files)) {
      mkdirSync(dirname(join(root, p)), { recursive: true });
      writeFileSync(join(root, p), text);
    }
    return lint({ root, css, checks: CHECKS.filter((c) => c !== 'contrast'), ...opts })
      .map((f) => `${f.file}:${f.line}: ${f.check}: ${f.literal}`);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

test('colour: declarations, styling APIs, colour-only strings, black and white', () => {
  assert.deepEqual(run({
    'web/a.css': '.a { color: #fff; }\n.b { background: rgba(0, 0, 0, .5); }\n.c { border: 1px solid white; }\n.d { fill: hsl(210 50% 40%); }\n',
    'web/b.js': [
      "el.style.color = '#abcdef';",
      "const PALETTE = { go: '#4a90d9', node: 'rgb(1, 2, 3)' };",
      "ctx.fillStyle = 'black';",
      "const t = { background: '#000000', black: token('--bx-term-black') };",
      "const s = css`.x { box-shadow: 0 1px 2px rgba(0,0,0,.4); }`;",
    ].join('\n'),
    'web/c.html': '<div style="color:#123">x</div>\n<svg><rect fill="#f00"/></svg>\n',
  }), [
    'web/a.css:1: colour: #fff',
    'web/a.css:2: colour: rgba(0, 0, 0, .5)',
    'web/a.css:3: colour: white',
    'web/a.css:4: colour: hsl(210 50% 40%)',
    'web/b.js:1: colour: #abcdef',
    'web/b.js:2: colour: #4a90d9',
    'web/b.js:2: colour: rgb(1, 2, 3)',
    'web/b.js:3: colour: black',
    'web/b.js:4: colour: #000000',
    'web/b.js:5: colour: rgba(0,0,0,.4)',
    'web/c.html:1: colour: #123',
    'web/c.html:2: colour: #f00',
  ]);
});

test('colour: a known token\'s fallback passes; an unknown one\'s is a hard-coded colour', () => {
  assert.deepEqual(run({
    'web/a.css': [
      '.a { color: var(--bx-text, #E9EAF0); background: var(--bx-panel,#1F2028); }',
      '.b { color: color-mix(in srgb, var(--bx-muted, #A3A6B6) 40%, transparent); }',
      '.c { color: var(--bx-link, var(--bx-accent, #8C9BFF)); }',
      '.d { color: var(--bx-yellow, #f2a71b); }',
      '.e { color: var(--my-own, #fff); }',
      '.f { color: transparent; background: currentColor; border-color: inherit; }',
    ].join('\n'),
  }), [
    'web/a.css:4: colour: #f2a71b',
    'web/a.css:5: colour: #fff',
  ]);
});

test('colour: not a colour — ids, anchors, white-space, masks, keys', () => {
  assert.deepEqual(run({
    'web/a.js': [
      "document.querySelector('#add').click();",
      "location.hash = '#fab';",
      'const nowrap = css`.x { white-space: nowrap; }`;',
      'const m = css`.y { mask: linear-gradient(#000, transparent); -webkit-mask-image: linear-gradient(black, transparent); }`;',
      "const keys = { white: 'bright', black: 1 };",
      "const entity = '&#123;';",
    ].join('\n'),
  }), []);
});

test('radius: anything but 0 or the token', () => {
  assert.deepEqual(run({
    'web/a.css': [
      '.a { border-radius: 4px; }',
      '.b { border-radius: 999px; }',
      '.c { border-top-left-radius: 50%; }',
      '.d { border-radius: var(--bx-radius); }',
      '.e { border-radius: var(--bx-radius, 2px); }',
      '.f { border-radius: 0; border-bottom-right-radius: 0px; }',
      '.g { border-radius: 0 0 var(--bx-radius) var(--bx-radius); }',
      '.h { border-radius: 2px; }',
    ].join('\n'),
    'web/b.js': "el.style.borderRadius = '6px';\nconst r = { borderRadius: 0 };\nconst t = css`.x { border-radius: ${r}px; }`;\n",
  }), [
    'web/a.css:1: radius: 4px',
    'web/a.css:2: radius: 999px',
    'web/a.css:3: radius: 50%',
    'web/a.css:8: radius: 2px',
    'web/b.js:1: radius: 6px',
  ]);
});

test('font: a family outside the tokens', () => {
  assert.deepEqual(run({
    'web/a.css': [
      '.a { font: 13px/1.4 system-ui, sans-serif; }',
      '.b { font-family: "Helvetica Neue", Arial; }',
      '.c { font: var(--bx-font); }',
      '.d { font: 600 var(--bx-text-size)/var(--bx-text-line) var(--bx-sans, system-ui); }',
      '.e { font-family: var(--bx-mono, ui-monospace, monospace); }',
      '.f { font: inherit; font-family: inherit; }',
    ].join('\n'),
    'web/b.js': "const term = new Terminal({ fontFamily: 'Menlo, monospace' });\nctx.font = '13px sans-serif';\n",
  }), [
    'web/a.css:1: font: system-ui',
    'web/a.css:2: font: "Helvetica Neue"',
    'web/b.js:1: font: Menlo',
    'web/b.js:2: font: sans-serif',
  ]);
});

test('small: literal sizes under 13px; the tokens and relative sizes pass', () => {
  assert.deepEqual(run({
    'web/a.css': [
      '.a { font-size: 12px; }',
      '.b { font: 600 11px/14px var(--bx-sans); }',
      '.c { font-size: 13px; font: 14px/20px var(--bx-sans); }',
      '.d { font: var(--bx-font-micro); letter-spacing: var(--bx-tracking-micro); }',
      '.e { font-size: .85em; }',
      '.f { font-size: 10.5px; }',
    ].join('\n'),
    'web/b.js': "const t = new Terminal({ fontSize: 11 });\nconst svg = '<text font-size=\"9\">x</text>';\n",
  }, { checks: ['small'] }), [
    'web/a.css:1: small: 12px',
    'web/a.css:2: small: 11px',
    'web/a.css:6: small: 10.5px',
    'web/b.js:1: small: 11',
    'web/b.js:2: small: 9px',
  ]);
});

test('emoji: pictographs, presentation selectors and escapes', () => {
  assert.deepEqual(run({
    'web/a.js': [
      "const lock = '🔒 private';",
      "const warn = '⚠️ careful';",
      "const esc = '\\u{1F512}';",
      "const glyphs = '› ✓ ▸ ⋯';",
    ].join('\n'),
  }, { checks: ['emoji'] }), [
    'web/a.js:1: emoji: 🔒',
    'web/a.js:2: emoji: ⚠️',
    'web/a.js:3: emoji: \\u{1F512}',
  ]);
});

test('comments never count: CSS, JS, HTML, Go', () => {
  assert.deepEqual(run({
    'web/a.css': '/* .old { color: #fff; border-radius: 6px } 🔒 */\n.a { color: var(--bx-text); }\n',
    'web/b.js': [
      "// color: #fff; border-radius: 6px; 🔒",
      "/* font: 11px Arial */ const u = 'http://example.com/#fff';",
      'const s = css`/* color: #f00 */ .x { color: var(--bx-text); }`;',
      "const re = /\\/\\/ x/; const t = html`<!-- 🔒 --><b>x</b>`;",
    ].join('\n'),
    'web/c.html': '<!-- <div style="color:#fff"> -->\n<style>/* .a { color: #000 } */</style>\n<script type="module">// 🔒 color: #fff\n</script>\n',
    'internal/server/page.go': 'package server\n\n// color: #fff\nconst page = `<style>/* color:#000 */ body{color:var(--bx-text)}</style>`\n',
  }), []);
});

test('Go: only internal/server, only its string literals', () => {
  assert.deepEqual(run({
    'internal/server/page.go': 'package server\n\nconst page = `<style>body{color:#0d1117;border-radius:6px;font:15px/1.5 system-ui}</style>`\n',
    'internal/server/page_test.go': 'package server\n\nconst want = `color:#fff`\n',
    'internal/broker/x.go': 'package broker\n\nconst page = `color:#fff`\n',
  }), [
    'internal/server/page.go:3: colour: #0d1117',
    'internal/server/page.go:3: radius: 6px',
    'internal/server/page.go:3: font: system-ui',
  ]);
});

test('theme-ok on the line or the one before, with a reason; the allowlist by glob and check', () => {
  const files = {
    'web/a.js': [
      "const qr = css`.qr { background: #fff; }`; // theme-ok: a QR code is dark on light for scanners",
      '// theme-ok: an avatar hue derived from a person\'s id',
      "const avatar = { background: 'hsl(200 40% 40%)' };",
      "const bare = { color: '#fff' }; // theme-ok:",
      "const later = { color: '#000' };",
    ].join('\n'),
    'web/xb/render-x.js': "const r = css`.x { border-radius: 10px; color: #111; }`;\n",
    'web/vendor/lib.js': "const v = { color: '#fff' };\n",
    'web/x.test.mjs': "const v = { color: '#fff' };\n",
  };
  const allow = readAllow('# a comment\nweb/xb/render-*.js  radius  the native renderer draws the app\'s shapes\n');
  assert.deepEqual(run(files, { allow }), [
    'web/a.js:4: colour: #fff',
    'web/a.js:5: colour: #000',
    'web/xb/render-x.js:1: colour: #111',
  ]);
  assert.throws(() => readAllow('web/a.js  colour\n'), /want '<path-glob>  <checks>  <reason>'/);
  assert.throws(() => readAllow('web/a.js  colours  a typo\n'), /unknown check colours/);
  assert.equal(readAllow('web/**/x.js  *  everything\n')[0].checks.size, CHECKS.length);
});

test('contrast: the real theme holds; a lowered pair fails, named', () => {
  assert.deepEqual(contrastFindings(css), []);
  const lowered = css.replace(/(\/\* bx-day:start \*\/[\s\S]*?--bx-subtle: )#626576/, '$1#9a9cab');
  const f = contrastFindings(lowered);
  assert.ok(f.length >= 1);
  assert.match(f.map((x) => x.literal).join('\n'), /--bx-subtle on --bx-panel \(Day\) 2\.\d\d < 4\.5/);
  const night = css.replace(/(:root \{[\s\S]*?--bx-accent-ink: )#0B0C12/, '$1#8C9BFF');
  assert.match(contrastFindings(night).map((x) => x.literal).join('\n'), /--bx-accent-ink on --bx-accent \(Night\) 1\.00 < 4\.5/);
});

test('the strippers keep lines and columns', () => {
  const js = "const a = 1; // x\n/* y\n z */ const b = `t /* c */ ${'s'} u`;\n";
  const s = stripJS(js);
  assert.equal(s.length, js.length);
  assert.equal(s.split('\n').length, js.split('\n').length);
  assert.doesNotMatch(s, /\/\/ x|\/\* y| c /);
  assert.match(s, /\$\{'s'\}/);
  assert.equal(stripHTML('<p>a</p><!--\nb\n--><p>c</p>').split('\n').length, 3);
  assert.equal(stripGo('x := 1 // c\ny := "s"\n'), '           \n      s \n');
});
