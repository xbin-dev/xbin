// hack/theme-icons.test.mjs — web/bx-icons.js, the icon set that replaces
// emoji and font-fallback glyphs (D184), run by `make js-test`: every name
// the plan's replacement table and the packages' hand-offs use exists, an
// alias draws its glyph, labels are escaped, an unknown name draws nothing
// (and says so once), and every glyph is well-formed SVG on the 16 px grid.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ICON_NAMES, hasIcon, iconSvg } from '../web/bx-icons.js';

// docs/frontend-kit.md §Theme's names: §1.6's table, the parts, the extras
// and the aliases the packages code against.
const NAMES = [
  'xmark', 'minimize', 'maximize', 'restore', 'popout', 'ellipsis', 'menu', 'settings', 'chevron-right', 'chevron-left',
  'chevron-up', 'chevron-down', 'caret-right', 'caret-down', 'plus', 'minus', 'terminal', 'code', 'list', 'diff', 'split',
  'deploy', 'refresh', 'branch', 'check', 'live', 'warning', 'error', 'info', 'ok', 'folder', 'doc', 'file', 'database',
  'host', 'vm', 'cpu', 'mail', 'lock', 'unlock', 'key', 'shield', 'pin', 'plug', 'globe', 'network', 'org', 'person',
  'people', 'eye', 'eye-slash', 'device', 'bell', 'bell-slash', 'save', 'link', 'box', 'photo', 'signal', 'clipboard',
  'wait', 'compact', 'calendar', 'trash', 'thought', 'paperclip', 'pencil', 'search', 'bolt', 'arrow-right', 'arrow-left',
  'window', 'agent', 'server', 'copy', 'download', 'upload', 'send', 'play', 'pause', 'filter', 'tag', 'home', 'chart',
  'clock', 'question', 'archive', 'chat', 'grip',
];
const ALIASES = {
  close: 'xmark', more: 'ellipsis', gear: 'settings', external: 'popout', logs: 'list', rack: 'server',
  danger: 'error', warn: 'warning', reload: 'refresh', edit: 'pencil', attach: 'paperclip',
};
const inner = (svg) => svg.replace(/^<svg[^>]*>/, '').replace(/<\/svg>$/, '');

test('every name the plan and the packages use exists', () => {
  for (const n of [...NAMES, ...Object.keys(ALIASES)]) assert.ok(hasIcon(n), `no glyph ${n}`);
  assert.ok(Object.isFrozen(ICON_NAMES));
  assert.deepEqual([...ICON_NAMES], [...new Set(ICON_NAMES)].sort(), 'sorted, no duplicates');
  for (const n of ICON_NAMES) assert.match(n, /^[a-z]+(-[a-z]+)*$/, `a name is lowercase words: ${n}`);
  for (const n of NAMES) assert.ok(ICON_NAMES.includes(n), `ICON_NAMES lists ${n}`);
});

test('an alias draws its glyph', () => {
  for (const [alias, name] of Object.entries(ALIASES)) assert.equal(inner(iconSvg(alias)), inner(iconSvg(name)), alias);
});

test('never drawn, by brand rule: faces, robots, brains, sparkles, stars, hearts', () => {
  for (const n of ['face', 'smile', 'robot', 'bot', 'brain', 'sparkle', 'sparkles', 'star', 'heart', 'emoji']) {
    assert.equal(hasIcon(n), false, n);
  }
});

test('the SVG: decorative by default, a named image with a label, sized, escaped', () => {
  const plain = iconSvg('lock');
  assert.match(plain, /^<svg xmlns="http:\/\/www\.w3\.org\/2000\/svg" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false" /);
  assert.match(plain, /stroke="currentColor" stroke-width="1.5" stroke-linecap="square" stroke-linejoin="miter"/);
  assert.doesNotMatch(plain, /role=|aria-label/);
  const big = iconSvg('lock', { size: 20, label: 'Locked' });
  assert.match(big, /width="20" height="20" role="img" aria-label="Locked" focusable/);
  assert.doesNotMatch(big, /aria-hidden/);
  const evil = iconSvg('lock', { label: '"><script>alert(1)</script>&' });
  assert.match(evil, /aria-label="&#34;&#62;&#60;script&#62;alert\(1\)&#60;\/script&#62;&#38;"/);
  assert.doesNotMatch(evil, /<script/);
  assert.equal(inner(evil), inner(plain), 'the label never reaches the drawing');
});

test('an unknown name draws nothing and warns once', () => {
  const warn = console.warn;
  const said = [];
  console.warn = (m) => said.push(m);
  try {
    assert.equal(hasIcon('no-such-glyph'), false);
    assert.equal(inner(iconSvg('no-such-glyph')), '');
    iconSvg('no-such-glyph');
    assert.equal(inner(iconSvg('<img src=x onerror=alert(1)>')), '');
    assert.equal(said.length, 2, 'once per name');
    assert.match(said[0], /no glyph named "no-such-glyph"/);
  } finally {
    console.warn = warn;
  }
});

test('every glyph is well-formed and stays on the 16 px grid', () => {
  for (const n of ICON_NAMES) {
    const body = inner(iconSvg(n));
    assert.ok(body.length > 0, `${n} draws something`);
    // only path and rect, each self-closed
    const tags = body.match(/<[^>]+>/g) ?? [];
    assert.equal(tags.join(''), body, `${n}: nothing but elements`);
    for (const t of tags) assert.match(t, /^<(path|rect) [^<>]*\/>$/, `${n}: ${t}`);
    // every coordinate inside the view box (a stroke's half-width aside)
    for (const t of tags) {
      const nums = t.startsWith('<rect')
        ? [...t.matchAll(/ (?:x|y|width|height)="([\d.]+)"/g)].map((m) => Number(m[1]))
        : (/ d="([^"]+)"/.exec(t)?.[1].match(/-?\d*\.?\d+/g) ?? []).map(Number);
      for (const v of nums) assert.ok(v >= -16 && v <= 16, `${n}: ${v} off the grid`);
    }
    if (n !== 'live' && !/^(caret|play|ellipsis|grip)/.test(n)) {
      assert.doesNotMatch(body, /fill="(?!currentColor)/, `${n}: fills are currentColor`);
    }
  }
});
