// hack/term-palettes.test.mjs — the terminal's palettes (web/term-palettes.js,
// D184): Concrete Night and Concrete Day equal theme.css's --bx-term-* values,
// "Workspace (follows the theme)" is built from those tokens (Night where a
// document has none), and every palette the settings menu lists has a label.
// Run by `make js-test`.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { TERM_TOKENS, NIGHT, DAY, TERM_THEMES, THEME_LABELS, themeName, workspaceTheme, paletteFor } from '../web/term-palettes.js';

const css = readFileSync(new URL('../web/theme.css', import.meta.url), 'utf8').replace(/\/\*(?!\s*bx-day:)[\s\S]*?\*\//g, '');

// the declarations of a block of custom properties
function props(block) {
  const out = {};
  for (const m of block.matchAll(/(--bx-[a-z0-9-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}
const night = props(css.slice(css.indexOf(':root {'), css.indexOf('}', css.indexOf(':root {'))));
const dayAt = css.indexOf('/* bx-day:start */');
const day = props(css.slice(dayAt, css.indexOf('/* bx-day:end */', dayAt)));

test('the token map covers fg, bg, cursor, its ink, selection and the 16 ANSI colours', () => {
  assert.equal(Object.keys(TERM_TOKENS).length, 21);
  assert.deepEqual(Object.keys(NIGHT), Object.keys(TERM_TOKENS));
  assert.deepEqual(Object.keys(DAY), Object.keys(TERM_TOKENS));
  for (const name of Object.values(TERM_TOKENS)) assert.ok(night[name], `theme.css defines ${name}`);
});

test('Concrete Night is theme.css\'s :root, Concrete Day its Day block', () => {
  assert.ok(Object.keys(day).length > 50, 'the Day block was found');
  for (const [key, name] of Object.entries(TERM_TOKENS)) {
    assert.equal(NIGHT[key].toUpperCase(), night[name].toUpperCase(), `Night ${key} = ${name}`);
    assert.equal(DAY[key].toUpperCase(), day[name].toUpperCase(), `Day ${key} = ${name}`);
  }
});

test('"Workspace" reads the tokens, and falls back to Night where a document has none', () => {
  assert.deepEqual(workspaceTheme((n) => day[n]), DAY);
  assert.deepEqual(workspaceTheme((n) => night[n]), NIGHT);
  assert.deepEqual(workspaceTheme(() => ''), NIGHT);
  const partial = workspaceTheme((n) => (n === '--bx-term-bg' ? ' #123456 ' : undefined));
  assert.equal(partial.background, '#123456');
  assert.equal(partial.foreground, NIGHT.foreground);
});

test('the menu: Workspace first, then Concrete Night and Day, every palette labelled', () => {
  assert.deepEqual(Object.keys(TERM_THEMES).slice(0, 3), ['default', 'concrete-night', 'concrete-day']);
  assert.equal(TERM_THEMES.default, null);
  assert.deepEqual(Object.keys(THEME_LABELS), Object.keys(TERM_THEMES));
  assert.equal(THEME_LABELS.default, 'Workspace (follows the theme)');
  for (const [k, p] of Object.entries(TERM_THEMES)) {
    if (!p) continue;
    for (const key of ['background', 'foreground', 'cursor', 'selectionBackground', 'black', 'brightWhite']) assert.match(p[key], /^#[0-9a-fA-F]{6}$/, `${k}.${key}`);
  }
});

test('a stored choice: a known palette stays, anything else is the workspace\'s', () => {
  assert.equal(themeName('nord'), 'nord');
  assert.equal(themeName('concrete-day'), 'concrete-day');
  for (const v of [null, '', 'bogus', 'toString', '__proto__', 42]) assert.equal(themeName(v), 'default', String(v));
  assert.equal(paletteFor('dracula', () => '').background, '#282a36');
  assert.deepEqual(paletteFor('bogus', (n) => day[n]), DAY);
});
