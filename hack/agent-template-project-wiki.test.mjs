// hack/agent-template-project-wiki.test.mjs — the agent template's project
// wiki model (builtin-templates/agent/model/project-wiki.js): its words,
// the revision diff, the links, and the store's compare-and-swap edit (a
// 409 keeps the editor with the conflict's words; Reload takes the page as
// it is now and keeps what was typed) over a stubbed xbin.fetch. Run by
// `make js-test`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { lineDiff, diffCount, statsWords, pageWords, wikiLinks, linkedSlugs, plainLinks, slugOf, SLUG, kb, projectWiki }
  from '../builtin-templates/agent/model/project-wiki.js';

const CONFLICT = 'Another agent changed the contents of this article since you last read it — read the article again before editing.';

test('lineDiff keeps the common lines and marks what was added and removed', () => {
  const d = lineDiff('a\nb\nc\nd', 'a\nc\nx\nd');
  assert.deepEqual(d.map((l) => l.op + l.text), [' a', '-b', ' c', '+x', ' d']);
  assert.deepEqual(diffCount(d), { add: 1, del: 1 });
  assert.deepEqual(lineDiff('', 'one').map((l) => l.op + l.text), ['-', '+one']);
  assert.deepEqual(diffCount(lineDiff('same\ntext', 'same\ntext')), { add: 0, del: 0 });
});

test('[[slug]] links: made links in rendered HTML, listed, and plain for a renderer of web links only', () => {
  assert.equal(wikiLinks('<p>see [[build-and-test]] and [[Bad Slug]]</p>'),
    '<p>see <a class="wlink" data-slug="build-and-test" href="#" role="link">build-and-test</a> and [[Bad Slug]]</p>');
  assert.equal(wikiLinks('[[x"onmouseover=1]]'), '[[x"onmouseover=1]]'); // nothing but a slug becomes markup
  assert.deepEqual(linkedSlugs('[[a]] then [[b-2]] and [[a]] again'), ['a', 'b-2']);
  assert.equal(plainLinks('see [[build]]'), 'see ‹build›');
});

test('slugs and sizes', () => {
  assert.equal(slugOf('Build & Test: the CI!'), 'build-test-the-ci');
  assert.ok(SLUG.test('index') && SLUG.test('a-b-1') && !SLUG.test('-a') && !SLUG.test('A') && !SLUG.test('a_b'));
  assert.equal(kb(512), '512 B');
  assert.equal(kb(1536), '1.5 KB');
  assert.equal(kb(20480), '20 KB');
});

test('the footer says size, reads per page, versions and what dream rounds cost', () => {
  assert.equal(statsWords({ pages: 4, bytes: 6144, reads: 10, readers: 3, revisions: 9, dreams: 2, dreamTokens: 12400 }),
    '4 pages · 6.0 KB · 10 reads (2.5 a page) · by 3 runs · 9 versions · 2 dream rounds, 12k tokens');
  assert.equal(statsWords({ pages: 1, bytes: 10, reads: 1 }), '1 page · 10 B · 1 read (1.0 a page)');
  assert.equal(statsWords(null), '');
  const now = Date.UTC(2026, 10, 8, 12);
  assert.equal(pageWords({ slug: 'index', title: 'Index', version: 3, size: 900, updatedBy: 'coordinator (alice)', updatedMs: now - 2 * 3600e3, reads: 7 }, now).meta,
    'v3 · 900 B · by coordinator (alice) · 2h ago · 7 reads');
});

// a stubbed backend: one page, whose version moves under the person's edit
function stubApp() {
  const page = { slug: 'build', title: 'Build', body: 'make', version: 1, updatedBy: 'alice', updatedMs: 1 };
  const calls = [];
  globalThis.xbin = {
    self: 'agent',
    fetch: async (url, opts = {}) => {
      const u = new URL(url, 'http://x');
      const method = opts.method || 'GET';
      calls.push(`${method} ${u.pathname}${u.search}`);
      const json = (status, body) => ({ ok: status < 300, status, text: async () => JSON.stringify(body) });
      if (u.pathname.endsWith('/wiki') && method === 'GET') return json(200, { pages: [{ ...page, body: undefined, size: page.body.length }], stats: { pages: 1, bytes: 4, reads: 0 }, dream: true });
      if (u.pathname.endsWith('/wiki/build') && method === 'GET') return json(200, { page });
      if (u.pathname.endsWith('/wiki/build') && method === 'PUT') {
        const b = JSON.parse(opts.body);
        if (b.baseVersion !== page.version) return json(409, { error: CONFLICT, refusal: 'conflict', current: page.version });
        Object.assign(page, { body: b.body, title: b.title, version: page.version + 1, updatedBy: 'alice' });
        return json(200, { page });
      }
      return json(404, { error: 'no' });
    },
  };
  const events = [];
  const app = { emit: (e) => events.push(e), projects: { take() {} } };
  return { app, page, calls, events };
}

test('an edit saves at the version it began at; a conflict keeps the editor, Reload takes the page as it is now', async () => {
  const { app, page, calls } = stubApp();
  const w = projectWiki(app);
  await w.load(7);
  const s = w.state(7);
  assert.equal(s.slug, 'build'); // the first page opens
  assert.equal(s.page.version, 1);
  w.edit(7);
  w.set(7, 'body', 'make all');
  page.version = 2; // someone wrote meanwhile
  page.body = 'make check';
  assert.equal(await w.save(7), false);
  assert.equal(s.edit.conflict, CONFLICT);
  assert.equal(s.edit.current, 2);
  assert.ok(calls.includes('PUT /api/agent/projects/7/wiki/build'));
  await w.reload(7);
  assert.equal(s.edit.base, 2);
  assert.equal(s.edit.body, 'make check');
  assert.equal(s.edit.mine, 'make all');
  assert.equal(s.edit.conflict, '');
  w.set(7, 'body', 'make check all');
  assert.equal(await w.save(7), true);
  assert.equal(s.edit, null);
  assert.equal(page.version, 3);
  assert.equal(page.body, 'make check all');
});

test('a new page needs a slug and text; its slug follows its title until typed', () => {
  const { app } = stubApp();
  const w = projectWiki(app);
  w.edit(9, null);
  const s = w.state(9);
  w.set(9, 'title', 'Build and Test');
  assert.equal(s.edit.slug, 'build-and-test');
  w.set(9, 'slug', 'bt');
  w.set(9, 'title', 'Something else');
  assert.equal(s.edit.slug, 'bt');
  return w.save(9).then((ok) => { assert.equal(ok, false); assert.match(s.edit.err, /some text/); });
});
