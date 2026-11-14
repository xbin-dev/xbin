// hack/chat-md-nohl.test.mjs — web/chat/md.js when its highlighter never
// arrives (/vendor/highlight.min.js fails to load): code stays plain, the
// renderer keeps working, no epoch moves and no paint is asked for. Its own
// file because a process imports the highlighter once. Run by `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { registerHooks } from 'node:module';

const WEB = new URL('../web/', import.meta.url);
let asked = 0;
registerHooks({
  resolve: (spec, ctx, next) => {
    if (spec === '/vendor/highlight.min.js') { asked++; return { url: new URL('vendor/no-such-highlighter.js', WEB).href, shortCircuit: true }; }
    if (spec.startsWith('/vendor/')) {
      const name = spec.slice('/vendor/'.length);
      for (const u of [new URL(name, WEB), new URL('vendor/' + name, WEB)]) if (existsSync(u)) return { url: u.href, shortCircuit: true };
    }
    return next(spec, ctx);
  },
});
const M = await import('/vendor/chat/md.js');

test('without a highlighter code is plain and the renderer works', async () => {
  const stray = [];
  const onStray = (e) => stray.push(e);
  process.on('unhandledRejection', onStray);
  let painted = 0;
  M.onHighlighter(() => { painted++; });
  const src = 'A plan:\n\n```js\nconst x = "<b>";\n```';
  const want = '<p>A plan:</p>\n<pre><code class="language-js">const x = &quot;&lt;b&gt;&quot;;</code></pre>\n';
  assert.equal(M.md(src), want);
  assert.equal(asked, 1, 'the first fence asked for it');
  await new Promise((r) => setTimeout(r, 100));
  assert.equal(M.md(src), want, 'still plain, still rendering');
  assert.equal(asked, 1, 'and it is asked for once');
  assert.equal(M.mdEpoch, 0);
  assert.equal(painted, 0);
  process.off('unhandledRejection', onStray);
  assert.deepEqual(stray, [], 'the failed import is nobody\'s uncaught error');
});

test('a highlighter handed over later is taken: the epoch moves and a paint is asked for', () => {
  let painted = 0;
  M.onHighlighter(() => { painted++; });
  M.useHighlighter({ nope: true }); // not a highlighter: ignored
  assert.equal(M.mdEpoch, 0);
  const fake = { getLanguage: (l) => l === 'js', highlight: (code) => ({ value: `<span class="hljs-x">${code.length}</span>` }) };
  M.useHighlighter(fake);
  assert.equal(M.mdEpoch, 1);
  assert.equal(painted, 1);
  M.useHighlighter(fake); // the same one again: nothing moves
  assert.equal(M.mdEpoch, 1);
  assert.equal(M.md('```js\nabc\n```'), '<pre><code class="hljs language-js"><span class="hljs-x">3</span></code></pre>\n');
  assert.equal(M.md('```py\nabc\n```'), '<pre><code class="language-py">abc</code></pre>\n');
});
