// hack/term-links.test.mjs — <bx-terminal>'s joined-URL links
// (web/term-links.js joinedLinksAt, D178): a URL a program broke over rows
// with real line breaks is one link on every row it spans. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { joinedLinksAt } from '../web/term-links.js';

// rows as a terminal of `cols` columns holds them: one cell per character
const grid = (lines, wrapped = []) => (j) => (j < lines.length ? { text: lines[j], xs: [...lines[j]].map((_, k) => k), wrapped: wrapped.includes(j) } : null);

const URL_ = 'https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&state=uAm9cA2Cb8PYSBVQ';

test('a URL hard-wrapped at the width is one link from any of its rows', () => {
  const cols = 40;
  const rows = ['Browser didn\'t open? Use the url below', ''];
  for (let k = 0; k < URL_.length; k += cols) rows.push(URL_.slice(k, k + cols));
  rows.push('', 'Paste code here if prompted >');
  const at = grid(rows);
  const first = 2, last = 2 + Math.ceil(URL_.length / cols) - 1;
  for (let i = first; i <= last; i++) {
    const ls = joinedLinksAt(at, cols, i);
    assert.equal(ls.length, 1, `row ${i}`);
    assert.equal(ls[0].text, URL_);
    assert.deepEqual(ls[0].start, { x: 1, y: first + 1 });
    assert.deepEqual(ls[0].end, { x: (URL_.length - 1) % cols + 1, y: last + 1 });
  }
  assert.deepEqual(joinedLinksAt(at, cols, 0), [], 'the line before');
  assert.deepEqual(joinedLinksAt(at, cols, last + 2), [], 'the prompt after');
});

test('indentation on the continued rows is not part of the URL', () => {
  const cols = 30;
  const rows = ['  ' + URL_.slice(0, 28), '  ' + URL_.slice(28, 56), '  ' + URL_.slice(56)];
  const ls = joinedLinksAt(grid(rows), cols, 1);
  assert.equal(ls.length, 1);
  assert.equal(ls[0].text, URL_);
  assert.deepEqual(ls[0].start, { x: 3, y: 1 });
});

test('a URL after a prompt, running to the edge, joins the rows after it', () => {
  const cols = 50, head = 'visit: ';
  const rows = [head + URL_.slice(0, cols - head.length), URL_.slice(cols - head.length)];
  const ls = joinedLinksAt(grid(rows), cols, 1);
  assert.equal(ls.length, 1);
  assert.equal(ls[0].text, URL_);
  assert.deepEqual(ls[0].start, { x: head.length + 1, y: 1 });
});

test('left to the web-links addon: one row, a soft wrap, a short row, prose after', () => {
  const cols = 40;
  assert.deepEqual(joinedLinksAt(grid(['see https://example.com/x for more']), cols, 0), [], 'one row');
  // the terminal's own wrap (isWrapped) only: no real break inside the URL
  const soft = [URL_.slice(0, 40), URL_.slice(40, 80), URL_.slice(80)];
  assert.deepEqual(joinedLinksAt(grid(soft, [1, 2]), cols, 1), [], 'soft-wrapped');
  // a URL that ends before the edge doesn't continue
  assert.deepEqual(joinedLinksAt(grid(['https://example.com/abc', 'def']), cols, 0), [], 'short row');
  // the next row has a space: prose, not the URL's tail
  assert.deepEqual(joinedLinksAt(grid([URL_.slice(0, 40), 'and then']), cols, 0), [], 'prose');
});

test('a soft wrap and a hard break in one URL: joined', () => {
  const cols = 40;
  const rows = [URL_.slice(0, 40), URL_.slice(40, 80), URL_.slice(80)];
  const ls = joinedLinksAt(grid(rows, [1]), cols, 2);
  assert.equal(ls.length, 1);
  assert.equal(ls[0].text, URL_);
});
