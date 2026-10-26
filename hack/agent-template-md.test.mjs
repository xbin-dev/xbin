// hack/agent-template-md.test.mjs — the agent template's markdown
// (builtin-templates/agent/chat-md.js): fenced code is highlighted by xbind's
// /vendor/highlight.min.js once it is imported (#26) — only a closed fence
// with a language hljs knows, its output escaped — and what was parsed
// before it arrived is parsed again (mdEpoch, onHighlighter). Run by `make
// js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerHooks } from 'node:module';

const WEB = new URL('../web/', import.meta.url);
const MAP = {
  '/vendor/bx-kit.js': new URL('bx-kit.js', WEB).href,
  '/vendor/marked.esm.js': new URL('vendor/marked.esm.js', WEB).href,
  '/vendor/highlight.min.js': new URL('vendor/highlight.min.js', WEB).href,
};
registerHooks({ resolve: (spec, ctx, next) => (MAP[spec] ? { url: MAP[spec], shortCircuit: true } : next(spec, ctx)) });
const M = await import(new URL('../builtin-templates/agent/chat-md.js', import.meta.url));

test('a closed fence is one whose source ends with its closing fence', () => {
  assert.equal(M.closedFence('```js\nlet a = 1;\n```'), true);
  assert.equal(M.closedFence('```js\nlet a = 1;\n```\n\n'), true);
  assert.equal(M.closedFence('~~~~py\nx\n~~~~~'), true);
  assert.equal(M.closedFence('```js\nlet a = 1;\n'), false, 'still streaming');
  assert.equal(M.closedFence('````js\nx\n```'), false, 'a shorter fence does not close it');
  assert.equal(M.closedFence('```js\nx\n~~~'), false, 'nor another kind');
  assert.equal(M.closedFence('    indented'), false);
});

test('fenced code is highlighted once the highlighter arrives; streaming, unknown and plain code stay plain', async () => {
  const src = '```js\nconst x = "<b>" + 1; // hi\n```';
  let painted = 0;
  M.onHighlighter(() => { painted++; });
  const before = M.md(src); // asks for the highlighter
  assert.match(before, /<code class="language-js">/);
  for (let i = 0; i < 100 && M.mdEpoch === 0; i++) await new Promise((r) => setTimeout(r, 10));
  assert.equal(M.mdEpoch, 1, 'the highlighter arrived');
  assert.equal(painted, 1, 'and a paint was asked for');
  const after = M.md(src);
  assert.match(after, /<code class="hljs language-js"><span class="hljs-keyword">const<\/span>/);
  assert.match(after, /&quot;&lt;b&gt;&quot;|"&lt;b&gt;"/, 'escaped');
  assert.doesNotMatch(after, /<b>/);
  assert.match(M.md('```js\nconst x = 1;\n'), /<code class="language-js">const x = 1;/, 'an open fence (streaming) stays plain');
  assert.match(M.md('```nosuchlang\nx\n```'), /<code class="language-nosuchlang">x/);
  assert.match(M.md('```\n<script>\n```'), /<code>&lt;script&gt;/);
  assert.match(M.md('```js\n' + 'x;'.repeat(12000) + '\n```'), /<code class="language-js">x;/, 'past the cap: plain');
});
