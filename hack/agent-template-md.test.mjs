// hack/agent-template-md.test.mjs — the agent template's markdown
// (builtin-templates/agent/chat-md.js): xbind's renderer, /vendor/chat/md.js
// (D224), under the template's own file name. Fenced code is highlighted by
// xbind's /vendor/highlight.min.js once the renderer has imported it (#26)
// — only a closed fence with a language hljs knows, its output escaped —
// and what was parsed before it arrived is parsed again (mdEpoch,
// onHighlighter, mdInto's blocks and memo). What the renderer unified for
// the template: the size cap, a language's class, an image's class. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { registerHooks } from 'node:module';

// /vendor/<name> as xbind serves it: web/<name>, else web/vendor/<name>
const WEB = new URL('../web/', import.meta.url);
registerHooks({
  resolve: (spec, ctx, next) => {
    if (spec.startsWith('/vendor/')) {
      const name = spec.slice('/vendor/'.length);
      for (const u of [new URL(name, WEB), new URL('vendor/' + name, WEB)]) if (existsSync(u)) return { url: u.href, shortCircuit: true };
    }
    return next(spec, ctx);
  },
});
const M = await import(new URL('../builtin-templates/agent/chat-md.js', import.meta.url));

// the least of a DOM mdInto touches
class El {
  constructor() { this.children = []; this.innerHTML = ''; this.className = ''; this.parent = null; }
  replaceChildren(...nodes) { this.children.length = 0; for (const n of nodes) this.appendChild(n); }
  appendChild(c) { c.parent = this; this.children.push(c); return c; }
  get lastElementChild() { return this.children[this.children.length - 1]; }
  remove() { this.parent.children.splice(this.parent.children.indexOf(this), 1); }
}

test('the file keeps its exports', () => {
  assert.deepEqual(Object.keys(M).sort(), ['closedFence', 'md', 'mdEpoch', 'mdInto', 'onHighlighter']);
});

test('a closed fence is one whose source ends with its closing fence', () => {
  assert.equal(M.closedFence('```js\nlet a = 1;\n```'), true);
  assert.equal(M.closedFence('```js\nlet a = 1;\n```\n\n'), true);
  assert.equal(M.closedFence('~~~~py\nx\n~~~~~'), true);
  assert.equal(M.closedFence('```js\nlet a = 1;\n'), false, 'still streaming');
  assert.equal(M.closedFence('````js\nx\n```'), false, 'a shorter fence does not close it');
  assert.equal(M.closedFence('```js\nx\n~~~'), false, 'nor another kind');
  assert.equal(M.closedFence('    indented'), false);
});

test('fenced code is highlighted once the highlighter arrives; streaming, unknown and plain code stay plain', async (t) => {
  globalThis.document = { createElement: () => new El(), createTextNode: (text) => ({ text }) };
  t.after(() => { delete globalThis.document; });
  const src = '```js\nconst x = "<b>" + 1; // hi\n```';
  let painted = 0;
  M.onHighlighter(() => { painted++; });
  assert.equal(M.mdEpoch, 0, 'no highlighter is imported before a fence asks');
  const before = M.md(src); // asks for the highlighter
  assert.match(before, /<code class="language-js">/);
  // a streamed answer and a windowed row's memo, both drawn before it arrived
  const el = new El();
  const memo = {};
  M.mdInto(el, `text\n\n${src}`, memo);
  assert.match(el.children[1].innerHTML, /<code class="language-js">const x/);
  for (let i = 0; i < 100 && M.mdEpoch === 0; i++) await new Promise((r) => setTimeout(r, 10));
  assert.equal(M.mdEpoch, 1, 'the highlighter arrived');
  assert.equal(painted, 1, 'and a paint was asked for');
  const after = M.md(src);
  assert.match(after, /<code class="hljs language-js"><span class="hljs-keyword">const<\/span>/);
  assert.match(after, /&quot;&lt;b&gt;&quot;|"&lt;b&gt;"/, 'escaped');
  assert.doesNotMatch(after, /<b>/);
  M.mdInto(el, `text\n\n${src}`, memo); // the same text, at the next paint
  assert.match(el.children[1].innerHTML, /<code class="hljs language-js">/, 'mdInto draws its blocks again');
  assert.match(memo.parts[1].html, /hljs-keyword/, 'and the memo holds no block of the old epoch');
  assert.match(M.md('```js\nconst x = 1;\n'), /<code class="language-js">const x = 1;/, 'an open fence (streaming) stays plain');
  assert.match(M.md('```nosuchlang\nx\n```'), /<code class="language-nosuchlang">x/);
  assert.match(M.md('```\n<script>\n```'), /<code>&lt;script&gt;/);
});

test('what the one renderer decided for the template: the cap, a language\'s class, an image\'s class', () => {
  // the size cap is the renderer's 32 KiB (the template's own was 20000 characters)
  assert.match(M.md('```js\n' + 'x;'.repeat(12000) + '\n```'), /<code class="hljs language-js">/, '24000 characters: highlighted');
  assert.match(M.md('```js\n' + 'x;'.repeat(17000) + '\n```'), /<code class="language-js">x;/, 'past 32 KiB: plain');
  // a language that is not a name is no attribute at all (it was escaped into one)
  assert.equal(M.md('```bad"lang\nx\n```'), '<pre><code>x</code></pre>\n');
  assert.equal(M.md('```c++\nx\n```').replace(/<span[^>]*>|<\/span>/g, ''), '<pre><code class="hljs language-c++">x</code></pre>\n');
  // an image is its text under the renderer's class (index.html colours .md-img as it did .muted)
  assert.equal(M.md('![alt](https://x/y.png)'), '<p><span class="md-img">[image: alt]</span></p>\n');
  assert.match(M.md('<img src=x onerror=alert(1)>'), /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.doesNotMatch(M.md('[x](javascript:alert(1))'), /href/);
});

test('it is xbind\'s renderer: the Agent tab\'s module is the same functions and the same epoch', async () => {
  const bx = await import('/vendor/bx-md.js');
  assert.equal(bx.md, M.md);
  assert.equal(bx.mdInto, M.mdInto);
  assert.equal(bx.closedFence, M.closedFence);
  assert.equal(bx.mdEpoch, M.mdEpoch);
});
