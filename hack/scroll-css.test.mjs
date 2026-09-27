// hack/scroll-css.test.mjs — the scrollbar CSS (D123) lives twice: as
// scrollCssText in web/bx-scroll.js (shadow roots include it) and between the
// bx-scroll markers in web/theme.css (documents link it). Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { scrollCssText } from '../web/bx-scroll.js';

const norm = (s) => s.replace(/\s+/g, ' ').trim();

test('theme.css carries scrollCssText verbatim', () => {
  const t = readFileSync(new URL('../web/theme.css', import.meta.url), 'utf8');
  const m = t.match(/\/\* bx-scroll:start \*\/([\s\S]*?)\/\* bx-scroll:end \*\//);
  assert.ok(m, 'theme.css has the bx-scroll:start/end block');
  assert.equal(norm(m[1]), norm(scrollCssText));
});

test('Chromium never sees the standard scrollbar properties', () => {
  // scrollbar-color/-width switch ::-webkit-scrollbar off in Chromium 121+:
  // they may only appear inside the @supports not selector(::-webkit-scrollbar) block
  const outside = scrollCssText.replace(/@supports not selector\(::-webkit-scrollbar\) \{[^}]*\{[^}]*\}[^}]*\{[^}]*\}\s*\}/, '');
  assert.ok(!/scrollbar-(color|width)/.test(outside), outside);
  assert.match(scrollCssText, /@media \(hover: hover\) and \(pointer: fine\)/);
});
