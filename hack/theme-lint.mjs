#!/usr/bin/env node
// hack/theme-lint.mjs — the light theme stays unbroken (D184): no colour,
// radius, font stack, small type or emoji written outside the tokens, and
// the tokens' own contrast pairs hold, in both themes. With
// hack/theme-fallbacks.mjs it is `make theme-check` (docs/maintenance.md).
//
//   node hack/theme-lint.mjs [--checks colour,radius,…] [--theme file.css] [path…]
//
// Paths (files or directories) limit the scan; none = every tree below.
// Output: `file:line: check: literal`, then a count per check; exit 1 on any
// finding.
//
// Scope: web/ (not web/vendor/), workspace-template/, builtin-tiles/,
// builtin-templates/, examples/, hack/demo/tiles/ (.js .mjs .html .css) and
// the string literals of internal/server/*.go. Skipped: test/, testdata/,
// _backend/, node_modules/, deps/, data/, *_test.go, *.test.mjs. Comments
// are stripped first (CSS, JS, HTML, Go), so a colour or emoji in a comment
// is not a finding.
//
// The checks:
//   colour   a hex colour, rgb()/rgba()/hsl()/hsla()/hwb()/lab()/lch()/
//            oklab()/oklch()/color(), or black/white, on a line that styles
//            (a colour-bearing declaration, style=, .style, setProperty,
//            fill/stroke, fillStyle/strokeStyle, light-dark(), color-mix(),
//            a gradient), or a string that is nothing but a colour (a data
//            table, an xterm theme). Not a finding: the fallback inside
//            var(--bx-x, …) when theme.css defines --bx-x (theme-fallbacks
//            checks its value); transparent, currentColor, inherit; a line
//            about a mask (alpha only). A fallback of a token theme.css
//            doesn't define IS a finding: a hard-coded colour in a var().
//   radius   a border-*radius (or borderRadius) other than 0 or
//            var(--bx-radius…).
//   font     a font / font-family (fontFamily, ctx.font) naming a family
//            outside var(--bx-…).
//   small    a literal font size under 13px (font-size, the font shorthand,
//            fontSize, an SVG font-size): 11px micro caps and 12px meta are
//            the --bx-font-micro / --bx-font-meta tokens.
//   emoji    a pictographic emoji (the audit's ranges, an emoji presentation
//            selector, a \u{1F…} escape) in UI source.
//   contrast the pairs of plans' §1.4 recomputed from theme.css's Night and
//            Day values against WCAG thresholds (4.5 text, 3 UI parts).
//
// Exceptions, each with a reason: hack/theme-allow.txt (`<path-glob>
// <checks> <reason>`), or `theme-ok: <reason>` in a comment on the line or
// the line before (the `// exec-ok:` precedent).
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, extname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { readTheme } from './theme-fallbacks.mjs';

const ROOT = new URL('..', import.meta.url).pathname.replace(/\/$/, '');
export const TREES = ['web', 'workspace-template', 'builtin-tiles', 'builtin-templates', 'examples', 'hack/demo/tiles', 'internal/server'];
const SKIP_DIRS = new Set(['vendor', 'node_modules', 'deps', 'data', 'test', 'testdata', '_backend', '.git']);
export const CHECKS = ['colour', 'radius', 'font', 'small', 'emoji', 'contrast'];

// ---- comment stripping: comments become spaces, newlines stay ----------

const blank = (s) => s.replace(/[^\n]/g, ' ');

export function stripCSS(src) { return src.replace(/\/\*[\s\S]*?\*\//g, blank); }

// Inside a string or template's text: CSS and HTML comments (lit css``,
// html`` templates; a Go raw string holding a page).
const stripEmbedded = (s) => s.replace(/\/\*[\s\S]*?\*\//g, blank).replace(/<!--[\s\S]*?-->/g, blank);

const REGEX_AFTER_WORD = new Set(['return', 'typeof', 'case', 'do', 'else', 'in', 'of', 'new', 'delete', 'void', 'throw', 'instanceof', 'yield', 'await']);

// stripJS blanks JS comments, tracking strings, template literals (with
// ${} nesting) and regex literals, and strips CSS/HTML comments inside
// template text.
export function stripJS(src) {
  const out = src.split('');
  const n = src.length;
  const put = (from, to, text) => { for (let k = from; k < to; k++) out[k] = text[k - from]; };
  const tpl = []; // the brace depth each open ${ … } returns to
  let braces = 0, i = 0, prev = '', prevWord = '';
  const regexOK = () => prev === '' || /[(,=:[!&|?{};+\-*%<>~^]/.test(prev) || REGEX_AFTER_WORD.has(prevWord);
  const templateText = (from) => {
    // from: just after ` or after the } closing a ${…}; returns the index after the closing ` or the ${
    let k = from;
    while (k < n) {
      const c = src[k];
      if (c === '\\') { k += 2; continue; }
      if (c === '`') { put(from, k, stripEmbedded(src.slice(from, k))); return { at: k + 1, open: false }; }
      if (c === '$' && src[k + 1] === '{') { put(from, k, stripEmbedded(src.slice(from, k))); return { at: k + 2, open: true }; }
      k++;
    }
    put(from, n, stripEmbedded(src.slice(from, n)));
    return { at: n, open: false };
  };
  const enterTemplate = (from) => {
    const r = templateText(from);
    if (r.open) { tpl.push(braces); braces++; prev = '{'; prevWord = ''; } else { prev = '`'; prevWord = ''; }
    return r.at;
  };
  while (i < n) {
    const c = src[i], d = src[i + 1];
    if (c === '/' && d === '/') { let e = src.indexOf('\n', i); if (e < 0) e = n; put(i, e, blank(src.slice(i, e))); i = e; continue; }
    if (c === '/' && d === '*') { let e = src.indexOf('*/', i + 2); e = e < 0 ? n : e + 2; put(i, e, blank(src.slice(i, e))); i = e; continue; }
    if (c === '"' || c === "'") {
      let k = i + 1;
      while (k < n && src[k] !== c && src[k] !== '\n') k += src[k] === '\\' ? 2 : 1;
      i = k + 1; prev = c; prevWord = '';
      continue;
    }
    if (c === '`') { i = enterTemplate(i + 1); continue; }
    if (c === '/' && regexOK()) {
      let k = i + 1, cls = false;
      while (k < n && src[k] !== '\n') {
        if (src[k] === '\\') { k += 2; continue; }
        if (src[k] === '[') cls = true;
        else if (src[k] === ']') cls = false;
        else if (src[k] === '/' && !cls) break;
        k++;
      }
      k++;
      while (k < n && /[a-z]/i.test(src[k])) k++;
      i = k; prev = 'r'; prevWord = '';
      continue;
    }
    if (c === '{') braces++;
    else if (c === '}') {
      braces--;
      if (tpl.length && tpl.at(-1) === braces) { tpl.pop(); i = enterTemplate(i + 1); continue; }
    }
    if (/[A-Za-z_$0-9]/.test(c)) {
      let k = i;
      while (k < n && /[A-Za-z_$0-9]/.test(src[k])) k++;
      prevWord = src.slice(i, k); prev = 'w';
      i = k;
      continue;
    }
    if (!/\s/.test(c)) { prev = c; prevWord = ''; }
    i++;
  }
  return out.join('');
}

// stripHTML: HTML comments; <script> bodies as JS; <style> bodies as CSS.
export function stripHTML(src) {
  let out = '', i = 0;
  const re = /<!--|<script\b[^>]*>|<style\b[^>]*>/gi;
  for (;;) {
    re.lastIndex = i;
    const m = re.exec(src);
    if (!m) return out + src.slice(i);
    out += src.slice(i, m.index);
    if (m[0] === '<!--') {
      let e = src.indexOf('-->', m.index + 4);
      e = e < 0 ? src.length : e + 3;
      out += blank(src.slice(m.index, e));
      i = e;
      continue;
    }
    const tag = /^<script/i.test(m[0]) ? 'script' : 'style';
    const bodyStart = m.index + m[0].length;
    let e = src.toLowerCase().indexOf(`</${tag}`, bodyStart);
    if (e < 0) e = src.length;
    const body = src.slice(bodyStart, e);
    const json = tag === 'script' && /type\s*=\s*["']?(importmap|application\/(ld\+)?json)/i.test(m[0]);
    out += m[0] + (json ? body : tag === 'script' ? stripJS(body) : stripCSS(body));
    i = e;
  }
}

// stripGo keeps only string literals' text (CSS/HTML comments inside them
// stripped); Go code and comments become spaces.
export function stripGo(src) {
  const out = blank(src).split('');
  const n = src.length;
  let i = 0;
  while (i < n) {
    const c = src[i];
    if (c === '/' && src[i + 1] === '/') { const e = src.indexOf('\n', i); i = e < 0 ? n : e; continue; }
    if (c === '/' && src[i + 1] === '*') { const e = src.indexOf('*/', i + 2); i = e < 0 ? n : e + 2; continue; }
    if (c === '`' || c === '"') {
      let k = i + 1;
      if (c === '`') { while (k < n && src[k] !== '`') k++; } else { while (k < n && src[k] !== '"' && src[k] !== '\n') k += src[k] === '\\' ? 2 : 1; }
      const text = stripEmbedded(src.slice(i + 1, k));
      for (let j = 0; j < text.length; j++) out[i + 1 + j] = text[j];
      i = k + 1;
      continue;
    }
    if (c === "'") { let k = i + 1; while (k < n && src[k] !== "'" && src[k] !== '\n') k += src[k] === '\\' ? 2 : 1; i = k + 1; continue; }
    i++;
  }
  return out.join('');
}

export function strip(file, src) {
  switch (extname(file)) {
    case '.css': return stripCSS(src);
    case '.html': return stripHTML(src);
    case '.go': return stripGo(src);
    default: return stripJS(src);
  }
}

// ---- the line checks -----------------------------------------------------

const HEX = /(?<![&\w#])#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})(?![\w-])/g;
const FUNC = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\(|\bcolor\(\s*(?:srgb|srgb-linear|display-p3|a98-rgb|prophoto-rgb|rec2020|xyz)/gi;
const NAMED = /(?<![\w-])(?:black|white)(?![\w-])(?!\s*:)/gi;
const CTX = new RegExp([
  String.raw`(?:^|[^\w-])(?:color|background(?:-color|-image)?|border(?:-(?:top|right|bottom|left|block|inline)(?:-(?:start|end))?)?(?:-color)?|outline(?:-color)?|fill|stroke|(?:box-|text-)?shadow|stop-color|flood-color|lighting-color|caret-color|accent-color|column-rule(?:-color)?|text-decoration(?:-color)?|scrollbar-color|--[\w-]+)\s*:`,
  String.raw`\bstyle\s*=|\.style\b|setProperty\(|\b(?:fill|stroke)(?:Style)?\s*=|shadowColor|\bstop-color\s*=|\bcolor\s*=|light-dark\(|color-mix\(|gradient\(`,
].join('|'), 'i');

// the extent of every var( … ) on a line: [{start, end, token, comma}]
function varsOf(line) {
  const out = [];
  for (let i = line.indexOf('var('); i >= 0; i = line.indexOf('var(', i + 4)) {
    let depth = 0, end = -1, comma = -1;
    for (let k = i + 3; k < line.length; k++) {
      if (line[k] === '(') depth++;
      else if (line[k] === ')') { if (--depth === 0) { end = k; break; } } else if (line[k] === ',' && depth === 1 && comma < 0) comma = k;
    }
    if (end < 0) end = line.length;
    out.push({ start: i, end, comma, token: line.slice(i + 4, comma < 0 ? end : comma).trim() });
  }
  return out;
}

// inFallback: the literal at p sits in the fallback of a var() — of a
// token theme.css defines (ok), or of anything else (a finding).
function fallbackOf(vars, p, tokens) {
  let best = null;
  for (const v of vars) if (v.comma >= 0 && p > v.comma && p < v.end && (!best || v.start > best.start)) best = v;
  if (!best) return 'none';
  return best.token in tokens ? 'known' : 'unknown';
}

// a string that holds nothing but the colour at [s, e)
const wholeString = (line, s, e) => {
  const q = line[s - 1];
  return (q === "'" || q === '"' || q === '`') && line[e] === q;
};

function colourFindings(line, tokens) {
  if (/mask/i.test(line)) return [];
  const ctx = CTX.test(line);
  const vars = varsOf(line);
  const out = [];
  const consider = (lit, s, e, named) => {
    const fb = fallbackOf(vars, s, tokens);
    if (fb === 'known') return;
    if (fb === 'unknown' || ctx || (!named && wholeString(line, s, e))) out.push(lit);
  };
  for (const m of line.matchAll(HEX)) {
    if (/querySelector|getElementById|location\.hash|href\s*=|\bid\s*=/.test(line) && !ctx) continue;
    consider(m[0], m.index, m.index + m[0].length, false);
  }
  for (const m of line.matchAll(FUNC)) {
    const open = line.indexOf('(', m.index);
    let depth = 0, end = open;
    for (; end < line.length; end++) { if (line[end] === '(') depth++; else if (line[end] === ')' && --depth === 0) break; }
    const lit = line.slice(m.index, end + 1);
    if (/var\(/.test(lit) && !/\d/.test(lit.replace(/var\([^)]*\)/g, ''))) continue; // rgb(var(--x)) and friends
    consider(lit, m.index, end + 1, false);
  }
  if (ctx) for (const m of line.matchAll(NAMED)) consider(m[0], m.index, m.index + m[0].length, true);
  return out;
}

// top-level (paren-aware) whitespace split
function splitTop(v) {
  const parts = [];
  let depth = 0, cur = '';
  for (const c of v) {
    if (c === '(') depth++;
    if (c === ')') depth--;
    if (/[\s/]/.test(c) && depth === 0) { if (cur) parts.push(cur); cur = ''; } else cur += c;
  }
  if (cur) parts.push(cur);
  return parts;
}

const radiusOK = (v) => {
  if (v.includes('${')) return true; // a template expression: not a literal
  const parts = splitTop(v.replace(/!important/, '').trim());
  return parts.length > 0 && parts.every((p) => /^0(?:\.0+)?(?:px|%|em|rem)?$/.test(p) || /^var\(--bx-radius\b/.test(p) || /^(inherit|initial|unset|revert)$/.test(p));
};

function radiusFindings(line) {
  const out = [];
  for (const m of line.matchAll(/(?:^|[^\w-])border(?:-[a-z]+)*-radius\s*:\s*([^;}"'`\n]+)/gi)) if (!radiusOK(m[1].trim())) out.push(m[1].trim());
  for (const m of line.matchAll(/\bborderRadius\s*[:=]\s*(['"`])([^'"`]*)\1/g)) if (!radiusOK(m[2].trim())) out.push(m[2].trim());
  for (const m of line.matchAll(/\bborderRadius\s*[:=]\s*(\d+(?:\.\d+)?)\b/g)) if (Number(m[1]) !== 0) out.push(m[1]);
  return out;
}

// without every var(…) (a token, or a token with its fallback)
function withoutVars(v) {
  let out = '';
  for (let i = 0; ;) {
    const at = v.indexOf('var(', i);
    if (at < 0) return out + v.slice(i);
    let depth = 0, k = at + 3;
    for (; k < v.length; k++) { if (v[k] === '(') depth++; else if (v[k] === ')' && --depth === 0) break; }
    out += v.slice(i, at) + ' ';
    i = k + 1;
  }
}

const FAMILY = /["'][^"']*["']|\b(?:system-ui|-apple-system|BlinkMacSystemFont|sans-serif|serif|monospace|cursive|fantasy|ui-monospace|ui-sans-serif|ui-serif|ui-rounded|Menlo|Monaco|Consolas|Courier(?: New)?|Arial|Helvetica(?: Neue)?|Segoe UI|Segoe|Roboto|Inter|SF ?Mono|SFMono-Regular|Ubuntu|Cantarell|Noto Sans|Georgia|Times(?: New Roman)?|Verdana|Tahoma|Liberation Mono|DejaVu Sans(?: Mono)?|Fira (?:Code|Mono|Sans))\b/;

function fontValues(line) {
  const vals = [];
  for (const m of line.matchAll(/(?:^|[^\w-])font(?:-family)?\s*:\s*([^;}`\n]*)/gi)) vals.push(m[1]);
  for (const m of line.matchAll(/\b(?:fontFamily|\.font)\s*[:=]\s*(['"`])((?:(?!\1).)*)\1/g)) vals.push(m[2]);
  return vals;
}

function fontFindings(line) {
  const out = [];
  for (const v of fontValues(line)) {
    const rest = withoutVars(v);
    const m = FAMILY.exec(rest);
    if (m && !/^\s*(inherit|initial|unset)\s*$/.test(rest)) out.push(m[0]);
  }
  return out;
}

function smallFindings(line) {
  const out = [];
  const px = (s) => Number(s);
  for (const m of line.matchAll(/(?:^|[^\w-])font-size\s*:\s*(\d*\.?\d+)px/gi)) if (px(m[1]) < 13) out.push(`${m[1]}px`);
  for (const m of line.matchAll(/\bfont-size\s*=\s*["']?(\d*\.?\d+)(?:px)?\b/gi)) if (px(m[1]) < 13) out.push(`${m[1]}px`);
  for (const v of fontValues(line)) {
    const size = /(?:^|\s)(\d*\.?\d+)px(?:\s*\/|\s|$)/.exec(withoutVars(v));
    if (size && px(size[1]) < 13) out.push(`${size[1]}px`);
  }
  for (const m of line.matchAll(/\bfontSize\s*[:=]\s*['"`]?(\d*\.?\d+)(px)?\b/g)) if (px(m[1]) < 13) out.push(`${m[1]}${m[2] ?? ''}`);
  return out;
}

// the audit's pictographs (emoji.py), an emoji presentation selector, and
// \u{1F…} / surrogate escapes in source
const EMOJI = /.\uFE0F|[\u{1F300}-\u{1FAFF}\u2614\u2615\u26A0\u26A1\u26D4\u2705\u274C\u2B50\u23F3\u231B]|\\u\{1F[0-9A-Fa-f]{3}\}|\\uD83[C-E]\\uD[C-F][0-9A-Fa-f]{2}/gu;
const emojiFindings = (line) => [...line.matchAll(EMOJI)].map((m) => m[0]);

const LINE_CHECKS = { colour: colourFindings, radius: radiusFindings, font: fontFindings, small: smallFindings, emoji: emojiFindings };

// ---- contrast (plans' §1.4) ----------------------------------------------

function rgbOf(hex) {
  const h = hex.replace('#', '');
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h.slice(0, 6);
  return [0, 2, 4].map((k) => parseInt(full.slice(k, k + 2), 16) / 255);
}
const lum = (hex) => {
  const [r, g, b] = rgbOf(hex).map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
export const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

// [foreground, background, threshold, themes]: text 4.5, UI parts 3.
const T = 4.5, UI = 3;
const BOTH = ['Night', 'Day'];
export const PAIRS = [
  ...['--bx-panel', '--bx-panel-2'].flatMap((bg) => ['--bx-text', '--bx-muted'].map((fg) => [fg, bg, T, BOTH])),
  ...['--bx-panel', '--bx-panel-2', '--bx-hover'].map((bg) => ['--bx-subtle', bg, T, BOTH]),
  ['--bx-text', '--bx-hover', T, BOTH], ['--bx-text', '--bx-sidebar', T, BOTH], ['--bx-muted', '--bx-sidebar', T, BOTH],
  ['--bx-accent', '--bx-panel', T, BOTH], ['--bx-link', '--bx-panel', T, BOTH], ['--bx-accent-ink', '--bx-accent', T, BOTH],
  ['--bx-accent-ink', '--bx-accent-hover', T, BOTH], ['--bx-selection-text', '--bx-selection', T, BOTH],
  ...['--bx-ok', '--bx-warn', '--bx-danger', '--bx-info'].flatMap((s) => [[s, '--bx-panel', T, BOTH], [s, `${s}-bg`, T, BOTH]]),
  ['--bx-focus', '--bx-panel', UI, BOTH], ['--bx-focus', '--bx-focus-gap', UI, BOTH], ['--bx-border-strong', '--bx-panel', UI, BOTH],
  ['--bx-part', '--bx-panel', UI, BOTH],
  ['--bx-title-text', '--bx-titlebar', T, BOTH], ['--bx-title-text', '--bx-titlebar-active', T, BOTH], ['--bx-title-text-inactive', '--bx-titlebar', T, BOTH],
  ['--bx-window-border', '--bx-bg', UI, ['Night']], ['--bx-window-border-active', '--bx-bg', UI, BOTH],
  ['--bx-close-hover-ink', '--bx-close-hover', T, BOTH], ['--bx-elevated-ink', '--bx-elevated-bg', T, BOTH],
  ...['keyword', 'string', 'number', 'comment', 'function', 'type', 'attr', 'builtin', 'deletion', 'addition'].flatMap((s) =>
    ['--bx-code-bg', '--bx-panel-2', '--bx-selection', '--bx-diff-add-bg', '--bx-diff-del-bg', '--bx-diff-hunk-bg'].map((bg) => [`--bx-syn-${s}`, bg, T, BOTH])),
  ['--bx-diff-add', '--bx-diff-add-bg', T, BOTH], ['--bx-diff-del', '--bx-diff-del-bg', T, BOTH], ['--bx-diff-hunk', '--bx-diff-hunk-bg', T, BOTH],
  ['--bx-diff-context', '--bx-code-bg', T, BOTH],
  ['--bx-term-fg', '--bx-term-bg', T, BOTH], ['--bx-term-cursor', '--bx-term-bg', UI, BOTH], ['--bx-term-cursor-ink', '--bx-term-cursor', T, BOTH],
  ...['red', 'green', 'yellow', 'blue', 'magenta', 'cyan'].map((c) => [`--bx-term-${c}`, '--bx-term-bg', T, BOTH]),
  ['--bx-term-white', '--bx-term-bg', T, ['Night']], ['--bx-term-black', '--bx-term-bg', T, ['Day']],
];

// the theme's Day values: Night overridden by the first Day block
export function dayTokens(css, night) {
  const block = /\/\*\s*bx-day:start\s*\*\/([\s\S]*?)\/\*\s*bx-day:end\s*\*\//.exec(css);
  const day = { ...night };
  if (block) for (const d of block[1].matchAll(/(--bx-[a-z0-9-]+)\s*:\s*([^;]+);/g)) day[d[1]] = d[2].trim();
  return day;
}

export function contrastFindings(css) {
  const night = readTheme(css).tokens;
  const themes = { Night: night, Day: dayTokens(css, night) };
  const lineOf = (name) => { const i = css.indexOf(`${name}:`); return i < 0 ? 1 : css.slice(0, i).split('\n').length; };
  const out = [];
  for (const [fg, bg, min, which] of PAIRS) {
    for (const t of which) {
      const a = themes[t][fg], b = themes[t][bg];
      if (!/^#[0-9a-f]{3,8}$/i.test(a ?? '') || !/^#[0-9a-f]{3,8}$/i.test(b ?? '')) {
        out.push({ line: lineOf(fg), literal: `${fg} on ${bg} (${t}): not two opaque colours (${a} / ${b})` });
        continue;
      }
      const r = ratio(a, b);
      if (r < min) out.push({ line: lineOf(fg), literal: `${fg} on ${bg} (${t}) ${r.toFixed(2)} < ${min}` });
    }
  }
  return out;
}

// ---- the allowlist and theme-ok ------------------------------------------

const globRe = (g) => new RegExp(`^${g.split('**').map((s) => s.split('*').map((t) => t.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('[^/]*')).join('.*')}$`);

export function readAllow(text) {
  const rules = [];
  text.split('\n').forEach((raw, i) => {
    const line = raw.trim();
    if (!line || line.startsWith('#')) return;
    const m = /^(\S+)\s+(\S+)\s+(\S.*)$/.exec(line);
    if (!m) throw new Error(`hack/theme-allow.txt:${i + 1}: want '<path-glob>  <checks>  <reason>'`);
    const checks = m[2] === '*' ? CHECKS : m[2].split(',');
    for (const c of checks) if (!CHECKS.includes(c)) throw new Error(`hack/theme-allow.txt:${i + 1}: unknown check ${c} (${CHECKS.join(', ')})`);
    rules.push({ glob: m[1], re: globRe(m[1]), checks: new Set(checks), reason: m[3] });
  });
  return rules;
}

const THEME_OK = /theme-ok:\s*\S/;

// ---- files ----------------------------------------------------------------

function* walk(dir, isGo) {
  let names;
  try { names = readdirSync(dir); } catch { return; }
  for (const name of names) {
    if (SKIP_DIRS.has(name) || name.startsWith('.')) continue;
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) { if (!isGo) yield* walk(p, isGo); continue; }
    if (wanted(p)) yield p;
  }
}
const wanted = (p) => {
  if (p.endsWith('.test.mjs') || p.endsWith('_test.go')) return false;
  if (extname(p) === '.go') return /(^|\/)internal\/server\/[^/]+\.go$/.test(p);
  return ['.js', '.mjs', '.html', '.css'].includes(extname(p));
};

export function* lintFiles(root, paths = []) {
  const inputs = paths.length ? paths.map((p) => resolve(p)) : TREES.map((t) => join(root, t));
  for (const p of inputs) {
    let st;
    try { st = statSync(p); } catch { continue; }
    if (st.isDirectory()) yield* walk(p, relative(root, p) === join('internal', 'server'));
    else if (wanted(p)) yield p;
  }
}

// ---- the run ---------------------------------------------------------------

// lint({root, paths, checks, css, allow}) → [{file, line, check, literal}]
export function lint({ root = ROOT, paths = [], checks = CHECKS, css, allow = [] } = {}) {
  css ??= readFileSync(join(root, 'web', 'theme.css'), 'utf8');
  const tokens = readTheme(css).tokens;
  const findings = [];
  const allowed = (file, check) => allow.some((r) => r.re.test(file) && r.checks.has(check));
  for (const abs of lintFiles(root, paths)) {
    const file = relative(root, abs);
    const src = readFileSync(abs, 'utf8');
    const raw = src.split('\n');
    const lines = strip(abs, src).split('\n');
    lines.forEach((line, i) => {
      if (!line.trim()) return;
      if (THEME_OK.test(raw[i]) || (i > 0 && THEME_OK.test(raw[i - 1]))) return;
      for (const check of checks) {
        if (!LINE_CHECKS[check] || allowed(file, check)) continue;
        for (const literal of LINE_CHECKS[check](line, tokens)) findings.push({ file, line: i + 1, check, literal });
      }
    });
  }
  if (checks.includes('contrast') && !allowed('web/theme.css', 'contrast')) {
    for (const f of contrastFindings(css)) findings.push({ file: 'web/theme.css', check: 'contrast', ...f });
  }
  return findings;
}

function main(argv) {
  const opt = (name) => { const i = argv.indexOf(name); return i >= 0 ? argv.splice(i, 2)[1] : undefined; };
  const checksArg = opt('--checks');
  const themeArg = opt('--theme');
  const checks = checksArg ? checksArg.split(',') : CHECKS;
  for (const c of checks) if (!CHECKS.includes(c)) { console.error(`theme-lint: unknown check ${c} (${CHECKS.join(', ')})`); process.exit(2); }
  const allow = readAllow(readFileSync(join(ROOT, 'hack', 'theme-allow.txt'), 'utf8'));
  const css = themeArg ? readFileSync(themeArg, 'utf8') : undefined;
  const findings = lint({ paths: argv.filter((a) => !a.startsWith('--')), checks, css, allow });
  for (const f of findings) console.error(`${f.file}:${f.line}: ${f.check}: ${f.literal}`);
  const by = Object.fromEntries(CHECKS.map((c) => [c, findings.filter((f) => f.check === c).length]));
  const counts = CHECKS.filter((c) => checks.includes(c)).map((c) => `${c} ${by[c]}`).join(', ');
  if (findings.length) {
    console.error(`theme-lint: ${findings.length} finding(s) (${counts}) — use the tokens (docs/frontend-kit.md §Theme), or give the line a reason: \`theme-ok: <why>\` or hack/theme-allow.txt (docs/maintenance.md)`);
    process.exit(1);
  }
  console.log(`theme-lint: no colour, radius, font, small type or emoji outside the tokens; the contrast pairs hold (${counts})`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) main(process.argv.slice(2));
