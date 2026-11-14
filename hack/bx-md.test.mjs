// hack/bx-md.test.mjs — web/bx-md.js, the hardened markdown renderer the
// Agent tab and screen text blocks use (web/chat/md.js with its highlighter
// loaded up front, D224), run by `make js-test`: a closed fence in a known
// language is highlighted (#26) in the first HTML md() returns and still
// escaped; an open (streaming) fence, an unknown language, a fence past the
// size cap stay plain; raw HTML, images and unsafe links keep their policy;
// the renderer's marked is its own (the shared one is neither changed nor
// listened to, and neither is the native runtime's lexer); mdInto renders a
// block at a time and remembers blocks in its memo.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { registerHooks } from 'node:module';

const web = new URL('../web/', import.meta.url);
registerHooks({
  resolve: (spec, ctx, next) => {
    if (spec.startsWith('/vendor/')) {
      const name = spec.slice('/vendor/'.length);
      for (const u of [new URL('vendor/' + name, web), new URL(name, web)]) if (existsSync(u)) return { url: u.href, shortCircuit: true };
    }
    return next(spec, ctx);
  },
});
const BX = await import('../web/bx-md.js');
const { md } = BX;

test('a closed fence in a known language is highlighted, escaped', () => {
  const h = md('```go\nfunc main() { s := "<b>" }\n```\n');
  assert.match(h, /<pre><code class="hljs language-go">/);
  assert.match(h, /<span class="hljs-keyword">func<\/span>/);
  assert.match(h, /&lt;b&gt;/);
  assert.doesNotMatch(h, /<b>/);
});

test('hostile code stays text', () => {
  const h = md('```js\n</code></pre><script>alert(1)</script><img src=x onerror=alert(2)>\n```');
  assert.doesNotMatch(h, /<script|<img|<\/code><\/pre><script/);
  assert.match(h, /&lt;<span class="hljs-name">script<\/span>&gt;/, 'the tags are text, coloured as tags');
});

test('plain where it should be: streaming, unknown, no language, past the cap', () => {
  assert.equal(md('```go\nfunc main() {'), '<pre><code class="language-go">func main() {</code></pre>\n', 'an open fence (still streaming)');
  assert.equal(md('```nosuchlang\nx < y\n```'), '<pre><code class="language-nosuchlang">x &lt; y</code></pre>\n');
  assert.equal(md('```\nx < y\n```'), '<pre><code>x &lt; y</code></pre>\n');
  assert.equal(md('```bad"lang\nx\n```'), '<pre><code>x</code></pre>\n', 'a language that is not a name is not an attribute');
  const big = 'let x = 1;\n'.repeat(4000);
  assert.doesNotMatch(md('```js\n' + big + '```'), /hljs-keyword/);
});

test('the policy holds: raw HTML escaped, images as text, unsafe links bare', () => {
  assert.match(md('<b>hi</b>'), /&lt;b&gt;hi&lt;\/b&gt;/);
  assert.match(md('![alt](https://x/y.png)'), /<span class="md-img">\[image: alt\]<\/span>/);
  assert.doesNotMatch(md('[x](javascript:alert(1))'), /href/);
  assert.match(md('[x](https://example.com)'), /target="_blank" rel="noopener noreferrer"/);
});

test('the exports: what it always had, and the chat kit\'s', async () => {
  for (const name of ['md', 'mdCssText', 'mdInto', 'closedFence', 'onHighlighter']) assert.equal(typeof BX[name], 'function', name);
  assert.equal(typeof BX.mdEpoch, 'number');
  const core = await import('/vendor/chat/md.js');
  for (const name of ['md', 'mdCssText', 'mdInto', 'closedFence', 'onHighlighter']) assert.equal(BX[name], core[name], `${name} is the chat kit's`);
  assert.match(BX.mdCssText('.md'), /\.md pre \{[^}]*var\(--bx-code-bg, #16171D\)/, 'the CSS, scoped, with Night\'s fallbacks');
});

test('the highlighter is here before the first render: nothing to draw again', async () => {
  let painted = 0;
  BX.onHighlighter(() => { painted++; });
  const epoch = BX.mdEpoch;
  assert.match(md('```js\nlet a = 1;\n```'), /hljs-keyword/);
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(painted, 0);
  assert.equal(BX.mdEpoch, epoch);
});

test('a fence is closed by its own kind, at least as long (the line-based check)', () => {
  assert.equal(BX.closedFence('```js\nlet a = 1;\n```'), true);
  assert.equal(BX.closedFence('````js\nlet a = 1;\n```'), false);
  assert.doesNotMatch(md('````js\nlet a = 1;\n```'), /hljs/, 'a shorter fence is code still being written: plain');
  assert.doesNotMatch(md('```js\nlet a = 1;\n~~~'), /hljs/, 'nor does another kind close it');
  assert.match(md('~~~~js\nlet a = 1;\n~~~~~'), /hljs-keyword/, 'a longer one of its kind does');
  assert.match(md('- a list\n\n  ```js\n  let a = 1;\n  ```'), /hljs-keyword/, 'a fence inside a list item');
});

test('its marked is its own: the shared one is not changed, and changing it changes nothing here', async () => {
  const shared = await import('/vendor/marked.esm.js');
  const rt = await import('/vendor/xb/rt-markdown.js');
  assert.equal(shared.marked.defaults.renderer, null, 'no renderer was installed on the shared marked');
  assert.equal(shared.marked.defaults.breaks, false);
  assert.match(shared.marked.parse('<b>hi</b>'), /<b>hi<\/b>/, 'the shared marked is still marked');
  const src = 'a line\nand its break, <b>raw</b>, [a link](https://example.com), ![an image](https://x/y.png)\n\n```go\nfunc main() {}\n```\n';
  const before = { html: md(src), tokens: JSON.stringify(rt.markdownTokens(src)) };
  // what an older agent template's page does to the shared marked, and worse
  shared.marked.use({ breaks: false, gfm: false, renderer: { html: () => '<i>shared</i>', code: () => 'shared', paragraph: () => 'shared' } });
  assert.equal(shared.marked.parse('text'), 'shared', 'the shared one did change');
  assert.equal(md(src), before.html);
  assert.equal(JSON.stringify(rt.markdownTokens(src)), before.tokens, 'nor do the native runtime\'s tokens');
  assert.match(before.html, /a line<br>and its break, &lt;b&gt;raw&lt;\/b&gt;/);
});

// the least of a DOM mdInto touches
class El {
  constructor() { this.children = []; this.innerHTML = ''; this.className = ''; this.parent = null; }
  replaceChildren(...nodes) { this.children.length = 0; for (const n of nodes) this.appendChild(n); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  get lastElementChild() { return this.children[this.children.length - 1]; }
  remove() { this.parent.children.splice(this.parent.children.indexOf(this), 1); }
}

test('mdInto: a block at a time, each block md()\'s HTML; the memo spares the parse', (t) => {
  globalThis.document = { createElement: () => new El(), createTextNode: (text) => ({ text }) };
  t.after(() => { delete globalThis.document; });
  const el = new El();
  const memo = {};
  BX.mdInto(el, 'one\n\n```js\nlet a = 1;\n```\n\nthr', memo);
  assert.deepEqual(el.children.map((c) => c.className), ['md-b', 'md-b', 'md-b']);
  assert.equal(el.children.map((c) => c.innerHTML).join(''), md('one\n\n```js\nlet a = 1;\n```\n\nthr'));
  const [first, code] = el.children;
  const parts = memo.parts.slice();
  code.innerHTML = 'kept'; // a block whose source stands is not written again
  BX.mdInto(el, 'one\n\n```js\nlet a = 1;\n```\n\nthree', memo);
  assert.equal(el.children[0], first);
  assert.equal(el.children[1].innerHTML, 'kept');
  assert.equal(el.children[2].innerHTML, '<p>three</p>\n');
  assert.equal(memo.parts[0], parts[0], 'an unchanged block is the memo\'s');
  assert.equal(memo.parts[1], parts[1]);
  assert.notEqual(memo.parts[2], parts[2]);
  // a row that left the window and came back: a new element, the same memo — no parse
  const back = new El();
  const kept = memo.parts.slice();
  BX.mdInto(back, 'one\n\n```js\nlet a = 1;\n```\n\nthree', memo);
  assert.deepEqual(memo.parts, kept);
  assert.ok(memo.parts.every((p, i) => p === kept[i]));
  assert.match(back.children[1].innerHTML, /hljs-keyword/);
  BX.mdInto(back, 'one', memo);
  assert.equal(back.children.length, 1, 'blocks that are gone are removed');
  BX.mdInto(back, 'one'); // no memo: as the agent template calls it
  assert.equal(back.children.length, 1);
});
