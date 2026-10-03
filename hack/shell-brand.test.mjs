// hack/shell-brand.test.mjs — the shell's default brand draws xbin's mark and
// wordmark (D183) inline, from the brand's lockup: the b's, the exponent's
// and the word's paths and the tile's colours are /vendor/logo.svg's (which
// assets_test.go holds to the master), and the word sits where lockup A sets
// it. shell-brand.js imports lit (a bare specifier node can't resolve), so
// this reads its source. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (p) => readFileSync(new URL(`../${p}`, import.meta.url), 'utf8');
const shell = read('workspace-template/shell/shell-brand.js');
const logo = read('web/logo.svg');

const str = (name) => {
  const m = shell.match(new RegExp(`const ${name} = '([^']+)';`));
  assert.ok(m, `shell-brand.js has const ${name}`);
  return m[1];
};
const word = () => {
  const m = shell.match(/const WORD = \[([\s\S]*?)\]\.join\(' '\);/);
  assert.ok(m, 'shell-brand.js has the WORD glyphs');
  return [...m[1].matchAll(/'([^']+)'/g)].map((g) => g[1]).join(' ');
};

test('the paths are the lockup\'s', () => {
  const ds = [...logo.matchAll(/ d="([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual([str('B'), str('X'), word()], ds, 'the b, the exponent and the word, as logo.svg draws them');
});

test('the tile\'s colours are the lockup\'s', () => {
  const m = shell.match(/const COBALT = '(#[0-9A-F]{6})', WHITE = '(#[0-9A-F]{6})', YELLOW = '(#[0-9A-F]{6})';/);
  assert.ok(m, 'shell-brand.js names the three colours');
  assert.match(logo, new RegExp(`<rect width="1024" height="1024" fill="${m[1]}"/>`));
  assert.match(logo, new RegExp(`<path fill="${m[2]}" fill-rule="evenodd"`));
  assert.match(logo, new RegExp(`<path fill="${m[3]}" d=`));
});

test('the word sits where lockup A sets it', () => {
  const num = (s) => Number(s);
  const tile = logo.match(/<g transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)">/);
  const w = logo.match(/class="w"[^>]*transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)"/);
  assert.ok(tile && w, 'logo.svg places the tile and the word');
  const s = num(tile[3]);
  const want = [(num(w[1]) - num(tile[1])) / s, (num(w[2]) - num(tile[2])) / s, num(w[3]) / s];
  const got = shell.match(/class="m-w" fill="currentColor" transform="translate\(([\d.]+) ([\d.]+)\) scale\(([\d.]+)\)"/);
  assert.ok(got, 'the lockup\'s word has a transform');
  got.slice(1).map(num).forEach((g, i) => assert.ok(Math.abs(g - want[i]) <= 1e-3 * Math.max(1, want[i]), `value ${i}: ${g}, lockup A's ${want[i]}`));
  const vb = shell.match(/<svg class="lockup" viewBox="0 0 ([\d.]+) 1024"/);
  const end = want[0] + 1952.2 * want[2];
  assert.ok(vb && num(vb[1]) >= end && num(vb[1]) <= end + 2, `the viewBox ends at the word's last edge (${end})`);
});
