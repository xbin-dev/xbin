// hack/term-links.test.mjs — <bx-terminal>'s joined-URL links
// (web/term-links.js joinedLinksAt, D178): a URL a program broke over rows
// with real line breaks is one link on every row it spans. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { joinedLinksAt, linkTrust, confirmWords, parseOsc52, openLink } from '../web/term-links.js';

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

// --- the security review of D178: what a click opens, OSC 52 reads ---

// An OSC 8 link opens at once only when its row shows part of its own
// target (Ink draws a long URL as one link per row, each pointing at the
// whole URL); one that shows something else asks first, naming where it
// goes; one with a user part, or not http(s), never opens.
test('linkTrust: a TUI\'s own URL opens; a link that shows something else asks; userinfo never opens', () => {
  const rows = [URL_.slice(0, 80), URL_.slice(80)];
  for (const r of rows) assert.equal(linkTrust(URL_, r), 'open', `Ink's row ${JSON.stringify(r)}`);
  assert.equal(linkTrust(URL_, `  ${rows[1]}  `), 'open', 'its padding is not text');
  // a link that shows a trusted URL and points elsewhere
  assert.equal(linkTrust('https://evil.example/steal', 'https://login.xbin.dev/ok'), 'confirm');
  // the shown host is in the target's query, not its host
  assert.equal(linkTrust('https://evil.example/?r=https://login.xbin.dev/ok', 'https://login.xbin.dev/ok'), 'confirm');
  assert.equal(linkTrust('https://evil.example/?login.xbin.dev', 'login.xbin.dev'), 'confirm', 'a bare host it shows must be its host');
  assert.equal(linkTrust('https://login.xbin.dev/a', 'login.xbin.dev/a'), 'open');
  assert.equal(linkTrust('https://evil.example/x', ''), 'confirm', 'it shows nothing');
  assert.equal(linkTrust('https://evil.example/x', 'click here'), 'confirm', 'words, not its URL');
  assert.equal(linkTrust('https://claude.ai@evil.example/x', 'https://claude.ai@evil.example/x'), 'refuse', 'a user part');
  assert.equal(linkTrust('https://u:p@claude.ai/', 'https://u:p@claude.ai/'), 'refuse');
  assert.equal(linkTrust('javascript:alert(1)', 'javascript:alert(1)'), 'refuse');
  assert.equal(linkTrust('file:///etc/passwd', 'x'), 'refuse');
  const words = confirmWords('https://evil.example/steal', 'https://login.xbin.dev/ok');
  assert.match(words, /goes to evil\.example/);
  assert.match(words, /It shows: https:\/\/login\.xbin\.dev\/ok/);
  assert.match(words, /It opens: https:\/\/evil\.example\/steal/);
});

test('openLink: never a user part, never another scheme', () => {
  const opened = [];
  globalThis.window = { open: (u) => opened.push(u) };
  try {
    assert.equal(openLink('https://claude.ai@evil.example/'), false);
    assert.equal(openLink('https://:pw@claude.ai/'), false);
    assert.equal(openLink('ftp://claude.ai/'), false);
    assert.equal(openLink('https://claude.ai/ok'), true);
    assert.deepEqual(opened, ['https://claude.ai/ok']);
  } finally { delete globalThis.window; }
});

// Rows joined across a hard break can make a user part out of two hosts:
// `https://claude.ai` at the edge, `@evil.com` on the next row. The link
// is found (it is what the rows show) and never opens.
test('a joined URL with a user part is never opened', () => {
  const cols = 17;
  const rows = ['https://claude.ai', '@evil.com/x'];
  const ls = joinedLinksAt(grid(rows), cols, 0);
  assert.equal(ls.length, 1);
  assert.equal(ls[0].text, 'https://claude.ai@evil.com/x');
  assert.equal(linkTrust(ls[0].text, ls[0].text), 'refuse');
});

// OSC 52: a read (`?`) is recognised — and never answered; a write decodes
// UTF-8 base64, bounded; bad base64 or bytes give no text.
test('parseOsc52: reads are reads, writes decode, bounds hold', () => {
  assert.deepEqual(parseOsc52('c;?'), { read: true, sel: 'c' });
  assert.deepEqual(parseOsc52(';?'), { read: true, sel: '' });
  const b64 = Buffer.from('héllo — copy').toString('base64');
  assert.deepEqual(parseOsc52(`c;${b64}`), { sel: 'c', text: 'héllo — copy' });
  assert.deepEqual(parseOsc52('c;!!notbase64'), { sel: 'c', text: null });
  assert.deepEqual(parseOsc52(`c;${Buffer.from([0xff, 0xfe]).toString('base64')}`), { sel: 'c', text: null }, 'not UTF-8');
  assert.equal(parseOsc52('no-separator'), null);
  const big = Buffer.alloc(64, 0x61).toString('base64');
  assert.deepEqual(parseOsc52(`c;${big}`, 32), { sel: 'c', text: null }, 'over the bound');
  assert.equal(parseOsc52(`c;${big}`, 64).text.length, 64);
});
