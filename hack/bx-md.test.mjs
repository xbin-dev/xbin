// hack/bx-md.test.mjs — web/bx-md.js, the hardened markdown renderer the
// Agent tab and screen text blocks use, run by `make js-test`: a closed
// fence in a known language is highlighted (#26) and still escaped; an
// open (streaming) fence, an unknown language, a fence past the size cap
// stay plain; raw HTML, images and unsafe links keep their policy.
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
const { md } = await import('../web/bx-md.js');

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
